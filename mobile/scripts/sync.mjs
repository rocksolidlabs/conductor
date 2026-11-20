// The workbench's bundle into the shell (make mobile-sync): internal/web/dist copied into <www> whole, a packaging's provider module
// (mobile/providers/, when there is one) beside it, and every page given the shell's policy and scripts (csp.mjs). The bridge's script
// is built into <www>/shell by `make mobile-sync` after this (mobile/bridge.vite.config.mjs).
//
//   node mobile/scripts/sync.mjs <dist> <www> [<providers>] [--connect=<https origin>,…] [--native-http=<https origin>,…]
//
// --connect names the origins a provider's build lets the pages connect to (each exactly an https origin); with none the pages
// connect to nothing. --native-http names the origins the shell's native HTTP call may reach, written into the bundle as
// shell/native-http.json, which the app reads once at its start; with none the shell makes no native HTTP call.
import { cpSync, existsSync, mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { connectOrigins, nativeOrigins, shellPages } from './csp.mjs'

/** Where the bundle holds the native HTTP call's origins (the app reads it at public/shell/native-http.json). */
export const NATIVE_HTTP = 'shell/native-http.json'

/**
 * Copies dist into www (emptied first), the provider folder into www/providers when it exists, writes the pages' policy and the
 * native HTTP call's origins.
 */
export function syncBundle(dist, www, providers, { connect = [], nativeHttp = [] } = {}) {
  if (!existsSync(join(dist, 'index.html'))) throw new Error(`${dist} holds no built bundle: make web-build first`)
  connectOrigins(connect)
  const origins = nativeOrigins(nativeHttp)
  rmSync(www, { recursive: true, force: true })
  cpSync(dist, www, { recursive: true, filter: (p) => !p.endsWith('.gitkeep') })
  if (providers && existsSync(join(providers, 'index.js'))) cpSync(providers, join(www, 'providers'), { recursive: true })
  const pages = shellPages(www, { connect })
  mkdirSync(join(www, 'shell'), { recursive: true })
  writeFileSync(join(www, NATIVE_HTTP), JSON.stringify({ origins }) + '\n')
  return pages
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  const args = process.argv.slice(2)
  const list = (name) => {
    const flag = args.find((a) => a.startsWith(`--${name}=`))
    return flag ? flag.slice(`--${name}=`.length).split(',').filter(Boolean) : []
  }
  const connect = list('connect')
  const nativeHttp = list('native-http')
  const [dist, www, providers] = args.filter((a) => !a.startsWith('--'))
  if (!dist || !www) {
    console.error('usage: node mobile/scripts/sync.mjs <dist> <www> [<providers>]')
    process.exit(2)
  }
  try {
    console.log(`sync: ${syncBundle(dist, www, providers, { connect, nativeHttp }).length} pages of ${dist} in ${www}`)
  } catch (e) {
    console.error(`sync: ${e instanceof Error ? e.message : e}`)
    process.exit(1)
  }
}
