import { nextTick, ref, watch, type Ref } from 'vue'
import type { SessionInfo } from '~/composables/useSessions'
import { ANSWER_DECISION, CONTROL_TOO_LARGE, ERR_NOT_TERMINAL, MAX_ANSWER_REQ_ID, MAX_QUESTION_ID, QUESTION_ANSWERED, TOO_LONG, type AnswerMsg, type Attention } from './protocol'
import { decisionAnsweredBy } from './attention'

/**
 * A reply to an agent's question: a choice's keys (`input`), or a line (`text`). `question` is the id of the question the person was
 * shown (the attention's `id`): the owner takes the reply only while that question still waits, so that a click that arrives late
 * never lands on the question that came next. Without one (an older owner's question), whatever shows takes it, as before.
 */
export type QuestionReply = ({ question?: string; input: string } | { question?: string; text: string }) & {
  /** The request id the answer goes with, when the caller minted one (the decision page's, fresh per action: its attempt is told apart from the winner's); else the transport's own. */
  reqId?: string
}

/**
 * A question's choice pressed (its label, the keys it types), as the reply it is by the way the session runs: in its terminal the
 * choice's keys, typed as a line as ever; in a chat, which has no terminal, the choice's words as the person's answer, sent as a
 * typed answer to a question is (docs/protocol.md: a chat refuses keys, `not_terminal`). Every place a choice is offered sends it so.
 */
export function choiceReply(o: { label: string; input: string }, way: string | undefined): { input: string } | { text: string } {
  return way === 'protocol' ? { text: o.label } : { input: o.input }
}

/** The question a reply to the attention shown names: its id while it waits on input; none otherwise. */
export function questionOf(att: Pick<Attention, 'state' | 'id'> | null | undefined): string | undefined {
  return att?.state === 'needs_input' && att.id ? att.id : undefined
}

/**
 * The question a reply line names: the one it was begun for. As its text goes from empty to something (`before` to `after`), the
 * question the attention shown then is (questionOf); otherwise the one it was bound to (`bound`). A line typed for one question
 * still names it once another has taken its place (someone else answered), so that the owner refuses it rather than take it for
 * the next.
 */
export function draftQuestion(
  before: string,
  after: string,
  bound: string | undefined,
  att: Pick<Attention, 'state' | 'id'> | null | undefined,
): string | undefined {
  return bindDraft(before, after, bound, questionOf(att))
}

/**
 * draftQuestion for a field that knows the question shown by its id alone (`current`: a sidebar row's prompt). `kept`: the line
 * came back not sent (#654); the person's first change of it binds it anew, to the question shown at that change, as a fresh
 * line's is. Unchanged, it stays bound to the question it was typed for.
 */
export function bindDraft(before: string, after: string, bound: string | undefined, current: string | undefined, kept = false): string | undefined {
  if (!after.trim()) return bound
  return !before.trim() || (kept && after !== before) ? current : bound
}

/**
 * The attention a session page shows when the live listing reports `listed`: the listing's, unless it lacks the question's id (a
 * server older than the host, a report the host has not told its id for yet) while the page's own connection to the owner, open,
 * reported the attention it shows with one (`shown`): the owner's word stands, as it is, until the listing names a question. No
 * attention is matched to another by its words: an identical question asked again is another question.
 */
export function listedAttention(listed: Attention, shown: Attention | null | undefined, ownerOpen: boolean): Attention {
  if (!listed.id && shown?.id && ownerOpen) return shown
  return listed
}

/** A question's or a request's id as the owner takes it: printable ASCII, no space, within its bound. */
function wireId(id: string, max: number): boolean {
  return id.length > 0 && id.length <= max && /^[\x21-\x7e]+$/.test(id)
}

/**
 * The `answer` message a reply goes as, or null when it names no question the owner could take (none, or not an id): it goes as
 * keys or a submit then, as before.
 */
export function answerMessage(reply: QuestionReply, reqId: string): AnswerMsg | null {
  const q = reply.question
  if (!q || !wireId(q, MAX_QUESTION_ID) || !wireId(reqId, MAX_ANSWER_REQ_ID)) return null
  if ('input' in reply) return reply.input ? { t: 'answer', reqId, question: q, input: reply.input } : null
  return reply.text ? { t: 'answer', reqId, question: q, text: reply.text } : null
}

