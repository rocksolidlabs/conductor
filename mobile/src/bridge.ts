// The phone shell's bridge, `window.conductorShell` v1 (docs/plans/phone-shell.md, Part two, OSS-1a "mobile/src/bridge.ts"): built
// once, in the page, before a provider module and before the workbench, from the native layer the shell's runtime puts in the page.
// It imports nothing. The native layer is described here by an interface of the shell's own (NativeLayer: the plugins the shell
// registers and the ones it uses, by the calls it makes of them), so the bridge stands, and is tested, with no native runtime: in a
// browser (the boot spec's), the platform is `web`, Open a server opens a tab, and the key and the store a provider is handed refuse
// every call (`no_native`).
//
// The workbench is handed the bridge alone: the platform, the push permission and its taps, the links the shell is opened with,
// Open a server and its last addresses, and the provider. The key, the store, the push token and the native HTTP call go to a
// provider module's factory and nowhere else.

/** A refusal: a code and its words. */
export interface Refusal {
  code: string
  words: string
}

/** A listener's handle, as the native runtime answers one. */
export interface ListenerHandle {
  remove(): void | Promise<void>
}

/** The key plugin (ConductorKey): it signs only what the packaging's native policy builds. */
export interface NativeKey {
  create(o: { until: string }): Promise<{ publicKey: string }>
  publicKey(): Promise<{ publicKey: string | null }>
  state(): Promise<{ state: 'none' | 'unbound' | 'bound' | 'ending'; id?: string; until?: string }>
  bind(o: { id: string }): Promise<void>
  sign(o: { request: Record<string, unknown> }): Promise<{ signature: string }>
  ending(): Promise<void>
  remove(): Promise<void>
}

/** The store plugin (ConductorStore): one file per record, in three areas with their bounds. */
export type StoreArea = 'drafts' | 'cache' | 'servers'
export interface NativeStore {
  put(o: { area: StoreArea; key: string; data: string }): Promise<void>
  get(o: { area: StoreArea; key: string }): Promise<{ data: string | null; savedAt?: string }>
  list(o: { area: StoreArea }): Promise<{ keys: Array<{ key: string; savedAt: string }> }>
  remove(o: { area: StoreArea; key: string }): Promise<void>
  clear(): Promise<void>
}

/** The calls the bridge makes of the platform's plugins (push, app, in-app browser, native HTTP). */
export interface NativeLayer {
  platform: 'ios' | 'android'
  key: NativeKey
  store: NativeStore
  push: {
    checkPermissions(): Promise<{ receive: string }>
    requestPermissions(): Promise<{ receive: string }>
    register(): Promise<void>
    addListener(event: 'registration', cb: (t: { value: string }) => void): Promise<ListenerHandle> | ListenerHandle
    addListener(event: 'pushNotificationActionPerformed', cb: (a: { notification?: { data?: unknown } }) => void): Promise<ListenerHandle> | ListenerHandle
  }
  app: {
    addListener(event: 'appUrlOpen', cb: (e: { url: string }) => void): Promise<ListenerHandle> | ListenerHandle
    getLaunchUrl(): Promise<{ url?: string } | undefined>
  }
  browser: { open(o: { url: string }): Promise<void> }
  /** The native HTTP call: asked for the body as text (`responseType: 'text'`), it answers it as received, never parsed. */
  http: { request(o: { url: string; method?: string; headers?: Record<string, string>; data?: string; responseType: 'text' }): Promise<{ status: number; headers: Record<string, string>; data: unknown }> }
}

/** What the page offers the bridge: where it is, and how a browser opens a tab (the platform `web`). */
export interface PageHost {
  open(url: string): void
  console: Pick<Console, 'warn'>
}

