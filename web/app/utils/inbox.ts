// The decision page, "Needs you" (docs/plans/decision-inbox.md, Program item MB0, PR-2): what the inbox routes answer
// (GET /api/inbox, /api/inbox/item, /api/inbox/track, docs/protocol.md), the order of the items, the four steps of an action in
// words, the rule that keeps a step from moving back, this browser's recent actions, the bar's count and the phone's two paths
// (its landing on the page, a notification's target) and the Open on your phone offer. Pure. The names keep clear of DR1's
// (utils/decisions.ts is the durable decision record's).

import { NO_QUESTION, ReplyError } from './reply'
import { relativeTime } from './sessions'
import { CommandRefused } from './tasks'

/** One item as the list shows it, folded: no action's input or scope (the page reads the item before it acts). */
export interface InboxSummary {
  /** Brakeman's ref of what the item is about: question:<session>/<question>, task:<run>/<task>, plan:<KEY-n>/<n>, decision:dr-<n>. */
  ref: string
  kind: 'question' | 'task' | 'plan' | 'decision' | string
  since: string
  title: string
  brief: string
  question: string
  answerer: string
  routed?: number
  blocks?: number
  actions: string[]
  card?: boolean
  /** A DR1 record's state (`decision` items). */
  state?: string
  /** A DR1 record answered and not yet applied: listed under "Answered, not yet applied", left out of the bar's number. */
  tracking?: boolean
  /** When a DR1 record closed (the server's Recent). */
  closed?: string
}

/** One thing a person may do with an item, through its own operation's route, at the revision that operation checks. */
export interface InboxAction {
  /** answer | retry | accept | cancel | recheck | approve | withdraw */
  name: string
  label: string
  /** A question's choice: its keys, exact. */
  input?: string
  /** A line is typed. */
  text?: boolean
  /** The revision the action's own operation checks: the question's id, the run's revision, the plan revision's rev. */
  expect: string
  /** Approve only: the ticket's revision, checked beside expect. */
  ticketRev?: number
  /** Exactly what it does, to what, at which revision: Conductor's words from the record. */
  scope: string
  /** Asked first (Accept anyway, Cancel, Withdraw). */
  confirm?: boolean
  /** The operation's own route: `/ws/sessions/{id}` (the answer message), `POST /api/…`. */
  route: string
  /** The ref the action's steps are read under, when not the item's own: a blocker's question tracks its task's Cancel as the task's. */
  track?: string
  /** Check again only: the candidate it checks, its attempt and its commit, as the route's run names it. */
  attempt?: number
  commit?: string
}

export type StepName = 'available' | 'recorded' | 'validated' | 'resumed'
export type StepState = 'done' | 'waiting' | 'refused' | 'not_applicable' | 'unknown'

/** One of an action's four steps, from the record of that action's own outcome. */
export interface DeliveryStep {
  step: StepName
  state: StepState
  at?: string
  words: string
  /** A ref of the receipt: command:, op:, question:. */
  receipt?: string
}

/** One item, whole. */
export interface InboxItem {
  ref: string
  /** The ref Ask Brakeman and the evidence use. */
  subject: string
  kind: string
  /** A DR1 record's state (`decision` items): open, answered, applied, cancelled… */
  state?: string
  since: string
  title: string
  brief: string
  briefBy: 'conductor' | 'brakeman' | string
  /** brief:<br-n>/<item>, the card the brief came from. */
  card?: string
  question: string
  answerer: string
  routedTo?: Array<{ kind: string; name: string }>
  routedRule?: string
  revision: string
  /** The current validity of what an earlier action did, apart from the steps. */
  now?: string
  blocking: { words: string; refs: string[]; more?: number }
  actions: InboxAction[]
  whyHuman?: string
  considered?: Array<{ label: string; tradeoffs: string }>
  /** Index into considered; -1 for none: Conductor recommends nothing of its own. */
  recommended: number
  uncertainty?: string
  /** Brakeman's "what answering authorizes", shown beside each scope. */
  authorizes?: string
  cardRev?: string
  cardNote?: string
  /** `session` on a decision link's read: the id a session entry in the link's scope is opened by there (its published id). */
  evidence?: Array<{ ref: string; label: string; session?: string }>
  delivery?: DeliveryStep[]
}

export interface InboxPage {
  items: InboxSummary[]
  next?: string
  total: number
  /** How many of `total` are DR1 records answered and not yet applied (tracking): the bar's number leaves them out. */
  tracking?: number
  /** The first page's: DR1's records closed in the last day, at most 50, the latest first, for every viewer (the server's Recent). */
  recent?: InboxSummary[]
  at: string
}

export interface InboxTrack {
  ref: string
  receipt: string
  steps: DeliveryStep[]
}

