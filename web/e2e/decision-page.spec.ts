import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { chromium, type Browser, type BrowserContext, type Page } from '@playwright/test'
import { Api, expect, task, test, type Session } from './fixtures'
import { holdOne } from './held-helpers'
import { renderConfig, scratchRepo, startServer, stopServer, type Started } from './server'

// Needs your decision (docs/plans/decision-inbox.md, Program item MB0, PR-2), on a server of its own (as brakeman-cards.spec.ts:
// its failed task and its plan revision stay open items, which no other spec's phone should land on). On a phone (390 x 844, touch):
// the first load of / with a question waiting lands on the page and the Sessions tab then stays on the sessions; the question
// answered from the Decide tab with its four steps; a managed task that failed its check, retried from its card with its steps, and
// a retry at a revision the run has left (moved by a retry through the API first) refused with words, nothing done; a plan revision
// approved, its yolo words shown first; a turn held for the terminal shown with no choice and no line and its terminal one tap away;
// the evidence unfolded (a check's log, the session's terminal); a Recent row opening its own action, and one whose item is gone
// showing its steps; the events stream cut while one question goes and another comes, then let back. At a desktop's width: the
// sidebar's Decide row, G then N, the palette's item row, Open on your phone (the page's address alone), and a line typed on one
// item that never reaches the next. Every item is found by its ref, never by the list's length. Each step raises the test's timeout
// by its own waits' caps as it takes them (budget).
test.describe.configure({ mode: 'serial' })

/** The cap of one step's wait (an expect's, a poll's); budget raises a test's timeout by it per wait it takes. */
const STEP_MS = 15_000
function budget(waits: number, ms = STEP_MS) {
  const info = test.info()
  info.setTimeout(info.timeout + waits * ms)
}

const PROJECT = 'DEC'
let server: Started | null = null
let api: Api
let root = ''
let home = ''
let serverLog = ''
let crewId = ''
const runs: string[] = []
const sessions: string[] = []
const contexts: BrowserContext[] = []

