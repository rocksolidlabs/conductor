import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import type { RunTask } from '~/composables/useSessions'
import type { GraphDirection } from './crewGraph'
import { NODE_W } from './crewGraph'
import { LINE_LABEL_CHARS, LINE_GEOMETRY, lineLayout, placeLabels, type LineLayout, type LinePoint, type LineSize, type LineStation } from './lineDiagram'
import { taskDepths, taskLayout, type DepthNode } from './taskGraph'

// lineLayout is a pure function of ids and needs: its cases are written as
// { id: [depth, lane] } with lines, connectors as from>to:kind and junctions,
// so that no pixel is compared but where the geometry is the point.

const n = (id: string, ...needs: string[]): DepthNode => (needs.length ? { id, needs } : { id })

function shape(l: LineLayout) {
  return {
    at: Object.fromEntries(l.stations.map((s) => [s.id, [s.depth, s.lane]])),
    lines: l.lines.map((x) => x.stations.join(' ')),
    connectors: l.connectors.map((c) => `${c.from}>${c.to}:${c.kind}`),
    junctions: Object.fromEntries(l.stations.flatMap((s) => (s.junction ? [[s.id, s.junction]] : []))),
  }
}

const ledger = [n('schema'), n('naming', 'schema'), n('backfill', 'schema'), n('soak'), n('migrate', 'naming'), n('release-notes', 'naming')]
const diamond = [n('plan'), n('core', 'plan'), n('cli', 'plan'), n('tests', 'core', 'cli'), n('review', 'tests')]
const forty = JSON.parse(readFileSync(new URL('./testdata/line-forty.json', import.meta.url), 'utf8')) as DepthNode[]
const chain = (count: number) => Array.from({ length: count }, (_, i) => (i ? n(`t${i}`, `t${i - 1}`) : n('t0')))

/** The path as drawn: each turn rounded by a quarter circle of `corner`, sampled finely. */
function drawn(points: LinePoint[], corner: number): LinePoint[] {
  const out: LinePoint[] = []
  points.forEach((p, i) => {
    const a = points[i - 1]
    const b = points[i + 1]
    if (!a || !b || corner === 0) return void out.push(p)
    const unit = (q: LinePoint) => {
      const len = Math.hypot(q.x - p.x, q.y - p.y)
      return { x: (q.x - p.x) / len, y: (q.y - p.y) / len }
    }
    const [ua, ub] = [unit(a), unit(b)]
    for (let k = 0; k <= 8; k++) {
      // A quadratic curve through the corner's two tangent points, inside the corner's square (as an arc is).
      const t = k / 8
      const s0 = { x: p.x + ua.x * corner, y: p.y + ua.y * corner }
      const s2 = { x: p.x + ub.x * corner, y: p.y + ub.y * corner }
      out.push({ x: (1 - t) ** 2 * s0.x + 2 * (1 - t) * t * p.x + t * t * s2.x, y: (1 - t) ** 2 * s0.y + 2 * (1 - t) * t * p.y + t * t * s2.y })
    }
  })
  return out
}

/** The distance from a point to a path's segments. */
function distance(path: LinePoint[], q: LinePoint): number {
  let best = Infinity
  for (let i = 1; i < path.length; i++) {
    const a = path[i - 1] as LinePoint
    const b = path[i] as LinePoint
    const [dx, dy] = [b.x - a.x, b.y - a.y]
    const len = dx * dx + dy * dy
    const t = len === 0 ? 0 : Math.max(0, Math.min(1, ((q.x - a.x) * dx + (q.y - a.y) * dy) / len))
    best = Math.min(best, Math.hypot(a.x + t * dx - q.x, a.y + t * dy - q.y))
  }
  return best
}

/** How far a connector keeps from a station's edge. */
const clearance = (l: LineLayout, c: { points: LinePoint[]; corner: number }, id: string, size: LineSize = 'full', dir: GraphDirection = 'LR') => {
  const s = l.stations.find((x) => x.id === id)
  if (!s) throw new Error(`no station ${id}`)
  return distance(drawn(c.points, c.corner), s) - LINE_GEOMETRY[size][dir].radius
}

