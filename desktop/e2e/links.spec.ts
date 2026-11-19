import { _electron as electron, expect, test, type ElectronApplication, type Page } from '@playwright/test'
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

// A link an agent prints in a session's terminal, clicked in the app (#1020): a hyperlink (OSC 8) raised the page's confirm, which
// the app shows as a native dialog titled with its name ("Do you want to navigate to …? WARNING: This link could potentially be
// dangerous"). Now a click, plain or with Ctrl, hands it to the system's browser once (shell.openExternal, recorded here: nothing
// opens), and the page asks nothing. The app runs a server of its own over a fresh
// HOME and settings (as firstrun.spec.ts), reach and publishing off, its catalog the e2e stub agent alone, its PATH holding no
// agent program. Needs a display (xvfb-run on Linux CI) and bin/conductor (make build-go).
const bin = process.env.CONDUCTOR_DESKTOP_BIN || join(__dirname, '..', '..', 'bin', 'conductor')
const stub = join(__dirname, '..', '..', 'web', 'e2e', 'stub-agent.sh')

test.skip(!existsSync(bin), `no server binary at ${bin}: make build-go`)
test.skip(process.platform !== 'linux', "a fresh install of the test's own is placed with XDG_CONFIG_HOME, which Linux alone reads")

const workbench = /^http:\/\/(127\.0\.0\.1|localhost):\d+\//
const MR = 'https://example.com/group/project/-/merge_requests/95'

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

