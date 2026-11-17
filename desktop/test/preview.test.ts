import { describe, expect, it, vi } from 'vitest'
import { capturePreview, frameOriginRefusal, injectBridge, MAX_PREVIEW_PNG, TOO_LARGE, previewFrames, rectRefusal, type PreviewContents, type PreviewFrame, type PreviewImage, type Rect } from '../src/preview'

const workbench = 'http://127.0.0.1:4312'
const preview = 'http://127.0.0.1:5173'
let ids = 1

type Fake = PreviewFrame & { origin: string; url: string; detached: boolean; ran: string[]; framesInSubtree: Fake[]; fail?: boolean }

/** A frame of a fake window: its origin, its page, its parent; what was run in it is kept. */
function frame(origin: string, url: string, parent: Fake | null): Fake {
  const f: Fake = {
    origin,
    url,
    parent,
    frameTreeNodeId: ids++,
    detached: false,
    ran: [],
    framesInSubtree: [],
    async executeJavaScript(code: string) {
      if (f.fail) throw new Error('frame gone')
      f.ran.push(code)
    },
  }
  return f
}

/** A fake window: the workbench's main frame and the frames under it (its own subtree listed, as Electron lists it). */
function contents(children: (main: Fake) => Fake[], capture?: (rect: Rect) => Promise<PreviewImage>, zoom = 1) {
  const main = frame(workbench, workbench + '/preview?run=r1&task=build&attempt=1', null)
  main.framesInSubtree = [main, ...children(main)]
  const wc = { id: ids++, mainFrame: main, getZoomFactor: () => zoom, capturePage: vi.fn(capture ?? (async () => image(100, 50))) }
  return wc as typeof wc & PreviewContents
}

/** A fake NativeImage of w x h whose PNG is bytes long, a number or one of its size (64 bytes by default). */
function image(w: number, h: number, bytes?: number | ((w: number, h: number) => number)): PreviewImage & { resized: Array<{ width: number; height: number }> } {
  const resized: Array<{ width: number; height: number }> = []
  const img = {
    resized,
    isEmpty: () => w === 0 || h === 0,
    getSize: () => ({ width: w, height: h }),
    resize: (o: { width: number; height: number }) => {
      resized.push({ width: o.width, height: o.height })
      const next = image(o.width, o.height, bytes)
      next.resized.unshift(...resized)
      return next
    },
    toPNG: () => new Uint8Array(typeof bytes === 'function' ? bytes(w, h) : (bytes ?? 64)),
  }
  return img
}

const script = '(function (conductorWorkbenchOrigins) {\n/* the bridge */\n})(["http://127.0.0.1:4312"]);\n'

/** The server's answer for its bridge, as fetch gives it. */
function served(over: Partial<{ ok: boolean; status: number; url: string; type: string; length: string; body: string }> = {}) {
  const r = { ok: true, status: 200, url: workbench + '/preview-bridge.js', type: 'text/javascript; charset=utf-8', length: '', body: script, ...over }
  return vi.fn(async (_url: string) => ({
    ok: r.ok,
    status: r.status,
    url: r.url,
    headers: { get: (n: string) => (n === 'content-type' ? r.type : n === 'content-length' ? r.length || null : null) },
    text: async () => r.body,
  }))
}

describe('the frames the bridge goes into', () => {
  it("are the workbench main frame's own children at the preview's origin, showing a page of it", () => {
    let found: Fake | null = null
    let nested: Fake | null = null
    const wc = contents((main) => {
      found = frame(preview, preview + '/app', main)
      nested = frame(preview, preview + '/inner', found)
      const other = frame('http://127.0.0.1:6000', 'http://127.0.0.1:6000/', main)
      const blank = frame(preview, 'about:blank', main)
      const gone = frame(preview, preview + '/old', main)
      gone.detached = true
      return [found, nested, other, blank, gone]
    })
    expect(previewFrames(wc, preview)).toEqual([found])
    // The main frame itself, at the workbench's origin, is never one.
    expect(previewFrames(wc, workbench)).toEqual([])
    expect(nested).not.toBeNull()
  })
})

