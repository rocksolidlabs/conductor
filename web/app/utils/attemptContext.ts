/**
 * The context an attempt was given (docs/plans/context-seams.md, CT-3): each block a context provider gave it, at its start or by a
 * lookup while it worked, as the task route lists it (never the text, which the body route reads), and the words the run page and a
 * ticket's candidates say of it.
 */

/** One block of context an attempt was given, as GET /api/runs/{run}/tasks/{task} lists it per attempt. */
export interface AttemptContextRow {
  seq: number
  /** `task`: given at the attempt's start; `lookup`: a lookup's pack, while it worked. */
  phase: 'task' | 'lookup'
  /** The context provider that gave it. */
  source: string
  title: string
  /** What it is about (a path, a symbol), when the provider said. */
  ref?: string
  /** The provider's pack mark, when it gave one. */
  pack?: string
  /** The SHA-256 of its text, and the text's size. */
  sha256: string
  bytes: number
  at: string
}

/** A lookup's reply (POST /api/sessions/{id}/context/lookup), as far as a late pack goes: its blocks, given only once recorded. */
export interface LookupReply {
  source: string
  blocks?: { title: string; ref?: string; body: string; pack?: string }[]
  /** The server's words when a late pack could not be recorded, and so is not given. */
  withheld?: string
}

/** A block's phase in words. */
export function phaseWords(phase: AttemptContextRow['phase']): string {
  return phase === 'lookup' ? 'looked up' : 'at start'
}

/** The list in one line: "Context: 3 at start, 2 looked up"; empty for none. */
export function contextSummary(rows: readonly AttemptContextRow[] | undefined): string {
  if (!rows?.length) return ''
  const start = rows.filter((r) => r.phase !== 'lookup').length
  const looked = rows.length - start
  const parts = [start ? `${start} at start` : '', looked ? `${looked} looked up` : ''].filter(Boolean)
  return `Context: ${parts.join(', ')}`
}

/** A candidate's count: "context: 1 block", "context: 5 blocks". */
export function contextCountWords(n: number): string {
  return `context: ${n} ${n === 1 ? 'block' : 'blocks'}`
}

/** A digest shortened to its first 12 characters. */
export function shortDigest(sha256: string): string {
  return sha256.slice(0, 12)
}

/** A text's size in words: 512 B, 3.2 KiB. */
export function contextSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  return `${Math.round((bytes / 1024) * 10) / 10} KiB`
}

/** The body route of a block: its text, at most 8 KiB, its SHA-256 in `X-Conductor-Context-SHA256`. */
export function contextBodyPath(run: string, task: string, attempt: number, seq: number): string {
  return `/api/runs/${encodeURIComponent(run)}/tasks/${encodeURIComponent(task)}/attempts/${attempt}/context/${seq}`
}

/** The header that carries a block's SHA-256. */
export const CONTEXT_SHA_HEADER = 'X-Conductor-Context-SHA256'

/** What a read of a block's text failed with, in words. */
export function contextReadError(e: unknown): string {
  const err = e as { status?: number; message?: string }
  if (err?.status === 404) return 'This text is not kept: the data directory has no copy whose digest is the one recorded.'
  return err?.message || 'The text could not be read.'
}
