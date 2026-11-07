import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Session } from './fixtures'

// Clone on the Agents page (#291): a copy of an agent with its whole
// configuration under a name and an ID of its own, here Claude Code's stub
// with a CLAUDE_CONFIG_DIR, as a second account would have. The copy is a
// catalog agent like any other: offered to launch, it runs with its
// environment (the stub notes the directory in its transcript) and finds the
// Conductor skill under that directory.
test.describe.configure({ mode: 'serial' })

const sessions: string[] = []

test.afterAll(async ({ api }) => {
  for (const id of sessions) await api.stopSession(id)
  // The copy leaves the catalog, for the specs after this one.
  await api.call('DELETE', '/api/catalog/claude-copy')
})

function transcript(home: string, s: Session): string {
  const file = join(home, '.stub-sessions', `${s.agentSession?.id ?? ''}.txt`)
  return existsSync(file) ? readFileSync(file, 'utf8') : ''
}

test('Clone fills the form from the agent, and the copy is a catalog agent of its own with its environment', async ({ page, api, state }) => {
  const configDir = join(state.home, 'claude-work')
  await page.goto('/agents')
  await page.locator('[data-agent="claude"] [data-agent-clone]').click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.locator('[data-agent-form="clone"]')).toBeVisible()
  await expect(dialog.getByText('Clone Claude Code (e2e stub)', { exact: true })).toBeVisible()
  await expect(dialog.getByPlaceholder('Aider', { exact: true })).toHaveValue('Claude Code (e2e stub) (copy)')
  const id = dialog.getByPlaceholder('aider', { exact: true })
  await expect(id).toHaveValue('claude-copy')
  await expect(id).toBeEnabled()
  // The source's stored values come along, masked.
  await expect(dialog.getByText('STUB_IDENTITY', { exact: true })).toBeVisible()
  await expect(dialog.getByText('set on the server').first()).toBeVisible()
  await dialog.getByRole('button', { name: 'Add variable' }).first().click()
  await dialog.getByRole('textbox', { name: 'Variable name' }).last().fill('CLAUDE_CONFIG_DIR')
  await dialog.getByLabel("Value; empty passes the server's value through").last().fill(configDir)
  await dialog.getByRole('button', { name: 'Save to catalog' }).click()

  const card = page.locator('[data-agent="claude-copy"]')
  await expect(card).toBeVisible({ timeout: 15_000 })
  await expect(card).toContainText('Claude Code (e2e stub) (copy)')
  const { agents } = await api.ok<{ agents: Array<Record<string, unknown>> }>('GET', '/api/catalog')
  const copy = agents.find((a) => a.id === 'claude-copy')
  expect(copy).toMatchObject({
    name: 'Claude Code (e2e stub) (copy)',
    adapter: 'claude',
    source: 'saved',
    env: { STUB_IDENTITY: '***', STUB_REPORT: '***', CLAUDE_CONFIG_DIR: '***' },
    yolo: { args: ['--dangerously-skip-permissions'] },
    session: { idFrom: 'hook' },
  })
  expect(copy?.replaces).toBeUndefined()
  expect(copy?.trustPrompt).toBeTruthy()
  // The built-in's questions: its two dialogs during the work, its sign-in screen (#685) and its first-run theme, login-method and browser screens (#714).
  expect((copy?.questions as unknown[]).length).toBe(6)
  // The source is as it was.
  const source = agents.find((a) => a.id === 'claude')
  expect(source).toMatchObject({ name: 'Claude Code (e2e stub)', source: 'config', env: { STUB_IDENTITY: '***', STUB_REPORT: '***' } })
})

test('the copy is offered to launch, runs with its environment and finds the skill under its configuration directory', async ({ page, api, state }) => {
  await page.goto('/agents')
  await page.getByRole('button', { name: 'Launch agent' }).first().click()
  const agents = page.getByRole('dialog').getByRole('radiogroup', { name: 'Agent' })
  await expect(agents.getByRole('radio', { name: 'Claude Code (e2e stub) (copy)' })).toBeVisible()
  await page.keyboard.press('Escape')

  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude-copy', name: 'e2e clone' })
  sessions.push(s.id)
  const configDir = join(state.home, 'claude-work')
  await expect.poll(async () => transcript(state.home, await api.session(s.id)), { timeout: 30_000, message: 'the stub noted its configuration directory' }).toContain(`env: CLAUDE_CONFIG_DIR=${configDir}\n`)
  const skill = join(configDir, 'skills', 'conductor', 'SKILL.md')
  expect(existsSync(skill)).toBe(true)
  expect(readFileSync(skill, 'utf8')).toContain('conductor:skill')
})
