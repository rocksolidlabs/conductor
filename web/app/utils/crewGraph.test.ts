import { useVueFlow } from '@vue-flow/core'
import { describe, expect, it } from 'vitest'
import type { RunTask } from '~/composables/useSessions'
import type { ActivityEntry } from '~/utils/protocol'
import { connectError, connectionAllowed, crewGraph, depths, GAP_X, GAP_Y, HANDOFF_ARC, handoffEdges, handoffPath, handoffsOf, layout, listRows, NODE_H, NODE_W, withStart, type GraphMember } from './crewGraph'
import { taskLayout } from './taskGraph'

const m = (name: string, start: GraphMember['start'] = { when: 'immediately' }, agentId = 'claude'): GraphMember => ({ name, agentId, start })

const crew: GraphMember[] = [m('lead'), m('core', { when: 'after', member: 'lead' }), m('cli', { when: 'after', member: 'lead' }), m('tests', { when: 'after', member: 'cli' }), m('docs', { when: 'manual' })]

describe('depths', () => {
  it('puts roots at 0 and each dependent one deeper', () => {
    const { depth, warnings } = depths(crew)
    expect([...depth.entries()]).toEqual([
      ['lead', 0],
      ['core', 1],
      ['cli', 1],
      ['tests', 2],
      ['docs', 0],
    ])
    expect(warnings.size).toBe(0)
  })

  it('treats a rule naming a member that is not here as a root, with a warning', () => {
    const { depth, warnings } = depths([m('a'), m('b', { when: 'after', member: 'gone' })])
    expect(depth.get('b')).toBe(0)
    expect(warnings.get('b')).toBe('starts after gone, which is not in this crew')
  })

  it('breaks a cycle into roots with a warning', () => {
    const { depth, warnings } = depths([m('a', { when: 'after', member: 'b' }), m('b', { when: 'after', member: 'a' }), m('c', { when: 'after', member: 'a' })])
    expect(depth.get('a')).toBe(0)
    expect(depth.get('b')).toBe(0)
    expect(depth.get('c')).toBe(1)
    expect(warnings.get('a')).toContain('cycle')
    expect(warnings.get('b')).toContain('cycle')
    expect(warnings.has('c')).toBe(false)
  })
})

describe('layout', () => {
  it('lays columns by depth and rows by crew order, left to right', () => {
    const g = layout(crew)
    const at = Object.fromEntries(g.nodes.map((n) => [n.id, [n.x, n.y]]))
    expect(at).toEqual({
      lead: [0, 0],
      docs: [0, NODE_H + GAP_Y],
      core: [NODE_W + GAP_X, 0],
      cli: [NODE_W + GAP_X, NODE_H + GAP_Y],
      tests: [2 * (NODE_W + GAP_X), 0],
    })
    expect(g.edges.map((e) => e.id)).toEqual(['after:lead-core', 'after:lead-cli', 'after:cli-tests'])
    expect(g.nodes.map((n) => n.row)).toEqual([0, 0, 1, 0, 1])
  })

  it('lays top to bottom when asked', () => {
    const g = layout(crew, 'TB')
    const core = g.nodes.find((n) => n.id === 'core')!
    expect([core.x, core.y]).toEqual([0, NODE_H + GAP_Y])
  })

  it('draws no edge for a rule it could not place', () => {
    const g = layout([m('a'), m('b', { when: 'after', member: 'gone' })])
    expect(g.edges).toEqual([])
    expect(g.nodes[1]!.warning).toContain('gone')
  })

  it('handles three roots and one member alone', () => {
    expect(layout([m('a'), m('b'), m('c')]).nodes.map((n) => [n.depth, n.row])).toEqual([
      [0, 0],
      [0, 1],
      [0, 2],
    ])
    expect(layout([m('solo')]).nodes).toHaveLength(1)
    expect(layout([]).nodes).toEqual([])
  })
})

describe('handoffEdges', () => {
  it('aggregates per pair with a count and the last words', () => {
    const names = new Set(['a', 'b', 'c'])
    const edges = handoffEdges(
      [
        { from: 'a', to: 'b', at: '2026-10-03T10:00:00Z', message: 'first' },
        { from: 'a', to: 'b', at: '2026-10-03T10:05:00Z' },
        { from: 'a', to: 'b', at: '2026-10-03T10:02:00Z', message: 'second' },
        { from: 'b', to: 'a', at: '2026-10-03T10:03:00Z', message: 'back' },
        { from: 'a', to: 'a', at: '2026-10-03T10:03:00Z' },
        { from: 'x', to: 'a', at: '2026-10-03T10:03:00Z' },
      ],
      names,
    )
    expect(edges).toEqual([
      { id: 'handoff:a-b', from: 'a', to: 'b', kind: 'handoff', count: 3, last: 'second', lastAt: '2026-10-03T10:02:00Z' },
      { id: 'handoff:b-a', from: 'b', to: 'a', kind: 'handoff', count: 1, last: 'back', lastAt: '2026-10-03T10:03:00Z' },
    ])
  })
})

