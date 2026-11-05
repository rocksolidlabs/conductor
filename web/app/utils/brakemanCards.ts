import type { RunInfo, RunTask, SessionInfo } from '~/composables/useSessions'
import type { AttentionOption } from './protocol'
import { liveAttention } from './attention'
import type { PlanSummary } from '~/composables/useDispatch'
import { parseRef, refRoute, type BrakemanAct, type BrakemanEdge, type BrakemanMade, type BrakemanRequest, type BriefItem } from './brakeman'
import { PLAN_BOUNDS, approvable, planReasons, revisionTag } from './dispatchPlans'
import { taskActions, type TaskCommand } from './tasks'

// A brief's decision cards and proposals as Conductor's controls (docs/plans/brakeman.md, "web/app: decision cards, drafts,
// handoffs and the entry points"; PR-7). A card's text is the brief's; its controls are read from the live record of what it is
// about, never from what Brakeman wrote: an open question is answered in its session, the one place a question is answered; a
// task's commands are those its state allows by the run page's own rules (taskActions), each carrying the revision of the run the
// turn read, so a run that moved since is refused by the route itself (409 stale_revision) rather than acted on blind; a ticket
// leads to its page. A proposal is a button only for an operation whose route itself refuses a stale revision (the guide's
// Operation.Revision); any other is text with a link to the page that does it. Pure: no Nuxt, no fetch.

/** What the card reads of the live records: the run (as the page holds it, or read) and the session the card is about. */
export interface LiveRecords {
  run?: (id: string) => Pick<RunInfo, 'id' | 'stoppedAt' | 'record' | 'resumedBy' | 'managed' | 'tasks'> | null | undefined
  session?: (id: string) => Pick<SessionInfo, 'id' | 'name' | 'status' | 'attention'> | null | undefined
}

/** A decision card's controls, from the live record of its `about`. */
export type CardControls =
  | {
      kind: 'question'
      session: string
      /** The session page, where the reply bar answers it. */
      to: string
      /** Whether the question is the one the session asks now. */
      open: boolean
      /** Its options, as the session reports them, while it is open. */
      options: AttentionOption[]
      words: string
    }
  | {
      kind: 'task'
      run: string
      task: string
      /** The commands the task's state allows, by taskActions' rules, in the run page's order. */
      commands: TaskCommand[]
      /** The run's revision the turn read: every command carries it. 0 when the brief names none (no command is offered then). */
      expectedRevision: number
      /** The run page at the task, where the person sees it as it is. */
      to: string
      words: string
    }
  | { kind: 'ticket'; to: string; words: string }
  | {
      kind: 'plan'
      /** The ticket the revision is of, and its number: the card reads the revision's live record by them (DS2's own read). */
      ticket: string
      n: number
      /** The ticket's page, whose Plan section is where the revision is decided as well. */
      to: string
      words: string
    }
  | { kind: 'none'; to: string | null; words: string }

/** A revision as a brief or a resolution carries it ("7"), as the number a command expects, or 0 for none. */
export function revisionOf(rev: string | undefined): number {
  return rev && /^[1-9][0-9]{0,15}$/.test(rev) ? Number(rev) : 0
}

/** Who answers a decision in this build: the operator (DR1 and HM1 add named people). */
export const ANSWERER = 'you, the operator'

/**
 * A decision card's controls from the live record of `about` (a `question:`, `task:` or `ticket:` ref). `seen` is the revision of
 * `about` the turn read (its resolution's `seen.rev`), else the card's `subjectRevision`, which the server held equal to it.
 */
