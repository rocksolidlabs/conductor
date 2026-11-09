// The command palette's pure parts (#302): the prefix a query begins with,
// the verbs, the ranking with frecency, the grouping and the match
// highlight. Nothing here fetches or acts; the composable (usePalette)
// reads the live stores and the component renders what these return.

export type PaletteMode = 'search' | 'actions' | 'runs' | 'files' | 'people' | 'events' | 'shortcuts' | 'history'

/** The prefix a mode is typed with; the search has none. `~` is history (Program item HS1): what was said, decided and checked, from the server. */
export const MODE_PREFIX: Record<Exclude<PaletteMode, 'search'>, string> = { actions: '>', runs: '#', files: ':', people: '@', events: '!', shortcuts: '?', history: '~' }

/** What a query's first character picks: the mode, and the rest of the query, trimmed. */
export function parsePrefix(query: string): { mode: PaletteMode; rest: string } {
  const q = query.replace(/^\s+/, '')
  for (const [mode, prefix] of Object.entries(MODE_PREFIX) as [Exclude<PaletteMode, 'search'>, string][]) {
    if (q.startsWith(prefix)) return { mode, rest: q.slice(prefix.length).trim() }
  }
  return { mode: 'search', rest: q.trim() }
}

/** A query with its prefix dropped (Backspace on an empty query drops the mode). */
export function dropPrefix(query: string): string {
  const { mode } = parsePrefix(query)
  return mode === 'search' ? query : ''
}

// ---------------------------------------------------------------------------
// Verbs: a line is read as a verb only when its first word is one of the
// twelve and its second word names exactly one session, run or crew (by the
// same match the filter uses); otherwise it is a search. `go` and `open`
// take a page or a path instead of a name.

export const VERBS = ['stop', 'share', 'answer', 'reply', 'say', 'broadcast', 'launch', 'run', 'resume', 'open', 'mark', 'go'] as const
export type Verb = (typeof VERBS)[number]

/** What kind of thing a verb's name may be; `go` and `open` take none. */
const VERB_KINDS: Record<Verb, Array<'session' | 'run' | 'crew' | 'agent'>> = {
  stop: ['session', 'run'],
  share: ['session', 'run'],
  answer: ['session'],
  reply: ['session'],
  say: ['session'],
  broadcast: ['run'],
  launch: ['agent'],
  run: ['crew'],
  resume: ['session', 'run'],
  open: [],
  mark: ['session'],
  go: [],
}

export const PAGES = ['yard', 'roundhouse', 'crews', 'dispatch', 'agents', 'events', 'history', 'settings', 'paste'] as const
export type Page = (typeof PAGES)[number]

export interface Named {
  kind: 'session' | 'run' | 'crew' | 'agent'
  id: string
  name: string
  /** What the filter matches beside the name (a session's cwd, host, branch, agent). */
  words?: string[]
}

export type ParsedVerb =
  | { verb: 'stop' | 'resume'; target: Named }
  | { verb: 'share'; target: Named; role: 'view' | 'control' }
  | { verb: 'answer'; target: Named; choice: number }
  | { verb: 'reply' | 'say'; target: Named; text: string }
  | { verb: 'broadcast'; target: Named; text: string }
  | { verb: 'launch'; target: Named; dir?: string }
  | { verb: 'run'; target: Named; label?: string }
  | { verb: 'open'; path: string; line?: number }
  | { verb: 'mark'; target: Named; state: 'waiting' | 'working' | 'clear' }
  | { verb: 'go'; page: Page }

/** The things a name matches, by the filter's rule: a word of the name or of its other words holds the query, case folded. */
export function matchNamed(names: readonly Named[], query: string, kinds?: readonly Named['kind'][]): Named[] {
  const q = query.trim().toLowerCase()
  if (!q) return []
  return names.filter((n) => (!kinds || kinds.includes(n.kind)) && [n.name, ...(n.words ?? [])].some((w) => w.toLowerCase().includes(q)))
}

/**
 * The verb a query reads as, or null for a search: the first word one of
 * the twelve, the second naming exactly one thing of the kinds the verb
 * takes (an ambiguous name is a search; a name that matches one session,
 * run and crew at once is a search unless the verb takes one kind alone);
 * the rest of the line the verb's text, choice, role, label or directory.
 */
