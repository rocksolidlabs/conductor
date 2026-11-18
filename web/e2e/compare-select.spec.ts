import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import type { Browser, Page } from '@playwright/test'
import { budget, dayPage, expect, test, useTheme } from './fixtures'
import { compareRun, comparisonOf } from './compare-stub'
import { decisionRecord, inboxItem, stubInbox, SELECTION_AUTHORIZES, type StubItem } from './inbox-stub'
import { inside, noSideways, tapTarget, uncut } from './layout'
import { serveComparison } from './compare-stub'
import { SETTLE_MS, checkScript, comparison, launchComparison, selectionUntil } from './compare-select-helpers'
import { decisionUntil } from './decisions-helpers'
import { ownServer } from './own-server'
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

// The selection and the supporting panel on the comparison (the ticket page's `?view=compare`), its reply from compare-stub.ts with a
// selection and a supporting part of the spec's own, the selection's record from the inbox stub: the panels apart, the selection
// answered through DR1's own answer, then the labels on the columns and the run page's lanes, checked again after a redraw.

const COMPARE_TICKET = 'CND-4'
const COMPARE_PATH = '/dispatch/CND/4'
const LOAD_MS = 15_000

/** The comparison's selection as the reply carries it (PR-6b), from the record's options. */
function selectionOf(it: StubItem, state: string, extra: Record<string, unknown> = {}): Record<string, unknown> {
  const rec = decisionRecord(it) as { request: { options: unknown[] } }
  return { decision: it.ref.slice('decision:'.length), state, options: rec.request.options, ...extra }
}

/** What the agents and Brakeman said, all for approach-a, the candidate the measured part shows failing nothing it did not fail. */
const SAID = {
  claims: [{ task: 'approach-a', attempt: 1, summary: 'approach-a is clearly the one to select: <b>select me</b>.' }],
  reviews: [
    { task: 'approach-a', attempt: 1, verdict: 'approved', findings: [] },
    { task: 'approach-b', attempt: 3, verdict: 'request_changes', findings: ['the bench script was changed'] },
  ],
  brakeman: { why: 'Brakeman is not set up' },
}

/**
 * The compare view of `run`, waited for by what draws it: the comparison's reply (the route's own, from compare-stub), then the view
 * drawn from it. A loaded machine takes seconds over the page's other reads before the comparison is asked: the wait is on the reply,
 * bounded by a hang's bound, and the test's timeout is raised by it.
 */
async function openCompare(page: Page, run: string): Promise<void> {
  budget(LOAD_MS * 4)
  const replied = page.waitForResponse((r) => new URL(r.url()).pathname === `/api/runs/${run}/comparison`, { timeout: LOAD_MS * 4 })
  await page.goto(`${COMPARE_PATH}?view=compare&run=${run}`)
  await replied
  await expect(page.locator('[data-compare-view][data-compare-state="content"]')).toBeVisible()
}

