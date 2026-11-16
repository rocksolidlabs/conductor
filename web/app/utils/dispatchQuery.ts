import type { DispatchSort, Horizon, TicketStatus, ViewQuery } from '~/composables/useDispatch'
import { HORIZONS, PRIORITIES, TICKET_STATUSES, assigneeOf, isSort, parseTicketKey, priorityWord, sortDesc, statusWord } from './dispatch'

// A list's filter, one codec both ways (docs/plans/dispatch.md, "web/app"): to and from a page's link (?status=in_progress&label=api&q=…),
// to and from a saved view's query, and to the list's request (GET /api/dispatch/tickets). A link says labels and the milestone by
// name and the priorities and horizons by their names, so it reads as words, but a milestone whose name another of the project has too
// by its id, since the store lets milestones' names repeat; a saved view keeps labels and the milestone by id, as the store checks them,
// so a rename leaves it whole; the request takes ids and numbers. A value outside its vocabulary, and a parameter the codec does not know
// (the page's ?view=, a stale link's), is ignored. Pure.

/** A list's filter, sort and grouping as a page holds them: labels and the milestone by name, people by name. */
export interface DispatchFilter {
  status: TicketStatus[]
  /** Numbers, 0 (none) to 4 (urgent). */
  priority: number[]
  /** Label names: a ticket holding any of them. */
  labels: string[]
  owner: string
  /** person:<name> or agent:<catalog id>. */
  assignees: string[]
  /**
   * A milestone: by its name, or by its id when another live milestone of the project has the same name (the store lets names repeat),
   * so that each can be chosen; an id whose row has gone stays the id.
   */
  milestone: string
  /** '' for not planned. */
  horizon: Horizon[]
  /** A ticket's key: its children. */
  parent: string
  blocked: boolean
  q: string
  deleted: boolean
  sort: DispatchSort
  desc: boolean
  /** What a list is grouped by (PR-9's views); '' for none. */
  group: string
}

/** A search's bound, in characters (the store's MaxQueryText). */
export const MAX_SEARCH = 200

/** What a list is grouped by: the store's Groupings. */
export const GROUPINGS = ['', 'status', 'priority', 'horizon', 'milestone', 'owner'] as const

/** The filter of a list nothing narrows: every ticket, the latest updated first. */
export function emptyFilter(): DispatchFilter {
  return { status: [], priority: [], labels: [], owner: '', assignees: [], milestone: '', horizon: [], parent: '', blocked: false, q: '', deleted: false, sort: 'updated', desc: true, group: '' }
}

/** Whether a filter narrows the list (a sort or a grouping does not). */
export function filterNarrows(f: DispatchFilter): boolean {
  return !!(f.status.length || f.priority.length || f.labels.length || f.owner || f.assignees.length || f.milestone || f.horizon.length || f.parent || f.blocked || f.q.trim() || f.deleted)
}

/** A value of the filter in use, as the toolbar shows it: a chip that takes itself out. */
export interface FilterChip {
  /** `<field>:<value>`, the field as the link names it (status, priority, label, owner, assignee, milestone, horizon, blocked, parent, deleted, q). */
  id: string
  field: 'status' | 'priority' | 'label' | 'owner' | 'assignee' | 'milestone' | 'horizon' | 'blocked' | 'parent' | 'deleted' | 'q'
  /** What it says: "Status: In progress", "Blocked", "Search: login". */
  words: string
  /** The filter without this one value, its sort, direction and grouping kept. */
  without: DispatchFilter
}

/**
 * The filter in use as chips, one a value, in the toolbar's order: the statuses, priorities, labels, owner, assignees, milestone,
 * horizons, blocked, parent, deleted, then the search. A label or a milestone is named as the link names it (a milestone kept by its id,
 * since another has its name, by its row's name); an agent by its catalog name. Pure.
 */
