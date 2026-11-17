import type { Locator, Page } from '@playwright/test'
import { dayPage, expect, stopStrays, test } from './fixtures'
import { budget, decisionsOf, decisionUntil, makeTicket, propose, type DecisionServer } from './decisions-helpers'
import { mark, marked } from './dispatch-helpers'
import { noSideways, probeColour } from './layout'
import { ownServer } from './own-server'

// A ticket's strip of sections; a plan folds once approved (UX1's PR-16, docs/plans/ux-review.md), on a server of its own, since it
// answers a decision. One ticket with a 60-line description and three comments, and a crew of two members whose task `deploy` waits for
// `build` behind a decision `ship`; its revision proposed, approved and launched through the routes the Plan section calls, and the gate
// waited for on the record. The strip stays at the top of the page's scroller, a press brings its section up under it, its states follow
// the page's reads with no reload; an approved revision that holds is folded to one line, and opens when its approval no longer holds.
test.describe.configure({ mode: 'serial' })

const PROJECT = 'STR'
const STEP_MS = 15_000
const DESCRIPTION = Array.from({ length: 60 }, (_, i) => `Line ${i + 1} of what the ticket asks: enough of it that the page scrolls.`).join('\n\n')

let srv: DecisionServer
let ticket = ''
let path = ''
let planId = ''
let runId = ''
let gate = ''

test.beforeAll(async () => {
  test.setTimeout(test.info().timeout + 130_000)
  srv = await ownServer('ticket-strip')
  await stopStrays(srv.api, [])
  const api = srv.api
  ticket = await makeTicket(api, PROJECT, 'Deploy the stub build behind its gate')
  path = `/dispatch/${PROJECT}/${ticket.split('-')[1]}`
  const t = (await api.ok<{ ticket: { rev: number } }>('GET', `/api/dispatch/tickets/${ticket}`)).ticket
  await api.ok('POST', `/api/dispatch/tickets/${ticket}/edit`, { opId: 'str-desc', expectedRev: t.rev, set: { description: DESCRIPTION } })
  for (const [i, body] of ['The build is green on the branch.', 'Deploy waits for the ship decision.', 'Ready when you are.'].entries()) {
    await api.ok('POST', `/api/dispatch/tickets/${ticket}/comments`, { opId: `str-c${i + 1}`, byName: 'Ana', body })
  }
  const crew = (
    await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
      name: 'e2e ticket strip',
      goal: 'build, then deploy once approved',
      cwd: '',
      where: 'server',
      isolation: 'worktree',
      openAfterLaunch: false,
      yolo: null,
      members: [
        { name: 'builder', agentId: 'worker', prompt: '', start: { when: 'task' } },
        { name: 'deployer', agentId: 'worker-cli', prompt: '', start: { when: 'task' } },
      ],
      tasks: [
        { id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.' },
        { id: 'deploy', member: 'deployer', kind: 'work', instructions: 'Deploy it.', needs: ['build'] },
      ],
    })
  ).crew.id
  planId = await propose(api, ticket, crew, [{ id: 'ship', question: 'May deploy go out?', kind: 'approval', before: ['deploy'] }])
  await api.ok('POST', `/api/dispatch/plans/${planId}/approve`, { opId: 'str-ap1', note: 'Go.' })
  // The route answers once the run's first ledger transaction, its gate's opening among it, is committed: the launch's bound, 2 min.
  runId = (await api.ok<{ launch: { runId: string } }>('POST', `/api/dispatch/tickets/${ticket}/launch`, { opId: 'str-l1', plan: planId })).launch.runId
  const gates = await decisionsOf(api, runId)
  expect(gates.map((d) => [d.gate, d.state])).toEqual([['ship', 'open']])
  gate = gates[0]!.id
  await decisionUntil(api, gate, 'open')
})

test.afterAll(async () => {
  // The run first, then the server: what a test started ends before its directories go.
  if (!srv) return
  if (runId) await srv.api.stopRun(runId)
  await srv.stop()
})

/** The ticket's page on the spec's server, its strip drawn with the comments counted. */
async function ticketPage(browser: Parameters<typeof dayPage>[0], o: Parameters<typeof dayPage>[1] = {}, query = ''): Promise<Page> {
  const page = await dayPage(browser, { server: srv.started, path: `${path}${query}`, ...o })
  if (!query) await expect(page.locator('[data-strip-item="comments"]')).toHaveAttribute('data-count', /\d/)
  return page
}

/** The nearest ancestor of the ticket that scrolls: the page's body. */
function scroller(page: Page): Locator {
  return page.locator('[data-ticket]').locator('xpath=ancestor::*[contains(@class, "overflow-y-auto")][1]')
}

