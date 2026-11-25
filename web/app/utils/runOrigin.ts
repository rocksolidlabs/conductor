import type { RunOrigin } from '~/composables/useSessions'
import { HIDDEN } from './hiddenMarks'

/**
 * The run page's origin line: where a run a registered provider launched
 * came from, from the run's `origin` (GET /api/runs/{run}). "Launched by
 * <label> for <subject> at <start commit>", each part only when the origin
 * has it; a continuation of such a run (`continued`) says it continues
 * one. The provider's words are shown as text: a label or a subject's
 * reference is never markup, and a character that draws nothing (a
 * bidirectional control, a zero-width or other invisible one: the set the
 * task file's quoting replaces) is shown as a mark of its own, never
 * applied. Long parts are cut for the eye
 * with an ellipsis, and the line's title holds them whole. The subject's
 * link is offered only when it is an https link with a host and no
 * userinfo, query or fragment; any other is shown as text. The host a
 * link opens is shown beside it, since its text is the subject's reference,
 * which may name another; the whole address is in the line's words.
 */
export interface RunOriginLine {
  /** The whole line, nothing cut: the element's title and its accessible name. */
  text: string
  /** The first words: "Launched by", or "Continues a run launched by". */
  lead: string
  /** The origin's label, cut for the eye. */
  label: string
  /** The subject's reference, cut for the eye; absent without one. */
  subject?: string
  /** The subject's link when it is one the page may open; absent otherwise. */
  href?: string
  /** The host that link opens, shown beside it; absent without a link. */
  host?: string
  /** The start commit, short; absent without one. */
  commit?: string
}

/** How many characters of a label or a subject the line shows before its ellipsis. */
export const ORIGIN_LABEL_SHOWN = 40
export const ORIGIN_SUBJECT_SHOWN = 48
/** How many hex digits of the start commit the line shows. */
export const ORIGIN_COMMIT_SHOWN = 7

// What draws nothing, as the task file's quoting judges it: the set utils/hiddenMarks.ts holds, which a chat's permission question
// draws too.

/** s as the line shows it: every character that draws nothing a visible mark of its own (U+FFFD), so none reorders or hides the words. */
export function shownText(s: string): string {
  return s.replace(HIDDEN, '\ufffd')
}

/** s cut to n characters (code points), an ellipsis after a cut. */
export function cutForEye(s: string, n: number): string {
  const cps = Array.from(s)
  return cps.length > n ? `${cps.slice(0, n - 1).join('')}…` : s
}

/** url when the page may open it: https, with a host, and no userinfo, query or fragment; undefined for any other. */
export function subjectHref(url: string | undefined): string | undefined {
  if (!url || !url.startsWith('https://') || /[?#\s\\]/.test(url) || url.length > 2048) return undefined
  let u: URL
  try {
    u = new URL(url)
  } catch {
    return undefined
  }
  if (u.protocol !== 'https:' || !u.hostname || u.username || u.password || u.search || u.hash) return undefined
  // The host the link was written with is the one the browser would open: a parser's repair (https:///x, a backslash) is no link.
  const written = url.slice('https://'.length).split('/')[0]!
  if (written.toLowerCase() !== u.host) return undefined
  return url
}

/** The origin line of a run, or null for a run no provider launched. */
export function runOriginLine(o: RunOrigin | undefined | null): RunOriginLine | null {
  if (!o || !o.label) return null
  const lead = o.continued ? 'Continues a run launched by' : 'Launched by'
  const label = shownText(o.label)
  const subject = o.subject ? shownText(o.subject) : undefined
  const commit = o.startCommit ? o.startCommit.slice(0, ORIGIN_COMMIT_SHOWN) : undefined
  const href = subject ? subjectHref(o.subjectUrl) : undefined
  const host = href ? new URL(href).host : undefined
  const text = `${lead} ${label}${subject ? ` for ${subject}` : ''}${href ? ` (its link opens ${href})` : ''}${o.startCommit ? ` at ${o.startCommit}` : ''}`
  return {
    text,
    lead,
    label: cutForEye(label, ORIGIN_LABEL_SHOWN),
    subject: subject ? cutForEye(subject, ORIGIN_SUBJECT_SHOWN) : undefined,
    href,
    host,
    commit,
  }
}

/** The words where a run's time limit has passed and an action would be refused for it. */
export const TIME_LIMIT_PASSED = "The run's time limit has passed: it takes no continuation, retry or request for changes"

/** Whether the run's launch's time limit has passed at now (ms): its origin's deadline is at or before it; false with none. */
export function timeLimitPassed(o: RunOrigin | undefined | null, now: number): boolean {
  if (!o?.deadline) return false
  const at = Date.parse(o.deadline)
  return Number.isFinite(at) && at <= now
}
