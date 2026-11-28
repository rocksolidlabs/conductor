import type { RelatedTicket } from '~/composables/useDispatch'
import type { CrewInput, CrewMember, CrewStart, RunInfo, RunMember, RunTicket, SessionInfo } from '~/composables/useSessions'
import type { ActivityEntry, ChatMessage } from './protocol'
import { attentionWords, liveAttention, offeredChoices } from './attention'
import { runLength } from './charts'
import type { NeedsItem } from './needsYou'
import { memberSession, ownError, STOPPED_ERROR, toCrewInput, type MemberStatus } from './crews'
import { rowStatus } from './historyRows'
import { runLive, runState } from './runs'
import { agentName } from './sessions'
import { memberStatusOf, runStatusLine, type MarkKey, type MarkView } from './status'

/**
 * The words of the Crews pages. Two nouns, said every time: a crew is a saved
 * plan, a run is one launch of it. A run is named by its crew and its start
 * ("users api · run started 08:31"), never by its id alone; a saved crew never
 * says Running, only "1 live run"; a member a stop cut off reads "not started"
 * or "stopped" in grey, and red is kept for an error of its own.
 */

// A member's own error (ownError) and the stop's (STOPPED_ERROR) are crews.ts's, beside memberStatus, which the status language reads too.

/** A start rule in words, with its icon: the table's Starts column, a graph node's last line. A member started by hand is muted. */
export function startWords(start: CrewStart): { icon: string; text: string; muted: boolean } {
  if (start.when === 'task') return { icon: 'i-lucide-list-checks', text: 'By its tasks', muted: false }
  if (start.when === 'after') return { icon: 'i-lucide-corner-down-right', text: `After ${start.member} is done`, muted: false }
  if (start.when === 'manual') return { icon: 'i-lucide-hand', text: 'When you press Start', muted: true }
  return { icon: 'i-lucide-zap', text: 'At launch', muted: false }
}

type ShapeMember = Pick<CrewMember, 'name' | 'start'>

/** The members grouped by their start rules: every member joined to the one it starts after, in crew order. */
function groupsOf(members: readonly ShapeMember[]): ShapeMember[][] {
  const names = new Set(members.map((m) => m.name))
  const parent = new Map<string, string>(members.map((m) => [m.name, m.name]))
  const find = (x: string): string => {
    const p = parent.get(x) ?? x
    if (p === x) return x
    const r = find(p)
    parent.set(x, r)
    return r
  }
  for (const m of members) if (m.start.when === 'after' && m.start.member && names.has(m.start.member)) parent.set(find(m.name), find(m.start.member))
  const groups = new Map<string, ShapeMember[]>()
  for (const m of members) {
    const r = find(m.name)
    groups.set(r, [...(groups.get(r) ?? []), m])
  }
  return [...groups.values()]
}

/** Whether a group of members is one line: no member has two members starting after it. */
function linear(group: readonly ShapeMember[]): boolean {
  const children = new Map<string, number>()
  for (const m of group) if (m.start.when === 'after' && m.start.member) children.set(m.start.member, (children.get(m.start.member) ?? 0) + 1)
  return [...children.values()].every((n) => n <= 1)
}

/** The group's members in start order: the roots, then what starts after each. */
function ordered(group: readonly ShapeMember[]): string[][] {
  const names = new Set(group.map((m) => m.name))
  const levels: string[][] = []
  let level = group.filter((m) => !(m.start.when === 'after' && m.start.member && names.has(m.start.member))).map((m) => m.name)
  const seen = new Set<string>()
  while (level.length && levels.length <= group.length) {
    levels.push(level)
    for (const n of level) seen.add(n)
    level = group.filter((m) => m.start.when === 'after' && m.start.member && level.includes(m.start.member) && !seen.has(m.name)).map((m) => m.name)
  }
  return levels
}

/**
 * A crew's shape in a few words, under its mini graph: "5 · a chain of 3, 1 by hand, 1 alone", "4 · all at launch", "2 · checker after
 * writer", "3 · a chain". A tree (two members start after the same one) is said so. A crew with tasks, whose members start by them: "4 ·
 * by tasks".
 */
export function crewShape(members: readonly ShapeMember[]): string {
  const n = members.length
  if (!n) return 'no members'
  if (members.every((m) => m.start.when === 'task')) return `${n} · by tasks`
  if (n === 1) return `1 · ${members[0]!.start.when === 'manual' ? 'by hand' : 'at launch'}`
  const groups = groupsOf(members)
  const chains = groups.filter((g) => g.length > 1)
  const singles = groups.filter((g) => g.length === 1).map((g) => g[0]!)
  const byHand = singles.filter((m) => m.start.when === 'manual').length
  const alone = singles.length - byHand
  if (n === 2 && chains.length === 1) {
    const child = members.find((m) => m.start.when === 'after')!
    return `2 · ${child.name} after ${child.start.member}`
  }
  if (!chains.length && !byHand) return `${n} · all at launch`
  const word = (g: readonly ShapeMember[]) => (linear(g) ? 'chain' : 'tree')
  if (chains.length === 1 && chains[0]!.length === n) return `${n} · a ${word(chains[0]!)}`
  const parts = [...chains].sort((a, b) => b.length - a.length).map((g) => `a ${word(g)} of ${g.length}`)
  if (byHand) parts.push(`${byHand} by hand`)
  if (alone) parts.push(`${alone} alone`)
  return `${n} · ${parts.join(', ')}`
}

