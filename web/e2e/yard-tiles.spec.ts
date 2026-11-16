import type { Locator, Page } from '@playwright/test'
import { budget, dayPage, expect, test, useTheme, type Session } from './fixtures'
import { DAY_STEP_MS, type Names } from './day'
import { inside, noOverlap, probeColour, tapTarget, uncut } from './layout'

// The Yard's tiles lead with the name, and its chips name the states (UX1's PR-17, docs/plans/ux-review.md, P10): a tile's header is
// its name, whole, and its state is a mark and a word at its foot; the chips are All, Needs you, Working, Finished, and Running while
// a session is in it; Needs you counts the sessions on this grid that ask, and says so in the text's colour, its mark amber.

/** The four live sessions of the day's `states` part, with the key and the word their foot shows. */
const LIVE: Record<string, { key: string; word: RegExp }> = {
  'mac-only-flake': { key: 'needs', word: /^needs you$/ },
  'yard-tile-resize': { key: 'working', word: /^working$/ },
  // The age ticks while the test reads.
  'flaky-vim-spec': { key: 'finished', word: /^finished (just now|\d+[mh] ago)$/ },
  driver: { key: 'running', word: /^running$/ },
}

const tileOf = (page: Page, id: string) => page.locator(`[data-session-tile][data-session-id="${id}"]`)

/** Each live session of the day: its name whole in the header, no mark there, its key and word at its foot, the foot's parts apart. */
async function tiles(page: Page, n: Names): Promise<void> {
  for (const [name, want] of Object.entries(LIVE)) {
    const tile = tileOf(page, n[name]!)
    const mark = tile.locator('[data-tile-foot] [data-status-mark]')
    await expect(mark, `${name}'s foot`).toHaveAttribute('data-status-mark', want.key)
    await expect(mark.locator('[data-status-word]'), `${name}'s word`).toHaveText(want.word)
    await expect(tile.locator('[data-tile-header] [data-status-mark]'), `${name}'s header holds no state`).toHaveCount(0)
    await expect(tile.locator('[data-tile-name]')).toHaveText(name)
    await expect(tile.locator('[data-tile-name]')).toHaveAttribute('title', name)
    await uncut(tile.locator('[data-tile-name]'))
    await noOverlap(tile.locator('[data-tile-foot]'))
    await inside(tile.locator('[data-tile-foot] > *'), tile)
  }
}

test("the day's tiles: the name whole in the header, the state's mark and word at the foot, a run's member's before its branch", async ({ day, browser }) => {
  const n = await day.part('states')
  const page = await dayPage(browser, { path: '/yard' })
  await tiles(page, n)
  // The four drawn, the grid holds live sessions alone: the exited one has no tile.
  await expect(tileOf(page, n['old-bisect']!)).toHaveCount(0)
  // Every other tile on the grid: a name that does not fit is cut by the header's rule, its whole text in its title.
  for (const name of await page.locator('[data-session-tile] [data-tile-name]').all()) expect(await name.getAttribute('title')).toBe(await name.textContent())

  // A run's member: the state before the branch and the diff, all in the foot.
  const r = await day.part('runs')
  budget(DAY_STEP_MS)
  await page.goto(`/runs/${encodeURIComponent(r['Ledger migration']!)}?view=grid`)
  const member = page.locator('[data-run-grid] [data-member="migrator"] [data-session-tile]')
  const foot = member.locator('[data-tile-foot]')
  await expect(foot.locator('[data-status-mark]')).toHaveAttribute('data-status-mark', 'working', { timeout: DAY_STEP_MS })
  await expect(foot.locator('[data-branch]')).toBeVisible()
  await inside(foot.locator('[data-status-mark], [data-branch], [data-diff]'), foot)
  await noOverlap(foot)
  const [mark, branch] = await Promise.all([foot.locator('[data-tile-state]').boundingBox(), foot.locator('[data-branch]').boundingBox()])
  expect(mark!.x + mark!.width, "the member's state is before its branch").toBeLessThanOrEqual(branch!.x + 0.5)
})

