import type { GitCheck, PathGit } from '~/composables/useSessions'

/** How long the picker waits after a keystroke before asking the server. */
export const DIR_DEBOUNCE_MS = 150

/**
 * A folder chosen (from the field's list, or the folder picker) leaves the field holding the focus with the caret at the end of
 * the path and the field scrolled to its end, so that the folder just chosen, at the end of a long path, shows, and typing goes on
 * from there (#167). A field without the focus shows its value from the start; a bare focus() leaves the caret where it was.
 */
export function caretToEnd(input: Pick<HTMLInputElement, 'focus' | 'setSelectionRange' | 'value'> & { scrollLeft: number; scrollWidth: number }, frames = 12): void {
  const place = () => {
    const end = input.value.length
    input.setSelectionRange(end, end)
    input.scrollLeft = input.scrollWidth
  }
  input.focus()
  place()
  // The field's text and width can still change over the next frames (the pick landing in the field a tick or two later, the
  // loading mark giving way to the list's chevron): for a short while the view is placed again each frame, the caret moved only
  // when the text changed, and no longer once the focus has gone elsewhere.
  if (typeof requestAnimationFrame !== 'function' || typeof document === 'undefined') return
  let last = input.value
  let n = 0
  const again = () => {
    if ((document.activeElement as unknown) !== input) return
    if (input.value !== last) {
      last = input.value
      place()
    } else {
      input.scrollLeft = input.scrollWidth
    }
    if (++n < frames) requestAnimationFrame(again)
  }
  requestAnimationFrame(again)
}

/**
 * What is typed with a leading `~/` read as the server's home (whoami's `home`), as a shell reads it: `~/code` is `<home>/code`. The
 * text as it is when it has no such start or the home is not known; a `~` elsewhere, or `~user`, is left alone.
 */
export function homeExpanded(text: string, home: string): string {
  if (!home || !text.startsWith('~/')) return text
  return `${home.replace(/\/+$/, '')}${text.slice(1)}`
}

/** The listing route's words for a prefix under none of the folders agents may use (internal/api/paths.go errNoAllowedDir). */
const NO_ALLOWED_DIR = 'no directory in the prefix is under the allowed roots'

/** At most this many of the folders agents may use are named in the picker's sentence; the rest are counted. */
export const ROOTS_NAMED = 3

/**
 * What the folder picker, or a folder list with no line under its field, says of a listing refused, in plain words: for a place
 * outside the folders agents may use, those folders (the route names them after its own words; at most ROOTS_NAMED, the rest
 * counted); for a server that did not answer, that; for anything else, that the place cannot be listed. Never the route's own text.
 */
export function listingProblem(code: string, message: string): string {
  if (code === 'invalid_cwd' && message.startsWith(NO_ALLOWED_DIR)) {
    const roots = message.slice(NO_ALLOWED_DIR.length).replace(/^:\s*/, '').trim()
    if (!roots) return "Agents can't use a folder here. Pick another."
    const all = roots.split(', ')
    const named = all.length > ROOTS_NAMED ? `${all.slice(0, ROOTS_NAMED).join(', ')} and ${all.length - ROOTS_NAMED} more` : all.join(', ')
    return `Agents can use only the folders inside ${named}. Pick one there.`
  }
  if (code === 'network') return 'Conductor did not answer. Type on to try again.'
  return "This place can't be listed. Pick another."
}

/** The prefix sent for what is typed: the text trimmed; empty means the server's default directory. */
export function dirQuery(text: string): string {
  return text.trim()
}

/**
 * The entries still shown while the listing of `text` is on its way: those
 * the listing could hold, directories directly under what is typed (none for
 * an empty text, the server's default listed anew). An entry the text has
 * moved past, or moved back above (shortened across a slash), can never be
 * highlighted, so Enter cannot put it in place of what was typed.
 */
export function matchingEntries<T extends { path: string }>(entries: T[], text: string): T[] {
  const q = dirQuery(text)
  return q ? entries.filter((e) => e.path.startsWith(q) && !e.path.slice(q.length).includes('/')) : []
}

/** Whether a key in the field moves the list's highlight (the list takes the arrows, Home and End from the field). */
export function movesHighlight(key: string): boolean {
  return key === 'ArrowDown' || key === 'ArrowUp' || key === 'Home' || key === 'End'
}

