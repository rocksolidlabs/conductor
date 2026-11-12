import { randomBytes, randomInt } from 'node:crypto'
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Api, type Session } from './fixtures'
import type { Locator, Page } from '@playwright/test'

// Accounts and the capacity panel (docs/plans/capacity.md, Program item AC1, PR-5). Every account is a folder of this spec's own
// under the test HOME, holding the stub's `stub-usage` line (web/e2e/stub-agent.sh): the state the stub's Claude Code or Codex
// answers its usage read with. No real tool, login or folder of anyone's is read. Each test ends what it made: its sessions, its
// accounts, the defaults it moved and its folders.
// Chromium is told that conductor.test is this computer (a host resolver rule, as launch-plain.spec.ts has it): the workbench opened
// at that name is seen from another machine, and the launch dialog asks where the agent runs. The other tests open 127.0.0.1.
test.use({ launchOptions: { args: ['--no-sandbox', '--host-resolver-rules=MAP conductor.test 127.0.0.1'] } })

/** What one test made, ended after it whatever its outcome. */
const made = { sessions: [] as string[], accounts: [] as string[], folders: [] as string[], defaults: new Set<string>() }

interface AccountsDoc {
  rev: number
  accounts: Array<{ id: string; adapter: string; label: string; folder: string; retired?: boolean }>
  adapters: Array<{ adapter: string; default: string }>
}

const accounts = (api: Api) => api.ok<AccountsDoc>('GET', '/api/accounts')

/** A folder of the test's own under the server's HOME, the stub's usage state written in it. */
function folder(home: string, line: string): string {
  const dir = mkdtempSync(join(home, 'e2e-account-'))
  made.folders.push(dir)
  writeFileSync(join(dir, 'stub-usage'), line + '\n')
  return dir
}

/** Adds an account through the route, as the admin; its id is ended after the test. */
async function addAccount(api: Api, adapter: string, label: string, dir: string): Promise<string> {
  const { rev } = await accounts(api)
  const r = await api.ok<{ account: { id: string } }>('POST', '/api/accounts', { adapter, label, folder: dir, expectedRev: rev })
  made.accounts.push(r.account.id)
  return r.account.id
}

async function setDefault(api: Api, adapter: string, account: string) {
  const { rev } = await accounts(api)
  made.defaults.add(adapter)
  await api.ok('PUT', `/api/accounts/defaults/${adapter}`, { account, expectedRev: rev })
}

/** A label no other test's account has. */
const label = (what: string) => `e2e ${what} ${randomBytes(3).toString('hex')}`

test.afterEach(async ({ api }) => {
  for (const id of made.sessions.splice(0)) await api.stopSession(id)
  for (const adapter of made.defaults) {
    const d = await accounts(api)
    if (d.adapters.find((a) => a.adapter === adapter)?.default !== 'default') await api.ok('PUT', `/api/accounts/defaults/${adapter}`, { account: 'default', expectedRev: d.rev })
  }
  made.defaults.clear()
  // An account is removed once nothing holds it: a stopped session's process may still be ending, so its removal is asked again.
  for (const id of made.accounts.splice(0)) {
    await expect
      .poll(
        async () => {
          const d = await accounts(api)
          if (!d.accounts.some((a) => a.id === id)) return 'gone'
          return String((await api.call('DELETE', `/api/accounts/${id}?expectedRev=${d.rev}`)).status)
        },
        { timeout: 20_000, message: `the account ${id} the test added is removed` },
      )
      .toMatch(/^(gone|204)$/)
  }
  for (const dir of made.folders.splice(0)) rmSync(dir, { recursive: true, force: true })
})

/** The transcript the stub keeps of its agent session, or "" before it is written. */
function transcript(home: string, agentSession: string): string {
  const p = join(home, '.stub-sessions', `${agentSession}.txt`)
  return existsSync(p) ? readFileSync(p, 'utf8') : ''
}

/** The id the launch gave the agent's own session (Claude Code's --session-id). */
function agentSessionOf(s: Session): string {
  const at = s.command?.indexOf('--session-id') ?? -1
  return at >= 0 ? (s.command?.[at + 1] ?? '') : ''
}

/** Opens the launch dialog and picks an agent; the dialog's own radio group is drawn before the pick. */
async function launchDialog(page: Page, agentName: string): Promise<Locator> {
  await page.getByRole('button', { name: 'Launch agent' }).first().click()
  const dialog = page.getByRole('dialog')
  const agent = dialog.getByRole('radiogroup', { name: 'Agent' }).getByRole('radio', { name: agentName })
  await agent.click()
  await expect(agent).toHaveAttribute('aria-checked', 'true')
  return dialog
}

