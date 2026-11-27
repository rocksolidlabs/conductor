import { randomBytes } from 'node:crypto'
import type { Page, Request } from '@playwright/test'
import { budget, expect, test, useTheme, type Api } from './fixtures'
import { freeProjectKey, makeProject, makeTicket, opOf, storedTicket } from './dispatch-helpers'
import { createComparison, makeComparison } from './compare-helpers'
import { noOverlap, noSideways } from './layout'

// A comparison's launch from a ticket's approved plan revision (CP1, docs/plans/compare-crew.md, "The launch says what it costs"): the
// dialog says what it starts from the revision's own `compare` part, above the members' lines, and its button says the count; the
// count goes with the launch, the same on every resend of one operation; a comparison whose part is not given is not launched from the
// dialog; an ordinary revision sends none (the launch route watched at this spec's own route, which answers the server's refusal where
// the spec only needs to see what was sent). Crews names New agent trial; a trial's crew, saved through the server's template route,
// shows its block in the editor, and makeComparison launches it for real from its ticket, the server taking the count; the crews
// page's own Launch of it is refused in the server's words. The form itself is trial-form.spec.ts's.

/** What one test made, ended after it. */
const made = { crews: [] as string[], runs: [] as string[] }
test.afterEach(async ({ api }) => {
  for (const id of made.runs.splice(0)) await api.stopRun(id)
  for (const id of made.crews.splice(0)) await api.call('DELETE', `/api/crews/${encodeURIComponent(id)}`)
})

const tag = () => randomBytes(3).toString('hex')

/** A comparison's crew as New comparison saves it: two candidates (the stub workers) with a check, a review after each, Brakeman's draft. */
async function comparisonCrew(api: Api, o: { reviews?: boolean } = {}) {
  const lanes = ['a', 'b']
  const agents = ['worker', 'worker-cli']
  const { crew } = await api.ok<{ crew: { id: string; name: string } }>('POST', '/api/crews', {
    name: `e2e comparison ${tag()}`,
    goal: 'Make the cache read fast',
    cwd: '',
    where: 'server',
    isolation: 'worktree',
    openAfterLaunch: false,
    members: [
      ...lanes.map((l, i) => ({ name: `cand-${l}`, agentId: agents[i], prompt: '', start: { when: 'task' } })),
      ...(o.reviews ? lanes.map((l) => ({ name: `rev-${l}`, agentId: 'worker', prompt: '', start: { when: 'task' } })) : []),
    ],
    tasks: [
      ...lanes.map((l) => ({ id: `approach-${l}`, member: `cand-${l}`, kind: 'work', instructions: 'Keep the API.', checks: [{ id: 'unit', argv: ['true'] }] })),
      ...(o.reviews ? lanes.map((l) => ({ id: `review-${l}`, member: `rev-${l}`, kind: 'review', instructions: 'Review it.', needs: [`approach-${l}`] })) : []),
    ],
    compare: { criteria: [{ id: 'no-new-dependency', text: 'no new dependency' }], candidates: [{ task: 'approach-a', approach: 'key the cache by locale' }, { task: 'approach-b' }], brakeman: true },
  })
  made.crews.push(crew.id)
  return crew
}

/** An ordinary managed crew of one stub worker. */
async function ordinaryCrew(api: Api) {
  const { crew } = await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
    name: `e2e ordinary ${tag()}`,
    goal: '',
    cwd: '',
    where: 'server',
    isolation: 'worktree',
    openAfterLaunch: false,
    members: [{ name: 'builder', agentId: 'worker', prompt: '', start: { when: 'task' } }],
    tasks: [{ id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.' }],
  })
  made.crews.push(crew.id)
  return crew
}

/** A ticket of a project of its own, with `crew` proposed as its revision 1. */
async function proposedTicket(api: Api, crew: string) {
  const key = await freeProjectKey(api, 'CMP')
  await makeProject(api, key)
  const ticketKey = await makeTicket(api, key, '1', { title: 'Compare the cache' })
  const { plan } = await api.ok<{ plan: { id: string; rev: number } }>('POST', `/api/dispatch/tickets/${ticketKey}/plans`, { opId: opOf(key, 'r1'), crew })
  return { key, ticketKey, plan }
}

