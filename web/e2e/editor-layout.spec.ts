import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Locator, Page } from '@playwright/test'
import { expect, test, type Api, type Session } from './fixtures'

// The editor and the terminal side by side as well as stacked (#309): the
// switch in the editor's tab strip, the bar between them draggable either
// way and moved by the arrow keys, the terminal keeping a minimum width
// side by side, the choice and each layout's split kept per browser and
// surviving a reload; a narrow window stacks whatever is kept.
test('the editor goes beside the terminal and back, the split drags and steps, and both outlive a reload', async ({ page, api, state }) => {
  const cwd = join(state.root, 'editor-layout')
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'notes.txt'), 'alpha\nbeta\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e editor layout', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    await page.locator('[data-inspector] [data-rail-item="explorer"]').click()
    await page.locator(`[data-file-node="${cwd}/notes.txt"]`).click()
    const column = page.locator('[data-editor-column]')
    const area = page.locator('[data-editor-area]')
    const slot = page.locator('[data-terminal-slot]')
    const handle = page.locator('[data-editor-split]')
    await expect(area).toHaveAttribute('data-editor-area', 'open')
    await expect(column).toHaveAttribute('data-editor-layout', 'stacked')
    await expect(handle).toHaveAttribute('aria-orientation', 'horizontal')
    // Stacked: the editor above the terminal.
    let a = (await area.boundingBox())!
    let t = (await slot.boundingBox())!
    expect(a.y + a.height).toBeLessThanOrEqual(t.y + 1)
    // The switch puts the editor beside the terminal, on the left; the bar turns vertical.
    await area.locator('[data-editor-layout-switch]').click()
    await expect(column).toHaveAttribute('data-editor-layout', 'side')
    await expect(handle).toHaveAttribute('aria-orientation', 'vertical')
    a = (await area.boundingBox())!
    t = (await slot.boundingBox())!
    expect(a.x + a.width).toBeLessThanOrEqual(t.x + 1)
    expect(Math.abs(a.y - t.y)).toBeLessThan(2)
    // A drag to the right widens the editor; dragged past the terminal's minimum, the terminal keeps its width.
    const h = (await handle.boundingBox())!
    const before = a.width
    await page.mouse.move(h.x + h.width / 2, h.y + h.height / 2)
    await page.mouse.down()
    await page.mouse.move(h.x + 120, h.y + h.height / 2, { steps: 6 })
    await page.mouse.up()
    a = (await area.boundingBox())!
    expect(a.width).toBeGreaterThan(before + 40)
    const c = (await column.boundingBox())!
    await page.mouse.move(a.x + a.width + 4, h.y + h.height / 2)
    await page.mouse.down()
    await page.mouse.move(c.x + c.width - 10, h.y + h.height / 2, { steps: 8 })
    await page.mouse.up()
    t = (await slot.boundingBox())!
    expect(t.width).toBeGreaterThanOrEqual(319)
    // The arrow keys move the bar: Left gives the editor less, End as far as it goes.
    await handle.focus()
    const wide = (await area.boundingBox())!.width
    await page.keyboard.press('ArrowLeft')
    await page.keyboard.press('ArrowLeft')
    expect((await area.boundingBox())!.width).toBeLessThan(wide)
    await page.keyboard.press('Home')
    const narrow = (await area.boundingBox())!.width
    await page.keyboard.press('End')
    expect((await area.boundingBox())!.width).toBeGreaterThan(narrow)
    // Kept per browser: the layout and its split come back after a reload; the stacked split is its own.
    const kept = (await area.boundingBox())!.width
    await page.reload()
    await page.locator('[data-inspector] [data-rail-item="explorer"]').click()
    await page.locator(`[data-file-node="${cwd}/notes.txt"]`).click()
    await expect(column).toHaveAttribute('data-editor-layout', 'side')
    expect(Math.abs((await area.boundingBox())!.width - kept)).toBeLessThan(4)
    // Back to stacked with the same switch; the stacked split is the one kept for it.
    await area.locator('[data-editor-layout-switch]').click()
    await expect(column).toHaveAttribute('data-editor-layout', 'stacked')
    await expect(handle).toHaveAttribute('aria-orientation', 'horizontal')
    await area.locator('[data-editor-layout-switch]').click()
    await expect(column).toHaveAttribute('data-editor-layout', 'side')
    // A narrow window stacks the two whatever is kept, and the switch is not offered.
    await page.setViewportSize({ width: 900, height: 900 })
    await expect(column).toHaveAttribute('data-editor-layout', 'stacked')
    await expect(area.locator('[data-editor-layout-switch]')).toHaveCount(0)
    await page.setViewportSize({ width: 1440, height: 900 })
    await expect(column).toHaveAttribute('data-editor-layout', 'side')
    // A wide window whose column is too narrow for both panes (beside wide panels, as a join page's Files and Chat leave it)
    // stacks too, and offers no switch: the column's own width decides, not the viewport's (Codex's round on #309).
    await column.evaluate((el) => ((el as HTMLElement).style.maxWidth = '400px'))
    await expect(column).toHaveAttribute('data-editor-layout', 'stacked')
    await expect(area.locator('[data-editor-layout-switch]')).toHaveCount(0)
    await column.evaluate((el) => ((el as HTMLElement).style.maxWidth = ''))
    await expect(column).toHaveAttribute('data-editor-layout', 'side')
  } finally {
    await api.stopSession(s.id)
  }
})

