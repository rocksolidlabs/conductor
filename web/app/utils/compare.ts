import type { CandidateReceipt } from '~/composables/useDispatch'
import type { RunInfo, RunTask } from '~/composables/useSessions'
import type { CrewCompare } from './protocol'
import { durationWords } from './dispatchEvidence'
import { ticketPath } from './dispatch'
import { markLook, type MarkKey, type MarkView } from './status'

/*
 * A comparison as the page reads it (Program item CP1, docs/plans/compare-crew.md, PR-4): GET /api/runs/{run}/comparison, any run of
 * the chain, with the keys of internal/crew/comparison.go's JSON tags. The page shows what the server measured and the server's
 * verdicts alone (I5): no word here judges a check, a size or a commit; each says what the reply holds. The measured part is read
 * from `candidates` and `checks` alone (I6); `supporting` (PR-5) and `selection` (PR-6b) are other panels'.
 */

/** A lane's candidate state: running, ready (a standing result), failed, cancelled. */
export type CandidateState = 'running' | 'ready' | 'failed' | 'cancelled'

/** One file a candidate changed against the base. */
export interface FileStat {
  path: string
  status: 'A' | 'D' | 'M' | 'T' | string
  added: number
  removed: number
  binary: boolean
}

/** A candidate's change against the base: counts, and the files (at most 100 listed; `total` the count before the cut). */
export interface DiffSize {
  files: number
  added: number
  removed: number
  filesAdded: number
  filesDeleted: number
  list: FileStat[]
  total: number
  /** Why the diff was not measured ("the diff passes 4 MiB: not measured"). */
  notMeasured?: string
}

/** One module a dependency file added (`from` empty), removed (`to` empty) or moved. */
export interface ModuleChange {
  file: string
  name: string
  from: string
  to: string
}

/** The dependency files a candidate changed and, for go.mod and package.json, the modules. */
export interface DepChanges {
  files: string[]
  modules: ModuleChange[]
  /** The dependency files changed, before the list's cut. */
  total: number
  /** The modules changed, before the list's cut. */
  modulesTotal?: number
}

/** A test or build file a candidate changed. */
export interface ListedFile {
  path: string
  kind: 'test' | 'build' | string
}

export interface FileList {
  files: ListedFile[]
  total: number
}

/** The commits a candidate has in common with another lane's (shared), or whose patch equals one of its (equivalent). */
export interface SharedCommits {
  with: string
  count: number
  commits: string[]
  /** "more than 10,000", "not checked: more than 200 commits". */
  note?: string
}

/** One lane's candidate as the comparison shows it. */
export interface ComparedCandidate {
  task: string
  member: string
  agent: string
  args: string[]
  approach: string
  state: CandidateState | string
  /** The run its attempt ran in (a continuation's imported result keeps its source run). */
  run: string
  attempt: number
  /** The candidate's commit: its standing result, or a failed lane's last submitted commit; '' when it has none. */
  commit: string
  start: string
  selectable: boolean
  attempts: number
  acceptedAnyway: boolean
  size?: DiffSize
  deps?: DepChanges
  verifyFiles?: FileList
  shared: SharedCommits[] | null
  equivalent: SharedCommits[] | null
  manifestChanged: boolean
  /** Git's words when it could not be read for this candidate; the rest of the reply stands. */
  unavailable?: string
}

/** One candidate's run of one check: the ledger's receipt, or `not_run`. */
export interface CheckCell {
  task: string
  attempt: number
  k: number
  /** The receipt's state (started, running, finished, interrupted), or `not_run`. */
  state: string
  exit: number | null
  signal: string
  timedOut: boolean
  durationMs: number
  where: string
  manifest: string
  stable: boolean
  /** `check:<run>/<task>/<n>/<id>/<k>`. */
  receipt: string
  logSha256?: string
  /** Why it did not run: earlier_failed, no_such_step, pending, or ended (its attempt ended before this check). */
  notRun?: string
}

/** One expected check across the candidates, with the server's verdict and its reasons. */
export interface ComparedCheck {
  /** check (a task's) or verify (a manifest's). */
  phase: string
  id: string
  argv: string[]
  argvCut: boolean
  argvDigest: string
  timeoutSeconds: number
  /** One per candidate, in the columns' order. */
  cells: CheckCell[]
  verdict: 'comparable' | 'unknown' | 'not_comparable' | string
  why: string[] | null
}

