import { describe, expect, it, vi } from 'vitest'

vi.mock('electron', () => ({ app: {}, BrowserWindow: class {}, screen: {}, shell: {}, session: {} }))
import { EventEmitter } from 'node:events'
import { followTrusted, mainFrameAllowed, ServerWindows, type ShownWindow } from '../src/window'

/** A window showing url: what it is told to load, kept. */
function win(id: number, url: string) {
  const w = {
    url,
    loads: [] as string[],
    destroyed: false,
    isDestroyed: () => w.destroyed,
    webContents: {
      id,
      getURL: () => w.url,
      loadURL: async (u: string) => {
        w.loads.push(u)
        w.url = u
      },
    },
  }
  return w
}

const OLD = 'http://127.0.0.1:4312'
const NEW = 'http://127.0.0.1:5000'

describe('every window follows the server the app trusts', () => {
  it('shows nothing in every window on a page of the server once it is trusted no more, and goes back to each page on the next', () => {
    const main = win(1, OLD + '/sessions/s1?x=1#y')
    const child = win(2, OLD + '/files')
    const grandchild = win(3, OLD + '/')
    const splash = win(4, 'file:///app/static/splash.html')
    const outside = win(5, 'https://example.com/')
    const all = [main, child, grandchild, splash, outside]
    const shown = new ServerWindows(() => all as unknown as ShownWindow[])
    shown.moved(OLD)
    // A stop, a crash or a give-up: the app trusts no origin.
    shown.moved('')
    for (const w of [main, child, grandchild]) expect(w.url).toBe('about:blank')
    expect(splash.loads).toEqual([])
    expect(outside.loads).toEqual([])
    // A reload while no server runs shows nothing.
    expect(shown.reloadTarget(main as unknown as ShownWindow)).toBe(null)
    expect(shown.reloadTarget(splash as unknown as ShownWindow)).toBe('file:///app/static/splash.html')
    // The next server: every window back on its page, at the new origin.
    shown.moved(NEW)
    expect(main.url).toBe(NEW + '/sessions/s1?x=1#y')
    expect(child.url).toBe(NEW + '/files')
    expect(grandchild.url).toBe(NEW + '/')
    expect(splash.loads).toEqual([])
    expect(outside.loads).toEqual([])
    expect(shown.reloadTarget(child as unknown as ShownWindow)).toBe(NEW + '/files')
  })

  it('moves a window still on an old address of the server to the trusted one, and leaves a closed window alone', () => {
    const stale = win(1, OLD + '/agents')
    const closed = win(2, OLD + '/')
    closed.destroyed = true
    const shown = new ServerWindows(() => [stale, closed] as unknown as ShownWindow[])
    shown.moved(NEW)
    expect(stale.url).toBe(NEW + '/agents')
    expect(closed.loads).toEqual([])
    expect(shown.reloadTarget(win(3, OLD + '/x') as unknown as ShownWindow)).toBe(NEW + '/x')
  })

  it('cancels a page load from an address the app does not trust now, in every window', () => {
    expect(mainFrameAllowed('mainFrame', NEW + '/sessions', NEW)).toBe(true)
    expect(mainFrameAllowed('mainFrame', OLD + '/sessions', NEW)).toBe(false)
    expect(mainFrameAllowed('mainFrame', NEW + '/', '')).toBe(false)
    expect(mainFrameAllowed('mainFrame', 'https://example.com/', NEW)).toBe(false)
    expect(mainFrameAllowed('mainFrame', 'file:///app/static/log.html', NEW)).toBe(true)
    expect(mainFrameAllowed('mainFrame', 'about:blank', '')).toBe(true)
    // A frame's or a request's own address is not a window's page.
    expect(mainFrameAllowed('subFrame', 'http://127.0.0.1:5173/', NEW)).toBe(true)
    expect(mainFrameAllowed('xhr', OLD + '/api/events', NEW)).toBe(true)
  })

  it("is what the app runs: a state of its server moves every window, the session's loads are gated, and Reload loads the target", () => {
    const supervisor = new EventEmitter()
    let trusted = OLD
    let gate: ((d: { resourceType: string; url: string }, cb: (r: { cancel: boolean }) => void) => void) | null = null
    const main = win(1, OLD + '/agents')
    const child = win(2, OLD + '/files')
    const wired = followTrusted({
      supervisor: supervisor as never,
      windows: () => [main, child] as unknown as ShownWindow[],
      requests: { onBeforeRequest: (_f, fn) => void (gate = fn) },
      origin: () => trusted,
    })
    supervisor.emit('state')
    const cancelled = (url: string, type = 'mainFrame') => {
      let out: boolean | undefined
      gate!({ resourceType: type, url }, (r) => (out = r.cancel))
      return out
    }
    expect(cancelled(OLD + '/agents')).toBe(false)
    trusted = ''
    supervisor.emit('state')
    expect(main.url).toBe('about:blank')
    expect(child.url).toBe('about:blank')
    expect(cancelled(OLD + '/agents')).toBe(true)
    // Reload while no server runs loads nothing.
    wired.reload(main as unknown as ShownWindow)
    expect(main.url).toBe('about:blank')
    trusted = NEW
    supervisor.emit('state')
    expect(main.url).toBe(NEW + '/agents')
    expect(child.url).toBe(NEW + '/files')
    expect(cancelled(OLD + '/agents')).toBe(true)
    expect(cancelled(NEW + '/agents')).toBe(false)
    main.loads.length = 0
    wired.reload(main as unknown as ShownWindow)
    expect(main.loads).toEqual([NEW + '/agents'])
    wired.reload(null)
  })
})
