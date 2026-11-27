import { mkdirSync, readFileSync, renameSync, writeFileSync } from 'node:fs'
import { dirname, isAbsolute, join } from 'node:path'
import { clampZoom } from './zoom'

/** The desktop app's settings (userData/settings.json): what it starts the server with. */
export interface DesktopSettings {
  /** The server's data directory (catalog, crews, hooks, certificates). */
  dataDir: string
  /** Directories server sessions may run in, as typed in Settings. */
  allowedRoots: string[]
  /**
   * The folders Allow this folder added, kept apart from the typed ones: the server judges each again at every start as the press
   * did, and uses one only while it is that folder. Settings shows the two lists as one (shownSettings).
   */
  pressedRoots: PressedRoot[]
  /** The working directory a launch gets without one. */
  defaultCwd: string
  /** Launch every agent with its yolo recipe. */
  yolo: boolean
  /** The server's reach mode: auto maps the TLS port on the router once there is one. */
  reach: 'auto' | 'manual' | 'off'
  /** Closing the window keeps the app in the tray; the server goes on. */
  closeToTray: boolean
  /** Windows: the WSL distribution the server runs in; '' for the default. */
  wslDistro: string
  /** Windows: also allow /mnt/c/Users/<user> (slow, but where Windows projects live). */
  wslWindowsHome: boolean
  /** Publish every session to a switchyard, so a share link works from anywhere. On by default. */
  switchyardEnabled: boolean
  /** The switchyard every session is published to (`https://host`); '' for the public one, switchyard.rslabs.net. */
  switchyardServer: string
  /** Optional: one of the switchyard's host tokens (a private switchyard, or a trusted seat on the public one). */
  switchyardToken: string
  /** How this machine is named there; '' for its host name. */
  switchyardName: string
  /** The one-time notices already shown, by name. */
  noticed: string[]
  /** The workbench's zoom level (0 is 100%), kept by the app as Ctrl+= and Ctrl+- move it; never set from the Settings page. */
  zoomLevel: number
  /** The update the workbench was told is ready (#290), '' for none: told once per version, whatever checks find it again. The app's own. */
  updateAnnounced: string
  /** The update downloaded last, told or not, '' for none: a start older than it did not install it (#290). The app's own. */
  updateDownloaded: string
  /** The update whose install was said not to have taken, '' for none: said once per version. The app's own. */
  updateFailedNoticed: string
}

/** A folder Allow this folder added, and the kind of wide folder it was confirmed as ('' for none). */
export interface PressedRoot {
  path: string
  confirmed: '' | 'home' | 'above_home' | 'holds_data'
}

const PRESSED_KINDS = ['', 'home', 'above_home', 'holds_data'] as const

/** under says whether path is root or lies inside it. */
function under(path: string, root: string): boolean {
  return path === root || path.startsWith(root.replace(/\/+$/, '') + '/')
}

/**
 * typedDefault makes the folder the default folder lies in a typed one when only a pressed folder holds it: choosing a default
 * folder in Settings is typing its folder there. The pressed entry leaves pressedRoots for allowedRoots, so the default folder
 * always lies under a typed folder, as validate asks.
 */
export function typedDefault<T extends Pick<DesktopSettings, 'allowedRoots' | 'pressedRoots' | 'defaultCwd'>>(s: T): T {
  if (!s.defaultCwd || (s.allowedRoots ?? []).some((r) => under(s.defaultCwd, r))) return s
  const holder = (s.pressedRoots ?? []).find((p) => under(s.defaultCwd, p.path))
  if (!holder) return s
  return { ...s, allowedRoots: [...(s.allowedRoots ?? []), holder.path], pressedRoots: (s.pressedRoots ?? []).filter((p) => p !== holder) }
}

/** rootPaths are every folder agents may use, as a person sees them: the typed ones, then the pressed ones not among them. */
export function rootPaths(s: Pick<DesktopSettings, 'allowedRoots' | 'pressedRoots'>): string[] {
  const out = [...(s.allowedRoots ?? [])]
  for (const p of s.pressedRoots ?? []) if (!out.includes(p.path)) out.push(p.path)
  return out
}

/**
 * shownSettings are the settings as the Settings page has them: one list of folders (rootPaths), the press's apart from the typed
 * ones being the app's own business.
 */
export function shownSettings(s: DesktopSettings): Omit<DesktopSettings, 'pressedRoots'> {
  const { pressedRoots: _pressed, ...rest } = s
  return { ...rest, allowedRoots: rootPaths(s) }
}

