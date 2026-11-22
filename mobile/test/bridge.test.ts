import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it, vi } from 'vitest'
import { checkAddress, makeBridge, MAX_ADDRESS, MAX_HTTP_BODY, PLAIN_WORDS, type ListenerHandle, type NativeLayer, type ProviderKit, type StoreArea } from '../src/bridge'

// The phone shell's bridge over a fake native layer (docs/plans/phone-shell.md, OSS-1a, Tests per surface, mobile/test/bridge.test.ts):
// a provider module registers once, and only before the workbench reads the bridge; its factory alone is handed the key, the store,
// the push token and the native HTTP call, and the bridge holds none of them; a tap's data is handed on unchanged; Open a server takes
// https:// and http:// (with its words) and refuses every other address; the last five addresses are the shell's own.

type Listener = (x: unknown) => void

/** A fake native layer: what it was asked, and its listeners, which a test fires. */
function fakeNative() {
  const files = new Map<string, string>()
  const listeners = new Map<string, Listener[]>()
  const opened: string[] = []
  const handle = (event: string, cb: Listener): ListenerHandle => {
    listeners.set(event, [...(listeners.get(event) ?? []), cb])
    return { remove: () => listeners.set(event, (listeners.get(event) ?? []).filter((l) => l !== cb)) }
  }
  const at = (area: StoreArea, key: string) => `${area}/${key}`
  const native: NativeLayer = {
    platform: 'ios',
    key: {
      create: vi.fn(async () => ({ publicKey: 'pk' })),
      publicKey: vi.fn(async () => ({ publicKey: 'pk' })),
      state: vi.fn(async () => ({ state: 'none' as const })),
      bind: vi.fn(async () => {}),
      sign: vi.fn(async () => ({ signature: 'sig' })),
      ending: vi.fn(async () => {}),
      remove: vi.fn(async () => {}),
    },
    store: {
      put: async ({ area, key, data }) => void files.set(at(area, key), data),
      get: async ({ area, key }) => ({ data: files.get(at(area, key)) ?? null }),
      list: async ({ area }) => ({ keys: [...files.keys()].filter((k) => k.startsWith(`${area}/`)).map((k) => ({ key: k.slice(area.length + 1), savedAt: 't' })) }),
      remove: async ({ area, key }) => void files.delete(at(area, key)),
      clear: async () => {
        for (const k of [...files.keys()]) if (!k.startsWith('servers/')) files.delete(k)
      },
    },
    push: {
      checkPermissions: async () => ({ receive: 'prompt' }),
      requestPermissions: async () => ({ receive: 'granted' }),
      register: vi.fn(async () => {}),
      addListener: ((event: string, cb: Listener) => handle(event, cb)) as NativeLayer['push']['addListener'],
    },
    app: {
      addListener: ((event: string, cb: Listener) => handle(event, cb)) as NativeLayer['app']['addListener'],
      getLaunchUrl: async () => undefined,
    },
    browser: { open: vi.fn(async ({ url }: { url: string }) => void opened.push(url)) },
    http: { request: vi.fn(async () => ({ status: 200, headers: {}, data: 'ok' })) },
  }
  const fire = (event: string, x: unknown) => (listeners.get(event) ?? []).forEach((l) => l(x))
  return { native, files, opened, fire, listeners }
}

const quiet = () => ({ open: vi.fn(), console: { warn: vi.fn() } })
const settle = () => new Promise((r) => setTimeout(r, 0))

/** Every value reachable from v through its own properties (getters read, `provider` left out). */
function reachable(v: unknown, seen = new Set<unknown>()): Set<unknown> {
  if ((typeof v !== 'object' && typeof v !== 'function') || v === null || seen.has(v)) return seen
  seen.add(v)
  for (const k of Reflect.ownKeys(v as object)) {
    if (k === 'provider') continue
    const d = Object.getOwnPropertyDescriptor(v, k)
    if (!d) continue
    reachable('value' in d ? d.value : d.get?.call(v), seen)
  }
  return seen
}

