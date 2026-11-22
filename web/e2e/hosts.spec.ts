import { spawn, type ChildProcess } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { openSync } from 'node:fs'
import { join } from 'node:path'
import type { Page, Route } from '@playwright/test'
import { hostAvailabilityWords, platformWords } from '../app/utils/hosts'
import type { HostRow, HostsReply } from '../app/utils/protocol'
import { clock } from '../app/utils/syncStatus'
import { budget, dayPage, endHosted, expect, hostedName, hostedSession, readAgain, test, type Api } from './fixtures'
import { noSideways } from './layout'
import { serverBinary, stubBash, stubPath } from './server'
import type { E2EState } from './state'

// The Hosts page (docs/plans/hosts.md, PR-3 and PR-4), against a stub of GET /api/hosts: This server, the hosts that reach it, the
// switchyard, the runs and who holds them, each count of what waits linked to its item on Needs you, and nothing that acts on work.
// The stub's keys, session ids and run ids are its own (no test server mints them). The page against the real route, with a real
// `conductor host`, is the last test.

const at = '2026-11-05T14:05:00Z'
const away = '2026-11-05T14:02:00Z'
const seen = '2026-11-05T13:40:00Z'
const started = '2026-11-05T09:12:00Z'

function stub(over: Partial<HostsReply> = {}): HostsReply {
  return {
    at,
    started,
    this: {
      key: 'hs2stub-this',
      kind: 'server',
      name: 'hs2stub-linuxbox',
      platform: 'linux/amd64',
      version: 'v0.7.0',
      availability: 'here',
      sessions: { running: 3, waiting: 1, waitingRef: 'question:hs2stub-s9/q-1' },
      decisions: { open: 2 },
    },
    hosts: [
      {
        key: 'hs2stub-seen',
        kind: 'host',
        name: 'hs2stub-old',
        platform: 'windows/amd64',
        availability: 'seen',
        since: seen,
        sessions: { running: 0, waiting: 0 },
        sessionIds: [],
        keyFrom: 'machine',
      },
      {
        key: 'hs2stub-studio',
        kind: 'host',
        name: 'hs2stub-studio',
        names: 2,
        platform: 'darwin/arm64',
        version: 'v0.7.0',
        user: 'ana',
        availability: 'connected',
        since: away,
        sessions: { running: 2, waiting: 1, waitingRef: 'question:hs2stub-s1/q-7' },
        sessionIds: ['hs2stub-s1', 'hs2stub-s2'],
        where: ['native'],
        keyFrom: 'machine',
      },
      {
        key: 'hs2stub-wsl',
        kind: 'host',
        name: 'hs2stub-wsl',
        platform: 'linux/amd64+wsl',
        availability: 'away',
        since: away,
        sessions: { running: 1, waiting: 0 },
        sessionIds: ['hs2stub-s3'],
        keyFrom: 'machine',
      },
    ],
    switchyard: { url: 'https://yard.example.test/', publishing: 2, connected: 1 },
    runs: [
      { id: 'hs2stub-run-1', name: 'Release', crew: 'release', state: 'running', paused: false, waiting: 1, waitingRef: 'decision:hs2stub-dr-1', heldBy: 'this' },
      { id: 'hs2stub-run-2', name: 'Nightly', crew: 'nightly', state: 'interrupted', paused: false, waiting: 0, heldBy: 'this' },
    ],
    ledger: { holder: 'this', note: '' },
    more: { hosts: 0, runs: 0 },
    ...over,
  }
}

/** Serves `next()` for every read of GET /api/hosts, counting the reads. */
async function serve(page: Page, next: () => { status?: number; json: unknown }): Promise<{ reads: () => number }> {
  let n = 0
  await page.route('**/api/hosts', (route: Route) => {
    n++
    const r = next()
    return route.fulfill({ status: r.status ?? 200, json: r.json })
  })
  return { reads: () => n }
}

const card = (page: Page, key: string) => page.locator(`[data-host-card="${key}"]`)

