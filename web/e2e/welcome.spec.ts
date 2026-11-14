import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'

// The first screens of a new install (#906): the welcome card, whose three choices show their words whole (the first point of
// #374), and the workbench token's dialog, which says where the token is as the server says it.

/**
 * The page as a server with no session at all shows it: the event stream refused, so the page reads the list, which is empty. The
 * server the specs share has sessions other specs left; this page sees none of them.
 */
async function noSessions(page: Page) {
  await page.route('**/api/events*', (r) => r.abort())
  await page.route('**/api/sessions', (r) => (r.request().method() === 'GET' ? r.fulfill({ status: 200, contentType: 'application/json', body: '{"sessions":[]}' }) : r.fallback()))
}

for (const [where, w, h, path] of [
  ['the desktop app\'s first window', 1440, 900, '/'],
  ['a phone', 400, 800, '/yard'],
] as const) {
  test(`the welcome card shows each choice's words whole, in ${where}`, async ({ page }) => {
    await page.setViewportSize({ width: w, height: h })
    await noSessions(page)
    await page.goto(path)
    const card = page.locator('[data-welcome]')
    await expect(card).toBeVisible({ timeout: 15_000 })
    const inside = (await card.boundingBox())!
    for (const label of ['Launch agent', 'Load the example crews', 'Settings']) {
      // Settings goes to a page: a link drawn as a button.
      const choice = card.getByRole(label === 'Settings' ? 'link' : 'button', { name: label, exact: true })
      await expect(choice).toBeVisible()
      // "Load the example crews", not "Load the ex…".
      expect(await choice.locator('[data-slot="label"]').evaluate((e) => e.scrollWidth <= e.clientWidth), label).toBe(true)
      const box = (await choice.boundingBox())!
      expect(box.x + box.width, label).toBeLessThanOrEqual(inside.x + inside.width)
    }
  })
}

test('the token dialog says where the server keeps a token it made: it prints the file\'s path, never the token', async ({ browser }) => {
  // A browser with no token: the page asks for it.
  const ctx = await browser.newContext()
  try {
    const page = await ctx.newPage()
    await page.goto('/')
    const gate = page.getByRole('dialog', { name: 'Workbench token' })
    await expect(gate).toBeVisible()
    await expect(gate).toContainText('CONDUCTOR_WORKBENCH_TOKEN')
    await expect(gate).toContainText('in the file workbench-token in its data directory: it prints that file\'s path as it starts, never the token.')
    await expect(gate).not.toContainText('printed at server start')
  } finally {
    await ctx.close()
  }
})
