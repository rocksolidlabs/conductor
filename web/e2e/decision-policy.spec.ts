import type { Page, Route } from '@playwright/test'
import { atRest, dayPage, expect, test, useTheme } from './fixtures'
import { decisionRecord, inboxItem, stubInbox, type StubItem } from './inbox-stub'
import { inside, uncut } from './layout'

// A decision router's policy said on the decision's card (SW1 PR-8, docs/plans/decision-policy.md, "web/e2e"), on the shipped build:
// the server's replies for one record under a policy are stubbed in the browser (Owner decisions, 6: the server's half is held by the
// Go tests on the real handlers with exttest's FakeRouter). The record's read answers its policy and policyNow; its respond route is
// the spec's own, which records what the page sent. Who may answer, by group; the operator's answer held back while the policy is in
// force, with whose it is; Override…, which needs a reason of at least ten characters and sends the answer as an override; a gap in
// its own words; and "not in force now", when the operator answers as in the free build. Both themes, a phone at 390 x 844, layout by
// measures.

const DESK = { width: 1440, height: 900 }
const PHONE = { width: 390, height: 844, touch: true }

const group = (eligible: unknown[] | null, gap = '') => ({ role: 'architecture', label: 'architecture owner', quorum: 1, rule: 'r-1', ...(eligible ? { eligible } : {}), ...(gap ? { gap } : {}) })
const ana = { subject: 'ana', name: 'Ana', via: 'role', ref: 'ref-ana' }
const ben = { subject: 'ben', name: 'Ben', via: 'delegate', for: 'ana', ref: 'ref-ben' }
const policy = (groups: unknown[], override = 'operator') => ({
  revision: 'r1', approver: 'router', quorum: 1, authority: [{ kind: 'operator' }], router: 'switchman', scope: 's1',
  pinned: { rule: 'r-1', groups: [{ role: 'architecture', quorum: 1 }], override, notify: 'now' }, groups, override,
  routedAt: '2026-11-19T09:00:00Z', spare: true, fallback: 'local/1',
})

interface Sent {
  body: Record<string, unknown>
}

/**
 * The decision page at `size`, its one item the record `it`, whose read answers `policyNow` beside it; its respond route answers by
 * `answer` and records what was sent.
 */
async function open(browser: Parameters<typeof dayPage>[0], size: typeof DESK | typeof PHONE, it: StubItem, policyNow: unknown, answer: (body: Record<string, unknown>) => { status: number; body: unknown }, extra: { router?: boolean; reads?: { now: unknown } } = {}): Promise<{ page: Page; sent: Sent[] }> {
  const page = await dayPage(browser, { ...size, path: '/yard' })
  await stubInbox(page, [it])
  const id = it.ref.slice('decision:'.length)
  const sent: Sent[] = []
  // Over the stub's own read of the record (a later route answers first): the record with its policyNow.
  await page.route(
    (u) => u.pathname === `/api/decisions/${id}` || u.pathname === `/api/decisions/${id}/respond`,
    async (route: Route) => {
      const req = route.request()
      const fulfil = (status: number, body: unknown) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })
      if (req.method() === 'GET') {
        const now = extra.reads ? extra.reads.now : policyNow
        return fulfil(200, { decision: decisionRecord(it, true), refs: [], ...(now ? { policyNow: now } : {}), ...(extra.router ? { router: true } : {}) })
      }
      const body = JSON.parse(req.postData() ?? '{}') as Record<string, unknown>
      sent.push({ body })
      const a = answer(body)
      return fulfil(a.status, a.body)
    },
  )
  await page.goto(`/decisions?item=${encodeURIComponent(it.ref)}`)
  await expect(page.locator('[data-decision-card]')).toBeVisible()
  return { page, sent }
}

/** A decision item whose record carries `p` as its policy. */
const routedItem = (p: unknown) => inboxItem('decision', { decision: { policy: p } })

const accepted = (it: StubItem, body: Record<string, unknown>) => {
  const id = it.ref.slice('decision:'.length)
  const receipt = { decision: id, k: 1, status: 'accepted', words: `Recorded: ${id} answered by Nate, overriding its router's policy`, actor: 'admin', byName: 'Nate', at: new Date().toISOString(), rev: 3, override: body.override === true, rationale: body.rationale, choice: body.choice }
  return { status: 201, body: { receipt, decision: { ...decisionRecord(it, true), state: 'answered', rev: 3, responses: [receipt] } } }
}

