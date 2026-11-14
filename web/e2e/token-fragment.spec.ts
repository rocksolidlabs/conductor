import type { Browser, BrowserContext, Page, Request } from '@playwright/test'
import { expect, test } from './fixtures'
import { addressHeld, apiSent, fragmentChanged, holdDocuments, recorder, workbenchHeld } from './fragment-helpers'

/** A token shaped as one (16 to 256 of `A-Z a-z 0-9 _ -`) that the server does not know. */
const UNKNOWN = 'e2e-unknown-token-0123456789'
const PASS = 'e2e.pass-of_ana.7'
const stored = (page: Page) => page.evaluate(() => localStorage.getItem('conductor.workbenchToken'))
/** Everything the tab's own storage holds, as text: no name of the workbench token's, and no token, belongs there. */
const inTab = (page: Page) => page.evaluate(() => JSON.stringify(Object.entries(sessionStorage)))
async function tabHoldsNoToken(page: Page, ...tokens: string[]) {
  const held = await inTab(page)
  expect(held).not.toMatch(/workbench/i)
  for (const t of tokens) expect(held).not.toContain(t)
}
/** What the workbench token's store knows of the token the page sends ('trying': a link's not answered yet), read off the store. */
const standing = (page: Page) => workbenchHeld(page).then((h) => h.standing)
/** The token the page sends, read off the store ('' none). */
const held = (page: Page) => workbenchHeld(page).then((h) => h.token)
/**
 * Another tab of `ctx` saving `token` in the browser's storage, as the gate of that tab does: a page of its own at an address of the
 * server's origin the test answers itself, so nothing of the app runs there.
 */
async function savedByAnotherTab(ctx: BrowserContext, token: string) {
  await ctx.route('**/e2e-another-tab', (r) => r.fulfill({ status: 200, contentType: 'text/html', body: '<!doctype html><title>another tab</title>' }))
  const other = await ctx.newPage()
  await other.goto('/e2e-another-tab')
  await other.evaluate((t) => localStorage.setItem('conductor.workbenchToken', t), token)
  await other.close()
}
/** A fresh context whose browser keeps `token`: its first page opens with it kept. */
async function keeping(browser: Browser, token: string) {
  const ctx = await browser.newContext()
  await savedByAnotherTab(ctx, token)
  return ctx
}
/** The gate opened from the account menu: what its field holds as it opens. */
async function gateOpened(page: Page) {
  await page.locator('[data-header-account]').click()
  await page.getByRole('menuitem', { name: /Workbench token/ }).click()
  const gate = page.getByRole('dialog', { name: 'Workbench token' })
  await expect(gate).toBeVisible()
  return gate.locator('input')
}
/** A read the page sends now: the inbox reads again when the tab is shown. */
const shown = (page: Page) => page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')))
const documents = (sent: Request[]) => sent.filter((r) => r.resourceType() === 'document').length
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
// already open, so no address, history state or entry of the page's, or route holds it; the browser's own global history and its
// first-navigation entry are the browser's. A link's token other than the page's own is sent by the page it came with and kept once
// the events stream accepts it while the browser keeps no other token; one the server refuses is dropped, and the page loads again
// with the token the browser keeps. A later link's token is taken by a page that holds none or one it has seen refused (the gate
// asks), with no load while it is tried, and changes nothing in a page that holds one, when it is the page's own, or on a decide
// address (decide-only.spec.ts). The gate's field starts from what the browser keeps. The tab's own storage holds nothing of the
// workbench token's. A fresh context each time, with no token stored unless the test keeps one. Requests are held through the
// test's own routes, never on the clock.

test("the workbench token in the fragment, in any spelling: signed in, kept, and no address, history state or entry of the page's, or route holds it", async ({ browser, state }) => {
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
      await expect.poll(() => stored(page), `${path}#${fragment}`).toBe(token)
      await expect(page.getByRole('dialog', { name: 'Workbench token' })).toHaveCount(0)
      expect(await addressHeld(page), `${path}#${fragment}`).not.toContain(token)
      expect(new URL(page.url()).hash).toBe('')
      await tabHoldsNoToken(page, token)
      await ctx.close()
    }
  }
})

