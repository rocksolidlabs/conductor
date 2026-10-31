import { describe, expect, it } from 'vitest'
import { accountActions, rowActions, toMenuItems } from './actions'
import { accountItems, rowMenuItems } from './sidebarActions'

const on = { open: () => {}, openRun: () => {}, share: () => {}, yard: () => {}, rename: () => {}, stop: () => {}, forget: () => {} }
const account = { name: () => {}, shortcuts: () => {}, theme: () => {}, token: () => {}, forget: () => {} }

describe('the action descriptors', () => {
  it("are what the sidebar's menus render, item for item", () => {
    for (const t of [
      { kind: 'session' as const, name: 'docs', live: true, id: 's1' },
      { kind: 'session' as const, name: 'core', live: false, inRun: true, id: 's2' },
      { kind: 'session' as const, name: 'renamed', live: true, renamable: true, id: 's3' },
      { kind: 'run' as const, name: 'nightly', live: true, id: 'r1' },
      { kind: 'shared' as const, name: 'review', live: true, id: 'j1' },
    ]) {
      expect(rowMenuItems(t, on)).toEqual(toMenuItems(rowActions(t, on)))
    }
    expect(accountItems(true, account)).toEqual(toMenuItems(accountActions(true, account)))
    expect(accountItems(false, account)).toEqual(toMenuItems(accountActions(false, account)))
  })

  it('name their kind, their target and the question a destructive one asks', () => {
    const groups = rowActions({ kind: 'session', name: 'docs', live: true, id: 's1' }, on)
    const stop = groups.flat().find((a) => a.id === 'stop')!
    expect(stop.kind).toBe('session')
    expect(stop.target).toBe('s1')
    expect(stop.ask).toEqual({ title: 'Stop docs?', detail: 'Its terminal closes. Resume brings the conversation back from Exited.' })
    expect(stop.kbds).toEqual(['X'])
    const run = rowActions({ kind: 'run', name: 'nightly', live: false, id: 'r1' }, on).flat()
    expect(run.find((a) => a.id === 'stop')?.disabled).toBe(true)
    expect(run.find((a) => a.id === 'stop')?.ask?.title).toBe('Stop nightly?')
    expect(run.map((a) => a.id)).toEqual(['open', 'share', 'stop'])
    expect(rowActions({ kind: 'shared', name: 'x', live: true }, on).flat().map((a) => a.id)).toEqual(['open', 'forget'])
    // A server session's Rename… (#318): no keys, nothing to ask, the session it is about named.
    const rename = rowActions({ kind: 'session', name: 'docs', live: false, renamable: true, id: 's1' }, on).flat().find((a) => a.id === 'rename')!
    expect(rename).toMatchObject({ kind: 'session', target: 's1', label: 'Rename…' })
    expect(rename.disabled).toBeUndefined()
    expect(rename.ask).toBeUndefined()
    expect(rename.kbds).toBeUndefined()
  })

  it('give the menu only what a menu shows', () => {
    const items = toMenuItems(rowActions({ kind: 'session', name: 'docs', live: true, id: 's1' }, on)).flat()
    for (const i of items) {
      expect(i).not.toHaveProperty('kind')
      expect(i).not.toHaveProperty('target')
      expect(i).not.toHaveProperty('ask')
      expect(typeof i.onSelect).toBe('function')
    }
  })
})
