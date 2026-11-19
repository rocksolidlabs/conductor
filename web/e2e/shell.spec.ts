import type { Browser, Page } from '@playwright/test'
import { walk } from './contrast'
import { expect, test, useTheme } from './fixtures'
import { noSideways, tapTarget } from './layout'
import { fakeShell, serverTraffic, shellDo, shellRecord, type FakeConfig } from './shell-fake'

// The phone shell's own screens (docs/plans/phone-shell.md, OSS-1b, Tests per surface, web/e2e/shell.spec.ts): the bridge injected
// as the shell's own script builds it, with a fake provider of the tests' own. With no provider, Open a server is the whole first
// screen; with one, Open a server is reachable in every state the provider reports and never asks it anything; the sign-in, Settings
// and a sign-out; the shell's pages kept to /shell inside a shell and away from it outside one; no request to the server's API made
// in a shell; and the screens' measures and contrast at a phone's size with touch and at a desktop's, in both themes.

const PHONE = { width: 390, height: 844 }
const DESK = { width: 1440, height: 900 }
/** The largest text size the system offers (its largest accessibility size for body text, 53 points), as the root's font size. */
const LARGEST_TEXT = 'html.conductor-shell { font-size: 53px !important; }'

const signedOut = { signedIn: false, notifications: 'unknown' }
const signedIn = { signedIn: true, who: 'ana@example.test', notifications: 'on', lastNotifiedAt: '2026-11-19T09:58:00Z' }
const provider = (state: Record<string, unknown>, extra: Partial<NonNullable<FakeConfig['provider']>> = {}): FakeConfig => ({ provider: { name: 'Desk relay', state, ...extra } })

/** A page in a context of its own at `size` (touch at a phone's size), the bridge injected with cfg (none for null), every request recorded. */
async function shellTab(browser: Browser, baseURL: string, cfg: FakeConfig | null, size = PHONE): Promise<{ page: Page; api: string[]; close: () => Promise<void> }> {
  const ctx = await browser.newContext({ baseURL, viewport: size, hasTouch: size.width < 1024, isMobile: size.width < 1024 })
  const page = await ctx.newPage()
  if (cfg) await fakeShell(page, cfg)
  const api = serverTraffic(page)
  // A shell's tab is closed only once it is shown to have sent the server nothing, whatever route it visited.
  return {
    page,
    api,
    close: async () => {
      if (cfg) expect(api, 'no request or socket to the server from a shell').toEqual([])
      await ctx.close()
    },
  }
}

/** Provider calls the fake recorded, past the subscription. */
const providerCalls = async (page: Page) => (await shellRecord(page)).calls.length