/** A ticket of a project of its own, with `crew` proposed and approved as its revision 1. */
async function approvedTicket(api: Api, crew: string) {
  const { key, ticketKey, plan } = await proposedTicket(api, crew)
  const ticket = await storedTicket(api, ticketKey)
  await api.ok('POST', `/api/dispatch/plans/${plan.id}/approve`, { opId: opOf(key, 'ap1'), expectedRev: plan.rev, ticketRev: ticket.rev })
  return { key, ticketKey, plan }
}

/**
 * The plan reads as `edit` makes them: the ticket's list and a revision's read, each answered by the server and handed to `edit` (the
 * server's `compare` part is what the page draws; a test that needs it missing takes it away here).
 */
async function editPlanReads(page: Page, edit: (p: Record<string, unknown>) => void) {
  await page.route(
    (u) => /^\/api\/dispatch\/(tickets\/[^/]+\/plans|plans\/[^/]+)$/.test(u.pathname),
    async (route) => {
      if (route.request().method() !== 'GET') return route.fallback()
      const res = await route.fetch()
      const body = (await res.json()) as { plan?: Record<string, unknown>; plans?: Array<Record<string, unknown>> }
      if (body.plan) edit(body.plan)
      for (const p of body.plans ?? []) edit(p)
      return route.fulfill({ response: res, json: body })
    },
  )
}

/** Every launch the page sends, its body; each answered by `answer` (a request let through when it answers nothing). */
async function watchLaunches(page: Page, ticketKey: string, answer: (n: number, r: Request) => 'abort' | { status: number; json: unknown } | null) {
  const sent: Array<Record<string, unknown>> = []
  await page.route(
    (u) => u.pathname === `/api/dispatch/tickets/${ticketKey}/launch`,
    async (route) => {
      if (route.request().method() !== 'POST') return route.fallback()
      sent.push(route.request().postDataJSON() as Record<string, unknown>)
      const a = answer(sent.length, route.request())
      if (a === 'abort') return route.abort('connectionreset')
      if (a) return route.fulfill({ status: a.status, contentType: 'application/json', body: JSON.stringify(a.json) })
      return route.fallback()
    },
  )
  return sent
}

async function openLaunch(page: Page, key: string) {
  await page.goto(`/dispatch/${key}/1`)
  const featured = page.locator('[data-plan-section] [data-plan-featured]')
  await expect(featured).toHaveAttribute('data-plan-state', 'approved', { timeout: 30_000 })
  await featured.locator('[data-plan-launch]').click()
  return page.locator('[data-launch-dialog]')
}

/** The refusal the server answers a comparison launched with another count (internal/api, confirm_candidates). */
const confirmRefusal = { status: 409, json: { error: { code: 'confirm_candidates', message: 'this revision starts 2 candidates: send candidates 2 to launch them' } } }

