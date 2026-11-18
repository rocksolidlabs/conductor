import type { Page, Route } from '@playwright/test'
import { dayPage, expect, test } from './fixtures'
import { stubInbox } from './inbox-stub'
import { inside, noSideways } from './layout'
import { legacyRun, serveRun, type StubRun } from './run-fixtures'

// Share's words are true in every outcome (#1006). A run the server keeps only as its record (stopped, then the server restarted)
// has no links: its links' routes answer 409 run_record (internal/api/links.go, refuseRunNotHeld), and the dialog says at once why
// none is made, never that a link is copied. A run it holds: the dialog says the link is copied only once one was made. Open on your
// phone answers the same: no stopped run or record offered, and a refusal in the same words. The e2e server cannot be restarted
// under a spec, so the runs are served by run-fixtures.ts and the links' routes are answered here, set before the page loads, as
// the server answers them.

/** The server's words for a run kept only as its record (internal/api/links.go, runRecordNoLink). */
const SERVER_WORDS =
  'no link can be made for this run: the server keeps it only as its record (it ended before the server last started, or was let go with the oldest runs), and a link opens only a run the server holds'
const REFUSAL = 'The server keeps this run only as its record: it ended before the server last started, or was let go with the oldest runs. A link opens only a run the server holds, so none was made. A selection the run still holds is answered on Needs you, not through a link.'
const COPIED = 'The link is copied: it opens every agent of the crew, one added later too. Revoking disconnects whoever used it.'

const fulfil = (route: Route, status: number, body: unknown) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })

/** Answers the run's links' routes, both of them, as the server answers a run kept only as its record; counts the creations asked. */
async function refuseAsARecord(page: Page, run: StubRun): Promise<{ posts: number; lists: number }> {
  const seen = { posts: 0, lists: 0 }
  await page.route(
    (u) => u.pathname === `/api/runs/${run.id}/links`,
    (route) => {
      if (route.request().method() === 'POST') seen.posts++
      else seen.lists++
      return fulfil(route, 409, { error: { code: 'run_record', message: SERVER_WORDS } })
    },
  )
  return seen
}

/** The run's page, its header drawn; then its menu's Share, the menu gone before the dialog is read. */
async function openShare(page: Page, run: StubRun) {
  await page.goto(`/runs/${run.id}`)
  const header = page.locator('[data-crew-run-header]')
  await expect(header.locator('[data-run-name]')).toHaveText(run.name)
  await header.locator('[data-run-menu]').click()
  await page.locator('[role="menuitem"][data-run-share]').click()
  await expect(page.locator('[role="menu"]')).toHaveCount(0)
  const dialog = page.getByRole('dialog', { name: `Share ${run.name}` })
  await expect(dialog).toBeVisible()
  return dialog
}

for (const theme of ['dark', 'light'] as const) {
  test(`${theme}: Share on a run kept only as its record says at once why no link is made, offers none, and never says a link is copied`, async ({ day, browser }) => {
    const run = legacyRun({ name: 'Kept as its record', record: true })
    const page = await dayPage(browser, { path: '/', theme })
    await stubInbox(page, [])
    await serveRun(page, run)
    const seen = await refuseAsARecord(page, run)
    const dialog = await openShare(page, run)
    await expect(page.locator('[data-run-record]')).toBeVisible()
    const refused = dialog.locator('[data-share-refused]')
    await expect(refused).toContainText('No link for this run')
    await expect(refused).toContainText(REFUSAL)
    await expect(dialog).toContainText('No link was made for this run.')
    await expect(dialog).not.toContainText('copied')
    await expect(dialog).not.toContainText('no such run')
    // Nothing of a link: none made, none being made, no form for another.
    for (const part of ['[data-share-created]', '[data-share-making]', '[data-another-link]', '[data-share-link]']) await expect(dialog.locator(part), part).toHaveCount(0)
    expect(seen.lists, 'the list was asked').toBeGreaterThan(0)
    expect(seen.posts, 'no link was asked for after the refusal').toBe(0)
    await inside(refused, dialog)
    // Opened again: the same answer, still no link asked for.
    await page.keyboard.press('Escape')
    await expect(dialog).toHaveCount(0)
    await page.locator('[data-crew-run-header] [data-run-menu]').click()
    await page.locator('[role="menuitem"][data-run-share]').click()
    await expect(page.locator('[role="menu"]')).toHaveCount(0)
    await expect(page.getByRole('dialog', { name: `Share ${run.name}` }).locator('[data-share-refused]')).toContainText(REFUSAL)
    expect(seen.posts).toBe(0)
    void day
  })
}

test('at 390 with touch the refusal lies inside the dialog and the page scrolls nowhere sideways', async ({ day, browser }) => {
  const run = legacyRun({ name: 'Kept as its record', record: true })
  const page = await dayPage(browser, { path: '/', width: 390, height: 844, touch: true })
  await stubInbox(page, [])
  await serveRun(page, run)
  await refuseAsARecord(page, run)
  await page.goto(`/runs/${run.id}`)
  const header = page.locator('[data-crew-run-header]')
  await expect(header.locator('[data-run-name]')).toHaveText(run.name)
  await header.locator('[data-run-menu]').tap()
  await page.locator('[role="menuitem"][data-run-share]').tap()
  const dialog = page.getByRole('dialog', { name: `Share ${run.name}` })
  const refused = dialog.locator('[data-share-refused]')
  await expect(refused).toContainText(REFUSAL)
  await inside(refused, dialog)
  await inside(dialog, page.locator('body'))
  await noSideways(page)
  void day
})

