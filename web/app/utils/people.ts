import type { KnownPerson, PersonTask, PersonWork } from '~/composables/usePeople'
import type { PaletteItem } from '~/composables/usePalette'
import type { TicketSummary, TicketsPage } from '~/composables/useDispatch'
import { responsibilityWords } from './crews'
import { capGroup, type PaletteGroup } from './palette'

// The people Conductor knows, as the app shows them (docs/plans/decisions.md, HM1, PR-11): a person's page and the palette's `@` rows.
// A person is a local label, never a session: their page lists their work, and a row opens it.

/**
 * A person's name in a path: the one rule every link to their page and every read of their work uses, the server's too
 * (internal/api/people.go, personSegment and personFromSegment; internal/api/testdata/person-segments.json the cases both are tested
 * against). Any string the people rules allow is a name, so a name is written as is only when nothing between the browser and the route
 * can read it as something else, and otherwise as a token nothing reads:
 *
 * - A plain name is written as itself, escaped as a segment (a space as `%20`, a character past ASCII as its UTF-8 bytes): every character
 *   an ASCII letter or digit, `-`, `_`, `.`, a space, or one past ASCII; the first neither `.` nor a space, the last not a space. Unescaped
 *   it holds no `/`, `\`, `%`, `?`, `#`, `;` or `~` and is never `.` or `..`, so no decoding, second decoding or cleaning of the path (the
 *   browser's, the router's, the server's, a proxy's) changes it or moves it out of its segment.
 * - Any other name is written as `~` and the base64url of its UTF-8, with no padding: letters, digits, `-` and `_` after the `~`.
 *
 * A segment is read back only in the one form its name is written in, so each name has one address and no address is another name's.
 */
export function plainPersonName(name: string): boolean {
  if (!name || name[0] === '.' || name[0] === ' ' || name.endsWith(' ')) return false
  for (const ch of name) {
    if (ch.codePointAt(0)! < 0x80 && !/^[A-Za-z0-9._ -]$/.test(ch)) return false
  }
  return true
}

function base64url(bytes: Uint8Array): string {
  let s = ''
  for (const b of bytes) s += String.fromCharCode(b)
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

/** The path segment of a person's name, escaped as a URL writes it. */
export function personSegment(name: string): string {
  if (plainPersonName(name)) return encodeURIComponent(name)
  return `~${base64url(new TextEncoder().encode(name))}`
}

/** The name a segment carries, the segment unescaped as the router hands it; null for a segment that is no name's (not the one form `personSegment` writes for it). */
export function personFromSegment(segment: string): string | null {
  if (!segment.startsWith('~')) return plainPersonName(segment) ? segment : null
  const token = segment.slice(1)
  if (!/^[A-Za-z0-9_-]+$/.test(token) || token.length % 4 === 1) return null
  let bytes: Uint8Array
  let name: string
  try {
    const bin = atob(token.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - (token.length % 4)) % 4))
    bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0))
    name = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(bytes)
  } catch {
    return null
  }
  if (!name || plainPersonName(name) || base64url(bytes) !== token) return null
  return name
}

/** A person's page: their name exactly, as the server matches it. */
export function personPath(name: string): string {
  return `/people/${personSegment(name)}`
}

/**
 * Where a person is known from, in words: what they hold in each crew that lists them ("architecture owner in api"), a crew where they
 * hold nothing by its name alone ("in web"), "and more crews" past the server's 20, and Dispatch's people when no crew lists them.
 */
export function personDetail(p: KnownPerson): string {
  const crews = p.crews.map((c) => {
    const words = responsibilityWords(c.responsibilities)
    return words ? `${words} in ${c.name}` : `in ${c.name}`
  })
  if (p.crewsMore) crews.push('and more crews')
  if (crews.length) return crews.join(', ')
  return p.dispatch ? "in Dispatch's people" : ''
}

/**
 * The palette's People rows (the `@` mode, between the page's people and You): one per person whose name contains `query` ignoring case,
 * as the server's `q` keeps them (so a row the detail alone matches, read for an earlier query, is never offered and then withdrawn), by
 * name (code unit order, as the server pages them), "nate · architecture owner in api", Enter opening their page through `open`, ⌘↵ in a
 * new tab.
 */
