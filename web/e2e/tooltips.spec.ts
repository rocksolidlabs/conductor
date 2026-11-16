import { DAY_STEP_MS, type Day } from './day'
import { dayPage, expect, test, type Session } from './fixtures'
import { tabTo } from './layout'

// A tooltip on every icon-only button (docs/design/system.md, Tooltips; the UX review's P20): an IconButton shows its label in a
// tooltip on hover and on keyboard focus, and keeps it as the button's accessible name on a phone, where no tooltip opens. The
// session page's More is the one walked here; designRules.test.ts holds that every icon-only button is an IconButton. Each test
// makes its own session on the day's server and ends and removes it after.

/** A session of the test's own on the day's server, ended and removed by `drop`. */
async function launch(day: Day, name: string): Promise<Session> {
  return day.api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name })
}

async function drop(day: Day, id: string): Promise<void> {
  await day.api.stopSession(id)
  await expect.poll(() => day.statusOf(id), { timeout: DAY_STEP_MS }).toMatch(/^(exited|stopped|gone)$/)
  await day.api.stopSession(id)
  await expect.poll(() => day.statusOf(id), { timeout: DAY_STEP_MS }).toBe('gone')
}

for (const theme of ['dark', 'light'] as const) {
  test(`an icon-only button shows its label in a tooltip on hover and on keyboard focus (${theme})`, async ({ browser, day }) => {
    const s = await launch(day, `e2e tooltips ${theme}`)
    try {
      const page = await dayPage(browser, { path: `/sessions/${s.id}`, theme })
      const more = page.locator('[data-more-button]')
      // The tooltip by its role (reka keeps the role on a copy of the words for the button's description, hidden from the tree) and
      // the box drawn around it.
      const anyTip = page.getByRole('tooltip', { includeHidden: true })
      const tip = page.locator('[data-slot="content"]').filter({ has: page.getByRole('tooltip', { name: 'More', exact: true, includeHidden: true }) })
      await expect(more).toHaveAccessibleName('More')
      await expect(anyTip).toHaveCount(0)

      // On hover.
      await more.hover()
      await expect(tip).toBeVisible()
      await expect(tip.locator('[data-slot="text"]')).toHaveText('More')
      await expect(more).toHaveAccessibleDescription('More')
      // Off it, the tooltip is gone before the next is asked for (a closing tooltip is still drawn through its animation).
      await page.locator('[data-session-title]').hover()
      await expect(anyTip).toHaveCount(0)

      // On focus by Tab. The page focuses its terminal on load, and the terminal keeps Tab: wait for that focus, then move it off
      // with a click on the page's header before tabbing from the button beside More.
      await expect(page.locator('.terminal-host .xterm-helper-textarea').first()).toBeFocused({ timeout: 30_000 })
      await page.locator('[data-session-title]').click()
      await tabTo(page, more, 4, page.locator('[data-share-button]'))
      await expect(tip).toBeVisible()

      // As a menu's trigger: the menu's attributes and the page's own are on the one button a person presses, and it opens its menu
      // by the keyboard and by a click.
      await expect(more).toHaveJSProperty('tagName', 'BUTTON')
      await expect(more).toHaveAttribute('aria-haspopup', 'menu')
      await expect(more).toHaveAttribute('data-icon-button', '')
      await page.keyboard.press('Enter')
      await expect(page.getByRole('menuitem', { name: 'Copy session id' })).toBeVisible()
      await expect(more).toHaveAttribute('aria-expanded', 'true')
      await page.keyboard.press('Escape')
      await expect(page.getByRole('menu')).toHaveCount(0)
      await more.click()
      await expect(page.getByRole('menuitem', { name: 'Copy session id' })).toBeVisible()
      await page.keyboard.press('Escape')
      await expect(page.getByRole('menu')).toHaveCount(0)
    } finally {
      await drop(day, s.id)
    }
  })
}

test("an icon-only button as a popover's trigger opens it by a click and by the keyboard, its attributes on the button", async ({ browser, day }) => {
  await day.part('dispatch')
  // Below sm the Views button of a Dispatch project is its bookmark alone (its label hidden), an IconButton over a UPopover.
  const page = await dayPage(browser, { path: '/dispatch/CND', width: 600, height: 900, ready: '[data-views-open]' })
  const views = page.locator('[data-views-open]')
  const menu = page.locator('[data-views-menu]')
  await expect(views).toHaveJSProperty('tagName', 'BUTTON')
  await expect(views).toHaveAttribute('data-icon-button', '')
  await expect(views).toHaveAccessibleName('Saved views')
  await expect(views).toHaveAttribute('aria-expanded', 'false')
  await views.click()
  await expect(menu).toBeVisible()
  await expect(views).toHaveAttribute('aria-expanded', 'true')
  await page.keyboard.press('Escape')
  await expect(views).toHaveAttribute('aria-expanded', 'false')
  await expect(menu).toHaveCount(0)
  await views.focus()
  await page.keyboard.press('Enter')
  await expect(menu).toBeVisible()
  await expect(views).toHaveAttribute('aria-expanded', 'true')
  await page.keyboard.press('Escape')
  await expect(menu).toHaveCount(0)
})

test('on a phone an icon-only button keeps its label as its name, and opens its menu at a tap', async ({ browser, day }) => {
  const s = await launch(day, 'e2e tooltips phone')
  try {
    const page = await dayPage(browser, { path: `/sessions/${s.id}`, width: 390, height: 844, touch: true })
    const more = page.locator('[data-more-button]')
    await expect(more).toBeVisible()
    await expect(more).toHaveAccessibleName('More')
    await more.tap()
    await expect(page.getByRole('menuitem', { name: 'Copy session id' })).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(page.getByRole('menu')).toHaveCount(0)
  } finally {
    await drop(day, s.id)
  }
})