/** What a provider module's factory is handed, and only it. */
export interface ProviderKit {
  key: Omit<NativeKey, never>
  store: {
    put(area: 'drafts' | 'cache', key: string, data: string): Promise<void>
    get(area: 'drafts' | 'cache', key: string): Promise<{ data: string | null; savedAt?: string }>
    list(area: 'drafts' | 'cache'): Promise<Array<{ key: string; savedAt: string }>>
    remove(area: 'drafts' | 'cache', key: string): Promise<void>
    clear(): Promise<void>
  }
  push: { token(): string | null; onToken(cb: (token: string) => void): () => void }
  /**
   * The native HTTP call. The request's body is sent exactly as given; the reply's body comes back as its text, exactly as
   * received, never parsed (a provider verifies signatures and hashes over the exact bytes), at most MAX_HTTP_BODY bytes. A reply
   * that is not text is refused `unreadable`, one past the bound `too_large`.
   */
  http: (o: { url: string; method?: string; headers?: Record<string, string>; data?: string }) => Promise<{ status: number; headers: Record<string, string>; data: string }>
}

/** The bridge the workbench reads (web/app/utils/shell.ts types it whole). */
export interface Bridge {
  readonly v: 1
  readonly platform: 'ios' | 'android' | 'web'
  readonly push: {
    permission(): Promise<'granted' | 'denied' | 'prompt'>
    request(): Promise<'granted' | 'denied'>
    onTap(cb: (data: Record<string, string>) => void): () => void
  }
  readonly links: { onOpen(cb: (url: string) => void): () => void }
  openServer(address: string): Promise<{ ok: true; words?: string } | { ok: false; words: string }>
  readonly servers: { recent(): Promise<string[]>; forget(): Promise<void> }
  readonly provider: unknown
  register(factory: unknown): void
}

/** The bounds of Open a server: an address's bytes, and how many are kept. */
export const MAX_ADDRESS = 2048
export const RECENT_SERVERS = 5
/** The bound of a reply's body the native HTTP call hands a provider, in bytes of its UTF-8 text. */
export const MAX_HTTP_BODY = 1024 * 1024
/** The words an `http://` address is opened with. */
export const PLAIN_WORDS = 'This address is not encrypted: use it only on a network you trust'
/** The record in the servers area that keeps the last addresses, newest first. */
const RECENT_KEY = 'recent'

const NO_NATIVE: Refusal = { code: 'no_native', words: 'This shell has no native layer here: nothing is kept or signed.' }
const refuse = (r: Refusal) => Promise.reject({ ...r })

/** Whether s holds a control character (C0, DEL or C1). */
function hasControl(s: string): boolean {
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i)
    if (c < 0x20 || (c >= 0x7f && c <= 0x9f)) return true
  }
  return false
}

/** An address Open a server takes: its URL to open and its words, or why not. */
export function checkAddress(address: unknown): { ok: true; url: string; words?: string } | { ok: false; words: string } {
  if (typeof address !== 'string' || !address.trim()) return { ok: false, words: "Type a Conductor server's address or a decision link." }
  const a = address.trim()
  if (new TextEncoder().encode(a).length > MAX_ADDRESS) return { ok: false, words: `This address is longer than ${MAX_ADDRESS} bytes.` }
  if (hasControl(a)) return { ok: false, words: 'This address holds a character an address cannot.' }
  let u: URL
  try {
    u = new URL(a)
  } catch {
    return { ok: false, words: 'This is not an address: it begins with https:// or http://.' }
  }
  if (u.protocol === 'https:' && u.host) return { ok: true, url: u.href }
  if (u.protocol === 'http:' && u.host) return { ok: true, url: u.href, words: PLAIN_WORDS }
  return { ok: false, words: 'Only an https:// or http:// address opens here.' }
}

/** A tap's data as strings: every value that is a string, unchanged; nothing else. */
function stringRecord(data: unknown): Record<string, string> {
  const out: Record<string, string> = {}
  if (data && typeof data === 'object') {
    for (const [k, v] of Object.entries(data as Record<string, unknown>)) if (typeof v === 'string') out[k] = v
  }
  return out
}

