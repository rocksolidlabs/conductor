import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// The pass store (docs/plans/decide-grant.md, "web/app"): the tab's sessionStorage, never localStorage, in memory when storage is
// refused; the workbench token's key never touched. Each test loads the store anew, as a new tab's page does.
class FakeStorage {
  m = new Map<string, string>()
  getItem = (k: string) => this.m.get(k) ?? null
  setItem = (k: string, v: string) => void this.m.set(k, v)
  removeItem = (k: string) => void this.m.delete(k)
}

type Store = typeof import('./useIdentityPass')

// The store's loads are made while the file is collected, never inside a test: a load is a request to vitest's module server,
// which a loaded machine's whole run keeps busy for seconds, and there no test's timeout runs. Each load after resetModules is a
// new evaluation of the module, holding nothing yet: the store reads the tab's storage at its first use, not at its load, so a
// test hands each instance the storage it stubbed. LOADS is how many the tests below take, each once.
const LOADS = 10
const loaded: Store[] = []
for (let i = 0; i < LOADS; i++) {
  vi.resetModules()
  loaded.push(await import('./useIdentityPass'))
}
if (new Set(loaded.map((m) => m.useIdentityPass)).size !== LOADS) throw new Error('a load gave back an instance another load made')
const load = async (): Promise<Store> => {
  const store = loaded.shift()
  if (!store) throw new Error(`the tests take more than the ${LOADS} loads the file made`)
  return store
}

let session: FakeStorage
let local: FakeStorage

beforeEach(() => {
  session = new FakeStorage()
  local = new FakeStorage()
  local.setItem('conductor.workbenchToken', 'workbench-token-0123456789')
  vi.stubGlobal('sessionStorage', session)
  vi.stubGlobal('localStorage', local)
})
afterEach(() => vi.unstubAllGlobals())

describe('the pass store', () => {
  it("keeps the pass in the tab's sessionStorage, never localStorage, and never replaces the workbench token", async () => {
    const { useIdentityPass, passHeld } = await load()
    expect(passHeld()).toBe(false)
    useIdentityPass().set('p.1')
    expect(session.getItem('conductor.identityPass')).toBe('p.1')
    expect([...local.m]).toEqual([['conductor.workbenchToken', 'workbench-token-0123456789']])
    expect(passHeld()).toBe(true)
  })

  it('reads the pass a reload of the tab finds', async () => {
    session.setItem('conductor.identityPass', 'p.kept')
    const { useIdentityPass } = await load()
    expect(useIdentityPass().get()).toBe('p.kept')
  })

  it('replaces the old pass with a new one', async () => {
    const { useIdentityPass } = await load()
    useIdentityPass().set('p.1')
    useIdentityPass().set('p.2')
    expect(useIdentityPass().get()).toBe('p.2')
    expect(session.getItem('conductor.identityPass')).toBe('p.2')
  })

  it('is empty after end() of the pass it holds, and marks the access ended until a new pass', async () => {
    const { useIdentityPass, passHeld } = await load()
    const s = useIdentityPass()
    s.set('p.1')
    s.end('p.1')
    expect(s.get()).toBe('')
    expect(passHeld()).toBe(false)
    expect(session.m.size).toBe(0)
    expect(s.ended.value).toBe(true)
    expect(local.getItem('conductor.workbenchToken')).toBe('workbench-token-0123456789')
    s.set('p.2')
    expect(s.ended.value).toBe(false)
  })

  it('holds the pass in memory when storage is refused', async () => {
    vi.stubGlobal('sessionStorage', {
      getItem: () => {
        throw new Error('refused')
      },
      setItem: () => {
        throw new Error('refused')
      },
      removeItem: () => {
        throw new Error('refused')
      },
    })
    const { useIdentityPass, passHeld } = await load()
    useIdentityPass().set('p.1')
    expect(useIdentityPass().get()).toBe('p.1')
    expect(passHeld()).toBe(true)
    expect(useIdentityPass().stored('p.2')).toBe(false)
    expect(useIdentityPass().get()).toBe('p.1')
    useIdentityPass().end('p.1')
    expect(passHeld()).toBe(false)
    expect(local.m.size).toBe(1)
  })

  it("writes a later pass to the tab's storage alone, and answers true only when the storage took one that differs", async () => {
    const { useIdentityPass } = await load()
    const s = useIdentityPass()
    s.set('p.1')
    expect(s.stored('p.1')).toBe(false)
    expect(s.stored('p.2')).toBe(true)
    expect(s.get()).toBe('p.1')
    expect(session.getItem('conductor.identityPass')).toBe('p.2')
    expect(s.stored('p.2')).toBe(false)
    expect([...local.m]).toEqual([['conductor.workbenchToken', 'workbench-token-0123456789']])
  })

  it("keeps the pass this page read when a later one is stored before it was ever asked for", async () => {
    session.setItem('conductor.identityPass', 'p.1')
    const { useIdentityPass } = await load()
    expect(useIdentityPass().stored('p.2')).toBe(true)
    expect(useIdentityPass().get()).toBe('p.1')
  })

  it('ends the pass a 401 refused and no other: a later pass the storage holds is kept for the next load', async () => {
    const { useIdentityPass } = await load()
    const s = useIdentityPass()
    s.set('p.1')
    s.stored('p.2')
    s.end('p.1')
    expect(s.get()).toBe('')
    expect(s.ended.value).toBe(true)
    expect(session.getItem('conductor.identityPass')).toBe('p.2')
    expect((await load()).useIdentityPass().get()).toBe('p.2')
  })

  it("ends neither the page's pass nor the stored one at a 401 of another pass", async () => {
    const { useIdentityPass } = await load()
    const s = useIdentityPass()
    s.set('p.1')
    s.end('p.0')
    expect(s.get()).toBe('p.1')
    expect(session.getItem('conductor.identityPass')).toBe('p.1')
  })
})
