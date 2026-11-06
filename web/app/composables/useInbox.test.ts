import { describe, expect, it } from 'vitest'
import { InboxLive, type InboxReads, type InboxState } from './useInbox'
import { RECENT_KEY, trackKey, type DeliveryStep, type InboxPage, type InboxSummary, type RecentEntry, type StepState } from '../utils/inbox'

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
