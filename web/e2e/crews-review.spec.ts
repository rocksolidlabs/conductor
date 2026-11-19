import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, member, test, type Session } from './fixtures'

// The Crews screens as the review of issue 1024 asked them to read. A run whose task failed reads Failed on the Crews home and in
// its crew's Runs tab, and the Failed chip counts it (C1: the lists read the run's own outcome). A member's question with choices is
// answered in place, on the home's card and on the run page's tile, through the run chat's own path, and the question leaves (C6).
// A launch from the home says so on the page with Open run, the run under Running now (C11). The Runs tab names a run by its own
// name, else "Run of today 19:28", its members by name with their state's word, and how long it took in words (C12).
test.describe.configure({ mode: 'serial' })

const made: string[] = []
const runs: string[] = []

test.afterAll(async ({ api }) => {
  for (const id of runs) await api.stopRun(id)
  for (const id of made) await api.call('DELETE', `/api/crews/${encodeURIComponent(id)}`)
})

async function crew(api: { ok: <T>(m: string, p: string, b?: unknown) => Promise<T> }, body: Record<string, unknown>): Promise<string> {
  const id = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', { goal: 'read the crews pages', cwd: '', where: 'server', openAfterLaunch: false, yolo: null, ...body })).crew.id
  made.push(id)
  return id
}

/** What the stub typed into its transcript: the keys a choice sent reach it. */
function transcript(home: string, s: Session): string {
  const file = join(home, '.stub-sessions', `${s.agentSession?.id ?? ''}.txt`)
  return existsSync(file) ? readFileSync(file, 'utf8') : ''
}

