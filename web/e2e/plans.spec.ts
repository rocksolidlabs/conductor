import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Page } from '@playwright/test'
import { expect, test, type Session } from './fixtures'

// A plan Claude Code writes outside the session's folder (#289): the stub
// writes it where Claude Code does, under the home's .claude/plans, reports
// the write through its hook and asks for its approval with the plan's
// path, as Claude Code 2.1.304 does. The plan opens in the editor from the
// approval's Open plan, from the Files tab's Plans entry and from the path
// the dialog prints in the terminal; it previews as Markdown, saves back
// to its file, and lists under Touched by its whole path. A plan of
// another session's in the same directory is not listed.
test.describe.configure({ mode: 'serial' })

async function typeLine(page: Page, sessionId: string, line: string) {
  await page.goto(`/sessions/${encodeURIComponent(sessionId)}`)
  const screen = page.locator('.terminal-host .xterm-screen').first()
  await expect(screen).toBeVisible()
  await screen.click()
  await page.keyboard.type(line)
  await page.keyboard.press('Enter')
}

/** Clicks the terminal's cell at column `col`, row `row` (0-based), hovering first so that the link provider has run on the row. */
async function clickCell(page: Page, api: { session: (id: string) => Promise<Session> }, sessionId: string, col: number, row: number) {
  const screen = page.locator('.terminal-host .xterm-screen').first()
  const box = await screen.boundingBox()
  const s = (await api.session(sessionId)) as Session & { rows?: number }
  if (!box || !s.cols || !s.rows) throw new Error(`no terminal geometry: ${JSON.stringify({ box, cols: s.cols, rows: s.rows })}`)
  const x = box.x + ((col + 0.5) * box.width) / s.cols
  const y = box.y + ((row + 0.5) * box.height) / s.rows
  await page.mouse.move(x, y)
  await page.waitForTimeout(600)
  await page.mouse.move(x + 1, y)
  await page.waitForTimeout(600)
  await page.mouse.click(x + 1, y)
}

test('a plan outside the folder opens from the approval, the Files tab and the printed path, previews and saves', async ({ page, api, state }) => {
  const cwd = join(state.root, 'plans-folder')
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'README.md'), '# plans\n')
  const plans = join(state.home, '.claude', 'plans')
  mkdirSync(plans, { recursive: true })
  // Another session's plan, in the same directory: never this session's.
  writeFileSync(join(plans, 'another-session.md'), '# Another\n')
  const plan = join(plans, 'plan.md')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e plan outside the folder', cwd })
  try {
    await typeLine(page, s.id, 'stub plan')
    await expect.poll(async () => (await api.session(s.id)).attention?.source, { timeout: 30_000, message: 'the dialog is asked' }).toBe('question')
    expect((await api.session(s.id)).attention?.plan).toBe(plan)

    // The approval offers the plan: it opens in the editor, as Markdown, with the preview offered (#166).
    const bar = page.locator('[data-quick-reply]')
    const open = bar.locator('[data-quick-reply-plan]')
    await expect(open).toHaveText('Open plan')
    await expect(open).toHaveAttribute('title', plan)
    await open.click()
    const area = page.locator('[data-editor-area]')
    await expect(area).toHaveAttribute('data-editor-area', 'open')
    await expect(area.locator('[data-editor-crumbs]')).toContainText('plan.md')
    const lines = area.locator('.monaco-editor .view-lines')
    await expect(lines).toContainText('Create hello.txt containing hi.', { timeout: 30_000 })
    await expect(area.locator('[data-editor-preview]')).toHaveAttribute('data-editor-preview', 'off')
    await area.locator('[data-editor-preview]').click()
    await expect(area.locator('[data-md-preview] h1')).toHaveText('Plan')
    await area.locator('[data-editor-preview]').click()
    await expect(lines).toBeVisible()

    // The Files tab lists it under Plans, this session's plan alone; clicking it is the same tab.
    await page.locator('[data-inspector] [data-rail-item="explorer"]').click()
    const pane = page.locator('[data-files-mode]')
    await expect(pane.locator('[data-files-plans-head]')).toHaveText('Plans', { timeout: 15_000 })
    await expect(pane.locator('[data-files-plans]')).toHaveCount(1)
    await expect(pane.locator(`[data-plan="${plan}"]`)).toContainText('plan.md')
    await pane.locator(`[data-plan="${plan}"]`).click()
    await expect(area.locator('[data-editor-tab]')).toHaveCount(1)

    // A save writes the plan's file; Touched has the agent's write of it, by its whole path.
    await lines.click()
    await page.keyboard.press('Control+End')
    await page.keyboard.type('Then say so.\n')
    await expect(area.locator('[data-editor-dirty]')).toHaveCount(1)
    await page.keyboard.press('Control+s')
    await expect(area.locator('[data-editor-saved]')).toBeVisible({ timeout: 15_000 })
    expect(readFileSync(plan, 'utf8')).toContain('Then say so.')
    await page.locator('[data-inspector] [data-rail-item="activity"]').click()
    await page.locator('[data-inspector] [data-activity-filter="files"]').click()
    await expect(page.locator(`[data-inspector] [data-files-touched] [data-touched="${plan}"]`).first()).toBeVisible({ timeout: 15_000 })

    // The path the dialog printed ("ctrl+g to edit in Vim · ~/.claude/plans/plan.md", its last row) is a link: with the tab closed,
    // clicking it opens the plan again.
    await area.locator('[data-editor-tab-active] [data-editor-close], [data-editor-close]').first().click()
    await expect(area.locator('[data-editor-tab]')).toHaveCount(0)
    await clickCell(page, api, s.id, 30, 17)
    await expect(area.locator('[data-editor-tab]')).toHaveCount(1, { timeout: 15_000 })
    await expect(area.locator('[data-editor-crumbs]')).toContainText('plan.md')
    await expect(lines).toContainText('Then say so.', { timeout: 30_000 })
  } finally {
    await api.stopSession(s.id)
  }
})