test('Working and Finished each show their own tile, and a tile moves when its state changes', async ({ page, api }) => {
  const mine: string[] = []
  try {
    const works = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e yard works' })
    mine.push(works.id)
    const done = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e yard done' })
    mine.push(done.id)
    for (const s of [works, done]) await expect.poll(async () => (await api.session(s.id)).status, { timeout: 30_000 }).toBe('running')
    await api.ok('POST', `/api/sessions/${works.id}/attention`, { state: 'working' })
    await api.ok('POST', `/api/sessions/${done.id}/attention`, { state: 'done' })
    for (const [s, want] of [[works, 'working'], [done, 'done']] as const) await expect.poll(async () => (await api.session(s.id)).attention?.state).toBe(want)

    await page.goto('/yard')
    const chip = (key: string) => page.locator(`[data-yard-filter="${key}"]`)
    await expect(tileOf(page, works.id).locator('[data-tile-foot] [data-status-mark]')).toHaveAttribute('data-status-mark', 'working', { timeout: 30_000 })
    await chip('working').click()
    await expect(chip('working')).toHaveAttribute('data-active', '')
    await expect(tileOf(page, works.id)).toBeVisible()
    await expect(tileOf(page, done.id)).toHaveCount(0)
    await chip('finished').click()
    await expect(tileOf(page, done.id)).toBeVisible()
    await expect(tileOf(page, works.id)).toHaveCount(0)

    // With Working chosen the working session finishes: it leaves the grid, and Finished shows it finished.
    await chip('working').click()
    await expect(tileOf(page, works.id)).toBeVisible()
    await api.ok('POST', `/api/sessions/${works.id}/attention`, { state: 'done' })
    await expect(tileOf(page, works.id)).toHaveCount(0)
    await chip('finished').click()
    const word = tileOf(page, works.id).locator('[data-tile-foot] [data-status-word]')
    await expect(word).toHaveText(/^finished/)
    // Again after a redraw the test causes: the grid left and come back to.
    await chip('all').click()
    await chip('finished').click()
    await expect(word).toHaveText(/^finished/)
    await expect(tileOf(page, done.id)).toBeVisible()
  } finally {
    for (const id of mine) await api.stopSession(id)
  }
})

/** The live sessions the day's server lists that ask, read at the moment of the poll. */
async function asking(list: () => Promise<Session[]>): Promise<number> {
  return (await list()).filter((s) => (s.status === 'running' || s.status === 'starting') && s.attention?.state === 'needs_input' && !s.attention.answered).length
}

for (const theme of ['dark', 'light'] as const) {
  test(`the Needs you chip counts this grid's asking sessions in the text's colour and says so (${theme})`, async ({ day, browser }) => {
    await day.part('states')
    const page = await dayPage(browser, { path: '/yard', theme })
    const chip = page.locator('[data-yard-filter="needs"]')
    await expect(chip).toContainText('Needs you')
    // The number, read with the server's listing at the same poll: the day's parts are shared with the worker's other specs.
    await expect
      .poll(async () => Number(await chip.locator('[data-yard-count]').textContent()) - (await asking(() => day.listSessions())), { message: "the chip's number is the asking sessions on the grid" })
      .toBe(0)
    await expect(chip).toHaveAttribute('aria-label', /^\d+ sessions? on this grid asks? you\. Plans, tasks and decisions wait in Needs you\.$/)
    const colours = await chip.evaluate((el) => ({
      count: getComputedStyle(el.querySelector('[data-yard-count]')!).color,
      mark: getComputedStyle(el.querySelector('[data-status-glyph]')!).color,
    }))
    const warning = await probeColour(page, 'var(--ui-warning)')
    expect(await probeColour(page, colours.count), 'the number is not amber').not.toBe(warning)
    expect(await probeColour(page, colours.mark), 'the mark is amber').toBe(warning)
  })
}

/** On a phone: the chips in their row under the header, each whole, inside the window, a 44 px target, clear of the header. */
async function phoneChips(page: Page): Promise<Locator> {
  const row = page.locator('[data-yard-toolbar]')
  const chips = row.locator('[data-yard-filter]')
  await expect(chips.first()).toBeVisible()
  await expect(page.locator('[data-yard-header] [data-yard-filter]')).toHaveCount(0)
  await uncut(chips)
  await inside(chips, page.locator('body'))
  await tapTarget(chips)
  await noOverlap(row)
  const [header, toolbar] = await Promise.all([page.locator('[data-yard-header]').boundingBox(), row.boundingBox()])
  expect(toolbar!.y, 'the chips are under the header').toBeGreaterThanOrEqual(header!.y + header!.height - 0.5)
  return chips
}

test('on a phone the chips sit in a row under the header, and Finished shows the finished tile', async ({ day, browser }) => {
  const n = await day.part('states')
  const page = await dayPage(browser, { path: '/yard', width: 390, height: 844, touch: true })
  const pick = async () => {
    await phoneChips(page)
    await page.locator('[data-yard-toolbar] [data-yard-filter="finished"]').tap()
    await expect(page.locator('[data-yard-filter="finished"]')).toHaveAttribute('data-active', '')
    await expect(tileOf(page, n['flaky-vim-spec']!)).toBeVisible()
    await expect(tileOf(page, n['yard-tile-resize']!)).toHaveCount(0)
  }
  await pick()
  // The theme switched as the app keeps it, a redraw the test causes, and the same again.
  await useTheme(page, 'light')
  await pick()
})
