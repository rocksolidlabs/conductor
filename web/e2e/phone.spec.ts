import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import type { BrowserContext, Locator, Page } from '@playwright/test'
import { expect, test, type Session } from './fixtures'

// The phone (design 3e): the list is the home screen, not a drawer; the
// pages sit in a bar at the foot; a prompt is answered in the row with
// full-width 44 px buttons; a row opens its page and Back returns to the
// list; a long press opens the row's actions as a sheet, Stop asking again;
// More holds the other pages, the alerts and your menu.
test.describe.configure({ mode: 'serial' })

const sessions: string[] = []
let crewId = ''
let runId = ''
let ctx: BrowserContext | null = null
let phone: Page

test.beforeAll(async ({ browser, state }) => {
  ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })
  await ctx.addInitScript(
    ({ token }) => {
      localStorage.setItem('conductor.workbenchToken', token)
      localStorage.setItem('conductor.displayName', 'Nate')
    },
    { token: state.token },
  )
  phone = await ctx.newPage()
})

test.afterAll(async ({ api }) => {
  await ctx?.close()
  if (runId) await api.stopRun(runId)
  for (const id of sessions) await api.stopSession(id)
  if (crewId) await api.call('DELETE', `/api/crews/${encodeURIComponent(crewId)}`)
})

function transcript(home: string, s: Session): string {
  const file = join(home, '.stub-sessions', `${s.agentSession?.id ?? ''}.txt`)
  return existsSync(file) ? readFileSync(file, 'utf8') : ''
}

/** A touch long press: the pointer down, held past the 500 ms, then up. */
async function longPress(target: Locator) {
  await target.dispatchEvent('pointerdown', { pointerType: 'touch', isPrimary: true, bubbles: true })
  await phone.waitForTimeout(700)
  await target.dispatchEvent('pointerup', { pointerType: 'touch', isPrimary: true, bubbles: true })
}

test('the list is the home screen: the bar at the foot, the filter on top, a prompt answered with full-width buttons', async ({ api, state }) => {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'asker', name: 'phone asker' })
  sessions.push(s.id)
  await expect.poll(async () => (await api.session(s.id)).attention?.state, { timeout: 30_000 }).toBe('needs_input')
  await phone.goto('/')
  await expect(phone).toHaveURL(/\/sessions$/)
  const list = phone.locator('[data-session-list="page"]')
  await expect(list).toBeVisible()
  await expect(phone.getByPlaceholder('Filter sessions, runs, people')).toBeVisible()
  // No hamburger, no slideover: the sidebar's own list is not even rendered below lg.
  await expect(phone.locator('[data-session-list="sidebar"]')).toHaveCount(0)
  await expect(phone.locator('[data-slot="toggle"]')).toHaveCount(0)
  // The bar: five targets of at least 44 px, the Yard's count.
  const bar = phone.locator('[data-bottom-bar]')
  const tabs = bar.locator('[data-bottom-tab]')
  await expect(tabs).toHaveCount(5)
  for (let i = 0; i < 5; i++) expect((await tabs.nth(i).boundingBox())!.height).toBeGreaterThanOrEqual(44)
  await expect(bar.locator('[data-bottom-tab="yard"] [data-bottom-count]')).toHaveText(/^[1-9]\d*$/)
  await expect(bar.locator('[data-bottom-tab="sessions"]')).toHaveAttribute('aria-current', 'page')
  // The prompt in the row: full-width 44 px buttons; one answers.
  const row = list.locator(`[data-sidebar-row="s:${s.id}"]`).first()
  await expect(row.locator('[data-row-prompt]')).toHaveText('Which database?')
  const choice = row.locator('[data-row-choice="2"]')
  const box = (await choice.boundingBox())!
  expect(box.height).toBeGreaterThanOrEqual(44)
  expect(box.width).toBeGreaterThanOrEqual(300)
  await choice.tap()
  await expect.poll(() => transcript(state.home, s), { timeout: 15_000, message: 'the choice was typed' }).toContain('SQLite\n')
  await expect(row).toHaveAttribute('data-row-state', 'running', { timeout: 15_000 })
})

