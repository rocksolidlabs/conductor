import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Frame, Page, Route } from '@playwright/test'
import { expect, stopStrays, task, test, type Api } from './fixtures'
import { PREVIEW_BOOT_MS, endMade, launch, made, pick, previewCrew, previewFrame, previewSeam, scratchDir, stepIn } from './preview-fixtures'

// A page whose bridge starts after the frame's load, later than the pane's first 3 s (docs/protocol.md, Preview bridge: the hello
// said again while the frame stays on its load): the preview fixture's /late adds the bridge's tag only once its own request for
// /late-gate is answered, and the spec holds that answer until the pane has said the page does not load the bridge. Then the
// bridge starts, silent until a workbench says hello: the pane, still saying its hello, is answered, the words go, and Capture
// selects an element by name. A Refresh is a new load: its hellos start afresh and reach the bridge started late again. And Capture
// keeps the frame's box: the editor opens over the frame's edge, never beside it, so the page is not laid out again under the
// pointer between the press and the pick. Every wait is on what the page draws or what the page asked; the gate is opened by the
// spec, never by a clock.
test.describe.configure({ mode: 'serial', timeout: 300_000 })

/** The pane's words from 3 s while the page has not answered and the pane keeps asking. */
const ASKING = "this page has not answered Conductor's bridge yet, and the pane keeps asking: until it answers, Capture takes the screenshot and your notes only."
/** The editor's words on a capture made before the page answered, once it has. */
const ANSWERED = 'the page answers now: an element selected joins this screenshot while the page still looks as it does in it'

const m = made()
let runId = ''
let origin = ''
let gate = ''

test.beforeAll(async ({ api }) => {
  await stopStrays(api, [])
  gate = join(scratchDir(m, 'preview-late'), 'gate')
  runId = await launch(api, m, await previewCrew(api, m, 'e2e preview late bridge', gate, 'http://127.0.0.1:${CONDUCTOR_PORT_WEB}/late#token=${CONDUCTOR_TREE_SECRET}'))
  await stepIn(api, runId, 1, 'check:gate:1', 'running')
  origin = new URL(task(await api.run(runId), 'build').attempt!.preview!).origin
})

test.afterAll(async ({ api }) => {
  if (gate) writeFileSync(gate, '')
  await endMade(api, m)
})

/** The preview page of the run's attempt, `&retake=` the capture given. */
const previewURL = (retake = '') => `/preview?run=${encodeURIComponent(runId)}&task=build&attempt=1${retake ? `&retake=${retake}` : ''}`

/** The page's requests for its gate: held while shut, let through once opened (and those held then). */
async function pageGate(page: Page, shut: boolean) {
  const g = {
    shut,
    held: [] as Route[],
    async open() {
      g.shut = false
      for (const r of g.held.splice(0)) await r.continue()
    },
  }
  await page.route(
    (u) => u.origin === origin && u.pathname === '/late-gate',
    (r) => (g.shut ? void g.held.push(r) : void r.continue()),
  )
  return g
}

interface Saved {
  id: string
  url: string
  retakeOf?: string
  annotations: Array<{ element?: { id?: string } }>
}

/** Saves the editor's capture and reads it back; the spec's routing ended first (a routed request's multipart body is not sent whole). */
async function save(page: Page, api: Api): Promise<Saved> {
  await page.unrouteAll({ behavior: 'wait' })
  const editor = page.locator('[data-capture-editor]')
  await editor.locator('[data-capture-save]').click()
  const saved = editor.locator('[data-capture-saved]')
  await expect(saved).toBeVisible()
  return api.ok<Saved>('GET', `/api/captures/${await saved.getAttribute('data-capture-saved')}`)
}

/** The pane's count of the frame's loads. */
const loads = async (page: Page) => Number(await page.locator('[data-preview-pane]').getAttribute('data-preview-loads'))