/** How long a reply waits for the owner's word (`answer_ok`, or a refusal) before the page says it was not confirmed, in seconds. */
export const ANSWER_WAIT_SECONDS = 15

/** How many questions a page remembers having replied to. */
const REMEMBERED = 64

/**
 * The questions a page has sent a reply to: a second click or key on one (a double click, a digit pressed twice before the
 * question leaves the page) is not sent, so the person is never told their own question was answered by someone else. `take`
 * says whether a reply to `question` may go, and notes it; `release` forgets it. `once` sends a reply that may go and forgets
 * it when it did not go, whatever the refusal (not connected, the session slow, or answered: a question refused while the
 * session's turn was held over it is asked again with its id once a person's keys end the hold without answering it), so that
 * it can be tried again; the owner refuses it again while it is answered. A reply naming no question always goes.
 */
export function repliedQuestions() {
  const sent: string[] = []
  function take(question: string | undefined): boolean {
    if (!question) return true
    if (sent.includes(question)) return false
    sent.push(question)
    if (sent.length > REMEMBERED) sent.shift()
    return true
  }
  function release(question: string | undefined) {
    const i = question ? sent.indexOf(question) : -1
    if (i >= 0) sent.splice(i, 1)
  }
  /** Sends a reply to `question` with `send` unless one went from here or is on its way: whether it was sent. */
  async function once(question: string | undefined, send: () => Promise<unknown>): Promise<boolean> {
    if (!take(question)) return false
    try {
      await send()
    } catch (e) {
      release(question)
      throw e
    }
    return true
  }
  return { take, release, once }
}

/**
 * The page's refusal of a reply that names no question to an owner that takes answers (a hosted session's question the server
 * listed before its host said its id, or a server older than its host): nothing is sent, for keys that name nothing could land
 * on another question.
 */
export const NO_QUESTION = 'no_question'

/**
 * What became of a reply line (#654), as its field hears it: `left`, the line left the page (its frame written: the field empties,
 * the line kept aside), always followed by one of the three others; `sent`, the owner took it, to type now or to hold for the
 * session's turn, or nothing will say (an older owner; no word in time; typed and not entered): nothing is left to send again;
 * `refused`, the owner said no, or the page did before it left: the line is in the field, exactly as typed and bound to the
 * question it was typed for; `held`, it never left the page (a reply to the same question, or an action on the same item, still
 * on its way): nothing changes.
 */
export type ReplyOutcome = 'left' | 'sent' | 'refused' | 'held'
/** A page's word to the field a reply line came from: what became of the line. Each word once, `left` first when it comes. */
export type ReplyTaken = (outcome: ReplyOutcome) => void

/** What the toast for a typed line refused as answered adds: the line stays in its field, and how it goes. */
export const LINE_KEPT = 'Your line is kept: change it to send it for what is asked now.'

/**
 * What a reply's failure (its promise's rejection) is for its field: every word the owner gives a reply, and the page's own, by
 * its code (docs/protocol.md, `answer`, `submit`, Error codes). None is left to a default.
 */
export function replyOutcomeOf(e: unknown): 'sent' | 'refused' {
  const code = (e as { code?: unknown } | null)?.code
  switch (code) {
    // The owner said no: nothing of the line was written.
    case QUESTION_ANSWERED:
    case ANSWER_DECISION:
    case 'not_sent':
    case 'read_only':
    case 'session_ended':
    case 'too_many_requests':
    case 'bad_frame':
    case 'revoked':
    case 'expired':
      return 'refused'
    // Its outcome is open: the line was typed and not entered, or may be typed yet (no word in time, the connection gone while
    // the owner holds it for the session's turn). Sent again, it would be typed twice.
    case 'input_failed':
    case 'no_word':
    case 'closed':
      return 'sent'
    // The page's own: nothing left the page.
    case TOO_LONG:
    case NO_QUESTION:
    case 'not_connected':
      return 'refused'
  }
  // A failure that carries no code of a reply's (the connection could not be made, a view-only link): nothing left the page.
  return 'refused'
}

