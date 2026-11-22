import { TOO_LONG } from './protocol'

/**
 * What the palette says after a line typed into a session (#303, #322): typed
 * when the owner said the line was entered, sent when an older owner said
 * nothing, not sent with why when it was refused (nothing typed, a line
 * past the control bound too: the line then waits in the row's field),
 * and not confirmed when its fate is not
 * known (no word in time, the connection gone, a line typed and not
 * entered): the owner finishes a submission after its client goes, and a
 * line typed sits in the program's input, so the line is not offered again
 * (Codex's round). Pure.
 */
export interface SayToast {
  title: string
  description: string
  color: 'success' | 'neutral' | 'warning'
}

export type SayOutcome = { typed: boolean } | { refused: string } | { unconfirmed: string }

/** The line as the toast shows it: cut past 80 characters. */
export function sayLine(line: string): string {
  return line.length > 80 ? `${line.slice(0, 80)}…` : line
}

/**
 * The owner's refusal codes that typed nothing, which the field offers the line again for; every other code (`input_failed`: typed
 * and not entered; `no_word`, `closed`: no word) leaves the outcome open.
 */
const REFUSED_CODES = new Set(['not_sent', 'read_only', 'session_ended', 'too_many_requests', 'bad_frame', 'not_connected', TOO_LONG])

/** An owner's or transport's word by its code: a refusal (nothing typed) or an outcome not known. */
export function sayOutcomeOf(code: string, message: string): SayOutcome {
  return REFUSED_CODES.has(code) ? { refused: message } : { unconfirmed: message }
}

export function sayWords(name: string, line: string, outcome: SayOutcome): { toast: SayToast; keepDraft: boolean } {
  const short = sayLine(line)
  if ('refused' in outcome) {
    return { toast: { title: `${name}: not sent`, description: `${outcome.refused} · the line waits in the row's field`, color: 'warning' }, keepDraft: true }
  }
  if ('unconfirmed' in outcome) {
    return { toast: { title: `${name}: not confirmed`, description: `${outcome.unconfirmed} · look at its terminal before typing the line again`, color: 'warning' }, keepDraft: false }
  }
  if (outcome.typed) return { toast: { title: `Typed into ${name}`, description: `${short} · entered, the session's owner says`, color: 'success' }, keepDraft: false }
  return {
    toast: { title: `Sent to ${name}`, description: `${short} · the session's owner types it and its Enter; a refusal shows here, nothing confirms it landed`, color: 'neutral' },
    keepDraft: false,
  }
}