test("the workbench token in a fragment that arrives after the load, in any spelling, in a tab at the gate: tried with no load, the gate gone and the token kept once accepted, and the address, the history state, the entries and the router's route hold none of it", async ({ browser, state }) => {
  const token = state.token
  for (const fragment of [`token=${token}`, `%74oken=${token}`, `%74oken=${token}&%E0`]) {
    const ctx = await browser.newContext()
    const page = await ctx.newPage()
    const sent: Request[] = []
    page.on('request', (r) => sent.push(r))
    const rec = await recorder(page)
    const gate = page.getByRole('dialog', { name: 'Workbench token' })
    await page.goto('/')
    await expect(gate).toBeVisible()
    const loads = documents(sent)
    const keyed = await fragmentChanged(page, rec, fragment, 'kept')
    expect(keyed.capture, 'one capture listener of popstate').toHaveLength(1)
    expect(keyed.after.filter((l) => l.startsWith('ran ') || l.startsWith('route ')), fragment).toEqual([`ran ${keyed.capture[0]}`])
    await expect.poll(() => stored(page), fragment).toBe(token)
    await expect(gate).toHaveCount(0)
    await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
    expect(documents(sent), fragment).toBe(loads)
    await tabHoldsNoToken(page, token)
    for (const step of ['tried', 'back', 'forward'] as const) {
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

test("a later link in a tab signed in with the kept token: the address rewritten, no load, the page's requests carry the kept token, and the browser keeps it", async ({ browser, state }) => {
  const token = state.token
  const ctx = await browser.newContext()
  const page = await ctx.newPage()
  const sent: Request[] = []
  page.on('request', (r) => sent.push(r))
  const rec = await recorder(page)
  await page.goto(`/#token=${token}`)
  await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
  await expect.poll(() => stored(page)).toBe(token)
  const loads = documents(sent)
  await fragmentChanged(page, rec, `token=${UNKNOWN}`, 'kept')
  const from = sent.length
  await shown(page)
  await expect.poll(async () => (await apiSent(sent, from)).length).toBeGreaterThan(0)
  for (const r of await apiSent(sent, from)) expect(r.auth, r.path).toBe(`Bearer ${token}`)
  for (const r of await apiSent(sent, 0)) expect(r.auth, r.path).not.toBe(`Bearer ${UNKNOWN}`)
  expect(documents(sent)).toBe(loads)
  expect(await stored(page)).toBe(token)
  await expect(page.getByRole('dialog', { name: 'Workbench token' })).toHaveCount(0)
  await tabHoldsNoToken(page, token, UNKNOWN)
  expect(await addressHeld(page)).not.toContain(UNKNOWN)
  expect(rec.since()).not.toContain(UNKNOWN)
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
    // The load's token accepted and kept before the later fragment arrives.
    await expect.poll(() => stored(page), fragment).toBe(token)
    await fragmentChanged(page, rec, fragment, 'kept')
    await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
    expect(await stored(page), fragment).toBe(token)
    expect(await standing(page), fragment).toBe('')
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
      if (this === window.localStorage && k === 'conductor.workbenchToken') {
        ;(window as unknown as { __refusedKeeps: number }).__refusedKeeps = ((window as unknown as { __refusedKeeps?: number }).__refusedKeeps ?? 0) + 1
        throw new DOMException('refused', 'QuotaExceededError')
      }
      return set.call(this, k, v)
    }
  })
  const sent: Request[] = []
  page.on('request', (r) => sent.push(r))
  const rec = await recorder(page)
  await page.goto(`/#token=${token}`)
  await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
  // The token accepted: the store has asked the browser to keep it, and the browser refused.
  await expect.poll(() => page.evaluate(() => (window as unknown as { __refusedKeeps?: number }).__refusedKeeps ?? 0)).toBeGreaterThan(0)
  expect(await stored(page)).toBeNull()
  await fragmentChanged(page, rec, `token=${UNKNOWN}`, 'kept')
  const from = sent.length
  await shown(page)
  await expect.poll(async () => (await apiSent(sent, from)).length).toBeGreaterThan(0)
  for (const r of await apiSent(sent, 0)) expect(r.auth, r.path).not.toBe(`Bearer ${UNKNOWN}`)
  await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
  await expect(page.getByRole('dialog', { name: 'Workbench token' })).toHaveCount(0)
  expect(await addressHeld(page)).not.toContain(UNKNOWN)
  // The browser keeps nothing: the gate, opened, starts empty, though the page sends the load's token.
  await expect(await gateOpened(page)).toHaveValue('')
  expect(await held(page)).toBe(token)
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

test("a load with a link's token while the browser keeps another: accepted, the page is signed in with the link's and the browser keeps the other; the gate, opened, starts from the kept one, and Save with nothing typed keeps it as it was", async ({ browser, state }) => {
  const token = state.token
  const ctx = await keeping(browser, UNKNOWN)
  const page = await ctx.newPage()
  const sent: Request[] = []
  page.on('request', (r) => sent.push(r))
  const gate = page.getByRole('dialog', { name: 'Workbench token' })
  // The page's first navigation is the link: a load.
  await page.goto(`/#token=${token}`)
  await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
  expect(documents(sent)).toBe(1)
  // The link's token accepted: the store no longer tries it.
  await expect.poll(() => standing(page)).toBe('')
  expect(await stored(page)).toBe(UNKNOWN)
  await expect.poll(() => signedInSince(sent, 0, token)).toBe(true)
  expect(triedSince(sent, 0), 'the kept token sent').toBe(false)
  await expect(gate).toHaveCount(0)
  await tabHoldsNoToken(page, token, UNKNOWN)
  const field = await gateOpened(page)
  await expect(field).toHaveValue(UNKNOWN)
  await gate.getByRole('button', { name: 'Save' }).click()
  // Saved: the page sends the kept token, the server refuses it, and the gate asks; the browser keeps what it kept.
  await expect.poll(() => held(page)).toBe(UNKNOWN)
  await expect.poll(() => standing(page)).toBe('refused')
  await expect(gate).toBeVisible()
  expect(await stored(page)).toBe(UNKNOWN)
  expect(documents(sent)).toBe(1)
  // A load without the link sends the token the browser keeps; the server refuses it, and the gate asks.
  const again = sent.length
  await page.reload()
  await expect(gate).toBeVisible()
  expect(triedSince(sent, again), 'the kept token sent').toBe(true)
  expect(await stored(page)).toBe(UNKNOWN)
  await ctx.close()
})

test("a later link in a tab at the gate, after another tab saved another token: the page is signed in with the link's, with no load, and the browser keeps the other", async ({ browser, state }) => {
  const token = state.token
  const ctx = await browser.newContext()
  const page = await ctx.newPage()
  const sent: Request[] = []
  page.on('request', (r) => sent.push(r))
  const rec = await recorder(page)
  const gate = page.getByRole('dialog', { name: 'Workbench token' })
  await page.goto('/')
  await expect(gate).toBeVisible()
  await savedByAnotherTab(ctx, UNKNOWN)
  const from = sent.length
  await fragmentChanged(page, rec, `token=${token}`, 'kept')
  await expect.poll(() => standing(page)).toBe('')
  expect(await stored(page)).toBe(UNKNOWN)
  await expect.poll(() => signedInSince(sent, from, token)).toBe(true)
  expect(triedSince(sent, 0), 'the kept token sent').toBe(false)
  await expect(gate).toHaveCount(0)
  expect(documents(sent)).toBe(1)
  await tabHoldsNoToken(page, token, UNKNOWN)
  await ctx.close()
})

test("a later link in a tab at the gate that holds none, carrying the token another tab saved meanwhile: tried with no load; accepted, the gate is gone and the browser keeps it as it was", async ({ browser, state }) => {
  const token = state.token
  const ctx = await browser.newContext()
  const page = await ctx.newPage()
  const sent: Request[] = []
  page.on('request', (r) => sent.push(r))
  const rec = await recorder(page)
  const gate = page.getByRole('dialog', { name: 'Workbench token' })
  await page.goto('/')
  await expect(gate).toBeVisible()
  await savedByAnotherTab(ctx, token)
  const from = sent.length
  await fragmentChanged(page, rec, `token=${token}`, 'kept')
  await expect(gate).toHaveCount(0)
  await expect.poll(() => standing(page)).toBe('')
  await expect.poll(() => signedInSince(sent, from, token)).toBe(true)
  expect(await stored(page)).toBe(token)
  expect(documents(sent)).toBe(1)
  await ctx.close()
})

test("a load with a link carrying the kept token: nothing changes, and the server's refusal asks at the gate with no load", async ({ browser }) => {
  const ctx = await keeping(browser, UNKNOWN)
  const page = await ctx.newPage()
  const sent: Request[] = []
  page.on('request', (r) => sent.push(r))
  const rec = await recorder(page)
  await page.goto(`/#token=${UNKNOWN}`)
  await expect(page.getByRole('dialog', { name: 'Workbench token' })).toBeVisible()
  await expect.poll(() => standing(page)).toBe('refused')
  expect(documents(sent)).toBe(1)
  expect(rec.lines.filter((l) => l.startsWith('reload '))).toEqual([])
  expect(await stored(page)).toBe(UNKNOWN)
  expect(await addressHeld(page)).not.toContain(UNKNOWN)
  await ctx.close()
})

test("a later link in a page whose kept token the server refused: carrying that token, nothing changes and the gate still asks; carrying another, it is tried with no load, and once accepted the gate is gone and the browser keeps the refused one", async ({ browser, state }) => {
  const token = state.token
  const ctx = await keeping(browser, UNKNOWN)
  const page = await ctx.newPage()
  const sent: Request[] = []
  page.on('request', (r) => sent.push(r))
  const rec = await recorder(page)
  const gate = page.getByRole('dialog', { name: 'Workbench token' })
  await page.goto('/')
  await expect(gate).toBeVisible()
  await expect.poll(() => standing(page)).toBe('refused')
  await fragmentChanged(page, rec, `token=${UNKNOWN}`, 'kept')
  expect(await standing(page)).toBe('refused')
  expect(await held(page)).toBe(UNKNOWN)
  await expect(gate).toBeVisible()
  expect(await stored(page)).toBe(UNKNOWN)
  const from = sent.length
  await fragmentChanged(page, rec, `token=${token}`, 'kept')
  await expect(gate).toHaveCount(0)
  await expect.poll(() => standing(page)).toBe('')
  await expect.poll(() => signedInSince(sent, from, token)).toBe(true)
  expect(await stored(page)).toBe(UNKNOWN)
  expect(documents(sent)).toBe(1)
  await tabHoldsNoToken(page, token, UNKNOWN)
  await ctx.close()
})

test("at every load the framework's reload state is gone from the tab's storage", async ({ browser, state }) => {
  const ctx = await browser.newContext()
  const page = await ctx.newPage()
  await page.goto(`/#token=${state.token}`)
  await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
  await page.evaluate(() => sessionStorage.setItem('nuxt:reload:state', JSON.stringify({ state: { $se2e: 'left by a reload' } })))
  await page.reload()
  await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
  expect(await page.evaluate(() => sessionStorage.getItem('nuxt:reload:state'))).toBeNull()
  await ctx.close()
})

// A reload Nuxt makes itself: its chunk-reload plugin, told of a new build (`app:manifest:update`, what its build check announces),
// turns the next navigation into a load at that address and first writes the page's state into the tab's storage
// (`nuxt:reload:state`). Every write to the tab's storage is recorded, and the storage is read through the browser's devtools while
// the next load is held.
test("a reload Nuxt makes itself, while the page holds a kept token and while it holds a link's token still tried: the tab's storage never holds either, and the page that loads is signed in with the kept one", async ({ browser, state }) => {
  const token = state.token
  for (const holding of ['kept', 'trying'] as const) {
    const ctx = await keeping(browser, token)
    const page = await ctx.newPage()
    const sent: Request[] = []
    page.on('request', (r) => sent.push(r))
    const rec = await recorder(page)
    await page.addInitScript(() => {
      const set = Storage.prototype.setItem
      Storage.prototype.setItem = function (k: string, v: string) {
        if (this === window.sessionStorage) (window as unknown as { __conductorRecord: (l: string) => void }).__conductorRecord(`tab ${k} ${v}`)
        return set.call(this, k, v)
      }
    })
    const docs = await holdDocuments(page)
    if (holding === 'trying') {
      const tried = await holdSentWith(page, UNKNOWN)
      await page.goto(`/#token=${UNKNOWN}`)
      await tried.asked
    } else await page.goto('/')
    await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
    expect(await workbenchHeld(page), holding).toEqual(holding === 'trying' ? { token: UNKNOWN, standing: 'trying' } : { token, standing: '' })
    await page.evaluate(() =>
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      (document.querySelector('#__nuxt') as any).__vue_app__.$nuxt.callHook('app:manifest:update', { id: 'e2e-new-build', timestamp: 0 }),
    )
    const before = rec.lines.length
    docs.on = true
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const pushed = page.evaluate(() => void (document.querySelector('#__nuxt') as any).__vue_app__.$nuxt.$router.push('/sessions')).catch(() => {})
    const asked = await docs.asked
    expect(new URL(asked.url()).pathname, holding).toBe('/sessions')
    const cdp = await ctx.newCDPSession(page)
    const tab = await cdp.send('DOMStorage.getDOMStorageItems', { storageId: { securityOrigin: new URL(asked.url()).origin, isLocalStorage: false } })
    const writes = rec.lines.slice(before).filter((l) => l.startsWith('tab '))
    expect(writes.some((l) => l.startsWith('tab nuxt:reload:state ')), `${holding}: Nuxt wrote its state`).toBe(true)
    expect(tab.entries.map((e) => e[0]), holding).toContain('nuxt:reload:state')
    for (const t of [token, UNKNOWN]) {
      expect(JSON.stringify(tab.entries), holding).not.toContain(t)
      expect(writes.join('\n'), holding).not.toContain(t)
    }
    const from = sent.length
    docs.on = false
    docs.release()
    await pushed
    await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
    await expect.poll(() => signedInSince(sent, from, token)).toBe(true)
    expect(triedSince(sent, from), `${holding}: the link's token after the load`).toBe(false)
    expect(await workbenchHeld(page)).toEqual({ token, standing: '' })
    expect(await stored(page)).toBe(token)
    await tabHoldsNoToken(page, token, UNKNOWN)
    expect(await page.evaluate(() => sessionStorage.getItem('nuxt:reload:state'))).toBeNull()
    await ctx.close()
  }
})

test('the gate cancelled after a refusal: the ask ends, and the next refusal opens the gate again', async ({ browser, state }) => {
  const ctx = await keeping(browser, state.token)
  const page = await ctx.newPage()
  const gate = page.getByRole('dialog', { name: 'Workbench token' })
  // The next request sent with the workbench token is refused, one each round: nothing else is.
  let refuse = 0
  await page.route(
    (u) => u.pathname.startsWith('/api/'),
    (r) => {
      if (refuse === 0 || r.request().headers()['authorization'] !== `Bearer ${state.token}`) return r.fallback()
      refuse--
      return r.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: { code: 'unauthorized', message: 'unauthorized' } }) })
    },
  )
  await page.goto('/')
  await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
  await expect(gate).toHaveCount(0)
  for (const round of [1, 2]) {
    refuse = 1
    const refused = page.waitForResponse((r) => new URL(r.url()).pathname.startsWith('/api/') && r.status() === 401)
    await shown(page)
    await refused
    await expect(gate, `refusal ${round}`).toBeVisible()
    await gate.getByRole('button', { name: 'Cancel' }).click()
    await expect(gate, `cancelled ${round}`).toHaveCount(0)
  }
  expect(await stored(page)).toBe(state.token)
  await ctx.close()
})

