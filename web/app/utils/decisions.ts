import { mintCommandId } from './tasks'
import type { RefState } from './brakeman'

// Durable decisions (Program item DR1, docs/plans/decisions.md, "web/app"; docs/protocol.md, "Decisions"): the record as the operator's
// routes give it, its four states in words (open, response recorded, validated by Yardmaster, applied or why not), and the operator's
// writes (respond, cancel, comment) with the id minted here and a lost reply asked once more with the same id, as a run command is
// (utils/tasks.ts). The card (DecisionCard.vue) and its answer (DecisionRespond.vue) read these; so do the run page, the session's
// question bar, Needs you, the palette and the ticket page.

export type DecisionState = 'open' | 'answered' | 'applied' | 'superseded' | 'cancelled' | 'expired'
export type DecisionType = 'clarification' | 'approval' | 'assignment'

/** One of a request's alternatives. */
export interface DecisionOption {
  label: string
  tradeoffs?: string
}

/** What a decision is about: a commit, a candidate (a task's submitted result), a file's blob at a commit, or a plan revision (a gate's). */
export interface DecisionSubject {
  kind: 'commit' | 'candidate' | 'artifact' | 'plan'
  commit?: string
  run?: string
  task?: string
  n?: number
  path?: string
  sha256?: string
  plan?: string
  digest?: string
}

/** What was asked, as the record keeps it. */
export interface DecisionRequest {
  question: string
  context?: string
  options?: DecisionOption[]
  /** An option's index, or -1 (or absent) for none. */
  recommendation?: number
  uncertainty?: string
  authorizes?: string
  evidence?: string[]
  expertise?: string[]
  subject?: DecisionSubject | null
  supersedes?: string
}

/** A response's receipt, kept with it: accepted (Recorded), or refused with its code and words. */
export interface ResponseReceipt {
  decision: string
  k?: number
  status: 'accepted' | 'refused'
  code?: string
  words: string
  actor: string
  byName?: string
  choice?: number
  text?: string
  rationale?: string
  at: string
  rev: number
  repeat?: boolean
  /** False for a refusal past the decision's bound, which keeps no row and binds nothing. */
  kept?: boolean
}

/** Yardmaster's receipt of an answer applied. */
export interface DecisionApplication {
  at: string
  by: string
  run: string
  task?: string
  attempt?: number
  response: number
  validated: boolean
  how: 'typed' | 'task_file' | 'released' | 'cancelled' | 'noted'
  tasks?: string[]
  words: string
}

/** An operator operation's receipt (a comment, a request for more, a cancel, a route). */
export interface OpReceipt {
  decision: string
  op: string
  seq: number
  at: string
  rev: number
  words: string
  repeat?: boolean
}

/** A row of a decision's history. */
export interface DecisionEntry {
  seq: number
  at: string
  kind: string
  by?: string
  detail?: Record<string, unknown>
}

/** The record as `GET /api/decisions/{id}` gives it whole (the lists leave out `history` and `responses`). */
export interface Decision {
  id: string
  /** The run that holds it now. */
  run: string
  openedIn: string
  origin: 'agent' | 'plan'
  type: DecisionType
  gate?: string
  /** An agent's decision's asking attempt, and whether it is live (the same run, task, attempt and session, the session not ended). */
  asker?: { task: string; attempt: number; live: boolean }
  member?: string
  ticket?: string
  plan?: string
  planDigest?: string
  requestId?: string
  request: DecisionRequest
  policy: { revision?: string; approver?: string; quorum?: number }
  state: DecisionState
  rev: number
  why?: string
  supersededBy?: string
  /** The accepted response in words. */
  answer?: string
  responses?: ResponseReceipt[]
  applied?: DecisionApplication
  history?: DecisionEntry[]
  createdAt: string
  answeredAt?: string
  closedAt?: string
}

/** One evidence ref's resolution: its record when the question was asked (`seen`, the baseline), now, and what changed between them. */
export interface DecisionRef {
  ref: string
  seen: RefState
  now: RefState
  change?: 'changed' | 'deleted' | 'unread'
}

