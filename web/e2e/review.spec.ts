import { mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Locator, Page } from '@playwright/test'
import { expect, git, stopStrays, task, test, type Api, type Run } from './fixtures'
import { scratchRepo } from './server'
import { freeProjectKey, makeProject, makeTicket, opOf } from './dispatch-helpers'

// The Review view (Program item RV1, docs/plans/review-repair.md, PR-6): a ticket launched from an approved revision of a stub worker
// crew, in a repository of the spec's own (one commit holding stub-builder.txt, so the candidate changes a file the base has). Its
// `build` task needs `prep`, so `build`'s first attempt starts at prep's result, not at the run's base: the base picker offers both.
// The Review view at build's first candidate; lines selected on the candidate side of stub-prepper.txt (added since the run's base),
// the diff then read against where the attempt started and lines selected on the base side of stub-builder.txt, which locks the base;
// a whole-file note and a general note; the draft, its base and the lock kept through a reload; Send review as Request changes after
// its confirmation; the review shown recorded and applied with its attempt, and as the store keeps it, its base and each note's anchor
// the server's; the repair's candidate with its own receipt, the first's unchanged; and a batch the run's task file could not hold
// refused, with nothing kept. A `hold` task by a member that never submits keeps the run the server's (never finished and let go), so
// the request is applied to the run as it is. The ticket and the run are found by their key and id; every wait is on what the ledger,
// the store or the page must reach, each raising the test's timeout by its own cap.
test.describe.configure({ mode: 'serial' })

let repo = ''
let crewId = ''
const runs: string[] = []

test.beforeAll(async ({ api, state }) => {
  repo = join(state.root, `review-${Date.now().toString(36)}`)
  mkdirSync(repo)
  scratchRepo(repo)
  writeFileSync(join(repo, 'stub-builder.txt'), 'one\ntwo\nthree\n')
  git(repo, 'add', 'stub-builder.txt')
  git(repo, 'commit', '-q', '-m', 'the builder\'s file')
  crewId = (
    await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
      name: `e2e review ${Date.now().toString(36)}`,
      goal: 'review a candidate',
      cwd: repo,
      where: 'server',
      isolation: 'worktree',
      openAfterLaunch: false,
      yolo: null,
      members: [
        { name: 'prepper', agentId: 'worker', prompt: '', start: { when: 'task' } },
        { name: 'builder', agentId: 'worker', prompt: '', start: { when: 'task' } },
        // Not a worker: it reads its task line as any line and never submits, so the run stays running, the server's.
        { name: 'asker', agentId: 'claude', prompt: '', start: { when: 'task' } },
      ],
      tasks: [
        { id: 'prep', member: 'prepper', kind: 'work', instructions: 'Prepare it.' },
        { id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.', needs: ['prep'], checks: [{ id: 'ok', argv: ['sh', '-c', 'test -s stub-builder.txt'] }] },
        { id: 'hold', member: 'asker', kind: 'work', instructions: 'Hold the line.' },
      ],
    })
  ).crew.id
})

test.afterAll(async ({ api }) => {
  for (const id of runs) await api.stopRun(id)
  if (crewId) await api.call('DELETE', `/api/crews/${encodeURIComponent(crewId)}`)
  if (repo) rmSync(repo, { recursive: true, force: true })
})

interface Receipt {
  phase: string
  id: string
  k: number
  state: string
  exit?: number
}
interface Candidate {
  attempt: number
  commit?: string
  start?: string
  state: string
  standing: boolean
  receipts: Receipt[]
}

