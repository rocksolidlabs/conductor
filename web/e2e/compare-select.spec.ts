import type { Browser, Page } from '@playwright/test'
import { dayPage, expect, test, useTheme } from './fixtures'
import { decisionRecord, inboxItem, stubInbox, SELECTION_AUTHORIZES, type StubItem } from './inbox-stub'
import { noSideways, tapTarget } from './layout'
import { runInfo, serveRun, type StubRun } from './run-fixtures'

// A comparison's selection (Program item CP1, docs/plans/compare-crew.md, PR-7), answered on DR1's card. The card is the same act
// wherever it is drawn (the comparison, the ticket's Decisions, Needs you, a phone): for a record of origin `compare` it marks nothing
// recommended and preselects nothing, says each option's candidate with its commit and links it to the ticket's comparison, asks for
// the rationale and holds the confirm until it holds text, and says before the confirm that the answer is final, the confirm naming
// the candidate. An agent's approval beside it is answered as DR1 made it.
//
// This part draws the record from the inbox stub (inbox-stub.ts, its `compare` origin) on the decision page, its run served with its
// ticket (run-fixtures.ts) so the options link to the ticket's comparison, and the respond route answered by the spec, which keeps
// what the page sent.

const DESK = { width: 1440, height: 900 }
const PHONE = { width: 390, height: 844, touch: true }
const TICKET = 'CND-12'
const FINAL = 'Final: the candidates not selected cannot be continued; a new comparison tries them again.'

/** A run launched from TICKET, as the live store lists it: its options link to the ticket's comparison by its key. */
function ticketRun(): StubRun {
  const r = runInfo({ tasks: 6 })
  return { ...r, ticket: { id: 'e'.repeat(16), key: TICKET, rev: 1, plan: 'f'.repeat(16), planN: 1, launch: 'l-1', digest: 'b'.repeat(64) } } as StubRun
}

/** The respond route answered as the server answers an accepted response, each body the page sent kept. */
async function answerResponds(page: Page, items: StubItem[]): Promise<Array<Record<string, unknown>>> {
  const sent: Array<Record<string, unknown>> = []
  await page.route(
    (u) => /^\/api\/decisions\/dr-\d+\/respond$/.test(u.pathname),
    async (route) => {
      const body = route.request().postDataJSON() as Record<string, unknown>
      sent.push(body)
      const id = new URL(route.request().url()).pathname.split('/')[3]!
      const it = items.find((x) => x.ref === `decision:${id}`)!
      const receipt = { decision: id, k: 1, status: 'accepted', words: `Recorded: ${it.choices[body.choice as number]}`, actor: 'operator', byName: 'Nate', choice: body.choice, rationale: body.rationale, at: new Date().toISOString(), rev: 2 }
      const decision = { ...decisionRecord(it, true), state: 'answered', rev: 2, answeredAt: receipt.at, responses: [receipt] }
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ receipt, decision }) })
    },
  )
  return sent
}

/** The decision page with `items` stubbed and `run` served, opened on `open`'s item. */
async function decisionPage(browser: Browser, size: typeof DESK | typeof PHONE, run: StubRun, items: StubItem[], open: StubItem): Promise<{ page: Page; sent: Array<Record<string, unknown>> }> {
  const page = await dayPage(browser, { ...size, path: '/yard' })
  await serveRun(page, run)
  await stubInbox(page, items)
  const sent = await answerResponds(page, items)
  await page.goto(`/decisions?item=${encodeURIComponent(open.ref)}`)
  await expect(page.locator(`[data-inbox-item="${open.ref}"] [data-decision-respond]`)).toBeVisible()
  return { page, sent }
}