test.describe('the selection and the supporting panel on the comparison', () => {
  test('keeps the two panels apart, answers the selection, then labels the columns and the lanes, again after a redraw', async ({ day, browser }) => {
    await day.part('dispatch')
    budget(6 * LOAD_MS)
    const run = compareRun({ ticket: COMPARE_TICKET })
    // The record's candidates are the comparison's: approach-a at its commit; approach-b's last commit failed, so it is no option.
    const sel = inboxItem('decision', { origin: 'compare', run, candidates: [{ task: 'approach-a', n: 1, commit: 'a'.repeat(40) }] })
    let selection: Record<string, unknown> = selectionOf(sel, 'open')
    let supporting: Record<string, unknown> | undefined
    const shape = { run: run.id, failed: true }
    const page = await dayPage(browser, { path: '/agents' })
    await serveRun(page, run)
    await stubInbox(page, [sel])
    const sent = await answerResponds(page, [sel])
    await serveComparison(page, run.id, () => ({ ...comparisonOf(shape), selection, ...(supporting ? { supporting } : {}) }))
    const asked: Array<Record<string, unknown>> = []
    await page.route(
      (u) => u.pathname === '/api/brakeman/requests',
      async (route) => {
        if (route.request().method() !== 'POST') return route.fallback()
        const body = route.request().postDataJSON() as Record<string, unknown>
        asked.push(body)
        const request = { id: 'br-7', opId: body.opId, kind: 'compare', subjects: body.subjects, createdAt: new Date().toISOString(), state: 'queued', agent: { id: 'brakeman-stub' }, config: { visibility: {}, scope: {}, permissions: {}, budget: {}, rev: 1 }, reads: 0 }
        // As the server does: the comparison names the request asked from then on.
        supporting = { ...SAID, brakeman: { request: 'br-7', state: 'queued' } }
        await route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify({ request }) })
      },
    )
    const open = () => openCompare(page, run.id)

    // The measured part, first with nothing said.
    await open()
    const v = page.locator('[data-compare-view]')
    const cells = async () => v.locator('[data-compare-cell]').evaluateAll((els) => els.map((e) => `${e.closest('[data-compare-check]')?.getAttribute('data-compare-check')}:${e.getAttribute('data-compare-cell')}:${e.getAttribute('data-cell-state')}`))
    const plain = await cells()
    expect(plain.length).toBeGreaterThan(0)

    // Everything said favours approach-a: the measured part is drawn the same, and what was said is in its own panel, below it, as text.
    supporting = SAID
    await open()
    expect(await cells()).toEqual(plain)
    const panel = v.locator('[data-compare-supporting]')
    await expect(panel).toContainText('Supporting information, not evidence')
    const claim = panel.locator('[data-compare-claim="approach-a"]')
    await expect(claim.locator('[data-compare-source]')).toHaveText("said by approach-a's agent, attempt 1")
    await expect(claim).toContainText('<b>select me</b>')
    await expect(claim.locator('b')).toHaveCount(0)
    await expect(panel.locator('[data-compare-review="approach-b"]')).toContainText('the bench script was changed')
    const measuredBox = await v.locator('[data-compare-measured]').boundingBox()
    const panelBox = await panel.boundingBox()
    expect(panelBox!.y).toBeGreaterThanOrEqual(measuredBox!.y + measuredBox!.height)
    await expect(v.locator('[data-compare-measured] [data-compare-supporting]')).toHaveCount(0)
    await expect(panel.locator('[data-compare-brakeman-words]')).toHaveText('Brakeman is not set up')

    // Ask Brakeman to compare: one request of kind compare on the comparison's launched run.
    await panel.locator('[data-compare-ask-brakeman]').click()
    await expect(panel.locator('[data-compare-ask-words]')).toHaveText('Asked: br-7 is queued.')
    // The page reads the comparison again by itself: the request asked is the panel's, and no second one can be asked meanwhile.
    await expect(panel.locator('[data-compare-brakeman-words]')).toHaveText("Brakeman's draft is queued (br-7)")
    await expect(panel.locator('[data-compare-ask-brakeman]')).toBeDisabled()
    expect(asked).toHaveLength(1)
    expect(asked[0]).toMatchObject({ kind: 'compare', subjects: [`run:${run.id}`] })
    // The person's op id is the page's own: one beginning `cmp-` is the server's, for the draft the settling asks.
    expect(String(asked[0]!.opId)).toMatch(/^[0-9a-f]{16}$/)

    // The selection: open, said at the top, nothing preselected or recommended, answered through DR1's own answer.
    await expect(v.locator('[data-compare-waits]')).toBeVisible()
    const select = v.locator('[data-compare-select]')
    await expect(select.locator('[data-compare-select-state]')).toHaveText("Open: waits for the operator's answer")
    await expect(select.locator('[data-decision-choice]')).toHaveCount(2)
    await expect(select.locator('[data-decision-recommended], [data-decision-confirm-step]')).toHaveCount(0)
    await select.locator('[data-decision-choice="0"]').click()
    const confirm = select.locator('[data-decision-confirm]')
    await expect(confirm).toHaveText('Select approach-a (final)')
    await expect(confirm).toBeDisabled()
    await expect(select.locator('[data-decision-final]')).toHaveText(FINAL)
    await select.locator('textarea[data-decision-rationale]').fill('the only candidate whose checks all ran')
    await confirm.click()
    await expect.poll(() => sent.length).toBe(1)
    expect(sent[0]).toMatchObject({ choice: 0, rationale: 'the only candidate whose checks all ran' })

    // Applied: the columns and the run page's lanes say selected and not selected; no answer is offered any more.
    selection = selectionOf(sel, 'applied', { chosen: 0, rationale: 'the only candidate whose checks all ran', by: 'Nate', applied: { how: 'selected', selected: { task: 'approach-a', run: run.id, attempt: 1, commit: 'a'.repeat(40) }, words: 'approach-a selected at aaaaaaa, by Nate' } })
    sel.decision = { state: 'applied', applied: { at: new Date().toISOString(), by: 'yardmaster', run: run.id, response: 1, validated: true, how: 'selected', selected: { task: 'approach-a', run: run.id, attempt: 1, commit: 'a'.repeat(40) }, words: 'approach-a selected at aaaaaaa, by Nate' } }
    const labelled = async () => {
      await open()
      await expect(select.locator('[data-compare-select-state]')).toHaveText('Applied: approach-a selected at aaaaaaa, by Nate')
      await expect(select.locator('[data-decision-respond]')).toHaveCount(0)
      await expect(v.locator('[data-compare-waits]')).toHaveCount(0)
      await expect(v.locator('[data-compare-candidate="approach-a"] [data-compare-selection]')).toHaveText('selected')
      await expect(v.locator('[data-compare-candidate="approach-b"] [data-compare-selection]')).toHaveText('not selected')
      await page.goto(`/runs/${run.id}?view=tasks`)
      await expect(page.locator('[data-task-lane="approach-a"]')).toHaveAttribute('data-lane-selection', 'selected')
      await expect(page.locator('[data-task-lane="approach-b"]')).toHaveAttribute('data-lane-selection', 'not-selected')
      await expect(page.locator('[data-task-lane="approach-b"] [data-task-lane-selection]')).toHaveText('not selected')
    }
    await labelled()
    // A mode switch and the page's own redraw: the labels again.
    await useTheme(page, 'light')
    await expect(page.locator('[data-task-lane="approach-a"]')).toHaveAttribute('data-lane-selection', 'selected')
    await labelled()
  })

  test('wide and on a phone: the candidates, the measured part, the supporting panel, then the selection, a line at the top that takes you to it', async ({ day, browser }) => {
    await day.part('dispatch')
    const run = compareRun({ ticket: COMPARE_TICKET })
    const sel = inboxItem('decision', { origin: 'compare', run, candidates: [{ task: 'approach-a', n: 1, commit: 'a'.repeat(40) }] })
    for (const size of [DESK, PHONE]) {
      const page = await dayPage(browser, { ...size, path: '/agents' })
      await serveRun(page, run)
      await stubInbox(page, [sel])
      await serveComparison(page, run.id, () => ({ ...comparisonOf({ run: run.id, failed: true }), selection: selectionOf(sel, 'open'), supporting: SAID }))
      for (const theme of ['dark', 'light'] as const) {
        // This pass's caps beside openCompare's own: the theme's reload, the measures, the press and what it brings into view.
        budget(8 * LOAD_MS)
        await openCompare(page, run.id)
        await useTheme(page, theme, '[data-compare-select] [data-decision-choice]')
        const v = page.locator('[data-compare-view]')
        const box = async (s: string) => (await v.locator(s).first().boundingBox())!
        const [columns, measured, supporting, select] = [await box('[data-compare-columns]'), await box('[data-compare-measured]'), await box('[data-compare-supporting]'), await box('[data-compare-select]')]
        // Each part starts below the end of the one before it: the answer sits under everything it is answered from.
        const at = `${size.width} wide, ${theme}`
        expect(measured.y, `the measured part after the candidates, ${at}`).toBeGreaterThanOrEqual(columns.y + columns.height)
        expect(supporting.y, `the supporting panel after the measured part, ${at}`).toBeGreaterThanOrEqual(measured.y + measured.height)
        expect(select.y, `the selection after the supporting panel, ${at}`).toBeGreaterThanOrEqual(supporting.y + supporting.height)
        if (size === PHONE) await tapTarget(v.locator('[data-compare-select] [data-decision-choice], [data-compare-ask-brakeman]'))
        await noSideways(page)

        // At the top, above the candidates, a line says a selection waits; its press brings the panel into view and gives it the focus.
        const waits = v.locator('[data-compare-waits]')
        await expect(waits, at).toContainText(`A selection waits for your answer (${sel.ref.slice('decision:'.length)})`)
        expect((await box('[data-compare-waits]')).y, `the line above the candidates, ${at}`).toBeLessThan(columns.y)
        await page.evaluate(() => window.scrollTo(0, 0))
        await expect(v.locator('[data-compare-select]')).not.toBeInViewport()
        if (size === PHONE) await tapTarget(waits.locator('[data-compare-waits-go]'))
        await waits.locator('[data-compare-waits-go]').click()
        await expect(v.locator('[data-compare-select]'), `the press brings the selection into view, ${at}`).toBeInViewport()
        await expect(v.locator('[data-compare-select]')).toBeFocused()
      }
      await page.context().close()
    }
  })

  test("an option's longest words wrap inside the selection, wide and on a phone, in both themes, nothing cut", async ({ day, browser }) => {
    await day.part('dispatch')
    budget(6 * LOAD_MS)
    const run = compareRun({ ticket: COMPARE_TICKET })
    const sel = inboxItem('decision', { origin: 'compare', run, candidates: [{ task: 'approach-a', n: 1, commit: 'a'.repeat(40) }, { task: 'approach-b', n: 3, commit: 'b'.repeat(40) }] })
    // The server's words at their longest (an option's words are cut to the ledger's 1 KiB): a clause per check with its verdict and
    // every reason, one of them a path with no place to break.
    const clause = (i: number) => `check check-${i}: passed, not comparable (approach-a changed internal/cache/locale/${'x'.repeat(60)}_${i}.sh, which the check runs; approach-b: not run: an earlier check failed)`
    let long = 'attempt 1'
    for (let i = 0; long.length < 1024; i++) long = `${long}; ${clause(i)}`
    // Cut to the ledger's bound, as the server cuts it.
    long = long.slice(0, 1024)
    expect(long.length).toBe(1024)
    const record = decisionRecord(sel, true) as { request: { options: Array<{ tradeoffs?: string }>; context: string } }
    for (const o of record.request.options.slice(0, 2)) o.tradeoffs = long
    record.request.context = `approach-a: aaaaaaa, attempt 1; ${long.slice('attempt 1; '.length)}.`
    for (const size of [DESK, PHONE]) {
      const page = await dayPage(browser, { ...size, path: '/agents' })
      await serveRun(page, run)
      await stubInbox(page, [sel])
      // The record read by its id, with the long words (registered after the stub, so it answers first).
      await page.route(
        (u) => u.pathname === `/api/decisions/${sel.ref.slice('decision:'.length)}`,
        (route) => (route.request().method() === 'GET' ? route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ decision: record, refs: [] }) }) : route.fallback()),
      )
      await serveComparison(page, run.id, () => ({ ...comparisonOf({ run: run.id }), selection: { ...selectionOf(sel, 'open'), options: record.request.options } }))
      for (const theme of ['dark', 'light'] as const) {
        // This pass's caps beside openCompare's own: the theme's reload, two expects per line, the measures.
        budget(8 * LOAD_MS)
        await openCompare(page, run.id)
        await useTheme(page, theme, '[data-compare-select] [data-decision-choice]')
        const select = page.locator('[data-compare-select]')
        const lines = select.locator('[data-decision-tradeoff]')
        await expect(lines.first()).toContainText(long.slice(0, 200))
        const at = `${size.width} wide, ${theme}`
        for (const i of [0, 1]) {
          const line = select.locator(`[data-decision-tradeoff="${i}"]`)
          await expect(line, at).toContainText(long.slice(-120))
          await line.scrollIntoViewIfNeeded()
          await uncut(line)
        }
        await inside(lines, select)
        await noSideways(page, { root: select })
      }
      await page.context().close()
    }
  })

  test('a selection closed because a lane changed says so and offers no answer, and none is offered before one opens', async ({ day, browser }) => {
    await day.part('dispatch')
    budget(3 * LOAD_MS)
    const run = compareRun({ ticket: COMPARE_TICKET })
    const sel = inboxItem('decision', { origin: 'compare', run, candidates: [{ task: 'approach-a', n: 1, commit: 'a'.repeat(40) }] })
    sel.decision = { state: 'superseded', why: 'approach-b was retried: dr-closed no longer lists what is compared' }
    let selection: Record<string, unknown> | undefined = selectionOf(sel, 'superseded')
    let running = false
    const page = await dayPage(browser, { path: '/agents' })
    await serveRun(page, run)
    await stubInbox(page, [sel])
    await serveComparison(page, run.id, () => ({ ...comparisonOf({ run: run.id, running }), ...(selection ? { selection } : {}) }))
    await openCompare(page, run.id)
    const select = page.locator('[data-compare-select]')
    await expect(select.locator('[data-compare-select-words]')).toHaveText(`${sel.ref.slice('decision:'.length)} is closed: approach-b was retried: dr-closed no longer lists what is compared. A new selection opens when every lane has ended; answer that one.`)
    await expect(select.locator('[data-decision-respond], [data-decision-choice]')).toHaveCount(0)

    // While a lane runs and no record is open: said, and nothing to answer.
    selection = undefined
    running = true
    await openCompare(page, run.id)
    await expect(select.locator('[data-compare-select-words]')).toHaveText('The selection opens when every lane has ended. Nothing is selected by default.')
    await expect(select.locator('[data-decision-choice]')).toHaveCount(0)
  })
})

