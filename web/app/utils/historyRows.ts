import { isEnded } from './attention'
import { historyTarget, hostWords, repoName, type HistoryItem } from './history'
import { sessionStatus, statusOfKey, type StatusInput, type StatusView } from './status'

// The History page's rows, as the review of its screens asked (#1024): the name first, one line of details, one state word in the
// app's status language, the time at the right, the rows under day headings, and the row's action written out. Pure: the page and
// HistoryRow draw what these say.

/** One piece of a row's line of details: what it is, its words, and its whole text where the words are cut. */
export interface RowPart {
  field: 'agent' | 'host' | 'repo' | 'branch'
  text: string
  title?: string
}

/** A long name cut in its middle, its start and its end kept: `crew/reconcile-the-…-6b7eefa5/reconcile.3`. */
export function cutMiddle(s: string, max = 32): string {
  if (s.length <= max) return s
  const keep = max - 1
  const head = Math.ceil(keep / 2)
  return `${s.slice(0, head)}…${s.slice(s.length - (keep - head))}`
}

/** A session's id from its entry's key, '' for an entry that is not a session's own. */
export function sessionIdOf(e: Pick<HistoryItem, 'kind' | 'key'>): string {
  return e.kind === 'session' && e.key.startsWith('session:') ? e.key.slice('session:'.length) : ''
}

/** The agent in a row's words: its name, else its id; for an entry that is not a session's, where it was said or done. */
function agentWords(e: Pick<HistoryItem, 'kind' | 'agent' | 'source'>): string {
  if (e.kind === 'session') return e.agent?.name || e.agent?.id || (e.source?.label ?? '').replace(/ session$/, '')
  return e.source?.label ?? ''
}

/**
 * A row's one line of details: agent · host (a hosted session's alone: this machine's goes without saying) · repository · branch, cut
 * in the middle when long; the session's id after them, and its folder for the line's title (the path is never printed in the line).
 * The time and the state are drawn apart from it, so neither is said twice.
 */
export function rowDetails(e: HistoryItem): { parts: RowPart[]; id: string; path: string } {
  const parts: RowPart[] = []
  const add = (field: RowPart['field'], text: string, title?: string) => {
    if (text) parts.push(title && title !== text ? { field, text, title } : { field, text })
  }
  add('agent', agentWords(e))
  if (e.host?.hosted) add('host', hostWords(e.host))
  add('repo', e.project?.key || repoName(e.project ?? {}))
  const branch = e.revision?.label ?? ''
  add('branch', cutMiddle(branch), branch)
  return { parts, id: sessionIdOf(e), path: e.project?.tree || '' }
}

/**
 * A session row's state, in the app's one status language (sessionStatus, the words the sidebar and the session page say): the listed
 * session's own state where the page holds it (needs you, working, finished, running, exited, exit N), else the state its record kept
 * (a session that ended: exited or exit N; a hosted one whose host is away: host away; one still live: running). A record the server
 * stopped observing while it ran says so: not observed. Null for an entry that is not a session's own (its class badge says what it is).
 */
export function rowStatus(e: Pick<HistoryItem, 'kind' | 'outcome' | 'availability'>, listed?: StatusInput | null, now = Date.now()): StatusView | null {
  if (e.kind !== 'session') return null
  if (listed) return sessionStatus(listed, now)
  const out = e.outcome ?? ''
  const ended = /^(exited|stopped)(?: \(exit (-?\d+)\))?$/.exec(out)
  if (ended) return sessionStatus({ status: ended[1]!, exitCode: ended[2] === undefined ? undefined : Number(ended[2]) }, now)
  const state = e.availability?.state ?? ''
  if (out === 'host away' || state === 'host_away') return sessionStatus({ status: 'host_disconnected' }, now)
  if (state === 'live') return statusOfKey('running')
  if (out === 'lost' || state === 'lost' || out === 'running') {
    return { key: 'exited', word: 'not observed', legend: 'not observed: the server stopped while it ran; whether its process ended is not known' }
  }
  return sessionStatus({ status: 'exited' }, now)
}

/** What pressing a row does, in words: Open for a thing still live (and for every entry that is not a session's own), Transcript for a session that ended. */
export interface RowAction {
  label: 'Open' | 'Transcript'
  to: string
}