/** An item as the page draws it: the lists the server leaves null when empty (a blocking's refs) as empty lists. */
export function itemOf(it: InboxItem): InboxItem {
  return {
    ...it,
    blocking: { words: it.blocking?.words ?? '', refs: it.blocking?.refs ?? [], ...(it.blocking?.more ? { more: it.blocking.more } : {}) },
    actions: it.actions ?? [],
    evidence: it.evidence ?? [],
    routedTo: it.routedTo ?? [],
    considered: it.considered ?? [],
    delivery: it.delivery ?? [],
  }
}

/** How many summaries a page of GET /api/inbox holds. */
export const INBOX_PAGE = 150

/** The items oldest first, by (since, ref), the server's own order: the oldest has waited longest (Owner decisions, 11). */
export function inboxOrder<T extends Pick<InboxSummary, 'since' | 'ref'>>(items: readonly T[]): T[] {
  const time = (s: string) => {
    const t = Date.parse(s)
    return Number.isNaN(t) ? 0 : t
  }
  return [...items].sort((a, b) => time(a.since) - time(b.since) || (a.ref < b.ref ? -1 : a.ref > b.ref ? 1 : 0))
}

/** "Showing 150 of 212" when the list holds fewer than there are; '' when it holds them all. */
export function showingWords(shown: number, total: number): string {
  return shown < total ? `Showing ${shown} of ${total}` : ''
}

/** The four steps' names, in order, as the card titles them. */
export const STEP_TITLES: Record<StepName, string> = {
  available: 'Asked',
  recorded: 'Your answer',
  validated: 'Checked by its owner',
  resumed: 'Work resumed',
}
export const STEPS: readonly StepName[] = ['available', 'recorded', 'validated', 'resumed']

/** A step's state in words. */
export function stateWords(state: StepState | string): string {
  switch (state) {
    case 'done':
      return 'Done'
    case 'waiting':
      return 'Waiting'
    case 'refused':
      return 'Refused'
    case 'not_applicable':
      return 'Does not apply'
  }
  return 'Unknown'
}

/** A step's colour and icon: done green, waiting amber, refused red, the others neutral. */
export function stateLook(state: StepState | string): { color: 'success' | 'warning' | 'error' | 'neutral'; icon: string } {
  switch (state) {
    case 'done':
      return { color: 'success', icon: 'i-lucide-circle-check' }
    case 'waiting':
      return { color: 'warning', icon: 'i-lucide-hourglass' }
    case 'refused':
      return { color: 'error', icon: 'i-lucide-circle-x' }
    case 'not_applicable':
      return { color: 'neutral', icon: 'i-lucide-circle-minus' }
  }
  return { color: 'neutral', icon: 'i-lucide-circle-help' }
}

/** Whether a state is settled in a page's view: once seen, it stays (reconcileDelivery). */
function settled(state: StepState | string): boolean {
  return state === 'done' || state === 'refused'
}

/**
 * What the page knows of its own request, kept in the recent entry as its `outcome` (useInbox, "What the page knows of an answer"),
 * one of four and no fifth, for every send the page makes (an answer, a task's command, a plan's operation; useInboxActs): none yet
 * (sent: the reply is out, or the page went before it came); `accepted`, confirmed by a receipt (an answer's `answer_ok`, a
 * command's or an operation's receipt); `refused: <words>`, refused by a receipt (the owner's own refusal, its words); `unconfirmed`,
 * unknown: the request went and no receipt came (a transport failure, a reply lost, an older owner that does not confirm answers, a
 * reply with no receipt). A request that never left the browser has no entry at all.
 */
export type ReplyWord = string

/** The outcome kept for a request that went with no word to come. */
export const UNCONFIRMED = 'unconfirmed'

/**
 * The steps a page shows after a read: `next` (the server's), with every step the page had seen `done` or `refused` kept over a
 * later `waiting` or `unknown` (a step never moves back in a page's view, whatever order the reads come back in). Where the server
 * says `unknown` for the recorded and validated steps (a hosted session's question: the host holds the answer; or a session that
 * forgot it), the reply's own word on this browser's request fills them: the host took it, or refused it with words. A refusal
 * fills them where they are `waiting` too: the owner refused this request whatever the track says of the question (a question
 * set aside or held over still waits to be shown again, and refuses every answer until then), so the steps end there.
 */
export function reconcileDelivery(prev: readonly DeliveryStep[] | undefined, next: readonly DeliveryStep[], reply?: ReplyWord): DeliveryStep[] {
  const out = STEPS.map((name) => {
    const n = next.find((s) => s.step === name)
    const p = prev?.find((s) => s.step === name)
    if (p && settled(p.state) && (!n || !settled(n.state))) return p
    return n ?? p ?? { step: name, state: 'unknown' as StepState, words: '' }
  })
  if (reply) {
    const word = replyStep(reply)
    for (const name of ['recorded', 'validated'] as const) {
      const i = STEPS.indexOf(name)
      const st = out[i]!.state
      if (word && (st === 'unknown' || (word.state === 'refused' && st === 'waiting'))) out[i] = { ...word, step: name }
    }
  }
  // A refused action resumes nothing: whatever the question or the task did after (another answer won, the run went on), it was not
  // this action's doing, and its steps end here.
  const r = STEPS.indexOf('resumed')
  if ((out[1]!.state === 'refused' || out[2]!.state === 'refused') && out[r]!.state !== 'not_applicable') {
    out[r] = { step: 'resumed', state: 'not_applicable', words: 'this action was refused: nothing resumed from it' }
  }
  return out
}

