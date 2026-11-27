import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Locator, Page } from '@playwright/test'
import { Api, DAILY, atRest, dayPage, expect, logged, member, test } from './fixtures'
import { stubInbox } from './inbox-stub'
import { inside, noOverlap, noSideways, uncut } from './layout'
import { ledgerRun, serveRun } from './run-fixtures'

// A run's Members view (UX3): a wall of the members' tiles alone, the tab named Members, and the run's Activity a column the person
// turns on beside the tiles, kept for the crew and taken by a crew opened for the first time; the tiles laid out as Grid, Columns,
// Rows or One large, and the gaps between them dragged, kept for the crew too; a tile that asks shows its question whole above its
// choices or says where it is answered, and a number key on its frame answers it.
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

/** The members whose terminal the server has welcomed. */
function welcomedNames(grid: Locator): Promise<string[]> {
  return grid.locator(':scope > [data-member]').evaluateAll((els) => els.filter((e) => e.querySelector('[data-welcomed]')).map((e) => e.getAttribute('data-member') ?? ''))
}

/** Every tile is inside the wall or reached by its scroll, and no two overlap. */
async function tilesLaidOut(grid: Locator, n: number, scroll: 'x' | 'y' = 'y'): Promise<void> {
  const tiles = grid.locator(':scope > [data-member]')
  await expect(tiles).toHaveCount(n)
  await atRest(grid)
  // The wall's content, where its scroll reaches, in the window's coordinates as they are now.
  const g = await grid.evaluate((el) => {
    const r = el.getBoundingClientRect()
    const left = r.left - el.scrollLeft
    const top = r.top - el.scrollTop
    return { left, top, right: left + el.scrollWidth, bottom: top + el.scrollHeight, scrollW: el.scrollWidth, w: el.clientWidth }
  })
  // Nothing sideways but in Columns: the wall scrolls down.
  if (scroll === 'y') expect(g.scrollW).toBeLessThanOrEqual(g.w + 1)
  const boxes = await tiles.evaluateAll((els) =>
    els.map((el) => {
      const r = el.getBoundingClientRect()
      return { name: el.getAttribute('data-member'), left: r.left, right: r.right, top: r.top, bottom: r.bottom, h: r.height, w: r.width }
    }),
  )
  for (const b of boxes) {
    expect(b.left, `${b.name} inside the wall`).toBeGreaterThanOrEqual(g.left - 1)
    expect(b.right, `${b.name} reached by the wall's scroll`).toBeLessThanOrEqual(g.right + 1)
    expect(b.top, `${b.name} inside the wall`).toBeGreaterThanOrEqual(g.top - 1)
    expect(b.bottom, `${b.name} reached by the wall's scroll`).toBeLessThanOrEqual(g.bottom + 1)
    expect(b.h, `${b.name} at least 140 px tall`).toBeGreaterThanOrEqual(139.5)
    if (scroll === 'x') expect(b.w, `${b.name} at least 260 px wide`).toBeGreaterThanOrEqual(259.5)
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
      await expect(sw.locator('[data-run-layout-choice]')).toHaveCount(4)
      await expect(sw.locator('[data-run-layout-choice="grid"]')).toHaveAttribute('aria-pressed', 'true')
      await expect(sw.locator('[data-run-layout-choice="grid"]')).toHaveAccessibleName('Grid')
      await expect(sw.locator('[data-run-layout-choice="large"]')).toHaveAccessibleName('One large')
      await expect(grid).toHaveAttribute('data-run-layout', 'grid')
      await tilesLaidOut(grid, names.length)
      // Every terminal drawn, and marked: a tile drawn again would be a new element without the mark, its connection a new one.
      await expect(grid.locator('[data-session-tile] .terminal-host')).toHaveCount(names.length)
      await markTerminals(grid)
      const was = await welcomedNames(grid)

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
      // Every connection welcomed before the change is welcomed still: none was made again.
      expect(await welcomedNames(grid)).toEqual(expect.arrayContaining(was))

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

// The last layout and Activity are the layout document's too (UX3 R88): the desktop app's server starts on a fresh address at each
// start, and what a page stores belongs to one address, so the server's copy brings them back.
const TILE_KEYS = ['conductor.runTiles', 'conductor.runTilesLast', 'conductor.ui.unsent']

test("the last layout and Activity come back from the server when the page's storage has lost them", async ({ page, api }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const { grid, toggle } = await open(page, second, 2)
  await page.locator('[data-run-layout-choice="large"]').click()
  await toggle.click()
  await expect(toggle).toHaveAttribute('aria-pressed', 'true')
  await expect(grid).toHaveAttribute('data-run-layout', 'large')
  await expect.poll(async () => (await api.ok<{ runTiles?: unknown }>('GET', '/api/ui/layout')).runTiles, { timeout: 15_000, message: 'the server keeps the choice' }).toMatchObject({ layout: 'large', activity: true })
  // The page's storage as a fresh address has it: no tiles record, no last choice.
  await page.evaluate((keys) => keys.forEach((k) => localStorage.removeItem(k)), TILE_KEYS)
  const read = page.waitForResponse((r) => r.url().endsWith('/api/ui/layout') && r.request().method() === 'GET')
  await page.reload()
  await read
  await expect(page.locator('[data-run-grid]')).toHaveAttribute('data-run-layout', 'large')
  await expect(page.locator('[data-activity-toggle]')).toHaveAttribute('aria-pressed', 'true')
  await expect(page.locator('[data-crew-feed]')).toBeVisible()
})

/** Goes to `path` inside the app, as a link does: the page is not loaded again, so what it holds in its memory stays. */
function within(page: Page, path: string) {
  return page.evaluate((u) => (document.querySelector('#__nuxt') as unknown as { __vue_app__: { config: { globalProperties: { $router: { push: (u: string) => Promise<unknown> } } } } }).__vue_app__.config.globalProperties.$router.push(u), path)
}

test("two windows of one app: a crew's choice made in the second survives a trip and a save in the first", async ({ page, context, api }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  // The first window has a choice of its own when it starts: Activity on for crew one, so its memory holds a list of crews.
  const one = await open(page, first, 3)
  await one.toggle.click()
  await expect(one.toggle).toHaveAttribute('aria-pressed', 'true')
  // The second window: crew two drawn One large.
  const second2 = await context.newPage()
  await second2.setViewportSize({ width: 1440, height: 900 })
  await open(second2, second, 2)
  await second2.locator('[data-run-layout-choice="large"]').click()
  await expect(second2.locator('[data-run-grid]')).toHaveAttribute('data-run-layout', 'large')
  const crewTwo = (await api.run(second)).crewId
  // Back in the first window, without a reload: crew two is One large there too.
  await within(page, `/runs/${encodeURIComponent(second)}`)
  await expect(page.locator('[data-run-grid] [data-session-tile]')).toHaveCount(2, { timeout: 30_000 })
  await expect(page.locator('[data-run-grid]')).toHaveAttribute('data-run-layout', 'large')
  // A save from the first window (its view) keeps crew two's choice, on the server and in the storage the windows share.
  await page.locator('[data-run-view]').getByRole('tab', { name: 'Graph' }).click()
  await expect
    .poll(async () => {
      const l = await api.ok<{ runView?: string; runTiles?: { crews?: Array<{ crew: string; layout: string }> } }>('GET', '/api/ui/layout')
      return `${l.runView} ${l.runTiles?.crews?.find((c) => c.crew === crewTwo)?.layout}`
    }, { timeout: 15_000, message: "the first window's save carries crew two's choice" })
    .toBe('graph large')
  const stored = await page.evaluate(() => localStorage.getItem('conductor.runTiles') ?? '')
  expect((JSON.parse(stored) as { crews: Array<{ crew: string; layout: string }> }).crews.find((c) => c.crew === crewTwo)?.layout).toBe('large')
  await second2.close()
})

test("a crew's own layout comes back from the server too, beside a different last choice", async ({ page, api }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  // second: One large, its own; then first: Grid, the last choice made anywhere.
  await open(page, second, 2)
  await page.locator('[data-run-layout-choice="large"]').click()
  await expect(page.locator('[data-run-grid]')).toHaveAttribute('data-run-layout', 'large')
  await open(page, first, 3)
  await page.locator('[data-run-layout-choice="grid"]').click()
  await expect(page.locator('[data-run-grid]')).toHaveAttribute('data-run-layout', 'grid')
  await expect
    .poll(async () => {
      const t = (await api.ok<{ runTiles?: { layout?: string; crews?: Array<{ layout: string }> } }>('GET', '/api/ui/layout')).runTiles
      return `${t?.layout} ${t?.crews?.map((c) => c.layout).join(',')}`
    }, { timeout: 15_000, message: 'the server keeps both crews and the last choice' })
    .toBe('grid grid,large')
  await page.evaluate((keys) => keys.forEach((k) => localStorage.removeItem(k)), TILE_KEYS)
  const read = page.waitForResponse((r) => r.url().endsWith('/api/ui/layout') && r.request().method() === 'GET')
  await page.goto(`/runs/${encodeURIComponent(second)}`)
  await read
  await expect(page.locator('[data-run-grid]')).toHaveAttribute('data-run-layout', 'large')
})

test('a phone-width window keeps its Activity to itself and writes no layout for the server', async ({ page, api }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const { toggle } = await open(page, second, 2)
  await toggle.click()
  await expect(toggle).toHaveAttribute('aria-pressed', 'true')
  // A desk's window marks a change for the server as it makes it (useUiState's setLayout); a phone's never does.
  expect(await page.evaluate(() => localStorage.getItem('conductor.ui.unsent') ?? '')).not.toContain('layout')
  expect(await page.evaluate(() => localStorage.getItem('conductor.runTilesLast'))).toContain('"activity":true')
  expect((await api.ok<{ runTiles?: unknown }>('GET', '/api/ui/layout')).runTiles).toBeUndefined()
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

// Columns and Rows (R19b): one row that scrolls sideways past 260 px a tile, one column that scrolls down past 140 px a tile.
for (const size of [DAILY, { width: 1440, height: 900 }]) {
  for (const which of ['two', 'six'] as const) {
    test(`at ${size.width} x ${size.height} with ${which} members, Columns and Rows lay the tiles out and are kept`, async ({ page }) => {
      await page.setViewportSize(size)
      const [runId, names] = which === 'two' ? [second, ['solo', 'pair']] : [sixRun, SIX]
      const { grid } = await open(page, runId, names.length)
      const sw = page.locator('[data-run-layout-switch]')
      await expect(sw.locator('[data-run-layout-choice]')).toHaveCount(4)
      expect(await sw.locator('[data-run-layout-choice]').evaluateAll((els) => els.map((e) => e.getAttribute('aria-label')))).toEqual(['Grid', 'Columns', 'Rows', 'One large'])
      // Every terminal drawn, and marked: a tile drawn again would be a new element without the mark, its connection a new one.
      await expect(grid.locator('[data-session-tile] .terminal-host')).toHaveCount(names.length)
      await markTerminals(grid)
      const was = await welcomedNames(grid)
      await sw.locator('[data-run-layout-choice="columns"]').click()
      await expect(grid).toHaveAttribute('data-run-layout', 'columns')
      await tilesLaidOut(grid, names.length, 'x')
      const tops = await grid.locator(':scope > [data-member]').evaluateAll((els) => els.map((e) => Math.round(e.getBoundingClientRect().top)))
      expect(new Set(tops).size, 'one row').toBe(1)
      expect(await marksKept(grid)).toEqual(names.map((_, i) => i + 1))
      await sw.locator('[data-run-layout-choice="rows"]').click()
      await expect(grid).toHaveAttribute('data-run-layout', 'rows')
      await tilesLaidOut(grid, names.length)
      const lefts = await grid.locator(':scope > [data-member]').evaluateAll((els) => els.map((e) => Math.round(e.getBoundingClientRect().left)))
      expect(new Set(lefts).size, 'one column').toBe(1)
      expect(await marksKept(grid)).toEqual(names.map((_, i) => i + 1))
      // Every connection welcomed before the change is welcomed still: none was made again.
      expect(await welcomedNames(grid)).toEqual(expect.arrayContaining(was))
      await page.reload()
      await expect(page.locator('[data-run-grid]')).toHaveAttribute('data-run-layout', 'rows')
    })
  }
}

// The gaps (R21).
test('a drag of a gap changes the tracks once, at its release; the keys move it; a lost drag changes nothing; sizes are kept', async ({ page }) => {
  test.setTimeout(20 * 15_000 + 30_000)
  await page.setViewportSize(DAILY)
  const { grid } = await open(page, first, 3)
  await expect(grid.locator('[data-session-tile] [data-welcomed]')).toHaveCount(3, { timeout: 30_000 })
  const gutter = grid.locator('[data-run-gutter="x"]').first()
  await expect(grid.locator('[data-run-gutter="x"]')).toHaveCount(2)
  await expect(gutter).toHaveAttribute('role', 'separator')
  await expect(gutter).toHaveAttribute('aria-orientation', 'vertical')
  const widths = () => grid.locator(':scope > [data-member]').evaluateAll((els) => els.map((e) => Math.round(e.getBoundingClientRect().width)))
  const fits = () => grid.locator('.terminal-host').evaluateAll((els) => els.map((e) => [Number(e.getAttribute('data-fit-passes')), Number(e.getAttribute('data-fit-resizes'))]))
  await atRest(grid)
  const before = await widths()
  // The fits settle before the drag: read twice an interval apart.
  // The fits settle before the drag: two reads an interval apart agree.
  let f0 = await fits()
  await expect
    .poll(
      async () => {
        const a = await fits()
        await page.waitForTimeout(600)
        f0 = await fits()
        return JSON.stringify(a) === JSON.stringify(f0)
      },
      { timeout: 30_000 },
    )
    .toBe(true)
  const g = (await gutter.boundingBox())!
  await page.mouse.move(g.x + g.width / 2, g.y + 100)
  await page.mouse.down()
  for (let i = 1; i <= 10; i++) await page.mouse.move(g.x + g.width / 2 + i * 12, g.y + 100)
  // While the pointer is held, the line follows it and nothing else moves.
  await expect(gutter).toHaveAttribute('data-dragging', 'true')
  expect(await widths()).toEqual(before)
  await page.mouse.up()
  await expect.poll(widths).not.toEqual(before)
  const after = await widths()
  expect(after[0]! - before[0]!).toBeGreaterThan(100)
  // The two terminals beside the gap were fitted for the drag's one change, each with one resize at most; the third kept its size.
  await expect.poll(async () => (await fits()).slice(0, 2).every(([p], i) => p > f0[i]![0]!), { timeout: 15_000 }).toBe(true)
  for (const [i, [, r]] of (await fits()).entries()) expect(r - f0[i]![1]!).toBeLessThanOrEqual(1)
  // The keys: Right moves the gap by 5 %, Home makes the two even.
  await gutter.focus()
  const v = Number(await gutter.getAttribute('aria-valuenow'))
  await page.keyboard.press('ArrowRight')
  await expect.poll(async () => Number(await gutter.getAttribute('aria-valuenow'))).toBeGreaterThan(v)
  await page.keyboard.press('Home')
  await expect.poll(async () => Number(await gutter.getAttribute('aria-valuenow'))).toBe(50)
  // A drag the pointer loses (the window's blur) ends with nothing changed.
  await atRest(grid)
  const kept = await widths()
  const g2 = (await gutter.boundingBox())!
  await page.mouse.move(g2.x + g2.width / 2, g2.y + 100)
  await page.mouse.down()
  await page.mouse.move(g2.x + g2.width / 2 + 80, g2.y + 100)
  await expect(gutter).toHaveAttribute('data-dragging', 'true')
  await page.evaluate(() => window.dispatchEvent(new Event('blur')))
  await expect(gutter).not.toHaveAttribute('data-dragging', 'true')
  await page.mouse.up()
  await atRest(grid)
  expect(await widths()).toEqual(kept)
  // Kept after a reload.
  await page.keyboard.press('Tab')
  await gutter.focus()
  await page.keyboard.press('ArrowLeft')
  await expect.poll(async () => Number(await gutter.getAttribute('aria-valuenow'))).toBeLessThan(50)
  await atRest(grid)
  const saved = await widths()
  await page.reload()
  await expect(page.locator('[data-run-grid] [data-session-tile]')).toHaveCount(3, { timeout: 30_000 })
  await atRest(page.locator('[data-run-grid]'))
  await expect.poll(() => page.locator('[data-run-grid] > [data-member]').evaluateAll((els) => els.map((e) => Math.round(e.getBoundingClientRect().width)))).toEqual(saved)
})

// Number keys on a tile's frame (R22).
test("a number key on an asking tile's frame answers it; in its terminal the key is the agent's; a mode choice has no number", async ({ page, api, state }) => {
  test.setTimeout(4 * 30_000 + 10 * 15_000 + 30_000)
  const run = await api.run(first)
  const id = member(run, 'core').sessionId ?? ''
  const s = await api.session(id)
  const transcript = () => {
    const file = join(state.home, '.stub-sessions', `${s.agentSession?.id ?? ''}.txt`)
    return existsSync(file) ? readFileSync(file, 'utf8') : ''
  }
  await expect.poll(async () => (await api.session(id)).attention?.state, { timeout: 30_000 }).toBe('done')
  await api.ok('POST', `/api/sessions/${id}/attention`, {
    state: 'needs_input',
    message: 'Which branch?',
    options: [
      { label: 'Yes, and use auto mode', input: 'yolo\r', mode: true },
      { label: 'main', input: 'main\r' },
      { label: 'release', input: 'release\r' },
    ],
  })
  const { grid } = await open(page, first, 3)
  const tile = grid.locator(':scope > [data-member="core"] [data-session-tile]')
  await expect(tile.locator('[data-run-ask-choice]')).toHaveCount(3)
  // The mode choice has no number: main is 2, release 3, as the choices draw them.
  await expect(tile.locator('[data-run-ask-choice="1"] kbd')).toHaveCount(0)
  // In its terminal, a digit is the agent's.
  await tile.locator('.xterm-screen').click()
  await page.keyboard.press('7')
  await expect(tile.locator('[data-run-ask-choice]')).toHaveCount(3)
  expect(transcript()).not.toMatch(/main|release/)
  // On the frame, 1 is the mode choice's place, which has no number: it answers nothing. Then 3 answers release: had 1 answered
  // (auto mode), the question would be gone and 3 refused, so release in the transcript and no yolo prove 1 sent nothing.
  await tile.focus()
  await page.keyboard.press('1')
  await expect(tile.locator('[data-run-ask-choice]')).toHaveCount(3)
  await page.keyboard.press('3')
  await expect.poll(transcript, { timeout: 30_000, message: 'core got the choice from the frame' }).toContain('release')
  await expect(tile.locator('[data-run-ask-choices]')).toHaveCount(0)
  expect(transcript()).not.toContain('yolo')
  // A held key answers once: the next question appears under the same focus, and the key's repeat answers nothing. Had the
  // repeat (1, alpha) answered it, the fresh press (2, beta) would be refused: beta in the transcript and no alpha prove it.
  await expect.poll(async () => (await api.session(id)).attention?.state, { timeout: 30_000, message: 'the answer turn is done' }).toBe('done')
  await api.ok('POST', `/api/sessions/${id}/attention`, {
    state: 'needs_input',
    message: 'Which one next?',
    options: [
      { label: 'alpha', input: 'alpha\r' },
      { label: 'beta', input: 'beta\r' },
    ],
  })
  await expect(tile.locator('[data-run-ask-choice]')).toHaveCount(2)
  await tile.focus()
  await tile.dispatchEvent('keydown', { key: '1', code: 'Digit1', repeat: true, bubbles: true, cancelable: true })
  await expect(tile.locator('[data-run-ask-choice]')).toHaveCount(2)
  await page.keyboard.press('2')
  await expect.poll(transcript, { timeout: 30_000, message: 'core got beta from the fresh press' }).toContain('beta')
  expect(transcript()).not.toContain('alpha')
  // Enter on the frame still opens the member: in the run's place (UX3 R72).
  await tile.focus()
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(new RegExp(`/runs/[^?]+\\?session=${id}$`))
})

// A question the tile cannot answer, and a member that ended (R20b).
test('a question without choices says it is answered in its terminal, and an ended member says Resume in words', async ({ page, api }) => {
  test.setTimeout(3 * 30_000 + 8 * 15_000 + 30_000)
  const run = await api.run(second)
  const solo = member(run, 'solo').sessionId ?? ''
  const pair = member(run, 'pair').sessionId ?? ''
  await expect.poll(async () => (await api.session(solo)).attention?.state, { timeout: 30_000 }).toBe('done')
  await api.ok('POST', `/api/sessions/${solo}/attention`, { state: 'needs_input', message: 'Which port should the preview use?' })
  await api.stopSession(pair)
  await expect.poll(async () => (await api.session(pair)).status, { timeout: 30_000 }).toMatch(/^(exited|stopped)$/)
  const { grid } = await open(page, second, 2)
  const asking = grid.locator(':scope > [data-member="solo"]')
  await expect(asking.locator('[data-run-tile-question]')).toHaveText('solo asks: Which port should the preview use?')
  await expect(asking.locator('[data-run-tile-answer="terminal"]')).toHaveText('Answer it in its terminal.')
  await expect(asking.locator('[data-run-ask-choice]')).toHaveCount(0)
  const ended = grid.locator(':scope > [data-member="pair"]')
  const resume = ended.getByRole('button', { name: /^(Resume|Relaunch)$/ })
  await expect(resume).toBeVisible()
  await expect(resume).toHaveAttribute('data-resume-kind', 'label')
  await expect(ended.locator('[data-tile-foot]')).not.toContainText('shared cwd')
})

// A run with tasks has a fourth view, Tasks: on a phone the layouts and Activity wrap under the views, their words kept, and nothing
// runs past the window (CI on the run header's phone walk).
for (const touch of [false, true]) {
  test(`at 390 x 844${touch ? ' with touch' : ''} a run with tasks keeps its views and Activity inside the window`, async ({ day, browser }) => {
    const run = ledgerRun()
    const page = await dayPage(browser, { path: '/', width: 390, height: 844, touch })
    await stubInbox(page, [])
    await serveRun(page, run)
    await page.goto(`/runs/${run.id}?view=grid`)
    const views = page.locator('[data-run-view]')
    await expect(views.getByRole('tab', { name: 'Tasks' })).toBeVisible()
    await expect(views.getByRole('tab', { name: 'Members' })).toHaveAttribute('aria-selected', 'true')
    const toggle = page.locator('[data-activity-toggle]')
    await expect(toggle).toHaveText('Activity')
    await atRest(toggle)
    const b = (await toggle.boundingBox())!
    expect(b.x).toBeGreaterThanOrEqual(0)
    expect(b.x + b.width).toBeLessThanOrEqual(390)
    await noOverlap(views.locator('..'))
    await noSideways(page)
    void day
  })
}

// The Needs you pill over a focused asking tile (R22): the frame keeps its digits, as a focused list's option does.
test("with the Needs you pill showing, a digit on a focused asking tile's frame answers the tile, not the pill", async ({ page, api, state }) => {
  test.setTimeout(4 * 30_000 + 10 * 15_000 + 30_000)
  // The pill lives for the test's length (the page's seam), so a press is judged with it shown, never after it went by itself.
  await page.addInitScript((ms) => ((window as Window & { conductorPillMs?: number }).conductorPillMs = ms), test.info().timeout)
  const core = member(await api.run(first), 'core').sessionId ?? ''
  const solo = member(await api.run(second), 'solo').sessionId ?? ''
  const read = async (id: string) => {
    const s = await api.session(id)
    const file = join(state.home, '.stub-sessions', `${s.agentSession?.id ?? ''}.txt`)
    return () => (existsSync(file) ? readFileSync(file, 'utf8') : '')
  }
  const coreText = await read(core)
  const soloText = await read(solo)
  await expect.poll(async () => (await api.session(core)).attention?.state, { timeout: 30_000 }).not.toBe('needs_input')
  await api.ok('POST', `/api/sessions/${core}/attention`, { state: 'needs_input', message: 'Which side?', options: [{ label: 'left', input: 'left\r' }, { label: 'right', input: 'right\r' }] })
  await api.ok('POST', `/api/sessions/${solo}/attention`, { state: 'needs_input', message: 'Which way?', options: [{ label: 'up', input: 'up\r' }, { label: 'down', input: 'down\r' }] })
  const { grid } = await open(page, first, 3)
  const tile = grid.locator(':scope > [data-member="core"] [data-session-tile]')
  await expect(tile.locator('[data-run-ask-choice]')).toHaveCount(2)
  await tile.focus()
  // ] steps through what needs you, the focus left on the frame, until the pill shows the other run's member.
  const pill = page.locator('[data-needs-pill]')
  await expect
    .poll(
      async () => {
        await page.keyboard.press(']')
        await expect(pill).toBeVisible()
        return pill.getAttribute('data-pill-session')
      },
      { timeout: 30_000, message: 'the pill shows solo' },
    )
    .toBe(solo)
  await expect(tile).toBeFocused()
  await page.keyboard.press('2')
  await expect.poll(coreText, { timeout: 30_000, message: 'core got right from its frame' }).toContain('right')
  expect(soloText()).not.toContain('down')
  await expect(pill).toBeVisible()
})
