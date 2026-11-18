import type { Locator, Page } from '@playwright/test'
import { brakemanServer, pressInTurn, type BrakemanRequestSeen, type BrakemanServer } from './brakeman-server'
import { walk, worst } from './contrast'
import { makeProject, makeTicket } from './dispatch-helpers'
import { atRest, budget, expect, test, useTheme } from './fixtures'
import { MEASURE_WAIT_MS, noSideways, tapTarget } from './layout'

// The ask bar (the Brakeman design review), on a server of its own with the Brakeman stub (the seeded day holds no Brakeman
// records): a typed line is a question about what it names with @, sent under the person's name and read back through the API by
// its id; a chip with "…" sets its kind and opens the @ list narrowed to what the kind takes; a briefing from another time; the
// page's ?kind=&subject= fills the bar and sends nothing; the problem's words and the server's refusal; the phone's foot bar; both
// themes; the palette's rows send from where the person is and say so in a toast; G then B goes to the page; and on a phone, More
// is lit on its pages and Brakeman's row in it counts the briefs not yet opened.
test.describe.configure({ mode: 'serial' })

/** The cap of every wait a step makes without a bound of its own. */
const STEP_MS = 15_000
test.use({ actionTimeout: STEP_MS, navigationTimeout: STEP_MS })

/** The bound on the earlier tests' turns all ending: each stub turn reads twice and submits; this only bounds a hang. */
const IDLE_MS = 120_000

const NAME = 'Rail Tester'
let srv: BrakemanServer
let ticket = ''
/** The run of the turn beforeAll waits out: a ref a person may type whole. */
let turnRun = ''

test.beforeAll(async () => {
  srv = await brakemanServer({ name: 'brakeman-ask' })
  srv.openGate()
  await makeProject(srv.api, 'ASK')
  ticket = await makeTicket(srv.api, 'ASK', '1', { title: 'Login keys the session by tenant' })
  // One turn ended: its run and session are what the page holds besides the ticket, for the @ list to offer and a kind to narrow away.
  const first = await srv.ask({ kind: 'overlaps' })
  turnRun = (await srv.until(first.id, ['briefed'])).runId ?? ''
  expect(turnRun).not.toBe('')
})

test.afterAll(async () => {
  await srv?.stop()
})

/** Every request the server holds, newest first. */
async function requests(): Promise<BrakemanRequestSeen[]> {
  const out = await srv.api.ok<{ requests: Array<BrakemanRequestSeen & { note?: string; by?: string; since?: string }> }>('GET', '/api/brakeman/requests')
  return out.requests
}

/** The one request made after `before` (the ids held then), waited for through the API: a request the test made, by its id. */
async function madeSince(before: Set<string>): Promise<BrakemanRequestSeen & { note?: string; by?: string; since?: string }> {
  await expect.poll(async () => (await requests()).filter((r) => !before.has(r.id)).length).toBe(1)
  return (await requests()).find((r) => !before.has(r.id))! as BrakemanRequestSeen & { note?: string; by?: string; since?: string }
}

const ids = async () => new Set((await requests()).map((r) => r.id))

/** Waits until no turn is under way and none waits: what the earlier tests asked has ended. */
async function idle() {
  budget(IDLE_MS)
  await expect
    .poll(async () => {
      const s = await srv.api.ok<{ current: unknown; queue: unknown[] }>('GET', '/api/brakeman')
      return !s.current && !s.queue.length
    }, { timeout: IDLE_MS })
    .toBe(true)
}

/** The page's bar of a size, once the state is read and it can ask. */
async function bar(page: Page, size: 'full' | 'foot'): Promise<Locator> {
  const b = page.locator(`[data-brakeman-ask][data-ask-size="${size}"]`)
  await expect(b).toBeVisible()
  await expect(b.locator('[data-ask-field]')).toBeEnabled()
  return b
}

test('a typed line is a question about what it names with @, sent under the person\'s name', async ({ page }) => {
  await srv.open(page, '/brakeman', { name: NAME })
  const b = await bar(page, 'full')
  const field = b.locator('[data-ask-field]')
  await field.click()
  await field.pressSequentially(`which tests cover @${ticket}`)
  const list = b.locator('[data-ask-mentions]')
  await expect(list.locator(`[data-ask-mention="ticket:${ticket}"]`)).toBeVisible()
  await field.press('Enter')
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await expect(b.locator(`[data-ask-subject="ticket:${ticket}"]`)).toBeVisible()
  await expect(field).toHaveValue('which tests cover ')
  await field.pressSequentially('first?')
  await expect(field).toHaveValue('which tests cover first?')
  const before = await ids()
  await b.locator('[data-ask-send]').click()
  const r = await madeSince(before)
  expect(r.kind).toBe('ask')
  expect(r.subjects).toEqual([`ticket:${ticket}`])
  expect(r.note).toBe('which tests cover first?')
  expect(r.by).toBe(NAME)
  // Kept, the bar is empty again.
  await expect(field).toHaveValue('')
  await expect(b.locator('[data-ask-subject]')).toHaveCount(0)
})

