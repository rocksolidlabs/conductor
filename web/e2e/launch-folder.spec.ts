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
async function folderPage(
  browser: Parameters<typeof dayPage>[0],
  bridge: 'press' | 'old' | 'tab',
  size: { width: number; height: number } = { width: 1440, height: 900 },
  touch = false,
): Promise<{ page: Page; asked: Asked[]; answer: (a: Record<string, unknown>) => void }> {
  const asked: Asked[] = []
  /** Answers the stand-in gives in place of the server's, in order (busy, a folder missing, ...), as the app can. */
  const queued: Array<Record<string, unknown>> = []
  const answer = (a: Record<string, unknown>) => void queued.push(a)
  const page = await dayPage(browser, { server: own.started, ...size, touch, path: '/sessions', ready: touch || size.width < 1024 ? '[data-bottom-bar]' : '#dashboard-sidebar-main' })
  if (bridge === 'tab') return { page, asked, answer }
  const ctx: BrowserContext = page.context()
  await ctx.exposeFunction('e2eAllowFolder', async (path: string, confirmed: string) => {
    asked.push({ path, confirmed })
    const next = queued.shift()
    if (next) return next
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
  return { page, asked, answer }
}

/** The app as another window allows a folder: the real POST /api/roots with the token and the key, as the app sends it. */
async function allowElsewhere(path: string) {
  const r = await fetch(`${own.started.baseURL}/api/roots`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${own.started.token}`, 'X-Conductor-Desktop-Key': KEY, 'Content-Type': 'application/json' },
    body: JSON.stringify({ path }),
  })
  expect(r.status).toBe(200)
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
  // Launch pressed with the line showing: nothing is sent, and the focus goes to Folder, never to the button.
  await dialog.locator('[data-launch-submit]').click()
  await expect(dialog.getByLabel('Folder', { exact: true })).toBeFocused()
  expect(launches(), 'nothing was sent').toBe(0)
  // Enter, Enter, Enter from the task: still nothing sent, and nothing allowed: the press is a person's own act.
  const task = dialog.locator('[data-launch-message-input]')
  await task.fill('say hello')
  await task.focus()
  for (let i = 0; i < 3; i++) {
    await page.keyboard.press('Enter')
    await expect(dialog.getByLabel('Folder', { exact: true })).toBeFocused()
  }
  expect(asked, 'Enter allowed nothing').toEqual([])
  expect(launches(), 'nothing was sent').toBe(0)
  // One press: the app is asked once, for exactly the folder the line names.
  const allow = dialog.locator('[data-launch-allow]')
  await allow.click()
  await expect(line(dialog)).toHaveAttribute('data-folder-state', 'allowed_now')
  await expect(line(dialog)).toHaveText('Agents can use this folder now.')
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
  await expect(line(dialog).locator('[data-folder-words]')).toHaveText(`Allow your whole home folder, ${folder}? Agents could read and change everything in it.`)
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
  await expect(line(dialog)).toHaveText("Agents can't use this folder. Pick another.")
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

// UX3 R76: the same line, and the same one press, under every field where a person names a folder agents will work in.

/** Types a folder into a field and moves the focus away, the field's list closed: its check runs. */
async function typeInto(page: Page, input: Locator, path: string) {
  await input.fill(path)
  await input.press('Tab')
  await expect(page.getByRole('listbox')).toHaveCount(0)
}

test("a crew's folder, an agent trial's, a Dispatch project's and Brakeman's say the same line, and the press allows the folder named", async ({ browser }) => {
  const { page, asked } = await folderPage(browser, 'press')
  const lineIn = (scope: Locator) => scope.locator('[data-launch-folder-line]')

  // A new crew's folder: the line, and the press.
  await page.goto('/crews/new')
  await expect(page.getByRole('textbox', { name: 'Crew name' })).toBeFocused()
  const crewFolder = outside('crew-folder')
  // The editor has one folder field: the crew's.
  const crewField = page.locator('[data-dir-input] input[role="combobox"]')
  await expect(crewField).toHaveCount(1)
  await typeInto(page, crewField, crewFolder)
  const crewLine = lineIn(page.locator('body'))
  await expect(crewLine).toHaveAttribute('data-folder-state', 'outside')
  await expect(crewLine.locator('[data-folder-words]')).toHaveText("Agents can't use this folder yet.")
  await crewLine.locator('[data-launch-allow]').click()
  await expect(crewLine).toHaveAttribute('data-folder-state', 'allowed_now')
  expect(asked).toEqual([{ path: crewFolder, confirmed: '' }])
  // The editor reads the folder's git state again once it is allowed: what it read before the press is not under the line.
  await expect(page.locator('[data-git-state]')).toContainText('not in a git repository')
  await expect(page.locator('[data-git-state]')).not.toContainText('allowed roots')

  // The agent trial's folder.
  await page.goto('/crews')
  await page.locator('[data-new-comparison]').click()
  const form = page.locator('[data-compare-template]')
  await expect(form).toBeVisible()
  await atRest(page.getByRole('dialog').filter({ has: form }))
  const trialFolder = outside('trial-folder')
  // A space at an end, which the trial trims as it sends the folder: the line checks the folder sent.
  await typeInto(page, form.locator('[data-compare-cwd]').getByRole('combobox'), `${trialFolder} `)
  await expect(lineIn(form)).toHaveAttribute('data-folder-state', 'outside')
  await expect(lineIn(form).locator('[data-launch-allow]')).toBeVisible()
  await page.keyboard.press('Escape')

  // A Dispatch project's repository.
  await page.goto('/dispatch')
  await page.locator('[data-new-project-open]').click()
  const project = page.locator('[data-new-project]')
  await expect(project).toBeVisible()
  await typeInto(page, project.locator('[data-dir-input] input[role="combobox"]'), `${outside('project-folder')} `)
  await expect(lineIn(project)).toHaveAttribute('data-folder-state', 'outside')
  await expect(lineIn(project).locator('[data-launch-allow]')).toBeVisible()
  await page.keyboard.press('Escape')

  // The folder Brakeman reads, on Settings, where Brakeman is offered.
  await page.goto('/settings?section=brakeman')
  const card = page.locator('[data-brakeman-settings]')
  await expect(card).toBeVisible()
  const brakemanField = card.locator('[data-brakeman-cwd] input, input[data-brakeman-cwd]').first()
  await expect(brakemanField).toBeVisible()
  await typeInto(page, brakemanField, `${outside('brakeman-folder')} `)
  await expect(lineIn(card)).toHaveAttribute('data-folder-state', 'outside')
  await expect(lineIn(card).locator('[data-launch-allow]')).toBeVisible()
  // Only the crew's press was made: each line asks the app only when pressed.
  expect(asked).toHaveLength(1)
})

test('an older check answered after a newer one changes nothing: the line and its button are the newer text\'s', async ({ browser }) => {
  const { page } = await folderPage(browser, 'press')
  const dialog = await openLaunch(page)
  const first = outside('older')
  const second = outside('newer')
  // The first text's check held, then answered as a missing folder once the second text's answer is shown.
  const held: Route[] = []
  const match = (u: URL) => u.pathname === '/api/roots/check' && u.searchParams.get('path') === first
  await page.route(match, (r) => void held.push(r))
  await typeFolder(dialog, first)
  await expect.poll(() => held.length).toBeGreaterThan(0)
  await typeFolder(dialog, second)
  await expect(line(dialog)).toHaveAttribute('data-folder-state', 'outside')
  const done = page.waitForEvent('requestfinished', (r) => new URL(r.url()).pathname === '/api/roots/check' && new URL(r.url()).searchParams.get('path') === first)
  for (const r of held.splice(0)) await r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ path: first, state: 'missing' }) })
  await done
  await page.unroute(match)
  // The page has taken the older answer and drawn after it (two frames): the second text still holds the line.
  await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))))
  await expect(line(dialog)).toHaveAttribute('data-folder-state', 'outside')
  await expect(dialog.locator('[data-launch-allow]')).toBeVisible()
  await expect(line(dialog)).not.toContainText('There is no folder at this path.')
})

test('the press allows the folder the check named, named on the line, never the text read again', async ({ browser }) => {
  const { page, asked } = await folderPage(browser, 'press')
  const dialog = await openLaunch(page)
  const typed = join(outsideRoot, 'as-typed')
  const named = outside('as-named')
  // The server's check names another folder than the text (another spelling of the same place, as for `..`).
  await page.route(
    (u) => u.pathname === '/api/roots/check' && u.searchParams.get('path') === typed,
    (r) => r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ path: named, state: 'outside' }) }),
  )
  await typeFolder(dialog, typed)
  await expect(line(dialog).locator('[data-folder-words]')).toHaveText(`Agents can't use ${named} yet.`)
  await expect(line(dialog).locator('[data-folder-named]')).toHaveText(named)
  await dialog.locator('[data-launch-allow]').click()
  await expect.poll(() => asked.length).toBe(1)
  expect(asked[0]).toEqual({ path: named, confirmed: '' })
})

test('a text is checked as typed: a space at its end is no other folder, and an empty Folder shows no line', async ({ browser }) => {
  const { page, asked } = await folderPage(browser, 'press')
  // An empty text checked would be the server's default folder: here one agents may not use, so a check of it would draw a line.
  const blank: string[] = []
  page.on('request', (r) => {
    const u = new URL(r.url())
    if (u.pathname === '/api/roots/check' && !u.searchParams.get('path')?.trim()) blank.push(u.search)
  })
  await page.route(
    (u) => u.pathname === '/api/roots/check' && !u.searchParams.get('path')?.trim(),
    (r) => r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ path: outsideRoot, state: 'outside' }) }),
  )
  const dialog = await openLaunch(page)
  const folder = outside('padded')
  await typeFolder(dialog, `${folder} `)
  await expect(line(dialog)).toHaveText('There is no folder at this path.')
  await expect(dialog.locator('[data-launch-allow]')).toHaveCount(0)
  await dialog.getByLabel('Folder', { exact: true }).fill('')
  await dialog.locator('[data-launch-message-input]').click()
  await expect(line(dialog)).toHaveCount(0)
  // Launch pressed with Folder empty: still no check of an empty text, and no line.
  const answered = page.waitForResponse((r) => r.request().method() === 'POST' && new URL(r.url()).pathname === '/api/sessions')
  await dialog.locator('[data-launch-submit]').click()
  const s = (await (await answered).json()) as Session
  if (s.id) made.push(s.id)
  expect(blank, 'no check of an empty Folder').toEqual([])
  expect(asked).toEqual([])
})

