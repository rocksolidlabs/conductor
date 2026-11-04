import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Page } from '@playwright/test'
import { expect, test, type Session } from './fixtures'

// Claude Code's dialogs during the work (#288): their choices are read off
// the dialog itself, never a fixed Yes / Always / No. The plan's approval
// (drawn by the stub as Claude Code 2.1.304 draws it, after its hooks)
// offers its own three choices in the reply bar and the sidebar's row; the
// one that switches to auto mode has no number key and is not the
// highlighted one, so its digit does nothing; each button writes its own
// digit. A permission whose dialog the screen does not show is answered in
// the terminal: no choices, and no reply field takes a line.
test.describe.configure({ mode: 'serial' })

const sessions: string[] = []

test.afterAll(async ({ api }) => {
  for (const id of sessions) await api.stopSession(id)
})

function transcript(home: string, s: Session): string {
  const file = join(home, '.stub-sessions', `${s.agentSession?.id ?? ''}.txt`)
  return existsSync(file) ? readFileSync(file, 'utf8') : ''
}

async function typeLine(page: Page, sessionId: string, line: string) {
  await page.goto(`/sessions/${encodeURIComponent(sessionId)}`)
  const screen = page.locator('.terminal-host .xterm-screen').first()
  await expect(screen).toBeVisible()
  await screen.click()
  await page.keyboard.type(line)
  await page.keyboard.press('Enter')
}

const PLAN = 'Claude has written up a plan and is ready to execute. Would you like to proceed?'

test("the plan's approval offers its own choices, the change of mode without a number, and a button writes its digit", async ({ page, api, state }) => {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e plan approval' })
  sessions.push(s.id)
  await typeLine(page, s.id, 'stub plan')
  await expect.poll(async () => (await api.session(s.id)).attention?.source, { timeout: 30_000, message: 'the dialog is asked' }).toBe('question')
  const att = (await api.session(s.id)).attention
  expect(att?.message).toBe(PLAN)
  expect(att?.kind).toBe('permission')
  expect(att?.options).toEqual([
    { label: 'Yes, and use auto mode', input: '1', mode: true },
    { label: 'Yes, manually approve edits', input: '2' },
    { label: 'Tell Claude what to change', input: '3' },
  ])

  // The reply bar: three choices, the change of mode without a number and outlined; the next one is the highlighted one.
  const bar = page.locator('[data-quick-reply]')
  await expect(bar.locator('[data-quick-reply-message]')).toHaveText(PLAN)
  // The choices alone: the bar also offers Open plan and Preview plan for the plan the approval is about (#289, #432).
  const choices = bar.locator('button:not([data-quick-reply-plan]):not([data-quick-reply-plan-preview])')
  await expect(choices).toHaveCount(3)
  const mode = bar.locator('button[data-choice-mode]')
  await expect(mode).toHaveCount(1)
  await expect(mode).toContainText('Yes, and use auto mode')
  await expect(mode).toHaveAttribute('title', "Changes the session's permission mode")
  await expect(mode.locator('kbd')).toHaveCount(0)
  await expect(choices.nth(1).locator('kbd')).toHaveText('2')
  await expect(choices.nth(2).locator('kbd')).toHaveText('3')

  // The sidebar's row offers the same, the first without its number.
  const row = page.locator(`[data-session-list="sidebar"] [data-sidebar-row="s:${s.id}"]`).first()
  await expect(row.locator('[data-row-choice]')).toHaveCount(3)
  await expect(row.locator('[data-row-choice="1"]')).toHaveAttribute('data-choice-mode', 'true')
  await expect(row.locator('[data-row-choice="1"] kbd')).toHaveCount(0)
  await expect(row.locator('[data-row-choice="2"] kbd')).toHaveText('2')

  // The digit 1, pressed outside the terminal, picks nothing: the change of mode is clicked or nothing.
  await bar.locator('[data-quick-reply-message]').click()
  await page.keyboard.press('1')
  await page.waitForTimeout(800)
  expect(transcript(state.home, await api.session(s.id))).not.toContain('plan:')
  expect((await api.session(s.id)).attention?.source).toBe('question')

  // "Yes, manually approve edits" writes its own digit, which the stub reads.
  await choices.nth(1).click()
  await expect.poll(async () => transcript(state.home, await api.session(s.id)), { timeout: 15_000, message: 'the stub read the choice' }).toContain('plan: 2')
  await expect.poll(async () => (await api.session(s.id)).attention?.source, { timeout: 15_000 }).not.toBe('question')
})

test('a permission whose dialog the screen does not show is answered in the terminal', async ({ page, api, state }) => {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e permission in the terminal' })
  sessions.push(s.id)
  await typeLine(page, s.id, 'stub permission')
  await expect.poll(async () => (await api.session(s.id)).attention?.message, { timeout: 30_000, message: 'the hook reports it' }).toBe('Allow Bash?')
  const att = (await api.session(s.id)).attention
  expect(att?.kind).toBe('permission')
  expect(att?.options ?? []).toEqual([])

  const bar = page.locator('[data-quick-reply]')
  await expect(bar).toHaveAttribute('data-terminal-answer', 'true')
  await expect(bar.getByRole('button', { name: 'Send' })).toBeDisabled()
  const field = bar.getByRole('textbox')
  await expect(field).toBeDisabled()
  await expect(field).toHaveAttribute('placeholder', 'Answer it in the terminal')
  const row = page.locator(`[data-session-list="sidebar"] [data-sidebar-row="s:${s.id}"]`).first()
  await expect(row.locator('[data-row-reply]')).toBeDisabled()

  // The terminal answers it.
  await page.locator('.terminal-host .xterm-screen').first().click()
  await page.keyboard.press('4')
  await expect.poll(async () => transcript(state.home, await api.session(s.id)), { timeout: 15_000 }).toContain('permission: 4')
})
