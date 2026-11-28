import type { CrewCheck, CrewManifest } from '~/composables/useSessions'
import type { CrewCriterion } from './protocol'
import { hasOpenQuote, splitArgs } from './argv'
import { checkProblems, toCrewCheck, TASK_LIMITS, type DraftCheck } from './crews'
import { GIT_CAN_WORKTREE, GIT_FIRST_COMMIT } from './dirInput'
import { FOLDER_CANT } from './folderAllow'
import { moreLine } from './moreOptions'

/**
 * The New agent trial form's draft, what stops its press, and the body it posts (CP1, docs/plans/compare-crew.md, "The template", where
 * a trial is called a comparison): POST /api/crews/templates/compare takes a CompareSpec (internal/crew CompareSpec, its JSON names) and
 * saves an ordinary crew with tasks approach-a, approach-b… and a `compare` block. The bounds below are the server's (internal/crew
 * compare.go, crew.go); the server holds the spec to them and answers `400 invalid_crew` in its words, which the form shows as they come.
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
  /** The trial's Yolo as its switch showed it at the press; the crew's own default otherwise. */
  yolo?: boolean
}

/** The server's bounds of a trial's template. */
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

/** The one line a good result does, as a new trial starts. */
export const DEFAULT_CRITERION = 'Does what the task asks'

/** The id the form's Check line is sent under; the checks under More options take others. */
export const CHECK_ID = 'check'

/** One agent of the trial: its agent, its arguments as one line (as Launch agent's), its approach. */
export interface DraftAgent {
  key: number
  agentId: string
  args: string
  approach: string
}

/**
 * The form's draft. On its face: the task, the agents, the Check line and the folder; `yolo` is undefined until the switch is moved (it
 * shows the server's default until then). Under More options: the name, what every agent is told, the lines for a good result, more
 * checks, the project's own checks (`manifestFile`: its conductor.manifest.json verify steps), a review and Brakeman's summary.
 */
export interface DraftComparison {
  task: string
  folder: string
  check: string
  agents: DraftAgent[]
  yolo?: boolean
  name: string
  told: string
  criteria: Array<{ key: number; text: string }>
  checks: DraftCheck[]
  manifestFile: boolean
  reviews: boolean
  reviewAgent: string
  brakeman: boolean
}

let keys = 0
/** A key for a new row of the form, never reused on the page. */
export const rowKey = () => ++keys

/** The catalog's fields the form reads. */
export type TrialAgent = { id: string; name: string; allowArgs?: boolean; available?: boolean; adapter?: string; firstMessage?: boolean }

/** The built-in plain shell's id: a shell has no task to try, so a trial never starts with it. */
const SHELL = 'shell'

/**
 * The agents a new trial starts with: two different installed agents, those that take a first message and have an adapter first, then
 * those with an adapter, then any other; never the shell. Two of one when only one is installed, none when none is.
 */
export function startingAgents(agents: readonly TrialAgent[]): string[] {
  const installed = agents.filter((a) => a.id !== SHELL && a.available !== false)
  const rank = (a: TrialAgent) => (a.adapter ? (a.firstMessage ? 0 : 1) : 2)
  const ranked = installed.map((a, i) => ({ a, i })).sort((x, y) => rank(x.a) - rank(y.a) || x.i - y.i).map((x) => x.a.id)
  if (!ranked.length) return []
  return ranked.length === 1 ? [ranked[0]!, ranked[0]!] : ranked.slice(0, 2)
}

/** The agent `Add agent` adds: the first installed one not yet in the trial, else the last one again. */
export function nextAgent(d: Pick<DraftComparison, 'agents'>, agents: readonly TrialAgent[]): string {
  const used = new Set(d.agents.map((a) => a.agentId))
  const installed = agents.filter((a) => a.id !== SHELL && a.available !== false)
  return installed.find((a) => !used.has(a.id))?.id ?? d.agents.at(-1)?.agentId ?? installed[0]?.id ?? ''
}

