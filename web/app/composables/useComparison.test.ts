import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as vue from 'vue'
import type { Comparison } from '~/utils/compare'
import type { RunInfo } from './useSessions'
// Imported with the file, not inside a test (a module loaded in a test is loaded on the test's 5 s). Their auto-imports are globals,
// stubbed below.
import { ApiError } from './useApi'
import { dispatchLive } from './useDispatchLive'
import { decisionsLive } from './useDecisions'
import { useComparison } from './useComparison'
import { RUN_CHANGING_RETRY_MS } from '~/utils/dispatchEvidence'
import { RUN_EVENT_WINDOW_MS } from '~/utils/runs'

// The comparison's reads (Program item CP1, docs/plans/compare-crew.md, PR-4), wired to an API whose every request the test holds and
// lets go: read again on the run's change, on a `brakeman` event naming its request while the run is idle, on a `dispatch` event naming
// its ticket and on a reconnect's snapshot; one fetch at a time.

function held<T>() {
  let ok!: (v: T) => void
  let fail!: (e: unknown) => void
  const p = new Promise<T>((resolve, reject) => {
    ok = resolve
    fail = reject
  })
  return { p, ok, fail }
}

let sent: Array<{ path: string } & ReturnType<typeof held<unknown>>>
let liveRuns: vue.Ref<RunInfo[]>
let requests: vue.Ref<Array<{ id: string; state: string }>>
let unmount: Array<() => void>
let seq = 1000

beforeEach(() => {
  sent = []
  unmount = []
  liveRuns = vue.ref([])
  requests = vue.ref([])
  for (const k of ['ref', 'computed', 'watch'] as const) vi.stubGlobal(k, vue[k])
  vi.stubGlobal('onBeforeUnmount', (f: () => void) => unmount.push(f))
  vi.stubGlobal('useApi', () => ({
    request: (path: string) => {
      const h = held<unknown>()
      sent.push({ path, ...h })
      return h.p
    },
  }))
  vi.stubGlobal('useAttention', () => ({ runOf: (id: string) => liveRuns.value.find((r) => r.id === id) }))
  vi.stubGlobal('useBrakeman', () => ({ requests }))
})
afterEach(() => {
  for (const f of unmount) f()
  vi.unstubAllGlobals()
})

const settle = async () => {
  for (let i = 0; i < 5; i++) await new Promise((r) => setTimeout(r, 0))
}

const RUN = 'e2e-crew-0a1b2c3d'
const TICKET = 'tk-1'
const run = (revision: number): RunInfo => ({ id: RUN, managed: { revision }, state: 'running' }) as unknown as RunInfo
const reply = (o: Partial<Comparison> = {}): Comparison => ({
  run: RUN,
  base: '1'.repeat(40),
  objective: 'Make the cache faster',
  criteria: [{ id: 'c1', text: 'no new dependency' }],
  candidates: [],
  checks: [],
  settled: true,
  supporting: { claims: [], reviews: [], brakeman: { request: 'br-4', state: 'queued' } },
  ...o,
})

/** Mounts the composable and answers its first read. */
async function mounted(first = reply()) {
  const c = useComparison({ run: () => RUN, ticket: () => TICKET })
  await settle()
  expect(sent.map((s) => s.path)).toEqual([`/api/runs/${RUN}/comparison`])
  sent[0]!.ok(first)
  await settle()
  expect(c.comparison.value).toEqual(first)
  return c
}

