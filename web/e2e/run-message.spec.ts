import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Page } from '@playwright/test'
import { atRest, expect, member, standingRoute, test, type Api, type Session } from './fixtures'

// The run page's message bar (UX3 R23a): `Message to`, a chip `All` and one for each member. A chip that can be ticked is a
// button, pressed or not; a member that cannot get a line says why on its chip and is not a button: `review · answer it first`
// while it waits on a question (amber, the status language's needs), `tests · not started`, and `· ended` once it ended. The
// field names who gets the line, the button counts them, and the line reaches exactly the ticked members: the request names them
// alone, and the stubs' transcripts are read by each member's own session. A send that failed says `Not sent` and keeps the line.
test.describe.configure({ mode: 'serial' })

let crewId = ''
let runId = ''
const sessions: Record<string, Session> = {}

test.beforeEach(async ({ context }) => {
  await standingRoute(context)
})

test.beforeAll(async ({ api }) => {
  // Its waits: three members started and their first turns done (60 s each at most); the timeout is above their sum.
  test.setTimeout(3 * 60_000 + 30_000)
  const crew = await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
    name: `e2e message bar ${Date.now().toString(36)}`,
    goal: 'message the crew',
    cwd: '',
    where: 'server',
    isolation: 'none',
    openAfterLaunch: false,
    yolo: null,
    members: [
      { name: 'lead', agentId: 'claude', prompt: 'say hello', start: { when: 'immediately' } },
      { name: 'core', agentId: 'claude', prompt: 'say hi', start: { when: 'immediately' } },
      { name: 'review', agentId: 'claude', prompt: 'say hey', start: { when: 'immediately' } },
      { name: 'tests', agentId: 'claude', prompt: 'say yo', start: { when: 'manual' } },
    ],
  })
  crewId = crew.crew.id
  runId = (await api.launchCrew(crewId)).id
  for (const name of ['lead', 'core', 'review']) {
    let id = ''
    await expect
      .poll(
        async () => {
          const m = member(await api.run(runId), name)
          id = m.sessionId ?? ''
          return id ? m.status : ''
        },
        { timeout: 60_000, message: `${name} runs` },
      )
      .toBe('running')
    // The first turn ends with the stub's Stop hook, which reports done: a question posted before it would be overwritten.
    await expect.poll(async () => (await api.session(id)).attention?.state, { timeout: 60_000, message: `${name}'s first turn is done` }).toBe('done')
    sessions[name] = await api.session(id)
  }
  // review waits on a question: the bar must never type into it.
  await asks(api, 'review')
})

test.afterAll(async ({ api }) => {
  if (runId) await api.stopRun(runId)
  if (crewId) await api.call('DELETE', `/api/crews/${encodeURIComponent(crewId)}`)
})

function asks(api: Api, name: string) {
  return api.ok('POST', `/api/sessions/${sessions[name]!.id}/attention`, { state: 'needs_input', message: 'Ship it as one pull request?' })
}

/** What the member's stub was typed: its transcript, by its own agent session. */
function transcript(home: string, name: string): string {
  const file = join(home, '.stub-sessions', `${sessions[name]!.agentSession?.id ?? ''}.txt`)
  return existsSync(file) ? readFileSync(file, 'utf8') : ''
}

const bar = (page: Page) => page.locator('[data-message-bar]')
// The field's mark is on its input (the UI library hands the attributes to it).
const field = (page: Page) => bar(page).locator('input[data-message-field]')
const chip = (page: Page, name: string) => bar(page).locator(`[data-message-chip="${name}"]`)

async function openRun(page: Page) {
  await page.goto(`/runs/${encodeURIComponent(runId)}?view=grid`)
  // The chips are drawn from the run's members and their sessions: wait until review's question is read.
  await expect(chip(page, 'review')).toHaveAttribute('data-chip-state', 'waiting', { timeout: 30_000 })
}