describe('a provider module', () => {
  it('registers once: a second call refused with a console line, the first factory kept', () => {
    const { native } = fakeNative()
    const host = quiet()
    const { bridge } = makeBridge(native, host)
    const first = { name: 'First' }
    bridge.register(() => first)
    bridge.register(() => ({ name: 'Second' }))
    expect(host.console.warn).toHaveBeenCalledTimes(1)
    expect(bridge.provider).toBe(first)
  })

  it('is refused once the workbench has read the bridge, or the page has loaded', () => {
    const { native } = fakeNative()
    const host = quiet()
    const a = makeBridge(native, host)
    expect(a.bridge.provider).toBeNull()
    const factory = vi.fn(() => ({ name: 'Late' }))
    a.bridge.register(factory)
    expect(factory).not.toHaveBeenCalled()
    expect(a.bridge.provider).toBeNull()
    const b = makeBridge(native, host)
    b.closeRegistration()
    b.bridge.register(factory)
    expect(factory).not.toHaveBeenCalled()
    expect(host.console.warn).toHaveBeenCalledTimes(2)
  })

  it('a factory that throws, or answers nothing, leaves no provider, with a console line', () => {
    for (const factory of [() => { throw new Error('broken') }, () => null, 'not a function']) {
      const host = quiet()
      const { bridge } = makeBridge(fakeNative().native, host)
      bridge.register(factory)
      expect(bridge.provider).toBeNull()
      expect(host.console.warn).toHaveBeenCalledTimes(1)
    }
  })

  it('is alone handed the key, the store, the push token and the HTTP call; the bridge holds none of them', async () => {
    const { native, fire } = fakeNative()
    const { bridge } = makeBridge(native, quiet())
    let kit: ProviderKit | undefined
    bridge.register((k: ProviderKit) => ((kit = k), { name: 'P' }))
    expect(Object.keys(kit!).sort()).toEqual(['http', 'key', 'push', 'store'])
    expect(Object.keys(kit!.key).sort()).toEqual(['bind', 'create', 'ending', 'publicKey', 'remove', 'sign', 'state'])
    await kit!.key.sign({ request: { a: 1 } })
    expect(native.key.sign).toHaveBeenCalledWith({ request: { a: 1 } })
    await kit!.http({ url: 'https://example.test/' })
    expect(native.http.request).toHaveBeenCalledTimes(1)
    const tokens: string[] = []
    kit!.push.onToken((t) => tokens.push(t))
    fire('registration', { value: 'tok-1' })
    expect(kit!.push.token()).toBe('tok-1')
    expect(tokens).toEqual(['tok-1'])

    const held = reachable(bridge)
    for (const part of [kit, kit!.key, kit!.store, kit!.push, kit!.http, ...Object.values(kit!.key), ...Object.values(kit!.store), native, native.key, native.store, native.http]) {
      expect(held.has(part)).toBe(false)
    }
    expect(Object.keys(bridge).sort()).toEqual(['links', 'openServer', 'platform', 'provider', 'push', 'register', 'servers', 'v'])
    expect(JSON.stringify([...held].filter((v) => typeof v === 'string'))).not.toContain('tok-1')
    expect(Object.isFrozen(bridge)).toBe(true)
  })

  it("is handed a reply's body as its text, byte for byte, never parsed; one that is not text, or past the bound, refused", async () => {
    const { native } = fakeNative()
    // Key order, spacing and escapes that a parse and a re-encoding would change.
    const body = '{"z":1,  "a":[2,1],\n "s":"\\u00e9t\u00e9", "n":1.50}'
    native.http.request = vi.fn(async () => ({ status: 200, headers: { 'content-type': 'application/json' }, data: body }))
    const { bridge } = makeBridge(native, quiet())
    let kit: ProviderKit | undefined
    bridge.register((k: ProviderKit) => ((kit = k), { name: 'P' }))
    const sent = '{"b":2,"a":1}'
    const r = await kit!.http({ url: 'https://relay.example.test/v1/x', method: 'POST', data: sent })
    expect(r.data).toBe(body)
    expect(r.status).toBe(200)
    expect(native.http.request).toHaveBeenCalledWith({ url: 'https://relay.example.test/v1/x', method: 'POST', data: sent, responseType: 'text' })
    native.http.request = vi.fn(async () => ({ status: 200, headers: {}, data: { parsed: true } }))
    await expect(kit!.http({ url: 'https://relay.example.test/' })).rejects.toMatchObject({ code: 'unreadable' })
    native.http.request = vi.fn(async () => ({ status: 200, headers: {}, data: 'x'.repeat(MAX_HTTP_BODY + 1) }))
    await expect(kit!.http({ url: 'https://relay.example.test/' })).rejects.toMatchObject({ code: 'too_large' })
    native.http.request = vi.fn(async () => ({ status: 200, headers: {}, data: 'é'.repeat(MAX_HTTP_BODY / 2) }))
    expect((await kit!.http({ url: 'https://relay.example.test/' })).data).toHaveLength(MAX_HTTP_BODY / 2)
    // The bound is the text's UTF-8 bytes, not its characters: one two-byte character past it is refused.
    native.http.request = vi.fn(async () => ({ status: 200, headers: {}, data: 'é'.repeat(MAX_HTTP_BODY / 2 + 1) }))
    await expect(kit!.http({ url: 'https://relay.example.test/' })).rejects.toMatchObject({ code: 'too_large' })
  })

  it("is handed the drafts and cache areas of the store, never the shell's servers area", async () => {
    const { native, files } = fakeNative()
    const { bridge } = makeBridge(native, quiet())
    let kit: ProviderKit | undefined
    bridge.register((k: ProviderKit) => ((kit = k), { name: 'P' }))
    await kit!.store.put('drafts', 'i1', '{}')
    expect(files.get('drafts/i1')).toBe('{}')
    for (const call of [
      () => kit!.store.put('servers' as 'cache', 'recent', '[]'),
      () => kit!.store.get('servers' as 'cache', 'recent'),
      () => kit!.store.list('servers' as 'cache'),
      () => kit!.store.remove('servers' as 'cache', 'recent'),
    ]) {
      await expect(call()).rejects.toMatchObject({ code: 'area_refused' })
    }
    expect([...files.keys()].some((k) => k.startsWith('servers/'))).toBe(false)
  })

  it("reads every native refusal with its code and words, whether the runtime put the words at the top (iPhone) or under data (Android)", async () => {
    const { native } = fakeNative()
    native.platform = 'android'
    // Android's runtime rejects with the message, the code, and the plugin's fields under `data`, on an Error of its own.
    const android = (code: string, words: string) => Object.assign(new Error(words), { code, data: { words } })
    native.key.sign = vi.fn(async () => Promise.reject(android('presence_refused', 'Not confirmed: nothing was signed.')))
    native.store.put = vi.fn(async () => Promise.reject(android('full', 'This phone keeps 32 drafts already.')))
    native.store.clear = vi.fn(async () => Promise.reject(android('not_cleared', 'Not everything of this sign-in could be removed from this phone.')))
    native.http.request = vi.fn(async () => Promise.reject(android('origin_refused', 'Only an https address on an origin this shell names is sent.')))
    const { bridge } = makeBridge(native, quiet())
    let kit: ProviderKit | undefined
    bridge.register((k: ProviderKit) => ((kit = k), { name: 'P' }))
    await expect(kit!.key.sign({ request: {} })).rejects.toMatchObject({ code: 'presence_refused', words: 'Not confirmed: nothing was signed.' })
    await expect(kit!.store.put('drafts', 'd', 'x')).rejects.toMatchObject({ code: 'full', words: 'This phone keeps 32 drafts already.' })
    await expect(kit!.store.clear()).rejects.toMatchObject({ code: 'not_cleared' })
    await expect(kit!.store.clear()).rejects.toHaveProperty('words', 'Not everything of this sign-in could be removed from this phone.')
    await expect(kit!.http({ url: 'https://x.example/' })).rejects.toMatchObject({ code: 'origin_refused', words: 'Only an https address on an origin this shell names is sent.' })
    // The iPhone's shape is left as it is; anything that is no refusal too.
    const ios = Object.assign(new Error('w'), { code: 'no_key', words: 'This phone has no key.' })
    native.key.state = vi.fn(async () => Promise.reject(ios))
    await expect(kit!.key.state()).rejects.toBe(ios)
    native.key.remove = vi.fn(async () => Promise.reject('plain'))
    await expect(kit!.key.remove()).rejects.toBe('plain')
  })
})

