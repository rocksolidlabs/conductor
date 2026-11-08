import { getCurrentScope, onScopeDispose, ref, shallowRef, watch } from 'vue'
import type { HistoryItem, HistoryReply, SimilarReply } from '~/utils/history'

// The history's reads (Program item HS1, docs/plans/history.md, "web/app", PR-7): the search as typed (the palette's `~` and its
// History group, the History page, each with a search of its own), its next page, Find this elsewhere, and the palette's read of an
// entry's Resume offer as → reveals a row. One rule governs every read, held in one place (`slot` and `read` below; the record page's
// `holds` is the same rule):
// - a read is bounded and cancelled by the next one for the same thing: each thing read (the answer shown, the → entry) has one read
//   in flight at most, the next aborts the one before, and a `429 history_busy` (an aborted read the server has not let go yet) is
//   asked again a bounded number of times while the read is still the current one;
// - a reply is applied only to the request that asked for it: by its number in its slot and the token's generation, so an answer that
//   lands after a newer request was asked (a slow answer to an earlier query) is dropped, a failure too;
// - a refusal counts only for the token it carried: a token forgotten or replaced moves the generation on, so nothing asked with the
//   old token (its 401 included) is applied after it, and useApi asks for the token only while the refused one is still held;
// - a token change asks again what was shown or asked with the old one (the search, Find this elsewhere, the → entry), with the new
//   token, so a token replaced by another (the gate's recovery from a wrong one) never leaves a page on Loading.

/** How long after the last key a search is asked. */
export const HISTORY_DEBOUNCE_MS = 200
/** How many queries' answers are kept, and how long each is served without asking again. */
export const HISTORY_CACHE_SIZE = 20
export const HISTORY_CACHE_MS = 30_000
/** A page of the search: the route's default. */
export const HISTORY_PAGE = 20
/** `429 history_busy` (two reads in flight from this client: an aborted one the server has not let go yet) is asked again, a few times. */
export const HISTORY_BUSY_RETRY_MS = 300
export const HISTORY_BUSY_TRIES = 3

/** What the search reads with: `GET /api/history`, `/api/history/similar` and `/api/history/entry`, the workbench token, the clock. */
export interface HistorySearchDeps {
  search: (q: string, o: { after?: string; limit: number; signal: AbortSignal }) => Promise<HistoryReply>
  similar: (key: string, scope: 'repo' | 'all', signal: AbortSignal) => Promise<SimilarReply>
  entry: (key: string, signal: AbortSignal) => Promise<HistoryItem>
  hasToken: () => boolean
  now?: () => number
  limit?: number
}

/** The answer shown: the query it is for and the reply, its later pages appended. */
export interface HistoryResult {
  q: string
  reply: HistoryReply
}

/** A request that failed, for the query it asked: its words and the route's code. */
export interface HistoryError {
  q: string
  code: string
  message: string
}

/** Find this elsewhere: the receipt asked about, the scope, and the answer once it came. */
export interface SimilarState {
  key: string
  title: string
  scope: 'repo' | 'all'
  reply: SimilarReply | null
  error?: string
}

/** An entry read as → reveals its row (whether Resume is offered): the key asked about, and the entry or the failure once it came. */
export interface EntryRead {
  key: string
  entry: HistoryItem | null
  error?: string
}

/** A read's ticket: its number in its slot, the token's generation when it was asked, and the signal that cancels it. */
interface Ticket {
  seq: number
  generation: number
  signal: AbortSignal
}

const codeOf = (e: unknown) => (e as { code?: string })?.code ?? 'error'

