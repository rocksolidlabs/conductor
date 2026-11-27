import { describe, expect, it } from 'vitest'
import type { SessionInfo } from '~/composables/useSessions'
import { accountActions, restartQuestion, resumeQuestion, revokeQuestion, rowActions, saveRestartQuestion, sessionTarget, stopFinishedQuestion, stopQuestion, stopRunPageQuestion, toMenuItems, type Ask } from './actions'
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
    const withPhone = { ...account, phone: () => {} }
    expect(accountItems(true, withPhone)).toEqual(toMenuItems(accountActions(true, withPhone)))
    const tools = accountActions(true, withPhone)[1]!
    expect(tools.map((a) => a.id)).toEqual(['shortcuts', 'theme', 'phone'])
    expect(tools[2]).toMatchObject({ kind: 'account', label: 'Open on your phone', icon: 'i-lucide-smartphone' })
  })

  it('name their kind, their target and the question a destructive one asks', () => {
    const groups = rowActions({ kind: 'session', name: 'docs', live: true, id: 's1' }, on)
    const stop = groups.flat().find((a) => a.id === 'stop')!
    expect(stop.kind).toBe('session')
    expect(stop.target).toBe('s1')
    expect(stop.ask).toEqual({ title: 'Stop docs?', detail: 'Its terminal closes. Resume brings the conversation back from Exited.', confirm: 'Stop', keep: 'Cancel' })
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

// Every question the palette can ask in a row (#1075): its two buttons' words come with it, never the same word, and they are the
// words the same action's own dialog says (the sidebar row's Cancel and Stop, the run header's Stop the run, Settings' buttons, the
// record page's Keep it and Resume). The task commands' questions are useTaskCommands.test.ts's.
describe('the questions the palette asks', () => {
  const asks: Array<[string, Ask, string, string]> = [
    ['a session row’s Stop…', stopQuestion({ kind: 'session', name: 'docs' }), 'Stop', 'Cancel'],
    ['a run row’s Stop run…', stopQuestion({ kind: 'run', name: 'nightly' }), 'Stop', 'Cancel'],
    ['the run page’s Stop run…', stopRunPageQuestion('nightly'), 'Stop the run', 'Cancel'],
    ['Stop every finished session', stopFinishedQuestion(['a', 'b']), 'Stop them', 'Cancel'],
    ['Restart server', restartQuestion(2), 'Restart server', 'Keep it running'],
    ['Save and restart', saveRestartQuestion(2), 'Save and restart', 'Keep editing'],
    ['a link’s Revoke', revokeQuestion('review'), 'Revoke', 'Cancel'],
    ['a History row’s Resume', resumeQuestion('docs'), 'Resume', 'Keep it'],
  ]
  it.each(asks)('%s says what its dialog says, in two words that differ', (_what, ask, confirm, keep) => {
    expect([ask.confirm, ask.keep]).toEqual([confirm, keep])
    expect(ask.confirm).not.toBe(ask.keep)
    expect(ask.title).not.toBe('')
  })

  it('keep their titles and details', () => {
    expect(stopRunPageQuestion('nightly')).toEqual({ ...stopQuestion({ kind: 'run', name: 'nightly' }), confirm: 'Stop the run' })
    expect(stopFinishedQuestion(['a'])).toMatchObject({ title: 'Stop 1 finished session?', detail: 'a' })
    expect(stopFinishedQuestion(['a', 'b'])).toMatchObject({ title: 'Stop 2 finished sessions?', detail: 'a, b' })
    expect(restartQuestion(0).detail).toBe('No agent is running.')
    expect(restartQuestion(1).detail).toBe('1 running agent stops.')
    expect(restartQuestion(2)).toEqual({ title: 'Restart the server?', detail: '2 running agents stop.', confirm: 'Restart server', keep: 'Keep it running' })
    // Settings' save bar asks the same words (UX3 R56); the old ones are gone.
    expect(saveRestartQuestion(1)).toMatchObject({ title: 'Restart the server now?', detail: '1 running agent stops.' })
    expect(saveRestartQuestion(3)).toEqual({ title: 'Restart the server now?', detail: '3 running agents stop.', confirm: 'Save and restart', keep: 'Keep editing' })
    expect(JSON.stringify(saveRestartQuestion(3))).not.toMatch(/sessions end|Save and restart the server/)
    expect(revokeQuestion('')).toMatchObject({ title: 'Revoke the link?', detail: 'Viewers using it are disconnected.' })
    expect(resumeQuestion('docs')).toMatchObject({ title: 'Resume docs?', detail: "A new session takes up its agent's own conversation." })
  })

  it('are the questions the rows carry', () => {
    const stop = (t: Parameters<typeof rowActions>[0]) => rowActions(t, on).flat().find((a) => a.id === 'stop')!.ask
    expect(stop({ kind: 'session', name: 'docs', live: true, id: 's1' })).toEqual(stopQuestion({ kind: 'session', name: 'docs' }))
    expect(stop({ kind: 'run', name: 'nightly', live: true, id: 'r1' })).toEqual(stopQuestion({ kind: 'run', name: 'nightly' }))
  })
})
