import type { Page } from '@playwright/test'
import { dayPage, expect, test } from './fixtures'
import { TICKETS } from './day'
import { aligned, inside, noOverlap, noSideways, rowsInView, topAtMost, uncut } from './layout'

// One toolbar on a Dispatch project, and columns that line up (UX1's PR-12, docs/plans/ux-review.md): the description as one line, then
// one toolbar (the views, the search, Filter and its chips, the count, Views, the sort), then the tickets, each row's milestone,
// priority, owner, assignees and age in a column of its own. On the seeded day's `dispatch` part (CND's fourteen tickets), read-only:
// filters, chips, the search and the selection change only the address and the page, never the server.

const ROWS = '[data-ticket-row]'
/** The five cells every row draws, empty or not. */
const CELLS = ['[data-row-milestone]', '[data-row-priority]', '[data-row-owner]', '[data-row-assignees]', '[data-row-updated]']

/** The project's page once its list shows the count the day made: a page not yet drawn passes every "hidden". */
async function project(browser: Parameters<typeof dayPage>[0], o: Parameters<typeof dayPage>[1] = {}, query = ''): Promise<Page> {
  const page = await dayPage(browser, { path: `/dispatch/CND${query}`, ...o })
  if (!query) await expect(page.locator('[data-list-total]')).toHaveAttribute('data-count', String(TICKETS.length))
  else await expect(page.locator('[data-list-total]')).toBeVisible()
  return page
}

/** The list's scroller: the list's parent (the page's body below the toolbar). */
function scroller(page: Page) {
  return page.locator('[data-ticket-list]').locator('xpath=..')
}

/** Each of the five cells counted on the rows: with data-empty and without. */
async function emptiness(page: Page): Promise<Record<string, [number, number]>> {
  return page.locator(ROWS).evaluateAll(
    (rows, cells) => Object.fromEntries(cells.map((c) => [c, [rows.filter((r) => r.querySelector(c)?.hasAttribute('data-empty')).length, rows.filter((r) => r.querySelector(c) && !r.querySelector(c)!.hasAttribute('data-empty')).length]])),
    CELLS,
  )
}

test.describe('a Dispatch project at 1440 x 900', () => {
  test('the first ticket starts under one line and one toolbar, the fourteen fit, and the filters wait behind Filter', async ({ day, browser }) => {
    await day.part('dispatch')
    const page = await project(browser)
    await topAtMost(page.locator(ROWS), 160)
    expect(await rowsInView(scroller(page), ROWS)).toBe(TICKETS.length)
    const line = await page.locator('[data-project-description-line]').boundingBox()
    expect(line!.height).toBeLessThanOrEqual(24)
    const toolbar = page.locator('[data-dispatch-toolbar]')
    expect((await toolbar.boundingBox())!.height).toBeLessThanOrEqual(40)
    await noOverlap(toolbar)
    await expect(page.locator('[data-filter-status]')).toHaveCount(0)
    await page.locator('[data-filter-open]').click()
    await expect(page.locator('[data-filter-popover] [data-filter-status]')).toBeVisible()
  })

  test("each row's milestone, priority, owner, assignees and age line up, before a selection and after it", async ({ day, browser }) => {
    await day.part('dispatch')
    const page = await project(browser)
    // The day's rows are not all alike: a milestone, an owner and assignees are drawn empty on some rows and not on others.
    const counts = await emptiness(page)
    for (const c of ['[data-row-milestone]', '[data-row-owner]', '[data-row-assignees]']) {
      expect(counts[c]![0], `${c} drawn empty on a row`).toBeGreaterThan(0)
      expect(counts[c]![1], `${c} drawn full on a row`).toBeGreaterThan(0)
    }
    for (const c of CELLS) expect((await aligned(page.locator(ROWS), c)).got).toHaveLength(TICKETS.length)
    // A selection widens the select track of every row at once: checked again after the redraw it causes.
    await expect(page.locator('#dashboard-sidebar-main')).toBeVisible()
    await page.keyboard.press('j')
    await expect(page.locator(ROWS).first()).toBeFocused()
    await page.keyboard.press('x')
    await expect(page.locator('[data-selection-bar]')).toBeVisible()
    for (const c of [...CELLS, '[data-row-select]']) expect((await aligned(page.locator(ROWS), c)).got).toHaveLength(TICKETS.length)
  })
})

