import type { BrakemanBrief, BrakemanConfig, BrakemanRequest, BrakemanState, RefResolution } from '../utils/brakeman'
import { closed, keptBrief, openedMark, requestNumber, stateRank, stopAction, unopened } from '../utils/brakeman'
import { dropKeptMake, readKeptMakes, reconcileActs, reconcileMade, sendKept, storeKeptMake, type KeptMake } from '../utils/brakemanCards'
import { mintCommandId } from '../utils/tasks'
import { ApiError } from './useApi'
import type { DispatchWatch } from './useDispatchLive'
import type { PlanSummary } from './useDispatch'

// Brakeman's records on the app's pages (docs/plans/brakeman.md, PR-6): the configuration, the requests (a page of them, then
// the `brakeman` events of GET /api/events folded in), and the person's writes (ask, cancel, make), each with an id the browser
// mints and a reply lost to the network asked for once more with the same id, which the server answers as the first (the rule of
// utils/tasks.ts). Which briefs this browser has opened is kept in its own storage: the sidebar counts the rest.

/** A `brakeman` event's data as the stream sent it, or null for what is not one: `{id, state}`, at most 200 bytes. */
export function asBrakemanEvent(v: unknown): { id: string; state: string } | null {
  if (v === null || typeof v !== 'object') return null
  const e = v as Record<string, unknown>
  return typeof e.id === 'string' && /^br-[1-9][0-9]{0,17}$/.test(e.id) && typeof e.state === 'string' ? { id: e.id, state: e.state } : null
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
  page: (after?: string) => Promise<{ requests: BrakemanRequest[]; next: string }>
  request: (id: string) => Promise<{ request: BrakemanRequest; brief: BrakemanBrief | null }>
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
  readonly requests = new Map<string, BrakemanRequest>()
  state: BrakemanState | null = null
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
  list(): BrakemanRequest[] {
    return [...this.requests.values()].sort((a, b) => requestNumber(b.id) - requestNumber(a.id))
  }

  /**
   * Takes a request as a reply gave it, unless the page has it further along already or the reply was sent before a reset (`at`); its
   * makes and edges are reconciled with the page's (reconcileMade), since replies to two cards' makes of one request can arrive in
   * either order, and so are its acts (reconcileActs), whose outcome may come after its brief, so that two reads of one briefed
   * request answered out of order never take a done act back to pending.
   */
  apply(r: BrakemanRequest, at = this.gen) {
    if (at !== this.gen) return
    const had = this.requests.get(r.id)
    if (had && stateRank(r.state) < stateRank(had.state)) return
    this.requests.set(r.id, had ? { ...r, ...reconcileMade(had, r), ...reconcileActs(had, r) } : r)
    this.changed()
  }

  /** A `brakeman` event: the request's state now, and a read of the rest. */
  event(e: { id: string; state: string }) {
    const had = this.requests.get(e.id)
    if (had && stateRank(e.state) >= stateRank(had.state) && had.state !== e.state) {
      this.requests.set(e.id, { ...had, state: e.state })
      this.changed()
    }
    void this.readRequest(e.id)
    void this.readState()
  }

  /** The stream started again: what changed meanwhile is read. */
  snapshot() {
    void this.readFirst()
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
      const { request } = await this.reads.request(id)
      if (at !== this.gen) return undefined
      this.apply(request)
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
      const { request } = await this.reads.request(id)
      this.apply(request, at)
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
          this.state = s
          this.available = true
          this.problem = ''
          if (s.current) this.apply(s.current)
          for (const q of s.queue) this.apply(q)
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

  private unavailable(e: unknown) {
    if (e instanceof ApiError && (e.status === 503 || e.status === 403)) {
      this.available = e.code === 'brakeman_recovering' || e.code === 'brakeman_not_owner' ? this.available : false
      this.problem = e.message
    }
  }
}

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
    page: (after) => request<{ requests: BrakemanRequest[]; next: string }>('/api/brakeman/requests', { query: after ? { after } : undefined }),
    request: (id) => request<{ request: BrakemanRequest; brief: BrakemanBrief | null }>(`/api/brakeman/requests/${encodeURIComponent(id)}`),
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

  /** The briefs not yet opened in this browser, newest first. The first read ever in a browser takes the briefs there are as opened. */
  const unopenedIds = computed(() => unopened(requests.value, new Set(opened.value ?? [])))
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
   * Saves the configuration read at `rev`: {config, warnings?}. A reply that arrives after a reset is the token before's (perhaps
   * another server's configuration): it is refused, and neither the store nor the form takes it.
   */
  async function saveConfig(config: BrakemanConfig) {
    const at = store.generation
    const out = await request<{ config: BrakemanConfig; warnings?: string[] }>('/api/brakeman/config', { method: 'PUT', body: { rev: config.rev, config } })
    if (store.generation !== at) throw new Error("The token changed before the save was answered: the configuration shown is this server's.")
    if (store.state) store.state = { ...store.state, config: out.config, warnings: out.warnings }
    version.value++
    void store.readState()
    return out
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

  /** A request whole, with its brief (null until one is kept; its items a list, none for a brief of its summary alone). */
  async function brief(id: string) {
    const at = store.generation
    const out = await reads.request(id)
    store.apply(out.request, at)
    return { request: out.request, brief: keptBrief(out.brief) }
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

  /** The store's generation, which a reset moves: what was read or set under one is not the next one's (Settings' form). */
  const generation = computed(() => {
    void version.value
    return store.generation
  })

  return { state, requests, available, problem, hasMore, generation, unopened: unopenedIds, markOpened, load, reset, more: () => store.readMore(), readRequest: (id: string) => store.readRequest(id), readNamed: (id: string) => store.readNamed(id), saveConfig, ask, cancel, stop, make, keptMake, forgetMake, finishMake, readTicketTitle, ticketTitle, ticketTitlesWatch, brief, refs, closed }
}
