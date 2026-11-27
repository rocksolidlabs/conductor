import type { Browser, BrowserContext, Page } from '@playwright/test'
import { expect, itemURL, standingRoute, task, test, type Run } from './fixtures'
import { brakemanServer, type BrakemanServer } from './brakeman-server'
import { noSideways } from './layout'
import { walk, worst } from './contrast'

// The answer where it was asked (issue 993, F8), on a server of the spec's own with the Brakeman stub (the seeded day holds no
// Brakeman records). A managed run whose check always fails (brakeman-cards.spec.ts's crew) leaves build failed and docs blocked.
// "Ask Brakeman why" on a row sends at once and selects the row, under which the answer stands: while the stub holds its turn at its
// gate, "Reading now" with the reads the server counted; after the gate, the brief's reading, Open the brief and Ask again. The run's
// panel names its latest brief; the run palette's Ask Brakeman… asks about the run and shows the Tasks view; the ticket's panel sends
// its chips; Needs you's Ask Brakeman sends at once with a toast. The same at 390 x 844, in both themes; an older server's refusal of
// the about filter draws nothing of Brakeman.
test.describe.configure({ mode: 'serial' })

const PROJECT = 'BKW'
let srv: BrakemanServer
let runId = ''

const failCrew = {
  name: 'e2e brakeman where',
  goal: 'fail a check',
  cwd: '',
  where: 'server',
  isolation: 'worktree',
  openAfterLaunch: false,
  yolo: null,
  members: [
    { name: 'builder', agentId: 'worker-cli', prompt: '', start: { when: 'task' } },
    { name: 'writer', agentId: 'worker', prompt: '', start: { when: 'task' } },
  ],
  tasks: [
    { id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.', checks: [{ id: 'never', argv: ['sh', '-c', 'exit 1'] }] },
    { id: 'docs', member: 'writer', kind: 'work', instructions: 'Document it.', needs: ['build'] },
  ],
}

/** The brief the stub submits: a fact on build with its evidence, and Brakeman's reading of it. */
function brief(title: string) {
  return {
    schema: 'brakeman.brief/1',
    title,
    summary: 'The check exits 1 whatever the change: build cannot pass it, and docs waits on build.',
    items: [
      { kind: 'fact/1', text: 'build failed its check never on each of its attempts.', evidence: [`task:${runId}/build`, `run:${runId}`] },
      { kind: 'interpretation/1', text: 'The check is what fails, not the work.', facts: [0] },
    ],
  }
}

const runOf = () => srv.api.ok<{ run: Run }>('GET', `/api/runs/${encodeURIComponent(runId)}`).then((r) => r.run)

test.beforeAll(async () => {
  test.setTimeout(240_000)
  srv = await brakemanServer({ name: 'brakeman-where' })
  await srv.api.ok('POST', '/api/dispatch/projects', { opId: 'bkw-p', key: PROJECT, name: 'Brakeman where' })
  await srv.api.ok('POST', '/api/dispatch/tickets', { opId: 'bkw-t1', project: PROJECT, title: 'The build that never passes' })
  const crew = (await srv.api.ok<{ crew: { id: string } }>('POST', '/api/crews', failCrew)).crew.id
  runId = (await srv.api.launchCrew(crew)).id
  await expect.poll(async () => (await runOf()).outcome ?? '', { timeout: 120_000, intervals: [500] }).toBe('failed')
  const settled = await runOf()
  expect(task(settled, 'build').state).toBe('failed')
  expect(task(settled, 'docs').state).toBe('blocked')
})

test.afterAll(async () => {
  if (!srv) return
  if (runId) await srv.api.stopRun(runId).catch(() => undefined)
  await srv.stop()
})

/** A page of its own on the spec's server at `path`, as Nate, at the size asked; the context closed at the test's end. */
async function opened(browser: Browser, path: string, o: { width?: number; height?: number; theme?: 'dark' | 'light'; before?: (ctx: BrowserContext) => Promise<void> } = {}): Promise<Page> {
  const ctx = await browser.newContext({ viewport: { width: o.width ?? 1440, height: o.height ?? 900 }, colorScheme: o.theme ?? 'dark' })
  await standingRoute(ctx)
  if (o.before) await o.before(ctx)
  pages.push(ctx)
  const page = await ctx.newPage()
  await srv.open(page, path, { name: 'Nate' })
  await expect(page.locator('#dashboard-sidebar-main, [data-bottom-bar]').first()).toBeAttached({ timeout: 30_000 })
  return page
}
const pages: BrowserContext[] = []
test.afterEach(async () => {
  for (const c of pages.splice(0)) await c.close()
})

const panel = (page: Page, subject: string) => page.locator(`[data-brakeman-subject-panel="${subject}"]`)
const answer = (page: Page, t: string) => page.locator(`[data-task-row="${t}"] [data-task-brakeman-answer]`)

/** Asks Brakeman why from the task's row menu, and returns the request its answer stands for. */
async function askWhy(page: Page, t: string): Promise<string> {
  await page.locator(`[data-task-menu="${t}"]`).click()
  await page.locator(`[data-task-ask-brakeman="${t}"]`).click()
  await expect(page.getByRole('menu')).toHaveCount(0)
  await expect(answer(page, t)).toBeVisible({ timeout: 15_000 })
  return (await answer(page, t).getAttribute('data-request'))!
}

test("Ask Brakeman why sends from the row and the answer stands under it: reading now with the reads counted, then the reading, and again after a redraw", async ({ browser }) => {
  test.setTimeout(180_000)
  srv.shutGate()
  srv.setBrief(brief('Build fails a check that never passes'))
  const page = await opened(browser, `/runs/${encodeURIComponent(runId)}?view=tasks`)
  await expect(page.locator('[data-task-row="build"]')).toBeVisible({ timeout: 30_000 })
  // Brakeman on and nothing said about the run yet: the panel says so.
  await expect(panel(page, `run:${runId}`).locator('[data-panel-none]')).toHaveText('Brakeman has said nothing about this run.', { timeout: 15_000 })

  const id = await askWhy(page, 'build')
  // The ask selected the row, and was sent as the row asks it, under the person's name.
  await expect(page).toHaveURL(/[?&]task=build(&|$)/)
  const asked = await srv.request(id)
  expect(asked).toMatchObject({ kind: 'explain', subjects: [`task:${runId}/build`] })
  expect((await srv.api.ok<{ request: { by?: string } }>('GET', `/api/brakeman/requests/${id}`)).request.by).toBe('Nate')

  // Held at the gate after its counted read: the answer shows what the server counted, never a starting value.
  await expect.poll(async () => (await srv.request(id)).reads ?? 0, { timeout: 60_000 }).toBeGreaterThanOrEqual(1)
  const reads = (await srv.request(id)).reads!
  await expect(answer(page, 'build')).toContainText('Reading now')
  await expect(answer(page, 'build').locator('[data-progress]')).toHaveAttribute('data-reads-used', String(reads), { timeout: 15_000 })
  await expect(panel(page, `run:${runId}`).locator(`[data-panel-reading-now] [data-request="${id}"]`)).toBeVisible()

  srv.openGate()
  await srv.until(id, ['briefed'])
  await expect(answer(page, 'build')).toHaveAttribute('data-answer-state', 'briefed', { timeout: 15_000 })
  await expect(answer(page, 'build').locator('[data-answer-reading]')).toContainText('The check exits 1 whatever the change')
  await expect(answer(page, 'build').locator('[data-answer-open]')).toHaveAttribute('href', `/brakeman?request=${id}`)
  await expect(answer(page, 'build').locator('[data-answer-evidence] a, [data-answer-evidence] button').first()).toBeVisible()
  // The run's panel names it as its latest brief, with its title and counts.
  const latest = panel(page, `run:${runId}`).locator('[data-panel-latest]')
  await expect(latest).toHaveAttribute('data-request', id)
  await expect(latest).toContainText('Build fails a check that never passes')
  await expect(latest.locator('[data-panel-counts]')).toContainText('1 fact')

  // A redraw the test causes: another view and back. The answer is still under the row.
  await page.locator('[data-run-view]').getByRole('tab', { name: 'Members' }).click()
  await expect(page.locator('[data-run-grid]')).toBeVisible()
  await page.locator('[data-run-view]').getByRole('tab', { name: 'Tasks' }).click()
  await expect(answer(page, 'build')).toHaveAttribute('data-request', id, { timeout: 15_000 })
  await expect(answer(page, 'build').locator('[data-answer-reading]')).toContainText('The check exits 1 whatever the change')

  // Ask again: the same ask under a new request, which then stands under the row.
  srv.shutGate()
  await answer(page, 'build').locator('[data-answer-again]').click()
  await expect(answer(page, 'build')).not.toHaveAttribute('data-request', id, { timeout: 15_000 })
  const again = (await answer(page, 'build').getAttribute('data-request'))!
  expect(await srv.request(again)).toMatchObject({ kind: 'explain', subjects: [`task:${runId}/build`] })
  srv.openGate()
  await srv.until(again, ['briefed'])
  await expect(answer(page, 'build')).toHaveAttribute('data-answer-state', 'briefed', { timeout: 15_000 })
})

test("the run palette's Ask Brakeman… asks about the run and shows the Tasks view, where the panel reads it", async ({ browser }) => {
  test.setTimeout(120_000)
  srv.shutGate()
  const page = await opened(browser, `/runs/${encodeURIComponent(runId)}?view=grid`)
  await expect(page.locator('[data-run-grid]')).toBeVisible({ timeout: 30_000 })
  // The palette's key once the layout listens for it (its sidebar root drawn), pressed once.
  await expect(page.locator('#dashboard-sidebar-main')).toBeAttached()
  await page.keyboard.press('Alt+/')
  const palette = page.locator('[data-palette]')
  await expect(palette).toBeVisible()
  await palette.locator('input[data-palette-input]').fill('ask brakeman')
  const row = palette.locator('[data-palette-row="page:ask-brakeman"]')
  await expect(row).toBeVisible()
  for (let i = 0; i < 12 && (await row.getAttribute('data-palette-focused')) === null; i++) await page.keyboard.press('ArrowDown')
  await expect(row).toHaveAttribute('data-palette-focused', '')
  await page.keyboard.press('Enter')
  const field = row.locator('[data-palette-field] input')
  await expect(field).toBeFocused()
  await field.fill('Why does build keep failing?')
  await expect(field).toHaveValue('Why does build keep failing?')
  await page.keyboard.press('Enter')
  // The person's own press: a toast names the request, and the page shows the Tasks view, the panel reading it.
  const toast = page.locator('li').filter({ hasText: /^Asked as br-\d+/ }).last()
  await expect(toast).toBeVisible({ timeout: 15_000 })
  const id = /Asked as (br-\d+)/.exec((await toast.innerText()) ?? '')![1]!
  expect(await srv.request(id)).toMatchObject({ kind: 'ask', subjects: [`run:${runId}`] })
  await expect(page).toHaveURL(/[?&]view=tasks(&|$)/)
  await expect(panel(page, `run:${runId}`).locator(`[data-panel-reading-now] [data-request="${id}"]`)).toBeVisible({ timeout: 15_000 })
  srv.openGate()
  await srv.until(id, ['briefed'])
  await expect(panel(page, `run:${runId}`).locator('[data-panel-latest]')).toHaveAttribute('data-request', id, { timeout: 15_000 })
})

test("the run panel's Overlapping changes, which names no subject, says where it went and never becomes the run's latest brief", async ({ browser }) => {
  test.setTimeout(120_000)
  // The stub's own brief, which cites its own session and run, never this run; held at the gate until the request is named here.
  srv.setBrief(null)
  srv.shutGate()
  const page = await opened(browser, `/runs/${encodeURIComponent(runId)}?view=tasks`)
  const latest = panel(page, `run:${runId}`).locator('[data-panel-latest]')
  await expect(latest).toBeVisible({ timeout: 30_000 })
  const before = (await latest.getAttribute('data-request'))!
  // The page's own read of the new request once its brief is kept: what would mark it briefed in the page.
  let id = ''
  const briefedRead = page.waitForResponse(async (res) => {
    if (!id || new URL(res.url()).pathname !== `/api/brakeman/requests/${id}`) return false
    const body = (await res.json().catch(() => null)) as { request?: { state?: string } } | null
    return body?.request?.state === 'briefed'
  })
  await panel(page, `run:${runId}`).locator('[data-ask-chip="overlaps"]').click()
  // Not about the run: the panel says where it went.
  const toast = page.locator('li').filter({ hasText: /^Asked as br-\d+/ }).last()
  await expect(toast).toBeVisible({ timeout: 15_000 })
  id = /Asked as (br-\d+)/.exec(await toast.innerText())![1]!
  expect(await srv.request(id)).toMatchObject({ kind: 'overlaps' })
  srv.openGate()
  await srv.until(id, ['briefed'])
  await briefedRead
  await page.evaluate(() => new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done))))
  // Its brief neither names nor cites the run: the panel's latest is the run's own, as before.
  await expect(latest).toHaveAttribute('data-request', before)
  await expect(panel(page, `run:${runId}`).locator(`[data-request="${id}"]`)).toHaveCount(0)
})

