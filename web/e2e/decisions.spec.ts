import { execFileSync } from 'node:child_process'
import type { Page } from '@playwright/test'
import { expect, stopStrays, task, test, type Session } from './fixtures'
import { budget, decisionServer, decisionUntil, decisionsOf, firstDraw, makeTicket, operatorPage, propose, runUntil, type DecisionServer } from './decisions-helpers'
import { storedTicket } from './dispatch-helpers'

// An agent's durable decision answered where its question is (Program item DR1, docs/plans/decisions.md, PR-7), on a server of its own:
// a crew of two tasks launched from a ticket's approved plan revision, `build` by the stub worker that asks the operator an approval
// before it commits (STUB_DECISION, with the ticket and its run as evidence) and `docs` by one that asks nothing. `docs` finishes while
// `build` waits; the run page says what build waits for; the session's question bar shows the question with Answer and no reply field
// or number key, on the session page and in the Yard's focused panel, and Answer opens the card; a share link's viewer sees where it is
// answered and no Answer; the ticket, edited after the question and before the card's first read, shows "changed since" in the card,
// the run's chip nothing; the palette's row opens the card; the operator approves there: Recorded, then Applied, "typed into build's
// attempt 1"; the stub writes the line it was typed into stub-answer.txt and build succeeds; a second tab's Approve, picked before
// and confirmed after, is told who answered first. Every wait is on the record through the API or on what the page draws.
test.describe.configure({ mode: 'serial' })

const PROJECT = 'DRA'
const STEP_MS = 15_000
test.use({ actionTimeout: STEP_MS, navigationTimeout: STEP_MS })

let srv: DecisionServer
let ticket = ''
let crewId = ''
let runId = ''

test.beforeAll(async () => {
  srv = await decisionServer('decisions', [{ id: 'worker-decides', from: 'worker', env: { STUB_DECISION: 'approval', STUB_DECISION_EVIDENCE: `ticket:${PROJECT}-1 run:{run}` } }])
  await stopStrays(srv.api, [])
  ticket = await makeTicket(srv.api, PROJECT, 'Ship the stub change')
  crewId = (
    await srv.api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
      name: 'e2e decisions',
      goal: 'ask the operator before committing',
      cwd: '',
      where: 'server',
      isolation: 'worktree',
      openAfterLaunch: false,
      yolo: null,
      members: [
        { name: 'builder', agentId: 'worker-decides', prompt: '', start: { when: 'task' } },
        { name: 'writer', agentId: 'worker', prompt: '', start: { when: 'task' } },
      ],
      tasks: [
        { id: 'build', member: 'builder', kind: 'work', instructions: 'Build it, once the operator approves.' },
        { id: 'docs', member: 'writer', kind: 'work', instructions: 'Document it.' },
      ],
    })
  ).crew.id
})

test.afterAll(async () => {
  if (!srv) return
  if (runId) await srv.api.stopRun(runId)
  await srv.stop()
})

/** Opens the palette with Alt+/ once the layout that listens for it is drawn. */
async function openPalette(page: Page) {
  await expect(page.locator('#dashboard-sidebar-main')).toBeAttached()
  await page.keyboard.press('Alt+/')
  const palette = page.locator('[data-palette]')
  await expect(palette).toBeVisible()
  return palette
}

/** Closes the decision's modal by its own Close button (a page's Escape may be its own: the Yard's leaves the focused panel). */
async function closeCard(page: Page) {
  const dialog = page.getByRole('dialog', { name: 'A decision for you' })
  await dialog.getByRole('button', { name: 'Close' }).click()
  await expect(page.locator('[data-decision-modal]')).toHaveCount(0)
}

// The measures below are this spec's own until the stream's shared helpers land (UX1 PR-1, web/e2e/layout.ts: `tabTo`, `probeColour`),
// where they move.

/** What a class of the app's resolves to on the page now (a colour, a fill, a ring's shadow), through a probe element. */
function probe(page: Page, cls: string, prop: 'color' | 'backgroundColor' | 'boxShadow' = 'color'): Promise<string> {
  return page.evaluate(
    ([c, p]) => {
      const e = document.createElement('span')
      e.className = c
      document.body.append(e)
      const v = getComputedStyle(e)[p]
      e.remove()
      return v
    },
    [cls, prop] as const,
  )
}

/** The colour a class of the app's resolves to on the page now, through a probe element (no parsing of oklch). */
const probeColour = (page: Page, cls: string) => probe(page, cls)