/**
 * A listener added, removed by the handle answered (whether the runtime answers it at once or later); `add` is handed a gate, so
 * nothing reaches the callback once the listener is removed, before the runtime's handle has come back too.
 */
function listen(add: (gate: <T>(cb: (x: T) => void) => (x: T) => void) => Promise<ListenerHandle> | ListenerHandle): () => void {
  let removed = false
  let handle: ListenerHandle | undefined
  const gate = <T>(cb: (x: T) => void) => (x: T) => {
    if (!removed) cb(x)
  }
  const got = add(gate)
  const keep = (h: ListenerHandle) => {
    handle = h
    if (removed) void h.remove()
  }
  if (got && typeof (got as Promise<ListenerHandle>).then === 'function') (got as Promise<ListenerHandle>).then(keep, () => {})
  else keep(got as ListenerHandle)
  return () => {
    if (removed) return
    removed = true
    if (handle) void handle.remove()
  }
}

/** The key and the store a provider is handed with no native layer: every call refused `no_native`. */
function refusingKit(): Pick<ProviderKit, 'key' | 'store' | 'http'> {
  const no = () => refuse(NO_NATIVE)
  return {
    key: { create: no, publicKey: no, state: no, bind: no, sign: no, ending: no, remove: no },
    store: { put: no, get: no, list: no, remove: no, clear: no },
    http: no,
  }
}

/** The store as a provider is handed it: the drafts and cache areas only. */
function providerStore(s: NativeStore): ProviderKit['store'] {
  const area = (a: unknown): a is 'drafts' | 'cache' => a === 'drafts' || a === 'cache'
  const out = (a: unknown) => refuse({ code: 'area_refused', words: 'A provider keeps drafts and its cache here, nothing else.' })
  return {
    put: (a, key, data) => (area(a) ? s.put({ area: a, key, data }) : out(a)),
    get: (a, key) => (area(a) ? s.get({ area: a, key }) : out(a)),
    list: (a) => (area(a) ? s.list({ area: a }).then((r) => r.keys) : out(a)),
    remove: (a, key) => (area(a) ? s.remove({ area: a, key }) : out(a)),
    clear: () => s.clear(),
  }
}

/**
 * Makes the bridge over `native` (none: a browser) for the page `host`. Registration is open until the workbench first reads
 * `provider` (it reads the bridge once, at its start) or `closeRegistration` is called (the page's DOMContentLoaded).
 */
