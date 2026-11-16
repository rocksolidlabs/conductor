import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, showView, test, type Session } from './fixtures'

// A tab's menu (#330), as VS Code's: Close, Close others, Close to the
// right, Close to the left and Close all, each through the close that asks
// about unsaved changes.
test('the tab menu closes to the right, others and all, asking about an unsaved file', async ({ page, api, state }) => {
  const cwd = join(state.root, 'tabs')
  mkdirSync(cwd, { recursive: true })
  for (const n of ['a', 'b', 'c']) writeFileSync(join(cwd, `${n}.txt`), `${n} one\n${n} two\n`)
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e tabs', cwd })
  const area = page.locator('[data-editor-area]')
  const tab = (n: string) => area.locator(`[data-editor-tab="file:${join(cwd, `${n}.txt`)}"]`)
  const openAll = async () => {
    await showView(page, 'explorer')
    for (const n of ['a', 'b', 'c']) {
      await page.locator(`[data-files-mode] [data-file-node="${join(cwd, `${n}.txt`)}"]`).click()
      await expect(tab(n)).toBeVisible({ timeout: 30_000 })
    }
    await expect(area.locator('[data-editor-tab]')).toHaveCount(3)
    await expect(tab('c')).toHaveAttribute('data-editor-tab-active', '')
  }
  const choose = async (n: string, item: string) => {
    await tab(n).click({ button: 'right' })
    // The Close item's accessible name carries its keys ("Close Ctrl W"); the others are their words alone.
    await page.getByRole('menuitem', { name: item === 'Close' ? /^Close(\s+Ctrl\s*W)?$/ : item, exact: true }).click()
  }
  try {
    await page.goto(`/sessions/${s.id}`)
    await expect(page.locator('.terminal-host .xterm-helper-textarea').first()).toBeFocused({ timeout: 30_000 })
    await openAll()
    // Close to the right from the middle: c goes, b (the nearest kept) takes the front.
    await choose('b', 'Close to the right')
    await expect(area.locator('[data-editor-tab]')).toHaveCount(2)
    await expect(tab('c')).toHaveCount(0)
    await expect(tab('b')).toHaveAttribute('data-editor-tab-active', '')
    // Close others from a: only a stays, in front.
    await choose('a', 'Close others')
    await expect(area.locator('[data-editor-tab]')).toHaveCount(1)
    await expect(tab('a')).toHaveAttribute('data-editor-tab-active', '')
    // Three again; an unsaved edit in b; Close all asks, Don't save closes them all and the editor goes.
    await showView(page, 'explorer')
    for (const n of ['b', 'c']) {
      await page.locator(`[data-files-mode] [data-file-node="${join(cwd, `${n}.txt`)}"]`).click()
      await expect(tab(n)).toBeVisible({ timeout: 30_000 })
    }
    await tab('b').locator('button').first().click()
    await expect(tab('b')).toHaveAttribute('data-editor-tab-active', '')
    const lines = area.locator('.monaco-editor .view-lines')
    await expect(lines).toContainText('b one', { timeout: 30_000 })
    await lines.click()
    await page.keyboard.press('Control+End')
    await page.keyboard.type('b three\n')
    await expect(tab('b').locator('[data-editor-dirty]')).toHaveCount(1)
    await choose('c', 'Close all')
    const prompt = page.locator('[role="dialog"]', { hasText: 'Save b.txt?' })
    await expect(prompt).toBeVisible()
    await prompt.locator('[data-editor-discard]').click()
    await expect(area).toHaveCount(0)
    // Close to the left: nothing to the left of the first does nothing; a Shift+F10 on the focused tab opens the same menu.
    await openAll()
    await tab('a').locator('button').first().focus()
    await page.keyboard.press('Shift+F10')
    await page.getByRole('menuitem', { name: 'Close to the left' }).click()
    await expect(area.locator('[data-editor-tab]')).toHaveCount(3)
    await choose('c', 'Close to the left')
    await expect(area.locator('[data-editor-tab]')).toHaveCount(1)
    await expect(tab('c')).toHaveAttribute('data-editor-tab-active', '')
    // From the keyboard alone (Codex's rounds): the Menu key opens the menu and Escape closes it, the tabs untouched; a letter
    // typed into the open menu is the menu's and not the page's (S would share the session); ↓ and Enter pick Close others.
    await showView(page, 'explorer')
    for (const n of ['a', 'b']) {
      await page.locator(`[data-files-mode] [data-file-node="${join(cwd, `${n}.txt`)}"]`).click()
      await expect(tab(n)).toBeVisible({ timeout: 30_000 })
    }
    await expect(area.locator('[data-editor-tab]')).toHaveCount(3)
    const menu = page.getByRole('menu')
    await tab('b').locator('button').first().focus()
    await page.keyboard.press('ContextMenu')
    await expect(menu).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(menu).toBeHidden()
    await expect(area.locator('[data-editor-tab]')).toHaveCount(3)
    await expect(area).toHaveAttribute('data-editor-area', 'open')
    await tab('b').locator('button').first().focus()
    await page.keyboard.press('Shift+F10')
    await expect(menu).toBeVisible()
    await page.keyboard.type('s')
    await page.waitForTimeout(300)
    await expect(page.getByRole('dialog')).toHaveCount(0)
    await expect(menu).toBeVisible()
    // ↓ until Close others has the focus (a menu opened from the keyboard focuses its first item already), then Enter.
    const others = page.getByRole('menuitem', { name: 'Close others' })
    for (let i = 0; i < 5 && !(await others.evaluate((el) => el === document.activeElement)); i++) await page.keyboard.press('ArrowDown')
    await expect(others).toBeFocused()
    await page.keyboard.press('Enter')
    await expect(menu).toBeHidden()
    await expect(area.locator('[data-editor-tab]')).toHaveCount(1)
    await expect(tab('b')).toHaveAttribute('data-editor-tab-active', '')
    // A clean tab closed from its own menu takes its menu away before the menu says it closed (Codex's third round): the
    // sidebar row's menu, and the next tab menu, still close on Escape afterwards.
    await showView(page, 'explorer')
    await page.locator(`[data-files-mode] [data-file-node="${join(cwd, 'a.txt')}"]`).click()
    await expect(area.locator('[data-editor-tab]')).toHaveCount(2)
    await choose('a', 'Close')
    await expect(area.locator('[data-editor-tab]')).toHaveCount(1)
    await expect(menu).toBeHidden()
    // The test's own session's row, by its id: the sidebar's first row may be an item of Needs you, which has no menu of its own.
    await page.locator(`[data-sidebar-row="s:${s.id}"]`).click({ button: 'right' })
    await expect(menu).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(menu).toBeHidden()
    await tab('b').locator('button').first().focus()
    await page.keyboard.press('Shift+F10')
    await expect(menu).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(menu).toBeHidden()
    await expect(area.locator('[data-editor-tab]')).toHaveCount(1)
    // Shift let go before F10 (Codex's fourth round): the release still opens the menu; F10 alone, no Shift, opens nothing.
    await tab('b').locator('button').first().focus()
    await page.keyboard.down('Shift')
    await page.keyboard.down('F10')
    await page.keyboard.up('Shift')
    await page.keyboard.up('F10')
    await expect(menu).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(menu).toBeHidden()
    await tab('b').locator('button').first().focus()
    await page.keyboard.press('F10')
    await page.waitForTimeout(300)
    await expect(menu).toHaveCount(0)
    // A chord pressed on one tab and released on another opens nothing (Codex's fifth round); the next chord on the tab that
    // has the focus opens its own menu.
    await showView(page, 'explorer')
    await page.locator(`[data-files-mode] [data-file-node="${join(cwd, 'a.txt')}"]`).click()
    await expect(area.locator('[data-editor-tab]')).toHaveCount(2)
    await tab('a').locator('button').first().focus()
    await page.keyboard.down('Shift')
    await page.keyboard.down('F10')
    await page.keyboard.up('Shift')
    await tab('b').locator('button').first().focus()
    await page.keyboard.up('F10')
    await page.waitForTimeout(300)
    await expect(menu).toHaveCount(0)
    await page.keyboard.press('Shift+F10')
    await expect(menu).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(menu).toBeHidden()
    await expect(area.locator('[data-editor-tab]')).toHaveCount(2)
  } finally {
    await api.stopSession(s.id)
  }
})