// Brakeman's draft on the real server (PR-5): the template asks for it, the comparison's settling queues one request, and the stub's
// turn waits at a gate the spec opens once it has written the brief (web/e2e/fixtures/compare-brief.json, its refs the run's own).
// The draft arrives in the supporting panel while the run sits idle: the page follows the request, not the run.

test('Brakeman’s stub draft arrives in the supporting panel while the run is idle', async ({ browser }) => {
  budget(6 * LOAD_MS + 60_000)
  const fixture = join(process.cwd(), 'e2e', 'fixtures', 'compare-brief.json')
  // The gate and the brief in a folder of the test's own, named in the stub's copy before its server starts.
  const files = mkdtempSync(join(tmpdir(), 'conductor-e2e-compare-brief-'))
  const gate = join(files, 'gate')
  const brief = join(files, 'brief.json')
  const s = await ownServer('compare-brakeman', { agents: [{ id: 'bm-compare', from: 'brakeman-stub', env: { STUB_BRAKEMAN_WAIT: gate, STUB_BRAKEMAN_BRIEF: brief } }] })
  try {
    const { config } = await s.api.ok<{ config: Record<string, unknown> & { rev: number } }>('GET', '/api/brakeman')
    await s.api.ok('PUT', '/api/brakeman/config', { rev: config.rev, config: { ...config, enabled: true, agentId: 'bm-compare' } })
    checkScript(s.repo)
    const c = await launchComparison(s.api, s.repo, 'CBRK', [{ agentId: 'worker-cli' }, { agentId: 'worker-cli' }], { brakeman: true })
    budget(SETTLE_MS)
    let request = ''
    await expect
      .poll(async () => (request = ((await s.api.ok<{ supporting?: { brakeman?: { request?: string } } }>('GET', `/api/runs/${c.run}/comparison`)).supporting?.brakeman?.request ?? '')), { timeout: SETTLE_MS, intervals: [1000], message: 'the settling queued a Brakeman request' })
      .not.toBe('')
    const page = await dayPage(browser, { server: s.started, path: '/agents' })
    await page.goto(`${c.path}?view=compare&run=${c.run}`)
    const panel = page.locator('[data-compare-supporting]')
    await expect(panel.locator('[data-compare-brakeman-words]')).toHaveText(new RegExp(`^(Brakeman's draft is queued|Brakeman is drafting) \\(${request}\\)$`))
    await expect(panel.locator('[data-compare-ask-brakeman]')).toBeDisabled()
    const revision = (await s.api.run(c.run)).managed?.revision

    // The brief, its refs the run's own candidates and checks, then the gate: the turn submits it.
    const read = await comparison(s.api, c.run)
    const [a, b] = read.candidates
    const text = readFileSync(fixture, 'utf8')
      .replace('@CANDIDATE_A', `candidate:${a!.run}/${a!.task}/${a!.attempt}`)
      .replace('@CANDIDATE_B', `candidate:${b!.run}/${b!.task}/${b!.attempt}`)
      .replace('@CHECK_A', `check:${a!.run}/${a!.task}/${a!.attempt}/unit/1`)
      .replace('@CHECK_B', `check:${b!.run}/${b!.task}/${b!.attempt}/unit/1`)
    writeFileSync(brief, text)
    writeFileSync(gate, '')

    // Briefed: the draft in the panel, its table in its own card, the page never reloaded; the run did not move.
    const table = panel.locator('[data-compare-brief] [data-brief-comparison]')
    await expect(table).toBeVisible({ timeout: SETTLE_MS })
    await expect(table.locator('tbody th')).toHaveText(['Behaviour', 'Complexity', 'Dependency changes', 'Checks', 'Unresolved tradeoffs'])
    await expect(panel.locator('[data-compare-brakeman-words]')).toHaveText(`Brakeman's draft (${request})`)
    expect((await s.api.run(c.run)).managed?.revision).toBe(revision)
    // Nothing of it reached the measured part.
    await expect(page.locator('[data-compare-measured] [data-brief-comparison]')).toHaveCount(0)
    await page.context().close()
  } finally {
    await s.stop()
    rmSync(files, { recursive: true, force: true })
  }
})

