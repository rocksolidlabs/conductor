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
