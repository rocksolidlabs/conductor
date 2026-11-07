import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as vue from 'vue'
import { ApiError } from './useApi'
import type { RunSummary, TicketRunEntry, TicketRunsPage } from './useDispatch'
import type { RunInfo, SessionInfo } from './useSessions'
import { DispatchOrder } from '../utils/dispatchOrder'
// Imported with the file, not inside a test: a module loaded in a test is loaded on the test's 5 s, which a loaded machine spends on the
// load alone. Its auto-imports are globals read when it is called, stubbed below.
import { useTicketRuns } from './useTicketRuns'

// A ticket page's Runs (Program item DS2, PR-11) wired to a live store and an API the test holds: what in the live store reads the
// ticket's runs again (a shown run's change, gathered for the window; a live run that continues a record; never a member's question),
// Show older's pages, another ticket, a run_changing reply asked again after its second with nothing said as a failure, and a 404
// ordered through the page's gate. useTicketRuns reads Nuxt's auto-imports (Vue's, useDispatchApi, useAttention, onBeforeUnmount) as
// globals: each is stubbed, the API by replies the test lets go, on fake timers.

function held<T>() {
  let ok!: (v: T) => void
  let fail!: (e: unknown) => void
  const p = new Promise<T>((resolve, reject) => {
    ok = resolve
    fail = reject
  })
  return { p, ok, fail }
}

/** Every ticketRuns request sent, with the cursor it named, held until the test lets it go. */
let sent: Array<{ key: string; before: string } & ReturnType<typeof held<TicketRunsPage>>>
/** The live store: its runs by id, every live run, the live sessions. */
let liveRuns: vue.Ref<RunInfo[]>
let sessions: vue.Ref<SessionInfo[]>
let unmount: Array<() => void>

function summary(id: string): RunSummary {
  return { id, label: '', state: 'running', paused: false, startedAt: '2026-11-01T10:00:00Z', record: false, members: [], tasks: {}, questions: 0, blockers: [] }
}
const launchEntry = (run: string): TicketRunEntry => ({ kind: 'launch', launch: { id: `l-${run}`, runId: run, state: 'started' } as TicketRunEntry['launch'], run: summary(run) })
function liveRun(id: string, p: Partial<RunInfo> = {}): RunInfo {
  return { id, state: 'running', managed: { revision: 1 }, members: [{ name: 'asker', sessionId: 's1', status: 'running' }], ...p } as unknown as RunInfo
}

