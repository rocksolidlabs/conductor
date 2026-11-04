import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Page } from '@playwright/test'
import { expect, test, type Session } from './fixtures'

// An agent's links open in the browser its settings name (#436). The Agents page's editor saves "Links open in" and a profile, and
// Clone keeps them; in the desktop app (a stub bridge here, as in settings.spec.ts) a link the stub agent opens with conductor open,
// or with its own opener ($BROWSER, Conductor's for an agent with a browser set), reaches the bridge once, with its agent's browser;
// in a plain tab nothing opens on its own, and a toast offers the link. What the agent was answered is read from the stub's
// transcript, which it writes once conductor open has answered: by then the page has told the server what became of the link.
test.describe.configure({ mode: 'serial' })
test.skip(process.platform === 'win32', 'the stub agent is a bash script')

const nonce = Date.now().toString(36)
const AGENT = `links-${nonce}`
const COPY = `${AGENT}-copy`
const sessions: string[] = []

test.afterAll(async ({ api }) => {
  for (const id of sessions) await api.stopSession(id)
  await api.call('DELETE', `/api/catalog/${COPY}`)
  await api.call('DELETE', `/api/catalog/${AGENT}`)
})

type Opened = { url: string; browser: unknown }
type Stubbed = { e2eOpened?: Opened[]; e2eStreams?: number }

/**
 * Counts the page's event streams whose answer came (GET /api/events): by then the server holds the stream, and a desktop app's
 * counts as one that opens links, so an open made after it reaches the page.
 */
async function watchStreams(page: Page) {
  await page.addInitScript(() => {
    const w = window as unknown as Stubbed
    w.e2eStreams = 0
    const fetch0 = window.fetch.bind(window)
    window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
      const res = await fetch0(input, init)
      if (String(input instanceof Request ? input.url : input).includes('/api/events') && res.ok) w.e2eStreams!++
      return res
    }
  })
}

/** The desktop app, stubbed: its bridge finds Edge and Firefox on this computer and records every link it is asked to open. */
async function stubApp(page: Page, token: string, url: string) {
  await page.addInitScript(
    ({ token, url }) => {
      const w = window as unknown as { conductorDesktop: unknown } & Stubbed
      w.e2eOpened = []
      w.conductorDesktop = {
        version: 'e2e',
        platform: 'linux',
        token: async () => token,
        openExternal: async () => {},
        openUrl: async (u: string, browser: unknown) => {
          w.e2eOpened!.push({ url: u, browser })
          return { outcome: 'browser' }
        },
        browsers: async () => ['edge', 'firefox'],
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
    { token, url },
  )
}

const opened = (page: Page) => page.evaluate(() => (window as unknown as Stubbed).e2eOpened ?? [])

function transcript(home: string, s: Session): string {
  const file = join(home, '.stub-sessions', `${s.agentSession?.id ?? ''}.txt`)
  return existsSync(file) ? readFileSync(file, 'utf8') : ''
}

/** Types a line into the session's terminal, once its transport is open. */
async function typeLine(page: Page, line: string) {
  const terminal = page.locator('.terminal-host .xterm-helper-textarea').first()
  await terminal.focus()
  await page.keyboard.type(line)
  await page.keyboard.press('Enter')
}

async function openSession(page: Page, id: string) {
  await page.goto(`/sessions/${encodeURIComponent(id)}`)
  await expect(page.locator('[data-transport-state]').first()).toHaveAttribute('data-transport-state', 'open', { timeout: 30_000 })
  await expect.poll(() => page.evaluate(() => (window as unknown as Stubbed).e2eStreams ?? 0), { timeout: 30_000, message: 'the page streams the events' }).toBeGreaterThan(0)
}

test('the editor saves the browser and its profile, shows them again, and Clone keeps them', async ({ page, api, state }) => {
  await stubApp(page, state.token, state.baseURL)
  await page.goto('/agents')
  await page.locator('[data-agent="claude"] [data-agent-clone]').click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.locator('[data-agent-form="clone"]')).toBeVisible()
  await dialog.getByPlaceholder('Aider', { exact: true }).fill(`Links ${nonce}`)
  const id = dialog.getByPlaceholder('aider', { exact: true })
  await id.fill(AGENT)
  await expect(id).toHaveValue(AGENT)
  // The browsers the app found are marked.
  await dialog.locator('[data-agent-browser]').getByRole('combobox').click()
  await expect(page.getByRole('option', { name: 'Firefox · on this computer' })).toBeVisible()
  await expect(page.getByRole('option', { name: 'Chrome', exact: true })).toBeVisible()
  await page.getByRole('option', { name: 'Edge · on this computer' }).click()
  await expect(page.getByRole('listbox')).toHaveCount(0)
  const profile = dialog.getByPlaceholder('Profile 2', { exact: true })
  await profile.fill('Profile 2')
  await expect(profile).toHaveValue('Profile 2')
  await dialog.getByRole('button', { name: 'Save to catalog' }).click()
  await expect(page.locator(`[data-agent="${AGENT}"]`)).toBeVisible({ timeout: 15_000 })
  const browserOf = async (agentId: string) => {
    const { agents } = await api.ok<{ agents: Array<{ id: string; browser?: unknown }> }>('GET', '/api/catalog')
    return agents.find((a) => a.id === agentId)?.browser
  }
  expect(await browserOf(AGENT)).toEqual({ name: 'edge', profile: 'Profile 2' })

  // Edit shows what was saved.
  await page.locator(`[data-agent="${AGENT}"]`).getByRole('button', { name: `Edit Links ${nonce}` }).click()
  await expect(dialog.locator('[data-agent-form="edit"]')).toBeVisible()
  await expect(dialog.locator('[data-agent-browser]').getByRole('combobox')).toContainText('Edge')
  await expect(dialog.getByPlaceholder('Profile 2', { exact: true })).toHaveValue('Profile 2')
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)

  // A clone of it keeps them.
  await page.locator(`[data-agent="${AGENT}"] [data-agent-clone]`).click()
  await expect(dialog.locator('[data-agent-form="clone"]')).toBeVisible()
  await expect(dialog.getByPlaceholder('aider', { exact: true })).toHaveValue(COPY)
  await expect(dialog.locator('[data-agent-browser]').getByRole('combobox')).toContainText('Edge')
  await dialog.getByRole('button', { name: 'Save to catalog' }).click()
  await expect(page.locator(`[data-agent="${COPY}"]`)).toBeVisible({ timeout: 15_000 })
  expect(await browserOf(COPY)).toEqual({ name: 'edge', profile: 'Profile 2' })
})

