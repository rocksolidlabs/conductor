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
// them, said in words; a stop and a resume are text with the run page's link. The drafts are made work as edited, once; the crew is
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
  stub.env = { ...stub.env, STUB_BRAKEMAN_WAIT: gate, STUB_BRAKEMAN_BRIEF: briefFile }
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

/** The brief the stub submits: every item this spec drives, at the run's revision `rev`, which the turn reads as it stands. */
function brief(rev: number) {
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
  writeFileSync(briefFile, JSON.stringify(brief(seen)))
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

test('two draft tickets, one blocking the other, are made as edited with their relation, and once', async ({ page }) => {
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
  // Made again, under another id: nothing more is made, and the route names the ticket it became.
  const again = await api.call<{ error: { code: string; key: string } }>('POST', '/api/brakeman/requests/br-1/items/6/make', { opId: 'bkc-again-1', by: 'Nate' })
  expect([again.status, again.body.error.code, again.body.error.key]).toEqual([409, 'already_made', keyA])
  const list = await api.ok<{ tickets: Array<{ key: string }> }>('GET', `/api/dispatch/tickets?project=${PROJECT}`)
  expect(list.tickets.map((t) => t.key).sort()).toEqual([`${PROJECT}-1`, keyA, keyB].sort())
  // The page read again says the same: made, linked, no Create.
  await page.reload()
  await expect(b.locator('[data-draft-item="6"] [data-made-link]')).toHaveText(keyA, { timeout: 30_000 })
  await expect(b.locator('[data-draft-item="6"] [data-draft-create]')).toHaveCount(0)
})

test('a draft crew is saved and opens in the crew editor, never launched', async ({ page }) => {
  await open(page, '/brakeman?request=br-1')
  const d = page.locator('[data-brief="br-1"] [data-draft-crew]')
  await expect(d.locator('[data-draft-gates]')).toContainText('Whether the check itself is wrong.', { timeout: 30_000 })
  const before = (await api.ok<{ runs: Array<{ id: string }> }>('GET', '/api/runs')).runs.length
  await d.locator('[data-draft-crew-name] input, input[data-draft-crew-name]').first().fill('Fix the check, as edited')
  await d.locator('[data-draft-create]').click()
  await expect(page).toHaveURL(/\/crews\/[a-z0-9-]+$/, { timeout: 30_000 })
  const id = decodeURIComponent(new URL(page.url()).pathname.split('/').pop()!)
  await expect(page.locator('[data-crew-editor]')).toBeVisible()
  expect((await api.ok<{ crew: { name: string } }>('GET', `/api/crews/${encodeURIComponent(id)}`)).crew.name).toBe('Fix the check, as edited')
  expect((await request('br-1')).made!.find((m) => m.item === 8)).toMatchObject({ state: 'made', crew: id })
  // Saved, not launched: no run more than before.
  expect((await api.ok<{ runs: Array<{ id: string }> }>('GET', '/api/runs')).runs.length).toBe(before)
  await api.call('DELETE', `/api/crews/${encodeURIComponent(id)}`)
})

test('the handoff copies, and comments on its ticket', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  await open(page, '/brakeman?request=br-1')
  const h = page.locator('[data-brief="br-1"] [data-brief-handoff]')
  await expect(h.locator('[data-handoff-markdown]')).toContainText('Handoff: Get build past its check', { timeout: 30_000 })
  await h.locator('[data-handoff-copy]').click()
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toContain('## Handoff: Get build past its check')
  await h.locator(`[data-handoff-comment="${PROJECT}-1"]`).click()
  await expect(h.locator(`[data-handoff-comment="${PROJECT}-1"]`)).toHaveText(`Commented on ${PROJECT}-1`)
  const comments = await api.ok<{ comments: Array<{ body: string; by: string }> }>('GET', `/api/dispatch/tickets/${PROJECT}-1/comments?after=0&limit=50`)
  expect(comments.comments.map((x) => [x.body.split('\n')[0], x.by])).toEqual([['## Handoff: Get build past its check', 'Nate']])
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
