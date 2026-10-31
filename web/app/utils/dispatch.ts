import type { CloseRequirement, CloseState, DispatchChange, DispatchSort, DispatchTicket, Horizon, LabelColor, RelatedTicket, TicketAttachment, TicketComment, TicketEdit, TicketLink, TicketRelation, TicketStatus, TicketSummary, Tracker } from '~/composables/useDispatch'
import type { Tone } from './tasks'

// Dispatch's words (docs/plans/dispatch.md, "web/app"): the key parser; the vocabularies of a ticket's fields with their words, colours
// and icons; a ticket's state in words; the close policy's words; the list's sorts; the board's and the roadmap's columns; the list's
// keyboard model, a roving focus by key; a ticket's history in words. Pure: the pages read them, vitest proves them.

// ---------------------------------------------------------------------------
// Keys

/** A project's key: one upper-case letter, then one to five upper-case letters or digits (CND), chosen at creation and never changed. */
export const PROJECT_KEY_PATTERN = /^[A-Z][A-Z0-9]{1,5}$/

/** Whether `key` is a project's key. */
export function validProjectKey(key: string): boolean {
  return PROJECT_KEY_PATTERN.test(key)
}

export interface TicketKeyParts {
  /** The project's key: CND. */
  project: string
  /** The ticket's number in its project: 12. */
  number: number
  /** The key as the store writes it: CND-12. */
  key: string
}

/**
 * A ticket's key read as the store reads one (dispatch.ParseKey): its project's key, a dash and its number, digits without a leading zero,
 * 1 or more, at most 18 of them; null for anything else, a key in lower case among it.
 */
export function parseTicketKey(s: string): TicketKeyParts | null {
  const m = /^([A-Z][A-Z0-9]{1,5})-([1-9][0-9]{0,17})$/.exec(s)
  if (!m) return null
  return { project: m[1]!, number: Number(m[2]), key: s }
}

/** A project's page: /dispatch/CND. */
export function projectPath(project: string): string {
  return `/dispatch/${encodeURIComponent(project)}`
}

/** A ticket's page by its key: /dispatch/CND/12; '' for what is no ticket's key. */
export function ticketPath(key: string): string {
  const k = parseTicketKey(key)
  return k ? `/dispatch/${k.project}/${k.key.slice(k.project.length + 1)}` : ''
}

// ---------------------------------------------------------------------------
// The vocabularies

/** A colour of Nuxt UI's palette (app.config.ts): status colours, never the brand's terracotta. */
export type WordColor = 'primary' | 'success' | 'info' | 'warning' | 'error' | 'neutral'

/** A value in words, with its icon and its colour. */
export interface Word {
  label: string
  icon: string
  color: WordColor
}

/** A ticket's statuses in a board's order (Owner decisions, 4). */
export const TICKET_STATUSES: readonly TicketStatus[] = ['backlog', 'ready', 'in_progress', 'in_review', 'done', 'cancelled']

/** As a run's task: in progress green as running, in review blue as checking, done the brand's colour, the waiting hollow, cancelled grey. */
const STATUS_WORDS: Record<TicketStatus, Word> = {
  backlog: { label: 'Backlog', icon: 'i-lucide-circle-dashed', color: 'neutral' },
  ready: { label: 'Ready', icon: 'i-lucide-circle', color: 'neutral' },
  in_progress: { label: 'In progress', icon: 'i-lucide-circle-play', color: 'success' },
  in_review: { label: 'In review', icon: 'i-lucide-circle-dot', color: 'info' },
  done: { label: 'Done', icon: 'i-lucide-circle-check', color: 'primary' },
  cancelled: { label: 'Cancelled', icon: 'i-lucide-circle-x', color: 'neutral' },
}

/** A status in words; one the vocabulary lacks (a newer server's) as it is. */
export function statusWord(status: string): Word {
  return STATUS_WORDS[status as TicketStatus] ?? { label: status, icon: 'i-lucide-circle', color: 'neutral' }
}

export interface PriorityWord extends Word {
  /** The priority's number, 0 (none) to 4 (urgent). */
  value: number
  /** Its name in a link's query: ?priority=high. */
  name: 'none' | 'low' | 'medium' | 'high' | 'urgent'
}

/** A ticket's priorities by their number: none 0 to urgent 4. Urgent alone is coloured: a priority is no operational status. */
export const PRIORITIES: readonly PriorityWord[] = [
  { value: 0, name: 'none', label: 'No priority', icon: 'i-lucide-minus', color: 'neutral' },
  { value: 1, name: 'low', label: 'Low', icon: 'i-lucide-signal-low', color: 'neutral' },
  { value: 2, name: 'medium', label: 'Medium', icon: 'i-lucide-signal-medium', color: 'neutral' },
  { value: 3, name: 'high', label: 'High', icon: 'i-lucide-signal-high', color: 'neutral' },
  { value: 4, name: 'urgent', label: 'Urgent', icon: 'i-lucide-triangle-alert', color: 'error' },
]

/** A priority in words; one outside 0 to 4 as its number. */
export function priorityWord(p: number): PriorityWord {
  return PRIORITIES[p] ?? { value: p, name: 'none', label: `Priority ${p}`, icon: 'i-lucide-minus', color: 'neutral' }
}

export interface HorizonWord extends Word {
  value: Horizon
  /** Its name in a link's query: ?horizon=now, `none` for not planned. */
  name: 'now' | 'next' | 'later' | 'none'
}

/** A ticket's horizons in the roadmap's order: Now, Next, Later, then Not planned. */
export const HORIZONS: readonly HorizonWord[] = [
  { value: 'now', name: 'now', label: 'Now', icon: 'i-lucide-zap', color: 'neutral' },
  { value: 'next', name: 'next', label: 'Next', icon: 'i-lucide-arrow-right', color: 'neutral' },
  { value: 'later', name: 'later', label: 'Later', icon: 'i-lucide-clock', color: 'neutral' },
  { value: '', name: 'none', label: 'Not planned', icon: 'i-lucide-minus', color: 'neutral' },
]

/** A horizon in words; one the vocabulary lacks as it is. */
export function horizonWord(h: string): HorizonWord {
  return HORIZONS.find((x) => x.value === h) ?? { value: h as Horizon, name: 'none', label: h, icon: 'i-lucide-minus', color: 'neutral' }
}

/**
 * A label's colour as a dot: each of the store's twelve names on one of the brand's scales at a shade (docs/design/brand.md; the store
 * keeps a name, never a value a client chose), terracotta never among them, since it is the brand's artwork.
 */
export const LABEL_COLORS: Record<LabelColor, string> = {
  gray: 'bg-zinc-400',
  brown: 'bg-signal-800',
  red: 'bg-brick-500',
  orange: 'bg-signal-500',
  amber: 'bg-signal-400',
  yellow: 'bg-signal-200',
  lime: 'bg-active-300',
  green: 'bg-active-500',
  teal: 'bg-forest-400',
  cyan: 'bg-harbor-300',
  blue: 'bg-harbor-500',
  indigo: 'bg-harbor-700',
}