test('a bridge that starts after the pane said the page has not answered is reached, and a selection names its element', async ({ page }) => {
  // The page's requests for its gate, held until the spec answers them.
  const held: Route[] = []
  await page.route(
    (u) => u.origin === origin && u.pathname === '/late-gate',
    (r) => void held.push(r),
  )
  await previewSeam(page)
  await page.goto(`/preview?run=${encodeURIComponent(runId)}&task=build&attempt=1`)
  const pane = page.locator('[data-preview-pane]')
  const words = page.locator('[data-preview-nobridge]')

  // The page loaded without its bridge: the pane says so at its 3 s.
  await expect(words).toHaveText(ASKING, { timeout: PREVIEW_BOOT_MS })
  await expect(pane).toHaveAttribute('data-preview-bridge', 'absent')
  const frame = await previewFrame(page, origin)
  await expect.poll(() => held.length, { timeout: PREVIEW_BOOT_MS, message: 'the page asked for its gate' }).toBeGreaterThan(0)
  expect(await frame.evaluate(() => document.querySelector('script[data-late-bridge]') === null)).toBe(true)

  // The gate opens: the bridge starts, long after the load, and the pane's hello said again reaches it.
  for (const r of held.splice(0)) await r.continue()
  await expect(pane).toHaveAttribute('data-preview-bridge', 'ready', { timeout: PREVIEW_BOOT_MS })
  await expect(words).toHaveCount(0)

  // Capture selects: the element is named, as with a bridge loaded with the page.
  await page.locator('[data-preview-capture]').click()
  await pick(page, frame, '#second', 'button#second.second', { downs: 1 })
  const editor = page.locator('[data-capture-editor]')
  await expect(editor.locator('[data-annotation="1"] [data-capture-element-summary]')).toContainText('button#second')

  // A Refresh is a new load, its bridge late again: the words once more at its 3 s, then the bridge reached once its gate opens.
  await editor.locator('[data-capture-close]').click()
  await page.locator('[data-preview-refresh]').click()
  await expect(words).toHaveText(ASKING, { timeout: PREVIEW_BOOT_MS })
  await expect.poll(() => held.length, { timeout: PREVIEW_BOOT_MS, message: 'the page asked for its gate again' }).toBeGreaterThan(0)
  for (const r of held.splice(0)) await r.continue()
  await expect(pane).toHaveAttribute('data-preview-bridge', 'ready', { timeout: PREVIEW_BOOT_MS })
  await expect(words).toHaveCount(0)
  const again = await previewFrame(page, origin)
  await page.locator('[data-preview-capture]').click()
  await pick(page, again, '#under', 'button#under.under', { downs: 1 })
  await expect(editor.locator('[data-capture-draft="2"]')).toBeVisible()
  await expect(editor.locator('[data-annotation="1"] [data-capture-element-summary]')).toContainText('button#under')
})

test('Capture keeps the frame’s box: the editor opens over the frame’s edge, out of the way while selecting, and the page is not laid out again under the pointer', async ({ page }) => {
  await page.route(
    (u) => u.origin === origin && u.pathname === '/late-gate',
    (r) => r.continue(),
  )
  await previewSeam(page)
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(`/preview?run=${encodeURIComponent(runId)}&task=build&attempt=1`)
  const pane = page.locator('[data-preview-pane]')
  await expect(pane).toHaveAttribute('data-preview-bridge', 'ready', { timeout: PREVIEW_BOOT_MS })
  const frame = await previewFrame(page, origin)
  const frameEl = page.locator('[data-preview-frame]')
  const editor = page.locator('[data-capture-editor]')
  const before = (await frameEl.boundingBox())!
  const target = (await frame.locator('#second').boundingBox())!
  const width = await frame.evaluate(() => window.innerWidth)

  // The press: selection mode on the frame as it was, nothing of the editor over it while the person aims.
  await page.locator('[data-preview-capture]').click()
  await frame.waitForFunction(() => (window as unknown as { selecting(): boolean }).selecting())
  await expect(editor).toBeHidden()
  expect(await frameEl.boundingBox()).toEqual(before)
  expect(await frame.locator('#second').boundingBox()).toEqual(target)
  expect(await frame.evaluate(() => window.innerWidth)).toBe(width)
  await pick(page, frame, '#second', 'button#second.second', { downs: 1 })

  // The pick made: the editor shows over the stage's right edge, the frame and the page as they were.
  await expect(editor.locator('[data-annotation="1"] [data-capture-element-summary]')).toContainText('button#second')
  expect(await frameEl.boundingBox()).toEqual(before)
  expect(await frame.evaluate(() => window.innerWidth)).toBe(width)
  const stage = (await page.locator('[data-preview-stage]').boundingBox())!
  const box = (await editor.boundingBox())!
  expect(box.x).toBeGreaterThan(stage.x)
  expect(Math.abs(box.x + box.width - (stage.x + stage.width))).toBeLessThanOrEqual(1)
  expect([box.y >= stage.y - 1, box.y + box.height <= stage.y + stage.height + 1]).toEqual([true, true])
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)

  // A second element, picked from the editor, joins the same capture: the frame never moved between the two.
  await editor.locator('[data-capture-select]').click()
  await pick(page, frame, '#under', 'button#under.under', { downs: 1 })
  await expect(editor.locator('[data-annotation="2"] [data-capture-element-summary]')).toContainText('button#under')
  await expect(editor.locator('[data-capture-draft="1"]')).toBeVisible()
  await expect(editor.locator('[data-capture-draft="2"]')).toHaveCount(0)
})

