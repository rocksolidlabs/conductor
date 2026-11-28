/**
 * Allow this folder (UX3 R30): what the line under a folder field says of the folder named, from the server's check of it
 * (GET /api/roots/check), what this app can do about it (the desktop app's bridge, whoami's `folders`) and the press's answer. One
 * function makes every word, its tone and the one button it offers, so the line reads the same under every folder field.
 *
 * Only the desktop app may allow a folder: the button shows only where its bridge offers the press and its server holds the app's key
 * (whoami `folders: app`), and only for a folder the check says is under no folder agents may use and not refused. Every other case
 * says the fact and the way on, with no button. A state or a reason this page does not know is never taken as allowed.
 */

/** GET /api/roots/check's answer. */
export interface RootCheck {
  path: string
  real?: string
  state: string
  link?: boolean
  refused?: string
  wide?: string
}

/** The press's answer, as the desktop app's bridge gives it (desktop/src/allow.ts AllowAnswer). */
export type AllowAnswer =
  | { ok: true; root: string; saved: boolean; error?: string }
  | { ok: false; code: string; message: string; real?: string; wide?: string; reason?: string }

/** The kinds of wide folder the press confirms. */
export const WIDE_KINDS = ['home', 'above_home', 'holds_data'] as const
export type WideKind = (typeof WIDE_KINDS)[number]

/** What this app can do about a folder agents cannot use yet. */
export type AllowWay =
  /** The desktop app's bridge offers the press, and its server holds the app's key. */
  | 'press'
  /** The desktop app, whose bridge has no press (an older app, or Windows while its switch is off). */
  | 'settings'
  /** A server the desktop app started, seen from elsewhere (a tab, another computer). */
  | 'app'
  /** A server whose folders its configuration sets. */
  | 'config'

/** allowWay is the way on from the app's bridge and the server's own word (whoami `folders`). */
export function allowWay(o: { desktop: boolean; bridgePress: boolean; folders: string }): AllowWay {
  if (o.folders === 'app') return o.desktop && o.bridgePress ? 'press' : o.desktop ? 'settings' : 'app'
  return o.desktop ? 'settings' : 'config'
}

/** The line's button: the press, the wide folder's question, or the link's folder put in the field. */
export type LineButton = 'allow' | 'wide' | 'use' | ''

/** The line under the field: its words, its tone, its button, and the folder it names (in the code font, as evidence). */
export interface FolderLine {
  /** The state of the line, the spec's mark (data-folder-state). */
  state: string
  /** The words before the folder named, and after it. */
  words: string
  after?: string
  /** The folder named in the words (a link's folder, a wide folder's real path), in the code font. */
  folder?: string
  tone: 'error' | 'warning' | 'success'
  button: LineButton
  /** For the wide question: the kind the second press confirms. */
  wide?: WideKind
  /** A link to Settings' folders follows the words. */
  settings?: boolean
  /** Whether the folder can be launched in as it stands. */
  blocks: boolean
}

const CANT = "Agents can't use this folder."
/** The way on from a refusal this page does not know: never taken as allowed. */
const CANT_PICK = `${CANT} Pick another.`

/** The words of the way on for a folder under no folder agents may use, where this app cannot press. */
function noPress(way: AllowWay): Pick<FolderLine, 'words' | 'settings' | 'after'> {
  switch (way) {
    case 'settings':
      return { words: `${CANT} Add it to Folders agents can use in`, settings: true, after: ', or pick another.' }
    case 'app':
      return { words: `${CANT} Allow it in the Conductor app on the computer that runs it.` }
    default:
      return { words: `${CANT} Add it to this server's folders agents can use, or pick another.` }
  }
}

/**
 * The words for a folder under none agents may use where no line is under Folder to say it (a server too old to check a folder): in
 * the app that presses, that agents can't use it yet; anywhere else, the way on where this app cannot press. As the line says them.
 */
export function outsideWords(way: AllowWay): Pick<FolderLine, 'words' | 'settings' | 'after'> {
  return way === 'press' ? { words: "Agents can't use this folder yet." } : noPress(way)
}

/** The words for a folder refused for a cause this page does not know: never the server's own. */
export const FOLDER_CANT = CANT_PICK

/** The longest folder the app allows (desktop/src/allow.ts MAX_ROOT_PATH): one longer gets no press. */
export const MAX_ALLOW_PATH = 1024
const TOO_LONG = "Agents can't use this folder: its path is too long to allow. Pick another."

/** The words for a folder the server never allows from the app's screens, by its reason; an unknown reason says only the fact. */
export function refusedWords(reason: string | undefined): string {
  switch (reason) {
    case 'whole_disk':
      return "Agents can't use the whole disk. Pick a folder inside it."
    case 'conductor_data':
      return "Agents can't use this folder: Conductor's data or an account's login is in it. Pick another."
    case 'name':
      return "Agents can't use this folder: a name in its path has a comma, a hidden character or a space at an end. Rename that folder, or pick another."
    case 'owner':
      return "Agents can't use this folder: another user owns it. Pick one of yours."
    default:
      return CANT_PICK
  }
}

/** The wide folder's question, in the app's words for its kind, naming the folder, before the second press confirms it. */
export function wideQuestion(kind: WideKind, real: string): Pick<FolderLine, 'words' | 'after' | 'folder'> {
  switch (kind) {
    case 'home':
      return { words: 'Allow your whole home folder,', folder: real, after: '? Agents could read and change everything in it.' }
    case 'above_home':
      return { words: 'Allow all of', folder: real, after: '? Your home folder is inside it, and agents could read and change everything there.' }
    case 'holds_data':
      return { words: 'Allow all of', folder: real, after: "? Conductor's data or an account's login is inside it, and agents could read it." }
  }
}