test('the page draws this server, its hosts, the switchyard and the runs, as the reply says', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await serve(page, () => ({ json: stub() }))
  await page.goto('/crews')
  // The layout's keys are listened for once its sidebar is drawn: G then O opens Hosts.
  await expect(page.locator('#dashboard-sidebar-main')).toBeVisible()
  await page.keyboard.press('g')
  await page.keyboard.press('o')
  await expect(page).toHaveURL(/\/hosts$/)
  await expect(page.locator('[data-hosts]')).toBeVisible()

  const here = card(page, 'hs2stub-this')
  await expect(here.locator('[data-host-name]')).toHaveText('hs2stub-linuxbox')
  await expect(here.locator('[data-host-availability-words]')).toHaveText('Here')
  await expect(here.locator('[data-host-meta]')).toHaveText(`${platformWords('linux/amd64')} · v0.7.0`)
  await expect(here.locator('[data-host-sessions]')).toHaveText('3 sessions')
  await expect(here.locator('[data-host-runs]')).toHaveText('Holds 2 runs')

  // The cards in their order: connected, away, then seen; each host holds no runs.
  await expect(page.locator('[data-hosts-section="hosts"] [data-host-card]')).toHaveCount(3)
  expect(await page.locator('[data-hosts-section="hosts"] [data-host-card]').evaluateAll((els) => els.map((e) => e.getAttribute('data-host-card')))).toEqual(['hs2stub-studio', 'hs2stub-wsl', 'hs2stub-seen'])
  const studio = card(page, 'hs2stub-studio')
  await expect(studio.locator('[data-host-availability-words]')).toHaveText('Connected')
  await expect(studio.locator('[data-host-meta]')).toHaveText('macOS · arm64 · v0.7.0 · user ana')
  await expect(studio.locator('[data-host-names]')).toHaveText('Reported as 2 names')
  await expect(studio.locator('[data-host-where]')).toHaveText('Runs in native')
  await expect(studio.locator('[data-host-session]')).toHaveCount(2)
  await expect(studio.locator('[data-host-session="hs2stub-s1"]')).toHaveAttribute('href', '/sessions/hs2stub-s1')
  await expect(studio.locator('[data-host-runs]')).toHaveText('Holds no runs')
  await expect(card(page, 'hs2stub-wsl').locator('[data-host-availability-words]')).toHaveText(hostAvailabilityWords('away', away))
  await expect(card(page, 'hs2stub-wsl').locator('[data-host-meta]')).toHaveText('Linux · x64 · WSL')
  await expect(card(page, 'hs2stub-seen').locator('[data-host-availability-words]')).toHaveText(`Last seen ${clock(seen)}`)
  await expect(page.locator('[data-hosts-started]')).toContainText(`since this server started at ${clock(started)}`)

  await expect(page.locator('[data-hosts-switchyard-url]')).toHaveText('https://yard.example.test/')
  await expect(page.locator('[data-hosts-switchyard-counts]')).toHaveText('2 sessions published · 1 connected')

  await expect(page.locator('[data-hosts-holder-words]')).toHaveText('Held by this server')
  const release = page.locator('[data-host-run="hs2stub-run-1"]')
  await expect(release.locator('[data-host-run-link]')).toHaveAttribute('href', '/runs/hs2stub-run-1')
  await expect(release.locator('[data-host-run-state] [data-status-word]')).toHaveText('running')
  await expect(release.locator('[data-host-run-holder]')).toHaveText('Held by this server')
  await expect(page.locator('[data-host-run="hs2stub-run-2"] [data-host-run-state] [data-status-word]')).toHaveText('interrupted, not yet continued')
  await expect(page.locator('[data-hosts-footer]')).toHaveText('Runs stay with the server that holds them; nothing on this page moves or starts work.')

  // Nothing on the page acts on work (I2): no button starts, stops, moves, launches, continues or retries anything.
  const body = page.locator('#dashboard-panel-hosts-body, [data-hosts]').first()
  await expect(body.getByRole('button', { name: /start|stop|move|launch|continue|retry/i })).toHaveCount(0)
})

