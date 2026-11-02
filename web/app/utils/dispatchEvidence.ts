import type { CandidateReceipt, DispatchChange, LaunchChanges, RunCandidate, RunSummary, TaskCandidates, TicketRunEntry } from '~/composables/useDispatch'
import type { Finding, RunInfo, RunStep, RunTask, SessionInfo } from '~/composables/useSessions'
import { isEnded, liveAttention } from './attention'

// A ticket's runs and their evidence in words (Program item DS2, docs/plans/dispatch-evidence.md, "web/app"): the entries of a ticket's
// runs and the run each names, a run's candidates grouped by task and attempt, each receipt's chip (passed, failed with its exit, timed
// out, interrupted, log dropped), the repairs (a failed check, then the attempt that started at the failed result; a review's
// needs_changes, then the reviewed task's attempts at the commit it reviewed), "changed since" a launch in words, and the questions and
// blockers of a run, its questions counted from the live sessions. Pure: the Runs section and its parts read these, vitest proves them
// (dispatchEvidence.test.ts). A receipt is shown by what ran, where, against which commit and how it ended, never by a process.

/** A badge's words and colour. */
export interface Chip {
  label: string
  color: 'success' | 'error' | 'warning' | 'info' | 'neutral'
  icon?: string
  title?: string
}

/** An entry's identity among a ticket's runs: its kind and what it is of (the launch, the link, the run). */
export function entryKey(e: TicketRunEntry): string {
  return `${e.kind}:${e.launch?.id ?? e.link?.id ?? e.run?.id ?? ''}`
}

/** The run an entry shows: its summary's, else a started launch's run, else a hand link's; '' for a launch with no run. */
export function entryRunId(e: TicketRunEntry): string {
  if (e.run) return e.run.id
  if (e.kind === 'launch') return e.launch?.state === 'started' ? (e.launch.runId ?? '') : ''
  return e.link?.ref ?? ''
}

/**
 * The entries of the pages read, in their order, each once: a page read again after a change may repeat an entry the page before it
 * ended on only if the order moved under the reads, and the first one stands.
 */
export function mergeEntries(pages: ReadonlyArray<{ entries: readonly TicketRunEntry[] }>): TicketRunEntry[] {
  const seen = new Set<string>()
  const out: TicketRunEntry[] = []
  for (const p of pages) {
    for (const e of p.entries ?? []) {
      const k = entryKey(e)
      if (seen.has(k)) continue
      seen.add(k)
      out.push(e)
    }
  }
  return out
}

/** A run summary's tasks as the run page's badges say them: done of all, then failed, cancelled, running, checking, paused. */
export function summaryBadges(s: Pick<RunSummary, 'tasks' | 'paused' | 'outcome'>): Chip[] {
  const n = (st: RunTask['state']) => s.tasks?.[st] ?? 0
  const total = Object.values(s.tasks ?? {}).reduce((a, b) => a + (b ?? 0), 0)
  const out: Chip[] = [{ label: `${n('succeeded')}/${total} tasks`, color: s.outcome === 'succeeded' ? 'success' : 'neutral' }]
  if (n('failed')) out.push({ label: `${n('failed')} failed`, color: 'error' })
  if (n('cancelled')) out.push({ label: `${n('cancelled')} cancelled`, color: 'neutral' })
  if (n('running')) out.push({ label: `${n('running')} running`, color: 'success' })
  if (n('verifying')) out.push({ label: `${n('verifying')} checking`, color: 'info' })
  if (s.paused) out.push({ label: 'paused', color: 'warning' })
  return out
}

/** A run summary's state as a badge, its questions counted as the page counts them (runQuestions). */
export function summaryState(s: Pick<RunSummary, 'state' | 'stoppedAt' | 'outcome' | 'record'>, questions: number): Chip {
  if (s.stoppedAt) return { label: 'Stopped', color: 'neutral' }
  if (s.outcome) return { label: s.outcome === 'succeeded' ? 'Succeeded' : s.outcome === 'failed' ? 'Failed' : 'Cancelled', color: s.outcome === 'succeeded' ? 'success' : s.outcome === 'failed' ? 'error' : 'neutral' }
  if (s.state === 'interrupted') return { label: 'Interrupted', color: 'warning' }
  if (s.state === 'finished') return { label: 'Finished', color: 'neutral' }
  if (questions > 0) return { label: `Needs input ${questions}`, color: 'warning' }
  return { label: s.record ? 'Recorded' : 'Running', color: s.record ? 'neutral' : 'success' }
}

