import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, showView, test, type Session } from './fixtures'
import { scratchRepo } from './server'

// Touched (design 4e): the files the agent read, edited, wrote or deleted,
// newest first, each with the tool, the agent and the time, from the file
// events its hooks report; a dot on a touched file in the Explorer; the
// same events in the Activity tab and in History's feed, quiet there.
test('Touched lists the files the agent touched, newest first, marks them in the Explorer, and the feeds carry them quietly', async ({ page, api, state }) => {
  const cwd = join(state.root, 'files-touched')
  mkdirSync(join(cwd, 'internal', 'api'), { recursive: true })
  writeFileSync(join(cwd, 'internal', 'api', 'users.go'), 'package api\n')
  writeFileSync(join(cwd, 'internal', 'api', 'router.go'), 'package api\n')
  writeFileSync(join(cwd, 'README.md'), '# touched\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'touched', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    await showView(page, 'explorer')
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator('[data-files-tree] [data-file-node]').first()).toBeVisible({ timeout: 30_000 })
    // The agent's hooks report, in order: a read, an edit, a repeat of the edit (one line), a write.
    const post = (body: Record<string, unknown>) => api.ok('POST', `/api/sessions/${s.id}/events`, body)
    await post({ type: 'file', op: 'read', path: 'internal/api/router.go', tool: 'Read' })
    await post({ type: 'file', op: 'edit', path: `${cwd}/internal/api/users.go`, tool: 'Edit' })
    await post({ type: 'file', op: 'edit', path: `${cwd}/internal/api/users.go`, tool: 'Edit' })
    await post({ type: 'file', op: 'write', path: 'README.md', tool: 'Write' })
    await page.locator('[data-inspector] [data-rail-item="activity"]').click()
    await page.locator('[data-inspector] [data-activity-filter="files"]').click()
    await expect(page.locator('[data-inspector] [data-files-touched-count]')).toHaveText('3', { timeout: 15_000 })
    const rows = page.locator('[data-inspector] [data-files-touched] [data-touched]')
    await expect(rows).toHaveCount(3)
    await expect(rows.nth(0)).toHaveAttribute('data-touched', `${cwd}/README.md`)
    await expect(rows.nth(0)).toHaveAttribute('data-touched-op', 'write')
    await expect(rows.nth(0)).toContainText('Write')
    await expect(rows.nth(1)).toHaveAttribute('data-touched', `${cwd}/internal/api/users.go`)
    await expect(rows.nth(2)).toHaveAttribute('data-touched-op', 'read')
    await expect(page.locator('[data-inspector] [data-files-touched-head]')).toContainText('3 since the session started')
    await expect(page.locator('[data-inspector] [data-files-touched-foot]')).toContainText('quiet there by default')
    // A touched row opens the file; the Explorer marks the touched files with a dot.
    await rows.nth(1).click()
    await expect(page.locator('[data-editor-area] [data-editor-crumbs]')).toContainText('users.go', { timeout: 30_000 })
    await showView(page, 'explorer')
    await expect(pane.locator(`[data-file-node="${cwd}/README.md"] [data-file-touched]`)).toBeVisible()
    await pane.locator(`[data-file-node="${cwd}/internal"]`).click()
    await pane.locator(`[data-file-node="${cwd}/internal/api"]`).click()
    await expect(pane.locator(`[data-file-node="${cwd}/internal/api/users.go"] [data-file-touched]`)).toBeVisible()
    // The Activity tab words it; History's feed lists what arrives (a page load starts the feed afresh) without a badge on the session.
    await page.locator('[data-inspector] [data-rail-item="activity"]').click()
    await page.locator('[data-inspector] [data-activity-filter="all"]').click()
    await expect(page.locator('[data-inspector] [data-activity="file"]').first()).toContainText('wrote README.md · Write')
    await page.goto('/history?view=feed')
    await expect(page.locator('[data-event-feed]')).toBeVisible()
    await post({ type: 'file', op: 'delete', path: 'internal/api/router.go', tool: 'Bash' })
    const row = page.locator('[data-event-feed] [data-event="file"]').filter({ hasText: 'router.go' }).first()
    await expect(row).toBeVisible({ timeout: 15_000 })
    await expect(row).toContainText('deleted internal/api/router.go · Bash')
    await expect(page.locator(`[data-sidebar-row="s:${s.id}"] [data-event-mark]`)).toHaveCount(0)
  } finally {
    await api.stopSession(s.id)
  }
})