/** Supporting information, not evidence (PR-5): read here only for the Brakeman request the page follows. */
export interface ComparisonSupporting {
  claims?: Array<{ task: string; attempt: number; summary: string }>
  reviews?: Array<{ task: string; attempt: number; verdict: string; findings: string[] }>
  brakeman?: { request?: string; state?: string; why?: string; brief?: string } | null
}

/** The selection (PR-6b), as the comparison carries it; drawn by its own panel. */
export interface ComparisonSelection {
  decision: string
  state: 'open' | 'answered' | 'applied' | 'superseded' | string
  options: Array<{ label: string; tradeoffs?: string; subject?: { kind: 'candidate'; run: string; task: string; n: number; commit: string } }>
  chosen?: number
  rationale?: string
  by?: string
  for?: { label: string; person?: string; responsibility?: string }
  at?: string
  applied?: { how: 'selected'; selected?: { task: string; run: string; attempt: number; commit: string }; none?: true; words?: string }
}

/** GET /api/runs/{run}/comparison. */
export interface Comparison {
  /** The chain's launched run. */
  run: string
  base: string
  objective: string
  criteria: Array<{ id: string; text: string }>
  candidates: ComparedCandidate[]
  checks: ComparedCheck[]
  settled: boolean
  /** What the comparison cannot see, in the server's words, when it says. */
  independence?: { notObserved?: string }
  supporting?: ComparisonSupporting | null
  selection?: ComparisonSelection | null
  /** running, selecting, answered, selected, none, ended: the server's word for where the comparison is. */
  state?: string
}

/** The words every row of the measured part carries: what is measured, and by whom. */
export const MEASURED_BY = 'measured by Conductor'

/** What independence cannot see, said beside its words always (Isolation, door 14). */
export const NOT_OBSERVED = "a read of another lane's folder, or of what an agent keeps for itself under its HOME, is not observed"
/** What the same-patch check catches and what it cannot. */
export const SAME_PATCHES = 'same patches: checked; a change rewritten by hand is not detected'
/**
 * What independence checks and cannot see, said beside its row always: the server's words when its reply gives them
 * (`independence.notObserved`), after "Same patches: checked."; else the page's own.
 */