test('at 1280 x 720 twelve rows fit and the toolbar is one row', async ({ day, browser }) => {
  await day.part('dispatch')
  const page = await project(browser, { width: 1280, height: 720 })
  expect(await rowsInView(scroller(page), ROWS)).toBeGreaterThanOrEqual(12)
  expect((await page.locator('[data-dispatch-toolbar]').boundingBox())!.height).toBeLessThanOrEqual(40)
})

test("on a phone (390 x 844, touch) a row's second line lines up its priority and age, nothing scrolls sideways, and Filter's popover fits", async ({ day, browser }) => {
  await day.part('dispatch')
  const page = await project(browser, { width: 390, height: 844, touch: true })
  for (const c of ['[data-row-priority]', '[data-row-updated]']) await aligned(page.locator(ROWS), c)
  const above = await page.locator(ROWS).evaluateAll((rows) => rows.filter((r) => r.querySelector('[data-row-title]')!.getBoundingClientRect().bottom <= r.querySelector('[data-row-updated]')!.getBoundingClientRect().top + 0.5).length)
  expect(above).toBe(TICKETS.length)
  await noSideways(page)
  await uncut(page.locator('[data-list-total]'))
  await page.locator('[data-filter-open]').tap()
  await inside(page.locator('[data-filter-popover]'), page.locator('body'))
})

test('Filter and its chips: a status picked shows its chip and number, its x takes it out; the search, a parent and deleted show as chips', async ({ day, browser }) => {
  await day.part('dispatch')
  const page = await project(browser)
  const total = page.locator('[data-list-total]')
  const open = page.locator('[data-filter-open]')
  await expect(open).toHaveText('Filter')
  await open.click()
  await page.locator('[data-filter-popover] [data-filter-status]').click()
  await page.getByRole('option', { name: 'In progress', exact: true }).click()
  // A multiple select's menu stays open after a pick: Escape closes it, and once it is gone, Escape closes the popover.
  await page.keyboard.press('Escape')
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await page.keyboard.press('Escape')
  await expect(page.locator('[data-filter-popover]')).toHaveCount(0)
  await expect(page).toHaveURL(/status=in_progress/)
  const chip = page.locator('[data-filter-chip="status:in_progress"]')
  await expect(chip).toHaveText('Status: In progress')
  await expect(open).toHaveText('Filter · 1')
  await expect(open).toHaveAttribute('data-count', '1')
  await expect(total).toHaveAttribute('data-count', String(TICKETS.filter((t) => t.status === 'in_progress').length))
  await chip.locator('[data-filter-chip-remove]').click()
  await expect(page).not.toHaveURL(/status=/)
  await expect(total).toHaveAttribute('data-count', String(TICKETS.length))
  await expect(page.locator('[data-filter-chip]')).toHaveCount(0)

  // The search: its chip once the address has it (waited on the address, not on the pause); Filter's number leaves it out.
  await page.locator('[data-filter-search]').fill('login')
  await expect(page).toHaveURL(/q=login/)
  await expect(page.locator('[data-filter-chip="q:login"]')).toHaveText('Search: login')
  await expect(open).toHaveText('Filter')

  // A parent and deleted, which only the address sets, are on screen and counted.
  await page.goto('/dispatch/CND?parent=CND-1&deleted=1')
  await expect(page.locator('[data-filter-chip="parent:CND-1"]')).toHaveText('Parent: CND-1')
  await expect(page.locator('[data-filter-chip="deleted:1"]')).toHaveText('Deleted too')
  await expect(open).toHaveText('Filter · 2')
})

test("the page's keys pause while Filter's popover is open, and come back once it is gone", async ({ day, browser }) => {
  await day.part('dispatch')
  const page = await project(browser)
  await page.locator('[data-filter-open]').click()
  await expect(page.locator('[data-filter-popover]')).toBeVisible()
  await page.keyboard.press('j')
  await expect(page.locator(`${ROWS}:focus`)).toHaveCount(0)
  await page.keyboard.press('Escape')
  await expect(page.locator('[data-filter-popover]')).toHaveCount(0)
  await page.keyboard.press('j')
  await expect(page.locator(ROWS).first()).toBeFocused()
})
