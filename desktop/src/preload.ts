import { contextBridge, ipcRenderer } from 'electron'

/**
 * The app on Windows offers no Allow this folder until one press has been seen to work there: allow.ts's ALLOW_ON_WINDOWS, copied
 * here because a sandboxed preload imports nothing of the app's (a test holds the two equal).
 */
export const ALLOW_FOLDER_ON_WINDOWS = false

/** What became of Allow this folder: ok with the folder and whether the settings file holds it, or why not. */
export type AllowFolderAnswer =
  | { ok: true; root: string; saved: boolean; error?: string }
  | { ok: false; code: string; message: string; real?: string; wide?: string; reason?: string }

/** allowFolderCall is the bridge's allowFolder on platform, or none where the app does not offer the press. */
export function allowFolderCall(platform: string): ((path: string, confirmed?: string) => Promise<AllowFolderAnswer>) | undefined {
  if (platform === 'win32' && !ALLOW_FOLDER_ON_WINDOWS) return undefined
  return (path, confirmed) => ipcRenderer.invoke('conductor:allowFolder', path, confirmed ?? '')
}

const allowFolder = allowFolderCall(process.platform)

/** The bridge the workbench sees as window.conductorDesktop (web/app/utils/desktop.ts mirrors it). */
const bridge = {
  version: process.env.CONDUCTOR_DESKTOP_VERSION ?? '',
  platform: process.platform,
  token: (): Promise<string> => ipcRenderer.invoke('conductor:token'),
  openExternal: (url: string): Promise<void> => ipcRenderer.invoke('conductor:openExternal', url),
  /**
   * Opens an agent's link in the browser its settings name (#436), on this computer, one tab; with none named, or one not installed
   * here, in the default browser. What became of it: 'browser', 'default', 'missing' (not installed) or 'failed' (did not start).
   */
  openUrl: (url: string, browser?: { name: string; profile?: string } | null): Promise<{ outcome: string }> =>
    ipcRenderer.invoke('conductor:openUrl', url, browser ?? null),
  /** The browsers of the five an agent may name that are installed on this computer. */
  browsers: (): Promise<string[]> => ipcRenderer.invoke('conductor:browsers'),
  settings: {
    get: () => ipcRenderer.invoke('conductor:settings:get'),
    set: (patch: Record<string, unknown>) => ipcRenderer.invoke('conductor:settings:set', patch),
    outcome: (): Promise<string> => ipcRenderer.invoke('conductor:settings:outcome'),
  },
  /**
   * Allow this folder: agents may use path from now on, with nothing restarted, and the settings keep it (saved false, with why,
   * when the file could not be written). confirmed names a wide folder's kind ('home', 'above_home', 'holds_data') once asked.
   * Absent where the app does not offer it.
   */
  ...(allowFolder ? { allowFolder } : {}),
  /** The settings changed outside the Settings page (a folder allowed): read them again. */
  onSettings: (cb: () => void): (() => void) => {
    const handler = () => cb()
    ipcRenderer.on('conductor:settings', handler)
    return () => ipcRenderer.removeListener('conductor:settings', handler)
  },
  restartServer: (): Promise<void> => ipcRenderer.invoke('conductor:restartServer'),
  openInBrowser: (): Promise<void> => ipcRenderer.invoke('conductor:openInBrowser'),
  showLog: (): Promise<void> => ipcRenderer.invoke('conductor:showLog'),
  serverState: () => ipcRenderer.invoke('conductor:serverState'),
  notice: (): Promise<string> => ipcRenderer.invoke('conductor:notice'),
  ice: () => ipcRenderer.invoke('conductor:ice'),
  allowIceFirewall: (): Promise<string> => ipcRenderer.invoke('conductor:allowIceFirewall'),
  versions: () => ipcRenderer.invoke('conductor:versions'),
  /**
   * Puts the server's preview bridge into the workbench's preview frames at origin (a page on this machine's loopback, not the
   * workbench's own): how many took it, or why the app refused.
   */
  previewBridge: (origin: string): Promise<{ injected: number } | { refused: string }> => ipcRenderer.invoke('conductor:previewBridge', origin),
  /**
   * The pixels of rect (the workbench's CSS pixels, whole numbers, inside the window) of this window, as PNG at most 2,048 pixels on
   * its long side, with the image's pixels to one of the rect's; or why the app refused (one at a time, at most 4 MiB).
   */
  capturePreview: (rect: { x: number; y: number; width: number; height: number }): Promise<{ png: Uint8Array; width: number; height: number; scale: number } | { refused: string }> =>
    ipcRenderer.invoke('conductor:capturePreview', rect),
  /**
   * The update downloaded ('' for none), whether to say so (the first page to ask, once per version), the version the tray's
   * Restart to update asked for, to ask in the dialog (once), and one whose install did not take, said once (#290).
   */
  update: (): Promise<{ ready: string; announce: boolean; restart: string; failed: string }> => ipcRenderer.invoke('conductor:update'),
  /**
   * Installs the update downloaded now, the version offered: the app quits, its server and the sessions on it with it
   * ('started'). 'gone' when that version is no longer ready, 'busy' while a check for updates is under way: nothing stops then.
   */
  installUpdate: (version: string): Promise<'started' | 'gone' | 'busy'> => ipcRenderer.invoke('conductor:installUpdate', version),
  /** A check found an update downloaded, the one downloaded is gone, or the tray asked for the restart: ask update() what to say. */
  onUpdate: (cb: () => void): (() => void) => {
    const handler = () => cb()
    ipcRenderer.on('conductor:update', handler)
    return () => ipcRenderer.removeListener('conductor:update', handler)
  },
  /** The workbench's zoom now (its level, the percent it reads, 'largest' or 'smallest' at a bound), and whether to say it: a level kept from before, to the first page that asks after a start. */
  zoom: (): Promise<{ level: number; percent: number; bound: string; announce: boolean }> => ipcRenderer.invoke('conductor:zoom'),
  /** Each zoom move by key or menu, one at a bound that changed nothing included: the status after it, to show. */
  onZoom: (cb: (zoom: { level: number; percent: number; bound: string }) => void): (() => void) => {
    const handler = (_e: unknown, zoom: { level: number; percent: number; bound: string }) => cb(zoom)
    ipcRenderer.on('conductor:zoom', handler)
    return () => ipcRenderer.removeListener('conductor:zoom', handler)
  },
  onServerState: (cb: (state: unknown) => void): (() => void) => {
    const handler = (_e: unknown, state: unknown) => cb(state)
    ipcRenderer.on('conductor:serverState', handler)
    return () => ipcRenderer.removeListener('conductor:serverState', handler)
  },
  /**
   * An invite the app was handed while this page is open: the page routes to its join page in place of a reload. cb says whether it
   * did, once its navigation has finished, and an invite routed is reported taken; one refused is left to the app, which loads the
   * join page for it.
   */
  onInvite: (cb: (invite: { server: string; token: string }) => boolean | Promise<boolean>): (() => void) => {
    const handler = (_e: unknown, invite: { server: string; token: string }, id: number) => {
      void Promise.resolve()
        .then(() => cb(invite))
        .then((routed) => {
          if (routed) void ipcRenderer.invoke('conductor:inviteTaken', id)
        })
        .catch(() => {})
    }
    const ask = () => void ipcRenderer.invoke('conductor:inviteReady')
    ipcRenderer.on('conductor:invite', handler)
    ipcRenderer.on('conductor:inviteAsk', ask)
    ask()
    return () => {
      ipcRenderer.removeListener('conductor:invite', handler)
      ipcRenderer.removeListener('conductor:inviteAsk', ask)
    }
  },
}

contextBridge.exposeInMainWorld('conductorDesktop', bridge)