describe('the origins the shell puts the bridge in for', () => {
  it('are pages on the loopback, never the workbench under any of its names', () => {
    for (const o of [preview, 'http://localhost:3000', 'http://[::1]:8080', 'https://127.0.0.1:8443', 'http://127.0.0.2:5173']) expect(frameOriginRefusal(o, workbench), o).toBe('')
    for (const o of [workbench, 'http://localhost:4312', 'http://[::1]:4312', 'http://127.0.0.5:4312']) expect(frameOriginRefusal(o, workbench), o).toBe('refused: this is Conductor itself')
  })
  it('refuse a page on another machine, and anything that is not an origin as a browser writes it', () => {
    for (const o of ['https://example.com', 'http://192.168.1.5:3000', 'http://10.0.0.1', 'http://localhost.example:3000', 'http://127.0.0.1.example']) {
      expect(frameOriginRefusal(o, workbench), o).toBe('refused: only a page on this machine is previewed')
    }
    for (const o of [preview + '/', preview + '/x', 'http://me@127.0.0.1:5173', 'HTTP://127.0.0.1:5173', 'file:///etc', 'javascript:1', 'null', '', 'x'.repeat(3000), 42, null, undefined, { origin: preview }]) {
      expect(frameOriginRefusal(o, workbench), String(o)).toBe('refused: not an origin')
    }
  })
})

describe('injectBridge', () => {
  it("runs the server's own bridge in the preview frame, fetched from the server's origin alone", async () => {
    let f: Fake | null = null
    const wc = contents((main) => [(f = frame(preview, preview + '/', main))])
    const fetch = served()
    expect(await injectBridge(wc, preview, { origin: () => workbench, fetch })).toEqual({ injected: 1 })
    expect(fetch.mock.calls).toEqual([[workbench + '/preview-bridge.js']])
    expect(f!.ran).toEqual([script])
  })
  it('refuses the workbench and a remote page before it fetches or runs anything', async () => {
    let f: Fake | null = null
    const wc = contents((main) => [(f = frame(workbench, workbench + '/', main)), frame('https://example.com', 'https://example.com/', main)])
    const fetch = served()
    expect(await injectBridge(wc, workbench, { origin: () => workbench, fetch })).toEqual({ refused: 'refused: this is Conductor itself' })
    expect(await injectBridge(wc, 'https://example.com', { origin: () => workbench, fetch })).toEqual({ refused: 'refused: only a page on this machine is previewed' })
    expect(fetch).not.toHaveBeenCalled()
    expect(f!.ran).toEqual([])
  })
  it('answers none when no frame shows the origin, and fetches nothing', async () => {
    const wc = contents((main) => [frame('http://127.0.0.1:6000', 'http://127.0.0.1:6000/', main)])
    const fetch = served()
    expect(await injectBridge(wc, preview, { origin: () => workbench, fetch })).toEqual({ injected: 0 })
    expect(fetch).not.toHaveBeenCalled()
  })
  it('refuses a bridge the server did not give whole, from its own origin', async () => {
    const cases: Array<[ReturnType<typeof served>, string]> = [
      [served({ ok: false, status: 404 }), 'refused: the server has no bridge to give'],
      [served({ url: 'http://127.0.0.1:9999/preview-bridge.js' }), 'refused: the server has no bridge to give'],
      [served({ type: 'text/html' }), 'refused: the server has no bridge to give'],
      [served({ length: String(2 << 20) }), 'refused: the bridge is larger than the shell runs'],
      [served({ body: 'x'.repeat((1 << 20) + 1) }), 'refused: the bridge is larger than the shell runs'],
      [vi.fn(async () => Promise.reject(new Error('refused'))) as never, 'refused: the server did not answer for its bridge'],
    ]
    for (const [fetch, words] of cases) {
      let f: Fake | null = null
      const wc = contents((main) => [(f = frame(preview, preview + '/', main))])
      expect(await injectBridge(wc, preview, { origin: () => workbench, fetch })).toEqual({ refused: words })
      expect(f!.ran).toEqual([])
    }
  })
  it('passes over a frame that left the origin while the bridge was fetched, one that left it while another ran it, and one that went', async () => {
    let moved: Fake | null = null
    let first: Fake | null = null
    let later: Fake | null = null
    let gone: Fake | null = null
    let kept: Fake | null = null
    const wc = contents((main) => [
      (moved = frame(preview, preview + '/', main)),
      (first = frame(preview, preview + '/a', main)),
      (later = frame(preview, preview + '/b', main)),
      (gone = frame(preview, preview + '/c', main)),
      (kept = frame(preview, preview + '/d', main)),
    ])
    gone!.fail = true
    // The second frame navigates elsewhere while the first runs the bridge.
    const run = first!.executeJavaScript.bind(first)
    first!.executeJavaScript = async (code: string) => {
      later!.origin = 'https://example.com'
      later!.url = 'https://example.com/'
      await run(code)
    }
    const fetch = vi.fn(async (url: string) => {
      moved!.origin = 'https://example.com'
      moved!.url = 'https://example.com/'
      return served()(url)
    })
    expect(await injectBridge(wc, preview, { origin: () => workbench, fetch })).toEqual({ injected: 2 })
    expect(moved!.ran).toEqual([])
    expect(later!.ran).toEqual([])
    expect(first!.ran).toEqual([script])
    expect(kept!.ran).toEqual([script])
  })
})

