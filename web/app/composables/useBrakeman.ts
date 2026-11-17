import type { BrakemanBrief, BrakemanConfig, BrakemanDigest, BrakemanFolders, BrakemanOpened, BrakemanProgress, BrakemanRequest, BrakemanRole, BrakemanRow, BrakemanSince, BrakemanState, RefResolution, RoleLogRow } from '../utils/brakeman'
import { closed, keptBrief, openedMark, requestNumber, stateRank, stopAction, unopened } from '../utils/brakeman'
import { dropKeptMake, readKeptMakes, reconcileActs, reconcileMade, sendKept, storeKeptMake, type KeptMake } from '../utils/brakemanCards'
import { newer } from '../utils/settingsSave'
import { mintCommandId } from '../utils/tasks'
import { onBeforeUnmount, onMounted } from 'vue'
import { ApiError } from './useApi'
import type { DispatchWatch } from './useDispatchLive'
import type { PlanSummary } from './useDispatch'

// Brakeman's records on the app's pages (docs/plans/brakeman.md, PR-6): the configuration, the requests (a page of them, then
// the `brakeman` events of GET /api/events folded in), and the person's writes (ask, cancel, make), each with an id the browser
// mints and a reply lost to the network asked for once more with the same id, which the server answers as the first (the rule of
// utils/tasks.ts). Which briefs this browser has opened is kept in its own storage: the sidebar counts the rest.

/**
 * A `brakeman` event's data as the stream sent it, or null for what is not one: `{id, state}`, at most 200 bytes; `id` a request's,
 * or `role` for a change of the background role (BK3).
 */
export function asBrakemanEvent(v: unknown): { id: string; state: string } | null {
  if (v === null || typeof v !== 'object') return null
  const e = v as Record<string, unknown>
  return typeof e.id === 'string' && (/^br-[1-9][0-9]{0,17}$/.test(e.id) || e.id === ROLE_EVENT) && typeof e.state === 'string' ? { id: e.id, state: e.state } : null
}

/** The `brakeman` event's id for a change of the background role. */
export const ROLE_EVENT = 'role'

/** The most bytes a `brakeman_progress` event's data takes (docs/protocol.md): one past it is not one. */
export const MAX_PROGRESS_EVENT = 768
/** The most bytes a `brakeman_opened` event's data takes: it is `{}`, naming no one. */
export const MAX_OPENED_EVENT = 32
/** The longest `what` of a read in progress (one of the guide's observe words). */
const MAX_WHAT = 16
/** A request's id as the server mints it. */
const REQUEST_ID = /^br-[1-9][0-9]{0,17}$/

/** The bytes a value takes as JSON, as the stream sent it (the server writes no spaces). */
function jsonBytes(v: unknown): number {
  return new TextEncoder().encode(JSON.stringify(v)).length
}

const count = (v: unknown): v is number => typeof v === 'number' && Number.isInteger(v) && v >= 0
const optionalText = (v: unknown) => v === undefined || typeof v === 'string'

/**
 * A `brakeman_progress` event's data, or the state's `progress`, as the page keeps it, or null for what is not one: a request's id,
 * its reads of its budget, its start and deadline when known, and the last read it was admitted to (`reading`, its ref left out when
 * the server's bound would not hold it). Past MAX_PROGRESS_EVENT bytes it is refused whole.
 */
export function asBrakemanProgress(v: unknown): BrakemanProgress | null {
  if (v === null || typeof v !== 'object' || Array.isArray(v)) return null
  const e = v as Record<string, unknown>
  if (typeof e.id !== 'string' || !REQUEST_ID.test(e.id) || !count(e.reads) || !count(e.readsPerTurn)) return null
  if (!optionalText(e.startedAt) || !optionalText(e.deadline)) return null
  let reading: BrakemanProgress['reading']
  if (e.reading !== undefined) {
    const r = e.reading as Record<string, unknown> | null
    if (r === null || typeof r !== 'object' || typeof r.what !== 'string' || r.what.length > MAX_WHAT || !optionalText(r.ref)) return null
    reading = { what: r.what, ...(typeof r.ref === 'string' ? { ref: r.ref } : {}) }
  }
  if (jsonBytes(v) > MAX_PROGRESS_EVENT) return null
  const out: BrakemanProgress = { id: e.id, reads: e.reads, readsPerTurn: e.readsPerTurn }
  if (typeof e.startedAt === 'string') out.startedAt = e.startedAt
  if (typeof e.deadline === 'string') out.deadline = e.deadline
  if (reading) out.reading = reading
  return out
}

/** A `brakeman_opened` event's data (`{}`: some brief was marked opened, by whom it never says), or null for what is not one. */
export function asBrakemanOpened(v: unknown): Record<string, never> | null {
  if (v === null || typeof v !== 'object' || Array.isArray(v) || jsonBytes(v) > MAX_OPENED_EVENT) return null
  return {}
}

/** The last `brakeman` event the store took, for a page that reads something of its own again on each (`seq` moves every time). */
export interface BrakemanEventSeen {
  /** The request's id, ROLE_EVENT for the background role, or "" when the stream started again (anything may have changed). */
  id: string
  state: string
  seq: number
}

/** How long the since read waits after one before the next (GET /api/brakeman/since, which the server keeps for as long). */
export const SINCE_EVERY_MS = 5_000

/** Whether an answer is an older server's to a query it does not take: every parameter but `after` and `limit` is refused so. */
function olderServer(e: unknown): boolean {
  return e instanceof ApiError && e.status === 400 && e.message.startsWith('this route takes no parameter')
}

/** What the list route is asked (useBrakeman().list): each filter optional, combined with AND by the server. */
export interface BrakemanListQuery {
  kind?: string[]
  has?: 'decision' | 'proposal' | 'draft' | 'brief'
  about?: string
  q?: string
  after?: string
  limit?: number
}

/** A query's parameters as the list route takes them, each given once and only when set. */
export function listParams(q: BrakemanListQuery): Record<string, string> {
  const out: Record<string, string> = {}
  if (q.after) out.after = q.after
  if (q.limit !== undefined) out.limit = String(q.limit)
  if (q.kind?.length) out.kind = [...new Set(q.kind)].join(',')
  if (q.has) out.has = q.has
  if (q.about) out.about = q.about
  if (q.q) out.q = q.q
  return out
}

