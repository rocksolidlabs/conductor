import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'

/**
 * Presses lowercase letters in turn as a person typing them quickly does:
 * the browser is handed every key at once and passes each to the page as
 * soon as the page has taken the one before. page.keyboard.press waits for
 * each key's answer before it sends the next, and on a loaded machine that
 * round trip took up to a second: G's letter then came after the page's
 * chord had stopped waiting for it (800 ms), and the chord was lost.
 */
async function pressInTurn(page: Page, ...keys: string[]) {
  const cdp = await page.context().newCDPSession(page)
  try {
    const sends: Array<Promise<unknown>> = []
    for (const key of keys) {
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

// The page chords go by each page's initial: G then Y the Yard, G then R the
// Roundhouse, G then C Crews, G then E History's feed (the retired Events page's; A Agents as before). The shortcuts
// modal lists every action in three columns: the action, its keys in a
// terminal, its keys outside one.
test("G then Y, R, C and E go to the Yard, the Roundhouse, Crews and History's feed; the modal shows three columns", async ({ page }) => {
  await page.goto('/agents')
  await expect(page.locator('[data-session-list]')).toBeVisible({ timeout: 30_000 })
  await pressInTurn(page, 'g', 'y')
  await expect(page).toHaveURL(/\/yard/)
  await pressInTurn(page, 'g', 'r')
  await expect(page).toHaveURL(/\/roundhouse/)
  await pressInTurn(page, 'g', 'c')
  await expect(page).toHaveURL(/\/crews/)
  await pressInTurn(page, 'g', 'e')
  await expect(page).toHaveURL(/\/history\?view=feed$/)
  await expect(page.locator('[data-event-feed]')).toBeVisible()

  await page.keyboard.press('?')
  const modal = page.locator('[data-shortcuts]')
  await expect(modal).toBeVisible()
  await expect(modal.locator('[data-shortcuts-columns]')).toHaveText(/Action\s*In a terminal\s*Outside/)
  const yard = modal.locator('[data-shortcut="Go to the Yard"]')
  await expect(yard.locator('[data-shortcut-terminal]')).toHaveText(/Alt.*Y/i)
  await expect(yard.locator('[data-shortcut-outside]')).toHaveText(/G\s*then\s*Y/)
  await expect(modal.locator('[data-shortcut="Go to History · Live"] [data-shortcut-outside]')).toHaveText(/G\s*then\s*E/)
  // What needs the list focused has no key in a terminal: the cell shows nothing and reads "none" (#328: no dot; the
  // word is visually hidden text, since an empty span's aria-label is not announced).
  const none = modal.locator('[data-shortcut="Next row (or ↓)"] [data-shortcut-terminal]')
  await expect(none).toHaveText('none')
  await expect(none.locator('.sr-only')).toHaveText('none')
  expect(await none.evaluate((el) => Array.from(el.querySelectorAll('.sr-only')).every((n) => n.getBoundingClientRect().width <= 1))).toBe(true)
})

// The table reads as columns (round 14: each row sized its own key columns,
// so nothing lined up and the owner could not tell them apart): at 1440 the
// modal is wide, every row's two key columns start where the first row's do,
// and the keys never run into the action; at 390 a row stacks and nothing
// runs off the side.
test('the shortcuts table lines its columns up at 1440 and fits a phone at 390', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto('/agents')
  await expect(page.locator('[data-session-list]')).toBeVisible({ timeout: 30_000 })
  await page.keyboard.press('?')
  const modal = page.locator('[data-shortcuts]')
  await expect(modal).toBeVisible()
  expect((await modal.boundingBox())!.width).toBeGreaterThan(650)
  // The column names stand once, in the header over the scroll (#328): no group repeats them.
  await expect(modal.locator('[data-shortcuts-columns]')).toHaveCount(1)
  await expect(modal.locator('[data-shortcuts-group-columns]')).toHaveCount(0)
  const cells = await modal.locator('[data-shortcut]').evaluateAll((rows) =>
    rows.map((r) => {
      const label = r.children[0]!.getBoundingClientRect()
      const term = r.children[1]!.getBoundingClientRect()
      const out = r.children[2]!.getBoundingClientRect()
      return { labelRight: label.right, term: Math.round(term.left), termWidth: Math.round(term.width), out: Math.round(out.left) }
    }),
  )
  expect(cells.length).toBeGreaterThan(10)
  for (const c of cells) {
    expect(Math.abs(c.term - cells[0]!.term)).toBeLessThanOrEqual(1)
    expect(Math.abs(c.out - cells[0]!.out)).toBeLessThanOrEqual(1)
    expect(Math.abs(c.termWidth - cells[0]!.termWidth)).toBeLessThanOrEqual(1)
    expect(c.labelRight).toBeLessThanOrEqual(c.term + 1)
  }
  await page.keyboard.press('Escape')
  await page.setViewportSize({ width: 390, height: 844 })
  await page.keyboard.press('?')
  await expect(modal).toBeVisible()
  const overflow = await modal.evaluate((el) => {
    const scroller = el.closest('[role="dialog"]') ?? el
    return { scroll: scroller.scrollWidth, client: scroller.clientWidth }
  })
  expect(overflow.scroll).toBeLessThanOrEqual(overflow.client + 1)
  await expect(modal.locator('[data-shortcut="Go to the Yard"] [data-shortcut-outside]')).toBeVisible()
})
