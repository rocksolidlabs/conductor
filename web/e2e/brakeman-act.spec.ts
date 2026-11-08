import { copyFileSync, cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { Page } from '@playwright/test'
import { Api, expect, test } from './fixtures'
import { renderConfig, scratchRepo, startServer, stopServer, type Started } from './server'

// Brakeman's authorized actions on the app's pages (docs/plans/brakeman.md, PR-13), on a server of its own (as brakeman-cards.spec.ts).
// A ticket whose plan's revision 1 the operator approved. Granted "Launch an approved plan" in Settings, the stub, as Brakeman, reads the
// ticket, launches the approved revision with conductor brakeman act through DS2's own launch route, and submits a brief proposing that
// launch: the brief shows "Brakeman did this" with the launch's receipt and the run it started, and the run page says who launched it,
// with the receipt. With the grant taken away, the same act is refused before anything is kept, the stub says so, and the brief's
// proposal is a proposal only. Granted "Route a decision" (PR-18), the stub routes a plan gate's open decision to the crew's security
// reviewer through DR1's own route: the brief shows DR1's receipt, the decision stays open, and its card says the operator answers it for
// ana. Each turn holds at its own gate until this spec has written what it does; the launch and the requests are
// read by their ids, every wait on what the page or the store must reach.
test.describe.configure({ mode: 'serial' })

// A test's waits fit in its timeout (AGENTS.md): each step raises the test's timeout by the caps of the waits it is about to make
// (budget, as palette-rows.spec.ts does), so the timeout is their sum, with the config's 120 s above it for what happens between them
// (the API's calls, a page's load). A wait without a bound of its own (an expect, an expect.poll, a click, a fill) has STEP_MS.
const STEP_MS = 15_000
test.use({ actionTimeout: STEP_MS })
/** Raises the running test's timeout by `ms`, the caps of the waits the step is about to make. */
function budget(ms: number) {
  const info = test.info()
  info.setTimeout(info.timeout + ms)
}

interface Act {
  opId: string
  operation: string
  op: string
  target: string
  state: string
  receipt?: string
  why?: string
}
interface Request {
  id: string
  state: string
  acts?: Act[]
}
interface Launch {
  id: string
  runId: string
  createdBy: string
  state: string
}

const PROJECT = 'BKA'
const KEY = `${PROJECT}-1`
let server: Started | null = null
let api: Api
let serverLog = ''
let root = ''
let gate = ''
let briefFile = ''
let actFile = ''
let planId = ''
const runs: string[] = []

test.beforeAll(async () => {
  const here = dirname(fileURLToPath(import.meta.url))
  root = mkdtempSync(join(tmpdir(), 'conductor-e2e-brakeman-act-'))
  const home = join(root, 'home')
  const data = join(root, 'data')
  const repo = join(root, 'repo')
  for (const d of [home, data, repo]) mkdirSync(d, { recursive: true })
  scratchRepo(repo)
  gate = join(root, 'gate')
  briefFile = join(root, 'brief.json')
  actFile = join(root, 'act')
  const config = join(root, 'conductor.json')
  renderConfig(join(here, 'conductor.e2e.json'), config)
  const cfg = JSON.parse(readFileSync(config, 'utf8')) as { catalog: { agents: Array<{ id: string; env?: Record<string, string> }> } }
  const stub = cfg.catalog.agents.find((a) => a.id === 'brakeman-stub')!
  // The stub reads the ticket it acts on, as a turn reads what it proposes: the proposal's revision is that read's.
  stub.env = { ...stub.env, STUB_BRAKEMAN_WAIT: gate, STUB_BRAKEMAN_BRIEF: briefFile, STUB_BRAKEMAN_TICKET: KEY, STUB_BRAKEMAN_ACT: actFile }
  writeFileSync(config, JSON.stringify(cfg, null, 2) + '\n')
  serverLog = join(root, 'server.log')
  server = await startServer({ config, home, data, allowedRoot: root, defaultCwd: repo, log: serverLog })
  api = new Api(server)
  const { config: c } = await api.ok<{ config: Record<string, unknown> & { rev: number } }>('GET', '/api/brakeman')
  await api.ok('PUT', '/api/brakeman/config', { rev: c.rev, config: { ...c, enabled: true, agentId: 'brakeman-stub' } })
  await api.ok('POST', '/api/dispatch/projects', { opId: 'bka-p', key: PROJECT, name: 'Brakeman acts' })
  await api.ok('POST', '/api/dispatch/tickets', { opId: 'bka-t1', project: PROJECT, title: 'Launch the approved plan' })
  const crewId = (
    await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
      name: 'e2e brakeman act',
      goal: 'build it',
      cwd: '',
      where: 'server',
      isolation: 'worktree',
      openAfterLaunch: false,
      yolo: null,
      members: [{ name: 'builder', agentId: 'worker', prompt: '', start: { when: 'task' } }],
      tasks: [{ id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.' }],
    })
  ).crew.id
  // Revision 1, proposed and approved by the operator: the one revision a granted launch may run.
  const proposed = await api.ok<{ plan: { id: string; rev: number } }>('POST', `/api/dispatch/tickets/${KEY}/plans`, { opId: 'bka-r1', crew: crewId })
  planId = proposed.plan.id
  await api.ok('POST', `/api/dispatch/plans/${planId}/approve`, { opId: 'bka-a1', expectedRev: proposed.plan.rev, ticketRev: await ticketRev() })
})

test.afterAll(async () => {
  if (server) {
    for (const id of runs) await api.stopRun(id)
    await stopServer(server.pid)
  }
  if (serverLog) {
    mkdirSync('test-results', { recursive: true })
    copyFileSync(serverLog, 'test-results/brakeman-act-server.log')
    // The stubs' transcripts (what each turn read, acted and submitted), kept beside the log.
    const stubs = join(root, 'home', '.stub-sessions')
    if (existsSync(stubs)) cpSync(stubs, 'test-results/brakeman-act-stubs', { recursive: true })
  }
  if (root) rmSync(root, { recursive: true, force: true })
})

/** The page on this spec's server, with its token where the workbench keeps it and a display name. */
async function open(page: Page, path: string) {
  await page.addInitScript((t) => {
    localStorage.setItem('conductor.workbenchToken', t)
    localStorage.setItem('conductor.displayName', 'Nate')
  }, server!.token)
  await page.goto(`${server!.baseURL}${path}`)
}

const request = (id: string) => api.ok<{ request: Request }>('GET', `/api/brakeman/requests/${id}`).then((r) => r.request)
async function ticketRev(): Promise<number> {
  return (await api.ok<{ ticket: { rev: number } }>('GET', `/api/dispatch/tickets/${KEY}`)).ticket.rev
}
/** What the stub wrote of its act once it ran it: taken or refused, then the command's words. */
async function actOut(): Promise<string> {
  budget(60_000)
  await expect.poll(() => existsSync(`${actFile}.out`), { timeout: 60_000 }).toBe(true)
  return readFileSync(`${actFile}.out`, 'utf8')
}

/** The grants saved, at the revision read. */
async function grant(act: string[]) {
  const { config: c } = await api.ok<{ config: Record<string, unknown> & { rev: number; permissions: Record<string, unknown> } }>('GET', '/api/brakeman')
  await api.ok('PUT', '/api/brakeman/config', { rev: c.rev, config: { ...c, permissions: { ...c.permissions, act } } })
}

/**
 * One turn: what it acts on and the brief it submits written, its gate shut until then, then the request asked and the turn let through;
 * answers the request once briefed. The act's id is `actId`, and the brief proposes the launch it acts on, at the ticket's revision the
 * turn reads, naming that act (args.opId), as the guide asks of a proposal of an act Brakeman ran.
 */
async function turn(opId: string, actId: string, propose = true): Promise<string> {
  budget(120_000 + 60_000)
  rmSync(gate, { force: true })
  rmSync(`${actFile}.out`, { force: true })
  const rev = await ticketRev()
  writeFileSync(actFile, `launch_approved_plan ${KEY}\n`)
  writeFileSync(`${actFile}.body`, JSON.stringify({ plan: planId, expectedRev: rev, opId: actId }))
  const fact = { kind: 'fact/1', text: `Revision 1 of ${KEY} is approved.`, evidence: [`ticket:${KEY}`] }
  writeFileSync(
    briefFile,
    JSON.stringify(
      propose
        ? {
            schema: 'brakeman.brief/1',
            title: `${KEY}'s approved revision`,
            summary: 'Revision 1 is approved and its approval holds: it can be launched.',
            items: [fact, { kind: 'proposal/1', text: 'Launch revision 1.', op: 'launch_approved_plan', target: `ticket:${KEY}`, expectedRevision: String(rev), args: { plan: planId, opId: actId }, facts: [0] }],
          }
        : {
            // As the live check's models briefed an act they ran: a fact, no proposal naming the act, and what they were unsure of as a
            // Markdown list.
            schema: 'brakeman.brief/1',
            title: `${KEY} launched`,
            summary: 'Revision 1 was launched.',
            uncertainty: '- Whether its run has finished.\n- Whether its checks pass.',
            items: [fact],
          },
    ),
  )
  const asked = await api.ok<{ request: Request }>('POST', '/api/brakeman/requests', { opId, kind: 'explain', subjects: [`ticket:${KEY}`] })
  // One turn at a time: a turn before this one holds the slot until its run has stopped (the brief's grace, then the stop).
  await expect.poll(async () => (await request(asked.request.id)).state, { timeout: 120_000 }).toBe('running')
  writeFileSync(gate, 'open\n')
  await expect.poll(async () => (await request(asked.request.id)).state, { timeout: 60_000 }).toBe('briefed')
  return asked.request.id
}

test('granted in Settings, Brakeman launches the approved revision, and the brief and the run page show its receipt', async ({ page }) => {
  // The grant, in Settings, with what it authorizes in words: one wait of 30 s, then fifteen steps and the poll of the saved grant.
  budget(30_000 + 16 * STEP_MS)
  await open(page, '/settings')
  const g = page.locator('[data-brakeman-grant="launch_approved_plan"]')
  await expect(g).toHaveAttribute('data-grant-served', 'true', { timeout: 30_000 })
  await expect(page.locator('[data-brakeman-grants]')).toContainText('never approves one')
  // A grant this build serves no route of cannot be turned on.
  await expect(page.locator('[data-brakeman-grant="request_repair"]')).toHaveAttribute('data-grant-served', 'false')
  // Routing a decision is served, to a person of its run's crew too, and never answers one.
  await expect(page.locator('[data-brakeman-grant="route_decision"]')).toHaveAttribute('data-grant-served', 'true')
  await expect(page.locator('[data-brakeman-grants]')).toContainText("or to a person of its run's crew, by name or by the responsibility they hold. It never answers one")
  const sw = page.locator('[data-brakeman-grants]').getByRole('switch', { name: 'Launch an approved plan' })
  await expect(sw).toHaveAttribute('aria-checked', 'false')
  // With drafting and proposing off Brakeman observes only, until a grant is on: a grant runs by itself, whatever those two say. Both
  // are put back before the save.
  const draft = page.getByRole('switch', { name: 'Draft', exact: true })
  const propose = page.getByRole('switch', { name: 'Propose', exact: true })
  const observeOnly = page.locator('[data-brakeman-observe-only]')
  await expect(observeOnly).toHaveCount(0)
  await draft.click()
  await propose.click()
  await expect(observeOnly).toBeVisible()
  await sw.click()
  await expect(sw).toHaveAttribute('aria-checked', 'true')
  await expect(observeOnly).toHaveCount(0)
  await draft.click()
  await propose.click()
  await expect(draft).toHaveAttribute('aria-checked', 'true')
  await expect(propose).toHaveAttribute('aria-checked', 'true')
  await page.locator('[data-brakeman-save]').click()
  await expect.poll(async () => (await api.ok<{ config: { permissions: { act?: string[] } } }>('GET', '/api/brakeman')).config.permissions.act).toEqual(['launch_approved_plan'])

  const id = await turn('bka-ask-1', 'bka-act-1')
  expect(await actOut()).toMatch(/^taken\nlaunch_approved_plan ran \(opId bka-act-1\): done; its receipt is op:brakeman\/bk[0-9a-f]{30}, the ref a brief cites it by \(an outcome\/1's receipt\); launch [0-9a-f]{16}; run \S+ launched/)
  const acts = (await request(id)).acts ?? []
  expect(acts).toHaveLength(1)
  expect(acts[0]).toMatchObject({ opId: 'bka-act-1', operation: 'launch_approved_plan', target: `ticket:${KEY}`, state: 'done' })
  const launch = (await api.ok<{ launch: Launch }>('GET', `/api/dispatch/launches/${acts[0]!.receipt}`)).launch
  runs.push(launch.runId)
  expect([launch.createdBy, launch.state]).toEqual(['Brakeman', 'started'])

  // The brief and the run page: five waits of 30 s and eight steps.
  budget(5 * 30_000 + 8 * STEP_MS)
  await open(page, `/brakeman?request=${id}`)
  const p = page.locator(`[data-brief="${id}"] [data-proposal-op="launch_approved_plan"]`)
  await expect(p).toHaveAttribute('data-proposal-act', 'done', { timeout: 30_000 })
  await expect(p.locator('[data-proposal-did-badge]')).toHaveText('Brakeman did this')
  // The act's receipt is its own (op:brakeman/<op>, what an outcome cites and its chip shows), the launch's id named as the launch (#715).
  const op = acts[0]!.op
  await expect(p.locator('[data-proposal-did-words]')).toHaveText(`Brakeman did this, under the grant "Launch an approved plan" you gave it: receipt ${op}; the launch ${launch.id} of ${KEY}.`)
  await expect(p.locator(`[data-proposal-did-receipt][data-ref-chip="op:brakeman/${op}"]`)).toHaveText(`receipt ${op}`)
  await expect(p.locator(`[data-proposal-did-launch][data-ref-chip="launch:${launch.id}"]`)).toContainText(`launch ${launch.id}`)
  // Done: no control offers it again.
  await expect(p.locator('[data-proposal-button], [data-proposal-text]')).toHaveCount(0)
  const run = p.locator(`[data-proposal-did-run="${launch.runId}"]`)
  await expect(run).toHaveAttribute('href', `/runs/${launch.runId}`, { timeout: 30_000 })
  // The run page: launched from the ticket's revision, by Brakeman, with the launch's receipt.
  await run.click()
  await expect(page).toHaveURL(new RegExp(`/runs/${launch.runId}$`))
  await expect(page.locator('[data-run-launched-from]')).toHaveAttribute('data-run-launched-key', KEY, { timeout: 30_000 })
  const header = page.locator('[data-run-launch]')
  await expect(header).toHaveAttribute('data-run-launch-by', 'Brakeman', { timeout: 30_000 })
  await expect(header.locator(`[data-ref-chip="launch:${launch.id}"]`)).toBeVisible()
  // Its receipt, read from the chip: the launch as Dispatch keeps it.
  await header.locator('[data-ref-receipt]').click()
  await expect(page.locator('[data-ref-receipt-text]')).toContainText(`"runId": "${launch.runId}"`, { timeout: 30_000 })

  // A launch is done by its launch, not by its receipt, which Dispatch keeps done before the run is started: with the launch's route
  // answering it pending, then failed (its answer rewritten in this page's reads alone), the brief says it is under way, then not done
  // with the launch's words, and links no run either time. The brief reads a pending launch again by itself. Three waits of 30 s and
  // eleven steps.
  budget(3 * 30_000 + 11 * STEP_MS)
  let shownState = 'pending'
  const launchRoute = `**/api/dispatch/launches/${launch.id}`
  await page.route(launchRoute, async (route) => {
    const r = await route.fetch()
    const body = (await r.json()) as { launch: Launch & { why?: string } }
    body.launch.state = shownState
    if (shownState === 'failed') body.launch.why = 'the agent could not be started'
    await route.fulfill({ response: r, json: body })
  })
  // The page's event stream restarts again and again, each restart a snapshot after which the page reads its requests again (as the
  // stream's restart does after a dropped connection): every read hands the request's act as a new object. Once `reread` is set, the
  // reads name the request's agent version "re-read", which the brief's provenance line draws: the moment a read was taken in, read off
  // the page rather than guessed.
  let reread = false
  const eventsRoute = '**/api/events'
  await page.route(eventsRoute, (route) => route.fulfill({ status: 200, contentType: 'text/event-stream', body: 'event: snapshot\ndata: []\n\n' }))
  const requestsRoute = (u: URL) => u.pathname.startsWith('/api/brakeman/requests')
  await page.route(requestsRoute, async (route) => {
    if (route.request().method() !== 'GET') return route.fallback()
    const r = await route.fetch()
    const body = (await r.json()) as { request?: Request & { agent?: { version?: string } }; requests?: Array<Request & { agent?: { version?: string } }> }
    for (const q of [body.request, ...(body.requests ?? [])]) if (reread && q?.id === id && q.agent) q.agent.version = 're-read'
    await route.fulfill({ response: r, json: body })
  })
  await open(page, `/brakeman?request=${id}`)
  await expect(p.locator('[data-proposal-did-words]')).toHaveText(`Brakeman launched this, under the grant "Launch an approved plan": receipt ${op}; the launch ${launch.id} of ${KEY}. Its run is still being started.`, { timeout: 30_000 })
  await expect(p).toHaveAttribute('data-proposal-act', 'pending')
  await expect(p.locator('[data-proposal-did-badge]')).toHaveText('Brakeman is doing this')
  await expect(p.locator('[data-proposal-did-run]')).toHaveCount(0)
  shownState = 'failed'
  await expect(p).toHaveAttribute('data-proposal-act', 'failed', { timeout: 30_000 })
  await expect(p.locator('[data-proposal-did-badge]')).toHaveText('Not done')
  await expect(p.locator('[data-proposal-did-words]')).toHaveText(`Brakeman launched this, under the grant "Launch an approved plan": receipt ${op}, but the launch ${launch.id} of ${KEY} failed: the agent could not be started. No run was started.`)
  await expect(p.locator('[data-proposal-did-run]')).toHaveCount(0)
  // A launch that failed started nothing: the proposal is offered again, as text with its page.
  await expect(p.locator('[data-proposal-text]')).toBeVisible()
  // And it stays offered once the request is read again: the same act, a new object, still failed (#691's review).
  reread = true
  await expect(page.locator(`[data-brief="${id}"] [data-brief-provenance]`)).toContainText('version re-read', { timeout: 30_000 })
  await expect(p).toHaveAttribute('data-proposal-act', 'failed')
  await expect(p.locator('[data-proposal-did]')).toHaveAttribute('data-act-shown', 'failed')
  await expect(p.locator('[data-proposal-text]')).toBeVisible()
  await page.unroute(launchRoute)
  await page.unroute(eventsRoute)
  await page.unroute(requestsRoute)
})

test("an act the brief names in no proposal shows apart with its receipt, and the act's words give the ref that cites it", async ({ page }) => {
  // The grant is still on from the test before (serial).
  const id = await turn('bka-ask-apart', 'bka-act-apart', false)
  const out = await actOut()
  const acts = (await request(id)).acts ?? []
  expect(acts).toHaveLength(1)
  expect(acts[0]).toMatchObject({ opId: 'bka-act-apart', state: 'done' })
  const launch = (await api.ok<{ launch: Launch }>('GET', `/api/dispatch/launches/${acts[0]!.receipt}`)).launch
  runs.push(launch.runId)
  // The words name the receipt by the ref a brief cites it by, and the launch as the launch.
  expect(out).toContain(`its receipt is op:brakeman/${acts[0]!.op},`)
  expect(out).toContain(`; launch ${launch.id};`)

  // The brief: one wait of 30 s and six steps.
  budget(30_000 + 6 * STEP_MS)
  await open(page, `/brakeman?request=${id}`)
  const apart = page.locator(`[data-brief="${id}"] [data-brief-acts]`)
  const card = apart.locator('[data-act="bka-act-apart"]')
  await expect(card).toHaveAttribute('data-act-shown', 'done', { timeout: 30_000 })
  await expect(card.locator('[data-proposal-did-badge]')).toHaveText('Brakeman did this')
  // The card's receipt is the ref the act's words gave, as an outcome citing it shows it; the launch is its own chip.
  await expect(card.locator(`[data-proposal-did-receipt][data-ref-chip="op:brakeman/${acts[0]!.op}"]`)).toHaveText(`receipt ${acts[0]!.op}`)
  await expect(card.locator(`[data-proposal-did-launch][data-ref-chip="launch:${launch.id}"]`)).toBeVisible()
  await expect(card.locator(`[data-proposal-did-run="${launch.runId}"]`)).toHaveAttribute('href', `/runs/${launch.runId}`)
  await expect(page.locator(`[data-brief="${id}"] [data-brief-proposal]`)).toHaveCount(0)
  // What Brakeman is unsure of is Markdown, drawn as the summary is: its list a list.
  await expect(page.locator(`[data-brief="${id}"] [data-brief-uncertainty] li`)).toHaveCount(2)
})

test('with the grant taken away, the act is refused and kept nowhere, and the proposal is a proposal only', async ({ page }) => {
  await grant([])
  const entries = async () => (await api.ok<{ entries?: unknown[] }>('GET', `/api/dispatch/tickets/${KEY}/runs`)).entries?.length ?? 0
  const before = await entries()
  // The two launches of the tests before it, each granted.
  expect(before).toBe(2)
  const id = await turn('bka-ask-2', 'bka-act-2')
  const out = await actOut()
  expect(out).toMatch(/^refused\n/)
  expect(out).toContain('not_granted')
  expect((await request(id)).acts ?? []).toHaveLength(0)
  expect(await entries()).toBe(before)

  // The brief: one wait of 30 s and three steps.
  budget(30_000 + 3 * STEP_MS)
  await open(page, `/brakeman?request=${id}`)
  const p = page.locator(`[data-brief="${id}"] [data-proposal-op="launch_approved_plan"]`)
  await expect(p.locator('[data-proposal-text]')).toContainText("ticket's Plan section", { timeout: 30_000 })
  await expect(p).toHaveAttribute('data-proposal-act', '')
  await expect(p.locator('[data-proposal-did]')).toHaveCount(0)
  await expect(p.locator('[data-proposal-link]')).toHaveAttribute('href', `/dispatch/${PROJECT}/1`)
})

test("granted Route a decision, Brakeman routes a gate's decision to the crew's security reviewer: the brief shows DR1's receipt and the card says for whom", async ({ page }) => {
  // A crew with people (nate the architecture owner, ana the security reviewer) and a ticket whose approved revision gates build: launched
  // by the operator, the run opens the gate's decision, which holds build, so the run stays held with it open. The spec's API calls.
  budget(120_000)
  await api.ok('POST', '/api/dispatch/tickets', { opId: 'bka-t2', project: PROJECT, title: 'Route the gate' })
  const key = `${PROJECT}-2`
  const crewId = (
    await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
      name: 'e2e brakeman route',
      goal: 'build it once approved',
      cwd: '',
      where: 'server',
      isolation: 'worktree',
      openAfterLaunch: false,
      yolo: null,
      members: [{ name: 'builder', agentId: 'worker', prompt: '', start: { when: 'task' } }],
      people: [
        { name: 'nate', responsibilities: ['architecture'] },
        { name: 'ana', responsibilities: ['security'] },
      ],
      tasks: [{ id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.' }],
    })
  ).crew.id
  const tk = async () => (await api.ok<{ ticket: { rev: number } }>('GET', `/api/dispatch/tickets/${key}`)).ticket.rev
  const proposed = await api.ok<{ plan: { id: string; rev: number } }>('POST', `/api/dispatch/tickets/${key}/plans`, {
    opId: 'bka-r2',
    crew: crewId,
    decisions: [{ id: 'ship', question: 'May build start?', kind: 'approval', before: ['build'] }],
  })
  await api.ok('POST', `/api/dispatch/plans/${proposed.plan.id}/approve`, { opId: 'bka-a2', expectedRev: proposed.plan.rev, ticketRev: await tk() })
  const runId = (await api.ok<{ launch: { runId: string } }>('POST', `/api/dispatch/tickets/${key}/launch`, { opId: 'bka-l2', plan: proposed.plan.id })).launch.runId
  runs.push(runId)
  type Listed = { id: string; origin: string; state: string; rev: number }
  let gateDecision: Listed | undefined
  await expect
    .poll(
      async () => {
        const r = await api.ok<{ decisions: Listed[] }>('GET', `/api/decisions?run=${encodeURIComponent(runId)}`)
        gateDecision = r.decisions.find((d) => d.origin === 'plan' && d.state === 'open')
        return Boolean(gateDecision)
      },
      { timeout: 60_000 },
    )
    .toBe(true)
  const did = gateDecision!.id
  await grant(['route_decision'])

  // The turn: it reads the run, whose decisions list the gate's with its ref and rev, routes it to role:security at that rev with
  // conductor brakeman act through DR1's own route, then submits a brief proposing that route on the decision's ref at the rev it read
  // (the stub's @REV, from its own read with its own token), naming the act (args.opId). Its gate shut until all of it is written.
  budget(120_000 + 60_000)
  rmSync(gate, { force: true })
  rmSync(`${actFile}.out`, { force: true })
  writeFileSync(`${actFile}.run`, `${runId}\n`)
  writeFileSync(actFile, `route_decision ${did} did\n`)
  writeFileSync(`${actFile}.body`, `{"to":{"kind":"role","name":"security"},"expectedRev":@REV,"note":"ana reviews what build touches.","opId":"bka-route-1"}`)
  writeFileSync(
    briefFile,
    JSON.stringify({
      schema: 'brakeman.brief/1',
      title: `${did} waits on the security reviewer`,
      summary: 'The gate before build is a security question: it is routed to the crew\'s security reviewer, for the operator to answer.',
      items: [
        { kind: 'fact/1', text: `${did} holds build.`, evidence: [`run:${runId}`] },
        { kind: 'proposal/1', text: `Route ${did} to the security reviewer.`, op: 'route_decision', target: `decision:${did}`, expectedRevision: '@REV', args: { opId: 'bka-route-1' }, facts: [0] },
      ],
    }),
  )
  let id = ''
  try {
    const asked = await api.ok<{ request: Request }>('POST', '/api/brakeman/requests', { opId: 'bka-ask-route', kind: 'explain', subjects: [`run:${runId}`] })
    id = asked.request.id
    await expect.poll(async () => (await request(id)).state, { timeout: 120_000 }).toBe('running')
    writeFileSync(gate, 'open\n')
    await expect.poll(async () => (await request(id)).state, { timeout: 60_000 }).toBe('briefed')
  } finally {
    rmSync(`${actFile}.run`, { force: true })
  }
  const out = await actOut()
  // What the receipt says: the decision, the person and the responsibility, and Brakeman, in whichever words DR1 writes them.
  expect(out).toMatch(new RegExp(`^taken\\nroute_decision ran \\(opId bka-route-1\\): done; its receipt is row [0-9]+ of ${did}'s history: ${did} `))
  expect(out).toContain('ana, the security reviewer')
  expect(out).toContain('brakeman')
  const acts = (await request(id)).acts ?? []
  expect(acts).toHaveLength(1)
  expect(acts[0]).toMatchObject({ opId: 'bka-route-1', operation: 'route_decision', target: `decision:${did}`, run: runId, state: 'done' })
  expect(acts[0]!.receipt).toMatch(new RegExp(`^${did}#[0-9]+$`))
  // Routed, never answered: the decision is still open, the operator's to answer.
  const after = await api.ok<{ decision: { state: string; history: Array<{ kind: string; by?: string }> } }>('GET', `/api/decisions/${did}`)
  expect(after.decision.state).toBe('open')
  expect(after.decision.history.filter((h) => h.kind === 'routed' && h.by === 'brakeman')).toHaveLength(1)

  // The brief and the decision's card: two waits of 30 s and nine steps.
  budget(2 * 30_000 + 9 * STEP_MS)
  await open(page, `/brakeman?request=${id}`)
  const p = page.locator(`[data-brief="${id}"] [data-proposal-op="route_decision"]`)
  await expect(p).toHaveAttribute('data-proposal-act', 'done', { timeout: 30_000 })
  await expect(p.locator('[data-proposal-did-badge]')).toHaveText('Brakeman did this')
  const seq = acts[0]!.receipt!.split('#')[1]
  await expect(p.locator('[data-proposal-did-words]')).toHaveText(`Brakeman did this, under the grant "Route a decision" you gave it; ${did} routed: row ${seq} of its history.`)
  // DR1's receipt is the decision's history: no op: chip, which names a Dispatch receipt.
  await expect(p.locator('[data-proposal-did-receipt]')).toHaveCount(0)
  await p.locator(`[data-proposal-did-decision="${did}"]`).click()
  // The decision's card: the operator answers it, for ana, as its history says.
  const card = page.locator(`[data-decision-routed-for="ana"]`)
  await expect(card).toHaveText(', for ana (routed by brakeman)', { timeout: 30_000 })
  // Its history, unfolded: the route Brakeman made, to ana.
  await page.locator('[data-decision-history-toggle]').click()
  await expect(page.locator('[data-decision-history] [data-history-kind="routed"]').filter({ hasText: 'routed to ana: ana reviews what build touches. · brakeman' })).toHaveCount(1)
})

test('Settings opened before the right token was saved offers the grants once it is, with no reload', async ({ page }) => {
  // The guide's read is refused with the token the browser holds, and asked again when the gate saves the right one: Settings is not
  // mounted again, so the read that failed as it mounted is the only one the old code made (#538's review). A refusal of the token
  // before that lands after the save does not ask for the token again (#281): the first guide read, sent with the wrong token, is held
  // until the gate has closed and answered then, and the read for the right token is held until that refusal is in, so the gate is
  // judged after a late 401 every run, not when a loaded machine happens to make one. Two waits of 30 s, then eight steps.
  budget(2 * 30_000 + 8 * STEP_MS)
  const wrong = 'not-the-workbench-token'
  let releaseWrong!: () => void
  const wrongHeld = new Promise<void>((r) => (releaseWrong = r))
  let releaseRight!: () => void
  const rightHeld = new Promise<void>((r) => (releaseRight = r))
  let heldWrong = 0
  await page.route('**/api/guide?*', async (route) => {
    if (route.request().headers()['authorization'] === `Bearer ${wrong}`) {
      heldWrong++
      await wrongHeld
    } else await rightHeld
    await route.continue()
  })
  await page.addInitScript((t) => {
    localStorage.setItem('conductor.workbenchToken', t)
    localStorage.setItem('conductor.displayName', 'Nate')
  }, wrong)
  await page.goto(`${server!.baseURL}/settings`)
  const gate = page.getByRole('dialog', { name: 'Workbench token' })
  await expect(gate).toBeVisible({ timeout: 30_000 })
  await expect.poll(() => heldWrong).toBeGreaterThanOrEqual(1)
  const field = gate.getByPlaceholder('paste the workbench token')
  await field.fill(server!.token)
  await expect(field).toHaveValue(server!.token)
  await gate.getByRole('button', { name: 'Save' }).click()
  await expect(gate).toHaveCount(0)
  // The wrong token's refusal, answered after the save: the page has its whole body before the right token's read is let go.
  const late = page.waitForResponse((r) => r.url().includes('/api/guide?') && r.request().headers()['authorization'] === `Bearer ${wrong}`)
  releaseWrong()
  const refusal = await late
  expect(refusal.status()).toBe(401)
  await refusal.finished()
  releaseRight()
  const g = page.locator('[data-brakeman-grant="launch_approved_plan"]')
  await expect(g).toHaveAttribute('data-grant-served', 'true', { timeout: 30_000 })
  // The late refusal was the token before's: the gate stays closed (it opened again here before #281's fix, the page behind it hidden
  // from the switch below), the right token kept.
  await expect(gate).toHaveCount(0)
  expect(await page.evaluate(() => localStorage.getItem('conductor.workbenchToken'))).toBe(server!.token)
  await expect(page.locator('[data-brakeman-grants]').getByRole('switch', { name: 'Launch an approved plan' })).toBeEnabled()
})
