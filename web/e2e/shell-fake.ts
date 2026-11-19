import type { Page } from '@playwright/test'

// A phone shell's bridge injected into the page (docs/plans/phone-shell.md, Tests per surface: "the bridge injected by
// page.addInitScript"), with a fake provider of the tests' own that names no service. Everything it is asked is recorded in the page
// (`window.__shell`); a test changes what it answers, and releases a reply it holds, through `shellDo`.

/** What the fake answers, set as the page starts. */
export interface FakeConfig {
  /** null: a shell with no provider. */
  provider: null | {
    name: string
    state: Record<string, unknown>
    /** What signIn answers. */
    signIn?: Record<string, unknown>
    /** What test answers. */
    test?: Record<string, unknown>
    /** The inbox's items, by id (OSS-2b). */
    items?: Record<string, unknown>
    /** What route answers for a link, by the link. */
    routes?: Record<string, string>
    /** What resolve answers for a handle, by the handle. */
    handles?: Record<string, unknown>
  }
}

/** Installs the bridge and its fake provider in every document of the page. */
export async function fakeShell(page: Page, cfg: FakeConfig): Promise<void> {
  await page.addInitScript((c: FakeConfig) => {
    type Rec = Record<string, unknown>
    const rec = {
      calls: [] as string[],
      opened: [] as string[],
      windows: [] as string[],
      recent: [] as string[],
      responds: [] as Rec[],
      drafts: {} as Record<string, Rec>,
      held: {} as Record<string, Array<() => void>>,
      hold: [] as string[],
      offline: false,
    }
    ;(window as unknown as { __shell: unknown }).__shell = rec
    window.open = ((url: string) => {
      rec.windows.push(String(url))
      return null
    }) as typeof window.open
    const stateListeners = new Set<(s: unknown) => void>()
    const watchers = new Set<(id: string) => void>()
    const tapListeners = new Set<(d: Record<string, string>) => void>()
    const linkListeners = new Set<(u: string) => void>()
    const p = c.provider
    /** A call recorded, answered once released when the test holds that method. */
    const call = async <T>(name: string, answer: () => T | Promise<T>): Promise<T> => {
      rec.calls.push(name)
      if (rec.hold.includes(name)) await new Promise<void>((r) => (rec.held[name] ??= []).push(r))
      return answer()
    }
    const clone = <T>(x: T): T => JSON.parse(JSON.stringify(x))
    const item = (id: string) => {
      const it = p?.items?.[id]
      if (!it) throw { code: 'not_visible', words: 'This decision is not one you can see here' }
      return { ...clone(it), offline: rec.offline } as Rec
    }
    const provider = p && {
      name: p.name,
      state: () => call('state', () => clone(p.state)),
      onState: (cb: (s: unknown) => void) => {
        rec.calls.push('onState')
        stateListeners.add(cb)
        return () => stateListeners.delete(cb)
      },
      signIn: () => call('signIn', () => clone(p.signIn ?? { words: 'No sign-in can begin now.' })),
      cancelSignIn: () => call('cancelSignIn', () => undefined),
      signOut: () =>
        call('signOut', () => {
          p.state = { ...p.state, signedIn: false, ending: true }
          rec.drafts = {}
          for (const l of stateListeners) l(clone(p.state))
        }),
      enableNotifications: () => call('enableNotifications', () => 'on' as const),
      test: () => call('test', () => clone(p.test ?? { words: 'A test notification was sent to this phone.' })),
      route: (url: string) => call('route', () => p.routes?.[url] ?? null),
      inbox: {
        list: () =>
          call('list', () => {
            if (rec.offline) throw { code: 'unreachable', words: `${p.name} cannot be reached` }
            return { items: Object.keys(p.items ?? {}).map(item), asOf: new Date().toISOString() }
          }),
        item: (id: string) =>
          call('item', () => {
            if (rec.offline) throw { code: 'offline', words: 'You are offline' }
            return item(id)
          }),
        respond: (id: string, a: Rec) =>
          call('respond', () => {
            rec.responds.push({ id, ...clone(a) })
            const it = p.items?.[id] as Rec | undefined
            const reply = (it?.reply as Rec | undefined) ?? { outcome: 'kept' }
            if (reply.outcome === 'kept' && it) it.own = { responseId: a.responseId, state: 'answered', words: 'Your answer was recorded' }
            return { ...clone(reply), ...(it ? { item: item(id) } : {}) }
          }),
        answerOf: (id: string, responseId: string) =>
          call('answerOf', () => {
            const own = (p.items?.[id] as Rec | undefined)?.own as Rec | undefined
            return own && own.responseId === responseId ? { state: own.state, words: own.words, h: rec.drafts[id]?.h ?? '' } : { state: 'none' }
          }),
        resolve: (handle: string) => call('resolve', () => clone(p.handles?.[handle] ?? { code: 'unknown_handle', words: 'unknown' })),
        watch: (cb: (id: string) => void) => {
          rec.calls.push('watch')
          watchers.add(cb)
          return () => watchers.delete(cb)
        },
      },
      drafts: {
        get: (id: string) => call('draftGet', () => clone(rec.drafts[id] ?? null)),
        list: () => call('draftList', () => clone(Object.values(rec.drafts))),
        put: (d: Rec) =>
          call('draftPut', () => {
            if (!p.state.signedIn) return { ok: false, code: 'signed_out', words: 'Signed out: not saved' }
            rec.drafts[String(d.id)] = clone(d)
            return { ok: true }
          }),
        remove: (id: string) =>
          call('draftRemove', () => {
            delete rec.drafts[id]
          }),
      },
    }
    const bridge = {
      v: 1,
      platform: 'ios',
      push: {
        permission: async () => 'granted',
        request: async () => 'granted',
        onTap: (cb: (d: Record<string, string>) => void) => (tapListeners.add(cb), () => tapListeners.delete(cb)),
      },
      links: { onOpen: (cb: (u: string) => void) => (linkListeners.add(cb), () => linkListeners.delete(cb)) },
      openServer: async (address: string) => {
        rec.opened.push(address)
        let u: URL
        try {
          u = new URL(address.trim())
        } catch {
          return { ok: false, words: 'This is not an address: it begins with https:// or http://.' }
        }
        if (u.protocol !== 'https:' && u.protocol !== 'http:') return { ok: false, words: 'Only an https:// or http:// address opens here.' }
        rec.recent = [u.href, ...rec.recent.filter((a) => a !== u.href)].slice(0, 5)
        return u.protocol === 'http:' ? { ok: true, words: 'This address is not encrypted: use it only on a network you trust' } : { ok: true }
      },
      servers: { recent: async () => rec.recent.slice(), forget: async () => void (rec.recent = []) },
      provider,
      register: () => {},
    }
    Object.assign(rec, {
      setState: (s: Rec) => {
        if (!p) return
        p.state = s
        for (const l of stateListeners) l(clone(s))
      },
      setItem: (id: string, it: Rec) => {
        if (!p) return
        p.items = { ...(p.items ?? {}), [id]: it }
        for (const w of watchers) w(id)
      },
      release: (name: string) => {
        rec.hold = rec.hold.filter((h) => h !== name)
        for (const r of rec.held[name]?.splice(0) ?? []) r()
      },
      tap: (d: Record<string, string>) => tapListeners.forEach((l) => l(d)),
      link: (u: string) => linkListeners.forEach((l) => l(u)),
    })
    Object.defineProperty(window, 'conductorShell', { value: Object.freeze(bridge), configurable: false })
  }, cfg)
}

/** What the fake has recorded, read from the page. */
export function shellRecord(page: Page): Promise<{ calls: string[]; opened: string[]; windows: string[]; recent: string[]; responds: Array<Record<string, unknown>>; drafts: Record<string, Record<string, unknown>> }> {
  return page.evaluate(() => {
    const r = (window as unknown as { __shell: Record<string, unknown> }).__shell
    return JSON.parse(JSON.stringify({ calls: r.calls, opened: r.opened, windows: r.windows, recent: r.recent, responds: r.responds, drafts: r.drafts }))
  })
}

/** Runs one of the fake's controls in the page: setState, setItem, release, tap, link; or sets a field (hold, offline). */
export function shellDo(page: Page, what: string, ...args: unknown[]): Promise<void> {
  return page.evaluate(
    ([w, a]) => {
      const r = (window as unknown as { __shell: Record<string, unknown> }).__shell
      if (typeof r[w as string] === 'function') (r[w as string] as (...x: unknown[]) => void)(...(a as unknown[]))
      else r[w as string] = (a as unknown[])[0]
    },
    [what, args] as const,
  )
}
