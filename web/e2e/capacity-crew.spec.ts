import { randomBytes } from 'node:crypto'
import { existsSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Page, Route } from '@playwright/test'
import { expect, readAgain, task, test, type Api, type Run } from './fixtures'
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
  const teamDir = folder(home)
  const teamId = await addAccount(api, 'codex', team, teamDir)
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
  return { crew, work, workDir, workId, team, teamDir, teamId, build }
}

/** What the panel warns at a window near its limit, and at a limit the provider reports (capacity.ts). */
const WARN = 'The agent may stop before the reset: a window is near its limit. Launching is still yours to decide.'
const LIMIT = 'The agent may stop: the provider says a limit is reached. Launching is still yours to decide.'
/** The one sentence for a member that names no model, on every screen (modelWords). */
const NO_MODEL = 'no model named: the agent chooses, or its arguments do'
/** The warning fixture's weekly window in a compact panel's details: its share, and its reset five days and an hour off. */
const WEEKLY_RESET = /Weekly, all models: 92% used, resets \w{3} \d{1,2} \w{3} \d{2}:\d{2}, in 5 days/

/** The config's bound of an expect, and the bound of a wait on a read of a crew or a view. */
const EXPECT = 15_000
const READ = 30_000
/** Raises the running test's timeout by the caps of the waits a step is about to make (palette-rows.spec.ts's budget). */
function budget(...caps: number[]) {
  const info = test.info()
  info.setTimeout(info.timeout + caps.reduce((a, b) => a + b, 0))
}

/** The app's router, through the page itself. */
const navigate = (page: Page, path: string) =>
  page.evaluate((u) => (document.querySelector('#__nuxt') as unknown as { __vue_app__: { config: { globalProperties: { $router: { push: (u: string) => Promise<unknown> } } } } }).__vue_app__.config.globalProperties.$router.push(u), path)

/** Asks for a crew's launch from the app-wide palette, on a page that has drawn its layout: the crew's row, then `before`, then Enter. */
async function paletteLaunch(page: Page, crew: { id: string; name: string }, before: () => Promise<void> = async () => {}) {
  await page.keyboard.press('Control+k')
  const palette = page.locator('[data-palette]')
  await palette.locator('input[data-palette-input]').fill(`# ${crew.name}`)
  const row = palette.locator(`[data-palette-row="crew:${crew.id}"]`)
  await expect(row).toBeVisible({ timeout: 15_000 })
  await page.keyboard.press('ArrowDown')
  await expect(row).toHaveAttribute('data-palette-focused', '')
  await before()
  await page.keyboard.press('Enter')
}

