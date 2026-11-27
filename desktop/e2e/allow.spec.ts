import { _electron as electron, expect, test, type ElectronApplication, type Page } from '@playwright/test'
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

// Allow this folder in the real app: a folder outside the home folder (the app's default folder for agents) is allowed through
// the bridge the Launch agent dialog presses; the settings file keeps it; the server's process is the same before and after, so
// a session started before goes on; a launch there starts; and after Restart server the folder is still allowed, from the saved
// settings. The app runs a server of its own over a fresh HOME and settings (as firstrun.spec.ts), reach and publishing off, its
// catalog one agent, cat. Needs a display (xvfb-run on Linux CI) and bin/conductor (make build-go).
const bin = process.env.CONDUCTOR_DESKTOP_BIN || join(__dirname, '..', '..', 'bin', 'conductor')

test.skip(!existsSync(bin), `no server binary at ${bin}: make build-go`)
test.skip(process.platform !== 'linux', "a fresh install of the test's own is placed with XDG_CONFIG_HOME, which Linux alone reads")

const workbench = /^http:\/\/(127\.0\.0\.1|localhost):\d+\//

type Bridge = {
  token(): Promise<string>
  allowFolder?(path: string, confirmed?: string): Promise<{ ok: boolean; root?: string; saved?: boolean; code?: string }>
  serverState(): Promise<{ state: string; url: string; origin?: string; pid?: number }>
  restartServer(): Promise<void>
}

async function open(env: Record<string, string>): Promise<{ app: ElectronApplication; page: Page }> {
  const app = await electron.launch({ args: [join(__dirname, '..')], env })
  let page: Page | undefined
  await expect
    .poll(
      () => {
        page = app.windows().find((p) => workbench.test(p.url()))
        return !!page
      },
      { timeout: 60_000 },
    )
    .toBe(true)
  await expect(page!.locator('[data-session-list], nav').first()).toBeVisible({ timeout: 30_000 })
  return { app, page: page! }
}

