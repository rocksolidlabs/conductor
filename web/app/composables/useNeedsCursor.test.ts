import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { computed, ref, type Ref } from 'vue'
import type { Decision } from '../utils/decisions'
import type { InboxSummary } from '../utils/inbox'
import { PILL_MS } from '../utils/needsCursor'
import { needsYou } from '../utils/needsYou'
import type { SessionInfo } from './useSessions'
import { useNeedsCursor } from './useNeedsCursor'

// The pill's own clock (#303), on fake timers: a press shows it for PILL_MS and the next press starts that again. The e2e spec
// that reads the pill holds it for the test's time instead (#281), so its four seconds are proved here. useNeedsCursor reads
// Nuxt's auto-imports (useState, useAttention, usePalette, useNeedsYou) as globals: each is stubbed, the one list over the stubbed
// stores.

function asking(id: string, since: string): SessionInfo {
  return { id, name: id, status: 'running', attention: { state: 'needs_input', id: `q-${id}`, since, message: 'Which database?' } } as SessionInfo
}

let states: Map<string, Ref<unknown>>
/** The open durable decisions the store holds (Program item DR1): none unless a test puts some there. */
const decisions = ref<Array<{ id: string; state: string; run: string }>>([])
/** The runs the live store holds, by id. */
const runs: Record<string, { stoppedAt?: string }> = { live: {}, stopped: { stoppedAt: '2026-10-31T00:00:00Z' } }
/** The inbox's plans and tasks: none unless a test puts some there. */
const inboxItems = ref<InboxSummary[]>([])
const sessions = ref<SessionInfo[]>([])
beforeEach(() => {
  vi.useFakeTimers()
  states = new Map()
  vi.stubGlobal('window', globalThis)
  vi.stubGlobal('useState', <T>(key: string, init: () => T) => {
    if (!states.has(key)) states.set(key, ref(init()))
    return states.get(key) as Ref<T>
  })
  sessions.value = [asking('a', '2026-10-31T00:00:02Z'), asking('b', '2026-10-31T00:00:01Z')]
  vi.stubGlobal('useAttention', () => ({ sessions, runOf: (id: string) => runs[id] }))
  vi.stubGlobal('usePalette', () => ({ page: ref(null) }))
  vi.stubGlobal('useNeedsYou', () => {
    const list = computed(() =>
      needsYou({
        sessions: { list: sessions.value, loaded: true, error: '' },
        decisions: { records: decisions.value as unknown as Decision[], loaded: true, error: '', cut: false },
        inbox: { items: inboxItems.value, loaded: true, error: '', next: '', unavailable: '' },
        runOf: (id) => runs[id],
      }),
    )
    return { list, items: computed(() => list.value.items), answerable: computed(() => list.value.answerable) }
  })
})
afterEach(() => {
  decisions.value = []
  inboxItems.value = []
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

  it('steps after the sessions to each open decision no shown question mirrors, once, a stopped run\'s too, and to none not open', () => {
    decisions.value = [
      { id: 'dr-2', state: 'open', run: 'live' },
      { id: 'dr-3', state: 'answered', run: 'live' },
      { id: 'dr-4', state: 'open', run: 'stopped' },
      { id: 'dr-5', state: 'open', run: 'gone' },
    ]
    const cursor = useNeedsCursor()
    cursor.step(1)
    cursor.step(1)
    cursor.step(1)
    expect(cursor.shown.value).toMatchObject({ id: 'dr-2', decision: 'dr-2', n: 3, of: 5 })
    cursor.step(1)
    expect(cursor.shown.value).toMatchObject({ id: 'dr-4', decision: 'dr-4', n: 4, of: 5 })
    cursor.step(1)
    expect(cursor.shown.value).toMatchObject({ id: 'dr-5', decision: 'dr-5', n: 5, of: 5 })
    cursor.step(1)
    expect(cursor.shown.value).toMatchObject({ id: 'a', n: 1, of: 5 })
    expect(cursor.shown.value?.decision).toBeUndefined()
  })

  it('says how many items of the one list it cannot answer: the plans and the tasks', () => {
    const at = '2026-10-31T00:00:00Z'
    inboxItems.value = [
      { ref: 'plan:CND-1/1', kind: 'plan', since: at, title: 'p', brief: '', question: '', answerer: '', actions: [] },
      { ref: 'task:live/t-1', kind: 'task', since: at, title: 't', brief: '', question: '', answerer: '', actions: [] },
      // The inbox's own question is the session's, counted once, and answerable.
      { ref: 'question:a/q-a', kind: 'question', since: at, title: 'q', brief: '', question: '', answerer: '', actions: [] },
    ]
    const cursor = useNeedsCursor()
    cursor.step(1)
    expect(cursor.shown.value).toMatchObject({ id: 'a', n: 1, of: 2, more: 2 })
  })
})
