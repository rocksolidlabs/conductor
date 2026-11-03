import type { BrakemanBrief, BrakemanConfig, BrakemanRequest, BrakemanState, RefResolution } from '../utils/brakeman'
import { closed, requestNumber, stateRank, stopAction, unopened } from '../utils/brakeman'
import { unfinishedMake, type KeptMake } from '../utils/brakemanCards'
import { mintCommandId } from '../utils/tasks'
import { ApiError } from './useApi'

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
 * started again) reads the first page and the state again, since events were missed meanwhile.
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

  constructor(changed: () => void = () => {}) {
    this.changed = changed
  }

  /** Gives the store its reads (the composable's, once the app has one). */
  attach(reads: BrakemanReads) {
    this.reads ??= reads
  }

  /** The requests, newest first. */
  list(): BrakemanRequest[] {
    return [...this.requests.values()].sort((a, b) => requestNumber(b.id) - requestNumber(a.id))
  }

  /** Takes a request as a reply gave it, unless the page has it further along already. */
  apply(r: BrakemanRequest) {
    const had = this.requests.get(r.id)
    if (had && stateRank(r.state) < stateRank(had.state)) return
    this.requests.set(r.id, r)
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
    try {
      const page = await this.reads.page()
      for (const r of page.requests) this.apply(r)
      if (!this.paged) this.next = page.next
      this.available = true
      this.problem = ''
    } catch (e) {
      this.unavailable(e)
      this.changed()
      return
    }
    await this.readState()
  }

  /** Reads the page after the oldest one read. */
  async readMore(): Promise<void> {
    if (!this.reads || !this.next) return
    const page = await this.reads.page(this.next)
    for (const r of page.requests) this.apply(r)
    this.next = page.next
    this.paged = true
    this.changed()
  }

  /** Reads one request again; a request whose read fails stays as the page has it. */
  async readRequest(id: string): Promise<BrakemanRequest | undefined> {
    if (!this.reads) return undefined
    try {
      const { request } = await this.reads.request(id)
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
    try {
      const { request } = await this.reads.request(id)
      this.apply(request)
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
    try {
      do {
        this.stateAgain = false
        try {
          const s = await this.reads.state()
          this.state = s
          this.available = true
          this.problem = ''
          if (s.current) this.apply(s.current)
          for (const q of s.queue) this.apply(q)
        } catch (e) {
          this.unavailable(e)
        }
        this.changed()
      } while (this.stateAgain)
    } finally {
      this.stateBusy = false
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

const OPENED_KEY = 'conductor.brakeman.opened'
/** How many opened ids a browser keeps: the newest requests kept are 500, so an older one is never counted again. */
const OPENED_MAX = 500

/** The ids of the briefs this browser opened, or null when it has kept none yet. */
function readOpened(): string[] | null {
  try {
    const raw = localStorage.getItem(OPENED_KEY)
    const v = raw ? JSON.parse(raw) : null
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : null
  } catch {
    return null
  }
}

function writeOpened(ids: Iterable<string>) {
  try {
    localStorage.setItem(OPENED_KEY, JSON.stringify([...ids].sort((a, b) => requestNumber(b) - requestNumber(a)).slice(0, OPENED_MAX)))
  } catch {
    /* storage refused: the count is this visit's */
  }
}

const KEPT_KEY = 'conductor.brakeman.kept-makes'
/** How many unfinished makes a browser keeps: the newest, by when they were asked. */
const KEPT_MAX = 50

/** The makes this browser asked that did not finish, by `<request>/<item>`. */
function readKept(): Record<string, KeptMake> {
  try {
    const v = JSON.parse(localStorage.getItem(KEPT_KEY) ?? '{}') as Record<string, KeptMake>
    if (!v || typeof v !== 'object' || Array.isArray(v)) return {}
    return Object.fromEntries(Object.entries(v).filter(([, k]) => k && typeof k.opId === 'string' && k.body && typeof k.body === 'object'))
  } catch {
    return {}
  }
}

function writeKept(kept: Record<string, KeptMake>) {
  try {
    const newest = Object.entries(kept).sort(([, a], [, b]) => (b.at ?? 0) - (a.at ?? 0)).slice(0, KEPT_MAX)
    if (newest.length) localStorage.setItem(KEPT_KEY, JSON.stringify(Object.fromEntries(newest)))
    else localStorage.removeItem(KEPT_KEY)
  } catch {
    /* storage refused: the make is kept for this visit */
  }
}

/** Brakeman for a page: its state and requests, live, and the person's writes. */
export function useBrakeman() {
  const { request } = useApi()
  const version = useState<number>('brakemanVersion', () => 0)
  const opened = useState<string[] | null>('brakemanOpened', () => (import.meta.client ? readOpened() : null))
  /**
   * The makes this browser asked whose answer left them to be asked again (a reply lost twice, the backend failing after the claim):
   * each with its id and the body it sent, the one ask the route finishes it on (the same id with other content is op_conflict, and
   * another id make_pending). Kept in the browser's storage too, so a page read again can still finish it.
   */
  const kept = useState<Record<string, KeptMake>>('brakemanKeptMakes', () => (import.meta.client ? readKept() : {}))
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
      opened.value = list.filter((r) => r.hasBrief).map((r) => r.id)
      writeOpened(opened.value)
    },
    { immediate: true },
  )

  /** Marks a brief opened in this browser. */
  function markOpened(id: string) {
    const set = new Set(opened.value ?? [])
    if (set.has(id)) return
    set.add(id)
    opened.value = [...set]
    writeOpened(set)
  }

  /** Reads the state and the first page; pages call it as they open, the sidebar once. */
  function load() {
    return store.readFirst()
  }

  /** Saves the configuration read at `rev`: {config, warnings?}. */
  async function saveConfig(config: BrakemanConfig) {
    const out = await request<{ config: BrakemanConfig; warnings?: string[] }>('/api/brakeman/config', { method: 'PUT', body: { rev: config.rev, config } })
    if (store.state) store.state = { ...store.state, config: out.config, warnings: out.warnings }
    version.value++
    void store.readState()
    return out
  }

  /** Asks Brakeman: the request kept (201) or the one this id asked already (200). */
  async function ask(body: { kind: string; subjects?: string[]; note?: string; since?: string; by?: string }) {
    const out = await sendTwice((b) => request<{ request: BrakemanRequest }>('/api/brakeman/requests', { method: 'POST', body: b }), { opId: mintCommandId(), ...body })
    store.apply(out.request)
    void store.readState()
    return out.request
  }

  /** Cancels a request: a queued one leaves the queue, one under way is written cancelled and its run stopped after. */
  async function cancel(id: string) {
    const out = await sendTwice((b) => request<{ request: BrakemanRequest }>(`/api/brakeman/requests/${encodeURIComponent(id)}/cancel`, { method: 'POST', body: b }), { opId: mintCommandId() })
    store.apply(out.request)
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
   * Makes a brief's draft work (PR-7's forms send it): {request, made, ticket?, crew?}. An answer that leaves the make to be asked
   * again (unfinishedMake) keeps its id and body for finishMake; any other answer forgets them.
   */
  async function make(id: string, item: number, body: { by?: string; edit?: unknown } = {}, opId = mintCommandId()) {
    const key = `${id}/${item}`
    try {
      const out = await sendTwice(
        (b) => request<{ request: BrakemanRequest; made: unknown; ticket?: { key: string; url: string }; crew?: { id: string; url: string } }>(`/api/brakeman/requests/${encodeURIComponent(id)}/items/${item}/make`, { method: 'POST', body: b }),
        { opId, ...body },
      )
      forgetMake(id, item)
      store.apply(out.request)
      return out
    } catch (e) {
      if (unfinishedMake(e)) kept.value = { ...kept.value, [key]: { opId, body, at: Date.now() } }
      else forgetMake(id, item)
      writeKept(kept.value)
      throw e
    }
  }

  /** The make of the item this browser asked that did not finish, or undefined. */
  function keptMake(id: string, item: number): KeptMake | undefined {
    return kept.value[`${id}/${item}`]
  }

  /** Forgets the item's unfinished make: it was answered, or another make holds the item. */
  function forgetMake(id: string, item: number) {
    const key = `${id}/${item}`
    if (!(key in kept.value)) return
    const rest = { ...kept.value }
    delete rest[key]
    kept.value = rest
    writeKept(rest)
  }

  /** Asks the item's unfinished make again, as it was sent: the same id and body, which the route finishes and never makes twice. */
  function finishMake(id: string, item: number) {
    const k = keptMake(id, item)
    if (!k) throw new Error('This browser holds no make of it to finish.')
    return make(id, item, k.body, k.opId)
  }

  /** A request whole, with its brief (null until one is kept). */
  async function brief(id: string) {
    const out = await reads.request(id)
    store.apply(out.request)
    return out
  }

  /** A brief's refs as resolved when it was kept, by ref. */
  async function refs(id: string): Promise<Map<string, RefResolution>> {
    const out = await request<{ refs: RefResolution[] }>(`/api/brakeman/requests/${encodeURIComponent(id)}/refs`)
    return new Map((out.refs ?? []).map((r) => [r.ref, r]))
  }

  return { state, requests, available, problem, hasMore, unopened: unopenedIds, markOpened, load, more: () => store.readMore(), readRequest: (id: string) => store.readRequest(id), readNamed: (id: string) => store.readNamed(id), saveConfig, ask, cancel, stop, make, keptMake, forgetMake, finishMake, brief, refs, closed }
}