export function createHistorySearch(deps: HistorySearchDeps) {
  const now = deps.now ?? (() => Date.now())
  const limit = deps.limit ?? HISTORY_PAGE

  /** The query last asked for (typed, and due after the debounce, or asked): what the rows are about. */
  const wanted = ref<string | null>(null)
  const result = shallowRef<HistoryResult | null>(null)
  /** A request for `wanted` (or its next page, or Find this elsewhere) is on its way. */
  const pending = ref(false)
  const error = shallowRef<HistoryError | null>(null)
  const similar = shallowRef<SimilarState | null>(null)
  const entryRead = shallowRef<EntryRead | null>(null)

  const cache = new Map<string, { reply: HistoryReply; at: number }>()
  let generation = 0
  let timer: ReturnType<typeof setTimeout> | undefined

  // ---- The read rule, in one place.

  /** One thing read: one read in flight at most, the next aborting the one before; `cancel` aborts it and applies nothing after. */
  function slot() {
    let seq = 0
    let controller: AbortController | null = null
    return {
      take(): Ticket {
        controller?.abort()
        controller = new AbortController()
        return { seq: ++seq, generation, signal: controller.signal }
      },
      holds: (t: Ticket) => t.seq === seq && t.generation === generation,
      cancel() {
        controller?.abort()
        controller = null
        seq++
      },
    }
  }
  type Slot = ReturnType<typeof slot>

  /**
   * A read under the rule: its answer or its failure, or null once it no longer holds (a newer read of the same thing, a stop, a token
   * change), so nothing is applied for it. A `history_busy` refusal is asked again after a pause, at most HISTORY_BUSY_TRIES in all,
   * while the read holds.
   */
  async function read<T>(s: Slot, t: Ticket, fn: (signal: AbortSignal) => Promise<T>): Promise<{ ok: true; value: T } | { ok: false; error: unknown } | null> {
    for (let tries = 1; ; tries++) {
      try {
        const value = await fn(t.signal)
        return s.holds(t) ? { ok: true, value } : null
      } catch (e) {
        if (!s.holds(t) || t.signal.aborted) return null
        if (codeOf(e) !== 'history_busy' || tries >= HISTORY_BUSY_TRIES) return { ok: false, error: e }
        await new Promise((r) => setTimeout(r, HISTORY_BUSY_RETRY_MS))
        if (!s.holds(t)) return null
      }
    }
  }

  /** The answer shown: a search, its next page and Find this elsewhere replace one another. */
  const shownSlot = slot()
  /** What the read in flight for the answer shown is about: a coalesced search asks nothing again. */
  let inflight: { t: Ticket; q: string; page: boolean } | null = null
  /** The → entry: the next → (another row's) replaces it. */
  const entrySlot = slot()

  /** A read for the answer shown begins: the one before aborted, `pending` until this one ends while it holds. */
  function begin(q: string, page: boolean): Ticket {
    const t = shownSlot.take()
    inflight = { t, q, page }
    pending.value = true
    return t
  }
  function end(t: Ticket) {
    if (shownSlot.holds(t) && inflight?.t === t) {
      inflight = null
      pending.value = false
    }
  }

  function cached(q: string): HistoryReply | null {
    const c = cache.get(q)
    if (!c) return null
    if (now() - c.at > HISTORY_CACHE_MS) {
      cache.delete(q)
      return null
    }
    return c.reply
  }
  function keep(q: string, reply: HistoryReply) {
    cache.delete(q)
    cache.set(q, { reply, at: now() })
    while (cache.size > HISTORY_CACHE_SIZE) cache.delete(cache.keys().next().value!)
  }

  const wordsOf = (e: unknown, none = 'The history could not be searched.') => (e as { message?: string })?.message || none

  /** Asks `q` now (no debounce), unless its answer is cached or a request for it is already on its way. */
  function searchNow(q: string) {
    clearTimeout(timer)
    timer = undefined
    wanted.value = q
    similar.value = null
    if (!deps.hasToken()) return
    const hit = cached(q)
    if (hit) {
      shownSlot.cancel()
      inflight = null
      pending.value = false
      error.value = null
      result.value = { q, reply: hit }
      return
    }
    if (inflight && inflight.q === q && !inflight.page) return
    void ask(q)
  }

  async function ask(q: string): Promise<void> {
    const t = begin(q, false)
    const r = await read(shownSlot, t, (signal) => deps.search(q, { limit, signal }))
    if (r?.ok) {
      keep(q, r.value)
      result.value = { q, reply: r.value }
      error.value = null
    } else if (r) error.value = { q, code: codeOf(r.error), message: wordsOf(r.error) }
    end(t)
  }

  /** The search as typed: asked HISTORY_DEBOUNCE_MS after the last call; a cached answer is shown at once. */
  function search(q: string) {
    if (q === wanted.value) {
      // The same query again (a redraw, the palette's watcher): nothing new to ask while it is due, on its way, answered or refused.
      const due = timer !== undefined || (inflight?.q === q && !inflight.page)
      if (due || (result.value?.q === q && cached(q)) || error.value?.q === q) return
    } else similar.value = null
    clearTimeout(timer)
    timer = undefined
    wanted.value = q
    if (!deps.hasToken()) return
    if (cached(q)) return searchNow(q)
    timer = setTimeout(() => searchNow(q), HISTORY_DEBOUNCE_MS)
  }

  /** The next page of the answer shown (the History page's Show more), appended to it. */
  async function more(): Promise<void> {
    const shown = result.value
    if (!shown?.reply.next || shown.q !== wanted.value || !deps.hasToken()) return
    const t = begin(shown.q, true)
    const r = await read(shownSlot, t, (signal) => deps.search(shown.q, { after: shown.reply.next, limit, signal }))
    if (r?.ok) {
      result.value = { q: shown.q, reply: { ...r.value, entries: [...shown.reply.entries, ...r.value.entries] } }
      error.value = null
    } else if (r) error.value = { q: shown.q, code: codeOf(r.error), message: wordsOf(r.error) }
    end(t)
  }

  /** Find this elsewhere: the receipts of the same command in the entry's repository (`repo`) or every one (`all`), shown in place of the answer. */
  async function findSimilar(e: Pick<HistoryItem, 'key' | 'title'>, scope: 'repo' | 'all' = 'repo'): Promise<void> {
    if (!deps.hasToken()) return
    clearTimeout(timer)
    timer = undefined
    const t = begin('', true)
    const state: SimilarState = { key: e.key, title: e.title, scope, reply: null }
    similar.value = state
    const r = await read(shownSlot, t, (signal) => deps.similar(e.key, scope, signal))
    if (r?.ok) similar.value = { ...state, reply: r.value }
    else if (r) similar.value = { ...state, reply: { entries: [] }, error: wordsOf(r.error) }
    end(t)
  }

  /**
   * The entry's own read as → reveals its row (the search's rows carry no Resume offer): one at a time, the next → aborting it; a →
   * again on the row whose read is on its way asks nothing more.
   */
  async function readEntry(key: string): Promise<void> {
    if (!deps.hasToken()) return
    const cur = entryRead.value
    if (cur?.key === key && !cur.entry && !cur.error) return
    const t = entrySlot.take()
    entryRead.value = { key, entry: null }
    const r = await read(entrySlot, t, (signal) => deps.entry(key, signal))
    if (r?.ok) entryRead.value = { key, entry: r.value }
    else if (r) entryRead.value = { key, entry: null, error: wordsOf(r.error, 'could not be read') }
  }

  /** Every read cancelled, nothing applied after (the palette closed, the page left); `wanted` kept or not as the caller says. */
  function halt() {
    clearTimeout(timer)
    timer = undefined
    shownSlot.cancel()
    entrySlot.cancel()
    inflight = null
    pending.value = false
    similar.value = null
    entryRead.value = null
  }

  /** Nothing more is asked or applied (the palette closed, the page left); the cache stays for the next open. */
  function stop() {
    halt()
    wanted.value = null
  }

  /**
   * The token forgotten or replaced: the cache goes with it, nothing asked with it is applied after this, and what was asked or shown
   * is asked again with the token held now (none without one: the next token asks it).
   */
  function forget() {
    const again = { q: wanted.value, similar: similar.value, entry: entryRead.value?.key }
    generation++
    halt()
    cache.clear()
    result.value = null
    error.value = null
    wanted.value = again.q
    if (!deps.hasToken()) return
    // Find this elsewhere is shown in place of the answer: asked again in its place; otherwise the query wanted.
    if (again.similar) void findSimilar(again.similar, again.similar.scope)
    else if (again.q !== null) searchNow(again.q)
    if (again.entry) void readEntry(again.entry)
  }

  return { wanted, result, pending, error, similar, entryRead, search, searchNow, more, findSimilar, readEntry, stop, forget }
}

export type HistorySearch = ReturnType<typeof createHistorySearch>

/** A search of its own for a component (the palette, the History page), with the workbench token; forgotten and asked again with the token. */
export function useHistorySearch(limit = HISTORY_PAGE): HistorySearch {
  const { request } = useApi()
  const history = useHistory()
  const admin = useWorkbenchToken()
  const s = createHistorySearch({
    limit,
    hasToken: () => admin.hasToken.value,
    search: (q, o) => request<HistoryReply>('/api/history', { query: { q, limit: String(o.limit), ...(o.after ? { after: o.after } : {}) }, signal: o.signal }),
    similar: (key, scope, signal) => request<SimilarReply>('/api/history/similar', { query: { key, scope }, signal }),
    entry: (key, signal) => history.entry(key, signal),
  })
  watch(
    () => admin.token.value,
    () => s.forget(),
  )
  // The component (or scope) that owns it ends: nothing more asked or applied.
  if (getCurrentScope()) onScopeDispose(() => s.stop())
  return s
}