test('a run whose task failed reads Failed on the home and in the Runs tab, and the Failed chip counts it', async ({ page, api }) => {
  // Its waits: the engine's three attempts at the check (120 s), then nine looks of 15 s each; the timeout is above their sum.
  test.setTimeout(120_000 + 9 * 15_000 + 30_000)
  const id = await crew(api, {
    name: `e2e review fails ${Date.now().toString(36)}`,
    isolation: 'worktree',
    members: [{ name: 'builder', agentId: 'worker-cli', prompt: '', start: { when: 'task' } }],
    tasks: [{ id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.', checks: [{ id: 'never', argv: ['sh', '-c', 'exit 1'] }] }],
  })
  const run = await api.launchCrew(id)
  runs.push(run.id)
  await expect.poll(async () => (await api.run(run.id)).outcome ?? '', { timeout: 120_000, intervals: [500] }).toBe('failed')
  // Its member carries no error of its own: the outcome is the run's alone.
  expect((await api.run(run.id)).members.every((m) => !m.error)).toBe(true)

  await page.goto('/crews')
  const entry = page.locator(`[data-crew-entry][data-crew-id="${id}"]`)
  await expect(entry.locator('[data-crew-last-state]')).toHaveText('Failed')
  await expect(entry.locator('[data-crew-last-state] [data-status-mark]')).toHaveAttribute('data-status-mark', 'failed')

  await page.goto(`/crews/${encodeURIComponent(id)}?tab=runs`)
  const row = page.locator(`[data-crew-runs] [data-run="${run.id}"]`)
  await expect(row.locator('[data-run-state]')).toHaveText('Failed')
  await expect(row).toHaveAttribute('data-phase', 'failed')
  await expect(page.locator('[data-runs-state="failed"]')).toHaveAttribute('data-count', '1')
  await expect(page.locator('[data-runs-state="succeeded"]')).toHaveAttribute('data-count', '0')
  await page.locator('[data-runs-state="failed"]').click()
  await expect(row).toBeVisible()
  await page.locator('[data-runs-state="all"]').click()
  await expect(page.locator('[data-runs-sentence]')).toHaveText(/^The only run so far failed (after|in) /)
})

let askId = ''
let askRun = ''
let coreId = ''

test('a launch from the home says so on the page with Open run, and the run is under Running now at once', async ({ page, api }) => {
  askId = await crew(api, {
    name: `e2e review asks ${Date.now().toString(36)}`,
    isolation: 'none',
    members: [{ name: 'core', agentId: 'claude', prompt: 'say hello', start: { when: 'immediately' } }],
  })
  await page.goto('/crews')
  await page.locator(`[data-launch-crew="${askId}"]`).click()
  await expect(page.locator('[data-crew-launch-line="core"]')).toBeVisible()
  await page.locator('[data-crew-launch-confirm]').click()
  const note = page.locator('[data-crew-started]')
  await expect(note).toContainText('Started e2e review asks')
  // In view, wherever the row's Launch run was on a long home.
  await expect(note).toBeInViewport()
  askRun = (await note.getAttribute('data-run')) ?? ''
  expect(askRun).toMatch(new RegExp(`^${askId}-[0-9a-f]{8}$`))
  runs.push(askRun)
  await expect(page.locator(`[data-running-now] [data-run="${askRun}"]`)).toBeVisible()
  await expect(note.getByRole('link', { name: 'Open run' })).toHaveAttribute('href', `/runs/${askRun}`)
  // The page stayed: the note is the home's.
  await expect(page).toHaveURL(/\/crews$/)
})

test("a member's question with choices is answered on the home's card and on the run's tile, and leaves", async ({ page, api, state }) => {
  // Its waits: four polls of 30 s, three of 20 s and two of 15 s, and seven looks of 15 s (the config's); the timeout is above their sum.
  test.setTimeout(4 * 30_000 + 3 * 20_000 + 2 * 15_000 + 7 * 15_000 + 30_000)
  await expect
    .poll(
      async () => {
        const m = member(await api.run(askRun), 'core')
        coreId = m.sessionId ?? ''
        return coreId ? m.status : ''
      },
      { timeout: 30_000 },
    )
    .toBe('running')
  // The member's first turn ends with the stub's Stop hook, which reports done: a question posted before would be overwritten.
  await expect.poll(async () => (await api.session(coreId)).attention?.state, { timeout: 30_000, message: "the member's first turn is done" }).toBe('done')
  const ask = (message: string) =>
    api.ok('POST', `/api/sessions/${coreId}/attention`, {
      state: 'needs_input',
      message,
      options: [
        { label: 'main', input: 'main\r' },
        { label: 'release', input: 'release\r' },
      ],
    })

  // On the home's card: the choices are buttons; the second answers core, and the question leaves the card.
  await ask('Which branch?')
  await page.goto('/crews')
  const card = page.locator(`[data-running-now] [data-run="${askRun}"]`)
  const asked = card.locator('[data-run-ask][data-member="core"]')
  await expect(asked).toContainText('Which branch?')
  await expect(asked.locator('[data-run-ask-open]')).toHaveCount(0)
  await asked.locator('[data-run-ask-choice="2"]').click()
  const core = await api.session(coreId)
  await expect.poll(() => transcript(state.home, core), { timeout: 20_000, message: 'core got the choice from the card' }).toContain('release')
  await expect(asked).toHaveCount(0)
  await expect.poll(async () => (await api.session(coreId)).attention?.state, { timeout: 15_000 }).not.toBe('needs_input')
  await expect(card.locator('[data-run-member="core"]')).not.toHaveAttribute('data-status', 'needs_input')

  // On the run page's tile: the same, under its terminal.
  await expect.poll(async () => (await api.session(coreId)).attention?.state, { timeout: 30_000, message: 'the answer turn is done' }).toBe('done')
  await ask('Which branch now?')
  await page.goto(`/runs/${encodeURIComponent(askRun)}?view=grid`)
  const tile = page.locator(`[data-run-grid] [data-member="core"]`)
  const choices = tile.locator('[data-run-ask-choices]')
  await expect(choices.locator('[data-run-ask-choice]')).toHaveCount(2)
  await expect(tile.locator('[data-tile-state] [data-status-word]')).toHaveText('Needs you')
  await choices.locator('[data-run-ask-choice="1"]').click()
  await expect.poll(() => transcript(state.home, core), { timeout: 20_000, message: 'core got the choice from the tile' }).toMatch(/release[\s\S]*main/)
  await expect(choices).toHaveCount(0)
  await expect.poll(async () => (await api.session(coreId)).attention?.state, { timeout: 15_000 }).not.toBe('needs_input')
})

test('the Runs tab names a run by its name or its start, its members by name with their state, and how long it took in words', async ({ page, api }) => {
  // Its waits: eleven looks of 15 s; the timeout is above their sum.
  test.setTimeout(11 * 15_000 + 30_000)
  await api.stopRun(askRun)
  const named = (await api.ok<{ run: { id: string } }>('POST', `/api/crews/${encodeURIComponent(askId)}/launch`, { label: 'e2e review named' })).run.id
  runs.push(named)
  await page.goto(`/crews/${encodeURIComponent(askId)}?tab=runs`)
  const first = page.locator(`[data-crew-runs] [data-run="${askRun}"]`)
  await expect(first.locator('[data-run-name]')).toHaveText(/^Run of today \d{2}:\d{2}$/)
  await expect(first.locator('[data-run-short-id]')).toHaveText(askRun.slice(-8))
  await expect(first.locator('[data-run-state]')).toHaveText('Stopped')
  await expect(first.locator('[data-run-member="core"]')).toHaveText(/core\s*Stopped/)
  await expect(first.locator('[data-run-took]')).toHaveText(/^(under a minute|\d+ min)$/)
  const second = page.locator(`[data-crew-runs] [data-run="${named}"]`)
  await expect(second.locator('[data-run-name]')).toHaveText('e2e review named')
  await expect(second.locator('[data-run-took]')).toHaveText(/ so far$/)
  await expect(second.locator('[data-run-member="core"] [data-status-word]')).toHaveText(/^(Working|Idle|Finished|Waiting to start|Needs you)$/)
})

// From five runs the Runs tab charts them (C13): five records served as the server's, one bar each in its state's tone, the median
// in the chart's line, and the sentence under it.
test('from five runs the Runs tab draws a bar a run with the median, and says how the last went', async ({ page, api }) => {
  const id = await crew(api, { name: `e2e review chart ${Date.now().toString(36)}`, isolation: 'none', members: [{ name: 'core', agentId: 'claude', prompt: 'say hello', start: { when: 'immediately' } }] })
  const at = (min: number) => new Date(Date.now() - min * 60_000).toISOString()
  const record = (n: number, minutes: number, how: 'succeeded' | 'stopped' | 'failed') => ({
    id: `${id}-0000000${n}`,
    crewId: id,
    name: 'e2e review chart',
    goal: 'read the crews pages',
    cwd: '/tmp',
    isolation: 'none',
    startedAt: at(100 - n * 10),
    ...(how === 'stopped' ? { stoppedAt: at(100 - n * 10 - minutes) } : {}),
    members: [{ name: 'core', agentId: 'claude', start: { when: 'immediately' }, status: 'ended', endedAt: at(100 - n * 10 - minutes), ...(how === 'failed' ? { error: 'exited (exit 1) before its prompt was typed' } : {}) }],
    log: [],
    state: how === 'stopped' ? 'stopped' : 'finished',
    needsInput: 0,
    yolo: false,
  })
  // Newest first, as the server lists them.
  const runs = [record(5, 6, 'succeeded'), record(4, 2, 'failed'), record(3, 4, 'stopped'), record(2, 8, 'succeeded'), record(1, 4, 'succeeded')]
  await page.route((u) => u.pathname === `/api/crews/${id}/runs`, (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ runs, live: 0 }) }))
  await page.goto(`/crews/${encodeURIComponent(id)}?tab=runs`)
  const chart = page.locator('[data-runs-chart]')
  await expect(chart).toHaveAttribute('data-runs', '5')
  const bars = chart.locator('[data-run-bar]')
  await expect(bars).toHaveCount(5)
  // Oldest first, each in its state.
  expect(await bars.evaluateAll((bs) => bs.map((b) => b.getAttribute('data-outcome')))).toEqual(['succeeded', 'succeeded', 'stopped', 'failed', 'succeeded'])
  await expect(chart).toContainText('duration in minutes · median 4m · 1 failed')
  await expect(chart.locator('[data-runs-sentence]')).toHaveText('The last run succeeded after 6 min; runs of this crew usually take 4 min (the middle of 5).')
  await expect(page.locator('[data-runs-state="failed"]')).toHaveAttribute('data-count', '1')
})
