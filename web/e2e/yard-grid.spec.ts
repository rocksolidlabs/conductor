import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Page } from '@playwright/test'
import { dayPage, expect, stopStrays, test, useTheme, type Session } from './fixtures'
import { BUSY_LIVE } from './day'
import { inside, noSideways } from './layout'

// The Yard is its grid (UX1's PR-18, docs/plans/ux-review.md, P22): no queue beside it at any width, whether a question waits or
// not; a tile that asks says the start of its question at its foot, its whole text in the tooltip; the pill's Enter on the Yard
// expands the tile in place, where its bar takes a typed reply.

/** The grid's padding and gap (pages/yard.vue: p-3, gap-3), and the tiles' preferred shape: what the page hands bestGrid. */
const PAD = 12
const GAP = 12
const ASPECT = 1.6

/** The app's bestGrid (utils/wall.ts), as the page calls it: the column count that gives n tiles the largest size in the box. */
function bestGrid(n: number, width: number, height: number): { cols: number; rows: number } {
  let best = { cols: 1, rows: n, size: -1 }
  for (let cols = 1; cols <= n; cols++) {
    const rows = Math.ceil(n / cols)
    const tileW = Math.min((width - GAP * (cols - 1)) / cols, ((height - GAP * (rows - 1)) / rows) * ASPECT)
    if (tileW > best.size + 0.5) best = { cols, rows, size: tileW }
  }
  return best
}

/** The grid's box and its tiles, read in one evaluation, with the panel's box (the Yard's UDashboardPanel). */
async function measure(page: Page) {
  return page.locator('[data-yard-grid]').evaluate((grid) => {
    const box = (el: Element) => {
      const r = el.getBoundingClientRect()
      return { left: r.left, right: r.right, top: r.top, bottom: r.bottom, width: r.width, height: r.height }
    }
    const panel = grid.closest('#wall')!
    return { grid: box(grid), panel: box(panel), tiles: Array.from(grid.querySelectorAll('[data-session-tile]')).map(box) }
  })
}

/** The grid at its full width: 12 px in from the panel's left edge, the panel's width less 24 px; every tile the size bestGrid gives it. */
async function fullWidth(page: Page, n: number) {
  await expect(page.locator('[data-wall-queue]')).toHaveCount(0)
  await expect(page.locator('[data-yard-grid] [data-session-tile]')).toHaveCount(n)
  const m = await measure(page)
  expect(m.grid.left - m.panel.left, 'the grid starts 12 px inside the panel').toBeCloseTo(PAD, 0)
  expect(m.grid.width, "the grid is the panel's width less 24 px").toBeCloseTo(m.panel.width - 2 * PAD, 0)
  const { cols, rows } = bestGrid(n, m.grid.width, m.grid.height)
  const cellW = (m.grid.width - GAP * (cols - 1)) / cols
  const cellH = (m.grid.height - GAP * (rows - 1)) / rows
  for (const t of m.tiles) {
    expect(t.width, 'a tile is as wide as its cell').toBeGreaterThanOrEqual(cellW - 1)
    expect(t.height, 'a tile is as tall as its cell').toBeGreaterThanOrEqual(cellH - 1)
  }
  return m
}

/** Each live session of the day that asks: its tile's foot shows the start of its question, the whole of it in the title. */
async function asks(page: Page, list: Session[]) {
  const asking = list.filter((s) => (s.status === 'running' || s.status === 'starting') && s.attention?.state === 'needs_input' && !s.attention.answered)
  expect(asking.length, 'the busy day has sessions that ask').toBeGreaterThan(0)
  for (const s of asking) {
    const ask = page.locator(`[data-session-tile][data-session-id="${s.id}"] [data-tile-ask]`)
    const words = s.attention!.message!
    await expect(ask).toHaveAttribute('title', s.attention!.decision ? `${s.attention!.decision} · ${words}` : words)
    const shown = (await ask.textContent()) ?? ''
    expect(shown.length).toBeGreaterThan(0)
    expect((s.attention!.decision ? `${s.attention!.decision} · ${words}` : words).startsWith(shown), 'the foot says the start of the question').toBe(true)
  }
  return asking
}

test("the busy day's eighteen tiles take the panel's width beside the sidebar, folded to the rail and opened again; each asking tile says its question", async ({ day, browser }) => {
  await day.part('busy')
  const page = await dayPage(browser, { path: '/yard' })
  const list = await day.listSessions()
  await fullWidth(page, BUSY_LIVE)
  await asks(page, list)
  // Folded to the rail: the grid takes the wider box.
  const before = (await measure(page)).grid.width
  await page.keyboard.press('Control+b')
  await expect.poll(async () => (await measure(page)).grid.width, { message: 'the grid widens with the rail' }).toBeGreaterThan(before + 100)
  await fullWidth(page, BUSY_LIVE)
  await asks(page, list)
  // Opened again: a redraw the test causes, and the same.
  await page.keyboard.press('Control+b')
  await expect.poll(async () => (await measure(page)).grid.width).toBeCloseTo(before, 0)
  await fullWidth(page, BUSY_LIVE)
  await asks(page, list)
})

