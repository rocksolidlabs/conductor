import type { Locator, Page, Route } from '@playwright/test'
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { DAILY, atRest, dayPage, expect, openMore, test } from './fixtures'
import { noSideways, probeColour, tapTarget } from './layout'
import { ownServer, type OwnServer } from './own-server'

// The Launch agent dialog (the six screens' Launch agent, UX3 R26 and R26b): its face holds the agents as chips, Folder, the account
// with its usage in one line, `What should it do?` with the cursor in it, More options folded, and Yolo beside Launch. Enter in the
// task launches, once; Ctrl+Enter launches from any field; what is wrong is said where it is wrong. On a server of its own, with
// copies of the Claude Code and Codex stubs whose prompt the server knows, so that the dialog offers them a task. Launches are held by
// a route, or refused by the server, so no session is made.

let own: OwnServer

test.describe.configure({ mode: 'serial' })

/** The stub's own prompt, "> " and what is typed after it as the last row: what the server recognises before it types a first message. */
const STUB_PROMPT = String.raw`(?m)^> ?[^\n]*\s*\z`
const PROMPTED = 'Claude Code (e2e stub), prompted'
const PROMPTED_CODEX = 'Codex CLI (e2e stub), prompted-codex'
const PLAIN = 'Plain (e2e stub)'
const re = (name: string) => new RegExp(`${name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`)

test.beforeAll(async () => {
  test.setTimeout(120_000)
  // The half dozen agents a person has (the e2e catalog's sixteen would make any dialog scroll).
  own = await ownServer('launch-dialog', {
    agents: [
      { id: 'prompted', from: 'claude', env: { STUB_TUI: '1' }, promptShape: STUB_PROMPT },
      { id: 'prompted-codex', from: 'codex', env: { STUB_TUI: '1' }, promptShape: STUB_PROMPT },
    ],
    keep: ['claude', 'codex', 'plain', 'yolo-login'],
  })
})

/** Sessions a test launched for real: ended before the server goes. */
const made: string[] = []

test.afterAll(async () => {
  for (const id of made) await own.api.stopSession(id).catch(() => {})
  await own.stop()
})

/** The Launch dialog opened from the page's Launch agent button, once its Launch button is drawn. */
async function openLaunch(page: Page): Promise<Locator> {
  await page.getByRole('button', { name: 'Launch agent' }).first().click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByRole('button', { name: 'Launch', exact: true })).toBeVisible()
  return dialog
}

/** Picks the agent named in the open dialog: its radio's name ends with the agent's (the avatar's letters come first). */
async function pick(dialog: Locator, name: string) {
  const radio = dialog.getByRole('radiogroup', { name: 'Agent' }).getByRole('radio', { name: new RegExp(`${name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`) })
  await radio.click()
  await expect(radio).toHaveAttribute('aria-checked', 'true')
}

/**
 * The dialog opened on the prompted stub as a person meets it every day: picked once, the dialog closed, and opened again, so that
 * the agent of the last opening is the one picked and nothing the test did has moved the focus. Returns it at rest.
 */
async function openOnPrompted(page: Page): Promise<Locator> {
  let dialog = await openLaunch(page)
  await pick(dialog, PROMPTED)
  await dialog.getByRole('button', { name: 'Cancel' }).click()
  await expect(dialog).toHaveCount(0)
  dialog = await openLaunch(page)
  await expect(dialog.getByRole('radio', { name: new RegExp(`${PROMPTED.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`) })).toHaveAttribute('aria-checked', 'true')
  await expect(dialog.locator('[data-launch-message-input]')).toBeVisible()
  await atRest(dialog)
  return dialog
}

/** Holds every launch the page sends (POST /api/sessions) until `refuse`, which aborts them all: no session is made. */
async function holdLaunches(page: Page): Promise<{ held: Route[]; refuse: () => Promise<void> }> {
  const held: Route[] = []
  const match = (url: URL) => url.pathname === '/api/sessions'
  await page.route(match, (route) => (route.request().method() === 'POST' ? void held.push(route) : route.fallback()))
  return {
    held,
    refuse: async () => {
      for (const r of held) await r.abort().catch(() => {})
      await page.unroute(match)
    },
  }
}