export function filterChips(f: DispatchFilter, names: FilterNames, agents: ReadonlyArray<{ id: string; name: string }>): FilterChip[] {
  const out: FilterChip[] = []
  const add = (field: FilterChip['field'], value: string, words: string, without: Partial<DispatchFilter>) => out.push({ id: `${field}:${value}`, field, words, without: { ...f, ...without } })
  for (const s of f.status) add('status', s, `Status: ${statusWord(s).label}`, { status: f.status.filter((x) => x !== s) })
  for (const p of f.priority) add('priority', PRIORITIES.find((x) => x.value === p)?.name ?? String(p), `Priority: ${priorityWord(p).label}`, { priority: f.priority.filter((x) => x !== p) })
  for (const l of f.labels) add('label', l, `Label: ${l}`, { labels: f.labels.filter((x) => x !== l) })
  if (f.owner) add('owner', f.owner, `Owner: ${f.owner}`, { owner: '' })
  for (const a of f.assignees) {
    const who = assigneeOf(a)
    add('assignee', a, `Assignee: ${who.kind === 'agent' ? (agents.find((x) => x.id === who.name)?.name ?? who.name) : who.name}`, { assignees: f.assignees.filter((x) => x !== a) })
  }
  if (f.milestone) add('milestone', f.milestone, `Milestone: ${names.milestones.find((m) => m.id === f.milestone)?.name ?? f.milestone}`, { milestone: '' })
  for (const h of f.horizon) {
    const w = HORIZONS.find((x) => x.value === h)
    add('horizon', w?.name ?? h, `Horizon: ${w?.label ?? h}`, { horizon: f.horizon.filter((x) => x !== h) })
  }
  if (f.blocked) add('blocked', '1', 'Blocked', { blocked: false })
  if (f.parent) add('parent', f.parent, `Parent: ${f.parent}`, { parent: '' })
  if (f.deleted) add('deleted', '1', 'Deleted too', { deleted: false })
  if (f.q.trim()) add('q', f.q, `Search: ${f.q.trim()}`, { q: '' })
  return out
}

/** Filter's number: the chips in use other than the search's, which shows in its own field. */
export function filterCount(chips: readonly FilterChip[]): number {
  return chips.filter((c) => c.field !== 'q').length
}

/** An assignee's actor: person:<name> or agent:<catalog id>, as the store checks one. */
const ASSIGNEE = /^(person:[^\p{Cc}]{1,60}|agent:[a-z0-9-]{1,32})$/u

/** A link's value (route.query's): every value of a parameter, as strings. */
function values(v: unknown): string[] {
  if (Array.isArray(v)) return v.filter((x): x is string => typeof x === 'string')
  return typeof v === 'string' ? [v] : []
}

/** The first value of a parameter, '' when none. */
function first(v: unknown): string {
  return values(v)[0] ?? ''
}

/** The values kept once each, in the order given. */
function unique<T>(xs: readonly T[]): T[] {
  return [...new Set(xs)]
}

/** A flag of a link: 1 or true. */
function flag(v: unknown): boolean {
  const s = first(v)
  return s === '1' || s === 'true'
}

/** Text cut to the store's bound for a search, by characters. */
function cutSearch(q: string): string {
  const cs = Array.from(q)
  return cs.length > MAX_SEARCH ? cs.slice(0, MAX_SEARCH).join('') : q
}

/** A priority by its name in a link (high), or its number. */
function priorityOf(s: string): number | undefined {
  return PRIORITIES.find((p) => p.name === s || String(p.value) === s)?.value
}

/** A horizon by its name in a link (now, next, later, none). */
function horizonOf(s: string): Horizon | undefined {
  return HORIZONS.find((h) => h.name === s)?.value
}

/** The filter a page's link holds (route.query). */
export function filterFromURL(query: Readonly<Record<string, unknown>>): DispatchFilter {
  const f = emptyFilter()
  f.status = unique(values(query.status).filter((s): s is TicketStatus => (TICKET_STATUSES as readonly string[]).includes(s)))
  f.priority = unique(values(query.priority).map(priorityOf).filter((p): p is number => p !== undefined))
  f.labels = unique(values(query.label).filter((l) => l !== ''))
  f.owner = first(query.owner)
  f.assignees = unique(values(query.assignee).filter((a) => ASSIGNEE.test(a)))
  f.milestone = first(query.milestone)
  f.horizon = unique(values(query.horizon).map(horizonOf).filter((h): h is Horizon => h !== undefined))
  const parent = first(query.parent)
  f.parent = parseTicketKey(parent) ? parent : ''
  f.blocked = flag(query.blocked)
  f.q = cutSearch(first(query.q))
  f.deleted = flag(query.deleted)
  const sort = first(query.sort)
  f.sort = isSort(sort) ? sort : 'updated'
  const desc = first(query.desc)
  f.desc = desc === '1' || desc === 'true' ? true : desc === '0' || desc === 'false' ? false : sortDesc(f.sort)
  const group = first(query.group)
  f.group = (GROUPINGS as readonly string[]).includes(group) ? group : ''
  return f
}

