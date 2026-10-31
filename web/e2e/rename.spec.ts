import type { Page } from '@playwright/test'
import { expect, test, type Session } from './fixtures'

// Renaming a server session (#318): from a sidebar row's More menu, from the palette's row (the same descriptors, #302) and from
// the session page's menu, through the one dialog. The name shows at once wherever the session does (the events stream), a name
// with nothing left or past 120 bytes is not sent, a rename made elsewhere reaches an open page, and a resume starts under the
// new name.
test.describe.configure({ mode: 'serial' })

const sessions: string[] = []

test.afterAll(async ({ api }) => {
  for (const id of sessions) await api.stopSession(id)
})

async function launch(api: { ok: <T>(method: string, path: string, body?: unknown) => Promise<T> }, name: string): Promise<Session> {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name })
  sessions.push(s.id)
  return s
}

/** Opens the Rename dialog from the session's sidebar row's More menu and returns the dialog, its field and its save button. */
async function renameFromRow(page: Page, s: Session) {
  const row = page.locator(`[data-session-list] [data-sidebar-row="s:${s.id}"]`).first()
  await expect(row).toContainText(s.name)
  await row.hover()
  await row.locator('[data-row-more]').click()
  await page.getByRole('menuitem', { name: /^Rename/ }).click()
  const dialog = page.getByRole('dialog')
  const field = dialog.getByRole('textbox', { name: 'Session name' })
  await expect(field).toHaveValue(s.name)
  return { dialog, field, save: dialog.locator('[data-rename-save]') }
}

test('a sidebar row renames its session through the dialog, which sends only a name the server keeps whole', async ({ page, api }) => {
  const s = await launch(api, 'e2e rename row')
  await page.goto('/crews')
  const row = page.locator(`[data-session-list] [data-sidebar-row="s:${s.id}"]`).first()
  await expect(row).toContainText('e2e rename row')
  await row.hover()
  await row.locator('[data-row-more]').click()
  await page.getByRole('menuitem', { name: /^Rename/ }).click()
  const dialog = page.getByRole('dialog')
  const field = dialog.getByRole('textbox', { name: 'Session name' })
  const save = dialog.locator('[data-rename-save]')
  await expect(field).toHaveValue('e2e rename row')
  // Nothing left once the space is trimmed: not sent.
  await field.fill('   ')
  await expect(save).toBeDisabled()
  // Forty-one three-byte characters, 123 bytes: the dialog asks for a shorter name rather than send one the server would cut.
  await field.fill('名'.repeat(41))
  await expect(dialog).toContainText('Too long')
  await expect(save).toBeDisabled()
  await field.fill('  e2e renamed row  ')
  await save.click()
  await expect(dialog).toHaveCount(0)
  // The row shows the name the server kept, trimmed, from the events stream.
  await expect(row).toContainText('e2e renamed row')
  expect((await api.session(s.id)).name).toBe('e2e renamed row')
})

test("the palette's row offers the same Rename…", async ({ page, api }) => {
  const s = await launch(api, 'e2e rename palette')
  await page.goto('/agents')
  await expect(page.locator('[data-session-list]')).toBeVisible({ timeout: 30_000 })
  await page.keyboard.press('Control+k')
  const palette = page.locator('[data-palette]')
  await expect(palette).toBeVisible()
  await palette.locator('input[data-palette-input]').fill('e2e rename palette')
  // The row by its id, focused itself: another session the query matches may rank above it.
  const row = palette.locator(`[data-palette-row="session:${s.id}"]`)
  await expect(row).toBeVisible()
  await row.focus()
  await expect(row).toHaveAttribute('data-palette-focused', '')
  await page.keyboard.press('ArrowRight')
  await row.locator('[data-palette-action="rename"]').click()
  await expect(palette).toBeHidden()
  const dialog = page.getByRole('dialog')
  const field = dialog.getByRole('textbox', { name: 'Session name' })
  await expect(field).toHaveValue('e2e rename palette')
  await field.fill('e2e renamed palette')
  await field.press('Enter')
  await expect(dialog).toHaveCount(0)
  await expect(page.locator(`[data-session-list] [data-sidebar-row="s:${s.id}"]`).first()).toContainText('e2e renamed palette')
  expect((await api.session(s.id)).name).toBe('e2e renamed palette')
})