/** Picks an account in the dialog's Account choice, then waits for its menu to be gone. */
async function pickAccount(page: Page, dialog: Locator, name: string) {
  await dialog.locator('[data-account-select]').click()
  await page.getByRole('option', { name, exact: true }).click()
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await expect(dialog.locator('[data-account-select]')).toContainText(name)
}

test('an account added in Settings and made the default launches a session that keeps it after the default moves and it is retired', async ({ page, api, state }) => {
  test.setTimeout(180_000)
  const work = label('work')
  const dir = folder(state.home, 'ok')

  // Settings: add the account, by its tool, its label and its folder.
  await page.goto('/settings')
  const card = page.locator('[data-accounts-card]')
  await expect(card.locator('[data-accounts-adapter="claude"]')).toBeVisible()
  const add = card.locator('[data-account-add]')
  await add.locator('[data-account-add-adapter]').click()
  await page.getByRole('option', { name: 'Claude Code', exact: true }).click()
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await add.locator('[data-account-add-label]').fill(work)
  await expect(add.locator('[data-account-add-label]')).toHaveValue(work)
  const folderInput = add.locator('[data-account-add-folder] input[role="combobox"]')
  await folderInput.fill(dir)
  await folderInput.press('Escape')
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await expect(folderInput).toHaveValue(dir)
  await add.locator('[data-account-add-submit]').click()
  const row = card.locator(`[data-account-label="${work}"]`)
  await expect(row).toBeVisible()
  const id = (await row.getAttribute('data-account-row')) ?? ''
  made.accounts.push(id)
  made.defaults.add('claude')
  await expect(row.locator('[data-account-folder]')).toHaveText(dir)
  // Made the default of Claude Code's agents.
  await row.locator('[data-account-set-default]').click()
  await expect(row.locator('[data-account-is-default]')).toBeVisible()
  expect((await accounts(api)).adapters.find((a) => a.adapter === 'claude')?.default).toBe(id)

  // The launch dialog: the account chosen is the default, and the panel shows what the stub's Claude Code reported of it.
  await page.goto('/')
  const dialog = await launchDialog(page, 'Claude Code (e2e stub)')
  await expect(dialog.locator('[data-account-select]')).toContainText(`${work} (default)`)
  await expect(dialog.locator('[data-launch-model-input]')).toHaveValue('')
  const panel = dialog.locator('[data-capacity-panel]')
  await expect(panel).toHaveAttribute('data-capacity-state', 'ok', { timeout: 30_000 })
  await expect(panel.locator('[data-capacity-window]')).toHaveCount(2)
  await expect(panel.locator('[data-capacity-window="session"]')).toContainText(/^5-hour: 33% used, resets \w{3} \d{1,2} \w{3} \d\d:\d\d, in 2 hours$/)
  await expect(panel.locator('[data-capacity-window="weekly_all"]')).toContainText(/^Weekly, all models: 40% used, resets \w{3} \d{1,2} \w{3} \d\d:\d\d, in 5 days$/)
  await expect(panel.locator('[data-capacity-plan]')).toHaveText('Claude Max')
  await expect(panel.locator('[data-capacity-account]')).toHaveText(work)
  await expect(panel.locator('[data-capacity-model]')).toHaveText("the agent's default")
  await expect(panel.locator('[data-capacity-host]')).toContainText('Runs on this server')
  await expect(panel.locator('[data-capacity-provider]')).toContainText('Extra usage: off')
  await expect(panel.locator('[data-capacity-estimate]')).toContainText("Conductor's estimate: ")
  await expect(panel.locator('[data-capacity-estimate]')).toContainText('running on this account here')
  await expect(panel.locator('[data-capacity-fresh]')).toHaveText(/^Read (just now|\d+ minutes? ago) by Claude Code 2\.1\.322$/)
  // Nothing the tool printed for people, and nothing of its login, reaches the page.
  await expect(panel).not.toContainText('E2E-PEOPLE-TEXT')
  await expect(panel).not.toContainText('e2e-person@example.invalid')
  await expect(dialog.getByRole('button', { name: 'Launch' })).toBeEnabled()

  const created = page.waitForResponse((r) => r.request().method() === 'POST' && new URL(r.url()).pathname === '/api/sessions')
  await dialog.getByRole('button', { name: 'Launch' }).click()
  const answer = await created
  const first = answer.ok() ? ((await answer.json()) as Session) : null
  if (first) made.sessions.push(first.id)
  expect(answer.status(), 'the launch made a session').toBe(201)
  expect(answer.request().postDataJSON()).toMatchObject({ agentId: 'claude', account: id })
  const firstThread = agentSessionOf(first!)
  await expect.poll(() => transcript(state.home, firstThread), { timeout: 15_000 }).toContain(`env: CLAUDE_CONFIG_DIR=${dir}\n`)

  // The default moves to the agent's own login, and the account is retired: neither moves the session.
  await page.goto('/settings')
  const claude = page.locator('[data-accounts-card] [data-accounts-adapter="claude"]')
  await claude.locator('[data-account-row="default"] [data-account-set-default]').click()
  await expect(claude.locator('[data-account-row="default"] [data-account-is-default]')).toBeVisible()
  await claude.locator(`[data-account-row="${id}"] [data-account-retire]`).click()
  await expect(claude.locator(`[data-account-row="${id}"] [data-account-retired]`)).toBeVisible()
  const launched = await api.ok<{ launch: { account?: { id: string; label?: string; source: string } } }>('GET', `/api/sessions/${first!.id}`)
  expect(launched.launch.account).toEqual({ id, label: work, source: 'account' })

  // Its resume keeps the account it was launched with, which only its folder names.
  await api.stopSession(first!.id)
  await expect.poll(async () => (await api.session(first!.id)).status, { timeout: 15_000 }).toMatch(/exited|stopped/)
  const resumed = await api.resumeSession(first!.id)
  if (resumed.body.session) made.sessions.push(resumed.body.session.id)
  expect(resumed.status, JSON.stringify(resumed.body)).toBe(201)
  const next = resumed.body.session!
  expect((await api.ok<{ launch: { account?: { id: string } } }>('GET', `/api/sessions/${next.id}`)).launch.account?.id).toBe(id)
  // Relaunched afresh (the stub had no turn) it is a new thread; resumed, the same thread's transcript notes the folder again.
  const nextThread = agentSessionOf(next) || firstThread
  const envLine = `env: CLAUDE_CONFIG_DIR=${dir}\n`
  const noted = () => transcript(state.home, nextThread).split(envLine).length - 1
  await expect.poll(noted, { timeout: 15_000 }).toBe(nextThread === firstThread ? 2 : 1)

  // A new launch takes the default now, the agent's own login; the retired account is no choice.
  await page.goto('/')
  const again = await launchDialog(page, 'Claude Code (e2e stub)')
  await expect(again.locator('[data-account-select]')).toContainText('Default login (default)')
  await again.locator('[data-account-select]').click()
  await expect(page.getByRole('option', { name: 'Default login (default)', exact: true })).toBeVisible()
  await expect(page.getByRole('option', { name: work })).toHaveCount(0)
  await page.getByRole('option', { name: 'Default login (default)', exact: true }).click()
  await expect(page.getByRole('listbox')).toHaveCount(0)
})

