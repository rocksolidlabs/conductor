import type { Page, Route } from '@playwright/test'
import { atRest, dayPage, expect, test, useTheme } from './fixtures'
import { decisionRecord, inboxItem, stubInbox, type StubItem } from './inbox-stub'
import { inside, noSideways, uncut } from './layout'

// A decision router's policy said on the decision's card (SW1 PR-8, docs/plans/decision-policy.md, "web/e2e"), on the shipped build:
// the server's replies for one record under a policy are stubbed in the browser (Owner decisions, 6: the server's half is held by the
// Go tests on the real handlers with exttest's FakeRouter). The record's read answers its policy and policyNow; its respond route is
// the spec's own, which records what the page sent. Who may answer, by group; the operator's answer held back while the policy is in
// force, with whose it is; Override…, which needs a reason of at least ten characters and sends the answer as an override; a gap in
// its own words; and "not in force now", when the operator answers as in the free build. Both themes, a phone at 390 x 844, layout by
// measures. Several approvals (SW1 PR-9): the card's count ("1 of 2 approvals", each group's, distinct persons said once), a second
// person's approval answering it, the operator's own approval counted and "counted already", one eligible Reject and an override
// answering it whole, the decide page's counted approval and its refusal, and four groups on a phone.

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

/** What the record's read answers now beside the item's record (SW1 PR-9): the record's own fields over it, and the read's `quorum`; changed by the test between reads. */
interface Live {
  decision?: Record<string, unknown>
  quorum?: unknown
}

/**
 * The decision page at `size`, its one item the record `it`, whose read answers `policyNow` beside it; its respond route answers by
 * `answer` and records what was sent.
 */
