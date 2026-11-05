import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { Page } from '@playwright/test'
import { Api, expect, task, test, type Run } from './fixtures'
import { renderConfig, scratchRepo, startServer, stopServer, type Started } from './server'

// A brief's decision cards, proposals, drafts and handoff with Conductor's controls (docs/plans/brakeman.md, PR-7), on a server of
// its own (as brakeman.spec.ts: its turns and records meet no other spec's). A managed run whose check always fails (tasks.spec.ts's
// fixture) leaves build failed with a submitted result and docs blocked; the stub, as Brakeman, submits the brief this spec writes
// once the run has settled: decision cards on both tasks and proposals at the run's revision it read, two draft tickets (one blocking
// the other), a draft crew and a handoff. The cards offer what the run page's rules allow (Retry and Accept anyway, no Cancel; Cancel,
// no Retry); once the run has moved, the card's Retry and the pause proposal carry the revision Brakeman read and their routes refuse
// them, said in words; a stop and a resume are text with the run page's link; a ticket's edit shows every field it sends and asks
// with them first. The drafts are made work as edited, once, and a make that did not finish is finished as it was sent; the crew is
// saved and opened in the editor, never launched; the handoff copies and comments on its ticket. The run page's tasks and the
// palette ask Brakeman about the run and the task.
test.describe.configure({ mode: 'serial' })

interface Request {
  id: string
  state: string
  runId?: string
  made?: Array<{ item: number; state: string; key?: string; crew?: string }>
}

const PROJECT = 'BKC'
let server: Started | null = null
let api: Api
let serverLog = ''
let root = ''
let gate = ''
let briefFile = ''
let runId = ''
let seen = 0
let ticketRev = 0
let crewId = ''

const failCrew = {
  name: 'e2e brakeman cards',
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
    { id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.', checks: [{ id: 'never', argv: ['sh', '-c', 'exit 1'] }] },
    { id: 'docs', member: 'writer', kind: 'work', instructions: 'Document it.', needs: ['build'] },
  ],
}

test.beforeAll(async () => {
  const here = dirname(fileURLToPath(import.meta.url))
  // A root of its own each run, outside the suite's scratch root (brakeman.spec.ts says why).
  root = mkdtempSync(join(tmpdir(), 'conductor-e2e-brakeman-cards-'))
  const home = join(root, 'home')
  const data = join(root, 'data')
  const repo = join(root, 'repo')
  for (const d of [home, data, repo]) mkdirSync(d, { recursive: true })
  scratchRepo(repo)
  gate = join(root, 'gate')
  briefFile = join(root, 'brief.json')
  const config = join(root, 'conductor.json')
  renderConfig(join(here, 'conductor.e2e.json'), config)
  const cfg = JSON.parse(readFileSync(config, 'utf8')) as { catalog: { agents: Array<{ id: string; env?: Record<string, string> }> } }
  const stub = cfg.catalog.agents.find((a) => a.id === 'brakeman-stub')!
  // The stub reads the ticket its edit proposal names, as a turn reads what it proposes to change: the proposal's rev is that read's.
  stub.env = { ...stub.env, STUB_BRAKEMAN_WAIT: gate, STUB_BRAKEMAN_BRIEF: briefFile, STUB_BRAKEMAN_TICKET: `${PROJECT}-1` }
  writeFileSync(config, JSON.stringify(cfg, null, 2) + '\n')
  serverLog = join(root, 'server.log')
  server = await startServer({ config, home, data, allowedRoot: root, defaultCwd: repo, log: serverLog })
  api = new Api(server)
  // Brakeman on, with the stub (Settings' save is brakeman.spec.ts's).
  const { config: c } = await api.ok<{ config: Record<string, unknown> & { rev: number } }>('GET', '/api/brakeman')
  await api.ok('PUT', '/api/brakeman/config', { rev: c.rev, config: { ...c, enabled: true, agentId: 'brakeman-stub' } })
  await api.ok('POST', '/api/dispatch/projects', { opId: 'bkc-p', key: PROJECT, name: 'Brakeman cards' })
  await api.ok('POST', '/api/dispatch/tickets', { opId: 'bkc-t1', project: PROJECT, title: 'The build that never passes' })
})

