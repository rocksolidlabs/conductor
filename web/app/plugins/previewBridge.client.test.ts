import { describe, expect, it, vi } from 'vitest'

// The plugin over a stub of the document: what it puts in the head. defineNuxtPlugin is stubbed to give back the definition.
vi.stubGlobal('defineNuxtPlugin', (p: unknown) => p)
const mod = await import('./previewBridge.client')
vi.unstubAllGlobals()
const plugin = mod.default as unknown as { setup: () => void }

/** A document of the parts the plugin uses: its head's children, the tags it made. */
function page() {
  const head: Array<{ src: string; async: boolean; dataset: Record<string, string> }> = []
  const doc = {
    head: { appendChild: (el: (typeof head)[number]) => void head.push(el) },
    createElement: (tag: string) => {
      expect(tag).toBe('script')
      return { src: '', async: false, dataset: {} as Record<string, string> }
    },
    querySelector: (sel: string) => (sel === 'script[data-conductor-preview-bridge]' ? (head.find((el) => 'conductorPreviewBridge' in el.dataset) ?? null) : null),
  }
  return { head, doc: doc as unknown as Document }
}

describe("the dev build's preview bridge", () => {
  it('is one script tag for the URL the configuration holds', () => {
    const { head, doc } = page()
    mod.loadPreviewBridge('http://127.0.0.1:4312/preview-bridge.js', doc)
    mod.loadPreviewBridge('http://127.0.0.1:4312/preview-bridge.js', doc)
    expect(head).toEqual([{ src: 'http://127.0.0.1:4312/preview-bridge.js', async: true, dataset: { conductorPreviewBridge: '' } }])
  })
  it('is none when the configuration holds no URL', () => {
    const { head, doc } = page()
    for (const src of ['', undefined, null, 42]) mod.loadPreviewBridge(src, doc)
    expect(head).toEqual([])
  })
  it('is never loaded by a build that is not the dev server', () => {
    const useRuntimeConfig = vi.fn(() => ({ public: { previewBridge: 'http://127.0.0.1:4312/preview-bridge.js' } }))
    vi.stubGlobal('useRuntimeConfig', useRuntimeConfig)
    vi.stubGlobal('document', page().doc)
    try {
      expect(import.meta.dev).toBeFalsy()
      plugin.setup()
      expect(useRuntimeConfig).not.toHaveBeenCalled()
    } finally {
      vi.unstubAllGlobals()
    }
  })
})
