import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// The fragment plugin over stubs of the browser (the address, the history, the window's listeners, the tab's storage) and of the two
// stores, recording what it asks of each in order: what it does first at a load, and where a later link's token goes. Tokens are
// invented strings.

const LATER = 'later-token-0123456789'

let order: string[]
let href: string
let tab: Map<string, string>
let popstate: ((e: { stopImmediatePropagation: () => void }) => void) | undefined
const fromLaterLink = vi.fn()

// The plugin, imported once as this file is loaded, with the framework's `defineNuxtPlugin` stubbed to give back the definition
// before the import: no test's own time holds the import, and each load runs its setup again.
vi.stubGlobal('defineNuxtPlugin', (p: unknown) => p)
const plugin = (await import('./token-fragment.client')).default as unknown as { setup: () => void }
vi.unstubAllGlobals()

/** A load of the page at `at`: the plugin's setup, run as Nuxt runs it. */
function load(at: string) {
  href = at
  vi.stubGlobal('location', {
    get href() {
      return href
    },
    get pathname() {
      return new URL(href).pathname
    },
    reload: vi.fn(),
  })
  vi.stubGlobal('history', { state: null, replaceState: (_s: unknown, _u: string, url: string) => void (href = url) })
  vi.stubGlobal('window', { addEventListener: (type: string, fn: typeof popstate) => void (type === 'popstate' && (popstate = fn)) })
  vi.stubGlobal('sessionStorage', {
    removeItem: (k: string) => {
      order.push(`tab: remove ${k}`)
      tab.delete(k)
    },
  })
  vi.stubGlobal('useWorkbenchToken', () => {
    order.push('workbench token store')
    return { fromLink: (v: string) => void order.push(`fromLink ${v}`), fromLaterLink }
  })
  vi.stubGlobal('useIdentityPass', () => {
    order.push('pass store')
    return { set: vi.fn(), stored: () => false }
  })
  plugin.setup()
}

/** A fragment that arrives after the load: the address changes, and the window's popstate runs the plugin's listener. */
function later(fragment: string) {
  href = href.replace(/#.*$/, '') + fragment
  const stop = vi.fn()
  popstate!({ stopImmediatePropagation: stop })
  return stop
}

beforeEach(() => {
  order = []
  tab = new Map([
    ['nuxt:reload:state', '{"state":{}}'],
    ['conductor.identityPass', 'e2e.pass-of_ana.7'],
  ])
  popstate = undefined
  fromLaterLink.mockReset()
})
afterEach(() => vi.unstubAllGlobals())

describe('the fragment plugin', () => {
  it("removes, at every load and first in this plugin, the page's state that the framework's own reload leaves in the tab's storage", () => {
    load('http://app.test/sessions')
    expect(order[0]).toBe('tab: remove nuxt:reload:state')
    expect([...tab.keys()]).toEqual(['conductor.identityPass'])
    expect(order).toEqual(['tab: remove nuxt:reload:state', 'workbench token store', 'pass store', 'fromLink '])
  })

  it("drops a later link's token on a decide address in any case of its letters, with the address cleaned, and hands it to the store on any other", () => {
    for (const path of ['/decide', '/decide/', '/decide/dr-7', '/DECIDE', '/Decide/', '/DeCiDe/dr-7']) {
      load(`http://app.test${path}`)
      const stop = later(`#token=${LATER}`)
      expect(stop, path).toHaveBeenCalledTimes(1)
      expect(href, path).toBe(`http://app.test${path}`)
      expect(fromLaterLink, path).not.toHaveBeenCalled()
    }
    for (const path of ['/', '/decisions', '/decided', '/DECIDED', '/decide/dr-7/x']) {
      fromLaterLink.mockReset()
      load(`http://app.test${path}`)
      later(`#token=${LATER}`)
      expect(href, path).toBe(`http://app.test${path}`)
      expect(fromLaterLink.mock.calls, path).toEqual([[LATER]])
    }
  })
})
