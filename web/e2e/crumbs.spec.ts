import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Page } from '@playwright/test'
import { expect, test, type Session } from './fixtures'

// The breadcrumb strip (#315): a working directory whose name overflows the
// strip shows its end, scrolled there by the watch that reads the crumbs,
// which used to throw at mount (its source read `crumbs` above the line
// declaring it) and so never scrolled; the pane mounts with no such error
// reaching the console. The nav lives in a conditional header: a view switch
// makes a new one, and a pane opened on Changes has none at mount; both end
// scrolled too (Codex's round on #315).

/** The strip overflows its pane and is scrolled to its end, the last crumbs in view. */
async function atEnd(page: Page) {
  const nav = page.locator('[data-files-mode] [data-files-crumbs]')
  await expect(nav.locator('[data-files-crumb]').last()).toHaveText(/^a-working-directory/)
  await expect.poll(() => nav.evaluate((el) => el.scrollWidth - el.clientWidth), { timeout: 10_000 }).toBeGreaterThan(20)
  await expect.poll(() => nav.evaluate((el) => el.scrollWidth - el.clientWidth - el.scrollLeft), { timeout: 10_000 }).toBeLessThanOrEqual(1)
}

test('a long working directory shows the end of its crumbs: at mount, after a view switch, and in a pane opened on Changes first', async ({ page, api, state }) => {
  // The working directory's own folder keeps its whole name in the strip (#169): one long enough overflows the pane.
  const cwd = join(state.root, 'crumbs-deep', 'a-working-directory-whose-own-folder-name-is-long-enough-to-overflow-the-breadcrumb-strip-of-the-pane')
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'notes.txt'), 'deep\n')
  const errors: string[] = []
  page.on('console', (m) => {
    if (m.type() === 'error') errors.push(m.text())
  })
  page.on('pageerror', (e) => errors.push(e.message))
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e crumbs', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    const rail = page.locator('[data-inspector]')
    await rail.locator('[data-rail-item="explorer"]').click()
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator('[data-files-tree] [data-file-node]').first()).toBeVisible({ timeout: 30_000 })
    await atEnd(page)
    // Changes, then the Explorer again: the header is made anew for the same path, and scrolled again.
    await rail.locator('[data-rail-item="changes"]').click()
    await expect(pane.locator('[data-files-crumbs]')).toBeHidden()
    await rail.locator('[data-rail-item="explorer"]').click()
    await atEnd(page)
    // A pane opened on Changes has no strip at mount: the Explorer's, made later, is scrolled as it appears. The panel is collapsed
    // first, so that the page loads with it closed (the choice is kept, P11) and opens on Changes, never on Explorer.
    await rail.locator('[data-rail-toggle]').click()
    await expect(rail).toHaveAttribute('data-inspector-open', 'false')
    await page.goto(`/sessions/${s.id}`)
    await expect(rail).toHaveAttribute('data-inspector-open', 'false')
    await rail.locator('[data-rail-item="changes"]').click()
    await expect(pane).toBeVisible({ timeout: 30_000 })
    await expect(pane.locator('[data-files-crumbs]')).toBeHidden()
    await rail.locator('[data-rail-item="explorer"]').click()
    await atEnd(page)
    expect(errors.filter((e) => /before initialization/i.test(e))).toEqual([])
  } finally {
    await api.stopSession(s.id)
  }
})
