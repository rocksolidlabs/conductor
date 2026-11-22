import type { RunSummary } from '~/composables/useDispatch'
import type { RunInfo, RunMember, RunTask, SessionInfo, SessionStatus } from '~/composables/useSessions'
import type { JoinedEntry } from '~/utils/joined'
import type { NeedsItem } from '~/utils/needsYou'
import type { Attention } from '~/utils/protocol'
import { isEnded, liveAttention } from './attention'
import { memberSession, memberStatus, ownError, type MemberStatus } from './crews'
import { runState } from './runs'
import { taskAnswer } from './tasks'

/**
 * One status language (the UX review's P1; docs/design/system.md, Status): a session is in one of five states, each with one mark
 * and one word, and every surface that shows a session's state asks sessionStatus and draws StatusMark. No component reads
 * `attention.state` or `status` to pick a colour or a word.
 */
export type StatusKey = 'needs' | 'working' | 'finished' | 'running' | 'exited'

/** The five, in the order the legend and the Yard's bar give them. */
export const STATUS_KEYS: readonly StatusKey[] = ['needs', 'working', 'finished', 'running', 'exited']

/**
 * A mark's shape: a filled dot, a filled dot inside a ring, a ring, a dashed ring (the five); a hollow square, a check, a cross, a
 * dash (a task's own states, TASK_LOOK).
 */
export type MarkShape = 'dot' | 'ringed-dot' | 'ring' | 'dashed-ring' | 'square' | 'check' | 'cross' | 'dash'
/** A mark's colour: amber for someone must act, green for a live agent, red for a task that failed, grey for the rest. */
export type StatusTone = 'warning' | 'success' | 'error' | 'neutral'

export interface StatusLook {
  shape: MarkShape
  tone: StatusTone
  /** The word a surface draws beside the mark (finished takes its age, sessionStatus). */
  word: string
  /** The legend's line: the word and what it means. */
  legend: string
}

/**
 * The marks: the one table both forms of StatusMark read (the HTML mark with its word, and the line diagram's station), so a
 * mark's shape and tone come from here alone. Green is for a live agent: working is the green dot in its ring, running (no signal)
 * the plain green dot, and the ring alone tells them apart; amber is for needs alone.
 */
export const STATUS_LOOK: Readonly<Record<StatusKey, StatusLook>> = Object.freeze({
  needs: { shape: 'dot', tone: 'warning', word: 'needs you', legend: "needs you: it asked, or something of it waits for a person's decision; nothing moves until someone answers" },
  working: { shape: 'ringed-dot', tone: 'success', word: 'working', legend: 'working: the agent is in a turn' },
  finished: { shape: 'ring', tone: 'neutral', word: 'finished', legend: "finished: the agent ended its turn and waits for the next word; it is a person's move, when they want it" },
  running: { shape: 'dot', tone: 'success', word: 'running', legend: 'running: the agent reports no turns; Conductor cannot tell working from waiting' },
  exited: { shape: 'dashed-ring', tone: 'neutral', word: 'exited', legend: 'exited: the process ended' },
})

/** A state as a surface draws it: the key (its mark), the word, and the legend line for the mark's title. */
export interface StatusView extends MarkView {
  key: StatusKey
}

/** Any mark as a surface draws it: one of the five, or a task's own (MarkKey). StatusMark draws this. */
export interface MarkView {
  key: MarkKey
  word: string
  /** When the state began, where it is known (a question's, a finished turn's). */
  since?: string
  legend: string
}

/** What sessionStatus reads of a session: the listing's, a join's or a guest page's own. */
export interface StatusInput {
  status: SessionStatus | string
  attention?: Attention
  turns?: boolean
  exitCode?: number
}

const HOST_AWAY_LEGEND = 'host away: the machine that runs it is not connected; it shows again when its host returns'
const STARTING_LEGEND = 'starting: the process is starting; its agent has reported nothing yet'

/** A view of key with its own word, else the table's. */
function view(key: StatusKey, word = STATUS_LOOK[key].word, since?: string, legend = STATUS_LOOK[key].legend): StatusView {
  return since ? { key, word, since, legend } : { key, word, legend }
}

