import { mkdirSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import type { Locator, Page } from '@playwright/test'
import { expect, git, stopStrays, task, test, type Api, type Run } from './fixtures'
import { scratchRepo } from './server'
import { freeProjectKey, makeProject, makeTicket, opOf } from './dispatch-helpers'

// The whole Review view (Program item RV1, docs/plans/review-repair.md, PR-7), each test with a ticket and a run of its own, launched
// from an approved plan revision of a stub worker crew in a repository of the spec's own. The `worker-review` agent is the CLI worker
// under STUB_REVIEW_EDITS=1: its first attempt writes stub-review.txt, eight numbered lines; an attempt that takes up a person's request
// inserts two lines at its top and rewrites the first line of stub-builder.txt, and names each change asked for as addressed. A `hold`
// task by a member that never submits keeps a run the server's, so a request is applied to it, or Check again taken on it, as it is.
//
// 1. Findings across candidates: a review of build's first candidate with notes on stub-review.txt 3–4, stub-builder.txt 1 and a
//    question; on the repair's candidate the first is "moved: now 5–6", the second "the lines changed", both with the agent's
//    addressed notes; one marked resolved, one still open, the question withdrawn; a second round sent from the page and applied; the
//    lineage; the run page's Review entry; the phone width (one column, a note by typed lines, a mark).
// 2. A check that fails until a counter the spec owns, outside the repository, reaches 3: build fails at attempt 3; Prepare a repair
//    shows the receipt, its class (`code`), the log's hash and the criteria; Check again, offered because the prepared request says
//    Yardmaster takes it, pressed: build succeeds at the same commit as attempt 4 with its own passing receipt, attempt 3's failing one
//    unchanged; Resolved refused for a finding of attempt 3 on attempt 4, the same commit; a request for changes to attempt 3, refused
//    since the task moved on, and Request again, its next try.
// 3. The same failure on a run stopped and continued (Continue on the run page): Prepare a repair from the first run's receipt offers
//    Check again for the continuation, and pressed, the candidate is checked again in the continuation's run, the first run's receipts
//    unchanged.
//
// Each test ends its strays (stopStrays), finds its ticket and its runs by their key and id, waits on what the ledger, the store or the
// page must reach, and raises its timeout by its own waits' caps (AGENTS.md, Tests that do not flake).
test.describe.configure({ mode: 'serial' })

const cleanup: Array<() => Promise<void> | void> = []
const runs: string[] = []

test.afterAll(async ({ api }) => {
  for (const id of runs) await api.stopRun(id)
  for (const f of cleanup.reverse()) await f()
})

interface Receipt {
  phase: string
  id: string
  k: number
  state: string
  exit?: number
  logSha256?: string
}
interface Candidate {
  attempt: number
  commit?: string
  start?: string
  state: string
  standing: boolean
  receipts: Receipt[]
}
interface Finding {
  id: string
  message: string
  state: string
  candidate: string
  locatedOn?: string
  located?: { state: string; from?: number; to?: number }
  addressed?: Array<{ run: string; attempt: number; note?: string }>
}

/** Raises the running test's timeout by the cap of a wait as the step is about to make it (AGENTS.md, Tests that do not flake). */
function budget(ms: number) {
  const info = test.info()
  info.setTimeout(info.timeout + ms)
}

/** `expect` with its bound, the test's timeout raised by it first. */
function within(ms: number) {
  budget(ms)
  return { timeout: ms }
}

/** A task's candidates as the candidates route answers them, from the ledger. */
async function candidatesOf(api: Api, run: string, taskId: string): Promise<Candidate[]> {
  const r = await api.ok<{ candidates: Array<{ task: string; candidates: Candidate[] }> }>('GET', `/api/runs/${encodeURIComponent(run)}/candidates`)
  return r.candidates.find((t) => t.task === taskId)?.candidates ?? []
}

/** A candidate of the task, read from the ledger until `done` holds of it. */
async function candidateUntil(api: Api, run: string, taskId: string, attempt: number, done: (c: Candidate) => boolean, message: string, timeout = 180_000): Promise<Candidate> {
  budget(timeout)
  let last: Candidate | undefined
  await expect
    .poll(
      async () => {
        last = (await candidatesOf(api, run, taskId)).find((c) => c.attempt === attempt)
        return !!last && done(last)
      },
      { timeout, intervals: [500], message },
    )
    .toBe(true)
  return last!
}

const passed = (c: Candidate) => c.standing && !!c.commit && c.receipts.some((r) => r.phase === 'check' && r.state === 'finished' && r.exit === 0)
const failedCheck = (c: Candidate) => c.state === 'failed' && !!c.commit && c.receipts.some((r) => r.phase === 'check' && r.state === 'finished' && r.exit !== 0)

/** The ticket's findings, every state, located on `candidate`. */
async function findingsOf(api: Api, ticketKey: string, candidate: string): Promise<Finding[]> {
  const r = await api.ok<{ findings: Finding[] }>('GET', `/api/dispatch/tickets/${ticketKey}/findings?state=all&candidate=${encodeURIComponent(candidate)}`)
  return r.findings
}

/**
 * A repository of the test's own under the e2e's allowed root, a crew of a `worker-review` builder doing `build` with the check given
 * and a `hold` that never submits, a project and a ticket, revision 1 proposed, approved and launched: the ticket's key and the run.
 */
async function launched(api: Api, root: string, name: string, check: string[]): Promise<{ repo: string; key: string; ticketKey: string; run: string; runBase: string }> {
  const repo = join(root, `${name}-${Date.now().toString(36)}`)
  mkdirSync(repo)
  scratchRepo(repo)
  const runBase = git(repo, 'rev-parse', 'HEAD').trim()
  const crewId = (
    await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
      name: `e2e ${name} ${Date.now().toString(36)}`,
      goal: 'review and repair a candidate',
      cwd: repo,
      where: 'server',
      isolation: 'worktree',
      openAfterLaunch: false,
      yolo: null,
      members: [
        { name: 'builder', agentId: 'worker-review', prompt: '', start: { when: 'task' } },
        // Not a worker: it reads its task line as any line and never submits, so the run stays running, the server's.
        { name: 'asker', agentId: 'claude', prompt: '', start: { when: 'task' } },
      ],
      tasks: [
        { id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.', checks: [{ id: 'ok', argv: check }] },
        { id: 'hold', member: 'asker', kind: 'work', instructions: 'Hold the line.' },
      ],
    })
  ).crew.id
  cleanup.push(async () => {
    await api.call('DELETE', `/api/crews/${encodeURIComponent(crewId)}`)
    rmSync(repo, { recursive: true, force: true })
  })
  const key = await freeProjectKey(api, 'RR')
  await makeProject(api, key)
  const ticketKey = await makeTicket(api, key, '1', { title: 'Build the thing', description: 'It needs building.', acceptance: 'It is built, and its lines are numbered.' })
  const proposed = await api.ok<{ plan: { id: string } }>('POST', `/api/dispatch/tickets/${ticketKey}/plans`, { opId: opOf(key, 'r1'), crew: crewId })
  await api.ok('POST', `/api/dispatch/plans/${proposed.plan.id}/approve`, { opId: opOf(key, 'ap1'), note: 'Go.' })
  const l = await api.ok<{ launch: { runId: string; state: string } }>('POST', `/api/dispatch/tickets/${ticketKey}/launch`, { opId: opOf(key, 'l1'), plan: proposed.plan.id })
  expect(l.launch.state).toBe('started')
  runs.push(l.launch.runId)
  return { repo, key, ticketKey, run: l.launch.runId, runBase }
}