export function notObservedWords(server?: string): string {
  const w = server?.trim()
  if (w) return `Same patches: checked. ${w}`
  return `${SAME_PATCHES.charAt(0).toUpperCase()}${SAME_PATCHES.slice(1)}; ${NOT_OBSERVED}.`
}
/** Until PR-7's selection, under the columns. */
export const SELECT_LATER = 'Selecting a candidate arrives with durable decisions'

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`
const short = (c: string) => c.slice(0, 12)

// ---------------------------------------------------------------------------
// Checks
// ---------------------------------------------------------------------------

/** A row's verdict in words: its label, and its reasons as the server gave them. */
export interface VerdictWords {
  /** comparable; not comparable; comparable only if their test changes are fair; else the server's own word. */
  label: string
  /** The words before the reasons: "because". */
  lead: string
  why: string[]
}

/**
 * The verdict of a check's row, as the server gave it (I5: never one the page made): comparable; not comparable, because…;
 * comparable only if their test changes are fair, with the files. A verdict the page does not know is said as the server sent it.
 */
export function verdictWords(c: Pick<ComparedCheck, 'verdict' | 'why'>): VerdictWords {
  const why = (c.why ?? []).filter((w) => !!w)
  switch (c.verdict) {
    case 'comparable':
      return { label: 'comparable', lead: '', why }
    case 'not_comparable':
      return { label: 'not comparable', lead: why.length ? 'because' : '', why }
    case 'unknown': {
      // The server says the label before each candidate's files ("…fair: approach-a changed x_test.go, a test file; approach-b
      // changed none"): said once, each candidate's part a reason of its own.
      const label = 'comparable only if their test changes are fair'
      return { label, lead: '', why: why.flatMap((w) => (w.startsWith(`${label}: `) ? w.slice(label.length + 2).split('; ') : [w])) }
    }
  }
  return { label: c.verdict || 'no verdict given', lead: '', why }
}

/** A check's name on the page: "check unit", "verify lint". */
export function checkName(c: Pick<ComparedCheck, 'phase' | 'id'>): string {
  return `${c.phase} ${c.id}`
}

/** A check's command as recorded, cut where the server cut it. */
export function checkCommand(c: Pick<ComparedCheck, 'argv' | 'argvCut'>): string {
  return `${c.argv.join(' ')}${c.argvCut ? ' …' : ''}`
}

/** A cell in words: its mark, its word ("passed", "failed with exit 1", "not run: an earlier check failed"), and its duration and place. */
export interface CellWords {
  mark: MarkView
  word: string
  /** "4.2 s · native": how long it took and where it ran; '' for a cell that did not run. */
  detail: string
  /** passed, failed, running, interrupted, not_run: what the cell's hook says. */
  state: 'passed' | 'failed' | 'running' | 'interrupted' | 'not_run' | 'other'
}

const CELL_LEGEND = {
  passed: 'passed: the check ran on the candidate and exited 0, its tree left as it found it',
  failed: 'failed: the check ran on the candidate and did not pass',
  running: 'checking: the check is running on the candidate',
  interrupted: 'interrupted: the check was cut short and has no outcome',
  cancelled: 'cancelled: the check was ended before it finished',
  notRun: 'not run: the candidate has no receipt of this check',
}

function cellMark(key: MarkKey, word: string, legend: string): MarkView {
  return { key, word, legend }
}

/**
 * Why a cell did not run, in words: an earlier check failed (a tree stops at its first failure), its manifest has no such step, its
 * checks are under way, or its attempt ended before this check (stopped, interrupted, or failed before its checks ran).
 */
export function notRunWords(why: string | undefined): string {
  if (why === 'earlier_failed') return 'not run: an earlier check failed'
  if (why === 'no_such_step' || why === 'no_step') return 'not run: its manifest has no such step'
  if (why === 'pending') return 'not run yet: its checks are under way'
  if (why === 'ended') return 'not run: its attempt ended before this check'
  return 'not run'
}

/**
 * A check's cell: passed, failed with its exit (or its signal), timed out, changed the tree, running, interrupted, or not run and why;
 * with its duration and where it ran. The mark is a task's own (TASK_LOOK): passed the neutral check, failed the red cross, interrupted
 * the dash, not run the square, running the working mark with "checking" (taskStatus's verifying). No colour is the page's own.
 */
export function cellWords(c: CheckCell): CellWords {
  const detail = c.state === 'not_run' ? '' : [durationWords(c.durationMs > 0 || c.state === 'finished' ? c.durationMs : undefined), c.where].filter(Boolean).join(' · ')
  if (c.state === 'not_run') {
    const word = notRunWords(c.notRun)
    return { mark: cellMark('waiting', 'not run', CELL_LEGEND.notRun), word, detail, state: 'not_run' }
  }
  if (c.state === 'started' || c.state === 'running') return { mark: cellMark('working', 'checking', CELL_LEGEND.running), word: 'running', detail, state: 'running' }
  if (c.state === 'interrupted') return { mark: cellMark('cancelled', 'interrupted', CELL_LEGEND.interrupted), word: 'interrupted', detail, state: 'interrupted' }
  if (c.state === 'cancelled') return { mark: cellMark('cancelled', 'cancelled', CELL_LEGEND.cancelled), word: 'cancelled', detail, state: 'interrupted' }
  if (c.state === 'finished') {
    const failed = (word: string): CellWords => ({ mark: cellMark('failed', 'failed', CELL_LEGEND.failed), word, detail, state: 'failed' })
    if (c.timedOut) return failed('timed out')
    if (c.signal) return failed(`failed with signal ${c.signal}`)
    if (c.exit !== null && c.exit !== undefined && c.exit !== 0) return failed(`failed with exit ${c.exit}`)
    if (!c.stable) return failed('changed the tree it checked')
    return { mark: cellMark('done', 'passed', CELL_LEGEND.passed), word: 'passed', detail, state: 'passed' }
  }
  return { mark: cellMark('waiting', c.state || 'unknown', CELL_LEGEND.notRun), word: c.state || 'unknown', detail, state: 'other' }
}

/** A cell's receipt ref read: `check:<run>/<task>/<n>/<id>/<k>`; null for none or another shape. */
export function parseCheckRef(ref: string | undefined): { run: string; task: string; attempt: number; id: string; k: number } | null {
  const m = /^check:([^/]+)\/([^/]+)\/([1-9][0-9]*)\/([^/]+)\/([1-9][0-9]*)$/.exec(ref ?? '')
  return m ? { run: m[1]!, task: m[2]!, attempt: Number(m[3]), id: m[4]!, k: Number(m[5]) } : null
}

/**
 * What DS2's receipt dialog (DispatchReceipt) opens for a cell: the run, task, attempt and commit its receipt is of, and the receipt as
 * the candidates route shapes one, from what the cell carries. Its argv and its log are read by the dialog itself (useReceiptRead), from
 * the attempt's own receipts. Null for a cell with no receipt (not run).
 */
export function cellReceipt(check: Pick<ComparedCheck, 'phase' | 'id'>, cell: CheckCell, candidate: Pick<ComparedCandidate, 'run' | 'commit'> | undefined): { run: string; task: string; attempt: number; commit: string; receipt: CandidateReceipt } | null {
  if (cell.state === 'not_run') return null
  const ref = parseCheckRef(cell.receipt)
  const run = ref?.run ?? candidate?.run ?? ''
  if (!run) return null
  const phase = check.phase === 'verify' ? 'verify' : 'check'
  return {
    run,
    task: ref?.task ?? cell.task,
    attempt: ref?.attempt ?? cell.attempt,
    commit: candidate?.commit ?? '',
    receipt: {
      phase,
      id: ref?.id ?? check.id,
      k: ref?.k ?? cell.k,
      where: cell.where,
      state: cell.state as CandidateReceipt['state'],
      ...(cell.exit !== null && cell.exit !== undefined ? { exit: cell.exit } : {}),
      ...(cell.signal ? { signal: cell.signal } : {}),
      timedOut: cell.timedOut,
      killed: false,
      stable: cell.stable,
      startedAt: '',
      ...(cell.logSha256 ? { logSha256: cell.logSha256 } : {}),
      logBytes: 0,
      logDropped: false,
    },
  }
}

// ---------------------------------------------------------------------------
// Candidates
// ---------------------------------------------------------------------------

const CANDIDATE_LEGEND = {
  working: 'running: the candidate is being worked',
  ready: 'ready: the candidate has a standing result',
  failed: 'failed: the lane ended with no result that stands',
  cancelled: 'cancelled: the lane was called off',
}

/**
 * A candidate column's state (the one status language): running is the working mark, ready (a standing result) the neutral check,
 * failed the red cross ("failed: not selectable" when its last submitted commit is shown), cancelled the dash. A column whose lane's
 * task the live store holds draws that task's own taskStatus instead (the page's).
 */
export function candidateMark(c: Pick<ComparedCandidate, 'state' | 'commit' | 'selectable'>): MarkView {
  switch (c.state) {
    case 'running':
      return { key: 'working', word: 'running', legend: CANDIDATE_LEGEND.working }
    case 'ready':
      return { key: 'done', word: 'ready', legend: CANDIDATE_LEGEND.ready }
    case 'failed':
      return { key: 'failed', word: c.commit && !c.selectable ? 'failed: not selectable' : 'failed', legend: CANDIDATE_LEGEND.failed }
    case 'cancelled':
      return { key: 'cancelled', word: 'cancelled', legend: CANDIDATE_LEGEND.cancelled }
  }
  return { key: 'waiting', word: c.state || 'unknown', legend: markLook('waiting').legend }
}

/** A candidate's commit in words: "4b825dc0e3f1 (standing result)", "… failed: not selectable", "no result yet". */
export function candidateCommitWords(c: Pick<ComparedCandidate, 'commit' | 'selectable' | 'state' | 'acceptedAnyway'>): string {
  if (!c.commit) return c.state === 'running' ? 'no result yet' : 'no result'
  if (!c.selectable) return 'last submitted commit · failed: not selectable'
  return c.acceptedAnyway ? 'standing result · accepted anyway' : 'standing result'
}

/** "attempt 2 of 3 made", the candidate's attempts along the chain. */
export function attemptsWords(c: Pick<ComparedCandidate, 'attempt' | 'attempts'>): string {
  if (!c.attempts) return 'no attempt yet'
  return c.attempt ? `attempt ${c.attempt} shown · ${plural(c.attempts, 'attempt')} made` : plural(c.attempts, 'attempt') + ' made'
}

// ---------------------------------------------------------------------------
// The measured rows
// ---------------------------------------------------------------------------

/** A changed file's status in words. */
export function fileStatusWord(s: string): string {
  return s === 'A' ? 'added' : s === 'D' ? 'deleted' : s === 'T' ? 'type changed' : 'changed'
}

/** The size of a candidate's change in words. */
export interface SizeWords {
  /** "12 files changed, +340 −90". */
  lines: string
  /** "2 added, 1 deleted"; '' when none was. */
  files: string
  /** "and 4 more files not listed"; '' when every file is. */
  more: string
}

/** A candidate's change against the base: files changed, lines added and removed, files added and deleted, and what the list leaves out. */
export function diffSizeWords(s: DiffSize | undefined): SizeWords | null {
  if (!s) return null
  if (s.notMeasured) return { lines: s.notMeasured, files: '', more: '' }
  const lines = `${plural(s.files, 'file')} changed, +${s.added} −${s.removed}`
  const parts = [s.filesAdded ? `${s.filesAdded} added` : '', s.filesDeleted ? `${s.filesDeleted} deleted` : ''].filter(Boolean)
  const left = Math.max(0, (s.total || s.files) - (s.list?.length ?? 0))
  return { lines, files: parts.join(', '), more: left ? `and ${plural(left, 'more file')} not listed` : '' }
}

/** A file's line in the size's list: "+12 −3", "binary". */
export function fileLines(f: FileStat): string {
  return f.binary ? 'binary' : `+${f.added} −${f.removed}`
}

/** One module's change: "added golang.org/x/text v0.3.0", "removed left-pad (was 1.3.0)", "moved vue from 3.4.0 to 3.5.1". */
export function moduleWords(m: ModuleChange): string {
  if (!m.from && m.to) return `added ${m.name} ${m.to}`
  if (m.from && !m.to) return `removed ${m.name} (was ${m.from})`
  if (m.from && m.to) return `moved ${m.name} from ${m.from} to ${m.to}`
  return `${m.name} changed`
}

/** A candidate's dependency changes in words: none, or the files and each module of go.mod and package.json. */
export interface DepsWords {
  /** "No dependency file changed", "2 dependency files changed". */
  summary: string
  files: string[]
  modules: Array<{ file: string; words: string }>
  more: string
}

export function depsWords(d: DepChanges | undefined): DepsWords {
  const files = d?.files ?? []
  const modules = (d?.modules ?? []).map((m) => ({ file: m.file, words: moduleWords(m) }))
  if (!files.length && !modules.length) return { summary: 'No dependency file changed', files: [], modules: [], more: '' }
  const total = Math.max(d?.total ?? 0, files.length)
  const filesLeft = total - files.length
  const modulesLeft = Math.max(0, (d?.modulesTotal ?? 0) - modules.length)
  const more = [filesLeft ? plural(filesLeft, 'more file') : '', modulesLeft ? plural(modulesLeft, 'more module') : ''].filter(Boolean).join(' and ')
  return { summary: `${plural(total, 'dependency file')} changed`, files, modules, more: more ? `and ${more} not listed` : '' }
}

/** A test or build file in words: "internal/cache/cache_test.go, a test file". */
export function verifyFileWords(f: ListedFile): string {
  return `${f.path}, ${f.kind === 'build' ? 'a build file' : f.kind === 'test' ? 'a test file' : `a ${f.kind} file`}`
}

/** A candidate's changed test and build files: "approach-a changed none", or each file with its kind, and what is left out. */
export function verifyFilesWords(task: string, l: FileList | undefined): { summary: string; files: string[]; more: string } {
  const files = (l?.files ?? []).map(verifyFileWords)
  if (!files.length) return { summary: `${task} changed no test or build file`, files: [], more: '' }
  const left = Math.max(0, (l?.total ?? 0) - files.length)
  return { summary: `${task} changed ${plural(l?.total || files.length, 'test or build file', 'test or build files')}`, files, more: left ? `and ${left} more not listed` : '' }
}

/**
 * A candidate's independence in words (I4: observed, never assumed): "no shared commit and no same patch", or each lane it shares
 * commits with ("shares 2 commits with approach-b") and each whose patches one of its commits carries ("1 commit carries the same change
 * as one of approach-b's"), each with the server's note ("more than 10,000", "not checked: more than 200 commits"). What it cannot see
 * is said beside it, always (NOT_OBSERVED, SAME_PATCHES).
 */
export function independenceWords(c: Pick<ComparedCandidate, 'shared' | 'equivalent'>): string[] {
  const out: string[] = []
  // The server's note, but the same-patch check's own words, which the row says once beside it (notObservedWords).
  const note = (n: string | undefined) => (n && !n.startsWith('same patches') ? ` (${n})` : '')
  for (const s of c.shared ?? []) {
    if (s.count > 0) out.push(`shares ${plural(s.count, 'commit')} with ${s.with}${note(s.note)}`)
    else if (s.note?.startsWith('more than')) out.push(`shares ${s.note} commits with ${s.with}`)
    else if (s.note) out.push(`commits shared with ${s.with}: ${s.note}`)
  }
  for (const e of c.equivalent ?? []) {
    if (e.count > 0) out.push(`${plural(e.count, 'commit')} ${e.count === 1 ? 'carries' : 'carry'} the same change as one of ${e.with}'s${note(e.note)}`)
    else if (e.note) out.push(`same patches as ${e.with}: ${e.note}`)
  }
  return out.length ? out : ['no shared commit and no same patch']
}