/**
 * A crew's members on the home (C4): how many, and on which agents, each agent once in the crew's order: "5 members · Claude Code,
 * Codex CLI, Shell agent", "1 member · Claude Code"; "No members yet". An agent the catalog does not name is said by its id.
 */
export function crewMembersWords(members: ReadonlyArray<Pick<CrewMember, 'agentId'>>, names: ReadonlyMap<string, string> = new Map()): string {
  if (!members.length) return 'No members yet'
  const agents = [...new Set(members.map((m) => names.get(m.agentId) || agentName(m.agentId)))]
  return `${members.length} ${members.length === 1 ? 'member' : 'members'} · ${agents.join(', ')}`
}

function list(names: readonly string[]): string {
  if (names.length <= 1) return names[0] ?? ''
  return `${names.slice(0, -1).join(', ')} and ${names.at(-1)}`
}

/**
 * The start order as one sentence beside the Members heading: "lead → core → tests start in a chain; docs waits for you; review starts at
 * once". A tree lists its levels: "lead → core, cli → tester start in order". Every member at launch: "all start at launch".
 */
export function startSentence(members: readonly ShapeMember[]): string {
  const n = members.length
  if (!n) return 'no members yet'
  if (members.every((m) => m.start.when === 'task')) return n === 1 ? `${members[0]!.name} does its tasks` : 'each does its tasks'
  const groups = groupsOf(members)
  const chains = groups.filter((g) => g.length > 1)
  const singles = groups.filter((g) => g.length === 1).map((g) => g[0]!)
  const byHand = singles.filter((m) => m.start.when === 'manual').map((m) => m.name)
  const atOnce = singles.filter((m) => m.start.when !== 'manual').map((m) => m.name)
  if (!chains.length && !byHand.length) return n === 1 ? `${atOnce[0]} starts at launch` : 'all start at launch'
  const parts: string[] = []
  for (const g of chains) {
    const levels = ordered(g).map((l) => l.join(', '))
    parts.push(`${levels.join(' → ')} start in ${linear(g) ? 'a chain' : 'order'}`)
  }
  if (byHand.length) parts.push(`${list(byHand)} ${byHand.length === 1 ? 'waits' : 'wait'} for you`)
  if (atOnce.length) parts.push(`${list(atOnce)} ${atOnce.length === 1 ? 'starts' : 'start'} at once`)
  return parts.join('; ')
}

/*
 * One vocabulary for the Crews pages (the review of issue 1024, C2): a run is Running, Needs you, Succeeded, Failed or Stopped; a
 * member is Working, Idle, Needs you, Waiting to start, Finished, Failed or Stopped. Each word has one mark of the status language
 * (StatusMark draws it: its shape and its colour), so no state is said by colour alone, and every list, card, header and tile of
 * these pages asks runPhase and memberPhase rather than reading a run's or a member's fields itself.
 */

/** A run's state as the Crews pages say it. */
export type RunPhase = 'running' | 'needs' | 'succeeded' | 'failed' | 'stopped'
export const RUN_PHASES: readonly RunPhase[] = ['running', 'needs', 'succeeded', 'failed', 'stopped']

interface Look {
  word: string
  /** The mark StatusMark draws for it: its shape and its tone. */
  mark: MarkKey
  legend: string
}

/** Each run state's word and mark: green dot, amber dot, check, red cross, grey dash. */
export const RUN_LOOK: Readonly<Record<RunPhase, Look>> = Object.freeze({
  running: { word: 'Running', mark: 'running', legend: 'Running: its members are at work, or wait for their turn' },
  needs: { word: 'Needs you', mark: 'needs', legend: "Needs you: a member asked, or a decision waits for a person's answer" },
  succeeded: { word: 'Succeeded', mark: 'done', legend: 'Succeeded: every task gave its result, or every member ended without an error' },
  failed: { word: 'Failed', mark: 'failed', legend: 'Failed: a task failed, or a member ended with an error of its own' },
  stopped: { word: 'Stopped', mark: 'cancelled', legend: 'Stopped: a person stopped it, or the server stopped while it ran' },
})

/**
 * A run's state, by these rules in order: a run that still goes needs you while a member waits on a prompt or the run has items of
 * the one list (`needs`, its decisions and the tasks a person decides on), else it is running; an ended run with tasks says its own
 * outcome (succeeded, failed; cancelled is stopped); a run whose member ended with an error of its own failed, stopped or not; a run
 * stopped by a person, or by the server's end (interrupted), stopped; a run every member of which ended without an error succeeded.
 */