const isWide = (k: string | undefined): k is WideKind => !!k && (WIDE_KINDS as readonly string[]).includes(k)

/** The folder a press allows: the one the server named at the check, its real path where it gave one, never the text read again. */
export function pressTarget(c: RootCheck): string {
  return c.real || c.path
}

/**
 * The line for a check's answer of `typed` (the text as the field holds it): null when the folder can be used (nothing to say), or
 * while the field has the focus and the folder does not exist yet (a path still being typed). The press's line names the folder it
 * allows whenever that is not, letter for letter, what was typed (a relative text, a `..`, another spelling).
 */
export function checkLine(c: RootCheck, way: AllowWay, focused = false, typed = c.path): FolderLine | null {
  const blocked = (words: string, extra: Partial<FolderLine> = {}): FolderLine => ({ state: c.state, words, tone: 'error', button: '', blocks: true, ...extra })
  switch (c.state) {
    case 'allowed':
      return null
    case 'missing':
      return focused ? null : blocked('There is no folder at this path.')
    case 'not_directory':
      return blocked('This is a file, not a folder.')
    case 'unreadable':
      return blocked("Conductor can't open this folder. Pick another.")
    case 'outside': {
      if (c.refused) return blocked(refusedWords(c.refused), { state: 'refused' })
      if (c.link && c.real && c.real !== c.path) {
        return blocked('This goes through a link to', { state: 'link', folder: c.real, after: ". Agents can't use that folder yet.", button: 'use' })
      }
      if (c.wide && !isWide(c.wide)) return blocked(CANT_PICK, { state: 'refused' })
      if (way !== 'press') return blocked('', { state: 'outside', ...noPress(way) })
      const target = pressTarget(c)
      if (target.length > MAX_ALLOW_PATH) return blocked(TOO_LONG, { state: 'refused' })
      const wide = isWide(c.wide) ? c.wide : undefined
      if (target !== typed) return blocked("Agents can't use", { state: 'outside', folder: target, after: ' yet.', button: 'allow', wide })
      return blocked("Agents can't use this folder yet.", { state: 'outside', button: 'allow', wide })
    }
    default:
      // A state this page does not know is never taken as allowed.
      return blocked(CANT_PICK, { state: 'unknown' })
  }
}

/**
 * The line once the press answered: what it became of the folder, and the way on from a refusal. An answer that keeps the press names
 * the folder it allows whenever that is not the text as typed, as the line before it did. No raw text of the app or the server.
 */
export function answerLine(a: AllowAnswer, c: RootCheck, typed = c.path): FolderLine {
  const blocked = (state: string, words: string, extra: Partial<FolderLine> = {}): FolderLine => ({ state, words, tone: 'error', button: '', blocks: true, ...extra })
  const target = pressTarget(c)
  /** An answer that keeps the press: its reason, after the folder's name where the folder is not the text. */
  const again = (reason: string): FolderLine =>
    target !== typed ? blocked('outside', "Agents can't use", { folder: target, after: ` yet. ${reason}`, button: 'allow' }) : blocked('outside', reason, { button: 'allow' })
  if (a.ok) {
    if (!a.saved) return { state: 'allowed_unsaved', words: 'Agents can use this folder until Conductor restarts. Add it in', settings: true, after: ' to keep it.', tone: 'warning', button: '', blocks: false }
    if (a.error) return { state: 'allowed_restarted', words: "Conductor keeps this folder. If agents can't use it once Conductor runs, restart Conductor.", tone: 'warning', button: '', blocks: false }
    return { state: 'allowed_now', words: 'Agents can use this folder now.', tone: 'success', button: '', blocks: false }
  }
  switch (a.code) {
    case 'folder_is_link':
      return a.real ? blocked('link', 'This goes through a link to', { folder: a.real, after: ". Agents can't use that folder yet.", button: 'use' }) : blocked('refused', CANT_PICK)
    case 'folder_wide':
      return isWide(a.wide) ? { state: 'wide', ...wideQuestion(a.wide, target), tone: 'warning', button: 'wide', wide: a.wide, blocks: true } : blocked('refused', CANT_PICK)
    case 'folder_refused':
      return blocked('refused', refusedWords(a.reason))
    case 'folder_missing':
      return blocked('missing', 'There is no folder at this path.')
    case 'folder_not_directory':
      return blocked('not_directory', 'This is a file, not a folder.')
    case 'folder_unreadable':
      return blocked('unreadable', "Conductor can't open this folder. Pick another.")
    case 'busy':
      // A Settings save or a restart under way: the dialog keeps what was typed, and the press can be made again.
      return again('Conductor is busy. Press Allow this folder again in a moment.')
    case 'no_answer':
      return again('Conductor did not answer. Press Allow this folder again in a moment.')
    case 'folder_changed':
      return again('The folder changed as it was allowed. Press Allow this folder again.')
    case 'rate_limited':
      return blocked('outside', 'Conductor is refusing requests for now. Try again later.')
    case 'too_many_roots':
      return blocked('outside', 'Agents can use no more folders. Remove one in', { settings: true, after: ', or pick a folder agents can use.' })
    case 'invalid_request':
      return blocked('refused', target.length > MAX_ALLOW_PATH ? TOO_LONG : CANT_PICK)
    case 'not_supported':
    case 'not_the_app':
      return blocked('outside', '', noPress('settings'))
    default:
      // A refusal this page does not know offers nothing that could not help.
      return blocked('outside', 'Conductor did not allow this folder. Add it in', { settings: true, after: ', or pick another.' })
  }
}
