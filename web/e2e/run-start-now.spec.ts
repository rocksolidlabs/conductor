import type { Page } from '@playwright/test'
import { expect, member, test, type Api, type Run } from './fixtures'

// The run page offers Start now where the server takes it (offersStart): on a waiting member of a run without tasks, on its tile and
// its node of the graph; never on a managed run's waiting member, whose tasks start it (the start route answers 409 managed_run).
// The managed run's builder is the stub as a plain agent, which never submits, so its deployer waits on build for as long as the
// test looks. Every wait is on the run's own record or on what the page draws.
test.describe.configure({ mode: 'serial' })

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
 * button are of one template): Start now on both, or on neither.
 */
async function startOffered(page: Page, runId: string, name: string, managed: boolean, offered: boolean) {
  await page.goto(`/runs/${encodeURIComponent(runId)}?view=grid`)
  const tile = page.locator(`[data-run-grid] [data-member="${name}"]`)
  await expect(tile.locator('[data-member-words]')).toBeVisible()
  await expect(tile.getByRole('button', { name: 'Start now' })).toHaveCount(offered ? 1 : 0)
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
  await expect.poll(async () => member(await api.run(run.id), 'later').status, { message: 'later waits' }).toBe('pending')
  await startOffered(page, run.id, 'later', false, true)
})

test("a managed run's waiting member offers no Start now on its tile or its node", async ({ page, api }) => {
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
  await expect
    .poll(async () => {
      const r = await api.run(run.id)
      return [!!r.managed, member(r, 'builder').status !== 'pending', member(r, 'deployer').status]
    }, { timeout: 60_000, message: 'build started, deployer waits' })
    .toEqual([true, true, 'pending'])
  // The server takes no start of it.
  expect((await api.call('POST', `/api/runs/${encodeURIComponent(run.id)}/members/deployer/start`)).status).toBe(409)
  await startOffered(page, run.id, 'deployer', true, false)
})