export function parseVerb(query: string, names: readonly Named[]): ParsedVerb | null {
  const words = query.trim().split(/\s+/).filter(Boolean)
  if (words.length < 2) return null
  const verb = words[0]!.toLowerCase() as Verb
  if (!VERBS.includes(verb)) return null
  const rest = words.slice(1)
  if (verb === 'go') {
    const page = rest[0]!.toLowerCase() as Page
    return rest.length === 1 && PAGES.includes(page) ? { verb, page } : null
  }
  if (verb === 'open') {
    const m = /^(.*?)(?::(\d+))?$/.exec(rest.join(' '))
    return m && m[1] ? { verb, path: m[1], line: m[2] ? Number(m[2]) : undefined } : null
  }
  const kinds = VERB_KINDS[verb]
  // The name is the longest run of words from the second that matches exactly one thing: "stop api sweep" names "api sweep".
  let target: Named | undefined
  let after: string[] = []
  for (let n = rest.length; n >= 1; n--) {
    const name = rest.slice(0, n).join(' ')
    const found = matchNamed(names, name, kinds)
    if (found.length === 1) {
      target = found[0]
      after = rest.slice(n)
      break
    }
    if (found.length > 1 && n === 1) return null
  }
  if (!target) return null
  const text = after.join(' ')
  switch (verb) {
    case 'stop':
    case 'resume':
      return after.length ? null : { verb, target }
    case 'share': {
      const role = (after[0] ?? 'view').toLowerCase()
      return after.length <= 1 && (role === 'view' || role === 'control') ? { verb, target, role } : null
    }
    case 'answer': {
      const choice = Number(after[0])
      return after.length === 1 && Number.isInteger(choice) && choice >= 1 && choice <= 9 ? { verb, target, choice } : null
    }
    case 'reply':
    case 'say':
    case 'broadcast':
      return text ? { verb, target, text } : null
    case 'launch': {
      if (!after.length) return { verb, target }
      return after[0]!.toLowerCase() === 'in' && after.length >= 2 ? { verb, target, dir: after.slice(1).join(' ') } : null
    }
    case 'run': {
      if (!after.length) return { verb, target }
      return after[0]!.toLowerCase() === 'as' && after.length >= 2 ? { verb, target, label: after.slice(1).join(' ').slice(0, 60) } : null
    }
    case 'mark': {
      const state = (after[0] ?? '').toLowerCase()
      return after.length === 1 && (state === 'waiting' || state === 'working' || state === 'clear') ? { verb, target, state } : null
    }
  }
}

// ---------------------------------------------------------------------------
// Frecency: how often and how lately a row was used, kept per browser.

export const RECENT_KEY = 'conductor.palette.recent'
export const RECENT_MAX = 200

export interface RecentUse {
  count: number
  /** Milliseconds since the epoch. */
  last: number
}

export type Recent = Record<string, RecentUse>

export function readRecent(storage: Pick<Storage, 'getItem'> | null): Recent {
  try {
    const raw = storage?.getItem(RECENT_KEY)
    if (!raw) return {}
    const v = JSON.parse(raw) as unknown
    if (!v || typeof v !== 'object' || Array.isArray(v)) return {}
    const out: Recent = {}
    for (const [k, u] of Object.entries(v as Record<string, unknown>)) {
      if (u && typeof u === 'object' && typeof (u as RecentUse).count === 'number' && typeof (u as RecentUse).last === 'number') out[k] = { count: (u as RecentUse).count, last: (u as RecentUse).last }
    }
    return out
  } catch {
    return {}
  }
}

/** `recent` with one more use of `key` now, the least lately used dropped past RECENT_MAX entries. */
export function noteUse(recent: Recent, key: string, now = Date.now()): Recent {
  const out: Recent = { ...recent, [key]: { count: (recent[key]?.count ?? 0) + 1, last: now } }
  const keys = Object.keys(out)
  if (keys.length > RECENT_MAX) {
    keys.sort((a, b) => out[a]!.last - out[b]!.last)
    for (const k of keys.slice(0, keys.length - RECENT_MAX)) delete out[k]
  }
  return out
}

export function writeRecent(storage: Pick<Storage, 'setItem'> | null, recent: Recent): void {
  try {
    storage?.setItem(RECENT_KEY, JSON.stringify(recent))
  } catch {
    // The browser may refuse storage; the palette then forgets at the reload.
  }
}