beforeEach(() => {
  vi.useFakeTimers()
  sent = []
  unmount = []
  liveRuns = vue.ref([])
  sessions = vue.ref([{ id: 's1', status: 'running' } as SessionInfo])
  for (const k of ['ref', 'computed', 'watch'] as const) vi.stubGlobal(k, vue[k])
  vi.stubGlobal('onBeforeUnmount', (f: () => void) => unmount.push(f))
  vi.stubGlobal('useDispatchApi', () => ({
    ticketRuns: (key: string, before = '') => {
      const h = held<TicketRunsPage>()
      sent.push({ key, before, ...h })
      return h.p
    },
  }))
  vi.stubGlobal('useAttention', () => ({
    runOf: (id: string) => liveRuns.value.find((r) => r.id === id),
    runs: liveRuns,
    sessions,
  }))
})
afterEach(() => {
  for (const f of unmount) f()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

async function section(key = vue.ref('EV-1')) {
  const gate = new DispatchOrder()
  const runs = useTicketRuns({ gate, key: () => key.value })
  return { gate, key, runs }
}
/** Lets the microtasks and the watchers run, and the clock move `ms`. */
const tick = (ms = 0) => vi.advanceTimersByTimeAsync(ms)

describe("a ticket page's Runs, wired to the live store", () => {
  it("reads the pages again once for a burst of a shown run's changes, gathered for the window, and never for a member's question", async () => {
    liveRuns.value = [liveRun('r1')]
    const { runs } = await section()
    const first = runs.read()
    sent[0]!.ok({ entries: [launchEntry('r1')] })
    await first
    await tick()
    expect(runs.entries.value.map((e) => e.run?.id)).toEqual(['r1'])

    // A question: the session's attention changes, the run does not; nothing is read.
    sessions.value = [{ id: 's1', status: 'running', attention: { state: 'needs_input', message: 'Ok?' } } as SessionInfo]
    await tick(1000)
    expect(sent).toHaveLength(1)

    // The run's revision moves twice and its member's session ends, within the window: one read after it.
    liveRuns.value = [liveRun('r1', { managed: { revision: 2 } } as Partial<RunInfo>)]
    await tick(50)
    liveRuns.value = [liveRun('r1', { managed: { revision: 3 } } as Partial<RunInfo>)]
    sessions.value = [{ id: 's1', status: 'exited' } as SessionInfo]
    await tick(150)
    expect(sent).toHaveLength(1)
    await tick(100)
    expect(sent).toHaveLength(2)
    sent[1]!.ok({ entries: [launchEntry('r1')] })
    await tick(1000)
    expect(sent).toHaveLength(2)
  })

  it('reads the pages again when a live run comes to continue a record it shows, which the live store never holds', async () => {
    const { runs } = await section()
    const first = runs.read()
    sent[0]!.ok({ entries: [{ ...launchEntry('r0'), run: { ...summary('r0'), record: true } }] })
    await first
    await tick()
    // The record resumed from another page: a new live run names it.
    liveRuns.value = [liveRun('c1', { resumedFrom: 'r0' })]
    await tick(250)
    expect(sent).toHaveLength(2)
    sent[1]!.ok({ entries: [{ kind: 'run', launch: launchEntry('r0').launch, run: summary('c1'), continues: 'r0' }, { ...launchEntry('r0'), run: { ...summary('r0'), record: true } }] })
    await tick(1000)
    expect(runs.entries.value.map((e) => e.run?.id)).toEqual(['c1', 'r0'])
    expect(sent).toHaveLength(2)
  })

  it('shows older by reading one page more with the others, from the start', async () => {
    const { runs } = await section()
    const first = runs.read()
    sent[0]!.ok({ entries: [launchEntry('r2')], next: 'cur1' })
    await first
    expect(runs.next.value).toBe('cur1')
    const older = runs.older()
    await tick()
    expect(sent.map((s) => s.before)).toEqual(['', ''])
    sent[1]!.ok({ entries: [launchEntry('r2')], next: 'cur1' })
    await tick()
    expect(sent.map((s) => s.before)).toEqual(['', '', 'cur1'])
    sent[2]!.ok({ entries: [launchEntry('r1')] })
    await older
    expect(runs.entries.value.map((e) => e.run?.id)).toEqual(['r2', 'r1'])
    expect(runs.next.value).toBe('')
    // A re-read reads both pages again.
    const again = runs.read()
    sent[3]!.ok({ entries: [launchEntry('r2')], next: 'cur1' })
    await tick()
    sent[4]!.ok({ entries: [launchEntry('r1')] })
    await again
    expect(sent).toHaveLength(5)
  })

  it("keeps nothing of another ticket: a key changed resets the pages, and the old ticket's late reply is dropped", async () => {
    const { runs, key } = await section()
    const first = runs.read()
    sent[0]!.ok({ entries: [launchEntry('r1')], next: 'cur1' })
    await first
    const older = runs.older()
    await tick()
    key.value = 'EV-2'
    await tick()
    expect(runs.pages.value).toBeNull()
    sent[1]!.ok({ entries: [launchEntry('r1')], next: 'cur1' })
    await tick()
    sent[2]!.ok({ entries: [launchEntry('r0')] })
    await older
    expect(runs.pages.value).toBeNull()
    // The new ticket reads one page, its own.
    const next = runs.read()
    expect(sent.at(-1)).toMatchObject({ key: 'EV-2', before: '' })
    sent.at(-1)!.ok({ entries: [launchEntry('r9')] })
    await next
    expect(runs.entries.value.map((e) => e.run?.id)).toEqual(['r9'])
  })

  it('asks a run_changing reply again after its second, on the first read too, with nothing said as a failure', async () => {
    const { runs } = await section()
    const first = runs.read()
    sent[0]!.fail(new ApiError(503, 'run_changing', 'a run of the chain is being resumed'))
    await expect(first).resolves.toBeUndefined()
    expect([runs.error.value, runs.pages.value]).toEqual(['', null])
    await tick(999)
    expect(sent).toHaveLength(1)
    await tick(1)
    expect(sent).toHaveLength(2)
    // Refused again with pages shown: they stay, nothing said, asked again.
    sent[1]!.ok({ entries: [launchEntry('r1')] })
    await tick()
    const again = runs.read()
    sent[2]!.fail(new ApiError(503, 'run_changing', 'a run of the chain is being resumed'))
    await again
    expect(runs.error.value).toBe('')
    expect(runs.entries.value.map((e) => e.run?.id)).toEqual(['r1'])
    await tick(1000)
    expect(sent).toHaveLength(4)
    sent[3]!.ok({ entries: [launchEntry('r2')] })
    await tick()
    expect(runs.entries.value.map((e) => e.run?.id)).toEqual(['r2'])
    // Any other failure is said, and rejects, for the page's reload to ask again.
    const failed = runs.read()
    sent[4]!.fail(new ApiError(500, 'internal', 'the ledger is closed'))
    await expect(failed).rejects.toThrow('the ledger is closed')
    expect(runs.error.value).toBe('the ledger is closed')
  })

  it('orders a 404 through the gate: a slow one never empties what a later read found, nor what a snapshot read again', async () => {
    const { runs, gate } = await section()
    const slow = runs.read()
    const later = runs.read()
    sent[1]!.ok({ entries: [launchEntry('r1')] })
    await later
    sent[0]!.fail(new ApiError(404, 'not_found', 'no such ticket'))
    await slow
    expect(runs.entries.value.map((e) => e.run?.id)).toEqual(['r1'])

    // A 404 to a read sent before a snapshot's reset is dropped; the read after it stands.
    const before = runs.read()
    gate.reset()
    const after = runs.read()
    sent[3]!.ok({ entries: [launchEntry('r2')] })
    await after
    sent[2]!.fail(new ApiError(404, 'not_found', 'no such ticket'))
    await before
    expect(runs.entries.value.map((e) => e.run?.id)).toEqual(['r2'])

    // A 404 taken, the latest: no runs, and no error.
    const gone = runs.read()
    sent[4]!.fail(new ApiError(404, 'not_found', 'no such ticket'))
    await gone
    expect([runs.pages.value, runs.error.value]).toEqual([[], ''])
  })
})
