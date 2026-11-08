import type { Locator, Page } from '@playwright/test'
import { expect, stopStrays, task, test } from './fixtures'
import { budget, decision, decisionServer, decisionUntil, decisionsOf, makeTicket, operatorPage, runUntil, type DecisionServer } from './decisions-helpers'

// Whom a decision is for (docs/plans/decisions.md, HM1, PR-12), on a server of its own: a crew whose architecture owner is nate and
// whose acceptance owner is ana; a plan revision proposed through the ticket page with an approval `schema` for the architecture owner
// before `build` and an assignment `migrate` for ana before `docs` (the For select and the Assignment kind), then, the crew given a
// security reviewer meanwhile, revised through the page with only its note changed: the reopened dialog reads the crew again, its For
// select offering the new holder, and the revision read through the API still holds both `for`s; approved and launched; the gates hold
// both tasks; the card says the operator answers for nate, the architecture owner; the operator approves and the receipt says for whom;
// `build` starts; ana's page lists her assignment until it is answered Done, and `docs` then starts. Every wait is on the record
// through the API or on what the page draws.
test.describe.configure({ mode: 'serial' })

const PROJECT = 'DRP'
const STEP_MS = 15_000
test.use({ actionTimeout: STEP_MS, navigationTimeout: STEP_MS })

let srv: DecisionServer
let ticket = ''
let crewId = ''
let runId = ''

