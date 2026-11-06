import { existsSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import type { Page } from '@playwright/test'
import { expect, task, test, type Run, type Session } from './fixtures'
import { freeProjectKey, makeProject, makeTicket, opOf } from './dispatch-helpers'

// People in a crew (docs/plans/decisions.md, HM1, PR-10): the crew editor's People section and Owner column, the run page's people and
// owners, the approved revision's people. A crew given an architecture owner (nate, who owns build) and an acceptance owner (ana, who owns
// docs by the fallback) is saved through the editor and read back after a reload; the second holder of a responsibility is refused in the
// editor (the option held by another row is disabled, a person named as a member blocks Save) and by the server. Launched from a ticket's
// approved plan revision with the stub workers: the run header's people, each task row's owner and the fallback's words, the sessions of
// the run counted by its id equal to its attempts (none for a person, and no worktree under a person's name), and the ticket's People
// listing the revision's people. Every crew, ticket and run is found by its id or key, never by a list's length.
test.describe.configure({ mode: 'serial' })

let crewId = ''
const runs: string[] = []

const crewBody = {
  name: `e2e people ${Date.now().toString(36)}`,
  goal: 'prove the people',
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
}

interface PeopleCrew {
  people?: Array<{ name: string; responsibilities?: string[]; note?: string }>
  tasks?: Array<{ id: string; owner?: string }>
}

test.beforeAll(async ({ api }) => {
  crewId = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', crewBody)).crew.id
})

test.afterAll(async ({ api }) => {
  for (const id of runs) await api.stopRun(id)
  if (crewId) await api.call('DELETE', `/api/crews/${encodeURIComponent(crewId)}`)
})

/**
 * Raises the test's timeout by a long wait's cap as the wait starts, so that the waits of one test, each within its own cap, never add up
 * to more than the test is given (AGENTS.md, "Tests that do not flake").
 */
function budget(ms: number) {
  const info = test.info()
  info.setTimeout(info.timeout + ms)
}

/** Picks a name in a person row's name picker: the name Conductor knows, or the typed one; the menu is gone before the next field. */
async function pickName(page: Page, row: number, name: string) {
  await page.locator('[data-crew-person]').nth(row).locator('[data-person-name-picker]').click()
  await page.getByPlaceholder('A name…').fill(name)
  await page.getByRole('option', { name: new RegExp(`^(Name them )?${name}$`) }).click()
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await expect(page.locator('[data-crew-person]').nth(row)).toHaveAttribute('data-person-name', name)
}

/** Gives a person row a responsibility by its words; the menu, which stays open for more, is closed before going on. */
async function hold(page: Page, row: number, words: string) {
  await page.locator('[data-crew-person]').nth(row).locator('[data-person-responsibilities]').click()
  await page.getByRole('option', { name: words, exact: true }).click()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('listbox')).toHaveCount(0)
}

