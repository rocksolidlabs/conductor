import { join } from 'node:path'
import { writeFileSync } from 'node:fs'
import type { Page } from '@playwright/test'
import { expect, stopStrays, task, test, type Api } from './fixtures'
import { freeProjectKey, makeProject, makeTicket, opOf } from './dispatch-helpers'
import { endMade, launch, made, pick, previewCrew, previewFrame, previewSeam, scratchDir } from './preview-fixtures'

// A capture sent to a review (docs/plans/browser-evidence.md, PR-10, #798): a managed run of the stub worker linked to a ticket, its
// candidate previewed and captured; Add to a review puts the capture's annotation in the person's review draft of that candidate, sent
// as a comment whose finding the server words from the annotation; Request a change sends the next capture as a review requesting
// changes, which Yardmaster applies as a repair: the run's repair attempt starts, and the ticket's Review shows the capture finding with
// its capture named. A capture of the attempt's worktree is offered neither. Every wait is on what the page draws or the server answers.
test.describe.configure({ mode: 'serial', timeout: 420_000 })

const m = made()
let runId = ''
let commit = ''
let ticket = ''

interface Finding {
  kind: string
  intent: string
  message: string
  path?: string
  capture?: { capture: string; send: string; k: number }
}

test.beforeAll(async ({ api }) => {
  await stopStrays(api, [])
  const project = await freeProjectKey(api, 'CRV')
  await makeProject(api, project)
  ticket = await makeTicket(api, project, '1', { title: 'Save is covered' })
  const gate = join(scratchDir(m, 'capture-review'), 'gate')
  writeFileSync(gate, '')
  runId = await launch(api, m, await previewCrew(api, m, 'e2e capture review', gate))
  await expect.poll(async () => task(await api.run(runId), 'build').state, { timeout: 120_000, message: 'build succeeded' }).toBe('succeeded')
  commit = task(await api.run(runId), 'build').result!.commit!
  // The ticket holds the run: a review of its candidate is the ticket's.
  await api.ok('POST', `/api/dispatch/tickets/${ticket}/links`, { opId: opOf(project, 'run'), kind: 'run', ref: runId })
})

test.afterAll(async ({ api }) => {
  await endMade(api, m)
})

const candidate = () => `${runId}/build/1@${commit}`

/** A capture of the candidate's preview with one element annotated with `note`, saved; its id. */
async function captureCandidate(page: Page, api: Api, note: string): Promise<string> {
  await previewSeam(page)
  await page.goto(`/runs/${encodeURIComponent(runId)}?view=tasks`)
  await expect(page.locator('[data-task-row="build"]')).toHaveAttribute('data-task-state', 'succeeded')
  await page.locator('[data-task-menu="build"]').click()
  await page.locator('[data-task-candidate-preview="build"]').click()
  await expect(page).toHaveURL(/&seq=\d+$/)
  const seq = Number(new URL(page.url()).searchParams.get('seq'))
  await expect(page.locator('[data-preview-pane]')).toHaveAttribute('data-preview-bridge', 'ready', { timeout: 120_000 })
  const l = await api.ok<{ url: string }>('GET', `/api/runs/${encodeURIComponent(runId)}/tasks/build/attempts/1/preview?seq=${seq}`)
  const frame = await previewFrame(page, new URL(l.url).origin)
  await page.locator('[data-preview-capture]').click()
  await pick(page, frame, '#second', 'button#second.second', { downs: 1 })
  const editor = page.locator('[data-capture-editor]')
  await editor.locator('[data-annotation="1"] textarea').fill(note)
  await expect(editor.locator('[data-annotation="1"] textarea')).toHaveValue(note)
  await editor.locator('[data-capture-save]').click()
  const saved = editor.locator('[data-capture-saved]')
  await expect(saved).toBeVisible()
  return (await saved.getAttribute('data-capture-saved'))!
}

/** The ticket's findings on the candidate, as the server keeps them. */
async function findings(api: Api): Promise<Finding[]> {
  return (await api.ok<{ findings: Finding[] }>('GET', `/api/dispatch/tickets/${ticket}/findings?state=all`)).findings
}

test('Add to a review puts the annotation in the draft, sent as a finding the server words', async ({ page, api }) => {
  const id = await captureCandidate(page, api, 'The label is cut off')
  const send = page.locator('[data-capture-editor] [data-capture-send]')
  await send.locator('[data-send-kind="review-draft"]').click()
  const form = send.locator('[data-capture-send-review][data-send-kind="review-draft"]')
  await expect(form.locator('[data-capture-review-annotation="1"]')).toContainText('The label is cut off')
  await form.locator('[data-capture-review-send]').click()
  await expect(form.locator('[data-capture-review-result]')).toHaveAttribute('data-send-state', 'sent')
  await form.locator('[data-capture-review-open]').click()

  // The ticket's Review of the candidate: the draft holds the capture note; sent as a comment, it is a finding of kind capture.
  await expect(page).toHaveURL(/view=review/)
  const draft = page.locator('[data-review-draft]')
  await expect(draft.locator('[data-review-draft-note][data-review-note-kind="capture"]')).toHaveCount(1)
  await expect(draft.locator('[data-review-draft-message]')).toHaveText('The label is cut off')
  await draft.locator('[data-review-verdict]').getByRole('radio', { name: 'Comment' }).click()
  await expect(draft.locator('[data-review-verdict]').getByRole('radio', { name: 'Comment' })).toBeChecked()
  await draft.locator('[data-review-send]').click()
  await expect(draft.locator('[data-review-draft-note]')).toHaveCount(0, { timeout: 60_000 })
  await expect.poll(async () => (await findings(api)).filter((f) => f.capture?.capture === id).length, { timeout: 30_000 }).toBe(1)
  const f = (await findings(api)).find((x) => x.capture?.capture === id)!
  expect(f.kind).toBe('capture')
  expect(f.message).toMatch(/^The label is cut off\n\nElement button/)
  await expect(page.locator(`[data-review-finding][data-finding-capture="${id}"] [data-finding-capture-ref]`)).toContainText(id)
})

test('Request a change sends the capture as a review requesting changes, and Yardmaster repairs the task', async ({ page, api }) => {
  const id = await captureCandidate(page, api, 'The save button is covered')
  const send = page.locator('[data-capture-editor] [data-capture-send]')
  await send.locator('[data-send-kind="review"]').click()
  const form = send.locator('[data-capture-send-review][data-send-kind="review"]')
  await form.locator('[data-capture-review-send]').click()
  await expect(form.locator('[data-capture-review-result]')).toHaveAttribute('data-send-state', 'sent', { timeout: 60_000 })
  await expect(form.locator('[data-capture-review-result]')).toContainText('Changes requested')

  // The repair: the task's attempt 2 starts at the candidate, with the capture's files handed to it.
  await expect.poll(async () => task(await api.run(runId), 'build').attempt?.n ?? 0, { timeout: 120_000, message: 'the repair attempt' }).toBe(2)
  const f = (await findings(api)).find((x) => x.capture?.capture === id)
  expect(f).toMatchObject({ kind: 'capture', intent: 'change' })
  expect(f!.message).toMatch(/^The save button is covered/)

  await page.goto(`/dispatch/${ticket.replace('-', '/')}?view=review&candidate=${encodeURIComponent(candidate())}`)
  await expect(page.locator(`[data-review-finding][data-finding-capture="${id}"]`)).toBeVisible({ timeout: 30_000 })
})
