import { MAX_SESSION_NAME } from './protocol'

/**
 * Renaming a session (#318): what the server makes of a name (session.CleanSessionName), so that the dialog says what it keeps
 * before it is sent. Control characters (C0, DEL and C1) go and the space around the name is trimmed; a name with nothing left
 * is refused (invalid_name), and one past MAX_SESSION_NAME bytes would be cut, which the dialog asks the person to do instead.
 */

/** Whether a code point is a control character: C0, DEL or C1, as Go's unicode.IsControl says. */
function isControl(cp: number): boolean {
  return cp < 0x20 || (cp >= 0x7f && cp < 0xa0)
}

/** The name without its control characters and the space around it. */
export function cleanSessionName(s: string): string {
  let out = ''
  for (const ch of s) if (!isControl(ch.codePointAt(0) ?? 0)) out += ch
  return out.trim()
}

/** A name's length as the server bounds it: its UTF-8 bytes. */
export function nameBytes(s: string): number {
  return new TextEncoder().encode(s).length
}

/** What the dialog makes of a draft: the name it would send, its bytes, and why it cannot be sent yet (nothing left, or too long). */
export interface RenameDraft {
  name: string
  bytes: number
  problem?: 'empty' | 'long'
}

export function renameDraft(draft: string): RenameDraft {
  const name = cleanSessionName(draft)
  const bytes = nameBytes(name)
  if (!name) return { name, bytes, problem: 'empty' }
  if (bytes > MAX_SESSION_NAME) return { name, bytes, problem: 'long' }
  return { name, bytes }
}
