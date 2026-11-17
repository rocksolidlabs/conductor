import type { CrewCheck, CrewManifest } from '~/composables/useSessions'
import type { CrewCriterion } from './protocol'
import { checkProblems, toCrewCheck, TASK_LIMITS, type DraftCheck } from './crews'

/**
 * The New comparison form's draft and the body it posts (CP1, docs/plans/compare-crew.md, "The template"): POST /api/crews/templates/compare
 * takes a CompareSpec (internal/crew CompareSpec, its JSON names) and saves an ordinary crew with tasks approach-a, approach-b… and a
 * `compare` block. The bounds below are the server's (internal/crew compare.go, crew.go); the server holds the spec to them and answers
 * `400 invalid_crew` in their words, which the form shows as they come.
 */

/** The template route's body (internal/crew CompareSpec): unknown fields are refused. */
export interface CompareSpec {
  name: string
  cwd: string
  goal: string
  instructions: string
  criteria: CrewCriterion[]
  checks?: CrewCheck[]
  manifest?: CrewManifest
  candidates: Array<{ agentId: string; args?: string[]; approach?: string }>
  reviews?: boolean
  reviewAgent?: string
  brakeman?: boolean
}

/** The server's bounds of a comparison's template. */
export const COMPARE_LIMITS = {
  name: 60,
  goal: 2000,
  instructions: TASK_LIMITS.instructions,
  minCriteria: 1,
  criteria: 12,
  criterionText: 500,
  minCandidates: 2,
  candidates: 4,
  approach: 2000,
  checks: TASK_LIMITS.checks,
} as const

/** A candidate in the form: its agent, its arguments as chips (and the text not yet one), its approach. */
export interface DraftCandidate {
  key: number
  agentId: string
  args: string[]
  argsPending?: string
  approach: string
}

/** The form's draft. `manifestFile`: the checks are the project's conductor.manifest.json verify steps, read at the run's base. */
export interface DraftComparison {
  name: string
  cwd: string
  goal: string
  instructions: string
  criteria: Array<{ key: number; text: string }>
  checks: DraftCheck[]
  manifestFile: boolean
  candidates: DraftCandidate[]
  reviews: boolean
  reviewAgent: string
  brakeman: boolean
}

let keys = 0
/** A key for a new row of the form, never reused on the page. */
export const rowKey = () => ++keys

/** A new draft: two candidates of the first agent, one criterion, one check, no review, no Brakeman. */
export function draftComparison(firstAgent = ''): DraftComparison {
  return {
    name: '',
    cwd: '',
    goal: '',
    instructions: '',
    criteria: [{ key: rowKey(), text: '' }],
    checks: [{ id: 'check', argv: [] }],
    manifestFile: false,
    candidates: [
      { key: rowKey(), agentId: firstAgent, args: [], approach: '' },
      { key: rowKey(), agentId: firstAgent, args: [], approach: '' },
    ],
    reviews: false,
    reviewAgent: '',
    brakeman: false,
  }
}

/**
 * A candidate moved to another agent: one that takes no arguments takes none of the arguments typed for the one before (nor the text not
 * yet a chip), which its form no longer shows, as a crew member's agent change does (CrewMembersTable).
 */
export function withCandidateAgent(c: DraftCandidate, agentId: string, agents: ReadonlyArray<{ id: string; allowArgs?: boolean }>): DraftCandidate {
  return agents.find((a) => a.id === agentId)?.allowArgs === false ? { ...c, agentId, args: [], argsPending: undefined } : { ...c, agentId }
}

/**
 * The draft New comparison shows as it opens: the one kept since it was last closed (Escape, a click outside), else a new one; its
 * candidates with no agent yet (the catalog was not read when it was made) on the catalog's first agent. Only Cancel and a crew created
 * end a draft.
 */
export function draftOnOpen(kept: DraftComparison | null, firstAgent: string): DraftComparison {
  if (!kept) return draftComparison(firstAgent)
  return { ...kept, candidates: kept.candidates.map((c) => (c.agentId ? c : { ...c, agentId: firstAgent })) }
}

const chars = (s: string) => [...s].length
// A line as the server takes a criterion's text: no control character and no line or paragraph separator.
const NOT_ONE_LINE = new RegExp('[\\u0000-\\u001f\\u007f-\\u009f\\u2028\\u2029]')

/**
 * A criterion's id from its text, as a task's id is written (lower-case letters, digits, "_" and "-", at most 40): its words joined by
 * "-" and cut between words, else `criterion-<n>`; then `-2`, `-3`… while another criterion has it.
 */
export function criterionId(text: string, n: number, taken: readonly string[]): string {
  const words = text
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
  // At most 34 characters, cut between words when there is one to cut at, so `-2`… still fit in 40.
  const cut = words.length <= 34 ? words : words.slice(0, 35).lastIndexOf('-') > 0 ? words.slice(0, words.slice(0, 35).lastIndexOf('-')) : words.slice(0, 34)
  const base = cut || `criterion-${n}`
  let id = base
  for (let i = 2; taken.includes(id); i++) id = `${base}-${i}`
  return id
}

