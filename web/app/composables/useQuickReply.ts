import { FOLLOW_SIZE, MAX_SUBMIT, type Welcome } from '~/utils/protocol'
import { sayOutcomeOf, type SayOutcome } from '~/utils/sayWords'
import { clearAnswered, repliedQuestions, ReplyError, type QuestionReply } from '~/utils/reply'
import type { CloseInfo, TerminalTransport } from '~/utils/transport/types'
import { skipWords } from '~/utils/crews'
import type { SessionInfo } from './useSessions'

/**
 * What became of an answer as this browser can know it: `taken`, the owner's own word that it took it (`answer_ok`); `unconfirmed`,
 * it went to an owner that never confirms answers (an older one: keys sent as they are); `not_sent`, nothing left this tab (a reply
 * to this session is already under way from here, or this tab replied to the question already). A refusal, the owner's or a lost
 * connection's, rejects with its ReplyError, as before.
 */
export type AnswerOutcome = 'taken' | 'unconfirmed' | 'not_sent'

/** The questions this browser tab has replied to from a row, a card or a chat (the app renders in the browser alone: ssr is off). */
const replied = repliedQuestions()

/**
 * Sends to a session without opening its terminal: a short-lived control connection over the existing transports
 * (relay for hosted sessions, so no WebRTC negotiation). The hello follows the session's size (FOLLOW_SIZE), so the
 * round trip never resizes it. `reply` submits a line (the owner types it and presses Enter 250 ms later, which a TUI
 * takes as Enter); `send` writes keys as they are, for a quick-reply option such as a digit.
 *
 * `question` is the id of the question the person was shown (questionOf): to an owner that takes answers, the reply is
 * taken only while that question still waits, and the promise rejects with a ReplyError (`question_answered`, replyFailure
 * has the words) when another reply answered it first, nothing sent. The connection stays until the owner's word.
 */