export function makeBridge(native: NativeLayer | null, host: PageHost): { bridge: Bridge; closeRegistration: () => void } {
  let provider: unknown = null
  let registered = false
  let closed = false
  let token: string | null = null
  const tokenListeners = new Set<(t: string) => void>()
  /** Open a server's last addresses with no native layer: this page's alone, kept for its life. */
  let memoryRecent: string[] = []

  if (native) {
    listen((gate) =>
      native.push.addListener(
        'registration',
        gate((t: { value: string }) => {
          if (!t || typeof t.value !== 'string') return
          token = t.value
          for (const cb of tokenListeners) cb(t.value)
        }),
      ),
    )
  }

  async function readRecent(): Promise<string[]> {
    if (!native) return memoryRecent.slice()
    try {
      const r = await native.store.get({ area: 'servers', key: RECENT_KEY })
      const list = r.data ? (JSON.parse(r.data) as unknown) : []
      return Array.isArray(list) ? list.filter((a): a is string => typeof a === 'string' && checkAddress(a).ok).slice(0, RECENT_SERVERS) : []
    } catch {
      return []
    }
  }

  async function keepRecent(url: string): Promise<void> {
    const list = [url, ...(await readRecent()).filter((a) => a !== url)].slice(0, RECENT_SERVERS)
    if (!native) {
      memoryRecent = list
      return
    }
    try {
      await native.store.put({ area: 'servers', key: RECENT_KEY, data: JSON.stringify(list) })
    } catch {
      // Not kept: the address was opened all the same.
    }
  }

  const kit: ProviderKit = native
    ? {
        key: {
          create: (o) => native.key.create(o),
          publicKey: () => native.key.publicKey(),
          state: () => native.key.state(),
          bind: (o) => native.key.bind(o),
          sign: (o) => native.key.sign(o),
          ending: () => native.key.ending(),
          remove: () => native.key.remove(),
        },
        store: providerStore(native.store),
        push: { token: () => token, onToken: (cb) => (tokenListeners.add(cb), () => tokenListeners.delete(cb)) },
        http: async (o) => {
          const r = await native.http.request({ ...o, responseType: 'text' })
          if (typeof r.data !== 'string') return refuse({ code: 'unreadable', words: 'The reply was not handed over as its text.' })
          if (new TextEncoder().encode(r.data).length > MAX_HTTP_BODY) return refuse({ code: 'too_large', words: `The reply is longer than ${MAX_HTTP_BODY} bytes.` })
          return { status: r.status, headers: r.headers, data: r.data }
        },
      }
    : { ...refusingKit(), push: { token: () => null, onToken: () => () => {} } }

  const bridge: Bridge = Object.freeze({
    v: 1 as const,
    platform: native ? native.platform : ('web' as const),
    push: Object.freeze({
      async permission() {
        if (!native) return 'denied' as const
        const r = await native.push.checkPermissions()
        return r.receive === 'granted' ? ('granted' as const) : r.receive === 'denied' ? ('denied' as const) : ('prompt' as const)
      },
      async request() {
        if (!native) return 'denied' as const
        const r = await native.push.requestPermissions()
        if (r.receive !== 'granted') return 'denied' as const
        await native.push.register()
        return 'granted' as const
      },
      onTap(cb: (data: Record<string, string>) => void) {
        if (!native) return () => {}
        return listen((gate) => native.push.addListener('pushNotificationActionPerformed', gate((a: { notification?: { data?: unknown } }) => cb(stringRecord(a?.notification?.data)))))
      },
    }),
    links: Object.freeze({
      onOpen(cb: (url: string) => void) {
        if (!native) return () => {}
        let stopped = false
        void native.app.getLaunchUrl().then(
          (r) => {
            if (!stopped && r && typeof r.url === 'string' && r.url) cb(r.url)
          },
          () => {},
        )
        const stop = listen((gate) => native.app.addListener('appUrlOpen', gate((e: { url: string }) => typeof e?.url === 'string' && cb(e.url))))
        return () => {
          stopped = true
          stop()
        }
      },
    }),
    async openServer(address: string) {
      const c = checkAddress(address)
      if (!c.ok) return { ok: false as const, words: c.words }
      try {
        if (native) await native.browser.open({ url: c.url })
        else host.open(c.url)
      } catch {
        return { ok: false as const, words: 'The address could not be opened.' }
      }
      await keepRecent(c.url)
      return c.words ? { ok: true as const, words: c.words } : { ok: true as const }
    },
    servers: Object.freeze({
      recent: readRecent,
      async forget() {
        memoryRecent = []
        if (native) await native.store.remove({ area: 'servers', key: RECENT_KEY }).catch(() => {})
      },
    }),
    get provider() {
      closed = true
      return provider
    },
    register(factory: unknown) {
      if (registered || closed) {
        host.console.warn(registered ? 'conductorShell.register: a provider is registered already; this one is not' : 'conductorShell.register: the workbench has started; no provider is registered now')
        return
      }
      registered = true
      if (typeof factory !== 'function') {
        host.console.warn('conductorShell.register: a provider registers a factory, a function')
        return
      }
      try {
        const p = (factory as (k: ProviderKit) => unknown)(kit)
        provider = p && typeof p === 'object' ? p : null
        if (!provider) host.console.warn('conductorShell.register: the factory answered no provider')
      } catch (e) {
        provider = null
        host.console.warn('conductorShell.register: the factory failed', e instanceof Error ? e.message : String(e))
      }
    },
  })
  return { bridge, closeRegistration: () => (closed = true) }
}
