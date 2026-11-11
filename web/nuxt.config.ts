import type { IncomingMessage, ServerResponse } from 'node:http'
import { fileURLToPath } from 'node:url'
import { AGENT_ICONS } from './app/utils/agentIcons'
import { bundledPackages } from './build/bundledPackages'
import { MERMAID_RENDERER_DEV_PATH } from './app/utils/mermaidRules'

const webRoot = fileURLToPath(new URL('.', import.meta.url))

// Nuxt configuration for the Conductor workbench. The app is a client-only
// SPA generated into internal/web/dist and embedded in the Go binary.
const bundled = bundledPackages(webRoot, `${webRoot}.nuxt/bundled-packages.json`)

export default defineNuxtConfig({
  compatibilityDate: '2026-09-01',
  ssr: false,
  modules: ['@nuxt/ui'],
  css: ['~/assets/css/main.css'],
  // Dark is the theme, whatever the OS prefers; the sidebar's theme button
  // switches to light and that choice is kept (under a key of our own, so a
  // "light" an earlier build saved while following the OS does not carry over).
  colorMode: { preference: 'dark', fallback: 'dark', storageKey: 'conductor-color-mode' },
  // The workbench never fetches icons at runtime: every icon it shows is
  // bundled from @iconify-json/lucide. The scan finds the names in the app's
  // sources (.ts too: event icons are named in app/utils); the built-in
  // catalog's icons come from the server (internal/catalog/defaults.go), so
  // they are listed, in app/utils/agentIcons.ts. A catalog icon outside that
  // list shows the generic agent icon (agentIcon).
  icon: {
    provider: 'none',
    clientBundle: {
      scan: { globInclude: ['app/**/*.{vue,ts}'] },
      icons: AGENT_ICONS.map((name) => name.replace(/^i-lucide-/, 'lucide:')),
      sizeLimitKb: 256,
    },
  },
  devtools: { enabled: false },
  app: {
    head: {
      title: 'Conductor',
      meta: [
        { name: 'referrer', content: 'no-referrer' },
        // The phone's bottom bar sits under the home indicator: its padding reads the safe area.
        { name: 'viewport', content: 'width=device-width, initial-scale=1, viewport-fit=cover' },
      ],
      link: [{ key: 'icon', rel: 'icon', type: 'image/svg+xml', href: '/brand/conductor-favicon.svg' }],
    },
  },
  runtimeConfig: {
    public: {
      // Empty means same origin (the embedded UI). Set NUXT_PUBLIC_API_BASE
      // to http://localhost:8080 when the dev proxy cannot upgrade WebSockets.
      apiBase: '',
    },
  },
  nitro: {
    // Output stays in web/.output/public; `make web-build` copies it into
    // internal/web/dist so Nuxt's own cleanup never empties the embed directory.
    devProxy: {
      '/api': { target: 'http://127.0.0.1:8080/api', changeOrigin: true },
      '/ws': { target: 'ws://127.0.0.1:8080/ws', ws: true, changeOrigin: true },
    },
  },
  vite: {
    // The npm packages the client bundle and its workers carry, for THIRD_PARTY_NOTICES (scripts/notices.py).
    plugins: [bundled.client],
    worker: { plugins: () => [bundled.worker] },
    optimizeDeps: { include: ['@xterm/xterm', '@xterm/addon-fit', '@xterm/addon-webgl', '@xterm/addon-web-links'] },
  },
  hooks: {
    // The dev server only: the Markdown preview's Mermaid renderer (app/utils/mermaidFrame.entry.ts) as one classic script
    // at a path of its own, bundled by Vite's library build as the dev server starts. The built app serves the same entry
    // as a worker-format asset; under the dev server Vite serves modules, which the preview's sandboxed frame (its origin
    // opaque) could load only with a CORS allowance for every opaque origin. A classic script needs none.
    'vite:serverCreated'(server, { isClient }) {
      if (!isClient) return
      const bundle = import('vite')
        .then(({ build }) =>
          build({
            configFile: false,
            root: webRoot,
            logLevel: 'silent',
            build: { write: false, minify: false, lib: { entry: `${webRoot}app/utils/mermaidFrame.entry.ts`, formats: ['iife'], name: 'conductorMermaidRenderer', fileName: () => 'mermaid-renderer.js' } },
          }),
        )
        .then((out) => {
          const first = (Array.isArray(out) ? out[0] : out) as { output?: Array<{ code?: string }> }
          const code = first.output?.find((c) => typeof c.code === 'string')?.code
          if (!code) throw new Error('the Mermaid renderer did not build')
          return code
        })
      bundle.catch(() => {})
      server.middlewares.stack.unshift({
        route: MERMAID_RENDERER_DEV_PATH,
        handle: (_req: IncomingMessage, res: ServerResponse, next: (err?: unknown) => void) => {
          bundle.then(
            (code) => {
              res.setHeader('Content-Type', 'text/javascript; charset=utf-8')
              res.setHeader('Cache-Control', 'no-store')
              res.end(code)
            },
            (e) => next(e),
          )
        },
      })
    },
  },
  typescript: { strict: true, typeCheck: false },
})
