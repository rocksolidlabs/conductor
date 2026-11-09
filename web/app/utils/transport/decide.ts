// A decision link's lane, the client's side (docs/plans/decision-inbox.md, PR-7; docs/protocol.md, Decision links): over the
// connection a decision link opens (a session's viewer, or a run's desk), the join page's Decide tab lists the items in the link's
// scope, reads one, unfolds its evidence, reads the four steps of what it did and acts on one. The wire is the transport's
// (BaseTransport.laneRequest and decide); this client keeps the lane's own bound, at most 4 reads and 4 decides under way, by
// queueing what passes it rather than having the owner refuse it, and turns each reply into the inbox's own types, so the same
// cards (InboxItem) draw what the lane brings. Pure but for the wire it is given.

import {
  DecideCode,
  MAX_DECIDE_IN_FLIGHT,
  MAX_DECISION_READS_IN_FLIGHT,
  type DecideMsg,
  type DecideOkMsg,
  type DecisionReply,
  type DecisionRequest,
} from '../protocol'
import type { DeliveryStep, InboxAction, InboxItem, InboxSummary } from '../inbox'
import type { CommentBody, Decision, DecisionEntry, DecisionRef, DecisionType, ResponseBody, ResponseReceipt } from '../decisions'

/** A lane request as the client asks it: the transport names it (`reqId`). */
export type LaneAsk = DecisionRequest extends infer R ? (R extends DecisionRequest ? Omit<R, 'reqId'> : never) : never
/** A decide as the client asks it: the transport names it (`reqId`). */
export type LaneDecide = Omit<DecideMsg, 'reqId'>

/** What carries the lane: the transport of the connection the link opened, while its welcome said `decisions`. */
export interface LaneWire {
  laneRequest(m: LaneAsk): Promise<DecisionReply>
  decide(m: LaneDecide): Promise<DecideOkMsg>
}

/** Codes the client gives a request that got no reply of the owner's (beside the owner's own, DecideCode). */
export const LaneLocal = {
  /** The connection closed before the reply: a decide may have been taken. */
  Closed: 'closed',
  /** No reply in time: a decide may have been taken. */
  NoWord: 'no_word',
  /** Nothing was sent: no connection, or one whose welcome did not say `decisions`. */
  NotSent: 'not_sent',
} as const

/** A lane request the owner refused (`decide_refused`, its code and words), or one that got no reply (LaneLocal). */
export class LaneError extends Error {
  constructor(
    readonly code: string,
    message: string,
    readonly extra: { rev?: string; ref?: string; receipt?: string } = {},
  ) {
    super(message)
    this.name = 'LaneError'
  }
}

/** Whether a decide that failed with e may have been taken: it went, and no word of the owner's says what came of it. */
export function decideUnconfirmed(e: unknown): boolean {
  return e instanceof LaneError && (e.code === LaneLocal.Closed || e.code === LaneLocal.NoWord)
}

/** An operation's own refusal (docs/protocol.md, Decision links): the owner could not read what it needed, a receipt it kept among them. */
export const OWNER_UNAVAILABLE = 'unavailable'

/** Owner refusals that say nothing of the action asked under the opId: not looked at (too many), or what was kept of it unread. */
const SAYS_NOTHING = new Set<string>([DecideCode.TooManyRequests, OWNER_UNAVAILABLE])
/** Refusals of the link a replay gives before it reads what the link kept of the opId (Server.replay holds the link first). */
const LINK_FIRST = new Set<string>([DecideCode.Revoked, DecideCode.Expired])
const LOCAL = new Set<string>(Object.values(LaneLocal))

/** What one send of an action makes of it: a reply naming its request confirmed it, or refused it; anything else leaves it owed. */
export type DecideOutcome = 'confirmed' | 'refused' | 'owed'

/**
 * The one rule of an action taken through a link (docs/plans/decision-inbox.md, PR-7): `decide_ok` confirms it and the owner's
 * refusal of it refuses it; everything else leaves it owed, to be asked again under its own opId: no reply (`closed`, `no_word`),
 * nothing sent (`not_sent`), a refusal that says nothing of it (too many under way, `unavailable`: a kept receipt the owner could
 * not read), and, once an earlier send of it may have been taken (`went`), the link's refusal a replay gives before it looks. `e`
 * is null for a `decide_ok`.
 */
export function decideOutcome(e: unknown, went: boolean): DecideOutcome {
  if (e === null) return 'confirmed'
  if (!(e instanceof LaneError) || LOCAL.has(e.code) || SAYS_NOTHING.has(e.code)) return 'owed'
  if (went && LINK_FIRST.has(e.code)) return 'owed'
  return 'refused'
}

/** A page of the scope's items, as `decisions_list` answers it; the first page carries the link's Recent. */
export interface LanePage {
  items: InboxSummary[]
  next?: string
  total: number
  at: string
  recent: LaneRecent[]
}

