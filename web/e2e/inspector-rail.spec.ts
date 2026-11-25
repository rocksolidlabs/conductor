import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Session } from './fixtures'

// The inspector rail (design 4h, #301): one rail on the inspector's outer
// edge holds People, Explorer, Changes, Commits, Activity and Chat; each
// item opens its view, the active item collapses the panel, the rail stays
// with its counts while the panel is collapsed; the header's Files and Chat
// buttons select rail items; Touched is Activity's Files filter; the Yard's
// aside carries the three Files items on a rail of its own.
test('each rail item opens its view, the active one collapses the panel, the counts stay on the rail', async ({ page, api, state }) => {
  const cwd = join(state.root, 'inspector-rail')
  mkdirSync(join(cwd, 'src'), { recursive: true })
  writeFileSync(join(cwd, 'README.md'), '# rail\n')
  writeFileSync(join(cwd, 'src', 'main.go'), 'package main\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e inspector rail', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    const inspector = page.locator('[data-inspector]')
    const rail = inspector.locator('[data-inspector-rail]')
    const item = (id: string) => rail.locator(`[data-rail-item="${id}"]`)
    await expect(rail).toBeVisible()
    // People holds the people's chat and who is here (docs/plans/chat-or-terminal.md, design 11): five items.
    await expect(rail.locator('[data-rail-item]')).toHaveCount(5)
    await expect(rail.locator('[data-rail-item="chat"]')).toHaveCount(0)
    // The panel is closed as the page opens (P11): the rail and its items shown, none pressed. People opens it, pressed, with its bar.
    await expect(inspector).toHaveAttribute('data-inspector-open', 'false')
    await expect(rail.locator('[data-rail-item][aria-pressed="true"]')).toHaveCount(0)
    await item('people').click()
    await expect(item('people')).toHaveAttribute('aria-pressed', 'true')
    await expect(item('people').locator('[data-rail-active]')).toBeVisible()
    await expect(inspector.locator('[data-inspector-head="people"]')).toContainText('People')
    // Explorer lists the folder; its head names the view and the folder.
    await item('explorer').click()
    await expect(inspector.locator('[data-files-explorer-head]')).toContainText('Explorer')
    await expect(inspector.locator(`[data-file-node="${cwd}/README.md"]`)).toBeVisible({ timeout: 30_000 })
    // Commits: a count pill on the rail once the log is read, the head's second line naming the branch and the count.
    await item('commits').click()
    await expect(inspector.locator('[data-files-commits-head]')).toContainText('Commits')
    await expect(item('commits')).toHaveAttribute('aria-pressed', 'true')
    // The active item collapses the panel; the rail stays, its items unpressed, and opens again on a click.
    await item('commits').click()
    await expect(inspector).toHaveAttribute('data-inspector-open', 'false')
    await expect(inspector.locator('[data-inspector-panel]')).toHaveCount(0)
    await expect(rail).toBeVisible()
    await expect(item('commits')).toHaveAttribute('aria-pressed', 'false')
    await item('changes').click()
    await expect(inspector).toHaveAttribute('data-inspector-open', 'true')
    await expect(inspector.locator('[data-files-changes-head]')).toContainText('Changes')
    // The toggle at the foot collapses and opens; the choice outlives a reload.
    await rail.locator('[data-rail-toggle]').click()
    await expect(inspector).toHaveAttribute('data-inspector-open', 'false')
    await page.reload()
    await expect(page.locator('[data-inspector]')).toHaveAttribute('data-inspector-open', 'false')
    await page.locator('[data-inspector] [data-rail-toggle]').click()
    await expect(page.locator('[data-inspector]')).toHaveAttribute('data-inspector-open', 'true')
    // At xl the bar has no Files or Chat button (#329): the rail's items are the way in. The toggle, no view chosen since the reload,
    // opened on Explorer; Explorer collapses when it already shows; Chat selects the chat item.
    await expect(page.locator('button[aria-label="Files"]')).toBeHidden()
    await expect(page.locator('[data-chat-button]')).toBeHidden()
    await expect(page.locator('[data-inspector] [data-rail-item="explorer"]')).toHaveAttribute('aria-pressed', 'true')
    await page.locator('[data-inspector] [data-rail-item="explorer"]').click()
    await expect(page.locator('[data-inspector]')).toHaveAttribute('data-inspector-open', 'false')
    await page.locator('[data-inspector] [data-rail-item="people"]').click()
    await expect(page.locator('[data-inspector] [data-rail-item="people"]')).toHaveAttribute('aria-pressed', 'true')
    await expect(page.locator('[data-inspector] [data-rail-item="people"]')).toHaveAttribute('aria-label', /^People · /)
    await expect(page.locator('[data-inspector] [data-inspector-head="people"]')).toContainText('People')
    await expect(page.locator('[data-inspector] [data-chat]')).toBeVisible()
    // Activity: All, then Files with the touched rows and their count on the chip, then Tools.
    await api.ok('POST', `/api/sessions/${s.id}/events`, { type: 'file', op: 'edit', path: 'src/main.go', tool: 'Edit' })
    await api.ok('POST', `/api/sessions/${s.id}/events`, { type: 'tool_use', tool: 'Bash' })
    await page.locator('[data-inspector] [data-rail-item="activity"]').click()
    await expect(page.locator('[data-inspector] [data-activity="file"]').first()).toContainText('edited src/main.go', { timeout: 15_000 })
    await page.locator('[data-inspector] [data-activity-filter="files"]').click()
    await expect(page.locator('[data-inspector] [data-files-touched-count]')).toHaveText('1', { timeout: 15_000 })
    await expect(page.locator('[data-inspector] [data-files-touched] [data-touched]')).toHaveCount(1)
    await expect(page.locator('[data-inspector] [data-files-touched-head]')).toContainText('1 since the session started')
    await page.locator('[data-inspector] [data-activity-filter="tools"]').click()
    await expect(page.locator('[data-inspector] [data-activity]')).toHaveCount(1)
    await expect(page.locator('[data-inspector] [data-activity="tool_use"]')).toContainText('used Bash')
    await page.locator('[data-inspector] [data-activity-filter="all"]').click()
    await expect(page.locator('[data-inspector] [data-activity]').first()).toBeVisible()
    // The Yard's focused aside: the three Files items on a rail of its own, which cannot collapse.
    await page.goto(`/yard?focus=${s.id}`)
    const aside = page.locator('[data-files-aside]')
    await expect(aside.locator('[data-rail-item]')).toHaveCount(3)
    await expect(aside.locator('[data-rail-toggle]')).toHaveCount(0)
    await aside.locator('[data-rail-item="commits"]').click()
    await expect(aside.locator('[data-files-commits-head]')).toContainText('Commits')
    await aside.locator('[data-rail-item="commits"]').click()
    await expect(aside.locator('[data-files-commits-head]')).toContainText('Commits')
    await aside.locator('[data-rail-item="explorer"]').click()
    await expect(aside.locator(`[data-file-node="${cwd}/README.md"]`)).toBeVisible({ timeout: 30_000 })
  } finally {
    await api.stopSession(s.id)
  }
})
