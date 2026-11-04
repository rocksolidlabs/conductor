import type { Page } from '@playwright/test'
import { expect, task, test, type Api, type Run } from './fixtures'
import { freeProjectKey, makeProject, makeTicket, opOf, storedTicket } from './dispatch-helpers'

// A delivery's candidate, verified, the decisions and the people (Program item DS2, docs/plans/dispatch-evidence.md, PR-12): in a project
// whose close policy requires the owner's acceptance, a delivery and `verified`, a ticket launched from its approved plan revision (the
// approval with a note), whose crew's one check passes: a delivery naming the final candidate, picked in the link form from the ticket's
// runs' standing results, is verified, and the ticket closes once the owner's acceptance is recorded, not before; the approval's note
// and the acceptance under Decisions; the owner, the approver and launcher, the plan's agent with its command and the launch's agent
// with the version its probe reported under People. A second ticket whose check always fails, its result accepted anyway: the delivery
// naming it is not verified, with the words, the close refused in them, the Accept anyway under Decisions. Every ticket is found by its
// key and every run by its id; every wait is on the state the ledger, the store or the page must reach, never on the clock.
test.describe.configure({ mode: 'serial' })

const crews: string[] = []
const runs: string[] = []
let project = ''

/** A crew of one stub worker whose one check runs the argv. */
async function crewOf(api: Api, name: string, check: { id: string; argv: string[] }): Promise<string> {
  const id = (
    await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
      name: `${name} ${Date.now().toString(36)}`,
      goal: 'deliver a candidate',
      cwd: '',
      where: 'server',
      isolation: 'worktree',
      openAfterLaunch: false,
      yolo: null,
      members: [{ name: 'builder', agentId: 'worker', prompt: '', start: { when: 'task' } }],
      tasks: [{ id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.', checks: [check] }],
    })
  ).crew.id
  crews.push(id)
  return id
}

test.beforeAll(async ({ api }) => {
  project = await freeProjectKey(api, 'DLV')
  await makeProject(api, project, ['acceptance', 'delivery', 'verified'])
})

test.afterAll(async ({ api }) => {
  for (const id of runs) await api.stopRun(id)
  for (const id of crews) await api.call('DELETE', `/api/crews/${encodeURIComponent(id)}`)
})

interface Launch {
  id: string
  runId: string
  state: string
  agents: Array<{ member: string; agent: string; version: string }>
}

/** Revision 1 of the ticket proposed from the crew, approved with the note, and launched, through the routes the Plan section calls. */
async function launchFrom(api: Api, ticketKey: string, crew: string, note: string): Promise<{ plan: string; launch: Launch }> {
  const tag = ticketKey.toLowerCase()
  const proposed = await api.ok<{ plan: { id: string } }>('POST', `/api/dispatch/tickets/${ticketKey}/plans`, { opId: `${tag}-r1`, crew })
  await api.ok('POST', `/api/dispatch/plans/${proposed.plan.id}/approve`, { opId: `${tag}-ap1`, note })
  const launched = await api.ok<{ launch: Launch }>('POST', `/api/dispatch/tickets/${ticketKey}/launch`, { opId: `${tag}-l1`, plan: proposed.plan.id })
  expect(launched.launch.state).toBe('started')
  runs.push(launched.launch.runId)
  return { plan: proposed.plan.id, launch: launched.launch }
}

/**
 * Raises the test's timeout by a long wait's cap as the wait starts, so that the waits of one test, each within its own cap, never add up
 * to more than the test is given (AGENTS.md, "Tests that do not flake"): the config's timeout is left for the rest.
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

/** The build task's candidate at the attempt, as the candidates route answers it from the ledger. */
async function commitOf(api: Api, run: string, attempt: number): Promise<string> {
  const r = await api.ok<{ candidates: Array<{ task: string; candidates: Array<{ attempt: number; commit?: string }> }> }>('GET', `/api/runs/${encodeURIComponent(run)}/candidates`)
  return r.candidates.find((t) => t.task === 'build')?.candidates.find((c) => c.attempt === attempt)?.commit ?? ''
}