for (const vp of [
  { width: 2000, height: 810 },
  { width: 1440, height: 900 },
]) {
  test(`at ${vp.width} x ${vp.height}: everyone who can get a line by default, a press leaves one out, and the line reaches exactly the ticked members`, async ({ page, state }) => {
    await page.setViewportSize(vp)
    await openRun(page)
    await expect(bar(page).locator('#message-to')).toHaveText('Message to')
    // The old bar's words are gone with the tile's checkboxes.
    await expect(page.getByText('Broadcast', { exact: true })).toHaveCount(0)
    await expect(page.locator('[data-broadcast-pick]')).toHaveCount(0)

    // Every member has a chip, in the run's order; All and the members that can get a line are pressed.
    await expect(bar(page).locator('[data-message-chip]')).toHaveText(['All', 'lead', 'core', /review · answer it first$/, 'tests · not started'])
    for (const name of ['all', 'lead', 'core']) await expect(chip(page, name)).toHaveAttribute('aria-pressed', 'true')
    // Neither can be ticked: review's says it waits on you, in the status language's amber, and is a button that shows its tile (no
    // pressed state); tests' is no button at all.
    await expect(chip(page, 'review')).not.toHaveAttribute('aria-pressed')
    expect(await chip(page, 'review').evaluate((e) => e.tagName)).toBe('BUTTON')
    await expect(bar(page).getByRole('button', { name: /tests/ })).toHaveCount(0)
    await expect(chip(page, 'review').locator('[data-status-mark="needs"]')).toHaveCount(1)
    await expect(chip(page, 'tests')).toHaveAttribute('data-chip-state', 'not_started')
    const input = field(page)
    const send = bar(page).locator('[data-broadcast-send]')
    await expect(input).toHaveAttribute('placeholder', 'Write a line for lead and core')
    await expect(send).toHaveText('Send to 2')

    // A press on core leaves it out: All is off, and the field and the button say who is left.
    await chip(page, 'core').click()
    await expect(chip(page, 'core')).toHaveAttribute('aria-pressed', 'false')
    await expect(chip(page, 'all')).toHaveAttribute('aria-pressed', 'false')
    await expect(input).toHaveAttribute('placeholder', 'Write a line for lead')
    await expect(send).toHaveText('Send to 1')

    // Enter sends; the request names lead alone, and the server typed it into lead alone.
    const line = `only lead ${vp.width} ${Date.now().toString(36)}`
    await input.fill(line)
    const answered = page.waitForResponse((r) => r.url().endsWith(`/api/runs/${encodeURIComponent(runId)}/broadcast`) && r.request().method() === 'POST')
    await input.press('Enter')
    const res = await answered
    expect(res.request().postDataJSON()).toMatchObject({ text: line, members: ['lead'] })
    expect(await res.json()).toEqual({ sent: ['lead'], skipped: [] })
    await expect.poll(() => transcript(state.home, 'lead'), { timeout: 20_000, message: 'lead got the line' }).toContain(line)
    await expect(input).toHaveValue('')
    for (const name of ['core', 'review']) expect(transcript(state.home, name), `${name} did not get the line`).not.toContain(line)

    // All puts everyone back.
    await chip(page, 'all').click()
    await expect(chip(page, 'core')).toHaveAttribute('aria-pressed', 'true')
    await expect(send).toHaveText('Send to 2')
  })
}

test('a send that failed says Not sent with the server’s words, and the line stays in the field', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await openRun(page)
  await page.route('**/api/runs/*/broadcast', (route) =>
    route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: { code: 'unavailable', message: 'the server is restarting' } }) }),
  )
  const input = field(page)
  await input.fill('this line stays')
  await bar(page).locator('[data-broadcast-send]').click()
  const toast = page.locator('li').filter({ hasText: 'Not sent' }).last()
  await expect(toast).toBeVisible()
  await expect(toast).toContainText('the server is restarting')
  await expect(page.locator('li').filter({ hasText: 'Broadcast failed' })).toHaveCount(0)
  await expect(input).toHaveValue('this line stays')
  await page.unroute('**/api/runs/*/broadcast')
})

