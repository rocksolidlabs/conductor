import { tokenFromFragment } from './desktop'
import { isDecisionId } from './decisions'

// A pass taken from the URL fragment (docs/plans/decide-grant.md, "web/app"; docs/protocol.md, the fragment keys): the credential an
// identity provider's decide principal is sent to the decide page with, `#pass=…`, beside the workbench token's `#token=…`. Both are
// taken out of the address before the app's router reads it; the pass is kept for the tab (useIdentityPass) and sent only in the
// decide page's Authorization header (useDecide).

/** The longest pass taken, in characters. */
export const MAX_PASS = 2048

/** The pass in a URL fragment (`#pass=…`): 1 to MAX_PASS of `A-Z a-z 0-9 . _ -`, '' for anything else. */
export function passFromFragment(hash: string): string {
  const p = new URLSearchParams(hash.replace(/^#/, '')).get('pass') ?? ''
  return /^[A-Za-z0-9._-]{1,2048}$/.test(p) ? p : ''
}

/**
 * The credential keys a fragment holds in any spelling, `?` beside them when it cannot be read: its keys are read as the browser's own
 * parser reads them (percent-decoded), and again after each layer of escapes is taken off (the router writes `%` as `%25`); a fragment
 * whose escapes cannot be taken off, or that still holds one after four, cannot be read.
 */
function credentialKeys(hash: string): Set<string> {
  const keys = new Set<string>()
  let s = hash.replace(/^#/, '')
  for (let i = 0; i < 4; i++) {
    for (const k of new URLSearchParams(s).keys()) if (k === 'token' || k === 'pass') keys.add(k)
    if (!s.includes('%')) return keys
    try {
      s = decodeURIComponent(s)
    } catch {
      return keys.add('?')
    }
  }
  return keys.add('?')
}

/** The fragment as it may stay in an address: as it is when it holds no credential key and can be read, else none at all. */
const withoutCredentials = (hash: string) => (credentialKeys(hash).size ? '' : hash)

/**
 * The workbench token and the pass a URL's fragment carries (each '' when absent or refused, and both '' when it holds both keys), and
 * the URL with no fragment when it carries either key in any spelling, whether or not their values were taken.
 */
export function takeCredentials(href: string): { token: string; pass: string; href: string } {
  const u = new URL(href)
  const hash = u.hash
  const keys = credentialKeys(hash)
  if (keys.size) u.hash = ''
  if (keys.has('token') && keys.has('pass')) return { token: '', pass: '', href: u.toString() }
  return { token: tokenFromFragment(hash), pass: passFromFragment(hash), href: u.toString() }
}

/**
 * A decide address, `/decide` or `/decide/<one segment>` (the segment in [1], as written), in any case of its letters, as the router
 * matches the decide page's route (its paths are not case-sensitive); null for any other path.
 */
export const decideAddress = (pathname: string) => /^\/decide(?:\/([^/]+))?\/?$/i.exec(pathname)

/** The workbench's decisions page, `/decisions`, in any case of its letters, as the router matches its route. */
export const decisionsAddress = (pathname: string) => /^\/decisions\/?$/i.test(pathname)

/**
 * Where a navigation to `fullPath` goes, the decide middleware's rule (null: where it was going). A workbench token held (or in the
 * fragment) wins: `/decide/<did>` opens the operator's page of that decision, `/decide` the operator's decision page. A pass alone:
 * the operator's address of one decision opens it on the decide page, and every other path but the decide page's goes to `/decide`.
 * A fragment that carries either key in any spelling, or that cannot be read, is always taken off the address whole.
 */
export function decideRedirect(fullPath: string, hasToken: boolean, hasPass: boolean): string | null {
  const u = new URL(fullPath, 'http://app.invalid')
  const rest = withoutCredentials(u.hash)
  const token = hasToken || !!tokenFromFragment(u.hash)
  const pass = hasPass || !!passFromFragment(u.hash)
  const decide = decideAddress(u.pathname)
  let to: string | null = null
  if (token && decide) to = isDecisionId(decide[1]) ? `/decisions?item=decision:${decide[1]}` : '/decisions'
  else if (!token && pass && !decide) {
    const did = /^decision:(.*)$/.exec(u.searchParams.get('item') ?? '')?.[1]
    to = decisionsAddress(u.pathname) && isDecisionId(did) ? `/decide/${did}` : '/decide'
  }
  if (to) return to + rest
  return rest === u.hash ? null : u.pathname + u.search + rest
}

/**
 * The name a decide principal's answer is shown under, as the server shows it (internal/api decideResponse, internal/crew
 * checkResponse): the provider's name without its characters of Unicode's Cf, Zl and Zp and its control characters, line breaks and
 * tabs as spaces, the space around it trimmed, at most 64 characters; its actor (`user:<subject>`) when nothing is left.
 */
export function shownName(name: string | undefined, subject: string | undefined): string {
  const cleaned = (name ?? '')
    .replace(/[\p{Cf}\p{Zl}\p{Zp}\u0000-\u0008\u000b-\u001f\u007f]/gu, '')
    .replace(/[\t\n]/g, ' ')
    .replace(/^[ \u0085\p{Zs}]+|[ \u0085\p{Zs}]+$/gu, '')
  return Array.from(cleaned).slice(0, 64).join('') || `user:${subject ?? ''}`
}
