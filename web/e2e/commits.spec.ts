import { execSync } from 'node:child_process'
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, showView, test, type Session } from './fixtures'

// The Commits section (design 4e): the commits on the branch since the
// session started, newest first, each with its subject, short id, author
// and age; a commit opens to its body and its files with their lines; a
// file opens as its diff against the commit's parent, in a tab named by the
// short id; a commit made while the section shows appears on its refresh;
// nothing from before the session lists.
function git(dir: string, args: string) {
  execSync(`git -c user.name=Ada -c user.email=ada@conductor.invalid -c commit.gpgsign=false ${args}`, { cwd: dir, stdio: 'ignore' })
}

test('Commits lists the commits since the session started, opens one to its files and a file to its diff', async ({ page, api, state }) => {
  const cwd = join(state.root, 'files-commits')
  mkdirSync(join(cwd, 'internal', 'api'), { recursive: true })
  writeFileSync(join(cwd, 'README.md'), '# commits\n')
  writeFileSync(join(cwd, 'internal', 'api', 'users.go'), 'package api\n\nfunc A() {}\n')
  git(cwd, 'init -q -b main')
  git(cwd, 'add -A')
  git(cwd, 'commit -q -m "before the session"')
  await new Promise((r) => setTimeout(r, 1100)) // the earlier commit's second is past
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'commits', cwd })
  try {
    // The agent commits twice once the session runs.
    writeFileSync(join(cwd, 'internal', 'api', 'users.go'), 'package api\n\nfunc A() {}\nfunc B() {}\nfunc C() {}\n')
    writeFileSync(join(cwd, 'internal', 'api', 'users_test.go'), 'package api\n\nimport "testing"\n\nfunc TestB(t *testing.T) {}\n')
    git(cwd, 'add -A')
    git(cwd, 'commit -q -m "users: B and C" -m "Two more handlers, with a test."')
    writeFileSync(join(cwd, 'README.md'), '# commits\n\nThe users API.\n')
    git(cwd, 'commit -q -am "readme: say what it is"')
    const sha = execSync('git rev-parse HEAD~1', { cwd }).toString().trim()
    await page.goto(`/sessions/${s.id}`)
    await showView(page, 'explorer')
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator('[data-files-tree] [data-file-node]').first()).toBeVisible({ timeout: 30_000 })
    await page.locator('[data-inspector] [data-rail-item="commits"]').click()
    const rows = pane.locator('[data-files-commits] [data-commit]')
    await expect(rows).toHaveCount(2, { timeout: 15_000 })
    await expect(page.locator('[data-inspector] [data-rail-count="commits"]')).toHaveText('2')
    await expect(rows.nth(0)).toContainText('readme: say what it is')
    await expect(rows.nth(1)).toContainText('users: B and C')
    await expect(rows.nth(1)).toContainText(sha.slice(0, 7))
    await expect(rows.nth(1)).toContainText('Ada')
    await expect(pane.locator('[data-files-commits-head]')).toContainText(/2 commits on main since \d{2}:\d{2}/)
    await expect(pane.locator('[data-files-commits-foot]')).toContainText('Nothing is pushed from Conductor')
    await expect(pane.locator('[data-files-commits]')).not.toContainText('before the session')
    // A commit opens to its body and its files with their lines.
    await rows.nth(1).click()
    const files = pane.locator(`[data-commit-files="${sha}"]`)
    await expect(files.locator('[data-commit-body]')).toHaveText('Two more handlers, with a test.', { timeout: 15_000 })
    const users = files.locator(`[data-commit-change="${cwd}/internal/api/users.go"]`)
    await expect(users).toHaveAttribute('data-change-status', 'M')
    await expect(users).toContainText('+2')
    await expect(files.locator(`[data-commit-change="${cwd}/internal/api/users_test.go"]`)).toHaveAttribute('data-change-status', 'A')
    // A file opens as its diff against the commit's parent, in a tab named by the short id.
    await users.click()
    const area = page.locator('[data-editor-area]')
    await expect(area.locator('[data-editor-state]')).toHaveAttribute('data-editor-state', 'diff', { timeout: 30_000 })
    await expect(area.locator('[data-editor-tab-active]')).toContainText(`${sha.slice(0, 7)} users.go`)
    await expect(area.locator('[data-editor-against]')).toHaveText(new RegExp(`^${sha.slice(0, 7)} vs its parent [0-9a-f]{7}$`))
    await expect(area.locator('[data-diff-editor]')).toContainText('func C()', { timeout: 30_000 })
    // The added test file opens with nothing on the parent's side.
    await files.locator(`[data-commit-change="${cwd}/internal/api/users_test.go"]`).click()
    await expect(area.locator('[data-editor-tab]')).toHaveCount(2)
    await expect(area.locator('[data-diff-editor]')).toContainText('TestB', { timeout: 30_000 })
    // A commit made while the section shows appears on its refresh.
    writeFileSync(join(cwd, 'NOTES.md'), 'notes\n')
    git(cwd, 'add -A')
    git(cwd, 'commit -q -m "notes: a third commit"')
    await pane.locator('[data-files-commits-head] button[aria-label="Refresh"]').click()
    await expect(rows).toHaveCount(3, { timeout: 15_000 })
    await expect(rows.nth(0)).toContainText('notes: a third commit')
  } finally {
    await api.stopSession(s.id)
  }
})
