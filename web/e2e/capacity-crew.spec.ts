import { randomBytes } from 'node:crypto'
import { existsSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, task, test, type Api, type Run } from './fixtures'
import { freeProjectKey, makeProject, makeTicket, opOf, storedTicket } from './dispatch-helpers'

// Crews, a ticket's launch and Brakeman on their accounts (docs/plans/capacity.md, Program item AC1, PR-7). A managed crew of a
// Claude Code member (the stub's worker, on the default account) and a Codex member (on an account it names, with a model): the
// launch sheet's two lines; the default changed while the run is paused, and the retried attempt still on the first account, read
// from the stub's transcript of that attempt's session; the run page's logins. A ticket's approved revision of that crew: its launch
// dialog shows the revision's accounts, and its run says which member took the default at the launch. Brakeman's account chosen from
// the list. Every account is a folder of this spec's own under the test HOME, holding the stub's `stub-usage` line; each test ends
// what it made: its runs, the Brakeman configuration it changed, the defaults it moved, its accounts, its crews and its folders.

/** What one test made, ended after it whatever its outcome. */
const made = { runs: [] as string[], crews: [] as string[], accounts: [] as string[], folders: [] as string[], defaults: new Set<string>(), brakeman: null as null | Record<string, unknown> }

interface AccountsDoc {
  rev: number
  accounts: Array<{ id: string; adapter: string; label: string; folder: string }>
  adapters: Array<{ adapter: string; default: string }>
}
const accounts = (api: Api) => api.ok<AccountsDoc>('GET', '/api/accounts')
const label = (what: string) => `e2e ${what} ${randomBytes(3).toString('hex')}`

/** A folder of the test's own under the server's HOME, the stub's usage state written in it. */
function folder(home: string): string {
  const dir = mkdtempSync(join(home, 'e2e-crew-account-'))
  made.folders.push(dir)
  writeFileSync(join(dir, 'stub-usage'), 'ok\n')
  return dir
}

async function addAccount(api: Api, adapter: string, name: string, dir: string): Promise<string> {
  const { rev } = await accounts(api)
  const r = await api.ok<{ account: { id: string } }>('POST', '/api/accounts', { adapter, label: name, folder: dir, expectedRev: rev })
  made.accounts.push(r.account.id)
  return r.account.id
}

async function setDefault(api: Api, adapter: string, account: string) {
  const { rev } = await accounts(api)
  made.defaults.add(adapter)
  await api.ok('PUT', `/api/accounts/defaults/${adapter}`, { account, expectedRev: rev })
}

/** The accounts and the managed crew every test of this spec launches or shows: the worker on Claude Code's default, Codex on Team. */
async function crewOnAccounts(api: Api, home: string) {
  const work = label('work')
  const workDir = folder(home)
  const workId = await addAccount(api, 'claude', work, workDir)
  await setDefault(api, 'claude', workId)
  const team = label('team')
  const teamId = await addAccount(api, 'codex', team, folder(home))
  // A task id of this test's own: the stub's transcript of an attempt names it.
  const build = `build-${randomBytes(3).toString('hex')}`
  const { crew } = await api.ok<{ crew: { id: string; name: string } }>('POST', '/api/crews', {
    name: label('accounts crew'),
    goal: 'prove the accounts',
    cwd: '',
    where: 'server',
    isolation: 'worktree',
    openAfterLaunch: false,
    yolo: null,
    members: [
      { name: 'builder', agentId: 'worker', prompt: '', start: { when: 'task' } },
      { name: 'reviewer', agentId: 'codex', prompt: '', start: { when: 'task' }, account: teamId, model: 'gpt-e2e' },
    ],
    tasks: [
      // The check fails until the worker adds stub-fixed.txt, which it does on its second attempt: a retry.
      { id: build, member: 'builder', kind: 'work', instructions: 'Build it.', checks: [{ id: 'fixed', argv: ['git', 'cat-file', '-e', 'HEAD:stub-fixed.txt'] }] },
      { id: 'look', member: 'reviewer', kind: 'work', instructions: 'Look at it.', needs: [build] },
    ],
  })
  made.crews.push(crew.id)
  return { crew, work, workDir, workId, team, teamId, build }
}

/**
 * The stub's transcript of one attempt: the file of the session whose worker read that attempt's task, which its line names (the
 * task's id is this test's own); "" before it is written. The transcripts are the stub's `<id>.txt`, each appended in place.
 */
