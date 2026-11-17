// Brakeman's page, Now (issue 993, F10): Conductor's own live counts, composed in the app from what the sidebar counts (so the page
// and the sidebar never disagree) and from the runs' tasks the app holds, and a line of what changed since the last briefing (GET
// /api/brakeman/since). No agent turn runs for it, and it spends no budget. Pure: no Nuxt, no fetch.
import type { RunInfo, SessionInfo } from '~/composables/useSessions'
import type { BrakemanSince, BrakemanSinceCount } from './brakeman'
import { parseRef } from './brakeman'
import type { NeedsItem, NeedsYou } from './needsYou'
import { needsCount } from './needsYou'
import type { SidebarItem, SidebarModel } from './sidebar'
import { itemWords } from './sidebar'
import { relativeTime } from './sessions'
import { attemptSession, finishedWord, statusOfKey, taskStatus, TASK_LOOK, type MarkView } from './status'

export type NowKey = 'needs' | 'working' | 'finished' | 'waiting'
export const NOW_KEYS: readonly NowKey[] = ['needs', 'working', 'finished', 'waiting']

export interface NowTile {
  key: NowKey
  /** How many, as the sidebar says it ("3", "3+" while the number is not known whole). */
  count: string
  /** The count and its word ("3 need you", "2 waiting"), or the tile's words for none ("Nothing needs you"). */
  words: string
  /** What they are, by name, at most NOW_NAMES of them and "+N" after. */
  names: string
  /** Where the tile leads. */
  to: string
  /** The tile's mark: none for a zero (an amber dot over a zero is a standing fact). */
  mark?: Pick<MarkView, 'key' | 'word' | 'legend'>
}

/** How many names a tile says before "+N". */
export const NOW_NAMES = 3

const NONE: Record<NowKey, string> = { needs: 'Nothing needs you', working: 'Nothing working', finished: 'Nothing finished', waiting: 'Nothing waiting' }
const WORD: Record<NowKey, string> = { needs: 'need you', working: 'working', finished: 'finished', waiting: 'waiting' }

/** Names, at most NOW_NAMES, then "+N". */
function nameList(names: readonly string[]): string {
  const shown = names.slice(0, NOW_NAMES)
  return names.length > NOW_NAMES ? `${shown.join(' · ')} +${names.length - NOW_NAMES}` : shown.join(' · ')
}

/** A thing that needs you in a few words: a question in its session, a decision's or a task's or a plan's title. */
export function needsName(item: NeedsItem): string {
  if (item.kind === 'question') return `a question in ${item.session?.name || item.session?.id || 'a session'}`
  return itemWords(item).title
}

/** A row of the sidebar's by name: a session's (Brakeman's turn's session as "Brakeman's turn"), a run's title. */
function rowName(it: SidebarItem, brakemanSession: string | undefined): string[] {
  if (it.kind === 'run') return [it.title]
  if (it.kind === 'item') return [itemWords(it.item).title]
  if (brakemanSession && it.id === brakemanSession) return ["Brakeman's turn"]
  return [it.session.name || it.id]
}

/** When a finished row's turn ended. */
function finishedAt(it: SidebarItem): string | undefined {
  if (it.kind === 'session') return it.session.attention?.since
  if (it.kind === 'run') return it.members[0]?.session.attention?.since
  return undefined
}

/** The tasks that wait for what they need (blocked; not ready, not a member that has not started), by run, in the runs' order. */
export function waitingTasks(runs: readonly RunInfo[], sessions: readonly SessionInfo[], now = Date.now()): Array<{ run: RunInfo; task: string; behind: string[] }> {
  const out: Array<{ run: RunInfo; task: string; behind: string[] }> = []
  for (const run of runs) {
    if (run.class === 'brakeman' || run.stoppedAt || run.outcome || run.record) continue
    const state = new Map((run.tasks ?? []).map((t) => [t.id, t.state]))
    for (const t of run.tasks ?? []) {
      const m = taskStatus(t, run, attemptSession(t, sessions), now)
      if (m.key !== 'waiting' || m.word !== TASK_LOOK.waiting.word) continue
      out.push({ run, task: t.id, behind: (t.needs ?? []).filter((n) => state.get(n) !== 'succeeded') })
    }
  }
  return out
}

/** Waiting tasks grouped by what they wait on: "review and deploy, behind build". */
export function waitingWords(waiting: ReadonlyArray<{ task: string; behind: string[] }>): string[] {
  const and = (xs: readonly string[]) => (xs.length <= 1 ? (xs[0] ?? '') : `${xs.slice(0, -1).join(', ')} and ${xs[xs.length - 1]}`)
  const groups = new Map<string, string[]>()
  for (const w of waiting) {
    const key = w.behind.join('\n')
    groups.set(key, [...(groups.get(key) ?? []), w.task])
  }
  return [...groups].map(([behind, tasks]) => (behind ? `${and(tasks)}, behind ${and(behind.split('\n'))}` : and(tasks)))
}

/**
 * Now's four tiles: need you (the sidebar's Needs you number, the one list's), working and finished (the sidebar's section counts, of
 * the same model), and the tasks waiting on others. Each names what it counts; a zero says so, with no mark.
 */
