import type { OwnLines, RowDrafts } from './rowDrafts'

/**
 * Whose the chat box's line is (UX3 R10, I19): the rules ChatComposer keeps by, pure, so that their tests prove what they do. A line
 * may go for the agent to expand only when it is the person's own: what their own typing, line breaks, composing, pasting and
 * deleting left in the box. Every other kind of input (a drop, a spelling fix the browser makes, an inserted suggestion, anything not
 * on this list) and every change made by code makes it the page's.
 */
const OWN_INPUT = new Set([
  'insertText',
  'insertLineBreak',
  'insertParagraph',
  'insertCompositionText',
  'insertFromComposition',
  'deleteCompositionText',
  'insertFromPaste',
  'insertFromPasteAsQuotation',
  'deleteContentBackward',
  'deleteContentForward',
  'deleteWordBackward',
  'deleteWordForward',
  'deleteSoftLineBackward',
  'deleteSoftLineForward',
  'deleteHardLineBackward',
  'deleteHardLineForward',
  'deleteEntireSoftLine',
  'deleteByCut',
])

/** Whether an input event is the person's own typing or pasting: trusted, and of a kind on the list. */
export function ownInput(e: { isTrusted: boolean; inputType?: string }): boolean {
  return e.isTrusted && !!e.inputType && OWN_INPUT.has(e.inputType)
}

/** What an input in the box does to whose its line is: the person's, with the text it left, or the page's. */
export function markInput(own: OwnLines, id: string, e: { isTrusted: boolean; inputType?: string }, text: string): void {
  if (ownInput(e)) own.typed(id, text)
  else own.set(id)
}

/**
 * A line held because this page had already answered its question: it goes free, a message now, and as written until the person
 * changes it (a line typed as an answer is never sent as a command). True when it was the line in the box.
 */
export function freeHeld(lines: RowDrafts, own: OwnLines, id: string, raw: string): boolean {
  if (lines.text(id) !== raw) return false
  lines.free(id)
  own.set(id)
  return true
}

/** Whether a line goes for the agent to expand: the server's word for this connection, and the person's own line, unchanged. */
export function expandsLine(canExpand: boolean, own: OwnLines, id: string, text: string): boolean {
  return canExpand && own.is(id, text)
}
