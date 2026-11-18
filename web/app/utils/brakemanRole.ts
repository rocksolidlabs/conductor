// Brakeman's background role on the app's pages (docs/plans/brakeman.md, BK3, PR-15): its states and its wakes in words, the
// role's controls' bounds (BOUNDS in ./brakeman, the server's), and what the page says of an unavailable role and of what tries
// again. Pure: no Nuxt, no fetch. The records are internal/crew/role.go's, as GET /api/brakeman/role answers them.
import type { BrakemanRequest, BrakemanRole, RoleBatch, RoleLogRow } from './brakeman'
import { refLabel } from './brakeman'

/** The names a page knows for a session's or a run's id (refLabel's). */
export interface RoleNames {
  session?: (id: string) => string | undefined
  run?: (id: string) => string | undefined
}

/** The role's states, as internal/crew/role.go names them (RoleStates). */
export const ROLE_STATES = ['idle', 'gathering', 'briefing', 'cooling', 'paused', 'unavailable'] as const

/** The wake sources, as internal/crew/role.go names them (WakeSources), with what the log calls each. */
export const WAKE_SOURCES: Readonly<Record<string, string>> = {
  task_failed: 'A task failed',
  question_open: 'A question waited',
  run_ended: 'A run ended',
  run_paused: 'A run paused',
  ticket_ready: 'A ticket became ready',
  capacity: 'An agent neared its usage limit',
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

/**
 * A row of the role's log in words: its source and subject by name, and what became of it: why it joined no batch, "waiting for its
 * turn" in the batch still gathering, or the turn its batch made, whose request the page links (`request`), never by the batch's id.
 */
export function rowWords(row: RoleLogRow, names: RoleNames = {}): { what: string; fate: string; request?: string } {
  const what = row.subject ? `${sourceWords(row.source)}: ${subjectLabel(row.subject, names)}` : sourceWords(row.source)
  if (row.held) return { what, fate: row.held === 'paused' ? 'not gathered: the role was paused' : `no batch: ${row.held}` }
  if (row.request) return { what, fate: 'taken in a turn', request: row.request }
  if (row.batch) return { what, fate: 'waiting for its turn' }
  return { what, fate: '' }
}

/** A capacity wake's subject: capacity:<agent>/<account>, the key by its ids alone, no reading's time (internal/api, capacityWake). */
const CAPACITY_SUBJECT = /^capacity:([a-z0-9-]{1,32})\/(default|a-[a-z2-7]{12})$/

/** A wake's subject as the page names it: a ref's label, a capacity wake's agent and account, a request's id as it is. */
export function subjectLabel(subject: string, names: RoleNames = {}): string {
  if (/^br-[1-9][0-9]{0,17}$/.test(subject)) return subject
  const cap = CAPACITY_SUBJECT.exec(subject)
  if (cap) return `capacity of ${cap[1]} on ${cap[2] === 'default' ? 'its default login' : cap[2]}`
  return refLabel(subject, names)
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

/** A time of day in the browser's clock, hours and minutes. */
function hm(iso: string): string {
  const t = new Date(iso)
  return Number.isNaN(t.getTime()) ? '' : t.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hour12: false })
}

/**
 * The role in one sentence, its state said once (the Brakeman design review's "In the background"): "Idle: nothing has woken it.",
 * "Gathering 3 wakes: its turn starts at 05:12.", "Briefing now on 3 wakes.", "Cooling until 05:41.", "Paused: nothing wakes it
 * until you resume it.", "Unavailable: <why>." The times are the browser's.
 */
