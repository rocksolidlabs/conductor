import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, showView, test, type Session } from './fixtures'

// The Files pane on a very short window (#203). The inspector gets what is
// left under the page's header, and the pane's header rows took it all: the
// tree's content was squashed to nothing, its rows laid out under the hint,
// which took their clicks. The content keeps a few rows' height now, the
// pane scrolls as a whole, and the advisory hint gives way at this height.
test.use({ viewport: { width: 1440, height: 230 } })

test('on a very short window a folder row in the Explorer takes its click and expands', async ({ page, api, state }) => {
  const cwd = join(state.root, 'files-short')
  mkdirSync(join(cwd, 'docs'), { recursive: true })
  mkdirSync(join(cwd, 'internal'), { recursive: true })
  writeFileSync(join(cwd, 'docs', 'users.md'), '# users\n')
  writeFileSync(join(cwd, 'README.md'), '# short\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'files-short', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    await showView(page, 'explorer')
    const pane = page.locator('[data-files-mode]')
    const tree = pane.locator('[data-files-tree]')
    await expect(tree.locator('[data-file-node]')).toHaveCount(3, { timeout: 30_000 })
    // The content keeps its rows' height; the hint is gone at this height.
    const content = pane.locator('.file-view')
    expect((await content.boundingBox())!.height).toBeGreaterThanOrEqual(90)
    await expect(pane.locator('[data-files-hint]')).toBeHidden()
    // The row is what a click at its own middle reaches, nothing of the pane over it.
    const row = tree.locator(`[data-file-node="${cwd}/docs"]`)
    await row.scrollIntoViewIfNeeded()
    const box = (await row.boundingBox())!
    const hit = await page.evaluate(
      ([x, y]) => document.elementFromPoint(x, y)?.closest('[data-file-node]')?.getAttribute('data-file-node') ?? document.elementFromPoint(x, y)?.outerHTML.slice(0, 80) ?? 'nothing',
      [box.x + box.width / 2, box.y + box.height / 2],
    )
    expect(hit).toBe(`${cwd}/docs`)
    await row.click()
    await expect(tree.locator(`[data-file-node="${cwd}/docs/users.md"]`)).toBeVisible()
  } finally {
    await api.stopSession(s.id)
  }
})
