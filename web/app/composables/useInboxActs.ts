import { isStale } from '~/utils/brakemanCards'
import { DispatchOrder } from '~/utils/dispatchOrder'
import { replyFailure, ReplyError } from '~/utils/reply'
import { mintCommandId, type TaskCommand } from '~/utils/tasks'
import {
  answerFailure,
  mintRequestId,
  movedOnWords,
  planIdOf,
  receiptFailure,
  refParts,
  UNCONFIRMED,
  whereToAnswer,
  type DeliveryStep,
  type InboxAction,
  type InboxItem,
  type RecentEntry,
} from '~/utils/inbox'
import { dispatchFailureWords } from './useDispatchOps'
import type { InboxLive } from './useInbox'
import type { AnswerOutcome } from './useQuickReply'
import type { SessionInfo } from './useSessions'

/**
 * What the decision page shows of the action under way or last taken on one item, kept by the item's ref and naming the action it
 * belongs to (its receipt and request id): a write that comes back for an action the item no longer holds (another taken on it
 * since) changes nothing, and nothing of one item's action is ever drawn on another's controls.
 */
export interface ActFeedback {
  receipt: string
  attempt?: string
  /** The action's label while its request is out; '' once what became of it is known. */
  busy: string
  /** Its refusal by a receipt, or why nothing was sent, in words; '' for none. */
  words: string
  /** A send kept in no recent entry (a comment) whose outcome is unknown, in words; '' otherwise. */
  unknown: string
}

/** Each item's feedback, by the item's ref. */
export type ActsFeedback = Record<string, ActFeedback>

/** A session as an answer needs it: its kind picks the transport (a hosted session's goes through the hosted relay). */
export type SessionKnown = Pick<SessionInfo, 'id' | 'kind' | 'name'>

/** How the page's sends go out; each rejects with what the operation's owner said (a ReplyError, a CommandRefused) or the failure. */
export interface ActSends {
  /** The session an answer goes to, its kind known; null when it cannot be known now (nothing is sent then). */
  session: (id: string) => Promise<SessionKnown | null>
  answer: (s: SessionKnown, what: { input: string } | { text: string }, o: { question: string; reqId: string }) => Promise<AnswerOutcome>
  command: (run: string, task: string, name: TaskCommand, o: { expectedRevision?: number; commandId: string }) => Promise<unknown>
  plan: (a: InboxAction, id: string, o: { what: string; expectedRev?: number; opId: string }) => Promise<unknown>
  comment: (ticket: string, body: string, opId: string) => Promise<unknown>
  /** The words of a stale refusal (the run, the revision moved on), read from what the item is now. */
  movedOn: (it: InboxItem, a: InboxAction) => Promise<string>
}

/** What a send came to: the four outcomes (sent is the time before one of the other three), or nothing sent at all. */
export type ActCame = { how: 'confirmed' } | { how: 'refused'; words: string } | { how: 'unconfirmed' } | { how: 'not_sent'; words: string }

/**
 * The session an answer to a question of session `id` goes to, its kind known before anything is sent: the stream's listing when it
 * holds the session, else the session read by its id (the item can be read before the stream's first snapshot comes); null when
 * neither knows it.
 */
export async function sessionFor(listed: readonly SessionKnown[], id: string, read: (id: string) => Promise<SessionKnown>): Promise<SessionKnown | null> {
  const s = listed.find((x) => x.id === id)
  if (s) return s
  try {
    return await read(id)
  } catch {
    return null
  }
}

const NOT_SENT_BUSY = 'Not sent: a reply from this tab is already on its way to this session, or this tab answered this question already.'
const NOT_SENT_SESSION = "Not sent: the session could not be read, so this page cannot tell how to reach it. Read again, or answer in the session's terminal."

/**
 * Every send of the decision page (docs/plans/decision-inbox.md, PR-2, "Acting"): a question's answer, a task's command, a plan's
 * approval or withdrawal, a comment on a ticket. One outcome for each, decided here and nowhere else, of four (useInbox, "What the
 * page knows of an answer"): sent (the request out), confirmed by a receipt, refused by a receipt (the owner's own refusal, with its
 * words, whatever the track says of the item), or unknown (a transport failure, a reply lost or with no receipt): never a refusal
 * for an unknown. A request that never left the browser is no send: its recent entry goes, and the item says why. What the steps
 * say past that (recorded, validated, resumed, whether they are final) is the server's track's (useInbox), never the send's.
 */
export class InboxActs {
  private fb: ActsFeedback = {}

  constructor(
    private live: Pick<InboxLive, 'remember' | 'forget' | 'outcome' | 'trackNow'>,
    private sends: ActSends,
    private apply: (f: ActsFeedback) => void = () => {},
    private ids: { request: () => string; command: () => string } = { request: mintRequestId, command: mintCommandId },
    private now: () => number = Date.now,
  ) {}

  get feedback(): ActsFeedback {
    return this.fb
  }

  /** Whether an action on the item is out: one at a time per item. */
  pending(ref: string): boolean {
    return !!this.fb[ref]?.busy
  }

