import { appendFileSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, git, showView, test, type Session } from './fixtures'

// git's settings and credentials often hold access tokens (a remote URL with
// one in it): a view-only guest who types .git/config, .git/HEAD or
// .git-credentials into the Files box is told git's files are for
// controllers, and the token never shows; the owner opens them as before,
// and the guest reads the rest. A
// repository whose core.worktree points outside its folder lists no changes
// from there: the Changes section says why.
test("git's files are for controllers; a working tree elsewhere lists no changes", async ({ page, api, state, browser }) => {
  const secret = 'ghp-e2e-token-in-the-remote-url'
  const cwd = join(state.root, 'files-git-settings')
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'notes.md'), '# notes\n\nline three\n')
  git(cwd, 'init', '-q')
  appendFileSync(join(cwd, '.git', 'config'), `[remote "origin"]\n\turl = https://me:${secret}@example.com/r.git\n`)
  writeFileSync(join(cwd, '.git-credentials'), `https://me:${secret}@example.com\n`)
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'git settings', cwd })
  try {
    // The owner reads the file.
    await page.goto(`/sessions/${s.id}`)
    await page.locator('[data-inspector] [data-rail-item="explorer"]').click()
    await expect(page.locator(`[data-file-node="${cwd}/notes.md"]`)).toBeVisible({ timeout: 30_000 })
    const box = page.locator('[data-files-box] input, input[data-files-box]').first()
    await box.fill('.git/config')
    await box.press('Enter')
    const area = page.locator('[data-editor-area]')
    await expect(area.locator('[data-editor-state]')).toHaveAttribute('data-editor-state', 'text', { timeout: 30_000 })
    await expect(area.locator('.monaco-editor .view-lines')).toContainText('remote', { timeout: 30_000 })

    // A view-only guest is told the file is for controllers, and sees no token.
    const { token } = await api.ok<{ token: string }>('POST', `/api/sessions/${s.id}/links`, { role: 'view', ttlSeconds: 3600 })
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
    await ctx.addInitScript(() => localStorage.setItem('conductor.displayName', 'Jane'))
    const jane = await ctx.newPage()
    await jane.goto(`/join/${token}`)
    await jane.getByRole('button', { name: /^Join/ }).first().click()
    const aside = jane.locator('[data-files-aside]')
    await expect(aside.locator(`[data-file-node="${cwd}/notes.md"]`)).toBeVisible({ timeout: 30_000 })
    const guestArea = jane.locator('[data-editor-area]')
    for (const p of ['.git/config', '.git/HEAD', '.git-credentials']) {
      const guestBox = aside.locator('[data-files-box] input, input[data-files-box]').first()
      await guestBox.fill(p)
      await guestBox.press('Enter')
      await expect(guestArea.locator('[data-editor-state]')).toHaveAttribute('data-editor-state', 'refused', { timeout: 30_000 })
      await expect(guestArea.locator('[data-editor-refused]')).toContainText("Git's files are for controllers")
      await expect(guestArea.locator('[data-editor-refused]')).toContainText('view-only link does not open them')
      await expect(jane.locator('body')).not.toContainText(secret)
    }
    // The rest of the folder still reads.
    await aside.locator(`[data-file-node="${cwd}/notes.md"]`).click()
    await expect(guestArea.locator('.monaco-editor .view-lines')).toContainText('line three', { timeout: 30_000 })
    await ctx.close()
  } finally {
    await api.stopSession(s.id)
  }

  // A working tree a core.worktree puts beside the folder: no changes from there, and the reason.
  const outside = join(state.root, 'files-git-elsewhere-tree')
  const folder = join(state.root, 'files-git-elsewhere')
  mkdirSync(outside, { recursive: true })
  mkdirSync(folder, { recursive: true })
  writeFileSync(join(outside, 'outside-name.txt'), 'one\ntwo\n')
  writeFileSync(join(folder, 'here.txt'), 'here\n')
  git(folder, 'init', '-q')
  git(folder, 'config', 'core.worktree', outside)
  const e = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'elsewhere', cwd: folder })
  try {
    await page.goto(`/sessions/${e.id}`)
    // The panel was left open on Explorer, and is kept so across the load (P11): Explorer shown, not clicked closed.
    await showView(page, 'explorer')
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator(`[data-file-node="${folder}/here.txt"]`)).toBeVisible({ timeout: 30_000 })
    await page.locator('[data-inspector] [data-rail-item="changes"]').click()
    await expect(pane.locator('[data-files-changes-error]')).toContainText('outside the folder', { timeout: 15_000 })
    await expect(pane.locator('[data-files-changes] [data-change]')).toHaveCount(0)
    await expect(page.locator('body')).not.toContainText('outside-name.txt')
  } finally {
    await api.stopSession(e.id)
  }
})