/** A crew of this test's own, saved through the API: members that start when pressed, so a launch starts none. */
async function saveCrew(api: Api, name: string, members: Array<Record<string, unknown>>, yolo: boolean | null = false) {
  const { crew } = await api.ok<{ crew: { id: string } }>('POST', '/api/crews', { name, goal: '', cwd: '', where: 'server', isolation: 'none', openAfterLaunch: false, yolo, members: members.map((m) => ({ prompt: '', start: { when: 'manual' }, ...m })) })
  made.crews.push(crew.id)
  return { id: crew.id, name }
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

// A route that lets every request through, kept from before the page loads: the tests' own routes go on top, so interception is
// never switched on or off while the page has requests out (a GET asked as `page.unroute` ended went unanswered for 30 s, #281).
test.beforeEach(async ({ page }) => {
  await page.route('**/*', (route) => route.fallback())
})

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

/** The cap of the stub's hold on the builder's first attempt, in seconds: generous for the pause and the default's change it waits on. */
const HOLD_CAP_S = 90

test("a crew's launch sheet shows each member's account, and a retry after the default moved stays on the first account", async ({ page, api, state }) => {
  // The run's two attempts are the stub's work, each bounded by the server's own waits: the run's step, two minutes, after the hold's
  // cap, on top of the rest.
  test.setTimeout(240_000 + HOLD_CAP_S * 1000)
  const { crew, work, workDir, workId, team, teamDir, teamId, build } = await crewOnAccounts(api, state.home)
  // The builder's first attempt waits at the stub's gate in Work's folder until the pause and the default's change are done, at most
  // the test's own cap; the poll after the gate outlasts the cap, so a gate that never opens fails in the cap's words.
  writeFileSync(join(workDir, 'stub-attempt-hold'), `${HOLD_CAP_S}\n`)
  // Team's usage with the provider's rate limit reached, its windows under 90%.
  writeFileSync(join(teamDir, 'stub-usage'), 'limit\n')

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
  await expect(builder.locator('[data-crew-launch-model]')).toHaveText(NO_MODEL)
  await expect(builder.locator('[data-crew-launch-account]')).toHaveText(`the default at the launch (now ${work})`)
  await expect(reviewer.locator('[data-crew-launch-agent]')).toHaveText('Codex CLI (e2e stub)')
  await expect(reviewer.locator('[data-crew-launch-model]')).toHaveText('gpt-e2e')
  await expect(reviewer.locator('[data-crew-launch-account]')).toHaveText(team)
  // Each line's usage, read as the sheet opened: the stub's figures for the account the member launches on.
  await expect(builder.locator('[data-capacity-panel]')).toHaveAttribute('data-capacity-state', 'ok', { timeout: 30_000 })
  await expect(builder.locator('[data-capacity-line]')).toContainText('Claude Max · ')
  await expect(builder.locator('[data-capacity-line]')).toContainText('Weekly, all models 40%')
  await expect(reviewer.locator('[data-capacity-panel]')).toHaveAttribute('data-capacity-state', 'ok', { timeout: 30_000 })
  await expect(reviewer.locator('[data-capacity-line]')).toContainText('ChatGPT Pro')
  // At the provider's limit, its windows under 90%: the warning is said, in brick, and Launch stays the person's.
  await expect(reviewer.locator('[data-capacity-warning]')).toHaveText(LIMIT)
  await expect(reviewer.locator('[data-capacity-warning]')).toHaveAttribute('data-capacity-warning-color', 'error')

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
  // The pause came before the retry: attempt 1, if it started, is held at the gate, so no second attempt can have begun.
  expect(task(await api.run(run.id), build).attempts).toBeLessThanOrEqual(1)
  await setDefault(api, 'claude', 'default')
  await api.ok('POST', `/api/runs/${encodeURIComponent(run.id)}/unpause`, command())
  writeFileSync(join(workDir, 'stub-attempt-gate'), '')

  // The retry: attempt 1 failed its check, attempt 2 ran, both on Work's folder, whatever the default says now.
  let last: Run = run
  await expect
    .poll(async () => {
      last = await api.run(run.id)
      return task(last, build).state
    }, { timeout: HOLD_CAP_S * 1000 + 120_000, intervals: [500], message: 'the build task succeeded on its second attempt' })
    .toBe('succeeded')
  const { attempts } = await api.ok<{ attempts: Array<{ n: number; state: string }> }>('GET', `/api/runs/${encodeURIComponent(run.id)}/tasks/${build}`)
  expect(attempts.map((a) => [a.n, a.state])).toEqual([
    [1, 'failed'],
    [2, 'succeeded'],
  ])
  for (const a of attempts) expect(attemptTranscript(state.home, build, a.n), `attempt ${a.n} ran on Work's folder`).toContain(`env: CLAUDE_CONFIG_DIR=${workDir}\n`)
  expect(attemptTranscript(state.home, build, 1), 'attempt 1 went on when the gate opened, not when its cap ran out').toContain('hold: attempt 1 went on when the gate opened\n')

  // The run page names each member's login and model as the launch bound them.
  await page.goto(`/runs/${encodeURIComponent(run.id)}`)
  await page.locator('[data-crew-run-header] [data-run-logins-button]').click({ timeout: 30_000 })
  const logins = page.locator('[data-run-logins]')
  await expect(logins.locator('[data-run-login="builder"] [data-run-login-account]')).toHaveText(work)
  await expect(logins.locator('[data-run-login="builder"] [data-run-login-model]')).toHaveText(`· ${NO_MODEL}`)
  await expect(logins.locator('[data-run-login="reviewer"] [data-run-login-account]')).toHaveText(team)
  await expect(logins.locator('[data-run-login="reviewer"] [data-run-login-model]')).toHaveText('· gpt-e2e')
  await expect(logins.locator('[data-run-login-at-launch]')).toHaveCount(0)
})

test("a ticket's approved revision shows each member's account before its launch, and its run says which took the default", async ({ page, api, state }) => {
  test.setTimeout(180_000)
  const { crew, work, workDir, workId, team, teamDir, teamId } = await crewOnAccounts(api, state.home)
  // Work's usage at the provider's warning, Team's at its rate limit, each read before the dialog opens (it asks for no read).
  writeFileSync(join(workDir, 'stub-usage'), 'warning\n')
  writeFileSync(join(teamDir, 'stub-usage'), 'limit\n')
  for (const [agent, account] of [
    ['worker', workId],
    ['codex', teamId],
  ] as const) {
    await api.ok('POST', '/api/capacity/read', { agent, account })
    await expect
      .poll(async () => (await api.ok<{ state: string }>('GET', `/api/capacity?agent=${agent}&account=${account}`)).state, { timeout: 30_000, message: `${agent} on ${account} read` })
      .toBe('ok')
  }
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
  await expect(dialog.locator('[data-launch-member="builder"] [data-launch-member-account]')).toHaveText(`the default at the launch (now ${work})`)
  await expect(dialog.locator('[data-launch-member="reviewer"] [data-launch-member-account]')).toHaveText(team)
  await expect(dialog.locator('[data-launch-member="reviewer"] [data-launch-member-model]')).toHaveText('gpt-e2e')
  await expect(dialog.locator('[data-launch-member] [data-capacity-panel]')).toHaveCount(2)
  await expect(dialog.locator('[data-launch-member] input, [data-launch-member] [role="combobox"]')).toHaveCount(0)
  // Each line's warning, and the window's reset in its details, opened by the keyboard.
  const builderLine = dialog.locator('[data-launch-member="builder"]')
  await expect(builderLine.locator('[data-capacity-warning]')).toHaveText(WARN, { timeout: 30_000 })
  await builderLine.locator('[data-capacity-details-open]').focus()
  await page.keyboard.press('Enter')
  await expect(builderLine.locator('[data-capacity-details]')).toContainText(WEEKLY_RESET)
  await expect(dialog.locator('[data-launch-member="reviewer"] [data-capacity-warning]')).toHaveText(LIMIT)
  await dialog.locator('[data-launch-confirm]').click()
  await expect(dialog).toHaveAttribute('data-launch-state', 'started', { timeout: 120_000 })
  const runId = (await dialog.locator('[data-launch-run-link]').getAttribute('data-launch-run-link')) ?? ''
  made.runs.push(runId)

  // The revision's read held at this test's route, then refused, then let through: the Logins say it is being read and show no line
  // meanwhile, then say it could not be read, and read it again as they open.
  let mode: 'hold' | 'refuse' | 'pass' = 'hold'
  let release!: () => void
  const held = new Promise<void>((r) => (release = r))
  await page.route(
    (u) => u.pathname === `/api/dispatch/plans/${plan.id}`,
    async (route) => {
      if (route.request().method() !== 'GET') return route.fallback()
      if (mode === 'hold') await held
      return mode === 'refuse' ? route.fulfill(refused('e2e: the revision is held back')) : route.fallback()
    },
  )
  await page.goto(`/runs/${encodeURIComponent(runId)}`)
  const button = page.locator('[data-crew-run-header] [data-run-logins-button]')
  await button.click({ timeout: 30_000 })
  const logins = page.locator('[data-run-logins]')
  await expect(logins.locator('[data-run-logins-reading]')).toBeVisible()
  await expect(logins.locator('[data-run-login]')).toHaveCount(0)
  mode = 'refuse'
  release()
  await expect(logins.locator('[data-run-logins-unread]')).toContainText('could not be read, so which member took the default at the launch is not said: e2e: the revision is held back')
  await expect(logins.locator('[data-run-logins-reading]')).toHaveCount(0)
  await expect(logins.locator('[data-run-login-at-launch]')).toHaveCount(0)
  await page.keyboard.press('Escape')
  await expect(logins).toHaveCount(0)
  mode = 'pass'
  await button.click()
  await expect(logins.locator('[data-run-login="builder"] [data-run-login-account]')).toHaveText(work)
  await expect(logins.locator('[data-run-login="builder"] [data-run-login-at-launch]')).toHaveText('(the default at the launch)')
  await expect(logins.locator('[data-run-logins-unread]')).toHaveCount(0)
  await expect(logins.locator('[data-run-login="reviewer"] [data-run-login-account]')).toHaveText(team)
  await expect(logins.locator('[data-run-login="reviewer"] [data-run-login-at-launch]')).toHaveCount(0)
})

/** An error the server could answer, for this spec's own routes. */
const refused = (message: string) => ({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: { code: 'unavailable', message } }) })

