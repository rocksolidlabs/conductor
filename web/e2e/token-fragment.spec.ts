import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'

// The workbench's own link, the token in the URL fragment ("Open in browser" from the desktop app opens `/#token=…`): taken out of
// the address before Nuxt's router reads it, in any spelling of its key, so once the page has drawn no address, history state or
// route holds it, and the workbench is signed in with it. A fresh context each time, with no token stored.

/** Everything of the page that could hold an address: the address bar, the history's state, the Navigation API's entries, and the router's route and the one it was redirected from. */
const addressHeld = (page: Page) =>
  page.evaluate(() => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const route = (document.querySelector('#__nuxt') as any).__vue_app__.config.globalProperties.$router.currentRoute.value
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const entries = ((window as any).navigation?.entries() ?? []).map((e: { url: string }) => e.url)
    return [location.href, JSON.stringify(history.state), ...entries, route.fullPath, route.redirectedFrom?.fullPath ?? ''].join(' ')
  })

test('the workbench token in the fragment, in any spelling: signed in, and no address, history state or route holds it', async ({ browser, state }) => {
  const token = state.token
  for (const path of ['/', '/wall']) {
    for (const fragment of [`token=${token}`, `%74oken=${token}`, `%74oken=${token}&%E0`, `%74oken=${token}&%`]) {
      const ctx = await browser.newContext()
      const page = await ctx.newPage()
      // A read the server answered with the token: the workbench is signed in with it.
      const signedIn = page.waitForResponse((r) => new URL(r.url()).pathname.startsWith('/api/') && r.status() === 200 && r.request().headers()['authorization'] === `Bearer ${token}`)
      await page.goto(`${path}#${fragment}`)
      await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
      await signedIn
      expect(await page.evaluate(() => localStorage.getItem('conductor.workbenchToken'))).toBe(token)
      await expect(page.getByRole('dialog', { name: 'Workbench token' })).toHaveCount(0)
      expect(await addressHeld(page), `${path}#${fragment}`).not.toContain(token)
      expect(new URL(page.url()).hash).toBe('')
      await ctx.close()
    }
  }
})
