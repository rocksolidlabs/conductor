import type { Page, Route } from '@playwright/test'
import { dayPage, expect, test } from './fixtures'
import { inboxItem, stubInbox, summary } from './inbox-stub'
import { inside, uncut } from './layout'

// The page of what needs you with nothing on it (UX1 PR-6, docs/plans/ux-review.md, P24): one column that says nothing needs you,
// when the last one closed, the four kinds that show up here and what was decided in the last day, with a way to every decision in
// History. "Pick an item" only while there is an item to pick. A read that failed, and a switchyard's refusal, are never zero. The
// inbox is stubbed in the browser (inbox-stub.ts); the first page's Recent, the failing read and the switchyard's refusal are a route
// of the spec's own over /api/inbox, registered after the stub's (the later route answers).

const DESK = { width: 1440, height: 900 }
const PHONE = { width: 390, height: 844, touch: true }
const KINDS = ['question', 'plan', 'task', 'decision']
const HOUR = 3_600_000

/** A page on the day's server, the inbox stubbed with `items`, `answer` (when given) answering /api/inbox over it, at /decisions. */
async function decisions(browser: Parameters<typeof dayPage>[0], size: typeof DESK | typeof PHONE, items = [] as ReturnType<typeof inboxItem>[], answer?: (route: Route) => Promise<void>): Promise<Page> {
  const page = await dayPage(browser, { ...size, path: '/yard' })
  await stubInbox(page, items)
  if (answer) await page.route((u) => u.pathname === '/api/inbox', (route) => (route.request().method() === 'GET' ? answer(route) : route.fallback()))
  await page.goto('/decisions')
  await expect(page.locator(size.width < 1024 ? '[data-bottom-bar]' : '#dashboard-sidebar-main')).toBeVisible()
  return page
}

const json = (route: Route, status: number, body: unknown) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })

/** The zero page read whole: its words, the four kinds in order, no open item's pane, no "Pick an item". */
async function readZero(page: Page) {
  const zero = page.locator('[data-decisions-empty]')
  // A page not yet drawn passes every "hidden": the block first.
  await expect(zero).toBeVisible()
  await expect(zero).toContainText('Nothing needs you now.')
  const kinds = page.locator('[data-decisions-kind]')
  await expect(kinds).toHaveCount(4)
  expect(await kinds.evaluateAll((els) => els.map((e) => e.getAttribute('data-decisions-kind')))).toEqual(KINDS)
  await expect(kinds.first()).toContainText('A question an agent asks')
  await expect(page.locator('[data-decisions-item]')).toHaveCount(0)
  await expect(page.getByText('Pick an item')).toHaveCount(0)
  await expect(page.locator('[data-decisions-history]')).toBeVisible()
}

/** At a desktop's width: the one column is wider than the two columns' 24 rem list, and inside the page's panel. */
async function oneColumn(page: Page) {
  const list = page.locator('[data-decisions-list]')
  const { parts } = await inside(list, page.locator('#dashboard-panel-decisions'))
  expect(parts[0]!.right - parts[0]!.left, 'the one column is wider than the list beside an item').toBeGreaterThan(24 * 16 + 1)
}

test('with nothing waiting, at a desktop: one column says so, names the four kinds, and stays one column in the other theme', async ({ day, browser }) => {
  void day
  const page = await decisions(browser, DESK)
  await readZero(page)
  await oneColumn(page)

  // The theme toggled from your menu: the column measured again after the redraw.
  await page.locator('[data-header-account]').click()
  await page.getByRole('menuitem', { name: 'Toggle theme' }).click()
  await expect(page.locator('html')).not.toHaveClass(/\bdark\b/)
  await expect(page.getByRole('menuitem')).toHaveCount(0)
  await readZero(page)
  await oneColumn(page)
})

