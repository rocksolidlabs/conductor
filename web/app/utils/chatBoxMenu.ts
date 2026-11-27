/**
 * The chat box's list (UX3 R6, R11; docs/plans/chat-or-terminal.md, row 12): what a word at the cursor asks for, what a pick writes,
 * and which names a pick may write. Pure: ChatComposer and ChatBoxMenu draw from it, and its tests are the rules.
 */

/** The words after `@` a find takes at most, in bytes (the `find` op's own bound). */
export const MENTION_WORDS_MAX = 200
/** A path a pick may write, at most, in bytes. */
export const MENTION_PATH_MAX = 1024
/** The rows the list draws at most. */
export const MENU_ROWS = 8

const bytes = (s: string) => new TextEncoder().encode(s).length

/** The `@` word the cursor is in: where it starts (its `@`), where it ends, and the words typed after the `@` up to the cursor. */
export interface Mention {
  start: number
  end: number
  words: string
}

/**
 * The `@` word at the cursor: a word that begins with `@` at the text's start or after white space, the cursor inside it or at its
 * end; never inside a word (an address, a@b), never words past MENTION_WORDS_MAX bytes.
 */
export function mentionAt(text: string, caret: number): Mention | null {
  if (caret < 0 || caret > text.length) return null
  let start = caret
  while (start > 0 && !/\s/.test(text[start - 1]!)) start--
  if (text[start] !== '@') return null
  let end = caret
  while (end < text.length && !/\s/.test(text[end]!)) end++
  const words = text.slice(start + 1, caret)
  if (words.includes('@') || bytes(words) > MENTION_WORDS_MAX) return null
  return { start, end, words }
}

/** The text with the `@` word replaced by `@<path> `, and where the cursor goes: after the space. */
export function pickMention(text: string, m: Mention, path: string): { text: string; caret: number } {
  const put = `@${path} `
  const rest = text.slice(m.end).replace(/^\s/, '')
  return { text: text.slice(0, m.start) + put + rest, caret: m.start + put.length }
}

/**
 * Whether a path may be written by a pick: letters, digits and `_ . + = , % / -`; not beginning with `-` or `/`; no `..` part; at
 * most MENTION_PATH_MAX bytes. Any other (a space, a quote, a mark a person cannot see) is listed and typed by the person.
 */
export function safePath(path: string): boolean {
  if (!path || bytes(path) > MENTION_PATH_MAX) return false
  if (!/^[\p{L}\p{N}_.+=,%/-]+$/u.test(path)) return false
  if (path.startsWith('-') || path.startsWith('/')) return false
  return !path.split('/').includes('..')
}

/** Whether the line begins with `/` (its first character, after white space). */
export function startsWithSlash(text: string): boolean {
  return text.trimStart().startsWith('/')
}

/** A file the agent touched (absolute), as a pick writes it: its path from the folder; none for one outside it. */
export function fromFolder(folder: string | undefined, path: string): string | null {
  if (!folder) return null
  const top = folder.endsWith('/') ? folder : `${folder}/`
  return path.startsWith(top) && path.length > top.length ? path.slice(top.length) : null
}

/** One row of the list: what it writes (a path, or a command's name) and whether a pick may write it. */
export interface MenuRow {
  key: string
  value: string
  pickable: boolean
  hint?: string
  words?: string
}

/** The rows of the file list: the paths as the read gave them, MENU_ROWS at most, each with whether it may be picked. */
export function fileRows(paths: readonly string[]): MenuRow[] {
  const seen = new Set<string>()
  const rows: MenuRow[] = []
  for (const p of paths) {
    if (seen.has(p)) continue
    seen.add(p)
    rows.push({ key: p, value: p, pickable: safePath(p) })
    if (rows.length === MENU_ROWS) break
  }
  return rows
}

/**
 * The first `@` word of a line, at a word's start, that names a file outside the session's folder (UX3 R10): one that begins with `/`
 * or `~`, or holds a `..` part. '' for none.
 */
export function outsideMention(text: string): string {
  for (const m of text.matchAll(/(?:^|\s)@(\S+)/g)) {
    const p = m[1]!
    if (p.startsWith('/') || p.startsWith('~') || p.split('/').includes('..')) return p
  }
  return ''
}

/** The `/` word a line begins with, when the cursor is in it (UX3 R11): what is typed after the slash, and where the word ends. */
export interface CommandWord {
  end: number
  typed: string
}

/** The first word of a line that begins with `/`, the cursor inside it or at its end; null anywhere else. */
export function commandAt(text: string, caret: number): CommandWord | null {
  if (!text.startsWith('/')) return null
  const m = /^\/\S*/.exec(text)!
  const end = m[0].length
  if (caret < 1 || caret > end) return null
  return { end, typed: text.slice(1, caret) }
}

/** A name the list may offer: the shape of a command's name (proto.CommandNameShape), checked again on the page. */
const NAME = /^[A-Za-z0-9_][A-Za-z0-9_.:-]*$/

/**
 * The rows of the list for `/`: the agent's own commands as the server sent them, names that start with what is typed, then names
 * that hold it, then those an alias of which matches; MENU_ROWS at most; a name of another shape dropped.
 */
export function commandRows(list: readonly { name: string; words?: string; hint?: string; aliases?: string[] }[], typed: string): MenuRow[] {
  const t = typed.toLowerCase()
  const ok = list.filter((c) => NAME.test(c.name))
  const starts = ok.filter((c) => c.name.toLowerCase().startsWith(t))
  const holds = ok.filter((c) => !starts.includes(c) && c.name.toLowerCase().includes(t))
  const alias = ok.filter((c) => !starts.includes(c) && !holds.includes(c) && (c.aliases ?? []).some((a) => NAME.test(a) && a.toLowerCase().includes(t)))
  return [...starts, ...holds, ...alias].slice(0, MENU_ROWS).map((c) => ({ key: c.name, value: c.name, pickable: true, hint: c.hint, words: c.words }))
}

/** Whether what is typed after the slash is already one of the listed names, whole: Enter then sends at once. */
export function wholeCommand(list: readonly { name: string }[], typed: string): boolean {
  return !!typed && list.some((c) => c.name === typed && NAME.test(c.name))
}

/** The line with its first word completed to `/name ` and where the cursor goes: after the space. */
export function pickCommand(text: string, w: CommandWord, name: string): { text: string; caret: number } {
  const put = `/${name} `
  const rest = text.slice(w.end).replace(/^\s/, '')
  return { text: put + rest, caret: put.length }
}
