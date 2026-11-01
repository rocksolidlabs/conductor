import type { BrowserContext } from '@playwright/test'
import { expect, stopStrays, test, type Run } from './fixtures'
import { freeProjectKey, makeProject, makeTicket, mark, marked, opOf, secondBrowser } from './dispatch-helpers'

// The run Tasks view inside a ticket and the run page's chip (docs/plans/dispatch.md, PR-11): a managed run launched (tasks.spec.ts's
// crew, the stub as its workers), linked to a ticket by the ticket page's link form; the ticket page's Runs section shows the run's task
// rows, the run page's own (RunTasks.vue); the run page's chip names the ticket and opens it; a second ticket's link of the same run is
// refused with the first one named; a second browser on the run page, opened before any link, gains the chip when the first links the
// run, loses it when the first removes the link, and says the ticket deleted when it is, all with no reload. The spec makes a project of
// its own (LNK, LNK1… when the server holds the earlier ones) and ends every stray session before it launches.
test.describe.configure({ mode: 'serial' })

let crewId = ''
let run: Run | null = null
let other: BrowserContext | null = null

const crewBody = {
  name: 'e2e dispatch links',
  goal: 'a run linked to a ticket',
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
    { id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.', checks: [{ id: 'fixed', argv: ['git', 'cat-file', '-e', 'HEAD:stub-fixed.txt'] }] },
    { id: 'docs', member: 'writer', kind: 'work', instructions: 'Document it.', needs: ['build'] },
  ],
}

test.beforeAll(async ({ api }, testInfo) => {
  // The run's two tasks take a few attempts' time: its own bound, above the poll's.
  testInfo.setTimeout(200_000)
  await stopStrays(api, [])
  crewId = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', crewBody)).crew.id
  run = await api.launchCrew(crewId)
  const id = run.id
  await expect
    .poll(async () => (await api.run(id)).outcome ?? '', { timeout: 180_000, intervals: [500], message: 'the run succeeds' })
    .toBe('succeeded')
})

test.afterEach(async () => {
  await other?.close()
  other = null
})

test.afterAll(async ({ api }) => {
  if (run) await api.stopRun(run.id)
  if (crewId) await api.call('DELETE', `/api/crews/${encodeURIComponent(crewId)}`)
})

test("a run linked from a ticket shows its task rows there, the run page's chip names the ticket and opens it, and a second browser's chip follows the link with no reload", async ({ page, api, state, browser }) => {
  const id = run!.id
  const key = await freeProjectKey(api, 'LNK')
  await makeProject(api, key)
  const first = await makeTicket(api, key, '1', { title: 'Build the thing' })
  const second = await makeTicket(api, key, '2', { title: 'Another thing' })

  // A second browser on the run page, before any link: no chip.
  other = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const watcher = await secondBrowser(other, state.token)
  await watcher.goto(`/runs/${encodeURIComponent(id)}`)
  await expect(watcher.locator('[data-run-name]')).toHaveText(crewBody.name)
  await expect(watcher.locator('[data-run-ticket]')).toHaveCount(0)
  await mark(watcher)

  // The ticket page: no run yet; the run linked by the form, its id typed.
  await page.goto(`/dispatch/${key}/1`)
  const runs = page.locator('[data-ticket-runs]')
  await expect(runs).toHaveAttribute('data-count', '0')
  await runs.locator('[data-run-link-open]').click()
  await runs.locator('[data-run-link-pick]').click()
  await page.getByPlaceholder('A run, or its id…').fill(id)
  await page.getByRole('option', { name: `Link the run ${id}` }).click()
  await expect(runs).toHaveAttribute('data-count', '1')

  // Its Runs section: the run, and the run page's own task rows with their states.
  const linked = runs.locator(`[data-ticket-run="${id}"]`)
  await expect(linked.locator('[data-ticket-run-open]')).toHaveText(crewBody.name)
  await expect(linked.locator('[data-task-row]')).toHaveCount(2)
  await expect(linked.locator('[data-task-row="build"]')).toHaveAttribute('data-task-state', 'succeeded')
  await expect(linked.locator('[data-task-row="docs"]')).toHaveAttribute('data-task-state', 'succeeded')
  await expect(linked.locator('[data-task-row="build"] [data-task-attempt]')).toHaveText('attempt 2')
  await expect(linked.locator('[data-ticket-run-badge]').first()).toHaveText('2/2 tasks')
  // A task opened in place: its attempts, as on the run page.
  await linked.locator('[data-task-row="build"] [role="button"]').click()
  await expect(linked.locator('[data-task-detail-id="build"] [data-task-attempt-row]')).toHaveCount(2)

  // The second browser's run page gained the chip, with no reload.
  const chip = watcher.locator('[data-run-ticket]')
  await expect(chip).toHaveAttribute('data-run-ticket-key', first)
  await expect(chip).toContainText(first)
  await expect(chip).not.toHaveAttribute('data-run-ticket-deleted')
  expect(await marked(watcher)).toBe('kept')

  // The same run linked to the second ticket: refused, the first one named.
  await page.goto(`/dispatch/${key}/2`)
  const runs2 = page.locator('[data-ticket-runs]')
  await runs2.locator('[data-run-link-open]').click()
  await runs2.locator('[data-run-link-pick]').click()
  await page.getByPlaceholder('A run, or its id…').fill(id)
  await page.getByRole('option', { name: `Link the run ${id}` }).click()
  await expect(runs2.locator('[data-run-link-error]')).toContainText(`The run is linked to ${first} already`)
  await expect(runs2).toHaveAttribute('data-count', '0')
  const two = await api.ok<{ ticket: { links: unknown[] } }>('GET', `/api/dispatch/tickets/${second}`)
  expect(two.ticket.links).toEqual([])

  // The run page's chip names the ticket and opens its page.
  await page.goto(`/runs/${encodeURIComponent(id)}`)
  await expect(page.locator('[data-run-ticket]')).toHaveAttribute('data-run-ticket-key', first)
  await page.locator('[data-run-ticket]').click()
  await expect(page).toHaveURL(new RegExp(`/dispatch/${key}/1$`))
  await expect(page.locator('[data-ticket-key]')).toHaveText(first)

  // The link removed on the ticket page: the second browser's chip goes, with no reload.
  await page.locator(`[data-ticket-run="${id}"] [data-ticket-run-remove]`).click()
  await expect(page.locator('[data-ticket-runs]')).toHaveAttribute('data-count', '0')
  await expect(chip).toHaveCount(0)
  expect(await marked(watcher)).toBe('kept')

  // Linked again, and its ticket deleted, by another person: the chip comes back, then says the ticket is deleted.
  await api.ok('POST', `/api/dispatch/tickets/${first}/links`, { opId: opOf(key, 'run2'), kind: 'run', ref: id })
  await expect(chip).toHaveAttribute('data-run-ticket-key', first)
  await api.ok('POST', `/api/dispatch/tickets/${first}/delete`, { opId: opOf(key, 'del1') })
  await expect(chip).toHaveAttribute('data-run-ticket-deleted', '')
  await expect(chip).toContainText(`${first} (deleted)`)
  expect(await marked(watcher)).toBe('kept')
})
