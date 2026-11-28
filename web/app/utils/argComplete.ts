import type { AgentFlag } from '~/composables/useSessions'
import { splitArgs } from './argv'

/**
 * An arguments field's completion (GET /api/catalog/{id}/flags): the word at the cursor, whether it is a flag or a flag's value,
 * the rows to offer, and the text and the cursor after a pick. Pure, so that every rule is tested without a page. Words are read as
 * utils/argv.ts splits them: a word that starts with a quote runs to its closing quote, or to the line's end while it is open.
 */

/** One word of the line: where it starts and ends, its text without quotes, and its quote. */
export interface ArgWord {
  start: number
  end: number
  /** What the word holds, its quotes taken off. */
  text: string
  quote?: '"' | "'"
  /** Where its closing quote is: what follows it up to the next white space is part of the word (as splitArgs reads it). */
  close?: number
  /** A quote opened and not closed: the word runs to the line's end. */
  open?: boolean
}

/** The words of a line, with their places. */
export function argWords(line: string): ArgWord[] {
  const out: ArgWord[] = []
  let i = 0
  while (i < line.length) {
    if (/\s/.test(line[i]!)) {
      i++
      continue
    }
    const c = line[i]!
    if (c === '"' || c === "'") {
      const close = line.indexOf(c, i + 1)
      if (close >= 0) {
        let j = close + 1
        while (j < line.length && !/\s/.test(line[j]!)) j++
        out.push({ start: i, end: j, text: line.slice(i + 1, close) + line.slice(close + 1, j), quote: c, close })
        i = j
        continue
      }
      // No closing quote: argv.ts reads a bare word here; while typing, the open quote holds the rest of the line.
      out.push({ start: i, end: line.length, text: line.slice(i + 1), quote: c, open: true })
      break
    }
    let j = i
    while (j < line.length && !/\s/.test(line[j]!)) j++
    out.push({ start: i, end: j, text: line.slice(i, j) })
    i = j
  }
  return out
}

/** What the cursor is in: a flag to complete, a value of a flag, or nothing to offer. */
export type ArgContext =
  | { kind: 'none' }
  | {
      kind: 'flag'
      /** The letters typed so far (the whole word up to the cursor). */
      typed: string
      /** The part of the line a pick replaces. */
      from: number
      to: number
    }
  | {
      kind: 'value'
      flag: AgentFlag
      typed: string
      from: number
      to: number
      /** The value follows `--flag=` in the same word. */
      joined: boolean
    }

/** The flag a word names (its name or one of its spellings), the part before any `=`. */
export function flagOf(flags: readonly AgentFlag[], word: string): AgentFlag | undefined {
  const name = word.split('=', 1)[0]!
  if (!name.startsWith('-')) return undefined
  return flags.find((f) => f.name === name || (f.aliases ?? []).includes(name))
}

function takesValue(f: AgentFlag): boolean {
  return f.value === 'required' || f.value === 'optional' || (f.value === 'unknown' && !!(f.choices?.length || f.examples?.length))
}

/**
 * The context at the cursor. `browse` asks for every flag on an empty word (Arrow Down). After a lone `--` nothing is offered: what
 * follows it is the agent's own words.
 */
export function argContext(line: string, cursor: number, flags: readonly AgentFlag[], browse = false): ArgContext {
  const words = argWords(line)
  const word = words.find((w) => w.start <= cursor && cursor <= w.end)
  const before = words.filter((w) => w !== word && w.end <= (word ? word.start : cursor))
  if (before.some((w) => w.text === '--' && !w.quote)) return { kind: 'none' }
  const inner = word ? word.start + (word.quote ? 1 : 0) : cursor
  // Past a closed quote: what it holds, without the quote that closes it, and what follows it up to the cursor.
  const typed = !word
    ? ''
    : word.close !== undefined && cursor > word.close
      ? line.slice(inner, word.close) + line.slice(word.close + 1, cursor)
      : line.slice(inner, Math.max(cursor, inner))
  const from = word ? word.start : cursor
  const to = word ? word.end : cursor
  // `--flag=part`: a value of that flag, filled in after the `=`.
  if (word && !word.quote && word.text.startsWith('-') && word.text.includes('=')) {
    const f = flagOf(flags, word.text)
    const eq = word.start + word.text.indexOf('=') + 1
    if (f && takesValue(f) && cursor >= eq) return { kind: 'value', flag: f, typed: line.slice(eq, cursor), from: eq, to: word.end, joined: true }
    return { kind: 'none' }
  }
  // A word that starts with a dash is a flag (`--` as it is typed too: the start of a long flag, not yet the lone `--`); a value a
  // help lists never starts with one.
  if (word && !word.quote && typed.startsWith('-')) return { kind: 'flag', typed, from, to }
  const prev = before[before.length - 1]
  const prevFlag = prev && !prev.quote && !prev.text.includes('=') ? flagOf(flags, prev.text) : undefined
  if (prevFlag && takesValue(prevFlag)) return { kind: 'value', flag: prevFlag, typed, from, to, joined: false }
  if (!word && browse) return { kind: 'flag', typed: '', from, to }
  return { kind: 'none' }
}

