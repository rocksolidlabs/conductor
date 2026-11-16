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

test('a refusal in the middle of work: the page asks, its field from the kept token, and Connect brings back the crew page with its unsaved edit', async ({ browser, state, api }) => {
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
    const field = ask.getByPlaceholder('paste the workbench token')
    await expect(field).toHaveValue(state.token)
    await ask.getByRole('button', { name: 'Connect' }).click()
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
    await tokenAsk(page).getByRole('button', { name: 'Connect' }).click()
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
    await tokenAsk(page).getByRole('button', { name: 'Connect' }).click()
    await expect(tokenAsk(page), name).toHaveCount(0)
    await expect(page.locator('#dashboard-sidebar-main'), name).toBeVisible()
    await page.context().close()
  }
})

test('a browser that keeps no token is asked for one, never told that the server refused a token it keeps', async ({ browser, state }) => {
  budget(6 * STEP_MS)
  // Brakeman's page reads at once, with no token: the server answers 401, and the store marks the refusal.
  const page = await dayPage(browser, { ...DESK, token: false, path: '/brakeman', ready: '[data-connect-page]', server: state })
  await expect(page.locator('[data-connect-page][data-standing="refused"]')).toBeVisible()
  await expect(tokenAsk(page)).toContainText('This server asks for its workbench token before it shows anything.')
  await expect(tokenAsk(page).locator('[data-connect-refused]')).toHaveCount(0)
  await expect(field(page)).toHaveValue('')
})