test("a comparison's launch says what it starts, its button says the count, and every send of the launch carries that count", async ({ page, api }) => {
  test.setTimeout(120_000)
  const crew = await comparisonCrew(api, { reviews: true })
  const { key, ticketKey } = await approvedTicket(api, crew.id)
  // Two network losses (the page asks once more by itself, then says so), then the server's refusal of the count.
  const sent = await watchLaunches(page, ticketKey, (n) => (n <= 2 ? 'abort' : confirmRefusal))
  const dialog = await openLaunch(page, key)

  const statement = dialog.locator('[data-launch-compare]')
  await expect(statement).toHaveAttribute('data-launch-candidates', '2', { timeout: 30_000 })
  await expect(statement.locator('[data-launch-compare-part="candidates"]')).toHaveText(
    "This trial starts 2 candidates (Worker (e2e stub), Worker, CLI (e2e stub)): 2 agent sessions, once the plan's decisions and the project's prerequisites let the work start",
  )
  await expect(statement.locator('[data-launch-compare-part="reviews"]')).toHaveText('then an agent review of each candidate that succeeds: up to 2 more sessions, none for a candidate that fails or is cancelled')
  await expect(statement.locator('[data-launch-compare-part="multiplier"]')).toContainText("a review's request for changes, an attempt that could not start and each Retry you give add more")
  await expect(statement.locator('[data-launch-compare-part="checks"]')).toHaveText(
    "1 check on each submitted candidate attempt (the task's checks; the plan holds no manifest, so no verify steps)",
  )
  await expect(statement.locator('[data-launch-compare-part="brakeman"]')).toHaveText('1 Brakeman turn on your Brakeman account, when all candidates are done')
  await expect(statement.locator('[data-launch-compare-part="usage"]')).toHaveText('Usage per account: shown below')
  // Above the members' lines, and no total of sessions or checks anywhere in it.
  const box = async (sel: string) => (await dialog.locator(sel).first().boundingBox())!
  expect((await box('[data-launch-compare]')).y).toBeLessThan((await box('[data-launch-members]')).y)
  expect(await statement.textContent()).not.toMatch(/\b(total|in all|altogether)\b/i)

  const confirm = dialog.locator('[data-launch-confirm]')
  await expect(confirm).toHaveText('Launch 2 candidates')
  await confirm.click()
  await expect(dialog.locator('[data-launch-error]')).toContainText('send it again; it is the same launch', { timeout: 30_000 })
  await expect(confirm).toHaveText('Send again')
  await confirm.click()
  await expect(dialog.locator('[data-launch-refused]')).toContainText('this revision starts 2 candidates: send candidates 2 to launch them', { timeout: 30_000 })
  // One operation, sent three times, each with the count the statement said.
  expect(sent).toHaveLength(3)
  expect(new Set(sent.map((b) => b.opId)).size).toBe(1)
  expect(sent.map((b) => b.candidates)).toEqual([2, 2, 2])
})

test("a comparison whose statement the server did not give is not launched from the dialog, and an ordinary revision sends no count", async ({ page, api }) => {
  test.setTimeout(120_000)
  const crew = await comparisonCrew(api)
  const { key, ticketKey } = await approvedTicket(api, crew.id)
  await editPlanReads(page, (p) => delete p.compare)
  const sent = await watchLaunches(page, ticketKey, () => confirmRefusal)
  const dialog = await openLaunch(page, key)
  await expect(dialog.locator('[data-launch-compare-unstated]')).toContainText('Revision 1 is a trial, and the server did not say what it starts', { timeout: 30_000 })
  await expect(dialog.locator('[data-launch-compare]')).toHaveCount(0)
  const confirm = dialog.locator('[data-launch-confirm]')
  await expect(confirm).toBeDisabled()
  await expect(confirm).toHaveText('Launch')
  await confirm.click({ force: true })
  // The body is read (its members are drawn), and the press sent nothing.
  await expect(dialog.locator('[data-launch-member="cand-a"]')).toBeVisible()
  expect(sent).toEqual([])
  await page.keyboard.press('Escape')

  // An ordinary revision: its launch carries no count.
  const plain = await ordinaryCrew(api)
  const other = await approvedTicket(api, plain.id)
  const plainSent = await watchLaunches(page, other.ticketKey, () => ({ status: 409, json: { error: { code: 'managed_off', message: 'e2e: managed runs are off here' } } }))
  const d2 = await openLaunch(page, other.key)
  await expect(d2.locator('[data-launch-member="builder"]')).toBeVisible({ timeout: 30_000 })
  await expect(d2.locator('[data-launch-compare], [data-launch-compare-unstated]')).toHaveCount(0)
  await expect(d2.locator('[data-launch-confirm]')).toHaveText('Launch')
  await d2.locator('[data-launch-confirm]').click()
  await expect(d2.locator('[data-launch-refused]')).toContainText('e2e: managed runs are off here', { timeout: 30_000 })
  expect(plainSent).toHaveLength(1)
  expect(plainSent[0]).not.toHaveProperty('candidates')
})

