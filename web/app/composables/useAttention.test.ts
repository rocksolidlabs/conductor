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

  it('hands back a reply that is no refusal, asking for nothing', async () => {
    const answer = held()
    const out = openEvents('http://api.test/api/events', admin, false, new AbortController().signal)
    answer(200)
    expect((await out)?.status).toBe(200)
    expect(admin.needsToken.value).toBe(false)
  })
})
