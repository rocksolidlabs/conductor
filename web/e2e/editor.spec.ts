import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, showView, test, type Session } from './fixtures'

// The editor area (design 4b, 4c, 4f): a file chosen in the Files pane opens
// in Monaco above a shrunk terminal, with a tab strip, the breadcrumb, the
// position, copy path and open raw; several files are tabs; T folds the
// editor to its strip and brings it back; closing the last tab takes the
// area away; a typed path:line opens at the line; an image and a binary
// file show their states.
test('a file opens in the editor above the terminal; tabs, folding, closing, a line, an image and a binary file', async ({ page, api, state }) => {
  const cwd = join(state.root, 'files-editor')
  mkdirSync(join(cwd, 'internal', 'api'), { recursive: true })
  writeFileSync(join(cwd, 'internal', 'api', 'users.go'), 'package api\n\n// ListUsers answers GET /v1/users with a page of users.\nfunc ListUsers() {}\n\n// GetUser answers GET /v1/users/{id}.\nfunc GetUser() {}\n')
  writeFileSync(join(cwd, 'README.md'), '# editor\n')
  writeFileSync(join(cwd, 'logo.png'), Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAYAAABytg0kAAAAC0lEQVQI12NgQAcAABIAAe+JVKQAAAAASUVORK5CYII=', 'base64'))
  writeFileSync(join(cwd, 'fixtures.bin'), Buffer.from([0, 1, 2, 3, 255, 254, 0, 0, 7, 9]))
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'editor', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    await showView(page, 'explorer')
    const tree = page.locator('[data-files-tree]')
    await expect(tree.locator(`[data-file-node="${cwd}/internal"]`)).toBeVisible({ timeout: 30_000 })
    await expect(page.locator('[data-editor-area]')).toHaveCount(0)
    // A file from the tree: Monaco above the terminal, the pane still the tree.
    await tree.locator(`[data-file-node="${cwd}/internal"]`).click()
    await tree.locator(`[data-file-node="${cwd}/internal/api"]`).click()
    await tree.locator(`[data-file-node="${cwd}/internal/api/users.go"]`).click()
    const area = page.locator('[data-editor-area]')
    await expect(area).toHaveAttribute('data-editor-area', 'open')
    await expect(area.locator('[data-editor-tab]')).toHaveCount(1)
    await expect(area.locator('[data-editor-state]')).toHaveAttribute('data-editor-state', 'text', { timeout: 30_000 })
    await expect(area.locator('.monaco-editor .view-lines')).toContainText('ListUsers', { timeout: 30_000 })
    await expect(area.locator('[data-editor-crumbs]')).toContainText('users.go')
    await expect(area.locator('[data-editor-pos]')).toHaveText('Ln 1, Col 1')
    await expect(area.locator('[data-editor-raw]')).toBeVisible()
    await expect(page.locator('[data-files-mode]')).toHaveAttribute('data-files-mode', 'tree')
    await expect(page.locator('.terminal-host .xterm-screen').first()).toBeVisible()
    // A second file: two tabs, the new one active; the first by its tab.
    await tree.locator(`[data-file-node="${cwd}/README.md"]`).click()
    await expect(area.locator('[data-editor-tab]')).toHaveCount(2)
    await expect(area.locator('[data-editor-tab-active]')).toContainText('README.md')
    await area.locator(`[data-editor-tab="file:${cwd}/internal/api/users.go"] button`).first().click()
    await expect(area.locator('[data-editor-tab-active]')).toContainText('users.go')
    // T folds the editor to its strip; T again brings it back.
    await page.locator('body').click({ position: { x: 5, y: 5 } })
    await page.keyboard.press('t')
    await expect(area).toHaveAttribute('data-editor-area', 'folded')
    await expect(area.locator('[data-editor-count]')).toHaveText('2 files open')
    await page.keyboard.press('t')
    await expect(area).toHaveAttribute('data-editor-area', 'open')
    // A typed path:line opens at the line, marked.
    const box = page.locator('[data-files-box] input, input[data-files-box]').first()
    await box.fill('internal/api/users.go:7')
    await box.press('Enter')
    await expect(area.locator('[data-editor-pos]')).toHaveText('Ln 7, Col 1', { timeout: 15_000 })
    await expect(area.locator('[data-editor-tab]')).toHaveCount(2)
    // An image on a checker with its size; a binary file says so.
    await tree.locator(`[data-file-node="${cwd}/logo.png"]`).click()
    await expect(area.locator('[data-editor-state]')).toHaveAttribute('data-editor-state', 'image', { timeout: 15_000 })
    await tree.locator(`[data-file-node="${cwd}/fixtures.bin"]`).click()
    await expect(area.locator('[data-editor-state]')).toHaveAttribute('data-editor-state', 'binary', { timeout: 15_000 })
    await expect(area).toContainText('Binary file; nothing to show')
    // Closing tabs, the last close takes the area away.
    await expect(area.locator('[data-editor-tab]')).toHaveCount(4)
    await area.locator(`[data-editor-close="file:${cwd}/fixtures.bin"]`).click({ force: true })
    await expect(area.locator('[data-editor-tab]')).toHaveCount(3)
    await area.locator('[data-editor-close-all]').click()
    await expect(page.locator('[data-editor-area]')).toHaveCount(0)
  } finally {
    await api.stopSession(s.id)
  }
})

