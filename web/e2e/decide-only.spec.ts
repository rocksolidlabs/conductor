import type { Browser, BrowserContext, Page, Request, Route } from '@playwright/test'
import { expect, test } from './fixtures'
import { addressHeld, apiSent, fragmentChanged, holdDocuments, recorder, setFragment } from './fragment-helpers'

// The decide page and a pass taken from the fragment (docs/plans/decide-grant.md, "web/app", Tests per surface): on the shipped
// build, which registers no identity provider, so the real server refuses every pass (`401`); the page's own behaviour is proved
// against the four decide routes stubbed in the browser (`route`), every other request recorded. No clock is waited on: a read is
// driven by a `focus` event the spec dispatches once the page has drawn who is signed in.

const PASS = 'e2e.pass-of_ana.7'
/** Another person's pass: the stub answers `/api/me` by the pass it is sent. */
const OTHER = 'e2e.pass-of_bo.9'
const ENDED = 'Your access to this server has ended. Open it again from your workspace page.'
const FOUR = [/^\/api\/me$/, /^\/api\/me\/decisions$/, /^\/api\/decisions\/dr-\d+$/, /^\/api\/decisions\/dr-\d+\/respond$/]

type View = Record<string, unknown> & { id: string; rev: number }
const approval = (id: string, rev = 1, extra: Partial<View> = {}): View => ({ id, rev, state: 'open', type: 'approval', question: `May ${id} go out?`, options: [{ label: 'Approve', tradeoffs: 'ships today' }, { label: 'Reject' }], createdAt: '2026-11-12T00:00:00Z', own: [], ...extra })

/** A tab with no workbench token (or with one), every request it sends recorded with its Authorization header and body. */
async function tab(browser: Browser, workbenchToken?: string): Promise<{ ctx: BrowserContext; page: Page; sent: Request[] }> {
  const ctx = await browser.newContext()
  if (workbenchToken) await ctx.addInitScript((t) => localStorage.setItem('conductor.workbenchToken', t), workbenchToken)
  const page = await ctx.newPage()
  const sent: Request[] = []
  page.on('request', (r) => sent.push(r))
  return { ctx, page, sent }
}

/** Every request: no pass in a URL; none to an events stream; every one to the API one of the four, a pass of `passes` in its header. */
async function checkSent(sent: Request[], passes = [PASS]) {
  for (const r of sent) {
    const u = new URL(r.url())
    for (const p of [PASS, OTHER]) expect(r.url(), 'a pass in a URL').not.toContain(p)
    expect(u.pathname).not.toMatch(/events/)
    if (!u.pathname.startsWith('/api/')) continue
    expect(FOUR.some((re) => re.test(u.pathname)), u.pathname).toBe(true)
    expect(passes.map((p) => `Bearer ${p}`)).toContain(await r.headerValue('authorization'))
  }
}

/** The four routes stubbed: `me`, `list`, each decision's read in `views` (a status beside a body), its respond in `respond`. */
function stub(page: Page) {
  const s = {
    list: { decisions: [] as Array<{ id: string; rev: number }> } as Record<string, unknown>,
    views: new Map<string, { status: number; body: unknown }>(),
    respond: async (_id: string, _body: string, route: Route) => route.abort(),
    statuses: [] as number[],
    listStatus: 200,
    /** `/api/me`'s refusal while set: its status and body. */
    meRefusal: null as null | { status: number; body: unknown },
  }
  const reply = (route: Route, status: number, body: unknown) => {
    s.statuses.push(status)
    return route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })
  }
  return page
    .route(
      (u) => u.pathname.startsWith('/api/'),
      async (route) => {
        const path = new URL(route.request().url()).pathname
        let m: RegExpExecArray | null
        if (path === '/api/me') {
          if (s.meRefusal) return reply(route, s.meRefusal.status, s.meRefusal.body)
          const auth = await route.request().headerValue('authorization')
          if (auth === `Bearer ${OTHER}`) return reply(route, 200, { subject: 'bo', name: 'Bo', grants: ['decide'] })
          return reply(route, 200, { subject: 'ana', name: 'Ana​ ', grants: ['decide'] })
        }
        if (path === '/api/me/decisions') return reply(route, s.listStatus, s.listStatus === 200 ? s.list : { error: { code: 'ledger_unavailable', message: 'the decisions could not be read' } })
        if ((m = /^\/api\/decisions\/(dr-\d+)$/.exec(path))) {
          const v = s.views.get(m[1]!) ?? { status: 404, body: { error: { code: 'not_found', message: 'no such decision' } } }
          return reply(route, v.status, v.body)
        }
        if ((m = /^\/api\/decisions\/(dr-\d+)\/respond$/.exec(path))) return s.respond(m[1]!, route.request().postData() ?? '', route)
        return route.abort()
      },
    )
    .then(() => ({ s, reply }))
}