  /** Does action `a` on item `it` (a typed line in `text`); null when nothing is done (one is out on the item, or no route fits). */
  act(it: InboxItem, a: InboxAction, text?: string): Promise<ActCame | null> {
    if (this.pending(it.ref)) return Promise.resolve(null)
    if (a.name === 'answer') return this.answer(it, a, text)
    if (a.name === 'retry' || a.name === 'accept' || a.name === 'cancel') return this.command(it, a)
    if (a.name === 'approve' || a.name === 'withdraw') return this.plan(it, a)
    return Promise.resolve(null)
  }

  /** Comments on the ticket a run of the item was launched from: kept in no recent entry (it has no track), its outcome said in place. */
  async comment(it: InboxItem, ticket: string, body: string): Promise<ActCame | null> {
    if (this.pending(it.ref)) return null
    const opId = this.ids.command()
    return this.send(null, it.ref, { receipt: `comment:${opId}` }, 'Comment', async (sent) => {
      sent()
      try {
        await this.sends.comment(ticket, body, opId)
        return { how: 'confirmed' }
      } catch (e) {
        return receiptFailure(e) === 'refused' ? { how: 'refused', words: `Not commented: ${dispatchFailureWords(e)}` } : { how: 'unconfirmed' }
      }
    })
  }

  private answer(it: InboxItem, a: InboxAction, text?: string): Promise<ActCame> {
    const reqId = this.ids.request()
    const entry: RecentEntry = { ref: it.ref, receipt: a.expect, attempt: reqId, at: this.now(), title: it.title, label: text === undefined ? a.label : 'A line' }
    return this.send(
      entry,
      it.ref,
      entry,
      a.label,
      async (sent) => {
        // The session's kind first: nothing goes while it is not known (a hosted session's answer goes through its relay).
        const s = await this.sends.session(refParts(it.ref).a)
        if (!s) return { how: 'not_sent', words: NOT_SENT_SESSION }
        sent()
        try {
          const out = await this.sends.answer(s, text === undefined ? { input: a.input ?? '' } : { text }, { question: a.expect, reqId })
          if (out === 'not_sent') return { how: 'not_sent', words: NOT_SENT_BUSY }
          return out === 'taken' ? { how: 'confirmed' } : { how: 'unconfirmed' }
        } catch (e) {
          const how = answerFailure(e)
          if (how === 'unconfirmed') return { how }
          // A refusal in the owner's own words (a question set aside refuses with the same words as one answered): the step says
          // what the owner said, never a guess at why.
          const said = how === 'refused' && e instanceof ReplyError ? e.message : ''
          if (said) return { how, words: said }
          const f = replyFailure(e, s.name || undefined)
          return { how, words: [f.title, f.description].filter(Boolean).join(': ') }
        }
      },
      (came, steps) => (came.how === 'refused' ? whereToAnswer(steps) : ''),
    )
  }

  private command(it: InboxItem, a: InboxAction): Promise<ActCame | null> {
    const m = /\/api\/runs\/([^/]+)\/tasks\/([^/]+)\/(retry|accept|cancel)$/.exec(a.route)
    if (!m) return Promise.resolve(null)
    const [, run, task, name] = m as unknown as [string, string, string, TaskCommand]
    const commandId = this.ids.command()
    const entry: RecentEntry = { ref: it.ref, receipt: commandId, at: this.now(), title: it.title, label: a.label, ...(a.track && a.track !== it.ref ? { track: a.track } : {}) }
    return this.send(entry, it.ref, entry, a.label, async (sent) => {
      sent()
      try {
        await this.sends.command(run, task, name, { expectedRevision: Number(a.expect) || undefined, commandId })
        return { how: 'confirmed' }
      } catch (e) {
        if (receiptFailure(e) !== 'refused') return { how: 'unconfirmed' }
        return { how: 'refused', words: isStale(e) ? await this.sends.movedOn(it, a) : (e as Error).message }
      }
    })
  }

  private plan(it: InboxItem, a: InboxAction): Promise<ActCame | null> {
    const id = planIdOf(a.route)
    if (!id) return Promise.resolve(null)
    const opId = this.ids.command()
    const entry: RecentEntry = { ref: it.ref, receipt: opId, at: this.now(), title: it.title, label: a.label }
    return this.send(entry, it.ref, entry, a.label, async (sent) => {
      sent()
      try {
        await this.sends.plan(a, id, { what: it.title, expectedRev: Number(a.expect) || undefined, opId })
        return { how: 'confirmed' }
      } catch (e) {
        if (receiptFailure(e) !== 'refused') return { how: 'unconfirmed' }
        return { how: 'refused', words: isStale(e) ? await this.sends.movedOn(it, a) : (e as Error).message }
      }
    })
  }

