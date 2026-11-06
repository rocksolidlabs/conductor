import type { KnownPerson, PersonTask } from '~/composables/usePeople'
import type { PaletteItem } from '~/composables/usePalette'
import type { TicketSummary, TicketsPage } from '~/composables/useDispatch'
import { responsibilityWords } from './crews'
import { capGroup, type PaletteGroup } from './palette'

// The people Conductor knows, as the app shows them (docs/plans/decisions.md, HM1, PR-11): a person's page and the palette's `@` rows.
// A person is a local label, never a session: their page lists their work, and a row opens it.

/**
 * A person's name as one segment of a path, the one rule every link to their page and every read of their work uses: the name escaped,
 * with a `~` before it when it is `.` or `..` (which a URL resolves as steps of its path, `%2E` included) or starts with `~`, so every
 * name has a segment of its own and none is read as another. The server removes one leading `~` (`personFromSegment`).
 */
export function personSegment(name: string): string {
  return (name === '.' || name === '..' || name.startsWith('~') ? '~' : '') + encodeURIComponent(name)
}

/** The name a person's path segment carries, unescaped as the router hands it: one leading `~` removed. */
export function personFromSegment(segment: string): string {
  return segment.startsWith('~') ? segment.slice(1) : segment
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