const failCrew = {
  name: 'e2e decision page',
  goal: 'fail a check',
  cwd: '',
  where: 'server',
  isolation: 'worktree',
  openAfterLaunch: false,
  yolo: null,
  members: [
    { name: 'builder', agentId: 'worker-cli', prompt: '', start: { when: 'task' } },
    { name: 'writer', agentId: 'worker', prompt: '', start: { when: 'task' } },
  ],
  tasks: [
    { id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.', checks: [{ id: 'never', argv: ['sh', '-c', 'echo the check looked; exit 1'] }] },
    { id: 'docs', member: 'writer', kind: 'work', instructions: 'Document it.', needs: ['build'] },
  ],
}

test.beforeAll(async () => {
  const here = dirname(fileURLToPath(import.meta.url))
  root = mkdtempSync(join(tmpdir(), 'conductor-e2e-decisions-'))
  home = join(root, 'home')
  const data = join(root, 'data')
  const repo = join(root, 'repo')
  for (const d of [home, data, repo]) mkdirSync(d, { recursive: true })
  scratchRepo(repo)
  const config = join(root, 'conductor.json')
  renderConfig(join(here, 'conductor.e2e.json'), config)
  serverLog = join(root, 'server.log')
  server = await startServer({ config, home, data, allowedRoot: root, defaultCwd: repo, log: serverLog })
  api = new Api(server)
  crewId = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', failCrew)).crew.id
})

test.afterAll(async () => {
  for (const c of contexts) await c.close().catch(() => {})
  if (server) {
    for (const id of runs) await api.stopRun(id)
    for (const id of sessions) await api.stopSession(id)
    await stopServer(server.pid)
  }
  if (serverLog) {
    mkdirSync('test-results', { recursive: true })
    copyFileSync(serverLog, 'test-results/decision-page-server.log')
  }
  if (root) rmSync(root, { recursive: true, force: true })
})

/** A browser context on this spec's server, with its token where the workbench keeps it and a display name. */
async function context(browser: Browser, phone: boolean): Promise<BrowserContext> {
  const ctx = await browser.newContext(
    phone ? { baseURL: server!.baseURL, viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true } : { baseURL: server!.baseURL, viewport: { width: 1440, height: 900 } },
  )
  await ctx.addInitScript((t) => {
    localStorage.setItem('conductor.workbenchToken', t)
    localStorage.setItem('conductor.displayName', 'Nate')
  }, server!.token)
  contexts.push(ctx)
  return ctx
}

/** A phone's page at `path`, once the bottom bar (the layout's own, drawn once the layout is mounted) is there. */
async function phoneAt(page: Page, path: string) {
  budget(1)
  await page.goto(path)
  await expect(page.locator('[data-bottom-bar]')).toBeVisible()
}

/** The steps a card shows, by their names, as `state` words. */
async function stepStates(page: Page): Promise<Record<string, string>> {
  return page.locator('[data-inbox-item] [data-inbox-delivery] [data-step]').evaluateAll((els) => Object.fromEntries(els.map((e) => [e.getAttribute('data-step'), e.getAttribute('data-step-state')])))
}

/** A session's question as the server keeps it: its id, once it asks. */
async function asking(id: string): Promise<string> {
  budget(2)
  let q = ''
  await expect
    .poll(async () => {
      const a = (await api.session(id)).attention
      q = a?.state === 'needs_input' ? (a.id ?? '') : ''
      return q
    }, { timeout: 2 * STEP_MS, message: 'the session asks' })
    .not.toBe('')
  return q
}

function transcript(s: Session): string {
  const file = join(home, '.stub-sessions', `${s.agentSession?.id ?? ''}.txt`)
  return existsSync(file) ? readFileSync(file, 'utf8') : ''
}

test("a phone opened with a question waiting lands on the page, the Sessions tab stays on the sessions, and the question answered from Decide shows its four steps", async ({ browser }) => {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'asker', name: 'decide asker' })
  sessions.push(s.id)
  const q = await asking(s.id)
  const ref = `question:${s.id}/${q}`
  const phone = await (await context(browser, true)).newPage()

  // The first load of / in the tab lands on the decision page: an item waits.
  await phoneAt(phone, '/')
  budget(3)
  await expect(phone).toHaveURL(/\/decisions$/)
  await expect(phone.locator(`[data-inbox-row="${ref}"]`)).toBeVisible()
  await expect(phone.locator('[data-bottom-tab="decide"] [data-bottom-count]')).toHaveText(/^[1-9]\d*$/)
  // Decide is the first tab; the Sessions tab goes to the sessions, and a later / in the tab stays there.
  await expect(phone.locator('[data-bottom-tab]').first()).toHaveAttribute('data-bottom-tab', 'decide')
  budget(3)
  await phone.locator('[data-bottom-tab="sessions"]').tap()
  await expect(phone).toHaveURL(/\/sessions$/)
  await phoneAt(phone, '/')
  await expect(phone).toHaveURL(/\/sessions$/)
  await expect(phone.locator('[data-session-list="page"]')).toBeVisible()

  // From Decide: the item read whole, its choices each with what it authorizes.
  budget(6)
  await phone.locator('[data-bottom-tab="decide"]').tap()
  await expect(phone).toHaveURL(/\/decisions$/)
  await phone.locator(`[data-inbox-row="${ref}"]`).tap()
  const item = phone.locator(`[data-inbox-item="${ref}"]`)
  await expect(item).toBeVisible()
  await expect(item.locator('[data-inbox-question]')).toHaveText('Which database?')
  await expect(item.locator('[data-inbox-recommendation]')).toHaveText('No recommendation')
  await expect(item.locator('[data-inbox-answerer]')).toContainText('you, the operator')
  await expect(item.locator('[data-inbox-action="answer"]')).toHaveText(['Postgres', 'SQLite', 'Keep both'])
  await expect(item.locator('[data-action-scope]').first()).toContainText('Types')
  for (const b of await item.locator('[data-inbox-action]').all()) expect((await b.boundingBox())!.height).toBeGreaterThanOrEqual(44)

  // Answered: typed into the session, and the four steps, recorded and validated by the session, resumed once it works again.
  budget(4)
  await item.locator('[data-inbox-action="answer"][data-action-label="SQLite"]').tap()
  await expect.poll(() => transcript(s), { timeout: STEP_MS, message: 'the choice was typed' }).toContain('SQLite\n')
  await expect.poll(() => stepStates(phone), { timeout: STEP_MS }).toEqual({ available: 'done', recorded: 'done', validated: 'done', resumed: 'done' })
  await expect(item.locator('[data-step="recorded"] [data-step-words]')).toContainText('kept by the session')
  await expect(item.locator('[data-step="validated"] [data-step-words]')).toContainText('the session is the owner')

  // The evidence, folded until asked for: the session's terminal one tap away.
  budget(3)
  await item.locator('[data-inbox-evidence-toggle]').tap()
  await item.locator(`[data-evidence-ref="session:${s.id}"] [data-evidence-link]`).tap()
  await expect(phone).toHaveURL(new RegExp(`/sessions/${s.id}$`))
  await expect(phone.locator('[data-transport-state]').first()).toHaveAttribute('data-transport-state', 'open')

  // Recent: this browser's action, resumed.
  budget(2)
  await phoneAt(phone, '/decisions')
  await expect(phone.locator(`[data-recent-row="${ref}"] [data-recent-word]`)).toHaveText('Work resumed')
  await expect(phone.locator(`[data-inbox-row="${ref}"]`)).toHaveCount(0)

  // An item the server no longer holds (a seam: its read answers not found): the Recent row still shows what became of the action.
  await phone.route('**/api/inbox/item?**', (route) => route.fulfill({ status: 404, json: { error: { code: 'not_found', message: 'not found' } } }))
  budget(3)
  await phone.locator(`[data-recent-row="${ref}"]`).tap()
  await expect(phone.locator('[data-inbox-missing]')).toBeVisible()
  const gone = phone.locator('[data-inbox-missing-delivery] [data-step]')
  await expect(gone).toHaveCount(4)
  await expect(phone.locator('[data-inbox-missing-delivery] [data-step="resumed"]')).toHaveAttribute('data-step-state', 'done')
  await phone.unroute('**/api/inbox/item?**')
})

