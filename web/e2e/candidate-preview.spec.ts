import { join } from 'node:path'
import { writeFileSync } from 'node:fs'
import type { Page } from '@playwright/test'
import { expect, stopStrays, task, test, type Api } from './fixtures'
import { endMade, launch, made, pick, previewCrew, previewFrame, previewSeam, scratchDir } from './preview-fixtures'

// A candidate's preview (docs/plans/browser-evidence.md, PR-8, #796): a managed crew of the stub worker whose project manifest serves
// the preview fixture; once its attempt has submitted and its check passed, its session ended and removed, Preview this candidate on
// the task's row makes the checkout and opens the preview page at its seq: the label "candidate … · as committed", a capture there
// naming the candidate, Capture again of it (the new capture's retakeOf the first's id), Close and the label gone, the commands' receipts on the run page, and a stopped run ending a checkout. Every
// wait is on what the page draws or what the server answers.
test.describe.configure({ mode: 'serial', timeout: 300_000 })

const m = made()
let runId = ''
let commit = ''

interface Label {
  url: string
  tree: string
  seq?: number
  commit: string
  candidate?: string
}

/** The label of the checkout seq: its answer, or the status of the refusal. */
async function label(api: Api, seq: number): Promise<Label | number> {
  const r = await api.call<Label>('GET', `/api/runs/${encodeURIComponent(runId)}/tasks/build/attempts/1/preview?seq=${seq}`)
  return r.status === 200 ? r.body : r.status
}

test.beforeAll(async ({ api }) => {
  await stopStrays(api, [])
  const gate = join(scratchDir(m, 'candidate-preview'), 'gate')
  writeFileSync(gate, '')
  runId = await launch(api, m, await previewCrew(api, m, 'e2e candidate preview', gate))
  // The attempt submits and its check passes at once: the task succeeds with its result.
  await expect.poll(async () => task(await api.run(runId), 'build').state, { timeout: 120_000, message: 'build succeeded' }).toBe('succeeded')
  const t = task(await api.run(runId), 'build')
  commit = t.result!.commit!
  expect(commit).toMatch(/^[0-9a-f]{40}$/)
  // Its session ended and removed (a second stop removes an ended one): the preview needs the run and the ledger alone.
  const session = t.attempt?.sessionId
  if (session) {
    // A stop ends a running session and removes an ended one: asked until the server no longer has it.
    await expect
      .poll(
        async () => {
          await api.stopSession(session)
          return (await api.call('GET', `/api/sessions/${encodeURIComponent(session)}`)).status
        },
        { timeout: 60_000, intervals: [500] },
      )
      .toBe(404)
  }
})

test.afterAll(async ({ api }) => {
  await endMade(api, m)
})

async function openTasks(page: Page) {
  await page.goto(`/runs/${encodeURIComponent(runId)}?view=tasks`)
  await expect(page.locator('[data-task-row="build"]')).toHaveAttribute('data-task-state', 'succeeded')
}

test('Preview this candidate opens its checkout as committed, a capture there names the candidate, and Close ends it', async ({ page, api }) => {
  await previewSeam(page)
  await openTasks(page)
  await page.locator('[data-task-menu="build"]').click()
  await page.locator('[data-task-candidate-preview="build"]').click()
  await expect(page).toHaveURL(new RegExp(`/preview\\?run=${runId}&task=build&attempt=1&seq=\\d+$`))
  const seq = Number(new URL(page.url()).searchParams.get('seq'))
  // The checkout is made with the manifest, then offered: the label says it is the candidate as committed.
  await expect(page.locator('[data-preview-label]')).toHaveText(`candidate ${commit.slice(0, 7)} of build · attempt 1 · as committed`, { timeout: 120_000 })
  await expect(page.locator('[data-preview-pane]')).toHaveAttribute('data-preview-bridge', 'ready')
  const l = await label(api, seq)
  expect(l).toMatchObject({ tree: 'preview', seq, commit, candidate: `${runId}/build/1@${commit}` })
  const frame = await previewFrame(page, new URL((l as Label).url).origin)

  // A capture of the checkout names the candidate it tested.
  await page.locator('[data-preview-capture]').click()
  await pick(page, frame, '#second', 'button#second.second', { downs: 1 })
  await page.locator('[data-capture-editor] [data-capture-save]').click()
  const saved = page.locator('[data-capture-editor] [data-capture-saved]')
  await expect(saved).toBeVisible()
  const c = await api.ok<{ origin: { kind: string; tree: string; candidate?: string; commit: string } }>('GET', `/api/captures/${await saved.getAttribute('data-capture-saved')}`)
  expect(c.origin).toMatchObject({ kind: 'candidate', tree: 'preview', commit, candidate: `${runId}/build/1@${commit}` })

  // Capture again on it: the same page and device mode, its element described again by its selector, a new capture naming it.
  const first = (await saved.getAttribute('data-capture-saved'))!
  await page.locator('[data-capture-editor] [data-capture-again]').click()
  await expect(page.locator('[data-capture-editor] [data-capture-draft]')).toBeVisible()
  await expect(page.locator('[data-capture-editor] [data-annotation="1"] [data-capture-element-summary]')).toContainText('button#second')
  await page.locator('[data-capture-editor] [data-capture-save]').click()
  await expect(page.locator(`[data-capture-editor] [data-capture-saved]:not([data-capture-saved="${first}"])`)).toBeVisible()
  const second = (await page.locator('[data-capture-editor] [data-capture-saved]').getAttribute('data-capture-saved'))!
  const r = await api.ok<{ retakeOf?: string; annotations: Array<{ element?: { id?: string } }> }>('GET', `/api/captures/${second}`)
  expect([r.retakeOf, r.annotations.map((a) => a.element?.id)]).toEqual([first, ['second']])

  // Close ends it: back on the run page, its label is no more, and the row offers Preview this candidate again.
  await page.locator('[data-preview-close]').click()
  await expect(page).toHaveURL(new RegExp(`/runs/${runId}`))
  await expect.poll(() => label(api, seq), { timeout: 60_000 }).toBe(404)
  await page.locator('[data-task-menu="build"]').click()
  await expect(page.locator('[data-task-candidate-preview="build"]')).toBeVisible()
  await page.keyboard.press('Escape')
  // The commands' receipts: the run's log names the preview's open and close.
  const run = await api.run(runId)
  expect(run.log.some((e) => /preview/.test(`${e.type} ${e.message ?? ''}`))).toBe(true)
})

test("a run stopped ends its candidate's checkout", async ({ page, api }) => {
  await openTasks(page)
  await page.locator('[data-task-menu="build"]').click()
  await page.locator('[data-task-candidate-preview="build"]').click()
  await expect(page).toHaveURL(/&seq=\d+$/)
  const seq = Number(new URL(page.url()).searchParams.get('seq'))
  await expect.poll(async () => typeof (await label(api, seq)), { timeout: 120_000 }).toBe('object')
  await api.stopRun(runId)
  await expect.poll(() => label(api, seq), { timeout: 60_000 }).toBe(404)
  await expect(page.locator('[data-preview-missing]')).toBeVisible({ timeout: 30_000 })
})
