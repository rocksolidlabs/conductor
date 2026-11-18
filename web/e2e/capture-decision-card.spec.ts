import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Browser, Locator, Page } from '@playwright/test'
import { expect, stopStrays, task, test } from './fixtures'
import { decision, decisionServer, operatorPage, type DecisionServer } from './decisions-helpers'
import { PREVIEW_BOOT_MS, PREVIEW_SERVICE, pick, previewFrame, previewSeam, scratchDir, made, endMade } from './preview-fixtures'
import { inside, layoutRoot, noSideways, tapTarget, uncut } from './layout'

// A capture sent to a pending decision, where the decision is answered (docs/plans/browser-evidence.md, PR-11; the card's rows are
// DecisionCaptures, who reads what is utils/decisionCaptures.ts): on a server of its own, a managed crew whose stub worker serves the
// preview fixture and asks the operator one approval; a capture of its attempt saved through the pane and sent to that decision.
// The operator's card on Needs you and in the decision's modal (the one the run's task and the ticket's Decisions open) shows the
// capture's screenshot small, its note, the element's name and Open, which shows the capture whole; wide and at 390 x 844, in both
// themes, by measures. A screenshot the server does not give is words, never a broken image. A decision link's holder, whom every
// capture route refuses, is shown the reference its history gives, as words, and its page asks no capture route.
test.describe.configure({ mode: 'serial', timeout: 300_000 })

const m = made()
let srv: DecisionServer
let runId = ''
let origin = ''
let gate = ''
let captureId = ''
let sendId = ''
let shotWidth = 0
const NOTE = 'PAGE-NOTE the save button is covered'
const SIZES = [
  { width: 1440, height: 900 },
  { width: 390, height: 844 },
]

test.beforeAll(async ({ browser }) => {
  test.setTimeout(420_000)
  srv = await decisionServer('capture-decision-card', [{ id: 'worker-decides', from: 'worker', env: { STUB_DECISION: 'approval' } }])
  await stopStrays(srv.api, [])
  gate = join(scratchDir(m, 'capture-decision-card'), 'gate')
  const crew = await srv.api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
    name: 'e2e capture decision card',
    goal: 'ask the operator, previewed',
    cwd: '',
    where: 'server',
    isolation: 'worktree',
    openAfterLaunch: false,
    yolo: null,
    members: [{ name: 'builder', agentId: 'worker-decides', prompt: '', start: { when: 'task' } }],
    tasks: [
      {
        id: 'build',
        member: 'builder',
        kind: 'work',
        instructions: 'Build it, once the operator approves.',
        checks: [{ id: 'gate', argv: ['sh', '-c', 'until [ -e "$1" ]; do sleep 0.2; done; echo gate open', 'sh', gate], timeoutSeconds: 900 }],
      },
    ],
    manifest: {
      ports: ['web'],
      services: [{ id: 'web', argv: ['python3', PREVIEW_SERVICE, '${CONDUCTOR_PORT_WEB}'] }],
      ready: [{ id: 'up', url: 'http://127.0.0.1:${CONDUCTOR_PORT_WEB}/ready', intervalSeconds: 1, timeoutSeconds: 60 }],
      preview: 'http://127.0.0.1:${CONDUCTOR_PORT_WEB}/',
    },
  })
  runId = (await srv.api.launchCrew(crew.crew.id)).id
  await expect
    .poll(async () => (await decision(srv.api, 'dr-1').catch(() => null))?.state ?? 'absent', { timeout: 120_000, intervals: [250], message: 'dr-1 asked' })
    .toBe('open')
  await expect
    .poll(async () => task(await srv.api.run(runId), 'build').attempt?.preview ?? '', { timeout: 120_000, intervals: [250], message: 'the preview served' })
    .not.toBe('')
  origin = new URL(task(await srv.api.run(runId), 'build').attempt!.preview!).origin

  // A capture of the covered button, saved through the pane, then sent to dr-1.
  const page = await operatorPage(browser, srv.started)
  await previewSeam(page)
  await page.goto(`${srv.started.baseURL}/preview?run=${encodeURIComponent(runId)}&task=build&attempt=1`)
  await expect(page.locator('[data-preview-pane]')).toHaveAttribute('data-preview-bridge', 'ready', { timeout: PREVIEW_BOOT_MS })
  const frame = await previewFrame(page, origin)
  const editor = page.locator('[data-capture-editor]')
  await page.locator('[data-preview-capture]').click()
  await pick(page, frame, '#under', 'button#under.under', { downs: 1 })
  await editor.locator('[data-annotation="1"] textarea').fill(NOTE)
  await editor.locator('[data-capture-save]').click()
  const saved = editor.locator('[data-capture-saved]')
  await expect(saved).toBeVisible({ timeout: 30_000 })
  captureId = (await saved.getAttribute('data-capture-saved'))!
  await page.context().close()
  const cap = await srv.api.ok<{ rev: number; image: { width?: number; how: string } }>('GET', `/api/captures/${captureId}`)
  expect(cap.image.how, 'the capture kept a screenshot').not.toBe('none')
  shotWidth = cap.image.width ?? 0
  expect(shotWidth).toBeGreaterThan(0)
  const sent = await srv.api.ok<{ send: { id: string; state: string } }>('POST', `/api/captures/${captureId}/send`, { opId: `cdc-${captureId}`, rev: cap.rev, to: { kind: 'decision', decision: 'dr-1' } })
  expect(sent.send.state).toBe('sent')
  sendId = sent.send.id
})

