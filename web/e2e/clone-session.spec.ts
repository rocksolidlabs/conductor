import { spawn, type ChildProcess } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { mkdirSync, openSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import type { BrowserContext, Locator, Page } from '@playwright/test'
import { endHosted, expect, hostedName, hostedSession, test, type Session } from './fixtures'
import { serverBinary, stubBash, stubPath } from './server'

// Cloning a session (#434): Clone… on a row's menu (More, a right-click, the phone's sheet) starts the same session again under
// another name, refusing one in use; Clone with options… opens the Launch dialog filled in with the source's launch, and a field
// changed launches as changed. A hosted session's entries are disabled with the reason. Every name is this run's own: a session an
// earlier repetition left listed holds its name, ended or not.
test.describe.configure({ mode: 'serial' })

const sessions: string[] = []
const runs: string[] = []
const crews: string[] = []
const agents: string[] = []
let host: ChildProcess | null = null
let hostName = ''
let ctx: BrowserContext | null = null

test.afterAll(async ({ api }) => {
  try {
    await ctx?.close()
    await endHosted(api, hostName, host)
  } finally {
    for (const id of runs) await api.stopRun(id)
    for (const id of sessions) await api.stopSession(id)
    for (const id of crews) await api.call('DELETE', `/api/crews/${encodeURIComponent(id)}`)
    // An agent a test saved leaves the catalog, for the specs after this one (a test that ran through has removed it already).
    for (const id of agents) await api.call('DELETE', `/api/catalog/${encodeURIComponent(id)}`)
  }
})

/** A name no session of an earlier repetition has. */
const unique = (base: string) => `${base} ${randomUUID().slice(0, 6)}`

async function launch(api: { ok: <T>(method: string, path: string, body?: unknown) => Promise<T> }, body: Record<string, unknown>): Promise<Session> {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', ...body })
  sessions.push(s.id)
  return s
}

/** What a session was launched with, as the server keeps it for an admin (GET /api/sessions/{id}'s `launch`). */
async function launched(api: { ok: <T>(method: string, path: string, body?: unknown) => Promise<T> }, s: Session) {
  return (await api.ok<{ launch: { agentId: string; cwd: string; args: string[]; yolo: boolean } }>('GET', `/api/sessions/${s.id}`)).launch
}

function row(page: Page, s: Session): Locator {
  return page.locator(`[data-session-list] [data-sidebar-row="s:${s.id}"]`).first()
}

/** The id of the session page a URL is on, or ''. */
const pageOf = (u: URL) => (u.pathname.startsWith('/sessions/') ? decodeURIComponent(u.pathname.slice('/sessions/'.length)) : '')

/** The session the page opened, once it is on its page: one this spec has not made or opened before, noted to be stopped. */
async function opened(page: Page, api: { session: (id: string) => Promise<Session> }): Promise<Session> {
  await page.waitForURL((u) => pageOf(u) !== '' && !sessions.includes(pageOf(u)), { timeout: 15_000 })
  const id = pageOf(new URL(page.url()))
  sessions.push(id)
  return api.session(id)
}

/** Opens Clone with options… from the row's More menu, and returns the dialog once it is the clone of `s`. */
async function withOptions(page: Page, s: Session): Promise<Locator> {
  const r = row(page, s)
  await expect(r).toBeVisible()
  await r.hover()
  await r.locator('[data-row-more]').click()
  await page.getByRole('menuitem', { name: /^Clone with options…/ }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.locator(`[data-launch-clone="${s.id}"]`)).toBeVisible()
  return dialog
}

/** Opens Clone… from the row's More menu, and returns its dialog. */
async function quickClone(page: Page, s: Session): Promise<Locator> {
  const r = row(page, s)
  await expect(r).toBeVisible()
  await r.hover()
  await r.locator('[data-row-more]').click()
  await page.getByRole('menuitem', { name: /^Clone…/ }).click()
  const dialog = page.locator('[data-clone-dialog]')
  await expect(dialog).toBeVisible()
  return dialog
}

/** The listed session named `name`, once there is one, noted to be stopped. */
async function named(api: { ok: <T>(method: string, path: string, body?: unknown) => Promise<T> }, name: string): Promise<Session> {
  let found: Session | undefined
  await expect
    .poll(async () => {
      found = (await api.ok<{ sessions: Session[] }>('GET', '/api/sessions')).sessions.find((x) => x.name === name)
      return !!found
    }, { message: `a session named ${name}` })
    .toBe(true)
  sessions.push(found!.id)
  return found!
}

/**
 * A name the dialogs take as free and the server refuses as `taken`'s (#434's second question for the owner): the long s (ſ) is its
 * own lower case to JavaScript and folds to s in Go's strings.EqualFold. `taken`'s name holds " s " once (unique's hex has no s).
 */
const foldedName = (taken: Session) => taken.name.replace(' s ', ' ſ ')

test("Clone… from More and from a right-click refuses a name in use, then opens a clone in the source's directory with its agent and yolo", async ({ page, api }) => {
  const source = await launch(api, { name: unique('e2e clone src'), args: ['--model', 'opus'], yolo: true })
  const taken = await launch(api, { name: unique('e2e clone taken') })
  await page.goto('/crews')
  const r = row(page, source)
  await expect(r).toContainText(source.name)
  await expect(r.locator('[data-yolo-badge]')).toBeVisible()
  await r.hover()
  await r.locator('[data-row-more]').click()
  await page.getByRole('menuitem', { name: /^Clone…/ }).click()
  const dialog = page.locator('[data-clone-dialog]')
  const field = dialog.getByRole('textbox', { name: "Clone's name" })
  const save = dialog.locator('[data-clone-save]')
  await expect(page.getByRole('dialog')).toContainText(`Clone ${source.name}`)
  // Empty: not sent, and said why.
  await expect(field).toHaveValue('')
  await expect(save).toBeDisabled()
  await expect(dialog).toContainText('Give the clone a name no session has')
  // A name in use, the source's or another's, ignoring case: not sent, and said why.
  await field.fill(source.name.toUpperCase())
  await expect(dialog).toContainText(`A session is named ${source.name.toUpperCase()} already`)
  await expect(save).toBeDisabled()
  await field.fill(` ${taken.name} `)
  await expect(save).toBeDisabled()
  const name = unique('e2e clone twin')
  await field.fill(name)
  await expect(save).toBeEnabled()
  await save.click()
  const clone = await opened(page, api)
  await expect(page.getByRole('dialog')).toHaveCount(0)
  // The toast names both sessions.
  const toast = page.locator('li').filter({ hasText: `Cloned ${source.name}` }).last()
  await expect(toast).toBeVisible()
  await expect(toast).toContainText(name)
  expect(clone).toMatchObject({ name, agentId: 'claude', cwd: source.cwd, yolo: true })
  expect(await launched(api, clone)).toEqual({ agentId: 'claude', cwd: source.cwd, args: ['--model', 'opus'], yolo: true })
  expect(clone.agentSession?.id).not.toBe(source.agentSession?.id)
  await expect(row(page, clone).locator('[data-yolo-badge]')).toBeVisible()
  // The source is as it was.
  expect(await api.session(source.id)).toMatchObject({ name: source.name, status: 'running' })

  // From a right-click: the same menu, and the clone's own name is now one in use.
  await row(page, source).click({ button: 'right' })
  await page.getByRole('menuitem', { name: /^Clone…/ }).click()
  await field.fill(name)
  await expect(dialog).toContainText('already')
  await expect(save).toBeDisabled()
  const second = unique('e2e clone right')
  await field.fill(second)
  await field.press('Enter')
  const again = await opened(page, api)
  expect(again).toMatchObject({ name: second, cwd: source.cwd, yolo: true })
  expect((await launched(api, again)).args).toEqual(['--model', 'opus'])
})

test('Clone with options… opens the Launch dialog filled in, yolo as the source chose it; a changed field launches as changed', async ({ page, api }) => {
  // Yolo on where the server's default is off: the switch shows the source's choice, not the default.
  const source = await launch(api, { name: unique('e2e clone opts'), args: ['--model', 'opus', 'two words'], yolo: true })
  await page.goto('/crews')
  const r = row(page, source)
  await expect(r).toContainText(source.name)
  await r.hover()
  await r.locator('[data-row-more]').click()
  await page.getByRole('menuitem', { name: /^Clone with options…/ }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toContainText(`Clone ${source.name}`)
  const form = dialog.locator(`[data-launch-clone="${source.id}"]`)
  await expect(form).toBeVisible()
  await expect(dialog.getByRole('radio', { name: /Claude Code/ })).toHaveAttribute('aria-checked', 'true', { timeout: 10_000 })
  // On this server, never asked.
  await expect(dialog.locator('[data-runs-on]')).toHaveCount(0)
  await expect(dialog.locator('[data-dir-input] input[role="combobox"]')).toHaveValue(source.cwd!)
  const args = dialog.locator('[data-launch-args]')
  await expect(args).toHaveValue('--model opus "two words"')
  await expect(dialog.locator('[data-yolo-switch]')).toHaveAttribute('aria-checked', 'true')
  const name = dialog.locator('[data-clone-name]')
  const submit = dialog.locator('[data-launch-clone-submit]')
  await expect(name).toHaveValue('')
  await expect(submit).toBeDisabled()
  await name.fill(source.name)
  await expect(dialog).toContainText('already')
  await expect(submit).toBeDisabled()
  // The arguments changed, the rest left as the source's.
  await args.fill('--model sonnet')
  const cloneName = unique('e2e clone sonnet')
  await name.fill(cloneName)
  await expect(submit).toBeEnabled()
  await submit.click()
  const clone = await opened(page, api)
  expect(clone).toMatchObject({ name: cloneName, agentId: 'claude', cwd: source.cwd, yolo: true })
  expect(await launched(api, clone)).toEqual({ agentId: 'claude', cwd: source.cwd, args: ['--model', 'sonnet'], yolo: true })

  // Left alone, the arguments launch as recorded; yolo turned off launches without it.
  await page.goto('/crews')
  await row(page, source).hover()
  await row(page, source).locator('[data-row-more]').click()
  await page.getByRole('menuitem', { name: /^Clone with options…/ }).click()
  await expect(args).toHaveValue('--model opus "two words"')
  await dialog.locator('[data-yolo-switch]').click()
  await expect(dialog.locator('[data-yolo-switch]')).toHaveAttribute('aria-checked', 'false')
  const plain = unique('e2e clone plain')
  await name.fill(plain)
  await submit.click()
  const other = await opened(page, api)
  expect(other).toMatchObject({ name: plain, cwd: source.cwd })
  expect(other.yolo ?? false).toBe(false)
  expect(await launched(api, other)).toEqual({ agentId: 'claude', cwd: source.cwd, args: ['--model', 'opus', 'two words'], yolo: false })
})

test("the palette's Launch another like opens the same dialog, filled in", async ({ page, api }) => {
  const source = await launch(api, { name: unique('e2e clone palette'), args: ['--model', 'opus'], yolo: false })
  await page.goto(`/sessions/${source.id}`)
  await expect(page.locator('[data-session-title]')).toHaveText(source.name)
  // Alt+/, the terminal's way to the palette, pressed once the layout listens: its sidebar's root is in the page.
  await expect(page.locator('#dashboard-sidebar-main')).toBeAttached()
  await page.keyboard.press('Alt+/')
  const palette = page.locator('[data-palette]')
  await expect(palette).toBeVisible()
  await palette.locator('input[data-palette-input]').fill(`Launch another like ${source.name}`)
  const like = palette.locator(`[data-palette-row="action:launch-like:${source.id}"]`)
  await expect(like).toBeVisible()
  await like.click()
  await expect(palette).toBeHidden()
  const dialog = page.getByRole('dialog')
  await expect(dialog.locator(`[data-launch-clone="${source.id}"]`)).toBeVisible()
  await expect(dialog.locator('[data-launch-args]')).toHaveValue('--model opus')
  await expect(dialog.locator('[data-yolo-switch]')).toHaveAttribute('aria-checked', 'false')
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)
})

test("a hosted row's entries are disabled, with the reason", async ({ page, api, state }) => {
  const logFile = join(state.root, `clone-host-${randomUUID()}.log`)
  const log = openSync(logFile, 'a')
  hostName = hostedName('clone-hosted')
  const bash = stubBash()
  host = spawn(serverBinary(), ['host', '--server', state.baseURL, '--token', state.hostToken, '--host-name', 'e2e-laptop', '--name', hostName, '--agent', 'claude', '--cwd', state.home, '--relay-only', '--no-local', '--', bash.command, stubPath, '--session-id', randomUUID()], {
    env: { PATH: bash.path, LANG: 'C.UTF-8', HOME: state.home, STUB_IDENTITY: 'claude', STUB_REPORT: 'plain' },
    stdio: ['ignore', log, log],
  })
  const hosted = await hostedSession(api, logFile, hostName)
  await page.goto('/crews')
  const r = row(page, hosted)
  await expect(r).toBeVisible()
  await r.hover()
  await r.locator('[data-row-more]').click()
  const reason = 'Runs on e2e-laptop: Conductor cannot start a session there yet'
  for (const label of [/^Clone…/, /^Clone with options…/]) {
    const item = page.getByRole('menuitem', { name: label })
    await expect(item).toHaveAttribute('data-disabled', '')
    await expect(item).toContainText(reason)
  }
  await page.keyboard.press('Escape')
  // And the server refuses one all the same.
  const refused = await api.call<{ error?: { code: string } }>('POST', `/api/sessions/${hosted.id}/clone`, { name: unique('e2e hosted clone') })
  expect(refused.status).toBe(400)
  expect(refused.body.error?.code).toBe('hosted_session')
})

test("the phone's sheet runs both entries", async ({ browser, api, state }) => {
  ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })
  await ctx.addInitScript(({ token }) => localStorage.setItem('conductor.workbenchToken', token), { token: state.token })
  const phone = await ctx.newPage()
  const source = await launch(api, { name: unique('e2e clone phone'), args: ['--model', 'opus'] })
  await phone.goto('/sessions')
  const r = phone.locator(`[data-session-list="page"] [data-sidebar-row="s:${source.id}"]`).first()
  await expect(r).toBeVisible({ timeout: 15_000 })
  const press = async () => {
    const target = r.locator('a[href]').first()
    await target.dispatchEvent('pointerdown', { pointerType: 'touch', isPrimary: true, bubbles: true })
    // The row's long press is 500 ms of the pointer held: the sheet it opens is what the test waits for.
    await expect(phone.locator('[data-row-sheet]')).toBeVisible()
    await target.dispatchEvent('pointerup', { pointerType: 'touch', isPrimary: true, bubbles: true })
  }
  await press()
  const sheet = phone.locator('[data-row-sheet]')
  await sheet.locator('[data-row-sheet-item="Clone with options…"]').tap()
  await expect(sheet).toBeHidden()
  const dialog = phone.getByRole('dialog')
  await expect(dialog.locator(`[data-launch-clone="${source.id}"]`)).toBeVisible()
  await expect(dialog.locator('[data-launch-args]')).toHaveValue('--model opus')
  await dialog.getByRole('button', { name: 'Cancel' }).tap()
  await expect(dialog).toHaveCount(0)
  await press()
  await sheet.locator('[data-row-sheet-item="Clone…"]').tap()
  await expect(sheet).toBeHidden()
  const name = unique('e2e clone phoned')
  await phone.locator('[data-clone-input]').fill(name)
  await phone.locator('[data-clone-save]').tap()
  const clone = await opened(phone, api)
  expect(clone).toMatchObject({ name, cwd: source.cwd })
  expect((await launched(api, clone)).args).toEqual(['--model', 'opus'])
})