/** A new draft: the starting agents, one line for a good result, no more checks, no review, no Brakeman. */
export function draftComparison(agents: readonly TrialAgent[] = []): DraftComparison {
  return {
    task: '',
    folder: '',
    check: '',
    agents: startingAgents(agents).map((agentId) => ({ key: rowKey(), agentId, args: '', approach: '' })),
    name: '',
    told: '',
    criteria: [{ key: rowKey(), text: DEFAULT_CRITERION }],
    checks: [],
    manifestFile: false,
    reviews: false,
    reviewAgent: '',
    brakeman: false,
  }
}

/**
 * An agent of the trial moved to another: one that takes no arguments takes none of the arguments typed for the one before, which its
 * form no longer shows, as a crew member's agent change does (CrewMembersTable).
 */
export function withAgent(a: DraftAgent, agentId: string, agents: readonly TrialAgent[]): DraftAgent {
  return agents.find((x) => x.id === agentId)?.allowArgs === false ? { ...a, agentId, args: '' } : { ...a, agentId }
}

/**
 * The draft the form shows as it opens: the one kept since it was last closed (Escape, the X, a click outside), else a new one. A kept
 * draft with no agent yet (the catalog was not read when it was made) takes the starting agents. Only Cancel and a press the server
 * took end a draft.
 */