export function decisionControls(about: string, seen: string | undefined, live: LiveRecords): CardControls {
  const r = parseRef(about)
  if (!r) return { kind: 'none', to: null, words: `${about} is no record Conductor can read.` }
  switch (r.kind) {
    case 'question': {
      const to = refRoute(about)!
      const s = live.session?.(r.session!)
      if (!s) return { kind: 'question', session: r.session!, to, open: false, options: [], words: 'The session is not listed here now: its question cannot be answered.' }
      const a = liveAttention(s.attention)
      if (!a || a.state !== 'needs_input' || a.id !== r.question) return { kind: 'question', session: s.id, to, open: false, options: [], words: `${s.name} no longer asks this question: it was answered, or it asks another now.` }
      return { kind: 'question', session: s.id, to, open: true, options: a.options ?? [], words: `${s.name} asks it now. Answer it in the session, where its reply bar is.` }
    }
    case 'task': {
      const to = refRoute(about)!
      const run = live.run?.(r.run!)
      const t = run?.tasks?.find((x) => x.id === r.task)
      if (!run || !t) return { kind: 'none', to, words: `The task ${r.task} is not held here now: open its run.` }
      return taskControls(run, t, revisionOf(seen), to)
    }
    case 'ticket':
      return { kind: 'ticket', to: refRoute(about)!, words: `Decide it on ${r.ticket}'s page.` }
    case 'plan':
      return { kind: 'plan', ticket: r.ticket!, n: r.n!, to: refRoute(about)!, words: `Revision ${r.n} of ${r.ticket}'s plan, as Dispatch holds it now.` }
  }
  return { kind: 'none', to: refRoute(about), words: 'This build has no control for what the card is about: open its page.' }
}

/**
 * A plan revision's card control, from the revision's live record as DS2's own read gives it (null: not there, or not read yet): DS2's
 * Approve, offered by DS2's own rule (approvable: a proposed or superseded revision, or an approved one whose only staleness an approval
 * cures, never one whose agents changed), labelled Approve again for an approved one; or none, with what the revision is now. The
 * approval is the operator's, made through DS2's own dialog and route: Brakeman never approves.
 */
export function planCardControl(p: Pick<PlanSummary, 'n' | 'state' | 'stale' | 'agentsChanged' | 'crewChanged' | 'crewName'> | null | undefined, ticket: string, n: number): { approve: 'Approve' | 'Approve again' | null; words: string } {
  const tag = `${ticket} ${revisionTag(n)}`
  if (!p) return { approve: null, words: `${tag} is not among ${ticket}'s revisions now: open the ticket.` }
  const all = planReasons(p)
  const reasons = all.map((r) => r.words)
  // Whether an approval holds is the blocking reasons' alone: a saved crew edited since (crewChanged) leaves the revision its frozen
  // crew, launchable as approved, and is said beside it.
  const blocking = all.filter((r) => r.blocks).map((r) => r.words)
  const besides = all.filter((r) => !r.blocks).map((r) => r.words)
  const note = besides.length ? ` ${besides.join('; ')}.` : ''
  if (approvable(p)) {
    const again = p.state === 'approved'
    return {
      approve: again ? 'Approve again' : 'Approve',
      words: again ? `${tag} is approved, but its approval no longer holds: ${blocking.join('; ')}. You approve it again, or not.${note}` : `${tag} is ${p.state}. You, the operator, approve it or not: Brakeman never does.${note}`,
    }
  }
  switch (p.state) {
    case 'approved':
      return { approve: null, words: blocking.length ? `${tag} is approved, and its approval no longer holds: ${blocking.join('; ')}. Revise it on the ticket.${note}` : `${tag} is approved, and its approval holds.${note}` }
    case 'withdrawn':
      return { approve: null, words: `${tag} was withdrawn: nothing approves it now.` }
  }
  return { approve: null, words: `${tag} is ${p.state}${reasons.length ? `: ${reasons.join('; ')}` : ''}. Open the ticket to revise it.` }
}

/** The commands a task's card offers, by taskActions' rules, with the revision they carry. */
function taskControls(run: Pick<RunInfo, 'id' | 'stoppedAt' | 'record' | 'resumedBy' | 'managed' | 'tasks'>, t: RunTask, expectedRevision: number, to: string): CardControls {
  const a = taskActions(t, run)
  const commands = (['retry', 'accept', 'cancel'] as const).filter((c) => a[c])
  const base = { kind: 'task' as const, run: run.id, task: t.id, to }
  if (!commands.length) return { ...base, commands: [], expectedRevision, words: `${t.id} is ${t.state}: nothing can be done to it from here now.` }
  if (!expectedRevision) return { ...base, commands: [], expectedRevision: 0, words: 'Brakeman named no revision of the run it read: act from the run page, on the run as it is.' }
  return { ...base, commands, expectedRevision, words: `${t.id} is ${t.state}. Each command carries revision ${expectedRevision} of the run, the one Brakeman read.` }
}

