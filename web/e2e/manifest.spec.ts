import { createHash } from 'node:crypto'
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import type { Page } from '@playwright/test'
import { expect, stopStrays, task, test, type Api, type Run } from './fixtures'

// A project manifest run end to end (EN1 PR-7, #399), with the stub as the
// worker as in tasks.spec.ts: a setup step that writes under .conductor/, a
// python3 http.server service on the tree's own port with a URL probe and a
// preview, and a cleanup step; the run page's receipts under the attempt,
// each with its log and the SHA-256 the server sent; Open preview on the
// task's row and in the palette's → actions while the attempt runs, opening
// a new tab, and gone from both once the task succeeded, its tree ended
// after with its service's process gone; a prerequisite that fails pauses
// the run with the step named and Resume runs it again; a manifest env name
// the server does not pass pauses it before any command, the manifest's
// section saying why; a task cancelled mid-step gets its tree's cleanup
// receipt; one task's receipts never show, nor ask for their logs, under
// another selected while its read is held or after it failed, and a quiet
// step's empty log is kept with its hash. Each wait is on the state the
// server reports (a receipt's state, the attempt's preview, the run's
// cleaning count), never on the clock; a check that holds the attempt open
// waits for a file the spec writes. Rows are found by their ids.
test.describe.configure({ mode: 'serial' })

interface Step {
  phase: string
  id: string
  k: number
  state: string
  exit?: number
  error?: string
  argv?: string[]
  url?: string
  dir?: string
  manifest?: string
  ports?: Record<string, number>
  logBytes: number
  logSha256?: string
}

const crewIds: string[] = []
const runs: string[] = []
const scratch: string[] = []

/**
 * A directory of the spec's own for the files its steps wait on, outside the server's allowed root: a directory added there is one
 * more entry in its listing, which GET /api/paths cuts at 50 in name order, and settings.spec.ts looks for the repository among them.
 */
function scratchDir(name: string): string {
  const dir = mkdtempSync(join(tmpdir(), `conductor-e2e-${name}-`))
  scratch.push(dir)
  return dir
}

test.beforeAll(async ({ api }) => {
  await stopStrays(api, [])
})

test.afterEach(async ({ api }) => {
  for (const id of runs.splice(0)) await api.stopRun(id)
})

test.afterAll(async ({ api }) => {
  for (const id of crewIds.splice(0)) await api.call('DELETE', `/api/crews/${encodeURIComponent(id)}`)
  for (const dir of scratch.splice(0)) rmSync(dir, { recursive: true, force: true })
})

