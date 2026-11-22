import type { Page, Route } from '@playwright/test'
import { hostAvailabilityWords, platformWords } from '../app/utils/hosts'
import type { HostsReply } from '../app/utils/protocol'
import { clock } from '../app/utils/syncStatus'
import { dayPage, expect, readAgain, test } from './fixtures'
import { noSideways } from './layout'

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
