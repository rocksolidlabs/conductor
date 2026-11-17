import type { Page } from '@playwright/test'
import { budget, dayPage, expect, test } from './fixtures'
import { fakeGitHub, type FakeGitHub } from './fake-github'
import { freeProjectKey, makeProject, opOf } from './dispatch-helpers'
import { walk, worst } from './contrast'
import { MEASURE_WAIT_MS, ageBy, noSideways } from './layout'
import { ownServer, type OwnServer } from './own-server'
import { START_WAIT_MS } from './server'

// GitHub on Dispatch's tickets (Program item IN1a, docs/plans/tracker-linking.md, PR-8), against the e2e's fake GitHub (fake-github.ts)
// on a server of the spec's own in loopback mode: the token saved in Settings and never shown again; an issue with two images and steps
// to reproduce imported from the palette; the GitHub panel; the title's editor read-only; GitHub's title changed, then Refresh: the title
// follows and the history says so; Take over; GitHub's issue closed, then Refresh: the ticket's status unchanged, GitHub's chip Closed.

/** An expect's bound and an action's (the config's expect timeout). */
const STEP_MS = 15_000
const TOKEN = 'ghp_e2eFakeToken0123456789abcdefIMPORT'
const REFUSED = 'ghp_e2eRefusedToken0123456789abcdefNOPE'
const PNG = Buffer.from('89504e470d0a1a0a0000000d49484452000000010000000108060000001f15c4890000000d49444154789c6300010000000500010d0a2db40000000049454e44ae426082', 'hex')

let srv: OwnServer
let gh: FakeGitHub

test.beforeAll(async () => {
  test.setTimeout(START_WAIT_MS + 30_000)
  gh = await fakeGitHub()
  srv = await ownServer('tracker-import', { env: { CONDUCTOR_TRACKER_LOOPBACK: '1' } })
})

test.afterAll(async () => {
  await gh?.stop()
  await srv?.stop()
})

/** Everything the page holds where a token could linger: its DOM, its storage, its address. */
async function pageHolds(page: Page): Promise<string> {
  return page.evaluate(() => {
    const store = (s: Storage) => JSON.stringify(Object.fromEntries(Object.keys(s).map((k) => [k, s.getItem(k)])))
    const fields = Array.from(document.querySelectorAll('input, textarea')).map((e) => (e as HTMLInputElement).value).join('\n')
    return [document.documentElement.outerHTML, fields, store(localStorage), store(sessionStorage), location.href, document.title].join('\n')
  })
}

