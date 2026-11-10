import { describe, expect, it } from 'vitest'
import { EventEmitter } from 'node:events'
import {
  DEFAULT_DOWNLOAD_PAGE,
  UpdateChecks,
  UpdateNotice,
  compareVersions,
  downloadPage,
  failedAtStart,
  installFailed,
  installVia,
  offerUpdates,
  restartToInstall,
  startUpdater,
  updateChannel,
  watchUpdater,
  type AppUpdater,
} from '../src/updater'

describe('update channel', () => {
  it('updates itself on macOS, Windows and AppImage, points at the download page for deb and rpm, and never in development', () => {
    expect(updateChannel('darwin', false, true, true)).toBe('auto')
    expect(updateChannel('win32', false, true, true)).toBe('auto')
    expect(updateChannel('linux', true, true, true)).toBe('auto')
    expect(updateChannel('linux', false, true, true)).toBe('link')
    expect(updateChannel('linux', true, false, true)).toBe('none')
  })

  // A package built with no publish target has no app-update.yml: an updater there would fail at every check.
  it('no feed in the package is no updater', async () => {
    for (const [platform, appImage] of [
      ['darwin', false],
      ['win32', false],
      ['linux', true],
      ['linux', false],
    ] as const) {
      expect(updateChannel(platform, appImage, true, false), `${platform} ${appImage}`).toBe('none')
      expect(updateChannel(platform, appImage, false, false)).toBe('none')
    }
    const lines: string[] = []
    let loaded = 0
    const started = await startUpdater('none', { line: (_s, l) => void lines.push(l) }, { downloaded: () => {}, gone: () => {} }, async () => {
      loaded++
      throw new Error('never loaded')
    })
    expect(started).toBeNull()
    expect(loaded).toBe(0)
    expect(lines).toEqual([])
  })
})

describe('the download page', () => {
  const pkg = (downloadPage: unknown) => JSON.stringify({ name: 'conductor-desktop', version: '0.7.0', conductor: { downloadPage } })

  it('the download page from the package', () => {
    expect(downloadPage(pkg('https://example.test/download'))).toBe('https://example.test/download')
    expect(downloadPage(pkg('https://example.test/download?from=app#mac'))).toBe('https://example.test/download?from=app#mac')
    const at200 = 'https://example.test/' + 'a'.repeat(200 - 'https://example.test/'.length)
    expect(downloadPage(pkg(at200))).toBe(at200)
    // None named: the repository's releases, all of them (/releases/latest answers 404 while every release is a prerelease).
    expect(DEFAULT_DOWNLOAD_PAGE).toBe('https://github.com/rocksolidlabs/conductor/releases')
    expect(downloadPage(JSON.stringify({ name: 'conductor-desktop' }))).toBe(DEFAULT_DOWNLOAD_PAGE)
    expect(downloadPage(JSON.stringify({ conductor: {} }))).toBe(DEFAULT_DOWNLOAD_PAGE)
    expect(downloadPage(null)).toBe(DEFAULT_DOWNLOAD_PAGE)
  })

  it('a bad download page is ignored', () => {
    const at200 = 'https://example.test/' + 'a'.repeat(200 - 'https://example.test/'.length)
    for (const bad of [
      'http://example.test/download',
      'javascript:alert(1)',
      'file:///etc/passwd',
      'https://',
      '/releases',
      'example.test/download',
      'https://user:secret@example.test/download',
      'https://example.test/a b',
      'https://example.test/a\tb',
      'https://example.test/a\nb',
      ' https://example.test/download',
      'https://example.test/\u007f',
      at200 + 'a',
      // Fewer than 200 characters, more than 200 bytes.
      'https://example.test/' + 'é'.repeat(100),
      '',
      42,
      true,
      null,
      ['https://example.test/download'],
      { url: 'https://example.test/download' },
    ]) {
      expect(downloadPage(pkg(bad)), JSON.stringify(bad)).toBe(DEFAULT_DOWNLOAD_PAGE)
    }
    for (const text of ['', 'not json', '{"conductor":', 'null', '[]', '"https://example.test/download"', '{"conductor":"https://example.test/download"}']) {
      expect(downloadPage(text), text).toBe(DEFAULT_DOWNLOAD_PAGE)
    }
  })
})

