import { readFileSync } from 'node:fs'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as vue from 'vue'
import { useRemote, useSyncStatus } from './useRemote'

// Other machines' records and the sync store's state (docs/plans/sync-seams.md, OS-4), read only while a sync store is registered:
// `sync` in a stubbed capabilities reply. Nuxt's auto-imports (Vue's, useCapabilities, useApi, useState) are stubbed as globals,
// read when the composables are called; the module is imported once with the file. The Share, rekey and Sync controls need a
// registered provider, which the e2e server never has: their drawing is held here, the components' sources checked for the
// bindings that draw them, as the app's vitest mounts no component.

let caps: vue.Ref<string[]>
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
      return replies[path.split('?')[0]!] ?? {}
    },
  }))
  vi.stubGlobal('useState', (key: string, init: () => unknown) => {
    if (!states.has(key)) states.set(key, vue.ref(init()))
    return states.get(key)
  })
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
    expect(share).toContain("const canRekey = computed(() => !props.archived && (props.project.share?.state === 'refused' || !!props.project.heldBy))")
    expect(share).toMatch(/label="Give this project another key"/)
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
