import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, test } from './fixtures'
import { renderConfig, START_WAIT_MS, startServer, stopServer } from './server'

// The free build's Hosts page (docs/plans/hosts.md, PR-5; articles 2 and 7): with no sync store registered, GET /api/hosts carries
// no `other` field and the page says nothing of other machines, no section and no line, a remote/ folder a store registered once
// left in the data directory included.

test('a server with no sync store says nothing of other machines on its Hosts page', async ({ page, state }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto('/hosts')
  await expect(page.locator('[data-hosts-section="this"] [data-host-card]')).toBeVisible()
  await expect(page.locator('[data-hosts-section="runs"]')).toBeVisible()
  await expect(page.locator('[data-hosts-section="other"]')).toHaveCount(0)
  await expect(page.locator('[data-hosts]')).not.toContainText('Other machines')
  const reply = await page.evaluate(async (t) => (await (await fetch('/api/hosts', { headers: { Authorization: `Bearer ${t}` } })).json()) as Record<string, unknown>, state.token)
  expect(reply.this).toBeTruthy()
  expect('other' in reply).toBe(false)
})

test.describe('a remote/ folder left in the data directory', () => {
  let root = ''
  let server: Awaited<ReturnType<typeof startServer>> | undefined
  const hex = (s: string) => Buffer.from(s, 'utf8').toString('hex')

  test.beforeAll(async () => {
    test.setTimeout(START_WAIT_MS + 30_000)
    const here = dirname(fileURLToPath(import.meta.url))
    root = mkdtempSync(join(tmpdir(), 'conductor-e2e-hosts-free-'))
    const home = join(root, 'home')
    const data = join(root, 'data')
    for (const d of [home, data]) mkdirSync(d, { recursive: true })
    // What a sync store registered once wrote: another server device of the workspace and its host summary.
    const dir = join(data, 'remote', hex('dev-hs2free'))
    mkdirSync(dir, { recursive: true })
    const at = '2026-11-20T09:00:00Z'
    const write = (kind: string, id: string, body: unknown) =>
      writeFileSync(join(dir, `${kind}-${hex(id)}.json`), JSON.stringify({ kind, device: 'dev-hs2free', deviceName: 'hs2free-studio', id, rev: 1, at, data: body }))
    write('device', 'dev-hs2free', { id: 'dev-hs2free', name: 'hs2free-studio', kind: 'server', platform: 'darwin/arm64', version: 'v0.7.0', lastReport: at })
    write('hosts', 'summary', { platform: 'darwin/arm64', version: 'v0.7.0', sessions: { running: 1, waiting: 0 }, runs: { held: 0, waiting: 0 }, hosts: [] })
    const config = join(root, 'conductor.json')
    renderConfig(join(here, 'conductor.e2e.json'), config)
    server = await startServer({ config, home, data, allowedRoot: root, defaultCwd: root, log: join(root, 'server.log') })
  })

  test.afterAll(async () => {
    if (server) await stopServer(server.pid)
    if (root) rmSync(root, { recursive: true, force: true })
  })

  test('shows nothing of it: no section, no line, and no other field', async ({ page }) => {
    await page.addInitScript((t) => localStorage.setItem('conductor.workbenchToken', t), server!.token)
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto(`${server!.baseURL}/hosts`)
    await expect(page.locator('#dashboard-sidebar-main')).toBeVisible()
    await expect(page.locator('[data-hosts-section="this"] [data-host-card]')).toBeVisible()
    await expect(page.locator('[data-hosts-section="other"]')).toHaveCount(0)
    await expect(page.locator('[data-hosts]')).not.toContainText('hs2free-studio')
    const reply = await page.evaluate(async (t) => (await (await fetch('/api/hosts', { headers: { Authorization: `Bearer ${t}` } })).json()) as Record<string, unknown>, server!.token)
    expect('other' in reply).toBe(false)
    expect(JSON.stringify(reply)).not.toContain('hs2free')
    await page.context().close()
  })
})
