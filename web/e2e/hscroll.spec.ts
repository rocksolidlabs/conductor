import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import type { BrowserContext, Page } from '@playwright/test'
import { expect, showView, test, type Session } from './fixtures'

// The session page never scrolls sideways (#325): the rail's count pill
// stuck out past the rail by two pixels and the panel's body, which scrolls
// on its own, grew a horizontal scrollbar for them, at every width, with
// the panel open or collapsed. The pill sits inside the rail now and the
// inspector clips sideways; the body keeps its own scroll (hiding it would
// put a phone's quick-reply choices out of reach), so the row's and the
// body's own widths are measured here and a new overhang fails.
test('the session page is never wider than its box: with the panel open or collapsed, a file open, at a wide and a narrow window', async ({ browser, api, state }) => {
  const contexts: BrowserContext[] = []
  let page!: Page
  const cwd = join(state.root, 'hscroll')
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'notes.txt'), 'alpha\nbeta\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e hscroll', cwd })
  const widths = () =>
    page.evaluate(() => {
      const row = document.querySelector('[data-editor-column]')!.parentElement!
      const body = row.closest('[data-slot=body]')!
      const rail = document.querySelector('[data-inspector-rail]')!.getBoundingClientRect()
      const pills = Array.from(document.querySelectorAll('[data-rail-count]')).map((p) => p.getBoundingClientRect().right - rail.right)
      return { row: row.scrollWidth - row.clientWidth, body: body.scrollWidth - body.clientWidth, doc: document.documentElement.scrollWidth - document.documentElement.clientWidth, pills }
    })
  const fits = async (where: string) => {
    const w = await widths()
    expect(w.row, `${where}: the row's content past its box`).toBeLessThanOrEqual(0)
    expect(w.body, `${where}: the body's content past its box`).toBeLessThanOrEqual(0)
    expect(w.doc, `${where}: the document past the window`).toBeLessThanOrEqual(0)
    for (const over of w.pills) expect(over, `${where}: a count pill past the rail's edge`).toBeLessThanOrEqual(0)
  }
  try {
    for (const [width, height] of [[2000, 820], [1280, 720]] as const) {
      // Each width in a fresh page of its own context: the second does not inherit the panel the first one opened (P11).
      const context = await browser.newContext({ viewport: { width, height } })
      contexts.push(context)
      page = await context.newPage()
      await page.addInitScript((token) => localStorage.setItem('conductor.workbenchToken', token), state.token)
      await page.goto(`${state.baseURL}/sessions/${s.id}`)
      await expect(page.locator('.terminal-host .xterm-helper-textarea').first()).toBeFocused({ timeout: 30_000 })
      // A count on the rail: this viewer is one person here, so the People pill shows. The panel is closed as the page opens.
      await expect(page.locator('[data-inspector] [data-rail-count="people"]')).toBeVisible()
      await expect(page.locator('[data-inspector]')).toHaveAttribute('data-inspector-open', 'false')
      await expect(page.locator('[data-inspector-panel]')).toHaveCount(0)
      await fits(`${width} as loaded`)
      await showView(page, 'people')
      await expect(page.locator('[data-inspector-panel]')).toBeVisible()
      await fits(`${width} People`)
      await page.locator('[data-inspector] [data-rail-toggle]').click()
      await expect(page.locator('[data-inspector]')).toHaveAttribute('data-inspector-open', 'false')
      await fits(`${width} collapsed`)
      await showView(page, 'explorer')
      await page.locator(`[data-files-mode] [data-file-node="${join(cwd, 'notes.txt')}"]`).click()
      await expect(page.locator('[data-editor-area] .monaco-editor .view-lines')).toContainText('alpha', { timeout: 30_000 })
      await fits(`${width} a file open`)
    }
  } finally {
    for (const c of contexts) await c.close()
    await api.stopSession(s.id)
  }
})