/**
 * Sends a write once, and once more with the same body (so the same id) when the reply was lost to the network (status 0); a second
 * loss, and any answer of the server, is the caller's.
 */
export async function sendTwice<B, R>(send: (body: B) => Promise<R>, body: B): Promise<R> {
  for (let attempt = 1; ; attempt++) {
    try {
      return await send(body)
    } catch (e) {
      if ((e as { status?: number }).status === 0 && attempt === 1) continue
      throw e
    }
  }
}

/** What the live store reads from the server. */
export interface BrakemanReads {
  state: () => Promise<BrakemanState>
  page: (after?: string) => Promise<{ requests: BrakemanRow[]; next: string }>
  request: (id: string) => Promise<{ request: BrakemanRequest; brief: BrakemanBrief | null; digest?: BrakemanDigest }>
  /** GET /api/brakeman/since: null where the server has no such route (an older one's 404). */
  since?: () => Promise<BrakemanSince | null>
  /**
   * GET /api/brakeman/opened (no ids) or POST it (the ids marked opened): the caller's unread briefs as the server keeps them, or null
   * where it keeps none (an older server's 404, a read-only store's 503): the browser's own marks count then.
   */
  opened?: (ids?: string[]) => Promise<BrakemanOpened | null>
}

/** The most ids an opened read answers, and a mark sends at once (docs/protocol.md). */
export const MAX_UNREAD = 500
export const MAX_MARKS = 100

/** An opened read's answer as the page keeps it, or null for what is not one: at most MAX_UNREAD request ids, newest first. */
export function asOpenedReply(v: unknown): BrakemanOpened | null {
  if (v === null || typeof v !== 'object') return null
  const o = v as Record<string, unknown>
  if (!Array.isArray(o.unread) || o.unread.length > MAX_UNREAD || !o.unread.every((id) => typeof id === 'string' && REQUEST_ID.test(id))) return null
  return { unread: [...o.unread] as string[], begun: typeof o.begun === 'string' ? o.begun : '' }
}

/**
 * The requests the pages show, newest first, and Brakeman's state, kept up to date by the `brakeman` events: an event moves its
 * request's state at once (a request the page has not read yet is read), and the request and the state are read again for the rest
 * (its run, its session, its words). A reply that says a request is less far along than the page has it (a read sent before the
 * event that moved it) is dropped: a request only goes forward (queued, starting, running, then closed). A snapshot (the stream
 * started again) reads the first page and the state again, since events were missed meanwhile; once older pages were read, it reads
 * on from the first page until it meets the newest of the pages read before, so no request asked meanwhile is left between them.
 * A new token (reset) may be another server's: what the store holds is dropped, and so is every reply to a read sent before it.
 */
export class BrakemanLive {
  /** The requests, each with its brief's digest once a read gave one (kept across reads that give none: `apply`). */
  readonly requests = new Map<string, BrakemanRow>()
  state: BrakemanState | null = null
  /**
   * The progress of each turn under way, by request: from the state's `progress` and the `brakeman_progress` events, the one with
   * the most reads taken (reads only grow in a turn), dropped once its request closes.
   */
  progress: Record<string, BrakemanProgress> = {}
  /** The last `brakeman` event taken (or the stream's start again, id ""), for pages that read something of their own on each. */
  lastEvent: BrakemanEventSeen = { id: '', state: '', seq: 0 }
  /** What changed since the last briefing (GET /api/brakeman/since), while a page follows it; null before a read or on an older server. */
  sinceNow: BrakemanSince | null = null
  /** How many pages follow the since read: it is read again on events only while one does. */
  private sinceFollowers = 0
  private sinceBusy = false
  private sinceAgain = false
  /** When the last since read was sent (the clock's ms), 0 before one. */
  private sinceAt = 0
  private sinceTimer: ReturnType<typeof setTimeout> | undefined
  /** The clock the since read is spaced by (a test's own). */
  now: () => number = () => Date.now()
  /** Whether the server has Brakeman (a data directory, and no switchyard); undefined until it has answered once. */
  available: boolean | undefined = undefined
  /** Why Brakeman cannot be read here, in the server's words. */
  problem = ''
  /** The id to read the next page after, "" for none. */
  next = ''
  /** Called after every change, for the pages' one counter. */
  changed: () => void
  private reads: BrakemanReads | undefined
  private stateBusy = false
  private stateAgain = false
  /** Whether pages after the first were read: `next` is then theirs. */
  private paged = false
  /** The number of the newest request the pages read hold (the first page's newest as it was read), 0 before one. */
  private newestPaged = 0
  /** How many times the store was reset: a reply to a read sent before the last reset is another token's, and is dropped. */
  private gen = 0
  /** How many times the background role changed (its `brakeman` events): the role's log is read again on each. */
  roleSeq = 0

  constructor(changed: () => void = () => {}) {
    this.changed = changed
  }

  /** Gives the store its reads (the composable's, once the app has one). */
  attach(reads: BrakemanReads) {
    this.reads ??= reads
  }

  /** The store's generation now, for a write's reply to be taken only while it is the same (apply's `at`). */
  get generation(): number {
    return this.gen
  }

  /**
   * The token changed: the requests, the state and the pages read are dropped, since the server may be another one whose br-1 is
   * not this one's, and the replies to reads sent before are dropped as they come.
   */
  reset() {
    this.gen++
    this.requests.clear()
    this.state = null
    this.progress = {}
    this.openedMode = 'unknown'
    this.unread = []
    this.openedBusy = false
    this.openedAgain = false
    this.marksWaiting.clear()
    this.sinceNow = null
    this.sinceAt = 0
    this.sinceBusy = false
    this.sinceAgain = false
    if (this.sinceTimer !== undefined) clearTimeout(this.sinceTimer)
    this.sinceTimer = undefined
    this.available = undefined
    this.problem = ''
    this.next = ''
    this.paged = false
    this.newestPaged = 0
    this.stateBusy = false
    this.stateAgain = false
    this.changed()
  }

  /** The requests, newest first. */
  list(): BrakemanRow[] {
    return [...this.requests.values()].sort((a, b) => requestNumber(b.id) - requestNumber(a.id))
  }

