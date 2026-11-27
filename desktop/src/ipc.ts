import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { app, ipcMain, shell, type BrowserWindow, type IpcMainInvokeEvent, type WebContents } from 'electron'
import { external } from './window'
import { shownSettings, type DesktopSettings } from './settings'
import { SettingsSaver } from './save'
import type { FolderAllow } from './allow'
import type { ServerSupervisor, ServerStatus } from './server'
import type { IceStatus } from './firewall'
import { zoomStatus, type ZoomStatus } from './zoom'
import { asBrowserChoice, installedBrowsers, openUrl, systemBrowserDeps, type BrowserDeps } from './browsers'
import { capturePreview, injectBridge, sameFrame, type BridgeDeps, type CaptureDeps, type PreviewContents, type PreviewFrame } from './preview'

export interface IpcDeps {
  origin: () => string
  token: () => string
  supervisor: ServerSupervisor
  settings: { get: () => DesktopSettings; set: (s: DesktopSettings) => void }
  showLog: () => void
  /** The one-time notice owed to this person, handed out once ('' after, and when none is owed). */
  notice: () => string
  mainWindow: () => BrowserWindow | null
  serverVersion: () => string
  /** What the app forwards for WebRTC from WSL (Windows), for Settings. */
  ice: () => IceStatus
  /** Adds the firewall rule for the ICE port through an elevated netsh; the rule's state after. */
  allowIceFirewall: () => Promise<IceStatus['firewall']>
  /** Windows: the WSL distribution the server runs in, whose paths the settings are; null elsewhere. */
  wsl?: () => { distro: string } | null
  /** The page in sender listens for invites (the bridge's onInvite): one pending goes to it now. */
  inviteReady?: (sender: WebContents) => void
  /** The page in sender routed the invite it was sent with id. */
  inviteTaken?: (sender: WebContents, id: number) => void
  /**
   * The update downloaded and waiting for the app to quit ('' for none), whether to say so (once per version), the version the
   * tray's Restart to update asked for (handed out once), and one whose install did not take (said once, '' after).
   */
  update?: () => { ready: string; announce: boolean; restart: string; failed: string }
  /**
   * Installs the update downloaded now, the version the page offered: the app quits, the server with it ('started'). 'gone'
   * when that version is no longer the one ready (a newer one found, a download failed), 'busy' while a check is under way;
   * nothing stops then.
   */
  installUpdate?: (version: string) => 'started' | 'gone' | 'busy'
  /** The workbench's zoom now, and whether this page is the one to say a level kept from before (once per start, ZoomStart). */
  zoom?: () => ZoomStatus & { announce: boolean }
  /** What finding and starting a browser takes of the computer (#436): the system's own when not given. */
  browsers?: BrowserDeps
  /** Fetches the server's preview bridge (preview.ts injectBridge); without it the shell puts no bridge into a frame. */
  previewFetch?: BridgeDeps['fetch']
  /** The size of a window's content (preview.ts capturePreview); without it the shell takes no screenshot. */
  contentSize?: CaptureDeps['contentSize']
  /**
   * Allow this folder (allow.ts): the press, one at a time, never beside a Settings save or a restart from the page. Without it
   * (the app on Windows until its switch is on) the call is refused and nothing is asked.
   */
  folders?: FolderAllow
}

/**
 * workbenchFrame says whether an IPC call came from the workbench's own main frame: the top frame of the window that sent it (no
 * other frame of it, which the preload is never put in anyway), whose page and whose origin are the server's, over http(s). Every
 * channel of the bridge answers that frame alone; the app's own local pages have channels of their own (ownPage).
 */
export function workbenchFrame(e: { sender?: { mainFrame?: PreviewFrame } | null; senderFrame?: PreviewFrame | null }, origin: string): boolean {
  const f = e.senderFrame
  const main = e.sender?.mainFrame
  if (!f || !main || f.parent !== null || !sameFrame(f, main)) return false
  try {
    const u = new URL(f.url)
    const o = new URL(origin)
    return (u.protocol === 'http:' || u.protocol === 'https:') && u.protocol === o.protocol && u.origin === o.origin && f.origin === o.origin
  } catch {
    return false
  }
}

/**
 * ownPage says whether an IPC call came from the main frame of the app's own window wc showing its local page file, and from nothing
 * else: each of the app's local pages is answered only the calls it needs, each by a check of its own.
 */
export function ownPage(
  e: { sender?: unknown; senderFrame?: PreviewFrame | null },
  wc: { mainFrame?: PreviewFrame; isDestroyed?: () => boolean },
  file: string,
): boolean {
  const f = e.senderFrame
  if (!f || e.sender !== wc || wc.isDestroyed?.() || f.parent !== null || !sameFrame(f, wc.mainFrame)) return false
  try {
    const u = new URL(f.url)
    if (u.protocol !== 'file:') return false
    // The page's file and the one meant, as the system names them (a drive and its letter's case on Windows).
    const got = resolve(fileURLToPath(u))
    const want = resolve(file)
    return process.platform === 'win32' ? got.toLowerCase() === want.toLowerCase() : got === want
  } catch {
    return false
  }
}

