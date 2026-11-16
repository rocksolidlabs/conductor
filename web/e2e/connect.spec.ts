import type { Browser, Page, Route } from '@playwright/test'
import { budget, dayPage, expect, test, tokenAsk, type Session } from './fixtures'
import { noSideways, tapTarget, uncut } from './layout'

// A page to connect (the UX review's P18): a browser that holds no workbench token, or whose token the server refused, sees a page of
// its own in place of the app: the mark, one sentence, one field and Connect, and where the token is. No Cancel: it leaves when a token
// is given. The app under it stays mounted, hidden, so a refusal in the middle of work keeps what was on the page. The dialog stays
// for the menu's Workbench token…, over an app that works, with Cancel.

/** An expect's bound and an action's (the config's expect timeout). */
const STEP_MS = 15_000

const DESK = { width: 1440, height: 900 }
const PHONE = { width: 390, height: 844, touch: true }

/** The next `n` requests to the API sent with `token` are refused (401); the rest go through. */
async function refuseNext(page: Page, token: string): Promise<{ refuse: (n?: number) => void }> {
  let left = 0
  await page.route(
    (u) => u.pathname.startsWith('/api/'),
    (r: Route) => {
      if (left === 0 || r.request().headers()['authorization'] !== `Bearer ${token}`) return r.fallback()
      left--
      return r.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: { code: 'unauthorized', message: 'unauthorized' } }) })
    },
  )
  return { refuse: (n = 1) => void (left = n) }
}

/** The tab shown again: the app reads what it shows once more, so a refused token is met at once. */
const shown = (page: Page) => page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')))

/** A refusal met on `page`: the next request with the token refused, a read made, and the page that asks drawn. */
async function refusedOn(page: Page, refuser: { refuse: (n?: number) => void }) {
  refuser.refuse()
  const refused = page.waitForResponse((r) => new URL(r.url()).pathname.startsWith('/api/') && r.status() === 401, { timeout: STEP_MS })
  await shown(page)
  await refused
  await expect(tokenAsk(page)).toBeVisible()
}

test('with no token, the page to connect stands in for the app at the desktop and the phone, in both themes; Connect shows the app and its reads go out', async ({ browser, state }) => {
  budget(2 * 2 * 12 * STEP_MS)
  for (const win of [DESK, PHONE]) {
    for (const theme of ['dark', 'light'] as const) {
      const at = `${win.width}, ${theme}`
      const page = await dayPage(browser, { ...win, theme, token: false, path: '/', ready: '[data-connect-page]', server: state })
      const ask = tokenAsk(page)
      await expect(ask, at).toBeVisible()
      // The app is under it, hidden: neither the sidebar nor the phone's bar shows, and no dialog is open.
      if (win === DESK) await expect(page.locator('#dashboard-sidebar-main'), at).toBeHidden()
      else await expect(page.locator('[data-bottom-bar]'), at).toBeHidden()
      await expect(page.getByRole('dialog'), at).toHaveCount(0)
      await expect(page.getByRole('main', { name: 'Connect to Conductor' }), at).toBeVisible()
      // Its words say where the token is, as the server says it.
      await expect(ask, at).toContainText('CONDUCTOR_WORKBENCH_TOKEN')
      await expect(ask, at).toContainText('workbench-token')
      await expect(ask, at).not.toContainText('printed at server start')
      // The mark for the theme, the other hidden; no Cancel.
      await expect(ask.locator(`[data-connect-mark="${theme}"]`), at).toBeVisible()
      await expect(ask.locator(`[data-connect-mark="${theme === 'dark' ? 'light' : 'dark'}"]`), at).toBeHidden()
      await expect(ask.getByRole('button', { name: 'Cancel' }), at).toHaveCount(0)
      await uncut(ask.locator('h1, [data-connect-submit], [data-connect-where] h2'))
      if (win === PHONE) {
        await tapTarget(ask.locator('[data-connect-submit]'))
        await noSideways(page)
      }
      // The server's token given: the app shows, and the home reads the sessions with it (their stream, or the list).
      const field = ask.getByPlaceholder('paste the workbench token')
      await field.fill(state.token)
      await expect(field).toHaveValue(state.token)
      const read = page.waitForRequest((r) => ['/api/events', '/api/sessions'].includes(new URL(r.url()).pathname) && r.headers()['authorization'] === `Bearer ${state.token}`, { timeout: STEP_MS })
      await ask.getByRole('button', { name: 'Connect' }).click()
      await read
      await expect(ask, at).toHaveCount(0)
      await expect(page.locator(win === DESK ? '#dashboard-sidebar-main' : '[data-bottom-bar]'), at).toBeVisible()
      await page.context().close()
    }
  }
})

