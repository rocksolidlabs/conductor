import { dayPage, expect, logged, test } from './fixtures'
import { inside, noSideways, probeColour } from './layout'
import { ledgerRun, serveRun } from './run-fixtures'

// The Crews pages tell a saved crew (a plan) from a run (one launch of it):
// the home lists the runs going now above the saved crews; a crew's page has
// its Setup and its Runs, says when a run of it is live, and keeps unsaved
// edits in a bar of their own; a run is named by its crew and its start.
test.describe.configure({ mode: 'serial' })

let crewId = ''
/** The crew's name, of this repetition's own: a run of an earlier one, kept as a record, is not this crew's. */
let crewName = ''
const runs: string[] = []
const made: string[] = []

test.afterAll(async ({ api }) => {
  for (const id of runs) await api.stopRun(id)
  for (const id of [crewId, ...made]) if (id) await api.call('DELETE', `/api/crews/${encodeURIComponent(id)}`)
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
  crewName = `e2e crews page ${Array.from({ length: 6 }, () => 'abcdefghijklmnopqrstuvwxyz'[Math.floor(Math.random() * 26)]).join('')}`
  await page.getByLabel('Crew name').fill(crewName)
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
  crewId = crewName.replaceAll(' ', '-')
  await expect(page).toHaveURL(new RegExp(`/crews/${crewId}$`))
  await expect(page.locator('[data-crew-kind]')).toHaveText('Saved crew')
  await expect(bar).toHaveCount(0)
  // The home's table: the plan, its members and their agents, and that it never ran (C4).
  await page.goto('/crews')
  const entry = page.locator(`[data-crew-entry][data-crew-id="${crewId}"]`)
  await expect(entry.locator('[data-crew-item]')).toHaveText(crewName)
  await expect(entry).toContainText('show the crews pages')
  await expect(entry.locator('[data-crew-members]')).toHaveText(/^2 members · /)
  await expect(entry.locator('[data-crew-last-run]')).toHaveText('Never run')
  await expect(entry).toHaveAttribute('data-live-runs', '0')
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
  // A launch asks first, each member on its line (docs/plans/capacity.md, PR-7); its Launch launches.
  await expect(page.locator('[data-crew-launch-line="lead"]')).toBeVisible()
  await page.locator('[data-crew-launch-confirm]').click()
  await page.waitForURL(new RegExp(`/runs/${crewId}-[0-9a-f]{8}$`))
  const runId = decodeURIComponent(new URL(page.url()).pathname.split('/').pop() ?? '')
  runs.push(runId)
  // The run page names the run by its crew and its start.
  await expect(page.locator('[data-crew-run-header]')).toContainText('run started')
  await expect.poll(async () => logged(await api.run(runId), "typed lead's prompt"), { timeout: 60_000 }).toBe(true)
  await page.goto('/crews')
  const card = page.locator(`[data-running-now] [data-run="${runId}"]`)
  await expect(card).toHaveAttribute('data-state', 'running')
  await expect(card.locator('[data-run-member="docs"]')).toHaveAttribute('data-status', 'pending')
  await expect(page.locator(`[data-crew-entry][data-crew-id="${crewId}"]`)).toHaveAttribute('data-live-runs', '1')
  await expect(page.locator(`[data-crew-entry][data-crew-id="${crewId}"] [data-crew-last-state]`)).toHaveText('Running')
  await page.goto(`/crews/${crewId}`)
  const banner = page.locator('[data-live-run-banner]')
  await expect(banner).toContainText('live run')
  // The banner says the run in the Crews pages' one vocabulary (runLook): its state's word, then lead counted by its session's state
  // in the member words (a member at its prompt is idle, never "running"), docs not yet started counted in none, nothing that needs a
  // person; no member's name in amber.
  await expect(banner.locator('[data-live-run-state]')).toContainText('Running')
  await expect(banner.locator('[data-live-run-line]')).toHaveText(/^1 (working|finished|idle)$/)
  await expect(banner).not.toContainText(/\brunning\b/)
  await expect(banner.locator('.text-warning')).toHaveCount(0)
  await expect(banner).not.toHaveClass(/border-warning/)
  // The bar says a live run keeps the version it launched with.
  await page.getByLabel('Goal').fill('changed under a live run')
  await expect(page.locator('[data-unsaved-words]')).toHaveText('Goal edited. The live run keeps the version it launched with.')
  await page.locator('[data-unsaved-bar]').getByRole('button', { name: 'Discard' }).click()
  // Stop from the Runs tab: the row reads Stopped, docs never started, and the run can be resumed as a new one.
  await page.getByRole('tab', { name: /Runs/ }).click()
  const row = page.locator(`[data-crew-runs] [data-run="${runId}"]`)
  await expect(row).toHaveAttribute('data-state', 'running')
  await row.locator('[data-run-stop]').click()
  await expect(row).toHaveAttribute('data-state', 'stopped', { timeout: 15_000 })
  await expect(row.locator('[data-run-state]')).toHaveText('Stopped')
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
  await expect(page.getByRole('dialog')).toContainText('As the run e2e named run')
  await page.locator('[data-crew-launch-confirm]').click()
  await page.waitForURL(new RegExp(`/runs/${crewId}-[0-9a-f]{8}$`))
  const runId = decodeURIComponent(new URL(page.url()).pathname.split('/').pop() ?? '')
  runs.push(runId)
  await expect(page.locator('[data-crew-run-header] [data-run-subtitle]')).toHaveText('e2e named run')
  await page.goto('/crews')
  await expect(page.locator(`[data-running-now] [data-run="${runId}"] [data-run-subtitle]`)).toHaveText('e2e named run')
  await page.goto(`/crews/${crewId}?tab=runs`)
  await expect(page.locator(`[data-crew-runs] [data-run="${runId}"] [data-run-label]`)).toHaveText('e2e named run')
})