test("the ticket's panel sends its chip about the ticket at once, and reads it", async ({ browser }) => {
  test.setTimeout(120_000)
  srv.shutGate()
  const page = await opened(browser, `/dispatch/${PROJECT}/1`)
  await expect(page.locator('[data-ticket-title]')).toHaveText('The build that never passes', { timeout: 30_000 })
  // The header's Ask Brakeman menu is gone: its two entries are the panel's chips.
  await expect(page.locator('[data-ticket-ask-brakeman]')).toHaveCount(0)
  const p = panel(page, `ticket:${PROJECT}-1`)
  await expect(p.locator('[data-panel-none]')).toHaveText('Brakeman has said nothing about this ticket.', { timeout: 15_000 })
  for (const key of ['explain', 'decision', 'draft_tickets', 'propose_crew']) await expect(p.locator(`[data-ask-chip="${key}"]`)).toBeVisible()
  await p.locator('[data-ask-chip="propose_crew"]').click()
  const reading = p.locator('[data-panel-reading-now] [data-request]').first()
  await expect(reading).toBeVisible({ timeout: 15_000 })
  const id = (await reading.getAttribute('data-request'))!
  expect(await srv.request(id)).toMatchObject({ kind: 'propose_crew', subjects: [`ticket:${PROJECT}-1`] })
  srv.openGate()
  await srv.until(id, ['briefed', 'failed', 'ended'])
  await expect(p.locator('[data-panel-reading-now]')).toHaveCount(0, { timeout: 15_000 })
})

