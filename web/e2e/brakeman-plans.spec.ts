import { copyFileSync, cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { Page } from '@playwright/test'
import { Api, expect, test } from './fixtures'
import { renderConfig, scratchRepo, startServer, stopServer, type Started } from './server'

// DS2's records in a brief (docs/plans/brakeman.md, PR-11), on a server of its own (as brakeman-cards.spec.ts: its turns and records meet
// no other spec's). A ticket whose plan has revision 1 proposed from a saved crew with tasks; the stub, as Brakeman, submits the brief
// this spec writes: a decision card about plan revision 1 and a draft plan revision revising it. The card's control is DS2's own Approve,
// from the revision's live record, which opens DS2's own approval dialog and approves as the operator; the draft, edited, is proposed
// through DS2's propose route under the person's name ("Propose as plan revision") and linked once it is revision 2, which the ticket
// page's own Approve then approves. The ticket's plans are read by its key, the request by its id; every wait is on what the page or the
// store must reach.
test.describe.configure({ mode: 'serial' })

interface Request {
  id: string
  state: string
  made?: Array<{ item: number; state: string; key?: string; plan?: string }>
}
interface Plan {
  id: string
  n: number
  state: string
  basedOn?: string
  createdBy: string
  approvedBy?: string
  rev: number
}

const PROJECT = 'BKP'
const KEY = `${PROJECT}-1`
let server: Started | null = null
let api: Api
let serverLog = ''
let root = ''
let gate = ''
let briefFile = ''
let crewId = ''
let requestId = ''

test.beforeAll(async () => {
  const here = dirname(fileURLToPath(import.meta.url))
  root = mkdtempSync(join(tmpdir(), 'conductor-e2e-brakeman-plans-'))
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
  const { config: c } = await api.ok<{ config: Record<string, unknown> & { rev: number } }>('GET', '/api/brakeman')
  await api.ok('PUT', '/api/brakeman/config', { rev: c.rev, config: { ...c, enabled: true, agentId: 'brakeman-stub' } })
  await api.ok('POST', '/api/dispatch/projects', { opId: 'bkp-p', key: PROJECT, name: 'Brakeman plans' })
  await api.ok('POST', '/api/dispatch/tickets', { opId: 'bkp-t1', project: PROJECT, title: 'Plan the cache key', description: 'The cache is keyed by language.', acceptance: 'It is keyed by locale.' })
  crewId = (
    await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
      name: 'e2e brakeman plans',
      goal: 'key the cache by locale',
      cwd: '',
      where: 'server',
      isolation: 'worktree',
      openAfterLaunch: false,
      yolo: null,
      members: [{ name: 'builder', agentId: 'worker', prompt: '', start: { when: 'task' } }],
      tasks: [{ id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.' }],
    })
  ).crew.id
  // Revision 1, proposed by the operator: what the card's decision is about, and what the draft revises.
  await api.ok('POST', `/api/dispatch/tickets/${KEY}/plans`, { opId: 'bkp-r1', crew: crewId })
})

test.afterAll(async () => {
  if (server) await stopServer(server.pid)
  if (serverLog) {
    mkdirSync('test-results', { recursive: true })
    copyFileSync(serverLog, 'test-results/brakeman-plans-server.log')
    // The stubs' transcripts (what each turn read, acted and submitted), kept beside the log.
    const stubs = join(root, 'home', '.stub-sessions')
    if (existsSync(stubs)) cpSync(stubs, 'test-results/brakeman-plans-stubs', { recursive: true })
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
const plans = () => api.ok<{ plans: Plan[] }>('GET', `/api/dispatch/tickets/${KEY}/plans`).then((r) => r.plans)
const plan = async (n: number) => (await plans()).find((p) => p.n === n)

/**
 * The brief the stub submits: a fact, a decision about revision 1 at the revision's `rev` the turn reads, and a draft revision 2 revising
 * it. Items are named by their index.
 */
function brief(planRev: number) {
  return {
    schema: 'brakeman.brief/1',
    title: 'The cache key needs a plan',
    summary: 'Revision 1 keys the cache by language; the ticket asks for the locale.',
    items: [
      { kind: 'fact/1', text: `${KEY} has revision 1 proposed and none approved.`, evidence: [`ticket:${KEY}`, `plan:${KEY}/1`] },
      {
        kind: 'decision/1',
        about: `plan:${KEY}/1`,
        happened: 'Revision 1 was proposed from the crew and waits for an approval.',
        whyHuman: 'Only the operator approves a plan revision.',
        question: 'Approve revision 1 as it stands?',
        alternatives: [
          { label: 'Approve it', tradeoffs: 'A launch runs the crew as revision 1 has it.' },
          { label: 'Revise it first', tradeoffs: 'The draft below keys by locale; it waits for its own approval.' },
        ],
        recommendation: 1,
        authorizes: 'A launch from revision 1.',
        evidence: [`plan:${KEY}/1`],
        subjectRevision: String(planRev),
      },
      {
        kind: 'draft_plan/1',
        ticket: `ticket:${KEY}`,
        crew: crewId,
        basedOn: `plan:${KEY}/1`,
        constraints: "Keep the cache's file format.",
        decisions: [{ id: 'design', question: 'Key by locale or by language?', kind: 'approval', before: ['build'] }],
        note: 'Revises 1: the locale, not the language.',
        from: [`ticket:${KEY}`],
      },
    ],
  }
}

test("a decision card about a plan revision offers DS2's own Approve from its live record, and approves as the operator", async ({ page }) => {
  // The turn (one stub session, its start and its brief) is bounded by the server's own minutes; each wait below by its own cap.
  test.setTimeout(240_000)
  const asked = await api.ok<{ request: Request }>('POST', '/api/brakeman/requests', { opId: 'bkp-ask-1', kind: 'propose_crew', subjects: [`ticket:${KEY}`] })
  requestId = asked.request.id
  await expect.poll(async () => (await request(requestId)).state, { timeout: 60_000 }).toBe('running')
  writeFileSync(briefFile, JSON.stringify(brief((await plan(1))!.rev)))
  writeFileSync(gate, 'open\n')
  await expect.poll(async () => (await request(requestId)).state, { timeout: 60_000 }).toBe('briefed')

  await open(page, `/brakeman?request=${requestId}`)
  const b = page.locator(`[data-brief="${requestId}"]`)
  await expect(b.locator('[data-brief-title]')).toHaveText('The cache key needs a plan', { timeout: 30_000 })
  // The chips name the revision on its ticket's page.
  await expect(b.locator(`[data-ref-chip="plan:${KEY}/1"] [data-ref-link]`).first()).toHaveAttribute('href', `/dispatch/${PROJECT}/1`)
  const card = b.locator(`[data-decision-about="plan:${KEY}/1"]`)
  const control = card.locator('[data-plan-control]')
  // Its control is read from the revision as Dispatch holds it: proposed, so DS2's Approve.
  await expect(control).toHaveAttribute('data-plan-state', 'proposed', { timeout: 30_000 })
  await expect(control.locator('[data-plan-control-words]')).toContainText('Brakeman never does')
  await control.locator('[data-card-approve]').click()
  // DS2's own dialog: it says the revision's yolo choice once its body is read, before anything is approved.
  const dialog = page.locator('[data-plan-approve-dialog]')
  await expect(dialog).toHaveAttribute('data-plan-revision', '1')
  await expect(dialog.locator('[data-plan-approve-yolo]')).toBeVisible({ timeout: 30_000 })
  expect((await plan(1))?.state).toBe('proposed')
  await dialog.locator('[data-plan-approve-note]').fill('Approved from the brief.')
  await dialog.locator('[data-plan-approve-submit]').click()
  await expect(dialog).toBeHidden()
  // Approved by the operator under the person's name: the card reads it again and offers nothing more.
  await expect.poll(async () => (await plan(1))?.state).toBe('approved')
  expect((await plan(1))?.approvedBy).toBe('Nate')
  await expect(control).toHaveAttribute('data-plan-state', 'approved', { timeout: 30_000 })
  await expect(control.locator('[data-plan-control-words]')).toHaveText(`${KEY} r1 is approved, and its approval holds.`)
  await expect(control.locator('[data-card-approve]')).toHaveCount(0)
})

test('a draft plan revision, edited, is proposed as revision 2 under the person\'s name, then approved on the ticket page', async ({ page }) => {
  test.setTimeout(180_000)
  await open(page, `/brakeman?request=${requestId}`)
  const d = page.locator(`[data-brief="${requestId}"] [data-draft-plan][data-draft-item="2"]`)
  await expect(d.locator('[data-draft-propose]')).toBeVisible({ timeout: 30_000 })
  const note = d.locator('[data-draft-plan-note] textarea, textarea[data-draft-plan-note]').first()
  await note.fill('Revises 1: keyed by locale, as the ticket asks.')
  await expect(note).toHaveValue('Revises 1: keyed by locale, as the ticket asks.')
  const question = d.locator('[data-draft-plan-decision="design"] input, input[data-draft-plan-decision="design"]').first()
  await question.fill('Key by locale?')
  await expect(question).toHaveValue('Key by locale?')
  expect(await plans()).toHaveLength(1)
  await d.locator('[data-draft-propose]').click()
  await expect(d).toHaveAttribute('data-made-state', 'made', { timeout: 30_000 })
  // Proposed, never approved: revision 2, revising 1, under the person's name, with the edits made on the card.
  const r1 = (await plan(1))!
  await expect.poll(async () => (await plan(2))?.state).toBe('proposed')
  const r2 = (await plan(2))!
  expect([r2.basedOn, r2.createdBy]).toEqual([r1.id, 'Nate'])
  const body = await api.ok<{ plan: { body: { note?: string; decisions?: Array<{ question: string }> } } }>('GET', `/api/dispatch/plans/${r2.id}`)
  expect(body.plan.body.note).toBe('Revises 1: keyed by locale, as the ticket asks.')
  expect(body.plan.body.decisions?.[0]?.question).toBe('Key by locale?')
  expect((await request(requestId)).made?.find((m) => m.item === 2)).toMatchObject({ state: 'made', key: KEY, plan: r2.id })
  const made = d.locator('[data-draft-plan-made]')
  await expect(made).toHaveAttribute('data-plan-revision', '2', { timeout: 30_000 })
  await expect(made).toHaveAttribute('data-plan-state', 'proposed')
  // The link opens the ticket, whose Plan section lists revision 2 beside the approved one, with DS2's own Approve.
  await d.locator('[data-made-link]').click()
  await expect(page).toHaveURL(new RegExp(`/dispatch/${PROJECT}/1$`))
  const section = page.locator('[data-plan-section]')
  await expect(section.locator('[data-plan-featured]')).toHaveAttribute('data-plan-revision', '1', { timeout: 30_000 })
  const other = section.locator('[data-plan-others] [data-plan-revision="2"]')
  await expect(other).toHaveAttribute('data-plan-state', 'proposed')
  await other.locator('[data-plan-approve]').click()
  const dialog = page.locator('[data-plan-approve-dialog]')
  await expect(dialog).toHaveAttribute('data-plan-revision', '2')
  await expect(dialog.locator('[data-plan-approve-yolo]')).toBeVisible({ timeout: 30_000 })
  await dialog.locator('[data-plan-approve-submit]').click()
  await expect(dialog).toBeHidden()
  await expect(section.locator('[data-plan-featured]')).toHaveAttribute('data-plan-revision', '2', { timeout: 30_000 })
  await expect(section.locator('[data-plan-featured]')).toHaveAttribute('data-plan-state', 'approved')
  expect([(await plan(1))?.state, (await plan(2))?.state]).toEqual(['superseded', 'approved'])

  // Back on the brief: the draft names revision 2 as Dispatch holds it now, and the card about revision 1 reads it superseded,
  // which DS2's rule lets the operator approve again (nothing is approved by reading it).
  await open(page, `/brakeman?request=${requestId}`)
  await expect(page.locator(`[data-brief="${requestId}"] [data-draft-item="2"] [data-draft-plan-made]`)).toHaveAttribute('data-plan-state', 'approved', { timeout: 30_000 })
  const control = page.locator(`[data-brief="${requestId}"] [data-decision-about="plan:${KEY}/1"] [data-plan-control]`)
  await expect(control).toHaveAttribute('data-plan-state', 'superseded', { timeout: 30_000 })
  await expect(control.locator('[data-card-approve]')).toHaveText('Approve')
  expect((await plan(1))?.state).toBe('superseded')
})
