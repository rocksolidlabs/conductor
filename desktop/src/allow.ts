import { loopbackOrigin } from './server'
import { validate, type DesktopSettings } from './settings'

/**
 * Allow this folder (the Launch agent dialog's one press): the app asks the server it started to let agents use one more folder
 * (POST /api/roots, with the workbench token and the key the app handed that server on its stdin for this run), then keeps
 * the folder in its settings file, with no restart: running sessions go on. Only the workbench's own main frame may ask (ipc.ts),
 * exactly who may save Settings.
 */

/** At most this many folders are kept: the list travels to the server as one environment variable. */
export const MAX_ROOTS = 64
/** The saved list, joined with commas, is at most this many characters. */
export const MAX_ROOTS_CHARS = 8192
/** A folder's path is at most this many characters. */
export const MAX_ROOT_PATH = 1024
/** How long the app waits for its server's answer. */
export const ALLOW_WAIT_MS = 10_000

/**
 * ALLOW_ON_WINDOWS is the switch for the app on Windows, where the server runs inside WSL: off until one press has been seen to
 * work in the Windows app (the key's crossing into WSL and the request into it have unit tests only). Off, the preload offers no
 * allowFolder there (its own copy of this switch: a sandboxed preload imports nothing) and the IPC refuses the call.
 */
export const ALLOW_ON_WINDOWS = false

/** allowOffered says whether the app on platform offers the press. */
export function allowOffered(platform: string): boolean {
  return platform !== 'win32' || ALLOW_ON_WINDOWS
}

/**
 * keyFor is the run's key on platform: minted where the app offers the press, '' where it does not (Windows while its switch is
 * off), so that a server there holds no key, refuses every request to allow a folder and tells the page its folders are the
 * configuration's (whoami folders config).
 */
export function keyFor(platform: string, mint: () => string): string {
  return allowOffered(platform) ? mint() : ''
}

/** The kinds of wide folder a press may confirm ('' for none). */
const CONFIRMED = ['', 'home', 'above_home', 'holds_data'] as const
export type Confirmed = (typeof CONFIRMED)[number]

/**
 * What the press answers the page. No answer carries the workbench token or the key. saved true with an error: the settings keep
 * the folder, but Conductor restarted meanwhile and the server running now has not taken it (the error says to restart it).
 */
export type AllowAnswer =
  | { ok: true; root: string; saved: true; error?: string }
  | { ok: true; root: string; saved: false; error: string }
  | { ok: false; code: string; message: string; real?: string; wide?: string; reason?: string }

export interface AllowDeps {
  get: () => DesktopSettings
  /** Saves the settings file; throws when it cannot be written. */
  set: (s: DesktopSettings) => void
  /** The server's origin now (the address it bound, '' while none runs) and its process id (the supervisor's handshake). */
  server: () => { url: string; pid?: number; running: boolean }
  token: () => string
  key: () => string
  fetch: (url: string, init: { method: string; headers: Record<string, string>; body: string; redirect: 'error'; signal: AbortSignal }) => Promise<{ status: number; json: () => Promise<unknown> }>
  /** Windows: the settings are the WSL distribution's paths. */
  posix: boolean
  /** Tells every workbench window the settings changed (conductor:settings). */
  told: () => void
  /** The wait for the server's answer; ALLOW_WAIT_MS when not given. */
  waitMs?: number
  /**
   * Resolves with the server once it runs (at once when it does), or null once ms have gone by without one: a server restarted
   * while the press was under way is waited for and asked. Without it, the server as it is now.
   */
  whenRunning?: (ms: number) => Promise<{ url: string; pid?: number; running: boolean } | null>
}

/**
 * pathRefusal is why path cannot be asked for before any request ('' for none): a string, at most 1,024 characters, absolute in
 * the posix form, written clean (no empty, . or .. element, no trailing slash), with no comma, control or formatting character, line
 * or paragraph separator, and no space at either end: the saved list is one comma-joined variable whose parts are trimmed.
 */