test('the cursor is in What should it do? as the dialog opens, Shift+Enter is a new line, and Enter launches once', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started })
  const dialog = await openOnPrompted(page)
  const task = dialog.locator('[data-launch-message-input]')
  await expect(task).toBeFocused()

  const launches = await holdLaunches(page)
  await page.keyboard.type('say hello')
  await page.keyboard.press('Shift+Enter')
  await page.keyboard.type('twice')
  await expect(task).toHaveValue('say hello\ntwice')
  expect(launches.held, 'Shift+Enter sent nothing').toHaveLength(0)

  await page.keyboard.press('Enter')
  await expect.poll(() => launches.held.length, { message: 'Enter sent the launch' }).toBe(1)
  expect(launches.held[0]!.request().postDataJSON()).toMatchObject({ agentId: 'prompted', message: 'say hello\ntwice' })
  // Pressed again while the launch is out: nothing more is sent. The held launch is then refused, and the refusal shown.
  await page.keyboard.press('Enter')
  await launches.refuse()
  await expect(dialog.locator('[data-launch-error]')).toBeVisible()
  expect(launches.held, 'the second Enter sent nothing').toHaveLength(1)
  // The dialog stays, with what was typed.
  await expect(task).toHaveValue('say hello\ntwice')
})

test('Ctrl+Enter launches from the folder field', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started })
  const dialog = await openOnPrompted(page)
  const launches = await holdLaunches(page)
  const folder = dialog.locator('[data-dir-input] input[role="combobox"]')
  await folder.focus()
  await expect(folder).toBeFocused()
  await page.keyboard.press('ControlOrMeta+Enter')
  await expect.poll(() => launches.held.length, { message: 'Ctrl+Enter sent the launch' }).toBe(1)
  await launches.refuse()
  await expect(dialog.locator('[data-launch-error]')).toBeVisible()
})

test('Yolo sits beside Launch, reads the same on and off, is in no warning colour, and is off and disabled for an agent that cannot skip its prompts', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started })
  const dialog = await openOnPrompted(page)
  const footer = dialog.locator('[data-slot="footer"]')
  const yolo = footer.locator('[data-yolo-switch]')
  const words = footer.locator('[data-yolo-words]')
  await expect(yolo).toBeVisible()
  await expect(words).toHaveText('skips permission prompts')
  // Never inside the fold.
  await expect(dialog.locator('[data-more] [data-yolo-switch]')).toHaveCount(0)

  const was = await yolo.getAttribute('aria-checked')
  await yolo.click()
  await expect(yolo).not.toHaveAttribute('aria-checked', was!)
  await expect(words).toHaveText('skips permission prompts')
  if ((await yolo.getAttribute('aria-checked')) !== 'true') {
    await yolo.click()
    await expect(yolo).toHaveAttribute('aria-checked', 'true')
  }
  await expect(words).toHaveText('skips permission prompts')
  await atRest(yolo)
  const on = await yolo.evaluate((el) => getComputedStyle(el).backgroundColor)
  expect(await probeColour(page, on), 'Yolo on is not drawn in the warning colour').not.toBe(await probeColour(page, 'var(--ui-warning)'))
  expect(await probeColour(page, await words.evaluate((el) => getComputedStyle(el).color))).not.toBe(await probeColour(page, 'var(--ui-warning-text)'))

  // An agent with no recipe: the switch off and disabled, saying why in place of the words.
  await pick(dialog, PLAIN)
  await expect(yolo).toBeDisabled()
  await expect(yolo).toHaveAttribute('aria-checked', 'false')
  await expect(words).toHaveText(`${PLAIN} cannot skip its prompts`)
  await expect(dialog.locator('[data-yolo-missing]')).toHaveCount(0)
})