test("New agent trial is the name on Crews and in the palette, on a phone too, and a trial's crew shows its block read only in the crew editor", async ({ page, api, state }) => {
  test.setTimeout(120_000)
  for (const size of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
    await page.setViewportSize(size)
    await page.goto('/crews')
    // The navbar's right side: About, New crew, New agent trial and Full screen.
    const navbar = page.locator('[data-new-comparison]').locator('..')
    await expect(page.locator('[data-new-comparison]')).toBeVisible({ timeout: 30_000 })
    // Beside New crew, neither filled: one filled button a navbar, and none here.
    await expect(page.locator('[data-new-comparison]')).toHaveText('New agent trial')
    // The old name is gone from the page.
    await expect(page.getByText('New comparison')).toHaveCount(0)
    await noOverlap(navbar)
    await noSideways(page)
    if (size.width === 1440) {
      // Each button says what it makes, in its tooltip; the palette's row has the same name.
      // (The tooltip by its role, which reka also keeps on a hidden copy of the words, and the box drawn around it: tooltips.spec.ts.)
      const tip = (name: string) => page.locator('[data-slot="content"]').filter({ has: page.getByRole('tooltip', { name, exact: true, includeHidden: true }) })
      await page.locator('[data-new-comparison]').hover()
      await expect(tip('Agents each try the same task. You keep the best result.')).toBeVisible()
      // Away from the button, its tooltip goes, and the next one is a new one.
      await page.mouse.move(0, size.height - 1)
      await expect(tip('Agents each try the same task. You keep the best result.')).toHaveCount(0)
      await page.locator('[data-new-crew]').hover()
      await expect(tip('Agents that work together on one goal.')).toBeVisible()
      await page.keyboard.press('Control+k')
      const row = page.locator('[data-palette] [data-palette-row="page:new-comparison"]')
      await expect(row).toContainText('New agent trial')
      await expect(row).toContainText('Two to four agents each try the same task. You keep the best result.')
      await page.keyboard.press('Escape')
      await expect(page.locator('[data-palette]')).toHaveCount(0)
    }
  }
  await page.setViewportSize({ width: 1440, height: 900 })

  const crewId = await createComparison(page, api, {
    name: `e2e compare ${tag()}`,
    cwd: state.repo,
    criteria: ['no new dependency', 'p99 of the cache read under 2 ms'],
    checks: [{ id: 'unit', argv: ['true'] }],
    candidates: [{ agentId: 'worker', approach: 'key the cache by locale' }, { agentId: 'worker-cli', args: ['--model', 'e2e'] }],
    reviewAgent: 'worker',
    brakeman: true,
  })
  made.crews.push(crewId)
  const crew = await api.crew(crewId)
  expect(crew.tasks?.map((t) => t.id)).toEqual(['approach-a', 'approach-b', 'review-a', 'review-b'])

  // The editor: the block, read only, above the tasks.
  const block = page.locator('[data-crew-compare-block]')
  await expect(block).toHaveAttribute('data-compare-candidates', '2', { timeout: 30_000 })
  await expect(block.locator('[data-crew-compare-criterion]')).toHaveText(['no new dependency', 'p99 of the cache read under 2 ms'])
  await expect(block.locator('[data-crew-compare-lane="approach-a"] [data-crew-compare-approach]')).toHaveText('key the cache by locale')
  await expect(block.locator('[data-crew-compare-lane="approach-b"] [data-crew-compare-approach]')).toHaveText('No approach given: an independent try')
  await expect(block.locator('[data-crew-compare-lane="approach-b"]')).toContainText('cand-b · Worker, CLI (e2e stub) · --model e2e')
  await expect(block.locator('[data-crew-compare-lane="approach-a"]')).toContainText('Then: review-a')
  await expect(block.locator('[data-crew-compare-brakeman]')).toHaveText('Brakeman sums up the results when all are done.')
  await expect(block).toContainText('2 agents, each working alone from the same commit, checked alike. Read only here.')
  await expect(block.locator('[data-crew-compare-lane="approach-a"]')).toContainText('Agent A')
  await expect(block).not.toContainText('Candidate')
  await expect(block.locator('input, textarea, [role="combobox"]')).toHaveCount(0)
  const box = async (sel: string) => (await page.locator(sel).first().boundingBox())!
  expect((await box('[data-crew-compare-block]')).y).toBeLessThan((await box('[data-crew-tasks-section]')).y)
})