/** What a card's command button says. */
export function commandLabel(c: TaskCommand): string {
  return c === 'retry' ? 'Retry' : c === 'accept' ? 'Accept anyway…' : 'Cancel…'
}

/**
 * What a card or a proposal says when its route refused the revision it carried: the run (or the ticket) moved on since Brakeman read
 * it, nothing was done, and what it is now when the page has read it again.
 */
export function staleWords(what: 'run' | 'ticket', seen: number, now?: number, state?: string): string {
  const moved = now && now !== seen ? `revision ${seen}; it is at revision ${now} now` : `revision ${seen}`
  const is = state ? `, and ${state}` : ''
  return `The ${what} has moved on since Brakeman read it (${moved}${is}). Nothing was done: look at it as it is before you act.`
}

/** Whether a refusal is the route's stale revision: a task command's receipt, or a Dispatch operation's. */
export function isStale(e: unknown): boolean {
  const x = e as { receipt?: { refusal?: string }; code?: string; body?: { error?: { code?: string } } }
  return x?.receipt?.refusal === 'stale_revision' || x?.code === 'stale_revision' || x?.body?.error?.code === 'stale_revision'
}

/** An operation as the guide lists it (GET /api/guide?section=operations): its name and the revision its route checks. */
export interface GuideOperation {
  name: string
  /** Who reaches it: admin, session, task, ticket, observe, or act (an operation the person may grant Brakeman). */
  scope?: string
  revision?: string
  method?: string
  path?: string
}

/**
 * The operations a proposal's button sends, each through the client call its own page uses, and what its target is. A ticket's
 * operation sends the proposal's arguments, which are not its text: it is asked first, with what it sends (proposalSends).
 */
export const PROPOSAL_CALLS: Readonly<Record<string, { target: 'run' | 'task' | 'ticket'; label: (target: string) => string; ask?: boolean }>> = {
  run_pause: { target: 'run', label: () => 'Pause the run' },
  run_unpause: { target: 'run', label: () => 'Unpause the run' },
  task_retry: { target: 'task', label: (t) => `Retry ${t}` },
  task_accept: { target: 'task', label: (t) => `Accept ${t} anyway…`, ask: true },
  task_cancel: { target: 'task', label: (t) => `Cancel ${t}…`, ask: true },
  ticket_edit: { target: 'ticket', label: (t) => `Edit ${t}…`, ask: true },
  ticket_comment_admin: { target: 'ticket', label: (t) => `Comment on ${t}…`, ask: true },
  ticket_link: { target: 'ticket', label: (t) => `Link ${t}…`, ask: true },
}

/** A proposal's control: a button that sends its operation with its revision, or text with the page that does it. */
export type ProposalControl =
  | { kind: 'button'; op: string; target: 'run' | 'task' | 'ticket'; run?: string; task?: string; ticket?: string; label: string; ask: boolean; expectedRevision: number }
  | { kind: 'text'; to: string | null; words: string }

/**
 * A proposal's control. `ops` is the guide's operations (null while they are not read, or could not be): a button only for an
 * operation the guide marks as checking a revision, that this page sends, on a target of its kind, with the revision the turn read.
 */
export function proposalControl(p: Extract<BriefItem, { kind: 'proposal/1' }>, ops: readonly GuideOperation[] | null): ProposalControl {
  const to = refRoute(p.target)
  const text = (words: string): ProposalControl => ({ kind: 'text', to, words })
  const guide = ops?.find((o) => o.name === p.op)
  const call = PROPOSAL_CALLS[p.op]
  if (!ops) return text('Do it from its page, on the record as it is.')
  if (!guide) return text(`${p.op} is no operation this server lists: do what it says from its page.`)
  if (!guide.revision) return text(`${p.op} checks no revision on its route, so a button here could act on what moved since Brakeman read it: do it from its page, where you see it as it is.`)
  // A launch and a follow-up revision (the operations a grant may name) are the ticket's Plan section's, which asks first and says what
  // a launch runs; Brakeman runs them itself only when the person granted it.
  if (!call) return text(guide.scope === 'act' ? `Do it from the ticket's Plan section, which says what it runs and asks first, or grant Brakeman "${p.op}" in Settings.` : `${p.op} has no button here: do it from its page, where you see it as it is.`)
  const r = parseRef(p.target)
  if (!r || r.kind !== call.target) return text(`${p.op} acts on a ${call.target}, and the proposal names ${p.target}: do it from its page.`)
  const expectedRevision = revisionOf(p.expectedRevision)
  if (!expectedRevision) return text('The proposal names no revision of what it acts on: do it from its page.')
  const name = r.kind === 'task' ? r.task! : r.kind === 'ticket' ? r.ticket! : 'the run'
  return { kind: 'button', op: p.op, target: call.target, run: r.run, task: r.task, ticket: r.ticket, label: call.label(name), ask: !!call.ask, expectedRevision }
}