/** The first `method` request `match` takes, held at this test's route until `release` (`seen` once it is held); the others pass. */
async function holdOnce(page: Page, match: (u: URL) => boolean, method: string) {
  let seen!: () => void
  let release!: () => void
  const at = new Promise<void>((r) => (seen = r))
  const gate = new Promise<void>((r) => (release = r))
  let first = true
  const handler = async (route: Route) => {
    if (route.request().method() !== method || !first) return route.fallback()
    first = false
    seen()
    await gate
    await route.fallback()
  }
  await page.route(match, handler)
  return { seen: at, release, off: () => page.unroute(match, handler) }
}

test("the launch sheet draws the crew as the server holds it, for the crew it was opened for, with each line's warning and the login an entry sets", async ({ page, api, state }) => {
  test.setTimeout(120_000)
  const warnDir = folder(state.home)
  writeFileSync(join(warnDir, 'stub-usage'), 'warning\n')
  const near = label('near')
  const nearId = await addAccount(api, 'claude', near, warnDir)
  // Two crews of a Claude Code member and one of the agent whose entry sets its login; nothing here is launched.
  const body = (name: string, account?: string) => ({
    name,
    goal: '',
    cwd: '',
    where: 'server',
    isolation: 'none',
    openAfterLaunch: false,
    yolo: false,
    members: [
      { name: 'lead', agentId: 'claude', prompt: '', start: { when: 'manual' }, ...(account ? { account } : {}) },
      { name: 'pinned', agentId: 'own-login', prompt: '', start: { when: 'manual' } },
    ],
  })
  const [aName, bName] = [label('sheet a'), label('sheet b')]
  const a = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', body(aName))).crew
  made.crews.push(a.id)
  const b = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', body(bName))).crew
  made.crews.push(b.id)
  const launches: string[] = []
  page.on('request', (r) => {
    if (r.method() === 'POST' && /^\/api\/crews\/[^/]+\/launch$/.test(new URL(r.url()).pathname)) launches.push(r.url())
  })

  await page.goto(`/crews/${encodeURIComponent(a.id)}`)
  await expect(page.getByLabel('Crew name')).toHaveValue(aName, { timeout: 30_000 })
  // The editor offers the agent whose entry sets its login that login alone, once the server's view of it says so.
  await expect(page.locator('[data-crew-member]').nth(1).locator('[data-member-account]')).toContainText('The login its entry sets (ANTHROPIC_BASE_URL)')
  // Changed through the API after the page read it: the lead now names Near.
  await api.ok('PUT', `/api/crews/${encodeURIComponent(a.id)}`, body(aName, nearId))

  // The sheet's read held at this test's route: the sheet says it is reading and Launch is held; let through, the server's copy is drawn.
  let release!: () => void
  const held = new Promise<void>((r) => (release = r))
  const aCrew = (u: URL) => u.pathname === `/api/crews/${a.id}`
  await page.route(aCrew, async (route) => {
    if (route.request().method() === 'GET') await held
    await route.fallback()
  })
  const confirm = page.locator('[data-crew-launch-confirm]')
  await page.locator('[data-launch]').click()
  await expect(page.locator('[data-crew-launch-reading]')).toBeVisible()
  await expect(confirm).toBeDisabled()
  release()
  const lead = page.locator('[data-crew-launch-line="lead"]')
  await expect(lead.locator('[data-crew-launch-account]')).toHaveText(near)
  await page.unroute(aCrew)
  // Near's usage at the provider's warning: said in words, in the warning's colour, and Launch stays enabled.
  await expect(lead.locator('[data-capacity-panel]')).toHaveAttribute('data-capacity-state', 'ok', { timeout: 30_000 })
  await expect(lead.locator('[data-capacity-line]')).toContainText('Weekly, all models 92%')
  await expect(lead.locator('[data-capacity-warning]')).toHaveText(WARN)
  await expect(lead.locator('[data-capacity-warning]')).toHaveAttribute('data-capacity-warning-color', 'warning')
  await expect(confirm).toBeEnabled()
  // The window's reset, in the line's details, opened by a pointer or a touch.
  await lead.locator('[data-capacity-details-open]').click()
  await expect(lead.locator('[data-capacity-details]')).toContainText(WEEKLY_RESET)
  // The member that names no account, of an agent whose entry sets its login: named by that login, as the server's view resolves it.
  await expect(page.locator('[data-crew-launch-line="pinned"] [data-crew-launch-account]')).toHaveText('the login its entry sets (ANTHROPIC_BASE_URL)')

  // Closed while its read is held at this test's route: the read is ended there, and opened again the sheet draws its own read.
  await page.locator('[data-crew-launch-cancel]').click()
  await expect(confirm).toHaveCount(0)
  const ended: string[] = []
  page.on('requestfailed', (r) => ended.push(`${r.method()} ${new URL(r.url()).pathname}`))
  let releaseAgain!: () => void
  const heldAgain = new Promise<void>((r) => (releaseAgain = r))
  await page.route(aCrew, async (route) => {
    if (route.request().method() === 'GET') await heldAgain
    await route.fallback().catch(() => {})
  })
  await page.locator('[data-launch]').click()
  await expect(page.locator('[data-crew-launch-reading]')).toBeVisible()
  await page.locator('[data-crew-launch-cancel]').click()
  await expect.poll(() => ended, { message: "the sheet's read was ended as it closed" }).toContain(`GET /api/crews/${a.id}`)
  releaseAgain()
  await page.unroute(aCrew)
  await expect(confirm).toHaveCount(0)
  await page.locator('[data-launch]').click()
  await expect(lead.locator('[data-crew-launch-account]')).toHaveText(near)

  // The page moves to crew B while the sheet is open for A: the sheet closes, and nothing was launched.
  await navigate(page, `/crews/${encodeURIComponent(b.id)}`)
  await expect(page.getByLabel('Crew name')).toHaveValue(bName, { timeout: 30_000 })
  await expect(confirm).toHaveCount(0)

  // B's read refused: the sheet says so in words and offers to read again, Launch held; read again once it answers, B is drawn.
  let refuse = true
  await page.route(
    (u) => u.pathname === `/api/crews/${b.id}`,
    (route) => (refuse && route.request().method() === 'GET' ? route.fulfill(refused('e2e: the crew is held back')) : route.fallback()),
  )
  await page.locator('[data-launch]').click()
  await expect(page.locator('[data-crew-launch-read-error]')).toContainText('The crew could not be read, so nothing is launched: e2e: the crew is held back')
  await expect(confirm).toBeDisabled()
  refuse = false
  await page.locator('[data-crew-launch-read-again]').click()
  await expect(lead.locator('[data-crew-launch-account]')).toHaveText('the default at the launch (now Default login)')
  await expect(confirm).toBeEnabled()
  expect(launches, 'nothing was launched before Launch').toEqual([])

  // One confirmation, one run: two clicks on Launch in one task send one launch. The launch held at this test's route, the sheet stays
  // open until it answers; then, "Open the run page" off, it closes, and a click on Launch the moment it is marked closing sends none.
  let releaseLaunch!: () => void
  const launchHeld = new Promise<void>((r) => (releaseLaunch = r))
  const bLaunch = (u: URL) => u.pathname === `/api/crews/${b.id}/launch`
  await page.route(bLaunch, async (route) => {
    await launchHeld
    await route.fallback()
  })
  const answered = page.waitForResponse((r) => bLaunch(new URL(r.url())))
  await confirm.evaluate((el: HTMLElement) => {
    el.click()
    el.click()
  })
  await expect.poll(() => launches.length, { message: 'the first click sent its launch' }).toBe(1)
  // The page's own click, the moment the sheet is marked closing: its observer is in place before the launch is let through.
  type Closing = { e2eClosingClick: Promise<string> }
  await page.evaluate(() => {
    const dialog = document.querySelector('[data-crew-launch-confirm]')!.closest('[role="dialog"]')!
    ;(window as unknown as Closing).e2eClosingClick = new Promise<string>((resolve) => {
      new MutationObserver((_, o) => {
        if (dialog.getAttribute('data-state') !== 'closed') return
        o.disconnect()
        const button = document.querySelector<HTMLButtonElement>('[data-crew-launch-confirm]')!
        button.click()
        resolve(button.disabled ? 'disabled' : 'enabled')
      }).observe(dialog, { attributes: true, attributeFilter: ['data-state'] })
    })
  })
  releaseLaunch()
  const launched = (await (await answered).json()) as { run: Run }
  made.runs.push(launched.run.id)
  expect(await page.evaluate(() => (window as unknown as Closing).e2eClosingClick), 'Launch is disabled while the sheet closes').toBe('disabled')
  await expect(confirm).toHaveCount(0)
  await page.unroute(bLaunch)
  expect(launches, 'one launch for one confirmation').toHaveLength(1)
})