export function useQuickReply() {
  const { create } = useTerminalTransport()
  const admin = useWorkbenchToken()
  const sending = useState<Set<string>>('quickReplySending', () => new Set())
  const store = useAttentionStore()
  const typedAnswers = useTypedAnswers()

  function mark(id: string, on: boolean) {
    const next = new Set(sending.value)
    if (on) next.add(id)
    else next.delete(id)
    sending.value = next
  }

  /** over sends the reply on a short control connection: whether the owner took it, it went unconfirmed, or nothing was sent (AnswerOutcome). */
  async function over(session: Pick<SessionInfo, 'id' | 'kind'>, token: string | undefined, reply: QuestionReply, server?: string): Promise<AnswerOutcome> {
    // One reply per question from here: a second click on one already answered from this tab is not sent; one refused may be
    // tried again (repliedQuestions).
    if (sending.value.has(session.id)) return 'not_sent'
    let outcome: AnswerOutcome = 'not_sent'
    try {
      await replied.once(reply.question, async () => {
        mark(session.id, true)
        const t: TerminalTransport = create({ sessionId: session.id, token: token ?? admin.token.value, kind: session.kind, forceRelay: true, server })
        try {
          const welcome = await t.connect(FOLLOW_SIZE)
          if (welcome.role !== 'control') throw new Error('This link is view-only')
          // An owner that takes answers says whether it took this one; to an older one, the frame is only let leave: it finishes
          // a submission after the connection goes.
          if (await t.answer(reply)) outcome = 'taken'
          else {
            outcome = 'unconfirmed'
            await new Promise((r) => setTimeout(r, 150))
          }
        } finally {
          t.close()
          mark(session.id, false)
        }
      })
      return outcome
    } catch (e) {
      // The owner says the question is answered: no page offers it any longer (#258), whatever the stream has said so far.
      if ((e as { code?: unknown } | null)?.code === QUESTION_ANSWERED) clearAnswered(store.value.sessions, session.id, reply.question)
      throw e
    }
  }

  /** `reqId`, when given, is the request id the answer goes with (the decision page mints one per action: its track names it). */
  async function send(session: Pick<SessionInfo, 'id' | 'kind'>, input: string, opts: { token?: string; server?: string; question?: string; reqId?: string } = {}): Promise<boolean> {
    return (await over(session, opts.token, { question: opts.question, input, reqId: opts.reqId }, opts.server)) !== 'not_sent'
  }

  async function reply(session: Pick<SessionInfo, 'id' | 'kind'>, text: string, opts: { token?: string; server?: string; question?: string; reqId?: string } = {}): Promise<boolean> {
    return (await answer(session, { text }, opts)) !== 'not_sent'
  }

  /**
   * Answers a question with a choice's keys (`input`) or a typed line (`text`), and says what became of it (AnswerOutcome): the
   * decision page keeps the owner's word, and only that, as its action's outcome (useInbox).
   */
  async function answer(session: Pick<SessionInfo, 'id' | 'kind'>, what: { input: string } | { text: string }, opts: { token?: string; server?: string; question?: string; reqId?: string } = {}): Promise<AnswerOutcome> {
    const out = await over(session, opts.token, { ...what, question: opts.question, reqId: opts.reqId }, opts.server)
    // The line sent, this browser shows it under the question to its own person: the owner repeats no typed line (#364). One
    // not sent (a reply under way from here, a duplicate) is not remembered.
    if (out !== 'not_sent' && 'text' in what) typedAnswers.remember(opts.question, what.text)
    return out
  }

  /**
   * Types a line into a session that is not asking (#303, `I` and the `say` verb): the same short control connection, `submit`
   * for the line, closed once it is typed; the owner writes it and its Enter 250 ms later and goes on if the client has left by
   * then (docs/protocol.md, `submit`). The palette refuses a session waiting on a prompt before calling this, with the
   * broadcast's words.
   */
  async function say(session: Pick<SessionInfo, 'id' | 'kind'>, text: string, opts: { token?: string; server?: string; waiting?: () => boolean } = {}): Promise<SayOutcome | null> {
    if (sending.value.has(session.id)) return null
    // The owner takes MAX_SUBMIT bytes of a line (bad_frame past it): said here, nothing sent (whole-batch round).
    if (new TextEncoder().encode(text).length > MAX_SUBMIT) return { refused: `a line is at most ${MAX_SUBMIT} bytes` }
    mark(session.id, true)
    const t: TerminalTransport = create({ sessionId: session.id, token: opts.token ?? admin.token.value, kind: session.kind, forceRelay: true, server: opts.server })
    let closed: CloseInfo | null = null
    let refused: { code: string; message: string } | null = null
    t.onClose((info) => (closed = info))
    // A refusal is an error frame on the open connection (not_sent, input_failed, too_many_requests, session_ended, bad_frame).
    t.onControl((m) => {
      if (m.t === 'error' && !refused) refused = { code: m.code, message: m.message }
    })
    try {
      let welcome: Welcome
      try {
        welcome = await t.connect(FOLLOW_SIZE)
      } catch (e) {
        return { refused: (e as Error).message || 'the session could not be reached' }
      }
      if (welcome.role !== 'control') return { refused: 'This link is view-only' }
      // Looked at once more with the connection up (the page's latest word): a prompt that came meanwhile refuses the line
      // (Codex's round on #303); the owner's own guard (`unlessWaiting`, #322) holds what comes after this look, as the owner
      // claims the session's turn.
      if (opts.waiting?.()) return { refused: skipWords('needs_input') }
      let typed: boolean
      try {
        typed = await t.submitAcked(text, { unlessWaiting: true })
      } catch (e) {
        // The owner's word, by its code: not_sent (the session asks: the broadcast's words) and the other refusals typed nothing;
        // input_failed (typed and not entered), no_word and closed leave the outcome open (Codex's round on #322).
        if (!(e instanceof ReplyError)) return { unconfirmed: (e as Error).message }
        return sayOutcomeOf(e.code, e.code === 'not_sent' ? skipWords('needs_input') : e.message || e.code)
      }
      if (typed) return { typed: true }
      // An older owner says nothing for a line it took; for one it did not, an error frame, or a close that carries one; a close
      // after the line went leaves its fate open (the owner finishes a submission after its client goes).
      await new Promise((r) => setTimeout(r, 400))
      const r = refused as { code: string; message: string } | null
      if (r) return sayOutcomeOf(r.code, r.message || r.code)
      const c = closed as CloseInfo | null
      if (c) return { unconfirmed: c.error?.message || 'the connection closed before the session said anything' }
      return { typed: false }
    } finally {
      t.close()
      mark(session.id, false)
    }
  }

  return { send, reply, answer, say, sending }
}