test('a folder outside is allowed from the app with no restart, kept in its settings, and still allowed after a restart', async () => {
  test.setTimeout(240_000)
  const root = mkdtempSync(join(tmpdir(), 'cd-allow-'))
  const home = join(root, 'home')
  const outside = join(root, 'projects', 'outside')
  mkdirSync(home)
  mkdirSync(outside, { recursive: true })
  const config = join(root, 'conductor.json')
  writeFileSync(config, JSON.stringify({ catalog: { disableDefaults: true, agents: [{ id: 'cat', name: 'cat', command: ['/bin/cat'] }] } }))
  const wrapper = join(root, 'conductor')
  // The wrapper keeps the first line the app writes on the server's stdin (the token and the key), so that the test knows their
  // values, and hands the server that line and the rest of stdin.
  const secretsFile = join(root, 'secrets.json')
  writeFileSync(
    wrapper,
    `#!/bin/sh\ncmd=$1\nshift\nIFS= read -r line\nprintf '%s\\n' "$line" > ${JSON.stringify(secretsFile)}\n{ printf '%s\\n' "$line"; cat; } | CONDUCTOR_REACH=off CONDUCTOR_RENDEZVOUS=0 ${JSON.stringify(bin)} "$cmd" --config ${JSON.stringify(config)} "$@"\n`,
  )
  chmodSync(wrapper, 0o755)
  const env: Record<string, string> = {}
  for (const [k, v] of Object.entries(process.env)) if (v !== undefined && !k.startsWith('CONDUCTOR_')) env[k] = v
  Object.assign(env, { CONDUCTOR_DESKTOP_BIN: wrapper, ELECTRON_DISABLE_SANDBOX: '1', XDG_CONFIG_HOME: join(root, 'config'), HOME: home })
  let app: ElectronApplication | undefined
  try {
    const started = await open(env)
    app = started.app
    const page = started.page
    // The bridge as the workbench's page has it (window.conductorDesktop).
    const desk = () => (window as unknown as { conductorDesktop: Bridge }).conductorDesktop
    const state = () => page.evaluate(`(${desk})().serverState()`) as Promise<{ state: string; url: string; origin?: string; pid?: number }>
    const token = (await page.evaluate(`(${desk})().token()`)) as string
    const api = async (method: string, origin: string, path: string, body?: unknown) => {
      const res = await fetch(origin + path, { method, headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) })
      return { status: res.status, body: (await res.json().catch(() => null)) as Record<string, unknown> | null }
    }
    const before = await state()
    expect(before.state).toBe('running')
    // The app talks to the address its server bound.
    expect(before.origin).toMatch(/^http:\/\/127\.0\.0\.1:\d+$/)
    expect(new URL(page.url()).origin).toBe(before.origin)
    const origin = before.origin!
    // whoami says the app may allow a folder; the folder is outside, and a launch there is refused.
    expect((await api('GET', origin, '/api/whoami')).body?.folders).toBe('app')
    expect((await api('GET', origin, `/api/roots/check?path=${encodeURIComponent(outside)}`)).body?.state).toBe('outside')
    expect((await api('POST', origin, '/api/sessions', { agentId: 'cat', cwd: outside })).status).toBe(400)
    // A session started before the press, in the home folder.
    const first = await api('POST', origin, '/api/sessions', { agentId: 'cat', cwd: home })
    expect(first.status).toBe(201)
    // The workbench token alone may not allow it: the key is the app's.
    expect((await api('POST', origin, '/api/roots', { path: outside })).status).toBe(403)

    const answer = await page.evaluate(`(${desk})().allowFolder(${JSON.stringify(outside)})`)
    expect(answer).toEqual({ ok: true, root: outside, saved: true })
    const userData = await app.evaluate(({ app }) => app.getPath('userData'))
    // Kept as the press's, apart from the folders typed in Settings.
    const saved = JSON.parse(readFileSync(join(userData, 'settings.json'), 'utf8')) as { allowedRoots: string[]; pressedRoots: Array<{ path: string; confirmed: string }> }
    expect(saved.pressedRoots).toContainEqual({ path: outside, confirmed: '' })
    expect(saved.allowedRoots).not.toContain(outside)
    // Nothing restarted: the same server process, the session started before still running.
    const after = await state()
    expect(after.pid).toBe(before.pid)
    expect(after.url).toBe(before.url)
    expect((await api('GET', origin, `/api/sessions/${first.body?.id}`)).body?.status).toBe('running')
    const there = await api('POST', origin, '/api/sessions', { agentId: 'cat', cwd: outside })
    expect(there.status).toBe(201)
    expect(there.body?.cwd).toBe(outside)
    // The token and the key the app handed its server, by their values: in neither the settings file nor either log, and in
    // neither the server's starting environment nor its command line as the system shows them.
    const secrets = JSON.parse(readFileSync(secretsFile, 'utf8')) as { workbenchToken: string; desktopKey: string }
    expect(secrets.workbenchToken).toBe(token)
    expect(secrets.desktopKey).toMatch(/^[0-9a-f]{64}$/)
    const kept = [
      readFileSync(join(userData, 'settings.json'), 'utf8'),
      readFileSync(join(userData, 'logs', 'main.log'), 'utf8'),
      readFileSync(join(userData, 'logs', 'server.log'), 'utf8'),
      readFileSync(`/proc/${before.pid}/environ`, 'utf8'),
      readFileSync(`/proc/${before.pid}/cmdline`, 'utf8'),
    ]
    for (const text of kept) {
      expect(text.includes(secrets.workbenchToken)).toBe(false)
      expect(text.includes(secrets.desktopKey)).toBe(false)
    }

    // Restart server: the folder comes back from the saved settings.
    // The restart moves the page to the new server's origin: the call is started, not awaited for its answer.
    void page.evaluate(`(${desk})().restartServer()`).catch(() => {})
    let next = after
    await expect
      .poll(
        async () => {
          next = await state().catch(() => next)
          return next.state === 'running' && next.pid !== before.pid
        },
        { timeout: 60_000 },
      )
      .toBe(true)
    const nextOrigin = next.origin!
    // A server's token is its own: the first one is refused by the next server, and the page is handed the new one.
    let nextToken = ''
    await expect
      .poll(
        async () => {
          nextToken = (await page.evaluate(`(${desk})().token()`).catch(() => '')) as string
          return nextToken !== '' && nextToken !== token
        },
        { timeout: 60_000 },
      )
      .toBe(true)
    const nextApi = (path: string, tok: string) => fetch(nextOrigin + path, { headers: { Authorization: `Bearer ${tok}` } }).then((r) => r.status)
    expect(await nextApi('/api/whoami', token)).toBe(401)
    expect(await nextApi('/api/whoami', nextToken)).toBe(200)
    const check = await fetch(nextOrigin + `/api/roots/check?path=${encodeURIComponent(outside)}`, { headers: { Authorization: `Bearer ${nextToken}` } })
    expect(((await check.json()) as { state?: string }).state).toBe('allowed')
    await app.close()
    app = undefined
  } finally {
    await app?.close().catch(() => {})
    rmSync(root, { recursive: true, force: true })
  }
})
