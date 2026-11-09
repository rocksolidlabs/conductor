import type { Browser, Page } from '@playwright/test'
import { expect, stopStrays, test } from './fixtures'
import type { Started } from './server'
import { budget as budgetMs, decision, decisionServer, decisionUntil, decisionsOf, makeTicket, propose, type DecisionServer } from './decisions-helpers'

// DR1's records on the decision page (docs/plans/decision-inbox.md, Program item MB0, PR-4), on a phone (390 x 844, touch), on a server
// of the spec's own (decisions-helpers: its decisions are its alone, and it is restarted over its data directory). A ticket's approved
// revision names an approval, `ship`, before `deploy`; two runs are launched from it, each opening its gate; a third run's agent asks
// the operator a clarification and waits. From the Decide tab, through DR1's card and controls drawn inside the item:
//
// - the first gate: Approve picked, the record moved by a comment through the API, Confirm refused with words and nothing recorded;
//   read again, approved, its four states through to applied;
// - the ticket page's record links to its item on the decision page;
// - the agent's decision: Ask for more information sent, the record still open, the note named in its history;
// - the server restarted over its data directory: the first gate's four states the same, and it is in the server's Recent;
// - the second gate, its run interrupted by the restart: answered, it stays under "Answered, not yet applied" after a reload, until
//   the run is continued and the answer applied, when it leaves for Recent.
//
// Every item is found by its ref; every wait is on the record through the API or on what the page draws. Each step raises the test's
// timeout by its own waits' caps as it takes them (budget).
test.describe.configure({ mode: 'serial' })

const PROJECT = 'DPD'
const STEP_MS = 15_000
test.use({ actionTimeout: STEP_MS, navigationTimeout: STEP_MS })
/** Raises the test's timeout by `waits` waits of `ms` each (a step's own caps, as it takes them). */
const budget = (waits: number, ms = STEP_MS) => budgetMs(waits * ms)

let srv: DecisionServer
let ticket = ''
let gatedCrew = ''
let askingCrew = ''
const runs: string[] = []

