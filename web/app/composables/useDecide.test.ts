import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createDecide, type DecideEnv } from './useDecide'
import type { DecideReceipt, DecideView } from '~/utils/protocol'

// The decide page's client (docs/plans/decide-grant.md, "web/app"), on vitest's fake clock over a fake fetch whose replies the test
// gives one by one: the four routes alone, the pass in the header and in no URL (checked after every test); the list's and each
// card's reads one at a time on their own schedules; the freshness window; the one order a card's replies pass; and what the page
// keeps of an answer.

const PASS = 'pass.of-ana_1'
type Req = { path: string; method: string; headers: Record<string, string>; body?: string; done: boolean; aborted: boolean; reply: (status: number, body?: unknown) => void; fail: () => void }

let reqs: Req[]
let env: DecideEnv & { doc: EventTarget & { visibilityState: string }; win: EventTarget }
let store: { value: string; ended: boolean }

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
        headers: init.headers as Record<string, string>,
        body: init.body as string | undefined,
        done: false,
        aborted: false,
        reply(status, body) {
          r.done = true
          const bytes = new TextEncoder().encode(body === undefined ? '' : JSON.stringify(body))
          resolve({ status, arrayBuffer: async () => bytes.buffer } as unknown as Response)
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
    pass: { get: () => store.value, end: () => Object.assign(store, { value: '', ended: true }) },
    base: '',
    doc: Object.assign(new EventTarget(), { visibilityState: 'visible' }),
    win: new EventTarget() as unknown as Window,
  } as typeof env
})

afterEach(() => {
  // Every request: one of the four routes, the pass in its Authorization header, the pass in no URL.
  for (const r of reqs) {
    expect(ROUTES.some((re) => re.test(r.path)), r.path).toBe(true)
    expect(r.headers.Authorization).toBe(`Bearer ${PASS}`)
    expect(r.path).not.toContain(PASS)
  }
  vi.useRealTimers()
})

const flush = () => vi.advanceTimersByTimeAsync(0)
/** The requests to path not yet answered, oldest first. */
const out = (path: string) => reqs.filter((r) => r.path === path && !r.done)
const count = (path: string) => reqs.filter((r) => r.path === path).length
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

