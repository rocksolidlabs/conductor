import type { RunTask } from '~/composables/useSessions'
import { GAP_X, GAP_Y, NODE_H, NODE_W, type GraphDirection, type GraphEdge } from './crewGraph'

// The tasks of a managed run as a graph (#334, row 7): one node per task,
// an edge from each task to the tasks that need its result, laid out in
// columns by depth as the crew graph lays out its members.

export interface TaskNode {
  /** The task's id. */
  id: string
  task: RunTask
  /** Column: one past the deepest task it needs; 0 for a task that needs nothing here. */
  depth: number
  /** Row within its column, in the run's order. */
  row: number
  x: number
  y: number
  /** A need the run has no task for, or a cycle: drawn as a root, said on the node. */
  warning?: string
}

export interface TaskGraphData {
  nodes: TaskNode[]
  edges: GraphEdge[]
}

/** What a task's depth reads of it: a run's task, a crew's task shape and a member as its needs all are one. */
export type DepthNode = Pick<RunTask, 'id' | 'needs'>

/** Each task's column, and the warnings for needs that cannot be drawn. */
export function taskDepths(tasks: readonly DepthNode[]): { depth: Map<string, number>; warnings: Map<string, string> } {
  const byId = new Map(tasks.map((t) => [t.id, t]))
  const depth = new Map<string, number>()
  const warnings = new Map<string, string>()
  const visiting = new Set<string>()
  const visit = (t: DepthNode): number => {
    const known = depth.get(t.id)
    if (known !== undefined) return known
    if (visiting.has(t.id)) {
      // The server refuses a cycle; drawn as a root all the same.
      warnings.set(t.id, 'needs itself, through the tasks it needs')
      return -1
    }
    visiting.add(t.id)
    let deepest = -1
    for (const need of t.needs ?? []) {
      const p = byId.get(need)
      if (!p) {
        warnings.set(t.id, `needs ${need}, which the run has no task for`)
        continue
      }
      deepest = Math.max(deepest, visit(p))
    }
    visiting.delete(t.id)
    const d = warnings.has(t.id) ? 0 : deepest + 1
    depth.set(t.id, d)
    return d
  }
  for (const t of tasks) visit(t)
  return { depth, warnings }
}

/** The nodes laid out by depth, and a `needs` edge from each task to each task that needs it (none to a task drawn with a warning). */
export function taskLayout(tasks: readonly RunTask[], direction: GraphDirection = 'LR'): TaskGraphData {
  const { depth, warnings } = taskDepths(tasks)
  const rows = new Map<number, number>()
  const ids = new Set(tasks.map((t) => t.id))
  const nodes: TaskNode[] = tasks.map((t) => {
    const d = depth.get(t.id) ?? 0
    const row = rows.get(d) ?? 0
    rows.set(d, row + 1)
    const along = d * (NODE_W + GAP_X)
    const across = row * (NODE_H + GAP_Y)
    return { id: t.id, task: t, depth: d, row, x: direction === 'LR' ? along : across, y: direction === 'LR' ? across : along, warning: warnings.get(t.id) }
  })
  const edges: GraphEdge[] = []
  for (const t of tasks) {
    if (warnings.has(t.id)) continue
    for (const need of t.needs ?? []) {
      if (ids.has(need)) edges.push({ id: `needs:${need}-${t.id}`, from: need, to: t.id, kind: 'needs' })
    }
  }
  return { nodes, edges }
}

/** The nodes as a list, a task after the tasks it needs: by column, then in the run's order. */
export function taskListRows(g: TaskGraphData): TaskNode[] {
  return [...g.nodes].sort((a, b) => a.depth - b.depth || a.row - b.row)
}
