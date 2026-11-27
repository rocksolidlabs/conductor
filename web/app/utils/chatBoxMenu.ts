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

/** A file a line names with `@` (UX3 R10): where its mention lies in the line, and the path it names. */
export interface NamedFile {
  start: number
  end: number
  path: string
}

/** What a bare mention's end does not hold: the punctuation a sentence puts after a word. */
const MENTION_TRAIL = /[.,;:!?)\]}>'"]+$/

/**
 * The files a line names, as the agent reads them: `@` at a word's start, then a path in double quotes (one mention, its spaces and
 * all) or a word, the punctuation after a bare word left out (`@notes.txt,` names notes.txt).
 */
export function mentionsIn(text: string): NamedFile[] {
  const out: NamedFile[] = []
  for (const m of text.matchAll(/(^|\s)@("[^"\n]*"|\S+)/g)) {
    const start = m.index! + m[1]!.length
    let body = m[2]!
    let path: string
    if (body.length >= 2 && body.startsWith('"') && body.endsWith('"')) path = body.slice(1, -1)
    else {
      body = body.replace(MENTION_TRAIL, '')
      path = body
    }
    if (path) out.push({ start, end: start + 1 + body.length, path })
  }
  return out
}

/**
 * The first file a line names that lies outside the session's folder (UX3 R10): a path that begins with `/` or `~`, or holds a `..`
 * part, quoted or not. '' for none.
 */
export function outsideMention(text: string): string {
  for (const f of mentionsIn(text)) {
    if (f.path.startsWith('/') || f.path.startsWith('~') || f.path.split('/').includes('..')) return f.path
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
 * that hold it, then those an alias of which matches; every match (the list scrolls); a name of another shape dropped.
 */
export function commandRows(list: readonly { name: string; words?: string; hint?: string; aliases?: string[] }[], typed: string): MenuRow[] {
  const t = typed.toLowerCase()
  const ok = list.filter((c) => NAME.test(c.name))
  const starts = ok.filter((c) => c.name.toLowerCase().startsWith(t))
  const holds = ok.filter((c) => !starts.includes(c) && c.name.toLowerCase().includes(t))
  const alias = ok.filter((c) => !starts.includes(c) && !holds.includes(c) && (c.aliases ?? []).some((a) => NAME.test(a) && a.toLowerCase().includes(t)))
  return [...starts, ...holds, ...alias].map((c) => ({ key: c.name, value: c.name, pickable: true, hint: c.hint, words: c.words }))
}

/**
 * The agent's commands the box offers for `/` (UX3 R11): the server's list for this connection, only where the server said the box
 * may expand a line, never while the box would answer a question (its line is an answer then), none for an empty list.
 */
export function offeredCommands<T>(o: { canExpand: boolean; answering: boolean; list: readonly T[] | null | undefined }): readonly T[] | null {
  if (!o.canExpand || o.answering || !o.list?.length) return null
  return o.list
}

/** What a key in the chat box does (UX3 R11): pass it on, move the list's highlight, complete its row, close it, send, or nothing. */
export type BoxKeyAct = 'pass' | 'move' | 'complete' | 'close' | 'send' | 'none'

/**
 * The box's keys. While its list is open, Enter and Tab complete the highlighted row and never send; with the list open and no row,
 * they do nothing. A line is sent only by an Enter the person pressed, held down for no repeat, with the list closed; Shift+Enter is
 * a new line (a phone's Enter sends, as its keyboard's key says). Nothing while an input method composes.
 */
export function boxKey(k: { key: string; shift: boolean; repeat: boolean; trusted: boolean; composing: boolean; phone: boolean }, list: { open: boolean; rows: number }): BoxKeyAct {
  if (k.composing) return 'pass'
  if (list.open) {
    if (k.key === 'Escape') return 'close'
    if (k.key === 'ArrowDown' || k.key === 'ArrowUp') return list.rows ? 'move' : 'pass'
    if (k.key === 'Tab' && !k.shift) return list.rows && !k.repeat && k.trusted ? 'complete' : list.rows ? 'none' : 'pass'
    if (k.key === 'Enter' && !(k.shift && !k.phone)) return list.rows && !k.repeat && k.trusted ? 'complete' : 'none'
  }
  if (k.key !== 'Enter') return 'pass'
  if (k.shift && !k.phone) return 'pass'
  return k.repeat || !k.trusted ? 'none' : 'send'
}

/** The line with its first word completed to `/name ` and where the cursor goes: after the space. */
export function pickCommand(text: string, w: CommandWord, name: string): { text: string; caret: number } {
  const put = `/${name} `
  const rest = text.slice(w.end).replace(/^\s/, '')
  return { text: put + rest, caret: put.length }
}
