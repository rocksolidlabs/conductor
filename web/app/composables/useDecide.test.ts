import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createDecide, type DecideEnv } from './useDecide'
import type { DecideReceipt, DecideView } from '~/utils/protocol'
import { MAX_DECIDE_LIST, MAX_DECIDE_LIST_ENTRIES, MAX_DECIDE_REPLY, MAX_DECIDE_VIEW, MAX_ME_REPLY } from '~/utils/protocol'

// The decide page's client (docs/plans/decide-grant.md, "web/app"), on vitest's fake clock over a fake fetch whose replies the test
// gives one by one: the four routes alone, the pass in the header and in no URL, the request's options (checked after every test);
// the list's and each card's reads one at a time on their own schedules; the freshness window on the steady and the wall clock; the
// one order a card's replies pass; and what the page keeps of an answer.

const PASS = 'pass.of-ana_1'
type Req = {
  path: string
  method: string
  init: RequestInit
  headers: Record<string, string>
  body?: string
  done: boolean
  aborted: boolean
  reply: (status: number, body?: unknown) => void
  /** Answers with `status` at once and holds the body until the function it returns is called; an abort after it rejects nothing. */
  hold: (status: number, body: unknown) => () => void
  fail: () => void
}

let reqs: Req[]
let env: DecideEnv & { doc: EventTarget & { visibilityState: string }; win: EventTarget }
let store: { value: string; ended: boolean; refused?: string }

const ROUTES = [/^\/api\/me$/, /^\/api\/me\/decisions$/, /^\/api\/decisions\/dr-\d+$/, /^\/api\/decisions\/dr-\d+\/respond$/]

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(1_000_000)
  reqs = []
  store = { value: PASS, ended: false }
  const fetch = (url: string, init: RequestInit) =>
    new Promise<Response>((resolve, reject) => {
      const r: Req = {
        path: url,
        method: init.method ?? 'GET',
        init,
        headers: init.headers as Record<string, string>,
        body: init.body as string | undefined,
        done: false,
        aborted: false,
        // A body given as a string is sent as it is; anything else as its JSON.
        reply(status, body) {
          r.done = true
          const bytes = new TextEncoder().encode(typeof body === 'string' ? body : body === undefined ? '' : JSON.stringify(body))
          const stream = new ReadableStream<Uint8Array>({
            start(c) {
              if (bytes.length) c.enqueue(bytes)
              c.close()
            },
          })
          resolve({ status, body: stream, arrayBuffer: async () => bytes.buffer } as unknown as Response)
        },
        hold(status, body) {
          r.done = true
          let release = () => {}
          const stream = new ReadableStream<Uint8Array>({
            start(c) {
              release = () => {
                c.enqueue(new TextEncoder().encode(JSON.stringify(body)))
                c.close()
              }
            },
          })
          resolve({ status, body: stream } as unknown as Response)
          return () => release()
        },
        fail() {
          r.done = true
          reject(new TypeError('network'))
        },
      }
      init.signal?.addEventListener('abort', () => {
        r.done = r.aborted = true
        reject(new DOMException('aborted', 'AbortError'))
      })
      reqs.push(r)
    })
  env = {
    fetch: fetch as unknown as typeof globalThis.fetch,
    pass: { get: () => store.value, end: (refused: string) => Object.assign(store, { value: '', ended: true, refused }) },
    base: '',
    doc: Object.assign(new EventTarget(), { visibilityState: 'visible' }),
    win: new EventTarget() as unknown as Window,
    // The steady clock: vitest's fake performance.now(), which setting the system's time does not move, begun before the page, and
    // moved on its own by `steadySkew`; the wall clock: vitest's fake Date.now(), moved on its own by vi.setSystemTime.
    now: () => 60_000 + performance.now() + steadySkew,
    wall: () => Date.now(),
  } as typeof env
  steadySkew = 0
})
let steadySkew = 0

afterEach(() => {
  // Every request: one of the four routes, the pass in its Authorization header and in no URL, no redirect followed, no cache, and no
  // credentials mode beside the header.
  for (const r of reqs) {
    expect(ROUTES.some((re) => re.test(r.path)), r.path).toBe(true)
    expect(r.headers.Authorization).toBe(`Bearer ${PASS}`)
    expect(r.path).not.toContain(PASS)
    expect(r.init).toMatchObject({ redirect: 'error', cache: 'no-store' })
    expect(r.init.credentials).toBeUndefined()
  }
  vi.useRealTimers()
})

const flush = () => vi.advanceTimersByTimeAsync(0)
/** The requests to path not yet answered, oldest first. */
const out = (path: string) => reqs.filter((r) => r.path === path && !r.done)
const count = (path: string) => reqs.filter((r) => r.path === path).length
const cardReadsOut = () => reqs.filter((r) => /^\/api\/decisions\/dr-\d+$/.test(r.path) && !r.done).length
/** Answers the oldest request to path not yet answered. */
async function answer(path: string, status: number, body?: unknown) {
  const r = out(path)[0]
  if (!r) throw new Error(`no request to ${path} out: ${JSON.stringify(reqs.filter((x) => !x.done).map((x) => x.path))}`)
  r.reply(status, body)
  await flush()
}
const open = (id: string, rev: number, extra: Partial<DecideView> = {}): DecideView => ({ id, rev, state: 'open', type: 'approval', question: `May ${id} go out?`, options: [{ label: 'Approve' }, { label: 'Reject' }], createdAt: '2026-11-12T00:00:00Z', own: [], ...extra })
const receipt = (responseId: string, extra: Partial<DecideReceipt> = {}): DecideReceipt => ({ k: 1, responseId, status: 'accepted', rev: 1, at: '2026-11-12T00:00:01Z', ...extra })
const list = (...ids: Array<[string, number]>) => ({ decisions: ids.map(([id, rev]) => ({ id, rev })) })
const ME = { subject: 'ana', name: 'Ana', grants: ['decide'] }
/** obj's JSON padded to exactly n bytes (ASCII). */
function padded(obj: object, n: number): string {
  const s = JSON.stringify({ ...obj, pad: '' })
  return s.slice(0, -2) + 'x'.repeat(n - s.length) + '"}'
}

/** A page signed in, its list answered with `entries`, and each card it opened read at its rev in the list. */
async function boot(did?: string, entries: Array<[string, number]> = [['dr-7', 1]]) {
  const d = createDecide(env, did)
  d.start()
  await answer('/api/me', 200, ME)
  await answer('/api/me/decisions', 200, list(...entries))
  for (const c of [...d.page.cards]) await answer(`/api/decisions/${c.id}`, 200, open(c.id, entries.find(([id]) => id === c.id)?.[1] ?? 1))
  return d
}
const card = (d: ReturnType<typeof createDecide>, id: string) => d.page.cards.find((c) => c.id === id)!
const NOT_KNOWN = 'Not known whether your answer to dr-7 was recorded: send it again.'

