import type { Logs } from './logs'

/**
 * updateChannel says whether the app can update itself: the dmg/zip and nsis installs and the AppImage do (electron-updater
 * against the update feed the package names), while a deb or rpm from a package manager is updated by it, so the app only
 * points at the download page. feed is whether the package names an update feed: an app-update.yml in its resources, which
 * electron-builder writes whenever a publish configuration resolves for the build (desktop/README.md, Updates, says how); only
 * publish set to null is sure to leave it out. A package with none has no updater, which would fail at every check.
 */
export function updateChannel(platform: string, appImage: boolean, packaged: boolean, feed: boolean): 'auto' | 'link' | 'none' {
  if (!packaged || !feed) return 'none'
  if (platform === 'darwin' || platform === 'win32') return 'auto'
  return appImage ? 'auto' : 'link'
}

/**
 * The download page when the package names none: the repository's releases, all of them (its /releases/latest answers 404
 * while every release is a prerelease).
 */
export const DEFAULT_DOWNLOAD_PAGE = 'https://github.com/rocksolidlabs/conductor/releases'
/** The longest download page a package may name, in bytes. */
export const MAX_DOWNLOAD_PAGE = 200

/**
 * ADDRESS_FORM is a download page's form, the same text as switchyard.appURL's (internal/config, appURLForm): https:// in those
 * letters; a host name of letters, digits and hyphens in labels joined by dots, the last beginning with a letter; a port of
 * digits, if any; then a path, query or fragment of printable ASCII (0x21 to 0x7e) without a backslash, a % only before two hex
 * digits. So no user information, no IP literal and no space or control character.
 */
