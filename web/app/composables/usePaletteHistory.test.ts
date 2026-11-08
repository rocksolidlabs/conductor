import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as vue from 'vue'
import type { HistoryItem, HistoryReply } from '~/utils/history'
// Imported with the file, not inside a test; their auto-imports are globals read when they are called, stubbed below.
import { ApiError } from './useApi'
import { useHistory } from './useHistory'
import { useHistorySearch } from './useHistorySearch'
import { usePaletteHistory } from './usePaletteHistory'

// The palette's history (Program item HS1, PR-7), review round 1 on #746, wired to an API whose every request the test holds and lets go
// in the order it needs: Find this elsewhere invoked from a plain search's History row shows its answer there; a resume whose outcome
// is not known (both replies lost, an uncertain receipt) says so and opens the record whose receipts tell, never "did not resume"; a
// search cut at a bound says which in its More row.

function held<T>() {
  let ok!: (v: T) => void
  let fail!: (e: unknown) => void
  const p = new Promise<T>((resolve, reject) => {
    ok = resolve
    fail = reject
  })
  return { p, ok, fail }
}

let sent: Array<{ path: string; method: string; query?: Record<string, string> } & ReturnType<typeof held<unknown>>>
let unmount: Array<() => void>
let toasts: Array<{ title: string; description?: string }>
let went: string[]
let query: vue.Ref<string>
let scope: vue.EffectScope