/** One action taken through the link (its Recent): the item, what its steps are read by, the action's own identity. */
export interface LaneRecent {
  ref: string
  receipt: string
  attempt: string
  at: string
}

/** The four steps of one action taken through the link. */
export interface LaneTrack {
  ref: string
  receipt: string
  attempt: string
  steps: DeliveryStep[]
}

/** An evidence entry read through the link: a check's log's end, or the published id of a session in the scope. */
export type LaneEvidence =
  | { ref: string; kind: 'log'; tail: string; cut?: boolean; bytes?: number; sha256?: string; exit: number; dropped?: boolean }
  | { ref: string; kind: 'session'; session: string }

function asArray<T>(v: unknown): T[] {
  return Array.isArray(v) ? (v as T[]) : []
}

/** A list reply as the page draws it: lists the server leaves out as empty ones. */
export function pageOf(v: unknown): LanePage {
  const p = (v ?? {}) as Partial<LanePage>
  return {
    items: asArray<InboxSummary>(p.items).map((s) => ({ ...s, actions: asArray<string>(s.actions) })),
    ...(p.next ? { next: p.next } : {}),
    total: typeof p.total === 'number' ? p.total : 0,
    at: typeof p.at === 'string' ? p.at : '',
    recent: asArray<LaneRecent>(p.recent),
  }
}

/**
 * The decide for an action of an item, as the frame carries it: the item's ref, the action's name, its `expect` as the revision
 * (and a plan's ticket revision), a question's choice keys exactly or its line, and the action's own identity, `opId`. The actor is
 * the link, never a field. Nothing else is sent: which fields an action takes is the owner's to judge.
 */
export function decideOf(item: Pick<InboxItem, 'ref'>, a: InboxAction, opId: string, text?: string): LaneDecide {
  const m: LaneDecide = { ref: item.ref, action: a.name, revision: a.expect, opId }
  if (a.ticketRev !== undefined) m.ticketRev = a.ticketRev
  if (a.input !== undefined) m.input = a.input
  if (a.text && text !== undefined) m.text = text
  return m
}

/**
 * A durable decision's response as DecisionRespond.vue builds it (ResponseBody, DR1's respond body), as the frame carries it
 * (docs/plans/decision-inbox.md, PR-4; docs/protocol.md, Decision links, Decide): field for field, nothing dropped or merged but the
 * name a person typed (`byName`), which a link never sends, its actor and name being the link's. `responseId` is the decide's `opId`
 * (DR1 keys the response by it, so a repeat is its receipt), `expectedRev` its `revision`, `choice`, `text` and `rationale` as they
 * are; the action is `approve` for an approval's first option and `reject` for its second (the owner holds an approve to choice 0
 * and a reject to choice 1), `answer` for any other. No `input`: terminal keys are a question's alone.
 */
export function decideOfResponse(item: Pick<InboxItem, 'ref'>, type: DecisionType, body: ResponseBody): LaneDecide {
  const action = type === 'approval' && body.choice === 0 ? 'approve' : type === 'approval' && body.choice === 1 ? 'reject' : 'answer'
  const m: LaneDecide = { ref: item.ref, action, revision: String(body.expectedRev), opId: body.responseId }
  if (body.choice !== undefined) m.choice = body.choice
  if (body.text !== undefined) m.text = body.text
  if (body.rationale !== undefined) m.rationale = body.rationale
  return m
}

/** A comment on a durable decision, or a request for more information (CommentBody, DR1's comment body), as the frame carries it. */
export function decideOfComment(item: Pick<InboxItem, 'ref'>, body: CommentBody): LaneDecide {
  return { ref: item.ref, action: body.kind, revision: String(body.expectedRev), opId: body.opId, text: body.text }
}

/**
 * The words a refused action shows on its card, from the owner's code and message: what moved on, that nothing was done, and where
 * to go on. The owner's own words come with any code it names alone (an operation's), so nothing it said is lost.
 */
export function refusedWords(e: unknown, kind: string): string {
  if (!(e instanceof LaneError)) return (e as Error)?.message || 'Not taken.'
  switch (e.code) {
    case DecideCode.StaleRevision:
      return `${kind === 'question' ? 'The question moved on' : 'The run moved on'}. Nothing was done.`
    case DecideCode.QuestionAnswered:
      return 'The question no longer waits for this answer (another answer came first, or it is held for the terminal). Nothing was typed.'
    case DecideCode.NotInScope:
      return 'This link does not cover that item now. Nothing was done.'
    case DecideCode.Revoked:
    case DecideCode.Expired:
      return 'This link no longer stands. Nothing was done.'
    case DecideCode.TooManyRequests:
      return 'Too many requests are under way on this link: wait for one to come back, then act again.'
    case DecideCode.OpConflict:
      return 'That request id named another request on this link. Nothing was done.'
    case LaneLocal.NotSent:
      return 'Not connected: nothing was sent.'
  }
  return e.message || e.code
}