/** One row of the list: what it shows, and the line and the cursor a pick leaves. */
export interface ArgRow {
  key: string
  /** The flag as its help writes it (`--permission-mode <mode>`, its short spelling after), or a value. */
  label: string
  about?: string
  /** A session launched with this flag stays in its terminal. */
  terminalOnly?: boolean
  text: string
  cursor: number
}

/** What a form sets itself, which the list leaves out. */
export interface ArgOptions {
  /** The form has a Model field for this agent: the model flag is the field's. */
  modelField?: boolean
}

/** A flag the list leaves out: what the form or a launch sets itself, and what ends the program at once. */
export function leftOut(f: AgentFlag, o: ArgOptions = {}): boolean {
  if (f.set === 'yolo' || f.set === 'session' || f.set === 'launch' || f.set === 'exit') return true
  return f.set === 'model' && !!o.modelField
}

/** The flag as its help writes it: its name, its placeholder, then its other spellings. */
export function flagLabel(f: AgentFlag): string {
  let s = f.name
  if (f.placeholder) {
    const dots = f.repeat ? '...' : ''
    s += f.value === 'optional' ? ` [${f.placeholder}${dots}]` : ` <${f.placeholder}${dots}>`
  }
  const more = (f.aliases ?? []).join(', ')
  return more ? `${s}, ${more}` : s
}

/** Starts-with before holds, the exact one first of all; the help's own order inside each. */
function ranked<T>(items: readonly T[], typed: string, spellings: (t: T) => string[]): T[] {
  const letters = typed.replace(/^-+/, '').toLowerCase()
  const t = typed.toLowerCase()
  const exact: T[] = []
  const starts: T[] = []
  const holds: T[] = []
  for (const it of items) {
    const sp = spellings(it).map((s) => s.toLowerCase())
    if (t && sp.includes(t)) exact.push(it)
    else if (sp.some((s) => s.startsWith(t))) starts.push(it)
    else if (letters && sp.some((s) => s.replace(/^-+/, '').includes(letters))) holds.push(it)
  }
  return [...exact, ...starts, ...holds]
}

/**
 * A control or format character (a newline, a bidi mark, a zero-width one), or a letter that draws as blank (the Hangul fillers,
 * the blank Braille pattern): a value holding one is never written by a pick.
 */
const UNSEEN = /[\p{Cc}\p{Cf}\u2028\u2029\u115F\u1160\u2800\u3164\uFFA0]/u

/**
 * A value as the line writes it, by the rule splitArgs reads back (utils/argv.ts): a plain word as it is; one with white space or
 * a quote at its start in double quotes, or in single ones when it holds a double quote. In a `--flag=value` word (`joined`) a
 * quote opens nothing, so only a plain word can be written there. null for a value that cannot be written as that one argument
 * (both kinds of quote and white space, an empty one) or that holds a control or format character: it is not a row a pick takes.
 */