test("Needs you's Ask Brakeman asks Brakeman to explain the item at once, with a toast, and the page stays", async ({ browser }) => {
  test.setTimeout(120_000)
  srv.openGate()
  const ref = `task:${runId}/build`
  const page = await opened(browser, `/decisions?item=${encodeURIComponent(ref)}`)
  const ask = page.locator('[data-inbox-ask-brakeman]')
  await expect(ask).toBeVisible({ timeout: 30_000 })
  await ask.click()
  const toast = page.locator('li').filter({ hasText: /^Asked as br-\d+/ }).last()
  await expect(toast).toBeVisible({ timeout: 15_000 })
  const id = /Asked as (br-\d+)/.exec(await toast.innerText())![1]!
  expect(await srv.request(id)).toMatchObject({ kind: 'explain', subjects: [ref] })
  await expect(page).toHaveURL(itemURL(ref))
  await srv.until(id, ['briefed'])
})

test('at 390 x 844 the panel wraps under the tasks and the answer stays in the page, nothing sideways', async ({ browser }) => {
  test.setTimeout(90_000)
  const page = await opened(browser, `/runs/${encodeURIComponent(runId)}?view=tasks&task=build`, { width: 390, height: 844 })
  await expect(answer(page, 'build')).toHaveAttribute('data-answer-state', 'briefed', { timeout: 30_000 })
  const p = panel(page, `run:${runId}`)
  await expect(p).toBeVisible()
  const table = await page.locator('[data-task-table]').boundingBox()
  const box = await p.boundingBox()
  expect(table && box && box.y >= table.y + table.height - 0.5, 'the panel is under the tasks').toBe(true)
  expect(box && box.x >= 0 && box.x + box.width <= 390 + 0.5, 'the panel is inside the window').toBe(true)
  await noSideways(page)
})

