import type { Page } from '@playwright/test'
import { expect, member, standingRoute, test, type Api, type Run } from './fixtures'

// The run page offers a hand's start or resume where the server takes it (memberOffer): Start now on a waiting member of a run
// without tasks, on its tile and its node of the graph; never on a managed run's waiting member, whose tasks start it (the start
// route answers 409 managed_run), nor Resume or Relaunch on its ended member once the run is stopped (the resume route answers the
// same); its header's Add agent is offered on the run without tasks and not on the managed one (runOffer). A stopped run the engine
// holds offers its ended member's Resume; its record offers nothing. The managed run's builder is the stub as a plain agent, which never submits, so its deployer waits on build for as long as
// the test looks. Every wait is on the run's own record or on what the page draws.
// Each step raises the test's timeout by the caps of its own waits as it takes them (budget), the config's 120 s above their sum.
test.describe.configure({ mode: 'serial' })

// The tests route the page while it works: the standing route keeps its interception on from before the first load.
test.beforeEach(async ({ page }) => {
  await standingRoute(page.context())
})

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
  // The header's Add agent, in the run's menu: offered or disabled; the menu closed and gone before the next click.
  await page.locator('[data-crew-run-header] [data-run-menu]').click()
  const add = page.locator('[role="menuitem"][data-run-add]')
  await expect(add).toBeVisible()
  if (offered) await expect(add).not.toHaveAttribute('data-disabled', '')
  else await expect(add).toHaveAttribute('data-disabled', '')
  await page.keyboard.press('Escape')
  await expect(page.locator('[role="menu"]')).toHaveCount(0)
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
  // The state's word is at the tile's foot, the header keeps the name and the actions: a member the run's stop ended reads Stopped
  // (the Crews pages' words, C2).
  const foot = page.locator(`[data-run-grid] [data-session-id="${builder}"] [data-tile-foot] [data-status-mark]`)
  await expect(foot).toHaveAttribute('data-status-mark', 'cancelled')
  await expect(foot).toHaveAttribute('aria-label', 'Stopped')
  await expect(header.locator('[data-resume]')).toHaveCount(0)
})

test("a stopped run the engine holds offers its ended member's Resume or Relaunch; as a record, read when the engine holds it no longer, nothing", async ({ page, api }) => {
  const run = await launch(api, { name: 'e2e start now record', isolation: 'none', members: [{ name: 'lead', agentId: 'claude', prompt: 'say hello', start: { when: 'immediately' } }] })
  budget(STEP_MS)
  await expect.poll(async () => member(await api.run(run.id), 'lead').status, { timeout: STEP_MS, message: 'lead runs' }).toBe('running')
  await api.stopRun(run.id)
  let lead = ''
  budget(60_000)
  await expect
    .poll(async () => {
      const r = await api.run(run.id)
      lead = member(r, 'lead').sessionId ?? ''
      return [!!r.stoppedAt, member(r, 'lead').status, lead ? (await api.session(lead)).status : '']
    }, { timeout: 60_000, message: "the run stopped, lead's session ended" })
    .toEqual([true, 'ended', expect.stringMatching(/^(exited|stopped)$/)])
  budget(3 * STEP_MS) // the navigation, the header's words, its Resume
  await page.goto(`/runs/${encodeURIComponent(run.id)}?view=grid`)
  const header = page.locator(`[data-run-grid] [data-session-id="${lead}"] [data-tile-header]`)
  // A member the run's stop ended reads Stopped at its tile's foot (the Crews pages' words, C2).
  await expect(page.locator(`[data-run-grid] [data-session-id="${lead}"] [data-tile-foot] [data-status-mark]`)).toHaveAttribute('data-status-mark', 'cancelled')
  await expect(header.locator('[data-resume]')).toHaveCount(1)

  // The run as the server answers a run it no longer holds: its read marked a record, and absent from the live runs.
  const path = `/api/runs/${encodeURIComponent(run.id)}`
  await page.route((u) => u.pathname === path, async (route) => {
    const r = await route.fetch()
    await route.fulfill({ response: r, json: { ...(await r.json()), record: true } })
  })
  await page.route((u) => u.pathname === '/api/runs', async (route) => {
    const r = await route.fetch()
    const body = (await r.json()) as { runs: Run[] }
    await route.fulfill({ response: r, json: { ...body, runs: body.runs.filter((x) => x.id !== run.id) } })
  })
  budget(3 * STEP_MS) // the navigation, the record's badge, the header's words
  await page.reload()
  await expect(page.locator('[data-run-record]')).toBeVisible()
  await expect(page.locator(`[data-run-grid] [data-session-id="${lead}"] [data-tile-foot] [data-status-mark]`)).toHaveAttribute('data-status-mark', 'cancelled')
  await expect(header.locator('[data-resume]')).toHaveCount(0)
})
