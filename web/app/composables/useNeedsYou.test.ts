import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import type { Decision } from '~/utils/decisions'
import { emptyInbox, type InboxState } from './useInbox'
import type { SessionInfo } from './useSessions'
import { useNeedsYou } from './useNeedsYou'

// The one list over the app's three stores (utils/needsYou.ts holds its rules; this holds the wiring): each store's reading reaches
// the list, and the list follows each store as it changes. useNeedsYou reads Nuxt's auto-imports as globals: each is stubbed.

const sessions = ref<SessionInfo[]>([])
const attention = { loaded: ref(false), error: ref('') }
const records = ref<Decision[]>([])
const decided = { loaded: ref(false), cut: ref(false), unavailable: ref(''), stale: ref({ since: null as number | null, error: '' }) }
const inbox = ref<InboxState>(emptyInbox())
const runs: Record<string, { stoppedAt?: string }> = { live: {}, stopped: { stoppedAt: '2026-11-16T00:00:00Z' } }

beforeEach(() => {
  vi.stubGlobal('useAttention', () => ({ sessions, loaded: attention.loaded, error: attention.error, runOf: (id: string) => runs[id] }))
  vi.stubGlobal('useDecisions', () => ({ decisions: records, ...decided }))
  vi.stubGlobal('useInbox', () => ({ state: inbox }))
})
afterEach(() => {
  sessions.value = []
  attention.loaded.value = false
  attention.error.value = ''
  records.value = []
  decided.loaded.value = false
  decided.cut.value = false
  decided.unavailable.value = ''
  decided.stale.value = { since: null, error: '' }
  inbox.value = emptyInbox()
  vi.unstubAllGlobals()
})

const asks = (id: string): SessionInfo => ({ id, name: id, status: 'running', attention: { state: 'needs_input', id: `q-${id}`, since: '2026-11-16T09:00:00Z' } }) as SessionInfo
const gate = (id: string, run: string): Decision => ({ id, run, state: 'open', createdAt: '2026-11-16T08:00:00Z' }) as Decision

describe('useNeedsYou', () => {
  it('is unknown until every store has read, then follows each as it changes', () => {
    const n = useNeedsYou()
    sessions.value = [asks('a')]
    attention.loaded.value = true
    expect([n.number.value, n.known.value]).toEqual([1, false])
    decided.loaded.value = true
    expect(n.known.value).toBe(false)
    inbox.value = { ...emptyInbox(), loaded: true }
    expect([n.number.value, n.known.value, n.more.value]).toEqual([1, true, false])
    // A stopped run's gate is counted, said stopped; the run that waits is listed.
    records.value = [gate('dr-1', 'stopped')]
    expect([n.number.value, n.items.value.map((it) => [it.ref, it.stopped ?? null])]).toEqual([2, [['decision:dr-1', true], ['question:a/q-a', null]]])
    expect(n.runs.value).toEqual(['stopped'])
    expect(n.answerable.value).toHaveLength(2)
    // The decisions' last read failed: unknown again; cut: a floor.
    decided.stale.value = { since: 1, error: 'decisions 500' }
    expect(n.known.value).toBe(false)
    decided.stale.value = { since: null, error: '' }
    decided.cut.value = true
    expect([n.known.value, n.more.value]).toEqual([false, true])
    decided.cut.value = false
    // The inbox's plan, and a page after its first.
    inbox.value = { ...inbox.value, items: [{ ref: 'plan:CND-1/1', kind: 'plan', since: '2026-11-16T10:00:00Z', title: 'p', brief: '', question: '', answerer: '', actions: [] }], next: 'c' }
    expect([n.number.value, n.known.value, n.more.value]).toEqual([3, false, true])
    // The sessions' stream failed.
    inbox.value = { ...inbox.value, next: '' }
    attention.error.value = 'events 502'
    expect([n.known.value, n.more.value]).toEqual([false, false])
  })

  it("reads a switchyard's refusals as stores with nothing in them", () => {
    const n = useNeedsYou()
    sessions.value = [asks('a'), asks('b')]
    attention.loaded.value = true
    decided.unavailable.value = 'switchyard'
    inbox.value = { ...emptyInbox(), loaded: true, unavailable: 'switchyard' }
    expect([n.number.value, n.known.value]).toEqual([2, true])
  })
})
