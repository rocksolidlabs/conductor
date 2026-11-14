import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { reactive, readonly, toRef } from 'vue'
import { ApiError } from './useApi'
import { useJoined } from './useJoined'
import { usePageMemory } from './usePageMemory'
import type { JoinInfo } from './useSessions'

// The links joined from this workbench, as the page holds them (useJoined): each entry carries its link's token, and the list lives
// in the page's memory. Each `load` is a new page: a new Nuxt app, its payload's state written by useState as Nuxt's is (what Nuxt
// writes into the tab's storage at a reload it makes itself), and after every event that sets the list no key of that state holds a
// token. Tokens are invented strings.
const TOKENS = ['joined-link-token-0123456789', 'joined-link-token-9876543210']
const info: JoinInfo = { run: { id: 'run-1', name: 'review crew', members: [] }, role: 'view' }

let app: { payload: { state: Record<string, unknown> } }
let join: ReturnType<typeof vi.fn>

/** A load of the page: a new Nuxt app, useState writing into its payload's state; another key of that state is set, as the app's are. */
function load() {
  app = { payload: { state: reactive({}) as Record<string, unknown> } }
  vi.stubGlobal('useNuxtApp', () => app)
  vi.stubGlobal('useState', (key: string, init: () => unknown) => {
    const r = toRef(app.payload.state, `$s${key}`)
    if (r.value === undefined) r.value = init()
    return r
  })
  ;(useState as (key: string, init: () => unknown) => unknown)('sidebarMode', () => 'list')
}
function noToken(after: string) {
  const text = JSON.stringify(app.payload.state)
  expect(text, 'the state is written').toContain('sidebarMode')
  for (const t of TOKENS) expect(text, after).not.toContain(t)
}

beforeEach(() => {
  join = vi.fn()
  vi.stubGlobal('readonly', readonly)
  vi.stubGlobal('useSessions', () => ({ join }))
  vi.stubGlobal('document', { visibilityState: 'visible' })
  load()
})
afterEach(() => vi.unstubAllGlobals())

describe('the joined links', () => {
  it('are one list for the page, outside the state Nuxt writes at a reload, after every event that sets it', async () => {
    const j = useJoined()
    j.add({ token: TOKENS[0]!, server: '', host: 'this workbench', kind: 'run', name: 'review crew', role: 'view', members: 0 })
    noToken('add')
    j.add({ token: TOKENS[1]!, server: '', host: 'this workbench', kind: 'session', name: 'docs', role: 'control' })
    noToken('a second add')
    j.noteInfo('', TOKENS[0]!, info)
    noToken('noteInfo')
    // Read by another caller of the page: the same list.
    expect(useJoined().list.value.map((e) => e.token)).toEqual([TOKENS[1], TOKENS[0]])
    join.mockResolvedValueOnce(info)
    await j.refresh(j.list.value[0]!)
    noToken('refresh answered')
    join.mockRejectedValue(new ApiError(404, 'revoked', 'revoked'))
    vi.useFakeTimers()
    const all = j.refreshAll()
    await vi.runAllTimersAsync()
    await all
    vi.useRealTimers()
    noToken('refreshAll refused')
    expect(j.list.value.map((e) => e.lastStatus)).toEqual(['revoked', 'revoked'])
    j.forget(j.list.value[0]!.id)
    noToken('forget')
    expect(j.list.value.map((e) => e.token)).toEqual([TOKENS[0]])
    expect(usePageMemory<unknown[]>('joined', () => []).value).toHaveLength(1)
  })

  it('a new page starts from its own list', () => {
    useJoined().add({ token: TOKENS[0]!, server: '', host: 'this workbench', kind: 'run', name: 'review crew', role: 'view' })
    load()
    expect(useJoined().list.value).toEqual([])
    noToken('a load')
  })
})