/** The commits independence names, short, for their titles. */
export function independenceCommits(c: Pick<ComparedCandidate, 'shared' | 'equivalent'>): string[] {
  return [...(c.shared ?? []), ...(c.equivalent ?? [])].flatMap((s) => s.commits ?? []).map(short)
}

// ---------------------------------------------------------------------------
// A comparison's run: its lanes, and the way to it
// ---------------------------------------------------------------------------

/** The run read's `compare` part (C3): the chain's launched run and its candidates' tasks; absent for a run of no comparison. */
export interface RunCompareInfo {
  launched?: string
  candidates: Array<string | { task: string; review?: string; approach?: string }>
}

/** A run's `compare` part, if it has one of the shape the page reads. */
export function runCompare(run: unknown): RunCompareInfo | undefined {
  const c = (run as { compare?: unknown } | null | undefined)?.compare as RunCompareInfo | undefined
  if (!c || typeof c !== 'object' || !Array.isArray(c.candidates) || !c.candidates.length) return undefined
  return c
}

/** One lane of a comparison's run: its candidate's task, the review of it when the crew has one, and its approach where known. */
export interface RunLane {
  task: string
  review?: string
  approach?: string
}

/**
 * A comparison's lanes, in the block's order: from the run read's `compare` part (C3), else from the crew's compare block; each
 * candidate's review is the review task whose one need is it. Empty for a run of no comparison.
 */
