import { expect, git, logged, member, sidebarLinks, test, type Run } from './fixtures'

// The example crews, run by the stub agent (web/e2e/stub-agent.sh) in place of Claude Code and Codex: the todo app through the
// workbench, every step of it checked where a person would see it and through the API, then the other three examples launched.
// The tests share the server and the run, so they run in order.
test.describe.configure({ mode: 'serial' })

const MEMBERS = ['lead', 'core', 'cli', 'tester']
let runId = ''
const runs: string[] = []

test.afterAll(async ({ api }) => {
  for (const id of runs) await api.stopRun(id)
})

test('the examples load on the empty Crews page, at the scratch repository', async ({ page, api, state }) => {
  await page.goto('/crews')
  await page.locator('[data-crews-empty]').getByRole('button', { name: 'Load the examples' }).click()
  // Loading opens the first example; the home lists all five, each by its id.
  await expect(page).toHaveURL(/\/crews\/example-todo-app$/)
  await page.goto('/crews')
  for (const id of ['example-todo-app', 'example-todo-app-tasks', 'example-test-fixer', 'example-docs-writer', 'example-dependency-upgrade']) {
    await expect(page.locator(`[data-crew-entry][data-crew-id="${id}"] [data-crew-item]`)).toBeVisible()
    // The examples work in the server's default working directory: the scratch repository.
    expect((await api.crew(id)).cwd).toBe(state.repo)
  }
  await expect(page.locator('[data-crew-entry][data-crew-id="example-todo-app-tasks"] [data-crew-item]')).toHaveText('Example: todo app, with tasks')
  // Its members start by their tasks, never "all at launch".
  await expect(page.locator('[data-crew-entry][data-crew-id="example-todo-app-tasks"] [data-crew-shape]')).toHaveText('4 · by tasks')
})

test('the todo app with tasks opens in the editor coordinated by tasks, its five tasks as saved and none refused', async ({ page }) => {
  await page.goto('/crews/example-todo-app-tasks')
  await expect(page.locator('[data-crew-coordination]')).toContainText('by tasks')
  await expect(page.locator('[data-crew-tasks-section]')).toBeVisible()
  const tasks = page.locator('[data-crew-task]')
  await expect(tasks).toHaveCount(5)
  expect(await tasks.evaluateAll((rows) => rows.map((r) => r.getAttribute('data-task-id')))).toEqual(['plan', 'core', 'cli', 'tests', 'review'])
  await expect(tasks.nth(3).locator('[data-task-needs]')).toContainText('core')
  await expect(tasks.nth(3).locator('[data-task-needs]')).toContainText('cli')
  // The editor's own check of the tasks (taskProblems) agrees with the server's: nothing to fix.
  await expect(page.locator('[data-task-error]')).toHaveCount(0)
  await expect(page.locator('[data-member-start]')).toHaveCount(0)
})

test('the todo app launches and opens its run page, a tile per member', async ({ page }) => {
  await page.goto('/crews/example-todo-app')
  await page.locator('[data-launch]').click()
  // A launch asks first, each member on its line (docs/plans/capacity.md, PR-7); its Launch launches.
  await page.locator('[data-crew-launch-confirm]').click()
  await page.waitForURL(/\/runs\/example-todo-app-[0-9a-f]{8}$/)
  runId = decodeURIComponent(new URL(page.url()).pathname.split('/').pop() ?? '')
  runs.push(runId)
  const grid = page.locator('[data-run-grid]')
  for (const name of MEMBERS) await expect(grid.locator(`[data-member="${name}"]`)).toBeVisible()
  // Every member gets a live tile, the tester last: it starts once cli is done.
  await expect(grid.locator('[data-member="tester"] [data-session-tile]')).toBeVisible({ timeout: 60_000 })
})

// The chain from the launch to the handoff runs three starts one after another (lead; core and cli; tester) and the tester's own
// wait for core's commit, each with a bound of its own in the mechanism: a start types its prompt once the member's screen has been
// quiet, or after readyCap (60 s, internal/crew/run.go) at the latest, and the stub waits STUB_MERGE_WAIT_S (60 s) for a branch it
// is told to wait for. The chain takes about five seconds on CI; its bound is the sum of those (#281: the 90 s it had was no
// mechanism's, and a machine loaded by another suite beside this one ran past it).
const HANDOFF_BOUND = 3 * 60_000 + 60_000