describe('handoffsOf', () => {
  const at = '2026-10-03T10:00:00Z'
  it('reads delivered handoffs from the run log fields and their words from the feed', () => {
    const log: ActivityEntry[] = [
      { at, type: 'status', message: 'handoff delivered from a to b', byName: 'a', to: 'b' },
      { at, type: 'status', message: "typed a's prompt" },
      { at, type: 'error', message: 'handoff dropped from a to c: no session' },
    ]
    const feed = [{ at: '2026-10-03T09:59:00Z', type: 'handoff' as const, to: 'b', message: 'over to you', sessionId: 's-a' }]
    expect(handoffsOf(log, feed, new Map([['s-a', 'a']]))).toEqual([{ from: 'a', to: 'b', at, message: 'over to you' }])
  })

  it('reads an older server note from its words alone', () => {
    expect(handoffsOf([{ at, type: 'status', message: 'handoff delivered from lead to tests' }], [], new Map())).toEqual([{ from: 'lead', to: 'tests', at, message: undefined }])
  })
})

describe('crewGraph', () => {
  it('adds the states and the handoff edges', () => {
    const g = crewGraph(crew, { states: new Map([['lead', 'running']]), handoffs: [{ from: 'core', to: 'tests', at: '2026-10-03T10:00:00Z' }] })
    expect(g.nodes[0]!.state).toBe('running')
    expect(g.edges.at(-1)).toMatchObject({ kind: 'handoff', from: 'core', to: 'tests', count: 1 })
  })
})

describe('connectError', () => {
  it('allows a free member and refuses a self edge, a second parent, a cycle and a stranger', () => {
    expect(connectError(crew, 'lead', 'docs')).toBe('')
    expect(connectError(crew, 'core', 'core')).toBe('core cannot start after itself')
    expect(connectError(crew, 'core', 'tests')).toBe('tests already starts after cli: delete that edge first')
    expect(connectError(crew, 'tests', 'lead')).toBe('that would go round in a cycle: lead after tests after cli after lead')
    expect(connectError(crew, 'nobody', 'lead')).toBe('not a member of this crew')
    // Re-drawing the edge that is there is allowed.
    expect(connectError(crew, 'lead', 'core')).toBe('')
  })
})

describe('connectionAllowed', () => {
  const task = (id: string, needs?: string[]): RunTask => ({ id, member: 'lead', kind: 'work', state: 'succeeded', attempts: 1, needs })
  const tasks = taskLayout([task('build'), task('docs', ['build'])])

  it("keeps every edge the graph draws, a task's need and a handoff back up the start rules among them", () => {
    // The start rules alone refuse both: a task is no member, and tests hands back to lead, which it waits for.
    expect(connectError(crew, 'build', 'docs')).toBe('not a member of this crew')
    expect(connectionAllowed(crew, tasks.edges, false, 'build', 'docs')).toBe(true)
    const run = crewGraph(crew, { handoffs: [{ from: 'tests', to: 'lead', at: '2026-10-30T10:00:00Z' }] })
    expect(connectError(crew, 'tests', 'lead')).not.toBe('')
    expect(connectionAllowed(crew, run.edges, false, 'tests', 'lead')).toBe(true)
  })

  it('takes a new connection in the editor alone, under the start rules', () => {
    const drawn = crewGraph(crew).edges
    expect(connectionAllowed(crew, drawn, true, 'lead', 'docs')).toBe(true)
    expect(connectionAllowed(crew, drawn, true, 'core', 'tests')).toBe(false)
    expect(connectionAllowed(crew, drawn, true, 'tests', 'lead')).toBe(false)
    expect(connectionAllowed(crew, drawn, true, 'core', 'core')).toBe(false)
    expect(connectionAllowed(crew, drawn, false, 'lead', 'docs')).toBe(false)
  })

  it("keeps a run's needs edge in Vue Flow's store when the graph is drawn again with the check in place", () => {
    // Vue Flow runs its connection check on every edge setEdges is handed and drops one it refuses. The run page draws its graph once
    // before the VueFlow component sets the check, then again at each clock tick and update of the run, with the check set.
    const flow = useVueFlow('crew-graph-connection-check')
    try {
      const nodes = tasks.nodes.map((n) => ({ id: n.id, position: { x: n.x, y: n.y } }))
      const edges = tasks.edges.map((e) => ({ id: e.id, source: e.from, target: e.to }))
      flow.setNodes(nodes)
      flow.setEdges(edges)
      flow.isValidConnection.value = (c) => connectionAllowed(crew, tasks.edges, false, c.source, c.target)
      flow.setNodes(nodes)
      flow.setEdges(edges)
      expect(flow.edges.value.map((e) => e.id)).toEqual(['needs:build-docs'])
    } finally {
      flow.$destroy()
    }
  })
})

describe('withStart and listRows', () => {
  it('sets a member start and lists parents before children with their depth', () => {
    const next = withStart(crew, 'docs', { when: 'after', member: 'tests' })
    expect(next.find((x) => x.name === 'docs')!.start).toEqual({ when: 'after', member: 'tests' })
    const rows = listRows(layout(next))
    expect(rows.map((n) => `${n.id}@${n.depth}`)).toEqual(['lead@0', 'core@1', 'cli@1', 'tests@2', 'docs@3'])
  })
})

describe('handoffPath', () => {
  it('arcs above the line left to right, and to its right top to bottom, with the badge half way up the arc', () => {
    expect(handoffPath(0, 100, 200, 100, 'LR')).toEqual([`M0,100 Q100,${100 - HANDOFF_ARC} 200,100`, 100, 100 - HANDOFF_ARC / 2])
    expect(handoffPath(50, 0, 50, 200, 'TB')).toEqual([`M50,0 Q${50 + HANDOFF_ARC},100 50,200`, 50 + HANDOFF_ARC / 2, 100])
  })
})
