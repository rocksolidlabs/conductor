import { describe, expect, it } from 'vitest'
import { trusted } from '../src/ipc'
import { join } from 'node:path'
import { mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { external, firstBounds, followOrigin, logWindowOptions, permissionAllowed, readBounds, sameOrigin, workbenchWindows, writeBounds } from '../src/window'

describe('window guards', () => {
  it('keeps navigation on the server\'s origin and opens only http(s) outside', () => {
    expect(sameOrigin('http://127.0.0.1:4312/sessions/x', 'http://127.0.0.1:4312')).toBe(true)
    expect(sameOrigin('http://127.0.0.1:9999/', 'http://127.0.0.1:4312')).toBe(false)
    expect(external('https://github.com/x')).toBe(true)
    expect(external('file:///etc/passwd')).toBe(false)
    expect(external('javascript:alert(1)')).toBe(false)
  })
  it('trusts only the workbench and the app\'s own pages as IPC senders', () => {
    expect(trusted('http://127.0.0.1:4312/agents', 'http://127.0.0.1:4312')).toBe(true)
    expect(trusted('file:///app/static/log.html', 'http://127.0.0.1:4312')).toBe(true)
    expect(trusted('https://evil.example/', 'http://127.0.0.1:4312')).toBe(false)
    expect(trusted('', 'http://127.0.0.1:4312')).toBe(false)
  })
})

describe('followOrigin', () => {
  it('moves a window on the old server origin to the new one, on the same page', () => {
    expect(followOrigin('http://localhost:43853/sessions/ab?x=1#y', 'http://localhost:37225')).toBe('http://localhost:37225/sessions/ab?x=1#y')
    expect(followOrigin('http://127.0.0.1:4312/', 'http://127.0.0.1:5000')).toBe('http://127.0.0.1:5000/')
  })
  it('leaves a window already there, the splash, and outside sites alone', () => {
    expect(followOrigin('http://localhost:37225/yard', 'http://localhost:37225')).toBe(null)
    expect(followOrigin('file:///opt/Conductor/resources/static/splash.html', 'http://localhost:37225')).toBe(null)
    expect(followOrigin('https://switchyard.rslabs.net/join/x', 'http://localhost:37225')).toBe(null)
    expect(followOrigin('not a url', 'http://localhost:37225')).toBe(null)
  })
})

describe('the server log window', () => {
  it('has the preload that passes it the log lines, isolated as every window', () => {
    const o = logWindowOptions('/app/dist')
    expect(o.webPreferences?.preload).toBe(join('/app/dist', 'log-preload.js'))
    expect(o.webPreferences?.contextIsolation).toBe(true)
    expect(o.webPreferences?.nodeIntegration).toBe(false)
    expect(o.webPreferences?.sandbox).toBe(true)
  })
})

describe('the permissions the app grants its pages', () => {
  it('grants the clipboard and fullscreen, and nothing else', () => {
    expect(permissionAllowed('clipboard-read')).toBe(true)
    expect(permissionAllowed('clipboard-sanitized-write')).toBe(true)
    expect(permissionAllowed('fullscreen')).toBe(true)
    for (const p of ['media', 'geolocation', 'notifications', 'openExternal', 'pointerLock', 'midi', 'unknown']) expect(permissionAllowed(p)).toBe(false)
  })
})

describe('the workbench windows', () => {
  it('are the open windows at the server\'s origin: a second one opened from the first, not the log or a closed one (#290)', () => {
    const win = (url: string, destroyed = false) => ({ url, isDestroyed: () => destroyed, webContents: { getURL: () => url } })
    const all = [win('http://127.0.0.1:43123/yard'), win('http://127.0.0.1:43123/sessions/s1'), win('file:///app/static/log.html'), win('http://127.0.0.1:43123/', true), win('http://127.0.0.1:9999/')]
    expect(workbenchWindows(all, 'http://127.0.0.1:43123').map((w) => w.url)).toEqual(['http://127.0.0.1:43123/yard', 'http://127.0.0.1:43123/sessions/s1'])
  })
})

describe('the first window (#359)', () => {
  const big = { x: 0, y: 0, width: 3440, height: 1400 }
  const laptop = { x: 0, y: 0, width: 1366, height: 728 }
  const scaled = { x: 0, y: 0, width: 1280, height: 672 }

  it('asks for 1440 x 900, centred, on a display that holds it, and draws the desktop layout there', () => {
    expect(firstBounds(null, [big], big)).toEqual({ bounds: { x: 1000, y: 250, width: 1440, height: 900 }, maximize: false })
  })
  it('fits into a smaller work area, maximized when the page would be narrower than the desktop layout', () => {
    expect(firstBounds(null, [laptop], laptop)).toEqual({ bounds: { x: 0, y: 0, width: 1366, height: 728 }, maximize: false })
    expect(firstBounds(null, [scaled], scaled).bounds).toEqual({ x: 0, y: 0, width: 1280, height: 672 })
    // 1000 px wide, less its frame: below lg.
    const small = { x: 0, y: 0, width: 1000, height: 700 }
    expect(firstBounds(null, [small], small).maximize).toBe(true)
  })
  it("reads the app's zoom: a level that leaves the page below lg maximizes the window", () => {
    expect(firstBounds(null, [big], big, 1).maximize).toBe(false) // 1416 / 1.2 = 1180
    expect(firstBounds(null, [big], big, 2).maximize).toBe(true) // 1416 / 1.44 = 983
    expect(firstBounds(null, [laptop], laptop, 1).maximize).toBe(false) // 1342 / 1.2 = 1118
    expect(firstBounds(null, [scaled], scaled, 1.5).maximize).toBe(true) // 1256 / 1.31 = 956
  })
  it('opens where it was left, maximized if it was, while that is still on a display', () => {
    expect(firstBounds({ x: 100, y: 80, width: 1200, height: 800, maximized: true }, [big], big)).toEqual({ bounds: { x: 100, y: 80, width: 1200, height: 800 }, maximize: true })
    // On a second display, to the left of the primary.
    const left = { x: -1920, y: 0, width: 1920, height: 1040 }
    expect(firstBounds({ x: -1800, y: 40, width: 1500, height: 900 }, [big, left], big).bounds).toEqual({ x: -1800, y: 40, width: 1500, height: 900 })
  })
  it('falls back to the default when the kept bounds lie on no display, and fits kept bounds into a display grown smaller', () => {
    expect(firstBounds({ x: 5000, y: 5000, width: 1200, height: 800 }, [big], big).bounds).toEqual({ x: 1000, y: 250, width: 1440, height: 900 })
    expect(firstBounds({ x: 200, y: 100, width: 2000, height: 1200 }, [laptop], laptop).bounds).toEqual({ x: 0, y: 0, width: 1366, height: 728 })
  })
  it('keeps the bounds in a file and refuses one that is not a window', () => {
    const dir = mkdtempSync(join(tmpdir(), 'conductor-bounds-'))
    const file = join(dir, 'window-bounds.json')
    expect(readBounds(file)).toBe(null)
    writeBounds(file, { x: 10, y: 20, width: 1300, height: 850, maximized: true })
    expect(readBounds(file)).toEqual({ x: 10, y: 20, width: 1300, height: 850, maximized: true })
    writeFileSync(file, JSON.stringify({ x: 1, y: 2, width: 100, height: 100 }))
    expect(readBounds(file)).toBe(null)
    writeFileSync(file, '{"x": "a"}')
    expect(readBounds(file)).toBe(null)
    writeFileSync(file, 'not json')
    expect(readBounds(file)).toBe(null)
  })
})
