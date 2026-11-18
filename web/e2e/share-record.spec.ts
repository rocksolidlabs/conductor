import type { Page, Route } from '@playwright/test'
import { dayPage, expect, test } from './fixtures'
import { stubInbox } from './inbox-stub'
import { inside, MEASURE_WAIT_MS, noSideways } from './layout'
import { legacyRun, serveRun, type StubRun } from './run-fixtures'

// Share's words are true in every outcome (#1006). A run the server keeps only as its record (stopped, then the server restarted)
// has no links: its links' routes answer 409 run_record (internal/api/links.go, refuseRunNotHeld), and the dialog says at once why
// none is made, never that a link is copied. A run it holds: the dialog says the link is copied only while it shows a link it made
// and copied, and no more once that link is revoked. Open on your phone answers the same: no stopped run or record offered, and a
// refusal in the same words. The e2e server cannot be restarted under a spec, so the runs are served by run-fixtures.ts and the
// links' routes are answered here, set before the page loads, as the server answers them.

/** The cap of one expect (playwright.config.ts) and of one measure (layout.ts): what each wait below may take. */
const EXPECT_MS = 15_000

/**
 * Raises the running test's timeout by the caps of the waits a step is about to make (palette-rows.spec.ts's budget): the test's
 * timeout is their sum, with the config's 120 s above it for the steps between them (a page's load, a click).
 */
function budget(...caps: number[]) {
  const info = test.info()
  info.setTimeout(info.timeout + caps.reduce((a, b) => a + b, 0))
}
/** `n` waits of an expect's cap. */
const waits = (n: number) => n * EXPECT_MS

/** The server's words for a run kept only as its record (internal/api/links.go, runRecordNoLink). */
const SERVER_WORDS =
  'no link can be made for this run: the server keeps it only as its record (it ended before the server last started, or was let go with the oldest runs), and a link opens only a run the server holds'
const REFUSAL = 'The server keeps this run only as its record: it ended before the server last started, or was let go with the oldest runs. A link opens only a run the server holds, so none was made. A selection the run still holds is answered on Needs you, not through a link.'
const COPIED = 'The link is copied: it opens every agent of the crew, one added later too. Revoking disconnects whoever used it.'
const OPENS = 'A link opens every agent of the crew, one added later too.'

const fulfil = (route: Route, status: number, body: unknown) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })

/** A page of the day with the runs served (run-fixtures.ts) and the inbox empty: its own waits budgeted (the layout drawn, its theme). */
async function runsPage(browser: Parameters<typeof dayPage>[0], runs: StubRun | StubRun[], o: Parameters<typeof dayPage>[1] = {}): Promise<Page> {
  budget(waits(2))
  const page = await dayPage(browser, { path: '/', ...o })
  await stubInbox(page, [])
  await serveRun(page, runs)
  return page
}

/**
 * Answers the run's links' routes, both of them, as the server answers a run kept only as its record. Each request is counted as the
 * route takes it, in the order the page sent them: a creation counted before a list the page asked after it.
 */
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
  budget(waits(4))
  await page.goto(`/runs/${run.id}`)
  const header = page.locator('[data-crew-run-header]')
  await expect(header.locator('[data-run-name]')).toHaveText(run.name)
  return shareFromMenu(page, run)
}

/** Share from the run's menu on a page already at the run, the menu gone before the dialog is read. */
async function shareFromMenu(page: Page, run: StubRun) {
  budget(waits(2))
  await page.locator('[data-crew-run-header] [data-run-menu]').click()
  await page.locator('[role="menuitem"][data-run-share]').click()
  await expect(page.locator('[role="menu"]')).toHaveCount(0)
  const dialog = page.getByRole('dialog', { name: `Share ${run.name}` })
  await expect(dialog).toBeVisible()
  return dialog
}

for (const theme of ['dark', 'light'] as const) {
  test(`${theme}: Share on a run kept only as its record says at once why no link is made, offers none, and never says a link is copied`, async ({ day, browser }) => {
    const run = legacyRun({ name: 'Kept as its record', record: true })
    const page = await runsPage(browser, run, { theme })
    const seen = await refuseAsARecord(page, run)
    const dialog = await openShare(page, run)
    budget(waits(7), MEASURE_WAIT_MS)
    await expect(page.locator('[data-run-record]')).toBeVisible()
    const refused = dialog.locator('[data-share-refused]')
    await expect(refused).toContainText('No link for this run')
    await expect(refused).toContainText(REFUSAL)
    await expect(dialog).toContainText('No link was made for this run.')
    await expect(dialog).not.toContainText('copied')
    await expect(dialog).not.toContainText('no such run')
    await inside(refused, dialog)
    // Nothing of a link: none made, none being made, no form for another.
    budget(waits(4))
    for (const part of ['[data-share-created]', '[data-share-making]', '[data-another-link]', '[data-share-link]']) await expect(dialog.locator(part), part).toHaveCount(0)
    // Closed and opened again: the same answer. The second opening's list is taken after anything the first opening sent after its
    // refusal (the route counts in the order the page sent): a creation the first opening asked for is counted by then.
    budget(waits(3))
    await page.keyboard.press('Escape')
    await expect(dialog).toHaveCount(0)
    const again = await shareFromMenu(page, run)
    budget(waits(2))
    await expect(again.locator('[data-share-refused]')).toContainText(REFUSAL)
    await expect.poll(() => seen.lists, { message: 'the second opening asked for the list' }).toBeGreaterThanOrEqual(2)
    expect(seen.posts, 'no link was asked for after the refusal').toBe(0)
    void day
  })
}