function attemptTranscript(home: string, taskId: string, n: number): string {
  const dir = join(home, '.stub-sessions')
  if (!existsSync(dir)) return ''
  const line = `task: task ${taskId} attempt ${n}:`
  for (const f of readdirSync(dir).filter((x) => x.endsWith('.txt'))) {
    const text = readFileSync(join(dir, f), 'utf8')
    if (text.includes(line)) return text
  }
  return ''
}

const command = () => ({ commandId: randomBytes(8).toString('hex') })

test.afterEach(async ({ api }) => {
  if (made.brakeman) {
    const { config } = await api.ok<{ config: { rev: number } }>('GET', '/api/brakeman')
    await api.ok('PUT', '/api/brakeman/config', { rev: config.rev, config: { ...made.brakeman, rev: config.rev } })
    made.brakeman = null
  }
  for (const id of made.runs.splice(0)) await api.stopRun(id)
  for (const adapter of made.defaults) {
    const d = await accounts(api)
    if (d.adapters.find((a) => a.adapter === adapter)?.default !== 'default') await api.ok('PUT', `/api/accounts/defaults/${adapter}`, { account: 'default', expectedRev: d.rev })
  }
  made.defaults.clear()
  // An account is removed once nothing holds it: a stopped run's sessions may still be ending, so its removal is asked again.
  for (const id of made.accounts.splice(0)) {
    await expect
      .poll(
        async () => {
          const d = await accounts(api)
          if (!d.accounts.some((a) => a.id === id)) return 'gone'
          return String((await api.call('DELETE', `/api/accounts/${id}?expectedRev=${d.rev}`)).status)
        },
        { timeout: 30_000, message: `the account ${id} the test added is removed` },
      )
      .toMatch(/^(gone|204)$/)
  }
  for (const id of made.crews.splice(0)) await api.call('DELETE', `/api/crews/${encodeURIComponent(id)}`)
  for (const dir of made.folders.splice(0)) rmSync(dir, { recursive: true, force: true })
})