/** One part of a DR1 record as `decision_record` answers it (docs/protocol.md, Decision links, Record). */
export interface LaneRecordPart {
  ref: string
  part: 'head' | 'responses' | 'history'
  /** The record's mark when the part was cut: its rev, its responses' count and its history rows', digits only. */
  rev: string
  /** The head's: the record without its responses, history and answer. */
  decision?: Decision
  refs?: DecisionRef[]
  /** The responses' first page's: the accepted response in words. */
  answer?: string
  /** Newest first, a page of them. */
  responses?: ResponseReceipt[]
  history?: DecisionEntry[]
  /** The offset of the next page, when more follow. */
  next?: string
  /** How many the part holds. */
  total: number
}

/** A DR1 record assembled from the parts of one mark: the record and refs `GET /api/decisions/{did}` gives at that mark. */
export interface AssembledRecord {
  ref: string
  rev: string
  decision: Decision
  refs: DecisionRef[]
}

/** How many times in a row an assembly starts again from the head on `record_moved` before it gives up until the next change. */
export const RECORD_RESTARTS = 4
/**
 * The most pages one part is read in: the derived pages hold at least 3 receipts (at the 32 KiB receipt bound) of a record's 32 and 32
 * history rows of its 256, so a part of more pages than this is no record's; it bounds a reply that would page for ever.
 */
export const RECORD_PAGES = 64

/** The words the card shows beside a record that changed faster than it was read (the last one assembled whole, or none). */
export const RECORD_MOVED_WORDS = 'This decision changed while it was read; reading again on its next change.'

/** What an assembly came to: the record whole at one mark; `moved`, after RECORD_RESTARTS restarts; `dropped`, a later one began. */
export type Assembly = { whole: AssembledRecord } | { moved: true } | { dropped: true }

const DROPPED = Symbol('dropped')

/**
 * Assembles DR1's record `ref` from its parts over the lane (docs/plans/decision-inbox.md, PR-3 and PR-4, "One record, one revision"):
 * the head, then every page of the responses and of the history, each asked at the mark the head carried, so DR1's card is handed only a
 * record every part of which was cut at one mark, never an open head beside an applied history. A part at another mark (`record_moved`
 * from the owner, or a part whose own mark or name is not the one asked) drops what was assembled and starts again from the head; after
 * RECORD_RESTARTS restarts in a row it gives up (`moved`) until the next change. `live` says whether this assembly is still the one
 * wanted (another item opened, or a later change asked a new one): once it says no, nothing more is asked and nothing it read is
 * handed on. Any other refusal or failure is thrown.
 */
export async function assembleRecord(ref: string, read: (part: LaneRecordPart['part'], rev?: string, after?: string) => Promise<LaneRecordPart>, live: () => boolean = () => true): Promise<Assembly> {
  const want = (p: LaneRecordPart, part: LaneRecordPart['part'], rev: string) => {
    if (!live()) throw DROPPED
    if (p.part !== part || p.ref !== ref || p.rev !== rev) throw new LaneError(DecideCode.RecordMoved, 'the part was cut at another mark', { rev: p.rev })
  }
  async function pages<T>(part: 'responses' | 'history', rev: string, items: (p: LaneRecordPart) => T[] | undefined): Promise<{ items: T[]; answer?: string }> {
    const out: T[] = []
    let answer: string | undefined
    let after: string | undefined
    let total = 0
    for (let i = 0; ; i++) {
      if (i >= RECORD_PAGES) throw new LaneError(DecideCode.Invalid, `the record's ${part} has more pages than a record holds`)
      const p = await read(part, rev, after)
      want(p, part, rev)
      if (i === 0) {
        answer = p.answer
        total = p.total
      }
      out.push(...(items(p) ?? []))
      if (!p.next) break
      const n = Number(p.next)
      if (!Number.isInteger(n) || n <= Number(after ?? 0) || n > total || String(n) !== p.next) throw new LaneError(DecideCode.Invalid, `the record's ${part} gave a page cursor that is no offset past the last`)
      after = p.next
    }
    if (out.length !== total) throw new LaneError(DecideCode.RecordMoved, `the record's ${part} were read as ${out.length} of ${total}`)
    return { items: out, answer }
  }
  try {
    for (let restarts = 0; ; restarts++) {
      try {
        const head = await read('head')
        if (!live()) throw DROPPED
        if (head.part !== 'head' || head.ref !== ref || !head.decision || !head.rev || `decision:${head.decision.id}` !== ref) throw new LaneError(DecideCode.Invalid, 'the head names another record')
        const rev = head.rev
        const responses = await pages('responses', rev, (p) => p.responses)
        const history = await pages('history', rev, (p) => p.history)
        // Newest first on the lane; the route gives them in the order they were kept.
        const decision: Decision = { ...head.decision, responses: [...responses.items].reverse(), history: [...history.items].reverse() }
        if (responses.answer) decision.answer = responses.answer
        else delete decision.answer
        return { whole: { ref, rev, decision, refs: head.refs ?? [] } }
      } catch (e) {
        if (!(e instanceof LaneError) || e.code !== DecideCode.RecordMoved) throw e
        if (restarts >= RECORD_RESTARTS) return { moved: true }
      }
    }
  } catch (e) {
    if (e === DROPPED) return { dropped: true }
    throw e
  }
}

