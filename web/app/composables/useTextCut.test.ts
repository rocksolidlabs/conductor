import { afterEach, describe, expect, it, vi } from 'vitest'
import { effectScope, nextTick, ref, shallowRef } from 'vue'
import { textCut, useTextCut } from './useTextCut'

// A session's name in its header (#906): whether its line is cut, measured again when its box changes size, when its text changes
// and when the element is swapped (the plain name for the button that shows it whole).

/** A ResizeObserver the test calls itself: what it watches, and a call of its callback as the browser would make it. */
class FakeObserver {
  static last: FakeObserver | undefined
  watched = new Set<Element>()
  constructor(public fire: () => void) {
    FakeObserver.last = this
  }
  observe(e: Element) {
    this.watched.add(e)
  }
  unobserve(e: Element) {
    this.watched.delete(e)
  }
  disconnect() {
    this.watched.clear()
  }
}

const box = (scrollWidth: number, clientWidth: number) => ({ scrollWidth, clientWidth }) as unknown as HTMLElement

afterEach(() => vi.unstubAllGlobals())

describe('textCut', () => {
  it('is cut only when the text is wider than its box', () => {
    expect(textCut({ scrollWidth: 881, clientWidth: 240 })).toBe(true)
    expect(textCut({ scrollWidth: 241, clientWidth: 240 })).toBe(true)
    expect(textCut({ scrollWidth: 240, clientWidth: 240 })).toBe(false)
    expect(textCut({ scrollWidth: 120, clientWidth: 240 })).toBe(false)
  })
})

describe('useTextCut', () => {
  it('measures the element when it is set, when its box changes, when its text changes and when it is swapped', async () => {
    vi.stubGlobal('ResizeObserver', FakeObserver)
    const el = shallowRef<HTMLElement | null>(null)
    const text = ref('a name')
    const scope = effectScope()
    const cut = scope.run(() => useTextCut(el, () => text.value))!
    const observer = FakeObserver.last!
    expect(cut.value, 'no element, nothing cut').toBe(false)

    const span = box(880, 240)
    el.value = span
    await nextTick()
    expect(cut.value, 'set: measured').toBe(true)
    expect([...observer.watched]).toEqual([span])

    // The window widened: the box holds the text, and the observer says so.
    Object.assign(span, { clientWidth: 900 })
    observer.fire()
    expect(cut.value, 'its box grew: measured again').toBe(false)

    // The name renamed longer, its box the same.
    Object.assign(span, { scrollWidth: 1200 })
    text.value = 'a much longer name'
    await nextTick()
    await nextTick()
    expect(cut.value, 'its text changed: measured again').toBe(true)

    // Swapped for another element: the new one is watched in the old one's place, and measured.
    const button = box(100, 240)
    el.value = button
    await nextTick()
    expect([...observer.watched]).toEqual([button])
    expect(cut.value).toBe(false)

    scope.stop()
    expect(observer.watched.size, 'stopped: nothing watched').toBe(0)
  })

  it('without a ResizeObserver still measures when the element is set', async () => {
    vi.stubGlobal('ResizeObserver', undefined)
    const el = shallowRef<HTMLElement | null>(box(500, 200))
    const scope = effectScope()
    const cut = scope.run(() => useTextCut(el, () => 'x'))!
    await nextTick()
    expect(cut.value).toBe(true)
    scope.stop()
  })
})
