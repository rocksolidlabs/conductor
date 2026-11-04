import { execFileSync } from 'node:child_process'
import { existsSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import type { Page } from '@playwright/test'
import { expect, logged, member, task, test, type Api, type Run } from './fixtures'
import { freeProjectKey, makeProject, makeTicket, mark, marked, opOf, sideways, storedTicket } from './dispatch-helpers'

// A ticket's runs and their evidence (Program item DS2, docs/plans/dispatch-evidence.md, PR-11), the assertions that need PR-8's routes
// and PR-11's pages: a ticket launched from its approved plan revision; the run under Runs and its page's header "Launched from KEY ·
// plan r1"; the candidate's committed file holding the ticket's first line, which the stub read from .conductor/ticket.md. The crew's
// check fails on its first run (a script, as tasks.spec.ts makes one, held at a gate the spec opens so the run is paused while it runs)
// and its review asks for changes once (the stub's): attempt 1's candidate with its failed receipt and where it ran, attempt 2 at that
// result passing, the review's finding counted and, opened, its message read from the task route, and the repairs; a receipt's log
// opened, its shown hash the route's header. The run paused, the title and the description edited, the run unpaused: the banner names
// them and every later candidate's file still holds the launch-time line. A member that asks a question while its task stays running
// (`stub ask-wait`) shows under the run's questions with no task transition and no reload, and, answered, leaves them. The phone width
// one column. The ticket is found by its key and the run by its id; every wait is on the state the ledger, the store or the page must
// reach, never on the clock.
test.describe.configure({ mode: 'serial' })

const ASK = `Which way, ${Date.now().toString(36)}?`
const REPLY = 'the left one'

let crewId = ''
let crewName = ''
let gateDir = ''
const runs: string[] = []

test.beforeAll(async ({ api }) => {
  gateDir = mkdtempSync(join(tmpdir(), 'conductor-evidence-'))
  const gate = join(gateDir, 'open')
  crewName = `e2e evidence ${Date.now().toString(36)}`
  crewId = (
    await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
      name: crewName,
      goal: 'prove the evidence',
      cwd: '',
      where: 'server',
      isolation: 'worktree',
      openAfterLaunch: false,
      yolo: null,
      members: [
        { name: 'builder', agentId: 'worker', prompt: '', start: { when: 'task' } },
        { name: 'critic', agentId: 'worker-cli', prompt: '', start: { when: 'task' } },
        // Not a worker: it reads its task line as any line and never submits, so its task stays running while it asks.
        { name: 'asker', agentId: 'claude', prompt: '', start: { when: 'task' } },
      ],
      tasks: [
        // The check waits for the spec's gate, then fails until the builder adds stub-fixed.txt, which it does from its second attempt.
        { id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.', checks: [{ id: 'fixed', argv: ['sh', '-c', `while [ ! -e '${gate}' ]; do sleep 0.1; done; git cat-file -e HEAD:stub-fixed.txt`] }] },
        { id: 'review', member: 'critic', kind: 'review', instructions: 'Review it.', needs: ['build'] },
        { id: 'hold', member: 'asker', kind: 'work', instructions: 'Hold the line.' },
      ],
    })
  ).crew.id
})

test.afterAll(async ({ api }) => {
  for (const id of runs) await api.stopRun(id)
  if (crewId) await api.call('DELETE', `/api/crews/${encodeURIComponent(crewId)}`)
  if (gateDir) rmSync(gateDir, { recursive: true, force: true })
})

const git = (cwd: string, ...args: string[]) => execFileSync('git', args, { cwd, encoding: 'utf8' }).trim()

interface Receipt {
  phase: string
  id: string
  k: number
  where: string
  state: string
  exit?: number
  logSha256?: string
}
interface Candidate {
  attempt: number
  commit?: string
  start?: string
  state: string
  verdict?: string
  findings?: Record<string, number>
  receipts: Receipt[]
}

/** A task's candidates as the candidates route answers them, from the ledger. */
async function candidatesOf(api: Api, run: string, taskId: string): Promise<Candidate[]> {
  const r = await api.ok<{ candidates: Array<{ task: string; candidates: Candidate[] }> }>('GET', `/api/runs/${encodeURIComponent(run)}/candidates`)
  return r.candidates.find((t) => t.task === taskId)?.candidates ?? []
}

/**
 * Raises the running test's timeout by the cap of a long wait as the step is about to make it (AGENTS.md, Tests that do not flake), as
 * dispatch-plan.spec.ts does: the run's waits are two minutes each, and their sum is above the slow test's timeout.
 */
function budget(ms: number) {
  const info = test.info()
  info.setTimeout(info.timeout + ms)
}