const card = (page: Page, id: string) => page.locator(`[data-decide-card][data-decision-id="${id}"]`)
/** A read of the list and the open cards, driven by the window's focus once the page is drawn. */
const focus = (page: Page) => page.evaluate(() => window.dispatchEvent(new Event('focus')))

const signedIn = (page: Page) => page.locator('[data-decide-signed-in]')

test("a pass in a fragment that arrives after the load, in any spelling: the page loads again signed in with it, and the address, the history state, the entries and the router's route hold neither pass", async ({ browser }) => {
  for (const fragment of [`pass=${OTHER}`, `%70ass=${OTHER}`, `%70ass=${OTHER}&%E0`]) {
    const { ctx, page, sent } = await tab(browser)
    const rec = await recorder(page)
    await stub(page)
    await page.goto(`/decide#pass=${PASS}`)
    await expect(signedIn(page)).toHaveText('Signed in as Ana')
    await expect(page.getByText('Nothing awaits your answer here.')).toBeVisible()

    // A fragment with no key goes to the router: both listeners run, the plugin's (capture) first.
    const plain = await fragmentChanged(page, rec, 'x=1', 'route')
    expect(plain.capture, 'one capture listener of popstate').toHaveLength(1)
    expect(plain.bubble.length, "the router's listener").toBeGreaterThan(0)
    expect(plain.after.filter((l) => l.startsWith('ran ')), 'the order the browser ran them in').toEqual([plain.capture[0]!, ...plain.bubble].map((i) => `ran ${i}`))

    // A fragment with the key: the plugin's listener alone, the address rewritten, the page loaded again.
    const keyed = await fragmentChanged(page, rec, fragment, 'reload')
    expect(keyed.after.filter((l) => l.startsWith('ran ') || l.startsWith('route ')), fragment).toEqual([`ran ${keyed.capture[0]}`])
    await expect(signedIn(page)).toHaveText('Signed in as Bo')
    expect(await page.evaluate(() => sessionStorage.getItem('conductor.identityPass'))).toBe(OTHER)
    for (const step of ['loaded again', 'back', 'forward'] as const) {
      if (step === 'back') await page.goBack()
      if (step === 'forward') await page.goForward()
      await expect(signedIn(page)).toHaveText('Signed in as Bo')
      const held = await addressHeld(page)
      for (const p of [PASS, OTHER]) {
        expect(held, `${fragment}, ${step}`).not.toContain(p)
        expect(rec.since(), `${fragment}, ${step}`).not.toContain(p)
      }
      // Back is the entry of the fragment with no key.
      expect(new URL(page.url()).hash).toBe(step === 'back' ? '#x=1' : '')
    }
    await checkSent(sent, [PASS, OTHER])
    await ctx.close()
  }
})

test('a fragment that holds both keys, at a load and later: neither is taken, and the address is clean', async ({ browser, state }) => {
  const both = `pass=${OTHER}&token=${state.token}`
  const stores = (page: Page) => page.evaluate(() => [sessionStorage.getItem('conductor.identityPass'), localStorage.getItem('conductor.workbenchToken')])

  const first = await tab(browser)
  const rec1 = await recorder(first.page)
  await stub(first.page)
  await first.page.goto(`/decide#${both}`)
  await expect(first.page.getByText(ENDED)).toBeVisible()
  expect(await stores(first.page)).toEqual([null, null])
  const held1 = await addressHeld(first.page)
  for (const secret of [OTHER, state.token]) {
    expect(held1).not.toContain(secret)
    expect(rec1.since()).not.toContain(secret)
  }
  for (const r of first.sent) {
    for (const secret of [OTHER, state.token]) expect(r.url()).not.toContain(secret)
    expect([`Bearer ${OTHER}`, `Bearer ${state.token}`]).not.toContain(await r.headerValue('authorization'))
  }
  await first.ctx.close()

  const { ctx, page, sent } = await tab(browser)
  const rec = await recorder(page)
  await stub(page)
  await page.goto(`/decide#pass=${PASS}`)
  await expect(signedIn(page)).toHaveText('Signed in as Ana')
  await fragmentChanged(page, rec, both, 'kept')
  await expect(signedIn(page)).toHaveText('Signed in as Ana')
  expect(await stores(page)).toEqual([PASS, null])
  const held = await addressHeld(page)
  for (const secret of [PASS, OTHER, state.token]) {
    expect(held).not.toContain(secret)
    expect(rec.since()).not.toContain(secret)
  }
  expect(new URL(page.url()).hash).toBe('')
  await checkSent(sent)
  await ctx.close()
})

