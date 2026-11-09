import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createHistorySearch, HISTORY_BUSY_RETRY_MS, HISTORY_BUSY_TRIES, HISTORY_CACHE_MS, HISTORY_CACHE_SIZE, HISTORY_DEBOUNCE_MS, HISTORY_READ_MS, HISTORY_TIMEOUT, type HistoryOptions, type HistorySearchDeps } from './useHistorySearch'
import type { HistoryItem, HistoryReply } from '../utils/history'

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

interface HeldEntry {
  key: string
  signal: AbortSignal
  resolve: (e: HistoryItem) => void
  reject: (e: unknown) => void
}

let held: Held[]
let entries: HeldEntry[]
let token: boolean
let clock: number

function make(o: Partial<HistorySearchDeps> = {}) {
  return createHistorySearch({
    hasToken: () => token,
    now: () => clock,
    search: (q, x) => new Promise<HistoryReply>((resolve, reject) => held.push({ q, after: x.after, signal: x.signal, resolve, reject })),
    similar: () => Promise.resolve({ entries: [] }),
    entry: (key, signal) => new Promise<HistoryItem>((resolve, reject) => entries.push({ key, signal, resolve, reject })),
    ...o,
  })
}

const busy = () => Object.assign(new Error('too many history reads in flight'), { code: 'history_busy' })
const refused = () => Object.assign(new Error('workbench token required'), { code: 'unauthorized', status: 401 })
const anEntry = (key: string, offered = 'yes') => ({ key, title: key, availability: { state: 'record', words: 'a record', resume: { offered } } }) as unknown as HistoryItem

const flush = () => new Promise<void>((r) => queueMicrotask(r)).then(() => new Promise<void>((r) => queueMicrotask(r)))

beforeEach(() => {
  vi.useFakeTimers()
  held = []
  entries = []
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
    // The token cleared: nothing asked again without one.
    token = false
    s.forget()
    expect(held[1]!.signal.aborted).toBe(true)
    held[1]!.resolve(reply('other'))
    await flush()
    expect(s.result.value).toBeNull()
    expect(held).toHaveLength(2)
    // Another token: the first one's answer is not served from the cache, it is asked again.
    token = true
    s.searchNow('stub')
    expect(held.map((h) => h.q)).toEqual(['stub', 'other', 'stub'])
    expect(s.result.value).toBeNull()
  })
})

// The read rule's paths, each its own case: a token replaced by another (A to B, the gate's recovery from a wrong token) asks again
// what was shown with the old one, with no new call from the caller (whose watch sees `hasToken` stay true and does not fire); a
// refusal counts only for the token it carried; the → entry read is bounded and cancelled by the next one; a busy refusal of a page,
// of Find this elsewhere and of an entry is asked again.
describe('a token replaced by another', () => {
  it('asks the query shown again with the new token, no search called again, and shows its answer', async () => {
    const s = make()
    s.searchNow('kind:session')
    held[0]!.resolve(reply('kind:session'))
    await flush()
    s.forget()
    expect(s.result.value).toBeNull()
    expect(held.map((h) => h.q)).toEqual(['kind:session', 'kind:session'])
    expect(s.pending.value).toBe(true)
    held[1]!.resolve(reply('kind:session'))
    await flush()
    expect(s.result.value?.q).toBe('kind:session')
    expect(s.pending.value).toBe(false)
  })

  it("asks again a query the old token's refusal left on Loading, and the late refusal counts for nothing", async () => {
    const s = make()
    s.searchNow('stub')
    s.forget()
    expect(held[0]!.signal.aborted).toBe(true)
    expect(held.map((h) => h.q)).toEqual(['stub', 'stub'])
    // The old token's 401 lands after the change: not this search's error.
    held[0]!.reject(refused())
    await flush()
    expect(s.error.value).toBeNull()
    held[1]!.resolve(reply('stub'))
    await flush()
    expect(s.result.value?.q).toBe('stub')
    expect(s.error.value).toBeNull()
  })

  it('asks a query typed while it was refused (due after the debounce) at once with the new token', () => {
    const s = make()
    s.search('stub')
    s.forget()
    expect(held.map((h) => h.q)).toEqual(['stub'])
    vi.advanceTimersByTime(HISTORY_DEBOUNCE_MS)
    expect(held).toHaveLength(1)
  })

  it('asks Find this elsewhere again in place of the answer', async () => {
    const asked: Array<[string, string]> = []
    const s = make({
      similar: (key, scope) => {
        asked.push([key, scope])
        return new Promise(() => {})
      },
    })
    s.searchNow('stub')
    void s.findSimilar({ key: 'check:r/b/1/c/1', title: 'c on b #1' }, 'all')
    s.forget()
    expect(asked).toEqual([
      ['check:r/b/1/c/1', 'all'],
      ['check:r/b/1/c/1', 'all'],
    ])
    expect(s.similar.value).toMatchObject({ key: 'check:r/b/1/c/1', scope: 'all', reply: null })
    expect(s.wanted.value).toBe('stub')
  })

  it("asks the → entry again, and the old token's answer is applied to nothing", async () => {
    const s = make()
    void s.readEntry('session:a')
    s.forget()
    expect(entries.map((e) => [e.key, e.signal.aborted])).toEqual([
      ['session:a', true],
      ['session:a', false],
    ])
    entries[0]!.resolve(anEntry('session:a', 'no'))
    await flush()
    expect(s.entryRead.value).toEqual({ key: 'session:a', entry: null })
    entries[1]!.resolve(anEntry('session:a'))
    await flush()
    expect(s.entryRead.value?.entry?.availability.resume?.offered).toBe('yes')
  })
})