test('with nothing waiting, on a phone: every line of the zero block is whole', async ({ day, browser }) => {
  void day
  const page = await decisions(browser, PHONE)
  await readZero(page)
  await uncut(page.locator('[data-decisions-empty] p, [data-decisions-empty] h2, [data-decisions-kind] span.block'))
  await uncut(page.locator('[data-decisions-history]'))
})

test('one decision closed two hours ago: Decided in the last day, when it closed, and the way to every decision in History', async ({ day, browser }) => {
  void day
  const record = inboxItem('decision')
  const closed = { ...summary(record), ref: record.ref, state: 'applied' }
  const page = await decisions(browser, DESK, [], (route) =>
    // The closed time from the clock at the route: two hours before the read.
    json(route, 200, { items: [], total: 0, recent: [{ ...closed, closed: new Date(Date.now() - 2 * HOUR).toISOString() }], at: new Date().toISOString() }),
  )
  await readZero(page)
  const recent = page.locator('[data-decisions-recent]')
  await expect(recent.getByRole('heading')).toHaveText('Decided in the last day')
  await expect(recent.locator(`[data-recent-decision="${closed.ref}"]`)).toBeVisible()
  await expect(page.locator('[data-decisions-last]')).toHaveText('The last one closed 2h ago.')
  await page.locator('[data-decisions-history]').click()
  await expect(page).toHaveURL(/\/history\?q=class%3Adecided$/)
  await expect(page.locator('[data-history-input]')).toHaveValue('class:decided')
})

test('a read that fails is not zero: the could-not-read alert, and no zero page', async ({ day, browser }) => {
  void day
  const page = await decisions(browser, DESK, [], (route) => json(route, 500, { error: { code: 'internal', message: 'the inbox could not be read' } }))
  await expect(page.locator('[data-decisions-error]')).toBeVisible()
  await expect(page.locator('[data-decisions-empty]')).toHaveCount(0)
  await expect(page.locator('[data-decisions-kinds]')).toHaveCount(0)
})

test("a switchyard's refusal is not zero: the unavailable alert, and no zero page", async ({ day, browser }) => {
  void day
  const page = await decisions(browser, DESK, [], (route) => json(route, 403, { error: { code: 'switchyard', message: 'Decisions are answered where the work runs.' } }))
  await expect(page.locator('[data-decisions-unavailable]')).toBeVisible()
  await expect(page.locator('[data-decisions-empty]')).toHaveCount(0)
  await expect(page.locator('[data-decisions-kinds]')).toHaveCount(0)
})

test('one open question at a desktop: the list beside it, the question opened by itself, no "Pick an item"', async ({ day, browser }) => {
  void day
  const q = inboxItem('question')
  const page = await decisions(browser, DESK, [q])
  await expect(page.locator(`[data-inbox-row="${q.ref}"]`)).toBeVisible()
  await expect(page.locator(`[data-inbox-item="${q.ref}"]`)).toBeVisible()
  await expect(page.locator('[data-decisions-empty]')).toHaveCount(0)
  await expect(page.getByText('Pick an item')).toHaveCount(0)
  const [list, pane] = [(await page.locator('[data-decisions-list]').boundingBox())!, (await page.locator('[data-decisions-item]').boundingBox())!]
  expect(list.width, 'the list is the 24 rem column').toBeLessThanOrEqual(24 * 16 + 0.5)
  expect(pane.x, 'the pane is beside the list').toBeGreaterThanOrEqual(list.x + list.width)
})

test('a run\'s decision answered and not yet applied alone at a desktop: it opens nothing, and the pane asks to pick it', async ({ day, browser }) => {
  void day
  const d = inboxItem('decision', { state: 'answered' })
  const page = await decisions(browser, DESK, [d])
  await expect(page.locator(`[data-inbox-row="${d.ref}"]`)).toBeVisible()
  await expect(page.getByText('Pick an item')).toBeVisible()
  await expect(page).toHaveURL(/\/decisions$/)
  await expect(page.locator('[data-decisions-empty] [data-decisions-kinds]')).toHaveCount(0)
})