/** The step a reply's word gives, or null when it says nothing (an older owner). */
function replyStep(reply: ReplyWord): Omit<DeliveryStep, 'step'> | null {
  if (reply === 'accepted') return { state: 'done', words: "taken: the owner's reply to this browser's request said so" }
  if (reply.startsWith('refused:')) return { state: 'refused', words: reply.slice('refused:'.length).trim() || 'refused' }
  return null
}

/**
 * Whether an action's steps are final, so its track is read no more: its resumed step `done` or `not_applicable`, or its validated
 * step `refused`. A command whose receipt is `accepted` (validated waiting) is never final: its settlement follows, or the
 * recovery's `uncertain`, which is final once its resumed step is read from the task.
 */
export function stepsFinal(steps: readonly DeliveryStep[] | undefined): boolean {
  if (!steps?.length) return false
  const of = (n: StepName) => steps.find((s) => s.step === n)?.state
  const resumed = of('resumed')
  return resumed === 'done' || resumed === 'not_applicable' || of('validated') === 'refused'
}

// ---------------------------------------------------------------------------
// This browser's recent actions (before DR1: Owner decisions, 12).

/** Where this browser keeps its recent actions. */
export const RECENT_KEY = 'conductor.inbox.recent'
/** At most this many, the newest kept. */
export const RECENT_MAX = 50
/** None older than a day. */
export const RECENT_MS = 24 * 60 * 60 * 1000

/** One action this browser took: the item, its receipt's id (a command's, an operation's, or a question's id), the request id an answer went with, the owner's word on it, when. */
export interface RecentEntry {
  ref: string
  receipt: string
  /** An answer's request id: this browser's attempt, told apart from the winner's. */
  attempt?: string
  /** The owner's word on the request when it came (ReplyWord). */
  outcome?: string
  at: number
  /** The ref the steps are read under, when not the item's (the action's `track`). */
  track?: string
  /** The item's title and the action's label as they were, for the list. */
  title?: string
  label?: string
}

function entryOf(v: unknown): RecentEntry | null {
  if (!v || typeof v !== 'object') return null
  const e = v as Record<string, unknown>
  if (typeof e.ref !== 'string' || typeof e.receipt !== 'string' || typeof e.at !== 'number') return null
  const out: RecentEntry = { ref: e.ref, receipt: e.receipt, at: e.at }
  if (typeof e.attempt === 'string') out.attempt = e.attempt
  if (typeof e.outcome === 'string') out.outcome = e.outcome
  if (typeof e.track === 'string') out.track = e.track
  if (typeof e.title === 'string') out.title = e.title
  if (typeof e.label === 'string') out.label = e.label
  return out
}

/**
 * The list kept: none older than a day, the newest RECENT_MAX, newest first; and only actions on items DR1 does not hold (questions,
 * tasks, plan revisions): a DR1 record is answered through DR1's card, and its Recent is the server's, for every viewer
 * (docs/plans/decision-inbox.md, PR-4).
 */
export function keptRecent(list: readonly RecentEntry[], now = Date.now()): RecentEntry[] {
  return list
    .filter((e) => now - e.at < RECENT_MS && e.at <= now + 60_000 && !isDecisionRef(e.ref))
    .sort((a, b) => b.at - a.at)
    .slice(0, RECENT_MAX)
}

/** The recent actions this browser keeps; empty when the storage holds none, holds something else, or throws. */
export function readRecentActions(storage: Pick<Storage, 'getItem'> | null | undefined, now = Date.now()): RecentEntry[] {
  try {
    const raw = storage?.getItem(RECENT_KEY)
    if (!raw) return []
    const v = JSON.parse(raw) as unknown
    if (!Array.isArray(v)) return []
    return keptRecent(v.map(entryOf).filter((e): e is RecentEntry => !!e), now)
  } catch {
    return []
  }
}

/** Keeps the list in the storage; a storage that throws keeps nothing (the page works without it). */
export function writeRecentActions(storage: Pick<Storage, 'setItem'> | null | undefined, list: readonly RecentEntry[]): void {
  try {
    storage?.setItem(RECENT_KEY, JSON.stringify(list))
  } catch {
    /* storage refused: the list lasts until the page goes */
  }
}

/** The list with one more action, written before its reply comes back: a reload while it is out still tracks it. */
export function addRecent(list: readonly RecentEntry[], e: RecentEntry, now = Date.now()): RecentEntry[] {
  return keptRecent([e, ...list.filter((x) => !(x.ref === e.ref && x.receipt === e.receipt && x.attempt === e.attempt))], now)
}

