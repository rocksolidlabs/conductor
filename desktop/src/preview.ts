/**
 * The preview in the desktop app (docs/architecture.md, Previews and captures): the shell puts the server's preview bridge into the
 * workbench's preview frame, whatever page it shows, and takes the pixels of a part of the workbench's own window, with no prompt.
 * Both are asked for by the workbench's main frame alone, over the two channels ipc.ts guards; what they act on is the asking window,
 * never another.
 */

/** The longest side of a screenshot as taken, in pixels: the workbench's own scale (web/app/utils/screenshot.ts) is the same. */
export const MAX_PREVIEW_SIDE = 2048
/** The most bytes of a screenshot's PNG: the store's bound on a capture's image (internal/dispatch MaxCaptureImage). */
export const MAX_PREVIEW_PNG = 4 << 20
/** The most bytes of the bridge's script the shell runs: the built bridge is a small fraction of it. */
export const MAX_BRIDGE_SCRIPT = 1 << 20
/** The longest frame origin asked for: the bridge protocol's bound on a URL (internal/proto MaxPreviewURL). */
export const MAX_FRAME_ORIGIN = 2048

/** The part of Electron's WebFrameMain the shell reads and acts on. */
export interface PreviewFrame {
  readonly origin: string
  readonly url: string
  readonly parent: PreviewFrame | null
  readonly frameTreeNodeId: number
  readonly detached?: boolean
  executeJavaScript(code: string): Promise<unknown>
}

/** The part of Electron's WebContents the shell reads and acts on. */
export interface PreviewContents {
  readonly id: number
  readonly mainFrame: PreviewFrame & { readonly framesInSubtree: PreviewFrame[] }
  getZoomFactor(): number
  capturePage(rect: Rect): Promise<PreviewImage>
}

/** The part of Electron's NativeImage the shell reads. */
export interface PreviewImage {
  isEmpty(): boolean
  getSize(): { width: number; height: number }
  resize(o: { width: number; height: number; quality?: 'good' | 'better' | 'best' }): PreviewImage
  toPNG(): Uint8Array
}

export interface Rect {
  x: number
  y: number
  width: number
  height: number
}

export type BridgeAnswer = { injected: number } | { refused: string }
export type CaptureAnswer = { png: Uint8Array; width: number; height: number; scale: number } | { refused: string }

/** sameFrame says whether a and b are one frame: Electron may hand two objects for it, whose node id is the same. */
export function sameFrame(a: PreviewFrame | null | undefined, b: PreviewFrame | null | undefined): boolean {
  return !!a && !!b && (a === b || a.frameTreeNodeId === b.frameTreeNodeId)
}

/** loopbackHost says whether a URL's host names this machine's loopback: localhost, an address in 127.0.0.0/8, or ::1. */
function loopbackHost(hostname: string): boolean {
  return hostname === 'localhost' || hostname === '[::1]' || /^127\.\d{1,3}\.\d{1,3}\.\d{1,3}$/.test(hostname)
}

/** The port of an http(s) URL, the scheme's default when none is written. */
function portOf(u: URL): string {
  return u.port || (u.protocol === 'https:' ? '443' : '80')
}

/**
 * frameOriginRefusal is why the shell will not put the bridge into frames at origin, '' when it will: an origin of a page on this
 * machine's loopback over http(s), written as a browser writes it (no path, user, query or fragment), and not the workbench's own
 * under any of the loopback's names (the same port on localhost, 127.0.0.1 or ::1 is the workbench).
 */
export function frameOriginRefusal(origin: unknown, workbench: string): string {
  if (typeof origin !== 'string' || origin.length === 0 || origin.length > MAX_FRAME_ORIGIN) return 'refused: not an origin'
  let u: URL
  let w: URL
  try {
    u = new URL(origin)
    w = new URL(workbench)
  } catch {
    return 'refused: not an origin'
  }
  if ((u.protocol !== 'http:' && u.protocol !== 'https:') || u.origin !== origin) return 'refused: not an origin'
  if (!loopbackHost(u.hostname)) return 'refused: only a page on this machine is previewed'
  if (u.origin === w.origin || (loopbackHost(w.hostname) && portOf(u) === portOf(w))) return 'refused: this is Conductor itself'
  return ''
}

/** urlOrigin is a URL's origin, '' for one that has none (about:blank, a data: URL). */
function urlOrigin(url: string): string {
  try {
    const o = new URL(url).origin
    return o === 'null' ? '' : o
  } catch {
    return ''
  }
}

/** previewFrames are the frames of wc's main frame, its children alone, whose origin, and whose page's, is origin. */
export function previewFrames(wc: PreviewContents, origin: string): PreviewFrame[] {
  const main = wc.mainFrame
  return main.framesInSubtree.filter((f) => !f.detached && !sameFrame(f, main) && sameFrame(f.parent, main) && f.origin === origin && urlOrigin(f.url) === origin)
}

export interface BridgeDeps {
  /** The server's origin, the workbench's: the bridge is fetched from there alone. */
  origin: () => string
  /** Fetches url, following no redirect. */
  fetch: (url: string) => Promise<{ ok: boolean; status: number; url: string; headers: { get(name: string): string | null }; text(): Promise<string> }>
}

/**
 * injectBridge puts the server's preview bridge (GET /preview-bridge.js, the workbench's origins written in by the server) into each
 * frame of wc's main frame at frameOrigin, and answers how many took it. A frame that already has a bridge, its page's own or one put
 * in before, keeps that one: the second stays idle (web/app/utils/previewBridge/bridge.ts). Refused for an origin frameOriginRefusal
 * refuses, and for a bridge the server did not give whole; a frame that navigated away meanwhile is passed over.
 */