test.describe("a selection on DR1's card", () => {
  test("marks nothing, links each candidate to the comparison, holds the confirm until the rationale holds text, and says it is final", async ({ day, browser }) => {
    void day
    const run = ticketRun()
    const sel = inboxItem('decision', { origin: 'compare', run })
    const { page, sent } = await decisionPage(browser, DESK, run, [sel], sel)
    const item = page.locator(`[data-inbox-item="${sel.ref}"]`)
    const card = item.locator('[data-decision-card]')
    const respond = item.locator('[data-decision-respond]')

    // What it authorizes, in the server's words, which say it is final; asked by the comparison; its subject the comparison.
    await expect(card.locator('[data-decision-authorizes-line]')).toHaveText(`A selection authorizes: ${SELECTION_AUTHORIZES}`)
    await expect(card).toContainText('Asked by the comparison in')
    await expect(card.locator('[data-decision-from-compare]')).toBeVisible()
    await expect(card.locator('[data-decision-subject]')).toHaveText('the comparison at base ccccccc, digest dddddddddddd')

    // Nothing is preselected or marked: three choices, none recommended, no confirm step until one is picked.
    await expect(respond.locator('[data-decision-choice]')).toHaveCount(3)
    await expect(respond.locator('[data-decision-recommended]')).toHaveCount(0)
    await expect(respond.locator('[data-decision-recommended-mark]')).toHaveCount(0)
    await expect(respond.locator('[data-decision-confirm-step]')).toHaveCount(0)

    // Each candidate's line names its task, attempt and commit, linked to the ticket's comparison; None of these names none.
    const b = respond.locator('[data-decision-option-subject="1"]')
    await expect(b).toHaveText('the result of approach-b, attempt 2 (4b825dc)')
    await expect(b.locator('a')).toHaveAttribute('href', `/dispatch/CND/12?view=compare&run=${encodeURIComponent(run.id)}`)
    await expect(respond.locator('[data-decision-option-subject="0"]')).toHaveText('the result of approach-a, attempt 1 (1f3a5c7)')
    await expect(respond.locator('[data-decision-option-subject="2"]')).toHaveCount(0)

    // The rationale is asked for as required.
    const why = respond.locator('textarea[data-decision-rationale]')
    await expect(why).toHaveAttribute('placeholder', 'Why (required: the record of this selection)…')
    await expect(why).toHaveAttribute('aria-required', 'true')

    // approach-b picked: the confirm says final and names it, held while the rationale is empty or only spaces.
    await respond.locator('[data-decision-choice="1"]').click()
    const step = respond.locator('[data-decision-confirm-step]')
    await expect(step.locator('[data-decision-final]')).toHaveText(FINAL)
    const confirm = step.locator('[data-decision-confirm]')
    await expect(confirm).toHaveText('Select approach-b (final)')
    await expect(confirm).toBeDisabled()
    await expect(step.locator('[data-decision-rationale-needed]')).toBeVisible()
    await why.fill('   ')
    await expect(confirm).toBeDisabled()

    // None of these picked instead says so in its own words.
    await respond.locator('[data-decision-choice="2"]').click()
    await expect(confirm).toHaveText('Answer None of these (final)')
    await expect(step.locator('[data-decision-final]')).toContainText('the comparison ends with no candidate selected')

    // With a rationale it goes, carrying the choice and the rationale; the receipt says it was recorded.
    await respond.locator('[data-decision-choice="1"]').click()
    await why.fill('approach-b keeps the public API')
    await expect(why).toHaveValue('approach-b keeps the public API')
    await expect(confirm).toBeEnabled()
    await confirm.click()
    await expect(respond.locator('[data-decision-receipt]')).toContainText('Recorded: approach-b at 4b825dc')
    expect(sent).toHaveLength(1)
    expect(sent[0]).toMatchObject({ choice: 1, rationale: 'approach-b keeps the public API', expectedRev: 1 })
  })

  test("leaves an agent's approval as DR1 made it: the rationale optional, the confirm 'Answer <choice>', nothing said final", async ({ day, browser }) => {
    void day
    const run = ticketRun()
    const agent = inboxItem('decision', { run })
    const sel = inboxItem('decision', { origin: 'compare', run })
    const { page, sent } = await decisionPage(browser, DESK, run, [agent, sel], agent)
    const respond = page.locator(`[data-inbox-item="${agent.ref}"] [data-decision-respond]`)
    await expect(respond.locator('textarea[data-decision-rationale]')).toHaveAttribute('placeholder', 'Why (optional, kept with the answer)…')
    await respond.locator('[data-decision-choice="0"]').click()
    const step = respond.locator('[data-decision-confirm-step]')
    await expect(step.locator('[data-decision-confirm]')).toHaveText('Answer Approve')
    await expect(step.locator('[data-decision-final]')).toHaveCount(0)
    await expect(step.locator('[data-decision-confirm]')).toBeEnabled()
    await step.locator('[data-decision-confirm]').click()
    await expect(respond.locator('[data-decision-receipt]')).toBeVisible()
    expect(sent).toHaveLength(1)
    expect(sent[0]!.rationale).toBeUndefined()
  })

  test('on a phone: the same act, every control a touch target, nothing scrolling sideways, in both themes', async ({ day, browser }) => {
    void day
    const run = ticketRun()
    const sel = inboxItem('decision', { origin: 'compare', run })
    const { page } = await decisionPage(browser, PHONE, run, [sel], sel)
    const respond = page.locator(`[data-inbox-item="${sel.ref}"] [data-decision-respond]`)
    // Each theme is a reload (useTheme): the pick is made again in each, on the card drawn anew.
    for (const theme of ['dark', 'light'] as const) {
      await useTheme(page, theme)
      await expect(respond).toBeVisible()
      await expect(respond.locator('[data-decision-confirm-step]')).toHaveCount(0)
      await respond.locator('[data-decision-choice="1"]').tap()
      const confirm = respond.locator('[data-decision-confirm]')
      await expect(confirm).toHaveText('Select approach-b (final)')
      await expect(confirm).toBeDisabled()
      await expect(respond.locator('[data-decision-final]')).toHaveText(FINAL)
      await tapTarget(respond.locator('[data-decision-choice], [data-decision-confirm], [data-decision-back]'))
      await noSideways(page)
    }
  })
})
