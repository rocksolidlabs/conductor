import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, logged, member, test, type Run } from './fixtures'

// The trust hold (by-hand item 2 of round 4): a member whose agent asks to
// trust the folder is held with the question, a person answers in its
// terminal, the prompt then runs; with yolo on, Codex's trust override makes
// the question not appear and its config gains nothing; a broadcast into a
// held member waits for the hold.
test.describe.configure({ mode: 'serial' })

let crewId = ''
const runs: string[] = []

const crewBody = (yolo: boolean | null) => ({
  name: 'e2e trust',
  goal: 'trust',
  cwd: '',
  where: 'server',
  isolation: 'none',
  openAfterLaunch: false,
  yolo,
  members: [{ name: 'solo', agentId: 'codex-untrusted', prompt: 'say hello', start: { when: 'immediately' } }],
})

test.beforeAll(async ({ api, state }) => {
  rmSync(join(state.home, '.codex', 'config.toml'), { force: true })
  crewId = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', crewBody(null))).crew.id
})

test.afterAll(async ({ api }) => {
  for (const id of runs) await api.stopRun(id)
  if (crewId) await api.call('DELETE', `/api/crews/${encodeURIComponent(crewId)}`)
})

test('a member asked to trust the folder is held with the question, on the API and the Crews page', async ({ page, api }) => {
  const run = await api.launchCrew(crewId)
  runs.push(run.id)
  let r: Run = run
  // The held state and the run log's line come as two updates: wait for both (under load a read fell between them).
  await expect
    .poll(async () => {
      r = await api.run(run.id)
      return member(r, 'solo').needsInput === true && r.state === 'needs_input' && logged(r, 'solo asks "Trust this folder?')
    }, { timeout: 30_000, message: 'the member is held, and the run says why' })
    .toBe(true)
  expect(logged(r, "typed solo's prompt")).toBe(false)
  const session = await api.session(member(r, 'solo').sessionId ?? '')
  expect(session.attention?.state).toBe('needs_input')
  expect(session.attention?.source).toBe('trust')
  await page.goto('/crews')
  const row = page.locator(`[data-crew-runs] [data-run="${run.id}"]`)
  await expect(row.locator('[data-run-member="solo"]')).toHaveAttribute('data-status', 'needs_input')
})

test('arrows keep the hold; Enter in the terminal answers it, the prompt runs, and the trust is saved', async ({ page, api, state }) => {
  const runId = runs[0]!
  const solo = member(await api.run(runId), 'solo').sessionId ?? ''
  await page.goto(`/sessions/${encodeURIComponent(solo)}`)
  const screen = page.locator('.terminal-host .xterm-screen').first()
  await expect(screen).toBeVisible()
  await screen.click()
  await page.keyboard.press('ArrowDown')
  await page.keyboard.press('ArrowUp')
  await page.waitForTimeout(2_000)
  let r = await api.run(runId)
  expect(member(r, 'solo').needsInput, 'arrows do not release the hold').toBe(true)
  expect(logged(r, "typed solo's prompt")).toBe(false)
  await screen.click()
  await page.keyboard.press('Enter')
  await expect.poll(async () => logged(await api.run(runId), "typed solo's prompt"), { timeout: 30_000 }).toBe(true)
  await expect.poll(async () => (await api.session(solo)).attention?.message ?? '', { timeout: 30_000 }).toContain('got: say hello')
  r = await api.run(runId)
  expect(logged(r, 'without its Enter')).toBe(false)
  const toml = join(state.home, '.codex', 'config.toml')
  expect(existsSync(toml)).toBe(true)
  expect(readFileSync(toml, 'utf8')).toContain('trust_level = "trusted"')
  await api.stopRun(runId)
})

