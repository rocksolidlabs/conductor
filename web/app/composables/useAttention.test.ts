import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { computed, ref, type Ref } from 'vue'
import { openEvents } from './useAttention'
import { useWorkbenchToken } from './useWorkbenchToken'

// The events stream's request as the layout makes it as it mounts: with a wrong token in the browser it is refused, and its refusal
// can land after the gate saved the right one (#281). It is judged by the token it carried. useWorkbenchToken is the app's own, over
// a useState of this test's; fetch is stubbed.
describe('openEvents', () => {
  let admin: ReturnType<typeof useWorkbenchToken>
  const fetchFn = vi.fn()

  beforeEach(() => {
    fetchFn.mockReset()
    const state = new Map<string, Ref<unknown>>()
    vi.stubGlobal('useState', (key: string, init: () => unknown) => {
      if (!state.has(key)) state.set(key, ref(init()))
      return state.get(key)
    })
    vi.stubGlobal('computed', computed)
    vi.stubGlobal('fetch', fetchFn)
    admin = useWorkbenchToken()
    admin.set('not-the-workbench-token')
  })
  afterEach(() => vi.unstubAllGlobals())

  /** A reply held until the test answers it with `status`. */
  function held() {
    let answer!: (status: number) => void
    fetchFn.mockReturnValueOnce(new Promise<Response>((resolve) => (answer = (status) => resolve(new Response(null, { status })))))
    return (status: number) => answer(status)
  }

  it('asks for nothing when its refusal lands after another token was saved', async () => {
    const answer = held()
    const out = openEvents('http://api.test/api/events', admin, false, new AbortController().signal)
    expect(fetchFn.mock.calls[0]![1].headers).toEqual({ Authorization: 'Bearer not-the-workbench-token' })
    admin.needsToken.value = true
    admin.set('the-workbench-token')
    answer(401)
    expect(await out).toBeNull()
    expect(admin.needsToken.value).toBe(false)
    expect(admin.token.value).toBe('the-workbench-token')
  })

  it('asks for the token when the one it carried is still held', async () => {
    const answer = held()
    const out = openEvents('http://api.test/api/events', admin, true, new AbortController().signal)
    expect(fetchFn.mock.calls[0]![1].headers).toEqual({ Authorization: 'Bearer not-the-workbench-token', 'X-Conductor-Desktop': '1' })
    answer(401)
    expect(await out).toBeNull()
    expect(admin.needsToken.value).toBe(true)
  })

  it("keeps a link's token it carried once a reply accepts it, and drops it, loading the page again, when one refuses it", async () => {
    const kept = new Map<string, string>()
    const reload = vi.fn()
    vi.stubGlobal('localStorage', { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) })
    vi.stubGlobal('location', { reload })
    admin.fromLink('link-token-0123456789')
    const refuse = held()
    const refused = openEvents('http://api.test/api/events', admin, false, new AbortController().signal)
    refuse(401)
    expect(await refused).toBeNull()
    expect(reload).toHaveBeenCalledTimes(1)
    expect(kept.size).toBe(0)
    expect(admin.needsToken.value).toBe(false)
    admin.fromLink('link-token-9876543210')
    const accept = held()
    const accepted = openEvents('http://api.test/api/events', admin, false, new AbortController().signal)
    accept(200)
    expect((await accepted)?.status).toBe(200)
    expect([...kept]).toEqual([['conductor.workbenchToken', 'link-token-9876543210']])
  })

  it("with a link's token tried, a reply that is neither success nor 401, or none, decides nothing; a success then keeps it", async () => {
    const kept = new Map<string, string>()
    const reload = vi.fn()
    vi.stubGlobal('localStorage', { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) })
    vi.stubGlobal('location', { reload })
    admin.fromLink('link-token-0123456789')
    for (const status of [403, 429, 503]) {
      const answer = held()
      const out = openEvents('http://api.test/api/events', admin, false, new AbortController().signal)
      answer(status)
      expect((await out)?.status).toBe(status)
    }
    fetchFn.mockRejectedValueOnce(new TypeError('Failed to fetch'))
    await expect(openEvents('http://api.test/api/events', admin, false, new AbortController().signal)).rejects.toThrow('Failed to fetch')
    expect(kept.size).toBe(0)
    expect(admin.token.value).toBe('link-token-0123456789')
    expect(admin.needsToken.value).toBe(false)
    expect(reload).not.toHaveBeenCalled()
    const accept = held()
    const accepted = openEvents('http://api.test/api/events', admin, false, new AbortController().signal)
    accept(200)
    expect((await accepted)?.status).toBe(200)
    expect([...kept]).toEqual([['conductor.workbenchToken', 'link-token-0123456789']])
    expect(reload).not.toHaveBeenCalled()
  })

  it('hands back a reply that is no refusal, asking for nothing', async () => {
    const answer = held()
    const out = openEvents('http://api.test/api/events', admin, false, new AbortController().signal)
    answer(200)
    expect((await out)?.status).toBe(200)
    expect(admin.needsToken.value).toBe(false)
  })
})
