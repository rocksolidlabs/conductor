import { onBeforeUnmount, ref, shallowRef, watch } from 'vue'
import type { HistoryItem, HistoryReply, SimilarReply } from '~/utils/history'

// The history's search as typed (Program item HS1, docs/plans/history.md, "web/app", PR-7): the palette's `~` and its History group,
// and the History page, each with a search of its own. One rule governs every reply, as on the record page (useSessionRecord's
// `holds`): a reply is applied only to the request that asked for it, by its identity and its sequence. Every request (a search, its
// next page, Find this elsewhere) takes the next number and aborts the one before, so one is in flight at a time and an answer that
// lands after a newer request was asked (a slow answer to an earlier query) is dropped; forgetting the token moves the generation on,
// so nothing asked with the old token is applied after it.

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

/** What the search reads with: `GET /api/history` and `GET /api/history/similar`, the workbench token, the clock and the timers. */
export interface HistorySearchDeps {
  search: (q: string, o: { after?: string; limit: number; signal: AbortSignal }) => Promise<HistoryReply>
  similar: (key: string, scope: 'repo' | 'all', signal: AbortSignal) => Promise<SimilarReply>
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

export function createHistorySearch(deps: HistorySearchDeps) {
  const now = deps.now ?? (() => Date.now())
  const limit = deps.limit ?? HISTORY_PAGE

  /** The query last asked for (typed, and due after the debounce, or asked): what the rows are about. */
  const wanted = ref<string | null>(null)
  const result = shallowRef<HistoryResult | null>(null)
  /** A request for `wanted` (or its next page) is on its way. */
  const pending = ref(false)
  const error = shallowRef<HistoryError | null>(null)
  const similar = shallowRef<SimilarState | null>(null)

  const cache = new Map<string, { reply: HistoryReply; at: number }>()
  let seq = 0
  let generation = 0
  let inflight: { controller: AbortController; q: string; page: boolean } | null = null
  let timer: ReturnType<typeof setTimeout> | undefined

  type Ticket = { seq: number; generation: number }
  const take = (): Ticket => {
    inflight?.controller.abort()
    inflight = null
    return { seq: ++seq, generation }
  }
  const holds = (t: Ticket) => t.seq === seq && t.generation === generation

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

  const codeOf = (e: unknown) => (e as { code?: string }).code ?? 'error'
  const wordsOf = (e: unknown) => (e as { message?: string }).message || 'The history could not be searched.'

  /** Asks `q` now (no debounce), unless its answer is cached or a request for it is already on its way. */
  function searchNow(q: string) {
    clearTimeout(timer)
    timer = undefined
    wanted.value = q
    similar.value = null
    if (!deps.hasToken()) return
    const hit = cached(q)
    if (hit) {
      take()
      pending.value = false
      error.value = null
      result.value = { q, reply: hit }
      return
    }
    if (inflight && inflight.q === q && !inflight.page) return
    void ask(q, take())
  }

  async function ask(q: string, t: Ticket, tries = 1): Promise<void> {
    const controller = new AbortController()
    inflight = { controller, q, page: false }
    pending.value = true
    try {
      const reply = await deps.search(q, { limit, signal: controller.signal })
      if (!holds(t)) return
      keep(q, reply)
      result.value = { q, reply }
      error.value = null
    } catch (e) {
      if (!holds(t) || controller.signal.aborted) return
      if (codeOf(e) === 'history_busy' && tries < HISTORY_BUSY_TRIES) {
        await new Promise((r) => setTimeout(r, HISTORY_BUSY_RETRY_MS))
        if (holds(t)) return ask(q, t, tries + 1)
        return
      }
      error.value = { q, code: codeOf(e), message: wordsOf(e) }
    } finally {
      if (holds(t) && inflight?.controller === controller) {
        inflight = null
        pending.value = false
      }
    }
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
    const t = take()
    const controller = new AbortController()
    inflight = { controller, q: shown.q, page: true }
    pending.value = true
    try {
      const reply = await deps.search(shown.q, { after: shown.reply.next, limit, signal: controller.signal })
      if (!holds(t)) return
      result.value = { q: shown.q, reply: { ...reply, entries: [...shown.reply.entries, ...reply.entries] } }
      error.value = null
    } catch (e) {
      if (!holds(t) || controller.signal.aborted) return
      error.value = { q: shown.q, code: codeOf(e), message: wordsOf(e) }
    } finally {
      if (holds(t) && inflight?.controller === controller) {
        inflight = null
        pending.value = false
      }
    }
  }

  /** Find this elsewhere: the receipts of the same command in the entry's repository (`repo`) or every one (`all`), shown in place of the answer. */
  async function findSimilar(e: Pick<HistoryItem, 'key' | 'title'>, scope: 'repo' | 'all' = 'repo'): Promise<void> {
    if (!deps.hasToken()) return
    clearTimeout(timer)
    timer = undefined
    const t = take()
    const state: SimilarState = { key: e.key, title: e.title, scope, reply: null }
    similar.value = state
    const controller = new AbortController()
    inflight = { controller, q: '', page: true }
    pending.value = true
    try {
      const reply = await deps.similar(e.key, scope, controller.signal)
      if (!holds(t)) return
      similar.value = { ...state, reply }
    } catch (x) {
      if (!holds(t) || controller.signal.aborted) return
      similar.value = { ...state, reply: { entries: [] }, error: wordsOf(x) }
    } finally {
      if (holds(t) && inflight?.controller === controller) {
        inflight = null
        pending.value = false
      }
    }
  }

  /** Nothing more is asked or applied (the palette closed, the page left); the cache stays for the next open. */
  function stop() {
    clearTimeout(timer)
    timer = undefined
    take()
    pending.value = false
    wanted.value = null
    similar.value = null
  }

  /** The token forgotten or replaced: the cache goes with it, and nothing asked with it is applied after this. */
  function forget() {
    generation++
    stop()
    cache.clear()
    result.value = null
    error.value = null
  }

  return { wanted, result, pending, error, similar, search, searchNow, more, findSimilar, stop, forget }
}

export type HistorySearch = ReturnType<typeof createHistorySearch>

/** A search of its own for a component (the palette, the History page), with the workbench token; its cache forgotten with the token. */
export function useHistorySearch(limit = HISTORY_PAGE): HistorySearch {
  const { request } = useApi()
  const admin = useWorkbenchToken()
  const s = createHistorySearch({
    limit,
    hasToken: () => admin.hasToken.value,
    search: (q, o) => request<HistoryReply>('/api/history', { query: { q, limit: String(o.limit), ...(o.after ? { after: o.after } : {}) }, signal: o.signal }),
    similar: (key, scope, signal) => request<SimilarReply>('/api/history/similar', { query: { key, scope }, signal }),
  })
  watch(
    () => admin.token.value,
    () => s.forget(),
  )
  onBeforeUnmount(() => s.stop())
  return s
}