test('a model and arguments set inside More options are named on its closed row, whole, and nothing is named when all is as it comes', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started })
  const dialog = await openOnPrompted(page)
  const button = dialog.locator('[data-more-options]')
  await expect(button).toHaveAttribute('aria-expanded', 'false')
  await expect(dialog.locator('[data-more-line]')).toHaveCount(0)
  const more = await openMore(dialog)
  await more.locator('[data-launch-model-input]').fill('opus')
  await more.locator('[data-launch-args]').fill('--add-dir ../shared "two words"')
  await button.click()
  await expect(button).toHaveAttribute('aria-expanded', 'false')
  const line = dialog.locator('[data-more-line]')
  await expect(line).toHaveText('opus · --add-dir ../shared "two words"')
  await expect(line).toBeVisible()
  // Cleared again, the row names nothing.
  await openMore(dialog)
  await more.locator('[data-launch-model-input]').fill('')
  await more.locator('[data-launch-args]').fill('')
  await button.click()
  await expect(dialog.locator('[data-more-line]')).toHaveCount(0)
})

for (const size of [DAILY, { width: 1440, height: 810 }]) {
  test(`at ${size.width} x ${size.height} the usage is in view under the account with More options closed, and Launch with it`, async ({ browser }) => {
    const page = await dayPage(browser, { server: own.started, ...size })
    const dialog = await openOnPrompted(page)
    await expect(dialog.locator('[data-more-options]')).toHaveAttribute('aria-expanded', 'false')
    const usage = dialog.locator('[data-capacity-panel] [data-capacity-line]')
    await expect(usage).toBeInViewport()
    await expect(dialog.locator('[data-launch-account]')).toBeInViewport()
    await expect(dialog.getByRole('button', { name: 'Launch', exact: true })).toBeInViewport()
  })
}

test('on a phone the footer wraps, Yolo above the buttons, and nothing scrolls sideways', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, path: '/sessions', width: 390, height: 844, touch: true })
  const dialog = await openOnPrompted(page)
  const footer = dialog.locator('[data-slot="footer"]')
  const yolo = footer.locator('[data-launch-yolo]')
  const launch = footer.getByRole('button', { name: 'Launch', exact: true })
  await expect(yolo).toBeVisible()
  await expect(launch).toBeVisible()
  const y = (await yolo.boundingBox())!
  const l = (await launch.boundingBox())!
  expect(y.y + y.height, 'Yolo sits above the buttons').toBeLessThanOrEqual(l.y + 0.5)
  await noSideways(page, { root: dialog })
})

test('the dialog says what it is for, its agents are chips with one Tab stop, and a chip pressed hands the cursor back to the task', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started })
  const dialog = await openOnPrompted(page)
  await expect(dialog.locator('[data-slot="description"]')).toHaveText('Start an agent in a folder.')
  const row = dialog.getByRole('radiogroup', { name: 'Agent' })
  const chips = row.getByRole('radio')
  // One Tab stop: the picked chip alone is in the tab order.
  await expect(row.locator('[role="radio"][tabindex="0"]')).toHaveCount(1)
  await expect(row.getByRole('radio', { name: re(PROMPTED) })).toHaveAttribute('tabindex', '0')
  const task = dialog.locator('[data-launch-message-input]')
  await expect(task).toBeFocused()
  await page.keyboard.type('say hello')
  // A chip pressed with the pointer: picked, and the cursor back in the task with its text.
  const codex = row.getByRole('radio', { name: re(PROMPTED_CODEX) })
  await codex.click()
  await expect(codex).toHaveAttribute('aria-checked', 'true')
  await expect(task).toBeFocused()
  await expect(task).toHaveValue('say hello')
  // The arrows move the choice within the row, and the focus with it; it never leaves the row.
  await codex.focus()
  const ids = await chips.evaluateAll((els) => els.map((e) => e.getAttribute('data-launch-agent')))
  const next = ids[(ids.indexOf('prompted-codex') + 1) % ids.length]!
  await page.keyboard.press('ArrowRight')
  const moved = row.locator(`[data-launch-agent="${next}"]`)
  await expect(moved).toHaveAttribute('aria-checked', 'true')
  await expect(moved).toBeFocused()
  await expect(moved).toHaveAttribute('tabindex', '0')
  await page.keyboard.press('ArrowLeft')
  await expect(codex).toBeFocused()
  await expect(codex).toHaveAttribute('aria-checked', 'true')
  // The old card's description line under the row is gone: it is each chip's tooltip now.
  await expect(dialog.locator('form > p.text-xs')).toHaveCount(0)
})