test("Clone with options… keeps arguments its agent takes no more in sight, and says when the source's agent is not offered", async ({ page, api }) => {
  const id = `e2e-clone-gone-${randomUUID().slice(0, 6)}`
  agents.push(id)
  const agent = { id, name: `E2E clone gone ${id.slice(-6)}`, command: ['/bin/cat'], allowArgs: true }
  await api.ok('POST', '/api/catalog', agent)
  const source = await launch(api, { agentId: id, name: unique('e2e clone gone'), args: ['-u'] })
  // Its arguments turned off since the launch: the recorded ones are shown, refused by the server as they are, and cleared, none.
  await api.ok('POST', '/api/catalog', { ...agent, allowArgs: false })
  await page.goto('/crews')
  let dialog = await withOptions(page, source)
  await expect(dialog.getByRole('radio', { name: agent.name })).toHaveAttribute('aria-checked', 'true')
  const args = dialog.locator('[data-launch-args]')
  await expect(args).toHaveValue('-u')
  await expect(dialog).toContainText('Recorded at its launch')
  const name = dialog.locator('[data-clone-name]')
  const submit = dialog.locator('[data-launch-clone-submit]')
  await name.fill(unique('e2e clone refused'))
  await submit.click()
  await expect(dialog.locator('[data-launch-error]')).toContainText('args_not_allowed')
  await args.fill('')
  const bare = unique('e2e clone bare')
  await name.fill(bare)
  await submit.click()
  const clone = await opened(page, api)
  expect(clone.name).toBe(bare)
  expect(await launched(api, clone)).toEqual({ agentId: id, cwd: source.cwd, args: [], yolo: false })

  // Not installed now. The page's cached catalog, from the opening before, still offers it; this opening's own read does not.
  await api.ok('POST', '/api/catalog', { ...agent, allowArgs: false, command: ['/nonexistent-e2e/cat'] })
  await expect
    .poll(async () => (await api.ok<{ agents: Array<{ id: string; available: boolean }> }>('GET', '/api/catalog')).agents.find((a) => a.id === id)?.available)
    .toBe(false)
  const notOffered = async () => {
    await expect(dialog.locator('[data-clone-missing-agent]')).toContainText(`${id}, the agent it was launched with, is not offered on this server`)
    await expect(dialog.getByRole('radio', { name: agent.name })).toHaveCount(0)
    await expect(dialog.getByRole('radio', { checked: true })).toHaveCount(0)
    await dialog.locator('[data-clone-name]').fill(unique('e2e clone none'))
    await expect(dialog.locator('[data-launch-clone-submit]')).toBeDisabled()
    await page.keyboard.press('Escape')
    await expect(dialog).toHaveCount(0)
  }
  dialog = await withOptions(page, source)
  await notOffered()
  // Gone from the catalog.
  await api.ok('DELETE', `/api/catalog/${id}`)
  dialog = await withOptions(page, source)
  await notOffered()
})