test('a GitHub token is saved in Settings by its own Check and save, and never shown again', async ({ browser }) => {
  budget(16 * STEP_MS)
  const page = await dayPage(browser, { path: '/settings?section=github', server: srv.started })
  const said: string[] = []
  page.on('console', (m) => said.push(m.text()))
  const carried: string[] = []
  page.on('request', (r) => {
    if ((r.postData() ?? '').includes(TOKEN) || r.url().includes(TOKEN)) carried.push(`${r.method()} ${new URL(r.url()).pathname}`)
  })
  const card = page.locator('[data-settings-section="github"]')
  await expect(card).toBeVisible()
  await expect(card.locator('[data-github-none]')).toBeVisible()
  const field = card.locator('input[data-github-token]')
  await expect(field).toHaveAttribute('type', 'password')
  await expect(field).toHaveAttribute('autocomplete', 'new-password')
  await card.locator('input[data-github-host]').fill(gh.host)
  await field.fill(TOKEN)
  // The card is out of the page's one Save: a token typed makes nothing for the save bar to save.
  const save = page.locator('[data-save-settings]')
  if (await save.count()) await expect(save).toBeDisabled()
  await card.locator('[data-github-save]').click()
  const entry = card.locator(`[data-github-entry="${gh.host}"]`)
  await expect(entry).toHaveAttribute('data-github-state', 'ok')
  await expect(entry.locator('[data-github-login]')).toHaveText('@mona-example')
  await expect(entry).toHaveAttribute('data-github-source', 'settings')
  await expect(entry.locator('[data-github-fingerprint]')).toHaveText(/^[0-9a-f]{12}$/)
  await expect(field).toHaveValue('')
  await expect(card.locator('[data-github-notice]')).toContainText('mona-example')
  expect(gh.count('user')).toBe(1)
  // Nowhere in the page, its storage, its address, its console; sent in one request, the save's.
  expect(await pageHolds(page)).not.toContain(TOKEN)
  expect(said.join('\n')).not.toContain(TOKEN)
  expect(carried).toEqual(['POST /api/trackers/github'])
  const listed = await srv.api.ok<{ github: unknown[] }>('GET', '/api/trackers')
  expect(JSON.stringify(listed)).not.toContain(TOKEN)
  // Read again: the entry, never the token.
  await page.reload()
  await expect(entry).toHaveAttribute('data-github-state', 'ok')
  expect(await pageHolds(page)).not.toContain(TOKEN)

  // A token GitHub refuses: said in the card's words, the field emptied, nothing kept, the saved one as it was.
  gh.refuse(REFUSED)
  await card.locator('input[data-github-host]').fill(gh.host)
  await field.fill(REFUSED)
  await card.locator('[data-github-save]').click()
  await expect(card.locator('[data-github-error]')).toContainText('GitHub refused the token')
  await expect(field).toHaveValue('')
  await expect(entry).toHaveAttribute('data-github-state', 'ok')
  // Nor in a toast: the page's DOM, its toasts among it, holds neither token.
  expect(await pageHolds(page)).not.toContain(REFUSED)

  // Remove says it stops Conductor's use only; the token stays valid on GitHub until revoked there.
  await entry.locator('[data-github-remove]').click()
  await expect(entry.locator('[data-github-remove-ask]')).toContainText('stays valid on GitHub until you revoke it there')
  await entry.locator('[data-github-remove-confirm]').click()
  await expect(entry).toHaveCount(0)
  await expect(card.locator('[data-github-notice]')).toContainText('stays valid on GitHub')
  // Kept again for the specs after this one, through the route.
  await srv.api.ok('POST', '/api/trackers/github', { host: gh.host, token: TOKEN })
})