/** A finding of the Review view by its words. */
function findingRow(page: Page, words: string): Locator {
  return page.locator('[data-review-finding]').filter({ has: page.locator('[data-finding-message]', { hasText: words }) })
}

/** Marks a finding on the page against the candidate shown, with a note, and waits for its state. */
async function markOnPage(row: Locator, verdict: 'resolved' | 'open' | 'withdrawn', note: string) {
  await row.locator(`[data-finding-mark="${verdict}"]`).click()
  const form = row.locator('[data-finding-mark-form]')
  await expect(form).toBeVisible(within(15_000))
  if (note) {
    await form.locator('textarea').fill(note, within(15_000))
    await expect(form.locator('textarea')).toHaveValue(note)
  }
  await form.locator('[data-finding-mark-confirm]').click()
  await expect(row).toHaveAttribute('data-finding-state', verdict, within(30_000))
}

/** Fills the note's form open on the page and adds it to the draft. */
async function addNote(form: Locator, words: string) {
  await expect(form).toBeVisible(within(15_000))
  await form.locator('textarea').fill(words, within(15_000))
  await expect(form.locator('textarea')).toHaveValue(words)
  await form.locator('[data-review-note-save]').click()
  await expect(form).toHaveCount(0, within(15_000))
}

/** The check that fails until the counter file `counter`, outside the repository, has been bumped three times, then passes. */
const countingCheck = (counter: string) => ['sh', '-c', 'n=$(cat "$1" 2>/dev/null || echo 0); echo $((n+1)) >"$1"; test "$n" -ge 3', 'check', counter]

