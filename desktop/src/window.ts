import { readFileSync, renameSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { BrowserWindow, app, screen, shell, session, type BrowserWindowConstructorOptions } from 'electron'

export interface WindowOptions {
  preload: string
  /** The server's origin (http://127.0.0.1:<port>): the only place the window navigates within. */
  origin: () => string
  icon?: string
  onClose?: (w: BrowserWindow, e: Electron.Event) => void
  /** The workbench's zoom level (the app's, 0 is 100%): the first window is sized so that the page still draws its desktop layout. */
  zoomLevel?: number
  /** Where the window's bounds are kept across launches; userData/window-bounds.json by default. */
  boundsFile?: string
}

/** A rectangle in screen (DIP) coordinates, as Electron's Rectangle. */
export interface Rect {
  x: number
  y: number
  width: number
  height: number
}

/** The window's bounds as last left: its normal (restored) bounds, and whether it was maximized. */
export interface SavedBounds extends Rect {
  maximized?: boolean
}

/** The size the first window asks for, and its least. */
export const WINDOW_SIZE = { width: 1440, height: 900 }
export const WINDOW_MIN = { width: 390, height: 500 }
/** The narrowest page, in CSS px, that the workbench draws its desktop layout at (Tailwind's lg, `web/app/layouts/default.vue`). */
export const DESKTOP_LAYOUT_WIDTH = 1024
/** What a window's frame and scrollbar take of its width, at most, on the platforms the app ships for. */
const FRAME_WIDTH = 24

/**
 * firstBounds is where the workbench window opens (#359). Bounds kept from the last launch, while they still lie on a display and
 * fit it; else WINDOW_SIZE fitted into the primary display's work area and centred there, maximized when that fitted size, at the
 * app's zoom (each level a factor of 1.2), leaves the page narrower than its desktop layout. 1440 x 900 asked for on a display
 * whose work area is smaller (a 1366 x 768 laptop, 1920 x 1080 at 150 % scaling: 1280 x 680 DIP) is not what Windows shows, and a
 * zoom kept from an earlier install narrows the page further: the first window opened in the narrow layout.
 */
export function firstBounds(saved: SavedBounds | null, workAreas: Rect[], primary: Rect, zoomLevel = 0): { bounds: Rect; maximize: boolean } {
  if (saved) {
    const area = workAreas.find((a) => contains(a, saved.x + saved.width / 2, saved.y + saved.height / 2))
    if (area) {
      const width = Math.max(WINDOW_MIN.width, Math.min(saved.width, area.width))
      const height = Math.max(WINDOW_MIN.height, Math.min(saved.height, area.height))
      const x = Math.min(Math.max(saved.x, area.x), area.x + area.width - width)
      const y = Math.min(Math.max(saved.y, area.y), area.y + area.height - height)
      return { bounds: { x, y, width, height }, maximize: !!saved.maximized }
    }
  }
  const width = Math.max(WINDOW_MIN.width, Math.min(WINDOW_SIZE.width, primary.width))
  const height = Math.max(WINDOW_MIN.height, Math.min(WINDOW_SIZE.height, primary.height))
  const bounds = { x: primary.x + Math.round((primary.width - width) / 2), y: primary.y + Math.round((primary.height - height) / 2), width, height }
  const page = (width - FRAME_WIDTH) / Math.pow(1.2, zoomLevel)
  return { bounds, maximize: page < DESKTOP_LAYOUT_WIDTH }
}

function contains(r: Rect, x: number, y: number): boolean {
  return x >= r.x && x < r.x + r.width && y >= r.y && y < r.y + r.height
}

/** readBounds is the bounds kept in file, or null when there are none or they are not a window's. */
export function readBounds(file: string): SavedBounds | null {
  try {
    const raw = JSON.parse(readFileSync(file, 'utf8')) as Record<string, unknown>
    const n = (k: string) => (typeof raw[k] === 'number' && Number.isFinite(raw[k]) ? (raw[k] as number) : NaN)
    const b = { x: n('x'), y: n('y'), width: n('width'), height: n('height') }
    if (Object.values(b).some(Number.isNaN) || b.width < WINDOW_MIN.width || b.height < WINDOW_MIN.height) return null
    return { ...b, maximized: raw.maximized === true }
  } catch {
    return null
  }
}

/** writeBounds keeps b in file (written whole, then renamed over it); a failure only costs the next launch its place. */
export function writeBounds(file: string, b: SavedBounds): void {
  try {
    const tmp = `${file}.new`
    writeFileSync(tmp, JSON.stringify({ x: b.x, y: b.y, width: b.width, height: b.height, maximized: !!b.maximized }) + '\n', { mode: 0o600 })
    renameSync(tmp, file)
  } catch {
    // The bounds are a convenience: the window opens at its default next time.
  }
}

/** sameOrigin reports whether url is on the server's origin. */
export function sameOrigin(url: string, origin: string): boolean {
  try {
    const u = new URL(url)
    const o = new URL(origin)
    return u.protocol === o.protocol && u.host === o.host
  } catch {
    return false
  }
}

/** workbenchWindows are the windows of all that show the workbench of the app's own server (origin): open, at its origin. */
export function workbenchWindows<W extends { isDestroyed(): boolean; webContents: { getURL(): string } }>(all: W[], origin: string): W[] {
  return all.filter((w) => !w.isDestroyed() && sameOrigin(w.webContents.getURL(), origin))
}

/**
 * followOrigin is where a window showing the app's own server goes once that server listens on a new origin: a restart (a crash, the
 * health check, Restart server) binds a fresh port, and a window left on the old one shows a dead page. The same path, query and hash
 * on the new origin; null when the window is there already or is not on a local server page (the splash, an outside site).
 */
export function followOrigin(current: string, origin: string): string | null {
  try {
    const u = new URL(current)
    const o = new URL(origin)
    if (u.protocol !== 'http:' && u.protocol !== 'https:') return null
    if (!['127.0.0.1', 'localhost', '[::1]'].includes(u.hostname)) return null
    if (u.protocol === o.protocol && u.host === o.host) return null
    return o.origin + u.pathname + u.search + u.hash
  } catch {
    return null
  }
}

/** external reports whether url may be opened outside: http(s) only, never file:, javascript: or an app's own scheme. */
export function external(url: string): boolean {
  try {
    const u = new URL(url)
    return u.protocol === 'http:' || u.protocol === 'https:'
  } catch {
    return false
  }
}

/**
 * logWindowOptions are the server log window's: its page (static/log.html) shows the lines its preload passes it (window.conductorLog,
 * log-preload.js in dir), so without the preload it stayed an empty dark box, seen by the owner on Windows on 2026-10-06.
 */
export function logWindowOptions(dir: string): BrowserWindowConstructorOptions {
  return { width: 900, height: 600, title: 'Conductor server log', webPreferences: { preload: join(dir, 'log-preload.js'), contextIsolation: true, nodeIntegration: false, sandbox: true } }
}

/**
 * createWindow makes the workbench window: context isolation on, no node integration, navigation kept to the server's origin,
 * window.open on that origin as a window of the app and anything else in the system browser, and no permissions beyond the clipboard.
 */
export function createWindow(o: WindowOptions): BrowserWindow {
  const boundsFile = o.boundsFile ?? join(app.getPath('userData'), 'window-bounds.json')
  const first = firstBounds(
    readBounds(boundsFile),
    screen.getAllDisplays().map((d) => d.workArea),
    screen.getPrimaryDisplay().workArea,
    o.zoomLevel,
  )
  const w = new BrowserWindow({
    ...first.bounds,
    minWidth: WINDOW_MIN.width,
    minHeight: WINDOW_MIN.height,
    title: 'Conductor',
    icon: o.icon,
    backgroundColor: '#263D35',
    show: false,
    webPreferences: { preload: o.preload, contextIsolation: true, nodeIntegration: false, sandbox: true, spellcheck: false },
  })
  w.webContents.on('will-navigate', (e, url) => {
    if (!sameOrigin(url, o.origin())) {
      e.preventDefault()
      if (external(url)) void shell.openExternal(url)
    }
  })
  w.webContents.setWindowOpenHandler(({ url }) => {
    if (sameOrigin(url, o.origin())) return { action: 'allow', overrideBrowserWindowOptions: { webPreferences: { preload: o.preload, contextIsolation: true, nodeIntegration: false, sandbox: true } } }
    if (external(url)) void shell.openExternal(url)
    return { action: 'deny' }
  })
  // The bounds are kept as the window closes (hidden to the tray or not) and when the app quits with it open.
  w.on('close', (e) => {
    if (!w.isDestroyed()) writeBounds(boundsFile, { ...w.getNormalBounds(), maximized: w.isMaximized() })
    o.onClose?.(w, e)
  })
  w.once('ready-to-show', () => {
    if (first.maximize) w.maximize()
    w.show()
  })
  return w
}

/** restrictPermissions denies every permission request but the clipboard's. */
export function restrictPermissions(): void {
  session.defaultSession.setPermissionRequestHandler((_wc, permission, cb) => {
    cb(permissionAllowed(permission))
  })
}

/**
 * permissionAllowed is what the app's pages may have: the clipboard, and fullscreen, which the page header's button and the F key ask
 * for (element.requestFullscreen); refused, the button did nothing in the app, seen by the owner on Windows on 2026-10-08.
 */
export function permissionAllowed(permission: string): boolean {
  return permission === 'clipboard-read' || permission === 'clipboard-sanitized-write' || permission === 'fullscreen'
}
