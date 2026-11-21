import { readFileSync } from 'node:fs'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as vue from 'vue'
import { canShareProject, type ProjectShare } from './useDispatch'
import { REMOTE_POLL_MS, useRemote, useSyncStatus } from './useRemote'

// Other machines' records and the sync store's state (docs/plans/sync-seams.md, OS-4), read only while a sync store is registered:
// `sync` in a stubbed capabilities reply. Nuxt's auto-imports (Vue's, useCapabilities, useApi, useState) are stubbed as globals,
// read when the composables are called; the module is imported once with the file. The Share, rekey and Sync controls need a
// registered provider, which the e2e server never has: their drawing is held here, the components' sources checked for the
// bindings that draw them, as the app's vitest mounts no component.

let caps: vue.Ref<string[]>
let attention: vue.Ref<{ connected: boolean }>
let requests: string[]
let replies: Record<string, unknown>
const states = new Map<string, vue.Ref<unknown>>()

beforeEach(() => {
  caps = vue.ref([])
  requests = []
  replies = {}
  states.clear()
  for (const k of ['ref', 'computed', 'watch'] as const) vi.stubGlobal(k, vue[k])
  vi.stubGlobal('useCapabilities', () => ({ has: (kind: string) => caps.value.includes(kind) }))
  vi.stubGlobal('useApi', () => ({
    request: async (path: string) => {
      requests.push(path)
      return replies[path] ?? replies[path.split('?')[0]!] ?? {}
    },
  }))
  vi.stubGlobal('useState', (key: string, init: () => unknown) => {
    if (!states.has(key)) states.set(key, vue.ref(init()))
    return states.get(key)
  })
  vi.stubGlobal('useAttentionStore', () => attention)
  attention = vue.ref({ connected: true })
})
afterEach(() => vi.unstubAllGlobals())

const settle = () => new Promise((r) => setTimeout(r, 0))

describe("other machines' records", () => {
  it('asks nothing with no sync store, and draws none, whatever the server would answer', async () => {
    replies['/api/remote'] = { records: [{ kind: 'pins', device: 'dev-1', deviceName: 'studio', id: 'pins', rev: 1, at: '', data: { pins: [] } }] }
    const r = useRemote('pins')
    const s = useSyncStatus()
    await settle()
    expect(requests).toEqual([])
    expect(r.records.value).toEqual([])
    expect(s.report.value).toBeNull()
  })

  it('drops what it drew once the sync store is gone, and asks again when one is registered', async () => {
    caps.value = ['sync']
    replies['/api/remote'] = { records: [{ kind: 'pins', device: 'dev-1', deviceName: 'studio', id: 'pins', rev: 1, at: '', data: { pins: [] } }] }
    const r = useRemote('pins')
    await settle()
    expect(r.records.value.map((x) => x.device)).toEqual(['dev-1'])
    caps.value = []
    await settle()
    expect(r.records.value).toEqual([])
    const asked = requests.filter((p) => p.startsWith('/api/remote')).length
    caps.value = ['sync']
    await settle()
    expect(requests.filter((p) => p.startsWith('/api/remote')).length).toBe(asked + 1)
  })

  it("reads them with a sync store, a ticket's by its key, without this server's own device's", async () => {
    caps.value = ['sync']
    replies['/api/sync'] = { state: 'current', device: 'dev-own' }
    replies['/api/remote'] = {
      records: [
        { kind: 'pins', device: 'dev-1', deviceName: 'studio', id: 'pins', rev: 1, at: '', data: { pins: [] } },
        { kind: 'pins', device: 'dev-own', deviceName: 'here', id: 'pins', rev: 1, at: '', data: { pins: [] } },
      ],
    }
    useSyncStatus()
    await settle()
    const r = useRemote('decision', () => 'CND-12')
    await settle()
    await settle()
    expect(requests).toContain('/api/sync')
    expect(requests).toContain('/api/remote?kind=decision&ticket=CND-12')
    expect(r.records.value.map((x) => x.device)).toEqual(['dev-1'])
  })
})

