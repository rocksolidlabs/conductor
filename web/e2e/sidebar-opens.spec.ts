import type { Locator, Page } from '@playwright/test'
import { dayPage, expect, test } from './fixtures'
import { inside, uncut } from './layout'

// The sidebar opens at a usable width (#1022): 280 px, where every row of the day reads whole, its question and its choices in the
// row, the filter's words whole, the header and the pages' tiles whole, at the desktop app's common window sizes and a large one.
// Closing it to the rail and opening it again, by any way there is, brings it back at that width; a width a person chose and the
// widths earlier versions saved still open sensibly, never narrower than the minimum, 256 px. The numbers are written here, never
// read from the app, so that a change of them fails these tests.

const OPEN = 280
const MIN = 256

const root = (page: Page) => page.locator('#dashboard-sidebar-main')
const width = (page: Page) => root(page).evaluate((el) => el.getBoundingClientRect().width)
const list = (page: Page) => page.locator('[data-session-list="sidebar"]')
const handle = (page: Page) => page.locator('[role="separator"][aria-controls="dashboard-sidebar-main"]')
const saved = (page: Page, key = 'conductor.sidebar.width') => page.evaluate((k) => localStorage.getItem(k), key)

/** Presses the handle and moves to `x`, then lets go. */
async function drag(page: Page, x: number) {
  const box = (await handle(page).boundingBox())!
  const y = box.y + box.height / 2
  await page.mouse.move(box.x + box.width / 2, y)
  await page.mouse.down()
  await page.mouse.move(x, y, { steps: 6 })
  await page.mouse.up()
}

/** The full sidebar drawn, its list shown, at `px`. */
async function fullAt(page: Page, px: number, how: string) {
  await expect(list(page), how).toBeVisible()
  await expect.poll(() => width(page), { message: `the sidebar's width ${how}` }).toBeCloseTo(px, 0)
}

/** The rail drawn, its expand button there. */
async function onRail(page: Page) {
  await expect(page.locator('[data-rail]')).toBeVisible()
  await expect(page.locator('[data-rail-expand]')).toBeVisible()
}

/** Each of `l`'s texts whole, each brought into the list's view first (a wide list's rows are taller than the window holds). */
async function wholeEach(l: Locator) {
  const n = await l.count()
  expect(n, `${l}: at least one`).toBeGreaterThan(0)
  for (let i = 0; i < n; i++) {
    await l.nth(i).scrollIntoViewIfNeeded()
    await uncut(l.nth(i))
  }
}

/** The filter's placeholder fits its field: its text's width, set in the field's font, within the field's content box. */
async function placeholderWhole(field: Locator) {
  const m = await field.evaluate((el: HTMLInputElement) => {
    const cs = getComputedStyle(el)
    const c = document.createElement('canvas').getContext('2d')!
    c.font = `${cs.fontStyle} ${cs.fontWeight} ${cs.fontSize} ${cs.fontFamily}`
    return { text: c.measureText(el.placeholder).width, room: el.clientWidth - Number.parseFloat(cs.paddingLeft) - Number.parseFloat(cs.paddingRight) }
  })
  expect(m.text, `the filter's words (${m.text.toFixed(1)} px) in its field (${m.room.toFixed(1)} px)`).toBeLessThanOrEqual(m.room)
}

for (const [w, h] of [
  [1280, 800],
  [1440, 900],
  [1920, 1080],
] as const) {
  test(`opened with nothing saved, at ${w}×${h}, the sidebar is 280 px and every row, the filter, the header and the tiles read whole; a row's choices are in the row`, async ({ day, browser }) => {
    const waiting = await day.part('waiting')
    await day.part('runs')
    const page = await dayPage(browser, { width: w, height: h, path: '/crews' })
    await fullAt(page, OPEN, 'as it opens')
    const side = root(page)
    // The header, Launch agent and the filter.
    await uncut(side.locator('[data-sidebar-name]'))
    await inside(side.locator('[data-header-account]'), side)
    await uncut(side.getByRole('button', { name: /Launch agent/ }).first().locator('[data-slot="label"]'))
    await placeholderWhole(side.getByPlaceholder('Filter sessions, runs, people'))
    // A wide list: a row's question and its choices are in the row, each choice whole and inside the sidebar.
    await expect(list(page)).toHaveAttribute('data-list-width', 'wide')
    const asker = list(page).locator(`[data-sidebar-row="s:${waiting['proc-race-tests']}"]`)
    await expect(asker.locator('[data-row-name]')).toHaveText('proc-race-tests')
    await expect(asker.locator('[data-row-prompt]')).not.toHaveAttribute('data-row-clamped', '')
    const choices = asker.locator('[data-row-choice]')
    await expect(choices.first()).toBeVisible()
    await choices.first().scrollIntoViewIfNeeded()
    await uncut(choices.locator('[data-slot="label"]'))
    await inside(choices, side)
    // Each row names itself whole: the loose asker, a run's members ("clarifier · naming #1"), a decision's question.
    await wholeEach(asker.locator('[data-row-name]'))
    const paging = list(page).locator(`[data-sidebar-run-block="${waiting['Paging spike']}"]`)
    await wholeEach(paging.locator('[data-row-name]'))
    await wholeEach(list(page).locator('[data-item-title]').filter({ hasText: 'May the pairing ship?' }))
    // A one-agent run's line, and the line that counts a run's other members.
    await wholeEach(list(page).locator(`[data-sidebar-run-subtitle="${waiting['Token pairing']}"]`))
    await wholeEach(paging.locator('[data-run-more] span'))
    // The pages' tiles.
    await uncut(page.getByRole('navigation', { name: 'Pages' }).locator('[data-nav-label]'))
    expect(await saved(page), 'nothing saved by opening').toBeNull()
  })
}

