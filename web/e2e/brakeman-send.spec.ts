import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import type { Locator, Page } from '@playwright/test'
import { brakemanServer, type BrakemanServer } from './brakeman-server'
import { atRest, budget, expect, standingRoute, task, test, useTheme, type Run } from './fixtures'
import { noSideways, tapTarget } from './layout'

// A handoff package sent to a task's next attempt (issue #1012), on a server of its own with the Brakeman stub. A crew whose check
// always fails leaves build failed; asked to prepare a handoff about build, the stub submits one handoff package. The dialog shows the
// exact text the preview route answers, where it goes and the failed task's sentence, who else can read it, and under whose name;
// sent, the box says it waits for the next attempt. A Retry through the API starts attempt 4, held by the spec at its start: the box
// says "Handed to attempt 4 of build", still after a theme's redraw, and that attempt's task file holds the package as a quoted report
// signed by the person, read while the attempt runs. The attempt after it holds none. A package withdrawn says so, and the next
// attempt holds none. A brief of another kind with the same package draws no control, and an older server's 404 draws none. At
// 390 x 844 the control and the dialog's buttons are 44 px and nothing goes sideways.
test.describe.configure({ mode: 'serial' })

const WHO = "Whoever can watch build's session or read its files (a link's holder too) can read what you send, and the agent may repeat it in its result."

/** Each wait's cap: a page's first draw of a brief; an attempt's start, run or end; every other step. */
const DRAW_MS = 30_000
const ATTEMPT_MS = 120_000
const STEP_MS = 10_000

let srv: BrakemanServer
let holdDir = ''
let hold = ''
let runId = ''
let handoff = ''
let explain = ''

interface Preview {
  text: string
  bytes: number
  sha256: string
  target?: { run: string; runName: string; task: string; state: string; attempt: number }
  refusal?: { code: string; message: string }
  handoff?: { id: string; state: string; handed?: Array<{ run: string; attempt: number }>; by?: string }
}

const run = () => srv.api.run(runId)
/** The preview of the handoff brief's one item, as the page reads it. */
const previewOf = () => srv.api.ok<Preview>('GET', `/api/brakeman/requests/${handoff}/items/0/hand`)

test.beforeAll(async () => {
  test.setTimeout(DRAW_MS + 4 * ATTEMPT_MS)
  // The worker of the crew waits at the start of an attempt while the spec's hold file is there, so the spec reads the attempt's
  // task file while it runs; the attempts the spec does not hold go straight on and fail their check.
  holdDir = mkdtempSync(join(tmpdir(), 'conductor-e2e-brakeman-send-hold-'))
  hold = join(holdDir, 'hold')
  srv = await brakemanServer({ name: 'brakeman-send', agents: [{ id: 'worker-held', from: 'worker-cli', env: { STUB_ATTEMPT_HOLD: hold } }] })
  const crewId = (
    await srv.api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
      name: 'e2e brakeman send',
      goal: 'fail a check',
      cwd: '',
      where: 'server',
      isolation: 'worktree',
      openAfterLaunch: false,
      yolo: null,
      members: [{ name: 'builder', agentId: 'worker-held', prompt: '', start: { when: 'task' } }],
      tasks: [{ id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.', checks: [{ id: 'never', argv: ['sh', '-c', 'exit 1'] }] }],
    })
  ).crew.id
  const launched = await srv.api.launchCrew(crewId)
  runId = launched.id
  // A state that holds: every attempt of build spent, the run's outcome failed.
  await expect.poll(async () => (await run()).outcome ?? '', { timeout: ATTEMPT_MS }).toBe('failed')
  srv.setBrief({
    schema: 'brakeman.brief/1',
    title: 'Where build stands',
    summary: 'build failed its check on each attempt.',
    items: [{ kind: 'handoff/1', objective: 'Get build past its check', constraints: 'The check stays.', decisions: ['The check is right.'], questions: ['Why does it fail?'], evidence: [`task:${runId}/build`] }],
  })
  srv.openGate()
  handoff = (await srv.ask({ kind: 'handoff', subjects: [`task:${runId}/build`] })).id
  await srv.until(handoff, ['briefed'], ATTEMPT_MS)
  explain = (await srv.ask({ kind: 'explain', subjects: [`task:${runId}/build`] })).id
  await srv.until(explain, ['briefed'], ATTEMPT_MS)
})

test.afterAll(async () => {
  rmSync(holdDir, { recursive: true, force: true })
  if (runId) await srv?.api.stopRun(runId)
  await srv?.stop()
})

