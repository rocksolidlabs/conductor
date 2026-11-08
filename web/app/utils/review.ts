import { cutBytes } from './quote'
import { shortCommit } from './dispatchPlans'

/**
 * A person's review of one candidate (Program item RV1, docs/plans/review-repair.md, "web/app"): a note made from a selection in the
 * candidate's diff, the batch's checks and its measured size as the run would keep it (FitChangeRequest's first measure in
 * internal/crew, held to the same fixture, internal/crew/testdata/change-request-payload.json), the words of a review's application and
 * of where a finding's lines are, a finding's state from its marks, and the bases a candidate's diff may be read against. Pure.
 */

export type NoteKind = 'line' | 'file' | 'general'
export type NoteIntent = 'change' | 'question' | 'note'
export type Severity = 'blocker' | 'major' | 'minor' | 'nit'
export type Side = 'candidate' | 'base'
export type Verdict = 'request_changes' | 'comment' | 'approve'

export const SEVERITIES: readonly Severity[] = ['blocker', 'major', 'minor', 'nit']
export const INTENTS: readonly NoteIntent[] = ['change', 'question', 'note']
export const VERDICTS: readonly Verdict[] = ['request_changes', 'comment', 'approve']

/** The store's bounds of a review (internal/dispatch/model.go), the review task's: fifty notes, their words, twelve lines of 200 bytes. */
export const MAX_REVIEW_FINDINGS = 50
export const MAX_REVIEW_SUMMARY = 4000
export const MAX_FINDING_MESSAGE = 1000
export const MAX_FINDING_RESOLUTION = 1000
export const MAX_FINDING_PATH = 1024
export const MAX_ANCHOR_LINES = 12
export const MAX_ANCHOR_LINE = 200
/** What the run keeps of a repair at most (the ledger's MaxPayload): a request past it is refused whole. */
export const MAX_REPAIR_PAYLOAD = 64 << 10
/** A Dispatch id's length: the request is measured with ids of it. */
const ID_LEN = 16

/** A note of the draft: where it is (a line range on one side of a file, a whole file, the candidate as a whole), what it asks and its words. */
export interface DraftNote {
  /** The draft's own name for it, never sent. */
  key: string
  kind: NoteKind
  intent: NoteIntent
  /** A change's alone. */
  severity?: Severity
  path?: string
  side?: Side
  from?: number
  to?: number
  /** The lines as the person saw them: at most 12, each cut to 200 bytes. */
  lines?: string[]
  message: string
  resolution?: string
}

/** What the route takes of a note (`findings[]` of POST /api/dispatch/tickets/{key}/reviews): its empty members left out. */
export interface NewFinding {
  kind: NoteKind
  intent: NoteIntent
  severity?: Severity
  path?: string
  side?: Side
  from?: number
  to?: number
  lines?: string[]
  message?: string
  resolution?: string
}

/** A selection in the diff (DiffEditor's `select`): the side, the lines from..to (1-based, whole lines), their text, and where to put the bar. */
export interface DiffSelection {
  side: Side
  from: number
  to: number
  lines: string[]
  top: number
  left: number
  /** Ctrl+Shift+M asked for the note's form at once. */
  open?: boolean
}

/** A finding's lines marked in the diff's margin. */
export interface DiffMark {
  side: Side
  from: number
  to: number
  label?: string
}

const enc = new TextEncoder()
const bytes = (s: string) => enc.encode(s).length
const chars = (s: string) => [...s].length

/** A line as the store keeps it: a control character other than a tab (a CR of a CRLF file among them) is one it refuses, so it is made U+FFFD. */
function anchorLine(s: string): string {
  // eslint-disable-next-line no-control-regex
  return cutBytes(s.replace(/[\u0000-\u0008\u000a-\u001f\u007f-\u009f]/g, '�'), MAX_ANCHOR_LINE)
}

/**
 * A note on lines from..to of one side of a file, from the file's lines as the diff shows them: the range in order and within the
 * file, the lines kept the way the store keeps them (the first 12 of the range, each cut to 200 bytes, as `makeQuote` cuts a quote).
 */
export function noteFromSelection(path: string, side: Side, fileLines: readonly string[], from: number, to: number): Omit<DraftNote, 'key' | 'intent' | 'message'> {
  const last = Math.max(fileLines.length, 1)
  const lo = Math.min(Math.max(1, Math.min(from, to)), last)
  const hi = Math.max(lo, Math.min(Math.max(from, to), last))
  const lines = fileLines.slice(lo - 1, hi).slice(0, MAX_ANCHOR_LINES).map(anchorLine)
  return { kind: 'line', path, side, from: lo, to: hi, lines: lines.length ? lines : [''] }
}