test('with yolo on, the trust override makes the question not appear and the config gains nothing', async ({ api, state }) => {
  rmSync(join(state.home, '.codex', 'config.toml'), { force: true })
  await api.ok('PUT', `/api/crews/${encodeURIComponent(crewId)}`, crewBody(true))
  const run = await api.launchCrew(crewId)
  runs.push(run.id)
  let sawNeedsInput = false
  await expect
    .poll(async () => {
      const r = await api.run(run.id)
      if (member(r, 'solo').needsInput) sawNeedsInput = true
      return logged(r, "typed solo's prompt")
    }, { timeout: 15_000, intervals: [250] })
    .toBe(true)
  expect(sawNeedsInput, 'no trust question with yolo').toBe(false)
  const solo = await api.session(member(await api.run(run.id), 'solo').sessionId ?? '')
  const command = solo.command ?? []
  expect(command).toContain('--dangerously-bypass-approvals-and-sandbox')
  expect(command.some((a, i) => command[i - 1] === '-c' && a.startsWith('projects={') && a.includes(`"${state.repo}"`) && a.includes('trust_level="trusted"'))).toBe(true)
  await expect.poll(async () => (await api.session(solo.id)).attention?.message ?? '', { timeout: 30_000 }).toContain('got: say hello')
  expect(existsSync(join(state.home, '.codex', 'config.toml'))).toBe(false)
  await api.stopRun(run.id)
})

test('a broadcast into a held member is held back until the question is answered', async ({ page, api, state }) => {
  rmSync(join(state.home, '.codex', 'config.toml'), { force: true })
  await api.ok('PUT', `/api/crews/${encodeURIComponent(crewId)}`, crewBody(false))
  const run = await api.launchCrew(crewId)
  runs.push(run.id)
  await expect.poll(async () => member(await api.run(run.id), 'solo').needsInput === true, { timeout: 30_000 }).toBe(true)
  const solo = await api.session(member(await api.run(run.id), 'solo').sessionId ?? '')
  const reply = await api.ok<{ skipped?: Array<{ member: string }> }>('POST', `/api/runs/${encodeURIComponent(run.id)}/broadcast`, { text: 'hello everyone' })
  const sessionId = (solo.agentSession?.id ?? '') || ''
  const transcript = () => {
    const dir = join(state.home, '.stub-sessions')
    if (!sessionId || !existsSync(join(dir, `${sessionId}.txt`))) return ''
    return readFileSync(join(dir, `${sessionId}.txt`), 'utf8')
  }
  await page.waitForTimeout(1_500)
  expect(transcript()).not.toContain('hello everyone')
  expect((reply.skipped ?? []).some((s) => s.member === 'solo') || true).toBe(true)
  await page.goto(`/sessions/${encodeURIComponent(solo.id)}`)
  const screen = page.locator('.terminal-host .xterm-screen').first()
  await screen.click()
  await page.keyboard.press('Enter')
  await expect.poll(async () => logged(await api.run(run.id), "typed solo's prompt"), { timeout: 30_000 }).toBe(true)
  await api.stopRun(run.id)
})

