import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Frame, Locator, Page } from '@playwright/test'
import { expect, stopStrays, task, test, type Api } from './fixtures'
import { PREVIEW_BOOT_MS, endMade, launch, made, pick, previewCrew, previewFrame, previewSeam, scratchDir, stepIn } from './preview-fixtures'

// A capture's sends (docs/plans/browser-evidence.md, PR-6, #794), from the capture editor's Send against a managed crew of the stub
// worker whose manifest serves the preview fixture: Create a ticket, its two attachments and its description; an edit, "edited
// since" on the first send, and r2 sent beside r1 to the same ticket by Attach to a ticket; the ticket's Captures showing both as
// they were sent; a capture saved with no screenshot sent as the document alone; Ask Brakeman, its stub's turn reading the document
// the send froze and citing it. Every wait is on what the page draws or what the server answers. The run's own ticket offered first
// is capture-repair.spec.ts's (a run launched from a ticket's plan).
test.describe.configure({ mode: 'serial', timeout: 300_000 })

const m = made()
let runId = ''
let origin = ''
let gate = ''
let project = ''
/** Brakeman's configuration as the spec found it, saved back after it. */
let brakemanWas: (Record<string, unknown> & { rev: number }) | null = null

interface Attachment {
  name: string
  sha256: string
  deletedAt?: string
}

test.beforeAll(async ({ api }) => {
  await stopStrays(api, [])
  gate = join(scratchDir(m, 'capture-send'), 'gate')
  runId = await launch(api, m, await previewCrew(api, m, 'e2e capture send', gate))
  await stepIn(api, runId, 1, 'check:gate:1', 'running')
  origin = new URL(task(await api.run(runId), 'build').attempt!.preview!).origin
  project = `CS${Date.now().toString(36).slice(-4).toUpperCase()}`.replace(/[^A-Z0-9]/g, 'X')
  await api.ok('POST', '/api/dispatch/projects', { opId: `cs-${project}`, key: project, name: 'Capture sends' })
})

test.afterAll(async ({ api }) => {
  if (gate) writeFileSync(gate, '')
  if (brakemanWas) {
    const { config } = await api.ok<{ config: { rev: number } }>('GET', '/api/brakeman')
    await api.ok('PUT', '/api/brakeman/config', { rev: config.rev, config: { ...brakemanWas, rev: config.rev } })
  }
  await endMade(api, m)
})

/** The preview page with its frame; screenshots through the seam, or none when `shots` is false. */
async function previewPage(page: Page, shots = true) {
  if (shots) await previewSeam(page)
  else {
    // A seam whose screenshot is none: the capture is saved without one.
    await page.addInitScript(() => {
      if (window.top === window) (window as unknown as { __conductorPreviewSeam: unknown }).__conductorPreviewSeam = { frame: async () => '' }
    })
  }
  await page.goto(`/preview?run=${encodeURIComponent(runId)}&task=build&attempt=1`)
  await expect(page.locator('[data-preview-pane]')).toHaveAttribute('data-preview-bridge', 'ready', { timeout: PREVIEW_BOOT_MS })
  return previewFrame(page, origin)
}

/** Captures the button under the overlay with a note and saves it: the capture's id, its Send drawn. */
async function captured(page: Page, note: string, shots = true): Promise<string> {
  return capturedIn(page, await previewPage(page, shots), note)
}

/** Another capture in the preview page already open, saved: its id. */
async function capturedIn(page: Page, frame: Frame, note: string): Promise<string> {
  const editor = page.locator('[data-capture-editor]')
  const before = await editor.locator('[data-capture-saved-tab]').count()
  await page.locator('[data-preview-capture]').click()
  await pick(page, frame, '#under', 'button#under.under', { downs: 1 })
  await editor.locator('[data-annotation="1"] textarea').fill(note)
  await editor.locator('[data-capture-save]').click()
  await expect(editor.locator('[data-capture-saved-tab]')).toHaveCount(before + 1)
  const saved = editor.locator('[data-capture-saved]')
  await expect(saved).toBeVisible()
  await expect(page.locator('[data-capture-send]')).toBeVisible()
  return (await saved.getAttribute('data-capture-saved'))!
}

/** Chooses the option whose label begins with `label` in the select `field` opens, and waits for its menu to be gone. */
async function choose(page: Page, field: Locator, label: string) {
  await field.click()
  await page.getByRole('option', { name: new RegExp(`^${label}`) }).click()
  await expect(page.getByRole('listbox')).toHaveCount(0)
}

