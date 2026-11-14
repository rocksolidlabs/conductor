import { afterEach, describe, expect, it, vi } from 'vitest'
import { computed, effectScope, nextTick, reactive, toRef, watch } from 'vue'
import type { After, CapacityQuery, CapacityView } from '../utils/capacity'
import { useAccounts } from './useAccounts'
import { useWorkbenchToken } from './useWorkbenchToken'
import { CapacityLive, READ_DEADLINE_MS, VIEW_DEADLINE_MS, useCapacity, type CapacityReads } from './useCapacity'

// The capacity store (docs/plans/capacity.md, PR-5): one fetch per question at a time, a fetch again on its key's `capacity` event,
// a read never sent while the view's nextReadAfter is ahead of the clock, and each answer the question's that asked it.

function view(agent: string, account: string, over: Partial<CapacityView> = {}): CapacityView {
  return { agent: { id: agent }, model: { chosen: '' }, account: { id: account }, host: { kind: 'native' }, state: 'never_read', windows: [], credits: [], fresh: 'none', ...over }
}

/** A server whose answers the test hands out one by one: every ask waits until the test answers it. */
function fakeServer() {
  const gets: Array<{ q: CapacityQuery; signal: AbortSignal; answer: (v: CapacityView) => void; fail: (e: Error) => void }> = []
  const posts: Array<{ q: CapacityQuery; check: boolean; answer: (v: CapacityView) => void; fail: (e: Error) => void }> = []
  const reads: CapacityReads = {
    view: (q, signal) => new Promise((answer, fail) => gets.push({ q, signal, answer, fail })),
    read: (q, check) => new Promise((answer, fail) => posts.push({ q, check, answer, fail })),
  }
  return { gets, posts, reads }
}

const settle = async () => {
  for (let i = 0; i < 5; i++) await new Promise((r) => setTimeout(r, 0))
}

