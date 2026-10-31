import type { RunAttempt, RunInfo, RunStep, RunTask } from '~/composables/useSessions'
import { relativeTime, shortCwd } from './sessions'

// A managed run's tasks on the run page (#334, row 7): the counts its
// header says, a task's state in words, which actions apply to it, and the
// commands a person sends with receipts (docs/protocol.md, "Commands with
// receipts"): the browser mints the id, retries a lost reply once with the
// same id, and reads a refusal from the receipt it got. Dispatch's
// operations (useDispatchOps.ts) are sent the same way, with their own body.

/** A command's receipt as the server answers it. */
export interface Receipt {
  commandId: string
  intent: string
  revision: number
  repeat?: boolean
  status: 'done' | 'failed'
  error?: string
  refusal?: string
  /** The task the command changed, as the receipt keeps it; the run has the rest. */
  task?: ReceiptTask
  paused?: boolean
}

/** A task as a receipt keeps it: what the command did to it. */
export interface ReceiptTask {
  id: string
  state: string
  reason?: string
  attempts: number
  result?: { attempt: number, commit?: string }
}

/** The body every command takes. */
export interface CommandBody {
  commandId: string
  expectedRevision?: number
}

/** A command's body with a minted id and, when one is given (above 0), the revision it expects. */
export function commandBody(expectedRevision?: number): CommandBody {
  const body: CommandBody = { commandId: mintCommandId() }
  if (expectedRevision) body.expectedRevision = expectedRevision
  return body
}

/** What a refusal's receipt carries, whatever it answers (a task command's, a Dispatch operation's): the rule's words. */
export interface RefusalReceipt {
  error?: string
  refusal?: string
}

export type TaskCommand = 'cancel' | 'retry' | 'accept'

export interface TaskCounts {
  total: number
  done: number
  failed: number
  cancelled: number
  running: number
  checking: number
  waiting: number
}

/** How many tasks are in each state: done (succeeded), failed, cancelled, running, checking (verifying), waiting (blocked or ready). */
export function taskCounts(tasks: readonly RunTask[] | undefined): TaskCounts {
  const c: TaskCounts = { total: 0, done: 0, failed: 0, cancelled: 0, running: 0, checking: 0, waiting: 0 }
  for (const t of tasks ?? []) {
    c.total++
    switch (t.state) {
      case 'succeeded':
        c.done++
        break
      case 'failed':
        c.failed++
        break
      case 'cancelled':
        c.cancelled++
        break
      case 'running':
        c.running++
        break
      case 'verifying':
        c.checking++
        break
      default:
        c.waiting++
    }
  }
  return c
}

export type BadgeColor = 'success' | 'warning' | 'error' | 'neutral' | 'info'

/** A header badge: its words, its colour, and the longer words its title carries (a pause's why). */
export interface TaskBadge {
  label: string
  color: BadgeColor
  title?: string
}

/**
 * The header's badges for a managed run: `3/7 tasks`, then what is wrong or moving (`1 failed`, `2 running`, `1 checking`), `paused`
 * (titled with why the run paused itself, when it did) and `cleaning up` while trees are being ended (EN1: their services stopped, their
 * cleanup steps run).
 */
export function taskBadges(run: Pick<RunInfo, 'tasks' | 'paused' | 'outcome' | 'pausedWhy' | 'cleaning'>): TaskBadge[] {
  const c = taskCounts(run.tasks)
  const out: TaskBadge[] = [{ label: `${c.done}/${c.total} tasks`, color: run.outcome === 'succeeded' ? 'success' : 'neutral' }]
  if (c.failed) out.push({ label: `${c.failed} failed`, color: 'error' })
  if (c.cancelled) out.push({ label: `${c.cancelled} cancelled`, color: 'neutral' })
  if (c.running) out.push({ label: `${c.running} running`, color: 'success' })
  if (c.checking) out.push({ label: `${c.checking} checking`, color: 'info' })
  if (run.paused) out.push(run.pausedWhy ? { label: 'paused', color: 'warning', title: `Paused: ${run.pausedWhy}` } : { label: 'paused', color: 'warning', title: 'Paused by a person' })
  if ((run.cleaning ?? 0) > 0) out.push({ label: 'cleaning up', color: 'info', title: `${run.cleaning} ${run.cleaning === 1 ? 'tree is' : 'trees are'} being ended: services stopped, cleanup steps run` })
  return out
}