test.afterAll(async () => {
  if (gate) writeFileSync(gate, '')
  if (!srv) return
  if (runId) await srv.api.stopRun(runId)
  await endMade(srv.api, m)
  await srv.stop()
})

/** The operator's page in a theme, every API path it asks recorded. */
async function themed(browser: Browser, theme: 'light' | 'dark', size = SIZES[0]!): Promise<{ page: Page; asked: string[] }> {
  const ctx = await browser.newContext({ viewport: size, colorScheme: theme })
  await ctx.addInitScript(
    ({ t, theme }) => {
      localStorage.setItem('conductor.workbenchToken', t)
      localStorage.setItem('conductor.displayName', 'Nate')
      localStorage.setItem('conductor-color-mode', theme)
    },
    { t: srv.started.token, theme },
  )
  const page = await ctx.newPage()
  const asked: string[] = []
  page.on('request', (r) => asked.push(new URL(r.url()).pathname))
  return { page, asked }
}

/** An image's natural width once its bytes are decoded (0 while not, or for a broken one). */
const natural = (img: Locator) => img.evaluate((i: HTMLImageElement) => (i.complete ? i.naturalWidth : 0))

/** The operator's row of the send in `within`: the screenshot small and decoded, the note, the element's name, Open; measured. */
async function operatorRow(page: Page, within: Locator, theme: string) {
  const row = within.locator(`[data-decision-capture="${sendId}"]`)
  const thumb = row.locator('[data-decision-capture-thumb]')
  await expect(thumb).toBeVisible({ timeout: 30_000 })
  await expect.poll(() => natural(thumb), { timeout: 15_000, message: 'the screenshot decoded' }).toBe(shotWidth)
  await expect(row.locator('[data-decision-capture-note]')).toHaveText(NOTE)
  await expect(row.locator('[data-decision-capture-element]')).toContainText('button#under.under')
  await expect(row).toContainText(`capture:${captureId}/${sendId}`)
  await expect(page.locator('html')).toHaveClass(theme === 'dark' ? /\bdark\b/ : /^(?!.*\bdark\b)/)
  // Small: inside the row, at most 128 px wide.
  await inside(thumb, row)
  const box = (await thumb.boundingBox())!
  expect(box.width).toBeLessThanOrEqual(128.5)
  await row.locator('[data-decision-capture-note]').scrollIntoViewIfNeeded()
  await uncut(row.locator('[data-decision-capture-note]'))
  await row.locator('[data-decision-capture-open]').scrollIntoViewIfNeeded()
  await tapTarget(row.locator('[data-decision-capture-open]'))

  // Open: the capture whole, its screenshot at its size within the card, every annotation, what changed since.
  await row.locator('[data-decision-capture-open]').click()
  const view = row.locator('[data-decision-capture-view]')
  await expect(view).toBeVisible()
  const full = view.locator('[data-decision-capture-full]')
  await expect.poll(() => natural(full), { timeout: 15_000 }).toBe(shotWidth)
  await inside(full, row)
  await expect(view.locator('[data-decision-capture-annotation="1"]')).toContainText(NOTE)
  await expect(view.locator('[data-decision-capture-now]')).toHaveText('Now r1: nothing changed since.')
  await expect(row.locator('[data-decision-capture-open]')).toHaveAttribute('aria-expanded', 'true')
}

