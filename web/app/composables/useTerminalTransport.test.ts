import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { computed, ref, type Ref } from 'vue'
import { useTerminalTransport } from './useTerminalTransport'
import { useWorkbenchToken } from './useWorkbenchToken'

/** Every WebSocket the code opens, by its address; none connects. */
let opened: string[] = []
class FakeSocket {
  binaryType = ''
  onopen: (() => void) | null = null
  onclose: ((e: { code: number; reason: string }) => void) | null = null
  onmessage: (() => void) | null = null
  onerror: (() => void) | null = null
  readyState = 0
  constructor(url: string) {
    opened.push(url)
  }
  send() {}
  close() {}
}

let admin: ReturnType<typeof useWorkbenchToken>

// useTerminalTransport reads Nuxt's auto-imports (useApiBase, useIdentity, useWorkbenchToken) as globals: each is stubbed here, and
// useWorkbenchToken is the app's own over a Nuxt app of this test's. Tokens are invented.
beforeEach(() => {
  opened = []
  const state = new Map<string, Ref<unknown>>()
  vi.stubGlobal('useState', (key: string, init: () => unknown) => {
    if (!state.has(key)) state.set(key, ref(init()))
    return state.get(key)
  })
  const app = {}
  vi.stubGlobal('useNuxtApp', () => app)
  vi.stubGlobal('computed', computed)
  vi.stubGlobal('useApiBase', () => ({ wsBase: ref('ws://api.test'), httpBase: ref('http://api.test') }))
  vi.stubGlobal('useIdentity', () => ({ name: ref('Nate') }))
  vi.stubGlobal('useWorkbenchToken', useWorkbenchToken)
  vi.stubGlobal('WebSocket', FakeSocket)
  vi.stubGlobal('window', { setTimeout, clearTimeout })
  admin = useWorkbenchToken()
  admin.set('old-token')
})
afterEach(() => vi.unstubAllGlobals())

const HELLO = { cols: 80, rows: 24 }
const tokenOf = (url: string) => new URL(url.replace(/^ws/, 'http')).searchParams.get('token')

describe('a terminal connection made while the page to connect shows', () => {
  it('opens with the token taken, not the refused one it was made under', async () => {
    const release = admin.hold()
    admin.answered('old-token', false)
    expect(admin.gated.value).toBe(true)
    const t = useTerminalTransport().create({ sessionId: 's-1', token: admin.token.value, kind: 'server', chatOnly: true })
    void t.connect(HELLO).catch(() => {})
    await Promise.resolve()
    expect(opened, 'nothing opens while the page shows').toEqual([])
    admin.take('new-token')
    await vi.waitFor(() => expect(opened).toHaveLength(1))
    expect(tokenOf(opened[0]!)).toBe('new-token')
    release()
  })

  it('does not open at all when it was closed while it waited', async () => {
    const release = admin.hold()
    admin.answered('old-token', false)
    const t = useTerminalTransport().create({ sessionId: 's-1', token: admin.token.value, kind: 'server' })
    const out = t.connect(HELLO).catch((e: unknown) => e)
    t.close()
    admin.take('new-token')
    expect(await out).toBeInstanceOf(Error)
    expect(opened).toEqual([])
    release()
  })

  it("a link's token is the caller's own: it opens at once, with it", () => {
    const release = admin.hold()
    admin.answered('old-token', false)
    const t = useTerminalTransport().create({ sessionId: 's-1', token: 'a-link-token', kind: 'server' })
    void t.connect(HELLO).catch(() => {})
    expect(opened.map(tokenOf)).toEqual(['a-link-token'])
    t.close()
    release()
  })
})