test("both dialogs warn of a run's worktree, and say the server's refusals: a name it holds taken, a directory gone", async ({ page, api, state }) => {
  const dir = join(state.repo, '.conductor', 'worktrees', `e2e-clone-${randomUUID().slice(0, 6)}`, 'build.1')
  mkdirSync(dir, { recursive: true })
  const source = await launch(api, { name: unique('e2e clone tree'), cwd: dir })
  const taken = await launch(api, { name: unique('e2e clone s') })
  await page.goto('/crews')
  const warning = "This is a run's worktree: two agents will work in one working tree."

  // Clone…: the warning, and a name the server holds taken although this page's list has none like it.
  let quick = await quickClone(page, source)
  await expect(quick.locator('[data-clone-shared-tree]')).toHaveText(warning)
  await quick.locator('[data-clone-input]').fill(foldedName(taken))
  await expect(quick.locator('[data-clone-save]')).toBeEnabled()
  await quick.locator('[data-clone-save]').click()
  await expect(quick).toContainText(`a session is named ${foldedName(taken)} already`)
  await page.keyboard.press('Escape')
  await expect(quick).toHaveCount(0)

  // Clone with options…: the same.
  let dialog = await withOptions(page, source)
  await expect(dialog.locator('[data-clone-shared-tree]')).toHaveText(warning)
  await expect(dialog.locator('[data-clone-attempt]')).toHaveCount(0)
  await expect(dialog.getByRole('radio', { name: /Claude Code/ })).toHaveAttribute('aria-checked', 'true')
  // The directory cleared, the clone runs in the source's, so the warning stays (review round 2); another directory has none.
  const cwd = dialog.locator('[data-dir-input] input[role="combobox"]')
  await cwd.fill('')
  await expect(dialog.locator('[data-clone-shared-tree]')).toHaveText(warning)
  await cwd.fill(state.repo)
  await expect(dialog.locator('[data-clone-shared-tree]')).toHaveCount(0)
  await cwd.fill('')
  await expect(dialog.locator('[data-clone-shared-tree]')).toHaveText(warning)
  await dialog.locator('[data-clone-name]').click()
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await dialog.locator('[data-clone-name]').fill(foldedName(taken))
  await expect(dialog.locator('[data-clone-name]')).toHaveValue(foldedName(taken))
  await dialog.locator('[data-launch-clone-submit]').click()
  await expect(dialog.locator('[data-launch-error]')).toContainText(`a session is named ${foldedName(taken)} already`)
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)

  // The directory removed: each dialog says the server's invalid_cwd, with no other directory taken instead.
  rmSync(dir, { recursive: true, force: true })
  quick = await quickClone(page, source)
  await quick.locator('[data-clone-input]').fill(unique('e2e clone nodir'))
  await quick.locator('[data-clone-save]').click()
  await expect(quick).toContainText('The directory cannot be used')
  await page.keyboard.press('Escape')
  await expect(quick).toHaveCount(0)
  dialog = await withOptions(page, source)
  await expect(dialog.getByRole('radio', { name: /Claude Code/ })).toHaveAttribute('aria-checked', 'true')
  await dialog.locator('[data-clone-name]').fill(unique('e2e clone nodir'))
  await dialog.locator('[data-launch-clone-submit]').click()
  await expect(dialog.locator('[data-launch-error]')).toContainText('The directory cannot be used')
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)
})

