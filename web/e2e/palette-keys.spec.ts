import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Session } from './fixtures'

// The palette's keys against a focused terminal (#302, principle 6): Alt+/
// opens it from the terminal and the terminal never sees the key; Ctrl+K
// stays the shell's (kill-line) and the palette stays closed; inside the
// palette ↓ enters the list, J K move, Escape goes back to the input and a
// second Escape closes; → reveals a row's actions and ← folds them.
function transcript(home: string, s: Session): string {
  const file = join(home, '.stub-sessions', `${s.agentSession?.id ?? ''}.txt`)
  return existsSync(file) ? readFileSync(file, 'utf8') : ''
}

test('Alt+/ opens the palette from the terminal without the terminal seeing it; Ctrl+K reaches the shell and opens nothing', async ({ page, api, state }) => {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e palette terminal' })
  try {
    await page.goto(`/sessions/${s.id}`)
    const terminal = page.locator('.terminal-host .xterm-helper-textarea').first()
    await expect(terminal).toBeFocused({ timeout: 30_000 })
    const palette = page.locator('[data-palette]')
    await page.keyboard.press('Alt+/')
    await expect(palette).toBeVisible()
    await expect(palette.locator('input[data-palette-input]')).toBeFocused()
    await page.keyboard.press('Escape')
    await expect(palette).toBeHidden()
    // Back in the terminal: a line typed now carries no slash from the chord.
    await terminal.focus()
    await page.keyboard.type('alt slash check')
    await page.keyboard.press('Enter')
    await expect.poll(() => transcript(state.home, s), { timeout: 15_000, message: 'the line was typed' }).toContain('alt slash check\n')
    expect(transcript(state.home, s)).not.toContain('/')
    // Ctrl+K: the shell's. The stub reads the line as typed, the control byte inside it, and the palette stays closed.
    await terminal.focus()
    await page.keyboard.type('abc')
    await page.keyboard.press('Control+k')
    await expect(palette).toBeHidden()
    await page.keyboard.type('def')
    await page.keyboard.press('Enter')
    await expect.poll(() => transcript(state.home, s), { timeout: 15_000, message: 'the line with the control byte was typed' }).toContain('abc\u000bdef\n')
    await expect(palette).toBeHidden()
  } finally {
    await api.stopSession(s.id)
  }
})

