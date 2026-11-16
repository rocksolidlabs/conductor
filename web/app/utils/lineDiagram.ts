import type { GraphDirection } from './crewGraph'
import { taskDepths, type DepthNode } from './taskGraph'

/**
 * A run's tasks laid out as lines and stations (UX1 PR-32, P15): a task is a
 * station, a chain of tasks is a line, a task that starts from a result
 * another line goes on from is a branch, and a task that needs more than one
 * line's result takes a merge from each other one. A pure function of the
 * tasks' ids and needs, in the run's order: it never reads a state, so a task
 * that changes state is recoloured where it is and never moved.
 *
 * Steps: 1 depth (`taskDepths`); 2 lines, visiting the tasks by depth, then in
 * the run's order; 3 lanes, by packing each line's span into the free lane
 * nearest its parent's, so the lanes number the most lines alive at one depth;
 * 4 geometry, a connector kept to the gutters between depths and the tracks
 * between lanes, where no station ever stands; 5 labels, alternating above
 * and below a line, flipped, cut or hidden so that none meets another label,
 * a station not its own, a line or a connector.
 */

export type LineSize = 'full' | 'mini'

export interface LineOptions {
  /** `LR`: depth runs left to right and lanes down; `TB`: depth runs down and lanes across. `LR` by default. */
  direction?: GraphDirection
  /** `full` (the Graph view's) or `mini` (a table cell's: smaller, no labels). `full` by default. */
  size?: LineSize
}

/** The fixed geometry of one size and direction, in px. */
export interface LineGeometry {
  /** Between two depths, along the drawing. */
  step: number
  /** Between two lanes, across it. */
  laneGap: number
  margin: number
  /** A station's circle. */
  radius: number
  /** The radius a connector's turns are rounded at (0: square). */
  corner: number
  labels: boolean
}

export const LINE_GEOMETRY: Record<LineSize, Record<GraphDirection, LineGeometry>> = {
  full: {
    LR: { step: 72, laneGap: 48, margin: 24, radius: 6, corner: 8, labels: true },
    TB: { step: 48, laneGap: 152, margin: 24, radius: 6, corner: 8, labels: true },
  },
  // At full every coordinate is on the 4 px grid; mini's 10 px between lanes puts its lanes on a 2 px one (it is scaled to its cell).
  mini: {
    LR: { step: 16, laneGap: 10, margin: 4, radius: 3, corner: 0, labels: false },
    TB: { step: 16, laneGap: 10, margin: 4, radius: 3, corner: 0, labels: false },
  },
}

/** A label is its id cut to this many characters, "…" among them; then to the short cut when both sides are taken. */
export const LINE_LABEL_CHARS = 16
export const LINE_LABEL_SHORT = 8
/** A label's box: JetBrains Mono at 11 px is 6.6 px a character; 12 px tall, 10 px off the station's centre. */
export const LINE_LABEL_CHAR_W = 6.6
export const LINE_LABEL_H = 12
export const LINE_LABEL_OFFSET = 10
/** `TB` labels sit to the right in at most this width (the 16-character cut is 105.6 px, so it fits). */
export const LINE_TB_LABEL_W = 128

export type LineLabelSide = 'above' | 'below' | 'right' | 'none'

export interface LineBox {
  x: number
  y: number
  w: number
  h: number
}

export interface LineLabel {
  /** The id as shown, cut with "…"; empty when hidden or at `mini`. A cut or hidden label's whole text is the station's `title`. */
  text: string
  /** `none`: hidden (no room, or `mini`); the whole id stays in the station's `title`. */
  side: LineLabelSide
  box?: LineBox
}

/** `split`: two or more tasks need it; `merge`: it needs two or more; `both`. */
export type LineJunction = 'split' | 'merge' | 'both'

export interface LineStation {
  id: string
  depth: number
  lane: number
  /** The centre. */
  x: number
  y: number
  /** The id of the line it is on. */
  line: string
  label: LineLabel
  /** The whole id. */
  title: string
  junction?: LineJunction
  /** Why a need of it is not drawn: a need the run has no task for, a cycle, a need that does not come before it. */
  warning?: string
}

export interface DiagramLine {
  /** Its first station's id. */
  id: string
  /** In depth order. */
  stations: string[]
  lane: number
  /** The station a branch leaves from; absent for a line that starts on its own. */
  from?: string
}

export interface LinePoint {
  x: number
  y: number
}