/** A filter as a page's link says it: only what differs from a list nothing narrows, in a fixed order, for router.replace. */
export function filterToURL(f: DispatchFilter): Record<string, string | string[]> {
  const out: Record<string, string | string[]> = {}
  const many = (name: string, xs: readonly string[]) => {
    if (xs.length) out[name] = xs.length === 1 ? xs[0]! : [...xs]
  }
  many('status', f.status)
  many('priority', f.priority.map((p) => PRIORITIES.find((x) => x.value === p)?.name ?? String(p)))
  many('label', f.labels)
  if (f.owner) out.owner = f.owner
  many('assignee', f.assignees)
  if (f.milestone) out.milestone = f.milestone
  many('horizon', f.horizon.map((h) => HORIZONS.find((x) => x.value === h)?.name ?? h))
  if (f.parent) out.parent = f.parent
  if (f.blocked) out.blocked = '1'
  if (f.q) out.q = f.q
  if (f.deleted) out.deleted = '1'
  if (f.sort !== 'updated') out.sort = f.sort
  if (f.desc !== sortDesc(f.sort)) out.desc = f.desc ? '1' : '0'
  if (f.group) out.group = f.group
  return out
}

/** The names a project gives its labels and milestones, for the ids a saved view and a request carry. */
export interface FilterNames {
  labels: ReadonlyArray<{ id: string; name: string }>
  milestones: ReadonlyArray<{ id: string; name: string }>
}

/** An id's shape: 16 lower-case hex characters. */
const ID = /^[0-9a-f]{16}$/

/**
 * The ids a name stands for: every one of `rows` with that name (two labels may share one after a merge), else the name itself when it
 * has an id's shape (a view's id whose row has gone, which the store then matches nothing by), else none.
 */
function idsOf(rows: ReadonlyArray<{ id: string; name: string }>, name: string): string[] {
  const ids = rows.filter((r) => r.name === name).map((r) => r.id)
  if (ids.length) return ids
  return ID.test(name) ? [name] : []
}

/** The name an id stands for: its row's, else the id itself. */
function nameOf(rows: ReadonlyArray<{ id: string; name: string }>, id: string): string {
  return rows.find((r) => r.id === id)?.name ?? id
}

/**
 * The filter's value for a milestone by its id: its name, so that the link reads as words, unless another live milestone of the project
 * has the same name: then the id, which keeps it apart from the other. An id whose row has gone stays the id.
 */
function milestoneValue(rows: ReadonlyArray<{ id: string; name: string }>, id: string): string {
  const row = rows.find((r) => r.id === id)
  if (!row) return id
  return rows.some((r) => r.id !== id && r.name === row.name) ? id : row.name
}

/**
 * The milestone a filter's value stands for, by its id: a row's id as it is; else the row of that name, the first in the project's order
 * when the name repeats (a link written before it did); else the value when it has an id's shape (a view's id whose row has gone, which
 * the store then matches nothing by); else none.
 */
function milestoneIdOf(rows: ReadonlyArray<{ id: string; name: string }>, value: string): string {
  if (rows.some((r) => r.id === value)) return value
  return rows.find((r) => r.name === value)?.id ?? (ID.test(value) ? value : '')
}

/**
 * The milestones as the filter bar offers them, each live one once with the value the filter keeps for it (by its name, or by its id when
 * the name repeats); one whose name repeats told apart by its place among those of that name, in the project's order, and its target.
 */
export function milestoneChoices(rows: ReadonlyArray<{ id: string; name: string; target?: string }>): Array<{ label: string; value: string }> {
  return rows.map((r) => {
    const same = rows.filter((x) => x.name === r.name)
    if (same.length < 2) return { label: r.name, value: r.name }
    return { label: `${r.name} (${same.indexOf(r) + 1} of ${same.length}${r.target ? `, due ${r.target}` : ''})`, value: r.id }
  })
}

/**
 * The filter's labels and milestone as the store takes them, by id; null when the filter cannot match a ticket of the project: every
 * label it names, or its milestone, is one the project lacks.
 */
function resolve(f: DispatchFilter, names: FilterNames): { labels: string[]; milestone: string } | null {
  const labels = unique(f.labels.flatMap((l) => idsOf(names.labels, l)))
  if (f.labels.length && !labels.length) return null
  let milestone = ''
  if (f.milestone) {
    milestone = milestoneIdOf(names.milestones, f.milestone)
    if (!milestone) return null
  }
  return { labels, milestone }
}

/**
 * A filter as a saved view keeps it (dispatch.ViewQuery): labels and the milestone by id, every empty field left out, desc kept only
 * when true (the store's meaning when absent). Null when it names labels or a milestone the project lacks: such a filter matches
 * nothing, and the store would refuse their names.
 */