/** The scroller taken to its end, waited for until it is there. */
async function toEnd(page: Page): Promise<void> {
  const s = scroller(page)
  await s.evaluate((el) => (el.scrollTop = el.scrollHeight))
  await expect.poll(() => s.evaluate((el) => Math.abs(el.scrollTop + el.clientHeight - el.scrollHeight) <= 1)).toBe(true)
}

/** The strip's top is the scroller's top, the title above it (a scroll happened), and the properties' top at or below its bottom. */
async function stripAtTop(page: Page, properties = true): Promise<void> {
  const got = await page.evaluate(
    ({ properties }) => {
      const box = (s: string) => document.querySelector(s)!.getBoundingClientRect()
      const scroller = (document.querySelector('[data-ticket]') as HTMLElement).closest('.overflow-y-auto')!.getBoundingClientRect()
      const strip = box('[data-ticket-strip]')
      return { scrollerTop: scroller.top, stripTop: strip.top, stripBottom: strip.bottom, titleBottom: box('[data-ticket-title]').bottom, aside: properties ? box('[data-ticket-properties]').top : Infinity }
    },
    { properties },
  )
  expect(got.titleBottom, 'the title is scrolled above the scroller').toBeLessThan(got.scrollerTop)
  expect(Math.abs(got.stripTop - got.scrollerTop), 'the strip is at the scroller top').toBeLessThanOrEqual(1)
  expect(got.aside, "the properties' top is at or below the strip's bottom").toBeGreaterThanOrEqual(got.stripBottom - 0.5)
}

function item(page: Page, id: string): Locator {
  return page.locator(`[data-strip-item="${id}"]`)
}

test('the strip stays at the top of the page as it scrolls, under the title, with the properties under it, and counts a comment made live', async ({ browser }) => {
  const page = await ticketPage(browser)
  const s = scroller(page)
  const size = await s.evaluate((el) => ({ scroll: el.scrollHeight, client: el.clientHeight }))
  expect(size.scroll, 'the page is long enough to prove the strip stays').toBeGreaterThanOrEqual(2 * size.client)
  await expect(item(page, 'comments').locator('[data-strip-words]')).toHaveText('3')
  await toEnd(page)
  await stripAtTop(page)
  // A comment made by someone else: the strip says so, with no reload, and stays where it was through the redraw.
  await mark(page)
  await srv.api.ok('POST', `/api/dispatch/tickets/${ticket}/comments`, { opId: 'str-c4', byName: 'Priya', body: 'Ship it once the gate is answered.' })
  await expect(item(page, 'comments').locator('[data-strip-words]')).toHaveText('4')
  await expect(item(page, 'comments')).toHaveAttribute('data-count', '4')
  expect(await marked(page)).toBe('kept')
  await toEnd(page)
  await stripAtTop(page)
})

test('each name brings its section up under the strip, with the focus', async ({ browser }) => {
  const page = await ticketPage(browser)
  for (const [id, section] of [
    ['comments', '[data-ticket-comments]'],
    ['decisions', '[data-ticket-decisions]'],
    ['captures', '[data-ticket-captures]'],
  ] as const) {
    await scroller(page).evaluate((el) => (el.scrollTop = 0))
    await expect.poll(() => scroller(page).evaluate((el) => el.scrollTop)).toBe(0)
    await item(page, id).click()
    await expect
      .poll(() =>
        page.evaluate((section) => {
          const s = (document.querySelector('[data-ticket]') as HTMLElement).closest('.overflow-y-auto') as HTMLElement
          const top = document.querySelector(section)!.getBoundingClientRect().top
          const strip = document.querySelector('[data-ticket-strip]')!.getBoundingClientRect().bottom
          const atEnd = Math.abs(s.scrollTop + s.clientHeight - s.scrollHeight) <= 1
          return top >= strip - 0.5 && (top - strip <= 24 || atEnd)
        }, section),
        { message: `${section} up under the strip` },
      )
      .toBe(true)
    await expect(page.locator(`${section}:focus-within, ${section}:focus`)).toHaveCount(1)
  }
})