export interface LineConnector {
  from: string
  to: string
  /** `branch`: into the first station of a line, from its parent; `merge`: any other need that is not a step along a line. */
  kind: 'branch' | 'merge'
  /** The ends (the two stations' centres) and the turns of an orthogonal path. */
  points: LinePoint[]
  /** The radius its turns are rounded at. */
  corner: number
}

export interface LineLayout {
  /** By depth, then in the run's order. */
  stations: LineStation[]
  /** In the order they were made. */
  lines: DiagramLine[]
  connectors: LineConnector[]
  width: number
  height: number
  /** A task's warning, by its id. */
  warnings: Map<string, string>
  /** The geometry it was laid out at (a copy of `LINE_GEOMETRY`'s for its size and direction), so that a drawing looks nothing up. */
  geometry: LineGeometry
}

/** A path drawn through the layout, which no label may be crossed by: a line through its stations' centres (`corner` 0), or a connector. */
export interface LinePath {
  points: readonly LinePoint[]
  corner: number
}

interface Building {
  id: string
  stations: string[]
  from?: string
  made: number
}

const ceil4 = (v: number) => Math.ceil(v / 4) * 4

/** The id cut to `n` characters with "…". */
export function cutLineLabel(id: string, n: number): string {
  return id.length > n ? `${id.slice(0, n - 1)}…` : id
}

function meets(a: LineBox, b: LineBox): boolean {
  return a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h
}

function boxHitsCircle(b: LineBox, x: number, y: number, r: number): boolean {
  const cx = Math.min(Math.max(x, b.x), b.x + b.w)
  const cy = Math.min(Math.max(y, b.y), b.y + b.h)
  return (x - cx) ** 2 + (y - cy) ** 2 < r * r
}

/** Drops a point that repeats the one before it, and one in the middle of a straight run. */
function simplify(ps: LinePoint[]): LinePoint[] {
  const out: LinePoint[] = []
  for (const p of ps) {
    const last = out[out.length - 1]
    if (last && last.x === p.x && last.y === p.y) continue
    const before = out[out.length - 2]
    if (last && before && ((before.x === last.x && last.x === p.x) || (before.y === last.y && last.y === p.y))) out.pop()
    out.push(p)
  }
  return out
}

function labelBox(s: LineStation, text: string, side: LineLabelSide): LineBox {
  const w = text.length * LINE_LABEL_CHAR_W
  if (side === 'right') return { x: s.x + LINE_LABEL_OFFSET, y: s.y - LINE_LABEL_H / 2, w, h: LINE_LABEL_H }
  // Centred over or under its station, but never past the drawing's left edge.
  return { x: Math.max(0, s.x - w / 2), y: side === 'above' ? s.y - LINE_LABEL_OFFSET - LINE_LABEL_H : s.y + LINE_LABEL_OFFSET, w, h: LINE_LABEL_H }
}

/**
 * What a path covers, as boxes a label's box must not meet: each straight run
 * (every run of a layout is straight along or across, so its box is the run
 * itself, of no width), and each rounded turn's square of its corner, inside
 * which the rounding is drawn, whatever its curve.
 */
function pathBoxes(path: LinePath): LineBox[] {
  const ps = path.points
  const out: LineBox[] = []
  ps.forEach((b, i) => {
    const a = ps[i - 1]
    if (a) out.push({ x: Math.min(a.x, b.x), y: Math.min(a.y, b.y), w: Math.abs(b.x - a.x), h: Math.abs(b.y - a.y) })
    const c = ps[i + 1]
    if (!a || !c || path.corner === 0) return
    const dx = Math.sign(a.x - b.x) + Math.sign(c.x - b.x)
    const dy = Math.sign(a.y - b.y) + Math.sign(c.y - b.y)
    out.push({ x: Math.min(b.x, b.x + dx * path.corner), y: Math.min(b.y, b.y + dy * path.corner), w: Math.abs(dx) * path.corner, h: Math.abs(dy) * path.corner })
  })
  return out
}

/**
 * Step 5: each station's label, in station order (the stations by depth, so a
 * line's own in its order). In `LR` the side alternates along the station's
 * line, above first; a box that meets a box already placed, another station's
 * circle or one of `paths` (the lines and the connectors, `pathBoxes`) takes
 * the other side; if both meet one it is cut short and tried again; if it
 * still meets one it is hidden. In `TB` it sits to the right, so it is cut,
 * then hidden. A cut or hidden label's whole text stays in the station's
 * `title`, for the drawing to show on focus or in a caption.
 */
