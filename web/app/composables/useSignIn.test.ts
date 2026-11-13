import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createRenderer, defineComponent, nextTick, ref, watch } from 'vue'
import {
  actNotTaken,
  asSignIn,
  codeGroups,
  useSignIn,
  SIGNIN_DEADLINE_MS,
  SIGNIN_EVERY_MS,
  SIGNIN_FIRST_MS,
  SIGNIN_READING,
  SIGNIN_UNAVAILABLE,
  SignInPoll,
  signInPage,
  wireSignIn,
  type SignInMethod,
  type SignInView,
} from './useSignIn'

// The sign-in's requests on fake timers, the route a stub or held requests: when it is asked, how often, what is shown and forgotten.

const signedOut = { supported: true, state: 'signed-out' }
const starting = { supported: true, state: 'starting' }
const code = { supported: true, state: 'code', code: 'WDJB-MJHT', url: 'https://id.example.com/device', expires: '2026-11-12T09:15:00Z' }
const signedIn = { supported: true, state: 'signed-in', who: 'Ana · workspace Acme' }
const lapsed = { supported: true, state: 'signed-out', why: 'The workspace lapsed.' }

type Reply = Record<string, unknown> | { status: number; code?: string }

/** The route, stubbed: each call answers the next reply of its method (the last again once they run out); `calls` each method and time. */
function route(replies: Partial<Record<SignInMethod, Reply[]>>) {
  const calls: Array<{ method: SignInMethod; at: number }> = []
  const call = vi.fn((method: SignInMethod, _signal: AbortSignal) => {
    calls.push({ method, at: Date.now() })
    const list = replies[method] ?? []
    const r = list.length > 1 ? list.shift()! : list[0]
    if (!r) return Promise.reject({ status: 0 })
    return 'status' in r ? Promise.reject(r) : Promise.resolve(structuredClone(r))
  })
  return { call, calls, gets: () => calls.filter((c) => c.method === 'GET').map((c) => c.at) }
}

/** The route, held: each call waits until the test answers or refuses it; `asks` in the order made, each with its signal. */
function held() {
  const asks: Array<{ method: SignInMethod; signal: AbortSignal; answer: (v: unknown) => void; refuse: (e: unknown) => void }> = []
  const call = vi.fn(
    (method: SignInMethod, signal: AbortSignal) =>
      new Promise((resolve, reject) => asks.push({ method, signal, answer: (v) => resolve(structuredClone(v)), refuse: reject })),
  )
  return { call, asks, methods: () => asks.map((a) => a.method) }
}

/** A card's poll: shown, visible, with a token and the identity capability unless told otherwise; its clock the fake Date's unless given. */
function card(call: (m: SignInMethod, s: AbortSignal) => Promise<unknown>, opts: { identity?: boolean; token?: string; now?: () => number } = {}) {
  const refs = { view: ref<SignInView | null>(null), error: ref(''), note: ref(''), busy: ref(false) }
  const poll = new SignInPoll(call, refs, undefined, opts.now ?? (() => Date.now()))
  poll.token(opts.token ?? 'workbench-token')
  poll.identity(opts.identity ?? true)
  poll.show(true)
  return { poll, ...refs }
}

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(0)
})
afterEach(() => vi.useRealTimers())

describe('the identity capability', () => {
  it('without it, nothing is requested, however long the card shows', async () => {
    const r = route({ GET: [signedOut] })
    const { poll, view } = card(r.call, { identity: false })
    poll.read()
    await vi.advanceTimersByTimeAsync(10 * SIGNIN_EVERY_MS)
    expect(r.call).not.toHaveBeenCalled()
    expect(view.value).toBeNull()
  })

  it('arriving reads at once; leaving forgets what was read and reads no more', async () => {
    const r = route({ GET: [signedIn] })
    const { poll, view } = card(r.call, { identity: false })
    poll.identity(true)
    await vi.advanceTimersByTimeAsync(0)
    expect(r.gets()).toEqual([0])
    expect(view.value?.who).toBe('Ana · workspace Acme')
    poll.identity(false)
    expect(view.value).toBeNull()
    await vi.advanceTimersByTimeAsync(10 * SIGNIN_EVERY_MS)
    expect(r.gets()).toEqual([0])
  })

  it('a server that offers no sign-in shows nothing', async () => {
    const r = route({ GET: [{ supported: false }] })
    const { view } = card(r.call)
    await vi.advanceTimersByTimeAsync(0)
    expect(view.value).toBeNull()
  })
})

