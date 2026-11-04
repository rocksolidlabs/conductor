import { dirname } from 'node:path'
import { expect, test } from './fixtures'
import type { Locator, Page, Request } from '@playwright/test'
import type { E2EState } from './state'

// The desktop app's Settings page, under a stand-in for its bridge: the
// folder picker browses the server's own folders (GET /api/paths, scope any,
// as the app's server allows), never a native dialog, so on Windows it is the
// WSL distribution that is browsed.
test.skip(process.platform === 'win32', 'POSIX paths')

test('the folder picker browses the server, outside the roots too, and fills the field', async ({ page, state }) => {
  const form = {
    dataDir: state.data,
    allowedRoots: [state.root],
    defaultCwd: state.root,
    yolo: false,
    reach: 'off',
    closeToTray: true,
    wslDistro: '',
    wslWindowsHome: false,
    switchyardEnabled: false,
    switchyardServer: '',
    switchyardToken: '',
    switchyardName: '',
    noticed: ['publishing'],
  }
  await page.addInitScript(
    ({ token, form, url }) => {
      const settings = { ...form }
      ;(window as unknown as { conductorDesktop: unknown }).conductorDesktop = {
        version: 'e2e',
        platform: 'linux',
        token: async () => token,
        openExternal: async () => {},
        settings: { get: async () => ({ ...settings }), set: async (p: object) => Object.assign(settings, p) },
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
    { token: state.token, form, url: state.baseURL },
  )
  await page.goto('/settings')
  await page.locator('[data-pick-dir="defaultCwd"]').click()
  const picker = page.getByRole('dialog')
  await expect(picker.locator('[data-dir-picker-entry="repo"]')).toBeVisible()
  // Up from the root lists its parent: outside the allowed roots, which only the any scope lists.
  await picker.locator('[data-dir-picker-up]').click()
  await expect(picker.locator('[data-dir-picker-path]')).toHaveValue(dirname(state.root))
  await expect(picker.locator('[data-dir-picker-note]')).toContainText('Outside the allowed roots')
  // Back in by typing the path, into the repository, and use it.
  await picker.locator('[data-dir-picker-path]').fill(state.root)
  await picker.locator('[data-dir-picker-path]').press('Enter')
  await picker.locator('[data-dir-picker-entry="repo"]').click()
  await expect(picker.locator('[data-dir-picker-path]')).toHaveValue(state.repo)
  await expect(picker.locator('[data-dir-picker-note]')).toHaveCount(0)
  await picker.locator('[data-dir-picker-use]').click()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(page.getByLabel('Default working directory', { exact: true })).toHaveValue(state.repo)
  // One column that reads top to bottom (#366), each card below the one before it, and the save bar in the panel's footer, in
  // reach wherever the page is scrolled. How wide it is, and the navigation beside it (#433), are the tests below.
  const cards = await page.locator('[data-settings-sections] > [data-settings-section]').evaluateAll((els) => els.map((e) => ({ top: e.getBoundingClientRect().top, bottom: e.getBoundingClientRect().bottom })))
  expect(cards.length).toBeGreaterThan(3)
  for (let i = 1; i < cards.length; i++) expect(cards[i]!.top).toBeGreaterThanOrEqual(cards[i - 1]!.bottom)
  await expect(page.locator('[data-settings-save-bar]')).toBeInViewport()
  // The app card ends with the sponsor credit (the Sponsor Kit's 1c).
  const about = page.locator('[data-about-card]')
  await expect(about.locator('[data-about-row="app"]')).toHaveText('e2e')
  await expect(about.locator('[data-about-license]')).toContainText('Apache-2.0')
  await expect(about.locator('[data-about-copyright]')).toContainText('the Conductor Authors and RockSolid Labs, Inc.')
  await expect(about.locator('[data-about-credit]')).toContainText('Sponsored and maintained by RockSolid Labs')
  await expect(about.locator('[data-about-credit] a')).toHaveAttribute('href', 'https://rocksolidlabs.io')
  // The License row links the third-party notices, which open as text from the app's own server.
  const notices = about.locator('[data-about-notices]')
  await expect(notices).toHaveText('Third-party notices')
  const [tab] = await Promise.all([page.context().waitForEvent('page'), notices.click()])
  await expect(tab).toHaveURL(`${state.baseURL}/third-party-notices.txt`)
  await expect(tab.locator('body')).toContainText('THIRD-PARTY NOTICES')
  await expect(tab.locator('body')).toContainText('github.com/pion/webrtc/v4')
  await tab.close()
})

test('in a browser, Settings says what the server is and who sponsors it', async ({ page }) => {
  await page.goto('/settings')
  const about = page.locator('[data-about-card]')
  await expect(about.locator('[data-about-row="server"]')).not.toBeEmpty()
  // The e2e server is a plain build: nothing is registered beside the open-source feature set (GET /api/capabilities).
  await expect(about.locator('[data-about-row="extensions"]')).toHaveText('None: the open-source feature set')
  await expect(about.locator('[data-about-credit]')).toContainText('Sponsored and maintained by RockSolid Labs')
})

// A provider still starting as Settings opens (docs/plans/ext.md, C9): the row says so, the route is read again on its own, and the
// row reads ready once the reply does, with no reload and the same token. Every reply after the first is held until the test has
// seen the starting row and set its mark, so the starting row holds for as long as the test needs it; nothing waits on the clock.
test('the Extensions row shows a provider that was starting as ready once it is, with no reload', async ({ page, state }) => {
  const replies = [
    { providers: [{ kind: 'identity', name: 'cloud', state: 'starting' }, { kind: 'notifications', name: 'push', state: 'ready' }] },
    { providers: [{ kind: 'identity', name: 'cloud', state: 'ready' }, { kind: 'notifications', name: 'push', state: 'ready' }] },
  ]
  let release!: () => void
  const held = new Promise<void>((resolve) => (release = resolve))
  const asked: Request[] = []
  await page.route('**/api/capabilities', async (route) => {
    const n = asked.push(route.request()) - 1
    if (n > 0) await held
    await route.fulfill({ json: replies[Math.min(n, replies.length - 1)] })
  })
  await page.goto('/settings')
  const row = page.locator('[data-about-card] [data-about-row="extensions"]')
  await expect(row).toHaveText('identity: cloud (starting) · notifications: push')
  // A mark on the page that a reload would lose.
  await page.evaluate(() => ((window as unknown as { capabilitiesMark: number }).capabilitiesMark = 1))
  release()
  await expect(row).toHaveText('identity: cloud · notifications: push')
  expect(asked.length).toBeGreaterThanOrEqual(2)
  expect(await page.evaluate(() => (window as unknown as { capabilitiesMark?: number }).capabilitiesMark)).toBe(1)
  expect(asked[0]!.headers().authorization).toBe(`Bearer ${state.token}`)
  expect(asked[1]!.headers().authorization).toBe(`Bearer ${state.token}`)
  await page.unroute('**/api/capabilities')
})

test('a "Runs in" field has a folder button that browses the allowed roots and fills it', async ({ page, state }) => {
  await page.goto('/crews/new')
  const field = page.locator('[data-crew-editor] [data-dir-input] input[role="combobox"]')
  await page.locator('[data-crew-editor] [data-dir-input-pick]').click()
  const picker = page
  // An empty field opens on the server's default directory.
  await expect(picker.locator('[data-dir-picker-path]')).toHaveValue(state.repo)
  // Inside the roots there is no Home to jump outside them.
  await expect(picker.locator('[data-dir-picker-home]')).toHaveCount(0)
  await picker.locator('[data-dir-picker-up]').click()
  await expect(picker.locator('[data-dir-picker-path]')).toHaveValue(state.root)
  await picker.locator('[data-dir-picker-entry="repo"]').click()
  await expect(picker.locator('[data-dir-picker-path]')).toHaveValue(state.repo)
  await picker.locator('[data-dir-picker-use]').click()
  await expect(page.locator('[data-dir-picker-path]')).toHaveCount(0)
  await expect(field).toHaveValue(state.repo)
})

// The one-time notice the desktop app hands the workbench (a fresh install's,
// or an upgrade's): one toast that says sessions are published to a
// switchyard, that it mints and checks the links, and how to turn it off, with
// the way to Settings.
test('the app\'s publishing notice says what is published and leads to Settings', async ({ page, state }) => {
  const form = {
    dataDir: state.data,
    allowedRoots: [state.root],
    defaultCwd: state.root,
    yolo: false,
    reach: 'off',
    closeToTray: true,
    wslDistro: '',
    wslWindowsHome: false,
    switchyardEnabled: true,
    switchyardServer: '',
    switchyardToken: '',
    switchyardName: '',
    noticed: ['publishing'],
  }
  await page.addInitScript(
    ({ token, form, url }) => {
      const settings = { ...form }
      // The app hands the notice out once; a page loaded again is told nothing.
      let owed = sessionStorage.getItem('e2e.noticed') ? '' : 'publishing'
      ;(window as unknown as { conductorDesktop: unknown }).conductorDesktop = {
        version: 'e2e',
        platform: 'linux',
        token: async () => token,
        openExternal: async () => {},
        settings: { get: async () => ({ ...settings }), set: async (p: object) => Object.assign(settings, p) },
        restartServer: async () => {},
        openInBrowser: async () => {},
        showLog: async () => {},
        serverState: async () => ({ state: 'running', url, failures: 0 }),
        notice: async () => {
          const n = owed
          owed = ''
          if (n) sessionStorage.setItem('e2e.noticed', '1')
          return n
        },
        ice: async () => ({ forwarding: false, port: 0, publicIp: '', wslAddress: '', firewall: 'unknown' }),
        allowIceFirewall: async () => 'unknown',
        versions: async () => ({ app: 'e2e', electron: '', node: '', chrome: '', server: '' }),
        onServerState: () => () => {},
      }
    },
    { token: state.token, form, url: state.baseURL },
  )
  await page.goto('/yard')
  const toast = page.locator('li').filter({ hasText: 'Sessions are published to a switchyard' }).last()
  await expect(toast).toBeVisible({ timeout: 15_000 })
  await expect(toast).toContainText('switchyard.rslabs.net')
  await expect(toast).toContainText('publish only to one whose operator you trust')
  await expect(toast).toContainText('Settings → Switchyard')
  await toast.getByRole('button', { name: 'Settings' }).click()
  // The notice names Settings → Switchyard: its button opens that section (#433).
  await expect(page).toHaveURL(/\/settings\?section=switchyard$/)
  const card = page.locator('[data-switchyard-settings]')
  await expect(card).toBeInViewport()
  await expect(page.locator('[data-settings-nav-entry="switchyard"]')).toHaveAttribute('aria-current', 'true')
  // The card says what the switchyard is told and what it does.
  await expect(card).toContainText("never its chat, activity or command line")
  await expect(card).toContainText('It mints and checks the links and decides who joins')
  await expect(card).toContainText('Publish only to a switchyard whose operator you trust')
  // Once only.
  await page.reload()
  await expect(page.locator('[data-switchyard-settings]')).toBeVisible()
  await expect(page.getByText('Sessions are published to a switchyard')).toHaveCount(0)
})

// A switchyard named as plain http on another machine is refused as it is
// saved, with the words why, and nothing reaches the app; one on this
// machine is saved and kept. Settings saved with such a switchyard before
// the app refused it show the words as the page opens.
test('a plain switchyard on another machine is refused at save with the words why, and a loopback one is kept', async ({ page, state }) => {
  const words = 'the switchyard must be https:// unless it runs on this machine (localhost): over plain http:// what is published there travels in the clear and unverified'
  const form = {
    dataDir: state.data,
    allowedRoots: [state.root],
    defaultCwd: state.root,
    yolo: false,
    reach: 'off',
    closeToTray: true,
    wslDistro: '',
    wslWindowsHome: false,
    switchyardEnabled: true,
    switchyardServer: '',
    switchyardToken: '',
    switchyardName: '',
    noticed: ['publishing'],
  }
  await page.addInitScript(
    ({ token, form, url }) => {
      // The app's settings across reloads of the page, and every save the page asked of it.
      const kept = sessionStorage.getItem('e2e.settings')
      const settings = kept ? JSON.parse(kept) : { ...form }
      const w = window as unknown as { conductorDesktop: unknown; e2eSets: object[] }
      w.e2eSets = JSON.parse(sessionStorage.getItem('e2e.sets') ?? '[]')
      w.conductorDesktop = {
        version: 'e2e',
        platform: 'linux',
        token: async () => token,
        openExternal: async () => {},
        settings: {
          get: async () => ({ ...settings }),
          set: async (p: object) => {
            w.e2eSets.push(p)
            sessionStorage.setItem('e2e.sets', JSON.stringify(w.e2eSets))
            Object.assign(settings, p)
            sessionStorage.setItem('e2e.settings', JSON.stringify(settings))
            return { ...settings }
          },
        },
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
    { token: state.token, form, url: state.baseURL },
  )
  const sets = () => page.evaluate(() => (window as unknown as { e2eSets: Array<{ switchyardServer?: string }> }).e2eSets)
  await page.goto('/settings')
  const field = page.locator('[data-switchyard-server]')
  const input = field.locator('input')
  const save = page.locator('[data-save-settings]')
  await expect(input).toBeEnabled()
  await expect(field).not.toContainText(words)

  // Another machine, plain: refused at save, the words under the field and at the top, nothing sent to the app.
  await input.fill('http://yard.example:8080')
  await expect(field).not.toContainText(words)
  await save.click()
  await expect(field).toContainText(words)
  await expect(page.locator('[data-settings-error]')).toContainText(words)
  expect(await sets()).toEqual([])
  // Changing the field takes the words away until the next save.
  await input.fill('http://yard.example:808')
  await expect(field).not.toContainText(words)
  await save.click()
  await expect(field).toContainText(words)
  expect(await sets()).toEqual([])

  // This machine: saved, and kept.
  await input.fill('http://localhost:9090')
  await expect(field).not.toContainText(words)
  await save.click()
  await expect(page.locator('[data-settings-error]')).toHaveCount(0)
  await expect(field).not.toContainText(words)
  await expect.poll(async () => (await sets()).map((s) => s.switchyardServer)).toEqual(['http://localhost:9090'])
  await page.reload()
  await expect(page.locator('[data-switchyard-server] input')).toHaveValue('http://localhost:9090')
  await expect(page.locator('[data-switchyard-server]')).not.toContainText(words)

  // Settings saved with one before the app refused it: the words as the page opens.
  await page.evaluate(() => {
    const s = JSON.parse(sessionStorage.getItem('e2e.settings')!)
    sessionStorage.setItem('e2e.settings', JSON.stringify({ ...s, switchyardServer: 'http://192.168.1.10:8080' }))
  })
  await page.reload()
  await expect(page.locator('[data-switchyard-server] input')).toHaveValue('http://192.168.1.10:8080')
  await expect(page.locator('[data-switchyard-server]')).toContainText(words)
  // An https one is saved.
  await page.locator('[data-switchyard-server] input').fill('https://yard.example')
  await page.locator('[data-save-settings]').click()
  await expect.poll(async () => (await sets()).map((s) => s.switchyardServer)).toEqual(['http://localhost:9090', 'https://yard.example'])
  await expect(page.locator('[data-switchyard-server]')).not.toContainText(words)
})

// A switchyard named with more than its address (a user name, a password, a
// query, a fragment) is refused as it is saved, with the words why, and
// nothing reaches the app; settings saved with one before the app refused it
// show the words as the page opens. Its address alone is saved.
test('a switchyard named with more than its address is refused at save with the words why', async ({ page, state }) => {
  const words = 'the switchyard must be named by its address alone, with no user name, password, query or fragment; a host token has a field of its own'
  const form = {
    dataDir: state.data,
    allowedRoots: [state.root],
    defaultCwd: state.root,
    yolo: false,
    reach: 'off',
    closeToTray: true,
    wslDistro: '',
    wslWindowsHome: false,
    switchyardEnabled: true,
    switchyardServer: 'https://:pw@yard.example',
    switchyardToken: '',
    switchyardName: '',
    noticed: ['publishing'],
  }
  await page.addInitScript(
    ({ token, form, url }) => {
      // The app's settings across reloads of the page, and every save the page asked of it.
      const kept = sessionStorage.getItem('e2e.settings')
      const settings = kept ? JSON.parse(kept) : { ...form }
      const w = window as unknown as { conductorDesktop: unknown; e2eSets: object[] }
      w.e2eSets = JSON.parse(sessionStorage.getItem('e2e.sets') ?? '[]')
      w.conductorDesktop = {
        version: 'e2e',
        platform: 'linux',
        token: async () => token,
        openExternal: async () => {},
        settings: {
          get: async () => ({ ...settings }),
          set: async (p: object) => {
            w.e2eSets.push(p)
            sessionStorage.setItem('e2e.sets', JSON.stringify(w.e2eSets))
            Object.assign(settings, p)
            sessionStorage.setItem('e2e.settings', JSON.stringify(settings))
            return { ...settings }
          },
        },
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
    { token: state.token, form, url: state.baseURL },
  )
  const sets = () => page.evaluate(() => (window as unknown as { e2eSets: Array<{ switchyardServer?: string }> }).e2eSets)
  await page.goto('/settings')
  const field = page.locator('[data-switchyard-server]')
  const input = field.locator('input')
  const save = page.locator('[data-save-settings]')

  // Saved with one before the app refused it: the words as the page opens.
  await expect(input).toHaveValue('https://:pw@yard.example')
  await expect(field).toContainText(words)

  // Each refused at save, the words under the field and at the top, nothing sent to the app.
  for (const server of ['https://me:pw@yard.example', 'https://@yard.example', 'https://yard.example/?token=t', 'https://yard.example#']) {
    await input.fill(server)
    await expect(field).not.toContainText(words)
    await save.click()
    await expect(field).toContainText(words)
    await expect(page.locator('[data-settings-error]')).toContainText(words)
  }
  expect(await sets()).toEqual([])

  // Its address alone, a path included: saved, and kept.
  await input.fill('https://yard.example/team')
  await save.click()
  await expect(page.locator('[data-settings-error]')).toHaveCount(0)
  await expect(field).not.toContainText(words)
  await expect.poll(async () => (await sets()).map((s) => s.switchyardServer)).toEqual(['https://yard.example/team'])
  await page.reload()
  await expect(page.locator('[data-switchyard-server] input')).toHaveValue('https://yard.example/team')
  await expect(page.locator('[data-switchyard-server]')).not.toContainText(words)
})

// The save toast says what became of the save: the server restarted for a
// change to where or how agents run, or it was saved alone. The app says which
// (the bridge's settings.outcome, once): a restarted server listens on a fresh
// port and the app loads this page there, so the page that saved is gone before
// its save resolves and the page that followed shows the toast; a restart that
// fails is an error, and the retry after it (the settings were kept) says
// saved, never restarted (#214). The stand-in here is the app: it keeps the
// settings and the outcome across the reload it makes for a restart that
// moves the server, and fails a restart when told to.
test('the save toast says what became of the save, on the page that saved or the one that followed the restarted server', async ({ page, state }) => {
  const form = {
    dataDir: state.data,
    allowedRoots: [state.root],
    defaultCwd: state.root,
    yolo: false,
    reach: 'off',
    closeToTray: true,
    wslDistro: '',
    wslWindowsHome: false,
    switchyardEnabled: false,
    switchyardServer: '',
    switchyardToken: '',
    switchyardName: '',
    noticed: ['publishing'],
  }
  await page.addInitScript(
    ({ token, form, url }) => {
      const SERVER = ['dataDir', 'allowedRoots', 'defaultCwd', 'yolo', 'reach', 'wslDistro', 'wslWindowsHome', 'switchyardEnabled', 'switchyardServer', 'switchyardToken', 'switchyardName']
      const kept = sessionStorage.getItem('e2e.settings')
      const settings: Record<string, unknown> = kept ? JSON.parse(kept) : { ...form }
      ;(window as unknown as { conductorDesktop: unknown }).conductorDesktop = {
        version: 'e2e',
        platform: 'linux',
        token: async () => token,
        openExternal: async () => {},
        settings: {
          get: async () => ({ ...settings }),
          set: async (p: Record<string, unknown>) => {
            const restart = SERVER.some((k) => JSON.stringify(settings[k]) !== JSON.stringify(p[k]))
            Object.assign(settings, p)
            sessionStorage.setItem('e2e.settings', JSON.stringify(settings))
            if (!restart) {
              sessionStorage.setItem('e2e.outcome', 'saved')
              return { ...settings }
            }
            const how = sessionStorage.getItem('e2e.restart')
            sessionStorage.removeItem('e2e.restart')
            if (how === 'fail') throw new Error('the server did not start') // the settings were kept; nothing is owed
            sessionStorage.setItem('e2e.outcome', 'restarted')
            if (how === 'follow') {
              // The server came back on a fresh port and the app loads the page there: this page's save never resolves.
              location.reload()
              return new Promise(() => {})
            }
            return { ...settings }
          },
          outcome: async () => {
            const o = sessionStorage.getItem('e2e.outcome') ?? ''
            sessionStorage.removeItem('e2e.outcome')
            return o
          },
        },
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
    { token: state.token, form, url: state.baseURL },
  )
  await page.goto('/settings')
  const save = page.locator('[data-save-settings]')
  // Exact words: the toaster also announces a toast in a hidden live region whose text begins with its own word.
  const toast = (words: string) => page.getByText(words, { exact: true })
  await expect(save).toBeDisabled()

  // On the page that saved: yolo is how agents run, the server restarts (here without moving), and the toast says so.
  await page.locator('[data-yolo-switch]').click()
  await expect(save).toHaveText('Save and restart the server')
  await save.click()
  await expect(toast('Saved; the server restarted')).toBeVisible()
  await expect(save).toBeDisabled()
  // Closing to the tray is the window's: saved, no restart.
  await page.locator('[data-tray-switch]').click()
  await expect(save).toHaveText('Save')
  await save.click()
  await expect(toast('Saved')).toBeVisible()

  // On the page that followed: the server came back on a fresh port and the app loaded this page there, where the toast is owed.
  await page.evaluate(() => sessionStorage.setItem('e2e.restart', 'follow'))
  await page.locator('[data-yolo-switch]').click()
  await save.click()
  await expect(toast('Saved; the server restarted')).toBeVisible()
  await expect(save).toBeDisabled()
  await expect(toast('Saved; the server restarted')).toBeHidden({ timeout: 15_000 })

  // A restart that fails: the error and no toast; the retry, the settings kept, says saved alone.
  await page.evaluate(() => sessionStorage.setItem('e2e.restart', 'fail'))
  await page.locator('[data-yolo-switch]').click()
  await expect(save).toHaveText('Save and restart the server')
  await save.click()
  await expect(page.locator('[data-settings-error]')).toContainText('did not start')
  await expect(toast('Saved; the server restarted')).toHaveCount(0)
  await save.click()
  await expect(toast('Saved')).toBeVisible()
  await expect(toast('Saved; the server restarted')).toHaveCount(0)
})

// Settings on a wide window and a phone (#433): a navigation beside one column of sections (#366: never two side by side), the
// whole as wide as the panel's body up to 80rem, each section no taller than the visible body at 1440 × 900; the section chosen in
// `?section=`; below lg a sticky row of links, nothing else scrolling sideways, and the save bar above the bottom bar. The
// stand-in bridge is the app's, as above; on win32 with an ICE status the WebRTC section and the WSL fields are laid out too.
type Platform = 'linux' | 'win32'
async function standIn(page: Page, state: E2EState, platform: Platform) {
  const form = {
    dataDir: state.data,
    allowedRoots: [state.root],
    defaultCwd: state.root,
    yolo: false,
    reach: 'off',
    closeToTray: true,
    wslDistro: '',
    wslWindowsHome: false,
    switchyardEnabled: false,
    switchyardServer: '',
    switchyardToken: '',
    switchyardName: '',
    noticed: ['publishing'],
  }
  await page.addInitScript(
    ({ token, form, url, platform }) => {
      const settings = { ...form }
      const w = window as unknown as { conductorDesktop: unknown; e2eSets: object[] }
      w.e2eSets = []
      w.conductorDesktop = {
        version: 'e2e',
        platform,
        token: async () => token,
        openExternal: async () => {},
        settings: {
          get: async () => ({ ...settings }),
          set: async (p: object) => {
            w.e2eSets.push(p)
            return Object.assign(settings, p)
          },
        },
        restartServer: async () => {},
        openInBrowser: async () => {},
        showLog: async () => {},
        serverState: async () => ({ state: 'running', url, failures: 0 }),
        notice: async () => '',
        ice: async () => ({ forwarding: true, port: 40001, publicIp: '203.0.113.7', wslAddress: '172.20.0.2', firewall: 'missing' }),
        allowIceFirewall: async () => 'present',
        versions: async () => ({ app: 'e2e', electron: '', node: '', chrome: '', server: '' }),
        onServerState: () => () => {},
      }
    },
    { token: state.token, form, url: state.baseURL, platform },
  )
}

/** The sections this machine shows, in page order: what the navigation must list. */
function expectedSections(platform: Platform) {
  return [...(platform === 'win32' ? ['webrtc'] : []), 'switchyard', 'folders', 'running', 'agents', 'brakeman', 'app']
}

const body = (page: Page) => page.locator('[data-slot="body"]:has([data-settings-sections])')
const boxOf = (l: Locator) => l.evaluate((e) => JSON.parse(JSON.stringify(e.getBoundingClientRect())) as DOMRect)
/** Every section's box, in page order, and the body's visible part. */
async function layoutOf(page: Page) {
  return body(page).evaluate((b) => {
    const r = (e: Element) => e.getBoundingClientRect()
    const cs = getComputedStyle(b)
    return {
      body: { top: r(b).top, bottom: r(b).bottom, left: r(b).left, right: r(b).right, clientWidth: b.clientWidth, clientHeight: b.clientHeight, scrollWidth: b.scrollWidth, padLeft: parseFloat(cs.paddingLeft), padRight: parseFloat(cs.paddingRight), scrollTop: b.scrollTop },
      sections: Array.from(b.querySelectorAll('[data-settings-section]')).map((e) => ({ id: e.getAttribute('data-settings-section')!, top: r(e).top, bottom: r(e).bottom, left: r(e).left, right: r(e).right, height: r(e).height })),
      layout: (() => {
        const l = b.querySelector('[data-desktop-settings], [data-settings-sections]')!
        return { left: r(l).left, right: r(l).right, width: r(l).width }
      })(),
      doc: { scrollWidth: document.documentElement.scrollWidth, clientWidth: document.documentElement.clientWidth },
    }
  })
}
/** Waits for what the page renders once the bridge, the catalog and Brakeman answered. */
async function settled(page: Page, platform: Platform) {
  await expect(page.locator('[data-agent]').first()).toBeVisible()
  await expect(page.locator('[data-brakeman-enabled]')).toBeVisible()
  if (platform === 'win32') await expect(page.locator('[data-ice-status]')).toBeVisible()
  await expect(page.locator('[data-settings-nav-entry]')).toHaveCount(expectedSections(platform).length)
}

for (const platform of ['linux', 'win32'] as const) {
  for (const size of [{ width: 1440, height: 900 }, { width: 2560, height: 1440 }]) {
    test(`on a wide window (${size.width} × ${size.height}, ${platform}) the navigation sits beside one column of sections that fills the body up to 80rem`, async ({ page, state }) => {
      await page.setViewportSize(size)
      await standIn(page, state, platform)
      await page.goto('/settings')
      await settled(page, platform)
      if (platform === 'win32') await expect(page.locator('[data-wsl-paths]')).toBeVisible()
      const nav = page.locator('[data-settings-nav]')
      const entries = page.locator('[data-settings-nav-entry]')
      expect(await entries.evaluateAll((es) => es.map((e) => e.getAttribute('data-settings-nav-entry')))).toEqual(expectedSections(platform))
      const l = await layoutOf(page)
      expect(l.sections.map((s) => s.id)).toEqual(expectedSections(platform))
      // The navigation left of the sections, and the whole as wide as the body less its padding, up to 1280 px and centered beyond.
      const n = await boxOf(nav)
      expect(n.right).toBeLessThanOrEqual(l.sections[0]!.left)
      const room = l.body.clientWidth - l.body.padLeft - l.body.padRight
      expect(Math.abs(l.layout.width - Math.min(room, 1280))).toBeLessThanOrEqual(1)
      if (room > 1280) expect(Math.abs(l.layout.left - l.body.left - (l.body.clientWidth - l.layout.width) / 2)).toBeLessThanOrEqual(1)
      // One column: each section below the one before, all of one width.
      for (let i = 1; i < l.sections.length; i++) {
        expect(l.sections[i]!.top).toBeGreaterThanOrEqual(l.sections[i - 1]!.bottom)
        expect(l.sections[i]!.left).toBe(l.sections[0]!.left)
        expect(l.sections[i]!.right).toBe(l.sections[0]!.right)
      }
      // No section taller than the visible body at 1440 × 900, but Brakeman's: its form came after the issue and holds more than
      // a screen (a question for the owner in #433's pull request).
      if (size.width === 1440) for (const s of l.sections) if (s.id !== 'brakeman') expect(s.height, s.id).toBeLessThanOrEqual(l.body.clientHeight)
      // The fields of a section as wide as this are rows: the label left of its control, which stops at 36rem.
      const field = page.locator('[data-switchyard-server]')
      const label = await boxOf(field.locator('label'))
      const input = await boxOf(field.locator('input'))
      expect(input.left).toBeGreaterThan(label.right)
      expect(input.width).toBeLessThanOrEqual(576)
      const roots = await boxOf(page.getByLabel('Allowed roots'))
      expect(roots.width).toBeLessThanOrEqual(576)
      await expect(page.locator('[data-settings-save-bar]')).toBeInViewport()
      // The navigation stays in place as the sections scroll, and marks the section in view; each entry brings its own into view.
      const before = await boxOf(nav)
      for (const id of expectedSections(platform)) {
        await page.locator(`[data-settings-nav-entry="${id}"]`).click()
        await expect(page).toHaveURL(new RegExp(`/settings\\?section=${id}$`))
        const section = page.locator(`[data-settings-section="${id}"]`)
        await expect(section).toBeInViewport()
        await expect(page.locator(`[data-settings-nav-entry="${id}"]`)).toHaveAttribute('aria-current', 'true')
        await expect(page.locator('[data-settings-nav-entry][aria-current]')).toHaveCount(1)
        expect((await boxOf(nav)).top).toBe(before.top)
      }
      // The window never scrolls: the body does.
      expect(await page.evaluate(() => window.scrollY)).toBe(0)
      expect((await layoutOf(page)).body.scrollTop).toBeGreaterThan(0)
      await expect(page.locator('[data-settings-save-bar]')).toBeInViewport()
    })
  }

  test(`/settings?section=<id> opens on that section once it has rendered, and an unknown one at the top (${platform})`, async ({ page, state }) => {
    await standIn(page, state, platform)
    for (const id of ['switchyard', 'agents', 'app'] as const) {
      await page.goto(`/settings?section=${id}`)
      await settled(page, platform)
      // The section's top at the top of the body, less its scroll margin (the body's padding), or, for one too near the end to
      // get there, the body scrolled to its end with the section whole in view.
      await expect.poll(async () => {
        const l = await layoutOf(page)
        const x = l.sections.find((x) => x.id === id)!
        const atEnd = await body(page).evaluate((b) => b.scrollTop > 0 && b.scrollTop + b.clientHeight >= b.scrollHeight - 1)
        return Math.abs(x.top - l.body.top - 24) <= 1 || (atEnd && x.top >= l.body.top && x.bottom <= l.body.bottom)
      }).toBe(true)
      await expect(page.locator(`[data-settings-nav-entry="${id}"]`)).toHaveAttribute('aria-current', 'true')
    }
    for (const q of ['?section=nope', '?section=', '', platform === 'linux' ? '?section=webrtc' : '?section=Switchyard']) {
      await page.goto(`/settings${q}`)
      await settled(page, platform)
      expect((await layoutOf(page)).body.scrollTop).toBe(0)
    }
  })

  test.describe(`on a phone (390 × 844, ${platform})`, () => {
    test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

    test(`one column, a sticky row of section links, nothing scrolling sideways, and the save bar above the bottom bar`, async ({ page, state }) => {
      await standIn(page, state, platform)
      await page.goto('/settings')
      await settled(page, platform)
      if (platform === 'win32') await expect(page.locator('[data-wsl-paths]')).toBeVisible()
      const l = await layoutOf(page)
      // One column, full width less a 16 px gutter.
      expect(l.body.padLeft).toBe(16)
      for (const s of l.sections) {
        expect(s.left - l.body.left).toBe(16)
        expect(l.body.left + l.body.clientWidth - s.right).toBe(16)
      }
      for (let i = 1; i < l.sections.length; i++) expect(l.sections[i]!.top).toBeGreaterThanOrEqual(l.sections[i - 1]!.bottom)
      // Nothing scrolls sideways but the row of links.
      expect(l.doc.scrollWidth).toBeLessThanOrEqual(l.doc.clientWidth)
      expect(l.body.scrollWidth).toBeLessThanOrEqual(l.body.clientWidth)
      // The fields stack, and the folder buttons stay beside their inputs.
      const field = page.locator('[data-switchyard-server]')
      expect((await boxOf(field.locator('input'))).top).toBeGreaterThan((await boxOf(field.locator('label'))).bottom)
      const cwd = await boxOf(page.getByLabel('Default working directory', { exact: true }))
      const pick = await boxOf(page.locator('[data-pick-dir="defaultCwd"]'))
      expect(pick.left).toBeGreaterThanOrEqual(cwd.right)
      expect(Math.abs(pick.top - cwd.top)).toBeLessThanOrEqual(4)
      // The navbar fits: its buttons, icons alone, inside the window.
      // The panel's navbar: the element before its body.
      const navbar = body(page).locator('xpath=preceding-sibling::*[1]')
      for (const b of ['[data-open-in-browser]', '[data-server-log]', '[data-restart-server]', '[data-fullscreen]']) {
        const box = await boxOf(page.locator(b))
        expect(box.right, b).toBeLessThanOrEqual(390)
        expect(box.width, b).toBeLessThanOrEqual(40)
      }
      expect(await navbar.evaluate((e) => e.scrollWidth <= e.clientWidth)).toBe(true)
      await expect(page.getByRole('button', { name: 'Restart server' })).toBeVisible()
      // The row of links: across the top of the body, stuck there as the sections scroll, scrolling sideways within itself.
      const nav = page.locator('[data-settings-nav]')
      expect(await nav.evaluate((e) => getComputedStyle(e).flexDirection)).toBe('row')
      const row = await boxOf(nav)
      expect(Math.round(row.top)).toBe(Math.round(l.body.top))
      expect(await nav.evaluate((e) => e.scrollWidth > e.clientWidth)).toBe(true)
      await page.locator('[data-settings-nav-entry="app"]').click()
      await expect(page).toHaveURL(/\/settings\?section=app$/)
      const app = page.locator('[data-settings-section="app"]')
      await expect(app).toBeInViewport()
      expect((await boxOf(app)).top).toBeGreaterThanOrEqual((await boxOf(nav)).bottom)
      expect(Math.round((await boxOf(nav)).top)).toBe(Math.round(l.body.top))
      await expect(page.locator('[data-settings-nav-entry="app"]')).toHaveAttribute('aria-current', 'true')
      await page.locator('[data-settings-nav-entry="running"]').click()
      await expect(page).toHaveURL(/\/settings\?section=running$/)
      const running = page.locator('[data-settings-section="running"]')
      expect((await boxOf(running)).top).toBeGreaterThanOrEqual((await boxOf(nav)).bottom)
      await expect(page.locator('[data-settings-nav-entry="running"]')).toHaveAttribute('aria-current', 'true')
      // A field changed and saved, the save bar in the window and above the bottom bar.
      await page.locator('[data-yolo-switch]').click()
      const save = page.locator('[data-save-settings]')
      await expect(save).toHaveText('Save and restart the server')
      const bar = page.locator('[data-settings-save-bar]')
      await expect(bar).toBeInViewport()
      expect((await boxOf(bar)).bottom).toBeLessThanOrEqual((await boxOf(page.locator('[data-bottom-bar]'))).top)
      await save.click()
      await expect(page.getByText('Saved; the server restarted', { exact: true })).toBeVisible()
      expect(await page.evaluate(() => (window as unknown as { e2eSets: Array<{ yolo?: boolean }> }).e2eSets.map((s) => s.yolo))).toEqual([true])
      // The section in view went in the address before the restart, so the page that follows the server opens on it.
      await expect(page).toHaveURL(/\/settings\?section=running$/)
    })
  })
}

test('the save that restarts the server keeps the section in view in the address, for the page that follows it', async ({ page, state }) => {
  await standIn(page, state, 'linux')
  await page.goto('/settings')
  await settled(page, 'linux')
  // Scrolled by hand to How agents run, its Yolo changed and saved.
  await page.locator('[data-yolo-switch]').scrollIntoViewIfNeeded()
  await body(page).evaluate((b) => {
    const s = b.querySelector('[data-settings-section="running"]')!
    b.scrollTop += s.getBoundingClientRect().top - b.getBoundingClientRect().top - 24
  })
  await expect(page.locator('[data-settings-nav-entry="running"]')).toHaveAttribute('aria-current', 'true')
  await expect(page).toHaveURL(/\/settings$/)
  await page.locator('[data-yolo-switch]').click()
  await page.locator('[data-save-settings]').click()
  await expect(page).toHaveURL(/\/settings\?section=running$/)
  // The page loaded there opens on it.
  await page.reload()
  await settled(page, 'linux')
  await expect.poll(async () => {
    const l = await layoutOf(page)
    return Math.abs(l.sections.find((s) => s.id === 'running')!.top - l.body.top - 24) <= 1
  }).toBe(true)
  // A save that does not restart leaves the address as it is.
  await page.goto('/settings')
  await settled(page, 'linux')
  await page.locator('[data-tray-switch]').click()
  await page.locator('[data-save-settings]').click()
  await expect(page.getByText('Saved', { exact: true })).toBeVisible()
  await expect(page).toHaveURL(/\/settings$/)
})

for (const size of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
  test(`in a browser at ${size.width} wide, the notice, Brakeman and The app use the width, stacked, with no section navigation`, async ({ page }) => {
    await page.setViewportSize(size)
    await page.goto('/settings')
    await expect(page.locator('[data-about-credit]')).toBeVisible()
    await expect(page.locator('[data-settings-nav]')).toHaveCount(0)
    const l = await layoutOf(page)
    const room = l.body.clientWidth - l.body.padLeft - l.body.padRight
    expect(Math.abs(l.layout.width - Math.min(room, 1280))).toBeLessThanOrEqual(1)
    expect(l.sections.map((s) => s.id).at(-1)).toBe('app')
    for (let i = 1; i < l.sections.length; i++) expect(l.sections[i]!.top).toBeGreaterThanOrEqual(l.sections[i - 1]!.bottom)
    expect(l.doc.scrollWidth).toBeLessThanOrEqual(l.doc.clientWidth)
    expect(l.body.scrollWidth).toBeLessThanOrEqual(l.body.clientWidth)
  })
}