/** registerIpc wires the handlers the preload bridge calls; each answers the workbench's main frame alone. */
export function registerIpc(d: IpcDeps): void {
  const saver = new SettingsSaver()
  // Every channel answers the workbench's main frame alone. The preview's two (docs/architecture.md, Previews and captures) also
  // act on that window alone: they run script in its frames and read its pixels.
  const mainFrameOnly = <T>(f: (e: IpcMainInvokeEvent, ...args: unknown[]) => T) => (e: IpcMainInvokeEvent, ...args: unknown[]): T => {
    if (!workbenchFrame(e as never, d.origin())) throw new Error("refused: not the workbench's main frame")
    return f(e, ...args)
  }
  const guard = mainFrameOnly
  ipcMain.handle('conductor:token', guard(() => d.token()))
  ipcMain.handle(
    'conductor:previewBridge',
    mainFrameOnly(async (e, origin) => {
      if (!d.previewFetch) return { refused: 'refused: this app puts no bridge into a preview' }
      return injectBridge(e.sender as unknown as PreviewContents, origin, { origin: d.origin, fetch: d.previewFetch })
    }),
  )
  ipcMain.handle(
    'conductor:capturePreview',
    mainFrameOnly(async (e, rect) => {
      if (!d.contentSize) return { refused: 'refused: this app takes no screenshot' }
      return capturePreview(e.sender as unknown as PreviewContents, rect, { contentSize: d.contentSize })
    }),
  )
  ipcMain.handle(
    'conductor:openExternal',
    guard(async (_e, url) => {
      if (typeof url === 'string' && external(url)) await shell.openExternal(url)
    }),
  )
  // An agent's link, in the browser its settings name (#436): one tab, through argv and never a shell; none named, or not on this
  // computer, through the default browser. The browsers found here, for the Agents page to mark.
  const browsers = () => d.browsers ?? systemBrowserDeps((url) => shell.openExternal(url))
  ipcMain.handle(
    'conductor:openUrl',
    guard(async (_e, url, browser) => {
      if (typeof url !== 'string' || !external(url)) throw new Error('refused: only an http(s) link opens in a browser')
      const choice = browser == null ? null : asBrowserChoice(browser)
      if (browser != null && !choice) throw new Error('refused: not a browser the app knows')
      return { outcome: await openUrl(url, choice, browsers()) }
    }),
  )
  ipcMain.handle('conductor:browsers', guard(() => installedBrowsers(browsers())))
  // The page sees one list of folders: the typed ones and the press's (shownSettings).
  ipcMain.handle('conductor:settings:get', guard(() => shownSettings(d.settings.get())))
  ipcMain.handle('conductor:update', guard(() => d.update?.() ?? { ready: '', announce: false, restart: '', failed: '' }))
  ipcMain.handle(
    'conductor:installUpdate',
    guard((_e, version) => (typeof version === 'string' && d.installUpdate?.(version)) || 'gone'),
  )
  ipcMain.handle('conductor:notice', guard(() => d.notice()))
  ipcMain.handle('conductor:zoom', guard(() => d.zoom?.() ?? { ...zoomStatus(0), announce: false }))
  // A save, and a restart asked from the page, wait for a press of Allow this folder under way, and a press meanwhile is busy.
  const serial = <T>(f: () => Promise<T>): Promise<T> => (d.folders ? d.folders.save(f) : f())
  ipcMain.handle(
    'conductor:settings:set',
    guard((_e, patch) =>
      serial(() => saver.save({ get: d.settings.get, set: d.settings.set, restart: () => d.supervisor.restart(), posix: !!d.wsl?.() }, patch as Partial<DesktopSettings>)).then(shownSettings),
    ),
  )
  // Allow this folder: it changes where agents may work, so it answers the workbench's main frame alone, as Settings' own page.
  ipcMain.handle(
    'conductor:allowFolder',
    mainFrameOnly(async (_e, path, confirmed) => {
      if (!d.folders) return { ok: false, code: 'not_supported', message: 'this app cannot allow a folder while Conductor runs' }
      return d.folders.allow(path, confirmed ?? '')
    }),
  )
  // What became of the last save, once: the page that followed a restarted server to its fresh port asks (SettingsSaver).
  ipcMain.handle('conductor:settings:outcome', guard(() => saver.take()))
  ipcMain.handle(
    'conductor:restartServer',
    guard(async () => {
      await serial(() => d.supervisor.restart())
    }),
  )
  ipcMain.handle(
    'conductor:openInBrowser',
    guard(async () => {
      // The token travels in the fragment, which never reaches the server and which the workbench takes and drops.
      const o = d.origin()
      if (o) await shell.openExternal(`${o}/#token=${encodeURIComponent(d.token())}`)
    }),
  )
  ipcMain.handle(
    'conductor:showLog',
    guard(() => {
      d.showLog()
    }),
  )
  ipcMain.handle('conductor:serverState', guard((): ServerStatus => d.supervisor.status))
  ipcMain.handle('conductor:ice', guard((): IceStatus => d.ice()))
  ipcMain.handle(
    'conductor:allowIceFirewall',
    guard(() => d.allowIceFirewall()),
  )
  ipcMain.handle(
    'conductor:inviteReady',
    guard((e) => {
      d.inviteReady?.(e.sender)
    }),
  )
  ipcMain.handle(
    'conductor:inviteTaken',
    guard((e, id) => {
      if (typeof id === 'number' && Number.isSafeInteger(id)) d.inviteTaken?.(e.sender, id)
    }),
  )
  ipcMain.handle(
    'conductor:versions',
    guard(() => ({ app: app.getVersion(), electron: process.versions.electron, node: process.versions.node, chrome: process.versions.chrome, server: d.serverVersion() })),
  )
}
