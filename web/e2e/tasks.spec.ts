import { execFileSync } from 'node:child_process'
import type { Page } from '@playwright/test'
import { expect, logged, task, test, type Run } from './fixtures'

// A managed run end to end, with the stub as its workers (#334, row 6): a
// crew with tasks launches, the tasks are dispatched into attempt worktrees,
// the worker reads its task through the MCP tools (the builder) or the CLI
// (the writer) and submits its result, a check that fails starts a fresh
// attempt at the result commit with the log in its task file, the dependent
// task starts at the accepted commit, and the run ends with an outcome. A
// done finishes nothing: the stub reports done after every submit, and no
// member ever started because another was done.
test.describe.configure({ mode: 'serial' })

let crewId = ''
const runs: string[] = []

const crewBody = {
  name: 'e2e tasks',
  goal: 'prove the harness',
  cwd: '',
  where: 'server',
  isolation: 'worktree',
  openAfterLaunch: false,
  yolo: null,
  members: [
    { name: 'builder', agentId: 'worker', prompt: '', start: { when: 'task' } },
    { name: 'writer', agentId: 'worker-cli', prompt: '', start: { when: 'task' } },
  ],
  tasks: [
    // The check fails until the builder adds stub-fixed.txt, which it does on its second attempt.
    { id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.', checks: [{ id: 'fixed', argv: ['git', 'cat-file', '-e', 'HEAD:stub-fixed.txt'] }] },
    { id: 'docs', member: 'writer', kind: 'work', instructions: 'Document it.', needs: ['build'] },
  ],
}

test.beforeAll(async ({ api }) => {
  crewId = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', crewBody)).crew.id
})

test.afterAll(async ({ api }) => {
  for (const id of runs) await api.stopRun(id)
  if (crewId) await api.call('DELETE', `/api/crews/${encodeURIComponent(crewId)}`)
})

const git = (cwd: string, ...args: string[]) => execFileSync('git', args, { cwd, encoding: 'utf8' }).trim()

test('a check that fails starts a second attempt at the result, the dependent task starts at the accepted commit, and the run succeeds', async ({ api, state }) => {
  const run = await api.launchCrew(crewId)
  runs.push(run.id)
  expect(run.managed, 'launched as a managed run').toBeTruthy()
  let last: Run = run
  await expect
    .poll(async () => {
      last = await api.run(run.id)
      return last.outcome ?? ''
    }, { timeout: 120_000, intervals: [500] })
    .toBe('succeeded')
  expect(last.state).toBe('finished')
  // build: attempt 1 submitted and failed its check; attempt 2 started at that commit and passed.
  const build = task(last, 'build')
  expect(build.state).toBe('succeeded')
  expect(build.attempts).toBe(2)
  const { attempts } = await api.ok<{ task: unknown; attempts: Array<{ n: number; state: string; stage?: string; error?: string; start?: string; result?: string; submitted?: { summary?: string } }> }>(
    'GET',
    `/api/runs/${encodeURIComponent(run.id)}/tasks/build`,
  )
  expect(attempts.map((a) => [a.n, a.state])).toEqual([
    [1, 'failed'],
    [2, 'succeeded'],
  ])
  expect(attempts[0].stage).toBe('check fixed')
  expect(attempts[0].submitted?.summary).toBe('stub builder did build, attempt 1')
  expect(attempts[1].start).toBe(attempts[0].result)
  expect(build.result?.commit).toBe(git(state.repo, 'rev-parse', `crew/${run.id}/build.2`))
  expect(git(state.repo, 'cat-file', '-e', `${build.result?.commit}:stub-fixed.txt`)).toBe('')
  // docs: one attempt, in a worktree made at build's result; its commit descends from it.
  const docs = task(last, 'docs')
  expect(docs.state).toBe('succeeded')
  expect(docs.attempts).toBe(1)
  expect(docs.attempt?.start).toBe(build.result?.commit)
  expect(git(state.repo, 'merge-base', '--is-ancestor', build.result!.commit!, docs.result!.commit!)).toBe('')
  // The run log: the dispatch, the failed check, the results; never a start by done.
  expect(logged(last, 'task build: attempt 1 failed at check fixed')).toBe(true)
  expect(logged(last, 'task docs: attempt 1 started as writer')).toBe(true)
  expect(logged(last, 'every task is done: the run succeeded')).toBe(true)
  expect(last.log.some((l) => /is done: starting/.test(l.message ?? ''))).toBe(false)
})

test('a task read by the admin is the attempt\'s task file, and a task of an unknown run is not found', async ({ api }) => {
  const run = await api.run(runs[0]!)
  const build = task(run, 'build')
  const r = await api.call<{ markdown: string; page: number; pages: number; task: { task: string; attempt: number } }>('GET', `/api/sessions/${build.attempt?.sessionId}/task`)
  expect(r.status).toBe(200)
  expect(r.body.page).toBe(1)
  expect(r.body.markdown).toContain('Build it.')
  expect(r.body.markdown).toContain('The attempt before')
  expect([r.body.task.task, r.body.task.attempt]).toEqual(['build', 2])
  expect((await api.call('GET', '/api/runs/nope-00000000/tasks/build')).status).toBe(404)
  expect((await api.call('GET', `/api/runs/${encodeURIComponent(run.id)}/tasks/nope`)).status).toBe(404)
})

// The run page (#334, row 7): the Tasks view with its rows, the selected task's attempts, the header's task badges, the graph's
// Tasks mode with its needs edges, the grid tile's task, and the palette's task rows.
test('the run page shows the tasks, their attempts and results, the task graph, and the tasks in the palette', async ({ page, api, state }) => {
  const run = await api.run(runs[0]!)
  await page.goto(`/runs/${encodeURIComponent(run.id)}?view=tasks`)
  await expect(page.locator('[data-run-view]').getByRole('tab', { name: 'Tasks' })).toHaveAttribute('aria-selected', 'true')
  const rows = page.locator('[data-task-row]')
  await expect(rows).toHaveCount(2)
  await expect(page.locator('[data-task-row="build"]')).toHaveAttribute('data-task-state', 'succeeded')
  await expect(page.locator('[data-task-row="build"] [data-task-attempt]')).toHaveText('attempt 2')
  await expect(page.locator('[data-task-row="docs"] [data-task-words]')).toContainText('succeeded by attempt 1')
  const commit = git(state.repo, 'rev-parse', `crew/${run.id}/build.2`)
  await expect(page.locator('[data-task-row="build"] [data-task-commit]')).toHaveText(commit.slice(0, 12))
  await expect(page.locator('[data-crew-run-header] [data-run-task-badge]').first()).toHaveText('2/2 tasks')
  // The selected task, in the URL: its result and both attempts, the first failed at its check with what it submitted.
  await page.locator('[data-task-row="build"] [role="button"]').click()
  await expect(page).toHaveURL(/task=build/)
  const detail = page.locator('[data-task-detail-id="build"]')
  await expect(detail.locator('[data-task-result]')).toContainText('attempt 2')
  await expect(detail.locator('[data-task-attempt-row]')).toHaveCount(2)
  await expect(detail.locator('[data-task-attempt-row="1"]')).toHaveAttribute('data-attempt-state', 'failed')
  await expect(detail.locator('[data-task-attempt-row="1"]')).toContainText('at check fixed')
  await expect(detail.locator('[data-task-attempt-row="1"]')).toContainText('stub builder did build, attempt 1')
  await page.locator('[data-task-detail-close]').click()
  await expect(page).not.toHaveURL(/task=/)
  // The grid tile says the member's task; the graph draws the tasks with their needs.
  await page.locator('[data-run-view]').getByRole('tab', { name: 'Grid' }).click()
  await expect(page.locator('[data-member="builder"] [data-branch]')).toContainText('build #2')
  await page.locator('[data-run-view]').getByRole('tab', { name: 'Graph' }).click()
  const canvas = page.locator('[data-graph-canvas]')
  const needs = canvas.locator('[data-graph-edge="build-docs"][data-graph-edge-kind="needs"]')
  await expect(page.locator('[data-graph-mode-switch]')).toBeVisible()
  await expect(canvas.locator('[data-graph-task]')).toHaveCount(2)
  await expect(canvas.locator('[data-graph-task="docs"]')).toHaveAttribute('data-task-state', 'succeeded')
  await expect(needs).toHaveCount(1)
  await page.locator('[data-graph-mode-switch]').getByRole('tab', { name: 'Members' }).click()
  await expect(canvas.locator('[data-graph-node]')).toHaveCount(2)
  await expect(canvas.locator('[data-graph-task]')).toHaveCount(0)
  // Drawn again, the tasks keep their needs edge (#281): every drawing after the first dropped it, at the page's 5 s clock tick as at this switch.
  await page.locator('[data-graph-mode-switch]').getByRole('tab', { name: 'Tasks' }).click()
  await expect(canvas.locator('[data-graph-task]')).toHaveCount(2)
  await expect(needs).toHaveCount(1)
  // The palette lists the tasks under the page's group.
  const palette = await openPalette(page)
  await palette.locator('input[data-palette-input]').fill('docs')
  await expect(palette.locator('[data-palette-row="page:task:docs"]')).toBeVisible()
  await expect(palette.locator('[data-palette-row="page:task:docs"]')).toContainText('work · writer')
  await page.keyboard.press('Escape')
})

const failCrew = {
  name: 'e2e tasks fail',
  goal: 'fail a check',
  cwd: '',
  where: 'server',
  isolation: 'worktree',
  openAfterLaunch: false,
  yolo: null,
  members: [
    { name: 'builder', agentId: 'worker-cli', prompt: '', start: { when: 'task' } },
    { name: 'writer', agentId: 'worker', prompt: '', start: { when: 'task' } },
  ],
  tasks: [
    { id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.', checks: [{ id: 'never', argv: ['sh', '-c', 'exit 1'] }] },
    { id: 'docs', member: 'writer', kind: 'work', instructions: 'Document it.', needs: ['build'] },
  ],
}
let failCrewId = ''
test.afterAll(async ({ api }) => {
  if (failCrewId) await api.call('DELETE', `/api/crews/${encodeURIComponent(failCrewId)}`)
})

test('a task that fails its checks three times offers Retry and Accept anyway; the dispatch pauses and resumes; Accept anyway finishes the run', async ({ page, api }) => {
  failCrewId = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', failCrew)).crew.id
  const run = await api.launchCrew(failCrewId)
  runs.push(run.id)
  await expect
    .poll(async () => (await api.run(run.id)).outcome ?? '', { timeout: 120_000, intervals: [500] })
    .toBe('failed')
  await page.goto(`/runs/${encodeURIComponent(run.id)}?view=tasks`)
  const build = page.locator('[data-task-row="build"]')
  await expect(build).toHaveAttribute('data-task-state', 'failed')
  await expect(build.locator('[data-task-words]')).toContainText('attempt 3 failed at check never')
  await expect(page.locator('[data-crew-run-header] [data-run-task-badge]').nth(1)).toHaveText('1 failed')
  await expect(page.locator('[data-task-row="docs"] [data-task-words]')).toContainText('will not succeed')
  // Pause and resume the dispatch: a receipt each, the badge following.
  await page.locator('[data-run-pause]').click()
  await expect(page.locator('[data-run-pause][data-run-paused]')).toBeVisible()
  await expect(page.locator('[data-crew-run-header] [data-run-task-badge]', { hasText: 'paused' })).toHaveCount(1)
  await page.locator('[data-run-pause]').click()
  await expect(page.locator('[data-run-pause][data-run-paused]')).toHaveCount(0)
  await expect(page.locator('[data-crew-run-header] [data-run-task-badge]', { hasText: 'paused' })).toHaveCount(0)
  // Accept anyway asks, then takes attempt 3's commit as the result; docs starts from it and the run succeeds.
  const before = task(await api.run(run.id), 'build')
  await build.locator('[data-task-accept="build"]').click()
  await page.locator('[data-task-confirm]').click()
  await expect(build).toHaveAttribute('data-task-state', 'succeeded')
  await expect
    .poll(async () => (await api.run(run.id)).outcome ?? '', { timeout: 120_000, intervals: [500] })
    .toBe('succeeded')
  const after = await api.run(run.id)
  expect(task(after, 'build').result?.commit).toBe(before.attempt?.result)
  expect(task(after, 'docs').attempt?.start).toBe(before.attempt?.result)
  expect(logged(after, "task build: attempt 3's result accepted by admin")).toBe(true)
  await expect(page.locator('[data-crew-run-header] [data-run-task-badge]').first()).toHaveText('2/2 tasks')
})

/** Opens the palette and returns it: Alt+/ (the terminal's way, which the page shares), else Ctrl+K where no terminal has the focus. */
async function openPalette(page: Page) {
  const palette = page.locator('[data-palette]')
  await page.keyboard.press('Alt+/')
  if (!(await palette.waitFor({ state: 'visible', timeout: 1500 }).then(() => true, () => false))) await page.keyboard.press('Control+k')
  await expect(palette).toBeVisible()
  return palette
}