test('a crew given an architecture owner and an acceptance owner is saved and read back; a second holder is refused in the editor and by the server', async ({ page, api }) => {
  await page.goto(`/crews/${encodeURIComponent(crewId)}`)
  // The first check after the navigation waits out the page's first paint (#281).
  budget(30_000)
  await expect(page.locator('[data-crew-people-section]')).toBeVisible({ timeout: 30_000 })
  await expect(page.locator('[data-people-never-started]')).toHaveText('A person is never started: no session, worktree or agent. They own decisions and outcomes.')
  await expect(page.locator('[data-crew-person]')).toHaveCount(0)

  // nate, the architecture owner.
  await page.locator('[data-add-person]').click()
  await pickName(page, 0, 'nate')
  await hold(page, 0, 'architecture owner')

  // A second person named as a member: the editor refuses it under the row and at Save.
  const save = page.locator('[data-unsaved-bar]').getByRole('button', { name: 'Save' })
  await page.locator('[data-add-person]').click()
  await pickName(page, 1, 'builder')
  await expect(page.locator('[data-crew-person]').nth(1).locator('[data-person-error]')).toContainText("builder is a member's name")
  await expect(save).toBeDisabled()
  await expect(save).toHaveAttribute('data-save-problem', /builder is a member's name/)

  // ana instead, the acceptance owner: the architecture owner nate holds is offered to her disabled, with its holder.
  await pickName(page, 1, 'ana')
  await expect(page.locator('[data-crew-person]').nth(1).locator('[data-person-error]')).toHaveCount(0)
  await page.locator('[data-crew-person]').nth(1).locator('[data-person-responsibilities]').click()
  await expect(page.getByRole('option', { name: 'architecture owner · held by nate' })).toHaveAttribute('data-disabled', '')
  await page.getByRole('option', { name: 'acceptance owner', exact: true }).click()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('listbox')).toHaveCount(0)

  // build is nate's; docs names no owner, so the "none" says whose it is then.
  const build = page.locator('[data-crew-task][data-task-id="build"]')
  const docs = page.locator('[data-crew-task][data-task-id="docs"]')
  await expect(docs.locator('[data-task-owner]')).toContainText('none: ana, the acceptance owner')
  await build.locator('[data-task-owner]').click()
  await page.getByRole('option', { name: 'nate', exact: true }).click()
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await expect(build.locator('[data-task-owner]')).toContainText('nate')

  await expect(save).toBeEnabled()
  await save.click()
  await expect(page.locator('[data-unsaved-bar]')).toHaveCount(0)
  const saved = (await api.crew(crewId)) as unknown as PeopleCrew
  expect(saved.people).toEqual([
    { name: 'nate', responsibilities: ['architecture'] },
    { name: 'ana', responsibilities: ['acceptance'] },
  ])
  expect(saved.tasks?.map((t) => [t.id, t.owner])).toEqual([
    ['build', 'nate'],
    ['docs', undefined],
  ])

  // Read back after a reload.
  await page.reload()
  budget(30_000)
  await expect(page.locator('[data-crew-person]')).toHaveCount(2, { timeout: 30_000 })
  await expect(page.locator('[data-crew-person]').nth(0)).toHaveAttribute('data-person-name', 'nate')
  await expect(page.locator('[data-crew-person]').nth(0).locator('[data-person-responsibilities]')).toContainText('architecture owner')
  await expect(page.locator('[data-crew-person]').nth(1)).toHaveAttribute('data-person-name', 'ana')
  await expect(page.locator('[data-crew-person]').nth(1).locator('[data-person-responsibilities]')).toContainText('acceptance owner')
  await expect(build.locator('[data-task-owner]')).toContainText('nate')
  await expect(docs.locator('[data-task-owner]')).toContainText('none: ana, the acceptance owner')
  await expect(page.locator('[data-unsaved-bar]')).toHaveCount(0)

  // The server refuses a second holder of the acceptance owner, whatever the editor offers, and keeps the crew as it was.
  const { createdAt: _c, updatedAt: _u, id: _i, manifestRevision: _m, ...body } = (await api.ok<{ crew: Record<string, unknown> }>('GET', `/api/crews/${encodeURIComponent(crewId)}`)).crew
  const twice = { ...body, people: [{ name: 'nate', responsibilities: ['architecture', 'acceptance'] }, { name: 'ana', responsibilities: ['acceptance'] }] }
  const refused = await api.call<{ error: { code: string; message: string } }>('PUT', `/api/crews/${encodeURIComponent(crewId)}`, twice)
  expect(refused.status).toBe(400)
  expect(refused.body.error.code).toBe('invalid_crew')
  expect(refused.body.error.message).toContain('acceptance is nate\'s already; one person of a crew is its acceptance owner')
  expect(((await api.crew(crewId)) as unknown as PeopleCrew).people).toEqual(saved.people)
})

