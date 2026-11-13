import type { CrewInput, CrewMember, CrewStart, RunInfo, RunMember, SessionInfo } from '~/composables/useSessions'
import type { ActivityEntry } from './protocol'
import { runLength } from './charts'
import { memberStatus, toCrewInput, type MemberStatus } from './crews'
import { runState, type RunState } from './runs'
import { relativeTime } from './sessions'

/**
 * The words of the Crews pages. Two nouns, said every time: a crew is a saved
 * plan, a run is one launch of it. A run is named by its crew and its start
 * ("users api · run started 08:31"), never by its id alone; a saved crew never
 * says Running, only "1 live run"; a member a stop cut off reads "not started"
 * or "stopped" in grey, and red is kept for an error of its own.
 */

/** The error the server gives a member a stop caught before it ran (internal/crew ErrRunStopped). */
export const STOPPED_ERROR = 'the run is stopped'

/** A member's own error: one that is not the run being stopped. */
export function ownError(m: Pick<RunMember, 'error'>): string {
  return m.error && m.error !== STOPPED_ERROR ? m.error : ''
}

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

/** The ways a run can be, as a badge says them. */
export type RunOutcome = 'needs you' | 'running' | 'finished' | 'stopped' | 'failed' | 'interrupted'

/** A run's outcome from its live state: failed when a member ended with an error of its own, whether the run was then stopped or not. */
export function runOutcome(run: Pick<RunInfo, 'members'>, state: RunState): RunOutcome {
  if (state === 'needs_input') return 'needs you'
  if (state === 'running') return 'running'
  if (state === 'interrupted') return 'interrupted'
  if (run.members.some((m) => ownError(m))) return 'failed'
  return state === 'stopped' ? 'stopped' : 'finished'
}