test("the crew editor shows a member's account and model as saved while the accounts cannot be read, and a save keeps them", async ({ page, api }) => {
  const name = label('unread crew')
  const { crew } = await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
    name,
    goal: '',
    cwd: '',
    where: 'server',
    isolation: 'none',
    openAfterLaunch: false,
    yolo: false,
    members: [{ name: 'lead', agentId: 'claude', prompt: '', start: { when: 'manual' }, account: 'default', model: 'opus' }],
  })
  made.crews.push(crew.id)
  let status = 503
  await page.route(
    (u) => u.pathname === '/api/accounts',
    (route) => route.fulfill(status === 503 ? refused('e2e: the accounts are held back') : { status, contentType: 'application/json', body: JSON.stringify({ error: { code: 'forbidden', message: 'e2e: not this token' } }) }),
  )
  await page.goto(`/crews/${encodeURIComponent(crew.id)}`)
  const row = page.locator('[data-crew-member]').nth(0)
  const unread = row.locator('[data-member-login-unread]')
  await expect(unread).toHaveText('Account: Default login, kept as saved: none can be chosen while the accounts cannot be read (e2e: the accounts are held back).', { timeout: 30_000 })
  await expect(row.locator('[data-member-account]')).toHaveCount(0)
  // The model does not depend on the accounts: its field is there, as saved.
  await expect(row.locator('[data-member-model]')).toHaveValue('opus')
  await expect(row.locator('[data-member-model]')).toBeEditable()
  // A save of another change sends them as saved.
  await page.getByLabel('Crew name').fill(`${name} 2`)
  await expect(page.getByLabel('Crew name')).toHaveValue(`${name} 2`)
  const saved = page.waitForRequest((r) => r.method() === 'PUT' && new URL(r.url()).pathname === `/api/crews/${crew.id}`)
  await page.locator('[data-unsaved-bar]').getByRole('button', { name: 'Save', exact: true }).click()
  expect(((await saved).postDataJSON() as { members: Array<Record<string, unknown>> }).members[0]).toMatchObject({ account: 'default', model: 'opus' })
  // A token that may not read the accounts: the same line, with that reason.
  status = 403
  await page.reload()
  await expect(unread).toHaveText('Account: Default login, kept as saved: none can be chosen while the accounts cannot be read (this token may not read them).', { timeout: 30_000 })
  await expect(row.locator('[data-member-model]')).toHaveValue('opus')
})

