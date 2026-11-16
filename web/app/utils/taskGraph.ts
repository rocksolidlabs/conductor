import type { RunTask } from '~/composables/useSessions'

// The tasks of a managed run by depth (#334, row 7): a task one past the
// deepest task it needs. The Graph view draws them as lines (lineLayout,
// utils/lineDiagram.ts); a phone lists them by depth (taskListRows).

export interface TaskNode {
  /** The task's id. */
  id: string
  task: RunTask
  /** One past the deepest task it needs; 0 for a task that needs nothing here. */
  depth: number
  /** A need the run has no task for, or a cycle: drawn as a root, said on its row. */
  warning?: string
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

/** The tasks as a list (a phone's Graph view), a task after the tasks it needs: by depth, then in the run's order. */
export function taskListRows(tasks: readonly RunTask[]): TaskNode[] {
  const { depth, warnings } = taskDepths(tasks)
  const rows: TaskNode[] = tasks.map((t) => {
    const n: TaskNode = { id: t.id, task: t, depth: depth.get(t.id) ?? 0 }
    const w = warnings.get(t.id)
    if (w) n.warning = w
    return n
  })
  const at = new Map(tasks.map((t, i) => [t, i]))
  return rows.sort((a, b) => a.depth - b.depth || (at.get(a.task) ?? 0) - (at.get(b.task) ?? 0))
}