describe('polling', () => {
  it('while a sign-in starts or shows a code: 2 s after the reply, doubling to 30 s', async () => {
    const r = route({ GET: [starting, { ...code, expires: undefined }] })
    card(r.call)
    await vi.advanceTimersByTimeAsync(120_000)
    const at = r.gets()
    const gaps = at.slice(1).map((t, i) => t - at[i]!)
    expect(gaps.slice(0, 7)).toEqual([SIGNIN_FIRST_MS, 4000, 8000, 16_000, SIGNIN_EVERY_MS, SIGNIN_EVERY_MS, SIGNIN_EVERY_MS])
  })

  it('a begin waits 2 s again before its next read, and reads every 30 s once signed in', async () => {
    const r = route({ GET: [signedOut, { ...code, expires: undefined }, code, signedIn], POST: [starting] })
    const { poll, view } = card(r.call)
    await vi.advanceTimersByTimeAsync(0)
    expect(view.value?.state).toBe('signed-out')
    vi.setSystemTime(10_000)
    await poll.act('POST')
    expect(view.value?.state).toBe('starting')
    await vi.advanceTimersByTimeAsync(SIGNIN_FIRST_MS - 1)
    expect(r.gets()).toEqual([0])
    await vi.advanceTimersByTimeAsync(1)
    expect(r.gets()).toEqual([0, 12_000])
    expect(view.value?.code).toBe('WDJB-MJHT')
    await vi.advanceTimersByTimeAsync(4000 + 8000)
    expect(view.value?.state).toBe('signed-in')
    const last = r.gets().at(-1)!
    await vi.advanceTimersByTimeAsync(SIGNIN_EVERY_MS)
    expect(r.gets().at(-1)).toBe(last + SIGNIN_EVERY_MS)
  })

  it('signed in, then a revoked or lapsed status shows at the next 30 s read', async () => {
    const r = route({ GET: [signedIn, lapsed] })
    const { view } = card(r.call)
    await vi.advanceTimersByTimeAsync(0)
    expect(view.value).toMatchObject({ state: 'signed-in', who: 'Ana · workspace Acme' })
    await vi.advanceTimersByTimeAsync(SIGNIN_EVERY_MS - 1)
    expect(view.value?.state).toBe('signed-in')
    await vi.advanceTimersByTimeAsync(1)
    expect(view.value).toMatchObject({ state: 'signed-out', who: '', why: 'The workspace lapsed.' })
  })

  it('a read that fails keeps what is known and reads again on the schedule', async () => {
    const r = route({ GET: [signedIn, { status: 503 }, signedIn] })
    const { view } = card(r.call)
    await vi.advanceTimersByTimeAsync(2 * SIGNIN_EVERY_MS)
    expect(r.gets()).toEqual([0, SIGNIN_EVERY_MS, 2 * SIGNIN_EVERY_MS])
    expect(view.value?.state).toBe('signed-in')
  })

  it('stops while the document is hidden, and reads at once when it is visible again', async () => {
    const r = route({ GET: [{ ...code, expires: undefined }] })
    const { poll } = card(r.call)
    await vi.advanceTimersByTimeAsync(0)
    poll.visible(false)
    await vi.advanceTimersByTimeAsync(10 * SIGNIN_EVERY_MS)
    expect(r.gets()).toEqual([0])
    poll.visible(true)
    await vi.advanceTimersByTimeAsync(0)
    expect(r.gets()).toEqual([0, 10 * SIGNIN_EVERY_MS])
  })

  it('stops when the card goes, forgetting what it read', async () => {
    const r = route({ GET: [code] })
    const { poll, view } = card(r.call)
    await vi.advanceTimersByTimeAsync(0)
    poll.show(false)
    expect(view.value).toBeNull()
    await vi.advanceTimersByTimeAsync(10 * SIGNIN_EVERY_MS)
    expect(r.gets()).toEqual([0])
  })
})