/**
 * The recent actions this page holds (`mine`) with those the storage holds (another tab's, or this one's before a reload), newest
 * first: an action in both is kept once, with the owner's word where either has it. The page's own list is the one it follows, so
 * a storage that is empty or throws loses none of its actions.
 */
export function mergeRecent(mine: readonly RecentEntry[], stored: readonly RecentEntry[], now = Date.now()): RecentEntry[] {
  const by = new Map<string, RecentEntry>()
  for (const e of [...stored, ...mine]) {
    const k = trackKey(e)
    const had = by.get(k)
    by.set(k, had && had.outcome && !e.outcome ? had : e)
  }
  return keptRecent([...by.values()], now)
}

/**
 * The recent action a page shows for an item: the one named (a Recent row opens its own action, `receipt` and `attempt` in the
 * page's query), else the newest on that item.
 */
export function recentFor(list: readonly RecentEntry[], ref: string, pick?: { receipt?: string; attempt?: string }): RecentEntry | undefined {
  if (pick?.receipt) return list.find((e) => e.ref === ref && e.receipt === pick.receipt && (e.attempt ?? '') === (pick.attempt ?? ''))
  return list.find((e) => e.ref === ref)
}

/** The list without one action: a request that never left the browser (nothing was sent) is no action of this browser's. */
export function withoutRecent(list: readonly RecentEntry[], e: Pick<RecentEntry, 'ref' | 'receipt' | 'attempt'>): RecentEntry[] {
  const k = trackKey(e)
  return list.filter((x) => trackKey(x) !== k)
}

/** The list with the owner's word on one action written into its entry. */
export function withOutcome(list: readonly RecentEntry[], receipt: string, attempt: string | undefined, outcome: string): RecentEntry[] {
  return list.map((e) => (e.receipt === receipt && e.attempt === attempt ? { ...e, outcome } : e))
}

/** The key a recent action's steps are kept under in a page. */
export function trackKey(e: Pick<RecentEntry, 'ref' | 'receipt' | 'attempt'>): string {
  return `${e.ref}|${e.receipt}|${e.attempt ?? ''}`
}

/**
 * Whether the list may say that nothing needs a decision: only when its last read answered, and the server counts nothing open that a
 * person decides (its `total` less the DR1 records answered and not yet applied, barCount), whatever page the list holds: a first
 * page of such records alone, with an open item on the next, is not empty. A read that failed says nothing of what is open now, so
 * after it the page shows the failure, never an empty list.
 */
export function inboxEmpty(s: { loaded: boolean; total: number; tracking?: number; unavailable: string; error: string }): boolean {
  return s.loaded && barCount(s) === 0 && !s.unavailable && !s.error
}

// ---------------------------------------------------------------------------
// The phone's paths and the bar's count.

/** Tailwind's lg, where the sidebar shows and the bar at the foot does not. */
export const LG = 1024

/**
 * The bar's number: every open item, whatever page the list holds (the inbox's total), less the DR1 records answered and not yet
 * applied (`tracking`), which wait on Yardmaster, not on a person; none before the first read.
 */
export function barCount(page: Pick<InboxPage, 'total' | 'tracking'> | null | undefined): number {
  return page ? Math.max(0, page.total - Math.max(0, page.tracking ?? 0)) : 0
}

/** Whether a ref names a DR1 record: `decision:dr-<n>`. */
export function isDecisionRef(ref: string): boolean {
  return /^decision:dr-[1-9][0-9]{0,17}$/.test(ref)
}

/** The DR1 record's id a ref names (`dr-<n>`), or ''. */
export function decisionIdOf(ref: string): string {
  return isDecisionRef(ref) ? ref.slice('decision:'.length) : ''
}

/**
 * The time an item's card heads it with, at `now` (the page's clock): an open item "waiting <for how long>"; one no longer open never
 * waits: "answered <how long> ago" when its recorded step carries that time, else nothing. No longer open: a durable decision past
 * `open`; a question or a task the server says is not open now (`now`, which it sets on an open one of neither); a plan revision with
 * no action left (an open one is offered its Withdraw, and its `now` may say an approval no longer holds).
 */
export function itemTimeWords(it: Pick<InboxItem, 'kind' | 'state' | 'since' | 'now' | 'delivery'> & Partial<Pick<InboxItem, 'actions'>>, now: number): string {
  const closed = it.kind === 'decision' ? !!it.state && it.state !== 'open' : it.kind === 'plan' ? !it.actions?.length : (it.kind === 'question' || it.kind === 'task') && !!it.now
  if (!closed) return it.since ? `waiting ${relativeTime(it.since, now)}` : ''
  const at = it.delivery?.find((s) => s.step === 'recorded' && s.state === 'done')?.at
  return at ? `answered ${relativeTime(at, now, { suffix: true })}` : ''
}

/**
 * A row of a decision list (the page's open and answered rows, a decision link's): a column whose lines take the row's width
 * (`items-stretch`: a button starts its items at the start), so a long title is cut with an ellipsis and its waiting time stays in view.
 */