test.afterAll(async () => {
  if (server) {
    if (runId) await api.stopRun(runId)
    await stopServer(server.pid)
  }
  if (serverLog) {
    mkdirSync('test-results', { recursive: true })
    copyFileSync(serverLog, 'test-results/brakeman-cards-server.log')
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
const runOf = () => api.ok<{ run: Run & { managed?: { revision: number }; paused?: boolean } }>('GET', `/api/runs/${encodeURIComponent(runId)}`).then((r) => r.run)

/**
 * The brief the stub submits: every item this spec drives, at the run's revision `rev`, which the turn reads as it stands, and the
 * ticket's `ticketRev`. Items are named by their index: new ones go at the end.
 */
function brief(rev: number, ticketRev: number) {
  const r = String(rev)
  const build = `task:${runId}/build`
  const docs = `task:${runId}/docs`
  return {
    schema: 'brakeman.brief/1',
    title: 'The build never passes its check',
    summary: 'build failed its check three times; docs waits on it.',
    items: [
      { kind: 'fact/1', text: 'build failed its check never on each of its three attempts.', evidence: [build, `run:${runId}`] },
      {
        kind: 'decision/1',
        about: build,
        happened: 'Three attempts, each refused by the check never.',
        whyHuman: 'Its attempts are spent: only a person allows another, or takes the last result.',
        question: 'Retry build, or accept its last result anyway?',
        alternatives: [
          { label: 'Retry', tradeoffs: 'One more attempt; the check has never passed.' },
          { label: 'Accept anyway', tradeoffs: 'docs starts from a result no check passed.' },
        ],
        recommendation: 0,
        authorizes: 'One more attempt of build.',
        evidence: [build],
        subjectRevision: r,
      },
      {
        kind: 'decision/1',
        about: docs,
        happened: 'docs never started: build has no result.',
        whyHuman: 'Only a person ends a task that waits for good.',
        question: 'Cancel docs?',
        alternatives: [
          { label: 'Cancel it', tradeoffs: 'The run ends with docs cancelled.' },
          { label: 'Leave it', tradeoffs: 'It waits for build.' },
        ],
        recommendation: -1,
        authorizes: 'docs ends cancelled.',
        evidence: [docs],
        subjectRevision: r,
      },
      { kind: 'proposal/1', text: 'Pause the run while the check is looked at.', op: 'run_pause', target: `run:${runId}`, expectedRevision: r, facts: [0] },
      { kind: 'proposal/1', text: 'Stop the run.', op: 'run_stop', target: `run:${runId}` },
      { kind: 'proposal/1', text: 'Resume the run later.', op: 'run_resume', target: `run:${runId}` },
      { kind: 'draft_ticket/1', project: PROJECT, title: 'Fix the check', description: 'The check never passes.', priority: 2, blocks: [7] },
      { kind: 'draft_ticket/1', project: PROJECT, title: 'Document the build', priority: 3 },
      {
        kind: 'draft_crew/1',
        crew: { name: 'Fix the check', goal: 'make the check pass', cwd: '', where: 'server', isolation: 'none', members: [{ name: 'lead', agentId: 'worker', prompt: '', start: { when: 'immediately' } }] },
        gates: [{ task: 'lead', decision: 'Whether the check itself is wrong.' }],
      },
      { kind: 'handoff/1', objective: 'Get build past its check', constraints: 'The check stays.', decisions: ['docs waits'], questions: ['Is the check right?'], evidence: [build, `ticket:${PROJECT}-1`] },
      // Its text says less than its arguments do: the card shows what the button sends, whatever the text says.
      {
        kind: 'proposal/1',
        text: 'Name the ticket after its check.',
        op: 'ticket_edit',
        target: `ticket:${PROJECT}-1`,
        expectedRevision: String(ticketRev),
        args: { set: { title: 'The check never passes', description: 'build fails its check never,\non every attempt.' } },
      },
      { kind: 'draft_ticket/1', project: PROJECT, title: 'Finish what did not finish', priority: 1 },
      { kind: 'draft_ticket/1', project: PROJECT, title: 'Asked while the page reads again', priority: 1 },
      // Valid, and longer than a ticket's comment holds (16 KiB): twenty decisions and twenty questions of 500 bytes.
      {
        kind: 'handoff/1',
        objective: 'Carry the whole check history',
        decisions: Array.from({ length: 20 }, (_, i) => `decision ${i}: ${'d'.repeat(480)}`),
        questions: Array.from({ length: 20 }, (_, i) => `question ${i}: ${'q'.repeat(480)}`),
        evidence: [`ticket:${PROJECT}-1`],
      },
      // One tab's make that does not finish, another tab's that does.
      { kind: 'draft_ticket/1', project: PROJECT, title: 'Asked in the first tab', priority: 1 },
      { kind: 'draft_ticket/1', project: PROJECT, title: 'Asked in the second tab', priority: 1 },
    ],
  }
}

test('a decision card offers what the run page allows, with the revision Brakeman read, and its route refuses it once the run moved', async ({ page }) => {
  test.setTimeout(300_000)
  crewId = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', failCrew)).crew.id
  runId = (await api.launchCrew(crewId)).id
  await expect.poll(async () => (await runOf()).outcome ?? '', { timeout: 120_000, intervals: [500] }).toBe('failed')
  // Brakeman asked; the stub reads its request and holds at its gate until the brief is written.
  const asked = await api.ok<{ request: Request }>('POST', '/api/brakeman/requests', { opId: 'bkc-ask-1', kind: 'decision', subjects: [`task:${runId}/build`] })
  await expect.poll(async () => (await request(asked.request.id)).state, { timeout: 60_000 }).toBe('running')
  const settled = await runOf()
  expect(task(settled, 'build').state).toBe('failed')
  expect(task(settled, 'docs').state).toBe('blocked')
  seen = settled.managed!.revision
  ticketRev = (await api.ok<{ ticket: { rev: number } }>('GET', `/api/dispatch/tickets/${PROJECT}-1`)).ticket.rev
  writeFileSync(briefFile, JSON.stringify(brief(seen, ticketRev)))
  writeFileSync(gate, 'open\n')
  await expect.poll(async () => (await request(asked.request.id)).state, { timeout: 60_000 }).toBe('briefed')

  await open(page, `/brakeman?request=${asked.request.id}`)
  const b = page.locator(`[data-brief="${asked.request.id}"]`)
  await expect(b.locator('[data-brief-title]')).toHaveText('The build never passes its check', { timeout: 30_000 })
  // build failed with a submitted result: Retry and Accept anyway, no Cancel; the card names who answers and what it authorizes.
  const buildCard = b.locator(`[data-decision-about="task:${runId}/build"]`)
  await expect(buildCard.locator('[data-card-command="retry"]')).toBeVisible()
  await expect(buildCard.locator('[data-card-command="accept"]')).toBeVisible()
  await expect(buildCard.locator('[data-card-command="cancel"]')).toHaveCount(0)
  await expect(buildCard.locator('[data-decision-answerer]')).toHaveText('you, the operator')
  await expect(buildCard.locator('[data-decision-authorizes]')).toHaveText('One more attempt of build.')
  await expect(buildCard.locator('[data-alternative="0"] [data-recommended]')).toBeVisible()
  // docs is blocked: Cancel, no Retry.
  const docsCard = b.locator(`[data-decision-about="task:${runId}/docs"]`)
  await expect(docsCard.locator('[data-card-command="cancel"]')).toBeVisible()
  await expect(docsCard.locator('[data-card-command="retry"]')).toHaveCount(0)
  // A stop and a resume check no revision: text with the run page's link, no button.
  for (const op of ['run_stop', 'run_resume']) {
    const p = b.locator(`[data-proposal-op="${op}"]`)
    await expect(p.locator('[data-proposal-link]')).toHaveAttribute('href', `/runs/${runId}`)
    await expect(p.locator('[data-proposal-button]')).toHaveCount(0)
  }
  const pause = b.locator('[data-proposal-op="run_pause"]')
  await expect(pause.locator('[data-proposal-button]')).toHaveText('Pause the run')

  // The run moves on (a pause and an unpause, the run page's own), past the revision Brakeman read.
  await api.ok('POST', `/api/runs/${encodeURIComponent(runId)}/pause`, { commandId: 'bkc-pause-1' })
  await api.ok('POST', `/api/runs/${encodeURIComponent(runId)}/unpause`, { commandId: 'bkc-unpause-1' })
  const moved = (await runOf()).managed!.revision
  expect(moved).toBeGreaterThan(seen)
  // The card's Retry carries the revision Brakeman read: the route refuses it, and the card says what changed. Nothing was done.
  await buildCard.locator('[data-card-command="retry"]').click()
  // (A refused command is a transaction of the run's too: the revision the card reads after it is past the one moved to.)
  const refused = buildCard.locator('[data-card-refused]')
  await expect(refused).toContainText(new RegExp(`^The run has moved on since Brakeman read it \\(revision ${seen}; it is at revision \\d+ now, and build is failed\\)\\. Nothing was done`))
  expect(Number(/at revision (\d+) now/.exec((await refused.textContent()) ?? '')?.[1])).toBeGreaterThanOrEqual(moved)
  expect(task(await runOf(), 'build').attempts).toBe(3)
  // The pause proposal the same way: refused by its route, with words; the run is not paused.
  await pause.locator('[data-proposal-button]').click()
  await expect(pause.locator('[data-proposal-refused]')).toContainText(new RegExp(`^The run has moved on since Brakeman read it \\(revision ${seen}; it is at revision \\d+ now\\)\\. Nothing was done`))
  expect((await runOf()).paused ?? false).toBe(false)
})

test("a ticket's proposal shows every field it sends, and asks with them before it edits the ticket", async ({ page }) => {
  await open(page, '/brakeman?request=br-1')
  const p = page.locator('[data-brief="br-1"] [data-proposal-op="ticket_edit"]')
  // On the card, before anything is pressed: each field the edit sets, as it is sent.
  await expect(p.locator('[data-proposal-args] [data-proposal-arg="set.title"]')).toHaveText('The check never passes', { timeout: 30_000 })
  // Every character of it, its line break too (textContent: toHaveText would fold the whitespace).
  await expect(p.locator('[data-proposal-args] [data-proposal-arg="set.description"]')).toHaveJSProperty('textContent', 'build fails its check never,\non every attempt.')
  await expect(p.locator('[data-proposal-args] [data-proposal-arg]')).toHaveCount(2)
  const stored = () => api.ok<{ ticket: { title: string; description: string; rev: number } }>('GET', `/api/dispatch/tickets/${PROJECT}-1`).then((r) => r.ticket)
  await p.locator('[data-proposal-button]').click()
  // Asked first, with the same fields; nothing is sent until the person says so.
  const asked = page.locator('[data-proposal-confirm-args]')
  await expect(asked.locator('[data-proposal-arg="set.title"]')).toHaveText('The check never passes')
  await expect(asked.locator('[data-proposal-arg="set.description"]')).toHaveJSProperty('textContent', 'build fails its check never,\non every attempt.')
  expect(await stored()).toMatchObject({ title: 'The build that never passes', rev: ticketRev })
  await page.locator('[data-proposal-confirm]').click()
  await expect.poll(async () => (await stored()).title).toBe('The check never passes')
  expect((await stored()).description).toBe('build fails its check never,\non every attempt.')
  await expect(p.locator('[data-proposal-refused]')).toHaveCount(0)
})

test('two draft tickets, one blocking the other, are made as edited with their relation, and once', async ({ page }) => {
  // Its waits' caps add up to 210 s (four of 30 s, six of the config's 15 s), past the config's 120 s.
  test.setTimeout(300_000)
  await open(page, '/brakeman?request=br-1')
  const b = page.locator('[data-brief="br-1"]')
  const a = b.locator('[data-draft-ticket][data-draft-item="6"]')
  const c = b.locator('[data-draft-ticket][data-draft-item="7"]')
  await expect(a.locator('[data-draft-blocks]')).toHaveText('Blocks "Document the build".', { timeout: 30_000 })
  // The second first: the relation is made whichever end is made last.
  await c.locator('[data-draft-title] input, input[data-draft-title]').first().fill('Document the build, edited')
  await c.locator('[data-draft-create]').click()
  await expect(c).toHaveAttribute('data-made-state', 'made')
  await a.locator('[data-draft-title] input, input[data-draft-title]').first().fill('Fix the check, edited')
  await a.locator('[data-draft-create]').click()
  await expect(a).toHaveAttribute('data-made-state', 'made')
  const r = await request('br-1')
  const keyA = r.made!.find((m) => m.item === 6)!.key!
  const keyB = r.made!.find((m) => m.item === 7)!.key!
  await expect(a.locator('[data-made-link]')).toHaveText(keyA)
  await expect(a.locator('[data-made-link]')).toHaveAttribute('href', `/dispatch/${PROJECT}/${keyA.split('-')[1]}`)
  const ticketA = (await api.ok<{ ticket: { title: string; priority: number; relations: Array<{ kind: string; other: { key: string }; toId: string; fromId: string; id: string; deletedAt?: string }> } }>('GET', `/api/dispatch/tickets/${keyA}`)).ticket
  expect(ticketA.title).toBe('Fix the check, edited')
  expect(ticketA.priority).toBe(2)
  expect((await api.ok<{ ticket: { title: string } }>('GET', `/api/dispatch/tickets/${keyB}`)).ticket.title).toBe('Document the build, edited')
  await expect.poll(async () => (await api.ok<{ ticket: typeof ticketA }>('GET', `/api/dispatch/tickets/${keyA}`)).ticket.relations.filter((x) => !x.deletedAt).map((x) => [x.kind, x.other.key])).toEqual([['blocks', keyB]])
  // The Blocks line names the other draft as it was made: the ticket it became and the title the person gave it, never the brief's
  // (the live check's line kept the title Brakeman wrote).
  await expect(a.locator('[data-draft-blocks]')).toHaveText(`Blocks ${keyB} "Document the build, edited" (related).`, { timeout: 30_000 })
  // Made again, under another id: nothing more is made, and the route names the ticket it became.
  const again = await api.call<{ error: { code: string; key: string } }>('POST', '/api/brakeman/requests/br-1/items/6/make', { opId: 'bkc-again-1', by: 'Nate' })
  expect([again.status, again.body.error.code, again.body.error.key]).toEqual([409, 'already_made', keyA])
  const list = await api.ok<{ tickets: Array<{ key: string }> }>('GET', `/api/dispatch/tickets?project=${PROJECT}`)
  expect(list.tickets.map((t) => t.key).sort()).toEqual([`${PROJECT}-1`, keyA, keyB].sort())
  // The page read again says the same: made, linked, no Create.
  await page.reload()
  await expect(b.locator('[data-draft-item="6"] [data-made-link]')).toHaveText(keyA, { timeout: 30_000 })
  await expect(b.locator('[data-draft-item="6"] [data-draft-create]')).toHaveCount(0)
  // Read again, the page names the ticket by its title as Dispatch holds it, read by its key: still the person's, never the brief's.
  await expect(b.locator('[data-draft-item="6"] [data-draft-blocks]')).toHaveText(`Blocks ${keyB} "Document the build, edited" (related).`, { timeout: 30_000 })
})

test('a make that did not finish is finished from the card as it was sent, after the page is read again', async ({ page }) => {
  // The first ask is answered as the route answers a backend that failed after the claim; the ask again goes to the server. The
  // server's side (a claim left pending, finished by the same id and content, make_pending for another id) is Go's
  // TestConcurrentMakesOfOneItemMakeOne; this is the page's: it keeps the id and the content, and asks again with exactly those.
  const bodies: unknown[] = []
  await page.route('**/api/brakeman/requests/br-1/items/11/make', async (route) => {
    bodies.push(route.request().postDataJSON())
    if (bodies.length === 1) {
      await route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: { code: 'make_failed', message: 'the make did not finish: it is completed when it is asked again with the same opId, or at the next start' } }) })
    } else await route.continue()
  })
  await open(page, '/brakeman?request=br-1')
  const d = page.locator('[data-brief="br-1"] [data-draft-ticket][data-draft-item="11"]')
  const title = d.locator('[data-draft-title] input, input[data-draft-title]').first()
  await title.fill('Finished as sent', { timeout: 30_000 })
  await d.locator('[data-draft-create]').click()
  await expect(d.locator('[data-draft-error]')).toContainText('did not finish')
  await expect(d.locator('[data-draft-finish]')).toBeVisible()
  await expect(d.locator('[data-draft-create]')).toHaveCount(0)
  await expect(title).toBeDisabled()
  // Read again, the page still holds the make, with the content it was asked with.
  await page.reload()
  await expect(d.locator('[data-draft-finish]')).toBeVisible({ timeout: 30_000 })
  await expect(title).toHaveValue('Finished as sent')
  await expect(title).toBeDisabled()
  await d.locator('[data-draft-finish]').click()
  await expect(d).toHaveAttribute('data-made-state', 'made')
  expect(bodies).toHaveLength(2)
  expect(bodies[1]).toEqual(bodies[0])
  const key = (await request('br-1')).made!.find((m) => m.item === 11)!.key!
  expect((await api.ok<{ ticket: { title: string } }>('GET', `/api/dispatch/tickets/${key}`)).ticket.title).toBe('Finished as sent')
  const list = await api.ok<{ tickets: Array<{ key: string; title: string }> }>('GET', `/api/dispatch/tickets?project=${PROJECT}`)
  expect(list.tickets.filter((t) => t.title === 'Finished as sent').map((t) => t.key)).toEqual([key])
  // Made, the browser holds nothing more to finish: read again, the card is the ticket.
  await page.reload()
  await expect(d.locator('[data-made-link]')).toHaveText(key, { timeout: 30_000 })
  await expect(d.locator('[data-draft-finish]')).toHaveCount(0)
})

