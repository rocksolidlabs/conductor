// The shell's page policy and its scripts (docs/plans/phone-shell.md, Part two, OSS-1a, "The page's content security policy"): each
// page of the bundle copied into mobile/www gains, at the head, a Content-Security-Policy of `script-src 'self' 'wasm-unsafe-eval'`
// and the SHA-256 of each inline script the page holds (the import map, the colour mode, the runtime configuration; a JSON data block
// is no script the browser runs and takes none), and, before the workbench's entry, the bridge's script and, when the packaging
// placed one, the provider module's. What a hash cannot admit fails the sync: an inline event handler or a `javascript:` address.
//
//   node mobile/scripts/csp.mjs <www>     every .html under <www>, in place
//
// Node's own modules alone: no package is added for it.
import { createHash } from 'node:crypto'
import { existsSync, readdirSync, readFileSync, statSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

/** The bridge's script and the provider module's, as the bundle's pages load them. */
export const BRIDGE_SRC = '/shell/bridge.js'
export const PROVIDER_SRC = '/providers/index.js'

/** The types of a script element the browser runs (an import map is one); any other type is a data block. */
const RUNS = new Set(['', 'module', 'text/javascript', 'application/javascript', 'importmap'])

/** The inline scripts of html the browser runs: their text, in order. */
export function inlineScripts(html) {
  const out = []
  for (const m of html.matchAll(/<script\b([^>]*)>([\s\S]*?)<\/script\s*>/gi)) {
    const attrs = m[1]
    if (/\bsrc\s*=/i.test(attrs)) continue
    const type = (/\btype\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))/i.exec(attrs) ?? [])
    const t = (type[1] ?? type[2] ?? type[3] ?? '').trim().toLowerCase()
    if (!RUNS.has(t)) continue
    out.push(m[2])
  }
  return out
}

/** What a hash cannot admit in html, in words: an inline event handler attribute, a `javascript:` address. */
export function refusals(html) {
  const out = []
  const tags = html.replace(/<script\b[^>]*>[\s\S]*?<\/script\s*>/gi, (s) => s.slice(0, s.indexOf('>') + 1))
  for (const m of tags.matchAll(/<[a-z][^>]*>/gi)) {
    const tag = m[0]
    const handler = /\s(on[a-z]+)\s*=/i.exec(tag)
    if (handler) out.push(`an inline event handler (${handler[1]}) in ${tag.slice(0, 80)}`)
    if (/\b(?:href|src|action|formaction|xlink:href)\s*=\s*["']?\s*javascript:/i.test(tag)) out.push(`a javascript: address in ${tag.slice(0, 80)}`)
  }
  return out
}

/** The policy for html: 'self', compiling WebAssembly, and each inline script's SHA-256. */
export function policyFor(html) {
  const hashes = inlineScripts(html).map((s) => `'sha256-${createHash('sha256').update(s, 'utf8').digest('base64')}'`)
  return ["script-src 'self' 'wasm-unsafe-eval'", ...hashes].join(' ')
}

/**
 * html with the policy at the head of its <head> and, before the workbench's entry (the first module script that loads a file), the
 * bridge's script tag and, when `provider`, the provider module's; a page with no entry, the policy alone. Throws on what a hash
 * cannot admit.
 */
export function shellPage(html, { provider = false } = {}) {
  const bad = refusals(html)
  if (bad.length) throw new Error(`the page cannot run under the shell's policy: ${bad.join('; ')}`)
  if (/http-equiv\s*=\s*["']?content-security-policy/i.test(html)) throw new Error('the page holds a policy already')
  // A page of the bundle that is not the workbench's (the brand page: no entry) gets the policy alone.
  const entry = /<script\b[^>]*\btype\s*=\s*["']?module["']?[^>]*\bsrc\s*=[^>]*>\s*<\/script\s*>/i.exec(html)
  const tags = `<script src="${BRIDGE_SRC}"></script>${provider ? `<script src="${PROVIDER_SRC}"></script>` : ''}`
  const withTags = entry ? html.slice(0, entry.index) + tags + html.slice(entry.index) : html
  const head = /<head\b[^>]*>/i.exec(withTags)
  if (!head) throw new Error('the page holds no <head>')
  const meta = `<meta http-equiv="Content-Security-Policy" content="${policyFor(withTags)}">`
  const at = head.index + head[0].length
  return withTags.slice(0, at) + meta + withTags.slice(at)
}

/** Every .html file under dir. */
function pages(dir) {
  const out = []
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) out.push(...pages(p))
    else if (name.endsWith('.html')) out.push(p)
  }
  return out
}

/** Writes the policy and the scripts into every page of the bundle in www; the provider's tag when www holds its module. */
export function shellPages(www) {
  const provider = existsSync(join(www, PROVIDER_SRC))
  const done = []
  for (const p of pages(www)) {
    writeFileSync(p, shellPage(readFileSync(p, 'utf8'), { provider }))
    done.push(p)
  }
  if (!done.some((p) => p === join(www, 'index.html'))) throw new Error(`${www} holds no index.html`)
  if (!readFileSync(join(www, 'index.html'), 'utf8').includes(`src="${BRIDGE_SRC}"`)) throw new Error("index.html holds no entry: the workbench's module script")
  return done
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  const www = process.argv[2]
  if (!www) {
    console.error('usage: node mobile/scripts/csp.mjs <www>')
    process.exit(2)
  }
  try {
    console.log(`csp: ${shellPages(www).length} pages under ${www} carry the shell's policy`)
  } catch (e) {
    console.error(`csp: ${e instanceof Error ? e.message : e}`)
    process.exit(1)
  }
}