test("the session page renames its session from its menu; its header, its tab's title and its row follow", async ({ page, api }) => {
  const s = await launch(api, 'e2e rename page')
  await page.goto(`/sessions/${s.id}`)
  const title = page.locator('[data-session-title]')
  await expect(title).toHaveText('e2e rename page')
  await page.getByRole('button', { name: 'More', exact: true }).click()
  await page.getByRole('menuitem', { name: /^Rename/ }).click()
  const dialog = page.getByRole('dialog')
  const field = dialog.getByRole('textbox', { name: 'Session name' })
  await expect(field).toHaveValue('e2e rename page')
  await field.fill('e2e renamed page')
  await field.press('Enter')
  await expect(dialog).toHaveCount(0)
  await expect(title).toHaveText('e2e renamed page')
  await expect(page).toHaveTitle(/e2e renamed page/)
  await expect(page.locator(`[data-session-list] [data-sidebar-row="s:${s.id}"]`).first()).toContainText('e2e renamed page')
})

test('a rename made elsewhere reaches an open page, and a resume starts under the new name', async ({ page, api }) => {
  const s = await launch(api, 'e2e rename resume')
  await page.goto(`/sessions/${s.id}`)
  const title = page.locator('[data-session-title]')
  await expect(title).toHaveText('e2e rename resume')
  // Another workbench renames it: this page hears it on the events stream.
  const renamed = await api.ok<Session>('PATCH', `/api/sessions/${s.id}`, { name: 'e2e resumed under it' })
  expect(renamed.name).toBe('e2e resumed under it')
  await expect(title).toHaveText('e2e resumed under it')
  await api.stopSession(s.id)
  await expect.poll(async () => (await api.session(s.id)).status, { timeout: 15_000 }).toMatch(/exited|stopped/)
  await page.locator('[data-resume][data-resume-kind="label"]').first().click()
  await page.waitForURL((u) => u.pathname.startsWith('/sessions/') && !u.pathname.endsWith(s.id), { timeout: 15_000 })
  const nextId = decodeURIComponent(new URL(page.url()).pathname.split('/').pop() ?? '')
  sessions.push(nextId)
  const next = await api.session(nextId)
  expect(next.name).toBe('e2e resumed under it')
  expect(next.resumedFrom).toBe(s.id)
  await expect(title).toHaveText('e2e resumed under it')
})

test('a save of the name the session has sends nothing and closes the dialog', async ({ page, api }) => {
  const s = await launch(api, 'e2e rename same')
  const patches: string[] = []
  page.on('request', (r) => {
    if (r.method() === 'PATCH') patches.push(r.url())
  })
  await page.goto('/crews')
  const { dialog, field } = await renameFromRow(page, s)
  // The same name, with space around it that the server would trim: still the name it has.
  await field.fill('  e2e rename same ')
  await field.press('Enter')
  await expect(dialog).toHaveCount(0)
  expect(patches).toEqual([])
  expect((await api.session(s.id)).name).toBe('e2e rename same')
})

test("a refusal shows the server's words and keeps the dialog open on the draft", async ({ page, api }) => {
  const s = await launch(api, 'e2e rename refused')
  await page.goto('/crews')
  const { dialog, field, save } = await renameFromRow(page, s)
  // The session goes while the dialog is open: stopped, then removed from the list.
  await api.stopSession(s.id)
  await expect.poll(async () => (await api.session(s.id)).status, { timeout: 15_000 }).toMatch(/exited|stopped/)
  await api.stopSession(s.id)
  await expect.poll(async () => (await api.call('GET', `/api/sessions/${s.id}`)).status).toBe(404)
  await field.fill('e2e renamed too late')
  const refused = page.waitForResponse((r) => r.request().method() === 'PATCH' && r.url().endsWith(`/api/sessions/${s.id}`))
  await save.click()
  expect((await refused).status()).toBe(404)
  await expect(dialog).toContainText('no such session')
  await expect(dialog).toBeVisible()
  await expect(field).toHaveValue('e2e renamed too late')
  // Editing the draft clears the refusal.
  await field.fill('e2e renamed again')
  await expect(dialog).not.toContainText('no such session')
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)
})