/** A row's action and where it goes (the page that shows its evidence, as the server names it); null when it opens nothing. */
export function rowAction(e: Pick<HistoryItem, 'kind' | 'url' | 'anchor' | 'availability'>, listed?: Pick<StatusInput, 'status'> | null): RowAction | null {
  const to = historyTarget(e)
  if (!to) return null
  if (e.kind !== 'session') return { label: 'Open', to }
  const live = listed ? !isEnded(listed.status) : e.availability?.state === 'live'
  return { label: live ? 'Open' : 'Transcript', to }
}

/** A time as the person's local day: `2026-11-18`. */
export function localDay(t: number): string {
  const d = new Date(t)
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}

/** A day's heading: Today, Yesterday, else its weekday, day and month ("Mon 16 Nov"), with its year when it is not this year's. */
export function dayWords(day: string, now = Date.now()): string {
  if (day === localDay(now)) return 'Today'
  const y = new Date(now)
  y.setDate(y.getDate() - 1)
  if (day === localDay(y.getTime())) return 'Yesterday'
  const [yy, mm, dd] = day.split('-').map(Number) as [number, number, number]
  const d = new Date(yy, mm - 1, dd)
  const words = `${d.toLocaleDateString('en-GB', { weekday: 'short' })} ${dd} ${d.toLocaleDateString('en-GB', { month: 'short' })}`
  return yy === new Date(now).getFullYear() ? words : `${words} ${yy}`
}

/** A row's time at the right of its name: the clock time of its local day ("09:41"); its whole date and time are its title. */
export function clockTime(at: string): string {
  const t = Date.parse(at)
  return Number.isNaN(t) ? '' : new Date(t).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit', hour12: false })
}

/** One run of rows under its day's heading. */
export interface DayGroup<T> {
  /** Unique among the runs: the day and the place of the run's first row (a day can head two runs of a ranked answer). */
  key: string
  day: string
  label: string
  entries: T[]
}

/**
 * The rows under day headings, by the person's local day, in the server's order: a heading starts wherever a row's day differs from
 * the row's before it, never by collecting a day's rows from all over the answer. A newest-first answer (the latest sessions, filters
 * alone) gets one heading a day; a ranked one keeps its best match first, and Show more's rows land at the end. A key seen twice (two
 * pages) is kept once. An entry with no time it can read stands under "Undated", where it falls.
 */
export function byDay<T extends Pick<HistoryItem, 'key' | 'at'>>(entries: readonly T[], now = Date.now()): Array<DayGroup<T>> {
  const out: Array<DayGroup<T>> = []
  const seen = new Set<string>()
  for (const e of entries) {
    if (seen.has(e.key)) continue
    seen.add(e.key)
    const t = Date.parse(e.at)
    const day = Number.isNaN(t) ? '' : localDay(t)
    const last = out[out.length - 1]
    if (last && last.day === day) last.entries.push(e)
    else out.push({ key: `${day || 'undated'}:${seen.size - 1}`, day, label: day ? dayWords(day, now) : 'Undated', entries: [e] })
  }
  return out
}

/**
 * The Agent filter's chips: the agents that have history first, the most used first (then by name), and the rest behind "More agents".
 * `used` counts each agent's sessions in the records the page read; an agent with history the catalog no longer names keeps its id.
 * A pressed agent is never hidden behind More.
 */
export function agentChips(agents: ReadonlyArray<{ id: string; name: string }>, used: Readonly<Record<string, number>>, pressed: readonly string[] = []): { shown: Array<{ value: string; label: string }>; more: Array<{ value: string; label: string }> } {
  const name = new Map(agents.map((a) => [a.id, a.name || a.id]))
  const withHistory = Object.entries(used)
    .filter(([, n]) => n > 0)
    .sort(([a, x], [b, y]) => y - x || (name.get(a) ?? a).localeCompare(name.get(b) ?? b))
    .map(([id]) => id)
  // What the query presses, as the server reads it: lower case, each once (`agent:Codex`, `agent:x,x`).
  const on = [...new Set(pressed.map((p) => p.toLowerCase()).filter(Boolean))]
  const shownIds = [...withHistory, ...on.filter((p) => !withHistory.includes(p))]
  const shown = shownIds.map((id) => ({ value: id, label: name.get(id) ?? id }))
  const more = agents.filter((a) => !shownIds.includes(a.id)).map((a) => ({ value: a.id, label: a.name || a.id }))
  return { shown, more }
}