describe('one request on its way, each with an end', () => {
  it('an act on its way, the page hidden and shown twice: no read until the act settles, then one; the act’s 503 is shown', async () => {
    const h = held()
    const { poll, view, error, busy } = card(h.call)
    h.asks[0]!.answer(signedOut)
    await vi.advanceTimersByTimeAsync(0)
    const begun = poll.act('POST')
    for (let i = 0; i < 2; i++) {
      poll.visible(false)
      poll.visible(true)
    }
    expect(h.methods()).toEqual(['GET', 'POST'])
    h.asks[1]!.refuse({ status: 503, code: 'signin_unavailable' })
    await begun
    expect(error.value).toBe(SIGNIN_UNAVAILABLE)
    expect(busy.value).toBe(false)
    expect(view.value?.state).toBe('signed-out')
    expect(h.methods()).toEqual(['GET', 'POST', 'GET'])
    await vi.advanceTimersByTimeAsync(SIGNIN_FIRST_MS)
    expect(h.methods()).toEqual(['GET', 'POST', 'GET'])
  })

  it('three triggers during one slow read: one read on its way, one more after it', async () => {
    const h = held()
    const { poll } = card(h.call)
    poll.read()
    poll.visible(false)
    poll.visible(true)
    poll.read()
    expect(h.methods()).toEqual(['GET'])
    h.asks[0]!.answer(signedIn)
    await vi.advanceTimersByTimeAsync(0)
    expect(h.methods()).toEqual(['GET', 'GET'])
    h.asks[1]!.answer(signedIn)
    await vi.advanceTimersByTimeAsync(SIGNIN_EVERY_MS - 1)
    expect(h.methods()).toEqual(['GET', 'GET'])
  })

  it('a read owed while a request is on its way is dropped if the page is hidden when it settles: shown again, one read, the next on the schedule', async () => {
    const h = held()
    const { poll } = card(h.call)
    poll.visible(false)
    poll.visible(true)
    poll.visible(false)
    h.asks[0]!.answer(signedIn)
    await vi.advanceTimersByTimeAsync(0)
    expect(h.methods()).toEqual(['GET'])
    poll.visible(true)
    expect(h.methods()).toEqual(['GET', 'GET'])
    h.asks[1]!.answer(signedIn)
    await vi.advanceTimersByTimeAsync(0)
    expect(h.methods()).toEqual(['GET', 'GET'])
    await vi.advanceTimersByTimeAsync(SIGNIN_EVERY_MS - 1)
    expect(h.methods()).toEqual(['GET', 'GET'])
    await vi.advanceTimersByTimeAsync(1)
    expect(h.methods()).toEqual(['GET', 'GET', 'GET'])
  })

  it('a read never answered is aborted at its deadline, what is shown kept, and the card still reads afterwards', async () => {
    const h = held()
    const { view } = card(h.call)
    h.asks[0]!.answer(signedIn)
    await vi.advanceTimersByTimeAsync(SIGNIN_EVERY_MS)
    expect(h.methods()).toEqual(['GET', 'GET'])
    await vi.advanceTimersByTimeAsync(SIGNIN_DEADLINE_MS - 1)
    expect(h.asks[1]!.signal.aborted).toBe(false)
    await vi.advanceTimersByTimeAsync(1)
    expect(h.asks[1]!.signal.aborted).toBe(true)
    expect(view.value?.state).toBe('signed-in')
    await vi.advanceTimersByTimeAsync(SIGNIN_EVERY_MS)
    expect(h.methods()).toEqual(['GET', 'GET', 'GET'])
  })

  it('an act held past its deadline is not known: no act before the read that follows, while it is on its way or after it fails; one POST in all', async () => {
    const h = held()
    const { poll, error, note, busy } = card(h.call)
    h.asks[0]!.answer(signedOut)
    await vi.advanceTimersByTimeAsync(0)
    const begun = poll.act('POST')
    await vi.advanceTimersByTimeAsync(SIGNIN_DEADLINE_MS)
    await begun
    expect(h.asks[1]!.signal.aborted).toBe(true)
    expect(busy.value).toBe(false)
    expect(note.value).toBe(SIGNIN_READING)
    expect(error.value).toBe('')
    await poll.act('POST')
    expect(h.methods()).toEqual(['GET', 'POST'])
    // The read that follows, at the short wait: an act while it is on its way neither aborts it nor is made.
    await vi.advanceTimersByTimeAsync(SIGNIN_FIRST_MS)
    expect(h.methods()).toEqual(['GET', 'POST', 'GET'])
    await poll.act('POST')
    expect(h.asks[2]!.signal.aborted).toBe(false)
    h.asks[2]!.refuse({ status: 0, code: 'network' })
    await vi.advanceTimersByTimeAsync(0)
    await poll.act('POST')
    expect(h.methods()).toEqual(['GET', 'POST', 'GET'])
    expect(note.value).toBe(SIGNIN_READING)
  })

  it('a token change during an act aborts its request, and nothing of its reply is shown', async () => {
    const h = held()
    const { poll, view, error, busy } = card(h.call)
    h.asks[0]!.answer(signedIn)
    await vi.advanceTimersByTimeAsync(0)
    const out = poll.act('DELETE')
    poll.token('another-token')
    expect(h.asks[1]!.signal.aborted).toBe(true)
    expect(view.value).toBeNull()
    expect(busy.value).toBe(false)
    h.asks[1]!.refuse({ status: 503, code: 'signin_unavailable' })
    await out
    expect(view.value).toBeNull()
    expect(error.value).toBe('')
    // The new token's own read.
    expect(h.methods()).toEqual(['GET', 'DELETE', 'GET'])
    h.asks[2]!.answer(signedIn)
    await vi.advanceTimersByTimeAsync(0)
    expect(view.value?.state).toBe('signed-in')
  })

  it('an act’s error is gone with the next reply that shows a state', async () => {
    const r = route({ GET: [signedIn], DELETE: [{ status: 503, code: 'signin_unavailable' }] })
    const { poll, view, error } = card(r.call)
    await vi.advanceTimersByTimeAsync(0)
    await poll.act('DELETE')
    expect(error.value).toBe(SIGNIN_UNAVAILABLE)
    await vi.advanceTimersByTimeAsync(SIGNIN_EVERY_MS)
    expect(error.value).toBe('')
    expect(view.value?.state).toBe('signed-in')
  })
})

