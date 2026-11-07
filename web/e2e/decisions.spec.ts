import { execFileSync } from 'node:child_process'
import type { Page } from '@playwright/test'
import { expect, stopStrays, task, test, type Session } from './fixtures'
import { budget, decisionServer, decisionUntil, decisionsOf, makeTicket, operatorPage, propose, runUntil, type DecisionServer } from './decisions-helpers'
import { storedTicket } from './dispatch-helpers'

// An agent's durable decision answered where its question is (Program item DR1, docs/plans/decisions.md, PR-7), on a server of its own:
// a crew of two tasks launched from a ticket's approved plan revision, `build` by the stub worker that asks the operator an approval
// before it commits (STUB_DECISION, with the ticket and its run as evidence) and `docs` by one that asks nothing. `docs` finishes while
// `build` waits; the run page says what build waits for; the session's question bar shows the question with Answer and no reply field
// or number key, on the session page and in the Yard's focused panel, and Answer opens the card; a share link's viewer sees where it is
// answered and no Answer; the ticket, edited after the question and before the card's first read, shows "changed since" in the card,
// the run's chip nothing; the palette's row opens the card; the operator approves there: Recorded, then Applied, "typed into build's
// attempt 1"; the stub writes the line it was typed into stub-answer.txt and build succeeds; a second tab's Approve, picked before
// and confirmed after, is told who answered first. Every wait is on the record through the API or on what the page draws.
test.describe.configure({ mode: 'serial' })

const PROJECT = 'DRA'
const STEP_MS = 15_000
test.use({ actionTimeout: STEP_MS, navigationTimeout: STEP_MS })

let srv: DecisionServer
let ticket = ''
let crewId = ''
let runId = ''

test.beforeAll(async () => {
  srv = await decisionServer('decisions', [{ id: 'worker-decides', from: 'worker', env: { STUB_DECISION: 'approval', STUB_DECISION_EVIDENCE: `ticket:${PROJECT}-1 run:{run}` } }])
  await stopStrays(srv.api, [])
  ticket = await makeTicket(srv.api, PROJECT, 'Ship the stub change')
  crewId = (
    await srv.api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
      name: 'e2e decisions',
      goal: 'ask the operator before committing',
      cwd: '',
      where: 'server',
      isolation: 'worktree',
      openAfterLaunch: false,
      yolo: null,
      members: [
        { name: 'builder', agentId: 'worker-decides', prompt: '', start: { when: 'task' } },
        { name: 'writer', agentId: 'worker', prompt: '', start: { when: 'task' } },
      ],
      tasks: [
        { id: 'build', member: 'builder', kind: 'work', instructions: 'Build it, once the operator approves.' },
        { id: 'docs', member: 'writer', kind: 'work', instructions: 'Document it.' },
      ],
    })
  ).crew.id
})

test.afterAll(async () => {
  if (!srv) return
  if (runId) await srv.api.stopRun(runId)
  await srv.stop()
})

/** Opens the palette with Alt+/ once the layout that listens for it is drawn. */
async function openPalette(page: Page) {
  await expect(page.locator('#dashboard-sidebar-main')).toBeAttached()
  await page.keyboard.press('Alt+/')
  const palette = page.locator('[data-palette]')
  await expect(palette).toBeVisible()
  return palette
}

/** Closes the decision's modal by its own Close button (a page's Escape may be its own: the Yard's leaves the focused panel). */
async function closeCard(page: Page) {
  const dialog = page.getByRole('dialog', { name: /^Decision dr-/ })
  await dialog.getByRole('button', { name: 'Close' }).click()
  await expect(page.locator('[data-decision-modal]')).toHaveCount(0)
}

/** The question bar of a page, with no reply field and no number key, its question the decision's. */
async function barHoldsNoReply(page: Page, question: string) {
  const bar = page.locator('[data-quick-reply]')
  await expect(bar.locator('[data-quick-reply-message]')).toHaveText(question, { timeout: 30_000 })
  await expect(bar.locator('input')).toHaveCount(0)
  await expect(bar.locator('kbd')).toHaveCount(0)
  return bar
}

