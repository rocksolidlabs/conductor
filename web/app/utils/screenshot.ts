import type { CaptureRect } from './capture'
import { desktopBridge, type DesktopBridge } from './desktop'
import { desktopSource, type DesktopSource } from './screenshotDesktop'

/**
 * The preview's pixels (docs/plans/browser-evidence.md, "The screenshot"): the desktop shell's capture of the frame's box when the
 * desktop app offers one (utils/screenshotDesktop.ts), else in a browser that has them
 * the tab's own capture with the person's permission, cropped to the frame (getDisplayMedia with preferCurrentTab, Region Capture's
 * CropTarget and cropTo), the track kept for the draft and stopped at Save, Escape or the pane's close, else none, said in the
 * capture. A source's `frame` gives the raw pixels the moment's comparisons use; the screenshot is scaled and encoded from them
 * (encodeShot): at most 2,048 pixels on its long side, PNG, its scale recorded; one past 4 MiB is scaled again by half, once, then
 * dropped with words.
 */

/** Raw pixels: RGBA, row by row, as an ImageData holds them. */
export interface Pixels {
  width: number
  height: number
  data: Uint8ClampedArray
}

/** Where a capture's screenshot comes from, and how its frames are read. */
export interface ScreenshotSource {
  how: 'desktop' | 'tab' | 'none'
  /** Why there is no screenshot, for `none`. */
  why?: string
  /** The frame's pixels now; rect is the frame's box in the window's CSS pixels. Null when no frame could be read. */
  frame(rect: CaptureRect): Promise<Pixels | null>
  /** Ends the source (the tab's track stopped). */
  stop(): void
}

/** The long side a screenshot is scaled to at most, and the size past which it is halved once, then dropped. */
export const MAX_SHOT_SIDE = 2048
export const MAX_SHOT_BYTES = 4 * 1024 * 1024

/** How pixels are scaled and encoded: the canvas's in a browser, a fake in the tests. */
export interface Encoder {
  scale(p: Pixels, factor: number): Promise<Pixels>
  png(p: Pixels): Promise<Blob>
}

export type EncodedShot = { blob: Blob; scale: number; width: number; height: number } | { why: string }

/** The words of a screenshot dropped at its size. */
export const TOO_LARGE_WORDS = 'the screenshot was more than 4 MiB even at half its size, so the capture is kept without it'

/**
 * The screenshot as it is saved: p scaled to at most MAX_SHOT_SIDE on its long side (never up), encoded as PNG, with the factor it was
 * scaled by; past MAX_SHOT_BYTES scaled again to half that factor, once; past it still, dropped with words.
 */
export async function encodeShot(p: Pixels, enc: Encoder): Promise<EncodedShot> {
  const long = Math.max(p.width, p.height)
  if (long <= 0) return { why: 'the screenshot was empty' }
  let factor = Math.min(1, MAX_SHOT_SIDE / long)
  for (let tries = 0; tries < 2; tries++) {
    const scaled = factor === 1 ? p : await enc.scale(p, factor)
    const blob = await enc.png(scaled)
    if (blob.size <= MAX_SHOT_BYTES) return { blob, scale: factor, width: scaled.width, height: scaled.height }
    factor /= 2
  }
  return { why: TOO_LARGE_WORDS }
}

/** The size pixels take scaled by factor, each side at least 1. */
export function scaledSize(p: { width: number; height: number }, factor: number): { width: number; height: number } {
  return { width: Math.max(1, Math.round(p.width * factor)), height: Math.max(1, Math.round(p.height * factor)) }
}

/** A box in pixels, cut to the image and to whole pixels: null when nothing of it is inside. */
function clip(p: { width: number; height: number }, box: CaptureRect): { x0: number; y0: number; x1: number; y1: number } | null {
  const x0 = Math.max(0, Math.floor(box.x))
  const y0 = Math.max(0, Math.floor(box.y))
  const x1 = Math.min(p.width, Math.ceil(box.x + box.width))
  const y1 = Math.min(p.height, Math.ceil(box.y + box.height))
  return x1 > x0 && y1 > y0 ? { x0, y0, x1, y1 } : null
}

/**
 * Whether two frames show the same pixels inside box (in their pixels): frames of other sizes never do; a box wholly outside them is
 * the same (nothing of it is drawn). A pixel differs by more than `tolerance` in a channel; at most `share` of the box's pixels may
 * (a cursor's blink, a glyph's antialiasing), a hover's colour or a moving control many more.
 */