test('an agent that takes no task says it starts at its prompt, and the dialog opened on it has the focus on Launch', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started })
  let dialog = await openLaunch(page)
  await pick(dialog, PLAIN)
  await expect(dialog.locator('[data-launch-message]')).toHaveCount(0)
  await expect(dialog.locator('[data-launch-no-message]')).toHaveText(`${PLAIN} starts at its prompt. Type your task in its terminal.`)
  await expect(dialog).not.toContainText('Conductor types a first message')
  await dialog.getByRole('button', { name: 'Cancel' }).click()
  await expect(dialog).toHaveCount(0)
  dialog = await openLaunch(page)
  await atRest(dialog)
  await expect(dialog.locator('[data-launch-submit]')).toBeFocused()
  const launches = await holdLaunches(page)
  await page.keyboard.press('Enter')
  await expect.poll(() => launches.held.length, { message: 'Enter on Launch sent the launch' }).toBe(1)
  expect(launches.held[0]!.request().postDataJSON()).toMatchObject({ agentId: 'plain' })
  await launches.refuse()
})

test("Folder is named so, shows the folder a launch runs in, and reads a leading ~/ as the server's home", async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started })
  const dialog = await openOnPrompted(page)
  await expect(dialog).not.toContainText('Working directory')
  await expect(dialog).not.toContainText('must be under an allowed root')
  const folder = dialog.getByLabel('Folder', { exact: true })
  await expect(folder).toHaveAttribute('placeholder', own.repo)
  await folder.click()
  await page.keyboard.type('~/')
  await expect(folder).toHaveValue(`${own.home}/`)
})

test('a folder the server refuses is said under Folder in plain words, and Launch sends nothing until it changes', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started })
  const dialog = await openOnPrompted(page)
  const folder = dialog.getByLabel('Folder', { exact: true })
  const refused = dialog.locator('[data-launch-folder-refused]')
  const launch = dialog.locator('[data-launch-submit]')
  let posts = 0
  page.on('request', (r) => {
    if (r.method() === 'POST' && new URL(r.url()).pathname === '/api/sessions') posts++
  })
  const file = join(own.root, 'a-file.txt')
  writeFileSync(file, 'not a folder\n')
  const cases = [
    { path: '/', words: "Agents can't use this folder yet." },
    { path: join(own.root, 'no-such-folder'), words: 'There is no folder at this path.' },
    { path: file, words: 'This is a file, not a folder.' },
  ]
  for (const [i, c] of cases.entries()) {
    await folder.fill(c.path)
    // The folder's list closed: the task takes the focus, then Launch is pressed.
    await dialog.locator('[data-launch-message-input]').click()
    const answered = page.waitForResponse((r) => r.request().method() === 'POST' && new URL(r.url()).pathname === '/api/sessions')
    await launch.click()
    expect((await answered).status(), c.path).toBe(400)
    await expect(refused, c.path).toHaveText(c.words)
    await expect(folder).toHaveAttribute('aria-invalid', 'true')
    await expect(folder).toBeFocused()
    await expect(dialog.locator('[data-launch-error]'), 'said under Folder alone').toHaveCount(0)
    // Pressed again with the folder as it was: the cursor goes back to Folder, and nothing is sent.
    await dialog.locator('[data-launch-message-input]').click()
    await launch.click()
    await expect(folder).toBeFocused()
    expect(posts, c.path).toBe(i + 1)
  }
  // Changed, the refusal goes.
  await folder.fill(own.repo)
  await expect(refused).toHaveCount(0)
  await expect(folder).not.toHaveAttribute('aria-invalid', 'true')
})

test("another refusal is said on top as Not started, in the server's words with no code, and the fields are kept", async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started })
  const dialog = await openOnPrompted(page)
  const match = (url: URL) => url.pathname === '/api/sessions'
  await page.route(match, (route) =>
    route.request().method() === 'POST' ? route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: { code: 'spawn_failed', message: 'could not start the agent process' } }) }) : route.fallback(),
  )
  await dialog.locator('[data-launch-message-input]').fill('say hello')
  await dialog.locator('[data-launch-submit]').click()
  const alert = dialog.locator('[data-launch-error]')
  await expect(alert).toHaveText('Not started: could not start the agent process.')
  await expect(alert).toHaveAttribute('data-launch-error-code', 'spawn_failed')
  await expect(alert).not.toContainText('spawn_failed')
  await expect(dialog.locator('[data-launch-message-input]')).toHaveValue('say hello')
  await page.unroute(match)
})