test("findings across candidates: located on the repair, the agent's addressed notes, marked, a question withdrawn, a second round, the lineage, the run page's Review and the phone", async ({ page, api, state }) => {
  test.slow()
  await stopStrays(api, [])
  const { key, ticketKey, run, runBase } = await launched(api, state.root, 'findings', ['sh', '-c', 'test -s stub-review.txt'])

  // build's first candidate, its check passed; a review of it sent through the route the page sends with (review.spec.ts sends one from
  // the page): a change on stub-review.txt 3–4, a change on stub-builder.txt's first line, a question on the whole of stub-review.txt.
  const first = await candidateUntil(api, run, 'build', 1, passed, "build's first candidate stands with its check passed")
  const cand1 = `${run}/build/1@${first.commit}`
  const sent = await api.ok<{ review: { id: string; application: { state: string; attempt?: number } } }>('POST', `/api/dispatch/tickets/${ticketKey}/reviews`, {
    opId: opOf(key, 'rv1'),
    candidate: cand1,
    base: runBase,
    verdict: 'request_changes',
    summary: 'Three things.',
    via: 'web',
    findings: [
      { kind: 'line', intent: 'change', severity: 'major', path: 'stub-review.txt', side: 'candidate', from: 3, to: 4, lines: ['review line 3', 'review line 4'], message: 'Number these from zero.' },
      { kind: 'line', intent: 'change', severity: 'minor', path: 'stub-builder.txt', side: 'candidate', from: 1, to: 1, lines: ['attempt 1 of build by builder'], message: 'Say more on the first line.' },
      { kind: 'file', intent: 'question', path: 'stub-review.txt', side: 'candidate', message: 'Why eight lines?' },
    ],
  })
  expect(sent.review.application).toMatchObject({ state: 'applied', attempt: 2 })

  // The repair's candidate: build's attempt 2 at the reviewed commit, its own check passed.
  const second = await candidateUntil(api, run, 'build', 2, passed, "the repair's candidate stands with its own check passed")
  expect(second.start).toBe(first.commit)
  const cand2 = `${run}/build/2@${second.commit}`

  // The Review view at it: the findings located on it, the agent's addressed notes beside the two changes, the question's beside none.
  await page.goto(`/dispatch/${key}/1?view=review&candidate=${encodeURIComponent(cand2)}`)
  const view = page.locator(`[data-review-view][data-review-candidate="${cand2}"]`)
  await expect(view).toBeVisible(within(60_000))
  await expect(page.locator('[data-review-findings]')).toHaveAttribute('data-review-findings-count', '3', within(30_000))
  const moved = findingRow(page, 'Number these from zero.')
  const changed = findingRow(page, 'Say more on the first line.')
  const question = findingRow(page, 'Why eight lines?')
  await expect(moved).toHaveAttribute('data-finding-located', 'moved')
  await expect(moved.locator('[data-finding-located-words]')).toHaveText('On this candidate: moved: now 5–6')
  await expect(changed).toHaveAttribute('data-finding-located', 'changed')
  await expect(changed.locator('[data-finding-located-words]')).toHaveText('On this candidate: the lines changed')
  await expect(moved.locator('[data-finding-addressed]')).toContainText('The agent says (attempt 2): done in attempt 2')
  await expect(changed.locator('[data-finding-addressed]')).toContainText('The agent says (attempt 2): done in attempt 2')
  await expect(question.locator('[data-finding-addressed]')).toHaveCount(0)
  // Every one is still open: an addressed note and a located state are never a resolution.
  for (const f of await findingsOf(api, ticketKey, cand2)) expect(f.state).toBe('open')

  // The candidate's lineage: attempt 2 the repair of the review, attempt 1 build's first.
  await expect(page.locator('[data-review-lineage-step][data-lineage-attempt="2"]')).toHaveAttribute('data-lineage-cause', 'review')
  await expect(page.locator('[data-review-lineage-step][data-lineage-attempt="2"] [data-lineage-words]')).toContainText(`the repair of review ${sent.review.id.slice(0, 7)} by`)
  await expect(page.locator('[data-review-lineage-step][data-lineage-attempt="1"]')).toHaveAttribute('data-lineage-cause', 'first')
  // The requirements as launched and the plan revision that launched the run; the task's expected check.
  await expect(page.locator('[data-review-criteria-launched]')).toHaveText('It is built, and its lines are numbered.')
  await expect(page.locator('[data-review-plan]')).toHaveAttribute('data-review-plan-n', '1')
  await expect(page.locator('[data-review-expected-checks]')).toContainText('ok')

  // Marked against the repair's candidate: one resolved, one still open, the question withdrawn; as the store keeps them.
  await markOnPage(moved, 'resolved', 'Renumbered.')
  await markOnPage(changed, 'open', 'Not yet.')
  await markOnPage(question, 'withdrawn', '')
  await expect(page.locator('[data-review-findings]')).toHaveAttribute('data-review-findings-open', '1')
  const kept = await findingsOf(api, ticketKey, cand2)
  expect(Object.fromEntries(kept.map((f) => [f.message, f.state]))).toEqual({ 'Number these from zero.': 'resolved', 'Say more on the first line.': 'open', 'Why eight lines?': 'withdrawn' })

  // A second round from the page: a general note sent as Request changes, after its confirmation, applied as build's attempt 3.
  await page.locator('[data-review-general]').click()
  await addNote(page.locator('[data-review-note-form][data-review-note-kind="general"]'), 'Say more on the first line, as asked.')
  await page.locator('[data-review-send]').click()
  const confirm = page.locator('[data-review-confirm-send]')
  await expect(confirm).toBeVisible(within(15_000))
  await confirm.click()
  const round2 = page.locator('[data-review-sent]')
  await expect(round2).toHaveAttribute('data-review-application', 'applied', within(60_000))
  await expect(round2.locator('[data-review-applied]')).toHaveAttribute('data-review-attempt', '3')
  const third = await candidateUntil(api, run, 'build', 3, passed, "the second round's candidate stands with its check passed")
  expect(third.start).toBe(second.commit)
  const cand3 = `${run}/build/3@${third.commit}`

  // The ticket's candidates list the person's repairs beside the run's.
  await page.locator('[data-review-back]').click()
  const entry = page.locator(`[data-run-entry][data-ticket-run="${run}"]`)
  await expect(entry.locator('[data-repair-person]')).toHaveCount(2, within(60_000))
  await expect(entry.locator(`[data-repair-person][data-repair-review="${sent.review.id}"]`)).toHaveAttribute('data-repair-person-attempt', '2')

  // The run page's Review entry: build's candidate on the ticket's Review view.
  await page.goto(`/runs/${run}?view=tasks&task=build`)
  const review = page.locator('[data-task-review]')
  await expect(review).toBeVisible(within(60_000))
  await review.click()
  await expect(page.locator(`[data-review-view][data-review-candidate="${cand3}"]`)).toBeVisible(within(60_000))

  // The phone width: one column, a note made by typing its lines, a mark.
  await page.setViewportSize({ width: 390, height: 844 })
  await page.reload()
  await expect(page.locator(`[data-review-view][data-review-candidate="${cand3}"]`)).toBeVisible(within(60_000))
  await expect(page.locator('[data-review-findings]')).toHaveAttribute('data-review-findings-count', '4', within(30_000))
  const files = await page.locator('[data-review-files]').boundingBox()
  const draft = await page.locator('[data-review-draft]').boundingBox()
  expect(files && draft && draft.y >= files.y + files.height).toBe(true)
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true)
  await page.locator('[data-review-file-path="stub-review.txt"]').click()
  await expect(page.locator('[data-review-diff]')).toHaveAttribute('data-review-diff-path', 'stub-review.txt', within(15_000))
  await expect(page.locator('[data-review-note-lines]')).toBeEnabled(within(30_000))
  await page.locator('[data-review-note-lines]').click()
  const typed = page.locator('[data-review-typed]')
  // The form's two number fields: from, then to.
  const lines = typed.locator('input[type="number"]')
  await expect(lines).toHaveCount(2, within(15_000))
  await lines.nth(0).fill('5', within(15_000))
  await lines.nth(1).fill('6', within(15_000))
  await expect(lines.nth(0)).toHaveValue('5')
  await expect(lines.nth(1)).toHaveValue('6')
  await typed.locator('[data-review-typed-add]').click()
  await addNote(page.locator('[data-review-note-form][data-review-note-kind="line"]'), 'These two moved twice now.')
  await expect(page.locator('[data-review-draft]')).toHaveAttribute('data-review-draft-notes', '1')
  await expect(page.locator('[data-review-draft-note]')).toHaveAttribute('data-review-note-path', 'stub-review.txt')
  await markOnPage(findingRow(page, 'Say more on the first line.'), 'resolved', 'Done on attempt 3.')
  const last = await findingsOf(api, ticketKey, cand3)
  expect(last.find((f) => f.message === 'Say more on the first line.')?.state).toBe('resolved')
})