describe('CapacityLive', () => {
  it('fetches a question once at a time, and once more for an event that came while it was out', async () => {
    const srv = fakeServer()
    const live = new CapacityLive()
    live.attach(srv.reads)
    const q = { agent: 'claude' }
    void live.watch(q)
    void live.watch(q)
    expect(srv.gets).toHaveLength(1)
    // The event names the key while the fetch is out: one more fetch, after it, never two at once.
    live.event({ agent: 'claude', account: 'default', state: 'ok', readAt: '2026-11-12T10:00:00Z' })
    live.event({ agent: 'claude', account: 'default', state: 'ok', readAt: '2026-11-12T10:00:01Z' })
    expect(srv.gets).toHaveLength(1)
    srv.gets[0]!.answer(view('claude', 'default'))
    await settle()
    expect(srv.gets).toHaveLength(2)
    srv.gets[1]!.answer(view('claude', 'default', { state: 'ok' }))
    await settle()
    expect(srv.gets).toHaveLength(2)
    expect(live.entry(q).view?.state).toBe('ok')
  })

  it("fetches again on its key's event only: another agent's or another account's moves nothing", async () => {
    const srv = fakeServer()
    const live = new CapacityLive()
    live.attach(srv.reads)
    void live.watch({ agent: 'claude', account: 'a-work' })
    srv.gets[0]!.answer(view('claude', 'a-work'))
    await settle()
    live.event({ agent: 'codex', account: 'a-work', state: 'ok', readAt: 'x' })
    live.event({ agent: 'claude', account: 'default', state: 'ok', readAt: 'x' })
    live.event({ nonsense: true })
    expect(srv.gets).toHaveLength(1)
    live.event({ agent: 'claude', account: 'a-work', state: 'ok', readAt: 'x' })
    expect(srv.gets).toHaveLength(2)
    // A question with no account follows the account its view resolved.
    void live.watch({ agent: 'codex' })
    srv.gets[2]!.answer(view('codex', 'a-codex'))
    await settle()
    live.event({ agent: 'codex', account: 'default', state: 'ok', readAt: 'x' })
    expect(srv.gets).toHaveLength(3)
    live.event({ agent: 'codex', account: 'a-codex', state: 'ok', readAt: 'x' })
    expect(srv.gets).toHaveLength(4)
  })

  it('fetches nothing for a question no page shows, and every shown one when the stream comes back', async () => {
    const srv = fakeServer()
    const live = new CapacityLive()
    live.attach(srv.reads)
    const shown = { agent: 'claude' }
    const gone = { agent: 'codex' }
    void live.watch(shown)
    void live.watch(gone)
    srv.gets[0]!.answer(view('claude', 'default'))
    srv.gets[1]!.answer(view('codex', 'default'))
    await settle()
    live.unwatch(gone)
    expect(live.entry(gone).view).toBeNull()
    live.event({ agent: 'codex', account: 'default', state: 'ok', readAt: 'x' })
    expect(srv.gets).toHaveLength(2)
    live.snapshot()
    expect(srv.gets.map((g) => g.q.agent)).toEqual(['claude', 'codex', 'claude'])
  })

  it("sends no read while nextReadAfter is ahead of the clock, and one once it has passed", async () => {
    const srv = fakeServer()
    let now = Date.parse('2026-11-12T10:00:00Z')
    const live = new CapacityLive(() => now)
    live.attach(srv.reads)
    const q = { agent: 'claude', account: 'a-work' }
    void live.watch(q)
    srv.gets[0]!.answer(view('claude', 'a-work', { state: 'ok', nextReadAfter: '2026-11-12T10:05:00Z' }))
    await settle()
    expect(await live.read(q)).toBe(false)
    expect(srv.posts).toHaveLength(0)
    now = Date.parse('2026-11-12T10:05:00Z')
    const sent = live.read(q, true)
    expect(srv.posts).toHaveLength(1)
    expect(srv.posts[0]).toMatchObject({ q, check: true })
    // One read of a question at a time.
    expect(await live.read(q)).toBe(false)
    expect(live.entry(q).reading).toBe(true)
    srv.posts[0]!.answer(view('claude', 'a-work', { state: 'reading' }))
    await settle()
    // The read's own answer is a plain session's: the question's view is fetched after it, reading nothing.
    expect(srv.gets).toHaveLength(2)
    srv.gets[1]!.answer(view('claude', 'a-work', { state: 'reading' }))
    expect(await sent).toBe(true)
    expect(live.entry(q).reading).toBe(false)
    expect(live.entry(q).view?.state).toBe('reading')
  })

  it("says a read's refusal in the server's words, and shows the view fetched after it", async () => {
    const srv = fakeServer()
    const live = new CapacityLive()
    live.attach(srv.reads)
    const q = { agent: 'claude' }
    void live.watch(q)
    const first = srv.gets[0]!
    void live.read(q, true)
    srv.posts[0]!.fail(new Error('another server over this data directory reads this agent usage'))
    await settle()
    expect(live.entry(q).error).toBe('another server over this data directory reads this agent usage')
    // The fetch after the read was owed behind the first one; the first, answered last and older, never replaces it.
    first.answer(view('claude', 'default', { state: 'never_read' }))
    await settle()
    srv.gets[1]!.answer(view('claude', 'default', { state: 'error' }))
    await settle()
    expect(live.entry(q).view?.state).toBe('error')
  })

  it('forgets what it knew for another token, and drops an answer on its way for the old one', async () => {
    const srv = fakeServer()
    const live = new CapacityLive()
    live.attach(srv.reads)
    const q = { agent: 'claude' }
    void live.watch(q)
    live.forget()
    srv.gets[0]!.answer(view('claude', 'a-secret', { state: 'ok' }))
    await settle()
    expect(live.entry(q).view).toBeNull()
  })

  it("answers a second page's watch of a question once the GET it waits behind has landed, so its opening sees the view", async () => {
    const srv = fakeServer()
    const live = new CapacityLive()
    live.attach(srv.reads)
    const q = { agent: 'claude' }
    void live.watch(q)
    let landed = false
    const second = live.watch(q).then(() => (landed = true))
    srv.gets[0]!.answer(view('claude', 'default'))
    await settle()
    expect(srv.gets).toHaveLength(2)
    expect(landed).toBe(false)
    srv.gets[1]!.answer(view('claude', 'default', { state: 'ok' }))
    await second
    expect(live.entry(q).view?.state).toBe('ok')
  })

  it('sends no read for a question no longer shown when its GET answers, and keeps no slot for it', async () => {
    const srv = fakeServer()
    const live = new CapacityLive()
    live.attach(srv.reads)
    const q = { agent: 'claude', account: 'a-work' }
    // The panel's opening: watch, then read once the view is here. The dialog closes while the GET is out.
    const opening = live.watch(q).then(() => live.read(q))
    live.unwatch(q)
    srv.gets[0]!.answer(view('claude', 'a-work', { state: 'never_read' }))
    await settle()
    expect(srv.posts).toHaveLength(0)
    expect(srv.gets).toHaveLength(1)
    expect(slots(live)).toBe(0)
    expect(await opening).toBe(false)
  })

  it('lets a GET that never answers go at its deadline: the next event asks again and the panel recovers', async () => {
    const srv = fakeServer()
    const timers = fakeTimers()
    const live = new CapacityLive(Date.now, timers.after)
    live.attach(srv.reads)
    const q = { agent: 'claude' }
    void live.watch(q)
    expect(timers.pending()).toEqual([VIEW_DEADLINE_MS])
    timers.fire(VIEW_DEADLINE_MS)
    await settle()
    expect(live.entry(q).error).toBe(`The server did not answer within ${VIEW_DEADLINE_MS / 1000} s`)
    expect(srv.gets[0]!.signal.aborted).toBe(true)
    live.event({ agent: 'claude', account: 'default' })
    expect(srv.gets).toHaveLength(2)
    srv.gets[1]!.answer(view('claude', 'default', { state: 'ok' }))
    await settle()
    expect(live.entry(q)).toMatchObject({ error: '', view: { state: 'ok' } })
    // The first GET's late answer is nobody's.
    srv.gets[0]!.answer(view('claude', 'default', { state: 'error' }))
    await settle()
    expect(live.entry(q).view?.state).toBe('ok')
  })

  it("gives a read a deadline above the server's own for a usage read, and lets it go there", async () => {
    const srv = fakeServer()
    const timers = fakeTimers()
    const live = new CapacityLive(Date.now, timers.after)
    live.attach(srv.reads)
    const q = { agent: 'claude' }
    void live.watch(q)
    srv.gets[0]!.answer(view('claude', 'default'))
    await settle()
    void live.read(q)
    expect(timers.pending()).toEqual([READ_DEADLINE_MS])
    expect(READ_DEADLINE_MS).toBeGreaterThan(90_000)
    timers.fire(READ_DEADLINE_MS)
    await settle()
    expect(live.entry(q)).toMatchObject({ reading: false, error: `The server did not answer the read within ${READ_DEADLINE_MS / 1000} s` })
    expect(srv.gets).toHaveLength(2)
  })

  it("asks the server's view again when an open reading goes stale and when a window resets, and never reads", async () => {
    const srv = fakeServer()
    let now = Date.parse('2026-11-12T10:00:00Z')
    const live = new CapacityLive(() => now)
    live.attach(srv.reads)
    const q = { agent: 'claude', account: 'a-work' }
    void live.watch(q)
    const reset = '2026-11-12T10:20:00Z'
    const window = { source: 'provider' as const, kind: 'session', name: '5-hour', percent: 30, resetsAt: reset }
    srv.gets[0]!.answer(view('claude', 'a-work', { state: 'ok', readAt: '2026-11-12T10:00:00Z', fresh: 'fresh', windows: [window] }))
    await settle()
    now = Date.parse('2026-11-12T10:14:59Z')
    live.age()
    expect(srv.gets).toHaveLength(1)
    // Fifteen minutes after the read: stale, by the server's view asked again.
    now = Date.parse('2026-11-12T10:15:00Z')
    live.age()
    live.age()
    expect(srv.gets).toHaveLength(2)
    srv.gets[1]!.answer(view('claude', 'a-work', { state: 'ok', readAt: '2026-11-12T10:00:00Z', fresh: 'stale', windows: [window] }))
    await settle()
    expect(live.entry(q).view?.fresh).toBe('stale')
    // The window's reset: a reset has passed.
    now = Date.parse(reset)
    live.age()
    expect(srv.gets).toHaveLength(3)
    srv.gets[2]!.answer(view('claude', 'a-work', { state: 'ok', readAt: '2026-11-12T10:00:00Z', fresh: 'reset_passed', windows: [window] }))
    await settle()
    expect(live.entry(q).view?.fresh).toBe('reset_passed')
    now = Date.parse('2026-11-12T12:00:00Z')
    live.age()
    expect(srv.gets).toHaveLength(3)
    expect(srv.posts).toHaveLength(0)
  })

  it("asks again at the next tick when the GET for an open reading's boundary failed, until the panel turns stale, and never reads", async () => {
    const srv = fakeServer()
    let now = Date.parse('2026-11-12T10:00:00Z')
    const live = new CapacityLive(() => now)
    live.attach(srv.reads)
    const q = { agent: 'claude', account: 'a-work' }
    void live.watch(q)
    srv.gets[0]!.answer(view('claude', 'a-work', { state: 'ok', readAt: '2026-11-12T10:00:00Z', fresh: 'fresh' }))
    await settle()
    now = Date.parse('2026-11-12T10:15:00Z')
    live.age()
    expect(srv.gets).toHaveLength(2)
    // One GET for the boundary at a time: a tick while it is out asks nothing more.
    live.age()
    expect(srv.gets).toHaveLength(2)
    srv.gets[1]!.fail(new Error('Cannot reach the server'))
    await settle()
    expect(live.entry(q).view?.fresh).toBe('fresh')
    now = Date.parse('2026-11-12T10:15:30Z')
    live.age()
    expect(srv.gets).toHaveLength(3)
    srv.gets[2]!.answer(view('claude', 'a-work', { state: 'ok', readAt: '2026-11-12T10:00:00Z', fresh: 'stale' }))
    await settle()
    expect(live.entry(q).view?.fresh).toBe('stale')
    now = Date.parse('2026-11-12T10:16:00Z')
    live.age()
    expect(srv.gets).toHaveLength(3)
    expect(srv.posts).toHaveLength(0)
  })

  it("asks again at a later tick when the GET for an open reading's boundary answered still fresh (the server's clock behind), and never reads", async () => {
    const srv = fakeServer()
    let now = Date.parse('2026-11-12T10:00:00Z')
    const live = new CapacityLive(() => now)
    live.attach(srv.reads)
    const q = { agent: 'claude', account: 'a-work' }
    void live.watch(q)
    const fresh = view('claude', 'a-work', { state: 'ok', readAt: '2026-11-12T10:00:00Z', fresh: 'fresh' })
    srv.gets[0]!.answer(fresh)
    await settle()
    now = Date.parse('2026-11-12T10:15:00Z')
    live.age()
    expect(srv.gets).toHaveLength(2)
    srv.gets[1]!.answer({ ...fresh })
    await settle()
    expect(live.entry(q).view?.fresh).toBe('fresh')
    now = Date.parse('2026-11-12T10:15:30Z')
    live.age()
    expect(srv.gets).toHaveLength(3)
    srv.gets[2]!.answer(view('claude', 'a-work', { state: 'ok', readAt: '2026-11-12T10:00:00Z', fresh: 'stale' }))
    await settle()
    expect(live.entry(q).view?.fresh).toBe('stale')
    live.age()
    expect(srv.gets).toHaveLength(3)
    expect(srv.posts).toHaveLength(0)
  })
})