/** A decision as a run's or a task's read lists it (`decisions`): a summary, never the record. */
export interface DecisionSummary {
  id: string
  requestId?: string
  type: DecisionType
  origin: 'agent' | 'plan'
  task?: string
  gate?: string
  state: DecisionState
  rev: number
  question: string
  answer?: string
  why?: string
  supersededBy?: string
  createdAt: string
  answeredAt?: string
  closedAt?: string
  cut?: boolean
}

/** A decision a task's read names (`holds`): the decisions that hold it. */
export interface TaskHold {
  id: string
  type: DecisionType
  state: DecisionState
  question: string
}

const FINAL: readonly DecisionState[] = ['applied', 'superseded', 'cancelled', 'expired']

/** Whether a decision is final: applied, superseded, cancelled or expired. */
export function isFinal(state: DecisionState): boolean {
  return FINAL.includes(state)
}

/** The decision's options as the card offers them: an assignment's two fixed answers, else the request's. */
export function optionsOf(d: Pick<Decision, 'type' | 'request'>): DecisionOption[] {
  if (d.type === 'assignment') return [{ label: 'Done' }, { label: 'Not done' }]
  return d.request.options ?? []
}

/** The option the requester recommends, by its index; -1 for none. */
export function recommended(d: Pick<Decision, 'type' | 'request'>): number {
  const r = d.request.recommendation
  return typeof r === 'number' && r >= 0 && r < optionsOf(d).length ? r : -1
}

/** Who may answer it, from its policy: the operator, the free build's one approver. */
export function approverWords(d: Pick<Decision, 'policy'>): string {
  return d.policy?.approver === 'operator' || !d.policy?.approver ? 'the operator' : d.policy.approver
}

/** Where a decision was asked, in words: `build's attempt 1` for an agent's, `the plan's gate deploy-ok` for a gate. */
export function askedWhere(d: Pick<Decision, 'origin' | 'gate' | 'asker'>): string {
  if (d.origin === 'plan') return d.gate ? `the plan's gate ${d.gate}` : "the plan's gate"
  return d.asker ? `${d.asker.task}'s attempt ${d.asker.attempt}` : 'an agent'
}

const short = (c?: string) => (c ? c.slice(0, 7) : '')

/** The subject in words, and the page it links to when it has one (a candidate's task). */
export function subjectWords(s: DecisionSubject | null | undefined): { text: string; to?: string } {
  if (!s) return { text: '' }
  switch (s.kind) {
    case 'commit':
      return { text: `commit ${short(s.commit)}` }
    case 'candidate':
      return {
        text: `the result of ${s.task}, attempt ${s.n}${s.commit ? ` (${short(s.commit)})` : ''}`,
        to: s.run && s.task ? `/runs/${encodeURIComponent(s.run)}?view=tasks&task=${encodeURIComponent(s.task)}` : undefined,
      }
    case 'artifact':
      return { text: `${s.path} at ${short(s.commit)}${s.sha256 ? `, sha256 ${s.sha256.slice(0, 12)}` : ''}` }
    case 'plan':
      return { text: `plan revision ${s.plan ?? ''}${s.digest ? `, digest ${s.digest.slice(0, 12)}` : ''}`.trim() }
  }
  return { text: '' }
}

/** The accepted response's receipt, when the record carries its responses. */
export function acceptedReceipt(d: Pick<Decision, 'responses'>): ResponseReceipt | undefined {
  return d.responses?.find((r) => r.status === 'accepted')
}

/** One of the four states the card shows apart. */
export interface DecisionStep {
  key: 'open' | 'recorded' | 'validated' | 'applied'
  label: string
  /** Reached, waiting (the step under way), or not reached (and never will be, `ended`). */
  status: 'done' | 'waiting' | 'pending' | 'ended'
  words: string
  /** When it was reached. */
  at?: string
  /** The receipt it rests on: the response's `dr-7/1`, the application's `dr-7/1 applied`. */
  receipt?: string
}

/**
 * The state in words, and the four states shown apart (docs/plans/decisions.md, "web/app"): open; the response recorded (its receipt
 * and time); validated by Yardmaster; applied, or why not (superseded, cancelled, expired: the record's `why`). `runHeld` false says the
 * run that holds it is not running (stopped, interrupted, not continued): an answer recorded then waits for the run to be continued.
 */