export function pathRefusal(path: unknown): string {
  if (typeof path !== 'string' || path.length === 0) return 'the folder must be a path'
  if (path.length > MAX_ROOT_PATH) return `the folder's path is longer than ${MAX_ROOT_PATH} characters`
  if (!path.startsWith('/')) return 'the folder must be an absolute path'
  if (path !== '/' && (path.endsWith('/') || path.split('/').slice(1).some((p) => p === '' || p === '.' || p === '..'))) return "the folder's path must be written clean"
  if (/[,\p{Cc}\p{Cf}\p{Zl}\p{Zp}]/u.test(path) || /^\s|\s$/u.test(path)) return "the folder's name holds a comma, a hidden character or a space at an end"
  return ''
}

/** listRefusal is why the saved list cannot take root ('' for none): past 64 folders, or 8,192 characters joined. */
export function listRefusal(roots: string[], root: string): string {
  if (roots.includes(root)) return ''
  if (roots.length + 1 > MAX_ROOTS) return `agents may use at most ${MAX_ROOTS} folders`
  if ([...roots, root].join(',').length > MAX_ROOTS_CHARS) return `the folders agents may use are at most ${MAX_ROOTS_CHARS} characters together`
  return ''
}

/** covered says whether a saved root is root or holds it. */
export function covered(roots: string[], root: string): boolean {
  return roots.some((r) => r === root || root.startsWith(r.replace(/\/+$/, '') + '/'))
}

/**
 * FolderAllow runs the presses one at a time, and never beside a Settings save: a press while a save or a restart is under way
 * is refused as busy, and a save waits for a press under way (save).
 */
export class FolderAllow {
  private chain: Promise<unknown> = Promise.resolve()
  private saves = 0

  constructor(private d: AllowDeps) {}

  /** save runs a Settings save once the press under way has ended; a press meanwhile is busy. */
  async save<T>(f: () => Promise<T>): Promise<T> {
    this.saves++
    try {
      await this.chain
      return await f()
    } finally {
      this.saves--
    }
  }

  /** allow is the press: path and confirmed as the page sent them, checked here before anything is asked. */
  allow(path: unknown, confirmed: unknown): Promise<AllowAnswer> {
    const run = this.chain.then(() => this.step(path, confirmed))
    this.chain = run.catch(() => {})
    return run.then(
      (a) => this.scrub(a),
      (e) => this.scrub({ ok: false, code: 'failed', message: (e as Error)?.message ?? 'the folder was not allowed' }),
    )
  }

  private async step(path: unknown, confirmed: unknown): Promise<AllowAnswer> {
    const why = pathRefusal(path)
    if (why) return { ok: false, code: 'invalid_request', message: why }
    if (typeof confirmed !== 'string' || !(CONFIRMED as readonly string[]).includes(confirmed)) return { ok: false, code: 'invalid_request', message: 'confirmed must be home, above_home or holds_data' }
    const p = path as string
    if (this.saves > 0) return { ok: false, code: 'busy', message: 'the settings are being saved: try again' }
    const full = listRefusal(this.d.get().allowedRoots, p)
    if (full) return { ok: false, code: 'too_many_roots', message: full }
    const first = this.d.server()
    if (!first.running || !first.url || first.pid === undefined) return { ok: false, code: 'busy', message: 'Conductor is restarting: try again' }
    const asked = await this.ask(first, p, confirmed)
    if (!asked.ok) return asked
    const root = asked.root
    // A folder already within the saved list needs nothing saved.
    const cur = this.d.get()
    if (covered(cur.allowedRoots, root)) return { ok: true, root, saved: true }
    const tooMany = listRefusal(cur.allowedRoots, root)
    if (tooMany) return { ok: true, root, saved: false, error: tooMany }
    const next = { ...cur, allowedRoots: [...cur.allowedRoots, root] }
    const problems = validate(next, this.d.posix)
    if (problems.length) return { ok: true, root, saved: false, error: problems.join('; ') }
    try {
      this.d.set(next)
    } catch (e) {
      return { ok: true, root, saved: false, error: (e as Error)?.message ?? 'the settings file could not be written' }
    }
    this.d.told()
    // The server that took it is gone (a restart, a crash and the supervisor's start again): the one running now, or once it runs,
    // was started from settings read before the save or after it. It is asked once more, which allows it there or says it holds
    // it already; when no server runs in time, or the one running does not take it, the answer says so.
    const now = this.d.server()
    if (now.running && now.pid === first.pid && now.url === first.url) return { ok: true, root, saved: true }
    const wait = this.d.waitMs ?? ALLOW_WAIT_MS
    const later = this.d.whenRunning ? await this.d.whenRunning(wait) : now.running ? now : null
    if (!later || !later.running || !later.url || later.pid === undefined) {
      return { ok: true, root, saved: true, error: 'Conductor restarted meanwhile and is not running yet: if agents cannot use the folder once it runs, restart Conductor' }
    }
    const again = await this.ask(later, p, confirmed)
    if (!again.ok) return { ok: true, root, saved: true, error: `Conductor restarted meanwhile and did not take the folder (${again.message}): restart Conductor to use it` }
    return { ok: true, root, saved: true }
  }

