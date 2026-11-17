// A Brakeman request as one row says it, wherever a row is drawn (the page's Earlier, a run's or a ticket's panel, the answer under a
// task row): its kind in short, what it is about by name, its brief's counts, its age, who asked it, and whether it can be asked
// again. Pure: no Nuxt, no fetch. One set of words for a kind (the kinds' own labels, utils/brakeman.ts KINDS), never a second.
import type { BrakemanDigest, BrakemanMade, BrakemanRequest, BrakemanRow } from './brakeman'
import { KINDS, closed, kindWords, parseRef, refLabel, stateWords } from './brakeman'
import { relativeTime } from './sessions'

/** The names a page knows for the ids a ref holds (refLabel's). */
export interface RowNames {
  session?: (id: string) => string | undefined
  run?: (id: string) => string | undefined
}

/** A kind in short: its own label without the "…" the asking chip ends with ("Explain this", "Find"); a wake is "Woken by events". */
export function shortKind(kind: string): string {
  if (kind === 'wake' || !KINDS.some((k) => k.value === kind)) return kindWords(kind)
  return kindWords(kind).replace(/…$/, '')
}

/** A time of day as the rows say it: the browser's clock, hours and minutes. */
export function clockOfDay(iso: string): string {
  const t = new Date(iso)
  return Number.isNaN(t.getTime()) ? '' : t.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hour12: false })
}

/**
 * A request's kind as its row leads with it: a briefing with its start ("Brief me since 18:00", "Brief me since the last briefing"
 * when it named none), any other kind in short.
 */
export function kindLine(r: Pick<BrakemanRequest, 'kind' | 'since'>): string {
  if (r.kind !== 'briefing') return shortKind(r.kind)
  const at = r.since ? clockOfDay(r.since) : ''
  return at ? `${shortKind('briefing')} ${at}` : `${shortKind('briefing')} the last briefing`
}

/** One subject by name: a task's, an attempt's or a check's with its run's name after it ("task build of fix-login"). */
export function subjectWord(ref: string, names: RowNames = {}): string {
  const label = refLabel(ref, names)
  const r = parseRef(ref)
  if (r && (r.kind === 'task' || r.kind === 'attempt' || r.kind === 'check') && r.run) return `${label} of ${names.run?.(r.run) || r.run}`
  return label
}