test("a failed check prepared for its repair, checked again at the same commit, Resolved refused on it, and a request refused since the task moved on, asked again", async ({ page, api, state }) => {
  test.slow()
  await stopStrays(api, [])
  const counter = join(state.root, `review-repair-counter-${Date.now().toString(36)}`)
  cleanup.push(() => rmSync(counter, { force: true }))
  const { key, ticketKey, run, runBase } = await launched(api, state.root, 'recheck', countingCheck(counter))

  // build fails: its check fails on each of its three attempts.
  const failed = await candidateUntil(api, run, 'build', 3, failedCheck, "build's attempt 3 failed its check")
  budget(60_000)
  await expect.poll(async () => task((await api.run(run)) as Run, 'build').state, { timeout: 60_000, intervals: [500], message: 'build is failed' }).toBe('failed')
  const c3 = failed.commit!
  const cand3 = `${run}/build/3@${c3}`
  const bad = failed.receipts.find((r) => r.phase === 'check' && r.exit !== 0)!

  // A comment on attempt 3's first line, recorded and applied to nothing: a finding of attempt 3.
  const comment = await api.ok<{ review: { id: string; application: { state: string } } }>('POST', `/api/dispatch/tickets/${ticketKey}/reviews`, {
    opId: opOf(key, 'c1'),
    candidate: cand3,
    base: runBase,
    verdict: 'comment',
    via: 'web',
    findings: [{ kind: 'line', intent: 'note', path: 'stub-builder.txt', side: 'candidate', from: 1, to: 1, lines: ['attempt 1 of build by builder'], message: 'The first line, as attempt 3 had it.' }],
  })
  expect(comment.review.application.state).toBe('none')

  // Prepare a repair from the failed receipt: the receipt, the tested commit, its class, the log's hash, the attempt before, the criteria.
  await page.goto(`/dispatch/${key}/1?view=review&candidate=${encodeURIComponent(cand3)}`)
  await expect(page.locator(`[data-review-view][data-review-candidate="${cand3}"]`)).toBeVisible(within(60_000))
  const prepare = page.locator(`[data-review-prepare="${bad.id}/${bad.k}"]`)
  await expect(prepare).toBeVisible(within(30_000))
  await prepare.click()
  const panel = page.locator('[data-repair-panel]')
  await expect(panel).toBeVisible(within(30_000))
  await expect(panel).toHaveAttribute('data-repair-receipt', `${run}/build/3/check/ok/${bad.k}`)
  await expect(panel).toHaveAttribute('data-repair-class', 'code')
  await expect(panel.locator('[data-repair-subject]')).toHaveText(c3)
  await expect(panel.locator('[data-repair-log-sha]')).toContainText(bad.logSha256!)
  await expect(panel.locator('[data-repair-previous]')).toContainText('attempt 2 failed')
  await expect(panel.locator('[data-repair-acceptance]')).toHaveText('It is built, and its lines are numbered.')
  // Check again is offered because the prepared request says Yardmaster takes it: after Add to the draft for a code defect, first for a
  // possibly flaky check; Retry beside them, the task having failed.
  await expect(panel).toHaveAttribute('data-repair-recheck-allowed', 'true')
  const actions = page.locator('[data-repair-actions]')
  await expect(actions).toHaveAttribute('data-repair-actions-order', 'request,recheck,retry')
  await panel.locator('[data-repair-chosen]').click()
  await page.getByRole('option', { name: 'possibly flaky' }).click()
  await expect(page.getByRole('listbox')).toHaveCount(0, within(15_000))
  await expect(actions).toHaveAttribute('data-repair-actions-order', 'recheck,request,retry')

  // Check again: build succeeds at the same commit as attempt 4, a candidate of its own with its own passing receipt; attempt 3's
  // failing receipt is unchanged.
  await page.locator('[data-repair-recheck]').click()
  await expect(panel.locator('[data-repair-rechecked]')).toHaveText('Checked again as attempt 4, the same commit as attempt 3.', within(60_000))
  const fourth = await candidateUntil(api, run, 'build', 4, passed, 'the check again stands with its own passing receipt')
  expect(fourth.commit).toBe(c3)
  const thirdNow = (await candidatesOf(api, run, 'build')).find((c) => c.attempt === 3)!
  expect(thirdNow.receipts.map((r) => [r.phase, r.id, r.k, r.state, r.exit])).toEqual(failed.receipts.map((r) => [r.phase, r.id, r.k, r.state, r.exit]))
  expect(task((await api.run(run)) as Run, 'build').state).toBe('succeeded')
  const cand4 = `${run}/build/4@${c3}`

  // Resolved refused for attempt 3's finding on attempt 4, the same commit: the page says why and sends nothing; the server refuses it.
  await page.keyboard.press('Escape')
  await page.goto(`/dispatch/${key}/1?view=review&candidate=${encodeURIComponent(cand4)}`)
  await expect(page.locator(`[data-review-view][data-review-candidate="${cand4}"]`)).toBeVisible(within(60_000))
  await expect(page.locator('[data-review-lineage-step][data-lineage-attempt="4"]')).toHaveAttribute('data-lineage-cause', 'recheck', within(30_000))
  const note = findingRow(page, 'The first line, as attempt 3 had it.')
  await expect(note).toBeVisible(within(30_000))
  await note.locator('[data-finding-mark="resolved"]').click()
  await expect(note.locator('[data-finding-mark-blocked]')).toContainText('the commit the finding was made on')
  await expect(note.locator('[data-finding-mark-confirm]')).toBeDisabled()
  const fid = (await findingsOf(api, ticketKey, cand4)).find((f) => f.message === 'The first line, as attempt 3 had it.')!.id
  const refused = await api.call<{ receipt?: { status: string } }>('POST', `/api/dispatch/findings/${fid}/marks`, { opId: opOf(key, 'm1'), candidate: cand4, verdict: 'resolved', via: 'web' })
  expect(refused.status).toBeGreaterThanOrEqual(400)
  expect((await findingsOf(api, ticketKey, cand4)).find((f) => f.id === fid)!.state).toBe('open')

  // A request for changes to attempt 3, sent after build moved on to attempt 4: recorded, and refused by Yardmaster; Request again
  // asks it as its next try, judged against the run as it is now.
  const late = await api.ok<{ review: { id: string; application: { state: string; try?: number } } }>('POST', `/api/dispatch/tickets/${ticketKey}/reviews`, {
    opId: opOf(key, 'rv-late'),
    candidate: cand3,
    base: runBase,
    verdict: 'request_changes',
    via: 'web',
    findings: [{ kind: 'general', intent: 'change', severity: 'major', message: 'Make the check pass.' }],
  })
  expect(late.review.application.state).toBe('refused')
  await page.goto(`/dispatch/${key}/1?view=review&candidate=${encodeURIComponent(cand3)}`)
  const lateRow = page.locator(`[data-review-sent][data-review-id="${late.review.id}"]`)
  await expect(lateRow).toHaveAttribute('data-review-application', 'refused', within(60_000))
  await lateRow.locator('[data-review-retry]').click()
  budget(60_000)
  await expect
    .poll(async () => (await api.ok<{ review: { application: { state: string; try?: number } } }>('GET', `/api/dispatch/reviews/${late.review.id}`)).review.application, { timeout: 60_000, intervals: [500], message: 'Request again made its next try' })
    .toMatchObject({ state: 'refused', try: 2 })
  await expect(lateRow.locator('[data-review-retry]')).toBeVisible(within(30_000))
  await expect(lateRow).toContainText('try 2 of 5', within(30_000))
})