test('at 390 with touch the refusal lies inside the dialog and the page scrolls nowhere sideways', async ({ day, browser }) => {
  const run = legacyRun({ name: 'Kept as its record', record: true })
  const page = await runsPage(browser, run, { width: 390, height: 844, touch: true })
  await refuseAsARecord(page, run)
  budget(waits(2), 3 * MEASURE_WAIT_MS)
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

test('Share on a run the server holds says the link is copied only while it shows a link it made and copied: not while it is made, nor once it is revoked', async ({ day, browser }) => {
  const run = legacyRun({ name: 'Held and shared' })
  const page = await runsPage(browser, run)
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])
  // The creation is held at a gate the test opens once it has read the dialog while the link is being made.
  let open!: () => void
  const gate = new Promise<void>((r) => (open = r))
  let asked!: () => void
  const making = new Promise<void>((r) => (asked = r))
  const origin = new URL(page.url()).origin
  const link = { id: 'lnk-held-1', role: 'view', label: '', runId: run.id, createdAt: new Date().toISOString(), expiresAt: new Date(Date.now() + 7_200_000).toISOString() }
  let made = false
  let revoked = 0
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
  // Its revoke, as the server answers one: 204, and the link listed no more.
  await page.route(
    (u) => u.pathname === `/api/runs/${run.id}/links/${link.id}`,
    (route) => {
      if (route.request().method() !== 'DELETE') return route.fallback()
      revoked++
      made = false
      return route.fulfill({ status: 204 })
    },
  )
  const dialog = await openShare(page, run)
  // The gate holds the creation until the test has looked: the page has asked, and nothing is made yet.
  budget(waits(3))
  await making
  await expect(dialog.locator('[data-share-making]')).toBeVisible()
  await expect(dialog).toContainText(OPENS)
  await expect(dialog).not.toContainText('copied')
  open()
  budget(waits(4))
  await expect(dialog.locator('[data-created-url]')).toHaveAttribute('title', `${origin}/join/tok-held-1`)
  await expect(dialog).toContainText(COPIED)
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(`${origin}/join/tok-held-1`)
  await expect(dialog.locator('[data-share-refused]')).toHaveCount(0)
  // Revoked from the list: the link it made is shown no more, and the line says nothing of a copy.
  budget(waits(5))
  await dialog.locator('[data-share-link]').getByRole('button', { name: 'Revoke' }).click()
  await expect.poll(() => revoked, { message: 'the revoke reached the server' }).toBe(1)
  await expect(dialog.locator('[data-share-created]')).toHaveCount(0)
  await expect(dialog.locator('[data-share-link]')).toHaveCount(0)
  await expect(dialog).toContainText(OPENS)
  await expect(dialog).not.toContainText('copied')
  void day
})

test('Share whose link the server fails to make shows the failure and says nothing of a copy', async ({ day, browser }) => {
  const run = legacyRun({ name: 'Held, link refused' })
  const page = await runsPage(browser, run)
  await page.route(
    (u) => u.pathname === `/api/runs/${run.id}/links`,
    (route) => (route.request().method() === 'POST' ? fulfil(route, 409, { error: { code: 'too_many_links', message: 'too many links for this run' } }) : fulfil(route, 200, { links: [] })),
  )
  const dialog = await openShare(page, run)
  budget(waits(5))
  await expect(dialog).toContainText('too many links for this run')
  await expect(dialog).toContainText(OPENS)
  await expect(dialog).not.toContainText('copied')
  await expect(dialog.locator('[data-share-created]')).toHaveCount(0)
  await expect(dialog.locator('[data-share-refused]')).toHaveCount(0)
  void day
})

test('Open on your phone offers no stopped run and no record, says so, and answers a refusal in the words Share does', async ({ day, browser }) => {
  const live = legacyRun({ name: 'Phone live run' })
  const stopped: StubRun = { ...legacyRun({ name: 'Phone stopped run' }), state: 'stopped', stoppedAt: new Date().toISOString() }
  const record = legacyRun({ name: 'Phone record run', record: true })
  const page = await runsPage(browser, [live, stopped, record])
  // The live run let go as it is picked: its link is refused as a record's.
  const seen = await refuseAsARecord(page, live)
  budget(waits(4))
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
  budget(waits(5))
  await offer.locator('[data-phone-target]').click()
  const options = page.getByRole('option')
  await expect(options.filter({ hasText: 'Run: Phone live run' })).toHaveCount(1)
  await expect(options.filter({ hasText: 'Phone stopped run' })).toHaveCount(0)
  await expect(options.filter({ hasText: 'Phone record run' })).toHaveCount(0)
  await options.filter({ hasText: 'Run: Phone live run' }).click()
  await expect(page.getByRole('listbox')).toHaveCount(0)
  budget(waits(3))
  await offer.locator('[data-phone-make-link]').click()
  await expect(offer.locator('[data-phone-link-error]')).toContainText(REFUSAL)
  await expect(offer.locator('[data-phone-link]')).toHaveCount(0)
  await expect.poll(() => seen.posts).toBe(1)
  void day
})
