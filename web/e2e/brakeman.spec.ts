import { copyFileSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { Api, expect, test, type Run } from './fixtures'
import { renderConfig, scratchRepo, startServer, stopServer, type Started } from './server'

// Brakeman on the app's pages (docs/plans/brakeman.md, PR-6), on a server of its own and its own data, so that its turns (runs and
// sessions of their own) meet no other spec and a restart finds the same records: Settings saves Brakeman with the stub as its
// agent and terminal screens off; a request asked from the page waits in the queue behind the one in progress, then runs with its
// session's link; the stub's brief, held by a gate until the spec opens it, arrives while a terminal has the focus and takes
// neither the focus nor the page, and counts in the sidebar; the brief shows its provenance, Brakeman's reading, the fact with its
// evidence linking to the run and the session, and the interpretation naming the fact; the turn's run is stopped after it; and the
// server started again on the same data shows the same brief.
test.describe.configure({ mode: 'serial' })

interface Request {
  id: string
  state: string
  runId?: string
  sessionId?: string
  hasBrief?: boolean
}

let server: Started | null = null
let api: Api
let serverLog = ''
let gate = ''
let start: () => Promise<Started>
let briefed: Request

test.beforeAll(async ({ state }) => {
  const here = dirname(fileURLToPath(import.meta.url))
  const root = join(state.root, 'brakeman')
  const home = join(root, 'home')
  const data = join(root, 'data')
  const repo = join(root, 'repo')
  for (const d of [home, data, repo]) mkdirSync(d, { recursive: true })
  scratchRepo(repo)
  // The stub's gate: it submits its brief once this file exists, so a request holds running while the spec looks at it.
  gate = join(root, 'gate')
  const config = join(root, 'conductor.json')
  renderConfig(join(here, 'conductor.e2e.json'), config)
  const cfg = JSON.parse(readFileSync(config, 'utf8')) as { catalog: { agents: Array<{ id: string; env?: Record<string, string> }> } }
  const stub = cfg.catalog.agents.find((a) => a.id === 'brakeman-stub')!
  stub.env = { ...stub.env, STUB_BRAKEMAN_WAIT: gate }
  writeFileSync(config, JSON.stringify(cfg, null, 2) + '\n')
  serverLog = join(root, 'server.log')
  start = () => startServer({ config, home, data, allowedRoot: root, defaultCwd: repo, log: serverLog })
  server = await start()
  api = new Api(server)
})

test.afterAll(async () => {
  if (server) await stopServer(server.pid)
  if (serverLog) {
    mkdirSync('test-results', { recursive: true })
    copyFileSync(serverLog, 'test-results/brakeman-server.log')
  }
})

/** The page on this spec's server, with its token where the workbench keeps it. */
async function open(page: import('@playwright/test').Page, path: string) {
  await page.addInitScript((t) => localStorage.setItem('conductor.workbenchToken', t), server!.token)
  await page.goto(`${server!.baseURL}${path}`)
}

const request = (id: string) => api.ok<{ request: Request }>('GET', `/api/brakeman/requests/${id}`).then((r) => r.request)

test('Settings saves Brakeman with the stub as its agent and terminal screens off', async ({ page }) => {
  await open(page, '/settings')
  const card = page.locator('[data-brakeman-settings]')
  await expect(card).toBeVisible()
  // Terminal screens are off until a person turns them on, and say what they would let Brakeman read.
  const terminals = card.getByRole('switch', { name: 'Terminal screens' })
  await expect(terminals).toHaveAttribute('aria-checked', 'false')
  await expect(card).toContainText("Lets Brakeman read what a session's screen shows")
  await card.getByRole('switch', { name: 'Brakeman on' }).click()
  await card.locator('[data-brakeman-agent]').click()
  await page.getByRole('option', { name: 'Brakeman (e2e stub)' }).click()
  // The stub's adapter is Claude Code's: it takes a model, and can be told to allow Conductor's tools.
  await expect(card.locator('[data-brakeman-model]')).toBeVisible()
  await card.locator('[data-brakeman-save]').click()
  await expect.poll(async () => {
    const c = (await api.ok<{ config: { enabled: boolean; agentId: string; visibility: { terminals: boolean; runs: boolean }; rev: number } }>('GET', '/api/brakeman')).config
    return [c.enabled, c.agentId, c.visibility.terminals, c.visibility.runs, c.rev]
  }, { timeout: 15_000 }).toEqual([true, 'brakeman-stub', false, true, 1])
  await expect(card.locator('[data-brakeman-save]')).toBeDisabled()
})

test('a briefing waits behind the turn in progress, then runs in its session, and its brief takes neither the focus nor the page', async ({ page }) => {
  test.setTimeout(180_000)
  await open(page, '/brakeman')
  const ask = page.locator('[data-brakeman-ask]')
  // The first request holds the slot (the stub waits at its gate).
  await ask.locator('[data-ask-kind]').click()
  await page.getByRole('option', { name: 'Overlapping changes' }).click()
  await ask.locator('[data-ask-send]').click()
  const current = page.locator('[data-brakeman-current]')
  await expect(current.locator('[data-request="br-1"]')).toHaveAttribute('data-request-state', 'running', { timeout: 30_000 })
  // The briefing waits in the queue.
  await ask.locator('[data-ask-kind]').click()
  await page.getByRole('option', { name: 'Brief me since…' }).click()
  await ask.locator('[data-ask-send]').click()
  const queued = page.locator('[data-brakeman-queue] [data-request="br-2"]')
  await expect(queued).toHaveAttribute('data-request-state', 'queued')
  await expect(queued.locator('[data-request-words]')).toHaveText('queued')
  expect((await request('br-2')).state).toBe('queued')
  // Stop ends the first: it is cancelled, and the briefing runs in its own session, which the page links.
  await current.locator('[data-request="br-1"] [data-request-stop]').click()
  await expect.poll(async () => (await request('br-1')).state, { timeout: 15_000 }).toBe('cancelled')
  await expect(current.locator('[data-request="br-2"]')).toHaveAttribute('data-request-state', 'running', { timeout: 60_000 })
  await expect(current.locator('[data-request="br-2"] [data-request-words]')).toHaveText('running in Brakeman · br-2')
  const running = await request('br-2')
  expect(running.sessionId).toMatch(/^[0-9a-f]{16}$/)
  await expect(page.locator('[data-brakeman-briefs] [data-request="br-1"] [data-request-words]')).toHaveText('cancelled: a person cancelled it')
  // Its terminal, from the page's link, with the focus in it.
  await current.locator('[data-request="br-2"] [data-request-session]').click()
  await expect(page).toHaveURL(new RegExp(`/sessions/${running.sessionId}$`))
  await expect(page.locator('[data-transport-state]').first()).toHaveAttribute('data-transport-state', 'open', { timeout: 30_000 })
  const terminal = page.locator('.terminal-host .xterm-helper-textarea').first()
  await terminal.focus()
  await page.evaluate(() => ((document.activeElement as HTMLElement & { __brakemanMark?: boolean }).__brakemanMark = true))
  const url = page.url()
  const row = page.locator('[data-sidebar-brakeman]')
  await expect(row).toBeVisible()
  await expect(row).not.toContainText(/\d/)
  // The gate opens: the stub submits its brief.
  writeFileSync(gate, 'open\n')
  await expect.poll(async () => (briefed = await request('br-2')).state, { timeout: 30_000 }).toBe('briefed')
  // The sidebar counts it; the focus and the page are as they were.
  await expect(row).toContainText('1')
  expect(page.url()).toBe(url)
  expect(await page.evaluate(() => (document.activeElement as HTMLElement & { __brakemanMark?: boolean } | null)?.__brakemanMark === true)).toBe(true)
  await expect(page.getByRole('dialog')).toHaveCount(0)

  // The brief, opened from the sidebar's row.
  await row.click()
  await expect(page).toHaveURL(/\/brakeman$/)
  await page.locator('[data-brakeman-briefs] [data-request="br-2"] [data-request-open]').click()
  const brief = page.locator('[data-brief="br-2"]')
  await expect(brief.locator('[data-brief-provenance]')).toContainText('brakeman-stub (claude) · its default model · version 2.1.287 · request br-2 · as of')
  await expect(brief.locator('[data-brief-title]')).toHaveText('Stub brief for br-2')
  await expect(brief.locator('[data-brief-reading]')).toContainText("Brakeman's reading")
  await expect(brief.locator('[data-brief-reading]')).toContainText('The stub read its request and what changed, and found its own turn.')
  const fact = brief.locator('[data-brief-fact]')
  await expect(fact).toContainText('This turn runs as its own session and run.')
  await expect(fact.locator(`[data-ref-chip="run:${briefed.runId}"] [data-ref-link]`)).toHaveAttribute('href', `/runs/${briefed.runId}`)
  await expect(fact.locator(`[data-ref-chip="session:${briefed.sessionId}"] [data-ref-link]`)).toHaveAttribute('href', `/sessions/${briefed.sessionId}`)
  await expect(brief.locator('[data-brief-interpretation]')).toContainText('Nothing else happened that the stub was asked about.')
  await expect(brief.locator('[data-brief-interpretation] [data-reads]')).toHaveText('reads fact 1')
  // Opened, it is no longer counted.
  await expect(row).not.toContainText(/\d/)

  // The turn's run is stopped once its brief is kept (after the desk's grace for the agent's last output).
  await expect.poll(async () => (await api.ok<{ run: Run }>('GET', `/api/runs/${briefed.runId}`)).run.state, { timeout: 60_000 }).toBe('stopped')
  // The run's chip says it has changed since the brief was kept, and still leads to the run.
  await expect(fact.locator(`[data-ref-chip="run:${briefed.runId}"]`)).toHaveAttribute('data-changed', 'true', { timeout: 15_000 })
  await fact.locator(`[data-ref-chip="run:${briefed.runId}"] [data-ref-link]`).click()
  await expect(page).toHaveURL(new RegExp(`/runs/${briefed.runId}$`))
})

test('the server started again on the same data shows the same brief', async ({ page }) => {
  await stopServer(server!.pid)
  server = null
  server = await start()
  api = new Api(server)
  expect((await request('br-2')).state).toBe('briefed')
  await open(page, '/brakeman?request=br-2')
  const brief = page.locator('[data-brief="br-2"]')
  await expect(brief.locator('[data-brief-title]')).toHaveText('Stub brief for br-2', { timeout: 30_000 })
  await expect(brief.locator('[data-brief-provenance]')).toContainText('request br-2')
  const fact = brief.locator('[data-brief-fact]')
  await expect(fact.locator(`[data-ref-chip="run:${briefed.runId}"] [data-ref-link]`)).toHaveAttribute('href', `/runs/${briefed.runId}`)
  await expect(fact.locator(`[data-ref-chip="session:${briefed.sessionId}"]`)).toBeVisible()
  await expect(brief.locator('[data-brief-interpretation] [data-reads]')).toHaveText('reads fact 1')
})
