import { describe, expect, it } from 'vitest'
import type { Decision } from '~/utils/decisions'
import { staleNotice } from '~/utils/decisions'
import { DECISION_PAGES, DECISION_RETRY_CAP_MS, DECISION_RETRY_FIRST_MS, DecisionRecordView, DecisionsLive, asDecisionEvent, readAll } from './useDecisions'

// The app's store of open and answered decisions (Program item DR1, docs/plans/decisions.md, "web/app"): read again on a `decision`
// event of the one events stream and on no other; on the stream's `snapshot` (it replays nothing it missed while away), and on the
// sessions' cadence while the stream is down; one read at a time, one more after it when asked meanwhile. Each read is held at a gate
// the test opens, so what is asked while one is under way is judged before it ends.

const RUN = 'e2e-crew-0a1b2c3d'
const decision = (id: string, state: Decision['state'], createdAt: string): Decision => ({
  id,
  run: RUN,
  openedIn: RUN,
  origin: 'plan',
  type: 'approval',
  request: { question: `${id}?` },
  policy: { approver: 'operator' },
  state,
  rev: 1,
  createdAt,
})

const num = (id: string) => Number(id.slice(3))

/**
 * A server whose list answers what `now` holds, as the route pages it (newest first by number, `page` a page, `next` the last id
 * when more follow, `after` reading on below it), each read held until the test releases it.
 */
function server(page = 100) {
  const now: Decision[] = []
  const reads: string[] = []
  const held: Array<() => void> = []
  const list = (state: 'open' | 'answered', after?: string) => {
    reads.push(state)
    const all = now.filter((d) => d.state === state && (!after || num(d.id) < num(after))).sort((a, b) => num(b.id) - num(a.id))
    const answer = { decisions: all.slice(0, page), next: all.length > page ? all[page - 1]!.id : undefined }
    return new Promise<{ decisions: Decision[]; next?: string }>((resolve) => held.push(() => resolve(answer)))
  }
  /** Lets every read waiting go, and the reads they started after them, until none waits. */
  async function drain() {
    for (let i = 0; i < 50 && held.length; i++) {
      for (const go of held.splice(0)) go()
      await new Promise((r) => setTimeout(r, 0))
    }
  }
  return { now, reads, held, list, drain }
}

const ev = (id: string, state = 'open') => ({ id, run: RUN, state, rev: 1 })

