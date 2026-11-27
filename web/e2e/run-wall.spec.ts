import type { Locator, Page } from '@playwright/test'
import { Api, DAILY, atRest, expect, logged, member, test } from './fixtures'
import { inside, noOverlap, uncut } from './layout'

// A run's Members view (UX3): a wall of the members' tiles alone, the tab named Members, and the run's Activity a column the person
// turns on beside the tiles, kept for the crew and taken by a crew opened for the first time; the tiles laid out as Grid or One
// large, kept for the crew too; a tile that asks shows its question whole above its choices.
test.describe.configure({ mode: 'serial' })

const made: string[] = []
const runs: string[] = []
let first = ''
let second = ''
let sixRun = ''
const SIX = ['lead', 'core', 'review', 'web', 'docs', 'tests']

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
  sixRun = await crewRun(api, `e2e wall six ${tag}`, SIX)
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

// The layouts (R19a): Grid and One large, kept for the crew, a change that moves the tiles and never draws them again.

/** Marks each tile's terminal element, so a later look can tell the same element from one drawn again. */
async function markTerminals(grid: Locator): Promise<void> {
  await grid.locator('[data-session-tile] .terminal-host').evaluateAll((els) => els.forEach((el, i) => ((el as unknown as { e2eMark: number }).e2eMark = i + 1)))
}
async function marksKept(grid: Locator): Promise<number[]> {
  return grid.locator('[data-session-tile] .terminal-host').evaluateAll((els) => els.map((el) => (el as unknown as { e2eMark?: number }).e2eMark ?? 0))
}

/** Every tile is inside the wall or reached by its scroll, and no two overlap. */
async function tilesLaidOut(grid: Locator, n: number): Promise<void> {
  const tiles = grid.locator(':scope > [data-member]')
  await expect(tiles).toHaveCount(n)
  await atRest(grid)
  const g = await grid.evaluate((el) => {
    const r = el.getBoundingClientRect()
    return { left: r.left, right: r.right, top: r.top, bottom: r.top + el.scrollHeight, scrollW: el.scrollWidth, w: el.clientWidth }
  })
  // Nothing sideways: the wall scrolls down only.
  expect(g.scrollW).toBeLessThanOrEqual(g.w + 1)
  const boxes = await tiles.evaluateAll((els, scrollTop) => els.map((el) => {
    const r = el.getBoundingClientRect()
    return { name: el.getAttribute('data-member'), left: r.left, right: r.right, top: r.top + scrollTop, bottom: r.bottom + scrollTop, h: r.height, w: r.width }
  }), await grid.evaluate((el) => el.scrollTop))
  for (const b of boxes) {
    expect(b.left, `${b.name} inside the wall`).toBeGreaterThanOrEqual(g.left - 1)
    expect(b.right, `${b.name} inside the wall`).toBeLessThanOrEqual(g.right + 1)
    expect(b.top, `${b.name} inside the wall`).toBeGreaterThanOrEqual(g.top - 1)
    expect(b.bottom, `${b.name} reached by the wall's scroll`).toBeLessThanOrEqual(g.bottom + 1)
    expect(b.h, `${b.name} at least 140 px tall`).toBeGreaterThanOrEqual(139.5)
  }
  for (const [i, a] of boxes.entries()) {
    for (const b of boxes.slice(i + 1)) {
      const w = Math.min(a.right, b.right) - Math.max(a.left, b.left)
      const h = Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top)
      expect(w > 0.5 && h > 0.5, `${a.name} and ${b.name} overlap`).toBe(false)
    }
  }
}

for (const size of [DAILY, { width: 1440, height: 900 }]) {
  for (const which of ['two', 'six'] as const) {
    test(`at ${size.width} x ${size.height} with ${which} members, Grid and One large lay the tiles out, move them without drawing them again, and are kept`, async ({ page }) => {
      await page.setViewportSize(size)
      const [runId, names] = which === 'two' ? [second, ['solo', 'pair']] : [sixRun, SIX]
      const { grid } = await open(page, runId, names.length)
      const sw = page.locator('[data-run-layout-switch]')
      await expect(sw.locator('[data-run-layout-choice]')).toHaveCount(2)
      await expect(sw.locator('[data-run-layout-choice="grid"]')).toHaveAttribute('aria-pressed', 'true')
      await expect(sw.locator('[data-run-layout-choice="grid"]')).toHaveAccessibleName('Grid')
      await expect(sw.locator('[data-run-layout-choice="large"]')).toHaveAccessibleName('One large')
      await expect(grid).toHaveAttribute('data-run-layout', 'grid')
      await tilesLaidOut(grid, names.length)
      await expect(grid.locator('[data-session-tile] [data-welcomed]')).toHaveCount(names.length, { timeout: 30_000 })
      await markTerminals(grid)

      await sw.locator('[data-run-layout-choice="large"]').click()
      await expect(sw.locator('[data-run-layout-choice="large"]')).toHaveAttribute('aria-pressed', 'true')
      await expect(grid).toHaveAttribute('data-run-layout', 'large')
      await tilesLaidOut(grid, names.length)
      // The first member is large: two thirds of the wall's width; every other tile offers Show large.
      const g = (await grid.boundingBox())!
      const big = (await grid.locator(`:scope > [data-member="${names[0]}"]`).boundingBox())!
      expect(big.width / g.width).toBeGreaterThan(0.6)
      expect(big.width / g.width).toBeLessThan(0.7)
      await expect(grid.locator(`[data-member="${names[0]}"] [data-tile-show-large]`)).toHaveCount(0)
      await expect(grid.locator('[data-tile-show-large]')).toHaveCount(names.length - 1)
      // Moved, never drawn again: the same terminal elements, still welcomed.
      expect(await marksKept(grid)).toEqual(names.map((_, i) => i + 1))
      await expect(grid.locator('[data-session-tile] [data-welcomed]')).toHaveCount(names.length)

      // Show large: the last member takes the large cell.
      const last = names[names.length - 1]!
      const show = grid.locator(`[data-member="${last}"] [data-tile-show-large]`)
      await expect(show).toHaveAccessibleName(`Show ${last} large`)
      await show.click()
      await expect(grid.locator(`[data-member="${last}"] [data-tile-show-large]`)).toHaveCount(0)
      await expect.poll(async () => ((await grid.locator(`:scope > [data-member="${last}"]`).boundingBox())!.width / g.width)).toBeGreaterThan(0.6)
      expect(await marksKept(grid)).toEqual(names.map((_, i) => i + 1))

      // Kept after a reload, with the member drawn large.
      await page.reload()
      await expect(page.locator('[data-run-grid]')).toHaveAttribute('data-run-layout', 'large')
      await expect(page.locator(`[data-run-grid] [data-member="${last}"] [data-tile-show-large]`)).toHaveCount(0)
      await expect(page.locator(`[data-run-grid] [data-member="${names[0]}"] [data-tile-show-large]`)).toHaveCount(1, { timeout: 30_000 })
      // A crew opened for the first time takes the last layout.
      const { grid: other } = await open(page, first, 3)
      await expect(other).toHaveAttribute('data-run-layout', 'large')
      // And Grid again: the tiles fill the wall.
      await page.locator('[data-run-layout-choice="grid"]').click()
      await expect(other).toHaveAttribute('data-run-layout', 'grid')
      await tilesLaidOut(other, 3)
    })
  }
}

