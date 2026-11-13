import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// The pass store (docs/plans/decide-grant.md, "web/app"): the tab's sessionStorage, never localStorage, in memory when storage is
// refused; the workbench token's key never touched. Each test loads the store anew, as a new tab's page does.

class FakeStorage {
  m = new Map<string, string>()
  getItem = (k: string) => this.m.get(k) ?? null
  setItem = (k: string, v: string) => void this.m.set(k, v)
  removeItem = (k: string) => void this.m.delete(k)
}

let session: FakeStorage
let local: FakeStorage
const load = async () => (await import('./useIdentityPass')) as typeof import('./useIdentityPass')

beforeEach(() => {
  vi.resetModules()
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

  it('is empty after end(), and marks the access ended until a new pass', async () => {
    const { useIdentityPass, passHeld } = await load()
    const s = useIdentityPass()
    s.set('p.1')
    s.end()
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
    useIdentityPass().end()
    expect(passHeld()).toBe(false)
    expect(local.m.size).toBe(1)
  })
})
