import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as vue from 'vue'
import { ApiError } from './useApi'
import type { RunCandidatesPage, RunSummary, TaskCandidates, TicketLink, TicketRunEntry } from './useDispatch'
import type { HeldRun } from './useTicketCandidates'
import type { RunCandidate } from './useDispatch'
import { ticketDecisions } from '../utils/dispatch'
import { DispatchOrder } from '../utils/dispatchOrder'

// The candidates of a ticket's runs as the delivery form and the Decisions read them (Program item DS2, PR-12), against replies the
// test lets go: the first read at once and a move gathered for the window; a run_changing reply asked again after its second; any
// other failure said and asked again, the wait doubled, until one answers; a 404 no candidates and no failure; a snapshot's reset of
// the gate with a read on its way, every run read again; another ticket, nothing of the first one's kept; a failure of the read that
// met it, never cleared by an older one's answer; a run hidden when its failure is asked again read when it is shown again; a task's
// older candidates read until its attempts accepted anyway are found; and which runs are read (heldRuns), a delivery's run among them. useTicketCandidates reads Nuxt's auto-imports (Vue's, useDispatchApi, onBeforeUnmount) as globals: each is stubbed, on
// fake timers.

function held<T>() {
  let ok!: (v: T) => void
  let fail!: (e: unknown) => void
  const p = new Promise<T>((resolve, reject) => {
    ok = resolve
    fail = reject
  })
  return { p, ok, fail }
}

let sent: Array<{ run: string; after: string } & ReturnType<typeof held<RunCandidatesPage>>>
let unmount: Array<() => void>

