/**
 * What a page shows of what it read (the UX review's P23, invariant 5: one shape for nothing): `reading` before its first answer,
 * `failed` when that first read failed and nothing was ever read, `empty` when an answer said there is nothing, `content` whenever
 * something is shown. Content wins over an error beside it: the page keeps what it has, with its alert. Unknown is not zero: `empty`
 * needs a read.
 */
export type PageState = 'reading' | 'failed' | 'empty' | 'content'

export interface PageRead {
  /** Whether an answer has been read (a snapshot of the sessions, a page of a list). */
  read: boolean
  /** The last read's failure, '' or undefined when none. */
  error?: string | null
  /** How many of the page's things are shown. */
  count: number
}

export function pageState({ read, error, count }: PageRead): PageState {
  if (count > 0) return 'content'
  if (read) return 'empty'
  return error ? 'failed' : 'reading'
}
