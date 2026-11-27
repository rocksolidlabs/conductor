import type { LocationQuery } from 'vue-router'
import type { RunInfo, SessionInfo } from '~/composables/useSessions'
import { memberSession } from './crews'
import { runTitle } from './crewWords'
import { memberStatusOf, type MarkView } from './status'

// A crew member's page carries its crew (UX3 R12): one strip above the session's header with `Back to the crew`, the run's title and
// a tab for each member. A member opens inside its crew (UX3 R72): its place is its run's page with `?session=<id>`, where the strip
// is how a person sees the crew and gets back to it; its old address lands there.

/** What `Back to the crew` says it goes to, as its tooltip: the run's title once the run is read. */
export const BACK_TO_CREW = 'Back to the crew'

/**
 * The run whose strip a session carries, '' for none: a member of a person's run. A Brakeman turn's session is a member of a run of
 * Brakeman's own, never a crew a person opened, and a session launched on its own has no crew.
 */
export function stripRunOf(s: Pick<SessionInfo, 'crew'> | null | undefined): string {
  return s?.crew && !s.crew.brakeman ? s.crew.runId : ''
}

/** Whether session `s` is a member of run `runId` that opens in its place: a Brakeman turn's session never is. */
export function memberOfRun(s: Pick<SessionInfo, 'crew'>, runId: string): boolean {
  return !!runId && stripRunOf(s) === runId
}

/** What a session's address carries to where it lands (`tab`, `open`, `line`), as a query to append with its `&`, or '' for none. */
export function carriedQuery(q: LocationQuery, lead = '?'): string {
  const kept = new URLSearchParams()
  for (const k of ['tab', 'open', 'line']) {
    const v = q[k]
    if (typeof v === 'string' && v) kept.set(k, v)
  }
  const t = kept.toString()
  return t ? `${lead}${t}` : ''
}

/**
 * A member's place inside its crew, for its old address (`/sessions/<id>`) to land on: its run's page with `?session=<id>`, `tab`, `open`
 * and `line` carried; '' for a session that is no member of a person's run, which keeps its page.
 */
export function memberPlace(s: Pick<SessionInfo, 'id' | 'crew'>, q: LocationQuery = {}): string {
  const run = stripRunOf(s)
  return run ? `/runs/${encodeURIComponent(run)}?session=${encodeURIComponent(s.id)}${carriedQuery(q, '&')}` : ''
}

/**
 * Where an in-app link or press opens session `id` (UX3 R72): a member of a person's run at its place inside its crew, else its own
 * page, `tab`, `open` and `line` carried either way. `s` is the session as the page knows it (the live list's, a reply's), or undefined
 * when it does not know it yet: the session's own page then, which lands a member in its crew. A press on the member already open is
 * the address the page is at, which the router takes as no move at all.
 */
export function sessionPlace(s: Pick<SessionInfo, 'crew'> | null | undefined, id: string, q: { tab?: string; open?: string; line?: string | number } = {}): string {
  const query: LocationQuery = {}
  for (const [k, v] of Object.entries(q)) if (v !== undefined && v !== '') query[k] = String(v)
  return (s && memberPlace({ id, crew: s.crew }, query)) || `/sessions/${encodeURIComponent(id)}${carriedQuery(query)}`
}

/**
 * The history state's mark of a move a session's read made (SessionView's goAway): the page it landed on is the read's word, and that
 * page waits for its own read of the session rather than judging by the live list, which can disagree with the read for a while (a
 * crew formed while this app's events were away). Without it, the two would send the page back and forth.
 */
export const READ_SAID = 'conductorReadSaid'

/** Whether the history entry the page is on was a move session `id`'s read made (READ_SAID). */
export function readSaidFor(state: unknown, id: string): boolean {
  return !!id && typeof state === 'object' && state !== null && (state as Record<string, unknown>)[READ_SAID] === id
}

/**
 * How `Back to the crew` goes back: the browser's Back when the page before is the run's own page with no member open (the person came
 * from it, so one Back later does not bring the member again), else a replace to it (a member opened from the sidebar, a link).
 */
export function backByHistory(back: unknown, runPath: string): boolean {
  if (typeof back !== 'string') return false
  const [path, query = ''] = back.split('?')
  return path === runPath && !new URLSearchParams(query).has('session')
}

/** Whether a run, once read, is one a strip is drawn for: a Brakeman turn's run draws none. */
export function stripsRun(run: Pick<RunInfo, 'class'>): boolean {
  return run.class !== 'brakeman'
}

/** One member's tab. */
export interface MemberTab {
  name: string
  /** The member's session a press opens; absent while it has none yet (the tab cannot be pressed). */
  sessionId?: string
  mark: MarkView
  /** The open member: the page shows this member's latest session. */
  selected: boolean
}

/**
 * The tabs of a run, in its members' order: each member's mark (memberStatusOf) and its session, the open one selected. A session that
 * is not a member's latest (an earlier attempt's, opened from a task of a run with tasks) selects no tab.
 */
export function memberTabs(run: RunInfo, sessions: readonly SessionInfo[], openId: string, now = Date.now()): MemberTab[] {
  return run.members.map((m) => {
    const sessionId = memberSession(run, m, sessions)?.id ?? m.sessionId
    return { name: m.name, sessionId, mark: memberStatusOf(run, m, sessions, now).mark, selected: !!sessionId && sessionId === openId }
  })
}

/** The tooltip of `Back to the crew`: the run it goes back to, by its title once it is read. */
export function backTitle(run: Pick<RunInfo, 'name' | 'label'> | undefined): string {
  return run ? `Back to ${runTitle(run)}` : BACK_TO_CREW
}