test('a chip with … sets its kind and opens the @ list narrowed to what the kind takes; the line after it is the note', async ({ page }) => {
  await srv.open(page, '/brakeman', { name: NAME })
  const b = await bar(page, 'full')
  // A line's @ offers what the page holds of every kind: more than tickets.
  const field = b.locator('[data-ask-field]')
  await field.click()
  await field.pressSequentially('@')
  await expect(b.locator('[data-ask-mentions] [data-ask-mention]:not([data-ask-mention^="ticket:"])').first()).toBeVisible()
  await field.press('Escape')
  await field.fill('')
  await expect(field).toHaveValue('')
  await b.locator('[data-ask-chip="propose_crew"]').click()
  await expect(b.locator('[data-ask-kind]')).toHaveText('Propose a crew')
  const list = b.locator('[data-ask-mentions]')
  const option = list.locator(`[data-ask-mention="ticket:${ticket}"]`)
  await expect(option).toBeVisible()
  // Propose a crew takes a ticket alone: nothing else is offered.
  for (const ref of await list.locator('[data-ask-mention]').evaluateAll((els) => els.map((e) => e.getAttribute('data-ask-mention') ?? ''))) expect(ref).toMatch(/^ticket:/)
  // Before a subject, Ask says what the kind needs, in the server's words, and does not send.
  await expect(b.locator('[data-ask-problem]')).toHaveText('Propose a crew names one subject.')
  await expect(b.locator('[data-ask-send]')).toBeDisabled()
  await option.click()
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await expect(field).toHaveValue('')
  await field.pressSequentially('two people at most')
  await expect(field).toHaveValue('two people at most')
  await expect(b.locator('[data-ask-problem]')).toHaveCount(0)
  const before = await ids()
  await b.locator('[data-ask-send]').click()
  const r = await madeSince(before)
  expect(r.kind).toBe('propose_crew')
  expect(r.subjects).toEqual([`ticket:${ticket}`])
  expect(r.note).toBe('two people at most')
  expect(r.by).toBe(NAME)
})

test('a briefing sends at once, and from another time through its caret', async ({ page }) => {
  await srv.open(page, '/brakeman', { name: NAME })
  const b = await bar(page, 'full')
  const chip = b.locator('[data-ask-chip="briefing"]')
  await expect(chip).toHaveText(/^Brief me since /)
  let before = await ids()
  await chip.click()
  let r = await madeSince(before)
  expect(r.kind).toBe('briefing')
  expect(r.since).toBeUndefined()
  await b.locator('[data-ask-since-open]').click()
  const panel = page.locator('[data-ask-since-panel]')
  await expect(panel).toBeVisible()
  await atRest(panel)
  await panel.locator('[data-ask-since]').fill('2026-11-16T18:00')
  before = await ids()
  await panel.locator('[data-ask-since-send]').click()
  r = await madeSince(before)
  expect(r.kind).toBe('briefing')
  // The browser's 18:00, as the server takes it: RFC 3339 in UTC.
  expect(r.since).toBe(await page.evaluate(() => new Date('2026-11-16T18:00').toISOString().replace('.000Z', 'Z')))
})

test('a click on Ask while the @ list is open sends the line as typed', async ({ page }) => {
  await srv.open(page, '/brakeman', { name: NAME })
  const b = await bar(page, 'full')
  const field = b.locator('[data-ask-field]')
  await field.click()
  // A ref typed whole is always offered, so the list is open with a match when Ask is clicked.
  await field.pressSequentially(`explain @run:${turnRun}`)
  await expect(b.locator(`[data-ask-mentions] [data-ask-mention="run:${turnRun}"]`)).toBeVisible()
  const before = await ids()
  await b.locator('[data-ask-send]').click()
  const r = await madeSince(before)
  expect(r.kind).toBe('ask')
  expect(r.note).toBe(`explain @run:${turnRun}`)
  expect(r.by).toBe(NAME)
  await expect(field).toHaveValue('')
  await expect(page.getByRole('listbox')).toHaveCount(0)
})