/** The pass the tab's storage holds, for the page loaded next. */
const passStored = (page: Page) => page.evaluate(() => sessionStorage.getItem('conductor.identityPass'))
/** A decide page signed in as Ana with dr-7 drawn and a draft typed into it; the four routes stubbed. */
async function signedInWithDraft(page: Page) {
  const stubbed = await stub(page)
  stubbed.s.list = { decisions: [{ id: 'dr-7', rev: 1 }] }
  stubbed.s.views.set('dr-7', { status: 200, body: approval('dr-7', 1, { type: 'clarification', options: [] }) })
  await page.goto(`/decide#pass=${PASS}`)
  await expect(signedIn(page)).toHaveText('Signed in as Ana')
  const text = card(page, 'dr-7').locator('textarea[data-decide-text]')
  await text.fill('btree, keyed by run')
  return { ...stubbed, text }
}

test("a later link with another pass: the address rewritten at once, the page loaded again once, signed in as it; while that load is held, the page drawn sends only the pass it was loaded with", async ({ browser }) => {
  const { ctx, page, sent } = await tab(browser)
  const rec = await recorder(page)
  const hold = await holdDocuments(page)
  await signedInWithDraft(page)
  const send = card(page, 'dr-7').locator('[data-decide-send]')
  await expect(send).toBeEnabled()
  const box = (await send.boundingBox())!
  const clean = new URL('/decide', page.url()).href
  const loads = rec.loads()
  const from = sent.length
  hold.on = true
  await setFragment(page, `pass=${OTHER}`)
  // The load has started and is held: the page drawn, loaded with PASS, goes on. It rewrote the address before it asked for the load.
  expect((await hold.asked).url()).toBe(clean)
  await expect.poll(() => rec.lines.filter((l) => l.startsWith('reload '))).toEqual([`reload ${clean}`])
  // Send pressed, and a read falling due on the page's own schedule (10 s after the last began; the wait's bound is twice that).
  await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2)
  await expect.poll(async () => (await apiSent(sent, from)).map((r) => r.path), { timeout: 20_000 }).toEqual(expect.arrayContaining(['/api/decisions/dr-7/respond', '/api/me/decisions', '/api/decisions/dr-7']))
  for (const r of await apiSent(sent, from)) expect(r.auth, r.path).toBe(`Bearer ${PASS}`)
  hold.on = false
  hold.release()
  await expect.poll(rec.loads).toBe(loads + 1)
  await expect(signedIn(page)).toHaveText('Signed in as Bo')
  expect(await passStored(page)).toBe(OTHER)
  for (const p of [PASS, OTHER]) {
    expect(await addressHeld(page)).not.toContain(p)
    expect(rec.since()).not.toContain(p)
  }
  expect(rec.loads()).toBe(loads + 1)
  await checkSent(sent, [PASS, OTHER])
  await ctx.close()
})

test('a later link with the same pass, a value not shaped as a pass, or both keys: the address rewritten, no load, the draft typed before still there, and an answer whose outcome was not known still held', async ({ browser, state }) => {
  for (const fragment of [`pass=${PASS}`, 'pass=not!a*pass', `pass=${OTHER}&token=${state.token}`]) {
    const { ctx, page, sent } = await tab(browser)
    const rec = await recorder(page)
    const { text } = await signedInWithDraft(page)
    // The draft sent, its respond aborted by the stub: the answer is held, its outcome not known, with Send again.
    const c = card(page, 'dr-7')
    await c.locator('[data-decide-send]').click()
    await expect(c.locator('[data-decide-unknown]')).toContainText('Not known whether your answer to dr-7 was recorded')
    await fragmentChanged(page, rec, fragment, 'kept')
    await expect(c.locator('[data-decide-unknown]'), fragment).toContainText('Not known whether your answer to dr-7 was recorded')
    await expect(c.getByRole('button', { name: 'Send again' }), fragment).toBeEnabled()
    await expect(text).toHaveValue('btree, keyed by run')
    await expect(signedIn(page)).toHaveText('Signed in as Ana')
    expect(await passStored(page), fragment).toBe(PASS)
    expect(await addressHeld(page)).not.toContain(OTHER)
    await checkSent(sent)
    await ctx.close()
  }
})

