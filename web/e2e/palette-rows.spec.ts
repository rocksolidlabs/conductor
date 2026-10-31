import { randomUUID } from 'node:crypto'
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Page } from '@playwright/test'
import { expect, test, type Session } from './fixtures'
import { freeProjectKey, makeProject, makeTicket, storedTicket } from './dispatch-helpers'

// Every palette row does what it says (#332): each row run from the
// palette, its visible effect asserted. A row named for showing something
// never hides it (Open Chat twice keeps the chat open: the defect that
// started this); a row that cannot act is disabled with its words. What the
// other palette specs cover already (digits, verbs that ask, the editor's
// rows, Touched, the pill) is not repeated here.
// Each test walks one page or several through twenty to forty palette steps. A fixed timeout is spent by such a walk on a loaded
// box with every step passing (#281: the walk of the other pages took the config's whole 120 s at a load average of 110), so
// each step raises the test's timeout by the caps of its own waits as it takes them (budget below), the way examples.spec.ts
// adds its handoff's caps: the test's timeout is their sum, and the config's 120 s above it, for the waits between the steps.
test.describe.configure({ mode: 'serial' })

/** The cap of every wait a palette step makes: the config's expect bound, and the actions' (a fill, a click, a read). */
const STEP_MS = 15_000
test.use({ actionTimeout: STEP_MS })
/** Raises the running test's timeout by the caps of `waits` waits, as a step is about to make them. */
function budget(waits: number) {
  const info = test.info()
  info.setTimeout(info.timeout + waits * STEP_MS)
}

const sessions: string[] = []
const runs: string[] = []
let crewId = ''

test.afterAll(async ({ api }) => {
  for (const id of runs) await api.stopRun(id)
  for (const id of sessions) await api.stopSession(id)
  if (crewId) await api.call('DELETE', `/api/crews/${encodeURIComponent(crewId)}`)
})

/**
 * Where the palette is: gone, closing (its dialog's data-state turns closed as Escape or a row closes it, before the fade ends
 * and it leaves the page), or open.
 */
function paletteState(page: Page): Promise<'gone' | 'closing' | 'open'> {
  return page.evaluate(() => {
    const p = document.querySelector('[data-palette]')
    if (!p) return 'gone'
    return p.closest('[role="dialog"]')?.getAttribute('data-state') === 'closed' ? 'closing' : 'open'
  })
}
/**
 * Closes the palette with one Escape from its input, and waits with the expect's bound for it to go. From a row, a field or a
 * question Escape goes back a step instead, and a key pressed on the clock (Escape again 1.5 s on, #281) landed wherever the
 * focus was by then, a fading palette's or the page's: so the input takes the focus first, and a closing palette is only waited for.
 */
async function closePalette(page: Page) {
  budget(1)
  if ((await paletteState(page)) === 'open') {
    const inInput = await page.evaluate(() => {
      const input = document.querySelector<HTMLElement>('[data-palette] input[data-palette-input]')
      input?.focus()
      return !!input && document.activeElement === input
    })
    if (inInput) await page.keyboard.press('Escape')
  }
  await expect(page.locator('[data-palette]')).toBeHidden()
}
/**
 * Opens the palette and returns it: Alt+/ (the terminal's way, which the page shares; the layout's own listener, from a terminal
 * or not, and it only ever opens the palette), pressed once, when the layout listens for it, and the expect's bound for it to
 * show. A key pressed before the layout is mounted is lost (#281: after a goto, Alt+/ went in 1.1 s on, the page's first request
 * 4.9 s on), and a second press on a 1.5 s clock only hid that; the sidebar's root is the layout's own, in the page once it is.
 */
