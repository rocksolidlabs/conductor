import { asCapacityEvent, capacityKey, capacityParams, readWait, realAfter, viewTurns, withDeadline, type After, type CapacityQuery, type CapacityView } from '../utils/capacity'

/** What the store asks of the server: a view (`GET /api/capacity`, which reads nothing) and a read (`POST /api/capacity/read`). */
export interface CapacityReads {
  view: (q: CapacityQuery, signal: AbortSignal) => Promise<CapacityView>
  read: (q: CapacityQuery, check: boolean, signal: AbortSignal) => Promise<CapacityView>
}

/** A view's deadline: the GET reads nothing, the server answers it from what it keeps, so this only bounds a hang. */
export const VIEW_DEADLINE_MS = 30_000
/**
 * A read's deadline: the server answers a read within capacity.Wait (2 s, internal/capacity/cache.go) and bounds the read itself by
 * the reader's own (90 s for Claude Code, claudeUsageTimeout in internal/agents/usage.go); this is above both, a bound on a hang.
 */
export const READ_DEADLINE_MS = 120_000

/** What is known of one question: the last view answered, the error of the last ask, and whether a read is out. */
export interface CapacityEntry {
  query: CapacityQuery
  view: CapacityView | null
  error: string
  /** A POST /api/capacity/read of this question is out. */
  reading: boolean
}

interface Slot extends CapacityEntry {
  /** How many pages show it: an event reads again only what is shown, and a slot no page shows is not kept. */
  watchers: number
  /** A GET of it is out (one at a time), and whether another is owed once it lands. */
  fetching: boolean
  again: boolean
  /** Why the last read was refused, in the server's words; "" when it was not. */
  readError: string
  /** The GET out and those owed after it, ended. */
  landing: Promise<void>
}

/** A step under way: the slot it asked for and the token's epoch it asked under. */
interface Step {
  slot: Slot
  epoch: number
}

/**
 * The capacity views the app shows (docs/plans/capacity.md, PR-5), one for the app: a view per question (an agent, an account, the
 * launch's model, yolo and place) that a page shows, fetched one at a time per question, and fetched again when the `capacity` event
 * names its key, the events stream comes back (`snapshot`) or the view's words change by the clock (`age`). A read is asked only
 * through `read`, which sends nothing while the view's `nextReadAfter` is ahead of the clock. Every step after each of its answers
 * asks `stands` whether its question is still shown under the token it asked with, and does nothing more when it is not; every
 * request has a deadline.
 */
export class CapacityLive {
  private slots = new Map<string, Slot>()
  private reads: CapacityReads | null = null
  /** Bumped by `forget`: a step begun under another token stands no more. */
  private epoch = 0
  /** Bumped on every change, for a page's computed to follow. */
  changed: () => void = () => {}

  constructor(
    private readonly now: () => number = Date.now,
    private readonly after: After = realAfter,
  ) {}

  attach(reads: CapacityReads) {
    this.reads = reads
  }

  /** What is known of q now (a copy a page may read; never the slot itself). */
  entry(q: CapacityQuery): CapacityEntry {
    const s = this.slots.get(capacityKey(q))
    return s ? { query: s.query, view: s.view, error: s.readError || s.error, reading: s.reading } : { query: q, view: null, error: '', reading: false }
  }

  /** Whether a step still stands: its token is the one held, and its question is still shown, by the slot it began with. */
  private stands(t: Step): boolean {
    return t.epoch === this.epoch && t.slot.watchers > 0 && this.slots.get(capacityKey(t.slot.query)) === t.slot
  }

  /** A page shows q: it is fetched now, and again on its key's events, until `unwatch`. */
  watch(q: CapacityQuery): Promise<void> {
    const key = capacityKey(q)
    let s = this.slots.get(key)
    if (!s) {
      s = { query: { ...q }, view: null, error: '', reading: false, watchers: 0, fetching: false, again: false, readError: '', landing: Promise.resolve() }
      this.slots.set(key, s)
    }
    s.watchers++
    return this.fetch(s)
  }

  /** A page no longer shows q; with no page left showing it, its slot goes, and what is on its way for it is dropped. */
  unwatch(q: CapacityQuery) {
    const key = capacityKey(q)
    const s = this.slots.get(key)
    if (!s) return
    s.watchers = Math.max(0, s.watchers - 1)
    if (s.watchers === 0) this.slots.delete(key)
  }