test("with the tab's storage refusing writes: the pass of the load is held for the page, and a later link rewrites the address and changes nothing else", async ({ browser }) => {
  const { ctx, page, sent } = await tab(browser)
  await page.addInitScript(() => {
    const set = Storage.prototype.setItem
    Storage.prototype.setItem = function (this: Storage, k: string, v: string) {
      if (this === window.sessionStorage) throw new DOMException('refused', 'QuotaExceededError')
      return set.call(this, k, v)
    }
  })
  const rec = await recorder(page)
  const { text } = await signedInWithDraft(page)
  expect(await passStored(page)).toBeNull()
  await fragmentChanged(page, rec, `pass=${OTHER}`, 'kept')
  await expect(text).toHaveValue('btree, keyed by run')
  await expect(signedIn(page)).toHaveText('Signed in as Ana')
  await focus(page)
  await expect.poll(async () => (await apiSent(sent, 0)).filter((r) => r.path === '/api/decisions/dr-7').length).toBeGreaterThan(1)
  expect(await addressHeld(page)).not.toContain(OTHER)
  await checkSent(sent)
  await ctx.close()
})

test("a 401 to a read sent with the pass the page was loaded with, landing after a later link stored another: the next load is signed in as the other", async ({ browser }) => {
  const { ctx, page, sent } = await tab(browser)
  const rec = await recorder(page, ['[data-decide-ended]'])
  const hold = await holdDocuments(page)
  const { s, reply } = await signedInWithDraft(page)
  // The next list read, sent with PASS, is held until the spec answers it; the reads after it go to the stub.
  let answerList: (status: number) => void = () => {}
  const listHeld = new Promise<void>((held) => {
    void page.route('**/api/me/decisions', (route) => {
      if (route.request().headers()['authorization'] !== `Bearer ${PASS}`) return route.fallback()
      held()
      answerList = (status) => void reply(route, status, status === 401 ? { error: { code: 'unauthorized', message: 'unauthorized' } } : s.list)
    })
  })
  await focus(page)
  await listHeld
  const loads = rec.loads()
  hold.on = true
  await setFragment(page, `pass=${OTHER}`)
  await hold.asked
  answerList(401)
  // The page drawn ends at the 401, before the load goes on (the one held is the only one since).
  await expect.poll(() => rec.lines).toContain('drawn [data-decide-ended]')
  expect(rec.loads()).toBe(loads)
  hold.on = false
  hold.release()
  await expect.poll(rec.loads).toBe(loads + 1)
  await expect(signedIn(page)).toHaveText('Signed in as Bo')
  expect(await passStored(page)).toBe(OTHER)
  await checkSent(sent, [PASS, OTHER])
  await ctx.close()
})

test('at 390 wide: a type the protocol does not name and a time that does not parse draw nothing, and nothing is wider than the card', async ({ browser }) => {
  const { ctx, page, sent } = await tab(browser)
  await page.setViewportSize({ width: 390, height: 844 })
  const { s } = await stub(page)
  s.list = { decisions: [{ id: 'dr-7', rev: 1 }] }
  s.views.set('dr-7', { status: 200, body: approval('dr-7', 1, { type: 'x'.repeat(400), createdAt: 'not-a-time', question: 'q'.repeat(400) }) })
  await page.goto(`/decide#pass=${PASS}`)
  const c = card(page, 'dr-7')
  await expect(c.locator('[data-decide-question]')).toBeVisible()
  await expect(c.getByText('Invalid Date')).toHaveCount(0)
  await expect(c.getByText(/^Asked /)).toHaveCount(0)
  await expect(c.getByText('x'.repeat(400))).toHaveCount(0)
  const past = await c.evaluate((el) => {
    const box = el.getBoundingClientRect()
    return Array.from(el.querySelectorAll('*')).filter((e) => e.getBoundingClientRect().right > box.right + 0.5).map((e) => `${e.tagName} ${e.getAttribute('class')}`)
  })
  expect(past).toEqual([])
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390)
  await checkSent(sent)
  await ctx.close()
})