export function runPhase(run: Pick<RunInfo, 'members' | 'managed' | 'outcome'> & Parameters<typeof runState>[0], sessions: readonly SessionInfo[], needs = 0): RunPhase {
  const st = runState(run, sessions).state
  if (st === 'needs_input') return 'needs'
  if (st === 'running') return needs > 0 ? 'needs' : 'running'
  if (run.outcome) return run.outcome === 'succeeded' ? 'succeeded' : run.outcome === 'failed' ? 'failed' : 'stopped'
  // The server's end stopped it: every member it cut off carries the interruption as its error, which is no error of its own.
  if (st === 'interrupted') return 'stopped'
  if (run.members.some((m) => ownError(m) || exitFailure(run, m, sessions) !== null)) return 'failed'
  return st === 'finished' ? 'succeeded' : 'stopped'
}

/** The words the server gives an interrupted run's members ("interrupted: the server stopped while it ran"), after its prefix. */
const INTERRUPTED = /^interrupted: /

/** Why a run that ended was stopped by the server's end, in the server's own words; '' for any other run. */
export function interruptedWords(run: Pick<RunInfo, 'state' | 'members'>): string {
  if (run.state !== 'interrupted') return ''
  const said = run.members.map((m) => m.error ?? '').find((e) => INTERRUPTED.test(e))
  return said ? said.replace(INTERRUPTED, '') : 'the server stopped while it ran'
}

/**
 * The exit code of a member whose agent failed, by the one rule History's rows say an ended session by (rowStatus, through the
 * status language's sessionStatus: "exit N" for a session that exited with a code other than 0, "exited" for one a person stopped);
 * null for a member that did not fail so. A member the run's own Stop ended, or whose session a person stopped, did not fail, whatever
 * its code; one whose session is no longer listed is not known to have failed.
 */
export function exitFailure(run: RunInfo, m: RunMember, sessions: readonly SessionInfo[]): number | null {
  if (run.stoppedAt) return null
  const s = memberSession(run, m, sessions)
  if (!s || s.status !== 'exited') return null
  // A listed session: the row says its own state (the record's fields are read only without one).
  const said = rowStatus({ kind: 'session', availability: { state: '', words: '' } }, s)
  const code = said ? /^exit (-?\d+)$/.exec(said.word) : null
  return code ? Number(code[1]) : null
}

/**
 * What a run's state word does not say and a person must know, beside it with its icon (never colour alone): a live run whose
 * scheduling is paused ("Paused: <the server's reason>"; "Paused" for a person's pause), or a run the server's end stopped (its
 * words). Null for anything else.
 */
export function runAside(run: Pick<RunInfo, 'paused' | 'pausedWhy' | 'state' | 'members' | 'stoppedAt' | 'outcome'>): { icon: string; text: string } | null {
  if (run.paused && !run.stoppedAt && !run.outcome) return { icon: 'i-lucide-pause', text: run.pausedWhy ? `Paused: ${run.pausedWhy}` : 'Paused' }
  const why = interruptedWords(run)
  return why ? { icon: 'i-lucide-power', text: why } : null
}

/** A run state as StatusMark draws it: its mark, its word, its legend (the interrupted run's own reason in the title). */
export function runPhaseView(phase: RunPhase, interrupted = false): MarkView {
  const l = RUN_LOOK[phase]
  return { key: l.mark, word: l.word, legend: interrupted && phase === 'stopped' ? 'Stopped: the server stopped while it ran' : l.legend }
}

/** A run's state and its words, for a list's row, a card, a header: the state's view, and the run's counts after it. */
export interface RunLook {
  phase: RunPhase
  view: MarkView
  /** What goes after the word: "2 of 6 done · 1 working", "1 working · 1 idle · 1 needs you"; '' for nothing more. */
  detail: string
  /** How many things a person can act on in the run: the items of the one list, else the members that ask. */
  needs: number
}

/**
 * A run in this vocabulary, with its counts: runStatusLine's parts (the members or the tasks by state, "N need you" from the run's
 * items of the one list) said in the member words (a session's "running", which reports no turn, is idle), the outcome left to the
 * state's word. An ended run with tasks keeps "2 of 3 done", and what still needs a person in it.
 */
export function runLook(run: RunInfo, sessions: readonly SessionInfo[], items: readonly NeedsItem[], now = Date.now()): RunLook {
  const line = runStatusLine(run, sessions, items, now)
  const prompts = runState(run, sessions).needs
  const needs = Math.max(line.needs, prompts)
  // A task held for a person (taskStatus's needs: an open decision, a failed task with Retry) makes the run need you, whether or not
  // the one list has read its item yet.
  const phase = runPhase(run, sessions, Math.max(needs, line.tasks.needs ?? 0))
  const st = runState(run, sessions).state
  const view = runPhaseView(phase, st === 'interrupted')
  // An ended run keeps what a person can still act on in it (a failed task's Retry, Accept anyway).
  if (line.outcome) return { phase, view, detail: [line.done, line.needs ? needWords(line.needs) : '', interruptedWords(run)].filter(Boolean).join(' · '), needs }
  const parts = line.parts.map((p) => (p.key === 'running' && p.n > 0 ? `${p.n} idle` : p.key === 'needs' ? needWords(p.n) : p.text))
  if (!line.parts.some((p) => p.key === 'needs') && prompts > 0) parts.push(needWords(prompts))
  return { phase, view, detail: parts.join(' · '), needs }
}

