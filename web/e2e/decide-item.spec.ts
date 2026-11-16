import type { Page } from '@playwright/test'
import { dayPage, expect, test, useTheme } from './fixtures'
import { inboxItem, stubInbox, type StubItem } from './inbox-stub'
import { probeColour } from './layout'

// The list and the item in a person's words (UX1 PR-21, docs/plans/ux-review.md, P21, D12): each row says its kind; on a wide
// window the oldest item opens by itself (never below lg, never over an item in the query, and a row picked while it is read wins);
// an item prints its title once, a question's fine print folds under Details, and Approve and Retry are the main action's colour.
// The inbox is stubbed in the browser (inbox-stub.ts), each brief in the shape the server writes (internal/api/inbox.go).

const DESK = { width: 1440, height: 900 }
const PHONE = { width: 390, height: 844, touch: true }
const MIN = 60_000
const ago = (m: number) => new Date(Date.now() - m * MIN).toISOString()
const NOW = 'no longer open: answered by Ana'

/** The day's items: an open question oldest, then a run's decision, a crew member's question, a plan, a failed task, a decision answered and not yet applied, and a question no longer open. */
function items() {
  const question = inboxItem('question', { since: ago(30) })
  const decision = inboxItem('decision', { since: ago(25) })
  const member = inboxItem('question', { since: ago(20) })
  // A crew member's question, as the server briefs it: the title, then "; it is attempt …" (inbox.go, questionItem).
  member.brief = `${member.title}; it is attempt build.1 of run crew-0000aaaa-11112222 for STB-9`
  const plan = inboxItem('plan', { since: ago(15) })
  const task = inboxItem('task', { since: ago(10) })
  const answered = inboxItem('decision', { since: ago(8), state: 'answered' })
  // A question no longer open: its brief is its `now` (inbox.go, a question past its turn).
  const gone = inboxItem('question', { since: ago(5) })
  gone.item = { title: gone.title.replace(/asks$/, 'asked'), brief: NOW, now: NOW, actions: [] }
  return { question, decision, member, plan, task, answered, gone }
}

const all = (o: ReturnType<typeof items>): StubItem[] => Object.values(o)
/** The decision page's address with `ref` the open item, as the router spells it (it may leave `:` and `/` unencoded). */
const itemURL = (ref: string) => new RegExp(`/decisions\\?item=${ref.replace(/[.*+?^${}()|[\]\\]/g, '\\$&').replace(/:/g, '(:|%3A)').replace(/\//g, '(/|%2F)')}$`)

async function decisions(browser: Parameters<typeof dayPage>[0], size: typeof DESK | typeof PHONE, list: StubItem[]): Promise<Page> {
  const page = await dayPage(browser, { ...size, path: '/yard' })
  await stubInbox(page, list)
  return page
}

/** How many times `text` appears in what `l` shows. */
async function times(l: ReturnType<Page['locator']>, text: string): Promise<number> {
  return (await l.innerText()).split(text).length - 1
}