test('a refusal in the middle of work: the page asks, its field empty, and the kept token tried again brings back the crew page with its unsaved edit', async ({ browser, state, api }) => {
  budget(10 * STEP_MS)
  const crew = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', { name: 'e2e connect keeps the edit', goal: 'a goal', cwd: '', where: 'server', isolation: 'none', openAfterLaunch: false, yolo: false, members: [{ name: 'lead', agentId: 'claude', prompt: 'hello', start: { when: 'immediately' } }] })).crew.id
  try {
    const page = await dayPage(browser, { ...DESK, path: `/crews/${encodeURIComponent(crew)}`, server: state })
    const refuser = await refuseNext(page, state.token)
    await page.getByLabel('Goal').fill('a goal, changed and not saved')
    await expect(page.locator('[data-unsaved-bar]')).toBeVisible()
    await refusedOn(page, refuser)
    const ask = tokenAsk(page)
    await expect(ask.locator('[data-connect-refused]')).toBeVisible()
    // The kept token is never put in the field: it starts empty, and the kept one is tried again from its own button, unseen.
    const field = ask.getByPlaceholder('paste the workbench token')
    await expect(field).toHaveValue('')
    const tried = page.waitForRequest((r) => new URL(r.url()).pathname === '/api/whoami' && r.headers()['authorization'] === `Bearer ${state.token}`)
    await ask.locator('[data-connect-kept]').click()
    await tried
    await expect(ask).toHaveCount(0)
    // Hidden, never unmounted: the edit and its bar are as they were.
    await expect(page.getByLabel('Goal')).toHaveValue('a goal, changed and not saved')
    await expect(page.locator('[data-unsaved-bar]')).toBeVisible()
  } finally {
    await api.call('DELETE', `/api/crews/${encodeURIComponent(crew)}`)
  }
})

test("a refusal on a session's page leaves the session's viewers as they were: the app was hidden, not unmounted", async ({ browser, state, api }) => {
  budget(10 * STEP_MS)
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e connect keeps the viewer' })
  try {
    const page = await dayPage(browser, { ...DESK, path: `/sessions/${s.id}`, server: state })
    const viewers = async () => (await api.session(s.id)).viewers ?? 0
    await expect.poll(viewers, { message: 'the page is attached' }).toBeGreaterThan(0)
    const before = await viewers()
    const refuser = await refuseNext(page, state.token)
    await refusedOn(page, refuser)
    expect(await viewers(), 'under the page').toBe(before)
    await tokenAsk(page).locator('[data-connect-kept]').click()
    await expect(tokenAsk(page)).toHaveCount(0)
    expect(await viewers(), 'after Connect').toBe(before)
  } finally {
    await api.stopSession(s.id)
  }
})

/** The menu's Workbench token…: the dialog over the working app, its words as the server says them, and Cancel leaves the app as it was. */
async function dialogFromMenu(page: Page, open: () => Promise<void>, at: string) {
  await open()
  const dialog = page.getByRole('dialog', { name: 'Workbench token' })
  await expect(dialog, at).toBeVisible()
  await expect(dialog, at).toContainText('CONDUCTOR_WORKBENCH_TOKEN')
  await expect(dialog, at).toContainText("in the file workbench-token in its data directory: it prints that file's path as it starts, never the token.")
  await expect(dialog, at).not.toContainText('printed at server start')
  await expect(tokenAsk(page), at).toHaveCount(0)
  await dialog.getByRole('button', { name: 'Cancel' }).click()
  await expect(dialog, at).toHaveCount(0)
  await expect(tokenAsk(page), at).toHaveCount(0)
}

async function menuOn(browser: Browser, server: { baseURL: string; token: string }, phone: boolean) {
  const page = await dayPage(browser, phone ? { ...PHONE, path: '/sessions', server } : { ...DESK, path: '/settings', server })
  return page
}

test("the menu's Workbench token… opens the dialog over the working app, with Cancel, at the desktop and on the phone's Sessions page", async ({ browser, state }) => {
  budget(8 * STEP_MS)
  const desk = await menuOn(browser, state, false)
  await dialogFromMenu(
    desk,
    async () => {
      await desk.locator('#dashboard-sidebar-main [data-header-account]').click()
      await desk.getByRole('menuitem', { name: /Workbench token/ }).click()
    },
    'the desktop',
  )
  await expect(desk.locator('#dashboard-sidebar-main')).toBeVisible()
  const phone = await menuOn(browser, state, true)
  await dialogFromMenu(
    phone,
    async () => {
      await phone.locator('#dashboard-panel-sessions [data-header-account]').click()
      await phone.getByRole('menuitem', { name: /Workbench token/ }).click()
    },
    'the phone',
  )
  await expect(phone.locator('[data-bottom-bar]')).toBeVisible()
})

