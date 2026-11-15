import { existsSync, readFileSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import type { Page } from '@playwright/test'
import { expect, test, type Session } from './fixtures'

// Codex's form asking to allow a tool of an MCP server (#716; drawn by the
// stub as Codex 0.163.0 to 0.169.0 draw it): the session asks, as it does
// for Codex's other dialogs, with the form's words and its own four choices,
// in the sidebar's row, the session page's question bar and Needs your
// decision; a choice types its digit alone, which the stub reads, and the
// session stops asking.
test.describe.configure({ mode: 'serial' })

const WORDS = 'Allow the conductor MCP server to run tool "guide"?'
const CHOICES = ['Allow', 'Allow for this session', 'Always allow', 'Cancel']
const sessions: string[] = []

test.beforeAll(({ state }) => {
  // No startup question in the way: those are the trust spec's.
  for (const m of ['stub-hooks-review', 'stub-background', 'stub-update']) rmSync(join(state.home, '.codex', m), { force: true })
})

test.afterAll(async ({ api }) => {
  for (const id of sessions) await api.stopSession(id)
})

function transcript(home: string, s: Session): string {
  const file = join(home, '.stub-sessions', `${s.agentSession?.id ?? ''}.txt`)
  return existsSync(file) ? readFileSync(file, 'utf8') : ''
}

async function typeLine(page: Page, line: string) {
  const screen = page.locator('.terminal-host .xterm-screen').first()
  await expect(screen).toBeVisible()
  await screen.click()
  await page.keyboard.type(line)
  await page.keyboard.press('Enter')
}

test("Codex's tool approval asks with the form's own choices, and a choice types its digit alone", async ({ page, api, state }) => {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'codex', name: 'e2e tool approval' })
  sessions.push(s.id)
  await page.goto(`/sessions/${encodeURIComponent(s.id)}`)
  await typeLine(page, 'stub mcp-allow')

  // The session asks: a question of the dialog's, a permission, with the form's words and its choices, each its digit.
  await expect.poll(async () => (await api.session(s.id)).attention?.source, { timeout: 30_000, message: 'the form is asked' }).toBe('question')
  const att = (await api.session(s.id)).attention
  expect(att?.state).toBe('needs_input')
  expect(att?.message).toBe(WORDS)
  expect(att?.kind).toBe('permission')
  expect(att?.options).toEqual(CHOICES.map((label, i) => ({ label, input: String(i + 1) })))

  // The sidebar's row: needs you, the words and the four choices.
  const row = page.locator(`[data-session-list="sidebar"] [data-sidebar-row="s:${s.id}"]`).first()
  await expect(row).toHaveAttribute('data-row-state', 'needs', { timeout: 30_000 })
  await expect(row.locator('[data-row-prompt]')).toHaveText(WORDS)
  await expect(row.locator('[data-row-choice]')).toHaveCount(4)
  for (const [i, label] of CHOICES.entries()) await expect(row.locator(`[data-row-choice="${i + 1}"]`)).toContainText(label)

  // The page's question bar: the words and the four choices, each with its number key.
  const bar = page.locator('[data-quick-reply]')
  await expect(bar.locator('[data-quick-reply-message]')).toHaveText(WORDS)
  const choices = bar.locator('button:not([data-quick-reply-plan]):not([data-quick-reply-plan-preview])')
  await expect(choices).toHaveCount(4)
  for (const [i, label] of CHOICES.entries()) {
    await expect(choices.nth(i)).toContainText(label)
    await expect(choices.nth(i).locator('kbd')).toHaveText(String(i + 1))
  }

  // Needs your decision: the question's item, its answers the form's choices.
  const ref = `question:${s.id}/${att?.id ?? ''}`
  await page.goto('/decisions')
  await page.locator(`[data-inbox-row="${ref}"]`).click()
  const item = page.locator(`[data-inbox-item="${ref}"]`)
  await expect(item.locator('[data-inbox-question]')).toHaveText(WORDS)
  await expect(item.locator('[data-inbox-action="answer"]')).toHaveText(CHOICES)

  // Allow for this session, from the question bar: the stub reads 2, and nothing else (the next line it reads is the one typed next).
  await page.goto(`/sessions/${encodeURIComponent(s.id)}`)
  await expect(choices).toHaveCount(4)
  await choices.nth(1).click()
  await expect.poll(async () => transcript(state.home, await api.session(s.id)), { timeout: 15_000, message: 'the stub read the choice' }).toContain('mcp-allow: allow-session\n')
  await expect.poll(async () => (await api.session(s.id)).attention?.source, { timeout: 15_000, message: 'the session stops asking' }).not.toBe('question')
  await expect(row).not.toHaveAttribute('data-row-state', 'needs', { timeout: 15_000 })
  await typeLine(page, 'after the form')
  await expect.poll(async () => transcript(state.home, await api.session(s.id)), { timeout: 15_000, message: 'the stub read the next line' }).toContain('mcp-allow next line: after the form\n')
  expect(transcript(state.home, await api.session(s.id)).match(/^mcp-allow: /gm)).toHaveLength(1)
})