/** A label's dot; grey for a colour the vocabulary lacks. */
export function labelDot(color: string): string {
  return LABEL_COLORS[color as LabelColor] ?? LABEL_COLORS.gray
}

/** The twelve colours a label may be given, in the order a picker offers them, each with its name in words and its dot. */
export const LABEL_COLOR_CHOICES: ReadonlyArray<{ value: LabelColor; label: string; dot: string }> = (Object.keys(LABEL_COLORS) as LabelColor[]).map((value) => ({
  value,
  label: value[0]!.toUpperCase() + value.slice(1),
  dot: LABEL_COLORS[value],
}))

/** Whether `color` is one of the store's twelve names: a label is never given a value of the client's own. */
export function isLabelColor(color: string): color is LabelColor {
  return Object.hasOwn(LABEL_COLORS, color)
}

/** A project's tracker in words: Dispatch itself, or a pointer at another one. */
export const TRACKERS: Record<Tracker, { label: string; icon: string }> = {
  native: { label: 'Dispatch', icon: 'i-lucide-ticket' },
  external: { label: 'External', icon: 'i-lucide-external-link' },
}

// ---------------------------------------------------------------------------
// A ticket in words

/** A ticket's state in words, as a run's task has its own: deleted, closed, blocked by an open ticket, or its status. */
export function ticketWords(t: Pick<TicketSummary, 'status' | 'blocked'> & { deletedAt?: string }): { text: string; tone: Tone } {
  if (t.deletedAt) return { text: 'Deleted', tone: 'muted' }
  const status = statusWord(t.status).label
  if (t.status === 'done') return { text: status, tone: 'success' }
  if (t.status === 'cancelled') return { text: status, tone: 'muted' }
  if (t.blocked) return { text: `${status} · blocked`, tone: 'warning' }
  return { text: status, tone: 'default' }
}

/**
 * The edit that takes a ticket's assignees from `before` to `next`: the members to add and to remove, in one operation; null when they
 * are the same, since the store refuses an edit that changes nothing.
 */
export function assigneesEdit(before: readonly string[], next: readonly string[]): TicketEdit | null {
  return membersEdit('assignees', before, next)
}

/**
 * The edit that takes a ticket's members of a set (its assignees or its labels) from `before` to `next`: the members to add and to remove,
 * in one operation; null when they are the same.
 */
export function membersEdit(field: 'assignees' | 'labels', before: readonly string[], next: readonly string[]): TicketEdit | null {
  const add = next.filter((a) => !before.includes(a))
  const remove = before.filter((a) => !next.includes(a))
  if (!add.length && !remove.length) return null
  const change: TicketEdit = {}
  if (add.length) change.add = { [field]: add }
  if (remove.length) change.remove = { [field]: remove }
  return change
}

/**
 * A person's picks of a ticket's assignees, from the click until the edit that carries them has answered (DispatchTicketPage): each member
 * ticked or unticked, as the person did it, against the list the picker showed. The picker shows the ticket's assignees with the picks
 * applied (`shown`), so a second tick made while the first one's edit is on its way builds on it. An edit is made when it is sent, from
 * the picks pending then and the ticket as the edit before it left it (`take`): it adds and removes only the members the person touched,
 * and a member ticked and unticked before it went is no change at all. A pick stays pending until the edit that carried it has answered,
 * done or not (`settle`), unless the person picked that member again since.
 */
export class AssigneePicks {
  /** By actor: whether the person wants it assigned, and the number of the pick; a later pick of the same actor replaces it. */
  private readonly picks = new Map<string, { on: boolean; n: number }>()
  private n = 0

  /** The set the picks are of: a ticket's assignees (actors), or its labels (ids), picked the same way. */
  constructor(private readonly field: 'assignees' | 'labels' = 'assignees') {}

  /** What the picker shows: the ticket's assignees without those unticked, then those ticked, in the order picked. */
  shown(assignees: readonly string[]): string[] {
    const out = assignees.filter((a) => this.picks.get(a)?.on !== false)
    for (const [a, p] of this.picks) if (p.on && !out.includes(a)) out.push(a)
    return out
  }

  /** The picker's new list, against the list it showed: each member ticked or unticked is a pick. Answers the picks made, to settle. */
  pick(shown: readonly string[], next: readonly string[]): Map<string, number> {
    const made = new Map<string, number>()
    for (const a of next) if (!shown.includes(a)) made.set(a, ++this.n)
    for (const a of shown) if (!next.includes(a)) made.set(a, ++this.n)
    for (const [a, n] of made) this.picks.set(a, { on: next.includes(a), n })
    return made
  }

  /**
   * The edit that makes every pending pick hold on the ticket's assignees as they are now (null when they all hold already), and the
   * picks it carries, to settle once it has answered.
   */
  take(assignees: readonly string[]): { edit: TicketEdit | null; carried: Map<string, number> } {
    return { edit: membersEdit(this.field, assignees, this.shown(assignees)), carried: new Map([...this.picks].map(([a, p]) => [a, p.n])) }
  }

  /** The picks given are no longer pending (their edit answered, done or not), each unless its member was picked again since. */
  settle(picks: ReadonlyMap<string, number>): void {
    for (const [a, n] of picks) if (this.picks.get(a)?.n === n) this.picks.delete(a)
  }

  /** Every pick forgotten: the page shows another ticket. */
  clear(): void {
    this.picks.clear()
  }
}

/** An assignee's actor read: a person by name (person:Alice), or one of the catalog's agents by id (agent:claude). */
export function assigneeOf(actor: string): { kind: 'person' | 'agent'; name: string } {
  if (actor.startsWith('agent:')) return { kind: 'agent', name: actor.slice(6) }
  return { kind: 'person', name: actor.startsWith('person:') ? actor.slice(7) : actor }
}

// ---------------------------------------------------------------------------
// The close policy

const REQUIREMENT_WORDS: Record<CloseRequirement, string> = {
  acceptance: "the owner's acceptance",
  delivery: 'a delivery link',
}

/** What a close policy requires, in words: "the owner's acceptance". */
export function requirementWords(r: string): string {
  return REQUIREMENT_WORDS[r as CloseRequirement] ?? r
}

/** "a", "a and b", "a, b and c". */
function andList(items: readonly string[]): string {
  if (items.length <= 1) return items[0] ?? ''
  return `${items.slice(0, -1).join(', ')} and ${items.at(-1)}`
}

export interface ClosePolicyWords {
  /** "To close: the owner's acceptance · a delivery link", or that nothing is required. */
  summary: string
  /** Each requirement with whether the ticket meets it. */
  items: Array<{ requirement: CloseRequirement; words: string; met: boolean }>
  /** Why an acceptance on record is unmet ("accepted for Alice; the owner is now Bob"), '' when none is. */
  why: string
}

/** A ticket's close policy in words, from its read's `close`. */
export function closePolicyWords(close: CloseState): ClosePolicyWords {
  const items = close.requires.map((r) => ({ requirement: r, words: requirementWords(r), met: !!close.met[r] }))
  return {
    summary: items.length ? `To close: ${items.map((i) => i.words).join(' · ')}` : 'Closes with nothing required',
    items,
    why: close.why ?? '',
  }
}