/**
 * splitRoots takes the one list the Settings page sends back (allowedRoots) apart again: a folder the press added stays the press's
 * while the list holds it, and is dropped when the page drops it; every other folder of the list is a typed one. Without a list,
 * both stay as they are.
 */
export function splitRoots(cur: Pick<DesktopSettings, 'allowedRoots' | 'pressedRoots'>, shown: unknown): Pick<DesktopSettings, 'allowedRoots' | 'pressedRoots'> {
  if (!Array.isArray(shown)) return { allowedRoots: cur.allowedRoots, pressedRoots: cur.pressedRoots }
  const list = shown.filter((r): r is string => typeof r === 'string')
  const pressedRoots = (cur.pressedRoots ?? []).filter((p) => list.includes(p.path))
  return { allowedRoots: list.filter((r) => !pressedRoots.some((p) => p.path === r)), pressedRoots }
}

/** The notices the app owes once: publishing, that sessions are published to a switchyard (on by default), to a fresh install and an upgrade alike. */
export const NOTICES = ['publishing'] as const
export type Notice = (typeof NOTICES)[number]

export const SERVER_SETTINGS: ReadonlyArray<keyof DesktopSettings> = ['dataDir', 'allowedRoots', 'pressedRoots', 'defaultCwd', 'yolo', 'reach', 'wslDistro', 'wslWindowsHome', 'switchyardEnabled', 'switchyardServer', 'switchyardToken', 'switchyardName']

export function defaultSettings(home: string, userData: string): DesktopSettings {
  return { dataDir: join(userData, 'conductor'), allowedRoots: [home], pressedRoots: [], defaultCwd: home, yolo: false, reach: 'auto', closeToTray: true, wslDistro: '', wslWindowsHome: false, switchyardEnabled: true, switchyardServer: '', switchyardToken: '', switchyardName: '', noticed: [], zoomLevel: 0, updateAnnounced: '', updateDownloaded: '', updateFailedNoticed: '' }
}

/** The words for a switchyard that is plain http on another machine; web/app/utils/desktop.ts's check, which the Settings page runs first, says the same. */
export const PLAIN_SWITCHYARD = 'the switchyard must be https:// unless it runs on this machine (localhost): over plain http:// what is published there travels in the clear and unverified'

/**
 * plainSwitchyard says whether url is a plain http:// URL to a host that is not this machine, as the server's own check
 * (config.PlainOffMachine) reads it, which refuses such a switchyard: localhost written in lower case, an address in 127.0.0.0/8 written as
 * four decimal numbers, or ::1 (or 127 mapped into IPv6) is this machine, and any other host is not, a name or a short form of an
 * address that a browser would read as 127.0.0.1 among them. A URL that does not parse, or is not http, is not one.
 */
export function plainSwitchyard(url: string): boolean {
  let u: URL
  try {
    u = new URL(url)
  } catch {
    return false
  }
  return u.protocol === 'http:' && !thisMachine(url, u)
}