test("the page's ?kind=&subject= fills the bar and sends nothing until Ask", async ({ page }) => {
  const before = await ids()
  await srv.open(page, `/brakeman?kind=draft_tickets&subject=${encodeURIComponent(`ticket:${ticket}`)}`, { name: NAME })
  const b = await bar(page, 'full')
  await expect(b.locator('[data-ask-kind]')).toHaveText('Draft tickets')
  await expect(b.locator(`[data-ask-subject="ticket:${ticket}"]`)).toBeVisible()
  await expect(b.locator('[data-ask-send]')).toBeEnabled()
  expect([...(await ids())].filter((id) => !before.has(id))).toEqual([])
  await b.locator('[data-ask-send]').click()
  const r = await madeSince(before)
  expect(r.kind).toBe('draft_tickets')
  expect(r.subjects).toEqual([`ticket:${ticket}`])
})

test("a refusal says the server's words under the bar", async ({ page }) => {
  // The queue full: the turn under way held at the gate, eight behind it, and the ninth refused. The earlier tests' turns end first
  // (a turn past the gate goes on and would free a place in the queue), each within the stub's own short turn.
  await idle()
  srv.shutGate()
  const asked: string[] = []
  try {
    for (let i = 0; i < 12; i++) {
      const out = await srv.api.call<{ request?: { id: string }; error?: { code?: string } }>('POST', '/api/brakeman/requests', { opId: `e2e-full-${process.pid}-${i}`, kind: 'overlaps' })
      if (out.status === 429) break
      expect(out.status).toBeLessThan(300)
      asked.push(out.body.request!.id)
    }
    await srv.open(page, '/brakeman', { name: NAME })
    const b = await bar(page, 'full')
    const field = b.locator('[data-ask-field]')
    await field.fill('anything new?')
    await expect(field).toHaveValue('anything new?')
    await b.locator('[data-ask-send]').click()
    await expect(b.locator('[data-ask-problem]')).toHaveText('8 requests wait already')
    // The line stays for the person to send again.
    await expect(field).toHaveValue('anything new?')
  } finally {
    for (const id of asked) await srv.api.ok('POST', `/api/brakeman/requests/${id}/cancel`, { opId: `e2e-cancel-${id}` })
    srv.openGate()
  }
})

test("the palette's rows ask from where the person is, under their name, and the toast's Open leads to the request", async ({ page }) => {
  await srv.open(page, `/dispatch/ASK/1`, { name: NAME })
  await expect(page.locator('[data-ticket-title]')).toHaveText('Login keys the session by tenant')
  await expect(page.locator('[data-sidebar-brakeman]')).toBeVisible()
  await expect(page.locator('#dashboard-sidebar-main')).toBeAttached()
  const before = await ids()
  await page.keyboard.press('Alt+/')
  const palette = page.locator('[data-palette]')
  await expect(palette).toBeVisible()
  await palette.locator('input[data-palette-input]').fill('propose a crew for this ticket')
  const row = palette.locator('[data-palette-row="brakeman:propose-crew"]')
  await expect(row).toBeVisible()
  await row.click()
  const r = await madeSince(before)
  expect(r.kind).toBe('propose_crew')
  expect(r.subjects).toEqual([`ticket:${ticket}`])
  expect(r.by).toBe(NAME)
  // The person's own press, said where they are: the page stays.
  const toast = page.getByRole('region', { name: /^Notifications/ }).locator('li').filter({ hasText: `Asked as ${r.id}` })
  await expect(toast).toBeVisible()
  await expect(page).toHaveURL(/\/dispatch\/ASK\/1$/)
  await toast.getByRole('link', { name: 'Open' }).click()
  await expect(page).toHaveURL(new RegExp(`/brakeman\\?request=${r.id}$`))
})

test('G then B goes to the Brakeman page', async ({ page }) => {
  await srv.open(page, '/agents', { name: NAME })
  // The layout's keys listen once its sidebar is drawn, and G B once the layout has read that the server has Brakeman.
  await expect(page.locator('#dashboard-sidebar-main')).toBeAttached()
  await expect(page.locator('[data-sidebar-brakeman]')).toBeVisible()
  await pressInTurn(page, 'g', 'b')
  await expect(page).toHaveURL(/\/brakeman$/)
})

for (const theme of ['dark', 'light'] as const) {
  test(`the bar and its @ list drawn in ${theme}: every text over its floor`, async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await srv.open(page, '/brakeman', { name: NAME })
    await useTheme(page, theme)
    const b = await bar(page, 'full')
    const field = b.locator('[data-ask-field]')
    await field.click()
    await field.pressSequentially('@')
    const list = b.locator('[data-ask-mentions]')
    // Before anything is typed after the @, a few of each kind: the ticket among them, past the stub turns' sessions.
    await expect(list.locator(`[data-ask-mention="ticket:${ticket}"]`)).toBeVisible()
    await atRest(list)
    const { judged, failures } = await walk(page, b)
    expect(judged, 'the walk judged some text').toBeGreaterThan(0)
    expect(failures, `${theme}: text under its floor (the ten worst):\n${worst(failures)}`).toEqual([])
  })
}

