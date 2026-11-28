import type { PlanBody, PlanCompare, PlanSummary } from '~/composables/useDispatch'
import { checksEachWords, manifestForm, type ManifestForm } from './dispatchPlans'

/**
 * A comparison's launch (CP1, docs/plans/compare-crew.md, "The launch says what it costs"): what it starts, said before anything starts, from
 * the revision's own `compare` part, and the count the person's Launch sends back. The count is only ever the revision's: never the saved
 * crew's, never one the page works out. No total is stated: the engine keeps none, since it refunds a review's request for changes and an
 * attempt that could not start, a Retry raises a task's bound, and a candidate that changes the manifest runs its own checks.
 */

/**
 * What the crews page and a crew's launch sheet say of a comparison's crew (an agent trial, in the app's words), in place of its Launch:
 * the server launches it from a ticket alone.
 */
export const COMPARISON_LAUNCHES_FROM_A_TICKET = "An agent trial launches from a ticket in Dispatch: press Propose a plan or New revision, pick this crew, then Propose, Approve and Launch"

/** The attempts a task's runs count before it fails (internal/crew, maxAttemptsPerTask): each one a session of its own. */
export const COUNTED_ATTEMPTS = 3

/** One part of the statement: what it counts, and its words. */
export interface StatementPart {
  key: 'candidates' | 'reviews' | 'multiplier' | 'checks' | 'brakeman' | 'usage'
  words: string
}

/** What a launch from a revision is: an ordinary one (no count sent), a comparison with its count and statement, or one not to send yet. */
export type CompareLaunch =
  | { kind: 'ordinary' }
  | { kind: 'compare'; count: number; agents: string[]; statement: StatementPart[] }
  /** A comparison whose statement cannot be drawn (its part not read, or the body and the part disagree): Launch is held, with why. */
  | { kind: 'unstated'; words: string }

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`

/** Where a candidate attempt's checks come from, by the revision's manifest. */
const CHECKS_FROM: Record<ManifestForm, string> = {
  none: "the task's checks; the plan holds no manifest, so no verify steps",
  crew: "the task's checks and the crew's manifest's verify steps",
  file: "the task's checks and conductor.manifest.json's verify steps at the base; a candidate that changes the manifest runs its own",
}

/**
 * The statement's parts, in the plan's words: the candidates and their sessions; up to as many review sessions after them; the
 * multiplier, with the refunds and Retries named; the checks on each submitted candidate attempt at the base, named by where they come
 * from (`manifest`: the revision's, manifestForm: no verify step is named for a crew with no manifest, and only the file form's
 * conductor.manifest.json can be changed by a candidate); the Brakeman turn; and where usage per account is shown (AC1's member lines,
 * below the statement).
 */
export function compareStatement(part: PlanCompare, agents: readonly string[], manifest: ManifestForm = 'file'): StatementPart[] {
  const n = part.candidates
  const out: StatementPart[] = [
    {
      key: 'candidates',
      words: `This trial starts ${plural(n, 'candidate')}${agents.length ? ` (${agents.join(', ')})` : ''}: ${plural(n, 'agent session')}, once the plan's decisions and the project's prerequisites let the work start`,
    },
  ]
  if (part.reviews > 0)
    out.push({
      key: 'reviews',
      words: `then an agent review of each candidate that succeeds: up to ${part.reviews} more ${part.reviews === 1 ? 'session' : 'sessions'}, none for a candidate that fails or is cancelled`,
    })
  out.push({
    key: 'multiplier',
    words: `then what ${plural(n, 'run')} of one candidate's crew would take, since each candidate is worked as a task of its own crew is (each attempt a session of its own; ${COUNTED_ATTEMPTS} counted attempts a task, and a review's request for changes, an attempt that could not start and each Retry you give add more)`,
  })
  out.push({
    key: 'checks',
    words: part.checksUnread
      ? `the checks on each submitted candidate attempt are not known: ${part.checksUnread} (a candidate that changes the manifest runs its own)`
      : `${checksEachWords(part.checksAtBase)} (${CHECKS_FROM[manifest]})`,
  })
  out.push({ key: 'brakeman', words: part.brakemanTurn ? '1 Brakeman turn on your Brakeman account, when all candidates are done' : 'no Brakeman turn: this trial asks no summary' })
  out.push({ key: 'usage', words: 'Usage per account: shown below' })
  return out
}

/**
 * The agents of a comparison's candidates, in the block's order, each by its catalog name (its id when the catalog does not list it):
 * each candidate's task, that task's member, that member's agent, as the revision's body holds them. null when the body does not name
 * one of them (a block the crew's validation would refuse): the statement is then not drawn.
 */
export function candidateAgents(body: PlanBody, catalog: ReadonlyArray<{ id: string; name: string }>): string[] | null {
  const block = body.crew.compare
  if (!block) return null
  const out: string[] = []
  for (const c of block.candidates) {
    const task = body.crew.tasks?.find((t) => t.id === c.task)
    const member = task && body.crew.members?.find((m) => m.name === task.member)
    if (!member) return null
    out.push(catalog.find((a) => a.id === member.agentId)?.name ?? member.agentId)
  }
  return out
}

/**
 * What a launch from `revision` is, once its body is read. A revision with a `compare` part whose body's block has as many candidates is
 * a comparison: its count is the part's, its statement drawn from it. A revision with neither is ordinary. Either one without the other
 * (a read made before the server stated comparisons, a body that disagrees with its part) is not sent: Launch is held with why, since a
 * count the person was not shown is never sent and the server refuses a comparison launched without one.
 */
export function compareLaunch(revision: Pick<PlanSummary, 'n' | 'compare'>, body: PlanBody, catalog: ReadonlyArray<{ id: string; name: string }>): CompareLaunch {
  const part = revision.compare
  const block = body.crew.compare
  if (!part && !block) return { kind: 'ordinary' }
  if (!part) return { kind: 'unstated', words: `Revision ${revision.n} is a trial, and the server did not say what it starts: read the ticket again before launching it` }
  const agents = block ? candidateAgents(body, catalog) : null
  if (!block || !agents || agents.length !== part.candidates || !Number.isInteger(part.candidates) || part.candidates < 1)
    return { kind: 'unstated', words: `Revision ${revision.n}'s statement and its crew disagree on its candidates: read the ticket again before launching it` }
  return { kind: 'compare', count: part.candidates, agents, statement: compareStatement(part, agents, manifestForm(body)) }
}

/** The launch button's words: "Launch 3 candidates" for a comparison, "Launch" otherwise; a resend's and a retry's words stay theirs. */
export function launchButtonWords(l: CompareLaunch | null, outcome: 'pending' | 'failed' | 'error' | null): string {
  if (outcome === 'pending' || outcome === 'error') return 'Send again'
  if (outcome === 'failed') return 'Launch again'
  return l?.kind === 'compare' ? `Launch ${plural(l.count, 'candidate')}` : 'Launch'
}
