import type { Locator, Page } from '@playwright/test'
import { Api, DAILY, atRest, expect, logged, test } from './fixtures'
import { inside, noOverlap } from './layout'

// A run's Members view (UX3): a wall of the members' tiles alone, the tab named Members, and the run's Activity a column the person
// turns on beside the tiles, kept for the crew and taken by a crew opened for the first time.
test.describe.configure({ mode: 'serial' })

const made: string[] = []
const runs: string[] = []
let first = ''
let second = ''

async function crewRun(api: Api, name: string, members: string[]): Promise<string> {
  const id = (
    await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
      name,
      goal: 'the run wall',
      cwd: '',
      where: 'server',
      isolation: 'none',
      openAfterLaunch: false,
      members: members.map((m) => ({ name: m, agentId: 'claude', prompt: `say ${m}`, start: { when: 'immediately' } })),
    })
  ).crew.id
  made.push(id)
  const run = await api.launchCrew(id)
  runs.push(run.id)
  return run.id
}

test.beforeAll(async ({ api }) => {
  const tag = Date.now().toString(36)
  first = await crewRun(api, `e2e wall one ${tag}`, ['lead', 'core', 'review'])
  second = await crewRun(api, `e2e wall two ${tag}`, ['solo', 'pair'])
  for (const id of runs) await expect.poll(async () => logged(await api.run(id), 'typed'), { timeout: 60_000 }).toBe(true)
})

test.afterAll(async ({ api }) => {
  for (const id of runs) await api.stopRun(id)
  for (const id of made) await api.call('DELETE', `/api/crews/${encodeURIComponent(id)}`)
})

async function open(page: Page, runId: string, tiles: number): Promise<{ grid: Locator; toggle: Locator }> {
  await page.goto(`/runs/${encodeURIComponent(runId)}`)
  const grid = page.locator('[data-run-grid]')
  await expect(grid.locator('[data-session-tile]')).toHaveCount(tiles, { timeout: 30_000 })
  return { grid, toggle: page.locator('[data-activity-toggle]') }
}

const box = async (l: Locator) => (await l.boundingBox())!

for (const size of [DAILY, { width: 1440, height: 900 }, { width: 390, height: 844 }]) {
  test(`at ${size.width} x ${size.height} the tab reads Members, the tiles fill the wall, and Activity is a column turned on and kept`, async ({ page }) => {
    await page.setViewportSize(size)
    const { grid, toggle } = await open(page, first, 3)
    const views = page.locator('[data-run-view]')
    await expect(views.getByRole('tab', { name: 'Members' })).toHaveAttribute('aria-selected', 'true')
    await expect(views.getByRole('tab', { name: 'Grid' })).toHaveCount(0)
    // Off at first: no feed, the tiles have the area to themselves.
    await expect(toggle).toHaveText('Activity')
    await expect(toggle).toHaveAttribute('aria-pressed', 'false')
    await expect(page.locator('[data-crew-feed]')).toHaveCount(0)
    await expect(page.getByText('Crew activity')).toHaveCount(0)
    const tiles = grid.locator('[data-session-tile]')
    await inside(tiles, grid)
    // One press: the column, headed Activity, beside the tiles (under them on a phone), the tiles still inside their own area.
    await toggle.click()
    await expect(toggle).toHaveAttribute('aria-pressed', 'true')
    const feed = page.locator('[data-crew-feed]')
    await expect(feed).toBeVisible()
    await expect(feed.getByRole('heading', { name: 'Activity' })).toBeVisible()
    await atRest(feed)
    const g = await box(grid)
    const f = await box(feed)
    if (size.width >= 640) {
      expect(Math.round(f.width)).toBe(320)
      expect(f.x).toBeGreaterThanOrEqual(g.x + g.width)
      expect(Math.abs(f.y - g.y)).toBeLessThan(1)
    } else {
      expect(f.y).toBeGreaterThanOrEqual(g.y + g.height)
    }
    await inside(tiles, grid)
    await noOverlap(page.locator('[data-run-view]').locator('..'))
    // Kept after a reload, and taken by a crew opened for the first time.
    await page.reload()
    await expect(page.locator('[data-activity-toggle]')).toHaveAttribute('aria-pressed', 'true')
    await expect(page.locator('[data-crew-feed]')).toBeVisible()
    await open(page, second, 2)
    await expect(page.locator('[data-activity-toggle]')).toHaveAttribute('aria-pressed', 'true')
    await expect(page.locator('[data-crew-feed]')).toBeVisible()
    // Off for this crew alone: the first keeps its own.
    await page.locator('[data-activity-toggle]').click()
    await expect(page.locator('[data-crew-feed]')).toHaveCount(0)
    await open(page, first, 3)
    await expect(page.locator('[data-crew-feed]')).toBeVisible()
  })
}

test("the tiles alone fill the wall: the last one takes its row's empty cells", async ({ page }) => {
  await page.setViewportSize(DAILY)
  const { grid } = await open(page, first, 3)
  const tiles = grid.locator('[data-session-tile]')
  await atRest(grid)
  const g = await box(grid)
  const last = await box(tiles.nth(2))
  // Three members on the wide window are one row: the last tile ends at the wall's right edge.
  expect(Math.abs(last.x + last.width - (g.x + g.width))).toBeLessThan(2)
  expect(Math.abs(last.y + last.height - (g.y + g.height))).toBeLessThan(2)
})

test("Activity's link opens this run in History", async ({ page }) => {
  const { toggle } = await open(page, first, 3)
  if ((await toggle.getAttribute('aria-pressed')) !== 'true') await toggle.click()
  const link = page.locator('[data-crew-feed-history]')
  await expect(link).toHaveText('This run in History')
  await link.click()
  await expect(page).toHaveURL(new RegExp(`/history\\?q=run(:|%3A)${first}$`))
})

test('the tile’s open button says Full size', async ({ page }) => {
  const { grid } = await open(page, first, 3)
  const button = grid.locator('[data-member="lead"] [data-tile-open]')
  await button.hover()
  await expect(page.locator('[data-slot="content"]').filter({ hasText: 'Full size (or double-click here)' })).toBeVisible()
  await expect(page.getByText('Open in full')).toHaveCount(0)
})
