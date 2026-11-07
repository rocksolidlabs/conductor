import { expect, stopStrays, task, test, type Session } from './fixtures'
import { budget, decision, decisionServer, decisionUntil, decisionsOf, makeTicket, operatorPage, propose, runUntil, type DecisionServer } from './decisions-helpers'

// A ticket's plan revision gating a task (Program item DR1, docs/plans/decisions.md, PR-7: the approve half; the reject half is PR-8's),
// on a server of its own: revision 1 names a decision `ship` before `deploy`; approved and launched through the ticket page's Plan, the
// run opens the gate before any attempt; `build` runs and succeeds while `deploy` waits with its words; the ticket page's Decisions
// lists the gate open; the operator approves it from there; `deploy` is released, starts, and the run succeeds. Every wait is on the
// record through the API or on what the page draws.
test.describe.configure({ mode: 'serial' })

const PROJECT = 'DRG'
const STEP_MS = 15_000
test.use({ actionTimeout: STEP_MS, navigationTimeout: STEP_MS })

let srv: DecisionServer
let ticket = ''
let crewId = ''
let runId = ''

test.beforeAll(async () => {
  srv = await decisionServer('decisions-gate')
  await stopStrays(srv.api, [])
  ticket = await makeTicket(srv.api, PROJECT, 'Deploy the stub build')
  crewId = (
    await srv.api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
      name: 'e2e decisions gate',
      goal: 'build, then deploy once approved',
      cwd: '',
      where: 'server',
      isolation: 'worktree',
      openAfterLaunch: false,
      yolo: null,
      members: [
        { name: 'builder', agentId: 'worker', prompt: '', start: { when: 'task' } },
        { name: 'deployer', agentId: 'worker-cli', prompt: '', start: { when: 'task' } },
      ],
      tasks: [
        { id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.' },
        { id: 'deploy', member: 'deployer', kind: 'work', instructions: 'Deploy it.', needs: ['build'] },
      ],
    })
  ).crew.id
})

test.afterAll(async () => {
  if (!srv) return
  if (runId) await srv.api.stopRun(runId)
  await srv.stop()
})