/**
 * Waits until nothing moves what `holder` draws: no running animation or transition on it, on an element it lies in (the modal's
 * dialog, which scales in for 200 ms as it opens) or on one inside it (a colour's transition after the theme changes), read from the
 * page (`document.getAnimations()`) and each awaited by its own `finished`, again until none is left. An endless one is never waited
 * for. Every colour or ring the spec reads in the modal is read after it, so it is the last frame's (review round 3).
 */
function settled(holder: ReturnType<Page['locator']>): Promise<string[]> {
  return holder.evaluate(async (el) => {
    const waited: string[] = []
    for (;;) {
      const moving = document.getAnimations().filter((a) => {
        const t = (a.effect as KeyframeEffect | null)?.target
        return !!t && (t.contains(el) || el.contains(t)) && a.playState === 'running' && Number.isFinite(Number(a.effect?.getComputedTiming().endTime))
      })
      if (!moving.length) return waited
      for (const a of moving) waited.push((a as CSSAnimation).animationName ?? (a as CSSTransition).transitionProperty)
      await Promise.all(moving.map((a) => a.finished.catch(() => undefined)))
    }
  })
}

/** Presses Tab from `from` until `target` has the focus, at most `max` times; it then draws a focus ring (an outline or a shadow). */
async function tabTo(page: Page, from: ReturnType<Page['locator']>, target: ReturnType<Page['locator']>, max: number) {
  await from.focus()
  let n = 0
  while (!(await target.evaluate((e) => e === document.activeElement))) {
    expect(++n, `Tab reaches the control within ${max} presses`).toBeLessThanOrEqual(max)
    await page.keyboard.press('Tab')
  }
  await settled(target)
  const ring = await target.evaluate((e) => {
    const s = getComputedStyle(e)
    return (s.outlineStyle !== 'none' && parseFloat(s.outlineWidth) > 0) || s.boxShadow !== 'none'
  })
  expect(ring, 'the focused control draws a ring').toBe(true)
}

/** The colour of a step's mark, and of its icon's class, as drawn. */
function stepColour(modal: ReturnType<Page['locator']>, key: string): Promise<string> {
  return modal.locator(`[data-decision-step="${key}"] [data-step-mark]`).evaluate((e) => getComputedStyle(e).color)
}

/** The question bar of a page, with no reply field and no number key, its question the decision's. */
async function barHoldsNoReply(page: Page, question: string) {
  const bar = page.locator('[data-quick-reply]')
  await expect(bar.locator('[data-quick-reply-message]')).toHaveText(question, { timeout: 30_000 })
  await expect(bar.locator('input')).toHaveCount(0)
  await expect(bar.locator('kbd')).toHaveCount(0)
  return bar
}