async function openPalette(page: Page) {
  const palette = page.locator('[data-palette]')
  await closePalette(page)
  budget(2)
  await expect(page.locator('#dashboard-sidebar-main')).toBeAttached()
  await page.keyboard.press('Alt+/')
  await expect(palette).toBeVisible()
  return palette
}
/** Runs the row `key` with a click, after a query that keeps it in the list. */
async function runRow(page: Page, key: string, query = '') {
  budget(3) // the line, the row, the click
  const palette = await openPalette(page)
  if (query) await palette.locator('input[data-palette-input]').fill(query)
  const row = palette.locator(`[data-palette-row="${key}"]`)
  await expect(row).toBeVisible()
  await row.click()
  return palette
}
/** Focuses the row `key` from the keyboard (↓ until it is the focused row) and presses Enter: what a field-on-Enter row needs. */
async function enterRow(page: Page, key: string, query = '') {
  budget(3) // the line, the row, its focus
  const palette = await openPalette(page)
  if (query) await palette.locator('input[data-palette-input]').fill(query)
  const row = palette.locator(`[data-palette-row="${key}"]`)
  await expect(row).toBeVisible()
  for (let i = 0; i < 12 && !(await row.getAttribute('data-palette-focused') !== null); i++) await page.keyboard.press('ArrowDown')
  await expect(row).toHaveAttribute('data-palette-focused', '')
  await page.keyboard.press('Enter')
  return palette
}
/** The switch state of the row `key` as the palette shows it now. */
async function switchOf(page: Page, key: string, query = ''): Promise<string | null> {
  budget(2) // the line, the read
  const palette = await openPalette(page)
  if (query) await palette.locator('input[data-palette-input]').fill(query)
  const state = await palette.locator(`[data-palette-row="${key}"] [data-palette-switch]`).getAttribute('data-palette-switch')
  await closePalette(page)
  return state
}
const toast = (page: Page, text: string | RegExp) => page.locator('li').filter({ hasText: text }).last()

test('the session page: the views show and never hide, Share, Copy a view link, the marks, Fold, the tab rows, Stop', async ({ page, api, state, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const cwd = join(state.root, 'rows-session')
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'notes.txt'), 'alpha\nbeta\n')
  writeFileSync(join(cwd, 'other.txt'), 'other\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e rows session', cwd })
  sessions.push(s.id)
  await page.goto(`/sessions/${s.id}`)
  await expect(page.locator('.terminal-host .xterm-helper-textarea').first()).toBeFocused({ timeout: 30_000 })
  const inspector = page.locator('[data-inspector]')
  const pressed = (item: string) => expect(inspector.locator(`[data-rail-item="${item}"]`)).toHaveAttribute('aria-pressed', 'true')
  // The views: each shows its view; Open Chat run twice keeps the chat open (it used to toggle it closed).
  await runRow(page, 'page:view:explorer')
  await pressed('explorer')
  await runRow(page, 'page:view:chat')
  await pressed('chat')
  await expect(inspector.locator('[data-chat]')).toBeVisible()
  await runRow(page, 'page:view:chat')
  await expect(inspector).toHaveAttribute('data-inspector-open', 'true')
  await expect(inspector.locator('[data-chat]')).toBeVisible()
  await runRow(page, 'page:view:activity')
  await pressed('activity')
  await runRow(page, 'page:view:people')
  await pressed('people')
  // Share opens the dialog, which makes a link as it opens; Copy a view link copies at once and says so.
  await runRow(page, 'page:share')
  await expect(page.locator('[data-share-created]')).toBeVisible({ timeout: 15_000 })
  await page.keyboard.press('Escape')
  await expect(page.locator('[data-share-created]')).toBeHidden()
  await runRow(page, 'page:copy-link')
  await expect(toast(page, /View link to .* copied/)).toBeVisible({ timeout: 15_000 })
  // The marks reach the server.
  await runRow(page, 'page:mark-working')
  await expect.poll(async () => (await api.session(s.id)).attention?.state, { timeout: 15_000 }).toBe('working')
  await runRow(page, 'page:mark-needs')
  await expect.poll(async () => (await api.session(s.id)).attention?.state, { timeout: 15_000 }).toBe('needs_input')
  await runRow(page, 'page:mark-clear')
  await expect.poll(async () => (await api.session(s.id)).attention?.state ?? '', { timeout: 15_000 }).not.toBe('needs_input')
  // Two files open: a tab row brings its tab to the front; Fold folds the editor and brings it back.
  const area = page.locator('[data-editor-area]')
  for (const name of ['notes.txt', 'other.txt']) {
    const palette = await openPalette(page)
    await palette.locator('input[data-palette-input]').fill(`:${name}`)
    await page.keyboard.press('ArrowDown')
    await page.keyboard.press('Enter')
    await expect(area.locator(`[data-editor-tab="file:${join(cwd, name)}"]`)).toBeVisible({ timeout: 30_000 })
  }
  await expect(area.locator(`[data-editor-tab="file:${join(cwd, 'other.txt')}"]`)).toHaveAttribute('data-editor-tab-active', '')
  await runRow(page, `page:tab:file:${join(cwd, 'notes.txt')}`)
  await expect(area.locator(`[data-editor-tab="file:${join(cwd, 'notes.txt')}"]`)).toHaveAttribute('data-editor-tab-active', '')
  await runRow(page, 'page:fold')
  await expect(area).toHaveAttribute('data-editor-area', 'folded')
  await runRow(page, 'page:fold')
  await expect(area).toHaveAttribute('data-editor-area', 'open')
  // Stop asks in the row; Enter stops.
  const palette = await runRow(page, 'page:stop')
  await expect(palette.locator('[data-palette-row="page:stop"] [data-palette-ask]')).toBeVisible()
  await page.keyboard.press('Enter')
  await expect.poll(async () => (await api.session(s.id)).status, { timeout: 15_000 }).toMatch(/stopped|ended/)
})