test('a row opens its page; Back returns to the list', async ({ api }) => {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'phone open' })
  sessions.push(s.id)
  await phone.goto('/sessions')
  const row = phone.locator(`[data-session-list="page"] [data-sidebar-row="s:${s.id}"]`).first()
  await expect(row).toBeVisible({ timeout: 15_000 })
  await row.locator('a[href]').first().tap()
  await expect(phone).toHaveURL(new RegExp(`/sessions/${s.id}$`))
  const back = phone.locator('[data-back-to-list]')
  await expect(back).toBeVisible()
  await back.tap()
  await expect(phone).toHaveURL(/\/sessions$/)
  await expect(phone.locator('[data-session-list="page"]')).toBeVisible()
})

test("a long press opens the row's actions as a sheet; Stop asks again; a run's header lists its own", async ({ api }) => {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'phone press' })
  sessions.push(s.id)
  crewId = (
    await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
      name: 'phone crew',
      goal: 'be pressed',
      cwd: '',
      where: 'server',
      isolation: 'none',
      openAfterLaunch: false,
      members: [{ name: 'lead', agentId: 'claude', prompt: 'say hello', start: { when: 'immediately' } }],
    })
  ).crew.id
  runId = (await api.launchCrew(crewId)).id
  await expect.poll(async () => (await api.run(runId)).members.find((m) => m.name === 'lead')?.status, { timeout: 30_000 }).toBe('running')
  await phone.goto('/sessions')
  const list = phone.locator('[data-session-list="page"]')
  const row = list.locator(`[data-sidebar-row="s:${s.id}"]`).first()
  await expect(row).toBeVisible({ timeout: 15_000 })
  await longPress(row.locator('a[href]').first())
  const sheet = phone.locator('[data-row-sheet]')
  await expect(sheet).toBeVisible()
  await expect(sheet.getByRole('button')).toHaveText([/^Open/, /^Share…/, /^Show in the Yard/, /^Rename…/, /^Stop…/, /^Cancel/])
  // Rename… opens the one dialog on the session (#318).
  await sheet.locator('[data-row-sheet-item="Rename…"]').tap()
  await expect(sheet).toBeHidden()
  const dialog = phone.getByRole('dialog')
  await expect(dialog.getByRole('textbox', { name: 'Session name' })).toHaveValue('phone press')
  await dialog.getByRole('button', { name: 'Cancel' }).tap()
  await expect(dialog).toHaveCount(0)
  await longPress(row.locator('a[href]').first())
  await expect(sheet).toBeVisible()
  // Stop asks again, in the row.
  await sheet.locator('[data-row-sheet-item="Stop…"]').tap()
  await expect(sheet).toBeHidden()
  await expect(row.locator('[data-row-stop-confirm]')).toHaveText('Stop phone press?')
  await row.getByRole('button', { name: 'Cancel' }).tap()
  await expect(row.locator('[data-row-stop-confirm]')).toHaveCount(0)
  // A run's header: its own actions.
  const header = list.locator(`[data-sidebar-run-block="${runId}"] [data-sidebar-run-group]`)
  await expect(header).toBeVisible()
  await longPress(header)
  await expect(sheet).toBeVisible()
  await expect(sheet.getByRole('button')).toHaveText([/^Open run/, /^Share run/, /^Stop run…/, /^Cancel/])
  await sheet.locator('[data-row-sheet-cancel]').tap()
  await expect(sheet).toBeHidden()
})

test('More holds the other pages, the alerts and your menu', async () => {
  await phone.goto('/sessions')
  await phone.locator('[data-bottom-tab="more"]').tap()
  const more = phone.locator('[data-bottom-more]')
  await expect(more).toBeVisible()
  await expect(more.getByRole('link')).toHaveText([/Roundhouse/, /Agents/, /Dispatch/])
  await expect(more.getByText('Browser notification')).toBeVisible()
  await expect(more.getByRole('button')).toHaveText([/Your name/, /Keyboard shortcuts/, /Toggle theme/, /Workbench token/, /Forget token/])
  await more.getByRole('button', { name: /Your name/ }).tap()
  await expect(phone.locator('[data-name-dialog]')).toBeVisible()
})