describe('the rectangle of a screenshot', () => {
  const size = { width: 1000, height: 800 }
  it('is whole CSS pixels, at least one each way, inside the content at its zoom', () => {
    expect(rectRefusal({ x: 0, y: 0, width: 1000, height: 800 }, size, 1)).toBe('')
    expect(rectRefusal({ x: 10, y: 20, width: 1, height: 1 }, size, 1)).toBe('')
    expect(rectRefusal({ x: 0, y: 0, width: 800, height: 640 }, size, 1.25)).toBe('')
  })
  it('refuses fractions, nothing, a rectangle past the content, and what is not one', () => {
    expect(rectRefusal({ x: 0.5, y: 0, width: 10, height: 10 }, size, 1)).toBe('refused: the rectangle is not in whole pixels')
    expect(rectRefusal({ x: 0, y: 0, width: '10', height: 10 }, size, 1)).toBe('refused: the rectangle is not in whole pixels')
    expect(rectRefusal({ x: 0, y: 0, width: Number.NaN, height: 10 }, size, 1)).toBe('refused: the rectangle is not in whole pixels')
    expect(rectRefusal({ x: 0, y: 0, width: 10 }, size, 1)).toBe('refused: the rectangle is not in whole pixels')
    expect(rectRefusal({ x: 0, y: 0, width: 0, height: 10 }, size, 1)).toBe('refused: the rectangle is smaller than a pixel')
    expect(rectRefusal({ x: 0, y: 0, width: 10, height: -1 }, size, 1)).toBe('refused: the rectangle is smaller than a pixel')
    expect(rectRefusal({ x: -1, y: 0, width: 10, height: 10 }, size, 1)).toBe("refused: the rectangle is outside the window's content")
    expect(rectRefusal({ x: 991, y: 0, width: 10, height: 10 }, size, 1)).toBe("refused: the rectangle is outside the window's content")
    expect(rectRefusal({ x: 0, y: 0, width: 801, height: 10 }, size, 1.25)).toBe("refused: the rectangle is outside the window's content")
    for (const r of [null, undefined, 'rect', 7, []]) expect(rectRefusal(r, size, 1), String(r)).toBe('refused: not a rectangle')
  })
})

