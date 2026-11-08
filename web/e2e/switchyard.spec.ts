import { hostname } from 'node:os'
import { spawn, type ChildProcess } from 'node:child_process'
import { randomBytes, randomUUID } from 'node:crypto'
import { existsSync, mkdirSync, openSync, readdirSync, readFileSync } from 'node:fs'
import { createServer } from 'node:net'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, task, test, type Run, type Session } from './fixtures'
import { renderConfig, scratchRepo, serverBinary, stubBash, startServer, stopServer, stubPath, type Started } from './server'

// A switchyard: a second Conductor in switchyard mode, a conductor host
// publishing a stub session to it with a link minted there over its own
// connection (--link: the switchyard's workbench token mints none), and this
// workbench's own join page signalling to the switchyard from its own origin
// (what the desktop app does for an invite). The terminal goes through the
// switchyard's relay (the host runs relay-only here, as a CI box may have no
// route for ICE).
test.describe.configure({ mode: 'serial' })

let switchyard: ChildProcess | null = null
let host: ChildProcess | null = null
let syURL = ''
const syAdmin = randomBytes(16).toString('hex')
const hostToken = 'sy-host-' + randomBytes(8).toString('hex')
const agentSession = randomUUID()
let home = ''
/** The link the conductor host had minted at the switchyard (--link control), from its terminal output. */
let hostLink = ''
/** A second server of the suite's shape, publishing to the switchyard: the crew run link test's. */
let publisher: Started | null = null

function pickPort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const s = createServer()
    s.listen(0, '127.0.0.1', () => {
      const port = (s.address() as { port: number }).port
      s.close(() => resolve(port))
    })
    s.on('error', reject)
  })
}

async function until(what: string, cond: () => Promise<boolean>, ms = 30_000) {
  const end = Date.now() + ms
  while (Date.now() < end) {
    if (await cond().catch(() => false)) return
    await new Promise((r) => setTimeout(r, 200))
  }
  throw new Error(`timed out waiting: ${what}`)
}

test.beforeAll(async ({ state }) => {
  const port = await pickPort()
  syURL = `http://127.0.0.1:${port}`
  home = join(state.root, 'switchyard-home')
  const data = join(state.root, 'switchyard-data')
  mkdirSync(home, { recursive: true })
  mkdirSync(data, { recursive: true })
  const log = openSync(join(state.root, 'switchyard.log'), 'a')
  switchyard = spawn(serverBinary(), ['switchyard', '--listen', `127.0.0.1:${port}`], {
    env: { PATH: process.env.PATH ?? '/usr/bin:/bin', LANG: 'C.UTF-8', HOME: home, CONDUCTOR_DATA_DIR: data, CONDUCTOR_PUBLIC_URL: syURL, CONDUCTOR_WORKBENCH_TOKEN: syAdmin, CONDUCTOR_HOST_TOKENS: hostToken, CONDUCTOR_ALLOWED_ROOTS: home, CONDUCTOR_REACH: 'off', CONDUCTOR_RENDEZVOUS: '0' },
    stdio: ['ignore', log, log],
  })
  await until('the switchyard answers', async () => {
    const r = await fetch(`${syURL}/api/health`)
    const h = (await r.json()) as { switchyard?: boolean }
    return r.ok && h.switchyard === true
  })
  // A hosted stub published to the switchyard, relay-only, headless.
  const bash = stubBash()
  host = spawn(serverBinary(), ['host', '--server', syURL, '--token', hostToken, '--relay-only', '--no-local', '--name', 'hosted-stub', '--cwd', home, '--link', 'control', '--', bash.command, stubPath, '--session-id', agentSession], {
    env: { PATH: bash.path, LANG: 'C.UTF-8', HOME: home, STUB_IDENTITY: 'claude', STUB_REPORT: 'plain' },
    stdio: ['ignore', log, log],
  })
  await until('the hosted session is listed', async () => {
    const r = await fetch(`${syURL}/api/sessions`, { headers: { Authorization: `Bearer ${syAdmin}` } })
    const list = (await r.json()) as { sessions?: Array<{ name: string }> } | Array<{ name: string }>
    const sessions = Array.isArray(list) ? list : (list.sessions ?? [])
    return sessions.some((s) => s.name === 'hosted-stub')
  })
  await until('the host prints its link', async () => {
    const m = /conductor: share link control (\S+)/.exec(readFileSync(join(state.root, 'switchyard.log'), 'utf8'))
    hostLink = m?.[1] ?? ''
    return hostLink !== ''
  })
})

test.afterAll(async ({ state }) => {
  if (publisher) await stopServer(publisher.pid)
  host?.kill('SIGKILL')
  switchyard?.kill('SIGKILL')
  // The log stays readable in the run's output: the scratch root goes with the teardown.
  const log = join(state.root, 'switchyard.log')
  if (existsSync(log)) console.log('switchyard log:\n' + readFileSync(log, 'utf8').split('\n').slice(-40).join('\n'))
})