// What the page holds is its address, its own history state and entries, and its router's route; the browser's own global history
// and its first-navigation entry are the browser's.
test("the pass in the fragment, in any spelling: once the page has drawn, no address, history state or entry of the page's, or route holds it", async ({ browser }) => {
  for (const fragment of [`pass=${PASS}`, `%70ass=${PASS}`, `%70ass=${PASS}&%E0`, `%70ass=${PASS}&%`]) {
    const { ctx, page, sent } = await tab(browser)
    const { s } = await stub(page)
    s.list = { decisions: [] }
    await page.goto(`/decide#${fragment}`)
    await expect(page.locator('[data-decide-signed-in]')).toHaveText('Signed in as Ana')
    await expect(page.getByText('Nothing awaits your answer here.')).toBeVisible()
    expect(await addressHeld(page), fragment).not.toContain(PASS)
    expect(new URL(page.url()).hash).toBe('')
    await checkSent(sent)
    await ctx.close()
  }
})

test('at 390 wide: a list that fails from its first read says so, the title, and long words wrap inside the card', async ({ browser }) => {
  const { ctx, page, sent } = await tab(browser)
  await page.setViewportSize({ width: 390, height: 844 })
  const { s } = await stub(page)
  s.listStatus = 503
  await page.goto(`/decide#pass=${PASS}`)
  await expect(page.locator('[data-decide-signed-in]')).toHaveText('Signed in as Ana')
  await expect(page.locator('[data-decide-list-failed]')).toContainText('The decisions awaiting you could not be read. Trying again.')
  await expect(page).toHaveTitle('Decide · Conductor')

  const long = (w: string) => w.repeat(300 / w.length)
  s.listStatus = 200
  s.list = { decisions: [{ id: 'dr-7', rev: 1 }] }
  s.views.set('dr-7', { status: 200, body: approval('dr-7', 1, { uncertainty: long('uncertain'), authorizes: long('authorize'), recommendation: 0, options: [{ label: long('option'), tradeoffs: long('tradeoff') }, { label: 'Reject' }] }) })
  await focus(page)
  const c = card(page, 'dr-7')
  await expect(c.getByText('May dr-7 go out?')).toBeVisible()
  await expect(page.locator('[data-decide-list-failed]')).toHaveCount(0)
  const past = await c.evaluate((el) => {
    const box = el.getBoundingClientRect()
    return Array.from(el.querySelectorAll('*')).filter((e) => e.getBoundingClientRect().right > box.right + 0.5).map((e) => `${e.tagName} ${e.getAttribute('class')}`)
  })
  expect(past).toEqual([])
  await checkSent(sent)
  await ctx.close()
})

test('a read that fails before it ever succeeded says so where its result goes: who the pass is, a card, and a refusal', async ({ browser }) => {
  const { ctx, page, sent } = await tab(browser)
  const { s } = await stub(page)
  s.meRefusal = { status: 503, body: { error: { code: 'ledger_unavailable', message: '' } } }
  s.list = { decisions: [{ id: 'dr-7', rev: 1 }] }
  s.views.set('dr-7', { status: 503, body: { error: { code: 'ledger_unavailable', message: '' } } })
  await page.goto(`/decide/dr-7#pass=${PASS}`)
  await expect(page.locator('[data-decide-me-failed]')).toContainText('Your access could not be checked. Trying again.')
  await expect(page.getByText('Checking your access…')).toHaveCount(0)
  s.meRefusal = null
  await focus(page)
  await expect(signedIn(page)).toHaveText('Signed in as Ana')
  await expect(page.locator('[data-decide-me-failed]')).toHaveCount(0)
  await expect(card(page, 'dr-7').locator('[data-decide-read-failed]')).toContainText('dr-7 could not be read. Trying again.')
  s.views.set('dr-7', { status: 200, body: approval('dr-7') })
  await focus(page)
  await expect(card(page, 'dr-7').getByText('May dr-7 go out?')).toBeVisible()
  await expect(card(page, 'dr-7').locator('[data-decide-read-failed]')).toHaveCount(0)
  await checkSent(sent)
  await ctx.close()

  const refused = await tab(browser)
  const r = await stub(refused.page)
  r.s.meRefusal = { status: 403, body: { error: { code: 'switchyard', message: 'this server coordinates hosted sessions and launches none' } } }
  await refused.page.goto(`/decide#pass=${PASS}`)
  await expect(refused.page.locator('[data-decide-list-refused]')).toContainText('This server serves no decisions to this pass.')
  await expect(refused.page.getByText('Checking your access…')).toHaveCount(0)
  await checkSent(refused.sent)
  await refused.ctx.close()
})

