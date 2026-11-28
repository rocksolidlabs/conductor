import type { Page } from '@playwright/test'
import { dayPage, expect, test } from './fixtures'
import { noSideways, tapTarget } from './layout'

// Alerts holds both halves of how a person is alerted: the two switches (a browser notification, a chime) and, under them, the
// rules for where each kind of event goes, kept in this browser. Your menu's Alerts…, the palette's Alerts…, the phone's "All alert
// rules", the feed's Edit routing and `?alerts` on any page open the one panel; an old link to the Events page's Routing tab opens it
// over the feed.

const panelOf = (page: Page) => page.getByRole('dialog', { name: 'Alerts' })
const routes = async (page: Page) => JSON.parse((await page.evaluate(() => localStorage.getItem('conductor.events.routes'))) ?? '{}')

/** Your menu, then Alerts…: the panel, once the menu has gone. */
async function openFromMenu(page: Page) {
  const account = page.locator('#dashboard-sidebar-main [data-slot="header"] [data-header-account]')
  await account.click()
  await page.getByRole('menuitem', { name: /^Alerts/ }).click()
  await expect(page.getByRole('menu')).toHaveCount(0)
  const panel = panelOf(page)
  await expect(panel).toBeVisible()
  return panel
}

test('your menu opens Alerts with the switches and the rules; a destination toggled there is kept on this device', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto('/crews')
  await expect(page.locator('[data-session-list="sidebar"]')).toBeVisible()
  let panel = await openFromMenu(page)
  await expect(panel.getByText('Notification on this device', { exact: true })).toBeVisible()
  await expect(panel.getByText('When a session needs you, and for the events routed to This device:')).toBeVisible()
  // The old words are gone (UX3 R86).
  await expect(panel.getByText(/browser/i)).toHaveCount(0)
  // Under the switches, where each event goes; inside the panel the switches have no way to the rules: they are here.
  await expect(panel.getByRole('heading', { name: 'Where each event goes' })).toBeVisible()
  const matrix = panel.locator('[data-routing-matrix]')
  await expect(matrix).toBeVisible()
  await expect(panel.locator('[data-alert-rules]')).toHaveCount(0)
  const switchesBottom = (await panel.locator('[data-alert-switches]').boundingBox())!
  expect((await matrix.boundingBox())!.y, 'the rules are under the switches').toBeGreaterThan(switchesBottom.y + switchesBottom.height)
  // At most 44 rem wide at 1440, and wide enough that a row's pills sit beside its title.
  const width = (await panel.boundingBox())!.width
  expect(width).toBeLessThanOrEqual(44 * 16 + 0.5)
  expect(width).toBeGreaterThan(600)
  // The pill toggles and is kept on this device.
  const pill = panel.locator('[data-cell="done.browser"]')
  await expect(pill).toHaveAttribute('aria-checked', 'false')
  await pill.click()
  await expect(pill).toHaveAttribute('aria-checked', 'true')
  expect((await routes(page)).done.browser).toBe(true)
  // After a reload, opened again from the menu, it is still on; working's badge is a fixed cell.
  await page.reload()
  await expect(page.locator('[data-session-list="sidebar"]')).toBeVisible()
  panel = await openFromMenu(page)
  await expect(panel.locator('[data-cell="done.browser"]')).toHaveAttribute('aria-checked', 'true')
  await expect(panel.locator('[data-cell="working.badge"]')).toHaveAttribute('data-fixed-cell', '')
  // Put back.
  await panel.locator('[data-cell="done.browser"]').click()
  await expect(panel.locator('[data-cell="done.browser"]')).toHaveAttribute('aria-checked', 'false')
  expect((await routes(page)).done.browser).toBe(false)
})

for (const theme of ['dark', 'light'] as const) {
  test(`the phone's More ends its switches with All alert rules, which opens the panel whole at 390 px (${theme})`, async ({ browser, state }) => {
    const page = await dayPage(browser, { server: state, path: '/sessions', width: 390, height: 844, touch: true, theme })
    await page.locator('[data-bottom-tab="more"]').tap()
    const more = page.locator('[data-bottom-more]')
    await expect(more.getByText('Notification on this device', { exact: true })).toBeVisible()
    await expect(more.getByText('Browser notification')).toHaveCount(0)
    const rules = more.getByRole('button', { name: 'All alert rules' })
    await expect(rules).toBeVisible()
    await tapTarget(rules)
    await rules.tap()
    // The sheet goes, the panel opens over the whole width with the rules.
    await expect(more).toHaveCount(0)
    const panel = panelOf(page)
    await expect(panel.locator('[data-routing-matrix]')).toBeVisible()
    const box = (await panel.boundingBox())!
    expect(box.x).toBeLessThanOrEqual(0.5)
    expect(box.width).toBeGreaterThanOrEqual(390 - 0.5)
    // Nothing in it scrolls or is cut sideways, and every pill and its close button are touch targets.
    await noSideways(page, { root: panel })
    await tapTarget(panel.locator('[data-routing-matrix] button[role="switch"]'))
    await tapTarget(panel.locator('[data-slot="close"]'))
    await panel.locator('[data-slot="close"]').tap()
    await expect(panel).toHaveCount(0)
    // More again: its switches and the way to the rules, as before.
    await page.locator('[data-bottom-tab="more"]').tap()
    await expect(more.getByText('Notification on this device', { exact: true })).toBeVisible()
    await expect(more.getByRole('button', { name: 'All alert rules' })).toBeVisible()
  })
}

test("an old link to the Events page's Routing tab opens the panel over History's feed, and ?alerts opens it on any page", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto('/events?tab=routing')
  await expect(panelOf(page).locator('[data-routing-matrix]')).toBeVisible()
  await expect(page.locator('[data-event-feed]')).toBeAttached()
  await expect(page).toHaveURL(/\/history\?view=feed$/)
  await page.keyboard.press('Escape')
  await expect(panelOf(page)).toHaveCount(0)
  // The feed's Edit routing opens the panel, and closed it gives the focus back to the button.
  const edit = page.locator('[data-routing-readout]').getByRole('button', { name: 'Edit routing' })
  await edit.click()
  await expect(panelOf(page).locator('[data-routing-matrix]')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(panelOf(page)).toHaveCount(0)
  await expect(edit).toBeFocused()
  // `?alerts` on another page opens it, and the address loses it.
  await page.goto('/crews?alerts')
  await expect(panelOf(page).locator('[data-routing-matrix]')).toBeVisible()
  await expect(page).toHaveURL(/\/crews$/)
})

test("the palette's Alerts… opens the panel, and its Events · Routing row is gone", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto('/crews')
  await expect(page.locator('#dashboard-sidebar-main')).toBeAttached()
  await page.keyboard.press('Alt+/')
  const palette = page.locator('[data-palette]')
  await expect(palette).toBeVisible()
  const input = palette.locator('input[data-palette-input]')
  // Go to lists History's Live view (found by its old name, Feed), and no longer the Events page or its Routing tab.
  await input.fill('feed')
  await expect(palette.locator('[data-palette-row="go:history-feed"]')).toBeVisible()
  await expect(palette.locator('[data-palette-row="go:events"]')).toHaveCount(0)
  await expect(palette.locator('[data-palette-row="go:events-routing"]')).toHaveCount(0)
  await input.fill('> alerts')
  const row = palette.locator('[data-palette-row="action:alerts"]')
  await expect(row).toBeVisible()
  await row.click()
  await expect(palette).toBeHidden()
  await expect(panelOf(page).locator('[data-routing-matrix]')).toBeVisible()
})