const needWords = (n: number) => `${n} ${n === 1 ? 'needs' : 'need'} you`

/** A member's state as the Crews pages say it. */
export type MemberPhase = 'working' | 'idle' | 'needs' | 'waiting' | 'finished' | 'failed' | 'stopped'
export const MEMBER_PHASES: readonly MemberPhase[] = ['working', 'idle', 'needs', 'waiting', 'finished', 'failed', 'stopped']

/** Each member state's word and mark: the status language's own for the five a session can be in, a task's own for the rest. */
export const MEMBER_LOOK: Readonly<Record<MemberPhase, Look>> = Object.freeze({
  working: { word: 'Working', mark: 'working', legend: 'Working: the agent is in a turn' },
  idle: { word: 'Idle', mark: 'running', legend: 'Idle: the agent is at its prompt, or does not say when it works' },
  needs: { word: 'Needs you', mark: 'needs', legend: 'Needs you: it asked, and waits for an answer' },
  waiting: { word: 'Waiting to start', mark: 'waiting', legend: 'Waiting to start: the member has not started yet' },
  finished: { word: 'Finished', mark: 'finished', legend: 'Finished: it ended its turn, or its session ended without an error' },
  failed: { word: 'Failed', mark: 'failed', legend: 'Failed: the member ended with an error of its own' },
  stopped: { word: 'Stopped', mark: 'cancelled', legend: "Stopped: the run's stop ended it, or it never started" },
})

/** A member as the Crews pages draw it: its state, its view for StatusMark, its memberStatus (what `data-status` says), and its start. */
export interface MemberLook {
  m: RunMember
  phase: MemberPhase
  view: MarkView
  status: MemberStatus
  /** Who it waits for, beside its name: "after driver", "by hand"; '' for a member that starts at launch or by its tasks. */
  after: string
}

/**
 * A member's state, from memberStatusOf (its live session's state, else the run's word for it): an ended member failed with an error
 * of its own, stopped when the run's stop ended it, else finished; a member not started waits to start, or stopped in a run that was
 * stopped or ended;
 * a starting one waits to start; a live one is working, idle (a session that reports no turn, or has none yet), needs you, or
 * finished (its turn ended). A host away keeps its own word on the idle mark.
 */
export function memberLook(run: RunInfo, m: RunMember, sessions: readonly SessionInfo[], now = Date.now()): MemberLook {
  const { status, mark } = memberStatusOf(run, m, sessions, now)
  const after = m.start.when === 'after' ? `after ${m.start.member}` : m.start.when === 'manual' ? 'by hand' : ''
  const one = (phase: MemberPhase, why?: string): MemberLook => {
    const l = MEMBER_LOOK[phase]
    return { m, phase, status, after, view: { key: l.mark, word: l.word, legend: why ?? l.legend, ...(mark.since ? { since: mark.since } : {}) } }
  }
  if (status === 'ended') {
    // The server's end cut it off: stopped, in the server's words.
    if (run.state === 'interrupted' && INTERRUPTED.test(m.error ?? '')) return one('stopped', `Stopped: ${(m.error ?? '').replace(INTERRUPTED, '')}`)
    if (ownError(m)) return one('failed', `Failed: ${ownError(m)}`)
    if (run.stoppedAt || m.error) return one('stopped')
    // A person stopped its session: stopped, whatever its code (the one rule History's rows say it by).
    if (memberSession(run, m, sessions)?.status === 'stopped') return one('stopped', 'Stopped: a person stopped its session')
    const code = exitFailure(run, m, sessions)
    if (code !== null) {
      const l = one('failed', `Failed: its agent exited with code ${code}`)
      return { ...l, view: { ...l.view, word: `Failed (exit ${code})` } }
    }
    return one('finished')
  }
  if (status === 'pending') {
    if (run.stoppedAt) return one('stopped', 'Stopped: the run was stopped before it started')
    // A run that ended (its tasks' outcome, the server's end) starts nothing more: its member never will.
    if (run.outcome || run.state === 'interrupted') return one('stopped', 'Stopped: the run ended before it started')
    return one('waiting')
  }
  if (status === 'starting') return one('waiting', 'Waiting to start: its agent is starting')
  switch (mark.key) {
    case 'needs':
      return one('needs')
    case 'working':
      return one('working')
    case 'finished':
      return one('finished')
    case 'exited':
      return { ...one('idle'), view: { key: 'running', word: mark.word.charAt(0).toUpperCase() + mark.word.slice(1), legend: mark.legend } }
  }
  return one('idle')
}

