import { MAX_SESSION_NAME, MAX_SUBMIT } from './protocol'

/**
 * Whether the launch dialog asks where the agent runs. There are two answers
 * only when the workbench is served from another machine: in the desktop
 * app, or on a workbench opened at this computer's own address, "this
 * server" and "this computer" are the same place, and the question is noise.
 */
export function showsRunsOn(isDesktop: boolean, hostname: string): boolean {
  if (isDesktop) return false
  return !isLoopbackHost(hostname)
}

/** A hostname that names the computer the browser runs on. */
export function isLoopbackHost(hostname: string): boolean {
  const h = hostname.trim().toLowerCase().replace(/^\[|\]$/g, '')
  return h === '' || h === 'localhost' || h.endsWith('.localhost') || h === '::1' || /^127\.\d+\.\d+\.\d+$/.test(h)
}

/**
 * Whether `conductor host` refuses `server` without --insecure: a plain
 * `http://` (or `ws://`) server that is not `localhost` or a loopback address
 * (config.PlainOffMachine on the server side, the host's rule, whose test
 * table launch.test.ts repeats). What passes between the host and such a
 * server, the session's output and its viewers' keys among it, travels in
 * the clear and unverified. The host reads the string as given, so the host
 * part is read raw here, not through the URL parser, which writes LOCALHOST
 * and 127.1 as localhost and 127.0.0.1, names the rule exempts and the host
 * does not: `localhost` alone is this machine by name, and an address is one
 * the host parses (netip.ParseAddr). Any other name is another machine, as
 * it is for the host, which does not resolve it before deciding.
 */
export function plainOffMachine(server: string): boolean {
  const m = /^([a-z][a-z0-9+.-]*):\/\/(?:[^/?#@]*@)?(\[[^\]]*\]|[^/?#:]*)/i.exec(server)
  if (!m) return false
  const scheme = (m[1] ?? '').toLowerCase()
  if (scheme !== 'http' && scheme !== 'ws') return false
  const host = m[2] ?? ''
  if (host === 'localhost') return false
  return !isLoopbackIP(host.startsWith('[') ? host.slice(1, -1) : host)
}

/**
 * Whether h is a loopback address as the host parses one (netip.ParseAddr,
 * then Unmap): four decimal octets without leading zeros, the first 127, or
 * an IPv6 address that is ::1 or 127.x.x.x mapped into IPv6 (the zone of a
 * link-local form set aside, as the host's check sets it aside). Anything
 * else, 127.1 or 127.0.0.01 among it, is no address to the host.
 */
function isLoopbackIP(h: string): boolean {
  const v4 = /^(0|[1-9]\d{0,2})\.(0|[1-9]\d{0,2})\.(0|[1-9]\d{0,2})\.(0|[1-9]\d{0,2})$/.exec(h)
  if (v4) return v4.slice(1).every((o) => Number(o) <= 255) && v4[1] === '127'
  if (!h.includes(':')) return false
  let canonical: string
  try {
    canonical = new URL(`http://[${h.replace(/%.*$/, '')}]/`).hostname.replace(/^\[|\]$/g, '')
  } catch {
    return false
  }
  return canonical === '::1' || /^::ffff:7f[0-9a-f]{2}:[0-9a-f]{1,4}$/.test(canonical)
}

/**
 * The server's one rule for a cut at a word (`session.CutAtWord`, and `cutAtWord` in internal/dispatch), held to it by one table,
 * internal/session/testdata/cut-at-word.json: s whole when it fits in `chars` characters (and `bytes` bytes, when a byte bound is
 * given); else cut to `chars - 1` characters (and `bytes - 3` bytes) at the last space after the first `keep` characters, trailing
 * spaces trimmed, and "…" added, inside both bounds. A first word past the bound is cut at a character.
 */
export function cutAtWord(s: string, keep: number, chars: number, bytes = 0): string {
  const size = (r: string[]) => new TextEncoder().encode(r.join('')).length
  let r = Array.from(s)
  if (r.length <= chars && (bytes === 0 || size(r) <= bytes)) return s
  r = r.slice(0, Math.max(chars - 1, 0))
  if (bytes > 0) while (r.length > 0 && size(r) > bytes - 3) r.pop()
  for (let i = r.length - 1; i > keep; i--) {
    if (r[i] === ' ') {
      r = r.slice(0, i)
      break
    }
  }
  return `${r.join('').replace(/ +$/, '')}…`
}

/** The most characters of a session's name made from its first message (`session.NameFromMessage`). */
export const NAME_FROM_MESSAGE = 40

/**
 * The name a launch with this first message and no name gets (`session.NameFromMessage`): control characters as spaces, runs of
 * spaces as one, trimmed, then cut at a word to 40 characters and the name's 120 bytes with "…". "" when nothing is left, for which
 * the server names the session "<agent> #N".
 */
export function namePreview(message: string): string {
  const flat = message
    .split(/[\s\p{Cc}]+/u)
    .filter(Boolean)
    .join(' ')
  return flat ? cutAtWord(flat, 0, NAME_FROM_MESSAGE, MAX_SESSION_NAME) : ''
}

/** Why a first message cannot be sent, in the words the field shows: past MAX_SUBMIT bytes as sent; "" when it can. */
export function messageProblem(text: string): string {
  const n = new TextEncoder().encode(text).length
  return n > MAX_SUBMIT ? `At most ${MAX_SUBMIT.toLocaleString('en-US')} bytes: this is ${n.toLocaleString('en-US')}.` : ''
}

/**
 * The line above a session's terminal while its first message waits (`firstMessage` "waiting"): that a startup question comes first
 * while one shows (a needs_input of source `trust`), else that the agent must be ready; "" once it no longer waits.
 */
export function firstMessageLine(s: { firstMessage?: string; attention?: { state: string; source?: string } } | null | undefined, agent: string): string {
  if (s?.firstMessage !== 'waiting') return ''
  if (s.attention?.state === 'needs_input' && s.attention.source === 'trust') return 'Your first message is typed once its question is answered.'
  return `Your first message is typed once ${agent} is ready.`
}

/** Whether a launch of the agent takes a first message: the catalog's word (`firstMessage`), which an older server never gives. */
export function takesFirstMessage(agent: { firstMessage?: boolean } | null | undefined): boolean {
  return agent?.firstMessage === true
}

/**
 * The line under the agent's choice when it takes no first message: why, and which of the agents offered do (by name, as the catalog
 * names them), so that the words stay true of the catalog as it is.
 */
export function noFirstMessageLine(offered: Array<{ name: string; firstMessage?: boolean }>): string {
  const names = offered.filter(takesFirstMessage).map((a) => a.name)
  const why = "Conductor types a first message only where it knows the agent's prompt"
  if (names.length === 0) return `${why}, and knows none of these agents' prompts.`
  const list = names.length === 1 ? names[0] : `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`
  return `${why}: ${list} here.`
}
