import type { Locator, Page } from '@playwright/test'
import { budget, dayPage, expect, test, useTheme } from './fixtures'
import type { Names } from './day'
import { inboxItem, stubInbox, type StubItem } from './inbox-stub'
import { probeColour } from './layout'
import { runInfo, serveRun, serveTicketRuns, type StubRun } from './run-fixtures'

// One status language for runs and tasks (UX1's PR-10, docs/plans/ux-review.md): a run's tasks, its graph, its card on the Crews page
// and Dispatch's card for it draw the marks and words of utils/status.ts. A managed run of six tasks from runInfo, served in the
// browser: t01 held by a plan gate's open decision, t02 failed with Retry and Accept named, t03 running in the day's working session
// (yard-tile-resize), t04 running in the day's finished one (flaky-vim-spec), t05 checking its result, t06 succeeded. The table below
// is this spec's own, never the app's, so that a change of the app's words fails it.

interface Want {
  key: string
  word: RegExp
  /** The token the mark is drawn in. */
  tone: string
}

const WANT: Record<string, Want> = {
  t01: { key: 'needs', word: /^needs you$/, tone: '--ui-warning' },
  t02: { key: 'needs', word: /^needs you$/, tone: '--ui-warning' },
  t03: { key: 'working', word: /^working$/, tone: '--ui-success' },
  // The age ticks while the test reads.
  t04: { key: 'finished', word: /^finished (just now|\d+[mh] ago)$/, tone: '--ui-text-muted' },
  t05: { key: 'working', word: /^checking$/, tone: '--ui-success' },
  t06: { key: 'done', word: /^done$/, tone: '--ui-text-muted' },
}
const TASKS = Object.keys(WANT)
/** The keys the drawn run has, in the legend's order (the five's, then a task's own). */
const LEGEND = ['needs', 'working', 'finished', 'done']
/** The ticket whose Runs section shows the run: one of the day's with no run of its own. */
const TICKET = 'CND-4'
/** One page load's and its first read's bound (the config's expect timeout): each load a test makes raises its timeout by it. */
const LOAD_MS = 15_000

/** The run and the two items of the one list that wait in it: the gate's decision that holds t01, and t02 out of its attempts. */
async function made(day: { part: (n: 'states' | 'dispatch') => Promise<Names> }): Promise<{ run: StubRun; items: StubItem[] }> {
  // Dispatch's tickets (TICKET among them), and the sessions in each state.
  await day.part('dispatch')
  const n = await day.part('states')
  const run = runInfo({ tasks: 6, states: ['ready', 'failed', 'running', 'running', 'verifying', 'succeeded'], sessions: { t03: n['yard-tile-resize']!, t04: n['flaky-vim-spec']! } })
  const gate = inboxItem('decision', { run, task: 't01', origin: 'plan' })
  run.tasks[0]!.holds = [{ id: gate.ref.slice('decision:'.length), type: 'approval', state: 'open', question: gate.question }] as typeof run.tasks[0]['holds']
  const out = inboxItem('task', { run, task: 't02' })
  return { run, items: [gate, out] }
}

/** A page of the day's with the stubs routed before the page that draws the run loads: the inbox's items, the run, the ticket's runs. */
async function opened(browser: Parameters<typeof dayPage>[0], run: StubRun, items: StubItem[], o: { live: boolean; width?: number; height?: number; touch?: boolean }): Promise<Page> {
  const page = await dayPage(browser, { path: '/agents', ...(o.width ? { width: o.width, height: o.height, touch: o.touch } : {}) })
  await stubInbox(page, items)
  if (o.live) await serveRun(page, run)
  await serveTicketRuns(page, TICKET, run)
  return page
}

/** Reads a mark once it shows `want`'s key: its label, its word where it draws one, and its colour against the token. */
async function check(page: Page, mark: Locator, name: string, want: Want, drawsWord = true): Promise<void> {
  await expect(mark, `${name}'s mark`).toHaveAttribute('data-status-mark', want.key)
  const got = await mark.evaluate((el) => {
    const glyph = el.querySelector('[data-status-glyph]')
    const word = el.querySelector('[data-status-word]')
    return { label: el.getAttribute('aria-label') ?? '', colour: glyph ? getComputedStyle(glyph).color : '', word: word ? (word.textContent ?? '').trim() : null, wordColour: word ? getComputedStyle(word).color : '' }
  })
  expect(got.label, `${name}'s mark is named by its word`).toMatch(want.word)
  expect(await probeColour(page, got.colour), `${name}'s mark is drawn in ${want.tone}`).toBe(await probeColour(page, `var(${want.tone})`))
  if (drawsWord) {
    expect(got.word, `${name}'s word`).toMatch(want.word)
    expect(await probeColour(page, got.wordColour), `${name}'s word is in the muted text`).toBe(await probeColour(page, 'var(--ui-text-muted)'))
  }
}

/** Each task row of the Tasks view: one mark, its word, its colour; the engine's state and sentence stay on the row. */
async function readRows(page: Page, run: StubRun): Promise<void> {
  for (const id of TASKS) {
    const row = page.locator(`[data-task-row="${id}"]`)
    await expect(row.locator('[data-status-mark]'), `${id} draws one mark`).toHaveCount(1)
    await check(page, row.locator('[data-status-mark]'), id, WANT[id]!)
    await expect(row).toHaveAttribute('data-task-state', run.tasks.find((t) => t.id === id)!.state)
  }
  // The held row's sentence is the engine's still: what it waits for.
  await expect(page.locator('[data-task-row="t01"] [data-task-words]')).toContainText('waits for decision')
}