const PLAN = 'Claude has written up a plan and is ready to execute. Would you like to proceed?'
const KEPT = ['conductor.editor.layout', 'conductor.editor.split', 'conductor.editor.split.side']

/** What the browser keeps of the column: the layout and each split. */
function keptLayout(page: Page): Promise<Array<string | null>> {
  return page.evaluate((keys) => keys.map((k) => localStorage.getItem(k)), KEPT)
}

/**
 * The session's size once this window has sized it: attached (the session counts it as a viewer), the session at the terminal's drawn
 * grid (data-cols and data-rows), and the same on two reads a second apart, past the refit the bundled font may bring. The session is
 * born 80 × 24, which a read made before the window attaches sees: a read alone, or one compared with a read a moment before, proves
 * nothing (AGENTS.md, a starting value is not a result).
 */
async function settledSize(api: Api, id: string, host: Locator): Promise<string> {
  let size = ''
  let seen = ''
  await expect
    .poll(
      async () => {
        const s = (await api.session(id)) as Session & { rows: number; viewers: number }
        const grid = await host.evaluate((h) => `${h.dataset.cols} × ${h.dataset.rows}`)
        const now = `${s.viewers} ${s.cols} × ${s.rows} ${grid}`
        const held = s.viewers > 0 && grid === `${s.cols} × ${s.rows}` && now === seen
        seen = now
        size = `${s.cols} × ${s.rows}`
        return held
      },
      { timeout: 15_000, intervals: [1000], message: 'the window sizes the session' },
    )
    .toBe(true)
  return size
}