/** "finished just now" under a minute, then "finished 12m ago", "finished 3h ago", "finished 2d ago"; "finished" with no time known. */
export function finishedWord(since: string | undefined, now: number): string {
  const at = since ? Date.parse(since) : NaN
  if (Number.isNaN(at)) return 'finished'
  const sec = Math.floor((now - at) / 1000)
  if (sec < 60) return 'finished just now'
  if (sec < 3600) return `finished ${Math.floor(sec / 60)}m ago`
  if (sec < 86400) return `finished ${Math.floor(sec / 3600)}h ago`
  return `finished ${Math.floor(sec / 86400)}d ago`
}

/**
 * A session's state, by these rules in order: ended (exited, stopped) is exited ("exit N" for status exited with a code other than
 * 0; a stopped session, ended by a person, says "exited" whatever its code); a question (liveAttention's needs_input) is needs,
 * "needs you · host away" while its host is away; a host away otherwise takes the exited mark, "host away"; working is working;
 * done is finished, with its age from the attention's since; the empty state with source input on a session whose agent reports
 * its turns is working (after an answer, or after the Enter that began the next turn); anything else is running, "starting" while
 * the process starts.
 */
export function sessionStatus(s: StatusInput, now = Date.now()): StatusView {
  if (isEnded(s.status)) return view('exited', s.status === 'exited' && s.exitCode !== undefined && s.exitCode !== 0 ? `exit ${s.exitCode}` : 'exited')
  const a = liveAttention(s.attention)
  if (a?.state === 'needs_input') return view('needs', s.status === 'host_disconnected' ? 'needs you · host away' : 'needs you', a.since)
  if (s.status === 'host_disconnected') return view('exited', 'host away', undefined, HOST_AWAY_LEGEND)
  if (a?.state === 'working') return view('working', undefined, a.since)
  if (a?.state === 'done') return view('finished', finishedWord(a.since, now), a.since)
  if (a?.state === '' && a.source === 'input' && s.turns) return view('working')
  if (s.status === 'starting') return view('running', 'starting', undefined, STARTING_LEGEND)
  return view('running')
}

/** The order a block of sessions is drawn by (blockStatus): needs, working, running, finished, exited. */
const BUSIEST: readonly StatusKey[] = ['needs', 'working', 'running', 'finished', 'exited']

/** The busiest of a run's members, for the rail's capsule: needs, then working, running, finished, exited (exited for none). */
export function blockStatus(keys: Iterable<StatusKey>): StatusKey {
  let best = BUSIEST.length - 1
  for (const k of keys) best = Math.min(best, BUSIEST.indexOf(k))
  return BUSIEST[best]!
}

/** A key's view with the table's word: a block's, a run's. */
export function statusOfKey(key: StatusKey): StatusView {
  return view(key)
}

const TALLY_WORD: Record<StatusKey, string> = { needs: 'need you', working: 'working', finished: 'finished', running: 'running', exited: 'exited' }

/** "1 working · 2 finished": how many of the keys are in each state, in the table's order, those in `only` alone, none at 0. */
export function statusTally(keys: Iterable<StatusKey>, only: readonly StatusKey[] = STATUS_KEYS): string {
  const n: Record<StatusKey, number> = { needs: 0, working: 0, finished: 0, running: 0, exited: 0 }
  for (const k of keys) n[k]++
  return STATUS_KEYS.filter((k) => only.includes(k) && n[k] > 0)
    .map((k) => `${n[k]} ${TALLY_WORD[k]}`)
    .join(' · ')
}

/**
 * A joined session's state ("Shared with you"): what the entry knows, so running, "starting" or exited (a host away takes the
 * exited mark). A link that does not answer, one revoked or gone, and an entry that has not said yet draw no mark (undefined):
 * their line says so. Amber stays for needs, which a joined entry does not know.
 */
export function joinedStatus(e: Pick<JoinedEntry, 'lastStatus' | 'sessionStatus' | 'kind'>): StatusView | undefined {
  if (e.lastStatus === 'revoked' || e.lastStatus === 'gone' || e.lastStatus === 'unreachable') return undefined
  if (e.kind === 'run') return view('running')
  if (!e.sessionStatus) return undefined
  return sessionStatus({ status: e.sessionStatus })
}