export function nowTiles(o: {
  model: Pick<SidebarModel, 'needs' | 'working' | 'finished' | 'counts'>
  needs: Pick<NeedsYou, 'number' | 'known' | 'more' | 'items'>
  runs: readonly RunInfo[]
  sessions: readonly SessionInfo[]
  /** The session of Brakeman's turn under way, named "Brakeman's turn". */
  brakemanSession?: string
  now?: number
}): NowTile[] {
  const now = o.now ?? Date.now()
  const waiting = waitingTasks(o.runs, o.sessions, now)
  const tile = (key: NowKey, n: number, count: string, names: string, to: string, mark: Pick<MarkView, 'key' | 'word' | 'legend'>): NowTile =>
    n > 0 || count.endsWith('+') ? { key, count, words: `${count} ${WORD[key]}`, names, to, mark } : { key, count: '0', words: NONE[key], names: '', to }
  // The number as the sidebar draws it: none while a store is unread or failed (unknown is not zero), "N+" while one is cut short.
  const needsNumber = needsCount(o.needs)
  const needs: NowTile = needsNumber ? tile('needs', o.needs.number, needsNumber, nameList(o.needs.items.map(needsName)), '/decisions', statusOfKey('needs')) : { key: 'needs', count: '', words: 'Needs you', names: '', to: '/decisions' }
  return [
    needs,
    tile('working', o.model.counts.working, String(o.model.counts.working), nameList(o.model.working.flatMap((it) => rowName(it, o.brakemanSession))), '/yard', statusOfKey('working')),
    tile(
      'finished',
      o.model.counts.finished,
      String(o.model.counts.finished),
      nameList(o.model.finished.flatMap((it) => rowName(it, o.brakemanSession).map((n) => `${n} ${finishedWord(finishedAt(it), now).replace(/^finished ?/, '')}`.trim()))),
      '/yard',
      statusOfKey('finished'),
    ),
    tile('waiting', waiting.length, String(waiting.length), nameList(waitingWords(waiting)), waiting[0] ? `/runs/${encodeURIComponent(waiting[0].run.id)}` : '/yard', { key: 'waiting', ...TASK_LOOK.waiting }),
  ]
}

/** A ref the since line names, by name: a run's, a task's with its run's, a ticket's key, a question's session. */
export function sinceName(ref: string, names: { run?: (id: string) => string | undefined; session?: (id: string) => string | undefined } = {}): string {
  const r = parseRef(ref)
  if (!r) return ref
  switch (r.kind) {
    case 'run':
      return names.run?.(r.run!) || r.run!
    case 'task':
      return `${r.task} of ${names.run?.(r.run!) || r.run}`
    case 'ticket':
      return r.ticket!
    case 'question':
      return names.session?.(r.session!) || r.session!
  }
  return ref
}

const named = (c: BrakemanSinceCount, names: Parameters<typeof sinceName>[1]) => {
  const shown = c.refs.map((r) => sinceName(r, names))
  return shown.length ? ` (${shown.join(', ')}${c.count > shown.length ? `, +${c.count - shown.length}` : ''})` : ''
}

/**
 * What changed since the last briefing, in one line: "Since the last briefing, at 02:55: 1 run ended (checkout-migration) · 1 task
 * failed (build of fix-login) · 1 ticket made ready (CND-52) · 1 question waiting 42m (ui)". With no briefing kept it is the last day's;
 * a part with nothing in it is left out, and with nothing at all the line says so. The time is the browser's (`clock`).
 */
export function sinceWords(s: BrakemanSince, clock: (iso: string) => string, names: Parameters<typeof sinceName>[1] = {}, now = Date.now()): string {
  const head = s.since.from === 'day' ? 'In the last day' : s.since.from === 'briefing' ? `Since the last briefing, at ${clock(s.since.at)}` : `Since ${clock(s.since.at)}`
  const parts: string[] = []
  const n = (c: BrakemanSinceCount | undefined) => c?.count ?? 0
  if (n(s.runsEnded)) parts.push(`${s.runsEnded.count} ${s.runsEnded.count === 1 ? 'run' : 'runs'} ended${named(s.runsEnded, names)}`)
  if (n(s.tasksFailed)) parts.push(`${s.tasksFailed.count} ${s.tasksFailed.count === 1 ? 'task' : 'tasks'} failed${named(s.tasksFailed, names)}`)
  if (n(s.ticketsReady)) parts.push(`${s.ticketsReady.count} ${s.ticketsReady.count === 1 ? 'ticket' : 'tickets'} made ready${named(s.ticketsReady, names)}`)
  if (n(s.questionsOpen)) {
    const q = s.questionsOpen
    const age = q.oldest ? relativeTime(q.oldest, now) : ''
    const waits = q.count === 1 ? `1 question waiting${age ? ` ${age}` : ''}` : `${q.count} questions waiting${age ? `, the oldest ${age}` : ''}`
    parts.push(`${waits}${named(q, names)}`)
  }
  if (!parts.length) return s.since.from === 'day' ? 'Nothing has changed in the last day.' : `Nothing has changed ${head.charAt(0).toLowerCase()}${head.slice(1)}.`
  return `${head}: ${parts.join(' · ')}${s.truncated ? ' · more than these, not all counted' : ''}`
}