export async function injectBridge(wc: PreviewContents, frameOrigin: unknown, d: BridgeDeps): Promise<BridgeAnswer> {
  const workbench = d.origin()
  const why = frameOriginRefusal(frameOrigin, workbench)
  if (why) return { refused: why }
  const origin = frameOrigin as string
  if (previewFrames(wc, origin).length === 0) return { injected: 0 }
  const script = await bridgeScript(workbench, d)
  if (typeof script !== 'string') return script
  let injected = 0
  // Found again after the fetch, and each checked again just before its run: a frame that left its page meanwhile is not run in.
  for (const f of previewFrames(wc, origin)) {
    if (f.detached || f.origin !== origin || urlOrigin(f.url) !== origin) continue
    try {
      await f.executeJavaScript(script)
      injected++
    } catch {
      // The frame went (navigated, removed) as it ran: nothing to do there.
    }
  }
  return { injected }
}

/** bridgeScript is the server's bridge as it serves it, or why not. */
async function bridgeScript(workbench: string, d: BridgeDeps): Promise<string | { refused: string }> {
  const url = new URL('/preview-bridge.js', workbench).toString()
  let res: Awaited<ReturnType<BridgeDeps['fetch']>>
  try {
    res = await d.fetch(url)
  } catch {
    return { refused: 'refused: the server did not answer for its bridge' }
  }
  if (!res.ok || res.status !== 200 || urlOrigin(res.url) !== new URL(workbench).origin) return { refused: 'refused: the server has no bridge to give' }
  if (!/^text\/javascript\b/i.test(res.headers.get('content-type') ?? '')) return { refused: 'refused: the server has no bridge to give' }
  const declared = Number(res.headers.get('content-length') ?? '0')
  if (declared > MAX_BRIDGE_SCRIPT) return { refused: 'refused: the bridge is larger than the shell runs' }
  const text = await res.text()
  if (text.length > MAX_BRIDGE_SCRIPT) return { refused: 'refused: the bridge is larger than the shell runs' }
  return text
}

/**
 * rectRefusal is why rect is not a part of a window whose content is size (in the window's own pixels, DIPs) at zoom, '' when it is:
 * the page's CSS pixels as integers, at least one pixel each way, inside the content.
 */
export function rectRefusal(rect: unknown, size: { width: number; height: number }, zoom: number): string {
  if (!rect || typeof rect !== 'object' || Array.isArray(rect)) return 'refused: not a rectangle'
  const { x, y, width, height } = rect as Record<string, unknown>
  const whole = (v: unknown): v is number => typeof v === 'number' && Number.isSafeInteger(v)
  if (!whole(x) || !whole(y) || !whole(width) || !whole(height)) return 'refused: the rectangle is not in whole pixels'
  if (width < 1 || height < 1) return 'refused: the rectangle is smaller than a pixel'
  if (x < 0 || y < 0 || x + width > Math.ceil(size.width / zoom) || y + height > Math.ceil(size.height / zoom)) return "refused: the rectangle is outside the window's content"
  return ''
}

export interface CaptureDeps {
  /** The size of wc's window's content, in its own pixels (DIPs). */
  contentSize: (wc: PreviewContents) => { width: number; height: number } | null
}

/** The windows taking a screenshot now, by their contents' id: one at a time each. */
const capturing = new Set<number>()

/**
 * capturePreview takes the pixels of rect (the page's CSS pixels) of wc's own window: at most MAX_PREVIEW_SIDE on the long side,
 * as PNG, with its scale (the image's pixels to one of the rect's). Refused for a rect rectRefusal refuses, while the same window is
 * taking one, for an empty image, and for a PNG past MAX_PREVIEW_PNG.
 */
export async function capturePreview(wc: PreviewContents, rect: unknown, d: CaptureDeps): Promise<CaptureAnswer> {
  if (capturing.has(wc.id)) return { refused: 'refused: a screenshot is being taken' }
  const size = d.contentSize(wc)
  const zoom = wc.getZoomFactor()
  if (!size || !(zoom > 0)) return { refused: 'refused: the window has no content to take' }
  const why = rectRefusal(rect, size, zoom)
  if (why) return { refused: why }
  const r = rect as Rect
  capturing.add(wc.id)
  try {
    // The window's pixels, whose rectangle is in its own units: the page's CSS pixels at its zoom, kept inside the content.
    const x = Math.floor(r.x * zoom)
    const y = Math.floor(r.y * zoom)
    const dip = { x, y, width: Math.max(1, Math.min(size.width, Math.ceil((r.x + r.width) * zoom)) - x), height: Math.max(1, Math.min(size.height, Math.ceil((r.y + r.height) * zoom)) - y) }
    let image = await wc.capturePage(dip)
    if (image.isEmpty()) return { refused: 'refused: the window gave no pixels' }
    let { width, height } = image.getSize()
    const long = Math.max(width, height)
    if (long > MAX_PREVIEW_SIDE) {
      const f = MAX_PREVIEW_SIDE / long
      image = image.resize({ width: Math.max(1, Math.round(width * f)), height: Math.max(1, Math.round(height * f)), quality: 'best' })
      ;({ width, height } = image.getSize())
    }
    const png = image.toPNG()
    if (png.byteLength > MAX_PREVIEW_PNG) return { refused: 'refused: the screenshot is larger than 4 MiB' }
    return { png: new Uint8Array(png.buffer, png.byteOffset, png.byteLength), width, height, scale: width / r.width }
  } catch {
    return { refused: 'refused: the window gave no pixels' }
  } finally {
    capturing.delete(wc.id)
  }
}