test("a hyperlink an agent printed opens in the system's browser once, and the app asks nothing", async ({}, testInfo) => {
  test.setTimeout(180_000)
  const root = mkdtempSync(join(tmpdir(), 'cd-links-'))
  const home = join(root, 'home')
  mkdirSync(home)
  const config = join(root, 'conductor.json')
  // web/e2e/conductor.e2e.json's claude stub, with its session recipe: the server hands the stub the id of its own session
  // (--session-id), which it names its transcript by. Without the recipe the server never knew that id (agentSession stayed empty),
  // and the first CI run waited on a transcript it could not name.
  const agent = {
    id: 'stub',
    name: 'Stub',
    command: ['/bin/bash', stub],
    env: { STUB_IDENTITY: 'claude', STUB_REPORT: 'claude' },
    adapter: 'claude',
    signal: { kind: 'hook' },
    session: { startArgs: ['--session-id', '{id}'], idFrom: 'hook', resumeArgs: ['--resume', '{id}'], idPattern: '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' },
  }
  writeFileSync(config, JSON.stringify({ catalog: { disableDefaults: true, agents: [agent] } }))
  const wrapper = join(root, 'conductor')
  writeFileSync(wrapper, `#!/bin/sh\ncmd=$1\nshift\nPATH=/usr/bin:/bin CONDUCTOR_REACH=off CONDUCTOR_RENDEZVOUS=0 exec ${JSON.stringify(bin)} "$cmd" --config ${JSON.stringify(config)} "$@"\n`)
  chmodSync(wrapper, 0o755)
  const env: Record<string, string> = {}
  for (const [k, v] of Object.entries(process.env)) if (v !== undefined && !k.startsWith('CONDUCTOR_')) env[k] = v
  Object.assign(env, { CONDUCTOR_DESKTOP_BIN: wrapper, ELECTRON_DISABLE_SANDBOX: '1', XDG_CONFIG_HOME: join(root, 'config'), HOME: home })
  let app: ElectronApplication | undefined
  try {
    const started = await open(env)
    app = started.app
    const page = started.page
    // The system's browser, recorded: every link the app hands it.
    await app.evaluate(({ shell }) => {
      const g = globalThis as unknown as { e2eExternal: string[] }
      g.e2eExternal = []
      const record = async (url: string) => {
        g.e2eExternal.push(url)
      }
      shell.openExternal = record
      if (shell.openExternal !== record) throw new Error('shell.openExternal could not be replaced: the test cannot see what the app hands the browser')
    })
    const external = () => app!.evaluate(() => (globalThis as unknown as { e2eExternal: string[] }).e2eExternal)
    const origin = new URL(page.url()).origin
    const token = await page.evaluate(() => (window as unknown as { conductorDesktop: { token(): Promise<string> } }).conductorDesktop.token())
    const api = async <T>(method: string, path: string, body?: unknown): Promise<T> => {
      const res = await fetch(origin + path, { method, headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) })
      if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${await res.text()}`)
      const text = await res.text()
      return (text ? JSON.parse(text) : undefined) as T
    }
    type S = { id: string; status?: string; agentSession?: { id: string } }
    const s = await api<S>('POST', '/api/sessions', { agentId: 'stub', name: 'desktop links' })
    const stubDir = join(home, '.stub-sessions')
    /** What a failure is told with: the session as the server has it, its agents, the stub's files, the server's log, the terminal. */
    const diagnose = async (): Promise<string> => {
      const out: string[] = []
      const step = async (name: string, f: () => Promise<string> | string) => {
        try {
          out.push(`--- ${name}:\n${await f()}`)
        } catch (e) {
          out.push(`--- ${name}: could not be read: ${(e as Error).message}`)
        }
      }
      await step('the session', async () => JSON.stringify(await api('GET', `/api/sessions/${encodeURIComponent(s.id)}`), null, 1))
      await step('the agents the server lists', async () => JSON.stringify((await api<{ agents: Array<{ id: string; command?: string[] }> }>('GET', '/api/catalog')).agents.map((a) => ({ id: a.id, command: a.command }))))
      await step("the stub's files", () => (existsSync(stubDir) ? readdirSync(stubDir).map((f) => `${f}:\n${readFileSync(join(stubDir, f), 'utf8').slice(-2000)}`).join('\n') : 'no folder'))
      await step('the page', async () => `${page.url()} transport=${await page.locator('[data-transport-state]').first().getAttribute('data-transport-state')} renderer=${await page.locator('.terminal-host').first().getAttribute('data-renderer')}`)
      await step("the terminal's rows (the DOM renderer's; a WebGL one draws on a canvas)", async () => (await page.locator('.terminal-host .xterm-rows').first().innerText({ timeout: 2000 })).trim() || '(empty)')
      const logs = join(await app!.evaluate(({ app }) => app.getPath('userData')), 'logs')
      await step("the server's log, the session's lines and its last 40", () => {
        const lines = readFileSync(join(logs, 'server.log'), 'utf8').split('\n')
        return [...lines.filter((l) => l.includes(s.id)), '…', ...lines.slice(-40)].join('\n')
      })
      await step("the app's log", () => readFileSync(join(logs, 'main.log'), 'utf8').slice(-3000))
      try {
        await testInfo.attach('screen', { body: await page.screenshot(), contentType: 'image/png' })
      } catch {
        /* a screenshot is extra */
      }
      return out.join('\n')
    }
    try {
      await page.goto(`${origin}/sessions/${encodeURIComponent(s.id)}`)
      await expect(page.locator('[data-transport-state]').first()).toHaveAttribute('data-transport-state', 'open', { timeout: 30_000 })
      // Every dialog the page raises, recorded and answered no: the app would show it as its native dialog.
      await page.evaluate(() => {
        const w = window as unknown as { e2eDialogs: string[] }
        w.e2eDialogs = []
        window.confirm = (m?: string) => {
          w.e2eDialogs.push(`confirm: ${m}`)
          return false
        }
        window.alert = (m?: string) => {
          w.e2eDialogs.push(`alert: ${m}`)
        }
      })
      const dialogs = () => page.evaluate(() => (window as unknown as { e2eDialogs: string[] }).e2eDialogs)
      await page.locator('.terminal-host .xterm-helper-textarea').first().focus()
      await page.keyboard.type(`stub links ${MR} the merge request`)
      await page.keyboard.press('Enter')
      // The stub's transcript, by its kind (<id>.txt, appended in place): the HOME is the test's own and holds this session's alone,
      // so it is read whether or not the server has named the stub's session yet.
      const transcript = () =>
        existsSync(stubDir)
          ? readdirSync(stubDir)
              .filter((f) => f.endsWith('.txt'))
              .map((f) => readFileSync(join(stubDir, f), 'utf8'))
              .join('')
          : ''
      await expect.poll(transcript, { timeout: 30_000, message: 'the stub printed its link' }).toContain('links: printed 1\n')
      const host = page.locator('.terminal-host').first()
      const box = (await host.boundingBox())!
      const rows = Number(await host.getAttribute('data-rows'))
      const cols = Number(await host.getAttribute('data-cols'))
      const x = box.x + (box.width / cols) * 3.5
      const y = box.y + (box.height / rows) * 0.5
      await expect(async () => {
        await page.mouse.move(x - 1, y)
        await page.mouse.move(x, y)
        await expect(host.locator('.xterm-cursor-pointer')).toHaveCount(1, { timeout: 1000 })
      }).toPass({ timeout: 15_000 })
      // A plain click: handed to the system's browser at once, once; the toast says where it went, with Preview in pane alone.
      await page.mouse.click(x, y)
      await expect.poll(async () => (await dialogs()).length + (await external()).length, { timeout: 15_000, message: 'the click was handled' }).toBeGreaterThan(0)
      expect(await dialogs(), 'a click on a link raises no dialog').toEqual([])
      expect(await external()).toEqual([MR])
      const said = page.locator('li').filter({ hasText: 'Opened example.com' })
      await expect(said).toHaveCount(1)
      await expect(said.getByRole('button', { name: 'Preview in pane' })).toBeVisible()
      await expect(said.getByRole('button', { name: 'Open in new tab' })).toHaveCount(0)
      // Ctrl+click: the same, once more, once.
      await page.keyboard.down('Control')
      await page.mouse.click(x, y)
      await page.keyboard.up('Control')
      await expect.poll(async () => (await external()).length, { timeout: 15_000 }).toBeGreaterThanOrEqual(2)
      expect(await dialogs(), 'a click on a link raises no dialog').toEqual([])
      expect(await external()).toEqual([MR, MR])
      // The app made no window of either.
      expect(app.windows().filter((p) => p.url().startsWith('https://example.com')).map((p) => p.url())).toEqual([])
    } catch (e) {
      const said = await diagnose()
      console.log(said)
      await testInfo.attach('diagnosis', { body: said, contentType: 'text/plain' })
      throw e
    }
    await api('DELETE', `/api/sessions/${encodeURIComponent(s.id)}`)
  } finally {
    await app?.close()
    rmSync(root, { recursive: true, force: true })
  }
})