test('a press refused keeps Folder and the task; a check after it decides, and the launch goes through once the cause is gone', async ({ browser }) => {
  const { page, asked, answer } = await folderPage(browser, 'press')
  const dialog = await openLaunch(page)
  const folder = outside('refused-then-allowed')
  await typeFolder(dialog, folder)
  await dialog.locator('[data-launch-message-input]').fill('say hello')
  // The app answers busy, then that the folder is missing.
  answer({ ok: false, code: 'busy', message: 'the settings are being saved: try again' })
  await dialog.locator('[data-launch-allow]').click()
  await expect(line(dialog).locator('[data-folder-words]')).toHaveText('Conductor is busy. Press Allow this folder again in a moment.')
  answer({ ok: false, code: 'folder_missing', message: 'no folder at this path' })
  await dialog.locator('[data-launch-allow]').click()
  await expect(line(dialog)).toHaveText('There is no folder at this path.')
  await expect(dialog.getByLabel('Folder', { exact: true })).toHaveValue(folder)
  await expect(dialog.locator('[data-launch-message-input]')).toHaveValue('say hello')
  expect(asked).toHaveLength(2)
  // The folder allowed from elsewhere: the check before the launch says so, and the launch is sent there.
  await allowElsewhere(folder)
  const answered = page.waitForResponse((r) => r.request().method() === 'POST' && new URL(r.url()).pathname === '/api/sessions')
  await dialog.locator('[data-launch-submit]').click()
  const reply = await answered
  const s = (await reply.json()) as Session
  if (s.id) made.push(s.id)
  expect(reply.status()).toBe(201)
  expect(s.cwd).toBe(folder)
})

