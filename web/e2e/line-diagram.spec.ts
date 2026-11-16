import type { Browser, Locator, Page } from '@playwright/test'
import type { Day } from './day'
import { dayPage, expect, test, type DayPageOptions, type Session } from './fixtures'
import { inside, noOverlap, noSideways, probeColour, tabTo } from './layout'
import { chainRun, fortyRun, ledgerRun, serveRun, type StubRun } from './run-fixtures'

// A run drawn as lines (UX1's PR-33, docs/plans/ux-review.md: P15): the run page's Graph view in Tasks mode draws each task as a
// station lit by its state's mark, each chain of tasks as a line in one neutral ink, and fits forty tasks with their names in the
// window. Runs are served from run-fixtures.ts and found by their ids; the Ledger's soak runs in a real stub session of the day's
// server, so that its station is working by taskStatus's own rule.

/** The Ledger's tasks and the states the server gives them. */
const STATES: Record<string, string> = { schema: 'succeeded', naming: 'succeeded', backfill: 'verifying', soak: 'running', migrate: 'failed', 'release-notes': 'ready' }
/** Its lines, each its stations in order: schema and naming split; soak stands alone; release-notes and backfill branch. */
const LINES = ['schema naming migrate', 'soak', 'release-notes', 'backfill']

/** A stub session of the day's server for soak's attempt, marked working and waited on the API; stopped and removed by `end`. */
async function soakSession(day: Day): Promise<{ id: string; end: () => Promise<void> }> {
  const api = day.api
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'day-working', name: 'line diagram soak' })
  const end = async () => {
    await api.stopSession(s.id)
    await expect.poll(async () => (await api.session(s.id)).status, { timeout: 15_000, message: 'the soak session ends' }).toMatch(/^(exited|stopped)$/)
    await api.stopSession(s.id)
  }
  try {
    await api.ok('POST', `/api/sessions/${s.id}/attention`, { state: 'working' })
    await expect.poll(async () => (await api.session(s.id)).attention?.state, { timeout: 15_000, message: 'the soak session is working' }).toBe('working')
  } catch (e) {
    await end()
    throw e
  }
  return { id: s.id, end }
}

/** The run served on a page of its own, opened on its Graph view; returns once its stations are drawn. */
async function graphPage(browser: Browser, run: StubRun, o: DayPageOptions = {}): Promise<{ page: Page; pane: Locator }> {
  const page = await dayPage(browser, { path: '/', ...o })
  await serveRun(page, run)
  await page.goto(`/runs/${run.id}?view=graph`)
  await expect(page.locator('[data-run-view]').getByRole('tab', { name: 'Graph' })).toHaveAttribute('aria-selected', 'true')
  if (!o.width || o.width >= 768) await expect(page.locator('[data-graph-canvas] [data-station]')).toHaveCount(run.tasks.length)
  await page.evaluate(() => document.fonts.ready.then(() => undefined))
  return { page, pane: page.locator('[data-graph-canvas] [data-line-pane]') }
}

const station = (page: Page, id: string) => page.locator(`[data-graph-canvas] [data-station="${id}"]`)

/** The lines as drawn: each one's stations, in no set order. */
async function linesOf(page: Page): Promise<string[]> {
  return (await page.locator('[data-graph-canvas] [data-line]').evaluateAll((ls) => ls.map((l) => l.getAttribute('data-line-stations') ?? ''))).sort()
}

/** Every station's box, by its id. */
async function boxes(page: Page): Promise<Record<string, string>> {
  return page.locator('[data-graph-canvas] [data-station] [data-station-hit]').evaluateAll((cs) =>
    Object.fromEntries(
      cs.map((c) => {
        const r = c.getBoundingClientRect()
        return [c.closest('[data-station]')!.getAttribute('data-station')!, `${r.x.toFixed(1)},${r.y.toFixed(1)},${r.width.toFixed(1)}`]
      }),
    ),
  )
}