/** A proposal's arguments for its Dispatch operation: a JSON object, without the fields the page sets itself (its id, revision and name). */
export function proposalArgs(args: Record<string, unknown> | undefined): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  if (!args || typeof args !== 'object' || Array.isArray(args)) return out
  for (const [k, v] of Object.entries(args)) if (k !== 'opId' && k !== 'expectedRev' && k !== 'byName') out[k] = v
  return out
}

/**
 * What a proposal's Dispatch operation sends, as the person reads it before pressing: each field of its arguments (proposalArgs) by
 * its path ("set.title"), with its value as it is sent: a string as itself (a comment's body is its Markdown), anything else as JSON.
 * Every field is listed: nothing the button sends is left off the page.
 */
export function proposalSends(args: Record<string, unknown> | undefined): Array<{ field: string; value: string }> {
  const out: Array<{ field: string; value: string }> = []
  const walk = (v: unknown, path: string) => {
    if (v && typeof v === 'object' && !Array.isArray(v) && Object.keys(v).length) {
      for (const [k, x] of Object.entries(v)) walk(x, path ? `${path}.${k}` : k)
      return
    }
    out.push({ field: path, value: typeof v === 'string' ? v : JSON.stringify(v) ?? String(v) })
  }
  for (const [k, v] of Object.entries(proposalArgs(args))) walk(v, k)
  return out
}

/** A make this browser asked that did not finish: the id and the body it sent, the one ask the route finishes it on. */
export interface KeptMake {
  opId: string
  body: { by?: string; edit?: unknown }
  /** When it was asked (ms since the epoch), so a browser keeps the newest. */
  at?: number
}

/**
 * Where a browser keeps its unfinished makes: one storage entry per `<request>/<item>`, so a tab that keeps or forgets its make
 * writes that entry alone and never another tab's (one shared entry, rewritten whole from each tab's own copy, lost the makes the
 * other tabs kept after it was read).
 */
export const KEPT_PREFIX = 'conductor.brakeman.kept-make:'
/** How many unfinished makes a browser keeps: the newest, by when they were asked, whichever tab asked them. */
export const KEPT_MAX = 50

/** The part of the browser's Storage the kept makes use. */
export type KeptStorage = Pick<Storage, 'length' | 'key' | 'getItem' | 'setItem' | 'removeItem'>

function keptNames(s: KeptStorage): string[] {
  const names: string[] = []
  for (let i = 0; i < s.length; i++) {
    const name = s.key(i)
    if (name?.startsWith(KEPT_PREFIX)) names.push(name)
  }
  return names
}

function parseKept(raw: string | null): KeptMake | null {
  try {
    const k = JSON.parse(raw ?? 'null') as KeptMake | null
    return k && typeof k === 'object' && typeof k.opId === 'string' && k.body && typeof k.body === 'object' ? k : null
  } catch {
    return null
  }
}

/** The unfinished makes the browser keeps, every tab's, by `<request>/<item>`; an entry that does not read as one is skipped. */
export function readKeptMakes(s: KeptStorage): Record<string, KeptMake> {
  const out: Record<string, KeptMake> = {}
  for (const name of keptNames(s)) {
    const k = parseKept(s.getItem(name))
    if (k) out[name.slice(KEPT_PREFIX.length)] = k
  }
  return out
}

/** Keeps one make under its own entry, then drops the oldest beyond KEPT_MAX. */
export function storeKeptMake(s: KeptStorage, key: string, k: KeptMake) {
  s.setItem(KEPT_PREFIX + key, JSON.stringify(k))
  const names = keptNames(s)
  if (names.length <= KEPT_MAX) return
  const byAge = names.map((name) => ({ name, at: parseKept(s.getItem(name))?.at ?? 0 })).sort((a, b) => b.at - a.at)
  for (const { name } of byAge.slice(KEPT_MAX)) s.removeItem(name)
}