/** The ticket's live attachments. */
async function attachments(api: Api, key: string): Promise<Attachment[]> {
  const t = (await api.ok<{ ticket: { attachments?: Attachment[] } }>('GET', `/api/dispatch/tickets/${key}`)).ticket
  return (t.attachments ?? []).filter((a) => !a.deletedAt)
}

test('Create a ticket with its two attachments; an edit, edited since, and r2 sent beside r1; the ticket’s Captures', async ({ page, api }) => {
  const id = await captured(page, 'The save button is covered')
  const send = page.locator('[data-capture-send]')
  await expect(send.locator('[data-send-kind="ticket-new"]')).toHaveAttribute('data-send-chosen', '')
  await choose(page, send.locator('[data-send-project]'), `${project} ·`)
  await expect(send.locator('[data-send-title]')).toHaveValue('The save button is covered')
  await send.locator('[data-capture-send-go]').click()
  const first = send.locator('[data-send-row]').first()
  await expect(first).toHaveAttribute('data-send-state', 'sent')
  const key = (await first.getAttribute('data-send-target'))!
  expect(key).toMatch(new RegExp(`^${project}-\\d+$`))
  await expect(first).toContainText(`sent as r1 to ${key}`)
  const files = await attachments(api, key)
  expect(files.map((a) => a.name).sort()).toEqual([`capture-${id}-r1.json`, `capture-${id}-r1.png`])
  const desc = (await api.ok<{ ticket: { description: string } }>('GET', `/api/dispatch/tickets/${key}`)).ticket.description
  expect(desc).toContain(`Capture ${id}, revision 1`)
  expect(desc).toContain('> The save button is covered')

  // An edit is a new revision: the first send says what changed since it, and r2 goes beside it.
  const editor = page.locator('[data-capture-editor]')
  await editor.locator('[data-annotation="1"] textarea').fill('The save button is covered by the banner')
  await editor.locator('[data-capture-save-changes]').click()
  await expect(send).toHaveAttribute('data-capture-send-rev', '2')
  await expect(send.locator(`[data-send-target="${key}"] [data-send-edited]`)).toHaveText("edited since: annotation 1's note")
  await send.locator('[data-send-kind="ticket"]').click()
  await choose(page, send.locator('[data-send-picker-project]'), `${project} ·`)
  await send.locator('[data-ticket-picker]').click()
  await page.keyboard.type(key)
  await page.locator(`[data-ticket-pick="${key}"]`).click()
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await expect(send.locator('[data-send-ticket]')).toContainText(`To ${key}`)
  await send.locator('[data-capture-send-go]').click()
  await expect(send.locator('[data-send-row]')).toHaveCount(2)
  await expect(send.locator('[data-send-row]').first()).toContainText(`sent as r2 to ${key}`)
  expect((await attachments(api, key)).map((a) => a.name).sort()).toEqual(
    [`capture-${id}-r1.json`, `capture-${id}-r1.png`, `capture-${id}-r2.json`, `capture-${id}-r2.png`].sort(),
  )

  // The ticket's Captures: the capture as each send froze it, its screenshot, its note as sent.
  const [p, n] = key.split('-')
  await page.goto(`/dispatch/${p}/${n}`)
  const here = page.locator(`[data-ticket-capture="${id}"]`)
  await expect(here.locator('[data-ticket-capture-send]')).toHaveCount(2)
  await expect(here.locator('[data-send-rev="1"] [data-ticket-capture-annotation="1"]')).toContainText('The save button is covered')
  await expect(here.locator('[data-send-rev="2"] [data-ticket-capture-annotation="1"]')).toContainText('by the banner')
  await expect(here.locator('[data-send-rev="1"] [data-ticket-capture-shot]')).toBeVisible()
  await here.locator('[data-ticket-capture-open]').click()
  await expect(here.locator('[data-ticket-capture-now]')).toHaveText('Now r2: nothing since')
})