/** A move to Done the policy refused (409 close_policy), from the receipt's `missing`: "It needs the owner's acceptance to be done." */
export function closeRefusalWords(missing: readonly string[]): string {
  if (!missing.length) return 'The close policy refused it.'
  return `It needs ${andList(missing.map(requirementWords))} to be done.`
}

/** A run link refused (409 run_linked) because another live ticket holds the run, the receipt's key naming it: a run belongs to one ticket. */
export function runLinkedWords(holder?: string): string {
  return holder ? `The run is linked to ${holder} already: a run belongs to one ticket.` : 'The run is linked to another ticket already: a run belongs to one ticket.'
}

/** What a project's close policy may require, in the order a policy is kept and said: `verified` is not offered (DS2 defines it). */
export const CLOSE_REQUIREMENTS: ReadonlyArray<{ value: CloseRequirement; label: string; description: string }> = [
  { value: 'acceptance', label: "The owner's acceptance", description: 'Recorded by a button of its own, for the owner and against the criteria as they stand' },
  { value: 'delivery', label: 'A delivery link', description: "The merged pull request's or the release's URL" },
]

/** A close policy as the person ticked it, in the order the policy keeps (acceptance, then delivery), each once. */
export function closeRequiresOf(picked: readonly string[]): CloseRequirement[] {
  return CLOSE_REQUIREMENTS.map((r) => r.value).filter((r) => picked.includes(r))
}

/** Whether two close policies require the same things, whatever their order. */
export function samePolicy(a: readonly string[], b: readonly string[]): boolean {
  const x = closeRequiresOf(a)
  const y = closeRequiresOf(b)
  return x.length === y.length && x.every((r, i) => r === y[i])
}

/** A ticket as its close card reads it: its policy's state, its owner, its acceptance on record, its links. */
export type ClosingTicket = Pick<DispatchTicket, 'close' | 'owner' | 'status' | 'acceptedAt' | 'acceptedBy' | 'acceptedFor' | 'acceptedNote' | 'links'>

/** One line of the close card: a requirement of the policy, whether the ticket meets it, and its state in words. */
export interface CloseCardItem {
  requirement: CloseRequirement
  words: string
  met: boolean
  /** "Accepted by Nate for Alice", "accepted for Alice; the owner is now Bob", "No delivery link yet". */
  state: string
}

/** The close card's words (DispatchClosePolicy.vue): what closing takes, each requirement's state, the acceptance's own, and the Close. */
export interface CloseCard {
  /** "To close: the owner's acceptance · a delivery link", or that nothing is required. */
  summary: string
  items: CloseCardItem[]
  /** The acceptance on record in words, whether the policy requires it or not: it is a fact of its own. */
  acceptance: { text: string; tone: Tone; recorded: boolean; met: boolean }
  /** Why Accept cannot be made now ('' when it can): a ticket with no owner has no one to accept it for. */
  acceptBlocked: string
  /** Whether the ticket is closed (done or cancelled): the card offers no Close then. */
  closed: boolean
  /** Whether the policy is met, as read: the Close is still sent and the store decides, its refusal said in words. */
  ready: boolean
}

/** The live delivery links of a ticket. */
export function deliveryLinks(links: readonly TicketLink[]): TicketLink[] {
  return links.filter((l) => l.kind === 'delivery' && !l.deletedAt)
}

/** A ticket's close card, from its read: `close` as the server computed it, never re-derived here, the words around it. */
export function closeCard(t: ClosingTicket): CloseCard {
  const policy = closePolicyWords(t.close)
  const recorded = !!t.acceptedAt
  const met = !!t.close.met.acceptance
  const by = t.acceptedBy || 'admin'
  let acceptance: CloseCard['acceptance']
  if (recorded && met) acceptance = { text: `Accepted by ${by} for ${t.acceptedFor || t.owner}`, tone: 'success', recorded, met }
  else if (recorded) acceptance = { text: t.close.why || `Accepted by ${by} for ${t.acceptedFor}, no longer met`, tone: 'warning', recorded, met }
  else if (t.owner) acceptance = { text: `Not accepted yet: ${t.owner} is the owner`, tone: 'muted', recorded, met }
  else acceptance = { text: 'Not accepted yet: the ticket has no owner to accept it', tone: 'muted', recorded, met }
  const delivered = deliveryLinks(t.links ?? []).length
  const items = policy.items.map((i): CloseCardItem => {
    if (i.requirement === 'acceptance') return { ...i, state: acceptance.text }
    return { ...i, state: delivered ? `Delivered: ${delivered === 1 ? 'one link' : `${delivered} links`}` : 'No delivery link yet' }
  })
  return {
    summary: policy.summary,
    items,
    acceptance,
    acceptBlocked: t.owner ? '' : 'It needs an owner to accept it for.',
    closed: t.status === 'done' || t.status === 'cancelled',
    ready: !t.close.missing.length,
  }
}

/**
 * A link's URL in short, for its row: a GitHub pull request or issue as `owner/repo#12`, a release as `owner/repo v1.2`, else its host
 * and path. The whole URL is the row's link and its title.
 */
export function linkLabel(url: string): string {
  let u: URL
  try {
    u = new URL(url)
  } catch {
    return url
  }
  const parts = u.pathname.split('/').filter(Boolean)
  if (u.hostname === 'github.com' && parts.length >= 4) {
    const [owner, repo, kind, ...rest] = parts
    if ((kind === 'pull' || kind === 'issues') && /^\d+$/.test(rest[0] ?? '')) return `${owner}/${repo}#${rest[0]}`
    if (kind === 'releases' && rest[0] === 'tag' && rest[1]) return `${owner}/${repo} ${decodeURIComponent(rest[1])}`
  }
  const path = u.pathname === '/' ? '' : u.pathname.replace(/\/$/, '')
  return `${u.host}${path}`
}

// ---------------------------------------------------------------------------
// The list's sorts

export interface SortWord {
  value: DispatchSort
  label: string
  /** The sort's own direction: the newest, the most urgent first; the rest in their order. */
  desc: boolean
}

/** A list's sorts, as the store sorts (dispatch.Sorts), each with the direction it is read in unless turned. */
export const LIST_SORTS: readonly SortWord[] = [
  { value: 'updated', label: 'Updated', desc: true },
  { value: 'created', label: 'Created', desc: true },
  { value: 'priority', label: 'Priority', desc: true },
  { value: 'status', label: 'Status', desc: false },
  { value: 'title', label: 'Title', desc: false },
  { value: 'key', label: 'Key', desc: false },
  { value: 'milestone', label: 'Milestone', desc: false },
]

/** Whether `sort` is a list's sort. */
export function isSort(sort: string): sort is DispatchSort {
  return LIST_SORTS.some((s) => s.value === sort)
}

/** The direction a sort is read in unless turned: true for the newest or most urgent first. */
export function sortDesc(sort: string): boolean {
  return LIST_SORTS.find((s) => s.value === sort)?.desc ?? false
}

// ---------------------------------------------------------------------------
// The board's and the roadmap's columns