test("the other pages: the Yard's chips, the Roundhouse's switches and rotation, Crews' New crew, Events' tabs and refresh, Agents' Launch, Settings' Save", async ({ page, api }) => {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e rows pages' })
  sessions.push(s.id)
  // The Yard: a chip row sets the filter and reads as on.
  await page.goto('/yard')
  await expect(page.locator('[data-yard-filter="all"]')).toBeVisible({ timeout: 30_000 })
  await runRow(page, 'page:filter:running')
  await expect(page.locator('[data-yard-filter="running"]')).toHaveAttribute('data-active', '')
  expect(await switchOf(page, 'page:filter:running')).toBe('on')
  await runRow(page, 'page:filter:all')
  await expect(page.locator('[data-yard-filter="all"]')).toHaveAttribute('data-active', '')
  // The Roundhouse: the two switches flip and flip back; the rotation row's words follow the rotation.
  await page.goto('/roundhouse')
  await expect(page.locator('[data-palette]')).toBeHidden()
  const autoplay = await switchOf(page, 'page:autoplay')
  await runRow(page, 'page:autoplay')
  expect(await switchOf(page, 'page:autoplay')).toBe(autoplay === 'on' ? 'off' : 'on')
  await runRow(page, 'page:autoplay')
  expect(await switchOf(page, 'page:autoplay')).toBe(autoplay)
  const follow = await switchOf(page, 'page:follow')
  await runRow(page, 'page:follow')
  expect(await switchOf(page, 'page:follow')).toBe(follow === 'on' ? 'off' : 'on')
  await runRow(page, 'page:follow')
  const before = await (await openPalette(page)).locator('[data-palette-row="page:rotation"]').textContent()
  await closePalette(page)
  await runRow(page, 'page:rotation')
  const after = await (await openPalette(page)).locator('[data-palette-row="page:rotation"]').textContent()
  expect(after).not.toBe(before)
  await closePalette(page)
  // Crews: New crew goes to the form.
  await page.goto('/crews')
  await expect(page.locator('[data-palette]')).toBeHidden()
  await runRow(page, 'page:new')
  await expect(page).toHaveURL(/\/crews\/new/)
  // Events: a tab row shows its tab and reads as on; Refresh asks the server again and shows its answer (the answer rewritten
  // on the way, so the words on the page prove the request: Codex's batch round, a no-op Refresh passed before).
  await page.goto('/events')
  await expect(page.locator('[data-events-tabs]')).toBeVisible({ timeout: 30_000 })
  await runRow(page, 'page:tab:routing')
  expect(await switchOf(page, 'page:tab:routing')).toBe('on')
  await runRow(page, 'page:tab:integrations')
  await expect(page.locator('[data-events-integrations-tab]')).toBeVisible()
  await page.route('**/api/integrations', async (route) => {
    const res = await route.fetch()
    const body = (await res.json()) as { integrations: Array<{ name: string }> }
    for (const i of body.integrations) i.name = `${i.name} (refreshed)`
    await route.fulfill({ response: res, json: body })
  })
  const asked = page.waitForResponse((r) => r.url().includes('/api/integrations'))
  await runRow(page, 'page:refresh')
  await asked
  await expect(page.locator('[data-events-integrations-tab]').getByText('(refreshed)').first()).toBeVisible()
  await page.unroute('**/api/integrations')
  // Agents: Launch opens the dialog.
  await page.goto('/agents')
  await expect(page.locator('[data-session-list]')).toBeVisible({ timeout: 30_000 })
  await runRow(page, 'page:launch')
  await expect(page.getByLabel('Working directory')).toBeVisible({ timeout: 15_000 })
  await page.keyboard.press('Escape')
  // Settings in the browser is the server's file and a notice: no Save row that could never act (the desktop app's rows are
  // the app's, under the settings spec's stand-in bridge).
  await page.goto('/settings')
  await expect(page.locator('[data-about-card]')).toBeVisible({ timeout: 30_000 })
  const palette = await openPalette(page)
  await expect(palette.locator('[data-palette-row]').first()).toBeVisible()
  await expect(palette.locator('[data-palette-row="page:save"]')).toHaveCount(0)
  await closePalette(page)
})

