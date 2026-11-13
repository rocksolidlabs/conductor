import type { Page, Request } from '@playwright/test'
import { expect, test } from './fixtures'
import { addressHeld, apiSent, fragmentChanged, holdDocuments, recorder, setFragment } from './fragment-helpers'

/** A token shaped as one (16 to 256 of `A-Z a-z 0-9 _ -`) that the server does not know. */
const UNKNOWN = 'e2e-unknown-token-0123456789'
const stored = (page: Page) => page.evaluate(() => localStorage.getItem('conductor.workbenchToken'))
/** A read the page sends now: the inbox reads again when the tab is shown. */
const shown = (page: Page) => page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')))
/** A later link's token the tab holds for its next load. */
const handedOn = (page: Page) => page.evaluate(() => sessionStorage.getItem('conductor.workbenchTokenLink'))
/** The index in `sent` of the document request of the page drawn now. */
const lastDocument = (sent: Request[]) => sent.map((r) => r.resourceType() === 'document').lastIndexOf(true)
/** Whether a request to the API from index `from` on, sent with `token`, was answered 200. */
async function signedInSince(sent: Request[], from: number, token: string) {
  for (const r of sent.slice(from))
    if (new URL(r.url()).pathname.startsWith('/api/') && r.headers()['authorization'] === `Bearer ${token}` && (await r.response())?.status() === 200) return true
  return false
}
/**
 * Whether a request to the API from index `from` on went with UNKNOWN, read off the headers the page gave it (a request of a page
 * since loaded again never reports its full headers).
 */
const triedSince = (sent: Request[], from: number) => sent.slice(from).some((r) => new URL(r.url()).pathname.startsWith('/api/') && r.headers()['authorization'] === `Bearer ${UNKNOWN}`)
/** Holds every request to the API sent with `token` until `release()`; `asked` resolves at the first. */
async function holdSentWith(page: Page, token: string) {
  let release = () => {}
  let asked: () => void = () => {}
  const h = { released: new Promise<void>((r) => (release = r)), asked: new Promise<void>((r) => (asked = r)), release: () => release() }
  await page.route(
    (u) => u.pathname.startsWith('/api/'),
    async (route) => {
      if (route.request().headers()['authorization'] === `Bearer ${token}`) {
        asked()
        await h.released
      }
      return route.fallback()
    },
  )
  return h
}

// The workbench's own link, the token in the URL fragment ("Open in browser" from the desktop app opens `/#token=…`): taken out of
// the address before Nuxt's router reads it, in any spelling of its key, at a load and when the fragment arrives later in a tab
// already open, so no address, history state or entry of the page's, or route holds it, and the workbench is signed in with it; the
// browser's own global history and its first-navigation entry are the browser's. A link's token is kept once the server accepts it;
// one it refuses is dropped, and the page loads again with the token the browser keeps. A later link loads the page again, with its
// token handed to that load, only when it is neither the token the page sends nor the one the browser keeps. A fresh context each
// time, with no token stored. Requests are held through the test's own routes, never on the clock.

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