describe("every page of other machines' records", () => {
  const decision = (n: number, state: string) => ({
    kind: 'decision',
    device: 'dev-1',
    deviceName: 'studio',
    id: `dr-${n}`,
    rev: 1,
    at: `2026-11-20T09:${String(n % 60).padStart(2, '0')}:00Z`,
    data: { id: `dr-${n}`, state },
  })

  it("reads the page after each page's next, and draws them all at once", async () => {
    caps.value = ['sync']
    const first = Array.from({ length: 200 }, (_, i) => decision(i + 1, 'applied'))
    replies['/api/remote?kind=decision'] = { records: first, next: 'x/decision-y.json' }
    replies[`/api/remote?kind=decision&after=${encodeURIComponent('x/decision-y.json')}`] = { records: [decision(300, 'open')] }
    const r = useRemote('decision')
    await settle()
    await settle()
    expect(requests.filter((p) => p.startsWith('/api/remote'))).toEqual([
      '/api/remote?kind=decision',
      `/api/remote?kind=decision&after=${encodeURIComponent('x/decision-y.json')}`,
    ])
    expect(r.records.value).toHaveLength(201)
    expect(r.records.value.some((x) => x.id === 'dr-300')).toBe(true)
  })

  it('reads no more pages than the records\' bound fills', async () => {
    caps.value = ['sync']
    vi.stubGlobal('useApi', () => ({
      request: async (path: string) => {
        requests.push(path)
        if (!path.startsWith('/api/remote')) return {}
        return { records: [decision(requests.length, 'open')], next: `x/decision-${requests.length}.json` }
      },
    }))
    const r = useRemote('decision')
    for (let i = 0; i < 60; i++) await settle()
    expect(requests.filter((p) => p.startsWith('/api/remote'))).toHaveLength(10_000 / 200)
    expect(r.records.value).toHaveLength(10_000 / 200)
  })
})

describe("other machines' records read again while a page stays open", () => {
  // A document the composable listens to, its visibility set by the test.
  let doc: { visibilityState: string; listeners: Map<string, () => void>; addEventListener: (t: string, f: () => void) => void; removeEventListener: (t: string) => void }
  const state = (s: string) => ({ records: [{ kind: 'decision', device: 'dev-1', deviceName: 'studio', id: 'dr-41', rev: 1, at: '', data: { id: 'dr-41', state: s } }] })

  beforeEach(() => {
    vi.useFakeTimers()
    doc = {
      visibilityState: 'visible',
      listeners: new Map(),
      addEventListener: (t, f) => doc.listeners.set(t, f),
      removeEventListener: (t) => doc.listeners.delete(t),
    }
    vi.stubGlobal('document', doc)
    caps.value = ['sync']
    replies['/api/remote'] = state('open')
  })
  afterEach(() => vi.useRealTimers())

  const flush = async () => {
    for (let i = 0; i < 5; i++) await vi.advanceTimersByTimeAsync(0)
  }
  const stateOf = (r: ReturnType<typeof useRemote>) => (r.records.value[0]?.data as { state?: string } | undefined)?.state

  it('reads again when the page becomes visible', async () => {
    const r = useRemote('decision')
    await flush()
    expect(stateOf(r)).toBe('open')
    replies['/api/remote'] = state('applied')
    doc.visibilityState = 'hidden'
    doc.listeners.get('visibilitychange')!()
    await flush()
    expect(stateOf(r)).toBe('open')
    doc.visibilityState = 'visible'
    doc.listeners.get('visibilitychange')!()
    await flush()
    expect(stateOf(r)).toBe('applied')
  })

  it('reads again when the events stream comes back after a break, not at its first connection', async () => {
    attention.value = { connected: false }
    const r = useRemote('decision')
    await flush()
    const asked = () => requests.filter((p) => p.startsWith('/api/remote')).length
    const first = asked()
    attention.value = { connected: true }
    await flush()
    expect(asked()).toBe(first)
    replies['/api/remote'] = state('applied')
    attention.value = { connected: false }
    await flush()
    attention.value = { connected: true }
    await flush()
    expect(asked()).toBe(first + 1)
    expect(stateOf(r)).toBe('applied')
  })

  it('reads again every poll while visible, and not while hidden', async () => {
    const r = useRemote('decision')
    await flush()
    const asked = () => requests.filter((p) => p.startsWith('/api/remote')).length
    const first = asked()
    doc.visibilityState = 'hidden'
    await vi.advanceTimersByTimeAsync(REMOTE_POLL_MS)
    expect(asked()).toBe(first)
    doc.visibilityState = 'visible'
    replies['/api/remote'] = state('applied')
    await vi.advanceTimersByTimeAsync(REMOTE_POLL_MS)
    await flush()
    expect(asked()).toBe(first + 1)
    expect(stateOf(r)).toBe('applied')
  })
})

