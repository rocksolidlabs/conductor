import type { Page } from '@playwright/test'
import { budget, dayPage, expect, test, useTheme } from './fixtures'
import { aligned, noOverlap, noSideways, probeColour, tabTo, uncut } from './layout'
import { ledgerRun, serveRun, type StubRun } from './run-fixtures'

// Task rows as a table that says the state once (UX1's PR-11, docs/plans/ux-review.md): state, task, who, what and actions, each
// read straight down; a row says its state once (the status language's mark and word), its attempt once, and its owner only when its
// task names one; Cancel, Copy branch and Ask Brakeman why sit behind the row's menu, and nothing in a row at rest is red. The Ledger
// run of run-fixtures.ts, served in the browser: schema and naming done, backfill checking on attempt 2, soak running on attempt 1,
// migrate failed out of its attempts (Retry and Accept named; its task names nate its owner), release-notes held by an open decision;
// every other task's outcome is ana's, the crew's acceptance owner. Rows are found by id.

/** The Ledger run (run-fixtures.ts), naming's gate applied since: its detail lists the decision, applied. */
function ledger(): StubRun {
  const run = ledgerRun()
  run.tasks.find((t) => t.id === 'naming')!.holds = [{ id: 'dr-41', type: 'approval', state: 'applied', question: 'May naming start?' }] as StubRun['tasks'][number]['holds']
  return run
}

/** A page of the day's with the run served before the run page loads, at `?view=tasks`, its rows drawn. */
async function opened(browser: Parameters<typeof dayPage>[0], run: StubRun, o: { width?: number; height?: number; touch?: boolean } = {}): Promise<{ page: Page; commands: string[] }> {
  const page = await dayPage(browser, { path: '/agents', ...o })
  await serveRun(page, run)
  // Every task command the page sends (a POST under the run's tasks), whoever answers it.
  const commands: string[] = []
  page.on('request', (r) => {
    if (r.method() === 'POST' && new URL(r.url()).pathname.startsWith(`/api/runs/${run.id}/tasks/`)) commands.push(new URL(r.url()).pathname)
  })
  await page.goto(`/runs/${run.id}?view=tasks`)
  await expect(page.locator('[data-task-row]')).toHaveCount(run.tasks.length)
  for (const t of run.tasks) await expect(page.locator(`[data-task-row="${t.id}"] [data-status-mark]`)).toBeVisible()
  return { page, commands }
}

const LOAD_MS = 15_000

test('at 1440 x 900 the columns line up, the state is said once, the attempt once, and the owner only where the task names one', async ({ day, browser }) => {
  void day
  const run = ledger()
  const { page } = await opened(browser, run)
  const rows = page.locator('[data-task-row] > [role="button"]')
  for (const col of ['who', 'what', 'actions']) await aligned(rows, `[data-task-col="${col}"]`)
  await expect(page.locator('[data-task-head]')).toBeVisible()
  for (const t of run.tasks) {
    const row = page.locator(`[data-task-row="${t.id}"]`)
    await expect(row.locator('[data-status-mark]'), `${t.id} draws one mark`).toHaveCount(1)
    await expect(row.locator('[data-task-state-badge]'), `${t.id} draws no state badge`).toHaveCount(0)
    const text = (await row.innerText()).replace(/\s+/g, ' ')
    expect(text.match(/attempt \d/g)?.length ?? 0, `${t.id} says an attempt at most once: ${text}`).toBeLessThanOrEqual(1)
  }
  await expect(page.locator('[data-task-row="backfill"] [data-task-attempt]')).toHaveText('attempt 2')
  await expect(page.locator('[data-task-row="soak"] [data-task-attempt]')).toHaveText('')
  // The acceptance owner is the header's; migrate's task names nate.
  await expect(page.locator('[data-task-owner]')).toHaveCount(1)
  await expect(page.locator('[data-task-row="migrate"] [data-task-owner]')).toHaveText('owner nate')
})

