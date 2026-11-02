import type { DispatchChange, DispatchReceipt, DispatchTicket, LaunchSummary, NewPlanReference, PlanAgent, PlanBody, PlanSummary } from '~/composables/useDispatch'
import type { RunInfo } from '~/composables/useSessions'

// A ticket's Plan section in words (Program item DS2, docs/plans/dispatch-evidence.md, "web/app"): a plan revision's state and why its
// approval no longer holds, with the one remedy that cures it; the checks a revision expects; why Launch is not offered; the reference
// field's parser; a launch's default label; and what a launch's reply came to. Pure: the Plan section, its dialogs and the palette's rows
// read these, vitest proves them (dispatchPlans.test.ts). "Plan" on a ticket's page and `r3` beside a run, never "plan" alone where the
// agent's plan could be meant (the plan's Owner decisions, 18).

/** The bounds the store applies, so a dialog says what is too long before it sends (internal/dispatch, model.go). */
export const PLAN_BOUNDS = {
  references: 20,
  referenceNote: 200,
  referencePath: 1024,
  attachments: 8,
  comments: 20,
  constraints: 8 << 10,
  decisions: 10,
  decisionQuestion: 500,
  decisionBefore: 8,
  note: 4 << 10,
  approvalNote: 1000,
  label: 60,
  base: 256,
} as const

/** A revision's short name beside a run: `r3`. */
export function revisionTag(n: number): string {
  return `r${n}`
}

/** A commit as a page shows it: its first 12 characters (the full one in a title). */
export function shortCommit(commit: string): string {
  return commit.slice(0, 12)
}

/** A revision's state as a badge says it. */
export interface PlanStateWord {
  label: string
  color: 'primary' | 'success' | 'warning' | 'neutral'
  icon: string
}

/** The badge of a revision's state; an approved one whose approval no longer holds says so. */
export function planStateWord(p: Pick<PlanSummary, 'state' | 'stale'>): PlanStateWord {
  switch (p.state) {
    case 'approved':
      return p.stale ? { label: 'approval stale', color: 'warning', icon: 'i-lucide-badge-alert' } : { label: 'approved', color: 'success', icon: 'i-lucide-badge-check' }
    case 'proposed':
      return { label: 'proposed', color: 'primary', icon: 'i-lucide-file-pen-line' }
    case 'superseded':
      return { label: 'superseded', color: 'neutral', icon: 'i-lucide-history' }
    default:
      return { label: 'withdrawn', color: 'neutral', icon: 'i-lucide-ban' }
  }
}

/** One reason a revision is not as it was approved or made: its words, the remedy that cures it, whether it stops a launch. */
export interface PlanReason {
  kind: 'stale' | 'agents' | 'crew'
  words: string
  remedy: 'approve' | 'revise'
  blocks: boolean
}

/**
 * Why a revision is not as it was approved or made, each with its remedy: a stale approval (the store's words: Approve again after an edit
 * of the title, the description or the criteria; Revise after a comment it hands over was edited or deleted), an agent that resolves
 * otherwise in the catalog now (Revise: the body holds the selection approved, and only a new revision covers another), the saved crew
 * edited since or gone (Revise, to take the crew as it is now; the revision keeps the crew it was made from, which is what runs, so it
 * stops nothing).
 */
export function planReasons(p: Pick<PlanSummary, 'n' | 'state' | 'stale' | 'agentsChanged' | 'crewChanged' | 'crewName'>): PlanReason[] {
  const out: PlanReason[] = []
  if (p.state === 'approved' && p.stale) out.push({ kind: 'stale', words: p.stale.words, remedy: p.stale.remedy === 'approve' ? 'approve' : 'revise', blocks: true })
  if (p.state !== 'withdrawn' && p.agentsChanged) {
    out.push({ kind: 'agents', words: `An agent of revision ${p.n} resolves otherwise in the catalog now (its command, its recipes' arguments or its environment)`, remedy: 'revise', blocks: true })
  }
  if (p.state !== 'withdrawn' && p.crewChanged) {
    out.push({ kind: 'crew', words: `The crew ${p.crewName} was edited or removed since revision ${p.n} was made; the revision keeps the crew as it was`, remedy: 'revise', blocks: false })
  }
  return out
}

/**
 * The one remedy offered for an approved revision whose approval no longer holds: Approve again when an intent edit is all that stands in
 * the way, Revise when anything the body holds changed (a comment handed over, an agent), never Approve again then, since an approval
 * cannot change an immutable body and the server would refuse it. Null when the approval holds, or for a revision not approved.
 */