describe('the platform', () => {
  it("hands a tap's data on unchanged, and nothing that is not a string", () => {
    const { native, fire } = fakeNative()
    const { bridge } = makeBridge(native, quiet())
    const taps: Array<Record<string, string>> = []
    const stop = bridge.push.onTap((d) => taps.push(d))
    fire('pushNotificationActionPerformed', { notification: { data: { h: 'abc', other: 'x y', n: 3, o: { a: 1 } } } })
    expect(taps).toEqual([{ h: 'abc', other: 'x y' }])
    stop()
    fire('pushNotificationActionPerformed', { notification: { data: { h: 'again' } } })
    expect(taps).toHaveLength(1)
  })

  it('asks for the permission, and registers for push only when it is granted', async () => {
    const { native } = fakeNative()
    const { bridge } = makeBridge(native, quiet())
    expect(await bridge.push.permission()).toBe('prompt')
    expect(await bridge.push.request()).toBe('granted')
    expect(native.push.register).toHaveBeenCalledTimes(1)
  })

  it('hands each link the shell is opened with, the launch link first', async () => {
    const { native, fire } = fakeNative()
    native.app.getLaunchUrl = async () => ({ url: 'https://example.test/first' })
    const { bridge } = makeBridge(native, quiet())
    const links: string[] = []
    bridge.links.onOpen((u) => links.push(u))
    await settle()
    fire('appUrlOpen', { url: 'https://example.test/second' })
    expect(links).toEqual(['https://example.test/first', 'https://example.test/second'])
  })

  it('is web in a browser: Open a server opens a tab, and the key and the store refuse every call', async () => {
    const host = quiet()
    const { bridge } = makeBridge(null, host)
    expect(bridge.platform).toBe('web')
    expect(await bridge.openServer('https://desk.example.test:8080/')).toEqual({ ok: true })
    expect(host.open).toHaveBeenCalledWith('https://desk.example.test:8080/')
    expect(await bridge.servers.recent()).toEqual(['https://desk.example.test:8080/'])
    let kit: ProviderKit | undefined
    bridge.register((k: ProviderKit) => ((kit = k), { name: 'P' }))
    await expect(kit!.key.create({ until: 'x' })).rejects.toMatchObject({ code: 'no_native' })
    await expect(kit!.store.put('drafts', 'a', 'b')).rejects.toMatchObject({ code: 'no_native' })
    await expect(kit!.http({ url: 'https://example.test/' })).rejects.toMatchObject({ code: 'no_native' })
    expect(kit!.push.token()).toBeNull()
    expect(await bridge.push.permission()).toBe('denied')
  })
})

