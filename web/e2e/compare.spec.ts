import type { Page } from '@playwright/test'
import { budget, dayPage, expect, readAgain, test, useTheme } from './fixtures'
import { BASE, NOT_OBSERVED, compareRun, comparisonOf, serveComparison, type ComparisonShape } from './compare-stub'
import { filledPrimaries, walk, worst } from './contrast'
import { noSideways } from './layout'
import { runInfo, serveRun, serveTicketRuns, type StubRun } from './run-fixtures'

// The comparison (Program item CP1, docs/plans/compare-crew.md, PR-4): the ticket page's compare view, `?view=compare&run=…`, drawn
// from the server's reply alone (compare-stub.ts here, the real server's in the walk below once its rows are in): the base, the
// objective and criteria, a column per candidate with its state, the checks with the server's verdicts and their reasons, each cell's
// outcome opening its receipt, the size, dependency, test-file and independence rows with what is not observed; the run card's and
// the run header's links to it; the run's tasks grouped by lane. Measured, never pixels; both themes; a phone at 390 x 844.

/** The ticket whose Runs section shows the run: one of the day's with no run of its own. */
const TICKET = 'CND-4'
const TICKET_PATH = '/dispatch/CND/4'
/** One page load's and its first read's bound (the config's expect timeout): each load raises the test's timeout by it. */
const LOAD_MS = 15_000

/** A page of the day's with the comparison's run and its reply routed before the page that draws them loads. */
async function opened(browser: Parameters<typeof dayPage>[0], run: StubRun, reply: () => Record<string, unknown> | null, o: { width?: number; height?: number; touch?: boolean } = {}): Promise<{ page: Page; reads: () => number }> {
  const page = await dayPage(browser, { path: '/agents', ...o })
  await serveRun(page, run)
  await serveTicketRuns(page, TICKET, run)
  const served = await serveComparison(page, run.id, reply)
  return { page, reads: () => served.reads }
}

async function view(page: Page, run: string): Promise<void> {
  budget(LOAD_MS)
  await page.goto(`${TICKET_PATH}?view=compare&run=${run}`)
  await expect(page.locator('[data-compare-view][data-compare-state="content"]')).toBeVisible()
}

