import { dirname } from 'node:path'
import { expect, test } from './fixtures'

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
  // One column that reads top to bottom (#366): at most 48rem wide, each card below the one before it, and the save bar in
  // the panel's footer, in reach wherever the page is scrolled.
  const grid = await page.locator('[data-settings-grid]').boundingBox()
  expect(grid!.width).toBeLessThanOrEqual(768)
  const cards = await page.locator('[data-settings-grid] > *').evaluateAll((els) => els.map((e) => ({ top: e.getBoundingClientRect().top, bottom: e.getBoundingClientRect().bottom })))
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
// row reads ready once the reply does, with no reload and the same token. It waits on the second request, never on the clock.
test('the Extensions row shows a provider that was starting as ready once it is, with no reload', async ({ page, state }) => {
  const replies = [
    { providers: [{ kind: 'identity', name: 'cloud', state: 'starting' }, { kind: 'notifications', name: 'push', state: 'ready' }] },
    { providers: [{ kind: 'identity', name: 'cloud', state: 'ready' }, { kind: 'notifications', name: 'push', state: 'ready' }] },
  ]
  let served = 0
  await page.route('**/api/capabilities', (route) => route.fulfill({ json: replies[Math.min(served++, replies.length - 1)] }))
  const first = page.waitForRequest('**/api/capabilities')
  await page.goto('/settings')
  const asked = await first
  // Asked for before the first reply can have been read, so the second request cannot pass it by.
  const second = page.waitForRequest('**/api/capabilities')
  const row = page.locator('[data-about-card] [data-about-row="extensions"]')
  await expect(row).toHaveText('identity: cloud (starting) · notifications: push')
  // A mark on the page that a reload would lose.
  await page.evaluate(() => ((window as unknown as { capabilitiesMark: number }).capabilitiesMark = 1))
  const again = await second
  await expect(row).toHaveText('identity: cloud · notifications: push')
  expect(await page.evaluate(() => (window as unknown as { capabilitiesMark?: number }).capabilitiesMark)).toBe(1)
  expect(asked.headers().authorization).toBe(`Bearer ${state.token}`)
  expect(again.headers().authorization).toBe(`Bearer ${state.token}`)
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
  await expect(page).toHaveURL(/\/settings$/)
  const card = page.locator('[data-switchyard-settings]')
  await expect(card).toBeVisible()
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
