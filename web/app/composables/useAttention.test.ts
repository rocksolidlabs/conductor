import { createServer } from 'node:http'
import type { AddressInfo } from 'node:net'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { computed, ref, type Ref } from 'vue'
import { openEvents } from './useAttention'
import { useWorkbenchToken } from './useWorkbenchToken'

// The events stream's request as the layout makes it as it mounts: with a wrong token in the browser it is refused, and its refusal
// can land after the gate saved the right one (#281). It is judged by the token it carried. useWorkbenchToken is the app's own, over
// a Nuxt app of this test's; fetch is stubbed.
describe('openEvents', () => {
  let admin: ReturnType<typeof useWorkbenchToken>
  const fetchFn = vi.fn()
  const realFetch = globalThis.fetch

  beforeEach(() => {
    fetchFn.mockReset()
    const state = new Map<string, Ref<unknown>>()
    vi.stubGlobal('useState', (key: string, init: () => unknown) => {
      if (!state.has(key)) state.set(key, ref(init()))
      return state.get(key)
    })
    const app = {}
    vi.stubGlobal('useNuxtApp', () => app)
    vi.stubGlobal('computed', computed)
    vi.stubGlobal('fetch', fetchFn)
    admin = useWorkbenchToken()
    admin.set('not-the-workbench-token')
  })
  afterEach(() => vi.unstubAllGlobals())

  const STREAM = 'text/event-stream'

  /** A reply held until the test answers it with `status`, and `type` as its content type when one is given. */
  function held() {
    let answer!: (status: number, type?: string) => void
    fetchFn.mockReturnValueOnce(new Promise<Response>((resolve) => (answer = (status, type) => resolve(new Response(null, { status, headers: type ? { 'Content-Type': type } : {} })))))
    return (status: number, type?: string) => answer(status, type)
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
    accept(200, STREAM)
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
    accept(200, STREAM)
    expect((await accepted)?.status).toBe(200)
    expect([...kept]).toEqual([['conductor.workbenchToken', 'link-token-0123456789']])
    expect(reload).not.toHaveBeenCalled()
  })

  it("with a link's token tried, only the stream itself answering accepts it: a 204, a 200 of another type and a 200 reached through a redirect decide nothing", async () => {
    // A server of the test's own on this machine, so that the redirect is followed as fetch follows it.
    const server = createServer((req, res) => {
      if (req.url === '/moved') return void res.writeHead(302, { Location: '/stream' }).end()
      res.writeHead(200, { 'Content-Type': STREAM }).end()
    })
    await new Promise<void>((r) => server.listen(0, '127.0.0.1', r))
    const base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`
    try {
      const kept = new Map<string, string>()
      const reload = vi.fn()
      vi.stubGlobal('localStorage', { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) })
      vi.stubGlobal('location', { reload })
      admin.fromLink('link-token-0123456789')
      for (const [status, type] of [[204, undefined], [200, 'text/html; charset=utf-8']] as const) {
        const answer = held()
        const out = openEvents('http://api.test/api/events', admin, false, new AbortController().signal)
        answer(status, type)
        expect((await out)?.status).toBe(status)
      }
      fetchFn.mockImplementationOnce((url: string, init: RequestInit) => realFetch(url, init))
      const moved = await openEvents(`${base}/moved`, admin, false, new AbortController().signal)
      expect([moved?.status, moved?.redirected, moved?.headers.get('content-type')]).toEqual([200, true, STREAM])
      expect(kept.size).toBe(0)
      expect(admin.needsToken.value).toBe(false)
      expect(reload).not.toHaveBeenCalled()
      fetchFn.mockImplementationOnce((url: string, init: RequestInit) => realFetch(url, init))
      expect((await openEvents(`${base}/stream`, admin, false, new AbortController().signal))?.status).toBe(200)
      expect([...kept]).toEqual([['conductor.workbenchToken', 'link-token-0123456789']])
      expect(reload).not.toHaveBeenCalled()
    } finally {
      await new Promise((r) => server.close(r))
    }
  })

  it("with a link's token tried, a 201 or a 206 of the stream's type decides nothing; a 200 of that type with a parameter, or in another case of its letters, keeps it", async () => {
    const kept = new Map<string, string>()
    const reload = vi.fn()
    vi.stubGlobal('localStorage', { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) })
    vi.stubGlobal('location', { reload })
    for (const [link, type] of [['link-token-0123456789', 'text/event-stream; charset=utf-8'], ['link-token-9876543210', 'Text/Event-Stream']] as const) {
      kept.clear()
      admin.fromLink(link)
      for (const status of [201, 206]) {
        const answer = held()
        const out = openEvents('http://api.test/api/events', admin, false, new AbortController().signal)
        answer(status, STREAM)
        expect((await out)?.status, `${status}`).toBe(status)
        expect(kept.size, `${status}`).toBe(0)
      }
      expect(admin.needsToken.value).toBe(false)
      const accept = held()
      const accepted = openEvents('http://api.test/api/events', admin, false, new AbortController().signal)
      accept(200, type)
      expect((await accepted)?.status).toBe(200)
      expect([...kept], type).toEqual([['conductor.workbenchToken', link]])
    }
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