test("the run page: the broadcast field, the views, the run's chat, Share, Stop", async ({ page, api }) => {
  const crew = await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
    name: 'e2e rows crew',
    goal: 'rows',
    cwd: '',
    where: 'server',
    isolation: 'none',
    openAfterLaunch: false,
    yolo: false,
    members: [{ name: 'lead', agentId: 'claude', prompt: 'say hello', start: { when: 'immediately' } }],
  })
  crewId = crew.crew.id
  const run = await api.launchCrew(crewId)
  runs.push(run.id)
  await page.goto(`/runs/${encodeURIComponent(run.id)}`)
  await expect(page.locator('[data-palette]')).toBeHidden()
  await expect(page.locator('[data-run-chat-button]')).toBeVisible({ timeout: 30_000 })
  // Broadcast takes a line in the row (Enter opens the field; nothing is sent without a line).
  let palette = await enterRow(page, 'page:broadcast')
  await expect(palette.locator('[data-palette-row="page:broadcast"] [data-palette-field] input')).toBeFocused()
  await closePalette(page)
  // The views read as on once run.
  await runRow(page, 'page:view:graph')
  expect(await switchOf(page, 'page:view:graph')).toBe('on')
  // The run's chat opens; Share opens the dialog; Stop asks and stops.
  await runRow(page, 'page:chat')
  await expect(page.locator('[data-run-chat]')).toBeVisible({ timeout: 15_000 })
  await page.keyboard.press('Escape')
  await runRow(page, 'page:share')
  await expect(page.locator('[data-share-created]')).toBeVisible({ timeout: 15_000 })
  await page.keyboard.press('Escape')
  palette = await runRow(page, 'page:stop')
  await expect(palette.locator('[data-palette-row="page:stop"] [data-palette-ask]')).toBeVisible()
  await page.keyboard.press('Enter')
  await expect.poll(async () => (await api.run(run.id)).state, { timeout: 30_000 }).toMatch(/stopped|ended|done/)
})

