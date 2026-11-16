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
let longCrew = ''
const runs: string[] = []

// Choices as long as the server takes them (80 characters, `ledger.MaxOptionLabel`), with no tradeoffs line, two of each pair
// differing only in their last word: with the option rows gone where the buttons show, each button must say its choice whole.
const LONG = {
  approval: ['Ship the change to every region tonight once its checks pass, then tell us: east', 'Ship the change to every region tonight once its checks pass, then tell us: west'],
  clarification: ['Name the new module after the team that owns it, keeping the old name too: alpha', 'Name the new module after the team that owns it, keeping the old name too: bravo'],
}
const longOptions = (labels: string[]) => JSON.stringify(labels.map((label) => ({ label })))

test.beforeAll(async () => {
  srv = await decisionServer('decision-page-durable', [
    { id: 'worker-clarifies', from: 'worker', env: { STUB_DECISION: 'clarification' } },
    { id: 'worker-approves-long', from: 'worker', env: { STUB_DECISION: 'approval', STUB_DECISION_OPTIONS: longOptions(LONG.approval) } },
    { id: 'worker-clarifies-long', from: 'worker', env: { STUB_DECISION: 'clarification', STUB_DECISION_OPTIONS: longOptions(LONG.clarification) } },
  ])
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
  longCrew = (
    await crew({
      name: 'e2e durable long choices',
      goal: 'ask with long choices',
      members: [
        { name: 'approver', agentId: 'worker-approves-long', prompt: '', start: { when: 'task' } },
        { name: 'clarifier', agentId: 'worker-clarifies-long', prompt: '', start: { when: 'task' } },
      ],
      tasks: [
        { id: 'approve', member: 'approver', kind: 'work', instructions: 'Ask an approval, then build.' },
        { id: 'clarify', member: 'clarifier', kind: 'work', instructions: 'Ask a clarification, then build.' },
      ],
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

// The measures below are this spec's own until the stream's shared helpers land (UX1 PR-1, web/e2e/layout.ts: `tapTarget`,
// `noSideways`, `inside`), where they move. Each fails when it matched nothing.

/** A control a thumb can hit: at least 44 px each way. */
async function tapTarget(l: ReturnType<Page['locator']>) {
  const b = await l.boundingBox()
  expect(b, 'the control is drawn').not.toBeNull()
  expect(Math.min(b!.width, b!.height), 'the control is 44 px each way').toBeGreaterThanOrEqual(44 - 0.5)
}

/** The page never scrolls sideways. */
async function noSideways(page: Page) {
  const [scroll, client] = await page.evaluate(() => [document.documentElement.scrollWidth, document.documentElement.clientWidth])
  expect(scroll, `the page is ${scroll} px wide in ${client} px`).toBeLessThanOrEqual(client)
}

/** Each element of `parts` lies within the window's width, whole. */
async function onScreen(page: Page, parts: ReturnType<Page['locator']>) {
  const width = page.viewportSize()!.width
  const boxes = await parts.evaluateAll((els) => els.map((e) => e.getBoundingClientRect()).map((r) => [r.left, r.right]))
  expect(boxes.length, 'the parts are drawn').toBeGreaterThan(0)
  for (const [left, right] of boxes) expect(left! >= -0.5 && right! <= width + 0.5, `a part runs ${left}..${right} in ${width} px`).toBe(true)
}

/** The colour a class of the app's resolves to on the page now, through a probe element (no parsing of oklch). */
function probeColour(page: Page, cls: string): Promise<string> {
  return page.evaluate((c) => {
    const e = document.createElement('span')
    e.className = c
    document.body.append(e)
    const colour = getComputedStyle(e).color
    e.remove()
    return colour
  }, cls)
}

type Box = [left: number, top: number, right: number, bottom: number]

/**
 * The choices drawn whole in `holder` (UX1 PR-20, invariant 6): each button's label is the choice's text, not clipped (its box's
 * scrollWidth not above its clientWidth), each button inside the holder, the recommended mark inside its button, no two buttons
 * overlapping, nothing sideways. Returns the tallest button's height.
 */
async function choicesWhole(page: Page, holder: ReturnType<Page['locator']>, labels: string[], where: string): Promise<number> {
  const buttons = holder.locator('[data-decision-choice]')
  await expect(buttons).toHaveCount(labels.length)
  const outer = await holder.evaluate((e) => {
    const r = e.getBoundingClientRect()
    return [r.left, r.top, r.right, r.bottom] as Box
  })
  const drawn = await buttons.evaluateAll((els) =>
    els.map((b) => {
      const box = (e: Element | null) => {
        if (!e) return null
        const r = e.getBoundingClientRect()
        return [r.left, r.top, r.right, r.bottom] as Box
      }
      const l = b.querySelector<HTMLElement>('[data-slot="label"]')
      return { text: l?.innerText.trim() ?? '', scroll: l?.scrollWidth ?? -1, client: l?.clientWidth ?? -1, box: box(b)!, mark: box(b.querySelector('[data-decision-recommended-mark]')) }
    }),
  )
  const within = (a: Box, b: Box) => a[0] >= b[0] - 0.5 && a[1] >= b[1] - 0.5 && a[2] <= b[2] + 0.5 && a[3] <= b[3] + 0.5
  drawn.forEach((d, i) => {
    expect(d.text, `${where}: choice ${i} says its label whole`).toBe(labels[i])
    expect(d.scroll, `${where}: choice ${i}'s label draws ${d.scroll} px of text in ${d.client} px`).toBeLessThanOrEqual(d.client)
    expect(within(d.box, outer), `${where}: choice ${i} runs ${d.box.join(',')} in its card ${outer.join(',')}`).toBe(true)
    if (d.mark) expect(within(d.mark, d.box), `${where}: choice ${i}'s recommended mark ${d.mark.join(',')} lies in its button ${d.box.join(',')}`).toBe(true)
  })
  expect(drawn[0]!.mark, `${where}: the recommended choice is marked`).not.toBeNull()
  for (let i = 0; i < drawn.length; i++)
    for (let j = i + 1; j < drawn.length; j++) {
      const [a, b] = [drawn[i]!.box, drawn[j]!.box]
      const overlap = Math.min(a[2], b[2]) - Math.max(a[0], b[0]) > 0.5 && Math.min(a[3], b[3]) - Math.max(a[1], b[1]) > 0.5
      expect(overlap, `${where}: choices ${i} ${a.join(',')} and ${j} ${b.join(',')} overlap`).toBe(false)
    }
  await noSideways(page)
  return Math.max(...drawn.map((d) => d.box[3] - d.box[1]))
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
  // UX1 PR-20: its choices once, as the buttons; the gate's own sentence for what a yes lets happen; Details open at once for a
  // plan's approval, the plan revision it approves in view. On the phone: nothing sideways, the buttons whole on screen, the
  // toggle a thumb's size.
  await expect(item.locator('[data-decision-choice]')).toHaveCount(2)
  await expect(card.locator('[data-decision-option]')).toHaveCount(0)
  await expect(card.locator('[data-decision-authorizes-line]')).toHaveText('Approve lets deploy start; Reject cancels it.')
  const details = card.locator('[data-decision-details-toggle]')
  await expect(details).toHaveAttribute('aria-expanded', 'true')
  await expect(card.locator('[data-decision-subject]')).toBeInViewport()
  await expect(card.locator('[data-decision-subject]')).toContainText('plan revision')
  await expect(card.locator('[data-decision-card-id]')).toHaveText(gate1)
  await expect(card.locator('[data-decision-from-plan]')).toContainText('From the plan')
  await tapTarget(details)
  for (const choice of await item.locator('[data-decision-choice]').all()) await tapTarget(choice)
  await onScreen(phone, item.locator('[data-decision-choice]'))
  await noSideways(phone)

  // A response at a stale record: Approve picked at the rev shown, the record moved by a comment, Confirm refused with words.
  budget(6)
  const respond = item.locator(`[data-decision-respond][data-decision-id="${gate1}"]`)
  await respond.locator('[data-decision-choice-label="Approve"]').tap()
  await expect(respond.locator('[data-decision-confirm-step]')).toBeVisible()
  // Confirm and Back, as the choices, a thumb's size (UX1 PR-20, invariant 8).
  await tapTarget(respond.locator('[data-decision-confirm]'))
  await tapTarget(respond.locator('[data-decision-back]'))
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
  // The record's new revs drew the card again: Details still open, and its choices back as rows once nothing is left to answer.
  await expect(details).toHaveAttribute('aria-expanded', 'true')
  await expect(card.locator('[data-decision-subject]')).toContainText('plan revision')
  await expect(card.locator('[data-decision-option]')).toHaveCount(2)
  await expect(item.locator('[data-decision-choice]')).toHaveCount(0)

  // The ticket page's record links to its item on the decision page.
  budget(3)
  await phone.goto(`/dispatch/${PROJECT}/1`)
  const link = phone.locator(`[data-ticket-decision-record="${gate1}"] [data-ticket-decision-item="${gate1}"]`)
  await expect(link).toBeVisible({ timeout: 30_000 })
  // The applied record there, its state words and its mark, in neutral ink, never green (UX1 PR-20, invariant 7).
  const record = phone.locator(`[data-ticket-decision-record="${gate1}"]`)
  await expect(record.locator('[data-ticket-decision-state]')).toContainText('Applied')
  const [ink, success] = [await probeColour(phone, 'text-default'), await probeColour(phone, 'text-success')]
  expect(ink, 'the success token is not the ink').not.toBe(success)
  expect(await record.locator('[data-ticket-decision-state]').evaluate((e) => getComputedStyle(e).color), "the applied record's words are ink").toBe(ink)
  expect(await record.locator(':scope > :first-child').evaluate((e) => getComputedStyle(e).color), "the applied record's mark is ink").toBe(ink)
  await link.tap()
  await expect(phone).toHaveURL(new RegExp(`/decisions\\?item=decision%3A${gate1}$`))
  await expect(phone.locator(`[data-inbox-item="decision:${gate1}"] [data-decision-card] [data-decision-question]`)).toHaveText(question)

  // The agent's decision: Ask for more information, typed to its asking session; the record stays open and its history names it.
  budget(5)
  await phone.goto(`/decisions?item=${encodeURIComponent(`decision:${asked}`)}`)
  const asks = phone.locator(`[data-inbox-item="decision:${asked}"] [data-decision-respond]`)
  // UX1 PR-20: an agent's clarification starts with Details folded; a tap opens them, and they stay open through the rev the
  // request for more makes below.
  const askedCard = phone.locator(`[data-inbox-item="decision:${asked}"] [data-decision-card]`)
  const askedDetails = askedCard.locator('[data-decision-details-toggle]')
  await expect(askedCard.locator('[data-decision-question]')).toBeVisible()
  await expect(askedDetails).toHaveAttribute('aria-expanded', 'false')
  await expect(askedCard.locator('[data-decision-card-id]')).toHaveCount(0)
  await expect(asks.locator('[data-decision-choice]')).toHaveCount(2)
  await expect(askedCard.locator('[data-decision-option]')).toHaveCount(0)
  await expect(asks.locator('[data-decision-choice][data-decision-recommended]')).toHaveAttribute('data-decision-choice-label', 'stub-a')
  await expect(asks.locator('[data-decision-tradeoff]')).toHaveText(['stub-a: the short name'])
  await askedDetails.tap()
  await expect(askedDetails).toHaveAttribute('aria-expanded', 'true')
  await expect(askedCard.locator('[data-decision-card-id]')).toHaveText(asked)
  const askedRev = (await decision(srv.api, asked)).rev
  const note = 'Which tests did you run?'
  await asks.locator('[data-decision-comment-text]').fill(note)
  await expect(asks.locator('[data-decision-comment-text]')).toHaveValue(note)
  await expect(asks.locator('[data-decision-more]')).toBeEnabled()
  await asks.locator('[data-decision-more]').tap()
  await expect(asks.locator('[data-decision-op-words]')).not.toBeEmpty()
  await expect.poll(async () => (await decision(srv.api, asked)).history?.some((h) => h.kind === 'info_requested') ?? false, { timeout: STEP_MS }).toBe(true)
  expect((await decision(srv.api, asked)).state).toBe('open')
  // The card drawn at the new rev (its history toggle counts the request's row) keeps Details open.
  const moreAsked = await decision(srv.api, asked)
  expect(moreAsked.rev).toBeGreaterThan(askedRev)
  await expect(askedCard.locator('[data-decision-history-toggle]')).toHaveText(`History (${moreAsked.history!.length})`, { timeout: STEP_MS })
  await expect(askedDetails).toHaveAttribute('aria-expanded', 'true')
  await expect(askedCard.locator('[data-decision-card-id]')).toHaveText(asked)
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

// UX1 PR-20 (invariant 6): the buttons are the one list of choices, so each says its choice whole. An agent's approval and its
// clarification, each with two choices of 80 characters that differ only in their last word and no tradeoffs line, on a phone
// (390 x 844, touch) and at 1280 x 720, in the item on the decision page and in the modal: every label whole and unclipped, each
// button in its card, the recommended mark in its button, no overlap, nothing sideways; the approval's Confirm says its choice whole.
test('choices as long as the server takes them read whole on their buttons, on a phone and at 1280 x 720, on the page and in the modal', async ({ browser }) => {
  test.slow()
  budget(1, 120_000)
  const run = (await srv.api.launchCrew(longCrew)).id
  runs.push(run)
  const ids = { approval: '', clarification: '' }
  await expect
    .poll(
      async () => {
        const all = await decisionsOf(srv.api, run)
        ids.approval = all.find((d) => d.asker?.task === 'approve' && d.state === 'open')?.id ?? ''
        ids.clarification = all.find((d) => d.asker?.task === 'clarify' && d.state === 'open')?.id ?? ''
        return !!ids.approval && !!ids.clarification
      },
      { timeout: 120_000, intervals: [500], message: 'both attempts ask their decisions' },
    )
    .toBe(true)
  const tallest: string[] = []
  for (const size of ['phone', 'desktop'] as const) {
    const page =
      size === 'phone'
        ? await phonePage(browser, srv.started)
        : await (async () => {
            const ctx = await browser.newContext({ baseURL: srv.started.baseURL, viewport: { width: 1280, height: 720 } })
            await ctx.addInitScript(({ t }) => localStorage.setItem('conductor.workbenchToken', t), { t: srv.started.token })
            return ctx.newPage()
          })()
    for (const type of ['approval', 'clarification'] as const) {
      const id = ids[type]
      budget(6)
      await page.goto(`/decisions?item=${encodeURIComponent(`decision:${id}`)}`)
      const item = page.locator(`[data-inbox-item="decision:${id}"]`)
      await expect(item.locator('[data-decision-card] [data-decision-question]')).toBeVisible()
      await expect(item.locator('[data-decision-tradeoffs]')).toHaveCount(0)
      tallest.push(`${size}, the page, the ${type}: ${await choicesWhole(page, item, LONG[type], `${size}, the page, the ${type}`)} px`)
      if (size === 'phone') for (const choice of await item.locator('[data-decision-choice]').all()) await tapTarget(choice)
      // The approval's Confirm names the choice picked, whole.
      if (type === 'approval') {
        await item.locator('[data-decision-choice="1"]').click()
        const label = item.locator('[data-decision-confirm] [data-slot="label"]')
        await expect(label).toHaveText(`Answer ${LONG.approval[1]}`)
        const [scroll, client] = await label.evaluate((e) => [e.scrollWidth, e.clientWidth])
        expect(scroll, `${size}: Confirm's label draws ${scroll} px of text in ${client} px`).toBeLessThanOrEqual(client!)
        await noSideways(page)
        await item.locator('[data-decision-back]').click()
        await expect(item.locator('[data-decision-confirm-step]')).toHaveCount(0)
      }
      await page.goto(`/runs/${encodeURIComponent(run)}?view=tasks&decision=${id}`)
      const modal = page.locator(`[data-decision-modal][data-decision-modal-id="${id}"]`)
      await expect(modal.locator('[data-decision-question]')).toBeVisible()
      tallest.push(`${size}, the modal, the ${type}: ${await choicesWhole(page, modal, LONG[type], `${size}, the modal, the ${type}`)} px`)
    }
    await page.context().close()
  }
  test.info().annotations.push({ type: 'tallest choice', description: tallest.join('; ') })
  console.log(`tallest choice: ${tallest.join('; ')}`)
})
