import { _electron as electron, expect, test, type ElectronApplication, type Page } from '@playwright/test'
import { chmodSync, existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

// Every window of the app keeps the same fences, in the real app: a window the workbench opens at its own server is a workbench
// window (the bridge in it), and in it, and in a window it opens in turn, a page elsewhere never loads: a navigation there or a
// window asked for there opens outside the app (shell.openExternal, recorded here: nothing opens), and the window stays on the
// server. The app runs a server of its own over a fresh HOME and settings (as firstrun.spec.ts), reach and publishing off. Needs a
// display (xvfb-run on Linux CI) and bin/conductor (make build-go).
const bin = process.env.CONDUCTOR_DESKTOP_BIN || join(__dirname, '..', '..', 'bin', 'conductor')

test.skip(!existsSync(bin), `no server binary at ${bin}: make build-go`)
test.skip(process.platform !== 'linux', "a fresh install of the test's own is placed with XDG_CONFIG_HOME, which Linux alone reads")

const workbench = /^http:\/\/127\.0\.0\.1:\d+\//

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

test('a window the workbench opens, and one that window opens, keep the same fences', async () => {
  test.setTimeout(180_000)
  const root = mkdtempSync(join(tmpdir(), 'cd-windows-'))
  const home = join(root, 'home')
  mkdirSync(home)
  const wrapper = join(root, 'conductor')
  writeFileSync(wrapper, `#!/bin/sh\nCONDUCTOR_REACH=off CONDUCTOR_RENDEZVOUS=0 exec ${JSON.stringify(bin)} "$@"\n`)
  chmodSync(wrapper, 0o755)
  const env: Record<string, string> = {}
  for (const [k, v] of Object.entries(process.env)) if (v !== undefined && !k.startsWith('CONDUCTOR_')) env[k] = v
  Object.assign(env, { CONDUCTOR_DESKTOP_BIN: wrapper, ELECTRON_DISABLE_SANDBOX: '1', XDG_CONFIG_HOME: join(root, 'config'), HOME: home })
  let app: ElectronApplication | undefined
  try {
    const started = await open(env)
    app = started.app
    const page = started.page
    const origin = new URL(page.url()).origin
    // The system's browser, recorded: every link the app hands it.
    await app.evaluate(({ shell }) => {
      const g = globalThis as unknown as { e2eExternal: string[] }
      g.e2eExternal = []
      shell.openExternal = async (url: string) => void g.e2eExternal.push(url)
    })
    const external = () => app!.evaluate(() => (globalThis as unknown as { e2eExternal: string[] }).e2eExternal)
    /** The window `from` opens at url (by window.open): the new window's page. */
    const opens = async (from: Page, url: string): Promise<Page> => {
      const next = app!.waitForEvent('window')
      await from.evaluate((u) => void window.open(u, '_blank'), url)
      return next
    }
    /** Asks `w` to show url in place, and waits for the app to hand it outside; the window stays on the server. */
    const staysOn = async (w: Page, url: string) => {
      await w.evaluate((u) => void (location.href = u), url)
      await expect.poll(external, { timeout: 30_000 }).toContain(url)
      expect(new URL(w.url()).origin).toBe(origin)
    }
    const child = await opens(page, origin + '/agents')
    await expect.poll(() => child.url(), { timeout: 30_000 }).toMatch(workbench)
    expect(await child.evaluate(() => typeof (window as unknown as { conductorDesktop?: unknown }).conductorDesktop)).toBe('object')
    await staysOn(child, 'https://example.com/child-link')
    // A window asked for elsewhere, from the child: none is made, and it opens outside the app.
    const windows = app.windows().length
    await child.evaluate(() => void window.open('https://example.com/child-window', '_blank'))
    await expect.poll(external, { timeout: 30_000 }).toContain('https://example.com/child-window')
    expect(app.windows().length).toBe(windows)
    // The child's own window at the server: fenced the same.
    const grandchild = await opens(child, origin + '/')
    await expect.poll(() => grandchild.url(), { timeout: 30_000 }).toMatch(workbench)
    await staysOn(grandchild, 'https://example.com/grandchild-link')
    await app.close()
    app = undefined
  } finally {
    await app?.close().catch(() => {})
    rmSync(root, { recursive: true, force: true })
  }
})