test('an issue with two images and steps to reproduce, imported from the palette, owned by GitHub until taken over', async ({ browser }) => {
  budget(30 * STEP_MS)
  await srv.api.ok('POST', '/api/trackers/github', { host: gh.host, token: TOKEN })
  const key = await freeProjectKey(srv.api, 'GHI')
  await makeProject(srv.api, key)
  const body = [
    'The list crashes when it is empty.',
    '',
    '### Steps to reproduce',
    '',
    '1. Open the list',
    '2. Remove every item',
    '',
    '<script>window.__pwned = 1</script>',
    '[a link](javascript:window.__pwned=2)',
    '![remote](https://evil.example/pixel.png)',
  ].join('\n')
  gh.set({ n: 12, kind: 'issue', title: 'The list crashes when empty', body, state: 'open', labels: ['bug'], assignees: ['hubot-example'], images: [{ path: 'one.png', type: 'image/png', bytes: PNG }, { path: 'two.png', type: 'image/png', bytes: PNG }] })

  const page = await dayPage(browser, { path: `/dispatch/${key}`, server: srv.started })
  await expect(page.locator('[data-dispatch-import-open]')).toBeVisible()
  await page.keyboard.press('Control+k')
  const palette = page.locator('[data-palette]')
  await palette.locator('input[data-palette-input]').fill(`>Import from GitHub into ${key}`)
  const row = palette.locator(`[data-palette-row="action:import:${key}"]`)
  await expect(row).toBeVisible()
  await row.click()
  const dialog = page.locator('[data-dispatch-import]')
  await expect(dialog).toBeVisible()
  await dialog.locator('input[data-import-url]').fill(gh.url(12))
  await dialog.locator('[data-import-read]').click()
  const preview = dialog.locator('[data-import-preview]')
  await expect(preview.locator('[data-import-title]')).toHaveText('The list crashes when empty')
  await expect(preview.locator('[data-import-image]')).toHaveCount(2)
  await expect(preview.locator('[data-import-repro]')).toContainText('Remove every item')
  await expect(preview.locator('[data-import-state]')).toHaveText('Open')
  await expect(preview.locator('[data-import-owners]')).toContainText('Title: GitHub · Description: GitHub')
  // The preview reads; it downloads nothing.
  expect(gh.count('image')).toBe(0)
  // Another address typed after the preview: Import waits for its own preview, and never sends the item read before.
  const submit = page.locator('[data-import-submit]')
  await expect(submit).toBeEnabled()
  await dialog.locator('input[data-import-url]').fill(gh.url(13))
  await expect(submit).toBeDisabled()
  await expect(page.locator('[data-import-stale]')).toContainText('Read it again')
  await dialog.locator('input[data-import-url]').fill(gh.url(12))
  await expect(submit).toBeEnabled()
  await submit.click()

  // The ticket's page, its GitHub panel first in the strip.
  await expect(page).toHaveURL(new RegExp(`/dispatch/${key}/1$`))
  const panel = page.locator('#ticket-github[data-ticket-github]')
  await expect(panel).toContainText(`Imported from ${gh.owner}/${gh.repo}#12`)
  await expect(page.locator('[data-ticket-strip] [data-strip-item]').first()).toHaveAttribute('data-strip-item', 'github')
  const chip = panel.locator('[data-github-state-chip]')
  await expect(chip).toHaveAttribute('data-github-state', 'open')
  // A loopback item's address is http: it is drawn as text, never as a link.
  await expect(panel.locator('[data-github-ref]')).toHaveText(`${gh.owner}/${gh.repo}#12`)
  expect(await panel.locator('[data-github-ref]').evaluate((e) => e.tagName)).toBe('SPAN')
  // The two images, downloaded once by the server, drawn from the attachment route alone.
  const imgs = panel.locator('[data-github-images] img')
  await expect(imgs).toHaveCount(2)
  for (const src of await imgs.evaluateAll((l) => l.map((e) => (e as HTMLImageElement).src))) expect(src).toMatch(/^blob:/)
  expect(gh.count('image')).toBe(2)
  expect(gh.requests().filter((r) => r.route === 'image').every((r) => r.token === '')).toBe(true)
  await expect(panel.locator('[data-github-repro]')).toContainText('Remove every item')
  // GitHub's description through the sanitiser: no script ran, no javascript: link, no remote image loaded.
  const description = page.locator('#ticket-description')
  await expect(description).toContainText('The list crashes when it is empty.')
  expect(await page.evaluate(() => (window as unknown as { __pwned?: number }).__pwned)).toBeUndefined()
  await expect(description.locator('script')).toHaveCount(0)
  await expect(description.locator('a[href^="javascript"]')).toHaveCount(0)
  await expect(description.locator('img')).toHaveCount(0)

  // The title and the description are GitHub's: no editor, and they say where they are edited.
  await expect(page.locator('[data-title-edit]')).toHaveCount(0)
  await expect(page.locator('[data-title-owned][data-owned-by="github"]')).toContainText('edit it on GitHub, then Refresh, or take it over')
  await expect(description).toHaveAttribute('data-owned-by', 'github')
  await expect(description.locator('[data-markdown-edit]')).toHaveCount(0)
  await expect(panel.locator('[data-github-owner="title"]')).toHaveAttribute('data-owner', 'github')

  // The item's address pasted into the palette's search finds the ticket linked to it, first, named by the address.
  await page.keyboard.press('Control+k')
  await palette.locator('input[data-palette-input]').fill(gh.url(12))
  const found = palette.locator(`[data-palette-row="ticket:${key}-1"]`)
  await expect(found).toContainText(`Linked to ${gh.url(12)}`)
  await page.keyboard.press('Escape')
  await expect(palette).toBeHidden()

  // GitHub's title changed, then Refresh: the title follows, and the history says it came from GitHub.
  gh.set({ ...gh.item(12)!, title: 'The list crashes when it is emptied', updatedAt: undefined })
  await panel.locator('[data-github-refresh]').click()
  await expect(page.locator('[data-ticket-title]')).toHaveText('The list crashes when it is emptied')
  await expect(page.locator('[data-ticket-history]')).toContainText('the title, from GitHub: "The list crashes when it is emptied"')

  // Take over: the title is Dispatch's, its editor back, and the history says so.
  await panel.locator('[data-github-take-over="title"]').click()
  await expect(panel.locator('[data-github-owner="title"]')).toHaveAttribute('data-owner', 'dispatch')
  await expect(page.locator('[data-title-edit]')).toBeVisible()
  await expect(page.locator('[data-ticket-history]')).toContainText('the title, taken over from GitHub')
  // Give back to GitHub asks first: the text here would be replaced by GitHub's. Keep it keeps it Dispatch's.
  await panel.locator('[data-github-give-back="title"]').click()
  await expect(panel.locator('[data-github-give-back-ask]')).toContainText("replaced by GitHub's")
  await panel.locator('[data-github-give-back-keep]').click()
  await expect(panel.locator('[data-github-give-back-ask]')).toHaveCount(0)
  await expect(panel.locator('[data-github-owner="title"]')).toHaveAttribute('data-owner', 'dispatch')

  // GitHub's issue closed, then Refresh: GitHub's chip says so, and the ticket's status has not moved.
  const status = await page.locator('[data-ticket-status]').textContent()
  gh.set({ ...gh.item(12)!, state: 'closed', stateReason: 'completed', updatedAt: undefined })
  await panel.locator('[data-github-refresh]').click()
  await expect(chip).toHaveAttribute('data-github-state', 'closed')
  await expect(chip).toHaveText('Closed as completed')
  await expect(panel.locator('[data-github-closed]')).toContainText('has not moved: you decide')
  await expect(page.locator('[data-strip-item="github"] [data-strip-words]')).toHaveText('closed')
  await expect(page.locator('[data-ticket-status]')).toHaveText(status ?? '')
  const stored = await srv.api.ok<{ ticket: { status: string } }>('GET', `/api/dispatch/tickets/${key}-1`)
  expect(stored.ticket.status).toBe('backlog')
})