/**
 * Whether Enter in the field keeps the text instead of letting the list pick
 * the highlighted entry. The list highlights its first entry after every
 * keystroke, so Enter on a complete path would put its first child in its
 * place: Enter picks only once the highlight was moved (`moved`: the arrows,
 * or the pointer over an entry, since the text last changed). With the list
 * closed or nothing highlighted, Enter is the form's; a modified Enter, or
 * one that ends a composition, the list leaves alone, and so does this.
 */
export function enterKeepsText(
  e: { isComposing?: boolean; ctrlKey?: boolean; metaKey?: boolean; altKey?: boolean },
  s: { open: boolean; highlighted: boolean; moved: boolean },
): boolean {
  if (e.isComposing || e.ctrlKey || e.metaKey || e.altKey) return false
  return s.open && s.highlighted && !s.moved
}

/** The mark beside a listed directory: whether a crew with worktrees could use it. */
export function gitMark(git: PathGit): { label: string; tone: 'success' | 'warning' | 'neutral' } {
  if (git.repo && git.commits) return { label: 'git', tone: 'success' }
  if (git.repo) return { label: 'git, no commit', tone: 'warning' }
  return { label: '', tone: 'neutral' }
}

/** A git check, or the failure of one (`error`: the message is the request's error). */
export type GitCheckView = GitCheck & { error?: boolean }

/**
 * The git check's message when a crew with worktrees can launch: the
 * server's msgCanWorktree (internal/crew/worktree.go, whose test reads this
 * line). Any other message is the launch's refusal, whatever inRepo and
 * hasCommit say (a .conductor that is a symbolic link is refused in a
 * repository with a commit).
 */
export const GIT_CAN_WORKTREE = 'a git repository with a commit: a crew with worktrees can launch here'

/**
 * The git check's message for a new project, a folder with no commit yet or no repository yet: the server's msgFirstCommit. The
 * operator's launch makes its first commit (internal/crew/firstcommit.go), so it is no refusal, and the line says nothing.
 */
export const GIT_FIRST_COMMIT = "no commit yet: the operator's launch makes the folder's first commit"

/**
 * What the crew editor says under the working directory. With worktrees the
 * line is the server's verdict in its words and explains a launch it would
 * refuse (`blocks`, for the launch button's tooltip); without a message (an
 * older server) inRepo and hasCommit decide. Without worktrees it only
 * informs. The launch's answer stays the authority: the button is never
 * disabled by this.
 */
export function gitCheckLine(check: GitCheckView | null, isolation: 'none' | 'worktree'): { text: string; tone: 'success' | 'warning' | 'neutral'; blocks: boolean } {
  if (!check) return { text: '', tone: 'neutral', blocks: false }
  if (check.error) return { text: check.message, tone: 'warning', blocks: false }
  if (check.message === GIT_FIRST_COMMIT) return { text: '', tone: 'neutral', blocks: false }
  if (isolation === 'worktree') {
    const canLaunch = check.message ? check.message === GIT_CAN_WORKTREE : check.inRepo && check.hasCommit
    if (canLaunch) return { text: check.message || 'A git repository with a commit: worktrees can be made.', tone: 'success', blocks: false }
    return { text: `${check.message || 'Not a git repository with a commit'}. Launch would be refused.`, tone: 'warning', blocks: true }
  }
  if (check.inRepo) return { text: check.toplevel ? `Git repository at ${check.toplevel}.` : 'In a git repository.', tone: 'neutral', blocks: false }
  return { text: 'Not a git repository; fine with a shared working directory.', tone: 'neutral', blocks: false }
}

/**
 * What a picker says under a listing that is not whole (round 14: a cut list looked whole, its faint note missed): how many folders
 * matching what was typed were left out and that typing narrows it; when only git marks were cut short, that some are unmarked; null
 * when the listing is whole.
 */
export function moreNote(r: { truncated: boolean; more?: number; moreUnknown?: boolean }): string | null {
  if (!r.truncated) return null
  const n = r.more ?? 0
  if (n > 0) return `${n}${r.moreUnknown ? '+' : ''} more ${n === 1 && !r.moreUnknown ? 'folder' : 'folders'} here: type the start of a name to narrow the list.`
  if (r.moreUnknown) return 'More folders here than were read: type the start of a name to narrow the list.'
  return 'Some folders are not marked: git did not answer in time.'
}