describe('Open a server', () => {
  it('takes https:// and http://, the latter with its words, and opens it in the in-app browser', async () => {
    const { native, opened } = fakeNative()
    const { bridge } = makeBridge(native, quiet())
    expect(await bridge.openServer('https://desk.example.test/decide/dr-7')).toEqual({ ok: true })
    expect(await bridge.openServer(' http://192.0.2.10:8080 ')).toEqual({ ok: true, words: PLAIN_WORDS })
    expect(opened).toEqual(['https://desk.example.test/decide/dr-7', 'http://192.0.2.10:8080/'])
  })

  it('refuses every other address with words, opening nothing', async () => {
    const { native, opened } = fakeNative()
    const { bridge } = makeBridge(native, quiet())
    const long = `https://example.test/${'a'.repeat(MAX_ADDRESS)}`
    for (const a of ['javascript:alert(1)', 'file:///etc/hosts', 'data:text/html,<b>x</b>', 'conductor://open', 'ftp://example.test/', long, 'https://example.test/a\u0000b', 'https://exa\nmple.test/', 'not an address', '', 'https://']) {
      const r = await bridge.openServer(a)
      expect(r.ok, a.slice(0, 40)).toBe(false)
      expect(r.words).toBeTruthy()
    }
    expect(opened).toEqual([])
    expect(checkAddress(`https://example.test/${'a'.repeat(MAX_ADDRESS - 'https://example.test/'.length)}`).ok).toBe(true)
  })

  it('keeps the last five addresses, newest first, through a cache eviction, and none after Forget', async () => {
    const { native, files } = fakeNative()
    const { bridge } = makeBridge(native, quiet())
    for (let i = 1; i <= 7; i++) await bridge.openServer(`https://s${i}.example.test/`)
    await bridge.openServer('https://s5.example.test/')
    const want = ['https://s5.example.test/', 'https://s7.example.test/', 'https://s6.example.test/', 'https://s4.example.test/', 'https://s3.example.test/']
    expect(await bridge.servers.recent()).toEqual(want)
    // The cache area emptied, as its eviction or a provider's sign-out empties it: the servers area stays.
    files.set('cache/x', 'y')
    await native.store.clear()
    expect(await bridge.servers.recent()).toEqual(want)
    await bridge.servers.forget()
    expect(await bridge.servers.recent()).toEqual([])
  })
})

describe('the workbench', () => {
  it('imports nothing of the shell: no module under web/app names mobile/', () => {
    const app = fileURLToPath(new URL('../../web/app', import.meta.url))
    const files: string[] = []
    const walk = (d: string) => {
      for (const n of readdirSync(d)) {
        const p = join(d, n)
        if (statSync(p).isDirectory()) walk(p)
        else if (/\.(ts|vue|mjs|js)$/.test(n)) files.push(p)
      }
    }
    walk(app)
    expect(files.length).toBeGreaterThan(100)
    for (const f of files) expect(readFileSync(f, 'utf8'), f).not.toMatch(/from\s+['"][^'"]*mobile\/|import\(\s*['"][^'"]*mobile\//)
  })
})