test('each count of what waits links to its item on Needs you, and to the list when more than one waits (PR-4)', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await serve(page, () => ({ json: stub() }))
  await page.goto('/hosts')
  const studio = card(page, 'hs2stub-studio')
  await expect(studio.locator('[data-host-waiting]')).toHaveText('1 needs you')
  await expect(studio.locator('[data-host-waiting]')).toHaveAttribute('href', `/decisions?item=${encodeURIComponent('question:hs2stub-s1/q-7')}`)
  const here = card(page, 'hs2stub-this')
  await expect(here.locator('[data-host-waiting]')).toHaveAttribute('href', `/decisions?item=${encodeURIComponent('question:hs2stub-s9/q-1')}`)
  await expect(here.locator('[data-host-decisions]')).toHaveText('2 decisions wait')
  await expect(here.locator('[data-host-decisions]')).toHaveAttribute('href', '/decisions')
  const run = page.locator('[data-host-run="hs2stub-run-1"] [data-host-run-waiting]')
  await expect(run).toHaveText('1 needs you')
  await expect(run).toHaveAttribute('href', `/decisions?item=${encodeURIComponent('decision:hs2stub-dr-1')}`)
  // Nothing waits: no count and no link.
  await expect(card(page, 'hs2stub-wsl').locator('[data-host-waiting]')).toHaveCount(0)
  await expect(page.locator('[data-host-run="hs2stub-run-2"] [data-host-run-waiting]')).toHaveCount(0)
})

test("each holder's words come from the reply that says them, redrawn: a former holder's line is gone", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const steps: Array<Pick<HostsReply, 'ledger' | 'runs'>> = []
  const run = (heldBy: HostsReply['ledger']['holder'], waiting: number | null) => [{ id: 'hs2stub-run-1', name: 'Release', crew: 'release', state: 'running', paused: false, waiting, heldBy }]
  steps.push({ ledger: { holder: 'this', note: '' }, runs: run('this', 0) })
  steps.push({ ledger: { holder: 'other', note: 'pid 4242 since 2026-11-05T13:00:00Z' }, runs: run('other', null) })
  steps.push({ ledger: { holder: 'none', note: 'this server stopped waiting for the lock at 13:02:10' }, runs: run('none', null) })
  steps.push({ ledger: { holder: 'unknown', note: 'the lock file could not be read: permission' }, runs: run('unknown', null) })
  steps.push({ ledger: { holder: 'this_read_only', note: 'a newer Conductor wrote the database' }, runs: run('this_read_only', 0) })
  let step = 0
  await serve(page, () => ({ json: stub(steps[step]) }))
  await page.goto('/hosts')
  const row = page.locator('[data-host-run="hs2stub-run-1"]')
  const want = [
    'Held by this server',
    `Held by another server on this data directory: pid 4242, since ${clock('2026-11-05T13:00:00Z')}, as that server wrote`,
    'Held by no server: this server stopped waiting for the lock at 13:02:10',
    'Not known who holds these runs: the lock file could not be read: permission',
    'Held by this server, read-only: a newer Conductor wrote the database',
  ]
  for (let i = 0; i < steps.length; i++) {
    if (i > 0) {
      step = i
      await page.getByRole('button', { name: 'Read again' }).click()
    }
    await expect(row.locator('[data-host-run-holder]')).toHaveText(want[i]!)
    await expect(page.locator('[data-hosts-holder]')).toHaveText(want[i]!)
    if (i === 1) await expect(row.locator('[data-host-run-waiting]')).toHaveText('counted on the server that holds it')
    if (i >= 2) await expect(page.locator('[data-hosts]')).not.toContainText('4242')
    await expect(row.getByRole('button')).toHaveCount(0)
  }
})