test('the compare view draws the comparison as the server measured it, in place of the ticket’s sections', async ({ day, browser }) => {
  await day.part('dispatch')
  const run = compareRun({ ticket: TICKET })
  let shape: ComparisonShape = { run: run.id, failed: true }
  const { page } = await opened(browser, run, () => comparisonOf(shape))
  await view(page, run.id)
  const v = page.locator('[data-compare-view]')
  await expect(v).toHaveAttribute('data-compare-run', run.id)
  // In place of the strip and the sections, as the Review view is.
  await expect(page.locator('[data-ticket-strip]')).toHaveCount(0)
  await expect(page.locator('#ticket-description')).toHaveCount(0)

  // The base, the objective, the criteria.
  await expect(v.locator('[data-compare-base]')).toHaveText(BASE.slice(0, 12))
  await expect(v.locator('[data-compare-objective]')).toHaveText('Make the locale cache read faster without a new dependency')
  await expect(v.locator('[data-compare-criterion]')).toHaveText(['no new dependency', 'p99 of the cache read under 2 ms in bench_test.go'])

  // A column per candidate, in the block's order: a ready one, and a failed lane's last submitted commit, not selectable.
  const cols = v.locator('[data-compare-candidate]')
  await expect(cols).toHaveCount(2)
  expect(await cols.evaluateAll((els) => els.map((e) => e.getAttribute('data-compare-candidate')))).toEqual(['approach-a', 'approach-b'])
  const a = v.locator('[data-compare-candidate="approach-a"]')
  const b = v.locator('[data-compare-candidate="approach-b"]')
  await expect(a).toHaveAttribute('data-state', 'ready')
  await expect(a).toHaveAttribute('data-selectable', 'true')
  await expect(a.locator('[data-status-mark]')).toHaveAttribute('data-status-mark', 'done')
  await expect(a.locator('[data-compare-candidate-approach]')).toHaveText('Key the cache by locale')
  await expect(b).toHaveAttribute('data-state', 'failed')
  await expect(b).toHaveAttribute('data-selectable', 'false')
  await expect(b.locator('[data-status-mark]')).toHaveAttribute('aria-label', 'failed: not selectable')
  await expect(b.locator('[data-compare-candidate-commit]')).toContainText('last submitted commit · failed: not selectable')
  await expect(b.locator('[data-compare-candidate-args]')).toHaveText('--model small')
  await expect(b.locator('[data-compare-candidate-attempts]')).toHaveText('attempt 3 shown · 3 attempts made')
  // Under the columns, the selection's panel: with no record yet, when one opens (PR-7, CompareSelect).
  await expect(v.locator('[data-compare-select] [data-compare-select-words]')).toHaveText(/^(The selection opens when every lane has ended\. Nothing is selected by default\.|The selection opens next: every lane has ended\.)$/)

  // The checks: each with the server's verdict and its reasons, each cell its outcome.
  const unit = v.locator('[data-compare-check="check/unit"]')
  await expect(unit).toHaveAttribute('data-verdict', 'comparable')
  await expect(unit.locator('[data-compare-verdict-words]')).toHaveText('comparable')
  await expect(unit.locator('[data-compare-why]')).toHaveCount(0)
  await expect(unit.locator('[data-compare-cell="approach-a"]')).toHaveAttribute('data-cell-state', 'passed')
  await expect(unit.locator('[data-compare-cell="approach-a"] [data-compare-cell-detail]')).toHaveText('4.2 s · native')
  const bench = v.locator('[data-compare-check="check/bench"]')
  await expect(bench).toHaveAttribute('data-verdict', 'not_comparable')
  await expect(bench.locator('[data-compare-verdict-words]')).toHaveText('not comparable, because')
  await expect(bench.locator('[data-compare-why]')).toHaveText(['approach-b changed scripts/bench.sh, which the check runs'])
  await expect(bench.locator('[data-compare-cell="approach-b"] [data-compare-cell-words]')).toHaveText('failed with exit 1')
  await expect(bench.locator('[data-compare-check-argv]')).toHaveText('sh scripts/bench.sh')
  const lint = v.locator('[data-compare-check="verify/lint"]')
  await expect(lint.locator('[data-compare-cell="approach-b"]')).toHaveAttribute('data-cell-state', 'not_run')
  await expect(lint.locator('[data-compare-cell="approach-b"] [data-compare-cell-words]')).toHaveText('not run: an earlier check failed')
  // No verdict is coloured: a not comparable row is words in default ink, with no amber.
  expect(await bench.locator('[data-compare-verdict-words]').evaluate((el) => getComputedStyle(el).color)).toBe(await page.evaluate(() => {
    const p = document.body.appendChild(document.createElement('span'))
    p.className = 'text-default'
    const c = getComputedStyle(p).color
    p.remove()
    return c
  }))

  // A cell opens its receipt in DS2's dialog; a cell that did not run opens nothing.
  await bench.locator('[data-compare-cell="approach-b"]').click()
  const modal = page.locator('[data-receipt-modal]')
  await expect(modal).toHaveAttribute('data-receipt-key', 'check:bench:1')
  await expect(modal.locator('[data-receipt-exit]')).toHaveText('1')
  await expect(modal.locator('[data-receipt-recorded-sha]')).toHaveText('e'.repeat(64))
  await page.keyboard.press('Escape')
  await expect(modal).toHaveCount(0)
  await expect(lint.locator('[data-compare-cell="approach-b"]')).not.toHaveAttribute('type', 'button')

  // The measured rows: size, dependency changes, test and build files, independence with what is not observed.
  const m = v.locator('[data-compare-measured]')
  await expect(m.locator('[data-compare-size="approach-a"] [data-compare-size-lines]')).toHaveText('3 files changed, +40 −2')
  await expect(m.locator('[data-compare-size="approach-a"] [data-compare-size-files]')).toHaveText('1 added')
  await m.locator('[data-compare-size="approach-a"] [data-compare-size-toggle]').click()
  await expect(m.locator('[data-compare-size="approach-a"] [data-compare-file]')).toHaveCount(3)
  await expect(m.locator('[data-compare-deps="approach-a"] [data-compare-deps-summary]')).toHaveText('2 dependency files changed')
  await expect(m.locator('[data-compare-deps="approach-a"] [data-compare-deps-module]')).toHaveText(['added golang.org/x/text v0.3.0 in go.mod'])
  await expect(m.locator('[data-compare-deps="approach-b"] [data-compare-deps-summary]')).toHaveText('No dependency file changed')
  await expect(m.locator('[data-compare-independence="approach-a"] [data-compare-independence-words]')).toHaveText(['no shared commit and no same patch'])
  await expect(m.locator('[data-compare-independence="approach-b"] [data-compare-independence-words]')).toHaveText(['shares 2 commits with approach-a', "1 commit carries the same change as one of approach-a's"])
  await expect(m.locator('[data-compare-not-observed]')).toHaveText(`Same patches: checked. ${NOT_OBSERVED}`)
  await expect(v.locator('[data-compare-measured-by]')).toHaveText('measured by Conductor')

  // Four candidates, one of which changed a test and a build file, and one git could not be read for: read again on a reload.
  shape = { run: run.id, candidates: 4, testChange: true, unavailable: true }
  await view(page, run.id)
  await expect(cols).toHaveCount(4)
  await expect(unit).toHaveAttribute('data-verdict', 'unknown')
  await expect(unit.locator('[data-compare-verdict-words]')).toHaveText('comparable only if their test changes are fair')
  await expect(unit.locator('[data-compare-why]')).toHaveText(['approach-a changed internal/cache/cache_test.go, a test file and 1 more', 'approach-b changed none', 'approach-c changed none', "approach-d's change could not be read"])
  await expect(unit.locator('[data-compare-check-files-of="approach-a"]')).toHaveText('approach-a changed 2 test or build files: internal/cache/cache_test.go, a test file; Makefile, a build file')
  await expect(unit.locator('[data-compare-check-files-of="approach-b"]')).toHaveText('approach-b changed no test or build file')
  await expect(m.locator('[data-compare-verify="approach-a"]')).toContainText('internal/cache/cache_test.go, a test file')
  await expect(v.locator('[data-compare-candidate="approach-d"] [data-compare-candidate-unavailable]')).toHaveText('Git could not be read for it: git diff: exit status 128: fatal: bad object')
  // The rest of the reply stands for it: its cells are drawn.
  await expect(unit.locator('[data-compare-cell="approach-d"]')).toHaveAttribute('data-cell-state', 'passed')

  // Back to the ticket: its sections again.
  await v.locator('[data-compare-back]').click()
  await expect(page.locator('#ticket-description')).toBeVisible()
  await expect(page.locator('[data-compare-view]')).toHaveCount(0)
})

