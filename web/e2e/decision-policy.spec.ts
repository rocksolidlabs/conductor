import type { Page, Route } from '@playwright/test'
import { dayPage, expect, test, useTheme } from './fixtures'
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
async function open(browser: Parameters<typeof dayPage>[0], size: typeof DESK | typeof PHONE, it: StubItem, policyNow: unknown, answer: (body: Record<string, unknown>) => { status: number; body: unknown }): Promise<{ page: Page; sent: Sent[] }> {
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
      if (req.method() === 'GET') return fulfil(200, { decision: decisionRecord(it, true), refs: [], ...(policyNow ? { policyNow } : {}) })
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
  for (const theme of ['dark', 'light'] as const) {
    if (theme === 'light') await useTheme(page, 'light', '[data-bottom-bar]')
    const held = page.locator('[data-decision-held-back]')
    await held.scrollIntoViewIfNeeded()
    await uncut(page.locator('[data-decision-held-back-words]'))
    await inside(page.locator('[data-decision-override]'), held)
    await page.locator('[data-decision-override]').click()
    const dialog = page.getByRole('dialog')
    await expect(dialog).toBeVisible()
    await uncut(dialog.locator('[data-decision-override-reason]'))
    await uncut(dialog.locator('[data-decision-override-send]'))
    await dialog.locator('[data-decision-override-cancel]').click()
    await expect(page.getByRole('dialog')).toHaveCount(0)
  }
  await page.context().close()
})