/** Forgets one make's entry, and only it. */
export function dropKeptMake(s: KeptStorage, key: string) {
  s.removeItem(KEPT_PREFIX + key)
}

/**
 * Whether a make's failure leaves it to be asked again with the same id and body: a failure that establishes nothing about the make.
 * A reply lost twice (status 0); the server's failure (500 make_failed: the backend failed after the claim, or before it; 503: a store
 * not open); or a refusal before the make route ran (401 for a token missing or wrong, 429 for too many of those, 408 for a request
 * that took too long), which says nothing of a claim the make may hold. Any other answer settles it: made, refused, or another make
 * holds the item.
 */
export function unfinishedMake(e: unknown): boolean {
  const status = (e as { status?: number })?.status
  return typeof status === 'number' && (status === 0 || status === 401 || status === 408 || status === 429 || status >= 500)
}

/**
 * Sends a make kept first: `keep` writes its id and body where a page read again finds them before `send` goes out, so a reload while
 * the reply is out (the backend may claim the make and then fail) can still finish it; an answer that settles it `forget`s them, one
 * that establishes nothing (unfinishedMake) leaves them kept.
 */
export async function sendKept<R>(keep: () => void, forget: () => void, send: () => Promise<R>): Promise<R> {
  keep()
  try {
    const out = await send()
    forget()
    return out
  } catch (e) {
    if (!unfinishedMake(e)) forget()
    throw e
  }
}

/** How far along a made entry or an edge is: pending, then done (made, refused, deleted), which it never leaves. */
const settledRank = (state: string) => (state === 'pending' ? 0 : 1)
const claimedAt = (m: { at?: string }) => (m.at ? Date.parse(m.at) || 0 : 0)

/**
 * A request's `made` and `edges` as a reply gives them, reconciled with what the page has: a reply sent before another (two cards' makes
 * answered out of order, a read sent before a make's answer) never takes an entry or an edge back. An item's entry goes from pending to
 * done under its id and is replaced only by a newer claim (a refused one asked again); an edge goes from pending to made or refused; the
 * server never removes either, so one the reply lacks is the page's.
 */
export function reconcileMade(had: Pick<BrakemanRequest, 'made' | 'edges'>, next: Pick<BrakemanRequest, 'made' | 'edges'>): Pick<BrakemanRequest, 'made' | 'edges'> {
  const made = new Map<number, BrakemanMade>()
  for (const m of had.made ?? []) made.set(m.item, m)
  for (const n of next.made ?? []) {
    const h = made.get(n.item)
    const older = h && (h.opId === n.opId ? settledRank(n.state) < settledRank(h.state) : claimedAt(n) < claimedAt(h))
    if (!older) made.set(n.item, n)
  }
  const edges = new Map<string, BrakemanEdge>()
  for (const e of had.edges ?? []) edges.set(`${e.from}/${e.to}`, e)
  for (const n of next.edges ?? []) {
    const h = edges.get(`${n.from}/${n.to}`)
    if (!h || settledRank(n.state) >= settledRank(h.state)) edges.set(`${n.from}/${n.to}`, n)
  }
  const out: Pick<BrakemanRequest, 'made' | 'edges'> = {}
  if (made.size) out.made = [...made.values()]
  if (edges.size) out.edges = [...edges.values()]
  return out
}

/** Whether an act is final: done or not run, which it never leaves. A refused one its store kept no receipt for is written pending anew when asked again under its opId. */
const actFinal = (state: string) => state === 'done' || state === 'not_run'
/** pending, then refused: of two entries of one act written at one instant, the refusal is the later. */
const actRank = (state: string) => (state === 'pending' ? 0 : state === 'refused' ? 1 : 2)

/**
 * An entry's time as the server writes it (RFC 3339, its seconds' fraction to the nanosecond) as [milliseconds of its whole seconds,
 * nanoseconds of the fraction], so that two writes within a millisecond are still told apart; [0, 0] for none.
 */
function atParts(at?: string): [number, number] {
  if (!at) return [0, 0]
  const m = /^(.*?)(?:\.(\d+))?(Z|[+-]\d\d:\d\d)$/.exec(at)
  if (!m) return [Date.parse(at) || 0, 0]
  return [Date.parse(m[1]! + m[3]!) || 0, Number((m[2] ?? '').padEnd(9, '0').slice(0, 9))]
}

