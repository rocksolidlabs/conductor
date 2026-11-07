import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { Page } from '@playwright/test'
import { Api, expect, test, type Run } from './fixtures'
import { renderConfig, scratchRepo, startServer, stopServer, type Started } from './server'

// Brakeman's background role on the app's pages (docs/plans/brakeman.md, BK3, PR-15), on a server of its own (as brakeman.spec.ts:
// its turns and records meet no other spec's). Settings turns the role on with its window, cooldown, briefing times and the day's
// minutes. A managed run whose check always fails (tasks.spec.ts's fixture) wakes it: past the window one turn of kind wake runs,
// whose stub asks a question first, listed in Needs you and raising no browser notification (the page's Notification is a stub that
// records what it is asked; a person's session asking after it is the one notified), then submits its brief once the spec opens its
// gate; the role cools, and the Brakeman page shows its state and its log, the wake leading to the batch and its request. Paused
// from the page, the next run's failure joins no batch and starts no turn.
test.describe.configure({ mode: 'serial' })

/** The cap of every wait a step makes without a bound of its own. */
const STEP_MS = 15_000
test.use({ actionTimeout: STEP_MS, navigationTimeout: STEP_MS })

interface Request {
  id: string
  kind: string
  state: string
  sessionId?: string
}

interface RoleRow {
  source: string
  subject?: string
  batch?: string
  request?: string
  held?: string
}

