import { randomBytes, randomInt } from 'node:crypto'
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Api, type Session } from './fixtures'
import type { Locator, Page, Response, Route } from '@playwright/test'

// Accounts and the capacity panel (docs/plans/capacity.md, Program item AC1, PR-5). Every account is a folder of this spec's own
// under the test HOME, holding the stub's `stub-usage` line (web/e2e/stub-agent.sh): the state the stub's Claude Code or Codex
// answers its usage read with. No real tool, login or folder of anyone's is read. Each test ends what it made: its sessions, its
// accounts, the defaults it moved and its folders.
// Chromium is told that conductor.test is this computer (a host resolver rule, as launch-plain.spec.ts has it): the workbench opened
// at that name is seen from another machine, and the launch dialog asks where the agent runs. The other tests open 127.0.0.1.
test.use({ launchOptions: { args: ['--no-sandbox', '--host-resolver-rules=MAP conductor.test 127.0.0.1'] } })

/** What one test made, ended after it whatever its outcome; `gated` are the accounts whose usage read waits for its gate file. */
const made = { sessions: [] as string[], accounts: [] as string[], folders: [] as string[], defaults: new Set<string>(), gated: [] as Array<{ agent: string; id: string; dir: string }> }

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
  // A gated read is a process of the server's that removing its account does not end: its gate is opened, and the server's view
  // of its key is waited on until no read of it is under way, before anything of it goes.
  for (const g of made.gated.splice(0)) {
    writeFileSync(join(g.dir, 'stub-usage-gate'), 'open\n')
    await expect
      .poll(async () => (await api.ok<{ state: string }>('GET', `/api/capacity?agent=${g.agent}&account=${g.id}`)).state, { timeout: 120_000, message: `the read of ${g.id} has ended` })
      .not.toBe('reading')
  }
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
  // Nobody picked an account or typed a model: neither is sent, and the route binds the default (the launch's record below).
  const sent = answer.request().postDataJSON()
  expect(sent).toMatchObject({ agentId: 'claude' })
  expect(sent).not.toHaveProperty('account')
  expect(sent).not.toHaveProperty('model')
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
  made.gated.push({ agent: 'claude', id: await addAccount(api, 'claude', slow, slowDir), dir: slowDir })
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
    // The launch uses the login there: the panel names no account of this server's, and no model.
    await expect(panel).not.toContainText(mine)
    await expect(panel.locator('[data-capacity-account]')).toHaveCount(0)
    await expect(panel.locator('[data-capacity-model]')).toHaveCount(0)
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

/** The dialog's launch on this server. */
const isLaunch = (r: Response) => r.request().method() === 'POST' && new URL(r.url()).pathname === '/api/sessions'

/** Launches what the dialog holds and answers the reply, the session it made ended after the test. */
async function launch(page: Page, dialog: Locator): Promise<{ status: number; sent: Record<string, unknown>; session: Session | null }> {
  const answered = page.waitForResponse(isLaunch)
  await dialog.getByRole('button', { name: 'Launch' }).click()
  const answer = await answered
  const session = answer.status() === 201 ? ((await answer.json()) as Session) : null
  if (session) made.sessions.push(session.id)
  return { status: answer.status(), sent: answer.request().postDataJSON(), session }
}

/** The account and model a session's launch was bound to, as the server keeps them. */
async function boundOf(api: Api, id: string) {
  return (await api.ok<{ launch: { account?: { id: string; label?: string; source: string }; model?: string } }>('GET', `/api/sessions/${id}`)).launch
}