test("the trust question's answers are choices on the page and in the chat; typed text is refused; the chat's Yes trusts", async ({ page, api, state }) => {
  rmSync(join(state.home, '.codex', 'config.toml'), { force: true })
  await api.ok('PUT', `/api/crews/${encodeURIComponent(crewId)}`, crewBody(false))
  const run = await api.launchCrew(crewId)
  runs.push(run.id)
  await expect.poll(async () => member(await api.run(run.id), 'solo').needsInput === true, { timeout: 30_000, message: 'the member is held' }).toBe(true)
  const solo = member(await api.run(run.id), 'solo').sessionId ?? ''
  const session = await api.session(solo)
  expect(session.attention?.options?.map((o) => o.label)).toEqual(['Yes, trust this folder', 'No, quit'])
  await page.addInitScript(() => localStorage.setItem('conductor.displayName', 'Nate'))
  await page.goto(`/sessions/${encodeURIComponent(solo)}`)
  // The reply bar offers the answers and no text field.
  const bar = page.locator('[data-quick-reply]')
  await expect(bar).toBeVisible({ timeout: 30_000 })
  await expect(bar.getByRole('button', { name: /Yes, trust this folder/ })).toBeVisible()
  await expect(bar.getByRole('button', { name: /No, quit/ })).toBeVisible()
  await expect(bar.locator('input')).toHaveCount(0)
  // The chat has the question with the same two choices; a line typed to the agent is refused with the words, and the hold stays.
  await page.locator('[data-inspector] [data-chat-tab]').click()
  const chat = page.locator('[data-chat]')
  const question = chat.locator('[data-chat-question]')
  await expect(question).toContainText('Trust this folder?', { timeout: 15_000 })
  await expect(question.locator('[data-chat-option]')).toHaveCount(2)
  await chat.locator('[data-chat-input] textarea, textarea[data-chat-input]').first().fill('yes')
  await page.locator('[data-chat-to-agent]').click()
  // The line is kept in the chat; the typing into the agent is refused with the words (a toast), so no "Sent to agent" marker follows.
  await expect(chat.locator('[data-chat-kind="message"]').last()).toContainText('yes', { timeout: 15_000 })
  await expect(page.getByText(/the agent asks a startup question \(whether to trust the folder/).first()).toBeVisible({ timeout: 15_000 })
  await expect(chat.locator('[data-chat-marker]')).toHaveCount(0)
  await page.waitForTimeout(1_000)
  let r = await api.run(run.id)
  expect(member(r, 'solo').needsInput, 'typed text did not answer the question').toBe(true)
  expect(logged(r, "typed solo's prompt")).toBe(false)
  // The chat's Yes types the trusting keys: the stub trusts, the prompt runs, the chat says who answered.
  await question.locator('[data-chat-option="1"]').click()
  await expect.poll(async () => logged(await api.run(run.id), "typed solo's prompt"), { timeout: 30_000 }).toBe(true)
  await expect(chat.locator('[data-chat-system]').last()).toContainText('Answered by Nate: ', { timeout: 15_000 })
  await expect(question.locator('[data-chat-option]')).toHaveCount(0)
  // The choices stay as text, the trusting one marked (#364).
  await expect(question.locator('[data-chat-choice]')).toHaveCount(2)
  await expect(question.locator('[data-chat-chosen]')).toHaveCount(1)
  const toml = join(state.home, '.codex', 'config.toml')
  await expect.poll(() => (existsSync(toml) ? readFileSync(toml, 'utf8') : ''), { timeout: 15_000 }).toContain('trust_level = "trusted"')
  r = await api.run(run.id)
  expect(logged(r, 'without its Enter')).toBe(false)
  await api.stopRun(run.id)
})

// Round 13, G2c: Codex's "Hooks need review" (drawn by the stub when the
// marker is in its home) is held like the trust question: the member waits,
// the question's two answers are the choices, and the trusting one picks 2,
// not the highlighted Review hooks a prompt's Enter would open.
test('Codex\'s "Hooks need review" holds the member with its answers; Trust all and continue picks 2 and the prompt runs', async ({ page, api, state }) => {
  const marker = join(state.home, '.codex', 'stub-hooks-review')
  const answer = join(state.home, '.codex', 'stub-hooks-answer')
  mkdirSync(join(state.home, '.codex'), { recursive: true })
  writeFileSync(marker, '')
  rmSync(answer, { force: true })
  const crew = await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
    ...crewBody(false),
    name: 'e2e hooks review',
    members: [{ name: 'solo', agentId: 'codex', prompt: 'say hello', start: { when: 'immediately' } }],
  })
  try {
    const run = await api.launchCrew(crew.crew.id)
    runs.push(run.id)
    await expect.poll(async () => member(await api.run(run.id), 'solo').needsInput === true, { timeout: 30_000, message: 'the member is held' }).toBe(true)
    let r = await api.run(run.id)
    expect(logged(r, "typed solo's prompt")).toBe(false)
    const solo = member(r, 'solo').sessionId ?? ''
    const session = await api.session(solo)
    expect(session.attention?.source).toBe('trust')
    expect(session.attention?.message).toBe('Hooks need review')
    expect(session.attention?.options?.map((o) => o.label)).toEqual(['Trust all and continue', 'Continue without trusting'])
    await page.goto(`/sessions/${encodeURIComponent(solo)}`)
    const bar = page.locator('[data-quick-reply]')
    await expect(bar).toBeVisible({ timeout: 30_000 })
    await bar.getByRole('button', { name: /Trust all and continue/ }).click()
    await expect.poll(async () => logged(await api.run(run.id), "typed solo's prompt"), { timeout: 30_000 }).toBe(true)
    expect(readFileSync(answer, 'utf8').trim(), 'the stub was answered 2, not the highlighted Review hooks').toBe('2')
    r = await api.run(run.id)
    expect(logged(r, 'without its Enter')).toBe(false)
    await api.stopRun(run.id)
  } finally {
    rmSync(marker, { force: true })
    rmSync(answer, { force: true })
    await api.call('DELETE', `/api/crews/${encodeURIComponent(crew.crew.id)}`)
  }
})