/**
 * A member's question as a run's card and tile offer it (C6): its words, and, where it has choices a page may press, the line of the
 * run's chat that question is (the session keeps a question in the chat under the question's own id, so a choice pressed names the
 * question the person was shown, as a press in the chat does). No line for a question without choices, for a decision's (answered
 * only as that decision), for one the terminal already answered, or from an owner that gives no id: those keep "Answer", the way in.
 */
export function memberAsk(name: string, s: Pick<SessionInfo, 'agentId' | 'attention'> | undefined): { question: string; line: ChatMessage | null } {
  const a = liveAttention(s?.attention)
  if (a?.state !== 'needs_input') return { question: '', line: null }
  const question = attentionWords(a) || 'waits on a prompt'
  const options = offeredChoices(a)
  if (!a.id || !options.length) return { question, line: null }
  const line: ChatMessage = { t: 'chat', id: a.id, at: a.since ?? '', scope: 'run', kind: 'question', by: { id: 'agent', name: s!.agentId, role: 'agent' }, text: question, on: name, options }
  return { question, line }
}

/** The members of a run as the rows, cards and tiles draw them. */
export function memberLooks(run: RunInfo, sessions: readonly SessionInfo[], now = Date.now()): MemberLook[] {
  return run.members.map((m) => memberLook(run, m, sessions, now))
}

/**
 * The time of day, everywhere a run's time is written (C3): 24-hour whatever the browser's locale ("19:28", "00:05", never "07:28
 * PM" nor "24:05"), as the sidebar, the chat and the activity log write it; with `seconds`, "19:28:50". '' for no time.
 */
export function clockTime(at: string | number, o: { seconds?: boolean; locale?: string } = {}): string {
  const d = new Date(at)
  if (Number.isNaN(d.getTime())) return ''
  return new Intl.DateTimeFormat(o.locale, { hour: '2-digit', minute: '2-digit', ...(o.seconds ? { second: '2-digit' } : {}), hourCycle: 'h23' }).format(d)
}

/**
 * How long a run took, in words (C12): "20 min so far" while it goes, "3 min", "under a minute", "1 h 4 min", "2 days". It ends at
 * its stop, at a run with tasks' outcome (its log's last line), or at its last member's end.
 */
export function tookWords(run: Pick<RunInfo, 'startedAt' | 'stoppedAt' | 'members' | 'state' | 'outcome' | 'log'>, live: boolean, now = Date.now()): string {
  if (Number.isNaN(Date.parse(run.startedAt))) return ''
  const words = lengthWords(runSpan(run, live, now))
  return live ? `${words} so far` : words
}

/** How long a run went, in ms: to now while it goes, else to its stop, to a run with tasks' outcome (its log's last line), or to its last member's end. */
export function runSpan(run: Pick<RunInfo, 'startedAt' | 'stoppedAt' | 'members' | 'state' | 'outcome' | 'log'>, live: boolean, now: number): number {
  const start = Date.parse(run.startedAt)
  if (Number.isNaN(start)) return 0
  if (live) return Math.max(0, now - start)
  if (!run.stoppedAt && run.outcome && run.log.length) {
    const end = Math.max(...run.log.map((e) => Date.parse(e.at)).filter((t) => !Number.isNaN(t)))
    if (Number.isFinite(end) && end >= start) return end - start
  }
  return runLength(run, now)
}

/** A length in words: "under a minute", "3 min", "1 h 4 min", "2 h", "2 days". */
export function lengthWords(ms: number): string {
  const min = Math.floor(ms / 60_000)
  if (min < 1) return 'under a minute'
  if (min < 60) return `${min} min`
  if (min < 48 * 60) {
    const h = Math.floor(min / 60)
    const rest = min % 60
    return rest ? `${h} h ${rest} min` : `${h} h`
  }
  return `${Math.floor(min / 1440)} days`
}

