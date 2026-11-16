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
      const tip = page.getByRole('tooltip', { name: 'More', exact: true })
      await expect(more).toHaveAccessibleName('More')
      await expect(page.getByRole('tooltip')).toHaveCount(0)

      // On hover.
      await more.hover()
      await expect(tip).toBeVisible()
      // Off it, the tooltip is gone before the next is asked for (a closing tooltip is still drawn through its animation).
      await page.locator('[data-session-title]').hover()
      await expect(page.getByRole('tooltip')).toHaveCount(0)

      // On focus by Tab. The page focuses its terminal on load, and the terminal keeps Tab: wait for that focus, then move it off
      // with a click on the page's header before tabbing from the button beside More.
      await expect(page.locator('.terminal-host .xterm-helper-textarea').first()).toBeFocused({ timeout: 30_000 })
      await page.locator('[data-session-title]').click()
      await tabTo(page, more, 4, page.locator('[data-share-button]'))
      await expect(tip).toBeVisible()

      // The button still opens its menu.
      await page.keyboard.press('Enter')
      await expect(page.getByRole('menuitem', { name: 'Copy session id' })).toBeVisible()
      await page.keyboard.press('Escape')
      await expect(page.getByRole('menu')).toHaveCount(0)
    } finally {
      await drop(day, s.id)
    }
  })
}

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
