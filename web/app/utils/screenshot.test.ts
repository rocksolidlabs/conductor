import { afterEach, describe, expect, it, vi } from 'vitest'
import { MAX_SHOT_BYTES, TAB_ENDED_WORDS, TAB_FRAME_MS, TOO_LARGE_WORDS, encodeShot, fromDesktop, noneSource, sameIn, scaledSize, screenshotSource, tabSource, type Encoder, type Pixels } from './screenshot'

// The screenshot as it is saved (docs/plans/browser-evidence.md, "The screenshot"): at most 2,048 pixels on its long side, PNG, its
// scale recorded; past 4 MiB scaled again by half, once, then dropped with words. And the pixels' check a moment rests on.

/** Pixels of a size, their bytes left out where the fake encoder never reads them. */
const sized = (width: number, height: number): Pixels => ({ width, height, data: new Uint8ClampedArray(0) })

/** An encoder whose PNG of pixels of a size weighs what `bytes` says; it counts its scalings. */
function fake(bytes: (p: Pixels) => number) {
  const scaled: number[] = []
  const enc: Encoder = {
    async scale(p, f) {
      scaled.push(f)
      return sized(scaledSize(p, f).width, scaledSize(p, f).height)
    },
    async png(p) {
      return { size: bytes(p) } as Blob
    },
  }
  return { enc, scaled }
}

describe('encodeShot', () => {
  it('keeps a screenshot within 2,048 pixels as it is, at scale 1', async () => {
    const { enc, scaled } = fake(() => 1000)
    expect(await encodeShot(sized(1280, 800), enc)).toMatchObject({ scale: 1, width: 1280, height: 800 })
    expect(scaled).toEqual([])
  })

  it('scales a larger one to 2,048 on its long side, never up, and records the scale', async () => {
    const { enc, scaled } = fake(() => 1000)
    expect(await encodeShot(sized(4096, 2048), enc)).toMatchObject({ scale: 0.5, width: 2048, height: 1024 })
    expect(await encodeShot(sized(1000, 3000), enc)).toMatchObject({ width: 683, height: 2048 })
    expect(scaled).toEqual([0.5, 2048 / 3000])
  })

  it('halves the scale once when the PNG is past 4 MiB', async () => {
    const { enc, scaled } = fake((p) => (p.width > 1024 ? MAX_SHOT_BYTES + 1 : MAX_SHOT_BYTES))
    expect(await encodeShot(sized(4096, 2048), enc)).toMatchObject({ scale: 0.25, width: 1024, height: 512 })
    expect(scaled).toEqual([0.5, 0.25])
  })

  it('drops it with words when the half is still past 4 MiB', async () => {
    const { enc, scaled } = fake(() => MAX_SHOT_BYTES + 1)
    expect(await encodeShot(sized(2000, 1000), enc)).toEqual({ why: TOO_LARGE_WORDS })
    // Once halved, never twice.
    expect(scaled).toEqual([0.5])
  })
})

/** Pixels of a size all of one grey, `paint` changing some. */
function grey(width: number, height: number, paint: (x: number, y: number) => number | undefined = () => undefined): Pixels {
  const data = new Uint8ClampedArray(width * height * 4)
  for (let y = 0; y < height; y++)
    for (let x = 0; x < width; x++) {
      const v = paint(x, y) ?? 128
      data.set([v, v, v, 255], (y * width + x) * 4)
    }
  return { width, height, data }
}

