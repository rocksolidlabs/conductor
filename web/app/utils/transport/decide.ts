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