test('↓ enters the list, J and K move, → and ← reveal and fold a row’s actions, Escape returns to the input and then closes', async ({ page, api }) => {
  const a = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e palette keys a' })
  const b = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e palette keys b' })
  try {
    await page.goto('/agents')
    await expect(page.locator('[data-session-list]')).toBeVisible({ timeout: 30_000 })
    await page.keyboard.press('Control+k')
    const palette = page.locator('[data-palette]')
    const input = palette.locator('input[data-palette-input]')
    await input.fill('e2e palette keys')
    const focused = palette.locator('[data-palette-row][data-palette-focused]')
    // Working lists the two (they send no signal); other groups may match the name too (Copy a view link to…), below them.
    await expect(palette.locator('[data-palette-group="working"] [data-palette-row]')).toHaveCount(2)
    await expect(palette.locator('[data-palette-group]').first()).toHaveAttribute('data-palette-group', 'working')
    await expect(focused).toHaveCount(0)
    await page.keyboard.press('ArrowDown')
    await expect(focused).toHaveCount(1)
    const first = await focused.getAttribute('data-palette-row')
    await page.keyboard.press('j')
    expect(await focused.getAttribute('data-palette-row')).not.toBe(first)
    await page.keyboard.press('k')
    expect(await focused.getAttribute('data-palette-row')).toBe(first)
    // The row's actions: Share, Show in the Yard, Stop, Copy a view link now…
    await page.keyboard.press('ArrowRight')
    await expect(focused.locator('[data-palette-actions] [data-palette-action="stop"]')).toBeVisible()
    await expect(focused.locator('[data-palette-actions] [data-palette-action="copy-link"]')).toBeVisible()
    await page.keyboard.press('ArrowLeft')
    await expect(focused.locator('[data-palette-actions]')).toHaveCount(0)
    // X asks to stop in the row; Escape cancels and the row keeps the focus.
    await page.keyboard.press('x')
    await expect(focused.locator('[data-palette-ask]')).toContainText('Stop e2e palette keys')
    await page.keyboard.press('Escape')
    await expect(focused.locator('[data-palette-ask]')).toHaveCount(0)
    await expect(focused).toHaveCount(1)
    // A button inside the row keeps its Enter: Tab from the row with its actions shown lands on the first action, Share…, and
    // Enter opens the Share dialog rather than the row (Codex's round on #302).
    await page.keyboard.press('ArrowRight')
    await page.keyboard.press('Tab')
    await expect(focused.locator('[data-palette-actions] [data-palette-action="share"]')).toBeFocused()
    await page.keyboard.press('Enter')
    await expect(palette).toBeHidden()
    await expect(page.locator('[data-share-created]')).toBeVisible({ timeout: 15_000 })
    await page.keyboard.press('Escape')
    await expect(page.locator('[data-share-created]')).toBeHidden()
    await expect(page).toHaveURL(/\/agents$/)
    await page.keyboard.press('Control+k')
    await input.fill('e2e palette keys')
    await page.keyboard.press('ArrowDown')
    await expect(focused).toHaveCount(1)
    // A click back into the input leaves no row focused: the next characters are the query's, not a row's keys.
    await input.click()
    await expect(focused).toHaveCount(0)
    await page.keyboard.type(' b')
    await expect(input).toHaveValue('e2e palette keys b')
    await expect(focused).toHaveCount(0)
    await input.fill('e2e palette keys')
    await page.keyboard.press('ArrowDown')
    await expect(focused).toHaveCount(1)
    // ↑ from the first row goes back to the input; Escape from a row too; Escape from the input closes.
    await page.keyboard.press('ArrowUp')
    await expect(input).toBeFocused()
    await page.keyboard.press('ArrowDown')
    await expect(focused).toHaveCount(1)
    await page.keyboard.press('Escape')
    await expect(input).toBeFocused()
    await expect(palette).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(palette).toBeHidden()
    // The match is marked in the label.
    await page.keyboard.press('Control+k')
    await input.fill('keys b')
    await expect(palette.locator(`[data-palette-row="session:${b.id}"] [data-palette-label] mark`)).toHaveText('keys b')
    await expect(palette.locator(`[data-palette-row="session:${a.id}"]`)).toHaveCount(0)
  } finally {
    await api.stopSession(a.id)
    await api.stopSession(b.id)
  }
})

test('a key inside the palette never reaches the page behind it: a digit on a row without a prompt answers no session', async ({ page, api, state }) => {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'asker', name: 'e2e palette behind' })
  try {
    await expect.poll(async () => (await api.session(s.id)).attention?.state, { timeout: 30_000 }).toBe('needs_input')
    await page.goto(`/sessions/${s.id}`)
    await expect(page.locator('[data-quick-reply]')).toBeVisible({ timeout: 30_000 })
    await expect(page.locator('.terminal-host .xterm-helper-textarea').first()).toBeFocused({ timeout: 30_000 })
    await page.keyboard.press('Alt+/')
    const palette = page.locator('[data-palette]')
    await expect(palette).toBeVisible()
    await palette.locator('input[data-palette-input]').fill('yard')
    const row = palette.locator('[data-palette-row="go:yard"]')
    await expect(row).toBeVisible()
    // Stepped to by its key: in the whole suite other specs' sessions match "yard" too and list above Go to.
    await page.keyboard.press('ArrowDown')
    const focusedKey = () => palette.locator('[data-palette-row][data-palette-focused]').getAttribute('data-palette-row')
    for (let i = 0; i < 12 && (await focusedKey()) !== 'go:yard'; i++) await page.keyboard.press('j')
    await expect(palette.locator('[data-palette-row][data-palette-focused]')).toHaveAttribute('data-palette-row', 'go:yard')
    // 1 on a row that asks nothing: the page's reply bar, which answers digits, never sees it (whole-batch round).
    await page.keyboard.press('1')
    await page.waitForTimeout(800)
    expect(transcript(state.home, s)).not.toContain('Postgres')
    expect((await api.session(s.id)).attention?.state).toBe('needs_input')
    await page.keyboard.press('Escape')
    await page.keyboard.press('Escape')
    await expect(palette).toBeHidden()
    // Outside the palette the bar's digit works as before (from the page, not the sidebar's list, which takes digits for its row).
    await page.locator('[data-quick-reply-message]').click()
    await page.keyboard.press('1')
    await expect.poll(() => transcript(state.home, s), { timeout: 15_000, message: 'the bar answered' }).toContain('Postgres\n')
  } finally {
    await api.stopSession(s.id)
  }
})
