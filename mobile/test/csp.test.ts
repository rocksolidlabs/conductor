import { createHash } from 'node:crypto'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterEach, describe, expect, it } from 'vitest'
// @ts-expect-error: the sync's scripts are plain JavaScript modules with no declarations.
import { BRIDGE_SRC, connectOrigins, inlineScripts, nativeOrigins, policyFor, PROVIDER_SRC, refusals, shellPage, shellPages } from '../scripts/csp.mjs'
// @ts-expect-error: as above.
import { NATIVE_HTTP, syncBundle } from '../scripts/sync.mjs'

// The shell's page policy (docs/plans/phone-shell.md, OSS-1a, Tests per surface, mobile/test/csp.test.ts): every page of the bundle
// carries `script-src 'self' 'wasm-unsafe-eval'` with every inline script's hash and no other; an inline event handler or a
// `javascript:` address fails the sync; the bridge's tag, and a provider module's when there is one, stand before the entry. The
// real build (internal/web/dist) is synced when it is there; the boot spec (web/e2e/shell-boot.spec.ts) runs the synced bundle.

const sha = (s: string) => `'sha256-${createHash('sha256').update(s, 'utf8').digest('base64')}'`
const ENTRY = '<script type="module" src="/_nuxt/entry.js" crossorigin></script>'
/** A page shaped as the build writes it: the import map, the entry, the colour mode, the runtime configuration and a JSON data block. */
const PAGE = `<!DOCTYPE html><html><head><meta charset="utf-8"><title>Conductor</title><script type="importmap">{"imports":{}}</script>${ENTRY}<script>"use strict";(()=>{document.documentElement.className="dark"})()</script></head><body><div id="__nuxt"></div><script>window.__NUXT__={}</script><script type="application/json" id="__NUXT_DATA__">[1]</script></body></html>`
const dist = fileURLToPath(new URL('../../internal/web/dist', import.meta.url))

const dirs: string[] = []
afterEach(() => {
  for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true })
})
const tmp = () => {
  const d = mkdtempSync(join(tmpdir(), 'shell-csp-'))
  dirs.push(d)
  return d
}

/** The policy a page carries, and its script tags' sources in order. */
function carried(html: string): { policy: string; srcs: string[] } {
  const policy = /<meta http-equiv="Content-Security-Policy" content="([^"]*)">/.exec(html)?.[1] ?? ''
  return { policy, srcs: [...html.matchAll(/<script\b[^>]*\bsrc="([^"]+)"/g)].map((m) => m[1]!) }
}