test('a make kept before it is sent is finished after the page is read again with its reply out, through a refused token', async ({ page }) => {
  // The first ask's reply never comes (held while the page is read again); the first finish is refused before the route runs (401:
  // the token gate opens); with the token saved again the finish goes to the server. Each ask is the first's id and content.
  // What the route does goes by the test's phase, never by a count: an ask held before the reload is held, whatever the page sends.
  const bodies: unknown[] = []
  let phase: 'hold' | 'refuse' | 'pass' = 'hold'
  let held = 0
  const holding = new Promise<void>(() => {})
  await page.route('**/api/brakeman/requests/br-1/items/12/make', async (route) => {
    bodies.push(route.request().postDataJSON())
    if (phase === 'hold') {
      held++
      await holding // never answered: the page is read again with the reply out
    } else if (phase === 'refuse') {
      phase = 'pass'
      await route.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: { code: 'unauthorized', message: 'workbench token required' } }) })
    } else await route.continue()
  })
  await open(page, '/brakeman?request=br-1')
  const d = page.locator('[data-brief="br-1"] [data-draft-ticket][data-draft-item="12"]')
  const title = d.locator('[data-draft-title] input, input[data-draft-title]').first()
  await title.fill('Kept while it was out', { timeout: 30_000 })
  await d.locator('[data-draft-create]').click()
  await expect.poll(() => held).toBeGreaterThanOrEqual(1)
  // While the reply is out the card is the new make's, not one to finish.
  await expect(d.locator('[data-draft-finish]')).toHaveCount(0)
  await page.reload()
  phase = 'refuse'
  await expect(d.locator('[data-draft-finish]')).toBeVisible({ timeout: 30_000 })
  await expect(title).toHaveValue('Kept while it was out')
  await expect(title).toBeDisabled()
  // Refused before the route ran: still kept, and the token gate asks for the token.
  await d.locator('[data-draft-finish]').click()
  await expect(d.locator('[data-draft-error]')).toContainText('workbench token required')
  await expect(d.locator('[data-draft-finish]')).toBeVisible()
  const gate = page.getByRole('dialog', { name: 'Workbench token' })
  await gate.getByRole('button', { name: 'Save' }).click()
  await expect(gate).toHaveCount(0)
  await d.locator('[data-draft-finish]').click()
  await expect(d).toHaveAttribute('data-made-state', 'made')
  // The held ask, the refused finish and the finish that made it: one id and one content throughout.
  expect(bodies.length).toBeGreaterThanOrEqual(3)
  for (const b of bodies) expect(b).toEqual(bodies[0])
  const key = (await request('br-1')).made!.find((m) => m.item === 12)!.key!
  expect((await api.ok<{ ticket: { title: string } }>('GET', `/api/dispatch/tickets/${key}`)).ticket.title).toBe('Kept while it was out')
  await page.reload()
  await expect(d.locator('[data-made-link]')).toHaveText(key, { timeout: 30_000 })
  await expect(d.locator('[data-draft-finish]')).toHaveCount(0)
  // The held handler never returns: the page closes without waiting on it.
  await page.unrouteAll({ behavior: 'ignoreErrors' })
})