export function placeLabels(stations: readonly LineStation[], direction: GraphDirection, radius: number, paths: readonly LinePath[] = []): LineLabel[] {
  const placed: LineBox[] = []
  const crossing = paths.flatMap(pathBoxes)
  const along = new Map<string, number>()
  return stations.map((s) => {
    const k = along.get(s.line) ?? 0
    along.set(s.line, k + 1)
    const sides: LineLabelSide[] = direction === 'TB' ? ['right'] : k % 2 === 0 ? ['above', 'below'] : ['below', 'above']
    const first = direction === 'TB' ? Math.min(LINE_LABEL_CHARS, Math.floor(LINE_TB_LABEL_W / LINE_LABEL_CHAR_W)) : LINE_LABEL_CHARS
    for (const n of [first, LINE_LABEL_SHORT]) {
      const text = cutLineLabel(s.id, n)
      for (const side of sides) {
        const box = labelBox(s, text, side)
        if (placed.some((b) => meets(b, box)) || crossing.some((b) => meets(b, box)) || stations.some((o) => o !== s && boxHitsCircle(box, o.x, o.y, radius))) continue
        placed.push(box)
        return { text, side, box }
      }
    }
    return { text: '', side: 'none' }
  })
}

/** The tasks laid out as lines and stations; see the module's comment for the rule. */
export function lineLayout(nodes: readonly DepthNode[], opts: LineOptions = {}): LineLayout {
  const direction = opts.direction ?? 'LR'
  const g = LINE_GEOMETRY[opts.size ?? 'full'][direction]
  const warnings = new Map<string, string>()
  const warn = (id: string, w: string) => {
    const had = warnings.get(id)
    warnings.set(id, had ? `${had}; ${w}` : w)
  }

  // A second task of one id (the server refuses it) is drawn once, as the first.
  const tasks: DepthNode[] = []
  const order = new Map<string, number>()
  for (const n of nodes) {
    if (order.has(n.id)) {
      warn(n.id, 'listed twice in the run: drawn once')
      continue
    }
    order.set(n.id, tasks.length)
    tasks.push(n)
  }

  // Step 1: depth, and the needs that are drawn.
  const { depth, warnings: depthWarnings } = taskDepths(tasks)
  for (const [id, w] of depthWarnings) warn(id, w)
  const d = (id: string) => depth.get(id) ?? 0
  const at = (id: string) => order.get(id) ?? 0
  const visit = [...tasks].sort((a, b) => d(a.id) - d(b.id) || at(a.id) - at(b.id))
  const drawn = new Map<string, string[]>()
  for (const t of visit) {
    const forward: string[] = []
    const back: string[] = []
    if (!depthWarnings.has(t.id)) {
      for (const n of t.needs ?? []) {
        if (!order.has(n) || forward.includes(n) || back.includes(n)) continue
        ;(d(n) < d(t.id) ? forward : back).push(n)
      }
    }
    if (back.length) warn(t.id, `needs ${back.join(', ')}, which ${back.length === 1 ? 'does' : 'do'} not come before it (a cycle): not drawn`)
    drawn.set(t.id, forward)
  }

  // Step 2: lines. A task continues the line that ends at its deepest need so far, or starts a line of its own.
  const lines: Building[] = []
  const lineOf = new Map<string, Building>()
  const links: Array<{ from: string; to: string; kind: 'branch' | 'merge' }> = []
  const neededBy = new Map<string, number>()
  const deepest = (ids: string[]) => ids.reduce((best, n) => (d(n) > d(best) ? n : best))
  for (const t of visit) {
    const needs = drawn.get(t.id) ?? []
    for (const n of needs) neededBy.set(n, (neededBy.get(n) ?? 0) + 1)
    let via: string | undefined
    let line: Building | undefined
    if (needs.length) {
      const candidates = needs.filter((n) => {
        const l = lineOf.get(n)
        return l !== undefined && l.stations[l.stations.length - 1] === n
      })
      if (candidates.length) {
        via = deepest(candidates)
        line = lineOf.get(via)
        line?.stations.push(t.id)
      } else {
        via = deepest(needs)
        links.push({ from: via, to: t.id, kind: 'branch' })
      }
    }
    if (!line) {
      line = { id: t.id, stations: [t.id], from: via, made: lines.length }
      lines.push(line)
    }
    lineOf.set(t.id, line)
    for (const n of needs) if (n !== via) links.push({ from: n, to: t.id, kind: 'merge' })
  }

  // Step 3: lanes. Placed by where their spans start, each takes the free lane nearest its parent's (below first on a tie).
  const span = (l: Building): [number, number] => [d(l.from ?? l.stations[0] ?? l.id), d(l.stations[l.stations.length - 1] ?? l.id)]
  const laneOf = new Map<Building, number>()
  const laneEnds: number[] = []
  for (const l of [...lines].sort((a, b) => span(a)[0] - span(b)[0] || a.made - b.made)) {
    const [start, end] = span(l)
    const free = laneEnds.flatMap((e, i) => (e < start ? [i] : []))
    const parent = l.from === undefined ? undefined : lineOf.get(l.from)
    const near = parent === undefined ? undefined : laneOf.get(parent)
    let lane = laneEnds.length
    if (free.length && near === undefined) lane = free[0] ?? lane
    else if (free.length && near !== undefined) {
      lane = free.reduce((best, i) => {
        const di = Math.abs(i - near)
        const db = Math.abs(best - near)
        return di < db || (di === db && i > best) ? i : best
      })
    }
    laneEnds[lane] = end
    laneOf.set(l, lane)
  }
  const laneAt = (id: string) => {
    const l = lineOf.get(id)
    return l === undefined ? 0 : (laneOf.get(l) ?? 0)
  }

  // Step 4: geometry. `along` is the depth axis, `across` the lanes'; a half depth is a gutter, a half lane a track.
  const along = (depthAt: number) => g.margin + depthAt * g.step
  const across = (laneAtValue: number) => g.margin + laneAtValue * g.laneGap
  const point = (a: number, c: number): LinePoint => (direction === 'LR' ? { x: a, y: c } : { x: c, y: a })
  const route = (from: string, to: string): LinePoint[] => {
    const [ds, dt, ls, lt] = [d(from), d(to), laneAt(from), laneAt(to)]
    const out = along(ds + 0.5)
    const ps: Array<[number, number]> = [
      [along(ds), across(ls)],
      [out, across(ls)],
    ]
    if (dt === ds + 1) ps.push([out, across(lt)])
    else {
      // The track beside the end's lane on the start's side; above on a shared lane, but on lane 0 below, inside the drawing.
      const track = across(lt + (ls > lt || (ls === lt && lt === 0) ? 0.5 : -0.5))
      const into = along(dt - 0.5)
      ps.push([out, track], [into, track], [into, across(lt)])
    }
    ps.push([along(dt), across(lt)])
    return simplify(ps.map(([a, c]) => point(a, c)))
  }
  const connectors: LineConnector[] = links.map((k) => ({ ...k, points: route(k.from, k.to), corner: g.corner }))

  const stations: LineStation[] = visit.map((t) => {
    const p = point(along(d(t.id)), across(laneAt(t.id)))
    const splits = (neededBy.get(t.id) ?? 0) >= 2
    const merges = (drawn.get(t.id) ?? []).length >= 2
    const junction: LineJunction | undefined = splits && merges ? 'both' : splits ? 'split' : merges ? 'merge' : undefined
    const s: LineStation = { id: t.id, depth: d(t.id), lane: laneAt(t.id), x: p.x, y: p.y, line: lineOf.get(t.id)?.id ?? t.id, label: { text: '', side: 'none' }, title: t.id }
    if (junction) s.junction = junction
    const w = warnings.get(t.id)
    if (w) s.warning = w
    return s
  })

  // Step 5: labels, clear of the lines (through their stations' centres) and the connectors.
  if (g.labels) {
    const at = new Map(stations.map((s) => [s.id, s]))
    const paths: LinePath[] = [...lines.map((l) => ({ points: l.stations.flatMap((id) => at.get(id) ?? []), corner: 0 })), ...connectors]
    const labels = placeLabels(stations, direction, g.radius, paths)
    stations.forEach((s, i) => (s.label = labels[i] ?? s.label))
  }

  // Bounds: everything drawn, and a margin past it.
  let right = g.margin
  let bottom = g.margin
  const reach = (x: number, y: number) => {
    right = Math.max(right, x)
    bottom = Math.max(bottom, y)
  }
  for (const s of stations) reach(s.x, s.y)
  for (const { label } of stations) if (label.box) reach(label.box.x + label.box.w, label.box.y + label.box.h)
  for (const c of connectors) for (const p of c.points) reach(p.x, p.y)

  return {
    stations,
    lines: lines.map((l) => {
      const out: DiagramLine = { id: l.id, stations: l.stations, lane: laneOf.get(l) ?? 0 }
      if (l.from !== undefined) out.from = l.from
      return out
    }),
    connectors,
    width: ceil4(right + g.margin),
    height: ceil4(bottom + g.margin),
    warnings,
    geometry: { ...g },
  }
}