test("under a policy in force the operator's answer waits for Ana, and Override… asks a reason before it sends the answer as an override", async ({ day, browser }) => {
  void day
  const it = routedItem(policy([group([ana, ben])]))
  const { page, sent } = await open(browser, DESK, it, { inForce: true }, (b) => accepted(it, b))

  const who = page.locator('[data-decision-policy]')
  await expect(who.locator('[data-policy-group="architecture"]')).toContainText('For the architecture owner: Ana, Ben (delegate for Ana)')
  await expect(who.locator('[data-policy-group="architecture"]')).toContainText('rule r-1')
  await expect(page.locator('[data-decision-policy-not-in-force]')).toHaveCount(0)

  // The answer buttons are held back with whose it is.
  await expect(page.locator('[data-decision-held-back-words]')).toHaveText("This is Ana or Ben's to answer.")
  for (const b of await page.locator('[data-decision-choice]').all()) await expect(b).toBeDisabled()

  // Override…: the send waits for a choice and a reason of at least ten characters.
  await page.locator('[data-decision-override]').click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible()
  const send = dialog.locator('[data-decision-override-send]')
  await expect(send).toBeDisabled()
  await dialog.locator('[data-decision-override-choice="0"]').click()
  await expect(send).toBeDisabled()
  await dialog.locator('[data-decision-override-reason]').fill('too short')
  await expect(dialog.locator('[data-decision-override-short]')).toBeVisible()
  await expect(send).toBeDisabled()
  await dialog.locator('[data-decision-override-reason]').fill('Ana is away this week')
  await expect(dialog.locator('[data-decision-override-reason]')).toHaveValue('Ana is away this week')
  await expect(send).toBeEnabled()
  await send.click()
  await expect.poll(() => sent.length).toBe(1)
  expect(sent[0]!.body).toMatchObject({ override: true, choice: 0, rationale: 'Ana is away this week' })
  await expect(page.locator('[data-decision-receipt]')).toContainText('overriding its router')

  // The same in the other theme, the policy's words read again after the redraw.
  await useTheme(page, 'light', '#dashboard-sidebar-main')
  await expect(page.locator('[data-decision-policy] [data-policy-group="architecture"]')).toContainText('For the architecture owner: Ana, Ben (delegate for Ana)')
  await page.context().close()
})

test('a policy whose override is none offers no Override…, and a group nobody fills says so in its own words', async ({ day, browser }) => {
  void day
  const it = routedItem(policy([group(null, 'Waiting for an architecture owner: none is assigned in CND')], 'none'))
  const { page } = await open(browser, DESK, it, { inForce: true }, () => ({ status: 500, body: {} }))
  await expect(page.locator('[data-decision-policy] [data-policy-gap]')).toHaveText(/Waiting for an architecture owner: none is assigned in CND/)
  await expect(page.locator('[data-decision-held-back-words]')).toHaveText('This is for the architecture owner: nobody is assigned yet.')
  await expect(page.locator('[data-decision-override]')).toHaveCount(0)
  await page.context().close()
})

test("not in force now: the card says the operator answers, with the router's why, and the operator's buttons answer", async ({ day, browser }) => {
  void day
  const it = routedItem(policy([group([ana])]))
  const { page } = await open(browser, DESK, it, { inForce: false, why: 'the roster is moving' }, () => ({ status: 500, body: {} }))
  await expect(page.locator('[data-decision-policy-not-in-force]')).toHaveText('Routed to Ana: not in force now (the roster is moving). The operator answers.')
  await expect(page.locator('[data-decision-held-back]')).toHaveCount(0)
  await expect(page.locator('[data-decision-choice="0"]')).toBeEnabled()
  await page.context().close()
})

test('on a phone, in both themes, the held answer, Override… and its dialog fit the screen', async ({ day, browser }) => {
  void day
  const it = routedItem(policy([group([ana])]))
  const { page } = await open(browser, PHONE, it, { inForce: true }, () => ({ status: 500, body: {} }))
  // The dialog scales in as it opens, and a measure taken while it does reads its fields out of a box still scaled: the page's
  // animations run at a tenth of their pace, so that a measure not taken at rest fails every run, not only on a slow one.
  const cdp = await page.context().newCDPSession(page)
  await cdp.send('Animation.enable')
  await cdp.send('Animation.setPlaybackRate', { playbackRate: 0.1 })
  for (const theme of ['dark', 'light'] as const) {
    if (theme === 'light') await useTheme(page, 'light', '[data-bottom-bar]')
    const held = page.locator('[data-decision-held-back]')
    await held.scrollIntoViewIfNeeded()
    await uncut(page.locator('[data-decision-held-back-words]'))
    await inside(page.locator('[data-decision-override]'), held)
    await page.locator('[data-decision-override]').click()
    const dialog = page.getByRole('dialog')
    await expect(dialog).toBeVisible()
    await atRest(dialog.locator('[data-decision-override-send]'))
    await uncut(dialog.locator('[data-decision-override-reason]'))
    await uncut(dialog.locator('[data-decision-override-send]'))
    await dialog.locator('[data-decision-override-cancel]').click()
    await expect(page.getByRole('dialog')).toHaveCount(0)
  }
  await page.context().close()
})

