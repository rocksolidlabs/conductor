import { describe, expect, it, vi } from 'vitest'
import type { RunInfo, RunMember, SessionInfo } from '~/composables/useSessions'
import type { NeedsItem } from '~/utils/needsYou'
import { sidebarModel, type RunBlock } from '~/utils/sidebar'
import { needsItemRow, runRowLook, waitingRows } from './usePaletteRows'

// The palette's store keeps module state in Nuxt's auto-imported refs: not needed by the rows' builder.
vi.mock('~/composables/usePalette', () => ({ usePalette: () => ({}) }))

// A plan's and a task's rows in the palette's Needs you (the UX review's P3): their kind's word, each opening its item on the page.
describe("the palette's plan and task rows", () => {
  it("says the kind's word and opens the item on the Needs you page", () => {
    const went: string[] = []
    const summary = (ref: string, kind: string, title: string, question: string, brief: string) => ({ ref, kind, since: '', title, question, brief, answerer: '', actions: [] })
    const rows = [
      needsItemRow({ kind: 'plan', ref: 'plan:CND-2/1', summary: summary('plan:CND-2/1', 'plan', 'Pairing rollout, revision 1', 'Approve the plan?', '') }, (to) => went.push(to)),
      needsItemRow({ kind: 'task', ref: 'task:r1/build', summary: summary('task:r1/build', 'task', 'build failed in run release', '', 'Task build of run release is failed after 3 attempts') }, (to) => went.push(to)),
    ]
    expect(rows.map((r) => [r.key, r.label, r.detail])).toEqual([
      ['needs:plan:CND-2/1', 'Plan: Pairing rollout, revision 1', 'Approve the plan?'],
      ['needs:task:r1/build', 'Task: build failed in run release', 'Task build of run release is failed after 3 attempts'],
    ])
    rows[1]!.run()
    expect(went).toEqual([`/decisions?item=${encodeURIComponent('task:r1/build')}`])
    expect(rows[0]!.href).toBe(`/decisions?item=${encodeURIComponent('plan:CND-2/1')}`)
  })
})

// The `#` palette's waiting members are the members whose tile offers Start now (memberOffer), whatever their start condition.
describe("the # palette's waiting members", () => {
  const m = (name: string, status: RunMember['status'], start: RunMember['start']) => ({ name, agentId: 'claude', status, start }) as RunMember
  const run = (id: string, over: Partial<RunInfo>, members: RunMember[]) => ({ id, name: id, members, ...over }) as RunInfo

  it('lists a waiting member that starts by hand or after another, and none of a stopped run, a managed run or a record', () => {
    const started: string[] = []
    const rows = waitingRows(
      [
        run('plain', {}, [m('lead', 'running', { when: 'immediately' }), m('later', 'pending', { when: 'manual' }), m('review', 'pending', { when: 'after', member: 'lead' })]),
        run('stopped', { stoppedAt: '2026-11-15T10:00:00Z' }, [m('later', 'pending', { when: 'manual' })]),
        run('managed', { managed: { revision: 1 } as RunInfo['managed'] }, [m('deployer', 'pending', { when: 'task' })]),
        run('record', { record: true }, [m('later', 'pending', { when: 'manual' })]),
      ],
      (r, name) => started.push(`${r.id}/${name}`),
    )
    expect(rows.map((r) => [r.key, r.label, r.detail])).toEqual([
      ['member:plain:later', 'later · Start now', 'plain · claude · waiting for a hand'],
      ['member:plain:review', 'review · Start now', 'plain · claude · waiting to start'],
    ])
    rows[1]!.run()
    expect(started).toEqual(['plain/review'])
  })
})

// A run's row: its members' tally said once, after the names of those who ask, and the mark of the run's own state (the one that
// places it in the sidebar's list), never its members' alone.
describe("the palette's run row", () => {
  const session = (id: string, attention: Partial<NonNullable<SessionInfo['attention']>>) =>
    ({ id, name: id, kind: 'server', agentId: 'claude', command: [], cwd: '/w', status: 'running', cols: 80, rows: 24, viewers: 0, createdAt: '2026-10-01T09:00:00Z', turns: true, crew: { runId: 'r1', crewId: 'c', member: id }, attention: { since: '2026-10-01T09:01:00Z', source: 'api', ...attention } }) as SessionInfo
  const run = { id: 'r1', crewId: 'c', name: 'API sweep', goal: '', cwd: '/w', isolation: 'none', startedAt: '2026-10-01T08:31:00Z', members: ['review', 'core', 'lead'].map((name) => ({ name, agentId: 'claude', start: { when: 'immediately' }, status: 'running' })), log: [], state: 'running', needsInput: 0, yolo: false } as unknown as RunInfo
  const block = (sessions: SessionInfo[], needs: NeedsItem[] = []) => {
    const m = sidebarModel(sessions, () => run, needs)
    return [...m.needs, ...m.finished, ...m.working].find((it): it is RunBlock => it.kind === 'run')!
  }

  it('says the tally once and draws the run\'s own state', () => {
    const three = block([session('review', { state: 'needs_input' }), session('core', { state: 'working' }), session('lead', { state: 'done' })])
    const look = runRowLook(three, run)
    expect(look.detail).toMatch(/^started \d\d:\d\d · 3 agents · review asks · 1 working · 1 finished$/)
    expect(look.status.key).toBe('needs')
    // A run that waits on a failed task while its members work: amber, as the sidebar lists it in Needs you.
    const task: NeedsItem = { kind: 'task', ref: 'task:r1/build', run: 'r1', since: '2026-10-01T09:03:00Z', summary: { ref: 'task:r1/build', kind: 'task', since: '', title: 'build failed', brief: '', question: '', answerer: '', actions: [] } }
    const waits = block([session('core', { state: 'working' }), session('lead', { state: 'working' })], [task])
    expect(waits.state).toBe('needs')
    expect(runRowLook(waits, run).status.key).toBe('needs')
    expect(runRowLook(waits, run).detail).toMatch(/ · 2 working$/)
  })
})
