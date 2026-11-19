import { expect, standingRoute, test, useTheme } from './fixtures'

// Dark is the theme whatever the OS prefers; light is a choice the account
// menu's Toggle theme makes and the browser remembers.
test('the workbench opens dark on a light OS, and remembers a switch to light', async ({ browser, state }) => {
  const ctx = await browser.newContext({ colorScheme: 'light' })
  const page = await ctx.newPage()
  await page.addInitScript((token) => localStorage.setItem('conductor.workbenchToken', token), state.token)
  await page.goto('/')
  await expect(page.locator('html')).toHaveClass(/\bdark\b/)
  await page.locator('[data-header-account]').click()
  await page.getByRole('menuitem', { name: 'Toggle theme' }).click()
  await expect(page.locator('html')).not.toHaveClass(/\bdark\b/)
  await page.reload()
  await expect(page.locator('html')).not.toHaveClass(/\bdark\b/)
  await ctx.close()
})

// The e2e helper useTheme on a page a goto has just loaded (#1019). A goto returns at the load, before the app's plugins run; the
// colour-mode module's start then writes back the preference the page read as it began. The seam holds the reload's document until
// the page it leaves has made that write, as a slow reload on a loaded machine lets it: a theme stored before the app's start is then
// overwritten, and the reloaded page is dark. The helper stores the theme once the app has mounted, after that write.
test('useTheme right after a goto still switches the theme, though the page it leaves writes its own start after the helper', async ({ browser, state }) => {
  const ctx = await browser.newContext()
  await standingRoute(ctx)
  const page = await ctx.newPage()
  // Each document as it begins (its id and the theme it read), and each write of the theme made in it.
  const docs: Array<{ id: string; start: string | null }> = []
  const writes: Array<{ doc: string; value: string }> = []
  await page.exposeBinding('__themeSeen', (_source, seen: { doc: string; start?: string | null; value?: string }) => {
    if (seen.value === undefined) docs.push({ id: seen.doc, start: seen.start ?? null })
    else writes.push({ doc: seen.doc, value: seen.value })
  })
  await page.addInitScript((token) => {
    localStorage.setItem('conductor.workbenchToken', token)
    const w = window as unknown as { __themeSeen: (s: unknown) => Promise<void> }
    const doc = Math.random().toString(36).slice(2)
    void w.__themeSeen({ doc, start: localStorage.getItem('conductor-color-mode') })
    const set = Storage.prototype.setItem
    Storage.prototype.setItem = function (this: Storage, k: string, v: string) {
      set.call(this, k, v)
      if (k === 'conductor-color-mode') void w.__themeSeen({ doc, value: v })
    }
  }, state.token)
  // The reload's document waits for the page it leaves to have written the theme it began with (its colour-mode start).
  let armed = false
  let held = 0
  await page.route(
    (url) => url.pathname === '/agents',
    async (route) => {
      if (!armed || !route.request().isNavigationRequest()) return route.fallback()
      armed = false
      const leaving = docs[docs.length - 1]!
      await expect.poll(() => writes.some((x) => x.doc === leaving.id && x.value === (leaving.start ?? 'dark')), { timeout: 15_000 }).toBe(true)
      held++
      await route.fallback()
    },
  )
  await page.goto('/agents')
  await expect(page.locator('html')).toHaveClass(/\bdark\b/)
  armed = true
  await useTheme(page, 'light')
  expect(held).toBe(1)
  await expect(page.locator('html')).not.toHaveClass(/\bdark\b/)
  expect(await page.evaluate(() => localStorage.getItem('conductor-color-mode'))).toBe('light')
  await ctx.close()
})