test('the accounts and the capacity are read again with each new token the page sends', async ({ browser, state }) => {
  const token = state.token
  const ctx = await keeping(browser, token)
  const page = await ctx.newPage()
  const sent: Request[] = []
  page.on('request', (r) => sent.push(r))
  const gate = page.getByRole('dialog', { name: 'Workbench token' })
  const readWith = async (from: number, path: string, t: string) => (await apiSent(sent, from)).some((r) => r.path === path && r.auth === `Bearer ${t}`)
  await page.goto('/settings')
  await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
  await expect.poll(() => readWith(0, '/api/accounts', token)).toBe(true)
  await expect.poll(() => readWith(0, '/api/capacity', token)).toBe(true)
  // Another token saved at the gate: the accounts are read again with it; the server refuses it, and the gate asks.
  const input = await gateOpened(page)
  await input.fill(UNKNOWN)
  await expect(input).toHaveValue(UNKNOWN)
  let from = sent.length
  await gate.getByRole('button', { name: 'Save' }).click()
  await expect.poll(() => readWith(from, '/api/accounts', UNKNOWN)).toBe(true)
  await expect(gate).toBeVisible()
  // The workbench token saved again: both are read again with it.
  await gate.locator('input').fill(token)
  await expect(gate.locator('input')).toHaveValue(token)
  from = sent.length
  await gate.getByRole('button', { name: 'Save' }).click()
  await expect(gate).toHaveCount(0)
  await expect.poll(() => readWith(from, '/api/accounts', token)).toBe(true)
  await expect.poll(() => readWith(from, '/api/capacity', token)).toBe(true)
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
  await tabHoldsNoToken(page, token, UNKNOWN)
  expect(await addressHeld(page)).not.toContain(UNKNOWN)
  expect(after()).not.toContain(UNKNOWN)
  expect(rec.loads()).toBe(loads + 2)
  await ctx.close()
})