test("a make that did not finish in one tab is still finished after another tab's make finished", async ({ page, context }) => {
  // Both tabs are open before either makes: A's make is answered as a backend that failed after the claim, B's is made. B forgets
  // its own make, and only it: A read again still holds its make, with the content it was asked with, and finishes it.
  const bodies: unknown[] = []
  await page.route('**/api/brakeman/requests/br-1/items/14/make', async (route) => {
    bodies.push(route.request().postDataJSON())
    if (bodies.length === 1) {
      await route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: { code: 'make_failed', message: 'the make did not finish: it is completed when it is asked again with the same opId, or at the next start' } }) })
    } else await route.continue()
  })
  const other = await context.newPage()
  await open(page, '/brakeman?request=br-1')
  await open(other, '/brakeman?request=br-1')
  const a = page.locator('[data-brief="br-1"] [data-draft-ticket][data-draft-item="14"]')
  const b = other.locator('[data-brief="br-1"] [data-draft-ticket][data-draft-item="15"]')
  const titleA = a.locator('[data-draft-title] input, input[data-draft-title]').first()
  await expect(titleA).toBeEditable({ timeout: 30_000 })
  await expect(b.locator('[data-draft-create]')).toBeVisible({ timeout: 30_000 })
  await titleA.fill('Kept in the first tab')
  await a.locator('[data-draft-create]').click()
  await expect(a.locator('[data-draft-finish]')).toBeVisible()
  await b.locator('[data-draft-create]').click()
  await expect(b).toHaveAttribute('data-made-state', 'made')
  await page.reload()
  await expect(a.locator('[data-draft-finish]')).toBeVisible({ timeout: 30_000 })
  await expect(titleA).toHaveValue('Kept in the first tab')
  await a.locator('[data-draft-finish]').click()
  await expect(a).toHaveAttribute('data-made-state', 'made')
  expect(bodies).toHaveLength(2)
  expect(bodies[1]).toEqual(bodies[0])
  const key = (await request('br-1')).made!.find((m) => m.item === 14)!.key!
  expect((await api.ok<{ ticket: { title: string } }>('GET', `/api/dispatch/tickets/${key}`)).ticket.title).toBe('Kept in the first tab')
  await other.close()
})

