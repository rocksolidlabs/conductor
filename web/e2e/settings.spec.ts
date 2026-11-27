import { mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { expect, standingRoute, stopStrays, test, viaMore, type Api } from './fixtures'
import type { Locator, Page, Request } from '@playwright/test'
import { uncut } from './layout'
import type { E2EState } from './state'

// The desktop app's Settings page, under a stand-in for its bridge: the
// folder picker browses the server's own folders (GET /api/paths, scope any,
// as the app's server allows), never a native dialog, so on Windows it is the
// WSL distribution that is browsed.
test.skip(process.platform === 'win32', 'POSIX paths')

// The tests route the page while it works: the standing route keeps its interception on from before the first load.
// No session another spec left runs: a save that restarts the server asks first while agents run (UX3 R56), and the tests that save
// mean the save to go at once; the question has tests of its own, each with a session of its own.
test.beforeEach(async ({ page, api }) => {
  await standingRoute(page.context())
  await stopStrays(api, [])
})

/**
 * A folder of the test's own under the allowed root, holding the one folder it picks. A listing shows a folder's first 50 entries by
 * name, and the scratch root holds a folder for every spec that ran before this one: the repository at its root ("repo") fell past
 * the 50th as specs were added (#432 added three), so the picker is tried where the test knows what is listed (AGENTS.md, count only
 * what the test made).
 */
function pickerFolder(root: string, name: string): { dir: string; pick: string } {
  const dir = join(root, name)
  const pick = join(dir, 'pick-me')
  mkdirSync(pick, { recursive: true })
  return { dir, pick }
}

/** Adds a folder agents can use through Add folder and the picker, typed into its path: its chip shows. */
async function addFolder(page: Page, dir: string) {
  await page.locator('[data-pick-dir="root"]').click()
  const picker = page.getByRole('dialog')
  await expect(picker).toContainText('Add a folder agents can use')
  await picker.locator('[data-dir-picker-path]').fill(dir)
  await picker.locator('[data-dir-picker-path]').press('Enter')
  await expect(picker.locator('[data-dir-picker-path]')).toHaveValue(dir)
  await picker.locator('[data-dir-picker-use]').click()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(page.locator(`[data-root-chip="${dir}"]`)).toBeVisible()
}

test('the folder picker browses the server, outside the roots too, and fills the field', async ({ page, state }) => {
  const own = pickerFolder(state.root, 'settings-picker')
  const form = {
    dataDir: state.data,
    allowedRoots: [state.root],
    defaultCwd: own.dir,
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
  // It opens on the field's folder, listing what it holds.
  await expect(picker.locator('[data-dir-picker-path]')).toHaveValue(own.dir)
  await expect(picker.locator('[data-dir-picker-entry="pick-me"]')).toBeVisible()
  // Up to the root, then up from the root to its parent: outside the allowed roots, which only the any scope lists.
  await picker.locator('[data-dir-picker-up]').click()
  await expect(picker.locator('[data-dir-picker-path]')).toHaveValue(state.root)
  await picker.locator('[data-dir-picker-up]').click()
  await expect(picker.locator('[data-dir-picker-path]')).toHaveValue(dirname(state.root))
  await expect(picker.locator('[data-dir-picker-note]')).toContainText('Outside the allowed roots')
  // Back in by typing the path, into the folder by a click, and use it.
  await picker.locator('[data-dir-picker-path]').fill(own.dir)
  await picker.locator('[data-dir-picker-path]').press('Enter')
  await picker.locator('[data-dir-picker-entry="pick-me"]').click()
  await expect(picker.locator('[data-dir-picker-path]')).toHaveValue(own.pick)
  await expect(picker.locator('[data-dir-picker-note]')).toHaveCount(0)
  await picker.locator('[data-dir-picker-use]').click()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(page.getByLabel('Default folder', { exact: true })).toHaveValue(own.pick)
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
  // Into a folder of the test's own by its path, then a click into the folder it holds, and use it.
  const own = pickerFolder(state.root, 'runs-in-picker')
  await picker.locator('[data-dir-picker-path]').fill(own.dir)
  await picker.locator('[data-dir-picker-path]').press('Enter')
  await picker.locator('[data-dir-picker-entry="pick-me"]').click()
  await expect(picker.locator('[data-dir-picker-path]')).toHaveValue(own.pick)
  await picker.locator('[data-dir-picker-use]').click()
  await expect(page.locator('[data-dir-picker-path]')).toHaveCount(0)
  await expect(field).toHaveValue(own.pick)
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
  await expect(save).toHaveText('Save and restart')
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
  await expect(save).toHaveText('Save and restart')
  await save.click()
  await expect(page.locator('[data-settings-error]')).toContainText('did not start')
  await expect(toast('Saved; the server restarted')).toHaveCount(0)
  await save.click()
  await expect(toast('Saved')).toBeVisible()
  await expect(toast('Saved; the server restarted')).toHaveCount(0)
})

// One Save (#707). Brakeman's section has no button of its own: the page's Save saves it with the app's settings, Brakeman's part
// first (the app's may restart the server and move the page to its new port), and a change to Brakeman's alone neither sets the
// app's settings nor restarts the server. A part refused says which part was saved and which was not, and every edit is kept. The
// stand-in is the app, as above: it records what the server held of Brakeman's settings when the app's were set, and fails a save
// when told to. Brakeman's settings are this run's shared server's: the test puts back what it found.
type BrakemanConf = { rev: number; cwd?: string; budget: { readsPerTurn: number; turnsPerDay: number } } & Record<string, unknown>
const brakemanConf = (api: Api) => api.ok<{ config: BrakemanConf }>('GET', '/api/brakeman').then((r) => r.config)
async function putBack(api: Api, start: BrakemanConf) {
  const now = await brakemanConf(api)
  await api.ok('PUT', '/api/brakeman/config', { rev: now.rev, config: { ...start, rev: now.rev } })
}
/** The Reads a turn field of Brakeman's section, and a value of it that is not the one saved. */
const readsField = (page: Page) => page.locator('[data-brakeman-reads] input, input[data-brakeman-reads]').first()
/** Brakeman's limits (#993), in view under Limits since UX3 R59: a settings page folds nothing. Waits for them to show. */
async function brakemanAdvanced(page: Page) {
  await expect(page.locator('[data-brakeman-advanced-toggle]')).toHaveCount(0)
  await expect(readsField(page)).toBeVisible()
}

/** One set the stand-in app was asked: what the server held of Brakeman's reads then, and the two switches the steps change. */
type AppSet = { reads: number; yolo: boolean; closeToTray: boolean }
/**
 * The app, stood in (as above): it records each set with what the server held of Brakeman's settings when it was asked, and fails a
 * set when told to (`e2e.fail`): `refuse` before keeping the settings (the app's validate), `restart` after keeping them (its restart
 * failed, desktop/src/save.ts).
 */
async function oneSaveStandIn(page: Page, state: E2EState) {
  const form = { dataDir: state.data, allowedRoots: [state.root], defaultCwd: state.root, yolo: false, reach: 'off', closeToTray: true, wslDistro: '', wslWindowsHome: false, switchyardEnabled: false, switchyardServer: '', switchyardToken: '', switchyardName: '', noticed: ['publishing'] }
  await page.addInitScript(
    ({ token, form, url }) => {
      const settings: Record<string, unknown> = { ...form }
      const w = window as unknown as { conductorDesktop: unknown; e2eSets: AppSet[]; e2eSent: string[] }
      w.e2eSets = []
      w.e2eSent = []
      let outcome = ''
      w.conductorDesktop = {
        version: 'e2e',
        platform: 'linux',
        token: async () => token,
        openExternal: async () => {},
        settings: {
          get: async () => ({ ...settings }),
          set: async (p: Record<string, unknown>) => {
            w.e2eSent.push(JSON.stringify(p))
            const r = await fetch('/api/brakeman', { headers: { Authorization: `Bearer ${token}` } }).then((x) => x.json())
            w.e2eSets.push({ reads: r.config.budget.readsPerTurn, yolo: p.yolo as boolean, closeToTray: p.closeToTray as boolean })
            const fail = sessionStorage.getItem('e2e.fail')
            sessionStorage.removeItem('e2e.fail')
            if (fail === 'refuse') throw new Error('the settings were refused')
            outcome = p.yolo !== settings.yolo ? 'restarted' : 'saved'
            Object.assign(settings, p)
            if (fail === 'restart') {
              outcome = ''
              throw new Error('the server did not start')
            }
            return { ...settings }
          },
          outcome: async () => outcome,
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
  return {
    sets: () => page.evaluate(() => (window as unknown as { e2eSets: AppSet[] }).e2eSets),
    /** Each set's settings as the app was handed them, whole. */
    sent: () => page.evaluate(() => (window as unknown as { e2eSent: string[] }).e2eSent),
  }
}

test("the page's one Save saves Brakeman's section with the app's settings, and a part refused keeps every edit", async ({ page, state, api }) => {
  // Some forty waits, each within the expect's bound (15 s): the timeout bounds a hang, above their sum.
  test.setTimeout(42 * 15_000)
  const start = await brakemanConf(api)
  const reads = start.budget.readsPerTurn === 400 ? 500 : 400
  const { sets, sent } = await oneSaveStandIn(page, state)
  const readsOf = async () => (await sets()).map((s) => s.reads)
  const savedReads = () => brakemanConf(api).then((c) => c.budget.readsPerTurn)
  try {
    await page.goto('/settings')
    await settled(page, 'linux')
    await brakemanAdvanced(page)
    const save = page.locator('[data-save-settings]')
    const error = page.locator('[data-settings-error]')
    const toast = (words: string) => page.getByText(words, { exact: true })
    await expect(page.locator('[data-brakeman-save]')).toHaveCount(0)
    await expect(save).toBeDisabled()

    // Brakeman's alone: saved on the server, the app's settings never set, no restart.
    await readsField(page).fill(String(reads))
    await expect(save).toHaveText('Save')
    await save.click()
    await expect(toast('Saved')).toBeVisible()
    await expect.poll(savedReads).toBe(reads)
    await expect(save).toBeDisabled()
    expect(await sets()).toEqual([])

    // The GitHub card (IN1a) is not the page's Save's: it has its own Check and save. A token typed in it makes nothing for the page's
    // Save to save, and the Save below, which sends the app's settings and Brakeman's, sends nothing of the card.
    const ghToken = page.locator('[data-settings-section="github"] input[data-github-token]')
    const typed = 'ghp_settingsSpecNotTheSaves0123456789'
    const carried: string[] = []
    page.on('request', (r) => {
      if (r.url().includes('/api/trackers/github') || (r.postData() ?? '').includes(typed)) carried.push(`${r.method()} ${new URL(r.url()).pathname}`)
    })
    await ghToken.fill(typed)
    await expect(save).toBeDisabled()

    // Both: Brakeman's is on the server by the time the app's are set (and the server restarts).
    await page.locator('[data-yolo-switch]').click()
    await readsField(page).fill(String(reads + 1))
    await expect(save).toHaveText('Save and restart')
    await save.click()
    await expect(toast('Saved; the server restarted')).toBeVisible()
    await expect(save).toBeDisabled()
    expect(await readsOf()).toEqual([reads + 1])
    expect(carried).toEqual([])
    expect((await sent()).join('\n')).not.toContain(typed)
    // The card's field is left as it was: its own Check and save sends it, or nothing does.
    await expect(ghToken).toHaveValue(typed)
    await ghToken.fill('')

    // The app's part refused after Brakeman's was saved: the page says which, and the app's edit stays for the next Save.
    await page.evaluate(() => sessionStorage.setItem('e2e.fail', 'refuse'))
    await page.locator('[data-yolo-switch]').click()
    await readsField(page).fill(String(reads + 2))
    await save.click()
    await expect(error).toHaveText("Brakeman's settings were saved. The app's settings were not: the settings were refused. Your edits are kept.")
    await expect.poll(savedReads).toBe(reads + 2)
    await expect(page.locator('[data-yolo-switch]')).toHaveAttribute('aria-checked', 'false')
    await expect(save).toHaveText('Save and restart')
    // Saved this time (the toast of the save before may still show its same words: the app's record says it).
    await save.click()
    await expect.poll(async () => (await sets()).length).toBe(3)
    await expect(save).toBeDisabled()

    // The app kept its settings and the server did not restart (Codex's review): never said not saved.
    await page.evaluate(() => sessionStorage.setItem('e2e.fail', 'restart'))
    await page.locator('[data-yolo-switch]').click()
    await readsField(page).fill(String(reads + 3))
    await save.click()
    await expect(error).toHaveText("Brakeman's settings were saved. The app's settings were saved, but the server did not restart with them: the server did not start.")
    await expect.poll(savedReads).toBe(reads + 3)
    expect(await readsOf()).toEqual([reads + 1, reads + 2, reads + 2, reads + 3])
    // Saved again, the settings kept: saved alone, no restart.
    await save.click()
    await expect.poll(async () => (await sets()).length).toBe(5)
    await expect(save).toBeDisabled()

    // Brakeman's part refused (saved elsewhere meanwhile): the app's is held back, the edits stay, and Keep mine saves them over it.
    await page.locator('[data-tray-switch]').click()
    await readsField(page).fill(String(reads + 4))
    const elsewhere = await brakemanConf(api)
    const mine = elsewhere.budget.turnsPerDay
    await api.ok('PUT', '/api/brakeman/config', { rev: elsewhere.rev, config: { ...elsewhere, budget: { ...elsewhere.budget, turnsPerDay: mine === 7 ? 8 : 7 } } })
    await save.click()
    await expect(error).toHaveText(
      "Brakeman's settings were not saved: they were changed elsewhere since this form was read. The app's settings were held back with them, so nothing changed. Your edits are kept.",
    )
    // The words are said once the save has ended: no set can follow them.
    expect((await sets()).length).toBe(5)
    await expect(readsField(page)).toHaveValue(String(reads + 4))
    await expect(page.locator('[data-tray-switch]')).toHaveAttribute('aria-checked', 'false')
    const stale = page.locator('[data-brakeman-stale]')
    await stale.getByRole('button', { name: 'Keep mine' }).click()
    await expect(stale).toHaveCount(0)
    await save.click()
    // The app's record, not the server's state: the app's part is sent after Brakeman's answer (the review's fifth finding).
    await expect.poll(async () => (await sets()).length).toBe(6)
    expect(await readsOf()).toEqual([reads + 1, reads + 2, reads + 2, reads + 3, reads + 3, reads + 4])
    await expect(save).toBeDisabled()
    // Keep mine replaced the other change: the form's turns a day, not the ones saved elsewhere.
    const now = await brakemanConf(api)
    expect(now.budget.readsPerTurn).toBe(reads + 4)
    expect(now.budget.turnsPerDay).toBe(mine)
  } finally {
    await putBack(api, start)
  }
})

test("a Save that cannot send Brakeman's section with the app's settings is refused whole: nothing sent, every edit kept", async ({ page, state, api }) => {
  test.setTimeout(30 * 15_000)
  const start = await brakemanConf(api)
  const reads = start.budget.readsPerTurn === 400 ? 500 : 400
  const { sets } = await oneSaveStandIn(page, state)
  try {
    await page.goto('/settings')
    await settled(page, 'linux')
    await brakemanAdvanced(page)
    const save = page.locator('[data-save-settings]')
    const error = page.locator('[data-settings-error]')
    const dataDir = page.getByLabel('Where Conductor keeps its data', { exact: true })
    const cwd = page.locator('[data-brakeman-cwd] input, input[data-brakeman-cwd]').first()
    /** Nothing was sent: no set of the app's, Brakeman's settings as they were. */
    const nothingSent = async () => {
      expect(await sets()).toEqual([])
      expect((await brakemanConf(api)).rev).toBe(start.rev)
    }

    // A new data directory: the server would start on it without the Brakeman settings this Save writes to the one it leaves.
    await readsField(page).fill(String(reads))
    await dataDir.fill(`${state.data}-moved`)
    await save.click()
    await expect(error).toHaveText(
      "Brakeman's settings are kept in the server's data directory, and this Save moves the server off it: save the data directory alone first, then Brakeman's settings. Nothing was sent, Brakeman's settings with the rest. Your edits are kept.",
    )
    await nothingSent()
    await expect(dataDir).toHaveValue(`${state.data}-moved`)
    await expect(readsField(page)).toHaveValue(String(reads))
    await dataDir.fill(state.data)

    // A new root, and Brakeman's folder under it: the folder would be checked against the roots the server has now.
    const more = `${state.root}-more`
    mkdirSync(more, { recursive: true })
    await addFolder(page, more)
    await cwd.fill(more)
    await cwd.press('Escape')
    await save.click()
    await expect(error).toHaveText(
      "Brakeman's folder is checked against the allowed roots the server has now, and this Save changes them: save the allowed roots alone first, then Brakeman's folder. Nothing was sent, Brakeman's settings with the rest. Your edits are kept.",
    )
    await nothingSent()
    await expect(cwd).toHaveValue(more)
    await page.locator(`[data-root-chip="${more}"] [data-root-remove]`).click()
    await expect(page.locator(`[data-root-chip="${more}"]`)).toHaveCount(0)
    await cwd.fill(start.cwd ?? '')
    await cwd.press('Escape')

    // The switchyard refused: Brakeman's change was not sent either, and the words say so (the review's third finding).
    await page.locator('[data-switchyard-enabled]').click()
    await page.locator('[data-switchyard-server] input').fill('http://yard.example:8080')
    await save.click()
    await expect(error).toHaveText(
      "the switchyard must be https:// unless it runs on this machine (localhost): over plain http:// what is published there travels in the clear and unverified. Nothing was sent, Brakeman's settings with the rest. Your edits are kept.",
    )
    await nothingSent()
    await expect(readsField(page)).toHaveValue(String(reads))
  } finally {
    await putBack(api, start)
  }
})

test('the Save sends what the page held at the click, and Read again drops the edits only once confirmed and read', async ({ page, state, api }) => {
  test.setTimeout(45 * 15_000)
  const start = await brakemanConf(api)
  const reads = start.budget.readsPerTurn === 400 ? 500 : 400
  const { sets } = await oneSaveStandIn(page, state)
  const savedReads = () => brakemanConf(api).then((c) => c.budget.readsPerTurn)
  try {
    await page.goto('/settings')
    await settled(page, 'linux')
    await brakemanAdvanced(page)
    const save = page.locator('[data-save-settings]')
    const stale = page.locator('[data-brakeman-stale]')

    // Saved elsewhere meanwhile, then Read again (the review's sixth finding): cancelled, the edits stay; confirmed while the read
    // fails, the edits stay and the failure is said (Codex's review); confirmed and read, the form shows what is saved now.
    await readsField(page).fill(String(reads))
    const elsewhere = await brakemanConf(api)
    const theirs = elsewhere.budget.turnsPerDay === 7 ? 8 : 7
    await api.ok('PUT', '/api/brakeman/config', { rev: elsewhere.rev, config: { ...elsewhere, budget: { ...elsewhere.budget, turnsPerDay: theirs } } })
    await save.click()
    await expect(stale).toBeVisible()
    const readAgain = page.locator('[data-brakeman-read-again]')
    await stale.getByRole('button', { name: 'Read again' }).click()
    await page.getByRole('dialog').getByRole('button', { name: 'Cancel' }).click()
    await expect(page.getByRole('dialog')).toHaveCount(0)
    await expect(readsField(page)).toHaveValue(String(reads))
    await expect(stale).toBeVisible()
    // Every read of the configuration fails until the route is taken away: the choice's own among them.
    await page.route('**/api/brakeman', (route) => (route.request().method() === 'GET' ? route.abort() : route.continue()))
    await stale.getByRole('button', { name: 'Read again' }).click()
    await readAgain.click()
    await expect(page.locator('[data-brakeman-stale-error]')).toContainText('Not read:')
    await expect(readsField(page)).toHaveValue(String(reads))
    await expect(stale).toBeVisible()
    await page.unroute('**/api/brakeman')
    await stale.getByRole('button', { name: 'Read again' }).click()
    await readAgain.click()
    await expect(stale).toHaveCount(0)
    await expect(readsField(page)).toHaveValue(String(elsewhere.budget.readsPerTurn))
    await expect(page.locator('[data-brakeman-turns] input, input[data-brakeman-turns]').first()).toHaveValue(String(theirs))
    await expect(save).toBeDisabled()

    // Both parts taken at the click (Codex's review), Brakeman's save held. A Save that sends the app's settings holds every field
    // until it is answered (Codex's second round): the app may answer it by loading the page on a restarted server's port, where
    // nothing typed meanwhile could follow. The app is asked for what the page held at the click.
    let held = false
    let release!: () => void
    let gate = new Promise<void>((done) => (release = done))
    await page.route('**/api/brakeman/config', async (route) => {
      held = true
      await gate
      await route.continue()
    })
    await page.locator('[data-tray-switch]').click()
    await readsField(page).fill(String(reads + 1))
    await save.click()
    await expect.poll(() => held).toBe(true)
    await expect(page.locator('[data-yolo-switch]')).toBeDisabled()
    await expect(readsField(page)).toBeDisabled()
    release()
    await expect.poll(async () => (await sets()).length).toBe(1)
    expect(await sets()).toEqual([{ reads: reads + 1, yolo: false, closeToTray: false }])
    await expect.poll(savedReads).toBe(reads + 1)
    await expect(readsField(page)).toBeEnabled()
    await expect(page.locator('[data-tray-switch]')).toHaveAttribute('aria-checked', 'false')
    await expect(readsField(page)).toHaveValue(String(reads + 1))
    await expect(save).toBeDisabled()

    // Brakeman's part alone leaves the fields open, and what is typed while it is out stays (Codex's second round): the reads
    // changed back to what they were read as is an edit of its own, kept against the revision saved, for the next Save.
    held = false
    gate = new Promise<void>((done) => (release = done))
    await readsField(page).fill(String(reads + 2))
    await save.click()
    await expect.poll(() => held).toBe(true)
    await readsField(page).fill(String(reads + 1))
    release()
    await expect.poll(savedReads).toBe(reads + 2)
    await expect(save).toBeEnabled()
    await expect(readsField(page)).toHaveValue(String(reads + 1))
    expect(await sets()).toHaveLength(1)
    await page.unroute('**/api/brakeman/config')
    // The next Save sends it, at the revision saved: no stale refusal.
    await save.click()
    await expect.poll(savedReads).toBe(reads + 1)
    await expect(stale).toHaveCount(0)
    await expect(save).toBeDisabled()
  } finally {
    await page.unrouteAll({ behavior: 'ignoreErrors' })
    await putBack(api, start)
  }
})

test("in a browser, Brakeman's section is saved by the page's one Save", async ({ page, api }) => {
  const start = await brakemanConf(api)
  const reads = start.budget.readsPerTurn === 400 ? 500 : 400
  try {
    await page.goto('/settings')
    await expect(page.locator('[data-brakeman-enabled]')).toBeVisible()
    await brakemanAdvanced(page)
    const save = page.locator('[data-save-settings]')
    await expect(page.locator('[data-brakeman-save]')).toHaveCount(0)
    await expect(save).toBeDisabled()
    // A value Brakeman refuses holds the Save, and the bar says why.
    await readsField(page).fill('5')
    await expect(page.locator('[data-settings-blocked]')).toBeVisible()
    await expect(save).toBeDisabled()
    await readsField(page).fill(String(reads))
    await expect(save).toHaveText('Save')
    await save.click()
    await expect(page.getByText('Saved', { exact: true })).toBeVisible()
    await expect.poll(() => brakemanConf(api).then((c) => c.budget.readsPerTurn)).toBe(reads)
    await expect(save).toBeDisabled()
  } finally {
    await putBack(api, start)
  }
})

// Settings on a wide window and a phone (#433): a navigation beside one column of sections (#366: never two side by side), the
// whole as wide as the panel's body up to 80rem, each section no taller than the visible body at 1440 × 900; the section chosen in
// `?section=`; below lg a sticky row of links, nothing else scrolling sideways, and the save bar above the bottom bar. The
// stand-in bridge is the app's, as above; on win32 with an ICE status the WebRTC section and the WSL fields are laid out too.
type Platform = 'linux' | 'win32'
async function standIn(page: Page, state: E2EState, platform: Platform, server?: { state: string; lastError?: string }) {
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
    ({ token, form, url, platform, server }) => {
      const settings = { ...form }
      const w = window as unknown as { conductorDesktop: unknown; e2eSets: object[]; e2eRestarts: number }
      w.e2eSets = []
      w.e2eRestarts = 0
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
        restartServer: async () => {
          w.e2eRestarts++
        },
        openInBrowser: async () => {},
        showLog: async () => {},
        serverState: async () => ({ state: 'running', url, failures: 0, ...server }),
        notice: async () => '',
        ice: async () => ({ forwarding: true, port: 40001, publicIp: '203.0.113.7', wslAddress: '172.20.0.2', firewall: 'missing' }),
        allowIceFirewall: async () => 'present',
        versions: async () => ({ app: 'e2e', electron: '', node: '', chrome: '', server: '' }),
        onServerState: () => () => {},
      }
    },
    { token: state.token, form, url: state.baseURL, platform, server },
  )
}

/**
 * The sections this machine shows, in page order: what the navigation must list. The page opens on where and how agents work, and
 * on Windows the firewall is a row of Switchyard, no section of its own (UX3 R54).
 */
function expectedSections(_platform: Platform) {
  return ['folders', 'running', 'agents', 'accounts', 'switchyard', 'github', 'brakeman', 'app']
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
      // The sections are the column's own children: a row inside one may carry an address of its own (the Windows firewall's webrtc).
      sections: Array.from(b.querySelectorAll('[data-settings-sections] > [data-settings-section]')).map((e) => ({ id: e.getAttribute('data-settings-section')!, top: r(e).top, bottom: r(e).bottom, left: r(e).left, right: r(e).right, height: r(e).height })),
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
  // The app booted and drew the page first (on a loaded machine that alone can take most of an expect's bound), then what its
  // reads bring: each step its own wait.
  await expect(page.locator('[data-desktop-settings]')).toBeVisible()
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
      const roots = await boxOf(page.locator('[data-allowed-roots] ul'))
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
    // Eight loads, each waited on to settle (four waits of the expect's bound): the timeout bounds a hang, above their sum.
    test.setTimeout(8 * 5 * 15_000)
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
    // WebRTC from WSL, a section once, is the Windows firewall row of Switchyard: its address lands on the row, Switchyard marked.
    if (platform === 'win32') {
      await page.goto('/settings?section=webrtc')
      await settled(page, platform)
      const row = page.locator('[data-switchyard-settings] [data-settings-section="webrtc"][data-ice-status]')
      await expect.poll(async () => {
        const b = await boxOf(body(page))
        const r = await boxOf(row)
        return Math.abs(r.top - b.top - 24) <= 1 || (r.top >= b.top && r.bottom <= b.bottom)
      }).toBe(true)
      await expect(row).toBeInViewport()
      await expect(page.locator('[data-settings-nav-entry="switchyard"]')).toHaveAttribute('aria-current', 'true')
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
      const cwd = await boxOf(page.getByLabel('Default folder', { exact: true }))
      const pick = await boxOf(page.locator('[data-pick-dir="defaultCwd"]'))
      expect(pick.left).toBeGreaterThanOrEqual(cwd.right)
      expect(Math.abs(pick.top - cwd.top)).toBeLessThanOrEqual(4)
      // The navbar fits: its buttons, icons alone, inside the window.
      // The panel's navbar: the element before its body.
      const navbar = body(page).locator('xpath=preceding-sibling::*[1]')
      // The server's three buttons are The app's now (UX3 R57): the navbar holds Full screen alone.
      for (const b of ['[data-open-in-browser]', '[data-server-log]', '[data-restart-server]']) await expect(navbar.locator(b)).toHaveCount(0)
      const full = await boxOf(page.locator('[data-fullscreen]'))
      expect(full.right).toBeLessThanOrEqual(390)
      expect(full.width).toBeLessThanOrEqual(40)
      expect(await navbar.evaluate((e) => e.scrollWidth <= e.clientWidth)).toBe(true)
      await expect(page.locator('[data-about-card]').getByRole('button', { name: 'Restart server' })).toBeVisible()
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
      await expect(save).toHaveText('Save and restart')
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

/**
 * The save bar at rest (its animations ended): its box and right padding, Save's box, and the box of each note beside it (every
 * child but the button).
 */
async function saveBarOf(page: Page) {
  const bar = page.locator('[data-settings-save-bar]')
  await bar.evaluate((b) => Promise.all(b.getAnimations({ subtree: true }).map((a) => a.finished)).then(() => undefined))
  return bar.evaluate((b) => {
    const r = (e: Element) => JSON.parse(JSON.stringify(e.getBoundingClientRect())) as DOMRect
    const save = b.querySelector('[data-save-settings]')!
    return { bar: r(b), padLeft: parseFloat(getComputedStyle(b).paddingLeft), padRight: parseFloat(getComputedStyle(b).paddingRight), save: r(save), notes: Array.from(b.children).filter((c) => c !== save).map(r) }
  })
}

/** Save at the bar's right end, within its padding, and every note wholly left of it and inside the bar's padding. */
function saveAtTheRight(m: Awaited<ReturnType<typeof saveBarOf>>, notes: number) {
  expect(Math.abs(m.bar.right - m.padRight - m.save.right), `Save's right edge (${m.save.right}) at the bar's (${m.bar.right}, padding ${m.padRight})`).toBeLessThanOrEqual(1)
  expect(m.notes).toHaveLength(notes)
  for (const n of m.notes) {
    expect(n.right, 'the note ends left of Save').toBeLessThanOrEqual(m.save.left)
    expect(n.left, "the note starts inside the bar's padding").toBeGreaterThanOrEqual(m.bar.left + m.padLeft - 1)
  }
}

// The save bar (#1154): Save at its right end, where a form's action is looked for, and the bar's note (the restart sentence) at its
// left, in the room left and wrapping there, so that on a phone the button stays whole in the window.
for (const at of [{ name: 'a wide window', use: { viewport: { width: 1440, height: 900 } } }, { name: 'a phone', use: { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true } }]) {
  test.describe(`on ${at.name}`, () => {
    test.use(at.use)

    test("Save sits at the save bar's right end, and its note to its left", async ({ page, state }) => {
      await standIn(page, state, 'linux')
      await page.goto('/settings?section=running')
      await settled(page, 'linux')
      const save = page.locator('[data-save-settings]')
      // No words: the button at the right end all the same, the bar's empty words left of it.
      await expect(save).toHaveText('Save')
      await expect(page.locator('[data-settings-discard]')).toHaveCount(0)
      saveAtTheRight(await saveBarOf(page), 1)
      // What changed and what a save costs, left of Discard, and Discard left of Save: whole, the button whole in the window.
      await page.locator('[data-yolo-switch]').click()
      await expect(save).toHaveText('Save and restart')
      await expect(page.locator('[data-settings-changed]')).toHaveText('Changed: How agents run. Saving restarts the server.')
      await expect(page.locator('[data-settings-save-bar]')).not.toContainText('running sessions end')
      const m = await saveBarOf(page)
      saveAtTheRight(m, 2)
      expect(m.notes[1]!.right, 'Discard ends where Save begins, less the gap').toBeLessThanOrEqual(m.save.left)
      expect(m.notes[1]!.left, 'Discard is right of the words').toBeGreaterThanOrEqual(m.notes[0]!.right - 1)
      await uncut(save)
      await uncut(page.locator('[data-settings-discard]'))
      await uncut(page.locator('[data-settings-changed]'))
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
  // Saved from the top of a page whose address names another section, the address names none, so the page that follows opens
  // at the top, where the person was (#433's review).
  const sections = await boxOf(page.locator('[data-settings-sections]'))
  await page.mouse.move(sections.left + sections.width / 2, 300)
  await page.mouse.wheel(0, -100000)
  await expect.poll(async () => (await layoutOf(page)).body.scrollTop).toBe(0)
  await expect(page.locator('[data-settings-nav-entry="folders"]')).toHaveAttribute('aria-current', 'true')
  // Yolo, in view at the top of the page, back as it was: a change that restarts all the same.
  await page.locator('[data-yolo-switch]').click()
  expect((await layoutOf(page)).body.scrollTop).toBe(0)
  await expect(page.locator('[data-save-settings]')).toHaveText('Save and restart')
  await page.locator('[data-save-settings]').click()
  await expect(page.getByText('Saved; the server restarted', { exact: true }).last()).toBeVisible()
  await expect(page).toHaveURL(/\/settings$/)
  expect((await layoutOf(page)).body.scrollTop).toBe(0)
  // A save that does not restart leaves the address as it is.
  await page.goto('/settings')
  await settled(page, 'linux')
  await page.locator('[data-tray-switch]').click()
  await page.locator('[data-save-settings]').click()
  await expect(page.getByText('Saved', { exact: true })).toBeVisible()
  await expect(page).toHaveURL(/\/settings$/)
})

test('a second click on Save while the first is under way saves once', async ({ page, state }) => {
  await standIn(page, state, 'linux')
  await page.goto('/settings?section=running')
  await settled(page, 'linux')
  await page.locator('[data-yolo-switch]').click()
  await expect(page.locator('[data-save-settings]')).toHaveText('Save and restart')
  // Scrolled by hand from the section the address names to the end, so the save writes the section in view first and has that to
  // wait on.
  const sections = await boxOf(page.locator('[data-settings-sections]'))
  await page.mouse.move(sections.left + sections.width / 2, 300)
  await page.mouse.wheel(0, 100000)
  await expect(page.locator('[data-settings-nav-entry="app"]')).toHaveAttribute('aria-current', 'true')
  // Two clicks in one task: the second lands before anything the first awaits has answered, and before Vue redraws the button.
  await page.locator('[data-save-settings]').evaluate((e: HTMLElement) => {
    e.click()
    e.click()
  })
  await expect(page.getByText('Saved; the server restarted', { exact: true })).toBeVisible()
  await expect(page).toHaveURL(/\/settings\?section=app$/)
  expect(await page.evaluate(() => (window as unknown as { e2eSets: object[] }).e2eSets.length)).toBe(1)
})

test('a scroll by hand after a choice moves the mark to the section in view', async ({ page, state }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await standIn(page, state, 'linux')
  await page.goto('/settings')
  await settled(page, 'linux')
  await page.locator('[data-settings-nav-entry="folders"]').click()
  await expect(page).toHaveURL(/\/settings\?section=folders$/)
  await expect(page.locator('[data-settings-nav-entry="folders"]')).toHaveAttribute('aria-current', 'true')
  // The wheel over the sections, until all but the foot of Where agents work has gone above the body's top: How agents run is the
  // section in view though the one chosen still shows.
  const start = (await layoutOf(page)).body.scrollTop
  const folders = await boxOf(page.locator('[data-settings-section="folders"]'))
  const sections = await boxOf(page.locator('[data-settings-sections]'))
  await page.mouse.move(sections.left + sections.width / 2, 450)
  const dy = Math.round(folders.height - 30)
  await page.mouse.wheel(0, dy)
  await expect.poll(async () => Math.abs((await layoutOf(page)).body.scrollTop - (start + dy)) <= 1).toBe(true)
  const l = await layoutOf(page)
  const f = l.sections.find((s) => s.id === 'folders')!
  expect(f.bottom).toBeGreaterThan(l.body.top)
  await expect(page.locator('[data-settings-nav-entry="running"]')).toHaveAttribute('aria-current', 'true')
  await expect(page.locator('[data-settings-nav-entry][aria-current]')).toHaveCount(1)
})

test('a window made shorter moves the mark to the section in view, and a restart save writes that one', async ({ page, state }) => {
  // A tall window, Yolo changed at the top, then the body scrolled by hand to its end: The app, the last section, is marked.
  await page.setViewportSize({ width: 1440, height: 1400 })
  await standIn(page, state, 'linux')
  await page.goto('/settings')
  await settled(page, 'linux')
  await page.locator('[data-yolo-switch]').click()
  await expect(page.locator('[data-save-settings]')).toHaveText('Save and restart')
  const sections = await boxOf(page.locator('[data-settings-sections]'))
  await page.mouse.move(sections.left + sections.width / 2, 450)
  await page.mouse.wheel(0, 100000)
  await expect.poll(async () => body(page).evaluate((b) => b.scrollTop > 0 && b.scrollTop + b.clientHeight >= b.scrollHeight - 1)).toBe(true)
  await expect(page.locator('[data-settings-nav-entry="app"]')).toHaveAttribute('aria-current', 'true')
  // Only the height changes: the sections keep their size and the body its scroll, so no scroll event and no change to the
  // sections says the body is shorter (#433's review). The section a quarter of the way down the visible body is the one marked.
  const before = (await layoutOf(page)).body.scrollTop
  await page.setViewportSize({ width: 1440, height: 700 })
  await expect.poll(async () => (await layoutOf(page)).body.clientHeight).toBeLessThan(700)
  const l = await layoutOf(page)
  expect(l.body.scrollTop).toBe(before)
  const line = l.body.top + l.body.clientHeight / 4
  const expected = l.sections.filter((s) => s.top <= line).at(-1)!.id
  expect(expected).not.toBe('app')
  await expect(page.locator(`[data-settings-nav-entry="${expected}"]`)).toHaveAttribute('aria-current', 'true')
  await expect(page.locator('[data-settings-nav-entry][aria-current]')).toHaveCount(1)
  // The save bar is outside the body, so the click scrolls nothing: the restart's address names the section in view.
  await page.locator('[data-save-settings]').click()
  await expect(page.getByText('Saved; the server restarted', { exact: true })).toBeVisible()
  await expect(page).toHaveURL(new RegExp(`/settings\\?section=${expected}$`))
})

test('a restart save made while a scroll\'s mark is still to come writes the section in view, not the one marked before', async ({ page, state }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await standIn(page, state, 'linux')
  await page.goto('/settings?section=folders')
  await settled(page, 'linux')
  await expect(page.locator('[data-settings-nav-entry="folders"]')).toHaveAttribute('aria-current', 'true')
  // Yolo changed (the click is the person's own, so the mark follows the scroll alone from here), then Where agents work brought
  // back to the top of the body and marked.
  await page.locator('[data-yolo-switch]').click()
  await expect(page.locator('[data-save-settings]')).toHaveText('Save and restart')
  await body(page).evaluate((b) => {
    const s = b.querySelector('[data-settings-section="folders"]')!
    b.scrollTop += s.getBoundingClientRect().top - b.getBoundingClientRect().top - 24
  })
  await expect(page.locator('[data-settings-nav-entry="folders"]')).toHaveAttribute('aria-current', 'true')
  await expect(page).toHaveURL(/\/settings\?section=folders$/)
  // The body scrolled to How agents run with the page's frames held, so the scroll's mark is still to come when Save is pressed
  // (#433's review): the save reads the section in view itself.
  const held = await body(page).evaluate(async (b) => {
    const w = window as unknown as { requestAnimationFrame: typeof requestAnimationFrame }
    const raf = w.requestAnimationFrame
    const frames: FrameRequestCallback[] = []
    w.requestAnimationFrame = (cb) => {
      frames.push(cb)
      return frames.length
    }
    try {
      const scrolled = new Promise((resolve) => b.addEventListener('scroll', resolve, { once: true }))
      const s = b.querySelector('[data-settings-section="running"]')!
      b.scrollTop += s.getBoundingClientRect().top - b.getBoundingClientRect().top - 24
      await scrolled
      const marked = document.querySelector('[data-settings-nav-entry][aria-current]')?.getAttribute('data-settings-nav-entry')
      const pending = frames.length > 0
      document.querySelector<HTMLElement>('[data-save-settings]')!.click()
      return { marked, pending }
    } finally {
      w.requestAnimationFrame = raf
      for (const cb of frames) raf(cb)
    }
  })
  expect(held).toEqual({ marked: 'folders', pending: true })
  await expect(page.getByText('Saved; the server restarted', { exact: true })).toBeVisible()
  await expect(page).toHaveURL(/\/settings\?section=running$/)
  await expect(page.locator('[data-settings-nav-entry="running"]')).toHaveAttribute('aria-current', 'true')
})

test('Settings chosen again in the same page goes back to the top, from a section or from a scroll by hand', async ({ page, state }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await standIn(page, state, 'linux')
  await page.goto('/settings')
  await settled(page, 'linux')
  // The sidebar's Settings is under its More tile.
  const settingsLink = { click: () => viaMore(page, 'Settings') }
  const atTop = async () => {
    await expect.poll(async () => (await layoutOf(page)).body.scrollTop).toBe(0)
    await expect(page.locator('[data-settings-nav-entry="folders"]')).toHaveAttribute('aria-current', 'true')
    await expect(page).toHaveURL(/\/settings$/)
  }
  // From a section the address names: the sidebar's Settings is a new address, plain /settings.
  await page.locator('[data-settings-nav-entry="app"]').click()
  await expect(page).toHaveURL(/\/settings\?section=app$/)
  await expect(page.locator('[data-settings-nav-entry="app"]')).toHaveAttribute('aria-current', 'true')
  await settingsLink.click()
  await atTop()
  // From a scroll by hand on plain /settings: the same address again, which Vue Router refuses as a duplicate.
  const sections = await boxOf(page.locator('[data-settings-sections]'))
  await page.mouse.move(sections.left + sections.width / 2, 450)
  await page.mouse.wheel(0, 1500)
  await expect.poll(async () => (await layoutOf(page)).body.scrollTop).toBeGreaterThan(1000)
  await expect(page.locator('[data-settings-nav-entry="folders"]')).not.toHaveAttribute('aria-current', 'true')
  await settingsLink.click()
  await atTop()
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

// The page opens on what people come for (UX3 R54): the folders agents may use and the Yolo switch are in view as it opens, in the
// wide short window people use all day and at 1440, and Switchyard, with Reach and the Windows firewall, sits below Accounts.
for (const size of [{ width: 2000, height: 810 }, { width: 1440, height: 810 }, { width: 1440, height: 900 }]) {
  test(`at ${size.width} × ${size.height} Settings opens on the folders and the Yolo switch`, async ({ page, state }) => {
    await page.setViewportSize(size)
    await standIn(page, state, 'win32')
    await page.goto('/settings')
    await settled(page, 'win32')
    expect((await layoutOf(page)).body.scrollTop).toBe(0)
    await expect(page.locator('[data-pick-dir="root"]')).toBeInViewport()
    await expect(page.locator('[data-yolo-switch]')).toBeInViewport()
    await expect(page.locator('[data-settings-nav-entry="folders"]')).toHaveAttribute('aria-current', 'true')
    // On Windows the Windows folders sit with the folders, and Reach and the firewall are Switchyard's.
    await expect(page.locator('[data-settings-section="folders"] [data-wsl-windows-home]')).toBeVisible()
    const yard = page.locator('[data-switchyard-settings]')
    await expect(yard.getByText('Reach from outside your network', { exact: true })).toBeVisible()
    await expect(yard.locator('[data-ice-firewall]')).toHaveText('rule missing')
    await expect(page.locator('[data-settings-section="running"]')).not.toContainText('Reach')
    // The switchyard's statement keeps every fact it stated.
    for (const fact of ["each session's name, agent and folder name, this machine's name, and whether it waits on you", 'never its chat, activity or command line', 'It mints and checks the links and decides who joins', 'through its relay, which can read it', 'Publish only to a switchyard whose operator you trust']) await expect(yard).toContainText(fact)
    await expect(page.getByText('WebRTC from WSL')).toHaveCount(0)
    // Allow adds the rule: the badge says so, and the button goes.
    await yard.locator('[data-ice-allow]').click()
    await expect(yard.locator('[data-ice-firewall]')).toHaveText('rule present')
    await expect(yard.locator('[data-ice-allow]')).toHaveCount(0)
  })
}

// Settings' agents are drawn as the Agents page draws them (UX3 R54): one not installed asks nothing, so it is neither amber nor green.
test("Settings' agents: not installed and an identified agent's badge are neither amber nor green", async ({ page, state }) => {
  await standIn(page, state, 'linux')
  await page.goto('/settings?section=agents')
  await settled(page, 'linux')
  const card = page.locator('[data-settings-section="agents"]')
  const colour = (l: Locator) => l.evaluate((e) => getComputedStyle(e).color)
  const probe = (v: string) =>
    page.evaluate((v) => {
      const e = document.createElement('span')
      e.style.color = v
      document.body.append(e)
      const c = getComputedStyle(e).color
      e.remove()
      return c
    }, v)
  const amber = [await probe('var(--ui-warning)'), await probe('var(--ui-warning-text)')]
  const green = [await probe('var(--ui-success)'), await probe('var(--ui-success-text)')]
  const badges = card.locator('[data-not-installed], [data-identity="ok"]')
  await expect(badges.first()).toBeVisible()
  for (const b of await badges.all()) {
    expect(amber).not.toContain(await colour(b))
    expect(green).not.toContain(await colour(b))
  }
})

// The folders agents can use are a list with Add folder, checked as they are picked (UX3 R55): a chip a folder, the last one kept, a
// Default folder outside every folder said under its field and holding Save, a data folder inside one said and not holding it.
test('the folders agents can use: Add folder makes a chip, a chip goes, the last stays, and the folder fields say what is wrong at once', async ({ page, state }) => {
  test.setTimeout(20 * 15_000)
  await page.setViewportSize({ width: 1440, height: 900 })
  await standIn(page, state, 'linux')
  await page.goto('/settings')
  await settled(page, 'linux')
  const save = page.locator('[data-save-settings]')
  await expect(save).toBeDisabled()
  await expect(page.getByText('Allowed roots')).toHaveCount(0)
  // Add folder, through the picker: a chip, and Save offered.
  const own = pickerFolder(state.root, 'settings-roots')
  await addFolder(page, own.pick)
  await expect(save).toBeEnabled()
  // A chip's remove takes it out, and the page is as saved again.
  await page.locator(`[data-root-chip="${own.pick}"]`).getByRole('button', { name: `Remove ${own.pick}` }).click()
  await expect(page.locator(`[data-root-chip="${own.pick}"]`)).toHaveCount(0)
  await expect(save).toBeDisabled()
  // The last folder cannot be taken away.
  const last = page.locator(`[data-root-chip="${state.root}"]`)
  await expect(last.locator('[data-root-remove]')).toBeDisabled()
  await expect(last.locator('[title="Agents need at least one folder."]')).toHaveCount(1)
  // A Default folder outside every folder agents can use: said under the field, Save held.
  const cwd = page.getByLabel('Default folder', { exact: true })
  await cwd.fill('/srv/elsewhere')
  const field = page.locator('[data-default-folder]')
  await expect(field).toContainText('Pick a folder inside one that agents can use.')
  await expect(save).toBeDisabled()
  await expect(page.locator('[data-settings-blocked]')).toBeVisible()
  await cwd.fill(state.root)
  await expect(field).not.toContainText('Pick a folder inside one that agents can use.')
  // A data folder inside a folder agents can use: said, Save not held.
  const data = page.getByLabel('Where Conductor keeps its data', { exact: true })
  await data.fill(join(state.root, 'data-here'))
  await expect(page.locator('[data-data-folder]')).toContainText('Keep this outside the folders agents can use.')
  await expect(save).toBeEnabled()
  await data.fill(state.data)
  await expect(save).toBeDisabled()
  // A long path wraps inside its chip and keeps its end in view, its whole form in its title; nothing scrolls sideways.
  const deep = join(state.root, 'settings-roots', 'a-folder-with-a-rather-long-name-that-goes-on', 'and-on-and-on-past-the-row', 'the-end-of-it')
  mkdirSync(deep, { recursive: true })
  await addFolder(page, deep)
  const chip = page.locator(`[data-root-chip="${deep}"] [data-mono="path"]`)
  await expect(chip).toHaveAttribute('title', deep)
  await uncut(chip)
  expect(await body(page).evaluate((b) => b.scrollWidth <= b.clientWidth)).toBe(true)
})

// The save bar names what changed and offers Discard (UX3 R56): Discard puts back the app's fields and Brakeman's, and a refused
// save's words sit in the bar beside Save, in view, the edits kept.
test("Discard puts back the app's fields and Brakeman's; a refused save says why in the bar", async ({ page, state, api }) => {
  test.setTimeout(20 * 15_000)
  const start = await brakemanConf(api)
  const reads = start.budget.readsPerTurn === 400 ? 500 : 400
  const { sets } = await oneSaveStandIn(page, state)
  try {
    await page.goto('/settings')
    await settled(page, 'linux')
    await brakemanAdvanced(page)
    const save = page.locator('[data-save-settings]')
    const words = page.locator('[data-settings-changed]')
    await page.locator('[data-yolo-switch]').click()
    await page.locator('[data-tray-switch]').click()
    await readsField(page).fill(String(reads))
    await expect(words).toHaveText('Changed: How agents run, Brakeman, The app. Saving restarts the server.')
    await page.locator('[data-settings-discard]').click()
    await expect(page.locator('[data-yolo-switch]')).toHaveAttribute('aria-checked', 'false')
    await expect(page.locator('[data-tray-switch]')).toHaveAttribute('aria-checked', 'true')
    await expect(readsField(page)).toHaveValue(String(start.budget.readsPerTurn))
    await expect(save).toBeDisabled()
    await expect(page.locator('[data-settings-discard]')).toHaveCount(0)
    expect(await sets()).toEqual([])
    // A save the app refuses: its words in the bar, in the error tone and in view, beside Save; the edit kept for the next Save.
    await page.evaluate(() => sessionStorage.setItem('e2e.fail', 'refuse'))
    await page.locator('[data-tray-switch]').click()
    await save.click()
    const error = page.locator('[data-settings-save-bar] [data-settings-error]')
    await expect(error).toHaveText('the settings were refused')
    await expect(error).toBeInViewport()
    await expect(error).toHaveAttribute('role', 'alert')
    await expect(page.locator('[data-tray-switch]')).toHaveAttribute('aria-checked', 'false')
    await expect(page.locator('[data-settings-sections] [data-settings-error]')).toHaveCount(0)
  } finally {
    await putBack(api, start)
  }
})

// A save that restarts the server while agents run asks first, in the bar (UX3 R56): Keep editing keeps every edit, Escape too, a double
// click does not pass the question, and Save and restart saves. The session is the test's own; none other runs (the beforeEach).
for (const size of [{ width: 2000, height: 810 }, { width: 1440, height: 810 }]) {
  test(`at ${size.width} × ${size.height} Save and restart asks before a running agent stops`, async ({ page, state, api }) => {
    test.setTimeout(20 * 15_000)
    await page.setViewportSize(size)
    const s = await api.ok<{ id: string }>('POST', '/api/sessions', { agentId: 'shell', name: 'settings restart question', cwd: state.root })
    try {
      await standIn(page, state, 'linux')
      await page.goto('/settings')
      await settled(page, 'linux')
      const bar = page.locator('[data-settings-save-bar]')
      const save = page.locator('[data-save-settings]')
      await page.locator('[data-yolo-switch]').click()
      await expect(save).toHaveText('Save and restart')
      // The session is on the page's list (its sidebar row) before the question can count it.
      await expect(page.locator(`[data-session-list="sidebar"] [data-sidebar-row="s:${s.id}"]`).first()).toBeAttached()
      await save.click()
      await expect(bar.locator('[data-settings-question]')).toHaveText('Restart the server now? 1 running agent stops.')
      await expect(bar.locator('[data-settings-keep-editing]')).toBeFocused()
      await bar.locator('[data-settings-keep-editing]').click()
      await expect(bar.locator('[data-settings-question]')).toHaveCount(0)
      await expect(page.locator('[data-yolo-switch]')).toHaveAttribute('aria-checked', 'true')
      // Escape keeps editing too.
      await save.click()
      await expect(bar.locator('[data-settings-keep-editing]')).toBeFocused()
      await page.keyboard.press('Escape')
      await expect(bar.locator('[data-settings-question]')).toHaveCount(0)
      // A double click on Save asks, and its second click does not pass the question.
      await save.dblclick()
      await expect(bar.locator('[data-settings-question]')).toBeVisible()
      expect(await page.evaluate(() => (window as unknown as { e2eSets: object[] }).e2eSets.length)).toBe(0)
      // Save and restart saves.
      await bar.locator('[data-settings-confirm]').click()
      await expect(page.getByText('Saved; the server restarted', { exact: true })).toBeVisible()
      expect(await page.evaluate(() => (window as unknown as { e2eSets: Array<{ yolo?: boolean }> }).e2eSets.map((x) => x.yolo))).toEqual([true])
    } finally {
      await api.stopSession(s.id)
    }
  })
}

// The server's state and its three buttons sit in The app (UX3 R57): no banner while it runs, one line at the top while it does not,
// and Restart server asks first while agents run, restarting at once with none.
test("The app holds Conductor's server and its buttons; Restart server asks only while agents run", async ({ page, state, api }) => {
  test.setTimeout(20 * 15_000)
  await standIn(page, state, 'linux')
  await page.goto('/settings?section=app')
  await settled(page, 'linux')
  const about = page.locator('[data-about-card]')
  const row = about.locator('[data-server-row]')
  await expect(page.locator('[data-server-state]')).toHaveCount(0)
  await expect(about).toContainText("Versions, the tray and Conductor's server.")
  await expect(row).toContainText("Conductor's server · Running at")
  await expect(row.locator('[data-server-url]')).toHaveText(state.baseURL)
  for (const b of ['[data-server-log]', '[data-open-in-browser]', '[data-restart-server]']) await expect(row.locator(b)).toBeVisible()
  // None running: Restart server restarts at once.
  const restarts = () => page.evaluate(() => (window as unknown as { e2eRestarts: number }).e2eRestarts)
  await row.locator('[data-restart-server]').click()
  await expect.poll(restarts).toBe(1)
  await expect(row.locator('[data-restart-question]')).toHaveCount(0)
  // One of the test's own running: it asks, Keep it running keeps it, and Restart server restarts.
  const s = await api.ok<{ id: string }>('POST', '/api/sessions', { agentId: 'shell', name: 'settings restart server', cwd: state.root })
  try {
    await expect(page.locator(`[data-session-list="sidebar"] [data-sidebar-row="s:${s.id}"]`).first()).toBeAttached()
    await row.locator('[data-restart-server]').click()
    await expect(row.locator('[data-restart-question]')).toHaveText('Restart the server? 1 running agent stops.')
    await expect(row.locator('[data-restart-keep]')).toBeFocused()
    await expect(row.locator('[data-restart-keep]')).toHaveText('Keep it running')
    await row.locator('[data-restart-keep]').click()
    await expect(row.locator('[data-restart-question]')).toHaveCount(0)
    expect(await restarts()).toBe(1)
    await row.locator('[data-restart-server]').click()
    await row.locator('[data-restart-confirm]').click()
    await expect.poll(restarts).toBe(2)
  } finally {
    await api.stopSession(s.id)
  }
})

test("a server that stopped is said in one line at the top, with its log and a restart", async ({ page, state }) => {
  await standIn(page, state, 'linux', { state: 'failed', lastError: 'the server exited with status 1' })
  await page.goto('/settings')
  await settled(page, 'linux')
  const line = page.locator('[data-server-state="failed"]')
  await expect(line).toContainText("Conductor's server stopped: the server exited with status 1.")
  await expect(line.locator('[data-server-state-log]')).toBeVisible()
  await line.locator('[data-server-state-restart]').click()
  await expect.poll(() => page.evaluate(() => (window as unknown as { e2eRestarts: number }).e2eRestarts)).toBe(1)
  await expect(page.locator('[data-about-card] [data-server-word]')).toHaveText('Stopped')
})

// Accounts and GitHub (UX3 R58): what the grey Add account needs is said beside it; a rename's Cancel comes before its Save and a
// remove question's Keep it before its Remove; GitHub's Check and save sits at its foot's right end. The account is the test's own.
test('Accounts says what Add account needs, puts Cancel and Keep it first, and GitHub its Check and save at the right', async ({ page, api, state }) => {
  test.setTimeout(20 * 15_000)
  const dir = mkdtempSync(join(state.home, 'e2e-settings-account-'))
  const { rev } = await api.ok<{ rev: number }>('GET', '/api/accounts')
  const label = `e2e settings ${Date.now()}`
  const made = await api.ok<{ account: { id: string } }>('POST', '/api/accounts', { adapter: 'claude', label, folder: dir, expectedRev: rev })
  try {
    await page.goto('/settings')
    const card = page.locator('[data-accounts-card]')
    await expect(card).toContainText('The logins your agents use. Each change saves at once.')
    const add = card.locator('[data-account-add]')
    await expect(add.locator('[data-account-add-submit]')).toBeDisabled()
    await expect(add.locator('[data-account-add-needs]')).toHaveText('Needs a label and a folder.')
    await add.locator('[data-account-add-label]').fill('Work')
    const folderInput = add.locator('[data-account-add-folder] input[role="combobox"]')
    await folderInput.fill(dir)
    await folderInput.press('Escape')
    await expect(add.locator('[data-account-add-needs]')).toHaveText('Saves at once.')
    await expect(add.locator('[data-account-add-submit]')).toBeEnabled()
    await add.locator('[data-account-add-label]').fill('')
    // The order of a rename's and a remove question's buttons: the one that changes nothing first.
    const row = card.locator(`[data-account-row="${made.account.id}"]`)
    await row.locator('[data-account-rename]').click()
    const renameButtons = await row.getByRole('button').evaluateAll((bs) => bs.map((b) => b.textContent?.trim()))
    expect(renameButtons.indexOf('Cancel')).toBeLessThan(renameButtons.indexOf('Save'))
    await row.locator('[data-account-rename-cancel]').click()
    await row.locator('[data-account-remove]').click()
    const removeButtons = await row.getByRole('button').evaluateAll((bs) => bs.map((b) => b.textContent?.trim()))
    expect(removeButtons.indexOf('Keep it')).toBeLessThan(removeButtons.indexOf('Remove'))
    await row.locator('[data-account-remove-keep]').click()
    // The default badge shows where an agent has two logins to choose from.
    await expect(card.locator('[data-accounts-adapter="claude"] [data-account-is-default]')).toHaveCount(1)
    // GitHub: Check and save at the right end of its foot, the note left of it.
    const foot = page.locator('[data-settings-github] [data-github-foot]')
    await foot.scrollIntoViewIfNeeded()
    const f = await boxOf(foot)
    const b = await boxOf(foot.locator('[data-github-save]'))
    expect(Math.abs(f.right - b.right)).toBeLessThanOrEqual(1)
    await expect(page.locator('[data-settings-github]')).toContainText('Saved by its own button.')
  } finally {
    const d = await api.ok<{ rev: number }>('GET', '/api/accounts')
    await api.call('DELETE', `/api/accounts/${made.account.id}?expectedRev=${d.rev}`)
    rmSync(dir, { recursive: true, force: true })
  }
})

// Edits not saved stay while another page is open (UX3 R81): back on Settings the field and the bar show them; a setting saved
// meanwhile wins; a reload drops them.
test('Settings keeps an edit not saved across a visit to another page, a setting saved meanwhile wins, and a reload drops it', async ({ page, state }) => {
  test.setTimeout(20 * 15_000)
  await standIn(page, state, 'linux')
  await page.goto('/settings')
  await settled(page, 'linux')
  await page.locator('[data-yolo-switch]').click()
  const mine = join(state.root, 'settings-draft-mine')
  const theirs = join(state.root, 'settings-draft-theirs')
  await page.getByLabel('Default folder', { exact: true }).fill(mine)
  await expect(page.locator('[data-settings-changed]')).toHaveText('Changed: Where agents work, How agents run. Saving restarts the server.')
  // Another page, in the same window.
  await page.locator('a[href="/yard"]').first().click()
  await expect(page).toHaveURL(/\/yard/)
  // Meanwhile the default folder is saved elsewhere (the app's settings change under the page): that one wins.
  await page.evaluate((theirs) => {
    const w = window as unknown as { conductorDesktop: { settings: { set: (p: object) => Promise<unknown> } }; e2eSets: object[] }
    return w.conductorDesktop.settings.set({ defaultCwd: theirs }).then(() => w.e2eSets.pop())
  }, theirs)
  await viaMore(page, 'Settings')
  await expect(page).toHaveURL(/\/settings/)
  await settled(page, 'linux')
  await expect(page.locator('[data-yolo-switch]')).toHaveAttribute('aria-checked', 'true')
  await expect(page.getByLabel('Default folder', { exact: true })).toHaveValue(theirs)
  await expect(page.locator('[data-settings-changed]')).toHaveText('Changed: How agents run. Saving restarts the server.')
  // A reload drops what was not saved.
  await page.reload()
  await settled(page, 'linux')
  await expect(page.locator('[data-yolo-switch]')).toHaveAttribute('aria-checked', 'false')
  await expect(page.locator('[data-save-settings]')).toBeDisabled()
})
