import { describe, expect, it } from 'vitest'
import { CHANGED_WORDS, MOVING_WORDS, NO_BRIDGE_WORDS, RESTLESS_WORDS, type HeardMessage, type LabelRead } from '~/utils/capture'
import { PREVIEW_MARK, type ElementReport, type PreviewPage } from '~/utils/protocol'
import type { Pixels, ScreenshotSource } from '~/utils/screenshot'
import { testElement, testPage } from '~/utils/testdata/preview'
import { HELLO_MS, createPreviewSession } from './usePreviewBridge'

// The workbench's side of the bridge (docs/plans/browser-evidence.md, "The messages" and "One capture is one moment"), against a
// fake page that answers as the bridge does, through the same gate the pane uses: the 3 s hello, one request in flight, and a
// draft as one moment, checked by the page's own report and, where there are pixels, by the frames the seam hands in.

const ORIGIN = 'http://127.0.0.1:41000'
const NONCE = 'nonce-of-the-session-1'

/** Pixels of the frame, 400 × 300 (the page's CSS size: one pixel each), grey with the boxes given painted black. */
function screen(...dark: Array<{ x: number; y: number; width: number; height: number }>): Pixels {
  const data = new Uint8ClampedArray(400 * 300 * 4).fill(128)
  for (const b of dark)
    for (let y = b.y; y < b.y + b.height; y++)
      for (let x = b.x; x < b.x + b.width; x++) data.set([0, 0, 0, 255], (y * 400 + x) * 4)
  return { width: 400, height: 300, data }
}

interface Harness {
  s: ReturnType<typeof createPreviewSession>
  page: PreviewPage
  posts: Array<Record<string, unknown>>
  /** The frame the source gives next (each call takes the next of `frames`, then repeats the last). */
  frames: Pixels[]
  framesTaken: number
  label: LabelRead
  view: { mode: 'fit' | 'desktop' | 'tablet' | 'phone'; scale: number }
  timers: Array<{ fn: () => void; ms: number }>
  /** Bumps the page's epoch as each look is answered, `bumps` times. */
  bumps: number
  /** Answers the pending select with the element. */
  pick(el: ElementReport): Promise<void>
  /** Delivers data to the workbench as from `source` (the frame's window unless said). */
  deliver(data: unknown, source?: unknown, origin?: string): void
  settle(): Promise<void>
  frameWindow: object
}