  /**
   * Takes a request as a reply gave it, unless the page has it further along already or the reply was sent before a reset (`at`); its
   * makes and edges are reconciled with the page's (reconcileMade), since replies to two cards' makes of one request can arrive in
   * either order, and so are its acts (reconcileActs), whose outcome may come after its brief, so that two reads of one briefed
   * request answered out of order never take a done act back to pending. A row's digest is kept when a later read gives none (the
   * request's own read, the state's), and a list's `match` is that query's, never the store's. A request closed drops its progress.
   */
  apply(r: BrakemanRow, at = this.gen) {
    if (at !== this.gen) return
    const had = this.requests.get(r.id)
    if (had && stateRank(r.state) < stateRank(had.state)) return
    const { match: _match, ...row } = r
    const next: BrakemanRow = had ? { ...row, ...reconcileMade(had, row), ...reconcileActs(had, row) } : row
    if (!next.digest && had?.digest) next.digest = had.digest
    this.requests.set(r.id, next)
    if (closed(next.state)) this.dropProgress(r.id)
    this.changed()
  }

  /** Takes a turn's progress, unless the page holds one of more reads for it already, or its request is closed. */
  takeProgress(p: BrakemanProgress) {
    const had = this.progress[p.id]
    if (had && p.reads < had.reads) return
    const r = this.requests.get(p.id)
    if (r && closed(r.state)) return
    this.progress = { ...this.progress, [p.id]: p }
    this.changed()
  }

  private dropProgress(id: string) {
    if (!(id in this.progress)) return
    const rest = { ...this.progress }
    delete rest[id]
    this.progress = rest
  }

  /** A `brakeman_progress` event: the turn's reads as they stand now. */
  progressEvent(p: BrakemanProgress) {
    this.takeProgress(p)
  }

  /** How many `brakeman_opened` events came: the server's unread count is read again on each. */
  openedSeq = 0
  /**
   * Whose count of unread briefs the page shows: the server's, kept per credential (`server`, its ids in `unread`), the browser's own
   * marks (`local`: an older server, a read-only store), or not known yet.
   */
  openedMode: 'unknown' | 'server' | 'local' = 'unknown'
  /** The server's unread briefs, newest first, while openedMode is `server`. */
  unread: string[] = []
  private openedBusy = false
  private openedAgain = false
  /** Ids marked opened while the server's count was not known yet: sent once it answers. */
  private marksWaiting = new Set<string>()

  /** A `brakeman_opened` event: a brief was marked opened under some credential, which the count read again says. */
  openedEvent() {
    this.openedSeq++
    this.changed()
    void this.readOpened()
  }

  /** Takes an opened read's or mark's answer (null: the server keeps no marks here, and the browser's count stands). */
  private takeOpened(out: BrakemanOpened | null) {
    if (out === null) {
      this.openedMode = 'local'
      this.unread = []
      this.marksWaiting.clear()
    } else {
      this.openedMode = 'server'
      this.unread = out.unread.filter((id) => !this.marksWaiting.has(id))
    }
    this.changed()
  }

  /** Reads the server's unread briefs (one read at a time: what is asked meanwhile is one read after); a failure leaves the count shown. */
  async readOpened(): Promise<void> {
    const read = this.reads?.opened
    if (!read || this.openedMode === 'local') return
    if (this.openedBusy) {
      this.openedAgain = true
      return
    }
    this.openedBusy = true
    const at = this.gen
    try {
      do {
        this.openedAgain = false
        try {
          const out = await read()
          if (at !== this.gen) return
          this.takeOpened(out)
          if (out && this.marksWaiting.size) {
            const ids = [...this.marksWaiting]
            this.marksWaiting.clear()
            await this.sendMarks(ids, at)
          }
        } catch {
          /* the count shown stands; the next event reads again */
        }
      } while (this.openedAgain && at === this.gen && this.openedMode !== 'local')
    } finally {
      if (at === this.gen) this.openedBusy = false
    }
  }

  /**
   * Marks briefs opened with the server, which keeps them for this credential: they leave the count at once, and the answer says what
   * stays unread. While the server's count is not known yet they wait for its first answer; where it keeps none, it is not asked.
   * Whether it took them or not, the browser's own marks are the caller's.
   */
  markOpened(ids: string[]) {
    if (!this.reads?.opened || this.openedMode === 'local' || !ids.length) return
    if (this.openedMode === 'unknown') {
      for (const id of ids) this.marksWaiting.add(id)
      this.changed()
      return
    }
    if (ids.every((id) => !this.unread.includes(id))) return
    this.unread = this.unread.filter((id) => !ids.includes(id))
    this.changed()
    void this.sendMarks(ids, this.gen)
  }

  private async sendMarks(ids: string[], at: number) {
    const send = this.reads?.opened
    if (!send) return
    for (let i = 0; i < ids.length; i += MAX_MARKS) {
      try {
        const out = await send(ids.slice(i, i + MAX_MARKS))
        if (at !== this.gen) return
        this.takeOpened(out)
      } catch {
        // Not taken (the network, a refusal): the server's count is read again, so the page shows what it holds.
        if (at === this.gen) void this.readOpened()
        return
      }
    }
  }

  private seen(id: string, state: string) {
    this.lastEvent = { id, state, seq: this.lastEvent.seq + 1 }
  }

  /** A `brakeman` event: the request's state now, and a read of the rest; for the role, the state read again. */
  event(e: { id: string; state: string }) {
    this.seen(e.id, e.state)
    this.nudgeSince()
    if (e.id === ROLE_EVENT) {
      this.roleSeq++
      if (this.state?.role && this.state.role.state !== e.state) this.state = { ...this.state, role: { ...this.state.role, state: e.state } }
      this.changed()
      void this.readState()
      return
    }
    const had = this.requests.get(e.id)
    if (had && stateRank(e.state) >= stateRank(had.state) && had.state !== e.state) {
      this.requests.set(e.id, { ...had, state: e.state })
      if (closed(e.state)) this.dropProgress(e.id)
      this.changed()
    } else if (closed(e.state) && e.id in this.progress) {
      this.dropProgress(e.id)
      this.changed()
    }
    void this.readRequest(e.id)
    void this.readState()
    if (e.state === 'briefed') void this.readOpened()
  }

  /** The stream started again: what changed meanwhile is read, the role's too (a page showing its log reads it again). */
  snapshot() {
    this.roleSeq++
    this.seen('', 'snapshot')
    this.nudgeSince()
    this.changed()
    void this.readFirst()
    void this.readOpened()
  }