test('each state the stub answers shows its words, and an agent no reader knows says its usage is unknown', async ({ page, api, state }) => {
  test.setTimeout(240_000)
  // A version past the one the reader was checked on, of this run alone: its turn is recorded on the server for good.
  const newer = `2.1.${1000 + randomInt(1_000_000)}`
  const cases = [
    { what: 'signed out', line: 'signed_out', state: 'signed_out', words: 'Signed out: not signed in to Claude Code' },
    { what: 'key paid', line: 'key_paid', state: 'key_paid', words: 'Paid by key: signed in with an API key: no usage windows are reported' },
    { what: 'error', line: 'error', state: 'error', words: "Could not read: Claude Code's result line says the /usage run failed" },
    { what: 'turn', line: `turn ${newer}`, state: 'unqualified', words: `Version not checked: Claude Code ${newer} answered /usage with a model turn: it is not run again` },
  ]
  const ids: Record<string, string> = {}
  const labels: Record<string, string> = {}
  for (const c of cases) {
    labels[c.what] = label(c.what)
    ids[c.what] = await addAccount(api, 'claude', labels[c.what]!, folder(state.home, c.line))
  }

  // The version past the checked one runs only on the admin's explicit check, once: here the check, as `conductor capacity --read
  // --check` makes it, before the dialog opens (the dialog's own read of a key inside its five minutes answers what is kept).
  const check = await api.ok<{ state: string }>('POST', '/api/capacity/read', { agent: 'claude', account: ids.turn, check: true })
  if (check.state === 'reading') {
    await expect.poll(async () => (await api.ok<{ state: string }>('GET', `/api/capacity?agent=claude&account=${ids.turn}`)).state, { timeout: 30_000 }).toBe('unqualified')
  }

  await page.goto('/')
  const dialog = await launchDialog(page, 'Claude Code (e2e stub)')
  const panel = dialog.locator('[data-capacity-panel]')
  for (const c of cases) {
    await pickAccount(page, dialog, labels[c.what]!)
    await expect(panel, c.what).toHaveAttribute('data-capacity-state', c.state, { timeout: 30_000 })
    await expect(panel.locator('[data-capacity-words]'), c.what).toHaveText(c.words)
    // A window the provider did not report is never drawn, as empty or at all.
    await expect(panel.locator('[data-capacity-window]'), c.what).toHaveCount(0)
    await expect(dialog.getByRole('button', { name: 'Launch' })).toBeEnabled()
  }
  // A Claude Code version the reader was not checked on offers the admin's check, in those words.
  await expect(panel.locator('[data-capacity-check]')).toHaveText('Check this version')

  // A read under way says so, and the panel shows its reading when it lands, by the server's event, with no reload.
  const slowDir = folder(state.home, 'slow')
  const slow = label('slow')
  await addAccount(api, 'claude', slow, slowDir)
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)
  const reopened = await launchDialog(page, 'Claude Code (e2e stub)')
  const slowPanel = reopened.locator('[data-capacity-panel]')
  await pickAccount(page, reopened, slow)
  await expect(slowPanel).toHaveAttribute('data-capacity-state', 'reading', { timeout: 30_000 })
  await expect(slowPanel.locator('[data-capacity-fresh]')).toHaveText('Reading…')
  writeFileSync(join(slowDir, 'stub-usage-gate'), 'open\n')
  await expect(slowPanel).toHaveAttribute('data-capacity-state', 'ok', { timeout: 30_000 })
  await expect(slowPanel.locator('[data-capacity-window]')).toHaveCount(2)

  // Codex, read through its app server: its windows by their length, its credits and its flags, the provider's.
  const codex = label('codex')
  await addAccount(api, 'codex', codex, folder(state.home, 'ok'))
  await page.keyboard.press('Escape')
  await expect(reopened).toHaveCount(0)
  const codexDialog = await launchDialog(page, 'Codex CLI (e2e stub)')
  await pickAccount(page, codexDialog, codex)
  const codexPanel = codexDialog.locator('[data-capacity-panel]')
  await expect(codexPanel).toHaveAttribute('data-capacity-state', 'ok', { timeout: 30_000 })
  await expect(codexPanel.locator('[data-capacity-window]')).toHaveCount(2)
  await expect(codexPanel).toContainText(/Weekly: 7% used, resets .*, in 5 days/)
  await expect(codexPanel).toContainText(/5-hour: 33% used, resets .*, in 2 hours/)
  await expect(codexPanel.locator('[data-capacity-provider]')).toContainText('Credits: 1,250')
  await expect(codexPanel.locator('[data-capacity-plan]')).toHaveText('ChatGPT Pro')
  await expect(codexPanel.locator('[data-capacity-fresh]')).toHaveText(/^Read (just now|\d+ minutes? ago) by Codex CLI 0\.163\.0$/)

  // An agent no reader knows: unknown, said so, with no account to choose and nothing read.
  await page.keyboard.press('Escape')
  await expect(codexDialog).toHaveCount(0)
  const plain = await launchDialog(page, 'Plain (e2e stub)')
  await expect(plain.locator('[data-capacity-panel]')).toHaveAttribute('data-capacity-state', 'unsupported', { timeout: 30_000 })
  await expect(plain.locator('[data-capacity-words]')).toHaveText("Usage unknown: Conductor knows no way to read this agent's usage")
  await expect(plain.locator('[data-launch-account]')).toHaveCount(0)
  await expect(plain.getByRole('button', { name: 'Launch' })).toBeEnabled()
})

