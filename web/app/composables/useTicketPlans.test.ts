import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as vue from 'vue'
import { ApiError } from './useApi'
import type { DispatchReceipt, DispatchTicket, LaunchSummary, PlanReply, PlanSummary } from './useDispatch'
import { useDispatchOps } from './useDispatchOps'
import { DispatchOrder } from '../utils/dispatchOrder'
import { useTicketPlans } from './useTicketPlans'

// A ticket page's plan state (Program item DS2, PR-10) against replies held back and let go in the order a test picks: an operation's
// answer, a 404 and a launch's answer go through the page's ordering gate, so a snapshot's reset or a later read is never undone by an
// answer to a request sent before it. useTicketPlans reads Nuxt's auto-imports (Vue's, useDispatchApi, useDispatchOps, useIdentity,
// useToast) as globals: each is stubbed, the API by replies the test holds. They are read when the composable is called, so the module
// is imported once with the file, never inside a test: an import that took seconds on a loaded machine timed the first test out, and
// its body, still running, then took the next test's requests (#281).

/** A reply the test lets go: `ok` answers, `fail` rejects. */
function held<T>() {
  let ok!: (v: T) => void
  let fail!: (e: unknown) => void
  const p = new Promise<T>((resolve, reject) => {
    ok = resolve
    fail = reject
  })
  return { p, ok, fail }
}

function plan(p: Partial<PlanSummary> & { id: string; rev: number }): PlanSummary {
  return { ticketId: 't1', n: 1, crewId: 'c1', crewName: 'crew', base: 'abc', digest: '', bytes: 0, state: 'proposed', createdAt: '2026-11-01T00:00:00Z', createdBy: 'admin', fingerprint: '', ...p } as PlanSummary
}
function launchOf(id: string, rev = 1): LaunchSummary {
  return { id, ticketId: 't1', ticketKey: 'PL-1', planId: 'p1', planN: 1, runId: `run-${id}`, label: 'PL-1', ticketRev: 1, intent: '', criteria: '', packageSha256: '', state: 'started', agents: [], createdAt: '2026-11-01T00:00:00Z', createdBy: 'admin', fingerprint: '', rev } as LaunchSummary
}
const receipt = { opId: 'op', rev: 1, intent: '', status: 'done' } as DispatchReceipt

/** Every request the page sends, held by name until the test lets it go. */
let calls: Map<string, Array<ReturnType<typeof held<unknown>>>>
function hold<T>(name: string): Promise<T> {
  const h = held<unknown>()
  if (!calls.has(name)) calls.set(name, [])
  calls.get(name)!.push(h)
  return h.p as Promise<T>
}
/** The `i`th request of that name the page sent. */
function sent(name: string, i = 0) {
  const h = calls.get(name)?.[i]
  if (!h) throw new Error(`no ${name} request ${i} sent`)
  return h
}
const settle = () => new Promise((resolve) => setTimeout(resolve, 0))

beforeEach(() => {
  calls = new Map()
  for (const k of ['ref', 'computed', 'reactive', 'shallowReactive', 'watch'] as const) vi.stubGlobal(k, vue[k])
  vi.stubGlobal('onBeforeUnmount', () => {})
  vi.stubGlobal('useToast', () => ({ add: () => {} }))
  vi.stubGlobal('useIdentity', () => ({ name: vue.ref('') }))
  vi.stubGlobal('useDispatchOps', useDispatchOps)
  vi.stubGlobal('useDispatchApi', () => ({
    plans: () => hold('plans'),
    plan: () => hold('plan'),
    history: () => Promise.resolve({ changes: [] }),
    launchRead: () => hold('launchRead'),
    proposePlan: () => hold('propose'),
    approvePlan: () => hold('approve'),
    withdrawPlan: () => hold('withdraw'),
    launch: () => hold('launch'),
  }))
})
afterEach(() => vi.unstubAllGlobals())

