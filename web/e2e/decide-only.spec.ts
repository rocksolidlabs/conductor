import type { Browser, BrowserContext, Page, Request, Route } from '@playwright/test'
import { expect, test } from './fixtures'

// The decide page and a pass taken from the fragment (docs/plans/decide-grant.md, "web/app", Tests per surface): on the shipped
// build, which registers no identity provider, so the real server refuses every pass (`401`); the page's own behaviour is proved
// against the four decide routes stubbed in the browser (`route`), every other request recorded. No clock is waited on: a read is
// driven by a `focus` event the spec dispatches once the page has drawn who is signed in.

const PASS = 'e2e.pass-of_ana.7'
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

/** Every request: the pass in no URL; none to an events stream; every one to the API one of the four, the pass in its header. */
async function checkSent(sent: Request[]) {
  for (const r of sent) {
    const u = new URL(r.url())
    expect(r.url(), 'the pass in a URL').not.toContain(PASS)
    expect(u.pathname).not.toMatch(/events/)
    if (!u.pathname.startsWith('/api/')) continue
    expect(FOUR.some((re) => re.test(u.pathname)), u.pathname).toBe(true)
    expect(await r.headerValue('authorization')).toBe(`Bearer ${PASS}`)
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
        if (path === '/api/me') return reply(route, 200, { subject: 'ana', name: 'Ana​ ', grants: ['decide'] })
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

/** Everything of the page that could hold an address: the address bar, the history's state, the Navigation API's entries, and the router's route and the one it was redirected from. */
const addressHeld = (page: Page) =>
  page.evaluate(() => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const route = (document.querySelector('#__nuxt') as any).__vue_app__.config.globalProperties.$router.currentRoute.value
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const entries = ((window as any).navigation?.entries() ?? []).map((e: { url: string }) => e.url)
    return [location.href, JSON.stringify(history.state), ...entries, route.fullPath, route.redirectedFrom?.fullPath ?? ''].join(' ')
  })

test('the pass in the fragment, in any spelling: once the page has drawn, no address, history state or route holds it', async ({ browser }) => {
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
