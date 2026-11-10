import type { Page } from '@playwright/test'
import { expect, test, type Session } from './fixtures'

// The update the desktop app downloaded (#290): never a dialog of the app's
// own, which every check that found the same download used to open again.
// The workbench is told once per version, in a toast that offers the
// restart, and keeps a button in the sidebar's header until it installs;
// the restart says what it ends before the app quits to install; an update
// the app no longer holds restarts nothing; an update that did not install is
// said once. The app is a stub bridge here, which hands the announcement out
// once, refuses to install a version it does not hold and puts a restart off
// while it checks for updates, as the app does.
test.describe.configure({ mode: 'serial' })

type Told = { ready: string; announce: boolean; failed: string; restart?: string; checking?: boolean }
type Invited = { e2eInvite?: (invite: unknown) => Promise<boolean>; e2eTaken?: boolean }

async function stubApp(page: Page, token: string, url: string, init: Told) {
  await page.addInitScript(
    ({ token, url, init }) => {
      const w = window as unknown as { conductorDesktop: unknown; e2eUpdate: Told; e2eFound?: () => void } & Invited
      w.e2eUpdate = JSON.parse(sessionStorage.getItem('e2e.update') ?? JSON.stringify(init))
      w.conductorDesktop = {
        version: 'e2e',
        platform: 'linux',
        token: async () => token,
        openExternal: async () => {},
        settings: { get: async () => ({}), set: async (p: object) => p },
        restartServer: async () => {},
        openInBrowser: async () => {},
        showLog: async () => sessionStorage.setItem('e2e.log', '1'),
        serverState: async () => ({ state: 'running', url, failures: 0 }),
        notice: async () => '',
        ice: async () => ({ forwarding: false, port: 0, publicIp: '', wslAddress: '', firewall: 'unknown' }),
        allowIceFirewall: async () => 'unknown',
        versions: async () => ({ app: 'e2e', electron: '', node: '', chrome: '', server: '' }),
        onServerState: () => () => {},
        // The app's UpdateNotice: the announcement, the tray's restart and a failed install go to the first page that asks, once.
        update: async () => {
          const told = { ...w.e2eUpdate }
          w.e2eUpdate = { ...w.e2eUpdate, announce: false, restart: '', failed: '' }
          sessionStorage.setItem('e2e.update', JSON.stringify(w.e2eUpdate))
          return told
        },
        onUpdate: (cb: () => void) => {
          w.e2eFound = cb
          return () => {}
        },
        onInvite: (cb: (invite: unknown) => Promise<boolean>) => {
          w.e2eInvite = cb
          return () => {}
        },
        installUpdate: async (version: string) => {
          if (!version || version !== w.e2eUpdate.ready) return 'gone'
          if (w.e2eUpdate.checking) return 'busy'
          sessionStorage.setItem('e2e.installed', String(Number(sessionStorage.getItem('e2e.installed') ?? '0') + 1))
          return 'started'
        },
      }
    },
    { token, url, init },
  )
}

/** The app's check finds an update downloaded, or lets it go (ready ''): told what is ready now, it says so to the page. */
async function found(page: Page, told: Told) {
  await page.evaluate((told) => {
    const w = window as unknown as { e2eUpdate: Told; e2eFound?: () => void }
    w.e2eUpdate = told
    sessionStorage.setItem('e2e.update', JSON.stringify(told))
    w.e2eFound?.()
  }, told)
}

const READY = 'Conductor 0.7.0-rc.12 is ready'

test('an update found is said once, in a toast, and kept in the header until it installs', async ({ page, state }) => {
  await stubApp(page, state.token, state.baseURL, { ready: '', announce: false, failed: '' })
  await page.goto('/yard')
  await expect(page.locator('[data-sidebar-menus]').first()).toBeVisible()
  await expect(page.locator('[data-header-update]')).toHaveCount(0)

  await found(page, { ready: '0.7.0-rc.12', announce: true, failed: '' })
  const toast = page.locator('li').filter({ hasText: READY })
  await expect(toast).toHaveCount(1)
  await expect(toast).toContainText('It installs when you quit Conductor')
  await expect(toast.getByRole('button', { name: 'Restart to update' })).toBeVisible()
  await expect(page.locator('[data-header-update]').first()).toBeVisible()

  // The checks that find the same download again, six hours apart, say nothing more.
  await found(page, { ready: '0.7.0-rc.12', announce: false, failed: '' })
  await found(page, { ready: '0.7.0-rc.12', announce: false, failed: '' })
  await page.waitForTimeout(500)
  await expect(page.locator('li').filter({ hasText: READY })).toHaveCount(1)

  // A page loaded later is told the update is ready, not said again.
  await page.reload()
  await expect(page.locator('[data-header-update]').first()).toBeVisible()
  await page.waitForTimeout(500)
  await expect(page.locator('li').filter({ hasText: READY })).toHaveCount(0)
})