/** A page signed in, its list answered with `entries`, and each card it opened read at its rev in the list. */
async function boot(did?: string, entries: Array<[string, number]> = [['dr-7', 1]]) {
  const d = createDecide(env, did)
  d.start()
  await answer('/api/me', 200, { subject: 'ana', name: 'Ana', grants: ['decide'] })
  await answer('/api/me/decisions', 200, list(...entries))
  for (const c of [...d.page.cards]) await answer(`/api/decisions/${c.id}`, 200, open(c.id, entries.find(([id]) => id === c.id)?.[1] ?? 1))
  return d
}
const card = (d: ReturnType<typeof createDecide>, id: string) => d.page.cards.find((c) => c.id === id)!

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

  it("reads a card at once when the list holds it at another rev, and once when it left the list", async () => {
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

  it("says an empty list and a cut list as the reply says them", async () => {
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

describe('a 401 and a 404', () => {
  for (const at of ['/api/me', '/api/me/decisions', '/api/decisions/dr-7', '/api/decisions/dr-7/respond']) {
    it(`ends the page at a 401 of ${at}: the pass emptied, every read stopped, every draft kept`, async () => {
      const d = createDecide(env, 'dr-7')
      d.start()
      if (at !== '/api/me') {
        await answer('/api/me', 200, { subject: 'ana', name: 'Ana', grants: ['decide'] })
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
      expect(store).toEqual({ value: '', ended: true })
      if (at !== '/api/me' && at !== '/api/me/decisions') expect(card(d, 'dr-7').draft.choice).toBe(1)
      const n = reqs.length
      await vi.advanceTimersByTimeAsync(60_000)
      env.win.dispatchEvent(new Event('focus'))
      expect(reqs).toHaveLength(n)
    })
  }

  it("closes a card at its 404 alone: its draft kept, the pass, the list and the other cards going on, and a late read drawing nothing", async () => {
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

describe("a card's one order", () => {
  it('keeps a card answered when a read begun before the answer returns after it with the open view', async () => {
    const d = await boot()
    const c = card(d, 'dr-7')
    c.draft.choice = 0
    env.win.dispatchEvent(new Event('focus'))
    const fresh = c.freshAt
    await vi.advanceTimersByTimeAsync(1_000)
    void d.send('dr-7')
    const sent = JSON.parse(out('/api/decisions/dr-7/respond')[0]!.body!)
    await answer('/api/decisions/dr-7/respond', 201, { receipt: receipt(sent.responseId), decision: { id: 'dr-7', rev: 2, own: [receipt(sent.responseId)], outcome: 'waiting' } })
    expect(c.view).toMatchObject({ rev: 2, outcome: 'waiting' })
    await answer('/api/decisions/dr-7', 200, open('dr-7', 1))
    expect(c.view?.rev).toBe(2)
    expect(c.view?.state).toBeUndefined()
    expect(d.answerable(c)).toBe(false)
    expect(c.freshAt).toBe(fresh)
    d.stop()
  })

  it('drops a read of a lower rev, takes one of the same rev, and drops a respond reply of the rev shown', async () => {
    const d = await boot(undefined, [['dr-7', 3]])
    const c = card(d, 'dr-7')
    env.win.dispatchEvent(new Event('focus'))
    await answer('/api/decisions/dr-7', 200, open('dr-7', 2, { question: 'older' }))
    expect(c.view?.question).toBe('May dr-7 go out?')
    env.win.dispatchEvent(new Event('focus'))
    await answer('/api/decisions/dr-7', 200, open('dr-7', 3, { question: 'same rev, read later' }))
    expect(c.view?.question).toBe('same rev, read later')
    c.draft.choice = 1
    void d.send('dr-7')
    const sent = JSON.parse(out('/api/decisions/dr-7/respond')[0]!.body!)
    await answer('/api/decisions/dr-7/respond', 409, { error: { code: 'stale_decision', message: '' }, receipt: receipt(sent.responseId, { status: 'refused', code: 'stale_decision', rev: 3 }) })
    expect(c.view?.question).toBe('same rev, read later')
    d.stop()
  })

  it('draws nothing of a read that returns after its own answer was applied, whatever its rev', async () => {
    const d = await boot()
    const c = card(d, 'dr-7')
    c.draft.choice = 0
    env.win.dispatchEvent(new Event('focus'))
    void d.send('dr-7')
    const id = JSON.parse(out('/api/decisions/dr-7/respond')[0]!.body!).responseId
    await answer('/api/decisions/dr-7/respond', 201, { receipt: receipt(id), decision: { id: 'dr-7', rev: 3, own: [receipt(id)], outcome: 'applied' } })
    expect(c.end).toBe('applied')
    await answer('/api/decisions/dr-7', 200, open('dr-7', 4))
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
    expect(c).toMatchObject({ unknown: true, sending: false, sent: first })
    c.draft = { text: '', rationale: '' }
    expect(d.closable(c)).toBe(false)
    c.draft = { choice: 1, text: '', rationale: 'edited meanwhile' }
    void d.send('dr-7')
    expect(out('/api/decisions/dr-7/respond')[0]!.body).toBe(first)
    await answer('/api/decisions/dr-7/respond', 503, { error: { code: 'decision_moved', message: '' } })
    expect(c.unknown).toBe(true)
    void d.send('dr-7')
    expect(out('/api/decisions/dr-7/respond')[0]!.body).toBe(first)
    const id = JSON.parse(first).responseId
    await answer('/api/decisions/dr-7/respond', 200, { receipt: { ...receipt(id), repeat: true }, decision: { id: 'dr-7', rev: 2, own: [receipt(id)], outcome: 'waiting' } })
    expect(c).toMatchObject({ unknown: false, sent: '', refused: '' })
    expect(c.draft.rationale).toBe('edited meanwhile')
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

  it("settles an unknown send by its own receipt in a read, and never by another response id's", async () => {
    const d = await boot()
    const c = card(d, 'dr-7')
    c.draft.choice = 0
    void d.send('dr-7')
    const id = JSON.parse(out('/api/decisions/dr-7/respond')[0]!.body!).responseId
    await vi.advanceTimersByTimeAsync(10_000)
    expect(c.unknown).toBe(true)
    await answer('/api/decisions/dr-7', 200, open('dr-7', 1, { own: [receipt('other-tab', { status: 'refused', code: 'decision_answered' })] }))
    expect(c.sent).not.toBe('')
    env.win.dispatchEvent(new Event('focus'))
    await answer('/api/decisions/dr-7', 200, { id: 'dr-7', rev: 2, own: [receipt('other-tab'), receipt(id, { status: 'refused', code: 'decision_answered' })], outcome: 'refused', outcomeCode: 'decision_answered' })
    expect(c).toMatchObject({ sent: '', unknown: false, refused: 'dr-7 was answered before your answer reached it.' })
    d.stop()
  })
})
