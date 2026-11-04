import { contextBridge, ipcRenderer } from 'electron'

/** The bridge the workbench sees as window.conductorDesktop (web/app/utils/desktop.ts mirrors it). */
const bridge = {
  version: process.env.CONDUCTOR_DESKTOP_VERSION ?? '',
  platform: process.platform,
  token: (): Promise<string> => ipcRenderer.invoke('conductor:token'),
  openExternal: (url: string): Promise<void> => ipcRenderer.invoke('conductor:openExternal', url),
  settings: {
    get: () => ipcRenderer.invoke('conductor:settings:get'),
    set: (patch: Record<string, unknown>) => ipcRenderer.invoke('conductor:settings:set', patch),
    outcome: (): Promise<string> => ipcRenderer.invoke('conductor:settings:outcome'),
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