/*
 * Runs and tasks (the UX review's P1, PR-10): a run's tasks, its members, its graph and its cards speak the same language. A task's
 * attempt that works is the working mark; a task that waits on a person is needs; a task's own states (waiting, ready, done, failed,
 * cancelled) have marks of their own, none of the five (TASK_LOOK). Every surface asks taskStatus, memberStatusOf, runStatusLine or
 * summaryStatusLine; none picks a colour or a word from a task's or a member's state itself.
 */

/** A task's own states, none of the five: waiting (blocked, and ready with its own word), done, failed, cancelled. */
export type TaskMarkKey = 'waiting' | 'done' | 'failed' | 'cancelled'
/** Every mark StatusMark draws: the five, then a task's own. */
export type MarkKey = StatusKey | TaskMarkKey

/** A task's own marks, in the legend's order. */
export const TASK_KEYS: readonly TaskMarkKey[] = ['waiting', 'done', 'failed', 'cancelled']

/**
 * The marks of a task's own states: waiting a hollow square, done a check in neutral ink (the forest fill is for the main action
 * alone), failed a red cross, cancelled a grey dash. None is one of the five's (the guard in status.test.ts): a blocked task's grey
 * ring would read as finished.
 */
export const TASK_LOOK: Readonly<Record<TaskMarkKey, StatusLook>> = Object.freeze({
  waiting: { shape: 'square', tone: 'neutral', word: 'waiting', legend: 'waiting: it waits for the tasks it needs to give their results' },
  done: { shape: 'check', tone: 'neutral', word: 'done', legend: 'done: it succeeded, and its result stands' },
  failed: { shape: 'cross', tone: 'error', word: 'failed', legend: 'failed: it ended without a result, and nothing is left to do on it' },
  cancelled: { shape: 'dash', tone: 'neutral', word: 'cancelled', legend: 'cancelled: a person, or a decision, called it off' },
})

/** The look of any mark: the five's (STATUS_LOOK) and a task's own (TASK_LOOK). */
export function markLook(key: MarkKey): StatusLook {
  return key in STATUS_LOOK ? STATUS_LOOK[key as StatusKey] : TASK_LOOK[key as TaskMarkKey]
}

const READY_LEGEND = 'ready: it can start; it starts when a member is free'
const CHECKING_LEGEND = "checking: the attempt's result is being checked"
const HELD_LEGEND = "needs you: it waits for a person's decision; nothing moves until someone answers"
const OUT_LEGEND = 'needs you: it failed, and a person decides: retry it, or accept its last result anyway'
const WAITING_TO_START_LEGEND = 'waiting to start: the member has not started yet'
const MEMBER_FAILED_LEGEND = 'failed: the member ended with an error of its own'

function mark(key: MarkKey, word = markLook(key).word, legend = markLook(key).legend, since?: string): MarkView {
  return since ? { key, word, since, legend } : { key, word, legend }
}

/** What taskStatus reads of a task: its state, and where it has them, its holds, the engine's commands and its attempt. */
export type TaskInput = Pick<RunTask, 'state'> & Partial<Pick<RunTask, 'holds' | 'commands'>>

/**
 * A task's state, by these rules in order: a decision still open that holds it (taskAnswer) is needs, and so is a failed task on which
 * the engine names a command (`commands`: the inbox's own rule, internal/api/inbox.go taskItem; a record carries none); a hold answered
 * and not yet applied is Yardmaster's to apply, not a person's. A running task is its attempt's session's state (working, finished,
 * needs or running), running while no live session is known; verifying is the working mark with "checking"; blocked is waiting and
 * ready is the waiting mark with "ready"; succeeded is done, failed with no command failed, cancelled cancelled.
 */