// The selection on the real server (PR-6a, PR-6b): two stub candidates of one base launched from a ticket, every lane ended, the
// selection opened by Yardmaster. Every wait is on the server's own reads (the comparison, DR1's record, the run); what the page
// shows is checked against them.

test.describe('the selection on the real server', () => {
  test('answered on the comparison with a rationale, the same act on the card, then labelled; a lane not selected takes no command', async ({ browser }) => {
    budget(8 * LOAD_MS + 60_000)
    const s = await ownServer('compare-select')
    try {
      checkScript(s.repo)
      const c = await launchComparison(s.api, s.repo, 'CSEL', [{ agentId: 'worker-cli', approach: 'Key the cache by locale' }, { agentId: 'worker-cli', approach: 'Memoise in the handler' }])
      const sel = await selectionUntil(s.api, c.run, 'open')
      expect(sel.options.map((o) => o.label.replace(/ at [0-9a-f]+$/, ''))).toEqual(['approach-a', 'approach-b', 'None of these'])
      const b = sel.options[1]!.subject!
      const page = await dayPage(browser, { server: s.started, path: '/agents' })

      // Needs you lists it, by its ref.
      await page.goto('/decisions')
      await expect(page.locator(`[data-inbox-row="decision:${sel.decision}"]`)).toBeVisible()

      // The card, from the ticket's Decisions: the options' commits, nothing marked, the rationale required.
      await page.goto(c.path)
      await page.locator(`[data-ticket-decision-open="${sel.decision}"]`).click()
      const modal = page.locator(`[data-decision-modal][data-decision-modal-id="${sel.decision}"]`)
      await expect(modal.locator('[data-decision-option-subject="1"]')).toHaveText(`the result of approach-b, attempt ${b.n} (${b.commit.slice(0, 7)})`)
      await expect(modal.locator('[data-decision-recommended]')).toHaveCount(0)
      await modal.locator('[data-decision-choice="1"]').click()
      await expect(modal.locator('[data-decision-confirm]')).toHaveText('Select approach-b (final)')
      await expect(modal.locator('[data-decision-confirm]')).toBeDisabled()
      await modal.locator('[data-decision-back]').click()
      await page.keyboard.press('Escape')
      await expect(modal).toHaveCount(0)

      // The comparison: no choice made; held without a rationale and said final; approach-b selected with one.
      await page.goto(`${c.path}?view=compare&run=${c.run}`)
      const select = page.locator('[data-compare-select]')
      await expect(select).toHaveAttribute('data-compare-selection', sel.decision)
      await expect(select.locator('[data-compare-select-state]')).toHaveText("Open: waits for the operator's answer")
      await expect(select.locator('[data-decision-confirm-step]')).toHaveCount(0)
      await select.locator('[data-decision-choice="1"]').click()
      const confirm = select.locator('[data-decision-confirm]')
      await expect(confirm).toBeDisabled()
      await expect(select.locator('[data-decision-final]')).toHaveText(FINAL)
      await select.locator('textarea[data-decision-rationale]').fill('approach-b keeps the handler simple')
      await confirm.click()
      // Recorded, then applied: the record's own states, the page following them by itself.
      await decisionUntil(s.api, sel.decision, 'applied')
      await expect(select.locator('[data-compare-select-state]')).toHaveText(`Applied: approach-b selected at ${b.commit.slice(0, 7)}`)
      await expect(select.locator('[data-compare-select-rationale]')).toHaveText('approach-b keeps the handler simple')
      await expect(select.locator('[data-decision-respond]')).toHaveCount(0)

      // The labels, on the columns and the run page's lanes; again after a mode switch and the page's own redraw.
      const labelled = async () => {
        await page.goto(`${c.path}?view=compare&run=${c.run}`)
        await expect(page.locator('[data-compare-candidate="approach-b"] [data-compare-selection]')).toHaveText('selected')
        await expect(page.locator('[data-compare-candidate="approach-a"] [data-compare-selection]')).toHaveText('not selected')
        await page.goto(`/runs/${c.run}?view=tasks`)
        await expect(page.locator('[data-task-lane="approach-b"]')).toHaveAttribute('data-lane-selection', 'selected')
        await expect(page.locator('[data-task-lane="approach-a"]')).toHaveAttribute('data-lane-selection', 'not-selected')
        // The ticket's candidates of the run (DispatchCandidates): the applied candidate selected, the other lane's not selected.
        await page.goto(c.path)
        const cands = page.locator(`[data-candidates][data-candidates-run="${c.run}"]`)
        await expect(cands.locator(`[data-candidate-task="approach-b"] [data-candidate="approach-b/${b.n}"] [data-candidate-selection]`)).toHaveText('selected')
        await expect(cands.locator('[data-candidate-task="approach-a"] [data-candidate-selection]')).toHaveText('not selected')
      }
      await labelled()
      await useTheme(page, 'light')
      await labelled()

      // Needs you no longer lists it: its list read (its empty words drawn, nothing else needing anyone on this server), then no row.
      await page.goto('/decisions')
      await expect(page.locator('[data-decisions-list] [data-decisions-empty]').first()).toBeVisible()
      await expect(page.locator(`[data-inbox-row="decision:${sel.decision}"]`)).toHaveCount(0)

      // A Retry of approach-a, a lane not selected, is refused in the server's words and changes nothing.
      const rev = (await s.api.run(c.run)).managed?.revision ?? 0
      const r = await s.api.call<{ error: { code: string; message: string } }>('POST', `/api/runs/${c.run}/tasks/approach-a/retry`, { commandId: 'csel-retry-a', expectedRevision: rev })
      expect(r.status).toBe(409)
      expect(r.body.error).toEqual({ code: 'not_selected', message: `approach-a was not selected in ${sel.decision}: launch a new comparison to try it again` })
      expect((await comparison(s.api, c.run)).selection?.state).toBe('applied')
      await page.context().close()
    } finally {
      await s.stop()
    }
  })

  test('a Retry of a failed lane closes the selection and a new one opens; a Stop keeps it, its answer applied on Continue', async ({ browser }) => {
    budget(8 * LOAD_MS + 60_000)
    // approach-b's stub edits the check's script, which then fails: its lane ends failed, and only approach-a is an option. Its retried
    // attempt waits while the spec holds it (STUB_ATTEMPT_HOLD), so the closed selection is checked before a new one can open.
    const files = mkdtempSync(join(tmpdir(), 'conductor-e2e-compare-hold-'))
    const hold = join(files, 'hold')
    const s = await ownServer('compare-select-retry', { agents: [{ id: 'cand-fail', from: 'worker-cli', env: { STUB_COMPARE_EDIT: 'script', STUB_ATTEMPT_HOLD: hold } }] })
    try {
      checkScript(s.repo)
      const c = await launchComparison(s.api, s.repo, 'CSRT', [{ agentId: 'worker-cli' }, { agentId: 'cand-fail' }])
      const first = await selectionUntil(s.api, c.run, 'open')
      expect(first.options.map((o) => o.label.replace(/ at [0-9a-f]+$/, ''))).toEqual(['approach-a', 'None of these'])
      const page = await dayPage(browser, { server: s.started, path: '/agents' })
      await page.goto(`${c.path}?view=compare&run=${c.run}`)
      const select = page.locator('[data-compare-select]')
      await expect(select).toHaveAttribute('data-compare-selection', first.decision)

      // A Retry of the failed lane while selecting: the selection is closed, the panel says so and offers no answer.
      const rev = (await s.api.run(c.run)).managed?.revision ?? 0
      writeFileSync(hold, '')
      await s.api.ok('POST', `/api/runs/${c.run}/tasks/approach-b/retry`, { commandId: 'csrt-retry-b', expectedRevision: rev })
      await decisionUntil(s.api, first.decision, 'superseded')
      await expect(select.locator('[data-compare-select-words]')).toContainText(`${first.decision} is closed`)
      await expect(select.locator('[data-decision-choice]')).toHaveCount(0)
      // Still held: the comparison names the closed record, and no other opened.
      expect((await comparison(s.api, c.run)).selection?.decision).toBe(first.decision)
      // The lane goes on and ends again (failed again): a new selection opens, the one to answer.
      rmSync(hold, { force: true })
      const second = await selectionUntil(s.api, c.run, 'open', { not: first.decision })
      await expect(select).toHaveAttribute('data-compare-selection', second.decision)
      await expect(select.locator('[data-decision-choice]')).toHaveCount(second.options.length)

      // A Stop while selecting, from the run page: its confirm says the selection stays open and how it is answered, its words clear of
      // the close button, wide and on a phone, in both themes; then the panel stays open with the same options, and the answer is
      // recorded and waits for the run.
      for (const size of [DESK, PHONE]) {
        await page.setViewportSize(size)
        for (const theme of ['dark', 'light'] as const) {
          // This pass's caps: the page and its theme's reload, the press, the words' wait, the measures, the close.
          budget(7 * LOAD_MS)
          await page.goto(`/runs/${c.run}?view=tasks`)
          await useTheme(page, theme, '[data-crew-run-header] [data-run-stop]')
          await page.locator('[data-crew-run-header] [data-run-stop]').click()
          const confirm = page.getByRole('dialog').filter({ has: page.locator('[data-confirm-stop]') })
          const at = `${size.width} wide, ${theme}`
          await expect(confirm.locator('[data-run-stop-selection]'), at).toHaveText(
            `Its selection, ${second.decision}, stays open: you can answer it on the stopped run, and the answer is applied when the run is continued. Stopping selects nothing and cancels nothing.`,
            { timeout: LOAD_MS },
          )
          const close = (await confirm.locator('[data-slot="close"]').boundingBox())!
          const words = (await confirm.locator('[data-slot="description"]').boundingBox())!
          expect(words.x + words.width <= close.x || words.y >= close.y + close.height, `the description clear of the close button, ${at}`).toBe(true)
          await noSideways(page)
          if (size === PHONE && theme === 'light') break
          await page.keyboard.press('Escape')
          await expect(confirm).toHaveCount(0)
        }
      }
      budget(30_000 + 6 * LOAD_MS)
      await page.locator('[data-confirm-stop]').click()
      await expect.poll(async () => !!(await s.api.run(c.run)).stoppedAt, { timeout: 30_000, intervals: [500], message: 'the run is stopped' }).toBe(true)
      await page.setViewportSize(DESK)
      await page.goto(`${c.path}?view=compare&run=${c.run}`)
      await expect(select.locator('[data-decision-choice]')).toHaveText(second.options.map((o) => o.label))
      await select.locator('[data-decision-choice="0"]').click()
      await select.locator('textarea[data-decision-rationale]').fill('the one whose checks pass')
      await select.locator('[data-decision-confirm]').click()
      await decisionUntil(s.api, second.decision, 'answered')
      await expect(select.locator('[data-compare-select-state]')).toHaveText('Recorded: waiting for the run to be continued')

      // Continue: applied in the continuation's first transaction.
      await s.api.ok('POST', `/api/runs/${c.run}/resume`, {})
      await decisionUntil(s.api, second.decision, 'applied', SETTLE_MS)
      await expect(select.locator('[data-compare-select-state]')).toHaveText(/^Applied: approach-a selected at [0-9a-f]{7}$/)

      // The ticket's candidates of the continuation, which submitted nothing of its own, name the run its results are kept from.
      budget(60_000 + 3 * LOAD_MS)
      const next = (await s.api.run(c.run)).resumedBy
      expect(next, 'the run is continued').toBeTruthy()
      await page.goto(c.path)
      await expect(page.locator(`[data-candidates][data-candidates-run="${next}"] [data-candidates-none]`)).toHaveText(
        `No attempt of this run has submitted a result yet. A result its tasks keep from the run it continues (${c.run.slice(c.run.lastIndexOf('-') + 1)}) is that run's candidate, listed with that run.`,
        { timeout: 60_000 },
      )
      await expect(page.locator(`[data-candidates][data-candidates-run="${c.run}"] [data-candidate-task="approach-a"] [data-candidate-selection]`)).toHaveText('selected')
      await page.context().close()
    } finally {
      rmSync(hold, { force: true })
      await s.stop()
      rmSync(files, { recursive: true, force: true })
    }
  })
})
