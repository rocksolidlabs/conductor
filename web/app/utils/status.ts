import type { SessionStatus } from '~/composables/useSessions'
import type { JoinedEntry } from '~/utils/joined'
import type { Attention } from '~/utils/protocol'
import { isEnded, liveAttention } from './attention'

/**
 * One status language (the UX review's P1; docs/design/system.md, Status): a session is in one of five states, each with one mark
 * and one word, and every surface that shows a session's state asks sessionStatus and draws StatusMark. No component reads
 * `attention.state` or `status` to pick a colour or a word.
 */
export type StatusKey = 'needs' | 'working' | 'finished' | 'running' | 'exited'

/** The five, in the order the legend and the Yard's bar give them. */
export const STATUS_KEYS: readonly StatusKey[] = ['needs', 'working', 'finished', 'running', 'exited']

/** A mark's shape: a filled dot, a filled dot inside a ring, a ring, a dashed ring. */
export type MarkShape = 'dot' | 'ringed-dot' | 'ring' | 'dashed-ring'
/** A mark's colour: amber for someone must act, green for a live agent, grey for the rest. */
export type StatusTone = 'warning' | 'success' | 'neutral'

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
export interface StatusView {
  key: StatusKey
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
