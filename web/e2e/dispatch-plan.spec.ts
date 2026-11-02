import { execFileSync } from 'node:child_process'
import type { BrowserContext } from '@playwright/test'
import { expect, test, type Api } from './fixtures'
import { freeProjectKey, makeProject, makeTicket, mark, marked, opOf, secondBrowser, sideways, storedTicket } from './dispatch-helpers'

// A ticket's Plan (Program item DS2, docs/plans/dispatch-evidence.md, PR-10), only what PR-10 and the PRs before it build: a managed crew
// of stub workers, a ticket with an attachment; revision 1 proposed (its base shown as the commit HEAD was, each task's assignee shown)
// and approved with a note; Launch, asked first, answers with the run started and its link, which opens the run page; a second browser
// sees the approval and the launch with no reload; the title edited: Launch is offered no more, with Approve again; approved again,
// launched again: a second launch with another run id. The ticket is found by its key and the runs by their ids; every wait is on the
// state the page or the store must reach.
test.describe.configure({ mode: 'serial' })

let crewId = ''
let crewName = ''
const runs: string[] = []
let other: BrowserContext | undefined

test.beforeAll(async ({ api }) => {
  crewName = `e2e plan ${Date.now().toString(36)}`
  crewId = (
    await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
      name: crewName,
      goal: 'prove the plan',
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
        { id: 'docs', member: 'writer', kind: 'work', instructions: 'Document it.', needs: ['build'] },
      ],
    })
  ).crew.id
})

test.afterAll(async ({ api }) => {
  await other?.close()
  for (const id of runs) await api.stopRun(id)
  if (crewId) await api.call('DELETE', `/api/crews/${encodeURIComponent(crewId)}`)
})

const git = (cwd: string, ...args: string[]) => execFileSync('git', args, { cwd, encoding: 'utf8' }).trim()

/** A file attached to the ticket through the upload route, as the browser sends it: the op id, then the file. */
async function attach(api: Api, key: string, opId: string, name: string, text: string): Promise<void> {
  const form = new FormData()
  form.append('opId', opId)
  form.append('file', new Blob([text], { type: 'text/plain' }), name)
  const r = await fetch(`${api.server.baseURL}/api/dispatch/tickets/${key}/attachments`, { method: 'POST', headers: { Authorization: `Bearer ${api.server.token}` }, body: form })
  expect(r.status, await r.clone().text()).toBe(201)
}