export function personRows(people: readonly KnownPerson[], open: (path: string) => void, query = ''): PaletteItem[] {
  const q = query.trim().toLowerCase()
  return people
    .filter((p) => !q || p.name.toLowerCase().includes(q))
    .sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0))
    .map((p) => {
      const href = personPath(p.name)
      return { key: `person:${p.name}`, label: p.name, detail: personDetail(p) || undefined, icon: 'i-lucide-user-round', href, run: () => open(href) }
    })
}

/** How a person came to own a task, in words: "the acceptance owner" when the task names no owner of its own, '' when it names them. */
export function ownedHow(t: Pick<PersonTask, 'ownerFrom'>): string {
  return t.ownerFrom === 'acceptance' ? 'as the acceptance owner' : ''
}

/** The People group's rows shown with nothing typed after the `@`, and with something typed (`capPeople`). */
export const PEOPLE_IDLE = 10
export const PEOPLE_MATCHED = 50

/** The palette's People group (id `known-people`) cut to its first rows as ranked, as the Tickets group is: the rest are found by typing. */
export function capPeople<R extends PaletteItem>(groups: readonly PaletteGroup<R>[], query: string): PaletteGroup<R>[] {
  return capGroup(groups, 'known-people', query.trim() ? PEOPLE_MATCHED : PEOPLE_IDLE)
}

/**
 * Reads of one query typed key by key, one at a time: a read asked while one is under way waits for it and then the latest asked is
 * read, those asked between never sent; a reply is applied only when nothing newer was asked meanwhile, so a slow one never puts back
 * what a newer one replaced. A failed read applies nothing. Each call's promise settles once the reads it waited behind are done.
 */
export function oneReadAtATime<T>(read: (q: string) => Promise<T>, apply: (value: T) => void): (q: string) => Promise<void> {
  let running: Promise<void> | null = null
  let pending: string | null = null
  async function loop(first: string): Promise<void> {
    let q: string | null = first
    while (q !== null) {
      try {
        const value = await read(q)
        if (pending === null) apply(value)
      } catch {
        // Nothing applied: what was read before stays.
      }
      q = pending
      pending = null
    }
  }
  return (q: string) => {
    if (running) {
      pending = q
      return running
    }
    running = loop(q).finally(() => (running = null))
    return running
  }
}

/** How long a read of a person's work may take before it is given up: its fetch aborted, the page saying so, the next read free to start. */
export const PERSON_READ_MS = 15_000

/** The words a read given up after `ms` fails with. */
export function noReplyWords(ms: number): string {
  return `the server sent no reply in ${Math.round(ms / 1000)} s`
}

/**
 * A read bounded in time and cancellable: `read` is handed `ctl`'s signal, which is aborted when `ms` pass without its reply (the promise
 * rejecting with `noReplyWords`) or when the caller aborts `ctl` (rejecting at once). A reply that comes after either is never returned,
 * so a stalled read never holds the reads queued after it (`SerialReads`) and a late one never puts back what a newer read replaced.
 */
export function boundedRead<T>(read: (signal: AbortSignal) => Promise<T>, ms: number, ctl: AbortController = new AbortController()): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    let done = false
    const settle = (f: () => void) => {
      if (done) return
      done = true
      clearTimeout(timer)
      ctl.signal.removeEventListener('abort', aborted)
      f()
    }
    const timer = setTimeout(() => {
      const why = new Error(noReplyWords(ms))
      settle(() => reject(why))
      ctl.abort(why)
    }, ms)
    const aborted = () => settle(() => reject(ctl.signal.reason instanceof Error ? ctl.signal.reason : new Error('the read was given up')))
    if (ctl.signal.aborted) {
      aborted()
      return
    }
    ctl.signal.addEventListener('abort', aborted)
    read(ctl.signal).then(
      (v) => settle(() => resolve(v)),
      (e) => settle(() => reject(e)),
    )
  })
}

/** The tickets one page of a person's list reads (`ticketsPageQuery`). */
export const TICKETS_PAGE = 20

/**
 * One of a person's ticket lists on their page: Dispatch's own list (`href`, the person's route's `ownerHref` or `assigneeHref`), the
 * tickets read so far, the cursor of the page after them (`next`), and what went wrong with the last read (`error`).
 */
export interface PersonTicketList {
  id: 'owner' | 'assignee'
  label: string
  href: string
  total: number
  tickets: TicketSummary[]
  next?: string
  read: boolean
  error: string
}