test('the GitHub panel and the Settings card, in both themes, on a desk and a phone: every text over its floor, nothing sideways', async ({ browser }) => {
  budget(8 * (3 * MEASURE_WAIT_MS + STEP_MS))
  await srv.api.ok('POST', '/api/trackers/github', { host: gh.host, token: TOKEN })
  const key = await freeProjectKey(srv.api, 'GHD')
  await makeProject(srv.api, key)
  gh.set({ n: 31, kind: 'issue', title: 'A long title of an issue written on GitHub, long enough to wrap on a phone', body: 'It fails.\n\n### Steps to reproduce\n\n1. Open it\n', state: 'open', labels: ['bug', 'ui'], assignees: ['hubot-example'], images: [{ path: 'shot.png', type: 'image/png', bytes: PNG }] })
  await srv.api.ok('POST', `/api/dispatch/projects/${key}/import`, { opId: opOf(key, 'import'), url: gh.url(31) })
  for (const theme of ['light', 'dark'] as const) {
    for (const size of [{ width: 1440, height: 900 }, { width: 390, height: 844, touch: true }]) {
      const at = `${theme} at ${size.width}`
      const page = await dayPage(browser, { path: `/dispatch/${key}/1`, server: srv.started, theme, ...size })
      const panel = page.locator('#ticket-github[data-ticket-github]')
      await expect(panel.locator('[data-github-images] img')).toHaveCount(1)
      await expect(panel.locator('[data-github-read-at]')).toContainText('read')
      // The walk judges what is in view: on a phone the properties come first, the panel below them.
      await panel.scrollIntoViewIfNeeded()
      const judged = await walk(page, panel)
      expect(judged.judged, at).toBeGreaterThan(5)
      expect(judged.failures, `${at}: ${worst(judged.failures)}`).toEqual([])
      await noSideways(page, { allow: page.locator('[data-ticket-strip]') })
      await page.goto('/settings?section=github')
      const card = page.locator('[data-settings-section="github"]')
      await expect(card.locator(`[data-github-entry="${gh.host}"]`)).toHaveAttribute('data-github-state', 'ok')
      await card.scrollIntoViewIfNeeded()
      const cardJudged = await walk(page, card)
      expect(cardJudged.failures, `${at}, Settings: ${worst(cardJudged.failures)}`).toEqual([])
      await noSideways(page)
      await page.context().close()
    }
  }
})