describe('begin and sign out', () => {
  it('show what the provider answers; one not taken is said in the card, what was shown kept', async () => {
    const r = route({ GET: [signedIn], DELETE: [{ status: 503, code: 'signin_unavailable' }, signedOut] })
    const { poll, view, error, busy } = card(r.call)
    await vi.advanceTimersByTimeAsync(0)
    const out = poll.act('DELETE')
    expect(busy.value).toBe(true)
    await out
    expect(busy.value).toBe(false)
    expect(error.value).toBe(SIGNIN_UNAVAILABLE)
    expect(view.value?.state).toBe('signed-in')
    await poll.act('DELETE')
    expect(error.value).toBe('')
    expect(view.value?.state).toBe('signed-out')
  })

  it('a begin whose status could not be read is said as being read, with no error line, and read again at the short wait', async () => {
    const r = route({ GET: [signedOut, starting], POST: [{ status: 503, code: 'signin_status_unknown' }] })
    const { poll, view, error, note } = card(r.call)
    await vi.advanceTimersByTimeAsync(0)
    vi.setSystemTime(10_000)
    await poll.act('POST')
    expect(note.value).toBe(SIGNIN_READING)
    expect(error.value).toBe('')
    await vi.advanceTimersByTimeAsync(SIGNIN_FIRST_MS - 1)
    expect(r.gets()).toEqual([0])
    await vi.advanceTimersByTimeAsync(1)
    expect(r.gets()).toEqual([0, 12_000])
    expect(view.value?.state).toBe('starting')
    expect(note.value).toBe('')
  })

  it('while an act’s outcome is not known no act is made, a failed read keeping it so, until a read shows a state', async () => {
    const r = route({ GET: [signedOut, { status: 503 }, starting], POST: [{ status: 503, code: 'signin_status_unknown' }, starting] })
    const { poll, note } = card(r.call)
    const acts = () => r.calls.filter((c) => c.method !== 'GET').length
    await vi.advanceTimersByTimeAsync(0)
    await poll.act('POST')
    expect(note.value).toBe(SIGNIN_READING)
    await poll.act('POST')
    await poll.act('DELETE')
    expect(acts()).toBe(1)
    await vi.advanceTimersByTimeAsync(SIGNIN_FIRST_MS)
    expect(r.gets()).toHaveLength(2)
    expect(note.value).toBe(SIGNIN_READING)
    await poll.act('POST')
    expect(acts()).toBe(1)
    await vi.advanceTimersByTimeAsync(2 * SIGNIN_FIRST_MS)
    expect(r.gets()).toHaveLength(3)
    expect(note.value).toBe('')
    await poll.act('POST')
    expect(acts()).toBe(2)
  })

  it('an act that fails with no answer, a 5xx other than signin_unavailable, any other code or a reply the card cannot read is not known: no act until a read shows a state', async () => {
    for (const failure of [
      { status: 0, code: 'network' },
      { status: 502 },
      { status: 504 },
      { status: 500, code: 'internal' },
      { status: 503 },
      { status: 503, code: 'signin_status_unknown' },
      { status: 413, code: 'bound' },
      { supported: true, state: 'pending' },
    ]) {
      const r = route({ GET: [signedOut], POST: [failure, starting] })
      const { poll, note, error } = card(r.call)
      const posts = () => r.calls.filter((c) => c.method === 'POST').length
      await vi.advanceTimersByTimeAsync(0)
      await poll.act('POST')
      expect(note.value, JSON.stringify(failure)).toBe(SIGNIN_READING)
      expect(error.value, JSON.stringify(failure)).toBe('')
      await poll.act('POST')
      expect(posts(), JSON.stringify(failure)).toBe(1)
      await vi.advanceTimersByTimeAsync(SIGNIN_FIRST_MS)
      expect(note.value, JSON.stringify(failure)).toBe('')
      await poll.act('POST')
      expect(posts(), JSON.stringify(failure)).toBe(2)
      poll.show(false)
    }
  })

  it('an act the server says was not taken leaves it offered: 503 signin_unavailable, and the refusals made before the provider is asked', async () => {
    for (const failure of [{ status: 503, code: 'signin_unavailable' }, { status: 400 }, { status: 401 }, { status: 403 }, { status: 429 }]) {
      const r = route({ GET: [signedOut], POST: [failure, starting] })
      const { poll, note } = card(r.call)
      await vi.advanceTimersByTimeAsync(0)
      await poll.act('POST')
      expect(note.value, JSON.stringify(failure)).toBe('')
      await poll.act('POST')
      expect(r.calls.filter((c) => c.method === 'POST'), JSON.stringify(failure)).toHaveLength(2)
      poll.show(false)
    }
    for (const status of [400, 401, 403, 404, 429]) expect(actNotTaken({ status }), String(status)).toBe(true)
    for (const e of [{ status: 503 }, { status: 503, code: 'signin_status_unknown' }, { status: 0 }, new Error('signin-deadline'), null]) {
      expect(actNotTaken(e), JSON.stringify(e)).toBe(false)
    }
  })

  it('the reads after a not-known act failing three times: still no act, still the short wait', async () => {
    const r = route({ GET: [signedOut, { status: 503 }, { status: 0, code: 'network' }, { status: 502 }, signedOut], POST: [{ status: 0, code: 'network' }, starting] })
    const { poll, note } = card(r.call)
    const posts = () => r.calls.filter((c) => c.method === 'POST').length
    await vi.advanceTimersByTimeAsync(0)
    await poll.act('POST')
    await vi.advanceTimersByTimeAsync(SIGNIN_FIRST_MS + 4000 + 8000)
    expect(r.gets()).toEqual([0, SIGNIN_FIRST_MS, SIGNIN_FIRST_MS + 4000, SIGNIN_FIRST_MS + 4000 + 8000])
    expect(note.value).toBe(SIGNIN_READING)
    await poll.act('POST')
    expect(posts()).toBe(1)
    // The next read sooner than the schedule's 30 s; it shows a state, and the act is offered again.
    await vi.advanceTimersByTimeAsync(16_000)
    expect(r.gets()).toHaveLength(5)
    expect(note.value).toBe('')
    await poll.act('POST')
    expect(posts()).toBe(2)
  })

  it('a read made before a begin never overwrites what the begin answered', async () => {
    const h = held()
    const { poll, view } = card(h.call)
    h.asks[0]!.answer(signedOut)
    await vi.advanceTimersByTimeAsync(SIGNIN_EVERY_MS) // the next read is on its way
    const begun = poll.act('POST')
    expect(h.asks[1]!.signal.aborted).toBe(true)
    h.asks[2]!.answer(starting)
    await begun
    h.asks[1]!.answer(signedOut)
    await vi.advanceTimersByTimeAsync(0)
    expect(view.value?.state).toBe('starting')
  })

  it('404 not_supported hides the card', async () => {
    const r = route({ GET: [signedOut], POST: [{ status: 404 }] })
    const { poll, view } = card(r.call)
    await vi.advanceTimersByTimeAsync(0)
    await poll.act('POST')
    expect(view.value).toBeNull()
  })
})