export function decisionWords(d: Decision, o: { runHeld?: boolean } = {}): { state: string; tone: 'warning' | 'success' | 'muted' | 'error' | 'default'; steps: DecisionStep[] } {
  const rc = acceptedReceipt(d)
  const app = d.applied
  const recordedAt = rc?.at ?? d.answeredAt
  const answered = d.state === 'answered' || d.state === 'applied' || !!recordedAt
  const why = d.why ? `: ${d.why}` : ''
  const steps: DecisionStep[] = [{ key: 'open', label: 'Open', status: 'done', words: `asked by ${askedWhere(d)}`, at: d.createdAt }]
  const ended = d.state === 'superseded' || d.state === 'cancelled' || d.state === 'expired'
  if (answered) {
    const by = rc ? rc.byName || rc.actor : ''
    steps.push({ key: 'recorded', label: 'Response recorded', status: 'done', words: rc ? rc.words : `answered${by ? ` by ${by}` : ''}`, at: recordedAt, receipt: rc?.k ? `${d.id}/${rc.k}` : undefined })
  } else {
    steps.push({ key: 'recorded', label: 'Response recorded', status: ended ? 'ended' : 'waiting', words: ended ? 'no response was recorded' : `waits for ${approverWords(d)}'s answer` })
  }
  if (app?.validated) {
    steps.push({ key: 'validated', label: 'Validated by Yardmaster', status: 'done', words: 'the subject, the policy and the asker checked again', at: app.at, receipt: `${d.id}/${app.response}` })
    steps.push({ key: 'applied', label: 'Applied', status: 'done', words: app.words, at: app.at, receipt: `${d.id}/${app.response} applied` })
  } else if (ended) {
    steps.push({ key: 'validated', label: 'Validated by Yardmaster', status: 'ended', words: answered ? 'not validated' : 'nothing to validate' })
    steps.push({ key: 'applied', label: `Not applied: ${d.state}`, status: 'ended', words: (d.why || d.state) + (d.supersededBy ? `; ${d.supersededBy} takes its place` : ''), at: d.closedAt })
  } else if (answered) {
    const waits = o.runHeld === false ? 'waiting for the run to be continued' : 'Yardmaster validates it next'
    steps.push({ key: 'validated', label: 'Validated by Yardmaster', status: 'waiting', words: waits })
    steps.push({ key: 'applied', label: 'Applied', status: 'pending', words: 'not yet' })
  } else {
    steps.push({ key: 'validated', label: 'Validated by Yardmaster', status: 'pending', words: 'not yet' })
    steps.push({ key: 'applied', label: 'Applied', status: 'pending', words: 'not yet' })
  }
  // An approval applied and later superseded (its subject moved): the application stays in the steps, the state says what moved.
  if (app && d.state === 'superseded') steps[3] = { ...steps[3]!, label: 'Applied, then superseded', words: `${app.words}; superseded${why}` }
  switch (d.state) {
    case 'open':
      return { state: `Open: waits for ${approverWords(d)}'s answer`, tone: 'warning', steps }
    case 'answered':
      return { state: o.runHeld === false ? 'Recorded: waiting for the run to be continued' : 'Recorded: Yardmaster applies it next', tone: 'default', steps }
    case 'applied':
      return { state: `Applied: ${app?.words ?? 'done'}`, tone: 'success', steps }
    case 'superseded':
      return { state: `Superseded${why}${d.supersededBy ? ` (${d.supersededBy} takes its place)` : ''}`, tone: 'muted', steps }
    case 'cancelled':
      return { state: `Cancelled${why}`, tone: 'muted', steps }
    case 'expired':
      return { state: `Expired${why}`, tone: 'muted', steps }
  }
  return { state: d.state, tone: 'muted', steps }
}

/**
 * Whether Ask for more is offered, and when it is not, why, in the words the server refuses it with (`409 no_recipient`): only an open
 * request of an agent whose asking attempt is live has someone to ask.
 */
