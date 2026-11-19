import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Page } from '@playwright/test'
import { budget, dayPage, expect, test, type Session } from './fixtures'
import { DAY_STEP_MS, type Day } from './day'
import { lines, rowsInView, tabTo, tapTarget, uncut } from './layout'

// A question in the narrow sidebar (the UX review's P5, owner decision 7): below 280 px a row shows two lines of its question, and
// the whole question with its choices or its reply field opens in a panel beside the row, to the right of the sidebar, while the row
// has the focus or from its Answer button. The digits answer the focused row as before. At 280 px and wider, and on the phone's list,
// the question stays whole in the row. The sidebar opens at 280 px (#1022), so each test here drags it to its minimum, 256 px, a
// narrow list. Every session here is the test's own on the day's server, ended and removed before the test
// ends (the day's guard).

/** A question of exactly 500 bytes, the server's bound (session.MaxAttentionMessage). */
const LONGEST = ('Should I rewrite the ledger reconciler so that every host replays its journal before the schema 5 cutover, ' + 'and keep the old path behind a flag until the next release? ').repeat(4).slice(0, 500)
/** The last choice at the server's bound for a label (session.MaxOptionLabel, 60 runes). */
const LAST = 'No, keep the old path; write the replay as a separate tool..'
const CHOICES = ['Yes', "Yes, and don't ask again", LAST]

/** The day's sidebar, measured: its root and its drawn width. */
const sidebar = (page: Page) => page.locator('#dashboard-sidebar-main')
const sidebarWidth = (page: Page) => sidebar(page).evaluate((el) => el.getBoundingClientRect().width)
const rowOf = (page: Page, id: string) => page.locator(`[data-session-list="sidebar"] [data-sidebar-row="s:${id}"]`)
const panelOf = (page: Page, id: string) => page.locator(`[data-row-panel][data-panel-for="s:${id}"]`)

/** A session of the test's own on the day's `plain` agent, asking `message` with `choices` (none: a reply). */
async function asking(day: Day, name: string, message: string, choices: string[] = CHOICES): Promise<Session> {
  const s = await day.api.ok<Session>('POST', '/api/sessions', { agentId: 'day-finished', name })
  budget(DAY_STEP_MS)
  // Its first turn ends with the stub's report of done: a question posted before that would be overwritten by it.
  await expect.poll(async () => (await day.api.session(s.id)).attention?.state, { timeout: DAY_STEP_MS, message: `${name} finishes its first turn` }).toBe('done')
  await ask(day, s.id, message, choices)
  return s
}
function ask(day: Day, id: string, message: string, choices: string[] = CHOICES) {
  return day.api.ok('POST', `/api/sessions/${id}/attention`, { state: 'needs_input', message, options: choices.map((label, i) => ({ label, input: `${i + 1}\r` })) })
}
/** Ends the test's own sessions and removes them (a second stop), so the day's guard finds the day as it was. */
async function end(day: Day, ids: string[]) {
  for (const id of ids) {
    await day.api.stopSession(id)
    await expect.poll(async () => (await day.api.session(id)).status, { message: 'the test\'s session ends' }).toMatch(/^(exited|stopped)$/)
    await day.api.stopSession(id)
  }
}

/** Drags the sidebar's handle to `x`. */
async function dragTo(page: Page, x: number) {
  const box = (await page.locator('[role="separator"][aria-controls="dashboard-sidebar-main"]').boundingBox())!
  const y = box.y + box.height / 2
  await page.mouse.move(box.x + box.width / 2, y)
  await page.mouse.down()
  await page.mouse.move(x, y, { steps: 4 })
  await page.mouse.up()
}

/** The sidebar's minimum, 256 px: a narrow list. */
const MIN = 256

/** Drags the sidebar from the width it opens at (280 px) to its minimum, and waits for it to be drawn there. */
async function atMinimum(page: Page) {
  await expect.poll(() => sidebarWidth(page)).toBeCloseTo(280, 0)
  await dragTo(page, 100)
  await expect.poll(() => sidebarWidth(page)).toBeCloseTo(MIN, 0)
}

/** The text a stub session was typed (its transcript): '' until it is written. */
function transcript(day: Day, s: Session): string {
  const file = join(day.server.home, '.stub-sessions', `${s.agentSession?.id ?? ''}.txt`)
  return existsSync(file) ? readFileSync(file, 'utf8') : ''
}