test('a line everyone it named got is said in the bar for a while, with no toast', async ({ page, state }) => {
  await page.setViewportSize({ width: 2000, height: 810 })
  await openRun(page)
  const input = field(page)
  const line = `to the crew ${Date.now().toString(36)}`
  await input.fill(line)
  const answered = page.waitForResponse((r) => r.url().endsWith('/broadcast') && r.request().method() === 'POST')
  await input.press('Enter')
  expect(await (await answered).json()).toEqual({ sent: ['lead', 'core'], skipped: [] })
  await expect(input).toHaveValue('')
  await expect(input).toHaveAttribute('placeholder', 'Sent to lead and core.')
  await expect(bar(page).locator('[data-message-said]')).toHaveText('Sent to lead and core.')
  await expect(page.locator('li').filter({ hasText: /Sent to \d+ of \d+/ })).toHaveCount(0)
  for (const name of ['lead', 'core']) await expect.poll(() => transcript(state.home, name), { timeout: 20_000, message: `${name} got the line` }).toContain(line)
  // Then the field names who gets the next line again (the bar says it for four seconds; the bound is above that).
  await expect(input).toHaveAttribute('placeholder', 'Write a line for lead and core', { timeout: 15_000 })
  await expect(bar(page).locator('[data-message-said]')).toHaveText('')
})

test('a line only some got toasts who was skipped and why', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await openRun(page)
  await page.route('**/api/runs/*/broadcast', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ sent: ['lead'], skipped: [{ member: 'core', reason: 'needs_input' }] }) }),
  )
  const input = field(page)
  await input.fill('only some get this')
  await bar(page).locator('[data-broadcast-send]').click()
  const toast = page.locator('li').filter({ hasText: 'Sent to 1 of 2' }).last()
  await expect(toast).toBeVisible()
  await expect(toast).toContainText('Skipped core (waiting on a prompt).')
  await expect(input).toHaveValue('')
  await expect(input).not.toHaveAttribute('placeholder', /^Sent to/)
  await page.unroute('**/api/runs/*/broadcast')
})

test('a long line shows its count from 3,072 bytes, red past 4,096 with Send off', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await openRun(page)
  const input = field(page)
  const count = bar(page).locator('[data-message-count]')
  const send = bar(page).locator('[data-broadcast-send]')
  await input.fill('x'.repeat(3071))
  await expect(count).toHaveCount(0)
  await input.fill('x'.repeat(3100))
  await expect(count).toHaveText('3,100 / 4,096')
  await expect(count).toHaveAttribute('data-message-count', 'under')
  await expect(send).toBeEnabled()
  // A letter of two bytes counts as two.
  await input.fill('é'.repeat(2050))
  await expect(count).toHaveText('4,100 / 4,096')
  await expect(count).toHaveAttribute('data-message-count', 'over')
  await expect(input).toHaveAttribute('aria-invalid', 'true')
  await expect(send).toBeDisabled()
  await input.fill('short')
  await expect(count).toHaveCount(0)
  await expect(send).toBeEnabled()
})

test('a double-click on a chip leaves that member alone, and All puts everyone back', async ({ page }) => {
  await page.setViewportSize({ width: 2000, height: 810 })
  await openRun(page)
  const send = bar(page).locator('[data-broadcast-send]')
  await chip(page, 'core').dblclick()
  await expect(chip(page, 'core')).toHaveAttribute('aria-pressed', 'true')
  await expect(chip(page, 'lead')).toHaveAttribute('aria-pressed', 'false')
  await expect(chip(page, 'all')).toHaveAttribute('aria-pressed', 'false')
  await expect(field(page)).toHaveAttribute('placeholder', 'Write a line for core')
  await expect(send).toHaveText('Send to 1')
  await chip(page, 'all').click()
  await expect(send).toHaveText('Send to 2')
})

