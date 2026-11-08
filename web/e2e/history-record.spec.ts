import { copyFileSync, mkdirSync, mkdtempSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { Page } from '@playwright/test'
import { Api, expect, task, test, type Run, type Session } from './fixtures'
import { freeProjectKey, makeProject, makeTicket, opOf } from './dispatch-helpers'
import { renderConfig, scratchRepo, startServer, stopServer, type Started } from './server'

// The pages a history row opens (Program item HS1, docs/plans/history.md, PR-6), reached by their URLs: no palette row reaches them
// yet. A session's record after a restart, when the server no longer lists the session: Resume asked and answered, the new session
// taking up the agent's own conversation (the recipe's --resume with the recorded id) and its record naming the one it resumed;
// Forget removing the record. The run page at a check's step (its log and the SHA-256 the step log route sends) and at a command's
// receipt (a pause, its actor and time). A ticket's page at a comment the page's first read does not hold, shown marked.
test.describe.configure({ mode: 'serial' })

interface RecordReply {
  record: { id: string; endedAt?: string; lostAt?: string; resumedFrom?: string; agent: { session?: { id: string; resumable: boolean } }; resumes?: Array<{ commandId: string; state: string; session?: string }> }
  live: boolean
}

// The first test's own server, ended after the test that started it (a test run again starts its own, on data of its own).
let server: Started | null = null
let serverLog = ''
test.afterEach(async () => {
  if (server) await stopServer(server.pid)
  server = null
  if (serverLog) {
    mkdirSync('test-results', { recursive: true })
    copyFileSync(serverLog, 'test-results/history-record-server.log')
  }
  serverLog = ''
})

async function typeLine(page: Page, baseURL: string, sessionId: string, line: string) {
  await page.goto(`${baseURL}/sessions/${encodeURIComponent(sessionId)}`)
  const screen = page.locator('.terminal-host .xterm-screen').first()
  await expect(screen).toBeVisible()
  await screen.click()
  await page.keyboard.type(line)
  await page.keyboard.press('Enter')
}

test("a session's record after a restart: Resume takes up its conversation, and Forget removes the record", async ({ state, page }) => {
  test.setTimeout(240_000)
  const here = dirname(fileURLToPath(import.meta.url))
  const root = mkdtempSync(join(state.root, 'history-record-'))
  const home = join(root, 'home')
  const data = join(root, 'data')
  const repo = join(root, 'repo')
  for (const d of [home, data, repo]) mkdirSync(d, { recursive: true })
  scratchRepo(repo)
  const config = join(root, 'conductor.json')
  renderConfig(join(here, 'conductor.e2e.json'), config)
  serverLog = join(root, 'server.log')
  const start = () => startServer({ config, home, data, allowedRoot: root, defaultCwd: repo, log: serverLog })
  server = await start()
  let api = new Api(server)
  await page.addInitScript((t) => localStorage.setItem('conductor.workbenchToken', t), server.token)

  // A Claude-shaped stub session with a turn, so its conversation can be resumed: its own session id set at launch.
  const created = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'history record' })
  const agentId = created.command?.[(created.command?.indexOf('--session-id') ?? -1) + 1] ?? ''
  expect(agentId).not.toBe('')
  await typeLine(page, server.baseURL, created.id, 'remember MANGO')
  await expect.poll(async () => (await api.session(created.id)).agentSession?.resumable, { timeout: 30_000 }).toBe(true)
  await api.stopSession(created.id)
  await expect.poll(async () => (await api.session(created.id)).status, { timeout: 15_000 }).toMatch(/exited|stopped/)
  // Its end observed and written in its record before the server stops, so the record is no lost one.
  await expect.poll(async () => (await api.ok<RecordReply>('GET', `/api/history/sessions/${created.id}`)).record.endedAt ?? '', { timeout: 15_000 }).not.toBe('')

  // The server restarted on its data: the session is no longer listed, its record is.
  await stopServer(server.pid)
  server = null
  server = await start()
  api = new Api(server)
  await page.addInitScript((t) => localStorage.setItem('conductor.workbenchToken', t), server.token)
  expect((await api.call('GET', `/api/sessions/${created.id}`)).status).toBe(404)
  const kept = await api.ok<RecordReply>('GET', `/api/history/sessions/${created.id}`)
  expect([kept.live, kept.record.agent.session?.id, kept.record.agent.session?.resumable]).toEqual([false, agentId, true])

  // The record's page, opened by its URL.
  await page.goto(`${server.baseURL}/history/sessions/${created.id}`)
  const recordPage = page.locator(`[data-record-page][data-record-id="${created.id}"]`)
  await expect(recordPage.locator('[data-record-agent-session]')).toHaveText(agentId)
  await expect(recordPage.locator('[data-record-ending]')).toHaveText(/exited|stopped/)
  await expect(recordPage.locator('[data-record-never-kept]')).toBeVisible()
  await expect(recordPage.locator('[data-record-entry-kind="typed"]')).toHaveCount(0)

  // Resume asks first, then starts a new session that takes up the conversation.
  await page.locator('[data-record-resume]').click()
  await page.locator('[data-record-resume-confirm]').click()
  await page.waitForURL((u) => u.pathname.startsWith('/sessions/') && !u.pathname.endsWith(created.id), { timeout: 30_000 })
  const nextId = decodeURIComponent(new URL(page.url()).pathname.split('/').pop() ?? '')
  const next = await api.session(nextId)
  const r = next.command?.indexOf('--resume') ?? -1
  expect(r).toBeGreaterThanOrEqual(0)
  expect(next.command?.[r + 1]).toBe(agentId)
  expect(next.resumedFrom).toBe(created.id)
  // The new session's record names the one it resumed; the old one keeps the receipt naming it.
  await expect.poll(async () => (await api.ok<RecordReply>('GET', `/api/history/sessions/${nextId}`)).record.resumedFrom ?? '', { timeout: 15_000 }).toBe(created.id)
  const after = await api.ok<RecordReply>('GET', `/api/history/sessions/${created.id}`)
  expect(after.record.resumes?.map((x) => [x.state, x.session])).toEqual([['started', nextId]])
  await api.stopSession(nextId)
  await expect.poll(async () => (await api.session(nextId)).status, { timeout: 15_000 }).toMatch(/exited|stopped/)

  // The record's page shows the receipt; Forget asks first and removes the record.
  await page.goto(`${server.baseURL}/history/sessions/${created.id}`)
  await expect(page.locator('[data-record-resume-receipt][data-record-resume-state="started"]')).toContainText(nextId)
  await page.locator('[data-record-forget]').click()
  await page.locator('[data-record-forget-confirm]').click()
  await expect(page.locator('[data-record-forgotten]')).toBeVisible()
  expect((await api.call('GET', `/api/history/sessions/${created.id}`)).status).toBe(404)
})