test("a managed task that failed its check is retried from its card with its steps; a retry at a revision the run has left is refused with words, nothing done", async ({ browser }) => {
  const run = await api.launchCrew(crewId)
  runs.push(run.id)
  const ref = `task:${run.id}/build`
  // Three attempts, each failing its check.
  budget(1, 180_000)
  await expect.poll(async () => task(await api.run(run.id), 'build').state, { timeout: 180_000, intervals: [500], message: 'build fails its check three times' }).toBe('failed')
  const phone = await (await context(browser, true)).newPage()

  await phoneAt(phone, `/decisions?item=${encodeURIComponent(ref)}`)
  budget(4)
  const item = phone.locator(`[data-inbox-item="${ref}"]`)
  await expect(item).toBeVisible()
  await expect(item.locator('[data-inbox-action]')).toHaveText(['Retry', 'Accept anyway…'])
  await expect(item.locator('[data-inbox-blocking]')).toContainText(`task:${run.id}/docs`)
  await expect(item.locator('[data-inbox-action="retry"] ~ [data-action-scope]')).toContainText('attempt 4')

  // The evidence: the failed check's log, read only once unfolded.
  budget(3)
  await item.locator('[data-inbox-evidence-toggle]').tap()
  const check = item.locator('[data-evidence-ref^="check:"]').first()
  await check.locator('[data-evidence-log-toggle]').tap()
  await expect(check.locator('[data-evidence-log]')).toContainText('the check looked')

  // The page holds the item as it read it (a seam: its re-reads of the item answer that copy), while a retry through the API moves
  // the run first: the page's Retry carries a revision the run has left.
  const shown = await api.ok<{ item: { actions: Array<{ name: string; expect: string }> } }>('GET', `/api/inbox/item?ref=${encodeURIComponent(ref)}`)
  const rev = shown.item.actions.find((a) => a.name === 'retry')!.expect
  let hold = true
  await phone.route('**/api/inbox/item?**', async (route) => (hold ? route.fulfill({ json: shown }) : route.continue()))
  await api.ok('POST', `/api/runs/${encodeURIComponent(run.id)}/tasks/build/retry`, { commandId: 'e2e-api-retry-1', expectedRevision: Number(rev) })
  budget(4)
  const refused = phone.waitForResponse((r) => r.url().includes(`/tasks/build/retry`) && r.request().method() === 'POST')
  await item.locator('[data-inbox-action="retry"]').tap()
  expect((await refused).status()).toBe(409)
  hold = false
  await expect(item.locator('[data-inbox-refusal]')).toContainText('The run moved on')
  await expect(item.locator('[data-inbox-refusal]')).toContainText('Nothing was done')
  await expect.poll(async () => (await stepStates(phone)).validated, { timeout: STEP_MS }).toBe('refused')
  // Nothing done: the API's retry made attempt 4, and the page's made none.
  await expect.poll(async () => task(await api.run(run.id), 'build').state, { timeout: 2 * STEP_MS, intervals: [500] }).toBe('failed')
  expect(task(await api.run(run.id), 'build').attempts).toBe(4)

  // Read again, the card offers Retry at the run's revision now: taken, its steps through to the next attempt running.
  budget(4)
  await phoneAt(phone, `/decisions?item=${encodeURIComponent(ref)}`)
  await expect(item.locator('[data-inbox-action="retry"]')).toBeVisible()
  await item.locator('[data-inbox-action="retry"]').tap()
  await expect.poll(async () => (await stepStates(phone)).validated, { timeout: STEP_MS }).toBe('done')
  await expect.poll(async () => (await stepStates(phone)).resumed, { timeout: 2 * STEP_MS, message: 'the next attempt runs' }).toBe('done')
  await expect(item.locator('[data-step="recorded"] [data-step-words]')).toContainText("ledger keeps the command's receipt")
  expect(task(await api.run(run.id), 'build').attempts).toBeGreaterThanOrEqual(5)

  // Recent holds both actions on the task: the refused one's row opens its own steps, not the later one's.
  budget(3)
  await phoneAt(phone, '/decisions')
  const refusedRow = phone.locator(`[data-recent-row="${ref}"]`).filter({ has: phone.locator('[data-recent-word]', { hasText: 'Refused' }) })
  await expect(refusedRow).toHaveCount(1)
  await refusedRow.tap()
  await expect(item).toBeVisible()
  await expect.poll(async () => (await stepStates(phone)).validated, { timeout: STEP_MS }).toBe('refused')
})