/** An autoUpdater as electron-updater's: checkForUpdates as given, its events heard. */
class FakeAutoUpdater extends EventEmitter {
  autoDownload = false
  autoInstallOnAppQuit = false
  logger: unknown = null
  checks = 0
  constructor(private readonly checking: () => Promise<{ downloadPromise?: Promise<unknown> | null } | null>) {
    super()
  }
  checkForUpdates() {
    this.checks++
    return this.checking()
  }
  quitAndInstall() {}
}

describe('starting the updater', () => {
  it('a feed that cannot be reached is logged and the app runs on', async () => {
    const lines: string[] = []
    const heard: string[] = []
    const u = new FakeAutoUpdater(() => Promise.reject(new Error('getaddrinfo ENOTFOUND updates.example.test')))
    const started = await startUpdater(
      'auto',
      { line: (s, l) => void lines.push(`${s}: ${l}`) },
      { downloaded: (v) => heard.push(`downloaded ${v}`), gone: (why) => heard.push(`gone: ${why}`) },
      async () => u as unknown as AppUpdater,
    )
    expect(started).not.toBeNull()
    expect(u.checks).toBe(1)
    expect(u.autoDownload && u.autoInstallOnAppQuit).toBe(true)
    expect(lines).toEqual(['main: updater: getaddrinfo ENOTFOUND updates.example.test'])
    // The error electron-updater emits for the failed check lets nothing go, and nothing is under way.
    u.emit('error', new Error('getaddrinfo ENOTFOUND updates.example.test'))
    expect(heard).toEqual([])
    await new Promise((r) => setTimeout(r, 0))
    expect(started!.busy()).toBe(false)
  })

  it('an updater that cannot be loaded is logged and the app runs on', async () => {
    const lines: string[] = []
    const started = await startUpdater('auto', { line: (_s, l) => void lines.push(l) }, { downloaded: () => {}, gone: () => {} }, async () => {
      throw new Error('Cannot find module electron-updater')
    })
    expect(started).toBeNull()
    expect(lines).toEqual(['updater unavailable: Cannot find module electron-updater'])
  })
})

/** A notice over a store of the version said last, as the settings keep it across starts. */
function notice(store: { announced: string }) {
  return new UpdateNotice(
    () => store.announced,
    (v) => (store.announced = v),
  )
}

