import { existsSync, mkdirSync, readFileSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { expect, git, logged, member, test, type Session } from './fixtures'

// Resume (by-hand item 3 of round 4): an ended session whose agent had a
// turn is resumed with its conversation (Claude Code's --resume, Codex's
// resume subcommand), one without a turn is relaunched afresh, a crew
// member keeps its branch and worktree, and a resume whose working
// directory is gone is refused.
test.describe.configure({ mode: 'serial' })

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/

async function typeLine(page: import('@playwright/test').Page, sessionId: string, line: string) {
  await page.goto(`/sessions/${encodeURIComponent(sessionId)}`)
  const screen = page.locator('.terminal-host .xterm-screen').first()
  await expect(screen).toBeVisible()
  await screen.click()
  await page.keyboard.type(line)
  await page.keyboard.press('Enter')
}

function transcript(home: string, id: string): string {
  const p = join(home, '.stub-sessions', `${id}.txt`)
  return existsSync(p) ? readFileSync(p, 'utf8') : ''
}

test('a Claude-shaped session is resumed with its conversation after it ended', async ({ page, api, state }) => {
  const created = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'resume me' })
  const at = created.command?.indexOf('--session-id') ?? -1
  expect(at).toBeGreaterThanOrEqual(0)
  const agentId = created.command?.[at + 1] ?? ''
  expect(agentId).toMatch(UUID)
  await typeLine(page, created.id, 'remember PINEAPPLE')
  await expect.poll(async () => (await api.session(created.id)).attention?.message ?? '', { timeout: 30_000 }).toContain('got: remember PINEAPPLE')
  await expect.poll(async () => (await api.session(created.id)).agentSession?.resumable, { timeout: 15_000 }).toBe(true)
  // The id was chosen at launch (source `set`); the hook's reports carry the same id and make it resumable.
  const as = (await api.session(created.id)).agentSession
  expect(as?.id).toBe(agentId)
  expect(['set', 'hook']).toContain(as?.source)
  expect(transcript(state.home, agentId)).toContain('remember PINEAPPLE')
  await api.stopSession(created.id)
  await expect.poll(async () => (await api.session(created.id)).status, { timeout: 15_000 }).toMatch(/exited|stopped/)
  await page.goto(`/sessions/${encodeURIComponent(created.id)}`)
  // The page says it ended at its foot, with Resume there (UX3 R16), and no Resume in the header.
  const foot = page.locator('[data-session-ended]')
  await expect(foot).toContainText('This session ended.')
  await expect(page.locator('[data-session-bar] [data-resume]')).toHaveCount(0)
  const button = foot.locator('[data-resume][data-resume-kind="label"]')
  await expect(button).toBeVisible()
  await expect(button).toHaveAttribute('aria-label', /^Resume/)
  await button.click()
  await page.waitForURL((u) => u.pathname.startsWith('/sessions/') && !u.pathname.endsWith(created.id), { timeout: 15_000 })
  const nextId = decodeURIComponent(new URL(page.url()).pathname.split('/').pop() ?? '')
  const next = await api.session(nextId)
  const r = next.command?.indexOf('--resume') ?? -1
  expect(r).toBeGreaterThanOrEqual(0)
  expect(next.command?.[r + 1]).toBe(agentId)
  expect(next.command).not.toContain('--session-id')
  expect(next.agentSession).toMatchObject({ id: agentId, source: 'resumed' })
  expect(next.resumedFrom).toBe(created.id)
  // The stub printed the transcript it kept; the conversation goes on in it.
  await typeLine(page, nextId, 'what did I say')
  await expect.poll(() => transcript(state.home, agentId), { timeout: 15_000 }).toContain('what did I say')
  expect(transcript(state.home, agentId)).toContain('remember PINEAPPLE')
  await api.stopSession(nextId)
})

test('a session that had no turn is relaunched afresh', async ({ page, api }) => {
  const created = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'untouched' })
  const agentId = created.command?.[(created.command?.indexOf('--session-id') ?? -1) + 1] ?? ''
  await api.stopSession(created.id)
  await expect.poll(async () => (await api.session(created.id)).status, { timeout: 15_000 }).toMatch(/exited|stopped/)
  await page.goto(`/sessions/${encodeURIComponent(created.id)}`)
  await expect(page.locator('[data-session-ended]')).toContainText('This session ended.')
  await expect(page.locator('[data-session-ended] [data-resume][data-resume-kind="label"]')).toHaveAttribute('aria-label', /^Relaunch/)
  const r = await api.resumeSession(created.id)
  expect(r.status).toBe(201)
  expect(r.body.resumed).toBe(false)
  expect(String(r.body.notice)).toContain('nothing to resume yet')
  const next = r.body.session as Session
  const at = next.command?.indexOf('--session-id') ?? -1
  expect(at).toBeGreaterThanOrEqual(0)
  expect(next.command?.[at + 1]).toMatch(UUID)
  expect(next.command?.[at + 1]).not.toBe(agentId)
  await api.stopSession(next.id)
})

