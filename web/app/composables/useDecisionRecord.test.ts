import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as vue from 'vue'
import type { Decision } from '~/utils/decisions'
import type { RunInfo } from './useSessions'
// Imported with the file, not inside a test: a module loaded in a test is loaded on the test's 5 s, which a loaded machine spends on the
// load alone. Their auto-imports are globals read when they are called, stubbed below.
import { decisionsLive, useDecisionRecord, useDecisionRun } from './useDecisions'
import { useTicketDecisions } from './useTicketDecisions'

// Codex's round 2 on #719, on the pages that read decisions (Program item DR1, docs/plans/decisions.md, "web/app"), wired to an API
// whose every request the test holds and lets go (or fails) in the order it needs: the card's record, read one at a time, so a reply of
// the same revision never lands before an earlier one; a stream come back after a store restored from a backup, whose revisions are
// below the one shown; a ticket page's read that fails with another asked meanwhile; and the card's run, kept open across a restart
// that leaves it a record. The composables read Nuxt's auto-imports (Vue's, useApi, useAttention, onBeforeUnmount) as globals.

function held<T>() {
  let ok!: (v: T) => void
  let fail!: (e: unknown) => void
  const p = new Promise<T>((resolve, reject) => {
    ok = resolve
    fail = reject
  })
  return { p, ok, fail }
}

/** Every request sent, by path, held until the test lets it go. */
let sent: Array<{ path: string } & ReturnType<typeof held<unknown>>>
/** Every refreshRun asked, by run, held likewise. */
let refreshed: Array<{ id: string } & ReturnType<typeof held<RunInfo | null>>>
let liveRuns: vue.Ref<RunInfo[]>
let unmount: Array<() => void>

beforeEach(() => {
  sent = []
  refreshed = []
  unmount = []
  liveRuns = vue.ref([])
  for (const k of ['ref', 'computed', 'watch'] as const) vi.stubGlobal(k, vue[k])
  vi.stubGlobal('onBeforeUnmount', (f: () => void) => unmount.push(f))
  vi.stubGlobal('useApi', () => ({
    request: (path: string) => {
      const h = held<unknown>()
      sent.push({ path, ...h })
      return h.p
    },
  }))
  vi.stubGlobal('useAttention', () => ({
    runOf: (id: string) => liveRuns.value.find((r) => r.id === id),
    refreshRun: (id: string) => {
      const h = held<RunInfo | null>()
      refreshed.push({ id, ...h })
      return h.p
    },
  }))
})
afterEach(() => {
  for (const f of unmount) f()
  vi.unstubAllGlobals()
})

/** Lets the microtasks and the watchers run. */
const settle = async () => {
  for (let i = 0; i < 5; i++) await new Promise((r) => setTimeout(r, 0))
}

const RUN = 'e2e-crew-0a1b2c3d'
const record = (rev: number, state: Decision['state'], run = RUN): Decision => ({
  id: 'dr-7',
  run,
  openedIn: RUN,
  origin: 'plan',
  type: 'approval',
  request: { question: 'Ship it?' },
  policy: { approver: 'operator' },
  state,
  rev,
  createdAt: '2026-11-07T10:00:00Z',
})
const ev = (rev: number) => ({ id: 'dr-7', run: RUN, state: 'open', rev })