export function askForMore(d: Pick<Decision, 'id' | 'origin' | 'state' | 'asker'>): { enabled: boolean; why: string } {
  if (d.origin === 'plan') return { enabled: false, why: `${d.id} is the plan's: no agent asked it, comment instead` }
  if (d.state !== 'open') return { enabled: false, why: `${d.id} is ${d.state}` }
  if (!d.asker?.live) return { enabled: false, why: `${d.id}'s agent has ended its attempt: comment, answer or cancel it` }
  return { enabled: true, why: '' }
}

/** A response as the respond route takes it. */
export interface ResponseBody {
  responseId: string
  choice?: number
  text?: string
  rationale?: string
  expectedRev: number
  byName?: string
}

/** A cancel as the cancel route takes it. */
export interface CancelBody {
  commandId: string
  why: string
  release?: boolean
  expectedRev: number
}

/** A comment, or a request for more information, as the comment route takes it. */
export interface CommentBody {
  opId: string
  kind: 'comment' | 'more_info'
  text: string
  expectedRev: number
}

/** A write's refusal: the server's code and words, and the receipt it kept when it kept one (a response refused after reaching the record). */
export class DecisionRefused extends Error {
  constructor(
    public code: string,
    message: string,
    public status: number,
    public receipt?: ResponseReceipt | OpReceipt,
  ) {
    super(message)
  }
}

/** Whether a reply was lost (no answer reached the page), or the server asks for the write once more (`503 decision_moved`). */
function askAgain(e: unknown): boolean {
  const err = e as { status?: number; code?: string }
  return err.status === 0 || (err.status === 503 && err.code === 'decision_moved')
}

/** The words of a refused write: the server's, which say what to do next, or the error's own. */
export function refusalOf(e: unknown): DecisionRefused {
  if (e instanceof DecisionRefused) return e
  const err = e as { status?: number; code?: string; message?: string; body?: { error?: { code?: string; message?: string }; receipt?: ResponseReceipt | OpReceipt } }
  const code = err.body?.error?.code ?? err.code ?? 'error'
  const words = err.body?.error?.message ?? err.message ?? 'refused'
  return new DecisionRefused(code, words, err.status ?? 0, err.body?.receipt)
}

/**
 * Sends a write with its body, whose id was minted once (the caller's body), and resolves to the reply. A reply lost to the network,
 * or a `503 decision_moved` (the decision moved to the run that continues its own while the write went), is asked for once more with
 * the same body, so the same id, which the server answers as the first (the receipt, `repeat`). Any other refusal, and a second loss,
 * is a DecisionRefused with the server's words and its kept receipt.
 */
export async function sendDecisionWrite<B, R>(send: (body: B) => Promise<R>, body: B): Promise<R> {
  for (let attempt = 1; ; attempt++) {
    try {
      return await send(body)
    } catch (e) {
      if (attempt === 1 && askAgain(e)) continue
      throw refusalOf(e)
    }
  }
}

/** A response's body with its id minted: `choice` an option's index, `text` a clarification's words; `expectedRev` the rev the card read. */
export function responseBody(r: { choice?: number; text?: string; rationale?: string; expectedRev: number; byName?: string }, responseId = mintCommandId()): ResponseBody {
  const body: ResponseBody = { responseId, expectedRev: r.expectedRev }
  if (r.choice !== undefined) body.choice = r.choice
  if (r.text?.trim()) body.text = r.text.trim()
  if (r.rationale?.trim()) body.rationale = r.rationale.trim()
  if (r.byName?.trim()) body.byName = r.byName.trim()
  return body
}

/** Answers decision `id`: the body (responseBody) is minted once by the caller, and sent once more with it on a lost reply. */
export function respondDecision(post: (path: string, body: ResponseBody) => Promise<{ receipt: ResponseReceipt; decision: Decision }>, id: string, body: ResponseBody) {
  return sendDecisionWrite((b: ResponseBody) => post(`/api/decisions/${encodeURIComponent(id)}/respond`, b), body)
}

/** Cancels decision `id`, its gate's tasks released when `release` says so, else cancelled with it. */
export function cancelDecision(post: (path: string, body: CancelBody) => Promise<{ receipt: OpReceipt; decision: Decision }>, id: string, c: { why: string; release?: boolean; expectedRev: number }, commandId = mintCommandId()) {
  const body: CancelBody = { commandId, why: c.why.trim(), expectedRev: c.expectedRev }
  if (c.release) body.release = true
  return sendDecisionWrite((b: CancelBody) => post(`/api/decisions/${encodeURIComponent(id)}/cancel`, b), body)
}