describe('the update notice (#290)', () => {
  it('says an update once, to the first page that asks, however many checks find it', () => {
    const store = { announced: '' }
    const n = notice(store)
    expect(n.take()).toEqual({ ready: '', announce: false, restart: '' })
    n.downloaded('0.7.0-rc.12')
    expect(n.take()).toEqual({ ready: '0.7.0-rc.12', announce: true, restart: '' })
    expect(store.announced).toBe('0.7.0-rc.12')
    // Another page, and the checks six hours apart that find the same download.
    expect(n.take()).toEqual({ ready: '0.7.0-rc.12', announce: false, restart: '' })
    for (let i = 0; i < 4; i++) n.downloaded('0.7.0-rc.12')
    expect(n.take()).toEqual({ ready: '0.7.0-rc.12', announce: false, restart: '' })
  })

  it('says it to a page that asks later when none was there to ask, and never again after a restart', () => {
    const store = { announced: '' }
    const first = notice(store)
    first.downloaded('0.7.0-rc.12')
    first.downloaded('0.7.0-rc.12')
    expect(store.announced).toBe('')
    // The app quit before a page asked: the next start finds the download and owes it still.
    const next = notice(store)
    next.downloaded('0.7.0-rc.12')
    expect(next.take().announce).toBe(true)
    const after = notice(store)
    after.downloaded('0.7.0-rc.12')
    expect(after.take()).toEqual({ ready: '0.7.0-rc.12', announce: false, restart: '' })
  })

  it('says a newer version again', () => {
    const store = { announced: '0.7.0-rc.12' }
    const n = notice(store)
    n.downloaded('0.7.0-rc.12')
    expect(n.take().announce).toBe(false)
    n.downloaded('0.7.0')
    expect(n.take()).toEqual({ ready: '0.7.0', announce: true, restart: '' })
    expect(store.announced).toBe('0.7.0')
  })

  it('offers nothing once the update downloaded is gone, and says the next one', () => {
    const store = { announced: '' }
    const n = notice(store)
    n.downloaded('0.7.0-rc.12')
    expect(n.ready()).toBe('0.7.0-rc.12')
    n.withdraw()
    expect(n.ready()).toBe('')
    expect(n.take()).toEqual({ ready: '', announce: false, restart: '' })
    expect(store.announced).toBe('')
    n.downloaded('0.7.0-rc.13')
    expect(n.take()).toEqual({ ready: '0.7.0-rc.13', announce: true, restart: '' })
  })

  // Restart to update from the tray is asked in the workbench's dialog, which says what the restart ends: handed to one page, for
  // the update ready still.
  it("hands the tray's restart to one page, for the update ready still", () => {
    const n = notice({ announced: '0.7.0-rc.12' })
    n.downloaded('0.7.0-rc.12')
    n.ask('0.7.0-rc.12')
    expect(n.take()).toEqual({ ready: '0.7.0-rc.12', announce: false, restart: '0.7.0-rc.12' })
    expect(n.take().restart).toBe('')
    // Asked, then gone, or another downloaded: nothing to ask.
    n.ask('0.7.0-rc.12')
    n.withdraw()
    expect(n.take().restart).toBe('')
    n.downloaded('0.7.0-rc.12')
    n.ask('0.7.0-rc.12')
    n.downloaded('0.7.0-rc.13')
    expect(n.take()).toEqual({ ready: '0.7.0-rc.13', announce: true, restart: '' })
  })

  it('knows an install that did not take: the version downloaded is newer than the one running', () => {
    expect(installFailed('0.7.0-rc.12', '0.7.0-rc.11')).toBe(true)
    expect(installFailed('0.7.0-rc.12', '0.7.0-rc.12')).toBe(false)
    expect(installFailed('0.7.0-rc.12', '0.7.0')).toBe(false)
    expect(installFailed('', '0.7.0-rc.11')).toBe(false)
    expect(installFailed('not a version', '0.7.0-rc.11')).toBe(false)
  })

  it('says at the start an update downloaded that did not install, told or not, once', () => {
    const s = { updateDownloaded: '0.7.0-rc.12', updateFailedNoticed: '' }
    // Never announced (no page asked: the window hidden), tried from the tray, and the app came back as the version before.
    expect(failedAtStart(s, '0.7.0-rc.11')).toBe('0.7.0-rc.12')
    expect(failedAtStart({ ...s, updateFailedNoticed: '0.7.0-rc.12' }, '0.7.0-rc.11')).toBe('')
    expect(failedAtStart(s, '0.7.0-rc.12')).toBe('')
    // Announced, then gone (a check found a newer one, or a download failed): the updater held nothing to install, so nothing failed.
    expect(failedAtStart({ updateDownloaded: '', updateFailedNoticed: '' }, '0.7.0-rc.11')).toBe('')
  })

  it('orders versions as semver does', () => {
    const order = ['0.6.9', '0.7.0-rc.2', '0.7.0-rc.9', '0.7.0-rc.11', '0.7.0-rc.12', '0.7.0', 'v0.7.1', '1.0.0-alpha', '1.0.0-alpha.1', '1.0.0-beta', '1.0.0']
    for (let i = 0; i + 1 < order.length; i++) {
      expect(compareVersions(order[i]!, order[i + 1]!), `${order[i]} before ${order[i + 1]}`).toBeLessThan(0)
      expect(compareVersions(order[i + 1]!, order[i]!)).toBeGreaterThan(0)
      expect(compareVersions(order[i]!, order[i]!)).toBe(0)
    }
    expect(compareVersions('0.7.0+build.5', '0.7.0')).toBe(0)
  })
})