  /** Reads the first page of requests and the state. */
  async readFirst(): Promise<void> {
    if (!this.reads) return
    const at = this.gen
    try {
      const page = await this.reads.page()
      if (at !== this.gen) return
      for (const r of page.requests) this.apply(r)
      if (this.paged) await this.fillGap(page.next, at)
      else this.next = page.next
      if (at !== this.gen) return
      this.newestPaged = Math.max(this.newestPaged, ...page.requests.map((r) => requestNumber(r.id)))
      this.available = true
      this.problem = ''
    } catch (e) {
      if (at !== this.gen) return
      this.unavailable(e)
      this.changed()
      return
    }
    if (this.openedMode === 'unknown') void this.readOpened()
    await this.readState()
  }

  /**
   * Reads on after the first page (`after`) until a page reaches the newest request the pages read before held: the requests asked
   * while the stream was down, more than a page of them, are then all held, and `next` stays the pages' own. A page that fails leaves
   * `next` at the first one not read, so the next "older requests" reads on from the gap (through what the page holds already).
   */
  private async fillGap(after: string, at: number): Promise<void> {
    let cursor = after
    while (cursor && requestNumber(cursor) > this.newestPaged + 1) {
      let page: { requests: BrakemanRequest[]; next: string }
      try {
        page = await this.reads!.page(cursor)
      } catch {
        if (at !== this.gen) return
        this.next = cursor
        this.paged = false
        return
      }
      if (at !== this.gen) return
      for (const r of page.requests) this.apply(r)
      if (page.next === cursor || page.requests.some((r) => requestNumber(r.id) <= this.newestPaged)) return
      cursor = page.next
    }
    // Nothing older is left to read past the gap: what the pages read before held is all the server keeps.
    if (!cursor) this.next = ''
  }

  /** Reads the page after the oldest one read. */
  async readMore(): Promise<void> {
    if (!this.reads || !this.next) return
    const at = this.gen
    const page = await this.reads.page(this.next)
    if (at !== this.gen) return
    for (const r of page.requests) this.apply(r)
    this.next = page.next
    this.paged = true
    this.changed()
  }

  /** Reads one request again; a request whose read fails stays as the page has it. */
  async readRequest(id: string): Promise<BrakemanRequest | undefined> {
    if (!this.reads) return undefined
    const at = this.gen
    try {
      const { request, digest } = await this.reads.request(id)
      if (at !== this.gen) return undefined
      this.apply(digest ? { ...request, digest } : request)
      return request
    } catch {
      return undefined
    }
  }

  /**
   * Reads a request a link names (`?request=br-n`, a `brief:` ref) when the page does not have it: one older than the pages read is
   * read by itself, and shows among the rest. "" when the page has it, or the words for why it cannot be shown: the server keeps the
   * newest 500 closed requests, so an older one is pruned (410) or gone (404).
   */
  async readNamed(id: string): Promise<string> {
    if (!this.reads || this.requests.has(id)) return ''
    const at = this.gen
    try {
      const { request, digest } = await this.reads.request(id)
      this.apply(digest ? { ...request, digest } : request, at)
      return ''
    } catch (e) {
      const status = (e as { status?: number }).status
      if (status === 404) return `${id} is not kept here: the server keeps the newest 500 requests that are done.`
      if (status === 410) return `${id} was pruned: the server keeps the newest 500 requests that are done.`
      return `${id} could not be read: ${(e as Error).message}`
    }
  }

  /** Reads the state; reads asked while one is out are one read after it. */
  async readState(): Promise<void> {
    if (!this.reads) return
    if (this.stateBusy) {
      this.stateAgain = true
      return
    }
    this.stateBusy = true
    const at = this.gen
    try {
      do {
        this.stateAgain = false
        try {
          const s = await this.reads.state()
          if (at !== this.gen) return
          this.take(s)
        } catch (e) {
          if (at !== this.gen) return
          this.unavailable(e)
        }
        this.changed()
      } while (this.stateAgain)
    } finally {
      // A read sent before a reset leaves the reads after it theirs.
      if (at === this.gen) this.stateBusy = false
    }
  }

  /**
   * Takes a state read. Its configuration is taken by the page's one rule (utils/settingsSave.ts, `newer`): never over one whose
   * revision is newer, whatever order the replies came in, so a read that left before a save and answers after it never takes the
   * store back to what the save replaced. The rest of the state is the read's.
   */
  private take(s: BrakemanState) {
    const held = this.state
    this.state = held && newer(held.config, s.config) ? { ...s, config: held.config, warnings: held.warnings } : s
    this.available = true
    this.problem = ''
    if (s.current) this.apply(s.current)
    for (const q of s.queue) this.apply(q)
    const p = s.progress && asBrakemanProgress(s.progress)
    if (p) this.takeProgress(p)
  }

  /**
   * A page follows what changed since the last briefing: the first follower reads it at once (unless a read is fresh), and while any
   * follows it is read again on the events that may change it (nudgeSince). Returns the unfollow.
   */
  followSince(): () => void {
    this.sinceFollowers++
    if (this.sinceFollowers === 1) this.nudgeSince()
    let done = false
    return () => {
      if (done) return
      done = true
      this.sinceFollowers--
      if (this.sinceFollowers === 0 && this.sinceTimer !== undefined) {
        clearTimeout(this.sinceTimer)
        this.sinceTimer = undefined
      }
    }
  }

  /**
   * Something that may change the since read happened (a run's, Dispatch's or Brakeman's event, the stream's start again): while a page
   * follows it, it is read again, at most once every SINCE_EVERY_MS and one read at a time; what is asked meanwhile is one read after.
   */
  nudgeSince() {
    if (this.sinceFollowers === 0 || !this.reads?.since) return
    if (this.sinceBusy) {
      this.sinceAgain = true
      return
    }
    if (this.sinceTimer !== undefined) return
    const wait = this.sinceAt ? this.sinceAt + SINCE_EVERY_MS - this.now() : 0
    if (wait > 0) {
      this.sinceTimer = setTimeout(() => {
        this.sinceTimer = undefined
        this.nudgeSince()
      }, wait)
      return
    }
    void this.readSince()
  }

