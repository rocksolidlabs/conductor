import type { AgentBrowser } from '~/utils/protocol'

/** The desktop app's settings as its shell keeps them (desktop/src/settings.ts). */
export interface DesktopSettings {
  dataDir: string
  allowedRoots: string[]
  defaultCwd: string
  yolo: boolean
  reach: 'auto' | 'manual' | 'off'
  closeToTray: boolean
  wslDistro: string
  wslWindowsHome: boolean
  switchyardEnabled: boolean
  switchyardServer: string
  switchyardToken: string
  switchyardName: string
  /** The one-time notices already shown. */
  noticed?: string[]
}

/** The words for a switchyard that is plain http on another machine: the desktop app's validate (desktop/src/settings.ts PLAIN_SWITCHYARD) says the same. */
export const PLAIN_SWITCHYARD = 'the switchyard must be https:// unless it runs on this machine (localhost): over plain http:// what is published there travels in the clear and unverified'

/** The words for a switchyard named with more than its address: the desktop app's validate (desktop/src/settings.ts SWITCHYARD_EXTRA) says the same. */
export const SWITCHYARD_EXTRA = 'the switchyard must be named by its address alone, with no user name, password, query or fragment; a host token has a field of its own'

/** The words for a switchyard written as the server does not read a URL: the desktop app's validate (desktop/src/settings.ts SWITCHYARD_SHAPE) says the same. */
export const SWITCHYARD_SHAPE = 'the switchyard must be an http(s) URL with a host, written as the server reads it: no space around it, no backslash, and a % only in an escape'

/**
 * switchyardProblem is what the Settings page says, as it opens and before it saves, of a switchyard that the app and its server refuse:
 * one named with more than its address (SWITCHYARD_EXTRA), written as the server does not read a URL (SWITCHYARD_SHAPE), or plain http
 * on another machine (PLAIN_SWITCHYARD); '' for one it may save, and while publishing is off. The app's switchyardRefusal
 * (desktop/src/settings.ts) is the same check, and internal/config/testdata/switchyard-urls.json holds both to the server's. This machine
 * is localhost, in lower case, by that name, an address in 127.0.0.0/8 written as four decimal numbers, or ::1 (or 127 mapped into
 * IPv6), as the server reads it (config.PlainOffMachine); a short form of an address the browser reads as 127.0.0.1 is not.
 */
export function switchyardProblem(s: Pick<DesktopSettings, 'switchyardEnabled' | 'switchyardServer'>): string {
  if (!s.switchyardEnabled || !s.switchyardServer) return ''
  if (switchyardExtra(s.switchyardServer)) return SWITCHYARD_EXTRA
  if (!switchyardShape(s.switchyardServer)) return SWITCHYARD_SHAPE
  const u = new URL(s.switchyardServer)
  return u.protocol === 'http:' && !thisMachine(s.switchyardServer, u) ? PLAIN_SWITCHYARD : ''
}

