import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { computed, ref, type Ref } from 'vue'
import { useWorkbenchToken } from './useWorkbenchToken'

// The workbench token's store in a browser (the desktop app's bridge is the browser e2e's, token-fragment.spec.ts): the token this
// page sends, the one the browser keeps (localStorage), and what the page knows of its token (a link's being tried, or one the server
// refused). Each `load` is a new page of the tab: a new useState, the page reading what the storage keeps, as the store does at a load.
// The tab's own storage is watched too: the store writes nothing there, in any case. Tokens are invented strings.

const KEY = 'conductor.workbenchToken'
const KEPT = 'kept-token-0123456789'
const LINKED = 'link-token-0123456789'
const LATER = 'later-token-0123456789'

class FakeStorage {
  m = new Map<string, string>()
  refuse = false
  getItem = (k: string) => this.m.get(k) ?? null
  setItem = (k: string, v: string) => {
    if (this.refuse) throw new DOMException('refused', 'QuotaExceededError')
    this.m.set(k, v)
  }
  removeItem = (k: string) => {
    if (this.refuse) throw new DOMException('refused', 'SecurityError')
    this.m.delete(k)
  }
}

let local: FakeStorage
let session: FakeStorage
const reload = vi.fn()

/** A load of the page: a new useState, with what the storage keeps as the token sent; then the plugin's `fromLink`. */
function load(link = '') {
  const state = new Map<string, Ref<unknown>>()
  vi.stubGlobal('useState', (key: string, init: () => unknown) => {
    if (!state.has(key)) state.set(key, ref(key === 'workbenchToken' ? (local.getItem(KEY) ?? '') : init()))
    return state.get(key)
  })
  const s = useWorkbenchToken()
  s.fromLink(link)
  return s
}

beforeEach(() => {
  local = new FakeStorage()
  session = new FakeStorage()
  reload.mockReset()
  vi.stubGlobal('localStorage', local)
  vi.stubGlobal('sessionStorage', session)
  vi.stubGlobal('location', { reload })
  vi.stubGlobal('computed', computed)
})
afterEach(() => {
  expect(session.m.size, "the tab's storage").toBe(0)
  vi.unstubAllGlobals()
})

describe("a load with a link's token", () => {
  it('nothing kept: sends it, keeps nothing until it is accepted, then keeps it', () => {
    const s = load(LINKED)
    expect(s.token.value).toBe(LINKED)
    expect(local.getItem(KEY)).toBeNull()
    expect(s.operator()).toBe(true)
    s.answered(LINKED, true)
    expect(local.getItem(KEY)).toBe(LINKED)
    expect(s.token.value).toBe(LINKED)
    expect(reload).not.toHaveBeenCalled()
    expect(load().token.value).toBe(LINKED)
  })

  it('the same token kept: taken as the kept one, and an accepted answer changes nothing', () => {
    local.setItem(KEY, KEPT)
    const s = load(KEPT)
    s.answered(KEPT, true)
    expect(local.getItem(KEY)).toBe(KEPT)
    expect(s.token.value).toBe(KEPT)
    // Taken as the kept one, not tried: a refusal asks for a token, and the page does not load again.
    s.answered(KEPT, false)
    expect(reload).not.toHaveBeenCalled()
    expect(s.needsToken.value).toBe(true)
  })

  it("another token kept: once the link's is accepted the page sends it, the browser keeps the other, and a load without the link sends the other", () => {
    local.setItem(KEY, KEPT)
    const s = load(LINKED)
    expect(s.token.value).toBe(LINKED)
    s.answered(KEPT, true)
    s.answered(LINKED, true)
    expect(s.token.value).toBe(LINKED)
    expect(local.getItem(KEY)).toBe(KEPT)
    expect(s.operator()).toBe(true)
    expect(reload).not.toHaveBeenCalled()
    expect(load().token.value).toBe(KEPT)
  })

  it('refused: the page loads again once, the kept token stays, and no answer after it asks for a token, keeps it, or counts for the decide page', () => {
    local.setItem(KEY, KEPT)
    const s = load(LINKED)
    s.answered(LINKED, false)
    expect(reload).toHaveBeenCalledTimes(1)
    expect(local.getItem(KEY)).toBe(KEPT)
    expect(s.needsToken.value).toBe(false)
    expect(s.operator()).toBe(false)
    s.answered(LINKED, false)
    s.answered(LINKED, true)
    expect(reload).toHaveBeenCalledTimes(1)
    expect(local.getItem(KEY)).toBe(KEPT)
    expect(s.needsToken.value).toBe(false)
    // The next load: the address holds no link, and the page sends the kept token.
    expect(load().token.value).toBe(KEPT)
  })

  it("the operator saves a token at the gate while the link's is tried: the operator's is kept, and the link's answers decide nothing", () => {
    const s = load(LINKED)
    s.set(KEPT)
    s.answered(LINKED, true)
    s.answered(LINKED, false)
    expect(local.getItem(KEY)).toBe(KEPT)
    expect(s.token.value).toBe(KEPT)
    expect(reload).not.toHaveBeenCalled()
    expect(s.needsToken.value).toBe(false)
  })

  it('accepted while the browser refuses to keep it: held for the page alone', () => {
    const s = load(LINKED)
    local.refuse = true
    s.answered(LINKED, true)
    expect(local.m.size).toBe(0)
    expect(s.token.value).toBe(LINKED)
    expect(s.operator()).toBe(true)
  })
})