type Waiting<T> = { run: () => Promise<T>; resolve: (v: T) => void; reject: (e: unknown) => void }

/** At most `max` calls under way; the others wait their turn, in order. */
class Gate {
  private running = 0
  private queue: Array<Waiting<unknown>> = []
  constructor(private readonly max: number) {}

  run<T>(f: () => Promise<T>): Promise<T> {
    return new Promise<T>((resolve, reject) => {
      this.queue.push({ run: f, resolve: resolve as (v: unknown) => void, reject })
      this.next()
    })
  }

  private next() {
    while (this.running < this.max && this.queue.length) {
      const w = this.queue.shift()!
      this.running++
      w.run()
        .then(w.resolve, w.reject)
        .finally(() => {
          this.running--
          this.next()
        })
    }
  }
}

/**
 * The lane's client over the connection `wire` gives (null while there is none, or its welcome did not say `decisions`): each read
 * and decide answered by its own reply, the reads and the decides each at most four under way (the owner refuses a fifth, so the
 * client holds it back instead).
 */
export class DecisionLane {
  private reads = new Gate(MAX_DECISION_READS_IN_FLIGHT)
  private decides = new Gate(MAX_DECIDE_IN_FLIGHT)

  constructor(private readonly wire: () => LaneWire | null) {}

  private ask(m: LaneAsk): Promise<DecisionReply> {
    return this.reads.run(() => {
      const w = this.wire()
      if (!w) return Promise.reject(new LaneError(LaneLocal.NotSent, 'not connected'))
      return w.laneRequest(m)
    })
  }

  /** A page of the scope's items, oldest first, after the cursor `after` (the `next` of the page before). */
  async list(after?: string): Promise<LanePage> {
    const r = await this.ask(after ? { t: 'decisions_list', after } : { t: 'decisions_list' })
    return pageOf(r.list)
  }

  /** One item with its detail, open or as it is now. */
  async read(ref: string): Promise<InboxItem> {
    const r = await this.ask({ t: 'decision_read', ref })
    if (!r.item || typeof r.item !== 'object') throw new LaneError(DecideCode.NotFound, 'no such item')
    return r.item as InboxItem
  }

  /** The four steps of one action this link took: its item, its receipt and its attempt (the decide's `opId`). */
  async track(ref: string, receipt: string, attempt: string): Promise<LaneTrack> {
    const r = await this.ask({ t: 'decision_track', ref, receipt, attempt })
    const t = (r.track ?? {}) as Partial<LaneTrack>
    return { ref: t.ref ?? ref, receipt: t.receipt ?? receipt, attempt: t.attempt ?? attempt, steps: asArray<DeliveryStep>(t.steps) }
  }

  /** One part of DR1's record `ref` (assembleRecord reads them all): `rev` the mark its head carried, `after` a page's `next`. */
  async record(ref: string, part: LaneRecordPart['part'], rev?: string, after?: string): Promise<LaneRecordPart> {
    const r = await this.ask({ t: 'decision_record', ref, part, ...(rev ? { rev } : {}), ...(after ? { after } : {}) })
    const p = r.record as LaneRecordPart | undefined
    if (!p || typeof p !== 'object' || typeof p.rev !== 'string') throw new LaneError(DecideCode.Invalid, 'no record part in the reply')
    return { ...p, total: typeof p.total === 'number' ? p.total : 0 }
  }

  /** A bounded read of the evidence entry `evidence` of the item `ref`; its reply names the entry it answers, checked here. */
  async evidence(ref: string, evidence: string): Promise<LaneEvidence> {
    const r = await this.ask({ t: 'decision_evidence', ref, evidence })
    const e = r.evidence as LaneEvidence | undefined
    if (!e || e.ref !== evidence) throw new LaneError(DecideCode.Invalid, 'the reply names another entry')
    return e
  }

  /** One action, through the item's own operation: its receipt, or a LaneError with the owner's code. */
  decide(m: LaneDecide): Promise<DecideOkMsg> {
    return this.decides.run(() => {
      const w = this.wire()
      if (!w) return Promise.reject(new LaneError(LaneLocal.NotSent, 'not connected'))
      return w.decide(m)
    })
  }
}
