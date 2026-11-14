import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'

// Needs your decision, as a phone opens it from the page's "Open on your phone" code, and its rows at a phone's width and a desktop's:
// the address opened with no workbench token asks for it at once, the token saved shows the decision list, and a decide pass held
// without one opens the decide page, which asks for nothing. A long title stays inside its row, cut with an ellipsis, its waiting
// time in view. The rows are the list's as the server's shape gives them (the inbox route stubbed in the browser): every wait is on
// what the page draws.

const PHONE = { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true }
const GATE = 'Workbench token'
const PASS = 'e2e.pass-of_ana.7'
const ENDED = 'Your access to this server has ended. Open it again from your workspace page.'

test('the address opened with no token asks for it at once; the token saved shows the list; a pass held without one asks for nothing', async ({ browser, state }) => {
  const ctx = await browser.newContext(PHONE)
  const page = await ctx.newPage()
  await page.goto('/decisions')
  const gate = page.getByRole('dialog', { name: GATE })
  await expect(gate).toBeVisible()
  const field = gate.getByPlaceholder('paste the workbench token')
  await field.fill(state.token)
  await expect(field).toHaveValue(state.token)
  const read = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/inbox' && r.status() === 200)
  await gate.getByRole('button', { name: 'Save' }).click()
  await read
  await expect(gate).toHaveCount(0)
  await expect(page.locator('[data-decisions-list]')).toBeVisible()
  await expect(page.locator('[data-decisions-empty]').or(page.locator('[data-inbox-row]').first())).toBeVisible()
  await ctx.close()

  // A pass and no token: the decide page (the server as shipped refuses the pass), and no gate.
  const passCtx = await browser.newContext(PHONE)
  const passPage = await passCtx.newPage()
  await passPage.goto(`/decisions#pass=${PASS}`)
  await expect(passPage.getByText(ENDED)).toBeVisible()
  expect(new URL(passPage.url()).pathname).toBe('/decide')
  await expect(passPage.getByRole('dialog', { name: GATE })).toHaveCount(0)
  await passCtx.close()
})

const LONG = `${'Deploy the release candidate to every region '.repeat(5)}`.slice(0, 200)

/** The list as the server shapes it: one open item and one answered and not yet applied, each with a 200-character title. */
async function longRows(page: Page) {
  const since = new Date(Date.now() - 5 * 60_000).toISOString()
  const row = (ref: string, tracking: boolean) => ({ ref, kind: 'decision', since, title: LONG, brief: 'b', question: 'May it go out?', answerer: 'you', actions: ['Approve', 'Reject'], blocks: 1, ...(tracking ? { state: 'answered', tracking: true } : { state: 'open' }) })
  await page.route(
    (u) => u.pathname === '/api/inbox',
    (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [row('decision:dr-901', false), row('decision:dr-902', true)], total: 2, tracking: 1, at: new Date().toISOString() }) }),
  )
}

for (const width of [360, 1440]) {
  test(`at ${width} wide: a 200-character title stays inside its row, cut with an ellipsis, and the waiting time is in view`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    await longRows(page)
    await page.goto('/decisions')
    const list = page.locator('[data-decisions-list]')
    for (const ref of ['decision:dr-901', 'decision:dr-902']) {
      const row = list.locator(`[data-inbox-row="${ref}"]`)
      await expect(row).toBeVisible()
      const since = row.locator('[data-inbox-row-since]')
      await expect(since).toBeVisible()
      const [l, r, s] = [(await list.boundingBox())!, (await row.boundingBox())!, (await since.boundingBox())!]
      expect(r.width, `${ref}'s row`).toBeLessThanOrEqual(l.width + 0.5)
      expect(s.x + s.width, `${ref}'s time`).toBeLessThanOrEqual(r.x + r.width + 0.5)
      expect(s.width).toBeGreaterThan(0)
      const title = row.locator('[data-inbox-row-title]')
      expect(await title.evaluate((e) => [getComputedStyle(e).textOverflow, e.scrollWidth > e.clientWidth])).toEqual(['ellipsis', true])
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  })
}