/** Negative when a was written before b, positive after, 0 at one instant. */
export function compareAt(a?: string, b?: string): number {
  const [x, xn] = atParts(a)
  const [y, yn] = atParts(b)
  return x - y || xn - yn
}

/**
 * A request's `acts` as a reply gives them, reconciled with what the page has: a reply sent before another (a read sent while an act was
 * pending, answered after the read that saw it done) never takes an act back. Each act is kept by its opId. A final one (done, not run)
 * is never left; one that is not yet final takes a final entry, or the later of two that are not final by the time the server wrote it
 * (each write of an act's entry, pending or its outcome, stamps it): a refusal before Dispatch kept a receipt, asked again, is pending
 * again under a later time, and a reply sent before that write still says refused under the earlier one. The server never removes an
 * act, so one the reply lacks is the page's.
 */
export function reconcileActs(had: Pick<BrakemanRequest, 'acts'>, next: Pick<BrakemanRequest, 'acts'>): Pick<BrakemanRequest, 'acts'> {
  const acts = new Map<string, BrakemanAct>()
  for (const a of had.acts ?? []) acts.set(a.opId, a)
  for (const n of next.acts ?? []) {
    const h = acts.get(n.opId)
    const older =
      h &&
      (actFinal(h.state)
        ? !actFinal(n.state) || compareAt(n.at, h.at) < 0
        : !actFinal(n.state) && (compareAt(n.at, h.at) || actRank(n.state) - actRank(h.state)) < 0)
    if (!older) acts.set(n.opId, n)
  }
  return acts.size ? { acts: [...acts.values()] } : {}
}

/**
 * What keeps a draft plan revision's form from being proposed, in words, "" for nothing: a crew named, and the bounds the server applies,
 * the constraints and the note in UTF-8 bytes, a decision's question in characters (Unicode code points), as internal/brakeman's
 * CheckDraftPlan and Dispatch's propose route count it (MaxDraftDecisionAsk, MaxDecisionQuestion), so 200 characters of CJK, 600
 * bytes, are within it.
 */
export function draftPlanProblem(f: { crew: string; constraints: string; note: string; questions: readonly string[] }): string {
  if (!f.crew.trim()) return 'A plan revision runs a saved crew with tasks: name its id.'
  if (utf8Bytes(f.constraints) > PLAN_BOUNDS.constraints) return `The constraints are at most ${PLAN_BOUNDS.constraints >> 10} KiB.`
  if (utf8Bytes(f.note) > PLAN_BOUNDS.note) return `The note is at most ${PLAN_BOUNDS.note >> 10} KiB.`
  if (f.questions.some((q) => [...q].length > PLAN_BOUNDS.decisionQuestion)) return `A decision's question is at most ${PLAN_BOUNDS.decisionQuestion} characters.`
  return ''
}

/** Dispatch's bound on a comment's body, in bytes (MaxCommentBody, internal/dispatch/model.go). */
export const COMMENT_MAX = 16 << 10

/** A text's length in UTF-8 bytes, as the server measures a bound. */
export function utf8Bytes(s: string): number {
  return new TextEncoder().encode(s).length
}

/** Being made under an ask this browser does not hold: the words for a pending entry with no make kept to finish. */
export const PENDING_ELSEWHERE = "Being made: it finishes when the browser that asked it asks again, or at the server's next start."

/**
 * What a draft card does about a make that did not finish, from the request's entry for the item and the make this browser kept:
 * `finish` it, asked again as it was sent (the form shows that content and takes no edit); `drop` the kept one, which an answer has
 * settled (made, deleted, refused under its id) or another make holds (pending under another id); or `none`, nothing kept.
 */
export function makeAgain(made: { state: string; opId?: string } | undefined, kept: KeptMake | undefined): { kind: 'finish' | 'drop' | 'none'; words: string } {
  const elsewhere = made?.state === 'pending' ? PENDING_ELSEWHERE : ''
  if (!kept) return { kind: 'none', words: elsewhere }
  if (made && (made.state === 'made' || made.state === 'deleted')) return { kind: 'drop', words: '' }
  if (made && made.opId !== kept.opId && made.state === 'pending') return { kind: 'drop', words: elsewhere }
  if (made && made.opId === kept.opId && made.state === 'refused') return { kind: 'drop', words: '' }
  return { kind: 'finish', words: 'The make did not finish. Finish it as it was sent: the same ask again, which makes it once.' }
}

