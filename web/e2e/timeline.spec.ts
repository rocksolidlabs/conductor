import { expect, logged, member, test, type Run } from './fixtures'

// The run timeline and the charts: a member held with a trust question shows
// an amber segment on its row; the Crews list charts a crew's runs once two
// ended; History's feed charts the stub's activity.
test.describe.configure({ mode: 'serial' })

let crewId = ''
const runs: string[] = []

test.beforeAll(async ({ api }) => {
  crewId = (
    await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
      name: 'e2e timeline',
      goal: 'timeline',
      cwd: '',
      where: 'server',
      isolation: 'none',
      openAfterLaunch: false,
      members: [
        { name: 'lead', agentId: 'claude', prompt: 'say hello', start: { when: 'immediately' } },
        { name: 'held', agentId: 'codex-untrusted', prompt: 'say hi', start: { when: 'immediately' } },
      ],
    })
  ).crew.id
})

test.afterAll(async ({ api }) => {
  for (const id of runs) await api.stopRun(id)
  if (crewId) await api.call('DELETE', `/api/crews/${encodeURIComponent(crewId)}`)
})

test('a member held with a trust question shows an amber segment on its row, and the stop line once stopped', async ({ page, api }) => {
  const run = await api.launchCrew(crewId)
  runs.push(run.id)
  let r: Run = run
  await expect
    .poll(async () => {
      r = await api.run(run.id)
      return member(r, 'held').needsInput === true && logged(r, "typed lead's prompt")
    }, { timeout: 60_000, message: 'held waits and lead runs' })
    .toBe(true)
  await page.goto(`/runs/${encodeURIComponent(run.id)}`)
  await page.locator('[data-run-view]').getByRole('tab', { name: 'Timeline' }).click()
  const tl = page.locator('[data-run-timeline]')
  await expect(tl).toBeVisible()
  await expect(tl.locator('[data-timeline-row="lead"] [data-timeline-bar]')).toHaveCount(1)
  await expect(tl.locator('[data-timeline-row="held"] [data-timeline-wait]')).toHaveCount(1, { timeout: 15_000 })
  await expect(tl.locator('[data-timeline-row="held"] [data-timeline-wait]')).toHaveAttribute('data-open', 'true')
  await expect(tl.locator('[data-timeline-row="lead"] [data-timeline-wait]')).toHaveCount(0)
  await expect(tl.locator('[data-timeline-stop]')).toHaveCount(0)
  await api.stopRun(run.id)
  await expect(tl.locator('[data-timeline-stop]')).toHaveCount(1, { timeout: 15_000 })
  await expect(tl.locator('[data-timeline-row="held"]')).toHaveAttribute('data-status', 'ended')
})

test('the crew\'s Runs tab charts the runs once there are two, and the feed in History charts the activity', async ({ page, api }) => {
  await page.goto(`/crews/${encodeURIComponent(crewId)}?tab=runs`)
  const runsChart = page.locator('[data-runs-chart]')
  await expect(runsChart).toHaveAttribute('data-runs', '1', { timeout: 15_000 })
  await expect(runsChart.locator('[data-runs-chart-empty]')).toHaveText('A chart of the last runs appears after two.')
  // The home's row of the crew draws the same runs as bars.
  await page.goto('/crews')
  const entry = page.locator(`[data-crew-entry][data-crew-id="${crewId}"]`)
  await expect(entry.locator('[data-crew-bars]')).toHaveAttribute('data-runs', '1', { timeout: 15_000 })
  await expect(entry.locator('[data-crew-bars] [data-run-bar]')).toHaveAttribute('data-outcome', 'stopped')
  await expect(entry.locator('[data-crew-last-run]')).toHaveText(/^today /)
  const second = await api.launchCrew(crewId)
  runs.push(second.id)
  await expect.poll(async () => logged(await api.run(second.id), "typed lead's prompt"), { timeout: 60_000 }).toBe(true)
  await api.stopRun(second.id)
  await page.goto(`/crews/${encodeURIComponent(crewId)}?tab=runs`)
  await expect(runsChart).toHaveAttribute('data-runs', '2', { timeout: 20_000 })
  await expect(runsChart.locator('[data-runs-chart-empty]')).toHaveCount(0)
  await expect(runsChart.locator('[data-run-bar]')).toHaveCount(2)
  await expect(runsChart).toContainText('median')
  // Both runs are rows, named by when they started, stopped and offering a new run.
  await expect(page.locator(`[data-crew-runs] [data-run="${second.id}"]`)).toHaveAttribute('data-state', 'stopped')
  await expect(page.locator(`[data-crew-runs] [data-run="${second.id}"] [data-run-resume]`)).toHaveText(/Resume as new run/)

  // The feed holds what arrives while the page is open: the chart fills as a run reports.
  await page.goto('/history?view=feed')
  await expect(page.locator('[data-event-feed]')).toBeVisible()
  const chart = page.locator('[data-activity-chart]')
  await expect(chart).toBeVisible()
  await expect(chart.locator('[data-activity-chart-empty]')).toBeVisible()
  const third = await api.launchCrew(crewId)
  runs.push(third.id)
  await expect.poll(async () => Number(await chart.getAttribute('data-total')), { timeout: 60_000 }).toBeGreaterThanOrEqual(2)
  await expect(chart.locator('[data-activity-bar]')).toHaveCount(60)
  await expect(chart.locator('[data-activity-chart-empty]')).toHaveCount(0)
})