describe('the reads', () => {
  it('reads the list again 10 s after the last began while shown, none while hidden, and at once when shown or focused', async () => {
    const d = await boot()
    await vi.advanceTimersByTimeAsync(9_999)
    expect(count('/api/me/decisions')).toBe(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(out('/api/me/decisions')).toHaveLength(1)
    await answer('/api/me/decisions', 200, list(['dr-7', 1]))
    env.doc.visibilityState = 'hidden'
    await vi.advanceTimersByTimeAsync(60_000)
    expect(count('/api/me/decisions')).toBe(2)
    env.doc.visibilityState = 'visible'
    env.doc.dispatchEvent(new Event('visibilitychange'))
    expect(out('/api/me/decisions')).toHaveLength(1)
    await answer('/api/me/decisions', 200, list(['dr-7', 1]))
    env.win.dispatchEvent(new Event('focus'))
    expect(count('/api/me/decisions')).toBe(4)
    d.stop()
  })

  it('aborts a read at 10 s, never has two out at once, and starts one that fell due while one was out as it returns', async () => {
    const d = await boot()
    env.win.dispatchEvent(new Event('focus'))
    expect(out('/api/decisions/dr-7')).toHaveLength(1)
    env.win.dispatchEvent(new Event('focus'))
    env.win.dispatchEvent(new Event('focus'))
    expect(out('/api/decisions/dr-7')).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(9_999)
    expect(out('/api/decisions/dr-7')[0]!.aborted).toBe(false)
    await vi.advanceTimersByTimeAsync(1)
    const reads = reqs.filter((r) => r.path === '/api/decisions/dr-7')
    expect(reads.at(-2)!.aborted).toBe(true)
    expect(out('/api/decisions/dr-7')).toHaveLength(1)
    expect(count('/api/decisions/dr-7')).toBe(3)
    d.stop()
  })

  it('reads a card at once when the list holds it at another rev, and once when it left the list', async () => {
    const d = await boot(undefined, [['dr-7', 1], ['dr-8', 1]])
    env.win.dispatchEvent(new Event('focus'))
    await answer('/api/decisions/dr-7', 200, open('dr-7', 1))
    await answer('/api/decisions/dr-8', 200, open('dr-8', 1))
    await answer('/api/me/decisions', 200, list(['dr-7', 2], ['dr-8', 1]))
    expect(out('/api/decisions/dr-7')).toHaveLength(1)
    expect(out('/api/decisions/dr-8')).toHaveLength(0)
    await answer('/api/decisions/dr-7', 200, open('dr-7', 2))
    card(d, 'dr-8').draft.text = 'a draft'
    await vi.advanceTimersByTimeAsync(10_000)
    await answer('/api/me/decisions', 200, list(['dr-7', 2]))
    expect(out('/api/decisions/dr-8')).toHaveLength(1)
    await answer('/api/decisions/dr-8', 404, { error: { code: 'not_found', message: 'no such decision' } })
    expect(card(d, 'dr-8').end).toBe('gone')
    expect(card(d, 'dr-8').draft.text).toBe('a draft')
    expect(store.value).toBe(PASS)
    expect(d.answerable(card(d, 'dr-7'))).toBe(true)
    d.stop()
  })

  it('answers on a read begun before a take-back while its mark is fresh, then closes the card at the next read, started at once', async () => {
    const d = await boot()
    const c = card(d, 'dr-7')
    c.draft.choice = 0
    env.win.dispatchEvent(new Event('focus'))
    // The take-back commits on the server; a read falls due while the one begun before it is out.
    env.win.dispatchEvent(new Event('focus'))
    expect(out('/api/decisions/dr-7')).toHaveLength(1)
    await answer('/api/decisions/dr-7', 200, open('dr-7', 1))
    expect(d.answerable(c)).toBe(true)
    expect(out('/api/decisions/dr-7')).toHaveLength(1)
    await answer('/api/decisions/dr-7', 404, { error: { code: 'not_found', message: 'no such decision' } })
    expect(c).toMatchObject({ end: 'gone', draft: { choice: 0 } })
    expect(d.answerable(c)).toBe(false)
    d.stop()
  })

  it('draws the outcome of its own answer for a decision that left the list', async () => {
    const d = await boot()
    await vi.advanceTimersByTimeAsync(10_000)
    await answer('/api/me/decisions', 200, list())
    await answer('/api/decisions/dr-7', 200, { id: 'dr-7', rev: 2, own: [receipt('r1')], outcome: 'waiting' })
    expect(card(d, 'dr-7').view?.outcome).toBe('waiting')
    expect(card(d, 'dr-7').end).toBe('')
    d.stop()
  })

  it('starts no read while hidden: a list reply with a new decision or another rev leaves them due until the page is shown', async () => {
    const d = await boot()
    env.win.dispatchEvent(new Event('focus'))
    await answer('/api/decisions/dr-7', 200, open('dr-7', 1))
    env.doc.visibilityState = 'hidden'
    await answer('/api/me/decisions', 200, list(['dr-8', 1], ['dr-7', 2]))
    expect(card(d, 'dr-8')).toBeDefined()
    expect(cardReadsOut()).toBe(0)
    await vi.advanceTimersByTimeAsync(60_000)
    expect(reqs.filter((r) => !r.done)).toHaveLength(0)
    env.doc.visibilityState = 'visible'
    env.doc.dispatchEvent(new Event('visibilitychange'))
    expect(out('/api/decisions/dr-8')).toHaveLength(1)
    expect(out('/api/decisions/dr-7')).toHaveLength(1)
    d.stop()
  })

  it('says a list read that fails before any succeeded, under who is signed in, and tries it again', async () => {
    const d = createDecide(env)
    d.start()
    await answer('/api/me', 200, ME)
    await answer('/api/me/decisions', 503, { error: { code: 'ledger_unavailable', message: 'the decisions could not be read' } })
    expect(d.page.list).toMatchObject({ failed: true, loaded: false, refused: false })
    await vi.advanceTimersByTimeAsync(10_000)
    await answer('/api/me/decisions', 200, list(['dr-7', 1]))
    expect(d.page.list).toMatchObject({ failed: false, loaded: true })
    await vi.advanceTimersByTimeAsync(10_000)
    await answer('/api/me/decisions', 429, { error: { code: 'rate_limited', message: '' } })
    expect(d.page.list.failed).toBe(false)
    d.stop()
  })

  it('starts no list request when the tab was hidden before /api/me was answered, says nothing failed, and starts it once shown', async () => {
    const d = createDecide(env)
    d.start()
    env.doc.visibilityState = 'hidden'
    await answer('/api/me', 200, ME)
    expect(count('/api/me/decisions')).toBe(0)
    await vi.advanceTimersByTimeAsync(60_000)
    expect(count('/api/me/decisions')).toBe(0)
    expect(d.page).toMatchObject({ meFailed: false, list: { failed: false, refused: false } })
    env.doc.visibilityState = 'visible'
    env.doc.dispatchEvent(new Event('visibilitychange'))
    expect(out('/api/me/decisions')).toHaveLength(1)
    expect(count('/api/me')).toBe(1)
    d.stop()
  })

  it('takes no entry of a list reply past MAX_DECIDE_LIST_ENTRIES: a failed read', async () => {
    const d = createDecide(env)
    d.start()
    await answer('/api/me', 200, ME)
    await answer('/api/me/decisions', 200, list(...Array.from({ length: MAX_DECIDE_LIST_ENTRIES + 1 }, (_, i): [string, number] => [`dr-${i + 1}`, 1])))
    expect(d.page.list).toMatchObject({ loaded: false, failed: true, entries: [] })
    expect(d.page.cards).toHaveLength(0)
    await vi.advanceTimersByTimeAsync(10_000)
    await answer('/api/me/decisions', 200, list(...Array.from({ length: MAX_DECIDE_LIST_ENTRIES }, (_, i): [string, number] => [`dr-${i + 1}`, 1])))
    expect(d.page.list).toMatchObject({ loaded: true, failed: false })
    expect(d.page.list.entries).toHaveLength(MAX_DECIDE_LIST_ENTRIES)
    d.stop()
  })

  it('says a failed read of who the pass is and of a card before either succeeded, and clears each at its first success', async () => {
    const d = createDecide(env, 'dr-7')
    d.start()
    await answer('/api/me', 503, { error: { code: 'ledger_unavailable', message: '' } })
    expect(d.page).toMatchObject({ me: null, meFailed: true, list: { failed: false, refused: false } })
    await vi.advanceTimersByTimeAsync(10_000)
    await answer('/api/me', 200, ME)
    expect(d.page.meFailed).toBe(false)
    await answer('/api/decisions/dr-7', 503, { error: { code: 'ledger_unavailable', message: '' } })
    expect(card(d, 'dr-7')).toMatchObject({ view: null, failed: true, end: '' })
    await answer('/api/me/decisions', 200, list(['dr-7', 1]))
    await vi.advanceTimersByTimeAsync(10_000)
    await answer('/api/decisions/dr-7', 200, open('dr-7', 1))
    expect(card(d, 'dr-7')).toMatchObject({ failed: false, view: { rev: 1 } })
    await vi.advanceTimersByTimeAsync(10_000)
    await answer('/api/decisions/dr-7', 503, { error: { code: 'ledger_unavailable', message: '' } })
    expect(card(d, 'dr-7').failed).toBe(false)
    d.stop()
  })

  for (const [what, name] of [['a number', 7], ['a list', ['Ana']], ['an object', { first: 'Ana' }]] as const) {
    it(`takes no reply of who the pass is whose name is ${what}: nothing changes, the read failed, and it is tried again`, async () => {
      const d = createDecide(env, 'dr-7')
      d.start()
      await answer('/api/me', 200, { ...ME, name })
      expect(d.page).toMatchObject({ me: null, meFailed: true, notForIt: false, ended: false, list: { loaded: false, failed: false, refused: false } })
      expect(d.page.cards).toHaveLength(0)
      expect(reqs.map((r) => r.path)).toEqual(['/api/me'])
      await vi.advanceTimersByTimeAsync(10_000)
      await answer('/api/me', 200, ME)
      expect(d.page).toMatchObject({ me: ME, meFailed: false })
      d.stop()
    })
  }

  it('reads no more after a 403 with a code to /api/me, and says the refusal', async () => {
    const d = createDecide(env, 'dr-7')
    d.start()
    await answer('/api/me', 403, { error: { code: 'switchyard', message: 'this server coordinates hosted sessions and launches none' } })
    expect(d.page).toMatchObject({ me: null, meFailed: false, list: { refused: true } })
    await vi.advanceTimersByTimeAsync(60_000)
    env.win.dispatchEvent(new Event('focus'))
    expect(reqs.map((r) => r.path)).toEqual(['/api/me'])
    d.stop()
  })

  it("stops reading the list or a card the server refuses (403 with its code), and says so", async () => {
    const d = await boot()
    env.win.dispatchEvent(new Event('focus'))
    await answer('/api/decisions/dr-7', 403, { error: { code: 'switchyard', message: 'this server coordinates hosted sessions and launches none' } })
    expect(card(d, 'dr-7').end).toBe('refused')
    await answer('/api/me/decisions', 403, { error: { code: 'switchyard', message: 'this server coordinates hosted sessions and launches none' } })
    expect(d.page.list.refused).toBe(true)
    const n = reqs.length
    await vi.advanceTimersByTimeAsync(60_000)
    env.win.dispatchEvent(new Event('focus'))
    expect(reqs).toHaveLength(n)
    expect(store.value).toBe(PASS)
    d.stop()
  })
})

describe('freshness', () => {
  it('suspends a card whose reads time out once its mark is 25 s old, keeps its draft, and lets it be answered again on a read', async () => {
    const d = await boot()
    const c = card(d, 'dr-7')
    c.draft.choice = 0
    // The list is answered on its schedule; the card's reads each time out.
    for (let t = 0; t < 25; t += 5) {
      await vi.advanceTimersByTimeAsync(5_000)
      for (const r of out('/api/me/decisions')) r.reply(200, list(['dr-7', 1]))
      await flush()
    }
    expect(c.suspended).toBe(false)
    await vi.advanceTimersByTimeAsync(1)
    expect(c.suspended).toBe(true)
    expect(d.answerable(c)).toBe(false)
    await d.send('dr-7')
    expect(count('/api/decisions/dr-7/respond')).toBe(0)
    expect(c.draft.choice).toBe(0)
    expect(d.page.list.suspended).toBe(false)
    // The read under way times out at 30 s, and the next, due at once, starts in the tick after.
    await vi.advanceTimersByTimeAsync(5_100)
    await answer('/api/decisions/dr-7', 200, open('dr-7', 1))
    expect(c.suspended).toBe(false)
    expect(d.answerable(c)).toBe(true)
    d.stop()
  })

  it('never suspends a card on one read that succeeds at its 10 s bound', async () => {
    const d = await boot()
    const c = card(d, 'dr-7')
    await vi.advanceTimersByTimeAsync(10_000)
    for (const r of out('/api/me/decisions')) r.reply(200, list(['dr-7', 1]))
    await vi.advanceTimersByTimeAsync(9_999)
    await answer('/api/decisions/dr-7', 200, open('dr-7', 1))
    expect(c.suspended).toBe(false)
    await vi.advanceTimersByTimeAsync(5_000)
    expect(c.suspended).toBe(false)
    d.stop()
  })

  it('counts the window from the moment a read began: answered at 9 s, answerable at 24.9 s, suspended just past 25 s', async () => {
    const d = await boot()
    const c = card(d, 'dr-7')
    c.draft.choice = 0
    await vi.advanceTimersByTimeAsync(5_000)
    env.win.dispatchEvent(new Event('focus'))
    await vi.advanceTimersByTimeAsync(9_000)
    await answer('/api/decisions/dr-7', 200, open('dr-7', 1))
    await vi.advanceTimersByTimeAsync(15_900)
    expect(d.answerable(c)).toBe(true)
    await vi.advanceTimersByTimeAsync(101)
    expect(c.suspended).toBe(true)
    expect(d.answerable(c)).toBe(false)
    d.stop()
  })

  it('keeps the schedule on the steady clock, and suspends a card when the wall clock is set back an hour until the next read renews it', async () => {
    const d = await boot()
    const c = card(d, 'dr-7')
    c.draft.choice = 0
    vi.setSystemTime(Date.now() - 3_600_000)
    await d.send('dr-7')
    expect(c.suspended).toBe(true)
    expect(count('/api/decisions/dr-7/respond')).toBe(0)
    const lists = count('/api/me/decisions')
    await vi.advanceTimersByTimeAsync(10_000)
    expect(count('/api/me/decisions')).toBe(lists + 1)
    await answer('/api/decisions/dr-7', 200, open('dr-7', 1))
    expect(c.suspended).toBe(false)
    expect(d.answerable(c)).toBe(true)
    d.stop()
  })

  it('suspends a card when judged after the wall clock moved an hour on and the steady clock two seconds, with no timer fired', async () => {
    const d = await boot()
    const c = card(d, 'dr-7')
    c.draft.choice = 0
    expect(d.answerable(c)).toBe(true)
    steadySkew = 2_000
    vi.setSystemTime(Date.now() + 3_600_000)
    await d.send('dr-7')
    expect(c.suspended).toBe(true)
    expect(d.answerable(c)).toBe(false)
    expect(count('/api/decisions/dr-7/respond')).toBe(0)
    d.stop()
  })

  it('keeps a card fresh while neither clock moves oddly, and suspends it on both together just past 25 s', async () => {
    const d = await boot()
    const c = card(d, 'dr-7')
    steadySkew = 24_000
    vi.setSystemTime(Date.now() + 24_000)
    d.shown()
    expect(c.suspended).toBe(false)
    steadySkew = 25_001
    vi.setSystemTime(Date.now() + 1_001)
    d.shown()
    expect(c.suspended).toBe(true)
    d.stop()
  })

  it('suspends the list once its mark is 25 s old: rows as last read, no card opened from it, no card made answerable', async () => {
    const d = await boot(undefined, [['dr-7', 1], ['dr-8', 1], ['dr-9', 1], ['dr-10', 1], ['dr-11', 1]])
    d.close('dr-10')
    expect(card(d, 'dr-10').open).toBe(false)
    await vi.advanceTimersByTimeAsync(10_000)
    for (const id of ['dr-7', 'dr-8', 'dr-9']) await answer(`/api/decisions/${id}`, 200, open(id, 1))
    await answer('/api/me/decisions', 503, { error: { code: 'ledger_unavailable', message: 'the decisions could not be read' } })
    await vi.advanceTimersByTimeAsync(10_000)
    for (const id of ['dr-7', 'dr-8', 'dr-9']) await answer(`/api/decisions/${id}`, 200, open(id, 1))
    await answer('/api/me/decisions', 429, { error: { code: 'rate_limited', message: 'a list of yours is being read' } })
    await vi.advanceTimersByTimeAsync(5_001)
    expect(d.page.list.suspended).toBe(true)
    expect(d.page.list.entries.map((e) => e.id)).toEqual(['dr-7', 'dr-8', 'dr-9', 'dr-10', 'dr-11'])
    d.open('dr-11')
    expect(card(d, 'dr-11')).toBeUndefined()
    expect(d.answerable(card(d, 'dr-7'))).toBe(true)
    d.stop()
  })

  for (const [what, wall] of [
    ['on', 3_600_000],
    ['back', -3_600_000],
  ] as const) {
    const five: Array<[string, number]> = [['dr-7', 1], ['dr-8', 1], ['dr-9', 1], ['dr-10', 1], ['dr-11', 1]]
    it(`opens no card on Open when the list was read before the wall clock was set ${what} an hour and the steady clock moved two seconds`, async () => {
      const d = await boot(undefined, five)
      d.close('dr-10')
      steadySkew = 2_000
      vi.setSystemTime(Date.now() + wall)
      d.open('dr-11')
      expect(card(d, 'dr-11')).toBeUndefined()
      expect(count('/api/decisions/dr-11')).toBe(0)
      expect(d.page.list.suspended).toBe(true)
      d.stop()
    })

    it(`opens no card from a list reply whose read began before the wall clock was set ${what} an hour and the steady clock moved two seconds`, async () => {
      const d = await boot(undefined, five)
      d.close('dr-10')
      env.win.dispatchEvent(new Event('focus'))
      steadySkew = 2_000
      vi.setSystemTime(Date.now() + wall)
      await answer('/api/me/decisions', 200, list(...five, ['dr-12', 1]))
      expect(d.page.list.suspended).toBe(true)
      expect(d.page.cards.map((c) => c.id)).toEqual(['dr-7', 'dr-8', 'dr-9', 'dr-10'])
      expect(count('/api/decisions/dr-11') + count('/api/decisions/dr-12')).toBe(0)
      d.stop()
    })
  }

  it('opens no card on Open whose card has ended, though the list still holds it', async () => {
    const d = await boot(undefined, [['dr-7', 1], ['dr-8', 1]])
    env.win.dispatchEvent(new Event('focus'))
    await answer('/api/decisions/dr-7', 404, { error: { code: 'not_found', message: 'no such decision' } })
    await answer('/api/me/decisions', 200, list(['dr-7', 1], ['dr-8', 1]))
    expect(d.page.list.suspended).toBe(false)
    const n = count('/api/decisions/dr-7')
    d.open('dr-7')
    expect(count('/api/decisions/dr-7')).toBe(n)
    expect(card(d, 'dr-7')).toMatchObject({ end: 'gone' })
    d.stop()
  })
})

describe('the list and the cards', () => {
  it("opens the address's decision first, then the list's newest, four at most; Open refused at four and Close only with nothing unsent", async () => {
    const d = await boot('dr-3', [['dr-9', 1], ['dr-8', 1], ['dr-7', 1], ['dr-6', 1], ['dr-3', 1]])
    expect(d.page.cards.map((c) => c.id)).toEqual(['dr-3', 'dr-9', 'dr-8', 'dr-7'])
    expect(count('/api/decisions/dr-6')).toBe(0)
    d.open('dr-6')
    expect(card(d, 'dr-6')).toBeUndefined()
    card(d, 'dr-9').draft.text = 'kept'
    expect(d.closable(card(d, 'dr-9'))).toBe(false)
    d.close('dr-9')
    expect(card(d, 'dr-9').open).toBe(true)
    d.close('dr-8')
    expect(card(d, 'dr-8').open).toBe(false)
    d.open('dr-6')
    expect(card(d, 'dr-6').open).toBe(true)
    d.close('dr-6')
    // A closed card opened again is drawn as it was and read at once.
    d.open('dr-8')
    expect(card(d, 'dr-8').view?.rev).toBe(1)
    expect(out('/api/decisions/dr-8')).toHaveLength(1)
    d.stop()
  })

  it('says an empty list and a cut list as the reply says them', async () => {
    const d = await boot(undefined, [])
    expect(d.page.list).toMatchObject({ loaded: true, entries: [], cut: false })
    await vi.advanceTimersByTimeAsync(10_000)
    await answer('/api/me/decisions', 200, { ...list(['dr-7', 1]), cut: true })
    expect(d.page.list.cut).toBe(true)
    d.stop()
  })

  it('reads no list for a pass that is not a decide pass', async () => {
    const d = createDecide(env)
    d.start()
    await answer('/api/me', 200, { subject: 'op', grants: ['admin', 'host', 'decide'] })
    expect(d.page.notForIt).toBe(true)
    await vi.advanceTimersByTimeAsync(60_000)
    expect(reqs.map((r) => r.path)).toEqual(['/api/me'])
  })

  it('sends nothing and shows the page ended with no pass', async () => {
    store.value = ''
    const d = createDecide(env, 'dr-7')
    d.start()
    await d.whenIdle()
    expect(d.page.ended).toBe(true)
    expect(reqs).toHaveLength(0)
  })
})

describe('a stopped page and a closed card', () => {
  for (const at of ['/api/me', '/api/me/decisions']) {
    it(`opens no card and sends nothing more when ${at} is answered after the page stopped`, async () => {
      const d = createDecide(env, 'dr-7')
      d.start()
      if (at !== '/api/me') await answer('/api/me', 200, ME)
      const n = reqs.length
      out(at)[0]!.reply(200, at === '/api/me' ? ME : list(['dr-7', 1], ['dr-8', 1]))
      d.stop()
      await flush()
      await vi.advanceTimersByTimeAsync(60_000)
      env.win.dispatchEvent(new Event('focus'))
      expect(reqs).toHaveLength(n)
      expect(d.page.cards.map((c) => c.id)).toEqual(at === '/api/me' ? [] : ['dr-7'])
      expect(d.page.list.loaded).toBe(false)
    })
  }

  it('aborts every request on its way when the page stops', async () => {
    const d = await boot()
    env.win.dispatchEvent(new Event('focus'))
    card(d, 'dr-7').draft.choice = 0
    void d.send('dr-7')
    expect(reqs.filter((r) => !r.done)).toHaveLength(3)
    d.stop()
    expect(reqs.filter((r) => !r.done)).toHaveLength(0)
    await d.whenIdle()
  })

  it('has no more card reads in flight than open cards: four held, closed, and four others opened', async () => {
    const ids = ['dr-8', 'dr-7', 'dr-6', 'dr-5', 'dr-4', 'dr-3', 'dr-2', 'dr-1']
    const d = await boot(undefined, ids.map((id): [string, number] => [id, 1]))
    env.win.dispatchEvent(new Event('focus'))
    expect(cardReadsOut()).toBe(4)
    for (const id of ids.slice(0, 4)) d.close(id)
    for (const id of ids.slice(4)) d.open(id)
    expect(d.openCount()).toBe(4)
    expect(cardReadsOut()).toBe(4)
    d.stop()
  })

  for (const [what, status, body] of [
    ['a 404', 404, { error: { code: 'not_found', message: 'no such decision' } }],
    ['a final outcome', 200, { id: 'dr-7', rev: 3, own: [receipt('r1')], outcome: 'applied' }],
  ] as const) {
    it(`leaves a closed card as it was when ${what} to its read lands after it closed`, async () => {
      const d = await boot()
      const c = card(d, 'dr-7')
      env.win.dispatchEvent(new Event('focus'))
      out('/api/decisions/dr-7')[0]!.reply(status, body)
      d.close('dr-7')
      await flush()
      expect(c).toMatchObject({ open: false, end: '', view: { rev: 1, state: 'open' } })
      d.stop()
    })
  }

  for (const [what, status, body] of [
    ['404', 404, () => ({ error: { code: 'not_found', message: 'no such decision' } })],
    ['201 with a final outcome', 201, (id: string) => ({ receipt: receipt(id), decision: { id: 'dr-7', rev: 9, own: [receipt(id)], outcome: 'applied' } })],
  ] as const) {
    it(`leaves the card that replaced a dismissed one as it is, and reading, when the dismissed card's send lands ${what}`, async () => {
      const d = await boot()
      const old = card(d, 'dr-7')
      old.draft.choice = 0
      void d.send('dr-7')
      const held = out('/api/decisions/dr-7/respond')[0]!
      const id = JSON.parse(held.body!).responseId
      env.win.dispatchEvent(new Event('focus'))
      // The send's reply arrives before the dismissal, its body held: the dismissal's abort rejects nothing, and the reply goes on.
      const release = held.hold(status, body(id))
      await flush()
      await answer('/api/decisions/dr-7', 403, { error: { code: 'switchyard', message: '' } })
      expect(old.end).toBe('refused')
      d.dismiss('dr-7')
      // The list read the focus started, answered: the same decision, opened again as a new card.
      await answer('/api/me/decisions', 200, list(['dr-7', 2]))
      const c = card(d, 'dr-7')
      expect(c).not.toBe(old)
      await answer('/api/decisions/dr-7', 200, open('dr-7', 2))
      const kept = (x: typeof old) => ({ end: x.end, sent: x.sent, unknown: x.unknown, refused: x.refused, view: x.view })
      const before = kept(old)
      release()
      await flush()
      expect(held.aborted).toBe(true)
      expect(kept(old)).toEqual(before)
      expect(c).toMatchObject({ end: '', sent: '', unknown: '', refused: '', view: { rev: 2, state: 'open' } })
      const reads = count('/api/decisions/dr-7')
      for (let i = 1; i <= 3; i++) {
        await vi.advanceTimersByTimeAsync(10_000)
        expect(count('/api/decisions/dr-7')).toBe(reads + i)
        await answer('/api/decisions/dr-7', 200, open('dr-7', 2))
        for (const r of out('/api/me/decisions')) r.reply(200, list(['dr-7', 2]))
        await flush()
      }
      d.stop()
    })
  }

  it('leaves no timer once the page stops', async () => {
    const d = await boot()
    env.win.dispatchEvent(new Event('focus'))
    await answer('/api/decisions/dr-7', 404, { error: { code: 'not_found', message: '' } })
    d.dismiss('dr-7')
    d.stop()
    await flush()
    expect(vi.getTimerCount()).toBe(0)
  })
})

describe('a 401 and a 404', () => {
  for (const at of ['/api/me', '/api/me/decisions', '/api/decisions/dr-7', '/api/decisions/dr-7/respond']) {
    it(`ends the page at a 401 of ${at}: the pass it was sent with ended, every read stopped, every draft kept`, async () => {
      const d = createDecide(env, 'dr-7')
      d.start()
      if (at !== '/api/me') {
        await answer('/api/me', 200, ME)
        if (at !== '/api/me/decisions') {
          await answer('/api/me/decisions', 200, list(['dr-7', 1]))
          await answer('/api/decisions/dr-7', 200, open('dr-7', 1))
          card(d, 'dr-7').draft.choice = 1
          if (at.endsWith('/respond')) void d.send('dr-7')
          else env.win.dispatchEvent(new Event('focus'))
        }
      }
      await answer(at, 401, { error: { code: 'unauthorized', message: 'unauthorized' } })
      for (const r of reqs.filter((x) => !x.done)) r.reply(200, open('dr-7', 1))
      await d.whenIdle()
      expect(d.page.ended).toBe(true)
      expect(store).toEqual({ value: '', ended: true, refused: PASS })
      if (at !== '/api/me' && at !== '/api/me/decisions') expect(card(d, 'dr-7').draft.choice).toBe(1)
      const n = reqs.length
      await vi.advanceTimersByTimeAsync(60_000)
      env.win.dispatchEvent(new Event('focus'))
      expect(reqs).toHaveLength(n)
    })
  }

  /** The page ended at a 401, the pass it was sent with ended, and no request sent after it. */
  async function endedForGood(d: ReturnType<typeof createDecide>) {
    await d.whenIdle()
    expect(d.page.ended).toBe(true)
    expect(store).toEqual({ value: '', ended: true, refused: PASS })
    const n = reqs.length
    await vi.advanceTimersByTimeAsync(60_000)
    env.win.dispatchEvent(new Event('focus'))
    expect(reqs).toHaveLength(n)
  }

  it('ends the page at a 401 to a read that lands with the reply that ends its card', async () => {
    const d = await boot()
    card(d, 'dr-7').draft.choice = 0
    env.win.dispatchEvent(new Event('focus'))
    void d.send('dr-7')
    const id = JSON.parse(out('/api/decisions/dr-7/respond')[0]!.body!).responseId
    out('/api/decisions/dr-7/respond')[0]!.reply(201, { receipt: receipt(id), decision: { id: 'dr-7', rev: 2, own: [receipt(id)], outcome: 'applied' } })
    out('/api/decisions/dr-7')[0]!.reply(401, { error: { code: 'unauthorized', message: 'unauthorized' } })
    await endedForGood(d)
  })

  it('ends the page at a 401 to a send that a read settled first', async () => {
    const d = await boot()
    const c = card(d, 'dr-7')
    c.draft.choice = 0
    void d.send('dr-7')
    const id = JSON.parse(out('/api/decisions/dr-7/respond')[0]!.body!).responseId
    env.win.dispatchEvent(new Event('focus'))
    await answer('/api/decisions/dr-7', 200, { id: 'dr-7', rev: 2, own: [receipt(id)], outcome: 'waiting' })
    expect(c.sent).toBe('')
    await answer('/api/decisions/dr-7/respond', 401, { error: { code: 'unauthorized', message: 'unauthorized' } })
    await endedForGood(d)
  })

  it("ends the page at a 401 whose body is past its route's bound", async () => {
    const d = createDecide(env)
    d.start()
    await answer('/api/me', 200, ME)
    await answer('/api/me/decisions', 401, padded({ error: { code: 'unauthorized', message: 'unauthorized' } }, MAX_DECIDE_LIST + 1))
    await endedForGood(d)
  })

  it('closes a card at its 404 alone: its draft kept, the pass, the list and the other cards going on, and a late read drawing nothing', async () => {
    const d = await boot(undefined, [['dr-8', 1], ['dr-7', 1]])
    card(d, 'dr-8').draft.text = 'mine'
    env.win.dispatchEvent(new Event('focus'))
    await answer('/api/decisions/dr-8', 404, { error: { code: 'not_found', message: 'no such decision' } })
    expect(card(d, 'dr-8')).toMatchObject({ end: 'gone', draft: { text: 'mine' } })
    expect(d.answerable(card(d, 'dr-8'))).toBe(false)
    await answer('/api/decisions/dr-7', 200, open('dr-7', 1))
    await answer('/api/me/decisions', 200, list(['dr-8', 1], ['dr-7', 1]))
    expect(store.value).toBe(PASS)
    await vi.advanceTimersByTimeAsync(10_000)
    expect(out('/api/decisions/dr-8')).toHaveLength(0)
    expect(out('/api/decisions/dr-7')).toHaveLength(1)
    d.dismiss('dr-8')
    expect(card(d, 'dr-8')).toBeUndefined()
    d.stop()
  })
})

describe('the bound of each reply', () => {
  const routes = [
    ['/api/me', MAX_ME_REPLY],
    ['/api/me/decisions', MAX_DECIDE_LIST],
    ['/api/decisions/dr-7', MAX_DECIDE_VIEW],
    ['/api/decisions/dr-7/respond', MAX_DECIDE_REPLY],
  ] as const
  for (const [path, max] of routes) {
    for (const n of [max, max + 1]) {
      it(`${n > max ? 'changes nothing on' : 'takes'} a reply to ${path} of ${n > max ? 'one byte past' : 'exactly'} its bound`, async () => {
        const d = createDecide(env, 'dr-7')
        d.start()
        const taken = n <= max
        if (path === '/api/me') {
          await answer(path, 200, padded(ME, n))
          expect(d.page.me !== null).toBe(taken)
          return d.stop()
        }
        await answer('/api/me', 200, ME)
        if (path === '/api/me/decisions') {
          await answer(path, 200, padded(list(['dr-8', 1]), n))
          expect(d.page.list.loaded).toBe(taken)
          return d.stop()
        }
        await answer('/api/me/decisions', 200, list(['dr-7', 1]))
        if (path === '/api/decisions/dr-7') {
          await answer(path, 200, padded(open('dr-7', 1), n))
          expect(card(d, 'dr-7').view !== null).toBe(taken)
          return d.stop()
        }
        await answer('/api/decisions/dr-7', 200, open('dr-7', 1))
        card(d, 'dr-7').draft.choice = 0
        void d.send('dr-7')
        const id = JSON.parse(out(path)[0]!.body!).responseId
        await answer(path, 201, padded({ receipt: receipt(id) }, n))
        expect(card(d, 'dr-7').sent === '').toBe(taken)
        expect(card(d, 'dr-7').unknown).toBe(taken ? '' : NOT_KNOWN)
        d.stop()
      })
    }
  }
})

describe("a card's one order", () => {
  it('keeps a card answered once its answer is taken: the read begun before it aborted, and the open view of the rev before drawing nothing', async () => {
    const d = await boot()
    const c = card(d, 'dr-7')
    c.draft.choice = 0
    env.win.dispatchEvent(new Event('focus'))
    const before = out('/api/decisions/dr-7')[0]!
    await vi.advanceTimersByTimeAsync(1_000)
    void d.send('dr-7')
    const sent = JSON.parse(out('/api/decisions/dr-7/respond')[0]!.body!)
    await answer('/api/decisions/dr-7/respond', 201, { receipt: receipt(sent.responseId), decision: { id: 'dr-7', rev: 2, own: [receipt(sent.responseId)], outcome: 'waiting' } })
    expect(c.view).toMatchObject({ rev: 2, outcome: 'waiting' })
    expect(before.aborted).toBe(true)
    await answer('/api/decisions/dr-7', 200, open('dr-7', 1))
    expect(c.view?.rev).toBe(2)
    expect(c.view?.state).toBeUndefined()
    expect(d.answerable(c)).toBe(false)
    expect(c).toMatchObject({ freshAt: 0, suspended: true })
    d.stop()
  })

  it('drops a read of a lower rev and takes one of the same rev', async () => {
    const d = await boot(undefined, [['dr-7', 3]])
    const c = card(d, 'dr-7')
    env.win.dispatchEvent(new Event('focus'))
    await answer('/api/decisions/dr-7', 200, open('dr-7', 2, { question: 'older' }))
    expect(c.view?.question).toBe('May dr-7 go out?')
    env.win.dispatchEvent(new Event('focus'))
    await answer('/api/decisions/dr-7', 200, open('dr-7', 3, { question: 'same rev, read later' }))
    expect(c.view?.question).toBe('same rev, read later')
    d.stop()
  })

  for (const status of [200, 201]) {
    it(`draws nothing of a ${status} respond reply whose decision is at the rev shown, while its receipt settles the send`, async () => {
      const d = await boot(undefined, [['dr-7', 3]])
      const c = card(d, 'dr-7')
      c.draft.choice = 1
      void d.send('dr-7')
      const id = JSON.parse(out('/api/decisions/dr-7/respond')[0]!.body!).responseId
      await answer('/api/decisions/dr-7/respond', status, { receipt: receipt(id, { rev: 3 }), decision: open('dr-7', 3, { question: 'other words', own: [receipt(id, { rev: 3 })] }) })
      expect(c.view?.question).toBe('May dr-7 go out?')
      expect(c).toMatchObject({ sent: '', unknown: '', refused: '' })
      d.stop()
    })
  }

  it('aborts the read on its way when its own answer is applied, and keeps the applied view', async () => {
    const d = await boot()
    const c = card(d, 'dr-7')
    c.draft.choice = 0
    env.win.dispatchEvent(new Event('focus'))
    void d.send('dr-7')
    const id = JSON.parse(out('/api/decisions/dr-7/respond')[0]!.body!).responseId
    await answer('/api/decisions/dr-7/respond', 201, { receipt: receipt(id), decision: { id: 'dr-7', rev: 3, own: [receipt(id)], outcome: 'applied' } })
    expect(c.end).toBe('applied')
    expect(out('/api/decisions/dr-7')).toHaveLength(0)
    expect(c.view).toMatchObject({ rev: 3, outcome: 'applied' })
    d.stop()
  })

  it('stops reading a card at a final outcome a read brings', async () => {
    const d = await boot()
    env.win.dispatchEvent(new Event('focus'))
    await answer('/api/decisions/dr-7', 200, { id: 'dr-7', rev: 3, own: [receipt('r1')], outcome: 'not_used' })
    expect(card(d, 'dr-7').end).toBe('not_used')
    await vi.advanceTimersByTimeAsync(60_000)
    expect(out('/api/decisions/dr-7')).toHaveLength(0)
    d.stop()
  })
})

describe('what the page keeps of an answer', () => {
  it('sends the draft frozen under a new response id, and the same body again, byte for byte, after no definite answer', async () => {
    const d = await boot()
    const c = card(d, 'dr-7')
    Object.assign(c.draft, { choice: 1, text: '', rationale: 'too risky' })
    void d.send('dr-7')
    expect(c.sending).toBe(true)
    const first = out('/api/decisions/dr-7/respond')[0]!.body!
    expect(JSON.parse(first)).toEqual({ responseId: expect.stringMatching(/^[0-9a-f]{16}$/), choice: 1, rationale: 'too risky', expectedRev: 1 })
    out('/api/decisions/dr-7/respond')[0]!.fail()
    await flush()
    expect(c).toMatchObject({ unknown: NOT_KNOWN, sending: false, sent: first })
    c.draft = { text: '', rationale: '' }
    expect(d.closable(c)).toBe(false)
    c.draft = { choice: 1, text: '', rationale: 'edited meanwhile' }
    void d.send('dr-7')
    expect(out('/api/decisions/dr-7/respond')[0]!.body).toBe(first)
    await answer('/api/decisions/dr-7/respond', 503, { error: { code: 'decision_moved', message: '' } })
    expect(c.unknown).toBe(NOT_KNOWN)
    void d.send('dr-7')
    expect(out('/api/decisions/dr-7/respond')[0]!.body).toBe(first)
    const id = JSON.parse(first).responseId
    await answer('/api/decisions/dr-7/respond', 200, { receipt: { ...receipt(id), repeat: true }, decision: { id: 'dr-7', rev: 2, own: [receipt(id)], outcome: 'waiting' } })
    expect(c).toMatchObject({ unknown: '', sent: '', refused: '' })
    expect(c.draft.rationale).toBe('edited meanwhile')
    d.stop()
  })

  it('holds the answer through a 429, a 503, a 400 of an unknown code and a failed request: the same id and body each time', async () => {
    const d = await boot()
    const c = card(d, 'dr-7')
    c.draft.choice = 0
    void d.send('dr-7')
    const first = out('/api/decisions/dr-7/respond')[0]!.body!
    await answer('/api/decisions/dr-7/respond', 429, { error: { code: 'rate_limited', message: 'your requests are being worked' } })
    expect(c).toMatchObject({ sent: first, refused: '', unknown: 'The server is working on other requests of yours: send your answer to dr-7 again in a moment.' })
    for (const next of [
      (r: Req) => r.reply(503, { error: { code: 'ledger_unavailable', message: '' } }),
      (r: Req) => r.reply(400, { error: { code: 'invalid_request', message: 'this route takes no query' } }),
      (r: Req) => r.reply(409),
      (r: Req) => r.fail(),
    ]) {
      expect(d.answerable(c)).toBe(true)
      void d.send('dr-7')
      next(out('/api/decisions/dr-7/respond')[0]!)
      await flush()
      expect(c).toMatchObject({ sent: first, refused: '', unknown: NOT_KNOWN })
    }
    const bodies = reqs.filter((r) => r.path.endsWith('/respond')).map((r) => r.body)
    expect(bodies).toEqual(Array(5).fill(first))
    d.stop()
  })

  it('offers Send again for an answer held after a read shows only its outcome, and sends the same id', async () => {
    const d = await boot()
    const c = card(d, 'dr-7')
    c.draft.choice = 0
    void d.send('dr-7')
    const first = out('/api/decisions/dr-7/respond')[0]!.body!
    out('/api/decisions/dr-7/respond')[0]!.fail()
    await flush()
    env.win.dispatchEvent(new Event('focus'))
    await answer('/api/decisions/dr-7', 200, { id: 'dr-7', rev: 4, own: [receipt('older-than-the-eight-kept')], outcome: 'waiting' })
    expect(c.view?.state).toBeUndefined()
    expect(d.answerable(c)).toBe(true)
    void d.send('dr-7')
    expect(out('/api/decisions/dr-7/respond')[0]!.body).toBe(first)
    d.stop()
  })

  it('takes a new response id and the latest rev after a definite refusal', async () => {
    const d = await boot()
    const c = card(d, 'dr-7')
    c.draft.choice = 0
    void d.send('dr-7')
    const first = JSON.parse(out('/api/decisions/dr-7/respond')[0]!.body!)
    await answer('/api/decisions/dr-7/respond', 409, { error: { code: 'stale_decision', message: '' } })
    expect(c).toMatchObject({ sent: '', refused: 'dr-7 changed since you read it: read it again and answer.' })
    env.win.dispatchEvent(new Event('focus'))
    await answer('/api/decisions/dr-7', 200, open('dr-7', 2))
    void d.send('dr-7')
    const second = JSON.parse(out('/api/decisions/dr-7/respond')[0]!.body!)
    expect(second.responseId).not.toBe(first.responseId)
    expect(second.expectedRev).toBe(2)
    expect(c.refused).toBe('')
    d.stop()
  })

  it('keeps the draft and the answer held at a send answered 404, offers no Send again, and says it cannot be learned here', async () => {
    const d = await boot()
    const c = card(d, 'dr-7')
    c.draft.choice = 1
    void d.send('dr-7')
    const first = out('/api/decisions/dr-7/respond')[0]!.body!
    await answer('/api/decisions/dr-7/respond', 404, { error: { code: 'not_found', message: 'no such decision' } })
    expect(c).toMatchObject({ end: 'gone', sent: first, draft: { choice: 1 }, unknown: 'Whether your answer to dr-7 was recorded cannot be learned here.' })
    expect(d.answerable(c)).toBe(false)
    await d.send('dr-7')
    expect(count('/api/decisions/dr-7/respond')).toBe(1)
    d.stop()
  })

  it('says a held answer cannot be learned here once a read answers 404, and when its send then lands with no answer', async () => {
    const d = await boot()
    const c = card(d, 'dr-7')
    c.draft.choice = 1
    void d.send('dr-7')
    out('/api/decisions/dr-7/respond')[0]!.fail()
    await flush()
    expect(c.unknown).toBe(NOT_KNOWN)
    void d.send('dr-7')
    env.win.dispatchEvent(new Event('focus'))
    await answer('/api/decisions/dr-7', 404, { error: { code: 'not_found', message: 'no such decision' } })
    expect(c.unknown).toBe('Whether your answer to dr-7 was recorded cannot be learned here.')
    await answer('/api/decisions/dr-7/respond', 503, { error: { code: 'decision_moved', message: '' } })
    expect(c).toMatchObject({ end: 'gone', unknown: 'Whether your answer to dr-7 was recorded cannot be learned here.' })
    expect(d.answerable(c)).toBe(false)
    d.stop()
  })

  for (const outcome of ['applied', 'not_used'] as const) {
    for (const [reply, status, then] of [
      ['409 decision_answered', 409, 'released'],
      ['200 with its own receipt', 200, 'settled'],
    ] as const) {
      it(`offers Send again after a read shows ${outcome} with no receipt of the answer held: the same id and body, then ${reply} ${then} it`, async () => {
        const d = await boot()
        const c = card(d, 'dr-7')
        c.draft.choice = 0
        void d.send('dr-7')
        const first = out('/api/decisions/dr-7/respond')[0]!.body!
        const id = JSON.parse(first).responseId
        out('/api/decisions/dr-7/respond')[0]!.fail()
        await flush()
        env.win.dispatchEvent(new Event('focus'))
        await answer('/api/decisions/dr-7', 200, { id: 'dr-7', rev: 4, own: [receipt('another-tab')], outcome })
        expect(c).toMatchObject({ end: outcome, sent: first, unknown: NOT_KNOWN })
        expect(d.answerable(c)).toBe(true)
        void d.send('dr-7')
        expect(out('/api/decisions/dr-7/respond')[0]!.body).toBe(first)
        if (status === 409) {
          await answer('/api/decisions/dr-7/respond', 409, { error: { code: 'decision_answered', message: '' }, receipt: receipt(id, { status: 'refused', code: 'decision_answered' }) })
          expect(c).toMatchObject({ sent: '', unknown: '', refused: 'dr-7 was answered before your answer reached it.' })
        } else {
          await answer('/api/decisions/dr-7/respond', 200, { receipt: { ...receipt(id), repeat: true }, decision: { id: 'dr-7', rev: 5, own: [receipt(id)], outcome } })
          expect(c).toMatchObject({ sent: '', unknown: '', refused: '' })
        }
        expect(d.answerable(c)).toBe(false)
        d.stop()
      })
    }
  }

  it('holds an answer whose 2xx receipt names another response id, and sends the same id and body next', async () => {
    const d = await boot()
    const c = card(d, 'dr-7')
    c.draft.choice = 0
    void d.send('dr-7')
    const first = out('/api/decisions/dr-7/respond')[0]!.body!
    await answer('/api/decisions/dr-7/respond', 201, { receipt: receipt('another-tab'), decision: { id: 'dr-7', rev: 2, own: [receipt('another-tab')], outcome: 'waiting' } })
    expect(c).toMatchObject({ sent: first, unknown: NOT_KNOWN })
    expect(d.answerable(c)).toBe(true)
    void d.send('dr-7')
    expect(out('/api/decisions/dr-7/respond')[0]!.body).toBe(first)
    d.stop()
  })

  for (const status of [201, 200]) {
    it(`sends no new answer after a ${status} with its receipt and no view until a read made after it renews the view, read at once`, async () => {
      const d = await boot()
      const c = card(d, 'dr-7')
      c.draft.choice = 0
      void d.send('dr-7')
      const id = JSON.parse(out('/api/decisions/dr-7/respond')[0]!.body!).responseId
      await answer('/api/decisions/dr-7/respond', status, { receipt: status === 200 ? { ...receipt(id), repeat: true } : receipt(id) })
      expect(c).toMatchObject({ sent: '', unknown: '', refused: '', suspended: true, view: { rev: 1, state: 'open' } })
      expect(d.answerable(c)).toBe(false)
      expect(out('/api/decisions/dr-7')).toHaveLength(1)
      await d.send('dr-7')
      // A read that fails keeps it so.
      await answer('/api/decisions/dr-7', 503, { error: { code: 'ledger_unavailable', message: '' } })
      expect(d.answerable(c)).toBe(false)
      await d.send('dr-7')
      expect(count('/api/decisions/dr-7/respond')).toBe(1)
      env.win.dispatchEvent(new Event('focus'))
      await answer('/api/decisions/dr-7', 200, open('dr-7', 2, { own: [receipt(id)] }))
      expect(d.answerable(c)).toBe(true)
      d.stop()
    })
  }

  it('has one answer on its way at a time for the whole page: a second card sends only once the first has an outcome', async () => {
    const d = await boot(undefined, [['dr-8', 1], ['dr-7', 1]])
    card(d, 'dr-7').draft.choice = 0
    card(d, 'dr-8').draft.choice = 1
    void d.send('dr-7')
    expect(d.answerable(card(d, 'dr-8'))).toBe(false)
    await d.send('dr-8')
    const posts = () => reqs.filter((r) => r.path.endsWith('/respond'))
    expect(posts().map((r) => r.path)).toEqual(['/api/decisions/dr-7/respond'])
    out('/api/decisions/dr-7/respond')[0]!.fail()
    await flush()
    expect(d.answerable(card(d, 'dr-8'))).toBe(true)
    void d.send('dr-8')
    expect(posts().map((r) => r.path)).toEqual(['/api/decisions/dr-7/respond', '/api/decisions/dr-8/respond'])
    expect(d.answerable(card(d, 'dr-7'))).toBe(false)
    d.stop()
  })

  it("settles an unknown send by its own receipt in a read, and never by another response id's", async () => {
    const d = await boot()
    const c = card(d, 'dr-7')
    c.draft.choice = 0
    void d.send('dr-7')
    const id = JSON.parse(out('/api/decisions/dr-7/respond')[0]!.body!).responseId
    await vi.advanceTimersByTimeAsync(10_000)
    expect(c.unknown).toBe(NOT_KNOWN)
    await answer('/api/decisions/dr-7', 200, open('dr-7', 1, { own: [receipt('other-tab', { status: 'refused', code: 'decision_answered' })] }))
    expect(c.sent).not.toBe('')
    env.win.dispatchEvent(new Event('focus'))
    await answer('/api/decisions/dr-7', 200, { id: 'dr-7', rev: 2, own: [receipt('other-tab'), receipt(id, { status: 'refused', code: 'decision_answered' })], outcome: 'refused', outcomeCode: 'decision_answered' })
    expect(c).toMatchObject({ sent: '', unknown: '', refused: 'dr-7 was answered before your answer reached it.' })
    d.stop()
  })
})

describe('what a read may bring', () => {
  for (const [what, bad] of [
    ['options that are not a list', { options: 'Approve' }],
    ['an option that is not an object', { options: ['Approve'] }],
    ['an option without a text label', { options: [{ label: 7 }] }],
    ['a numeric createdAt', { createdAt: 1_762_905_600_000 }],
    ['a receipt that is not an object', { own: [null] }],
  ] as const) {
    it(`takes no view with ${what}: the card as it was, and a held answer's words still shown`, async () => {
      const d = await boot()
      const c = card(d, 'dr-7')
      c.draft.choice = 0
      void d.send('dr-7')
      out('/api/decisions/dr-7/respond')[0]!.fail()
      await flush()
      const view = c.view
      env.win.dispatchEvent(new Event('focus'))
      await answer('/api/decisions/dr-7', 200, open('dr-7', 2, bad as unknown as Partial<DecideView>))
      expect(c.view).toBe(view)
      expect(c).toMatchObject({ unknown: NOT_KNOWN, failed: false, end: '', draft: { choice: 0 } })
      d.stop()
    })
  }

  it('says nothing of a read its card aborted: closed and opened again while its first read is out, no "could not be read"', async () => {
    const d = createDecide(env)
    d.start()
    await answer('/api/me', 200, ME)
    await answer('/api/me/decisions', 200, list(['dr-7', 1]))
    const first = out('/api/decisions/dr-7')[0]!
    d.close('dr-7')
    d.open('dr-7')
    await flush()
    expect(first.aborted).toBe(true)
    expect(card(d, 'dr-7')).toMatchObject({ open: true, failed: false, view: null })
    expect(out('/api/decisions/dr-7')).toHaveLength(1)
    d.stop()
  })
})