test("build's approval waits while docs finishes, is answered in its card from the run page, the bar, the palette; Recorded, then Applied", async ({ browser }) => {
  test.slow()
  const api = srv.api
  const base = srv.started.baseURL
  const plan = await propose(api, ticket, crewId)
  await api.ok('POST', `/api/dispatch/plans/${plan}/approve`, { opId: 'dra-ap1', note: 'Go.' })
  runId = (await api.ok<{ launch: { runId: string } }>('POST', `/api/dispatch/tickets/${ticket}/launch`, { opId: 'dra-l1', plan })).launch.runId

  // The decision, as the record says it: opened by build's attempt 1, open.
  budget(120_000)
  let id = ''
  await expect
    .poll(async () => (id = (await decisionsOf(api, runId)).find((d) => d.asker?.task === 'build' && d.state === 'open')?.id ?? ''), { timeout: 120_000, intervals: [500], message: "build's attempt asks its decision" })
    .toMatch(/^dr-[1-9][0-9]*$/)
  expect(id).toBe('dr-1')
  const question = 'May build commit its change?'

  // The ticket edited after the question was asked and before any card read it: its chip says what changed since.
  const before = await storedTicket(api, ticket)
  await api.ok('POST', `/api/dispatch/tickets/${ticket}/edit`, { opId: 'dra-e1', expectedRev: before.rev, set: { description: 'Edited after the question.' } })

  // docs finishes meanwhile; build is held with its words.
  const run = await runUntil(api, runId, (r) => task(r, 'docs').state === 'succeeded', 'docs succeeds while build waits')
  expect(task(run, 'build').state).toBe('running')
  // The session asking it, once its question shows (the stub ended its turn).
  let asking: Session | undefined
  budget(60_000)
  await expect
    .poll(async () => {
      asking = (await api.ok<{ sessions: Session[] }>('GET', '/api/sessions')).sessions.find((s) => s.crew?.runId === runId && s.attention?.state === 'needs_input' && s.attention?.decision === id)
      return !!asking
    }, { timeout: 60_000, intervals: [500], message: "build's session shows the decision's question" })
    .toBe(true)
  const sid = asking!.id

  const page = await operatorPage(browser, srv.started)
  budget(10 * STEP_MS)
  await page.goto(`${base}/runs/${runId}?view=tasks`)
  const row = page.locator('[data-task-row="build"]')
  await expect(row.locator('[data-task-words]')).toHaveText(`attempt 1 waits for decision ${id}`, { timeout: 30_000 })
  await expect(page.locator('[data-task-row="docs"]')).toHaveAttribute('data-task-state', 'succeeded')
  await expect(page.locator('[data-crew-run-header]')).toContainText('1 decision open')

  // The run page's Answer opens the card: the ticket's chip changed since the question (its revision then named), the run's not.
  await row.locator(`[data-task-answer="build"]`).click()
  const modal = page.locator(`[data-decision-modal][data-decision-modal-id="${id}"]`)
  await expect(modal.locator('[data-decision-question]')).toHaveText(question)
  await expect(modal.locator('[data-decision-recommended]')).toHaveCount(1)
  await expect(modal.locator('[data-decision-authorizes]')).toHaveText('build commits stub-builder.txt and submits it')
  const ticketChip = modal.locator(`[data-ref-chip="ticket:${ticket}"]`)
  await expect(ticketChip.locator('[data-ref-changed]')).toHaveText(`changed since r${before.rev}`)
  await expect(modal.locator(`[data-ref-chip="run:${runId}"]`)).toBeVisible()
  await expect(modal.locator(`[data-ref-chip="run:${runId}"] [data-ref-changed]`)).toHaveCount(0)
  await expect(modal.locator('[data-decision-step="open"]')).toHaveAttribute('data-step-status', 'done')
  await expect(modal.locator('[data-decision-step="recorded"]')).toHaveAttribute('data-step-status', 'waiting')
  await closeCard(page)

  // The session page's bar: the question, Answer, no reply field or number key; Answer opens the card.
  budget(6 * STEP_MS)
  await page.goto(`${base}/sessions/${sid}`)
  let bar = await barHoldsNoReply(page, question)
  await bar.locator('[data-quick-reply-answer]').click()
  await expect(modal.locator('[data-decision-question]')).toHaveText(question)
  await closeCard(page)

  // The Yard's focused panel: the same.
  budget(5 * STEP_MS)
  await page.goto(`${base}/yard?focus=${sid}`)
  bar = await barHoldsNoReply(page, question)
  await bar.locator('[data-quick-reply-answer]').click()
  await expect(modal.locator('[data-decision-question]')).toHaveText(question)
  await closeCard(page)

  // A share link's viewer, with control: the question and where it is answered, no Answer, reply field or number key.
  budget(5 * STEP_MS)
  const { token } = await api.ok<{ token: string }>('POST', `/api/sessions/${sid}/links`, { role: 'control' })
  const guestCtx = await browser.newContext({ viewport: { width: 1280, height: 800 } })
  await guestCtx.addInitScript(() => localStorage.setItem('conductor.displayName', 'Ana'))
  const guest = await guestCtx.newPage()
  await guest.goto(`${base}/join/${token}`)
  await guest.getByRole('button', { name: 'Join session' }).click()
  const guestBar = await barHoldsNoReply(guest, question)
  await expect(guestBar.locator('[data-quick-reply-decision]')).toHaveText(`The operator answers ${id} in Conductor`)
  await expect(guestBar.locator('[data-quick-reply-answer]')).toHaveCount(0)
  await expect(guestBar.locator('button')).toHaveCount(0)
  await guestCtx.close()

  // A second tab picks Approve now and confirms it only after the first answered.
  budget(5 * STEP_MS)
  const second = await operatorPage(browser, srv.started, 'Priya')
  await second.goto(`${base}/runs/${runId}?view=tasks&decision=${id}`)
  const secondModal = second.locator(`[data-decision-modal][data-decision-modal-id="${id}"]`)
  await secondModal.locator('[data-decision-choice-label="Approve"]').click()
  await expect(secondModal.locator('[data-decision-confirm]')).toBeVisible()

  // The palette's row opens the card, where the operator approves: Recorded at once, then Applied, typed into build's attempt 1.
  budget(6 * STEP_MS)
  await page.goto(`${base}/`)
  const palette = await openPalette(page)
  const prow = palette.locator(`[data-palette-row="decision:${id}"]`)
  await expect(prow).toContainText(`Answer ${id}: ${question}`)
  // One row for the decision (the reviews of #719): its "Answer" row in Needs you stands for the session's question that mirrors it,
  // so the session is listed among the running, not again in Needs you.
  await expect(prow).toHaveCount(1)
  await expect(palette.locator(`[data-palette-group="needs"] [data-palette-row="session:${sid}"]`)).toHaveCount(0)
  await expect(palette.locator(`[data-palette-group="running"] [data-palette-row="session:${sid}"]`)).toHaveCount(1)
  await prow.click()
  await expect(modal.locator('[data-decision-question]')).toHaveText(question)
  await modal.locator('[data-decision-choice-label="Approve"]').click()
  await modal.locator('[data-decision-rationale]').fill('Small enough.')
  await modal.locator('[data-decision-confirm]').click()
  await expect(modal.locator('[data-decision-receipt]')).toContainText(`Recorded: ${id} answered by Nate`)
  const applied = await decisionUntil(api, id, 'applied')
  expect(applied.applied?.how).toBe('typed')
  await expect(modal.locator('[data-decision-step="applied"]')).toHaveAttribute('data-step-status', 'done', { timeout: 30_000 })
  await expect(modal.locator('[data-decision-step="applied"] [data-step-words]')).toContainText("typed into build's attempt 1")
  await expect(modal.locator('[data-decision-step="recorded"] [data-step-words]')).toContainText(`Recorded: ${id} answered by Nate`)
  await expect(modal.locator('[data-decision-state-words]')).toHaveText("Applied: typed into build's attempt 1")
  // The card says the answer itself: the option picked, why, and who (Codex's round 1 on #719).
  await expect(modal.locator('[data-decision-answer-choice]')).toHaveText('Approve')
  await expect(modal.locator('[data-decision-answer-rationale]')).toHaveText('Small enough.')
  await expect(modal.locator('[data-decision-answer-by]')).toHaveText('Nate')

  // The stub took the typed line as its answer and went on: build succeeds with stub-answer.txt in its result.
  const done = await runUntil(api, runId, (r) => task(r, 'build').state === 'succeeded', 'build succeeds once its answer is typed')
  const commit = task(done, 'build').result?.commit ?? ''
  expect(commit).toMatch(/^[0-9a-f]{40,64}$/)
  const answer = execFileSync('git', ['show', `${commit}:stub-answer.txt`], { cwd: srv.repo, encoding: 'utf8' })
  expect(answer).toContain(`Decision ${id} (approval) answered by Nate: Approve. Why: Small enough. (receipt ${id}/1)`)

  // The second tab confirms its Approve now: told who answered first, nothing applied twice.
  budget(3 * STEP_MS)
  await secondModal.locator('[data-decision-confirm]').click()
  await expect(secondModal.locator('[data-decision-refusal-words]')).toContainText(`${id} was answered by Nate`)
  const after = await api.ok<{ decision: { history: Array<{ kind: string }> } }>('GET', `/api/decisions/${id}`)
  expect(after.decision.history.filter((h) => h.kind === 'applied')).toHaveLength(1)
  await second.context().close()
  await page.context().close()
})
