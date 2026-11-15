import type { Attention } from './protocol'

/**
 * What the terminal's paste control types for text (#902): its characters with every control character taken out (a line break,
 * a tab, ESC and the C1 controls among them), never turned into a key. A program with bracketed paste off reads no Enter from it,
 * and with it on the text cannot end the paste early (ESC [ 2 0 1 ~ loses its ESC and stays inside the paste).
 */
export function pasteKeys(text: string): string {
  let out = ''
  for (const ch of text) {
    const c = ch.codePointAt(0) ?? 0
    if (c < 0x20 || (c >= 0x7f && c <= 0x9f)) continue
    out += ch
  }
  return out
}

/** Whether text holds a line break, which the paste control takes out (pasteKeys) and says so. */
export function holdsBreak(text: string): boolean {
  return text.includes('\n') || text.includes('\r')
}

/**
 * The startup question a shows, when it is one (the session's trust source): its id, '' for none. The paste control is tied to
 * it: opened on one question, it types nothing once another shows.
 */
export function startupShown(a: Pick<Attention, 'state' | 'source' | 'id'> | undefined): string {
  return a?.state === 'needs_input' && a.source === 'trust' ? (a.id ?? '?') : ''
}

/**
 * Whether a startup question with choices shows (a folder's trust question, an update dialog, a numbered choice): the paste
 * control is not offered then, and types nothing; the question is answered with its own choices. a is the attention as the page
 * received it, never its live form (liveAttention): a question answered in the terminal reads as none there, while its screen,
 * still drawn, would take the paste. A startup question with no choices (a sign-in screen that asks for a code) holds nothing.
 */
export function pasteHeld(a: Pick<Attention, 'state' | 'source' | 'options'> | undefined): boolean {
  return a?.state === 'needs_input' && a.source === 'trust' && !!a.options?.length
}

/** What the paste control says while a startup question with choices shows (pasteHeld). */
export const PASTE_HELD = 'The agent asks a startup question: answer it with the question’s choices, not by typing.'

/** What it says when the startup question it opened on is no longer the one showing. */
export const PASTE_MOVED = 'The screen changed since this opened: nothing was typed. Look at the terminal, then type it again.'

/** What it says of a text that holds a line break (holdsBreak). */
export const PASTE_BREAKS = 'Its line breaks are taken out: nothing is entered.'