test('against the server as shipped: the pass leaves the address bar, the server refuses it, the page says access ended', async ({ browser }) => {
  const { ctx, page, sent } = await tab(browser)
  const me = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/me')
  await page.goto(`/decide/dr-1#pass=${PASS}`)
  expect((await me).status()).toBe(401)
  await expect(page.getByText(ENDED)).toBeVisible()
  expect(page.url()).not.toContain(PASS)
  expect(new URL(page.url()).pathname).toBe('/decide/dr-1')
  expect(await page.evaluate(() => [sessionStorage.getItem('conductor.identityPass'), localStorage.getItem('conductor.workbenchToken')])).toEqual([null, null])
  await checkSent(sent)
  await ctx.close()
})

test('the decide page alone: the list newest first, the address first, an empty list, and a decision gone from the list', async ({ browser }) => {
  const { ctx, page, sent } = await tab(browser)
  const { s } = await stub(page)
  s.list = { decisions: [{ id: 'dr-8', rev: 1 }, { id: 'dr-7', rev: 1 }] }
  s.views.set('dr-7', { status: 200, body: approval('dr-7') })
  s.views.set('dr-8', { status: 200, body: approval('dr-8', 1, { type: 'clarification', question: 'Which index?', options: [] }) })
  await page.goto(`/decide#pass=${PASS}`)
  await expect(page.locator('[data-decide-signed-in]')).toHaveText('Signed in as Ana')
  await expect(page.locator('[data-decide-card]')).toHaveCount(2)
  await expect(page.locator('[data-decide-card]').first()).toHaveAttribute('data-decision-id', 'dr-8')
  await expect(card(page, 'dr-7').getByText('May dr-7 go out?')).toBeVisible()
  await expect(page.locator('[data-sidebar-brand]')).toHaveCount(0)
  await expect(page.getByRole('dialog', { name: 'Workbench token' })).toHaveCount(0)
  expect(page.url()).not.toContain(PASS)

  // dr-8 taken back: the list no longer holds it, its read is refused; its draft stays in its field, dr-7 stays answerable.
  await card(page, 'dr-8').locator('textarea[data-decide-text]').fill('btree, keyed by run')
  s.views.set('dr-8', { status: 404, body: { error: { code: 'not_found', message: 'no such decision' } } })
  s.list = { decisions: [{ id: 'dr-7', rev: 1 }] }
  await focus(page)
  await expect(card(page, 'dr-8').getByText('dr-8 is no longer open to you.')).toBeVisible()
  await expect(card(page, 'dr-8').locator('textarea[data-decide-text]')).toHaveValue('btree, keyed by run')
  await card(page, 'dr-7').getByRole('radio', { name: 'Approve' }).check()
  await expect(card(page, 'dr-7').locator('[data-decide-send]')).toBeEnabled()
  expect(s.statuses).not.toContain(401)
  await checkSent(sent)

  // The address's decision first.
  s.list = { decisions: [{ id: 'dr-8', rev: 1 }, { id: 'dr-7', rev: 1 }] }
  s.views.set('dr-8', { status: 200, body: approval('dr-8') })
  await page.goto('/decide/dr-7')
  await expect(page.locator('[data-decide-card]')).toHaveCount(2)
  await expect(page.locator('[data-decide-card]').first()).toHaveAttribute('data-decision-id', 'dr-7')

  // An empty list.
  s.list = { decisions: [] }
  await page.goto('/decide')
  await expect(page.getByText('Nothing awaits your answer here.')).toBeVisible()
  await checkSent(sent)
  await ctx.close()
})