test("a Yardmaster attempt's Clone… is disabled with the reason, and Clone with options… warns of the attempt", async ({ page, api }) => {
  const crew = await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
    name: unique('e2e clone attempt'),
    goal: 'an attempt to clone',
    cwd: '',
    where: 'server',
    isolation: 'worktree',
    openAfterLaunch: false,
    yolo: null,
    // The stub as a plain agent: it never submits, so the attempt stays running.
    members: [{ name: 'builder', agentId: 'claude', prompt: '', start: { when: 'task' } }],
    tasks: [{ id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.' }],
  })
  crews.push(crew.crew.id)
  const run = await api.launchCrew(crew.crew.id)
  runs.push(run.id)
  let attempt: Session | undefined
  await expect
    .poll(async () => {
      const listed = (await api.ok<{ sessions: Array<Session & { crew?: { runId: string; task?: string } }> }>('GET', '/api/sessions')).sessions
      attempt = listed.find((s) => s.crew?.runId === run.id && s.crew.task === 'build')
      return !!attempt
    }, { timeout: 60_000, message: "the task's attempt" })
    .toBe(true)
  await page.goto('/crews')
  const r = row(page, attempt!)
  await expect(r).toBeVisible()
  await r.hover()
  await r.locator('[data-row-more]').click()
  const quick = page.getByRole('menuitem', { name: /^Clone…/ })
  await expect(quick).toHaveAttribute('data-disabled', '')
  await expect(quick).toContainText("A Yardmaster attempt's worktree must stay clean apart from .conductor/: use Clone with options…")
  await page.getByRole('menuitem', { name: /^Clone with options…/ }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.locator(`[data-launch-clone="${attempt!.id}"]`)).toBeVisible()
  await expect(dialog.locator('[data-clone-attempt]')).toContainText('This session is a Yardmaster attempt')
  await expect(dialog.locator('[data-clone-shared-tree]')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)
})

