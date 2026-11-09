import { mintCommandId } from './tasks'
import type { RefState } from './brakeman'
import { RESPONSIBILITY_WORDS } from './crews'

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
  /** Whom it is for (HM1): the label as given, the person it named when it opened, and the responsibility (absent for a person by name). */
  for?: DecisionFor
}

/** Whom a decision is for, as its request keeps it (HM1); the operator answers it for that person. */
export interface DecisionFor {
  label: string
  person: string
  responsibility?: string
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
  /** Whom an accepted response answered for (HM1), beside who answered (actor, byName). */
  for?: { person: string; responsibility?: string }
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
export function approverWords(d: Pick<Decision, 'policy'> & { request?: Pick<DecisionRequest, 'for'> }): string {
  const who = d.policy?.approver === 'operator' || !d.policy?.approver ? 'the operator' : d.policy.approver
  const f = d.request?.for
  if (!f?.person) return who
  const w = respWords(f.responsibility)
  return w ? `${who}, for ${f.person} (${w})` : `${who}, for ${f.person}`
}

/** Whom a decision is for, or a receipt answered for, in words (HM1): "nate, the architecture owner", or "ana" for a person by name. '' for none. */
export function forWords(f: { person?: string; responsibility?: string } | null | undefined): string {
  if (!f?.person) return ''
  const w = respWords(f.responsibility)
  return w ? `${f.person}, the ${w}` : f.person
}

/** A responsibility in words, '' for none or one not of the four. */
function respWords(r: string | undefined): string {
  return r && r in RESPONSIBILITY_WORDS ? RESPONSIBILITY_WORDS[r as keyof typeof RESPONSIBILITY_WORDS] : ''
}

/** What the confirm step says of whom the operator answers for (HM1): "You answer as the operator, for nate"; '' for a decision for no one. */
export function answerAsWords(d: { request?: Pick<DecisionRequest, 'for'> }): string {
  const f = d.request?.for
  return f?.person ? `You answer as the operator, for ${f.person}` : ''
}

/** What an assignment's card says the person is to do (HM1): "nate is to do this by hand; the operator answers Done or Not done for them". */
export function assignmentWords(d: Pick<Decision, 'type'> & { request?: Pick<DecisionRequest, 'for'> }): string {
  const f = d.request?.for
  if (d.type !== 'assignment' || !f?.person) return ''
  return `${f.person} is to do this by hand; the operator answers Done or Not done for them`
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

/**
 * The accepted response as the card shows it once a decision is answered (Codex's round 1 on #719): the option picked (by its label,
 * an assignment's Done or Not done), the words a clarification was answered with, the rationale kept with it, and who answered; from
 * the accepted receipt when the record carries its responses, else from the record's `answer` (the lists' words) alone. Null while
 * nothing was accepted.
 */
export function acceptedAnswer(d: Pick<Decision, 'type' | 'request' | 'responses' | 'answer'>): { choice?: string; text?: string; rationale?: string; by?: string; for?: string; words?: string } | null {
  const rc = acceptedReceipt(d)
  if (rc) {
    const out: { choice?: string; text?: string; rationale?: string; by?: string; for?: string } = {}
    if (typeof rc.choice === 'number' && rc.choice >= 0) out.choice = optionsOf(d)[rc.choice]?.label ?? `option ${rc.choice + 1}`
    if (rc.text) out.text = rc.text
    if (rc.rationale) out.rationale = rc.rationale
    const by = rc.byName || rc.actor
    if (by) out.by = by
    if (rc.for?.person) out.for = forWords(rc.for)
    return out
  }
  return d.answer ? { words: d.answer } : null
}

/**
 * What the confirm step says a choice authorizes (the independent review of #719, finding 3): a gate's options are fixed, Approve then
 * Reject, and an assignment's Done then Not done, so their yes is option 0 and only that choice authorizes; an agent's approval has
 * options of its own labels, none of them the yes by its place, so whichever is picked the confirm says what the approval
 * authorizes. '' for nothing to say.
 */
export function confirmAuthorizes(d: Pick<Decision, 'origin' | 'type' | 'request'>, choice: number): string {
  const a = d.request.authorizes
  if (!a) return ''
  if (d.origin === 'plan' || d.type === 'assignment') return choice === 0 ? `A yes authorizes: ${a}` : ''
  return d.type === 'approval' ? `This approval authorizes: ${a}` : ''
}

/**
 * Whether a write's or a read's reply may replace the record a card shows (Codex's round 1 on #719): a reply of another decision never,
 * an older revision never (a write's reply delayed past a read of what another tab did since would bring the record back), the same
 * revision only when it is the whole record (a read, which carries the history and the responses a write's reply leaves out).
 */
export function replaces(current: Pick<Decision, 'id' | 'rev'> | null, next: Pick<Decision, 'id' | 'rev'>, whole: boolean): boolean {
  if (!current || current.id !== next.id) return true
  return next.rev > current.rev || (next.rev === current.rev && whole)
}

/**
 * Whether the run that holds a decision runs here now, from the run as the live store holds it or as it was read for the card: true
 * for a run the server holds and runs, false for one stopped, a record (interrupted at a restart and not continued: the live store
 * leaves those out) or continued by another, or for none the server holds (null); undefined while it is not known yet.
 */
export function runHeldOf(r: { stoppedAt?: string; record?: boolean; resumedBy?: string; state?: string } | null | undefined): boolean | undefined {
  if (r === undefined) return undefined
  if (r === null) return false
  return !r.record && !r.stoppedAt && !r.resumedBy && r.state !== 'interrupted'
}

/**
 * A required decision's latest record on the ticket page (the reviews of #719): the newest gate of that plan decision opened under the
 * approved revision `plan`, never a gate of the same id another revision's run opened. Records come newest first.
 */
export function requiredRecord<D extends Pick<Decision, 'origin' | 'gate' | 'plan'>>(records: readonly D[] | null | undefined, gate: string, plan: string | undefined): D | undefined {
  if (!plan) return undefined
  return records?.find((r) => r.origin === 'plan' && r.gate === gate && r.plan === plan)
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

/**
 * What Needs you says when the store's list of decisions is cut (more open or answered than it reads, useDecisions' `cut`), or '' when
 * it is whole. Said whether or not a row of it is shown (Codex's round 2 on #719): the open decisions read may all be of stopped runs
 * or mirrored, and the ones past the cut, a running gate's among them, are still open.
 */
export function cutNotice(cut: boolean, shown: number): string {
  if (!cut) return ''
  return shown ? 'More decisions are open than these: the oldest are on their run and ticket pages.' : 'More decisions are open than could be read here: the oldest are on their run and ticket pages.'
}

/**
 * Needs you's and the palette's words for a list whose last read failed (Codex's round 3 on #719): what they show may miss a decision
 * opened or answered since `since` (ms since the epoch), and the list is read again by itself until a read succeeds. '' while the last
 * read succeeded.
 */
export function staleNotice(since: number | null, error: string): string {
  if (since === null) return ''
  const at = new Date(since).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false })
  return `Decisions out of date since ${at}${error ? ` (${error})` : ''}: read again until a read succeeds.`
}

/** Whether a run, as the live store holds it, runs: the server holds it, and it is not stopped or continued by another. */
export function runRuns(r: { stoppedAt?: string; record?: boolean; resumedBy?: string } | undefined): boolean {
  return !!r && !r.stoppedAt && !r.record && !r.resumedBy
}

/**
 * The palette's rows: one per open decision, "Answer dr-7: <question>", oldest first; Enter (`open`) opens its card. `in` names the
 * session whose shown question mirrors it, if one does: that row stands for the session's question too (oneRowPerDecision).
 */
export function answerRows(decisions: ReadonlyArray<Pick<Decision, 'id' | 'state' | 'request' | 'origin' | 'gate' | 'asker' | 'createdAt'>>, open: (id: string) => void, inSession: (id: string) => string | undefined = () => undefined) {
  return decisions
    .filter((d) => d.state === 'open')
    .slice()
    .sort((a, b) => a.createdAt.localeCompare(b.createdAt) || a.id.localeCompare(b.id))
    .map((d) => {
      const where = inSession(d.id)
      return { key: `decision:${d.id}`, label: `Answer ${d.id}: ${d.request.question}`, detail: `asked by ${askedWhere(d)}${where ? ` in ${where}` : ''}`, icon: 'i-lucide-scale', run: () => open(d.id) }
    })
}

/**
 * The sessions whose shown question mirrors an open decision of `open` (an attention needing input with `decision`), by session id:
 * the palette lists such a question once, as its decision's row, and the session itself among the running ones.
 */
export function mirroringSessions(sessions: ReadonlyArray<{ id: string; attention?: { state?: string; decision?: string } }>, open: ReadonlyArray<Pick<Decision, 'id'>>): Map<string, string> {
  const ids = new Set(open.map((d) => d.id))
  const out = new Map<string, string>()
  for (const s of sessions) if (s.attention?.state === 'needs_input' && s.attention.decision && ids.has(s.attention.decision)) out.set(s.id, s.attention.decision)
  return out
}

/**
 * One row per durable decision across the palette's groups (the independent review of #719, finding 2): the first row naming a
 * decision stays (Needs you's "Answer dr-7", which opens its card), every later row naming the same decision goes, whatever group
 * gave it: the decision page's row of the session question that mirrors it among them. `decisionOf` names a row's decision, or none.
 */
export function oneRowPerDecision<G extends { rows: ReadonlyArray<{ key: string }> }>(groups: G[], decisionOf: (key: string) => string | undefined): G[] {
  const seen = new Set<string>()
  return groups.map((g) => ({
    ...g,
    rows: g.rows.filter((r) => {
      const d = decisionOf(r.key)
      if (!d) return true
      if (seen.has(d)) return false
      seen.add(d)
      return true
    }),
  }))
}

/**
 * The decision a palette row names, by its key: a decision's own row (`decision:dr-7`, and the decision page's `decide:decision:dr-7`
 * once its items carry decisions), or a row of a session's question
 * (`decide:question:<session>/<question>`, the decision page's) whose session shows that question mirroring a decision.
 */
export function rowDecision(key: string, sessions: ReadonlyArray<{ id: string; attention?: { id?: string; state?: string; decision?: string } }>): string | undefined {
  for (const prefix of ['decision:', 'decide:decision:']) if (key.startsWith(prefix)) return isDecisionId(key.slice(prefix.length)) ? key.slice(prefix.length) : undefined
  const m = /^decide:question:([^/]+)\/(.+)$/.exec(key)
  if (!m) return undefined
  const s = sessions.find((x) => x.id === m[1])
  return s?.attention?.state === 'needs_input' && s.attention.id === m[2] && s.attention.decision ? s.attention.decision : undefined
}

/** The words a history row is shown with: its kind in words, and its text when it has one; a route to a person names them. */
export function historyWords(h: DecisionEntry): string {
  const text = typeof h.detail?.text === 'string' ? h.detail.text : typeof h.detail?.why === 'string' ? h.detail.why : typeof h.detail?.note === 'string' ? h.detail.note : ''
  let kind = HISTORY_KINDS[h.kind] ?? h.kind
  const person = h.kind === 'routed' ? routedPerson(h) : ''
  if (person) kind = `routed to ${person}`
  return text ? `${kind}: ${text}` : kind
}

/** The person a routed row names beside the operator (`to`'s `{kind: person, name}`, HM1's label resolved); "" for none. */
function routedPerson(h: DecisionEntry): string {
  const to = Array.isArray(h.detail?.to) ? (h.detail.to as Array<Record<string, unknown>>) : []
  const p = to.find((d) => d?.kind === 'person' && typeof d.name === 'string' && d.name)
  return p ? String(p.name) : ''
}

/**
 * Whom the decision was last routed to by one of the operator's routes (the operator's own, or Brakeman's under its grant), as its
 * history says: the person and who routed it; null for none, or when the last such route was to the operator alone. The operator
 * still answers it (local/1): the card says for whom. A decision router's row (`router:`) is Switchman's history, not this. Null
 * too for a decision whose request names whom it is for (HM1's `for`, which a route to a person rewrites): the card's approver line
 * names that person then (approverWords), and says it once.
 */
export function routedFor(d: Pick<Decision, 'history'> & { request?: unknown }): { person: string; by: string } | null {
  if ((d.request as { for?: { person?: string } } | undefined)?.for?.person) return null
  const rows = (d.history ?? []).filter((h) => h.kind === 'routed' && h.by && !h.by.startsWith('router:'))
  const last = rows[rows.length - 1]
  const person = last ? routedPerson(last) : ''
  return last && person ? { person, by: last.by! } : null
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
