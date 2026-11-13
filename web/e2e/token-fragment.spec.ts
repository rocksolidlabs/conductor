import { expect, test } from './fixtures'
import { addressHeld, fragmentChanged, recorder } from './fragment-helpers'

// The workbench's own link, the token in the URL fragment ("Open in browser" from the desktop app opens `/#token=…`): taken out of
// the address before Nuxt's router reads it, in any spelling of its key, at a load and when the fragment arrives later in a tab
// already open, so no address, history state or route holds it, and the workbench is signed in with it. A fresh context each time,
// with no token stored.

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

test('the workbench token in a fragment that arrives after the load, in any spelling: the page loads again signed in, and nothing of it holds the token', async ({ browser, state }) => {
  const token = state.token
  for (const fragment of [`token=${token}`, `%74oken=${token}`, `%74oken=${token}&%E0`]) {
    const ctx = await browser.newContext()
    const page = await ctx.newPage()
    const rec = await recorder(page)
    await page.goto('/')
    await expect(page.getByRole('dialog', { name: 'Workbench token' })).toBeVisible()
    const signedIn = page.waitForResponse((r) => new URL(r.url()).pathname.startsWith('/api/') && r.status() === 200 && r.request().headers()['authorization'] === `Bearer ${token}`)
    const keyed = await fragmentChanged(page, rec, fragment, true)
    expect(keyed.capture, 'one capture listener of popstate').toHaveLength(1)
    expect(keyed.after.filter((l) => l.startsWith('ran ') || l.startsWith('route ')), fragment).toEqual([`ran ${keyed.capture[0]}`])
    await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
    await signedIn
    await expect(page.getByRole('dialog', { name: 'Workbench token' })).toHaveCount(0)
    expect(await page.evaluate(() => localStorage.getItem('conductor.workbenchToken'))).toBe(token)
    for (const step of ['loaded again', 'back', 'forward'] as const) {
      if (step === 'back') await page.goBack()
      if (step === 'forward') await page.goForward()
      await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
      expect(await addressHeld(page), `${fragment}, ${step}`).not.toContain(token)
      expect(rec.since(), `${fragment}, ${step}`).not.toContain(token)
      expect(new URL(page.url()).hash).toBe('')
    }
    await ctx.close()
  }
})