describe('the controls a sync store brings', () => {
  const share = readFileSync(new URL('../components/DispatchShare.vue', import.meta.url), 'utf8')
  const about = readFileSync(new URL('../components/AboutCard.vue', import.meta.url), 'utf8')
  const pinned = readFileSync(new URL('../components/SidebarPinnedList.vue', import.meta.url), 'utf8')
  const sidebar = readFileSync(new URL('../components/SessionSidebar.vue', import.meta.url), 'utf8')
  const decisions = readFileSync(new URL('../components/RemoteDecisionRows.vue', import.meta.url), 'utf8')

  it('draws Share and the other key only with a sync store, each under its condition', () => {
    expect(share).toContain("const shown = computed(() => caps.has('sync'))")
    expect(share).toMatch(/<section\s+v-if="shown"[^>]*data-project-share/)
    expect(share).toMatch(/v-if="canShare"\s+label="Share this project"/)
    expect(share).toContain('const canShare = computed(() => canShareProject(props.project, props.archived))')
    expect(share).toContain("const canRekey = computed(() => !props.archived && (props.project.share?.state === 'refused' || !!props.project.heldBy))")
    expect(share).toMatch(/label="Give this project another key"/)
  })

  it('offers Share in every state the store takes a share from, and in no other', () => {
    const at = (state: ProjectShare['state'] | null, words?: string) => ({ share: state === null ? undefined : { state, words } })
    const offered = (p: ReturnType<typeof at>, archived = false) => canShareProject(p, archived)
    expect(offered(at(null))).toBe(true)
    expect(offered(at('', "sharing was interrupted when this machine's data was copied: share it again"))).toBe(true)
    expect(offered(at('refused', 'the key is taken'))).toBe(true)
    expect(offered(at('dormant'))).toBe(true)
    for (const state of ['pending', 'completing', 'shared'] as const) expect(offered(at(state))).toBe(false)
    expect(offered(at(null), true)).toBe(false)
    expect(offered(at('dormant'), true)).toBe(false)
  })

  it("draws the Sync row from the store's report, with what was kept beneath it, only with a sync store", () => {
    expect(about).toContain("const syncWords = capabilities.has('sync') ? syncLine(sync.report.value) : ''")
    expect(about).toContain("out.push({ key: 'sync', label: 'Sync', value: syncWords, wrap: true })")
    expect(about).toMatch(/v-if="r.key === 'sync' && syncKept.length"[^>]*data-about-sync-kept/)
  })

  it("draws another machine's pins after this machine's, greyed and opening nothing", () => {
    expect(sidebar).toContain("const otherRemote = useRemote('pins')")
    expect(sidebar).toContain(':others="otherPins"')
    const remoteRow = pinned.slice(pinned.indexOf('v-for="p in others"'))
    expect(remoteRow).not.toContain('NuxtLink')
    expect(remoteRow).toContain('text-dimmed')
    expect(remoteRow).toContain('{{ p.where }}')
  })

  it('offers no action on a remote decision but its answer page', () => {
    expect(decisions).not.toMatch(/<UButton/)
    expect(decisions).toContain('Open its answer page')
    expect(decisions).toContain('target="_blank"')
  })
})
