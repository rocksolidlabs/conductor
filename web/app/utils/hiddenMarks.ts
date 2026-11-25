// The characters a person cannot see (docs/plans/chat-or-terminal.md, design 11): what the task file's quoting judges draws nothing
// (internal/quoted). A character that is not graphic (a control, a format character such as a bidirectional one or a zero-width
// space, a line or paragraph separator, a private-use or an unassigned one), a default-ignorable one, or a variation selector. The
// run page's origin line (runOrigin.ts) and a chat's permission question draw each as a mark of its own, never applied.

/** The set, as a pattern's source: one character of it. */
export const HIDDEN_SOURCE = String.raw`[^\p{L}\p{M}\p{N}\p{P}\p{S}\p{Zs}]|[\p{Default_Ignorable_Code_Point}\p{Variation_Selector}]`

/** The set as a global pattern, for a replace over a whole string. */
export const HIDDEN = new RegExp(HIDDEN_SOURCE, 'gu')

const ONE = new RegExp(`^(?:${HIDDEN_SOURCE})$`, 'u')

/** A piece of text as a question draws it: words as typed, a hidden character's mark, or a line break (a line feed). */
export type MarkedPiece = { text: string } | { mark: string; code: number } | { br: true }

/** Whether the character c (one code point) is one a person cannot see. A line feed is one: a question draws it as a line break. */
export function isHidden(c: string): boolean {
  return ONE.test(c)
}

/** The mark a hidden character is drawn as: a carriage return as `\r`, a tab as `\t`, any other as its `\u` escape. */
export function hiddenMark(code: number): string {
  if (code === 0x0d) return '\\r'
  if (code === 0x09) return '\\t'
  const hex = code.toString(16).toUpperCase()
  return code > 0xffff ? `\\u{${hex}}` : `\\u${hex.padStart(4, '0')}`
}

/**
 * s as a question draws it: every character a person cannot see a mark of its own (hiddenMark), so none reorders, hides or joins the
 * words around it; a line feed a line break, and nothing else. `hidden` says whether any was marked (a line feed is not counted).
 */
export function markHidden(s: string): { pieces: MarkedPiece[]; hidden: boolean } {
  const pieces: MarkedPiece[] = []
  let text = ''
  let hidden = false
  const flush = () => {
    if (text) pieces.push({ text })
    text = ''
  }
  for (const c of s) {
    if (c === '\n') {
      flush()
      pieces.push({ br: true })
    } else if (isHidden(c)) {
      flush()
      const code = c.codePointAt(0)!
      pieces.push({ mark: hiddenMark(code), code })
      hidden = true
    } else text += c
  }
  flush()
  return { pieces, hidden }
}

/** Whether s holds a character a person cannot see, its line feeds aside. */
export function holdsHidden(s: string): boolean {
  for (const c of s) if (c !== '\n' && isHidden(c)) return true
  return false
}