/** How many times a member asked a question in a run: the "asks" lines of its log. */
export function askedCount(log: readonly Pick<ActivityEntry, 'message'>[]): number {
  return log.filter((e) => /^\S+ asks "/.test(e.message ?? '')).length
}

/** The id's own part, after the crew's: the eight hex digits. */
export function shortRunId(id: string): string {
  const at = id.lastIndexOf('-')
  return at >= 0 ? id.slice(at + 1) : id
}

/** The clock time a run started, "08:31" (clockTime: 24-hour). */
export function startedClock(iso: string): string {
  return clockTime(iso)
}

/** The part after the crew's name: the run's own name when it was given one at launch, else "run started 08:31". */
export function runSubtitle(run: Pick<RunInfo, 'label' | 'startedAt'>): string {
  return run.label || `run started ${startedClock(run.startedAt)}`
}

/**
 * Where a launch that stays on the page is said (C11): in the crew's row of the home's table when the home shows that row, else (the
 * crew on another page of Saved crews, the person on another page) by a toast, so a launch is never said nowhere.
 */
export function launchSaidIn(onHome: boolean, shown: ReadonlyArray<{ id: string }>, crewId: string): 'row' | 'toast' {
  return onHome && shown.some((c) => c.id === crewId) ? 'row' : 'toast'
}

/** A run in a list of its crew's runs (C12): its own name when it has one, else "Run of today 08:31". */
export function runListName(run: Pick<RunInfo, 'label' | 'startedAt'>, now = Date.now()): string {
  return run.label || `Run of ${runWhen(run.startedAt, now)}`
}

/**
 * The ticket chips a run's header draws: "Launched from CND-12 · plan r3" for a run launched from a ticket's revision, and the
 * ticket the run is linked to, which is dropped when it names the ticket the run was launched from (one chip for one ticket; both
 * only when they name two, so a different linked ticket is never hidden).
 */
export function runTicketChips(launched: RunTicket | null | undefined, linked: RelatedTicket | null | undefined): { launched: RunTicket | null; linked: RelatedTicket | null } {
  return { launched: launched ?? null, linked: linked && linked.key !== launched?.key ? linked : null }
}

/** How a run is named where one line must do (a tab title, the sidebar): its own name, else its crew's. */
export function runTitle(run: Pick<RunInfo, 'name' | 'label'>): string {
  return run.label || run.name
}

/** When a run started, for a row: "today 08:31", "Oct 2 16:04", or without the time "Oct 2". The clock is the sidebar's (24-hour): one run reads the same everywhere. */
export function runWhen(iso: string, now = Date.now(), time = true): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const n = new Date(now)
  const hm = clockTime(d.getTime())
  if (d.getFullYear() === n.getFullYear() && d.getMonth() === n.getMonth() && d.getDate() === n.getDate()) return `today ${hm}`
  const md = d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
  return time ? `${md} ${hm}` : md
}

/**
 * Whether a run's row offers Resume as new run: a run that is not live, was stopped and has not been resumed already; a record of a run the
 * server no longer holds (#127) when it stopped, finished or was interrupted and has not been resumed from its record yet (#153).
 */
export function runResumable(r: RunInfo, live: boolean): boolean {
  if (r.record) return !r.resumedBy && (r.state === 'stopped' || r.state === 'finished' || r.state === 'interrupted')
  // A managed run is continued once it stopped or ended with an outcome (#334, row 8); a legacy run resumed once stopped.
  return !live && (!!r.stoppedAt || (!!r.managed && !!r.outcome)) && !r.resumedBy
}

/** What a member's tile reads, and in which tone: red only for an error of its own; what a stop cut off is grey. */
export function memberWords(run: Pick<RunInfo, 'stoppedAt'>, m: Pick<RunMember, 'start' | 'error'>, status: MemberStatus): { text: string; tone: 'warning' | 'error' | 'muted' | 'default' } {
  switch (status) {
    case 'needs_input':
      return { text: 'needs input', tone: 'warning' }
    case 'running':
      return { text: 'running', tone: 'default' }
    case 'starting':
      return { text: 'starting…', tone: 'muted' }
    case 'ended': {
      const err = ownError(m)
      if (err) return { text: `ended: ${err}`, tone: 'error' }
      if (m.error) return { text: 'not started', tone: 'muted' }
      return { text: run.stoppedAt ? 'stopped' : 'ended', tone: 'muted' }
    }
  }
  if (run.stoppedAt) return { text: 'not started', tone: 'muted' }
  if (m.start.when === 'after') return { text: `starts after ${m.start.member} is done`, tone: 'muted' }
  if (m.start.when === 'manual') return { text: 'waits for Start now', tone: 'muted' }
  return { text: 'waiting to start', tone: 'muted' }
}

/** The members of a run as the rows and cards draw them: each with its live status (memberStatus), its mark (memberStatusOf) and its words. */
export function memberTiles(run: RunInfo, sessions: readonly SessionInfo[], now = Date.now()) {
  return run.members.map((m) => {
    const { status, mark } = memberStatusOf(run, m, sessions, now)
    return { m, status, mark, words: memberWords(run, m, status) }
  })
}

/**
 * A run's note in a table: what a reader would ask about it. The members that ask ("review asks a question"), a member's own error
 * ("tests: exited 1"), what a stop cut off ("3 never started"), the members waiting for Start now, else what the run changed
 * ("+412 −58 on 5 branches").
 */
