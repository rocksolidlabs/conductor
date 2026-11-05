import { expect, logged, test } from './fixtures'

// The Crews pages tell a saved crew (a plan) from a run (one launch of it):
// the home lists the runs going now above the saved crews; a crew's page has
// its Setup and its Runs, says when a run of it is live, and keeps unsaved
// edits in a bar of their own; a run is named by its crew and its start.
test.describe.configure({ mode: 'serial' })

let crewId = ''
const runs: string[] = []

test.afterAll(async ({ api }) => {
  for (const id of runs) await api.stopRun(id)
  if (crewId) await api.call('DELETE', `/api/crews/${encodeURIComponent(crewId)}`)
})

test('a new crew is drafted at /crews/new, saved from its bar, and listed with its shape', async ({ page }) => {
  await page.goto('/crews')
  await page.locator('[data-new-crew]').click()
  await expect(page).toHaveURL(/\/crews\/new$/)
  await expect(page.locator('[data-crew-kind]')).toHaveText('New crew')
  const bar = page.locator('[data-unsaved-bar]')
  await expect(bar).toContainText('New crew, not saved yet')
  // The bar is drawn on the app's own elevated surface, never the inverted one (#376).
  await expect(bar).toHaveClass(/\bbg-elevated\b/)
  await expect(bar).not.toHaveClass(/\bbg-inverted\b/)
  await page.getByLabel('Crew name').fill('e2e crews page')
  await page.getByLabel('Goal').fill('show the crews pages')
  // The default draft has one member; a second one starts after it.
  await page.locator('[data-add-member]').click()
  const rows = page.locator('[data-crew-member]')
  await expect(rows).toHaveCount(2)
  await rows.nth(0).getByLabel('First prompt for lead').fill('say hello')
  await rows.nth(1).getByLabel(/^Name of/).fill('docs')
  await rows.nth(1).locator('[data-member-start]').click()
  await page.getByRole('option', { name: 'When you press Start' }).click()
  await expect(page.locator('[data-start-sentence]')).toHaveText('docs waits for you; lead starts at once')
  await bar.getByRole('button', { name: 'Save' }).click()
  await expect(page).toHaveURL(/\/crews\/e2e-crews-page$/)
  crewId = 'e2e-crews-page'
  await expect(page.locator('[data-crew-kind]')).toHaveText('Saved crew')
  await expect(bar).toHaveCount(0)
  // The home's table: the plan, its shape, where it runs, and that it never ran.
  await page.goto('/crews')
  const entry = page.locator(`[data-crew-entry][data-crew-id="${crewId}"]`)
  await expect(entry.locator('[data-crew-item]')).toHaveText('e2e crews page')
  await expect(entry).toContainText('show the crews pages')
  await expect(entry.locator('[data-crew-shape]')).toHaveText('2 · 1 by hand, 1 alone')
  await expect(entry.locator('[data-crew-meta]')).toContainText('worktrees')
  await expect(entry.locator('[data-crew-last-run]')).toHaveText('never')
  await expect(entry.locator('[data-crew-live]')).toHaveCount(0)
  await expect(page.locator('[data-running-now]')).toHaveCount(0)
})

test('an edit to the saved crew gets its own bar, which names what changed and discards it', async ({ page, api }) => {
  // At a phone's width: the bar and its buttons stay inside the window (#376).
  await page.setViewportSize({ width: 390, height: 800 })
  await page.goto(`/crews/${crewId}`)
  await page.getByLabel('Goal').fill('show the crews pages, edited')
  const bar = page.locator('[data-unsaved-bar]')
  await expect(bar).toContainText('Unsaved changes to the saved crew')
  await expect(bar.locator('[data-unsaved-words]')).toHaveText('Goal edited.')
  await expect(bar).not.toHaveClass(/\bbg-inverted\b/)
  for (const name of ['Discard', 'Save']) {
    const box = await bar.getByRole('button', { name }).boundingBox()
    expect(box && box.x >= 0 && box.x + box.width <= 390).toBe(true)
  }
  await bar.getByRole('button', { name: 'Discard' }).click()
  await expect(bar).toHaveCount(0)
  await expect(page.getByLabel('Goal')).toHaveValue('show the crews pages')
  expect((await api.crew(crewId)).members.map((m) => m.name)).toEqual(['lead', 'docs'])
})

