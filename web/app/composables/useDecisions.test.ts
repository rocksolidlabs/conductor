import { describe, expect, it } from 'vitest'
import type { Decision } from '~/utils/decisions'
import { DecisionsLive, asDecisionEvent } from './useDecisions'

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

/** A server whose list answers what `now` holds, each read held until the test releases it. */
function server() {
  const now: Decision[] = []
  const reads: string[] = []
  const held: Array<() => void> = []
  const list = (state: 'open' | 'answered') => {
    reads.push(state)
    const answer = now.filter((d) => d.state === state)
    return new Promise<Decision[]>((resolve) => held.push(() => resolve(answer)))
  }
  /** Lets every read waiting go, and the reads they started after them, until none waits. */
  async function drain() {
    for (let i = 0; i < 10 && held.length; i++) {
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

  it('tells the watchers which decision changed, and null when the stream came back or is down', () => {
    const live = new DecisionsLive()
    const told: Array<string | null> = []
    const stop = live.subscribe((e) => told.push(e ? e.id : null))
    live.stream('decision', ev('dr-7'))
    live.stream('snapshot', [])
    live.down()
    live.stream('session', { id: 's1' })
    stop()
    live.stream('decision', ev('dr-8'))
    expect(told).toEqual(['dr-7', null, null])
    expect(asDecisionEvent({ id: 'dr-7', run: RUN, state: 'open', rev: 2 })).toEqual({ id: 'dr-7', run: RUN, state: 'open', rev: 2 })
    expect(asDecisionEvent({ id: 'dr-7', run: RUN, state: 'open' })).toBeNull()
  })
})