export function lanesOf(run: Pick<RunInfo, 'tasks'> & { compare?: unknown }, block?: Pick<CrewCompare, 'candidates'> | null): RunLane[] {
  const part = runCompare(run)
  const named = part ? part.candidates.map((c) => (typeof c === 'string' ? { task: c } : c)) : (block?.candidates ?? []).map((c) => ({ task: c.task, approach: c.approach }))
  const approach = new Map((block?.candidates ?? []).map((c) => [c.task, c.approach]))
  const tasks = run.tasks ?? []
  return named
    .filter((c) => !!c.task)
    .map((c) => {
      const review = (c as { review?: string }).review ?? tasks.find((t) => t.kind === 'review' && t.needs?.length === 1 && t.needs[0] === c.task)?.id
      const a = (c as { approach?: string }).approach ?? approach.get(c.task)
      return { task: c.task, ...(review ? { review } : {}), ...(a ? { approach: a } : {}) }
    })
}

/** A run's tasks grouped by lane: each lane's candidate, then its review; then the tasks of no lane, in the crew's order. */
export function tasksByLane(tasks: readonly RunTask[], lanes: readonly RunLane[]): { groups: Array<{ lane: RunLane; tasks: RunTask[] }>; rest: RunTask[] } {
  const taken = new Set<string>()
  const groups = lanes.map((lane) => {
    const mine = [lane.task, lane.review].map((id) => tasks.find((t) => t.id === id)).filter((t): t is RunTask => !!t)
    for (const t of mine) taken.add(t.id)
    return { lane, tasks: mine }
  })
  return { groups, rest: tasks.filter((t) => !taken.has(t.id)) }
}

/** The ticket page's compare view of a run (C2): `/dispatch/<project>/<n>?view=compare&run=<run>`; null without a ticket. */
export function comparisonLink(ticketKey: string | undefined, run: string): { path: string; query: { view: 'compare'; run: string } } | null {
  const path = ticketKey ? ticketPath(ticketKey) : ''
  return path && run ? { path, query: { view: 'compare', run } } : null
}

/** The runs a comparison is of: the run asked for, the chain's launched run, each candidate's run. A change of any reads it again. */
export function comparisonRuns(asked: string, c: Pick<Comparison, 'run' | 'candidates'> | null): string[] {
  return [...new Set([asked, c?.run ?? '', ...(c?.candidates ?? []).map((x) => x.run)].filter(Boolean))]
}