describe("the card's record, read one at a time", () => {
  it('takes the later of two reads of the same revision: a continuation names another run without a new revision', async () => {
    const r = useDecisionRecord(() => 'dr-7')
    await settle()
    expect(sent).toHaveLength(1)
    // The run is continued (the record now names run B, at the same revision), and its event asks for a read while the first is under way.
    decisionsLive().stream('decision', ev(2))
    await settle()
    expect(sent).toHaveLength(1)
    sent[0]!.ok({ decision: record(2, 'open', 'run-a') })
    await settle()
    expect(r.decision.value?.run).toBe('run-a')
    // The read asked meanwhile is made only now, after the first answered: its reply, the later one, stands.
    expect(sent).toHaveLength(2)
    sent[1]!.ok({ decision: record(2, 'open', 'run-b') })
    await settle()
    expect(r.decision.value?.run).toBe('run-b')
    expect(sent).toHaveLength(2)
  })

  it('reads again after a read that failed when another was asked meanwhile, and says the failure until one is read', async () => {
    const r = useDecisionRecord(() => 'dr-7')
    await settle()
    decisionsLive().stream('decision', ev(2))
    sent[0]!.fail(new Error('the server is busy'))
    await settle()
    expect(r.error.value).toBe('the server is busy')
    expect(sent).toHaveLength(2)
    sent[1]!.ok({ decision: record(2, 'answered') })
    await settle()
    expect(r.decision.value?.state).toBe('answered')
    expect(r.error.value).toBe('')
  })

  it('takes what a store restored from a backup holds after the stream comes back, below the revision shown, and drops a reply asked before', async () => {
    const r = useDecisionRecord(() => 'dr-7')
    await settle()
    sent[0]!.ok({ decision: record(5, 'applied') })
    await settle()
    expect(r.decision.value?.rev).toBe(5)
    // A read under way when the server goes down; the stream comes back to a store restored at revision 1, open.
    decisionsLive().stream('decision', ev(5))
    await settle()
    expect(sent).toHaveLength(2)
    decisionsLive().stream('snapshot', [])
    // The old server's reply to the read asked before the snapshot comes late: dropped, whatever its revision.
    sent[1]!.ok({ decision: record(6, 'applied') })
    await settle()
    expect(r.decision.value?.rev).toBe(5)
    // A write answered before the read of the restored store is dropped too: the store is not known yet.
    r.apply(record(7, 'applied'))
    expect(r.decision.value?.rev).toBe(5)
    expect(sent).toHaveLength(3)
    sent[2]!.ok({ decision: record(1, 'open') })
    await settle()
    expect([r.decision.value?.rev, r.decision.value?.state]).toEqual([1, 'open'])
    // From there the revisions order it again: Read again takes the store's next one, a write's older reply does not land.
    void r.read()
    await settle()
    sent[3]!.ok({ decision: record(2, 'answered') })
    await settle()
    expect(r.decision.value?.rev).toBe(2)
    r.apply(record(1, 'open'))
    expect(r.decision.value?.rev).toBe(2)
  })
  it("keeps the read under way on the fallback's cadence while the stream is down: only its coming back starts a new generation", async () => {
    const r = useDecisionRecord(() => 'dr-7')
    await settle()
    decisionsLive().down()
    sent[0]!.ok({ decision: record(3, 'answered') })
    await settle()
    expect(r.decision.value?.rev).toBe(3)
    // The read the tick asked is made after it, and ordered by revision: an older one does not land.
    expect(sent).toHaveLength(2)
    sent[1]!.ok({ decision: record(2, 'open') })
    await settle()
    expect(r.decision.value?.rev).toBe(3)
  })
})

describe("a ticket page's decisions", () => {
  it('reads again after a read that failed when a decision changed meanwhile', async () => {
    const t = useTicketDecisions(() => 'EV-1')
    await settle()
    expect(sent.map((s) => s.path)).toEqual(['/api/dispatch/tickets/EV-1/decisions'])
    decisionsLive().stream('decision', ev(1))
    sent[0]!.fail(new Error('the server is busy'))
    await settle()
    expect(t.error.value).toBe('the server is busy')
    expect(sent).toHaveLength(2)
    sent[1]!.ok({ decisions: [record(1, 'open')] })
    await settle()
    expect(t.decisions.value?.map((d) => d.id)).toEqual(['dr-7'])
    expect(t.error.value).toBe('')
  })
})

describe("the card's run, kept open across a restart", () => {
  const running = { id: RUN, state: 'running' } as unknown as RunInfo
  it('reads the run again when the live store lets it go, after it was read before the store held it', async () => {
    const held = useDecisionRun(() => RUN)
    await settle()
    // The card opened before the live store had loaded: the run is read through its own route, running.
    expect(refreshed.map((r) => r.id)).toEqual([RUN])
    refreshed[0]!.ok(running)
    await settle()
    expect(held.value).toBe(true)
    // The store loads it, then a restart leaves it a record the store leaves out: it is read again, not judged by the old read.
    liveRuns.value = [running]
    await settle()
    expect(held.value).toBe(true)
    liveRuns.value = []
    await settle()
    expect(held.value).toBeUndefined()
    expect(refreshed).toHaveLength(2)
    refreshed[1]!.ok({ ...running, state: 'interrupted', record: true } as unknown as RunInfo)
    await settle()
    expect(held.value).toBe(false)
  })

  it("drops a read's reply that comes after the store took the run", async () => {
    const held = useDecisionRun(() => RUN)
    await settle()
    liveRuns.value = [running]
    await settle()
    liveRuns.value = []
    await settle()
    expect(refreshed).toHaveLength(2)
    // The first read, asked before the store held the run, answers last with the run running: dropped, the second's record stands.
    refreshed[1]!.ok({ ...running, record: true } as unknown as RunInfo)
    await settle()
    refreshed[0]!.ok(running)
    await settle()
    expect(held.value).toBe(false)
  })
})
