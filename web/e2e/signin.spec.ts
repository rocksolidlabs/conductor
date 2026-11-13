import { expect, test } from './fixtures'
import type { Page } from '@playwright/test'

// Settings → Sign in (GET, POST and DELETE /api/signin, docs/protocol.md): absent on the plain e2e server, which registers no
// identity provider and is asked nothing; with the replies of a provider that offers a sign-in (page.route), sign in, the code,
// signed in and sign out, the card beside the others and over none of them.

/** The page's own schedule of reads while a sign-in starts or shows a code: the first 2 s after a reply (useSignIn, SIGNIN_FIRST_MS). */
const FIRST_READ_MS = 2000
/** Its read in every other state: every 30 s (SIGNIN_EVERY_MS). */
const EVERY_MS = 30_000

/** Answers GET /api/capabilities with a ready identity provider, as a build with one registered would. */
async function withIdentity(page: Page) {
  await page.route('**/api/capabilities', (route) =>
    route.fulfill({ json: { providers: [{ kind: 'identity', name: 'cloud', state: 'ready' }] } }),
  )
}

/** Whether two boxes overlap by more than a pixel. */
function overlaps(a: { x: number; y: number; width: number; height: number }, b: { x: number; y: number; width: number; height: number }) {
  return a.x + a.width - 1 > b.x && b.x + b.width - 1 > a.x && a.y + a.height - 1 > b.y && b.y + b.height - 1 > a.y
}

test('the card is absent on the plain e2e server, and nothing asks for the sign-in', async ({ page }) => {
  const asked: string[] = []
  page.on('request', (r) => {
    if (new URL(r.url()).pathname === '/api/signin') asked.push(r.method())
  })
  await page.goto('/settings')
  // The row the capabilities reply draws, so the page has its reply before the absence is judged.
  await expect(page.locator('[data-about-card] [data-about-row="extensions"]')).toHaveText('None: the open-source feature set')
  await expect(page.locator('[data-signin-card]')).toHaveCount(0)
  expect(asked).toEqual([])
})

test('sign in, the code, signed in and sign out, through a provider’s replies', async ({ page }, testInfo) => {
  // Two of the page's own reads at most are waited for (the code after starting, signed in after the code): each bounded by the
  // longest wait of its schedule, with the steps' own bounds above them.
  testInfo.setTimeout(testInfo.timeout + 2 * EVERY_MS)
  await withIdentity(page)
  const page1 = 'https://id.example.com/device'
  let status: Record<string, unknown> = { supported: true, state: 'signed-out' }
  const calls: string[] = []
  await page.route('**/api/signin', async (route) => {
    const method = route.request().method()
    calls.push(method)
    if (method === 'POST') status = { supported: true, state: 'starting' }
    // The provider's own goroutines give it a code once it has started: the page's next read shows it.
    else if (method === 'GET' && status.state === 'starting') status = { supported: true, state: 'code', code: 'WDJB-MJHT', url: page1, expires: '2026-11-12T09:15:00Z' }
    else if (method === 'DELETE') status = { supported: true, state: 'signed-out', why: 'Signed out from Settings.' }
    await route.fulfill({ json: status })
  })
  await page.goto('/settings')
  const card = page.locator('[data-signin-card]')
  await expect(card).toHaveAttribute('data-signin-state', 'signed-out')
  await expect(card.locator('[data-signin-who]')).toHaveCount(0)

  await card.locator('[data-signin-begin]').click()
  // The begin's own reply.
  await expect(card).toHaveAttribute('data-signin-state', 'starting')
  await expect(card.locator('[data-signin-starting]')).toBeVisible()
  // The read the page makes on its own, FIRST_READ_MS after it.
  await expect(card).toHaveAttribute('data-signin-state', 'code', { timeout: FIRST_READ_MS + 15_000 })
  await expect(card.locator('[data-signin-group]')).toHaveText(['WDJB', 'MJHT'])
  const open = card.locator('[data-signin-open]')
  await expect(open).toHaveAttribute('href', page1)
  await expect(open).toHaveAttribute('target', '_blank')
  await expect(card.locator('[data-signin-copy]')).toBeVisible()

  // The provider signs the server in: one of the page's next reads shows it, at most EVERY_MS after the last.
  status = { supported: true, state: 'signed-in', who: 'Ana · workspace Acme' }
  await expect(card.locator('[data-signin-who]')).toHaveText('Signed in as Ana · workspace Acme', { timeout: EVERY_MS + 15_000 })
  await expect(card.locator('[data-signin-group]')).toHaveCount(0)

  await card.locator('[data-signin-out]').click()
  await expect(card).toHaveAttribute('data-signin-state', 'signed-out')
  await expect(card.locator('[data-signin-why]')).toHaveText('Signed out from Settings.')
  expect(calls.filter((m) => m === 'POST')).toHaveLength(1)
  expect(calls.filter((m) => m === 'DELETE')).toHaveLength(1)

  // The code is never kept in the page's storage.
  const stored = await page.evaluate(() => JSON.stringify({ ...localStorage }) + JSON.stringify({ ...sessionStorage }))
  expect(stored).not.toContain('WDJB')
  await page.unroute('**/api/signin')
  await page.unroute('**/api/capabilities')
})

for (const size of [
  { width: 390, height: 844 },
  { width: 1440, height: 900 },
]) {
  test(`at ${size.width} wide the card covers no other control and the page does not scroll sideways`, async ({ page }) => {
    await page.setViewportSize(size)
    await withIdentity(page)
    await page.route('**/api/signin', (route) =>
      route.fulfill({ json: { supported: true, state: 'code', code: 'WDJB-MJHT', url: 'https://id.example.com/device' } }),
    )
    await page.goto('/settings')
    const card = page.locator('[data-signin-card]')
    await expect(card.locator('[data-signin-group]')).toHaveText(['WDJB', 'MJHT'])
    const about = page.locator('[data-about-card]')
    await expect(about.locator('[data-about-row="extensions"]')).toHaveText('identity: cloud')
    const box = (await card.boundingBox())!
    for (const other of [about, page.locator('[data-brakeman-settings]')]) {
      if ((await other.count()) === 0) continue
      expect(overlaps(box, (await other.boundingBox())!)).toBe(false)
    }
    // Each of the card's controls is the one hit at its centre: nothing lies over it.
    for (const sel of ['[data-signin-copy]', '[data-signin-open]']) {
      await card.locator(sel).scrollIntoViewIfNeeded()
      const hit = await card.locator(sel).evaluate((el) => {
        const r = el.getBoundingClientRect()
        const at = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2)
        return !!at && el.contains(at)
      })
      expect(hit, sel).toBe(true)
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true)
    await page.unroute('**/api/signin')
    await page.unroute('**/api/capabilities')
  })
}
