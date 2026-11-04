import { EventEmitter } from 'node:events'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { beforeEach, describe, expect, it, vi } from 'vitest'

// A fake Electron: the app's events, one display, and a window whose bounds and state the test sets (#359).
const fake = vi.hoisted(() => ({ app: null as unknown as import('node:events').EventEmitter, windows: [] as unknown[] }))
vi.mock('electron', async () => {
  const { EventEmitter } = await import('node:events')
  const app = Object.assign(new EventEmitter(), { getPath: () => tmpdir() })
  fake.app = app
  class BrowserWindow extends EventEmitter {
    bounds: { x: number; y: number; width: number; height: number }
    maximized = false
    visible = true
    minimized = false
    destroyed = false
    webContents = { on: () => {}, setWindowOpenHandler: () => {} }
    constructor(o: { x: number; y: number; width: number; height: number }) {
      super()
      this.bounds = { x: o.x, y: o.y, width: o.width, height: o.height }
      fake.windows.push(this)
    }
    getNormalBounds() {
      return { ...this.bounds }
    }
    isMaximized() {
      return this.maximized
    }
    isVisible() {
      return this.visible
    }
    isMinimized() {
      return this.minimized
    }
    isDestroyed() {
      return this.destroyed
    }
    maximize() {
      this.maximized = true
    }
    show() {
      this.visible = true
    }
  }
  const area = { x: 0, y: 0, width: 1920, height: 1040 }
  return { app, BrowserWindow, screen: { getAllDisplays: () => [{ workArea: area }], getPrimaryDisplay: () => ({ workArea: area }) }, shell: {}, session: {} }
})
const { createWindow, readBounds } = await import('../src/window')

interface FakeWindow extends EventEmitter {
  bounds: { x: number; y: number; width: number; height: number }
  maximized: boolean
  visible: boolean
  minimized: boolean
  destroyed: boolean
}

/** quit is the app's quit as main.ts makes it: before-quit, whose own listener prevents the default and later calls app.exit, which
 * destroys the windows without a 'close'. */
function quit(windows: FakeWindow[]) {
  const e = { defaultPrevented: false, preventDefault() { this.defaultPrevented = true } }
  fake.app.emit('before-quit', e)
  for (const w of windows) {
    w.destroyed = true
    w.emit('closed')
  }
}

describe('when the window keeps its bounds (#359)', () => {
  let file: string
  let w: FakeWindow
  beforeEach(() => {
    fake.app.removeAllListeners()
    fake.windows.length = 0
    // main.ts's own before-quit, there before the window: it stops the quit to stop the server first.
    fake.app.on('before-quit', (e: { preventDefault(): void }) => e.preventDefault())
    file = join(mkdtempSync(join(tmpdir(), 'conductor-bounds-')), 'window-bounds.json')
    createWindow({ preload: 'p.js', origin: () => 'http://127.0.0.1:4312', boundsFile: file })
    w = fake.windows[0] as FakeWindow
  })

  it('keeps them as the app quits with the window shown, moved, resized and maximized: no close comes', () => {
    w.bounds = { x: 100, y: 80, width: 1200, height: 800 }
    w.maximized = true
    quit([w])
    expect(readBounds(file)).toEqual({ x: 100, y: 80, width: 1200, height: 800, maximized: true })
  })

  it('keeps them as the app quits with the window minimized', () => {
    w.bounds = { x: 10, y: 20, width: 1000, height: 700 }
    w.visible = false
    w.minimized = true
    quit([w])
    expect(readBounds(file)).toEqual({ x: 10, y: 20, width: 1000, height: 700, maximized: false })
  })

  it('keeps them as the window closes to the tray, and a quit while it is hidden leaves them as kept', () => {
    w.bounds = { x: 200, y: 100, width: 1300, height: 850 }
    w.maximized = true
    w.emit('close', { preventDefault() {} })
    expect(readBounds(file)).toEqual({ x: 200, y: 100, width: 1300, height: 850, maximized: true })
    w.visible = false
    w.maximized = false // what a hidden window may report
    quit([w])
    expect(readBounds(file)).toEqual({ x: 200, y: 100, width: 1300, height: 850, maximized: true })
  })

  it('lets go of the app\'s before-quit once the window is closed', () => {
    expect(fake.app.listenerCount('before-quit')).toBe(2)
    w.emit('close', { preventDefault() {} })
    w.destroyed = true
    w.emit('closed')
    expect(fake.app.listenerCount('before-quit')).toBe(1)
  })
})
