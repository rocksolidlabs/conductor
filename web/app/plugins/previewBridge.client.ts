/**
 * The preview bridge in Conductor's own dev build (conductor.manifest.json, docs/architecture.md, Previews and captures): a tree of
 * this repository serves the app's dev server, and the workbench that frames it reads it through the bridge the tree is told of
 * (CONDUCTOR_PREVIEW_BRIDGE, held to a loopback server's /preview-bridge.js by build/devConfig.ts). The dev build loads it by one
 * script tag; the built app never does, since its configuration never holds the URL and this code is left out of it.
 */
export default defineNuxtPlugin({
  name: 'conductor:preview-bridge',
  setup() {
    if (!import.meta.dev) return
    loadPreviewBridge(useRuntimeConfig().public.previewBridge, document)
  },
})

/** loadPreviewBridge puts one script tag for src into doc's head; nothing for an empty src, or when the tag is there already. */
export function loadPreviewBridge(src: unknown, doc: Document): void {
  if (typeof src !== 'string' || !src) return
  if (doc.querySelector('script[data-conductor-preview-bridge]')) return
  const s = doc.createElement('script')
  s.src = src
  s.async = true
  s.dataset.conductorPreviewBridge = ''
  doc.head.appendChild(s)
}