/**
 * An updater as electron-updater's BaseUpdater behaves: quitAndInstall either quits the app (here: says so) or, when the
 * install fails, emits error and returns, throwing nothing.
 */
class FakeUpdater extends EventEmitter {
  constructor(private readonly outcome: 'quits' | 'fails' | 'fails later' | 'hangs') {
    super()
  }
  quits = 0
  quitAndInstall(): void {
    if (this.outcome === 'quits') this.quits++
    else if (this.outcome === 'fails') this.emit('error', new Error('EACCES: permission denied, open the installer'))
    else if (this.outcome === 'fails later') setTimeout(() => this.emit('error', new Error('Could not get code signature for running application')), 10)
  }
}

describe('installing through the updater (#290)', () => {
  it('rejects with the error the updater emits, at once or later, and never with a listener left behind', async () => {
    for (const outcome of ['fails', 'fails later'] as const) {
      const u = new FakeUpdater(outcome)
      await expect(installVia(u, 1000)).rejects.toThrow(/EACCES|code signature/)
      expect(u.listenerCount('error')).toBe(0)
    }
  })

  it('rejects an install that neither quits nor fails in time', async () => {
    const u = new FakeUpdater('hangs')
    await expect(installVia(u, 30)).rejects.toThrow('the app did not quit to install within 0 s')
    expect(u.listenerCount('error')).toBe(0)
  })

  it('settles nothing for an install that quits the app', async () => {
    const u = new FakeUpdater('quits')
    const settled = await Promise.race([installVia(u, 1000).then(() => 'settled', () => 'settled'), new Promise((r) => setTimeout(() => r('pending'), 50))])
    expect(settled).toBe('pending')
    expect(u.quits).toBe(1)
  })
})

/** An updater's events as electron-updater emits them: a check that finds a version, its download done, an error. */
class Events extends EventEmitter {
  found(version: string) {
    this.emit('update-available', { version })
  }
  downloaded(version: string) {
    this.emit('update-downloaded', { version })
  }
  failed(message: string) {
    this.emit('error', new Error(message))
  }
}

describe('what the updater holds to install (#290)', () => {
  function watched() {
    const u = new Events()
    const heard: string[] = []
    watchUpdater(u, { downloaded: (v) => heard.push(`downloaded ${v}`), gone: (why) => heard.push(`gone: ${why}`) }, (l) => heard.push(`log: ${l}`))
    return { u, heard }
  }

  it('holds the update downloaded through the checks that find it cached again', () => {
    const { u, heard } = watched()
    u.found('0.7.0-rc.12')
    u.downloaded('0.7.0-rc.12')
    for (let i = 0; i < 3; i++) {
      u.found('0.7.0-rc.12')
      u.downloaded('0.7.0-rc.12')
    }
    expect(heard).toEqual(Array(4).fill('downloaded 0.7.0-rc.12'))
  })

  // electron-updater empties its cache as a newer version's download begins: the one held is gone whether that download
  // succeeds or fails.
  it('lets the update go once a check finds a newer one, and holds the newer one downloaded', () => {
    const { u, heard } = watched()
    u.found('0.7.0-rc.12')
    u.downloaded('0.7.0-rc.12')
    u.found('0.7.0-rc.13')
    expect(heard).toEqual(['downloaded 0.7.0-rc.12', 'gone: a check found 0.7.0-rc.13'])
    u.failed('net::ERR_CONNECTION_RESET')
    expect(heard.slice(2)).toEqual(['log: the download failed: net::ERR_CONNECTION_RESET'])
    u.found('0.7.0-rc.13')
    u.downloaded('0.7.0-rc.13')
    expect(heard.slice(3)).toEqual(['downloaded 0.7.0-rc.13'])
  })

  it('lets the update go when downloading it again fails, and holds on through a check or an install that fails', () => {
    const { u, heard } = watched()
    u.found('0.7.0-rc.12')
    u.downloaded('0.7.0-rc.12')
    // A check that fails, and an install, emit error with no download under way.
    u.failed('getaddrinfo ENOTFOUND github.com')
    u.failed('EACCES: permission denied, open the installer')
    expect(heard).toEqual(['downloaded 0.7.0-rc.12'])
    // The cache gone (cleaned away), the same version is downloaded again, and that fails.
    u.found('0.7.0-rc.12')
    u.failed('sha512 checksum mismatch')
    expect(heard.slice(1)).toEqual(['log: the download failed: sha512 checksum mismatch', 'gone: its download failed'])
  })

  it('says a failed download with none held, and lets nothing go', () => {
    const { u, heard } = watched()
    u.found('0.7.0-rc.12')
    u.failed('net::ERR_INTERNET_DISCONNECTED')
    expect(heard).toEqual(['log: the download failed: net::ERR_INTERNET_DISCONNECTED'])
  })
})

