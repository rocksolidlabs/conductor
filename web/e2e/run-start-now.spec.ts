import type { Page } from '@playwright/test'
import { expect, member, test, type Api, type Run } from './fixtures'

// The run page offers a hand's start or resume where the server takes it (memberOffer): Start now on a waiting member of a run
// without tasks, on its tile and its node of the graph; never on a managed run's waiting member, whose tasks start it (the start
// route answers 409 managed_run), nor Resume or Relaunch on its ended member once the run is stopped (the resume route answers the
// same); its header's Add agent is offered on the run without tasks and not on the managed one (runOffer). The managed run's builder is the stub as a plain agent, which never submits, so its deployer waits on build for as long as
// the test looks. Every wait is on the run's own record or on what the page draws.
// Each step raises the test's timeout by the caps of its own waits as it takes them (budget), the config's 120 s above their sum.
test.describe.configure({ mode: 'serial' })

/** The cap of one wait the page's steps make: the config's expect bound, and its actions' (a navigation, a click). */
const STEP_MS = 15_000
test.use({ actionTimeout: STEP_MS, navigationTimeout: STEP_MS })
/** Raises the running test's timeout by `ms`, as a step is about to make waits of those caps. */
function budget(ms: number) {
  const info = test.info()
  info.setTimeout(info.timeout + ms)
}

const crews: string[] = []
const runs: string[] = []

test.afterAll(async ({ api }) => {
  for (const id of runs) await api.stopRun(id)
  for (const id of crews) await api.call('DELETE', `/api/crews/${encodeURIComponent(id)}`)
})

async function launch(api: Api, body: Record<string, unknown>): Promise<Run> {
  const crew = await api.ok<{ crew: { id: string } }>('POST', '/api/crews', { goal: 'wait', cwd: '', where: 'server', openAfterLaunch: false, yolo: null, ...body })
  crews.push(crew.crew.id)
  const run = await api.launchCrew(crew.crew.id)
  runs.push(run.id)
  return run
}

/**
 * The member's tile, then its node in the graph's members, each looked at once it has drawn the member waiting (the words and the
 * button are of one template): Start now on both, or on neither; and the header's Add agent, which a hand's run offers.
 */
async function startOffered(page: Page, runId: string, name: string, managed: boolean, offered: boolean) {
  budget(9 * STEP_MS) // the navigation, the tile's two looks, the header's, the tab, the mode's tab, the node's two looks, and one spare
  await page.goto(`/runs/${encodeURIComponent(runId)}?view=grid`)
  const tile = page.locator(`[data-run-grid] [data-member="${name}"]`)
  await expect(tile.locator('[data-member-words]')).toBeVisible()
  await expect(tile.getByRole('button', { name: 'Start now' })).toHaveCount(offered ? 1 : 0)
  const add = page.getByRole('button', { name: 'Add agent' })
  if (offered) await expect(add).toBeEnabled()
  else await expect(add).toBeDisabled()
  await page.locator('[data-run-view]').getByRole('tab', { name: 'Graph' }).click()
  if (managed) await page.locator('[data-graph-mode-switch]').getByRole('tab', { name: 'Members' }).click()
  const node = page.locator(`[data-graph-canvas] [data-graph-node="${name}"]`)
  await expect(node).toHaveAttribute('data-status', 'pending')
  await expect(node.getByRole('button', { name: 'Start now' })).toHaveCount(offered ? 1 : 0)
}

test("a run without tasks offers Start now on its waiting member's tile and node", async ({ page, api }) => {
  const run = await launch(api, {
    name: 'e2e start now',
    isolation: 'none',
    members: [
      { name: 'lead', agentId: 'claude', prompt: 'say hello', start: { when: 'immediately' } },
      { name: 'later', agentId: 'claude', prompt: 'when asked', start: { when: 'manual' } },
    ],
  })
  budget(STEP_MS)
  await expect.poll(async () => member(await api.run(run.id), 'later').status, { timeout: STEP_MS, message: 'later waits' }).toBe('pending')
  await startOffered(page, run.id, 'later', false, true)
})

test("a managed run's waiting member offers no Start now on its tile or its node, and once stopped its ended member no Resume or Relaunch", async ({ page, api }) => {
  const run = await launch(api, {
    name: 'e2e start now managed',
    isolation: 'worktree',
    members: [
      { name: 'builder', agentId: 'claude', prompt: '', start: { when: 'task' } },
      { name: 'deployer', agentId: 'claude', prompt: '', start: { when: 'task' } },
    ],
    tasks: [
      { id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.' },
      { id: 'deploy', member: 'deployer', kind: 'work', instructions: 'Deploy it.', needs: ['build'] },
    ],
  })
  budget(60_000)
  await expect
    .poll(async () => {
      const r = await api.run(run.id)
      // builder running, past its start: a stop during its start leaves it starting, its session ended.
      return [!!r.managed, member(r, 'builder').status, member(r, 'deployer').status]
    }, { timeout: 60_000, message: 'builder runs build, deployer waits' })
    .toEqual([true, 'running', 'pending'])
  // The server takes no start of it.
  expect((await api.call('POST', `/api/runs/${encodeURIComponent(run.id)}/members/deployer/start`)).status).toBe(409)
  await startOffered(page, run.id, 'deployer', true, false)

  // Stopped, its builder's session ended: the server takes no resume of it, and its tile offers neither Resume nor Relaunch.
  await api.stopRun(run.id)
  let builder = ''
  budget(60_000)
  await expect
    .poll(async () => {
      const r = await api.run(run.id)
      const b = member(r, 'builder')
      builder = b.sessionId ?? ''
      return [!!r.stoppedAt, b.status, builder ? (await api.session(builder)).status : '']
    }, { timeout: 60_000, message: "the run stopped, builder's session ended" })
    .toEqual([true, 'ended', expect.stringMatching(/^(exited|stopped)$/)])
  expect((await api.call('POST', `/api/runs/${encodeURIComponent(run.id)}/members/builder/resume`)).status).toBe(409)
  expect((await api.call('POST', `/api/sessions/${encodeURIComponent(builder)}/resume`)).status).toBe(409)
  budget(3 * STEP_MS) // the navigation, the header's words, its Resume
  await page.goto(`/runs/${encodeURIComponent(run.id)}?view=grid`)
  const header = page.locator(`[data-run-grid] [data-session-id="${builder}"] [data-tile-header]`)
  await expect(header).toContainText(/exited|stopped/)
  await expect(header.locator('[data-resume]')).toHaveCount(0)
})
