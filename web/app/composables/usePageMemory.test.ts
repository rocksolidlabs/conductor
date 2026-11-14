import { afterEach, describe, expect, it, vi } from 'vitest'
import { reactive, toRef } from 'vue'
import { usePageMemory } from './usePageMemory'

// A value of the page kept in its memory (usePageMemory), as the crews page keeps the run's link it shows once: shared by every
// caller under its key as useState's is, and never in the payload's state, which useState writes into here as Nuxt's does (what Nuxt
// writes into the tab's storage at a reload it makes itself). The link is an invented one.
const TOKEN = 'run-view-token-0123456789'
const SHOWN = `https://workbench.example.net/join/${TOKEN}`

/** A load of the page: a new Nuxt app, useState writing into its payload's state. */
function load() {
  const app = { payload: { state: reactive({}) as Record<string, unknown> } }
  vi.stubGlobal('useNuxtApp', () => app)
  vi.stubGlobal('useState', (key: string, init: () => unknown) => {
    const r = toRef(app.payload.state, `$s${key}`)
    if (r.value === undefined) r.value = init()
    return r
  })
  return app
}

describe('a value in the page memory', () => {
  afterEach(() => vi.unstubAllGlobals())

  it("is one value for the page under its key, outside the page's state, and a new page has its own", () => {
    const app = load()
    const shown = usePageMemory<{ url: string } | null>('crewShownLink', () => null)
    expect(shown.value).toBeNull()
    shown.value = { url: SHOWN }
    expect(usePageMemory<{ url: string } | null>('crewShownLink', () => null).value).toEqual({ url: SHOWN })
    expect(usePageMemory('another', () => 1).value).toBe(1)
    expect(JSON.stringify(app.payload.state)).not.toContain(TOKEN)
    load()
    expect(usePageMemory<{ url: string } | null>('crewShownLink', () => null).value).toBeNull()
  })
})