/** A person's two ticket lists as a read of their work starts them: each with tickets, its first page not read yet. */
export function freshTicketLists(tickets: PersonWork['tickets']): PersonTicketList[] {
  return tickets
    ? [
        { id: 'owner' as const, label: 'Owned', href: tickets.ownerHref, total: tickets.owner, tickets: [], read: false, error: '' },
        { id: 'assignee' as const, label: 'Assigned', href: tickets.assigneeHref, total: tickets.assignee, tickets: [], read: false, error: '' },
      ].filter((l) => l.total)
    : []
}

/**
 * Whether a read of a person's work keeps the ticket lists the page shows, every page read through Show more with them: only a read made
 * for a decision's change (`decisionsOnly`) of the person the lists were read for (`listsFor`), whose tickets' lists and counts are the
 * ones the lists were started from (`from`). Any other read starts them again from their first page.
 */
export function keepsTicketLists(decisionsOnly: boolean, listsFor: string | null, who: string, from: PersonWork['tickets'] | undefined, now: PersonWork['tickets']): boolean {
  if (!decisionsOnly || listsFor !== who || from === undefined) return false
  if (from === null || now === null) return from === now
  return from.owner === now.owner && from.assignee === now.assignee && from.ownerHref === now.ownerHref && from.assigneeHref === now.assigneeHref
}

/** The query of a page of a person's list: the list's own filter, a page of `TICKETS_PAGE`, after `cursor` when one is given. */
export function ticketsPageQuery(href: string, cursor?: string): string {
  const q = new URLSearchParams(href.split('?')[1] ?? '')
  q.set('limit', String(TICKETS_PAGE))
  if (cursor) q.set('cursor', cursor)
  return q.toString()
}

/**
 * Reads the next page of a person's list (the first when none was read) and adds its tickets after those read, each once; a failed read
 * keeps what was read and says it in `error`, for the list alone, and the same page is read again on the next call. `current` says
 * whether the list is still the one shown when the reply comes.
 */
export async function readTicketsPage(list: PersonTicketList, fetch: (query: string) => Promise<TicketsPage>, current: () => boolean = () => true): Promise<void> {
  try {
    const page = await fetch(ticketsPageQuery(list.href, list.read ? list.next : undefined))
    if (!current()) return
    const seen = new Set(list.tickets.map((t) => t.id))
    list.tickets = [...list.tickets, ...(page.tickets ?? []).filter((t) => !seen.has(t.id))]
    list.next = page.next || undefined
    list.total = page.total ?? list.total
    list.read = true
    list.error = ''
  } catch (e) {
    if (!current()) return
    list.error = (e as Error).message || 'the tickets could not be read'
  }
}

/**
 * What one list of a person's page is, the rule every list there follows: read and holding rows, read and empty, or not read, wholly or
 * for some of its records (a crew, a run's record, Dispatch). A list not read whole says so in its place (`unread`), with the rows read
 * above it, and is never drawn as empty or as all there is.
 */
export interface PersonList {
  state: 'rows' | 'empty' | 'unread'
  /** What could not be read and what that leaves out, in words; '' when the list was read whole. */
  unread: string
}

/** The person's page's lists (their crews, the tasks they own, Dispatch's tickets) by what the server read for each. */
export function personLists(w: Pick<PersonWork, 'crews' | 'tasks' | 'tickets' | 'crewsUnavailable' | 'recordsUnavailable' | 'dispatchUnavailable'>): Record<'crews' | 'tasks' | 'tickets', PersonList> {
  const list = (rows: number, unread: boolean, some: string, none: string): PersonList => ({
    state: rows ? 'rows' : unread ? 'unread' : 'empty',
    unread: unread ? (rows ? some : none) : '',
  })
  const tickets = w.tickets
  return {
    crews: list(w.crews.length, !!w.crewsUnavailable, 'Some saved crews could not be read: they may be in more crews than these.', 'Some saved crews could not be read: a crew that lists them may not be shown.'),
    tasks: list(
      w.tasks.length,
      !!w.recordsUnavailable,
      'Some records of runs the server no longer holds could not be read: they may own more tasks than these.',
      'Some records of runs the server no longer holds could not be read: a task they own there may not be shown.',
    ),
    tickets: tickets
      ? list(tickets.owner + tickets.assignee, false, '', '')
      : { state: 'unread', unread: 'Dispatch could not be read: the tickets they own or are assigned are not shown.' },
  }
}