test("a yolo choice moved before the catalog answers is the clone's, whatever default the answer brings", async ({ page, api }) => {
  const source = await launch(api, { name: unique('e2e clone early'), yolo: true })
  await page.goto('/crews')
  await expect(row(page, source)).toBeVisible()
  // The dialog's catalog read held, and its answer's default turned on: the switch is moved while the dialog knows no default.
  let release!: () => void
  const held = new Promise<void>((resolve) => (release = resolve))
  let asked!: () => void
  const reading = new Promise<void>((resolve) => (asked = resolve))
  await page.route('**/api/catalog', async (route) => {
    if (route.request().method() !== 'GET') return route.continue()
    asked()
    await held
    const res = await route.fetch()
    await route.fulfill({ response: res, json: { ...(await res.json()), yoloDefault: true } })
  })
  const dialog = await withOptions(page, source)
  await reading
  const yolo = dialog.locator('[data-yolo-switch]')
  await expect(yolo).toHaveAttribute('aria-checked', 'true')
  await yolo.click()
  await expect(yolo).toHaveAttribute('aria-checked', 'false')
  release()
  await expect(dialog.getByRole('radio', { name: /Claude Code/ })).toHaveAttribute('aria-checked', 'true')
  await expect(yolo).toHaveAttribute('aria-checked', 'false')
  const name = unique('e2e clone early off')
  await dialog.locator('[data-clone-name]').fill(name)
  await dialog.locator('[data-launch-clone-submit]').click()
  const clone = await opened(page, api)
  await page.unroute('**/api/catalog')
  expect(clone.name).toBe(name)
  expect((await launched(api, clone)).yolo).toBe(false)
})