test("the waiting member's chip shows its tile, focused, from another view", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(`/runs/${encodeURIComponent(runId)}?view=graph`)
  await expect(page.locator('[data-run-graph]')).toBeVisible({ timeout: 30_000 })
  await expect(chip(page, 'review')).toHaveAttribute('data-chip-state', 'waiting', { timeout: 30_000 })
  await chip(page, 'review').click()
  const tile = page.locator('[data-run-grid] [data-member="review"] [data-session-tile]')
  await expect(tile).toBeFocused()
  await expect(tile).toBeInViewport()
  await expect(page.locator('[data-run-view]').getByRole('tab', { name: 'Members' })).toHaveAttribute('aria-selected', 'true')
})

test('while the bar holds the focus the tiles that will get the line carry the ring', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await openRun(page)
  const ringed = page.locator('[data-run-grid] [data-session-tile][data-receiving]')
  await expect(ringed).toHaveCount(0)
  await field(page).focus()
  await expect(ringed).toHaveCount(2)
  await expect(page.locator('[data-run-grid] [data-member="lead"] [data-session-tile]')).toHaveAttribute('data-receiving', 'true')
  // A press on a chip keeps the focus in the bar: the ring follows the pick.
  await chip(page, 'core').click()
  await expect(ringed).toHaveCount(1)
  await expect(page.locator('[data-run-grid] [data-member="core"] [data-session-tile]')).not.toHaveAttribute('data-receiving')
  // Out of the bar, no ring.
  await page.locator('[data-run-view]').getByRole('tab', { name: 'Members' }).focus()
  await expect(ringed).toHaveCount(0)
  await chip(page, 'all').click()
})

test('the pick and the line begun survive a trip to a member and back', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await openRun(page)
  await chip(page, 'core').click()
  await field(page).fill('half a line')
  await page.locator('[data-run-grid] [data-member="lead"] [data-tile-open]').click()
  await expect(page).toHaveURL(/\/sessions\//)
  await page.goBack()
  await expect(chip(page, 'core')).toHaveAttribute('aria-pressed', 'false', { timeout: 30_000 })
  await expect(chip(page, 'lead')).toHaveAttribute('aria-pressed', 'true')
  await expect(field(page)).toHaveValue('half a line')
  await field(page).fill('')
  await chip(page, 'all').click()
})

test('an answered member comes back ticked only while All is on', async ({ page, api }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await openRun(page)
  const send = bar(page).locator('[data-broadcast-send]')
  // core left out: review's answer brings its chip back not ticked, and the button stays.
  await chip(page, 'core').click()
  await expect(send).toHaveText('Send to 1')
  await api.ok('POST', `/api/sessions/${sessions.review!.id}/attention`, { state: 'working' })
  // A chip that can be ticked says on or off.
  await expect(chip(page, 'review')).toHaveAttribute('data-chip-state', 'off')
  await expect(chip(page, 'review')).toHaveText('review')
  await expect(chip(page, 'review')).toHaveAttribute('aria-pressed', 'false')
  await expect(send).toHaveText('Send to 1')
  // All on: review asks again and leaves the line; answered, it joins by itself.
  await chip(page, 'all').click()
  await expect(send).toHaveText('Send to 3')
  await asks(api, 'review')
  await expect(chip(page, 'review')).toHaveAttribute('data-chip-state', 'waiting')
  await expect(send).toHaveText('Send to 2')
  await api.ok('POST', `/api/sessions/${sessions.review!.id}/attention`, { state: 'working' })
  await expect(chip(page, 'review')).toHaveAttribute('aria-pressed', 'true')
  await expect(send).toHaveText('Send to 3')
  await asks(api, 'review')
})