export function runNote(run: RunInfo, sessions: readonly SessionInfo[]): { text: string; tone: 'warning' | 'error' | 'muted' } {
  const tiles = memberTiles(run, sessions)
  const asking = tiles.filter((t) => t.status === 'needs_input').map((t) => t.m.name)
  if (asking.length) return { text: `${list(asking)} ask${asking.length === 1 ? 's' : ''} a question`, tone: 'warning' }
  const failed = tiles.find((t) => ownError(t.m))
  if (failed) return { text: `${failed.m.name}: ${ownError(failed.m)}`, tone: 'error' }
  if (run.stoppedAt) {
    const cut = tiles.filter((t) => t.status === 'pending' || (t.status === 'ended' && t.m.error === STOPPED_ERROR)).length
    if (cut) return { text: `${cut} never started`, tone: 'muted' }
  } else {
    const waits = tiles.filter((t) => t.status === 'pending' && t.m.start.when === 'manual').map((t) => t.m.name)
    if (waits.length) return { text: `${list(waits)} wait${waits.length === 1 ? 's' : ''} for Start now`, tone: 'muted' }
  }
  const diffs = run.members.filter((m) => m.diff)
  if (diffs.length) {
    const added = diffs.reduce((n, m) => n + m.diff!.added, 0)
    const removed = diffs.reduce((n, m) => n + m.diff!.removed, 0)
    const branches = run.members.filter((m) => m.branch).length
    return { text: `+${added} −${removed}${branches ? ` on ${branches} ${branches === 1 ? 'branch' : 'branches'}` : ''}`, tone: 'muted' }
  }
  const asked = askedCount(run.log)
  if (asked) return { text: `answered ${asked === 1 ? 'once' : asked === 2 ? 'twice' : `${asked} times`}`, tone: 'muted' }
  return { text: '', tone: 'muted' }
}

/** One bar of a crew's last runs: how long it ran, how it ended, and whether someone had to answer. */
export interface RunBar {
  id: string
  /** ms. */
  startedAt: number
  minutes: number
  phase: RunPhase
  /** Questions answered during the run. */
  asked: number
}

/** The last `limit` runs of a crew as bars, oldest first, from the runs newest first (live and recorded). */
export function runBars(runs: readonly RunInfo[], sessions: readonly SessionInfo[], now: number, limit = 12): RunBar[] {
  return runs
    .slice(0, limit)
    .map((r) => ({
      id: r.id,
      startedAt: Date.parse(r.startedAt) || 0,
      minutes: Math.round((runSpan(r, runLive(runState(r, sessions).state), now) / 60_000) * 10) / 10,
      phase: runPhase(r, sessions),
      asked: askedCount(r.log),
    }))
    .reverse()
}

/** A crew's runs need this many before the Runs tab draws them as a chart (C13): fewer say it in a sentence. */
export const CHART_AT = 5

/** The bars' colours, each the tone of its state's mark (RUN_LOOK): the live green, amber, neutral ink for succeeded, red, grey. */
export function barClass(p: RunPhase): string {
  switch (p) {
    case 'running':
      return 'bg-(--ui-success)'
    case 'needs':
      return 'bg-(--ui-warning)'
    case 'failed':
      return 'bg-(--ui-error)'
    case 'stopped':
      return 'bg-accented'
  }
  return 'bg-(--ui-text-muted)'
}

/** The median of the bars' lengths in minutes, 0 without bars. */
export function medianMinutes(bars: readonly RunBar[]): number {
  if (!bars.length) return 0
  const sorted = bars.map((b) => b.minutes).sort((a, b) => a - b)
  const mid = Math.floor(sorted.length / 2)
  return sorted.length % 2 ? sorted[mid]! : Math.round(((sorted[mid - 1]! + sorted[mid]!) / 2) * 10) / 10
}

/** "18m", "1h 4m", "33s" for a length in minutes. */
export function minutesLabel(minutes: number): string {
  if (minutes < 1) return `${Math.round(minutes * 60)}s`
  if (minutes < 60) return `${Math.round(minutes)}m`
  const h = Math.floor(minutes / 60)
  const m = Math.round(minutes % 60)
  return m ? `${h}h ${m}m` : `${h}h`
}

/** The chart card's second line: "duration in minutes · median 18m · 3 needed an answer · 1 failed". */
export function barsSummary(bars: readonly RunBar[]): string {
  const parts = [`duration in minutes · median ${minutesLabel(medianMinutes(bars))}`]
  const asked = bars.filter((b) => b.asked > 0).length
  if (asked) parts.push(`${asked} needed an answer`)
  const failed = bars.filter((b) => b.phase === 'failed').length
  if (failed) parts.push(`${failed} failed`)
  return parts.join(' · ')
}

const ENDED_WORDS: Record<RunPhase, string> = { running: 'is running', needs: 'needs you', succeeded: 'succeeded', failed: 'failed', stopped: 'was stopped' }

/**
 * The Runs tab's sentence where a chart would say little (C13: under CHART_AT runs): how the last run went and how long runs of the crew
 * usually take. "No runs yet."; "The only run so far failed after 3 min."; "The last run succeeded after 4 min; runs of this crew
 * usually take 6 min (the middle of 3)."; a run that still goes "is running, 2 min so far".
 */
export function runsSentence(bars: readonly RunBar[]): string {
  const last = bars.at(-1)
  if (!last) return 'No runs yet.'
  const live = last.phase === 'running' || last.phase === 'needs'
  const took = lengthWords(last.minutes * 60_000)
  const how = live ? `${ENDED_WORDS[last.phase]}, ${took} so far` : `${ENDED_WORDS[last.phase]} ${took === 'under a minute' ? 'in' : 'after'} ${took}`
  if (bars.length === 1) return `The only run so far ${how}.`
  const ended = bars.filter((b) => b.phase !== 'running' && b.phase !== 'needs')
  if (!ended.length) return `The last run ${how}.`
  const usual = lengthWords(medianMinutes(ended) * 60_000)
  return `The last run ${how}; runs of this crew usually take ${usual}${ended.length > 1 ? ` (the middle of ${ended.length})` : ''}.`
}

