import { describe, expect, it, vi } from 'vitest'

vi.mock('electron', () => ({ app: {}, BrowserWindow: class {}, screen: {}, shell: {}, session: {} }))
import { fenceLocalPage, fenceWorkbench, type FenceContents } from '../src/window'

type Nav = (e: { preventDefault(): void; isMainFrame?: boolean }, url: string) => void
type Open = (d: { url: string }) => { action: 'allow' | 'deny'; overrideBrowserWindowOptions?: { webPreferences?: { preload?: string } } }

/** A window's webContents as the fences see it: the handlers they put on it, kept to be called as Electron calls them. */
class Contents implements FenceContents {
  navigate: Nav | null = null
  redirect: Nav | null = null
  created: ((child: { webContents: FenceContents }) => void) | null = null
  open: Open | null = null
  on(event: string, fn: never): unknown {
    if (event === 'will-navigate') this.navigate = fn
    if (event === 'will-redirect') this.redirect = fn
    if (event === 'did-create-window') this.created = fn
    return this
  }
  setWindowOpenHandler(fn: never): void {
    this.open = fn
  }
  /** A navigation (or a redirect) of the window's page to url: whether it went on. */
  goes(kind: 'navigate' | 'redirect', url: string, isMainFrame?: boolean): boolean {
    let stopped = false
    this[kind]!({ preventDefault: () => void (stopped = true), isMainFrame }, url)
    return !stopped
  }
  /** A window the page opens: the fences on it, as Electron makes it once the handler allowed it. */
  child(): Contents {
    const c = new Contents()
    this.created!({ webContents: c })
    return c
  }
}

const origin = 'http://127.0.0.1:4312'

function workbench(trusted = () => origin) {
  const outside: string[] = []
  const wc = new Contents()
  fenceWorkbench(wc, { origin: trusted, preload: '/app/preload.js', openExternal: (url) => void outside.push(url) })
  return { wc, outside }
}

describe('every window of the app keeps the same fences', () => {
  it("keeps the workbench's window on the server's origin: a link elsewhere opens outside the app, and nothing else does", () => {
    const { wc, outside } = workbench()
    expect(wc.goes('navigate', origin + '/sessions/s1')).toBe(true)
    expect(wc.goes('navigate', 'https://example.com/')).toBe(false)
    expect(wc.goes('navigate', 'http://127.0.0.1:5173/')).toBe(false)
    expect(wc.goes('navigate', 'file:///etc/passwd')).toBe(false)
    expect(wc.goes('navigate', 'javascript:alert(1)')).toBe(false)
    expect(outside).toEqual(['https://example.com/', 'http://127.0.0.1:5173/'])
  })

  it("refuses a redirect of the window's page away from the server's origin, and leaves a frame's own alone", () => {
    const { wc, outside } = workbench()
    expect(wc.goes('redirect', origin + '/next')).toBe(true)
    expect(wc.goes('redirect', 'https://example.com/')).toBe(false)
    expect(wc.goes('redirect', 'https://example.com/', false)).toBe(true)
    expect(outside).toEqual([])
  })

  it('opens a window at the server alone, with the bridge, and every other one outside the app', () => {
    const { wc, outside } = workbench()
    const same = wc.open!({ url: origin + '/files' })
    expect(same.action).toBe('allow')
    expect(same.overrideBrowserWindowOptions?.webPreferences?.preload).toBe('/app/preload.js')
    expect(wc.open!({ url: 'https://example.com/' })).toEqual({ action: 'deny' })
    expect(wc.open!({ url: 'file:///etc/passwd' })).toEqual({ action: 'deny' })
    expect(outside).toEqual(['https://example.com/'])
  })

  it('puts the same fences on a window it opens, and on that window\'s, however deep', () => {
    const { wc, outside } = workbench()
    const child = wc.child()
    const grandchild = child.child()
    for (const w of [child, grandchild]) {
      expect(w.goes('navigate', origin + '/')).toBe(true)
      expect(w.goes('navigate', 'https://example.com/a')).toBe(false)
      expect(w.goes('redirect', 'https://example.com/b')).toBe(false)
      expect(w.open!({ url: 'https://example.com/c' })).toEqual({ action: 'deny' })
      expect(w.open!({ url: origin + '/d' }).action).toBe('allow')
    }
    expect(outside).toEqual(['https://example.com/a', 'https://example.com/c', 'https://example.com/a', 'https://example.com/c'])
  })

  it('navigates nowhere, opens nothing and hands nothing outside while the app trusts no origin (its server not running)', () => {
    const { wc, outside } = workbench(() => '')
    expect(wc.goes('navigate', origin + '/')).toBe(false)
    expect(wc.open!({ url: origin + '/' }).action).toBe('deny')
    expect(wc.child().goes('navigate', origin + '/')).toBe(false)
    expect(outside).toEqual([])
  })

  it("keeps each of the app's own local pages on its page, its links opening outside the app", () => {
    const outside: string[] = []
    const wc = new Contents()
    fenceLocalPage(wc, (url) => void outside.push(url))
    expect(wc.goes('navigate', origin + '/')).toBe(false)
    expect(wc.goes('navigate', 'https://example.com/')).toBe(false)
    expect(wc.goes('navigate', 'file:///etc/passwd')).toBe(false)
    expect(wc.goes('redirect', 'https://example.com/r')).toBe(false)
    expect(wc.open!({ url: origin + '/' })).toEqual({ action: 'deny' })
    expect(wc.open!({ url: 'https://example.com/w' })).toEqual({ action: 'deny' })
    expect(outside).toEqual([origin + '/', 'https://example.com/', origin + '/', 'https://example.com/w'])
  })
})
