import type { RunInfo, SessionInfo } from '~/composables/useSessions'
import { memberSession } from './crews'
import { runTitle } from './crewWords'
import { memberStatusOf, type MarkView } from './status'

// A crew member's page carries its crew (UX3 R12): one strip above the session's header with `Back to the crew`, the run's title and
// a tab for each member. The page keeps its own address; the strip is how a person sees the crew and gets back to it.

/** What `Back to the crew` says it goes to, as its tooltip: the run's title once the run is read. */
export const BACK_TO_CREW = 'Back to the crew'

/**
 * The run whose strip a session carries, '' for none: a member of a person's run. A Brakeman turn's session is a member of a run of
 * Brakeman's own, never a crew a person opened, and a session launched on its own has no crew.
 */
export function stripRunOf(s: Pick<SessionInfo, 'crew'> | null | undefined): string {
  return s?.crew && !s.crew.brakeman ? s.crew.runId : ''
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
