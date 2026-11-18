import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Browser, Page } from '@playwright/test'
import { budget, expect, member, test, type Api } from './fixtures'

// A member's question answered from the run's chat (#1009). In a run with tasks each member works an attempt, whose session is
// launched under the attempt's own label; Codex's update dialog, read off each member's screen, is in the run's chat on the
// member's name with its choices. A choice pressed in the run page's chat answers it as the same choice on the member's session
// page does; on a run link, a view link's holder gets the choices as text and a control link's holder answers the other member's.
test.describe.configure({ mode: 'serial' })

const crews: string[] = []
const runs: string[] = []

test.afterAll(async ({ api, state }) => {
  for (const id of runs) await api.stopRun(id)
  for (const id of crews) await api.call('DELETE', `/api/crews/${encodeURIComponent(id)}`)
  for (const m of ['stub-update', 'stub-update-answer']) rmSync(join(state.home, '.codex', m), { force: true })
})

/** A wait's cap, the config's expect bound: every wait and action of the test is bounded by it unless it names its own. */
const STEP = 15_000
/** A wait of the cap `ms`, added to the test's timeout as the step takes it (AGENTS.md, Tests that do not flake). */
function cap(ms: number): number {
  budget(ms)
  return ms
}

/** A guest on a run link of the role, in a browser with no workbench token: the run's chat opened. */
async function guest(browser: Browser, api: Api, runId: string, role: 'view' | 'control', name: string): Promise<Page> {
  const { token } = await api.ok<{ token: string }>('POST', `/api/runs/${runId}/links`, { role, ttlSeconds: 3600 })
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  await ctx.addInitScript((n) => localStorage.setItem('conductor.displayName', n), name)
  const page = await ctx.newPage()
  await page.goto(`/join/${token}`, { timeout: cap(STEP) })
  await page.getByRole('button', { name: 'Join crew' }).click({ timeout: cap(STEP) })
  await expect(page.locator('[data-join-tiles] [data-member]')).toHaveCount(2, { timeout: cap(30_000) })
  await expect(page.locator('[data-run-chat-button]:not([data-run-chat-offline])')).toBeVisible({ timeout: cap(STEP) })
  await page.locator('[data-run-chat-button]').click({ timeout: cap(STEP) })
  return page
}

