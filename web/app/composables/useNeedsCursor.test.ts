import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ref, type Ref } from 'vue'
import { PILL_MS } from '../utils/needsCursor'
import type { SessionInfo } from './useSessions'
import { useNeedsCursor } from './useNeedsCursor'

// The pill's own clock (#303), on fake timers: a press shows it for PILL_MS and the next press starts that again. The e2e spec
// that reads the pill holds it for the test's time instead (#281), so its four seconds are proved here. useNeedsCursor reads
// Nuxt's auto-imports (useState, useAttention, usePalette) as globals: each is stubbed.

function asking(id: string, since: string): SessionInfo {
  return { id, name: id, status: 'running', attention: { state: 'needs_input', id: `q-${id}`, since, message: 'Which database?' } } as SessionInfo
}

let states: Map<string, Ref<unknown>>
beforeEach(() => {
  vi.useFakeTimers()
  states = new Map()
  vi.stubGlobal('window', globalThis)
  vi.stubGlobal('useState', <T>(key: string, init: () => T) => {
    if (!states.has(key)) states.set(key, ref(init()))
    return states.get(key) as Ref<T>
  })
  vi.stubGlobal('useAttention', () => ({ sessions: ref([asking('a', '2026-10-31T00:00:02Z'), asking('b', '2026-10-31T00:00:01Z')]) }))
  vi.stubGlobal('usePalette', () => ({ page: ref(null) }))
})
afterEach(() => {
  delete (globalThis as { conductorPillMs?: unknown }).conductorPillMs
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

describe('the needs-you pill', () => {
  it('shows for PILL_MS after a press and goes on its own', () => {
    const cursor = useNeedsCursor()
    cursor.step(1)
    expect(cursor.shown.value).toMatchObject({ id: 'a', n: 1, of: 2 })
    vi.advanceTimersByTime(PILL_MS - 1)
    expect(cursor.shown.value?.id).toBe('a')
    vi.advanceTimersByTime(1)
    expect(cursor.shown.value).toBeNull()
  })

  it('starts its clock again at the next press, which steps from the one shown; once gone, a press starts over', () => {
    const cursor = useNeedsCursor()
    cursor.step(1)
    vi.advanceTimersByTime(PILL_MS - 1)
    cursor.step(1)
    expect(cursor.shown.value).toMatchObject({ id: 'b', n: 2, of: 2 })
    vi.advanceTimersByTime(PILL_MS - 1)
    expect(cursor.shown.value?.id).toBe('b')
    vi.advanceTimersByTime(1)
    expect(cursor.shown.value).toBeNull()
    cursor.step(1)
    expect(cursor.shown.value).toMatchObject({ id: 'a', n: 1 })
  })

  it('lives as long as a page was given in conductorPillMs (an e2e spec’s), and PILL_MS for anything but a positive number', () => {
    const scope = globalThis as { conductorPillMs?: unknown }
    scope.conductorPillMs = 120_000
    const cursor = useNeedsCursor()
    cursor.step(1)
    vi.advanceTimersByTime(119_999)
    expect(cursor.shown.value?.id).toBe('a')
    vi.advanceTimersByTime(1)
    expect(cursor.shown.value).toBeNull()
    // Each still shows a millisecond before PILL_MS (a zero delay would have hidden it) and is gone at PILL_MS.
    for (const bad of [0, -5, Number.NaN, Number.POSITIVE_INFINITY, '600000', null]) {
      scope.conductorPillMs = bad
      cursor.step(1)
      vi.advanceTimersByTime(PILL_MS - 1)
      expect(cursor.shown.value, String(bad)).not.toBeNull()
      vi.advanceTimersByTime(1)
      expect(cursor.shown.value, String(bad)).toBeNull()
    }
  })
})