test('a session row’s actions: the copies say so, Open Files/Chat/Activity/People reach the page on that view, Type a line opens the field, Resume makes a new session', async ({ page, api, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  // Names of this run's own: a repeat's sessions stay listed, ended, until the server sweeps them, and a name typed into the
  // palette must find this one alone (the first row, Resume's new session by its name).
  const tag = randomUUID().slice(0, 8)
  const actions = `e2e rows actions ${tag}`
  const goneName = `e2e rows gone ${tag}`
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: actions })
  sessions.push(s.id)
  const gone = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: goneName })
  sessions.push(gone.id)
  await api.stopSession(gone.id)
  await expect.poll(async () => (await api.session(gone.id)).status, { timeout: 15_000 }).toMatch(/stopped|ended/)
  await page.goto('/agents')
  await expect(page.locator('[data-session-list]')).toBeVisible({ timeout: 30_000 })
  const act = async (name: string, id: string) => {
    budget(5) // the line, the row, its focus, its actions, the action's click
    const palette = await openPalette(page)
    await palette.locator('input[data-palette-input]').fill(name)
    const row = palette.locator(`[data-palette-row="session:${id}"]`)
    await expect(row).toBeVisible()
    await page.keyboard.press('ArrowDown')
    await expect(row).toHaveAttribute('data-palette-focused', '')
    await page.keyboard.press('ArrowRight')
    await expect(row.locator('[data-palette-actions]')).toBeVisible()
    return (action: string) => row.locator(`[data-palette-action="${action}"]`)
  }
  let a = await act(actions, s.id)
  await a('copy-path').click()
  await expect(toast(page, 'Path copied')).toBeVisible({ timeout: 15_000 })
  a = await act(actions, s.id)
  await a('copy-link').click()
  await expect(toast(page, /View link to .* copied/)).toBeVisible({ timeout: 15_000 })
  for (const [action, tab, item] of [['open-files', 'files', 'explorer'], ['open-chat', 'chat', 'chat'], ['open-activity', 'activity', 'activity'], ['open-people', 'people', 'people']] as const) {
    await page.goto('/agents')
    await expect(page.locator('[data-session-list]')).toBeVisible({ timeout: 30_000 })
    a = await act(actions, s.id)
    await a(action).click()
    await expect(page).toHaveURL(new RegExp(`/sessions/${s.id}\\?tab=${tab}`), { timeout: 15_000 })
    await expect(page.locator(`[data-inspector] [data-rail-item="${item}"]`)).toHaveAttribute('aria-pressed', 'true', { timeout: 30_000 })
  }
  // On the session's own page the same action shows the view here, twice without hiding it.
  a = await act(actions, s.id)
  await a('open-chat').click()
  await expect(page.locator('[data-inspector] [data-chat]')).toBeVisible()
  a = await act(actions, s.id)
  await a('open-chat').click()
  await expect(page.locator('[data-inspector] [data-chat]')).toBeVisible()
  // Type a line opens the field; Resume on an exited session makes a new one.
  await page.goto('/agents')
  await expect(page.locator('[data-session-list]')).toBeVisible({ timeout: 30_000 })
  a = await act(actions, s.id)
  await a('field').click()
  await expect(page.locator(`[data-palette-row="session:${s.id}"] [data-palette-field] input`)).toBeFocused()
  await closePalette(page)
  a = await act(goneName, gone.id)
  await a('resume').click()
  // The new session itself, by its name: the list's length is no measure of it, since the server sweeps sessions that ended a
  // while ago at any time (#281: on CI, with ninety sessions from the specs before, the count fell by eight as this waited).
  const resumed = async () => (await api.ok<{ sessions: Session[] }>('GET', '/api/sessions')).sessions.find((x) => x.name === goneName && x.id !== gone.id)
  await expect.poll(async () => (await resumed())?.id ?? '', { timeout: 30_000, message: 'Resume makes a new session under the old name' }).toMatch(/^[0-9a-f]{16}$/)
  const made = await resumed()
  if (made) sessions.push(made.id)
})