/** The criteria as the spec sends them: each with its id from its text, in the form's order. */
export function specCriteria(rows: ReadonlyArray<{ text: string }>): CrewCriterion[] {
  const out: CrewCriterion[] = []
  rows.forEach((r, i) => {
    const text = r.text.trim()
    out.push({ id: criterionId(text, i + 1, out.map((c) => c.id)), text })
  })
  return out
}

/** The body Create posts: the fields the route takes and nothing else; the checks or the project's manifest, the review agent only with reviews. */
export function compareSpec(d: DraftComparison): CompareSpec {
  const out: CompareSpec = {
    name: d.name.trim(),
    cwd: d.cwd.trim(),
    goal: d.goal.trim(),
    instructions: d.instructions.trim(),
    criteria: specCriteria(d.criteria),
    candidates: d.candidates.map((c) => {
      const o: CompareSpec['candidates'][number] = { agentId: c.agentId }
      if (c.args.length) o.args = [...c.args]
      if (c.approach.trim()) o.approach = c.approach.trim()
      return o
    }),
  }
  if (d.checks.length) out.checks = d.checks.map(toCrewCheck)
  if (d.manifestFile) out.manifest = { file: true }
  if (d.reviews) {
    out.reviews = true
    out.reviewAgent = d.reviewAgent
  }
  if (d.brakeman) out.brakeman = true
  return out
}

/**
 * What stops Create, in words, the first first: the server's bounds as far as a page can know them (it decides; its refusal is shown as
 * it comes). `agents` the catalog, for an agent that takes no arguments.
 */
export function compareProblems(d: DraftComparison, agents: ReadonlyArray<{ id: string; name: string; allowArgs?: boolean }>): string[] {
  const out: string[] = []
  const name = d.name.trim()
  if (!name) out.push('Name the comparison')
  else if (chars(name) > COMPARE_LIMITS.name) out.push(`The name is at most ${COMPARE_LIMITS.name} characters`)
  if (!d.cwd.trim()) out.push('Pick the project folder')
  if (!d.goal.trim()) out.push('Say the objective')
  else if (chars(d.goal) > COMPARE_LIMITS.goal) out.push(`The objective is at most ${COMPARE_LIMITS.goal} characters`)
  if (chars(d.instructions) > COMPARE_LIMITS.instructions) out.push(`The instructions are at most ${COMPARE_LIMITS.instructions} characters`)
  if (d.criteria.length < COMPARE_LIMITS.minCriteria || d.criteria.length > COMPARE_LIMITS.criteria) out.push(`A comparison has ${COMPARE_LIMITS.minCriteria} to ${COMPARE_LIMITS.criteria} criteria`)
  d.criteria.forEach((c, i) => {
    if (!c.text.trim()) out.push(`Criterion ${i + 1}: say what is compared`)
    else if (chars(c.text.trim()) > COMPARE_LIMITS.criterionText) out.push(`Criterion ${i + 1}: at most ${COMPARE_LIMITS.criterionText} characters`)
    else if (NOT_ONE_LINE.test(c.text.trim())) out.push(`Criterion ${i + 1}: one line`)
  })
  if (!d.checks.length && !d.manifestFile) out.push("Add a check, or use the project's conductor.manifest.json: a comparison with nothing to check compares nothing")
  const ids: string[] = []
  d.checks.forEach((c, i) => {
    out.push(...checkProblems(c, `Check ${i + 1}`, ids))
    ids.push(c.id)
  })
  if (d.candidates.length < COMPARE_LIMITS.minCandidates || d.candidates.length > COMPARE_LIMITS.candidates) out.push(`A comparison has ${COMPARE_LIMITS.minCandidates} to ${COMPARE_LIMITS.candidates} candidates`)
  d.candidates.forEach((c, i) => {
    const what = `Candidate ${String.fromCharCode(65 + i)}`
    const agent = agents.find((a) => a.id === c.agentId)
    if (!c.agentId) out.push(`${what}: pick an agent`)
    else if (!agent) out.push(`${what}: ${c.agentId} is not in the catalog`)
    else if (c.args.length && agent.allowArgs === false) out.push(`${what}: ${agent.name} takes no arguments`)
    if (c.argsPending) out.push(`${what}: an argument is still being typed: close its quote, or press Enter`)
    if (chars(c.approach.trim()) > COMPARE_LIMITS.approach) out.push(`${what}: the approach is at most ${COMPARE_LIMITS.approach} characters`)
  })
  if (d.reviews && !d.reviewAgent) out.push('Pick the agent that reviews each candidate')
  return out
}