describe('lineLayout: a table of shapes', () => {
  it('a chain of four is one line on lane 0, depths 0 to 3, with no connector', () => {
    expect(shape(lineLayout([n('a'), n('b', 'a'), n('c', 'b'), n('d', 'c')]))).toEqual({ at: { a: [0, 0], b: [1, 0], c: [2, 0], d: [3, 0] }, lines: ['a b c d'], connectors: [], junctions: {} })
  })
  it('a split: the first child keeps the line, the second branches onto lane 1', () => {
    expect(shape(lineLayout([n('a'), n('b', 'a'), n('c', 'a')]))).toEqual({ at: { a: [0, 0], b: [1, 0], c: [1, 1] }, lines: ['a b', 'c'], connectors: ['a>c:branch'], junctions: { a: 'split' } })
  })
  it("the example crew's diamond", () => {
    const l = lineLayout(diamond)
    expect(shape(l)).toEqual({
      at: { plan: [0, 0], core: [1, 0], cli: [1, 1], tests: [2, 0], review: [3, 0] },
      lines: ['plan core tests review', 'cli'],
      connectors: ['plan>cli:branch', 'cli>tests:merge'],
      junctions: { plan: 'split', tests: 'merge' },
    })
    expect(l.lines[1]).toEqual({ id: 'cli', stations: ['cli'], lane: 1, from: 'plan' })
  })
  it('the Ledger shape: release-notes takes lane 1 again once soak has ended, backfill lane 2', () => {
    expect(shape(lineLayout(ledger))).toEqual({
      at: { schema: [0, 0], soak: [0, 1], naming: [1, 0], backfill: [1, 2], migrate: [2, 0], 'release-notes': [2, 1] },
      lines: ['schema naming migrate', 'soak', 'backfill', 'release-notes'],
      connectors: ['schema>backfill:branch', 'naming>release-notes:branch'],
      junctions: { schema: 'split', naming: 'split' },
    })
  })
  it('two independent chains on two lanes', () => {
    expect(shape(lineLayout([n('a'), n('b', 'a'), n('c'), n('d', 'c')]))).toEqual({ at: { a: [0, 0], c: [0, 1], b: [1, 0], d: [1, 1] }, lines: ['a b', 'c d'], connectors: [], junctions: {} })
  })
  it('lane reuse: two short branches at depths that do not meet share lane 1', () => {
    const l = shape(lineLayout([n('r'), n('a', 'r'), n('b', 'a'), n('c', 'b'), n('d', 'c'), n('x', 'r'), n('y', 'c')]))
    expect(l.at).toMatchObject({ x: [1, 1], y: [4, 1] })
    expect(l.connectors).toEqual(['r>x:branch', 'c>y:branch'])
  })
  it('nearest lane: a branch from lane 2 with lanes 1 and 3 free takes 3, the one below', () => {
    const l = shape(lineLayout([n('a'), n('b'), n('c'), n('d'), n('a1', 'a'), n('a2', 'a1'), n('c1', 'c'), n('y', 'c1'), n('x', 'c1')]))
    expect(l.at).toMatchObject({ a: [0, 0], b: [0, 1], c: [0, 2], d: [0, 3], c1: [1, 2], x: [2, 3] })
    expect(l.connectors).toEqual(['c1>x:branch'])
  })
  it('a long need: it continues the line free at depth 0 and takes a merge from the one at depth 2', () => {
    expect(shape(lineLayout([n('p'), n('q0'), n('q1', 'q0'), n('q2', 'q1'), n('q3', 'q2'), n('m', 'p', 'q2')]))).toEqual({
      at: { p: [0, 0], q0: [0, 1], q1: [1, 1], q2: [2, 1], q3: [3, 1], m: [3, 0] },
      lines: ['p m', 'q0 q1 q2 q3'],
      connectors: ['q2>m:merge'],
      junctions: { q2: 'split', m: 'merge' },
    })
  })
  it('a need the run has no task for, and a two-task cycle: roots, nothing between them, and their warnings', () => {
    const l = lineLayout([n('a', 'zzz'), n('b', 'c'), n('c', 'b')])
    expect(shape(l)).toEqual({ at: { a: [0, 0], b: [0, 1], c: [0, 2] }, lines: ['a', 'b', 'c'], connectors: [], junctions: {} })
    expect([...l.warnings]).toEqual([
      ['a', 'needs zzz, which the run has no task for'],
      ['b', 'needs itself, through the tasks it needs'],
      ['c', 'needs b, which does not come before it (a cycle): not drawn'],
    ])
    expect(l.stations.map((s) => s.warning)).toEqual([...l.warnings.values()])
  })
  it('TB: every station has x and y swapped against LR, each under its own gaps, labels to the right', () => {
    const [lr, tb] = [lineLayout(ledger), lineLayout(ledger, { direction: 'TB' })]
    const [L, T] = [LINE_GEOMETRY.full.LR, LINE_GEOMETRY.full.TB]
    expect(shape(tb)).toEqual(shape(lr))
    for (const s of tb.stations) {
      const o = lr.stations.find((x) => x.id === s.id)
      expect(o && [(s.x - T.margin) / T.laneGap, (s.y - T.margin) / T.step]).toEqual(o && [(o.y - L.margin) / L.laneGap, (o.x - L.margin) / L.step])
      expect(s.label.side).toBe('right')
    }
  })
  it('mini: the same lines, no labels, and a five-task crew of depth 3 inside 150 px', () => {
    const l = lineLayout(diamond, { size: 'mini' })
    expect(shape(l)).toEqual(shape(lineLayout(diamond)))
    expect(l.stations.every((s) => s.label.side === 'none' && s.label.text === '' && s.title === s.id)).toBe(true)
    expect(l.width).toBeLessThanOrEqual(150)
    expect(l.height).toBeLessThanOrEqual(150)
  })
  it('a fan of fourteen children: thirteen branches run down the gutter at x 60, the last one clear of the child above its own', () => {
    const l = lineLayout([n('root'), ...Array.from({ length: 14 }, (_, i) => n(`c${i}`, 'root'))])
    expect(shape(l).at).toEqual(Object.fromEntries([['root', [0, 0]], ...Array.from({ length: 14 }, (_, i) => [`c${i}`, [1, i]])]))
    expect(l.connectors).toHaveLength(13)
    for (const c of l.connectors) {
      const lane = Number(c.to.slice(1))
      expect(c).toMatchObject({ from: 'root', kind: 'branch', corner: 8 })
      expect(c.points).toEqual([
        { x: 24, y: 24 },
        { x: 60, y: 24 },
        { x: 60, y: 24 + lane * 48 },
        { x: 96, y: 24 + lane * 48 },
      ])
    }
    const last = l.connectors[12]
    expect(l.stations.find((s) => s.id === 'c13')).toMatchObject({ x: 96, y: 648 })
    expect(l.stations.find((s) => s.id === 'c12')).toMatchObject({ x: 96, y: 600 })
    expect(last && clearance(l, last, 'c12')).toBeGreaterThanOrEqual(8)
  })
  it('a merge past a line that goes on runs on the track between lanes 0 and 1, never along lane 0 over t', () => {
    const l = lineLayout([n('r'), n('s', 'r'), n('t', 's'), n('r2'), n('a', 'r2'), n('b', 'a'), n('m', 'b', 's')])
    expect(shape(l)).toMatchObject({ lines: ['r s t', 'r2 a b m'], connectors: ['s>m:merge'] })
    const merge = l.connectors[0]
    expect(merge?.points).toEqual([
      { x: 96, y: 24 },
      { x: 132, y: 24 },
      { x: 132, y: 48 },
      { x: 204, y: 48 },
      { x: 204, y: 72 },
      { x: 240, y: 72 },
    ])
    expect(merge && clearance(l, merge, 't')).toBeGreaterThanOrEqual(8)
  })
  it('never moves a task that changes state: whole tasks in any state give the layout their ids and needs give', () => {
    const states: RunTask['state'][] = ['succeeded', 'failed', 'running', 'blocked', 'ready', 'verifying']
    const whole = (k: number): RunTask[] => ledger.map((t, i) => ({ ...t, member: 'lead', kind: 'work', state: states[(i + k) % states.length] ?? 'ready', attempts: k }))
    expect(lineLayout(whole(0))).toEqual(lineLayout(ledger))
    expect(lineLayout(whole(3), { direction: 'TB' })).toEqual(lineLayout(ledger, { direction: 'TB' }))
  })
  it("placeLabels: a label that meets one takes the other side, then is cut to 8, then is hidden; another station's circle counts too", () => {
    // The full geometry never packs stations this close (see placeLabels), so they are placed by hand.
    const at = (id: string, x: number, y: number): LineStation => ({ id, depth: 0, lane: 0, x, y, line: id, label: { text: '', side: 'none' }, title: id })
    const labels = placeLabels([at('a'.repeat(20), 60, 40), at('b'.repeat(20), 100, 40), at('c'.repeat(20), 140, 40), at('d'.repeat(20), 120, 40), at('e', 300, 40), at('f', 300, 22)], 'LR', 6)
    expect(labels.map((x) => [x.side, x.text])).toEqual([
      ['above', `${'a'.repeat(15)}…`],
      ['below', `${'b'.repeat(15)}…`],
      ['above', 'ccccccc…'],
      ['none', ''],
      ['below', 'e'],
      ['above', 'f'],
    ])
    expect(labels[0]?.box).toEqual({ x: 60 - 52.8, y: 18, w: 105.6, h: 12 })
  })
  it('prints the Ledger at full and at mini, to be read by hand', () => {
    for (const size of ['full', 'mini'] as const) {
      const l = lineLayout(ledger, { size })
      const where = l.stations.map((s) => `${s.id}@${s.x},${s.y}${s.label.side === 'none' ? '' : ` ${s.label.side} "${s.label.text}"`}`)
      const paths = l.connectors.map((c) => `${c.from}>${c.to}:${c.kind} ${c.points.map((p) => `${p.x},${p.y}`).join(' ')}`)
      console.log(`Ledger at ${size}, ${l.width} x ${l.height}:\n  ${where.join('\n  ')}\n  lines: ${l.lines.map((x) => `[${x.stations.join(' ')}] lane ${x.lane}`).join(', ')}\n  ${paths.join('\n  ')}`)
      expect(paths).toEqual(
        size === 'full'
          ? ['schema>backfill:branch 24,24 60,24 60,120 96,120', 'naming>release-notes:branch 96,24 132,24 132,72 168,72']
          : ['schema>backfill:branch 4,4 12,4 12,24 20,24', 'naming>release-notes:branch 20,4 28,4 28,14 36,14'],
      )
    }
  })
})