test('Open plan works on the Yard and on a shared session page too', async ({ page, api, state, browser }) => {
  const cwd = join(state.root, 'plans-elsewhere')
  mkdirSync(cwd, { recursive: true })
  const plan = join(state.home, '.claude', 'plans', 'plan.md')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e plan elsewhere', cwd })
  try {
    await typeLine(page, s.id, 'stub plan')
    await expect.poll(async () => (await api.session(s.id)).attention?.plan, { timeout: 30_000, message: 'the approval names the plan' }).toBe(plan)
    // The Yard, focused on the session: its reply bar's Open plan opens the plan in the Yard's editor.
    await page.goto(`/yard?focus=${s.id}`)
    await page.locator('[data-quick-reply] [data-quick-reply-plan]').click()
    const area = page.locator('[data-editor-area]')
    await expect(area).toHaveAttribute('data-editor-area', 'open')
    await expect(area.locator('[data-editor-crumbs]')).toContainText('plan.md')
    await expect(area.locator('.monaco-editor .view-lines')).toContainText('Create hello.txt containing hi.', { timeout: 30_000 })
    // A guest with a control link, on the bare join page, in a browser with no workbench token.
    const { token } = await api.ok<{ token: string }>('POST', `/api/sessions/${s.id}/links`, { role: 'control' })
    const guestCtx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
    try {
      await guestCtx.addInitScript(() => localStorage.setItem('conductor.displayName', 'Priya'))
      const guest = await guestCtx.newPage()
      await guest.goto(`/join/${token}`)
      await guest.getByRole('button', { name: 'Join session' }).click()
      await expect(guest.locator('[data-transport-state]').first()).toHaveAttribute('data-transport-state', 'open', { timeout: 30_000 })
      await guest.locator('[data-quick-reply] [data-quick-reply-plan]').click()
      const guestArea = guest.locator('[data-editor-area]')
      await expect(guestArea).toHaveAttribute('data-editor-area', 'open')
      await expect(guestArea.locator('[data-editor-crumbs]')).toContainText('plan.md')
      await expect(guestArea.locator('.monaco-editor .view-lines')).toContainText('Create hello.txt containing hi.', { timeout: 30_000 })
    } finally {
      await guestCtx.close()
    }
  } finally {
    await api.stopSession(s.id)
  }
})

/** Preview plan's one click (#432): the plan's tab in front, rendered, the preview with the focus; the terminal as it was, never folded. */
async function previewsThePlan(page: Page) {
  await page.locator('[data-quick-reply] [data-quick-reply-plan-preview]').click()
  const area = page.locator('[data-editor-area]')
  await expect(area).toHaveAttribute('data-editor-area', 'open')
  await expect(area.locator('[data-editor-crumbs]')).toContainText('plan.md')
  await expect(area.locator('[data-md-preview] h1')).toHaveText('Plan', { timeout: 30_000 })
  await expect(area.locator('[data-editor-preview]')).toHaveAttribute('data-editor-preview', 'on')
  await expect(area.locator('[data-md-preview]')).toBeFocused()
  await expect(page.locator('[data-terminal-slot]')).toHaveAttribute('data-terminal-slot', 'open')
  await expect(page.locator('[data-terminal-bar]')).toHaveCount(0)
  await expect(page.locator('.terminal-host .xterm-screen').first()).toBeVisible()
}

