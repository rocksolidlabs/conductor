import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as vue from 'vue'

// New tab for a preview that names its tree's secret (docs/plans/browser-evidence.md, "Run page and candidates"): its label read ahead
// as the pointer or the focus comes to the button, so the click opens the tab at once, within a click's leave to open one; a label it
// could not read is said. usePreviewOpen reads Nuxt's auto-imports as globals: each is stubbed.

let opened: string[]
let toasts: string[]
let answer: () => Promise<{ url: string }>
let asks: number
let changes: { value: number }

beforeEach(() => {
  opened = []
  toasts = []
  answer = async () => ({ url: 'http://127.0.0.1:41000/#token=s3cret' })
  asks = 0
  changes = { value: 0 }
  for (const k of ['ref', 'watch'] as const) vi.stubGlobal(k, vue[k])
  vi.stubGlobal('useApi', () => ({ request: () => (asks++, answer()) }))
  vi.stubGlobal('useWorkbenchToken', () => ({ changes }))
  vi.stubGlobal('useToast', () => ({ add: (t: { title: string; description?: string }) => toasts.push(`${t.title}: ${t.description ?? ''}`) }))
  vi.stubGlobal('window', { open: (u: string) => void opened.push(u) })
})
afterEach(() => vi.unstubAllGlobals())

const SECRET = 'http://127.0.0.1:41000/#token=${CONDUCTOR_TREE_SECRET}'

describe('usePreviewOpen', () => {
  it('opens at once, inside the click, a label read ahead', async () => {
    const { usePreviewOpen } = await import('./usePreviewLabel')
    const o = usePreviewOpen()
    const t = { run: 'r-12345678', task: 'build', attempt: 7 }
    o.prime(t, SECRET)
    await new Promise((r) => setTimeout(r, 0))
    // The click's own tick: nothing awaited before the tab is opened.
    void o.newTab(t, SECRET)
    expect(opened).toEqual(['http://127.0.0.1:41000/#token=s3cret'])
  })

  it('says so when the label could not be read, never silent', async () => {
    const { usePreviewOpen } = await import('./usePreviewLabel')
    answer = async () => {
      throw new Error('no preview is open')
    }
    await usePreviewOpen().newTab({ run: 'r-12345678', task: 'build', attempt: 8 }, SECRET)
    expect(opened).toEqual([])
    expect(toasts).toEqual(['The preview did not open: Its address could not be read: no preview is open'])
  })

  it('holds a label read ahead only for the token it was read under: a token forgotten or changed drops it', async () => {
    const { usePreviewOpen } = await import('./usePreviewLabel')
    const o = usePreviewOpen()
    const t = { run: 'r-12345678', task: 'build', attempt: 10 }
    o.prime(t, SECRET)
    await new Promise((r) => setTimeout(r, 0))
    expect(asks).toBe(1)
    // The token changes in the page (Forget token, another token): the address read under the earlier one is gone, and the
    // click reads the label again, under the token the page holds now.
    changes.value++
    answer = async () => ({ url: 'http://127.0.0.1:41000/#token=other' })
    await o.newTab(t, SECRET)
    expect(asks).toBe(2)
    expect(opened).toEqual(['http://127.0.0.1:41000/#token=other'])
  })

  it('does not ask again at every read ahead for a label that could not be read, and asks again at a click', async () => {
    const { usePreviewOpen } = await import('./usePreviewLabel')
    const o = usePreviewOpen()
    const t = { run: 'r-12345678', task: 'build', attempt: 11 }
    answer = async () => {
      throw new Error('no preview is open')
    }
    // A list that primes at each redraw: one ask, not one a redraw.
    for (let i = 0; i < 5; i++) {
      o.prime(t, SECRET)
      await new Promise((r) => setTimeout(r, 0))
    }
    expect(asks).toBe(1)
    // The person's own click asks again.
    answer = async () => ({ url: 'http://127.0.0.1:41000/#token=s3cret' })
    await o.newTab(t, SECRET)
    expect(asks).toBe(2)
    expect(opened).toEqual(['http://127.0.0.1:41000/#token=s3cret'])
  })

  it('opens a preview with no secret at its own address at once', async () => {
    const { usePreviewOpen } = await import('./usePreviewLabel')
    void usePreviewOpen().newTab({ run: 'r-12345678', task: 'build', attempt: 9 }, 'http://127.0.0.1:41000/')
    expect(opened).toEqual(['http://127.0.0.1:41000/'])
  })
})