// Codex round 2's ordering: a reply that has arrived (its read resumed) but not yet been applied when the token is forgotten. The
// ticket is compared where the reply is applied, in the same step, so the forget between arrival and apply leaves nothing of the old
// reply: not the answer, not the cache, not a page, not Find this elsewhere, not the → entry. Each case lets the reply's own step run
// (one microtask: the read resumes), forgets, then lets the rest run.
const arrived = () => Promise.resolve()

describe('a forget between a reply arriving and its being applied', () => {
  it('leaves the search cleared and its cache empty, the new request on its way', async () => {
    const s = make()
    s.searchNow('stub')
    held[0]!.resolve(reply('stub'))
    await arrived()
    s.forget()
    await flush()
    expect(s.result.value).toBeNull()
    expect(s.pending.value).toBe(true)
    expect(held.map((h) => h.q)).toEqual(['stub', 'stub'])
    // The cache holds nothing asked with the old token: the same query asks again rather than serving the old reply.
    token = false
    s.forget()
    token = true
    s.searchNow('stub')
    expect(held).toHaveLength(3)
    expect(s.result.value).toBeNull()
  })

  it('leaves the next page unappended', async () => {
    const s = make()
    s.searchNow('stub')
    held[0]!.resolve(reply('stub', { next: 'c1' }))
    await flush()
    void s.more()
    expect(held[1]!.after).toBe('c1')
    held[1]!.resolve(reply('page2'))
    await arrived()
    s.forget()
    await flush()
    expect(s.result.value).toBeNull()
    expect(held.map((h) => [h.q, h.after])).toEqual([
      ['stub', undefined],
      ['stub', 'c1'],
      ['stub', undefined],
    ])
  })

  it('leaves Find this elsewhere asked again, the old answer not shown', async () => {
    const similar: Array<(r: { entries: HistoryItem[] }) => void> = []
    const s = make({ similar: () => new Promise((resolve) => similar.push(resolve)) })
    void s.findSimilar({ key: 'check:r/b/1/c/1', title: 'c on b #1' })
    similar[0]!({ entries: [anEntry('check:old')] })
    await arrived()
    s.forget()
    await flush()
    expect(similar).toHaveLength(2)
    expect(s.similar.value).toMatchObject({ key: 'check:r/b/1/c/1', reply: null })
  })

  it('leaves the → entry asked again, the old answer not applied', async () => {
    const s = make()
    void s.readEntry('session:a')
    entries[0]!.resolve(anEntry('session:a', 'no'))
    await arrived()
    s.forget()
    await flush()
    expect(entries).toHaveLength(2)
    expect(s.entryRead.value).toEqual({ key: 'session:a', entry: null })
  })
})

