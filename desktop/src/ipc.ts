import { app, ipcMain, shell, type BrowserWindow, type IpcMainInvokeEvent, type WebContents } from 'electron'
import { external } from './window'
import type { DesktopSettings } from './settings'
import { SettingsSaver } from './save'
import type { ServerSupervisor, ServerStatus } from './server'
import type { IceStatus } from './firewall'
import { zoomStatus, type ZoomStatus } from './zoom'

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
}

/** trusted says whether the sender is the workbench served by this app's own server, or the app's own pages. */
export function trusted(senderUrl: string, origin: string): boolean {
  try {
    const u = new URL(senderUrl)
    const o = new URL(origin)
    if (u.protocol === 'file:') return true
    return u.protocol === o.protocol && u.host === o.host
  } catch {
    return false
  }
}

/** registerIpc wires the handlers the preload bridge calls; each checks its sender first. */
export function registerIpc(d: IpcDeps): void {
  const saver = new SettingsSaver()
  const guard = <T>(f: (e: IpcMainInvokeEvent, ...args: unknown[]) => T) => (e: IpcMainInvokeEvent, ...args: unknown[]): T => {
    if (!trusted(e.senderFrame?.url ?? '', d.origin())) throw new Error('refused: not the workbench')
    return f(e, ...args)
  }
  ipcMain.handle('conductor:token', guard(() => d.token()))
  ipcMain.handle(
    'conductor:openExternal',
    guard(async (_e, url) => {
      if (typeof url === 'string' && external(url)) await shell.openExternal(url)
    }),
  )
  ipcMain.handle('conductor:settings:get', guard(() => d.settings.get()))
  ipcMain.handle('conductor:update', guard(() => d.update?.() ?? { ready: '', announce: false, restart: '', failed: '' }))
  ipcMain.handle(
    'conductor:installUpdate',
    guard((_e, version) => (typeof version === 'string' && d.installUpdate?.(version)) || 'gone'),
  )
  ipcMain.handle('conductor:notice', guard(() => d.notice()))
  ipcMain.handle('conductor:zoom', guard(() => d.zoom?.() ?? { ...zoomStatus(0), announce: false }))
  ipcMain.handle(
    'conductor:settings:set',
    guard((_e, patch) => saver.save({ get: d.settings.get, set: d.settings.set, restart: () => d.supervisor.restart(), posix: !!d.wsl?.() }, patch as Partial<DesktopSettings>)),
  )
  // What became of the last save, once: the page that followed a restarted server to its fresh port asks (SettingsSaver).
  ipcMain.handle('conductor:settings:outcome', guard(() => saver.take()))
  ipcMain.handle(
    'conductor:restartServer',
    guard(async () => {
      await d.supervisor.restart()
    }),
  )
  ipcMain.handle(
    'conductor:openInBrowser',
    guard(async () => {
      // The token travels in the fragment, which never reaches the server and which the workbench takes and drops.
      await shell.openExternal(`${d.origin()}/#token=${encodeURIComponent(d.token())}`)
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