test("the strip's states follow the page's reads: the gate answered, Decisions says none open with no reload", async ({ browser }) => {
  const page = await ticketPage(browser)
  await expect(item(page, 'plan').locator('[data-strip-words]')).toHaveText('approved')
  await expect(item(page, 'decisions').locator('[data-strip-words]')).toHaveText('1 open')
  await expect(item(page, 'decisions')).toHaveAttribute('data-tone', 'warning')
  await expect(item(page, 'runs').locator('[data-strip-words]')).toContainText('1 needs you')
  await expect(item(page, 'runs')).toHaveAttribute('data-tone', 'warning')
  await mark(page)
  const open = await decisionUntil(srv.api, gate, 'open')
  await srv.api.ok('POST', `/api/decisions/${gate}/respond`, { responseId: 'str-ship', choice: 0, expectedRev: open.rev })
  // Waited for a state that holds: an answer is applied a moment after it is recorded.
  await decisionUntil(srv.api, gate, 'applied')
  await expect(item(page, 'decisions').locator('[data-strip-words]')).toHaveText('none open')
  await expect(item(page, 'decisions')).toHaveAttribute('data-tone', 'neutral')
  expect(await marked(page)).toBe('kept')
})

test('an approved revision that holds is folded to one line, its body one press away, each command printed once; a stale one opens', async ({ browser }) => {
  const page = await ticketPage(browser)
  const featured = page.locator('[data-plan-featured]')
  await expect(featured.locator('[data-plan-fold]')).toBeVisible()
  await expect(featured.locator('[data-plan-body]')).toHaveCount(0)
  const plans = (await srv.api.ok<{ plans: Array<{ id: string; base: string }> }>('GET', `/api/dispatch/tickets/${ticket}/plans`)).plans
  const base = plans.find((p) => p.id === planId)!.base
  const fold = featured.locator('[data-plan-fold]')
  await expect(fold).toContainText('2 members')
  await expect(fold).toContainText(base.slice(0, 12))
  await expect(fold).toContainText('Yolo off')
  await expect(featured.locator('[data-plan-launch]')).toBeVisible()
  await featured.locator('[data-plan-unfold-body]').click()
  await expect(featured.locator('[data-plan-unfold-body]')).toHaveAttribute('aria-expanded', 'true')
  await expect(featured.locator('[data-plan-body]')).toBeVisible()
  await expect(featured.locator('[data-plan-member-command]')).toHaveCount(2)
  await expect(page.locator('[data-people-plan-argv]')).toHaveCount(0)
  await expect(featured.locator('[data-plan-body]')).not.toContainText('Arguments:')
  await featured.locator('[data-plan-unfold-body]').click()
  await expect(featured.locator('[data-plan-body]')).toHaveCount(0)

  // The title edited: the approval no longer holds, and the revision opens with its body, with no reload.
  await mark(page)
  const t = (await srv.api.ok<{ ticket: { rev: number } }>('GET', `/api/dispatch/tickets/${ticket}`)).ticket
  await srv.api.ok('POST', `/api/dispatch/tickets/${ticket}/edit`, { opId: 'str-title', expectedRev: t.rev, set: { title: 'Deploy the stub build, renamed' } })
  await expect(featured).toHaveAttribute('data-plan-stale', /./)
  await expect(featured.locator('[data-plan-body]')).toBeVisible()
  await expect(featured.locator('[data-plan-fold]')).toHaveCount(0)
  // An approval that no longer holds waits for the operator again: the one list holds the revision, and the strip's Plan says so.
  await expect(item(page, 'plan').locator('[data-strip-words]')).toHaveText('r1 waits for your approval')
  await expect(item(page, 'plan')).toHaveAttribute('data-tone', 'warning')
  expect(await marked(page)).toBe('kept')
})

for (const theme of ['dark', 'light'] as const) {
  test(`in the ${theme} theme the strip is drawn on a background of its own, over what scrolls under it`, async ({ browser }) => {
    const page = await ticketPage(browser, { theme })
    await toEnd(page)
    const bg = await page.locator('[data-ticket-strip]').evaluate((el) => getComputedStyle(el).backgroundColor)
    expect(await probeColour(page, bg), "the strip's background is opaque").toMatch(/^rgb\(/)
  })
}

test('on a phone (390 x 844, touch) the strip is under the title, nothing but it scrolls sideways, and scrolled it stays at the top; the Review view has none', async ({ browser }) => {
  budget(STEP_MS)
  const page = await ticketPage(browser, { width: 390, height: 844, touch: true })
  const before = await page.evaluate(() => ({ strip: document.querySelector('[data-ticket-strip]')!.getBoundingClientRect().top, title: document.querySelector('[data-ticket-title]')!.getBoundingClientRect().bottom }))
  expect(before.strip).toBeGreaterThanOrEqual(before.title)
  const strip = page.locator('[data-ticket-strip]')
  await noSideways(page, { allow: strip })
  await toEnd(page)
  await stripAtTop(page, false)

  await page.goto(`${path}?view=review`)
  await expect(page.locator('[data-review-view]')).toBeVisible()
  await expect(strip).toHaveCount(0)
})