test.beforeAll(async () => {
  srv = await decisionServer('decision-page-durable', [{ id: 'worker-clarifies', from: 'worker', env: { STUB_DECISION: 'clarification' } }])
  await stopStrays(srv.api, [])
  ticket = await makeTicket(srv.api, PROJECT, 'Deploy through a gate')
  const crew = (body: object) => srv.api.ok<{ crew: { id: string } }>('POST', '/api/crews', { cwd: '', where: 'server', isolation: 'worktree', openAfterLaunch: false, yolo: null, ...body })
  gatedCrew = (
    await crew({
      name: 'e2e durable gate',
      goal: 'build, then deploy once approved',
      members: [
        { name: 'builder', agentId: 'worker', prompt: '', start: { when: 'task' } },
        { name: 'deployer', agentId: 'worker-cli', prompt: '', start: { when: 'task' } },
      ],
      tasks: [
        { id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.' },
        { id: 'deploy', member: 'deployer', kind: 'work', instructions: 'Deploy it.', needs: ['build'] },
      ],
    })
  ).crew.id
  askingCrew = (
    await crew({
      name: 'e2e durable asker',
      goal: 'ask the operator first',
      members: [{ name: 'asker', agentId: 'worker-clarifies', prompt: '', start: { when: 'task' } }],
      tasks: [{ id: 'ask', member: 'asker', kind: 'work', instructions: 'Ask, then build.' }],
    })
  ).crew.id
})

test.afterAll(async () => {
  if (!srv) return
  for (const id of runs) await srv.api.stopRun(id).catch(() => {})
  await srv.stop()
})

/** A phone of the spec's server, as the operator. */
async function phonePage(browser: Browser, s: Started): Promise<Page> {
  const ctx = await browser.newContext({ baseURL: s.baseURL, viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })
  await ctx.addInitScript(
    ({ t }) => {
      localStorage.setItem('conductor.workbenchToken', t)
      localStorage.setItem('conductor.displayName', 'Nate')
    },
    { t: s.token },
  )
  return ctx.newPage()
}

/** DR1's card's four states as drawn: each step's key and status. */
function states(card: ReturnType<Page['locator']>): Promise<Record<string, string>> {
  return card.locator('[data-decision-step]').evaluateAll((els) => Object.fromEntries(els.map((e) => [e.getAttribute('data-decision-step'), e.getAttribute('data-step-status')])))
}

test('a gate answered from the Decide tab through DR1\'s card, a stale response refused, the ticket\'s link, Ask for more, a restart, and an answer waiting for its run', async ({ browser }) => {
  test.slow()
  const question = 'May deploy go out?'
  // Revision 1 with the gate, approved; two runs launched from it, each opening its own gate with its first transaction.
  const plan = await propose(srv.api, ticket, gatedCrew, [{ id: 'ship', question, kind: 'approval', before: ['deploy'] }])
  await srv.api.ok('POST', `/api/dispatch/plans/${plan}/approve`, { opId: 'dpd-ap1' })
  budget(2, 130_000)
  for (const op of ['dpd-l1', 'dpd-l2']) runs.push((await srv.api.ok<{ launch: { runId: string } }>('POST', `/api/dispatch/tickets/${ticket}/launch`, { opId: op, plan })).launch.runId)
  const [run1, run2] = runs as [string, string]
  const gate1 = (await decisionsOf(srv.api, run1)).find((d) => d.gate === 'ship')!.id
  const gate2 = (await decisionsOf(srv.api, run2)).find((d) => d.gate === 'ship')!.id
  // The asking run: its attempt asks a clarification and waits, its session live.
  const asking = (await srv.api.launchCrew(askingCrew)).id
  runs.push(asking)
  budget(1, 120_000)
  let asked = ''
  await expect
    .poll(async () => (asked = (await decisionsOf(srv.api, asking)).find((d) => d.asker?.task === 'ask' && d.state === 'open')?.id ?? ''), { timeout: 120_000, intervals: [500], message: "ask's attempt asks its decision" })
    .toMatch(/^dr-[1-9][0-9]*$/)

  // The Decide tab: the first gate's row, and its item drawn through DR1's card, with the inbox's brief and what waits around it.
  const phone = await phonePage(browser, srv.started)
  budget(6)
  await phone.goto('/decisions')
  await expect(phone.locator('[data-bottom-bar]')).toBeVisible()
  await phone.locator(`[data-inbox-row="decision:${gate1}"]`).tap()
  const item = phone.locator(`[data-inbox-item="decision:${gate1}"]`)
  const card = item.locator(`[data-decision-card][data-decision-id="${gate1}"]`)
  await expect(card.locator('[data-decision-question]')).toHaveText(question)
  await expect(item.locator('[data-inbox-blocking]')).toContainText('deploy')
  // Nothing of the record twice: no inbox question, choices or steps beside DR1's card.
  await expect(item.locator('[data-inbox-question]')).toHaveCount(0)
  await expect(item.locator('[data-inbox-actions]')).toHaveCount(0)
  await expect(item.locator('[data-inbox-delivery]')).toHaveCount(0)

  // A response at a stale record: Approve picked at the rev shown, the record moved by a comment, Confirm refused with words.
  budget(6)
  const respond = item.locator(`[data-decision-respond][data-decision-id="${gate1}"]`)
  await respond.locator('[data-decision-choice-label="Approve"]').tap()
  await expect(respond.locator('[data-decision-confirm-step]')).toBeVisible()
  const shown = await decision(srv.api, gate1)
  await srv.api.ok('POST', `/api/decisions/${gate1}/comment`, { opId: 'dpd-c1', kind: 'comment', text: 'Moved from the desktop.', expectedRev: shown.rev })
  await expect(respond.locator('[data-decision-moved]')).toBeVisible()
  await respond.locator('[data-decision-confirm]').tap()
  await expect(respond.locator('[data-decision-refusal-words]')).not.toBeEmpty()
  expect((await decision(srv.api, gate1)).state).toBe('open')
  // Read again, then approved at the rev it is at now: recorded, and its four states through to applied.
  budget(4)
  await respond.locator('[data-decision-read-again]').tap()
  await expect(respond.locator('[data-decision-refusal]')).toHaveCount(0)
  await respond.locator('[data-decision-choice-label="Approve"]').tap()
  await respond.locator('[data-decision-confirm]').tap()
  await expect(respond.locator('[data-decision-receipt]')).toContainText(`Recorded: ${gate1} answered by Nate`)
  await decisionUntil(srv.api, gate1, 'applied')
  budget(1)
  await expect.poll(() => states(card), { timeout: STEP_MS }).toEqual({ open: 'done', recorded: 'done', validated: 'done', applied: 'done' })
  const before = await states(card)

  // The ticket page's record links to its item on the decision page.
  budget(3)
  await phone.goto(`/dispatch/${PROJECT}/1`)
  const link = phone.locator(`[data-ticket-decision-record="${gate1}"] [data-ticket-decision-item="${gate1}"]`)
  await expect(link).toBeVisible({ timeout: 30_000 })
  await link.tap()
  await expect(phone).toHaveURL(new RegExp(`/decisions\\?item=decision%3A${gate1}$`))
  await expect(phone.locator(`[data-inbox-item="decision:${gate1}"] [data-decision-card] [data-decision-question]`)).toHaveText(question)

  // The agent's decision: Ask for more information, typed to its asking session; the record stays open and its history names it.
  budget(5)
  await phone.goto(`/decisions?item=${encodeURIComponent(`decision:${asked}`)}`)
  const asks = phone.locator(`[data-inbox-item="decision:${asked}"] [data-decision-respond]`)
  const note = 'Which tests did you run?'
  await asks.locator('[data-decision-comment-text]').fill(note)
  await expect(asks.locator('[data-decision-comment-text]')).toHaveValue(note)
  await expect(asks.locator('[data-decision-more]')).toBeEnabled()
  await asks.locator('[data-decision-more]').tap()
  await expect(asks.locator('[data-decision-op-words]')).not.toBeEmpty()
  await expect.poll(async () => (await decision(srv.api, asked)).history?.some((h) => h.kind === 'info_requested') ?? false, { timeout: STEP_MS }).toBe(true)
  expect((await decision(srv.api, asked)).state).toBe('open')
  const history = phone.locator(`[data-inbox-item="decision:${asked}"] [data-decision-card]`)
  await history.locator('[data-decision-history-toggle]').tap()
  await expect(history.locator('[data-decision-history]')).toContainText(note)
  await phone.context().close()

  // The server restarted over its data directory: the first gate's four states the same, and it is in the server's Recent; the
  // second gate's run interrupted, its gate open.
  budget(1, 60_000)
  await srv.restart()
  budget(1, 30_000)
  await expect.poll(async () => (await srv.api.run(run2)).state, { timeout: 30_000, intervals: [500], message: 'the second run is recovered interrupted' }).toBe('interrupted')
  expect((await decision(srv.api, gate2)).state).toBe('open')
  const after = await phonePage(browser, srv.started)
  budget(4)
  await after.goto('/decisions')
  await expect(after.locator(`[data-recent-decision="decision:${gate1}"]`)).toBeVisible({ timeout: 30_000 })
  await after.locator(`[data-recent-decision="decision:${gate1}"]`).tap()
  const again = after.locator(`[data-inbox-item="decision:${gate1}"] [data-decision-card]`)
  await expect.poll(() => states(again), { timeout: STEP_MS }).toEqual(before)

  // The second gate answered while its run is interrupted: recorded, waiting for the run to be continued.
  budget(6)
  await after.goto(`/decisions?item=${encodeURIComponent(`decision:${gate2}`)}`)
  const second = after.locator(`[data-inbox-item="decision:${gate2}"]`)
  await second.locator('[data-decision-choice-label="Approve"]').tap()
  await second.locator('[data-decision-confirm]').tap()
  await expect(second.locator('[data-decision-receipt]')).toContainText(`Recorded: ${gate2} answered by Nate`)
  await decisionUntil(srv.api, gate2, 'answered')
  await expect(second.locator('[data-decision-state-words]')).toHaveText('Recorded: waiting for the run to be continued', { timeout: STEP_MS })
  // After a reload, still under "Answered, not yet applied", from the server's list.
  budget(3)
  await after.goto('/decisions')
  const tracking = after.locator(`[data-decisions-tracking] [data-inbox-row="decision:${gate2}"]`)
  await expect(tracking).toBeVisible({ timeout: 30_000 })
  await after.reload()
  await expect(tracking).toBeVisible({ timeout: 30_000 })

  // The run continued: the continuation applies the answer, and the record leaves the list for Recent.
  budget(4)
  const resumed = await srv.api.call<{ run: { id: string } }>('POST', `/api/runs/${encodeURIComponent(run2)}/resume`)
  if (resumed.body?.run?.id) runs.push(resumed.body.run.id)
  await decisionUntil(srv.api, gate2, 'applied')
  await expect(tracking).toHaveCount(0, { timeout: 30_000 })
  await expect(after.locator(`[data-recent-decision="decision:${gate2}"]`)).toBeVisible({ timeout: STEP_MS })
  await after.context().close()
})