describe('sameIn: the pixels inside a box', () => {
  const a = grey(100, 100)
  it('holds the same pixels the same, and a change outside the box', () => {
    expect(sameIn(a, grey(100, 100), { x: 10, y: 10, width: 20, height: 20 })).toBe(true)
    expect(sameIn(a, grey(100, 100, (x, y) => (x > 50 && y > 50 ? 0 : undefined)), { x: 10, y: 10, width: 20, height: 20 })).toBe(true)
  })
  it('sees a change inside the box, frames of other sizes, and passes a box wholly outside', () => {
    expect(sameIn(a, grey(100, 100, (x, y) => (x >= 15 && x < 25 && y >= 15 && y < 25 ? 0 : undefined)), { x: 10, y: 10, width: 20, height: 20 })).toBe(false)
    expect(sameIn(a, grey(100, 99), { x: 0, y: 0, width: 10, height: 10 })).toBe(false)
    expect(sameIn(a, grey(100, 100, () => 0), { x: 200, y: 200, width: 10, height: 10 })).toBe(true)
  })
})

describe('noneSource', () => {
  it('gives no frame and says why', async () => {
    const s = noneSource('the screen was not shared')
    expect([s.how, s.why, await s.frame({ x: 0, y: 0, width: 1, height: 1 })]).toEqual(['none', 'the screen was not shared', null])
  })
})

describe("the desktop app's source", () => {
  it("asks the app for the frame's box in the window's CSS pixels and says why it gave none", async () => {
    const asked: unknown[] = []
    const d = { how: 'desktop' as const, frame: async (b: unknown) => (asked.push(b), null), stop() {}, why: () => 'the window is hidden' }
    const s = fromDesktop(d)
    expect(await s.frame({ x: 10, y: 20, width: 300, height: 200 })).toBeNull()
    expect(asked).toEqual([{ left: 10, top: 20, right: 310, bottom: 220 }])
    expect([s.how, s.why]).toEqual(['desktop', 'the window is hidden'])
  })
  it('is chosen over the tab when the app offers one, and the seam over both', async () => {
    const d = { how: 'desktop' as const, frame: async () => null, stop() {}, why: () => '' }
    expect((await screenshotSource({} as Element, { desktop: d })).how).toBe('desktop')
    const seam = noneSource('seam')
    expect(await screenshotSource({} as Element, { seam, desktop: d })).toBe(seam)
  })
})

describe("the tab's capture", () => {
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  /** The browser's tab capture, stubbed: a track cropped to the element, a video that presents no frame (a crop to an element gone). */
  function stubTab() {
    const track = { readyState: 'live', cropTo: async () => {}, stop: () => void (track.readyState = 'ended'), addEventListener: () => {}, removeEventListener: () => {} }
    const stream = { getVideoTracks: () => [track], getTracks: () => [track] }
    vi.stubGlobal('CropTarget', { fromElement: async () => ({}) })
    vi.stubGlobal('navigator', { mediaDevices: { getDisplayMedia: async () => stream } })
    const video = { muted: false, srcObject: null as unknown, videoWidth: 640, videoHeight: 400, play: async () => {}, requestVideoFrameCallback: () => {} }
    vi.stubGlobal('document', { createElement: () => video })
    return track
  }

  it('gives no frame, and says why, when the capture presents none within its bound, rather than waiting for ever', async () => {
    stubTab()
    vi.useFakeTimers()
    const src = await tabSource({} as Element)
    expect(src.how).toBe('tab')
    let got: Pixels | null | 'waiting' = 'waiting'
    void src.frame({ x: 0, y: 0, width: 10, height: 10 }).then((p) => (got = p))
    await vi.advanceTimersByTimeAsync(TAB_FRAME_MS - 1)
    expect(got).toBe('waiting')
    await vi.advanceTimersByTimeAsync(1)
    expect(got).toBeNull()
    expect(src.why).toBe(TAB_ENDED_WORDS)
  })

  it('ends a wait for a frame when the source is stopped', async () => {
    stubTab()
    vi.useFakeTimers()
    const src = await tabSource({} as Element)
    let got: Pixels | null | 'waiting' = 'waiting'
    void src.frame({ x: 0, y: 0, width: 10, height: 10 }).then((p) => (got = p))
    await Promise.resolve()
    src.stop()
    await vi.advanceTimersByTimeAsync(0)
    expect(got).toBeNull()
  })
})
