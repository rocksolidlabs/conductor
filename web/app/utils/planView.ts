/**
 * A plan revision made on another machine (sync's seams, OS-1a, docs/plans/sync-seams.md "Plan views"): this machine holds its view
 * alone, never its body. The view names the revision's base, its crew's tasks (each with the member that does it and that member's
 * agent), the agents by their catalog ids, the references, the attachments by name, the comments handed over, the constraints, the
 * decisions and the note; never a command, an argument or a path, which stay on the machine that made it, which alone launches it.
 */

/** A revision's view as the server gives it, version 1. */
export interface PlanViewDoc {
  v: number
  /** The whole body's digest, the revision's identity everywhere. */
  digest: string
  base: string
  crew: { id: string; name: string; tasks: PlanViewTask[] }
  agents: string[]
  references: Array<{ path: string; start?: number; end?: number; note?: string; object: string; kind: string }>
  attachments: Array<{ id: string; name: string }>
  comments: Array<{ id: string; sha256: string }>
  constraints: string
  decisions: Array<{ id: string; question: string; kind: string; before: string[]; for?: string }>
  note: string
}

/** A task as a view gives it. */
export interface PlanViewTask {
  id: string
  kind: string
  member: string
  agent: string
  instructions: string
  needs: string[]
}

/** Whether the revision is held as its view: made on another machine, never launched here. */
export function heldAsView(p: { bodyHere?: boolean }): boolean {
  return p.bodyHere === false
}

/** "made on studio", "approved on studio", joined: where a revision was made and last approved, when that was another machine; "" for none. */
export function originWords(p: { origin?: string; approvedOrigin?: string }): string {
  const parts: string[] = []
  if (p.origin) parts.push(`made on ${p.origin}`)
  if (p.approvedOrigin) parts.push(`approved on ${p.approvedOrigin}`)
  return parts.join(', ')
}

/** Why a revision held as its view is not launched here, naming the machine that keeps its crew. */
export function viewLaunchWords(p: { origin?: string }): string {
  return `This revision's crew is on ${p.origin || 'another machine'}: launch it there`
}

/** Each task of a view in one line: its id, its kind when a review, who does it with their agent, and what it needs. */
export function viewTaskLines(view: Pick<PlanViewDoc, 'crew'>): Array<{ id: string; line: string; instructions: string }> {
  return view.crew.tasks.map((t) => {
    const who = t.agent ? `${t.member} (${t.agent})` : t.member
    const kind = t.kind === 'review' ? 'review by' : 'by'
    const needs = t.needs.length ? `, after ${t.needs.join(', ')}` : ''
    return { id: t.id, line: `${t.id}: ${kind} ${who}${needs}`, instructions: t.instructions }
  })
}
