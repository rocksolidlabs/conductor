// Brakeman's background role on the app's pages (docs/plans/brakeman.md, BK3, PR-15): its states and its wakes in words, the
// role's controls' bounds (BOUNDS in ./brakeman, the server's), and what the page says of an unavailable role and of what tries
// again. Pure: no Nuxt, no fetch. The records are internal/crew/role.go's, as GET /api/brakeman/role answers them.
import type { BrakemanRole, RoleBatch, RoleLogRow } from './brakeman'
import { refLabel } from './brakeman'

/** The role's states, as internal/crew/role.go names them (RoleStates). */
export const ROLE_STATES = ['idle', 'gathering', 'briefing', 'cooling', 'paused', 'unavailable'] as const

/** The wake sources, as internal/crew/role.go names them (WakeSources), with what the log calls each. */
export const WAKE_SOURCES: Readonly<Record<string, string>> = {
  task_failed: 'A task failed',
  question_open: 'A question waited',
  run_ended: 'A run ended',
  run_paused: 'A run paused',
  ticket_ready: 'A ticket became ready',
  request: 'Your request',
  briefing: 'A briefing time',
}

/** What the log calls a wake source. */
export function sourceWords(source: string): string {
  return WAKE_SOURCES[source] ?? source
}

/** A time as the page shows it: the clock on today's date, else the date too. */
export function clockTime(iso: string | undefined, now = new Date()): string {
  if (!iso) return ''
  const t = new Date(iso)
  if (Number.isNaN(t.getTime())) return ''
  const hm = t.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  return t.toDateString() === now.toDateString() ? hm : `${t.toLocaleDateString()} ${hm}`
}

/** How many wakes a batch counts, its overflow's too. */
export function batchCount(b: RoleBatch | undefined): number {
  if (!b) return 0
  return b.wakes.reduce((n, w) => n + w.count, 0) + (b.overflow?.count ?? 0)
}

/**
 * The role's state in words: idle, gathering (the batch and when its window ends, given the window), briefing (its request),
 * cooling (until when), paused, unavailable (why).
 */
export function roleWords(r: Pick<BrakemanRole, 'state' | 'why' | 'open' | 'request' | 'coolUntil'>, windowSeconds?: number, now = new Date()): string {
  switch (r.state) {
    case 'idle':
      return 'idle: nothing has woken it'
    case 'gathering': {
      const n = batchCount(r.open)
      const wakes = `${n} ${n === 1 ? 'wake' : 'wakes'}`
      if (r.open && windowSeconds) {
        const ends = new Date(new Date(r.open.openedAt).getTime() + windowSeconds * 1000).toISOString()
        return `gathering ${wakes} into ${r.open.id}: its turn starts at ${clockTime(ends, now)}`
      }
      return `gathering ${wakes}${r.open ? ` into ${r.open.id}` : ''}`
    }
    case 'briefing':
      return r.request ? `briefing: ${r.request}` : 'briefing'
    case 'cooling':
      return r.coolUntil ? `cooling until ${clockTime(r.coolUntil, now)}: wakes meanwhile wait for the next turn` : 'cooling'
    case 'paused':
      return 'paused: nothing wakes it until you resume it'
    case 'unavailable':
      return r.why ? `unavailable: ${r.why}` : 'unavailable'
  }
  return r.state
}

/** What else tries again for an unavailable role, by the reason it is (the server's crew.SaveEnds and Role.Started). */
const RETRY_ASKED: Record<string, string> = {
  start: 'its agent reading a request (yours, once it is signed in or past its screen) or any save of its settings',
  budget: 'your next request, a change of its budget or the next briefing time',
  off: 'your next request, turning it on in its settings or the next briefing time',
  stuck: 'your next request or the next briefing time, once the last run is stopped',
}

/** What tries again for an unavailable role, in words ("" for a role that is not). */
export function retryWords(r: Pick<BrakemanRole, 'state' | 'retryAt' | 'reason'>, now = new Date()): string {
  if (r.state !== 'unavailable') return ''
  const asked = RETRY_ASKED[r.reason ?? ''] ?? 'your next request, a save of its settings or the next briefing time'
  return r.retryAt ? `It tries again at ${clockTime(r.retryAt, now)}, or sooner on ${asked}.` : `It tries again on ${asked}. Meanwhile the wakes wait in its next batch, and nothing else waits on it.`
}

/** A row of the role's log in words: its source and subject, and what became of it (its batch and request, or why it joined none). */
export function rowWords(row: RoleLogRow): { what: string; fate: string } {
  const what = row.subject ? `${sourceWords(row.source)}: ${subjectLabel(row.subject)}` : sourceWords(row.source)
  let fate = ''
  if (row.held) fate = row.held === 'paused' ? 'not gathered: the role was paused' : `no batch: ${row.held}`
  else if (row.request) fate = `${row.batch} → ${row.request}`
  else if (row.batch) fate = `waiting in ${row.batch}`
  return { what, fate }
}

/** A wake's subject as the page names it: a ref's label, a request's id as it is. */
export function subjectLabel(subject: string): string {
  return /^br-[1-9][0-9]{0,17}$/.test(subject) ? subject : refLabel(subject)
}

/** Whether the role's controls offer Pause (any state but paused) or Resume. */
export function pauseAction(r: Pick<BrakemanRole, 'state'> | undefined): 'pause' | 'resume' | null {
  if (!r) return null
  return r.state === 'paused' ? 'resume' : 'pause'
}

/**
 * Orders a page's reads of one thing that may overlap (on mount, on each of the role's events, after a control): each read takes a
 * ticket as it begins, and its reply is taken only while no read began after it, so an older reply arriving last never draws over a
 * newer one.
 */
export class ReadOrder {
  private last = 0
  /** A read begins: its ticket. */
  begin(): number {
    return ++this.last
  }
  /** Whether the read with the ticket is still the newest begun. */
  current(ticket: number): boolean {
    return ticket === this.last
  }
}