/** A handoff package as Markdown, for Copy and for a ticket's comment: built from the brief's fields, which are Conductor's records. */
export function handoffMarkdown(h: Extract<BriefItem, { kind: 'handoff/1' }>, label: (ref: string) => string = (r) => r): string {
  const out = [`## Handoff: ${h.objective}`]
  if (h.constraints) out.push('', '### Constraints', '', h.constraints)
  if (h.decisions?.length) out.push('', '### Decisions made', '', ...h.decisions.map((d) => `- ${d}`))
  if (h.revision) out.push('', '### Revision', '', `- ${label(h.revision)} (\`${h.revision}\`)`)
  if (h.questions?.length) out.push('', '### Open questions', '', ...h.questions.map((q) => `- ${q}`))
  if (h.evidence?.length) out.push('', '### Evidence', '', ...h.evidence.map((r) => `- ${label(r)} (\`${r}\`)`))
  return out.join('\n') + '\n'
}

/** The tickets a handoff's refs name, which "Comment on the ticket" offers. */
export function handoffTickets(h: Extract<BriefItem, { kind: 'handoff/1' }>): string[] {
  const out: string[] = []
  for (const ref of [...(h.evidence ?? []), ...(h.revision ? [h.revision] : [])]) {
    const r = parseRef(ref)
    const key = r?.kind === 'ticket' || r?.kind === 'comment' ? r.ticket! : ''
    if (key && !out.includes(key)) out.push(key)
  }
  return out
}

/** A made draft's state in words, from the request's `made` entry: made (with its key or crew), being made, refused, deleted. */
export function madeWords(m: { state: string; key?: string; crew?: string; plan?: string; why?: string } | undefined): string {
  if (!m) return ''
  switch (m.state) {
    case 'made':
      if (m.plan) return `Proposed as a revision of ${m.key}'s plan, under your name: the operator approves it or not.`
      return m.key ? `Made as ${m.key}.` : m.crew ? `Saved as the crew ${m.crew}, not launched.` : 'Made.'
    case 'pending':
      return PENDING_ELSEWHERE
    case 'refused':
      return `Refused: ${m.why || 'the store refused it'}. It can be made again.`
    case 'deleted':
      return 'It was saved and then deleted: it is not made again.'
  }
  return m.state
}

/**
 * A brief's draft ticket as another card's Blocks line names it. Once made, by the ticket it became and that ticket's title as Dispatch
 * holds it (ticketTitle, by the key: the person's edit, the brief's own title only if they kept it), else by its key alone until the
 * title is read: never by the brief's title, which the person may have changed before making it. Until then, by the brief's title.
 */
export function draftName(i: number, items: readonly BriefItem[], made: ReadonlyArray<{ item: number; state: string; key?: string }> | undefined, ticketTitle: (key: string) => string | undefined): string {
  const key = madeKey(i, made)
  if (key) {
    const t = ticketTitle(key)
    return t ? `${key} "${t}"` : key
  }
  const it = items[i]
  return it?.kind === 'draft_ticket/1' ? `"${it.title}"` : `item ${i + 1}`
}

/** The ticket the brief's item `i` became, when its make is made: its key, else undefined. */
export function madeKey(i: number, made: ReadonlyArray<{ item: number; state: string; key?: string }> | undefined): string | undefined {
  return made?.find((x) => x.item === i && x.state === 'made' && x.key)?.key
}

/** A draft ticket's blocks in words, with each edge's state once a make has written them: "blocks draft 2 (made)". */
export function blocksWords(item: number, blocks: readonly number[] | undefined, edges: ReadonlyArray<{ from: number; to: number; state: string }> | undefined, title: (i: number) => string): string {
  if (!blocks?.length) return ''
  return `Blocks ${blocks
    .map((to) => {
      const e = edges?.find((x) => x.from === item && x.to === to)
      return `${title(to)}${e ? ` (${e.state === 'made' ? 'related' : e.state === 'pending' ? 'related once both are made' : e.state})` : ''}`
    })
    .join(', ')}.`
}