function harness(o: { noSource?: boolean } = {}): Harness {
  let heard: ((e: HeardMessage) => void) | null = null
  let pendingSelect = false
  const h = {} as Harness
  const frameWindow = {
    postMessage(m: Record<string, unknown>, origin: string) {
      expect(origin).toBe(ORIGIN)
      h.posts.push(m)
      const answer = (a: Record<string, unknown>) => queueMicrotask(() => h.deliver({ conductor: PREVIEW_MARK, nonce: m.nonce, ...a }))
      switch (m.t) {
        case 'hello':
          answer({ t: 'ready', version: 1, hints: [], ...h.page })
          break
        case 'select':
          pendingSelect = true
          break
        case 'describe':
          answer(m.selector === '#none' ? { t: 'described', missing: true, ...h.page } : { t: 'described', element: testElement({ selector: String(m.selector) }), ...h.page })
          break
        case 'look':
          if (h.bumps > 0) {
            h.bumps--
            h.page = { ...h.page, epoch: h.page.epoch + 1 }
          }
          answer({ t: 'looked', ...h.page })
          break
        case 'cancel':
          pendingSelect = false
          answer({ t: 'cancelled' })
          break
      }
    },
  }
  Object.assign(h, {
    page: testPage(),
    posts: [],
    frames: [screen()],
    framesTaken: 0,
    label: { commit: 'a'.repeat(40), content: 'b'.repeat(40), changed: 1, readAt: '2026-11-16T10:00:00Z' },
    view: { mode: 'fit', scale: 1 },
    timers: [],
    bumps: 0,
    frameWindow,
    deliver(data: unknown, source: unknown = frameWindow, origin = ORIGIN) {
      heard?.({ data, source, origin, isTrusted: true })
    },
    async pick(el: ElementReport) {
      expect(pendingSelect, 'a select is waiting').toBe(true)
      pendingSelect = false
      h.deliver({ conductor: PREVIEW_MARK, nonce: NONCE, t: 'selected', element: el, ...h.page })
      await h.settle()
    },
    async settle() {
      for (let i = 0; i < 50; i++) await Promise.resolve()
    },
  })
  const source: ScreenshotSource = o.noSource
    ? { how: 'none', why: 'none here', frame: async () => null, stop() {} }
    : {
        how: 'tab',
        async frame() {
          const f = h.frames[Math.min(h.framesTaken, h.frames.length - 1)]!
          h.framesTaken++
          // A frame of its own each time, as a source's frames are.
          return { width: f.width, height: f.height, data: f.data }
        },
        stop() {},
      }
  h.s = createPreviewSession({
    listen(fn) {
      heard = fn
      return () => (heard = null)
    },
    end: () => ({ window: frameWindow, origin: ORIGIN }),
    label: async () => ({ ...h.label }),
    source: async () => source,
    rect: () => ({ x: 0, y: 0, width: 400, height: 300 }),
    view: () => ({ ...h.view }),
    fallback: () => ({ url: `${ORIGIN}/`, width: 400, height: 300 }),
    nonce: () => NONCE,
    setTimer(fn, ms) {
      h.timers.push({ fn, ms })
      return h.timers.length - 1
    },
    clearTimer(id) {
      const t = h.timers[id as number]
      if (t) t.fn = () => {}
    },
  })
  return h
}

/** The harness with the hello answered. */
async function ready(o: { noSource?: boolean } = {}): Promise<Harness> {
  const h = harness(o)
  h.s.hello()
  await h.settle()
  expect(h.s.state.bridge).toBe('ready')
  return h
}

/** A selection of el (the person's pick), from its select to the draft. */
async function selectOf(h: Harness, el: ElementReport) {
  const done = h.s.select()
  await h.settle()
  await h.pick(el)
  await done
}

const first = testElement({ selector: '#save', box: { x: 10, y: 20, width: 80, height: 30 } })
const second = testElement({ selector: '#next', id: 'next', box: { x: 200, y: 200, width: 50, height: 20 } })

describe('the hello', () => {
  it("says the page does not load the bridge when no ready comes within 3 s, and nothing when one does", async () => {
    const h = harness()
    // The page answers nothing: its window takes no message.
    h.s.hello()
    expect(h.timers.map((t) => t.ms)).toEqual([HELLO_MS])
    expect(HELLO_MS).toBe(3000)
    await h.settle()
    expect(h.s.state.bridge).toBe('ready')
    h.timers[0]!.fn()
    expect(h.s.state.bridge).toBe('ready')

    const quiet = harness()
    ;(quiet.frameWindow as { postMessage: unknown }).postMessage = () => {}
    quiet.s.hello()
    expect(quiet.s.state.bridge).toBe('hello')
    quiet.timers[0]!.fn()
    expect([quiet.s.state.bridge, quiet.s.state.words]).toEqual(['absent', NO_BRIDGE_WORDS])
  })

  it('takes no answer from another window or origin, nor one with another nonce', async () => {
    const h = harness()
    ;(h.frameWindow as { postMessage: unknown }).postMessage = () => {}
    h.s.hello()
    const ready = { conductor: PREVIEW_MARK, nonce: NONCE, t: 'ready', version: 1, hints: [], ...testPage() }
    h.deliver(ready, { other: 'window' })
    h.deliver(ready, h.frameWindow, 'http://127.0.0.1:41001')
    h.deliver({ ...ready, nonce: 'another-nonce-0000000' })
    expect(h.s.state.bridge).toBe('hello')
    h.deliver(ready)
    expect(h.s.state.bridge).toBe('ready')
  })
})