/** A row's frecency: its count, decayed by how long ago it was used (half every day); 0 for one never used. */
export function frecency(recent: Recent, key: string, now = Date.now()): number {
  const u = recent[key]
  if (!u) return 0
  const days = Math.max(0, now - u.last) / 86_400_000
  return u.count / 2 ** days
}

// ---------------------------------------------------------------------------
// Rows, groups and ranking.

export interface PaletteRow {
  /** The row's key, stable across opens: `session:<id>`, `run:<id>`, `action:<id>`… (frecency is kept by it). */
  key: string
  label: string
  /** The second line: the prompt's message, `agent · where · how long`, a crew's goal. */
  detail?: string
  /** What the query matches beside the label. */
  words?: string[]
  /** A query equal to one of these, case folded, ranks the row first in its group whatever else was used lately: a ticket's key. */
  exact?: string[]
}

export interface PaletteGroup<R extends PaletteRow = PaletteRow> {
  id: string
  label: string
  rows: R[]
  /** Needs you and the page's group keep their place and every row whatever the query. */
  pinned?: boolean
}

/** Where `query` sits in `text`, case folded: the start and end, or null. */
export function matchRange(text: string, query: string): [number, number] | null {
  const q = query.trim().toLowerCase()
  if (!q) return null
  const i = text.toLowerCase().indexOf(q)
  return i < 0 ? null : [i, i + q.length]
}

/** Whether a row matches the query: its label, detail or words hold it. */
export function rowMatches(row: PaletteRow, query: string): boolean {
  const q = query.trim().toLowerCase()
  if (!q) return true
  return [row.label, row.detail ?? '', ...(row.words ?? [])].some((w) => w.toLowerCase().includes(q))
}

/** Whether the query is one of the row's exact names (a ticket's key typed whole), case folded. */
export function rowExact(row: PaletteRow, query: string): boolean {
  const q = query.trim().toLowerCase()
  return !!q && !!row.exact?.some((w) => w.toLowerCase() === q)
}

/**
 * The groups ranked for a query: Needs you first, always, then the page's
 * own group, then the others in their order; within a group the rows that
 * match the query (every row of a pinned group with no query), a row the
 * query names exactly first (a ticket's key typed whole), then the ones
 * used lately (frecency), then the group's order. A group left with no
 * rows is dropped.
 */
export function rank<R extends PaletteRow>(groups: readonly PaletteGroup<R>[], query: string, recent: Recent, now = Date.now()): PaletteGroup<R>[] {
  const q = query.trim()
  const order = (g: PaletteGroup<R>) => (g.id === 'needs' ? 0 : g.id === 'page' ? 1 : 2)
  return [...groups]
    .sort((a, b) => order(a) - order(b))
    .map((g) => {
      const rows = g.rows
        .map((r, i) => ({ r, i, f: frecency(recent, r.key, now), x: rowExact(r, q) ? 1 : 0 }))
        .filter(({ r }) => (!q && g.pinned) || rowMatches(r, q))
        .sort((a, b) => b.x - a.x || b.f - a.f || a.i - b.i)
        .map(({ r }) => r)
      return { ...g, rows }
    })
    .filter((g) => g.rows.length > 0)
}

/**
 * A long group cut to its first `n` rows, as ranked, its label saying how many it holds: the rest are found by typing. The Tickets group
 * may hold a thousand open tickets, which drawn whole would slow every open of the palette and every key typed in it.
 */
export function capGroup<R extends PaletteRow>(groups: readonly PaletteGroup<R>[], id: string, n: number): PaletteGroup<R>[] {
  return groups.map((g) => (g.id === id && g.rows.length > n ? { ...g, label: `${g.label} · ${n} of ${g.rows.length}, type to find the rest`, rows: g.rows.slice(0, n) } : g))
}

/**
 * `group` placed in `groups` (ranked) right after the last group whose id is one of `after`, else first: the History group goes after
 * Tickets and before Shared with you, wherever ranking left the groups before it. A group with no rows is not placed.
 */
export function placeAfter<R extends PaletteRow>(groups: readonly PaletteGroup<R>[], group: PaletteGroup<R>, after: readonly string[]): PaletteGroup<R>[] {
  if (!group.rows.length) return [...groups]
  let at = 0
  groups.forEach((g, i) => {
    if (after.includes(g.id)) at = i + 1
  })
  return [...groups.slice(0, at), group, ...groups.slice(at)]
}