test("Remove's question shows under the row it was asked for, never under the environment's token for the same host", async ({ browser }) => {
  budget(4 * STEP_MS)
  const page = await dayPage(browser, { path: '/', server: srv.started })
  // The server lists a host the environment gives a token for twice: the file's entry (unused) and the environment's.
  await page.route('**/api/trackers', (r) =>
    r.fulfill({
      json: {
        github: [
          { host: 'github.com', login: 'mona-example', fingerprint: 'a1b2c3d4e5f6', scopes: ['repo'], savedAt: '2026-11-16T12:00:00Z', source: 'settings', state: 'unused' },
          { host: 'github.com', login: 'hubot-example', fingerprint: 'f6e5d4c3b2a1', scopes: [], source: 'environment', state: 'unchecked' },
        ],
      },
    }),
  )
  await page.goto('/settings?section=github')
  const card = page.locator('[data-settings-section="github"]')
  const saved = card.locator('[data-github-entry="github.com"][data-github-source="settings"]')
  const env = card.locator('[data-github-entry="github.com"][data-github-source="environment"]')
  await expect(env).toContainText('from the environment')
  await expect(env.locator('[data-github-remove]')).toHaveCount(0)
  await saved.locator('[data-github-remove]').click()
  await expect(saved.locator('[data-github-remove-ask]')).toBeVisible()
  await expect(card.locator('[data-github-remove-ask]')).toHaveCount(1)
  await expect(env.locator('[data-github-remove-ask]')).toHaveCount(0)
})

test("a description Dispatch owns that differs from GitHub's is said so on a page with no crypto.subtle (plain http's)", async ({ browser }) => {
  budget(6 * STEP_MS)
  await srv.api.ok('POST', '/api/trackers/github', { host: gh.host, token: TOKEN })
  const key = await freeProjectKey(srv.api, 'GHS')
  await makeProject(srv.api, key)
  gh.set({ n: 41, kind: 'issue', title: 'The same title', body: 'What GitHub says.', state: 'open' })
  const ticket = await srv.api.ok<{ ticket: { key: string } }>('POST', '/api/dispatch/tickets', { opId: opOf(key, 't1'), project: key, title: 'The same title', description: 'What Dispatch says.' })
  await srv.api.ok('POST', `/api/dispatch/tickets/${ticket.ticket.key}/external`, { opId: opOf(key, 'link'), url: gh.url(41) })
  const page = await dayPage(browser, { path: '/', server: srv.started })
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  page.on('console', (m) => {
    if (m.type() === 'error') errors.push(m.text())
  })
  // As a page served over plain http from another machine: no crypto.subtle.
  await page.addInitScript(() => Object.defineProperty(globalThis.crypto, 'subtle', { value: undefined, configurable: true }))
  await page.goto(`/dispatch/${key}/1`)
  const panel = page.locator('#ticket-github[data-ticket-github]')
  await expect(panel.locator('[data-github-owner="description"]')).toHaveAttribute('data-owner', 'dispatch')
  await expect(panel.locator('[data-github-differs]')).toContainText("GitHub's description differs")
  await expect(panel.locator('[data-github-differs]')).not.toContainText('title')
  expect(await page.evaluate(() => globalThis.crypto.subtle)).toBeUndefined()
  expect(errors.filter((e) => /digest|subtle/.test(e))).toEqual([])
})