/** A member of a run that waits on a person now: its name, its session, the question's words. */
export interface WaitingMember {
  member: string
  sessionId: string
  message: string
}

/**
 * A run's questions: the members whose live session waits on a person, joined by the session id the summary names (or, for a run the
 * live store holds, the run's own members), as runState counts them (utils/runs.ts, memberStatus: an ended member, or an ended session,
 * waits on no one). A question needs no run event: a member's session that comes to wait changes the session, not the run. Only a run
 * whose record answers, with no live session to join, takes the summary's own count, and names no member.
 */
export function runQuestions(s: Pick<RunSummary, 'record' | 'members' | 'questions'>, live: Pick<RunInfo, 'id' | 'members'> | undefined, sessions: readonly SessionInfo[]): { count: number; waiting: WaitingMember[] } {
  if (s.record && !live) return { count: s.questions ?? 0, waiting: [] }
  const members = live ? live.members.map((m) => ({ name: m.name, sessionId: m.sessionId, status: m.status })) : s.members
  const waiting: WaitingMember[] = []
  for (const m of members) {
    if (m.status === 'ended') continue
    const session = (m.sessionId && sessions.find((x) => x.id === m.sessionId)) || (live && sessions.find((x) => x.crew?.runId === live.id && x.crew.member === m.name)) || undefined
    if (!session || isEnded(session.status)) continue
    const a = liveAttention(session.attention)
    if (a?.state !== 'needs_input') continue
    waiting.push({ member: m.name, sessionId: session.id, message: a.message ?? '' })
  }
  return { count: waiting.length, waiting }
}

/**
 * What the Runs section watches to read its summaries again, gathered for the window (gather), by run: for each run it shows, the live
 * store's reading of it (its revision, state, pause, outcome, stop, and its members with their sessions and statuses), which the store
 * reads again on the run's events and whenever a member session's status changes, and the status of each member session the live run
 * names. A question is not in it: the page counts those from the live sessions without a read.
 */
export function runsSignature(runs: readonly string[], runOf: (id: string) => RunInfo | undefined, sessions: readonly SessionInfo[]): Record<string, string> {
  const out: Record<string, string> = {}
  for (const id of runs) {
    const l = runOf(id)
    out[id] = JSON.stringify(
      l ? [l.managed?.revision ?? 0, l.state, !!l.paused, l.outcome ?? '', l.stoppedAt ?? '', l.members.map((m) => [m.sessionId ?? '', m.status, m.sessionId ? (sessions.find((s) => s.id === m.sessionId)?.status ?? '') : ''])] : null,
    )
  }
  return out
}

/**
 * Whether a run shown in both readings changed between them: a run that only came or went with a read of the pages is not a change (the
 * read that brought it is its summary), so a read never asks for itself.
 */
export function runsChanged(before: Record<string, string>, now: Record<string, string>): boolean {
  return Object.keys(now).some((id) => id in before && before[id] !== now[id])
}

/**
 * Asks made within `ms` of the first gathered into one call of `read`, as the runs store gathers run events (RUN_EVENT_WINDOW_MS): one
 * read for a burst, and one more for an ask made while a read is on its way. `stop` ends it.
 */