describe('the decisions store', () => {
  it('reads the open and the answered ones, oldest first', async () => {
    const s = server()
    s.now.push(decision('dr-2', 'answered', '2026-11-07T10:01:00Z'), decision('dr-1', 'open', '2026-11-07T10:00:00Z'), decision('dr-3', 'applied', '2026-11-07T10:02:00Z'))
    const live = new DecisionsLive()
    live.attach({ list: s.list })
    const done = live.read()
    await s.drain()
    await done
    expect(s.reads.sort()).toEqual(['answered', 'open'])
    expect(live.decisions.map((d) => d.id)).toEqual(['dr-1', 'dr-2'])
    expect(live.open().map((d) => d.id)).toEqual(['dr-1'])
  })

  // The reviews of #719: the route pages newest first, at most 100; the store follows `next`, so the decisions that waited longest
  // (and a run's gate behind a page of newer ones) are in Needs you and the palette, and says when it stopped short (`cut`).
  it("follows the list's pages, so an open decision past the first page is read", async () => {
    const s = server(100)
    for (let i = 1; i <= 250; i++) s.now.push(decision(`dr-${i}`, 'open', `2026-11-07T10:00:${String(i % 60).padStart(2, '0')}Z`))
    const live = new DecisionsLive()
    live.attach({ list: s.list })
    const done = live.read()
    await s.drain()
    await done
    expect(live.open()).toHaveLength(250)
    expect(live.open().some((d) => d.id === 'dr-1')).toBe(true)
    expect(s.reads.filter((r) => r === 'open')).toHaveLength(3)
    expect(live.cut).toBe(false)
  })

  it('reads at most DECISION_PAGES pages of a state, and says the list is cut past them', async () => {
    const s = server(1)
    for (let i = 1; i <= DECISION_PAGES + 2; i++) s.now.push(decision(`dr-${i}`, 'open', '2026-11-07T10:00:00Z'))
    const reading = readAll({ list: s.list }, 'open')
    await s.drain()
    const r = await reading
    expect(r.decisions).toHaveLength(DECISION_PAGES)
    expect(r.cut).toBe(true)
    const live = new DecisionsLive()
    live.attach({ list: s.list })
    const done = live.read()
    await s.drain()
    await done
    expect(live.cut).toBe(true)
  })

  it('reads again on a decision event, and on no other event', async () => {
    const s = server()
    const live = new DecisionsLive()
    live.attach({ list: s.list })
    for (const [event, payload] of [['session', { id: 's1' }], ['run', { id: RUN }], ['dispatch', { seq: 1 }], ['brakeman', { id: 'br-1', state: 'done' }], ['activity', { sessionId: 's1' }]] as const) live.stream(event, payload)
    // A decision event's data that is not one (no id of a decision's shape) reads nothing either.
    live.stream('decision', { id: 'br-1', run: RUN, state: 'open', rev: 1 })
    expect(s.reads).toEqual([])
    live.stream('decision', ev('dr-1'))
    expect(s.reads).toEqual(['open', 'answered'])
    await s.drain()
  })

  it("reads again on the stream's snapshot, after a decision changed while the stream was away, and on the sessions' cadence while it is down", async () => {
    const s = server()
    const live = new DecisionsLive()
    live.attach({ list: s.list })
    // A gate opened while the stream was away: no event came for it, the snapshot reads it.
    s.now.push(decision('dr-4', 'open', '2026-11-07T10:00:00Z'))
    live.stream('snapshot', [])
    await s.drain()
    expect(live.open().map((d) => d.id)).toEqual(['dr-4'])
    // Down: each tick of the fallback reads it again, as it reads the sessions.
    s.now[0] = decision('dr-4', 'answered', '2026-11-07T10:00:00Z')
    live.down()
    await s.drain()
    expect(live.open()).toEqual([])
    expect(live.decisions.map((d) => [d.id, d.state])).toEqual([['dr-4', 'answered']])
    expect(s.reads).toHaveLength(4)
  })

  it('reads once more after a read under way, however many events came meanwhile', async () => {
    const s = server()
    const live = new DecisionsLive()
    live.attach({ list: s.list })
    live.stream('decision', ev('dr-1'))
    for (let i = 0; i < 5; i++) live.stream('decision', ev('dr-1', 'answered'))
    expect(s.reads).toHaveLength(2)
    await s.drain()
    expect(s.reads).toHaveLength(4)
  })

  // Codex's round 2 on #719: the read under way failed after an event had asked for another, and the one asked was never made; with the
  // stream connected nothing else reads, so an open decision stayed missing from Needs you and the palette.
  it('reads again after a read that failed when an event came meanwhile, and keeps what was read before it', async () => {
    const s = server()
    s.now.push(decision('dr-1', 'open', '2026-11-07T10:00:00Z'))
    const live = new DecisionsLive()
    let fail = false
    live.attach({ list: (state, after) => (fail ? Promise.reject(new Error('the server is busy')) : s.list(state, after)) })
    const first = live.read()
    await s.drain()
    await first
    expect(live.open().map((d) => d.id)).toEqual(['dr-1'])
    // A gate opens: its event reads, and that read fails; a second event comes while it is under way.
    s.now.push(decision('dr-2', 'open', '2026-11-07T10:01:00Z'))
    fail = true
    live.stream('decision', ev('dr-2'))
    fail = false
    live.stream('decision', ev('dr-2'))
    // The failure comes first (no read is held for it); then the read asked meanwhile.
    await new Promise((r) => setTimeout(r, 0))
    expect(s.held).toHaveLength(2)
    await s.drain()
    expect(live.open().map((d) => d.id)).toEqual(['dr-1', 'dr-2'])
    expect(live.error).toBe('')
  })

  it('says a read that failed, and keeps what was read before it', async () => {
    const s = server()
    s.now.push(decision('dr-1', 'open', '2026-11-07T10:00:00Z'))
    const live = new DecisionsLive()
    let fail = false
    live.attach({ list: (state, after) => (fail ? Promise.reject(new Error('the server is busy')) : s.list(state, after)) })
    const first = live.read()
    await s.drain()
    await first
    // Its retry (the next test's) is not set off here.
    live.schedule = () => () => {}
    fail = true
    await live.read()
    expect(live.error).toBe('the server is busy')
    expect(live.open().map((d) => d.id)).toEqual(['dr-1'])
  })

  // Codex's round 3 on #719: the last read failed and no event came after it (the stream connected, nothing else reads), so a gate
  // whose opening event asked for it stayed missing from Needs you and the palette, with nothing said. It is read again by itself, with
  // a capped backoff, and the list is said out of date since the failure until a read succeeds.
  it('reads again by itself, with backoff, after the last read failed with no event after it, and says since when it is out of date', async () => {
    const s = server()
    s.now.push(decision('dr-1', 'open', '2026-11-07T10:00:00Z'))
    const live = new DecisionsLive()
    let fail = false
    live.attach({ list: (state, after) => (fail ? Promise.reject(new Error('the server is busy')) : s.list(state, after)) })
    const retries: Array<{ ms: number; go: () => void; cancelled: boolean }> = []
    live.schedule = (go, ms) => {
      const r = { ms, go, cancelled: false }
      retries.push(r)
      return () => (r.cancelled = true)
    }
    let clock = 1_000_000
    live.clock = () => clock
    const first = live.read()
    await s.drain()
    await first
    expect([live.staleSince, retries]).toEqual([null, []])
    // A gate opens; its event's read fails, and no event comes after it.
    s.now.push(decision('dr-2', 'open', '2026-11-07T10:01:00Z'))
    fail = true
    live.stream('decision', ev('dr-2'))
    await new Promise((r) => setTimeout(r, 0))
    expect(live.open().map((d) => d.id)).toEqual(['dr-1'])
    expect([live.error, live.staleSince]).toEqual(['the server is busy', 1_000_000])
    expect(retries.map((r) => r.ms)).toEqual([1000])
    // The retries fail too: each waits twice as long, up to the cap, and the list stays out of date since the first failure.
    for (let i = 0; i < 6; i++) {
      clock += 1000
      retries.at(-1)!.go()
      await new Promise((r) => setTimeout(r, 0))
    }
    expect(retries.map((r) => r.ms)).toEqual([1000, 2000, 4000, 8000, 16000, DECISION_RETRY_CAP_MS, DECISION_RETRY_CAP_MS])
    expect(live.staleSince).toBe(1_000_000)
    expect(staleNotice(live.staleSince, live.error)).toMatch(/^Decisions out of date since .+ \(the server is busy\): read again until a read succeeds\.$/)
    // The server answers again: the next retry reads the gate, and the list is no longer said out of date.
    fail = false
    retries.at(-1)!.go()
    await s.drain()
    expect(live.open().map((d) => d.id)).toEqual(['dr-1', 'dr-2'])
    expect([live.error, live.staleSince, retries.length]).toEqual(['', null, 7])
    // A failure after it starts the backoff over, and a read that succeeds before its retry cancels it.
    fail = true
    await live.read()
    expect(retries.at(-1)!.ms).toBe(DECISION_RETRY_FIRST_MS)
    fail = false
    const again = live.read()
    await s.drain()
    await again
    expect(retries.at(-1)!.cancelled).toBe(true)
    expect(live.staleSince).toBeNull()
  })

  it('tells the watchers which decision changed, and null when the stream came back (restarted) or is down', () => {
    const live = new DecisionsLive()
    const told: Array<string | null> = []
    const stop = live.subscribe((e, restarted) => told.push(e ? e.id : restarted ? 'restarted' : null))
    live.stream('decision', ev('dr-7'))
    live.stream('snapshot', [])
    live.down()
    live.stream('session', { id: 's1' })
    stop()
    live.stream('decision', ev('dr-8'))
    expect(told).toEqual(['dr-7', 'restarted', null])
    expect(asDecisionEvent({ id: 'dr-7', run: RUN, state: 'open', rev: 2 })).toEqual({ id: 'dr-7', run: RUN, state: 'open', rev: 2 })
    expect(asDecisionEvent({ id: 'dr-7', run: RUN, state: 'open' })).toBeNull()
  })
})