test("Brakeman's account is chosen from the accounts of its agent's tool, saved as that account's folder, with the Capacity switch", async ({ page, api, state }) => {
  const work = label('brakeman')
  const dir = folder(state.home)
  const workId = await addAccount(api, 'claude', work, dir)
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
  await expect(card.locator('[data-brakeman-account-select]')).toContainText(`${elsewhere} (not in Accounts by this path)`, { timeout: 30_000 })
  await expect(card.locator('[data-brakeman-account-unlisted]')).toHaveText('Not in Accounts by this path: add it there to read its usage.')

  // The account it names retired: said by its label, and that Brakeman's turns are refused until another is chosen.
  const latest = (await api.ok<{ config: Record<string, unknown> & { rev: number } }>('GET', '/api/brakeman')).config
  await api.ok('PUT', '/api/brakeman/config', { rev: latest.rev, config: { ...latest, account: dir } })
  const { rev } = await accounts(api)
  await api.ok('POST', `/api/accounts/${workId}/retire`, { expectedRev: rev })
  await page.reload()
  await expect(card.locator('[data-brakeman-account-retired]')).toHaveText(`${work} is retired: Brakeman's turns are refused until another account is chosen.`, { timeout: 30_000 })
  await expect(card.locator('[data-brakeman-account-select]')).toContainText(`${work} (retired)`)
})

test("a crew that inherits its yolo setting is drawn with the server's default once it is read, opened from the app-wide palette on a fresh visit", async ({ page, api }) => {
  const crew = await saveCrew(api, label('inherits'), [{ name: 'lead', agentId: 'yolo-login' }], null)
  await page.goto('/agents')
  await expect(page.locator('[data-session-list]')).toBeVisible({ timeout: 30_000 })
  // The read that carries the server's default held at this test's route from the Enter on, then answered with the default on.
  let release!: () => void
  const held = new Promise<void>((r) => (release = r))
  await paletteLaunch(page, crew, () =>
    page.route(
      (u) => u.pathname === '/api/catalog',
      async (route) => {
        if (route.request().method() !== 'GET') return route.fallback()
        await held
        const res = await route.fetch()
        await route.fulfill({ response: res, json: { ...((await res.json()) as object), yoloDefault: true } })
      },
    ),
  )
  await expect(page).toHaveURL(new RegExp(`/crews/${crew.id}$`), { timeout: 15_000 })
  const confirm = page.locator('[data-crew-launch-confirm]')
  await expect(page.locator('[data-crew-launch-reading]')).toBeVisible({ timeout: 15_000 })
  await expect(confirm).toBeDisabled()
  release()
  // The member whose yolo recipe sets its login: that login, as the server's view of a yolo launch resolves it.
  await expect(page.locator('[data-crew-launch-line="lead"] [data-crew-launch-account]')).toHaveText('the login its launch sets', { timeout: 30_000 })
  await expect(confirm).toBeEnabled()
  await page.locator('[data-crew-launch-cancel]').click()
  await expect(confirm).toHaveCount(0)
})

test('the launch sheet closes for another token and for a crew gone, and an ask whose navigation ended elsewhere opens nothing later', async ({ page, api }) => {
  const a = await saveCrew(api, label('token'), [{ name: 'lead', agentId: 'claude' }])
  const b = await saveCrew(api, label('gone'), [{ name: 'lead', agentId: 'claude' }])
  const launches: string[] = []
  page.on('request', (r) => {
    if (r.method() === 'POST' && /^\/api\/crews\/[^/]+\/launch$/.test(new URL(r.url()).pathname)) launches.push(r.url())
  })
  const confirm = page.locator('[data-crew-launch-confirm]')
  const name = page.getByLabel('Crew name')

  // Another workbench token while the sheet is open, through the token's store: the sheet closes.
  await page.goto(`/crews/${encodeURIComponent(a.id)}`)
  await expect(name).toHaveValue(a.name, { timeout: 30_000 })
  await page.locator('[data-launch]').click()
  await expect(confirm).toBeEnabled({ timeout: 30_000 })
  await page.evaluate(() => {
    const h = (document.querySelector('#__nuxt') as unknown as { __vue_app__: { config: { globalProperties: { $nuxt: { _conductorWorkbenchToken: { token: { value: string }; changes: { value: number } } } } } } }).__vue_app__.config.globalProperties.$nuxt._conductorWorkbenchToken
    h.changes.value++
    h.token.value = `${h.token.value}-another`
  })
  await expect(confirm).toHaveCount(0)

  // A crew deleted after the page read it: the sheet's read finds it gone, the sheet closes and the page says so.
  await page.goto(`/crews/${encodeURIComponent(b.id)}`)
  await expect(name).toHaveValue(b.name, { timeout: 30_000 })
  await api.ok('DELETE', `/api/crews/${encodeURIComponent(b.id)}`)
  await page.locator('[data-launch]').click()
  await expect(page.getByText(`${b.id} is no longer saved on the server: nothing was launched.`).first()).toBeVisible({ timeout: 15_000 })
  await expect(confirm).toHaveCount(0)

  // An ask whose navigation ends on another page (a guard of this test's own sends it there) is dropped: a later visit opens no sheet.
  await page.goto('/agents')
  await expect(page.locator('[data-session-list]')).toBeVisible({ timeout: 30_000 })
  await page.evaluate((id) => {
    const router = (document.querySelector('#__nuxt') as unknown as { __vue_app__: { config: { globalProperties: { $router: { beforeEach: (g: (to: { path: string }) => string | boolean) => () => void } } } } }).__vue_app__.config.globalProperties.$router
    ;(window as unknown as { e2eGuardOff: () => void }).e2eGuardOff = router.beforeEach((to) => (to.path === `/crews/${id}` ? '/history' : true))
  }, a.id)
  await paletteLaunch(page, a)
  await expect(page).toHaveURL(/\/history$/, { timeout: 15_000 })
  await page.evaluate(() => (window as unknown as { e2eGuardOff: () => void }).e2eGuardOff())
  await navigate(page, `/crews/${encodeURIComponent(a.id)}`)
  await expect(name).toHaveValue(a.name, { timeout: 30_000 })
  await expect(page.locator('[data-crew-tabs]')).toBeVisible()
  await expect(confirm).toHaveCount(0)
  expect(launches, 'nothing was launched').toEqual([])
})