test("the line's Settings link closes the dialog on its way there", async ({ browser }) => {
  const { page } = await folderPage(browser, 'old')
  const dialog = await openLaunch(page)
  await typeFolder(dialog, outside('to-settings'))
  await line(dialog).locator('[data-folder-settings]').click()
  await expect(page).toHaveURL(/\/settings\?section=folders$/)
  await expect(page.getByRole('dialog')).toHaveCount(0)
})

for (const size of [DAILY, { width: 1440, height: 810 }]) {
  test(`at ${size.width} x ${size.height} the dialog does not scroll with the line, nor with the wide question`, async ({ browser }) => {
    const { page } = await folderPage(browser, 'press', size)
    const dialog = await openLaunch(page)
    const folder = outside(`no-scroll-${size.width}`)
    await page.route(
      (u) => u.pathname === '/api/roots/check' && u.searchParams.get('path') === folder,
      (r) => r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ path: folder, real: folder, state: 'outside', wide: 'home' }) }),
    )
    await typeFolder(dialog, folder)
    const body = dialog.locator('[data-slot="body"]')
    const overflow = () => body.evaluate((el) => el.scrollHeight - el.clientHeight)
    await expect(dialog.locator('[data-launch-allow]')).toBeVisible()
    await atRest(dialog)
    expect(await overflow(), 'with the line').toBeLessThanOrEqual(0.5)
    await dialog.locator('[data-launch-allow]').click()
    await expect(dialog.locator('[data-folder-cancel]')).toBeVisible()
    await atRest(dialog)
    expect(await overflow(), 'with the wide question').toBeLessThanOrEqual(0.5)
  })
}


