import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { Api, expect, test } from './fixtures'
import { renderConfig, START_WAIT_MS, startServer, stopServer } from './server'

// The free build (docs/plans/sync-seams.md, OS-4): with no sync store registered, no Share control on a project's page, no Sync row
// in Settings → The app, and no other machine's rows on the decision page, a ticket's page or the sidebar, a remote/ folder a store
// registered once left in the data directory included: the app asks nothing of it and the server reads none of it.

test('a server with no sync store draws no Share control, no Sync row and no remote rows', async ({ page, api }) => {
  await api.ok('POST', '/api/dispatch/projects', { opId: `sf-${Date.now()}`, key: 'SFREE', name: 'Sync free' }).catch(() => undefined)
  const t = await api.ok<{ ticket: { key: string } }>('POST', '/api/dispatch/tickets', {
    opId: `sft-${Date.now()}`,
    project: 'SFREE',
    title: 'Nothing elsewhere',
  })
  await page.setViewportSize({ width: 1440, height: 900 })

  await page.goto('/dispatch/SFREE')
  await expect(page.locator('[data-project-settings-open]')).toBeVisible()
  await expect(page.locator('[data-new-ticket-open]')).toBeEnabled()
  await expect(page.locator('[data-project-share]')).toHaveCount(0)

  const [project, n] = t.ticket.key.split('-')
  await page.goto(`/dispatch/${project}/${n}`)
  await expect(page.locator(`[data-ticket="${t.ticket.key}"]`)).toBeVisible()
  await expect(page.locator('[data-remote-decisions], [data-remote-runs], [data-ticket-also-linked]')).toHaveCount(0)

  await page.goto('/decisions')
  await expect(page.locator('[data-decisions]')).toBeVisible()
  await expect(page.locator('[data-remote-decisions]')).toHaveCount(0)
  await expect(page.locator('[data-remote-pin]')).toHaveCount(0)

  await page.goto('/settings')
  await expect(page.locator('[data-about-card]')).toBeVisible()
  await expect(page.locator('[data-about-row="sync"]')).toHaveCount(0)
})

test.describe('a remote/ folder left in the data directory', () => {
  let root = ''
  let server: Awaited<ReturnType<typeof startServer>> | undefined
  let ticketKey = ''
  const hex = (s: string) => Buffer.from(s, 'utf8').toString('hex')

  test.beforeAll(async () => {
    test.setTimeout(START_WAIT_MS + 30_000)
    const here = dirname(fileURLToPath(import.meta.url))
    root = mkdtempSync(join(tmpdir(), 'conductor-e2e-sync-free-'))
    const home = join(root, 'home')
    const data = join(root, 'data')
    for (const d of [home, data]) mkdirSync(d, { recursive: true })
    // What a sync store registered once wrote: another machine's decision, open, and its pins.
    const dir = join(data, 'remote', hex('dev-studio'))
    mkdirSync(dir, { recursive: true })
    const at = '2026-11-20T09:00:00Z'
    const write = (kind: string, id: string, body: unknown) =>
      writeFileSync(join(dir, `${kind}-${hex(id)}.json`), JSON.stringify({ kind, device: 'dev-studio', deviceName: 'studio', id, rev: 1, at, data: body }))
    write('decision', 'dr-41', {
      id: 'dr-41',
      run: 'team-0000aa01',
      openedIn: 'team-0000aa01',
      origin: 'plan',
      type: 'approval',
      gate: 'ship',
      request: { question: 'Ship the release made on studio?', options: [{ label: 'Approve' }, { label: 'Reject' }] },
      requestSHA256: 'a'.repeat(64),
      openedTo: 'operator',
      policy: {},
      state: 'open',
      rev: 1,
      createdAt: at,
      states: { open: true },
    })
    write('pins', 'pins', { v: 1, pins: [{ session: 's-studio', name: 'studio build', agent: 'claude', at: 1 }] })
    const config = join(root, 'conductor.json')
    renderConfig(join(here, 'conductor.e2e.json'), config)
    server = await startServer({ config, home, data, allowedRoot: root, defaultCwd: root, log: join(root, 'server.log') })
    const api = new Api(server)
    await api.ok('POST', '/api/dispatch/projects', { opId: 'p1', key: 'LEFT', name: 'Left over' })
    ticketKey = (await api.ok<{ ticket: { key: string } }>('POST', '/api/dispatch/tickets', { opId: 't1', project: 'LEFT', title: 'Here alone' })).ticket.key
  })

  test.afterAll(async () => {
    if (server) await stopServer(server.pid)
    if (root) rmSync(root, { recursive: true, force: true })
  })

  test("shows nothing of it, and the app asks for none of it", async ({ page }) => {
    const asked: string[] = []
    page.on('request', (r) => {
      if (new URL(r.url()).pathname === '/api/remote') asked.push(r.url())
    })
    await page.addInitScript((t) => localStorage.setItem('conductor.workbenchToken', t), server!.token)
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto(`${server!.baseURL}/decisions`)
    await expect(page.locator('[data-decisions]')).toBeVisible()
    await expect(page.locator('#dashboard-sidebar-main')).toBeVisible()
    await expect(page.locator('[data-remote-decisions], [data-remote-decision]')).toHaveCount(0)
    await expect(page.locator('[data-remote-pin]')).toHaveCount(0)
    const [project, n] = ticketKey.split('-')
    await page.goto(`${server!.baseURL}/dispatch/${project}/${n}`)
    await expect(page.locator(`[data-ticket="${ticketKey}"]`)).toBeVisible()
    await expect(page.locator('[data-remote-decisions], [data-remote-runs]')).toHaveCount(0)
    expect(asked).toEqual([])
    // And the server, asked, answers none of it.
    const reply = await page.evaluate(async (t) => {
      const r = await fetch('/api/remote', { headers: { Authorization: `Bearer ${t}` } })
      return (await r.json()) as { records: unknown[] }
    }, server!.token)
    expect(reply.records).toEqual([])
  })
})