beforeEach(() => {
  vi.useFakeTimers()
  sent = []
  unmount = []
  toasts = []
  went = []
  query = vue.ref('')
  for (const k of ['ref', 'computed', 'watch', 'shallowRef'] as const) vi.stubGlobal(k, vue[k])
  vi.stubGlobal('onBeforeUnmount', (f: () => void) => unmount.push(f))
  vi.stubGlobal('useApi', () => ({
    request: (path: string, o?: { method?: string; query?: Record<string, string> }) => {
      const h = held<unknown>()
      sent.push({ path, method: o?.method ?? 'GET', query: o?.query, ...h })
      return h.p
    },
  }))
  vi.stubGlobal('useHistory', useHistory)
  vi.stubGlobal('useHistorySearch', useHistorySearch)
  vi.stubGlobal('useWorkbenchToken', () => ({ hasToken: vue.ref(true), needsToken: vue.ref(false), token: vue.ref('t') }))
  vi.stubGlobal('usePalette', () => ({ open: vue.ref(true), query }))
  vi.stubGlobal('useRoute', () => ({ path: '/agents', params: {} }))
  vi.stubGlobal('useCopy', () => () => {})
  vi.stubGlobal('useToast', () => ({ add: (t: { title: string; description?: string }) => toasts.push(t) }))
})
afterEach(() => {
  scope?.stop()
  for (const f of unmount) f()
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

const settle = async () => {
  for (let i = 0; i < 5; i++) await new Promise<void>((r) => queueMicrotask(r))
  await vue.nextTick()
  for (let i = 0; i < 5; i++) await new Promise<void>((r) => queueMicrotask(r))
}
const last = (method: string, path: string) => {
  const r = [...sent].reverse().find((s) => s.method === method && s.path === path)
  if (!r) throw new Error(`no ${method} ${path} sent; sent: ${sent.map((s) => `${s.method} ${s.path}`).join(', ')}`)
  return r
}

const receipt = { key: 'check:r/build/1/fixed/1', kind: 'check', class: 'checked', outcome: 'failed', title: 'fixed on build #1', url: '/runs/r?attempt=1', anchor: { ref: 'task:r/build', label: 'build' }, links: [], host: {}, project: {}, revision: {}, source: { label: 'build #1' }, availability: { state: 'log', words: 'log kept' } } as unknown as HistoryItem
const session = { key: 'session:0123456789abcdef', kind: 'session', class: 'record', title: 'fix-tests', url: '/history/sessions/0123456789abcdef', anchor: { ref: 'session:0123456789abcdef', label: 'fix-tests' }, links: [], host: {}, project: {}, revision: {}, source: { label: 'fix-tests' }, availability: { state: 'record', words: 'a record' } } as unknown as HistoryItem
const reply = (entries: HistoryItem[], extra: Partial<HistoryReply> = {}): HistoryReply => ({ entries, total: entries.length, indexing: { state: 'ready' }, ...extra })

async function mounted() {
  scope = vue.effectScope()
  const h = scope.run(() => usePaletteHistory({ go: (to) => went.push(to), token: () => {} }))!
  await settle()
  return h
}

describe('Find this elsewhere from a plain search', () => {
  it("shows the similar route's answer in the plain search's History group", async () => {
    const h = await mounted()
    query.value = 'stub-fixed'
    await settle()
    vi.advanceTimersByTime(200)
    last('GET', '/api/history').ok(reply([receipt]))
    await settle()
    const group = h.searchGroup('stub-fixed', Date.now())
    const row = group.rows.find((r) => r.key === `history:${receipt.key}`)!
    row.actions!.find((a) => a.id === 'elsewhere')!.run()
    await settle()
    expect(h.searchGroup('stub-fixed', Date.now()).id).toBe('history-similar')
    last('GET', '/api/history/similar').ok({ entries: [{ ...receipt, key: 'check:r2/build/1/fixed/1' }] })
    await settle()
    const shown = h.searchGroup('stub-fixed', Date.now())
    expect(shown.id).toBe('history-similar')
    expect(shown.rows.map((r) => r.key)).toContain('history:check:r2/build/1/fixed/1')
  })
})

describe("a resume from the palette whose outcome is not known", () => {
  async function resumeRow() {
    const h = await mounted()
    query.value = '~'
    await settle()
    vi.advanceTimersByTime(200)
    last('GET', '/api/history').ok(reply([session]))
    await settle()
    const find = () => h.modeGroups('', Date.now()).flatMap((g) => g.rows).find((r) => r.key === `history:${session.key}`)!
    find().reveal!()
    await settle()
    last('GET', '/api/history/entry').ok({ entry: { ...session, availability: { ...session.availability, resume: { offered: 'yes' } } } })
    await settle()
    return find().actions!.find((a) => a.id === 'resume')!
  }

  it('says it is not known when both replies were lost, and opens the record, never "did not resume"', async () => {
    const resume = await resumeRow()
    resume.run()
    await settle()
    last('POST', '/api/history/sessions/0123456789abcdef/resume').fail(new ApiError(0, 'network', 'Cannot reach the server'))
    await settle()
    last('POST', '/api/history/sessions/0123456789abcdef/resume').fail(new ApiError(0, 'network', 'Cannot reach the server'))
    await settle()
    expect(toasts.map((t) => t.title)).toEqual(['fix-tests: whether it resumed is not known'])
    expect(toasts[0]!.description).toMatch(/The outcome is unknown/)
    expect(went).toEqual(['/history/sessions/0123456789abcdef'])
  })

  it('says an uncertain receipt is not known', async () => {
    const resume = await resumeRow()
    resume.run()
    await settle()
    const body = { error: { code: 'resume_uncertain', message: 'uncertain' }, receipt: { commandId: 'c', state: 'uncertain', error: 'the server stopped while starting it', at: '2026-11-08T00:00:00Z' } }
    last('POST', '/api/history/sessions/0123456789abcdef/resume').fail(new ApiError(409, 'resume_uncertain', 'uncertain', body.error, body))
    await settle()
    expect(toasts.map((t) => t.title)).toEqual(['fix-tests: whether it resumed is not known'])
    expect(toasts[0]!.description).toBe('the server stopped while starting it')
    expect(went).toEqual(['/history/sessions/0123456789abcdef'])
  })

  it('says a refusal did not resume', async () => {
    const resume = await resumeRow()
    resume.run()
    await settle()
    const body = { error: { code: 'not_resumable', message: 'no conversation' }, receipt: { commandId: 'c', state: 'failed', code: 'not_resumable', error: 'no conversation', at: '2026-11-08T00:00:00Z' } }
    last('POST', '/api/history/sessions/0123456789abcdef/resume').fail(new ApiError(409, 'not_resumable', 'no conversation', body.error, body))
    await settle()
    expect(toasts.map((t) => t.title)).toEqual(['fix-tests did not resume'])
    expect(went).toEqual([])
  })
})

describe("a search cut at a bound", () => {
  it('says which bound in its More row and in the plain search, never "All N matches"', async () => {
    const h = await mounted()
    query.value = '~ stub'
    await settle()
    vi.advanceTimersByTime(200)
    last('GET', '/api/history').ok(reply([receipt], { total: 3000, next: 'c1', cut: 'ranked' }))
    await settle()
    const more = h.modeGroups('stub', Date.now()).find((g) => g.id === 'history-more')!.rows[0]!
    expect(more.label).toBe('3000 matches, the searched ones on the History page')
    expect(more.detail).toMatch(/only the newest 2,000 of them are ranked/)
    query.value = 'stub'
    await settle()
    const all = h.searchGroup('stub', Date.now()).rows.find((r) => r.key === 'history:all')!
    expect(all.detail).toMatch(/^3000 in the history; only the newest 2,000/)
  })
})