export type Tone = 'warning' | 'error' | 'muted' | 'default' | 'success'

/** A task's state in words: what it waits for, which attempt runs, how it ended; on an interrupted run, what its continuation takes up. */
export function taskWords(t: RunTask, run: Pick<RunInfo, 'stoppedAt' | 'paused'> & { state?: string; pausedWhy?: string }): { text: string; tone: Tone } {
  const n = t.attempt?.n ?? t.attempts
  switch (t.state) {
    case 'blocked':
      return { text: t.reason || (t.needs?.length ? `needs ${t.needs.join(', ')}` : 'waiting'), tone: t.reason?.includes('will not') ? 'error' : 'muted' }
    case 'ready': {
      const why = t.reason ? `${t.reason}; ` : ''
      if (run.state === 'interrupted') return { text: `${why}taken up when the run is continued`, tone: 'warning' }
      if (run.stoppedAt) return { text: `${why}ready, the run is stopped`, tone: 'muted' }
      // The run paused itself over this task's own reason (a project manifest's step that failed in a tree the agent had not
      // touched, EN1: failUnusable gives the task and the pause the same words): the pause is the news, the reason why. A reason
      // that is not the pause's (one kept from an attempt the server's end interrupted, or this task's failure while another's
      // paused the run) is not presented as its cause: it reads as before, the pause after it.
      if (run.paused && run.pausedWhy && t.reason && t.reason === run.pausedWhy) return { text: `ready, the dispatch is paused: ${t.reason}`, tone: 'warning' }
      if (run.paused) return { text: `${why}ready, scheduling is paused`, tone: 'warning' }
      return { text: `${why}ready to start`, tone: 'default' }
    }
    case 'running':
      if (t.blocker) return { text: `attempt ${n} blocked by ${t.blocker.by}: ${t.blocker.reason}`, tone: 'warning' }
      return { text: `attempt ${n} running`, tone: 'default' }
    case 'verifying':
      return { text: `attempt ${n}: checking the result`, tone: 'default' }
    case 'succeeded': {
      const r = t.result
      if (!r) return { text: 'succeeded', tone: 'success' }
      const kept = t.importedFrom ? ' (kept from the run continued)' : ''
      if (t.kind === 'review') return { text: `${r.verdict === 'needs_changes' ? `asked for changes (${r.findings?.length ?? 0} findings)` : `approved${r.findings?.length ? ` with ${r.findings.length} notes` : ''}`}${kept}`, tone: 'success' }
      return { text: `succeeded by attempt ${r.attempt}${kept}${r.summary ? `: ${r.summary}` : ''}`, tone: 'success' }
    }
    case 'failed':
      return { text: t.reason || 'failed', tone: 'error' }
    case 'cancelled':
      return { text: 'cancelled', tone: 'muted' }
  }
  return { text: t.state, tone: 'muted' }
}

/** A task's state as a dot: running green, checking blue, failed red, done the brand colour, waiting hollow, cancelled grey. */
export function taskDot(state: RunTask['state']): string {
  switch (state) {
    case 'running':
      return 'bg-success'
    case 'verifying':
      return 'bg-info'
    case 'failed':
      return 'bg-error'
    case 'succeeded':
      return 'bg-primary'
    case 'blocked':
      return 'bg-transparent ring-1 ring-neutral-400'
  }
  return 'bg-neutral-400'
}

export interface TaskActionSet {
  retry: boolean
  accept: boolean
  cancel: boolean
  open: boolean
  copyBranch: boolean
  showResult: boolean
  /** The attempt's preview URL while its worktree answers behind it (EN1), else ''. */
  preview: string
}

/**
 * Which actions apply to a task: Retry on a failed or cancelled task, Accept anyway on a failed task whose last attempt submitted a commit,
 * Cancel on a live one. None that changes a run continued: its continuation owns its tasks (`409 run_continued`). Open preview follows the
 * attempt's `preview` alone: the server sets it at its worktree's `tree.ready` and clears it at the attempt's close, at a service's own
 * exit and while the run stops, so nothing here second-guesses it.
 */
