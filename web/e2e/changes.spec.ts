import { execSync } from 'node:child_process'
import { mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, showView, test, type Session } from './fixtures'

// The Changes section (design 4d): git status of the working directory with
// each file's lines, refreshed as the agent works; a change opens as a diff
// in the Monaco diff editor, side by side or inline, with Open file beside
// it; the Explorer marks changed files; a directory with no repository says
// so.
function git(dir: string, args: string) {
  execSync(`git -c user.name=e2e -c user.email=e2e@conductor.invalid -c commit.gpgsign=false ${args}`, { cwd: dir, stdio: 'ignore' })
}

test('Changes lists the status with lines, opens a diff, marks the Explorer; no repository says so', async ({ page, api, state }) => {
  const cwd = join(state.root, 'files-changes')
  mkdirSync(join(cwd, 'internal', 'api'), { recursive: true })
  writeFileSync(join(cwd, 'README.md'), '# changes\n\none\ntwo\n')
  writeFileSync(join(cwd, 'internal', 'api', 'users.go'), 'package api\n')
  writeFileSync(join(cwd, 'old.txt'), 'gone\n')
  git(cwd, 'init -q')
  git(cwd, 'add -A')
  git(cwd, 'commit -q -m init')
  writeFileSync(join(cwd, 'README.md'), '# changes\n\none\nthree\nfour\n')
  writeFileSync(join(cwd, 'internal', 'api', 'users_test.go'), 'package api\n\nimport "testing"\n\nfunc TestA(t *testing.T) {}\n')
  rmSync(join(cwd, 'old.txt'))
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'changes', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    await showView(page, 'explorer')
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator('[data-files-tree] [data-file-node]').first()).toBeVisible({ timeout: 30_000 })
    // The Explorer marks the modified file once the status is read; the section's count says three.
    await expect(pane.locator(`[data-file-node="${cwd}/README.md"] [data-file-mark]`)).toHaveText('M', { timeout: 15_000 })
    await expect(page.locator('[data-inspector] [data-rail-count="changes"]')).toHaveText('3')
    await page.locator('[data-inspector] [data-rail-item="changes"]').click()
    const rows = pane.locator('[data-files-changes] [data-change]')
    await expect(rows).toHaveCount(3)
    const readme = pane.locator(`[data-change="${cwd}/README.md"]`)
    await expect(readme).toHaveAttribute('data-change-status', 'M')
    await expect(readme).toContainText('+2')
    await expect(readme).toContainText('−1')
    await expect(pane.locator(`[data-change="${cwd}/internal/api/users_test.go"]`)).toHaveAttribute('data-change-status', '?')
    await expect(pane.locator(`[data-change="${cwd}/old.txt"]`)).toHaveAttribute('data-change-status', 'D')
    await expect(pane.locator('[data-files-changes-foot]')).toContainText('Refreshed as the agent works')
    await expect(pane.locator('[data-files-changes-foot]')).toContainText('+7')
    // The modified file opens as a diff: both sides in Monaco's diff editor, inline on request, the file itself one click away.
    await readme.click()
    const area = page.locator('[data-editor-area]')
    await expect(area.locator('[data-editor-state]')).toHaveAttribute('data-editor-state', 'diff', { timeout: 30_000 })
    await expect(area.locator('[data-editor-against]')).toHaveText('working directory vs HEAD')
    await expect(area.locator('[data-diff-editor] .monaco-diff-editor')).toBeVisible({ timeout: 30_000 })
    await expect(area.locator('[data-diff-editor]')).toContainText('three', { timeout: 30_000 })
    await area.locator('[data-editor-inline]').click()
    await expect(area.locator('[data-diff-editor] .monaco-diff-editor')).toBeVisible()
    await area.locator('[data-editor-open-file]').click()
    await expect(area.locator('[data-editor-tab]')).toHaveCount(2)
    await expect(area.locator('[data-editor-state]')).toHaveAttribute('data-editor-state', 'text', { timeout: 30_000 })
    // The agent works on: a new change shows within the refresh.
    writeFileSync(join(cwd, 'internal', 'api', 'users.go'), 'package api\n\nfunc B() {}\n')
    await expect(rows).toHaveCount(4, { timeout: 15_000 })
  } finally {
    await api.stopSession(s.id)
  }
  // No repository.
  const plain = join(state.root, 'files-plain')
  mkdirSync(plain, { recursive: true })
  writeFileSync(join(plain, 'notes.txt'), 'hi\n')
  const p = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'plain', cwd: plain })
  try {
    await page.goto(`/sessions/${p.id}`)
    // The panel was left open on Explorer, and is kept so across the load (P11): Explorer shown, not clicked closed.
    await showView(page, 'explorer')
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator('[data-files-tree] [data-file-node]').first()).toBeVisible({ timeout: 30_000 })
    await page.locator('[data-inspector] [data-rail-item="changes"]').click()
    await expect(pane.locator('[data-files-not-repo]')).toContainText('Not a git repository', { timeout: 15_000 })
  } finally {
    await api.stopSession(p.id)
  }
})
