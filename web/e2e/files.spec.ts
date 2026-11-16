import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, showView, test, type Session } from './fixtures'

// The Files tab as the Explorer (design 4a): the working directory listed as
// soon as the tab opens, folders expanding in place, the breadcrumb from the
// top, the box filtering the tree or opening a typed path:line in the
// editor area, the hint that paths in the terminal are clickable.
test('the Files tab opens on the working directory as a tree; the box filters it or opens a path at a line', async ({ page, api, state }) => {
  // A working directory of this test's own (the shared scratch repository gains files from other specs).
  const cwd = join(state.root, 'files-explorer')
  mkdirSync(join(cwd, 'internal', 'api'), { recursive: true })
  mkdirSync(join(cwd, 'docs'), { recursive: true })
  mkdirSync(join(cwd, 'empty'), { recursive: true })
  writeFileSync(join(cwd, 'README.md'), '# files\n')
  writeFileSync(join(cwd, 'internal', 'api', 'users.go'), 'package api\n\n// ListUsers answers GET /v1/users.\nfunc ListUsers() {}\n')
  writeFileSync(join(cwd, 'internal', 'api', 'router.go'), 'package api\n')
  writeFileSync(join(cwd, 'docs', 'users.md'), '# users\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'files', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    await showView(page, 'explorer')
    const pane = page.locator('[data-files-mode]')
    await expect(pane).toHaveAttribute('data-files-mode', 'tree')
    // The working directory, listed: folders first, then files; the breadcrumb ends with its folder.
    const tree = pane.locator('[data-files-tree]')
    await expect(tree.locator('[data-file-node]')).toHaveCount(4, { timeout: 30_000 })
    await expect(tree.locator('[data-file-node]')).toHaveText([/docs/, /empty/, /internal/, /README\.md/])
    await expect(pane.locator('[data-files-crumbs] [data-files-crumb]').last()).toHaveText(cwd.split('/').pop()!)
    await expect(pane.locator('[data-files-hint]')).toContainText('Paths the agent prints in the terminal are clickable')
    // A folder expands in place with its own listing; an empty one says so.
    await tree.locator(`[data-file-node="${cwd}/internal"]`).click()
    await expect(tree.locator(`[data-file-node="${cwd}/internal/api"]`)).toBeVisible()
    await tree.locator(`[data-file-node="${cwd}/internal/api"]`).click()
    await expect(tree.locator(`[data-file-node="${cwd}/internal/api/users.go"]`)).toBeVisible()
    await tree.locator(`[data-file-node="${cwd}/empty"]`).click()
    await expect(tree.locator(`[data-file-empty="${cwd}/empty"]`)).toHaveText('Empty directory')
    // The filter narrows the loaded tree to the names that match, their folders kept.
    const box = pane.locator('[data-files-box] input, input[data-files-box]').first()
    await box.fill('users')
    await expect(tree.locator('[data-file-node]')).toHaveText([/internal/, /api/, /users\.go/])
    await box.press('Escape')
    await expect(tree.locator('[data-file-node]')).toHaveCount(7)
    // A path in the box narrows the tree to that folder and what is below it (#138), the folders on the way kept.
    await box.fill('internal/api')
    await expect(tree.locator('[data-file-node]')).toHaveText([/internal/, /api/, /router\.go/, /users\.go/])
    // Clicking a folder clears the filter: the whole tree shows again, that folder closed by the click.
    await tree.locator(`[data-file-node="${cwd}/internal/api"]`).click()
    await expect(box).toHaveValue('')
    await expect(tree.locator('[data-file-node]')).toHaveText([/docs/, /empty/, /internal/, /api/, /README\.md/])
    // A typed path:line opens the file in the editor area at that line; the pane stays the tree.
    await box.fill('internal/api/users.go:4')
    await box.press('Enter')
    const area = page.locator('[data-editor-area]')
    await expect(area).toHaveAttribute('data-editor-area', 'open')
    await expect(area.locator('[data-editor-crumbs]')).toContainText('users.go')
    await expect(area.locator('[data-editor-pos]')).toHaveText('Ln 4, Col 1', { timeout: 30_000 })
    await expect(pane).toHaveAttribute('data-files-mode', 'tree')
    // A file in the tree opens as another tab.
    await tree.locator(`[data-file-node="${cwd}/docs"]`).click()
    await tree.locator(`[data-file-node="${cwd}/docs/users.md"]`).click()
    await expect(area.locator('[data-editor-tab]')).toHaveCount(2)
    await expect(area.locator('[data-editor-tab-active]')).toContainText('users.md')
    await expect(pane).toHaveAttribute('data-files-mode', 'tree')
  } finally {
    await api.stopSession(s.id)
  }
})

// A deep working directory's breadcrumb reads whole at its end (#169): the session's own folder keeps its full name, the folders
// above fold into one "…" crumb that opens a menu listing them, and no crumb is cut to a letter.
test('the Files tab\'s breadcrumb folds the folders above into one and keeps the last whole', async ({ page, api, state }) => {
  const cwd = join(state.root, 'crumbs-one', 'crumbs-two-longer', 'crumbs-three-longer-still', 'crumbs-four', 'crumbs-five-longer', 'the-session-folder-itself')
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'README.md'), '# deep\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'crumbs', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    await showView(page, 'explorer')
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator('[data-files-tree] [data-file-node]')).toHaveCount(1, { timeout: 30_000 })
    const crumbs = pane.locator('[data-files-crumbs] [data-files-crumb]')
    const last = crumbs.last()
    await expect(last).toHaveText('the-session-folder-itself')
    const whole = await last.evaluate((el) => el.scrollWidth <= el.clientWidth + 1)
    expect(whole, 'the last crumb reads whole').toBe(true)
    for (const text of await crumbs.allTextContents()) expect(text.length, `a crumb cut short: ${text}`).toBeGreaterThan(3)
    await expect(pane.locator('[data-files-crumbs]')).toHaveAttribute('title', new RegExp('crumbs-one/crumbs-two-longer/.*/the-session-folder-itself$'))
    // The folded folders, top to bottom, in the "…" crumb's menu.
    await pane.locator('[data-files-crumbs-more]').click()
    const items = page.getByRole('menuitem')
    await expect(items.first()).toBeVisible()
    const names = await items.allTextContents()
    expect(names.map((n) => n.trim())).toEqual(expect.arrayContaining(['crumbs-one', 'crumbs-two-longer', 'crumbs-three-longer-still', 'crumbs-four', 'crumbs-five-longer']))
    expect(names.indexOf(names.find((n) => n.includes('crumbs-one'))!)).toBeLessThan(names.indexOf(names.find((n) => n.includes('crumbs-five-longer'))!))
    await page.keyboard.press('Escape')
  } finally {
    await api.stopSession(s.id)
  }
})
