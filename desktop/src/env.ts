import { switchyardRefusal, type DesktopSettings } from './settings'

/**
 * serverEnv is the environment the server runs with: the app's own minus any CONDUCTOR_* that was set, the login shell's PATH, and
 * the CONDUCTOR_* values the settings choose. The workbench token and the run's key are in it nowhere: the app hands them over on the
 * server's stdin (secretsLine, serve --secrets-stdin), so neither is in what the system shows of the server's starting environment
 * or its command line. appDir is the app's
 * own directory (userData: its settings, which hold the switchyard token, its logs and its browser data), which the server's sessions
 * must not read: CONDUCTOR_DESKTOP_DIR names it, a path on this machine (inside WSL, linuxEnv gives the distribution's path to it).
 */
export function serverEnv(base: NodeJS.ProcessEnv, settings: DesktopSettings, path: string, appDir: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(base)) {
    if (v === undefined || k.startsWith('CONDUCTOR_')) continue
    out[k] = v
  }
  if (path) out.PATH = path
  out.CONDUCTOR_DATA_DIR = settings.dataDir
  if (appDir) out.CONDUCTOR_DESKTOP_DIR = appDir
  out.CONDUCTOR_ALLOWED_ROOTS = settings.allowedRoots.join(',')
  // The press's folders, apart: the server judges each again as it starts.
  if (settings.pressedRoots?.length) out.CONDUCTOR_PRESSED_ROOTS = JSON.stringify(settings.pressedRoots.map((p) => (p.confirmed ? p : { path: p.path })))
  out.CONDUCTOR_DEFAULT_CWD = settings.defaultCwd
  out.CONDUCTOR_YOLO = settings.yolo ? '1' : '0'
  out.CONDUCTOR_REACH = settings.reach
  // The app's server is the person's own machine: its Settings picker browses
  // every folder there (on Windows, the distribution's), not only the roots.
  out.CONDUCTOR_PATHS_BROWSE = 'any'
  // Publishing is the server's default; off is a choice. The server's own
  // default switchyard stands when none is named, and a token is optional.
  // A switchyard the server refuses to start with, plain http on another
  // machine, named with more than its address or written as it does not read
  // a URL (settings saved before the app refused it too), is never handed to
  // it, publishing on or off: the server starts without publishing, and the
  // Settings page says why.
  const s = settings.switchyardServer
  const refused = !!s && switchyardRefusal(s) !== ''
  out.CONDUCTOR_RENDEZVOUS = settings.switchyardEnabled && !refused ? '1' : '0'
  if (s && !refused) out.CONDUCTOR_RENDEZVOUS_SERVER = s.replace(/\/+$/, '')
  if (settings.switchyardToken) out.CONDUCTOR_RENDEZVOUS_TOKEN = settings.switchyardToken
  if (settings.switchyardName) out.CONDUCTOR_RENDEZVOUS_HOST_NAME = settings.switchyardName
  if (!out.HOME && base.HOME) out.HOME = base.HOME
  return out
}

/** The server's arguments: a free loopback port, the handshake, the exit with the app, and its secrets on stdin. */
export const SERVE_ARGS = ['serve', '--listen', '127.0.0.1:0', '--print-listen', '--exit-on-stdin-close', '--secrets-stdin']

/**
 * secretsLine is the first line the app writes on its server's stdin (serve --secrets-stdin): the workbench token minted for this
 * run, and the run's key where the app offers Allow this folder ('' for none: the server then allows no folder while it runs).
 */
export function secretsLine(token: string, key: string): string {
  return JSON.stringify(key ? { workbenchToken: token, desktopKey: key } : { workbenchToken: token }) + '\n'
}