export function taskActions(t: RunTask, run: Pick<RunInfo, 'stoppedAt' | 'record' | 'resumedBy'>): TaskActionSet {
  const live = !run.stoppedAt && !run.record && !run.resumedBy
  const terminal = t.state === 'succeeded' || t.state === 'failed' || t.state === 'cancelled'
  return {
    retry: live && (t.state === 'failed' || t.state === 'cancelled'),
    accept: live && t.state === 'failed' && t.kind === 'work' && !!t.attempt?.result,
    cancel: live && !terminal,
    open: !!t.attempt?.sessionId,
    copyBranch: !!t.attempt?.branch,
    showResult: !!t.result,
    preview: t.attempt?.preview ?? '',
  }
}

/** A step's duration in words: `4.2 s` under ten seconds, `60 s` under two minutes, `3 min 5 s` after. */
export function stepDuration(ms: number): string {
  if (ms < 10_000) return `${(Math.max(0, ms) / 1000).toFixed(1)} s`
  const s = Math.round(ms / 1000)
  if (s < 120) return `${s} s`
  return s % 60 ? `${Math.floor(s / 60)} min ${s % 60} s` : `${Math.floor(s / 60)} min`
}

/**
 * A receipt's outcome in words (EN1): `exited 0 in 4.2 s`, `exited 1 in 3.0 s`, `timed out after 60 s`, `ready after 3 tries`,
 * `not ready after 30 tries`, a service's `exited 0 after 12 s` or `ended after 12 s` (a signal: the tree's end stops it), `running
 * since 2m ago`, `starting`, `interrupted`, and `failed in 2.0 s (exited 0)` for a step that exited 0 with an error. The failure's
 * own words are the step's `error`, shown beside these.
 */
export function stepWords(s: RunStep, now = Date.now()): { text: string; tone: Tone } {
  const tries = (n: number) => `${n} ${n === 1 ? 'try' : 'tries'}`
  switch (s.state) {
    case 'started':
      return { text: 'starting', tone: 'muted' }
    case 'running':
      return { text: `running since ${relativeTime(s.startedAt, now, { suffix: true })}`, tone: 'default' }
    case 'interrupted':
      return { text: 'interrupted', tone: 'warning' }
  }
  const d = stepDuration(s.duration ?? 0)
  if (s.phase === 'ready') {
    // A probe passed only with exit 0 and no error: a command probe whose child kept its output open exits 0 and fails.
    if (s.exit === 0 && !s.timedOut && !s.error) return { text: `ready after ${tries(s.tries)}`, tone: 'success' }
    if (!s.tries) return { text: 'never tried', tone: 'error' }
    return { text: `not ready after ${tries(s.tries)}`, tone: 'error' }
  }
  if (s.phase === 'service') {
    if (s.exit === undefined || s.exit < 0) return { text: `ended after ${d}`, tone: s.error ? 'error' : 'muted' }
    return { text: `exited ${s.exit} after ${d}`, tone: s.error ? 'error' : 'muted' }
  }
  if (s.timedOut) return { text: `timed out after ${d}`, tone: 'error' }
  if (s.exit === undefined || s.exit < 0) return { text: s.error ? 'did not run' : `ended after ${d}`, tone: 'error' }
  // A step that exited 0 and still failed (a check or verify step, or a setup step in a result's checkout, that changed tracked
  // files): its error says how, and it never reads as a success.
  if (s.exit === 0 && s.error) return { text: `failed in ${d} (exited 0)`, tone: 'error' }
  return { text: `exited ${s.exit} in ${d}`, tone: s.exit === 0 ? 'success' : 'error' }
}

/**
 * Why the run's prerequisites failed with no receipt to show it (EN1): the run's pause words when they are the prerequisites'
 * refusal ("the prerequisites failed: …", docs/protocol.md: a manifest `env` name the server does not allow or the host does not
 * have, checked before any command runs, or a receipt the ledger refused), else ''. A prerequisite command that failed has its
 * receipt, and its words ("the prerequisite <id> failed: …") are not this.
 */
