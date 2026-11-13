import { $fetch as ofetch } from 'ofetch'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { computed, ref, type Ref } from 'vue'
import { ApiError, useApi } from './useApi'
import { useWorkbenchToken } from './useWorkbenchToken'

const raw = vi.fn()
const fetchFn = Object.assign(vi.fn(), { raw })
/** The workbench token as the app keeps it (useWorkbenchToken itself, over a stubbed useState), holding `workbench-token`. */
let admin: ReturnType<typeof useWorkbenchToken>

// useApi reads Nuxt's auto-imports (useRuntimeConfig, computed, useWorkbenchToken, $fetch) as globals: each is stubbed here, and
// useWorkbenchToken is the app's own over a useState of this test's.
beforeEach(() => {
  raw.mockReset()
  fetchFn.mockReset()
  const state = new Map<string, Ref<unknown>>()
  vi.stubGlobal('useState', (key: string, init: () => unknown) => {
    if (!state.has(key)) state.set(key, ref(init()))
    return state.get(key)
  })
  vi.stubGlobal('useRuntimeConfig', () => ({ public: { apiBase: 'http://api.test' } }))
  vi.stubGlobal('computed', computed)
  vi.stubGlobal('useWorkbenchToken', useWorkbenchToken)
  vi.stubGlobal('$fetch', fetchFn)
  admin = useWorkbenchToken()
  admin.set('workbench-token')
})
afterEach(() => vi.unstubAllGlobals())

describe('requestText', () => {

  it('returns the body as text and as its bytes with the reply headers, the bearer header sent', async () => {
    raw.mockResolvedValue({ _data: new TextEncoder().encode('set-up\n').buffer, headers: new Headers({ 'X-Conductor-Log-SHA256': 'ab12' }) })
    const { requestText } = useApi()
    const reply = await requestText('/api/runs/r-1/prerequisites/tools/1/log')
    expect(reply.text).toBe('set-up\n')
    expect(Array.from(reply.bytes)).toEqual(Array.from(new TextEncoder().encode('set-up\n')))
    expect(reply.headers.get('X-Conductor-Log-SHA256')).toBe('ab12')
    expect(raw).toHaveBeenCalledWith('http://api.test/api/runs/r-1/prerequisites/tools/1/log', {
      method: 'GET',
      responseType: 'arrayBuffer',
      headers: { Authorization: 'Bearer workbench-token' },
    })
  })

  it('reads an empty log as empty text and no bytes', async () => {
    raw.mockResolvedValue({ _data: undefined, headers: new Headers({ 'X-Conductor-Log-SHA256': 'e3b0' }) })
    const reply = await useApi().requestText('/log')
    expect(reply.text).toBe('')
    expect(reply.bytes.length).toBe(0)
    expect(reply.headers.get('X-Conductor-Log-SHA256')).toBe('e3b0')
  })

  // A log the proc bound cut inside a character (Codex's review of #502): the bytes the server hashed are kept whole for Save, the
  // text shows the cut character as U+FFFD.
  it("keeps a log's bytes that are no UTF-8 as they came, through ofetch itself", async () => {
    const sent = new Uint8Array([...new TextEncoder().encode('caf'), 0xc3, 0x0a, 0xe2, 0x9c])
    vi.stubGlobal('$fetch', ofetch.create({}, { fetch: vi.fn(async () => new Response(sent, { status: 200, headers: { 'Content-Type': 'text/plain; charset=utf-8', 'X-Conductor-Log-SHA256': 'cut' } })) }))
    const reply = await useApi().requestText('/log')
    expect(Array.from(reply.bytes)).toEqual(Array.from(sent))
    expect(reply.text).toBe('caf\uFFFD\n\uFFFD')
    expect(reply.headers.get('X-Conductor-Log-SHA256')).toBe('cut')
  })

  // Through ofetch itself, the app's $fetch, against a server's reply: under `responseType: 'text'` it hands back an error's JSON body
  // as text, and under `arrayBuffer` (requestText's, since the review of #502) as bytes, which a stub that rejects with the body
  // already parsed hid (the reviews of #422).
  it('normalises an error as request does, the body ofetch hands a text request as text', async () => {
    const reply = (status: number, body: string, type: string) => vi.fn(async () => new Response(body, { status, headers: { 'Content-Type': type } }))
    const gone = reply(410, JSON.stringify({ error: { code: 'log_dropped', message: 'the log was not kept' } }), 'application/json')
    vi.stubGlobal('$fetch', ofetch.create({}, { fetch: gone }))
    const asText = await ofetch.create({}, { fetch: gone }).raw('/log', { responseType: 'text' }).catch((e: { data?: unknown }) => e.data)
    expect(typeof asText).toBe('string')
    const asBytes = await ofetch.create({}, { fetch: gone }).raw('/log', { responseType: 'arrayBuffer' }).catch((e: { data?: unknown }) => e.data)
    expect(asBytes).toBeInstanceOf(ArrayBuffer)
    const { request, requestText } = useApi()
    const fromText = await requestText('/log').catch((e: unknown) => e)
    const fromJSON = await request('/log').catch((e: unknown) => e)
    for (const e of [fromText, fromJSON]) {
      expect(e).toBeInstanceOf(ApiError)
      expect(e).toMatchObject({ status: 410, code: 'log_dropped', message: 'the log was not kept', details: { code: 'log_dropped' } })
    }
    // A body that is no JSON (a proxy's page) is no error body: the status alone.
    vi.stubGlobal('$fetch', ofetch.create({}, { fetch: reply(404, 'nothing here', 'text/plain') }))
    const plain = await useApi().requestText('/log').catch((e: unknown) => e)
    expect(plain).toMatchObject({ status: 404, code: 'http_error' })
  })

  it('says the server cannot be reached, and asks for the token on a 401 only without one of its own', async () => {
    raw.mockRejectedValueOnce(new Error('fetch failed'))
    const unreachable = await useApi().requestText('/log').catch((e: unknown) => e)
    expect(unreachable).toMatchObject({ status: 0, code: 'network', message: 'Cannot reach the server' })
    const unauthorized = Object.assign(new Error('Unauthorized'), { statusCode: 401, data: { error: { code: 'unauthorized', message: 'workbench token required' } } })
    raw.mockRejectedValueOnce(unauthorized)
    await useApi().requestText('/log', { token: 'a-link-token' }).catch(() => {})
    expect(admin.needsToken.value).toBe(false)
    raw.mockRejectedValueOnce(unauthorized)
    await useApi().requestText('/log').catch(() => {})
    expect(admin.needsToken.value).toBe(true)
  })
})