// What the app offers follows the updater first: a settings file that cannot be written must not leave an update offered that
// is gone, whose restart would stop the server for nothing.
describe('offering the update held (#290)', () => {
  function offer(keep: (v: string) => void) {
    const steps: string[] = []
    const n = notice({ announced: '' })
    const on = offerUpdates(n, { tray: (v) => steps.push(`tray ${v || 'none'}`), tell: () => steps.push('tell'), keep, log: (l) => steps.push(l) })
    return { n, on, steps }
  }

  it('offers the update downloaded, keeps it, and lets it go everywhere once gone', () => {
    const kept: string[] = []
    const { n, on, steps } = offer((v) => kept.push(v))
    on.downloaded('0.7.0-rc.12')
    expect(n.ready()).toBe('0.7.0-rc.12')
    on.gone('a check found 0.7.0-rc.13')
    expect(n.ready()).toBe('')
    expect(kept).toEqual(['0.7.0-rc.12', ''])
    expect(steps).toEqual(['tray 0.7.0-rc.12', 'tell', 'updates: 0.7.0-rc.12 is no longer ready to install: a check found 0.7.0-rc.13', 'tray none', 'tell'])
  })

  it('offers and lets go all the same when the settings cannot be saved', () => {
    const { n, on, steps } = offer(() => {
      throw new Error("EACCES: permission denied, open 'settings.json.tmp'")
    })
    on.downloaded('0.7.0-rc.12')
    expect(n.ready()).toBe('0.7.0-rc.12')
    on.gone('its download failed')
    expect(n.ready()).toBe('')
    expect(steps.filter((s) => s.startsWith('tray') || s === 'tell')).toEqual(['tray 0.7.0-rc.12', 'tell', 'tray none', 'tell'])
    expect(steps.filter((s) => s.includes('could not be saved'))).toHaveLength(2)
  })
})