test("at a desktop: each row says its kind, the oldest opens by itself, a row picked while it is read wins, and each item says itself once", async ({ day, browser }) => {
  void day
  const o = items()
  const page = await decisions(browser, DESK, all(o))
  // The oldest question's read held at a gate (a route of the spec's own, over the stub's): a row picked meanwhile wins.
  let release!: () => void
  const gate = new Promise<void>((r) => (release = r))
  let held = 0
  const isQuestion = (u: URL) => u.pathname === '/api/inbox/item' && u.searchParams.get('ref') === o.question.ref
  await page.route(isQuestion, async (route) => {
    held++
    await gate
    await route.fallback()
  })
  await page.goto('/decisions')
  await expect(page.locator('#dashboard-sidebar-main')).toBeVisible()

  // Every row carries its kind.
  for (const it of all(o)) await expect(page.locator(`[data-inbox-row="${it.ref}"] [data-inbox-row-kind]`)).toHaveAttribute('data-inbox-row-kind', it.kind)
  await expect(page.locator(`[data-inbox-row="${o.decision.ref}"] [data-inbox-row-kind]`)).toHaveText('Decision')

  // The oldest open item opens by itself, through the address; its read is held.
  await expect(page).toHaveURL(itemURL(o.question.ref))
  await expect.poll(() => held, { message: "the question's read reached the gate" }).toBeGreaterThan(0)
  await expect(page.getByText('Pick an item')).toHaveCount(0)

  // The plan's row picked while the question is read: the plan is drawn, the question never, and the address names the plan.
  await page.locator(`[data-inbox-row="${o.plan.ref}"]`).click()
  const planItem = page.locator(`[data-inbox-item="${o.plan.ref}"]`)
  await expect(planItem).toBeVisible()
  await expect(page).toHaveURL(itemURL(o.plan.ref))
  const answeredLate = page.waitForResponse((r) => isQuestion(new URL(r.url())))
  release()
  await answeredLate
  await expect(page.locator(`[data-inbox-item="${o.question.ref}"]`)).toHaveCount(0)
  await expect(planItem).toBeVisible()
  await page.reload()
  await expect(planItem).toBeVisible()
  await expect(page).toHaveURL(itemURL(o.plan.ref))
  // A plan's fine print stays in view: what an approval authorizes.
  await expect(planItem.locator('[data-action-scope]')).toHaveCount(2)
  await expect(planItem.locator('[data-inbox-scopes-toggle]')).toHaveCount(0)

  // The plain question: its title once, no brief that says it again; its fine print under Details.
  await page.locator(`[data-inbox-row="${o.question.ref}"]`).click()
  const q = page.locator(`[data-inbox-item="${o.question.ref}"]`)
  await expect(q.locator('[data-inbox-title]')).toHaveText(o.question.title)
  await expect(q.locator('[data-inbox-brief-text]')).toHaveCount(0)
  expect(await times(q, o.question.title)).toBe(1)
  await expect(q.locator('[data-action-scope]')).toHaveCount(0)
  await q.locator('[data-inbox-scopes-toggle]').click()
  await expect(q.locator('[data-action-scope]')).toHaveCount(o.question.choices.length + 1)

  // A crew member's question: the brief says what the title does not.
  await page.locator(`[data-inbox-row="${o.member.ref}"]`).click()
  const m = page.locator(`[data-inbox-item="${o.member.ref}"]`)
  await expect(m.locator('[data-inbox-brief-text]')).toHaveText('It is attempt build.1 of run crew-0000aaaa-11112222 for STB-9')
  expect(await times(m, o.member.title)).toBe(1)

  // A question no longer open: its `now` said once.
  await page.locator(`[data-inbox-row="${o.gone.ref}"]`).click()
  const g = page.locator(`[data-inbox-item="${o.gone.ref}"]`)
  await expect(g.locator('[data-inbox-now]')).toBeVisible()
  expect(await times(g, NOW)).toBe(1)

  // A run's decision: no title of its own; its card's question heads it, once.
  await page.locator(`[data-inbox-row="${o.decision.ref}"]`).click()
  const d = page.locator(`[data-inbox-item="${o.decision.ref}"]`)
  await expect(d.locator('[data-decision-question]')).toHaveText(o.decision.question)
  await expect(d.locator('[data-inbox-title]')).toHaveCount(0)
  await expect(d.locator('[data-inbox-brief-text]')).toHaveText(o.decision.brief)
  expect(await times(d, o.decision.question)).toBe(1)
})

/** Approve on the plan and Retry on the task draw the main action's colour. */
async function mainActions(page: Page, o: ReturnType<typeof items>) {
  const primary = await probeColour(page, 'var(--ui-primary)')
  for (const [it, action] of [[o.plan, 'approve'], [o.task, 'retry']] as const) {
    await page.locator(`[data-inbox-row="${it.ref}"]`).click()
    const b = page.locator(`[data-inbox-item="${it.ref}"] [data-inbox-action="${action}"]`)
    await expect(b).toBeVisible()
    expect(await probeColour(page, await b.evaluate((e) => getComputedStyle(e).backgroundColor)), `${action}'s fill`).toBe(primary)
  }
}

