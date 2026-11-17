import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Page } from '@playwright/test'
import { walk, worst } from './contrast'
import { expect, git, stopStrays, task, test, useTheme } from './fixtures'
import { noSideways } from './layout'
import { endMade, launch, made, pick, previewCrew, previewFrame, previewSeam, scratchDir, stepIn } from './preview-fixtures'

// A preview in Conductor and a capture saved (docs/plans/browser-evidence.md, PR-4, #792), against a managed crew of the stub worker
// whose project manifest serves the fixture (web/e2e/fixtures/preview/) in the attempt's worktree, its page loading the bridge the
// tree names. Open in Conductor from the task's row; the label; Capture: a button under an overlay over the whole page, selected by
// stepping Down, the hit test naming the overlay; a second element joining; a third after a client-side navigation starting a second
// capture; an element no hit test returns, by walking the DOM and by selector; a region; Save, and the records read back with the
// server's commit and content. Then the page held at its commit while the spec commits B in the worktree: the label and the save
// read B, the capture says the page serves A. Then an animated control and a hover-styled one, as the pixels say; and a page without
// the bridge, said after 3 s. The screenshots come through the seam (Playwright's own of the frame's box), so the comparisons run on
// real pixels. Every wait is on what the page draws or what the server answers.
test.describe.configure({ mode: 'serial', timeout: 300_000 })

/**
 * The bound on a preview page drawn and its bridge ready: the app's start, the label's read (at most its 5 s), the fixture page and the
 * bridge's script loaded, the hello answered (at most 3 s). Each is quick on a quiet machine; on a loaded one the app's start alone
 * took 11 s, so the bound is the sum of their worst with room, above the expect's 15 s.
 */
const BOOT_MS = 60_000

const m = made()
let runId = ''
let worktree = ''
let origin = ''
let gate = ''

interface Capture {
  id: string
  url: string
  rev: number
  image: { how: string; sha256?: string; width?: number }
  origin: { kind: string; commit?: string; content?: string; tree?: string; serving: { state: string; page?: string } }
  annotations: Array<{ k: number; kind: string; note: string; element?: { id?: string; tag: string; hit: { self: boolean } } }>
}

test.beforeAll(async ({ api }) => {
  await stopStrays(api, [])
  gate = join(scratchDir(m, 'preview-capture'), 'gate')
  runId = await launch(api, m, await previewCrew(api, m, 'e2e preview capture', gate))
  // The attempt's check runs: its worktree is ready, its service answers behind the preview.
  await stepIn(api, runId, 1, 'check:gate:1', 'running')
  const a = task(await api.run(runId), 'build').attempt!
  origin = new URL(a.preview!).origin
  worktree = a.worktree!
})

test.afterAll(async ({ api }) => {
  if (gate) writeFileSync(gate, '')
  await endMade(api, m)
})

/** The preview page of the attempt, its bridge ready, its frame. */
async function previewPage(page: Page) {
  await previewSeam(page)
  await page.goto(`/preview?run=${encodeURIComponent(runId)}&task=build&attempt=1`)
  await expect(page.locator('[data-preview-pane]')).toHaveAttribute('data-preview-bridge', 'ready', { timeout: BOOT_MS })
  return previewFrame(page, origin)
}

const editorOf = (page: Page) => page.locator('[data-capture-editor]')

/** The saved capture the editor shows now, read back from the server. */
async function savedNow(page: Page, api: { ok<T>(m: string, p: string): Promise<T> }): Promise<Capture> {
  const saved = editorOf(page).locator('[data-capture-saved]')
  await expect(saved).toBeVisible()
  const id = (await saved.getAttribute('data-capture-saved'))!
  return api.ok<Capture>('GET', `/api/captures/${id}`)
}

