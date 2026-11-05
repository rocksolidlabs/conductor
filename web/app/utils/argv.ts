import { shellQuote } from './hostCommand'

// One argument per match: a double-quoted group, a single-quoted group, or a
// run of non-space characters. A quote only opens a group at the start of an
// argument, so it's and --x="a are plain characters.
const argToken = () => /"([^"]*)"|'([^']*)'|(\S+)/g

/** Minimal shell-like splitting: whitespace separated, quotes group. Nothing is expanded. */
export function splitArgs(s: string): string[] {
  const out: string[] = []
  for (const m of s.matchAll(argToken())) out.push(m[1] ?? m[2] ?? m[3] ?? '')
  return out
}

/**
 * Arguments as a line splitArgs reads back to them (#434: a clone's arguments, shown to be changed): a plain word as it is, one
 * with a space, a quote at its start or nothing in it in double quotes, or in single ones when it holds a double quote. An
 * argument holding both quotes and a space cannot be written so; a dialog that shows this line keeps the arguments themselves
 * for when the line is left as it was.
 */
export function argsText(args: readonly string[]): string {
  return args
    .map((a) => {
      if (a !== '' && !/\s/.test(a) && !/^["']/.test(a)) return a
      return a.includes('"') ? `'${a}'` : `"${a}"`
    })
    .join(' ')
}

/**
 * True while the text ends inside a quoted argument that splitArgs would not
 * close, as in `--model "gpt`. Typing a space there belongs to the argument.
 */
export function hasOpenQuote(s: string): boolean {
  for (const m of s.matchAll(argToken())) {
    // Only the bare-word branch can start with a quote, and only when no closing quote follows.
    if (m[3] !== undefined && (m[3].startsWith('"') || m[3].startsWith("'"))) return true
  }
  return false
}

/**
 * What pasting `pasted` after the typed `pending` leaves: the arguments it
 * completes and what stays typed. With a quote still open, everything stays
 * typed, so a closing quote typed or pasted next can finish it.
 */
export function pasteArgs(pending: string, pasted: string): { chips: string[]; pending: string } {
  const t = pending + pasted
  if (hasOpenQuote(t)) return { chips: [], pending: t }
  return { chips: splitArgs(t), pending: '' }
}

/** A catalog ID (`[a-z0-9-]`, at most 32 characters, no dash at either end) suggested by a display name; empty when nothing usable is left. */
export function slugId(name: string): string {
  return name
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 32)
    .replace(/-+$/, '')
}

/** An argv as one line for display, quoting only what needs it. Never fed to a shell. */
export function joinArgv(argv: string[]): string {
  return argv.map(shellQuote).join(' ')
}
