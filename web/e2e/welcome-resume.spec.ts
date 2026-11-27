import type { Browser, Page } from '@playwright/test'
import { budget, DAILY, dayPage, expect, test, type Session } from './fixtures'
import { inside, uncut } from './layout'
import { ownServer, type OwnServer } from './own-server'
import { START_WAIT_MS } from './server'

// The welcome offers the sessions that ended (UX3): under the Or line, "Pick up where you left off" lists the three that ended last,
// each with its folder and age, and Resume, which asks first and resumes as the palette does; a record whose end was not seen opens its
// own page, which asks there. With a row drawn, the title says nothing is running. Each test has a server of its own, made as
// history-page.spec.ts makes one: a stub session given a turn (its conversation resumable), stopped, its end in its record, the server
// started again on its data, so that the session is no longer listed and the home shows its welcome.

/** An expect's bound and an action's (the config's expect timeout). */
const STEP_MS = 15_000
/** A wait on the server's own work: a screen drawn, a turn reported, a record written. */
const LONG_MS = 30_000

/** A Claude-shaped stub session named `name`, given a turn on its page so that its conversation can be resumed. */
async function withTurn(browser: Browser, srv: OwnServer, name: string): Promise<{ id: string; page: Page }> {
  budget(2 * LONG_MS + 3 * STEP_MS)
  const s = await srv.api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name })
  const page = await dayPage(browser, { width: 1440, height: 900, path: `/sessions/${encodeURIComponent(s.id)}`, server: srv.started })
  const screen = page.locator('.terminal-host .xterm-screen').first()
  await expect(screen).toBeVisible({ timeout: LONG_MS })
  await screen.click()
  await page.keyboard.type('remember PAPAYA')
  await page.keyboard.press('Enter')
  await expect.poll(async () => (await srv.api.session(s.id)).agentSession?.resumable, { timeout: LONG_MS, message: `${name} resumable` }).toBe(true)
  return { id: s.id, page }
}

/** Stops the session and waits for its end in its record. */
async function ended(srv: OwnServer, id: string) {
  budget(2 * STEP_MS)
  await srv.api.stopSession(id)
  await expect.poll(async () => (await srv.api.session(id)).status, { timeout: STEP_MS }).toMatch(/exited|stopped/)
  await expect.poll(async () => (await srv.api.ok<{ record: { endedAt?: string } }>('GET', `/api/history/sessions/${id}`)).record.endedAt ?? '', { timeout: STEP_MS }).not.toBe('')
}

/** The server started again on its data: the sessions it had are no longer listed. */
async function again(srv: OwnServer, ids: string[]) {
  budget(START_WAIT_MS + STEP_MS)
  await srv.restart()
  for (const id of ids) expect((await srv.api.call('GET', `/api/sessions/${id}`)).status, `${id} no longer listed`).toBe(404)
}

/** The home's welcome with its rows drawn. */
async function home(browser: Browser, srv: OwnServer, size = { width: 1440, height: 900 }) {
  const page = await dayPage(browser, { ...size, path: '/', server: srv.started })
  const card = page.locator('[data-welcome-card]')
  await expect(card.locator('[data-welcome-primary]')).toBeVisible()
  await expect(card.locator('[data-welcome-resume]')).toBeVisible({ timeout: LONG_MS })
  return { page, card }
}

let srv: OwnServer | undefined
test.afterEach(async () => {
  await srv?.stop()
  srv = undefined
})