test('Launch run from the home opens the run page; the crew then says a run is live, and its Runs tab lists it', async ({ page, api }) => {
  await page.goto('/crews')
  await page.locator(`[data-launch-crew="${crewId}"]`).click()
  await page.waitForURL(/\/runs\/e2e-crews-page-[0-9a-f]{8}$/)
  const runId = decodeURIComponent(new URL(page.url()).pathname.split('/').pop() ?? '')
  runs.push(runId)
  // The run page names the run by its crew and its start.
  await expect(page.locator('[data-crew-run-header]')).toContainText('run started')
  await expect.poll(async () => logged(await api.run(runId), "typed lead's prompt"), { timeout: 60_000 }).toBe(true)
  await page.goto('/crews')
  const card = page.locator(`[data-running-now] [data-run="${runId}"]`)
  await expect(card).toHaveAttribute('data-state', 'running')
  await expect(card.locator('[data-run-member="docs"]')).toHaveAttribute('data-status', 'pending')
  await expect(page.locator(`[data-crew-entry][data-crew-id="${crewId}"] [data-crew-live]`)).toHaveText(/1 live run/)
  await page.goto(`/crews/${crewId}`)
  const banner = page.locator('[data-live-run-banner]')
  await expect(banner).toContainText('live run')
  await expect(banner).toContainText('1 running')
  await expect(banner).toContainText('docs waits for Start now')
  // The bar says a live run keeps the version it launched with.
  await page.getByLabel('Goal').fill('changed under a live run')
  await expect(page.locator('[data-unsaved-words]')).toHaveText('Goal edited. The live run keeps the version it launched with.')
  await page.locator('[data-unsaved-bar]').getByRole('button', { name: 'Discard' }).click()
  // Stop from the Runs tab: the row reads stopped, docs never started, and the run can be resumed as a new one.
  await page.getByRole('tab', { name: /Runs/ }).click()
  const row = page.locator(`[data-crew-runs] [data-run="${runId}"]`)
  await expect(row).toHaveAttribute('data-state', 'running')
  await row.locator('[data-run-stop]').click()
  await expect(row).toHaveAttribute('data-state', 'stopped', { timeout: 15_000 })
  await expect(row.locator('[data-run-state]')).toHaveText('stopped')
  await expect(row.locator('[data-run-note]')).toHaveText('1 never started')
  await expect(row.locator('[data-run-member="docs"]')).toHaveAttribute('data-status', 'pending')
  await expect(row.locator('[data-run-resume]')).toHaveText(/Resume as new run/)
  await expect(banner).toHaveCount(0)
  // The filter: the stopped chip keeps the row, the live one hides it, and the choice outlasts a reload.
  await page.locator('[data-runs-state="stopped"]').click()
  await expect(page.locator('[data-runs-state="stopped"]')).toHaveAttribute('data-count', '1')
  await expect(row).toBeVisible()
  await page.locator('[data-runs-state="live"]').click()
  await expect(row).toHaveCount(0)
  await expect(page.locator('[data-runs-filter-reset]')).toBeVisible()
  await page.reload()
  await expect(page.locator('[data-runs-state="live"]')).toHaveAttribute('data-active', 'true')
  await page.locator('[data-runs-filter-reset]').click()
  await expect(page.locator(`[data-crew-runs] [data-run="${runId}"]`)).toBeVisible()
  await expect(page.locator('[data-runs-state="all"]')).toHaveAttribute('data-active', 'true')
})

test('a run named at launch is called by its name on its page, the home card and the Runs row', async ({ page }) => {
  await page.goto(`/crews/${crewId}`)
  await page.locator('[data-launch-menu]').click()
  await page.locator('[data-launch-name]').fill('e2e named run')
  await page.locator('[data-launch-named]').click()
  await page.waitForURL(/\/runs\/e2e-crews-page-[0-9a-f]{8}$/)
  const runId = decodeURIComponent(new URL(page.url()).pathname.split('/').pop() ?? '')
  runs.push(runId)
  await expect(page.locator('[data-crew-run-header] [data-run-subtitle]')).toHaveText('e2e named run')
  await page.goto('/crews')
  await expect(page.locator(`[data-running-now] [data-run="${runId}"] [data-run-subtitle]`)).toHaveText('e2e named run')
  await page.goto(`/crews/${crewId}?tab=runs`)
  await expect(page.locator(`[data-crew-runs] [data-run="${runId}"] [data-run-label]`)).toHaveText('e2e named run')
})