const field = (page: Page) => tokenAsk(page).getByPlaceholder('paste the workbench token')

test('with no token, an address that opens Alerts opens nothing over the page to connect: the field has the focus, and the panel opens once a token is given', async ({ browser, state }) => {
  budget(10 * STEP_MS)
  const page = await dayPage(browser, { ...DESK, token: false, path: '/crews?alerts', ready: '[data-connect-page]', server: state })
  // The field focused is the page's last step (what was open closed first); the address keeps what it asks for, for later.
  await expect(field(page)).toBeFocused()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(page).toHaveURL(/\/crews\?alerts$/)
  // Escape closes nothing and leaves the focus where it is; typing goes to the field.
  await page.keyboard.press('Escape')
  await expect(tokenAsk(page)).toBeVisible()
  await expect(field(page)).toBeFocused()
  await page.keyboard.type(state.token)
  await expect(field(page)).toHaveValue(state.token)
  await tokenAsk(page).getByRole('button', { name: 'Connect' }).click()
  await expect(tokenAsk(page)).toHaveCount(0)
  // A token held: the panel the address asked for opens, and the address loses it.
  await expect(page.getByRole('dialog', { name: 'Alerts' })).toBeVisible()
  await expect(page).toHaveURL(/\/crews$/)
})

test('a refusal while Alerts or the Launch dialog is open closes it: the page to connect is all there is, its field has the focus, and Escape is harmless', async ({ browser, state }) => {
  budget(2 * 12 * STEP_MS)
  const opens: Array<[string, (page: Page) => Promise<void>]> = [
    ['Alerts', async (page) => void (await page.goto('/crews?alerts'))],
    ['Launch agent', (page) => page.keyboard.press('n')],
  ]
  for (const [name, open] of opens) {
    const page = await dayPage(browser, { ...DESK, path: '/crews', server: state })
    const refuser = await refuseNext(page, state.token)
    await open(page)
    await expect(page.getByRole('dialog', { name }), name).toBeVisible()
    await refusedOn(page, refuser)
    await expect(field(page), name).toBeFocused()
    await expect(page.getByRole('dialog'), name).toHaveCount(0)
    await page.keyboard.press('Escape')
    await expect(field(page), name).toBeFocused()
    await tokenAsk(page).locator('[data-connect-kept]').click()
    await expect(tokenAsk(page), name).toHaveCount(0)
    await expect(page.locator('#dashboard-sidebar-main'), name).toBeVisible()
    await page.context().close()
  }
})

test('a browser that keeps no token is asked for one, never told that the server refused a token it keeps; the reads behind it wait', async ({ browser, state }) => {
  budget(6 * STEP_MS)
  // Brakeman's page reads at once: with no token held the read waits for one (the page counts what it holds back), never sent.
  const page = await dayPage(browser, { ...DESK, token: false, path: '/brakeman', ready: '[data-connect-page]', server: state })
  const sent: string[] = []
  page.on('request', (r) => void (new URL(r.url()).pathname.startsWith('/api/') && sent.push(new URL(r.url()).pathname)))
  await expect.poll(async () => Number(await page.locator('[data-connect-page]').getAttribute('data-held'))).toBeGreaterThan(0)
  await expect(tokenAsk(page)).toContainText('This server asks for its workbench token before it shows anything.')
  await expect(tokenAsk(page).locator('[data-connect-refused]')).toHaveCount(0)
  await expect(page.locator('[data-connect-page]')).not.toHaveAttribute('data-standing', 'refused')
  await expect(field(page)).toHaveValue('')
  expect(sent, 'no read went with no token').toEqual([])
})

/**
 * The span requests are counted over while the page to connect shows: the longest retry cap of the app's stores (the decisions' and a
 * run's linked reads, DECISION_RETRY_CAP_MS and RETRY_CAP_MS, 30 s) and one tick of the sessions' poll (3 s) after it.
 */
const QUIET_SPAN_MS = 30_000 + 3_000