test('at rest nothing in a row is red but its state mark, in both themes', async ({ day, browser }) => {
  void day
  const run = ledger()
  const { page } = await opened(browser, run)
  const read = async () => {
    const error = await probeColour(page, 'var(--ui-error)')
    for (const t of run.tasks) {
      // Every drawn element of the row but the state mark's: its colour, its border's and its background's, against the error token.
      const colours = await page.locator(`[data-task-row="${t.id}"]`).evaluate((row) =>
        Array.from(row.querySelectorAll('*'))
          .filter((e) => !e.closest('[data-status-mark]'))
          .filter((e) => (e as HTMLElement).offsetParent !== null || e instanceof SVGElement)
          .flatMap((e) => {
            const s = getComputedStyle(e)
            return [s.color, s.backgroundColor, ...(parseFloat(s.borderTopWidth) > 0 ? [s.borderTopColor] : [])].filter((c) => c && c !== 'rgba(0, 0, 0, 0)')
          }),
      )
      for (const c of new Set(colours)) expect(await probeColour(page, c), `${t.id}: a colour of the row at rest is not the error red`).not.toBe(error)
    }
  }
  await read()
  await useTheme(page, 'light')
  await expect(page.locator(`[data-task-row="migrate"] [data-status-mark]`)).toBeVisible()
  await read()
})

test("the row's menu holds Cancel, Copy branch and Ask Brakeman why; the keyboard opens and closes it; Enter on a row selects it", async ({ day, browser }) => {
  void day
  const run = ledger()
  const { page, commands } = await opened(browser, run)
  // soak, running: Cancel and Copy branch are behind its menu, none in the row.
  await expect(page.locator('[data-task-cancel]')).toHaveCount(0)
  const button = page.locator('[data-task-menu="soak"]')
  await tabTo(page, button, 20, page.locator('[data-task-row="schema"] > [role="button"]'))
  await page.keyboard.press('Enter')
  const menu = page.getByRole('menu')
  await expect(menu).toBeVisible()
  await expect(menu.getByRole('menuitem')).toHaveText(['Cancel…', 'Copy branch'])
  await expect(page.locator('[data-task-cancel="soak"]')).toHaveCount(1)
  await page.keyboard.press('Escape')
  // A menu that has closed is not gone until its content is (AGENTS.md): wait for it, then the focus is back on the button.
  await expect(menu).toHaveCount(0)
  await expect(button).toBeFocused()
  // Cancel… asks first; Keep it sends nothing.
  await button.click()
  await page.locator('[data-task-cancel="soak"]').click()
  await expect(menu).toHaveCount(0)
  const modal = page.getByRole('dialog')
  await expect(modal.getByRole('button', { name: 'Cancel the task' })).toBeVisible()
  await modal.getByRole('button', { name: 'Keep it' }).click()
  await expect(modal).toHaveCount(0)
  expect(commands, 'Keep it sends no command').toEqual([])
  // migrate, out of its attempts: Preview this candidate (its last attempt submitted a commit), Copy branch and Ask Brakeman why (the
  // day's server has Brakeman), and no Cancel (the engine names none on a failed task). Retry and Accept anyway stay in the row.
  await expect(page.locator('[data-task-row="migrate"] [data-task-retry="migrate"]')).toBeVisible()
  await expect(page.locator('[data-task-row="migrate"] [data-task-accept="migrate"]')).toBeVisible()
  await page.locator('[data-task-menu="migrate"]').click()
  await expect(menu.getByRole('menuitem')).toHaveText(['Preview this candidate', 'Copy branch', 'Ask Brakeman why'])
  // Ask Brakeman why asks at once from where it is (issue 993): an item of the menu, no longer a link to Brakeman's page
  // (brakeman-where.spec.ts sends it, on a server with Brakeman on).
  await expect(page.locator('[data-task-ask-brakeman="migrate"]')).toHaveRole('menuitem')
  await expect(page.locator('[data-task-ask-brakeman="migrate"]')).not.toHaveAttribute('href', /./)
  await expect(page.locator('[data-task-cancel="migrate"]')).toHaveCount(0)
  await page.keyboard.press('Escape')
  await expect(menu).toHaveCount(0)
  // release-notes, held by an open decision, keeps Answer in the row.
  await expect(page.locator('[data-task-row="release-notes"] [data-task-answer="release-notes"]')).toBeVisible()
  // Enter on the row itself selects it: ?task= in the URL, its detail opened.
  const row = page.locator('[data-task-row="naming"] > [role="button"]')
  await row.focus()
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(/[?&]task=naming(&|$)/)
  await expect(page.locator('[data-task-detail-id="naming"]')).toBeVisible()
})

/**
 * At the page's narrow width, every row of `run` whole: who below the state, the actions (when the row has any) on a line of their own
 * below who and what, no two of the row's pieces over each other (an action over the id or the name), and the id, the state's word
 * and every action's label uncut.
 */