export interface BoardColumn<T> {
  status: TicketStatus
  word: Word
  tickets: T[]
}

/** A board's columns: one per status in its order, each with its tickets in the list's order; a status the vocabulary lacks has none. */
export function boardColumns<T extends { status: string }>(tickets: readonly T[]): BoardColumn<T>[] {
  return TICKET_STATUSES.map((status) => ({ status, word: statusWord(status), tickets: tickets.filter((t) => t.status === status) }))
}

export interface RoadmapColumn<T> {
  horizon: Horizon
  word: HorizonWord
  tickets: T[]
  /** Not planned: drawn folded, its count alone, until opened. */
  folded: boolean
}

/** A roadmap's columns: Now, Next and Later, then a folded Not planned, each with its tickets in the list's order. */
export function roadmapColumns<T extends { horizon: string }>(tickets: readonly T[]): RoadmapColumn<T>[] {
  return HORIZONS.map((word) => ({ horizon: word.value, word, tickets: tickets.filter((t) => (t.horizon ?? '') === word.value), folded: word.value === '' }))
}

// ---------------------------------------------------------------------------
// The list's keyboard model (Owner decisions, 10): a roving focus by key over the rows in their order, as the palette's rows, and the
// selection `x` toggles. Pure: the list holds the focus and the selection, and these say where they go.

/** The key of the row `delta` steps from `focus`, wrapping at the ends; from no row (or one gone), the first down or the last up; null when there are none. */
export function listFocusStep(keys: readonly string[], focus: string | null, delta: 1 | -1): string | null {
  if (!keys.length) return null
  const i = focus === null ? -1 : keys.indexOf(focus)
  if (i < 0) return delta === 1 ? keys[0]! : keys[keys.length - 1]!
  return keys[(i + delta + keys.length) % keys.length]!
}

/** The selection with `key` toggled: added when it was not there, taken out when it was; unchanged for no row. */
export function listToggle(selected: ReadonlySet<string>, key: string | null): Set<string> {
  const out = new Set(selected)
  if (key === null) return out
  if (out.has(key)) out.delete(key)
  else out.add(key)
  return out
}

/**
 * Where the focus goes when the rows change (a re-read, a filter): it stays on its row while the row is there; a row gone hands it on to
 * the first row after it that is still there, else the last one before it, else nowhere.
 */
export function listRefocus(before: readonly string[], after: readonly string[], focus: string | null): string | null {
  if (focus === null) return null
  if (after.includes(focus)) return focus
  const there = new Set(after)
  const i = before.indexOf(focus)
  if (i < 0) return null
  for (let j = i + 1; j < before.length; j++) if (there.has(before[j]!)) return before[j]!
  for (let j = i - 1; j >= 0; j--) if (there.has(before[j]!)) return before[j]!
  return null
}

/** The selection kept to the rows still there. */
export function listKeepSelected(selected: ReadonlySet<string>, after: readonly string[]): Set<string> {
  const there = new Set(after)
  return new Set([...selected].filter((k) => there.has(k)))
}

// ---------------------------------------------------------------------------
// A ticket's history in words, as the store recorded it, field by field (GET …/history).

/** What a change names by id, in words: its project's labels and milestones, the catalog's agents, tickets by their keys. */
export interface ChangeNames {
  labels?: ReadonlyMap<string, string>
  milestones?: ReadonlyMap<string, string>
  agents?: ReadonlyMap<string, string>
  tickets?: ReadonlyMap<string, string>
}

/** An assignee's actor in words: the person's name, or the agent's name from the catalog (its id when the catalog lacks it). */
function actorWords(actor: string, names: ChangeNames): string {
  const a = assigneeOf(actor)
  return a.kind === 'agent' ? (names.agents?.get(a.name) ?? a.name) : a.name
}

/** What a set's member names in a change row's value: an id inside its JSON (a relation, a link, an attachment). */
function memberJSON(value: string): Record<string, unknown> {
  try {
    const v = JSON.parse(value) as unknown
    return v !== null && typeof v === 'object' ? (v as Record<string, unknown>) : {}
  } catch {
    return {}
  }
}

/** A text field's change: written, edited or cleared. */
function textChange(what: string, c: DispatchChange): string {
  if (!c.value) return `cleared ${what}`
  return c.prior ? `edited ${what}` : `wrote ${what}`
}

/** One change of a ticket's own fields or sets, in words: "set the priority to High", "assigned Alice". */
function ticketChange(c: DispatchChange, names: ChangeNames): string {
  if (c.op === 'create') return 'made the ticket'
  if (c.op === 'add' || c.op === 'remove') {
    const adds = c.op === 'add'
    switch (c.field) {
      case 'assignees':
        return `${adds ? 'assigned' : 'unassigned'} ${actorWords(c.value, names)}`
      case 'labels':
        return `${adds ? 'added' : 'removed'} the label ${names.labels?.get(c.value) ?? 'a label since deleted'}`
      case 'relations': {
        const kind = memberJSON(c.value).kind
        return `${adds ? 'added' : 'removed'} ${kind === 'blocks' ? 'a blocking relation' : 'a relation'}`
      }
      case 'links': {
        const kind = memberJSON(c.value).kind
        return `${adds ? 'added' : 'removed'} ${typeof kind === 'string' ? `a ${kind} link` : 'a link'}`
      }
      case 'attachments': {
        const name = memberJSON(c.value).name
        return adds ? `attached ${typeof name === 'string' ? name : 'a file'}` : 'removed an attachment'
      }
    }
    return `${adds ? 'added to' : 'removed from'} ${c.field ?? 'a set'}`
  }
  const v = c.value
  switch (c.field) {
    case 'title':
      return `set the title to "${v}"`
    case 'description':
      return textChange('the description', c)
    case 'acceptance':
      return textChange('the acceptance criteria', c)
    case 'status':
      return c.prior ? `moved it from ${statusWord(c.prior).label} to ${statusWord(v).label}` : `set the status to ${statusWord(v).label}`
    case 'priority':
      return `set the priority to ${priorityWord(Number(v)).label}`
    case 'horizon':
      return `set the horizon to ${horizonWord(v).label}`
    case 'owner':
      return v ? `made ${v} the owner` : 'cleared the owner'
    case 'parent_id':
      return v ? `set the parent to ${names.tickets?.get(v) ?? 'a ticket'}` : 'cleared the parent'
    case 'milestone_id':
      return v ? `set the milestone to ${names.milestones?.get(v) ?? 'a milestone since deleted'}` : 'cleared the milestone'
    case 'closed_at':
      // It follows the status, whose row says so.
      return ''
    case 'deleted_at':
      return v ? 'deleted it' : 'restored it'
    case 'accepted_for':
      return v ? `accepted it for ${v}` : ''
    case 'accepted_at':
      // Set by every acceptance. One for an owner it was not made for says so in accepted_for's row, beside which historyEntries leaves
      // this one out; one made again for the same owner has no such row (the store records only the fields that change), and this
      // one says it.
      if (!v) return 'took the acceptance back'
      return c.prior ? 'accepted it again' : 'accepted it'
    case 'accepted_by':
    case 'accepted_criteria':
      // The acceptance's own record: accepted_for's row and accepted_at's say it.
      return ''
    case 'accepted_note':
      return v ? `noted "${v}"` : ''
  }
  return v ? `set ${c.field ?? 'a field'} to "${v}"` : `cleared ${c.field ?? 'a field'}`
}

