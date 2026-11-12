import type { InlineConfig } from 'vite'

/**
 * The preview bridge (app/utils/previewBridge.entry.ts; docs/protocol.md, Preview bridge) as one classic script, by Vite's
 * library build: the built app's preview-bridge.js, the dev server's copy and the vitest that runs what is built all build it here.
 */
export const PREVIEW_BRIDGE = 'preview-bridge.js'

/** The library build of the bridge under root (the web directory): written to outDir, or kept in memory when outDir is none. */
export function previewBridgeBuild(root: string, outDir?: string): InlineConfig {
  return {
    configFile: false,
    root,
    publicDir: false,
    logLevel: outDir ? 'warn' : 'silent',
    build: {
      ...(outDir ? { outDir, emptyOutDir: false } : { write: false }),
      minify: false,
      lib: { entry: `${root}app/utils/previewBridge.entry.ts`, formats: ['iife'], name: 'conductorPreviewBridge', fileName: () => PREVIEW_BRIDGE },
    },
  }
}

/** The bridge built in memory: its code. */
export async function buildPreviewBridge(root: string): Promise<string> {
  const { build } = await import('vite')
  const out = await build(previewBridgeBuild(root))
  const code = ((Array.isArray(out) ? out[0] : out) as { output?: Array<{ code?: string }> }).output?.find((c) => typeof c.code === 'string')?.code
  if (!code) throw new Error('the preview bridge did not build')
  return code
}

/**
 * The script as GET /preview-bridge.js serves it (internal/api/previewbridge.go, wrapBridge): its bytes unchanged inside a
 * function whose one parameter is the workbench origins, called with them. The dev server's origins are loopback origins alone,
 * which JSON writes with nothing to escape.
 */
export function wrapPreviewBridge(code: string, origins: string[]): string {
  return `(function (conductorWorkbenchOrigins) {\n${code}\n})(${JSON.stringify(origins)});\n`
}

/**
 * The dev server's answer for the bridge to a request with host (its Host header), as the route answers one on a loopback listener
 * (internal/api/testdata/preview-bridge-served.json, which web/build/previewBridge.test.ts holds it to): the route's four headers,
 * and the script in its wrapper with the loopback's three names at the host's port, or no origin for a host off the loopback.
 */
export function devPreviewBridge(code: string, host: string): { headers: Record<string, string>; body: string } {
  const m = /^(?:localhost|127\.0\.0\.1|\[::1\])(?::(\d{1,5}))?$/.exec(host)
  const port = m ? Number(m[1] ?? 80) : 0
  const origins = port > 0 && port < 65536 ? ['127.0.0.1', 'localhost', '[::1]'].map((h) => `http://${h}${port === 80 ? '' : ':' + port}`) : []
  const headers = { 'Content-Type': 'text/javascript; charset=utf-8', 'Cache-Control': 'no-cache', 'Cross-Origin-Resource-Policy': 'cross-origin', 'X-Content-Type-Options': 'nosniff' }
  return { headers, body: wrapPreviewBridge(code, origins) }
}