test('a draft made before the bridge answers names the page without the fragment of the preview’s address, shown and saved', async ({ page, api }) => {
  const held: Route[] = []
  await page.route(
    (u) => u.origin === origin && u.pathname === '/late-gate',
    (r) => void held.push(r),
  )
  await previewSeam(page)
  await page.goto(`/preview?run=${encodeURIComponent(runId)}&task=build&attempt=1`)
  const pane = page.locator('[data-preview-pane]')
  await expect(pane).toHaveAttribute('data-preview-bridge', 'absent', { timeout: PREVIEW_BOOT_MS })
  // The frame is opened at the preview's address, its fragment with it.
  expect(await page.locator('[data-preview-frame]').getAttribute('src')).toMatch(/\/late#token=[0-9a-f]{64}$/)

  // Capture while the page has not answered: the screenshot alone, at the frame's address without its fragment.
  await page.locator('[data-preview-capture]').click()
  const editor = page.locator('[data-capture-editor]')
  const draft = editor.locator('[data-capture-draft="1"]')
  await expect(draft).toHaveAttribute('data-capture-url', `${origin}/late`)
  await expect(draft.locator('code')).toHaveText(`${origin}/late`)
  // The gate let through and the spec's routing ended before the save: a routed request's multipart body is not sent whole.
  for (const r of held.splice(0)) await r.continue()
  await page.unrouteAll({ behavior: 'wait' })
  await editor.locator('[data-capture-save]').click()
  const saved = editor.locator('[data-capture-saved]')
  await expect(saved).toBeVisible()
  const c = await api.ok<{ url: string }>('GET', `/api/captures/${await saved.getAttribute('data-capture-saved')}`)
  expect(c.url).toBe(`${origin}/late`)
})

test('a capture made before the page answered takes its element once it has, while the words say the pane keeps asking', async ({ page }) => {
  const g = await pageGate(page, true)
  await previewSeam(page)
  await page.goto(previewURL())
  const pane = page.locator('[data-preview-pane]')
  const words = page.locator('[data-preview-nobridge]')
  await expect(words).toHaveText(ASKING, { timeout: PREVIEW_BOOT_MS })
  await expect(pane).toHaveAttribute('data-preview-bridge', 'absent')

  // Capture now: the screenshot alone.
  await page.locator('[data-preview-capture]').click()
  const editor = page.locator('[data-capture-editor]')
  const draft = editor.locator('[data-capture-draft="1"]')
  await expect(draft.locator('[data-capture-shot]')).toBeVisible()
  await expect(editor.locator('[data-annotation]')).toHaveCount(0)

  // The page answers: the words go, the editor says the capture can take its element, and a selection joins it.
  await g.open()
  await expect(pane).toHaveAttribute('data-preview-bridge', 'ready', { timeout: PREVIEW_BOOT_MS })
  await expect(words).toHaveCount(0)
  await expect(draft.locator('[data-capture-changed]')).toHaveText(ANSWERED)
  const frame = await previewFrame(page, origin)
  await editor.locator('[data-capture-select]').click()
  await pick(page, frame, '#second', 'button#second.second', { downs: 1 })
  await expect(editor.locator('[data-annotation="1"] [data-capture-element-summary]')).toContainText('button#second')
  await expect(draft).toBeVisible()
  await expect(editor.locator('[data-capture-draft="2"]')).toHaveCount(0)
  await expect(draft.locator('[data-capture-changed]')).toHaveCount(0)
  // The page's moment now, named without the fragment the preview was opened with.
  await expect(draft).toHaveAttribute('data-capture-url', `${origin}/late`)
})

test('Capture again waits for a bridge that answers late, describes on the page as it is, and the retake names the first capture', async ({ page, api }) => {
  await pageGate(page, false)
  await previewSeam(page)
  await page.goto(previewURL())
  const pane = page.locator('[data-preview-pane]')
  await expect(pane).toHaveAttribute('data-preview-bridge', 'ready', { timeout: PREVIEW_BOOT_MS })
  await page.locator('[data-preview-capture]').click()
  await pick(page, await previewFrame(page, origin), '#second', 'button#second.second', { downs: 1 })
  const first = (await save(page, api)).id

  // The retake's page, its bridge held back.
  const g = await pageGate(page, true)
  await page.goto(previewURL(first))
  const retake = page.locator('[data-preview-retake]')
  await expect(retake).toBeVisible({ timeout: PREVIEW_BOOT_MS })
  const frame = await previewFrame(page, origin)
  await frame.evaluate(() => ((window as unknown as { kept: string }).kept = 'this load'))
  await retake.click()
  await expect(page.locator('[data-preview-retake-waiting]')).toBeVisible()

  // The page answers: the retake is made on the page as it was, never loaded again.
  await g.open()
  const editor = page.locator('[data-capture-editor]')
  // The retake's own draft (the editor shows a draft over the saved captures it lists).
  await expect(editor.locator('[data-capture-draft="1"]')).toBeVisible({ timeout: PREVIEW_BOOT_MS })
  await expect(editor.locator('[data-annotation="1"] [data-capture-element-summary]')).toContainText('button#second')
  expect(await frame.evaluate(() => (window as unknown as { kept?: string }).kept)).toBe('this load')
  await expect(page.locator('[data-preview-retake-error]')).toHaveCount(0)
  await expect(retake).toBeVisible()
  const second = await save(page, api)
  expect([second.retakeOf, second.annotations.map((a) => a.element?.id)]).toEqual([first, ['second']])
})

test('Capture again describes an element the person opened on the page, and from another page goes to the capture’s first, describing on the next press', async ({ page, api }) => {
  await previewSeam(page)
  await page.goto(previewURL())
  const pane = page.locator('[data-preview-pane]')
  await expect(pane).toHaveAttribute('data-preview-bridge', 'ready', { timeout: PREVIEW_BOOT_MS })

  // The dialog page, its dialog opened by the person, its button captured.
  const before = await loads(page)
  const frame = await previewFrame(page, origin)
  await frame.goto(`${origin}/dialog`)
  await expect.poll(() => loads(page), { timeout: PREVIEW_BOOT_MS }).toBeGreaterThan(before)
  await expect(pane).toHaveAttribute('data-preview-bridge', 'ready', { timeout: PREVIEW_BOOT_MS })
  await frame.locator('#open').click()
  await page.locator('[data-preview-capture]').click()
  await pick(page, frame, '#in-sheet', 'button#in-sheet')
  const first = await save(page, api)
  expect(first.url).toBe(`${origin}/dialog`)

  // The retake's page opens at the preview's own page: the first press sends the frame to the capture's and describes nothing.
  await page.goto(previewURL(first.id))
  await expect(pane).toHaveAttribute('data-preview-bridge', 'ready', { timeout: PREVIEW_BOOT_MS })
  const retake = page.locator('[data-preview-retake]')
  const at = await loads(page)
  await retake.click()
  await expect(page.locator('[data-preview-retake-moved]')).toBeVisible({ timeout: PREVIEW_BOOT_MS })
  await expect.poll(() => loads(page), { timeout: PREVIEW_BOOT_MS }).toBeGreaterThan(at)
  await expect(pane).toHaveAttribute('data-preview-bridge', 'ready', { timeout: PREVIEW_BOOT_MS })
  const editor = page.locator('[data-capture-editor]')
  await expect(editor.locator('[data-capture-draft]')).toHaveCount(0)

  // The person opens the dialog, then presses again: its button is described, the dialog still open.
  let there: Frame | undefined
  await expect.poll(() => (there = page.frames().find((f) => f.url().startsWith(`${origin}/dialog`))) !== undefined).toBe(true)
  await there!.locator('#open').click()
  await expect(there!.locator('#in-sheet')).toBeVisible()
  await retake.click()
  await expect(editor.locator('[data-capture-draft="1"]')).toBeVisible({ timeout: PREVIEW_BOOT_MS })
  await expect(editor.locator('[data-annotation="1"] [data-capture-element-summary]')).toContainText('button#in-sheet')
  await expect(there!.locator('#sheet')).toBeVisible()
  const second = await save(page, api)
  expect([second.retakeOf, second.annotations.map((a) => a.element?.id)]).toEqual([first.id, ['in-sheet']])
})