/**
 * One change of a ticket's history in words, its own or one of its comments'; a repair a merge made says so. '' for a row that says
 * nothing its operation's other rows do not (a comment's body as it is added, the time an edit or a close was made).
 */
export function changeWords(c: DispatchChange, names: ChangeNames = {}): string {
  const words = c.entity === 'comment' ? commentChange(c) : ticketChange(c, names)
  if (!words) return ''
  return c.op === 'reconcile' ? `${words} (a merge's repair)` : c.op === 'migrate' ? `${words} (an upgrade's)` : words
}

/** One change of a comment in words: its add (the create row; the body's row beside it says nothing more), its edit, its delete. */
function commentChange(c: DispatchChange): string {
  if (c.op === 'create') return 'commented'
  if (c.field === 'deleted_at') return c.value ? 'deleted a comment' : 'restored a comment'
  if (c.field === 'body') return c.prior ? 'edited a comment' : ''
  return ''
}

/** One operation of a ticket's history: who made it, when, and its changes in words, in the order it made them. */
export interface HistoryEntry {
  /** Its operation's identity: (actor, opId). */
  key: string
  /** The display name it recorded, else its actor. */
  who: string
  at: string
  lines: string[]
  /** Its last change's seq. */
  seq: number
}

/**
 * A ticket's history as entries, one per operation: an operation's changes are consecutive in the log, so a run of rows of one (actor,
 * opId) is one entry, oldest first. An acceptance is said once: by accepted_for's row when its operation has one, else by its time's (an
 * acceptance made again for the same owner, whose operation records its time and whatever else changed, and no accepted_for).
 */
export function historyEntries(changes: readonly DispatchChange[], names: ChangeNames = {}): HistoryEntry[] {
  const ops: DispatchChange[][] = []
  for (const c of changes) {
    const last = ops.at(-1)
    if (last && last[0]!.actor === c.actor && last[0]!.opId === c.opId) last.push(c)
    else ops.push([c])
  }
  const out: HistoryEntry[] = []
  for (const rows of ops) {
    const first = rows[0]!
    const forOwner = rows.some((c) => accepting(c, 'accepted_for'))
    const lines = rows
      .filter((c) => !(forOwner && accepting(c, 'accepted_at')))
      .map((c) => changeWords(c, names))
      .filter((words) => words)
    if (lines.length) out.push({ key: `${first.actor}\n${first.opId}`, who: first.by || first.actor, at: first.at, lines, seq: rows.at(-1)!.seq })
  }
  return out
}

/** Whether `c` is a ticket's row of an acceptance's `field` as one is made, not as one is taken back: set to a value. */
function accepting(c: DispatchChange, field: 'accepted_for' | 'accepted_at'): boolean {
  return c.entity === 'ticket' && c.field === field && !!c.value
}

// ---------------------------------------------------------------------------
// The board's and the roadmap's moves (PR-9): a card moved by drag and drop, or by ← → on a focused card, one column at a time; a move
// never wraps, so → on the last column, or ← on the first, is no move.

/** The status of the board's column `delta` columns from `status`'s; null past either end, and for a status the board has no column of. */
export function boardStep(status: string, delta: 1 | -1): TicketStatus | null {
  const i = TICKET_STATUSES.indexOf(status as TicketStatus)
  if (i < 0) return null
  return TICKET_STATUSES[i + delta] ?? null
}

/** The horizon of the roadmap's column `delta` columns from `horizon`'s (Now, Next, Later, Not planned); null past either end. */
export function roadmapStep(horizon: string, delta: 1 | -1): Horizon | null {
  const i = HORIZONS.findIndex((h) => h.value === horizon)
  if (i < 0) return null
  return HORIZONS[i + delta]?.value ?? null
}

/** A move on its way: the column a card was dropped in, shown until the store has answered. */
export interface CardMove {
  status?: TicketStatus
  horizon?: Horizon
}

/**
 * The rows as a board or a roadmap shows them while moves are on their way: each card moved in the column it was dropped in, every other
 * row as the store last answered. A move refused takes its entry away, and its card is back in the column the store holds: it snaps back.
 */
export function withMoves<T extends { key: string; status: TicketStatus; horizon: Horizon }>(rows: readonly T[], moves: ReadonlyMap<string, CardMove>): T[] {
  if (!moves.size) return [...rows]
  return rows.map((r) => {
    const m = moves.get(r.key)
    return m ? { ...r, ...m } : r
  })
}

/** The edit a card's move makes: its status set (a board), or its horizon (a roadmap); null when the ticket is in that column already. */
export function moveEdit(t: Pick<TicketSummary, 'status' | 'horizon'>, to: CardMove): TicketEdit | null {
  if (to.status !== undefined) return to.status === t.status ? null : { set: { status: to.status } }
  if (to.horizon !== undefined) return to.horizon === (t.horizon ?? '') ? null : { set: { horizon: to.horizon } }
  return null
}

/** A ticket's summary, as a list holds it, from the ticket in full an operation answers with. */
export function ticketSummaryOf(t: DispatchTicket): TicketSummary {
  const s: TicketSummary = {
    id: t.id,
    projectId: t.projectId,
    number: t.number,
    key: t.key,
    title: t.title,
    status: t.status,
    priority: t.priority,
    horizon: t.horizon,
    owner: t.owner,
    assignees: [...t.assignees],
    labels: [...t.labels],
    milestoneId: t.milestoneId,
    parentId: t.parentId,
    blocked: t.blocked,
    createdAt: t.createdAt,
    updatedAt: t.updatedAt,
    rev: t.rev,
  }
  if (t.closedAt) s.closedAt = t.closedAt
  if (t.deletedAt) s.deletedAt = t.deletedAt
  return s
}

/**
 * The rows with the one of `t`'s key replaced by `t`, an operation's answer, when `t` is at that row's revision or later (the store's
 * `rev` orders a ticket's states; an older answer never replaces a newer row); the rows unchanged otherwise, and when none has the key.
 */
export function patchRows(rows: readonly TicketSummary[], t: TicketSummary): TicketSummary[] {
  return rows.map((r) => (r.key === t.key && t.rev >= r.rev ? t : r))
}

/**
 * A list's rows as read, each replaced by the ticket `known` holds for its key when that one is at a later revision: an edit's answer this
 * page applied, newer than a row of a read sent before it and served after. Rows `known` has nothing newer for are as read.
 */
export function newestRows(rows: readonly TicketSummary[], known: ReadonlyMap<string, TicketSummary>): TicketSummary[] {
  return rows.map((r) => {
    const k = known.get(r.key)
    return k && k.rev > r.rev ? k : r
  })
}