test('a session that ended is offered under the Or line, with its folder and age; Resume asks, Keep it starts nothing, Resume starts it and opens it', async ({ browser }) => {
  budget(START_WAIT_MS)
  srv = await ownServer('welcome-resume')
  const name = 'welcome resume one'
  const { id, page: first } = await withTurn(browser, srv, name)
  await first.context().close()
  await ended(srv, id)
  await again(srv, [id])

  budget(12 * STEP_MS)
  const { page, card } = await home(browser, srv)
  await expect(card.locator('[data-welcome-title]')).toHaveText('Nothing is running')
  await expect(card.locator('[data-welcome-sentence]')).toHaveCount(0)
  const row = card.locator(`[data-resume-row="${id}"]`)
  await expect(row.locator('[data-resume-name]')).toHaveText(name)
  await expect(row.locator('[data-resume-folder]')).toHaveAttribute('title', srv.repo)
  await expect(row.locator('[data-resume-age]')).toHaveText(/^· ended (just now|\d+m ago)$/)
  await expect(card.locator('[data-resume-all]')).toHaveCount(0)

  // Resume asks first; Keep it closes the question and starts nothing.
  const button = row.locator('[data-resume-button]')
  await expect(button).toHaveAccessibleName(`Resume ${name}`)
  await button.click()
  const ask = page.getByRole('dialog', { name: `Resume ${name}?` })
  await expect(ask).toBeVisible()
  await ask.locator('[data-resume-keep]').click()
  await expect(ask).toHaveCount(0)
  expect((await srv.api.ok<{ sessions: Session[] }>('GET', '/api/sessions')).sessions, 'nothing started').toEqual([])

  // Resume: the new session's page, and its record names the one it resumed.
  await button.click()
  await expect(ask).toBeVisible()
  await ask.locator('[data-resume-yes]').click()
  await page.waitForURL((u) => /^\/sessions\/[^/]+$/.test(u.pathname) && !u.pathname.endsWith(id), { timeout: LONG_MS })
  const made = decodeURIComponent(new URL(page.url()).pathname.split('/').pop()!)
  await expect(page.getByText(`${name} resumed`, { exact: true })).toBeVisible()
  await expect
    .poll(async () => (await srv!.api.ok<{ record: { resumedFrom?: string } }>('GET', `/api/history/sessions/${made}`)).record.resumedFrom ?? '', { timeout: STEP_MS })
    .toBe(id)
  await srv.api.stopSession(made)
  await page.context().close()
})

test("a session whose end was not seen says Resume… and opens its record's page, which asks there", async ({ browser }) => {
  budget(START_WAIT_MS)
  srv = await ownServer('welcome-resume-lost')
  const name = 'welcome resume lost'
  const { id, page: first } = await withTurn(browser, srv, name)
  await first.context().close()
  // The server ended at once, the session still running: its record has no end, and the next start finds it lost.
  await again(srv, [id])
  budget(6 * STEP_MS)
  const { page, card } = await home(browser, srv)
  const button = card.locator(`[data-resume-row="${id}"] [data-resume-button]`)
  await expect(button).toHaveText('Resume…')
  await button.click()
  await page.waitForURL((u) => u.pathname === `/history/sessions/${id}`, { timeout: STEP_MS })
  await expect(page.locator(`[data-record-page][data-record-id="${id}"]`)).toBeVisible()
  await page.context().close()
})

test('four ended sessions give three rows and All 4 ended sessions, which opens History; at the daily window the card is whole, the folders in the code font', async ({ browser }) => {
  budget(START_WAIT_MS)
  srv = await ownServer('welcome-resume-four')
  const ids: string[] = []
  for (const n of [1, 2, 3, 4]) {
    const { id, page } = await withTurn(browser, srv, `welcome resume ${n}`)
    await page.context().close()
    await ended(srv, id)
    ids.push(id)
  }
  await again(srv, ids)

  budget(10 * STEP_MS)
  const { page, card } = await home(browser, srv, DAILY)
  const rows = card.locator('[data-resume-row]')
  await expect(rows).toHaveCount(3)
  // Newest end first: the last three ended.
  expect(await rows.evaluateAll((els) => els.map((e) => e.getAttribute('data-resume-row')))).toEqual([ids[3], ids[2], ids[1]])
  const view = page.viewportSize()!
  const box = (await card.boundingBox())!
  expect(box.y + box.height, 'the card inside the window').toBeLessThanOrEqual(view.height)
  for (let i = 0; i < 3; i++) {
    await uncut(rows.nth(i).locator('[data-resume-button]'))
    await inside(rows.nth(i), card)
  }
  const family = await rows.first().locator('[data-resume-folder]').evaluate((el) => getComputedStyle(el).fontFamily)
  expect(family).toMatch(/JetBrains Mono/)
  const all = card.locator('[data-resume-all]')
  await expect(all).toHaveText('All 4 ended sessions')
  await all.click()
  await page.waitForURL((u) => u.pathname === '/history' && u.searchParams.get('q') === 'kind:session', { timeout: STEP_MS })
  await page.context().close()
})
