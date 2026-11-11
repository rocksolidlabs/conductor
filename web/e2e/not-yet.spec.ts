import type { Page } from '@playwright/test'
import { expect, notYet, test } from './fixtures'

// notYet (fixtures.ts) proved by construction: the answer the invite specs lost on CI (#281) was never lost here,
// so a read is cut on purpose, by a real page load while it waits, and the helpers are shown to read again where the bare read
// fails. This shows the specs no longer depend on such a rejection; it does not show what lost the answer on CI.
const site = 'http://not-yet.test/'

// The page function: the first `cuts` reads of the tab reload it while they wait (a load, which loses their answer); later ones answer.
function cutRead(cuts: number): string | Promise<string> {
  const n = Number(sessionStorage.getItem('reads') ?? '0') + 1
  sessionStorage.setItem('reads', String(n))
  if (n > cuts) return 'answered'
  location.reload()
  return new Promise<string>(() => {})
}

const reads = (page: Page) => notYet(() => page.evaluate(() => sessionStorage.getItem('reads')))

test.beforeEach(async ({ page }) => {
  await page.route(site, (r) => r.fulfill({ contentType: 'text/html', body: '<!doctype html><title>not yet</title>' }))
  await page.goto(site)
})

test('a bare read a load cuts fails at the first cut', async ({ page }) => {
  await expect(expect.poll(() => page.evaluate(cutRead, 1)).toBe('answered')).rejects.toThrow('Execution context was destroyed')
  await expect.poll(() => reads(page)).toBe('1')
})

test('a read through notYet is made again after each cut until it answers', async ({ page }) => {
  await expect.poll(() => notYet(() => page.evaluate(cutRead, 3))).toBe('answered')
  await expect.poll(() => reads(page)).toBe('4')
})

test('a read through notYet that rejects otherwise fails at once in its own words', async ({ page }) => {
  const failed = await expect
    .poll(() =>
      notYet(() =>
        page.evaluate(() => {
          throw new Error('a page function that throws')
        }),
      ),
    )
    .toBe(1)
    .then(() => null, (e: Error) => e)
  expect(failed?.message).toContain('a page function that throws')
  expect(failed?.message).not.toContain('not yet')
})

test('a read through notYet of a selector that matches nothing fails at once in its own words', async ({ page }) => {
  const failed = await expect
    .poll(() => notYet(() => page.$eval('[data-not-there]', (el) => el.id)))
    .toBe('x')
    .then(() => null, (e: Error) => e)
  expect(failed?.message).toContain('Failed to find element matching selector "[data-not-there]"')
  expect(failed?.message).not.toContain('not yet')
})

test('a read through notYet lost until its bound says what it last saw', async ({ page }) => {
  const failed = await expect
    .poll(() =>
      notYet(() =>
        page.evaluate(() => {
          throw new Error('Execution context was destroyed (on purpose)')
        }),
      ),
      { timeout: 2_000 },
    )
    .toBe(1)
    .then(() => null, (e: Error) => e)
  expect(failed?.message).toContain('not yet: page.evaluate: Error: Execution context was destroyed (on purpose)')
})

