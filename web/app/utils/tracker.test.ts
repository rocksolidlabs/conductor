import { describe, expect, it } from 'vitest'
import { checkHost, checksWords, githubHref, githubStateWords, linkStateWords, ownersWords, parseGitHubRef, readAge, readIsStale, refWords, trackerRefusalWords, type GitHubRef } from './tracker'

// A GitHub item's reference as the server's parse reads it (internal/tracker/ref.go): the same table as TestParseGitHubURLs in
// internal/tracker/ref_test.go, row for row, so the app and the server never disagree on what a reference is.

const o = { hosts: ['ghe.example.test'] }
const lo = { loopback: true }
const ref = (host: string, space: string, kind: GitHubRef['kind'], id: string): GitHubRef => ({ host, space, kind, id })

describe("a GitHub item's reference (TestParseGitHubURLs' table)", () => {
  const ok: Array<[string, string, typeof o | typeof lo, GitHubRef]> = [
    ['https://github.com/octo-example/widgets/issues/42', '', o, ref('github.com', 'octo-example/widgets', 'issue', '42')],
    ['https://github.com/octo-example/widgets/pull/43', '', o, ref('github.com', 'octo-example/widgets', 'change', '43')],
    ['  https://GitHub.com/Octo-Example/Widgets.js/pull/43  ', '', o, ref('github.com', 'Octo-Example/Widgets.js', 'change', '43')],
    ['octo-example/widgets#12', '', o, ref('github.com', 'octo-example/widgets', 'issue', '12')],
    ['#12', 'octo-example/widgets', o, ref('github.com', 'octo-example/widgets', 'issue', '12')],
    ['#12', 'https://github.com/octo-example/widgets', o, ref('github.com', 'octo-example/widgets', 'issue', '12')],
    ['#12', 'https://github.com/octo-example/widgets.git/', o, ref('github.com', 'octo-example/widgets', 'issue', '12')],
    ['#12', 'git@github.com:octo-example/widgets.git', o, ref('github.com', 'octo-example/widgets', 'issue', '12')],
    ['#12', 'ssh://git@github.com:22/octo-example/widgets.git', o, ref('github.com', 'octo-example/widgets', 'issue', '12')],
    ['#12', 'git@ghe.example.test:team/tool.git', o, ref('ghe.example.test', 'team/tool', 'issue', '12')],
    ['team/other#3', 'git@ghe.example.test:team/tool.git', o, ref('ghe.example.test', 'team/other', 'issue', '3')],
    ['https://ghe.example.test/team/tool/issues/9', '', o, ref('ghe.example.test', 'team/tool', 'issue', '9')],
    ['http://127.0.0.1:8123/team/tool/issues/9', '', lo, ref('127.0.0.1:8123', 'team/tool', 'issue', '9')],
    ['https://github.com/octo-example/widgets/issues/18446744073709551615', '', o, ref('github.com', 'octo-example/widgets', 'issue', '18446744073709551615')],
  ]
  it.each(ok)('reads %j (in %j)', (raw, def, opts, want) => {
    expect(parseGitHubRef(raw, def, opts)).toEqual(want)
  })

  const refused: Array<[string, string, Record<string, unknown>]> = [
    ['https://gitlab.example/octo/widgets/issues/1', '', o],
    ['https://ghe.other.test/team/tool/issues/9', '', o],
    ['https://github.com/octo-example/widgets/issues/42?x=1', '', o],
    ['https://github.com/octo-example/widgets/issues/42?', '', o],
    ['https://github.com/octo-example/widgets/issues/42#issuecomment-1', '', o],
    ['https://github.com/octo-example/widgets/issues/42/files', '', o],
    ['https://github.com/octo-example/widgets/pulls/42', '', o],
    ['https://github.com/octo-example/widgets/issues/042', '', o],
    ['https://github.com/octo-example/widgets/issues/0', '', o],
    ['https://github.com/octo-example/widgets/issues/1x', '', o],
    ['https://github.com/octo-example/widgets/issues/123456789012345678901', '', o],
    ['https://github.com:8443/octo-example/widgets/issues/42', '', o],
    ['https://user@github.com/octo-example/widgets/issues/42', '', o],
    ['http://github.com/octo-example/widgets/issues/42', '', o],
    ['http://127.0.0.1:8123/team/tool/issues/9', '', o],
    ['http://ghe.example.test/team/tool/issues/9', '', { hosts: ['ghe.example.test'], loopback: true }],
    ['ftp://github.com/octo-example/widgets/issues/42', '', o],
    ['https://github.com/octo_example/widgets/issues/42', '', o],
    ['https://github.com/octo-example/../issues/42', '', o],
    ['#12', '', o],
    ['12', 'octo-example/widgets', o],
    ['octo-example#12', '', o],
    ['', '', o],
    ['https://gİthub.com/octo-example/widgets/issues/42', '', o],
    ['https://g%C4%B0thub.com/octo-example/widgets/issues/42', '', o],
    ['https://ghe.example.test./team/tool/issues/9', '', o],
    ['http://localhost:1@evil.example/team/tool/issues/9', '', lo],
    ['http://127.0.0.2:8123/team/tool/issues/9', '', lo],
    ['#12', 'git@gİthub.com:octo-example/widgets.git', o],
  ]
  it.each(refused)('refuses %j (in %j)', (raw, def, opts) => {
    expect(parseGitHubRef(raw, def, opts)).toBeNull()
  })

  it('says a reference as a person writes it', () => {
    expect(refWords(ref('github.com', 'octo-example/widgets', 'change', '43'))).toBe('octo-example/widgets#43')
  })

  it("judges a host as the server's CheckHost does, a loopback form only with the switch", () => {
    for (const h of ['github.com', 'ghe.example.test', 'a.b-c.d', 'xn--github-qyd.com', 'a1.example']) expect(checkHost(h)).toBe(true)
    for (const h of ['', 'GitHub.com', 'github.com.', '.github.com', 'git..hub.com', '-a.example', 'a-.example', 'a_b.example', 'gİthub.com', 'github.com:443', 'user@github.com', 'github.com/x', 'localhost:8123', '127.0.0.1:8123', 'ghe', 'd0e1f2a3b4c5d6e7f8091a2b3c4d5e6f7a8b9c0d'])
      expect(checkHost(h)).toBe(false)
    expect(checkHost('127.0.0.1:8123', true)).toBe(true)
    expect(checkHost('127.0.0.2:8123', true)).toBe(false)
  })
})

