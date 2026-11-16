import { describe, expect, it, vi } from 'vitest'
import type { RunInfo, RunMember } from '~/composables/useSessions'
import { decisionRows, waitingRows } from './usePaletteRows'

// The palette's store keeps module state in Nuxt's auto-imported refs: not needed by the rows' builder.
vi.mock('~/composables/usePalette', () => ({ usePalette: () => ({}) }))

// The palette's rows for the decision page (docs/plans/decision-inbox.md, PR-2): one per open item, by its ref, each opening the item.

describe("the palette's decision rows", () => {
  it('names each open item by its ref and opens it on the decision page', () => {
    const went: string[] = []
    const rows = decisionRows(
      [
        { ref: 'question:s1/q1', title: 'Claude Code in api-fix asks', question: 'Which database?', brief: '' },
        { ref: 'task:r1/build', title: 'build failed in run release', question: '', brief: 'Task build of run release is failed after 3 attempts' },
      ],
      (to) => went.push(to),
    )
    expect(rows.map((r) => [r.key, r.label, r.detail])).toEqual([
      ['decide:question:s1/q1', 'Decide: Claude Code in api-fix asks', 'Which database?'],
      ['decide:task:r1/build', 'Decide: build failed in run release', 'Task build of run release is failed after 3 attempts'],
    ])
    rows[1]!.run()
    expect(went).toEqual([`/decisions?item=${encodeURIComponent('task:r1/build')}`])
    expect(rows[0]!.href).toBe(`/decisions?item=${encodeURIComponent('question:s1/q1')}`)
    expect(decisionRows([], () => {})).toEqual([])
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
