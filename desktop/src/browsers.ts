import { spawn, type ChildProcess, type SpawnOptions } from 'node:child_process'
import { realpathSync, statSync } from 'node:fs'
import { homedir } from 'node:os'
import { posix, win32 } from 'node:path'
import { external } from './window'

/**
 * The browser an agent's links open in (#436), as the server's catalog names it (internal/catalog/browser.go): one of five, found
 * by the app on the computer it runs on, so one setting means the same browser on any of the person's computers; a profile, a
 * Chromium profile directory, for Chrome, Edge and Brave alone.
 */
export const BROWSER_NAMES = ['chrome', 'edge', 'firefox', 'brave', 'safari'] as const
export type BrowserName = (typeof BROWSER_NAMES)[number]
export interface BrowserChoice {
  name: BrowserName
  profile?: string
}

/** What became of a link: in the browser named, in the default one (none named; the one named not found here; it did not start). */
export type OpenOutcome = 'browser' | 'default' | 'missing' | 'failed'

/** The server's bound on a profile (catalog.MaxBrowserProfile), in characters. */
export const MAX_PROFILE = 100

const PROFILED: ReadonlySet<string> = new Set(['chrome', 'edge', 'brave'])

/**
 * asBrowserChoice reads what the page sent as a browser the app knows, by the server's rule: a name of the five, and a profile only
 * for a Chromium one, of at most MAX_PROFILE characters, with no control character or quote and no space at either end; null for
 * anything else, which the app refuses.
 */
