import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createRenderer, defineComponent, h } from 'vue'
import type { DesktopBridge, DesktopZoom } from '../utils/desktop'
import { useDesktopZoom, ZOOM_SHOWN_MS, ZoomIndicator } from './useDesktopZoom'

// The desktop app's zoom indicator (#431) on fake timers: one indicator, its words, its time, and nothing without the app's event.

const at = (level: number, percent: number, bound: DesktopZoom['bound'] = ''): DesktopZoom => ({ level, percent, bound })

beforeEach(() => vi.useFakeTimers())
afterEach(() => vi.useRealTimers())

describe('the zoom indicator', () => {
  it('shows the percent the app sent, and goes after its time', () => {
    const z = new ZoomIndicator()
    z.show(at(0.5, 110))
    expect(z.shown.value).toBe('110%')
    vi.advanceTimersByTime(ZOOM_SHOWN_MS - 1)
    expect(z.shown.value).toBe('110%')
    vi.advanceTimersByTime(1)
    expect(z.shown.value).toBe('')
    // The words last shown stay for the live region's mark.
    expect(z.last.value).toBe('110%')
  })

  it('says the largest and the smallest at a bound, a move that changed nothing included', () => {
    const z = new ZoomIndicator()
    z.show(at(5, 249, 'largest'))
    expect(z.shown.value).toBe('249%, the largest')
    z.show(at(5, 249, 'largest'))
    expect(z.shown.value).toBe('249%, the largest')
    z.show(at(-3, 58, 'smallest'))
    expect(z.shown.value).toBe('58%, the smallest')
    z.show(at(0, 100))
    expect(z.shown.value).toBe('100%')
  })

  it('is one indicator, updated in place, its time restarted by each move', () => {
    const z = new ZoomIndicator()
    const seen: string[] = []
    // Three presses of Ctrl+= from 100%, each a moment after the last.
    for (const [level, percent] of [[0.5, 110], [1, 120], [1.5, 131]] as const) {
      z.show(at(level, percent))
      seen.push(z.shown.value)
      vi.advanceTimersByTime(ZOOM_SHOWN_MS - 100)
    }
    expect(seen).toEqual(['110%', '120%', '131%'])
    // Shown past the first move's time: the last move's runs from its own.
    expect(z.shown.value).toBe('131%')
    expect(vi.getTimerCount()).toBe(1)
    vi.advanceTimersByTime(100)
    expect(z.shown.value).toBe('')
    expect(vi.getTimerCount()).toBe(0)
  })

  it('shows nothing for what is not a zoom status', () => {
    const z = new ZoomIndicator()
    for (const told of [null, undefined, 'x', {}, { level: 1, percent: 'a' }, { level: Number.NaN, percent: 120 }, { level: 1, percent: 0 }]) z.show(told)
    expect(z.shown.value).toBe('')
    expect(vi.getTimerCount()).toBe(0)
  })
})

// The indicator's composable, mounted and unmounted by Vue itself through a renderer with no DOM, with a stub bridge.
describe('the zoom indicator in a page', () => {
  const nodeOps = {
    insert: () => {},
    remove: () => {},
    createElement: () => ({}),
    createText: () => ({}),
    createComment: () => ({}),
    setText: () => {},
    setElementText: () => {},
    parentNode: () => null,
    nextSibling: () => null,
    patchProp: () => {},
  }
  const { createApp } = createRenderer<object, object>(nodeOps)

  function mount(bridge: Partial<DesktopBridge> | null) {
    let zoom!: ReturnType<typeof useDesktopZoom>
    const app = createApp(defineComponent({ setup: () => ((zoom = useDesktopZoom(() => bridge as DesktopBridge | null)), () => h('div')) }))
    app.mount({})
    return { app, zoom }
  }

  /** A bridge with the zoom event, which `move` fires, and the getter answering `now`. */
  function app(now: DesktopZoom & { announce: boolean }) {
    let cb: ((z: DesktopZoom) => void) | undefined
    const off = vi.fn()
    const bridge: Partial<DesktopBridge> = {
      zoom: vi.fn(async () => now),
      onZoom: (f) => {
        cb = f
        return off
      },
    }
    return { bridge, off, move: (z: DesktopZoom) => cb?.(z) }
  }

  it('shows each move the app tells, and lets go of the event when unmounted', async () => {
    const a = app({ ...at(0, 100), announce: false })
    const { app: vue, zoom } = mount(a.bridge)
    await vi.advanceTimersByTimeAsync(0)
    expect(zoom.listening.value).toBe(true)
    expect(zoom.shown.value).toBe('')
    a.move(at(0.5, 110))
    expect(zoom.shown.value).toBe('110%')
    vue.unmount()
    expect(a.off).toHaveBeenCalledTimes(1)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('says a level kept from before when the app says to, and nothing when it does not', async () => {
    const kept = app({ ...at(1, 120), announce: true })
    const first = mount(kept.bridge)
    await vi.advanceTimersByTimeAsync(0)
    expect(first.zoom.shown.value).toBe('120%')
    first.app.unmount()
    const later = app({ ...at(1, 120), announce: false })
    const second = mount(later.bridge)
    await vi.advanceTimersByTimeAsync(0)
    expect(second.zoom.shown.value).toBe('')
    second.app.unmount()
  })

  it('shows nothing and throws nothing without the bridge, or with a bridge that lacks the event', async () => {
    for (const bridge of [null, {}, { zoom: vi.fn(async () => ({ ...at(1, 120), announce: true })) }]) {
      const { app: vue, zoom } = mount(bridge)
      await vi.advanceTimersByTimeAsync(0)
      expect(zoom.listening.value).toBe(false)
      expect(zoom.shown.value).toBe('')
      vue.unmount()
    }
  })

  it('shows the moves under an app whose getter fails or is missing', async () => {
    let cb: ((z: DesktopZoom) => void) | undefined
    const failing: Partial<DesktopBridge> = { zoom: () => Promise.reject(new Error('refused')), onZoom: (f) => ((cb = f), () => {}) }
    const { app: vue, zoom } = mount(failing)
    await vi.advanceTimersByTimeAsync(0)
    expect(zoom.listening.value).toBe(true)
    cb?.(at(-0.5, 91))
    expect(zoom.shown.value).toBe('91%')
    vue.unmount()
  })
})