const ADDRESS_FORM = /^https:\/\/((?:[A-Za-z0-9-]+\.)*[A-Za-z][A-Za-z0-9-]*)(?::([0-9]{1,5}))?(?:[/?#](?:[!-$&-\[\]-~]|%[0-9A-Fa-f]{2})*)?$/

/**
 * downloadPage is where the app points for a new version: the packaged package.json's conductor.downloadPage (packageJson, its
 * text, null when it could not be read) in ADDRESS_FORM, of at most MAX_DOWNLOAD_PAGE bytes, no label of its host beginning
 * xn-- (an internationalised name, which the form cannot check), its port 1 to 65535, and parsed by the URL standard as the
 * host it names; the page is then the parsed address's canonical text (its href), which must be in the form and within the bound
 * too (the parser writes some characters as three), so the address checked is the one used. Anything else is ignored, and the
 * page is DEFAULT_DOWNLOAD_PAGE.
 */
export function downloadPage(packageJson: string | null): string {
  let page: unknown
  try {
    page = (JSON.parse(packageJson ?? 'null') as { conductor?: { downloadPage?: unknown } } | null)?.conductor?.downloadPage
  } catch {
    return DEFAULT_DOWNLOAD_PAGE
  }
  if (typeof page !== 'string') return DEFAULT_DOWNLOAD_PAGE
  // The form is ASCII, so its length is its bytes.
  const m = ADDRESS_FORM.exec(page)
  if (!m || page.length > MAX_DOWNLOAD_PAGE || /(^|\.)xn--/i.test(m[1]!) || (m[2] && (Number(m[2]) < 1 || Number(m[2]) > 65535))) return DEFAULT_DOWNLOAD_PAGE
  try {
    const u = new URL(page)
    return u.hostname === m[1]!.toLowerCase() && u.href.length <= MAX_DOWNLOAD_PAGE && ADDRESS_FORM.test(u.href) ? u.href : DEFAULT_DOWNLOAD_PAGE
  } catch {
    return DEFAULT_DOWNLOAD_PAGE
  }
}

/**
 * updatesLine is what the log says of updates on channel (updateChannel): from the feed, by the package manager with the
 * download page (page, read only then) by its origin and path alone, never its query or fragment, or none.
 */
export function updatesLine(channel: 'auto' | 'link' | 'none', packaged: boolean, page: () => string): string {
  if (channel === 'auto') return 'from the update feed this package names, checked every six hours'
  if (channel === 'link') {
    const u = new URL(page())
    return `by your package manager (${u.origin}${u.pathname})`
  }
  return packaged ? 'none: this build names no update feed' : 'none in development'
}

const EVERY = 6 * 60 * 60 * 1000
/** How long an install may take to quit the app before it is taken for one that did not start. */
const INSTALL_WAIT = 60 * 1000

/** What startUpdater needs of electron-updater's autoUpdater. */
export interface AppUpdater extends WatchedUpdater, InstallingUpdater {
  autoDownload: boolean
  autoInstallOnAppQuit: boolean
  logger: unknown
  checkForUpdates(): Promise<{ downloadPromise?: Promise<unknown> | null } | null>
}

/** What the app does with the updater it started (startUpdater). */
export interface StartedUpdater {
  /** Quits the app to install the update held (installVia). */
  install: () => Promise<void>
  /** Whether a check, or the download it started, is under way (UpdateChecks). */
  busy: () => boolean
  /** Starts no check from now on (UpdateChecks). */
  pause: () => void
}

/**
 * startUpdater checks for a release on start and every six hours where the
 * app updates itself, downloads it in the background and installs it on
 * quit; it logs what it does and never interrupts a session. electron-updater
 * is loaded lazily so the unit tests need no Electron. on hears what the
 * updater holds to install (watchUpdater): downloaded at every check that
 * finds the update downloaded, the same version again included
 * (electron-updater emits update-downloaded for a cached download too), and
 * gone once it holds it no more; UpdateNotice decides what is said. It
 * returns what installs the downloaded update now (quitting the app, which
 * stops the server: installVia) and what a restart to update holds the
 * checks with (UpdateChecks), null where the app does not update itself.
 */
export async function startUpdater(
  channel: 'auto' | 'link' | 'none',
  logs: Pick<Logs, 'line'>,
  on: UpdaterEvents,
  load: () => Promise<AppUpdater> = async () => (await import('electron-updater')).autoUpdater,
): Promise<StartedUpdater | null> {
  if (channel !== 'auto') return null
  try {
    const autoUpdater = await load()
    autoUpdater.autoDownload = true
    autoUpdater.autoInstallOnAppQuit = true
    autoUpdater.logger = { info: (m: unknown) => logs.line('main', `updater: ${String(m)}`), warn: (m: unknown) => logs.line('main', `updater: ${String(m)}`), error: (m: unknown) => logs.line('main', `updater: ${String(m)}`), debug: () => {} }
    const log = (line: string) => logs.line('main', `updater: ${line}`)
    watchUpdater(autoUpdater, on, log)
    const checks = new UpdateChecks(() => autoUpdater.checkForUpdates(), log)
    await checks.check()
    setInterval(() => void checks.check(), EVERY).unref()
    return { install: () => installVia(autoUpdater), busy: () => checks.busy(), pause: () => checks.pause() }
  } catch (e) {
    logs.line('main', `updater unavailable: ${(e as Error).message}`)
    return null
  }
}

/**
 * UpdateChecks runs the updater's checks (start: autoUpdater.checkForUpdates) and knows when one is under way: from its start
 * until the download it started, if any, is done or has failed. Only then does electron-updater empty or replace its cache
 * (watchUpdater), the same version's included: one whose cached installer is gone is downloaded again. A restart to update
 * starts only while none is (busy), and pauses them, so the update it installs stays while the server stops.
 */
export class UpdateChecks {
  private running = 0
  private paused = false

  constructor(
    private readonly start: () => Promise<{ downloadPromise?: Promise<unknown> | null } | null>,
    private readonly log: (line: string) => void,
  ) {}

  /** check starts one unless paused, and settles with the check; a failed one is logged, a failed download said by error. */
  check(): Promise<void> {
    if (this.paused) return Promise.resolve()
    this.running++
    let checked: Promise<{ downloadPromise?: Promise<unknown> | null } | null>
    try {
      checked = this.start()
    } catch (e) {
      checked = Promise.reject(e)
    }
    void checked
      .then((r) => r?.downloadPromise)
      .catch(() => {})
      .finally(() => this.running--)
    return checked.then(
      () => {},
      (e: Error) => this.log(e.message),
    )
  }

  /** busy says whether a check, or the download it started, is under way. */
  busy(): boolean {
    return this.running > 0
  }

  /** pause starts no check from now on: the app restarts to install. */
  pause(): void {
    this.paused = true
  }
}

/** What the app hears of the update the updater holds to install (watchUpdater). */
export interface UpdaterEvents {
  /** An installer for version is held: a download done, or a check that found it cached, the same version again included. */
  downloaded: (version: string) => void
  /** The installer held is gone, and why: nothing installs until the next is downloaded. */
  gone: (why: string) => void
}

/** What watchUpdater needs of electron-updater's autoUpdater: its events. */
export interface WatchedUpdater {
  on(event: 'update-available', listener: (info: { version: string }) => void): unknown
  on(event: 'update-downloaded', listener: (info: { version: string }) => void): unknown
  on(event: 'error', listener: (e: Error) => void): unknown
}

/**
 * watchUpdater follows what electron-updater (6.8) holds to install. update-downloaded is an installer held, at every check that
 * finds it cached too. A check that finds another version empties the cache as that one's download begins
 * (DownloadedUpdateHelper.getValidCachedUpdateFile), and a download that fails clears what was held (AppUpdater.executeDownload's
 * removeFileIfAny): the update said to be ready is gone then, and offered still, Restart to update would stop the server and its
 * sessions and quit with nothing to install. A check that fails, or an install, holds on to it. log says what failed a download,
 * which electron-updater emits and does not log.
 */
export function watchUpdater(updater: WatchedUpdater, on: UpdaterEvents, log: (line: string) => void): void {
  let held = ''
  let downloading = false
  const gone = (why: string) => {
    if (!held) return
    held = ''
    on.gone(why)
  }
  updater.on('update-available', (info) => {
    // The app downloads what a check finds (autoDownload).
    downloading = true
    if (info.version !== held) gone(`a check found ${info.version}`)
  })
  updater.on('update-downloaded', (info) => {
    downloading = false
    held = info.version
    on.downloaded(info.version)
  })
  updater.on('error', (e) => {
    if (!downloading) return
    downloading = false
    log(`the download failed: ${e.message}`)
    gone('its download failed')
  })
}

/** What installVia needs of electron-updater's autoUpdater. */
export interface InstallingUpdater {
  quitAndInstall(): void
  once(event: 'error', listener: (e: Error) => void): unknown
  removeListener(event: 'error', listener: (e: Error) => void): unknown
}

/**
 * installVia asks the updater to quit the app and install. One that starts quits the app, and the promise never settles. One
 * that fails rejects: electron-updater does not throw then but emits error (an installer that cannot be written, say, or
 * macOS's updater refusing an unsigned app), and one that neither quits nor fails within wait is taken for failed too.
 */
export function installVia(updater: InstallingUpdater, wait = INSTALL_WAIT): Promise<void> {
  return new Promise<void>((_resolve, reject) => {
    const failed = (e: Error) => {
      clearTimeout(timer)
      updater.removeListener('error', failed)
      reject(e)
    }
    const timer = setTimeout(() => failed(new Error(`the app did not quit to install within ${Math.round(wait / 1000)} s`)), wait)
    updater.once('error', failed)
    try {
      updater.quitAndInstall()
    } catch (e) {
      failed(e as Error)
    }
  })
}

/**
 * What the workbench is told of the update downloaded as it asks: its version ('' for none), whether to say so, and the version
 * the tray's Restart to update asked for, which the page asks in its dialog ('' for none).
 */
export interface UpdateTold {
  ready: string
  announce: boolean
  restart: string
}

/**
 * UpdateNotice decides what the workbench is told of the updates downloaded (#290). electron-updater says an update is
 * downloaded at every check that finds it (on start and every six hours) while the app runs on, in the tray; each was a dialog
 * of the app's own, left open on top of the last. Now the workbench asks (as it loads, and when the app says a check found one),
 * and an update is said once per version: to the first page that asks after it is downloaded, across starts, so that one found
 * while no window was open, or before the page was ready, is said when one is. announced() is the version said last (the
 * settings' updateAnnounced), remember() keeps the one said now.
 */
export class UpdateNotice {
  private version = ''
  private owed = false
  private restart = ''

  constructor(
    private readonly announced: () => string,
    private readonly remember: (version: string) => void,
  ) {}

  /** downloaded records the update a check found downloaded: owed to the workbench unless its version was said already. */
  downloaded(version: string): void {
    this.version = version
    this.owed = this.announced() !== version
  }

  /** withdraw records that the update downloaded is gone (watchUpdater): none is ready, nor owed, until the next is downloaded. */
  withdraw(): void {
    this.version = ''
    this.owed = false
    this.restart = ''
  }

  /**
   * ask records Restart to update to version asked from the tray: the workbench asks it in its dialog, which says what the restart
   * ends, before anything does.
   */
  ask(version: string): void {
    this.restart = version
  }

  /** ready is the version downloaded and held to install, '' for none. */
  ready(): string {
    return this.version
  }

  /**
   * take is what the page asking is told: the update ready, and whether to say so, which only the first to ask for it is told; so
   * is the tray's ask, for the update ready still.
   */
  take(): UpdateTold {
    const announce = this.owed
    if (announce) {
      this.owed = false
      this.remember(this.version)
    }
    const restart = this.restart === this.version ? this.restart : ''
    this.restart = ''
    return { ready: this.version, announce, restart }
  }
}

/**
 * offerUpdates is what the app does as the updater holds an update and lets it go (#290): the notice, the tray's item (tray,
 * '' for none), every workbench window told (tell), and the version downloaded kept for the next start (keep, failedAtStart).
 * What is offered follows the updater whatever keeping it does: a settings file that cannot be written is logged, and never
 * leaves offered an update that is gone, whose restart would stop the server for nothing.
 */
export function offerUpdates(
  notice: UpdateNotice,
  d: { tray: (version: string) => void; tell: () => void; keep: (downloaded: string) => void; log: (line: string) => void },
): UpdaterEvents {
  const keep = (version: string) => {
    try {
      d.keep(version)
    } catch (e) {
      d.log(`updates: the settings could not be saved: ${(e as Error).message}`)
    }
  }
  return {
    downloaded: (version) => {
      keep(version)
      notice.downloaded(version)
      d.tray(version)
      d.tell()
    },
    gone: (why) => {
      d.log(`updates: ${notice.ready()} is no longer ready to install: ${why}`)
      notice.withdraw()
      d.tray('')
      d.tell()
      keep('')
    },
  }
}

/** What restartToInstall needs of the app. */
export interface RestartDeps {
  /** The version the updater holds to install now, '' for none (UpdateNotice.ready). */
  ready: () => string
  /** Whether a check, or the download it started, is under way: either can empty or replace what the updater holds. */
  busy: () => boolean
  /** Starts no check from now on. */
  pause: () => void
  /** Lets the windows close and a quit go through without stopping the server again (main.ts's quitting). */
  letGo: () => void
  stopServer: () => Promise<void>
  /** The updater quits the app to install (installVia): never settles when it does, rejects when it does not. */
  install: () => Promise<void>
  /** Quits the app, the server stopped already. */
  quit: () => void
  log: (line: string) => void
}

/** What a restart to update came to: under way; refused, the version gone; or put off while a check is under way. */
export type RestartOutcome = 'started' | 'gone' | 'busy'

/**
 * restartToInstall is Restart to update to version (#290), asked from the workbench or the tray. It starts only while the updater
 * still holds that version (ready): one gone since (watchUpdater) would stop the server and its sessions and quit with nothing
 * to install. While a check is under way (busy), which may yet empty or replace what is held, it is put off; once it starts, no
 * check does (pause), so what is held stays while the server stops. The server stops first, as a quit stops it; then the windows
 * may close and the quit go through (closing to the tray would hide them, and macOS's updater closes them before it quits) and
 * the updater quits the app to install. An install that does not go through quits the app all the same: the person asked for
 * the restart, the server and its sessions are stopped already, and what the updater still has queued (Windows' quit after an
 * installer that did not launch, macOS's install once its native updater is ready) ends in a quit too, never in a server started
 * again that a late install would stop unasked. The next start says the update did not install (failedAtStart). done settles
 * when the install did not go through; one that does quits the app first.
 */
export function restartToInstall(version: string, d: RestartDeps): { outcome: RestartOutcome; done: Promise<void> } {
  const held = d.ready()
  if (!version || held !== version) {
    d.log(`updates: no restart to update to ${version || 'no version'}: ${held ? `the update ready is ${held}` : 'no update is ready'}`)
    return { outcome: 'gone', done: Promise.resolve() }
  }
  if (d.busy()) {
    d.log(`updates: no restart to update to ${version} yet: a check for updates is under way`)
    return { outcome: 'busy', done: Promise.resolve() }
  }
  d.pause()
  d.log(`updates: restarting to install ${version}`)
  const done = (async () => {
    d.letGo()
    await d.stopServer().catch(() => {})
    try {
      await d.install()
    } catch (e) {
      d.log(`updates: the install did not go through: ${(e as Error).message}; quitting`)
      d.quit()
    }
  })()
  return { outcome: 'started', done }
}

/**
 * failedAtStart is the update this start of the app says did not install, '' for none: the one downloaded last and held still
 * (settings' updateDownloaded, which gone clears), newer than the version running, not said already. Whatever the way the install
 * was tried (a quit, Restart to update from the workbench or the tray) and whether the update was ever announced.
 */
export function failedAtStart(s: { updateDownloaded: string; updateFailedNoticed: string }, running: string): string {
  const tried = s.updateDownloaded
  return installFailed(tried, running) && s.updateFailedNoticed !== tried ? tried : ''
}

/**
 * installFailed says whether the update downloaded is newer than the version running: the app was quit, or ended, since, and the
 * update did not install (macOS, for one, updates only a signed app). '' or unreadable: no.
 */
export function installFailed(downloaded: string, running: string): boolean {
  return downloaded !== '' && compareVersions(downloaded, running) > 0
}

/**
 * compareVersions orders two release versions as semver does ("0.7.0-rc.12" before "0.7.0", "rc.12" after "rc.9"): negative
 * when a is older, 0 when they are the same or either does not read as one, positive when a is newer.
 */
export function compareVersions(a: string, b: string): number {
  const parse = (v: string) => /^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$/.exec(v.trim())
  const x = parse(a)
  const y = parse(b)
  if (!x || !y) return 0
  for (let i = 1; i <= 3; i++) {
    const d = Number(x[i]) - Number(y[i])
    if (d !== 0) return Math.sign(d)
  }
  const px = x[4]?.split('.') ?? []
  const py = y[4]?.split('.') ?? []
  // A release is newer than its prereleases.
  if (!px.length || !py.length) return Math.sign(py.length - px.length)
  for (let i = 0; i < Math.max(px.length, py.length); i++) {
    if (i >= px.length) return -1
    if (i >= py.length) return 1
    const nx = /^\d+$/.test(px[i]!)
    const ny = /^\d+$/.test(py[i]!)
    if (nx && ny) {
      const d = Number(px[i]) - Number(py[i])
      if (d !== 0) return Math.sign(d)
    } else if (nx !== ny) {
      return nx ? -1 : 1
    } else if (px[i] !== py[i]) {
      return px[i]! < py[i]! ? -1 : 1
    }
  }
  return 0
}