test('a plan revision is approved from its card, its yolo words shown before the Approve', async ({ browser }) => {
  await api.ok('POST', '/api/dispatch/projects', { opId: 'dec-p', key: PROJECT, name: 'Decisions' })
  const { ticket } = await api.ok<{ ticket: { key: string } }>('POST', '/api/dispatch/tickets', { opId: 'dec-t1', project: PROJECT, title: 'Decide its plan' })
  const { plan } = await api.ok<{ plan: { id: string; n: number } }>('POST', `/api/dispatch/tickets/${ticket.key}/plans`, { opId: 'dec-r1', crew: crewId })
  const ref = `plan:${ticket.key}/${plan.n}`
  const phone = await (await context(browser, true)).newPage()
  await phoneAt(phone, `/decisions?item=${encodeURIComponent(ref)}`)
  budget(5)
  const item = phone.locator(`[data-inbox-item="${ref}"]`)
  await expect(item).toBeVisible()
  const approve = item.locator('[data-inbox-action="approve"]')
  await expect(approve).toBeVisible()
  // What the Approve authorizes, the yolo choice among it, read from the revision's body before it is offered.
  await expect(item.locator('[data-inbox-action="approve"] ~ [data-action-scope]')).toContainText('yolo')
  await expect(item.locator('[data-inbox-action="approve"] ~ [data-action-scope]')).toContainText('launching is a separate step')
  await approve.tap()
  await expect.poll(() => stepStates(phone), { timeout: STEP_MS }).toEqual({ available: 'done', recorded: 'done', validated: 'done', resumed: 'not_applicable' })
  await expect(item.locator('[data-step="resumed"] [data-step-words]')).toContainText('a launch from the ticket page starts it')
  const { plans } = await api.ok<{ plans: Array<{ n: number; state: string }> }>('GET', `/api/dispatch/tickets/${ticket.key}/plans`)
  expect(plans.find((p) => p.n === plan.n)?.state).toBe('approved')
})

test("a turn held for the terminal shows no choice and no line, its words, and its terminal one tap away", async ({ browser }) => {
  const desk = await (await context(browser, false)).newPage()
  const { s, guestCtx } = await holdOne(desk, api, browser, home, { baseURL: server!.baseURL })
  sessions.push(s.id)
  try {
    const q = await asking(s.id)
    const ref = `question:${s.id}/${q}`
    const phone = await (await context(browser, true)).newPage()
    await phoneAt(phone, `/decisions?item=${encodeURIComponent(ref)}`)
    budget(5)
    const item = phone.locator(`[data-inbox-item="${ref}"]`)
    await expect(item.locator('[data-inbox-held]')).toContainText('held for keys typed in its terminal')
    await expect(item.locator('[data-inbox-action]')).toHaveCount(0)
    await expect(item.locator('[data-inbox-line]')).toHaveCount(0)
    await item.locator('[data-inbox-terminal]').tap()
    await expect(phone).toHaveURL(new RegExp(`/sessions/${s.id}$`))
  } finally {
    await guestCtx.close()
    await api.stopSession(s.id)
  }
})