describe("taskDepths' smallest input", () => {
  it('nodes of an id, or of an id and its needs, give the depths and warnings whole tasks give', () => {
    const small: DepthNode[] = [{ id: 'build' }, { id: 'test', needs: ['build'] }, { id: 'deploy', needs: ['test', 'zzz'] }, { id: 'b', needs: ['c'] }, { id: 'c', needs: ['b'] }]
    const whole: RunTask[] = small.map((t) => ({ ...t, member: 'lead', kind: 'work', state: 'ready', attempts: 0 }))
    expect(taskDepths(small)).toEqual(taskDepths(whole))
    expect([...taskDepths(small).depth]).toEqual([
      ['build', 0],
      ['test', 1],
      ['deploy', 0],
      ['c', 0],
      ['b', 0],
    ])
  })
})

describe('lineLayout: bounds', () => {
  it('the forty-task shape fits 960 x 400 at full: depth 8 on six lanes (today it is printed beside it)', () => {
    const l = lineLayout(forty)
    expect(forty).toHaveLength(40)
    expect(Math.max(...l.stations.map((s) => s.depth))).toBe(8)
    expect(new Set(l.stations.map((s) => s.lane)).size).toBe(6)
    expect(l.width).toBeLessThanOrEqual(960)
    expect(l.height).toBeLessThanOrEqual(400)
    const today = Math.max(...taskLayout(forty.map((t) => ({ ...t, member: 'lead', kind: 'work' as const, state: 'ready' as const, attempts: 0 }))).nodes.map((x) => x.x)) + NODE_W
    console.log(`forty tasks: lineLayout ${l.width} x ${l.height} px; taskLayout today ${today} px wide`)
  })
  it('a chain of 128, the crew limit, is drawn with one lane', () => {
    const l = lineLayout(chain(128))
    expect(l.lines).toHaveLength(1)
    expect(l.stations.every((s) => s.lane === 0)).toBe(true)
    expect(l.width).toBeGreaterThan(9100)
    expect(l.width).toBeLessThan(9300)
  })
  it('nothing given is an empty drawing of its margins', () => {
    expect(lineLayout([])).toEqual({ stations: [], lines: [], connectors: [], width: 48, height: 48, warnings: new Map() })
  })
  it('past the bounds: a second task of one id is drawn once, a long id is cut, and past 128 tasks nothing is cut', () => {
    const twice = lineLayout([n('a'), n('b', 'a'), n('a', 'b')])
    expect(shape(twice)).toEqual({ at: { a: [0, 0], b: [1, 0] }, lines: ['a b'], connectors: [], junctions: {} })
    expect(twice.warnings.get('a')).toBe('listed twice in the run: drawn once')
    const long = 'a'.repeat(30) + 'z'.repeat(30)
    const s = lineLayout([n(long)]).stations[0]
    expect(s && [s.title, s.label.text]).toEqual([long, `${'a'.repeat(15)}…`])
    const big = lineLayout([n('root'), ...Array.from({ length: 299 }, (_, i) => n(`c${i}`, 'root'))])
    expect([big.stations.length, big.connectors.length, Math.max(...big.stations.map((x) => x.lane))]).toEqual([300, 298, 298])
  })
})