async function narrowRowsWhole(page: Page, run: StubRun): Promise<void> {
  await expect(page.locator('[data-task-head]')).toBeHidden()
  for (const t of run.tasks) {
    const row = page.locator(`[data-task-row="${t.id}"]`)
    // A row below the fold is scrolled into view first: uncut measures what a person can see.
    await row.scrollIntoViewIfNeeded()
    const state = await row.locator('[data-task-col="state"]').boundingBox()
    const who = await row.locator('[data-task-col="who"]').boundingBox()
    const what = await row.locator('[data-task-col="what"]').boundingBox()
    expect(who!.y, `${t.id}: who is on the second line`).toBeGreaterThanOrEqual(state!.y + state!.height - 1)
    const actions = row.locator('[data-task-col="actions"]')
    if (await actions.locator('button, a').count()) {
      const box = await actions.boundingBox()
      expect(box!.y, `${t.id}: the actions are below who and what`).toBeGreaterThanOrEqual(Math.max(who!.y + who!.height, what!.y + what!.height) - 1)
      if (await actions.locator('[data-slot="label"]').count()) await uncut(actions.locator('[data-slot="label"]'))
    } else await expect(actions, `${t.id}: a row with no action draws no line for them`).toBeHidden()
    await noOverlap(row.locator('> [role="button"]'))
    await uncut(row.locator('[data-task-id]'))
    await uncut(row.locator('[data-status-word]'))
  }
}

test('on a phone, 390 x 844 with touch, each row folds into two lines whole, and a tap on the menu copies the branch', async ({ day, browser }) => {
  void day
  budget(LOAD_MS)
  const run = ledger()
  const { page } = await opened(browser, run, { width: 390, height: 844, touch: true })
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])
  await narrowRowsWhole(page, run)
  // Measured before the toast, whose progress bar slides in from outside its own box.
  await noSideways(page)
  await page.locator('[data-task-menu="soak"]').tap()
  await page.locator('[data-task-copy-branch="soak"]').tap()
  await expect(page.getByText('Branch copied', { exact: true }).first()).toBeVisible()
})

test("narrow, a failed task's Retry and Accept anyway… lie clear of its id and name, with a short id and a 40-character one, in both themes", async ({ day, browser }) => {
  void day
  budget(LOAD_MS)
  // The Ledger run with a second failed task beside migrate: the same task under the longest id a task takes.
  const run = ledger()
  const migrate = run.tasks.find((t) => t.id === 'migrate')!
  const long = 'migrate-ledger-accounts-to-new-schema-v2'
  expect(long).toHaveLength(40)
  run.tasks.push({ ...structuredClone(migrate), id: long })
  const { page } = await opened(browser, run, { width: 390, height: 844, touch: true })
  for (const theme of ['dark', 'light'] as const) {
    if (theme === 'light') {
      await useTheme(page, 'light')
      await expect(page.locator('[data-task-row]')).toHaveCount(run.tasks.length)
    }
    for (const id of ['migrate', long]) {
      await expect(page.locator(`[data-task-row="${id}"] [data-task-retry="${id}"]`), `${theme}: ${id} draws Retry`).toBeVisible()
      await expect(page.locator(`[data-task-row="${id}"] [data-task-accept="${id}"]`), `${theme}: ${id} draws Accept anyway…`).toBeVisible()
    }
    await narrowRowsWhole(page, run)
    await noSideways(page)
  }
})

test("a selected task's applied decision is drawn in neutral ink, never green, in both themes", async ({ day, browser }) => {
  void day
  const run = ledger()
  const { page } = await opened(browser, run)
  const read = async () => {
    budget(LOAD_MS)
    await page.goto(`/runs/${run.id}?view=tasks&task=naming`)
    const badge = page.locator('[data-task-detail-id="naming"] [data-task-decision="dr-41"] [data-task-decision-badge="applied"]')
    await expect(badge).toHaveText('applied')
    const colour = await badge.evaluate((e) => getComputedStyle(e).color)
    // Neutral ink: the default text a neutral badge is drawn in, never the success colour nor its text tone.
    expect(await probeColour(page, colour), "the applied badge's text is the default text").toBe(await probeColour(page, 'var(--ui-text)'))
    expect(await probeColour(page, colour), 'the applied badge is not in the success colour').not.toBe(await probeColour(page, 'var(--ui-success)'))
    expect(await probeColour(page, colour), "the applied badge is not in the success text's tone").not.toBe(await probeColour(page, 'var(--ui-success-text)'))
  }
  await read()
  await useTheme(page, 'light')
  await read()
})
