import { writeFileSync, mkdirSync } from 'node:fs'
import { join } from 'node:path'
import type { Page } from '@playwright/test'
import { budget, dayPage, expect, readAgain, test, useTheme } from './fixtures'
import { makeProject, makeTicket, opOf, storedTicket } from './dispatch-helpers'
import { ownServer } from './own-server'
import { git } from './server'
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
  // Nothing git could not read of it is said as a fact: each measured row says it was not measured, in the server's words.
  const gitWords = 'Not measured: git diff: exit status 128: fatal: bad object.'
  for (const row of ['size', 'deps', 'verify', 'independence']) await expect(m.locator(`[data-compare-${row}="approach-d"] [data-compare-not-measured]`), row).toHaveText(gitWords)
  await expect(m.locator('[data-compare-independence="approach-d"] [data-compare-independence-words]')).toHaveCount(0)
  await expect(m.locator('[data-compare-independence="approach-a"] [data-compare-not-compared]')).toHaveText('not compared with approach-d: not measured')
  await expect(unit.locator('[data-compare-check-files-of="approach-d"]')).toHaveText(`approach-d: ${gitWords}`)

  // A lane with no result yet has a cell in each row, under its column, saying so; a lane still worked with its last submitted
  // commit shown says it is not a result yet, never failed.
  shape = { run: run.id, candidates: 4, running: true, checking: true }
  await view(page, run.id)
  await expect(unit.locator('[data-compare-cell]')).toHaveCount(4)
  expect(await unit.locator('[data-compare-cell]').evaluateAll((els) => els.map((e) => e.getAttribute('data-compare-cell')))).toEqual(['approach-a', 'approach-b', 'approach-c', 'approach-d'])
  await expect(unit.locator('[data-compare-cell="approach-a"]')).toHaveAttribute('data-cell-state', 'none')
  await expect(unit.locator('[data-compare-cell="approach-a"] [data-compare-cell-words]')).toHaveText('no result to check')
  await expect(m.locator('[data-compare-size="approach-a"] [data-compare-not-measured]')).toHaveText('Not measured: it has no commit.')
  const c = v.locator('[data-compare-candidate="approach-c"]')
  await expect(c).toHaveAttribute('data-state', 'running')
  await expect(c.locator('[data-compare-candidate-commit]')).toContainText('not a result yet: its checks or a further attempt are under way')
  await expect(c.locator('[data-compare-candidate-commit]')).not.toContainText('failed')

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

/** Every key of a value, as `path: kind` (an array's elements under `[]`), as stubs.spec.ts reads the run and inbox stubs. */
function paths(v: unknown, at = '', out = new Set<string>()): Set<string> {
  const kind = v === null ? 'null' : Array.isArray(v) ? 'array' : typeof v
  if (at) out.add(`${at}: ${kind}`)
  if (Array.isArray(v)) for (const x of v) paths(x, `${at}[]`, out)
  else if (v && typeof v === 'object') for (const [k, x] of Object.entries(v)) paths(x, at ? `${at}.${k}` : k, out)
  return out
}
/** Whether a path is under one of `drop`: what the real comparison has no case of (a lane git could not read), or the stub none. */
const under = (p: string, drop: readonly string[]) => drop.some((d) => p.startsWith(`${d}: `) || p.startsWith(`${d}.`) || p.startsWith(`${d}[]`))
function keyDiff(stub: unknown, real: unknown, drop: readonly string[]) {
  const s = paths(stub)
  const r = paths(real)
  return { missing: [...r].filter((p) => !s.has(p) && !under(p, drop)).sort(), extra: [...s].filter((p) => !r.has(p) && !under(p, drop)).sort() }
}

/** A stub candidate's run and every check of it, on a loaded machine: the engine's own caps (an attempt, its checks, its review). */
const SETTLE_MS = 240_000