for (const theme of ['dark', 'light'] as const) {
  test(`the panel and the answer pass the contrast walk, ${theme}`, async ({ browser }) => {
    test.setTimeout(90_000)
    const page = await opened(browser, `/runs/${encodeURIComponent(runId)}?view=tasks&task=build`, { theme })
    await expect(answer(page, 'build')).toHaveAttribute('data-answer-state', 'briefed', { timeout: 30_000 })
    await expect(panel(page, `run:${runId}`).locator('[data-panel-latest]')).toBeVisible()
    for (const root of [panel(page, `run:${runId}`), page.locator('[data-task-row="build"]')]) {
      const { judged, failures } = await walk(page, root)
      expect(judged).toBeGreaterThan(3)
      expect(failures, worst(failures)).toEqual([])
    }
  })
}

test("an older server's refusal of the about filter draws nothing of Brakeman on the run", async ({ browser }) => {
  test.setTimeout(90_000)
  let refuse: () => void = () => {}
  const refused = new Promise<void>((done) => (refuse = done))
  const page = await opened(browser, `/runs/${encodeURIComponent(runId)}?view=tasks&task=build`, {
    // Set before the page loads: the list answers a filter as a server before the filters did.
    before: (ctx) =>
      ctx.route(/\/api\/brakeman\/requests\?.*about=/, async (route) => {
        await route.fulfill({ status: 400, contentType: 'application/json', body: JSON.stringify({ error: { code: 'invalid_request', message: 'this route takes no parameter "about"' } }) })
        refuse()
      }),
  })
  await expect(page.locator('[data-task-row="build"]')).toBeVisible({ timeout: 30_000 })
  await expect(page.locator('[data-task-detail-id="build"]')).toBeVisible()
  // The refusal answered, and the page drawn after it: what it shows now is what it shows for it.
  await refused
  await page.evaluate(() => new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done))))
  // The run's tasks are drawn as ever; no panel and no answer, though the row is selected and its requests exist.
  await expect(page.locator('[data-brakeman-subject-panel]')).toHaveCount(0)
  await expect(page.locator('[data-task-brakeman-answer]')).toHaveCount(0)
})
