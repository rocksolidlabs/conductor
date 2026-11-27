import type { AgentFlag } from '~/composables/useSessions'

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
        out.push({ start: i, end: close + 1, text: line.slice(i + 1, close), quote: c })
        i = close + 1
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
  const typed = word ? line.slice(inner, Math.max(cursor, inner)) : ''
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

/** A value as the line writes it: in double quotes when it holds a space. */
export function valueWord(v: string): string {
  return /\s/.test(v) ? `"${v}"` : v
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
  return ranked(values, ctx.typed, (v) => [v.value]).map((v) => ({
    key: v.value,
    label: v.value,
    about: v.about,
    ...replaceWord(line, ctx.from, ctx.to, valueWord(v.value)),
  }))
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
 * Whether the line holds a flag, or a flag's value, that turns the agent's questions off (set yolo): the line under the field then
 * points at the Yolo switch. What is typed is sent as typed all the same.
 */
export function holdsYolo(line: string, flags: readonly AgentFlag[]): boolean {
  const words = argWords(line)
  for (let i = 0; i < words.length; i++) {
    const w = words[i]!
    if (w.quote) continue
    if (w.text === '--') return false
    const f = flagOf(flags, w.text)
    if (!f) continue
    if (f.set === 'yolo') return true
    const value = w.text.includes('=') ? w.text.slice(w.text.indexOf('=') + 1) : words[i + 1]?.text
    if (value !== undefined && (f.choices ?? []).some((c) => c.set === 'yolo' && c.value === value)) return true
  }
  return false
}