describe("the shell's policy", () => {
  it("hashes every inline script the browser runs, the import map among them, and no data block", () => {
    const scripts = inlineScripts(PAGE)
    expect(scripts).toHaveLength(3)
    expect(policyFor(PAGE)).toBe(`${["script-src 'self' 'wasm-unsafe-eval'", ...scripts.map(sha)].join(' ')}; connect-src 'none'; frame-src 'none'`)
    expect(policyFor(PAGE)).not.toContain("'unsafe-eval'")
    expect(policyFor(PAGE)).not.toContain("'unsafe-inline'")
  })

  it('lets the pages connect to nothing by default, and to the https origins a provider\'s build names', () => {
    expect(policyFor(PAGE)).toMatch(/; connect-src 'none'; frame-src 'none'$/)
    expect(policyFor(PAGE, { connect: ['https://relay.example.test', 'https://push.example.test:8443'] })).toMatch(/; connect-src https:\/\/relay\.example\.test https:\/\/push\.example\.test:8443; frame-src 'none'$/)
    const from = tmp()
    writeFileSync(join(from, 'index.html'), PAGE)
    const www = join(tmp(), 'www')
    syncBundle(from, www, undefined, { connect: ['https://relay.example.test'] })
    expect(carried(readFileSync(join(www, 'index.html'), 'utf8')).policy).toMatch(/; connect-src https:\/\/relay\.example\.test; frame-src 'none'$/)
  })

  it('refuses a connection source that is not exactly an https origin, and syncs nothing with it', () => {
    for (const bad of ['http://relay.example.test', 'https://relay.example.test/api', 'https://*.example.test', "'self'", '*', 'https://user@relay.example.test', 'relay.example.test', 'https://relay.example.test/', 'wss://relay.example.test']) {
      expect(() => connectOrigins([bad]), bad).toThrow(/an https origin/)
      expect(() => policyFor(PAGE, { connect: [bad] }), bad).toThrow(/an https origin/)
    }
    const from = tmp()
    writeFileSync(join(from, 'index.html'), PAGE)
    const www = join(tmp(), 'www')
    expect(() => syncBundle(from, www, undefined, { connect: ['http://relay.example.test'] })).toThrow(/an https origin/)
    expect(existsSync(www)).toBe(false)
  })

  it("writes the native HTTP call's origins into the bundle, none by default, and refuses one that is not exactly an https origin", () => {
    const from = tmp()
    writeFileSync(join(from, 'index.html'), PAGE)
    const www = join(tmp(), 'www')
    syncBundle(from, www)
    expect(JSON.parse(readFileSync(join(www, NATIVE_HTTP), 'utf8'))).toEqual({ origins: [] })
    syncBundle(from, www, undefined, { native: ['https://relay.example.test', 'https://relay.example.test:8443'] })
    expect(JSON.parse(readFileSync(join(www, NATIVE_HTTP), 'utf8'))).toEqual({ origins: ['https://relay.example.test', 'https://relay.example.test:8443'] })
    // The page's own connections are not widened by it.
    expect(carried(readFileSync(join(www, 'index.html'), 'utf8')).policy).toMatch(/; connect-src 'none'; frame-src 'none'$/)
    for (const bad of ['http://relay.example.test', 'https://relay.example.test/', 'https://relay.example.test.', 'https://user@relay.example.test', 'https://*.example.test', 'https://Relay.example.test', 'file:///etc']) {
      expect(() => nativeOrigins([bad]), bad).toThrow(/an https origin/)
      const fresh = join(tmp(), 'www')
      expect(() => syncBundle(from, fresh, undefined, { native: [bad] }), bad).toThrow(/an https origin/)
      expect(existsSync(fresh)).toBe(false)
    }
  })

  it('hashes an extra inline script like the others', () => {
    const more = PAGE.replace('</body>', '<script>console.log(1)</script></body>')
    expect(policyFor(more)).toContain(sha('console.log(1)'))
    expect(inlineScripts(more)).toHaveLength(4)
  })

  it('fails on an inline event handler or a javascript: address', () => {
    for (const bad of [PAGE.replace('<div id="__nuxt">', '<div id="__nuxt" onclick="go()">'), PAGE.replace('<div id="__nuxt">', '<a href="javascript:go()">x</a><div id="__nuxt">'), PAGE.replace('<body>', '<body onload="x()">')]) {
      expect(refusals(bad).length).toBeGreaterThan(0)
      expect(() => shellPage(bad)).toThrow(/cannot run under the shell's policy/)
    }
    // Text that is no attribute is not a handler.
    expect(refusals(PAGE.replace('</body>', '<script>const t = " onclick=x javascript:y"</script></body>'))).toEqual([])
  })

  it('puts the policy at the head of the page, and the bridge (then a provider module) before the entry', () => {
    const plain = shellPage(PAGE)
    expect(plain.indexOf('<meta http-equiv="Content-Security-Policy"')).toBe(plain.indexOf('<head>') + '<head>'.length)
    expect(carried(plain).srcs).toEqual([BRIDGE_SRC, '/_nuxt/entry.js'])
    expect(carried(plain).policy).toBe(policyFor(PAGE))
    const withProvider = shellPage(PAGE, { provider: true })
    expect(carried(withProvider).srcs).toEqual([BRIDGE_SRC, PROVIDER_SRC, '/_nuxt/entry.js'])
    expect(() => shellPage(plain)).toThrow(/a policy already/)
  })

  it("syncs a bundle: every page carries it, the provider's tag only when its module is there", () => {
    const from = tmp()
    writeFileSync(join(from, 'index.html'), PAGE)
    mkdirSync(join(from, 'runs'))
    writeFileSync(join(from, 'runs', 'index.html'), PAGE)
    writeFileSync(join(from, '.gitkeep'), '')
    const www = join(tmp(), 'www')
    expect(syncBundle(from, www)).toHaveLength(2)
    for (const p of ['index.html', 'runs/index.html']) expect(carried(readFileSync(join(www, p), 'utf8')).srcs).toEqual([BRIDGE_SRC, '/_nuxt/entry.js'])
    expect(existsSync(join(www, '.gitkeep'))).toBe(false)
    const providers = tmp()
    writeFileSync(join(providers, 'index.js'), 'conductorShell.register(() => ({ name: "Test" }))')
    syncBundle(from, www, providers)
    expect(carried(readFileSync(join(www, 'index.html'), 'utf8')).srcs).toEqual([BRIDGE_SRC, PROVIDER_SRC, '/_nuxt/entry.js'])
    expect(existsSync(join(www, PROVIDER_SRC))).toBe(true)
    // Synced again with the policy there: refused, never doubled.
    expect(() => shellPages(www)).toThrow(/a policy already/)
  })

  it.skipIf(!existsSync(join(dist, 'index.html')))('syncs the real build: its index.html carries the policy with every inline script hashed and no other', () => {
    const www = join(tmp(), 'www')
    syncBundle(dist, www)
    const html = readFileSync(join(www, 'index.html'), 'utf8')
    const { policy, srcs } = carried(html)
    const source = readFileSync(join(dist, 'index.html'), 'utf8')
    expect(policy).toBe(`${["script-src 'self' 'wasm-unsafe-eval'", ...inlineScripts(source).map(sha)].join(' ')}; connect-src 'none'; frame-src 'none'`)
    // Every inline script the synced page runs is one the policy admits.
    for (const s of inlineScripts(html)) expect(policy).toContain(sha(s))
    expect(srcs[0]).toBe(BRIDGE_SRC)
    expect(srcs[1]).toMatch(/^\/_nuxt\//)
    // The sync copies the whole real build (some 36 MB) and rewrites every page: vitest's 5 s default is no bound of that work, and a
    // loaded machine ran past it. This bound only bounds a hang.
  }, 120_000)
})
