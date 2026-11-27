import { mkdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { Api, expect, test, type Session } from './fixtures'
import { renderConfig, startServer, stopServer, type Started } from './server'

// A server with nothing on it yet (#359): at desktop width the home shows
// its welcome, which is not the sidebar's empty line, and the sessions list
// leaves for the home; below lg the list is the home screen, and a window
// grown past lg while on it leaves too. With a session, a fresh visit lands
// on it: the home waits for the sessions to be read before saying nothing.

test.describe.configure({ mode: 'serial' })

let server: Started | null = null

test.afterAll(async () => {
  if (server) await stopServer(server.pid)
})

test('the empty home shows its welcome at desktop width, the list below it, and the list leaves for the home when the window grows', async ({ state, browser }) => {
  test.setTimeout(120_000)
  const here = dirname(fileURLToPath(import.meta.url))
  const root = join(state.root, 'home-wide')
  const home = join(root, 'home')
  const data = join(root, 'data')
  for (const d of [home, data]) mkdirSync(d, { recursive: true })
  const config = join(root, 'conductor.json')
  renderConfig(join(here, 'conductor.e2e.json'), config)
  server = await startServer({ config, home, data, allowedRoot: root, defaultCwd: root, log: join(root, 'server.log') })

  const context = await browser.newContext({ baseURL: server.baseURL, viewport: { width: 1440, height: 900 } })
  const page = await context.newPage()
  await page.addInitScript((token) => localStorage.setItem('conductor.workbenchToken', token), server.token)

  await page.goto('/')
  await expect(page).toHaveURL(/\/$/, { timeout: 30_000 })
  const welcome = page.locator('[data-welcome]')
  await expect(welcome).toBeVisible()
  // Its ways to start (the UX review's P17; UX3): an agent found on this machine to launch, and folded under Other ways to start, the
  // example crews to load. Settings left the card.
  await expect(welcome.locator('[data-welcome-primary]')).toBeVisible()
  await welcome.locator('[data-welcome-expander]').click()
  await expect(welcome.getByRole('button', { name: 'Load the example crews' })).toBeVisible()
  await expect(welcome.getByRole('link', { name: 'Settings' })).toHaveCount(0)

  // A phone: the list is the home screen.
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/')
  await expect(page).toHaveURL(/\/sessions$/, { timeout: 30_000 })

  // Grown past lg while on the list: the sidebar is the list now, so the page leaves for the home and its welcome.
  await page.setViewportSize({ width: 1440, height: 900 })
  await expect(page).toHaveURL(/\/$/, { timeout: 30_000 })
  await expect(page.locator('[data-welcome]')).toBeVisible({ timeout: 30_000 })

  // With a session on the server, a fresh visit lands on it, not on the Yard: the home waits for the sessions to be read.
  const api = new Api(server)
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e home target' })
  await page.goto('/')
  await expect(page).toHaveURL(new RegExp(`/sessions/${s.id}$`), { timeout: 30_000 })
  await page.goto('/sessions')
  await expect(page).toHaveURL(new RegExp(`/sessions/${s.id}$`), { timeout: 30_000 })
  await api.stopSession(s.id)
  await context.close()
})