// Round 13, G1: Touched comes from the session's own index, so a page
// opened after more file events than the activity replay holds (50) lists
// every file, one row per file with its count.
test('Touched lists every file of the session, past the activity replay, one row per file with its count', async ({ page, api, state }) => {
  const cwd = join(state.root, 'files-touched-many')
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'main.go'), 'package main\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'touched many', cwd })
  try {
    const post = (body: Record<string, unknown>) => api.ok('POST', `/api/sessions/${s.id}/events`, body)
    await post({ type: 'file', op: 'read', path: 'main.go', tool: 'Read' })
    await post({ type: 'file', op: 'edit', path: 'main.go', tool: 'Edit' })
    // Sixty more files, at the pace a hook reports (the session's event bucket takes 20 a second).
    for (let i = 0; i < 60; i++) {
      await post({ type: 'file', op: 'write', path: `gen/f${String(i).padStart(2, '0')}.go`, tool: 'Write' })
      await page.waitForTimeout(70)
    }
    await page.goto(`/sessions/${s.id}`)
    await showView(page, 'explorer')
    await page.locator('[data-inspector] [data-rail-item="activity"]').click()
    await page.locator('[data-inspector] [data-activity-filter="files"]').click()
    await expect(page.locator('[data-inspector] [data-files-touched-count]')).toHaveText('61', { timeout: 30_000 })
    const rows = page.locator('[data-inspector] [data-files-touched] [data-touched]')
    await expect(rows).toHaveCount(61)
    await expect(rows.first()).toHaveAttribute('data-touched', `${cwd}/gen/f59.go`)
    const main = page.locator(`[data-inspector] [data-files-touched] [data-touched="${cwd}/main.go"]`)
    await expect(main).toHaveAttribute('data-touched-op', 'edit')
    await expect(main).toContainText(/Edit · .*2 times/)
  } finally {
    await api.stopSession(s.id)
  }
})

// Round 13, G2a: a file the agent changed with no hook naming it (a shell
// redirect, a generator) still reaches Touched: after a tool call the
// session looks at git and records what moved, "seen by git".
test('a file written with no hook naming it shows in Touched as seen by git', async ({ page, api, state }) => {
  const cwd = join(state.root, 'files-git-seen')
  mkdirSync(cwd, { recursive: true })
  scratchRepo(cwd)
  writeFileSync(join(cwd, 'README.md'), '# changed before the session\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'git seen', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    await showView(page, 'explorer')
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator('[data-files-tree] [data-file-node]').first()).toBeVisible({ timeout: 30_000 })
    // What the agent's shell did: no file event, only the tool call.
    writeFileSync(join(cwd, 'generated.txt'), 'from a shell redirect\n')
    await api.ok('POST', `/api/sessions/${s.id}/events`, { type: 'tool_use', tool: 'Bash' })
    await page.locator('[data-inspector] [data-rail-item="activity"]').click()
    await page.locator('[data-inspector] [data-activity-filter="files"]').click()
    const row = page.locator(`[data-inspector] [data-files-touched] [data-touched="${cwd}/generated.txt"]`)
    await expect(row).toBeVisible({ timeout: 15_000 })
    await expect(row).toHaveAttribute('data-touched-op', 'write')
    await expect(row).toContainText('seen by git')
    // What was dirty before the session is not the agent's.
    await expect(page.locator(`[data-inspector] [data-files-touched] [data-touched="${cwd}/README.md"]`)).toHaveCount(0)
  } finally {
    await api.stopSession(s.id)
  }
})

// Round 13, G2b: Codex, Copilot, agy and Goose name their files in the payloads a
// live run of each sends (internal/notify/testdata); the stub replays them
// through `conductor notify`, the path a real hook takes, and Touched lists
// the shell read and the write.
for (const agent of ['codex', 'copilot', 'agy', 'goose'] as const) {
  test(`${agent}'s hook payloads name the files its tools read and wrote`, async ({ page, api, state }) => {
    const cwd = join(state.root, `files-hooks-${agent}`)
    mkdirSync(cwd, { recursive: true })
    writeFileSync(join(cwd, 'README.md'), '# hooks\n')
    const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: `hooks ${agent}`, cwd })
    try {
      await page.goto(`/sessions/${s.id}`)
      const screen = page.locator('.terminal-host .xterm-screen').first()
      await expect(screen).toBeVisible({ timeout: 30_000 })
      await screen.click()
      await page.keyboard.type(`stub tools ${agent}`)
      await page.keyboard.press('Enter')
      // The panel was left open on Explorer, and is kept so across the load (P11): Explorer shown, not clicked closed.
      await showView(page, 'explorer')
      await page.locator('[data-inspector] [data-rail-item="activity"]').click()
      await page.locator('[data-inspector] [data-activity-filter="files"]').click()
      const readme = page.locator(`[data-inspector] [data-files-touched] [data-touched="${cwd}/README.md"]`)
      await expect(readme).toBeVisible({ timeout: 15_000 })
      await expect(readme).toHaveAttribute('data-touched-op', 'read')
      const written = page.locator(`[data-inspector] [data-files-touched] [data-touched="${cwd}/stub-tools.txt"]`)
      await expect(written).toBeVisible()
      await expect(written).toHaveAttribute('data-touched-op', 'write')
    } finally {
      await api.stopSession(s.id)
    }
  })
}