export function asBrowserChoice(v: unknown): BrowserChoice | null {
  if (!v || typeof v !== 'object') return null
  const { name, profile } = v as Record<string, unknown>
  if (typeof name !== 'string' || !(BROWSER_NAMES as readonly string[]).includes(name)) return null
  if (profile === undefined || profile === '') return { name: name as BrowserName }
  if (typeof profile !== 'string' || !PROFILED.has(name)) return null
  if ([...profile].length > MAX_PROFILE || profile.trim() !== profile || /[\p{Cc}"'`]/u.test(profile)) return null
  return { name: name as BrowserName, profile }
}

/** What the app needs of the computer to find a browser and start it: real ones in the app (systemBrowserDeps), fakes in a test. */
export interface BrowserDeps {
  platform: NodeJS.Platform
  env: Record<string, string | undefined>
  home: string
  /** Whether p is a regular file (Windows, Linux) or a directory (a macOS app bundle). */
  isFile(p: string): boolean
  isDir(p: string): boolean
  /** p with its links resolved; p itself when it cannot be. */
  realpath(p: string): string
  spawn(file: string, args: string[], options: SpawnOptions): Pick<ChildProcess, 'once' | 'unref'>
  openExternal(url: string): Promise<void>
}

const WINDOWS: Record<BrowserName, Array<[string, string]>> = {
  chrome: [
    ['ProgramFiles', 'Google\\Chrome\\Application\\chrome.exe'],
    ['ProgramFiles(x86)', 'Google\\Chrome\\Application\\chrome.exe'],
    ['LOCALAPPDATA', 'Google\\Chrome\\Application\\chrome.exe'],
  ],
  edge: [
    ['ProgramFiles(x86)', 'Microsoft\\Edge\\Application\\msedge.exe'],
    ['ProgramFiles', 'Microsoft\\Edge\\Application\\msedge.exe'],
    ['LOCALAPPDATA', 'Microsoft\\Edge\\Application\\msedge.exe'],
  ],
  firefox: [
    ['ProgramFiles', 'Mozilla Firefox\\firefox.exe'],
    ['ProgramFiles(x86)', 'Mozilla Firefox\\firefox.exe'],
    ['LOCALAPPDATA', 'Mozilla Firefox\\firefox.exe'],
  ],
  brave: [
    ['ProgramFiles', 'BraveSoftware\\Brave-Browser\\Application\\brave.exe'],
    ['ProgramFiles(x86)', 'BraveSoftware\\Brave-Browser\\Application\\brave.exe'],
    ['LOCALAPPDATA', 'BraveSoftware\\Brave-Browser\\Application\\brave.exe'],
  ],
  safari: [],
}

const MAC_APPS: Record<BrowserName, string> = {
  chrome: 'Google Chrome.app',
  edge: 'Microsoft Edge.app',
  firefox: 'Firefox.app',
  brave: 'Brave Browser.app',
  safari: 'Safari.app',
}

const LINUX_PROGRAMS: Record<BrowserName, string[]> = {
  chrome: ['google-chrome-stable', 'google-chrome'],
  edge: ['microsoft-edge-stable', 'microsoft-edge'],
  firefox: ['firefox'],
  brave: ['brave-browser', 'brave'],
  safari: [],
}

/** candidates are where the browser's program is on this OS, in the order looked at: an .exe on Windows, an app bundle on macOS, a program on PATH on Linux. */
export function candidates(name: BrowserName, d: Pick<BrowserDeps, 'platform' | 'env' | 'home'>): string[] {
  switch (d.platform) {
    case 'win32':
      return WINDOWS[name].flatMap(([key, rel]) => {
        const base = d.env[key]
        return base && win32.isAbsolute(base) ? [win32.join(base, rel)] : []
      })
    case 'darwin':
      return ['/Applications', posix.join(d.home, 'Applications')].map((dir) => posix.join(dir, MAC_APPS[name]))
    default: {
      const dirs = (d.env.PATH ?? '').split(':').filter((p) => posix.isAbsolute(p))
      return LINUX_PROGRAMS[name].flatMap((prog) => dirs.map((dir) => posix.join(dir, prog)))
    }
  }
}

/** refusedProgram says whether a program resolves to a batch file, which Windows runs through cmd.exe with its arguments reparsed: never started. */
export function refusedProgram(p: string): boolean {
  return /\.(bat|cmd)$/i.test(p)
}

/** findBrowser is the browser's program on this computer, the first candidate there; null when none is, or it resolves to a batch file. */
export function findBrowser(name: BrowserName, d: BrowserDeps): string | null {
  for (const c of candidates(name, d)) {
    if (!(d.platform === 'darwin' ? d.isDir(c) : d.isFile(c))) continue
    if (refusedProgram(c) || refusedProgram(d.realpath(c))) return null
    return c
  }
  return null
}

/** installedBrowsers are the browsers found on this computer, for the Agents page to mark. */
export function installedBrowsers(d: BrowserDeps): BrowserName[] {
  return BROWSER_NAMES.filter((n) => findBrowser(n, d) !== null)
}

/**
 * browserArgv is the command that opens url in the browser at program: the program, the profile switch, then the URL as one
 * argument, never a string a shell reads. On macOS an app bundle is started by open, which hands a running browser the link (-n so
 * that Chromium is given its profile switch even while it runs: the new process passes it to the one running and quits).
 */
export function browserArgv(program: string, choice: BrowserChoice, url: string, platform: NodeJS.Platform): { file: string; args: string[] } {
  const profile = choice.profile && PROFILED.has(choice.name) ? [`--profile-directory=${choice.profile}`] : []
  if (platform === 'darwin') {
    return profile.length ? { file: '/usr/bin/open', args: ['-n', '-a', program, '--args', ...profile, url] } : { file: '/usr/bin/open', args: ['-a', program, url] }
  }
  return { file: program, args: [...profile, url] }
}

/**
 * start starts file with args, detached, without a shell, and says whether it started. With helper (macOS's open, which starts the
 * browser and exits), started is the helper's exit 0: its own start says only that the helper ran, and it exits 1 when it could not
 * launch the app.
 */
function start(d: BrowserDeps, file: string, args: string[], helper: boolean): Promise<boolean> {
  return new Promise((resolve) => {
    let child: Pick<ChildProcess, 'once' | 'unref'>
    try {
      child = d.spawn(file, args, { shell: false, detached: true, stdio: 'ignore', windowsHide: false })
    } catch {
      resolve(false)
      return
    }
    child.once('spawn', () => {
      child.unref()
      if (!helper) resolve(true)
    })
    if (helper) child.once('exit', (code: number | null) => resolve(code === 0))
    child.once('error', () => resolve(false))
  })
}

/**
 * openUrl opens url, one tab: in the browser choice names when it is on this computer, with its profile, else (none named, not
 * found, did not start) through the system's default browser. A URL that is not http(s) is refused.
 */
export async function openUrl(url: string, choice: BrowserChoice | null, d: BrowserDeps): Promise<OpenOutcome> {
  if (!external(url)) throw new Error('refused: only an http(s) link opens in a browser')
  if (!choice) {
    await d.openExternal(url)
    return 'default'
  }
  const program = findBrowser(choice.name, d)
  if (!program) {
    await d.openExternal(url)
    return 'missing'
  }
  const { file, args } = browserArgv(program, choice, url, d.platform)
  if (await start(d, file, args, d.platform === 'darwin')) return 'browser'
  await d.openExternal(url)
  return 'failed'
}

/** systemBrowserDeps are the computer's own: its OS, environment and files, and child_process's spawn. */
export function systemBrowserDeps(openExternal: (url: string) => Promise<void>): BrowserDeps {
  return {
    platform: process.platform,
    env: process.env,
    home: homedir(),
    isFile: (p) => {
      try {
        return statSync(p).isFile()
      } catch {
        return false
      }
    },
    isDir: (p) => {
      try {
        return statSync(p).isDirectory()
      } catch {
        return false
      }
    },
    realpath: (p) => {
      try {
        return realpathSync(p)
      } catch {
        return p
      }
    },
    spawn: (file, args, options) => spawn(file, args, options),
    openExternal,
  }
}