test("launched from the approved revision: the run's people and owners, a session per attempt and none for a person, the revision's people", async ({ page, api, state }) => {
  const project = await freeProjectKey(api, 'PPL')
  await makeProject(api, project)
  const ticketKey = await makeTicket(api, project, '1', { title: 'Ship it with its people' })
  const proposed = await api.ok<{ plan: { id: string; n: number } }>('POST', `/api/dispatch/tickets/${ticketKey}/plans`, { opId: opOf(project, 'r1'), crew: crewId })
  await api.ok('POST', `/api/dispatch/plans/${proposed.plan.id}/approve`, { opId: opOf(project, 'ap1') })
  const launched = await api.ok<{ launch: { runId: string; state: string } }>('POST', `/api/dispatch/tickets/${ticketKey}/launch`, { opId: opOf(project, 'l1'), plan: proposed.plan.id })
  expect(launched.launch.state).toBe('started')
  const runId = launched.launch.runId
  runs.push(runId)

  budget(120_000)
  let run: Run = await api.run(runId)
  await expect.poll(async () => (run = await api.run(runId)).outcome ?? '', { timeout: 120_000, intervals: [500], message: 'the run succeeds' }).toBe('succeeded')
  expect(run.people).toEqual([
    { name: 'nate', responsibilities: ['architecture'] },
    { name: 'ana', responsibilities: ['acceptance'] },
  ])
  expect(run.tasks?.map((t) => [t.owner, t.ownerFrom])).toEqual([
    ['nate', 'task'],
    ['ana', 'acceptance'],
  ])

  // The run's sessions, found by its id, are its attempts: none was made for a person, and no worktree is named for one.
  const attempts = task(run, 'build').attempts + task(run, 'docs').attempts
  const listed = await api.ok<{ sessions?: Session[] } | Session[]>('GET', '/api/sessions')
  const sessions = (Array.isArray(listed) ? listed : (listed.sessions ?? [])).filter((s) => s.crew?.runId === runId)
  expect(sessions).toHaveLength(attempts)
  expect(sessions.map((s) => s.crew?.member).sort()).toEqual(['builder', 'writer'])
  const trees = join(state.repo, '.conductor', 'worktrees', runId)
  const names = existsSync(trees) ? readdirSync(trees) : []
  // An attempt's worktree is <task>.<n>, the checkout its checks ran in <task>.<n>.check.
  expect(names.filter((n) => !/^(build|docs)\.\d+(\.check)?$/.test(n))).toEqual([])

  // The run page: the people under the bar, each task row's owner, the fallback's words in the row and the detail.
  await page.goto(`/runs/${encodeURIComponent(runId)}?view=tasks`)
  budget(30_000)
  await expect(page.locator('[data-run-people]')).toBeVisible({ timeout: 30_000 })
  await expect(page.locator('[data-run-people] [data-run-person="nate"]')).toContainText('architecture owner')
  await expect(page.locator('[data-run-people] [data-run-person="ana"]')).toContainText('acceptance owner')
  await expect(page.locator('[data-task-row="build"] [data-task-owner]')).toHaveText('owner nate')
  await expect(page.locator('[data-task-row="build"] [data-task-owner]')).toHaveAttribute('data-task-owner-from', 'task')
  await expect(page.locator('[data-task-row="docs"] [data-task-owner]')).toHaveText('owner ana, the acceptance owner')
  await page.locator('[data-task-row="docs"] [role="button"]').click()
  await expect(page.locator('[data-task-detail-id="docs"] [data-task-detail-owner]')).toContainText('owner ana, the acceptance owner')

  // The ticket's People: the approved revision's people with their responsibilities.
  await page.goto(`/dispatch/${ticketKey.replace('-', '/')}`)
  budget(30_000)
  const people = page.locator('[data-ticket-people] [data-people-plan-people]')
  await expect(people).toContainText(`as r${proposed.plan.n} was approved`, { timeout: 30_000 })
  await expect(people.locator('[data-people-plan-person="nate"]')).toHaveAttribute('data-responsibilities', 'architecture')
  await expect(people.locator('[data-people-plan-person="ana"]')).toContainText('acceptance owner')
})