  private async readSince() {
    const read = this.reads?.since
    if (!read) return
    this.sinceBusy = true
    this.sinceAgain = false
    this.sinceAt = this.now()
    const at = this.gen
    try {
      const out = await read()
      if (at !== this.gen) return
      this.sinceNow = out
      this.changed()
    } catch {
      /* the line keeps what it showed; the next event reads again */
    } finally {
      if (at === this.gen) {
        this.sinceBusy = false
        if (this.sinceAgain) this.nudgeSince()
      }
    }
  }

  /** Takes a state read made on its own (the composable's readConfig, whose caller checked the generation), as readState does. */
  takeState(s: BrakemanState) {
    this.take(s)
    this.changed()
  }

  /** Takes a save's answer (saveConfig, whose caller checked the generation): unless the configuration held is newer (`newer`). */
  takeConfig(out: { config: BrakemanConfig; warnings?: string[]; folders?: BrakemanFolders }) {
    if (this.state && !newer(this.state.config, out.config)) this.state = { ...this.state, config: out.config, warnings: out.warnings, folders: out.folders }
  }

  private unavailable(e: unknown) {
    if (e instanceof ApiError && (e.status === 503 || e.status === 403)) {
      this.available = e.code === 'brakeman_recovering' || e.code === 'brakeman_not_owner' ? this.available : false
      this.problem = e.message
    }
  }
}

/** The words of a configuration read answered, or asked, after the token changed. */
export const TOKEN_MOVED_READ = "The token changed before Brakeman's settings were read: the form shown is this server's."

let live: BrakemanLive | undefined
/** The app's one live store of Brakeman's requests; useAttention hands it every `brakeman` event and every `snapshot`. */
export function brakemanLive(): BrakemanLive {
  return (live ??= new BrakemanLive())
}

/**
 * Where a browser keeps a brief opened: a key of its own per brief, `<prefix><openedMark>`. Two tabs opening two briefs at once each
 * write their own key, so neither can write over the other's (one list read and written again could: both read it, then each wrote
 * its own over the other's).
 */
const OPENED_PREFIX = 'conductor.brakeman.opened/'
/** Set once the first read in a browser took the briefs there were as opened: from then on a brief without its key is new. */
const OPENED_BEGUN = 'conductor.brakeman.opened-begun'
/** The one list of ids kept before marks named their request's time: another server's br-3 matched it, so it is dropped. */
const OPENED_LIST = 'conductor.brakeman.opened'
/** How many opened marks a browser keeps: the newest requests kept are 500, so an older one is never counted again. */
const OPENED_MAX = 500

/** A mark's request number, then its time, newest first. */
function newestFirst(marks: string[]): string[] {
  const parts = (m: string) => {
    const at = m.indexOf('@')
    return at < 0 ? ([requestNumber(m), ''] as const) : ([requestNumber(m.slice(0, at)), m.slice(at + 1)] as const)
  }
  return marks.sort((a, b) => {
    const [na, ta] = parts(a)
    const [nb, tb] = parts(b)
    return nb - na || (tb < ta ? -1 : tb > ta ? 1 : 0)
  })
}

/** The marks of the briefs this browser opened (openedMark), newest first, or null when its first read has not begun them yet. */
function readOpened(): string[] | null {
  try {
    const marks: string[] = []
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i)
      if (k?.startsWith(OPENED_PREFIX)) marks.push(k.slice(OPENED_PREFIX.length))
    }
    return marks.length || localStorage.getItem(OPENED_BEGUN) !== null ? newestFirst(marks) : null
  } catch {
    return null
  }
}

/**
 * Keeps `marks` as opened, each under its own key, and returns all the browser keeps (another tab's too), newest first, past the
 * newest OPENED_MAX dropped. Nothing is read before it is written, so a tab never writes over what another opened.
 */
export function keepOpened(marks: Iterable<string>): string[] {
  const mine = [...new Set(marks)]
  try {
    for (const m of mine) localStorage.setItem(OPENED_PREFIX + m, '1')
    localStorage.setItem(OPENED_BEGUN, '1')
    localStorage.removeItem(OPENED_LIST)
    const all = readOpened() ?? []
    for (const m of all.slice(OPENED_MAX)) localStorage.removeItem(OPENED_PREFIX + m)
    return all.slice(0, OPENED_MAX)
  } catch {
    /* storage refused: the count is this visit's */
    return newestFirst(mine)
  }
}

/** Whether this page follows the opened ids other tabs write (one listener for the app's one list). */
let followingOpened = false

/** The request a make is of: its id, and when the server wrote it, which tells one server's br-1 from another's. */
export type MakeOf = Pick<BrakemanRequest, 'id' | 'createdAt'>

/**
 * Where a make is kept: `<request>/<item>@<createdAt>`. A new token may be another server's, whose br-1 is another request written at
 * another time: it never finds this one's make, and the server it was asked of (the same request, under any token) still does.
 */
export function keptKey(r: MakeOf, item: number): string {
  return `${r.id}/${item}@${r.createdAt}`
}

/** The makes this browser asked that did not finish, by keptKey: every tab's, each kept under its own entry. */
function readKept(): Record<string, KeptMake> {
  try {
    return readKeptMakes(localStorage)
  } catch {
    return {}
  }
}

/** Keeps or forgets one make in the browser's storage, its entry alone (a refused storage keeps it for this visit). */
function writeKept(key: string, k: KeptMake | undefined) {
  try {
    if (k) storeKeptMake(localStorage, key, k)
    else dropKeptMake(localStorage, key)
  } catch {
    /* storage refused: the make is kept for this visit */
  }
}

/** Brakeman for a page: its state and requests, live, and the person's writes. */
/** How many title reads each `<generation>:<key>` has asked: a read's answer is taken only while it is the latest (readTicketTitle). */
const titleReads = new Map<string, number>()

