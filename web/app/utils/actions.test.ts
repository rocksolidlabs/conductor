import { describe, expect, it } from 'vitest'
import type { SessionInfo } from '~/composables/useSessions'
import { accountActions, rowActions, sessionTarget, toMenuItems } from './actions'
import { accountItems, rowMenuItems } from './sidebarActions'

const on = { open: () => {}, openRun: () => {}, share: () => {}, yard: () => {}, rename: () => {}, clone: () => {}, cloneWithOptions: () => {}, stop: () => {}, forget: () => {} }
const account = { name: () => {}, shortcuts: () => {}, theme: () => {}, token: () => {}, forget: () => {} }

describe('the action descriptors', () => {
  it("are what the sidebar's menus render, item for item", () => {
    for (const t of [
      { kind: 'session' as const, name: 'docs', live: true, id: 's1' },
      { kind: 'session' as const, name: 'core', live: false, inRun: true, id: 's2' },
      { kind: 'session' as const, name: 'renamed', live: true, renamable: true, id: 's3' },
      { kind: 'session' as const, name: 'hosted', live: true, hosted: true, machine: 'studio', id: 's4' },
      { kind: 'session' as const, name: 'attempt', live: true, inRun: true, renamable: true, attempt: true, id: 's5' },
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

  it("clone a session, its reason said beside a disabled one (#434)", () => {
    const clones = (t: Parameters<typeof rowActions>[0]) => rowActions(t, on).flat().filter((a) => a.id.startsWith('clone'))
    const loose = clones({ kind: 'session', name: 'docs', live: false, renamable: true, id: 's1' })
    expect(loose.map((a) => [a.id, a.label, a.target, a.disabled, a.reason])).toEqual([
      ['clone', 'Clone…', 's1', undefined, undefined],
      ['clone-options', 'Clone with options…', 's1', undefined, undefined],
    ])
    for (const a of clones({ kind: 'session', name: 'laptop', live: true, hosted: true, machine: 'studio', id: 's2' })) {
      expect(a).toMatchObject({ disabled: true, reason: 'Runs on studio: Conductor cannot start a session there yet' })
    }
    const [quick, options] = clones({ kind: 'session', name: 'a · t1 #1', live: true, inRun: true, attempt: true, id: 's3' })
    expect(quick).toMatchObject({ disabled: true })
    expect(quick!.reason).toContain('Yardmaster attempt')
    expect(options!.disabled).toBeUndefined()
    // The reason is the menu item's description.
    const item = toMenuItems([[quick!]])[0]![0]!
    expect(item).toMatchObject({ label: 'Clone…', disabled: true, description: quick!.reason })
  })

  it("read a session's row from the session: hosted, an attempt", () => {
    const base = { id: 's1', name: 'docs', kind: 'server', agentId: 'claude', command: [], cwd: '/r', status: 'running', cols: 80, rows: 24, viewers: 0, createdAt: '' } as SessionInfo
    expect(sessionTarget(base, true)).toEqual({ kind: 'session', name: 'docs', live: true, inRun: false, renamable: true, hosted: false, machine: undefined, attempt: false, id: 's1' })
    expect(sessionTarget({ ...base, kind: 'hosted', hostName: 'studio' }, true)).toMatchObject({ hosted: true, machine: 'studio', renamable: false })
    expect(sessionTarget({ ...base, crew: { runId: 'r', crewId: 'c', member: 'a', task: 't1', attempt: 1 } }, false)).toMatchObject({ inRun: true, attempt: true, live: false })
    expect(sessionTarget({ ...base, crew: { runId: 'r', crewId: 'c', member: 'a' } }, true)).toMatchObject({ inRun: true, attempt: false })
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
