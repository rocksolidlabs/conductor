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
 * workbenchPreferences are every workbench window's: isolated, sandboxed, with no node, and the preload in the main frame alone (no
 * nodeIntegrationInSubFrames), so a frame the workbench shows (a preview) has no window.conductorDesktop to call.
 */
export function workbenchPreferences(preload: string): Electron.WebPreferences {
  return { preload, contextIsolation: true, nodeIntegration: false, sandbox: true }
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
    webPreferences: { ...workbenchPreferences(o.preload), spellcheck: false },
  })
  fenceWorkbench(w.webContents as unknown as FenceContents, { origin: o.origin, preload: o.preload, openExternal: (url) => void shell.openExternal(url) })
  // The bounds are kept as the window closes (hidden to the tray or not) and as the app quits with it shown. The app's quit (the
  // tray's Quit, Cmd+Q, the menu) stops the server in main.ts's before-quit and then calls app.exit, which destroys every window
  // without a 'close': so the window keeps them on before-quit itself, shown or minimized. One hidden to the tray kept them as it
  // was hidden (its 'close'); read again hidden, its maximized state is not reported alike on every platform. A minimized window is
  // not maximized on Windows (the two states exclude each other), so one maximized, then minimized, then quit or closed from the
  // taskbar is kept as maximized was when it was minimized: followed by the window's own maximize and unmaximize, and read off the
  // window only while it is not minimized.
  let maximized = false
  w.on('maximize', () => {
    maximized = true
  })
  w.on('unmaximize', () => {
    if (!w.isDestroyed() && !w.isMinimized()) maximized = false
  })
  const keep = () => {
    if (w.isDestroyed()) return
    writeBounds(boundsFile, { ...w.getNormalBounds(), maximized: w.isMinimized() ? maximized : w.isMaximized() })
  }
  const keepOnQuit = () => {
    if (!w.isDestroyed() && (w.isVisible() || w.isMinimized())) keep()
  }
  app.on('before-quit', keepOnQuit)
  w.on('closed', () => app.removeListener('before-quit', keepOnQuit))
  w.on('close', (e) => {
    keep()
    o.onClose?.(w, e)
  })
  w.once('ready-to-show', () => {
    if (first.maximize) w.maximize()
    w.show()
  })
  return w
}

/** The part of a window's webContents the fences use. */
export interface FenceContents {
  on(event: 'will-navigate' | 'will-redirect', fn: (e: { preventDefault(): void; isMainFrame?: boolean }, url: string) => void): unknown
  on(event: 'did-create-window', fn: (child: { webContents: FenceContents }) => void): unknown
  setWindowOpenHandler(fn: (d: { url: string }) => { action: 'allow'; overrideBrowserWindowOptions?: BrowserWindowConstructorOptions } | { action: 'deny' }): void
}

/**
 * fenceWorkbench puts the workbench's fences on a window of it, and on every window it opens, and on theirs, however deep: it
 * navigates and is redirected only within the server's origin, a link elsewhere opening outside the app (http(s) alone, and
 * nothing else); it opens a window only at the server's origin, as a workbench window with the bridge and these same fences, and
 * anything else outside the app. While the app trusts no origin (its server not running), it navigates nowhere and opens nothing.
 */
export function fenceWorkbench(wc: FenceContents, o: { origin: () => string; preload: string; openExternal: (url: string) => void }): void {
  const away = (e: { preventDefault(): void; isMainFrame?: boolean }, url: string, hand: boolean) => {
    // A frame's own redirect (a preview's) is the frame's: the window's page is what is fenced.
    const trusted = o.origin()
    if (e.isMainFrame === false || sameOrigin(url, trusted)) return
    e.preventDefault()
    // While no origin is trusted (the server restarting), a link is held, not handed outside: it may be the workbench's own.
    if (hand && trusted && external(url)) o.openExternal(url)
  }
  wc.on('will-navigate', (e, url) => away(e, url, true))
  wc.on('will-redirect', (e, url) => away(e, url, false))
  wc.setWindowOpenHandler(({ url }) => {
    const trusted = o.origin()
    if (sameOrigin(url, trusted)) return { action: 'allow', overrideBrowserWindowOptions: { webPreferences: workbenchPreferences(o.preload) } }
    if (trusted && external(url)) o.openExternal(url)
    return { action: 'deny' }
  })
  wc.on('did-create-window', (child) => fenceWorkbench(child.webContents, o))
}

/**
 * fenceLocalPage puts the fences of one of the app's own local pages (the server log, the Windows setup screen) on its window: it
 * stays on its page, and a link from it opens outside the app (http(s) alone), never in a window of the app.
 */
