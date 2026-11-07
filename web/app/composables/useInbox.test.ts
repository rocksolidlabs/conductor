import { describe, expect, it } from 'vitest'
import { InboxLive, type InboxReads, type InboxState } from './useInbox'
import { inboxEmpty, RECENT_KEY, trackKey, UNCONFIRMED, type DeliveryStep, type InboxPage, type InboxSummary, type RecentEntry, type StepState } from '../utils/inbox'

// The live inbox (docs/plans/decision-inbox.md, "Events", PR-2): one read in flight and one queued, read again on the events, the
// snapshot and the stream's return, and the tracks of recent actions not yet final read with the list.

const summary = (ref: string, since = '2026-11-06T10:00:00Z'): InboxSummary => ({ ref, kind: ref.slice(0, ref.indexOf(':')), since, title: ref, brief: '', question: '', answerer: 'you, the operator', actions: ['Retry'] })
const steps = (...states: StepState[]): DeliveryStep[] => (['available', 'recorded', 'validated', 'resumed'] as const).map((step, i) => ({ step, state: states[i]!, words: '' }))

/** A server the test moves: its open items and each receipt's steps; reads it holds until let go. */
function fakeServer() {
  const open = new Map<string, InboxSummary>()
  const tracks = new Map<string, DeliveryStep[]>()
  const held: Array<() => void> = []
  let hold = false
  let lists = 0
  let trackReads = 0
  const later = <T>(v: () => T): Promise<T> => (hold ? new Promise((done) => held.push(() => done(v()))) : Promise.resolve(v()))
  const reads: InboxReads = {
    list: () => {
      lists++
      return later((): InboxPage => ({ items: [...open.values()], total: open.size, at: '' }))
    },
    track: (e) => {
      trackReads++
      return later(() => ({ ref: e.ref, receipt: e.receipt, steps: tracks.get(e.receipt) ?? steps('done', 'unknown', 'unknown', 'unknown') }))
    },
  }
  return {
    open,
    tracks,
    reads,
    lists: () => lists,
    trackReads: () => trackReads,
    holdReads: (on: boolean) => (hold = on),
    release: async () => {
      while (held.length) held.shift()!()
      await settle()
    },
  }
}
const settle = () => new Promise((r) => setTimeout(r, 0))

function storage() {
  const m = new Map<string, string>()
  return { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v) }
}

function liveOn(srv: ReturnType<typeof fakeServer>, store = storage()) {
  let state: InboxState | null = null
  const l = new InboxLive()
  l.attach(srv.reads, { storage: () => store, apply: (s) => (state = s) })
  return { l, state: () => state!, store }
}