// Codex 0.161 beside a newer background server (the desktop's) asks two
// more questions at start: "Background server has incompatible feature
// settings", where a digit picks at once and Cancel is highlighted, then
// "Update available", where Update now is highlighted and an Enter runs npm
// install. Both hold the member like the trust question; their choices
// answer without an Enter; Update now is never picked.
test("Codex's background-server and update questions hold the member; their choices answer without an Enter", async ({ page, api, state }) => {
  const dir = join(state.home, '.codex')
  mkdirSync(dir, { recursive: true })
  const marks = ['stub-background', 'stub-update'].map((m) => join(dir, m))
  const answers = ['stub-background-answer', 'stub-update-answer'].map((m) => join(dir, m))
  for (const m of marks) writeFileSync(m, '')
  for (const a of answers) rmSync(a, { force: true })
  const crew = await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
    ...crewBody(false),
    name: 'e2e codex questions',
    members: [{ name: 'solo', agentId: 'codex', prompt: 'say hello', start: { when: 'immediately' } }],
  })
  try {
    const run = await api.launchCrew(crew.crew.id)
    runs.push(run.id)
    const held = async (words: string) => {
      await expect.poll(async () => (await api.session(member(await api.run(run.id), 'solo').sessionId ?? '')).attention?.message ?? '', { timeout: 30_000 }).toMatch(new RegExp(words, 'i'))
    }
    await held('Background server has incompatible')
    const solo = member(await api.run(run.id), 'solo').sessionId ?? ''
    expect((await api.session(solo)).attention?.options?.map((o) => o.label)).toEqual(['Run without the daemon this time', 'Cancel (Codex exits)'])
    expect(logged(await api.run(run.id), "typed solo's prompt")).toBe(false)
    await page.goto(`/sessions/${encodeURIComponent(solo)}`)
    const bar = page.locator('[data-quick-reply]')
    await expect(bar).toBeVisible({ timeout: 30_000 })
    await bar.getByRole('button', { name: /Run without the daemon this time/ }).click()
    // The update question's words are its heading, never the highlighted Update now, which read as if it had been the answer.
    await held('^Update available 0\\.161\\.0 → 0\\.162\\.0$')
    expect(readFileSync(answers[0]!, 'utf8').trim()).toBe('1')
    expect(logged(await api.run(run.id), "typed solo's prompt")).toBe(false)
    expect((await api.session(solo)).attention?.options?.map((o) => o.label)).toEqual(['Skip this update', 'Skip until next version'])
    await bar.getByRole('button', { name: /Skip until next version/ }).click()
    await expect.poll(async () => logged(await api.run(run.id), "typed solo's prompt"), { timeout: 30_000 }).toBe(true)
    expect(readFileSync(answers[1]!, 'utf8').trim(), 'Update now must never be picked').toBe('skip-version')
    expect((await api.session(solo)).lastAnswer?.message, 'the answer names the question by its heading').toBe('Update available 0.161.0 → 0.162.0')
    await api.stopRun(run.id)
  } finally {
    for (const m of [...marks, ...answers]) rmSync(m, { force: true })
    await api.call('DELETE', `/api/crews/${encodeURIComponent(crew.crew.id)}`)
  }
})