test("build's approval waits while docs finishes, is answered in its card from the run page, the bar, the palette; Recorded, then Applied", async ({ browser }) => {
  test.slow()
  const api = srv.api
  const base = srv.started.baseURL
  const plan = await propose(api, ticket, crewId)
  await api.ok('POST', `/api/dispatch/plans/${plan}/approve`, { opId: 'dra-ap1', note: 'Go.' })
  runId = (await api.ok<{ launch: { runId: string } }>('POST', `/api/dispatch/tickets/${ticket}/launch`, { opId: 'dra-l1', plan })).launch.runId

  // The decision, as the record says it: opened by build's attempt 1, open.
  budget(120_000)
  let id = ''
  await expect
    .poll(async () => (id = (await decisionsOf(api, runId)).find((d) => d.asker?.task === 'build' && d.state === 'open')?.id ?? ''), { timeout: 120_000, intervals: [500], message: "build's attempt asks its decision" })
    .toMatch(/^dr-[1-9][0-9]*$/)
  expect(id).toBe('dr-1')
  const question = 'May build commit its change?'

  // The ticket edited after the question was asked and before any card read it: its chip says what changed since.
  const before = await storedTicket(api, ticket)
  await api.ok('POST', `/api/dispatch/tickets/${ticket}/edit`, { opId: 'dra-e1', expectedRev: before.rev, set: { description: 'Edited after the question.' } })

  // docs finishes meanwhile; build is held with its words.
  const run = await runUntil(api, runId, (r) => task(r, 'docs').state === 'succeeded', 'docs succeeds while build waits')
  expect(task(run, 'build').state).toBe('running')
  // The session asking it, once its question shows (the stub ended its turn).
  let asking: Session | undefined
  budget(60_000)
  await expect
    .poll(async () => {
      asking = (await api.ok<{ sessions: Session[] }>('GET', '/api/sessions')).sessions.find((s) => s.crew?.runId === runId && s.attention?.state === 'needs_input' && s.attention?.decision === id)
      return !!asking
    }, { timeout: 60_000, intervals: [500], message: "build's session shows the decision's question" })
    .toBe(true)
  const sid = asking!.id

  const page = await operatorPage(browser, srv.started)
  budget(10 * STEP_MS)
  await page.goto(`${base}/runs/${runId}?view=tasks`)
  const row = page.locator('[data-task-row="build"]')
  await firstDraw(row)
  // The row says the attempt once: attempt 1 is the norm, so the words alone (UX1 PR-11).
  await expect(row.locator('[data-task-words]')).toHaveText(`waits for decision ${id}`, { timeout: 30_000 })
  // Held by an open decision, it waits on a person: the status language's needs (UX1 PR-10), never the running green.
  await expect(row.locator('[data-status-mark]')).toHaveAttribute('data-status-mark', 'needs')
  await expect(page.locator('[data-task-row="docs"]')).toHaveAttribute('data-task-state', 'succeeded')
  await expect(page.locator('[data-task-row="docs"] [data-status-mark]')).toHaveAttribute('data-status-mark', 'done')
  // The header (UX1 PR-10): the bar's needs segment holds build, and the sentence's one item is the decision its question mirrors.
  await expect(page.locator('[data-crew-run-header] [data-run-bar] [data-part="needs"]')).toHaveAttribute('data-count', '1')
  await expect(page.locator('[data-crew-run-header] [data-run-progress]')).toHaveAttribute('data-needs', '1')
  await expect(page.locator('[data-crew-run-header] [data-run-progress]')).toContainText('1 needs you')

  // The run page's Answer, reached by the keyboard, opens the card on Enter and on Space; Enter on the row itself selects the row
  // (UX1 PR-20 fixed main here: the row's own Enter took the key as it came up from the button, and selected the row instead).
  const modal = page.locator(`[data-decision-modal][data-decision-modal-id="${id}"]`)
  const answerButton = row.locator('[data-task-answer="build"]')
  const rowControl = row.locator(':scope > [role="button"]')
  for (const key of ['Enter', 'Space']) {
    await tabTo(page, rowControl, answerButton, 4)
    await page.keyboard.press(key)
    await expect(modal.locator('[data-decision-question]'), `${key} on Answer opens the card`).toHaveText(question)
    await closeCard(page)
  }
  await expect(page.locator('[data-task-detail-id="build"]')).toHaveCount(0)
  await rowControl.focus()
  await page.keyboard.press('Enter')
  await expect(page.locator('[data-task-detail-id="build"]'), 'Enter on the row selects it').toBeVisible()
  // The run page's Answer opens the card: the ticket's chip changed since the question (its revision then named), the run's not.
  await answerButton.click()
  await expect(modal.locator('[data-decision-question]')).toHaveText(question)
  // UX1 PR-20: the choices once, as the buttons, the recommended one marked on its button and each tradeoff in a line under them;
  // what a yes lets happen in one line; the modal titled for the person, the id folded under Details.
  await expect(page.getByRole('dialog', { name: 'A decision for you' })).toBeVisible()
  await expect(modal.locator('[data-decision-choice]')).toHaveCount(2)
  await expect(modal.locator('[data-decision-option]')).toHaveCount(0)
  await expect(modal.locator('[data-decision-recommended]')).toHaveCount(1)
  await expect(modal.locator('[data-decision-choice][data-decision-recommended]')).toHaveCount(1)
  await expect(modal.locator('[data-decision-choice][data-decision-recommended]')).toHaveAttribute('data-decision-choice-label', 'Approve')
  await expect(modal.locator('[data-decision-choice-label="Approve"]')).toContainText('recommended')
  await expect(modal.locator('[data-decision-tradeoff="0"]')).toHaveText('Approve: the change goes in as it is')
  await expect(modal.locator('[data-decision-tradeoff="1"]')).toHaveText('Reject: nothing is committed')
  await expect(modal.locator('[data-decision-authorizes]')).toHaveText('build commits stub-builder.txt and submits it')
  await expect(modal.locator('[data-decision-authorizes-line]')).toHaveText('A yes authorizes: build commits stub-builder.txt and submits it')
  // Details, folded for an agent's request, reached by Tab from the run's link and opened by Enter: the id, its type, the subject.
  const details = modal.locator('[data-decision-details-toggle]')
  await expect(details).toHaveAttribute('aria-expanded', 'false')
  await expect(modal.locator('[data-decision-card-id]')).toHaveCount(0)
  await tabTo(page, modal.locator('[data-decision-run]'), details, 8)
  await page.keyboard.press('Enter')
  await expect(details).toHaveAttribute('aria-expanded', 'true')
  await expect(modal.locator('[data-decision-card-id]')).toHaveText(id)
  await expect(modal.locator('[data-decision-type]')).toHaveText('approval')
  await expect(modal.locator('[data-decision-from-plan]')).toHaveCount(0)
  // The steps in a person's words; the answer owed is a still amber dot, the step reached a neutral check, nothing a loader. In
  // both themes, the stored colour mode switched and the card drawn again.
  await expect(modal.locator('[data-decision-step="recorded"]')).toHaveAttribute('data-step-icon', 'i-lucide-circle-dot')
  await expect(modal.locator('[data-decision-step="recorded"] [data-step-label]')).toHaveText('Your answer')
  await expect(modal.locator('[data-decision-step] [data-step-label]')).toHaveText(['Asked', 'Your answer', 'Checked by Yardmaster', 'Applied'])
  await expect(modal.locator('[data-decision-step][data-step-icon="i-lucide-loader-circle"]')).toHaveCount(0)
  for (const theme of ['dark', 'light'] as const) {
    if (theme === 'light') {
      budget(4 * STEP_MS)
      await page.evaluate(() => localStorage.setItem('conductor-color-mode', 'light'))
      await page.goto(`${base}/runs/${runId}?view=tasks&decision=${id}`)
      await firstDraw(modal.locator('[data-decision-question]'))
      await expect(page.locator('html')).toHaveClass(/\blight\b/)
      await expect(modal.locator('[data-decision-step="recorded"]')).toHaveAttribute('data-step-icon', 'i-lucide-circle-dot')
    } else {
      await expect(page.locator('html')).toHaveClass(/\bdark\b/)
    }
    await settled(modal)
    const [warning, ink, success] = [await probeColour(page, 'text-warning'), await probeColour(page, 'text-default'), await probeColour(page, 'text-success')]
    expect(ink, `${theme}: the success token is not the ink`).not.toBe(success)
    expect(await stepColour(modal, 'recorded'), `${theme}: the answer owed is amber`).toBe(warning)
    expect(await stepColour(modal, 'open'), `${theme}: the step reached is neutral ink`).toBe(ink)
  }
  const ticketChip = modal.locator(`[data-ref-chip="ticket:${ticket}"]`)
  await expect(ticketChip.locator('[data-ref-changed]')).toHaveText(`changed since r${before.rev}`)
  await expect(modal.locator(`[data-ref-chip="run:${runId}"]`)).toBeVisible()
  await expect(modal.locator(`[data-ref-chip="run:${runId}"] [data-ref-changed]`)).toHaveCount(0)
  await expect(modal.locator('[data-decision-step="open"]')).toHaveAttribute('data-step-status', 'done')
  await expect(modal.locator('[data-decision-step="recorded"]')).toHaveAttribute('data-step-status', 'waiting')
  await closeCard(page)

  // The session page's bar: the question, Answer, no reply field or number key; Answer opens the card.
  budget(6 * STEP_MS)
  await page.goto(`${base}/sessions/${sid}`)
  await firstDraw(page.locator('[data-quick-reply]'))
  let bar = await barHoldsNoReply(page, question)
  await bar.locator('[data-quick-reply-answer]').click()
  await expect(modal.locator('[data-decision-question]')).toHaveText(question)
  await closeCard(page)

  // The Yard's focused panel: the same.
  budget(5 * STEP_MS)
  await page.goto(`${base}/yard?focus=${sid}`)
  await firstDraw(page.locator('[data-quick-reply]'))
  bar = await barHoldsNoReply(page, question)
  await bar.locator('[data-quick-reply-answer]').click()
  await expect(modal.locator('[data-decision-question]')).toHaveText(question)
  await closeCard(page)

  // A share link's viewer, with control: the question and where it is answered, no Answer, reply field or number key.
  budget(5 * STEP_MS)
  const { token } = await api.ok<{ token: string }>('POST', `/api/sessions/${sid}/links`, { role: 'control' })
  const guestCtx = await browser.newContext({ viewport: { width: 1280, height: 800 } })
  await guestCtx.addInitScript(() => localStorage.setItem('conductor.displayName', 'Ana'))
  const guest = await guestCtx.newPage()
  await guest.goto(`${base}/join/${token}`)
  await firstDraw(guest.getByRole('button', { name: 'Join session' }))
  await guest.getByRole('button', { name: 'Join session' }).click()
  const guestBar = await barHoldsNoReply(guest, question)
  await expect(guestBar.locator('[data-quick-reply-decision]')).toHaveText(`The operator answers ${id} in Conductor`)
  await expect(guestBar.locator('[data-quick-reply-answer]')).toHaveCount(0)
  await expect(guestBar.locator('button')).toHaveCount(0)
  await guestCtx.close()

  // A second tab picks Approve now and confirms it only after the first answered.
  budget(5 * STEP_MS)
  const second = await operatorPage(browser, srv.started, 'Priya')
  await second.goto(`${base}/runs/${runId}?view=tasks&decision=${id}`)
  const secondModal = second.locator(`[data-decision-modal][data-decision-modal-id="${id}"]`)
  await firstDraw(secondModal.locator('[data-decision-choice-label="Approve"]'))
  await secondModal.locator('[data-decision-choice-label="Approve"]').click()
  await expect(secondModal.locator('[data-decision-confirm]')).toBeVisible()
  await expect(secondModal.locator('[data-decision-option]')).toHaveCount(0)

  // The palette's row opens the card, where the operator approves: Recorded at once, then Applied, typed into build's attempt 1.
  budget(6 * STEP_MS)
  await page.goto(`${base}/`)
  await firstDraw(page.locator('#dashboard-sidebar-main'))
  const palette = await openPalette(page)
  const prow = palette.locator(`[data-palette-row="decision:${id}"]`)
  await expect(prow).toContainText(`Answer ${id}: ${question}`)
  // One row for the decision (the reviews of #719): its "Answer" row in Needs you stands for the session's question that mirrors it,
  // so the session is listed among the running, not again in Needs you.
  await expect(prow).toHaveCount(1)
  await expect(palette.locator(`[data-palette-group="needs"] [data-palette-row="session:${sid}"]`)).toHaveCount(0)
  await expect(palette.locator(`[data-palette-group="working"] [data-palette-row="session:${sid}"]`)).toHaveCount(1)
  await prow.click()
  await expect(modal.locator('[data-decision-question]')).toHaveText(question)
  await modal.locator('[data-decision-choice-label="Approve"]').click()
  await modal.locator('[data-decision-rationale]').fill('Small enough.')
  await modal.locator('[data-decision-confirm]').click()
  await expect(modal.locator('[data-decision-receipt]')).toContainText(`Recorded: ${id} answered by Nate`)
  const applied = await decisionUntil(api, id, 'applied')
  expect(applied.applied?.how).toBe('typed')
  await expect(modal.locator('[data-decision-step="applied"]')).toHaveAttribute('data-step-status', 'done', { timeout: 30_000 })
  await expect(modal.locator('[data-decision-step="applied"] [data-step-words]')).toContainText("typed into build's attempt 1")
  await expect(modal.locator('[data-decision-step="recorded"] [data-step-words]')).toContainText(`Recorded: ${id} answered by Nate`)
  await expect(modal.locator('[data-decision-state-words]')).toHaveText("Applied: typed into build's attempt 1")
  // The card says the answer itself: the option picked, why, and who (Codex's round 1 on #719).
  await expect(modal.locator('[data-decision-answer-choice]')).toHaveText('Approve')
  await expect(modal.locator('[data-decision-answer-rationale]')).toHaveText('Small enough.')
  await expect(modal.locator('[data-decision-answer-by]')).toHaveText('Nate')
  // Applied, the buttons gone: the card lists its choices again, the one place they are left (UX1 PR-20).
  await expect(modal.locator('[data-decision-choice]')).toHaveCount(0)
  await expect(modal.locator('[data-decision-option]')).toHaveCount(2)
  // Applied is done, not green (invariant 7): its state words, the answer's box and the receipt in neutral ink and fills, in both
  // themes. The page is light now (the stored mode above); dark is set on the page in place, so the receipt stays drawn.
  await expect(modal.locator('[data-decision-receipt] [data-decision-receipt-mark]')).toHaveCount(1)
  for (const theme of ['light', 'dark'] as const) {
    if (theme === 'dark')
      await page.evaluate(() => {
        localStorage.setItem('conductor-color-mode', 'dark')
        document.documentElement.classList.remove('light')
        document.documentElement.classList.add('dark')
      })
    await expect(page.locator('html')).toHaveClass(theme === 'dark' ? /\bdark\b/ : /\blight\b/)
    await settled(modal)
    const ink = await probeColour(page, 'text-default')
    const [fill, ring] = [await probe(page, 'bg-elevated/40', 'backgroundColor'), await probe(page, 'ring ring-default', 'boxShadow')]
    expect(fill, `${theme}: the success fill is not the neutral one`).not.toBe(await probe(page, 'bg-success/5', 'backgroundColor'))
    expect(await probe(page, 'text-success'), `${theme}: the success token is not the ink`).not.toBe(ink)
    expect(await modal.locator('[data-decision-state-words]').evaluate((e) => getComputedStyle(e).color), `${theme}: Applied is ink`).toBe(ink)
    const box = await modal.locator('[data-decision-answer]').evaluate((e) => [getComputedStyle(e).backgroundColor, getComputedStyle(e).boxShadow])
    expect(box[0], `${theme}: the answer's box has the neutral fill`).toBe(fill)
    expect(box[1], `${theme}: the answer's box has the neutral ring`).toBe(ring)
    expect(await modal.locator('[data-decision-receipt]').evaluate((e) => getComputedStyle(e).color), `${theme}: the receipt is ink`).toBe(ink)
  }

  // The stub took the typed line as its answer and went on: build succeeds with stub-answer.txt in its result.
  const done = await runUntil(api, runId, (r) => task(r, 'build').state === 'succeeded', 'build succeeds once its answer is typed')
  const commit = task(done, 'build').result?.commit ?? ''
  expect(commit).toMatch(/^[0-9a-f]{40,64}$/)
  const answer = execFileSync('git', ['show', `${commit}:stub-answer.txt`], { cwd: srv.repo, encoding: 'utf8' })
  expect(answer).toContain(`Decision ${id} (approval) answered by Nate: Approve. Why: Small enough. (receipt ${id}/1)`)

  // The card opened again from the run page, the record applied: its choices as rows, no buttons; its steps done in neutral checks,
  // their receipts in Details.
  budget(4 * STEP_MS)
  await page.goto(`${base}/runs/${runId}?view=tasks&decision=${id}`)
  await firstDraw(modal.locator('[data-decision-state-words]'))
  await expect(modal.locator('[data-decision-state-words]')).toHaveText("Applied: typed into build's attempt 1")
  await expect(modal.locator('[data-decision-option]')).toHaveCount(2)
  await expect(modal.locator('[data-decision-option="0"] [data-decision-recommended]')).toHaveCount(1)
  await expect(modal.locator('[data-decision-choice]')).toHaveCount(0)
  await expect(modal.locator('[data-decision-step][data-step-icon="i-lucide-circle-check"]')).toHaveCount(4)
  // A card that cannot answer names the step "The answer", never "Your answer".
  await expect(modal.locator('[data-decision-step="recorded"] [data-step-label]')).toHaveText('The answer')
  await modal.locator('[data-decision-details-toggle]').click()
  await expect(modal.locator('[data-step-receipt-of="recorded"]')).toHaveText(`${id}/1`)
  await expect(modal.locator('[data-step-receipt-of="applied"]')).toHaveText(`${id}/1 applied`)
  await closeCard(page)

  // The second tab, its Approve still waiting for Confirm on a record applied since: the buttons stay, the rows do not come back.
  budget(4 * STEP_MS)
  await expect(secondModal.locator('[data-decision-moved]')).toBeVisible({ timeout: 30_000 })
  await expect(secondModal.locator('[data-decision-choice]')).toHaveCount(2)
  await expect(secondModal.locator('[data-decision-option]')).toHaveCount(0)
  // It confirms its Approve now: told who answered first, nothing applied twice.
  await secondModal.locator('[data-decision-confirm]').click()
  await expect(secondModal.locator('[data-decision-refusal-words]')).toContainText(`${id} was answered by Nate`)
  const after = await api.ok<{ decision: { history: Array<{ kind: string }> } }>('GET', `/api/decisions/${id}`)
  expect(after.decision.history.filter((h) => h.kind === 'applied')).toHaveLength(1)
  await second.context().close()
  await page.context().close()
})