test('while the page to connect shows, the app behind it asks nothing with the refused token; a good token taken, the stream opens once', async ({ browser, state }) => {
  budget(QUIET_SPAN_MS + 8 * STEP_MS)
  const refused = 'e2e-a-token-the-server-refuses'
  const page = await dayPage(browser, { ...DESK, path: '/yard', ready: '[data-connect-page]', server: { baseURL: state.baseURL, token: refused } })
  // What the page asks from here on, with which token: a header's bearer, or a terminal's address.
  const asked: Array<{ path: string; token: string }> = []
  const tokenOf = (url: string, auth?: string) => (auth?.startsWith('Bearer ') ? auth.slice(7) : (new URL(url).searchParams.get('token') ?? ''))
  page.on('request', (r) => {
    const u = new URL(r.url())
    if (u.pathname.startsWith('/api/') || u.pathname.startsWith('/ws/')) asked.push({ path: u.pathname, token: tokenOf(r.url(), r.headers()['authorization']) })
  })
  page.on('websocket', (ws) => asked.push({ path: new URL(ws.url()).pathname, token: tokenOf(ws.url()) }))
  // The server refused the kept token: the page says so, and from then on the hidden app waits.
  await expect(tokenAsk(page).locator('[data-connect-refused]')).toBeVisible()
  await expect(page.locator('[data-connect-page][data-standing="refused"]')).toBeVisible()
  const from = asked.length
  await page.waitForTimeout(QUIET_SPAN_MS)
  expect(asked.slice(from).filter((a) => a.token === refused), 'asked with the refused token while the page showed').toEqual([])
  expect(asked.slice(from), 'asked with no token while the page showed').toEqual([])

  // The server's token given: the app shows and its stream opens once with it, as at a first load; the refused one is never sent.
  const field = tokenAsk(page).getByPlaceholder('paste the workbench token')
  await field.fill(state.token)
  await expect(field).toHaveValue(state.token)
  const streamed = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/events' && r.request().headers()['authorization'] === `Bearer ${state.token}`, { timeout: STEP_MS })
  await tokenAsk(page).getByRole('button', { name: 'Connect' }).click()
  expect((await streamed).status()).toBe(200)
  await expect(page.locator('#dashboard-sidebar-main')).toBeVisible()
  await expect(page.locator('#dashboard-sidebar-main nav[data-needs-read="true"], #dashboard-sidebar-main [data-needs-read="true"]').first()).toBeAttached()
  expect(asked.filter((a) => a.path === '/api/events' && a.token === state.token), 'the stream opened once').toHaveLength(1)
  expect(asked.slice(from).filter((a) => a.token === refused), 'the refused token after Connect').toEqual([])
})

test('a wrong token typed on the page to connect costs its one try and nothing else; the page says so and stays', async ({ browser, state }) => {
  budget(8 * STEP_MS)
  const refused = 'e2e-a-kept-token-the-server-refuses'
  const page = await dayPage(browser, { ...DESK, path: '/yard', ready: '[data-connect-page]', server: { baseURL: state.baseURL, token: refused } })
  await expect(tokenAsk(page).locator('[data-connect-refused]')).toBeVisible()
  // The app's reads are held behind the page (it counts them) before the try, so a burst of them would show in what is counted.
  await expect.poll(async () => Number(await page.locator('[data-connect-page]').getAttribute('data-held'))).toBeGreaterThan(0)
  const sent: Array<{ path: string; auth: string }> = []
  page.on('request', (r) => void (new URL(r.url()).pathname.startsWith('/api/') && sent.push({ path: new URL(r.url()).pathname, auth: r.headers()['authorization'] ?? '' })))
  page.on('websocket', (ws) => sent.push({ path: new URL(ws.url()).pathname, auth: '' }))
  const wrong = 'e2e-a-typed-token-the-server-refuses'
  await field(page).fill(wrong)
  await expect(field(page)).toHaveValue(wrong)
  const answered = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/whoami')
  await tokenAsk(page).getByRole('button', { name: 'Connect' }).click()
  expect((await answered).status()).toBe(401)
  await expect(tokenAsk(page).locator('[data-connect-tried]')).toHaveText('The server refused that token.')
  await expect(tokenAsk(page)).toBeVisible()
  expect(sent, "the page's one try, nothing else").toEqual([{ path: '/api/whoami', auth: `Bearer ${wrong}` }])
})

test('with a kept token the server refused, the field starts empty; the kept token is tried again from its own button, never shown', async ({ browser, state }) => {
  budget(8 * STEP_MS)
  const page = await dayPage(browser, { ...DESK, path: '/yard', server: state })
  const refuser = await refuseNext(page, state.token)
  await refusedOn(page, refuser)
  await expect(field(page)).toHaveValue('')
  await expect(tokenAsk(page).locator('input[type="password"]')).toHaveValue('')
  const kept = tokenAsk(page).locator('[data-connect-kept]')
  await expect(kept).toHaveText('Try the kept token again')
  const tried = page.waitForRequest((r) => new URL(r.url()).pathname === '/api/whoami' && r.headers()['authorization'] === `Bearer ${state.token}`)
  await kept.click()
  await tried
  await expect(tokenAsk(page)).toHaveCount(0)
  await expect(page.locator('#dashboard-sidebar-main')).toBeVisible()
})
