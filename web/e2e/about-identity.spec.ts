import { expect, test } from './fixtures'

// Settings → The app's Identity row (docs/plans/identity-uplink.md): who the server is signed in as, from the identity provider's
// detail on its GET /api/capabilities row; none for a plain build, whose reply registers nothing.

test("the About card's Identity row shows who the server is signed in as", async ({ page }) => {
  await page.route('**/api/capabilities', (route) =>
    route.fulfill({ json: { providers: [{ kind: 'identity', name: 'cloud', state: 'ready', detail: 'signed in as Ana · workspace Acme' }] } }),
  )
  await page.goto('/settings')
  const about = page.locator('[data-about-card]')
  await expect(about.locator('[data-about-row="extensions"]')).toHaveText('identity: cloud')
  await expect(about.locator('[data-about-row="identity"]')).toHaveText('cloud: signed in as Ana · workspace Acme')
  await page.unroute('**/api/capabilities')
})

test('a plain build shows no Identity row', async ({ page }) => {
  await page.goto('/settings')
  const about = page.locator('[data-about-card]')
  // The row the reply draws, so the card has its reply before the absence is judged.
  await expect(about.locator('[data-about-row="extensions"]')).toHaveText('None: the open-source feature set')
  await expect(about.locator('[data-about-row="identity"]')).toHaveCount(0)
})