describe('a token the browser keeps', () => {
  it('is sent by a load with no link, and an accepted answer changes nothing', () => {
    local.setItem(KEY, KEPT)
    const s = load()
    expect(s.token.value).toBe(KEPT)
    expect(s.operator()).toBe(true)
    s.answered(KEPT, true)
    expect(local.getItem(KEY)).toBe(KEPT)
  })

  it("refused: stays kept, the gate asks, the decide page's rule counts it no more in this page, and a load counts it again", () => {
    local.setItem(KEY, KEPT)
    const s = load()
    s.answered(KEPT, false)
    expect(local.getItem(KEY)).toBe(KEPT)
    expect(s.token.value).toBe(KEPT)
    expect(s.needsToken.value).toBe(true)
    expect(s.operator()).toBe(false)
    expect(reload).not.toHaveBeenCalled()
    const next = load()
    expect(next.token.value).toBe(KEPT)
    expect(next.operator()).toBe(true)
  })

  it("refused late, for the token before the one the operator saved and the browser refused to keep: the page's token and the storage stay, and nothing is asked", () => {
    local.setItem(KEY, KEPT)
    const s = load()
    local.refuse = true
    s.set(LATER)
    s.answered(KEPT, false)
    expect(s.token.value).toBe(LATER)
    expect(local.getItem(KEY)).toBe(KEPT)
    expect(s.needsToken.value).toBe(false)
    expect(s.operator()).toBe(true)
  })

  it('is forgotten by "Forget token": nothing sent, nothing kept, and a later link is then taken', () => {
    local.setItem(KEY, KEPT)
    const s = load()
    s.clear()
    expect(s.token.value).toBe('')
    expect(local.getItem(KEY)).toBeNull()
    expect(s.operator()).toBe(false)
    s.fromLaterLink(LATER)
    expect(s.token.value).toBe(LATER)
  })
})

describe("a later link's token", () => {
  it('in a page that holds the kept token, or a link\'s being tried: dropped; the page sends its own, the browser keeps its own, and no load', () => {
    local.setItem(KEY, KEPT)
    const s = load()
    s.fromLaterLink(LATER)
    expect(s.token.value).toBe(KEPT)
    expect(local.getItem(KEY)).toBe(KEPT)
    const trying = load(LINKED)
    trying.fromLaterLink(LATER)
    expect(trying.token.value).toBe(LINKED)
    trying.answered(LATER, true)
    trying.answered(LATER, false)
    expect(local.getItem(KEY)).toBe(KEPT)
    expect(reload).not.toHaveBeenCalled()
  })

  it('in a page that holds none: taken as a load\'s link and tried, with no load; accepted, kept, and the gate is done', () => {
    const s = load()
    s.needsToken.value = true
    s.fromLaterLink(LATER)
    expect(s.token.value).toBe(LATER)
    expect(s.needsToken.value).toBe(true)
    expect(local.getItem(KEY)).toBeNull()
    s.answered(LATER, true)
    expect(local.getItem(KEY)).toBe(LATER)
    expect(s.needsToken.value).toBe(false)
    expect(reload).not.toHaveBeenCalled()
  })

  it('in a page whose kept token was refused: taken and tried; accepted, sent, while the browser keeps the refused one', () => {
    local.setItem(KEY, KEPT)
    const s = load()
    s.answered(KEPT, false)
    s.fromLaterLink(LATER)
    expect(s.token.value).toBe(LATER)
    expect(s.operator()).toBe(true)
    s.answered(KEPT, false)
    s.answered(LATER, true)
    expect(s.token.value).toBe(LATER)
    expect(local.getItem(KEY)).toBe(KEPT)
    expect(s.needsToken.value).toBe(false)
    expect(reload).not.toHaveBeenCalled()
  })

  it('in a page that holds none, refused: the page loads again, and nothing is kept', () => {
    const s = load()
    s.needsToken.value = true
    s.fromLaterLink(LATER)
    s.answered(LATER, false)
    expect(reload).toHaveBeenCalledTimes(1)
    expect(local.m.size).toBe(0)
  })
})
