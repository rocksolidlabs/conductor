import { describe, expect, it, vi } from 'vitest'
import type { RunInfo, RunMember } from '~/composables/useSessions'
import { needsItemRow, waitingRows } from './usePaletteRows'

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