export function filterToView(f: DispatchFilter, names: FilterNames): ViewQuery | null {
  const r = resolve(f, names)
  if (!r) return null
  const q: ViewQuery = {}
  if (f.status.length) q.status = [...f.status]
  if (f.priority.length) q.priority = [...f.priority]
  if (r.labels.length) q.labels = r.labels
  if (f.owner) q.owner = f.owner
  if (f.assignees.length) q.assignees = [...f.assignees]
  if (r.milestone) q.milestone = r.milestone
  if (f.horizon.length) q.horizon = [...f.horizon]
  if (f.parent) q.parent = f.parent
  if (f.blocked) q.blocked = true
  if (f.q) q.q = f.q
  if (f.deleted) q.deleted = true
  q.sort = f.sort
  if (f.desc) q.desc = true
  if (f.group) q.group = f.group
  return q
}

/**
 * The filter a saved view keeps: its label ids by their names and its milestone's by its name, or as the id when another milestone has
 * that name (an id whose row has gone as the id itself).
 */
export function filterFromView(q: ViewQuery, names: FilterNames): DispatchFilter {
  const f = emptyFilter()
  f.status = unique((q.status ?? []).filter((s): s is TicketStatus => (TICKET_STATUSES as readonly string[]).includes(s)))
  f.priority = unique((q.priority ?? []).filter((p) => PRIORITIES.some((x) => x.value === p)))
  f.labels = unique((q.labels ?? []).map((id) => nameOf(names.labels, id)))
  f.owner = q.owner ?? ''
  f.assignees = unique((q.assignees ?? []).filter((a) => ASSIGNEE.test(a)))
  f.milestone = q.milestone ? milestoneValue(names.milestones, q.milestone) : ''
  f.horizon = unique((q.horizon ?? []).filter((h): h is Horizon => HORIZONS.some((x) => x.value === h)))
  f.parent = q.parent && parseTicketKey(q.parent) ? q.parent : ''
  f.blocked = !!q.blocked
  f.q = cutSearch(q.q ?? '')
  f.deleted = !!q.deleted
  f.sort = q.sort && isSort(q.sort) ? q.sort : 'updated'
  f.desc = !!q.desc
  f.group = q.group && (GROUPINGS as readonly string[]).includes(q.group) ? q.group : ''
  return f
}

/** A page of a list: at most `limit` tickets (1 to 200), after `cursor` (a page's `next`). */
export interface ListPage {
  limit?: number
  cursor?: string
}

/**
 * A list's request as GET /api/dispatch/tickets takes it, without its `?`: the project's key, the filter (labels and the milestone by
 * id, the priorities by number, a horizon's not planned as an empty value) and the page. Null when the filter cannot match a ticket of
 * the project (resolve): the list is empty without asking.
 */
export function apiQuery(project: string, f: DispatchFilter, names: FilterNames, page: ListPage = {}): string | null {
  const r = resolve(f, names)
  if (!r) return null
  const p = new URLSearchParams()
  if (project) p.set('project', project)
  for (const s of f.status) p.append('status', s)
  for (const n of f.priority) p.append('priority', String(n))
  for (const id of r.labels) p.append('label', id)
  if (f.owner) p.set('owner', f.owner)
  for (const a of f.assignees) p.append('assignee', a)
  if (r.milestone) p.set('milestone', r.milestone)
  for (const h of f.horizon) p.append('horizon', h)
  if (f.parent) p.set('parent', f.parent)
  if (f.blocked) p.set('blocked', '1')
  if (f.q.trim()) p.set('q', f.q)
  if (f.deleted) p.set('deleted', '1')
  p.set('sort', f.sort)
  if (f.desc) p.set('desc', '1')
  if (page.limit) p.set('limit', String(page.limit))
  if (page.cursor) p.set('cursor', page.cursor)
  return p.toString()
}

/**
 * Whether two filters list the same tickets in the same order (a saved view's chip shows itself applied by it): the same values in each
 * field whatever their order in the link, the same search, sort, direction and grouping.
 */
export function sameFilter(a: DispatchFilter, b: DispatchFilter): boolean {
  const set = (xs: readonly (string | number)[]) => JSON.stringify([...new Set(xs.map(String))].sort())
  return (
    set(a.status) === set(b.status) &&
    set(a.priority) === set(b.priority) &&
    set(a.labels) === set(b.labels) &&
    a.owner === b.owner &&
    set(a.assignees) === set(b.assignees) &&
    a.milestone === b.milestone &&
    set(a.horizon) === set(b.horizon) &&
    a.parent === b.parent &&
    a.blocked === b.blocked &&
    a.q.trim() === b.q.trim() &&
    a.deleted === b.deleted &&
    a.sort === b.sort &&
    a.desc === b.desc &&
    a.group === b.group
  )
}