/** A run read until `done` holds of it. */
async function runUntil(api: Api, id: string, done: (r: Run) => boolean, message: string, timeout = 120_000): Promise<Run> {
  budget(timeout)
  let last = await api.run(id)
  await expect.poll(async () => done((last = await api.run(id))), { timeout, intervals: [500], message }).toBe(true)
  return last
}

/** The stub whose transcript holds the line: its id names its files under the server's HOME. */
function stubOf(home: string, line: string): string {
  const dir = join(home, '.stub-sessions')
  for (const f of existsSync(dir) ? readdirSync(dir) : []) {
    if (f.endsWith('.txt') && readFileSync(join(dir, f), 'utf8').split('\n').includes(line)) return f.slice(0, -4)
  }
  return ''
}

/** The run's entry on the ticket page. */
const entryOf = (page: Page, run: string) => page.locator(`[data-ticket-runs] [data-run-entry][data-ticket-run="${run}"]`)

test('a launched run under Runs: its candidates with their receipts and where they ran, a review\'s finding, the repairs, a log\'s hash, what changed since, and a question that comes and goes', async ({ page, api, state }) => {
  // A run of seven attempts with a pause between, and some forty steps around it.
  test.slow()
  const key = await freeProjectKey(api, 'EV')
  await makeProject(api, key)
  const title = 'Fix the login loop'
  const ticketKey = await makeTicket(api, key, '1', { title, description: 'It loops after the token expires.', acceptance: 'It stops looping.' })
  const line = `ticket: # ${ticketKey}: ${title}`

  // Revision 1 proposed from the crew, approved, and launched, through the routes the Plan section calls (dispatch-plan.spec.ts drives its dialogs).
  const proposed = await api.ok<{ plan: { id: string } }>('POST', `/api/dispatch/tickets/${ticketKey}/plans`, { opId: opOf(key, 'r1'), crew: crewId })
  await api.ok('POST', `/api/dispatch/plans/${proposed.plan.id}/approve`, { opId: opOf(key, 'ap1'), note: 'Go.' })
  const launched = await api.ok<{ launch: { id: string; runId: string; state: string } }>('POST', `/api/dispatch/tickets/${ticketKey}/launch`, { opId: opOf(key, 'l1'), plan: proposed.plan.id })
  const run = launched.launch.runId
  runs.push(run)
  expect(launched.launch.state).toBe('started')

  // The ticket page: the launch under Runs, started, with its run.
  await page.goto(`/dispatch/${key}/1`)
  await expect(page.locator('[data-ticket-title]')).toHaveText(title, { timeout: 30_000 })
  const entry = entryOf(page, run)
  await expect(entry).toHaveAttribute('data-run-entry-kind', 'launch')
  await expect(entry).toHaveAttribute('data-launch-entry', launched.launch.id)
  await expect(entry).toHaveAttribute('data-launch-state', 'started')
  await expect(entry.locator('[data-launch-entry-plan]')).toHaveText('r1')
  await expect(entry.locator('[data-ticket-run-open]')).toBeVisible()
  await mark(page)

  // Attempt 1 of build submitted and its check runs, held at the gate; hold's attempt runs too. The run paused there, the title and the
  // description edited, then the gate opened: the check fails, and no attempt starts while the run is paused.
  budget(120_000)
  await expect.poll(async () => (await candidatesOf(api, run, 'build'))[0]?.receipts[0]?.state ?? '', { timeout: 120_000, message: "build's first check runs" }).toMatch(/^(started|running)$/)
  await runUntil(api, run, (r) => task(r, 'hold').state === 'running' && !!member(r, 'asker').sessionId, "hold's attempt runs")
  await api.ok('POST', `/api/runs/${encodeURIComponent(run)}/pause`, { commandId: `${key.toLowerCase()}-pause` })
  const before = await storedTicket(api, ticketKey)
  await api.ok('POST', `/api/dispatch/tickets/${ticketKey}/edit`, { opId: opOf(key, 'e1'), expectedRev: before.rev, set: { title: 'Fix the login loop, and the logout', description: 'It loops, and logging out loops too.' } })
  writeFileSync(join(gateDir, 'open'), '')
  budget(60_000)
  await expect.poll(async () => (await candidatesOf(api, run, 'build'))[0]?.receipts[0]?.state ?? '', { timeout: 60_000, message: "build's first check ends" }).toBe('finished')
  const paused = await api.run(run)
  expect([paused.paused, task(paused, 'build').attempts]).toEqual([true, 1])

  // The banner names what changed, with no reload; the run keeps working from what it was given.
  const changed = entry.locator('[data-changed-since]')
  await expect(changed).toHaveAttribute('data-changed-fields', 'title,description', { timeout: 30_000 })
  await expect(changed).toContainText('The ticket changed after this run launched: the title and the description')
  await expect(changed.locator('[data-changed-text]')).toContainText('The run keeps working from what it was given: the ticket at its revision')
  await expect(page.locator('[data-ticket-title]')).toHaveText('Fix the login loop, and the logout')
  // Attempt 1's candidate: its commit, its failed receipt, where it ran.
  const cands = entry.locator(`[data-candidates][data-candidates-run="${run}"]`)
  const first = cands.locator('[data-candidate="build/1"]')
  await expect(first.locator('[data-receipt="check:fixed:1"]')).toHaveAttribute('data-receipt-state', 'failed')
  await expect(first.locator('[data-receipt="check:fixed:1"]')).toHaveAttribute('data-receipt-where', 'native')
  await expect(first.locator('[data-receipt="check:fixed:1"]')).toContainText('failed (exit')

  // Unpaused: the repair at the failed result passes, the review asks for changes once, the repair of the reviewed commit passes, and
  // the review approves. The ledger is waited on for what it must hold.
  await api.ok('POST', `/api/runs/${encodeURIComponent(run)}/unpause`, { commandId: `${key.toLowerCase()}-unpause` })
  const done = await runUntil(api, run, (r) => task(r, 'build').state === 'succeeded' && task(r, 'review').state === 'succeeded', 'build and review succeed')
  expect([task(done, 'build').attempts, task(done, 'review').attempts, task(done, 'hold').state]).toEqual([3, 2, 'running'])
  const build = await candidatesOf(api, run, 'build')
  const review = await candidatesOf(api, run, 'review')
  expect(build.map((c) => c.attempt)).toEqual([3, 2, 1])
  const [b3, b2, b1] = build as [Candidate, Candidate, Candidate]
  expect(b2.start).toBe(b1.commit)
  expect(review.map((c) => [c.attempt, c.verdict])).toEqual([
    [2, 'approved'],
    [1, 'needs_changes'],
  ])
  expect(review[1]!.start).toBe(b2.commit)
  expect(b3.start).toBe(b2.commit)

  // The page: attempt 2 at attempt 1's result passing, the review's finding counted and, opened, read from the task route; the repairs.
  await expect(cands.locator('[data-candidate="build/3"]')).toHaveAttribute('data-candidate-state', 'succeeded', { timeout: 30_000 })
  await expect(cands.locator('[data-candidate="build/2"]')).toHaveAttribute('data-candidate-start', b1.commit!)
  await expect(cands.locator('[data-candidate="build/2"] [data-receipt="check:fixed:1"]')).toHaveAttribute('data-receipt-state', 'passed')
  await expect(cands.locator('[data-candidate="build/1"]')).toHaveAttribute('data-candidate-commit', b1.commit!)
  await expect(cands.locator('[data-candidate="build/3"] [data-candidate-standing]')).toBeVisible()
  const critique = cands.locator('[data-candidate="review/1"]')
  await expect(critique.locator('[data-candidate-verdict]')).toHaveText('needs changes')
  await expect(critique.locator('[data-candidate-findings]')).toHaveAttribute('data-candidate-findings', '1')
  await critique.locator('[data-candidate-findings-open]').click()
  await expect(critique.locator('[data-candidate-finding-message]')).toHaveText('add stub-fixed.txt')
  await expect(cands.locator('[data-repair="check:build:1"]')).toHaveAttribute('data-repair-by', 'build/2')
  await expect(cands.locator('[data-repair="review:review:1"]')).toHaveAttribute('data-repair-by', 'build/3')

  // A receipt's log opened: the hash shown is the one the step log route sends, and the receipt's.
  const res = await fetch(`${api.server.baseURL}/api/runs/${encodeURIComponent(run)}/tasks/build/attempts/1/steps/fixed/1/log`, { headers: { Authorization: `Bearer ${api.server.token}` } })
  expect(res.status).toBe(200)
  const served = res.headers.get('X-Conductor-Log-SHA256') ?? ''
  expect(served).toMatch(/^[0-9a-f]{64}$/)
  expect(b1.receipts[0]!.logSha256).toBe(served)
  await first.locator('[data-receipt="check:fixed:1"]').click()
  const modal = page.locator('[data-receipt-modal]')
  await expect(modal.locator('[data-receipt-log-sha]')).toHaveText(served)
  await expect(modal.locator('[data-receipt-sha-match]')).toHaveAttribute('data-receipt-sha-match', 'matches')
  await expect(modal.locator('[data-receipt-recorded-sha]')).toHaveText(served)
  await expect(modal.locator('[data-receipt-commit]')).toHaveText(b1.commit!)
  await expect(modal.locator('[data-receipt-argv]')).toContainText('git cat-file -e HEAD:stub-fixed.txt')
  await page.keyboard.press('Escape')
  await expect(modal).toBeHidden()

  // The package reached every attempt: each commit's file holds the ticket's first line as launched, never the title edited after.
  for (const c of [b1, b3]) {
    const file = git(state.repo, 'show', `${c.commit}:stub-builder.txt`)
    const tickets = file.split('\n').filter((l) => l.startsWith('ticket: '))
    expect(tickets.length).toBe(c.attempt)
    expect(new Set(tickets)).toEqual(new Set([line]))
  }

  // A question: the asker, whose task stays running, asks its own; it shows under the run's questions with no task transition and no
  // reload. The engine's one reminder to it is typed first, so the question is the line it answers last.
  await runUntil(api, run, (r) => logged(r, 'task hold: asker reports done without a result; reminded once'), 'the asker was reminded once')
  const card = entry.locator(`[data-run-card="${run}"]`)
  await expect(card.locator('[data-task-row="hold"]')).toHaveAttribute('data-task-state', 'running')
  await expect(card).toHaveAttribute('data-run-questions-count', '0')
  const asked = await api.ok<{ sent: string[] }>('POST', `/api/runs/${encodeURIComponent(run)}/broadcast`, { text: `stub ask-wait ${ASK}`, members: ['asker'] })
  expect(asked.sent).toEqual(['asker'])
  const question = card.locator('[data-run-question="asker"]')
  await expect(question.locator('[data-run-question-message]')).toHaveText(ASK, { timeout: 30_000 })
  await expect(card).toHaveAttribute('data-run-questions-count', '1')
  const asker = member(await api.run(run), 'asker').sessionId!
  await expect(question.locator('[data-run-question-open]')).toHaveAttribute('href', `/sessions/${asker}`)
  await expect(card.locator('[data-task-row="hold"]')).toHaveAttribute('data-task-state', 'running')
  expect(task(await api.run(run), 'hold').attempts).toBe(1)

  // Answered from the asker's row in another page: it leaves the run's questions here, with no reload.
  const stub = stubOf(state.home, `stub ask-wait ${ASK}`)
  expect(stub).not.toBe('')
  writeFileSync(join(state.home, '.stub-sessions', `${stub}.spin`), 'stop\n')
  const other = await page.context().newPage()
  await other.goto('/crews')
  const row = other.locator(`[data-session-list="sidebar"] [data-sidebar-row="s:${asker}"]`).first()
  await expect(row).toHaveAttribute('data-row-state', 'needs', { timeout: 30_000 })
  const field = row.locator('[data-row-reply] input, input[data-row-reply]').first()
  await field.fill(REPLY)
  await field.press('Enter')
  await expect(card.locator('[data-run-question="asker"]')).toHaveCount(0, { timeout: 30_000 })
  await expect(card).toHaveAttribute('data-run-questions-count', '0')
  await expect(card.locator('[data-task-row="hold"]')).toHaveAttribute('data-task-state', 'running')
  await other.close()
  expect(await marked(page)).toBe('kept')

  // The run page's header: launched from the ticket, at plan revision 1, opening it.
  await page.goto(`/runs/${encodeURIComponent(run)}`)
  const from = page.locator('[data-run-launched-from]')
  await expect(from).toHaveText(`Launched from ${ticketKey} · plan r1`, { timeout: 30_000 })
  await from.click()
  await expect(page).toHaveURL(new RegExp(`/dispatch/${key}/1$`))
})

test("a phone shows a ticket's runs in one column, nothing wider than the screen", async ({ page, api }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const run = runs[0]!
  const r = await api.run(run)
  const ticketKey = (r as unknown as { ticket?: { key: string } }).ticket?.key ?? ''
  expect(ticketKey).not.toBe('')
  const [key, n] = ticketKey.split('-') as [string, string]
  await page.goto(`/dispatch/${key}/${n}`)
  const entry = entryOf(page, run)
  await expect(entry.locator('[data-candidate="build/1"]')).toBeVisible({ timeout: 30_000 })
  await entry.locator('[data-candidate="build/1"]').scrollIntoViewIfNeeded()
  expect(await sideways(page)).toBeLessThanOrEqual(0)
  for (const el of [entry, entry.locator('[data-candidate="build/1"]'), entry.locator('[data-changed-since]')]) {
    const box = await el.boundingBox()
    expect(box && box.x >= 0 && box.x + box.width <= 390, 'within the screen').toBeTruthy()
  }
})