describe("the one rule of what of GitHub's text is a link", () => {
  it('is an https address on the item’s own host, as written', () => {
    expect(githubHref('https://github.com/octo-example/widgets/issues/42', 'github.com')).toBe('https://github.com/octo-example/widgets/issues/42')
    expect(githubHref('https://github.com/octo-example/widgets/actions/runs/1/job/2', 'github.com')).toBe('https://github.com/octo-example/widgets/actions/runs/1/job/2')
    expect(githubHref('https://ghe.example.test/team/tool/pull/9', 'ghe.example.test')).toBe('https://ghe.example.test/team/tool/pull/9')
  })

  it.each([
    ['http://github.com/octo-example/widgets/issues/42', 'github.com'],
    ['javascript:alert(1)', 'github.com'],
    ['JavaScript://github.com/%0aalert(1)', 'github.com'],
    ['data:text/html,<script>alert(1)</script>', 'github.com'],
    ['//github.com/octo-example/widgets/issues/42', 'github.com'],
    ['https://ci.example/builds/1', 'github.com'],
    ['https://github.com.evil.example/x', 'github.com'],
    ['https://evil.example/https://github.com/x', 'github.com'],
    ['https://user@github.com/x', 'github.com'],
    ['https://github.com:8443/x', 'github.com'],
    ['https://GitHub.com/x', 'github.com'],
    ['https://github.com/x y', 'github.com'],
    ['https://ghe.example.test/x', 'github.com'],
    // A loopback item's address (the tests' fake) is never a link: http, and no loopback host is a token's host here.
    ['http://127.0.0.1:8123/team/tool/issues/9', '127.0.0.1:8123'],
    ['https://127.0.0.1:8123/team/tool/issues/9', '127.0.0.1:8123'],
    ['https://github.com/x', ''],
    ['', 'github.com'],
  ])('is never %j on %j', (url, host) => {
    expect(githubHref(url, host)).toBeNull()
  })
})

