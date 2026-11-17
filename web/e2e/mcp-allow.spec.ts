import { existsSync, readFileSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import type { Page } from '@playwright/test'
import { expect, rowAnswer, showView, test, type Api, type Session } from './fixtures'
import { drawnGrid, narrowedBy } from './terminal-width'

// Codex's form asking to allow a tool of an MCP server (#716; drawn by the
// stub as Codex 0.163.0 to 0.169.0 draw it): the session asks, as it does
// for Codex's other dialogs, with the form's words and its own four choices,
// in the sidebar's row, the session page's question bar and Needs your
// decision; an allowing choice types its digit alone and Cancel an Escape,
// which the stub reads, and the session stops asking. The two choices that
// remember the approval are mode answers: no number key, never offered first.
test.describe.configure({ mode: 'serial' })

const WORDS = 'Allow the conductor MCP server to run tool "guide"?'
const CHOICES = ['Allow', 'Allow for this session', 'Always allow', 'Cancel']
const OPTIONS = [
  { label: 'Allow', input: '1' },
  { label: 'Allow for this session', input: '2', mode: true },
  { label: 'Always allow', input: '3', mode: true },
  { label: 'Cancel', input: '\u001b' },
]
// The number key a page gives each choice: none for a mode answer.
const KEYS = ['1', '', '', '4']
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

// At the suite's 1440 by 900 (the terminal wider than the session's 80 columns) and at 1280 by 800 with touch (narrower than 80
// columns, where Codex wraps the choices' descriptions under their column, as the stub does).
for (const size of [
  { name: '1440 by 900', use: {}, narrower: false },
  { name: '1280 by 800 with touch', use: { viewport: { width: 1280, height: 800 }, hasTouch: true }, narrower: true },
]) {
  test.describe(`a page ${size.name}`, () => {
    test.use(size.use)
    test("Codex's tool approval asks with the form's own choices, and a choice types its key alone", async ({ page, api, state }) => {
      await toolApproval(page, api, state, size.narrower)
    })
  })
}

async function toolApproval(page: Page, api: Api, state: { home: string }, narrower: boolean) {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'codex', name: 'e2e tool approval' })
  sessions.push(s.id)
  // The narrower page opens with the sidebar at 300 px, the width a person dragged it to before (kept for the browser under the app's
  // own key, in rem), and its inspector open (it starts closed, the terminal taking the room, P11): the terminal is then narrower
  // than the session's 80 columns, where Codex wraps the choices' descriptions, while the window keeps the sidebar whose row this test
  // reads. The window is not made narrower instead (terminalColumns): the inspector is beside the terminal only from 1280 px (xl),
  // and below lg the sidebar is not drawn. At 300 px the row holds its choices; at 1440 they are in the panel beside the 200 px row.
  if (narrower) await page.addInitScript((rem) => localStorage.setItem('conductor.sidebar.width', rem), String(300 / 16))
  await page.goto(`/sessions/${encodeURIComponent(s.id)}`)
  await expect(page.locator('.terminal-host .xterm-screen').first()).toBeVisible()
  // The page's window sizes the session before the form is drawn: the stub lays it out for that width. Waited on what the page
  // computed, its terminal's grid, until the session has that width, then judged against the 80 it was born with. Where the
  // inspector opens, the grid read at once after it is still the one drawn before (on CI: 109 columns with the inspector closed,
  // already the session's): the wait is for the fit pass the inspector's opening brings (narrowedBy).
  const sessionCols = async () => (await api.session(s.id)).cols ?? 0
  let grid: { cols: number; cell: number }
  if (narrower) {
    await expect.poll(() => page.locator('#dashboard-sidebar-main').evaluate((el) => Math.round(el.getBoundingClientRect().width))).toBe(300)
    grid = await narrowedBy(page, sessionCols, async () => {
      await showView(page, 'people')
      await expect(page.locator('[data-inspector-open="true"]')).toBeVisible()
    })
  } else {
    await expect.poll(async () => { grid = await drawnGrid(page); return grid.cols !== 80 && (await sessionCols()) === grid.cols }, { timeout: 15_000, message: 'the page sized the session to its grid' }).toBe(true)
  }
  const cols = grid!.cols
  expect(narrower ? cols < 80 : cols > 80, `the page's grid is ${cols} columns of ${grid!.cell.toFixed(2)} px`).toBe(true)
  await typeLine(page, 'stub mcp-allow')

  // The session asks: a question of the dialog's, a permission, with the form's words and its choices.
  await expect.poll(async () => (await api.session(s.id)).attention?.source, { timeout: 30_000, message: 'the form is asked' }).toBe('question')
  const att = (await api.session(s.id)).attention
  expect(att?.state).toBe('needs_input')
  expect(att?.message).toBe(WORDS)
  expect(att?.kind).toBe('permission')
  expect(att?.options).toEqual(OPTIONS)

  // The sidebar's row: needs you, the words, and the four choices where a person answers it (rowAnswer: in the panel beside the
  // row while the sidebar is narrower than 280 px, as it opens).
  const row = page.locator(`[data-session-list="sidebar"] [data-sidebar-row="s:${s.id}"]`).first()
  await expect(row).toHaveAttribute('data-row-state', 'needs', { timeout: 30_000 })
  await expect(row.locator('[data-row-prompt]')).toHaveText(WORDS)
  const answer = await rowAnswer(page, row)
  await expect(answer.locator('[data-row-choice]')).toHaveCount(4)
  for (const [i, label] of CHOICES.entries()) await expect(answer.locator(`[data-row-choice="${i + 1}"]`)).toContainText(label)

  // The page's question bar: the words and the four choices, Allow and Cancel with their number keys, the two that remember
  // the approval marked as mode answers, with none.
  const bar = page.locator('[data-quick-reply]')
  await expect(bar.locator('[data-quick-reply-message]')).toHaveText(WORDS)
  const choices = bar.locator('button:not([data-quick-reply-plan]):not([data-quick-reply-plan-preview])')
  await expect(choices).toHaveCount(4)
  for (const [i, label] of CHOICES.entries()) {
    await expect(choices.nth(i)).toContainText(label)
    if (KEYS[i]) await expect(choices.nth(i).locator('kbd')).toHaveText(KEYS[i]!)
    else await expect(choices.nth(i).locator('kbd')).toHaveCount(0)
    if (OPTIONS[i]!.mode) await expect(choices.nth(i)).toHaveAttribute('data-choice-mode', 'true')
    else await expect(choices.nth(i)).not.toHaveAttribute('data-choice-mode')
  }

  // Needs you: the question's item, its answers the form's choices, the two mode answers after the others.
  const ref = `question:${s.id}/${att?.id ?? ''}`
  await page.goto('/decisions')
  await page.locator(`[data-inbox-row="${ref}"]`).click()
  const item = page.locator(`[data-inbox-item="${ref}"]`)
  await expect(item.locator('[data-inbox-question]')).toHaveText(WORDS)
  await expect(item.locator('[data-inbox-action="answer"]')).toHaveText(['Allow', 'Cancel', 'Allow for this session', 'Always allow'])

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

  // The same form again is a question of its own; Cancel, from the question bar, types an Escape, which the stub reads as
  // the form's cancel.
  await typeLine(page, 'stub mcp-allow')
  await expect.poll(async () => { const a = (await api.session(s.id)).attention; return a?.source === 'question' && a.id !== att?.id && !a.answered }, { timeout: 30_000, message: 'the form is asked again' }).toBe(true)
  await expect(choices).toHaveCount(4)
  await choices.nth(3).click()
  await expect.poll(async () => transcript(state.home, await api.session(s.id)), { timeout: 15_000, message: 'the stub read the cancel' }).toContain('mcp-allow: cancel\n')
  await expect.poll(async () => (await api.session(s.id)).attention?.source, { timeout: 15_000, message: 'the session stops asking' }).not.toBe('question')
  expect(transcript(state.home, await api.session(s.id)).match(/^mcp-allow: /gm)).toHaveLength(2)
}
