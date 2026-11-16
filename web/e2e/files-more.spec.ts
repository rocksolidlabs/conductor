import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, showView, test, type Session } from './fixtures'

// The rest of the Files tab (round 12): the Explorer's filter reaches
// folders not opened yet (a find on the session's machine, .git and
// node_modules left out), and the Yard's focused tile shows Touched.
test('the Explorer finds files in folders not opened yet; the Yard\'s focused tile shows Touched', async ({ page, api, state }) => {
  const cwd = join(state.root, 'files-more')
  mkdirSync(join(cwd, 'internal', 'api'), { recursive: true })
  mkdirSync(join(cwd, 'node_modules', 'users'), { recursive: true })
  writeFileSync(join(cwd, 'internal', 'api', 'users.go'), 'package api\n\nfunc Users() {}\n')
  writeFileSync(join(cwd, 'node_modules', 'users', 'index.js'), 'module.exports = {}\n')
  writeFileSync(join(cwd, 'README.md'), '# more\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'more', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    await showView(page, 'explorer')
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator(`[data-file-node="${cwd}/internal"]`)).toBeVisible({ timeout: 30_000 })
    // internal is not opened: the tree has nothing named users, the find has the file below.
    await pane.locator('[data-files-box] input, input[data-files-box]').first().fill('users')
    await expect(pane.locator('[data-files-found-head]')).toBeVisible({ timeout: 15_000 })
    const found = pane.locator(`[data-file-found="${cwd}/internal/api/users.go"]`)
    await expect(found).toBeVisible()
    await expect(found).toContainText('internal/api/')
    await expect(pane.locator(`[data-file-found="${cwd}/node_modules/users/index.js"]`)).toHaveCount(0)
    await found.click()
    await expect(page.locator('[data-editor-area] .monaco-editor .view-lines')).toContainText('func Users()', { timeout: 30_000 })
    // The Yard's focused tile: Touched from what its connection reports.
    await page.goto(`/yard?focus=${s.id}`)
    const aside = page.locator('[data-files-aside]')
    await expect(aside.locator(`[data-file-node="${cwd}/README.md"]`)).toBeVisible({ timeout: 30_000 })
    await api.ok('POST', `/api/sessions/${s.id}/events`, { type: 'file', op: 'edit', path: 'internal/api/users.go', tool: 'Edit' })
    await api.ok('POST', `/api/sessions/${s.id}/events`, { type: 'file', op: 'read', path: 'README.md', tool: 'Read' })
    // The aside has the three Files views alone (4h): the touched files show as the Explorer's dots.
    await expect(aside.locator(`[data-file-node="${cwd}/README.md"] [data-file-touched]`)).toBeVisible({ timeout: 15_000 })
    await expect(aside.locator('[data-rail-item]')).toHaveCount(3)
  } finally {
    await api.stopSession(s.id)
  }
})
