import type { Page } from '@playwright/test'
import { expect, test, type Session } from './fixtures'

// A question that mirrors a durable decision (Program item DR1) is answered
// only as the decision, by the operator: no page offers it a typed reply.
// Every place that draws a reply field for an attention reads the same one
// answer (noReply, web/app/utils/attention.ts), and says where the decision
// is answered instead: the sidebar's row, the Yard's queue and the command
// palette (#638, review round 1). The stub, a worker of a managed run, asks
// the decision over its own token when its task's instructions say STUB
// DECIDE, and submits nothing while it waits.
test.describe.configure({ mode: 'serial' })

let crewId = ''
const runs: string[] = []

// The walk's waits run one after another: each step raises the test's timeout by the caps of its own waits as it takes them
// (budget, the pattern of palette-rows.spec.ts), so a loaded box that spends every cap with each step passing still passes; the
// config's 120 s stays above them, for the launch and the waits between the steps.
/** The cap of every wait a step makes but the first poll's: the config's expect bound, and the actions' (a goto, a key). */
const STEP_MS = 15_000
const QUESTION_MS = 60_000
test.use({ actionTimeout: STEP_MS, navigationTimeout: STEP_MS })
/** Raises the running test's timeout by `ms`, as a step is about to make waits of those caps. */
function budget(ms: number) {
  const info = test.info()
  info.setTimeout(info.timeout + ms)
}

const crewBody = {
  name: 'e2e decision mirror',
  goal: 'ask the operator first',
  cwd: '',
  where: 'server',
  isolation: 'worktree',
  openAfterLaunch: false,
  yolo: null,
  members: [{ name: 'asker', agentId: 'worker-cli', prompt: '', start: { when: 'task' } }],
  tasks: [{ id: 'ask', member: 'asker', kind: 'work', instructions: 'STUB DECIDE: ask the operator which name to take.' }],
}

test.beforeAll(async ({ api }) => {
  crewId = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', crewBody)).crew.id
})

test.afterAll(async ({ api }) => {
  for (const id of runs) await api.stopRun(id)
  if (crewId) await api.call('DELETE', `/api/crews/${encodeURIComponent(crewId)}`)
})

/** Opens the palette (Alt+/, once the layout that listens for it is mounted) and returns it. */
async function openPalette(page: Page) {
  await expect(page.locator('#dashboard-sidebar-main')).toBeAttached()
  await page.keyboard.press('Alt+/')
  const palette = page.locator('[data-palette]')
  await expect(palette).toBeVisible()
  return palette
}

test("a decision's question takes no reply in the sidebar's row, the Yard's queue or the palette: each says where it is answered", async ({ page, api }) => {
  const run = await api.launchCrew(crewId)
  runs.push(run.id)
  // The attempt's session, once it shows the decision's question.
  let asking: Session | undefined
  budget(QUESTION_MS)
  await expect
    .poll(
      async () => {
        const list = (await api.ok<{ sessions: Session[] }>('GET', '/api/sessions')).sessions
        asking = list.find((s) => s.crew?.runId === run.id && s.attention?.state === 'needs_input' && !!s.attention?.decision)
        return asking?.attention?.decision ?? ''
      },
      { timeout: QUESTION_MS, intervals: [500], message: "the attempt's session shows the decision's question" },
    )
    .toMatch(/^dr-[1-9][0-9]*$/)
  const s = asking!
  const words = `The operator answers ${s.attention!.decision} in Conductor`

  // The sidebar's row: the words, no reply field, no choice.
  budget(4 * STEP_MS) // the page, the words, no reply, no choice
  await page.goto('/')
  const row = page.locator(`[data-session-list="sidebar"] [data-sidebar-row="s:${s.id}"]`).first()
  await expect(row.locator('[data-row-decision]')).toHaveText(words, { timeout: 15_000 })
  await expect(row.locator('[data-row-reply]')).toHaveCount(0)
  await expect(row.locator('[data-row-choice]')).toHaveCount(0)

  // The Yard's queue: the card says it, and has no reply box.
  budget(3 * STEP_MS) // the page, the words, no box
  await page.goto('/yard')
  const card = page.locator(`[data-wall-queue] [data-queue-session="${s.id}"]`)
  await expect(card.locator('[data-queue-decision]')).toHaveText(words, { timeout: 15_000 })
  await expect(card.locator('input')).toHaveCount(0)

  // The palette's row for the session: the words where a reply field would be.
  budget(6 * STEP_MS) // the page, the sidebar, the key, the palette, the words, no reply
  await page.goto('/')
  const palette = await openPalette(page)
  const prow = palette.locator(`[data-palette-row="session:${s.id}"]`)
  await expect(prow.locator('[data-palette-held]')).toHaveText(words, { timeout: 15_000 })
  await expect(prow.locator('[data-palette-reply]')).toHaveCount(0)
})