async function open(browser: Parameters<typeof dayPage>[0], size: typeof DESK | typeof PHONE, it: StubItem, policyNow: unknown, answer: (body: Record<string, unknown>) => { status: number; body: unknown }, extra: { router?: boolean; reads?: { now: unknown }; live?: Live } = {}): Promise<{ page: Page; sent: Sent[] }> {
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
        const live = extra.live ?? {}
        return fulfil(200, { decision: { ...decisionRecord(it, true), ...live.decision }, refs: [], ...(now ? { policyNow: now } : {}), ...(live.quorum ? { quorum: live.quorum } : {}), ...(extra.router ? { router: true } : {}) })
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

// Several approvals (SW1 PR-9, docs/plans/decision-policy.md, "Groups, quorum and distinct persons"): the record's read answers its
// `quorum` (the places, those filled, each group's count), the responses its record keeps, and the decide view its `own`.

const cam = { subject: 'cam', name: 'Cam', via: 'role', ref: 'ref-cam' }
const groupOf = (role: string, label: string, quorum: number, eligible: unknown[]) => ({ role, label, quorum, rule: `r-${role}`, eligible })
const policyOf = (groups: Array<{ role: string; quorum: number }>, o: { distinct?: boolean; override?: string } = {}) => ({
  ...policy(groups, o.override ?? 'operator'),
  revision: 'r2',
  pinned: { rule: 'r-1', groups: groups.map((g) => ({ role: g.role, quorum: g.quorum })), ...(o.distinct ? { distinct: true } : {}), override: o.override ?? 'operator', notify: 'now' },
  ...(o.distinct ? { distinct: true } : {}),
})
const twoGroups = () => policyOf([groupOf('architecture', 'architecture owner', 1, [ana]), groupOf('security', 'security owner', 1, [cam])], { distinct: true })
/** The read's quorum from each group's [role, quorum, counted]. */
const countOf = (gs: Array<[string, number, number]>) => {
  const groups = gs.map(([role, quorum, counted]) => ({ role, quorum, counted, met: counted >= quorum }))
  return { places: groups.reduce((n, g) => n + g.quorum, 0), filled: groups.reduce((n, g) => n + Math.min(g.counted, g.quorum), 0), groups }
}
const idOf = (it: StubItem) => it.ref.slice('decision:'.length)
const response = (it: StubItem, k: number, who: { subject: string; name: string }, status: string, choice: number, extra: Record<string, unknown> = {}) => ({
  decision: idOf(it), k, status, words: `${status === 'counted' ? 'Counted' : 'Recorded'}: ${idOf(it)} by ${who.name}`, actor: `user:${who.subject}`, byName: who.name, choice, at: '2026-11-19T09:05:00Z', rev: k + 1, ...extra,
})

test('a record of one group of quorum 1 shows no count: a simple policy reads as it did', async ({ day, browser }) => {
  void day
  const it = routedItem(policy([group([ana])]))
  const { page } = await open(browser, DESK, it, { inForce: true }, () => ({ status: 500, body: {} }), { live: { quorum: countOf([['architecture', 1, 0]]) } })
  await expect(page.locator('[data-decision-policy] [data-policy-group="architecture"]')).toContainText('For the architecture owner: Ana')
  await expect(page.locator('[data-decision-quorum]')).toHaveCount(0)
  await expect(page.locator('[data-decision-policy-distinct]')).toHaveCount(0)
  await expect(page.locator('[data-decision-card]')).not.toContainText(/1 of 1/)
  await page.context().close()
})

test("the card says 1 of 2 with each group's count, and a second person's approval answers it", async ({ day, browser }) => {
  void day
  const it = routedItem(twoGroups())
  const anaCounted = response(it, 1, ana, 'counted', 0, { groups: ['architecture'], via: 'role', ref: 'ref-ana' })
  const live: Live = { quorum: countOf([['architecture', 1, 1], ['security', 1, 0]]), decision: { rev: 2, responses: [anaCounted] } }
  const { page } = await open(browser, DESK, it, { inForce: true }, () => ({ status: 500, body: {} }), { live })

  const count = page.locator('[data-decision-quorum]')
  await expect(count.locator('[data-decision-quorum-lead]')).toHaveText('1 of 2 approvals')
  await expect(count.locator('[data-quorum-group="architecture"]')).toHaveText('1 of 1 for the architecture owner: met')
  await expect(count.locator('[data-quorum-group="architecture"]')).toHaveAttribute('data-quorum-met', '')
  await expect(count.locator('[data-quorum-group="security"]')).toHaveText('0 of 1 for the security owner')
  await expect(count.locator('[data-quorum-group="security"]')).not.toHaveAttribute('data-quorum-met')
  await expect(page.locator('[data-decision-policy-distinct]')).toHaveText('One person may fill only one group.')
  await expect(page.locator('[data-decision-held-back-words]')).toHaveText("This is Ana or Cam's to answer.")
  await expect(page.locator('[data-decision-card]')).toHaveAttribute('data-decision-state', 'open')

  // Cam approves for security: the record is answered, every group met; the card learns it at its next read (here the window's focus).
  const camAccepted = response(it, 2, cam, 'accepted', 0, { groups: ['security'], via: 'role', ref: 'ref-cam' })
  live.decision = { state: 'answered', rev: 3, answeredAt: '2026-11-19T09:10:00Z', responses: [anaCounted, camAccepted] }
  live.quorum = countOf([['architecture', 1, 1], ['security', 1, 1]])
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(page.locator('[data-decision-card]')).toHaveAttribute('data-decision-state', 'answered')
  await expect(count.locator('[data-decision-quorum-lead]')).toHaveText('2 of 2 approvals')
  await expect(count.locator('[data-quorum-met]')).toHaveCount(2)
  await expect(page.locator('[data-decision-answer-by]')).toHaveText('Cam')
  // Answered by its approvals: no Reject or override line.
  await expect(page.locator('[data-decision-answered-how]')).toHaveCount(0)

  // The same after the other theme's redraw.
  await useTheme(page, 'light', '#dashboard-sidebar-main')
  await expect(page.locator('[data-decision-quorum] [data-decision-quorum-lead]')).toHaveText('2 of 2 approvals')
  await expect(page.locator('[data-decision-quorum] [data-quorum-group="security"]')).toHaveText('1 of 1 for the security owner: met')
  await page.context().close()
})

test("an operator the policy names approves: counted, with what it still waits for, and the answer is not offered again", async ({ day, browser }) => {
  void day
  const it = routedItem(twoGroups())
  const live: Live = { quorum: countOf([['architecture', 1, 0], ['security', 1, 0]]) }
  const { page, sent } = await open(browser, DESK, it, { inForce: true, eligible: true }, (b) => {
    const rc = response(it, 1, ana, 'counted', b.choice as number, { groups: ['architecture'], via: 'role', ref: 'ref-ana' })
    live.decision = { rev: 2, responses: [rc] }
    live.quorum = countOf([['architecture', 1, 1], ['security', 1, 0]])
    return { status: 201, body: { receipt: rc, decision: { ...decisionRecord(it, true), rev: 2, responses: [rc] } } }
  }, { live })
  await expect(page.locator('[data-decision-quorum-lead]')).toHaveText('0 of 2 approvals')
  await page.locator('[data-decision-choice="0"]').click()
  await page.locator('[data-decision-confirm]').click()
  await expect.poll(() => sent.length).toBe(1)
  // The count the read brings after it, and what is still needed.
  await expect(page.locator('[data-decision-counted]')).toHaveText('Your approval is counted: 1 of 2 approvals. It waits for the security owner.')
  await expect(page.locator('[data-decision-quorum-lead]')).toHaveText('1 of 2 approvals')
  await expect(page.locator('[data-decision-receipt]')).toHaveAttribute('data-receipt-status', 'counted')
  for (const b of await page.locator('[data-decision-choice]').all()) await expect(b).toBeDisabled()
  await expect(page.locator('[data-decision-confirm-step]')).toHaveCount(0)
  await expect(page.locator('[data-decision-rationale]')).toHaveCount(0)
  expect(sent).toHaveLength(1)
  await page.context().close()
})

test("an approval the server says is counted already holds the answer, the choice as picked kept, in its own words", async ({ day, browser }) => {
  void day
  const it = routedItem(twoGroups())
  const { page, sent } = await open(browser, DESK, it, { inForce: true, eligible: true }, () => ({ status: 409, body: { error: { code: 'already_counted', message: `Your answer to ${idOf(it)} is counted already.` } } }), { live: { quorum: countOf([['architecture', 1, 1], ['security', 1, 0]]) } })
  await page.locator('[data-decision-choice="1"]').click()
  await page.locator('[data-decision-rationale]').fill('the rollback plan is thin')
  await page.locator('[data-decision-confirm]').click()
  await expect.poll(() => sent.length).toBe(1)
  await expect(page.locator('[data-decision-refusal-words]')).toHaveText(`Your approval of ${idOf(it)} is counted already: nothing more is asked of you. Your answer to ${idOf(it)} is counted already.`)
  await expect(page.locator('[data-decision-counted]')).toHaveText('Your approval is counted: 1 of 2 approvals. It waits for the security owner.')
  // Nothing offered again; the choice picked stays drawn as picked.
  await expect(page.locator('[data-decision-choice="1"]')).toBeDisabled()
  await expect(page.locator('[data-decision-choice="1"]')).toHaveAttribute('data-decision-choice-label', 'Reject')
  await expect(page.locator('[data-decision-confirm-step]')).toHaveCount(0)
  // Read again lets the person look again: the rationale typed is still there.
  await page.locator('[data-decision-read-again]').click()
  await expect(page.locator('[data-decision-rationale]')).toHaveValue('the rollback plan is thin')
  await page.context().close()
})

test('one eligible Reject answers it at once, and an override answers every group: the card says which', async ({ day, browser }) => {
  void day
  const rejected = routedItem(twoGroups())
  const anaCounted = response(rejected, 1, ana, 'counted', 0, { groups: ['architecture'] })
  const camReject = response(rejected, 2, cam, 'accepted', 1, { groups: ['security'] })
  const a = await open(browser, DESK, rejected, { inForce: true }, () => ({ status: 500, body: {} }), {
    live: { decision: { state: 'answered', rev: 3, answeredAt: '2026-11-19T09:10:00Z', responses: [anaCounted, camReject] }, quorum: countOf([['architecture', 1, 1], ['security', 1, 0]]) },
  })
  await expect(a.page.locator('[data-decision-answered-how]')).toHaveText('Reject by Cam: one eligible Reject answers it at once. The approval counted before it stays in the record.')
  await expect(a.page.locator('[data-decision-answer-choice]')).toHaveText('Reject')
  await a.page.context().close()

  const overridden = routedItem(twoGroups())
  const counted = response(overridden, 1, ana, 'counted', 0, { groups: ['architecture'] })
  const override = { ...response(overridden, 2, { subject: 'admin', name: 'Nate' }, 'accepted', 0), actor: 'admin', override: true, rationale: 'Cam is away this week' }
  const b = await open(browser, DESK, overridden, { inForce: true }, () => ({ status: 500, body: {} }), {
    live: { decision: { state: 'answered', rev: 3, answeredAt: '2026-11-19T09:10:00Z', responses: [counted, override] }, quorum: countOf([['architecture', 1, 1], ['security', 1, 0]]) },
  })
  await expect(b.page.locator('[data-decision-policy-override]')).toHaveText('Overridden by Nate: Cam is away this week')
  await expect(b.page.locator('[data-decision-answered-how]')).toHaveText('The override answered every group at once. The approval counted before it stays in the record.')
  await b.page.context().close()
})

test('on a phone, in both themes, four groups of long names wrap in the card and nothing scrolls sideways', async ({ day, browser }) => {
  void day
  const long = 'platform-architecture-and-infrastructure-governance owner'
  const unbroken = 'securityandprivacyreviewboardrepresentativeforcustomerdata'
  const four = policyOf(
    [
      groupOf('architecture', long, 4, [ana]),
      groupOf('security', unbroken, 4, [cam]),
      groupOf('release', 'release manager', 4, [ben]),
      groupOf('privacy', 'privacy reviewer of the customer data the change touches', 4, [{ subject: 'dee', name: 'Dee', via: 'assigned', ref: 'ref-dee' }]),
    ],
    { distinct: true },
  )
  const it = routedItem(four)
  const { page } = await open(browser, PHONE, it, { inForce: true }, () => ({ status: 500, body: {} }), { live: { quorum: countOf([['architecture', 4, 4], ['security', 4, 1], ['release', 4, 3], ['privacy', 4, 0]]) } })
  for (const theme of ['dark', 'light'] as const) {
    if (theme === 'light') await useTheme(page, 'light', '[data-bottom-bar]')
    const count = page.locator('[data-decision-quorum]')
    await expect(count.locator('[data-decision-quorum-lead]')).toHaveText('8 of 16 approvals')
    await expect(count.locator('[data-quorum-group="security"]')).toHaveText(`1 of 4 for the ${unbroken}`)
    await count.scrollIntoViewIfNeeded()
    await atRest(count)
    for (const words of await count.locator('[data-quorum-words]').all()) {
      await uncut(words)
      await inside(words, page.locator('[data-decision-policy]'))
    }
    for (const line of await page.locator('[data-decision-policy] [data-policy-group]').all()) await inside(line, page.locator('[data-decision-policy]'))
    await noSideways(page)
  }
  await page.context().close()
})

test("on the decide page, an approval that counts is said as counted and offered nothing; counted already says so, and a second person's approval is recorded", async ({ browser }) => {
  const ctx = await browser.newContext()
  const page = await ctx.newPage()
  const base = { state: 'open', type: 'approval', question: 'May dr-8 go out?', options: [{ label: 'Approve' }, { label: 'Reject' }], createdAt: '2026-11-12T00:00:00Z' }
  const counted = { k: 1, responseId: 'r-ana', status: 'counted', rev: 2, at: '2026-11-12T00:01:00Z', choice: 0 }
  // dr-8: Ana's approval counts already. dr-9: her view is older than her approval, so her send is refused as counted already, and the
  // read made then shows it counted. dr-10: Cam's approval completes it.
  const views: Record<string, Record<string, unknown>> = {
    'dr-8': { id: 'dr-8', rev: 2, ...base, own: [counted], outcome: 'waiting' },
    'dr-9': { id: 'dr-9', rev: 1, ...base, question: 'May dr-9 go out?', own: [] },
    'dr-10': { id: 'dr-10', rev: 2, ...base, question: 'May dr-10 go out?', own: [] },
  }
  const sends: string[] = []
  await page.route(
    (u) => u.pathname.startsWith('/api/'),
    async (route) => {
      const path = new URL(route.request().url()).pathname
      const fulfil = (status: number, body: unknown) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })
      if (path === '/api/me') return fulfil(200, { subject: 'ana', name: 'Ana', grants: ['decide'] })
      if (path === '/api/me/decisions') return fulfil(200, { decisions: Object.values(views).map((v) => ({ id: v.id, rev: v.rev })) })
      const m = /^\/api\/decisions\/(dr-\d+)(\/respond)?$/.exec(path)
      if (m && !m[2] && views[m[1]!]) return fulfil(200, views[m[1]!])
      if (m && m[2] === '/respond') {
        sends.push(m[1]!)
        const body = JSON.parse(route.request().postData() ?? '{}') as { responseId: string; choice: number }
        if (m[1] === 'dr-9') {
          views['dr-9'] = { ...views['dr-9']!, rev: 2, own: [{ ...counted, responseId: 'r-earlier' }], outcome: 'waiting' }
          return fulfil(409, { error: { code: 'already_counted', message: 'Your answer to dr-9 is counted already.' } })
        }
        if (m[1] === 'dr-10') {
          const rc = { k: 1, responseId: body.responseId, status: 'accepted', rev: 3, at: '2026-11-12T00:02:00Z', choice: body.choice }
          views['dr-10'] = { id: 'dr-10', rev: 3, own: [rc], outcome: 'waiting' }
          return fulfil(201, { receipt: rc, decision: views['dr-10'] })
        }
      }
      return route.abort()
    },
  )
  await page.goto('/decide/dr-8#pass=e2e.pass-of_ana.7')

  const dr8 = page.locator('[data-decide-card][data-decision-id="dr-8"]')
  await expect(dr8.locator('[data-decide-standing]')).toHaveText(/Your approval of dr-8 is counted\. It waits for the other approvals it needs: nothing more is asked of you\./)
  await expect(dr8.locator('[data-decide-form]')).toHaveCount(0)
  await expect(dr8.locator('[data-decide-outcome]')).toHaveCount(0)

  const dr9 = page.locator('[data-decide-card][data-decision-id="dr-9"]')
  await dr9.getByRole('radio', { name: 'Approve' }).check()
  await dr9.locator('[data-decide-send]').click()
  await expect(dr9.getByText('Your answer to dr-9 is counted already.')).toBeVisible()
  await expect(dr9.locator('[data-decide-form]')).toHaveCount(0)
  await expect(dr9.locator('[data-decide-standing]')).toHaveText(/Your approval of dr-9 is counted\./)

  const dr10 = page.locator('[data-decide-card][data-decision-id="dr-10"]')
  await dr10.getByRole('radio', { name: 'Approve' }).check()
  await dr10.locator('[data-decide-send]').click()
  await expect(dr10.locator('[data-decide-outcome]')).toHaveText(/Your answer to dr-10 was recorded\./)
  await expect(dr10.locator('[data-decide-standing]')).toHaveCount(0)
  expect(sends).toEqual(['dr-9', 'dr-10'])
  await ctx.close()
})