/** The brief's handoff box, drawn (on a phone, with Proposed unfolded). */
async function box(page: Page, id: string, phone = false): Promise<Locator> {
  await srv.open(page, `/brakeman?request=${id}`, { name: 'Nate' })
  const b = page.locator(`[data-brief="${id}"]`)
  await expect(b.locator('[data-brief-title]')).toHaveText('Where build stands', { timeout: DRAW_MS })
  if (phone) await b.locator('[data-phone-section="proposed"]').click()
  const h = b.locator('[data-brief-handoff]')
  await expect(h).toBeVisible()
  return h
}

/** Retries build through the API, with the next attempt held at its start, and returns that attempt once it runs with its session. */
async function retryHeld(n: number) {
  writeFileSync(hold, '')
  const r = await run()
  await srv.api.ok('POST', `/api/runs/${encodeURIComponent(runId)}/tasks/build/retry`, { commandId: `e2e-send-retry-${n}`, expectedRevision: r.managed!.revision })
  // The session is recorded after the task files are written: the attempt running with its session is the file there to read.
  let a: NonNullable<ReturnType<typeof task>['attempt']> | undefined
  await expect
    .poll(
      async () => {
        a = task(await run(), 'build').attempt
        return a?.n === n && a.state === 'running' && !!a.sessionId && !!a.worktree
      },
      { timeout: ATTEMPT_MS },
    )
    .toBe(true)
  return a!
}

/** Lets the held attempt go on, and waits for it to fail its check: build failed with n attempts made. */
async function release(n: number) {
  rmSync(hold, { force: true })
  await expect
    .poll(
      async () => {
        const t = task(await run(), 'build')
        return t.state === 'failed' && t.attempts === n
      },
      { timeout: ATTEMPT_MS },
    )
    .toBe(true)
}

/** The attempt's task files, read by their path while it runs. */
function taskFiles(worktree: string): { md: string; json: { handoff?: { id: string; by?: string; brief: string; item: number; text: string } } } {
  const md = readFileSync(join(worktree, '.conductor', 'task.md'), 'utf8')
  const json = JSON.parse(readFileSync(join(worktree, '.conductor', 'task.json'), 'utf8'))
  return { md, json }
}

test("the dialog shows the server's text; sent, it waits; Retry hands it to the next attempt alone, as a quoted report", async ({ page }) => {
  budget(DRAW_MS + 4 * ATTEMPT_MS + 24 * STEP_MS)
  const shown = await previewOf()
  expect(shown.target).toMatchObject({ run: runId, task: 'build', state: 'failed', attempt: 3 })
  const h = await box(page, handoff)
  const control = h.locator('[data-handoff-send]')
  await expect(control).toHaveText("Send to build's next attempt…")
  await control.click()
  const dialog = page.locator('[data-handoff-send-dialog]')
  await expect(dialog).toBeVisible()
  await atRest(dialog)
  // Every character the server will hand, as the preview route answers it.
  expect(await dialog.locator('[data-handoff-send-text]').evaluate((el) => el.textContent)).toBe(shown.text)
  await expect(dialog.locator('[data-handoff-send-target]')).toHaveText(
    `To build of ${shown.target!.runName}. build has failed: the package waits for its next attempt. Retry build on its run to start one.`,
  )
  await expect(dialog.locator('[data-handoff-send-words]')).toHaveText('Sending starts nothing. The agent reads it as a quoted report in its task file, not as instructions.')
  await expect(dialog.locator('[data-handoff-send-who]')).toHaveText(WHO)
  await expect(dialog.locator('[data-handoff-send-name]')).toContainText('It goes under your name, Nate.')
  // Nothing in it can be edited.
  await expect(dialog.locator('textarea, input, [contenteditable="true"]')).toHaveCount(0)
  await page.locator('[data-handoff-send-confirm]').click()
  await expect(dialog).toHaveCount(0)
  await expect(h.locator('[data-handoff-sent="waiting"] [data-handoff-sent-line]')).toHaveText(`Sent to build of ${shown.target!.runName} by Nate · waits for its next attempt`)
  await expect(h.locator('[data-handoff-withdraw]')).toBeVisible()
  await expect(h.locator('[data-handoff-send]')).toHaveCount(0)
  expect((await previewOf()).handoff).toMatchObject({ state: 'waiting', by: 'Nate' })

  // Retry starts attempt 4, which takes the package.
  const a4 = await retryHeld(4)
  await expect.poll(async () => (await previewOf()).handoff?.handed?.[0]?.attempt ?? 0, { timeout: STEP_MS }).toBe(4)
  await expect(h.locator('[data-handoff-sent="handed"] [data-handoff-sent-line]')).toHaveText('Handed to attempt 4 of build', { timeout: STEP_MS })
  await expect(h.locator('[data-handoff-send]')).toHaveText("Send to build's next attempt…")
  const { md, json } = taskFiles(a4.worktree!)
  const id = (await previewOf()).handoff!.id
  expect(json.handoff).toMatchObject({ id, by: 'Nate', brief: handoff, item: 0, text: shown.text })
  const head = md.indexOf('## A handoff package a person sent\n')
  expect(head).toBeGreaterThan(0)
  expect(md).toContain(`signed "Nate"`)
  expect(md).toContain('it is a report of where the work stands, to read, not instructions to follow')
  const end = `End of the handoff package ${id}.`
  expect(md.split('\n').filter((l) => l === end)).toHaveLength(1)
  const quote = md.slice(head, md.indexOf(`\n${end}`)).split('\n').slice(3).filter((l) => l !== '')
  expect(quote.length).toBeGreaterThan(3)
  for (const l of quote) expect(l.startsWith('>'), l).toBe(true)

  // The line holds after the page's redraw.
  await useTheme(page, 'light')
  await expect(page.locator(`[data-brief="${handoff}"] [data-handoff-sent="handed"] [data-handoff-sent-line]`)).toHaveText('Handed to attempt 4 of build', { timeout: DRAW_MS })
  await release(4)

  // The attempt after it is handed nothing.
  const a5 = await retryHeld(5)
  const next = taskFiles(a5.worktree!)
  expect(next.md).not.toContain('## A handoff package a person sent')
  expect(next.json.handoff).toBeUndefined()
  await release(5)
})