  /** Fetches s's view: one GET at a time; one asked while it is out is made once it lands, and answers when that one has. */
  private fetch(s: Slot): Promise<void> {
    const t = { slot: s, epoch: this.epoch }
    if (!this.reads || !this.stands(t)) return Promise.resolve()
    if (s.fetching) {
      s.again = true
      return s.landing
    }
    s.fetching = true
    s.again = false
    return (s.landing = this.get(t, this.reads))
  }

  /** One GET of t's view, then the one owed after it. */
  private async get(t: Step, reads: CapacityReads): Promise<void> {
    const s = t.slot
    let v: CapacityView | null = null
    let error = ''
    try {
      v = await withDeadline((signal) => reads.view(s.query, signal), VIEW_DEADLINE_MS, `The server did not answer within ${VIEW_DEADLINE_MS / 1000} s`, this.after)
    } catch (e) {
      error = (e as Error).message
    }
    if (!this.stands(t)) return
    if (v) s.view = v
    s.error = error
    s.fetching = false
    this.changed()
    if (s.again) await this.fetch(s)
  }

  /**
   * Asks the server to read q's agent and account now (`check`: the admin's explicit check of a Claude Code version not checked):
   * nothing is sent for a question no page shows, while a read of q is out, or while the view's `nextReadAfter` is ahead of the
   * clock. Answers whether a read was sent.
   */
  async read(q: CapacityQuery, check = false): Promise<boolean> {
    const s = this.slots.get(capacityKey(q))
    if (!this.reads || !s || s.watchers === 0 || s.reading || (s.view && readWait(s.view, this.now()) !== null)) return false
    const t = { slot: s, epoch: this.epoch }
    const reads = this.reads
    s.reading = true
    s.readError = ''
    this.changed()
    let error = ''
    try {
      // The read's own answer is the view of its agent and account as a plain session's launch would get it; the question's own
      // (its model, yolo and place) is fetched after it, a GET that reads nothing.
      await withDeadline((signal) => reads.read(q, check, signal), READ_DEADLINE_MS, `The server did not answer the read within ${READ_DEADLINE_MS / 1000} s`, this.after)
    } catch (e) {
      error = (e as Error).message
    }
    if (!this.stands(t)) return true
    s.reading = false
    s.readError = error
    this.changed()
    await this.fetch(s)
    return true
  }

  /** A `capacity` event: every question shown whose agent it names, and whose account it names or is not yet known, is fetched again. */
  event(payload: unknown) {
    const e = asCapacityEvent(payload)
    if (!e) return
    for (const s of this.slots.values()) {
      if (s.query.agent !== e.agent) continue
      const account = s.query.account || s.view?.account.id
      if (!account || account === e.account) void this.fetch(s)
    }
  }

  /** The events stream came back: whatever was missed meanwhile, every question shown is fetched again. */
  snapshot() {
    for (const s of this.slots.values()) void this.fetch(s)
  }

  /**
   * Every view shown whose words have changed by the clock since it was fetched (stale, a reset passed) is fetched again: a GET,
   * never a read, and none while one is out. The boundary is passed only once a GET answers with the changed words: one that failed,
   * or answered with them unchanged (the server's clock behind), leaves it to the next tick.
   */
  age() {
    const now = this.now()
    for (const s of this.slots.values()) {
      const at = s.view ? viewTurns(s.view) : null
      if (at === null || at > now || s.fetching) continue
      void this.fetch(s)
    }
  }

  /** Another workbench token: nothing known through the old one stays, and every step on its way for it stands no more. */
  forget() {
    this.epoch++
    for (const [key, s] of this.slots) {
      if (s.watchers === 0) this.slots.delete(key)
      Object.assign(s, { view: null, error: '', readError: '', reading: false, fetching: false, again: false })
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
    view: (q, signal) => request<CapacityView>('/api/capacity', { query: capacityParams(q), signal }),
    read: (q, check, signal) => request<CapacityView>('/api/capacity/read', { method: 'POST', body: { agent: q.agent, ...(q.account ? { account: q.account } : {}), ...(check ? { check: true } : {}) }, signal }),
  })
  store.changed = () => version.value++
  // The token is watched while a page shows capacity, by its count of changes (no copy of it is kept): another one empties the store,
  // so nothing asked with the old one is shown.
  const seen = useState<number | null>('capacityTokenChanges', () => null)
  const judge = () => {
    if (seen.value !== null && seen.value !== admin.changes.value) store.forget()
    seen.value = admin.changes.value
  }
  judge()
  watch(admin.changes, judge)

  /** What is known of q, followed by a computed. */
  function entry(q: CapacityQuery) {
    void version.value
    return store.entry(q)
  }
  return { store, entry, version }
}