test('verbs and switches: go, mark, share, answer; the theme, the sidebar, notifications; the dialogs; the prefixes', async ({ page, api, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write', 'notifications'])
  // Names of this run's own: a verb's name must match one thing, and a repeat's sessions stay listed, ended, until the server
  // sweeps them (#281: "mark e2e rows verbs working" read as a search in the second repeat of a --repeat-each run).
  const tag = randomUUID().slice(0, 8)
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: `e2e rows verbs ${tag}` })
  sessions.push(s.id)
  const asks = await api.ok<Session>('POST', '/api/sessions', { agentId: 'asker', name: `e2e rows asks ${tag}` })
  sessions.push(asks.id)
  await expect.poll(async () => (await api.session(asks.id)).attention?.state, { timeout: 30_000 }).toBe('needs_input')
  await page.goto('/agents')
  await expect(page.locator('[data-session-list]')).toBeVisible({ timeout: 30_000 })
  const verb = async (line: string) => {
    budget(2) // the line, the row
    const palette = await openPalette(page)
    await palette.locator('input[data-palette-input]').fill(line)
    const row = palette.locator('[data-palette-row^="verb:"]').first()
    await expect(row).toBeVisible()
    await page.keyboard.press('ArrowDown')
    await page.keyboard.press('Enter')
    return palette
  }
  await verb('go yard')
  await expect(page).toHaveURL(/\/yard/)
  await verb(`mark e2e rows verbs ${tag} working`)
  await expect.poll(async () => (await api.session(s.id)).attention?.state, { timeout: 15_000 }).toBe('working')
  await verb(`share e2e rows verbs ${tag}`)
  await expect(toast(page, /View link to .* copied/)).toBeVisible({ timeout: 15_000 })
  await verb(`answer e2e rows asks ${tag} 1`)
  await expect.poll(async () => (await api.session(asks.id)).attention?.state ?? '', { timeout: 15_000 }).not.toBe('needs_input')
  // The switches: the theme flips the html class and back; the sidebar row's words follow its state; notifications flips.
  const dark = async () => page.evaluate(() => document.documentElement.classList.contains('dark'))
  const was = await dark()
  await runRow(page, 'action:theme', '> theme')
  expect(await dark()).toBe(!was)
  await runRow(page, 'action:theme', '> theme')
  expect(await dark()).toBe(was)
  const sidebarWords = async () => (await openPalette(page)).locator('[data-palette-row="action:sidebar"]').textContent()
  const words = await sidebarWords()
  await closePalette(page)
  await runRow(page, 'action:sidebar', '> sidebar')
  expect(await sidebarWords()).not.toBe(words)
  await closePalette(page)
  await runRow(page, 'action:sidebar', '> sidebar')
  const notifications = await switchOf(page, 'action:notifications', '> notifications')
  await runRow(page, 'action:notifications', '> notifications')
  expect(await switchOf(page, 'action:notifications', '> notifications')).toBe(notifications === 'on' ? 'off' : 'on')
  await runRow(page, 'action:notifications', '> notifications')
  // Dialogs: Your name, Keyboard shortcuts, Launch.
  await runRow(page, 'action:name', '> your name')
  await expect(page.locator('[data-name-dialog]')).toBeVisible()
  await page.keyboard.press('Escape')
  await runRow(page, 'action:shortcuts', '> keyboard')
  await expect(page.locator('[data-shortcuts]')).toBeVisible()
  await page.keyboard.press('Escape')
  await runRow(page, 'action:launch', '> launch an agent')
  await expect(page.getByLabel('Working directory')).toBeVisible({ timeout: 15_000 })
  await page.keyboard.press('Escape')
  // The prefixes each pick their kind.
  for (const [prefix, chip] of [['>', 'Actions'], ['#', 'Runs and crews'], [':', 'Files'], ['@', 'People'], ['!', 'Events'], ['?', 'Shortcuts']] as const) {
    const palette = await openPalette(page)
    await palette.locator('input[data-palette-input]').fill(prefix)
    await expect(palette.locator('[data-palette-chip]')).toHaveText(chip)
    await closePalette(page)
  }
})