async function page() {
  const gate = new DispatchOrder()
  const ticket = { key: 'PL-1', rev: 3 } as DispatchTicket
  const plans = useTicketPlans({ gate, key: () => 'PL-1', ticket: () => ticket })
  return { gate, plans }
}

describe("a ticket page's plan", () => {
  it("drops an approval answered after a snapshot's reset to a request sent before it: the restored store's list stands", async () => {
    const { gate, plans } = await page()
    const p1 = plan({ id: 'p1', rev: 4 })
    void plans.read()
    sent('plans').ok([p1])
    await settle()
    const approving = plans.approve(p1, '')
    // The stream starts again on a store restored from a backup that has no revision at all.
    gate.reset()
    plans.reset()
    void plans.read()
    sent('plans', 1).ok([])
    await settle()
    sent('approve').ok({ receipt, plan: plan({ id: 'p1', rev: 5, state: 'approved' }) } satisfies PlanReply)
    await approving
    await settle()
    expect(plans.plans.value).toEqual([])
    expect(calls.get('plans')).toHaveLength(2)
  })

  it('shows an approval answered on the store the page shows over an older list read, and reads the list again', async () => {
    const { plans } = await page()
    const p1 = plan({ id: 'p1', rev: 4 })
    void plans.read()
    sent('plans').ok([p1])
    await settle()
    const approving = plans.approve(p1, '')
    sent('approve').ok({ receipt, plan: plan({ id: 'p1', rev: 5, state: 'approved' }) } satisfies PlanReply)
    await approving
    expect(plans.plans.value.map((p) => [p.id, p.rev, p.state])).toEqual([['p1', 5, 'approved']])
    expect(calls.get('plans')).toHaveLength(2)
  })

  it('drops a proposal answered after a reset to a request sent before it', async () => {
    const { gate, plans } = await page()
    void plans.read()
    sent('plans').ok([])
    await settle()
    const proposing = plans.propose({ crew: 'c1' })
    gate.reset()
    plans.reset()
    sent('propose').ok({ receipt, plan: plan({ id: 'p9', rev: 1 }) } satisfies PlanReply)
    await proposing
    expect(plans.plans.value).toEqual([])
  })

  it("never lets a slow 404 of the list empty what a later read found (the ticket made since by another browser)", async () => {
    const { plans } = await page()
    void plans.read().catch(() => {})
    void plans.read().catch(() => {})
    sent('plans', 1).ok([plan({ id: 'p1', rev: 1 })])
    await settle()
    sent('plans', 0).fail(new ApiError(404, 'no such ticket', 'not_found'))
    await settle()
    expect(plans.plans.value.map((p) => p.id)).toEqual(['p1'])
    expect(plans.listed.value?.map((p) => p.id)).toEqual(['p1'])
  })

  it('shows a 404 of the list that is the latest answer as no revision', async () => {
    const { plans } = await page()
    void plans.read().catch(() => {})
    sent('plans').fail(new ApiError(404, 'no such ticket', 'not_found'))
    await settle()
    expect(plans.listed.value).toEqual([])
  })

  it("forgets a launch it made at a snapshot's reset, and does not remember one answered after it to a request sent before", async () => {
    const { gate, plans } = await page()
    const p1 = plan({ id: 'p1', rev: 5, state: 'approved' })
    void plans.read()
    sent('plans').ok([p1])
    await settle()
    const first = plans.launch(p1, '', 'op-1')
    sent('launch').ok({ receipt, launch: launchOf('l1'), run: null })
    await first
    expect(plans.known.value.map((l) => l.id)).toEqual(['l1'])
    // A store restored from a backup that lacks l1: it is no longer listed, and never left "reading".
    gate.reset()
    plans.reset()
    expect(plans.known.value).toEqual([])
    const second = plans.launch(p1, '', 'op-2')
    gate.reset()
    plans.reset()
    sent('launch', 1).ok({ receipt, launch: launchOf('l2'), run: null })
    await second
    expect(plans.known.value).toEqual([])
    expect(plans.launches.size).toBe(0)
  })
})
