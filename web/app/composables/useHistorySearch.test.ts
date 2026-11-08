import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createHistorySearch, HISTORY_BUSY_RETRY_MS, HISTORY_CACHE_MS, HISTORY_CACHE_SIZE, HISTORY_DEBOUNCE_MS, type HistorySearchDeps } from './useHistorySearch'
import type { HistoryReply } from '../utils/history'

// The history's search as typed (Program item HS1, PR-7): asked 200 ms after the last key, one request in flight (the one before
// aborted), an answer applied only to the request that asked it (a slow answer to an earlier query dropped), the last 20 queries'
// answers kept 30 s, nothing asked without the workbench token and the cache forgotten with it. The clock and the timers are fake;
// every reply is held until the test lets it go, in the order each case needs.

const reply = (q: string, extra: Partial<HistoryReply> = {}): HistoryReply => ({ entries: [{ key: `k:${q}`, title: q } as HistoryReply['entries'][number]], total: 1, indexing: { state: 'ready' }, ...extra })

interface Held {
  q: string
  after?: string
  signal: AbortSignal
  resolve: (r: HistoryReply) => void
  reject: (e: unknown) => void
}

let held: Held[]
let token: boolean
let clock: number

function make(o: Partial<HistorySearchDeps> = {}) {
  return createHistorySearch({
    hasToken: () => token,
    now: () => clock,
    search: (q, x) => new Promise<HistoryReply>((resolve, reject) => held.push({ q, after: x.after, signal: x.signal, resolve, reject })),
    similar: () => Promise.resolve({ entries: [] }),
    ...o,
  })
}

const flush = () => new Promise<void>((r) => queueMicrotask(r)).then(() => new Promise<void>((r) => queueMicrotask(r)))

beforeEach(() => {
  vi.useFakeTimers()
  held = []
  token = true
  clock = 1_000_000
})
afterEach(() => vi.useRealTimers())

describe('the debounce', () => {
  it('asks once, 200 ms after the last key, for the query as typed then', () => {
    const s = make()
    s.search('s')
    vi.advanceTimersByTime(HISTORY_DEBOUNCE_MS - 1)
    s.search('st')
    vi.advanceTimersByTime(HISTORY_DEBOUNCE_MS - 1)
    s.search('stub')
    expect(held).toHaveLength(0)
    vi.advanceTimersByTime(HISTORY_DEBOUNCE_MS - 1)
    expect(held).toHaveLength(0)
    vi.advanceTimersByTime(1)
    expect(held.map((h) => h.q)).toEqual(['stub'])
    expect(s.wanted.value).toBe('stub')
    expect(s.pending.value).toBe(true)
  })

  it('asks nothing again for the same query while it is due, on its way or answered', async () => {
    const s = make()
    s.search('stub')
    s.search('stub')
    vi.advanceTimersByTime(HISTORY_DEBOUNCE_MS)
    s.search('stub')
    expect(held).toHaveLength(1)
    held[0]!.resolve(reply('stub'))
    await flush()
    s.search('stub')
    vi.advanceTimersByTime(HISTORY_DEBOUNCE_MS)
    expect(held).toHaveLength(1)
    expect(s.result.value?.q).toBe('stub')
  })
})

describe('one request in flight', () => {
  it('aborts the request before when the next is asked', () => {
    const s = make()
    s.searchNow('one')
    s.searchNow('two')
    expect(held.map((h) => [h.q, h.signal.aborted])).toEqual([
      ['one', true],
      ['two', false],
    ])
  })

  it('drops a slow answer to an earlier query, landing after the later one', async () => {
    const s = make()
    s.searchNow('one')
    s.searchNow('two')
    held[1]!.resolve(reply('two'))
    await flush()
    expect(s.result.value?.q).toBe('two')
    // The earlier answer lands last (its server answered before it saw the abort): applied to nothing.
    held[0]!.resolve(reply('one'))
    await flush()
    expect(s.result.value?.q).toBe('two')
    expect(s.result.value?.reply.entries[0]?.key).toBe('k:two')
    expect(s.pending.value).toBe(false)
  })

  it('drops an earlier one that fails after the later one was asked, and its refusal too', async () => {
    const s = make()
    s.searchNow('one')
    s.searchNow('two')
    held[0]!.reject(Object.assign(new Error('bad'), { code: 'bad_query' }))
    await flush()
    expect(s.error.value).toBeNull()
    expect(s.pending.value).toBe(true)
    held[1]!.reject(Object.assign(new Error('Unknown filter "foo"'), { code: 'bad_query' }))
    await flush()
    expect(s.error.value).toEqual({ q: 'two', code: 'bad_query', message: 'Unknown filter "foo"' })
  })

  it('asks a busy refusal again after a pause, with the same ticket', async () => {
    const s = make()
    s.searchNow('stub')
    held[0]!.reject(Object.assign(new Error('busy'), { code: 'history_busy' }))
    await flush()
    expect(held).toHaveLength(1)
    vi.advanceTimersByTime(HISTORY_BUSY_RETRY_MS)
    await flush()
    expect(held.map((h) => h.q)).toEqual(['stub', 'stub'])
    held[1]!.resolve(reply('stub'))
    await flush()
    expect(s.result.value?.q).toBe('stub')
    expect(s.error.value).toBeNull()
  })

  it('appends the next page to the answer shown, and a search asked meanwhile overtakes it', async () => {
    const s = make()
    s.searchNow('stub')
    held[0]!.resolve(reply('stub', { next: 'c1', total: 2 }))
    await flush()
    void s.more()
    expect(held[1]).toMatchObject({ q: 'stub', after: 'c1' })
    held[1]!.resolve({ entries: [{ key: 'k:2', title: 'two' } as HistoryReply['entries'][number]], total: 2, indexing: { state: 'ready' } })
    await flush()
    expect(s.result.value?.reply.entries.map((e) => e.key)).toEqual(['k:stub', 'k:2'])
    expect(s.result.value?.reply.next).toBeUndefined()
    // A next page asked, then another query: the page lands after and is applied to nothing.
    s.searchNow('other')
    held[2]!.resolve(reply('other', { next: 'c2' }))
    await flush()
    void s.more()
    s.searchNow('third')
    expect(held[3]!.signal.aborted).toBe(true)
    held[3]!.resolve({ entries: [{ key: 'k:late', title: 'late' } as HistoryReply['entries'][number]], total: 2, indexing: { state: 'ready' } })
    await flush()
    expect(s.result.value?.q).toBe('other')
    expect(s.result.value?.reply.entries.map((e) => e.key)).toEqual(['k:other'])
  })
})