describe('the live inbox', () => {
  it('reads once in flight and once queued over a burst of 20 events', async () => {
    const srv = fakeServer()
    const { l, state } = liveOn(srv)
    srv.holdReads(true)
    const first = l.event()
    for (let i = 0; i < 20; i++) void l.event()
    srv.open.set('task:r/build', summary('task:r/build'))
    // The first read is in flight; the twenty after it are one read queued behind it.
    expect(srv.lists()).toBe(1)
    await srv.release()
    await srv.release()
    await first
    expect(srv.lists()).toBe(2)
    expect(state().items.map((i) => i.ref)).toEqual(['task:r/build'])
  })

  it('reads the list again on a snapshot and on a removed session', async () => {
    const srv = fakeServer()
    const { l, state } = liveOn(srv)
    srv.open.set('question:s1/q1', summary('question:s1/q1'))
    await l.snapshot()
    expect(state().items.map((i) => i.ref)).toEqual(['question:s1/q1'])
    srv.open.delete('question:s1/q1')
    await l.event() // `removed`: the session left
    expect(state().items).toEqual([])
    expect(state().total).toBe(0)
    expect(srv.lists()).toBe(2)
  })

  it("shows, once the stream is back, the question answered while it was down gone and the one asked meanwhile there", async () => {
    const srv = fakeServer()
    const { l, state } = liveOn(srv)
    srv.open.set('question:s1/q1', summary('question:s1/q1'))
    await l.snapshot()
    expect(state().items.map((i) => i.ref)).toEqual(['question:s1/q1'])
    // The stream is down: no event comes. On the server the question is answered and another asked.
    srv.open.delete('question:s1/q1')
    srv.open.set('question:s1/q2', summary('question:s1/q2', '2026-11-06T10:05:00Z'))
    expect(state().items.map((i) => i.ref)).toEqual(['question:s1/q1'])
    // Back: the stream's snapshot.
    await l.snapshot()
    expect(state().items.map((i) => i.ref)).toEqual(['question:s1/q2'])
  })

  it("reads again, on a run event that leaves the open list unchanged, the track of a retry still waiting, and shows it resumed", async () => {
    const srv = fakeServer()
    const store = storage()
    const e: RecentEntry = { ref: 'task:r/build', receipt: 'c0ffee00c0ffee00', at: Date.now() }
    store.setItem(RECENT_KEY, JSON.stringify([e]))
    const { l, state } = liveOn(srv, store)
    srv.tracks.set(e.receipt, steps('done', 'done', 'done', 'waiting'))
    const stop = l.track()
    await l.changed()
    expect(state().tracks[trackKey(e)]!.map((s) => s.state)).toEqual(['done', 'done', 'done', 'waiting'])
    // The next attempt starts: the open list is the same (the task is not failed any more, nothing else changed), the track moves.
    srv.tracks.set(e.receipt, steps('done', 'done', 'done', 'done'))
    await l.event() // `run`
    expect(state().items).toEqual([])
    expect(state().tracks[trackKey(e)]!.map((s) => s.state)).toEqual(['done', 'done', 'done', 'done'])
    // Final now: the next read of the list reads its track no more.
    const before = srv.trackReads()
    await l.event()
    expect(srv.trackReads()).toBe(before)
    stop()
  })

  it('keeps an action before its reply and writes its outcome in, and reads nothing before it is ready', async () => {
    const srv = fakeServer()
    const store = storage()
    let ready = false
    const l = new InboxLive()
    let state: InboxState | null = null
    l.attach(srv.reads, { storage: () => store, apply: (s) => (state = s), ready: () => ready })
    await l.changed()
    expect(srv.lists()).toBe(0)
    ready = true
    l.remember({ ref: 'question:s1/q1', receipt: 'q1', attempt: 'd1', at: Date.now() })
    expect(JSON.parse(store.getItem(RECENT_KEY)!)).toHaveLength(1)
    l.outcome('q1', 'd1', 'accepted')
    expect(state!.recent[0]!.outcome).toBe('accepted')
    expect(JSON.parse(store.getItem(RECENT_KEY)!)[0].outcome).toBe('accepted')
  })
})

describe('what the page knows of an answer', () => {
  const hosted = () => steps('done', 'unknown', 'unknown', 'waiting')
  it("keeps a hosted question's recorded and validated steps unknown for an answer nobody confirmed, and fills them with the owner's word", async () => {
    const srv = fakeServer()
    const { l, state } = liveOn(srv)
    const stop = l.track()
    // An older owner (its welcome says no `answer`): the keys went, and no word will come.
    const old: RecentEntry = { ref: 'question:h1/q1', receipt: 'q1', attempt: 'd1', at: Date.now() }
    srv.tracks.set('q1', hosted())
    l.remember(old)
    l.outcome('q1', 'd1', UNCONFIRMED)
    await l.trackNow(old)
    expect(state().tracks[trackKey(old)]!.map((s) => s.state)).toEqual(['done', 'unknown', 'unknown', 'waiting'])
    // The owner's own word (answer_ok) on another request fills them.
    const took: RecentEntry = { ref: 'question:h1/q2', receipt: 'q2', attempt: 'd2', at: Date.now() }
    srv.tracks.set('q2', hosted())
    l.remember(took)
    l.outcome('q2', 'd2', 'accepted')
    await l.trackNow(took)
    expect(state().tracks[trackKey(took)]!.map((s) => s.state)).toEqual(['done', 'done', 'done', 'waiting'])
    stop()
  })

  it('drops an action whose request never left the browser', () => {
    const srv = fakeServer()
    const { l, state, store } = liveOn(srv)
    const e: RecentEntry = { ref: 'question:s1/q1', receipt: 'q1', attempt: 'd1', at: Date.now() }
    l.remember(e)
    l.forget(e)
    expect(state().recent).toEqual([])
    expect(JSON.parse(store.getItem(RECENT_KEY)!)).toEqual([])
  })

  it('follows its actions and their outcomes when the storage throws on every read and write', async () => {
    const srv = fakeServer()
    const throwing = {
      getItem: () => {
        throw new Error('storage refused')
      },
      setItem: () => {
        throw new Error('storage refused')
      },
    }
    const { l, state } = liveOn(srv, throwing as unknown as ReturnType<typeof storage>)
    const e: RecentEntry = { ref: 'question:h1/q1', receipt: 'q1', attempt: 'd1', at: Date.now() }
    srv.tracks.set('q1', hosted())
    l.remember(e)
    l.outcome('q1', 'd1', 'accepted')
    expect(state().recent.map((x) => [x.receipt, x.outcome])).toEqual([['q1', 'accepted']])
    // The list's reads read the tracks again: the entry and its word are still followed.
    const stop = l.track()
    await l.changed()
    expect(state().recent.map((x) => [x.receipt, x.outcome])).toEqual([['q1', 'accepted']])
    expect(state().tracks[trackKey(e)]!.map((s) => s.state)).toEqual(['done', 'done', 'done', 'waiting'])
    stop()
  })
})