test('with the events stream cut, a question answered and another asked meanwhile show as they are once it is back', async ({ browser }) => {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'decide cut' })
  sessions.push(s.id)
  const ask = (message: string) =>
    api.ok<{ attention: { id: string } }>('POST', `/api/sessions/${s.id}/attention`, { state: 'needs_input', message, options: [{ label: 'Left', input: 'l' }, { label: 'Right', input: 'r' }] }).then((r) => r.attention.id)
  const first = await ask('First way?')
  const ctx = await context(browser, true)
  const phone = await ctx.newPage()
  // The stream is cut: every request for it is refused until it is let back. So is the sessions' list, which the fallback poll
  // reads before it reads the inbox (useAttention's poll): with it refused the poll reads no inbox, and only the stream's return can.
  await phone.route('**/api/events', (route) => route.abort())
  await phone.route('**/api/sessions', (route) => (route.request().method() === 'GET' ? route.abort() : route.continue()))
  let lists = 0
  phone.on('request', (r) => {
    if (new URL(r.url()).pathname === '/api/inbox') lists++
  })
  await phoneAt(phone, '/decisions')
  budget(1)
  await expect(phone.locator(`[data-inbox-row="question:${s.id}/${first}"]`)).toBeVisible()
  // Meanwhile on the server: the first answered (the agent works on), another asked; the page, reading nothing, still shows the first.
  await api.ok('POST', `/api/sessions/${s.id}/attention`, { state: 'working' })
  const second = await ask('Second way?')
  const cut = lists
  await expect(phone.locator(`[data-inbox-row="question:${s.id}/${first}"]`)).toBeVisible()
  await expect(phone.locator(`[data-inbox-row="question:${s.id}/${second}"]`)).toHaveCount(0)
  // Let back, the stream alone: it comes back on its own retry (at most 15 s apart) and its snapshot reads the inbox again.
  await phone.unroute('**/api/events')
  budget(2, 2 * STEP_MS)
  await expect(phone.locator(`[data-inbox-row="question:${s.id}/${second}"]`)).toBeVisible({ timeout: 2 * STEP_MS })
  await expect(phone.locator(`[data-inbox-row="question:${s.id}/${first}"]`)).toHaveCount(0, { timeout: 2 * STEP_MS })
  expect(lists).toBeGreaterThan(cut)
  await phone.unroute('**/api/sessions')
})

/** Presses lowercase letters in turn as a person does, each handed to the page once it has taken the one before (shortcuts.spec.ts). */
async function pressInTurn(page: Page, ...keys: string[]) {
  const cdp = await page.context().newCDPSession(page)
  try {
    const sends: Array<Promise<unknown>> = []
    for (const key of keys) {
      const code = `Key${key.toUpperCase()}`
      const windowsVirtualKeyCode = key.toUpperCase().charCodeAt(0)
      sends.push(cdp.send('Input.dispatchKeyEvent', { type: 'keyDown', key, code, text: key, unmodifiedText: key, windowsVirtualKeyCode }))
      sends.push(cdp.send('Input.dispatchKeyEvent', { type: 'keyUp', key, code, windowsVirtualKeyCode }))
    }
    await Promise.all(sends)
  } finally {
    await cdp.detach()
  }
}