/** Keeps a comment on decision `id`, or asks its agent for more information (`more_info`). */
export function commentDecision(post: (path: string, body: CommentBody) => Promise<{ receipt: OpReceipt; decision: Decision }>, id: string, c: { kind: 'comment' | 'more_info'; text: string; expectedRev: number }, opId = mintCommandId()) {
  return sendDecisionWrite((b: CommentBody) => post(`/api/decisions/${encodeURIComponent(id)}/comment`, b), { opId, kind: c.kind, text: c.text.trim(), expectedRev: c.expectedRev })
}

/** Whether s names a decision: `dr-<n>`. */
export function isDecisionId(s: string | undefined | null): s is string {
  return !!s && /^dr-[1-9][0-9]{0,17}$/.test(s)
}

/** The decision ids the shown session questions mirror (an attention needing input with `decision`). */
export function mirroredIds(sessions: ReadonlyArray<{ attention?: { state?: string; decision?: string } }>): Set<string> {
  const out = new Set<string>()
  for (const s of sessions) if (s.attention?.state === 'needs_input' && s.attention.decision) out.add(s.attention.decision)
  return out
}

/**
 * The open decisions no shown session question mirrors (a gate's, or an agent's set aside while it works), of a run that runs (`runs`
 * says which: held by the server, not stopped, not continued): Needs you counts each once. A stopped run's open decision waits for the
 * run as much as for the operator, and is answered from its run page, its ticket page or the palette.
 */
export function unmirrored<D extends Pick<Decision, 'id' | 'state' | 'run'>>(decisions: readonly D[], sessions: ReadonlyArray<{ attention?: { state?: string; decision?: string } }>, runs: (id: string) => boolean): D[] {
  const shown = mirroredIds(sessions)
  return decisions.filter((d) => d.state === 'open' && !shown.has(d.id) && runs(d.run))
}

/** Whether a run, as the live store holds it, runs: the server holds it, and it is not stopped or continued by another. */
export function runRuns(r: { stoppedAt?: string; record?: boolean; resumedBy?: string } | undefined): boolean {
  return !!r && !r.stoppedAt && !r.record && !r.resumedBy
}

/** The palette's rows: one per open decision, "Answer dr-7: <question>", oldest first; Enter (`open`) opens its card. */
export function answerRows(decisions: ReadonlyArray<Pick<Decision, 'id' | 'state' | 'request' | 'origin' | 'gate' | 'asker' | 'createdAt'>>, open: (id: string) => void) {
  return decisions
    .filter((d) => d.state === 'open')
    .slice()
    .sort((a, b) => a.createdAt.localeCompare(b.createdAt) || a.id.localeCompare(b.id))
    .map((d) => ({ key: `decision:${d.id}`, label: `Answer ${d.id}: ${d.request.question}`, detail: `asked by ${askedWhere(d)}`, icon: 'i-lucide-scale', run: () => open(d.id) }))
}

/** The words a history row is shown with: its kind in words, and its text when it has one. */
export function historyWords(h: DecisionEntry): string {
  const text = typeof h.detail?.text === 'string' ? h.detail.text : typeof h.detail?.why === 'string' ? h.detail.why : typeof h.detail?.note === 'string' ? h.detail.note : ''
  const kind = HISTORY_KINDS[h.kind] ?? h.kind
  return text ? `${kind}: ${text}` : kind
}

const HISTORY_KINDS: Record<string, string> = {
  opened: 'opened',
  routed: 'routed',
  notified: 'notified',
  responded: 'response',
  applied: 'applied',
  refunded: 'attempt given back',
  'delivery.typing': 'being typed',
  'delivery.entered': 'typed in',
  'delivery.untyped': 'not typed',
  'delivery.uncertain': 'typing uncertain',
  'delivery.task_file': 'in the task file',
  commented: 'comment',
  info_requested: 'asked for more',
  superseded: 'superseded',
  cancelled: 'cancelled',
}
