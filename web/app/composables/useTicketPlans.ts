import type { InjectionKey, ShallowRef } from 'vue'
import { ApiError } from './useApi'
import type { PaletteItem } from './usePalette'
import type { DispatchTicket, LaunchSummary, NewPlan, OpBody, PlanBody, PlanReply, PlanSummary } from './useDispatch'
import { dispatchFailureWords } from './useDispatchOps'
import { through, throughOrAbsent, type DispatchOrder } from '~/utils/dispatchOrder'
import { retried } from '~/utils/dispatchOrder'
import { approvedRevision, launchOutcome, launchUnresolved, launchesToRead, scanLaunches, withAnswers, type KnownLaunch, type LaunchOutcome, type LaunchServer } from '~/utils/dispatchPlans'
import { mintCommandId } from '~/utils/tasks'
import { RUN_EVENT_WINDOW_MS } from '~/utils/runs'
import { DISPATCH_RETRY_MAX_MS } from './useDispatchLive'

/**
 * The ticket page's plan rows for the palette's page group (pages/dispatch/[project]/[number].vue provides it, the ticket page fills it):
 * Propose a plan revision, Approve revision n, Launch from revision n (asks first) and Open run.
 */
export const TICKET_PLAN_ROWS: InjectionKey<ShallowRef<PaletteItem[]>> = Symbol('ticketPlanRows')

/** How many of a ticket's launches the Plan section reads and shows, the newest: every one is the Runs section's. */
export const LAUNCHES_SHOWN = 5

/** The rows of a page of the history the launches are found in: the route's largest. */
const LAUNCH_SCAN_PAGE = 500

/**
 * A ticket page's plan revisions and launches (Program item DS2, docs/plans/dispatch-evidence.md, "web/app"): the revisions' summaries
 * (GET …/{key}/plans), the body of each revision opened (immutable, read once by its id), and the launches the ticket's whole history
 * names (scanned to its end, then from where the last scan stopped, whatever pages the history section shows), the newest LAUNCHES_SHOWN
 * read by their ids (GET /api/dispatch/launches/{id}) until each is started or failed, which never changes again, every read through the
 * page's ordering gate. The page reads them with the ticket (its `read` is among the page's reload, so a failure is asked again as a
 * re-read is), which the live events do at once for a `plan` or a `launch` event of the ticket, as for any event naming it; a launch the
 * scan finds is read as it appears. Nothing polls. The operations (propose, approve, withdraw, launch) are the page's, each one operation
 * with an op id the browser mints; a revision an operation answered with is shown over an older list read, by its `rev`, and a launch an
 * operation answered with goes through the gate as a read of it does.
 */
