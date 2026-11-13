import type { Page, Request } from '@playwright/test'
import { expect, test } from './fixtures'
import { addressHeld, apiSent, fragmentChanged, holdDocuments, recorder, setFragment } from './fragment-helpers'

/** A token shaped as one (16 to 256 of `A-Z a-z 0-9 _ -`) that the server does not know. */
const UNKNOWN = 'e2e-unknown-token-0123456789'
const stored = (page: Page) => page.evaluate(() => localStorage.getItem('conductor.workbenchToken'))
/** A read the page sends now: the inbox reads again when the tab is shown. */
const shown = (page: Page) => page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')))

// The workbench's own link, the token in the URL fragment ("Open in browser" from the desktop app opens `/#token=…`): taken out of
// the address before Nuxt's router reads it, in any spelling of its key, at a load and when the fragment arrives later in a tab
// already open, so no address, history state or entry of the page's, or route holds it, and the workbench is signed in with it; the
// browser's own global history and its first-navigation entry are the browser's. A later link loads the page again only when it
// changed the token this browser keeps. A fresh context each time, with no token stored.

test("the workbench token in the fragment, in any spelling: signed in, and no address, history state or entry of the page's, or route holds it", async ({ browser, state }) => {
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
    const keyed = await fragmentChanged(page, rec, fragment, 'reload')
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

test('a later link with another workbench token: the address rewritten at once, the page loaded again once, signed in with it; while that load is held, the page drawn sends only the token it was loaded with', async ({ browser, state }) => {
  const token = state.token
  const ctx = await browser.newContext()
  const page = await ctx.newPage()
  const sent: Request[] = []
  page.on('request', (r) => sent.push(r))
  const rec = await recorder(page)
  const hold = await holdDocuments(page)
  // The event stream answered at once and empty: the page reads what it may have missed and opens the stream again, after 1 s.
  await page.route('**/api/events', (route) => route.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }))
  await page.goto(`/#token=${UNKNOWN}`)
  await expect(page.getByRole('dialog', { name: 'Workbench token' })).toBeVisible()
  const clean = new URL('/', page.url()).href
  const loads = rec.loads()
  const from = sent.length
  hold.on = true
  await setFragment(page, `token=${token}`)
  // The load has started and is held: the page drawn, loaded with UNKNOWN, goes on. It rewrote the address before it asked for the load.
  expect((await hold.asked).url()).toBe(clean)
  await expect.poll(() => rec.lines.filter((l) => l.startsWith('reload '))).toEqual([`reload ${clean}`])
  // Its reads go on: the stream opened again (its backoff's bound is 15 s; the wait's is twice that).
  await expect.poll(async () => (await apiSent(sent, from)).map((r) => r.path), { timeout: 30_000 }).toContain('/api/events')
  for (const r of await apiSent(sent, from)) {
    expect(r.auth, r.path).toBe(`Bearer ${UNKNOWN}`)
    expect(r.url).not.toContain(token)
  }
  const signedIn = page.waitForResponse((r) => new URL(r.url()).pathname.startsWith('/api/') && r.status() === 200 && r.request().headers()['authorization'] === `Bearer ${token}`)
  hold.on = false
  hold.release()
  await expect.poll(rec.loads).toBe(loads + 1)
  await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
  await signedIn
  await expect(page.getByRole('dialog', { name: 'Workbench token' })).toHaveCount(0)
  expect(await stored(page)).toBe(token)
  expect(await addressHeld(page)).not.toContain(token)
  expect(rec.since()).not.toContain(token)
  expect(rec.loads()).toBe(loads + 1)
  await ctx.close()
})

test('a later link with the same workbench token, a value not shaped as one, or both keys: the address rewritten, no load, the page as it was', async ({ browser, state }) => {
  const token = state.token
  for (const fragment of [`token=${token}`, 'token=short', `token=${UNKNOWN}&pass=e2e.pass-of_ana.7`]) {
    const ctx = await browser.newContext()
    const page = await ctx.newPage()
    const rec = await recorder(page)
    await page.goto(`/#token=${token}`)
    await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
    await fragmentChanged(page, rec, fragment, 'kept')
    await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
    expect(await stored(page), fragment).toBe(token)
    expect(await page.evaluate(() => sessionStorage.getItem('conductor.identityPass')), fragment).toBeNull()
    await ctx.close()
  }
})

test("with this browser's storage refusing the token: the token of the load is held for the page, and a later link rewrites the address and changes nothing else", async ({ browser, state }) => {
  const token = state.token
  const ctx = await browser.newContext()
  const page = await ctx.newPage()
  await page.addInitScript(() => {
    const set = Storage.prototype.setItem
    Storage.prototype.setItem = function (this: Storage, k: string, v: string) {
      if (this === window.localStorage && k === 'conductor.workbenchToken') throw new DOMException('refused', 'QuotaExceededError')
      return set.call(this, k, v)
    }
  })
  const sent: Request[] = []
  page.on('request', (r) => sent.push(r))
  const rec = await recorder(page)
  const signedIn = page.waitForResponse((r) => new URL(r.url()).pathname.startsWith('/api/') && r.status() === 200 && r.request().headers()['authorization'] === `Bearer ${token}`)
  await page.goto(`/#token=${token}`)
  await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
  await signedIn
  expect(await stored(page)).toBeNull()
  await fragmentChanged(page, rec, `token=${UNKNOWN}`, 'kept')
  const from = sent.length
  await shown(page)
  await expect.poll(async () => (await apiSent(sent, from)).length).toBeGreaterThan(0)
  for (const r of await apiSent(sent, 0)) expect(r.auth, r.path).not.toBe(`Bearer ${UNKNOWN}`)
  await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
  await expect(page.getByRole('dialog', { name: 'Workbench token' })).toHaveCount(0)
  expect(await addressHeld(page)).not.toContain(UNKNOWN)
  await ctx.close()
})
