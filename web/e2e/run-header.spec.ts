import type { Locator, Page } from '@playwright/test'
import { dayPage, expect, test, type DayPageOptions } from './fixtures'
import { inboxItem, stubInbox } from './inbox-stub'
import { inside, noOverlap, noSideways, probeColour, tabTo, uncut } from './layout'
import { LEDGER_HOLD, ledgerRun, legacyRun, runInfo, serveRun, type StubRun } from './run-fixtures'

// A run's header (UX1's PR-9, docs/plans/ux-review.md: P7, D2), on runs served from run-fixtures.ts: row one holds the name whole,
// one ticket chip and the run's actions, nothing under a button; row two, at every width, what the run is doing, its people and
// how long it has been up. Add agent and Share are in the run's one menu; Stop is neutral until its confirm.

// A crew's name at its bound, 60 characters (internal/crew: a name the server takes).
const NAME = 'Reconcile the ledger after the schema five cutover, each host'.slice(0, 60)

/** The Ledger run on a page of its own: its decision gate and its failed task in the inbox, the run served; the page at the run. */
async function ledgerPage(browser: Parameters<typeof dayPage>[0], o: DayPageOptions & { soak?: string } = {}): Promise<{ page: Page; run: StubRun; header: Locator }> {
  const run = ledgerRun({ name: NAME, ...(o.soak ? { soak: o.soak } : {}) })
  const page = await dayPage(browser, { path: '/', ...o })
  // The decision is the one that holds release-notes (LEDGER_HOLD), an id the day's server never issues: the day's dr-1 is the
  // clarification a real session mirrors once the day's waiting part is built, and the one list counted the stub's item as that
  // session's question, not this run's.
  await stubInbox(page, [inboxItem('decision', { ref: `decision:${LEDGER_HOLD}`, run, task: 'release-notes', origin: 'plan', question: 'May release-notes ship?' }), inboxItem('task', { run, task: 'migrate' })])
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
  const add = page.locator('[role="menuitem"][data-run-add]')
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
  await expect(managed.locator('[role="menuitem"][data-run-add]')).toHaveAttribute('data-disabled', '')
  await expect(managed.locator('[role="menuitem"][data-run-share]')).not.toHaveAttribute('data-disabled', '')
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
  await page.locator('[role="menuitem"][data-run-share]').tap()
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

// What the run is doing (UX1 PR-10): row two's bar counts the run's tasks by part, and its one sentence says them with the run's items
// of the one list ("N need you"). soak's attempt runs in the day's working session, so it is working.
for (const theme of ['dark', 'light'] as const) {
  test(`in the ${theme} theme the bar counts the tasks by part, the sentence says the run's two items, and the held tasks are amber`, async ({ day, browser }) => {
    const n = await day.part('states')
    const { page, header } = await ledgerPage(browser, { theme, soak: n['yard-tile-resize']! })
    const bar = header.locator('[data-run-bar]')
    const counts = async () => bar.locator('[data-part]').evaluateAll((els) => els.map((e) => `${e.getAttribute('data-part')} ${e.getAttribute('data-count')}`))
    await expect.poll(counts, { message: "the bar's parts" }).toEqual(['done 2', 'working 1', 'checking 1', 'needs 2'])
    const progress = header.locator('[data-run-progress]')
    // The run's state in the Crews pages' words (C2), its counts after it.
    await expect(progress.locator('[data-run-phase] [data-status-word]')).toHaveText('Needs you')
    await expect(progress.locator('[data-run-detail]')).toHaveText('2 of 6 done · 1 working · 1 checking · 2 need you')
    // The run's number is the one list's for this run: the gate's decision and the failed task, one item each.
    await expect(progress).toHaveAttribute('data-needs', '2')
    await expect(bar.locator('[data-part="needs"]')).toHaveAttribute('title', '2 tasks held')
    const fill = await bar.locator('[data-part="needs"]').evaluate((e) => getComputedStyle(e).backgroundColor)
    expect(await probeColour(page, fill), 'the held tasks are drawn amber').toBe(await probeColour(page, 'var(--ui-warning)'))
  })
}

test('one gate holding two tasks: the sentence says one item, the bar two tasks held', async ({ day, browser }) => {
  void day
  const run = runInfo({ tasks: 6, states: ['ready', 'ready', 'succeeded', 'succeeded', 'succeeded', 'succeeded'] })
  const gate = inboxItem('decision', { run, task: 't01', origin: 'plan' })
  const hold = [{ id: gate.ref.slice('decision:'.length), type: 'approval', state: 'open', question: gate.question }] as StubRun['tasks'][number]['holds']
  run.tasks[0]!.holds = hold
  run.tasks[1]!.holds = hold
  const page = await dayPage(browser, { path: '/' })
  await stubInbox(page, [gate])
  await serveRun(page, run)
  await page.goto(`/runs/${run.id}`)
  const header = page.locator('[data-crew-run-header]')
  await expect(header.locator('[data-run-bar] [data-part="needs"]')).toHaveAttribute('data-count', '2')
  await expect(header.locator('[data-run-progress]')).toHaveAttribute('data-needs', '1')
  await expect(header.locator('[data-run-progress] [data-run-detail]')).toHaveText('4 of 6 done · 1 needs you')
})

test("at 390 with touch a run of members says its members' sentence and draws no bar", async ({ day, browser }) => {
  void day
  const legacy = legacyRun()
  const page = await dayPage(browser, { path: '/', width: 390, height: 844, touch: true })
  await stubInbox(page, [])
  await serveRun(page, legacy)
  await page.goto(`/runs/${legacy.id}`)
  const header = page.locator('[data-crew-run-header]')
  // Its members run in sessions no server holds: each is idle as the run last read it (a session's "running", C2).
  await expect(header.locator('[data-run-progress] [data-run-phase] [data-status-word]')).toHaveText('Running')
  await expect(header.locator('[data-run-progress] [data-run-detail]')).toHaveText('4 idle')
  await expect(header.locator('[data-run-bar]')).toHaveCount(0)
  await inside(header.locator('[data-run-summary]'), page.locator('body'))
  await noSideways(page)
})

test('in a low window (1440 x 360) row two gives way and the bar and the sentence stay, in row one', async ({ day, browser }) => {
  const { page, header } = await ledgerPage(browser, { height: 360 })
  await expect(header.locator('[data-run-summary]')).toBeHidden()
  const low = header.locator('[data-run-progress-low]')
  await expect(low).toBeVisible()
  await expect(low).toContainText(/^Needs you\d of 6 done · /)
  await noOverlap(header.locator('[data-run-row-one]'))
  // Back to a tall window: row two again, the sentence there alone.
  await page.setViewportSize({ width: 1440, height: 900 })
  await expect(header.locator('[data-run-summary]')).toBeVisible()
  await expect(low).toBeHidden()
  void day
})