export function sameIn(a: Pixels, b: Pixels, box: CaptureRect, tolerance = 8, share = 0.002): boolean {
  if (a.width !== b.width || a.height !== b.height) return false
  const c = clip(a, box)
  if (!c) return true
  let differ = 0
  const most = Math.floor((c.x1 - c.x0) * (c.y1 - c.y0) * share)
  const ad = a.data
  const bd = b.data
  for (let y = c.y0; y < c.y1; y++) {
    let i = (y * a.width + c.x0) * 4
    for (let x = c.x0; x < c.x1; x++, i += 4) {
      if (Math.abs(ad[i]! - bd[i]!) > tolerance || Math.abs(ad[i + 1]! - bd[i + 1]!) > tolerance || Math.abs(ad[i + 2]! - bd[i + 2]!) > tolerance || Math.abs(ad[i + 3]! - bd[i + 3]!) > tolerance) {
        if (++differ > most) return false
      }
    }
  }
  return true
}

/** A source with no pixels, and why. */
export function noneSource(why: string): ScreenshotSource {
  return { how: 'none', why, frame: async () => null, stop: () => {} }
}

// ---- The canvas: scaling, encoding, decoding (a browser's) ----

type Canvas2D = { canvas: HTMLCanvasElement | OffscreenCanvas; ctx: CanvasRenderingContext2D | OffscreenCanvasRenderingContext2D }

function canvas(width: number, height: number): Canvas2D {
  if (typeof OffscreenCanvas !== 'undefined') {
    const c = new OffscreenCanvas(width, height)
    return { canvas: c, ctx: c.getContext('2d')! }
  }
  const c = document.createElement('canvas')
  c.width = width
  c.height = height
  return { canvas: c, ctx: c.getContext('2d')! }
}

function put(p: Pixels): Canvas2D {
  const c = canvas(p.width, p.height)
  c.ctx.putImageData(new ImageData(new Uint8ClampedArray(p.data), p.width, p.height), 0, 0)
  return c
}

async function blobOf(c: Canvas2D): Promise<Blob> {
  if ('convertToBlob' in c.canvas) return c.canvas.convertToBlob({ type: 'image/png' })
  const el = c.canvas as HTMLCanvasElement
  return new Promise((resolve, reject) => el.toBlob((b) => (b ? resolve(b) : reject(new Error('the screenshot could not be encoded'))), 'image/png'))
}

/** The browser's encoder: a canvas scales (smoothed) and encodes as PNG. */
export function canvasEncoder(): Encoder {
  return {
    async scale(p, factor) {
      const src = put(p)
      const size = scaledSize(p, factor)
      const dst = canvas(size.width, size.height)
      dst.ctx.imageSmoothingEnabled = true
      dst.ctx.imageSmoothingQuality = 'high'
      dst.ctx.drawImage(src.canvas as CanvasImageSource, 0, 0, size.width, size.height)
      return { width: size.width, height: size.height, data: dst.ctx.getImageData(0, 0, size.width, size.height).data }
    },
    png: (p) => blobOf(put(p)),
  }
}

/** A bitmap's pixels, the bitmap closed once read. */
export function pixelsOf(bmp: ImageBitmap): Pixels {
  try {
    const c = canvas(bmp.width, bmp.height)
    c.ctx.drawImage(bmp, 0, 0)
    return { width: bmp.width, height: bmp.height, data: c.ctx.getImageData(0, 0, bmp.width, bmp.height).data }
  } finally {
    bmp.close()
  }
}

/** A PNG's pixels (the seam's screenshots come as PNG). */
export async function decodePng(png: Blob | Uint8Array | ArrayBuffer): Promise<Pixels> {
  const blob = png instanceof Blob ? png : new Blob([png as BlobPart], { type: 'image/png' })
  return pixelsOf(await createImageBitmap(blob))
}

/** A base64 text's bytes. */
export function fromBase64(s: string): Uint8Array {
  const bin = atob(s)
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out
}

// ---- The tab's path ----

/** The words of a tab capture the person declined or the browser refused. */
export const DECLINED_WORDS = 'the screen was not shared, so the capture has no screenshot'
/** How long the tab's capture is given to present a frame of the preview, and what is said when it presents none. */
export const TAB_FRAME_MS = 1000
export const TAB_ENDED_WORDS = 'the shared tab gave no picture of the preview within a second, so this capture has no screenshot: capture again to share the tab anew'
/** The words where a capture cropped to the frame cannot be taken. */
export const NO_TAB_WORDS = 'a picture cropped to the frame cannot be taken here (the desktop app and Chromium can take one), so the capture has no screenshot'

interface CropTargetStatic {
  fromElement(el: Element): Promise<unknown>
}
type CroppableTrack = MediaStreamTrack & { cropTo?(target: unknown): Promise<void> }