// ---------------------------------------------------------------------------
// The pickers of the list's letters (Owner decisions, 10): S P A L M O pick a ticket's status, priority, assignees, labels, milestone
// and owner, for the focused ticket or for the tickets selected with X, one operation per ticket.

/** What a picker sets. */
export type PickField = 'status' | 'priority' | 'assignees' | 'labels' | 'milestone' | 'owner'

/** The letter of each picker, as the list takes it. */
export const PICK_KEYS: Readonly<Record<string, PickField>> = { s: 'status', p: 'priority', a: 'assignees', l: 'labels', m: 'milestone', o: 'owner' }

/** A picker's field in words: its title ("Status") and what a row of the palette says it sets ("Set status of"). */
export const PICK_WORDS: Readonly<Record<PickField, { title: string; set: string; icon: string }>> = {
  status: { title: 'Status', set: 'Set status of', icon: 'i-lucide-circle-dot' },
  priority: { title: 'Priority', set: 'Set priority of', icon: 'i-lucide-signal-high' },
  assignees: { title: 'Assignees', set: 'Assign', icon: 'i-lucide-users' },
  labels: { title: 'Labels', set: 'Label', icon: 'i-lucide-tag' },
  milestone: { title: 'Milestone', set: 'Set the milestone of', icon: 'i-lucide-milestone' },
  owner: { title: 'Owner', set: 'Set the owner of', icon: 'i-lucide-user' },
}

/** What of a ticket a picker reads. */
export type Pickable = Pick<TicketSummary, 'key' | 'status' | 'priority' | 'assignees' | 'labels' | 'milestoneId' | 'owner'>

/** A ticket's value of a scalar field, as a picker's choices carry it: a status, a priority's number, a milestone's id, an owner's name. */
function scalarOf(field: 'status' | 'priority' | 'milestone' | 'owner', t: Pickable): string | number {
  switch (field) {
    case 'status':
      return t.status
    case 'priority':
      return t.priority
    case 'milestone':
      return t.milestoneId ?? ''
    case 'owner':
      return t.owner ?? ''
  }
}

/** A ticket's members of a set field: its assignees' actors, its labels' ids. */
function membersOf(field: 'assignees' | 'labels', t: Pickable): readonly string[] {
  return field === 'assignees' ? t.assignees : t.labels
}

/**
 * The choices a picker marks as held over `tickets`: a scalar's value when every ticket holds the same one (none when they differ), a
 * set's members every ticket holds.
 */
export function pickHeld(field: PickField, tickets: readonly Pickable[]): Array<string | number> {
  if (!tickets.length) return []
  if (field === 'assignees' || field === 'labels') {
    const [first, ...rest] = tickets
    return membersOf(field, first!).filter((m) => rest.every((t) => membersOf(field, t).includes(m)))
  }
  const v = scalarOf(field, tickets[0]!)
  return tickets.every((t) => scalarOf(field, t) === v) ? [v] : []
}

/**
 * Whether picking `member` of a set (an assignee, a label) over `tickets` adds it: some ticket lacks it. When every one holds it already,
 * the pick takes it away from every one, so picking a member twice puts things back.
 */
export function pickAdds(field: 'assignees' | 'labels', tickets: readonly Pickable[], member: string): boolean {
  return tickets.some((t) => !membersOf(field, t).includes(member))
}

/**
 * One ticket's edit for a pick: the scalar set to `value` ('' clears a milestone or an owner), or the set's `value` added (`adds`) or
 * taken away; null when the ticket holds it already, since the store refuses an edit that changes nothing.
 */
export function pickEdit(field: PickField, t: Pickable, value: string | number, adds = true): TicketEdit | null {
  if (field === 'assignees' || field === 'labels') {
    const member = String(value)
    const has = membersOf(field, t).includes(member)
    if (adds === has) return null
    return adds ? { add: { [field]: [member] } } : { remove: { [field]: [member] } }
  }
  if (scalarOf(field, t) === value) return null
  switch (field) {
    case 'status':
      return { set: { status: value as TicketStatus } }
    case 'priority':
      return { set: { priority: Number(value) } }
    case 'milestone':
      return { set: { milestone: String(value) } }
    case 'owner':
      return { set: { owner: String(value) } }
  }
}

/** The tickets a pick acts on, in words: the key of one, "3 tickets" of several. */
export function targetWords(keys: readonly string[]): string {
  return keys.length === 1 ? keys[0]! : `${keys.length} tickets`
}

// ---------------------------------------------------------------------------
// The roadmap's milestones (PR-9), beside its columns: each with how many of the tickets shown are in it and how many of those are done.

export interface MilestoneProgress<M> {
  milestone: M
  /** The tickets shown in it, the cancelled left out: what is planned for it. */
  total: number
  /** Of those, the ones done. */
  done: number
}

/**
 * The project's milestones with their progress over the tickets given, the open ones first by their target (the soonest first, one with
 * no target after those with one), then the closed ones; a cancelled ticket counts in neither number, since it will never be done.
 */
export function milestoneProgress<M extends { id: string; target: string; state: string; name: string }>(milestones: readonly M[], tickets: readonly { milestoneId: string; status: string }[]): MilestoneProgress<M>[] {
  const order = (m: M) => (m.state === 'closed' ? 2 : m.target ? 0 : 1)
  return [...milestones]
    .sort((a, b) => order(a) - order(b) || a.target.localeCompare(b.target) || a.name.localeCompare(b.name))
    .map((milestone) => {
      const mine = tickets.filter((t) => t.milestoneId === milestone.id && t.status !== 'cancelled')
      return { milestone, total: mine.length, done: mine.filter((t) => t.status === 'done').length }
    })
}

// ---------------------------------------------------------------------------
// A ticket's discussion (PR-10): its comments as the page shows them, numbered as the store derives them.

/** The most comments a page of them holds: the ticket's read carries its last 50, and the comments route 50 at a time. */
export const COMMENT_PAGE = 50

/**
 * A ticket's comments as its page shows them, in their order (their number `n`, which the store derives from their place, tombstones
 * keeping theirs): the ticket's read's last page, the earlier pages read since, and the comments an operation of this page answered with,
 * each comment by its id at the latest revision any of them holds, so a read served before a comment's edit committed never shows the
 * older body once the edit's answer is in, nor leaves out a comment just made. Those of another ticket are left out. A comment the read
 * lacks (one of an earlier page) keeps the number it was read with. `earlierOutdated` says the earlier pages were read before the stream
 * started again (a store restored from a backup, whose `rev`s may be below those read): the read's comment is shown over theirs then,
 * whatever its `rev`, until they are read again.
 */
export function shownComments(
  ticketId: string,
  read: readonly TicketComment[],
  earlier: readonly TicketComment[],
  known: ReadonlyMap<string, TicketComment> = new Map(),
  o: { earlierOutdated?: boolean } = {},
): TicketComment[] {
  const byId = new Map<string, TicketComment>()
  const keep = (c: TicketComment, over = false) => {
    const had = byId.get(c.id)
    if (!had || over || c.rev > had.rev) byId.set(c.id, c)
  }
  for (const c of earlier) keep(c)
  for (const c of read) keep(c, o.earlierOutdated)
  for (const c of known.values()) if (c.ticketId === ticketId) keep(c)
  return [...byId.values()].sort((a, b) => a.n - b.n || a.createdAt.localeCompare(b.createdAt) || a.id.localeCompare(b.id))
}