test('the first read failing says so, and Read again draws the page', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  let fail = true
  await serve(page, () => (fail ? { status: 500, json: { error: { code: 'internal', message: 'the hosts could not be read' } } } : { json: stub() }))
  await page.goto('/hosts')
  const empty = page.locator('[data-page-empty="hosts"]')
  await expect(empty).toHaveAttribute('data-state', 'failed')
  await expect(empty.locator('[data-page-why]')).toContainText('the hosts could not be read')
  fail = false
  // The page reads again by itself too (an event, its 30 s tick): Read again is pressed until the page is drawn, never once and blind.
  await readAgain(empty.locator('[data-page-again]'), card(page, 'hs2stub-this'))
  await expect(page.locator('[data-page-empty="hosts"]')).toHaveCount(0)
})

test('on a phone, in both themes, the page fits its width: nothing scrolls sideways', async ({ browser, state }) => {
  for (const theme of ['dark', 'light'] as const) {
    const page = await dayPage(browser, { server: state, path: '/crews', theme, width: 390, height: 844, touch: true })
    await serve(page, () => ({ json: stub({ hosts: [...stub().hosts, { key: 'hs2stub-long', kind: 'host', name: 'hs2stub-' + 'a-very-long-machine-name-'.repeat(4), availability: 'away', since: away, sessions: { running: 1, waiting: 0 }, sessionIds: ['hs2stub-' + 'x'.repeat(60)], keyFrom: 'session' }] }) }))
    await page.goto('/hosts')
    await expect(card(page, 'hs2stub-long')).toBeVisible()
    await expect(page.locator('[data-hosts-footer]')).toBeVisible()
    await noSideways(page, { root: page.locator('[data-hosts]') })
    await page.context().close()
  }
})

