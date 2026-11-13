import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { computed, ref, type Ref } from 'vue'
import { useWorkbenchToken } from './useWorkbenchToken'

// The workbench token's store in a browser (the desktop app's bridge is the browser e2e's, token-fragment.spec.ts): the token this
// page sends, the one the browser keeps (localStorage), and whether the page's is a link's the server has not answered yet. Each
// `load` is a new page of the tab: a new useState, the page reading what the storage keeps, as the store does at a load. Tokens are
// invented strings.

const KEY = 'conductor.workbenchToken'
const LINK = 'conductor.workbenchTokenLink'
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
afterEach(() => vi.unstubAllGlobals())

describe("a load with a link's token", () => {
  it('sends it, keeps nothing until an answer, and keeps it once one is accepted', () => {
    const s = load(LINKED)
    expect(s.token.value).toBe(LINKED)
    expect(local.getItem(KEY)).toBeNull()
    expect(s.kept()).toBe('')
    s.answered(LINKED, true)
    expect(local.getItem(KEY)).toBe(LINKED)
    expect(s.kept()).toBe(LINKED)
    expect(reload).not.toHaveBeenCalled()
  })

  it('leaves the token the browser keeps until the link is accepted, then replaces it', () => {
    local.setItem(KEY, KEPT)
    const s = load(LINKED)
    expect(s.token.value).toBe(LINKED)
    expect(s.kept()).toBe(KEPT)
    s.answered(KEPT, true)
    expect(local.getItem(KEY)).toBe(KEPT)
    s.answered(LINKED, true)
    expect(local.getItem(KEY)).toBe(LINKED)
  })

  it('drops a refused one: the page loads again once, the kept token stays, and no answer after it asks for a token or keeps it', () => {
    local.setItem(KEY, KEPT)
    const s = load(LINKED)
    s.answered(LINKED, false)
    expect(reload).toHaveBeenCalledTimes(1)
    expect(local.getItem(KEY)).toBe(KEPT)
    expect(s.needsToken.value).toBe(false)
    s.answered(LINKED, false)
    s.answered(LINKED, true)
    expect(reload).toHaveBeenCalledTimes(1)
    expect(local.getItem(KEY)).toBe(KEPT)
    expect(s.needsToken.value).toBe(false)
    // The next load: the address holds no link, and the page sends the kept token.
    expect(load().token.value).toBe(KEPT)
  })

  it('takes a link carrying the token the browser keeps as the kept one: a refusal removes it and asks for a token', () => {
    local.setItem(KEY, KEPT)
    const s = load(KEPT)
    s.answered(KEPT, false)
    expect(reload).not.toHaveBeenCalled()
    expect(local.getItem(KEY)).toBeNull()
    expect(s.needsToken.value).toBe(true)
  })

  it('holds an accepted one for the page alone when the browser refuses to keep it', () => {
    const s = load(LINKED)
    local.refuse = true
    s.answered(LINKED, true)
    expect(local.m.size).toBe(0)
    expect(s.token.value).toBe(LINKED)
    expect(s.kept()).toBe(LINKED)
  })

  it("goes on with the operator's token once one is saved: a late answer for the link's keeps nothing", () => {
    const s = load(LINKED)
    s.set(KEPT)
    s.answered(LINKED, true)
    s.answered(LINKED, false)
    expect(local.getItem(KEY)).toBe(KEPT)
    expect(reload).not.toHaveBeenCalled()
    expect(s.needsToken.value).toBe(false)
  })
})

describe('a token the browser keeps', () => {
  it('is sent by a load with no link, and an accepted answer changes nothing', () => {
    local.setItem(KEY, KEPT)
    const s = load()
    expect(s.token.value).toBe(KEPT)
    s.answered(KEPT, true)
    expect(local.getItem(KEY)).toBe(KEPT)
    expect(s.kept()).toBe(KEPT)
  })

  it('is removed when the server refuses it, and the gate asks for a token', () => {
    local.setItem(KEY, KEPT)
    const s = load()
    s.answered(KEPT, false)
    expect(local.getItem(KEY)).toBeNull()
    expect(s.kept()).toBe('')
    expect(s.needsToken.value).toBe(true)
    expect(reload).not.toHaveBeenCalled()
  })

  it('kept later is not removed by the refusal of a request sent before it', () => {
    local.setItem(KEY, KEPT)
    const s = load()
    // Another page of this browser keeps another token while this page's request is on its way.
    local.setItem(KEY, LATER)
    s.answered(KEPT, false)
    expect(local.getItem(KEY)).toBe(LATER)
    expect(s.needsToken.value).toBe(true)
  })

  it('saved by the operator is not removed, nor asked for, by the refusal of the token before it', () => {
    local.setItem(KEY, KEPT)
    const s = load()
    s.set(LATER)
    s.answered(KEPT, false)
    expect(local.getItem(KEY)).toBe(LATER)
    expect(s.needsToken.value).toBe(false)
  })

  it('held for the page alone is no longer held once refused, and a later link is then taken', () => {
    local.refuse = true
    const s = load()
    s.set(KEPT)
    expect(s.kept()).toBe(KEPT)
    expect(s.handOn(LATER)).toBe(false)
    s.answered(KEPT, false)
    expect(s.kept()).toBe('')
    expect(s.handOn(LATER)).toBe(true)
  })
})

describe("a later link's token", () => {
  it('is handed to the next load of the tab alone: this page sends its own, the browser keeps its own', () => {
    local.setItem(KEY, KEPT)
    const s = load()
    expect(s.handOn(LATER)).toBe(true)
    expect(s.token.value).toBe(KEPT)
    expect(local.getItem(KEY)).toBe(KEPT)
    expect(session.getItem(LINK)).toBe(LATER)
    // The next load takes it as a link's, and removes it from the tab's storage.
    const next = load()
    expect(next.token.value).toBe(LATER)
    expect(session.getItem(LINK)).toBeNull()
    expect(local.getItem(KEY)).toBe(KEPT)
    next.answered(LATER, false)
    expect(reload).toHaveBeenCalledTimes(1)
    expect(load().token.value).toBe(KEPT)
  })

  it("gives way to a load's own link, and is removed all the same", () => {
    session.setItem(LINK, LATER)
    expect(load(LINKED).token.value).toBe(LINKED)
    expect(session.getItem(LINK)).toBeNull()
  })

  it("is not handed on when it is the token this page sends or the browser keeps, or when the tab's storage refuses it", () => {
    local.setItem(KEY, KEPT)
    const s = load(LINKED)
    expect(s.handOn(KEPT)).toBe(false)
    expect(s.handOn(LINKED)).toBe(false)
    session.refuse = true
    expect(s.handOn(LATER)).toBe(false)
    expect(session.m.size).toBe(0)
  })
})

describe("the decide page's rule", () => {
  it("counts the kept token alone: a link's not yet accepted, or one refused, makes no browser the operator's", () => {
    const s = load(LINKED)
    expect(s.hasToken.value).toBe(true)
    expect(s.kept()).toBe('')
    local.setItem(KEY, KEPT)
    expect(load(LINKED).kept()).toBe(KEPT)
    const refused = load()
    refused.answered(KEPT, false)
    expect(refused.hasToken.value).toBe(true)
    expect(refused.kept()).toBe('')
  })
})
