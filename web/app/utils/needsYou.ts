import type { SessionInfo } from '~/composables/useSessions'
import type { Decision } from '~/utils/decisions'
import { runRuns } from '~/utils/decisions'
import { isEnded } from '~/utils/attention'
import { inboxOrder, questionRef, RECENT_MS, refParts, type InboxSummary, type RecentEntry } from '~/utils/inbox'

/**
 * What needs a person, as one list (the UX review's X-needs-model): every count of it on the desktop reads this, never a number made
 * by subtracting one store from another. An item is one thing a person can act on: a session's question, an open durable decision no
 * shown question mirrors, a task or a plan that waits on a person.
 */

export type NeedsKind = 'question' | 'decision' | 'task' | 'plan'

export interface NeedsItem {
  kind: NeedsKind
  /** The inbox's ref of it: question:<session>/<question>, decision:dr-<n>, task:<run>/<task>, plan:<KEY-n>/<n>. */
  ref: string
  /** The run it waits in, when it has one: a member's question, a decision's run, a task's run. */
  run?: string
  /** Since when it waits. */
  since: string
  /** A question's session. */
  session?: SessionInfo
  /** A decision's record; for a question that mirrors an open record, that record (the question is its one item). */
  decision?: Decision
  /** A decision whose run no longer runs (stopped, kept as a record, or continued by another): answered from its run's or ticket's page. */
  stopped?: boolean
  /** A task's or a plan's summary, as the inbox lists it. */
  summary?: InboxSummary
}

/** A store's reading: read once at least, its last read failed (its words), cut short. */
export interface StoreRead {
  loaded: boolean
  error: string
}

export interface NeedsInput {
  /** The attention's sessions (useAttention: the live form of each), and whether they were read. */
  sessions: StoreRead & { list: readonly SessionInfo[] }
  /** The decisions store (useDecisions): its records, whether it is cut, and why it is not served here ('' when it is: a switchyard). */
  decisions: StoreRead & { records: readonly Decision[]; cut: boolean; unavailable?: string }
  /** The inbox (useInbox's state): its items, its next page's cursor, and why it is not served here. */
  inbox: StoreRead & { items: readonly InboxSummary[]; next: string; unavailable: string }
  /** The run with this id as last read (useAttention's runOf), for whether a decision's run still runs. */
  runOf?: (id: string) => { stoppedAt?: string; record?: boolean; resumedBy?: string } | undefined
}

export interface NeedsYou {
  /** Every item, oldest first, by (since, ref) as the inbox orders them. */
  items: NeedsItem[]
  /** The items the pill and the sidebar's row can answer: the questions and the decisions. */
  answerable: NeedsItem[]
  /** The ids of the runs that wait on a person, in the order their first item waits. */
  runs: string[]
  /** How many items: the count when `known`, a floor of it when `more`, and to be drawn as no number otherwise. */
  number: number
  /** Every store was read, none failed, none was cut short: `number` is the count. Unknown is not zero. */
  known: boolean
  /** Every store was read and none failed, but one was cut short (the inbox has a next page, the decisions store is cut): drawn "N+". */
  more: boolean
}

/**
 * The one list. A question is a session not ended whose attention asks (a host away among them), counted once; a question that
 * mirrors an open record is that record's one item. A decision is an open record no counted question mirrors, of any run, a stopped
 * run's too, as the server counts it. A task or a plan is the inbox's summary of that kind. The inbox's own question and decision
 * summaries are never counted: a new question counts the moment its session says so, and a gate the moment the decisions store reads
 * it. A record answered and not yet applied is not open, and is not counted.
 *
 * On a switchyard neither the inbox nor the decisions are served (each answers 403 `switchyard`): each counts as read with nothing in
 * it, and the number is the sessions that ask.
 */