const failCrew = {
  name: 'e2e brakeman role',
  goal: 'fail a check',
  cwd: '',
  where: 'server',
  isolation: 'worktree',
  openAfterLaunch: false,
  yolo: null,
  members: [{ name: 'builder', agentId: 'worker-cli', prompt: '', start: { when: 'task' } }],
  tasks: [{ id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.', checks: [{ id: 'never', argv: ['sh', '-c', 'exit 1'] }] }],
}

const QUESTION = 'May I read the build log?'

let server: Started | null = null
let api: Api
let serverLog = ''
let root = ''
let gate = ''
let crewId = ''
const runs: string[] = []
let wake = ''

test.beforeAll(async () => {
  const here = dirname(fileURLToPath(import.meta.url))
  root = mkdtempSync(join(tmpdir(), 'conductor-e2e-brakeman-role-'))
  const home = join(root, 'home')
  const data = join(root, 'data')
  const repo = join(root, 'repo')
  for (const d of [home, data, repo]) mkdirSync(d, { recursive: true })
  scratchRepo(repo)
  gate = join(root, 'gate')
  const config = join(root, 'conductor.json')
  renderConfig(join(here, 'conductor.e2e.json'), config)
  const cfg = JSON.parse(readFileSync(config, 'utf8')) as { catalog: { agents: Array<{ id: string; env?: Record<string, string> }> } }
  const stub = cfg.catalog.agents.find((a) => a.id === 'brakeman-stub')!
  // A wake turn's stub asks its question first, then holds at its gate until the spec has looked at Needs you.
  stub.env = { ...stub.env, STUB_BRAKEMAN_WAIT: gate, STUB_BRAKEMAN_ASK: QUESTION }
  writeFileSync(config, JSON.stringify(cfg, null, 2) + '\n')
  serverLog = join(root, 'server.log')
  server = await startServer({ config, home, data, allowedRoot: root, defaultCwd: repo, log: serverLog })
  api = new Api(server)
  const { config: c } = await api.ok<{ config: Record<string, unknown> & { rev: number } }>('GET', '/api/brakeman')
  await api.ok('PUT', '/api/brakeman/config', { rev: c.rev, config: { ...c, enabled: true, agentId: 'brakeman-stub' } })
  crewId = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', failCrew)).crew.id
})

test.afterAll(async () => {
  if (server) {
    for (const id of runs) await api.stopRun(id)
    await stopServer(server.pid)
  }
  if (serverLog) {
    mkdirSync('test-results', { recursive: true })
    copyFileSync(serverLog, 'test-results/brakeman-role-server.log')
  }
  if (root) rmSync(root, { recursive: true, force: true })
})

/**
 * The page on this spec's server, with its token, browser notifications on and granted, and a Notification of the spec's that
 * records each it is asked to show (window.__notes).
 */
async function open(page: Page, path: string) {
  await page.addInitScript((t) => {
    localStorage.setItem('conductor.workbenchToken', t)
    localStorage.setItem('conductor.attention.settings', JSON.stringify({ notifications: true, chime: false }))
    const notes: Array<{ title: string; body?: string }> = []
    ;(window as unknown as { __notes: typeof notes }).__notes = notes
    class RecordingNotification {
      static permission = 'granted'
      static requestPermission() {
        return Promise.resolve('granted')
      }
      onclick: (() => void) | null = null
      constructor(title: string, options?: { body?: string }) {
        notes.push({ title, body: options?.body })
      }
      close() {}
    }
    ;(window as unknown as { Notification: unknown }).Notification = RecordingNotification
  }, server!.token)
  await page.goto(`${server!.baseURL}${path}`)
}

const notes = (page: Page) => page.evaluate(() => (window as unknown as { __notes: Array<{ title: string; body?: string }> }).__notes)
const roleLog = () => api.ok<{ role: { state: string }; log: RoleRow[] }>('GET', '/api/brakeman/role?limit=1000')
const requests = () => api.ok<{ requests: Request[] }>('GET', '/api/brakeman/requests').then((r) => r.requests)
const runOf = (id: string) => api.ok<{ run: Run }>('GET', `/api/runs/${encodeURIComponent(id)}`).then((r) => r.run)

test('Settings turns the background role on with its window, cooldown, briefing times and the day\'s minutes', async ({ page }) => {
  await open(page, '/settings')
  const card = page.locator('[data-brakeman-settings]')
  await expect(card.locator('[data-brakeman-role-settings]')).toBeVisible()
  await card.getByRole('switch', { name: 'Wake on events' }).click()
  await card.locator('[data-brakeman-window] input, input[data-brakeman-window]').first().fill('10')
  await card.locator('[data-brakeman-cooldown] input, input[data-brakeman-cooldown]').first().fill('30')
  await card.locator('[data-brakeman-briefings] input, input[data-brakeman-briefings]').first().fill('07:00, 18:30')
  // A time the server would refuse is said before the save.
  await card.locator('[data-brakeman-minutes] input, input[data-brakeman-minutes]').first().fill('5')
  await expect(card).toContainText('Minutes a day are 10 to 1440.')
  await expect(card.locator('[data-brakeman-save]')).toBeDisabled()
  await card.locator('[data-brakeman-minutes] input, input[data-brakeman-minutes]').first().fill('240')
  await card.locator('[data-brakeman-save]').click()
  await expect
    .poll(async () => {
      const c = (await api.ok<{ config: { wake: Record<string, unknown>; budget: { minutesPerDay: number } } }>('GET', '/api/brakeman')).config
      return [c.wake.mode, c.wake.windowSeconds, c.wake.cooldownMinutes, c.wake.briefings, c.budget.minutesPerDay]
    }, { timeout: STEP_MS })
    .toEqual(['background', 10, 30, ['07:00', '18:30'], 240])
  // No briefing time from here on: one that fell due while the spec runs would wake a turn of its own.
  const { config: saved } = await api.ok<{ config: Record<string, unknown> & { rev: number; wake: Record<string, unknown> } }>('GET', '/api/brakeman')
  await api.ok('PUT', '/api/brakeman/config', { rev: saved.rev, config: { ...saved, wake: { ...saved.wake, briefings: [] } } })
  // The Brakeman page shows the role, idle.
  await page.goto(`${server!.baseURL}/brakeman`)
  await expect(page.locator('[data-brakeman-role] [data-role-state]')).toHaveAttribute('data-role-state', 'idle', { timeout: STEP_MS })
})

// The run's failure (its check's three attempts, 120 s), the window (10 s) and the wake request's start (60 s), the question shown in
// Needs you, the person's notification, the brief kept once the gate opens (60 s) and the role cooling, and STEP_MS for each of the
// test's other steps.
const WAKE_CAPS = [120_000, 40_000, 60_000, 30_000, 30_000, 60_000, 30_000]
const WAKE_STEPS = 10

test('a task failing wakes Brakeman once: its question is listed in Needs you and notifies no one, one brief, the role cooling, its log', async ({ page }) => {
  test.setTimeout(WAKE_CAPS.reduce((a, b) => a + b, 0) + WAKE_STEPS * STEP_MS + 60_000)
  await open(page, '/brakeman')
  await expect(page.locator('[data-brakeman-role] [data-role-state]')).toHaveAttribute('data-role-state', 'idle', { timeout: STEP_MS })
  const runId = (await api.launchCrew(crewId)).id
  runs.push(runId)
  await expect.poll(async () => (await runOf(runId)).outcome ?? '', { timeout: WAKE_CAPS[0], intervals: [500] }).toBe('failed')
  // The failure is a wake of the role's log, which the window's end fires as one request of kind wake: read from the log, which keeps it.
  await expect
    .poll(async () => (await roleLog()).log.find((r) => r.source === 'task_failed' && r.subject === `task:${runId}/build`)?.request ?? '', { timeout: WAKE_CAPS[1], intervals: [500] })
    .toMatch(/^br-\d+$/)
  wake = (await roleLog()).log.find((r) => r.source === 'task_failed')!.request!
  await expect.poll(async () => (await requests()).find((r) => r.id === wake)?.state ?? '', { timeout: WAKE_CAPS[2] }).toBe('running')
  // Its question is in Needs you, and the page raised no notification for it.
  await expect(page.locator('[data-sidebar-section="needs"]').first()).toContainText(wake, { timeout: WAKE_CAPS[3] })
  // A person's session asking after it is notified: the notifications are on, and the quiet one was not among them.
  const asker = await api.ok<{ id: string }>('POST', '/api/sessions', { agentId: 'asker' })
  await expect.poll(async () => (await notes(page)).length, { timeout: WAKE_CAPS[4] }).toBe(1)
  const shown = await notes(page)
  expect(shown[0]!.body).toBe('Which database?')
  expect(JSON.stringify(shown)).not.toContain(QUESTION)
  await api.stopSession(asker.id)
  // The gate opens: the stub submits its brief, and the role cools.
  writeFileSync(gate, 'open\n')
  await expect.poll(async () => (await requests()).find((r) => r.id === wake)?.state ?? '', { timeout: WAKE_CAPS[5] }).toBe('briefed')
  await expect(page.locator('[data-brakeman-role] [data-role-state]')).toHaveAttribute('data-role-state', 'cooling', { timeout: WAKE_CAPS[6] })
  await expect(page.locator('[data-brakeman-role] [data-role-words]')).toContainText('cooling until')
  // The log: the wake, its batch and the request it made.
  const row = page.locator(`[data-brakeman-role] [data-role-row="task_failed"][data-role-request="${wake}"]`)
  await expect(row).toBeVisible()
  await expect(row).toContainText('A task failed: task build')
  // One turn of kind wake, whatever else changed in the window (the run's end joined the same batch).
  const wakes = (await requests()).filter((r) => r.kind === 'wake')
  expect(wakes.map((r) => r.id)).toEqual([wake])
  // The brief is listed with the rest, as woken by events.
  await expect(page.locator(`[data-brakeman-briefs] [data-request="${wake}"]`)).toContainText('Woken by events')
})

test('paused from the page, nothing wakes it: the next failure joins no batch and starts no turn', async ({ page }) => {
  test.setTimeout(120_000 + 30_000 + 8 * STEP_MS + 60_000)
  await open(page, '/brakeman')
  const card = page.locator('[data-brakeman-role]')
  await expect(card.locator('[data-role-action="pause"]')).toBeVisible({ timeout: STEP_MS })
  await card.locator('[data-role-action="pause"]').click()
  await expect(card.locator('[data-role-state]')).toHaveAttribute('data-role-state', 'paused')
  await expect(card.locator('[data-role-action="resume"]')).toBeVisible()
  const runId = (await api.launchCrew(crewId)).id
  runs.push(runId)
  await expect.poll(async () => (await runOf(runId)).outcome ?? '', { timeout: 120_000, intervals: [500] }).toBe('failed')
  // The failure is logged as held, and joins no batch.
  await expect
    .poll(async () => (await roleLog()).log.find((r) => r.subject === `task:${runId}/build`)?.held ?? '', { timeout: 30_000, intervals: [500] })
    .toBe('paused')
  const log = await roleLog()
  expect(log.role.state).toBe('paused')
  expect(log.log.filter((r) => r.subject === `task:${runId}/build`).every((r) => !r.batch)).toBe(true)
  await expect(card.locator(`[data-role-row="task_failed"][data-role-held="paused"]`).first()).toContainText('not gathered: the role was paused', { timeout: STEP_MS })
  expect((await requests()).filter((r) => r.kind === 'wake').map((r) => r.id)).toEqual([wake])
  // Resumed, it takes wakes again (nothing is due: the failure while paused stays held).
  await card.locator('[data-role-action="resume"]').click()
  await expect(card.locator('[data-role-state]')).not.toHaveAttribute('data-role-state', 'paused')
})