describe('the → entry read', () => {
  it('is cancelled by the next one, whose answer alone is applied', async () => {
    const s = make()
    void s.readEntry('session:a')
    void s.readEntry('session:b')
    void s.readEntry('session:c')
    expect(entries.map((e) => [e.key, e.signal.aborted])).toEqual([
      ['session:a', true],
      ['session:b', true],
      ['session:c', false],
    ])
    entries[2]!.resolve(anEntry('session:c'))
    entries[0]!.resolve(anEntry('session:a'))
    entries[1]!.reject(new Error('late'))
    await flush()
    expect(s.entryRead.value).toMatchObject({ key: 'session:c' })
    expect(s.entryRead.value?.error).toBeUndefined()
  })

  it('asks nothing more for a → again on the row whose read is on its way', () => {
    const s = make()
    void s.readEntry('session:a')
    void s.readEntry('session:a')
    expect(entries).toHaveLength(1)
  })

  it('is cancelled as the palette closes (stop), its answer applied to nothing', async () => {
    const s = make()
    void s.readEntry('session:a')
    s.stop()
    expect(entries[0]!.signal.aborted).toBe(true)
    entries[0]!.resolve(anEntry('session:a'))
    await flush()
    expect(s.entryRead.value).toBeNull()
  })

  it('asks a busy refusal again while it is the current read, and says a refusal past the tries', async () => {
    const s = make()
    void s.readEntry('session:a')
    entries[0]!.reject(busy())
    await flush()
    vi.advanceTimersByTime(HISTORY_BUSY_RETRY_MS)
    await flush()
    expect(entries.map((e) => e.key)).toEqual(['session:a', 'session:a'])
    entries[1]!.resolve(anEntry('session:a'))
    await flush()
    expect(s.entryRead.value?.entry?.key).toBe('session:a')
    // Another read, busy every time: after HISTORY_BUSY_TRIES it is said, not asked forever.
    void s.readEntry('session:b')
    for (let i = 0; i < HISTORY_BUSY_TRIES; i++) {
      entries.at(-1)!.reject(busy())
      await flush()
      vi.advanceTimersByTime(HISTORY_BUSY_RETRY_MS)
      await flush()
    }
    expect(entries.filter((e) => e.key === 'session:b')).toHaveLength(HISTORY_BUSY_TRIES)
    expect(s.entryRead.value).toEqual({ key: 'session:b', entry: null, error: 'too many history reads in flight' })
  })

  it('a busy refusal waited out is not asked again once a newer read replaced it', async () => {
    const s = make()
    void s.readEntry('session:a')
    entries[0]!.reject(busy())
    await flush()
    void s.readEntry('session:b')
    vi.advanceTimersByTime(HISTORY_BUSY_RETRY_MS)
    await flush()
    expect(entries.map((e) => e.key)).toEqual(['session:a', 'session:b'])
  })
})