test('a crew member resumed keeps its branch and worktree, and is not prompted again', async ({ api, state }) => {
  const crew = await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
    name: 'e2e resume member',
    goal: 'resume',
    cwd: '',
    where: 'server',
    isolation: 'worktree',
    openAfterLaunch: false,
    members: [{ name: 'solo', agentId: 'claude', prompt: 'first turn', start: { when: 'immediately' } }],
  })
  const run = await api.launchCrew(crew.crew.id)
  try {
    await expect.poll(async () => logged(await api.run(run.id), "typed solo's prompt"), { timeout: 60_000 }).toBe(true)
    let solo = member(await api.run(run.id), 'solo')
    const sessionId = solo.sessionId ?? ''
    await expect.poll(async () => (await api.session(sessionId)).agentSession?.resumable, { timeout: 30_000 }).toBe(true)
    const branch = solo.branch ?? ''
    expect(branch).toBe(`crew/${run.id}/solo`)
    const worktrees = () => git(state.repo, 'worktree', 'list', '--porcelain')
    const before = worktrees()
    await api.stopSession(sessionId)
    await expect.poll(async () => member(await api.run(run.id), 'solo').status, { timeout: 15_000 }).toBe('ended')
    const r = await api.resumeMember(run.id, 'solo')
    expect(r.status, JSON.stringify(r.body)).toBe(201)
    await expect.poll(async () => member(await api.run(run.id), 'solo').status, { timeout: 30_000 }).toBe('running')
    solo = member(await api.run(run.id), 'solo')
    expect(solo.branch).toBe(branch)
    expect(solo.sessionId).not.toBe(sessionId)
    const next = await api.session(solo.sessionId ?? '')
    expect(next.command).toContain('--resume')
    expect(next.agentSession?.source).toBe('resumed')
    expect(worktrees()).toBe(before)
    expect(git(state.repo, 'rev-parse', '--abbrev-ref', 'HEAD', ...[]).length).toBeGreaterThan(0)
    await new Promise((f) => setTimeout(f, 2_000))
    const r2 = await api.run(run.id)
    expect(r2.log.filter((e) => (e.message ?? '').includes("typed solo's prompt")).length, 'the prompt is typed once').toBe(1)
    expect(logged(r2, 'resumed')).toBe(true)
  } finally {
    await api.stopRun(run.id)
    await api.call('DELETE', `/api/crews/${encodeURIComponent(crew.crew.id)}`)
  }
})

test('a Codex-shaped session resumes by its captured thread, and a gone working directory refuses the resume', async ({ page, api, state }) => {
  const dir = join(state.root, 'gone-after')
  mkdirSync(dir, { recursive: true })
  const created = await api.ok<Session>('POST', '/api/sessions', { agentId: 'codex', name: 'codex resume', cwd: dir, yolo: true })
  expect(created.command).not.toContain('--session-id')
  await typeLine(page, created.id, 'hello codex')
  await expect.poll(async () => (await api.session(created.id)).agentSession?.resumable, { timeout: 30_000 }).toBe(true)
  const thread = (await api.session(created.id)).agentSession?.id ?? ''
  expect(thread).toMatch(UUID)
  await api.stopSession(created.id)
  await expect.poll(async () => (await api.session(created.id)).status, { timeout: 15_000 }).toMatch(/exited|stopped/)
  const r = await api.resumeSession(created.id)
  expect(r.status).toBe(201)
  const next = r.body.session as Session
  const at = next.command?.indexOf('resume') ?? -1
  expect(at).toBeGreaterThanOrEqual(0)
  expect(next.command?.slice(at, at + 4)).toEqual(['resume', thread, '-c', 'tui.resume_cwd="session"'])
  await api.stopSession(next.id)
  await expect.poll(async () => (await api.session(next.id)).status, { timeout: 15_000 }).toMatch(/exited|stopped/)
  rmSync(dir, { recursive: true, force: true })
  const refused = await api.resumeSession(next.id)
  expect(refused.status).toBe(400)
  expect(refused.body.error?.code).toBe('invalid_cwd')
})

