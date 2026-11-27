import { serverAffecting, splitRoots, typedDefault, validate, type DesktopSettings } from './settings'

/** What became of the last save from the Settings page, for its toast: handed to the workbench once (SettingsSaver.take). */
export type SaveOutcome = '' | 'saved' | 'restarted'

export interface SaveDeps {
  get: () => DesktopSettings
  set: (s: DesktopSettings) => void
  /** Restarts the server; what it resolves with (the supervisor's handshake) is not the saver's business. */
  restart: () => Promise<unknown>
  /** Windows: the settings are the WSL distribution's paths. */
  posix: boolean
}

/**
 * SettingsSaver applies a save from the Settings page and keeps what became of
 * it for the page to ask once. A restarted server listens on a fresh port and
 * the window follows it, on the same page, as soon as the server runs: the
 * page that saved is gone before its save resolves, and the page that followed
 * asks (#214). The outcome is handed out only once the restart has completed
 * (the supervisor resolves the restart in the turn it reports the server
 * running, long before the page that follows has loaded), so an ask that comes
 * while a restart is pending, the outgoing page's own, gets nothing and takes
 * nothing from the page that follows; a restart that fails leaves nothing to
 * take, and the failure reaches the page that saved, which is still there. A
 * save that changes nothing the server reads, the retry after a failed restart
 * among them (the settings were kept), says saved, never restarted. The IPC
 * hands the outcome to the workbench at the server's current origin only
 * (trusted), which after a port change is the page that followed.
 */
export class SettingsSaver {
  private outcome: SaveOutcome = ''

  async save(d: SaveDeps, patch: Partial<DesktopSettings>): Promise<DesktopSettings> {
    const cur = d.get()
    // The zoom is the app's own (Ctrl+= and Ctrl+-): a form saved with an older level leaves the current one. So are the updates
    // told (#290): a form loaded before one was told would tell it again.
    // What the page cannot set: the press's folders (pressedRoots), and the read-only list of them it is shown (pressedPaths).
    const { pressedRoots: _pr, pressedPaths: _pp, ...fromPage } = patch as Partial<DesktopSettings> & { pressedPaths?: unknown }
    const next = typedDefault({
      ...cur,
      ...fromPage,
      zoomLevel: cur.zoomLevel,
      updateAnnounced: cur.updateAnnounced,
      updateDownloaded: cur.updateDownloaded,
      updateFailedNoticed: cur.updateFailedNoticed,
      // The page has one list of folders: the press's stay the app's own while the list holds them (splitRoots).
      ...splitRoots(cur, (patch as { allowedRoots?: unknown }).allowedRoots),
    } as DesktopSettings)
    const problems = validate(next, d.posix)
    if (problems.length) throw new Error(problems.join('; '))
    d.set(next)
    // A save under way owes nothing yet: what an earlier save left untaken goes with it.
    this.outcome = ''
    if (!serverAffecting(cur, next)) {
      this.outcome = 'saved'
      return next
    }
    await d.restart()
    this.outcome = 'restarted'
    return next
  }

  /** The last completed save's outcome, once: '' after, while a restart is pending, and when none is owed. */
  take(): SaveOutcome {
    const o = this.outcome
    this.outcome = ''
    return o
  }
}