describe('one request in flight', () => {
  it('asks nothing while a selection waits, and a cancel ends it', async () => {
    const h = await ready()
    const sel = h.s.select()
    await h.settle()
    expect(h.posts.map((p) => p.t)).toEqual(['hello', 'select'])
    await h.s.describe('#other')
    await h.s.select()
    expect(h.posts.map((p) => p.t)).toEqual(['hello', 'select'])
    h.s.cancel()
    await sel
    expect(h.posts.map((p) => p.t)).toEqual(['hello', 'select', 'cancel'])
    expect(h.s.state.busy).toBe('')
    expect(h.s.state.drafts).toEqual([])
  })

  it('never takes a selected from another window as the answer', async () => {
    const h = await ready()
    const sel = h.s.select()
    await h.settle()
    h.deliver({ conductor: PREVIEW_MARK, nonce: NONCE, t: 'selected', element: first, ...h.page }, { other: 'window' })
    await h.settle()
    expect(h.s.state.busy).toBe('select')
    h.s.cancel()
    await sel
  })
})

describe('one capture is one moment', () => {
  it('joins a selection on the unchanged page, the same pixels, both stable', async () => {
    const h = await ready()
    await selectOf(h, first)
    await selectOf(h, second)
    expect(h.s.state.drafts).toHaveLength(1)
    const d = h.s.state.drafts[0]!
    expect(d.annotations.map((a) => [a.element?.selector, a.observed?.stable])).toEqual([
      ['#save', true],
      ['#next', true],
    ])
    expect(d.words).toEqual([])
    // One moment: every element's observed is the draft's.
    for (const a of d.annotations) expect([a.observed?.url, a.observed?.epoch, a.observed?.viewport, a.observed?.label]).toEqual([d.moment.url, d.moment.epoch, d.moment.viewport, { commit: h.label.commit, content: h.label.content }])
  })

  const changes: Array<[string, (h: Harness) => void]> = [
    ['a client-side navigation', (h) => (h.page = { ...h.page, url: `${ORIGIN}/next` })],
    ['a device-mode change', (h) => ((h.view = { mode: 'phone', scale: 0.5 }), (h.page = { ...h.page, viewport: { ...h.page.viewport, width: 390, height: 300 } }))],
    ['a scroll', (h) => (h.page = { ...h.page, viewport: { ...h.page.viewport, scrollY: 120 } })],
    ['a page mutation', (h) => (h.page = { ...h.page, epoch: h.page.epoch + 1 })],
    ['a label whose content moved', (h) => (h.label = { ...h.label, content: 'c'.repeat(40) })],
  ]
  for (const [what, change] of changes)
    it(`starts a new draft after ${what}, with its own screenshot and moment, never joining the first`, async () => {
      const h = await ready()
      await selectOf(h, first)
      const before = h.framesTaken
      change(h)
      await selectOf(h, second)
      expect(h.s.state.drafts).toHaveLength(2)
      const [a, b] = h.s.state.drafts
      expect(a!.annotations.map((x) => x.element?.selector)).toEqual(['#save'])
      expect(b!.annotations.map((x) => x.element?.selector)).toEqual(['#next'])
      expect(b!.words).toEqual([CHANGED_WORDS])
      // Its own screenshot: a frame taken for it (and one after it, for its stability).
      expect(h.framesTaken - before).toBe(2)
      expect(b!.shot === a!.shot).toBe(false)
      expect(b!.moment).not.toEqual(a!.moment)
      expect(b!.annotations[0]!.observed!.url).toBe(h.page.url)
    })

  it('retakes the screenshot when a look finds the epoch moved, twice, then says the page does not hold still', async () => {
    const h = await ready()
    h.bumps = 99
    await selectOf(h, first)
    expect(h.s.state.drafts).toEqual([])
    expect(h.s.state.refusal).toBe(RESTLESS_WORDS)
    // Three screenshots in all: the first and two retakes, each followed by a look.
    expect(h.framesTaken).toBe(3)
    expect(h.posts.filter((p) => p.t === 'look')).toHaveLength(3)
  })

  it('keeps the retake once the page held still, at the moment of the element read again', async () => {
    const h = await ready()
    h.bumps = 1
    await selectOf(h, first)
    expect(h.s.state.drafts).toHaveLength(1)
    expect(h.s.state.drafts[0]!.moment.epoch).toBe(h.page.epoch)
    expect(h.posts.filter((p) => p.t === 'describe').map((p) => p.selector)).toEqual(['#save'])
  })

  it('keeps an animated control whose box moved between the screenshot and the next frame stable: false, and a later selection while it moves starts a new draft', async () => {
    const h = await ready()
    // The screenshot, then the frame after it with the control's box moved.
    h.frames = [screen(), screen({ x: 30, y: 25, width: 20, height: 20 })]
    await selectOf(h, first)
    const d = h.s.state.drafts[0]!
    expect(d.annotations[0]!.observed?.stable).toBe(false)
    expect(d.annotations[0]!.words).toBe(MOVING_WORDS)
    // Still moving at the next selection: the new frame differs inside the first's box.
    h.frames = [screen({ x: 40, y: 25, width: 20, height: 20 })]
    h.framesTaken = 0
    await selectOf(h, second)
    expect(h.s.state.drafts).toHaveLength(2)
    expect(h.s.state.drafts[1]!.words).toEqual([CHANGED_WORDS])
  })

  it('marks a control that reports animations playing stable: false, whatever its pixels', async () => {
    const h = await ready()
    await selectOf(h, testElement({ state: { ...first.state, motion: 2 } }))
    expect(h.s.state.drafts[0]!.annotations[0]!.observed?.stable).toBe(false)
  })

  it('puts the next element in a new draft when a hover-styled control looks otherwise once the pointer left it', async () => {
    const h = await ready()
    // Selected while hovered: dark in the screenshot and the frame after it.
    h.frames = [screen({ x: 10, y: 20, width: 80, height: 30 })]
    await selectOf(h, first)
    expect(h.s.state.drafts[0]!.annotations[0]!.observed?.stable).toBe(true)
    // The pointer left it: the next frame shows it plain, no DOM change, no epoch.
    h.frames = [screen()]
    h.framesTaken = 0
    await selectOf(h, second)
    expect(h.s.state.drafts).toHaveLength(2)
    expect(h.s.state.drafts[1]!.annotations.map((a) => a.element?.selector)).toEqual(['#next'])
  })

  it('claims no stability without pixels', async () => {
    const h = await ready({ noSource: true })
    await selectOf(h, first)
    await selectOf(h, second)
    const d = h.s.state.drafts[0]!
    expect([d.shot, d.how, d.why]).toEqual([null, 'none', 'none here'])
    expect(d.annotations.map((a) => a.observed && 'stable' in a.observed)).toEqual([false, false])
  })

  it('starts a draft by selector as a selection does, and says so when nothing matches', async () => {
    const h = await ready()
    await h.s.describe('#hidden-button')
    expect(h.s.state.drafts).toHaveLength(1)
    expect(h.s.state.drafts[0]!.annotations[0]!.element?.selector).toBe('#hidden-button')
    await h.s.describe('#none')
    expect(h.s.state.refusal).toBe('no element of the page matches that selector')
  })

  it('joins no draft again after the frame said hello again (a reload)', async () => {
    const h = await ready()
    await selectOf(h, first)
    h.s.hello()
    await h.settle()
    await selectOf(h, second)
    expect(h.s.state.drafts).toHaveLength(2)
  })
})