test('restart to update says what it ends, Later leaves it, and the restart installs', async ({ page, state }) => {
  await stubApp(page, state.token, state.baseURL, { ready: '0.7.0-rc.12', announce: false, failed: '' })
  await page.goto('/yard')
  await page.locator('[data-header-update]').first().click()
  const modal = page.getByRole('dialog')
  await expect(modal).toContainText('Restart to update to 0.7.0-rc.12?')
  await expect(modal).toContainText('Conductor quits to install 0.7.0-rc.12, and its server stops.')
  await expect(modal).toContainText(' on this computer')
  await modal.getByRole('button', { name: 'Later' }).click()
  await expect(modal).toHaveCount(0)
  expect(await page.evaluate(() => sessionStorage.getItem('e2e.installed'))).toBeNull()

  // Put off while the app checks for updates, which may change the one it holds: the dialog stays, and nothing installs.
  await page.evaluate(() => {
    const w = window as unknown as { e2eUpdate: Told }
    w.e2eUpdate = { ...w.e2eUpdate, checking: true }
  })
  await page.locator('[data-header-update]').first().click()
  await expect(modal).toContainText(' on this computer')
  await page.locator('[data-update-restart]').click()
  await expect(page.locator('li').filter({ hasText: 'Conductor is checking for updates' })).toHaveCount(1)
  await expect(modal).toContainText('Restart to update to 0.7.0-rc.12?')
  expect(await page.evaluate(() => sessionStorage.getItem('e2e.installed'))).toBeNull()
  await page.evaluate(() => {
    const w = window as unknown as { e2eUpdate: Told }
    w.e2eUpdate = { ...w.e2eUpdate, checking: false }
  })
  await modal.getByRole('button', { name: 'Later' }).click()
  await expect(modal).toHaveCount(0)

  // Sessions that cannot be counted are never said to be none.
  await page.route('**/api/sessions', (r) => r.abort())
  await page.locator('[data-header-update]').first().click()
  await expect(modal).toContainText('The sessions running on this computer, if any, end: they could not be counted.')
  await page.unroute('**/api/sessions')
  await page.locator('[data-update-restart]').click()
  await expect.poll(() => page.evaluate(() => sessionStorage.getItem('e2e.installed'))).toBe('1')
})

// The tray's Restart to update shows the workbench and asks there, in the
// dialog that says what the restart ends; only the dialog restarts.
test("the tray's restart to update is asked in the workbench's dialog", async ({ page, state }) => {
  await stubApp(page, state.token, state.baseURL, { ready: '0.7.0-rc.12', announce: false, failed: '' })
  await page.goto('/yard')
  await expect(page.locator('[data-header-update]').first()).toBeVisible()
  await found(page, { ready: '0.7.0-rc.12', announce: false, failed: '', restart: '0.7.0-rc.12' })
  const modal = page.getByRole('dialog')
  await expect(modal).toContainText('Restart to update to 0.7.0-rc.12?')
  await expect(modal).toContainText(' on this computer')
  expect(await page.evaluate(() => sessionStorage.getItem('e2e.installed'))).toBeNull()
  await page.locator('[data-update-restart]').click()
  await expect.poll(() => page.evaluate(() => sessionStorage.getItem('e2e.installed'))).toBe('1')
})

// The sessions are counted as the dialog opens: a count asked by a dialog
// closed since, answering late, never stands for the one open now.
test('a restart dialog reopened says its own count of the sessions, never an earlier one', async ({ page, api, state }) => {
  await stubApp(page, state.token, state.baseURL, { ready: '0.7.0-rc.12', announce: false, failed: '' })
  await page.goto('/yard')
  await expect(page.locator('[data-header-update]').first()).toBeVisible()
  let release!: () => void
  const released = new Promise<void>((r) => (release = r))
  let counts = 0
  await page.route('**/api/sessions', async (route) => {
    if (route.request().method() !== 'GET') return route.continue()
    // The first count, taken before the session below starts, answers late.
    if (++counts === 1) {
      const response = await route.fetch()
      await released
      return route.fulfill({ response })
    }
    return route.continue()
  })
  const modal = page.getByRole('dialog')
  await page.locator('[data-header-update]').first().click()
  await expect(modal).toContainText('Counting the sessions')
  await modal.getByRole('button', { name: 'Later' }).click()
  await expect(modal).toHaveCount(0)
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'counted' })
  await page.locator('[data-header-update]').first().click()
  await expect(modal).toContainText(' on this computer')
  const said = await modal.innerText()
  expect(said).not.toContain('No session runs')
  release()
  await page.waitForTimeout(500)
  expect(await modal.innerText()).toBe(said)
  await modal.getByRole('button', { name: 'Later' }).click()
  await page.unroute('**/api/sessions')
  await api.stopSession(s.id)
})