export function useBrakeman() {
  const { request } = useApi()
  const version = useState<number>('brakemanVersion', () => 0)
  const opened = useState<string[] | null>('brakemanOpened', () => (import.meta.client ? readOpened() : null))
  // A brief opened in another tab is opened here too: the event's key is the brief's own mark. A tab whose first read has not run
  // yet takes all the browser keeps, since the other tab's first read began them.
  if (import.meta.client && !followingOpened) {
    followingOpened = true
    window.addEventListener('storage', (e) => {
      if (!e.key?.startsWith(OPENED_PREFIX) || e.newValue === null) return
      const mark = e.key.slice(OPENED_PREFIX.length)
      if (opened.value === null) opened.value = readOpened()
      else if (!opened.value.includes(mark)) opened.value = [...opened.value, mark]
    })
  }
  /**
   * The makes this browser asked that have no answer yet, or whose answer left them to be asked again (a reply lost twice, the backend
   * failing after the claim, a token refused before the route ran): each with its id and the body it sent, the one ask the route
   * finishes it on (the same id with other content is op_conflict, and another id make_pending). Kept in the browser's storage before
   * the make is sent, so a page read again while it is out can still finish it. Each is the request's it was asked of (keptKey), so a
   * new token, which may be another server's, finds only its own.
   */
  const kept = useState<Record<string, KeptMake>>('brakemanKeptMakes', () => (import.meta.client ? readKept() : {}))
  /**
   * The title of each ticket a made draft became, as Dispatch holds it (GET /api/dispatch/tickets/{key}): the person's edit, which the
   * brief's own title is not, so another card's Blocks line names the ticket as made (draftName), in this tab or another, before a
   * reload or after. By the token's generation and the key, since a new token's server has its own BK-3: its title once read (absent
   * while the first read is out) and the ticket's id, which Dispatch's events name it by (ticketTitlesWatch).
   */
  const ticketTitles = useState<Record<string, { title?: string; id?: string }>>('brakemanTicketTitles', () => ({}))
  const store = brakemanLive()
  const reads: BrakemanReads = {
    state: () => request<BrakemanState>('/api/brakeman'),
    page: (after) => request<{ requests: BrakemanRow[]; next: string }>('/api/brakeman/requests', { query: after ? { after } : undefined }),
    request: (id) => request<{ request: BrakemanRequest; brief: BrakemanBrief | null; digest?: BrakemanDigest }>(`/api/brakeman/requests/${encodeURIComponent(id)}`),
    since: () => since(),
    opened: async (ids) => {
      try {
        const out = await request<unknown>('/api/brakeman/opened', ids ? { method: 'POST', body: { ids } } : {})
        const got = asOpenedReply(out)
        if (!got) throw new Error('The unread briefs could not be read.')
        return got
      } catch (e) {
        // An older server has no such route; a read-only store keeps no marks: the browser's own count stands.
        if (e instanceof ApiError && (e.status === 404 || (e.status === 503 && e.code === 'brakeman_not_owner'))) return null
        throw e
      }
    },
  }
  store.attach(reads)
  // The store tells the pages it changed through one counter.
  store.changed = () => version.value++

  const state = computed(() => {
    void version.value
    return store.state
  })
  const requests = computed(() => {
    void version.value
    return store.list()
  })
  const available = computed(() => {
    void version.value
    return store.available
  })
  const problem = computed(() => {
    void version.value
    return store.problem
  })
  const hasMore = computed(() => {
    void version.value
    return store.next !== ''
  })

  /**
   * The briefs not yet opened, newest first: the server's count for this credential (the same on every browser that holds it) while
   * the server keeps one; else this browser's, whose first read ever takes the briefs there are as opened.
   */
  const unopenedIds = computed(() => {
    void version.value
    if (store.openedMode === 'server') return store.unread
    return unopened(requests.value, new Set(opened.value ?? []))
  })
  watch(
    requests,
    (list) => {
      if (opened.value !== null || store.available !== true) return
      // Another tab may have taken the first read meanwhile: its list is this one's.
      opened.value = readOpened() ?? keepOpened(list.filter((r) => r.hasBrief).map(openedMark))
    },
    { immediate: true },
  )

  /**
   * Marks a brief opened in this browser: the request as the card drew it, whose time tells it from another server's request of the
   * same id (a new token's br-3 is not this one).
   */
  function markOpened(r: Pick<BrakemanRequest, 'id' | 'createdAt'>) {
    store.markOpened([r.id])
    const mark = openedMark(r)
    if (opened.value?.includes(mark)) return
    opened.value = [...new Set([...(opened.value ?? []), ...keepOpened([mark])])]
  }

  /** Reads the state and the first page; pages call it as they open, the sidebar once. */
  function load() {
    return store.readFirst()
  }

  /**
   * A write's send, asked again (sendTwice) only with the token it was first sent with: useApi reads the token on every call, and
   * after a reset the server may be another one, whose br-3 is not this one's. The retry is refused instead (`again`), as a reply lost
   * twice is.
   */
  function sameToken<B, R>(at: number, again: string, send: (b: B) => Promise<R>): (b: B) => Promise<R> {
    return (b) => (store.generation === at ? send(b) : Promise.reject(new ApiError(0, 'network', again)))
  }

  /**
   * Saves the configuration read at `rev`: {config, warnings?, folders?}. A reply that arrives after a reset is the token before's (perhaps
   * another server's configuration): it is refused, and neither the store nor the form takes it.
   */
  async function saveConfig(config: BrakemanConfig) {
    const at = store.generation
    const out = await request<{ config: BrakemanConfig; warnings?: string[]; folders?: BrakemanFolders }>('/api/brakeman/config', { method: 'PUT', body: { rev: config.rev, config } })
    if (store.generation !== at) throw new Error("The token changed before the save was answered: the configuration shown is this server's.")
    store.takeConfig(out)
    version.value++
    void store.readState()
    return out
  }

  /**
   * The configuration saved now, read fresh for a choice made under the store's generation `at` (Settings' stale refusal): a read of
   * its own, never folded into one already out (which may have been sent before the save it answers) nor swallowed when it fails,
   * and taken by the store unless the configuration it holds is newer, which is then the one answered (`newer`). Refused when
   * the token moved before it was asked or before it was answered: the reply is the form's that asked, and a new token's form is
   * another server's (#707's review).
   */
  async function readConfig(at: number): Promise<BrakemanConfig> {
    if (store.generation !== at) throw new Error(TOKEN_MOVED_READ)
    const s = await request<BrakemanState>('/api/brakeman')
    if (store.generation !== at) throw new Error(TOKEN_MOVED_READ)
    store.takeState(s)
    return store.state?.config ?? s.config
  }

  /** Asks Brakeman: the request kept (201) or the one this id asked already (200). */
  async function ask(body: { kind: string; subjects?: string[]; note?: string; since?: string; by?: string }) {
    const at = store.generation
    const send = sameToken(at, 'The token changed before the ask was sent again: see whether the server it was sent to has it.', (b: typeof body & { opId: string }) =>
      request<{ request: BrakemanRequest }>('/api/brakeman/requests', { method: 'POST', body: b }),
    )
    const out = await sendTwice(send, { opId: mintCommandId(), ...body })
    store.apply(out.request, at)
    void store.readState()
    return out.request
  }

  /** Cancels a request: a queued one leaves the queue, one under way is written cancelled and its run stopped after. */
  async function cancel(id: string) {
    const at = store.generation
    const send = sameToken(at, `The token changed before the cancel of ${id} was sent again: see whether the server it was sent to cancelled it.`, (b: { opId: string }) =>
      request<{ request: BrakemanRequest }>(`/api/brakeman/requests/${encodeURIComponent(id)}/cancel`, { method: 'POST', body: b }),
    )
    const out = await sendTwice(send, { opId: mintCommandId() })
    store.apply(out.request, at)
    void store.readState()
    return out.request
  }

  /**
   * Stops the request holding the slot (stopAction): an open one is cancelled; a closed one the desk still holds has its run stopped
   * (POST /api/runs/{run}/stop), which a cancel would not do. Null when there is nothing to stop.
   */
  async function stop(r: Pick<BrakemanRequest, 'id' | 'state' | 'runId'>) {
    switch (stopAction(r)) {
      case 'cancel':
        return cancel(r.id)
      case 'stop_run':
        await request(`/api/runs/${encodeURIComponent(r.runId!)}/stop`, { method: 'POST' })
        void store.readRequest(r.id)
        void store.readState()
        return null
    }
    return null
  }

  /**
   * Makes a brief's draft work (PR-7's forms send it): {request, made, ticket?, crew?}. Its id and body are kept for finishMake before
   * it is sent (sendKept); an answer that settles it forgets them, one that establishes nothing (unfinishedMake) leaves them kept.
   */
  async function make(r: MakeOf, item: number, body: { by?: string; edit?: unknown } = {}, opId = mintCommandId()) {
    const at = store.generation
    const key = keptKey(r, item)
    const keep = () => {
      const k: KeptMake = { opId, body, at: Date.now() }
      kept.value = { ...kept.value, [key]: k }
      writeKept(key, k)
    }
    // A reply lost is asked again only with the token it was sent with: after a reset the server may be another one, and the make
    // stays kept for the one it was asked of.
    const send = sameToken(at, 'The token changed before the make was asked again: it is kept for the server it was asked of.', (b: { opId: string; by?: string; edit?: unknown }) =>
      request<{ request: BrakemanRequest; made: unknown; ticket?: { key: string; url: string }; crew?: { id: string; url: string }; plan?: PlanSummary }>(`/api/brakeman/requests/${encodeURIComponent(r.id)}/items/${item}/make`, { method: 'POST', body: b }),
    )
    const out = await sendKept(keep, () => forget(key), () => sendTwice(send, { opId, ...body }))
    // Answered after a reset: the ticket or crew is the server's before, perhaps another one's, so the caller is given nothing of it
    // to open here (a crew card would open /crews/<its id> on the new server).
    if (store.generation !== at) throw new Error('The token changed before the make was answered: it was made on the server it was asked of, and shows there.')
    store.apply(out.request, at)
    return out
  }

  /**
   * Reads the title of the ticket `key` from Dispatch: a made draft's, which another card's Blocks line names. Asked first, once for the
   * token it is asked under: a read that fails leaves nothing, so the next ask reads again, and the line names the key alone meanwhile.
   * Asked `again` (a Dispatch event names the ticket, a reconnect's snapshot), it is read anew, the title shown kept until the answer
   * and kept too when the read fails, which then rejects so the live store asks it again. Only the latest read's answer is taken, so a
   * slow read sent before a rename never puts the title back.
   */
  function readTicketTitle(key: string, again = false): Promise<void> {
    const at = `${store.generation}:${key}`
    const held = at in ticketTitles.value
    if (held && !again) return Promise.resolve()
    if (!held) ticketTitles.value = { ...ticketTitles.value, [at]: {} }
    const n = (titleReads.get(at) ?? 0) + 1
    titleReads.set(at, n)
    const latest = () => titleReads.get(at) === n && at in ticketTitles.value
    const drop = () => {
      if (!latest() || ticketTitles.value[at]?.title !== undefined) return
      const rest = { ...ticketTitles.value }
      delete rest[at]
      ticketTitles.value = rest
    }
    return request<{ ticket: { id?: unknown; title?: unknown } }>(`/api/dispatch/tickets/${encodeURIComponent(key)}`).then(
      (r) => {
        const { id, title } = r.ticket ?? {}
        if (typeof title !== 'string') return drop()
        if (latest()) ticketTitles.value = { ...ticketTitles.value, [at]: { title, id: typeof id === 'string' ? id : undefined } }
      },
      (e: unknown) => {
        drop()
        if (again) throw e
      },
    )
  }

  /** The title of the ticket `key` as Dispatch holds it, once readTicketTitle has read it under this token. */
  function ticketTitle(key: string): string | undefined {
    return ticketTitles.value[`${store.generation}:${key}`]?.title
  }

  /**
   * The Dispatch watch (useDispatchLive) of the tickets `keys` names, whose titles a card's Blocks line shows: each read again at once
   * for an event of that ticket (by its id, once read), and after the window for an event of its project that names no ticket (the one
   * a commit of more than 64 sends) or while its id is not known yet; every one at once on a snapshot (a reconnect), which is how a
   * rename missed meanwhile shows.
   */
  function ticketTitlesWatch(keys: () => readonly string[]): DispatchWatch {
    const held = () => keys().map((k) => ({ key: k, project: k.slice(0, k.lastIndexOf('-')), id: ticketTitles.value[`${store.generation}:${k}`]?.id }))
    return {
      now: (e) => e.entity === 'ticket' && e.ticket !== '' && held().some((t) => t.id === e.ticket),
      later: (e) => e.entity === 'ticket' && e.project !== '' && held().some((t) => t.project === e.project && (e.ticket === '' || !t.id)),
      read: () => Promise.all(keys().map((k) => readTicketTitle(k, true))),
    }
  }

  /** The make of the item this browser asked that did not finish, or undefined. */
  function keptMake(r: MakeOf, item: number): KeptMake | undefined {
    return kept.value[keptKey(r, item)]
  }

  /** Forgets the item's unfinished make: it was answered, or another make holds the item. */
  function forgetMake(r: MakeOf, item: number) {
    forget(keptKey(r, item))
  }

  function forget(key: string) {
    if (!(key in kept.value)) return
    const rest = { ...kept.value }
    delete rest[key]
    kept.value = rest
    writeKept(key, undefined)
  }

  /** Asks the item's unfinished make again, as it was sent: the same id and body, which the route finishes and never makes twice. */
  function finishMake(r: MakeOf, item: number) {
    const k = keptMake(r, item)
    if (!k) throw new Error('This browser holds no make of it to finish.')
    return make(r, item, k.body, k.opId)
  }

  /**
   * A request whole, with its brief (null until one is kept; its items a list, none for a brief of its summary alone) and its digest
   * (none on an older server).
   */
  async function brief(id: string) {
    const at = store.generation
    const out = await reads.request(id)
    store.apply(out.digest ? { ...out.request, digest: out.digest } : out.request, at)
    return { request: out.request, brief: keptBrief(out.brief), digest: out.digest }
  }

  /**
   * A page of requests, newest first, filtered as `q` asks (GET /api/brakeman/requests): `filtered` says the server applied the
   * filters. An older server refuses every filter (400, "this route takes no parameter"): a query `about` a subject then answers no
   * rows (paging every request to match them here would read them all), and any other is read again without its filters, for the
   * page to filter what it holds. Every row is applied into the live store (its digest kept there), unless a reset came between.
   */
  async function list(q: BrakemanListQuery = {}): Promise<{ requests: BrakemanRow[]; next: string; filtered: boolean }> {
    const at = store.generation
    const params = listParams(q)
    const filters = q.kind?.length || q.has || q.about || q.q
    let out: { requests: BrakemanRow[]; next: string }
    let filtered = true
    try {
      out = await request<{ requests: BrakemanRow[]; next: string }>('/api/brakeman/requests', { query: Object.keys(params).length ? params : undefined })
    } catch (e) {
      if (!filters || !olderServer(e)) throw e
      if (q.about) return { requests: [], next: '', filtered: false }
      filtered = false
      const plain = listParams({ after: q.after, limit: q.limit })
      out = await request<{ requests: BrakemanRow[]; next: string }>('/api/brakeman/requests', { query: Object.keys(plain).length ? plain : undefined })
    }
    if (store.generation !== at) throw new Error("The token changed before the list was answered: the requests shown are this server's.")
    const rows = out.requests ?? []
    for (const r of rows) store.apply(r, at)
    return { requests: rows, next: out.next ?? '', filtered }
  }

  /** What changed in Conductor since the last briefing, or since `at` (RFC 3339): null where the server has no such route (404). */
  async function since(at?: string): Promise<BrakemanSince | null> {
    try {
      return await request<BrakemanSince>('/api/brakeman/since', { query: at ? { since: at } : undefined })
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) return null
      throw e
    }
  }

  /**
   * Follows what changed since the last briefing for a component (its setup): read on mount, again on the run, Dispatch and Brakeman
   * events while mounted (BrakemanLive.nudgeSince), at most once every five seconds; `sinceNow` holds the answer.
   */
  function followSince() {
    let stop: (() => void) | undefined
    onMounted(() => (stop = store.followSince()))
    onBeforeUnmount(() => stop?.())
  }

  /** A brief's refs as resolved when it was kept, by ref. */
  async function refs(id: string): Promise<Map<string, RefResolution>> {
    const out = await request<{ refs: RefResolution[] }>(`/api/brakeman/requests/${encodeURIComponent(id)}/refs`)
    return new Map((out.refs ?? []).map((r) => [r.ref, r]))
  }

  /**
   * The token changed: what the store holds is dropped (the server may be another one), and so are replies to reads and writes sent
   * before. The makes kept stay, each its own request's (keptKey), and so do the briefs opened (openedMark): another server's requests
   * never find them.
   */
  function reset() {
    store.reset()
  }

  /** The background role whole, with its log newest first (GET /api/brakeman/role), at most `limit` rows. */
  async function role(limit = 200) {
    return request<{ role: BrakemanRole; log: RoleLogRow[] }>('/api/brakeman/role', { query: { limit: String(limit) } })
  }

  /** Pauses or resumes the background role: the role as it is after. */
  async function roleAction(action: 'pause' | 'resume') {
    const out = await request<{ role: BrakemanRole }>('/api/brakeman/role', { method: 'POST', body: { action } })
    void store.readState()
    return out.role
  }

  /** Keeps a brief's fact or interpretation as one of the role's notes. */
  async function keepNote(requestId: string, item: number) {
    return (await request<{ role: BrakemanRole }>('/api/brakeman/role/notes', { method: 'POST', body: { request: requestId, item } })).role
  }

  /** Clears the role's notes. */
  async function clearNotes() {
    return (await request<{ role: BrakemanRole }>('/api/brakeman/role/notes', { method: 'DELETE' })).role
  }

  /** How many times the role changed: a page that shows its log reads it again when this moves. */
  const roleSeq = computed(() => {
    void version.value
    return store.roleSeq
  })

  /** The store's generation, which a reset moves: what was read or set under one is not the next one's (Settings' form). */
  const generation = computed(() => {
    void version.value
    return store.generation
  })

  /** What changed since the last briefing, while a page follows it (followSince); null before its read and on an older server. */
  const sinceNow = computed(() => {
    void version.value
    return store.sinceNow
  })

  /** Each turn under way's progress, by request id (the state's `progress`, then the `brakeman_progress` events). */
  const progress = computed(() => {
    void version.value
    return store.progress
  })

  /** The last `brakeman` event the store took, `seq` moving on each (id "" when the stream started again): a page's own reads follow it. */
  const events = computed(() => {
    void version.value
    return store.lastEvent
  })

  return { list, since, sinceNow, followSince, progress, events, state, requests, available, problem, hasMore, generation, unopened: unopenedIds, markOpened, load, readConfig, reset, more: () => store.readMore(), readRequest: (id: string) => store.readRequest(id), readNamed: (id: string) => store.readNamed(id), saveConfig, ask, cancel, stop, make, keptMake, forgetMake, finishMake, readTicketTitle, ticketTitle, ticketTitlesWatch, brief, refs, closed, role, roleAction, keepNote, clearNotes, roleSeq }
}
