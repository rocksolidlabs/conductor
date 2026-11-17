import { describe, expect, it, vi } from 'vitest'
import type { DesktopBridge, PreviewRect } from './desktop'
import { desktopPreviewBridge, desktopSource } from './screenshotDesktop'

/** A bitmap stand-in: the PNG it was decoded from. */
const decode = async (png: Uint8Array) => ({ png: Array.from(png) }) as unknown as ImageBitmap

function app(capturePreview?: DesktopBridge['capturePreview'], previewBridge?: DesktopBridge['previewBridge']) {
  return { capturePreview, previewBridge } as unknown as DesktopBridge
}

describe("the desktop app's screenshot source", () => {
  it('is there only in an app that takes one', () => {
    expect(desktopSource(null)).toBeNull()
    expect(desktopSource(app())).toBeNull()
    expect(desktopSource(app(async () => ({ refused: 'x' })))?.how).toBe('desktop')
  })

  it("asks the app for the frame's box in whole pixels and gives the PNG as a bitmap", async () => {
    const asked: PreviewRect[] = []
    const src = desktopSource(app(async (rect) => (asked.push(rect), { png: new Uint8Array([1, 2]), width: 200, height: 100, scale: 2 })), decode)!
    expect(await src.frame({ left: 10.4, top: 20.6, right: 110.2, bottom: 70 })).toEqual({ png: [1, 2] })
    expect(asked).toEqual([{ x: 10, y: 20, width: 101, height: 50 }])
    expect(src.why()).toBe('')
  })

  it("says why when the app refuses, gives nothing, or gives what is not a screenshot", async () => {
    let answer: unknown = { refused: 'refused: a screenshot is being taken' }
    const src = desktopSource(app(async () => answer as never), decode)!
    expect(await src.frame({ left: 0, top: 0, right: 10, bottom: 10 })).toBeNull()
    expect(src.why()).toBe('refused: a screenshot is being taken')
    for (const a of [null, {}, { png: 'x', width: 1, height: 1, scale: 1 }, { png: new Uint8Array([1]), width: 1.5, height: 1, scale: 1 }, { png: new Uint8Array(), width: 1, height: 1, scale: 1 }]) {
      answer = a
      expect(await src.frame({ left: 0, top: 0, right: 10, bottom: 10 })).toBeNull()
      expect(src.why()).toBe('the desktop app gave no screenshot')
    }
    expect(await src.frame({ left: 5, top: 5, right: 5, bottom: 50 })).toBeNull()
    expect(src.why()).toBe('the frame has no pixels to take')
    const thrown = desktopSource(app(async () => Promise.reject(new Error("refused: not the workbench's main frame"))), decode)!
    expect(await thrown.frame({ left: 0, top: 0, right: 10, bottom: 10 })).toBeNull()
    expect(thrown.why()).toBe('the desktop app gave no screenshot')
    const broken = desktopSource(app(async () => ({ png: new Uint8Array([9]), width: 1, height: 1, scale: 1 })), async () => Promise.reject(new Error('bad png')))!
    expect(await broken.frame({ left: 0, top: 0, right: 10, bottom: 10 })).toBeNull()
    expect(broken.why()).toBe('the desktop app gave a screenshot that is not a PNG')
  })

  it('takes frames asked together in turn, never two at once', async () => {
    let inFlight = 0
    let most = 0
    const releases: Array<() => void> = []
    const capture = vi.fn(async () => {
      inFlight++
      most = Math.max(most, inFlight)
      await new Promise<void>((r) => releases.push(r))
      inFlight--
      return { png: new Uint8Array([1]), width: 1, height: 1, scale: 1 }
    })
    const src = desktopSource(app(capture), decode)!
    const a = src.frame({ left: 0, top: 0, right: 1, bottom: 1 })
    const b = src.frame({ left: 0, top: 0, right: 1, bottom: 1 })
    await vi.waitFor(() => expect(releases).toHaveLength(1))
    releases[0]!()
    await a
    await vi.waitFor(() => expect(releases).toHaveLength(2))
    releases[1]!()
    await b
    expect(capture).toHaveBeenCalledTimes(2)
    expect(most).toBe(1)
  })
})

describe('the bridge the desktop app puts into the preview', () => {
  it("is asked for the frame's origin, and nothing is asked outside the app", async () => {
    const ask = vi.fn(async (_o: string) => ({ injected: 1 }))
    expect(await desktopPreviewBridge('http://127.0.0.1:5173/app?x=1#token=abc', app(undefined, ask), 'http://127.0.0.1:4312/preview')).toEqual({ injected: 1 })
    expect(ask.mock.calls).toEqual([['http://127.0.0.1:5173']])
    expect(await desktopPreviewBridge('http://127.0.0.1:5173/', null, 'http://127.0.0.1:4312/')).toBeNull()
    expect(await desktopPreviewBridge('http://127.0.0.1:5173/', app(), 'http://127.0.0.1:4312/')).toBeNull()
  })
  it("says why when the frame has no origin or the app refuses", async () => {
    const ask = vi.fn(async (_o: string) => ({ refused: 'refused: this is Conductor itself' }))
    expect(await desktopPreviewBridge('about:blank', app(undefined, ask), 'http://127.0.0.1:4312/')).toEqual({ refused: 'the preview has no origin to put the bridge in' })
    expect(ask).not.toHaveBeenCalled()
    expect(await desktopPreviewBridge('http://127.0.0.1:4312/', app(undefined, ask), 'http://127.0.0.1:4312/')).toEqual({ refused: 'refused: this is Conductor itself' })
    const thrown = vi.fn(async () => Promise.reject(new Error("refused: not the workbench's main frame")))
    expect(await desktopPreviewBridge('http://127.0.0.1:5173/', app(undefined, thrown), 'http://127.0.0.1:4312/')).toEqual({ refused: 'the desktop app put no bridge in' })
  })
})
