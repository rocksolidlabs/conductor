import { afterEach, describe, expect, it, vi } from 'vitest'
import { sha256Hex } from './publication'
import { MAX_TICKET_EXTERNAL_SEARCH } from './protocol'
import { descriptionDiffersFrom, externalSearchable, imageCaption, importReady } from './tracker'
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

  it('kept from an earlier read say when they were read, never as the last read whole (#989)', () => {
    const now = Date.parse('2026-11-18T12:00:00Z')
    const kept = checksWords(sum({ passed: 2, incomplete: true, readAt: '2026-11-18T11:48:00Z' }), now)
    expect(kept.allPassed).toBe(false)
    expect(kept.incomplete).toBe('checks as read 12m ago: not read again with the last read')
    expect(checksWords(sum({ passed: 2, incomplete: true, omitted: 3, readAt: '2026-11-18T11:48:00Z' }), now).incomplete).toBe(
      'checks as read 12m ago: not read again with the last read; 3 checks not read, see them on GitHub',
    )
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

describe("GitHub's description differs (a page with no crypto.subtle, as one served over plain http)", () => {
  afterEach(() => vi.unstubAllGlobals())
  it("says so for a Dispatch-owned description whose SHA-256 is not the snapshot's, by the page's own digest", () => {
    vi.stubGlobal('crypto', {})
    const github = sha256Hex('GitHub wrote this.')
    expect(descriptionDiffersFrom('Dispatch wrote this.', github, 'dispatch', sha256Hex)).toBe(true)
    expect(descriptionDiffersFrom('GitHub wrote this.', github, 'dispatch', sha256Hex)).toBe(false)
    // GitHub's own, or no digest: nothing to say.
    expect(descriptionDiffersFrom('Dispatch wrote this.', github, 'github', sha256Hex)).toBe(false)
    expect(descriptionDiffersFrom('Dispatch wrote this.', undefined, 'dispatch', sha256Hex)).toBe(false)
  })
})

describe("Import's send", () => {
  const preview = { linked: undefined as string | undefined }
  const at = 'https://github.com/octo-example/widgets/issues/12'
  it('is offered only for the address the preview was read for, as the field holds it now', () => {
    expect(importReady({ preview, previewed: at, url: at, reading: false })).toBe(true)
    expect(importReady({ preview, previewed: at, url: ` ${at} `, reading: false })).toBe(true)
    // Another address typed after the preview: read it first.
    expect(importReady({ preview, previewed: at, url: 'https://github.com/octo-example/widgets/issues/13', reading: false })).toBe(false)
    // The dialog opened again (its field emptied) while an earlier read landed.
    expect(importReady({ preview, previewed: at, url: '', reading: false })).toBe(false)
    expect(importReady({ preview: null, previewed: '', url: at, reading: false })).toBe(false)
    expect(importReady({ preview, previewed: at, url: at, reading: true })).toBe(false)
    expect(importReady({ preview: { linked: 'CND-3' }, previewed: at, url: at, reading: false })).toBe(false)
  })
})

describe('a pasted address the palette searches by', () => {
  const o = { hosts: ['ghe.example.test'] }
  it('is a GitHub item the parse reads, within the server’s bound', () => {
    expect(MAX_TICKET_EXTERNAL_SEARCH).toBe(2048)
    expect(externalSearchable('https://github.com/octo-example/widgets/issues/12', o)).toBe(true)
    expect(externalSearchable('https://ghe.example.test/team/tool/pull/9', o)).toBe(true)
    expect(externalSearchable('https://ghe.example.test/team/tool/pull/9', {})).toBe(false)
    expect(externalSearchable('octo-example/widgets#12', o)).toBe(false)
    const long = `https://github.com/octo-example/${'w'.repeat(100)}/issues/${'9'.repeat(20)}`
    expect(externalSearchable(long, o)).toBe(true)
    expect(externalSearchable(`https://github.com/octo-example/widgets/issues/1${'0'.repeat(MAX_TICKET_EXTERNAL_SEARCH)}`, o)).toBe(false)
  })
})

describe('imageCaption', () => {
  // The ids GitHub serves its images under: a run of hexadecimal digits and an id of 36 characters.
  const HEX = '4f9a0c3e8b2d7a61c5e0f8b9a3d2c1e07b6a5f4e9d8c7b6a5f4e3d2c1b0a9f8e'
  const UUID = '0a1b2c3d-4e5f-4a7b-8c9d-0e1f2a3b4c5d'
  const attachments = [
    { id: 'att-1', name: 'github-1.png' },
    { id: 'att-2', name: 'github-2.jpg' },
  ]

  it("is the attachment's name once the import downloaded the image", () => {
    expect(imageCaption(0, 'att-1', attachments)).toBe('github-1.png')
    expect(imageCaption(1, 'att-2', attachments)).toBe('github-2.jpg')
  })

  it('is "image N" by its place where there is no attachment: the preview, a skipped image, one not downloaded yet, one not found', () => {
    expect([0, 1, 2].map((i) => imageCaption(i))).toEqual(['image 1', 'image 2', 'image 3'])
    expect(imageCaption(2, undefined, attachments)).toBe('image 3')
    expect(imageCaption(3, 'att-gone', attachments)).toBe('image 4')
    expect(imageCaption(0, 'att-3', [{ id: 'att-3', name: '  ' }])).toBe('image 1')
  })

  it('never shows the identifier GitHub serves an image under', () => {
    // The image's id is its address; nothing of its last part reaches the caption, whichever case.
    for (const id of [`https://github.com/user-attachments/assets/${UUID}`, `https://private-user-images.githubusercontent.com/1/${HEX}.png?jwt=x`]) {
      const last = id.slice(id.lastIndexOf('/') + 1)
      for (const caption of [imageCaption(0), imageCaption(0, 'att-1', attachments), imageCaption(5, 'none', attachments)]) {
        expect(caption).not.toContain(last.slice(0, 8))
        expect(caption).not.toMatch(/[0-9a-f]{8}/)
      }
    }
  })
})