export function useTicketPlans(o: { gate: DispatchOrder; key: () => string; ticket: () => DispatchTicket | null }) {
  const api = useDispatchApi()
  const ops = useDispatchOps(o.gate)
  const identity = useIdentity()

  /** The revisions as the list read them, newest first; null until the first read answered. */
  const listed = ref<PlanSummary[] | null>(null)
  /** The revisions the operations answered with, by id: newer than a list read sent before them and served after. */
  const answered = shallowReactive(new Map<string, PlanSummary>())
  /** The bodies read, by revision id: a body never changes. */
  const bodies = shallowReactive(new Map<string, PlanBody>())
  /** Why a body could not be read, by revision id. */
  const bodyErrors = shallowReactive(new Map<string, string>())
  /** The launches read, by id. */
  const launches = shallowReactive(new Map<string, LaunchSummary>())
  /** The launches this page made, before the history's scan names them. */
  const made = ref<KnownLaunch[]>([])
  /** The launches the history's scans found, by id, and the seq the next scan starts after. */
  const scanned = shallowReactive(new Map<string, KnownLaunch>())
  let scannedAfter = 0
  /**
   * Counts what makes an answer another ticket's or an older store's (a key changed, a reset): a scan of an earlier one is dropped, and a
   * launch answered to a request sent before it is not remembered as this page's.
   */
  let gen = 0
  /** The scans, one after another: each starts where the one before stopped. */
  let scanning: Promise<void> = Promise.resolve()
  /** What the server is known to refuse a launch for, from its replies. */
  const server = reactive<LaunchServer>({})

  /** The ticket's key the page shows: its revisions are read by it, beside the ticket's own read. */
  const key = () => o.key()

  /** The revisions shown: the list's, each replaced by an operation's answer that is later (a higher `rev`). */
  const plans = computed<PlanSummary[]>(() => {
    const shownTicket = o.ticket()
    return withAnswers(listed.value ?? (shownTicket?.key === key() ? shownTicket.approved : undefined) ?? [], answered)
  })
  const approved = computed(() => approvedRevision(plans.value))
  /** The revision shown in full: the approved one, else the newest proposed. */
  const featured = computed(() => approved.value ?? plans.value.find((p) => p.state === 'proposed') ?? null)
  /** The launches known, newest first: the history's, and this page's own not in it yet. */
  const known = computed<KnownLaunch[]>(() => {
    const out = [...scanned.values()]
    for (const m of made.value) if (!scanned.has(m.id)) out.push(m)
    return out.sort((a, b) => b.createdAt.localeCompare(a.createdAt) || b.id.localeCompare(a.id))
  })
  const shown = computed(() => known.value.slice(0, LAUNCHES_SHOWN))

  function notFound(e: unknown): boolean {
    return e instanceof ApiError && e.status === 404
  }

  async function readList(): Promise<void> {
    const k = key()
    if (!k) return
    // A 404 (no ticket by the key yet) is ordered as a list is: a slow one never empties what a later read found.
    await throughOrAbsent(o.gate, `plans:${k}`, () => api.plans(k), {
      apply: (ps) => {
        if (k === key()) listed.value = ps
      },
      absent: notFound,
      gone: () => {
        if (k === key()) listed.value = []
      },
    })
  }

  /** Reads a revision's body, once: it never changes. A failure is said where the body would be, and is not asked again until the next read. */
  async function readBody(id: string): Promise<void> {
    if (bodies.has(id)) return
    try {
      const p = await api.plan(id)
      bodies.set(id, p.body)
      bodyErrors.delete(id)
    } catch (e) {
      bodyErrors.set(id, (e as Error).message)
      throw e
    }
  }

  /** Scans the history for launches from where the last scan stopped, after the scans before it. Rejects when a page could not be read. */
  function scanHistory(): Promise<void> {
    const k = key()
    const g = gen
    const next = scanning.catch(() => {}).then(async () => {
      if (!k || g !== gen || k !== key()) return
      try {
        const r = await scanLaunches((after) => api.history(k, after, LAUNCH_SCAN_PAGE), scannedAfter)
        if (g !== gen || k !== key()) return
        for (const l of r.launches) scanned.set(l.id, l)
        scannedAfter = r.after
      } catch (e) {
        if (!notFound(e)) throw e
      }
    })
    scanning = next
    return next
  }

  async function readLaunch(id: string): Promise<void> {
    try {
      await through(
        o.gate,
        `launch:${id}`,
        () => api.launchRead(id),
        (l) => {
          const { package: _package, ...summary } = l
          launches.set(id, summary)
        },
        (l) => l.rev,
      )
    } catch (e) {
      if (!notFound(e)) throw e
    }
  }

  /**
   * Everything the section shows, read again: the list and the history's launches since the last scan, then the body of the revision shown
   * in full and the launches shown that may still change (not read yet, or pending). Rejects when a read failed.
   */
  async function read(): Promise<void> {
    await Promise.all([readList(), scanHistory()])
    const f = featured.value
    await Promise.all([f ? readBody(f.id) : Promise.resolve(), ...launchesToRead(shown.value, launches).map((id) => readLaunch(id))])
  }

  // A launch a scan finds, or this page made, that is not read yet: read as it appears, asked again until it answers, as a re-read is.
  let gone = false
  onBeforeUnmount(() => (gone = true))
  watch(
    () => shown.value.map((l) => l.id).join(','),
    () => {
      for (const l of shown.value) {
        if (launches.has(l.id)) continue
        const k = key()
        void retried({
          read: () => readLaunch(l.id),
          stop: () => gone || k !== key() || launches.has(l.id),
          wait: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
          firstMs: RUN_EVENT_WINDOW_MS,
          maxMs: DISPATCH_RETRY_MAX_MS,
        })
      }
    },
  )

  // Another ticket on the page: nothing of this one's stays.
  watch(
    () => o.key(),
    (id, old) => {
      if (id === old) return
      listed.value = null
      answered.clear()
      launches.clear()
      made.value = []
      forgetScan()
      forgetRefusals()
    },
  )

  /** The history's scans forgotten: the next starts from the history's first change. */
  function forgetScan(): void {
    gen++
    scanned.clear()
    scannedAfter = 0
  }

  /** What the server was known to refuse a launch for, forgotten: the next launch asks it again (a ledger taken over since, a restart). */
  function forgetRefusals(): void {
    delete server.managedOff
    delete server.noLedger
  }

  /**
   * The stream started again (a snapshot), maybe on a store restored from a backup whose `rev`s are below those shown, or a server started
   * again: the answers kept, the launches read, scanned and made here, and what the server refused launches for are forgotten, and shown
   * as the next read finds them (a launch made here that the restored store lacks is no longer listed). An operation's answer to a
   * request sent before it is dropped by the gate. The bodies stay: each is named by its id and never changes.
   */
  function reset(): void {
    answered.clear()
    launches.clear()
    made.value = []
    forgetScan()
    forgetRefusals()
  }

  /**
   * How an operation's answer is shown, applied by the gate (ops.run's `apply`, `rev`): only when the gate takes it, so an answer to a
   * request sent before a snapshot's reset, or under a later one on the same revision, is dropped, and only while the page shows the
   * ticket it was made on. Taken, the revision is shown over an older list read and the list is read again (the approval superseded another).
   */
  function answering(k: string) {
    return {
      apply: (r: PlanReply) => {
        const p = r.plan
        if (!p || k !== key()) return
        const before = answered.get(p.id)
        if (!before || before.rev <= p.rev) answered.set(p.id, p)
        void readList().catch(() => {})
      },
      rev: (r: PlanReply) => r.plan?.rev,
    }
  }

  /** Proposes a revision; the reply's revision, or throws the failure in words. Its thing is its own: a revision nothing has shown yet. */
  async function propose(req: NewPlan): Promise<PlanSummary | null> {
    const k = key()
    const reply = await ops.run<PlanReply>((body) => api.proposePlan(k, { ...body, ...req }), { thing: `propose:${k}:${mintCommandId()}`, what: `${k}: the plan revision`, raise: true, ...answering(k) })
    return reply?.plan ?? null
  }

  /** The operator's approval of a revision, at the revision and the ticket shown, with a note. */
  async function approve(p: PlanSummary, note: string): Promise<PlanSummary | null> {
    const t = o.ticket()
    const k = key()
    const reply = await ops.run<PlanReply>((body) => api.approvePlan(p.id, { ...body, ...(t ? { ticketRev: t.rev } : {}), ...(note.trim() ? { note: note.trim() } : {}) }), {
      thing: `plan:${p.id}`,
      what: `Revision ${p.n}`,
      expectedRev: p.rev,
      raise: true,
      ...answering(k),
    })
    return reply?.plan ?? null
  }

  /** A revision withdrawn; said in a toast either way. */
  async function withdraw(p: PlanSummary): Promise<boolean> {
    const reply = await ops.run<PlanReply>((body) => api.withdrawPlan(p.id, body), { thing: `plan:${p.id}`, what: `Revision ${p.n}`, expectedRev: p.rev, done: `Revision ${p.n} withdrawn`, ...answering(key()) })
    return !!reply
  }

  /**
   * Launches a managed run from the revision `p`, at the ticket's revision shown, under the op id given (minted by the dialog, so the same
   * launch sent again after a lost reply, or while it is pending, is the same operation, never a second run): one network loss is asked
   * again at once with it, a second is thrown, as is a server failure that is no refusal (`launchUnresolved`: the launch may have been
   * kept), so the dialog sends the same operation again. A refusal that says the server has managed runs off, or no ledger, is kept and
   * said by Launch until a launch is made, a snapshot, or the person asks again (`forgetRefusals`). The launch an answer carries goes
   * through the gate as a read of it: a slow answer never replaces a later state a read showed. It is remembered as this page's only when
   * its request was sent after the last reset, on the ticket shown: one sent before a snapshot is the restored store's to name, or not.
   */
  async function launch(p: PlanSummary, label: string, opId: string): Promise<LaunchOutcome> {
    const t = o.ticket()
    const k = key()
    const body: OpBody & { plan: string; label?: string } = { opId, plan: p.id }
    if (t?.rev) body.expectedRev = t.rev
    const name = identity.name.value.trim()
    if (name) body.byName = name
    if (label.trim()) body.label = label.trim()
    let out: LaunchOutcome
    let n = 0
    let g = gen
    for (let attempt = 1; ; attempt++) {
      n = o.gate.ask()
      g = gen
      try {
        out = launchOutcome({ reply: await api.launch(k, body) })
        break
      } catch (e) {
        if (!(e instanceof ApiError)) throw e
        if (e.status === 0 && attempt === 1) continue
        if (launchUnresolved(e)) throw e
        out = launchOutcome({ failure: { status: e.status, code: e.code, message: e.message, body: e.body } })
        break
      }
    }
    if (out.state === 'refused') {
      if (out.code === 'managed_off') server.managedOff = out.words
      if (out.code === 'ledger_unavailable') server.noLedger = out.words
    } else {
      delete server.managedOff
      delete server.noLedger
    }
    const l = out.state === 'refused' ? null : out.launch
    if (l) {
      if (k === key() && o.gate.take(`launch:${l.id}`, n, l.rev)) launches.set(l.id, l)
      if (g === gen && k === key() && !made.value.some((m) => m.id === l.id)) made.value = [...made.value, { id: l.id, runId: l.runId, planId: l.planId, label: l.label, createdAt: l.createdAt, createdBy: l.createdBy }]
    }
    return out
  }

  return { plans, listed, approved, featured, bodies, bodyErrors, readBody, launches, known, shown, server, forgetRefusals, read, reset, propose, approve, withdraw, launch, busy: ops.busy, mintOpId: mintCommandId, failureWords: dispatchFailureWords }
}

export type TicketPlans = ReturnType<typeof useTicketPlans>