test('a draft crew is saved and opens in the crew editor, never launched', async ({ page }) => {
  await open(page, '/brakeman?request=br-1')
  const d = page.locator('[data-brief="br-1"] [data-draft-crew]')
  await expect(d.locator('[data-draft-gates]')).toContainText('Whether the check itself is wrong.', { timeout: 30_000 })
  await d.locator('[data-draft-crew-name] input, input[data-draft-crew-name]').first().fill('Fix the check, as edited')
  await d.locator('[data-draft-create]').click()
  await expect(page).toHaveURL(/\/crews\/[a-z0-9-]+$/, { timeout: 30_000 })
  const id = decodeURIComponent(new URL(page.url()).pathname.split('/').pop()!)
  await expect(page.locator('[data-crew-editor]')).toBeVisible()
  expect((await api.ok<{ crew: { name: string } }>('GET', `/api/crews/${encodeURIComponent(id)}`)).crew.name).toBe('Fix the check, as edited')
  expect((await request('br-1')).made!.find((m) => m.item === 8)).toMatchObject({ state: 'made', crew: id })
  // Saved, not launched: no run is of the crew (by its id, never by how many runs there are: Brakeman's turns are runs too).
  expect((await api.ok<{ runs: Array<{ id: string; crewId?: string }> }>('GET', '/api/runs')).runs.filter((r) => r.crewId === id)).toEqual([])
  await api.call('DELETE', `/api/crews/${encodeURIComponent(id)}`)
})