/** The first case: six stations, each with its task's state and mark, the lines and the two splits. */
async function ledgerDrawn(page: Page): Promise<void> {
  await expect(page.locator('[data-graph-canvas] [data-station]')).toHaveCount(6)
  for (const [id, state] of Object.entries(STATES)) await expect(station(page, id)).toHaveAttribute('data-task-state', state)
  await expect(station(page, 'migrate')).toHaveAttribute('data-status-mark', 'needs')
  await expect(station(page, 'release-notes')).toHaveAttribute('data-status-mark', 'needs')
  await expect(station(page, 'soak')).toHaveAttribute('data-status-mark', 'working')
  expect(await linesOf(page)).toEqual([...LINES].sort())
  await expect(station(page, 'schema')).toHaveAttribute('data-junction', 'split')
  await expect(station(page, 'naming')).toHaveAttribute('data-junction', 'split')
  await expect(page.locator('[data-graph-canvas] [data-junction]')).toHaveCount(2)
  await expect(page.locator('[data-graph-canvas] [data-connector]')).toHaveCount(2)
}

test('the Ledger at 1440: six stations with their states and marks, its lines and its splits, and Members mode drawn by Vue Flow', async ({ day, browser }) => {
  const soak = await soakSession(day)
  try {
    const run = ledgerRun({ soak: soak.id })
    const { page } = await graphPage(browser, run)
    await ledgerDrawn(page)
    // The lines carry no state: one ink, no arrowhead.
    await expect(page.locator('[data-graph-canvas] [marker-end]')).toHaveCount(0)
    // Members mode is Vue Flow's: a node per member, and no station.
    await page.locator('[data-graph-mode-switch]').getByRole('tab', { name: 'Members' }).click()
    await expect(page.locator('[data-graph-canvas] [data-graph-node]')).toHaveCount(run.members.length)
    await expect(page.locator('[data-graph-canvas] [data-station]')).toHaveCount(0)
    await expect(page.locator('[data-graph-canvas]')).toHaveAttribute('data-graph-store', /^crew-graph-\d+$/)
  } finally {
    await soak.end()
  }
})

for (const size of [
  { width: 1440, height: 900 },
  { width: 1280, height: 720 },
]) {
  test(`forty tasks at ${size.width} x ${size.height}: every station inside the pane, which does not scroll, and no station or label over another`, async ({ day, browser }) => {
    const { page, pane } = await graphPage(browser, fortyRun(), size)
    const stations = page.locator('[data-graph-canvas] [data-station]')
    await expect(stations).toHaveCount(40)
    await inside(page.locator('[data-graph-canvas] [data-station] [data-status-glyph]'), pane)
    const { sw, cw, sh, ch, scale } = await pane.evaluate((el) => ({ sw: el.scrollWidth, cw: el.clientWidth, sh: el.scrollHeight, ch: el.clientHeight, scale: Number(el.getAttribute('data-line-scale')) }))
    expect(sw, 'the pane does not scroll sideways').toBeLessThanOrEqual(cw)
    expect(sh, 'the pane does not scroll down').toBeLessThanOrEqual(ch)
    expect(scale).toBeGreaterThanOrEqual(0.5)
    // The marks and the shown labels, each clear of every other.
    const labels = page.locator('[data-graph-canvas] [data-station-label]')
    expect(await labels.count()).toBeGreaterThan(30)
    await noOverlap(page.locator('[data-graph-canvas] [data-line-diagram]'), '[data-status-glyph], [data-station-label]')
    // Each state of a task drawn: every mark a run's task takes without a live session (a failed task the engine names Retry and
    // Accept for waits on a person: needs).
    const marks = new Set(await stations.evaluateAll((ss) => ss.map((s) => s.getAttribute('data-status-mark'))))
    expect([...marks].sort()).toEqual(['cancelled', 'done', 'needs', 'running', 'waiting', 'working'])
    void day
  })
}