test("the Dispatch pages' rows (#409): go dispatch; the projects page's New project; the project page's views, Save, a saved view and New ticket; a ticket row's Set status… and Assign to me; the ticket page's Copy and Back", async ({ page, api, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  await page.addInitScript(() => localStorage.setItem('conductor.displayName', 'Nate'))
  const key = await freeProjectKey(api, 'ER')
  await makeProject(api, key)
  await makeTicket(api, key, '1', { title: 'Rows one' })
  await page.goto('/agents')
  await expect(page.locator('[data-session-list]')).toBeVisible({ timeout: 30_000 })

  // go dispatch, the verb: the projects page.
  const palette = await openPalette(page)
  await palette.locator('input[data-palette-input]').fill('go dispatch')
  await expect(palette.locator('[data-palette-row="verb:go:dispatch"]')).toBeVisible()
  await page.keyboard.press('ArrowDown')
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(/\/dispatch$/)

  // The projects page's group: New project opens its dialog.
  await runRow(page, 'page:new-project', 'new project')
  await expect(page.locator('[data-new-project]')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.locator('[data-new-project]')).toBeHidden()

  // The project page's group: the views, Save the filter as a view, the saved view's own row, New ticket.
  await page.goto(`/dispatch/${key}`)
  await expect(page.locator('[data-list-total]')).toHaveAttribute('data-count', '1')
  await runRow(page, 'page:view:board', 'show the board')
  await expect(page.locator(`[data-board] [data-card="${key}-1"]`)).toBeVisible()
  await expect(page).toHaveURL(/view=board/)
  await runRow(page, 'page:view:list', 'show the list')
  await expect(page.locator(`[data-ticket-row="${key}-1"]`)).toBeVisible()
  await runRow(page, 'page:save-view', 'save the filter')
  await expect(page.locator('[data-view-dialog]')).toBeVisible()
  await page.locator('[data-view-dialog] [data-view-name-input]').fill('All of it')
  await page.locator('[data-view-submit]').click()
  const chip = page.locator('[data-view-chip][data-view-name="All of it"]')
  await expect(chip).toHaveAttribute('data-active', '')
  const viewId = (await chip.getAttribute('data-view-chip')) ?? ''
  try {
    await runRow(page, 'page:view:roadmap', 'show the roadmap')
    await expect(page.locator('[data-roadmap]')).toBeVisible()
    await runRow(page, `page:saved-view:${viewId}`, 'saved view: all of it')
    await expect(page.locator(`[data-ticket-row="${key}-1"]`)).toBeVisible()
    await expect(chip).toHaveAttribute('data-active', '')
    await runRow(page, 'page:new-ticket', `new ticket in ${key}`)
    await expect(page.locator('[data-new-ticket]')).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(page.locator('[data-new-ticket]')).toBeHidden()
  } finally {
    await api.ok('POST', `/api/dispatch/views/${viewId}/delete`, { opId: `${key.toLowerCase()}-vdel` })
  }

  // A ticket's row: Set status… opens the picker and sets it; Assign to me adds the name as a person.
  const ticketRow = async () => {
    budget(5) // the line, the row, its focus, its actions, the action's click
    const p = await openPalette(page)
    await p.locator('input[data-palette-input]').fill(`${key}-1`)
    const row = p.locator(`[data-palette-row="ticket:${key}-1"]`)
    await expect(row).toBeVisible()
    await row.focus()
    await page.keyboard.press('ArrowRight')
    await expect(row.locator('[data-palette-actions]')).toBeVisible()
    return row
  }
  await (await ticketRow()).locator('[data-palette-action="set-status"]').click()
  const picker = page.locator('[data-picker]')
  await expect(picker).toHaveAttribute('data-picker-field', 'status')
  await picker.locator('[data-pick-value="in_progress"]').click()
  await expect.poll(async () => (await storedTicket(api, `${key}-1`)).status).toBe('in_progress')
  await expect(toast(page, `Status of ${key}-1 set`)).toBeVisible({ timeout: 15_000 })
  await (await ticketRow()).locator('[data-palette-action="assign-me"]').click()
  await expect.poll(async () => (await storedTicket(api, `${key}-1`)).assignees).toEqual(['person:Nate'])
  await expect(toast(page, `${key}-1 assigned to Nate`)).toBeVisible({ timeout: 15_000 })

  // The ticket page's group: Copy the key, Back to the project.
  await page.goto(`/dispatch/${key}/1`)
  await expect(page.locator('[data-ticket-title]')).toHaveText('Rows one')
  await runRow(page, 'page:copy-key', `copy ${key}-1`)
  await expect(toast(page, `${key}-1 copied`)).toBeVisible({ timeout: 15_000 })
  await runRow(page, 'page:back', `back to ${key}`)
  await expect(page).toHaveURL(new RegExp(`/dispatch/${key}$`))
})