export function draftOnOpen(kept: DraftComparison | null, agents: readonly TrialAgent[]): DraftComparison {
  if (!kept) return draftComparison(agents)
  if (kept.agents.length) return kept
  return { ...kept, agents: draftComparison(agents).agents }
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

/** The trial's name when none is typed: the task's first line, cut at a word within the name's 60 characters. */
export function nameFromTask(task: string): string {
  const line = (task.trim().split(/\r?\n/)[0] ?? '').trim().replace(/\s+/g, ' ')
  const max = COMPARE_LIMITS.name
  if (chars(line) <= max) return line
  const head = [...line].slice(0, max + 1).join('')
  const at = head.lastIndexOf(' ')
  return (at > 0 ? head.slice(0, at) : [...line].slice(0, max).join('')).trim()
}

/** The Check line as a check: its words as an argv (never a shell line), and the text an open quote leaves. */
function lineCheck(line: string): DraftCheck {
  const c: DraftCheck = { id: CHECK_ID, argv: splitArgs(line) }
  if (hasOpenQuote(line)) c.pending = line
  return c
}

/** The body the press posts: the fields the route takes and nothing else; the Check line first among the checks; Yolo as shown. */
export function compareSpec(d: DraftComparison, yoloShown: boolean): CompareSpec {
  const goal = d.task.trim()
  const out: CompareSpec = {
    name: d.name.trim() || nameFromTask(goal),
    cwd: d.folder.trim(),
    goal,
    instructions: d.told.trim(),
    criteria: specCriteria(d.criteria),
    candidates: d.agents.map((a) => {
      const o: CompareSpec['candidates'][number] = { agentId: a.agentId }
      const args = splitArgs(a.args)
      if (args.length) o.args = args
      if (a.approach.trim()) o.approach = a.approach.trim()
      return o
    }),
    yolo: yoloShown,
  }
  const checks = [...(d.check.trim() ? [{ id: CHECK_ID, argv: splitArgs(d.check) }] : []), ...d.checks.map(toCrewCheck)]
  if (checks.length) out.checks = checks
  if (d.manifestFile) out.manifest = { file: true }
  if (d.reviews) {
    out.reviews = true
    out.reviewAgent = d.reviewAgent
  }
  if (d.brakeman) out.brakeman = true
  return out
}

/**
 * Where a problem is shown: a field of the face (task, agents, `agent-<i>` the chip of agent i, check, folder) or one inside More
 * options (name, told, criteria, checks, `args-<i>` and `approach-<i>` of agent i, reviews).
 */
export type TrialField = 'task' | 'agents' | 'check' | 'folder' | 'name' | 'told' | 'criteria' | 'checks' | 'reviews' | `agent-${number}` | `args-${number}` | `approach-${number}`

/** A problem that stops the press: its field and its words. */
export interface TrialProblem {
  field: TrialField
  words: string
}

/** Whether a field is inside More options, which a press with a problem there opens. */
export function inMore(field: TrialField): boolean {
  return !['task', 'agents', 'check', 'folder'].includes(field) && !field.startsWith('agent-')
}

/** Agent A, B…: the trial's agents by their letter. */
export const agentLetter = (i: number) => String.fromCharCode(65 + i)

/** A check's problem in the form's words: a sentence of its own, ending in a full stop. */
const sentence = (s: string) => {
  const t = s.charAt(0).toUpperCase() + s.slice(1)
  return /[.!?]$/.test(t) ? t : `${t}.`
}

/**
 * What stops the press, field by field in the form's order, each with its words: the server's bounds as far as a page can know them (it
 * decides; its refusal is shown as it comes). `agents` the catalog.
 */
export function compareProblems(d: DraftComparison, agents: readonly TrialAgent[]): TrialProblem[] {
  const out: TrialProblem[] = []
  const add = (field: TrialField, words: string) => out.push({ field, words })
  const task = d.task.trim()
  if (!task) add('task', 'Say what each agent should do.')
  else if (chars(task) > COMPARE_LIMITS.goal) add('task', `The task is at most ${COMPARE_LIMITS.goal} characters.`)

  if (d.agents.length < COMPARE_LIMITS.minCandidates || d.agents.length > COMPARE_LIMITS.candidates)
    add('agents', `A trial has ${COMPARE_LIMITS.minCandidates} to ${COMPARE_LIMITS.candidates} agents.`)
  d.agents.forEach((a, i) => {
    const agent = agents.find((x) => x.id === a.agentId)
    if (!a.agentId) add(`agent-${i}`, 'Pick an agent.')
    else if (!agent) add(`agent-${i}`, `Agent ${agentLetter(i)}: ${a.agentId} is not on this server. Pick another.`)
  })

  if (d.check.trim()) {
    for (const p of checkProblems(lineCheck(d.check), 'Check')) {
      add('check', p.includes('still being typed') ? 'Close the quote in the check.' : sentence(p.replace(/^Check: /, '')))
    }
  } else if (!d.checks.length && !d.manifestFile) add('check', 'Give a command that tests each result, such as npm test.')

  if (!d.folder.trim()) add('folder', 'Pick the folder.')

  d.agents.forEach((a, i) => {
    const agent = agents.find((x) => x.id === a.agentId)
    const what = `Agent ${agentLetter(i)}`
    if (a.args.trim() && agent?.allowArgs === false) add(`args-${i}`, `${what}: ${agent.name} takes no arguments.`)
    else if (hasOpenQuote(a.args)) add(`args-${i}`, `${what}: close the quote in its arguments.`)
    if (chars(a.approach.trim()) > COMPARE_LIMITS.approach) add(`approach-${i}`, `${what}: the approach is at most ${COMPARE_LIMITS.approach} characters.`)
  })

  if (chars(d.told) > COMPARE_LIMITS.instructions) add('told', `At most ${COMPARE_LIMITS.instructions} characters.`)
  if (d.criteria.length < COMPARE_LIMITS.minCriteria || d.criteria.length > COMPARE_LIMITS.criteria)
    add('criteria', `${COMPARE_LIMITS.minCriteria} to ${COMPARE_LIMITS.criteria} lines.`)
  d.criteria.forEach((c, i) => {
    const t = c.text.trim()
    if (!t) add('criteria', `Line ${i + 1}: say what a good result does.`)
    else if (chars(t) > COMPARE_LIMITS.criterionText) add('criteria', `Line ${i + 1}: at most ${COMPARE_LIMITS.criterionText} characters.`)
    else if (NOT_ONE_LINE.test(t)) add('criteria', `Line ${i + 1}: one line.`)
  })
  const ids: string[] = d.check.trim() ? [CHECK_ID] : []
  d.checks.forEach((c, i) => {
    for (const p of checkProblems(c, `Check ${i + 2}`, ids)) add('checks', sentence(p))
    ids.push(c.id)
  })
  if (d.reviews && !d.reviewAgent) add('reviews', 'Pick the agent that reviews.')
  if (chars(d.name.trim()) > COMPARE_LIMITS.name) add('name', `At most ${COMPARE_LIMITS.name} characters.`)
  return out
}

/** The note under a Check line that holds a shell's operator as a word of its own: a note, never a stop. */
export function checkNote(line: string): string {
  return /(^|\s)(&&|\|\||\||;)(?=\s|$)/.test(line) ? 'This runs as one command, not in a shell. Add a second check under More options.' : ''
}

/** How long the form waits after the folder last changed before it asks the server about it (as the crew editor does). */
export const FOLDER_CHECK_MS = 300

/**
 * What the form says under Folder when the server refused to look at it (GET /api/git/check's error), beside the folder's own line
 * (FolderAllowLine), which says a folder agents can't use, a missing one and a file: one line under Folder, so nothing for those; for
 * another refusal of the folder (`invalid_cwd`), that agents can't use it, never the server's words, which holds the button; '' for
 * any other error.
 */
export function trialFolderLine(answer: { code?: string; message?: string } | null): string {
  if (answer?.code !== 'invalid_cwd') return ''
  return /allowed root|does not exist|no such file|not a directory/i.test(answer.message ?? '') ? '' : FOLDER_CANT
}

/**
 * What the form says under Folder of the server's answer for a folder it looked at (GET /api/git/check): a trial's agents work on copies,
 * so a folder the press would refuse (a repository with no commit, among others) is said in the server's words, which holds the button
 * as a folder agents may not use does; '' for a folder with a commit, a new folder (whose first commit the press makes), and an answer
 * with no words (an older server).
 */
export function trialCheckLine(check: { message?: string } | null): string {
  const m = check?.message?.trim() ?? ''
  return !m || m === GIT_CAN_WORKTREE || m === GIT_FIRST_COMMIT ? '' : sentence(m)
}

/**
 * The words of a press the server refused, as a sentence, with what the form kept: the name of the server's package it came from
 * (`compare: `, `crew: `) is not a person's word and is left off.
 */
export function notSavedWords(server: string): string {
  const t = server.trim().replace(/^(?:(?:compare|crew): )+/i, '').replace(/[.\s]+$/, '')
  return t ? `${t.charAt(0).toUpperCase()}${t.slice(1)}. What you typed is kept.` : 'What you typed is kept.'
}

/** How many characters of an agent's arguments its chip shows before it cuts them in the middle; the whole line is the chip's title. */
export const CHIP_ARGS = 24

/** An agent's arguments as its chip shows them: the line as typed, its spaces made one, cut in the middle past CHIP_ARGS characters. */
export function chipArgs(line: string): string {
  const t = [...line.trim().replace(/\s+/g, ' ')]
  if (t.length <= CHIP_ARGS) return t.join('')
  const head = Math.ceil((CHIP_ARGS - 1) / 2)
  return `${t.slice(0, head).join('')}…${t.slice(t.length - (CHIP_ARGS - 1 - head)).join('')}`
}

const count = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`

/** The closed More options row: what the person changed inside, in the fold's order; '' when nothing was. */
export function optionsLine(d: DraftComparison): string {
  const criteriaChanged = d.criteria.length !== 1 || d.criteria[0]!.text.trim() !== DEFAULT_CRITERION
  return moreLine([
    d.agents.some((a) => a.approach.trim()) && 'approaches',
    d.agents.some((a) => a.args.trim()) && 'arguments',
    d.told.trim() && 'instructions',
    criteriaChanged && count(d.criteria.length, 'line for a good result', 'lines for a good result'),
    d.checks.length > 0 && count(d.checks.length, 'more check', 'more checks'),
    d.manifestFile && "project's checks",
    d.reviews && 'reviewed',
    d.brakeman && 'summary by Brakeman',
    d.name.trim() && 'named',
  ])
}
