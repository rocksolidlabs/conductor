import { onBeforeUnmount, onMounted, ref } from 'vue'
import { asDesktopZoom, zoomWords, type DesktopBridge } from '../utils/desktop'

/** How long the zoom indicator shows a level before it fades. */
export const ZOOM_SHOWN_MS = 1500

/**
 * ZoomIndicator is the one indicator of the desktop app's zoom (#431): each zoom told shows its words (zoomWords) in place of the
 * last, for ZOOM_SHOWN_MS from the latest, so repeated moves update one indicator and restart its time. `last` keeps the words last
 * shown after the indicator has gone, which the page puts on its live region for a test to read.
 */
export class ZoomIndicator {
  readonly shown = ref('')
  readonly last = ref('')
  private timer: ReturnType<typeof setTimeout> | undefined

  /** Shows what the app told, read as a zoom status; anything else shows nothing. */
  show(told: unknown): void {
    const z = asDesktopZoom(told)
    if (!z) return
    this.shown.value = this.last.value = zoomWords(z)
    clearTimeout(this.timer)
    this.timer = setTimeout(() => {
      this.timer = undefined
      this.shown.value = ''
    }, ZOOM_SHOWN_MS)
  }

  stop(): void {
    clearTimeout(this.timer)
    this.timer = undefined
    this.shown.value = ''
  }
}

/**
 * The desktop app's zoom, shown (#431): each move by key or menu that the app tells (the bridge's onZoom), and a level kept from
 * before once per start of the app (zoom()'s announce). The keys reach the app before the page, so the page learns of a move only
 * from it. Nothing in a browser, nor under an app whose bridge has no zoom event: `listening` is false and nothing renders.
 */
export function useDesktopZoom(bridge: () => DesktopBridge | null) {
  const indicator = new ZoomIndicator()
  const listening = ref(false)
  let off: (() => void) | undefined
  onMounted(() => {
    const b = bridge()
    if (typeof b?.onZoom !== 'function') return
    listening.value = true
    off = b.onZoom((z) => indicator.show(z))
    if (typeof b.zoom !== 'function') return
    b.zoom()
      .then((z) => {
        if (z?.announce) indicator.show(z)
      })
      .catch(() => {})
  })
  onBeforeUnmount(() => {
    off?.()
    indicator.stop()
  })
  return { listening, shown: indicator.shown, last: indicator.last }
}