test('closed to the rail and opened again, by every way there is, the sidebar comes back at 280 px', async ({ page }) => {
  await page.goto('/crews')
  await fullAt(page, OPEN, 'as it opens')
  // Ctrl+B, both ways.
  await page.keyboard.press('Control+b')
  await onRail(page)
  await expect.poll(() => width(page)).toBe(64)
  await page.keyboard.press('Control+b')
  await fullAt(page, OPEN, 'after Ctrl+B twice')
  // Your menu's collapse, then the rail's expand button.
  await page.locator('[data-header-account]').click()
  await page.getByRole('menuitem', { name: /Collapse to the rail/ }).click()
  await onRail(page)
  await page.locator('[data-rail-expand]').click()
  await fullAt(page, OPEN, "after your menu's collapse and the rail's button")
  // A drag on to the window's edge passes the minimum before it gives the rail: the rail's button brings the sidebar back at the
  // minimum for the visit (Nuxt UI holds the width the drag passed), and nothing is saved by it, so a reload opens it at 280 px.
  await drag(page, 1)
  await onRail(page)
  await page.locator('[data-rail-expand]').click()
  await fullAt(page, MIN, 'after a drag to the rail and back')
  expect(await saved(page), 'a drag that ended on the rail saves no width').toBeNull()
  // Reloaded while on the rail, then opened.
  await page.keyboard.press('Control+b')
  await onRail(page)
  await page.reload()
  await onRail(page)
  await page.locator('[data-rail-expand]').click()
  await fullAt(page, OPEN, 'after a reload on the rail')
  // A window made narrow (the bottom bar in its place) and wide again, as the desktop app's window restored or maximised.
  await page.setViewportSize({ width: 800, height: 900 })
  await expect(page.locator('[data-bottom-bar]')).toBeVisible()
  await expect(root(page)).toBeHidden()
  await page.setViewportSize({ width: 1440, height: 900 })
  await fullAt(page, OPEN, 'after the window was narrow')
  // Opened in a narrow window and made wide.
  await page.setViewportSize({ width: 800, height: 900 })
  await page.reload()
  await expect(page.locator('[data-bottom-bar]')).toBeVisible()
  await page.setViewportSize({ width: 1920, height: 1080 })
  await fullAt(page, OPEN, 'opened narrow, then made wide')
})

test("a drag's width is kept and stops at 256 px; the widths earlier versions saved open sensibly; the rail and the phone's bar are as they were", async ({ page }) => {
  await page.goto('/crews')
  await fullAt(page, OPEN, 'as it opens')
  // A drag wider: kept over the rail and back and over a reload.
  await drag(page, 330)
  await fullAt(page, 330, 'dragged wider')
  await expect.poll(() => saved(page)).toBe('20.63')
  await page.keyboard.press('Control+b')
  await onRail(page)
  await page.locator('[data-rail-expand]').click()
  await fullAt(page, 330, 'the rail and back')
  await page.reload()
  await fullAt(page, 330, 'after a reload')
  // A drag narrower stops at the minimum, and that choice is kept too.
  await drag(page, 120)
  await fullAt(page, MIN, 'dragged as narrow as it goes')
  await expect.poll(() => saved(page)).toBe('16')
  await page.reload()
  await fullAt(page, MIN, 'the minimum after a reload')
  // A double-click on the handle: back to the width it opens at.
  const box = (await handle(page).boundingBox())!
  await page.mouse.dblclick(box.x + box.width / 2, box.y + box.height / 2)
  await fullAt(page, OPEN, 'after a double-click on the handle')
  await expect.poll(() => saved(page)).toBe('17.5')

  // What earlier versions saved, each read on a reload.
  async function opensWith(entries: Record<string, string>, px: number, how: string) {
    await page.evaluate((e) => {
      localStorage.removeItem('conductor.sidebar.width')
      localStorage.removeItem('conductor.sidebar.size')
      for (const [k, v] of Object.entries(e)) localStorage.setItem(k, v)
    }, entries)
    await page.reload()
    await fullAt(page, px, how)
  }
  // The 200 px minimum of the versions before this one, left by a double-click or a drag: the width it opens at; the value stays
  // until the next drag.
  await opensWith({ 'conductor.sidebar.width': '12.5' }, OPEN, 'with 12.5 rem saved')
  expect(await saved(page)).toBe('12.5')
  // The 268 px a version opened at, dragged to: kept.
  await opensWith({ 'conductor.sidebar.width': '16.75' }, 268, 'with 16.75 rem saved')
  // A percent of the window from before #437: 24 % of 1440 px, kept under the width's own key, the old key gone.
  await opensWith({ 'conductor.sidebar.size': '24' }, 345.6, 'with 24 % saved')
  expect(await saved(page)).toBe('21.6')
  expect(await saved(page, 'conductor.sidebar.size')).toBeNull()

  // The rail is 64 px wide, its handle gone; the phone has the bottom bar, no sidebar.
  await page.keyboard.press('Control+b')
  await onRail(page)
  await expect.poll(() => width(page)).toBe(64)
  await expect(handle(page)).toHaveCount(0)
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(page.locator('[data-bottom-bar]')).toBeVisible()
  await expect(root(page)).toBeHidden()
})