test('a kept token the server refuses stays kept: the gate asks, a decide address the page opens is the decide page, and a load counts the kept token again', async ({ browser }) => {
  const ctx = await browser.newContext()
  const page = await ctx.newPage()
  const gate = page.getByRole('dialog', { name: 'Workbench token' })
  await page.goto('/')
  await expect(gate).toBeVisible()
  await page.evaluate((t) => localStorage.setItem('conductor.workbenchToken', t), UNKNOWN)
  await page.goto('/')
  await expect(gate).toBeVisible()
  expect(await stored(page)).toBe(UNKNOWN)
  // The page still sends the refused token; a decide address it opens itself is the decide page's.
  await page.evaluate(() => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    void (document.querySelector('#__nuxt') as any).__vue_app__.config.globalProperties.$router.push('/decide')
  })
  await expect(page.locator('[data-decide-page]')).toBeVisible()
  expect(new URL(page.url()).pathname).toBe('/decide')
  expect(await stored(page)).toBe(UNKNOWN)
  // A load counts the kept token again: while its requests are held, a decide address opens the operator's page.
  const hold = await holdSentWith(page, UNKNOWN)
  await page.goto('/decide/dr-7')
  await expect(page).toHaveURL(/\/decisions\?item=decision(:|%3A)dr-7$/)
  hold.release()
  await expect(gate).toBeVisible()
  expect(await stored(page)).toBe(UNKNOWN)
  await ctx.close()
})