beforeEach(() => {
  vi.useFakeTimers()
  sent = []
  unmount = []
  for (const k of ['ref', 'computed', 'shallowReactive', 'watch'] as const) vi.stubGlobal(k, vue[k])
  vi.stubGlobal('onBeforeUnmount', (f: () => void) => unmount.push(f))
  vi.stubGlobal('useDispatchApi', () => ({
    runCandidates: (run: string, after = '') => {
      const h = held<RunCandidatesPage>()
      sent.push({ run, after, ...h })
      return h.p
    },
    // A task's older candidates, noted as `task<before`.
    olderCandidates: (run: string, task: string, before: number) => {
      const h = held<RunCandidatesPage>()
      sent.push({ run, after: `${task}<${before}`, ...h })
      return h.p
    },
  }))
})
afterEach(() => {
  for (const f of unmount) f()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

const tick = (ms = 0) => vi.advanceTimersByTimeAsync(ms)
const held1 = (id: string, signature = 's1', label = ''): HeldRun => ({ id, label, signature })
function page(run: string, task: string, next?: string): RunCandidatesPage {
  const t: TaskCandidates = { task, kind: 'work', more: 0, candidates: [{ task, kind: 'work', attempt: 1, commit: '1'.repeat(40), state: 'succeeded', standing: true, withdrawn: false, receipts: [] }] }
  return { run, managed: true, candidates: [t], ...(next ? { next } : {}) }
}
const tasksOf = (c: { byRun: ReadonlyMap<string, TaskCandidates[]> }, run: string) => c.byRun.get(run)?.map((t) => t.task)

async function candidates(runs: HeldRun[] = [held1('team-0000aaaa')]) {
  const { useTicketCandidates } = await import('./useTicketCandidates')
  const gate = new DispatchOrder()
  const key = vue.ref('CND-1')
  const shown = vue.ref<HeldRun[]>(runs)
  return { gate, key, shown, c: useTicketCandidates({ gate, key: () => key.value, runs: () => shown.value }) }
}

describe("a ticket's runs' candidates", () => {
  it('read a run first shown at once, every page; a move of it gathered for the window', async () => {
    const { c, shown } = await candidates()
    expect(sent.map((s) => [s.run, s.after])).toEqual([['team-0000aaaa', '']])
    expect(c.loading.value).toBe(true)
    sent[0]!.ok(page('team-0000aaaa', 'build', 'build'))
    await tick()
    expect(sent.map((s) => s.after)).toEqual(['', 'build'])
    sent[1]!.ok(page('team-0000aaaa', 'docs'))
    await tick()
    expect(tasksOf(c, 'team-0000aaaa')).toEqual(['build', 'docs'])
    expect([c.loading.value, c.error.value]).toEqual([false, ''])
    // The run moved twice inside the window: one read.
    shown.value = [held1('team-0000aaaa', 's2')]
    await tick()
    shown.value = [held1('team-0000aaaa', 's3')]
    await tick(249)
    expect(sent).toHaveLength(2)
    await tick(1)
    expect(sent).toHaveLength(3)
  })

  it('ask a run_changing reply again after its second, with nothing said as a failure', async () => {
    const { c } = await candidates()
    sent[0]!.fail(new ApiError(503, 'run_changing', 'the run moved through every read'))
    await tick()
    expect(c.error.value).toBe('')
    await tick(999)
    expect(sent).toHaveLength(1)
    await tick(1)
    expect(sent).toHaveLength(2)
    sent[1]!.ok(page('team-0000aaaa', 'build'))
    await tick()
    expect(tasksOf(c, 'team-0000aaaa')).toEqual(['build'])
  })

  it('say any other failure and ask it again, the wait doubled, until one answers, on a run that never moves', async () => {
    const { c } = await candidates([held1('team-0000aaaa', 's1', 'CND-1 run')])
    sent[0]!.fail(new ApiError(503, 'ledger_unavailable', 'the ledger could not be opened'))
    await tick()
    expect(c.error.value).toBe('The candidates of CND-1 run could not be read (the ledger could not be opened); asked again.')
    expect(c.byRun.has('team-0000aaaa')).toBe(false)
    await tick(250)
    expect(sent).toHaveLength(2)
    sent[1]!.fail(new Error('Failed to fetch'))
    await tick()
    expect(c.error.value).toContain('Failed to fetch')
    await tick(499)
    expect(sent).toHaveLength(2)
    await tick(1)
    expect(sent).toHaveLength(3)
    sent[2]!.ok(page('team-0000aaaa', 'build'))
    await tick()
    expect(tasksOf(c, 'team-0000aaaa')).toEqual(['build'])
    expect(c.error.value).toBe('')
    // Answered: nothing more is asked.
    await tick(20_000)
    expect(sent).toHaveLength(3)
  })

  it('cap the wait between failed reads at the re-read maximum', async () => {
    await candidates()
    const waits: number[] = []
    let at = 0
    for (let i = 0; i < 7; i++) {
      sent.at(-1)!.fail(new ApiError(500, 'internal', 'boom'))
      const n = sent.length
      let waited = 0
      while (sent.length === n && waited < 10_000) {
        await tick(50)
        waited += 50
      }
      waits.push(waited)
      at += waited
    }
    expect(waits).toEqual([250, 500, 1000, 2000, 4000, 8000, 8000])
    expect(at).toBe(23_750)
  })

  it('take a run the server knows nowhere (a 404) for one with no candidates, said as no failure and not asked again', async () => {
    const { c } = await candidates()
    sent[0]!.fail(new ApiError(404, 'not_found', 'no such run'))
    await tick()
    expect([c.byRun.get('team-0000aaaa'), c.error.value]).toEqual([[], ''])
    await tick(20_000)
    expect(sent).toHaveLength(1)
  })

  it("read every run again on a snapshot's reset, the reply on its way dropped by the gate", async () => {
    const { c, gate } = await candidates([held1('team-0000aaaa'), held1('team-0000bbbb')])
    expect(sent.map((s) => s.run)).toEqual(['team-0000aaaa', 'team-0000bbbb'])
    sent[1]!.ok(page('team-0000bbbb', 'docs'))
    await tick()
    // The stream started again while aaaa's read is on its way: the page's reset of its gate, then of the candidates.
    gate.reset()
    c.reset()
    expect(sent.map((s) => s.run)).toEqual(['team-0000aaaa', 'team-0000bbbb', 'team-0000aaaa', 'team-0000bbbb'])
    sent[0]!.ok(page('team-0000aaaa', 'old'))
    await tick()
    expect(c.byRun.has('team-0000aaaa')).toBe(false)
    // What was read before the reset stays shown until the new read answers.
    expect(tasksOf(c, 'team-0000bbbb')).toEqual(['docs'])
    sent[2]!.ok(page('team-0000aaaa', 'build'))
    sent[3]!.ok(page('team-0000bbbb', 'docs2'))
    await tick()
    expect(tasksOf(c, 'team-0000aaaa')).toEqual(['build'])
    expect(tasksOf(c, 'team-0000bbbb')).toEqual(['docs2'])
    expect(c.loading.value).toBe(false)
  })

  it("keep nothing of another ticket's: its late reply, its failure and its retry dropped", async () => {
    const { c, key, shown } = await candidates()
    sent[0]!.fail(new ApiError(500, 'internal', 'boom'))
    await tick()
    expect(c.error.value).not.toBe('')
    key.value = 'CND-2'
    shown.value = [held1('team-0000cccc')]
    await tick()
    expect(c.error.value).toBe('')
    expect(sent.map((s) => s.run)).toEqual(['team-0000aaaa', 'team-0000cccc'])
    // CND-1's retry is not asked.
    await tick(20_000)
    expect(sent.map((s) => s.run)).toEqual(['team-0000aaaa', 'team-0000cccc'])
    sent[1]!.ok(page('team-0000cccc', 'build'))
    await tick()
    expect([...c.byRun.keys()]).toEqual(['team-0000cccc'])
    expect(c.runs.value.map((r) => [r.run, r.tasks.length])).toEqual([['team-0000cccc', 1]])
  })

  it("keep a newer read's failure when an older read answers after it, and ask it again", async () => {
    const { c, shown } = await candidates()
    // The first read is held; the run moves, and the read of its move fails.
    shown.value = [held1('team-0000aaaa', 's2')]
    await tick(250)
    expect(sent).toHaveLength(2)
    sent[1]!.fail(new ApiError(500, 'internal', 'boom'))
    await tick()
    expect(c.error.value).toContain('boom')
    // The older read answers: what it read is shown, the newer failure stands and is asked again.
    sent[0]!.ok(page('team-0000aaaa', 'old'))
    await tick()
    expect(tasksOf(c, 'team-0000aaaa')).toEqual(['old'])
    expect(c.error.value).toContain('boom')
    await tick(250)
    expect(sent).toHaveLength(3)
    sent[2]!.ok(page('team-0000aaaa', 'build'))
    await tick()
    expect([tasksOf(c, 'team-0000aaaa'), c.error.value]).toEqual([['build'], ''])
  })

  it("take no older read's failure for the run once a newer read answered", async () => {
    const { c, shown } = await candidates()
    shown.value = [held1('team-0000aaaa', 's2')]
    await tick(250)
    sent[1]!.ok(page('team-0000aaaa', 'build'))
    await tick()
    sent[0]!.fail(new Error('Failed to fetch'))
    await tick()
    expect([tasksOf(c, 'team-0000aaaa'), c.error.value]).toEqual([['build'], ''])
    await tick(20_000)
    expect(sent).toHaveLength(2)
  })

  it('read a run whose failed read was given up while it was hidden at once when it is shown again, at the same signature', async () => {
    const { c, shown } = await candidates()
    sent[0]!.fail(new ApiError(500, 'internal', 'boom'))
    await tick()
    shown.value = []
    await tick(250)
    // Hidden when its failure was asked again: not read.
    expect(sent).toHaveLength(1)
    shown.value = [held1('team-0000aaaa')]
    await tick()
    expect(sent).toHaveLength(2)
    sent[1]!.ok(page('team-0000aaaa', 'build'))
    await tick()
    expect([tasksOf(c, 'team-0000aaaa'), c.error.value]).toEqual([['build'], ''])
  })

  it("read a task's older candidates until its attempts accepted anyway past the six are found, for the Decisions", async () => {
    const { c } = await candidates([held1('team-0000aaaa', 's1', 'CND-1 run')])
    const cand = (attempt: number, accepted = false): RunCandidate => ({
      task: 'build',
      kind: 'work',
      attempt,
      commit: String(attempt).repeat(40).slice(0, 40),
      state: 'failed',
      standing: false,
      withdrawn: true,
      receipts: [],
      ...(accepted ? { acceptedAnyway: { actor: 'alice', at: `2026-11-0${attempt}T00:00:00Z`, command: `acc-${attempt}` } } : {}),
    })
    const tasks = (cs: RunCandidate[], more: number, acceptedOlder = 0): RunCandidatesPage => ({
      run: 'team-0000aaaa',
      managed: true,
      candidates: [{ task: 'build', kind: 'work', candidates: cs, more, withdrawn: 9, acceptedOlder }],
    })
    sent[0]!.ok(tasks([9, 8, 7, 6, 5, 4].map((n) => cand(n)), 3, 1))
    await tick()
    expect(sent.map((s) => s.after)).toEqual(['', 'build<4'])
    sent[1]!.ok(tasks([cand(3)], 2))
    await tick()
    expect(sent.map((s) => s.after)).toEqual(['', 'build<4', 'build<3'])
    sent[2]!.ok(tasks([cand(2, true)], 1))
    await tick()
    // Found: attempt 1 is not read.
    expect(sent).toHaveLength(3)
    expect(c.byRun.get('team-0000aaaa')?.[0]?.candidates.map((x) => x.attempt)).toEqual([9, 8, 7, 6, 5, 4, 3, 2])
    const ticket = { close: { met: {} }, links: [] } as unknown as Parameters<typeof ticketDecisions>[0]['ticket']
    expect(ticketDecisions({ plans: [], ticket, runs: c.runs.value }).map((d) => [d.key, d.by])).toEqual([['anyway:team-0000aaaa/build/2', 'alice']])
  })

  it("read a delivery's run that Runs has not read, for whether its candidate was withdrawn, offered to nothing", async () => {
    const { c } = await candidates([held1('team-0000aaaa'), { id: 'team-0000bbbb', label: '', signature: 's', linked: true }])
    expect(sent.map((s) => s.run)).toEqual(['team-0000aaaa', 'team-0000bbbb'])
    sent[0]!.ok(page('team-0000aaaa', 'build'))
    sent[1]!.ok(page('team-0000bbbb', 'docs'))
    await tick()
    expect(tasksOf(c, 'team-0000bbbb')).toEqual(['docs'])
    expect(c.runs.value.map((r) => r.run)).toEqual(['team-0000aaaa'])
  })

  it('read a run again for the next ticket that holds it too, at the same signature', async () => {
    const { c, key, shown } = await candidates([held1('team-0000aaaa')])
    sent[0]!.ok(page('team-0000aaaa', 'build'))
    await tick()
    // The runs seen before the key, then the key before the runs.
    shown.value = [held1('team-0000aaaa')]
    key.value = 'CND-2'
    await tick()
    expect(sent).toHaveLength(2)
    sent[1]!.ok(page('team-0000aaaa', 'docs'))
    await tick()
    expect(tasksOf(c, 'team-0000aaaa')).toEqual(['docs'])
    key.value = 'CND-3'
    shown.value = [held1('team-0000aaaa')]
    await tick()
    expect(sent).toHaveLength(3)
    sent[2]!.ok(page('team-0000aaaa', 'lint'))
    await tick()
    expect(tasksOf(c, 'team-0000aaaa')).toEqual(['lint'])
  })
})

describe('the runs a ticket holds (heldRuns)', () => {
  const summary = (id: string, p: Partial<RunSummary> = {}): RunSummary => ({ id, label: `run ${id}`, state: 'running', paused: false, startedAt: '', record: false, members: [], tasks: {}, ...p }) as RunSummary
  const launch = (id: string, runId: string): TicketRunEntry => ({ kind: 'launch', launch: { id, state: 'started', runId, label: `launch ${id}` } as TicketRunEntry['launch'] })

  it("leave out a contested chain's run and a run shown twice, in the section's order, with a launch's label for a run unread", async () => {
    const { heldRuns } = await import('./useTicketCandidates')
    const entries: TicketRunEntry[] = [
      { kind: 'run', run: summary('team-0000aaaa') },
      { kind: 'run', run: summary('team-0000bbbb'), contested: { tickets: ['CND-2'] } as unknown as TicketRunEntry['contested'] },
      launch('l1', 'team-0000cccc'),
      { kind: 'link', link: { id: 'k1', kind: 'run', ref: 'team-0000aaaa' } as TicketRunEntry['link'] },
      { kind: 'launch', launch: { id: 'l2', state: 'failed', label: 'never started' } as TicketRunEntry['launch'] },
    ]
    expect(heldRuns(entries, () => undefined).map((r) => [r.id, r.label])).toEqual([
      ['team-0000aaaa', 'run team-0000aaaa'],
      ['team-0000cccc', 'launch l1'],
    ])
  })

  it("add a live delivery's run no entry holds, after the entries, and never a contested chain's", async () => {
    const { heldRuns } = await import('./useTicketCandidates')
    const delivery = (run: string, p: Partial<TicketLink> = {}): TicketLink =>
      ({ id: run, kind: 'delivery', ref: '', url: '', note: '', candidate: `${run}/build/2@${'a'.repeat(40)}`, createdAt: '', createdBy: '', ...p }) as TicketLink
    const entries: TicketRunEntry[] = [
      { kind: 'run', run: summary('team-0000aaaa') },
      { kind: 'run', run: summary('team-0000bbbb'), contested: { tickets: ['CND-2'] } as unknown as TicketRunEntry['contested'] },
    ]
    const links = [
      delivery('team-0000aaaa'),
      delivery('team-0000bbbb'),
      delivery('team-0000cccc'),
      delivery('team-0000cccc'),
      delivery('team-0000dddd', { deletedAt: '2026-11-03T00:00:00Z' }),
      delivery('team-0000eeee', { candidate: 'not a candidate' }),
      { ...delivery('team-0000ffff'), kind: 'run' as const },
    ]
    expect(heldRuns(entries, (id) => (id === 'team-0000cccc' ? 4 : undefined), links).map((r) => [r.id, !!r.linked, r.signature])).toEqual([
      ['team-0000aaaa', false, heldRuns(entries, () => undefined)[0]!.signature],
      ['team-0000cccc', true, JSON.stringify([4, null])],
    ])
  })

  it("move a run's signature with its summary and the live store's revision, and only then", async () => {
    const { heldRuns } = await import('./useTicketCandidates')
    const sig = (s: Partial<RunSummary>, rev?: number) => heldRuns([{ kind: 'run', run: summary('team-0000aaaa', s) }], () => rev)[0]!.signature
    const base = sig({})
    expect(sig({ label: 'renamed' })).toBe(base)
    expect(sig({}, 3)).not.toBe(base)
    expect(sig({ state: 'stopped' })).not.toBe(base)
    expect(sig({ paused: true })).not.toBe(base)
    expect(sig({ outcome: 'succeeded' } as Partial<RunSummary>)).not.toBe(base)
    expect(sig({ stoppedAt: '2026-11-03T00:00:00Z' })).not.toBe(base)
    expect(sig({ tasks: { succeeded: 1 } } as Partial<RunSummary>)).not.toBe(base)
  })
})