/**
 * The page of comments before those shown, whose first is numbered `first`: those numbered after `after`, at most `limit`, so the page
 * ends at the comment before it; null when the first shown is the ticket's first (or none is shown).
 */
export function earlierComments(first: number | undefined): { after: number; limit: number } | null {
  if (!first || first <= 1) return null
  const after = Math.max(0, first - 1 - COMMENT_PAGE)
  return { after, limit: first - 1 - after }
}

/** Comments not shown between two that are, whose numbers are those after `after`, `limit` of them, before the comment `before`. */
export interface CommentGap {
  before: string
  after: number
  limit: number
}

/**
 * The comments missing among those shown (shownComments), each run of them before the comment that follows it: a comment an operation of
 * this page answered with can sit below the read's window (the 50 it carries moved on since), and earlier pages read before the window
 * moved end below its first, until they are read again. Only below the read's first comment (`readFirst`), where the earlier pages
 * reach; a gap above it is the read's own to fill, on the event of what made it. None when the read carries no comment.
 */
export function commentGaps(shown: readonly TicketComment[], readFirst: number | undefined): CommentGap[] {
  if (!readFirst) return []
  const out: CommentGap[] = []
  for (let i = 1; i < shown.length; i++) {
    const prev = shown[i - 1]!
    const next = shown[i]!
    if (next.n > readFirst) break
    if (next.n > prev.n + 1) out.push({ before: next.id, after: prev.n, limit: next.n - prev.n - 1 })
  }
  return out
}

/** Who wrote a comment, in words: the display name it recorded, else its actor (an agent's by its member's name). */
export function commentAuthor(c: Pick<TicketComment, 'author' | 'by'>): string {
  if (c.by) return c.by
  return c.author.startsWith('agent:') ? c.author.slice(6) : c.author || 'admin'
}

// ---------------------------------------------------------------------------
// A ticket's relations (PR-10): what it blocks, what blocks it ("depends on" is a blocks read from the other end), what it relates to.

export interface RelationRow {
  relation: TicketRelation
  /** The ticket at the other end. */
  other: RelatedTicket
  /** Whether it counts: a relation whose other end is deleted is kept, inactive, and comes back with that ticket's undelete. */
  active: boolean
}

export interface RelationGroups {
  /** The tickets this one blocks. */
  blocks: RelationRow[]
  /** The tickets that block this one: it depends on them. */
  blockedBy: RelationRow[]
  /** The tickets it relates to, either way. */
  relates: RelationRow[]
  /** Of those blocking it, the ones that hold it: live, neither done nor cancelled. */
  blocking: RelationRow[]
}

/** Whether a ticket's status is a close: done or cancelled. */
export function closedStatus(status: string): boolean {
  return status === 'done' || status === 'cancelled'
}

/** A ticket's live relations by what they are from its side, each with its other end, the active ones first, then by the other's key. */
export function relationGroups(ticketId: string, relations: readonly TicketRelation[]): RelationGroups {
  const out: RelationGroups = { blocks: [], blockedBy: [], relates: [], blocking: [] }
  for (const relation of relations) {
    if (relation.deletedAt) continue
    const row: RelationRow = { relation, other: relation.other, active: !relation.other.deletedAt }
    if (relation.kind === 'relates') out.relates.push(row)
    else if (relation.fromId === ticketId) out.blocks.push(row)
    else if (relation.toId === ticketId) out.blockedBy.push(row)
  }
  const order = (a: RelationRow, b: RelationRow) => Number(b.active) - Number(a.active) || keyOrder(a.other.key, b.other.key)
  out.blocks.sort(order)
  out.blockedBy.sort(order)
  out.relates.sort(order)
  out.blocking = out.blockedBy.filter((r) => r.active && !closedStatus(r.other.status))
  return out
}

/** Two keys in their project's order: by number, then as text for keys of other projects. */
function keyOrder(a: string, b: string): number {
  const pa = parseTicketKey(a)
  const pb = parseTicketKey(b)
  if (pa && pb && pa.project === pb.project) return pa.number - pb.number
  return a.localeCompare(b)
}

/** What a relation is made as from the page's ticket: it blocks the other, it is blocked by it (made from the other's side), or relates. */
export type RelationChoice = 'blocks' | 'blocked_by' | 'relates'

export const RELATION_CHOICES: ReadonlyArray<{ value: RelationChoice; label: string; icon: string }> = [
  { value: 'blocks', label: 'Blocks', icon: 'i-lucide-octagon-x' },
  { value: 'blocked_by', label: 'Is blocked by', icon: 'i-lucide-octagon-pause' },
  { value: 'relates', label: 'Relates to', icon: 'i-lucide-link-2' },
]

/**
 * The tickets a relation of `choice` from the page's ticket cannot be made with, not to be offered: the ticket itself, and those it
 * holds that relation with already (the store refuses a second of the same kind between the same two, a `relates` either way). A
 * relation of another kind with the same ticket can be made, so it is offered: a ticket related to may also be blocked. A `blocks` that
 * would close a cycle is offered too, and refused in the store's words.
 */
export function relationExcluded(ticketId: string, relations: readonly TicketRelation[], choice: RelationChoice): string[] {
  const out = [ticketId]
  for (const r of relations) {
    if (r.deletedAt) continue
    const held =
      choice === 'relates' ? r.kind === 'relates' : r.kind === 'blocks' && (choice === 'blocks' ? r.fromId === ticketId : r.toId === ticketId)
    if (held) out.push(r.other.id)
  }
  return out
}

/**
 * The route a relation is added through, from the page's ticket (`self`) and the one picked (`other`): a ticket blocks or relates to
 * another from its own route, so "is blocked by" is the other's `blocks` of this one, made through the other's route.
 */
export function relationAdd(choice: RelationChoice, self: string, other: string): { on: string; kind: 'blocks' | 'relates'; to: string } {
  if (choice === 'blocked_by') return { on: other, kind: 'blocks', to: self }
  return { on: self, kind: choice, to: other }
}

// ---------------------------------------------------------------------------
// The ticket picker (PR-10): a search over a project's tickets by key and title, for a parent and a relation's other end.

/** What a picker offers of a ticket. */
export type PickTicket = Pick<TicketSummary, 'id' | 'key' | 'number' | 'title' | 'status'> & { deletedAt?: string }

/**
 * The tickets offered for what is typed, best first: the key typed whole (CND-12, cnd-12, or the number alone, 12), then the keys it
 * begins (CND-1 offers CND-1, CND-10…), then the titles it begins, then the titles holding every word of it, each by number; the tickets
 * `exclude` names (the page's own, those related already) and the deleted ones left out. With nothing typed, every ticket, by number,
 * the newest first.
 */