/** A delivery link added on the ticket page, naming the candidate picked by its name (build#n) in the form's select. */
async function deliver(page: Page, url: string, name: string): Promise<void> {
  const links = page.locator('[data-ticket-links]')
  await links.locator('[data-link-add-open]').click()
  const form = links.locator('[data-link-form]')
  await form.locator('[data-link-url-input]').fill(url)
  await form.locator('[data-link-candidate-pick]').click()
  await page.getByRole('option', { name: new RegExp(`^${name} · `) }).click()
  await form.locator('[data-link-submit]').click()
  await expect(form).toHaveCount(0)
}

const item = (page: Page, requirement: string) => page.locator(`[data-close-policy] [data-close-item="${requirement}"]`)

test("a delivery naming the final candidate is verified, the ticket closes once accepted; the decisions and the people", async ({ page, api }) => {
  const crew = await crewOf(api, 'e2e delivery', { id: 'smoke', argv: ['sh', '-c', 'exit 0'] })
  const ticketKey = await makeTicket(api, project, '1', { title: 'Ship the login fix', acceptance: 'It stops looping.', owner: 'Alice' })
  const note = 'Go with the small fix.'
  const { plan, launch } = await launchFrom(api, ticketKey, crew, note)
  const run = launch.runId
  await runUntil(api, run, (r) => task(r, 'build').state === 'succeeded', 'build succeeds')
  const commit = await commitOf(api, run, 1)
  expect(commit).toMatch(/^[0-9a-f]{40,64}$/)

  await page.goto(`/dispatch/${ticketKey.replace('-', '/')}`)
  budget(30_000)
  await expect(page.locator('[data-ticket-title]')).toHaveText('Ship the login fix', { timeout: 30_000 })
  await expect(page.locator('[data-close-policy] [data-close-summary]')).toHaveText("To close: the owner's acceptance · a delivery link · a verified delivery")
  await expect(item(page, 'verified')).not.toHaveAttribute('data-met')
  await expect(item(page, 'verified').locator('[data-close-state]')).toHaveText('No delivery names a candidate')

  // The delivery names the final candidate, picked from the run's standing results: verified, from the receipt the link recorded.
  await deliver(page, 'https://github.com/acme/api/pull/12', 'build#1')
  const row = page.locator('[data-ticket-links] [data-link-kind="delivery"]')
  await expect(row.locator('[data-link-candidate]')).toHaveAttribute('data-candidate', `${run}/build/1@${commit}`)
  await expect(row.locator('[data-link-candidate]')).toHaveAttribute('data-link-verified', 'verified')
  await expect(row.locator('[data-link-verification]')).toHaveText('Verified: build#1 passed its check')
  await expect(item(page, 'verified')).toHaveAttribute('data-met', '')
  await expect(item(page, 'verified').locator('[data-close-state]')).toHaveText('Verified: build#1 passed its check')
  await expect(item(page, 'delivery')).toHaveAttribute('data-met', '')

  // Close is refused until the owner's acceptance is recorded, then done.
  const card = page.locator('[data-close-policy]')
  await card.locator('[data-close]').click()
  await expect(card.locator('[data-close-refused]')).toContainText("It needs the owner's acceptance to be done.")
  expect((await storedTicket(api, ticketKey)).status).not.toBe('done')
  await card.locator('[data-accept]').click()
  await expect(card).toHaveAttribute('data-accepted', 'met')
  await card.locator('[data-close]').click()
  budget(30_000)
  await expect.poll(async () => (await storedTicket(api, ticketKey)).status, { timeout: 30_000, message: 'the ticket is done' }).toBe('done')
  await expect(card).toHaveAttribute('data-close-closed', '')

  // Decisions: the approval with its note, the acceptance.
  const decisions = page.locator('[data-ticket-decisions]')
  const approval = decisions.locator(`[data-decision="approval:${plan}"]`)
  await expect(approval.locator('[data-decision-title]')).toContainText('Approved plan r1')
  await expect(approval.locator('[data-decision-note]')).toHaveText(note)
  await expect(decisions.locator('[data-decision="acceptance"] [data-decision-title]')).toHaveText('Accepted for Alice')

  // People: the owner, the approver and launcher, the plan's agent with its command, the launch's agent with the version it reported.
  const people = page.locator('[data-ticket-people]')
  await expect(people.locator('[data-person="Alice"]')).toHaveAttribute('data-person-roles', 'owner')
  await expect(people.locator('[data-person="admin"]')).toHaveAttribute('data-person-roles', /approver launcher/)
  await expect(people.locator('[data-people-plan-agent="builder"]')).toHaveAttribute('data-agent', 'worker')
  await expect(people.locator('[data-people-plan-agent="builder"] [data-people-plan-argv]')).not.toBeEmpty()
  const recorded = (await api.ok<{ launch: Launch }>('GET', `/api/dispatch/launches/${launch.id}`)).launch.agents
  expect(recorded.map((a) => [a.member, a.agent])).toEqual([['builder', 'worker']])
  const agent = people.locator(`[data-people-launch-agent="${launch.id}/builder"]`)
  await expect(agent).toHaveAttribute('data-agent', 'worker')
  await expect(agent).toHaveAttribute('data-version', recorded[0]!.version)
  await expect(agent.locator('[data-people-version]')).toHaveText(recorded[0]!.version || 'no version reported')
})