test('on a phone the grid takes the width, and an asking tile says its question inside the tile', async ({ day, browser }) => {
  const n = await day.part('states')
  const page = await dayPage(browser, { path: '/yard', width: 390, height: 844, touch: true })
  const asker = page.locator(`[data-session-tile][data-session-id="${n['mac-only-flake']}"]`)
  await expect(asker.locator('[data-tile-ask]')).toBeVisible()
  const check = async () => {
    expect((await measure(page)).grid.width, 'the grid is wider than 300 px').toBeGreaterThan(300)
    await inside(asker.locator('[data-tile-ask]'), asker)
    await noSideways(page)
  }
  await check()
  await useTheme(page, 'light')
  await check()
})

function transcript(home: string, s: Session): string {
  const file = join(home, '.stub-sessions', `${s.agentSession?.id ?? ''}.txt`)
  return existsSync(file) ? readFileSync(file, 'utf8') : ''
}

test("a question asked again shows its new start on the tile, and the grid's width never moves", async ({ page, api }) => {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e yard asks again' })
  try {
    await expect.poll(async () => (await api.session(s.id)).status, { timeout: 30_000 }).toBe('running')
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto('/yard')
    const tile = page.locator(`[data-session-tile][data-session-id="${s.id}"]`)
    await expect(tile).toBeVisible({ timeout: 30_000 })
    const width = (await measure(page)).grid.width
    await api.ok('POST', `/api/sessions/${s.id}/attention`, { state: 'needs_input', message: 'Run the tests again?' })
    await expect(tile.locator('[data-tile-ask]')).toHaveAttribute('title', 'Run the tests again?')
    expect((await measure(page)).grid.width, 'no queue opened beside the grid').toBeCloseTo(width, 0)
    // Answered, then a new question.
    await api.ok('POST', `/api/sessions/${s.id}/attention`, { state: 'working' })
    await expect(tile.locator('[data-tile-ask]')).toHaveCount(0)
    await api.ok('POST', `/api/sessions/${s.id}/attention`, { state: 'needs_input', message: 'Ship the release notes now?' })
    await expect(tile.locator('[data-tile-ask]')).toHaveAttribute('title', 'Ship the release notes now?')
    await expect(tile.locator('[data-tile-ask]')).toHaveText(/^Ship/)
    expect((await measure(page)).grid.width).toBeCloseTo(width, 0)
    // Again after a redraw the test causes: the window narrowed and widened back.
    await page.setViewportSize({ width: 1280, height: 720 })
    await page.setViewportSize({ width: 1440, height: 900 })
    await expect.poll(async () => (await measure(page)).grid.width).toBeCloseTo(width, 0)
    await expect(tile.locator('[data-tile-ask]')).toHaveAttribute('title', 'Ship the release notes now?')
  } finally {
    await api.stopSession(s.id)
  }
})

test("the pill's Enter on the Yard expands the tile in place, and its bar takes a typed reply", async ({ page, api, state }, info) => {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e yard pill' })
  try {
    // ] steps through what needs you: the test's session alone among the sessions.
    await stopStrays(api, [s.id])
    await expect.poll(async () => (await api.session(s.id)).status, { timeout: 30_000 }).toBe('running')
    await api.ok('POST', `/api/sessions/${s.id}/attention`, { state: 'needs_input', message: 'Which branch?' })
    // The pill lives as long as the test may: the product's 4 s is not this test's bound.
    await page.addInitScript((ms) => ((window as Window & { conductorPillMs?: number }).conductorPillMs = ms), info.timeout)
    await page.goto('/yard')
    await expect(page.locator('#dashboard-sidebar-main')).toBeVisible({ timeout: 30_000 })
    await expect(page.locator(`[data-session-list] [data-sidebar-row="s:${s.id}"]`).first()).toHaveAttribute('data-row-state', 'needs', { timeout: 30_000 })
    const pill = page.locator('[data-needs-pill]')
    // A decision another spec left open may come first: step until the pill names this session, never more than what waits.
    const count = pill.locator('[data-pill-count]')
    await page.keyboard.press(']')
    for (let i = 0; i < 20; i++) {
      await expect(pill).toBeVisible()
      if ((await pill.getAttribute('data-pill-session')) === s.id) break
      const was = (await count.count()) ? await count.textContent() : ''
      await page.keyboard.press(']')
      // The step taken: the pill's place moved on (a mechanism, never a clock).
      await expect(count).not.toHaveText(was ?? '')
    }
    await expect(pill).toHaveAttribute('data-pill-session', s.id)
    await page.keyboard.press('Enter')
    await expect(page).toHaveURL(new RegExp(`/yard\\?focus=${s.id}$`))
    const field = page.locator('[data-yard-focus] [data-quick-reply] input')
    await field.fill('main please')
    await field.press('Enter')
    await expect.poll(() => transcript(state.home, s), { timeout: 15_000, message: 'the reply was typed' }).toContain('main please')
  } finally {
    await api.stopSession(s.id)
  }
})