test.describe('the account a launch is sent with', () => {
  test("a picked account and a typed model are sent as picked and typed, the launch keeps them, and a refusal of them is shown in the route's words", async ({ page, api, state }) => {
    const work = label('work')
    const id = await addAccount(api, 'claude', work, folder(state.home, 'ok'))
    await page.goto('/')
    const dialog = await launchDialog(page, 'Claude Code (e2e stub)')
    await pickAccount(page, dialog, work)
    const model = dialog.locator('[data-launch-model-input]')
    await model.fill('claude-e2e-model')
    await expect(model).toHaveValue('claude-e2e-model')
    // The arguments choose a model as well: the route refuses, in its words, and the dialog stays open with the pick as it was.
    const args = dialog.locator('[data-launch-args]')
    await args.fill('--model opus')
    await expect(args).toHaveValue('--model opus')
    const refused = await launch(page, dialog)
    expect(refused.status).toBe(400)
    await expect(dialog.locator('[data-launch-error]')).toHaveText('the arguments choose a model already: set the model in one place (invalid_request)')
    await expect(dialog.locator('[data-account-select]')).toContainText(work)
    await args.fill('')
    await expect(args).toHaveValue('')
    const started = await launch(page, dialog)
    expect(started.status, 'the launch made a session').toBe(201)
    expect(started.sent).toMatchObject({ agentId: 'claude', account: id, model: 'claude-e2e-model' })
    expect(await boundOf(api, started.session!.id)).toMatchObject({ account: { id, source: 'account' }, model: 'claude-e2e-model' })
  })

  test('a launch whose yolo recipe sets a login variable offers no account, names none, and starts on that login while an account is the default', async ({ page, api, state }) => {
    const work = label('work')
    const id = await addAccount(api, 'claude', work, folder(state.home, 'ok'))
    await setDefault(api, 'claude', id)
    await page.goto('/')
    const dialog = await launchDialog(page, 'Yolo login (e2e stub)')
    const yolo = dialog.locator('[data-yolo-switch]')
    // Without yolo, the default account is the one shown, as for any agent of Claude Code.
    await expect(yolo).toHaveAttribute('aria-checked', 'false')
    await expect(dialog.locator('[data-account-select]')).toContainText(`${work} (default)`)
    await yolo.click()
    await expect(yolo).toHaveAttribute('aria-checked', 'true')
    const panel = dialog.locator('[data-capacity-panel]')
    await expect(panel).toHaveAttribute('data-capacity-state', 'env_set')
    await expect(dialog.locator('[data-launch-account]')).toHaveCount(0)
    await expect(panel.locator('[data-capacity-account]')).toHaveCount(0)
    await expect(panel).not.toContainText(work)
    const started = await launch(page, dialog)
    expect(started.status, 'the launch made a session').toBe(201)
    expect(started.sent).not.toHaveProperty('account')
    expect((await boundOf(api, started.session!.id)).account).toMatchObject({ id: 'default', source: 'launch_env' })
  })

  test("an entry that keeps its own login is offered the accounts, and a pick its launch refuses is said in the route's words, still to change", async ({ page, api, state }) => {
    const work = label('work')
    const id = await addAccount(api, 'claude', work, folder(state.home, 'ok'))
    await page.goto('/')
    const dialog = await launchDialog(page, 'Own login (e2e stub)')
    // The server's view of this launch with no account named is the default login, as any agent's is.
    await expect(dialog.locator('[data-account-select]')).toContainText('Default login (default)')
    await pickAccount(page, dialog, work)
    const panel = dialog.locator('[data-capacity-panel]')
    await expect(panel).toHaveAttribute('data-capacity-state', 'env_set')
    await expect(panel.locator('[data-capacity-account]')).toHaveCount(0)
    await expect(panel).not.toContainText(work)
    const refused = await launch(page, dialog)
    expect(refused.status).toBe(400)
    expect(refused.sent).toMatchObject({ account: id })
    await expect(dialog.locator('[data-launch-error]')).toContainText('its default account only (invalid_account)')
    await pickAccount(page, dialog, 'Default login (default)')
    const started = await launch(page, dialog)
    expect(started.status, 'the launch made a session').toBe(201)
    expect(started.sent).toMatchObject({ account: 'default' })
    expect((await boundOf(api, started.session!.id)).account).toMatchObject({ id: 'default', source: 'pinned' })
  })

  test('a launch sent before the server said which account it takes names none, and the route binds the default', async ({ page, api, state }) => {
    const work = label('work')
    const id = await addAccount(api, 'claude', work, folder(state.home, 'ok'))
    await setDefault(api, 'claude', id)
    // The dialog's questions of the capacity view are held until the launch has been sent.
    const held: Route[] = []
    await page.route((url) => url.pathname === '/api/capacity', (route) => void held.push(route))
    await page.goto('/')
    const dialog = await launchDialog(page, 'Claude Code (e2e stub)')
    await expect.poll(() => held.length, { message: "the dialog asked the server's view" }).toBeGreaterThan(0)
    await expect(dialog.locator('[data-account-select]')).toContainText('as the server resolves it')
    const started = await launch(page, dialog)
    expect(started.status, 'the launch made a session').toBe(201)
    expect(started.sent).not.toHaveProperty('account')
    expect((await boundOf(api, started.session!.id)).account).toMatchObject({ id, source: 'account' })
    await page.unroute((url) => url.pathname === '/api/capacity')
    for (const r of held.splice(0)) await r.continue().catch(() => {})
  })
})

test.describe('on a phone', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

  test("Settings' capacity line and an account's folder wrap: nothing is cut, and the page does not scroll sideways", async ({ page, api, state }) => {
    const dir = mkdtempSync(join(state.home, 'e2e-account-a-folder-name-long-enough-to-wrap-on-a-phone-'))
    made.folders.push(dir)
    writeFileSync(join(dir, 'stub-usage'), 'signed_out\n')
    const id = await addAccount(api, 'claude', label('phone'), dir)
    await page.goto('/settings')
    const row = page.locator(`[data-accounts-card] [data-account-row="${id}"]`)
    const line = row.locator('[data-capacity-line]')
    await expect(line).toContainText('Not read yet')
    await expect(row.locator('[data-account-folder]')).toHaveText(dir)
    for (const el of [line, row.locator('[data-account-folder]'), page.locator('[data-accounts-card] [data-accounts-adapter="claude"] [data-account-row="default"] [data-capacity-line]')]) {
      expect(await el.evaluate((e) => ({ cut: e.scrollWidth > e.clientWidth, text: e.textContent }))).toMatchObject({ cut: false })
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true)
  })
})