test("a comparison made on Crews launches from its ticket with the count its statement said, and the crews page offers no Launch of it", async ({ page, api, state }) => {
  test.setTimeout(240_000)
  const sent: Array<Record<string, unknown>> = []
  page.on('request', (r) => {
    if (r.method() === 'POST' && /^\/api\/dispatch\/tickets\/[^/]+\/launch$/.test(new URL(r.url()).pathname)) sent.push(r.postDataJSON() as Record<string, unknown>)
  })
  const made1 = await makeComparison(page, api, { cwd: state.repo, checks: [{ id: 'unit', argv: ['true'] }], candidates: [{ agentId: 'worker' }, { agentId: 'worker-cli' }] })
  made.crews.push(made1.crewId)
  made.runs.push(made1.runId)
  expect(sent.map((b) => b.candidates)).toEqual([2])
  const run = await api.run(made1.runId)
  expect(run.tasks?.map((t) => t.id).sort()).toEqual(['approach-a', 'approach-b'])

  // The crews page offers no Launch of it: the page, its palette row and the crew's launch sheet say where a comparison is launched.
  await page.goto(`/crews/${encodeURIComponent(made1.crewId)}`)
  await expect(page.locator('[data-crew-compare-block]')).toBeVisible({ timeout: 30_000 })
  await expect(page.locator('[data-launch]')).toBeDisabled()
  await page.locator('[data-launch]').hover({ force: true })
  await expect(page.getByText(WHERE).first()).toBeVisible({ timeout: 15_000 })
  await page.keyboard.press('Control+k')
  const palette = page.locator('[data-palette]')
  const row = palette.locator(`[data-palette-row="page:launch:${made1.crewId}"]`)
  await expect(row).toHaveAttribute('aria-disabled', 'true', { timeout: 15_000 })
  await expect(row).toContainText(WHERE)
  await page.keyboard.press('Escape')
  await page.goto('/agents')
  await expect(page.locator('[data-session-list]')).toBeVisible({ timeout: 30_000 })
  await page.keyboard.press('Control+k')
  const { crew: saved } = await api.ok<{ crew: { name: string } }>('GET', `/api/crews/${encodeURIComponent(made1.crewId)}`)
  await palette.locator('input[data-palette-input]').fill(`# ${saved.name}`)
  const crewRow = palette.locator(`[data-palette-row="crew:${made1.crewId}"]`)
  await expect(crewRow).toBeVisible({ timeout: 15_000 })
  await crewRow.click()
  await expect(page.locator('[data-crew-launch-comparison]')).toHaveText(`${WHERE}.`, { timeout: 30_000 })
  await expect(page.locator('[data-crew-launch-confirm]')).toBeDisabled()
  await page.locator('[data-crew-launch-cancel]').click()
  // The server refuses it all the same.
  const refused = await api.launchCrewRaw(made1.crewId)
  expect([refused.status, refused.body.error.code]).toEqual([400, 'invalid_crew'])
})

/** Where an agent trial is started, as the crews page and the crew's launch sheet say it. */
const WHERE = "An agent trial starts from a ticket's approved plan: propose this crew as the ticket's plan, approve it, and launch it there"

const PHONE = { width: 390, height: 844 }
const DESK = { width: 1440, height: 900 }

/** An expect's own bound (playwright.config.ts): what each step's budget counts per expect. */
const EXPECT_MS = 15_000