test("a member's question in a run with tasks is answered from the run's chat: the run page, a control link; a view link gets the choices as text", async ({ page, api, state, browser }) => {
  // Every wait below adds its own cap to the test's timeout as it is taken (cap): the config's 120 s is left for the requests and
  // the setup between them.
  const dir = join(state.home, '.codex')
  mkdirSync(dir, { recursive: true })
  for (const m of ['stub-hooks-review', 'stub-background']) rmSync(join(dir, m), { force: true })
  const answerFile = join(dir, 'stub-update-answer')
  const answered = () => (existsSync(answerFile) ? readFileSync(answerFile, 'utf8').trim() : '')
  writeFileSync(join(dir, 'stub-update'), '')
  rmSync(answerFile, { force: true })
  const crew = await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
    name: `e2e run chat answer ${Date.now().toString(36)}`,
    goal: 'answer from the run chat',
    cwd: '',
    where: 'server',
    isolation: 'worktree',
    openAfterLaunch: false,
    yolo: null,
    members: [
      { name: 'cand-a', agentId: 'codex', prompt: '', start: { when: 'task' } },
      { name: 'cand-b', agentId: 'codex', prompt: '', start: { when: 'task' } },
    ],
    tasks: [
      { id: 'approach-a', member: 'cand-a', kind: 'work', instructions: 'Try it.' },
      { id: 'approach-b', member: 'cand-b', kind: 'work', instructions: 'Try it.' },
    ],
  })
  crews.push(crew.crew.id)
  const run = await api.launchCrew(crew.crew.id)
  runs.push(run.id)
  const asked = async (name: string) => {
    const id = member(await api.run(run.id), name).sessionId ?? ''
    return id ? ((await api.session(id)).attention ?? { state: '' }) : { state: '' }
  }
  await expect.poll(async () => `${(await asked('cand-a')).message ?? ''} | ${(await asked('cand-b')).message ?? ''}`, { timeout: cap(45_000), message: 'both attempts ask whether to update' }).toMatch(/^Update available.* \| Update available/)
  expect(answered(), 'no choice taken yet').toBe('')
  // Each question's line in the run's chat is the question's own id.
  const idA = (await asked('cand-a')).id ?? ''
  const idB = (await asked('cand-b')).id ?? ''
  expect(idA && idB && idA !== idB, 'each question has an id of its own').toBeTruthy()

  // The run page: cand-a's question, on cand-a, answered by its first choice (Escape: skip).
  await page.addInitScript(() => localStorage.setItem('conductor.displayName', 'Nate'))
  await page.goto(`/runs/${run.id}`, { timeout: cap(STEP) })
  await expect(page.locator('[data-run-chat-button]:not([data-run-chat-offline])')).toBeVisible({ timeout: cap(STEP) })
  await page.locator('[data-run-chat-button]').click({ timeout: cap(STEP) })
  const chat = page.locator('[data-run-chat]')
  const onA = chat.locator(`[data-chat-question][data-chat-message="${idA}"]`)
  await expect(onA).toContainText('Update available', { timeout: cap(STEP) })
  await onA.locator('[data-chat-option]').filter({ hasText: 'Skip this update' }).click({ timeout: cap(STEP) })
  await expect.poll(answered, { timeout: cap(STEP), message: "cand-a's stub skipped" }).toBe('skip')
  await expect.poll(async () => (await asked('cand-a')).state, { timeout: cap(STEP), message: "cand-a's question goes" }).not.toBe('needs_input')
  await expect(onA.locator('[data-chat-chosen]')).toHaveText('Skip this update', { timeout: cap(STEP) })
  await expect(onA.locator('[data-chat-option]')).toHaveCount(0, { timeout: cap(STEP) })
  // The line is on the member as the run names it, not on its attempt's label.
  await expect(onA.locator('[data-chat-on]')).toHaveAttribute('data-chat-on', 'cand-a', { timeout: cap(STEP) })
  expect((await asked('cand-b')).state, "cand-b's question is still its own").toBe('needs_input')

  // A view link's holder: cand-b's question with its choices as text, no button.
  const jane = await guest(browser, api, run.id, 'view', 'Jane')
  const janeOnB = jane.locator(`[data-run-chat] [data-chat-question][data-chat-message="${idB}"]`)
  await expect(janeOnB.locator('[data-chat-choice]')).toHaveCount(2, { timeout: cap(STEP) })
  await expect(janeOnB.locator('[data-chat-option]')).toHaveCount(0, { timeout: cap(STEP) })

  // A control link's holder: cand-b's question answered by its second choice.
  const priya = await guest(browser, api, run.id, 'control', 'Priya')
  const priyaOnB = priya.locator(`[data-run-chat] [data-chat-question][data-chat-message="${idB}"]`)
  await priyaOnB.locator('[data-chat-option]').filter({ hasText: 'Skip until next version' }).click({ timeout: cap(STEP) })
  await expect.poll(answered, { timeout: cap(STEP), message: "cand-b's stub skipped until the next version" }).toBe('skip-version')
  await expect.poll(async () => (await asked('cand-b')).state, { timeout: cap(STEP), message: "cand-b's question goes" }).not.toBe('needs_input')
  await expect(janeOnB.locator('[data-chat-chosen]')).toHaveText('Skip until next version', { timeout: cap(STEP) })
  await expect(chat.locator('[data-chat-system]').filter({ hasText: 'Answered by Priya: Skip until next version' })).toHaveCount(1, { timeout: cap(STEP) })
  await jane.context().close()
  await priya.context().close()
})
