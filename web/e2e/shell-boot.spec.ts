import { mkdtempSync, readFileSync, rmSync, statSync } from 'node:fs'
import { createServer, type Server } from 'node:http'
import type { AddressInfo } from 'node:net'
import { tmpdir } from 'node:os'
import { extname, join, normalize } from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, test, type Page } from '@playwright/test'
import { decisionAddress } from './shell-fake'
import { build } from 'vite'
// @ts-expect-error: the shell's build options are a plain JavaScript module (mobile/bridge.vite.config.mjs).
import { bridgeBuild } from '../../mobile/bridge.vite.config.mjs'
// @ts-expect-error: as above (mobile/scripts/sync.mjs).
import { syncBundle } from '../../mobile/scripts/sync.mjs'

// The shell's bundle starting under its own policy (docs/plans/phone-shell.md, Tests per surface, the boot checks; the plan's
// mobile/e2e/boot.spec.ts, run here by the app's Playwright until the shell has packages of its own): the built bundle synced as
// `make mobile-sync` syncs it (sync.mjs, the bridge built by mobile/bridge.vite.config.mjs), served by this spec with the page's own
// policy as a header too, and started with no injected bridge, as a browser with no native runtime starts it. Its first screen is the
// shell's with no provider (Open a server), the colour mode applied, and no `securitypolicyviolation` reported. An injected bridge
// could not stand in: it is the bundle's start that is checked.

const repo = fileURLToPath(new URL('../../', import.meta.url))
const dist = join(repo, 'internal/web/dist')

const TYPES: Record<string, string> = { '.html': 'text/html', '.js': 'text/javascript', '.mjs': 'text/javascript', '.css': 'text/css', '.json': 'application/json', '.svg': 'image/svg+xml', '.png': 'image/png', '.woff2': 'font/woff2', '.wasm': 'application/wasm', '.ico': 'image/x-icon' }

/** Serves www as the shell's local server does: a file by its path, any other address the bundle's index.html; each page under its policy. */
function serve(www: string, policy: (html: string) => string): Promise<{ server: Server; url: string }> {
  const server = createServer((req, res) => {
    const path = normalize(decodeURIComponent(new URL(req.url ?? '/', 'http://x').pathname)).replace(/^(\.\.[/\\])+/, '')
    let file = join(www, path)
    try {
      if (statSync(file).isDirectory()) file = join(file, 'index.html')
      statSync(file)
    } catch {
      file = join(www, 'index.html')
    }
    const body = readFileSync(file)
    const type = TYPES[extname(file)] ?? 'application/octet-stream'
    const headers: Record<string, string> = { 'Content-Type': type }
    if (type === 'text/html') headers['Content-Security-Policy'] = policy(body.toString('utf8'))
    res.writeHead(200, headers)
    res.end(body)
  })
  return new Promise((resolve) => server.listen(0, '127.0.0.1', () => resolve({ server, url: `http://127.0.0.1:${(server.address() as AddressInfo).port}` })))
}

/** The policy a page of the bundle carries in its meta. */
const metaPolicy = (html: string) => /<meta http-equiv="Content-Security-Policy" content="([^"]*)">/.exec(html)?.[1] ?? ''

/** A page that records every policy violation it reports, from before its first script. */
async function watched(page: Page): Promise<() => Promise<string[]>> {
  await page.addInitScript(() => {
    const seen: string[] = []
    ;(window as unknown as { __violations: string[] }).__violations = seen
    document.addEventListener('securitypolicyviolation', (e) => seen.push(`${e.violatedDirective} ${e.blockedURI}`))
  })
  return () => page.evaluate(() => (window as unknown as { __violations: string[] }).__violations.slice())
}

let root = ''
let www = ''
let served: { server: Server; url: string } | undefined
/** The same bundle synced with the tests' provider module (mobile/e2e/fixtures/provider). */
let withProvider: { server: Server; url: string } | undefined
const fixture = join(repo, 'mobile/e2e/fixtures/provider')

test.beforeAll(async () => {
  test.setTimeout(180_000)
  root = mkdtempSync(join(tmpdir(), 'shell-boot-'))
  www = join(root, 'www')
  syncBundle(dist, www)
  await build(bridgeBuild(join(www, 'shell')))
  served = await serve(www, metaPolicy)
  const www2 = join(root, 'www-provider')
  syncBundle(dist, www2, fixture)
  await build(bridgeBuild(join(www2, 'shell')))
  withProvider = await serve(www2, metaPolicy)
})

test.afterAll(async () => {
  await new Promise((r) => served?.server.close(r))
  await new Promise((r) => withProvider?.server.close(r))
  if (root) rmSync(root, { recursive: true, force: true })
})

test('the synced bundle starts under its policy on Open a server, with no provider and no violation', async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })
  const page = await ctx.newPage()
  const violations = await watched(page)
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  await page.goto(`${served!.url}/`)
  await expect(page).toHaveURL(/\/shell$/)
  await expect(page.locator('[data-shell-open-server]')).toBeVisible()
  await expect(page.locator('[data-shell-signin]')).toHaveCount(0)
  // The colour mode's inline script ran under the policy: its class is on the root.
  await expect(page.locator('html')).toHaveClass(/\b(dark|light)\b/)
  expect(await page.evaluate(() => (window as unknown as { conductorShell?: { v: number; platform: string } }).conductorShell?.platform)).toBe('web')
  const html = readFileSync(join(www, 'index.html'), 'utf8')
  expect(metaPolicy(html)).toMatch(/^script-src 'self' 'wasm-unsafe-eval'( 'sha256-[A-Za-z0-9+/=]+')+; connect-src 'none'; frame-src 'none'$/)
  expect(await violations()).toEqual([])
  expect(errors).toEqual([])
  await ctx.close()
})

test("with a provider module, a decision's page draws its code block highlighted and its diagram under the policy, with no violation", async ({ browser }) => {
  // Shiki's highlighter is compiled from WebAssembly: without 'wasm-unsafe-eval' the block stays plain and a violation is reported.
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })
  const page = await ctx.newPage()
  const violations = await watched(page)
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  await page.goto(`${withProvider!.url}${decisionAddress('boot-1')}`)
  await expect(page.locator('[data-shell-decision]')).toBeVisible()
  await expect(page.locator('[data-decision-question]')).toHaveText('May the boot check pass?')
  await expect(page.locator('[data-decision-context] pre.shiki')).toContainText('package main', { timeout: 30_000 })
  await expect(page.locator('[data-decision-context] [data-mermaid-rendered]')).toHaveCount(1, { timeout: 30_000 })
  await expect(page.locator('[data-step]')).toHaveCount(4)
  expect(await violations()).toEqual([])
  expect(errors).toEqual([])
  await ctx.close()
})