export function gather(read: () => Promise<unknown>, ms: number, timers: { set: (f: () => void, ms: number) => unknown; clear: (t: unknown) => void } = { set: (f, d) => setTimeout(f, d), clear: (t) => clearTimeout(t as ReturnType<typeof setTimeout>) }) {
  let timer: unknown
  let reading = false
  let again = false
  let stopped = false
  const fire = async () => {
    timer = undefined
    if (stopped) return
    if (reading) {
      again = true
      return
    }
    reading = true
    try {
      await read()
    } catch {
      // A failed read waits for the next ask, an event or a snapshot.
    } finally {
      reading = false
    }
    if (again && !stopped) {
      again = false
      ask()
    }
  }
  function ask() {
    if (stopped || timer !== undefined) return
    timer = timers.set(() => void fire(), ms)
  }
  function stop() {
    stopped = true
    if (timer !== undefined) timers.clear(timer)
    timer = undefined
  }
  return { ask, stop }
}

/** Whether a receipt passed: finished with exit 0, its tree unchanged by it. */
export function receiptPassed(r: Pick<CandidateReceipt, 'state' | 'exit' | 'stable' | 'timedOut' | 'killed'>): boolean {
  return r.state === 'finished' && r.exit === 0 && r.stable && !r.timedOut && !r.killed
}

/** Whether a receipt failed: it ended and did not pass (running or started is neither). */
export function receiptFailed(r: Pick<CandidateReceipt, 'state' | 'exit' | 'stable' | 'timedOut' | 'killed'>): boolean {
  return (r.state === 'finished' || r.state === 'interrupted') && !receiptPassed(r)
}

/**
 * A receipt's chips: how it ended (passed; failed with its exit or its signal; timed out; interrupted; changed the tree it checked;
 * running), then "log dropped" when the run's logs were at their bound, its hash kept on the receipt.
 */
export function receiptChips(r: CandidateReceipt): Chip[] {
  const out: Chip[] = []
  if (r.state === 'started' || r.state === 'running') out.push({ label: 'running', color: 'info', icon: 'i-lucide-loader' })
  else if (r.timedOut) out.push({ label: 'timed out', color: 'error', icon: 'i-lucide-timer-off' })
  else if (r.state === 'interrupted' || r.killed) out.push({ label: 'interrupted', color: 'warning', icon: 'i-lucide-circle-slash' })
  else if (r.signal) out.push({ label: `failed (${r.signal})`, color: 'error', icon: 'i-lucide-circle-x' })
  else if (r.exit !== undefined && r.exit !== 0) out.push({ label: `failed (exit ${r.exit})`, color: 'error', icon: 'i-lucide-circle-x' })
  else if (!r.stable) out.push({ label: 'changed the tree', color: 'error', icon: 'i-lucide-circle-x', title: 'It exited 0 but changed tracked files: a check must leave the tree as it found it' })
  else out.push({ label: 'passed', color: 'success', icon: 'i-lucide-circle-check' })
  if (r.logDropped) out.push({ label: 'log dropped', color: 'neutral', icon: 'i-lucide-file-x', title: "The log was not kept: the run's logs were at their bound. Its SHA-256 stays on the receipt." })
  return out
}

/** A receipt's state as one word, for a test's hook and a screen reader: passed, failed, running or interrupted. */
export function receiptState(r: CandidateReceipt): 'passed' | 'failed' | 'running' | 'interrupted' {
  if (r.state === 'started' || r.state === 'running') return 'running'
  if (receiptPassed(r)) return 'passed'
  if (!r.timedOut && (r.state === 'interrupted' || r.killed)) return 'interrupted'
  return 'failed'
}

/** A receipt's name: its phase, its id, and its run when there were more than one ("check fixed", "verify lint #2"). */
export function receiptName(r: Pick<CandidateReceipt, 'phase' | 'id' | 'k'>): string {
  return `${r.phase} ${r.id}${r.k > 1 ? ` #${r.k}` : ''}`
}

/** A receipt's key on the page: its phase, id and k. */
export function receiptKey(r: Pick<CandidateReceipt, 'phase' | 'id' | 'k'>): string {
  return `${r.phase}:${r.id}:${r.k}`
}

/** How long a receipt ran, in milliseconds; undefined while it runs. */
export function receiptDuration(r: Pick<CandidateReceipt, 'startedAt' | 'finishedAt'>): number | undefined {
  if (!r.finishedAt) return undefined
  const d = Date.parse(r.finishedAt) - Date.parse(r.startedAt)
  return Number.isFinite(d) && d >= 0 ? d : undefined
}

