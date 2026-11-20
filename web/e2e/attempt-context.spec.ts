import type { Page } from '@playwright/test'
import { budget, dayPage, expect, test, useTheme } from './fixtures'
import { noSideways, uncut } from './layout'
import { runInfo, serveRun, serveTicketRuns, type StubRun } from './run-fixtures'

// The context an attempt was given (docs/plans/context-seams.md, CT-3): a stub run of six tasks served in the browser, its first
// task's attempt given three blocks (two at its start, one by a lookup), the task route's reply carrying the list and the body route a
// block's text. The run page's attempt panel and the ticket's candidate draw the list, at a desktop's size and a phone's, in both
// themes; Copy copies a pack mark; a row opens its text. With no context in the reply, no list: the build with no provider.

const TICKET = 'CND-4'
const LOAD_MS = 15_000
const SHA = (c: string) => c.repeat(64)

const ROWS = [
  { seq: 1, phase: 'task' as const, source: 'graph', title: 'Callers of Build', ref: 'build.go', pack: 'p-4f2c-1', sha256: SHA('a'), bytes: 17, at: new Date().toISOString() },
  { seq: 2, phase: 'task' as const, source: 'graph', title: 'Recent changes to the migration and every file it touched in the last week', sha256: SHA('b'), bytes: 2048, at: new Date().toISOString() },
  { seq: 3, phase: 'lookup' as const, source: 'graph', title: 'The pack', pack: 'p-9e01-3', sha256: SHA('c'), bytes: 19, at: new Date().toISOString() },
]
const TEXT: Record<number, string> = { 1: 'main calls Build.', 2: 'build.go changed twice.\n  indented line', 3: 'a block of the pack' }

/** A stub run whose t01 (succeeded) was given ROWS, or nothing when `given` is false. */
function contextRun(given = true): StubRun {
  const run = runInfo({ tasks: 6, states: ['succeeded', 'running', 'running', 'running', 'running', 'running'] })
  if (given) run.tasks[0]!.attempt!.context = ROWS.map((r) => ({ ...r }))
  return run
}

/** A page of the day's with the run served, the body route of t01's attempt answered from TEXT, and the ticket's runs naming it. */
async function opened(browser: Parameters<typeof dayPage>[0], run: StubRun, o: { width?: number; height?: number; touch?: boolean; theme?: 'light' | 'dark' } = {}): Promise<{ page: Page; reads: number[] }> {
  const page = await dayPage(browser, { path: '/agents', ...o })
  await serveRun(page, run)
  await serveTicketRuns(page, TICKET, run)
  const reads: number[] = []
  await page.route(
    (u) => u.pathname.startsWith(`/api/runs/${run.id}/tasks/t01/attempts/1/context/`),
    (route) => {
      const seq = Number(new URL(route.request().url()).pathname.split('/').pop())
      reads.push(seq)
      const row = ROWS.find((r) => r.seq === seq)
      if (!row) return route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ error: { code: 'not_found', message: 'no such block' } }) })
      return route.fulfill({ status: 200, contentType: 'text/plain; charset=utf-8', headers: { 'X-Conductor-Context-SHA256': row.sha256 }, body: TEXT[seq]! })
    },
  )
  return { page, reads }
}

/** The run page at t01's attempts. */
async function runPage(page: Page, run: StubRun): Promise<void> {
  budget(LOAD_MS)
  await page.goto(`/runs/${run.id}?view=tasks&task=t01`)
  await expect(page.locator('[data-task-attempt-row="1"]')).toBeVisible()
}

/** Checks the list drawn in `root`: its summary, its rows in order, each fitting, and the page not wider than its window. */
async function drawnList(page: Page, root: ReturnType<Page['locator']>): Promise<void> {
  const list = root.locator('[data-attempt-context]')
  await expect(list.locator('[data-attempt-context-summary]')).toHaveText('Context: 2 at start, 1 looked up')
  await expect(list.locator('[data-attempt-context-row]')).toHaveCount(3)
  await expect(list.locator('[data-attempt-context-row="3"]')).toHaveAttribute('data-attempt-context-phase', 'lookup')
  await expect(list.locator('[data-attempt-context-row="1"] [data-attempt-context-digest]')).toHaveText('aaaaaaaaaaaa')
  await expect(list.locator('[data-attempt-context-row="2"] [data-attempt-context-size]')).toHaveText('2 KiB')
  await expect(list.locator('[data-attempt-context-row="2"] [data-attempt-context-pack]')).toHaveCount(0)
  // Each title whole, read once it is scrolled into view.
  for (const r of ROWS) {
    const title = list.locator(`[data-attempt-context-open="${r.seq}"]`)
    await title.scrollIntoViewIfNeeded()
    await uncut(title)
  }
  // Nothing of the list runs past the window (the page's own sideways strips, a ticket's tabs, are theirs).
  await noSideways(page, { root: list.locator('*') })
}

