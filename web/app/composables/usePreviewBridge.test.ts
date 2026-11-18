import { describe, expect, it } from 'vitest'
import { ANSWERED_WORDS, ASKING_WORDS, CHANGED_WORDS, MOVING_WORDS, NO_BRIDGE_WORDS, RESTLESS_WORDS, type HeardMessage, type LabelRead } from '~/utils/capture'
import { PREVIEW_MARK, type ElementReport, type PreviewPage } from '~/utils/protocol'
import type { Pixels, ScreenshotSource } from '~/utils/screenshot'
import { testElement, testPage } from '~/utils/testdata/preview'
import { HELLO_AGAIN_MS, HELLO_EVERY_MS, HELLO_MS, NO_BRIDGE_MS, createPreviewSession, type BridgeDeps } from './usePreviewBridge'

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
  /** The session's timers on the harness's clock: each its delay, when it is due, and whether it is still set. */
  timers: Array<{ fn: () => void; ms: number; due: number; live: boolean }>
  /** The harness's clock, in ms since the harness was made: only advance moves it. */
  clock: number
  /** Moves the clock on by ms, running every timer that falls due meanwhile in the order they fall due. */
  advance(ms: number): void
  /** The timers still set. */
  live(): number
  /** Bumps the page's epoch as each look is answered, `bumps` times. */
  bumps: number
  /** Answers the pending select with the element. */
  pick(el: ElementReport): Promise<void>
  /** Delivers data to the workbench as from `source` (the frame's window unless said). */
  deliver(data: unknown, source?: unknown, origin?: string): void
  settle(): Promise<void>
  frameWindow: object
}