export function roleSentence(r: Pick<BrakemanRole, 'state' | 'why' | 'open' | 'fired' | 'coolUntil'>, windowSeconds?: number): string {
  const wakes = (n: number) => `${n} ${n === 1 ? 'wake' : 'wakes'}`
  switch (r.state) {
    case 'idle':
      return 'Idle: nothing has woken it.'
    case 'gathering': {
      const n = batchCount(r.open)
      if (r.open && windowSeconds) return `Gathering ${wakes(n)}: its turn starts at ${hm(new Date(new Date(r.open.openedAt).getTime() + windowSeconds * 1000).toISOString())}.`
      return `Gathering ${wakes(n)}.`
    }
    case 'briefing': {
      const n = batchCount(r.fired)
      return n ? `Briefing now on ${wakes(n)}.` : 'Briefing now.'
    }
    case 'cooling':
      return r.coolUntil ? `Cooling until ${hm(r.coolUntil)}.` : 'Cooling.'
    case 'paused':
      return 'Paused: nothing wakes it until you resume it.'
    case 'unavailable':
      return r.why ? `Unavailable: ${r.why.replace(/[.\s]+$/, '')}.` : 'Unavailable.'
  }
  return `${r.state.charAt(0).toUpperCase()}${r.state.slice(1)}.`
}

/** The role's state in a few words, for a phone's header: "cooling until 05:41", "gathering 3 wakes", "paused". */
export function roleShort(r: Pick<BrakemanRole, 'state' | 'open' | 'coolUntil'> | null | undefined): string {
  if (!r) return ''
  switch (r.state) {
    case 'gathering': {
      const n = batchCount(r.open)
      return `gathering ${n} ${n === 1 ? 'wake' : 'wakes'}`
    }
    case 'briefing':
      return 'briefing now'
    case 'cooling':
      return r.coolUntil ? `cooling until ${hm(r.coolUntil)}` : 'cooling'
  }
  return r.state
}

/**
 * When the next briefing is: "Next briefing at 07:00." from the server's `nextBriefing` (an instant, in the browser's clock); an older
 * server's configured times as it keeps them, in its own zone ("Next briefing at 07:00 (server time).", "Briefings at 07:00 and 18:30
 * (server time)."); "" for a role paused or unavailable, or with no time.
 */
export function nextBriefingWords(role: Pick<BrakemanRole, 'state'> | null | undefined, nextBriefing: string | undefined, briefings: readonly string[] | undefined, background: boolean): string {
  if (!background || !role || role.state === 'paused' || role.state === 'unavailable') return ''
  if (nextBriefing) {
    const at = hm(nextBriefing)
    return at ? `Next briefing at ${at}.` : ''
  }
  const times = briefings ?? []
  if (!times.length) return ''
  if (times.length === 1) return `Next briefing at ${times[0]} (server time).`
  return `Briefings at ${times.slice(0, -1).join(', ')} and ${times[times.length - 1]} (server time).`
}

/** The most rows of the role's log the page reads: a count that reaches it says "or more". */
export const ROLE_LOG_READ = 200

/**
 * How many times the role was woken today and how many briefs those wakes led to: the log's rows (newest first, as read, at most
 * ROLE_LOG_READ) not held, on the browser's day, and the distinct requests among them the page holds as briefed. "Woken 5 times today, 2
 * briefs." A read that reached its bound inside today says what it holds "or more".
 */
export function wokenWords(log: readonly RoleLogRow[], requests: ReadonlyMap<string, Pick<BrakemanRequest, 'hasBrief'>>, now = new Date(), read = ROLE_LOG_READ): string {
  const today = now.toDateString()
  const rows = log.filter((r) => !r.held && new Date(r.at).toDateString() === today)
  const briefs = new Set(rows.filter((r) => r.request && requests.get(r.request)?.hasBrief).map((r) => r.request))
  // The rows read are the newest: a full read whose oldest row is today's was cut inside today, and what it holds is a floor.
  const oldest = log[log.length - 1]
  const cut = log.length >= read && !!oldest && new Date(oldest.at).toDateString() === today
  const n = cut ? `${rows.length} or more` : String(rows.length)
  const times = n === '1' ? 'once' : `${n} times`
  return `Woken ${times} today, ${briefs.size} ${briefs.size === 1 ? 'brief' : 'briefs'}.`
}