test("a save's reply after its dialog was dismissed leaves the dialog opened since on another session alone", async ({ page, api }) => {
  const a = await launch(api, 'e2e rename slow')
  const b = await launch(api, 'e2e rename next')
  // A's rename is held at the network until the test lets it go: the dialog is dismissed and opened on B meanwhile.
  let arrived!: () => void
  const held = new Promise<void>((r) => (arrived = r))
  let release!: () => void
  const released = new Promise<void>((r) => (release = r))
  await page.route(`**/api/sessions/${a.id}`, async (route) => {
    if (route.request().method() !== 'PATCH') return route.continue()
    arrived()
    await released
    await route.continue()
  })
  await page.goto('/crews')
  const first = await renameFromRow(page, a)
  await first.field.fill('e2e renamed slow')
  await first.save.click()
  await held
  await page.keyboard.press('Escape')
  await expect(first.dialog).toHaveCount(0)
  const second = await renameFromRow(page, b)
  await second.field.fill('e2e next draft')
  const replied = page.waitForResponse((r) => r.request().method() === 'PATCH' && r.url().endsWith(`/api/sessions/${a.id}`))
  release()
  expect((await replied).status()).toBe(200)
  // A's rename went through, and reached A's row; B's dialog is still open on its draft, and its save is not held by A's.
  await expect(page.locator(`[data-session-list] [data-sidebar-row="s:${a.id}"]`).first()).toContainText('e2e renamed slow')
  await expect(second.dialog).toBeVisible()
  await expect(second.field).toHaveValue('e2e next draft')
  await expect(second.save).toBeEnabled()
  await second.save.click()
  await expect(second.dialog).toHaveCount(0)
  expect((await api.session(b.id)).name).toBe('e2e next draft')
})

test("Rename… on another session from the palette, over an open dialog, starts that dialog afresh on the other session", async ({ page, api }) => {
  const a = await launch(api, 'e2e rename over')
  const b = await launch(api, 'e2e rename under palette')
  // A's rename is held at the network until the test lets it go: the dialog is still open on A, saving, when B's opening comes.
  let arrived!: () => void
  const held = new Promise<void>((r) => (arrived = r))
  let release!: () => void
  const released = new Promise<void>((r) => (release = r))
  await page.route(`**/api/sessions/${a.id}`, async (route) => {
    if (route.request().method() !== 'PATCH') return route.continue()
    arrived()
    await released
    await route.continue()
  })
  await page.goto('/crews')
  const first = await renameFromRow(page, a)
  await first.field.fill('e2e renamed over')
  await first.save.click()
  await held
  await expect(first.dialog).toBeVisible()
  // Alt+/ opens the palette over the dialog; B's row offers Rename….
  await page.keyboard.press('Alt+Slash')
  const palette = page.locator('[data-palette]')
  await expect(palette).toBeVisible()
  await palette.locator('input[data-palette-input]').fill('e2e rename under palette')
  const row = palette.locator(`[data-palette-row="session:${b.id}"]`)
  await expect(row).toBeVisible()
  await row.focus()
  await expect(row).toHaveAttribute('data-palette-focused', '')
  await page.keyboard.press('ArrowRight')
  // By the keyboard: the open dialog's overlay takes the pointer, as it does a person's.
  const action = row.locator('[data-palette-action="rename"]')
  await action.focus()
  await page.keyboard.press('Enter')
  await expect(palette).toBeHidden()
  // The dialog is B's now: B's name, not A's draft, and a save not held by A's.
  const dialog = page.getByRole('dialog')
  const field = dialog.getByRole('textbox', { name: 'Session name' })
  const save = dialog.locator('[data-rename-save]')
  await expect(field).toHaveValue('e2e rename under palette')
  await expect(save).toBeEnabled()
  // A's reply comes back: A is renamed, and B's dialog stays open on B.
  const replied = page.waitForResponse((r) => r.request().method() === 'PATCH' && r.url().endsWith(`/api/sessions/${a.id}`))
  release()
  expect((await replied).status()).toBe(200)
  await expect(page.locator(`[data-session-list] [data-sidebar-row="s:${a.id}"]`).first()).toContainText('e2e renamed over')
  await expect(dialog).toBeVisible()
  await expect(field).toHaveValue('e2e rename under palette')
  await field.fill('e2e renamed under palette')
  await save.click()
  await expect(dialog).toHaveCount(0)
  expect((await api.session(b.id)).name).toBe('e2e renamed under palette')
  expect((await api.session(a.id)).name).toBe('e2e renamed over')
})