describe('a code’s end', () => {
  it('at its expires the code and its page go, and the card says it ended', async () => {
    const r = route({ GET: [{ ...code, expires: new Date(60_000).toISOString() }] })
    const { view } = card(r.call)
    await vi.advanceTimersByTimeAsync(60_000 - 1)
    expect(view.value).toMatchObject({ state: 'code', code: 'WDJB-MJHT', url: 'https://id.example.com/device', ended: false })
    await vi.advanceTimersByTimeAsync(1)
    expect(view.value).toMatchObject({ state: 'code', code: '', url: '', ended: true })
    // Read again, the same code is still ended.
    await vi.advanceTimersByTimeAsync(SIGNIN_EVERY_MS)
    expect(view.value).toMatchObject({ code: '', url: '', ended: true })
  })

  it('a code whose expires is past at first sight is never shown', async () => {
    vi.setSystemTime(Date.parse('2026-11-12T09:15:00Z'))
    const r = route({ GET: [code] })
    const { view } = card(r.call)
    const codes: string[] = []
    watch(view, (v) => codes.push(v?.code ?? ''), { flush: 'sync' })
    await vi.advanceTimersByTimeAsync(0)
    expect(view.value).toMatchObject({ state: 'code', code: '', url: '', ended: true })
    expect(codes).not.toContain('WDJB-MJHT')
  })

  it('a code with no expires is shown, however late', async () => {
    vi.setSystemTime(Date.parse('2030-01-01T00:00:00Z'))
    const { view } = card(route({ GET: [{ ...code, expires: undefined }] }).call)
    await vi.advanceTimersByTimeAsync(0)
    expect(view.value).toMatchObject({ state: 'code', code: 'WDJB-MJHT', url: 'https://id.example.com/device', ended: false })
  })
})