test('at 390 x 844 no layout switch is drawn: the tiles stack', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const { grid } = await open(page, sixRun, SIX.length)
  await expect(grid).toHaveAttribute('data-run-layout', 'stacked')
  await expect(page.locator('[data-run-layout-switch]')).toHaveCount(0)
  await expect(page.locator('[data-activity-toggle]')).toBeVisible()
})

/** A question of 300 characters with two choices, asked by a member of the six once its first turn is done. */
const LONG = `Ship the refund split as one pull request, or two: the API first, then the storefront? The API change moves the rounding of the last cent to the first payment, and the storefront shows each payment's share on the order page, ${'so a reviewer reads both at once. '.repeat(3)}`.slice(0, 300)

test('a tile that asks shows its question whole above its choices, in at most half of the tile, scrolling inside past that', async ({ page, api }) => {
  // Its waits: two polls of 30 s and the looks of the config (15 s each, about ten); the timeout is above their sum.
  test.setTimeout(2 * 30_000 + 10 * 15_000 + 30_000)
  let id = ''
  await expect.poll(async () => (id = member(await api.run(sixRun), 'review').sessionId ?? ''), { timeout: 30_000 }).not.toBe('')
  // The stub's first turn ends with its Stop hook, which reports done: a question posted before would be overwritten.
  await expect.poll(async () => (await api.session(id)).attention?.state, { timeout: 30_000 }).toBe('done')
  expect(LONG).toHaveLength(300)
  await api.ok('POST', `/api/sessions/${id}/attention`, {
    state: 'needs_input',
    message: LONG,
    options: [
      { label: 'One pull request', input: '1' },
      { label: 'Two, the API first', input: '2' },
    ],
  })
  // One large with Activity on at 1440: the column's tiles are about 270 px wide and 140 px tall.
  await page.setViewportSize({ width: 1440, height: 900 })
  const { grid, toggle } = await open(page, sixRun, SIX.length)
  await page.locator('[data-run-layout-choice="large"]').click()
  await toggle.click()
  await expect(page.locator('[data-crew-feed]')).toBeVisible()
  const tile = grid.locator(':scope > [data-member="review"]')
  const words = tile.locator('[data-run-tile-question]')
  await expect(words).toHaveText(`review asks: ${LONG.trim()}`)
  const choices = tile.locator('[data-run-ask-choices] [data-run-ask-choice]')
  await expect(choices).toHaveCount(2)
  await atRest(tile)
  const w = (await tile.boundingBox())!.width
  expect(w).toBeGreaterThan(240)
  expect(w).toBeLessThan(300)
  // At most half of the tile, and the rest is reached by its own scroll.
  const bar = tile.locator('[data-tile-ask-bar]')
  const m = await bar.evaluate((el) => ({ h: el.clientHeight, sh: el.scrollHeight, tile: (el.closest('[data-session-tile]') as HTMLElement).clientHeight }))
  expect(m.h).toBeLessThanOrEqual(m.tile / 2 + 1)
  expect(m.sh).toBeGreaterThan(m.h)
  // Whole: the question's text is all there, never cut with an ellipsis, and every choice's label is whole once scrolled to.
  expect(await words.evaluate((el) => getComputedStyle(el).textOverflow)).not.toBe('ellipsis')
  await bar.evaluate((el) => (el.scrollTop = el.scrollHeight))
  await expect(choices.nth(1)).toBeInViewport()
  await uncut(tile.locator('[data-run-ask-choices]'))
  // On the wide window in Grid, the bar holds it all without a scroll: the question above its choices.
  await page.setViewportSize(DAILY)
  await page.locator('[data-run-layout-choice="grid"]').click()
  await toggle.click()
  await atRest(grid)
  const q = (await words.boundingBox())!
  const c = (await choices.first().boundingBox())!
  expect(q.y + q.height).toBeLessThanOrEqual(c.y + 0.5)
})