test("the crew editor asks each member's login as the crew launches, marks an account the launch refuses in the server's words, and clears a tool's own on an agent change", async ({ page, api, state }) => {
  const work = label('editor')
  const workId = await addAccount(api, 'claude', work, folder(state.home))
  // A crew with yolo whose member's yolo recipe sets its login, and one whose member's entry sets it, each naming Work beside it.
  const yolo = await saveCrew(api, label('yolo editor'), [{ name: 'lead', agentId: 'yolo-login', account: workId }], true)
  const entry = await saveCrew(api, label('entry editor'), [{ name: 'pinned', agentId: 'own-login', account: workId, model: 'opus' }])
  // The launch route's refusal of each, which the editor's words are compared with.
  const refusal = async (id: string) => {
    const r = await api.call<{ error?: { message: string }; run?: { id: string } }>('POST', `/api/crews/${encodeURIComponent(id)}/launch`)
    if (r.body?.run) made.runs.push(r.body.run.id)
    expect(r.status, `the launch of ${id} is refused`).toBe(400)
    return r.body.error!.message
  }

  await page.goto(`/crews/${encodeURIComponent(yolo.id)}`)
  await expect(page.getByLabel('Crew name')).toHaveValue(yolo.name, { timeout: 30_000 })
  const lead = page.locator('[data-crew-member]').nth(0)
  // Asked with yolo, as the crew launches: that login alone is offered, and Work beside it is marked.
  await expect(lead.locator('[data-member-account-error]')).toHaveText('this launch sets its login: it uses that login, its default account only', { timeout: 30_000 })
  await lead.locator('[data-member-account]').click()
  await expect(page.getByRole('option')).toHaveText(['The login its launch sets', work])
  await page.keyboard.press('Escape')
  await expect(page.getByRole('listbox')).toHaveCount(0)
  // The server names the variable the launch sets; the page, which is not told it, says the rest in the server's words.
  expect(await refusal(yolo.id)).toContain('this launch sets ANTHROPIC_BASE_URL: it uses that login, its default account only')

  await navigate(page, `/crews/${encodeURIComponent(entry.id)}`)
  await expect(page.getByLabel('Crew name')).toHaveValue(entry.name, { timeout: 30_000 })
  const pinned = page.locator('[data-crew-member]').nth(0)
  const marked = pinned.locator('[data-member-account-error]')
  await expect(marked).toHaveText("this agent's entry sets ANTHROPIC_BASE_URL: it uses that login, its default account only", { timeout: 30_000 })
  expect(await refusal(entry.id)).toContain((await marked.textContent())!.trim())

  // Moved to an agent of another tool: the account and the model were Claude Code's, so Codex's names neither.
  await pinned.getByRole('combobox', { name: 'Agent for pinned' }).click()
  await page.getByRole('option', { name: 'Codex CLI (e2e stub)' }).click()
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await expect(pinned.locator('[data-member-account]')).toContainText('The default at launch', { timeout: 30_000 })
  await expect(marked).toHaveCount(0)
  await expect(pinned.locator('[data-member-model]')).toHaveValue('')
})

test("a brief's capacity chip says how old and how sure its reading is, and its details open and close by the keyboard alone", async ({ page }) => {
  // A brief of this test's own, answered at its routes: one fact citing a reading that was stale when it was cited, at the warning.
  const id = `br-${900_000 + Math.floor(Math.random() * 90_000)}`
  const ref = 'capacity:claude/a-aaaaaaaaaaaa@2026-11-14T09:00:00Z'
  const at = '2026-11-14T09:05:00Z'
  const reading = {
    agent: { id: 'claude', name: 'Claude Code', adapter: 'claude', version: '2.1.322' },
    account: { id: 'a-aaaaaaaaaaaa', label: 'Work', plan: 'max' },
    state: 'ok',
    windows: [{ source: 'provider', kind: 'weekly_all', name: 'Weekly, all models', percent: 92, severity: 'warning', resetsAt: new Date(Date.now() + 5 * 86_400_000 + 3_600_000).toISOString() }],
    credits: [],
    counts: { source: 'conductor', estimate: true, running: 1, started5h: 2, started7d: 3 },
    countedAt: '2026-11-14T09:01:00Z',
    readAt: '2026-11-14T09:00:00Z',
    readBy: { program: 'Claude Code', version: '2.1.322', qualified: '2.1.322' },
    fresh: 'stale',
  }
  const request = {
    id,
    opId: 'e2e-capacity-chip',
    kind: 'briefing',
    createdAt: at,
    state: 'briefed',
    agent: { agentId: 'claude' },
    config: { visibility: { runs: true, logs: true, dispatch: true, sessions: true, touched: true, terminals: false, capacity: true }, scope: {}, permissions: { observe: true, draft: false, propose: false }, budget: { turnMinutes: 15, readsPerTurn: 300, turnsPerDay: 40, minutesPerDay: 240 }, rev: 1 },
    reads: 1,
    hasBrief: true,
    briefAt: at,
  }
  const brief = { schema: 'brakeman.brief/1', title: 'e2e capacity brief', summary: 'A reading cited.', items: [{ kind: 'fact/1', text: 'Work is near its weekly limit.', evidence: [ref] }] }
  await page.route((u) => u.pathname === `/api/brakeman/requests/${id}`, (route) => route.fulfill({ json: { request, brief } }))
  await page.route((u) => u.pathname === `/api/brakeman/requests/${id}/refs`, (route) => route.fulfill({ json: { refs: [{ ref, seen: {}, now: {}, capacity: reading }] } }))
  await page.goto(`/brakeman?request=${id}`)
  const chip = page.locator(`[data-ref-chip="${ref}"]`)
  // The chip's own text: when it was read, that it was stale when cited, and that the agent may stop.
  await expect(chip.locator('[data-ref-capacity]')).toContainText('stale when cited', { timeout: 30_000 })
  await expect(chip.locator('[data-ref-capacity]')).toContainText('may stop')
  // Tab to the chip, Enter opens its details, Escape closes them and gives the chip the focus back; Space opens them again.
  const open = chip.locator('[data-ref-capacity-open]')
  for (let i = 0; i < 100 && !(await open.evaluate((el) => el === document.activeElement)); i++) await page.keyboard.press('Tab')
  await expect(open).toBeFocused()
  await page.keyboard.press('Enter')
  const details = page.locator('[data-ref-capacity-details]')
  await expect(details).toContainText('Stale when the brief cited it')
  await expect(details).toContainText(WARN)
  await expect(details).toContainText(WEEKLY_RESET)
  await expect(details).toContainText(/counted by this server since it started, as of /)
  await expect(details).toContainText('Read ')
  await page.keyboard.press('Escape')
  await expect(details).toHaveCount(0)
  await expect(open).toBeFocused()
  await page.keyboard.press('Space')
  await expect(details).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(details).toHaveCount(0)
  await expect(open).toBeFocused()
})