test("a crew's launch sheet shows each member's account, and a retry after the default moved stays on the first account", async ({ page, api, state }) => {
  // The run's two attempts are the stub's work, each bounded by the server's own waits: the run's step, two minutes, on top of the rest.
  test.setTimeout(240_000)
  const { crew, work, workDir, workId, team, teamId, build } = await crewOnAccounts(api, state.home)

  await page.goto(`/crews/${encodeURIComponent(crew.id)}`)
  await expect(page.locator('[data-crew-kind]')).toHaveText('Saved crew', { timeout: 30_000 })
  // The editor gives each member its account and model.
  const builderRow = page.locator('[data-crew-member]').nth(0)
  const reviewerRow = page.locator('[data-crew-member]').nth(1)
  await expect(builderRow.locator('[data-member-account]')).toContainText(`The default at launch (now ${work})`)
  await expect(reviewerRow.locator('[data-member-account]')).toContainText(team)
  await expect(reviewerRow.locator('[data-member-model]')).toHaveValue('gpt-e2e')

  // The launch asks first: a line per member, with its agent, model and account and what the server read of its usage.
  await page.locator('[data-launch]').click()
  const builder = page.locator('[data-crew-launch-line="builder"]')
  const reviewer = page.locator('[data-crew-launch-line="reviewer"]')
  await expect(builder.locator('[data-crew-launch-agent]')).toHaveText('Worker (e2e stub)')
  await expect(builder.locator('[data-crew-launch-model]')).toHaveText("the agent's default")
  await expect(builder.locator('[data-crew-launch-account]')).toHaveText(`the default at launch (now ${work})`)
  await expect(reviewer.locator('[data-crew-launch-agent]')).toHaveText('Codex CLI (e2e stub)')
  await expect(reviewer.locator('[data-crew-launch-model]')).toHaveText('gpt-e2e')
  await expect(reviewer.locator('[data-crew-launch-account]')).toHaveText(team)
  // Each line's usage, read as the sheet opened: the stub's figures for the account the member launches on.
  await expect(builder.locator('[data-capacity-panel]')).toHaveAttribute('data-capacity-state', 'ok', { timeout: 30_000 })
  await expect(builder.locator('[data-capacity-line]')).toContainText('Claude Max · ')
  await expect(builder.locator('[data-capacity-line]')).toContainText('Weekly, all models 40%')
  await expect(reviewer.locator('[data-capacity-panel]')).toHaveAttribute('data-capacity-state', 'ok', { timeout: 30_000 })
  await expect(reviewer.locator('[data-capacity-line]')).toContainText('ChatGPT Pro')

  const launched = page.waitForResponse((r) => r.request().method() === 'POST' && new URL(r.url()).pathname === `/api/crews/${crew.id}/launch`)
  await page.locator('[data-crew-launch-confirm]').click()
  const answer = await launched
  expect(answer.status(), 'the sheet launched the crew').toBe(201)
  const run = ((await answer.json()) as { run: Run }).run
  made.runs.push(run.id)
  await expect(page.locator('[data-crew-launch-sheet]')).toHaveCount(0)
  // Bound at the launch: the default as it was then, and the account and model the reviewer names.
  expect(run.members.map((m) => [m.name, m.account, m.model])).toEqual([
    ['builder', { id: workId, source: 'account' }, undefined],
    ['reviewer', { id: teamId, source: 'account' }, 'gpt-e2e'],
  ])

  // Paused, so no attempt starts past this point; the default then moves to the agent's own login, and the run goes on.
  await api.ok('POST', `/api/runs/${encodeURIComponent(run.id)}/pause`, command())
  expect((await api.run(run.id)).paused).toBe(true)
  // The pause came before the retry: an attempt takes the stub's whole turn after a quiet screen, the pause one request.
  expect(task(await api.run(run.id), build).attempts).toBeLessThanOrEqual(1)
  await setDefault(api, 'claude', 'default')
  await api.ok('POST', `/api/runs/${encodeURIComponent(run.id)}/unpause`, command())

  // The retry: attempt 1 failed its check, attempt 2 ran, both on Work's folder, whatever the default says now.
  let last: Run = run
  await expect
    .poll(async () => {
      last = await api.run(run.id)
      return task(last, build).state
    }, { timeout: 120_000, intervals: [500], message: 'the build task succeeded on its second attempt' })
    .toBe('succeeded')
  const { attempts } = await api.ok<{ attempts: Array<{ n: number; state: string }> }>('GET', `/api/runs/${encodeURIComponent(run.id)}/tasks/${build}`)
  expect(attempts.map((a) => [a.n, a.state])).toEqual([
    [1, 'failed'],
    [2, 'succeeded'],
  ])
  for (const a of attempts) expect(attemptTranscript(state.home, build, a.n), `attempt ${a.n} ran on Work's folder`).toContain(`env: CLAUDE_CONFIG_DIR=${workDir}\n`)

  // The run page names each member's login and model as the launch bound them.
  await page.goto(`/runs/${encodeURIComponent(run.id)}`)
  const logins = page.locator('[data-run-logins]')
  await expect(logins.locator('[data-run-login="builder"] [data-run-login-account]')).toHaveText(work, { timeout: 30_000 })
  await expect(logins.locator('[data-run-login="builder"] [data-run-login-model]')).toHaveText("· the agent's default")
  await expect(logins.locator('[data-run-login="reviewer"] [data-run-login-account]')).toHaveText(team)
  await expect(logins.locator('[data-run-login="reviewer"] [data-run-login-model]')).toHaveText('· gpt-e2e')
  await expect(logins.locator('[data-run-login-at-launch]')).toHaveCount(0)
})