export function taskStatus(t: TaskInput, run: Pick<RunInfo, 'record'> | undefined, attemptSession: StatusInput | undefined, now = Date.now()): MarkView {
  const terminal = t.state === 'succeeded' || t.state === 'failed' || t.state === 'cancelled'
  if (!terminal && taskAnswer(t)) return mark('needs', 'needs you', HELD_LEGEND)
  if (t.state === 'failed' && !run?.record && (t.commands?.length ?? 0) > 0) return mark('needs', 'needs you', OUT_LEGEND)
  switch (t.state) {
    case 'running': {
      if (!attemptSession) return mark('running')
      const s = sessionStatus(attemptSession, now)
      // An attempt's session that ended while the engine still says running: the engine's word stands until it moves.
      return s.key === 'exited' ? mark('running') : s
    }
    case 'verifying':
      return mark('working', 'checking', CHECKING_LEGEND)
    case 'blocked':
      return mark('waiting')
    case 'ready':
      return mark('waiting', 'ready', READY_LEGEND)
    case 'succeeded':
      return mark('done')
    case 'failed':
      return mark('failed')
    case 'cancelled':
      return mark('cancelled')
  }
  return mark('waiting')
}

/** The live session of a task's attempt, from the sessions the page holds: none before the attempt has one, or once it left the server. */
export function attemptSession(t: Pick<RunTask, 'attempt'>, sessions: readonly SessionInfo[]): SessionInfo | undefined {
  const id = t.attempt?.sessionId
  return id ? sessions.find((s) => s.id === id) : undefined
}

/** A member's state as the run's surfaces draw it: its mark, and its `memberStatus` (what `data-status` says), unchanged. */
export interface MemberMark {
  status: MemberStatus
  mark: MarkView
}

/**
 * A member's state: a member with a live session takes its session's state (sessionStatus); starting is the running mark with
 * "starting"; pending is the waiting mark with "waiting to start"; ended is exited, and ended with an error of its own is failed. Its
 * `status` is memberStatus's, which the tests and the member's controls read (data-status, memberOffer).
 */
export function memberStatusOf(run: RunInfo, member: RunMember, sessions: readonly SessionInfo[], now = Date.now()): MemberMark {
  const status = memberStatus(run, member, sessions)
  const s = memberSession(run, member, sessions)
  switch (status) {
    case 'ended':
      return { status, mark: ownError(member) ? mark('failed', 'failed', MEMBER_FAILED_LEGEND) : mark('exited') }
    case 'pending':
      return { status, mark: mark('waiting', 'waiting to start', WAITING_TO_START_LEGEND) }
    case 'starting':
      return { status, mark: mark('running', 'starting', STARTING_LEGEND) }
    case 'needs_input':
      return { status, mark: s ? sessionStatus(s, now) : mark('needs') }
  }
  return { status, mark: s ? sessionStatus(s, now) : mark('running') }
}

const PAUSED_LEGEND = 'paused: a person paused the run; nothing new starts until it is resumed on its run page'
const INTERRUPTED_LEGEND = 'interrupted: a restart ended its attempts and it is not yet continued; its run page continues it'

/**
 * An open managed run as the Hosts page lists it, from its row alone (GET /api/hosts: its state and paused flag as the engine or the
 * ledger gives them): interrupted and not yet continued takes the exited mark with those words, paused the waiting mark with
 * "paused", and any other open run the running mark.
 */
export function heldRunStatus(state: string, paused: boolean): MarkView {
  if (state === 'interrupted') return mark('exited', 'interrupted, not yet continued', INTERRUPTED_LEGEND)
  if (paused) return mark('waiting', 'paused', PAUSED_LEGEND)
  return mark('running')
}

/** The items of the one list (needsYou) that wait in the run `id`: what its "N need you" counts. */
export function runItems(items: readonly NeedsItem[], id: string): NeedsItem[] {
  return items.filter((it) => it.run === id)
}

/** One part of a run's sentence: what it counts, how many, its words, and its tone (amber for needs, red for failed, else neutral). */
export interface RunLinePart {
  key: 'done' | 'working' | 'checking' | 'finished' | 'running' | 'failed' | 'needs' | 'outcome'
  n: number
  text: string
  tone: 'warning' | 'error' | 'neutral'
}

/** A run's one sentence: its parts, joined by " · ", how many items wait on a person, and whether it has ended. */
export interface RunLine {
  text: string
  parts: RunLinePart[]
  /** How many things a person can act on in the run: its items of the one list, each once. */
  needs: number
  /** The ended run's outcome ("Succeeded", "Failed", "Stopped", "Interrupted", "Cancelled", "Ended"); '' while it goes. */
  outcome: string
  /** "2 of 6 done" for a run with tasks, whether it goes or ended; '' for a run of members. */
  done: string
  /** A run with tasks: its tasks counted by the bar's parts (runBarParts); empty for a run of members, and from a summary alone. */
  tasks: Partial<Record<BarPart, number>>
}