for (const [w, h] of [
  [1440, 900],
  [1280, 720],
] as const) {
  test(`at the minimum, 256 px, a question at the server's bound is two lines in its row and whole in the panel beside it, at ${w}×${h}`, async ({ day, browser }) => {
    const a = await asking(day, 'e2e bound question', LONGEST)
    const b = await asking(day, 'e2e second asker', 'Run the migration now?')
    try {
      const page = await dayPage(browser, { width: w, height: h, path: '/crews' })
      await atMinimum(page)
      const row = rowOf(page, a.id)
      const prompt = row.locator('[data-row-prompt]')
      await expect(prompt).toHaveAttribute('data-row-clamped', '')
      await lines(prompt, 2)
      await expect(row.locator('[data-row-choice]')).toHaveCount(0)
      // Reached by Tab: the panel for the row shows, beside the sidebar and inside the window, the question whole, every choice
      // whole; the focus stays on the row's link.
      const link = row.locator('a[href]').first()
      await tabTo(page, link, 40, page.getByPlaceholder('Filter sessions, runs, people'))
      const panel = panelOf(page, a.id)
      await expect(panel).toBeVisible()
      const [p, side] = [(await panel.boundingBox())!, (await sidebar(page).boundingBox())!]
      expect(p.x, "the panel at the sidebar's right edge or beyond").toBeGreaterThanOrEqual(side.x + side.width - 0.5)
      expect(p.x + p.width, 'the panel inside the window').toBeLessThanOrEqual(w + 0.5)
      expect(p.y + p.height, 'the panel inside the window').toBeLessThanOrEqual(h + 0.5)
      await expect(panel.locator('[data-panel-question]')).toHaveText(LONGEST)
      const choices = panel.locator('[data-row-choice]')
      await expect(choices).toHaveCount(3)
      for (let i = 0; i < 3; i++) {
        await expect(choices.nth(i)).toContainText(CHOICES[i]!)
        await uncut(choices.nth(i).locator('[data-slot="label"]'))
      }
      await expect(link).toBeFocused()
      // J moves the focus to the next row and the panel with it; K back; Escape closes it.
      const order = await page.locator('[data-session-list="sidebar"] [data-sidebar-section="needs"] [data-row-kind="session"]').evaluateAll((els) => els.map((e) => e.getAttribute('data-sidebar-row')))
      const next = order[order.indexOf(`s:${a.id}`) + 1]
      if (next === `s:${b.id}`) {
        await page.keyboard.press('j')
        await expect(page.locator('[data-row-panel]')).toHaveAttribute('data-panel-for', `s:${b.id}`)
        await page.keyboard.press('k')
      } else {
        await rowOf(page, b.id).locator('a[href]').first().focus()
        await expect(page.locator('[data-row-panel]')).toHaveAttribute('data-panel-for', `s:${b.id}`)
        await link.focus()
      }
      await expect(page.locator('[data-row-panel]')).toHaveAttribute('data-panel-for', `s:${a.id}`)
      await page.keyboard.press('Escape')
      await expect(page.locator('[data-row-panel]')).toHaveCount(0)
      // Focused again: the digit 2 answers the row, its attention clears, and the panel is gone.
      await link.focus()
      await expect(panel).toBeVisible()
      await page.keyboard.press('2')
      // The stub takes the 2 as a turn and ends it (done): asked again only after that, or its end would overwrite the question.
      await expect.poll(async () => (await day.api.session(a.id)).attention?.state ?? '', { message: 'the 2 answered the question, and the turn it began ended' }).toBe('done')
      await expect(page.locator('[data-row-panel]')).toHaveCount(0)
      // Dragged to 300 px: the question is whole in the row with its choices, and focus opens no panel.
      await ask(day, a.id, LONGEST)
      await expect(prompt).toHaveText(LONGEST)
      await dragTo(page, 300)
      await expect.poll(() => sidebarWidth(page)).toBeCloseTo(300, 0)
      await expect(prompt).not.toHaveAttribute('data-row-clamped', '')
      await expect(row.locator('[data-row-choice]')).toHaveCount(3)
      await link.focus()
      await expect(row).toHaveAttribute('data-row-focused', '')
      await expect(page.locator('[data-row-panel]')).toHaveCount(0)
      // Back to the minimum: clamped again, and again after the row is drawn anew (asked again).
      await link.blur()
      await dragTo(page, 100)
      await expect.poll(() => sidebarWidth(page)).toBeCloseTo(MIN, 0)
      await expect(prompt).toHaveAttribute('data-row-clamped', '')
      await ask(day, a.id, `${LONGEST.slice(0, 480)} once more?`)
      await expect(prompt).toHaveText(`${LONGEST.slice(0, 480)} once more?`)
      await expect(prompt).toHaveAttribute('data-row-clamped', '')
      await lines(prompt, 2)
      await expect(row.locator('[data-row-choice]')).toHaveCount(0)
    } finally {
      await end(day, [a.id, b.id])
    }
  })
}

