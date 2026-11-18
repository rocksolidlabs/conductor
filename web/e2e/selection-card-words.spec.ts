import type { Browser, Page } from '@playwright/test'
import { dayPage, expect, test, useTheme } from './fixtures'
import { inboxItem, stubInbox, type StubItem } from './inbox-stub'
import { noSideways } from './layout'
import { runInfo, serveRun, type StubRun } from './run-fixtures'

// Needs you's count of choices, and a comparison's selection's card in each of its states (docs/plans/compare-crew.md; the card is
// DecisionCard and its answer DecisionRespond): the count is of the answers a person can give, Comment and Ask for more aside, for
// every kind of decision the page lists; the selection's card is to be answered while open, answered once answered and once applied,
// and offers no Ask for more in any state, no agent having asked it, while an agent's record keeps it. The items are the inbox
// stub's (inbox-stub.ts) on the day's server, wide and at 390 x 844, in both themes; every wait is on what the page draws.

const DESK = { width: 1440, height: 900 }
const PHONE = { width: 390, height: 844, touch: true }

const run: StubRun = runInfo({ tasks: 6 })
const accepted = (id: string) => ({ decision: id, k: 1, status: 'accepted', words: 'Recorded: approach-a at 0000001', actor: 'operator', byName: 'Nate', choice: 0, rationale: 'the smaller change', at: '2026-11-18T00:01:00Z', rev: 2 })

/** A selection open; one answered and waiting to be applied; one applied (read by its ref, as the ticket's Decisions opens it). */
function selections(): { open: StubItem; answered: StubItem; applied: StubItem } {
  const open = inboxItem('decision', { origin: 'compare', run })
  const a = inboxItem('decision', { origin: 'compare', run })
  const answered: StubItem = { ...a, state: 'answered', decision: { state: 'answered', rev: 2, answeredAt: '2026-11-18T00:01:00Z', responses: [accepted(a.ref.slice(9))] } }
  const p = inboxItem('decision', { origin: 'compare', run })
  const applied: StubItem = {
    ...p,
    state: 'answered',
    item: { state: 'applied', now: `${p.ref.slice(9)} is applied` },
    decision: { state: 'applied', rev: 3, answeredAt: '2026-11-18T00:01:00Z', closedAt: '2026-11-18T00:02:00Z', applied: { words: 'continued approach-a' }, responses: [accepted(p.ref.slice(9))] },
  }
  return { open, answered, applied }
}

async function needsYou(browser: Browser, size: typeof DESK | typeof PHONE, items: StubItem[], open?: StubItem): Promise<Page> {
  const page = await dayPage(browser, { ...size, path: '/yard' })
  await serveRun(page, run)
  await stubInbox(page, items)
  await page.goto(open ? `/decisions?item=${encodeURIComponent(open.ref)}` : '/decisions')
  return page
}

test.describe('Needs you and a selection’s card', () => {
  test('counts each row’s answers, never Comment or Ask for more, for a selection, an agent’s approval, a plan’s gate, a clarification and a question', async ({ day, browser }) => {
    void day
    const sel = selections().open
    const agent = inboxItem('decision', { run })
    const plan = inboxItem('decision', { origin: 'plan', run })
    const clar = inboxItem('decision', { type: 'clarification', run })
    const question = inboxItem('question')
    const items = [sel, agent, plan, clar, question]
    const page = await needsYou(browser, DESK, items)
    // What each row offers, as the stub's whole item lists it: the options it answers by, and the line a clarification takes.
    const want: Array<[StubItem, number]> = [
      [sel, sel.choices.length],
      [agent, 2],
      [plan, 2],
      [clar, clar.choices.length + 1],
      [question, question.choices.length + (question.line === false ? 0 : 1)],
    ]
    for (const [it, n] of want) {
      const row = page.locator(`[data-inbox-row="${it.ref}"]`)
      await expect(row.locator('[data-inbox-row-choices]'), it.ref).toHaveText(`${n} ${n === 1 ? 'choice' : 'choices'}`)
    }
    // The selection's three: two candidates and None of these.
    await expect(page.locator(`[data-inbox-row="${sel.ref}"] [data-inbox-row-choices]`)).toHaveText('3 choices')
    await page.context().close()
  })

  for (const size of [DESK, PHONE]) {
    test(`a selection’s card says what is true of it, open, answered and applied, at ${size.width} x ${size.height}, in both themes`, async ({ day, browser }) => {
      void day
      const { open, answered, applied } = selections()
      const agent = inboxItem('decision', { run })
      const items = [open, answered, applied, agent]
      const page = await needsYou(browser, size, items, open)
      for (const theme of ['dark', 'light'] as const) {
        for (const [it, lead] of [
          [open, 'to be answered by'],
          [answered, 'answered by'],
          [applied, 'answered by'],
        ] as const) {
          await page.goto(`/decisions?item=${encodeURIComponent(it.ref)}`)
          await useTheme(page, theme)
          const item = page.locator(`[data-inbox-item="${it.ref}"]`)
          const card = item.locator('[data-decision-card]')
          await expect(card.locator('[data-decision-question]'), `${it.ref} drawn`).toBeVisible()
          await expect(card).toHaveAttribute('data-decision-state', it === open ? 'open' : it === answered ? 'answered' : 'applied')
          await expect(card.locator('[data-decision-approver-lead]')).toHaveText(lead)
          await expect(card.locator('[data-decision-approver]')).toHaveText('the operator')
          // No Ask for more, nor its words about an agent, in any state.
          await expect(item.locator('[data-decision-more]')).toHaveCount(0)
          await expect(item.locator('[data-decision-more-why]')).toHaveCount(0)
          await expect(item).not.toContainText('no agent asked it')
          const box = item.locator('[data-decision-comment-text] textarea')
          if (await box.count()) await expect(box).toHaveAttribute('placeholder', 'A comment, kept with the decision')
          // The words in view, nothing of the card scrolling or cut sideways.
          await card.locator('[data-decision-approver-lead]').scrollIntoViewIfNeeded()
          await expect(card.locator('[data-decision-approver-lead]')).toBeInViewport({ ratio: 1 })
          await noSideways(page)
        }
      }
      // An agent's approval, its attempt live, keeps Ask for more, to be answered while open.
      await page.goto(`/decisions?item=${encodeURIComponent(agent.ref)}`)
      const item = page.locator(`[data-inbox-item="${agent.ref}"]`)
      await expect(item.locator('[data-decision-card] [data-decision-approver-lead]')).toHaveText('to be answered by')
      await expect(item.locator('[data-decision-more]')).toHaveCount(1)
      await page.context().close()
    })
  }
})
