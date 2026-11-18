// Earlier's filtered list on the Brakeman page (the list route's filters and search): the ids the server answered, in its order, the
// cursor to read on after, and whether a read is out; one read at a time, a query changed while one is out read fresh after it. And
// the rows drawn from those ids: only requests that are done (the server's filters also match a turn queued or under way, which is In
// progress's, not Earlier's). Pure: no Nuxt; the component gives it its list read and is told of each change.
import type { BrakemanListQuery } from '~/composables/useBrakeman'
import type { BrakemanRow } from './brakeman'
import { closed } from './brakeman'

export type ListRead = (q: BrakemanListQuery) => Promise<{ requests: BrakemanRow[]; next: string; filtered: boolean }>

export class FilteredList {
  /** The ids the server answered, its order, newest first. */
  ids: string[] = []
  /** The id to read the next page after; "" when the server has none. */
  next = ''
  /** A first page of a new query is being read: the list has no answer for it yet. */
  reading = false
  /** Why the last read failed, in the server's words. */
  failed = ''
  private q: BrakemanListQuery | null = null
  /** Moves with every new query: a reply to a read asked under another is dropped. */
  private sent = 0
  private busy = false
  private again = false
  /** The read waiting behind the one out is a new query's first page (its ids and cursor replace the list's). */
  private againFresh = false

  constructor(
    private readonly list: ListRead,
    private readonly changed: () => void,
    /** The server took no filter (an older one): the page filters what it holds. */
    private readonly older: () => void,
  ) {}

  /** The query now, or null for none (the page shows the live store's list). */
  get query(): BrakemanListQuery | null {
    return this.q
  }

  /** A new query: the list is emptied and its first page read (after the read out, if one is). */
  set(q: BrakemanListQuery | null) {
    this.q = q
    this.ids = []
    this.next = ''
    this.failed = ''
    this.sent++
    this.reading = !!q
    this.changed()
    if (q) void this.read(true)
  }

  /** Something changed that the list may hold (a brief kept, the stream back): its first page again, the rows shown keeping their place. */
  refresh() {
    if (this.q) void this.read(false)
  }

  private async read(fresh: boolean) {
    const q = this.q
    if (!q) return
    if (this.busy) {
      this.again = true
      this.againFresh ||= fresh
      return
    }
    this.busy = true
    const mine = this.sent
    try {
      const out = await this.list(q)
      if (mine !== this.sent || this.q !== q) return
      if (!out.filtered) {
        this.older()
        return
      }
      this.failed = ''
      const got = out.requests.map((r) => r.id)
      // A page read again: what is new goes first, what was shown keeps its place.
      this.ids = fresh ? got : [...got.filter((id) => !this.ids.includes(id)), ...this.ids]
      if (fresh) this.next = out.next
      this.reading = false
    } catch (e) {
      if (mine === this.sent) {
        this.failed = (e as Error).message
        this.reading = false
      }
    } finally {
      this.busy = false
      this.changed()
      if (this.again) {
        const f = this.againFresh
        this.again = false
        this.againFresh = false
        void this.read(f)
      }
    }
  }

  /** Reads the page after the last one read. */
  async more(): Promise<void> {
    const q = this.q
    if (!q || !this.next) return
    const mine = this.sent
    try {
      const out = await this.list({ ...q, after: this.next })
      if (mine !== this.sent || this.q !== q) return
      this.ids = [...this.ids, ...out.requests.map((r) => r.id).filter((id) => !this.ids.includes(id))]
      this.next = out.next
    } catch (e) {
      if (mine === this.sent) this.failed = (e as Error).message
    } finally {
      this.changed()
    }
  }
}

/** The rows of a filtered list as the live store holds each now: the requests done alone, in the list's order. */
export function filteredRows(ids: readonly string[], requests: readonly BrakemanRow[]): BrakemanRow[] {
  const byId = new Map(requests.map((r) => [r.id, r]))
  return ids.map((id) => byId.get(id)).filter((r): r is BrakemanRow => !!r && closed(r.state))
}