test("in the desktop app, the agent's conductor open and its own opener reach the bridge once each, with its agent's browser", async ({ page, api, state }) => {
  await stubApp(page, state.token, state.baseURL)
  await watchStreams(page)
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: AGENT, name: `e2e links ${nonce}` })
  sessions.push(s.id)
  await openSession(page, s.id)
  const edge = { name: 'edge', profile: 'Profile 2' }

  await typeLine(page, `stub open https://example.test/open-${nonce}`)
  await expect.poll(async () => transcript(state.home, await api.session(s.id)), { timeout: 30_000, message: 'conductor open answered' }).toContain('open: opened in edge (Profile 2)\n')
  expect(await opened(page)).toEqual([{ url: `https://example.test/open-${nonce}`, browser: edge }])

  await typeLine(page, `stub browser https://example.test/own-${nonce}`)
  await expect.poll(async () => transcript(state.home, await api.session(s.id)), { timeout: 30_000, message: 'the opener answered' }).toContain('browser: opened in edge (Profile 2)\n')
  expect(await opened(page)).toEqual([
    { url: `https://example.test/open-${nonce}`, browser: edge },
    { url: `https://example.test/own-${nonce}`, browser: edge },
  ])
})

test('in a plain tab nothing opens on its own, and a toast offers the link', async ({ page, api, state }) => {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: AGENT, name: `e2e links plain ${nonce}` })
  sessions.push(s.id)
  let popups = 0
  page.on('popup', () => popups++)
  await watchStreams(page)
  await openSession(page, s.id)
  const link = `https://example.test/plain-${nonce}`
  await typeLine(page, `stub open ${link}`)
  await expect.poll(async () => transcript(state.home, await api.session(s.id)), { timeout: 30_000, message: 'conductor open answered' }).toContain(
    'open: offered in the workbench: no desktop app took it\n',
  )
  const toast = page.locator('li').filter({ hasText: `e2e links plain ${nonce} opened a link` }).last()
  await expect(toast).toBeVisible({ timeout: 15_000 })
  await expect(toast).toContainText(link)
  await expect(toast.getByRole('button', { name: 'Open' })).toBeVisible()
  expect(popups).toBe(0)
})