test("a comparison's plan revision and its approval show each candidate's approach and the criteria, and the checks its launch states", async ({ page, api }) => {
  const crew = await comparisonCrew(api, { reviews: true })
  const { key } = await proposedTicket(api, crew.id)
  for (const size of [DESK, PHONE]) {
    await page.setViewportSize(size)
    for (const theme of ['dark', 'light'] as const) {
      // This pass's caps: the page's two 30 s waits, its load and its theme's reload, about twenty expects and measures.
      budget(2 * 30_000 + 22 * EXPECT_MS)
      await page.goto(`/dispatch/${key}/1`)
      const featured = page.locator('[data-plan-section] [data-plan-featured]')
      await expect(featured).toHaveAttribute('data-plan-state', 'proposed', { timeout: 30_000 })
      await useTheme(page, theme, '[data-plan-section] [data-plan-featured] [data-plan-body]')
      const at = `${size.width} wide, ${theme}`
      // The revision's body: the criteria, then each candidate with its agent and approach, before the members.
      const cmp = featured.locator('[data-plan-compare]')
      await expect(cmp, at).toHaveAttribute('data-plan-compare-candidates', '2')
      await expect(cmp.locator('[data-plan-compare-criterion]')).toHaveText(['no new dependency'])
      await expect(cmp.locator('[data-plan-compare-candidate="approach-a"] [data-plan-compare-approach]')).toHaveText('key the cache by locale')
      await expect(cmp.locator('[data-plan-compare-candidate="approach-b"] [data-plan-compare-approach]')).toHaveText('No approach given: an independent try.')
      await expect(cmp.locator('[data-plan-compare-candidate="approach-b"]')).toContainText('cand-b · Worker, CLI (e2e stub)')
      // One check on each candidate attempt: each lane has the one check, never the two added together.
      await expect(featured.locator('[data-plan-expected]')).toHaveAttribute('data-count', '1')
      await expect(featured.locator('[data-plan-expected]')).toHaveText('1 check on each submitted candidate attempt, the same for every candidate.')
      const box = async (sel: string) => (await featured.locator(sel).first().boundingBox())!
      expect((await box('[data-plan-compare]')).y, at).toBeLessThan((await box('[data-plan-member]')).y)
      // The plan's own section, at the comparison (the ticket's strip of sections above it scrolls sideways by design).
      await cmp.scrollIntoViewIfNeeded()
      await noSideways(page, { root: page.locator('[data-plan-section]') })

      // The approval dialog says the same before it is approved.
      await featured.locator('[data-plan-approve]').click()
      const dialog = page.locator('[data-plan-approve-dialog]')
      await expect(dialog.locator('[data-plan-approve-yolo]')).toBeVisible({ timeout: 30_000 })
      await expect(dialog.locator('[data-plan-compare-criterion]')).toHaveText(['no new dependency'])
      await expect(dialog.locator('[data-plan-compare-candidate="approach-a"] [data-plan-compare-approach]')).toHaveText('key the cache by locale')
      await expect(dialog.locator('[data-plan-compare-candidate="approach-b"] [data-plan-compare-approach]')).toHaveText('No approach given: an independent try.')
      await expect(dialog.locator('[data-plan-compare-checks]')).toHaveAttribute('data-count', '1')
      await expect(dialog.locator('[data-plan-compare-checks]')).toHaveText('1 check on each submitted candidate attempt: unit true.')
      await noSideways(page, { root: dialog })
      await page.keyboard.press('Escape')
      await expect(dialog).toHaveCount(0)
    }
  }

  // Approved, its launch states the same count.
  budget(2 * 30_000 + 4 * EXPECT_MS)
  await page.setViewportSize(DESK)
  await page.goto(`/dispatch/${key}/1`)
  const featured = page.locator('[data-plan-section] [data-plan-featured]')
  await featured.locator('[data-plan-approve]').click()
  await page.locator('[data-plan-approve-submit]').click()
  await expect(featured).toHaveAttribute('data-plan-state', 'approved', { timeout: 30_000 })
  await featured.locator('[data-plan-launch]').click()
  const statement = page.locator('[data-launch-dialog] [data-launch-compare]')
  await expect(statement.locator('[data-launch-compare-part="checks"]')).toHaveText(/^1 check on each submitted candidate attempt /, { timeout: 30_000 })
})