test("an answer: an assignment's Not done, a send with no reply sent again unchanged, a refused read, a 401, and not_used", async ({ browser }) => {
  const { ctx, page, sent } = await tab(browser)
  const { s, reply } = await stub(page)
  s.list = { decisions: [{ id: 'dr-7', rev: 1 }] }
  s.views.set('dr-7', { status: 200, body: approval('dr-7', 1, { type: 'assignment', question: 'Run the migration on staging', options: [{ label: 'Done' }, { label: 'Not done' }] }) })
  const bodies: string[] = []
  s.respond = async (_id, body, route) => {
    bodies.push(body)
    if (bodies.length === 1) return route.abort()
    const responseId = (JSON.parse(body) as { responseId: string }).responseId
    const receipt = { k: 1, responseId, status: 'accepted', rev: 1, at: '2026-11-12T00:00:01Z', choice: 1 }
    return reply(route, 200, { receipt: { ...receipt, repeat: true }, decision: { id: 'dr-7', rev: 2, own: [receipt], outcome: 'waiting' } })
  }
  await page.goto(`/decide/dr-7#pass=${PASS}`)
  await expect(page.locator('[data-decide-signed-in]')).toHaveText('Signed in as Ana')
  const c = card(page, 'dr-7')
  await c.getByRole('radio', { name: 'Not done' }).check()
  await c.locator('[data-decide-send]').click()
  await expect(c.getByText('Not known whether your answer to dr-7 was recorded')).toBeVisible()
  await c.getByRole('button', { name: 'Send again' }).click()
  await expect(c.getByText('Your answer to dr-7 was recorded.')).toBeVisible()
  expect(bodies).toHaveLength(2)
  expect(bodies[1]).toBe(bodies[0])
  expect(JSON.parse(bodies[0]!)).toMatchObject({ choice: 1, expectedRev: 1 })

  // A read the server refuses: the card says so.
  s.views.set('dr-9', { status: 200, body: approval('dr-9') })
  await page.goto('/decide/dr-9')
  await expect(card(page, 'dr-9').getByText('May dr-9 go out?')).toBeVisible()
  s.views.set('dr-9', { status: 404, body: { error: { code: 'not_found', message: 'no such decision' } } })
  await focus(page)
  await expect(card(page, 'dr-9').getByText('dr-9 is no longer open to you.')).toBeVisible()

  // The outcome of an answer not used.
  s.views.set('dr-9', { status: 200, body: { id: 'dr-9', rev: 4, own: [{ k: 2, responseId: 'r1', status: 'accepted', rev: 1, at: '2026-11-12T00:00:01Z' }], outcome: 'not_used' } })
  await page.goto('/decide/dr-9')
  await expect(card(page, 'dr-9').getByText('Your answer to dr-9 was not used.')).toBeVisible()

  // A 401 to a send: the page ends, the draft stays in its field, the pass is emptied.
  s.views.set('dr-9', { status: 200, body: approval('dr-9', 5, { type: 'clarification', options: [] }) })
  s.respond = async (_id, _body, route) => reply(route, 401, { error: { code: 'unauthorized', message: 'workbench token required' } })
  await page.goto('/decide/dr-9')
  await card(page, 'dr-9').locator('textarea[data-decide-text]').fill('later, please')
  await card(page, 'dr-9').locator('[data-decide-send]').click()
  await expect(page.getByText(ENDED)).toBeVisible()
  await expect(card(page, 'dr-9').locator('textarea[data-decide-text]')).toHaveValue('later, please')
  await expect(card(page, 'dr-9').locator('[data-decide-send]')).toBeDisabled()
  expect(await page.evaluate(() => sessionStorage.getItem('conductor.identityPass'))).toBeNull()
  await checkSent(sent)
  await ctx.close()
})

test("a workbench token held: the pass is sent nowhere, the token is kept, and the operator's page opens", async ({ browser, state }) => {
  const { ctx, page, sent } = await tab(browser, state.token)
  await page.goto(`/decide/dr-7#pass=${PASS}`)
  await expect(page).toHaveURL(/\/decisions\?item=decision(:|%3A)dr-7$/)
  await expect(page.locator('[data-sidebar-brand]').first()).toBeVisible()
  expect(await page.evaluate(() => localStorage.getItem('conductor.workbenchToken'))).toBe(state.token)
  for (const r of sent) {
    expect(r.url()).not.toContain(PASS)
    expect(await r.headerValue('authorization')).not.toBe(`Bearer ${PASS}`)
  }
  await ctx.close()
})