export function planRemedy(p: Pick<PlanSummary, 'n' | 'state' | 'stale' | 'agentsChanged' | 'crewChanged' | 'crewName'>): 'approve' | 'revise' | null {
  if (p.state !== 'approved') return null
  const blocking = planReasons(p).filter((r) => r.blocks)
  if (!blocking.length) return null
  return blocking.some((r) => r.remedy === 'revise') ? 'revise' : 'approve'
}

/** Whether Approve is offered on a revision: a proposed or a superseded one, or an approved one whose only staleness an approval cures. */
export function approvable(p: Pick<PlanSummary, 'n' | 'state' | 'stale' | 'agentsChanged' | 'crewChanged' | 'crewName'>): boolean {
  if (p.agentsChanged) return false
  if (p.state === 'proposed' || p.state === 'superseded') return true
  return planRemedy(p) === 'approve'
}

/** A check a revision expects: a task's check, or a project manifest's verify step, run in a result's checkout after the task's checks. */
export interface ExpectedCheck {
  phase: 'check' | 'verify'
  /** The task whose result it checks; none for a verify step, which checks every work task's. */
  task?: string
  id: string
  argv: string[]
  timeoutSeconds?: number
}

/** The checks a revision's body expects: each work task's checks in the crew's order, then the manifest's verify steps (the crew's own, else the file form's). */
export function expectedChecks(body: Pick<PlanBody, 'crew' | 'manifestFile'>): ExpectedCheck[] {
  const out: ExpectedCheck[] = []
  for (const t of body.crew.tasks ?? []) for (const c of t.checks ?? []) out.push({ phase: 'check', task: t.id, id: c.id, argv: [...c.argv], timeoutSeconds: c.timeoutSeconds })
  const verify = body.crew.manifest?.file ? body.manifestFile?.manifest?.verify : body.crew.manifest?.verify
  for (const c of verify ?? []) out.push({ phase: 'verify', id: c.id, argv: [...c.argv], timeoutSeconds: c.timeoutSeconds })
  return out
}

