import { asDesktopCapture, desktopBridge, frameOrigin, previewRect, type DesktopBridge } from '~/utils/desktop'

/**
 * The screenshot's desktop path (docs/architecture.md, Previews and captures): in the desktop app the shell takes the pixels of the
 * preview frame's box in the workbench's own window, with no prompt, and puts the server's bridge into the frame whatever page it
 * shows. utils/screenshot.ts chooses this source when the app offers it; the pane asks for the bridge once the frame has loaded.
 */

/** A box of the workbench's page, as getBoundingClientRect gives it. */
export interface PageBox {
  left: number
  top: number
  right: number
  bottom: number
}

/** The desktop app's screenshot source: utils/screenshot.ts's ScreenshotSource, with why the last frame was not taken. */
export interface DesktopSource {
  how: 'desktop'
  /** The pixels of box now, as a bitmap; null when the app refused (why() says why). One at a time: a second waits for the first. */
  frame(box: PageBox): Promise<ImageBitmap | null>
  /** Nothing to stop: the app holds no track. */
  stop(): void
  /** Why the last frame was not taken, '' when it was. */
  why(): string
}

/** Decodes a PNG into a bitmap (the browser's own, by default). */
export type DecodePng = (png: Uint8Array) => Promise<ImageBitmap>

const decodePng: DecodePng = (png) => createImageBitmap(new Blob([png as Uint8Array<ArrayBuffer>], { type: 'image/png' }))

/** desktopSource is the desktop app's screenshot source, or null outside the app and in an app too old to take one. */
export function desktopSource(bridge: DesktopBridge | null = desktopBridge(), decode: DecodePng = decodePng): DesktopSource | null {
  const capture = bridge?.capturePreview?.bind(bridge)
  if (!capture) return null
  let why = ''
  // The app takes one screenshot per window at a time and refuses a second asked meanwhile: frames asked together are taken in turn.
  let turn: Promise<unknown> = Promise.resolve()
  const take = async (box: PageBox): Promise<ImageBitmap | null> => {
    const rect = previewRect(box)
    if (!rect) {
      why = 'the frame has no pixels to take'
      return null
    }
    let answer
    try {
      answer = asDesktopCapture(await capture(rect))
    } catch {
      answer = { refused: 'the desktop app gave no screenshot' }
    }
    if ('refused' in answer) {
      why = answer.refused
      return null
    }
    try {
      const bitmap = await decode(answer.png)
      why = ''
      return bitmap
    } catch {
      why = 'the desktop app gave a screenshot that is not a PNG'
      return null
    }
  }
  return {
    how: 'desktop',
    frame(box) {
      const next = turn.then(() => take(box))
      turn = next.catch(() => {})
      return next
    },
    stop() {},
    why: () => why,
  }
}

/**
 * desktopPreviewBridge asks the desktop app to put the server's bridge into the preview frame showing src, once it has loaded: how
 * many frames took it, or why not ('' when it did). Outside the app, or in one too old, nothing is asked.
 */
export async function desktopPreviewBridge(src: string, bridge: DesktopBridge | null = desktopBridge(), base = typeof location === 'undefined' ? '' : location.href): Promise<{ injected: number } | { refused: string } | null> {
  const ask = bridge?.previewBridge?.bind(bridge)
  if (!ask) return null
  const origin = frameOrigin(src, base)
  if (!origin) return { refused: 'the preview has no origin to put the bridge in' }
  try {
    const answer = await ask(origin)
    if (answer && typeof answer === 'object' && 'injected' in answer && typeof answer.injected === 'number') return { injected: answer.injected }
    if (answer && typeof answer === 'object' && 'refused' in answer && typeof answer.refused === 'string') return { refused: answer.refused }
  } catch {
    // Refused by the app's guard (not the workbench's main frame) or the app gone: no bridge from it.
  }
  return { refused: 'the desktop app put no bridge in' }
}
