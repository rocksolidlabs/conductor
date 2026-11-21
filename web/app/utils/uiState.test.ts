import { describe, expect, it } from 'vitest'
import { LAYOUT_KEY, SIDE_SPLIT_KEY } from './editorLayout'
import { SPLIT_KEY } from './editorTabs'
import { RECENT_MAX, type Recent } from './palette'
import { SIDEBAR_FOLDS_KEY, SIDEBAR_KEY, SIDEBAR_SIZE_KEY } from './sidebar'
import { contentOf, CREW_GRAPH_KEY, isKept, layoutFromStorage, layoutToStorage, mergeRecents, patchLayout, PROJECT_VIEW_KEY, RUN_VIEW_KEY, sameContent, type UiLayout } from './uiState'

function memory(init: Record<string, string> = {}) {
  const m = new Map(Object.entries(init))
  return { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v), map: m }
}

const full: UiLayout = {
  v: 1,
  editor: { layout: 'side', split: 0.4, sideSplit: 0.6 },
  sidebar: { mode: 'rail', width: 18.5, folds: { pinned: true, exited: false } },
  crewGraph: { direction: 'TB', handoffs: false, members: true },
  runView: 'timeline',
  projectView: 'board',
}

describe('the layout in local storage', () => {
  it('reads back what it writes, each setting under its own key', () => {
    const s = memory()
    layoutToStorage(s, full)
    expect(s.map.get(LAYOUT_KEY)).toBe('side')
    expect(s.map.get(SPLIT_KEY)).toBe('0.4')
    expect(s.map.get(SIDE_SPLIT_KEY)).toBe('0.6')
    expect(s.map.get(SIDEBAR_KEY)).toBe('rail')
    expect(s.map.get(SIDEBAR_SIZE_KEY)).toBe('18.5')
    expect(JSON.parse(s.map.get(SIDEBAR_FOLDS_KEY)!)).toEqual({ pinned: true, exited: false })
    expect(JSON.parse(s.map.get(CREW_GRAPH_KEY)!)).toEqual({ direction: 'TB', handoffs: false, members: true })
    expect(s.map.get(RUN_VIEW_KEY)).toBe('timeline')
    expect(s.map.get(PROJECT_VIEW_KEY)).toBe('board')
    expect(layoutFromStorage(s)).toEqual(full)
  })

  it('leaves out what is not saved or not of its shape, and keeps what a layout leaves out', () => {
    const s = memory({ [LAYOUT_KEY]: 'diagonal', [SPLIT_KEY]: '7', [SIDEBAR_SIZE_KEY]: 'wide', [SIDEBAR_FOLDS_KEY]: '{"elsewhere":true,"needs":"yes"}', [RUN_VIEW_KEY]: '/home', [PROJECT_VIEW_KEY]: 'list' })
    expect(layoutFromStorage(s)).toEqual({ v: 1, projectView: 'list' })
    layoutToStorage(s, { v: 1, runView: 'grid' })
    expect(s.map.get(PROJECT_VIEW_KEY)).toBe('list')
    expect(layoutFromStorage(memory())).toEqual({ v: 1 })
    expect(layoutFromStorage(null)).toEqual({ v: 1 })
  })

  it('lays a patch over a layout one level deep', () => {
    expect(patchLayout(full, { sidebar: { mode: 'full' } }).sidebar).toEqual({ mode: 'full', width: 18.5, folds: { pinned: true, exited: false } })
    expect(patchLayout({ v: 1 }, { editor: { split: 0.3 } })).toEqual({ v: 1, editor: { split: 0.3 } })
  })
})

describe('mergeRecents', () => {
  const a: Recent = { 'go:/': { count: 3, last: 10 }, 'run:r1': { count: 1, last: 50 } }
  const b: Recent = { 'go:/': { count: 1, last: 40 }, 'crew:c1': { count: 2, last: 5 } }

  it('takes each key with its greater count and its later use', () => {
    expect(mergeRecents(a, b)).toEqual({ 'go:/': { count: 3, last: 40 }, 'run:r1': { count: 1, last: 50 }, 'crew:c1': { count: 2, last: 5 } })
  })

  it('is commutative and idempotent, so two machines reach one value', () => {
    const ab = mergeRecents(a, b)
    expect(mergeRecents(b, a)).toEqual(ab)
    expect(mergeRecents(ab, ab)).toEqual(ab)
    expect(mergeRecents(ab, b)).toEqual(ab)
  })

  it(`keeps at most ${RECENT_MAX}, the least lately used dropped`, () => {
    const many: Recent = {}
    for (let i = 0; i < RECENT_MAX; i++) many[`go:/${i}`] = { count: 1, last: 100 + i }
    const out = mergeRecents(many, { 'go:/old': { count: 9, last: 1 }, 'go:/new': { count: 1, last: 1000 } })
    expect(Object.keys(out)).toHaveLength(RECENT_MAX)
    expect(out['go:/new']).toBeDefined()
    expect(out['go:/old']).toBeUndefined()
    expect(out['go:/0']).toBeUndefined()
  })
})

describe('documents', () => {
  it('tells a kept document from none and compares content alone', () => {
    expect(isKept({})).toBe(false)
    expect(isKept({ v: 1, at: '2026-11-19T00:00:00Z', by: 'a'.repeat(32) })).toBe(true)
    expect(contentOf({ v: 1, runView: 'grid', at: 'x', by: 'y' })).toEqual({ v: 1, runView: 'grid' })
    expect(sameContent({ v: 1, sidebar: { mode: 'rail', width: 2 }, at: 'x' }, { sidebar: { width: 2, mode: 'rail' }, v: 1 })).toBe(true)
    expect(sameContent({ v: 1, runView: 'grid' }, { v: 1, runView: 'tasks' })).toBe(false)
  })
})
