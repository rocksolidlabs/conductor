import { copyFileSync, mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { Api, expect, logged, task, test, type Run } from './fixtures'
import { renderConfig, scratchRepo, startServer, stopServer, type Started } from './server'

/** Ends the server at once, as a crash or a kill does: nothing of its state is written on the way out. */
async function crashServer(pid: number): Promise<void> {
  const alive = () => {
    try {
      process.kill(pid, 0)
      return true
    } catch {
      return false
    }
  }
  process.kill(pid, 'SIGKILL')
  const deadline = Date.now() + 5_000
  while (alive() && Date.now() < deadline) await new Promise((r) => setTimeout(r, 50))
}

// A managed run continues after the server's end (#334, row 8): this spec
// runs a server of its own on its own data, ends it while a result is
// being checked, starts it again on the same data (the ledger's owner
// recovers what was left open, and the run's record says interrupted),
// and continues the run: the result is verified again without a session,
// the dependent task starts from it, and the run ends with an outcome.
test.describe.configure({ mode: 'serial' })

let server: Started | null = null

let serverLog = ''
test.afterAll(async () => {
  if (server) await stopServer(server.pid)
  // The server's log beside the test results, for a failure's reading.
  if (serverLog) {
    mkdirSync('test-results', { recursive: true })
    copyFileSync(serverLog, 'test-results/tasks-resume-server.log')
  }
})

test("a run interrupted by the server's end is continued: the candidate verified again, the rest taken up", async ({ state, page }) => {
  test.setTimeout(240_000)
  const here = dirname(fileURLToPath(import.meta.url))
  const root = join(state.root, 'resume')
  const home = join(root, 'home')
  const data = join(root, 'data')
  const repo = join(root, 'repo')
  for (const d of [home, data, repo]) mkdirSync(d, { recursive: true })
  scratchRepo(repo)
  const config = join(root, 'conductor.json')
  renderConfig(join(here, 'conductor.e2e.json'), config)
  const mark = join(root, 'passed')
  serverLog = join(root, 'server.log')
  const start = () => startServer({ config, home, data, allowedRoot: root, defaultCwd: repo, log: serverLog })
  server = await start()
  let api = new Api(server)
  // build's check takes long until the mark exists: the server ends during it.
  const crew = {
    name: 'e2e tasks resume',
    goal: 'continue after a restart',
    cwd: repo,
    where: 'server',
    isolation: 'worktree',
    openAfterLaunch: false,
    yolo: null,
    members: [
      { name: 'builder', agentId: 'worker-cli', prompt: '', start: { when: 'task' } },
      { name: 'writer', agentId: 'worker', prompt: '', start: { when: 'task' } },
    ],
    tasks: [
      { id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.', checks: [{ id: 'mark', argv: ['sh', '-c', `test -e ${mark} || sleep 120`], timeoutSeconds: 180 }] },
      { id: 'docs', member: 'writer', kind: 'work', instructions: 'Document it.', needs: ['build'] },
    ],
  }
  const crewId = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', crew)).crew.id
  const run = await api.launchCrew(crewId)
  let last: Run = run
  await expect
    .poll(async () => {
      last = await api.run(run.id)
      return task(last, 'build').attempt?.state ?? ''
    }, { timeout: 60_000, intervals: [500] })
    .toBe('verifying')
  const candidate = task(last, 'build').attempt!.result!
  expect(candidate).toMatch(/^[0-9a-f]{40}$/)
  // The server ends mid-check, as a crash does (an orderly stop would record a stop instead); the mark lets the check pass from
  // now on; the server starts again on the same data.
  await crashServer(server.pid)
  server = null
  writeFileSync(mark, 'ok\n')
  server = await start()
  api = new Api(server)
  // The record after the recovery: the run interrupted, build's attempt interrupted at its verification, taken up by a continuation.
  await expect
    .poll(async () => {
      last = await api.run(run.id)
      return `${last.state}/${task(last, 'build').attempt?.state}`
    }, { timeout: 30_000, intervals: [500] })
    .toBe('interrupted/interrupted')
  expect(task(last, 'build').state).toBe('ready')
  expect(task(last, 'build').reason).toContain('attempt 1 was interrupted')
  // The continuation.
  const r = await api.call<{ run: Run }>('POST', `/api/runs/${encodeURIComponent(run.id)}/resume`)
  expect(r.status).toBe(201)
  const next = r.body.run
  expect(next.id).not.toBe(run.id)
  expect(next.resumedFrom).toBe(run.id)
  let done: Run = next
  await expect
    .poll(async () => {
      done = await api.run(next.id)
      return done.outcome ?? ''
    }, { timeout: 120_000, intervals: [500] })
    .toBe('succeeded')
  // build: the candidate verified again as attempt 2, without a session; docs: one attempt, from it.
  const build = task(done, 'build')
  expect([build.state, build.attempts, build.result?.attempt, build.result?.commit, build.attempt?.sessionId ?? null]).toEqual(['succeeded', 2, 2, candidate, null])
  expect(build.result?.summary).toBe('stub builder did build, attempt 1')
  const docs = task(done, 'docs')
  expect([docs.state, docs.attempts, docs.attempt?.start]).toEqual(['succeeded', 1, candidate])
  expect(logged(done, `continues ${run.id}: 0 results kept, 1 verified again, 0 uncertain launches waiting for a retry`)).toBe(true)
  expect(logged(done, 'task build: attempt 2 verifies again the result')).toBe(true)
  // The old run names the continuation and is continued once.
  await expect.poll(async () => (await api.run(run.id)).resumedBy ?? '', { timeout: 10_000 }).toBe(next.id)
  const again = await api.call<{ error: { code: string; run?: string } }>('POST', `/api/runs/${encodeURIComponent(run.id)}/resume`)
  expect([again.status, again.body.error.code, again.body.error.run]).toEqual([409, 'already_resumed', next.id])
  // The page of the continuation says what was kept and verified again; the old run's page offers Continue no more.
  await page.addInitScript((t) => localStorage.setItem('conductor.workbenchToken', t), server.token)
  await page.goto(`${server.baseURL}/runs/${encodeURIComponent(next.id)}?view=tasks`)
  await expect(page.locator('[data-task-row="build"] [data-task-words]')).toContainText('succeeded by attempt 2')
  await expect(page.locator('[data-crew-run-header] [data-run-task-badge]').first()).toHaveText('2/2 tasks')
  await page.goto(`${server.baseURL}/runs/${encodeURIComponent(run.id)}?view=tasks`)
  await expect(page.locator('[data-run-resume]')).toHaveCount(0)
  await expect(page.locator('[data-run-record]')).toBeVisible()
})