test('a folder outside the folders agents may use opens no list under the field, and the listing\'s own words are nowhere', async ({ browser }) => {
  const { page } = await folderPage(browser, 'press')
  const dialog = await openLaunch(page)
  const field = dialog.getByLabel('Folder', { exact: true })
  const folder = outside('no-list')
  // Typed, the focus kept in the field: its listing is asked and refused; the line under the field says it.
  await field.click()
  const listed = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/paths' && new URL(r.url()).searchParams.get('prefix') === folder)
  await field.fill(folder)
  expect((await listed).status()).toBe(400)
  await expect(line(dialog)).toHaveAttribute('data-folder-state', 'outside')
  await expect(field).toBeFocused()
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await expect(page.locator('body')).not.toContainText('no directory in the prefix')
  await expect(page.locator('body')).not.toContainText('under the allowed roots')
})

test("a Dispatch project's dialog left for Settings through its line keeps its draft for the next opening", async ({ browser }) => {
  const { page } = await folderPage(browser, 'old')
  await page.goto('/dispatch')
  const open = page.locator('[data-new-project-open]')
  await open.click()
  const project = page.locator('[data-new-project]')
  await expect(project).toBeVisible()
  await project.locator('[data-new-project-key]').fill('LEFT')
  await project.locator('[data-new-project-name]').fill('Left for Settings')
  const folder = outside('project-to-settings')
  await typeInto(page, project.locator('[data-dir-input] input[role="combobox"]'), folder)
  await project.locator('[data-close-requirement="acceptance"]').click()
  const settings = project.locator('[data-launch-folder-line] [data-folder-settings]')
  await expect(settings).toBeVisible()
  await settings.click()
  await expect(page).toHaveURL(/\/settings\?section=folders$/)
  await expect(page.locator('[data-new-project]')).toHaveCount(0)
  // Back on Dispatch: the next opening has the draft as it was left.
  await page.goBack()
  await expect(open).toBeVisible()
  await open.click()
  await expect(project).toBeVisible()
  await expect(project.locator('[data-new-project-key]')).toHaveValue('LEFT')
  await expect(project.locator('[data-new-project-name]')).toHaveValue('Left for Settings')
  await expect(project.locator('[data-dir-input] input[role="combobox"]')).toHaveValue(folder)
  await expect(project.locator('[data-close-requirement="acceptance"]')).toHaveAttribute('aria-checked', 'false')
  // Taken back once: closed and opened again, the dialog starts anew.
  await page.keyboard.press('Escape')
  await expect(project).toHaveCount(0)
  await open.click()
  await expect(project.locator('[data-new-project-key]')).toHaveValue('')
})

test("in an agent trial's form, the line's Settings link closes the form's dialog on its way there", async ({ browser }) => {
  const { page } = await folderPage(browser, 'old')
  await page.goto('/crews')
  await page.locator('[data-new-comparison]').click()
  const form = page.locator('[data-compare-template]')
  await expect(form).toBeVisible()
  await atRest(page.getByRole('dialog').filter({ has: form }))
  await typeInto(page, form.locator('[data-compare-cwd]').getByRole('combobox'), outside('trial-to-settings'))
  const settings = form.locator('[data-launch-folder-line] [data-folder-settings]')
  await expect(settings).toBeVisible()
  await settings.click()
  await expect(page).toHaveURL(/\/settings\?section=folders$/)
  await expect(page.locator('[data-compare-template]')).toHaveCount(0)
})

test("an older answer for the same text, come after the newer one, changes nothing", async ({ browser }) => {
  const { page } = await folderPage(browser, 'press')
  const dialog = await openLaunch(page)
  const folder = outside('same-text')
  const other = outside('same-text-between')
  // The first check of the folder held; another folder typed, then the folder again, whose second check is answered.
  const held: Route[] = []
  let first = true
  const match = (u: URL) => u.pathname === '/api/roots/check' && u.searchParams.get('path') === folder
  await page.route(match, (r) => {
    if (first) {
      first = false
      held.push(r)
    } else void r.continue()
  })
  await typeFolder(dialog, folder)
  await expect.poll(() => held.length).toBe(1)
  await typeFolder(dialog, other)
  await typeFolder(dialog, folder)
  await expect(line(dialog)).toHaveAttribute('data-folder-state', 'outside')
  const done = page.waitForEvent('requestfinished', (r) => r === held[0]!.request())
  await held[0]!.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ path: folder, state: 'missing' }) })
  await done
  await page.unroute(match)
  await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))))
  await expect(line(dialog)).toHaveAttribute('data-folder-state', 'outside')
  await expect(dialog.locator('[data-launch-allow]')).toBeVisible()
})