describe('a code’s end, by the wall clock at every look', () => {
  const sixty = { ...code, expires: new Date(60_000).toISOString() }

  it('the wall clock past its expires, the timers standing still: the next read failing shows it ended', async () => {
    const at = { wall: 0 }
    const r = route({ GET: [sixty, { status: 503 }] })
    const { poll, view } = card(r.call, { now: () => at.wall })
    await vi.advanceTimersByTimeAsync(0)
    expect(view.value).toMatchObject({ code: 'WDJB-MJHT', ended: false })
    at.wall = 60_000
    poll.read()
    await vi.advanceTimersByTimeAsync(0)
    expect(r.gets()).toHaveLength(2)
    expect(view.value).toMatchObject({ state: 'code', code: '', url: '', ended: true })
  })

  it('the wall clock past its expires while the page was hidden: shown again, it has ended before any read answers', async () => {
    const at = { wall: 0 }
    const h = held()
    const { poll, view } = card(h.call, { now: () => at.wall })
    h.asks[0]!.answer(sixty)
    await vi.advanceTimersByTimeAsync(0)
    expect(view.value).toMatchObject({ code: 'WDJB-MJHT', ended: false })
    poll.visible(false)
    at.wall = 60_000
    poll.visible(true)
    expect(view.value).toMatchObject({ state: 'code', code: '', url: '', ended: true })
    expect(h.asks).toHaveLength(2)
  })
})

/** A document whose visibility the test sets, dispatching visibilitychange to the listeners it holds. */
function page(visibilityState = 'visible') {
  const listeners = new Set<() => void>()
  return {
    visibilityState,
    listeners,
    addEventListener: (_: 'visibilitychange', fn: () => void) => listeners.add(fn),
    removeEventListener: (_: 'visibilitychange', fn: () => void) => listeners.delete(fn),
    set(v: string) {
      this.visibilityState = v
      for (const fn of listeners) fn()
    },
  }
}