// A 401 is the refusal of the token the request carried, not of the one held when its reply lands (#281): a page opened with a wrong
// token sends its reads with it, the gate opens on the first refusal, the right token is saved, and a refusal still on its way for the
// wrong one must not open the gate again over the right one.
describe('a 401', () => {
  const unauthorized = () => Object.assign(new Error('Unauthorized'), { statusCode: 401, data: { error: { code: 'unauthorized', message: 'workbench token required' } } })
  /** A reply held until the test answers it. */
  function held() {
    let refuse!: () => void
    const reply = new Promise<never>((_, reject) => (refuse = () => reject(unauthorized())))
    return { reply, refuse }
  }
  const requests = {
    request: (path: string, opts?: { token?: string }) => useApi().request(path, opts),
    requestText: (path: string, opts?: { token?: string }) => useApi().requestText(path, opts),
    requestBlob: (path: string, opts?: { token?: string }) => useApi().requestBlob(path, opts),
  }
  const send = (kind: keyof typeof requests, reply: Promise<never>) => (kind === 'requestText' ? raw : fetchFn).mockReturnValueOnce(reply)

  it.each(Object.keys(requests) as Array<keyof typeof requests>)('%s answered after another token was saved asks for nothing', async (kind) => {
    const { reply, refuse } = held()
    send(kind, reply)
    const out = requests[kind]('/api/guide').catch((e: unknown) => e)
    const sentWith = (kind === 'requestText' ? raw : fetchFn).mock.calls[0]![1].headers
    expect(sentWith).toEqual({ Authorization: 'Bearer workbench-token' })
    // The gate opened on another refusal and the right token was saved: the gate closes.
    admin.needsToken.value = true
    admin.set('the-right-token')
    expect(admin.needsToken.value).toBe(false)
    refuse()
    expect(await out).toMatchObject({ status: 401, code: 'unauthorized' })
    expect(admin.needsToken.value).toBe(false)
    expect(admin.token.value).toBe('the-right-token')
  })

  it.each(Object.keys(requests) as Array<keyof typeof requests>)('%s answered with the token it carried still held asks for it', async (kind) => {
    const { reply, refuse } = held()
    send(kind, reply)
    const out = requests[kind]('/api/guide').catch((e: unknown) => e)
    refuse()
    expect(await out).toMatchObject({ status: 401 })
    expect(admin.needsToken.value).toBe(true)
  })

  // Routes beside the workbench's admit other credentials too (a session's own token reads its session): the events stream, which
  // admits the workbench token alone, is what keeps a link's token (useAttention.test.ts).
  it.each(Object.keys(requests) as Array<keyof typeof requests>)("%s answered with success keeps no link's token; a 401 drops it", async (kind) => {
    const kept = new Map<string, string>()
    const reload = vi.fn()
    vi.stubGlobal('localStorage', { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) })
    vi.stubGlobal('location', { reload })
    admin.fromLink('link-token-0123456789')
    ;(kind === 'requestText' ? raw : fetchFn).mockResolvedValueOnce(kind === 'requestText' ? { _data: undefined, headers: new Headers() } : {})
    await requests[kind]('/api/guide')
    expect((kind === 'requestText' ? raw : fetchFn).mock.calls[0]![1].headers).toEqual({ Authorization: 'Bearer link-token-0123456789' })
    expect(kept.size).toBe(0)
    const { reply, refuse } = held()
    send(kind, reply)
    const out = requests[kind]('/api/guide').catch((e: unknown) => e)
    refuse()
    expect(await out).toMatchObject({ status: 401 })
    expect(reload).toHaveBeenCalledTimes(1)
    expect(kept.size).toBe(0)
    expect(admin.needsToken.value).toBe(false)
  })

  it('of a token of the caller\'s own never asks for the workbench token, even when it is the one held', async () => {
    for (const token of ['a-link-token', 'workbench-token']) {
      fetchFn.mockRejectedValueOnce(unauthorized())
      await useApi().request('/api/guide', { token }).catch(() => {})
      expect(admin.needsToken.value).toBe(false)
    }
  })
})