/** The slots a store keeps, shown or not. */
const slots = (live: CapacityLive) => (live as unknown as { slots: Map<string, unknown> }).slots.size

/** A clock for deadlines the test fires itself. */
function fakeTimers() {
  const set: Array<{ ms: number; fn: () => void; live: boolean }> = []
  const after: After = (ms, fn) => {
    const t = { ms, fn, live: true }
    set.push(t)
    return () => (t.live = false)
  }
  return {
    after,
    pending: () => set.filter((t) => t.live).map((t) => t.ms),
    fire: (ms: number) => {
      for (const t of set.filter((x) => x.live && x.ms === ms)) {
        t.live = false
        t.fn()
      }
    },
  }
}

// The token watched while a page shows the stores: another one, with a reply on its way for the old one, empties both, the late
// reply is dropped, and the accounts are read again. The composables run in an effect scope, as a mounted component runs them, over
// the app's auto-imports stubbed and the workbench token's own store; useState writes into the Nuxt app's payload as Nuxt's does, and
// no key of it holds a token at any step (the state Nuxt writes into the tab's storage at a reload it makes itself).
describe('another workbench token while a page shows capacity and the accounts', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('empties both stores, drops the replies on their way for the old token, reads the accounts again, and keeps no token in the state', async () => {
    const tokens = ['the-first-token-0123', 'the-second-token-0123']
    const app = { payload: { state: reactive({}) as Record<string, unknown> } }
    const held: Array<{ path: string; answer: (v: unknown) => void }> = []
    vi.stubGlobal('useNuxtApp', () => app)
    vi.stubGlobal('useState', (key: string, init: () => unknown) => {
      const r = toRef(app.payload.state, `$s${key}`)
      if (r.value === undefined) r.value = init()
      return r
    })
    vi.stubGlobal('computed', computed)
    vi.stubGlobal('watch', watch)
    vi.stubGlobal('useWorkbenchToken', useWorkbenchToken)
    vi.stubGlobal('useApi', () => ({ request: (path: string) => new Promise((answer) => held.push({ path, answer })) }))
    const noToken = () => {
      const text = JSON.stringify(app.payload.state)
      for (const t of tokens) expect(text).not.toContain(t)
    }
    const admin = useWorkbenchToken()
    admin.set(tokens[0]!)
    const scope = effectScope()
    const { cap, acc } = scope.run(() => ({ cap: useCapacity(), acc: useAccounts() }))!
    const q = { agent: 'claude', account: 'a-work' }
    void cap.store.watch(q)
    const loading = acc.store.load()
    expect(held.map((h) => h.path)).toEqual(['/api/capacity', '/api/accounts'])
    noToken()
    // The same token saved again is no change: nothing is forgotten or read again.
    admin.set(tokens[0]!)
    await nextTick()
    expect(held).toHaveLength(2)
    admin.set(tokens[1]!)
    await nextTick()
    noToken()
    held[0]!.answer(view('claude', 'a-work', { state: 'ok', account: { id: 'a-work', label: 'Work' } }))
    held[1]!.answer({ owner: true, rev: 3, accounts: [{ id: 'a-work', adapter: 'claude', label: 'Work', folder: '/home/first/.claude-work', createdAt: '2026-11-12T10:00:00Z' }], adapters: [] })
    await loading
    await settle()
    expect(cap.entry(q)).toMatchObject({ view: null, error: '' })
    expect(acc.doc.value).toBeNull()
    expect(acc.available.value).toBeNull()
    expect(held.map((h) => h.path)).toEqual(['/api/capacity', '/api/accounts', '/api/accounts'])
    noToken()
    expect(Object.keys(app.payload.state).length).toBeGreaterThan(0)
    scope.stop()
    cap.store.unwatch(q)
  })

  // Judged by the token's count of changes, not by its value: a token changed and back to the one read with is a change too, within
  // one flush while a page shows the lists and while none does (settings left, the token forgotten and the same one saved, settings
  // again). Both lists are read again then; a save of the very token the page holds is no change.
  it('reads both again when the token changed since they were read, also when it came back to the same one', async () => {
    const [first, second] = ['the-first-token-0123', 'the-second-token-0123']
    const app = { payload: { state: reactive({}) as Record<string, unknown> } }
    const held: Array<{ path: string; answer: (v: unknown) => void }> = []
    vi.stubGlobal('useNuxtApp', () => app)
    vi.stubGlobal('useState', (key: string, init: () => unknown) => {
      const r = toRef(app.payload.state, `$s${key}`)
      if (r.value === undefined) r.value = init()
      return r
    })
    vi.stubGlobal('computed', computed)
    vi.stubGlobal('watch', watch)
    vi.stubGlobal('useWorkbenchToken', useWorkbenchToken)
    vi.stubGlobal('useApi', () => ({ request: (path: string) => new Promise((answer) => held.push({ path, answer })) }))
    const doc = { owner: true, rev: 3, accounts: [], adapters: [] }
    const q = { agent: 'claude', account: 'a-home' }
    const admin = useWorkbenchToken()
    admin.set(first)
    const shown = () => {
      const scope = effectScope()
      return { scope, ...scope.run(() => ({ cap: useCapacity(), acc: useAccounts() }))! }
    }
    /** Both lists read and answered with the token held now. */
    async function read(page: ReturnType<typeof shown>) {
      const from = held.length
      void page.cap.store.watch(q)
      const loading = page.acc.store.load()
      expect(held.slice(from).map((h) => h.path)).toEqual(['/api/capacity', '/api/accounts'])
      held[from]!.answer(view('claude', 'a-home', { state: 'ok', account: { id: 'a-home', label: 'Home' } }))
      held[from + 1]!.answer(doc)
      await loading
      await settle()
      expect(page.acc.doc.value).not.toBeNull()
      expect(page.cap.entry(q).view).not.toBeNull()
    }
    let page = shown()
    await read(page)
    // Within one flush, while the page shows them: forgotten, and the accounts read again.
    let from = held.length
    admin.set(second)
    admin.set(first)
    await nextTick()
    expect(page.acc.doc.value).toBeNull()
    expect(page.cap.entry(q).view).toBeNull()
    expect(held.slice(from).map((h) => h.path)).toEqual(['/api/accounts'])
    held[from]!.answer(doc)
    await settle()
    page.cap.store.unwatch(q)
    await read(page)
    // While no page shows them: forgotten as the next page shows them, and the accounts read again.
    page.scope.stop()
    page.cap.store.unwatch(q)
    admin.clear()
    admin.set(first)
    from = held.length
    page = shown()
    expect(page.acc.doc.value).toBeNull()
    expect(page.cap.entry(q).view).toBeNull()
    expect(held.slice(from).map((h) => h.path)).toEqual(['/api/accounts'])
    held[from]!.answer(doc)
    await settle()
    // The token the page holds saved again: kept, nothing read again.
    await read(page)
    from = held.length
    admin.set(first)
    await nextTick()
    expect(page.acc.doc.value).not.toBeNull()
    expect(page.cap.entry(q).view).not.toBeNull()
    expect(held.length).toBe(from)
    page.scope.stop()
    page.cap.store.unwatch(q)
  })
})