test("a decide address opened with a workbench link, nothing kept: the operator's page while its token is tried; accepted, it stays and the token is kept; refused, the load without it opens the decide page for the tab's pass", async ({ browser, state }) => {
  const token = state.token
  const good = await browser.newContext()
  const page = await good.newPage()
  await page.goto(`/decide/dr-7#token=${token}`)
  await expect(page).toHaveURL(/\/decisions\?item=decision(:|%3A)dr-7$/)
  await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
  await expect.poll(() => stored(page)).toBe(token)
  await expect(page.locator('[data-decide-page]')).toHaveCount(0)
  await good.close()

  const refused = await browser.newContext()
  await refused.addInitScript((p) => sessionStorage.setItem('conductor.identityPass', p), PASS)
  const other = await refused.newPage()
  const rec = await recorder(other)
  const hold = await holdSentWith(other, UNKNOWN)
  await other.goto(`/decide/dr-7#token=${UNKNOWN}`)
  await hold.asked
  await expect(other).toHaveURL(/\/decisions\?item=decision(:|%3A)dr-7$/)
  hold.release()
  await expect.poll(rec.loads).toBe(2)
  await expect(other.locator('[data-decide-page]')).toBeVisible()
  expect(new URL(other.url()).pathname).toBe('/decide/dr-7')
  expect(await stored(other)).toBeNull()
  expect(await addressHeld(other)).not.toContain(UNKNOWN)
  await refused.close()
})

