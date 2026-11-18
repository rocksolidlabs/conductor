import type { ExternalSnapshot, LinkReadSummary, TicketOwners } from '~/composables/useDispatch'
import { MAX_TICKET_EXTERNAL_SEARCH } from './protocol'
import { relativeTime } from './sessions'

// GitHub on Dispatch's tickets (Program item IN1a, docs/plans/tracker-linking.md, "web/app"): a GitHub item's reference as the server's
// parse reads it (internal/tracker/ref.go, ParseGitHub: the same table as TestParseGitHubURLs holds it in tracker.test.ts), the one rule
// of what of GitHub's text may be drawn as a link (githubHref), GitHub's state in its own words, the field owners', a read's age, the
// checks counted with the words of a cut list, and the words of each refusal of the tracker's routes. Pure: the pages draw them.

export const GITHUB_HOST = 'github.com'

/** A GitHub item's reference: its host, its repository (owner/name), its kind (an issue, or a pull request: `change`) and its number. */
export interface GitHubRef {
  host: string
  space: string
  kind: 'issue' | 'change'
  id: string
}

/** Which hosts a reference may name: github.com always, the Enterprise Server hosts given beside it, a loopback form with `loopback`. */
export interface RefOptions {
  hosts?: readonly string[]
  /** The tests' loopback switch (CONDUCTOR_TRACKER_LOOPBACK=1): an http loopback host with its port. */
  loopback?: boolean
}

/** The bounds the server's parse holds a reference to (internal/tracker/tracker.go). */
const MAX_REF = 2048
const MAX_SPACE = 200
const MAX_ID = 20

/** A loopback form the loopback switch admits: localhost, 127.0.0.1 or [::1], a ':' and a port of 1 to 65535 with no leading zero. */
export function isLoopback(host: string): boolean {
  for (const name of ['localhost:', '127.0.0.1:', '[::1]:']) {
    if (!host.startsWith(name)) continue
    const port = host.slice(name.length)
    return /^[1-9]\d{0,4}$/.test(port) && Number(port) <= 65535
  }
  return false
}

/**
 * A host as the server judges one (CheckHost): an exact lower-case DNS name of at least two labels whose last begins with a letter, never
 * localhost or a name under it; a loopback form only with the switch. Nothing is folded or trimmed: the string judged is the one dialled.
 */
export function checkHost(host: string, loopback = false): boolean {
  if (loopback && isLoopback(host)) return true
  if (!host || host.length > 253 || !host.includes('.') || host.endsWith('.localhost')) return false
  const last = host.slice(host.lastIndexOf('.') + 1)
  if (!/^[a-z]/.test(last)) return false
  return host.split('.').every((l) => l.length >= 1 && l.length <= 63 && /^[a-z0-9-]+$/.test(l) && !l.startsWith('-') && !l.endsWith('-'))
}

/** ASCII upper case folded, as a person may type a host; a character past ASCII is no host (null). */
function asciiLower(s: string): string | null {
  // eslint-disable-next-line no-control-regex
  if (/[^\x00-\x7f]/.test(s)) return null
  return s.replace(/[A-Z]/g, (c) => c.toLowerCase())
}

function hostAdmitted(host: string, o: RefOptions): boolean {
  if (!checkHost(host, !!o.loopback)) return false
  return host === GITHUB_HOST || (!!o.loopback && isLoopback(host)) || (o.hosts ?? []).includes(host)
}

/** An owner and a repository's name GitHub would make (checkSpace). */
function spaceOK(owner: string, name: string): boolean {
  if (!owner || owner.length > 39 || !name || name.length > 100 || name === '.' || name === '..' || owner.length + 1 + name.length > MAX_SPACE) return false
  return /^[A-Za-z0-9-]+$/.test(owner) && /^[A-Za-z0-9._-]+$/.test(name)
}

function makeRef(host: string, owner: string, name: string, kind: GitHubRef['kind'], num: string): GitHubRef | null {
  if (!spaceOK(owner, name)) return null
  if (!num || num.length > MAX_ID || num[0] === '0' || !/^\d+$/.test(num)) return null
  return { host, space: `${owner}/${name}`, kind, id: num }
}

