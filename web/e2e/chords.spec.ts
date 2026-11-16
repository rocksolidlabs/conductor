import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Session } from './fixtures'

function transcript(home: string, s: Session): string {
  const file = join(home, '.stub-sessions', `${s.agentSession?.id ?? ''}.txt`)
  return existsSync(file) ? readFileSync(file, 'utf8') : ''
}

// The chords per system (#312), on the one system the suite runs on
// (Linux, Chromium): Alt+Q leaves a terminal's focus on the Yard's focus and
// the Roundhouse, Alt+Esc too where the system leaves it; the shortcuts
// modal shows Alt+Q for that row and says what this system takes; Ctrl+B
// collapses the sidebar outside a terminal and Alt+B inside one.
test('Alt+Q (and Alt+Esc here) leave the terminal on the Yard and the Roundhouse, and reaches the shell on a session page; the modal shows the system’s chords and notes', async ({ page, api, state }) => {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e chords' })
  try {
    const inTerminal = () => page.evaluate(() => !!document.activeElement?.closest('.terminal-host'))
    await page.goto(`/yard?focus=${s.id}`)
    const terminal = page.locator('[data-yard-focus] .terminal-host .xterm-helper-textarea').first()
    await expect(terminal).toBeVisible({ timeout: 30_000 })
    await terminal.focus()
    expect(await inTerminal()).toBe(true)
    await page.keyboard.press('Alt+q')
    // The Yard's Alt+Q with nothing to fold leaves the focus and goes back to the grid.
    await expect(page).toHaveURL(/\/yard$/)
    expect(await inTerminal()).toBe(false)
    // To the Roundhouse by the Yard's view switch, a link and not a load: the next page is set up before the last goes, and both take
    // Alt+Q (whole-batch round).
    await page.locator('[data-session-list]').click({ position: { x: 5, y: 5 } })
    await page.locator('[data-yard-view="roundhouse"]').click()
    await expect(page).toHaveURL(/\/roundhouse$/)
    const slide = page.locator('[aria-roledescription="slide"]').filter({ has: page.getByText('e2e chords', { exact: true }) })
    await expect(slide).toBeVisible({ timeout: 30_000 })
    await page.locator('[data-carousel-strip] [data-strip-chip]').filter({ has: page.getByText('e2e chords', { exact: true }) }).click()
    await page.keyboard.press('Enter')
    await expect.poll(inTerminal, { timeout: 15_000 }).toBe(true)
    await page.keyboard.press('Alt+q')
    await expect.poll(inTerminal).toBe(false)
    await page.keyboard.press('Enter')
    await expect.poll(inTerminal, { timeout: 15_000 }).toBe(true)
    await page.keyboard.press('Alt+Escape')
    await expect.poll(inTerminal).toBe(false)
    // The modal: the row shows Alt+Q, and the notes name this system.
    await page.keyboard.press('?')
    const modal = page.locator('[data-shortcuts]')
    await expect(modal).toBeVisible()
    await expect(modal.locator('[data-shortcut^="Leave the terminal"] [data-shortcut-terminal]')).toHaveText(/Alt.*Q/i)
    await expect(modal.locator('[data-shortcuts-notes]')).toHaveAttribute('data-shortcuts-system', 'linux')
    await expect(modal.locator('[data-shortcuts-notes] h3')).toContainText('On Linux, in a browser')
    await expect(modal.locator('[data-shortcut-note="Ctrl+B"]')).toContainText('Firefox')
    await expect(modal.locator('[data-shortcut-note="Alt+Esc"]')).toHaveCount(0)
    // The table's shape (#328): the column names once, no lone dot for a missing chord, the caps of each column on one axis down
    // the table, and the close button clear of the description.
    await expect(modal.locator('[data-shortcuts-group-columns]')).toHaveCount(0)
    await expect(modal.locator('[data-shortcuts-columns]')).toHaveCount(1)
    expect(await modal.locator('[data-shortcut-terminal], [data-shortcut-outside]').evaluateAll((els) => els.filter((e) => (e.textContent || '').trim() === '·').length)).toBe(0)
    const shape = await modal.evaluate((root) => {
      const centres = (q: string) => Array.from(root.querySelectorAll(q)).map((e) => { const r = e.getBoundingClientRect(); return r.left + r.width / 2 })
      const spread = (xs: number[]) => Math.max(...xs) - Math.min(...xs)
      const dialog = root.closest('[role="dialog"]')!
      const close = dialog.querySelector('button[aria-label="Close"]')?.getBoundingClientRect()
      const description = dialog.querySelector('p')?.getBoundingClientRect()
      const overlap = close && description ? description.right > close.left && description.top < close.bottom && description.bottom > close.top : false
      return { terminal: spread(centres('[data-shortcut-terminal]')), outside: spread(centres('[data-shortcut-outside]')), overlap }
    })
    expect(shape.terminal, 'the terminal column\'s cells on one axis').toBeLessThanOrEqual(1)
    expect(shape.outside, 'the outside column\'s cells on one axis').toBeLessThanOrEqual(1)
    expect(shape.overlap, 'the close button over the description').toBe(false)
    await page.keyboard.press('Escape')
    await expect(modal).toBeHidden()
    // The sidebar: Ctrl+B outside a terminal, Alt+B inside one.
    await page.goto(`/sessions/${s.id}`)
    const session = page.locator('.terminal-host .xterm-helper-textarea').first()
    await expect(session).toBeFocused({ timeout: 30_000 })
    const rail = page.locator('[data-rail-expand]')
    await page.keyboard.press('Alt+b')
    await expect(rail).toBeVisible()
    await page.keyboard.press('Alt+b')
    await expect(rail).toBeHidden()
    await page.locator('[data-session-list]').click({ position: { x: 5, y: 5 } })
    await page.keyboard.press('Control+b')
    await expect(rail).toBeVisible()
    await page.keyboard.press('Control+b')
    await expect(rail).toBeHidden()
    // On a session page, which takes no Alt+Q, the chord is the shell's: it lands in the line as Escape then q (Codex's round). Alt+J
    // and Alt+K are the terminal's on every page (the Yard's queue that took them is gone): Escape then j, Escape then k.
    await session.focus()
    await page.keyboard.type('a')
    await page.keyboard.press('Alt+j')
    await page.keyboard.press('Alt+k')
    await page.keyboard.press('Alt+q')
    await page.keyboard.type('b')
    await page.keyboard.press('Enter')
    await expect.poll(() => transcript(state.home, s), { timeout: 15_000, message: 'the line with the chords was typed' }).toContain('a\u001bj\u001bk\u001bqb\n')
    // In a tile expanded on the Yard the same: Alt+J and Alt+K reach the terminal (Alt+Q stays the Yard's, and leaves the tile).
    await page.goto(`/yard?focus=${s.id}`)
    const tile = page.locator('[data-yard-focus] .terminal-host .xterm-helper-textarea').first()
    await expect(tile).toBeVisible({ timeout: 30_000 })
    await tile.focus()
    await page.keyboard.type('c')
    await page.keyboard.press('Alt+j')
    await page.keyboard.press('Alt+k')
    await page.keyboard.type('d')
    await page.keyboard.press('Enter')
    await expect.poll(() => transcript(state.home, s), { timeout: 15_000, message: 'the line typed in the expanded tile' }).toContain('c\u001bj\u001bkd\n')
    await expect(page).toHaveURL(new RegExp(`/yard\\?focus=${s.id}$`))
  } finally {
    await api.stopSession(s.id)
  }
})