test('Approve and Retry are the main action, in both themes', async ({ day, browser }) => {
  void day
  const o = items()
  const page = await decisions(browser, DESK, all(o))
  await page.goto('/decisions')
  await expect(page).toHaveURL(itemURL(o.question.ref))
  await mainActions(page, o)
  await useTheme(page, 'light')
  await mainActions(page, o)
})

test('on a phone nothing opens by itself; the same page made wide opens the oldest', async ({ day, browser }) => {
  void day
  const o = items()
  const page = await decisions(browser, PHONE, all(o))
  await page.goto('/decisions')
  await expect(page.locator(`[data-inbox-row="${o.question.ref}"]`)).toBeVisible()
  await expect(page).toHaveURL(/\/decisions$/)
  await expect(page.locator('[data-decisions-list]')).toBeVisible()
  await page.setViewportSize(DESK)
  await expect(page).toHaveURL(itemURL(o.question.ref))
  await expect(page.locator(`[data-inbox-item="${o.question.ref}"]`)).toBeVisible()
})

test("a run's decision says its question while the item and its record are read, and when the record's read fails", async ({ day, browser }) => {
  void day
  const decision = inboxItem('decision', { since: ago(25) })
  const id = decision.ref.slice('decision:'.length)
  const page = await decisions(browser, DESK, [decision])
  // The item's read and its record's reads held at gates (routes of the spec's own, over the stub's); the record's reads then fail
  // as a server error would, until the spec lets them go on to the stub.
  let releaseItem!: () => void
  const itemGate = new Promise<void>((r) => (releaseItem = r))
  let itemHeld = 0
  await page.route(
    (u) => u.pathname === '/api/inbox/item' && u.searchParams.get('ref') === decision.ref,
    async (route) => {
      itemHeld++
      await itemGate
      await route.fallback()
    },
  )
  let failRecord!: () => void
  const recordGate = new Promise<void>((r) => (failRecord = r))
  let recordReads = 0
  let recordServed = false
  await page.route(
    (u) => u.pathname === `/api/decisions/${id}`,
    async (route) => {
      recordReads++
      if (recordServed) return route.fallback()
      await recordGate
      await route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: { code: 'internal', message: 'the record could not be read' } }) })
    },
  )
  await page.goto('/decisions')
  await expect(page).toHaveURL(itemURL(decision.ref))
  const pane = page.locator('[data-decisions-item]')

  // The item read whole is held: the pane says the list's title for it, the question, and that it is being read.
  await expect.poll(() => itemHeld, { message: "the item's read reached its gate" }).toBeGreaterThan(0)
  await expect(pane.locator('[data-inbox-reading-title]')).toHaveText(decision.title)
  await expect(pane.locator('[data-inbox-reading]')).toContainText('Reading the item…')
  expect(decision.title, "the server titles a run's decision by its question").toBe(decision.question)

  // The item read, its record held: the item's title says the question while the record is read.
  releaseItem()
  const d = page.locator(`[data-inbox-item="${decision.ref}"]`)
  await expect(d).toBeVisible()
  await expect.poll(() => recordReads, { message: "the record's read reached its gate" }).toBeGreaterThan(0)
  await expect(d.locator('[data-decision-question]')).toHaveCount(0)
  await expect(d.locator('[data-inbox-title]')).toHaveText(decision.question)
  await expect(d).toContainText(`Reading ${id}…`)

  // The record's read failed: the question still heads the item, beside the warning and its Read again.
  failRecord()
  await expect(d.locator('[data-inbox-record-error]')).toBeVisible()
  await expect(d.locator('[data-inbox-title]')).toHaveText(decision.question)

  // Read again: the card is drawn, its question heads the item once, and the title leaves.
  recordServed = true
  await d.locator('[data-inbox-record-error]').getByRole('button', { name: 'Read again' }).click()
  await expect(d.locator('[data-decision-question]')).toHaveText(decision.question)
  await expect(d.locator('[data-inbox-title]')).toHaveCount(0)
  expect(await times(d, decision.question)).toBe(1)
})