test('each prompt runs without a person, the members start in order, the handoff is delivered and the merges succeed', async ({ page, api, state }) => {
  test.setTimeout(HANDOFF_BOUND + 120_000)
  let run: Run = await api.run(runId)
  await expect
    .poll(async () => {
      run = await api.run(runId)
      return ['typed lead', 'lead is done: starting core, cli', 'cli is done: starting tester', 'handoff delivered from tester to core'].filter((t) => !logged(run, t))
    }, { timeout: HANDOFF_BOUND, message: 'run log' })
    .toEqual([])
  for (const name of MEMBERS) expect(logged(run, `typed ${name}'s prompt`), `${name}'s prompt`).toBe(true)
  const at = (name: string) => Date.parse(member(run, name).startedAt ?? '')
  expect(at('lead')).toBeLessThan(at('core'))
  expect(at('lead')).toBeLessThan(at('cli'))
  expect(at('cli')).toBeLessThan(at('tester'))
  // Each stub ran its prompt (no one pressed Enter): it merged the branches its prompt names and committed on its own. A branch
  // holds a member's file only through a merge that went through.
  const holds = (branch: string, name: string) => {
    try {
      git(state.repo, 'cat-file', '-e', `crew/${runId}/${branch}:stub-${name}.txt`)
      return true
    } catch {
      return false
    }
  }
  for (const [branch, names] of [['lead', ['lead']], ['core', ['lead', 'core']], ['cli', ['lead', 'cli']], ['tester', ['lead', 'cli', 'core', 'tester']]] as const) {
    for (const name of names) await expect.poll(() => holds(branch, name), { timeout: 60_000, message: `crew/${runId}/${branch} holds stub-${name}.txt` }).toBe(true)
  }
  // core answered the tester's handoff, typed into it as a line of its own.
  await expect
    .poll(() => git(state.repo, 'show', `crew/${runId}/core:stub-core.txt`), { timeout: 30_000 })
    .toContain('Handoff from tester: stub tester: over to core')
  // The feed shows the same steps.
  await page.goto(`/runs/${encodeURIComponent(runId)}`)
  const feed = page.locator('[data-crew-feed]')
  for (const text of ["typed lead's prompt", 'lead is done: starting core, cli', 'cli is done: starting tester', 'handoff delivered from tester to core']) {
    await expect(feed).toContainText(text)
  }
})

test('the Crews home shows the run as running now, and the crew\'s Runs tab lists it with each member and its status', async ({ page }) => {
  await page.goto('/crews')
  const card = page.locator(`[data-running-now] [data-run="${runId}"]`)
  await expect(card).toBeVisible()
  await expect(card).toHaveAttribute('data-state', 'running')
  await expect(card).toContainText('Example: todo app')
  await expect(card).toContainText('run started')
  // The saved crew's row says a run is live, and says nothing of running itself.
  const entry = page.locator('[data-crew-entry][data-crew-id="example-todo-app"]')
  await expect(entry.locator('[data-crew-live]')).toHaveText(/1 live run/)
  await expect(entry.locator('[data-crew-shape]')).toHaveText('4 · a tree')
  await page.goto('/crews/example-todo-app')
  await expect(page.locator('[data-live-run-banner]')).toContainText('live run')
  await page.getByRole('tab', { name: /Runs/ }).click()
  const row = page.locator(`[data-crew-runs] [data-run="${runId}"]`)
  await expect(row).toBeVisible()
  await expect(row).toHaveAttribute('data-state', 'running')
  for (const name of MEMBERS) await expect(row.locator(`[data-run-member="${name}"]`)).toHaveAttribute('data-status', 'running')
  await expect(row.getByRole('link', { name: /open/i })).toHaveAttribute('href', `/runs/${encodeURIComponent(runId)}`)
})

test('the full sidebar groups the run: its header, then its four members', async ({ page, api }) => {
  await page.goto('/crews')
  const run = await api.run(runId)
  const sessions = new Set(run.members.map((m) => `/sessions/${m.sessionId}`))
  await expect.poll(async () => (await sidebarLinks(page)).filter((h) => sessions.has(h)).length).toBe(4)
  const links = await sidebarLinks(page)
  const header = links.indexOf(`/runs/${encodeURIComponent(runId)}`)
  expect(header, 'the run header').toBeGreaterThanOrEqual(0)
  expect(new Set(links.slice(header + 1, header + 5))).toEqual(sessions)
})

test('typing into a tile on the Yard reaches the session', async ({ page, api }) => {
  const lead = member(await api.run(runId), 'lead').sessionId ?? ''
  await page.goto('/yard')
  const tile = page.locator('[data-session-tile]').filter({ has: page.getByText('lead', { exact: true }) })
  await expect(tile).toHaveCount(1)
  await tile.locator('.xterm-screen').click()
  await page.keyboard.type('ping-from-yard')
  await page.keyboard.press('Enter')
  // The click focused the terminal in place: no navigation, and the stub got the line.
  await expect(page).toHaveURL(/\/yard$/)
  await expect.poll(async () => (await api.session(lead)).attention?.message ?? '', { timeout: 30_000 }).toContain('got: ping-from-yard')
})

for (const [crew, first, second] of [
  ['example-test-fixer', 'triage', 'fixer'],
  ['example-docs-writer', 'reader', 'writer'],
  ['example-dependency-upgrade', 'scout', 'upgrader'],
] as const) {
  test(`${crew} launches: ${first}, then ${second} on ${first}'s branch`, async ({ api, state }) => {
    const id = (await api.launchCrew(crew)).id
    runs.push(id)
    await expect
      .poll(async () => {
        const run = await api.run(id)
        return [`typed ${first}'s prompt`, `${first} is done: starting ${second}`, `typed ${second}'s prompt`].filter((t) => !logged(run, t))
      }, { timeout: 60_000 })
      .toEqual([])
    // The second member merged the first's branch: its own branch holds the first's file.
    await expect
      .poll(() => {
        try {
          git(state.repo, 'cat-file', '-e', `crew/${id}/${second}:stub-${first}.txt`)
          return true
        } catch {
          return false
        }
      }, { timeout: 30_000 })
      .toBe(true)
  })
}
