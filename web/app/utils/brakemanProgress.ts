// A Brakeman turn's progress in words (docs/protocol.md, Brakeman, the brakeman_progress event and GET /api/brakeman's `progress`):
// the reads used of the turn's budget, the time it has run of its deadline, and what it read last. Pure: no Nuxt, no fetch.

import { refLabel, type BrakemanProgress, type BrakemanReadingNow } from './brakeman'
import { relativeTime } from './sessions'

/** What the line needs of the request it is drawn for: the reads and the budget as last read, and the deadline. */
export interface ProgressOf {
  id: string
  state: string
  reads: number
  deadline?: string
  config?: { budget?: { readsPerTurn?: number } }
}

/**
 * The words for a read that named no record, by what it read. The progress a page holds names the read the turn made LAST (it is
 * kept until the next read or the close), so the words say what was read, not what is being read.
 */
export const READ_WORDS: Readonly<Record<string, string>> = {
  changes: 'Read what changed',
  status: 'Read the status',
  overlaps: 'Read overlapping changes',
  briefs: 'Read earlier briefs',
  search: 'Searched',
  runs: 'Read the runs',
  projects: "Read Dispatch's projects",
  tickets: 'Read the tickets',
  sessions: 'Read the sessions',
  touched: 'Read the touched files',
  capacity: 'Read capacity',
}

/** What a turn starting says, before its first read. */
export const STARTING_WORDS = 'Starting its agent'

type Names = Parameters<typeof refLabel>[1]

/**
 * What the turn read last, in words: "Last read: check tests of build, attempt 1" for a read of one record, the words by what it read
 * for a listing ("Read what changed"), and "Starting its agent" while it has read nothing. A turn that has read but whose last read
 * the page does not hold (an older server's state, a read-only server's) says nothing of it: "".
 */
export function readingWords(reading: BrakemanReadingNow | undefined, reads: number, names?: Names): string {
  if (reading?.ref) return `Last read: ${refLabel(reading.ref, names)}`
  if (reading) return READ_WORDS[reading.what] ?? 'Read'
  return reads > 0 ? '' : STARTING_WORDS
}

/**
 * The reads used and the turn's budget of them: the progress the server last told, else the request's own count against the budget it
 * was asked under. The budget is 0 when neither says it.
 */
export function readCounts(request: ProgressOf, progress?: BrakemanProgress): { used: number; of: number } {
  if (progress?.id === request.id) return { used: Math.max(progress.reads, request.reads), of: progress.readsPerTurn }
  return { used: request.reads, of: request.config?.budget?.readsPerTurn ?? 0 }
}

/** "41 of 300 reads", "1 of 300 reads". */
export function readCountWords(c: { used: number; of: number }): string {
  return c.of > 0 ? `${c.used} of ${c.of} reads` : `${c.used} ${c.used === 1 ? 'read' : 'reads'}`
}

/** "41 of 300 reads used": the meter's name for a reader that does not see it. */
export function readsLabel(c: { used: number; of: number }): string {
  return `${readCountWords(c)} used`
}

/** The meter's fill, 0 to 100. */
export function readsPercent(c: { used: number; of: number }): number {
  return c.of > 0 ? Math.min(100, Math.max(0, (100 * c.used) / c.of)) : 0
}

/** "05:23", the time of day in the browser's zone. */
export function clockOf(at: number, timeZone?: string): string {
  return new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit', hourCycle: 'h23', timeZone }).format(new Date(at))
}

/**
 * The time the turn has run against its deadline, on the app's one clock: "3m of 15m" with the turn's start known, "until 05:23"
 * with only its deadline, and "" with neither. Ages are the app's own words (relativeTime).
 */
export function timeWords(startedAt: string | undefined, deadline: string | undefined, now: number, timeZone?: string): string {
  const end = Date.parse(deadline ?? '')
  const start = Date.parse(startedAt ?? '')
  if (!Number.isNaN(start) && !Number.isNaN(end) && end > start) return `${relativeTime(startedAt!, Math.min(now, end))} of ${relativeTime(startedAt!, end)}`
  if (!Number.isNaN(end)) return `until ${clockOf(end, timeZone)}`
  return ''
}