test("the palette's Clone with options… of another session, over the open dialog, is that session's", async ({ page, api }) => {
  const first = await launch(api, { name: unique('e2e clone first'), args: ['--model', 'opus'] })
  const second = await launch(api, { name: unique('e2e clone second'), args: ['--model', 'haiku'], yolo: true })
  // On the second's page, whose palette offers Launch another like it; the first's dialog opened from its row.
  await page.goto(`/sessions/${second.id}`)
  await expect(page.locator('[data-session-title]')).toHaveText(second.name)
  await expect(page.locator('#dashboard-sidebar-main')).toBeAttached()
  let dialog = await withOptions(page, first)
  await expect(dialog.locator('[data-launch-args]')).toHaveValue('--model opus')
  await page.keyboard.press('Alt+/')
  const palette = page.locator('[data-palette]')
  await expect(palette).toBeVisible()
  await palette.locator('input[data-palette-input]').fill(`Launch another like ${second.name}`)
  const like = palette.locator(`[data-palette-row="action:launch-like:${second.id}"]`)
  await expect(like).toBeVisible()
  // The keyboard's way: the open dialog's overlay takes the pointer.
  await like.press('Enter')
  await expect(palette).toBeHidden()
  dialog = page.getByRole('dialog')
  await expect(dialog.locator(`[data-launch-clone="${second.id}"]`)).toBeVisible()
  await expect(dialog).toContainText(`Clone ${second.name}`)
  await expect(dialog.locator('[data-launch-args]')).toHaveValue('--model haiku')
  await expect(dialog.locator('[data-yolo-switch]')).toHaveAttribute('aria-checked', 'true')
  const name = unique('e2e clone of second')
  await dialog.locator('[data-clone-name]').fill(name)
  await dialog.locator('[data-launch-clone-submit]').click()
  const clone = await opened(page, api)
  expect(clone.name).toBe(name)
  expect(await launched(api, clone)).toEqual({ agentId: 'claude', cwd: second.cwd, args: ['--model', 'haiku'], yolo: true })
})