describe('the cache', () => {
  it('serves an answer again at once for 30 s, then asks again', async () => {
    const s = make()
    s.searchNow('stub')
    held[0]!.resolve(reply('stub'))
    await flush()
    s.searchNow('other')
    held[1]!.resolve(reply('other'))
    await flush()
    // Back to the first: no request, no debounce.
    s.search('stub')
    expect(held).toHaveLength(2)
    expect(s.result.value?.q).toBe('stub')
    expect(s.pending.value).toBe(false)
    clock += HISTORY_CACHE_MS + 1
    s.search('other')
    vi.advanceTimersByTime(HISTORY_DEBOUNCE_MS)
    expect(held.map((h) => h.q)).toEqual(['stub', 'other', 'other'])
  })

  it('keeps the last 20 queries: the 21st asked forgets the first', async () => {
    const s = make()
    for (let i = 0; i <= HISTORY_CACHE_SIZE; i++) {
      s.searchNow(`q${i}`)
      held[i]!.resolve(reply(`q${i}`))
      await flush()
    }
    s.searchNow('q1')
    expect(held).toHaveLength(HISTORY_CACHE_SIZE + 1)
    s.searchNow('q0')
    expect(held).toHaveLength(HISTORY_CACHE_SIZE + 2)
    expect(held.at(-1)!.q).toBe('q0')
  })

  it('a cached answer shown overtakes a request on its way', async () => {
    const s = make()
    s.searchNow('stub')
    held[0]!.resolve(reply('stub'))
    await flush()
    s.searchNow('slow')
    s.searchNow('stub')
    expect(held[1]!.signal.aborted).toBe(true)
    held[1]!.resolve(reply('slow'))
    await flush()
    expect(s.result.value?.q).toBe('stub')
  })
})

describe('the workbench token', () => {
  it('asks nothing without it', () => {
    token = false
    const s = make()
    s.search('stub')
    vi.advanceTimersByTime(HISTORY_DEBOUNCE_MS)
    s.searchNow('stub')
    void s.more()
    void s.findSimilar({ key: 'check:r/t/1/c/1', title: 'c on t #1' })
    expect(held).toHaveLength(0)
    expect(s.result.value).toBeNull()
  })

  it('forgets the cache with it, and an answer asked with it lands on nothing', async () => {
    const s = make()
    s.searchNow('stub')
    held[0]!.resolve(reply('stub'))
    await flush()
    s.searchNow('other')
    s.forget()
    expect(held[1]!.signal.aborted).toBe(true)
    held[1]!.resolve(reply('other'))
    await flush()
    expect(s.result.value).toBeNull()
    // Another token: the first one's answer is not served from the cache, it is asked again.
    s.searchNow('stub')
    expect(held.map((h) => h.q)).toEqual(['stub', 'other', 'stub'])
    expect(s.result.value).toBeNull()
  })
})

describe('Find this elsewhere', () => {
  it("shows the similar route's answer for the receipt asked about, and a search typed after drops it", async () => {
    let release!: (r: { entries: HistoryReply['entries'] }) => void
    const asked: Array<[string, string]> = []
    const s = make({
      similar: (key, scope) => {
        asked.push([key, scope])
        return new Promise((r) => (release = r))
      },
    })
    s.searchNow('stub')
    held[0]!.resolve(reply('stub'))
    await flush()
    void s.findSimilar({ key: 'check:r/build/1/fixed/1', title: 'fixed on build #1' }, 'all')
    expect(asked).toEqual([['check:r/build/1/fixed/1', 'all']])
    expect(s.similar.value).toMatchObject({ key: 'check:r/build/1/fixed/1', scope: 'all', reply: null })
    release({ entries: [{ key: 'check:r2/build/1/fixed/1', title: 'fixed on build #1' } as HistoryReply['entries'][number]] })
    await flush()
    expect(s.similar.value?.reply?.entries.map((e) => e.key)).toEqual(['check:r2/build/1/fixed/1'])
    s.search('stub fixed')
    expect(s.similar.value).toBeNull()
  })
})