test("the run card and the run header link to the comparison, and the run page groups the run's tasks by lane", async ({ day, browser }) => {
  await day.part('dispatch')
  const run = compareRun({ ticket: TICKET })
  const { page } = await opened(browser, run, () => comparisonOf({ run: run.id }))
  const target = `${TICKET_PATH}?view=compare&run=${run.id}`

  // The ticket's run card: the link in its first line, beside its revision.
  budget(LOAD_MS)
  await page.goto(TICKET_PATH)
  const card = page.locator(`[data-run-card="${run.id}"]`)
  const link = card.locator('[data-run-card-comparison]')
  await expect(link).toHaveText('Comparison')
  await expect(link).toHaveAttribute('href', target)
  await link.click()
  await expect(page.locator('[data-compare-view][data-compare-state="content"]')).toBeVisible()

  // The run page's header: in row two, after the sentence, and in the run's menu, never in row one.
  budget(LOAD_MS)
  await page.goto(`/runs/${run.id}?view=tasks`)
  const header = page.locator('[data-crew-run-header]')
  await expect(header.locator('[data-run-summary] [data-run-comparison]')).toHaveAttribute('href', target)
  await expect(header.locator('[data-run-row-one] [data-run-comparison]')).toHaveCount(0)
  await header.locator('[data-run-menu]').click()
  const item = page.locator('[data-run-comparison-menu]')
  await expect(item).toHaveAttribute('href', target)
  await page.keyboard.press('Escape')
  await expect(item).toHaveCount(0)

  // The tasks by lane: each lane's row, then its candidate, then its review, whatever the crew's order.
  const rows = page.locator('[data-task-rows] > li')
  await expect(page.locator('[data-task-lane]')).toHaveCount(2)
  await expect(page.locator('[data-task-lane="approach-a"] [data-task-lane-approach]')).toHaveText('a candidate of the comparison, reviewed by review-a')
  expect(await rows.evaluateAll((els) => els.map((e) => (e.hasAttribute('data-task-lane') ? `lane ${e.getAttribute('data-task-lane')}` : e.getAttribute('data-task-row'))))).toEqual([
    'lane approach-a',
    'approach-a',
    'review-a',
    'lane approach-b',
    'approach-b',
    'review-b',
  ])
  // The task rows keep the one grid: each row's state column starts where the first one's does.
  const lefts = await page.locator('[data-task-row] [data-task-col="state"]').evaluateAll((els) => els.map((e) => Math.round(e.getBoundingClientRect().left)))
  expect(new Set(lefts).size).toBe(1)
})