for (const theme of ['light', 'dark'] as const) {
  test(`Needs you (${theme}): the capture sent to the decision is shown, its screenshot small, its note, its element and Open, wide and on a phone`, async ({ browser }) => {
    const { page, asked } = await themed(browser, theme)
    for (const size of SIZES) {
      await page.setViewportSize(size)
      await page.goto(`${srv.started.baseURL}/decisions?item=decision%3Adr-1`)
      await expect(page.locator(layoutRoot(size.width)).first()).toBeVisible({ timeout: 30_000 })
      const card = page.locator('[data-decision-card][data-decision-id="dr-1"]')
      await expect(card.locator('[data-decision-question]')).toBeVisible({ timeout: 30_000 })
      await operatorRow(page, card, theme)
      await noSideways(page)
    }
    expect(asked.filter((p) => p === `/api/captures/${captureId}/image`).length, 'the screenshot asked').toBeGreaterThan(0)
    await page.context().close()
  })
}

test('the decision’s modal, as the run’s task and the ticket’s Decisions open it, shows the capture the same way, on a phone too', async ({ browser }) => {
  const { page } = await themed(browser, 'dark')
  for (const size of SIZES) {
    await page.setViewportSize(size)
    await page.goto(`${srv.started.baseURL}/runs/${encodeURIComponent(runId)}?view=tasks&task=build`)
    await expect(page.locator(layoutRoot(size.width)).first()).toBeVisible({ timeout: 30_000 })
    await page.locator('[data-task-answer="build"]').first().click()
    const modal = page.locator('[data-decision-modal][data-decision-modal-id="dr-1"]')
    await expect(modal.locator('[data-decision-question]')).toBeVisible({ timeout: 30_000 })
    await operatorRow(page, modal, 'dark')
    await inside(modal.locator(`[data-decision-capture="${sendId}"]`), modal)
    await noSideways(page, { root: modal })
    await page.keyboard.press('Escape')
    await expect(modal).toHaveCount(0)
  }
  await page.context().close()
})

test('a screenshot the server does not give is words in its place, never a broken image; the note and the element stay', async ({ browser }) => {
  const { page } = await themed(browser, 'light', SIZES[1])
  let refused = 0
  await page.route(
    (u) => u.pathname === `/api/captures/${captureId}/image`,
    (route) => {
      refused++
      return route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ error: { code: 'not_found', message: 'no such capture' } }) })
    },
  )
  await page.goto(`${srv.started.baseURL}/decisions?item=decision%3Adr-1`)
  const row = page.locator(`[data-decision-card][data-decision-id="dr-1"] [data-decision-capture="${sendId}"]`)
  await expect(row).toHaveAttribute('data-decision-capture-shot', 'unread', { timeout: 30_000 })
  await expect(row.locator('[data-decision-capture-noshot]')).toHaveText('The screenshot could not be read: the capture may have been deleted since')
  await expect(row.locator('img')).toHaveCount(0)
  await expect(row.locator('[data-decision-capture-note]')).toHaveText(NOTE)
  await expect(row.locator('[data-decision-capture-element]')).toContainText('button#under.under')
  await row.locator('[data-decision-capture-noshot]').scrollIntoViewIfNeeded()
  await uncut(row.locator('[data-decision-capture-noshot]'))
  await row.locator('[data-decision-capture-open]').click()
  await expect(row.locator('[data-decision-capture-view]')).toBeVisible()
  await expect(row.locator('img')).toHaveCount(0)
  expect(refused).toBeGreaterThan(0)
  await page.context().close()
})