export function needsYou(input: NeedsInput): NeedsYou {
  const items: NeedsItem[] = []
  const records = new Map(input.decisions.records.filter((d) => d.state === 'open').map((d) => [d.id, d]))
  const mirrored = new Set<string>()
  for (const s of input.sessions.list) {
    const a = s.attention
    if (a?.state !== 'needs_input' || isEnded(s.status)) continue
    const record = a.decision ? records.get(a.decision) : undefined
    if (record) mirrored.add(record.id)
    // A question that mirrors a record waits in the record's run as well as in its session.
    const run = s.crew?.runId || record?.run || undefined
    items.push({ kind: 'question', ref: questionRef(s.id, a.id ?? ''), run, since: a.since ?? '', session: s, ...(record ? { decision: record } : {}) })
  }
  for (const d of records.values()) {
    if (mirrored.has(d.id)) continue
    const stopped = input.runOf ? !runRuns(input.runOf(d.run)) : false
    items.push({ kind: 'decision', ref: `decision:${d.id}`, run: d.run || undefined, since: d.createdAt, decision: d, stopped })
  }
  for (const it of input.inbox.unavailable ? [] : input.inbox.items) {
    if (it.kind !== 'task' && it.kind !== 'plan') continue
    const run = it.kind === 'task' ? refParts(it.ref).a : ''
    items.push({ kind: it.kind, ref: it.ref, run: run || undefined, since: it.since, summary: it })
  }
  const ordered = inboxOrder(items)
  const runs: string[] = []
  for (const it of ordered) if (it.run && !runs.includes(it.run)) runs.push(it.run)
  // A store not served here is read with nothing in it; a last read that failed is a failure whatever was read before it.
  const decisionsRead = !input.decisions.error && (input.decisions.loaded || !!input.decisions.unavailable)
  const inboxRead = !input.inbox.error && (input.inbox.loaded || !!input.inbox.unavailable)
  const read = input.sessions.loaded && !input.sessions.error && decisionsRead && inboxRead
  const cut = (!input.decisions.unavailable && input.decisions.cut) || (!input.inbox.unavailable && !!input.inbox.next)
  return {
    items: ordered,
    answerable: ordered.filter((it) => it.kind === 'question' || it.kind === 'decision'),
    runs,
    number: ordered.length,
    known: read && !cut,
    more: read && cut,
  }
}

/** The one number as a reader draws it: "9", "9+" past a store cut short, and nothing while it is not known (unknown is not zero). */
export function needsCount(n: Pick<NeedsYou, 'number' | 'known' | 'more'>): string {
  if (n.known) return String(n.number)
  return n.more ? `${n.number}+` : ''
}

/**
 * Whether Needs you's row shows in the nav. It shows while the one number is not known (a store unread, failed or cut short), while an
 * item waits, while a record is answered and not yet applied (`tracking`), while something was decided in the last day (the server's
 * `closed`, or this browser's `recent`), and while its page is open; it leaves when nothing waits and nothing was decided that day. On
 * a switchyard, which serves no inbox, it is hidden.
 */
export function needsRowShown(
  state: { unavailable: string; tracking: number; closed: ReadonlyArray<Pick<InboxSummary, 'closed' | 'since'>>; recent: ReadonlyArray<Pick<RecentEntry, 'at'>> },
  needs: Pick<NeedsYou, 'number' | 'known'>,
  path: string,
  now: number,
): boolean {
  if (state.unavailable) return false
  if (path === '/decisions' || path.startsWith('/decisions/')) return true
  if (!needs.known || needs.number > 0 || state.tracking > 0) return true
  const within = (t: number) => !Number.isNaN(t) && now - t < RECENT_MS
  return state.closed.some((c) => within(Date.parse(c.closed ?? c.since))) || state.recent.some((e) => within(e.at))
}

/**
 * The sidebar section's header, while it lists fewer kinds than the page (questions and the decisions of runs that run): its own count
 * and the items of the one list it does not list (plans, tasks, the open decisions of stopped runs), counted from the list, never as a
 * difference of two stores. "Needs you 6 · +3 on the page"; "Needs you 6" when it lists them all.
 */
export function sectionHeader(listed: number, items: readonly NeedsItem[]): { listed: number; more: number; words: string } {
  const more = items.filter((it) => it.kind === 'plan' || it.kind === 'task' || (it.kind === 'decision' && it.stopped)).length
  return { listed, more, words: more ? `Needs you ${listed} · +${more} on the page` : `Needs you ${listed}` }
}

/** The items the pill cannot answer (plans and tasks, answered on the page). */
export function unanswerable(items: readonly NeedsItem[]): number {
  return items.filter((it) => it.kind === 'plan' || it.kind === 'task').length
}

/** The pill's count: "2 of 4", and " · 2 more in Needs you" for the items it cannot answer. */
export function pillWords(n: number, of: number, more: number): string {
  return `${n} of ${of}${more ? ` · ${more} more in Needs you` : ''}`
}