test.describe('on a phone', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

  test("More is lit on its own pages, and its Brakeman row counts the briefs not yet opened, in neutral ink", async ({ page }) => {
    await idle()
    const listed = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/brakeman/requests' && r.ok())
    await srv.open(page, '/dispatch', { name: NAME })
    const more = page.locator('[data-bottom-bar] [data-bottom-tab="more"]')
    await expect(more).toHaveAttribute('aria-current', 'page')
    // The count as it stands once the layout has read the server's requests (the More sheet's Brakeman row is there), then one
    // brief more: the count of what this test made is the step it takes, whatever earlier tests' briefs left unopened.
    await more.tap()
    const row = page.locator('[data-bottom-more] [data-more-page="brakeman"]')
    await expect(row).toBeVisible()
    const count = row.locator('[data-more-count]')
    await listed
    await page.evaluate(() => new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done))))
    const before = (await count.count()) ? Number(await count.textContent()) : 0
    await page.keyboard.press('Escape')
    await expect(page.locator('[data-bottom-more]')).toHaveCount(0)
    const asked = await srv.ask({ kind: 'overlaps' })
    await srv.until(asked.id, ['briefed'])
    await more.tap()
    await expect(count).toHaveText(String(before + 1))
    await expect(count).not.toHaveClass(/warning/)
    await row.tap()
    await expect(page).toHaveURL(/\/brakeman$/)
    await expect(more).toHaveAttribute('aria-current', 'page')
    // A tab's page is the tab's: More is not lit there.
    await page.locator('[data-bottom-bar] [data-bottom-tab="yard"]').tap()
    await expect(page).toHaveURL(/\/yard/)
    await expect(more).not.toHaveAttribute('aria-current', 'page')
  })

  test("a press on a foot chip where the press takes no focus (WebKit's order, dispatched by hand) still asks", async ({ page }) => {
    await idle()
    await srv.open(page, '/brakeman', { name: NAME })
    const b = await bar(page, 'foot')
    await b.locator('[data-ask-field]').focus()
    const chip = b.locator('[data-ask-chip="overlaps"]')
    await expect(chip).toBeVisible()
    const before = await ids()
    // WebKit gives a pressed button no focus: the press clears the focus (a focusout with no related target) unless its mousedown
    // is prevented, the page redraws, and the click goes only to a chip still in the page.
    const pressed = await chip.evaluate(async (el) => {
      const down = new MouseEvent('mousedown', { bubbles: true, cancelable: true })
      el.dispatchEvent(down)
      if (!down.defaultPrevented) (document.activeElement as HTMLElement | null)?.blur()
      await new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done)))
      el.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, cancelable: true }))
      if (el.isConnected) (el as HTMLElement).click()
      return { prevented: down.defaultPrevented, connected: el.isConnected }
    })
    expect(pressed).toEqual({ prevented: true, connected: true })
    const r = await madeSince(before)
    expect(r.kind).toBe('overlaps')
    expect(r.by).toBe(NAME)
  })

  for (const theme of ['dark', 'light'] as const) {
    test(`the foot bar in ${theme}: 44 px to tap, its chips on focus, nothing sideways, every text over its floor`, async ({ page }) => {
      await srv.open(page, '/brakeman', { name: NAME })
      await useTheme(page, theme, '[data-bottom-bar]')
      const b = await bar(page, 'foot')
      // The label is there for a reader that does not see it, and hidden.
      await expect(b.locator('label')).toHaveClass(/sr-only/)
      await expect(b.locator('[data-ask-chip]')).toHaveCount(0)
      await tapTarget(b.locator('[data-ask-field], [data-ask-send]'))
      await b.locator('[data-ask-field]').tap()
      await expect(b.locator('[data-ask-chip]').first()).toBeVisible()
      await tapTarget(b.locator('[data-ask-chip], [data-ask-since-open]'))
      await noSideways(page, { timeout: MEASURE_WAIT_MS })
      const { judged, failures } = await walk(page, b)
      expect(judged, 'the walk judged some text').toBeGreaterThan(0)
      expect(failures, `${theme}: text under its floor (the ten worst):\n${worst(failures)}`).toEqual([])
    })
  }
})