/**
 * The desktop app's bridge in `page`, answering its token as `answer` says: `released`, with `token` once the test releases it (so the
 * page runs a while with a link's token in its address); `none`, with no token; `fails`, with a failure. `__bridgeAsked` counts the
 * page's asks.
 */
function desktopBridge(page: Page, answer: 'released' | 'none' | 'fails', token: string, url: string) {
  return page.addInitScript(
    ({ answer, token, url }) => {
      const w = window as unknown as { conductorDesktop: unknown; __releaseBridge: () => void; __bridgeAsked: number }
      let release = () => {}
      const answered = new Promise<string>((r) => (release = () => r(token)))
      w.__releaseBridge = () => release()
      w.__bridgeAsked = 0
      w.conductorDesktop = {
        version: 'e2e',
        platform: 'linux',
        token: () => {
          w.__bridgeAsked++
          if (answer === 'none') return Promise.resolve('')
          if (answer === 'fails') return Promise.reject(new Error('the bridge failed'))
          return answered
        },
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
    { answer, token, url },
  )
}

test("in the desktop app the bridge's token is the workbench's: a load's link and a later one change nothing, every request carries the bridge's token or none, and the gate, opened, starts empty", async ({ browser, state }) => {
  const token = state.token
  const ctx = await browser.newContext()
  const page = await ctx.newPage()
  await desktopBridge(page, 'released', token, state.baseURL)
  const sent: Request[] = []
  page.on('request', (r) => sent.push(r))
  const rec = await recorder(page)
  await page.goto(`/#token=${UNKNOWN}`)
  await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
  // Drawn while the bridge has not answered: the page holds no token, the link's dropped.
  await expect.poll(() => page.evaluate(() => (window as unknown as { __bridgeAsked: number }).__bridgeAsked)).toBeGreaterThan(0)
  expect(await held(page)).toBe('')
  await page.evaluate(() => (window as unknown as { __releaseBridge: () => void }).__releaseBridge())
  await expect.poll(() => held(page)).toBe(token)
  await shown(page)
  await expect.poll(() => signedInSince(sent, 0, token)).toBe(true)
  await fragmentChanged(page, rec, `token=${UNKNOWN}`, 'kept')
  const from = sent.length
  await shown(page)
  await expect.poll(async () => (await apiSent(sent, from)).length).toBeGreaterThan(0)
  for (const r of await apiSent(sent, from)) expect(r.auth, r.path).toBe(`Bearer ${token}`)
  for (const r of await apiSent(sent, 0)) expect(r.auth, r.path).not.toBe(`Bearer ${UNKNOWN}`)
  expect(await stored(page)).toBeNull()
  await tabHoldsNoToken(page, token, UNKNOWN)
  expect(await addressHeld(page)).not.toContain(UNKNOWN)
  // The gate's draft starts from what the browser keeps, and the desktop app keeps nothing there: empty, though the page sends the bridge's.
  await expect(await gateOpened(page)).toHaveValue('')
  expect(await held(page)).toBe(token)
  await ctx.close()
})

test("in the desktop app with a bridge that answers no token, and with one that fails: the page holds none and sends none, a load's link and a later one change nothing, and the gate starts empty though the browser's storage holds a token", async ({ browser, state }) => {
  const token = state.token
  for (const answer of ['none', 'fails'] as const) {
    // The browser's storage of the server's origin holds a good token: the desktop app neither sends nor shows it.
    const ctx = await keeping(browser, token)
    const page = await ctx.newPage()
    await desktopBridge(page, answer, token, state.baseURL)
    const sent: Request[] = []
    page.on('request', (r) => sent.push(r))
    const rec = await recorder(page)
    const gate = page.getByRole('dialog', { name: 'Workbench token' })
    await page.goto(`/#token=${UNKNOWN}`)
    await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
    // The bridge asked and its answer settled: a read made after it, in a task of its own, sees what the store did with it.
    await expect.poll(() => page.evaluate(() => (window as unknown as { __bridgeAsked: number }).__bridgeAsked), answer).toBeGreaterThan(0)
    expect(await held(page), answer).toBe('')
    // The page holds no token: it asks at the gate, whose draft starts empty.
    await expect(gate, answer).toBeVisible()
    await expect(gate.locator('input'), answer).toHaveValue('')
    await fragmentChanged(page, rec, `token=${UNKNOWN}`, 'kept')
    expect(await held(page), answer).toBe('')
    await expect(gate, answer).toBeVisible()
    await expect(gate.locator('input'), answer).toHaveValue('')
    for (const r of await apiSent(sent, 0)) expect(r.auth, `${answer} ${r.path}`).toBe('')
    expect(await stored(page), answer).toBe(token)
    expect(await addressHeld(page)).not.toContain(UNKNOWN)
    await ctx.close()
  }
})

test("the gate's draft when a 401 opens it, and when a page asks for it: the token the browser keeps, as when the person opens it, not the link's token the page sends", async ({ browser, state }) => {
  const token = state.token
  for (const how of ['401', 'page'] as const) {
    const ctx = await keeping(browser, UNKNOWN)
    const page = await ctx.newPage()
    const gate = page.getByRole('dialog', { name: 'Workbench token' })
    if (how === 'page') {
      // The stream and the sessions' read fail with no refusal, so the page shows its error and its "Set token", which asks for the gate.
      await page.route(
        (u) => u.pathname === '/api/events' || u.pathname === '/api/sessions',
        (r) => r.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: { code: 'unavailable', message: 'unavailable' } }) }),
      )
    }
    await page.goto(`/#token=${token}`)
    await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
    if (how === '401') {
      // The link's token accepted and sent while the browser keeps the other; then the server refuses it.
      await expect.poll(() => standing(page)).toBe('')
      await page.route(
        (u) => u.pathname.startsWith('/api/'),
        (r) => (r.request().headers()['authorization'] === `Bearer ${token}` ? r.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: { code: 'unauthorized', message: 'unauthorized' } }) }) : r.fallback()),
      )
      await shown(page)
    } else {
      await expect(gate).toHaveCount(0)
      await page.getByRole('button', { name: 'Set token' }).click()
    }
    await expect(gate, how).toBeVisible()
    await expect(gate.locator('input'), how).toHaveValue(UNKNOWN)
    expect(await held(page), how).toBe(token)
    expect(await stored(page), how).toBe(UNKNOWN)
    await ctx.close()
  }
})