test("the workbench token in a fragment that arrives after the load, in any spelling: the page loads again signed in, and the address, the history state, the entries and the router's route hold none of it", async ({ browser, state }) => {
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

test('a later link with a token the server refuses: the address rewritten at once, the page loaded again and the token tried, then loaded once more with the kept token; while the first load is held, the page drawn sends only the token it was loaded with', async ({ browser, state }) => {
  const token = state.token
  const ctx = await browser.newContext()
  const page = await ctx.newPage()
  const sent: Request[] = []
  page.on('request', (r) => sent.push(r))
  const rec = await recorder(page)
  const hold = await holdDocuments(page)
  // The event stream sent with the token answered at once and empty: the page reads what it may have missed and opens the stream
  // again, after 1 s. Sent with another token, it goes to the server.
  await page.route('**/api/events', (route) => (route.request().headers()['authorization'] === `Bearer ${token}` ? route.fulfill({ status: 200, contentType: 'text/event-stream', body: '' }) : route.fallback()))
  await page.goto(`/#token=${token}`)
  await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
  await expect.poll(() => stored(page)).toBe(token)
  const clean = new URL('/', page.url()).href
  const loads = rec.loads()
  const from = sent.length
  hold.on = true
  await setFragment(page, `token=${UNKNOWN}`)
  // The load has started and is held: the page drawn, loaded with the token, goes on. It rewrote the address before it asked for the load.
  expect((await hold.asked).url()).toBe(clean)
  await expect.poll(() => rec.lines.filter((l) => l.startsWith('reload '))).toEqual([`reload ${clean}`])
  // Its reads go on: the stream opened again (its backoff's bound is 15 s; the wait's is twice that).
  await expect.poll(async () => (await apiSent(sent, from)).map((r) => r.path), { timeout: 30_000 }).toContain('/api/events')
  for (const r of await apiSent(sent, from)) {
    expect(r.auth, r.path).toBe(`Bearer ${token}`)
    expect(r.url).not.toContain(UNKNOWN)
  }
  const held = sent.length
  hold.on = false
  hold.release()
  // The load tries the later link's token; the server refuses it, and the page loads again at the same address, with the kept token.
  await expect.poll(rec.loads).toBe(loads + 2)
  await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
  await expect.poll(() => signedInSince(sent, lastDocument(sent), token)).toBe(true)
  expect(triedSince(sent, held), 'the later token tried').toBe(true)
  await expect(page.getByRole('dialog', { name: 'Workbench token' })).toHaveCount(0)
  expect(await stored(page)).toBe(token)
  expect(await handedOn(page)).toBeNull()
  expect(await addressHeld(page)).not.toContain(UNKNOWN)
  expect(rec.since()).not.toContain(UNKNOWN)
  expect(rec.lines.filter((l) => l.startsWith('reload '))).toEqual([`reload ${clean}`, `reload ${clean}`])
  expect(rec.loads()).toBe(loads + 2)
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

test("a link's token and nothing kept: not kept while its first answers are held, kept once they are answered", async ({ browser, state }) => {
  const token = state.token
  const ctx = await browser.newContext()
  const page = await ctx.newPage()
  const hold = await holdSentWith(page, token)
  await page.goto(`/#token=${token}`)
  await hold.asked
  await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
  expect(await stored(page)).toBeNull()
  hold.release()
  await expect.poll(() => stored(page)).toBe(token)
  await expect(page.getByRole('dialog', { name: 'Workbench token' })).toHaveCount(0)
  expect(await addressHeld(page)).not.toContain(token)
  await ctx.close()
})

test('a load with a token the server refuses, the browser keeping a good one: the page loads again at its address with the kept token, signed in, and nothing keeps the refused one', async ({ browser, state }) => {
  const token = state.token
  const ctx = await browser.newContext()
  const page = await ctx.newPage()
  const sent: Request[] = []
  page.on('request', (r) => sent.push(r))
  const rec = await recorder(page)
  await page.goto(`/#token=${token}`)
  await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
  await expect.poll(() => stored(page)).toBe(token)
  const loads = rec.loads()
  const from = sent.length
  await page.goto(`/sessions#token=${UNKNOWN}`)
  const clean = new URL('/sessions', page.url()).href
  await expect.poll(rec.loads).toBe(loads + 2)
  // What the page did after the load the test opened, whose address is the link.
  const at = rec.lines.indexOf(`load ${clean}#token=${UNKNOWN}`)
  expect(at).toBeGreaterThan(0)
  const after = () => rec.lines.slice(at + 1).join('\n')
  await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
  await expect.poll(() => signedInSince(sent, lastDocument(sent), token)).toBe(true)
  expect(triedSince(sent, from), "the link's token tried").toBe(true)
  expect(rec.lines.filter((l) => l.startsWith('reload '))).toEqual([`reload ${clean}`])
  await expect(page.getByRole('dialog', { name: 'Workbench token' })).toHaveCount(0)
  expect(await stored(page)).toBe(token)
  expect(await handedOn(page)).toBeNull()
  expect(await addressHeld(page)).not.toContain(UNKNOWN)
  expect(after()).not.toContain(UNKNOWN)
  expect(rec.loads()).toBe(loads + 2)
  await ctx.close()
})

test("a kept token the server refuses is removed, a decide address then opens the decide page, and a token kept later is not removed by the refusal of a request sent before it", async ({ browser, state }) => {
  const ctx = await browser.newContext()
  const page = await ctx.newPage()
  const gate = page.getByRole('dialog', { name: 'Workbench token' })
  await page.goto('/')
  await expect(gate).toBeVisible()
  await page.evaluate((t) => localStorage.setItem('conductor.workbenchToken', t), UNKNOWN)
  await page.goto('/')
  await expect(gate).toBeVisible()
  await expect.poll(() => stored(page)).toBeNull()
  // The page still sends the refused token; a decide address it opens itself is the decide page's.
  await page.evaluate(() => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    void (document.querySelector('#__nuxt') as any).__vue_app__.config.globalProperties.$router.push('/decide')
  })
  await expect(page.locator('[data-decide-page]')).toBeVisible()
  expect(new URL(page.url()).pathname).toBe('/decide')
  await ctx.close()

  const later = await browser.newContext()
  const other = await later.newPage()
  await other.goto('/')
  await expect(other.getByRole('dialog', { name: 'Workbench token' })).toBeVisible()
  await other.evaluate((t) => localStorage.setItem('conductor.workbenchToken', t), UNKNOWN)
  const hold = await holdSentWith(other, UNKNOWN)
  await other.goto('/')
  await hold.asked
  // Another page of this browser keeps the good token while this page's requests are on their way; their refusals land after.
  await other.evaluate((t) => localStorage.setItem('conductor.workbenchToken', t), state.token)
  hold.release()
  await expect(other.getByRole('dialog', { name: 'Workbench token' })).toBeVisible()
  expect(await stored(other)).toBe(state.token)
  await later.close()
})

test("in the desktop app the bridge's token is the workbench's: a link's token is kept nowhere, and a later link changes nothing", async ({ browser, state }) => {
  const token = state.token
  const ctx = await browser.newContext()
  const page = await ctx.newPage()
  await page.addInitScript(
    ({ token, url }) => {
      ;(window as unknown as { conductorDesktop: unknown }).conductorDesktop = {
        version: 'e2e',
        platform: 'linux',
        token: async () => token,
        openExternal: async () => {},
        openUrl: async () => ({ outcome: 'browser' }),
        browsers: async () => [],
        settings: { get: async () => ({}), set: async (p: object) => p },
        restartServer: async () => {},
        openInBrowser: async () => {},
        showLog: async () => {},
        serverState: async () => ({ state: 'running', url, failures: 0 }),
        notice: async () => '',
        ice: async () => ({ forwarding: false, port: 0, publicIp: '', wslAddress: '', firewall: 'unknown' }),
        allowIceFirewall: async () => 'unknown',
        versions: async () => ({ app: 'e2e', electron: '', node: '', chrome: '', server: '' }),
        onServerState: () => () => {},
      }
    },
    { token, url: state.baseURL },
  )
  const sent: Request[] = []
  page.on('request', (r) => sent.push(r))
  const rec = await recorder(page)
  await page.goto(`/#token=${UNKNOWN}`)
  await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
  await expect.poll(() => signedInSince(sent, 0, token)).toBe(true)
  await fragmentChanged(page, rec, `token=${UNKNOWN}`, 'kept')
  const from = sent.length
  await shown(page)
  await expect.poll(async () => (await apiSent(sent, from)).length).toBeGreaterThan(0)
  for (const r of await apiSent(sent, from)) expect(r.auth, r.path).toBe(`Bearer ${token}`)
  for (const r of await apiSent(sent, 0)) expect(r.auth, r.path).not.toBe(`Bearer ${UNKNOWN}`)
  expect(await stored(page)).toBeNull()
  expect(await handedOn(page)).toBeNull()
  expect(await addressHeld(page)).not.toContain(UNKNOWN)
  await ctx.close()
})