export function rankTickets<T extends PickTicket>(typed: string, tickets: readonly T[], exclude: ReadonlySet<string> = new Set()): T[] {
  const q = typed.trim()
  const pool = tickets.filter((t) => !exclude.has(t.id) && !t.deletedAt)
  const seen = new Set<string>()
  const unique = pool.filter((t) => (seen.has(t.id) ? false : (seen.add(t.id), true)))
  if (!q) return [...unique].sort((a, b) => b.number - a.number)
  const upper = q.toUpperCase()
  const lower = q.toLowerCase()
  const words = lower.split(/\s+/).filter(Boolean)
  const rank = (t: T): number => {
    const key = t.key.toUpperCase()
    const title = t.title.toLowerCase()
    if (key === upper || (/^[1-9][0-9]*$/.test(q) && t.number === Number(q))) return 0
    if (key.startsWith(upper)) return 1
    if (title.startsWith(lower)) return 2
    if (words.every((w) => title.includes(w))) return 3
    return -1
  }
  return unique
    .map((t) => ({ t, r: rank(t) }))
    .filter((x) => x.r >= 0)
    .sort((a, b) => a.r - b.r || a.t.number - b.t.number)
    .map((x) => x.t)
}

// ---------------------------------------------------------------------------
// A ticket's attachments (PR-10).

/** The types an attachment is shown inline as, once fetched: the images the server serves as their own type. An SVG is never one. */
const INLINE_IMAGES = new Set(['image/png', 'image/jpeg', 'image/gif', 'image/webp'])

/** Whether an attachment of `contentType` is shown as an image: one of the four the server serves inline, never an SVG, which can script. */
export function inlineImage(contentType: string): boolean {
  return INLINE_IMAGES.has(contentType.split(';', 1)[0]!.trim().toLowerCase())
}

/** How AttachmentImages makes and revokes an object URL: the browser's, or a test's. */
export interface ObjectUrls {
  create: (blob: Blob) => string
  revoke: (url: string) => void
}

/**
 * A ticket page's images of its attachments as object URLs (useDispatchAttachments): each content fetched once by its id, a fetch that
 * failed forgotten, to be asked again; an answer the server serves as no image the page shows (an SVG comes as
 * application/octet-stream) is none. `dispose`, as the page goes, revokes every URL made, and a fetch that answers after it makes none,
 * so no blob is kept past the page.
 */
export class AttachmentImages {
  private readonly images = new Map<string, Promise<string | null>>()
  private readonly made: string[] = []
  private disposed = false

  constructor(
    private readonly fetch: (id: string) => Promise<Blob>,
    private readonly urls: ObjectUrls = { create: (b) => URL.createObjectURL(b), revoke: (u) => URL.revokeObjectURL(u) },
  ) {}

  image(id: string): Promise<string | null> {
    if (this.disposed) return Promise.resolve(null)
    let p = this.images.get(id)
    if (!p) {
      p = this.fetch(id).then(
        (blob) => {
          if (this.disposed || !inlineImage(blob.type)) return null
          const url = this.urls.create(blob)
          this.made.push(url)
          return url
        },
        () => {
          this.images.delete(id)
          return null
        },
      )
      this.images.set(id, p)
    }
    return p
  }

  /** How many object URLs are held, not yet revoked. */
  get held(): number {
    return this.made.length
  }

  dispose(): void {
    this.disposed = true
    for (const url of this.made) this.urls.revoke(url)
    this.made.length = 0
    this.images.clear()
  }
}

/** An attachment's id as a Markdown image names it: `attachment:<id>`, 16 lower-case hex characters; null for any other source. */
export function attachmentRef(src: string): string | null {
  const m = /^attachment:([0-9a-f]{16})$/.exec(src)
  return m ? m[1]! : null
}

/** The Markdown that shows an image attachment in a description or a comment: ![name](attachment:<id>). */
export function attachmentMarkdown(a: Pick<TicketAttachment, 'id' | 'name'>): string {
  const alt = a.name.replace(/[[\]\\]/g, '\\$&')
  return `![${alt}](attachment:${a.id})`
}

/** A size in words: 512 B, 3.2 KiB, 15.1 MiB. */
export function sizeWords(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${Math.round((bytes / 1024) * 10) / 10} KiB`
  return `${Math.round((bytes / (1024 * 1024)) * 10) / 10} MiB`
}

/** What became of an upload: done, swept before its row was written (409 expired: sent again under a new op id), or failed. */
export type UploadOutcome = 'done' | 'expired' | 'failed'

/** A ticket's attachments' bounds (the store's): 16 MiB each, 50 on a ticket. */
export const MAX_ATTACHMENT_SIZE = 16 << 20
export const MAX_ATTACHMENTS = 50

// ---------------------------------------------------------------------------
// The palette's Tickets cache (usePalette.ts): the pages read whole, and a read that answers after an edit's answer it was served before.

/** Every page of a list read in turn by its cursor (`read('')` first, then each page's `next`), at most `max` items. */
export async function allPages<T>(read: (cursor: string) => Promise<{ items: readonly T[]; next?: string }>, max = Infinity): Promise<T[]> {
  const all: T[] = []
  let cursor = ''
  do {
    const page = await read(cursor)
    all.push(...page.items)
    cursor = page.next ?? ''
  } while (cursor && all.length < max)
  return all.slice(0, max)
}

/** Whether the palette's Tickets group lists a ticket: one not done, cancelled or deleted. */
export function paletteListed(t: Pick<TicketSummary, 'status' | 'deletedAt'>): boolean {
  return (t.status === 'backlog' || t.status === 'ready' || t.status === 'in_progress' || t.status === 'in_review') && !t.deletedAt
}

/**
 * The palette's tickets from a read, beside the tickets its edits answered with (`patched`, by key): a row the read carries below the
 * revision an edit answered with shows the edit's ticket instead, or goes when that ticket is no longer listed (a read served before the
 * commit and answered after it never brings the older row back). `keep` is what of `patched` a later read may still need: an entry the
 * read carries at its revision or later is done with.
 */
export function withPatched(read: readonly TicketSummary[], patched: ReadonlyMap<string, TicketSummary>): { rows: TicketSummary[]; keep: Map<string, TicketSummary> } {
  const keep = new Map(patched)
  const rows = read.flatMap((r) => {
    const p = patched.get(r.key)
    if (!p) return [r]
    if (p.rev > r.rev) return paletteListed(p) ? [p] : []
    keep.delete(r.key)
    return [r]
  })
  return { rows, keep }
}

// ---------------------------------------------------------------------------
// A name typed into the owner's or the assignees' picker (DispatchPicker.vue).

/**
 * The value a name typed into the owner's or the assignees' picker offers as a choice of its own, or null when a choice is it already:
 * the owner's is the name, the assignees' the person (`person:<name>`), compared without case with the values offered (`values`), so an
 * agent whose display name is the name typed never stands for the person. At most 60 characters of the name.
 */
export function typedPick(field: 'owner' | 'assignees', query: string, values: readonly string[]): string | null {
  const typed = query.trim().slice(0, 60)
  if (!typed) return null
  const value = field === 'owner' ? typed : `person:${typed}`
  const v = value.toLowerCase()
  return values.some((x) => x.toLowerCase() === v) ? null : value
}
