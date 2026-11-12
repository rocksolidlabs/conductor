import { asCapacityEvent, capacityKey, capacityParams, readWait, type CapacityQuery, type CapacityView } from '../utils/capacity'

/** What the store asks of the server: a view (`GET /api/capacity`, which reads nothing) and a read (`POST /api/capacity/read`). */
export interface CapacityReads {
  view: (q: CapacityQuery) => Promise<CapacityView>
  read: (q: CapacityQuery, check: boolean) => Promise<CapacityView>
}

/** What is known of one question: the last view answered, the error of the last ask, and whether a read is out. */
export interface CapacityEntry {
  query: CapacityQuery
  view: CapacityView | null
  error: string
  /** A POST /api/capacity/read of this question is out. */
  reading: boolean
}

interface Slot extends CapacityEntry {
  /** How many pages show it: an event reads again only what is shown. */
  watchers: number
  /** A GET of it is out (one at a time), and whether another is owed once it lands. */
  fetching: boolean
  again: boolean
  /** The number of the last ask sent, and of the last whose answer was taken: an answer older than one taken is dropped. */
  sent: number
  taken: number
  /** Why the last read was refused, in the server's words; "" when it was not. */
  readError: string
}

/**
 * The capacity views the app shows (docs/plans/capacity.md, PR-5), one for the app: a view per question (an agent, an account, the
 * launch's model, yolo and place), fetched one at a time per question, and fetched again when the `capacity` event names its key or
 * the events stream comes back (`snapshot`). A read is asked only through `read`, which sends nothing while the view's
 * `nextReadAfter` is ahead of the clock: the server would answer the kept reading anyway. Each answer is the question's that asked
 * it: one older than an answer taken is dropped, so a GET that crossed a read never puts the earlier view back.
 */
export class CapacityLive {
  private slots = new Map<string, Slot>()
  private reads: CapacityReads | null = null
  /** Bumped on every change, for a page's computed to follow. */
  changed: () => void = () => {}

  constructor(private readonly now: () => number = Date.now) {}

  attach(reads: CapacityReads) {
    this.reads = reads
  }

  /** What is known of q now (a copy a page may read; never the slot itself). */
  entry(q: CapacityQuery): CapacityEntry {
    const s = this.slots.get(capacityKey(q))
    return s ? { query: s.query, view: s.view, error: s.readError || s.error, reading: s.reading } : { query: q, view: null, error: '', reading: false }
  }

  private slot(q: CapacityQuery): Slot {
    const key = capacityKey(q)
    let s = this.slots.get(key)
    if (!s) {
      s = { query: { ...q }, view: null, error: '', reading: false, watchers: 0, fetching: false, again: false, sent: 0, taken: 0, readError: '' }
      this.slots.set(key, s)
    }
    return s
  }

  /** A page shows q: it is fetched now, and again on its key's events, until `unwatch`. */
  watch(q: CapacityQuery): Promise<void> {
    const s = this.slot(q)
    s.watchers++
    return this.fetch(s)
  }

  /** A page no longer shows q; with no page left showing it, what is known of it goes. */
  unwatch(q: CapacityQuery) {
    const key = capacityKey(q)
    const s = this.slots.get(key)
    if (!s) return
    s.watchers = Math.max(0, s.watchers - 1)
    if (s.watchers === 0 && !s.fetching && !s.reading) this.slots.delete(key)
  }

  /** Fetches q's view: one GET at a time; one asked while it is out is made once it lands. */
  private async fetch(s: Slot): Promise<void> {
    if (!this.reads) return
    if (s.fetching) {
      s.again = true
      return
    }
    s.fetching = true
    s.again = false
    const n = ++s.sent
    try {
      const v = await this.reads.view(s.query)
      this.take(s, n, v, '')
    } catch (e) {
      this.take(s, n, null, (e as Error).message)
    } finally {
      s.fetching = false
      this.changed()
    }
    if (s.again && this.slots.get(capacityKey(s.query)) === s) await this.fetch(s)
  }

  private take(s: Slot, n: number, v: CapacityView | null, error: string) {
    if (n < s.taken) return
    s.taken = n
    if (v) s.view = v
    s.error = error
  }

  /**
   * Asks the server to read q's agent and account now (`check`: the admin's explicit check of a Claude Code version not checked):
   * nothing is sent while a read of q is out or the view's `nextReadAfter` is ahead of the clock. Answers whether a read was sent.
   */
  async read(q: CapacityQuery, check = false): Promise<boolean> {
    if (!this.reads) return false
    const s = this.slot(q)
    if (s.reading || (s.view && readWait(s.view, this.now()) !== null)) return false
    s.reading = true
    s.readError = ''
    this.changed()
    try {
      // The read's own answer is the view of its agent and account as a plain session's launch would get it; the question's own
      // (its model, yolo and place) is fetched after it, a GET that reads nothing.
      await this.reads.read(q, check)
    } catch (e) {
      s.readError = (e as Error).message
    } finally {
      s.reading = false
      this.changed()
    }
    await this.fetch(s)
    return true
  }

  /** A `capacity` event: every question shown whose agent it names, and whose account it names or is not yet known, is fetched again. */
  event(payload: unknown) {
    const e = asCapacityEvent(payload)
    if (!e) return
    for (const s of this.slots.values()) {
      if (s.watchers === 0 || s.query.agent !== e.agent) continue
      const account = s.query.account || s.view?.account.id
      if (!account || account === e.account) void this.fetch(s)
    }
  }

  /** The events stream came back: whatever was missed meanwhile, every question shown is fetched again. */
  snapshot() {
    for (const s of this.slots.values()) if (s.watchers > 0) void this.fetch(s)
  }

  /** Another workbench token: nothing known through the old one stays, and an answer on its way for it is dropped. */
  forget() {
    for (const s of this.slots.values()) {
      s.view = null
      s.error = ''
      s.readError = ''
      s.taken = s.sent + 1
      s.sent = s.taken
    }
    this.changed()
  }
}

let live: CapacityLive | undefined
/** The app's one store of capacity views; useAttention hands it every `capacity` event and every `snapshot`. */
export function capacityLive(): CapacityLive {
  return (live ??= new CapacityLive())
}

/** Capacity for a page: the store, its reads wired to the server, and a counter a computed follows. */
export function useCapacity() {
  const { request } = useApi()
  const admin = useWorkbenchToken()
  const version = useState<number>('capacityVersion', () => 0)
  const store = capacityLive()
  store.attach({
    view: (q) => request<CapacityView>('/api/capacity', { query: capacityParams(q) }),
    read: (q, check) => request<CapacityView>('/api/capacity/read', { method: 'POST', body: { agent: q.agent, ...(q.account ? { account: q.account } : {}), ...(check ? { check: true } : {}) } }),
  })
  store.changed = () => version.value++
  const token = useState<string | null>('capacityToken', () => null)
  if (token.value !== null && token.value !== admin.token.value) store.forget()
  token.value = admin.token.value

  /** What is known of q, followed by a computed. */
  function entry(q: CapacityQuery) {
    void version.value
    return store.entry(q)
  }
  return { store, entry, version }
}