  /**
   * One send: the item's feedback says it is out; `go` calls `sent` just before its request leaves the browser, which keeps the
   * action in the recent list before its reply can come back; then the outcome is written into the entry (or the entry dropped, for
   * a request that never left), the action's track read, and the item's feedback set, only while it still names this action.
   */
  private async send(
    entry: RecentEntry | null,
    ref: string,
    key: { receipt: string; attempt?: string },
    label: string,
    go: (sent: () => void) => Promise<ActCame>,
    where?: (came: ActCame, steps: DeliveryStep[] | undefined) => string,
  ): Promise<ActCame> {
    const own = (f: ActFeedback | undefined) => !!f && f.receipt === key.receipt && f.attempt === key.attempt
    this.set(ref, { receipt: key.receipt, ...(key.attempt ? { attempt: key.attempt } : {}), busy: label, words: '', unknown: '' })
    let left = false
    let came: ActCame
    try {
      came = await go(() => {
        left = true
        if (entry) this.live.remember(entry)
      })
    } catch {
      // A failure no path above read as a refusal: unknown if anything left, else nothing sent.
      came = left ? { how: 'unconfirmed' } : { how: 'not_sent', words: 'Not sent.' }
    }
    let steps: DeliveryStep[] | undefined
    if (entry) {
      if (came.how === 'not_sent') {
        if (left) this.live.forget(entry)
      } else {
        this.live.outcome(entry.receipt, entry.attempt, came.how === 'confirmed' ? 'accepted' : came.how === 'refused' ? `refused: ${came.words}` : UNCONFIRMED)
        steps = await this.live.trackNow(entry)
      }
    }
    const f = this.fb[ref]
    if (own(f)) {
      const said = came.how === 'refused' || came.how === 'not_sent' ? came.words.charAt(0).toUpperCase() + came.words.slice(1) : ''
      const there = where?.(came, steps) ?? ''
      const words = said && there ? `${/[.!?]$/.test(said) ? said : `${said}.`} ${there}` : said
      const unknown =
        !entry && came.how === 'unconfirmed' ? 'Your comment went, and no receipt came back to say whether Dispatch took it: its ticket shows whether it is there; comment again only if it is not.' : ''
      this.set(ref, { ...f!, busy: '', words, unknown })
    }
    return came
  }

  private set(ref: string, f: ActFeedback) {
    this.fb = { ...this.fb, [ref]: f }
    this.apply(this.fb)
  }
}

/**
 * The decision page's sends, wired: the session from the stream's listing or read by its id, an answer through useQuickReply (the
 * transport the session's kind needs), a command through useTaskCommands, a plan's operation and a comment through
 * useDispatchOps. `feedback` is each item's (ActFeedback), for the item shown.
 */
export function useInboxActs() {
  const inbox = useInbox()
  const live = useAttention()
  const api = useSessions()
  const dispatch = useDispatchApi()
  const quick = useQuickReply()
  const ops = useDispatchOps(new DispatchOrder())
  const commands = useTaskCommands(() => '')
  const feedback = ref<ActsFeedback>({})

  const acts = new InboxActs(
    inbox.live,
    {
      session: (id) => sessionFor(live.sessions.value, id, async (x) => (await api.get(x)).session),
      answer: (s, what, o) => quick.answer(s, what, o),
      command: async (run, task, name, o) => {
        let failed: unknown = null
        const ok = await commands.send({ id: task }, name, { ...o, run, refused: (e) => (failed = e) })
        if (!ok) throw failed
      },
      plan: (a, id, o) =>
        ops.run((body) => (a.name === 'approve' ? dispatch.approvePlan(id, { ...body, ticketRev: a.ticketRev }) : dispatch.withdrawPlan(id, body)), {
          thing: `plan:${id}`,
          what: o.what,
          expectedRev: o.expectedRev,
          opId: o.opId,
          raise: true,
          done: a.name === 'approve' ? 'Approved: launching is a separate step on the ticket page' : 'Withdrawn',
        }),
      comment: (ticket, body, opId) => ops.run((b) => dispatch.addComment(ticket, { ...b, body }), { thing: `ticket:${ticket}`, what: ticket, opId, raise: true, done: `Commented on ${ticket}` }),
      movedOn: async (it, a) => {
        if (it.kind === 'plan') return movedOnWords('plan', await api.inboxItem(it.ref).catch(() => null))
        const m = /\/api\/runs\/([^/]+)\/tasks\/([^/]+)\//.exec(a.route)
        const r = m ? await live.refreshRun(m[1]!).catch(() => null) : null
        const t = r?.tasks?.find((x) => x.id === m?.[2])
        return `The run moved on${r?.managed ? ` (revision ${r.managed.revision} now, the choice was read at ${a.expect})` : ''}${t ? `: ${t.id} is ${t.state} now` : ''}. Nothing was done.`
      },
    },
    (f) => (feedback.value = f),
  )

  return {
    feedback,
    pending: (ref: string) => !!feedback.value[ref]?.busy,
    act: (it: InboxItem, a: InboxAction, text?: string) => acts.act(it, a, text),
    comment: (it: InboxItem, ticket: string, body: string) => acts.comment(it, ticket, body),
  }
}