test('Open in Conductor, the label, elements under an overlay, a navigation, the walked and the selected, a region, Save', async ({ page, api }) => {
  await previewSeam(page)
  await page.goto(`/runs/${encodeURIComponent(runId)}?view=tasks`)
  await page.locator('[data-task-menu="build"]').click()
  await page.locator('[data-task-preview-open="build"]').click()
  await expect(page).toHaveURL(new RegExp(`/preview\\?run=${runId}&task=build&attempt=1$`))
  const head = git(worktree, 'rev-parse', 'HEAD').trim()
  await expect(page.locator('[data-preview-label]')).toHaveText(new RegExp(`^attempt 1 of build · worktree at ${head.slice(0, 7)} .+ · changes as the agent works$`), { timeout: BOOT_MS })
  await expect(page.locator('[data-preview-pane]')).toHaveAttribute('data-preview-bridge', 'ready', { timeout: BOOT_MS })
  const frame = await previewFrame(page, origin)
  const editor = editorOf(page)

  // The button under the overlay that covers the whole page, by stepping Down from the overlay; the hit test names the overlay.
  await page.locator('[data-preview-capture]').click()
  await pick(page, frame, '#under', 'button#under.under', { downs: 1 })
  await expect(editor.locator('[data-capture-draft="1"]')).toBeVisible()
  await expect(editor.locator('[data-annotation="1"] [data-capture-element-summary]')).toContainText('button#under')
  await expect(editor.locator('[data-annotation="1"] [data-annotation-hit]')).toHaveText('a click at its centre reaches #overlay')
  await editor.locator('[data-annotation="1"] textarea').fill('The save button is covered')

  // A second element on the unchanged page joins the capture.
  await editor.locator('[data-capture-select]').click()
  await pick(page, frame, '#second', 'button#second.second', { downs: 1 })
  await expect(editor.locator('[data-annotation="2"] [data-capture-element-summary]')).toContainText('button#second')
  await expect(editor.locator('[data-capture-draft="1"]')).toBeVisible()
  await expect(editor.locator('[data-annotation-stable="false"]')).toHaveCount(0)

  // A third after a client-side navigation starts a second capture, at the new URL, with its own screenshot.
  await frame.evaluate(() => history.pushState({}, '', '/next'))
  await editor.locator('[data-capture-select]').click()
  await pick(page, frame, '#second', 'button#second.second', { downs: 1 })
  const second = editor.locator('[data-capture-draft="2"]')
  await expect(second).toBeVisible()
  await expect(second).toHaveAttribute('data-capture-url', `${origin}/next`)
  await expect(second.locator('[data-capture-changed]')).toHaveText('the page changed since this screenshot: this element starts a new capture')
  await expect(editor.locator('[data-annotation]')).toHaveCount(1)

  // An element no hit test returns: pointer-events none, reached by walking to its next sibling; an inert section's, by selector.
  await editor.locator('[data-capture-select]').click()
  await pick(page, frame, '#second', 'button#nopointer.nopointer', { downs: 1, siblings: 1 })
  await expect(editor.locator('[data-annotation="2"] [data-capture-element-summary]')).toContainText('button#nopointer')
  await editor.locator('[data-capture-selector] input, input[data-capture-selector]').first().fill('#inside')
  await editor.locator('[data-capture-describe]').click()
  await expect(editor.locator('[data-annotation="3"] [data-capture-element-summary]')).toContainText('button#inside')
  await expect(editor.locator('[data-annotation="3"] [data-capture-element-state]')).toContainText('inert')

  // A region drawn on the screenshot.
  await editor.locator('[data-capture-region]').click()
  const shot = (await editor.locator('[data-capture-shot]').boundingBox())!
  await page.mouse.move(shot.x + 10, shot.y + 10)
  await page.mouse.down()
  await page.mouse.move(shot.x + 80, shot.y + 50, { steps: 4 })
  await page.mouse.up()
  await expect(editor.locator('[data-annotation="4"]')).toHaveAttribute('data-annotation-kind', 'region')

  // Save the second capture, then the first; each read back with the server's own revision.
  await editor.locator('[data-capture-save]').click()
  const c2 = await savedNow(page, api)
  await editor.locator('[data-capture-draft-tab="1"]').click()
  await editor.locator('[data-capture-save]').click()
  await expect(editor.locator('[data-capture-draft-tab]')).toHaveCount(0)
  const c1 = await savedNow(page, api)

  for (const c of [c1, c2]) {
    expect(c.origin).toMatchObject({ kind: 'attempt', tree: 'worktree', commit: head })
    expect(c.origin.content).toMatch(/^[0-9a-f]{40,64}$/)
    expect(c.image.how).toBe('tab')
    expect(c.image.sha256).toMatch(/^[0-9a-f]{64}$/)
  }
  expect(c1.url).toBe(`${origin}/`)
  expect(c1.annotations.map((a) => [a.kind, a.element?.id, a.element?.hit.self, a.note])).toEqual([
    ['element', 'under', false, 'The save button is covered'],
    ['element', 'second', false, ''],
  ])
  expect(c2.url).toBe(`${origin}/next`)
  expect(c2.annotations.map((a) => [a.kind, a.element?.id])).toEqual([
    ['element', 'second'],
    ['element', 'nopointer'],
    ['element', 'inside'],
    ['region', undefined],
  ])
})

test('the page held at its commit while the spec commits B in the worktree: the label and the save read B, and the page serves A', async ({ page, api }) => {
  const frame = await previewPage(page)
  const a = git(worktree, 'rev-parse', 'HEAD').trim()
  writeFileSync(join(worktree, 'b.txt'), 'B\n')
  git(worktree, 'add', 'b.txt')
  git(worktree, 'commit', '-q', '-m', 'B')
  const b = git(worktree, 'rev-parse', 'HEAD').trim()
  // The label reads the tree every 5 s: B, though the page was not reloaded.
  await expect(page.locator('[data-preview-label]')).toContainText(`worktree at ${b.slice(0, 7)}`, { timeout: 30_000 })
  await page.locator('[data-preview-capture]').click()
  await pick(page, frame, '#second', 'button#second.second', { downs: 1 })
  await editorOf(page).locator('[data-capture-save]').click()
  const c = await savedNow(page, api)
  expect(c.origin.commit).toBe(b)
  expect(c.origin.serving).toEqual({ state: 'stale', page: a })
  await expect(editorOf(page).locator('[data-capture-serving="stale"]')).toContainText(a)
})