describe('the card’s lifetime', () => {
  afterEach(() => vi.unstubAllGlobals())

  /** A component that uses useSignIn, mounted through a renderer of no document, the document and the app's auto-imports stubbed. */
  function mountCard(visibility: string) {
    const r = route({ GET: [signedIn] })
    const doc = page(visibility)
    vi.stubGlobal('document', doc)
    vi.stubGlobal('useApi', () => ({ request: (_path: string, o: { method: SignInMethod; signal: AbortSignal }) => r.call(o.method, o.signal) }))
    vi.stubGlobal('useWorkbenchToken', () => ({ token: ref('workbench-token') }))
    vi.stubGlobal('useCapabilities', () => ({ has: () => true }))
    const none = () => null
    const app = createRenderer<object, object>({
      patchProp: () => {},
      insert: () => {},
      remove: () => {},
      createElement: () => ({}),
      createText: () => ({}),
      createComment: () => ({}),
      setText: () => {},
      setElementText: () => {},
      parentNode: none,
      nextSibling: none,
    }).createApp(
      defineComponent({
        setup() {
          useSignIn()
          return none
        },
      }),
    )
    app.mount({})
    return { r, doc, app }
  }

  it('its unmount takes its listener and its schedule down: no read after it', async () => {
    const { r, doc, app } = mountCard('visible')
    await vi.advanceTimersByTimeAsync(0)
    expect(r.gets()).toHaveLength(1)
    app.unmount()
    doc.set('hidden')
    doc.set('visible')
    await vi.advanceTimersByTimeAsync(10 * SIGNIN_EVERY_MS)
    expect(r.gets()).toHaveLength(1)
    expect(doc.listeners.size).toBe(0)
  })

  it('mounted while the page is hidden, it reads nothing until the page is shown', async () => {
    const { r, doc, app } = mountCard('hidden')
    await vi.advanceTimersByTimeAsync(10 * SIGNIN_EVERY_MS)
    expect(r.gets()).toHaveLength(0)
    doc.set('visible')
    await vi.advanceTimersByTimeAsync(0)
    expect(r.gets()).toHaveLength(1)
    app.unmount()
  })
})

describe('the wiring', () => {
  it('follows the document, the token and the capability, and lets go of all three when the card goes', async () => {
    const r = route({ GET: [signedIn] })
    const refs = { view: ref<SignInView | null>(null), error: ref(''), note: ref(''), busy: ref(false) }
    const doc = page()
    const token = ref('workbench-token')
    const identity = ref(true)
    const poll = new SignInPoll(r.call, refs)
    const unwire = wireSignIn(poll, doc, token, identity)
    await vi.advanceTimersByTimeAsync(0)
    expect(r.gets()).toEqual([0])
    doc.set('hidden')
    await vi.advanceTimersByTimeAsync(10 * SIGNIN_EVERY_MS)
    expect(r.gets()).toEqual([0])
    doc.set('visible')
    await vi.advanceTimersByTimeAsync(0)
    expect(r.gets()).toHaveLength(2)
    token.value = 'another-token'
    await nextTick()
    expect(r.gets()).toHaveLength(3)
    identity.value = false
    await nextTick()
    expect(refs.view.value).toBeNull()
    identity.value = true
    await nextTick()
    await vi.advanceTimersByTimeAsync(0)
    expect(r.gets()).toHaveLength(4)
    unwire()
    expect(doc.listeners.size).toBe(0)
    expect(refs.view.value).toBeNull()
    // Its watchers are stopped: the poll is told of no change after it.
    const told = [vi.spyOn(poll, 'token'), vi.spyOn(poll, 'identity')]
    token.value = 'a-third-token'
    identity.value = false
    await nextTick()
    for (const spy of told) expect(spy).not.toHaveBeenCalled()
    doc.set('hidden')
    doc.set('visible')
    await vi.advanceTimersByTimeAsync(10 * SIGNIN_EVERY_MS)
    expect(r.gets()).toHaveLength(4)
  })
})

