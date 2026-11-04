import { _electron as electron, expect, test } from '@playwright/test'
import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'

// The packaged-shape smoke: the shell starts the checkout's server, the window shows the workbench signed in, and quitting ends the
// server. Needs a display (xvfb-run on Linux CI) and bin/conductor (make build-go).
const bin = process.env.CONDUCTOR_DESKTOP_BIN || join(__dirname, '..', '..', 'bin', 'conductor')

test.skip(!existsSync(bin), `no server binary at ${bin}: make build-go`)

test('the app opens the workbench on its own server and stops it on quit', async () => {
  test.setTimeout(120_000)
  const app = await electron.launch({ args: [join(__dirname, '..')], env: { ...process.env, CONDUCTOR_DESKTOP_BIN: bin, ELECTRON_DISABLE_SANDBOX: '1' } })
  const output: string[] = []
  app.process().stderr?.on('data', (d: Buffer) => output.push(d.toString()))
  app.process().stdout?.on('data', (d: Buffer) => output.push(d.toString()))
  // The splash is the first window, up while the server starts; the workbench window comes once the server answers.
  const splash = await app.firstWindow()
  await expect.poll(() => splash.url(), { timeout: 30_000 }).toContain('splash.html')
  await expect(splash.locator('#credit')).toHaveAttribute('aria-label', 'Sponsored and maintained by RockSolid Labs')
  const workbench = /^http:\/\/(127\.0\.0\.1|localhost):\d+\//
  let page = splash
  try {
    await expect
      .poll(
        () => {
          const w = app.windows().find((p) => workbench.test(p.url()))
          if (w) page = w
          return !!w
        },
        { timeout: 60_000 },
      )
      .toBe(true)
  } catch (e) {
    // What the shell and its server said, for a CI log that otherwise shows only the timeout.
    console.log('window urls:', app.windows().map((p) => p.url()))
    console.log('electron output:\n' + output.join('').slice(-4000))
    try {
      const userData = await app.evaluate(({ app }) => app.getPath('userData'))
      const dir = join(userData, 'logs')
      for (const f of readdirSync(dir)) console.log(`--- ${f}:\n` + readFileSync(join(dir, f), 'utf8').slice(-4000))
    } catch (err) {
      console.log('no app logs:', (err as Error).message)
    }
    throw e
  }
  const url = new URL(page.url())
  await expect(page.locator('[data-session-list], nav').first()).toBeVisible({ timeout: 30_000 })
  // The splash goes once the workbench window shows.
  await expect.poll(() => app.windows().filter((p) => p.url().includes('splash.html')).length, { timeout: 30_000 }).toBe(0)
  // Signed in through the bridge: the agents page lists the catalog without asking for a token.
  await page.goto(`${url.origin}/agents`)
  await expect(page.locator('[data-agent]').first()).toBeVisible({ timeout: 30_000 })
  // Restart server (the Server menu) binds a fresh port: the window follows to it, on the same page, still signed in.
  await app.evaluate(({ Menu }) => {
    const find = (items: Electron.MenuItem[]): Electron.MenuItem | undefined => {
      for (const it of items) {
        if (it.label === 'Restart server') return it
        const sub = it.submenu ? find(it.submenu.items) : undefined
        if (sub) return sub
      }
    }
    const item = find(Menu.getApplicationMenu()?.items ?? [])
    if (!item) throw new Error('no Restart server in the menu')
    item.click()
  })
  await expect.poll(() => new URL(page.url()).origin, { timeout: 60_000 }).not.toBe(url.origin)
  expect(new URL(page.url()).pathname).toBe('/agents')
  await expect(page.locator('[data-agent]').first()).toBeVisible({ timeout: 30_000 })
  const live = new URL(page.url())
  const token = await page.evaluate(() => (window as unknown as { conductorDesktop: { token(): Promise<string> } }).conductorDesktop.token())
  expect(token.length).toBeGreaterThanOrEqual(32)
  expect(await page.evaluate(() => localStorage.getItem('conductor.workbenchToken'))).toBeNull()
  // Ctrl+= zooms in without Shift and Ctrl+- out, taken before the page sees them (the terminal would eat Ctrl+-); Ctrl+0 resets.
  const level = () => app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows().find((w) => w.webContents.getURL().startsWith('http'))?.webContents.getZoomLevel() ?? NaN)
  // Through the native input path (sendInputEvent), as a key pressed on the keyboard arrives; Playwright's keys go straight to the page.
  const press = (keyCode: string) =>
    app.evaluate(({ BrowserWindow }, keyCode) => {
      const wc = BrowserWindow.getAllWindows().find((w) => w.webContents.getURL().startsWith('http'))!.webContents
      const modifiers: Array<'control' | 'meta'> = [process.platform === 'darwin' ? 'meta' : 'control']
      wc.sendInputEvent({ type: 'keyDown', keyCode, modifiers })
      wc.sendInputEvent({ type: 'keyUp', keyCode, modifiers })
    }, keyCode)
  // Each move shows its level in the workbench (#431), whose live region keeps the words last shown (data-zoom-last: the indicator
  // itself goes after 1.5 s, which a loaded machine could spend before a look), and in the View menu's level line, built again.
  const menuItem = (prefix: string) =>
    app.evaluate(({ Menu }, prefix) => {
      const find = (items: Electron.MenuItem[]): Electron.MenuItem | undefined => {
        for (const it of items) {
          if (it.label.startsWith(prefix)) return it
          const sub = it.submenu ? find(it.submenu.items) : undefined
          if (sub) return sub
        }
      }
      const item = find(Menu.getApplicationMenu()?.items ?? [])
      return item ? { label: item.label, enabled: item.enabled } : null
    }, prefix)
  const shown = page.locator('[data-zoom-status]')
  await press('0')
  await expect.poll(level).toBe(0)
  await expect(shown).toHaveAttribute('data-zoom-last', '100%')
  expect(await menuItem('Zoom: ')).toEqual({ label: 'Zoom: 100%', enabled: false })
  await press('=')
  await expect.poll(level).toBe(0.5)
  await expect(shown).toHaveAttribute('data-zoom-last', '110%')
  await expect.poll(() => menuItem('Zoom: ')).toEqual({ label: 'Zoom: 110%', enabled: false })
  await press('-')
  await press('-')
  await expect.poll(level).toBe(-0.5)
  await expect(shown).toHaveAttribute('data-zoom-last', '91%')
  await expect.poll(() => menuItem('Zoom: ')).toEqual({ label: 'Zoom: 91%', enabled: false })
  await press('0')
  await expect.poll(level).toBe(0)
  await expect(shown).toHaveAttribute('data-zoom-last', '100%')
  await expect.poll(() => menuItem('Zoom: ')).toEqual({ label: 'Zoom: 100%', enabled: false })
  // View › Reset Zoom to 100%, found as Restart server is and clicked at 110%, brings the level to 0 and says 100%.
  await press('=')
  await expect.poll(level).toBe(0.5)
  await expect(shown).toHaveAttribute('data-zoom-last', '110%')
  expect(await menuItem('Reset Zoom to 100%')).toEqual({ label: 'Reset Zoom to 100%', enabled: true })
  expect(await menuItem('Actual Size')).toBeNull()
  await app.evaluate(({ Menu }) => {
    const find = (items: Electron.MenuItem[]): Electron.MenuItem | undefined => {
      for (const it of items) {
        if (it.label === 'Reset Zoom to 100%') return it
        const sub = it.submenu ? find(it.submenu.items) : undefined
        if (sub) return sub
      }
    }
    const item = find(Menu.getApplicationMenu()?.items ?? [])
    if (!item) throw new Error('no Reset Zoom to 100% in the menu')
    item.click()
  })
  await expect.poll(level).toBe(0)
  await expect(shown).toHaveAttribute('data-zoom-last', '100%')
  await expect.poll(() => menuItem('Zoom: ')).toEqual({ label: 'Zoom: 100%', enabled: false })
  // Server log (the Server menu) opens a window that shows the server's log, the lines so far and then live: it was an empty dark box
  // (no preload, so no line reached the page), seen by the owner on Windows on 2026-10-06.
  await app.evaluate(({ Menu }) => {
    const find = (items: Electron.MenuItem[]): Electron.MenuItem | undefined => {
      for (const it of items) {
        if (it.label === 'Server log') return it
        const sub = it.submenu ? find(it.submenu.items) : undefined
        if (sub) return sub
      }
    }
    const item = find(Menu.getApplicationMenu()?.items ?? [])
    if (!item) throw new Error('no Server log in the menu')
    item.click()
  })
  // A new window's URL is still blank as it opens: the app's windows are polled until one shows the log page.
  await expect.poll(() => app.windows().some((p) => p.url().includes('log.html')), { timeout: 30_000 }).toBe(true)
  const log = app.windows().find((p) => p.url().includes('log.html'))!
  await expect(log.locator('#log')).toContainText('msg=', { timeout: 30_000 })
  await log.close()
  // The page header's fullscreen button takes the window fullscreen and back: the app refused the page's fullscreen request before,
  // and the button did nothing (seen by the owner on Windows on 2026-10-08).
  const fsButton = page.locator('[data-fullscreen]').first()
  await expect(fsButton).toHaveAttribute('aria-label', 'Enter fullscreen')
  await fsButton.click()
  await expect(fsButton).toHaveAttribute('aria-label', 'Exit fullscreen', { timeout: 10_000 })
  expect(await page.evaluate(() => !!document.fullscreenElement)).toBe(true)
  await fsButton.click()
  await expect(fsButton).toHaveAttribute('aria-label', 'Enter fullscreen', { timeout: 10_000 })
  // An invite handed to the app while its window is open (open-url on macOS; second-instance argv elsewhere goes the same way) reaches
  // the page over the bridge: the join page opens in place, with no page load, where the app used to load the page anew.
  await page.evaluate(() => ((window as unknown as { kept?: string }).kept = 'still here'))
  const invited = 'SmokeInviteToken0123456789abcdefABCDEF_-xyz'
  await app.evaluate(({ app }, url) => app.emit('open-url', { preventDefault: () => {} }, url), `conductor://${live.host}/join/${invited}?http=1`)
  await expect.poll(() => page.url(), { timeout: 30_000 }).toBe(`${live.origin}/join/${invited}?server=${encodeURIComponent(live.origin)}`)
  expect(await page.evaluate(() => (window as unknown as { kept?: string }).kept)).toBe('still here')
  const health = await fetch(`${live.origin}/api/health`)
  expect(health.ok).toBe(true)
  await app.close()
  await expect.poll(() => fetch(`${live.origin}/api/health`).then(() => 'up').catch(() => 'down'), { timeout: 30_000 }).toBe('down')
})