/**
 * The word a reply field hands its page with a line (#654), for a field that keeps no question (the decision card's line). A line
 * that leaves empties the field if it still holds it, and is kept aside: refused, it comes back into the field if the field is
 * still empty (`kept` runs, and the focus goes back to the field when nothing else took it meanwhile: a field disabled while its
 * line was on its way loses it); sent, it is let go. Held changes nothing.
 */
export function holdLine(text: Ref<string>, kept?: () => void): ReplyTaken {
  const line = text.value
  const from = typeof document === 'undefined' ? null : (document.activeElement as HTMLElement | null)
  let away = false
  return (outcome) => {
    if (outcome === 'held') return
    if (outcome === 'left') {
      if (text.value === line) {
        text.value = ''
        away = true
      }
      return
    }
    if (outcome === 'sent') {
      if (!away && text.value === line) text.value = ''
      away = false
      return
    }
    if (away && text.value === '') text.value = line
    away = false
    kept?.()
    giveFocusBack(from)
  }
}

/**
 * A page's word passed on as said once: `left` once, and the first of the other three alone (a fallback said after the word was
 * given is not heard).
 */
export function onceTaken(taken: ReplyTaken | undefined): ReplyTaken {
  let left = false
  let told = false
  return (outcome) => {
    if (told) return
    if (outcome === 'left') {
      if (!left) taken?.('left')
      left = true
      return
    }
    told = true
    taken?.(outcome)
  }
}

/**
 * Gives the focus back to `from`, the field a line was sent from, once the page has drawn the refusal, when nothing else took it
 * meanwhile (a field disabled while its line was on its way loses it) and the field is still there.
 */
export function giveFocusBack(from: HTMLElement | null): void {
  if (!from || typeof document === 'undefined' || from === document.body) return
  void nextTick(() => {
    const now = document.activeElement
    if (from.isConnected && (!now || now === document.body)) from.focus()
  })
}

/**
 * A reply field's line and the question it names (#654): bound to the question shown as the line is begun, and bound anew only when
 * the person changes a line that came back refused (bindDraft). A line that left the page and is refused comes back into the
 * field, if it is still empty, bound to the question it was typed for. `send` hands the line (trimmed), its question and the
 * page's word; null for a blank line.
 */
export function replyLine(current: () => string | undefined) {
  const text = ref('')
  const question = ref<string | undefined>()
  let kept = false
  /** The binding a line coming back into the field takes, set for the edit that puts it back. */
  let back: { question: string | undefined } | null = null
  watch(
    text,
    (now, before) => {
      if (back) {
        question.value = back.question
        back = null
        kept = true
        return
      }
      question.value = bindDraft(before, now, question.value, current(), kept)
      kept = false
    },
    { flush: 'sync' },
  )
  function send(): { line: string; question: string | undefined; taken: ReplyTaken } | null {
    const line = text.value.trim()
    if (!line) return null
    const raw = text.value
    const bound = question.value
    const from = typeof document === 'undefined' ? null : (document.activeElement as HTMLElement | null)
    let away = false
    const taken: ReplyTaken = (outcome) => {
      if (outcome === 'held') return
      if (outcome === 'left') {
        if (text.value === raw) {
          text.value = ''
          away = true
        }
        return
      }
      if (outcome === 'sent') {
        if (!away && text.value === raw) text.value = ''
        away = false
        return
      }
      if (away && text.value === '') {
        back = { question: bound }
        text.value = raw
      } else if (text.value === raw) kept = true
      away = false
      giveFocusBack(from)
    }
    return { line, question: bound, taken }
  }
  return { text, question, send }
}

/** A reply that did not go: the owner's refusal (its error code), or the page's own (`not_connected`, `no_word`, `no_question`, `too_long`). */
export class ReplyError extends Error {
  readonly code: string
  constructor(code: string, message: string) {
    super(message)
    this.name = 'ReplyError'
    this.code = code
  }
}

/**
 * A reply the owner refused as answered (`question_answered`) named question `question` of session `id`: no page goes on offering
 * that question (#258). The live store's session loses it at once, its attention the empty state with source `input`, as the
 * stream will say; a session whose attention is another question, or none, is left alone. Whether anything changed.
 */