for (const size of [DAILY, { width: 1440, height: 810 }]) {
  test(`at ${size.width} x ${size.height} the closed dialog's body does not scroll`, async ({ browser }) => {
    const page = await dayPage(browser, { server: own.started, ...size })
    const dialog = await openOnPrompted(page)
    await expect(dialog.locator('[data-more-options]')).toHaveAttribute('aria-expanded', 'false')
    await expect(dialog.locator('[data-capacity-panel] [data-capacity-line]')).toBeVisible()
    const body = dialog.locator('[data-slot="body"]')
    const overflow = await body.evaluate((el) => el.scrollHeight - el.clientHeight)
    expect(overflow, "the closed dialog's body does not scroll").toBeLessThanOrEqual(0.5)
  })
}

test("on a phone every control of the footer is a touch target", async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, path: '/sessions', width: 390, height: 844, touch: true })
  const dialog = await openOnPrompted(page)
  const footer = dialog.locator('[data-slot="footer"]')
  for (const l of [footer.locator('[data-launch-yolo]'), footer.getByRole('button', { name: 'Cancel' }), footer.getByRole('button', { name: 'Launch', exact: true })]) await tapTarget(l)
})


test('each agent keeps its own model and arguments while the dialog is open, and the closed row names what the picked one holds', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started })
  const dialog = await openOnPrompted(page)
  const more = await openMore(dialog)
  const model = more.locator('[data-launch-model-input]')
  const args = more.locator('[data-launch-args]')
  await model.fill('opus')
  await args.fill('--add-dir ../shared')
  // Under the arguments: what it runs, and the model in words.
  const runs = more.locator('[data-launch-runs]')
  await expect(runs).toContainText('--add-dir ../shared')
  await expect(runs).toContainText('· model opus')
  await expect(runs.locator('[data-mono="command"]')).not.toContainText('model opus')
  // Another agent: its own fields, empty; back, the first's again.
  await pick(dialog, PROMPTED_CODEX)
  await expect(model).toHaveValue('')
  await expect(args).toHaveValue('')
  await args.fill('--search')
  await pick(dialog, PROMPTED)
  await expect(model).toHaveValue('opus')
  await expect(args).toHaveValue('--add-dir ../shared')
  // Closed, the row names what the picked agent holds, whole.
  await dialog.locator('[data-more-options]').click()
  await expect(dialog.locator('[data-more-line]')).toHaveText('opus · --add-dir ../shared')
  await pick(dialog, PROMPTED_CODEX)
  await expect(dialog.locator('[data-more-line]')).toHaveText('--search')
})

test("an agent that takes no arguments shows none, and Name's placeholder says what the session will be called", async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started })
  const dialog = await openOnPrompted(page)
  const more = await openMore(dialog)
  const name = more.getByRole('textbox', { name: 'Name', exact: true })
  await expect(name).toHaveAttribute('placeholder', 'Made from the task')
  await dialog.locator('[data-launch-message-input]').fill('fix the build')
  await expect(name).toHaveAttribute('placeholder', 'fix the build')
  await expect(more.locator('[data-launch-args]')).toHaveAttribute('placeholder', 'Flags, as you would type them in a terminal')
  await expect(more.locator('[data-launch-model-input]')).toHaveAttribute('placeholder', "The agent's choice")
  await pick(dialog, PLAIN)
  await expect(name).toHaveAttribute('placeholder', `${PLAIN} and a number`)
  // The built-in shell: no Model, no Extra arguments.
  const shell = dialog.getByRole('radiogroup', { name: 'Agent' }).getByRole('radio', { name: /Shell$/ })
  if (await shell.count()) {
    await shell.click()
    await expect(shell).toHaveAttribute('aria-checked', 'true')
    await expect(more.locator('[data-launch-args]')).toHaveCount(0)
    await expect(more.locator('[data-launch-model]')).toHaveCount(0)
  }
})