test("a comparison's crew edited and saved in the editor keeps its compare block", async ({ page, api }) => {
  const worker = (name: string) => ({ name, agentId: 'worker', prompt: '', start: { when: 'task' } })
  const approach = (id: string, member: string) => ({ id, member, kind: 'work', instructions: 'Make it fast.', checks: [{ id: 'ok', argv: ['true'] }] })
  const compare = {
    criteria: [{ id: 'fast', text: 'It is fast.' }],
    candidates: [{ task: 'approach-a', approach: 'Cache the parse.' }, { task: 'approach-b' }],
  }
  const body = {
    name: `e2e comparison ${Date.now().toString(36)}`,
    goal: 'compare two ways',
    cwd: '',
    where: 'server',
    isolation: 'worktree',
    openAfterLaunch: false,
    members: [worker('cand-a'), worker('cand-b')],
    tasks: [approach('approach-a', 'cand-a'), approach('approach-b', 'cand-b')],
    compare,
  }
  const id = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', body)).crew.id
  made.push(id)
  await page.goto(`/crews/${encodeURIComponent(id)}`)
  // The editor has drawn the crew as read.
  const goal = page.getByLabel('Goal')
  await expect(goal).toHaveValue('compare two ways')
  await goal.fill('compare two ways, edited')
  const bar = page.locator('[data-unsaved-bar]')
  await expect(bar.locator('[data-unsaved-words]')).toHaveText('Goal edited.')
  const saved = page.waitForResponse((r) => r.request().method() === 'PUT' && new URL(r.url()).pathname === `/api/crews/${encodeURIComponent(id)}`)
  await bar.getByRole('button', { name: 'Save' }).click()
  expect((await saved).status()).toBe(200)
  await expect(bar).toHaveCount(0)
  const { crew: read } = await api.ok<{ crew: { goal: string; compare?: unknown } }>('GET', `/api/crews/${encodeURIComponent(id)}`)
  expect(read.goal).toBe('compare two ways, edited')
  expect(read.compare).toEqual(compare)
})

// A live run's card reads without a legend (C5): its state's word beside its mark, amber for a run that needs a person, then each
// member as a line with its state's word: the Ledger stub served as live, two of its tasks held by a decision.
for (const theme of ['light', 'dark'] as const) {
  test(`a live run's card says its state and each member's in words; in the ${theme} theme a run that needs you is amber`, async ({ day, browser }) => {
    const run = ledgerRun()
    const page = await dayPage(browser, { path: '/', theme })
    await serveRun(page, run)
    await page.goto('/crews')
    const card = page.locator(`[data-running-now] [data-run="${run.id}"]`)
    const state = card.locator('[data-run-state]')
    await expect(state).toHaveAttribute('data-phase', 'needs')
    await expect(state.locator('[data-status-word]').first()).toHaveText('Needs you')
    const fill = await state.locator('[data-status-glyph] circle').first().evaluate((c) => getComputedStyle(c).fill)
    expect(await probeColour(page, fill)).toBe(await probeColour(page, 'var(--ui-warning)'))
    await expect(card.locator('[data-run-member]')).toHaveCount(run.members.length)
    for (const m of run.members) await expect(card.locator(`[data-run-member="${m.name}"] [data-status-word]`)).toHaveText(/^(Working|Idle|Needs you|Waiting to start|Finished|Failed|Stopped)$/)
    void day
  })
}

test("at 390 with touch a live run's member lines lie inside its card, and nothing scrolls sideways", async ({ day, browser }) => {
  const run = ledgerRun()
  const page = await dayPage(browser, { path: '/', width: 390, height: 844, touch: true })
  await serveRun(page, run)
  await page.goto('/crews')
  const card = page.locator(`[data-running-now] [data-run="${run.id}"]`)
  await expect(card.locator('[data-run-member]')).toHaveCount(run.members.length)
  await inside(card.locator('[data-run-members]'), card)
  await inside(card.locator('[data-run-member] [data-status-glyph]'), card)
  await noSideways(page)
  void day
})