const crewBody = {
  name: 'e2e history record',
  goal: 'a step and a command to link to',
  cwd: '',
  where: 'server',
  isolation: 'worktree',
  openAfterLaunch: false,
  yolo: null,
  members: [{ name: 'builder', agentId: 'worker', prompt: '', start: { when: 'task' } }],
  // The check fails until the builder adds stub-fixed.txt, which it does on its second attempt: attempt 1's receipt holds a log.
  tasks: [{ id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.', checks: [{ id: 'fixed', argv: ['git', 'cat-file', '-e', 'HEAD:stub-fixed.txt'] }] }],
}

test("the run page at a check's step shows its log and hash, and at a command its receipt", async ({ page, api }) => {
  test.setTimeout(180_000)
  const crewId = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', crewBody)).crew.id
  const run = await api.launchCrew(crewId)
  const runPath = `/api/runs/${encodeURIComponent(run.id)}`
  // A pause and its unpause, each a command with a receipt of the admin's.
  const pauseId = `hsr-pause-${run.id.slice(-8)}`
  await api.ok('POST', `${runPath}/pause`, { commandId: pauseId })
  await api.ok('POST', `${runPath}/unpause`, { commandId: `hsr-unpause-${run.id.slice(-8)}` })
  let last: Run = run
  await expect
    .poll(async () => {
      last = await api.run(run.id)
      return last.outcome ?? ''
    }, { timeout: 120_000, intervals: [500] })
    .toBe('succeeded')
  expect(task(last, 'build').attempts).toBe(2)
  const steps = (await api.ok<{ steps: Array<{ phase: string; id: string; k: number }> }>('GET', `${runPath}/tasks/build/attempts/1/steps`)).steps
  const check = steps.find((s) => s.phase === 'check' && s.id === 'fixed')
  expect(check).toBeTruthy()
  const res = await fetch(`${api.server.baseURL}${runPath}/tasks/build/attempts/1/steps/fixed/${check!.k}/log`, { headers: { Authorization: `Bearer ${api.server.token}` } })
  expect(res.status).toBe(200)
  const served = res.headers.get('X-Conductor-Log-SHA256') ?? ''
  expect(served).toMatch(/^[0-9a-f]{64}$/)

  // The step's link, as a history row's: the attempt marked, the step's log open with the hash the route sends, the receipt's.
  await page.goto(`/runs/${encodeURIComponent(run.id)}?view=tasks&task=build&attempt=1&step=${encodeURIComponent(`check/fixed/${check!.k}`)}`)
  await expect(page.locator('[data-task-attempt-row="1"][data-attempt-wanted]')).toBeVisible()
  const modal = page.locator('[data-step-log-modal]')
  await expect(modal.locator('[data-step-log-sha]')).toHaveText(served)
  await expect(modal.locator('[data-step-log-sha-match]')).toHaveAttribute('data-step-log-sha-match', 'matches')
  await expect(modal.locator('[data-step-log-text]')).not.toBeEmpty()

  // The command's link: its intent, its actor and its time.
  const receipt = await api.ok<{ command: { intent: string; actor: string; createdAt: string } }>('GET', `/api/history/command?run=${encodeURIComponent(run.id)}&actor=admin&id=${pauseId}`)
  await page.goto(`/runs/${encodeURIComponent(run.id)}?command=${pauseId}&actor=admin`)
  const panel = page.locator(`[data-run-command][data-run-command-id="${pauseId}"]`)
  await expect(panel.locator('[data-run-command-intent]')).toHaveText(receipt.command.intent)
  await expect(panel.locator('[data-run-command-intent]')).toContainText('pause')
  await expect(panel.locator('[data-run-command-actor]')).toHaveText('admin')
  await expect(panel.locator('[data-run-command-at]')).toContainText(receipt.command.createdAt)
})

test("a ticket's page at a comment its first read does not hold shows that comment marked", async ({ page, api }) => {
  test.setTimeout(120_000)
  const key = await freeProjectKey(api, 'HSR')
  await makeProject(api, key)
  const ticket = await makeTicket(api, key, '1', { title: 'Many comments' })
  // 51 comments: the ticket's read carries the last 50, so the first is on no page the ticket's page reads at first.
  for (let i = 1; i <= 51; i++) await api.ok('POST', `/api/dispatch/tickets/${ticket}/comments`, { opId: opOf(key, `c${i}`), byName: 'Priya', body: `Comment number ${i}` })
  const n = ticket.split('-').pop()
  await page.goto(`/dispatch/${key}/${n}?comment=1`)
  const comments = page.locator('[data-ticket-comments]')
  const at = comments.locator('[data-comment-at][data-comment-at-n="1"]')
  await expect(at.locator('[data-comment-at-number]')).toHaveText('#1')
  await expect(at.locator('[data-comment-at-body]')).toContainText('Comment number 1')
  await expect(comments.locator('[data-comment="51"]')).toBeVisible()
  await expect(comments.locator('[data-comment="1"]')).toHaveCount(0)
  // The way to the comments around it: read from it on, and shown in its place.
  await at.locator('[data-comment-at-around]').click()
  await expect(comments.locator('[data-comment="1"] [data-comment-body]')).toContainText('Comment number 1')
})
