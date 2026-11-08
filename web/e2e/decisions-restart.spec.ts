import { execFileSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, logged, stopStrays, task, test, type RunAttempt } from './fixtures'
import { budget, decision, decisionServer, decisionUntil, decisionsOf, operatorPage, runUntil, type DecisionServer } from './decisions-helpers'

// A durable decision across the server's end (Program item DR1, docs/plans/decisions.md, PR-8), on a server of its own
// (tasks-resume.spec.ts's pattern): `build`'s stub worker asks the operator an approval and exits while it waits (STUB_DECISION_EXIT),
// so its attempt 1 ends at the stage `decision`, given back, not counted (the record's `refunded` row); the server is killed and
// started again on the same data, the run interrupted and the decision still open; the operator answers it on the run page: Recorded,
// waiting for the run to be continued; Continue: the continuation applies the answer once, through the task file, and build's next
// attempt, attempt 2 of 4 (the attempt given back is not one of its 3), starts with the answer in its task file and succeeds. Every
// wait is on the record through the API or on what the page draws.
test.describe.configure({ mode: 'serial' })

const STEP_MS = 15_000
test.use({ actionTimeout: STEP_MS, navigationTimeout: STEP_MS })

let srv: DecisionServer
let crewId = ''
let runId = ''
let nextId = ''

/** A task's file as task.json has it, what the spec reads of it. */
interface TaskJSON {
  task: string
  attempt: number
  maxAttempts: number
  decisions?: Array<{ id: string; type: string; state: string; question: string; answer?: string; rationale?: string; by?: string }>
}

test.beforeAll(async () => {
  srv = await decisionServer('decisions-restart', [{ id: 'worker-asks-exits', from: 'worker', env: { STUB_DECISION: 'approval', STUB_DECISION_EXIT: '1' } }])
  await stopStrays(srv.api, [])
  crewId = (
    await srv.api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
      name: 'e2e decisions restart',
      goal: 'ask the operator, and go on after a restart',
      cwd: '',
      where: 'server',
      isolation: 'worktree',
      openAfterLaunch: false,
      yolo: null,
      members: [{ name: 'builder', agentId: 'worker-asks-exits', prompt: '', start: { when: 'task' } }],
      tasks: [{ id: 'build', member: 'builder', kind: 'work', instructions: 'Build it, once the operator approves.' }],
    })
  ).crew.id
})

test.afterAll(async () => {
  if (!srv) return
  for (const id of [nextId, runId]) if (id) await srv.api.stopRun(id)
  await srv.stop()
})