test("a ticket's approved revision shows each member's account before its launch, and its run says which took the default", async ({ page, api, state }) => {
  test.setTimeout(180_000)
  const { crew, work, team } = await crewOnAccounts(api, state.home)
  const key = await freeProjectKey(api, 'CAP')
  await makeProject(api, key)
  const ticketKey = await makeTicket(api, key, '1', { title: 'Launch on the accounts' })
  const { plan } = await api.ok<{ plan: { id: string; rev: number } }>('POST', `/api/dispatch/tickets/${ticketKey}/plans`, { opId: opOf(key, 'r1'), crew: crew.id })
  const ticket = await storedTicket(api, ticketKey)
  await api.ok('POST', `/api/dispatch/plans/${plan.id}/approve`, { opId: opOf(key, 'ap1'), expectedRev: plan.rev, ticketRev: ticket.rev })

  await page.goto(`/dispatch/${key}/1`)
  const featured = page.locator('[data-plan-section] [data-plan-featured]')
  await expect(featured).toHaveAttribute('data-plan-state', 'approved', { timeout: 30_000 })
  await featured.locator('[data-plan-launch]').click()
  const dialog = page.locator('[data-launch-dialog]')
  // The revision's members, read only: what the operator approved, the default said with what it is at this launch.
  await expect(dialog.locator('[data-launch-member="builder"] [data-launch-member-account]')).toHaveText(`the default at launch (now ${work})`)
  await expect(dialog.locator('[data-launch-member="reviewer"] [data-launch-member-account]')).toHaveText(team)
  await expect(dialog.locator('[data-launch-member="reviewer"] [data-launch-member-model]')).toHaveText('gpt-e2e')
  await expect(dialog.locator('[data-launch-member] [data-capacity-panel]')).toHaveCount(2)
  await expect(dialog.locator('[data-launch-member] input, [data-launch-member] [role="combobox"]')).toHaveCount(0)
  await dialog.locator('[data-launch-confirm]').click()
  await expect(dialog).toHaveAttribute('data-launch-state', 'started', { timeout: 120_000 })
  const runId = (await dialog.locator('[data-launch-run-link]').getAttribute('data-launch-run-link')) ?? ''
  made.runs.push(runId)

  await page.goto(`/runs/${encodeURIComponent(runId)}`)
  const logins = page.locator('[data-run-logins]')
  await expect(logins.locator('[data-run-login="builder"] [data-run-login-account]')).toHaveText(work, { timeout: 30_000 })
  await expect(logins.locator('[data-run-login="builder"] [data-run-login-at-launch]')).toHaveText('(the default at the launch)')
  await expect(logins.locator('[data-run-login="reviewer"] [data-run-login-account]')).toHaveText(team)
  await expect(logins.locator('[data-run-login="reviewer"] [data-run-login-at-launch]')).toHaveCount(0)
})

test("Brakeman's account is chosen from the accounts of its agent's tool, saved as that account's folder, with the Capacity switch", async ({ page, api, state }) => {
  const work = label('brakeman')
  const dir = folder(state.home)
  await addAccount(api, 'claude', work, dir)
  made.brakeman = (await api.ok<{ config: Record<string, unknown> }>('GET', '/api/brakeman')).config

  await page.goto('/settings')
  const card = page.locator('[data-brakeman-settings]')
  await expect(card.locator('[data-brakeman-agent]')).toBeVisible({ timeout: 30_000 })
  await card.locator('[data-brakeman-agent]').click()
  await page.getByRole('option', { name: 'Brakeman (e2e stub)' }).click()
  await expect(page.getByRole('listbox')).toHaveCount(0)
  const account = card.locator('[data-brakeman-account-select]')
  await expect(account).toContainText('Default login')
  await account.click()
  await expect(page.getByRole('option', { name: 'Default login', exact: true })).toBeVisible()
  await page.getByRole('option', { name: work, exact: true }).click()
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await expect(account).toContainText(work)
  await expect(card.locator('[data-brakeman-account-unlisted]')).toHaveCount(0)
  // The Capacity switch: what Brakeman may read of the capacity view.
  const capacity = card.getByRole('switch', { name: 'Capacity', exact: true })
  await expect(capacity).toBeVisible()
  const on = (await capacity.getAttribute('aria-checked')) === 'true'
  await capacity.click()
  await expect(capacity).toHaveAttribute('aria-checked', on ? 'false' : 'true')
  await page.locator('[data-save-settings]').click()
  await expect
    .poll(async () => {
      const c = (await api.ok<{ config: { agentId: string; account?: string; visibility: { capacity?: boolean } } }>('GET', '/api/brakeman')).config
      return [c.agentId, c.account, !!c.visibility.capacity]
    }, { timeout: 15_000 })
    .toEqual(['brakeman-stub', dir, !on])

  // A folder no account holds is shown as one: the server binds it unlisted.
  const { config } = await api.ok<{ config: Record<string, unknown> & { rev: number } }>('GET', '/api/brakeman')
  const elsewhere = folder(state.home)
  await api.ok('PUT', '/api/brakeman/config', { rev: config.rev, config: { ...config, account: elsewhere } })
  await page.reload()
  await expect(card.locator('[data-brakeman-account-select]')).toContainText(`${elsewhere} (a folder not in Accounts)`, { timeout: 30_000 })
  await expect(card.locator('[data-brakeman-account-unlisted]')).toHaveText('A folder not in Accounts: add it there to read its usage.')
})