// Codex's round on #330: a clean tab the menu closes takes its conflict and compare state with it, as its X does; a tab that
// kept them would open again into the old compare and save its stale text over the disk's.
test('a clean tab closed from the menu drops its compare state, so the file opens again as it is on disk', async ({ page, api, state }) => {
  const cwd = join(state.root, 'tabs-compare')
  mkdirSync(cwd, { recursive: true })
  for (const n of ['a', 'b']) writeFileSync(join(cwd, `${n}.txt`), `${n} one\n`)
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e tabs compare', cwd })
  const area = page.locator('[data-editor-area]')
  const tab = (n: string) => area.locator(`[data-editor-tab="file:${join(cwd, `${n}.txt`)}"]`)
  const open = async (n: string) => {
    await page.locator(`[data-files-mode] [data-file-node="${join(cwd, `${n}.txt`)}"]`).click()
    await expect(tab(n)).toHaveAttribute('data-editor-tab-active', '', { timeout: 30_000 })
  }
  try {
    await page.goto(`/sessions/${s.id}`)
    await expect(page.locator('.terminal-host .xterm-helper-textarea').first()).toBeFocused({ timeout: 30_000 })
    await showView(page, 'explorer')
    await open('a')
    await open('b')
    await open('a')
    const lines = area.locator('.monaco-editor .view-lines')
    await expect(lines).toContainText('a one', { timeout: 30_000 })
    // The file changes on disk; a save of the unchanged tab is refused; Compare opens the compare view.
    writeFileSync(join(cwd, 'a.txt'), 'a one\nfrom the agent\n')
    await lines.click()
    await page.keyboard.press('Control+s')
    const conflict = area.locator('[data-editor-conflict]')
    await expect(conflict).toBeVisible({ timeout: 15_000 })
    await conflict.locator('[data-editor-compare]').click()
    await expect(area.locator('[data-editor-comparing]')).toBeVisible()
    // Close others from b closes a without asking (nothing unsaved); a opens again from disk, no compare, no conflict.
    await tab('b').click({ button: 'right' })
    await page.getByRole('menuitem', { name: 'Close others' }).click()
    await expect(area.locator('[data-editor-tab]')).toHaveCount(1)
    await expect(area.locator('[data-editor-comparing]')).toHaveCount(0)
    await open('a')
    await expect(lines).toContainText('from the agent', { timeout: 30_000 })
    await expect(area.locator('[data-editor-comparing]')).toHaveCount(0)
    await expect(conflict).toHaveCount(0)
    await expect(area.locator('[data-editor-dirty]')).toHaveCount(0)
  } finally {
    await api.stopSession(s.id)
  }
})