test("the panel's reply field keeps the focus and every key while the rows change, and Enter sends the line", async ({ day, browser }) => {
  const a = await asking(day, 'e2e panel reply', 'Which branch should I base the fix on?', [])
  const ids = [a.id]
  try {
    const page = await dayPage(browser, { path: '/crews' })
    await atMinimum(page)
    const row = rowOf(page, a.id)
    await expect(row.locator('[data-row-prompt]')).toHaveAttribute('data-row-clamped', '')
    // Answer moves the focus into the panel's field.
    const open = row.locator('[data-row-answer-open]')
    await expect(open).toHaveAttribute('aria-expanded', 'false')
    await open.click()
    const panel = panelOf(page, a.id)
    const field = panel.locator('[data-row-reply] input, input[data-row-reply]').first()
    await expect(field).toBeFocused()
    await expect(open).toHaveAttribute('aria-expanded', 'true')
    await page.keyboard.type('ma')
    // Another session of the test's asks meanwhile: the rows are drawn again, and the field keeps the focus and its keys (#596).
    const b = await asking(day, 'e2e panel rows change', 'Ship it?')
    ids.push(b.id)
    await expect(rowOf(page, b.id)).toBeVisible()
    await expect(field).toBeFocused()
    await page.keyboard.type('in')
    await expect(field).toHaveValue('main')
    await expect(panel).toBeVisible()
    await page.keyboard.press('Enter')
    await expect.poll(() => transcript(day, a), { message: 'the line was typed' }).toContain('main\n')
    // A fresh panel: Escape gives the focus back to the row. Asked once the turn the line began has ended (its done would overwrite it).
    await expect.poll(async () => (await day.api.session(a.id)).attention?.state ?? '', { message: 'the turn the line began ended' }).toBe('done')
    await ask(day, a.id, 'And the next branch?', [])
    // This time from the keyboard: Enter on Answer moves the focus into the panel, as its click does, and opens no page.
    await open.focus()
    await page.keyboard.press('Enter')
    await expect(panel.locator('[data-row-reply] input, input[data-row-reply]').first()).toBeFocused()
    await expect(page).toHaveURL(/\/crews$/)
    await page.keyboard.press('Escape')
    await expect(page.locator('[data-row-panel]')).toHaveCount(0)
    await expect(row.locator('a[href]').first()).toBeFocused()
  } finally {
    await end(day, ids)
  }
})

test('on a touch screen a tap on Answer opens the panel and a tap on a choice answers; the phone\'s list keeps the question whole', async ({ day, browser }) => {
  const a = await asking(day, 'e2e touch question', 'Apply the patch to every host?')
  try {
    const page = await dayPage(browser, { width: 1280, height: 800, touch: true, path: '/crews' })
    await atMinimum(page)
    const row = rowOf(page, a.id)
    await expect(row.locator('[data-row-prompt]')).toHaveAttribute('data-row-clamped', '')
    await row.locator('[data-row-answer-open]').tap()
    const panel = panelOf(page, a.id)
    await expect(panel).toBeVisible()
    await panel.locator('[data-row-choice="1"]').tap()
    await expect.poll(async () => (await day.api.session(a.id)).attention?.state ?? '', { message: 'the tap answered the question, and the turn it began ended' }).toBe('done')
    await expect(page.locator('[data-row-panel]')).toHaveCount(0)
    // The phone's list: the question whole in the row, no Answer button, choices 44 px.
    await ask(day, a.id, LONGEST)
    const phone = await dayPage(browser, { width: 390, height: 844, touch: true, path: '/sessions' })
    const item = phone.locator(`[data-session-list="page"] [data-sidebar-row="s:${a.id}"]`)
    await expect(item.locator('[data-row-prompt]')).toHaveText(LONGEST)
    await expect(item.locator('[data-row-prompt]')).not.toHaveAttribute('data-row-clamped', '')
    await expect(item.locator('[data-row-answer-open]')).toHaveCount(0)
    await item.locator('[data-row-choice="3"]').scrollIntoViewIfNeeded()
    await tapTarget(item.locator('[data-row-choice]'))
  } finally {
    await end(day, [a.id])
  }
})

// With the pages as six tiles at the sidebar's foot the list is about 625 px tall at 900 px, where the column of pages left it
// about 440 (the review counted two asking rows in view, each holding its question whole). A plan's, a task's and a decision's row
// of Needs you (UX1 PR-13) count as asking rows. The plan asked for five: an asking session's row in the narrow list is 100 to 116 px tall and
// the busy day's runs each add a header and an "and N more" line, so the list holds four.
test("on the day's busy part, at the minimum, the list shows more asking rows whole than with each question whole", async ({ day, browser }) => {
  await day.part('busy')
  const page = await dayPage(browser, { path: '/crews' })
  await atMinimum(page)
  const list = page.locator('[data-session-list="sidebar"]')
  await expect(list).toHaveAttribute('data-list-width', 'narrow')
  await expect(list.locator('[data-row-clamped]').first()).toBeVisible()
  // A row that asks: a session's question, or (UX1 PR-13) a plan's, a task's or a decision's row of Needs you.
  expect(await rowsInView(list, '[data-row-kind="session"][data-row-state="needs"], [data-row-kind="item"]')).toBeGreaterThanOrEqual(4)
})
