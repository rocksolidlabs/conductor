import { mkdirSync, renameSync, symlinkSync, unlinkSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, showView, test, type Session } from './fixtures'

// The Files tab serves the folder its session started in, which the server
// keeps open: when the session's path comes to name another folder (the
// folder moved away and a link to another put in its place), the tree says
// so rather than listing where the path leads now; with the folder back at
// its path, Retry lists it again.
test('the Files tab keeps to the folder the session started in', async ({ page, api, state }) => {
  const cwd = join(state.root, 'files-folder')
  const other = join(state.root, 'files-folder-other')
  mkdirSync(cwd, { recursive: true })
  mkdirSync(other, { recursive: true })
  writeFileSync(join(cwd, 'mine.md'), '# mine\n')
  writeFileSync(join(other, 'elsewhere.md'), '# elsewhere\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'folder', cwd })
  try {
    renameSync(cwd, `${cwd}.moved`)
    symlinkSync(other, cwd)
    await page.goto(`/sessions/${s.id}`)
    await showView(page, 'explorer')
    const pane = page.locator('[data-files-mode]')
    const error = pane.locator('[data-files-error]')
    await expect(error).toContainText('the session\'s folder is no longer at its path', { timeout: 30_000 })
    await expect(pane.locator(`[data-file-node="${cwd}/elsewhere.md"]`)).toHaveCount(0)
    // The folder back at its path.
    unlinkSync(cwd)
    renameSync(`${cwd}.moved`, cwd)
    await error.getByRole('button', { name: 'Retry' }).click()
    await expect(pane.locator(`[data-file-node="${cwd}/mine.md"]`)).toBeVisible({ timeout: 30_000 })
  } finally {
    await api.stopSession(s.id)
  }
})