/**
 * The tab's own capture, cropped to el (the frame): asked of the person once (getDisplayMedia with preferCurrentTab), kept until stop.
 * Each frame is the video's latest, drawn to a canvas. A browser without Region Capture, or a person who declines, gives none.
 */
export async function tabSource(el: Element): Promise<ScreenshotSource> {
  const w = globalThis as unknown as { CropTarget?: CropTargetStatic; navigator?: Navigator }
  const media = w.navigator?.mediaDevices
  if (!w.CropTarget || !media?.getDisplayMedia) return noneSource(NO_TAB_WORDS)
  let stream: MediaStream
  try {
    stream = await media.getDisplayMedia({ video: { displaySurface: 'browser' }, audio: false, preferCurrentTab: true, selfBrowserSurface: 'include' } as DisplayMediaStreamOptions)
  } catch {
    return noneSource(DECLINED_WORDS)
  }
  const track = stream.getVideoTracks()[0] as CroppableTrack | undefined
  const stop = () => stream.getTracks().forEach((t) => t.stop())
  if (!track?.cropTo) {
    stop()
    return noneSource(NO_TAB_WORDS)
  }
  try {
    await track.cropTo(await w.CropTarget.fromElement(el))
  } catch {
    stop()
    return noneSource(NO_TAB_WORDS)
  }
  const video = document.createElement('video')
  video.muted = true
  video.srcObject = stream
  await video.play().catch(() => {})
  // The waits for a frame now: each ends with null at its bound, at the track's end, or at stop.
  const waits = new Set<() => void>()
  const ended = () => waits.forEach((end) => end())
  track.addEventListener?.('ended', ended)
  const src: ScreenshotSource = {
    how: 'tab',
    async frame() {
      if (track.readyState !== 'live' || !video.videoWidth) {
        src.why = TAB_ENDED_WORDS
        return null
      }
      // The next frame the video presents, so the pixels are of a moment after the ask; none within the bound (a track cropped to a
      // frame element no longer on the page sends none) is no screenshot, said, never a wait without end.
      const presented = await new Promise<boolean>((resolve) => {
        let done = false
        const end = (ok: boolean) => {
          if (done) return
          done = true
          clearTimeout(bound)
          waits.delete(miss)
          resolve(ok)
        }
        const miss = () => end(false)
        const bound = setTimeout(miss, TAB_FRAME_MS)
        waits.add(miss)
        const v = video as HTMLVideoElement & { requestVideoFrameCallback?: (cb: () => void) => void }
        if (v.requestVideoFrameCallback) v.requestVideoFrameCallback(() => end(true))
        else requestAnimationFrame(() => end(true))
      })
      if (!presented || track.readyState !== 'live') {
        src.why = TAB_ENDED_WORDS
        return null
      }
      src.why = ''
      const c = canvas(video.videoWidth, video.videoHeight)
      c.ctx.drawImage(video, 0, 0)
      return { width: video.videoWidth, height: video.videoHeight, data: c.ctx.getImageData(0, 0, video.videoWidth, video.videoHeight).data }
    },
    stop() {
      ended()
      track.removeEventListener?.('ended', ended)
      stop()
      video.srcObject = null
    },
  }
  return src
}

// ---- Choosing a source ----

/** The desktop app's source as the pane reads one: the frame's box asked in the window's CSS pixels, its bitmap read as pixels. */
export function fromDesktop(d: DesktopSource): ScreenshotSource {
  const src: ScreenshotSource = {
    how: 'desktop',
    async frame(r) {
      const bmp = await d.frame({ left: r.x, top: r.y, right: r.x + r.width, bottom: r.y + r.height })
      src.why = d.why()
      return bmp ? pixelsOf(bmp) : null
    },
    stop: () => d.stop(),
  }
  return src
}

/** The words of a desktop app older than its screenshots: its window offers no tab capture either. */
export const OLD_DESKTOP_WORDS = 'this desktop app is too old to take a screenshot (update it), so the capture has no screenshot'

/**
 * The source for the frame el, chosen in this order: the test seam's (a spec's own screenshots), the desktop shell's, the tab's, none.
 * A desktop app too old to take one gives none with words: its window answers no tab capture, and the tab's words would say the person
 * declined a prompt never shown.
 */
export async function screenshotSource(el: Element, o: { seam?: ScreenshotSource | null; desktop?: DesktopSource | null; app?: DesktopBridge | null } = {}): Promise<ScreenshotSource> {
  if (o.seam) return o.seam
  const app = o.app === undefined ? desktopBridge() : o.app
  const d = o.desktop === undefined ? desktopSource(app) : o.desktop
  if (d) return fromDesktop(d)
  if (app) return noneSource(OLD_DESKTOP_WORDS)
  return tabSource(el)
}