/** A task's candidates as the candidates route answers them, from the ledger. */
async function candidatesOf(api: Api, run: string, taskId: string): Promise<Candidate[]> {
  const r = await api.ok<{ candidates: Array<{ task: string; candidates: Candidate[] }> }>('GET', `/api/runs/${encodeURIComponent(run)}/candidates`)
  return r.candidates.find((t) => t.task === taskId)?.candidates ?? []
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

/** A candidate of the task, read from the ledger until `done` holds of it. */
async function candidateUntil(api: Api, run: string, taskId: string, attempt: number, done: (c: Candidate) => boolean, message: string, timeout = 120_000): Promise<Candidate> {
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

/** Selects lines of one side of the diff shown: a click on the first line's text, then Shift with the keys given. */
async function selectLines(page: Page, side: 'original' | 'modified', text: string, keys: string[]) {
  const line = page.locator(`[data-review-diff] .editor.${side} .view-line`, { hasText: text }).first()
  await expect(line).toBeVisible(within(30_000))
  await line.click()
  await page.keyboard.press('Home')
  for (const k of keys) await page.keyboard.press(k)
}

/** Fills the note's form open on the page and adds it to the draft. */
async function addNote(form: Locator, words: string) {
  await expect(form).toBeVisible(within(15_000))
  await form.locator('textarea').fill(words, within(15_000))
  await expect(form.locator('textarea')).toHaveValue(words)
  await form.locator('[data-review-note-save]').click()
  await expect(form).toHaveCount(0, within(15_000))
}

test('a candidate reviewed: notes on both sides, a file and the whole, the draft and its base kept, Request changes applied, the repair with its own receipts, and a batch too large kept nowhere', async ({ page, api }) => {
  test.slow()
  await stopStrays(api, [])
  const key = await freeProjectKey(api, 'RV')
  await makeProject(api, key)
  const ticketKey = await makeTicket(api, key, '1', { title: 'Build the thing', description: 'It needs building.', acceptance: 'It is built.' })
  const runBase = git(repo, 'rev-parse', 'HEAD').trim()

  // Revision 1 proposed from the crew, approved and launched, through the routes the Plan section calls.
  const proposed = await api.ok<{ plan: { id: string } }>('POST', `/api/dispatch/tickets/${ticketKey}/plans`, { opId: opOf(key, 'r1'), crew: crewId })
  await api.ok('POST', `/api/dispatch/plans/${proposed.plan.id}/approve`, { opId: opOf(key, 'ap1'), note: 'Go.' })
  const launched = await api.ok<{ launch: { runId: string; state: string } }>('POST', `/api/dispatch/tickets/${ticketKey}/launch`, { opId: opOf(key, 'l1'), plan: proposed.plan.id })
  const run = launched.launch.runId
  runs.push(run)
  expect(launched.launch.state).toBe('started')

  // build's first attempt, its check passed: the first candidate, which started at prep's result.
  const first = await candidateUntil(api, run, 'build', 1, (c) => c.standing && !!c.commit && c.receipts.some((r) => r.state === 'finished' && r.exit === 0), "build's first candidate stands with its check passed")
  const c1 = first.commit!
  const start = first.start!
  expect(start).toBe((await candidatesOf(api, run, 'prep'))[0]!.commit)
  expect(start).not.toBe(runBase)
  const cand1 = `${run}/build/1@${c1}`

  // The ticket page's candidate offers Review: the Review view at it, the diff against the run's base.
  await page.goto(`/dispatch/${key}/1`)
  const entry = page.locator(`[data-run-entry][data-ticket-run="${run}"] [data-candidate="build/1"]`)
  await expect(entry.locator('[data-candidate-review]')).toBeVisible(within(60_000))
  await entry.locator('[data-candidate-review]').click()
  const view = page.locator(`[data-review-view][data-review-candidate="${cand1}"]`)
  await expect(view).toHaveAttribute('data-review-diff-base', runBase, within(30_000))
  await expect(page.locator('[data-review-later]')).toBeVisible()
  await expect(page.locator('[data-review-files]')).toHaveAttribute('data-review-files-count', '2', within(15_000))

  // A note on lines of the candidate side of stub-prepper.txt, added since the run's base.
  await page.locator('[data-review-file-path="stub-prepper.txt"]').click()
  await expect(page.locator('[data-review-diff]')).toHaveAttribute('data-review-diff-path', 'stub-prepper.txt', within(15_000))
  await selectLines(page, 'modified', 'attempt 1 of prep by prepper', ['Shift+End'])
  const bar = page.locator('[data-review-selection-bar]')
  await expect(bar).toHaveAttribute('data-review-selection-side', 'candidate', within(15_000))
  await bar.locator('[data-review-add-note]').click()
  await addNote(page.locator('[data-review-note-form][data-review-note-kind="line"]'), 'Say what was prepared.')
  await expect(page.locator('[data-review-draft]')).toHaveAttribute('data-review-draft-notes', '1')

  // The diff read against where the attempt started instead: the draft's base moves with it while every note is on the candidate side.
  await page.locator('[data-review-base]').click()
  await page.getByRole('option', { name: /where attempt 1 started/ }).click()
  await expect(page.getByRole('listbox')).toHaveCount(0, within(15_000))
  await expect(view).toHaveAttribute('data-review-diff-base', start, within(30_000))
  await expect(page.locator('[data-review-files]')).toHaveAttribute('data-review-files-count', '1', within(15_000))

  // A note on lines of the base side of stub-builder.txt: the base is locked to it.
  await expect(page.locator('[data-review-diff]')).toHaveAttribute('data-review-diff-path', 'stub-builder.txt', within(15_000))
  await selectLines(page, 'original', 'one', ['Shift+ArrowDown', 'Shift+End'])
  await expect(bar).toHaveAttribute('data-review-selection-side', 'base', within(15_000))
  await bar.locator('[data-review-add-note]').click()
  await addNote(page.locator('[data-review-note-form][data-review-note-kind="line"]'), 'Why do these two lines stay?')
  await expect(page.locator('[data-review-base-locked]')).toContainText('1 note is on the base')
  await expect(page.locator('[data-review-base]')).toBeDisabled()

  // A note on the whole file, and one on the candidate as a whole.
  await page.locator('[data-review-note-file]').click()
  await addNote(page.locator('[data-review-note-form][data-review-note-kind="file"]'), 'This file grows by appending: say why.')
  await page.locator('[data-review-general]').click()
  await addNote(page.locator('[data-review-note-form][data-review-note-kind="general"]'), 'Add stub-fixed.txt as the other tasks do.')
  await expect(page.locator('[data-review-draft]')).toHaveAttribute('data-review-draft-notes', '4')

  // Reloaded: the draft, its base and the lock as they were.
  await page.reload()
  await expect(view).toHaveAttribute('data-review-diff-base', start, within(60_000))
  await expect(page.locator('[data-review-draft]')).toHaveAttribute('data-review-draft-notes', '4', within(15_000))
  await expect(page.locator('[data-review-base-locked]')).toBeVisible()
  await expect(page.locator('[data-review-base]')).toBeDisabled()
  const sides = await page.locator('[data-review-draft-note]').evaluateAll((els) => els.map((el) => `${el.getAttribute('data-review-note-kind')}:${el.getAttribute('data-review-note-side')}:${el.getAttribute('data-review-note-path')}`))
  expect(sides).toEqual(['line:candidate:stub-prepper.txt', 'line:base:stub-builder.txt', 'file:candidate:stub-builder.txt', 'general::'])

  // Send review as Request changes, after its confirmation: recorded, then applied with the attempt that takes it up.
  const summary = page.locator('[data-review-draft]').getByLabel('Summary', { exact: true })
  await summary.fill('Four things before it ships.', within(15_000))
  await expect(summary).toHaveValue('Four things before it ships.')
  await page.locator('[data-review-send]').click()
  const confirm = page.locator('[data-review-confirm-send]')
  await expect(confirm).toBeVisible(within(15_000))
  await expect(page.getByRole('dialog')).toContainText('Yardmaster reopens build at')
  await confirm.click()
  const sent = page.locator('[data-review-sent]')
  await expect(sent).toHaveAttribute('data-review-application', 'applied', within(60_000))
  await expect(sent.locator('[data-review-recorded]')).toContainText('Recorded in Dispatch: 4 notes')
  await expect(sent.locator('[data-review-applied]')).toHaveAttribute('data-review-attempt', '2')
  await expect(sent.locator('[data-review-applied]')).toHaveAttribute('data-review-run', run)
  // The draft is emptied once sent.
  await expect(page.locator('[data-review-draft]')).toHaveAttribute('data-review-draft-notes', '0')

  // As the store keeps it: the base the draft was read against, each anchor the server's.
  const listed = await api.ok<{ reviews: Array<{ id: string; candidate: string }> }>('GET', `/api/dispatch/tickets/${ticketKey}/reviews`)
  expect(listed.reviews.map((r) => r.candidate)).toEqual([cand1])
  const kept = (await api.ok<{ review: { base: string; verdict: string; summary: string; findings: Array<{ kind: string; side?: string; anchorCommit?: string; from?: number; to?: number; lines: string[] }> } }>('GET', `/api/dispatch/reviews/${listed.reviews[0]!.id}`)).review
  expect([kept.base, kept.verdict, kept.summary]).toEqual([start, 'request_changes', 'Four things before it ships.'])
  expect(kept.findings.map((f) => [f.kind, f.side ?? '', f.anchorCommit ?? ''])).toEqual([
    ['line', 'candidate', c1],
    ['line', 'base', start],
    ['file', 'candidate', c1],
    ['general', '', ''],
  ])
  expect(kept.findings[0]).toMatchObject({ from: 1, to: 1, lines: ['attempt 1 of prep by prepper'] })
  expect(kept.findings[1]).toMatchObject({ from: 1, to: 2, lines: ['one', 'two'] })

  // The repair: build's attempt 2 at the reviewed commit, a candidate with its own receipt; the first candidate's receipts unchanged.
  const second = await candidateUntil(api, run, 'build', 2, (c) => c.standing && !!c.commit && c.receipts.some((r) => r.state === 'finished' && r.exit === 0), "the repair's candidate stands with its own check passed")
  expect(second.start).toBe(c1)
  expect(second.commit).not.toBe(c1)
  const firstNow = (await candidatesOf(api, run, 'build')).find((c) => c.attempt === 1)!
  expect(firstNow.receipts.map((r) => [r.phase, r.id, r.k, r.state])).toEqual(first.receipts.map((r) => [r.phase, r.id, r.k, r.state]))
  await page.locator('[data-review-back]').click()
  const candidates = page.locator(`[data-run-entry][data-ticket-run="${run}"]`)
  await expect(candidates.locator('[data-candidate="build/2"] [data-receipt]')).toHaveCount(second.receipts.length, within(60_000))
  await expect(candidates.locator('[data-candidate="build/1"] [data-receipt]')).toHaveCount(first.receipts.length)
  await expect(candidates.locator('[data-candidate="build/1"] [data-candidate-review]')).toHaveCount(0)

  // A batch the repair attempt's task file could not hold: fifty notes on paths of 1,024 bytes, under what the run keeps of a repair, so
  // the page lets it go, and past what build's task file holds with them, so the server refuses it whole, keeping nothing.
  const cand2 = `${run}/build/2@${second.commit}`
  const notes = Array.from({ length: 50 }, (_, i) => ({ key: `big-${i}`, kind: 'file', intent: 'change', severity: 'nit', path: `${String(i).padStart(4, '0')}${'p'.repeat(1020)}`, side: 'candidate', message: 'Too long a path.' }))
  await page.evaluate(
    ([k, v]) => localStorage.setItem(k!, v!),
    [`conductor.reviewDraft:${ticketKey}:${cand2}`, JSON.stringify({ v: 1, base: runBase, verdict: 'request_changes', summary: '', notes })],
  )
  await page.goto(`/dispatch/${key}/1?view=review&candidate=${encodeURIComponent(cand2)}`)
  const view2 = page.locator(`[data-review-view][data-review-candidate="${cand2}"]`)
  await expect(view2).toHaveAttribute('data-review-diff-base', runBase, within(60_000))
  await expect(page.locator('[data-review-draft]')).toHaveAttribute('data-review-draft-notes', '50', within(15_000))
  await expect(page.locator('[data-review-draft-problems]')).toHaveCount(0)
  await expect(page.locator('[data-review-send]')).toBeEnabled()
  await page.locator('[data-review-send]').click()
  await expect(confirm).toBeVisible(within(15_000))
  await confirm.click()
  await expect(page.locator('[data-review-draft-failure]')).toContainText('more than 64 KiB', within(60_000))
  await expect(page.locator('[data-review-draft]')).toHaveAttribute('data-review-draft-notes', '50')
  const after = await api.ok<{ reviews: Array<{ id: string }> }>('GET', `/api/dispatch/tickets/${ticketKey}/reviews`)
  expect(after.reviews.map((r) => r.id)).toEqual([listed.reviews[0]!.id])
  const r = await api.run(run)
  expect(task(r as Run, 'build').state).toBe('succeeded')
})