export const INBOX_ROW_CLASS = 'flex min-h-11 w-full flex-col items-stretch gap-1 rounded-md border px-3 py-2 text-left transition-colors hover:bg-elevated/50'

/**
 * The list's two parts, in the list's order: what needs a person now, and the DR1 records answered and not yet applied ("Answered, not
 * yet applied", from the server's list: they stay there until DR1 gives them a final state, whatever any browser keeps).
 */
export function inboxSections<T extends Pick<InboxSummary, 'tracking'>>(items: readonly T[]): { open: T[]; tracking: T[] } {
  return { open: items.filter((s) => !s.tracking), tracking: items.filter((s) => !!s.tracking) }
}

/** An item's kind in words, as its card heads it. */
export function inboxKindWords(kind: string): string {
  switch (kind) {
    case 'question':
      return 'A question for you'
    case 'task':
      return 'A task out of attempts'
    case 'plan':
      return 'A plan to approve'
    case 'decision':
      return 'A decision for you'
  }
  return kind
}

/**
 * A row's second line (UX1 PR-21): the question when it says something the title does not, else the brief, so that a run's decision,
 * titled by its question, does not say it twice.
 */
export function rowLine(s: Pick<InboxSummary, 'title' | 'question' | 'brief'>): string {
  return s.question && s.question !== s.title ? s.question : s.brief
}

/**
 * The item a wide window opens by itself (UX1 PR-21): the oldest open row's ref when the window is at lg (`wide`), no item is in the
 * query and the list holds an open row; '' otherwise. Rows answered and not yet applied, alone, open nothing.
 */
export function firstToOpen(wide: boolean, openRef: string, sections: { open: ReadonlyArray<Pick<InboxSummary, 'ref'>> }): string {
  return wide && !openRef ? (sections.open[0]?.ref ?? '') : ''
}

/**
 * The brief as the item draws it under its title (UX1 PR-21, D12): nothing when it is the title or the item's `now` (drawn already);
 * the rest, capitalised, when it starts with the title and "; " (a crew member's question: "It is attempt build.1 of run …"); else
 * the brief whole.
 */
export function briefLine(it: Pick<InboxItem, 'title' | 'brief' | 'now'>): string {
  const b = it.brief.trim()
  if (!b || b === it.title.trim() || (it.now && b === it.now.trim())) return ''
  const head = `${it.title.trim()}; `
  if (it.title.trim() && b.startsWith(head)) {
    const rest = b.slice(head.length).trim()
    return rest ? rest[0]!.toUpperCase() + rest.slice(1) : ''
  }
  return b
}

/** The four kinds of thing that need a person, in the order the page names them. */
export const INBOX_KINDS = ['question', 'plan', 'task', 'decision'] as const

/** A kind's mark: its icon, its word and the line that says what it is. */
export interface KindLook {
  icon: string
  word: string
  line: string
}

const KIND_LOOKS: Record<string, KindLook> = {
  question: { icon: 'i-lucide-message-circle-question', word: 'Question', line: 'A question an agent asks' },
  plan: { icon: 'i-lucide-clipboard-check', word: 'Plan', line: 'A plan that waits for your approval' },
  task: { icon: 'i-lucide-list-x', word: 'Task', line: 'A task that failed and is out of attempts' },
  // The icon a ticket's decisions already use.
  decision: { icon: 'i-lucide-scale', word: 'Decision', line: 'A decision a run needs from you' },
}

/** An item's kind as the page, the list's rows and the sidebar mark it; a kind the app does not know keeps its own name. */
export function kindLook(kind: string): KindLook {
  return KIND_LOOKS[kind] ?? { icon: 'i-lucide-circle-help', word: kind, line: kind }
}

/**
 * Whether the page is at zero, drawn as one column that says so: the list was read and answered (inboxEmpty: never after a failed
 * read, never on a switchyard), no record waits answered and not yet applied, and no item is open (`openRef`, the query's).
 */
export function atZero(
  s: { loaded: boolean; total: number; tracking?: number; unavailable: string; error: string; items: ReadonlyArray<Pick<InboxSummary, 'tracking'>> },
  openRef: string,
): boolean {
  return inboxEmpty(s) && !(s.tracking ?? 0) && !s.items.some((it) => it.tracking) && !openRef
}

/** Whether the open item's pane asks the person to pick one: no item is open and the list holds a row to pick. */
export function pickShown(sections: { open: readonly unknown[]; tracking: readonly unknown[] }, openRef: string): boolean {
  return !openRef && sections.open.length + sections.tracking.length > 0
}

/**
 * When the last thing closed, at `now`: "The last one closed 2h ago.", from the newest of the server's records closed in the last day
 * (`closed`) and this browser's own acts (`at`). Nothing when neither holds a time, or when the newest is ahead of `now`: the server
 * keeps no list of what another browser answered, so the line says nothing rather than guess.
 */