function splitPath(p: string): string[] {
  const t = p.replace(/^\//, '').replace(/\/$/, '')
  return t ? t.split('/') : []
}

/**
 * A URL's host and path segments as the server's parseURL reads them: https (http only to a loopback host with the switch), no query,
 * fragment or user, no port but a loopback one, an admitted host. The text is split by hand, never by the browser's URL parser, which
 * folds and decodes what the server's refuses.
 */
function parseURL(raw: string, o: RefOptions): { host: string; segs: string[] } | null {
  const m = /^([A-Za-z][A-Za-z0-9+.-]*):\/\/([^/?#]*)([^?#]*)$/.exec(raw)
  if (!m || raw.includes('?') || raw.includes('#')) return null
  const scheme = m[1]!.toLowerCase()
  const authority = m[2]!
  if (authority.includes('@') || authority.includes('%')) return null
  const host = asciiLower(authority)
  if (host === null) return null
  const hasPort = /:\d*$/.test(host) && !host.endsWith(']')
  if (scheme === 'https') {
    if (hasPort && !(o.loopback && isLoopback(host))) return null
  } else if (scheme === 'http') {
    if (!o.loopback || !isLoopback(host)) return null
  } else return null
  if (!hostAdmitted(host, o)) return null
  return { host, segs: splitPath(m[3]!) }
}

/** A repository named by owner/name, its URL, or a git remote (ParseRepository): its host and owner/name, or null. */
export function parseRepository(raw: string, o: RefOptions = {}): { host: string; space: string } | null {
  const s = raw.trim()
  if (!s || s.length > MAX_REF) return null
  let host = GITHUB_HOST
  let segs: string[]
  if (s.startsWith('ssh://')) {
    const m = /^ssh:\/\/(?:[^@/]*@)?([^/:?#]+)(?::\d+)?(\/[^?#]*)$/.exec(s)
    if (!m) return null
    const h = asciiLower(m[1]!)
    if (h === null || !hostAdmitted(h, o)) return null
    host = h
    segs = splitPath(m[2]!)
  } else if (s.includes('://')) {
    const u = parseURL(s, o)
    if (!u) return null
    host = u.host
    segs = u.segs
  } else if (s.includes('@') && s.includes(':')) {
    const rest = s.slice(s.indexOf('@') + 1)
    const at = rest.indexOf(':')
    if (at < 0) return null
    const h = asciiLower(rest.slice(0, at))
    if (h === null || !hostAdmitted(h, o)) return null
    host = h
    segs = splitPath(rest.slice(at + 1))
  } else segs = s.split('/')
  if (segs.length !== 2) return null
  const name = segs[1]!.replace(/\.git$/, '')
  if (!spaceOK(segs[0]!, name)) return null
  return { host, space: `${segs[0]}/${name}` }
}

/**
 * A GitHub item's reference as the server's parse reads it (ParseGitHub): its URL (https://<host>/<owner>/<name>/issues/<n> or
 * …/pull/<n>), owner/name#n, or #n in the repository `defaultSpace` names (a project's tracker URL). A short form is of kind issue; null
 * for anything the server would refuse.
 */
export function parseGitHubRef(raw: string, defaultSpace = '', o: RefOptions = {}): GitHubRef | null {
  const s = raw.trim()
  if (!s || s.length > MAX_REF) return null
  if (s.includes('://')) {
    const u = parseURL(s, o)
    if (!u || u.segs.length !== 4) return null
    const kind = u.segs[2] === 'issues' ? 'issue' : u.segs[2] === 'pull' ? 'change' : null
    if (!kind) return null
    return makeRef(u.host, u.segs[0]!, u.segs[1]!, kind, u.segs[3]!)
  }
  const hash = s.indexOf('#')
  if (hash < 0) return null
  let space = s.slice(0, hash)
  const num = s.slice(hash + 1)
  let host = GITHUB_HOST
  if (!space) {
    if (!defaultSpace) return null
    const r = parseRepository(defaultSpace, o)
    if (!r) return null
    host = r.host
    space = r.space
  } else if (defaultSpace) {
    const r = parseRepository(defaultSpace, o)
    if (r) host = r.host
  }
  const slash = space.indexOf('/')
  if (slash < 0) return null
  return makeRef(host, space.slice(0, slash), space.slice(slash + 1), 'issue', num)
}

/** A reference as a person writes it: owner/name#12. */
export function refWords(r: Pick<GitHubRef, 'space' | 'id'>): string {
  return `${r.space}#${r.id}`
}

/**
 * The one rule of what of GitHub's text is drawn as a link (an item's URL, a snapshot's, a check's): an https address with no user, on
 * the item's own host byte for byte (github.com or the Enterprise Server's whose token read it), returned as written; null for anything
 * else (http, another host, a port, a script's scheme), which a page draws as text. A link drawn opens a new tab with no opener and no
 * referrer (rel="noopener noreferrer").
 */
export function githubHref(url: string | undefined | null, host: string | undefined | null): string | null {
  if (!url || !host || url.length > MAX_REF || !checkHost(host)) return null
  const m = /^https:\/\/([^/?#]*)(\/[^\s]*)?$/.exec(url)
  if (!m || m[1] !== host) return null
  return url
}

/** GitHub's state of an item in its own words, apart from the ticket's status: "Open", "Closed as completed", "Merged". */
export function githubStateWords(s: Pick<ExternalSnapshot, 'state'> & Partial<Pick<ExternalSnapshot, 'stateReason' | 'merged'>>): { key: 'open' | 'closed' | 'merged'; label: string } {
  if (s.merged || s.state === 'merged') return { key: 'merged', label: 'Merged' }
  if (s.state === 'closed') {
    if (s.stateReason === 'completed') return { key: 'closed', label: 'Closed as completed' }
    if (s.stateReason === 'not_planned') return { key: 'closed', label: 'Closed as not planned' }
    return { key: 'closed', label: 'Closed' }
  }
  return { key: 'open', label: 'Open' }
}

/** A field's owner in words: GitHub for the linked item's system, Dispatch otherwise. */
export function ownerWord(owner: string): 'GitHub' | 'Dispatch' {
  return owner === 'github' ? 'GitHub' : 'Dispatch'
}

/** The field owners in words: "Title: GitHub · Description: GitHub". */
export function ownersWords(o: TicketOwners): string {
  return `Title: ${ownerWord(o.title)} · Description: ${ownerWord(o.description)}`
}

/**
 * Whether a description Dispatch owns differs from GitHub's: its SHA-256 (by `hash`, the page's own: utils/publication.ts' sha256Hex,
 * which needs no `crypto.subtle`, absent from a page served over plain http) against the snapshot's. Never for one GitHub owns, nor
 * without a snapshot's digest.
 */
export function descriptionDiffersFrom(description: string, want: string | undefined, owner: string, hash: (text: string) => string): boolean {
  return !!want && owner === 'dispatch' && hash(description) !== want
}

/**
 * Whether a pasted text is a GitHub item's address the palette searches the tickets by: one the parse reads, within the server's bound
 * (MAX_TICKET_EXTERNAL_SEARCH, in UTF-8 bytes; past it the server refuses the search).
 */
export function externalSearchable(text: string, o: RefOptions): boolean {
  const url = text.trim()
  return url.includes('://') && new TextEncoder().encode(url).length <= MAX_TICKET_EXTERNAL_SEARCH && !!parseGitHubRef(url, '', o)
}

/**
 * Whether Import may send: a preview shown, of an item no ticket has yet, read for the address the field holds now and no read under
 * way. A field changed since its preview (or emptied by the dialog's reopening) needs a new one: Import never sends an item the field no
 * longer names.
 */
export function importReady(s: { preview: { linked?: string } | null; previewed: string; url: string; reading: boolean }): boolean {
  return !!s.preview && !s.preview.linked && !s.reading && s.url.trim() === s.previewed
}

/** What a field GitHub owns says where its editor would be. */
export const GITHUB_OWNED_WORDS = 'GitHub owns this: edit it on GitHub, then Refresh, or take it over.'

/** A read's age: "read 5m ago", or "not read yet". */
export function readAge(readAt: string | undefined, now: number): string {
  return readAt ? `read ${relativeTime(readAt, now, { suffix: true })}` : 'not read yet'
}

/** How old a linked item's snapshot may be when its ticket's page opens before it is read again (the plan's Owner decision 5). */
export const STALE_READ_MS = 10 * 60_000

/** Whether a ticket page that opens now reads its linked item again: never read, or read more than ten minutes ago. */
export function readIsStale(readAt: string | undefined, now: number): boolean {
  if (!readAt) return true
  const t = Date.parse(readAt)
  return !Number.isFinite(t) || now - t > STALE_READ_MS
}

/** A snapshot's JSON as the ticket's read carries it, or null for none or one that does not read as a snapshot. */
export function snapshotOf(raw: unknown): ExternalSnapshot | null {
  if (!raw || typeof raw !== 'object') return null
  const s = raw as Partial<ExternalSnapshot>
  if (typeof s.host !== 'string' || typeof s.space !== 'string' || typeof s.id !== 'string' || typeof s.state !== 'string') return null
  return s as ExternalSnapshot
}

/**
 * An imported image's caption (#989): the name of the attachment the import downloaded it as (`github-1.png`), else "image N" by its
 * place in the list (`index` from 0), as in the import's preview, where no attachment exists yet. Never the last part of the image's
 * address: GitHub serves an image under an identifier (a long run of hexadecimal digits, or an id of 36 characters), not a name.
 */
export function imageCaption(index: number, attachmentId?: string, attachments: readonly { id: string; name: string }[] = []): string {
  const name = attachmentId ? attachments.find((a) => a.id === attachmentId)?.name.trim() : ''
  return name || `image ${index + 1}`
}

/**
 * A link's checks counted in words: "3 passed · 1 failed · 2 pending"; "no checks" for none. A cut list never says all passed: its
 * words are "incomplete: N checks not read, see them on GitHub" (`incomplete`), shown in place of any all-passed word. Checks a later
 * read did not read (or could not) and kept from an earlier one (`readAt`) say when they were read (#989): "checks as read 12m ago: not
 * read again with the last read", never shown as the last read's.
 */
export function checksWords(c: LinkReadSummary['checks'], now = Date.now()): { counts: string; incomplete: string; allPassed: boolean } {
  const parts = [
    c.passed ? `${c.passed} passed` : '',
    c.failed ? `${c.failed} failed` : '',
    c.pending ? `${c.pending} pending` : '',
    c.other ? `${c.other} other` : '',
  ].filter(Boolean)
  const notRead = `${c.omitted ? `${c.omitted} ${c.omitted === 1 ? 'check' : 'checks'}` : 'some checks'} not read, see them on GitHub`
  const kept = c.readAt ? `checks as read ${relativeTime(c.readAt, now, { suffix: true })}: not read again with the last read` : ''
  const incomplete = !c.incomplete ? '' : kept ? (c.omitted ? `${kept}; ${notRead}` : kept) : `incomplete: ${notRead}`
  const allPassed = !c.incomplete && c.passed > 0 && !c.failed && !c.pending && !c.other
  return { counts: parts.length ? parts.join(' · ') : 'no checks', incomplete, allPassed }
}

/** A link's read state in words: "Open", "Closed", "Merged". */
export function linkStateWords(r: Pick<LinkReadSummary, 'state' | 'merged'>): { key: 'open' | 'closed' | 'merged' | 'unknown'; label: string } {
  if (r.merged || r.state === 'merged') return { key: 'merged', label: 'Merged' }
  if (r.state === 'closed') return { key: 'closed', label: 'Closed' }
  if (r.state === 'open') return { key: 'open', label: 'Open' }
  return { key: 'unknown', label: 'Not read' }
}

/** The tracker's refusals (docs/protocol.md, "A ticket's GitHub item" and "GitHub tokens"), each in words of the app's. */
const REFUSALS: Record<string, string> = {
  no_token: 'No GitHub token is saved for this host: save one in Settings, GitHub.',
  token_refused: 'GitHub refused the token saved for this host: save a new one in Settings, GitHub.',
  token_permission: 'The token lacks a permission this needs: give it the permissions Settings, GitHub names.',
  external_not_found: 'GitHub has no such item, or the token cannot see it.',
  moved: 'The item moved or is now another item: unlink it and link the one it is now.',
  external_linked: 'Another ticket has this item as its linked issue already.',
  rate_limited: 'GitHub is limiting this token’s requests: try again later.',
  tracker_unavailable: 'GitHub could not be reached, or its answer could not be read: try again.',
  tracker_bound: 'GitHub’s answer was larger than Conductor reads.',
  tracker_busy: 'Another read of this item is under way: try again in a moment.',
  not_a_tracker_url: 'Not a GitHub issue or pull request: give the whole address, or set the project’s tracker URL.',
  invalid_token: 'That is not a token: 1 to 255 visible characters, with no space.',
  invalid_host: 'Not a host Conductor reads: github.com, or a GitHub Enterprise Server’s name.',
  too_many_hosts: 'Tokens are kept for at most four hosts: remove one first.',
  environment: 'The server is given a token for this host in its environment: that one is used, and it is changed where the server is started.',
  credentials_busy: 'Another change of the tokens is under way: try again.',
  credentials_unusable: 'The server’s credentials folder cannot be used safely: its log names the file.',
  store_failed: 'The token could not be kept: the server could not write its file.',
}

/** A tracker route's refusal in words, by its code; `fallback` (the server's own fixed words) for any other. */
export function trackerRefusalWords(code: string, fallback: string): string {
  return REFUSALS[code] ?? fallback
}

/** Whether a refusal is the one Settings answers: no token saved, or GitHub refusing the one saved. */
export function needsSettings(code: string): boolean {
  return code === 'no_token' || code === 'token_refused'
}

/** A token's state as Settings says it. */
export function tokenStateWords(state: string): string {
  switch (state) {
    case 'ok':
      return 'GitHub accepted it'
    case 'refused':
      return 'GitHub refused it when last asked: save a new one'
    case 'unchecked':
      return 'Not asked of GitHub since the server started'
    case 'unused':
      return 'Not used: the server’s environment gives a token for this host'
  }
  return state
}