test('Preview plan opens the plan rendered on the session page, the Yard and a shared session page, and leaves the terminal as it was', async ({ page, api, state, browser }) => {
  const cwd = join(state.root, 'plans-preview')
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'notes.txt'), 'alpha\n')
  const plan = join(state.home, '.claude', 'plans', 'plan.md')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e plan preview', cwd })
  try {
    await typeLine(page, s.id, 'stub plan')
    await expect.poll(async () => (await api.session(s.id)).attention?.plan, { timeout: 30_000, message: 'the approval names the plan' }).toBe(plan)
    const bar = page.locator('[data-quick-reply]')
    const preview = bar.locator('[data-quick-reply-plan-preview]')
    // Beside Open plan, after it, with the eye of the editor's Preview toggle.
    await expect(preview).toHaveText('Preview plan')
    await expect(preview).toHaveAttribute('aria-label', 'Preview plan')
    await expect(preview.locator('[data-slot="leadingIcon"]')).toHaveClass(/lucide:eye/)
    expect(await bar.locator('[data-quick-reply-plan], [data-quick-reply-plan-preview]').evaluateAll((els) => els.map((e) => e.hasAttribute('data-quick-reply-plan-preview')))).toEqual([false, true])

    // At a phone's 390 px both plan buttons are icons alone, inside the window, and the bar is no wider than it (#327).
    await page.setViewportSize({ width: 390, height: 844 })
    for (const b of [bar.locator('[data-quick-reply-plan]'), preview]) {
      await expect(b.locator('[data-slot="label"]')).toBeHidden()
      await expect(b.locator('[data-slot="leadingIcon"]')).toBeVisible()
      const box = (await b.boundingBox())!
      expect(box.x >= 0 && box.x + box.width <= 390, 'the plan button within the window').toBeTruthy()
    }
    const barBox = (await bar.boundingBox())!
    expect(barBox.x >= 0 && barBox.x + barBox.width <= 390, 'the reply bar within the window').toBeTruthy()
    await page.setViewportSize({ width: 1440, height: 900 })
    await expect(bar.locator('[data-quick-reply-plan] [data-slot="label"]')).toBeVisible()

    // The session page: a file already open and the editor folded; Preview plan opens the plan rendered and unfolds the editor.
    await page.locator('[data-inspector] [data-rail-item="explorer"]').click()
    await page.locator(`[data-file-node="${cwd}/notes.txt"]`).click()
    const area = page.locator('[data-editor-area]')
    await expect(area).toHaveAttribute('data-editor-area', 'open')
    await area.locator('[data-editor-fold]').click()
    await expect(area).toHaveAttribute('data-editor-area', 'folded')
    await previewsThePlan(page)
    await expect(area.locator('[data-editor-tab]')).toHaveCount(2)
    // Source (or Alt+P) shows the plan's editable source; a second Preview plan shows it rendered again, the same tab.
    await area.locator('[data-editor-preview]').click()
    await expect(area.locator('[data-editor-preview]')).toHaveAttribute('data-editor-preview', 'off')
    await expect(area.locator('.monaco-editor .view-lines')).toContainText('Create hello.txt containing hi.', { timeout: 30_000 })
    await previewsThePlan(page)
    await expect(area.locator('[data-editor-tab]')).toHaveCount(2)
    // Open plan is as it was: the tab to the front as it shows, rendered here.
    await bar.locator('[data-quick-reply-plan]').click()
    await expect(area.locator('[data-editor-preview]')).toHaveAttribute('data-editor-preview', 'on')
    await expect(page.locator('[data-terminal-slot]')).toHaveAttribute('data-terminal-slot', 'open')

    // The Yard, focused on the session.
    await page.goto(`/yard?focus=${s.id}`)
    await previewsThePlan(page)
    // A guest with a control link, on the bare join page.
    const { token } = await api.ok<{ token: string }>('POST', `/api/sessions/${s.id}/links`, { role: 'control' })
    const guestCtx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
    try {
      await guestCtx.addInitScript(() => localStorage.setItem('conductor.displayName', 'Priya'))
      const guest = await guestCtx.newPage()
      await guest.goto(`/join/${token}`)
      await guest.getByRole('button', { name: 'Join session' }).click()
      await expect(guest.locator('[data-transport-state]').first()).toHaveAttribute('data-transport-state', 'open', { timeout: 30_000 })
      await previewsThePlan(guest)
    } finally {
      await guestCtx.close()
    }
  } finally {
    await api.stopSession(s.id)
  }
})
