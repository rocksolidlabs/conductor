// The workbench's bundle into the shell (make mobile-sync): internal/web/dist copied into <www> whole, a packaging's provider module
// (mobile/providers/, when there is one) beside it, and every page given the shell's policy and scripts (csp.mjs). The bridge's script
// is built into <www>/shell by `make mobile-sync` after this (mobile/bridge.vite.config.mjs).
//
//   node mobile/scripts/sync.mjs <dist> <www> [<providers>]
import { cpSync, existsSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { shellPages } from './csp.mjs'

/** Copies dist into www (emptied first), the provider folder into www/providers when it exists, and writes the pages' policy. */
export function syncBundle(dist, www, providers) {
  if (!existsSync(join(dist, 'index.html'))) throw new Error(`${dist} holds no built bundle: make web-build first`)
  rmSync(www, { recursive: true, force: true })
  cpSync(dist, www, { recursive: true, filter: (p) => !p.endsWith('.gitkeep') })
  if (providers && existsSync(join(providers, 'index.js'))) cpSync(providers, join(www, 'providers'), { recursive: true })
  return shellPages(www)
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  const [dist, www, providers] = process.argv.slice(2)
  if (!dist || !www) {
    console.error('usage: node mobile/scripts/sync.mjs <dist> <www> [<providers>]')
    process.exit(2)
  }
  try {
    console.log(`sync: ${syncBundle(dist, www, providers).length} pages of ${dist} in ${www}`)
  } catch (e) {
    console.error(`sync: ${e instanceof Error ? e.message : e}`)
    process.exit(1)
  }
}