describe('capturePreview', () => {
  const deps = { contentSize: () => ({ width: 1000, height: 800 }) }
  it("takes the rectangle's pixels at the window's zoom, as PNG, with its scale", async () => {
    const wc = contents(() => [], async () => image(300, 150), 1.5)
    const got = await capturePreview(wc, { x: 10, y: 10, width: 100, height: 50 }, deps)
    expect(wc.capturePage.mock.calls).toEqual([[{ x: 15, y: 15, width: 150, height: 75 }]])
    expect(got).toEqual({ png: new Uint8Array(64), width: 300, height: 150, scale: 3 })
  })
  it('scales an image down to 2,048 pixels on its long side', async () => {
    const wc = contents(() => [], async () => image(4000, 1000))
    const got = await capturePreview(wc, { x: 0, y: 0, width: 1000, height: 250 }, deps)
    expect(got).toMatchObject({ width: 2048, height: 512, scale: 2.048 })
  })
  it('takes a PNG past 4 MiB again at half its size, as the workbench encodes one, its scale halved with it', async () => {
    // Over the bound by a byte at its full size, within it at half (a PNG's bytes go with its pixels).
    const perPixel = (MAX_PREVIEW_PNG + 1) / (2000 * 1000)
    const wc = contents(() => [], async () => image(2000, 1000, (w, h) => Math.ceil(w * h * perPixel)))
    const got = await capturePreview(wc, { x: 0, y: 0, width: 1000, height: 500 }, deps)
    expect(got).toMatchObject({ width: 1000, height: 500, scale: 1 })
    expect((got as { png: Uint8Array }).png.byteLength).toBeLessThanOrEqual(MAX_PREVIEW_PNG)
    // At the bound, it is kept as it is.
    const at = contents(() => [], async () => image(100, 100, MAX_PREVIEW_PNG))
    expect(await capturePreview(at, { x: 0, y: 0, width: 100, height: 100 }, deps)).toMatchObject({ width: 100, height: 100, scale: 1 })
  })
  it('refuses a PNG past 4 MiB even at half its size, an empty image, and a rectangle it refuses, taking nothing then', async () => {
    const big = contents(() => [], async () => image(100, 100, MAX_PREVIEW_PNG + 1))
    expect(await capturePreview(big, { x: 0, y: 0, width: 100, height: 100 }, deps)).toEqual({ refused: TOO_LARGE })
    const empty = contents(() => [], async () => image(0, 0))
    expect(await capturePreview(empty, { x: 0, y: 0, width: 100, height: 100 }, deps)).toEqual({ refused: 'refused: the window gave no pixels' })
    const outside = contents(() => [])
    expect(await capturePreview(outside, { x: 0, y: 0, width: 2000, height: 100 }, deps)).toEqual({ refused: "refused: the rectangle is outside the window's content" })
    expect(outside.capturePage).not.toHaveBeenCalled()
    const closed = contents(() => [])
    expect(await capturePreview(closed, { x: 0, y: 0, width: 10, height: 10 }, { contentSize: () => null })).toEqual({ refused: 'refused: the window has no content to take' })
    expect(closed.capturePage).not.toHaveBeenCalled()
  })
  it('takes one at a time per window: a second asked meanwhile is refused, another window is not', async () => {
    let release: (img: PreviewImage) => void = () => {}
    const held = new Promise<PreviewImage>((r) => (release = r))
    const wc = contents(() => [], () => held)
    const first = capturePreview(wc, { x: 0, y: 0, width: 100, height: 50 }, deps)
    expect(await capturePreview(wc, { x: 0, y: 0, width: 100, height: 50 }, deps)).toEqual({ refused: 'refused: a screenshot is being taken' })
    const other = contents(() => [])
    expect(await capturePreview(other, { x: 0, y: 0, width: 100, height: 50 }, deps)).toMatchObject({ width: 100 })
    release(image(100, 50))
    expect(await first).toMatchObject({ width: 100, height: 50, scale: 1 })
    expect(wc.capturePage).toHaveBeenCalledTimes(1)
    // Once the first is done, the window takes the next.
    expect(await capturePreview(wc, { x: 0, y: 0, width: 100, height: 50 }, deps)).toMatchObject({ width: 100 })
  })
  it('ends its turn when the window fails to give pixels', async () => {
    let fail = true
    const wc = contents(() => [], async () => {
      if (fail) throw new Error('gone')
      return image(10, 10)
    })
    expect(await capturePreview(wc, { x: 0, y: 0, width: 10, height: 10 }, deps)).toEqual({ refused: 'refused: the window gave no pixels' })
    fail = false
    expect(await capturePreview(wc, { x: 0, y: 0, width: 10, height: 10 }, deps)).toMatchObject({ width: 10 })
  })
})