/** The Graph view's Tasks mode: each station's mark (the line diagram, UX1 PR-33), and the legend listing exactly the keys drawn, in the table's order. */
async function readGraph(page: Page): Promise<void> {
  await page.locator('[data-run-view]').getByRole('tab', { name: 'Graph' }).click()
  const canvas = page.locator('[data-graph-canvas]')
  for (const id of TASKS) {
    // A station is named by its task's id and words, so its mark is read by its key and its colour.
    const station = canvas.locator(`[data-station="${id}"]`)
    await expect(station, `${id}'s station`).toHaveAttribute('data-status-mark', WANT[id]!.key)
    const colour = await station.locator('[data-status-glyph]').evaluate((el) => getComputedStyle(el).color)
    expect(await probeColour(page, colour), `${id}'s station is drawn in ${WANT[id]!.tone}`).toBe(await probeColour(page, `var(${WANT[id]!.tone})`))
  }
  await page.locator('[data-graph-legend-open]').click()
  const legend = page.locator('[data-graph-legend-list]')
  await expect(legend.locator('[data-graph-legend]')).toHaveCount(LEGEND.length)
  expect(await legend.locator('[data-graph-legend]').evaluateAll((els) => els.map((e) => e.getAttribute('data-graph-legend')))).toEqual(LEGEND)
  await expect(legend.locator('[data-status-mark]')).toHaveCount(LEGEND.length)
  await page.keyboard.press('Escape')
  await expect(legend).toHaveCount(0)
}

test('a run of six tasks speaks the status language: its rows, its graph, its legend and its card on the Crews page, in both themes', async ({ day, browser }) => {
  const { run, items } = await made(day)
  const page = await opened(browser, run, items, { live: true })
  const read = async () => {
    budget(LOAD_MS)
    await page.goto(`/runs/${run.id}?view=tasks`)
    await readRows(page, run)
    await readGraph(page)
    // Back to the Tasks view: a redraw the test causes, read again (AGENTS.md: check again after a redraw).
    await page.locator('[data-run-view]').getByRole('tab', { name: 'Tasks' }).click()
    await readRows(page, run)
    // The run's card on the Crews page carries the run's sentence: its done part, its working and finished apart, its two items.
    budget(LOAD_MS)
    await page.goto('/crews')
    const card = page.locator(`[data-running-now] [data-run="${run.id}"] [data-run-state]`)
    await expect(card.locator('[data-status-word]').first()).toHaveText('Needs you')
    await expect(card.locator('[data-run-detail]')).toHaveText(/^1 of 6 done · 1 working · 1 checking · 1 finished · 2 need you$/)
  }
  await read()
  await useTheme(page, 'light')
  await read()
})

test("Dispatch's card says what the ticket's summary carries, and the live run's whole sentence once the live store holds it", async ({ day, browser }) => {
  const { run, items } = await made(day)
  // The summary alone: the run is left out of GET /api/runs and the server answers /api/runs/<id> 404, so neither the live store nor
  // the records hold it. The card says the done part and the run's two items, and no working, finished or checking part.
  const alone = await opened(browser, run, items, { live: false })
  budget(LOAD_MS)
  await alone.goto(`/dispatch/${TICKET.replace('-', '/')}`)
  const state = (p: Page) => p.locator(`[data-run-card="${run.id}"] [data-ticket-run-state]`)
  await expect(state(alone)).toHaveAttribute('data-run-status-from', 'summary')
  await expect(state(alone)).toHaveText('1 of 6 done · 2 need you')
  await useTheme(alone, 'light')
  await expect(state(alone)).toHaveText('1 of 6 done · 2 need you')
  // The live run: its sentence whole, working and finished apart.
  const live = await opened(browser, run, items, { live: true })
  budget(LOAD_MS)
  await live.goto(`/dispatch/${TICKET.replace('-', '/')}`)
  await expect(state(live)).toHaveAttribute('data-run-status-from', 'run')
  await expect(state(live)).toHaveText('1 of 6 done · 1 working · 1 checking · 1 finished · 2 need you')
  // Its task rows, embedded from the run page, draw the same marks.
  for (const id of TASKS) await check(live, live.locator(`[data-run-card="${run.id}"] [data-task-row="${id}"] [data-status-mark]`), `${id} on the ticket`, WANT[id]!)
})

test('on a phone, 390 x 844 with touch, the task rows and the graph\'s list draw the same marks', async ({ day, browser }) => {
  const { run, items } = await made(day)
  const page = await opened(browser, run, items, { live: true, width: 390, height: 844, touch: true })
  const read = async () => {
    budget(LOAD_MS)
    await page.goto(`/runs/${run.id}?view=tasks`)
    await readRows(page, run)
    await page.locator('[data-run-view]').getByRole('tab', { name: 'Graph' }).tap()
    for (const id of TASKS) await check(page, page.locator(`[data-graph-task-row="${id}"] [data-status-mark]`), `${id} in the graph's list`, WANT[id]!)
  }
  await read()
  await useTheme(page, 'light', '[data-bottom-bar]')
  await read()
})
