import { describe, expect, it } from 'vitest'
import { launchBlockers } from './dispatchPlans'
import { heldAsView, originWords, viewLaunchWords, viewTaskLines, type PlanViewDoc } from './planView'

const view: PlanViewDoc = {
  v: 1,
  digest: 'd'.repeat(64),
  base: 'ab'.repeat(20),
  crew: {
    id: 'crew',
    name: 'Crew',
    tasks: [
      { id: 'build', kind: 'work', member: 'dev', agent: 'builder', instructions: 'Build it', needs: [] },
      { id: 'review', kind: 'review', member: 'rev', agent: '', instructions: 'Review it', needs: ['build'] },
    ],
  },
  agents: ['builder'],
  references: [],
  attachments: [],
  comments: [],
  constraints: '',
  decisions: [],
  note: '',
}

describe('a revision made on another machine', () => {
  it('is held as its view only when the server says its body is not here', () => {
    expect(heldAsView({ bodyHere: false })).toBe(true)
    expect(heldAsView({ bodyHere: true })).toBe(false)
    expect(heldAsView({})).toBe(false)
  })

  it('names the machines it was made and approved on, and none for this one', () => {
    expect(originWords({ origin: 'studio', approvedOrigin: 'studio' })).toBe('made on studio, approved on studio')
    expect(originWords({ approvedOrigin: 'laptop' })).toBe('approved on laptop')
    expect(originWords({})).toBe('')
  })

  it('draws each task with who does it and what it needs', () => {
    expect(viewTaskLines(view).map((t) => t.line)).toEqual(['build: by dev (builder)', 'review: review by rev, after build'])
  })

  it('is never offered a Launch here, the machine that keeps its crew named', () => {
    const p = { n: 1, state: 'approved' as const, crewName: 'Crew', bodyHere: false, origin: 'studio' }
    const why = launchBlockers(p, { ticket: { key: 'CND-1', status: 'ready' }, project: 'CND', archived: false })
    expect(why).toEqual([viewLaunchWords(p)])
    expect(why[0]).toContain('studio')
    expect(launchBlockers({ ...p, bodyHere: true }, { ticket: { key: 'CND-1', status: 'ready' }, project: 'CND', archived: false })).toEqual([])
  })
})