test("a failed check of a run stopped and continued, checked again in the continuation from the first run's receipt", async ({ page, api, state }) => {
  test.slow()
  await stopStrays(api, [])
  const counter = join(state.root, `review-repair-counter-${Date.now().toString(36)}`)
  cleanup.push(() => rmSync(counter, { force: true }))
  const { key, run } = await launched(api, state.root, 'continued', countingCheck(counter))

  const failed = await candidateUntil(api, run, 'build', 3, failedCheck, "build's attempt 3 failed its check")
  const c3 = failed.commit!
  const cand3 = `${run}/build/3@${c3}`
  const bad = failed.receipts.find((r) => r.phase === 'check' && r.exit !== 0)!

  // The run stopped, then continued on the run page.
  await api.stopRun(run)
  budget(60_000)
  await expect.poll(async () => !!(await api.run(run)).stoppedAt, { timeout: 60_000, intervals: [500], message: 'the run is stopped' }).toBe(true)
  await page.goto(`/runs/${run}`)
  await page.locator('[data-run-resume]').click(within(60_000))
  let next = ''
  budget(60_000)
  await expect.poll(async () => (next = (await api.run(run)).resumedBy ?? ''), { timeout: 60_000, intervals: [500], message: 'the run is continued' }).not.toBe('')
  runs.push(next)

  // Prepare a repair from the first run's receipt: Check again offered for the continuation, and pressed, checked there.
  await page.goto(`/dispatch/${key}/1?view=review&candidate=${encodeURIComponent(cand3)}&repair=${bad.id}/${bad.k}`)
  const panel = page.locator('[data-repair-panel]')
  await expect(panel).toBeVisible(within(60_000))
  await expect(panel).toHaveAttribute('data-repair-receipt', `${run}/build/3/check/ok/${bad.k}`)
  await expect(panel).toHaveAttribute('data-repair-recheck-allowed', 'true', within(30_000))
  await page.locator('[data-repair-recheck]').click()
  const words = panel.locator('[data-repair-rechecked]')
  await expect(words).toContainText(`of run ${next}, a continuation of ${run}.`, within(60_000))
  const n = Number(/attempt (\d+) of run/.exec((await words.textContent()) ?? '')?.[1])
  expect(n).toBeGreaterThan(3)
  const again = await candidateUntil(api, next, 'build', n, passed, "the continuation's check again stands with its own passing receipt")
  expect(again.commit).toBe(c3)
  const firstNow = (await candidatesOf(api, run, 'build')).find((c) => c.attempt === 3)!
  expect(firstNow.receipts.map((r) => [r.phase, r.id, r.k, r.state, r.exit])).toEqual(failed.receipts.map((r) => [r.phase, r.id, r.k, r.state, r.exit]))
})
