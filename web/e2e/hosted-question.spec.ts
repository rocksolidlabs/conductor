import { spawn, type ChildProcess } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { existsSync, openSync, readFileSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { endHosted, expect, hostedName, hostedSession, test } from './fixtures'
import { serverBinary, stubBash, stubPath } from './server'

// A hosted session watches what a server session of the same agent watches
// (#293): a `conductor host --agent codex` is handed Codex's recipe by the
// server as it registers, so the stub's command approval, drawn as Codex
// 0.161 draws it, is a question with its three choices in the sidebar's row,
// and the choice clicked there types its key alone, which the stub reads.
let host: ChildProcess | null = null
let hostName = ''

test.afterAll(async ({ api }) => {
  await endHosted(api, hostName, host)
})

test("a hosted Codex's command approval is in the sidebar's row with its choices, answered from the page", async ({ page, api, state }) => {
  // No startup question in the way: those are the trust spec's. The session runs in the home, as the other hosted specs' do: a
  // folder more in the root would push the root's own repo out of the folder picker's 50 entries (settings.spec.ts).
  for (const m of ['stub-hooks-review', 'stub-background', 'stub-update']) rmSync(join(state.home, '.codex', m), { force: true })
  const run = randomUUID()
  const logFile = join(state.root, `hosted-question-${run}.log`)
  const log = openSync(logFile, 'a')
  hostName = hostedName('hosted-question')
  const agentSession = randomUUID()
  const bash = stubBash()
  host = spawn(serverBinary(), ['host', '--server', state.baseURL, '--token', state.hostToken, '--host-name', 'e2e-laptop', '--name', hostName, '--agent', 'codex', '--cwd', state.home, '--relay-only', '--no-local', '--', bash.command, stubPath, '--session-id', agentSession], {
    env: { PATH: bash.path, LANG: 'C.UTF-8', HOME: state.home, CONDUCTOR_BIN: serverBinary(), STUB_IDENTITY: 'codex', STUB_REPORT: 'codex' },
    stdio: ['ignore', log, log],
  })
  const id = (await hostedSession(api, logFile, hostName)).id
  const transcript = () => {
    const file = join(state.home, '.stub-sessions', `${agentSession}.txt`)
    return existsSync(file) ? readFileSync(file, 'utf8') : ''
  }

  await page.goto(`/sessions/${encodeURIComponent(id)}`)
  const screen = page.locator('.terminal-host .xterm-screen').first()
  await expect(screen).toBeVisible({ timeout: 30_000 })
  await screen.click()
  await page.keyboard.type('stub approve curl -sI https://example.com')
  await page.keyboard.press('Enter')
  await expect.poll(async () => (await api.session(id)).attention?.source ?? '', { timeout: 30_000, message: 'the approval is asked' }).toBe('question')
  const asked = (await api.session(id)).attention
  expect(asked?.message).toBe('Would you like to run the following command? $ curl -sI https://example.com')
  expect(asked?.options?.map((o) => o.label)).toEqual(['Yes, proceed', "Yes, and don't ask again", 'No, tell Codex what to do differently'])

  // The sidebar's row: the question and its three choices; the first answers it.
  const row = page.locator(`[data-session-list="sidebar"] [data-sidebar-row="s:${id}"]`).first()
  const choices = row.locator('[data-row-choice]')
  await expect(choices).toHaveCount(3)
  await expect(choices.nth(0)).toContainText('Yes, proceed')
  await expect(choices.nth(1)).toContainText("Yes, and don't ask again")
  await expect(choices.nth(2)).toContainText('No, tell Codex what to do differently')
  await choices.nth(0).click()
  await expect.poll(transcript, { timeout: 15_000, message: 'the stub read the choice' }).toContain('approval: approved\n')
  await expect.poll(async () => (await api.session(id)).attention?.source ?? '', { timeout: 15_000 }).not.toBe('question')
  await expect(choices).toHaveCount(0)
})
