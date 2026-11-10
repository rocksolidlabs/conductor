import type { Page } from '@playwright/test'
import { expect, test, type Session } from './fixtures'

// An invite (conductor://…) the desktop app is handed while its window is
// open comes to the page over the bridge, a stand-in for
// desktop/src/preload.ts here: the workbench routes to its join page in
// place, with no page load, and says it took it; what a join page would not
// take it refuses (false), which the app answers with a full load.
type Invited = { __invite?: (invite: unknown) => boolean | Promise<boolean>; __taken?: boolean; __loaded?: string }

/**
 * The app hands the page an invite. The call is started, never awaited by the evaluate: an invite taken routes the page, and an
 * evaluate that waits for the answer across that route can lose it (#281). The page keeps the answer, read by `taken`.
 */
async function invite(page: Page, handed: { server: string; token: string }) {
  await page.evaluate((handed) => {
    const w = window as unknown as Invited
    w.__taken = undefined
    void Promise.resolve(w.__invite!(handed)).then((answer) => (w.__taken = answer))
  }, handed)
}
const taken = (page: Page) => page.evaluate(() => (window as unknown as Invited).__taken)

test('an invite handed over the bridge opens the join page in place', async ({ page, api, state }) => {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'invited' })
  const { token } = await api.ok<{ token: string }>('POST', `/api/sessions/${s.id}/links`, { role: 'view' })
  await page.addInitScript(
    ({ token, url }) => {
      const w = window as unknown as Invited & { conductorDesktop: unknown }
      w.conductorDesktop = {
        version: 'e2e',
        platform: 'linux',
        token: async () => token,
        openExternal: async () => {},
        settings: { get: async () => ({}), set: async () => ({}) },
        restartServer: async () => {},
        openInBrowser: async () => {},
        showLog: async () => {},
        serverState: async () => ({ state: 'running', url, failures: 0 }),
        notice: async () => '',
        ice: async () => ({ forwarding: false, port: 0, publicIp: '', wslAddress: '', firewall: 'unknown' }),
        allowIceFirewall: async () => 'unknown',
        versions: async () => ({ app: 'e2e', electron: '', node: '', chrome: '', server: '' }),
        onServerState: () => () => {},
        onInvite: (cb: (invite: unknown) => boolean | Promise<boolean>) => {
          w.__invite = cb
          return () => {}
        },
      }
    },
    { token: state.token, url: state.baseURL },
  )
  await page.goto('/')
  await expect(page.locator('[data-session-list]').first()).toBeVisible()
  await expect.poll(() => page.evaluate(() => typeof (window as unknown as Invited).__invite)).toBe('function')
  await page.evaluate(() => ((window as unknown as Invited).__loaded = 'once'))
  const entries = await page.evaluate(() => history.length)
  // What a join page would not take goes nowhere and is refused: plain http off loopback, a token that is a path.
  await invite(page, { server: 'http://switchyard.example.net', token })
  await expect.poll(() => taken(page)).toBe(false)
  await invite(page, { server: 'https://switchyard.example.net', token: '../settings?aaaaaaaaaaaaaaaa' })
  await expect.poll(() => taken(page)).toBe(false)
  expect(page.url()).not.toContain('/join/')
  // The invite itself (its server written with a trailing slash, as an app might): taken, and the join page, told to signal to the
  // server it names, in the same page.
  await invite(page, { server: `${state.baseURL}/`, token })
  await expect(page).toHaveURL(`${state.baseURL}/join/${token}?server=${encodeURIComponent(state.baseURL)}`)
  await expect.poll(() => taken(page)).toBe(true)
  await expect(page.getByRole('heading', { name: 'Join invited' })).toBeVisible({ timeout: 15_000 })
  expect(await page.evaluate(() => (window as unknown as Invited).__loaded)).toBe('once')
  expect(await page.evaluate(() => history.length)).toBe(entries + 1)
  // The same token for another server (the same one, named another way) is a new join page: it asks that server for the session
  // anew rather than keeping the first page's.
  const other = state.baseURL.replace('127.0.0.1', 'localhost')
  const asked = page.waitForRequest((r) => r.url().startsWith(`${other}/api/join/`))
  await invite(page, { server: other, token })
  await asked
  await expect(page).toHaveURL(`${state.baseURL}/join/${token}?server=${encodeURIComponent(other)}`)
  await expect.poll(() => taken(page)).toBe(true)
  await api.stopSession(s.id)
})