/** A check as electron-updater's checkForUpdates runs one: settled with the check, its download going on after. */
function deferred<T>() {
  let resolve!: (v: T) => void
  let reject!: (e: Error) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

describe('the checks for updates (#290)', () => {
  it('are under way from the start of a check until the download it started is done or has failed', async () => {
    const lines: string[] = []
    const check = deferred<{ downloadPromise?: Promise<unknown> | null } | null>()
    const download = deferred<unknown>()
    const checks = new UpdateChecks(() => check.promise, (l) => lines.push(l))
    expect(checks.busy()).toBe(false)
    const checked = checks.check()
    expect(checks.busy()).toBe(true)
    check.resolve({ downloadPromise: download.promise })
    await checked
    // The same version downloaded again (its cached installer gone), or another one: the cache can change until it is done.
    expect(checks.busy()).toBe(true)
    download.reject(new Error('net::ERR_CONNECTION_RESET'))
    await new Promise((r) => setTimeout(r, 0))
    expect(checks.busy()).toBe(false)
    expect(lines).toEqual([])
  })

  it('logs a check that fails, which starts no download', async () => {
    const lines: string[] = []
    const checks = new UpdateChecks(() => Promise.reject(new Error('getaddrinfo ENOTFOUND github.com')), (l) => lines.push(l))
    await checks.check()
    await new Promise((r) => setTimeout(r, 0))
    expect(checks.busy()).toBe(false)
    expect(lines).toEqual(['getaddrinfo ENOTFOUND github.com'])
  })

  it('starts none once paused', async () => {
    let started = 0
    const checks = new UpdateChecks(async () => {
      started++
      return null
    }, () => {})
    await checks.check()
    checks.pause()
    await checks.check()
    expect(started).toBe(1)
    expect(checks.busy()).toBe(false)
  })
})

describe('restart to install (#290)', () => {
  function app(install: () => Promise<void>, ready = '0.7.0-rc.12', busy = false) {
    const steps: string[] = []
    return {
      steps,
      d: {
        ready: () => ready,
        busy: () => busy,
        pause: () => void steps.push('pause'),
        letGo: () => void steps.push('let go'),
        stopServer: async () => void steps.push('stop server'),
        install: () => {
          steps.push('install')
          return install()
        },
        quit: () => void steps.push('quit'),
        log: (l: string) => steps.push(l),
      },
    }
  }

  // No check starts once it does: one could empty or replace the update held while the server stops.
  it('pauses the checks, stops the server, lets the windows close and the quit through, then installs', async () => {
    const a = app(() => new Promise(() => {}))
    expect(restartToInstall('0.7.0-rc.12', a.d).outcome).toBe('started')
    await new Promise((r) => setTimeout(r, 20))
    expect(a.steps).toEqual(['pause', 'updates: restarting to install 0.7.0-rc.12', 'let go', 'stop server', 'install'])
  })

  // A check, or the download it started, may yet empty or replace what is held: the same version's too, downloaded again
  // when its cached installer is gone.
  it('is put off while a check is under way, and stops nothing', async () => {
    const a = app(() => Promise.reject(new Error('never asked')), '0.7.0-rc.12', true)
    const r = restartToInstall('0.7.0-rc.12', a.d)
    await r.done
    expect(r.outcome).toBe('busy')
    expect(a.steps).toEqual(['updates: no restart to update to 0.7.0-rc.12 yet: a check for updates is under way'])
  })

  // A check that found a newer version, or a download that failed, took the update offered away: a restart would stop the
  // server and its sessions and quit with nothing to install.
  it('stops nothing for a version the updater no longer holds', async () => {
    for (const [held, asked, why] of [
      ['', '0.7.0-rc.12', 'no update is ready'],
      ['0.7.0-rc.13', '0.7.0-rc.12', 'the update ready is 0.7.0-rc.13'],
      ['0.7.0-rc.12', '', 'the update ready is 0.7.0-rc.12'],
    ] as const) {
      const a = app(() => Promise.reject(new Error('never asked')), held)
      const r = restartToInstall(asked, a.d)
      await r.done
      expect(r.outcome).toBe('gone')
      expect(a.steps).toEqual([`updates: no restart to update to ${asked || 'no version'}: ${why}`])
    }
  })

  // Never a server started again, which what the updater still has queued (a quit, a late install) would stop unasked: the
  // app quits, and the next start says the update did not install.
  it('quits all the same when the updater emits an error, or the install neither quits nor fails in time', async () => {
    const failed = app(() => installVia(new FakeUpdater('fails'), 1000))
    await restartToInstall('0.7.0-rc.12', failed.d).done
    expect(failed.steps).toEqual(['pause', 'updates: restarting to install 0.7.0-rc.12', 'let go', 'stop server', 'install', 'updates: the install did not go through: EACCES: permission denied, open the installer; quitting', 'quit'])
    const stuck = app(() => installVia(new FakeUpdater('hangs'), 30))
    await restartToInstall('0.7.0-rc.12', stuck.d).done
    expect(stuck.steps).toEqual(['pause', 'updates: restarting to install 0.7.0-rc.12', 'let go', 'stop server', 'install', 'updates: the install did not go through: the app did not quit to install within 0 s; quitting', 'quit'])
  })
})