/** Another workbench token in the page, through the token's store: what the page holds under the old one stands no more. */
const changeToken = (page: Page) =>
  page.evaluate(() => {
    const h = (document.querySelector('#__nuxt') as unknown as { __vue_app__: { config: { globalProperties: { $nuxt: { _conductorWorkbenchToken: { token: { value: string }; changes: { value: number } } } } } } }).__vue_app__.config.globalProperties.$nuxt._conductorWorkbenchToken
    h.changes.value++
    h.token.value = `${h.token.value}-another`
  })

test("a draft's launch starts a run only under the opening and the token it was confirmed under, and a launch closes only its own sheet", async ({ page, api }) => {
  const a = await saveCrew(api, label('confirmed'), [{ name: 'lead', agentId: 'claude' }])
  const launches: string[] = []
  page.on('request', (r) => {
    if (r.method() === 'POST' && /^\/api\/crews\/[^/]+\/launch$/.test(new URL(r.url()).pathname)) launches.push(r.url())
  })
  const confirm = page.locator('[data-crew-launch-confirm]')
  const cancel = page.locator('[data-crew-launch-cancel]')
  const name = page.getByLabel('Crew name')
  const lead = page.locator('[data-crew-launch-line="lead"]')
  const notStarted = page.getByText('was saved, but the launch sheet was opened again or the workbench token changed meanwhile: no run was started.')
  const aCrew = (u: URL) => u.pathname === `/api/crews/${a.id}`

  // A member's view refused at this test's route: its compact panel says so with Read again, which reads it and draws it.
  let refuseView = true
  const views = (u: URL) => u.pathname === '/api/capacity'
  await page.route(views, (route) => (refuseView && route.request().method() === 'GET' ? route.fulfill(refused('e2e: the view is held back')) : route.fallback()))
  budget(READ, READ, EXPECT, EXPECT, EXPECT) // the page, the view refused, read again, its error gone, the sheet closed
  await page.goto(`/crews/${encodeURIComponent(a.id)}`)
  await expect(name).toHaveValue(a.name, { timeout: 30_000 })
  await page.locator('[data-launch]').click()
  await expect(lead.locator('[data-capacity-error]')).toContainText('e2e: the view is held back', { timeout: 30_000 })
  refuseView = false
  // The panel reads again by itself after a refused view: Read again only hurries it, and can be gone before a click lands. It is
  // pressed until the view it leads to is drawn (readAgain), never once and blind.
  await readAgain(lead.locator('[data-capacity-again]'), lead.locator('[data-capacity-panel]:not(:has([data-capacity-error]))'))
  await expect(lead.locator('[data-capacity-panel]')).toHaveAttribute('data-capacity-state', /^[a-z_]+$/)
  await expect(lead.locator('[data-capacity-error]')).toHaveCount(0)
  await page.unroute(views)
  await cancel.click()
  await expect(confirm).toHaveCount(0)

  // An edited draft's Launch, its save held at this test's route; another token while it is held; the save let through: no launch.
  budget(READ, EXPECT) // the sheet, the page's words
  await name.fill(`${a.name} 2`)
  await page.locator('[data-launch]').click()
  await expect(confirm).toBeEnabled({ timeout: 30_000 })
  const save1 = await holdOnce(page, aCrew, 'PUT')
  await confirm.click()
  await save1.seen
  await changeToken(page)
  const saved1 = page.waitForResponse((r) => r.request().method() === 'PUT' && aCrew(new URL(r.url())))
  save1.release()
  expect((await saved1).ok(), 'the save went through under the token it was sent with').toBe(true)
  await expect(notStarted.first()).toBeVisible({ timeout: 15_000 })
  await save1.off()
  expect(launches, 'no launch under another token').toEqual([])

  // Again under the page's own token: the save held, Cancel held with it; the sheet closed by Escape and opened again for the same crew
  // from the app-wide palette; the save let through: the first confirmation starts nothing, and the sheet opened again stays open.
  // The page, the sheet, Cancel held, the sheet closed, the palette's row and its focus, the sheet again, the page's words, its Launch.
  budget(READ, READ, EXPECT, EXPECT, EXPECT, EXPECT, READ, EXPECT, EXPECT)
  await page.goto(`/crews/${encodeURIComponent(a.id)}`)
  await expect(name).toHaveValue(`${a.name} 2`, { timeout: 30_000 })
  await name.fill(`${a.name} 3`)
  await page.locator('[data-launch]').click()
  await expect(confirm).toBeEnabled({ timeout: 30_000 })
  const save2 = await holdOnce(page, aCrew, 'PUT')
  await confirm.click()
  await save2.seen
  await expect(cancel).toBeDisabled()
  await page.keyboard.press('Escape')
  await expect(confirm).toHaveCount(0)
  await paletteLaunch(page, a)
  await expect(lead.locator('[data-crew-launch-account]')).toBeVisible({ timeout: 30_000 })
  const saved2 = page.waitForResponse((r) => r.request().method() === 'PUT' && aCrew(new URL(r.url())))
  save2.release()
  expect((await saved2).ok()).toBe(true)
  await expect(notStarted.first()).toBeVisible({ timeout: 15_000 })
  await save2.off()
  await expect(confirm, 'the sheet opened again stays open, its Launch free once the first launch ended').toBeEnabled()
  expect(launches, 'the first confirmation started nothing').toEqual([])

  // That sheet read the crew while the save was held: its Launch names the copy from before the save, which the server refuses (#957,
  // crew-launch-revision.spec.ts); the sheet reads the crew again and draws the copy saved.
  const launchA = (u: URL) => u.pathname === `/api/crews/${a.id}/launch`
  budget(EXPECT, READ, READ) // the notice, the crew drawn again, its Launch
  const refusedLaunch = page.waitForResponse((r) => r.request().method() === 'POST' && launchA(new URL(r.url())))
  await confirm.click()
  expect((await refusedLaunch).status(), 'the copy from before the save is refused').toBe(409)
  await expect(page.locator('[data-crew-launch-changed]')).toBeVisible()
  await expect(page.locator('[role="dialog"]').filter({ has: confirm })).toContainText(`Launch ${a.name} 3`, { timeout: 30_000 })
  await expect(confirm).toBeEnabled({ timeout: 30_000 })

  // The sheet read again launches, its launch held at this test's route; closed and opened again meanwhile, it stays open.
  // Cancel held, the sheet closed, the palette's row and its focus, the sheet again, its Launch, its state, closed at the end.
  budget(EXPECT, EXPECT, EXPECT, EXPECT, READ, EXPECT, EXPECT, EXPECT)
  const held = await holdOnce(page, launchA, 'POST')
  await confirm.click()
  await held.seen
  await expect(cancel).toBeDisabled()
  await page.keyboard.press('Escape')
  await expect(confirm).toHaveCount(0)
  await paletteLaunch(page, a)
  await expect(lead.locator('[data-crew-launch-account]')).toBeVisible({ timeout: 30_000 })
  const answered = page.waitForResponse((r) => r.request().method() === 'POST' && launchA(new URL(r.url())))
  held.release()
  const launched = (await (await answered).json()) as { run: Run }
  made.runs.push(launched.run.id)
  await held.off()
  await expect(confirm, 'the launch closed none of the sheet opened since').toBeEnabled()
  await expect(page.locator('[role="dialog"]').filter({ has: confirm })).toHaveAttribute('data-state', 'open')
  expect(launches, 'the refused launch, then one from its own confirmation').toHaveLength(2)
  await cancel.click()
  await expect(confirm).toHaveCount(0)
})