test("an approval asked by an attempt that ended survives the server's end, is answered, and is applied once when the run is continued", async ({ browser }) => {
  test.slow()
  const question = 'May build commit its change?'
  runId = (await srv.api.launchCrew(crewId)).id

  // The decision, as the record says it: opened by build's attempt 1, open.
  budget(120_000)
  let id = ''
  await expect
    .poll(async () => (id = (await decisionsOf(srv.api, runId)).find((d) => d.asker?.task === 'build' && d.state === 'open')?.id ?? ''), { timeout: 120_000, intervals: [500], message: "build's attempt asks its decision" })
    .toMatch(/^dr-[1-9][0-9]*$/)
  expect(id).toBe('dr-1')

  // The stub exits once it has asked: the attempt ends while it waits, given back. The ledger holds the refund (the record's history,
  // read from it) before anything is judged or the server is ended.
  budget(60_000)
  await expect
    .poll(async () => (await decision(srv.api, id)).history?.filter((h) => h.kind === 'refunded').length ?? 0, { timeout: 60_000, intervals: [250], message: "build's attempt 1 is given back while it waits" })
    .toBe(1)
  let run = await srv.api.run(runId)
  let build = task(run, 'build')
  expect([build.state, build.attempts, build.attempt?.n, build.attempt?.state, build.attempt?.stage]).toEqual(['ready', 1, 1, 'failed', 'decision'])
  expect(logged(run, `task build: attempt 1 ended while it waits for ${id}: given back, not counted`)).toBe(true)
  expect((await decision(srv.api, id)).state).toBe('open')

  // The run page says what build waits for, before the server's end.
  const before = await operatorPage(browser, srv.started)
  budget(3 * STEP_MS)
  await before.goto(`${srv.started.baseURL}/runs/${runId}?view=tasks`)
  await expect(before.locator('[data-task-row="build"] [data-task-words]')).toHaveText(`waits for decision ${id}`, { timeout: 30_000 })
  await before.context().close()

  // The server is killed and started again on the same data: the run interrupted, build still held, its attempt 1 at `decision`, and
  // the decision open as it was.
  const kept = await decision(srv.api, id)
  budget(60_000)
  await srv.restart()
  const api = srv.api
  await expect.poll(async () => (await api.run(runId)).state, { timeout: 30_000, intervals: [500], message: 'the run is recovered interrupted' }).toBe('interrupted')
  run = await api.run(runId)
  build = task(run, 'build')
  expect([build.state, build.attempts, build.attempt?.n, build.attempt?.stage]).toEqual(['ready', 1, 1, 'decision'])
  const reopened = await decision(api, id)
  expect([reopened.state, reopened.rev, reopened.run]).toEqual(['open', kept.rev, runId])

  // The run page's graph says it as its task list does (#641, review round 1): held, taken up when the run is continued; drawn again
  // after a switch to the members and back, the same.
  const page = await operatorPage(browser, srv.started)
  const base = srv.started.baseURL
  const held = `waits for decision ${id}; taken up when the run is continued`
  budget(5 * STEP_MS)
  await page.goto(`${base}/runs/${runId}?view=graph`)
  const node = page.locator('[data-graph-canvas] [data-graph-task="build"]')
  await expect(node).toHaveAttribute('aria-label', `build: ${held}`, { timeout: 30_000 })
  await page.locator('[data-graph-mode-switch]').getByRole('tab', { name: 'Members' }).click()
  await expect(node).toHaveCount(0)
  await page.locator('[data-graph-mode-switch]').getByRole('tab', { name: 'Tasks' }).click()
  await expect(node).toHaveAttribute('aria-label', `build: ${held}`)

  // The operator answers it on the run page: Recorded, waiting for the run to be continued (it is not running), and nothing applied.
  budget(10 * STEP_MS)
  await page.goto(`${base}/runs/${runId}?view=tasks`)
  const row = page.locator('[data-task-row="build"]')
  await expect(row.locator('[data-task-words]')).toHaveText(held, { timeout: 30_000 })
  await row.locator('[data-task-answer="build"]').click()
  const modal = page.locator(`[data-decision-modal][data-decision-modal-id="${id}"]`)
  await expect(modal.locator('[data-decision-question]')).toHaveText(question)
  await modal.locator('[data-decision-choice-label="Approve"]').click()
  await modal.locator('[data-decision-rationale]').fill('Restart proven.')
  await modal.locator('[data-decision-confirm]').click()
  await expect(modal.locator('[data-decision-receipt]')).toContainText(`Recorded: ${id} answered by Nate`)
  const answered = await decisionUntil(api, id, 'answered')
  expect(answered.applied).toBeUndefined()
  await expect(modal.locator('[data-decision-state-words]')).toHaveText('Recorded: waiting for the run to be continued', { timeout: 30_000 })
  await expect(modal.locator('[data-decision-step="validated"] [data-step-words]')).toContainText('waiting for the run to be continued')
  await page.getByRole('dialog', { name: /^Decision dr-/ }).getByRole('button', { name: 'Close' }).click()
  await expect(page.locator('[data-decision-modal]')).toHaveCount(0)

  // Continue: the continuation takes the decision and its hold, and applies the answer once, through build's task file.
  budget(4 * STEP_MS)
  await page.locator('[data-run-resume]').click()
  await expect.poll(async () => (nextId = (await api.run(runId)).resumedBy ?? ''), { timeout: 30_000, intervals: [500], message: 'the run is continued' }).not.toBe('')
  await expect(page).toHaveURL(new RegExp(`/runs/${nextId}`))
  const applied = await decisionUntil(api, id, 'applied')
  expect([applied.run, applied.applied?.how, applied.applied?.words]).toEqual([nextId, 'task_file', "build's next attempt reads the answer in its task file"])
  expect(applied.history?.filter((h) => h.kind === 'applied')).toHaveLength(1)
  expect(applied.history?.filter((h) => h.kind === 'refunded')).toHaveLength(1)

  // build's next attempt starts with the answer in its task file, and succeeds.
  const done = await runUntil(api, nextId, (r) => task(r, 'build').state === 'succeeded', 'build succeeds once its answer is applied')
  const result = task(done, 'build').result!
  expect(result.attempt).toBe(2)
  // The task route names attempt 2's worktree, where its task file is (.conductor/task.json and task.md): attempt 2 of 4, the attempt
  // given back not one of the task's 3, and the decision applied with its answer.
  const route = await api.ok<{ attempts: RunAttempt[] }>('GET', `/api/runs/${encodeURIComponent(nextId)}/tasks/build`)
  const a2 = route.attempts.find((a) => a.n === 2)
  expect(a2?.worktree).toBeTruthy()
  const file = JSON.parse(readFileSync(join(a2!.worktree!, '.conductor', 'task.json'), 'utf8')) as TaskJSON
  expect([file.task, file.attempt, file.maxAttempts]).toEqual(['build', 2, 4])
  expect(file.decisions?.find((d) => d.id === id)).toMatchObject({ type: 'approval', state: 'applied', question, answer: 'Approve', rationale: 'Restart proven.', by: 'Nate' })
  const md = readFileSync(join(a2!.worktree!, '.conductor', 'task.md'), 'utf8')
  const line = `${id} (approval), applied: ${question} Nate answered: Approve. Why: Restart proven.`
  expect(md).toContain('(attempt 2 of 4)')
  expect(md).toContain(`- ${line}`)
  // The stub read it through its task route (the task_context tool), asked nothing again, and committed the line it read.
  const answer = execFileSync('git', ['show', `${result.commit}:stub-answer.txt`], { cwd: srv.repo, encoding: 'utf8' })
  expect(answer.trim()).toBe(line)
  const after = await decision(api, id)
  expect([after.state, after.history?.filter((h) => h.kind === 'applied').length, after.history?.filter((h) => h.kind === 'delivery.task_file').length]).toEqual(['applied', 1, 1])
  expect((await decisionsOf(api, nextId)).map((d) => d.id)).toEqual([id])

  // The old run's attempt 1 still reads stage `decision`; the card, opened on the continuation, says how it was applied.
  expect(task(await api.run(runId), 'build').attempt).toMatchObject({ n: 1, stage: 'decision' })
  budget(3 * STEP_MS)
  await page.goto(`${base}/runs/${nextId}?view=tasks&decision=${id}`)
  await expect(modal.locator('[data-decision-state-words]')).toHaveText("Applied: build's next attempt reads the answer in its task file", { timeout: 30_000 })
  await page.context().close()
})