test('a chain of 128 tasks: 128 stations, the pane scrolls sideways inside itself and the page never does', async ({ day, browser }) => {
  const { page, pane } = await graphPage(browser, chainRun(128))
  await expect(page.locator('[data-graph-canvas] [data-station]')).toHaveCount(128)
  await expect(pane).toHaveAttribute('data-line-scale', '0.500')
  expect(await pane.evaluate((el) => el.scrollWidth - el.clientWidth)).toBeGreaterThan(1000)
  await noSideways(page, { allow: pane })
  // Its last station is reached by scrolling the pane alone.
  await station(page, 't128').scrollIntoViewIfNeeded()
  expect(await pane.evaluate((el) => el.scrollLeft)).toBeGreaterThan(1000)
  expect(await page.evaluate(() => document.scrollingElement!.scrollLeft)).toBe(0)
  void day
})

test('a click selects a station and shows its caption; Show in Tasks opens the Tasks view on it; Enter on a station opens its session; the arrows move along a line', async ({ day, browser }) => {
  const soak = await soakSession(day)
  try {
    const run = ledgerRun({ soak: soak.id })
    const { page } = await graphPage(browser, run)
    await station(page, 'naming').click()
    await expect(page).toHaveURL(/[?&]task=naming(&|$)/)
    const caption = page.locator('[data-station-caption="naming"]')
    await expect(caption).toBeVisible()
    await expect(station(page, 'naming')).toHaveAttribute('aria-pressed', 'true')
    await expect(station(page, 'naming').locator('[data-station-ring]')).toHaveCount(1)
    // A task whose attempt has no session (release-notes has made none) offers no Open, only the Tasks view.
    await station(page, 'release-notes').click()
    await expect(page.locator('[data-station-caption="release-notes"] [data-station-show]')).toBeVisible()
    await expect(page.locator('[data-station-caption="release-notes"] [data-station-open]')).toHaveCount(0)
    await station(page, 'naming').click()
    await expect(caption.locator('[data-station-open]')).toBeVisible()
    await caption.locator('[data-station-show]').click()
    await expect(page).toHaveURL(/[?&]view=tasks(&|$)/)
    await expect(page).toHaveURL(/[?&]task=naming(&|$)/)
    await expect(page.locator('[data-run-tasks-view]')).toBeVisible()

    // Back on the graph: Tab walks the stations by depth, then lane, with a ring drawn; ArrowRight from schema goes along its line.
    await page.locator('[data-run-view]').getByRole('tab', { name: 'Graph' }).click()
    await expect(station(page, 'schema')).toBeVisible()
    expect(await tabTo(page, station(page, 'soak'), 1, station(page, 'schema'))).toBe(1)
    await station(page, 'schema').focus()
    await page.keyboard.press('ArrowRight')
    await expect(station(page, 'naming')).toBeFocused()
    await page.keyboard.press('ArrowRight')
    await expect(station(page, 'migrate')).toBeFocused()
    // Down: the nearest station on the lane below, release-notes beside migrate.
    await page.keyboard.press('ArrowDown')
    await expect(station(page, 'release-notes')).toBeFocused()
    // Enter on soak goes to its attempt's session page.
    await tabTo(page, station(page, 'soak'), 1, station(page, 'schema'))
    await page.keyboard.press('Enter')
    await expect(page).toHaveURL(new RegExp(`/sessions/${soak.id}$`))
  } finally {
    await soak.end()
  }
})