describe('a reply as the card keeps it', () => {
  it('keeps each state with what it carries and nothing else', () => {
    expect(asSignIn(code)).toEqual({ state: 'code', who: '', code: 'WDJB-MJHT', url: 'https://id.example.com/device', expires: '2026-11-12T09:15:00Z', why: '', ended: false })
    expect(asSignIn({ ...signedIn, code: 'X', url: 'https://e.example' })).toEqual({ state: 'signed-in', who: 'Ana · workspace Acme', code: '', url: '', expires: '', why: '', ended: false })
    expect(asSignIn({ ...signedOut, who: 'Ana' })?.who).toBe('')
  })

  it('shows nothing for a reply it cannot trust', () => {
    for (const body of [null, 'x', { supported: false }, { supported: true, state: 'pending' }, { ...code, code: 'AB CD' }, { ...code, code: 'A'.repeat(17) }, { ...code, code: undefined }]) {
      expect(asSignIn(body)).toBeNull()
    }
    expect(asSignIn({ ...signedIn, who: 'a\nb' })?.who).toBe('')
    expect(asSignIn({ ...signedOut, why: 'é'.repeat(61) })?.why).toBe('')
  })

  it('refuses in who and why controls, and the format characters and separators the server sends them without', () => {
    const c = (n: number) => String.fromCharCode(n)
    for (const who of [`Ana ${c(0x202e)}gnp.exe`, c(0x200b), `Ana${c(0x2028)}Eve`, `Ana${c(0x2029)}Eve`,'a\u0085b', 'a\u001bb', 'a\u007fb']) {
      expect(asSignIn({ ...signedIn, who })?.who, JSON.stringify(who)).toBe('')
      expect(asSignIn({ ...signedOut, why: who })?.why, JSON.stringify(who)).toBe('')
    }
  })

  it('draws no link for a page the server’s rule refuses, and keeps the code', () => {
    for (const url of ['http://id.example.com/device', 'javascript:alert(1)', undefined]) {
      expect(asSignIn({ ...code, url })).toMatchObject({ code: 'WDJB-MJHT', url: '' })
    }
  })

  it('links to the page by the server’s rule, the href the text that was sent, never the app’s own', () => {
    const app = 'https://conductor.example:8443'
    for (const u of ['https://id.example.com/device?x=1', 'https://id.example/device?user_code=ABCD-EFGH', 'https://id.example:8443/x', 'https://id.example.com/a%20b']) {
      expect(signInPage(u, app), u).toBe(u)
      expect(asSignIn({ ...code, url: u }, app)?.url, u).toBe(u)
    }
    for (const u of [
      // The server refuses each (ext's signInURL): an address literal, capitals, a trailing dot or an escape in the host, no path,
      // port 443 or one with a leading zero, a fragment.
      'https://999.1.1.1/device',
      'https://[fe80::1%25eth0]/device',
      'https://[::1]/x',
      'https://a.0x7f/x',
      'https://ID.example/x',
      'https://id.example./x',
      'https://ex%25ample.test/d',
      'https://id.example',
      'https://id.example:443/x',
      'https://id.example:0443/x',
      'https://id.example/x#token=abc',
      'https://id.example/x#a',
      // The server refuses these too: an empty authority, an empty port, a scheme not in lower case.
      'https:///id.example/device',
      'https://id.example:/x',
      'HTTPS://id.example/x',
      'Https://id.example/x',
      // The server takes these two, which a browser writes otherwise.
      'https://id.example/a/../b',
      'https://id.example/%2e%2e/b',
      // No `//` authority, no host name, a bad port, user information.
      'https:/id.example.com/x',
      'https:id.example.com',
      'https://:443/x',
      'https://./',
      'https://id.example.com:99999/d',
      'https://id.example.com:0/d',
      'https://@id.example.com/',
      'https://ana:pw@id.example.com',
      // Characters and escapes the server refuses.
      'https://id.example.com/d?x=%zz',
      "https://id.example.com/a'b",
      'https://id.example.com/a`b',
      'https://id.example.com/a b',
      'https://id.example.com/a\\b',
      // Not https, not absolute, too long: 200 bytes is the bound, counted in bytes.
      'http://id.example.com',
      'data:text/html,x',
      '/device',
      '',
      'https://e.example/' + 'a'.repeat(200),
      'https://example.com/' + 'é'.repeat(100),
      // The app's own origin, with a fragment or without.
      'https://conductor.example:8443/settings',
      'https://conductor.example:8443/#x',
    ]) {
      expect(signInPage(u, app), u).toBe('')
    }
  })

  it('shows eight letters and digits in two groups of four, anything else as it is', () => {
    expect(codeGroups('WDJB-MJHT')).toEqual(['WDJB', 'MJHT'])
    expect(codeGroups('WDJBMJHT')).toEqual(['WDJB', 'MJHT'])
    expect(codeGroups('ABC-123')).toEqual(['ABC-123'])
  })
})