describe('a read that never answers', () => {
  it('is given up on past its bound, and the next event reads again', async () => {
    const srv = fakeServer()
    let hang = true
    const reads: InboxReads = { ...srv.reads, list: (after) => (hang ? new Promise<InboxPage>(() => {}) : srv.reads.list(after)) }
    const l = new InboxLive()
    let state: InboxState | null = null
    l.attach(reads, { storage: () => storage(), apply: (s) => (state = s), waitMs: 20 })
    const stuck = l.changed()
    // Behind the read that never answers, an event's read is queued; once it is given up on, the queued one reads.
    hang = false
    srv.open.set('question:s1/q2', summary('question:s1/q2'))
    void l.event()
    await stuck
    expect(state!.items.map((i) => i.ref)).toEqual(['question:s1/q2'])
    expect(state!.error).toBe('')
  })
})

describe("a command's acceptance", () => {
  it("fills none of its steps: an uncertain receipt's validated step stays unknown", async () => {
    const srv = fakeServer()
    const { l, state } = liveOn(srv)
    const e: RecentEntry = { ref: 'task:r/build', receipt: 'c1', at: Date.now() }
    srv.tracks.set('c1', steps('done', 'done', 'unknown', 'not_applicable'))
    l.remember(e)
    l.outcome('c1', undefined, 'accepted')
    await l.trackNow(e)
    expect(state().tracks[trackKey(e)]!.map((s) => s.state)).toEqual(['done', 'done', 'unknown', 'not_applicable'])
  })
})

describe('a failed read', () => {
  it('after an empty one, says nothing of what is open: the list is not drawn as empty', async () => {
    const srv = fakeServer()
    let fail = false
    const reads: InboxReads = { ...srv.reads, list: (after) => (fail ? Promise.reject(new Error('the server could not be reached')) : srv.reads.list(after)) }
    const l = new InboxLive()
    let state: InboxState | null = null
    l.attach(reads, { storage: () => storage(), apply: (s) => (state = s) })
    await l.changed()
    expect(inboxEmpty(state!)).toBe(true)
    fail = true
    await l.changed()
    expect(state!.error).toBe('the server could not be reached')
    expect(inboxEmpty(state!)).toBe(false)
  })
})

describe("a blocker's Cancel", () => {
  it("is tracked under the ref its action names, the item's own kept for the list", async () => {
    const srv = fakeServer()
    const store = storage()
    const asked: string[] = []
    const reads: InboxReads = { ...srv.reads, track: (e) => (asked.push(e.track ?? e.ref), srv.reads.track(e)) }
    const l = new InboxLive()
    l.attach(reads, { storage: () => store })
    l.remember({ ref: 'question:s1/q1', track: 'task:r1/build', receipt: 'c1', at: Date.now() })
    const stop = l.track()
    await l.changed()
    expect(asked).toContain('task:r1/build')
    expect(l.current.recent[0]!.ref).toBe('question:s1/q1')
    stop()
  })
})