test("the operator's card follows the router: a policy that lapses while the page is open lets the operator answer, without a reload", async ({ day, browser }) => {
  void day
  const it = routedItem(policy([group([ana])], 'none'))
  const reads = { now: { inForce: true } as unknown }
  const { page } = await open(browser, DESK, it, null, () => ({ status: 500, body: {} }), { reads })
  await expect(page.locator('[data-decision-held-back-words]')).toHaveText("This is Ana's to answer.")
  await expect(page.locator('[data-decision-choice="0"]')).toBeDisabled()
  // The router stops confirming: no event comes, and the card's next read (here the window's focus) learns it.
  reads.now = { inForce: false, why: 'the roster is moving' }
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(page.locator('[data-decision-policy-not-in-force]')).toHaveText('Routed to Ana: not in force now (the roster is moving). The operator answers.')
  await expect(page.locator('[data-decision-held-back]')).toHaveCount(0)
  await expect(page.locator('[data-decision-choice="0"]')).toBeEnabled()
  await page.context().close()
})

test('an identity the policy names, signed in as the operator, answers as eligible: nothing held back', async ({ day, browser }) => {
  void day
  const it = routedItem(policy([group([ana])], 'none'))
  const { page } = await open(browser, DESK, it, { inForce: true, eligible: true }, () => ({ status: 500, body: {} }))
  await expect(page.locator('[data-decision-held-back]')).toHaveCount(0)
  await expect(page.locator('[data-decision-choice="0"]')).toBeEnabled()
  await page.context().close()
})

test('Ask the router again is offered whenever a router is registered, on a record under the server\'s own policy too, and never without one', async ({ day, browser }) => {
  void day
  const it = inboxItem('decision')
  const withRouter = await open(browser, DESK, it, null, () => ({ status: 500, body: {} }), { router: true })
  await expect(withRouter.page.locator('[data-decision-ask-router]')).toBeVisible()
  await withRouter.page.context().close()
  const without = await open(browser, DESK, routedItem(policy([group([ana])])), { inForce: false }, () => ({ status: 500, body: {} }))
  await expect(without.page.locator('[data-decision-comment]')).toBeVisible()
  await expect(without.page.locator('[data-decision-ask-router]')).toHaveCount(0)
  await without.page.context().close()
})

test("on the decide page, an answer the router's policy refuses says the person is not among those who may answer", async ({ browser }) => {
  const ctx = await browser.newContext()
  const page = await ctx.newPage()
  const view = { id: 'dr-7', rev: 1, state: 'open', type: 'approval', question: 'May dr-7 go out?', options: [{ label: 'Approve' }, { label: 'Reject' }], createdAt: '2026-11-12T00:00:00Z', own: [] }
  await page.route(
    (u) => u.pathname.startsWith('/api/'),
    async (route) => {
      const path = new URL(route.request().url()).pathname
      const fulfil = (status: number, body: unknown) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })
      if (path === '/api/me') return fulfil(200, { subject: 'ana', name: 'Ana', grants: ['decide'] })
      if (path === '/api/me/decisions') return fulfil(200, { decisions: [{ id: 'dr-7', rev: 1 }] })
      if (path === '/api/decisions/dr-7') return fulfil(200, view)
      if (path === '/api/decisions/dr-7/respond') return fulfil(403, { error: { code: 'not_eligible', message: 'You are not among those who may answer dr-7.' } })
      return route.abort()
    },
  )
  await page.goto('/decide/dr-7#pass=e2e.pass-of_ana.7')
  const card = page.locator('[data-decide-card][data-decision-id="dr-7"]')
  await card.getByRole('radio', { name: 'Approve' }).check()
  await card.locator('[data-decide-send]').click()
  await expect(card.getByText('You are not among those who may answer dr-7.')).toBeVisible()
  await ctx.close()
})
