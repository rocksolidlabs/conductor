// The bridge's build (mobile/src/main.ts) into one classic script, mobile/www/shell/bridge.js, by Vite's library build: no bundler of
// the shell's own is added. It imports nothing, so the app's Vite builds it (make mobile-sync: `vite build --config` this file from
// web/); the boot spec builds it in memory with the same options (bridgeBuild).
import { fileURLToPath } from 'node:url'

const here = fileURLToPath(new URL('.', import.meta.url))

/** The library build of the bridge: written to outDir, or kept in memory when outDir is none. */
export function bridgeBuild(outDir) {
  return {
    configFile: false,
    root: here,
    publicDir: false,
    logLevel: outDir ? 'warn' : 'silent',
    build: {
      ...(outDir ? { outDir, emptyOutDir: false } : { write: false }),
      minify: false,
      target: 'es2020',
      lib: { entry: `${here}src/main.ts`, formats: ['iife'], name: 'conductorShellBridge', fileName: () => 'bridge.js' },
    },
  }
}

export default bridgeBuild(`${here}www/shell`)