test('a ticket opened on a read older than ten minutes reads GitHub where it shows, holds no edit, and keeps a failure in the panel', async ({ browser }) => {
  budget(12 * STEP_MS)
  await srv.api.ok('POST', '/api/trackers/github', { host: gh.host, token: TOKEN })
  const key = await freeProjectKey(srv.api, 'GHO')
  await makeProject(srv.api, key)
  gh.set({ n: 51, kind: 'issue', title: 'Read on opening', body: 'It is read.', state: 'open' })
  await srv.api.ok('POST', `/api/dispatch/projects/${key}/import`, { opId: opOf(key, 'import'), url: gh.url(51) })
  // The page's clock eleven minutes on: the import's read is past the ten minutes a page reads again after.
  const page = await dayPage(browser, { path: '/', server: srv.started })
  await ageBy(page, 11 * 60_000)
  const held = gh.hold('read')
  const reads = gh.count('read')
  await page.goto(`/dispatch/${key}/1`)
  const panel = page.locator('#ticket-github[data-ticket-github]')
  await held.reached
  await expect(panel.locator('[data-github-read-at]')).toHaveText('reading GitHub…')
  // An edit made meanwhile is not held behind the read the person did not ask for.
  await srv.api.ok('GET', `/api/dispatch/tickets/${key}-1`)
  await page.locator('[data-ticket-priority]').click()
  await page.getByRole('option', { name: 'High' }).click()
  await expect.poll(async () => (await srv.api.ok<{ ticket: { priority: number } }>('GET', `/api/dispatch/tickets/${key}-1`)).ticket.priority).toBe(3)
  held.release()
  await expect(panel.locator('[data-github-read-at]')).toContainText('read ')
  expect(gh.count('read')).toBeGreaterThan(reads)

  // GitHub refusing the token on opening: said in the panel with Settings, never in a toast.
  const refusedAt = gh.count('read')
  gh.refuse(TOKEN)
  try {
    const again = await dayPage(browser, { path: '/', server: srv.started })
    await ageBy(again, 11 * 60_000)
    await again.goto(`/dispatch/${key}/1`)
    const failure = again.locator('#ticket-github [data-github-failure]')
    await expect(failure).toContainText('GitHub refused the token')
    await expect(failure.locator('[data-github-settings-link]')).toBeVisible()
    await expect(again.getByText('not changed')).toHaveCount(0)
    expect(gh.count('read')).toBe(refusedAt)
  } finally {
    gh.allow(TOKEN)
  }

  // With no token kept for the item's host, nothing is asked on opening.
  await srv.api.ok('POST', '/api/trackers/github/remove', { host: gh.host })
  const before = gh.requests().length
  const none = await dayPage(browser, { path: '/', server: srv.started })
  await ageBy(none, 11 * 60_000)
  await none.goto(`/dispatch/${key}/1`)
  await expect(none.locator('#ticket-github [data-github-read-at]')).toContainText('read ')
  await expect(none.locator('#ticket-github [data-github-failure]')).toHaveCount(0)
  await expect(none.getByText('not changed')).toHaveCount(0)
  expect(gh.requests().length).toBe(before)
  await srv.api.ok('POST', '/api/trackers/github', { host: gh.host, token: TOKEN })
})