/** Saves a managed crew with one task, build, done by the stub worker, with the manifest and the checks given. */
async function manifestCrew(api: Api, name: string, manifest: Record<string, unknown>, checks: Array<Record<string, unknown>>): Promise<string> {
  const body = {
    name,
    goal: 'prove the project manifest',
    cwd: '',
    where: 'server',
    isolation: 'worktree',
    openAfterLaunch: false,
    yolo: null,
    members: [{ name: 'builder', agentId: 'worker', prompt: '', start: { when: 'task' } }],
    tasks: [{ id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.', checks }],
    manifest,
  }
  const id = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', body)).crew.id
  crewIds.push(id)
  return id
}

async function launch(api: Api, crewId: string): Promise<string> {
  const run = await api.launchCrew(crewId)
  runs.push(run.id)
  expect(run.managed, 'launched as a managed run').toBeTruthy()
  return run.id
}


/**
 * An attempt's receipts, from the steps route; none while the attempt is not in the ledger yet (its `attempt.reserved` commits after
 * the launch answers, and the route says `404 not_found` until then). Any other refusal fails the test.
 */
async function steps(api: Api, run: string, n: number): Promise<Step[]> {
  const path = `/api/runs/${encodeURIComponent(run)}/tasks/build/attempts/${n}/steps`
  const r = await api.call<{ steps?: Step[]; error?: { code?: string } }>('GET', path)
  if (r.status === 404 && r.body?.error?.code === 'not_found') return []
  if (r.status >= 300) throw new Error(`GET ${path}: ${r.status} ${JSON.stringify(r.body)}`)
  return r.body.steps ?? []
}

/** The receipt of phase:id:k, or undefined before it is put. */
async function step(api: Api, run: string, n: number, key: string): Promise<Step | undefined> {
  return (await steps(api, run, n)).find((s) => `${s.phase}:${s.id}:${s.k}` === key)
}

/** Polls the receipt phase:id:k until its state is `state`, and returns it. */
async function stepIn(api: Api, run: string, n: number, key: string, state: string): Promise<Step> {
  let got: Step | undefined
  await expect
    .poll(async () => {
      got = await step(api, run, n, key)
      return got?.state ?? 'absent'
    }, { timeout: 120_000, intervals: [250], message: `the receipt ${key} is ${state}` })
    .toBe(state)
  return got!
}

/** A process is alive while signal 0 reaches it. */
function alive(pid: number): boolean {
  try {
    process.kill(pid, 0)
    return true
  } catch {
    return false
  }
}

/** The pid a service wrote into its tree's state directory, waited for: a file not yet written is no evidence. */
async function pidIn(file: string): Promise<number> {
  let pid = 0
  await expect
    .poll(() => {
      pid = existsSync(file) ? Number.parseInt(readFileSync(file, 'utf8').trim(), 10) || 0 : 0
      return pid
    }, { timeout: 60_000, message: `${file} holds a pid` })
    .toBeGreaterThan(0)
  return pid
}

async function openPalette(page: Page) {
  const palette = page.locator('[data-palette]')
  await page.keyboard.press('Alt+/')
  if (!(await palette.waitFor({ state: 'visible', timeout: 1500 }).then(() => true, () => false))) await page.keyboard.press('Control+k')
  await expect(palette).toBeVisible()
  return palette
}

/** Opens the palette on the task's row and reveals its → actions; returns the row. */
async function taskRowActions(page: Page, taskId: string) {
  const palette = await openPalette(page)
  await palette.locator('input[data-palette-input]').fill(taskId)
  const row = palette.locator(`[data-palette-row="page:task:${taskId}"]`)
  await expect(row).toBeVisible()
  // Other rows may match the words too: move the focus down until it is on the task's row.
  for (let i = 0; i < 12 && (await row.getAttribute('data-palette-focused')) === null; i++) await page.keyboard.press('ArrowDown')
  await expect(row).toHaveAttribute('data-palette-focused', '')
  await page.keyboard.press('ArrowRight')
  await expect(row.locator('[data-palette-actions]')).toBeVisible()
  return row
}

test('the receipts and their logs, Open preview while the attempt runs and gone once it succeeded, and the tree ended after', async ({ page, api, context }) => {
  const dir = scratchDir('manifest-preview')
  const gate = join(dir, 'gate')
  const crewId = await manifestCrew(
    api,
    'e2e manifest preview',
    {
      ports: ['web'],
      // The log ends inside a character (a byte the proc bound could cut it at): Save must keep the bytes the server hashed.
      setup: [{ id: 'state', argv: ['sh', '-c', 'echo made by setup | tee "$1"; printf "cut \\342\\234"', 'sh', '${CONDUCTOR_STATE_DIR}/setup.txt'] }],
      services: [{ id: 'web', argv: ['sh', '-c', 'echo $$ > "$CONDUCTOR_STATE_DIR/web.pid"; exec python3 -m http.server "$CONDUCTOR_PORT_WEB" --bind 127.0.0.1'] }],
      ready: [{ id: 'up', url: 'http://127.0.0.1:${CONDUCTOR_PORT_WEB}/', intervalSeconds: 1, timeoutSeconds: 60 }],
      preview: 'http://127.0.0.1:${CONDUCTOR_PORT_WEB}/',
      cleanup: [{ id: 'note', argv: ['sh', '-c', 'echo cleaned | tee "$1"', 'sh', '${CONDUCTOR_STATE_DIR}/cleaned.txt'] }],
    },
    // The check holds the attempt open (verifying) until the spec writes the gate.
    [{ id: 'gate', argv: ['sh', '-c', 'until [ -e "$1" ]; do sleep 0.2; done; echo gate open', 'sh', gate], timeoutSeconds: 600 }],
  )
  const runId = await launch(api, crewId)
  // The attempt's check runs: its worktree is ready and answers behind its preview.
  await stepIn(api, runId, 1, 'check:gate:1', 'running')
  let run = await api.run(runId)
  const attempt = task(run, 'build').attempt!
  const preview = attempt.preview ?? ''
  expect(preview).toMatch(/^http:\/\/127\.0\.0\.1:\d+\/$/)
  const worktree = attempt.worktree!
  // The test reads the preview itself: the worktree's service answers.
  const answer = await fetch(preview)
  expect(answer.status).toBe(200)
  expect(await answer.text()).toContain('Directory listing')
  const servicePid = await pidIn(join(worktree, '.conductor', 'state', 'web.pid'))
  const checkoutPid = await pidIn(join(`${worktree}.check`, '.conductor', 'state', 'web.pid'))
  expect(alive(servicePid) && alive(checkoutPid)).toBe(true)
  expect(readFileSync(join(worktree, '.conductor', 'state', 'setup.txt'), 'utf8')).toBe('made by setup\n')

  // The Tasks view: Open preview on the row, a link to a new tab at the preview.
  await page.goto(`/runs/${encodeURIComponent(runId)}?view=tasks`)
  const row = page.locator('[data-task-row="build"]')
  const open = row.locator('[data-task-preview="build"]')
  await expect(open).toBeVisible()
  await expect(open).toHaveAttribute('href', preview)
  await expect(open).toHaveAttribute('target', '_blank')
  const [tab] = await Promise.all([context.waitForEvent('page'), open.click()])
  await tab.waitForLoadState()
  expect(tab.url()).toBe(preview)
  await expect(tab.locator('body')).toContainText('Directory listing')
  await tab.close()
  // And in the palette's → actions for the task's row.
  let palRow = await taskRowActions(page, 'build')
  await expect(palRow.locator('[data-palette-action="open-preview"]')).toBeVisible()
  const [tab2] = await Promise.all([context.waitForEvent('page'), palRow.locator('[data-palette-action="open-preview"]').click()])
  await tab2.waitForLoadState()
  expect(tab2.url()).toBe(preview)
  await tab2.close()
  await page.keyboard.press('Escape')

  // The attempt's receipts, in order, with the manifest that ran them, where and the ports.
  await row.locator('[role="button"]').click()
  const detail = page.locator('[data-task-detail-id="build"] [data-task-attempt-row="1"]')
  const receipts = detail.locator('[data-steps]')
  await expect(receipts.locator('[data-step-row="setup:state:1"]')).toContainText('exited 0 in')
  await expect(receipts.locator('[data-step-row="setup:state:1"] [data-step-where]')).toHaveText('in worktree')
  await expect(receipts.locator('[data-step-row="setup:state:1"] [data-step-manifest]')).toHaveText(`crew:1`)
  await expect(receipts.locator('[data-step-row="service:web:1"]')).toHaveAttribute('data-step-state', 'running')
  await expect(receipts.locator('[data-step-row="service:web:1"] [data-step-ports]')).toContainText('ports web ')
  await expect(receipts.locator('[data-step-row="ready:up:1"] [data-step-words]')).toContainText(/^ready after \d+ tr(y|ies)$/)
  await expect(receipts.locator('[data-step-row="ready:up:1"] [data-step-command]')).toHaveText(`GET ${preview}`)
  // The checkout's steps follow, k counting the attempt's runs of each id.
  await expect(receipts.locator('[data-step-row="setup:state:2"] [data-step-where]')).toHaveText('in checkout')
  await expect(receipts.locator('[data-step-row="check:gate:1"] [data-step-words]')).toContainText('running since')
  const order = await receipts.locator('[data-step-row]').evaluateAll((els) => els.map((e) => e.getAttribute('data-step-row')))
  expect(order.slice(0, 3)).toEqual(['setup:state:1', 'service:web:1', 'ready:up:1'])

  // Log: the step's text and the hash the route's header carried, which the spec reads itself and checks against the body.
  await receipts.locator('[data-step-log="setup:state:1"]').click()
  const modal = page.locator('[data-step-log-modal]')
  await expect(modal.locator('[data-step-log-text]')).toContainText('made by setup')
  const shown = (await modal.locator('[data-step-log-sha]').textContent())?.trim() ?? ''
  const res = await fetch(`${api.server.baseURL}/api/runs/${encodeURIComponent(runId)}/tasks/build/attempts/1/steps/state/1/log`, { headers: { Authorization: `Bearer ${api.server.token}` } })
  expect(res.status).toBe(200)
  const bytes = Buffer.from(await res.arrayBuffer())
  const body = new TextDecoder().decode(bytes)
  expect(bytes.subarray(-6).toString('latin1')).toBe('cut \xe2\x9c')
  const header = res.headers.get('X-Conductor-Log-SHA256') ?? ''
  expect(header).toMatch(/^[0-9a-f]{64}$/)
  expect(createHash('sha256').update(bytes).digest('hex')).toBe(header)
  expect(shown).toBe(header)
  expect(await modal.locator('[data-step-log-text]').textContent()).toBe(body)
  // Save downloads the log's bytes as the server sent them, so the file matches the hash shown.
  const [download] = await Promise.all([page.waitForEvent('download'), page.locator('[data-step-log-save]').click()])
  expect(download.suggestedFilename()).toBe(`${runId}-build-1-setup-state-1.log`)
  const saved = readFileSync(await download.path())
  expect(saved.equals(bytes)).toBe(true)
  expect(createHash('sha256').update(saved).digest('hex')).toBe(shown)
  await page.keyboard.press('Escape')
  await expect(modal).toHaveCount(0)

  // The gate opens: the check passes, the task succeeds, which closes the attempt and takes its preview away.
  writeFileSync(gate, 'open\n')
  await expect(row).toHaveAttribute('data-task-state', 'succeeded', { timeout: 120_000 })
  await expect(row.locator('[data-task-preview]')).toHaveCount(0)
  palRow = await taskRowActions(page, 'build')
  await expect(palRow.locator('[data-palette-action="open-attempt"]')).toBeVisible()
  await expect(palRow.locator('[data-palette-action="open-preview"]')).toHaveCount(0)
  await page.keyboard.press('Escape')
  // The trees end after the close: each tree's cleanup step finishes once its services ended, and the run's cleaning count is
  // back to none once each tree.ended is committed (it counts a tree from its hand-over to that commit).
  await expect
    .poll(async () => {
      run = await api.run(runId)
      const done = (await steps(api, runId, 1)).filter((s) => s.phase === 'cleanup' && s.state === 'finished').length
      return [run.outcome ?? '', done, run.cleaning ?? 0, task(run, 'build').attempt?.preview ?? '']
    }, { timeout: 120_000, intervals: [250] })
    .toEqual(['succeeded', 2, 0, ''])
  expect(alive(servicePid), "the worktree's service is gone").toBe(false)
  expect(alive(checkoutPid), "the checkout's service is gone").toBe(false)
  const after = await steps(api, runId, 1)
  const byKey = new Map(after.map((s) => [`${s.phase}:${s.id}:${s.k}`, s]))
  expect(byKey.get('service:web:1')?.state).toBe('finished')
  // Each tree's cleanup step ran once, after its services ended.
  const cleanups = after.filter((s) => s.phase === 'cleanup' && s.id === 'note')
  expect(cleanups.map((s) => [s.state, s.exit])).toEqual([
    ['finished', 0],
    ['finished', 0],
  ])
  expect(readFileSync(join(worktree, '.conductor', 'state', 'cleaned.txt'), 'utf8')).toBe('cleaned\n')
  // And the page says so under the attempt, its receipts read again with the run.
  await expect(receipts.locator(`[data-step-row="${cleanups[0]!.phase}:${cleanups[0]!.id}:${cleanups[0]!.k}"] [data-step-words]`)).toContainText('exited 0 in')
})

test('a prerequisite that fails pauses the run with the step named, and Resume runs it again and goes on', async ({ page, api }) => {
  const dir = scratchDir('manifest-prereq')
  const needed = join(dir, 'tool')
  const crewId = await manifestCrew(api, 'e2e manifest prerequisite', { prerequisites: [{ id: 'tool', argv: ['test', '-e', needed] }] }, [{ id: 'unit', argv: ['true'] }])
  const runId = await launch(api, crewId)
  let run: Run = await api.run(runId)
  await expect
    .poll(async () => {
      run = await api.run(runId)
      return run.paused === true && (run.pausedWhy ?? '').includes('tool')
    }, { timeout: 60_000, intervals: [250] })
    .toBe(true)
  expect(run.pausedWhy).toMatch(/^the prerequisite tool failed: /)
  expect(task(run, 'build').attempts, 'no attempt is spent').toBe(0)
  await page.goto(`/runs/${encodeURIComponent(runId)}?view=tasks`)
  // The header: the paused badge titled with the words, the words beside it.
  const paused = page.locator('[data-crew-run-header] [data-run-task-badge]', { hasText: 'paused' })
  await expect(paused).toHaveAttribute('title', `Paused: ${run.pausedWhy}`)
  await expect(page.locator('[data-crew-run-header] [data-run-paused-why]')).toHaveText(run.pausedWhy!)
  // The prerequisite's receipt above the rows, its log the test's own command's.
  const manifest = page.locator('[data-run-manifest]')
  await expect(manifest).toContainText("the crew's, crew:1")
  await expect(manifest.locator('[data-step-row="prerequisite:tool:1"] [data-step-words]')).toContainText('exited 1 in')
  await expect(manifest.locator('[data-step-row="prerequisite:tool:1"] [data-step-command]')).toHaveText(`test -e ${needed}`)
  await expect(page.locator('[data-task-row="build"] [data-task-words]')).toHaveText('ready, scheduling is paused')
  // The host fixed, Resume: the prerequisites run again (k 2), pass, and the task runs to its end.
  writeFileSync(needed, '')
  await page.locator('[data-run-pause]').click()
  await expect(manifest.locator('[data-step-row="prerequisite:tool:2"] [data-step-words]')).toContainText('exited 0 in', { timeout: 60_000 })
  await expect(page.locator('[data-task-row="build"]')).toHaveAttribute('data-task-state', 'succeeded', { timeout: 120_000 })
  await expect(page.locator('[data-crew-run-header] [data-run-paused-why]')).toHaveCount(0)
  const pre = (await api.ok<{ steps: Step[] }>('GET', `/api/runs/${encodeURIComponent(runId)}/prerequisites`)).steps
  expect(pre.map((s) => [s.id, s.k, s.state, s.exit, s.manifest])).toEqual([
    ['tool', 1, 'finished', 1, 'crew:1'],
    ['tool', 2, 'finished', 0, 'crew:1'],
  ])
})

test("a manifest env name the server does not pass pauses the run before any command, and the manifest's section says why", async ({ page, api }) => {
  const name = 'EN1_E2E_NOT_PASSED'
  const crewId = await manifestCrew(api, 'e2e manifest env', { env: [name], prerequisites: [{ id: 'tool', argv: ['true'] }] }, [{ id: 'unit', argv: ['true'] }])
  const runId = await launch(api, crewId)
  let run: Run = await api.run(runId)
  await expect
    .poll(async () => {
      run = await api.run(runId)
      return run.paused === true ? (run.pausedWhy ?? '') : ''
    }, { timeout: 60_000, intervals: [250] })
    .toBe(`the prerequisites failed: ${name} is not in the server's envPassthrough`)
  // No command ran: no receipt, no attempt.
  expect((await api.ok<{ steps: Step[] }>('GET', `/api/runs/${encodeURIComponent(runId)}/prerequisites`)).steps).toEqual([])
  expect(task(run, 'build').attempts, 'no attempt is spent').toBe(0)
  await page.goto(`/runs/${encodeURIComponent(runId)}?view=tasks`)
  await expect(page.locator('[data-crew-run-header] [data-run-paused-why]')).toHaveText(run.pausedWhy!)
  // The section says the run's words, never that the prerequisites have not run or that there are none.
  const manifest = page.locator('[data-run-manifest]')
  await expect(manifest.locator('[data-run-prereq-refused]')).toHaveText(run.pausedWhy!)
  await expect(manifest).not.toContainText('have not run yet')
  await expect(manifest).not.toContainText('No prerequisites.')
  await expect(manifest.locator('[data-steps]')).toHaveCount(0)
})

test('a task cancelled mid-step gets its tree cleaned up: the step interrupted, the cleanup receipt after it', async ({ page, api }) => {
  const dir = scratchDir('manifest-cancel')
  const crewId = await manifestCrew(
    api,
    'e2e manifest cancel',
    {
      // The setup step holds until it is ended: the cancel ends it.
      setup: [{ id: 'hold', argv: ['sh', '-c', 'echo holding; exec sleep 600'], timeoutSeconds: 900 }],
      cleanup: [{ id: 'tidy', argv: ['sh', '-c', 'echo tidied | tee "$1"', 'sh', join(dir, 'tidied')] }],
    },
    [{ id: 'unit', argv: ['true'] }],
  )
  const runId = await launch(api, crewId)
  await stepIn(api, runId, 1, 'setup:hold:1', 'running')
  await page.goto(`/runs/${encodeURIComponent(runId)}?view=tasks&task=build`)
  const row = page.locator('[data-task-row="build"]')
  await row.locator('[data-task-cancel="build"]').click()
  await page.locator('[data-task-confirm]').click()
  await expect(row).toHaveAttribute('data-task-state', 'cancelled')
  const tidy = await stepIn(api, runId, 1, 'cleanup:tidy:1', 'finished')
  expect(tidy.exit).toBe(0)
  // The step the cancel cut was concluded before the cleanup ran: endTree waits for the tree to be quiet.
  expect((await step(api, runId, 1, 'setup:hold:1'))?.state).toBe('interrupted')
  expect(readFileSync(join(dir, 'tidied'), 'utf8')).toBe('tidied\n')
  await expect.poll(async () => (await api.run(runId)).cleaning ?? 0, { timeout: 60_000, intervals: [250] }).toBe(0)
  // The page shows the cleanup's receipt under the cancelled attempt, with its log.
  const receipts = page.locator('[data-task-detail-id="build"] [data-task-attempt-row="1"] [data-steps]')
  await expect(receipts.locator('[data-step-row="setup:hold:1"] [data-step-words]')).toHaveText('interrupted')
  await expect(receipts.locator('[data-step-row="cleanup:tidy:1"] [data-step-words]')).toContainText('exited 0 in')
  await receipts.locator('[data-step-log="cleanup:tidy:1"]').click()
  await expect(page.locator('[data-step-log-modal] [data-step-log-text]')).toHaveText('tidied\n')
  await page.keyboard.press('Escape')
})

test("a task's receipts never show under another selected before its read replied or after it failed, and a quiet step's empty log is kept with its hash", async ({ page, api }) => {
  // Two tasks with the same manifest, so their receipts share step ids: a log asked on the wrong task's route would answer.
  const body = {
    name: 'e2e manifest two tasks',
    goal: 'prove the project manifest',
    cwd: '',
    where: 'server',
    isolation: 'worktree',
    openAfterLaunch: false,
    yolo: null,
    members: [
      { name: 'builder', agentId: 'worker', prompt: '', start: { when: 'task' } },
      { name: 'second', agentId: 'worker', prompt: '', start: { when: 'task' } },
    ],
    tasks: [
      { id: 'a', member: 'builder', kind: 'work', instructions: 'Build it.', checks: [{ id: 'unit', argv: ['true'] }] },
      { id: 'b', member: 'second', kind: 'work', instructions: 'Build it too.', needs: ['a'], checks: [{ id: 'unit', argv: ['true'] }] },
    ],
    // A setup step that prints nothing: its log is kept empty, with the SHA-256 of no bytes.
    manifest: { setup: [{ id: 'quiet', argv: ['true'] }] },
  }
  const crewId = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', body)).crew.id
  crewIds.push(crewId)
  const runId = await launch(api, crewId)
  await expect.poll(async () => (await api.run(runId)).outcome ?? '', { timeout: 120_000, intervals: [250] }).toBe('succeeded')
  const runPath = `/api/runs/${encodeURIComponent(runId)}`
  const emptySha = createHash('sha256').update(Buffer.alloc(0)).digest('hex')

  // a's quiet receipt offers its log: empty, with its hash, and Save writes no bytes.
  await page.goto(`/runs/${encodeURIComponent(runId)}?view=tasks&task=a`)
  const aReceipts = page.locator('[data-task-detail-id="a"] [data-task-attempt-row="1"] [data-steps]')
  await expect(aReceipts.locator('[data-step-row="setup:quiet:1"] [data-step-words]')).toContainText('exited 0 in')
  const res = await fetch(`${api.server.baseURL}${runPath}/tasks/a/attempts/1/steps/quiet/1/log`, { headers: { Authorization: `Bearer ${api.server.token}` } })
  expect(res.status).toBe(200)
  expect((await res.arrayBuffer()).byteLength).toBe(0)
  expect(res.headers.get('X-Conductor-Log-SHA256')).toBe(emptySha)
  await aReceipts.locator('[data-step-log="setup:quiet:1"]').click()
  const modal = page.locator('[data-step-log-modal]')
  await expect(modal.locator('[data-step-log-sha]')).toHaveText(emptySha)
  await expect(modal.locator('[data-step-log-text]')).toHaveText('')
  const [download] = await Promise.all([page.waitForEvent('download'), page.locator('[data-step-log-save]').click()])
  expect(download.suggestedFilename()).toBe(`${runId}-a-1-setup-quiet-1.log`)
  expect(readFileSync(await download.path()).length).toBe(0)
  await page.keyboard.press('Escape')
  await expect(modal).toHaveCount(0)

  // b's read held until the spec lets it go, then refused. Every log request the page makes on b's route is counted.
  const bRead = (url: URL) => url.pathname === `${runPath}/tasks/b`
  let asked!: () => void
  const bAsked = new Promise<void>((resolve) => (asked = resolve))
  let letGo!: () => void
  const released = new Promise<void>((resolve) => (letGo = resolve))
  await page.route(bRead, async (route) => {
    if (route.request().method() !== 'GET') return route.fallback()
    asked()
    await released
    await route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: { code: 'unavailable', message: 'the ledger is unavailable' } }) })
  })
  const bLogs: string[] = []
  page.on('request', (r) => {
    if (new URL(r.url()).pathname.startsWith(`${runPath}/tasks/b/attempts/`)) bLogs.push(r.url())
  })
  await page.locator('[data-task-row="b"] [role="button"]').click()
  await bAsked
  const bDetail = page.locator('[data-task-detail-id="b"]')
  await expect(bDetail).toBeVisible()
  // While b's read is under way, none of a's attempts shows under b.
  await expect(bDetail.locator('[data-task-attempt-row]')).toHaveCount(0)
  await expect(bDetail.locator('[data-step-log]')).toHaveCount(0)
  letGo()
  // b's read failed: its words show, and still none of a's attempts.
  await expect(bDetail).toContainText('the ledger is unavailable')
  await expect(bDetail.locator('[data-task-attempt-row]')).toHaveCount(0)
  await expect(bDetail.locator('[data-step-log]')).toHaveCount(0)
  expect(bLogs).toEqual([])

  // Read as the server answers, b shows its own receipt, and its Log asks b's route.
  await page.unroute(bRead)
  await page.locator('[data-task-row="a"] [role="button"]').click()
  await expect(aReceipts.locator('[data-step-row="setup:quiet:1"]')).toBeVisible()
  await page.locator('[data-task-row="b"] [role="button"]').click()
  const bReceipts = page.locator('[data-task-detail-id="b"] [data-task-attempt-row="1"] [data-steps]')
  await expect(bReceipts.locator('[data-step-row="setup:quiet:1"] [data-step-words]')).toContainText('exited 0 in')
  await expect(bDetail).not.toContainText('the ledger is unavailable')
  await bReceipts.locator('[data-step-log="setup:quiet:1"]').click()
  await expect(modal.locator('[data-step-log-sha]')).toHaveText(emptySha)
  expect(bLogs.map((u) => new URL(u).pathname)).toEqual([`${runPath}/tasks/b/attempts/1/steps/quiet/1/log`])
  await page.keyboard.press('Escape')
})