describe('useComparison', () => {
  it("reads again on the run's change, one fetch at a time", async () => {
    liveRuns.value = [run(3)]
    const c = await mounted()
    liveRuns.value = [run(4)]
    await settle()
    expect(sent).toHaveLength(2)
    // Two more changes while that read is out: one read after it, never two at once.
    liveRuns.value = [run(5)]
    await settle()
    liveRuns.value = [run(6)]
    await settle()
    expect(sent).toHaveLength(2)
    sent[1]!.ok(reply({ settled: false }))
    await settle()
    expect(sent).toHaveLength(3)
    sent[2]!.ok(reply({ objective: 'newest' }))
    await settle()
    expect(sent).toHaveLength(3)
    expect(c.comparison.value?.objective).toBe('newest')
  })

  it('reads again on a brakeman event naming its request while the run is idle, and not on another request', async () => {
    liveRuns.value = [run(3)]
    requests.value = [{ id: 'br-4', state: 'queued' }]
    await mounted()
    requests.value = [{ id: 'br-4', state: 'queued' }, { id: 'br-9', state: 'running' }]
    await settle()
    expect(sent).toHaveLength(1)
    // The draft arrived: the request closed, and nothing of the run moved.
    requests.value = [{ id: 'br-4', state: 'done' }, { id: 'br-9', state: 'running' }]
    await settle()
    expect(sent).toHaveLength(2)
    sent[1]!.ok(reply({ supporting: { brakeman: { request: 'br-4', state: 'done', brief: 'brief-1' } } }))
    await settle()
  })

  it('reads again on a dispatch event naming its ticket, and not for another ticket', async () => {
    await mounted()
    dispatchLive().event({ seq: ++seq, entity: 'ticket', id: 'tk-2', ticket: 'tk-2', project: 'CND' })
    await settle()
    expect(sent).toHaveLength(1)
    dispatchLive().event({ seq: ++seq, entity: 'comment', id: 'cm-1', ticket: TICKET, project: 'CND' })
    await settle()
    expect(sent).toHaveLength(2)
    sent[1]!.ok(reply())
    await settle()
  })

  it("reads again on a decision event of its selection, answered from a second client while the run is not held, with no reconnect, and shows the new state", async () => {
    // The run is stopped: its events do not move as the selection is answered, nor does its ticket.
    const stopped = { ...run(3), stoppedAt: '2026-11-16T10:00:00Z' } as RunInfo
    liveRuns.value = [stopped]
    const open = { decision: 'dr-9', state: 'open', options: [{ label: 'approach-a at 1f3a5c7' }, { label: 'None of these' }] } as const
    const c = await mounted(reply({ selection: { ...open, options: [...open.options] } }))
    // Another record of another run: nothing read.
    decisionsLive().stream('decision', { id: 'dr-40', run: 'e2e-crew-ffffffff', state: 'answered', rev: 2 })
    await settle()
    expect(sent).toHaveLength(1)
    // The selection answered elsewhere: its decision event reads the comparison again, and the panel shows it answered.
    decisionsLive().stream('decision', { id: 'dr-9', run: RUN, state: 'answered', rev: 2 })
    await settle()
    expect(sent).toHaveLength(2)
    sent[1]!.ok(reply({ selection: { ...open, options: [...open.options], state: 'answered', chosen: 0, rationale: 'smaller' } }))
    await settle()
    expect(c.comparison.value?.selection?.state).toBe('answered')
    // A record of the chain's run that is not the selection shown (a new one opened after a lane ended again) reads it too.
    decisionsLive().stream('decision', { id: 'dr-10', run: RUN, state: 'open', rev: 1 })
    await settle()
    expect(sent).toHaveLength(3)
    sent[2]!.ok(reply())
    await settle()
    expect(liveRuns.value[0]!.managed?.revision).toBe(3)
  })

  it("reads again on the stream's reconnect snapshot", async () => {
    await mounted()
    dispatchLive().snapshot()
    await settle()
    expect(sent).toHaveLength(2)
    sent[1]!.ok(reply())
    await settle()
  })

  it('asks a read refused while the chain moved again after the second, and a failure of that read again after the window', async () => {
    vi.useFakeTimers()
    try {
      const step = (ms = 0) => vi.advanceTimersByTimeAsync(ms)
      const c = useComparison({ run: () => RUN, ticket: () => TICKET })
      await step()
      sent[0]!.ok(reply())
      await step()
      dispatchLive().snapshot()
      await step()
      expect(sent).toHaveLength(2)
      sent[1]!.fail(new ApiError(503, 'run_changing', 'the run changed through every read of its comparison; ask again'))
      await step()
      expect(c.changing.value).toBe(true)
      await step(RUN_CHANGING_RETRY_MS)
      expect(sent).toHaveLength(3)
      // That read fails another way: said, the chain no longer said to be moving, and asked again after the live store's window.
      sent[2]!.fail(new ApiError(500, 'internal', 'the trial could not be read'))
      await step()
      expect(c.error.value).toBe('the trial could not be read')
      expect(c.changing.value).toBe(false)
      await step(RUN_EVENT_WINDOW_MS)
      expect(sent).toHaveLength(4)
      sent[3]!.ok(reply())
      await step()
      expect(c.error.value).toBe('')
      expect(c.comparison.value).not.toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  it('says a run of no comparison, and keeps what it shows while the chain moves', async () => {
    const c = await mounted()
    dispatchLive().snapshot()
    await settle()
    sent[1]!.fail(new ApiError(503, 'run_changing', 'the run changed through every read of its comparison; ask again'))
    await settle()
    expect(c.changing.value).toBe(true)
    expect(c.comparison.value).not.toBeNull()
    expect(c.error.value).toBe('')

    const other = useComparison({ run: () => 'e2e-crew-99999999' })
    await settle()
    sent.at(-1)!.fail(new ApiError(404, 'not_found', 'this run is not a trial'))
    await settle()
    expect(other.missing.value).toBe('this run is not a trial')
    expect(other.loaded.value).toBe(true)
    expect(other.comparison.value).toBeNull()
  })
})