test('the handoff copies, and comments on its ticket', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  await open(page, '/brakeman?request=br-1')
  const h = page.locator('[data-brief="br-1"] [data-brief-item="9"] [data-brief-handoff]')
  await expect(h.locator('[data-handoff-markdown]')).toContainText('Handoff: Get build past its check', { timeout: 30_000 })
  await expect(h.locator('[data-handoff-bound]')).toHaveCount(0)
  await h.locator('[data-handoff-copy]').click()
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toContain('## Handoff: Get build past its check')
  await h.locator(`[data-handoff-comment="${PROJECT}-1"]`).click()
  await expect(h.locator(`[data-handoff-comment="${PROJECT}-1"]`)).toHaveText(`Commented on ${PROJECT}-1`)
  const comments = await api.ok<{ comments: Array<{ body: string; by: string }> }>('GET', `/api/dispatch/tickets/${PROJECT}-1/comments?after=0&limit=50`)
  expect(comments.comments.map((x) => [x.body.split('\n')[0], x.by])).toEqual([['## Handoff: Get build past its check', 'Nate']])
})

test("a handoff longer than a ticket's comment holds is commented as the person shortens it, and Copy keeps the whole", async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  await open(page, '/brakeman?request=br-1')
  const h = page.locator('[data-brief="br-1"] [data-brief-item="13"] [data-brief-handoff]')
  await expect(h.locator('[data-handoff-bound]')).toContainText("more than a ticket's comment holds (16.0 KiB)", { timeout: 30_000 })
  const button = h.locator(`[data-handoff-comment="${PROJECT}-1"]`)
  await expect(button).toBeDisabled()
  await h.locator('[data-handoff-copy]').click()
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toContain('question 19:')
  // Shortened to the bound exactly (in UTF-8 bytes, as Dispatch measures it), it is sent; a byte over, it is not.
  const body = h.locator('[data-handoff-comment-body] textarea, textarea[data-handoff-comment-body]').first()
  const head = '## Handoff: Carry the whole check history\n\n'
  const fill = 'é'.repeat((16384 - head.length - 1) / 2)
  await body.fill(head + fill + 'xx')
  await expect(button).toBeDisabled()
  await body.fill(head + fill + 'x')
  await expect(h.locator('[data-handoff-comment-size]')).toHaveText('16.0 KiB of 16.0 KiB')
  await button.click()
  await expect(button).toHaveText(`Commented on ${PROJECT}-1`)
  const comments = await api.ok<{ comments: Array<{ body: string }> }>('GET', `/api/dispatch/tickets/${PROJECT}-1/comments?after=0&limit=50`)
  const sent = comments.comments.find((x) => x.body.startsWith('## Handoff: Carry the whole check history'))!
  expect(Buffer.byteLength(sent.body, 'utf8')).toBe(16384)
})

