import type { Locator, Page } from '@playwright/test'
import { dayPage, expect, test, type DayPageOptions } from './fixtures'
import { inboxItem, stubInbox } from './inbox-stub'
import { inside, noOverlap, noSideways, probeColour, tabTo, uncut } from './layout'
import { ledgerRun, legacyRun, serveRun, type StubRun } from './run-fixtures'

// A run's header (UX1's PR-9, docs/plans/ux-review.md: P7, D2), on runs served from run-fixtures.ts: row one holds the name whole,
// one ticket chip and the run's actions, nothing under a button; row two, at every width, what the run is doing, its people and
// how long it has been up. Add agent and Share are in the run's one menu; Stop is neutral until its confirm.

// A crew's name at its bound, 60 characters (internal/crew: a name the server takes).
const NAME = 'Reconcile the ledger after the schema five cutover, each host'.slice(0, 60)

/** The Ledger run on a page of its own: its decision gate and its failed task in the inbox, the run served; the page at the run. */
async function ledgerPage(browser: Parameters<typeof dayPage>[0], o: DayPageOptions = {}): Promise<{ page: Page; run: StubRun; header: Locator }> {
  const run = ledgerRun({ name: NAME })
  const page = await dayPage(browser, { path: '/', ...o })
  await stubInbox(page, [inboxItem('decision', { ref: 'decision:dr-1', run, task: 'release-notes', origin: 'plan', question: 'May release-notes ship?' }), inboxItem('task', { run, task: 'migrate' })])
  await serveRun(page, run)
  await page.goto(`/runs/${run.id}`)
  const header = page.locator('[data-crew-run-header]')
  await expect(header.locator('[data-run-name]')).toHaveText(NAME)
  await page.evaluate(() => document.fonts.ready.then(() => undefined))
  return { page, run, header }
}

/** What a person must read whole, and nothing in row one under another piece; row two inside the header. */
async function fits(header: Locator): Promise<void> {
  await uncut(header.locator('[data-run-name]'))
  await noOverlap(header.locator('[data-run-row-one]'))
  await inside(header.locator('[data-run-summary]'), header)
}

test('at 1440 the name is whole and nothing in row one runs under another piece; row two lies inside the header, and still does after the uptime ticks', async ({ day, browser }) => {
  const { page, header } = await ledgerPage(browser)
  expect(NAME).toHaveLength(60)
  await fits(header)
  // The run page redraws at its 5 s tick: the uptime (in seconds, the run just started) changes, and everything is measured again.
  const uptime = header.locator('[data-run-uptime]')
  await expect(uptime).toHaveText(/^up \d+s · server · worktrees$/)
  const first = await uptime.textContent()
  await expect(uptime).not.toHaveText(first!, { timeout: 15_000 })
  await fits(header)
  expect(await uptime.evaluate((e) => getComputedStyle(e).fontFamily)).not.toMatch(/mono/i)
  void page
  void day
})

