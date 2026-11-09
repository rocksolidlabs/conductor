import { copyFileSync, mkdirSync, mkdtempSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { Api, expect, test, type Session } from './fixtures'
import { HEALTH_WAIT_MS, renderConfig, scratchRepo, startServer, stopServer, type Started } from './server'

// The History page and a record reached from the palette (Program item HS1, docs/plans/history.md, PR-7). A session ended before
// the server restarted is no longer listed: `~` alone lists it among the latest sessions, its row opening its record page, and →
// offers Resume, its record's entry saying it is. The History page's chips write the query's filters, the URL keeps the query, and a
// phone's width shows one column. A token replaced by another after a refusal (the gate's recovery from a wrong one) shows the search.
test.describe.configure({ mode: 'serial' })

/**
 * Each test's timeout is the sum of its waits' caps, each added as its step is about to take it (AGENTS.md: a test's timeout bounds a
 * hang, not its steps), the config's 120 s above them for the work between.
 */
const STEP_MS = 15_000 // the config's expect bound, and an action's
const STOP_MS = 5_000 // stopServer's SIGTERM wait
function budget(ms: number) {
  const info = test.info()
  info.setTimeout(info.timeout + ms)
}

// The first test's own server, ended after the test that started it (a test run again starts its own, on data of its own).
let server: Started | null = null
let serverLog = ''
test.afterEach(async () => {
  if (server) await stopServer(server.pid)
  server = null
  if (serverLog) {
    mkdirSync('test-results', { recursive: true })
    copyFileSync(serverLog, 'test-results/history-page-server.log')
  }
  serverLog = ''
})

const search = (api: Api, q: string) => api.ok<{ entries: Array<{ key: string; availability: { state: string } }> }>('GET', `/api/history?q=${encodeURIComponent(q)}&limit=100`).then((r) => r.entries)

test("the record of a session ended before a restart is reached from the palette's row", async ({ state, page }) => {
  const here = dirname(fileURLToPath(import.meta.url))
  const root = mkdtempSync(join(state.root, 'history-page-'))
  const home = join(root, 'home')
  const data = join(root, 'data')
  const repo = join(root, 'repo')
  for (const d of [home, data, repo]) mkdirSync(d, { recursive: true })
  scratchRepo(repo)
  const config = join(root, 'conductor.json')
  renderConfig(join(here, 'conductor.e2e.json'), config)
  serverLog = join(root, 'server.log')
  const start = () => startServer({ config, home, data, allowedRoot: root, defaultCwd: repo, log: serverLog })
  budget(HEALTH_WAIT_MS)
  server = await start()
  let api = new Api(server)

  // A Claude-shaped stub session given a turn (so its conversation can be resumed), then ended, its end in its record.
  const created = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'history page record' })
  await page.addInitScript((t) => localStorage.setItem('conductor.workbenchToken', t), server.token)
  budget(2 * 30_000 + 2 * 15_000 + 3 * STEP_MS) // the screen, the resumable conversation, the stop, the end kept; the goto, click, type
  await page.goto(`${server.baseURL}/sessions/${encodeURIComponent(created.id)}`)
  const screen = page.locator('.terminal-host .xterm-screen').first()
  await expect(screen).toBeVisible({ timeout: 30_000 })
  await screen.click()
  await page.keyboard.type('remember PAPAYA')
  await page.keyboard.press('Enter')
  await expect.poll(async () => (await api.session(created.id)).agentSession?.resumable, { timeout: 30_000 }).toBe(true)
  await api.stopSession(created.id)
  await expect.poll(async () => (await api.session(created.id)).status, { timeout: 15_000 }).toMatch(/exited|stopped/)
  await expect.poll(async () => (await api.ok<{ record: { endedAt?: string } }>('GET', `/api/history/sessions/${created.id}`)).record.endedAt ?? '', { timeout: 15_000 }).not.toBe('')

  // Restarted on its data: the session is no longer listed, and the index, caught up again, holds its record as a record only.
  budget(STOP_MS + HEALTH_WAIT_MS + 60_000) // the stop, the start, the index caught up
  await stopServer(server.pid)
  server = null
  server = await start()
  api = new Api(server)
  await page.addInitScript((t) => localStorage.setItem('conductor.workbenchToken', t), server.token)
  expect((await api.call('GET', `/api/sessions/${created.id}`)).status).toBe(404)
  const key = `session:${created.id}`
  await expect.poll(async () => (await search(api, 'kind:session')).find((e) => e.key === key)?.availability.state ?? '', { timeout: 60_000, message: 'the record is indexed after the restart' }).toBe('record')

  // `~` alone, from a page whose layout is drawn: the record among the latest sessions.
  budget(2 * 30_000 + 12 * STEP_MS) // the sidebar, the row; the goto, focus, fill, label, detail, focus, →, Resume twice, ←, Enter, the URL, the ending
  await page.goto(`${server.baseURL}/agents`)
  await expect(page.locator('[data-session-list]')).toBeVisible({ timeout: 30_000 })
  await page.keyboard.press('Control+k')
  const p = page.locator('[data-palette]')
  const input = p.locator('input[data-palette-input]')
  await expect(input).toBeFocused()
  await input.fill('~')
  const row = p.locator(`[data-palette-group="history-latest"] [data-palette-row="history:${key}"]`)
  await expect(row).toBeVisible({ timeout: 30_000 })
  await expect(row.locator('[data-palette-label]')).toHaveText('history page record')
  await expect(row.locator('[data-palette-detail]')).toContainText('record only')
  // → reads the record's entry: Resume is offered.
  await row.focus()
  await expect(row).toHaveAttribute('data-palette-focused', '')
  await page.keyboard.press('ArrowRight')
  await expect(row.locator('[data-palette-action="resume"]')).toBeEnabled({ timeout: 15_000 })
  await expect(row.locator('[data-palette-action="resume"]')).toHaveText(/Resume/)
  // Enter opens the record page.
  await page.keyboard.press('ArrowLeft')
  await page.keyboard.press('Enter')
  await page.waitForURL((u) => u.pathname === `/history/sessions/${created.id}`, { timeout: 15_000 })
  await expect(page.locator(`[data-record-page][data-record-id="${created.id}"] [data-record-ending]`)).toHaveText(/exited|stopped/)
})