test("a revision's gate holds deploy until the operator approves it on the ticket page; deploy then runs and the run succeeds", async ({ browser }) => {
  test.slow()
  const api = srv.api
  const base = srv.started.baseURL
  // Proposed through its route, with the gate (the propose form's rows are DS2's, dispatch-plan.spec.ts); approved and launched
  // through the page.
  await propose(api, ticket, crewId, [{ id: 'ship', question: 'May deploy go out?', kind: 'approval', before: ['deploy'] }])

  const page = await operatorPage(browser, srv.started)
  // The pill ] shows lives as long as the test may (PILL_MS is the product's bound, not this test's: palette-reach.spec.ts).
  await page.addInitScript((ms) => ((window as Window & { conductorPillMs?: number }).conductorPillMs = ms), test.info().timeout + 600_000)
  budget(8 * STEP_MS)
  await page.goto(`${base}/dispatch/${PROJECT}/1`)
  await expect(page.locator('[data-ticket-title]')).toHaveText('Deploy the stub build', { timeout: 30_000 })
  const featured = page.locator('[data-plan-section] [data-plan-featured]')
  await expect(featured).toHaveAttribute('data-plan-state', 'proposed')
  await featured.locator('[data-plan-approve]').click()
  const approve = page.locator('[data-plan-approve-dialog]')
  await expect(approve).toHaveAttribute('data-plan-revision', '1')
  await approve.locator('[data-plan-approve-submit]').click()
  await expect(approve).toBeHidden()
  await expect(featured).toHaveAttribute('data-plan-state', 'approved')
  await expect(page.locator('[data-decisions-required] [data-decision-required="ship"]')).toBeVisible()
  await expect(page.locator('[data-decisions-held]')).toContainText('Conductor holds the tasks they name until the operator answers')

  await expect(featured.locator('[data-plan-launch]')).toBeEnabled()
  await featured.locator('[data-plan-launch]').click()
  const launch = page.locator('[data-launch-dialog]')
  await expect(launch).toHaveAttribute('data-launch-revision', '1')
  await launch.locator('[data-launch-confirm]').click()
  // The route answers once the run's first ledger transaction (the gate's opening among it) is committed: the launch's bound, 2 min.
  const link = launch.locator('[data-launch-run-link]')
  budget(130_000)
  await expect(link).toBeVisible({ timeout: 130_000 })
  runId = (await link.getAttribute('data-launch-run-link')) ?? ''
  expect(runId).toMatch(new RegExp(`^${crewId}-[0-9a-f]{8}$`))
  await page.keyboard.press('Escape')

  // The gate was opened with the run: dr-1, the plan's, open, holding deploy.
  const gates = await decisionsOf(api, runId)
  expect(gates.map((d) => [d.id, d.origin, d.gate, d.state])).toEqual([['dr-1', 'plan', 'ship', 'open']])
  const id = gates[0]!.id

  // build runs and succeeds; deploy, its need met, waits for the gate with its words.
  const run = await runUntil(api, runId, (r) => task(r, 'build').state === 'succeeded', 'build succeeds')
  expect(task(run, 'deploy').attempts).toBe(0)
  budget(5 * STEP_MS)
  await page.goto(`${base}/runs/${runId}?view=tasks`)
  await expect(page.locator('[data-task-row="deploy"] [data-task-words]')).toHaveText(`waits for decision ${id}`, { timeout: 30_000 })
  await expect(page.locator('[data-task-row="deploy"] [data-task-answer="deploy"]')).toBeVisible()
  expect((await api.run(runId)).tasks!.find((t) => t.id === 'deploy')!.attempts).toBe(0)

  // Needs you with a session's question waiting beside the gate (the reviews of #719): ] steps from the session's pill to the gate's,
  // which stays shown, and Enter opens its card; in the sidebar, the gate's row is one a key lands on (↓ from the session's row, or
  // the focus moved there), and Enter on it opens its card, not the session before it.
  budget(16 * STEP_MS)
  const asker = await api.ok<Session>('POST', '/api/sessions', { agentId: 'asker', name: 'e2e gate asker' })
  await expect.poll(async () => (await api.session(asker.id)).attention?.state, { timeout: 30_000, intervals: [250] }).toBe('needs_input')
  const sideRow = page.locator(`[data-session-list="sidebar"] [data-sidebar-row="s:${asker.id}"]`).first()
  await expect(sideRow).toHaveAttribute('data-row-state', 'needs', { timeout: 30_000 })
  const gateRow = page.locator(`[data-session-list="sidebar"] [data-sidebar-row="d:${id}"]`)
  await expect(gateRow).toBeVisible({ timeout: 30_000 })
  const card = page.locator(`[data-decision-modal][data-decision-modal-id="${id}"]`)
  const closeCard = async () => {
    await page.getByRole('dialog', { name: /^Decision dr-/ }).getByRole('button', { name: 'Close' }).click()
    await expect(page.locator('[data-decision-modal]')).toHaveCount(0)
  }
  // Each pill by what it shows: the one it replaces leaves through its transition while the next comes in.
  const sessionPill = page.locator(`[data-needs-pill][data-pill-session="${asker.id}"]`)
  const gatePill = page.locator(`[data-needs-pill][data-pill-decision="${id}"]`)
  await page.keyboard.press(']')
  await expect(sessionPill.locator('[data-pill-count]')).toHaveText('1 of 2')
  await page.keyboard.press(']')
  await expect(gatePill.locator('[data-pill-count]')).toHaveText('2 of 2')
  await expect(gatePill.locator('[data-pill-message]')).toHaveText('May deploy go out?')
  await expect(sessionPill).toHaveCount(0)
  // Still the gate's once the session's has gone: nothing of the session it stepped from hid it.
  await expect(gatePill).toBeVisible()
  await page.keyboard.press('Enter')
  await expect(card.locator('[data-decision-question]')).toHaveText('May deploy go out?')
  await expect(page.locator('[data-needs-pill]')).toHaveCount(0)
  await closeCard()
  const gateButton = gateRow.locator(`[data-sidebar-decision="${id}"]`)
  await sideRow.locator('a[href]').first().focus()
  await page.keyboard.press('ArrowDown')
  await expect(gateButton).toBeFocused()
  await page.keyboard.press('Enter')
  await expect(card.locator('[data-decision-question]')).toHaveText('May deploy go out?')
  await closeCard()
  await sideRow.locator('a[href]').first().focus()
  await gateButton.focus()
  await page.keyboard.press('Enter')
  await expect(card.locator('[data-decision-question]')).toHaveText('May deploy go out?')
  await expect(page).toHaveURL(new RegExp(`/runs/${runId}\\?view=tasks$`))
  await closeCard()
  await api.stopSession(asker.id)

  // The ticket page's Decisions lists the gate open, and its required decision names it; Answer opens the card, where it is approved.
  budget(8 * STEP_MS)
  await page.goto(`${base}/dispatch/${PROJECT}/1`)
  const record = page.locator(`[data-ticket-decisions] [data-ticket-decision-record="${id}"]`)
  await expect(record).toHaveAttribute('data-decision-state', 'open', { timeout: 30_000 })
  await expect(page.locator(`[data-decision-required="ship"] [data-decision-required-record="${id}"]`)).toContainText(`${id} open`)
  await record.locator(`[data-ticket-decision-open="${id}"]`).click()
  const modal = page.locator(`[data-decision-modal][data-decision-modal-id="${id}"]`)
  await expect(modal.locator('[data-decision-question]')).toHaveText('May deploy go out?')
  await expect(modal.locator('[data-decision-more]')).toBeDisabled()
  await expect(modal.locator('[data-decision-more-why]')).toHaveText(`${id} is the plan's: no agent asked it, comment instead`)
  await modal.locator('[data-decision-choice-label="Approve"]').click()
  await modal.locator('[data-decision-confirm]').click()
  await expect(modal.locator('[data-decision-receipt]')).toContainText(`Recorded: ${id} answered by Nate`)
  const applied = await decisionUntil(api, id, 'applied')
  expect(applied.applied).toMatchObject({ how: 'released', tasks: ['deploy'] })
  await expect(modal.locator('[data-decision-state-words]')).toHaveText('Applied: released deploy', { timeout: 30_000 })
  await page.getByRole('dialog', { name: /^Decision dr-/ }).getByRole('button', { name: 'Close' }).click()
  await expect(record).toHaveAttribute('data-decision-state', 'applied', { timeout: 30_000 })

  // deploy is released: it runs and succeeds, and so does the run.
  const done = await runUntil(api, runId, (r) => r.outcome === 'succeeded', 'the run succeeds once deploy is released')
  expect(task(done, 'deploy').state).toBe('succeeded')
  expect((await decision(api, id)).history?.filter((h) => h.kind === 'applied')).toHaveLength(1)
  await page.context().close()
})