// The Yard's focused tile and a guest's join page open a file the same way
// (design 4g): the editor above the terminal, the Files pane beside it in
// place of the old slide-over; a view-only guest gets the pane marked read
// only and the editor read-only. On a phone the editor takes the column and
// the terminal folds to a bar that brings it back.
test("the Yard's focused tile and a guest's page open files the same way; on a phone the terminal folds to a bar", async ({ page, api, state, browser }) => {
  const cwd = join(state.root, 'files-editor-yard')
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'notes.md'), '# notes\n\nline three\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'yard editor', cwd })
  try {
    await page.goto(`/yard?focus=${s.id}`)
    const aside = page.locator('[data-files-aside]')
    await expect(aside.locator(`[data-file-node="${cwd}/notes.md"]`)).toBeVisible({ timeout: 30_000 })
    await aside.locator(`[data-file-node="${cwd}/notes.md"]`).click()
    const area = page.locator('[data-editor-area]')
    await expect(area).toHaveAttribute('data-editor-area', 'open')
    await expect(area.locator('.monaco-editor .view-lines')).toContainText('line three', { timeout: 30_000 })
    await expect(page.locator('.terminal-host .xterm-screen').first()).toBeVisible()
    // Esc folds the editor first; a second Esc leaves the focus.
    await page.locator('body').click({ position: { x: 5, y: 5 } })
    await page.keyboard.press('Escape')
    await expect(area).toHaveAttribute('data-editor-area', 'folded')
    // On a phone the terminal folds to a bar; a tap brings it back (the editor folds).
    await area.locator('[data-editor-unfold]').click()
    await page.setViewportSize({ width: 390, height: 844 })
    const bar = page.locator('[data-terminal-bar]')
    await expect(bar).toBeVisible()
    await expect(bar).toContainText('tap to bring it up')
    // Above the bottom bar, not under it: the panels stop at the layout's padding below lg.
    const barBox = (await bar.boundingBox())!
    const bottomBox = (await page.locator('[data-bottom-bar]').boundingBox())!
    expect(barBox.y + barBox.height).toBeLessThanOrEqual(bottomBox.y)
    await bar.click()
    await expect(area).toHaveAttribute('data-editor-area', 'folded')
    await expect(bar).toHaveCount(0)
    await page.setViewportSize({ width: 1440, height: 900 })

    // A view-only guest: the Files pane marked read only, the file read-only in the editor.
    const { token } = await api.ok<{ token: string }>('POST', `/api/sessions/${s.id}/links`, { role: 'view', ttlSeconds: 3600 })
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
    await ctx.addInitScript(() => localStorage.setItem('conductor.displayName', 'Jane'))
    const jane = await ctx.newPage()
    await jane.goto(`/join/${token}`)
    await jane.getByRole('button', { name: /^Join/ }).first().click()
    const guestAside = jane.locator('[data-files-aside]')
    await expect(guestAside.locator('[data-files-readonly]')).toBeVisible({ timeout: 30_000 })
    await expect(guestAside.locator(`[data-file-node="${cwd}/notes.md"]`)).toBeVisible({ timeout: 30_000 })
    await guestAside.locator(`[data-file-node="${cwd}/notes.md"]`).click()
    const guestArea = jane.locator('[data-editor-area]')
    await expect(guestArea.locator('.monaco-editor .view-lines')).toContainText('line three', { timeout: 30_000 })
    await expect(guestArea.locator('[data-editor-readonly]')).toBeVisible()
    await ctx.close()
  } finally {
    await api.stopSession(s.id)
  }
})
