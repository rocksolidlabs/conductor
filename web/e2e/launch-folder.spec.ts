import { mkdirSync, mkdtempSync, realpathSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { randomBytes } from 'node:crypto'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import type { BrowserContext, Locator, Page, Route } from '@playwright/test'
import { DAILY, atRest, dayPage, expect, test, type Session } from './fixtures'
import { noSideways, tapTarget } from './layout'
import { ownServer, type OwnServer } from './own-server'

// Allow this folder (UX3 R30): the Launch agent dialog says of the folder in Folder what the server's check says, and in the desktop
// app allows a folder agents cannot use yet with one press. On a server of its own started as the desktop app starts it (the token
// and the app's key on its stdin: whoami says `folders: app`). The app's bridge is a stand-in in the page whose allowFolder sends the
// real POST /api/roots from the test's side with the token and the key, as the app does, and records every folder it was asked for;
// a page with no bridge is a plain tab.

let own: OwnServer
const KEY = randomBytes(24).toString('hex')
const made: string[] = []
/** Folders outside the allowed root, made by the spec and removed at its end. */
let outsideRoot = ''

test.describe.configure({ mode: 'serial' })

const STUB_PROMPT = String.raw`(?m)^> ?[^\n]*\s*\z`
const PROMPTED = 'Claude Code'

test.beforeAll(async () => {
  test.setTimeout(120_000)
  own = await ownServer('launch-folder', { agents: [{ id: 'prompted', from: 'claude', env: { STUB_TUI: '1' }, promptShape: STUB_PROMPT, name: PROMPTED }], keep: ['plain'], desktopKey: KEY })
  outsideRoot = realpathSync(mkdtempSync(join(tmpdir(), 'conductor-e2e-outside-')))
})

test.afterAll(async () => {
  for (const id of made) await own.api.stopSession(id).catch(() => {})
  await own.stop()
  if (outsideRoot) rmSync(outsideRoot, { recursive: true, force: true })
})

/** A folder of the spec's own outside the folders agents may use. */
function outside(name: string): string {
  const p = join(outsideRoot, name)
  mkdirSync(p, { recursive: true })
  return p
}

/** What the stand-in bridge was asked: each press's folder and the kind it confirmed. */
interface Asked {
  path: string
  confirmed: string
}

/**
 * A page in a context with the desktop app's bridge stood in (`bridge: 'press'`), a bridge without the press (`'old'`, an older
 * app), or none (`'tab'`). The press sends POST /api/roots from the test's side, with the token and the app's key, and answers as the
 * app's allow.ts does.
 */
async function folderPage(browser: Parameters<typeof dayPage>[0], bridge: 'press' | 'old' | 'tab', size: { width: number; height: number } = { width: 1440, height: 900 }, touch = false): Promise<{ page: Page; asked: Asked[] }> {
  const asked: Asked[] = []
  const page = await dayPage(browser, { server: own.started, ...size, touch, path: '/sessions', ready: touch || size.width < 1024 ? '[data-bottom-bar]' : '#dashboard-sidebar-main' })
  if (bridge === 'tab') return { page, asked }
  const ctx: BrowserContext = page.context()
  await ctx.exposeFunction('e2eAllowFolder', async (path: string, confirmed: string) => {
    asked.push({ path, confirmed })
    const r = await fetch(`${own.started.baseURL}/api/roots`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${own.started.token}`, 'X-Conductor-Desktop-Key': KEY, 'Content-Type': 'application/json' },
      body: JSON.stringify({ path, confirmed: confirmed || undefined }),
    })
    const body = (await r.json().catch(() => ({}))) as { root?: string; error?: { code?: string; message?: string; real?: string; wide?: string; reason?: string } }
    if (r.status === 200 && body.root) return { ok: true, root: body.root, saved: true }
    const e = body.error ?? {}
    return { ok: false, code: e.code ?? `http_${r.status}`, message: e.message ?? '', ...(e.real ? { real: e.real } : {}), ...(e.wide ? { wide: e.wide } : {}), ...(e.reason ? { reason: e.reason } : {}) }
  })
  await ctx.addInitScript(
    ({ token, url, press }) => {
      const settings = { dataDir: '', allowedRoots: [], defaultCwd: '', yolo: false, reach: 'off', closeToTray: true, wslDistro: '', wslWindowsHome: false, switchyardEnabled: false, switchyardServer: '', switchyardToken: '', switchyardName: '' }
      const w = window as unknown as { conductorDesktop: unknown; e2eAllowFolder: (p: string, c: string) => Promise<unknown> }
      w.conductorDesktop = {
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
        ...(press ? { allowFolder: (p: string, c?: string) => w.e2eAllowFolder(p, c ?? '') } : {}),
      }
    },
    { token: own.started.token, url: own.started.baseURL, press: bridge === 'press' },
  )
  // The bridge is read as the app starts: the page is loaded again with it in place.
  await page.reload()
  await expect(page.locator(touch || size.width < 1024 ? '[data-bottom-bar]' : '#dashboard-sidebar-main')).toBeVisible()
  return { page, asked }
}

/** The Launch dialog on the prompted stub, at rest. */
async function openLaunch(page: Page): Promise<Locator> {
  await page.getByRole('button', { name: 'Launch agent' }).first().click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByRole('button', { name: 'Launch', exact: true })).toBeVisible()
  const radio = dialog.getByRole('radiogroup', { name: 'Agent' }).getByRole('radio', { name: /Claude Code$/ })
  await radio.click()
  await expect(radio).toHaveAttribute('aria-checked', 'true')
  await atRest(dialog)
  return dialog
}

/** Types a folder into Folder and moves on, the field's list closed: the check runs for it. */
async function typeFolder(dialog: Locator, path: string) {
  await dialog.getByLabel('Folder', { exact: true }).fill(path)
  await dialog.locator('[data-launch-message-input]').click()
}

const line = (dialog: Locator) => dialog.locator('[data-launch-folder-line]')

/** Counts the launches the page sends, from now on. */
function countLaunches(page: Page): () => number {
  let n = 0
  page.on('request', (r) => {
    if (r.method() === 'POST' && new URL(r.url()).pathname === '/api/sessions') n++
  })
  return () => n
}

test('one press allows a folder agents cannot use yet, exactly the folder named, and the launch goes on there', async ({ browser }) => {
  const before = await own.api.ok<Session>('POST', '/api/sessions', { agentId: 'plain', name: 'running before' })
  made.push(before.id)
  const { page, asked } = await folderPage(browser, 'press')
  const dialog = await openLaunch(page)
  const folder = outside('downloads')
  const launches = countLaunches(page)
  await typeFolder(dialog, folder)
  await expect(line(dialog)).toHaveAttribute('data-folder-state', 'outside')
  await expect(line(dialog)).toContainText("Agents can't use this folder yet.")
  await expect(dialog.getByLabel('Folder', { exact: true })).toHaveAttribute('aria-invalid', 'true')
  // Launch pressed with the line showing: nothing is sent, and the focus goes to the button.
  await dialog.locator('[data-launch-submit]').click()
  const allow = dialog.locator('[data-launch-allow]')
  await expect(allow).toBeFocused()
  expect(launches(), 'nothing was sent').toBe(0)
  // One press: the app is asked once, for exactly the folder the line names.
  await dialog.locator('[data-launch-message-input]').fill('say hello')
  await allow.click()
  await expect(line(dialog)).toHaveAttribute('data-folder-state', 'allowed_now')
  await expect(line(dialog)).toHaveText('Agents can use this folder now. Saved in Settings.')
  expect(asked).toEqual([{ path: folder, confirmed: '' }])
  // Nothing else of the dialog is lost, and the launch starts there.
  await expect(dialog.locator('[data-launch-message-input]')).toHaveValue('say hello')
  await expect(dialog.locator('[data-launch-submit]')).toBeFocused()
  const answered = page.waitForResponse((r) => r.request().method() === 'POST' && new URL(r.url()).pathname === '/api/sessions')
  // Enter on Launch, where the focus went.
  await page.keyboard.press('Enter')
  const answer = await answered
  const s = (await answer.json()) as Session
  if (s.id) made.push(s.id)
  expect(answer.status()).toBe(201)
  expect(s.cwd).toBe(folder)
  // A session started before still runs: nothing restarted.
  expect((await own.api.call<{ session?: Session }>('GET', `/api/sessions/${before.id}`)).body?.session?.status).toBe('running')
  // Opened again on that folder, no line.
  await page.goto('/sessions')
  const again = await openLaunch(page)
  await typeFolder(again, folder)
  await expect(again.locator('[data-launch-folder-line]')).toHaveCount(0)
})

test('the press is for the text the check answered: a folder typed after the check hides the button until its own answer comes', async ({ browser }) => {
  const { page, asked } = await folderPage(browser, 'press')
  const dialog = await openLaunch(page)
  const first = outside('first')
  const second = outside('second')
  await typeFolder(dialog, first)
  await expect(dialog.locator('[data-launch-allow]')).toBeVisible()
  // The check of the second folder held: the first's button goes at once, and none shows until the second's answer.
  const held: Route[] = []
  const match = (u: URL) => u.pathname === '/api/roots/check' && u.searchParams.get('path') === second
  await page.route(match, (r) => void held.push(r))
  await typeFolder(dialog, second)
  await expect.poll(() => held.length).toBeGreaterThan(0)
  await expect(dialog.locator('[data-launch-allow]')).toHaveCount(0)
  for (const r of held.splice(0)) await r.continue()
  await page.unroute(match)
  await expect(line(dialog)).toHaveAttribute('data-folder-state', 'outside')
  await dialog.locator('[data-launch-allow]').click()
  await expect(line(dialog)).toHaveAttribute('data-folder-state', 'allowed_now')
  expect(asked, 'the press asked for the folder in the field, once').toEqual([{ path: second, confirmed: '' }])
})

test('a wide folder asks first: Cancel holds the focus and allows nothing, the second button confirms its kind', async ({ browser }) => {
  const { page, asked } = await folderPage(browser, 'press')
  const dialog = await openLaunch(page)
  const folder = outside('like-home')
  // The server's check of this folder answered as for a home folder (the spec's server's own home lies inside its allowed root).
  await page.route(
    (u) => u.pathname === '/api/roots/check' && u.searchParams.get('path') === folder,
    (r) => r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ path: folder, real: folder, state: 'outside', wide: 'home' }) }),
  )
  await typeFolder(dialog, folder)
  const allow = dialog.locator('[data-launch-allow]')
  await expect(allow).toBeVisible()
  await allow.click()
  await expect(line(dialog)).toHaveAttribute('data-folder-state', 'wide')
  await expect(line(dialog)).toContainText('Allow your whole home folder? Agents could read and change everything in it.')
  const cancel = dialog.locator('[data-folder-cancel]')
  await expect(cancel).toBeFocused()
  // A double press lands on Cancel: nothing is asked.
  await page.keyboard.press('Enter')
  await expect(line(dialog)).toHaveAttribute('data-folder-state', 'outside')
  expect(asked).toEqual([])
  await dialog.locator('[data-launch-allow]').click()
  await dialog.locator('[data-launch-allow-wide]').click()
  await expect.poll(() => asked.length).toBe(1)
  expect(asked[0]).toEqual({ path: folder, confirmed: 'home' })
})

test('only the app may allow: no button in a plain tab or an older app, nor for a folder refused, and the line says the way on', async ({ browser }) => {
  // A plain tab of the app's server.
  const tab = await folderPage(browser, 'tab')
  let dialog = await openLaunch(tab.page)
  const folder = outside('tab-folder')
  await typeFolder(dialog, folder)
  await expect(line(dialog)).toHaveText("Agents can't use this folder. Allow it in the Conductor app on the computer that runs it.")
  await expect(dialog.locator('[data-launch-allow]')).toHaveCount(0)
  // An app whose bridge has no press.
  const old = await folderPage(browser, 'old')
  dialog = await openLaunch(old.page)
  await typeFolder(dialog, folder)
  await expect(line(dialog)).toHaveText("Agents can't use this folder. Add it to Folders agents can use in Settings, or pick another.")
  await expect(dialog.locator('[data-launch-allow]')).toHaveCount(0)
  expect(old.asked).toEqual([])
  // In the app with the press: the whole disk is refused, with no button.
  const app = await folderPage(browser, 'press')
  dialog = await openLaunch(app.page)
  await typeFolder(dialog, '/')
  await expect(line(dialog)).toHaveAttribute('data-folder-state', 'refused')
  await expect(line(dialog)).toHaveText("Agents can't use the whole disk. Pick a folder inside it.")
  await expect(dialog.locator('[data-launch-allow]')).toHaveCount(0)
  // A reason this page does not know is no yes.
  const unknown = outside('unknown-reason')
  await app.page.route(
    (u) => u.pathname === '/api/roots/check' && u.searchParams.get('path') === unknown,
    (r) => r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ path: unknown, state: 'outside', refused: 'a_reason_to_come' }) }),
  )
  await typeFolder(dialog, unknown)
  await expect(line(dialog)).toHaveText("Agents can't use this folder.")
  await expect(dialog.locator('[data-launch-allow]')).toHaveCount(0)
  expect(app.asked).toEqual([])
})

test('a link says where it leads and offers that folder; a missing folder and a file are said as such', async ({ browser }) => {
  const { page, asked } = await folderPage(browser, 'press')
  const dialog = await openLaunch(page)
  const target = outside('link-target')
  const link = join(outsideRoot, 'link-to-target')
  symlinkSync(target, link)
  await typeFolder(dialog, link)
  await expect(line(dialog)).toHaveAttribute('data-folder-state', 'link')
  await expect(line(dialog).locator('[data-folder-words]')).toHaveText(`This goes through a link to ${target}. Agents can't use that folder yet.`)
  await dialog.locator('[data-folder-use]').click()
  await expect(dialog.getByLabel('Folder', { exact: true })).toHaveValue(target)
  await expect(line(dialog)).toHaveAttribute('data-folder-state', 'outside')
  expect(asked).toEqual([])
  await typeFolder(dialog, join(outsideRoot, 'no-such-folder'))
  await expect(line(dialog)).toHaveText('There is no folder at this path.')
  const file = join(outsideRoot, 'a-file.txt')
  writeFileSync(file, 'x\n')
  await typeFolder(dialog, file)
  await expect(line(dialog)).toHaveText('This is a file, not a folder.')
})

for (const [size, touch] of [[DAILY, false], [{ width: 1440, height: 900 }, false], [{ width: 390, height: 844 }, true]] as const) {
  test(`at ${size.width} x ${size.height} the line and its button sit under Folder, in view, nothing scrolling sideways`, async ({ browser }) => {
    const { page } = await folderPage(browser, 'press', size, touch)
    const dialog = await openLaunch(page)
    await typeFolder(dialog, outside(`sized-${size.width}`))
    const allow = dialog.locator('[data-launch-allow]')
    await expect(allow).toBeVisible()
    await allow.scrollIntoViewIfNeeded()
    await atRest(allow)
    await expect(allow).toBeInViewport()
    const field = (await dialog.getByLabel('Folder', { exact: true }).boundingBox())!
    const box = (await allow.boundingBox())!
    expect(box.y, 'the button is under the field').toBeGreaterThan(field.y + field.height - 0.5)
    if (touch) await tapTarget(allow)
    await noSideways(page, { root: dialog })
  })
}