for (const theme of ['light', 'dark'] as const) {
  test(`a decision link’s holder (${theme}) is shown the reference as words, and its page asks no capture route`, async ({ browser }) => {
    const link = await srv.api.ok<{ url: string }>('POST', `/api/runs/${runId}/links`, { role: 'control', decide: true, label: `e2e capture card link ${theme}` })
    // The page holds a workbench token where the workbench keeps it: it is the card that asks nothing, not the server that refuses.
    const ctx = await browser.newContext({ viewport: SIZES[1], colorScheme: theme })
    await ctx.addInitScript(
      ({ t, theme }) => {
        localStorage.setItem('conductor.workbenchToken', t)
        localStorage.setItem('conductor-color-mode', theme)
      },
      { t: srv.started.token, theme },
    )
    const phone = await ctx.newPage()
    const asked: string[] = []
    phone.on('request', (r) => asked.push(new URL(r.url()).pathname))
    await phone.goto(link.url)
    await phone.getByRole('textbox', { name: 'Your name' }).fill('e2e link holder')
    await expect(phone.getByRole('textbox', { name: 'Your name' })).toHaveValue('e2e link holder')
    await phone.getByRole('button', { name: 'Join crew' }).click()
    const decide = phone.locator('[data-join-run-decide]')
    await decide.locator('[data-inbox-row="decision:dr-1"]').click()
    const card = decide.locator('[data-inbox-item="decision:dr-1"] [data-decision-card][data-decision-id="dr-1"]')
    await expect(card.locator('[data-decision-question]')).toBeVisible({ timeout: 30_000 })
    const captures = card.locator('[data-decision-captures]')
    await expect(captures).toHaveAttribute('data-decision-captures-reader', 'refused', { timeout: 30_000 })
    await expect(captures.locator(`[data-decision-capture-ref="${sendId}"]`)).toContainText(`capture:${captureId}/${sendId}`)
    await expect(captures.locator(`[data-decision-capture-ref="${sendId}"]`)).toContainText('revision 1')
    await expect(captures.locator('[data-decision-captures-refused]')).toHaveText("A capture's screenshot and notes open on the operator's workbench.")
    await expect(captures.locator('img')).toHaveCount(0)
    await expect(captures.locator('[data-decision-capture-open]')).toHaveCount(0)
    await captures.locator('[data-decision-captures-refused]').scrollIntoViewIfNeeded()
  await uncut(captures.locator('[data-decision-captures-refused]'))
    await expect(phone.locator('html')).toHaveClass(theme === 'dark' ? /\bdark\b/ : /^(?!.*\bdark\b)/)
    // The page at a phone's width: its header's every control inside the window, nothing scrolling or cut sideways.
    await joinHeaderFits(phone, SIZES[1].width)
    await noSideways(phone)
    expect(asked.filter((p) => /^\/api\/captures(\/|$)/.test(p) || /^\/api\/decisions\/[^/]+\/captures$/.test(p)), 'no capture route asked').toEqual([])
    expect(await captures.textContent()).not.toContain('PAGE-NOTE')
    await ctx.close()

    // A holder with no workbench token, as on a phone of the holder's own: the header shows the brand, and fits at 390 x 844 (the
    // mark alone) and at 1440 x 900 (the mark and its word), nothing of the page scrolling sideways.
    const own = await browser.newContext({ viewport: SIZES[1], colorScheme: theme })
    await own.addInitScript((theme) => localStorage.setItem('conductor-color-mode', theme), theme)
    const bare = await own.newPage()
    await bare.goto(link.url)
    await bare.getByRole('textbox', { name: 'Your name' }).fill('e2e phone holder')
    await expect(bare.getByRole('textbox', { name: 'Your name' })).toHaveValue('e2e phone holder')
    await bare.getByRole('button', { name: 'Join crew' }).click()
    await expect(bare.locator('[data-join-run-decide] [data-inbox-row="decision:dr-1"]')).toBeVisible({ timeout: 30_000 })
    await expect(bare.locator('html')).toHaveClass(theme === 'dark' ? /\bdark\b/ : /^(?!.*\bdark\b)/)
    await joinHeaderFits(bare, SIZES[1].width)
    await expect(bare.locator('[data-join-wordmark]')).toBeHidden()
    await noSideways(bare)
    await bare.setViewportSize(SIZES[0])
    await joinHeaderFits(bare, SIZES[0].width)
    await expect(bare.locator('[data-join-wordmark]')).toBeVisible()
    await noSideways(bare)
    await own.close()
  })
}

/** The join page's header within a window `width` wide: its box, and every control and word drawn in it, inside the header and the window. */
async function joinHeaderFits(page: Page, width: number) {
  const header = page.locator('[data-join-header]')
  await expect(header).toBeVisible()
  await expect.poll(() => header.evaluate((h) => Math.round(h.getBoundingClientRect().width)), { message: 'the header as wide as the window' }).toBe(width)
  await inside(header.locator(':scope > *'), header)
  const past = await header.evaluate((h, w) => Array.from(h.querySelectorAll('*')).filter((e) => { const r = e.getBoundingClientRect(); return r.width > 0 && r.right > w + 0.5 }).map((e) => e.outerHTML.slice(0, 80)), width)
  expect(past, 'nothing of the header past the window’s right edge').toEqual([])
  expect(await header.evaluate((h) => h.scrollWidth - h.clientWidth), 'the header holds what it draws').toBeLessThanOrEqual(0)
}