test("the run page's Logins say a registered account in words while the accounts are read or could not be, and Add member asks as the yolo run binds", async ({ page, api, state }) => {
  test.setTimeout(180_000)
  const work = label('logins')
  const workId = await addAccount(api, 'claude', work, folder(state.home))
  // A run with yolo on, its member named on Work; it starts when pressed, so nothing runs.
  const crew = await saveCrew(api, label('yolo run'), [{ name: 'lead', agentId: 'claude', account: workId }], true)
  const { run } = await api.ok<{ run: Run }>('POST', `/api/crews/${encodeURIComponent(crew.id)}/launch`)
  made.runs.push(run.id)

  // The accounts' read held at this test's route, then refused: the line says so in words, never by the account's id.
  let asked!: () => void
  const read = new Promise<void>((r) => (asked = r))
  let releaseAccounts!: () => void
  const held = new Promise<void>((r) => (releaseAccounts = r))
  const accountsAt = (u: URL) => u.pathname === '/api/accounts'
  await page.route(accountsAt, async (route) => {
    if (route.request().method() !== 'GET') return route.fallback()
    asked()
    await held
    await route.fulfill(refused('e2e: the accounts are held back'))
  })
  await page.goto(`/runs/${encodeURIComponent(run.id)}`)
  await read
  const button = page.locator('[data-crew-run-header] [data-run-logins-button]')
  await button.click({ timeout: 30_000 })
  const logins = page.locator('[data-run-logins]')
  const account = logins.locator('[data-run-login="lead"] [data-run-login-account]')
  await expect(account).toHaveText('a registered account, not read yet')
  await expect(logins).not.toContainText(workId)
  releaseAccounts()
  await expect(account).toHaveText('a registered account that could not be read: e2e: the accounts are held back')
  await expect(logins).not.toContainText(workId)
  // Opened again, the Logins read the accounts again: the account by its label.
  await page.unroute(accountsAt)
  await page.keyboard.press('Escape')
  await expect(logins).toHaveCount(0)
  await button.click()
  await expect(account).toHaveText(work)
  await page.keyboard.press('Escape')
  await expect(logins).toHaveCount(0)

  // Add member: Work chosen for a Claude Code member, then the agent whose yolo recipe sets its login: asked under the run's yolo,
  // that login alone is offered, Work beside it is marked in the server's words and Add is held; the login chosen, Add is free.
  await page.locator('[data-crew-run-header] [data-run-menu]').click()
  await page.locator('[role="menuitem"][data-run-add]').click()
  const slide = page.locator('[role="dialog"]').filter({ has: page.locator('[data-crew-members]') })
  const row = slide.locator('[data-crew-member]')
  const add = slide.getByRole('button', { name: 'Add to the run' })
  await row.getByRole('combobox', { name: /^Agent for / }).click()
  await page.getByRole('option', { name: 'Claude Code (e2e stub)' }).click()
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await row.locator('[data-member-account]').click()
  await page.getByRole('option', { name: work, exact: true }).click()
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await expect(row.locator('[data-member-account]')).toContainText(work)
  await row.getByRole('combobox', { name: /^Agent for / }).click()
  await page.getByRole('option', { name: 'Yolo login (e2e stub)' }).click()
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await expect(row.locator('[data-member-account-error]')).toHaveText('this launch sets its login: it uses that login, its default account only', { timeout: 30_000 })
  await expect(add).toBeDisabled()
  await row.locator('[data-member-account]').click()
  await expect(page.getByRole('option')).toHaveText(['The login its launch sets', work])
  await page.getByRole('option', { name: 'The login its launch sets' }).click()
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await expect(row.locator('[data-member-account-error]')).toHaveCount(0)
  await expect(add).toBeEnabled()
  await slide.getByRole('button', { name: 'Cancel' }).click()
  await expect(slide).toHaveCount(0)
})
