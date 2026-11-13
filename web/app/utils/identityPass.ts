import { tokenFromFragment } from './desktop'
import { isDecisionId } from './decisions'

// A pass taken from the URL fragment (docs/plans/decide-grant.md, "web/app"; docs/protocol.md, the fragment keys): the credential an
// identity provider's decide principal is sent to the decide page with, `#pass=…`, beside the workbench token's `#token=…`. Both are
// taken out of the address before anything else runs; the pass is kept for the tab (useIdentityPass) and sent only in the decide
// page's Authorization header (useDecide).

/** The longest pass taken, in characters. */
export const MAX_PASS = 2048

/** The pass in a URL fragment (`#pass=…`): 1 to MAX_PASS of `A-Z a-z 0-9 . _ -`, '' for anything else. */
export function passFromFragment(hash: string): string {
  const p = new URLSearchParams(hash.replace(/^#/, '')).get('pass') ?? ''
  return /^[A-Za-z0-9._-]{1,2048}$/.test(p) ? p : ''
}

/** The fragment with both credential keys taken out, the rest kept: '' or `#…`; one with neither key as it is. */
function withoutCredentials(hash: string): string {
  const params = new URLSearchParams(hash.replace(/^#/, ''))
  if (!params.has('token') && !params.has('pass')) return hash
  params.delete('token')
  params.delete('pass')
  const rest = params.toString()
  return rest ? '#' + rest : ''
}

/**
 * The workbench token and the pass a URL's fragment carries (each '' when absent or refused), and the URL with both keys taken out of
 * its fragment whether or not their values were taken (the rest of the fragment kept).
 */
export function takeCredentials(href: string): { token: string; pass: string; href: string } {
  const u = new URL(href)
  const hash = u.hash
  u.hash = withoutCredentials(hash)
  return { token: tokenFromFragment(hash), pass: passFromFragment(hash), href: u.toString() }
}

/**
 * Where a navigation to `fullPath` goes, the decide middleware's rule (null: where it was going). A workbench token held (or in the
 * fragment) wins: `/decide/<did>` opens the operator's page of that decision, `/decide` the operator's decision page. A pass alone:
 * the operator's address of one decision opens it on the decide page, and every other path but the decide page's goes to `/decide`.
 * A fragment that carries either key is always taken off the address.
 */
export function decideRedirect(fullPath: string, hasToken: boolean, hasPass: boolean): string | null {
  const u = new URL(fullPath, 'http://app.invalid')
  const rest = withoutCredentials(u.hash)
  const token = hasToken || !!tokenFromFragment(u.hash)
  const pass = hasPass || !!passFromFragment(u.hash)
  const decide = /^\/decide(?:\/([^/]+))?\/?$/.exec(u.pathname)
  let to: string | null = null
  if (token && decide) to = isDecisionId(decide[1]) ? `/decisions?item=decision:${decide[1]}` : '/decisions'
  else if (!token && pass && !decide) {
    const did = /^decision:(.*)$/.exec(u.searchParams.get('item') ?? '')?.[1]
    to = u.pathname === '/decisions' && isDecisionId(did) ? `/decide/${did}` : '/decide'
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