/** A duration in words: "850 ms", "4.2 s", "3 min 5 s". */
export function durationWords(ms: number | undefined): string {
  if (ms === undefined) return ''
  if (ms < 1000) return `${ms} ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`
  const m = Math.floor(ms / 60_000)
  const s = Math.round((ms % 60_000) / 1000)
  return s ? `${m} min ${s} s` : `${m} min`
}

/** The step of an attempt's receipts (`runSteps`) a candidate's receipt is, by its phase, id and k: its argv, directory and duration. */
export function stepOf(steps: readonly RunStep[], r: Pick<CandidateReceipt, 'phase' | 'id' | 'k'>): RunStep | undefined {
  return steps.find((s) => s.phase === r.phase && s.id === r.id && s.k === r.k)
}

/** Whether the log shown is the receipt's: the hash the step log route sent beside it against the one the receipt recorded. */
export function logMatches(shown: string, recorded: string | undefined): 'matches' | 'differs' | 'unknown' {
  if (!shown || !recorded) return 'unknown'
  return shown.toLowerCase() === recorded.toLowerCase() ? 'matches' : 'differs'
}

/** A review's findings counted, in words, the most severe first: "1 blocker, 2 major". */
export function findingsWords(counts: RunCandidate['findings']): string {
  const order: Finding['severity'][] = ['blocker', 'major', 'minor', 'nit']
  return order
    .filter((s) => (counts?.[s] ?? 0) > 0)
    .map((s) => `${counts![s]} ${s}`)
    .join(', ')
}

/** How many findings a review counted. */
export function findingsCount(counts: RunCandidate['findings']): number {
  return Object.values(counts ?? {}).reduce((a, b) => a + (b ?? 0), 0)
}

/** A candidate in words: a work attempt's outcome against its checks, or a review's verdict. */
export function candidateWords(c: RunCandidate): { text: string; tone: 'success' | 'error' | 'warning' | 'muted' | 'default' } {
  if (c.kind === 'review') {
    if (c.verdict === 'approved') return { text: 'approved', tone: 'success' }
    if (c.verdict === 'needs_changes') {
      const n = findingsCount(c.findings)
      return { text: `asked for changes${n ? `: ${findingsWords(c.findings)}` : ''}`, tone: 'warning' }
    }
    return { text: c.state, tone: 'muted' }
  }
  const failed = c.receipts.filter((r) => receiptFailed(r))
  if (c.acceptedAnyway) return { text: 'accepted anyway', tone: 'warning' }
  if (failed.length) return { text: `failed ${failed.map((r) => receiptName(r)).join(', ')}`, tone: 'error' }
  if (c.receipts.some((r) => r.state === 'started' || r.state === 'running')) return { text: 'being checked', tone: 'default' }
  if (c.state === 'succeeded') return { text: c.receipts.length ? 'passed its checks' : 'succeeded, no check', tone: 'success' }
  if (c.state === 'failed') return { text: `failed${c.stage ? ` at ${c.stage}` : ''}`, tone: 'error' }
  return { text: c.state, tone: 'muted' }
}

/** A repair: what asked for it (a failed check, or a review's needs_changes), on which commit, and the attempts that took it up. */
export interface Repair {
  key: string
  kind: 'check' | 'review'
  /** The task and attempt that failed the check, or the review and its attempt. */
  task: string
  attempt: number
  /** The commit the check failed, or the review reviewed: where the repair starts. */
  commit: string
  /** The check's name (receiptName), or the review's findings in words. */
  what: string
  /** The attempts that started at the commit: the same task's after a failed check, the reviewed task's after a review. */
  by: Array<{ task: string; attempt: number; state: string; commit?: string }>
}

/**
 * A run's repairs from its candidates (and, for an attempt that has not submitted yet, the live run's tasks): each failed check of a
 * work attempt with the same task's later attempts that started at its result, and each review that asked for changes with the attempts
 * of other tasks that started at the commit it reviewed. Oldest first.
 */