test("at a desktop's width: the sidebar's Decide row, G then N, the palette's item row, and Open on your phone's address alone", async ({ browser }) => {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'asker', name: 'decide desk' })
  sessions.push(s.id)
  const q = await asking(s.id)
  const ref = `question:${s.id}/${q}`
  const page = await (await context(browser, false)).newPage()
  const sidebar = page.locator('#dashboard-sidebar-main')

  // The sidebar's Decide row, with the count.
  budget(3)
  await page.goto('/yard')
  await expect(sidebar).toBeAttached()
  await page.locator('[data-sidebar-decide]').first().click()
  await expect(page).toHaveURL(/\/decisions$/)
  await expect(page.locator(`[data-inbox-row="${ref}"]`)).toBeVisible()

  // G then N, pressed once.
  budget(2)
  await page.goto('/yard')
  await expect(sidebar).toBeAttached()
  await pressInTurn(page, 'g', 'n')
  await expect(page).toHaveURL(/\/decisions$/)

  // The palette's row for the item opens it.
  budget(4)
  await page.goto('/yard')
  await expect(sidebar).toBeAttached()
  await page.keyboard.press('Alt+/')
  const palette = page.locator('[data-palette]')
  await expect(palette).toBeVisible()
  await palette.locator('input[data-palette-input]').fill('decide desk')
  await expect(palette.locator('input[data-palette-input]')).toHaveValue('decide desk')
  const row = palette.locator(`[data-palette-row="decide:${ref}"]`)
  await expect(row).toBeVisible()
  await row.click()
  await expect(page).toHaveURL(new RegExp(`/decisions\\?item=${encodeURIComponent(ref).replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`))
  await expect(page.locator(`[data-inbox-item="${ref}"]`)).toBeVisible()

  // Open on your phone, on this machine's own address: a phone cannot reach it, and the page says so.
  budget(2)
  await page.locator('[data-open-on-phone]').click()
  await expect(page.locator('[data-phone-offer="loopback"]')).toContainText('A phone cannot reach')
  await page.keyboard.press('Escape')
  await expect(page.locator('[data-phone-offer]')).toHaveCount(0)

  // On an address of the network (a name this browser resolves to the server): the page's address alone, as a QR code.
  budget(3)
  const lan = await chromium.launch({ args: ['--no-sandbox', `--host-resolver-rules=MAP decide.test 127.0.0.1`] })
  try {
    const origin = `http://decide.test:${server!.port}`
    const ctx = await lan.newContext({ baseURL: origin, viewport: { width: 1440, height: 900 } })
    await ctx.addInitScript((t) => localStorage.setItem('conductor.workbenchToken', t), server!.token)
    const there = await ctx.newPage()
    await there.goto('/decisions')
    await expect(there.locator('#dashboard-sidebar-main')).toBeAttached()
    await there.locator('[data-open-on-phone]').click()
    const offer = there.locator('[data-phone-offer="qr"]')
    await expect(offer).toHaveAttribute('data-phone-url', `${origin}/decisions`)
    await expect(offer.locator('[data-phone-qr]')).toBeVisible()
    expect(await offer.getAttribute('data-phone-url')).not.toContain(server!.token)
  } finally {
    await lan.close()
  }
})

test('at a desktop\'s width, a line typed on one item is not in the next one picked, and nothing is sent to either', async ({ browser }) => {
  // Two questions that take a line (no choices), side by side with the list at lg.
  const ask = async (name: string, message: string) => {
    const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name })
    sessions.push(s.id)
    const q = (await api.ok<{ attention: { id: string } }>('POST', `/api/sessions/${s.id}/attention`, { state: 'needs_input', message })).attention.id
    return { s, q, ref: `question:${s.id}/${q}` }
  }
  const a = await ask('decide line a', 'Which name for A?')
  const b = await ask('decide line b', 'Which name for B?')
  const page = await (await context(browser, false)).newPage()
  budget(4)
  await page.goto(`/decisions?item=${encodeURIComponent(a.ref)}`)
  const itemA = page.locator(`[data-inbox-item="${a.ref}"]`)
  await expect(itemA.locator('input[data-inbox-line-input]')).toBeVisible()
  await itemA.locator('input[data-inbox-line-input]').fill('only for A')
  await expect(itemA.locator('input[data-inbox-line-input]')).toHaveValue('only for A')

  // B picked from the list: its card is its own, its line empty, its Send not to be pressed.
  budget(4)
  await page.locator(`[data-inbox-row="${b.ref}"]`).click()
  const itemB = page.locator(`[data-inbox-item="${b.ref}"]`)
  await expect(itemB).toBeVisible()
  await expect(itemA).toHaveCount(0)
  await expect(itemB.locator('input[data-inbox-line-input]')).toHaveValue('')
  await expect(itemB.locator('[data-inbox-line-send]')).toBeDisabled()
  // Neither question was answered.
  for (const x of [a, b]) {
    const att = (await api.session(x.s.id)).attention
    expect(att?.state).toBe('needs_input')
    expect(att?.id).toBe(x.q)
  }
})

