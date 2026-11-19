import { EventEmitter } from 'node:events'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { beforeEach, describe, expect, it, vi } from 'vitest'

// A link the workbench opens (#1020): the page opens a web link with window.open or an anchor to a new tab, and the window hands it
// to the system's browser (shell.openExternal) once and makes no window of it; a navigation away from the server's origin is stopped
// and handed over the same way; any other scheme is refused and handed to nothing. A fake Electron records the window's handlers.
type OpenHandler = (d: { url: string }) => { action: 'allow' | 'deny' }
type NavigateHandler = (e: { preventDefault(): void }, url: string) => void
const fake = vi.hoisted(() => ({
  windows: [] as Array<{ open?: OpenHandler; navigate?: NavigateHandler }>,
  opened: [] as string[],
}))
vi.mock('electron', async () => {
  const { EventEmitter } = await import('node:events')
  const { tmpdir } = await import('node:os')
  const app = Object.assign(new EventEmitter(), { getPath: () => tmpdir() })
  class BrowserWindow extends EventEmitter {
    handlers: { open?: OpenHandler; navigate?: NavigateHandler } = {}
    webContents = {
      on: (event: string, fn: NavigateHandler) => {
        if (event === 'will-navigate') this.handlers.navigate = fn
      },
      setWindowOpenHandler: (fn: OpenHandler) => {
        this.handlers.open = fn
      },
      executeJavaScript: async () => {},
      once: () => {},
    }
    constructor() {
      super()
      fake.windows.push(this.handlers)
    }
    getNormalBounds() {
      return { x: 0, y: 0, width: 1440, height: 900 }
    }
    isMaximized() {
      return false
    }
    isVisible() {
      return true
    }
    isMinimized() {
      return false
    }
    isDestroyed() {
      return false
    }
    loadFile() {
      return Promise.resolve()
    }
  }
  const area = { x: 0, y: 0, width: 1920, height: 1040 }
  return {
    app,
    BrowserWindow,
    screen: { getAllDisplays: () => [{ workArea: area }], getPrimaryDisplay: () => ({ workArea: area }) },
    shell: {
      openExternal: async (url: string) => {
        fake.opened.push(url)
      },
    },
    session: {},
  }
})
const { createWindow } = await import('../src/window')
const { showSplash } = await import('../src/splash')

const ORIGIN = 'http://127.0.0.1:4312'
const MR = 'https://example.com/group/project/-/merge_requests/95'
const OTHER = ['file:///etc/passwd', 'javascript:alert(1)', 'vscode://file/etc/passwd', 'mailto:a@example.com', 'about:blank', '']

function navigate(h: { navigate?: NavigateHandler }, url: string): boolean {
  let prevented = false
  h.navigate!({ preventDefault: () => (prevented = true) }, url)
  return prevented
}

describe("the workbench window's links (#1020)", () => {
  let h: { open?: OpenHandler; navigate?: NavigateHandler }
  beforeEach(() => {
    fake.windows.length = 0
    fake.opened.length = 0
    createWindow({ preload: 'p.js', origin: () => ORIGIN, boundsFile: join(mkdtempSync(join(tmpdir(), 'conductor-links-')), 'b.json') })
    h = fake.windows[0]!
  })

  it('hands a web link the page opens in a new tab to the system browser once, and makes no window of it', () => {
    expect(h.open!({ url: MR })).toEqual({ action: 'deny' })
    expect(fake.opened).toEqual([MR])
    expect(h.open!({ url: 'http://example.com/x' })).toEqual({ action: 'deny' })
    expect(fake.opened).toEqual([MR, 'http://example.com/x'])
  })

  it("opens a window of the app for a page of the server's own origin, and hands it to nothing else", () => {
    expect(h.open!({ url: `${ORIGIN}/sessions/s1` }).action).toBe('allow')
    expect(fake.opened).toEqual([])
  })

  it('refuses any other scheme and hands it to nothing', () => {
    for (const url of OTHER) expect(h.open!({ url }), url).toEqual({ action: 'deny' })
    expect(fake.opened).toEqual([])
  })

  it("stops a navigation away from the server's origin, handing a web one to the system browser once and any other to nothing", () => {
    expect(navigate(h, MR)).toBe(true)
    expect(fake.opened).toEqual([MR])
    for (const url of OTHER) expect(navigate(h, url), url).toBe(true)
    expect(fake.opened).toEqual([MR])
    expect(navigate(h, `${ORIGIN}/agents`)).toBe(false)
    expect(fake.opened).toEqual([MR])
  })
})

describe("the splash window's links", () => {
  beforeEach(() => {
    fake.windows.length = 0
    fake.opened.length = 0
  })

  it('hands a web link to the system browser once and nothing else to anything, and never navigates or opens a window', () => {
    showSplash('/app/static/splash.html', {})
    const h = fake.windows[0]!
    expect(h.open!({ url: MR })).toEqual({ action: 'deny' })
    expect(navigate(h, MR)).toBe(true)
    expect(fake.opened).toEqual([MR, MR])
    for (const url of OTHER) {
      expect(h.open!({ url }), url).toEqual({ action: 'deny' })
      expect(navigate(h, url), url).toBe(true)
    }
    expect(fake.opened).toEqual([MR, MR])
  })
})