/** The fields of a draft that differ from the crew as saved, as the unsaved-changes bar names them: "Goal, Members". */
export function changedFields(draft: CrewInput, saved: CrewInput | undefined): string[] {
  if (!saved) return []
  const a = toCrewInput(draft)
  const b = toCrewInput(saved)
  const out: string[] = []
  if (a.name !== b.name) out.push('Name')
  if (a.goal !== b.goal) out.push('Goal')
  // In the Setup's own words: its Folder, its Workspace, its Yolo, and what More options folds.
  if (a.cwd !== b.cwd) out.push('Folder')
  if (a.isolation !== b.isolation) out.push('Workspace')
  if (a.yolo !== b.yolo) out.push('Yolo')
  if (a.openAfterLaunch !== b.openAfterLaunch || a.viewLinkTtlSeconds !== b.viewLinkTtlSeconds) out.push('More options')
  if (JSON.stringify(a.members) !== JSON.stringify(b.members)) out.push('Members')
  if (JSON.stringify(a.compare) !== JSON.stringify(b.compare)) out.push('Agent trial')
  return out
}

/** The Runs tab's filter: by the run's state (Running counts the runs that need you too) and by when they started. */
export type RunFilterState = 'all' | 'live' | 'succeeded' | 'failed' | 'stopped'
export type RunFilterSince = 'today' | '7d' | '30d' | 'all'
export interface RunFilter {
  state: RunFilterState
  since: RunFilterSince
}
export const DEFAULT_RUN_FILTER: RunFilter = { state: 'all', since: 'all' }
/** Where this browser keeps the filter: a habit of the reader's, not part of a link. */
export const RUN_FILTER_KEY = 'conductor.crewRuns.filter'
/** The chips, in their order, each with the vocabulary's word. */
export const RUN_FILTER_CHIPS: ReadonlyArray<{ value: RunFilterState; label: string }> = [
  { value: 'all', label: 'All' },
  { value: 'live', label: 'Running' },
  { value: 'succeeded', label: 'Succeeded' },
  { value: 'failed', label: 'Failed' },
  { value: 'stopped', label: 'Stopped' },
]

/** The chip a run's state falls under: Running holds the runs that need you. */
export function filterState(p: RunPhase): Exclude<RunFilterState, 'all'> {
  return p === 'running' || p === 'needs' ? 'live' : p
}

/** The instant a window begins: local midnight for today, now minus 7 or 30 days, -Infinity for all. */
export function sinceStart(since: RunFilterSince, now: number): number {
  if (since === 'all') return -Infinity
  if (since === 'today') {
    const d = new Date(now)
    d.setHours(0, 0, 0, 0)
    return d.getTime()
  }
  return now - (since === '7d' ? 7 : 30) * 86_400_000
}

function inWindow(startedAt: string, since: RunFilterSince, now: number): boolean {
  const t = Date.parse(startedAt)
  return !Number.isNaN(t) && t >= sinceStart(since, now)
}

/** The rows a filter keeps, in their order. */
export function filterRuns<T extends { run: Pick<RunInfo, 'startedAt'>; phase: RunPhase }>(rows: readonly T[], f: RunFilter, now: number): T[] {
  return rows.filter((r) => inWindow(r.run.startedAt, f.since, now) && (f.state === 'all' || filterState(r.phase) === f.state))
}

/** Each chip's count within the time window; All counts the window. */
export function filterCounts<T extends { run: Pick<RunInfo, 'startedAt'>; phase: RunPhase }>(rows: readonly T[], since: RunFilterSince, now: number): Record<RunFilterState, number> {
  const out: Record<RunFilterState, number> = { all: 0, live: 0, succeeded: 0, failed: 0, stopped: 0 }
  for (const r of rows) {
    if (!inWindow(r.run.startedAt, since, now)) continue
    out.all++
    out[filterState(r.phase)]++
  }
  return out
}

/** The filter as this browser kept it; the default for anything odd. */
export function readRunFilter(raw: string | null): RunFilter {
  try {
    const v = JSON.parse(raw ?? '') as Partial<RunFilter>
    const states: RunFilterState[] = ['all', 'live', 'succeeded', 'failed', 'stopped']
    const sinces: RunFilterSince[] = ['today', '7d', '30d', 'all']
    // "finished" and "ended" are what this browser kept before the chip was named Succeeded.
    const state = (v.state as string) === 'finished' || (v.state as string) === 'ended' ? 'succeeded' : v.state
    return {
      state: states.includes(state as RunFilterState) ? (state as RunFilterState) : 'all',
      since: sinces.includes(v.since as RunFilterSince) ? (v.since as RunFilterSince) : 'all',
    }
  } catch {
    return { ...DEFAULT_RUN_FILTER }
  }
}