describe("GitHub's words", () => {
  it("says GitHub's state of an item in its own words", () => {
    expect(githubStateWords({ state: 'open' })).toEqual({ key: 'open', label: 'Open' })
    expect(githubStateWords({ state: 'closed', stateReason: 'completed' })).toEqual({ key: 'closed', label: 'Closed as completed' })
    expect(githubStateWords({ state: 'closed', stateReason: 'not_planned' }).label).toBe('Closed as not planned')
    expect(githubStateWords({ state: 'closed' }).label).toBe('Closed')
    expect(githubStateWords({ state: 'closed', merged: true })).toEqual({ key: 'merged', label: 'Merged' })
    expect(githubStateWords({ state: 'merged' }).key).toBe('merged')
  })

  it("says the field owners", () => {
    expect(ownersWords({ title: 'github', description: 'github' })).toBe('Title: GitHub · Description: GitHub')
    expect(ownersWords({ title: 'dispatch', description: 'github' })).toBe('Title: Dispatch · Description: GitHub')
  })

  it("says a link's read state", () => {
    expect(linkStateWords({ state: 'open', merged: false }).label).toBe('Open')
    expect(linkStateWords({ state: 'closed', merged: true }).label).toBe('Merged')
    expect(linkStateWords({ state: 'closed', merged: false }).label).toBe('Closed')
    expect(linkStateWords({ merged: false }).key).toBe('unknown')
  })

  it("says a read's age, and reads again past ten minutes", () => {
    const now = Date.parse('2026-11-16T12:00:00Z')
    expect(readAge('2026-11-16T11:55:00Z', now)).toBe('read 5m ago')
    expect(readAge(undefined, now)).toBe('not read yet')
    expect(readIsStale(undefined, now)).toBe(true)
    expect(readIsStale('2026-11-16T11:55:00Z', now)).toBe(false)
    expect(readIsStale('2026-11-16T11:49:59Z', now)).toBe(true)
    expect(readIsStale('not a time', now)).toBe(true)
  })
})

describe("a link's checks", () => {
  const sum = (p: Partial<Parameters<typeof checksWords>[0]>) => ({ passed: 0, failed: 0, pending: 0, other: 0, incomplete: false, omitted: 0, ...p })
  it('are counted in words', () => {
    expect(checksWords(sum({ passed: 3, failed: 1, pending: 2 })).counts).toBe('3 passed · 1 failed · 2 pending')
    expect(checksWords(sum({})).counts).toBe('no checks')
    expect(checksWords(sum({ passed: 2 })).allPassed).toBe(true)
  })

  it('never say all passed when the set is cut, and say how many were not read', () => {
    const cut = checksWords(sum({ passed: 4, incomplete: true, omitted: 3 }))
    expect(cut.allPassed).toBe(false)
    expect(cut.incomplete).toBe('incomplete: 3 checks not read, see them on GitHub')
    expect(checksWords(sum({ passed: 4, incomplete: true, omitted: 1 })).incomplete).toBe('incomplete: 1 check not read, see them on GitHub')
    expect(checksWords(sum({ passed: 4, incomplete: true })).incomplete).toBe('incomplete: some checks not read, see them on GitHub')
    expect(checksWords(sum({ passed: 4 })).incomplete).toBe('')
  })
})

describe("the tracker's refusals", () => {
  it.each(['no_token', 'token_refused', 'token_permission', 'external_not_found', 'moved', 'external_linked', 'rate_limited', 'tracker_unavailable', 'tracker_bound', 'tracker_busy'])('words %s', (code) => {
    const w = trackerRefusalWords(code, 'the server said')
    expect(w).not.toBe('the server said')
    expect(w.length).toBeGreaterThan(10)
  })
  it("says the token's refusal points at Settings, and keeps the server's words for any other", () => {
    expect(trackerRefusalWords('no_token', '')).toContain('Settings')
    expect(trackerRefusalWords('token_refused', '')).toContain('Settings')
    expect(trackerRefusalWords('something_new', 'the server said')).toBe('the server said')
  })
})