/** An argv as one line: each element quoted when it holds a space, a quote or nothing, so where one ends is plain. Shown, never run. */
export function argvLine(argv: readonly string[]): string {
  return argv.map((a) => (a === '' || /[\s"'\\]/.test(a) ? JSON.stringify(a) : a)).join(' ')
}

/**
 * What a member's agent runs as the revision resolved it: the catalog's command, the session recipe's start arguments (`{id}` as the
 * session's id, filled at the start), the yolo recipe's arguments when yolo is on, then the member's own arguments.
 */
export function resolvedArgv(agent: Pick<PlanAgent, 'command' | 'startArgs' | 'yoloArgs'> | undefined, memberArgs: readonly string[] = []): string[] {
  if (!agent) return [...memberArgs]
  return [...agent.command, ...agent.startArgs, ...agent.yoloArgs, ...memberArgs]
}

/** The yolo choice said plainly (the plan's Owner decisions, 7): it is what an approval most needs to show. */
export function yoloWords(yolo: boolean | undefined): { label: string; words: string; tone: 'warning' | 'neutral' } {
  return yolo
    ? { label: 'Yolo on', words: 'The agents run without their permission prompts: nothing they do asks first.', tone: 'warning' }
    : { label: 'Yolo off', words: 'The agents ask before they act, as they do on their own.', tone: 'neutral' }
}

/** What the server is known to refuse a launch for, learned from its replies (no read says it beforehand): managed runs off, no ledger. */
export interface LaunchServer {
  /** The words of a `managed_off` refusal. */
  managedOff?: string
  /** The words of a `ledger_unavailable` refusal. */
  noLedger?: string
}

/** What `launchBlockers` judges: the ticket shown, its project, the revision Launch would name, and what the server is known to refuse. */
export interface LaunchContext {
  ticket: Pick<DispatchTicket, 'key' | 'status' | 'deletedAt'>
  project: string
  archived: boolean
  server?: LaunchServer
}

/**
 * Why Launch is not offered from `revision` (the ticket's approved one; null for none), in words, the first the one to say: no approved
 * revision, a stale approval or an agent changed (each with its remedy), the ticket deleted or closed, the project archived, managed runs
 * off on the server or its ledger unavailable. Empty when Launch is offered; the server still decides.
 */
export function launchBlockers(revision: Pick<PlanSummary, 'n' | 'state' | 'stale' | 'agentsChanged' | 'crewChanged' | 'crewName'> | null, o: LaunchContext): string[] {
  const out: string[] = []
  if (o.ticket.deletedAt) out.push(`${o.ticket.key} is deleted: restore it to launch`)
  else if (o.ticket.status === 'done' || o.ticket.status === 'cancelled') out.push(`${o.ticket.key} is ${o.ticket.status}: reopen it to launch`)
  if (o.archived) out.push(`${o.project} is archived: unarchive it to launch`)
  if (!revision || revision.state !== 'approved') out.push('No plan revision is approved: approve one to launch')
  else {
    for (const r of planReasons(revision)) {
      if (!r.blocks) continue
      out.push(`${r.words}: ${r.remedy === 'approve' ? 'approve it again or revise it' : 'revise it'}`)
    }
  }
  if (o.server?.managedOff) out.push(o.server.managedOff)
  if (o.server?.noLedger) out.push(o.server.noLedger)
  return out
}

/** A ticket's approved revision, the one Launch names: the newest approved (a merge may leave two, each its own to launch or withdraw). */
export function approvedRevision<T extends Pick<PlanSummary, 'state' | 'createdAt' | 'id'>>(plans: readonly T[]): T | null {
  let best: T | null = null
  for (const p of plans) {
    if (p.state !== 'approved') continue
    if (!best || p.createdAt > best.createdAt || (p.createdAt === best.createdAt && p.id > best.id)) best = p
  }
  return best
}

/**
 * The reference field's parser: `path`, `path:12`, `path:12-30`, each with `=note` after it (`internal/api/session.go:12-30=the loop`).
 * The note runs from the first `=`; the line range is the digits after the path's last `:`. The path is relative to the crew's working
 * directory: never absolute, never leaving it (`..`), at most 1,024 bytes; a range starts at 1 and ends at its start or after; a note is
 * at most 200 characters. The server resolves the path at the base and refuses one that is not there.
 */
export function parseReference(text: string): { ref: NewPlanReference } | { error: string } {
  const raw = text.trim()
  if (!raw) return { error: 'A reference names a path' }
  const eq = raw.indexOf('=')
  const left = (eq < 0 ? raw : raw.slice(0, eq)).trim()
  const note = eq < 0 ? '' : raw.slice(eq + 1).trim()
  let path = left
  let start: number | undefined
  let end: number | undefined
  const m = /^(.*):(\d+)(?:-(\d+))?$/.exec(left)
  if (m) {
    path = m[1]!
    start = Number(m[2])
    end = m[3] === undefined ? undefined : Number(m[3])
  }
  path = path.replace(/^\.\/+/, '').replace(/\/+$/, '')
  if (!path) return { error: 'A reference names a path before its lines' }
  if (path.startsWith('/') || /^[A-Za-z]:[\\/]/.test(path) || path.startsWith('\\')) return { error: `${path}: a reference is relative to the crew's folder, never absolute` }
  if (path.split(/[\\/]/).some((part) => part === '..')) return { error: `${path}: a reference never leaves the crew's folder` }
  if (/[\u0000-\u001f\u007f]/.test(path)) return { error: `${path}: a path holds no control character` }
  if (new TextEncoder().encode(path).length > PLAN_BOUNDS.referencePath) return { error: `The path is longer than ${PLAN_BOUNDS.referencePath} bytes` }
  if (start !== undefined && start < 1) return { error: `${path}: lines are numbered from 1` }
  if (start !== undefined && end !== undefined && end < start) return { error: `${path}: the range ${start}-${end} ends before it starts` }
  if (Array.from(note).length > PLAN_BOUNDS.referenceNote) return { error: `The note on ${path} is longer than ${PLAN_BOUNDS.referenceNote} characters` }
  const ref: NewPlanReference = { path }
  if (start !== undefined) ref.start = start
  if (end !== undefined && end !== start) ref.end = end
  if (note) ref.note = note
  return { ref }
}

/** A reference as its field says it, the parser's own form: `path:12-30=note`. */
export function referenceText(r: NewPlanReference): string {
  let s = r.path
  if (r.start) s += `:${r.start}`
  if (r.start && r.end && r.end !== r.start) s += `-${r.end}`
  if (r.note) s += `=${r.note}`
  return s
}

/**
 * A launched run's default label, as the store makes it when none is given: the ticket's key and title, cut to 60 characters at a word
 * (`CND-12 Fix the login loop`), never inside the key.
 */
export function defaultLabel(key: string, title: string): string {
  const s = `${key} ${title}`
  const r = Array.from(s)
  if (r.length <= PLAN_BOUNDS.label) return s
  let cut = r.slice(0, PLAN_BOUNDS.label).join('')
  const i = cut.lastIndexOf(' ')
  if (i > key.length) cut = cut.slice(0, i)
  return cut.replace(/ +$/, '')
}

/** What a launch's reply came to: its run started, the launch failed with why, still pending, or the operation refused before any run. */
export type LaunchOutcome =
  | { state: 'started'; launch: LaunchSummary; run: RunInfo | null; repeat: boolean }
  | { state: 'failed'; words: string; launch: LaunchSummary | null }
  | { state: 'pending'; words: string; launch: LaunchSummary | null }
  | { state: 'refused'; words: string; code: string; receipt?: DispatchReceipt }

/** A failure as the launch route answers it: an ApiError's status, code, message and the whole reply (`{error, receipt?, launch?}`). */
export interface LaunchFailure {
  status: number
  code: string
  message: string
  body?: unknown
}

/**
 * A launch's reply in its outcome: a 201 (or a repeat's 200) is the run started, with its link; a `409 launch_failed` the launch kept
 * failed, with why; a `503 ledger_unavailable` carrying a launch is one still pending, which the same operation sent again settles; any
 * other failure refused the launch before a run was made (a stale approval, a ticket closed, managed runs off, no ledger), its words the
 * receipt's when it kept one, else the server's.
 */
export function launchOutcome(r: { reply: { receipt: DispatchReceipt; launch: LaunchSummary | null; run: RunInfo | null } } | { failure: LaunchFailure }): LaunchOutcome {
  if ('reply' in r) {
    const { reply } = r
    if (reply.launch?.state === 'started') return { state: 'started', launch: reply.launch, run: reply.run, repeat: !!reply.receipt.repeat }
    if (reply.launch?.state === 'failed') return { state: 'failed', words: reply.launch.why || 'the run could not be started', launch: reply.launch }
    return { state: 'pending', words: 'The launch is still being started: send it again to settle it', launch: reply.launch }
  }
  const f = r.failure
  const body = (f.body ?? {}) as { receipt?: DispatchReceipt; launch?: LaunchSummary | null }
  if (f.code === 'launch_failed') return { state: 'failed', words: body.launch?.why || f.message || 'the run could not be started', launch: body.launch ?? null }
  if (f.code === 'ledger_unavailable' && body.launch) return { state: 'pending', words: f.message, launch: body.launch }
  return { state: 'refused', words: body.receipt?.error || f.message, code: f.code, receipt: body.receipt }
}

/** A launch as the history's create row names it: what never changes of it (the run's id, the revision, the label, who made it, when). */
export interface KnownLaunch {
  id: string
  runId: string
  planId: string
  label: string
  createdAt: string
  createdBy: string
}

/**
 * The launches a ticket's history names, newest first: each launch's create row (entity `launch`, op `create`), whose value is the launch's
 * summary as made. The history read so far is all the page has of them; a ticket's runs page by page are the Runs section's.
 */
export function historyLaunches(changes: readonly DispatchChange[]): KnownLaunch[] {
  const out: KnownLaunch[] = []
  for (const c of changes) {
    if (c.entity !== 'launch' || c.op !== 'create') continue
    let v: Partial<KnownLaunch> = {}
    try {
      v = JSON.parse(c.value) as Partial<KnownLaunch>
    } catch {
      v = {}
    }
    out.push({ id: c.entityId, runId: v.runId ?? '', planId: v.planId ?? '', label: v.label ?? '', createdAt: v.createdAt ?? c.at, createdBy: v.createdBy ?? c.by ?? c.actor })
  }
  return out.sort((a, b) => b.createdAt.localeCompare(a.createdAt) || b.id.localeCompare(a.id))
}

/** A launch's state as its row says it. */
export function launchStateWords(l: Pick<LaunchSummary, 'state' | 'why'>): { label: string; color: 'success' | 'error' | 'neutral'; words: string } {
  if (l.state === 'started') return { label: 'started', color: 'success', words: 'Its run started' }
  if (l.state === 'failed') return { label: 'failed', color: 'error', words: l.why || 'The run could not be started' }
  return { label: 'pending', color: 'neutral', words: 'Recorded; its run is being started, or the server stopped before it could tell' }
}