// Codex's round 1 on #719: a write's reply (a comment's) delayed past the read of what another tab did since (the answer, applied)
// brought the older, open record back. The card's record is ordered by the record's revision, whichever reply comes last.
describe("the card's record", () => {
  const at = (rev: number, state: Decision['state']) => ({ ...decision('dr-7', state, '2026-11-07T10:00:00Z'), rev, history: rev > 1 ? [{ seq: rev, at: '2026-11-07T10:01:00Z', kind: 'responded' }] : [] })

  it('keeps the newer revision when a write answers after a read of a newer one', () => {
    const v = new DecisionRecordView()
    expect(v.read(v.generation, 'dr-7', 'dr-7', { decision: at(1, 'open') })).toBe(true)
    // The event of the other tab's answer read the applied record; the comment's reply, sent at rev 1, answers with rev 2 after it.
    expect(v.read(v.generation, 'dr-7', 'dr-7', { decision: at(4, 'applied') })).toBe(true)
    expect(v.write(v.generation, 'dr-7', { ...at(2, 'open'), history: undefined })).toBe(false)
    expect([v.decision?.rev, v.decision?.state]).toEqual([4, 'applied'])
  })

  it('keeps the newer revision when a read answers after a write of a newer one', () => {
    const v = new DecisionRecordView()
    v.read(v.generation, 'dr-7', 'dr-7', { decision: at(1, 'open') })
    // The answer's reply (rev 2) came first; a read started before it answers with rev 1.
    expect(v.write(v.generation, 'dr-7', { ...at(2, 'answered'), history: undefined })).toBe(true)
    expect(v.decision?.history).toEqual(at(1, 'open').history)
    expect(v.read(v.generation, 'dr-7', 'dr-7', { decision: at(1, 'open') })).toBe(false)
    expect([v.decision?.rev, v.decision?.state]).toEqual([2, 'answered'])
    // A read of the same revision fills what the write's reply left out.
    expect(v.read(v.generation, 'dr-7', 'dr-7', { decision: at(2, 'answered') })).toBe(true)
    expect(v.decision?.history).toHaveLength(1)
  })

  it("drops a read of another decision than the one asked for now, and a write's reply of another", () => {
    const v = new DecisionRecordView()
    expect(v.read(v.generation, 'dr-7', 'dr-8', { decision: at(1, 'open') })).toBe(false)
    expect(v.write(v.generation, 'dr-8', at(1, 'open'))).toBe(false)
    expect(v.decision).toBeNull()
  })

  // Codex's rounds 2 and 3 on #719: a store restored from a backup answers below the revision shown; after the stream comes back (reset)
  // the next reply is taken whatever its revision, and a read or a write started before the reset is dropped whenever it answers,
  // before the restored store's first read or after it.
  it('takes the next read whatever its revision after a reset, and drops what was started before it, before or after that read', () => {
    const v = new DecisionRecordView()
    v.read(v.generation, 'dr-7', 'dr-7', { decision: at(5, 'applied') })
    const before = v.generation
    v.reset()
    expect(v.read(before, 'dr-7', 'dr-7', { decision: at(6, 'applied') })).toBe(false)
    expect(v.write(before, 'dr-7', { ...at(6, 'applied'), history: undefined })).toBe(false)
    expect(v.read(v.generation, 'dr-7', 'dr-7', { decision: at(1, 'open') })).toBe(true)
    expect([v.decision?.rev, v.decision?.state]).toEqual([1, 'open'])
    // The old store's write answers after the restored read, above its revision: dropped all the same.
    expect(v.write(before, 'dr-7', { ...at(6, 'applied'), history: undefined })).toBe(false)
    expect([v.decision?.rev, v.decision?.state]).toEqual([1, 'open'])
    // Ordered by revision again from there.
    expect(v.read(v.generation, 'dr-7', 'dr-7', { decision: at(1, 'open') })).toBe(true)
    expect(v.read(v.generation, 'dr-7', 'dr-7', { decision: at(0, 'open') })).toBe(false)
    expect(v.write(v.generation, 'dr-7', { ...at(2, 'answered'), history: undefined })).toBe(true)
  })

  it("takes a write started after the reset as the restored store's first reply, and orders the read started before it by revision", () => {
    const v = new DecisionRecordView()
    v.read(v.generation, 'dr-7', 'dr-7', { decision: at(5, 'applied') })
    v.reset()
    expect(v.write(v.generation, 'dr-7', { ...at(2, 'answered'), history: undefined })).toBe(true)
    expect([v.decision?.rev, v.decision?.state]).toEqual([2, 'answered'])
    expect(v.read(v.generation, 'dr-7', 'dr-7', { decision: at(1, 'open') })).toBe(false)
    expect(v.decision?.rev).toBe(2)
  })
})