test('each saved capture has a Send of its own, and changes not saved hold it', async ({ page }) => {
  // Two captures saved in one editor: each one's Send lists its own sends and takes its own note as the new ticket's title.
  const first = await captured(page, 'The first capture')
  const send = page.locator('[data-capture-send]')
  await choose(page, send.locator('[data-send-project]'), `${project} ·`)
  await send.locator('[data-capture-send-go]').click()
  await expect(send.locator('[data-send-row]').first()).toHaveAttribute('data-send-state', 'sent')
  const second = await capturedIn(page, await previewFrame(page, origin), 'The second capture')
  expect(second).not.toBe(first)
  await expect(send.locator('[data-send-title]')).toHaveValue('The second capture')
  await expect(send.locator('[data-send-row]')).toHaveCount(0)
  await page.locator(`[data-capture-saved-tab="${first}"]`).click()
  await expect(page.locator('[data-capture-saved]')).toHaveAttribute('data-capture-saved', first)
  await expect(send.locator('[data-send-row]')).toHaveCount(1)
  await expect(send.locator('[data-send-title]')).toHaveValue('The first capture')

  // A note changed and not saved: Send waits for the save, and says so; once saved, it sends the new revision.
  await page.locator(`[data-capture-saved-tab="${second}"]`).click()
  await expect(page.locator('[data-capture-saved]')).toHaveAttribute('data-capture-saved', second)
  const editor = page.locator('[data-capture-editor]')
  await editor.locator('[data-annotation="1"] textarea').fill('The second capture, edited')
  await expect(send.locator('[data-capture-send-go]')).toBeDisabled()
  await expect(send.locator('[data-send-problem]')).toHaveText('Save your changes first: Send sends the saved r1')
  await editor.locator('[data-capture-save-changes]').click()
  await expect(send).toHaveAttribute('data-capture-send-rev', '2')
  await expect(send.locator('[data-send-problem]')).toHaveCount(0)
  await expect(send.locator('[data-capture-send-go]')).toBeEnabled()
})

test('a capture saved with no screenshot is sent as its document alone', async ({ page, api }) => {
  const id = await captured(page, 'No pixels here', false)
  const capture = await api.ok<{ image: { how: string } }>('GET', `/api/captures/${id}`)
  expect(capture.image.how).toBe('none')
  const send = page.locator('[data-capture-send]')
  await choose(page, send.locator('[data-send-project]'), `${project} ·`)
  await send.locator('[data-capture-send-go]').click()
  const row = send.locator('[data-send-row]').first()
  await expect(row).toHaveAttribute('data-send-state', 'sent')
  const key = (await row.getAttribute('data-send-target'))!
  expect((await attachments(api, key)).map((a) => a.name)).toEqual([`capture-${id}-r1.json`])
  const [p, n] = key.split('-')
  await page.goto(`/dispatch/${p}/${n}`)
  await expect(page.locator(`[data-ticket-capture="${id}"] [data-ticket-capture-noshot]`)).toBeVisible()
})

test('Ask Brakeman: the stub’s turn reads the document the send froze and cites it', async ({ page, api }) => {
  const { config } = await api.ok<{ config: Record<string, unknown> & { rev: number } }>('GET', '/api/brakeman')
  brakemanWas = config
  await api.ok('PUT', '/api/brakeman/config', { rev: config.rev, config: { ...config, enabled: true, agentId: 'brakeman-stub' } })
  const id = await captured(page, 'Why is this covered?')
  const send = page.locator('[data-capture-send]')
  await send.locator('[data-send-kind="brakeman"]').click()
  await send.locator('[data-send-question] textarea, textarea[data-send-question]').first().fill('What covers the save button?')
  await send.locator('[data-capture-send-go]').click()
  const row = send.locator('[data-send-row]').first()
  await expect(row).toHaveAttribute('data-send-state', 'sent')
  const br = (await row.getAttribute('data-send-target'))!
  expect(br).toMatch(/^br-\d+$/)
  const sendId = (await row.getAttribute('data-send-row'))!
  const req = await api.ok<{ request: { note: string; subjects: Array<{ ref: string } | string> } }>('GET', `/api/brakeman/requests/${br}`)
  expect(req.request.note).toBe('What covers the save button?')
  expect(JSON.stringify(req.request.subjects)).toContain(`capture:${id}/${sendId}`)
  await expect
    .poll(async () => (await api.ok<{ request: { state: string } }>('GET', `/api/brakeman/requests/${br}`)).request.state, { timeout: 120_000, message: 'the turn briefed' })
    .toBe('briefed')
  const refs = await api.ok<{ refs: Array<{ ref: string; seen: { state: string } }> }>('GET', `/api/brakeman/requests/${br}/refs`)
  const cited = refs.refs.find((r) => r.ref === `capture:${id}/${sendId}`)
  expect(cited, 'the brief cites the send').toBeTruthy()
  expect(cited!.seen.state).toBe('sent')

  // Cancelled after its turn: the page says what the cancel did, the row that one was asked, and Cancel is no longer offered.
  await row.locator('[data-send-cancel]').click()
  await expect(send.locator('[data-send-error]')).toHaveText('The request had finished before the cancel reached it.')
  await expect(row).toContainText(`sent as r1 to Brakeman (${br}) · cancel asked`)
  await expect(row.locator('[data-send-cancel]')).toHaveCount(0)
})