test('after a switch to Members and back, a direction there and back, and the page’s 5 s tick, the stations are where they were', async ({ day, browser }) => {
  const soak = await soakSession(day)
  try {
    // The run started a few seconds ago, so its uptime reads in seconds and changes at the page's next tick.
    const run = ledgerRun({ soak: soak.id, startedAt: new Date(Date.now() - 3_000).toISOString() })
    const { page } = await graphPage(browser, run)
    await ledgerDrawn(page)
    const switcher = page.locator('[data-graph-mode-switch]')
    await switcher.getByRole('tab', { name: 'Members' }).click()
    await expect(page.locator('[data-graph-canvas] [data-station]')).toHaveCount(0)
    await switcher.getByRole('tab', { name: 'Tasks' }).click()
    await ledgerDrawn(page)
    const before = await boxes(page)
    // Top to bottom: a line runs down, and each station's lane order runs across.
    await page.locator('[data-graph-direction]').click()
    await expect(page.locator('[data-graph-canvas] [data-line-diagram]')).toHaveAttribute('data-line-direction', 'TB')
    await ledgerDrawn(page)
    const at = async (id: string) => (await station(page, id).locator('[data-station-hit]').boundingBox())!
    const [schema, soakAt, naming] = [await at('schema'), await at('soak'), await at('naming')]
    expect(soakAt.x).toBeGreaterThan(schema.x)
    expect(Math.abs(soakAt.y - schema.y)).toBeLessThan(1)
    expect(naming.y).toBeGreaterThan(schema.y)
    await page.locator('[data-graph-direction]').click()
    await expect(page.locator('[data-graph-canvas] [data-line-diagram]')).toHaveAttribute('data-line-direction', 'LR')
    await ledgerDrawn(page)
    expect(await boxes(page)).toEqual(before)
    // The page's tick: wait until the uptime's text has changed (a tick of 5 s, and the expect's own bound), then the same again.
    const uptime = page.locator('[data-run-uptime]')
    const first = await uptime.textContent()
    await expect(uptime).not.toHaveText(first ?? '', { timeout: 5_000 + 15_000 })
    await ledgerDrawn(page)
    expect(await boxes(page)).toEqual(before)
  } finally {
    await soak.end()
  }
})

test('at 1024 with touch: a tap selects soak and shows its caption, and a tap on its Open goes to its session', async ({ day, browser }) => {
  const soak = await soakSession(day)
  try {
    const { page } = await graphPage(browser, ledgerRun({ soak: soak.id }), { width: 1024, height: 768, touch: true })
    await station(page, 'soak').locator('[data-station-hit]').tap()
    const caption = page.locator('[data-station-caption="soak"]')
    await expect(caption).toBeVisible()
    await expect(page).toHaveURL(/[?&]task=soak(&|$)/)
    await caption.locator('[data-station-open]').tap()
    await expect(page).toHaveURL(new RegExp(`/sessions/${soak.id}$`))
  } finally {
    await soak.end()
  }
})

for (const theme of ['light', 'dark'] as const) {
  test(`in the ${theme} theme a needs station is drawn amber, and every line and connector in the muted ink`, async ({ day, browser }) => {
    const { page } = await graphPage(browser, ledgerRun(), { theme })
    const warning = await probeColour(page, 'var(--ui-warning)')
    const muted = await probeColour(page, 'var(--ui-text-muted)')
    const fill = await station(page, 'migrate').locator('[data-status-glyph] circle').first().evaluate((c) => getComputedStyle(c).fill)
    expect(await probeColour(page, fill)).toBe(warning)
    const strokes = await page.locator('[data-graph-canvas] [data-line], [data-graph-canvas] [data-connector]').evaluateAll((ps) => ps.map((p) => getComputedStyle(p).stroke))
    expect(strokes).toHaveLength(LINES.length + 2)
    for (const s of strokes) expect(await probeColour(page, s)).toBe(muted)
    void day
  })
}

test('a phone keeps the list: at 390 with touch the tasks are listed by depth and no station is drawn', async ({ day, browser }) => {
  const { page } = await graphPage(browser, ledgerRun(), { width: 390, height: 844, touch: true })
  const list = page.locator('[data-graph-task-list]')
  await expect(list).toBeVisible()
  await expect(list.locator('[data-graph-task-row]')).toHaveCount(6)
  await expect(list.locator('[data-graph-task-row="migrate"]')).toHaveAttribute('data-depth', '2')
  await expect(page.locator('[data-station]')).toHaveCount(0)
  void day
})
