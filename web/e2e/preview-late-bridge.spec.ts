import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Route } from '@playwright/test'
import { expect, stopStrays, task, test } from './fixtures'
import { PREVIEW_BOOT_MS, endMade, launch, made, pick, previewCrew, previewFrame, previewSeam, scratchDir, stepIn } from './preview-fixtures'

// A page whose bridge starts after the frame's load, later than the pane's first 3 s (docs/protocol.md, Preview bridge: the hello
// said again while the frame stays on its load): the preview fixture's /late adds the bridge's tag only once its own request for
// /late-gate is answered, and the spec holds that answer until the pane has said the page does not load the bridge. Then the
// bridge starts, silent until a workbench says hello: the pane, still saying its hello, is answered, the words go, and Capture
// selects an element by name. A Refresh is a new load: its hellos start afresh and reach the bridge started late again. Every wait
// is on what the page draws or what the page asked; the gate is opened by the spec, never by a clock.
test.describe.configure({ mode: 'serial', timeout: 300_000 })

const NO_BRIDGE = "this page does not load Conductor's bridge: Capture takes the screenshot and your notes only. In the desktop app every page has it."

const m = made()
let runId = ''
let origin = ''
let gate = ''

test.beforeAll(async ({ api }) => {
  await stopStrays(api, [])
  gate = join(scratchDir(m, 'preview-late'), 'gate')
  runId = await launch(api, m, await previewCrew(api, m, 'e2e preview late bridge', gate, 'http://127.0.0.1:${CONDUCTOR_PORT_WEB}/late'))
  await stepIn(api, runId, 1, 'check:gate:1', 'running')
  origin = new URL(task(await api.run(runId), 'build').attempt!.preview!).origin
})

test.afterAll(async ({ api }) => {
  if (gate) writeFileSync(gate, '')
  await endMade(api, m)
})

test('a bridge that starts after the pane said the page does not load one is reached, and a selection names its element', async ({ page }) => {
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
  await expect(words).toHaveText(NO_BRIDGE, { timeout: PREVIEW_BOOT_MS })
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
  await expect(words).toHaveText(NO_BRIDGE, { timeout: PREVIEW_BOOT_MS })
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