  /** ask posts the folder to the server s, its handshake's, following no redirect; its answer, or why there is none. */
  private async ask(s: { url: string; pid?: number }, path: string, confirmed: string): Promise<{ ok: true; root: string } | Extract<AllowAnswer, { ok: false }>> {
    // The token and the key go to a loopback address written as one, the socket the server bound, and to nothing else.
    if (!s.url || loopbackOrigin(s.url.replace(/^http:\/\//, '')) !== s.url) return { ok: false, code: 'busy', message: 'Conductor is restarting: try again' }
    let res: { status: number; json: () => Promise<unknown> }
    try {
      res = await this.d.fetch(`${s.url.replace(/\/+$/, '')}/api/roots`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${this.d.token()}`, 'X-Conductor-Desktop-Key': this.d.key(), 'Content-Type': 'application/json' },
        body: JSON.stringify({ path, confirmed: confirmed || undefined }),
        redirect: 'error',
        signal: AbortSignal.timeout(this.d.waitMs ?? ALLOW_WAIT_MS),
      })
    } catch {
      return { ok: false, code: 'no_answer', message: 'Conductor did not answer' }
    }
    let body: unknown
    try {
      body = await res.json()
    } catch {
      body = null
    }
    const o = (body && typeof body === 'object' ? body : {}) as Record<string, unknown>
    if (res.status === 200) {
      if (typeof o.root !== 'string' || pathRefusal(o.root)) return { ok: false, code: 'failed', message: 'Conductor gave an answer the app cannot read' }
      return { ok: true, root: o.root }
    }
    if (res.status === 404 || res.status === 405) return { ok: false, code: 'not_supported', message: 'this Conductor cannot allow a folder while it runs' }
    const e = (o.error && typeof o.error === 'object' ? o.error : {}) as Record<string, unknown>
    const out: Extract<AllowAnswer, { ok: false }> = {
      ok: false,
      code: typeof e.code === 'string' ? e.code : `http_${res.status}`,
      message: typeof e.message === 'string' ? e.message : `Conductor answered ${res.status}`,
    }
    for (const k of ['real', 'wide', 'reason'] as const) if (typeof e[k] === 'string') out[k] = e[k] as string
    return out
  }

  /** scrub takes the token and the key out of every string of an answer, should anything have echoed them. */
  private scrub(a: AllowAnswer): AllowAnswer {
    const secrets = [this.d.token(), this.d.key()].filter((s) => s.length >= 8)
    const clean = (s: string) => secrets.reduce((acc, x) => acc.split(x).join('(hidden)'), s)
    const out = { ...a } as Record<string, unknown>
    for (const [k, v] of Object.entries(out)) if (typeof v === 'string') out[k] = clean(v)
    return out as AllowAnswer
  }
}