export function lastClosedWords(closed: ReadonlyArray<Pick<InboxSummary, 'closed'>>, recent: ReadonlyArray<Pick<RecentEntry, 'at'>>, now: number): string {
  const times = [...closed.map((c) => (c.closed ? Date.parse(c.closed) : Number.NaN)), ...recent.map((e) => e.at)].filter((t) => Number.isFinite(t))
  if (!times.length) return ''
  const newest = Math.max(...times)
  if (newest > now) return ''
  return `The last one closed ${relativeTime(new Date(newest).toISOString(), now, { suffix: true })}.`
}

/** History's own query for its Decision chip: every decision kept. */
export const HISTORY_DECISIONS = '/history?q=class%3Adecided'

/**
 * Where the first load of `/` in a tab goes on a phone (Owner decisions, 10): below lg with an item open, the decision page; at lg
 * and up, or with none open, nowhere (the home goes on as before).
 */
export function phoneLanding(width: number, open: number): string | null {
  return width < LG && open > 0 ? '/decisions' : null
}

/** Where the home is in judging the phone's landing: still judging it (this tab's first load of `/` below lg), and whether it chose the decision page. */
export interface HomeLanding {
  judging: boolean
  landed: boolean
}

/**
 * The home's next step (pages/index.vue), each time what it reads changes: while it judges the landing, it waits for the inbox's
 * first answer (or its failure, or the wait's end) and then goes to the decision page when an item is open (phoneLanding); once it
 * chose the decision page, nothing after (a session that changes the home's target while that navigation is under way) replaces
 * it. Otherwise the home goes where it goes (`home`, homeLeavesFor's), or nowhere.
 */
export function homeNext(
  l: HomeLanding,
  o: { wide: boolean; answered: boolean; late: boolean; width: number; open: number; home: string | null },
): { landing: HomeLanding; to: string | null } {
  if (l.landed) return { landing: l, to: null }
  if (l.judging && !o.wide) {
    if (!o.answered && !o.late) return { landing: l, to: null }
    const to = phoneLanding(o.width, o.open)
    if (to) return { landing: { judging: false, landed: true }, to }
  }
  return { landing: { judging: false, landed: false }, to: o.home }
}

/** Where a tab notes that its first load of the home was judged for the phone's landing. */
export const LANDED_KEY = 'conductor.inbox.landed'

/**
 * Whether this is the tab's first load of the home, noting it: true once per tab (the session storage keeps the mark for the tab's
 * life). A storage that throws or holds nothing answers true each page load: a landing then happens on each fresh load of `/`.
 */
export function firstLandingInTab(storage: Pick<Storage, 'getItem' | 'setItem'> | null | undefined): boolean {
  try {
    if (storage?.getItem(LANDED_KEY)) return false
    storage?.setItem(LANDED_KEY, '1')
  } catch {
    /* no storage: judged on this load */
  }
  return true
}

/** A question's ref: question:<session>/<question>. */
export function questionRef(session: string, question: string): string {
  return `question:${session}/${question}`
}

/** Where a question's browser notification goes when tapped: below lg its item on the decision page, at lg and up its session. */
export function notificationTarget(width: number, session: string, question?: string): string {
  if (width < LG && question) return `/decisions?item=${encodeURIComponent(questionRef(session, question))}`
  return `/sessions/${session}`
}

/** What Open on your phone offers: the page's address as a QR code, or, on a loopback origin, the words why a phone cannot reach it. */
export type PhoneOffer = { kind: 'qr'; url: string } | { kind: 'loopback'; words: string }

/**
 * Open on your phone (Owner decisions, 7): the page's address as the browser has it, its origin and `/decisions` alone (never a
 * token, a query or a fragment), as a QR code; on a loopback origin (the desktop app listens on 127.0.0.1) a phone cannot reach it,
 * and the offer says that, asking no step of the person, and offers a decision link instead (decisionLinkTargets, MB0's PR-7).
 */
export function phoneOffer(origin: string): PhoneOffer {
  let u: URL
  try {
    u = new URL(origin)
  } catch {
    return { kind: 'loopback', words: 'This page has no address a phone could open.' }
  }
  const host = u.hostname.replace(/^\[|\]$/g, '')
  if (host === 'localhost' || host.endsWith('.localhost') || /^127\./.test(host) || host === '::1' || host === '0.0.0.0') {
    return { kind: 'loopback', words: `A phone cannot reach this page from here: ${u.host} is this computer's own address, which only this computer opens.` }
  }
  return { kind: 'qr', url: `${u.origin}/decisions` }
}

// ---------------------------------------------------------------------------
// Acting.

/**
 * What an answer's failure says of it: `not_sent` when nothing left this browser (no connection, a view-only link, a reply that
 * names no question the page could tell the session), `unconfirmed` when it went and no word came (`no_word`: the owner said nothing
 * in time; `closed`: the connection went first), `refused` for the owner's own refusal (`question_answered` and its other codes),
 * which is the answer's receipt.
 */