test('a member of a stopped run resumed reopens the run, and Resume as new run continues every conversation in a new run', async ({ page, api, state }) => {
  const crew = await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
    name: 'e2e resume stopped',
    goal: 'resume',
    cwd: '',
    where: 'server',
    isolation: 'worktree',
    openAfterLaunch: false,
    members: [
      { name: 'lead', agentId: 'claude', prompt: 'first turn', start: { when: 'immediately' } },
      { name: 'later', agentId: 'claude', prompt: 'when asked', start: { when: 'manual' } },
    ],
  })
  const run = await api.launchCrew(crew.crew.id)
  const runs = [run.id]
  try {
    await expect.poll(async () => logged(await api.run(run.id), "typed lead's prompt"), { timeout: 60_000 }).toBe(true)
    const lead = member(await api.run(run.id), 'lead')
    await expect.poll(async () => (await api.session(lead.sessionId ?? '')).agentSession?.resumable, { timeout: 30_000 }).toBe(true)
    await api.stopRun(run.id)
    await expect.poll(async () => (await api.run(run.id)).state, { timeout: 15_000 }).toBe('stopped')
    // In place: the run page offers Resume on the ended member of the stopped run; it reopens the run.
    await page.goto(`/runs/${encodeURIComponent(run.id)}`)
    const resume = page.locator('[data-run-grid] [data-member="lead"] [data-resume]')
    await expect(resume).toBeVisible()
    await resume.click()
    await expect.poll(async () => (await api.run(run.id)).state, { timeout: 30_000 }).toBe('running')
    let r = await api.run(run.id)
    expect(r.stoppedAt).toBeUndefined()
    expect(logged(r, 'reopened: lead resumes')).toBe(true)
    expect(member(r, 'lead').branch).toBe(lead.branch)
    await api.stopRun(run.id)
    await expect.poll(async () => (await api.run(run.id)).state, { timeout: 15_000 }).toBe('stopped')
    // As a new run: the header's Resume as new run continues lead's conversation in its kept worktree.
    const worktrees = git(state.repo, 'worktree', 'list', '--porcelain')
    await page.goto(`/runs/${encodeURIComponent(run.id)}`)
    // A stopped run's header offers Resume and no Stop (#137).
    await expect(page.locator('[data-crew-run-header] [data-run-stop]')).toHaveCount(0)
    await page.locator('[data-crew-run-header] [data-run-resume]').click()
    await page.waitForURL((u) => u.pathname.startsWith('/runs/') && !u.pathname.endsWith(run.id), { timeout: 30_000 })
    const nextId = decodeURIComponent(new URL(page.url()).pathname.split('/').pop() ?? '')
    runs.push(nextId)
    const next = await api.run(nextId)
    expect(next.id).not.toBe(run.id)
    expect((next as unknown as { resumedFrom?: string }).resumedFrom).toBe(run.id)
    // Starting until its session is ready for a prompt (#155), then running.
    await expect.poll(async () => member(await api.run(nextId), 'lead').status, { timeout: 15_000 }).toBe('running')
    expect(member(next, 'lead').branch).toBe(lead.branch)
    expect(member(next, 'later').status).toBe('pending')
    const nextLead = await api.session(member(next, 'lead').sessionId ?? '')
    expect(nextLead.command).toContain('--resume')
    expect(git(state.repo, 'worktree', 'list', '--porcelain')).toBe(worktrees)
    r = await api.run(run.id)
    expect((r as unknown as { resumedBy?: string }).resumedBy).toBe(nextId)
    expect(r.state).toBe('stopped')
    await expect(page.locator('[data-crew-run-header] [data-run-resume]')).toHaveCount(0)
    // The stopped run, resumed already, offers no Resume as new run on its own page either (#126).
    await page.goto(`/runs/${encodeURIComponent(run.id)}`)
    await expect(page.locator('[data-crew-run-header] [data-run-subtitle]')).toBeVisible()
    await expect(page.locator('[data-crew-run-header] [data-run-resume]')).toHaveCount(0)
    // The Crews page lists the new run as running now, and the stopped run no longer offers Resume as new run once resumed.
    await page.goto('/crews')
    await expect(page.locator(`[data-running-now] [data-run="${nextId}"]`)).toHaveAttribute('data-state', 'running')
    await expect(page.locator(`[data-running-now] [data-run="${run.id}"]`)).toHaveCount(0)
    await page.goto(`/crews/${encodeURIComponent(crew.crew.id)}?tab=runs`)
    await expect(page.locator(`[data-crew-runs] [data-run="${run.id}"]`)).toHaveAttribute('data-state', 'stopped')
    await expect(page.locator(`[data-crew-runs] [data-run="${run.id}"] [data-run-resume]`)).toHaveCount(0)
    await expect(page.locator(`[data-crew-runs] [data-run="${nextId}"] [data-run-stop]`)).toBeVisible()
  } finally {
    for (const id of runs) await api.stopRun(id)
    await api.call('DELETE', `/api/crews/${encodeURIComponent(crew.crew.id)}`)
  }
})