test("Clone with options… of one session, then of another, is the other's when the first's read answers last", async ({ page, api }) => {
  const first = await launch(api, { name: unique('e2e clone slow'), args: ['--model', 'opus'] })
  const second = await launch(api, { name: unique('e2e clone quick'), args: ['--model', 'haiku'] })
  await page.goto('/crews')
  await expect(row(page, first)).toBeVisible()
  // The first's read of its launch held until the second's dialog is open and typed in (review round 2).
  let release!: () => void
  const held = new Promise<void>((resolve) => (release = resolve))
  let asked!: () => void
  const reading = new Promise<void>((resolve) => (asked = resolve))
  await page.route(`**/api/sessions/${first.id}`, async (route) => {
    if (route.request().method() !== 'GET') return route.continue()
    asked()
    await held
    await route.continue()
  })
  const r = row(page, first)
  await r.hover()
  await r.locator('[data-row-more]').click()
  await page.getByRole('menuitem', { name: /^Clone with options…/ }).click()
  await reading
  const dialog = await withOptions(page, second)
  await expect(dialog.getByRole('radio', { name: /Claude Code/ })).toHaveAttribute('aria-checked', 'true')
  const name = unique('e2e clone of quick')
  await dialog.locator('[data-clone-name]').fill(name)
  const answered = page.waitForResponse((res) => res.url().endsWith(`/api/sessions/${first.id}`) && res.request().method() === 'GET')
  release()
  await (await answered).finished()
  await expect(dialog.locator(`[data-launch-clone="${second.id}"]`)).toBeVisible()
  await expect(dialog.locator('[data-launch-args]')).toHaveValue('--model haiku')
  await expect(dialog.locator('[data-clone-name]')).toHaveValue(name)
  await dialog.locator('[data-launch-clone-submit]').click()
  const clone = await opened(page, api)
  await page.unroute(`**/api/sessions/${first.id}`)
  expect(clone.name).toBe(name)
  expect(await launched(api, clone)).toEqual({ agentId: 'claude', cwd: second.cwd, args: ['--model', 'haiku'], yolo: false })
})