// The terminal collapses under an open file (#432), only when the person presses the strip's button beside Terminal only: the editor
// takes the column and the terminal folds to the bar at its foot, which says what the agent is doing; the bar or the button again
// brings the terminal back with the layout and the split it had, nothing kept per browser by the collapse. Collapsed, the terminal
// stays connected and asks no resize, and a waiting question's choices stay under the bar and answer. T's fold clears the collapse,
// as the last tab's close does.
test('the strip collapses the terminal to a bar and brings it back, stacked and side by side, its split and its size kept, a question answered meanwhile', async ({ page, api, state }) => {
  const cwd = join(state.root, 'editor-collapse')
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'notes.txt'), 'alpha\nbeta\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e terminal collapse', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    const screen = page.locator('.terminal-host .xterm-screen').first()
    await expect(screen).toBeVisible()
    await screen.click()
    await page.keyboard.type('stub plan')
    await page.keyboard.press('Enter')
    await expect.poll(async () => (await api.session(s.id)).attention?.source, { timeout: 30_000, message: 'the dialog is asked' }).toBe('question')

    await page.locator('[data-inspector] [data-rail-item="explorer"]').click()
    await page.locator(`[data-file-node="${cwd}/notes.txt"]`).click()
    const column = page.locator('[data-editor-column]')
    const area = page.locator('[data-editor-area]')
    const slot = page.locator('[data-terminal-slot]')
    const termBar = page.locator('[data-terminal-bar]')
    const reply = page.locator('[data-quick-reply]')
    const host = page.locator('.terminal-host').first()
    const collapse = area.locator('[data-editor-collapse-terminal]')
    await expect(area).toHaveAttribute('data-editor-area', 'open')
    await expect(column).toHaveAttribute('data-editor-layout', 'stacked')
    // Opening a file collapses nothing.
    await expect(slot).toHaveAttribute('data-terminal-slot', 'open')
    await expect(termBar).toHaveCount(0)
    await expect(collapse).toHaveAttribute('aria-label', 'Collapse the terminal')
    await expect(collapse).toHaveAttribute('data-editor-collapse-terminal', 'off')
    // Beside Terminal only.
    expect(await area.locator('[data-editor-fold], [data-editor-collapse-terminal]').count()).toBe(2)
    const size = await settledSize(api, s.id, host)
    const grid = await host.evaluate((h) => `${h.dataset.cols} × ${h.dataset.rows}`)
    const kept = await keptLayout(page)
    const stacked = (await area.boundingBox())!.height
    // The terminal stays mounted and connected through every collapse: the same element (marked here), and no socket opened again.
    const transport = page.locator('[data-transport-state]').first()
    await expect(transport).toHaveAttribute('data-transport-state', 'open')
    await host.evaluate((h) => ((h as HTMLElement & { e2eMark?: string }).e2eMark = 'kept'))
    const sameHost = () => host.evaluate((h) => (h as HTMLElement & { e2eMark?: string }).e2eMark ?? 'remounted')
    let sockets = 0
    page.on('websocket', () => sockets++)

    // Stacked: pressed, the editor takes the column and the terminal is the bar at its foot, saying the attention's words; the reply
    // bar stays under it. The terminal is still there, its grid unchanged.
    await collapse.click()
    await expect(slot).toHaveAttribute('data-terminal-slot', 'folded')
    await expect(collapse).toHaveAttribute('data-editor-collapse-terminal', 'on')
    await expect(termBar).toContainText(PLAN)
    await expect(termBar).toContainText('click to bring it back')
    await expect(page.locator('[data-editor-split]')).toHaveCount(0)
    expect((await area.boundingBox())!.height).toBeGreaterThan(stacked + 40)
    const tb = (await termBar.boundingBox())!
    await expect(reply).toBeVisible()
    expect((await reply.boundingBox())!.y).toBeGreaterThanOrEqual(tb.y + tb.height - 1)
    expect(await host.evaluate((h) => `${h.dataset.cols} × ${h.dataset.rows}`)).toBe(grid)
    // The bar brings it back with its split; nothing was kept per browser by the collapse.
    await termBar.click()
    await expect(slot).toHaveAttribute('data-terminal-slot', 'open')
    await expect(termBar).toHaveCount(0)
    await expect(collapse).toHaveAttribute('data-editor-collapse-terminal', 'off')
    expect(Math.abs((await area.boundingBox())!.height - stacked)).toBeLessThan(3)
    expect(await keptLayout(page)).toEqual(kept)
    // Fitted to its pane again, the session at the size it had.
    expect(await settledSize(api, s.id, host)).toBe(size)

    // Collapsed, the waiting question's choices show under the bar and answer; the session kept its size. (Before any change of
    // layout: the stub draws its dialog once, and a terminal made narrower reflows the screen the server reads the dialog off.)
    await collapse.click()
    await expect(slot).toHaveAttribute('data-terminal-slot', 'folded')
    const choices = reply.locator('button:not([data-quick-reply-plan]):not([data-quick-reply-plan-preview])')
    await expect(choices).toHaveCount(3)
    await expect(choices.nth(1)).toBeVisible()
    await choices.nth(1).click()
    const transcript = async () => {
      const file = join(state.home, '.stub-sessions', `${(await api.session(s.id)).agentSession?.id ?? ''}.txt`)
      return existsSync(file) ? readFileSync(file, 'utf8') : ''
    }
    await expect.poll(transcript, { timeout: 15_000, message: 'the stub read the choice' }).toContain('plan: 2')
    await expect.poll(async () => (await api.session(s.id)).attention?.source, { timeout: 15_000 }).not.toBe('question')
    const now = (await api.session(s.id)) as Session & { rows: number }
    expect(`${now.cols} × ${now.rows}`).toBe(size)
    await expect(slot).toHaveAttribute('data-terminal-slot', 'folded')
    // Brought back, it is the terminal it was, never remounted or connected again, so its scrollback is its own (read by element, not
    // by its text: the renderer CI's browser has draws no DOM rows).
    await termBar.click()
    await expect(slot).toHaveAttribute('data-terminal-slot', 'open')
    expect(await sameHost()).toBe('kept')
    await expect(transport).toHaveAttribute('data-transport-state', 'open')
    expect(sockets).toBe(0)

    // Side by side: collapsed, the bar is at the column's foot, full width; the button again brings the layout and its split back.
    await area.locator('[data-editor-layout-switch]').click()
    await expect(column).toHaveAttribute('data-editor-layout', 'side')
    const side = (await area.boundingBox())!.width
    const keptSide = await keptLayout(page)
    const sideSize = await settledSize(api, s.id, host)
    await collapse.click()
    await expect(slot).toHaveAttribute('data-terminal-slot', 'folded')
    await expect(column).toHaveAttribute('data-editor-layout', 'stacked')
    const col = (await column.boundingBox())!
    const bar = (await termBar.boundingBox())!
    expect(bar.y + bar.height).toBeGreaterThan(col.y + col.height / 2)
    expect(bar.width).toBeGreaterThan(col.width * 0.9)
    await collapse.click()
    await expect(slot).toHaveAttribute('data-terminal-slot', 'open')
    await expect(column).toHaveAttribute('data-editor-layout', 'side')
    expect(Math.abs((await area.boundingBox())!.width - side)).toBeLessThan(3)
    expect(await keptLayout(page)).toEqual(keptSide)
    expect(await settledSize(api, s.id, host)).toBe(sideSize)
    await area.locator('[data-editor-layout-switch]').click()
    await expect(column).toHaveAttribute('data-editor-layout', 'stacked')
    const keptStacked = await keptLayout(page)
    await collapse.click()
    await expect(slot).toHaveAttribute('data-terminal-slot', 'folded')

    // Alt+T folds the editor as ever, which ends the collapse: the terminal takes the column, and stays when the editor comes back.
    await page.keyboard.press('Alt+t')
    await expect(area).toHaveAttribute('data-editor-area', 'folded')
    await expect(slot).toHaveAttribute('data-terminal-slot', 'open')
    await page.keyboard.press('Alt+t')
    await expect(area).toHaveAttribute('data-editor-area', 'open')
    await expect(slot).toHaveAttribute('data-terminal-slot', 'open')
    await expect(collapse).toHaveAttribute('data-editor-collapse-terminal', 'off')
    // Closing the last tab ends it too: the file opened again finds the terminal open.
    await collapse.click()
    await expect(slot).toHaveAttribute('data-terminal-slot', 'folded')
    await area.locator('[data-editor-close-all]').click()
    await expect(area).toHaveCount(0)
    await expect(slot).toHaveAttribute('data-terminal-slot', 'open')
    await page.locator(`[data-file-node="${cwd}/notes.txt"]`).click()
    await expect(area).toHaveAttribute('data-editor-area', 'open')
    await expect(slot).toHaveAttribute('data-terminal-slot', 'open')
    expect(await keptLayout(page)).toEqual(keptStacked)
    expect(await sameHost()).toBe('kept')
    expect(sockets).toBe(0)
  } finally {
    await api.stopSession(s.id)
  }
})

// On a phone the terminal already folds under an open file by itself: the strip offers no collapse there.
test('a phone offers no collapse button', async ({ page, api, state }) => {
  const cwd = join(state.root, 'editor-collapse-phone')
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'notes.txt'), 'alpha\n')
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e terminal collapse phone', cwd })
  try {
    await page.goto(`/sessions/${s.id}`)
    await page.locator('[data-inspector] [data-rail-item="explorer"]').click()
    await page.locator(`[data-file-node="${cwd}/notes.txt"]`).click()
    const area = page.locator('[data-editor-area]')
    await expect(area).toHaveAttribute('data-editor-area', 'open')
    await expect(area.locator('[data-editor-collapse-terminal]')).toHaveCount(1)
    await page.setViewportSize({ width: 390, height: 844 })
    await expect(page.locator('[data-terminal-slot]')).toHaveAttribute('data-terminal-slot', 'folded')
    await expect(area.locator('[data-editor-fold]')).toBeVisible()
    await expect(area.locator('[data-editor-collapse-terminal]')).toHaveCount(0)
    await expect(page.locator('[data-terminal-bar]')).toContainText('tap to bring it up')
  } finally {
    await page.setViewportSize({ width: 1440, height: 900 })
    await api.stopSession(s.id)
  }
})
