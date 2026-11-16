import type { Page } from '@playwright/test'
import { dayPage, expect, pageTitle, test } from './fixtures'
import { inside, noOverlap, noSideways, tapTarget } from './layout'

// The Roundhouse is the Yard's second view (UX1's PR-17, docs/plans/ux-review.md, P12): a switch in each header, Grid and Roundhouse,
// goes between the two; the Roundhouse keeps its route, G R and the old /carousel, leaves the nav and the phone's More, and the Yard's
// row is current on both views.

/**
 * Presses G then R as a person typing them quickly does: both keys handed to the browser at once (shortcuts.spec.ts's reason: a round
 * trip per key on a loaded machine outlasts the chord's 800 ms).
 */
async function pressGR(page: Page) {
  const cdp = await page.context().newCDPSession(page)
  try {
    const sends: Array<Promise<unknown>> = []
    for (const key of ['g', 'r']) {
      const code = `Key${key.toUpperCase()}`
      const windowsVirtualKeyCode = key.toUpperCase().charCodeAt(0)
      sends.push(cdp.send('Input.dispatchKeyEvent', { type: 'keyDown', key, code, text: key, unmodifiedText: key, windowsVirtualKeyCode }))
      sends.push(cdp.send('Input.dispatchKeyEvent', { type: 'keyUp', key, code, windowsVirtualKeyCode }))
    }
    await Promise.all(sends)
  } finally {
    await cdp.detach()
  }
}

/** The sidebar's foot: the pages. */
const foot = (page: Page) => page.locator('#dashboard-sidebar-main').getByRole('navigation').last()

/** The checks made on either view at 1440: the view's switch current, the title, the nav's Yard row current, no Roundhouse row. */
async function onView(page: Page, view: 'grid' | 'roundhouse') {
  await expect(page).toHaveURL(view === 'grid' ? /\/yard$/ : /\/roundhouse$/)
  await expect(page.locator(`[data-yard-view="${view}"]`)).toHaveAttribute('aria-current', 'page')
  await expect(page.locator(`[data-yard-view="${view === 'grid' ? 'roundhouse' : 'grid'}"]`)).not.toHaveAttribute('aria-current', /./)
  await expect(page.locator(view === 'grid' ? '[data-yard-header]' : '[data-roundhouse-header]').locator('[data-slot="title"]')).toHaveText('Yard')
  // The title's page part: a count of what needs you may lead it ("(1) Yard · Conductor").
  await expect.poll(() => pageTitle(page)).toMatch(view === 'grid' ? /^Yard\b/ : /^Yard · Roundhouse\b/)
  await expect(foot(page).getByRole('link', { name: /^Yard/ })).toHaveAttribute('aria-current', 'page')
  await expect(foot(page).getByRole('link', { name: /Roundhouse/ })).toHaveCount(0)
}

test("the Yard's header switches to the Roundhouse and back, the Yard's row current on both", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto('/yard')
  await expect(page.locator('#dashboard-sidebar-main')).toBeVisible({ timeout: 30_000 })
  await onView(page, 'grid')
  await page.locator('[data-yard-view="roundhouse"]').click()
  await onView(page, 'roundhouse')
  await page.locator('[data-yard-view="grid"]').click()
  await onView(page, 'grid')
})

test('G R from another page and the old /carousel land on the Roundhouse, the Yard current', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto('/agents')
  // The layout's keys listen once its sidebar is drawn: a key pressed before is lost.
  await expect(page.locator('#dashboard-sidebar-main')).toBeVisible({ timeout: 30_000 })
  await pressGR(page)
  await onView(page, 'roundhouse')
  await page.goto('/carousel')
  await onView(page, 'roundhouse')
})

test("on a phone the Yard tab is current on the Roundhouse, More has no Roundhouse, and both headers fit", async ({ browser, state }) => {
  const page = await dayPage(browser, { server: state, path: '/yard', width: 390, height: 844, touch: true })
  const views = page.locator('[data-yard-views]')
  for (const view of ['grid', 'roundhouse'] as const) {
    if (view === 'roundhouse') await page.locator('[data-yard-view="roundhouse"]').tap()
    await expect(page.locator(`[data-yard-view="${view}"]`)).toHaveAttribute('aria-current', 'page')
    await expect(page.locator('[data-bottom-tab="yard"]')).toHaveAttribute('aria-current', 'page')
    const header = page.locator(view === 'grid' ? '[data-yard-header]' : '[data-roundhouse-header]')
    // Every part of the header within the window and clear of the others.
    await inside(header.locator('button, a, [data-slot="title"]'), page.locator('body'))
    await noOverlap(header)
    await noSideways(page)
    // The view's switch is two icons named by their words, each a 44 px target under touch.
    await expect(views.locator('[data-yard-view="grid"]')).toHaveAccessibleName('Grid')
    await expect(views.locator('[data-yard-view="roundhouse"]')).toHaveAccessibleName('Roundhouse')
    await tapTarget(views.locator('[data-yard-view]'))
  }
  await page.locator('[data-bottom-tab="more"]').tap()
  const more = page.locator('[data-bottom-more]')
  await expect(more).toBeVisible()
  await expect(more.getByRole('link', { name: /Agents/ })).toBeVisible()
  await expect(more.getByRole('link', { name: /Roundhouse/ })).toHaveCount(0)
})
