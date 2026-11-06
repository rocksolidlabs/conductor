import type { SessionInfo } from '~/composables/useSessions'
import { MAX_ANSWER_REQ_ID, MAX_QUESTION_ID, QUESTION_ANSWERED, type AnswerMsg, type Attention } from './protocol'

/**
 * A reply to an agent's question: a choice's keys (`input`), or a line (`text`). `question` is the id of the question the person was
 * shown (the attention's `id`): the owner takes the reply only while that question still waits, so that a click that arrives late
 * never lands on the question that came next. Without one (an older owner's question), whatever shows takes it, as before.
 */
export type QuestionReply = ({ question?: string; input: string } | { question?: string; text: string }) & {
  /** The request id the answer goes with, when the caller minted one (the decision page's, fresh per action: its attempt is told apart from the winner's); else the transport's own. */
  reqId?: string
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

/** draftQuestion for a field that knows the question shown by its id alone (`current`: a sidebar row's prompt). */
export function bindDraft(before: string, after: string, bound: string | undefined, current: string | undefined): string | undefined {
  return after.trim() && !before.trim() ? current : bound
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

/** A reply that did not go: the owner's refusal (its error code), or the page's own (`not_connected`, `no_word`, `no_question`). */
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
export function replyFailure(e: unknown, name?: string): { title: string; description?: string; color: 'warning' | 'error' } {
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
      description: `${name ? `${name}'s` : 'This'} question ${what}. Nothing was sent. If the agent still asks, answer it in its terminal.`,
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
  return { title: name ? `Reply to ${name} failed` : 'Reply failed', description: (e as Error | null)?.message, color: 'error' }
}