test('a press answered busy goes with a later check that fails: no button left that does nothing', async ({ browser }) => {
  const { page, answer } = await folderPage(browser, 'press')
  const dialog = await openLaunch(page)
  const folder = outside('busy-then-failed')
  await typeFolder(dialog, folder)
  answer({ ok: false, code: 'busy', message: 'the settings are being saved: try again' })
  await dialog.locator('[data-launch-allow]').click()
  await expect(line(dialog).locator('[data-folder-words]')).toHaveText('Conductor is busy. Press Allow this folder again in a moment.')
  // The folder's next check fails at the network: the busy line and its button go with it.
  const match = (u: URL) => u.pathname === '/api/roots/check' && u.searchParams.get('path') === folder
  await page.route(match, (r) => r.abort())
  const failed = page.waitForEvent('requestfailed', (r) => match(new URL(r.url())))
  await typeFolder(dialog, outside('busy-between'))
  await typeFolder(dialog, folder)
  await failed
  await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))))
  await expect(dialog.locator('[data-launch-allow]')).toHaveCount(0)
  await page.unroute(match)
})

test('a folder missing stays said once a launch was tried with it, the cursor back in Folder', async ({ browser }) => {
  const { page } = await folderPage(browser, 'press')
  const dialog = await openLaunch(page)
  const launches = countLaunches(page)
  const missing = join(outsideRoot, 'never-made')
  const field = dialog.getByLabel('Folder', { exact: true })
  // Typed with the cursor kept in Folder: not said while typed.
  await field.click()
  const checkedIt = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/roots/check' && new URL(r.url()).searchParams.get('path') === missing)
  await field.fill(missing)
  await checkedIt
  await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))))
  await expect(field).toBeFocused()
  await expect(line(dialog)).toHaveCount(0)
  // Launch tried: the check before it says the folder is missing, the cursor goes back to Folder, and the line stays.
  await dialog.locator('[data-launch-submit]').click()
  await expect(field).toBeFocused()
  await expect(line(dialog)).toHaveText('There is no folder at this path.')
  expect(launches(), 'nothing was sent').toBe(0)
})

test("a clone's Folder of spaces alone is no folder: nothing is checked, and the clone runs in its source's folder", async ({ browser }) => {
  const source = await own.api.ok<Session>('POST', '/api/sessions', { agentId: 'plain', name: `clone source ${randomBytes(3).toString('hex')}` })
  made.push(source.id)
  const { page } = await folderPage(browser, 'press')
  const checked: string[] = []
  page.on('request', (r) => {
    const u = new URL(r.url())
    if (u.pathname === '/api/roots/check') checked.push(u.searchParams.get('path') ?? '')
  })
  const r = page.locator(`[data-session-list] [data-sidebar-row="s:${source.id}"]`).first()
  await expect(r).toBeVisible()
  await r.hover()
  await r.locator('[data-row-more]').click()
  await page.getByRole('menuitem', { name: /^Clone with options…/ }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.locator(`[data-launch-clone="${source.id}"]`)).toBeVisible()
  await dialog.locator('[data-clone-name]').fill(`clone ${randomBytes(3).toString('hex')}`)
  await dialog.getByLabel('Folder', { exact: true }).fill('   ')
  await dialog.getByLabel('Folder', { exact: true }).blur()
  const sent = page.waitForRequest((q) => q.method() === 'POST' && new URL(q.url()).pathname === `/api/sessions/${source.id}/clone`)
  const answered = page.waitForResponse((q) => q.request().method() === 'POST' && new URL(q.url()).pathname === `/api/sessions/${source.id}/clone`)
  await dialog.locator('[data-launch-clone-submit]').click()
  expect(((await sent).postDataJSON() as { cwd?: string }).cwd).toBeUndefined()
  const reply = await answered
  const clone = (await reply.json()) as Session
  if (clone.id) made.push(clone.id)
  expect(reply.status()).toBe(201)
  expect(clone.cwd).toBe(source.cwd)
  expect(checked.filter((p) => !p.trim()), 'no check of a Folder of spaces').toEqual([])
})
