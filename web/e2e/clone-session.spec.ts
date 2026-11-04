import { spawn, type ChildProcess } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { openSync } from 'node:fs'
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
let host: ChildProcess | null = null
let hostName = ''
let ctx: BrowserContext | null = null

test.afterAll(async ({ api }) => {
  try {
    await ctx?.close()
    await endHosted(api, hostName, host)
  } finally {
    for (const id of sessions) await api.stopSession(id)
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