test("a clone's answer that comes after its dialog was cancelled leaves the next dialog alone", async ({ page, api }) => {
  const first = await launch(api, { name: unique('e2e clone late') })
  const second = await launch(api, { name: unique('e2e clone next') })
  const taken = await launch(api, { name: unique('e2e clone s') })
  await page.goto('/crews')
  // Each clone request held until the test lets it go, once the dialog it was sent from is cancelled and another open.
  let release: () => void = () => {}
  let sent: () => void = () => {}
  await page.route('**/api/sessions/*/clone', async (route) => {
    const held = new Promise<void>((resolve) => (release = resolve))
    sent()
    await held
    await route.continue()
  })
  const round = async (cloneName: string) => {
    const dialog = await withOptions(page, first)
    await expect(dialog.getByRole('radio', { name: /Claude Code/ })).toHaveAttribute('aria-checked', 'true')
    await dialog.locator('[data-clone-name]').fill(cloneName)
    const posted = new Promise<void>((resolve) => (sent = resolve))
    await dialog.locator('[data-launch-clone-submit]').click()
    await posted
    await dialog.getByRole('button', { name: 'Cancel' }).click()
    await expect(dialog).toHaveCount(0)
    const next = await withOptions(page, second)
    await expect(next.getByRole('radio', { name: /Claude Code/ })).toHaveAttribute('aria-checked', 'true')
    const answered = page.waitForResponse((r) => r.url().includes(`/api/sessions/${first.id}/clone`))
    release()
    await (await answered).finished()
    return next
  }
  // Refused (the name the server holds taken): the refusal is not said in the next dialog. The next dialog's own check of a name,
  // drawn after the answer came, is the page's work after it.
  let next = await round(foldedName(taken))
  await next.locator('[data-clone-name]').fill(second.name)
  await expect(next).toContainText(`A session is named ${second.name} already`)
  await expect(next.locator(`[data-launch-clone="${second.id}"]`)).toBeVisible()
  await expect(next.locator('[data-launch-error]')).toHaveCount(0)
  await page.keyboard.press('Escape')
  await expect(next).toHaveCount(0)
  // Made: its toast is drawn in the flush that would have closed the next dialog; the next stays open, its Clone not spinning.
  const made = unique('e2e clone late made')
  next = await round(made)
  await named(api, made)
  await expect(page.locator('li').filter({ hasText: `Cloned ${first.name}` }).last()).toContainText(made)
  await expect(next).toHaveAttribute('data-state', 'open')
  await expect(next.locator(`[data-launch-clone="${second.id}"]`)).toBeVisible()
  await expect(next.locator('[data-launch-error]')).toHaveCount(0)
  await next.locator('[data-clone-name]').fill(unique('e2e clone next made'))
  await expect(next.locator('[data-launch-clone-submit]')).toBeEnabled()
  await page.unroute('**/api/sessions/*/clone')
  await page.keyboard.press('Escape')
  await expect(next).toHaveCount(0)
})