/** A segment of a run's bar: what it counts of the run's tasks. */
export type BarPart = 'done' | 'working' | 'checking' | 'finished' | 'running' | 'needs' | 'failed' | 'waiting' | 'cancelled'

const needWords = (n: number) => `${n} ${n === 1 ? 'needs' : 'need'} you`
const part = (key: RunLinePart['key'], n: number, text: string, tone: RunLinePart['tone'] = 'neutral'): RunLinePart => ({ key, n, text, tone })

function line(parts: RunLinePart[], needs: number, outcome: string, done = '', tasks: Partial<Record<BarPart, number>> = {}): RunLine {
  if (needs > 0) parts.push(part('needs', needs, needWords(needs), 'warning'))
  return { text: parts.map((p) => p.text).join(' · '), parts, needs, outcome, done, tasks }
}

function outcomePart(outcome: string): RunLinePart {
  return part('outcome', 0, outcome, outcome === 'Failed' ? 'error' : 'neutral')
}

const MANAGED_OUTCOME: Record<NonNullable<RunInfo['outcome']>, string> = { succeeded: 'Succeeded', failed: 'Failed', cancelled: 'Cancelled' }

/** The counted parts in their order: working, checking, finished, running, failed (each at 0 none). */
function counted(keys: ReadonlyMap<RunLinePart['key'], number>): RunLinePart[] {
  const order: Array<[RunLinePart['key'], string, RunLinePart['tone']]> = [
    ['working', 'working', 'neutral'],
    ['checking', 'checking', 'neutral'],
    ['finished', 'finished', 'neutral'],
    ['running', 'running', 'neutral'],
    ['failed', 'failed', 'error'],
  ]
  return order.filter(([k]) => (keys.get(k) ?? 0) > 0).map(([k, w, tone]) => part(k, keys.get(k)!, `${keys.get(k)} ${w}`, tone))
}

/**
 * A run's sentence: for a managed run "2 of 6 done · 1 working · 1 checking · 2 need you" (its tasks by taskStatus: checking is a
 * verifying task, a task that needs you is in none of the counts), for a run of members "1 working · 2 finished · 1 needs you" (its
 * members by memberStatusOf), and for an ended run its outcome: "Succeeded", "Failed", "Cancelled" (a managed run's), "Stopped",
 * "Interrupted", and "Ended" for a run of members that all ended ("Failed" when one ended with an error of its own). "N need you" is the
 * run's items of the one list (`needs`, runItems): each thing a person can act on once, so a decision that holds two tasks is one, two
 * decisions that hold one task are two, and a plan gate's decision that holds no task is counted; an ended run still says them.
 */
export function runStatusLine(run: RunInfo, sessions: readonly SessionInfo[], needs: readonly NeedsItem[], now = Date.now()): RunLine {
  const n = needs.length
  const st = runState(run, sessions).state
  const ended = st === 'stopped' ? 'Stopped' : st === 'interrupted' ? 'Interrupted' : st === 'finished' ? (run.managed ? MANAGED_OUTCOME[run.outcome ?? 'succeeded'] : run.members.some((m) => ownError(m)) ? 'Failed' : 'Ended') : ''
  const keys = new Map<RunLinePart['key'], number>()
  const add = (k: RunLinePart['key']) => keys.set(k, (keys.get(k) ?? 0) + 1)
  if (run.managed) {
    const tasks = run.tasks ?? []
    const done = tasks.filter((t) => t.state === 'succeeded').length
    const doneWords = `${done} of ${tasks.length} done`
    const bar: Partial<Record<BarPart, number>> = {}
    for (const t of tasks) {
      const m = taskStatus(t, run, attemptSession(t, sessions), now)
      const k: BarPart = m.key === 'needs' ? 'needs' : t.state === 'verifying' ? 'checking' : m.key === 'exited' ? 'running' : m.key
      bar[k] = (bar[k] ?? 0) + 1
      if (k === 'checking' || k === 'working' || k === 'finished' || k === 'running' || k === 'failed') add(k)
    }
    if (ended) return line([outcomePart(ended)], n, ended, doneWords, bar)
    return line([part('done', done, doneWords), ...counted(keys)], n, '', doneWords, bar)
  }
  if (ended) return line([outcomePart(ended)], n, ended)
  for (const m of run.members) {
    const k = memberStatusOf(run, m, sessions, now).mark.key
    if (k === 'working' || k === 'finished' || k === 'running') add(k)
  }
  const parts = counted(keys)
  // A live run of members none of which has started yet, nor asks: it says so rather than nothing.
  if (!parts.length && !n) parts.push(part('running', 0, 'waiting to start'))
  return line(parts, n, '')
}