test('a candidate accepted anyway is not verified, with the words; the close refused in them; the Accept anyway under Decisions', async ({ page, api }) => {
  const crew = await crewOf(api, 'e2e delivery anyway', { id: 'never', argv: ['sh', '-c', 'exit 1'] })
  const ticketKey = await makeTicket(api, project, '2', { title: 'Ship it anyway', owner: 'Alice' })
  const { launch } = await launchFrom(api, ticketKey, crew, 'Try it.')
  const run = launch.runId
  await runUntil(api, run, (r) => task(r, 'build').state === 'failed' && task(r, 'build').attempts === 3, 'build fails its check three times')
  await api.ok('POST', `/api/runs/${encodeURIComponent(run)}/tasks/build/accept`, { commandId: opOf(project, 'anyway') })
  await runUntil(api, run, (r) => task(r, 'build').state === 'succeeded', "build's attempt 3 accepted anyway")
  const commit = await commitOf(api, run, 3)

  await page.goto(`/dispatch/${ticketKey.replace('-', '/')}`)
  budget(30_000)
  await expect(page.locator('[data-ticket-title]')).toHaveText('Ship it anyway', { timeout: 30_000 })
  await deliver(page, 'https://github.com/acme/api/pull/13', 'build#3')
  const row = page.locator('[data-ticket-links] [data-link-kind="delivery"]')
  await expect(row.locator('[data-link-candidate]')).toHaveAttribute('data-candidate', `${run}/build/3@${commit}`)
  await expect(row.locator('[data-link-candidate]')).toHaveAttribute('data-link-verified', 'unverified')
  await expect(row.locator('[data-link-verification]')).toHaveText('Not verified: build#3 failed the check never (exit 1)')
  await expect(item(page, 'delivery')).toHaveAttribute('data-met', '')
  await expect(item(page, 'verified')).not.toHaveAttribute('data-met')
  await expect(item(page, 'verified').locator('[data-close-state]')).toHaveText('The delivered candidate build#3 failed the check never (exit 1)')

  // Accepted by the owner too: the close is still refused, for the verified delivery it lacks.
  const card = page.locator('[data-close-policy]')
  await card.locator('[data-accept]').click()
  await expect(card).toHaveAttribute('data-accepted', 'met')
  await card.locator('[data-close]').click()
  await expect(card.locator('[data-close-refused]')).toContainText('a verified delivery')
  expect((await storedTicket(api, ticketKey)).status).not.toBe('done')

  // Decisions: the Accept anyway, of the run, by the operator.
  const anyway = page.locator(`[data-ticket-decisions] [data-decision="anyway:${run}/build/3"]`)
  await expect(anyway).toHaveAttribute('data-decision-kind', 'accepted-anyway')
  await expect(anyway.locator('[data-decision-title]')).toHaveText('Accepted build#3 anyway')
  await expect(anyway.locator('[data-decision-state]')).toContainText('1 of 1 failed: check never (exit 1)')
})