test.describe('on this computer', () => {
  test('the Account and Model fields are not shown, the panel says its usage is unknown here, and the pasted command names no account', async ({ page, api, state }) => {
    const mine = label('mine')
    const dir = folder(state.home, 'ok')
    const id = await addAccount(api, 'claude', mine, dir)
    await setDefault(api, 'claude', id)
    await page.goto(`http://conductor.test:${state.port}/`)
    const dialog = await launchDialog(page, 'Claude Code (e2e stub)')
    await expect(dialog.locator('[data-launch-account]')).toBeVisible()
    await dialog.getByRole('button', { name: 'On this computer' }).click()
    await expect(dialog.locator('[data-launch-account]')).toHaveCount(0)
    await expect(dialog.locator('[data-launch-model]')).toHaveCount(0)
    const panel = dialog.locator('[data-capacity-panel]')
    await expect(panel).toHaveAttribute('data-capacity-state', 'remote')
    await expect(panel.locator('[data-capacity-host]')).toHaveText('On this computer: your own login there, usage unknown here')
    await dialog.getByLabel('Name', { exact: true }).fill('capacity-host')
    const command = dialog.locator('[data-code-block] li').first()
    await expect(command).toContainText(' --name capacity-host ')
    const text = (await command.textContent()) ?? ''
    expect(text).not.toContain(dir)
    expect(text).not.toContain(mine)
    expect(text).not.toContain(id)
    expect(text).not.toContain('CLAUDE_CONFIG_DIR')
    expect(text).not.toContain('--account')
  })
})