function harness(o: { noSource?: boolean; deps?: Partial<BridgeDeps> } = {}): Harness {
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
    clock: 0,
    advance(ms: number) {
      const until = h.clock + ms
      for (;;) {
        const next = h.timers.filter((t) => t.live && t.due <= until).sort((a, b) => a.due - b.due)[0]
        if (!next) break
        h.clock = next.due
        next.live = false
        next.fn()
      }
      h.clock = until
    },
    live: () => h.timers.filter((t) => t.live).length,
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
    ...o.deps,
    setTimer(fn, ms) {
      h.timers.push({ fn, ms, due: h.clock + ms, live: true })
      return h.timers.length - 1
    },
    clearTimer(id) {
      const t = h.timers[id as number]
      if (t) t.live = false
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
  it("says the page has not answered yet when no ready comes within 3 s, that it does not load the bridge at 30 s, and nothing when one does", async () => {
    const h = harness()
    // The page answers nothing: its window takes no message.
    h.s.hello()
    expect(h.timers.map((t) => t.ms)).toEqual([HELLO_AGAIN_MS[0], HELLO_MS])
    expect(HELLO_MS).toBe(3000)
    await h.settle()
    expect(h.s.state.bridge).toBe('ready')
    // The ready ended the hellos and the 3 s: nothing is left to fire.
    expect(h.live()).toBe(0)
    h.advance(60_000)
    expect([h.s.state.bridge, h.posts.filter((p) => p.t === 'hello').length]).toEqual(['ready', 1])

    const quiet = harness()
    ;(quiet.frameWindow as { postMessage: unknown }).postMessage = () => {}
    quiet.s.hello()
    expect(quiet.s.state.bridge).toBe('hello')
    quiet.advance(HELLO_MS - 1)
    expect([quiet.s.state.bridge, quiet.s.state.words]).toEqual(['hello', ''])
    quiet.advance(1)
    expect([quiet.s.state.bridge, quiet.s.state.words, quiet.s.state.unanswered]).toEqual(['absent', ASKING_WORDS, false])
    quiet.advance(NO_BRIDGE_MS - HELLO_MS - 1)
    expect(quiet.s.state.words).toBe(ASKING_WORDS)
    quiet.advance(1)
    expect([quiet.s.state.bridge, quiet.s.state.words, quiet.s.state.unanswered]).toEqual(['absent', NO_BRIDGE_WORDS, true])
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

describe('Capture again', () => {
  it("describes each element of the earlier capture again into a new draft that names it, notes kept, joining nothing before it", async () => {
    const h = await ready()
    await selectOf(h, first)
    await h.s.retake('0123456789abcdef', [
      { selector: '#save', note: 'covered' },
      { selector: '#next', note: '' },
    ])
    expect(h.s.state.drafts).toHaveLength(2)
    const d = h.s.state.drafts[1]!
    expect(d.retakeOf).toBe('0123456789abcdef')
    expect(d.annotations.map((a) => [a.element?.selector, a.note])).toEqual([
      ['#save', 'covered'],
      ['#next', ''],
    ])
    expect(h.posts.filter((p) => p.t === 'describe').map((p) => p.selector)).toEqual(['#save', '#next'])
    // The first draft names nothing, and a selection after the retake is no retake.
    expect(h.s.state.drafts[0]!.retakeOf).toBe('')
    expect(h.s.state.retakeOf).toBe('')
  })

  it('says which elements are not on the page now, and retakes the rest', async () => {
    const h = await ready()
    await h.s.retake('0123456789abcdef', [
      { selector: '#none', note: '' },
      { selector: '#save', note: '' },
    ])
    expect(h.s.state.refusal).toBe('not on this page now, so not retaken: #none')
    expect(h.s.state.drafts.map((d) => [d.retakeOf, d.annotations.length])).toEqual([['0123456789abcdef', 1]])
  })
})

describe('the hello said again, and Capture during it', () => {
  it('is said again within its 3 s, so a bridge that starts after the frame loaded answers', async () => {
    const h = harness()
    const post = (h.frameWindow as { postMessage(m: Record<string, unknown>, o: string): void }).postMessage
    let hellos = 0
    // The bridge's script is not running at the first hello: only a later one is answered.
    ;(h.frameWindow as { postMessage: unknown }).postMessage = (m: Record<string, unknown>, origin: string) => {
      if (m.t === 'hello' && ++hellos === 1) return
      post(m, origin)
    }
    h.s.hello()
    expect(h.s.state.bridge).toBe('hello')
    h.advance(HELLO_AGAIN_MS[0])
    await h.settle()
    expect([hellos, h.s.state.bridge]).toEqual([2, 'ready'])
  })

  it('waits for the hello: selection mode once the page answers, the screenshot alone once it is given up', async () => {
    const h = harness()
    ;(h.frameWindow as { postMessage: unknown }).postMessage = () => {}
    h.s.hello()
    const capture = h.s.start()
    await h.settle()
    expect(h.s.state.drafts).toEqual([])
    // Given up at its 3 s: the screenshot alone.
    h.advance(HELLO_MS)
    await capture
    expect(h.s.state.drafts.map((d) => d.annotations.length)).toEqual([0])

    const ok = harness()
    ok.s.hello()
    const started = ok.s.start()
    await ok.settle()
    expect(ok.s.state.busy).toBe('select')
    await ok.pick(first)
    await started
    expect(ok.s.state.drafts.map((d) => d.annotations.length)).toEqual([1])
  })
})

describe('a bridge that starts late: the hello said while the frame stays on its load', () => {
  /**
   * The frame's page with a bridge not running yet: every hello is kept (its nonce, at the clock's time) and none is answered until
   * `up`; then a hello is answered as the bridge answers it, with the nonce it carried.
   */
  function late(o: { deps?: Partial<BridgeDeps> } = {}) {
    const h = harness(o)
    const post = (h.frameWindow as { postMessage(m: Record<string, unknown>, o: string): void }).postMessage
    const c = { h, up: false, hellos: [] as Array<{ nonce: unknown; at: number }> }
    ;(h.frameWindow as { postMessage: unknown }).postMessage = (m: Record<string, unknown>, origin: string) => {
      if (m.t === 'hello') c.hellos.push({ nonce: m.nonce, at: h.clock })
      if (c.up) post(m, origin)
    }
    return c
  }
  const readyOf = (nonce: string) => ({ conductor: PREVIEW_MARK, nonce, t: 'ready', version: 1, hints: [], ...testPage() })
  /** A nonce of its own for each load. */
  const loads = () => {
    let n = 0
    return () => `nonce-of-load-${++n}-0000000`
  }

  it('takes a ready that comes after the words, and the words go', async () => {
    const c = late()
    c.h.s.hello()
    c.h.advance(HELLO_MS)
    expect([c.h.s.state.bridge, c.h.s.state.words]).toEqual(['absent', ASKING_WORDS])
    // The bridge's script starts now, long after the load: it answers the next hello, said at the pane's own pace.
    c.h.advance(10_000)
    c.up = true
    const said = c.hellos.length
    c.h.advance(HELLO_EVERY_MS)
    await c.h.settle()
    expect(c.hellos.length).toBe(said + 1)
    expect([c.h.s.state.bridge, c.h.s.state.words]).toEqual(['ready', ''])
    // Ready: no hello is said again and no timer is left.
    expect(c.h.live()).toBe(0)
    c.h.advance(60_000)
    expect(c.hellos.length).toBe(said + 1)
    // Capture now selects.
    const sel = c.h.s.start()
    await c.h.settle()
    expect(c.h.s.state.busy).toBe('select')
    await c.h.pick(first)
    await sel
    expect(c.h.s.state.drafts.map((d) => d.annotations.map((a) => a.element?.selector))).toEqual([['#save']])
  })

  it('takes a ready the page sends of itself for the load’s nonce after the words, as a bridge answering an earlier hello late', () => {
    const c = late()
    c.h.s.hello()
    c.h.advance(HELLO_MS)
    expect(c.h.s.state.bridge).toBe('absent')
    c.h.deliver(readyOf(NONCE))
    expect([c.h.s.state.bridge, c.h.s.state.words, c.h.live()]).toEqual(['ready', '', 0])
  })

  it('goes on saying the hello with the load’s one nonce, every second within its 3 s and every two after, one timer at a time', () => {
    const c = late()
    c.h.s.hello()
    for (let i = 0; i < 40; i++) {
      c.h.advance(1000)
      // The 3 s and at most one hello said again are set; after the 3 s, the one hello alone.
      // The words' timer (3 s, then 30 s) and at most one hello said again; past 30 s, the one hello alone.
      expect(c.h.live()).toBe(c.h.clock < NO_BRIDGE_MS ? 2 : 1)
    }
    expect(c.hellos.map((x) => x.at)).toEqual([0, 1000, 2000, ...Array.from({ length: 19 }, (_, i) => 4000 + 2000 * i)])
    expect(new Set(c.hellos.map((x) => x.nonce))).toEqual(new Set([NONCE]))
    expect(c.h.s.state.bridge).toBe('absent')
  })

  it('ends the old load’s hellos at a new load, and drops a ready for the old load’s nonce', () => {
    const c = late({ deps: { nonce: loads() } })
    c.h.s.hello()
    c.h.advance(5000)
    const old = 'nonce-of-load-1-0000000'
    expect(c.hellos.every((x) => x.nonce === old)).toBe(true)
    // The frame loads again: a new nonce, the old load's hellos ended.
    c.h.s.hello()
    const at = c.hellos.length
    c.h.advance(30_000)
    expect(c.hellos.slice(at).map((x) => x.nonce)).toEqual(Array(c.hellos.length - at).fill('nonce-of-load-2-0000000'))
    expect(c.h.live()).toBe(1)
    // A late ready of the old load is dropped; the new load's is taken.
    c.h.deliver(readyOf(old))
    expect(c.h.s.state.bridge).toBe('absent')
    c.h.deliver(readyOf('nonce-of-load-2-0000000'))
    expect([c.h.s.state.bridge, c.h.live()]).toEqual(['ready', 0])
  })

  it('takes a ready long after it said the page does not load the bridge, and the words go', () => {
    const c = late()
    c.h.s.hello()
    c.h.advance(NO_BRIDGE_MS + 10_000)
    expect([c.h.s.state.words, c.h.s.state.unanswered]).toEqual([NO_BRIDGE_WORDS, true])
    c.h.deliver(readyOf(NONCE))
    expect([c.h.s.state.bridge, c.h.s.state.words, c.h.s.state.unanswered, c.h.live()]).toEqual(['ready', '', false, 0])
  })

  /** A capture made while the page has not answered: the screenshot alone, then the page answers. */
  async function askedThenAnswered(frames: Pixels[]) {
    const c = late()
    c.h.frames = frames
    c.h.s.hello()
    c.h.advance(HELLO_MS)
    await c.h.s.start()
    const d = c.h.s.state.drafts[0]!
    expect([d.annotations.length, d.open, d.asked, d.words]).toEqual([0, true, true, []])
    c.h.s.addRegion(d, { x: 1, y: 1, width: 5, height: 5 })
    c.up = true
    c.h.deliver(readyOf(NONCE))
    expect(c.h.s.state.drafts[0]!.words).toEqual([ANSWERED_WORDS])
    await selectOf(c.h, first)
    return c
  }

  it('lets a capture made before the page answered take its first element once it has, where the frame still looks as its screenshot', async () => {
    const c = await askedThenAnswered([screen()])
    expect(c.h.s.state.drafts).toHaveLength(1)
    const d = c.h.s.state.drafts[0]!
    expect(d.annotations.map((a) => [a.kind, a.element?.selector ?? '', a.observed?.stable ?? null])).toEqual([
      ['region', '', null],
      ['element', '#save', true],
    ])
    // The page's moment now: its URL, viewport and epoch, the element observed at it; the answered words gone.
    expect([d.moment.url, d.moment.epoch, d.moment.viewport.width, d.asked, d.words]).toEqual([c.h.page.url, c.h.page.epoch, c.h.page.viewport.width, false, []])
    expect(d.annotations[1]!.observed!.url).toBe(d.moment.url)
    // A second element joins it as a selection joins any draft.
    await selectOf(c.h, second)
    expect(c.h.s.state.drafts.map((x) => x.annotations.length)).toEqual([3])
  })

  it('starts a new capture for the element when the frame no longer looks as the screenshot taken before the page answered', async () => {
    const c = await askedThenAnswered([screen(), screen({ x: 300, y: 200, width: 50, height: 50 })])
    expect(c.h.s.state.drafts).toHaveLength(2)
    const [a, b] = c.h.s.state.drafts
    expect([a!.annotations.map((x) => x.kind), a!.open, a!.words]).toEqual([['region'], false, []])
    expect([b!.annotations.map((x) => x.element?.selector), b!.words]).toEqual([['#save'], [CHANGED_WORDS]])
  })

  it('starts a new capture for the element when only the element’s own box changed since the screenshot taken before the page answered', async () => {
    // 225 pixels inside #save's box: fewer than the whole frame's share of differing pixels, all of the box's.
    const c = await askedThenAnswered([screen(), screen({ x: 20, y: 25, width: 15, height: 15 })])
    expect(c.h.s.state.drafts).toHaveLength(2)
    const [a, b] = c.h.s.state.drafts
    expect([a!.annotations.map((x) => x.kind), a!.open]).toEqual([['region'], false])
    expect([b!.annotations.map((x) => x.element?.selector), b!.words]).toEqual([['#save'], [CHANGED_WORDS]])
  })

  it('starts a new capture for the element when only a region drawn before the page answered changed inside its box', async () => {
    const c = await askedThenAnswered([screen(), screen({ x: 2, y: 2, width: 3, height: 3 })])
    expect(c.h.s.state.drafts.map((d) => d.annotations.map((x) => x.kind))).toEqual([['region'], ['element']])
  })

  it('takes a late ready only from the frame’s window at the preview’s origin', () => {
    const c = late()
    c.h.s.hello()
    c.h.advance(HELLO_MS)
    c.h.deliver(readyOf(NONCE), { other: 'window' })
    c.h.deliver(readyOf(NONCE), c.h.frameWindow, 'http://127.0.0.1:41001')
    c.h.deliver({ ...readyOf(NONCE), version: 2 })
    expect(c.h.s.state.bridge).toBe('absent')
  })

  it('leaves no timer when the frame goes, and takes nothing of its load after', () => {
    const c = late()
    c.h.s.hello()
    c.h.advance(7000)
    c.h.s.gone()
    expect([c.h.live(), c.h.s.state.bridge, c.h.s.state.words]).toEqual([0, 'none', ''])
    const said = c.hellos.length
    c.h.advance(60_000)
    expect(c.hellos.length).toBe(said)
    c.h.deliver(readyOf(NONCE))
    expect(c.h.s.state.bridge).toBe('none')

    const closed = late()
    closed.h.s.hello()
    closed.h.advance(1500)
    closed.h.s.close()
    expect(closed.h.live()).toBe(0)
    closed.h.advance(60_000)
    expect(closed.hellos.length).toBe(2)
  })

  it('stops saying the hello to a frame element since replaced, or once the frame has no window', () => {
    let el: object = { frame: 1 }
    const c = late({ deps: { frameElement: () => el } })
    c.h.s.hello()
    c.h.advance(4000)
    el = { frame: 2 }
    const said = c.hellos.length
    c.h.advance(60_000)
    expect([c.hellos.length, c.h.live()]).toEqual([said, 0])

    // The frame's window gone (the pane frames nothing now): the next hello is not said, and nothing is set again.
    let open = true
    const quiet = { postMessage: () => {} }
    const w = harness({ deps: { end: () => ({ window: open ? quiet : null, origin: ORIGIN }) } })
    w.s.hello()
    w.advance(4000)
    open = false
    w.advance(60_000)
    expect(w.live()).toBe(0)
  })
})

describe('the frame is read with nothing of the workbench over it', () => {
  it('waits for the pane to clear and paint what it draws over the frame before every frame it reads', async () => {
    const order: string[] = []
    const h = harness({ deps: { beforeFrame: async () => void order.push('cleared') } })
    const frame0 = h.frames[0]!
    h.frames = [frame0]
    h.s.hello()
    await h.settle()
    const read = h.framesTaken
    void read
    await selectOf(h, first)
    await selectOf(h, second)
    // Each frame read (the screenshot, its stability frame, the joining frame) after its own clearing.
    expect(order).toEqual(['cleared', 'cleared', 'cleared'])
    expect(h.framesTaken).toBe(3)
  })
})

describe('the screenshot source and the frame element', () => {
  function counted(frames: Array<Pixels | null>) {
    const made: Array<{ stopped: boolean }> = []
    let i = 0
    const source = async (): Promise<ScreenshotSource> => {
      const mine = { stopped: false }
      made.push(mine)
      return { how: 'tab', frame: async () => frames[Math.min(i++, frames.length - 1)] ?? null, stop: () => void (mine.stopped = true) }
    }
    return { made, source }
  }

  it('ends a source made for a frame element since replaced, and makes one for the element there now', async () => {
    let el: object = { frame: 1 }
    const c = counted([screen()])
    const h = harness({ deps: { source: c.source, frameElement: () => el } })
    h.s.hello()
    await h.settle()
    await selectOf(h, first)
    expect(c.made.map((m) => m.stopped)).toEqual([false])
    // Refresh: a new frame element.
    el = { frame: 2 }
    h.s.hello()
    await h.settle()
    await selectOf(h, second)
    expect(c.made.map((m) => m.stopped)).toEqual([true, false])
  })

  it('ends a source that gives no frame, so the next capture asks for one again rather than waiting on it', async () => {
    const c = counted([null])
    const h = harness({ deps: { source: c.source } })
    h.s.hello()
    await h.settle()
    await selectOf(h, first)
    expect(h.s.state.busy).toBe('')
    expect(h.s.state.drafts[0]!.shot).toBeNull()
    expect(c.made.map((m) => m.stopped)).toEqual([true])
    h.s.hello()
    await h.settle()
    await selectOf(h, second)
    expect(c.made.length).toBe(2)
  })
})

describe("a draft's address", () => {
  it('is the page’s address the bridge reports, without the fragment the pane opened the frame with, and any other fragment kept', async () => {
    const opened = `#token=${'c'.repeat(64)}`
    const h = harness({ deps: { opened: () => opened } })
    h.page = { ...h.page, url: `${ORIGIN}/app${opened}` }
    h.s.hello()
    await h.settle()
    await selectOf(h, first)
    const d = h.s.state.drafts[0]!
    expect([d.moment.url, d.annotations[0]!.observed!.url]).toEqual([`${ORIGIN}/app`, `${ORIGIN}/app`])
    h.page = { ...h.page, url: `${ORIGIN}/app#/settings` }
    await selectOf(h, second)
    expect(h.s.state.drafts[1]!.moment.url).toBe(`${ORIGIN}/app#/settings`)
  })

  it('is the address the pane gave the frame, without its fragment', async () => {
    const h = harness({ deps: { fallback: () => ({ url: `${ORIGIN}/app?tab=2#token=${'c'.repeat(64)}`, width: 400, height: 300 }) } })
    ;(h.frameWindow as { postMessage: unknown }).postMessage = () => {}
    h.s.hello()
    h.advance(HELLO_MS)
    await h.s.snap()
    expect(h.s.state.drafts.map((d) => d.moment.url)).toEqual([`${ORIGIN}/app?tab=2`])
  })
})