export function valueWord(v: string, joined = false): string | null {
  if (v === '' || UNSEEN.test(v)) return null
  const plain = !/\s/.test(v) && !/^["']/.test(v)
  if (joined) return plain ? v : null
  const word = plain ? v : !v.includes('"') ? `"${v}"` : !v.includes("'") ? `'${v}'` : null
  if (word === null) return null
  const back = splitArgs(word)
  return back.length === 1 && back[0] === v ? word : null
}

/** The line with line[from, to) replaced by word, a space after it (one already there is kept), and the cursor after that space. */
export function replaceWord(line: string, from: number, to: number, word: string): { text: string; cursor: number } {
  const rest = line.slice(to)
  const space = /^\s/.test(rest) ? '' : ' '
  const text = line.slice(0, from) + word + space + rest
  return { text, cursor: from + word.length + 1 }
}

/** The rows to offer at the cursor. */
export function argRows(line: string, cursor: number, flags: readonly AgentFlag[], o: ArgOptions = {}, browse = false): ArgRow[] {
  const ctx = argContext(line, cursor, flags, browse)
  if (ctx.kind === 'none') return []
  if (ctx.kind === 'flag') {
    const offered = flags.filter((f) => !leftOut(f, o))
    return ranked(offered, ctx.typed, (f) => [f.name, ...(f.aliases ?? [])]).map((f) => {
      // A short spelling typed in full is kept; anything else becomes the long name.
      const word = (f.aliases ?? []).includes(ctx.typed) ? ctx.typed : f.name
      return { key: f.name, label: flagLabel(f), about: f.about, terminalOnly: !!f.keepsTerminal, ...replaceWord(line, ctx.from, ctx.to, word) }
    })
  }
  const values = [
    ...(ctx.flag.choices ?? []).filter((c) => c.set !== 'yolo').map((c) => ({ value: c.value, about: c.about })),
    ...(ctx.flag.examples ?? []).map((v) => ({ value: v, about: undefined as string | undefined })),
  ].filter((v, i, all) => all.findIndex((w) => w.value === v.value) === i)
  const rows: ArgRow[] = []
  for (const v of ranked(values, ctx.typed, (x) => [x.value])) {
    const word = valueWord(v.value, ctx.joined)
    if (word !== null) rows.push({ key: v.value, label: v.value, about: v.about, ...replaceWord(line, ctx.from, ctx.to, word) })
  }
  return rows
}

/** The word at the cursor as typed so far, for the rule that closes a list whose only row is that word. */
export function typedAt(line: string, cursor: number, flags: readonly AgentFlag[], browse = false): string {
  const ctx = argContext(line, cursor, flags, browse)
  return ctx.kind === 'none' ? '' : ctx.typed
}

/** Whether the list shows: rows to offer, unless the only one is the word already typed. */
export function listShows(rows: readonly ArgRow[], typed: string): boolean {
  if (rows.length === 0) return false
  if (rows.length === 1 && (rows[0]!.key === typed || rows[0]!.label === typed)) return false
  return true
}

/**
 * Whether the arguments hold a flag, or a flag's value, that turns the agent's questions off (set yolo): the line under the field
 * then points at the Yolo switch. Judged on the words the agent gets (splitArgs: quotes taken off), up to a lone `--`; a short
 * flag carries its value attached (`-anever`), or stands among others (`-xy`), as the agents' parsers read them. What is typed is
 * sent as typed all the same.
 */
export function holdsYolo(line: string, flags: readonly AgentFlag[]): boolean {
  const words = splitArgs(line)
  const yoloValue = (f: AgentFlag | undefined, v: string | undefined) => v !== undefined && !!f?.choices?.some((c) => c.set === 'yolo' && c.value === v)
  for (let i = 0; i < words.length; i++) {
    const w = words[i]!
    if (w === '--') return false
    if (!w.startsWith('-') || w === '-') continue
    if (!w.startsWith('--') && w.length > 2) {
      const short = flagOf(flags, w.slice(0, 2))
      if (short && takesValue(short)) {
        if (short.set === 'yolo' || yoloValue(short, w.slice(2).replace(/^=/, ''))) return true
        continue
      }
      // A cluster of short flags that take no value.
      if ([...w.slice(1)].some((c) => flagOf(flags, `-${c}`)?.set === 'yolo')) return true
      continue
    }
    const f = flagOf(flags, w)
    if (!f) continue
    if (f.set === 'yolo') return true
    if (yoloValue(f, w.includes('=') ? w.slice(w.indexOf('=') + 1) : words[i + 1])) return true
  }
  return false
}

/**
 * Whether a session launched with these arguments stays in its terminal, by what the agent's flags say (`keepsTerminal`, the chat's
 * own refusal) and, for an agent with a chat way (`chat`), a lone `--`, which every chat refuses.
 */
export function keepsTerminal(line: string, flags: readonly AgentFlag[], chat: boolean): boolean {
  return terminalWords(line, flags, chat).length > 0
}

/** The words of the line that keep its session in its terminal (keepsTerminal), each once, as typed before any `=`. */
export function terminalWords(line: string, flags: readonly AgentFlag[], chat: boolean): string[] {
  const out: string[] = []
  for (const w of splitArgs(line)) {
    if (w === '--') {
      if (chat && !out.includes('--')) out.push('--')
      break
    }
    const name = w.split('=', 1)[0]!
    if (!w.startsWith('-') || out.includes(name)) continue
    if (flagOf(flags, w)?.keepsTerminal) {
      out.push(name)
      continue
    }
    // A cluster of short flags (-pc): one a chat refuses keeps the session in its terminal; a letter that takes a value ends it.
    if (!w.startsWith('--') && w.length > 2 && !flagOf(flags, w)) {
      for (const c of w.slice(1)) {
        const f = flagOf(flags, `-${c}`)
        if (f?.keepsTerminal) {
          out.push(w)
          break
        }
        if (!f || takesValue(f)) break
      }
    }
  }
  return out
}

/** The line under the field for those words: `With --add-dir, this session stays in its terminal.` (two or more joined by and). */
export function terminalLine(words: readonly string[]): string {
  if (!words.length) return ''
  const list = words.length === 1 ? words[0]! : `${words.slice(0, -1).join(', ')} and ${words.at(-1)}`
  return `With ${list}, this session stays in its terminal.`
}

/**
 * The prefix GET /api/paths lists for a folder flag's value at the cursor (a flag of kind folder, as --add-dir): what is typed when
 * it is an absolute path; the folder that holds the session's folder when nothing is typed, so that the folders beside it are
 * listed (`cwd`: the session's folder, '' for the server's default, which the caller reads from the listing's `dir`); null for a
 * relative path and for any other word: nothing is listed for it.
 */
export function folderPrefix(ctx: ArgContext, cwd: string): string | null {
  if (ctx.kind !== 'value' || ctx.flag.kind !== 'folder') return null
  if (ctx.typed === '') {
    const c = cwd.trim().replace(/\/+$/, '')
    if (!c.startsWith('/')) return null
    return c.slice(0, c.lastIndexOf('/') + 1)
  }
  return ctx.typed.startsWith('/') ? ctx.typed : null
}

/**
 * The rows of a folder flag's value: each folder listed, its path put in place of the word (in quotes when it holds a space) and the
 * cursor after it with no space, so that the list goes on to that folder's children; a space typed ends the value.
 */
export function folderRows(line: string, ctx: ArgContext, folders: readonly { path: string; name: string }[]): ArgRow[] {
  if (ctx.kind !== 'value') return []
  const rows: ArgRow[] = []
  for (const f of folders) {
    // A folder whose path cannot be written as the one argument the row shows is not offered (valueWord); nor one whose name, as
    // the row would show it, holds a mark a person cannot see.
    const word = valueWord(f.path, ctx.joined)
    if (word === null || UNSEEN.test(f.name)) continue
    // The cursor goes after the closing quote: a child typed there is part of the same argument (splitArgs), and a space ends it.
    rows.push({ key: f.path, label: f.name, text: line.slice(0, ctx.from) + word + line.slice(ctx.to), cursor: ctx.from + word.length })
  }
  return rows
}

/**
 * The last word of the arguments when it is a flag the agent's help says takes a value and none follows it: the agent would take
 * the next word of its command line, one Conductor adds, as that value, so the launch is held and the server refuses it alike.
 * An optional value is no such flag; a flag the list does not know is not judged.
 */
export function needsValue(line: string, flags: readonly AgentFlag[]): string {
  const words = splitArgs(line)
  const last = words.at(-1)
  if (!last || !last.startsWith('-') || last === '-' || last === '--' || last.includes('=')) return ''
  if (words.includes('--') && words.indexOf('--') < words.length - 1) return ''
  const f = flagOf(flags, last)
  return f && f.value === 'required' ? last : ''
}

/** The line under the field for such a flag. */
export function needsValueLine(flag: string): string {
  return flag ? `${flag} needs a value.` : ''
}
