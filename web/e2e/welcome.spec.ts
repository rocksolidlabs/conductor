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

/**
 * The welcome's three choices, each label whole and inside the card, at one width each, either all in one row or one to a row:
 * never two in a row with the third stretched under them (#906).
 */
async function choicesWhole(page: Page, at: string) {
  const card = page.locator('[data-welcome]')
  await expect(card, at).toBeVisible({ timeout: 15_000 })
  const inside = (await card.boundingBox())!
  const boxes = []
  for (const label of ['Launch agent', 'Load the example crews', 'Settings']) {
    // Settings goes to a page: a link drawn as a button.
    const choice = card.getByRole(label === 'Settings' ? 'link' : 'button', { name: label, exact: true })
    await expect(choice, `${label} ${at}`).toBeVisible()
    // "Load the example crews", not "Load the ex…".
    expect(await choice.locator('[data-slot="label"]').evaluate((e) => e.scrollWidth <= e.clientWidth), `${label} whole ${at}`).toBe(true)
    const box = (await choice.boundingBox())!
    expect(box.x, `${label} ${at}`).toBeGreaterThanOrEqual(inside.x)
    expect(box.x + box.width, `${label} ${at}`).toBeLessThanOrEqual(inside.x + inside.width)
    boxes.push(box)
  }
  const widths = boxes.map((b) => Math.round(b.width))
  expect(Math.max(...widths) - Math.min(...widths), `equal widths ${at}: ${widths.join(', ')}`).toBeLessThanOrEqual(1)
  const tops = new Set(boxes.map((b) => Math.round(b.y)))
  expect([1, 3], `one row or one to a row ${at}: ${[...tops].join(', ')}`).toContain(tops.size)
}

for (const [where, path, sizes] of [
  ['the desktop app\'s first window', '/', [[1440, 900], [1024, 768]]],
  ['a phone', '/yard', [[400, 800], [1024, 768]]],
] as const) {
  test(`the welcome card shows each choice's words whole, at equal widths, in ${where}, and again as the window changes`, async ({ page }) => {
    const [[w, h], ...then] = sizes
    await page.setViewportSize({ width: w, height: h })
    await noSessions(page)
    await page.goto(path)
    await choicesWhole(page, `at ${w} × ${h}`)
    // The window resized: the card is drawn again at each width, and its choices still read whole.
    for (const [nw, nh] of then) {
      await page.setViewportSize({ width: nw, height: nh })
      await expect.poll(() => page.evaluate(() => window.innerWidth)).toBe(nw)
      await choicesWhole(page, `resized to ${nw} × ${nh}`)
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