function thisMachine(raw: string, u: URL): boolean {
  const host = u.hostname.toLowerCase()
  if (host.startsWith('[')) return host === '[::1]' || /^\[::ffff:7f[0-9a-f]{2}:[0-9a-f]{1,4}\]$/.test(host)
  // The host as written: the URL parser reads 127.1 or 0x7f.0.0.1 as 127.0.0.1, and the server does not; nor does it read a
  // backslash as the parser does.
  if (raw.includes('\\')) return false
  const authority = raw.trim().replace(/^[a-z][a-z0-9+.-]*:\/\//i, '').split(/[/?#]/, 1)[0] ?? ''
  const typed = authority.slice(authority.lastIndexOf('@') + 1).split(':', 1)[0]!
  if (typed.toLowerCase() !== host) return false
  // localhost written so, in lower case, as the server reads it: the one spelling of it that a proxy the environment names never takes.
  if (typed === 'localhost') return true
  const quad = /^127\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(typed)
  return !!quad && quad.slice(1).every((p) => (p === '0' || !p.startsWith('0')) && Number(p) <= 255)
}

/** The words for a switchyard named with more than its address; web/app/utils/desktop.ts's check, which the Settings page runs first, says the same. */
export const SWITCHYARD_EXTRA = 'the switchyard must be named by its address alone, with no user name, password, query or fragment; a host token has a field of its own'

/**
 * switchyardExtra says whether url carries more than a scheme, a host and a path: user information, a query or a fragment (a ? or a #,
 * an empty one included). User information is an @ in the authority as written, an @ alone included, which the URL parser reads as no
 * user name and no password, or a user name or password the parser finds where it repairs what is written (an extra slash). The server
 * refuses such a switchyard (config.ServerURLExtra): it is named by its address alone, and its host token is a setting of its own.
 */
export function switchyardExtra(url: string): boolean {
  if (/[?#]/.test(url)) return true
  const authority = url.trim().replace(/^[a-z][a-z0-9+.-]*:\/\//i, '').split(/[/?#]/, 1)[0] ?? ''
  if (authority.includes('@')) return true
  try {
    const u = new URL(url)
    return !!u.username || !!u.password
  } catch {
    return false
  }
}

/** The words for a switchyard written as the server does not read a URL; web/app/utils/desktop.ts's check says the same. */
export const SWITCHYARD_SHAPE = 'the switchyard must be an http(s) URL with a host, written as the server reads it: no space around it, no backslash, and a % only in an escape'

/**
 * switchyardShape says whether url is written so that the server (Go's url.Parse) reads it, and as the URL parser does: http:// or
 * https://, two slashes exactly, a host of letters, digits, dots, hyphens, underscores, tildes, an IPv6 address's brackets and colons,
 * a port's colon and digits, or letters past ASCII, written or escaped (Go takes an escape in a host only of a byte past ASCII; an @
 * and what comes before it are switchyardExtra's), then a path with no control character or backslash and a % only in an escape, and
 * no space around it all. The parser repairs what the server refuses (an extra
 * slash, a backslash for a slash, a space around it, a broken escape, a host escaped), so the server would not start with a switchyard
 * the app had saved; this holds the app to the narrower of the two. internal/config/testdata/switchyard-urls.json holds both to it.
 */
export function switchyardShape(url: string): boolean {
  if (url !== url.trim() || !/^https?:\/\/(?:[\w.~:[\]@\u0080-\uffff-]|%[89a-f][0-9a-f])+(?:\/(?:[^\x00-\x1f\x7f\\%?#]|%[0-9a-f]{2})*)?$/i.test(url)) return false
  try {
    const u = new URL(url)
    return (u.protocol === 'https:' || u.protocol === 'http:') && !!u.host
  } catch {
    return false
  }
}

/**
 * switchyardRefusal is why the server would refuse the switchyard url as a URL, in validate's words, '' for one it takes: named with
 * more than its address (SWITCHYARD_EXTRA), written as it does not read a URL (SWITCHYARD_SHAPE), or plain http on another machine
 * (PLAIN_SWITCHYARD). The app hands the server no switchyard it refuses so (serverEnv), publishing on or off. (One the server takes
 * for its own default address is a matter of its config: #297.)
 */
export function switchyardRefusal(url: string): string {
  if (switchyardExtra(url)) return SWITCHYARD_EXTRA
  if (!switchyardShape(url)) return SWITCHYARD_SHAPE
  if (plainSwitchyard(url)) return PLAIN_SWITCHYARD
  return ''
}

/**
 * refusedSwitchyardNote is the line the app logs as it starts with settings that publish to a switchyard the server would refuse (kept
 * as saved, never handed over: serverEnv), '' for none: validate's words, which name nothing of the URL. The URL parser's host is not
 * named either: where it reads a backslash as a slash, its host is not the one the server reads.
 */
export function refusedSwitchyardNote(s: Pick<DesktopSettings, 'switchyardEnabled' | 'switchyardServer'>): string {
  const why = s.switchyardEnabled && s.switchyardServer ? switchyardRefusal(s.switchyardServer) : ''
  return why ? `settings: ${why}; sessions are not published until Settings names a switchyard the server takes` : ''
}

/** switchyardRefused says whether s publishes to a switchyard that is plain http on another machine, which the server refuses to start with. */
export function switchyardRefused(s: Pick<DesktopSettings, 'switchyardEnabled' | 'switchyardServer'>): boolean {
  return !!s.switchyardEnabled && !!s.switchyardServer && plainSwitchyard(s.switchyardServer)
}

/**
 * validate returns the problems with s, in words; none for good settings. With posix the paths must be the WSL distribution's
 * (absolute Linux paths), whatever the host: Windows, where the server runs inside WSL.
 */
export function validate(s: DesktopSettings, posix = false): string[] {
  const out: string[] = []
  const absolute = (p: string) => (posix ? p.startsWith('/') : isAbsolute(p))
  const kind = posix ? 'an absolute path inside the WSL distribution, such as /home/<user>/code' : 'an absolute path'
  if (!s.dataDir || !absolute(s.dataDir)) out.push(`the data directory must be ${kind}`)
  if (!Array.isArray(s.allowedRoots) || s.allowedRoots.length === 0) out.push('at least one allowed root is needed')
  for (const r of s.allowedRoots ?? []) if (!r || !absolute(r)) out.push(`allowed root ${JSON.stringify(r)} must be ${kind}`)
  for (const p of s.pressedRoots ?? []) if (!p || typeof p.path !== 'string' || !absolute(p.path)) out.push(`allowed root ${JSON.stringify(p?.path)} must be ${kind}`)
  if (!s.defaultCwd || !absolute(s.defaultCwd)) out.push(`the default working directory must be ${kind}`)
  // Under a typed folder, as the release before reads it: a file written here always loads there.
  else if (s.allowedRoots?.length && !s.allowedRoots.some((r) => under(s.defaultCwd, r))) out.push('the default working directory must lie under an allowed root')
  if (!['auto', 'manual', 'off'].includes(s.reach)) out.push('reach must be auto, manual or off')
  // Off, nothing is published there: a switchyard the server would refuse is kept, and never handed to it (serverEnv).
  if (s.switchyardEnabled && s.switchyardServer) {
    const why = switchyardRefusal(s.switchyardServer)
    if (why) out.push(why)
  }
  if ((s.switchyardName ?? '').length > 64) out.push('the name at the switchyard is at most 64 characters')
  return out
}

/** wslDefaults are the defaults for a server inside a WSL distribution whose home is linuxHome. */
export function wslDefaults(s: DesktopSettings, linuxHome: string): DesktopSettings {
  return { ...s, dataDir: `${linuxHome}/.local/share/conductor/data`, allowedRoots: [linuxHome], defaultCwd: linuxHome }
}

/**
 * migrateToWsl moves settings saved with Windows paths (an earlier build kept them, and the launcher threw them away) to the
 * distribution's: a path that is not an absolute Linux path becomes the default inside the distribution. changed says so.
 */
export function migrateToWsl(s: DesktopSettings, linuxHome: string): { settings: DesktopSettings; changed: boolean } {
  const d = wslDefaults(s, linuxHome)
  const linux = (p: string) => typeof p === 'string' && p.startsWith('/')
  const roots = (s.allowedRoots ?? []).filter(linux)
  const next: DesktopSettings = {
    ...s,
    dataDir: linux(s.dataDir) ? s.dataDir : d.dataDir,
    allowedRoots: roots.length ? roots : d.allowedRoots,
    defaultCwd: linux(s.defaultCwd) ? s.defaultCwd : d.defaultCwd,
  }
  if (!next.allowedRoots.some((r) => next.defaultCwd === r || next.defaultCwd.startsWith(r.replace(/\/+$/, '') + '/'))) next.defaultCwd = next.allowedRoots[0]!
  return { settings: next, changed: JSON.stringify(next) !== JSON.stringify(s) }
}

/**
 * loadSettings reads the file, filling what it lacks from the defaults; a missing or broken file gives the defaults. Settings saved
 * with a switchyard the server would refuse (switchyardRefusal), before the app refused it too, keep it, and everything else: the
 * server is started without publishing for them (serverEnv), the Settings page shows why, and nothing is rewritten. The defaults
 * would publish to the public switchyard.
 */
export function loadSettings(file: string, defaults: DesktopSettings): DesktopSettings {
  try {
    const raw = JSON.parse(readFileSync(file, 'utf8')) as Partial<DesktopSettings>
    const merged: DesktopSettings = typedDefault({ ...defaults, ...pick(raw) })
    return validate(merged).some((p) => p !== PLAIN_SWITCHYARD && p !== SWITCHYARD_EXTRA && p !== SWITCHYARD_SHAPE) ? defaults : merged
  } catch {
    return defaults
  }
}

function pick(raw: Partial<DesktopSettings>): Partial<DesktopSettings> {
  const out: Partial<DesktopSettings> = {}
  if (typeof raw.dataDir === 'string') out.dataDir = raw.dataDir
  if (Array.isArray(raw.allowedRoots)) out.allowedRoots = raw.allowedRoots.filter((r): r is string => typeof r === 'string')
  if (Array.isArray(raw.pressedRoots)) {
    out.pressedRoots = raw.pressedRoots
      .filter((p): p is PressedRoot => !!p && typeof p === 'object' && typeof (p as PressedRoot).path === 'string')
      .map((p) => ({ path: p.path, confirmed: (PRESSED_KINDS as readonly string[]).includes(p.confirmed) ? p.confirmed : '' }))
  }
  if (typeof raw.defaultCwd === 'string') out.defaultCwd = raw.defaultCwd
  if (typeof raw.yolo === 'boolean') out.yolo = raw.yolo
  if (raw.reach === 'auto' || raw.reach === 'manual' || raw.reach === 'off') out.reach = raw.reach
  if (typeof raw.closeToTray === 'boolean') out.closeToTray = raw.closeToTray
  if (typeof raw.wslDistro === 'string') out.wslDistro = raw.wslDistro
  if (typeof raw.wslWindowsHome === 'boolean') out.wslWindowsHome = raw.wslWindowsHome
  if (typeof raw.switchyardEnabled === 'boolean') out.switchyardEnabled = raw.switchyardEnabled
  if (typeof raw.switchyardServer === 'string') out.switchyardServer = raw.switchyardServer
  if (typeof raw.switchyardToken === 'string') out.switchyardToken = raw.switchyardToken
  if (typeof raw.switchyardName === 'string') out.switchyardName = raw.switchyardName
  // A file from before the notices existed has no noticed list: every notice is owed to it.
  out.noticed = Array.isArray(raw.noticed) ? raw.noticed.filter((n): n is string => typeof n === 'string') : []
  out.zoomLevel = clampZoom(raw.zoomLevel)
  if (typeof raw.updateAnnounced === 'string') out.updateAnnounced = raw.updateAnnounced
  if (typeof raw.updateDownloaded === 'string') out.updateDownloaded = raw.updateDownloaded
  if (typeof raw.updateFailedNoticed === 'string') out.updateFailedNoticed = raw.updateFailedNoticed
  return out
}

/**
 * owedNotice is the notice to show once, and the settings with it marked shown: publishing, to settings that have not been told
 * yet while publishing is on (turned off, there is nothing to tell): a fresh install (defaultSettings marks nothing) and settings
 * from a file saved before the notice existed alike. The caller asks as it hands the notice to the workbench, and saves the
 * settings it returns, which keeps the notice marked.
 */
export function owedNotice(s: DesktopSettings): { notice: Notice | ''; settings: DesktopSettings } {
  if (s.noticed.includes('publishing')) return { notice: '', settings: s }
  const settings = { ...s, noticed: [...s.noticed, 'publishing'] }
  return { notice: s.switchyardEnabled ? 'publishing' : '', settings }
}

/**
 * noticeOnce is the bridge's notice handler: the first time the workbench asks, the notice owed to the settings get() gives
 * (owedNotice), marked through set() then; '' after, and when none is owed. Nothing is marked until the workbench asks, so a
 * start that never shows it (Windows without WSL yet, a server that does not start) still owes the notice.
 */
export function noticeOnce(get: () => DesktopSettings, set: (s: DesktopSettings) => void): () => Notice | '' {
  let taken = false
  return () => {
    if (taken) return ''
    taken = true
    const s = get()
    const owed = owedNotice(s)
    if (owed.settings !== s) set(owed.settings)
    return owed.notice
  }
}

/** saveSettings writes the file through a temporary one, mode 0600. */
export function saveSettings(file: string, s: DesktopSettings): void {
  mkdirSync(dirname(file), { recursive: true })
  const tmp = file + '.tmp'
  writeFileSync(tmp, JSON.stringify(s, null, 2) + '\n', { mode: 0o600 })
  renameSync(tmp, file)
}

/**
 * keepIn is the app's way to keep settings: written to file first, and only once the file holds them taken as the settings the app
 * runs with (assign). A file that cannot be written throws, and the settings the app runs with are left as they were.
 */
export function keepIn(file: string, assign: (s: DesktopSettings) => void): (s: DesktopSettings) => void {
  return (s) => {
    saveSettings(file, s)
    assign(s)
  }
}

/** serverAffecting says whether changing from a to b needs the server restarted. */
export function serverAffecting(a: DesktopSettings, b: DesktopSettings): boolean {
  return SERVER_SETTINGS.some((k) => JSON.stringify(a[k]) !== JSON.stringify(b[k]))
}