test('a link minted on the switchyard opens on this workbench with ?server=, and the terminal reaches the hosted session', async ({ page }) => {
  const r = await fetch(`${syURL}/api/sessions`, { headers: { Authorization: `Bearer ${syAdmin}` } })
  const list = (await r.json()) as { sessions?: Array<{ id: string; name: string; kind: string }> } | Array<{ id: string; name: string; kind: string }>
  const sessions = Array.isArray(list) ? list : (list.sessions ?? [])
  const hosted = sessions.find((s) => s.name === 'hosted-stub')!
  expect(hosted.kind).toBe('hosted')
  // The link the host had minted over its own connection; the operator's token mints none.
  expect(hostLink.startsWith(`${syURL}/join/`), hostLink).toBe(true)
  const link = { token: hostLink.slice(`${syURL}/join/`.length) }
  const refused = await fetch(`${syURL}/api/sessions/${hosted.id}/links`, { method: 'POST', headers: { Authorization: `Bearer ${syAdmin}`, 'Content-Type': 'application/json' }, body: JSON.stringify({ role: 'control', label: 'e2e invite' }) })
  expect(refused.status).toBe(403)
  expect(((await refused.json()) as { error: { code: string } }).error.code).toBe('switchyard')

  // This workbench's own join page, told to signal to the switchyard: the join route answers it across origins.
  await page.goto(`/join/${link.token}?server=${encodeURIComponent(syURL)}`)
  await expect(page.getByText(`Shared through 127.0.0.1:${new URL(syURL).port}`)).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Join hosted-stub' })).toBeVisible({ timeout: 15_000 })
  // Opened from the workbench (its token is in this browser), the page sits beside the sidebar.
  await expect(page.locator('[data-join-frame]')).toHaveAttribute('data-join-frame', 'workbench')
  await page.getByRole('textbox', { name: 'Your name' }).fill('e2e guest')
  await page.getByRole('button', { name: 'Join session' }).click()
  const badge = page.locator('[data-transport-state]').first()
  await expect(badge).toHaveAttribute('data-transport-state', 'open', { timeout: 30_000 })
  await expect(badge).toHaveAttribute('data-transport-kind', 'relay')
  const screen = page.locator('.terminal-host .xterm-screen').first()
  await expect(screen).toBeVisible({ timeout: 30_000 })
  await expect
    .poll(async () => {
      const list2 = (await (await fetch(`${syURL}/api/sessions`, { headers: { Authorization: `Bearer ${syAdmin}` } })).json()) as { sessions?: Array<{ id: string; viewers?: number }> } | Array<{ id: string; viewers?: number }>
      const s = (Array.isArray(list2) ? list2 : (list2.sessions ?? [])).find((x) => x.id === hosted.id)
      return s?.viewers ?? 0
    }, { timeout: 30_000, message: 'the switchyard counts the viewer' })
    .toBeGreaterThan(0)
  await page.locator('.terminal-host .xterm-helper-textarea').first().focus()
  await page.keyboard.type('hello through the switchyard')
  await page.keyboard.press('Enter')
  const transcript = join(home, '.stub-sessions', `${agentSession}.txt`)
  await expect.poll(() => (existsSync(transcript) ? readFileSync(transcript, 'utf8') : ''), { timeout: 20_000, message: 'the hosted stub got the line' }).toContain('hello through the switchyard')

  // Leave brings the card back, the name kept, and the switchyard no longer counts the viewer; the link still joins.
  await page.locator('[data-join-leave]').click()
  await expect(page.locator('[data-join-left]')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Join session' })).toBeVisible()
  await expect(page.getByRole('textbox', { name: 'Your name' })).toHaveValue('e2e guest')
  await expect(page.locator('.terminal-host')).toHaveCount(0)
  await expect
    .poll(async () => {
      const list3 = (await (await fetch(`${syURL}/api/sessions`, { headers: { Authorization: `Bearer ${syAdmin}` } })).json()) as { sessions?: Array<{ id: string; viewers?: number }> } | Array<{ id: string; viewers?: number }>
      const s = (Array.isArray(list3) ? list3 : (list3.sessions ?? [])).find((x) => x.id === hosted.id)
      return s?.viewers ?? 0
    }, { timeout: 30_000, message: 'the switchyard drops the viewer' })
    .toBe(0)

  // The link stays under Shared with you after Leave, on any page; opening it joins at once; × forgets it.
  const shared = page.locator('[data-sidebar-shared] [data-shared-entry]')
  await expect(shared).toHaveCount(1)
  await expect(shared).toContainText('hosted-stub')
  await expect(shared).toContainText(`through 127.0.0.1:${new URL(syURL).port}`)
  await page.goto('/yard')
  await expect(shared).toHaveCount(1)
  await shared.getByRole('link').click()
  await expect(page).toHaveURL(new RegExp(`/join/${link.token}\\?server=`))
  await expect(page.locator('[data-transport-state]').first()).toHaveAttribute('data-transport-state', 'open', { timeout: 30_000 })
  await expect(page.getByRole('button', { name: 'Join session' })).toHaveCount(0)
  await page.locator('[data-shared-forget]').click()
  await expect(page.locator('[data-sidebar-shared]')).toHaveCount(0)
  expect(await page.evaluate(() => localStorage.getItem('conductor.joined'))).toBe('[]')
})

test('the switchyard serves its landing page, a 404 for workbench paths, and the app only for joining', async () => {
  const port = new URL(syURL).port
  const landing = await fetch(`${syURL}/`)
  const body = await landing.text()
  expect(landing.status).toBe(200)
  expect(body).toContain('This is a Conductor switchyard.')
  expect(body).toContain(`conductor://127.0.0.1:${port}/join/`)
  expect(body).toContain('Off · plain http')
  // The sponsor kit's badge at the footer's lower right, its logo served beside the app.
  expect(body).toContain('<a class="right badge" href="https://rocksolidlabs.io" data-credit><span>Sponsored by</span><img src="/sponsor/rocksolidlabs-logo-reversed.png" alt="RockSolid Labs"')
  const logo = await fetch(`${syURL}/sponsor/rocksolidlabs-logo-reversed.png`)
  expect(logo.status).toBe(200)
  expect(logo.headers.get('content-type')).toContain('image/png')
  for (const p of ['/crews', '/sessions/x', '/wall', '/yard', '/roundhouse', '/agents', '/events', '/settings']) {
    const r = await fetch(`${syURL}${p}`)
    expect(r.status, p).toBe(404)
    expect(await r.text(), p).toContain("The workbench isn't here.")
  }
  const join = await fetch(`${syURL}/join/not-a-token`)
  expect(join.status).toBe(200)
  const joinBody = await join.text()
  expect(joinBody).toMatch(/__nuxt|\/_nuxt\//)
  expect(joinBody).not.toContain("The workbench isn't here.")
  const status = await fetch(`${syURL}/api/switchyard/status`, { headers: { Authorization: `Bearer ${syAdmin}` } })
  expect(status.status).toBe(200)
  const figures = (await status.json()) as { hosts: number; sessions: number; hostList: Array<{ name: string }> }
  expect(figures.hosts).toBe(1)
  expect(figures.sessions).toBe(1)
  expect(figures.hostList[0]?.name).toBe(hostname()) // the publishing machine, not the session's name
  expect((await fetch(`${syURL}/api/switchyard/status`)).status).toBe(401)
})

test('the switchyard launches nothing, and its join route answers no stranger origin', async () => {
  const r = await fetch(`${syURL}/api/sessions`, { method: 'POST', headers: { Authorization: `Bearer ${syAdmin}`, 'Content-Type': 'application/json' }, body: JSON.stringify({ agentId: 'claude' }) })
  expect(r.status).toBe(403)
  expect(((await r.json()) as { error: { code: string } }).error.code).toBe('switchyard')
  const made = await fetch(`${syURL}/api/health`, { headers: { Origin: 'https://evil.example' } })
  expect(made.headers.get('access-control-allow-origin')).toBeNull()
})

// A crew run on a server that publishes to the switchyard: its Share mints
// the link there (link_run), the switchyard's join lists the members, a
// member's terminal comes through the relay, and a revoke from the home
// server takes the link away there. The Go loopback test covers the
// protocol; this is what a person sees.
test('a crew run published to the switchyard is shared through it: Share says works from anywhere, the link lists the members there, and a revoke ends it', async ({ page, browser, state }) => {
  test.setTimeout(150_000)
  const here = dirname(fileURLToPath(import.meta.url))
  const home2 = join(state.root, 'publisher-home')
  const data2 = join(state.root, 'publisher-data')
  const repo2 = join(state.root, 'publisher-repo')
  for (const d of [home2, data2, repo2]) mkdirSync(d, { recursive: true })
  scratchRepo(repo2)
  const config = join(state.root, 'publisher.json')
  renderConfig(join(here, 'conductor.e2e.json'), config)
  publisher = await startServer({
    config,
    home: home2,
    data: data2,
    allowedRoot: state.root,
    defaultCwd: repo2,
    log: join(state.root, 'publisher.log'),
    env: { CONDUCTOR_RENDEZVOUS: '1', CONDUCTOR_RENDEZVOUS_SERVER: syURL, CONDUCTOR_RENDEZVOUS_TOKEN: hostToken, CONDUCTOR_RENDEZVOUS_RELAY_ONLY: '1', CONDUCTOR_RENDEZVOUS_HOST_NAME: 'publisher' },
  })
  const pub = async <T>(method: string, path: string, body?: unknown): Promise<{ status: number; body: T }> => {
    const r = await fetch(publisher!.baseURL + path, { method, headers: { Authorization: `Bearer ${publisher!.token}`, 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) })
    const text = await r.text()
    return { status: r.status, body: (text ? JSON.parse(text) : undefined) as T }
  }
  const crew = await pub<{ crew: { id: string } }>('POST', '/api/crews', {
    name: 'e2e crew link',
    goal: 'be shared',
    cwd: '',
    where: 'server',
    isolation: 'none',
    openAfterLaunch: false,
    members: [
      { name: 'lead', agentId: 'claude', prompt: 'say hello', start: { when: 'immediately' } },
      { name: 'review', agentId: 'claude', prompt: 'review it', start: { when: 'immediately' } },
    ],
  })
  expect(crew.status).toBe(201)
  const launched = await pub<{ run: { id: string } }>('POST', `/api/crews/${crew.body.crew.id}/launch`)
  expect(launched.status).toBe(201)
  const runId = launched.body.run.id
  await until('both members run', async () => {
    const r = await pub<{ run: { members: Array<{ status: string }> } }>('GET', `/api/runs/${runId}`)
    return r.body.run.members.length === 2 && r.body.run.members.every((m) => m.status === 'running')
  })

  // Share on the run page of the publisher's own workbench: minted at the switchyard, and a control link on request.
  const ctx = await browser.newContext()
  await ctx.addInitScript((t) => localStorage.setItem('conductor.workbenchToken', t), publisher.token)
  const owner = await ctx.newPage()
  await owner.goto(`${publisher.baseURL}/runs/${runId}`)
  await owner.getByRole('button', { name: 'Share', exact: true }).first().click()
  const dialog = owner.getByRole('dialog')
  await expect(dialog.locator('[data-created-url]')).toBeVisible({ timeout: 30_000 })
  await expect(dialog.locator('[data-link-reach]')).toHaveAttribute('data-link-reach', 'remote')
  await expect(dialog).toContainText('Works from anywhere')
  await expect(dialog).toContainText('opens every agent of the crew there')
  await dialog.locator('[data-share-role-option="control"]').click()
  await expect(dialog.locator('[data-share-link]').first()).toContainText('Control', { timeout: 30_000 })
  const url = (await dialog.locator('[data-created-url]').getAttribute('title')) ?? ''
  expect(url.startsWith(`${syURL}/join/`), url).toBe(true)
  const token = url.slice(`${syURL}/join/`.length)
  await ctx.close()

  // The link joins through the switchyard from this workbench: the crew's members, one opened, its terminal over the relay.
  const port = new URL(syURL).port
  await page.goto(`/join/${token}?server=${encodeURIComponent(syURL)}`)
  await expect(page.getByRole('heading', { name: 'Join e2e crew link' })).toBeVisible({ timeout: 15_000 })
  await expect(page.getByText(`Shared through 127.0.0.1:${port}`)).toBeVisible()
  await page.getByRole('textbox', { name: 'Your name' }).fill('e2e crew guest')
  await page.getByRole('button', { name: 'Join crew' }).click()
  await expect(page.locator('[data-join-tiles] [data-member]')).toHaveCount(2)
  await expect(page.locator('[data-join-tiles] [data-member="lead"]')).toBeVisible()
  await expect(page.locator('[data-join-tiles] [data-member="review"]')).toBeVisible()
  await page.locator('[data-join-tiles] [data-member="lead"] [data-tile-open]').click()
  const badge = page.locator('[data-transport-state]').first()
  await expect(badge).toHaveAttribute('data-transport-state', 'open', { timeout: 30_000 })
  await expect(badge).toHaveAttribute('data-transport-kind', 'relay')
  await expect(page.locator('.terminal-host .xterm-screen').first()).toBeVisible({ timeout: 30_000 })
  await page.locator('.terminal-host .xterm-helper-textarea').first().focus()
  await page.keyboard.type('hello crew through the switchyard')
  await page.keyboard.press('Enter')
  const transcripts = join(home2, '.stub-sessions')
  await expect
    .poll(() => existsSync(transcripts) && readdirSync(transcripts).some((f) => f.endsWith('.txt') && readFileSync(join(transcripts, f), 'utf8').includes('hello crew through the switchyard')), { timeout: 20_000, message: 'the member got the line' })
    .toBe(true)

  // Revoked at home, the link is gone at the switchyard and the viewer is cut off.
  const links = await pub<{ links: Array<{ id: string; remote?: boolean; runId?: string }> }>('GET', `/api/runs/${runId}/links`)
  expect(links.body.links.length).toBeGreaterThan(0)
  expect(links.body.links.every((l) => l.remote === true)).toBe(true)
  for (const l of links.body.links) expect((await pub('DELETE', `/api/runs/${runId}/links/${l.id}`)).status).toBe(204)
  await expect.poll(async () => (await fetch(`${syURL}/api/join/${token}`)).status, { timeout: 15_000 }).toBe(404)
  await expect.poll(() => badge.getAttribute('data-transport-state'), { timeout: 30_000 }).not.toBe('open')
  await pub('POST', `/api/runs/${runId}/stop`)
  await stopServer(publisher.pid)
  publisher = null
})

// A session a Conductor publishes to the switchyard: the switchyard lists it
// by its name, its agent, its folder's last element and the machine, never by
// its command line; the owner's Share mints the link there; a guest's Files
// pane roots at the folder the session's own welcome names, not at what the
// switchyard lists. A stop from the switchyard's operator ends the
// publication: the guest is let go, the session runs on at home and its
// Activity says so, and the owner's next Share publishes it again.
test('a session a Conductor publishes is listed there by little, a guest\'s Files pane roots at its folder, and a stop there ends only the publication', async ({ page, browser, state }) => {
  test.setTimeout(150_000)
  const here = dirname(fileURLToPath(import.meta.url))
  const home3 = join(state.root, 'publisher3-home')
  const data3 = join(state.root, 'publisher3-data')
  const repo3 = join(state.root, 'publisher3-repo')
  for (const d of [home3, data3, repo3]) mkdirSync(d, { recursive: true })
  scratchRepo(repo3)
  const config = join(state.root, 'publisher3.json')
  renderConfig(join(here, 'conductor.e2e.json'), config)
  publisher = await startServer({
    config,
    home: home3,
    data: data3,
    allowedRoot: state.root,
    defaultCwd: repo3,
    log: join(state.root, 'publisher3.log'),
    env: { CONDUCTOR_RENDEZVOUS: '1', CONDUCTOR_RENDEZVOUS_SERVER: syURL, CONDUCTOR_RENDEZVOUS_TOKEN: hostToken, CONDUCTOR_RENDEZVOUS_RELAY_ONLY: '1', CONDUCTOR_RENDEZVOUS_HOST_NAME: 'publisher3' },
  })
  const pub = async <T>(method: string, path: string, body?: unknown): Promise<{ status: number; body: T }> => {
    const r = await fetch(publisher!.baseURL + path, { method, headers: { Authorization: `Bearer ${publisher!.token}`, 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) })
    const text = await r.text()
    return { status: r.status, body: (text ? JSON.parse(text) : undefined) as T }
  }
  const made = await pub<{ id: string }>('POST', '/api/sessions', { agentId: 'claude', name: 'published-stub', cwd: repo3 })
  expect(made.status).toBe(201)
  type Listed = { id: string; name: string; agentId: string; hostName: string; hostUser?: string; cwd: string; command: string[]; status: string }
  const listed = async () => {
    const r = await fetch(`${syURL}/api/sessions`, { headers: { Authorization: `Bearer ${syAdmin}` } })
    const list = (await r.json()) as { sessions?: Listed[] } | Listed[]
    return (Array.isArray(list) ? list : (list.sessions ?? [])).find((s) => s.hostName === 'publisher3' && s.status === 'running')
  }
  await until('the switchyard lists the published session', async () => !!(await listed()))
  const there = (await listed())!
  expect(there.name).toBe('published-stub')
  expect(there.agentId).toBe('claude')
  expect(there.cwd).toBe('publisher3-repo')
  expect(there.command).toEqual(['claude'])
  expect(there.hostUser ?? '').toBe('')

  // Share at home mints the link at the switchyard.
  const share = await pub<{ url: string; remote?: boolean }>('POST', `/api/sessions/${made.body.id}/links`, { role: 'view', label: 'e2e files guest' })
  expect(share.status).toBe(201)
  expect(share.body.remote).toBe(true)
  expect(share.body.url.startsWith(`${syURL}/join/`), share.body.url).toBe(true)
  const token = share.body.url.slice(`${syURL}/join/`.length)

  await page.goto(`/join/${token}?server=${encodeURIComponent(syURL)}`)
  await expect(page.getByRole('heading', { name: 'Join published-stub' })).toBeVisible({ timeout: 15_000 })
  await page.getByRole('textbox', { name: 'Your name' }).fill('e2e files guest')
  await page.getByRole('button', { name: 'Join session' }).click()
  await expect(page.locator('[data-transport-state]').first()).toHaveAttribute('data-transport-state', 'open', { timeout: 30_000 })
  const pane = page.locator('[data-files-mode]').first()
  await expect(pane.locator(`[data-file-node="${repo3}/README.md"]`)).toBeVisible({ timeout: 30_000 })
  await expect(pane.locator('[data-files-crumbs] [data-files-crumb]').last()).toHaveText('publisher3-repo')
  await expect(pane.locator(`[data-files-crumbs] [data-files-crumb="${repo3}"]`)).toHaveCount(1)

  // The owner's Share before the stop: a link minted there, which the dialog keeps for its next open.
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  await ctx.addInitScript((t) => localStorage.setItem('conductor.workbenchToken', t), publisher.token)
  const owner = await ctx.newPage()
  try {
    await owner.goto(`${publisher.baseURL}/sessions/${made.body.id}`)
    await owner.getByRole('button', { name: 'Share', exact: true }).first().click()
    const dialog = owner.getByRole('dialog')
    await expect(dialog.locator('[data-created-url]')).toBeVisible({ timeout: 30_000 })
    await expect(dialog.locator('[data-link-reach]')).toHaveAttribute('data-link-reach', 'remote')
    const before = await dialog.locator('[data-created-url]').getAttribute('title')
    await owner.keyboard.press('Escape')
    await expect(dialog).toHaveCount(0)

    // The operator stops it at the switchyard: the guest is let go, the session runs on at home.
    const stopped = await fetch(`${syURL}/api/sessions/${there.id}`, { method: 'DELETE', headers: { Authorization: `Bearer ${syAdmin}` } })
    expect(stopped.status).toBe(200)
    await expect.poll(() => page.locator('[data-transport-state]').first().getAttribute('data-transport-state'), { timeout: 30_000 }).not.toBe('open')
    const home = await pub<{ session: { status: string } }>('GET', `/api/sessions/${made.body.id}`)
    expect(home.body.session.status).toBe('running')
    await owner.locator('[data-inspector] [data-rail-item="activity"]').click()
    await expect(owner.locator('[data-inspector]')).toContainText('no longer shared through the switchyard', { timeout: 30_000 })
    // Share again, in the same page: a new link, which publishes it again there as a new session.
    await owner.getByRole('button', { name: 'Share', exact: true }).first().click()
    await expect(dialog.locator('[data-created-url]')).toBeVisible({ timeout: 30_000 })
    await expect(dialog.locator('[data-link-reach]')).toHaveAttribute('data-link-reach', 'remote')
    await expect(dialog.locator('[data-created-url]')).not.toHaveAttribute('title', before ?? '')
    await expect.poll(async () => (await listed())?.id ?? '', { timeout: 30_000 }).not.toBe(there.id)
  } finally {
    await ctx.close()
  }
  await pub('DELETE', `/api/sessions/${made.body.id}`)
  await stopServer(publisher.pid)
  publisher = null
})

// A decision link through the switchyard (docs/plans/decision-inbox.md, MB0 PR-7): a server publishing to the spec's switchyard
// (relay-only, as the host above: a CI box may have no route for ICE, so every connection goes through the switchyard's relay) runs
// a managed run of one member whose task fails its check; its member's session, which outlives the failed attempt, then asks a
// question (through the attention route: the stub asks only before its task line, which answers it). The owner's Share makes a
// control link that answers decisions, minted there over the run's desk. A phone opens it at the switchyard's own join page and,
// over the desk: sees both items; opens the member's terminal from the question's evidence, one tap; answers the question, its
// steps through to resumed; then, the member's session ended (no member of the run lives), unfolds the failed check's log, retries
// the task, its steps through to resumed, and reads them again from the link's Recent once the item has left the open list. Each
// item is found by its ref. Each step raises the test's timeout by its own waits' caps as it takes them (budget).
test("a run decision link through the switchyard: a phone answers a member's question and retries a failed task with no member alive, over the run's desk", async ({ browser, state }) => {
  const STEP = 30_000
  const budget = (waits: number, ms = STEP) => test.info().setTimeout(test.info().timeout + waits * ms)
  budget(2)
  const here = dirname(fileURLToPath(import.meta.url))
  // Its own folders each time it runs (a repeat of it included): the scratch repository is made once per folder.
  const tag = randomBytes(4).toString('hex')
  const home4 = join(state.root, `publisher4-${tag}-home`)
  const data4 = join(state.root, `publisher4-${tag}-data`)
  const repo4 = join(state.root, `publisher4-${tag}-repo`)
  for (const d of [home4, data4, repo4]) mkdirSync(d, { recursive: true })
  scratchRepo(repo4)
  const config = join(state.root, `publisher4-${tag}.json`)
  renderConfig(join(here, 'conductor.e2e.json'), config)
  publisher = await startServer({
    config,
    home: home4,
    data: data4,
    allowedRoot: state.root,
    defaultCwd: repo4,
    log: join(state.root, `publisher4-${tag}.log`),
    env: { CONDUCTOR_RENDEZVOUS: '1', CONDUCTOR_RENDEZVOUS_SERVER: syURL, CONDUCTOR_RENDEZVOUS_TOKEN: hostToken, CONDUCTOR_RENDEZVOUS_RELAY_ONLY: '1', CONDUCTOR_RENDEZVOUS_HOST_NAME: 'publisher4' },
  })
  const pub = async <T>(method: string, path: string, body?: unknown): Promise<{ status: number; body: T }> => {
    const r = await fetch(publisher!.baseURL + path, { method, headers: { Authorization: `Bearer ${publisher!.token}`, 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) })
    const text = await r.text()
    return { status: r.status, body: (text ? JSON.parse(text) : undefined) as T }
  }
  const runOf = async (id: string) => (await pub<{ run: Run }>('GET', `/api/runs/${id}`)).body.run
  const crew = await pub<{ crew: { id: string } }>('POST', '/api/crews', {
    name: 'e2e decide link',
    goal: 'fail a check',
    cwd: '',
    where: 'server',
    isolation: 'worktree',
    openAfterLaunch: false,
    yolo: null,
    members: [{ name: 'builder', agentId: 'worker-cli', prompt: '', start: { when: 'task' } }],
    tasks: [{ id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.', checks: [{ id: 'never', argv: ['sh', '-c', 'echo the check looked; exit 1'] }] }],
  })
  expect(crew.status).toBe(201)
  const launched = await pub<{ run: { id: string } }>('POST', `/api/crews/${crew.body.crew.id}/launch`)
  expect(launched.status).toBe(201)
  const runId = launched.body.run.id
  const failed = `task:${runId}/build`
  const contexts: Array<{ close: () => Promise<void> }> = []
  try {
    // Three attempts, each failing its check; the last attempt's session lives on, its turn done, and asks.
    budget(1, 180_000)
    await expect.poll(async () => task(await runOf(runId), 'build').state, { timeout: 180_000, intervals: [500], message: 'build fails its check three times' }).toBe('failed')
    const member = task(await runOf(runId), 'build').attempt?.sessionId ?? ''
    expect(member).not.toBe('')
    budget(1)
    await expect.poll(async () => (await pub<{ session: Session }>('GET', `/api/sessions/${member}`)).body.session?.attention?.state ?? '', { timeout: STEP, message: "the member's turn is done" }).not.toBe('needs_input')
    const asks = await pub<{ attention: { id: string } }>('POST', `/api/sessions/${member}/attention`, { state: 'needs_input', message: 'Which way?', options: [{ label: 'Left', input: 'Left\r' }, { label: 'Right', input: 'Right\r' }] })
    expect(asks.status).toBe(200)
    const question = `question:${member}/${asks.body.attention.id}`

    // The owner's Share on the run page: another link, control, answering decisions, minted at the switchyard over the run's desk.
    budget(5)
    const ownerCtx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
    contexts.push(ownerCtx)
    await ownerCtx.addInitScript((t) => localStorage.setItem('conductor.workbenchToken', t), publisher.token)
    const owner = await ownerCtx.newPage()
    await owner.goto(`${publisher.baseURL}/runs/${runId}`)
    await owner.getByRole('button', { name: 'Share', exact: true }).first().click()
    const dialog = owner.getByRole('dialog')
    await expect(dialog.locator('[data-created-url], [data-share-making]').first()).toBeVisible({ timeout: STEP })
    await dialog.getByText('Another link').click()
    await dialog.locator('[data-another-link]').getByRole('radio', { name: /Control/ }).click()
    const sw = dialog.getByRole('switch', { name: 'Answer decisions from this link' })
    await sw.click()
    await expect(sw).toHaveAttribute('aria-checked', 'true')
    await dialog.getByRole('button', { name: 'Create link' }).click()
    await expect(dialog.locator('[data-created-decide]')).toBeVisible({ timeout: STEP })
    const url = (await dialog.locator('[data-created-url]').getAttribute('title')) ?? ''
    expect(url.startsWith(`${syURL}/join/`), url).toBe(true)
    await expect(dialog.locator('[data-share-link-decide]').first()).toBeVisible()

    // A phone opens the link at the switchyard's own join page: the run's items above its members, over the run's desk.
    budget(4)
    const phoneCtx = await browser.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })
    contexts.push(phoneCtx)
    const phone = await phoneCtx.newPage()
    await phone.goto(url)
    await expect(phone.getByRole('heading', { name: 'Join e2e decide link' })).toBeVisible({ timeout: STEP })
    await phone.getByRole('textbox', { name: 'Your name' }).fill('e2e phone')
    await phone.getByRole('button', { name: 'Join crew' }).tap()
    const decide = phone.locator('[data-join-run-decide]')
    await expect(decide.locator(`[data-inbox-row="${question}"]`)).toBeVisible({ timeout: STEP })
    await expect(decide.locator(`[data-inbox-row="${failed}"]`)).toBeVisible()
    const steps = (): Promise<Record<string, string>> =>
      decide.locator('[data-inbox-item] [data-inbox-delivery] [data-step]').evaluateAll((els) => Object.fromEntries(els.map((e) => [e.getAttribute('data-step'), e.getAttribute('data-step-state')])))

    // The member's terminal from the question's evidence, one tap; then back to the crew, and the question again.
    budget(5)
    await decide.locator(`[data-inbox-row="${question}"]`).tap()
    const asked = decide.locator(`[data-inbox-item="${question}"]`)
    await expect(asked.locator('[data-inbox-question]')).toHaveText('Which way?')
    await expect(asked.locator('[data-inbox-action="answer"]')).toHaveText(['Left', 'Right'])
    await asked.locator('[data-inbox-evidence-toggle]').tap()
    await asked.locator(`[data-evidence-ref="session:${member}"] [data-evidence-terminal]`).tap()
    await expect(phone.locator('.terminal-host .xterm-screen').first()).toBeVisible({ timeout: STEP })
    await phone.getByRole('button', { name: 'Back to the crew' }).tap()
    await expect(asked.locator('[data-inbox-action="answer"]')).toHaveText(['Left', 'Right'], { timeout: STEP })

    // The question answered through the link: typed into the member, its steps through to resumed.
    budget(3)
    await asked.locator('[data-inbox-action="answer"][data-action-label="Right"]').tap()
    const agent = (await pub<{ session: Session }>('GET', `/api/sessions/${member}`)).body.session.agentSession?.id ?? ''
    const transcript = join(home4, '.stub-sessions', `${agent}.txt`)
    await expect.poll(() => (existsSync(transcript) ? readFileSync(transcript, 'utf8') : ''), { timeout: STEP, message: 'the choice was typed into the member' }).toContain('Right')
    await expect.poll(steps, { timeout: STEP }).toEqual({ available: 'done', recorded: 'done', validated: 'done', resumed: 'done' })

    // The member's session ended: no member of the run lives, and the run's desk still carries its decisions.
    budget(1)
    expect((await pub('DELETE', `/api/sessions/${member}`)).status).toBeLessThan(300)
    await expect.poll(async () => (await runOf(runId)).members.every((m) => m.status !== 'running' && m.status !== 'starting'), { timeout: STEP, message: 'no member lives' }).toBe(true)
    const attemptsBefore = task(await runOf(runId), 'build').attempts

    // The failed task over the desk: its check's log unfolded, retried, its steps through to the next attempt running.
    budget(6)
    await decide.locator('[data-inbox-back]').tap()
    await decide.locator(`[data-inbox-row="${failed}"]`).tap()
    const item = decide.locator(`[data-inbox-item="${failed}"]`)
    await expect(item.locator('[data-inbox-action]')).toHaveText(['Retry', 'Accept anyway…'])
    await item.locator('[data-inbox-evidence-toggle]').tap()
    const check = item.locator('[data-evidence-ref^="check:"]').first()
    await check.locator('[data-evidence-log-toggle]').tap()
    await expect(check.locator('[data-evidence-log]')).toContainText('the check looked', { timeout: STEP })
    await item.locator('[data-inbox-action="retry"]').tap()
    await expect.poll(async () => (await steps()).validated, { timeout: STEP }).toBe('done')
    await expect.poll(async () => (await steps()).resumed, { timeout: STEP, message: 'the next attempt runs' }).toBe('done')
    expect(task(await runOf(runId), 'build').attempts).toBeGreaterThan(attemptsBefore)

    // Read again from the link's Recent: the retry's steps, kept by the link after the item left the open list.
    budget(4)
    await decide.locator('[data-inbox-back]').tap()
    const row = decide.locator(`[data-recent-row="${failed}"]`)
    await expect(row.locator('[data-recent-word]')).toHaveText('Work resumed', { timeout: STEP })
    await expect(decide.locator(`[data-recent-row="${question}"] [data-recent-word]`)).toHaveText('Work resumed')
    await row.tap()
    await expect.poll(async () => (await steps()).resumed, { timeout: STEP }).toBe('done')
    await expect(item.locator('[data-step="recorded"] [data-step-words]')).toContainText("ledger keeps the command's receipt")
  } finally {
    for (const c of contexts) await c.close().catch(() => {})
    await pub('POST', `/api/runs/${runId}/stop`).catch(() => {})
    await stopServer(publisher.pid)
    publisher = null
  }
})