test.describe('on a phone', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

  test('at rest the bar is one row, the asking member in view, and the last tile scrolls whole above it', async ({ page }) => {
    await openRun(page)
    const b = bar(page)
    await expect(b).toHaveAttribute('data-bar-folded', 'true')
    // Who gets the line, short; the asking member's amber chip, without opening anything; the field and Send. No chip of the rest.
    await expect(b.locator('[data-message-to]')).toHaveText('To all')
    await expect(chip(page, 'review')).toBeInViewport()
    await expect(chip(page, 'review').locator('[data-status-mark="needs"]')).toHaveCount(1)
    await expect(chip(page, 'review')).toHaveText('review')
    await expect(b.locator('[data-message-chip="all"], [data-message-chip="lead"], [data-message-chip="tests"]')).toHaveCount(0)
    await expect(b.locator('[data-broadcast-send]')).toHaveAccessibleName('Send to 2')
    await atRest(b)
    const parts = await b.locator('[data-message-to], [data-message-chip], input[data-message-field], [data-broadcast-send]').evaluateAll((els) => els.map((e) => e.getBoundingClientRect()).map((r) => ({ top: r.top, bottom: r.bottom, right: r.right, h: r.height })))
    const barBox = (await b.boundingBox())!
    expect(barBox.height, 'one row').toBeLessThan(70)
    for (const p of parts) {
      expect(p.top, 'on the bar').toBeGreaterThanOrEqual(barBox.y - 0.5)
      expect(p.bottom, 'on the bar').toBeLessThanOrEqual(barBox.y + barBox.height + 0.5)
      expect(p.right, 'inside the window').toBeLessThanOrEqual(390)
    }
    for (const p of parts.filter((x) => x.h > 40)) expect(p.h).toBeGreaterThanOrEqual(44)
    // The wall scrolls clear of the bar: scrolled to its end, its last tile stands whole between the view row and the bar, the wall's
    // own room under it.
    const last = page.locator('[data-run-grid] > [data-member]').last()
    await page.locator('[data-run-grid]').evaluate((g) => {
      const area = g.parentElement!
      area.scrollTop = area.scrollHeight
    })
    await atRest(last)
    const t = (await last.boundingBox())!
    const area = (await page.locator('[data-run-grid]').evaluate((g) => g.parentElement!.getBoundingClientRect().toJSON())) as DOMRect
    expect(t.y, 'its top in the wall').toBeGreaterThanOrEqual(area.top - 0.5)
    expect(t.y + t.height, 'its foot above the bar').toBeLessThanOrEqual(barBox.y - 4)
  })

  test('a press on who gets it opens the chips, 44 px tall, and leaving the bar folds them', async ({ page }) => {
    await openRun(page)
    await bar(page).locator('[data-message-to]').tap()
    await expect(bar(page)).not.toHaveAttribute('data-bar-folded')
    await expect(field(page)).toBeFocused()
    const chips = bar(page).locator('[data-message-chip]')
    await expect(chips).toHaveCount(5)
    const boxes = await chips.evaluateAll((els) => els.map((e) => e.getBoundingClientRect()).map((r) => ({ top: r.top, height: r.height, right: r.right })))
    for (const b of boxes) {
      expect(b.height).toBeGreaterThanOrEqual(44)
      expect(b.right).toBeLessThanOrEqual(390)
    }
    expect(new Set(boxes.map((b) => Math.round(b.top))).size, 'the chips wrap').toBeGreaterThan(1)
    await chip(page, 'lead').tap()
    await expect(chip(page, 'lead')).toHaveAttribute('aria-pressed', 'false')
    await expect(bar(page).locator('[data-broadcast-send]')).toHaveText('Send to 1')
    // Out of the bar: folded again, saying who gets the line.
    await page.locator('[data-run-view]').getByRole('tab', { name: 'Members' }).tap()
    await expect(bar(page)).toHaveAttribute('data-bar-folded', 'true')
    await expect(bar(page).locator('[data-message-to]')).toHaveText('To core')
    // The cursor in the field opens them too.
    await field(page).tap()
    await expect(bar(page)).not.toHaveAttribute('data-bar-folded')
    await expect(chip(page, 'lead')).toBeVisible()
  })
})

test('on a stopped run the bar says it has ended, with no field', async ({ page, api }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await openRun(page)
  await api.stopRun(runId)
  await expect(page.locator('[data-broadcast-ended]')).toHaveText('This run has ended.', { timeout: 30_000 })
  await expect(page.locator('[data-message-field]')).toHaveCount(0)
})