test('a model set twice, and Yolo typed by hand, are said before the press, and Launch stays enabled', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started })
  const dialog = await openOnPrompted(page)
  const more = await openMore(dialog)
  const field = more.locator('[data-launch-args-field]')
  await more.locator('[data-launch-model-input]').fill('opus')
  for (const typed of ['--model sonnet', '--model=sonnet', '-m sonnet']) {
    await more.locator('[data-launch-args]').fill(typed)
    await expect(field, typed).toContainText('The model is set here and in Model. Keep one of them.')
  }
  await expect(dialog.locator('[data-launch-submit]')).toBeEnabled()
  await more.locator('[data-launch-model-input]').fill('')
  await expect(field).not.toContainText('The model is set here')
  const recipe = (await own.api.call<{ agents: Array<{ id: string; yolo?: { args?: string[] } }> }>('GET', '/api/catalog')).body?.agents.find((a) => a.id === 'prompted')?.yolo?.args ?? []
  expect(recipe.length, "the prompted stub's yolo recipe").toBeGreaterThan(0)
  await more.locator('[data-launch-args]').fill(recipe.join(' '))
  await expect(field).toContainText('Use the Yolo switch for this.')
  await expect(dialog.locator('[data-launch-submit]')).toBeEnabled()
})

for (const size of [DAILY, { width: 1440, height: 810 }]) {
  test(`at ${size.width} x ${size.height} the dialog with More options open does not scroll, its chips in view`, async ({ browser }) => {
    const page = await dayPage(browser, { server: own.started, ...size })
    const dialog = await openOnPrompted(page)
    const more = await openMore(dialog)
    await more.locator('[data-launch-model-input]').fill('opus')
    await expect(more.locator('[data-launch-runs]')).toBeVisible()
    await expect(dialog.getByRole('radiogroup', { name: 'Agent' })).toBeInViewport({ ratio: 1 })
    const body = dialog.locator('[data-slot="body"]')
    const overflow = await body.evaluate((el) => el.scrollHeight - el.clientHeight)
    expect(overflow, "the open dialog's body does not scroll").toBeLessThanOrEqual(0.5)
  })
}

/** An account of Claude Code's adapter on the spec's server, its folder holding the stub's usage state `line`. */
async function account(label: string, line: string): Promise<string> {
  const dir = mkdtempSync(join(own.home, 'e2e-account-'))
  writeFileSync(join(dir, 'stub-usage'), `${line}\n`)
  const { rev } = await own.api.ok<{ rev: number }>('GET', '/api/accounts')
  return (await own.api.ok<{ account: { id: string } }>('POST', '/api/accounts', { adapter: 'claude', label, folder: dir, expectedRev: rev })).account.id
}

/** Picks an account in the dialog's Account menu, then waits for its menu to be gone. */
async function pickAccount(page: Page, dialog: Locator, name: string) {
  await dialog.locator('[data-account-select]').click()
  await page.getByRole('option', { name, exact: true }).click()
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await expect(dialog.locator('[data-account-select]')).toContainText(name)
}

test('the usage sits beside the account as its figures, opens the full panel in place, and a login near its limit says so in one line', async ({ browser }) => {
  await account('Plenty', 'ok')
  await account('Busy', 'warning')
  const page = await dayPage(browser, { server: own.started })
  const dialog = await openOnPrompted(page)
  const usage = dialog.locator('[data-launch-usage]')
  const note = dialog.locator('[data-launch-note]')
  const panel = dialog.locator('[data-launch-usage-panel]')

  await pickAccount(page, dialog, 'Plenty')
  await expect(usage).toHaveText('5-hour 33% · Weekly, all models 40%', { timeout: 30_000 })
  await expect(note).toHaveCount(0)
  // Pressed, the full panel shows in place under the row, every window with its reset.
  await expect(usage).toHaveAttribute('aria-expanded', 'false')
  await expect(panel).toBeHidden()
  await usage.click()
  await expect(usage).toHaveAttribute('aria-expanded', 'true')
  await expect(panel).toBeVisible()
  await expect(panel.locator('[data-capacity-window]')).toHaveCount(2)
  await expect(panel.locator('[data-capacity-window="weekly_all"]')).toContainText('Weekly, all models: 40% used, resets')
  await usage.click()
  await expect(panel).toBeHidden()

  // Near a limit: the figure in the warning colour, one line under the row, announced, and Launch still enabled.
  await pickAccount(page, dialog, 'Busy')
  await expect(usage).toContainText('Weekly, all models 92%', { timeout: 30_000 })
  await expect(note).toHaveText(/^Busy is near a limit: Weekly, all models 92% used, resets \w{3} \d{1,2} \w{3} \d\d:\d\d\. The agent may stop before then\.$/)
  await expect(note).toHaveAttribute('role', 'status')
  await expect(note).toHaveAttribute('data-launch-note-tone', 'warning')
  const hot = usage.locator('span.text-warning')
  await expect(hot).toHaveText('Weekly, all models 92%')
  await expect(dialog.locator('[data-launch-submit]')).toBeEnabled()

  // An agent no reader knows shows no usage.
  await pick(dialog, PLAIN)
  await expect(usage).toHaveCount(0)
  await expect(note).toHaveCount(0)
})