test("the run page's Ask Brakeman why and the palette's Brakeman rows open the composer about the task and the run", async ({ page }) => {
  await open(page, `/runs/${encodeURIComponent(runId)}?view=tasks`)
  const ask = page.locator('[data-task-row="build"] [data-task-ask-brakeman="build"]')
  await expect(ask).toBeVisible({ timeout: 30_000 })
  // docs, blocked, is asked about too (a failed, cancelled or blocked task, or one whose attempt reports a blocker).
  await expect(page.locator('[data-task-row="docs"] [data-task-ask-brakeman="docs"]')).toBeVisible()
  await ask.click()
  await expect(page).toHaveURL(new RegExp(`/brakeman\\?kind=explain&subject=${encodeURIComponent(`task:${runId}/build`)}$`))
  await expect(page.locator('[data-ask-kind]')).toContainText('Explain this')
  await expect(page.locator('[data-ask-subjects]')).toContainText('build')

  // The palette on the run page, the task selected: brief me since…, explain this run, explain this task, Open Brakeman.
  await page.goto(`${server!.baseURL}/runs/${encodeURIComponent(runId)}?view=tasks&task=build`)
  await expect(page.locator('#dashboard-sidebar-main')).toBeAttached()
  await expect(page.locator('[data-task-row="build"]')).toBeVisible({ timeout: 30_000 })
  await page.keyboard.press('Alt+/')
  const palette = page.locator('[data-palette]')
  await expect(palette).toBeVisible()
  await palette.locator('input[data-palette-input]').fill('ask brakeman')
  for (const key of ['briefing', 'explain-run', 'explain-task']) await expect(palette.locator(`[data-palette-row="brakeman:${key}"]`)).toBeVisible()
  await palette.locator('[data-palette-row="brakeman:explain-run"]').click()
  await expect(page).toHaveURL(new RegExp(`/brakeman\\?kind=explain&subject=${encodeURIComponent(`run:${runId}`)}$`))
  await expect(page.locator('[data-ask-subjects]')).toContainText(runId)
})