test('an animated control is kept stable: false and the next element is another capture; a hover-styled one too, once the pointer left it', async ({ page }) => {
  const frame = await previewPage(page)
  await frame.goto(`${origin}/motion`)
  await expect(page.locator('[data-preview-pane]')).toHaveAttribute('data-preview-bridge', 'ready', { timeout: BOOT_MS })
  const editor = editorOf(page)

  await page.locator('[data-preview-capture]').click()
  await pick(page, frame, '#spinner', '', { click: true })
  await expect(editor.locator('[data-capture-draft="1"] ')).toBeVisible()
  await expect(editor.locator('[data-annotation="1"] [data-annotation-stable="false"]')).toHaveText('this element was moving: its report is of a moment near the screenshot, not of it')
  // The spinner still turns: the next element's frame differs inside its box, so it starts a capture of its own.
  await editor.locator('[data-capture-select]').click()
  await pick(page, frame, '#plain', '', { click: true })
  await expect(editor.locator('[data-capture-draft="2"] [data-capture-changed]')).toBeVisible()

  // Picked under the pointer, the hover-styled control is drawn hovered: no DOM change, no epoch, but other pixels in its box.
  await editor.locator('[data-capture-select]').click()
  await pick(page, frame, '#hovered', '', { click: true })
  await expect(editor.locator('[data-capture-draft="3"]')).toBeVisible()
  await expect(editor.locator('[data-annotation="1"] [data-capture-element-state]')).toContainText('hovered')
  // The pointer leaves it for the next element: its box no longer matches the screenshot, and the next is another capture.
  await editor.locator('[data-capture-select]').click()
  await pick(page, frame, '#plain', '', { click: true })
  await expect(editor.locator('[data-capture-draft="4"]')).toBeVisible()
  await expect(editor.locator('[data-capture-draft-tab]')).toHaveCount(4)
})

test("a page that does not load the bridge says so after 3 s, and Capture keeps the screenshot and the notes", async ({ page, api }) => {
  const frame = await previewPage(page)
  await frame.goto(`${origin}/no-bridge`)
  await expect(page.locator('[data-preview-nobridge]')).toHaveText("this page does not load Conductor's bridge: Capture takes the screenshot and your notes only. In the desktop app every page has it.", { timeout: BOOT_MS })
  await expect(page.locator('[data-preview-pane]')).toHaveAttribute('data-preview-bridge', 'absent', { timeout: BOOT_MS })
  await page.locator('[data-preview-capture]').click()
  const editor = editorOf(page)
  await expect(editor.locator('[data-capture-shot]')).toBeVisible()
  await editor.locator('[data-capture-save]').click()
  const c = await savedNow(page, api)
  expect([c.annotations, c.image.how, c.origin.kind]).toEqual([[], 'tab', 'attempt'])
})

for (const theme of ['light', 'dark'] as const)
  test(`the pane and its editor read in the ${theme} theme`, async ({ page }) => {
    const frame = await previewPage(page)
    await useTheme(page, theme, '[data-preview-pane][data-preview-bridge="ready"]')
    const again = await previewFrame(page, origin)
    expect(again).toBeTruthy()
    void frame
    await page.locator('[data-preview-capture]').click()
    await pick(page, again, '#second', 'button#second.second', { downs: 1 })
    await expect(editorOf(page).locator('[data-annotation="1"]')).toBeVisible()
    const { judged, failures } = await walk(page, page.locator('[data-preview-pane]'))
    expect(judged).toBeGreaterThan(10)
    expect(failures, worst(failures)).toEqual([])
  })

test('on a phone the editor is the page over the frame, out of the way while selecting, and nothing is wider than the screen', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const frame = await previewPage(page)
  await page.locator('[data-preview-capture]').click()
  // Selecting: the frame shows, the editor waits.
  await expect(page.locator('[data-preview-selecting]')).toBeVisible()
  await expect(editorOf(page)).toBeHidden()
  await pick(page, frame, '#second', 'button#second.second', { downs: 1 })
  const editor = editorOf(page)
  await expect(editor.locator('[data-annotation="1"]')).toBeVisible()
  const pane = (await page.locator('[data-preview-stage]').boundingBox())!
  const drawn = (await editor.boundingBox())!
  // The editor covers the frame's stage, edge to edge.
  expect(Math.abs(drawn.x - pane.x)).toBeLessThan(1)
  expect(Math.abs(drawn.width - pane.width)).toBeLessThan(1)
  await noSideways(page)
})