test('a comparison of two stub candidates on the real server: the view shows what the server measured, and the stub has its keys', async ({ browser }) => {
  budget(SETTLE_MS + 4 * LOAD_MS + 60_000)
  // One lane's agent edits the check's script (STUB_COMPARE_EDIT): that check's row is not comparable, in the server's words.
  const s = await ownServer('compare', { agents: [{ id: 'cand-edit', from: 'worker-cli', env: { STUB_COMPARE_EDIT: 'script' } }] })
  try {
    mkdirSync(join(s.repo, 'scripts'), { recursive: true })
    writeFileSync(join(s.repo, 'scripts', 'check.sh'), '#!/bin/sh\nexit 0\n')
    git(s.repo, 'add', 'scripts/check.sh')
    git(s.repo, 'commit', '-q', '-m', 'the check')
    const base = git(s.repo, 'rev-parse', 'HEAD').trim()
    const { crew } = await s.api.ok<{ crew: { id: string } }>('POST', '/api/crews/templates/compare', {
      name: 'Compare the check',
      cwd: s.repo,
      goal: 'Make the check pass',
      instructions: 'Keep the API as it is.',
      criteria: [{ id: 'no-dep', text: 'no new dependency' }],
      checks: [{ id: 'unit', argv: ['sh', 'scripts/check.sh'] }],
      candidates: [{ agentId: 'worker-cli', approach: 'Change nothing of the check' }, { agentId: 'cand-edit' }],
      reviews: true,
      reviewAgent: 'worker-cli',
    })
    // Proposed as a ticket's revision and approved (DS2's routes), then launched with the count the revision states.
    const project = 'CMPW'
    await makeProject(s.api, project)
    const ticket = await makeTicket(s.api, project, '1', { title: 'Compare approaches' })
    const { plan } = await s.api.ok<{ plan: { id: string; rev: number } }>('POST', `/api/dispatch/tickets/${ticket}/plans`, { opId: opOf(project, 'r1'), crew: crew.id })
    await s.api.ok('POST', `/api/dispatch/plans/${plan.id}/approve`, { opId: opOf(project, 'ap1'), expectedRev: plan.rev, ticketRev: (await storedTicket(s.api, ticket)).rev })
    const launched = await s.api.ok<{ launch: { runId: string } }>('POST', `/api/dispatch/tickets/${ticket}/launch`, { opId: opOf(project, 'l1'), plan: plan.id, candidates: 2 })
    const run = launched.launch.runId
    expect(run).toBeTruthy()
    // Every lane ended: the ledger's word (settled), not a page's.
    // Its selection open too: the selection is read just after the measures, so it can come one change later than `settled`.
    await expect
      .poll(async () => (await s.api.ok<{ settled: boolean; selection?: { state: string } }>('GET', `/api/runs/${run}/comparison`)).selection?.state ?? '', { timeout: SETTLE_MS, intervals: [1000] })
      .toBe('open')
    const real = await s.api.ok<Record<string, unknown>>('GET', `/api/runs/${run}/comparison`)

    // The stub's keys are the server's: what the real comparison has no case of here is left out by name, never by its shape.
    const stub = comparisonOf({ run, brakemanRequest: undefined, supporting: true, selection: 'open' })
    expect(
      keyDiff(stub, real, [
        // No lane git could not read, no diff past its bound, no test or build file changed.
        'candidates[].unavailable',
        'candidates[].size.notMeasured',
        'candidates[].verifyFiles',
        // The stub's second review asks for changes with a finding; these reviews' findings may be none.
        'supporting.reviews[].findings[]',
        'supporting.reviews[].favours',
        // The stub's lanes share commits and same patches, a check not run, a failed cell; the real ones none of these.
        'candidates[].shared[]',
        'candidates[].equivalent[]',
        'checks[].cells[].notRun',
        'checks[].cells[].exit',
        // No candidate here names an argument or changes a dependency file.
        'candidates[].args[]',
        'candidates[].deps',
      ]),
      "the comparison's keys the server sends that the stub lacks (missing), and the stub's that it never sends (extra)",
    ).toEqual({ missing: [], extra: [] })
    expect((real.independence as { notObserved: string }).notObserved, "the stub carries the server's words").toBe(NOT_OBSERVED)
    expect((real.supporting as { brakeman: unknown }).brakeman, "the stub's words for a draft not asked").toEqual({ why: 'Brakeman is not set up' })
    // The run's compare part, as compareRun gives it.
    const runRead = await s.api.ok<{ run: Record<string, unknown> }>('GET', `/api/runs/${run}`)
    expect(runRead.run.compare).toEqual({ candidates: [{ task: 'approach-a', review: 'review-a' }, { task: 'approach-b', review: 'review-b' }] })
    expect(keyDiff(compareRun({ ticket }).compare, runRead.run.compare, [])).toEqual({ missing: [], extra: [] })

    const page = await dayPage(browser, { server: s.started, path: '/agents' })
    try {
      budget(LOAD_MS)
      await page.goto(`/dispatch/${project}/1?view=compare&run=${run}`)
      const v = page.locator('[data-compare-view][data-compare-state="content"]')
      await expect(v).toBeVisible()
      await expect(v).toHaveAttribute('data-compare-settled', 'true')
      await expect(v.locator('[data-compare-base]')).toHaveAttribute('title', base)
      await expect(v.locator('[data-compare-objective]')).toHaveText('Make the check pass')
      await expect(v.locator('[data-compare-criterion]')).toHaveText(['no new dependency'])
      for (const lane of ['approach-a', 'approach-b']) {
        await expect(v.locator(`[data-compare-candidate="${lane}"]`)).toHaveAttribute('data-state', 'ready')
        await expect(v.locator(`[data-compare-candidate="${lane}"]`)).toHaveAttribute('data-selectable', 'true')
      }
      await expect(v.locator('[data-compare-candidate="approach-a"] [data-compare-candidate-approach]')).toHaveText('Change nothing of the check')
      // The check approach-b's agent edited: not comparable, the server's words naming the script.
      const unit = v.locator('[data-compare-check="check/unit"]')
      await expect(unit).toHaveAttribute('data-verdict', 'not_comparable')
      await expect(unit.locator('[data-compare-why]')).toContainText(['approach-b changed scripts/check.sh, which the check runs'])
      for (const lane of ['approach-a', 'approach-b']) await expect(unit.locator(`[data-compare-cell="${lane}"]`)).toHaveAttribute('data-cell-state', 'passed')
      // The size rows say the server's counts; approach-b's change is approach-a's and the script.
      const sizes = (real.candidates as Array<{ task: string; size: { files: number; added: number; removed: number; list: Array<{ path: string }> } }>).map((c) => c.size)
      for (const [i, lane] of (['approach-a', 'approach-b'] as const).entries()) {
        const z = sizes[i]!
        await expect(v.locator(`[data-compare-size="${lane}"] [data-compare-size-lines]`)).toHaveText(`${z.files} ${z.files === 1 ? 'file' : 'files'} changed, +${z.added} −${z.removed}`)
      }
      expect(sizes[1]!.files).toBe(sizes[0]!.files + 1)
      expect(sizes[1]!.list.map((f) => f.path)).toContain('scripts/check.sh')
      expect(sizes[0]!.list.map((f) => f.path)).not.toContain('scripts/check.sh')
      await expect(v.locator('[data-compare-deps="approach-a"] [data-compare-deps-summary]')).toHaveText('No dependency file changed')
      await expect(v.locator('[data-compare-independence="approach-b"] [data-compare-independence-words]')).toHaveText(['no shared commit and no same patch'])

      // The run page: its lanes, each candidate then its review.
      budget(LOAD_MS)
      await page.goto(`/runs/${run}?view=tasks`)
      await expect(page.locator('[data-task-lane]')).toHaveCount(2)
      expect(await page.locator('[data-task-rows] > li').evaluateAll((els) => els.map((e) => e.getAttribute('data-task-lane') ?? e.getAttribute('data-task-row')))).toEqual(['approach-a', 'approach-a', 'review-a', 'approach-b', 'approach-b', 'review-b'])
      await expect(page.locator('[data-run-summary] [data-run-comparison]')).toHaveAttribute('href', `/dispatch/${project}/1?view=compare&run=${run}`)

      // The crews page offers no Launch of the comparison's crew, and says where one is launched (the server's own refusal is compare-launch.spec.ts's).
      budget(LOAD_MS)
      await page.goto(`/crews/${crew.id}`)
      await expect(page.locator('[data-launch]')).toBeDisabled({ timeout: LOAD_MS })
      await page.locator('[data-launch]').hover({ force: true })
      await expect(page.getByText("An agent trial starts from a ticket's approved plan").first()).toBeVisible({ timeout: 15_000 })

      // Answered and applied: the selection's keys once applied are the stub's too.
      const open = real.selection as { decision: string }
      const rev = (await s.api.ok<{ decision: { rev: number } }>('GET', `/api/decisions/${open.decision}`)).decision.rev
      await s.api.ok('POST', `/api/decisions/${open.decision}/respond`, { responseId: 'cmpw-select-a', choice: 0, rationale: 'the stub keys', expectedRev: rev })
      budget(SETTLE_MS)
      await expect
        .poll(async () => (await s.api.ok<{ selection?: { state: string } }>('GET', `/api/runs/${run}/comparison`)).selection?.state ?? '', { timeout: SETTLE_MS, intervals: [1000] })
        .toBe('applied')
      const applied = await s.api.ok<Record<string, unknown>>('GET', `/api/runs/${run}/comparison`)
      expect(keyDiff((comparisonOf({ run, supporting: true, selection: 'applied' }) as { selection: unknown }).selection, applied.selection, []), "the applied selection's keys").toEqual({ missing: [], extra: [] })
      expect(applied.state).toBe('selected')
    } finally {
      await page.context().close()
    }
  } finally {
    await s.stop()
  }
})