export function clearAnswered(sessions: Map<string, SessionInfo>, id: string, question: string | undefined): boolean {
  if (!question) return false
  const s = sessions.get(id)
  if (!s || s.attention?.state !== 'needs_input' || s.attention.id !== question) return false
  sessions.set(id, { ...s, attention: { state: '', source: 'input' } })
  return true
}

/** The words a page shows for a reply that did not go (a toast's): `name` the session it was for, where the page lists several. */
/** A choice's keys refused by a chat (`not_terminal`): the page's own words, which say what to do. */
export const NOT_TERMINAL_CHOICE = 'This session runs as a chat now, so your choice was not sent. Press it again, or write your answer in the chat.'

/**
 * `line`: the reply was a typed line, which its field keeps (#654): an answered question's toast says so, and how it goes. `way`: how
 * the session runs; a choice refused in a chat is told no terminal (it has none).
 */
export function replyFailure(e: unknown, name?: string, line = false, way?: string): { title: string; description?: string; color: 'warning' | 'error' } {
  const code = (e as { code?: unknown } | null)?.code
  if (code === QUESTION_ANSWERED) {
    // The owner's words say what became of the question when it knows: answered by whom (its recorded result), being
    // answered by another reply, or answered in its terminal.
    const words = (e as { message?: unknown } | null)?.message
    const said = typeof words === 'string' ? words : ''
    const by = /answered by (.+?): nothing was sent$/.exec(said)?.[1]
    const what = by ? `was answered by ${by}` : /being answered/.test(said) ? 'is being answered by someone else' : /in the terminal/.test(said) ? 'was answered in its terminal' : 'was answered before your reply arrived'
    return {
      title: 'Already answered',
      // A line typed is kept in its field, whatever the way; a choice on a session in its terminal may be answered there, and one in
      // a chat has nothing more to say: its question was answered and nothing was sent (#1148).
      description: `${name ? `${name}'s` : 'This'} question ${what}. Nothing was sent.${line ? ` ${LINE_KEPT}` : way === 'protocol' ? '' : ' If the agent still asks, answer it in its terminal.'}`,
      color: 'warning',
    }
  }
  if (code === ANSWER_DECISION) {
    // The question mirrors a durable decision: it is answered as that decision, by the operator, and the owner's words name it.
    const words = (e as { message?: unknown } | null)?.message
    const id = typeof words === 'string' ? /\b(dr-[1-9][0-9]*)\b/.exec(words)?.[1] : undefined
    return {
      title: 'Answered as a decision',
      description: `${id ? decisionAnsweredBy(id) : 'The operator answers it in Conductor'}: ${name ? `${name}'s` : 'this'} question takes no reply. Nothing was sent.`,
      color: 'warning',
    }
  }
  if (code === NO_QUESTION) {
    return { title: 'Not sent', description: 'This page could not tell the session which question your reply was for, so nothing was sent. Try again in a moment, or answer in the terminal.', color: 'warning' }
  }
  if (code === 'no_word' || code === 'closed') {
    const when = code === 'no_word' ? `within ${ANSWER_WAIT_SECONDS} s` : 'before the connection closed'
    return {
      title: 'Not confirmed',
      description: `${name ? `${name} did` : 'The session did'} not say ${when} whether it took your reply. Look at the session before you answer again.`,
      color: 'warning',
    }
  }
  if (code === 'not_connected') return { title: 'Not connected', description: 'Reconnect the terminal and try again.', color: 'warning' }
  // A choice's keys that reached a chat (the session moved to its chat as the choice left): nothing was typed.
  if (code === ERR_NOT_TERMINAL) return { title: 'Not sent', description: NOT_TERMINAL_CHOICE, color: 'warning' }
  // The line's frame would pass the owner's control bound: nothing was sent, in the owner's own words.
  if (code === TOO_LONG) return { title: 'Not sent', description: CONTROL_TOO_LARGE, color: 'warning' }
  return { title: name ? `Reply to ${name} failed` : 'Reply failed', description: (e as Error | null)?.message, color: 'error' }
}