export function answerFailure(e: unknown): 'not_sent' | 'unconfirmed' | 'refused' {
  if (!(e instanceof ReplyError)) return 'not_sent'
  if (e.code === 'no_word' || e.code === 'closed') return 'unconfirmed'
  if (e.code === 'not_connected' || e.code === NO_QUESTION) return 'not_sent'
  return 'refused'
}

/**
 * What a task command's or a plan operation's failure says of it: `refused` only for a refusal its receipt states (CommandRefused:
 * the reply carried the operation's receipt); anything else (a transport failure, a reply lost after sendCommand's one retry, an
 * answer with no receipt) is `unconfirmed`, never a refusal: the operation may have been done, and its track says.
 */
export function receiptFailure(e: unknown): 'unconfirmed' | 'refused' {
  return e instanceof CommandRefused ? 'refused' : 'unconfirmed'
}

/**
 * The words of an action whose outcome nobody confirmed, with a way to read again (never to send again by itself), or '' when its
 * outcome is known: confirmed or refused by a receipt, its recorded step settled by the server's track, or the request still out
 * from this page (`pending`). An action with no word and not pending went from an earlier load of the page, its reply never seen.
 */
export function unknownWords(e: RecentEntry | undefined, steps: readonly DeliveryStep[] | undefined, pending: boolean): string {
  if (!e || pending || (e.outcome && e.outcome !== UNCONFIRMED)) return ''
  const recorded = steps?.find((s) => s.step === 'recorded')?.state
  if (recorded === 'done' || recorded === 'refused') return ''
  if (e.attempt) return 'Your answer went, and nothing has said whether the session took it. Read again to see what the server knows now; answer again only if the agent still asks.'
  return 'Your request went, and no receipt came back to say whether it was taken. Read again to see what the server knows now; act again only if the item still asks for it.'
}

/**
 * The word a Recent row shows for one of this browser's actions, from its entry, its steps as last read and whether its request is
 * still out from this page (`pending`): Refused by the server's track; Not confirmed while nothing confirmed it was taken
 * (unknownWords), checked before any word of success, since a session that ended or resumed on another answer says nothing of this
 * one; then what the resumed step says; Sent otherwise.
 */
export function recentBadge(
  e: RecentEntry,
  steps: readonly DeliveryStep[] | undefined,
  pending: boolean,
): { text: string; color: 'success' | 'warning' | 'error' | 'neutral' } {
  const of = (n: StepName) => steps?.find((s) => s.step === n)?.state
  if (of('validated') === 'refused' || of('recorded') === 'refused') return { text: 'Refused', color: 'error' }
  // Went, and neither a receipt nor the server's track says what became of it.
  if (unknownWords(e, steps, pending)) return { text: 'Not confirmed', color: 'warning' }
  const r = of('resumed')
  if (r === 'done') return { text: 'Work resumed', color: 'success' }
  if (r === 'not_applicable') return { text: 'Taken', color: 'neutral' }
  if (r) return { text: `Resumed: ${stateWords(r).toLowerCase()}`, color: 'warning' }
  return { text: 'Sent', color: 'neutral' }
}

/**
 * Where a question an answer was refused for can still be answered, from the server's track of it read after the refusal (`steps`,
 * as the server gave them): a question still waiting (shown to another answer under way, or set aside or held over to show again)
 * is answered here once the page offers it again, or in the session's terminal; '' for one that is gone.
 */
export function whereToAnswer(steps: readonly DeliveryStep[] | undefined): string {
  const recorded = steps?.find((s) => s.step === 'recorded')
  if (recorded?.state !== 'waiting') return ''
  return `The question still waits (${recorded.words}): answer it here once this page offers it again, or in the session's terminal.`
}

/** A request id for an answer, fresh per action, so this browser's attempt and the winner are told apart: 16 hex characters. */
export function mintRequestId(): string {
  const bytes = new Uint8Array(8)
  const c = (globalThis as { crypto?: { getRandomValues?: (b: Uint8Array) => void } }).crypto
  if (c?.getRandomValues) c.getRandomValues(bytes)
  else for (let i = 0; i < bytes.length; i++) bytes[i] = Math.floor(Math.random() * 256)
  return 'd' + Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('')
}

/** The plan revision's id an approve or withdraw route names (`POST /api/dispatch/plans/{id}/approve`), or ''. */
export function planIdOf(route: string): string {
  return /\/api\/dispatch\/plans\/([^/]+)\/(approve|withdraw)$/.exec(route)?.[1] ?? ''
}

/** The session and the question a question ref names, the run and the task a task ref names. */
export function refParts(ref: string): { kind: string; a: string; b: string } {
  const colon = ref.indexOf(':')
  const rest = colon < 0 ? '' : ref.slice(colon + 1)
  const slash = rest.lastIndexOf('/')
  return { kind: colon < 0 ? '' : ref.slice(0, colon), a: slash < 0 ? rest : rest.slice(0, slash), b: slash < 0 ? '' : rest.slice(slash + 1) }
}