export function repairs(tasks: readonly TaskCandidates[], live?: readonly RunTask[]): Repair[] {
  const out: Repair[] = []
  const all = tasks.flatMap((t) => t.candidates.map((c) => ({ ...c, task: t.task, kind: t.kind })))
  const startedAt = (task: string, commit: string, after: number) => {
    const by = all.filter((c) => c.kind === 'work' && c.task === task && c.attempt > after && c.start === commit).map((c) => ({ task: c.task, attempt: c.attempt, state: c.state, commit: c.commit }))
    const a = live?.find((t) => t.id === task)?.attempt
    if (a && a.n > after && a.start === commit && !by.some((b) => b.attempt === a.n)) by.push({ task, attempt: a.n, state: a.state, commit: a.result })
    return by.sort((x, y) => x.attempt - y.attempt)
  }
  for (const c of [...all].sort((a, b) => a.attempt - b.attempt)) {
    if (c.kind === 'work' && c.commit) {
      const failed = c.receipts.find((r) => receiptFailed(r))
      if (!failed) continue
      out.push({ key: `check:${c.task}:${c.attempt}`, kind: 'check', task: c.task, attempt: c.attempt, commit: c.commit, what: receiptName(failed), by: startedAt(c.task, c.commit, c.attempt) })
    } else if (c.kind === 'review' && c.verdict === 'needs_changes' && c.start) {
      const start = c.start
      const reviewed = [...new Set(all.filter((x) => x.kind === 'work' && x.commit === start).map((x) => x.task))]
      const by = reviewed.flatMap((task) => {
        const after = Math.max(...all.filter((x) => x.task === task && x.commit === start).map((x) => x.attempt))
        return startedAt(task, start, after)
      })
      out.push({ key: `review:${c.task}:${c.attempt}`, kind: 'review', task: c.task, attempt: c.attempt, commit: start, what: findingsWords(c.findings) || 'changes', by })
    }
  }
  return out
}

const FIELD_WORDS: Record<LaunchChanges['fields'][number], string> = { title: 'the title', description: 'the description', acceptance: 'the acceptance criteria' }

/** A list in words: "a", "a and b", "a, b and c". */
function listWords(parts: readonly string[]): string {
  if (parts.length < 2) return parts[0] ?? ''
  return `${parts.slice(0, -1).join(', ')} and ${parts.at(-1)}`
}

/**
 * What changed of the ticket since a launch, in words, '' for nothing: "The ticket changed after this run launched: the description
 * and the acceptance criteria; 2 comments since".
 */
export function changedSinceWords(c: LaunchChanges | undefined): string {
  if (!c) return ''
  const fields = listWords(c.fields.map((f) => FIELD_WORDS[f] ?? f))
  const comments = c.comments ? `${c.comments} ${c.comments === 1 ? 'comment' : 'comments'} since` : ''
  if (!fields && !comments) return ''
  return `The ticket changed after this run launched: ${[fields, comments].filter(Boolean).join('; ')}`
}

/**
 * The ticket's own history rows after a launch's create row (its fields' and its comments' rows), for the history between the launch
 * and now; null when the history read so far does not reach the launch's row (the page has read only its first pages).
 */
export function changesSince(changes: readonly DispatchChange[], launchId: string): DispatchChange[] | null {
  const created = changes.find((c) => c.entity === 'launch' && c.entityId === launchId && c.op === 'create')
  if (!created) return null
  return changes.filter((c) => c.seq > created.seq && (c.entity === 'ticket' || c.entity === 'comment'))
}

/** A launch entry's words: its state, and why a failed one failed. */
export function launchEntryWords(e: Pick<TicketRunEntry, 'launch' | 'run'>): string {
  const l = e.launch
  if (!l) return ''
  if (l.state === 'failed') return l.why ? `The launch failed: ${l.why}` : 'The launch failed'
  if (l.state === 'pending') return 'Recorded; its run is being started, or the server stopped before it could tell'
  return e.run ? '' : 'Its run is known to no engine, record or ledger the server can read now'
}