test("the History page's chips write the query, the URL keeps it, and a phone shows one column", async ({ page, api }) => {
  const tag = `hp${Date.now().toString(36)}`
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: `e2e history page ${tag}` })
  try {
    const key = `session:${s.id}`
    budget(60_000)
    await expect.poll(async () => (await search(api, tag)).some((e) => e.key === key), { timeout: 60_000, message: 'the session is indexed' }).toBe(true)

    budget(4 * 30_000 + 22 * STEP_MS) // the four rows and none of 30 s; the goto twice, every chip, value, URL and box
    await page.goto(`/history?q=${tag}`)
    const pageEl = page.locator('[data-history-page]')
    const input = pageEl.locator('[data-history-input] input, input[data-history-input]').first()
    await expect(input).toHaveValue(tag)
    const row = pageEl.locator(`[data-history-row="${key}"]`)
    await expect(row).toBeVisible({ timeout: 30_000 })
    await expect(row.locator('[data-history-row-title]')).toHaveText(`e2e history page ${tag}`)

    // A chip writes its filter into the query and the URL; another kind replaces it; pressed again, it is taken out.
    const chip = (v: string) => pageEl.locator(`[data-history-chip="${v}"]`)
    await chip('class:record').click()
    await expect(input).toHaveValue(`${tag} class:record`)
    await expect.poll(() => new URL(page.url()).searchParams.get('q')).toBe(`${tag} class:record`)
    await expect(chip('class:record')).toHaveAttribute('data-history-chip-on', '')
    await expect(row).toBeVisible()
    await chip('class:said').click()
    await expect(input).toHaveValue(`${tag} class:said`)
    await expect(chip('class:record')).not.toHaveAttribute('data-history-chip-on', '')
    await expect(pageEl.locator('[data-history-none]')).toBeVisible({ timeout: 30_000 })
    await expect(row).toHaveCount(0)
    await chip('class:said').click()
    await expect(input).toHaveValue(tag)
    await chip('since:24h').click()
    await expect(input).toHaveValue(`${tag} since:24h`)
    await expect(row).toBeVisible({ timeout: 30_000 })
    // Typed in the box: the URL follows, and a link to it opens the same search.
    await input.fill(`${tag} kind:session`)
    await expect.poll(() => new URL(page.url()).searchParams.get('q')).toBe(`${tag} kind:session`)
    await expect(row).toBeVisible({ timeout: 30_000 })

    // A phone: one column, the chips under the search box, nothing wider than the screen.
    await page.setViewportSize({ width: 390, height: 844 })
    await page.goto(`/history?q=${encodeURIComponent(`${tag} kind:session`)}`)
    await expect(row).toBeVisible({ timeout: 30_000 })
    const box = async (sel: string) => (await pageEl.locator(sel).boundingBox())!
    const [searchBox, chips, results] = [await box('[data-history-search]'), await box('[data-history-chips]'), await box('[data-history-results]')]
    expect(Math.abs(chips.x - searchBox.x)).toBeLessThan(2)
    expect(Math.abs(results.x - searchBox.x)).toBeLessThan(2)
    expect(chips.y).toBeGreaterThan(searchBox.y)
    expect(results.y).toBeGreaterThan(chips.y)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  } finally {
    await api.stopSession(s.id)
  }
})

test('a token replaced after its refusal shows the search: the History page is not left on Loading', async ({ page, api }) => {
  // The gate's recovery from a wrong token: the page's search is refused with the token the browser holds, the right one is saved over
  // it (one token replaced by another, never empty between), and the search is asked again with it (#746's review: the page stayed on
  // Loading, its watch on `hasToken` never firing again).
  const tag = `ht${Date.now().toString(36)}`
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: `e2e history token ${tag}` })
  try {
    const key = `session:${s.id}`
    budget(60_000)
    await expect.poll(async () => (await search(api, tag)).some((e) => e.key === key), { timeout: 60_000, message: 'the session is indexed' }).toBe(true)

    budget(2 * 30_000 + 6 * STEP_MS) // the gate, the row; the goto, the fill, its value, Save, the gate gone, the loading gone
    await page.addInitScript(() => localStorage.setItem('conductor.workbenchToken', 'not-the-workbench-token'))
    await page.goto(`/history?q=${tag}`)
    const gate = page.getByRole('dialog', { name: 'Workbench token' })
    await expect(gate).toBeVisible({ timeout: 30_000 })
    const field = gate.getByPlaceholder('paste the workbench token')
    await field.fill(api.server.token)
    await expect(field).toHaveValue(api.server.token)
    await gate.getByRole('button', { name: 'Save' }).click()
    await expect(gate).toHaveCount(0)
    const pageEl = page.locator('[data-history-page]')
    await expect(pageEl.locator(`[data-history-row="${key}"]`)).toBeVisible({ timeout: 30_000 })
    await expect(pageEl.locator('[data-history-loading]')).toHaveCount(0)
  } finally {
    await api.stopSession(s.id)
  }
})