/**
 * The words for an action the operation refused because what it was about moved on (stale_revision), after the item was read
 * again: what it is now, and that nothing was done.
 */
export function movedOnWords(kind: string, now: Pick<InboxItem, 'now' | 'title'> | null): string {
  const what = kind === 'plan' ? 'The revision or its ticket moved on' : kind === 'question' ? 'The question moved on' : 'The run moved on'
  const state = now?.now ? `: ${now.now}` : ''
  return `${what}${state}. Nothing was done.`
}

/**
 * What a Check again refused because the run moved on (stale_revision) says stands now, from its item read again: the candidate the
 * item's Check again names now, the engine's own rule judging it, or that none stands it takes; '' when the item could not be read.
 */
export function recheckStands(now: Pick<InboxItem, 'actions'> | null): string {
  if (!now) return ''
  const c = now.actions.find((a) => a.name === 'recheck')
  return c?.attempt && c.commit ? `; the candidate that stands is attempt ${c.attempt} at ${c.commit.slice(0, 12)}, which Check again offers now` : '; no candidate stands that Check again takes'
}

/** The ticket a run of the item was launched from, as its evidence names it (`ticket:<KEY-n>`), for Comment on the ticket. */
export function ticketOf(item: Pick<InboxItem, 'kind' | 'evidence'>): string {
  if (item.kind !== 'task' && item.kind !== 'question') return ''
  const t = item.evidence?.find((e) => e.ref.startsWith('ticket:'))
  return t ? t.ref.slice('ticket:'.length) : ''
}

/** What a decision link may cover, for Open on your phone on a loopback origin: a run or a session the person picks. */
export interface DecisionLinkTarget {
  /** `run:<id>` or `session:<id>`. */
  value: string
  label: string
}

/**
 * What Open on your phone says of the runs it does not offer (decisionLinkTargets), as Share answers for them: a stopped run is not
 * offered, and a run the server keeps only as its record has no links at all (`run_record`, shareRefusal); a selection either still
 * holds is answered on Needs you, which no link answers (a link cannot answer a run's decision record).
 */
export const PHONE_NOT_OFFERED =
  'Stopped runs are not offered here, and a run the server keeps only as its record has no links at all. A selection such a run still holds is answered on Needs you, not through a link.'

/**
 * The runs and sessions a decision link can be made for (Open on your phone, on a loopback origin, MB0's PR-7): the runs the engine
 * still holds (running, or finished and still retried: a failed run's tasks are its decisions), newest first, then this server's own
 * sessions still running (a hosted session's decisions are answered where it runs). A run's members are covered by the run's link.
 */
export function decisionLinkTargets(
  sessions: ReadonlyArray<{ id: string; name: string; kind: string; status: string; crew?: { runId: string } }>,
  runs: ReadonlyArray<{ id: string; name: string; label?: string; state: string }>,
): DecisionLinkTarget[] {
  const out: DecisionLinkTarget[] = []
  for (const r of runs) if (r.state !== 'stopped') out.push({ value: `run:${r.id}`, label: `Run: ${r.label || r.name}` })
  for (const s of sessions) {
    if (s.kind !== 'server' || s.crew?.runId || s.status === 'exited' || s.status === 'stopped') continue
    out.push({ value: `session:${s.id}`, label: `Session: ${s.name}` })
  }
  return out
}

/** The labels the server gives a record's Comment and its Ask for more (internal/api/inbox_decisions.go), which answer nothing. */
export const COMMENT_LABEL = 'Comment'
export const MORE_INFO_LABEL = 'Ask for more information'

/**
 * The answers a person can give a listed item, of its actions' labels (the list's summary carries the labels alone): every action of
 * a question, a plan revision and a task; a decision's options and its line of words, never its Comment or its Ask for more. The
 * server lists a decision's actions in one order, its answers, then Ask for more when it is offered, then Comment always last, so the
 * two are taken off its end by their fixed labels. The summary gives the labels alone (no action's name, no flag that the asker is
 * live), so the count goes by them: an option called "Comment" anywhere but last, or "Ask for more information" anywhere but just
 * before Comment, is counted; one called "Ask for more information" and listed last, with no Ask for more offered, is not, and the
 * badge then says one choice fewer (the card still shows every option). Telling them apart would need the summary to carry the
 * actions' names, a protocol change this count does not make.
 */
export function answersOf(kind: string, actions: readonly string[]): string[] {
  const out = [...actions]
  if (kind !== 'decision') return out
  if (out[out.length - 1] === COMMENT_LABEL) out.pop()
  if (out[out.length - 1] === MORE_INFO_LABEL) out.pop()
  return out
}

/** A row's count of its answers ("3 choices"), '' for an item with none. */
export function choicesWords(s: Pick<InboxSummary, 'kind' | 'actions'>): string {
  const n = answersOf(s.kind, s.actions).length
  return n ? `${n} ${n === 1 ? 'choice' : 'choices'}` : ''
}