const crewBody = {
  name: 'e2e decisions people',
  goal: 'change the schema, once its owner approves',
  cwd: '',
  where: 'server',
  isolation: 'worktree',
  openAfterLaunch: false,
  yolo: null,
  members: [
    { name: 'builder', agentId: 'worker', prompt: '', start: { when: 'task' } },
    { name: 'writer', agentId: 'worker-cli', prompt: '', start: { when: 'task' } },
  ],
  tasks: [
    { id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.' },
    { id: 'docs', member: 'writer', kind: 'work', instructions: 'Write the docs.' },
  ],
  people: [
    { name: 'nate', responsibilities: ['architecture'] },
    { name: 'ana', responsibilities: ['acceptance'] },
  ],
}

test.beforeAll(async () => {
  srv = await decisionServer('decisions-people')
  await stopStrays(srv.api, [])
  ticket = await makeTicket(srv.api, PROJECT, 'Change the schema')
  crewId = (await srv.api.ok<{ crew: { id: string } }>('POST', '/api/crews', crewBody)).crew.id
})

test.afterAll(async () => {
  if (!srv) return
  if (runId) await srv.api.stopRun(runId)
  await srv.stop()
})

/**
 * Picks the option of a select (a USelect or a USelectMenu) whose words are `option` exactly (the crew's, which carry a description
 * after its name, by what they start with), and waits for its menu to be gone before anything else.
 */
async function pick(page: Page, select: Locator, option: string, multiple = false, prefix = false) {
  await select.click()
  const words = option.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  await page.getByRole('option').filter({ hasText: new RegExp(`^\\s*${words}${prefix ? '' : '\\s*$'}`) }).click()
  if (multiple) await page.keyboard.press('Escape')
  await expect(page.getByRole('listbox')).toHaveCount(0)
}

interface PlanBodyRead {
  plan: { body: { decisions: Array<{ id: string; kind: string; before: string[]; for?: string }>; note: string } }
}

test('a gate for the architecture owner and an assignment for ana: proposed, revised, approved and launched through the page, each answered for its person', async ({ browser }) => {
  test.slow()
  const api = srv.api
  const base = srv.started.baseURL
  const page = await operatorPage(browser, srv.started)
  budget(17 * STEP_MS)
  await page.goto(`${base}/dispatch/${PROJECT}/1`)
  await expect(page.locator('[data-ticket-title]')).toHaveText('Change the schema', { timeout: 30_000 })
  const plan = page.locator('[data-plan-section]')
  await expect(plan.locator('[data-plan-empty]')).toBeVisible()

  // Revision 1 through the propose form: the crew, then two decisions, each with its kind, its question, its task and whom it is for.
  await plan.locator('[data-plan-propose]').click()
  const propose = page.locator('[data-plan-propose-dialog]')
  await expect(propose).toBeVisible()
  await pick(page, propose.locator('[data-plan-propose-crew]'), 'e2e decisions people', false, true)
  await expect(propose.locator('[data-plan-propose-crew]')).toContainText('e2e decisions people')
  const ids = propose.getByLabel("The decision's id")
  const kinds = propose.getByLabel('Its kind')
  const questions = propose.getByLabel('The question')
  const befores = propose.getByLabel('The tasks that wait on it')
  // The For select offers the crew's people once the crew is read.
  const rows: Array<{ id: string; kind?: string; question: string; before: string; for: string }> = [
    { id: 'schema', question: 'Is the schema right?', before: 'build', for: 'architecture owner: nate' },
    { id: 'migrate', kind: 'Assignment', question: 'Run the migration on staging', before: 'docs', for: 'ana' },
  ]
  for (const [i, r] of rows.entries()) {
    await propose.locator('[data-plan-propose-decision-add]').click()
    await expect(ids).toHaveCount(i + 1)
    await ids.nth(i).fill(r.id)
    await expect(ids.nth(i)).toHaveValue(r.id)
    if (r.kind) await pick(page, kinds.nth(i), r.kind)
    await questions.nth(i).fill(r.question)
    await expect(questions.nth(i)).toHaveValue(r.question)
    await pick(page, befores.nth(i), r.before, true)
    await pick(page, propose.locator(`[data-plan-propose-decision-for="${i}"]`), r.for)
    await expect(propose.locator(`[data-plan-propose-decision-for="${i}"]`)).toContainText(r.for)
  }
  await expect(kinds.nth(1)).toContainText('Assignment')
  await propose.locator('[data-plan-propose-submit]').click()
  await expect(propose).toBeHidden()
  const featured = plan.locator('[data-plan-featured]')
  await expect(featured).toHaveAttribute('data-plan-revision', '1')
  await expect(featured.locator('[data-plan-decision="schema"] [data-plan-decision-for]')).toHaveText('· for nate (architecture owner)')
  await expect(featured.locator('[data-plan-decision="migrate"] [data-plan-decision-for]')).toHaveText('· for ana')
  const fors = (b: PlanBodyRead) => b.plan.body.decisions.map((d) => [d.id, d.kind, d.before, d.for])
  const want = [
    ['schema', 'approval', ['build'], 'role:architecture'],
    ['migrate', 'assignment', ['docs'], 'person:ana'],
  ]
  const listed = async () => (await api.ok<{ plans: Array<{ id: string; n: number }> }>('GET', `/api/dispatch/tickets/${ticket}/plans`)).plans
  const r1 = (await listed()).find((p) => p.n === 1)!.id
  expect(fors(await api.ok<PlanBodyRead>('GET', `/api/dispatch/plans/${r1}`))).toEqual(want)

  // The crew gains a security reviewer while the dialog is closed (as another tab's crew editor would give it one): the same crew,
  // reopened, is read again, so its For select offers lee, whom the people read when it was first picked did not hold.
  await api.ok('PUT', `/api/crews/${encodeURIComponent(crewId)}`, { ...crewBody, people: [...crewBody.people, { name: 'lee', responsibilities: ['security'] }] })

  // Revised through the page with only its note: the form is filled from revision 1, every decision whole, both fors kept.
  await featured.locator('[data-plan-revise]').click()
  await expect(propose.locator('[data-plan-propose-crew]')).toContainText('e2e decisions people')
  await expect(propose.locator('[data-plan-propose-decision-for="0"]')).toContainText('architecture owner: nate')
  await expect(propose.locator('[data-plan-propose-decision-for="1"]')).toContainText('ana')
  await propose.locator('[data-plan-propose-decision-for="0"]').click()
  await expect(page.getByRole('option').filter({ hasText: /^\s*security reviewer: lee\s*$/ })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await expect(propose).toBeVisible()
  await expect(propose.locator('[data-plan-propose-decision-for="0"]')).toContainText('architecture owner: nate')
  const note = propose.locator('[data-plan-propose-note]')
  await note.fill('Only the note changes.')
  await expect(note).toHaveValue('Only the note changes.')
  await propose.locator('[data-plan-propose-submit]').click()
  await expect(propose).toBeHidden()
  await expect(featured).toHaveAttribute('data-plan-revision', '2')
  const r2 = (await listed()).find((p) => p.n === 2)!.id
  const body2 = await api.ok<PlanBodyRead>('GET', `/api/dispatch/plans/${r2}`)
  expect(fors(body2)).toEqual(want)
  expect(body2.plan.body.note).toBe('Only the note changes.')

  // Approved and launched through the page.
  await featured.locator('[data-plan-approve]').click()
  const approve = page.locator('[data-plan-approve-dialog]')
  await expect(approve).toHaveAttribute('data-plan-revision', '2')
  await approve.locator('[data-plan-approve-submit]').click()
  await expect(approve).toBeHidden()
  await expect(featured).toHaveAttribute('data-plan-state', 'approved')
  await expect(featured.locator('[data-plan-launch]')).toBeEnabled()
  await featured.locator('[data-plan-launch]').click()
  const launch = page.locator('[data-launch-dialog]')
  await expect(launch).toHaveAttribute('data-launch-revision', '2')
  await launch.locator('[data-launch-confirm]').click()
  // The route answers once the run's first ledger transaction (the gates' openings among it) is committed: the launch's bound, 2 min.
  const link = launch.locator('[data-launch-run-link]')
  budget(130_000)
  await expect(link).toBeVisible({ timeout: 130_000 })
  runId = (await link.getAttribute('data-launch-run-link')) ?? ''
  expect(runId).toMatch(new RegExp(`^${crewId}-[0-9a-f]{8}$`))
  await page.keyboard.press('Escape')

  // Two gates, opened with the run, each for its person; both tasks held, neither started.
  const gates = await decisionsOf(api, runId)
  const gateOf = (g: string) => gates.find((d) => d.gate === g)!
  expect(gates.map((d) => [d.gate, d.type, d.state]).sort()).toEqual([
    ['migrate', 'assignment', 'open'],
    ['schema', 'approval', 'open'],
  ])
  const forOf = async (id: string) => ((await api.ok<{ decision: { request: { for?: unknown } } }>('GET', `/api/decisions/${id}`)).decision.request.for)
  expect(await forOf(gateOf('schema').id)).toEqual({ label: 'role:architecture', person: 'nate', responsibility: 'architecture' })
  expect(await forOf(gateOf('migrate').id)).toEqual({ label: 'person:ana', person: 'ana' })
  const schema = gateOf('schema').id
  const migrate = gateOf('migrate').id
  const held = await api.run(runId)
  expect([task(held, 'build').attempts, task(held, 'docs').attempts]).toEqual([0, 0])

  // The run page's Answer on build: the card says the operator answers for nate, the architecture owner; the confirm says for whom;
  // the receipt says by whom and for whom.
  budget(8 * STEP_MS)
  await page.goto(`${base}/runs/${runId}?view=tasks`)
  const row = page.locator('[data-task-row="build"]')
  await expect(row.locator('[data-task-words]')).toHaveText(`waits for decision ${schema}`, { timeout: 30_000 })
  await row.locator('[data-task-answer="build"]').click()
  const modal = page.locator(`[data-decision-modal][data-decision-modal-id="${schema}"]`)
  await expect(modal.locator('[data-decision-question]')).toHaveText('Is the schema right?')
  await expect(modal.locator('[data-decision-approver]')).toHaveText('the operator, for nate (architecture owner)')
  await modal.locator('[data-decision-choice-label="Approve"]').click()
  await expect(modal.locator('[data-decision-confirm-step] [data-decision-answer-as]')).toHaveText('You answer as the operator, for nate.')
  await modal.locator('[data-decision-confirm]').click()
  await expect(modal.locator('[data-decision-receipt]')).toContainText(`Recorded: ${schema} answered by Nate for nate, the architecture owner`)
  const applied = await decisionUntil(api, schema, 'applied')
  expect(applied.applied).toMatchObject({ how: 'released', tasks: ['build'] })
  await expect(modal.locator('[data-decision-answer-for]')).toHaveText('nate, the architecture owner', { timeout: 30_000 })
  await page.getByRole('dialog', { name: /^Decision dr-/ }).getByRole('button', { name: 'Close' }).click()
  await expect(page.locator('[data-decision-modal]')).toHaveCount(0)
  const started = await runUntil(api, runId, (r) => task(r, 'build').attempts > 0, 'build starts once its gate is approved')
  expect(task(started, 'docs').attempts).toBe(0)
  const kept = (await api.ok<{ decision: { responses: Array<{ actor: string; byName?: string; for?: unknown }> } }>('GET', `/api/decisions/${schema}`)).decision.responses
  expect(kept.map((r) => [r.actor, r.byName, r.for])).toEqual([['admin', 'Nate', { person: 'nate', responsibility: 'architecture' }]])

  // ana's page lists her assignment, with what she is to do, until it is answered Done; docs then starts.
  budget(8 * STEP_MS)
  await page.goto(`${base}/people/ana`)
  const item = page.locator(`[data-person-decisions] [data-person-decision="${migrate}"]`)
  await expect(item).toBeVisible({ timeout: 30_000 })
  await expect(item.locator('[data-decision-assignment]')).toHaveText('ana is to do this by hand; the operator answers Done or Not done for them.')
  await expect(page.locator(`[data-person-decisions] [data-person-decision="${schema}"]`)).toHaveCount(0)
  await item.locator(`[data-person-decision-open="${migrate}"]`).click()
  const card = page.locator(`[data-decision-modal][data-decision-modal-id="${migrate}"]`)
  await expect(card.locator('[data-decision-question]')).toHaveText('Run the migration on staging')
  await card.locator('[data-decision-choice-label="Done"]').click()
  await expect(card.locator('[data-decision-confirm-step] [data-decision-answer-as]')).toHaveText('You answer as the operator, for ana.')
  await card.locator('[data-decision-confirm]').click()
  await expect(card.locator('[data-decision-receipt]')).toContainText(`Recorded: ${migrate} answered by Nate for ana`)
  expect((await decisionUntil(api, migrate, 'applied')).applied).toMatchObject({ how: 'released', tasks: ['docs'] })
  await page.getByRole('dialog', { name: /^Decision dr-/ }).getByRole('button', { name: 'Close' }).click()
  await expect(page.locator('[data-decision-modal]')).toHaveCount(0)
  // The page reads itself again on the decision's event: the assignment is gone from it.
  await expect(item).toHaveCount(0, { timeout: 30_000 })
  await expect(page.locator('[data-person-decisions] [data-person-decisions-none]')).toBeVisible()
  await runUntil(api, runId, (r) => task(r, 'docs').attempts > 0, 'docs starts once ana is answered Done')
  expect((await decision(api, migrate)).history?.filter((h) => h.kind === 'applied')).toHaveLength(1)
  await page.context().close()
})