/** switchyardShape is desktop/src/settings.ts's: a URL written so that the server reads it, and as the URL parser does. */
function switchyardShape(url: string): boolean {
  if (url !== url.trim() || !/^https?:\/\/(?:[\w.~:[\]@\u0080-\uffff-]|%[89a-f][0-9a-f])+(?:\/(?:[^\x00-\x1f\x7f\\%?#]|%[0-9a-f]{2})*)?$/i.test(url)) return false
  try {
    const p = new URL(url)
    return (p.protocol === 'https:' || p.protocol === 'http:') && !!p.host
  } catch {
    return false
  }
}

/**
 * switchyardExtra says whether url carries more than a scheme, a host and a path: user information, a query or a fragment (a ? or a #,
 * an empty one included). User information is an @ in the authority as written, an @ alone included, which the URL parser reads as no
 * user name and no password, or a user name or password the parser finds where it repairs what is written (an extra slash).
 * desktop/src/settings.ts's is the same.
 */
function switchyardExtra(url: string): boolean {
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

/** The words of a one-time notice the desktop app hands the workbench; null for one this build does not know. */
export function noticeWords(notice: string): { title: string; description: string } | null {
  if (notice === 'publishing') {
    return {
      title: 'Sessions are published to a switchyard',
      description:
        'Each session you start is listed at switchyard.rslabs.net, or the switchyard Settings names, so Share makes a link that works from anywhere. The switchyard mints and checks those links: publish only to one whose operator you trust. Turn it off under Settings → Switchyard.',
    }
  }
  return null
}

export interface DesktopServerState {
  state: 'stopped' | 'starting' | 'running' | 'failed'
  url: string
  pid?: number
  version?: string
  failures: number
  lastError?: string
}

/** The bridge the desktop shell's preload exposes as window.conductorDesktop (desktop/src/preload.ts). */
export interface DesktopIceStatus {
  forwarding: boolean
  port: number
  publicIp: string
  wslAddress: string
  firewall: 'present' | 'missing' | 'unknown'
  reason?: string
}

/**
 * The workbench's zoom as the desktop app tells it (desktop/src/zoom.ts ZoomStatus): the level, the percent it reads, which the app
 * computes (zoomPercent) and the page only shows, and whether it is the largest or the smallest the app allows ('' between).
 */
export interface DesktopZoom {
  level: number
  percent: number
  bound: 'largest' | 'smallest' | ''
}

/** asDesktopZoom is what the app sent, read as a zoom status; null for anything else, which the page shows nothing for. */
export function asDesktopZoom(v: unknown): DesktopZoom | null {
  if (!v || typeof v !== 'object') return null
  const { level, percent, bound } = v as Record<string, unknown>
  if (typeof level !== 'number' || !Number.isFinite(level) || typeof percent !== 'number' || !Number.isInteger(percent) || percent <= 0) return null
  return { level, percent, bound: bound === 'largest' || bound === 'smallest' ? bound : '' }
}

/** zoomWords is what the zoom indicator says of a level: its percent, and at a bound that it is the largest or the smallest. */
export function zoomWords(z: DesktopZoom): string {
  return `${z.percent}%${z.bound ? `, the ${z.bound}` : ''}`
}

export interface DesktopBridge {
  version: string
  platform: string
  token(): Promise<string>
  openExternal(url: string): Promise<void>
  settings: {
    get(): Promise<DesktopSettings>
    set(patch: Partial<DesktopSettings>): Promise<DesktopSettings>
    /**
     * What became of the last save ('saved', or 'restarted' when the app restarted the server for it), handed out once; '' when
     * none is owed. A restarted server listens on a fresh port and the app loads the page there: the page that follows asks. Older
     * apps lack it.
     */
    outcome?(): Promise<string>
  }
  /**
   * Opens an agent's link in the browser its settings name (#436), on this computer, one tab; none named, or one not installed here,
   * in the default browser. What became of it: 'browser', 'default', 'missing' (not installed) or 'failed' (did not start). Older
   * apps lack it.
   */
  openUrl?(url: string, browser?: AgentBrowser | null): Promise<{ outcome: string }>
  /** The browsers of the five an agent may name that are installed on this computer, for the Agents page to mark. Older apps lack it. */
  browsers?(): Promise<string[]>
  restartServer(): Promise<void>
  openInBrowser(): Promise<void>
  showLog(): Promise<void>
  serverState(): Promise<DesktopServerState>
  /** The one-time notice the app owes ('publishing'), handed out once; '' when none. Older apps lack it. */
  notice?(): Promise<string>
  /** Windows: what the app forwards for WebRTC from WSL. */
  ice(): Promise<DesktopIceStatus>
  /** Windows: adds the firewall rule for the ICE port (one elevation prompt); the rule's state after. */
  allowIceFirewall(): Promise<DesktopIceStatus['firewall']>
  versions(): Promise<{ app: string; electron: string; node: string; chrome: string; server: string }>
  onServerState(cb: (state: DesktopServerState) => void): () => void
  /** An invite the app was handed while this page is open (desktop/src/invite.ts InviteDelivery); cb says whether the page routed it. Older apps lack it. */
  onInvite?(cb: (invite: { server: string; token: string }) => boolean | Promise<boolean>): () => void
  /**
   * The update the app downloaded ('' for none) and whether to say so, which the first page to ask after the download is told,
   * once per version; the version the tray's Restart to update asked for, which the page asks in its dialog (once, '' for none);
   * and one whose install did not take ('' for none), said once (#290). Older apps lack it.
   */
  update?(): Promise<{ ready: string; announce: boolean; restart?: string; failed: string }>
  /** A check found an update downloaded, the one downloaded is gone, or the tray asked for the restart: ask update() what to say. Older apps lack it. */
  onUpdate?(cb: () => void): () => void
  /**
   * Installs the update downloaded now, the version offered: the app quits, its server and the sessions running on it with it
   * ('started'). 'gone' when that version is no longer ready (a check found a newer one, or a download failed), 'busy' while a
   * check for updates is under way; nothing stops then. Older apps lack it.
   */
  installUpdate?(version: string): Promise<'started' | 'gone' | 'busy'>
  /**
   * The workbench's zoom now, and whether this page is the one to say it: a level other than 100% kept from before, to the first
   * page that asks after the app starts (once per start, not per page load). Older apps lack it.
   */
  zoom?(): Promise<DesktopZoom & { announce: boolean }>
  /** Each zoom move by key or menu, one at a bound that changed nothing included: the zoom after it, to show. Older apps lack it. */
  onZoom?(cb: (zoom: DesktopZoom) => void): () => void
}

/** What the workbench says of an update the desktop app downloaded, once per version (#290). */
export function updateReadyWords(version: string): { title: string; description: string } {
  return { title: `Conductor ${version} is ready`, description: 'It installs when you quit Conductor. Restart to update now, or later.' }
}

/**
 * What the workbench says of an update offered that the app's updater no longer holds: a check found a newer one, whose download
 * replaced it, or a download failed. Nothing restarted.
 */
export function updateGoneWords(version: string): { title: string; description: string } {
  return {
    title: `Conductor ${version} is no longer ready`,
    description: 'A newer update replaced it, or its download failed, and nothing restarted. The button in the header shows when an update is ready.',
  }
}

/** What the workbench says of a restart to update put off while the app checks for updates, which may change the one held. */
export function updateBusyWords(): { title: string; description: string } {
  return { title: 'Conductor is checking for updates', description: 'Nothing restarted. Restart to update again in a moment.' }
}

/** What the workbench says of an update the app downloaded and this start of it runs older than: its install did not take. */
export function updateFailedWords(version: string): { title: string; description: string } {
  return {
    title: `Conductor ${version} did not install`,
    description: "It was downloaded and Conductor started again as the version before. The app's log says why; the update is offered again.",
  }
}

/**
 * What restarting to update ends: the app quits to install version, and the server with it, ending the sessions running on it;
 * sessions is how many, or negative when they could not be counted, which never reads as none.
 */
export function restartWords(version: string, sessions: number): string {
  const ends =
    sessions < 0
      ? 'The sessions running on this computer, if any, end: they could not be counted.'
      : sessions === 0
        ? 'No session runs on this computer, so none ends.'
        : sessions === 1
          ? 'The session running on this computer ends.'
          : `The ${sessions} sessions running on this computer end.`
  return `Conductor quits to install ${version}, and its server stops. ${ends}`
}

/** desktopBridge is the bridge when the workbench runs in the desktop app, else null. */
export function desktopBridge(): DesktopBridge | null {
  if (typeof window === 'undefined') return null
  const b = (window as unknown as { conductorDesktop?: DesktopBridge }).conductorDesktop
  return b && typeof b.token === 'function' ? b : null
}

/** tokenFromFragment reads a workbench token the desktop app put in the URL fragment ("Open in browser"): `#token=…`, '' when none. */
export function tokenFromFragment(hash: string): string {
  const h = hash.startsWith('#') ? hash.slice(1) : hash
  if (!h) return ''
  const params = new URLSearchParams(h)
  const t = params.get('token') ?? ''
  return /^[A-Za-z0-9_-]{16,256}$/.test(t) ? t : ''
}

/** withoutTokenFragment is the URL with the token taken out of its fragment (the rest of the fragment kept). */
export function withoutTokenFragment(href: string): string {
  const u = new URL(href)
  const h = u.hash.startsWith('#') ? u.hash.slice(1) : u.hash
  const params = new URLSearchParams(h)
  params.delete('token')
  const rest = params.toString()
  u.hash = rest ? '#' + rest : ''
  return u.toString()
}

/**
 * A plain copy of a form for the desktop bridge: Vue's reactive proxies cannot be
 * cloned across contextBridge or ipcRenderer ("An object could not be cloned",
 * #348), so what crosses is the JSON value, arrays and nested objects included.
 */
export function plainCopy<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T
}