export function prerequisitesRefused(run: Pick<RunInfo, 'paused' | 'pausedWhy'>): string {
  const why = run.pausedWhy ?? ''
  return run.paused && why.startsWith('the prerequisites failed: ') ? why : ''
}

/**
 * Whether a receipt's Log can be asked for: once the step ended, with a log the server kept (its SHA-256 is on the receipt, an empty
 * log's among them: a quiet command's log is kept empty and answers 200) or one it dropped past the run's bound (Log then says so).
 * A receipt with neither has no log (its route answers 404): a check the attempt closed under, a step the recovery concluded.
 */
export function stepHasLog(s: Pick<RunStep, 'state' | 'logSha256' | 'logDropped'>): boolean {
  return (s.state === 'finished' || s.state === 'interrupted') && (!!s.logSha256 || !!s.logDropped)
}

/** A receipt's command as shown: a probe over HTTP is `GET <url>`; an argv is its words, one in quotes when it holds a space or a quote. */
export function stepCommand(s: Pick<RunStep, 'argv' | 'url'>): string {
  if (s.url) return `GET ${s.url}`
  return (s.argv ?? []).map((a) => (a === '' || /[\s"'\\]/.test(a) ? JSON.stringify(a) : a)).join(' ')
}

/** The manifest that produced a receipt: `crew:3` as it is, `file:<blob>` as `file: <its first 12>`; '' for a task's check. */
export function stepManifest(m?: string): string {
  if (!m) return ''
  if (m.startsWith('file:')) return `file: ${m.slice(5, 17)}`
  return m
}

/** Where a receipt ran: the attempt's worktree, its result's checkout (`<worktree>.check`), or the directory itself (a prerequisite's: the crew's). */
export function stepWhere(s: Pick<RunStep, 'dir'>, attempt?: Pick<RunAttempt, 'worktree'>): string {
  const dir = s.dir ?? ''
  const wt = attempt?.worktree
  if (wt && dir) {
    if (dir === `${wt}.check` || dir.startsWith(`${wt}.check/`)) return 'checkout'
    if (dir === wt || dir.startsWith(`${wt}/`)) return 'worktree'
  }
  return shortCwd(dir)
}

/** A receipt's ports as words: `web 41234, db 40112`. */
export function stepPorts(p?: Record<string, number>): string {
  return Object.entries(p ?? {})
    .map(([name, n]) => `${name} ${n}`)
    .join(', ')
}

/** A command id the browser mints: 16 hex characters, from the platform's random bytes where there are any. */
export function mintCommandId(): string {
  const bytes = new Uint8Array(8)
  const c = (globalThis as { crypto?: { getRandomValues?: (b: Uint8Array) => void } }).crypto
  if (c?.getRandomValues) c.getRandomValues(bytes)
  else for (let i = 0; i < bytes.length; i++) bytes[i] = Math.floor(Math.random() * 256)
  return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('')
}

/** A command the run refused, or a Dispatch operation the store refused: the receipt says why (its `error`), and nothing changed. */
export class CommandRefused<R extends RefusalReceipt = Receipt> extends Error {
  constructor(
    message: string,
    public receipt: R,
  ) {
    super(message)
  }
}

/**
 * Sends a command, or a Dispatch operation, with its body, which carries the id the browser minted (a command's `commandId`, from
 * commandBody; an operation's `opId`), and resolves to the whole reply. A reply lost to the network is asked for once more with the same
 * body, so the same id, which the server answers as the first (the receipt); a second loss is the error. A refusal (a 409, or a Dispatch
 * operation's 404, carrying the receipt) is a CommandRefused with the server's words.
 */
export async function sendCommand<B, R extends { receipt: RefusalReceipt } = { receipt: Receipt }>(send: (body: B) => Promise<R>, body: B): Promise<R> {
  for (let attempt = 1; ; attempt++) {
    try {
      return await send(body)
    } catch (e) {
      const err = e as { status?: number; message?: string; body?: { receipt?: R['receipt'] } }
      if (err.status === 0 && attempt === 1) continue
      if (err.body?.receipt) throw new CommandRefused<R['receipt']>(err.body.receipt.error || err.message || 'refused', err.body.receipt)
      throw e
    }
  }
}