test('a run of no comparison has no link and no lanes, and its compare view says so', async ({ day, browser }) => {
  await day.part('dispatch')
  const plain = runInfo({ tasks: 6 })
  const { page } = await opened(browser, plain, () => null)
  budget(LOAD_MS)
  await page.goto(`/runs/${plain.id}?view=tasks`)
  await expect(page.locator('[data-task-row]')).toHaveCount(6)
  await expect(page.locator('[data-task-lane]')).toHaveCount(0)
  await expect(page.locator('[data-run-comparison]')).toHaveCount(0)
  budget(LOAD_MS)
  await page.goto(TICKET_PATH)
  await expect(page.locator(`[data-run-card="${plain.id}"]`)).toBeVisible()
  await expect(page.locator('[data-run-card-comparison]')).toHaveCount(0)
  budget(LOAD_MS)
  await page.goto(`${TICKET_PATH}?view=compare&run=${plain.id}`)
  await expect(page.locator('[data-compare-missing] [data-page-why]')).toHaveText(`this run is not a comparison: ${plain.id}`)
})

test('a first read that fails says the server’s words, and Read again shows the comparison', async ({ day, browser }) => {
  await day.part('dispatch')
  const run = compareRun({ ticket: TICKET })
  let fail = true
  const page = await dayPage(browser, { path: '/agents' })
  await serveRun(page, run)
  await serveTicketRuns(page, TICKET, run)
  await page.route(
    (u) => u.pathname === `/api/runs/${run.id}/comparison`,
    (route) =>
      fail
        ? route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: { code: 'internal', message: 'the ledger could not be read' } }) })
        : route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(comparisonOf({ run: run.id })) }),
  )
  budget(LOAD_MS)
  await page.goto(`${TICKET_PATH}?view=compare&run=${run.id}`)
  await expect(page.locator('[data-compare-failed] [data-page-why]')).toHaveText('the ledger could not be read')
  fail = false
  await readAgain(page.locator('[data-compare-failed] [data-page-again]'), page.locator('[data-compare-view][data-compare-state="content"]'))
  await expect(page.locator('[data-compare-candidate]')).toHaveCount(2)
})

for (const size of [
  { width: 1440, height: 900 },
  { width: 390, height: 844, touch: true },
]) {
  test(`the compare view passes the contrast walk in both themes at ${size.width}, with nothing sideways and no filled button`, async ({ day, browser }) => {
    await day.part('dispatch')
    const run = compareRun({ ticket: TICKET })
    const { page } = await opened(browser, run, () => comparisonOf({ run: run.id, candidates: 4, failed: true, testChange: true, unavailable: true }), size)
    await view(page, run.id)
    for (const theme of ['dark', 'light'] as const) {
      budget(LOAD_MS)
      await useTheme(page, theme, size.width < 1024 ? '[data-bottom-bar]' : '#dashboard-sidebar-main')
      const v = page.locator('[data-compare-view][data-compare-state="content"]')
      await expect(v).toBeVisible()
      await v.locator('[data-compare-size="approach-a"] [data-compare-size-toggle]').click()
      await expect(v.locator('[data-compare-size="approach-a"] [data-compare-file]')).toHaveCount(3)
      const { judged, failures } = await walk(page, v)
      expect(judged, `${theme}: the walk judged some text`).toBeGreaterThan(0)
      expect(failures, `${theme}, ${size.width}: text under its floor (the ten worst):\n${worst(failures)}`).toEqual([])
      await noSideways(page, { root: v })
      expect(await filledPrimaries(v), 'the compare view has no main action until the selection').toEqual([])
    }
  })
}