/**
 * A run's sentence from a ticket's summary of it alone (owner decision 40): what a summary can carry and nothing more. It counts tasks
 * by state and names its members' sessions, not which task's attempt runs in which session nor what holds a task, so it says "2 of 6
 * done" from the counts and "N need you" from the run's items of the one list, and no working, finished or checking part; an ended
 * run says its outcome. A run of members alone, live, says "running".
 */
export function summaryStatusLine(s: Pick<RunSummary, 'state' | 'stoppedAt' | 'outcome' | 'tasks'>, needs: readonly NeedsItem[]): RunLine {
  const n = needs.length
  const ended = s.stoppedAt ? 'Stopped' : s.state === 'interrupted' ? 'Interrupted' : s.outcome ? MANAGED_OUTCOME[s.outcome] : s.state === 'finished' ? 'Ended' : s.state === 'stopped' ? 'Stopped' : ''
  const counts = Object.values(s.tasks ?? {}).reduce((a, b) => a + (b ?? 0), 0)
  const done = counts > 0 ? `${s.tasks?.succeeded ?? 0} of ${counts} done` : ''
  if (ended) return line([outcomePart(ended)], n, ended, done)
  if (counts > 0) return line([part('done', s.tasks?.succeeded ?? 0, done)], n, '', done)
  return line(n ? [] : [part('running', 0, 'running')], n, '')
}

/** A segment of the run's bar: its part, how many tasks, the mark whose tone it is drawn in, and its words. */
export interface BarSegment {
  part: BarPart
  count: number
  /** The mark whose tone the segment takes: checking takes working's, done, failed, waiting and cancelled a task's own. */
  mark: MarkKey
  /** Its title and label: "2 done", "1 checking", "2 tasks held" (the tasks that wait on a person, never "need you": items count that). */
  words: string
}

const BAR_ORDER: readonly BarPart[] = ['done', 'working', 'checking', 'finished', 'running', 'needs', 'failed', 'waiting', 'cancelled']
const BAR_MARK: Record<BarPart, MarkKey> = { done: 'done', working: 'working', checking: 'working', finished: 'finished', running: 'running', needs: 'needs', failed: 'failed', waiting: 'waiting', cancelled: 'cancelled' }

/**
 * The run's bar (the run header's): its tasks counted by part in one order (done, working, checking, finished, running, needs, failed,
 * waiting, cancelled), the parts at zero left out. It counts tasks, not items: its needs segment is the tasks whose taskStatus is
 * needs, "2 tasks held", while the sentence's "N need you" counts the things a person can act on.
 */
export function runBarParts(l: Pick<RunLine, 'tasks'>): BarSegment[] {
  return BAR_ORDER.filter((p) => (l.tasks[p] ?? 0) > 0).map((p) => {
    const count = l.tasks[p]!
    const words = p === 'needs' ? `${count} ${count === 1 ? 'task' : 'tasks'} held` : `${count} ${p}`
    return { part: p, count, mark: BAR_MARK[p], words }
  })
}

/** The sidebar's section a mark falls under (a palette row's state): needs, running (a live agent's three), idle (waiting), exited (the rest). */
export function markSection(key: MarkKey): 'needs' | 'running' | 'idle' | 'exited' {
  if (key === 'needs') return 'needs'
  if (key === 'working' || key === 'finished' || key === 'running') return 'running'
  return key === 'waiting' ? 'idle' : 'exited'
}