export function fenceLocalPage(wc: FenceContents, openExternal: (url: string) => void): void {
  const away = (e: { preventDefault(): void; isMainFrame?: boolean }, url: string, hand: boolean) => {
    if (e.isMainFrame === false) return
    e.preventDefault()
    if (hand && external(url)) openExternal(url)
  }
  wc.on('will-navigate', (e, url) => away(e, url, true))
  wc.on('will-redirect', (e, url) => away(e, url, false))
  wc.setWindowOpenHandler(({ url }) => {
    if (external(url)) openExternal(url)
    return { action: 'deny' }
  })
}

/** A window as the app's following of its server sees it. */
export interface ShownWindow {
  isDestroyed(): boolean
  webContents: { id: number; getURL(): string; loadURL(url: string): Promise<void> }
}

/**
 * ServerWindows keeps every window that shows a page of the app's server on the server the app trusts: when the trusted origin goes
 * (the server stops, crashes or gives up), every window on a page of it shows nothing (about:blank) and keeps where it was; when a
 * server runs again, each goes back to its page on the new origin, the main window and every other alike. A window on another
 * page (the splash, the log, an outside site) is left as it is.
 */
export class ServerWindows {
  private on = ''
  private held = new Map<number, string>()

  constructor(private all: () => ShownWindow[]) {}

  /** moved is told the origin the app trusts now ('' while none). */
  moved(next: string): void {
    if (next === this.on) return
    const prev = this.on
    this.on = next
    for (const w of this.all()) {
      if (w.isDestroyed()) continue
      const wc = w.webContents
      const url = wc.getURL()
      if (!next) {
        if (prev && sameOrigin(url, prev)) {
          const u = new URL(url)
          this.held.set(wc.id, u.pathname + u.search + u.hash)
          void wc.loadURL('about:blank').catch(() => {})
        }
        continue
      }
      const path = this.held.get(wc.id)
      if (path !== undefined) {
        this.held.delete(wc.id)
        void wc.loadURL(next + path).catch(() => {})
        continue
      }
      const follow = followOrigin(url, next)
      if (follow) void wc.loadURL(follow).catch(() => {})
    }
  }

  /**
   * reloadTarget is what a reload of the window shows: its page again while it is on the trusted origin (or one of the app's own
   * file: pages), the page it was on once a server runs again, and nothing at all otherwise.
   */
  reloadTarget(w: ShownWindow): string | null {
    const url = w.webContents.getURL()
    if (url.startsWith('file:')) return url
    if (this.on && sameOrigin(url, this.on)) return url
    const path = this.held.get(w.webContents.id)
    if (path !== undefined) return this.on ? this.on + path : null
    const follow = this.on ? followOrigin(url, this.on) : null
    return follow
  }
}

/**
 * followTrusted wires the app's windows to the server it trusts: every state of the supervisor moves them (ServerWindows.moved),
 * every page load in the session goes through mainFrameAllowed, and reload is the View menu's Reload for a window, loading its
 * reloadTarget. It hands back the reload.
 */
export function followTrusted(o: {
  supervisor: { on(event: 'state', fn: () => void): unknown }
  windows: () => ShownWindow[]
  requests: { onBeforeRequest(filter: { urls: string[] }, fn: (d: { resourceType: string; url: string }, cb: (r: { cancel: boolean }) => void) => void): void }
  origin: () => string
}): { reload: (w: ShownWindow | null) => void; shown: ServerWindows } {
  const shown = new ServerWindows(o.windows)
  o.requests.onBeforeRequest({ urls: ['<all_urls>'] }, (details, cb) => cb({ cancel: !mainFrameAllowed(details.resourceType, details.url, o.origin()) }))
  o.supervisor.on('state', () => shown.moved(o.origin()))
  return {
    shown,
    reload: (w) => {
      if (!w || w.isDestroyed()) return
      const target = shown.reloadTarget(w)
      if (target) void w.webContents.loadURL(target).catch(() => {})
    },
  }
}

/**
 * mainFrameAllowed says whether a window's page may be loaded from url: a page of the server the app trusts, or anything that is
 * not http(s) (the app's own file: pages, about:blank). A reload, a history step or a load at an address the app does not trust
 * now is cancelled, in every window.
 */
export function mainFrameAllowed(resourceType: string, url: string, origin: string): boolean {
  if (resourceType !== 'mainFrame') return true
  if (!/^https?:/i.test(url)) return true
  return !!origin && sameOrigin(url, origin)
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
