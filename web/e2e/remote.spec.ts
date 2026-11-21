import { mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { Page, Route } from '@playwright/test'
import { Api, expect, test, useTheme } from './fixtures'
import { renderConfig, START_WAIT_MS, startServer, stopServer, type Started } from './server'

// What other machines hold, drawn read only (docs/plans/sync-seams.md, OS-4). The e2e server has no sync store, and with none it
// holds nothing of other machines' (sync-free.spec.ts holds that, a remote/ folder left on disk too): here the app is told a store
// is registered (`sync` in a stubbed capabilities reply, a stubbed report) and GET /api/remote is answered from the spec's records,
// by kind and ticket as the server answers it (internal/api's remote tests hold the server's side). The decision page's and the
// ticket page's "on studio" rows, read only with no action of this machine's offered, and the sidebar's pins of another machine,
// greyed and opening nothing; in both themes and on a phone. The Share, rekey and Sync controls' drawing is vitest's
// (composables/useRemote.test.ts) beside the last test here.

let server: Started | undefined
let api: Api
let root = ''
let ticketKey = ''
let ticketID = ''

const at = '2026-11-20T09:00:00Z'

interface Held {
  kind: string
  id: string
  data: Record<string, unknown>
}

/** The records another machine, studio, holds: what the stubbed GET /api/remote answers. */
const held: Held[] = []

function remote(kind: string, id: string, data: Record<string, unknown>) {
  held.push({ kind, id, data })
}

function decision(id: string, ticket: string, question: string) {
  return {
    id,
    run: 'team-0000aa01',
    openedIn: 'team-0000aa01',
    origin: 'plan',
    type: 'approval',
    gate: 'ship',
    ticket,
    request: { question, options: [{ label: 'Approve' }, { label: 'Reject' }] },
    requestSHA256: 'a'.repeat(64),
    openedTo: 'operator',
    policy: {},
    state: 'open',
    rev: 1,
    createdAt: at,
    states: { open: true },
  }
}

/** GET /api/remote as the server answers it with a sync store: by kind, a ticket's by its id (its decisions and its runs' pointers). */
function answerRemote(route: Route) {
  const q = new URL(route.request().url()).searchParams
  const kind = q.get('kind')
  const ticket = q.get('ticket')
  const records = held
    .filter((r) => !kind || r.kind === kind)
    .filter((r) => !ticket || (ticket === ticketKey && r.data.ticket === ticketID))
    .map((r) => ({ kind: r.kind, device: 'dev-studio', deviceName: 'studio', id: r.id, rev: 1, at, data: r.data }))
  return route.fulfill({ json: { records } })
}

test.beforeAll(async () => {
  test.setTimeout(START_WAIT_MS + 30_000)
  const here = dirname(fileURLToPath(import.meta.url))
  root = mkdtempSync(join(tmpdir(), 'conductor-e2e-remote-'))
  const home = join(root, 'home')
  const data = join(root, 'data')
  for (const d of [home, data]) mkdirSync(d, { recursive: true })
  const config = join(root, 'conductor.json')
  renderConfig(join(here, 'conductor.e2e.json'), config)
  server = await startServer({ config, home, data, allowedRoot: root, defaultCwd: root, log: join(root, 'server.log') })
  api = new Api(server)
  await api.ok('POST', '/api/dispatch/projects', { opId: 'p1', key: 'RMT', name: 'Remote' })
  const t = await api.ok<{ ticket: { id: string; key: string } }>('POST', '/api/dispatch/tickets', {
    opId: 't1',
    project: 'RMT',
    title: 'Made here, run on studio',
  })
  ticketKey = t.ticket.key
  ticketID = t.ticket.id
  remote('decision', 'dr-41', decision('dr-41', '', 'Ship the release made on studio?'))
  remote('pins', 'pins', { v: 1, pins: [{ session: 's-studio', name: 'studio build', agent: 'claude', at: 1 }] })
  // The ticket's own records on studio: a decision and a run naming it.
  remote('decision', 'dr-42', decision('dr-42', t.ticket.id, 'Merge the ticket on studio?'))
  remote('pointer', 'team-0000aa02', { id: 'team-0000aa02', crew: 'team', state: 'running', revision: 3, ticket: t.ticket.id })
})

test.afterAll(async () => {
  if (server) await stopServer(server.pid)
  if (root) rmSync(root, { recursive: true, force: true })
})

/** The store's report the page is told unless a test names its own: current, naming this machine's own device. */
const currentReport = { state: 'current', device: 'dev-here', deviceName: 'here' }

/** The page told a sync store is registered, with its report, before its first load. */
async function withStore(page: Page, report: Record<string, unknown>) {
  await page.route('**/api/capabilities', (route) => route.fulfill({ json: { providers: [{ kind: 'sync', name: 'cloud', state: 'ready' }] } }))
  await page.route('**/api/sync', (route) => route.fulfill({ json: report }))
  await page.route('**/api/remote?*', answerRemote)
}

async function open(page: Page, path: string, report: Record<string, unknown> = currentReport) {
  await page.addInitScript((t) => localStorage.setItem('conductor.workbenchToken', t), server!.token)
  await withStore(page, report)
  await page.goto(`${server!.baseURL}${path}`)
}

/** Every element of the locator inside the page's width: nothing of a row runs off a phone. */
async function inView(page: Page, selector: string) {
  const width = page.viewportSize()!.width
  for (const box of await page.locator(selector).evaluateAll((els) => els.map((e) => e.getBoundingClientRect().toJSON() as DOMRect))) {
    expect(box.left).toBeGreaterThanOrEqual(0)
    expect(box.right).toBeLessThanOrEqual(width + 0.5)
  }
}

for (const theme of ['dark', 'light'] as const) {
  for (const size of [
    { name: 'a desk', width: 1440, height: 900 },
    { name: 'a phone', width: 390, height: 844 },
  ]) {
    test(`another machine's decisions are listed read only on the decision page, ${theme}, on ${size.name}`, async ({ page }) => {
      await page.setViewportSize({ width: size.width, height: size.height })
      await open(page, '/decisions')
      await useTheme(page, theme, '[data-decisions]')
      const row = page.locator('[data-remote-decision="dr-41"]')
      await expect(row).toBeVisible()
      await expect(row.locator('[data-remote-where]')).toHaveText('on studio')
      await expect(row.locator('[data-remote-question]')).toHaveText('Ship the release made on studio?')
      // Read only: no action of this machine's, and no answer page with no store to name one.
      await expect(page.locator('[data-remote-decisions] button')).toHaveCount(0)
      await expect(row.locator('[data-remote-answer]')).toHaveCount(0)
      await inView(page, '[data-remote-decision]')
    })

    test(`a ticket's decisions and runs on another machine are on its page, read only, ${theme}, on ${size.name}`, async ({ page }) => {
      await page.setViewportSize({ width: size.width, height: size.height })
      const [project, n] = ticketKey.split('-')
      await open(page, `/dispatch/${project}/${n}`)
      await useTheme(page, theme, `[data-ticket="${ticketKey}"]`)
      const d = page.locator('[data-remote-decision="dr-42"]')
      await expect(d).toBeVisible()
      await expect(d.locator('[data-remote-where]')).toHaveText('on studio')
      // The decision of no ticket is not this ticket's.
      await expect(page.locator('[data-remote-decision="dr-41"]')).toHaveCount(0)
      const run = page.locator('[data-remote-run="team-0000aa02"]')
      await expect(run.locator('[data-remote-run-words]')).toHaveText('running on studio')
      await expect(run.locator('a, button')).toHaveCount(0)
      await inView(page, '[data-remote-decision], [data-remote-run]')
    })
  }
}

test("another machine's pins are in the sidebar's pinned group, greyed and opening nothing", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await open(page, '/')
  const pin = page.locator('[data-remote-pin="s-studio"]')
  await expect(pin).toBeVisible()
  await expect(pin.locator('[data-pinned-name]')).toHaveText('studio build')
  await expect(pin.locator('[data-remote-where]')).toHaveText('on studio')
  await expect(pin.locator('a')).toHaveCount(0)
  const url = page.url()
  await pin.click()
  expect(page.url()).toBe(url)
})

// The Sync row and the Share control with `sync` in a stubbed capabilities reply and a stubbed report of its own: what a registered sync
// store would draw. The store itself is not there: Share is refused (no_sync) and says so.
test("with a sync store's report, Settings says its state and what was kept, and a project offers Share", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await open(page, '/settings', {
    state: 'behind',
    waiting: 37,
    withheld: [{ ref: "RMT-1's comment", words: 'quotes captured output' }],
    here: { cut: 2 },
  })
  const about = page.locator('[data-about-card]')
  await expect(about.locator('[data-about-row="sync"]')).toContainText('behind: 37 changes waiting')
  await expect(about.locator('[data-about-sync-kept] li')).toHaveText([
    "RMT-1's comment: quotes captured output",
    '2 decisions have parts cut as output: a person elsewhere sees "[output kept on this machine]" in their place',
  ])
  await page.goto(`${server!.baseURL}/dispatch/RMT`)
  const share = page.locator('[data-project-share]')
  await expect(share.locator('[data-project-share-words]')).toHaveText('RMT is on this machine alone.')
  await share.locator('[data-project-share-button]').click()
  await expect(share.locator('[data-project-share-failure]')).toContainText('no sync provider is registered')
})