describe('a busy refusal of a page or of Find this elsewhere', () => {
  it('asks the next page again', async () => {
    const s = make()
    s.searchNow('stub')
    held[0]!.resolve(reply('stub', { next: 'c1', total: 2 }))
    await flush()
    void s.more()
    held[1]!.reject(busy())
    await flush()
    expect(s.error.value).toBeNull()
    vi.advanceTimersByTime(HISTORY_BUSY_RETRY_MS)
    await flush()
    expect(held.map((h) => [h.q, h.after])).toEqual([
      ['stub', undefined],
      ['stub', 'c1'],
      ['stub', 'c1'],
    ])
    held[2]!.resolve({ entries: [{ key: 'k:2', title: 'two' } as HistoryReply['entries'][number]], total: 2, indexing: { state: 'ready' } })
    await flush()
    expect(s.result.value?.reply.entries.map((e) => e.key)).toEqual(['k:stub', 'k:2'])
    expect(s.error.value).toBeNull()
  })

  it('asks Find this elsewhere again', async () => {
    let n = 0
    const s = make({
      similar: () => (++n === 1 ? Promise.reject(busy()) : Promise.resolve({ entries: [{ key: 'check:r2/b/1/c/1', title: 'c' } as HistoryReply['entries'][number]] })),
    })
    void s.findSimilar({ key: 'check:r/b/1/c/1', title: 'c' })
    await flush()
    expect(s.similar.value?.error).toBeUndefined()
    vi.advanceTimersByTime(HISTORY_BUSY_RETRY_MS)
    await flush()
    expect(n).toBe(2)
    expect(s.similar.value?.reply?.entries.map((e) => e.key)).toEqual(['check:r2/b/1/c/1'])
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

// Codex round 3: every read carries the one bound, HISTORY_READ_MS. A read that stalls (a reply that never comes) is aborted at the
// bound and ends as a refusal with words, its pending state cleared, so the same search or → asked again is asked anew.
describe('a read that stalls', () => {
  it('ends the search at the bound as a refusal with words, aborted, and the same query asked again is asked anew', async () => {
    const s = make()
    s.searchNow('stub')
    vi.advanceTimersByTime(HISTORY_READ_MS - 1)
    await flush()
    expect(s.pending.value).toBe(true)
    expect(s.error.value).toBeNull()
    vi.advanceTimersByTime(1)
    await flush()
    expect(held[0]!.signal.aborted).toBe(true)
    expect(s.pending.value).toBe(false)
    expect(s.error.value).toMatchObject({ q: 'stub', code: HISTORY_TIMEOUT })
    expect(s.error.value?.message).toMatch(/did not answer in time/)
    s.searchNow('stub')
    expect(held.map((h) => h.q)).toEqual(['stub', 'stub'])
    expect(s.pending.value).toBe(true)
    held[1]!.resolve(reply('stub'))
    await flush()
    expect(s.result.value?.q).toBe('stub')
    expect(s.error.value).toBeNull()
  })

  it("ends the → entry's read at the bound with words, and a → again on the row asks it anew", async () => {
    const s = make()
    void s.readEntry('session:a')
    vi.advanceTimersByTime(HISTORY_READ_MS)
    await flush()
    expect(entries[0]!.signal.aborted).toBe(true)
    expect(s.entryRead.value).toMatchObject({ key: 'session:a', entry: null })
    expect(s.entryRead.value?.error).toMatch(/did not answer in time/)
    void s.readEntry('session:a')
    expect(entries).toHaveLength(2)
    entries[1]!.resolve(anEntry('session:a'))
    await flush()
    expect(s.entryRead.value?.entry?.availability.resume?.offered).toBe('yes')
  })

  it('counts the busy pauses inside the one bound', async () => {
    const s = make()
    s.searchNow('stub')
    held[0]!.reject(busy())
    await flush()
    vi.advanceTimersByTime(HISTORY_BUSY_RETRY_MS)
    await flush()
    expect(held).toHaveLength(2)
    vi.advanceTimersByTime(HISTORY_READ_MS - HISTORY_BUSY_RETRY_MS)
    await flush()
    expect(held[1]!.signal.aborted).toBe(true)
    expect(s.error.value?.code).toBe(HISTORY_TIMEOUT)
  })

  it('leaves a read that answered in time alone, and a read overtaken by the next applies nothing at its bound', async () => {
    const s = make()
    s.searchNow('one')
    held[0]!.resolve(reply('one'))
    await flush()
    s.searchNow('two')
    vi.advanceTimersByTime(HISTORY_READ_MS / 2)
    s.searchNow('three')
    vi.advanceTimersByTime(HISTORY_READ_MS / 2)
    await flush()
    // The bound of "two" passed: it was overtaken, so nothing is said of it; "three" is still on its way.
    expect(s.error.value).toBeNull()
    expect(s.pending.value).toBe(true)
    expect(s.result.value?.q).toBe('one')
    held[2]!.resolve(reply('three'))
    await flush()
    vi.advanceTimersByTime(HISTORY_READ_MS)
    await flush()
    expect(s.error.value).toBeNull()
    expect(s.result.value?.q).toBe('three')
  })
})

// Codex round 3: the History page's filter values are read under the same rule. A token forgotten clears them at once (nothing the old
// token read stays shown) and asks them again with the token held then; an older batch that lands after a newer token's is dropped.
describe('the filter values', () => {
  interface HeldOptions {
    signal: AbortSignal
    resolve: (o: HistoryOptions) => void
  }
  let asked: HeldOptions[]
  const opts = (p: string): HistoryOptions => ({ projects: [p], hosts: [`host-${p}`], agents: [{ id: `agent-${p}`, name: p }] })
  const withOptions = () =>
    make({ options: (signal) => new Promise<HistoryOptions>((resolve) => asked.push({ signal, resolve })) })
  beforeEach(() => (asked = []))

  it('are read with the token, cleared when it is forgotten, and asked again with the next one', async () => {
    const s = withOptions()
    void s.readOptions()
    asked[0]!.resolve(opts('a'))
    await flush()
    expect(s.options.value?.projects).toEqual(['a'])
    // Forget: cleared at once, nothing asked without a token.
    token = false
    s.forget()
    expect(s.options.value).toBeNull()
    expect(asked).toHaveLength(1)
    // The next token asks them again.
    token = true
    s.forget()
    expect(asked).toHaveLength(2)
    asked[1]!.resolve(opts('b'))
    await flush()
    expect(s.options.value).toEqual(opts('b'))
  })

  it("drop an older token's batch that lands after a newer token's", async () => {
    const s = withOptions()
    void s.readOptions()
    s.forget()
    expect(asked.map((a) => a.signal.aborted)).toEqual([true, false])
    asked[1]!.resolve(opts('new'))
    await flush()
    asked[0]!.resolve(opts('old'))
    await flush()
    expect(s.options.value).toEqual(opts('new'))
  })

  it('asked without a token are read when one comes', async () => {
    token = false
    const s = withOptions()
    void s.readOptions()
    expect(asked).toHaveLength(0)
    token = true
    s.forget()
    expect(asked).toHaveLength(1)
  })

  it('end at the bound with none, aborted, and are not read by a search of its own that has none to read', async () => {
    const s = withOptions()
    void s.readOptions()
    vi.advanceTimersByTime(HISTORY_READ_MS)
    await flush()
    expect(asked[0]!.signal.aborted).toBe(true)
    expect(s.options.value).toBeNull()
    // The palette's search reads no filter values: readOptions asks nothing, a forget neither.
    const p = make()
    void p.readOptions()
    p.forget()
    expect(p.options.value).toBeNull()
  })
})