test.describe('the phone shell', () => {
  test('with no provider, Open a server is the whole first screen and opens an address through the bridge', async ({ browser, baseURL }) => {
    for (const size of [PHONE, DESK]) {
      const t = await shellTab(browser, baseURL!, { provider: null }, size)
      const { page } = t
      await page.goto('/shell')
      await expect(page.locator('[data-shell-open-server]')).toBeVisible()
      await expect(page.locator('[data-shell-title]')).toHaveText('Open a server')
      await expect(page.locator('[data-shell-signin], [data-shell-to-settings], [data-shell-back]')).toHaveCount(0)

      const field = page.getByLabel('Server address')
      await field.fill('https://desk.example.test/decide/dr-7')
      await page.locator('[data-shell-open]').click()
      await expect.poll(async () => (await shellRecord(page)).opened).toEqual(['https://desk.example.test/decide/dr-7'])
      await expect(page.locator('[data-shell-open-words]')).toHaveCount(0)

      await field.fill('http://192.0.2.10:8080')
      await page.locator('[data-shell-open]').click()
      await expect(page.locator('[data-shell-open-words="opened"]')).toHaveText('This address is not encrypted: use it only on a network you trust')

      await field.fill('javascript:alert(1)')
      await page.locator('[data-shell-open]').click()
      await expect(page.locator('[data-shell-open-words="refused"]')).toHaveText('Only an https:// or http:// address opens here.')

      await expect(page.locator('[data-shell-recent-address]')).toHaveText(['http://192.0.2.10:8080/', 'https://desk.example.test/decide/dr-7'])
      await page.locator('[data-shell-recent-address]').nth(1).click()
      await expect.poll(async () => (await shellRecord(page)).opened.at(-1)).toBe('https://desk.example.test/decide/dr-7')
      await page.locator('[data-shell-forget]').click()
      await expect(page.locator('[data-shell-recent]')).toHaveCount(0)
      expect(t.api, 'no request to the server in a shell').toEqual([])
      await t.close()
    }
  })

  test("the recorder of what a shell sends the server sees a socket the page opens", async ({ browser, baseURL }) => {
    // The proof that a shell sends nothing rests on this recorder: a WebSocket, which no request event reports, is seen too.
    const t = await shellTab(browser, baseURL!, { provider: null })
    await t.page.goto('/shell')
    await expect(t.page.locator('[data-shell-open-server]')).toBeVisible()
    await t.page.evaluate(() => void new WebSocket(`${location.origin.replace(/^http/, 'ws')}/ws/terminal`))
    await expect.poll(() => t.api.slice()).toContain('socket /ws/terminal')
    t.api.splice(0)
    await t.close()
  })

  test('a state the provider does not give is said in words with Try again, and the inbox says it is reading', async ({ browser, baseURL }) => {
    const t = await shellTab(browser, baseURL!, provider(signedIn))
    const { page } = t
    await page.addInitScript(() => {
      const r = (window as unknown as { __shell: Record<string, unknown> }).__shell
      r.stateFails = true
      r.hold = ['list']
    })
    await page.goto('/shell')
    await expect(page.locator('[data-shell-state-failed]')).toHaveText('Desk relay could not say whether this phone is signed in.')
    await expect(page.locator('[data-shell-checking]')).toHaveCount(0)
    await expect(page.locator('[data-shell-to-server]')).toBeVisible()
    await shellDo(page, 'stateFails', false)
    await page.locator('[data-shell-state-again]').click()
    await expect(page.locator('[data-shell-title]')).toHaveText('Waiting for you')
    await expect(page.locator('[data-shell-inbox-reading]')).toHaveText('Reading what waits…')
    await shellDo(page, 'release', 'list')
    await expect(page.locator('[data-shell-inbox-reading]')).toHaveCount(0)
    await expect(page.locator('[data-shell-inbox-empty]')).toBeVisible()
    await t.close()
  })

  test('keeps a page in a shell on /shell, and a page outside one off it', async ({ browser, baseURL }) => {
    const inShell = await shellTab(browser, baseURL!, { provider: null }, DESK)
    await inShell.page.goto('/')
    await expect(inShell.page).toHaveURL(/\/shell$/)
    await expect(inShell.page.locator('[data-shell-open-server]')).toBeVisible()
    await inShell.page.goto('/runs')
    await expect(inShell.page).toHaveURL(/\/shell$/)
    await expect(inShell.page.locator('#dashboard-sidebar-main')).toHaveCount(0)
    expect(inShell.api).toEqual([])
    await inShell.close()

    const outside = await shellTab(browser, baseURL!, null, DESK)
    await outside.page.goto('/shell/settings')
    await expect(outside.page).toHaveURL(/\/$/)
    await expect(outside.page.locator('[data-shell]')).toHaveCount(0)
    await outside.close()
  })

  test('with a provider, Open a server is reachable in every state it reports and never asks it anything', async ({ browser, baseURL }) => {
    const states: Array<{ state: Record<string, unknown> & { words?: string }; via: string }> = [
      { state: signedOut, via: 'sign-in' },
      { state: signedIn, via: 'settings' },
      { state: { ...signedIn, words: 'Desk relay cannot be reached: what you see is as of 09:58' }, via: 'settings' },
      { state: { ...signedOut, words: 'Desk relay cannot answer now' }, via: 'sign-in' },
    ]
    for (const s of states) {
      const t = await shellTab(browser, baseURL!, provider(s.state))
      const { page } = t
      await page.goto('/shell')
      if (s.via === 'settings') {
        await page.locator('[data-shell-to-settings]').click()
        await expect(page.locator('[data-shell-settings]')).toBeVisible()
      } else {
        await expect(page.locator('[data-shell-signin]')).toBeVisible()
      }
      if (s.state.words) await expect(page.getByText(s.state.words)).toBeVisible()
      await page.locator('[data-shell-to-server]').click()
      await expect(page.locator('[data-shell-open-server]')).toBeVisible()
      const before = await providerCalls(page)
      await page.getByLabel('Server address').fill('https://desk.example.test/')
      await page.locator('[data-shell-open]').click()
      await expect.poll(async () => (await shellRecord(page)).opened).toEqual(['https://desk.example.test/'])
      await page.locator('[data-shell-forget]').click()
      await expect(page.locator('[data-shell-recent]')).toHaveCount(0)
      expect(await providerCalls(page), `${JSON.stringify(s.state)}: the provider asked nothing`).toBe(before)
      expect(t.api).toEqual([])
      await t.close()
    }
  })

  test('the sign-in: the code, its page and its time left, Cancel, and words with Try again', async ({ browser, baseURL }) => {
    // The page's clock is the spec's: the code's time left is read against a time the spec set, never against the wall clock.
    const NOW = Date.parse('2026-11-19T10:00:00Z')
    const expiresAt = new Date(NOW + 9.5 * 60_000).toISOString()
    const t = await shellTab(browser, baseURL!, provider(signedOut, { signIn: { code: 'WDJB-MJHT', page: 'https://id.example.test/device', expiresAt } }))
    const { page } = t
    await page.addInitScript((now) => (Date.now = () => now), NOW)
    await page.goto('/shell')
    await expect(page.locator('[data-shell-title]')).toHaveText('Sign in to Desk relay')
    await page.locator('[data-shell-signin-begin]').click()
    await expect(page.locator('[data-shell-code]')).toHaveText('WDJB-MJHT')
    await expect(page.locator('[data-shell-code-left]')).toHaveText('Expires in 9 min')
    await page.locator('[data-shell-open-page]').click()
    await expect.poll(async () => (await shellRecord(page)).windows).toEqual(['https://id.example.test/device'])
    await page.locator('[data-shell-signin-cancel]').click()
    await expect(page.locator('[data-shell-code]')).toHaveCount(0)
    await expect.poll(async () => (await shellRecord(page)).calls).toContain('cancelSignIn')

    // Signed in elsewhere: the provider says so, and the first screen is what waits.
    await shellDo(page, 'setState', signedIn)
    await expect(page.locator('[data-shell-title]')).toHaveText('Waiting for you')
    await expect(page.locator('[data-shell-line]')).toHaveText('Signed in as ana@example.test')
    await t.close()

    const w = await shellTab(browser, baseURL!, provider(signedOut, { signIn: { words: 'Desk relay <b>cannot</b> begin a sign-in now' } }))
    await w.page.goto('/shell')
    await w.page.locator('[data-shell-signin-begin]').click()
    await expect(w.page.locator('[data-shell-signin-words]')).toHaveText('Desk relay <b>cannot</b> begin a sign-in now')
    await expect(w.page.locator('[data-shell-signin-begin]')).toHaveText('Try again')
    await expect(w.page.locator('[data-shell-signin-words] b')).toHaveCount(0)
    await w.close()
  })

  test("Settings: who, the notifications, the test notification's words, and a sign-out with its end not yet confirmed", async ({ browser, baseURL }) => {
    const t = await shellTab(browser, baseURL!, provider({ ...signedIn, notifications: 'off' }, { test: { words: 'Sent: it reaches this phone in a moment' } }))
    const { page } = t
    await page.goto('/shell/settings')
    await expect(page.locator('[data-shell-who]')).toHaveText('ana@example.test')
    await expect(page.locator('[data-shell-notifications]')).toHaveText(/^Off · the last received at /)
    await page.locator('[data-shell-notify-on]').click()
    await expect(page.locator('[data-shell-notify-words]')).toHaveText('Notifications are on.')
    await page.locator('[data-shell-test]').click()
    await expect(page.locator('[data-shell-test-words]')).toHaveText('Sent: it reaches this phone in a moment')
    await page.locator('[data-shell-sign-out]').click()
    await expect(page.locator('[data-shell-ending]')).toHaveText('Signed out on this phone; Desk relay will be told when it can be reached')
    await expect(page.locator('[data-shell-who]')).toHaveText('Not signed in')
    await expect(page.locator('[data-shell-sign-out]')).toHaveCount(0)
    await page.locator('[data-shell-back]').click()
    await expect(page.locator('[data-shell-signin]')).toBeVisible()
    expect((await shellRecord(page)).calls).toEqual(expect.arrayContaining(['enableNotifications', 'test', 'signOut']))
    expect(t.api).toEqual([])
    await t.close()
  })

  test('the screens fit a phone with touch at the largest text size and a desktop, and every text clears its floor, in both themes', async ({ browser, baseURL }) => {
    test.setTimeout(600_000)
    const expiresAt = new Date(Date.now() + 9.5 * 60_000).toISOString()
    const screens: Array<{ name: string; cfg: FakeConfig; path: string; then?: (p: Page) => Promise<void> }> = [
      { name: 'Open a server', cfg: { provider: null }, path: '/shell', then: async (p) => {
          await p.getByLabel('Server address').fill('http://192.0.2.10:8080')
          await p.locator('[data-shell-open]').click()
          await expect(p.locator('[data-shell-recent-address]')).toHaveCount(1)
        } },
      { name: 'the sign-in', cfg: provider({ ...signedOut, words: 'Desk relay cannot answer now' }, { signIn: { code: 'WDJB-MJHT', page: 'https://id.example.test/device', expiresAt } }), path: '/shell', then: async (p) => {
          await p.locator('[data-shell-signin-begin]').click()
          await expect(p.locator('[data-shell-code]')).toBeVisible()
        } },
      { name: 'Settings', cfg: provider({ ...signedIn, notifications: 'off', words: 'Desk relay cannot be reached' }), path: '/shell/settings' },
      { name: 'Settings while ending', cfg: provider({ signedIn: false, ending: true, notifications: 'off' }), path: '/shell/settings' },
    ]
    for (const size of [PHONE, DESK]) {
      for (const theme of ['dark', 'light'] as const) {
        for (const s of screens) {
          const what = `${s.name} at ${size.width} in ${theme}`
          await test.step(what, async () => {
            const t = await shellTab(browser, baseURL!, s.cfg, size)
            const { page } = t
            await page.goto(s.path)
            await useTheme(page, theme, '[data-shell] [data-shell-title]')
            if (size === PHONE) await page.addStyleTag({ content: LARGEST_TEXT })
            await s.then?.(page)
            await noSideways(page)
            if (size === PHONE) await tapTarget(page.locator('[data-shell] :is(button, a, input):visible'))
            const { judged, failures } = await walk(page, page.locator('[data-shell]'))
            expect(judged, what).toBeGreaterThan(0)
            expect(failures, `${what}: every text over its floor`).toEqual([])
            expect(t.api, what).toEqual([])
            await t.close()
          })
        }
      }
    }
  })
})