/** A request's subjects by name, in its order, joined; "" for none. */
export function subjectWords(subjects: readonly string[] | undefined, names: RowNames = {}): string {
  return (subjects ?? []).map((s) => subjectWord(s, names)).join(', ')
}

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`

/** How many of a request's drafts a person made work: each item once, whose latest claim is made. */
export function madeCount(made: readonly BrakemanMade[] | undefined): number {
  const latest = new Map<number, BrakemanMade>()
  for (const m of made ?? []) {
    const had = latest.get(m.item)
    if (!had || (m.at ?? '') >= (had.at ?? '')) latest.set(m.item, m)
  }
  return [...latest.values()].filter((m) => m.state === 'made').length
}

/** The most counts a row says. */
export const MAX_COUNTS = 3

/**
 * A brief's counts as a row says them, at most MAX_COUNTS, in this order: its decisions ("1 decision"), proposals, drafts ("3 drafts,
 * 2 made"), facts. A brief with no decision says "no decision" in the last place left (never "for you": a row is not a person's
 * Needs you). None for a request with no digest.
 */
export function countsWords(digest: BrakemanDigest | undefined, made?: readonly BrakemanMade[]): string[] {
  if (!digest) return []
  const c = digest.counts ?? {}
  const out: string[] = []
  if (c.decisions) out.push(plural(c.decisions, 'decision', 'decisions'))
  if (c.proposals) out.push(plural(c.proposals, 'proposal', 'proposals'))
  if (c.drafts) {
    const n = madeCount(made)
    out.push(`${plural(c.drafts, 'draft', 'drafts')}${n ? `, ${n} made` : ''}`)
  }
  if (c.facts) out.push(plural(c.facts, 'fact', 'facts'))
  const kept = out.slice(0, c.decisions ? MAX_COUNTS : MAX_COUNTS - 1)
  if (!c.decisions) kept.push('no decision')
  return kept
}

/** A row's age: since its brief, else since it closed, else since it was asked ("2h ago"). */
export function rowAge(r: Pick<BrakemanRequest, 'briefAt' | 'closedAt' | 'createdAt'>, now = Date.now()): string {
  return relativeTime(r.briefAt || r.closedAt || r.createdAt, now, { suffix: true })
}

/** Who a request is of: "Asked by <name>" (the display name it was sent with, which is no identity), "Asked", or "Woken by events". */
export function askedWords(r: Pick<BrakemanRequest, 'kind' | 'by'>): string {
  if (r.kind === 'wake') return kindWords('wake')
  return r.by ? `Asked by ${r.by}` : 'Asked'
}

/** A row's first line: its brief's title, or "" for a request with no brief (its row then says why: noBriefWords). */
export function rowTitle(r: Pick<BrakemanRow, 'digest'>): string {
  return r.digest?.title ?? ''
}

/** Why a closed request has no brief, as its row says it ("no brief: timed out after 15 min"); "" for one with a brief or still open. */
export function noBriefWords(r: Pick<BrakemanRequest, 'state' | 'why' | 'sessionId' | 'config' | 'hasBrief'>, sessionName?: string): string {
  if (r.hasBrief || !closed(r.state)) return ''
  return `no brief: ${stateWords(r, sessionName)}`
}

/**
 * A row's second line, in parts: its kind (a briefing with its start), its subjects by name, then its brief's counts (countsWords);
 * a request with no brief says why after its kind and subjects instead.
 */
export function rowMeta(r: BrakemanRow, names: RowNames = {}, sessionName?: string): string[] {
  const out = [kindLine(r)]
  const subjects = subjectWords(r.subjects, names)
  if (subjects) out.push(subjects)
  if (r.digest) out.push(...countsWords(r.digest, r.made))
  else {
    const why = noBriefWords(r, sessionName)
    if (why) out.push(why)
  }
  return out
}

/** What a request asks, as the ask route takes it: the same ask under a new id is "Ask again". */
export interface AskBody {
  kind: string
  subjects?: string[]
  note?: string
  since?: string
}

/** The ask a request made, as it would be sent again. */
export function askOf(r: Pick<BrakemanRequest, 'kind' | 'subjects' | 'note' | 'since'>): AskBody {
  const out: AskBody = { kind: r.kind }
  if (r.subjects?.length) out.subjects = [...r.subjects]
  if (r.note) out.note = r.note
  if (r.since) out.since = r.since
  return out
}

const sameAsk = (a: AskBody, b: AskBody) => a.kind === b.kind && (a.note ?? '') === (b.note ?? '') && (a.since ?? '') === (b.since ?? '') && (a.subjects ?? []).join('\n') === (b.subjects ?? []).join('\n')

/**
 * Whether a row offers "Ask again", and what it sends: a closed request with no brief, unless it was cancelled (a person ended it),
 * it was a wake (no person may ask one), or the same ask waits already among `open` (queued or under way). Null when it offers none.
 */
export function askAgain(r: BrakemanRequest, open: readonly Pick<BrakemanRequest, 'kind' | 'subjects' | 'note' | 'since' | 'state'>[]): AskBody | null {
  if (r.hasBrief || !closed(r.state) || r.state === 'cancelled' || r.kind === 'wake') return null
  const body = askOf(r)
  if (open.some((o) => !closed(o.state) && sameAsk(askOf(o), body))) return null
  return body
}

/**
 * The head line of a brief shown in the page's card on a phone, and on a panel: "Latest brief · 12m ago · woken by events", or the
 * request's id in place of "Latest brief" for one a link named.
 */
export function briefHeadWords(r: Pick<BrakemanRequest, 'id' | 'kind' | 'by' | 'briefAt' | 'closedAt' | 'createdAt'>, latest: boolean, now = Date.now()): string {
  const who = askedWords(r)
  return `${latest ? 'Latest brief' : r.id} · ${rowAge(r, now)} · ${who.charAt(0).toLowerCase()}${who.slice(1)}`
}

/** Earlier's filters: All, With a decision, Briefings (a person's and the background role's), Drafts; each the list route's query. */
export type EarlierFilter = 'all' | 'decision' | 'briefings' | 'drafts'
export const EARLIER_FILTERS: ReadonlyArray<{ key: EarlierFilter; label: string; query?: { kind?: string[]; has?: 'decision' | 'draft' } }> = [
  { key: 'all', label: 'All' },
  { key: 'decision', label: 'With a decision', query: { has: 'decision' } },
  { key: 'briefings', label: 'Briefings', query: { kind: ['briefing', 'wake'] } },
  { key: 'drafts', label: 'Drafts', query: { has: 'draft' } },
]

/** The longest search the list route takes, in bytes. */
export const MAX_SEARCH = 200

/** Why a search cannot be sent ("" when it can, or for none): one line of at most MAX_SEARCH bytes. */
export function searchProblem(q: string): string {
  const t = q.trim()
  if (!t) return ''
  if (/[\r\n]/.test(t)) return 'A search is one line.'
  if (new TextEncoder().encode(t).length > MAX_SEARCH) return `A search is at most ${MAX_SEARCH} bytes.`
  return ''
}

/**
 * Whether a row matches a search on a server that takes none (the pages the page read, searched here): the words, case folded, in
 * its title, its note, its subjects (as refs and by name), its kind's words or its id.
 */
export function localMatch(r: BrakemanRow, q: string, names: RowNames = {}): boolean {
  const w = q.trim().toLowerCase()
  if (!w) return true
  const hay = [r.digest?.title, r.digest?.reading, r.note, r.id, kindLine(r), ...(r.subjects ?? []), subjectWords(r.subjects, names)]
  return hay.some((h) => !!h && h.toLowerCase().includes(w))
}