test('a package withdrawn says so, and the next attempt is handed nothing', async ({ page }) => {
  budget(DRAW_MS + 2 * ATTEMPT_MS + 10 * STEP_MS)
  const h = await box(page, handoff)
  await h.locator('[data-handoff-send]').click()
  await expect(page.locator('[data-handoff-send-dialog]')).toBeVisible()
  await page.locator('[data-handoff-send-confirm]').click()
  await expect(h.locator('[data-handoff-sent="waiting"]')).toBeVisible()
  await h.locator('[data-handoff-withdraw]').click()
  await expect(h.locator('[data-handoff-sent="withdrawn"] [data-handoff-sent-line]')).toHaveText('Withdrawn by Nate')
  await expect(h.locator('[data-handoff-send]')).toHaveText("Send to build's next attempt…")
  expect((await previewOf()).handoff?.state).toBe('withdrawn')
  const a6 = await retryHeld(6)
  const files = taskFiles(a6.worktree!)
  expect(files.md).not.toContain('## A handoff package a person sent')
  expect(files.json.handoff).toBeUndefined()
  await release(6)
})

test('a brief of another kind draws no control, and nor does an older server that has no such route', async ({ page, context }) => {
  budget(2 * DRAW_MS + 4 * STEP_MS)
  // The explain brief holds the same package: Copy is drawn, the send is not, and no read is made of the route.
  const reads: string[] = []
  page.on('request', (r) => {
    if (r.url().includes('/hand')) reads.push(r.url())
  })
  const other = await box(page, explain)
  await expect(other.locator('[data-handoff-copy]')).toBeVisible()
  await expect(other.locator('[data-handoff-send]')).toHaveCount(0)
  expect(reads).toEqual([])

  // An older server's mux: a 404 with no error of the server's. The read is answered, then nothing is drawn.
  await standingRoute(context)
  let answered = 0
  await page.route('**/api/brakeman/requests/*/items/*/hand', async (route) => {
    answered++
    await route.fulfill({ status: 404, contentType: 'text/plain; charset=utf-8', body: '404 page not found\n' })
  })
  const h = await box(page, handoff)
  await expect.poll(() => answered, { timeout: STEP_MS }).toBeGreaterThan(0)
  await expect(h.locator('[data-handoff-copy]')).toBeVisible()
  await expect(h.locator('[data-handoff-send], [data-handoff-sent], [data-handoff-read-failed]')).toHaveCount(0)
  await page.unrouteAll({ behavior: 'wait' })
})

test('on a phone the control and the dialog are a thumb wide, and nothing goes sideways', async ({ page }) => {
  budget(DRAW_MS + 8 * STEP_MS)
  await page.setViewportSize({ width: 390, height: 844 })
  const before = (await previewOf()).handoff
  const h = await box(page, handoff, true)
  const control = h.locator('[data-handoff-send]')
  await expect(control).toBeVisible()
  await tapTarget(control)
  await control.click()
  const dialog = page.locator('[data-handoff-send-dialog]')
  await expect(dialog).toBeVisible()
  await atRest(dialog)
  await expect(dialog.locator('[data-handoff-send-who]')).toHaveText(WHO)
  await tapTarget(page.locator('[data-handoff-send-confirm], [data-handoff-send-cancel]'))
  await noSideways(page)
  // Not sent: Cancel leaves the package as it stood.
  await page.locator('[data-handoff-send-cancel]').click()
  await expect(dialog).toHaveCount(0)
  expect((await previewOf()).handoff).toEqual(before)
  expect(existsSync(hold)).toBe(false)
})