// Properties over 200 graphs of 1 to 128 nodes, made by a seeded generator of
// this file's own (mulberry32): fans of up to 127 children, chains, merges
// from two and more depths back, a run's order that is not the needs' order,
// and now and then a need the run has no task for or a two-task cycle. A
// failing property prints the seed, the configuration and the graph.

function rng(seed: number): () => number {
  let a = seed >>> 0
  return () => {
    a = (a + 0x6d2b79f5) >>> 0
    let t = a
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

interface Case {
  seed: number
  nodes: DepthNode[]
  /** Whether the generator closed a cycle (then depths follow the run's order). */
  cycle: boolean
}

function graph(seed: number): Case {
  const r = rng(seed)
  const int = (lo: number, hi: number) => lo + Math.floor(r() * (hi - lo + 1))
  const count = seed === 0 ? 1 : seed === 1 || seed === 2 ? 128 : int(1, 128)
  const kind = seed === 2 ? 'fan' : (['mixed', 'fan', 'chains', 'dense'] as const)[seed % 4]
  const word = () => Array.from({ length: int(0, 36) }, () => 'abcdefghijklmnopqrstuvwxyz0123456789'[int(0, 35)]).join('')
  const ids = Array.from({ length: count }, (_, i) => `${word()}-${i}`.replace(/^-/, 'n-'))
  const needs: string[][] = ids.map(() => [])
  for (let i = 1; i < count; i++) {
    const pick = () => ids[r() < 0.5 ? int(Math.max(0, i - 3), i - 1) : int(0, i - 1)] as string
    const k = kind === 'fan' ? 1 : kind === 'dense' ? int(0, 3) : kind === 'chains' ? (r() < 0.8 ? 1 : int(0, 2)) : [0, 1, 1, 1, 2, 2, 3][int(0, 6)] ?? 1
    for (let j = 0; j < k; j++) (needs[i] as string[]).push(kind === 'fan' && j === 0 ? (ids[0] as string) : kind === 'chains' && j === 0 ? (ids[i - 1] as string) : pick())
  }
  if (r() < 0.1 && count > 1) (needs[int(0, count - 1)] as string[]).push('gone')
  let cycle = false
  const back = needs.findIndex((ns, i) => i > 0 && ns.length > 0)
  if (r() < 0.1 && back > 0) {
    const target = ids.indexOf((needs[back] as string[])[0] as string)
    ;(needs[target] as string[]).push(ids[back] as string)
    cycle = true
  }
  const nodes = ids.map((id, i) => n(id, ...(needs[i] as string[])))
  if (r() < 0.5) {
    for (let i = nodes.length - 1; i > 0; i--) {
      const j = int(0, i)
      ;[nodes[i], nodes[j]] = [nodes[j] as DepthNode, nodes[i] as DepthNode]
    }
  }
  return { seed, nodes, cycle }
}

const CASES = Array.from({ length: 200 }, (_, seed) => graph(seed))
const CONFIGS: Array<{ size: LineSize; direction: GraphDirection }> = [
  { size: 'full', direction: 'LR' },
  { size: 'full', direction: 'TB' },
  { size: 'mini', direction: 'LR' },
  { size: 'mini', direction: 'TB' },
]
/** The clearance every line and connector keeps from a station's edge: 8 px at full, in both directions, and 2 px at mini. */
const CLEAR: Record<LineSize, number> = { full: 8, mini: 2 }

/** Runs one property over every case and configuration and fails with the first few that break it, each with its graph. */
const LAID = new Map<string, LineLayout>()
function laid(c: Case, size: LineSize, direction: GraphDirection): LineLayout {
  const key = `${c.seed}/${size}/${direction}`
  const l = LAID.get(key) ?? lineLayout(c.nodes, { size, direction })
  LAID.set(key, l)
  return l
}
function holds(check: (l: LineLayout, c: Case, size: LineSize, direction: GraphDirection) => string | undefined) {
  const failures: string[] = []
  for (const c of CASES) {
    for (const { size, direction } of CONFIGS) {
      const why = check(laid(c, size, direction), c, size, direction)
      if (why && failures.length < 3) failures.push(`seed ${c.seed} at ${size} ${direction}: ${why}\n  graph: ${JSON.stringify(c.nodes)}`)
    }
  }
  expect(failures, failures.join('\n')).toEqual([])
}

/** The needs a layout draws, as from>to, each once per line step or connector. */
function drawnNeeds(l: LineLayout): string[] {
  const steps = l.lines.flatMap((x) => x.stations.slice(1).map((id, i) => `${x.stations[i]}>${id}`))
  return [...steps, ...l.connectors.map((c) => `${c.from}>${c.to}`)]
}

/** The lines alive at a depth: those whose span (from their `from` station, else their first, to their last) holds it. */
function spans(l: LineLayout): Array<[number, number, number]> {
  const depthOf = new Map(l.stations.map((s) => [s.id, s.depth]))
  return l.lines.map((x) => [depthOf.get(x.from ?? x.stations[0] ?? '') ?? 0, depthOf.get(x.stations[x.stations.length - 1] ?? '') ?? 0, x.lane])
}

// Each property's work is bounded by the graphs' sizes, not by a clock: the timeout bounds a hang, far above a loaded machine's run.
describe('lineLayout: properties over 200 seeded graphs', { timeout: 120_000 }, () => {
  it('every drawn need goes forward in depth', () =>
    holds((l) => {
      const depthOf = new Map(l.stations.map((s) => [s.id, s.depth]))
      return drawnNeeds(l).find((e) => {
        const [a, b] = e.split('>') as [string, string]
        return (depthOf.get(a) ?? 0) >= (depthOf.get(b) ?? 0)
      })
    }))
  it('no two stations share a depth and a lane', () =>
    holds((l) => {
      const seen = new Set<string>()
      for (const s of l.stations) {
        const key = `${s.depth}/${s.lane}`
        if (seen.has(key)) return `two stations at depth ${s.depth}, lane ${s.lane}`
        seen.add(key)
      }
    }))
  it('every need is drawn exactly once, as a step along a line or one connector, or its task carries a warning', () =>
    holds((l, c) => {
      const ids = new Set(c.nodes.map((t) => t.id))
      const { depth, warnings } = taskDepths(c.nodes)
      const want = c.nodes.flatMap((t) =>
        warnings.has(t.id) ? [] : [...new Set(t.needs ?? [])].filter((x) => ids.has(x) && (depth.get(x) ?? 0) < (depth.get(t.id) ?? 0)).map((x) => `${x}>${t.id}`),
      )
      const got = drawnNeeds(l)
      if (got.length !== new Set(got).size) return 'a need drawn twice'
      if ([...want].sort().join() !== [...got].sort().join()) return `drew ${got.length} needs, not the ${want.length} expected`
      const unseen = c.nodes.find((t) => (t.needs ?? []).some((x) => !got.includes(`${x}>${t.id}`)) && !l.warnings.has(t.id))
      if (unseen) return `${unseen.id} has a need not drawn and no warning`
    }))
  it('each station is on one line, whose stations share its lane and rise in depth', () =>
    holds((l) => {
      const byId = new Map(l.stations.map((s) => [s.id, s]))
      const on = l.lines.flatMap((x) => x.stations)
      if (on.length !== l.stations.length || new Set(on).size !== on.length) return 'a station on no line or on two'
      for (const x of l.lines) {
        const ss = x.stations.map((id) => byId.get(id))
        if (ss.some((s, i) => !s || s.lane !== x.lane || s.line !== x.id || (i > 0 && s.depth <= (ss[i - 1]?.depth ?? 0)))) return `line ${x.id} is not one lane in rising depth`
      }
    }))
  it('no two lines in a lane have spans that meet', () =>
    holds((l) => {
      const ss = spans(l)
      for (const [i, a] of ss.entries()) for (const b of ss.slice(i + 1)) if (a[2] === b[2] && a[0] <= b[1] && b[0] <= a[1]) return `two lines in lane ${a[2]} meet`
    }))
  it('the lanes number the most lines alive at one depth', () =>
    holds((l) => {
      const ss = spans(l)
      const deepest = Math.max(0, ...ss.map((s) => s[1]))
      let most = 0
      for (let dd = 0; dd <= deepest; dd++) most = Math.max(most, ss.filter((s) => s[0] <= dd && dd <= s[1]).length)
      const lanes = new Set(l.lines.map((x) => x.lane)).size
      if (lanes !== most || Math.max(-1, ...l.lines.map((x) => x.lane)) !== lanes - 1) return `${lanes} lanes for at most ${most} lines alive at one depth`
    }))
  it('no line passes within the clearance of a station that is not its own', () =>
    holds((l, _c, size, dir) => {
      const r = LINE_GEOMETRY[size][dir].radius
      const at = new Map(l.stations.map((s) => [s.id, s]))
      for (const x of l.lines) {
        const path = x.stations.map((id) => at.get(id) as LinePoint)
        for (const s of l.stations) if (s.line !== x.id && path.length > 1 && distance(path, s) - r < CLEAR[size]) return `line ${x.id} passes ${s.id}`
      }
    }))
  it('every connector is orthogonal, ends at its stations, turns only in a gutter, and keeps the clearance from every other station', () =>
    holds((l, _c, size, dir) => {
      const g = LINE_GEOMETRY[size][dir]
      const at = new Map(l.stations.map((s) => [s.id, s]))
      const gutter = (p: LinePoint) => {
        const k = ((dir === 'LR' ? p.x : p.y) - g.margin) / g.step - 0.5
        return Math.abs(k - Math.round(k)) < 1e-9
      }
      for (const c of l.connectors) {
        const [a, b] = [at.get(c.from), at.get(c.to)]
        const [first, last] = [c.points[0], c.points[c.points.length - 1]]
        if (!a || !b || first?.x !== a.x || first.y !== a.y || last?.x !== b.x || last.y !== b.y) return `${c.from}>${c.to} does not end at its stations`
        if (c.points.slice(1).some((p, i) => p.x !== c.points[i]?.x && p.y !== c.points[i]?.y)) return `${c.from}>${c.to} is not orthogonal`
        if (c.points.slice(1, -1).some((p) => !gutter(p))) return `${c.from}>${c.to} turns outside a gutter`
        if (c.corner !== g.corner) return `${c.from}>${c.to} has corner ${c.corner}`
        const path = drawn(c.points, c.corner)
        // Only a station inside the path's box, grown by the clearance and the radius, can come near it.
        const grow = CLEAR[size] + g.radius
        const [x0, x1] = [Math.min(...path.map((p) => p.x)) - grow, Math.max(...path.map((p) => p.x)) + grow]
        const [y0, y1] = [Math.min(...path.map((p) => p.y)) - grow, Math.max(...path.map((p) => p.y)) + grow]
        for (const s of l.stations) {
          if (s.id === c.from || s.id === c.to || s.x < x0 || s.x > x1 || s.y < y0 || s.y > y1) continue
          const gap = distance(path, s) - g.radius
          if (gap < CLEAR[size]) return `${c.from}>${c.to} passes ${gap.toFixed(1)} px from ${s.id}'s edge`
        }
      }
    }))
  it('no two shown labels meet, nor a label and a station not its own; each is its id cut, the whole id in the title', () =>
    holds((l, _c, size, dir) => {
      const r = LINE_GEOMETRY[size][dir].radius
      const shown = l.stations.filter((s) => s.label.side !== 'none')
      if (size === 'mini' && shown.length) return 'a label at mini'
      for (const s of l.stations) {
        if (s.title !== s.id) return `${s.id}'s title`
        if (s.label.side === 'none' ? s.label.text !== '' : !(s.label.text === s.id || (s.label.text.endsWith('…') && s.id.startsWith(s.label.text.slice(0, -1)) && s.label.text.length <= LINE_LABEL_CHARS))) return `${s.id}'s label "${s.label.text}"`
      }
      for (const [i, s] of shown.entries()) {
        const a = s.label.box
        if (!a) return `${s.id} shown with no box`
        for (const o of shown.slice(i + 1)) {
          const b = o.label.box
          if (b && a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h) return `the labels of ${s.id} and ${o.id} meet`
        }
        for (const o of l.stations) {
          if (o === s) continue
          const [cx, cy] = [Math.min(Math.max(o.x, a.x), a.x + a.w), Math.min(Math.max(o.y, a.y), a.y + a.h)]
          if (Math.hypot(o.x - cx, o.y - cy) < r) return `${s.id}'s label meets ${o.id}`
        }
      }
    }))
  it('at full, every label is shown on the side its line gives it (alternating, above first; TB to the right), cut only at 16 characters', () =>
    holds((l, _c, size, dir) => {
      if (size === 'mini') return
      const along = new Map<string, number>()
      for (const s of l.stations) {
        const k = along.get(s.line) ?? 0
        along.set(s.line, k + 1)
        const side = dir === 'TB' ? 'right' : k % 2 === 0 ? 'above' : 'below'
        const text = s.id.length > 16 ? `${s.id.slice(0, 15)}…` : s.id
        if (s.label.side !== side || s.label.text !== text) return `${s.id}'s label is ${s.label.side} "${s.label.text}", not ${side} "${text}"`
      }
    }))
  it('everything drawn is inside the bounds, on the 4 px grid at full', () =>
    holds((l, _c, size, dir) => {
      const r = LINE_GEOMETRY[size][dir].radius
      const inside = (x: number, y: number) => x >= 0 && y >= 0 && x <= l.width && y <= l.height
      if (l.stations.some((s) => !inside(s.x - r, s.y - r) || !inside(s.x + r, s.y + r))) return 'a station outside'
      if (l.stations.some((s) => s.label.box && (!inside(s.label.box.x, s.label.box.y) || !inside(s.label.box.x + s.label.box.w, s.label.box.y + s.label.box.h)))) return 'a label outside'
      if (l.connectors.some((c) => c.points.some((p) => !inside(p.x, p.y)))) return 'a connector outside'
      const grid = [l.width, l.height, ...l.stations.flatMap((s) => [s.x, s.y]), ...l.connectors.flatMap((c) => c.points.flatMap((p) => [p.x, p.y]))]
      if (size === 'full' && grid.some((v) => v % 4 !== 0)) return 'a coordinate off the 4 px grid'
    }))
  it('the same input twice gives the same output; in another order, the same depths and the same needs drawn', () =>
    holds((l, c, size, direction) => {
      if (JSON.stringify([...l.warnings, l]) !== JSON.stringify([...lineLayout(structuredClone(c.nodes), { size, direction }).warnings, lineLayout(c.nodes, { size, direction })])) return 'two runs differ'
      if (c.cycle) return
      const turned = lineLayout([...c.nodes].reverse(), { size, direction })
      const depths = (x: LineLayout) => JSON.stringify(x.stations.map((s) => [s.id, s.depth]).sort())
      if (depths(turned) !== depths(l)) return 'depths follow the order'
      if (drawnNeeds(turned).sort().join() !== drawnNeeds(l).sort().join()) return 'the needs drawn follow the order'
    }))
  it('the generator makes what the properties need: fans of 127, merges from two and more depths back, both sizes of graph', () => {
    const layouts = CASES.map((c) => laid(c, 'full', 'LR'))
    expect(Math.max(...layouts.map((l) => Math.max(0, ...l.stations.map((s) => s.lane))))).toBe(126)
    expect(CASES.some((c) => c.nodes.length === 1)).toBe(true)
    const farMerges = layouts.filter((l) => l.connectors.some((c) => c.kind === 'merge' && (l.stations.find((s) => s.id === c.to)?.depth ?? 0) - (l.stations.find((s) => s.id === c.from)?.depth ?? 0) >= 2))
    expect(farMerges.length).toBeGreaterThan(20)
    expect(CASES.filter((c) => c.cycle).length).toBeGreaterThan(0)
  })
})