/** A note as the route takes it: a change's severity alone, a line's range and lines alone, a located note's path and side alone. */
export function findingOf(n: DraftNote): NewFinding {
  const f: NewFinding = { kind: n.kind, intent: n.intent }
  if (n.intent === 'change' && n.severity) f.severity = n.severity
  if (n.kind !== 'general') {
    if (n.path) f.path = n.path
    if (n.side) f.side = n.side
  }
  if (n.kind === 'line') {
    f.from = n.from
    f.to = n.to
    f.lines = [...(n.lines ?? [])]
  }
  const message = n.message.trim()
  if (message) f.message = message
  const resolution = n.intent === 'change' ? (n.resolution ?? '').trim() : ''
  if (resolution) f.resolution = resolution
  return f
}

/** A review's body as sent, beside the operation's members: the candidate, the base read against, the verdict, the summary, the notes. */
export interface ReviewBody {
  candidate: string
  base: string
  verdict: Verdict
  summary?: string
  via: 'web'
  findings: NewFinding[]
}

export function reviewBody(candidate: string, base: string, verdict: Verdict, summary: string, notes: readonly DraftNote[]): ReviewBody {
  const s = summary.trim()
  return { candidate, base, verdict, ...(s ? { summary: s } : {}), via: 'web', findings: notes.map(findingOf) }
}

/** A person's change request as Yardmaster's repair carries it (crew.ChangeRequest), each member left out where Go's omitempty leaves it. */
export interface ChangeRequest {
  review: string
  by: string
  via?: string
  base?: string
  summary?: string
  changes: Array<NewFinding & { id: string }>
}

/** The request the review makes of the engine, as the server measures it before anything is kept: its ids placeholders of an id's length. */
export function changeRequestOf(body: ReviewBody, by: string): ChangeRequest {
  return {
    review: '0'.repeat(ID_LEN),
    by,
    via: body.via,
    ...(body.base ? { base: body.base } : {}),
    ...(body.summary ? { summary: body.summary } : {}),
    changes: body.findings.map((f, i) => ({ id: (i + 1).toString(16).padStart(ID_LEN, '0'), ...f })),
  }
}

/**
 * The length of v as Go's encoding/json writes it: JSON.stringify's, but with `<`, `>`, `&`, U+2028 and U+2029 escaped as six bytes
 * each, as Go's encoder escapes them for HTML (a control character is escaped the same way by both).
 */
export function goJSONBytes(v: unknown): number {
  const s = JSON.stringify(v)
  let n = bytes(s)
  for (const ch of s) {
    if (ch === '<' || ch === '>' || ch === '&') n += 5
    else if (ch === ' ' || ch === ' ') n += 3
  }
  return n
}

/**
 * FitChangeRequest's first measure (internal/crew, repairBytes): the repair the request makes as the task's row keeps it, at its
 * largest (an attempt of 1,048,576, a commit and a start of 64 hex digits), the summary the repair's and not the request's. The server
 * has the last word, since only it knows the task's own text (the second measure, the repair attempt's task file).
 */
export function repairBytes(cr: ChangeRequest): number {
  const { summary, ...request } = cr
  const zeros = '0'.repeat(64)
  return goJSONBytes({ review: cr.review, attempt: 1 << 20, commit: zeros, start: zeros, ...(summary ? { summary } : {}), request })
}

/** n bytes in KiB, rounded up, as the server says it. */
export const kib = (n: number) => Math.ceil(n / 1024)

/** Where a note is, in words: "stub.txt:3–4 (base)", "stub.txt (the whole file)", "the candidate as a whole". */
export function noteWhere(n: Pick<DraftNote, 'kind' | 'path' | 'side' | 'from' | 'to'>): string {
  if (n.kind === 'general') return 'the candidate as a whole'
  const side = n.side === 'base' ? ' (base)' : ''
  if (n.kind === 'file') return `${n.path} (the whole file)${side}`
  const range = (n.to ?? 0) > (n.from ?? 0) ? `${n.from}–${n.to}` : `${n.from}`
  return `${n.path}:${range}${side}`
}

/**
 * What stops the batch from being sent, in words, the store's and the engine's rules checked here first: a note or a summary, at most
 * fifty notes, a request with a change in it, each text within its bound, and a request within what the run keeps (64 KiB). Empty when
 * it may be sent; the server checks all of it again, and the task file's size, which only it can measure.
 */