test('Share on a run the server holds says the link is copied only once one was made and copied, and never while it is made', async ({ day, browser }) => {
  const run = legacyRun({ name: 'Held and shared' })
  const page = await dayPage(browser, { path: '/' })
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])
  await stubInbox(page, [])
  await serveRun(page, run)
  // The creation is held at a gate the test opens once it has read the dialog while the link is being made.
  let open!: () => void
  const gate = new Promise<void>((r) => (open = r))
  let asked!: () => void
  const making = new Promise<void>((r) => (asked = r))
  const origin = new URL(page.url()).origin
  const link = { id: 'lnk-held-1', role: 'view', label: '', runId: run.id, createdAt: new Date().toISOString(), expiresAt: new Date(Date.now() + 7_200_000).toISOString() }
  let made = false
  await page.route(
    (u) => u.pathname === `/api/runs/${run.id}/links`,
    async (route) => {
      if (route.request().method() !== 'POST') return fulfil(route, 200, { links: made ? [{ ...link, active: 0 }] : [] })
      asked()
      await gate
      made = true
      return fulfil(route, 201, { link, token: 'tok-held-1', url: `${origin}/join/tok-held-1` })
    },
  )
  const dialog = await openShare(page, run)
  await making
  await expect(dialog.locator('[data-share-making]')).toBeVisible()
  await expect(dialog).toContainText('A link opens every agent of the crew, one added later too.')
  await expect(dialog).not.toContainText('copied')
  open()
  await expect(dialog.locator('[data-created-url]')).toHaveAttribute('title', `${origin}/join/tok-held-1`)
  await expect(dialog).toContainText(COPIED)
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(`${origin}/join/tok-held-1`)
  await expect(dialog.locator('[data-share-refused]')).toHaveCount(0)
  void day
})

test('Share whose link the server fails to make shows the failure and says nothing of a copy', async ({ day, browser }) => {
  const run = legacyRun({ name: 'Held, link refused' })
  const page = await dayPage(browser, { path: '/' })
  await stubInbox(page, [])
  await serveRun(page, run)
  await page.route(
    (u) => u.pathname === `/api/runs/${run.id}/links`,
    (route) => (route.request().method() === 'POST' ? fulfil(route, 409, { error: { code: 'too_many_links', message: 'too many links for this run' } }) : fulfil(route, 200, { links: [] })),
  )
  const dialog = await openShare(page, run)
  await expect(dialog).toContainText('too many links for this run')
  await expect(dialog).toContainText('A link opens every agent of the crew, one added later too.')
  await expect(dialog).not.toContainText('copied')
  await expect(dialog.locator('[data-share-created]')).toHaveCount(0)
  await expect(dialog.locator('[data-share-refused]')).toHaveCount(0)
  void day
})

test('Open on your phone offers no stopped run and no record, says so, and answers a refusal in the words Share does', async ({ day, browser }) => {
  const live = legacyRun({ name: 'Phone live run' })
  const stopped: StubRun = { ...legacyRun({ name: 'Phone stopped run' }), state: 'stopped', stoppedAt: new Date().toISOString() }
  const record = legacyRun({ name: 'Phone record run', record: true })
  const page = await dayPage(browser, { path: '/' })
  await stubInbox(page, [])
  await serveRun(page, [live, stopped, record])
  // The live run let go as it is picked: its link is refused as a record's.
  const seen = await refuseAsARecord(page, live)
  await page.goto('/decisions')
  await expect(page.locator('#dashboard-sidebar-main')).toBeAttached()
  await page.locator('[data-header-account]').click()
  await page.getByRole('menuitem', { name: 'Open on your phone' }).click()
  await expect(page.getByRole('menuitem')).toHaveCount(0)
  const offer = page.locator('[data-phone-offer="loopback"]')
  await expect(offer).toContainText('A phone cannot reach')
  await expect(offer.locator('[data-phone-not-offered]')).toHaveText(
    'Stopped runs are not offered here, and a run the server keeps only as its record has no links at all. A selection such a run still holds is answered on Needs you, not through a link.',
  )
  await offer.locator('[data-phone-target]').click()
  const options = page.getByRole('option')
  await expect(options.filter({ hasText: 'Run: Phone live run' })).toHaveCount(1)
  await expect(options.filter({ hasText: 'Phone stopped run' })).toHaveCount(0)
  await expect(options.filter({ hasText: 'Phone record run' })).toHaveCount(0)
  await options.filter({ hasText: 'Run: Phone live run' }).click()
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await offer.locator('[data-phone-make-link]').click()
  await expect(offer.locator('[data-phone-link-error]')).toContainText(REFUSAL)
  await expect(offer.locator('[data-phone-link]')).toHaveCount(0)
  expect(seen.posts).toBe(1)
  void day
})
