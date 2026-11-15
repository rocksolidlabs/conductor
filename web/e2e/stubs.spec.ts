import { dayPage, expect, test } from './fixtures'
import { inboxItem, stubInbox, summary, type InboxKind } from './inbox-stub'
import { runInfo, serveRun } from './run-fixtures'

// The two browser stubs (inbox-stub.ts, run-fixtures.ts) held to the real server (UX1's PR-1, docs/plans/ux-review.md): the run page
// drawn from serveRun shows every task, and made no read of the run the module did not answer; an item of each kind from inboxItem
// carries every key the day's real item of that kind carries. A read the run page gains, or a field the server adds to an item,
// fails here, not in a layout spec that would blame the layout.

for (const tasks of [6, 40] as const) {
  test(`the run page drawn from serveRun with ${tasks} tasks shows every task, and reads nothing of the run the stub does not answer`, async ({ day, browser }) => {
    const run = runInfo({ tasks })
    const page = await dayPage(browser, { path: '/' })
    await stubInbox(page, [])
    const served = await serveRun(page, run)
    await page.goto(`/runs/${run.id}?view=tasks`)
    await expect(page.locator('[data-crew-run-header]')).toBeVisible()
    for (const t of run.tasks) {
      const row = page.locator(`[data-task-row="${t.id}"]`)
      await expect(row, t.id).toHaveCount(1)
      await expect(row, t.id).toHaveAttribute('data-task-state', t.state)
    }
    // Every state at once (as many as the tasks hold), drawn: the stub's default.
    expect(new Set(run.tasks.map((t) => t.state)).size).toBe(Math.min(tasks, 7))
    expect(served.unanswered.filter((r) => r.includes(run.id))).toEqual([])
    void day
  })
}

test("an inboxItem of each kind carries every key the day's real item of that kind carries", async ({ day }) => {
  await day.part('waiting')
  const real = await day.inbox()
  for (const kind of ['question', 'plan', 'task', 'decision'] as InboxKind[]) {
    const items = real.filter((it) => it.kind === kind)
    expect(items.length, `the day holds a ${kind}`).toBeGreaterThan(0)
    const keys = [...new Set(items.flatMap((it) => Object.keys(it)))].sort()
    const stub = Object.keys(summary(inboxItem(kind)))
    expect(keys.filter((k) => !stub.includes(k)), `the keys of the day's ${kind} the stub lacks`).toEqual([])
  }
})