export function batchProblems(body: ReviewBody, by: string): string[] {
  const out: string[] = []
  if (!body.findings.length && !body.summary) out.push('Add a note or a summary first.')
  if (body.findings.length > MAX_REVIEW_FINDINGS) out.push(`A review holds at most ${MAX_REVIEW_FINDINGS} notes: this one has ${body.findings.length}.`)
  if (body.verdict === 'request_changes' && !body.findings.some((f) => f.intent === 'change')) out.push('Request changes needs at least one note that asks for a change.')
  if (body.summary && chars(body.summary) > MAX_REVIEW_SUMMARY) out.push(`The summary is more than ${MAX_REVIEW_SUMMARY} characters.`)
  body.findings.forEach((f, i) => {
    const what = `Note ${i + 1}`
    if (!f.message) out.push(`${what} has no words.`)
    if (f.message && chars(f.message) > MAX_FINDING_MESSAGE) out.push(`${what}'s words are more than ${MAX_FINDING_MESSAGE} characters.`)
    if (f.resolution && chars(f.resolution) > MAX_FINDING_RESOLUTION) out.push(`${what}'s resolution is more than ${MAX_FINDING_RESOLUTION} characters.`)
    if (f.path && bytes(f.path) > MAX_FINDING_PATH) out.push(`${what}'s path is more than ${MAX_FINDING_PATH} bytes.`)
  })
  if (body.verdict === 'request_changes') {
    const n = repairBytes(changeRequestOf(body, by))
    if (n > MAX_REPAIR_PAYLOAD) out.push(`The notes are ${kib(n)} KiB as the run keeps them, more than ${MAX_REPAIR_PAYLOAD >> 10} KiB: shorten them.`)
  }
  return out
}

/** A review's application as the store records it (`application` of a review). */
export interface ReviewApplication {
  state: 'none' | 'pending' | 'applied' | 'refused'
  try?: number
  run?: string
  command?: string
  revision?: number
  attempt?: number
  why?: string
  at?: string
}

/** A review's application in words: its title, what follows it, and its tone. `task` is the candidate's task, which the repair reopens. */
export function applicationWords(a: ReviewApplication, task: string, commit: string): { title: string; detail: string; tone: 'success' | 'warning' | 'error' | 'muted' } {
  switch (a.state) {
    case 'applied':
      return { title: 'Applied by Yardmaster', detail: `In run ${a.run}: ${task} reopened at ${shortCommit(commit)}, its attempt ${a.attempt} takes up the request (command ${a.command}, revision ${a.revision}).`, tone: 'success' }
    case 'refused':
      return { title: 'Refused by Yardmaster', detail: a.why || 'Nothing was changed.', tone: 'error' }
    case 'pending':
      return { title: 'Waiting on Yardmaster', detail: a.why || 'Yardmaster took the request; the ledger has not recorded it yet.', tone: 'warning' }
    default:
      return { title: 'Recorded', detail: 'A comment or an approval changes no run: it is kept in Dispatch beside the candidate.', tone: 'muted' }
  }
}

/** Where a finding's lines are on a candidate, as the server located them (`located` of a finding). */
export interface Located {
  state: 'same' | 'moved' | 'changed' | 'file_gone' | 'renamed' | 'unknown'
  from?: number
  to?: number
  path?: string
  located?: Located
  why?: string
}

/** A located state in words: "same lines", "moved: now 41–44", "the lines changed", "the file is gone", "renamed to …", "not located: …". */
export function locatedWords(l: Located | null | undefined): string {
  if (!l) return ''
  const range = (a?: number, b?: number) => ((b ?? 0) > (a ?? 0) ? `${a}–${b}` : `${a}`)
  switch (l.state) {
    case 'same':
      return 'same lines'
    case 'moved':
      return `moved: now ${range(l.from, l.to)}`
    case 'changed':
      return 'the lines changed'
    case 'file_gone':
      return 'the file is gone'
    case 'renamed': {
      const inner = l.located && l.located.state !== 'renamed' ? locatedWords(l.located) : ''
      return `renamed to ${l.path}${inner ? `, ${inner}` : ''}`
    }
    default:
      return `not located${l.why ? `: ${l.why}` : ''}`
  }
}

/** A finding's state: its latest mark's verdict (the marks oldest first, the store's one order), open with none. A located state never decides it. */
export function findingState(marks: ReadonlyArray<{ verdict: 'resolved' | 'open' | 'withdrawn' }>): 'resolved' | 'open' | 'withdrawn' {
  return marks.length ? marks[marks.length - 1]!.verdict : 'open'
}

/** A candidate the Review view's picker offers: its reference, its words, whether it stands as its task's result. */
export interface ReviewCandidateOption {
  value: string
  label: string
  standing: boolean
}

/** A base a candidate's diff may be read against: the commit, and what it is in words. */
export interface BaseChoice {
  commit: string
  label: string
}

/**
 * The bases the server takes for a candidate's diff (the diff route's rule): the run's base, where the attempt started, and the result
 * of each other attempt of the same task the page lists, each once (the first attempt of a task with no input starts at the run's base).
 */
export function offeredBases(runBase: string | undefined, c: { attempt: number; start?: string; commit?: string }, sameTask: ReadonlyArray<{ attempt: number; commit?: string }>): BaseChoice[] {
  const out: BaseChoice[] = []
  const add = (commit: string | undefined, label: string) => {
    if (!commit || commit === c.commit) return
    const had = out.find((b) => b.commit === commit)
    if (had) had.label += `, ${label}`
    else out.push({ commit, label })
  }
  add(runBase, "the run's base")
  add(c.start, `where attempt ${c.attempt} started`)
  for (const o of sameTask) if (o.attempt !== c.attempt) add(o.commit, `attempt ${o.attempt}'s result`)
  return out
}