test('Add agent and Share are in the run’s menu: by key on a run of members, disabled on a managed run, and from the palette', async ({ day, browser }) => {
  const legacy = legacyRun()
  const page = await dayPage(browser, { path: '/' })
  await stubInbox(page, [])
  await serveRun(page, legacy)
  await page.goto(`/runs/${legacy.id}`)
  const header = page.locator('[data-crew-run-header]')
  await expect(header.locator('[data-run-name]')).toHaveText(legacy.name)
  // No button of the header is named for them: they are the menu's.
  await expect(header.getByRole('button', { name: 'Add agent', exact: true })).toHaveCount(0)
  await expect(header.getByRole('button', { name: 'Share', exact: true })).toHaveCount(0)
  // By key: Tab to the menu's button (its ring drawn), Enter opens it, Enter on Add agent opens the slideover.
  const menuButton = header.locator('[data-run-menu]')
  await expect(menuButton).toHaveAttribute('aria-label', 'More for this run')
  await tabTo(page, menuButton, 12, header.locator('[data-run-name]'))
  await page.keyboard.press('Enter')
  const add = page.locator('[data-run-add]')
  await expect(add).toBeFocused()
  await page.keyboard.press('Enter')
  const dialog = page.getByRole('dialog', { name: 'Add an agent' })
  await expect(dialog).toBeVisible()
  // The menu has closed and is gone before the slideover's field is read (a menu that has closed is not gone).
  await expect(page.locator('[role="menu"]')).toHaveCount(0)
  await expect(dialog.locator('[data-crew-member]').getByPlaceholder('name', { exact: true })).toHaveValue(/\S/)
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)
  // The palette's rows open the same two.
  const palette = page.locator('[data-palette]')
  for (const [key, opened] of [
    ['page:add', page.getByRole('dialog', { name: 'Add an agent' })],
    ['page:share', page.getByRole('dialog', { name: `Share ${legacy.name}` })],
  ] as const) {
    await page.keyboard.press('Alt+/')
    await expect(palette).toBeVisible()
    await palette.locator(`[data-palette-row="${key}"]`).click()
    await expect(opened, key).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(opened, key).toHaveCount(0)
  }

  // A managed run: Add agent is in the menu and disabled, as is the palette's row.
  const { page: managed, header: mh } = await ledgerPage(browser)
  await mh.locator('[data-run-menu]').click()
  await expect(managed.locator('[data-run-add]')).toHaveAttribute('data-disabled', '')
  await expect(managed.locator('[data-run-share]')).not.toHaveAttribute('data-disabled', '')
  await managed.keyboard.press('Escape')
  await expect(managed.locator('[role="menu"]')).toHaveCount(0)
  await managed.keyboard.press('Alt+/')
  await expect(managed.locator('[data-palette-row="page:add"]')).toHaveAttribute('aria-disabled', 'true')
  void day
})

test('at 390 with touch the name, Stop and the menu are drawn apart, row two wraps inside the window, and a tap on the menu and on Share opens the share dialog', async ({ day, browser }) => {
  const { page, run, header } = await ledgerPage(browser, { width: 390, height: 844, touch: true })
  for (const part of ['[data-run-name]', '[data-run-stop]', '[data-run-menu]']) await expect(header.locator(part), part).toBeVisible()
  await noOverlap(header.locator('[data-run-row-one]'))
  await inside(header.locator('[data-run-summary]'), page.locator('body'))
  await noSideways(page)
  await header.locator('[data-run-menu]').tap()
  await page.locator('[data-run-share]').tap()
  await expect(page.getByRole('dialog', { name: `Share ${run.name}` })).toBeVisible()
  void day
})

/** The colours a computed style draws an element with, each as probeColour gives it: its text, its background, its borders, and the
 * colours of its shadows (the ring of an outline button). */
async function drawnColours(l: Locator): Promise<string[]> {
  const page = l.page()
  const raw = await l.evaluate((e) => {
    const s = getComputedStyle(e)
    const shadows = s.boxShadow === 'none' ? [] : (s.boxShadow.match(/(?:rgba?|oklch|oklab|lab|lch|color|hsla?)\([^)]*\)/g) ?? [])
    return [s.color, s.backgroundColor, s.borderTopColor, s.borderRightColor, s.borderBottomColor, s.borderLeftColor, ...shadows]
  })
  const out: string[] = []
  for (const c of raw) if (!/rgba\(0, 0, 0, 0\)|transparent/.test(c)) out.push(await probeColour(page, c))
  return out
}

for (const theme of ['dark', 'light'] as const) {
  test(`in the ${theme} theme Stop at rest is drawn in no error colour`, async ({ day, browser }) => {
    const { page, header } = await ledgerPage(browser, { theme })
    const stop = header.locator('[data-run-stop]')
    await expect(stop).toBeVisible()
    const error = await probeColour(page, 'var(--ui-error)')
    const drawn = await drawnColours(stop)
    expect(drawn.length, "Stop's colours").toBeGreaterThan(0)
    expect(drawn, `Stop at rest, against --ui-error ${error}`).not.toContain(error)
    void day
  })
}