test("a revision proposed from a crew, approved with a note and launched; a second browser sees both; an edit of the title waits for Approve again; launched again under another run id", async ({ page, api, state, browser }) => {
  // Two launches, each answering once its run's first ledger transaction is committed, and some twenty steps between them.
  test.slow()
  const key = await freeProjectKey(api, 'PL')
  await makeProject(api, key)
  const ticketKey = await makeTicket(api, key, '1', { title: 'Plan the login loop', description: 'It loops after the token expires.', acceptance: 'It stops looping.' })
  await attach(api, ticketKey, opOf(key, 'a1'), 'spec.txt', 'what the loop does\n')
  const head = git(state.repo, 'rev-parse', 'HEAD')

  await page.addInitScript(() => localStorage.setItem('conductor.displayName', 'Nate'))
  await page.goto(`/dispatch/${key}/1`)
  await expect(page.locator('[data-ticket-title]')).toHaveText('Plan the login loop', { timeout: 30_000 })
  const plan = page.locator('[data-plan-section]')
  await expect(plan.locator('[data-plan-empty]')).toBeVisible()

  // A second browser on the same ticket, kept open: what it shows later comes from the live events.
  other = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const second = await secondBrowser(other, state.token)
  await second.goto(`/dispatch/${key}/1`)
  await expect(second.locator('[data-plan-section] [data-plan-empty]')).toBeVisible({ timeout: 30_000 })
  await mark(second)

  // Revision 1, proposed from the crew with the attachment handed over and a note.
  await plan.locator('[data-plan-propose]').click()
  const propose = page.locator('[data-plan-propose-dialog]')
  await expect(propose).toBeVisible()
  await propose.locator('[data-plan-propose-crew]').click()
  await page.getByRole('option').filter({ hasText: crewName }).click()
  await propose.getByRole('checkbox', { name: 'spec.txt' }).click()
  await propose.locator('[data-plan-propose-note]').fill('The two-task crew.')
  await propose.locator('[data-plan-propose-submit]').click()
  await expect(propose).toBeHidden()
  const featured = plan.locator('[data-plan-featured]')
  await expect(featured).toHaveAttribute('data-plan-revision', '1')
  await expect(featured).toHaveAttribute('data-plan-state', 'proposed')
  // Its base is the commit HEAD was, shown as a commit; each task with its assignee; what is handed over.
  await expect(featured.locator('[data-plan-base]')).toHaveAttribute('data-plan-base', head)
  await expect(featured.locator('[data-plan-base]')).toHaveText(head.slice(0, 12))
  await expect(featured.locator('[data-plan-task="build"] [data-plan-task-assignee]')).toHaveText('builder')
  await expect(featured.locator('[data-plan-task="docs"] [data-plan-task-assignee]')).toHaveText('writer')
  await expect(featured.locator('[data-plan-member="builder"]')).toHaveAttribute('data-plan-member-agent', 'worker')
  await expect(featured.locator('[data-plan-attachment]')).toHaveText('spec.txt')
  await expect(featured.locator('[data-plan-note]')).toHaveText('The two-task crew.')
  const { plans } = await api.ok<{ plans: Array<{ id: string; n: number; base: string; state: string }> }>('GET', `/api/dispatch/tickets/${ticketKey}/plans`)
  expect(plans.map((p) => [p.n, p.base, p.state])).toEqual([[1, head, 'proposed']])
  const yolo = (await api.ok<{ plan: { body: { crew: { yolo?: boolean } } } }>('GET', `/api/dispatch/plans/${plans[0]!.id}`)).plan.body.crew.yolo
  await expect(featured.locator('[data-plan-yolo]')).toHaveAttribute('data-plan-yolo', yolo ? 'on' : 'off')

  // Approved with a note: the operator's approval, on the page and in the store.
  await featured.locator('[data-plan-approve]').click()
  const approve = page.locator('[data-plan-approve-dialog]')
  await expect(approve).toHaveAttribute('data-plan-revision', '1')
  await approve.locator('[data-plan-approve-note]').fill('Small enough to ship today.')
  await approve.locator('[data-plan-approve-submit]').click()
  await expect(approve).toBeHidden()
  await expect(featured).toHaveAttribute('data-plan-state', 'approved')
  await expect(featured.locator('[data-plan-approval-note]')).toHaveText('Small enough to ship today.')
  await expect(page.locator('[data-ticket-history] [data-history-line]').filter({ hasText: 'approved plan revision 1' })).toHaveCount(1)
  // The second browser sees the approval, with no reload.
  await expect(second.locator('[data-plan-featured]')).toHaveAttribute('data-plan-state', 'approved')
  await expect(second.locator('[data-plan-featured] [data-plan-approval-note]')).toHaveText('Small enough to ship today.')

  // Launch, asked first: the revision, the commit, the yolo choice; then the run started, with its link.
  await expect(featured.locator('[data-plan-launch]')).toBeEnabled()
  await featured.locator('[data-plan-launch]').click()
  const launch = page.locator('[data-launch-dialog]')
  await expect(launch).toHaveAttribute('data-launch-revision', '1')
  await expect(launch.locator('[data-launch-base]')).toHaveText(head.slice(0, 12))
  await expect(launch.locator('[data-launch-yolo]')).toHaveAttribute('data-launch-yolo', yolo ? 'on' : 'off')
  await expect(launch.locator('[data-launch-label]')).toHaveAttribute('placeholder', `${ticketKey} Plan the login loop`)
  await launch.locator('[data-launch-confirm]').click()
  // The route answers once the run's first ledger transaction is committed and the launch recorded: the launch's own bound, 2 minutes.
  const link = launch.locator('[data-launch-run-link]')
  await expect(link).toBeVisible({ timeout: 130_000 })
  const run1 = (await link.getAttribute('data-launch-run-link')) ?? ''
  runs.push(run1)
  expect(run1).toMatch(new RegExp(`^${crewId}-[0-9a-f]{8}$`))
  await expect(launch).toHaveAttribute('data-launch-state', 'started')
  const launched = await api.run(run1)
  expect(launched.managed, 'launched as a managed run').toBeTruthy()
  expect((launched as unknown as { ticket?: { key: string } }).ticket?.key).toBe(ticketKey)
  expect((launched as unknown as { label?: string }).label).toBe(`${ticketKey} Plan the login loop`)

  // The second browser sees the launch, its run started, with no reload.
  await expect(second.locator(`[data-plan-launch-row][data-launch-run="${run1}"]`)).toHaveAttribute('data-launch-state', 'started')
  expect(await marked(second)).toBe('kept')

  // The link opens the run page main has.
  await link.click()
  await expect(page).toHaveURL(new RegExp(`/runs/${run1}$`))
  await expect(page.locator('[data-crew-run-header]')).toBeVisible({ timeout: 30_000 })

  // The title edited (another person's edit, through the API): the approval no longer holds, and Launch is offered no more, with Approve again.
  const before = await storedTicket(api, ticketKey)
  await api.ok('POST', `/api/dispatch/tickets/${ticketKey}/edit`, { opId: opOf(key, 'title2'), expectedRev: before.rev, set: { title: 'Plan the login loop, twice' } })
  await page.goto(`/dispatch/${key}/1`)
  await expect(page.locator('[data-ticket-title]')).toHaveText('Plan the login loop, twice', { timeout: 30_000 })
  await expect(featured).toHaveAttribute('data-plan-stale', 'approve')
  await expect(featured.locator('[data-plan-reason="stale"]')).toHaveAttribute('data-plan-remedy', 'approve')
  await expect(featured.locator('[data-plan-launch]')).toBeDisabled()
  await expect(featured.locator('[data-plan-blockers]')).toContainText('approve it again')
  await expect(second.locator('[data-plan-featured]')).toHaveAttribute('data-plan-stale', 'approve')
  // The run launched before the edit keeps what it was given: its label is the title as launched.
  expect((await api.run(run1) as unknown as { label?: string }).label).toBe(`${ticketKey} Plan the login loop`)

  // Approved again, launched again: a second launch, under another run id.
  await featured.locator('[data-plan-approve-again]').click()
  await expect(approve).toHaveAttribute('data-plan-revision', '1')
  await approve.locator('[data-plan-approve-submit]').click()
  await expect(approve).toBeHidden()
  await expect(featured).not.toHaveAttribute('data-plan-stale', /.+/)
  await expect(featured.locator('[data-plan-launch]')).toBeEnabled()
  await featured.locator('[data-plan-launch]').click()
  await expect(launch).toHaveAttribute('data-launch-state', 'asking')
  await launch.locator('[data-launch-confirm]').click()
  await expect(link).toBeVisible({ timeout: 130_000 })
  const run2 = (await link.getAttribute('data-launch-run-link')) ?? ''
  runs.push(run2)
  expect(run2).toMatch(new RegExp(`^${crewId}-[0-9a-f]{8}$`))
  expect(run2).not.toBe(run1)
  await page.keyboard.press('Escape')
  await expect(launch).toBeHidden()
  await expect(plan.locator('[data-plan-launch-row]')).toHaveCount(2)
  await expect(plan.locator(`[data-plan-launch-row][data-launch-run="${run2}"]`)).toHaveAttribute('data-launch-state', 'started')
  await expect(second.locator(`[data-plan-launch-row][data-launch-run="${run2}"]`)).toHaveAttribute('data-launch-state', 'started')
  expect((await storedTicket(api, ticketKey)).rev).toBeGreaterThan(before.rev)
  const { ticket } = await api.ok<{ ticket: { launchCount: number } }>('GET', `/api/dispatch/tickets/${ticketKey}`)
  expect(ticket.launchCount).toBe(2)
})

test('a phone shows the Plan in one column, nothing wider than the screen', async ({ page, api }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const key = await freeProjectKey(api, 'PLP')
  await makeProject(api, key)
  const ticketKey = await makeTicket(api, key, '1', { title: 'On a phone' })
  await api.ok('POST', `/api/dispatch/tickets/${ticketKey}/plans`, { opId: opOf(key, 'r1'), crew: crewId })
  await page.goto(`/dispatch/${key}/1`)
  const featured = page.locator('[data-plan-section] [data-plan-featured]')
  await expect(featured).toHaveAttribute('data-plan-state', 'proposed', { timeout: 30_000 })
  await expect(featured.locator('[data-plan-task="build"]')).toBeVisible()
  expect(await sideways(page)).toBeLessThanOrEqual(0)
  const box = await featured.boundingBox()
  expect(box && box.x >= 0 && box.x + box.width <= 390).toBeTruthy()
})