// A restart asked outlives its dialog: an invite's join page has no layout,
// and the dialog that comes back with it asks again, the sessions counted.
test('a restart asked is asked again as the workbench comes back from an invite', async ({ page, api, state }) => {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'invited' })
  const { token } = await api.ok<{ token: string }>('POST', `/api/sessions/${s.id}/links`, { role: 'view' })
  await stubApp(page, state.token, state.baseURL, { ready: '0.7.0-rc.12', announce: false, failed: '' })
  await page.goto('/yard')
  const modal = page.getByRole('dialog')
  await page.locator('[data-header-update]').first().click()
  await expect(modal).toContainText(' on this computer')
  await expect.poll(() => page.evaluate(() => typeof (window as unknown as Invited).e2eInvite)).toBe('function')
  // The invite routes the page: started, never awaited across that route, its answer kept by the page and read after (#281).
  await page.evaluate(({ server, token }) => {
    const w = window as unknown as Invited
    void w.e2eInvite!({ server, token }).then((taken) => (w.e2eTaken = taken))
  }, { server: state.baseURL, token })
  await expect(page).toHaveURL(new RegExp(`/join/${token}`))
  await expect.poll(() => page.evaluate(() => (window as unknown as Invited).e2eTaken)).toBe(true)
  await page.goBack()
  await expect(page).toHaveURL(/\/yard$/)
  await expect(modal).toContainText('Restart to update to 0.7.0-rc.12?')
  await expect(modal).toContainText(' on this computer')
  await expect(page.locator('[data-update-restart]')).toBeEnabled()
  await modal.getByRole('button', { name: 'Later' }).click()
  await api.stopSession(s.id)
})

// A check that finds a newer version empties the updater's cache as its
// download begins, and a download that fails clears it: the app lets the
// update offered go and refuses to install it, and the dialog that asked
// for it closes and says so, before anything stops.
test('an update the app no longer holds restarts nothing, and the dialog says so', async ({ page, state }) => {
  await stubApp(page, state.token, state.baseURL, { ready: '0.7.0-rc.12', announce: false, failed: '' })
  await page.goto('/yard')
  const modal = page.getByRole('dialog')
  const gone = page.locator('li').filter({ hasText: 'Conductor 0.7.0-rc.12 is no longer ready' })

  // Let go while the dialog is open.
  await page.locator('[data-header-update]').first().click()
  await expect(modal).toContainText('Restart to update to 0.7.0-rc.12?')
  await found(page, { ready: '', announce: false, failed: '' })
  await expect(modal).toHaveCount(0)
  await expect(gone).toHaveCount(1)
  await expect(page.locator('[data-header-update]')).toHaveCount(0)

  // Let go as the restart is asked, before the page hears: the app refuses it.
  await found(page, { ready: '0.7.0-rc.12', announce: false, failed: '' })
  await page.locator('[data-header-update]').first().click()
  await expect(modal).toContainText(' on this computer')
  await page.evaluate(() => {
    const w = window as unknown as { e2eUpdate: Told }
    w.e2eUpdate = { ...w.e2eUpdate, ready: '' }
  })
  await page.locator('[data-update-restart]').click()
  await expect(modal).toHaveCount(0)
  await expect(gone).toHaveCount(2)
  expect(await page.evaluate(() => sessionStorage.getItem('e2e.installed'))).toBeNull()
})

// A window hidden in the tray asks nothing when a check finds the update:
// told once, it would take the announcement for a toast nobody sees. It
// asks as it shows again.
test('a hidden window asks once it shows again, and only then is told', async ({ page, state }) => {
  await stubApp(page, state.token, state.baseURL, { ready: '', announce: false, failed: '' })
  await page.goto('/yard')
  await expect(page.locator('[data-sidebar-menus]').first()).toBeVisible()
  const shown = (visible: boolean) =>
    page.evaluate((visible) => {
      Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => (visible ? 'visible' : 'hidden') })
      Object.defineProperty(document, 'hidden', { configurable: true, get: () => !visible })
      document.dispatchEvent(new Event('visibilitychange'))
    }, visible)
  await shown(false)
  await found(page, { ready: '0.7.0-rc.12', announce: true, failed: '' })
  await page.waitForTimeout(500)
  expect(await page.evaluate(() => (window as unknown as { e2eUpdate: Told }).e2eUpdate.announce)).toBe(true)
  await expect(page.locator('li').filter({ hasText: READY })).toHaveCount(0)
  await shown(true)
  await expect(page.locator('li').filter({ hasText: READY })).toHaveCount(1)
  await expect(page.locator('[data-header-update]').first()).toBeVisible()
})

test('an update that did not install is said once, with the way to the log', async ({ page, state }) => {
  await stubApp(page, state.token, state.baseURL, { ready: '', announce: false, failed: '0.7.0-rc.12' })
  await page.goto('/yard')
  const toast = page.locator('li').filter({ hasText: 'Conductor 0.7.0-rc.12 did not install' })
  await expect(toast).toHaveCount(1)
  await toast.getByRole('button', { name: 'Show the log' }).click()
  await expect.poll(() => page.evaluate(() => sessionStorage.getItem('e2e.log'))).toBe('1')
  await page.reload()
  await expect(page.locator('[data-sidebar-menus]').first()).toBeVisible()
  await page.waitForTimeout(500)
  await expect(page.locator('li').filter({ hasText: 'did not install' })).toHaveCount(0)
})