for (const theme of ['dark', 'light'] as const) {
  test(`the run page's attempt panel lists the context, Copy copies a mark and a row opens its text (${theme})`, async ({ day, browser }) => {
    void day
    const run = contextRun()
    const { page, reads } = await opened(browser, run, { theme })
    await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])
    await runPage(page, run)
    await useTheme(page, theme)
    const panel = page.locator('[data-task-attempt-row="1"]')
    await drawnList(page, panel)
    await panel.locator('[data-attempt-context-copy="1"]').click()
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe('p-4f2c-1')
    await panel.locator('[data-attempt-context-open="2"]').click()
    const text = page.locator('[data-attempt-context-text-seq="2"]')
    await expect(text.locator('[data-attempt-context-body]')).toHaveText(TEXT[2]!, { useInnerText: true })
    expect(reads).toEqual([2])
  })
}

test('on a phone, 390 x 844 with touch, the list is one column and fits', async ({ day, browser }) => {
  void day
  const run = contextRun()
  const { page } = await opened(browser, run, { width: 390, height: 844, touch: true })
  await runPage(page, run)
  const panel = page.locator('[data-task-attempt-row="1"]')
  await drawnList(page, panel)
  // One column: each row's pieces stack, the title under its phase.
  const row = panel.locator('[data-attempt-context-row="1"]')
  const phase = await row.locator('span').first().boundingBox()
  const title = await row.locator('[data-attempt-context-open="1"]').boundingBox()
  expect(title!.y, 'the title is under the phase').toBeGreaterThanOrEqual(phase!.y + phase!.height - 1)
  await row.locator('[data-attempt-context-open="1"]').tap()
  await expect(page.locator('[data-attempt-context-text-seq="1"] [data-attempt-context-body]')).toHaveText(TEXT[1]!)
})

for (const size of [{ width: 1440, height: 900 }, { width: 390, height: 844, touch: true }]) {
  test(`a ticket's candidate says how many blocks and opens the list (${size.width})`, async ({ day, browser }) => {
    await day.part('dispatch')
    const run = contextRun()
    const { page } = await opened(browser, run, size)
    budget(LOAD_MS)
    await page.goto(`/dispatch/${TICKET.replace('-', '/')}`)
    const card = page.locator(`[data-run-card="${run.id}"]`)
    const count = card.locator('[data-candidate-context]')
    await expect(count).toHaveText('context: 3 blocks')
    await expect(card.locator('[data-attempt-context]')).toHaveCount(0)
    await count.click()
    await drawnList(page, card)
  })
}

test("a ticket's candidate reads its context at every open: a list that grew shows whole, and a read with none says so", async ({ day, browser }) => {
  await day.part('dispatch')
  const run = contextRun()
  const { page } = await opened(browser, run)
  // The task route as the stub answers it, but for the rows this test sets: each open's read is counted.
  let given: unknown[] = ROWS.slice(0, 2)
  let reads = 0
  await page.route(
    (u) => u.pathname === `/api/runs/${run.id}/tasks/t01`,
    async (route) => {
      reads++
      const real = (await (await route.fetch()).json()) as { attempts: Array<Record<string, unknown>> }
      for (const a of real.attempts) if (a.n === 1) a.context = given
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(real) })
    },
  )
  budget(LOAD_MS)
  await page.goto(`/dispatch/${TICKET.replace('-', '/')}`)
  const card = page.locator(`[data-run-card="${run.id}"]`)
  const count = card.locator('[data-candidate-context]')
  await count.click()
  await expect(card.locator('[data-attempt-context-row]')).toHaveCount(2)
  const first = reads
  expect(first).toBeGreaterThan(0)
  // The attempt's lookup is recorded meanwhile: closed and opened again, the list is read again and holds it.
  given = ROWS
  await count.click()
  await expect(card.locator('[data-attempt-context]')).toHaveCount(0)
  await count.click()
  await expect(card.locator('[data-attempt-context-row]')).toHaveCount(3)
  expect(reads).toBeGreaterThan(first)
  // A read that lists none for the attempt says so, never "Reading" with nothing on its way.
  given = []
  await count.click()
  await count.click()
  await expect(card.locator('[data-candidate-context-none]')).toHaveText("The task's read lists no context for this attempt.")
  await expect(card.locator('[data-candidate-context-reading]')).toHaveCount(0)
})

test('with no context in the reply, no list and no count', async ({ day, browser }) => {
  await day.part('dispatch')
  const run = contextRun(false)
  const { page } = await opened(browser, run)
  await runPage(page, run)
  await expect(page.locator('[data-task-attempt-row="1"]')).toBeVisible()
  await expect(page.locator('[data-attempt-context]')).toHaveCount(0)
  budget(LOAD_MS)
  await page.goto(`/dispatch/${TICKET.replace('-', '/')}`)
  await expect(page.locator(`[data-run-card="${run.id}"] [data-candidate="t01/1"]`)).toBeVisible()
  await expect(page.locator('[data-candidate-context]')).toHaveCount(0)
})