// Against the real route: a real `conductor host` running the stub agent, and a managed run on this server. Counted by what this test
// made alone: its host's card is found by its session's id in the reply, never by a list's length.
test.describe('the page against the real route', () => {
  const hosts: Array<{ name: string; proc: ChildProcess | null }> = []
  const crews: string[] = []
  const runs: string[] = []

  test.afterAll(async ({ api }) => {
    for (const id of runs) await api.stopRun(id)
    for (const id of crews) await api.call('DELETE', `/api/crews/${encodeURIComponent(id)}`)
    for (const h of hosts) await endHosted(api, h.name, h.proc)
  })

  /** A `conductor host` of the stub agent as Codex, in a process group of its own; its session's id once it registered. */
  async function startHost(api: Api, state: E2EState, base: string): Promise<{ id: string; proc: ChildProcess }> {
    const logFile = join(state.root, `${base}-${randomUUID()}.log`)
    const log = openSync(logFile, 'a')
    const name = hostedName(base)
    const bash = stubBash()
    const proc = spawn(serverBinary(), ['host', '--server', state.baseURL, '--token', state.hostToken, '--host-name', `${base}-machine`, '--name', name, '--agent', 'codex', '--cwd', state.home, '--relay-only', '--no-local', '--', bash.command, stubPath, '--session-id', randomUUID()], {
      env: { PATH: bash.path, LANG: 'C.UTF-8', HOME: state.home, CONDUCTOR_BIN: serverBinary(), STUB_IDENTITY: 'codex', STUB_REPORT: 'codex' },
      stdio: ['ignore', log, log],
      detached: true,
    })
    hosts.push({ name, proc })
    return { id: (await hostedSession(api, logFile, name)).id, proc }
  }

  /** The reply's row of the machine that hosts `session`, read as the page reads it. */
  async function rowOf(page: Page, token: string, session: string): Promise<HostRow | undefined> {
    const r = await page.evaluate(async (t) => (await (await fetch('/api/hosts', { headers: { Authorization: `Bearer ${t}` } })).json()) as HostsReply, token)
    return r.hosts.find((h) => h.sessionIds.includes(session))
  }

  test('a host is Connected with its session and its question, a managed run is held by this server, and a host gone is Away, then Last seen', async ({ page, api, state }) => {
    budget(60_000)
    await page.setViewportSize({ width: 1440, height: 900 })
    const first = await startHost(api, state, 'hosts-a')
    await page.goto('/crews')
    await expect(page.locator('#dashboard-sidebar-main')).toBeVisible()
    await page.keyboard.press('g')
    await page.keyboard.press('o')
    await expect(page).toHaveURL(/\/hosts$/)
    await expect(card(page, (await rowOf(page, state.token, first.id))?.key ?? 'none')).toBeVisible()
    const row = (await rowOf(page, state.token, first.id))!
    const mine = card(page, row.key)
    await expect(mine.locator('[data-host-availability-words]')).toHaveText('Connected')
    await expect(mine.locator('[data-host-meta]')).toContainText(platformWords(row.platform) || 'Platform not reported')
    await expect(mine.locator(`[data-host-session="${first.id}"]`)).toHaveAttribute('href', `/sessions/${first.id}`)
    await expect(mine.locator('[data-host-runs]')).toHaveText('Holds no runs')

    // The stub's command approval, asked in the session's terminal: the card's count of what waits, waited on by the count itself.
    await page.goto(`/sessions/${encodeURIComponent(first.id)}`)
    const screen = page.locator('.terminal-host .xterm-screen').first()
    await expect(screen).toBeVisible({ timeout: 30_000 })
    await screen.click()
    await page.keyboard.type('stub approve curl -sI https://example.com')
    await page.keyboard.press('Enter')
    await expect.poll(async () => (await api.session(first.id)).attention?.source ?? '', { timeout: 30_000, message: 'the approval is asked' }).toBe('question')
    await page.goto('/hosts')
    await expect(mine.locator('[data-host-waiting]')).toHaveAttribute('data-count', '1')
    await expect(mine.locator('[data-host-waiting]')).toHaveText('1 needs you')

    // A managed run on this server: held by this server, a link to its run page.
    const crew = await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
      name: 'e2e hosts managed',
      goal: 'wait',
      cwd: '',
      where: 'server',
      openAfterLaunch: false,
      yolo: null,
      isolation: 'worktree',
      members: [{ name: 'builder', agentId: 'claude', prompt: '', start: { when: 'task' } }],
      tasks: [{ id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.' }],
    })
    crews.push(crew.crew.id)
    const run = await api.launchCrew(crew.crew.id)
    runs.push(run.id)
    const runRow = page.locator(`[data-host-run="${run.id}"]`)
    await expect(runRow.locator('[data-host-run-link]')).toHaveAttribute('href', `/runs/${run.id}`)
    const holderWords = async () => {
      await expect(runRow.locator('[data-host-run-holder-words]')).toHaveText('Held by this server')
      await expect(page.locator('[data-hosts]').getByRole('button', { name: /start|stop|move|launch|continue|retry/i })).toHaveCount(0)
    }
    await holderWords()

    // A redraw the test causes: a second host connects; the run's holder and the page's controls are checked again.
    const second = await startHost(api, state, 'hosts-b')
    await expect.poll(async () => (await rowOf(page, state.token, second.id))?.key ?? '', { message: 'the second host is listed' }).not.toBe('')
    await expect(card(page, (await rowOf(page, state.token, second.id))!.key)).toBeVisible()
    await holderWords()

    // The first host gone (its process group killed): Away while its session waits in the grace, then Last seen once it is removed:
    // the grace, then the server's maintenance tick that expires it, then the page's read on the removal.
    process.kill(-first.proc.pid!, 'SIGKILL')
    await expect(mine.locator('[data-host-availability-words]')).toHaveText(/^Away/)
    const graceAndTick = 60_000 + 30_000 + 15_000
    budget(graceAndTick)
    await expect(mine.locator('[data-host-availability-words]')).toHaveText(/^Last seen /, { timeout: graceAndTick })
    await expect(mine.locator('[data-host-session]')).toHaveCount(0)
  })
})