test('after a launch the dialog remembers the agent, the folder, its model and the fold, across a reload, and never the rest', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started })
  const work = join(own.root, 'remembered')
  mkdirSync(work, { recursive: true })
  let dialog = await openLaunch(page)
  await pick(dialog, PROMPTED)
  await dialog.getByLabel('Folder', { exact: true }).fill(work)
  const more = await openMore(dialog)
  await more.locator('[data-launch-model-input]').fill('claude-e2e-model')
  await more.locator('[data-launch-args]').fill('--add-dir ../secret-key')
  await more.getByRole('textbox', { name: 'Name', exact: true }).fill('remembered launch')
  await dialog.locator('[data-launch-message-input]').fill('say hello')
  const answered = page.waitForResponse((r) => r.request().method() === 'POST' && new URL(r.url()).pathname === '/api/sessions')
  await dialog.locator('[data-launch-submit]').click()
  const answer = await answered
  if (answer.status() === 201) made.push(((await answer.json()) as { id: string }).id)
  expect(answer.status(), 'the launch made a session').toBe(201)
  await expect(page).toHaveURL(/\/sessions\//)
  // Kept once the server took it: read after the page moved, through a poll.
  const kept = () => page.evaluate(() => localStorage.getItem('conductor.launch.last') ?? '')
  await expect.poll(kept).toContain('claude-e2e-model')
  const raw = await kept()
  for (const never of ['secret-key', 'say hello', 'remembered launch', 'yolo', 'account']) expect(raw, never).not.toContain(never)

  await page.reload()
  await expect(page.locator('#dashboard-sidebar-main')).toBeVisible()
  dialog = await openLaunch(page)
  await expect(dialog.getByRole('radio', { name: re(PROMPTED) })).toHaveAttribute('aria-checked', 'true')
  await expect(dialog.getByLabel('Folder', { exact: true })).toHaveValue(work)
  // The fold was left open: it opens so; its model is back, and the names typed are offered.
  await expect(dialog.locator('[data-more-options]')).toHaveAttribute('aria-expanded', 'true')
  await expect(dialog.locator('[data-launch-model-input]')).toHaveValue('claude-e2e-model')
  await expect(dialog.locator('[data-launch-model-names] option')).toHaveAttribute('value', 'claude-e2e-model')
  await expect(dialog.locator('[data-launch-args]')).toHaveValue('')
  await dialog.locator('[data-more-options]').click()
  await expect(dialog.locator('[data-more-line]')).toHaveText('claude-e2e-model')
  await dialog.getByRole('button', { name: 'Cancel' }).click()
  await expect(dialog).toHaveCount(0)

  // A remembered folder the server refuses is said under Folder, never changed quietly.
  await page.evaluate(() => {
    const v = JSON.parse(localStorage.getItem('conductor.launch.last') ?? '{}')
    localStorage.setItem('conductor.launch.last', JSON.stringify({ ...v, folder: '/' }))
  })
  await page.reload()
  await expect(page.locator('#dashboard-sidebar-main')).toBeVisible()
  dialog = await openLaunch(page)
  const folder = dialog.getByLabel('Folder', { exact: true })
  await expect(folder).toHaveValue('/')
  await dialog.locator('[data-launch-submit]').click()
  await expect(dialog.locator('[data-launch-folder-refused]')).toHaveText("Agents can't use this folder yet.")
  await expect(folder).toHaveValue('/')
})

