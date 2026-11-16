import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, showView, test, type Session } from './fixtures'

// A file larger than a read carries (about 960 KiB, so that its frame fits
// the viewer's 1 MiB queue) opens in the editor cut short: its first lines,
// the "truncated" badge, read-only; the session's connection stays open
// throughout, nothing reconnects.
test('a file of more than 1 MiB opens cut short, marked truncated and read-only, on the same connection', async ({ page, api, state }) => {
  const cwd = join(state.root, 'files-large')
  mkdirSync(cwd, { recursive: true })
  const lines: string[] = []
  for (let i = 1, size = 0; size < 1536 * 1024; i++) {
    const line = `line ${String(i).padStart(6, '0')} of a file larger than one read\n`
    lines.push(line)
    size += line.length
  }
  writeFileSync(join(cwd, 'large.txt'), lines.join(''))
  writeFileSync(join(cwd, 'small.txt'), 'a small file\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'large file', cwd })
  const closed: string[] = []
  page.on('websocket', (ws) => {
    if (ws.url().includes(s.id)) ws.on('close', () => closed.push(ws.url()))
  })
  try {
    await page.goto(`/sessions/${s.id}`)
    const transport = page.locator('[data-transport-state]').first()
    await expect(transport).toHaveAttribute('data-transport-state', 'open', { timeout: 30_000 })
    await showView(page, 'explorer')
    const tree = page.locator('[data-files-tree]')
    await expect(tree.locator(`[data-file-node="${cwd}/large.txt"]`)).toBeVisible({ timeout: 30_000 })
    await tree.locator(`[data-file-node="${cwd}/large.txt"]`).click()
    const area = page.locator('[data-editor-area]')
    await expect(area.locator('[data-editor-state]')).toHaveAttribute('data-editor-state', 'text', { timeout: 30_000 })
    await expect(area.locator('.monaco-editor .view-lines')).toContainText('line 000001 of a file larger than one read', { timeout: 30_000 })
    await expect(area.getByText('truncated', { exact: true })).toBeVisible()
    await expect(area.locator('[data-editor-readonly]')).toBeVisible()
    // The same connection goes on: another file opens through it, whole.
    await tree.locator(`[data-file-node="${cwd}/small.txt"]`).click()
    await expect(area.locator('[data-editor-tab-active]')).toContainText('small.txt')
    await expect(area.locator('.monaco-editor .view-lines')).toContainText('a small file', { timeout: 30_000 })
    await expect(area.getByText('truncated', { exact: true })).toHaveCount(0)
    await expect(transport).toHaveAttribute('data-transport-state', 'open')
    expect(closed).toEqual([])
  } finally {
    await api.stopSession(s.id)
  }
})