/** The badge of an outcome: its colour, and whether it is drawn solid-ish (the ones to act on) or as an outline. */
export function outcomeBadge(o: RunOutcome): { label: string; color: 'warning' | 'success' | 'error' | 'neutral'; variant: 'subtle' | 'outline' } {
  switch (o) {
    case 'needs you':
      return { label: 'needs you', color: 'warning', variant: 'subtle' }
    case 'running':
      return { label: 'running', color: 'success', variant: 'subtle' }
    case 'failed':
      return { label: 'failed', color: 'error', variant: 'subtle' }
    case 'stopped':
      return { label: 'stopped', color: 'neutral', variant: 'outline' }
    case 'interrupted':
      return { label: 'interrupted', color: 'warning', variant: 'outline' }
  }
  return { label: 'finished', color: 'neutral', variant: 'outline' }
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

/** The clock time a run started, "08:31". */
export function startedClock(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
}

/** The part after the crew's name: the run's own name when it was given one at launch, else "run started 08:31". */
export function runSubtitle(run: Pick<RunInfo, 'label' | 'startedAt'>): string {
  return run.label || `run started ${startedClock(run.startedAt)}`
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
  const hm = d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', hour12: false })
  if (d.getFullYear() === n.getFullYear() && d.getMonth() === n.getMonth() && d.getDate() === n.getDate()) return `today ${hm}`
  const md = d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
  return time ? `${md} ${hm}` : md
}

/** How long a run has gone or went: "up 4m" while it goes, else "22m". */
/**
 * Whether a run's row offers Resume as new run: a run that is not live, was stopped and has not been resumed already; a record of a run the
 * server no longer holds (#127) when it stopped, finished or was interrupted and has not been resumed from its record yet (#153).
 */
export function runResumable(r: RunInfo, live: boolean): boolean {
  if (r.record) return !r.resumedBy && (r.state === 'stopped' || r.state === 'finished' || r.state === 'interrupted')
  // A managed run is continued once it stopped or ended with an outcome (#334, row 8); a legacy run resumed once stopped.
  return !live && (!!r.stoppedAt || (!!r.managed && !!r.outcome)) && !r.resumedBy
}

export function runTook(run: Pick<RunInfo, 'startedAt' | 'stoppedAt' | 'members' | 'state'>, live: boolean, now = Date.now()): string {
  if (live) return `up ${relativeTime(run.startedAt, now)}`
  const start = Date.parse(run.startedAt)
  return Number.isNaN(start) ? '' : relativeTime(run.startedAt, start + runLength(run, now))
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

/** The dot on a member's tile: amber needs you, green runs, blue starts, grey ended, red ended with its own error, an outline for pending. */
export function memberDotClass(status: MemberStatus, m: Pick<RunMember, 'error'>): string {
  switch (status) {
    case 'needs_input':
      return 'bg-warning'
    case 'running':
      return 'bg-success'
    case 'starting':
      return 'bg-info'
    case 'ended':
      return ownError(m) ? 'bg-error' : 'bg-neutral-400'
  }
  return 'bg-transparent ring-1 ring-neutral-400'
}

/** The members of a run as the rows and cards draw them: each with its live status, its dot and its words. */
export function memberTiles(run: RunInfo, sessions: readonly SessionInfo[]) {
  return run.members.map((m) => {
    const status = memberStatus(run, m, sessions)
    return { m, status, dot: memberDotClass(status, m), words: memberWords(run, m, status) }
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
  outcome: RunOutcome
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
      minutes: Math.round((runLength(r, now) / 60_000) * 10) / 10,
      outcome: runOutcome(r, runState(r, sessions).state),
      asked: askedCount(r.log),
    }))
    .reverse()
}

/** The bars' colours: finished green, stopped grey, failed red, still going the brand's primary. */
export function barClass(o: RunOutcome): string {
  switch (o) {
    case 'finished':
      return 'bg-success'
    case 'failed':
      return 'bg-error'
    case 'stopped':
      return 'bg-neutral-400'
  }
  return 'bg-primary'
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
  const failed = bars.filter((b) => b.outcome === 'failed').length
  if (failed) parts.push(`${failed} failed`)
  return parts.join(' · ')
}

/** The live-run banner's words: when it started, how many run, who needs you, who waits for Start now. */
export function liveRunLine(run: RunInfo, sessions: readonly SessionInfo[]): { started: string; running: number; needs: string[]; waits: string[] } {
  const tiles = memberTiles(run, sessions)
  return {
    started: startedClock(run.startedAt),
    running: tiles.filter((t) => t.status === 'running' || t.status === 'starting').length,
    needs: tiles.filter((t) => t.status === 'needs_input').map((t) => t.m.name),
    waits: tiles.filter((t) => t.status === 'pending' && t.m.start.when === 'manual').map((t) => t.m.name),
  }
}

/** The fields of a draft that differ from the crew as saved, as the unsaved-changes bar names them: "Goal, Members". */
export function changedFields(draft: CrewInput, saved: CrewInput | undefined): string[] {
  if (!saved) return []
  const a = toCrewInput(draft)
  const b = toCrewInput(saved)
  const out: string[] = []
  if (a.name !== b.name) out.push('Name')
  if (a.goal !== b.goal) out.push('Goal')
  if (a.cwd !== b.cwd) out.push('Working directory')
  if (a.isolation !== b.isolation || a.openAfterLaunch !== b.openAfterLaunch || a.viewLinkTtlSeconds !== b.viewLinkTtlSeconds || a.yolo !== b.yolo) out.push('Each run')
  if (JSON.stringify(a.members) !== JSON.stringify(b.members)) out.push('Members')
  if (JSON.stringify(a.compare) !== JSON.stringify(b.compare)) out.push('Comparison')
  return out
}

/** The Runs tab's filter: by how runs ended (live is running or needing you) and by when they started. */
export type RunFilterState = 'all' | 'live' | 'finished' | 'stopped' | 'failed'
export type RunFilterSince = 'today' | '7d' | '30d' | 'all'
export interface RunFilter {
  state: RunFilterState
  since: RunFilterSince
}
export const DEFAULT_RUN_FILTER: RunFilter = { state: 'all', since: 'all' }
/** Where this browser keeps the filter: a habit of the reader's, not part of a link. */
export const RUN_FILTER_KEY = 'conductor.crewRuns.filter'

/** The chip an outcome falls under. */
export function filterState(o: RunOutcome): Exclude<RunFilterState, 'all'> {
  if (o === 'needs you' || o === 'running') return 'live'
  // A run the server's end cut short did not finish: it sits with the stopped ones.
  return o === 'interrupted' ? 'stopped' : o
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
export function filterRuns<T extends { run: Pick<RunInfo, 'startedAt'>; outcome: RunOutcome }>(rows: readonly T[], f: RunFilter, now: number): T[] {
  return rows.filter((r) => inWindow(r.run.startedAt, f.since, now) && (f.state === 'all' || filterState(r.outcome) === f.state))
}

/** Each chip's count within the time window; All counts the window. */
export function filterCounts<T extends { run: Pick<RunInfo, 'startedAt'>; outcome: RunOutcome }>(rows: readonly T[], since: RunFilterSince, now: number): Record<RunFilterState, number> {
  const out: Record<RunFilterState, number> = { all: 0, live: 0, finished: 0, stopped: 0, failed: 0 }
  for (const r of rows) {
    if (!inWindow(r.run.startedAt, since, now)) continue
    out.all++
    out[filterState(r.outcome)]++
  }
  return out
}

/** The filter as this browser kept it; the default for anything odd. */
export function readRunFilter(raw: string | null): RunFilter {
  try {
    const v = JSON.parse(raw ?? '') as Partial<RunFilter>
    const states: RunFilterState[] = ['all', 'live', 'finished', 'stopped', 'failed']
    const sinces: RunFilterSince[] = ['today', '7d', '30d', 'all']
    return {
      state: states.includes(v.state as RunFilterState) ? (v.state as RunFilterState) : 'all',
      since: sinces.includes(v.since as RunFilterSince) ? (v.since as RunFilterSince) : 'all',
    }
  } catch {
    return { ...DEFAULT_RUN_FILTER }
  }
}
