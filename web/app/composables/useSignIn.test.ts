import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { asSignIn, codeGroups, SIGNIN_EVERY_MS, SIGNIN_FIRST_MS, SIGNIN_UNAVAILABLE, SignInPoll, signInPage, type SignInMethod, type SignInView } from './useSignIn'

// The sign-in's reads on fake timers, the route a stub: when it is read, how often, and what is shown and forgotten.

const signedOut = { supported: true, state: 'signed-out' }
const starting = { supported: true, state: 'starting' }
const code = { supported: true, state: 'code', code: 'WDJB-MJHT', url: 'https://id.example.com/device', expires: '2026-11-12T09:15:00Z' }
const signedIn = { supported: true, state: 'signed-in', who: 'Ana · workspace Acme' }
const lapsed = { supported: true, state: 'signed-out', why: 'The workspace lapsed.' }

type Reply = Record<string, unknown> | { status: number }

/** The route, stubbed: each call answers the next reply of its method (the last again once they run out); `calls` each method and time. */
function route(replies: Partial<Record<SignInMethod, Reply[]>>) {
  const calls: Array<{ method: SignInMethod; at: number }> = []
  const call = vi.fn((method: SignInMethod) => {
    calls.push({ method, at: Date.now() })
    const list = replies[method] ?? []
    const r = list.length > 1 ? list.shift()! : list[0]
    if (!r) return Promise.reject({ status: 0 })
    return 'status' in r ? Promise.reject(r) : Promise.resolve(structuredClone(r))
  })
  return { call, calls, gets: () => calls.filter((c) => c.method === 'GET').map((c) => c.at) }
}

/** A card's poll: shown, visible, with a token and the identity capability unless told otherwise. */
function card(call: (m: SignInMethod) => Promise<unknown>, opts: { identity?: boolean; token?: string } = {}) {
  const view = ref<SignInView | null>(null)
  const error = ref('')
  const busy = ref(false)
  const poll = new SignInPoll(call, view, error, busy)
  poll.token(opts.token ?? 'workbench-token')
  poll.identity(opts.identity ?? true)
  poll.show(true)
  return { poll, view, error, busy }
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
    await poll.read()
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
    const r = route({ GET: [starting, code] })
    card(r.call)
    await vi.advanceTimersByTimeAsync(120_000)
    const at = r.gets()
    const gaps = at.slice(1).map((t, i) => t - at[i]!)
    expect(gaps.slice(0, 7)).toEqual([SIGNIN_FIRST_MS, 4000, 8000, 16_000, SIGNIN_EVERY_MS, SIGNIN_EVERY_MS, SIGNIN_EVERY_MS])
  })

  it('a begin waits 2 s again before its next read, and reads every 30 s once signed in', async () => {
    const r = route({ GET: [signedOut, code, code, signedIn], POST: [starting] })
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
    const r = route({ GET: [code] })
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

describe('the token', () => {
  it('another token forgets the code at once and drops the reply on its way for the last one', async () => {
    let answer: (v: unknown) => void = () => {}
    const call = vi.fn((_: SignInMethod) => new Promise((resolve) => (answer = resolve)))
    const { poll, view } = card(call)
    answer(structuredClone(code))
    await vi.advanceTimersByTimeAsync(0)
    expect(view.value?.code).toBe('WDJB-MJHT')
    // A read for the first token on its way when the token changes.
    await vi.advanceTimersByTimeAsync(SIGNIN_FIRST_MS)
    const late = answer
    poll.token('another-token')
    expect(view.value).toBeNull()
    late(structuredClone(code))
    await vi.advanceTimersByTimeAsync(0)
    expect(view.value).toBeNull()
    // The new token's own read answers.
    answer(structuredClone(signedOut))
    await vi.advanceTimersByTimeAsync(0)
    expect(view.value?.state).toBe('signed-out')
  })

  it('no token reads nothing', async () => {
    const r = route({ GET: [signedOut] })
    card(r.call, { token: '' })
    await vi.advanceTimersByTimeAsync(10 * SIGNIN_EVERY_MS)
    expect(r.call).not.toHaveBeenCalled()
  })
})

describe('begin and sign out', () => {
  it('show what the provider answers; one it did not answer is said in the card, what was shown kept', async () => {
    const r = route({ GET: [signedIn], DELETE: [{ status: 503 }, signedOut] })
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

  it('a read made before a begin never overwrites what the begin answered', async () => {
    const answers: Array<(v: unknown) => void> = []
    const call = vi.fn((_: SignInMethod) => new Promise((resolve) => answers.push(resolve)))
    const { poll, view } = card(call)
    answers[0]!(structuredClone(signedOut))
    await vi.advanceTimersByTimeAsync(SIGNIN_EVERY_MS) // the next read is on its way
    const begun = poll.act('POST')
    answers[2]!(structuredClone(starting))
    await begun
    answers[1]!(structuredClone(signedOut))
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

describe('a reply as the card keeps it', () => {
  it('keeps each state with what it carries and nothing else', () => {
    expect(asSignIn(code)).toEqual({ state: 'code', who: '', code: 'WDJB-MJHT', url: 'https://id.example.com/device', expires: '2026-11-12T09:15:00Z', why: '' })
    expect(asSignIn({ ...signedIn, code: 'X', url: 'https://e.example' })).toEqual({ state: 'signed-in', who: 'Ana · workspace Acme', code: '', url: '', expires: '', why: '' })
    expect(asSignIn({ ...signedOut, who: 'Ana' })?.who).toBe('')
  })

  it('shows nothing for a reply it cannot trust', () => {
    for (const body of [null, 'x', { supported: false }, { supported: true, state: 'pending' }, { ...code, url: 'http://id.example.com/device' }, { ...code, url: 'javascript:alert(1)' }, { ...code, code: 'AB CD' }, { ...code, code: 'A'.repeat(17) }, { ...code, url: undefined }]) {
      expect(asSignIn(body)).toBeNull()
    }
    expect(asSignIn({ ...signedIn, who: 'a\nb' })?.who).toBe('')
    expect(asSignIn({ ...signedOut, why: 'é'.repeat(61) })?.why).toBe('')
  })

  it('links only to an https page with a host', () => {
    expect(signInPage('https://id.example.com/device?x=1')).toBe(true)
    for (const u of ['http://id.example.com', 'https://ana:pw@id.example.com', 'data:text/html,x', '/device', '', 'https://e.example/' + 'a'.repeat(200)]) expect(signInPage(u)).toBe(false)
  })

  it('shows eight letters and digits in two groups of four, anything else as it is', () => {
    expect(codeGroups('WDJB-MJHT')).toEqual(['WDJB', 'MJHT'])
    expect(codeGroups('WDJBMJHT')).toEqual(['WDJB', 'MJHT'])
    expect(codeGroups('ABC-123')).toEqual(['ABC-123'])
  })
})
