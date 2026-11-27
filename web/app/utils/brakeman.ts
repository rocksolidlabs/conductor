// Brakeman on the app's pages (docs/plans/brakeman.md, "web/app: Settings, the Brakeman page and a brief"): the records as
// GET /api/brakeman and its requests answer them (docs/protocol.md, Brakeman), the words the pages say of them, where a ref leads
// and what it is called, the order a brief's items are shown in, and the configuration form's checks, which mirror the server's
// (internal/brakeman/config.go, Validate) so the form says what is wrong before a save is refused for it. Pure: no Nuxt, no fetch.

import { capacityStateWords, clockWords, countsWords, creditWords, limitsWords, planWords, warningOf, windowWords, type CapacityCounts, type CapacityCredit, type CapacityLimits, type CapacityWindow } from './capacity'

/** What Brakeman may see, a switch a class. */
export interface BrakemanVisibility {
  runs: boolean
  logs: boolean
  dispatch: boolean
  sessions: boolean
  touched: boolean
  terminals: boolean
  /** The capacity view (docs/plans/capacity.md, PR-6): on for a new configuration, absent (off) in one saved before it. */
  capacity?: boolean
}

/** What Brakeman may see of each kind: a selector left empty is all of it. */
export interface BrakemanScope {
  projects?: string[]
  repos?: string[]
  crews?: string[]
  runs?: string[]
  sessions?: string[]
}

export interface BrakemanPermissions {
  observe: boolean
  draft: boolean
  propose: boolean
  act?: string[]
}

export interface BrakemanBudget {
  turnMinutes: number
  readsPerTurn: number
  turnsPerDay: number
  /** The day's minutes, whatever started the turns (BK3, PR-14). */
  minutesPerDay: number
}

/** When Brakeman runs: only when asked (`invoked`), or also woken in the background by the role (BK3). */
export interface BrakemanWake {
  mode: string
  windowSeconds: number
  cooldownMinutes: number
  questionMinutes: number
  /** "HH:MM" in the server's local zone. */
  briefings?: string[]
}

/** Brakeman's configuration, brakeman/config.json, as GET /api/brakeman answers it and PUT /api/brakeman/config takes it. */
export interface BrakemanConfig {
  enabled: boolean
  agentId: string
  account?: string
  model?: string
  cwd: string
  allowConductorTools: boolean
  visibility: BrakemanVisibility
  scope: BrakemanScope
  permissions: BrakemanPermissions
  wake: BrakemanWake
  budget: BrakemanBudget
  rev: number
  updatedAt?: string
}

/** The agent a turn ran: its id, adapter, probed version, model, whether an account was set, and the folder it ran in. */
export interface BrakemanStamp {
  agentId?: string
  adapter?: string
  version?: string
  model?: string
  account?: boolean
  /** The configured folder, or the desk's own for the account in place of the home folder (#705). */
  folder?: string
}

/** Where a turn under the configuration runs, and the folders it reads besides (at most 16), as its launch would take them now. */
export interface BrakemanFolders {
  turn: string
  reads: string[]
}

/**
 * Settings' words for where a turn runs and what it reads besides (#705), given the configured folder `cwd`: a turn run elsewhere runs
 * in the desk's own folder, which a person trusts once.
 */
export function foldersLine(f: BrakemanFolders, cwd: string): string {
  const where = f.turn === cwd ? `Its turns run in ${f.turn}` : `Its turns run in ${f.turn}, a folder of their own you trust once`
  if (!f.reads.length) return `${where}.`
  const list = f.reads.length === 1 ? f.reads[0] : `${f.reads.slice(0, -1).join(', ')} and ${f.reads[f.reads.length - 1]}`
  return `${where}, and read ${list} besides.`
}

/** A draft made work by a person, or its claim (PR-7 shows them): pending, made, refused or deleted. */
export interface BrakemanMade {
  item: number
  opId: string
  state: string
  key?: string
  crew?: string
  /** A draft plan revision proposed: the revision's id (its ticket's key in `key`). */
  plan?: string
  rev?: number
  why?: string
  /** When its claim was written (the server's time): a newer claim of the item replaces an older one. */
  at?: string
}

/**
 * An operation Brakeman ran under a grant the person made (docs/plans/brakeman.md, PR-12): written pending before the operation ran,
 * then done with its receipt (the id of what it made: a launch's, a revision's), refused with the words, or not_run when the server
 * stopped before it ran. Its target is the ticket it acted on.
 */
export interface BrakemanAct {
  opId: string
  operation: string
  target: string
  run?: string
  op: string
  state: 'pending' | 'done' | 'refused' | 'not_run' | string
  receipt?: string
  why?: string
  at?: string
}

/** A draft ticket's block of another, made a relation once both are tickets: pending, made, or refused. */
export interface BrakemanEdge {
  from: number
  to: number
  op: string
  state: string
}

/** A request, brakeman/requests/br-<n>.json, as the routes answer it. */
export interface BrakemanRequest {
  id: string
  opId: string
  kind: string
  subjects?: string[]
  note?: string
  since?: string
  by?: string
  createdAt: string
  state: string
  why?: string
  runId?: string
  sessionId?: string
  agent: BrakemanStamp
  config: { visibility: BrakemanVisibility; scope: BrakemanScope; permissions: BrakemanPermissions; budget: BrakemanBudget; rev: number }
  deadline?: string
  reads: number
  hasBrief?: boolean
  briefAt?: string
  made?: BrakemanMade[]
  edges?: BrakemanEdge[]
  acts?: BrakemanAct[]
  strayRun?: string
  /** A wake request's batch (BK3): the wakes its turn is about. */
  batch?: string
  closedAt?: string
}

/** GET /api/brakeman's answer. */
export interface BrakemanState {
  config: BrakemanConfig
  current: BrakemanRequest | null
  queue: BrakemanRequest[]
  turnsToday: number
  /** The day's minutes charged, and the background role as it stands (GET /api/brakeman/role has its log). */
  minutesToday?: number
  role?: BrakemanRole
  /** The server's words for the agent saved, when its read-only mode does not hold (brakeman.Warnings). */
  warnings?: string[]
  /** Where a turn under the configuration saved runs and what it reads besides; absent with no agent set. */
  folders?: BrakemanFolders
  readOnly?: boolean
  why?: string
  stuck?: boolean
  /** The running turn's reads as they stand (an older server sends none: the page reads `current` alone). */
  progress?: BrakemanProgress
  /** When the background role's next scheduled briefing is, an instant the page draws in the browser's time. */
  nextBriefing?: string
}

/** The longest derived reading a digest keeps, in characters (internal/brakeman/digest.go, MaxReading). */
export const MAX_READING = 280

/** A digest's counts of its brief's items, each omitted when 0 (drafts: draft tickets, crews and plans together). */
export interface BrakemanDigestCounts {
  decisions?: number
  proposals?: number
  drafts?: number
  handoffs?: number
  facts?: number
  interpretations?: number
  outcomes?: number
  overlaps?: number
  comparisons?: number
  evidence?: number
}

/** What a brief cites, first cited first: run ids, "<run>/<task>", session ids and ticket keys; each list omitted when empty. */
export interface BrakemanDigestAbout {
  runs?: string[]
  tasks?: string[]
  sessions?: string[]
  tickets?: string[]
}

/** A brief's digest, the server's sibling of a kept brief: its title, a derived reading cut to MAX_READING, its counts and what it cites. */
export interface BrakemanDigest {
  schema: number
  title: string
  reading: string
  readingCut?: boolean
  counts: BrakemanDigestCounts
  about: BrakemanDigestAbout
}

/** A request as the list route answers it: with its brief's digest when it has one, and how it matched an `about` filter. */
export interface BrakemanRow extends BrakemanRequest {
  digest?: BrakemanDigest
  match?: 'subject' | 'cited'
}

/** The last read a running turn was admitted to, by what it read and, when it fits, the ref. */
export interface BrakemanReadingNow {
  what: string
  ref?: string
}

/** A running turn's progress, from the `brakeman_progress` event or GET /api/brakeman's `progress`. */
export interface BrakemanProgress {
  id: string
  reads: number
  readsPerTurn: number
  startedAt?: string
  deadline?: string
  reading?: BrakemanReadingNow
}

/** One count of GET /api/brakeman/since: how many, at most five refs, and the oldest's time where it has one. */
export interface BrakemanSinceCount {
  count: number
  refs: string[]
  oldest?: string
}

/** GET /api/brakeman/since's answer: what changed in Conductor since the last briefing, the last day, or a given time. */
export interface BrakemanSince {
  since: { from: 'briefing' | 'day' | 'given'; briefing?: string; at: string }
  runsEnded: BrakemanSinceCount
  tasksFailed: BrakemanSinceCount
  ticketsReady: BrakemanSinceCount
  questionsOpen: BrakemanSinceCount
  truncated: boolean
}

/** GET and POST /api/brakeman/opened's answer: the caller's briefed requests not yet opened, newest first, and when its marks began. */
export interface BrakemanOpened {
  unread: string[]
  begun: string
}

/** One item of a brief: its kind beside that kind's own fields (docs/protocol.md, "A brief"). */
export type BriefItem =
  | { kind: 'fact/1'; text: string; evidence: string[] }
  | { kind: 'interpretation/1'; text: string; facts: number[] }
  | { kind: 'proposal/1'; text: string; op: string; target: string; args?: Record<string, unknown>; expectedRevision?: string; facts?: number[] }
  | { kind: 'outcome/1'; text: string; receipt: string }
  | {
      kind: 'decision/1'
      about: string
      happened: string
      whyHuman: string
      question: string
      alternatives: Array<{ label: string; tradeoffs: string }>
      /** An alternative's index, or -1 (or null) for none. */
      recommendation: number | null
      uncertainty?: string
      blocked?: string[]
      authorizes: string
      evidence: string[]
      subjectRevision?: string
    }
  | { kind: 'comparison/1'; title: string; columns: string[]; rows: Array<{ label: string; cells: string[] }>; evidence?: string[] }
  | { kind: 'evidence/1'; title: string; refs: string[] }
  | { kind: 'draft_ticket/1'; project: string; title: string; description?: string; acceptance?: string; priority?: number; blocks?: number[]; from?: string[] }
  | { kind: 'draft_crew/1'; crew: { name?: string } & Record<string, unknown>; gates?: Array<{ task: string; decision: string }>; from?: string[] }
  | {
      kind: 'draft_plan/1'
      /** The ticket: ref it is for. */
      ticket: string
      /** A saved crew's id, one with tasks. */
      crew: string
      base?: string
      /** A plan: ref of the same ticket, the revision it revises. */
      basedOn?: string
      references?: Array<{ path: string; start?: number; end?: number; note?: string }>
      attachments?: string[]
      /** comment: refs of the same ticket, handed over. */
      comments?: string[]
      constraints?: string
      decisions?: Array<{ id: string; question: string; kind: string; before?: string[] }>
      note?: string
      from?: string[]
    }
  | { kind: 'handoff/1'; objective: string; constraints?: string; decisions?: string[]; revision?: string; questions?: string[]; evidence?: string[] }
  | { kind: 'overlap/1'; paths: string[]; between: string[]; evidence?: string[] }

/** A brief, schema brakeman.brief/1, as kept. */
export interface BrakemanBrief {
  schema: string
  title: string
  summary: string
  uncertainty?: string
  items: BriefItem[]
}

/**
 * A brief as a reply carries it, with its items a list: the server writes a brief kept with none (its summary alone) as `items:
 * null`, and the brief's groups read that as no items.
 */
export function keptBrief(b: (Omit<BrakemanBrief, 'items'> & { items?: BriefItem[] | null }) | null | undefined): BrakemanBrief | null {
  return b ? { ...b, items: b.items ?? [] } : null
}

/** A ref's record at one time: what the turn read of it (`seen`), or what it was as the brief was kept (`now`). */
export interface RefState {
  url?: string
  state?: string
  rev?: string
  sha256?: string
  at?: string
}

/** One ref of a kept brief, as GET /api/brakeman/requests/{id}/refs answers it. */
export interface RefResolution {
  ref: string
  seen: RefState
  now: RefState
  /** A capacity: ref's reading as the server kept it with the brief (docs/protocol.md, Brakeman): labels and figures, never a folder. */
  capacity?: CapacitySnapshot
}

/** A reading a brief cites, as the server copied it when the brief was kept (internal/brakeman Capacity). */
export interface CapacitySnapshot {
  agent: { id: string; name?: string; adapter?: string; version?: string }
  account: { id: string; label?: string; retired?: boolean; plan?: string }
  state: string
  words?: string
  windows: CapacityWindow[]
  credits: CapacityCredit[]
  limits?: CapacityLimits
  counts?: CapacityCounts
  countedAt?: string
  readAt?: string
  readBy?: { program: string; version: string; qualified: string }
  fresh?: string
}

/** How fresh a reading was when the brief cited it, in words; nothing for a fresh one. */
const CITED_FRESH: Record<string, string> = {
  stale: 'Stale when the brief cited it',
  reset_passed: 'A reset had passed since this reading when the brief cited it',
}

/**
 * A capacity: ref's chip in words, from the reading kept with the brief (PR-7): the agent and the account by their names, a short
 * summary (each window the provider reported with its share used, or the state's title), and its title, every fact the reading
 * holds: the plan as the tool reported it, the server's words, each window with its reset, the warning (a window at warning, critical
 * or 90%, a limit the provider reports), the provider's figures, Conductor's estimate apart with when it was counted, when it was read
 * and by what, and whether it was stale or a reset had passed when it was cited, one fact a line (`lines`, the chip's details); `read`,
 * for the chip's own text, when the reading was made, whether it was stale or a reset had passed when cited, and "may stop" when it warns. A window the provider did not report is in none of them. Null without a reading.
 */
export function capacityRefWords(c: CapacitySnapshot | undefined, now: number, timeZone?: string): { label: string; summary: string; read: string; lines: string[]; title: string } | null {
  if (!c) return null
  const account = c.account.id === 'default' ? 'its default login' : (c.account.label || c.account.id) + (c.account.retired ? ' (retired)' : '')
  const label = `capacity of ${c.agent.name || c.agent.id} on ${account}`
  const state = capacityStateWords({ state: c.state, words: c.words })
  const summary = c.state === 'ok' ? c.windows.map((w) => `${w.name} ${w.percent}%`).join(' · ') || 'no windows reported' : state.title
  const at = Date.parse(c.readAt ?? '')
  const counted = Date.parse(c.countedAt ?? '')
  const by = c.readBy ? ` by ${c.readBy.program} ${c.readBy.version}${c.readBy.qualified && c.readBy.qualified !== c.readBy.version ? `, checked on ${c.readBy.qualified}` : ''}` : ''
  const lines = [
    planWords(c.account.plan, c.agent.adapter),
    c.state === 'ok' ? (c.words ?? '') : `${state.title}: ${state.words}`,
    ...c.windows.map((w) => windowWords(w, now, timeZone)),
    warningOf(c)?.words ?? '',
    [...c.credits.map(creditWords), ...limitsWords(c.limits)].map((x) => `The provider's: ${x}`).join(' · '),
    c.counts ? `Conductor's estimate: ${countsWords(c.counts)}${Number.isNaN(counted) ? '' : `, as of ${clockWords(counted, timeZone)}`}` : '',
    Number.isNaN(at) ? '' : `Read ${clockWords(at, timeZone)}${by}`,
    CITED_FRESH[c.fresh ?? ''] ?? '',
  ]
  const shown = lines.filter(Boolean)
  const marks = [Number.isNaN(at) ? '' : `read ${clockWords(at, timeZone)}`, c.fresh === 'stale' ? 'stale when cited' : c.fresh === 'reset_passed' ? 'a reset had passed when cited' : '', warningOf(c) ? 'may stop' : '']
  return { label, summary, read: marks.filter(Boolean).join(' · '), lines: shown, title: shown.join('\n') }
}

/** The kinds a request may have, in the order the composer offers them, with what the page calls each. */
export const KINDS: ReadonlyArray<{ value: string; label: string }> = [
  { value: 'briefing', label: 'Brief me since…' },
  { value: 'explain', label: 'Explain this' },
  { value: 'decision', label: 'Prepare a decision' },
  { value: 'draft_tickets', label: 'Draft tickets' },
  { value: 'propose_crew', label: 'Propose a crew' },
  { value: 'handoff', label: 'Prepare a handoff' },
  { value: 'locate', label: 'Find…' },
  { value: 'overlaps', label: 'Overlapping changes' },
  { value: 'ask', label: 'A question' },
  { value: 'compare', label: 'Compare approaches' },
]

/** What the page calls a request's kind: a wake request is the background role's own, never one the composer offers. */
export function kindWords(kind: string): string {
  if (kind === 'wake') return 'Woken by events'
  return KINDS.find((k) => k.value === kind)?.label ?? kind
}

/**
 * The subjects each kind takes (internal/brakeman/request.go, subjectKinds): the ref kinds, how many, and whether the note is the
 * request. The composer offers these and refuses what the server would.
 */
export const KIND_SUBJECTS: Readonly<Record<string, { kinds: readonly string[]; min: number; max: number; note: boolean }>> = {
  briefing: { kinds: [], min: 0, max: 0, note: false },
  explain: { kinds: ['run', 'task', 'attempt', 'session', 'question', 'ticket'], min: 1, max: 1, note: false },
  decision: { kinds: ['question', 'task', 'ticket'], min: 1, max: 1, note: false },
  draft_tickets: { kinds: ['ticket', 'comment'], min: 0, max: 1, note: false },
  propose_crew: { kinds: ['ticket'], min: 1, max: 1, note: false },
  handoff: { kinds: ['task', 'attempt', 'session'], min: 1, max: 1, note: false },
  locate: { kinds: [], min: 0, max: 0, note: true },
  overlaps: { kinds: [], min: 0, max: 0, note: false },
  ask: { kinds: [], min: 0, max: 10, note: true },
  compare: { kinds: ['run'], min: 1, max: 1, note: false },
}

/** A note's bound, in bytes of UTF-8 (brakeman.MaxNote). */
export const MAX_NOTE = 16 << 10

/** A display name's bound, in bytes of UTF-8 (brakeman.MaxBy): what a request and a make keep as `by`. */
export const MAX_BY = 60

/**
 * The display name as a request or a make keeps it: at most MAX_BY bytes with no control character (the make route refuses any, as
 * Dispatch does), cut where a character ends. The identity takes 40 characters, which in a script of three-byte characters is past
 * the bound: sent whole, the route would refuse it.
 */
export function displayBy(name: string): string {
  const enc = new TextEncoder()
  let out = ''
  for (const ch of name.replace(/\p{Cc}/gu, ' ')) {
    if (enc.encode(out + ch).length > MAX_BY) break
    out += ch
  }
  return out.trim()
}

/** Why the composer cannot send what it holds, or "" when it can: the server's checks of a request (checkAsked). */
export function askProblem(kind: string, subjects: readonly string[], note: string, since: string): string {
  const sk = KIND_SUBJECTS[kind]
  if (!sk) return 'Choose what to ask.'
  if (subjects.length < sk.min || subjects.length > sk.max) {
    if (sk.max === 0) return `${kindWords(kind)} names no subject.`
    return sk.min === sk.max ? `${kindWords(kind)} names ${sk.min === 1 ? 'one subject' : `${sk.min} subjects`}.` : `${kindWords(kind)} names at most ${sk.max}.`
  }
  for (const s of subjects) {
    const r = parseRef(s)
    if (!r) return `${s} is no ref.`
    if (sk.kinds.length && !sk.kinds.includes(r.kind)) return `${kindWords(kind)} names a ${sk.kinds.join(', a ')}.`
  }
  if (new TextEncoder().encode(note).length > MAX_NOTE) return `The note is at most ${MAX_NOTE >> 10} KiB.`
  if (sk.note && !note.trim()) return `${kindWords(kind)} is its note: write it.`
  if (kind === 'draft_tickets' && !subjects.length && !note.trim()) return 'Name a ticket or a comment, or write the discussion in the note.'
  if (since && kind !== 'briefing') return 'Only a briefing starts at a time.'
  return ''
}

/** Every state a request may be in; the first three are open. */
export const OPEN_STATES = ['queued', 'starting', 'running'] as const

/** Whether a request is closed: it never changes state again. */
export function closed(state: string): boolean {
  return !(OPEN_STATES as readonly string[]).includes(state)
}

/** A state's place in the order a request goes through: a read that says less far along is older than what the page has. */
export function stateRank(state: string): number {
  const i = (OPEN_STATES as readonly string[]).indexOf(state)
  return i >= 0 ? i : OPEN_STATES.length
}

/** What the page says of a request's state: "running in <session>", "timed out after 15 min", "the session ended with no brief". */
export function stateWords(r: Pick<BrakemanRequest, 'state' | 'why' | 'sessionId' | 'config'>, sessionName?: string): string {
  switch (r.state) {
    case 'queued':
      return 'queued'
    case 'starting':
      return 'starting'
    case 'running':
      return r.sessionId ? `running in ${sessionName || r.sessionId}` : 'running'
    case 'briefed':
      return 'briefed'
    case 'timed_out':
      return `timed out after ${r.config?.budget?.turnMinutes ?? 15} min`
    case 'ended':
      return 'the session ended with no brief'
    case 'failed':
      return r.why ? `failed: ${r.why}` : 'failed'
    case 'over_budget':
      return r.why ? `not started: ${r.why}` : "not started: today's budget was spent"
    case 'cancelled':
      return r.why ? `cancelled: ${r.why}` : 'cancelled'
    case 'interrupted':
      return 'interrupted: the server stopped during the turn'
  }
  return r.state
}

/** A ref parsed: its kind and its parts, as internal/brakeman's ParseRef reads them. */
export interface ParsedRef {
  kind: string
  run?: string
  task?: string
  n?: number
  check?: string
  k?: number
  command?: string
  seq?: number
  session?: string
  question?: string
  ticket?: string
  comment?: string
  actor?: string
  op?: string
  commit?: string
  path?: string
  request?: string
  item?: number
  launch?: string
  decision?: string
  /** capture: a capture's id, and the send whose frozen document it names. */
  capture?: string
  send?: string
  agent?: string
  account?: string
  readAt?: string
}

const RUN = /^[a-z0-9][a-z0-9-]{0,63}-[0-9a-f]{8}$/
const TASK = /^[a-z0-9][a-z0-9_-]{0,39}$/
const OP = /^[A-Za-z0-9._~-]{1,32}$/
const HEX = /^[0-9a-f]{16}$/
const TICKET = /^[A-Z][A-Z0-9]{1,5}-[1-9][0-9]{0,17}$/
const COMMIT = /^[0-9a-f]{40}([0-9a-f]{24})?$/
const REQUEST = /^br-[1-9][0-9]{0,17}$/
const DECISION = /^dr-[1-9][0-9]{0,17}$/
const AGENT = /^[a-z0-9-]{1,32}$/
const ACCOUNT = /^(default|a-[a-z2-7]{12})$/
// A reading's time as the server writes it (time.RFC3339Nano in UTC): at most nine fraction digits, none of them trailing zeros.
const READ_AT = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(\.\d{0,8}[1-9])?Z$/

/** Whether s is a reading's time the server's grammar takes: READ_AT's shape, a real calendar date and a time of day. */
function readAtOk(s: string): boolean {
  const m = READ_AT.exec(s)
  if (!m) return false
  const [y, mo, d, h, mi, sec] = m.slice(1, 7).map(Number) as [number, number, number, number, number, number]
  const leap = (y % 4 === 0 && y % 100 !== 0) || y % 400 === 0
  const days = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31][mo - 1]
  return days !== undefined && d >= 1 && d <= days && h <= 23 && mi <= 59 && sec <= 59
}
const NUMBER = /^(0|[1-9][0-9]{0,17})$/

/** The ref's bound, in bytes (brakeman.MaxRef). */
export const MAX_REF = 512

/** Parses a ref by the grammar of docs/protocol.md ("Refs"), or null for one that is not. */
export function parseRef(s: string): ParsedRef | null {
  if (!s || new TextEncoder().encode(s).length > MAX_REF) return null
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f-\u009f]/.test(s)) return null
  const colon = s.indexOf(':')
  if (colon < 0) return null
  const kind = s.slice(0, colon)
  const rest = s.slice(colon + 1)
  if (kind === 'file') {
    // file:<session>/<path>, the path absolute and its slashes its own: file:<session>//home/me/repo/main.go.
    const slash = rest.indexOf('/')
    const session = rest.slice(0, slash)
    const path = rest.slice(slash + 1)
    return slash > 0 && HEX.test(session) && path.startsWith('/') && cleanPath(path) === path ? { kind, session, path } : null
  }
  if (kind === 'op') {
    // op:<actor>/<opId>: an actor may hold a slash; an op id never does.
    const slash = rest.lastIndexOf('/')
    const actor = rest.slice(0, slash)
    const op = rest.slice(slash + 1)
    return slash > 0 && new TextEncoder().encode(actor).length <= 60 && actor.trim() === actor && OP.test(op) ? { kind, actor, op } : null
  }
  const p = rest.split('/')
  const num = (v: string | undefined, min: number) => (v !== undefined && NUMBER.test(v) && Number(v) >= min ? Number(v) : undefined)
  switch (kind) {
    case 'run':
      return p.length === 1 && RUN.test(p[0]!) ? { kind, run: p[0] } : null
    case 'task':
      return p.length === 2 && RUN.test(p[0]!) && TASK.test(p[1]!) ? { kind, run: p[0], task: p[1] } : null
    case 'attempt': {
      const n = num(p[2], 1)
      return p.length === 3 && RUN.test(p[0]!) && TASK.test(p[1]!) && n ? { kind, run: p[0], task: p[1], n } : null
    }
    case 'check': {
      const n = num(p[2], 1)
      const k = num(p[4], 1)
      return p.length === 5 && RUN.test(p[0]!) && TASK.test(p[1]!) && n && TASK.test(p[3]!) && k ? { kind, run: p[0], task: p[1], n, check: p[3], k } : null
    }
    case 'command':
      return p.length === 2 && RUN.test(p[0]!) && OP.test(p[1]!) ? { kind, run: p[0], command: p[1] } : null
    case 'event':
    case 'message': {
      const seq = num(p[1], 1)
      return p.length === 2 && RUN.test(p[0]!) && seq ? { kind, run: p[0], seq } : null
    }
    case 'session':
      return p.length === 1 && HEX.test(p[0]!) ? { kind, session: p[0] } : null
    case 'question':
      return p.length === 2 && HEX.test(p[0]!) && HEX.test(p[1]!) ? { kind, session: p[0], question: p[1] } : null
    case 'ticket':
      return p.length === 1 && TICKET.test(p[0]!) ? { kind, ticket: p[0] } : null
    case 'comment':
      return p.length === 2 && TICKET.test(p[0]!) && HEX.test(p[1]!) ? { kind, ticket: p[0], comment: p[1] } : null
    case 'commit':
      return p.length === 2 && RUN.test(p[0]!) && COMMIT.test(p[1]!) ? { kind, run: p[0], commit: p[1] } : null
    case 'brief': {
      const item = num(p[1], 0)
      return p.length === 2 && REQUEST.test(p[0]!) && item !== undefined && item < 100 ? { kind, request: p[0], item } : null
    }
    case 'plan': {
      // plan:<KEY-n>/<revision>, the revision's number from 1.
      const n = num(p[1], 1)
      return p.length === 2 && TICKET.test(p[0]!) && n ? { kind, ticket: p[0], n } : null
    }
    case 'launch':
      return p.length === 1 && HEX.test(p[0]!) ? { kind, launch: p[0] } : null
    case 'candidate': {
      const n = num(p[2], 1)
      return p.length === 3 && RUN.test(p[0]!) && TASK.test(p[1]!) && n ? { kind, run: p[0], task: p[1], n } : null
    }
    case 'decision':
      // decision:<dr-n>, DR1's record, as its run's read lists it.
      return p.length === 1 && DECISION.test(p[0]!) ? { kind, decision: p[0] } : null
    case 'capture':
      // capture:<id> (a capture as it is now) or capture:<id>/<send> (what that send froze), PV1's.
      return (p.length === 1 || p.length === 2) && p.every((x) => HEX.test(x)) ? { kind, capture: p[0], ...(p[1] ? { send: p[1] } : {}) } : null
    case 'capacity': {
      // capacity:<agent>/<account>@<readAt>, AC1's reading (docs/plans/capacity.md, PR-6); its chip is PR-7's.
      // Exactly one '@', and the time as the server's grammar has it (brakeman.ParseRef).
      const parts = (p[1] ?? '').split('@')
      const [account, readAt] = parts
      return p.length === 2 && parts.length === 2 && AGENT.test(p[0]!) && ACCOUNT.test(account!) && readAtOk(readAt!) ? { kind, agent: p[0], account, readAt } : null
    }
  }
  return null
}

/** A ticket's page: /dispatch/<project>/<number>. */
function ticketPath(key: string): string {
  const dash = key.lastIndexOf('-')
  return `/dispatch/${encodeURIComponent(key.slice(0, dash))}/${key.slice(dash + 1)}`
}

/**
 * The page a person follows a ref to in the app (the server's refURL, internal/api/brakeman_refs.go, for the kinds it gives a page),
 * or null for one with no page of its own: a Dispatch receipt, whose page is its ticket's as the resolution names it (refPage), and a
 * launch, which the server serves from its API route. A task, an attempt, a check and a candidate open the run page's Tasks view at
 * the task, whatever view this browser chose last (the page selects no one attempt: they are listed under the task); a plan
 * revision opens its ticket's page, which lists them. A check receipt, a command receipt and a comment are read from the API with the
 * token, which a link would not carry (receiptPath): their chip leads to the page they belong to, the comment's to its ticket's.
 */
export function refRoute(ref: string): string | null {
  const r = parseRef(ref)
  if (!r) return null
  const run = r.run ? `/runs/${encodeURIComponent(r.run)}` : ''
  switch (r.kind) {
    case 'run':
    case 'event':
    case 'message':
    case 'commit':
    case 'command':
      return run
    case 'task':
    case 'attempt':
    case 'check':
    case 'candidate':
      return `${run}?view=tasks&task=${encodeURIComponent(r.task!)}`
    case 'session':
    case 'question':
      return `/sessions/${encodeURIComponent(r.session!)}`
    case 'file':
      return `/sessions/${encodeURIComponent(r.session!)}?tab=files&open=${encodeURIComponent(r.path!)}`
    case 'ticket':
      return ticketPath(r.ticket!)
    case 'comment':
    case 'plan':
      return ticketPath(r.ticket!)
    case 'brief':
      return `/brakeman?request=${encodeURIComponent(r.request!)}#item-${r.item}`
  }
  return null
}

/** Whether a URL the server gave is a path of the app's own pages: one slash, then no API route. */
function appPage(u: string | undefined): u is string {
  return !!u && u.startsWith('/') && !u.startsWith('//') && !u.startsWith('/api/')
}

/**
 * The page a ref's chip links to: the one its resolution names when that is a page of the app (a Dispatch receipt's ticket or
 * project, as the server read it), else refRoute's.
 */
export function refPage(ref: string, resolution?: RefResolution): string | null {
  const u = resolution?.now.url
  return appPage(u) ? u : refRoute(ref)
}

/**
 * The API route a ref's record is read from with the token, for the kinds whose record no page shows by itself: a check receipt's
 * log, a command receipt (GET /api/runs/{run}/commands/{command}), a Dispatch receipt of the admin's (GET /api/dispatch/ops/{opId},
 * which reads only the admin's), and a comment, by the route its resolution names (the comment's number is the server's, not the
 * ref's). Null for every other kind.
 */
export function receiptPath(ref: string, resolution?: RefResolution): string | null {
  const r = parseRef(ref)
  if (!r) return null
  const e = encodeURIComponent
  switch (r.kind) {
    case 'check':
      return `/api/runs/${e(r.run!)}/tasks/${e(r.task!)}/attempts/${r.n}/steps/${e(r.check!)}/${r.k}/log`
    case 'command':
      return `/api/runs/${e(r.run!)}/commands/${e(r.command!)}`
    case 'op':
      return r.actor === 'admin' ? `/api/dispatch/ops/${e(r.op!)}` : null
    case 'launch':
      // A launch from a ticket, its summary and package (GET /api/dispatch/launches/{id}): its run's page is the chip's own (refPage).
      return `/api/dispatch/launches/${e(r.launch!)}`
    case 'comment': {
      const u = resolution?.now.url
      return u?.startsWith(`/api/dispatch/tickets/${e(r.ticket!)}/comments?`) ? u : null
    }
  }
  return null
}

/** What a ref's chip says: the record it names, in words, with the names the page knows (a session's, a run's). */
export function refLabel(ref: string, names: { session?: (id: string) => string | undefined; run?: (id: string) => string | undefined } = {}): string {
  const r = parseRef(ref)
  if (!r) return ref
  const run = (id: string) => names.run?.(id) || id
  const session = (id: string) => names.session?.(id) || id
  switch (r.kind) {
    case 'run':
      return `run ${run(r.run!)}`
    case 'task':
      return `task ${r.task}`
    case 'attempt':
      return `${r.task}, attempt ${r.n}`
    case 'check':
      return `check ${r.check} of ${r.task}, attempt ${r.n}${r.k! > 1 ? ` (run ${r.k})` : ''}`
    case 'command':
      return `command ${r.command}`
    case 'event':
      return `event ${r.seq} of ${run(r.run!)}`
    case 'message':
      return `message ${r.seq} of ${run(r.run!)}`
    case 'session':
      return `session ${session(r.session!)}`
    case 'question':
      return `question in ${session(r.session!)}`
    case 'ticket':
      return r.ticket!
    case 'comment':
      return `comment on ${r.ticket}`
    case 'op':
      return `receipt ${r.op}`
    case 'commit':
      return `commit ${r.commit!.slice(0, 8)}`
    case 'file':
      return r.path!.split('/').pop() || r.path!
    case 'brief':
      return `${r.request}, item ${r.item! + 1}`
    case 'plan':
      return `plan revision ${r.n} of ${r.ticket}`
    case 'launch':
      return `launch ${r.launch}`
    case 'candidate':
      return `candidate ${r.task}, attempt ${r.n}`
    case 'decision':
      return `decision ${r.decision}`
    case 'capture':
      return r.send ? `capture ${r.capture}, as sent` : `capture ${r.capture}`
    case 'capacity':
      return `capacity of ${r.agent} on ${r.account}`
  }
  return ref
}

/** Whether a ref's record moved since the brief was kept: the live state or revision the page has differs from the resolution's `now`. */
export function changedSince(now: RefState | undefined, live: { state?: string; rev?: string } | undefined): boolean {
  if (!now || !live) return false
  if (live.state !== undefined && now.state !== undefined && live.state !== now.state) return true
  return live.rev !== undefined && now.rev !== undefined && live.rev !== now.rev
}

/**
 * What changed of a decision's evidence ref since its question was asked (docs/plans/decisions.md, "web/app": the chip's `baseline`
 * mode), from its resolution alone: its `seen` (the record when the request was made) against its `now`. `unread` when the baseline's
 * store could not be read then (nothing says what it was), `deleted` when the record is gone since, `changed` when its state, revision
 * or digest differ (a run's or a task's state alone: a run's revision moves with every transition of it, the decision's own opening
 * among them, internal/api refChange), '' when nothing changed.
 */
export function changedFromBaseline(r: { ref: string; seen: RefState; now: RefState }): '' | 'changed' | 'deleted' | 'unread' {
  if (r.seen.state === 'unavailable') return 'unread'
  if (r.now.state === 'deleted' && r.seen.state !== 'deleted') return 'deleted'
  const kind = r.ref.slice(0, r.ref.indexOf(':'))
  if (kind === 'run' || kind === 'task') return r.now.state !== r.seen.state ? 'changed' : ''
  return r.now.state !== r.seen.state || (r.now.rev ?? '') !== (r.seen.rev ?? '') || (r.now.sha256 ?? '') !== (r.seen.sha256 ?? '') ? 'changed' : ''
}

/** The regions a brief is drawn in, top to bottom, by what needs a person first; an item kind appears in exactly one. */
export type BriefRegionName = 'needs' | 'proposed' | 'acts' | 'changed' | 'compared' | 'fold'

/**
 * Each region's kinds, in the order they are drawn within it (a list's kinds share one place and keep the brief's order): the decisions
 * waiting on a person; what Brakeman proposes, its drafts and its handoffs; the outcomes it verified (beside its acts, which are the
 * request's, not the brief's); what changed, the facts with their readings, then overlapping changes; comparisons, drawn open; and the
 * evidence panels, folded with the provenance.
 */
export const BRIEF_REGIONS: ReadonlyArray<{ region: BriefRegionName; kinds: ReadonlyArray<readonly string[]> }> = [
  { region: 'needs', kinds: [['decision/1']] },
  { region: 'proposed', kinds: [['proposal/1'], ['draft_ticket/1', 'draft_crew/1', 'draft_plan/1'], ['handoff/1']] },
  { region: 'acts', kinds: [['outcome/1']] },
  { region: 'changed', kinds: [['fact/1', 'interpretation/1'], ['overlap/1']] },
  { region: 'compared', kinds: [['comparison/1']] },
  { region: 'fold', kinds: [['evidence/1']] },
]

/**
 * A brief's item as a region draws it: the item, its index in the brief (its anchor `item-<index>`, the make route's item, the note it
 * keeps, its key), a fact's number, and, for an interpretation, the number of the last fact it reads, under which it is drawn (none: it
 * names no fact and is drawn after them).
 */
export interface GroupedItem {
  item: BriefItem
  index: number
  /** A fact's number among the brief's facts, from 1, as an interpretation names it. */
  fact?: number
  /** An interpretation's place: the last fact it reads, by number. */
  under?: number
}

/**
 * A brief's items in their regions (BRIEF_REGIONS), for display only: every item keeps its index. Within "changed", each
 * interpretation follows the last fact it reads, those naming no fact follow the facts, and overlaps come last. A region with no item
 * is left out, unless `keep` names it (the acts region holds the request's acts too, which are not items).
 */
export function briefRegions(items: readonly BriefItem[], keep: readonly BriefRegionName[] = []): Array<{ region: BriefRegionName; items: GroupedItem[] }> {
  const facts = factNumbers(items)
  const under = (it: BriefItem): number | undefined => {
    if (it.kind !== 'interpretation/1') return undefined
    const named = (it.facts ?? []).map((i) => facts.get(i)).filter((n): n is number => n !== undefined)
    return named.length ? Math.max(...named) : undefined
  }
  const placed = (kinds: readonly string[]): GroupedItem[] => {
    const xs = items.flatMap((item, index) => (kinds.includes(item.kind) ? [{ item, index, fact: facts.get(index), under: under(item) }] : []))
    if (!kinds.includes('fact/1')) return xs
    const out: GroupedItem[] = []
    for (const x of xs) {
      if (x.item.kind !== 'fact/1') continue
      out.push(x, ...xs.filter((y) => y.under === x.fact))
    }
    return [...out, ...xs.filter((y) => y.item.kind === 'interpretation/1' && y.under === undefined)]
  }
  return BRIEF_REGIONS.map((r) => ({ region: r.region, items: r.kinds.flatMap(placed) })).filter((r) => r.items.length > 0 || keep.includes(r.region))
}

/** A region's heading as the brief draws it: "Needs you · 1", "Proposed · 2", "What changed · 3 facts". */
export function regionHeading(region: BriefRegionName, items: readonly GroupedItem[]): string {
  switch (region) {
    case 'needs':
      return `Needs you · ${items.length}`
    case 'proposed':
      return `Proposed · ${items.length}`
    case 'acts':
      return 'What Brakeman did'
    case 'changed': {
      const n = items.filter((x) => x.item.kind === 'fact/1').length
      return n ? `What changed · ${n} ${n === 1 ? 'fact' : 'facts'}` : 'What changed'
    }
    case 'compared':
      return 'Compared'
    case 'fold':
      return 'Evidence'
  }
}

/** The refs a brief's items cite, each once, first cited first: every evidence chip of the brief in one list. */
export function briefEvidence(items: readonly BriefItem[]): string[] {
  const out = new Set<string>()
  const add = (refs: readonly string[] | undefined) => refs?.forEach((r) => out.add(r))
  for (const it of items) {
    switch (it.kind) {
      case 'fact/1':
      case 'decision/1':
        add(it.evidence)
        if (it.kind === 'decision/1') add(it.blocked)
        break
      case 'outcome/1':
        add([it.receipt])
        break
      case 'evidence/1':
        add(it.refs)
        break
      case 'overlap/1':
        add(it.between)
        add(it.evidence)
        break
      case 'comparison/1':
      case 'handoff/1':
        add(it.evidence)
        break
    }
  }
  return [...out]
}

/** The runs a brief cites, first cited first, by id: what a wake brief is about when the server sends no digest (an older server). */
export function citedRuns(items: readonly BriefItem[]): string[] {
  const out = new Set<string>()
  for (const ref of briefEvidence(items)) {
    const r = parseRef(ref)
    if (r?.run) out.add(r.run)
  }
  return [...out]
}

/** The tickets a brief names, each once, first named first: its request's subjects, then every ref its items cite or act on. */
export function briefTickets(b: Pick<BrakemanBrief, 'items'>, r: Pick<BrakemanRequest, 'subjects'>): string[] {
  const acted = (b.items ?? []).flatMap((it) => (it.kind === 'proposal/1' ? [it.target] : it.kind === 'decision/1' ? [it.about] : it.kind === 'draft_plan/1' ? [it.ticket] : []))
  const out = new Set<string>()
  for (const ref of [...(r.subjects ?? []), ...acted, ...briefEvidence(b.items ?? [])]) {
    const k = refTicket(ref)
    if (k) out.add(k)
  }
  return [...out]
}

/** The most names a brief's head gives for what it is about, before "+N". */
export const HEAD_ABOUT_MAX = 3

/**
 * What a brief is about, by name, for its head: an asked request's subjects as their chips say them; a wake request's runs the brief
 * cites (the digest's, else those its items cite), at most HEAD_ABOUT_MAX by name and then "+N".
 */
export function briefAbout(
  r: Pick<BrakemanRequest, 'kind' | 'subjects'>,
  cites: { digest?: Pick<BrakemanDigest, 'about'>; items?: readonly BriefItem[] },
  names: { session?: (id: string) => string | undefined; run?: (id: string) => string | undefined } = {},
): string[] {
  const shown = (xs: string[]) => (xs.length > HEAD_ABOUT_MAX ? [...xs.slice(0, HEAD_ABOUT_MAX), `+${xs.length - HEAD_ABOUT_MAX}`] : xs)
  if (r.kind !== 'wake') return shown((r.subjects ?? []).map((s) => refLabel(s, names)))
  const runs = cites.digest?.about.runs ?? citedRuns(cites.items ?? [])
  return shown(runs.map((id) => names.run?.(id) || id))
}

/** The fold's line: "Provenance and all evidence: <agent>, <model>, request br-n, N reads" (a closed turn's length is not kept). */
export function foldWords(r: Pick<BrakemanRequest, 'id' | 'agent' | 'reads'>): string {
  const a = r.agent ?? {}
  const reads = r.reads ?? 0
  return `Provenance and all evidence: ${a.agentId || 'an agent'}, ${a.model || 'its default model'}, request ${r.id}, ${reads} ${reads === 1 ? 'read' : 'reads'}`
}

/** Each fact's number among the brief's facts, by its index in the brief. */
export function factNumbers(items: readonly BriefItem[]): Map<number, number> {
  const out = new Map<number, number>()
  items.forEach((it, i) => {
    if (it.kind === 'fact/1') out.set(i, out.size + 1)
  })
  return out
}

/** What an interpretation (or a proposal) says it reads: "reads fact 2", "reads facts 2 and 3", "reads facts 1, 2 and 4". */
export function readsWords(facts: readonly number[] | undefined, items: readonly BriefItem[]): string {
  const numbers = factNumbers(items)
  const named = (facts ?? []).map((i) => numbers.get(i)).filter((n): n is number => n !== undefined)
  if (!named.length) return ''
  if (named.length === 1) return `reads fact ${named[0]}`
  return `reads facts ${named.slice(0, -1).join(', ')} and ${named[named.length - 1]}`
}

/** A brief's provenance line: the agent, its model, its version, the request, when the brief was kept and the folder the turn ran in. */
export function provenance(r: Pick<BrakemanRequest, 'id' | 'agent' | 'briefAt'>, agentName?: string, at?: (iso: string) => string): string {
  const a = r.agent ?? {}
  const parts = [agentName || a.agentId || 'an agent', a.model ? `model ${a.model}` : 'its default model']
  if (a.version) parts.push(`version ${a.version}`)
  parts.push(`request ${r.id}`)
  if (r.briefAt) parts.push(`as of ${at ? at(r.briefAt) : r.briefAt}`)
  if (a.folder) parts.push(`ran in ${a.folder}`)
  return parts.join(' · ')
}

/**
 * What each adapter can do as Brakeman's agent (internal/agents: Adapter.MCP, ReadOnlyArgs, ModelArgs, AllowTools): Settings offers
 * an agent whose adapter registers Conductor's MCP server and has a read-only mode, shows the model field only for one with a model
 * flag, and the tools switch only for one that can allow a server's tools. The server checks it again on the save.
 */
export const BRAKEMAN_ADAPTERS: Readonly<Record<string, { model: boolean; allowTools: boolean }>> = {
  claude: { model: true, allowTools: true },
  codex: { model: true, allowTools: true },
}

/**
 * The Agent select's value for no agent. A select's item may not have "" as its value (Reka's SelectItem throws: "" clears the
 * selection), so None is this, which no catalog id can be (ids are `^[a-z0-9-]{1,32}$`, internal/catalog), and stands for `agentId` "".
 */
export const NO_AGENT = '_none'

/** The Agent select's items: None, then each agent offered, by its catalog id. */
export function agentItems(offered: ReadonlyArray<{ id: string; name: string; available?: boolean }>): Array<{ label: string; value: string }> {
  return [{ label: 'None', value: NO_AGENT }, ...offered.map((a) => ({ label: a.available === false ? `${a.name} (not installed)` : a.name, value: a.id }))]
}

/** The Agent select's value for a configuration's `agentId`. */
export function agentChoice(agentId: string | undefined): string {
  return agentId || NO_AGENT
}

/** The `agentId` the Agent select's value saves. */
export function agentIdOf(choice: string): string {
  return choice === NO_AGENT ? '' : choice
}

/** The configuration's bounds (internal/brakeman/config.go). */
export const BOUNDS = {
  turnMinutes: [1, 60],
  readsPerTurn: [10, 2000],
  turnsPerDay: [1, 500],
  minutesPerDay: [10, 1440],
  windowSeconds: [10, 900],
  cooldownMinutes: [0, 1440],
  questionMinutes: [1, 1440],
  briefings: 24,
  selector: 50,
  path: 4096,
  scopeBytes: 8 << 10,
} as const

const MODEL = /^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,99}$/
const SCOPE_PATTERNS: Record<keyof BrakemanScope, { re?: RegExp; words: string }> = {
  projects: { re: /^[A-Z][A-Z0-9]{1,5}$/, words: 'a project key, such as CND' },
  repos: { words: 'an absolute folder' },
  crews: { re: /^[a-z0-9][a-z0-9-]{0,63}$/, words: "a crew's id" },
  runs: { re: RUN, words: "a run's id" },
  sessions: { re: HEX, words: "a session's id, 16 hex digits" },
}

/**
 * How many bytes Go's json.Marshal writes for a scope (the server's MaxScopeBytes check, Scope.validate): its selectors in the
 * struct's order, an empty one left out (omitempty), each string escaped as encoding/json escapes it: `"`, `\`, and \b, \f, \n,
 * \r and \t short, other control characters and `<`, `>`, `&`, U+2028 and U+2029 as `\u` and four digits.
 */
export function scopeJSONBytes(scope: BrakemanScope): number {
  const str = (v: string) => {
    let n = 2
    for (const ch of v) {
      const c = ch.codePointAt(0)!
      if (ch === '"' || ch === '\\' || ch === '\b' || ch === '\f' || ch === '\n' || ch === '\r' || ch === '\t') n += 2
      else if (c < 0x20 || ch === '<' || ch === '>' || ch === '&' || c === 0x2028 || c === 0x2029) n += 6
      else n += c < 0x80 ? 1 : c < 0x800 ? 2 : c < 0x10000 ? 3 : 4
    }
    return n
  }
  const parts: number[] = []
  for (const key of ['projects', 'repos', 'crews', 'runs', 'sessions'] as const) {
    const list = scope[key]
    if (!list?.length) continue
    // "key":[a,b]
    parts.push(key.length + 3 + 2 + list.reduce((sum, v) => sum + str(v), 0) + (list.length - 1))
  }
  return 2 + parts.reduce((a, b) => a + b, 0) + Math.max(0, parts.length - 1)
}

/** Why a folder is refused as the server refuses it (absPath), or "". */
function folderProblem(p: string): string {
  if (new TextEncoder().encode(p).length > BOUNDS.path) return `at most ${BOUNDS.path} bytes`
  if (!p.startsWith('/')) return 'must be an absolute path'
  if (p !== cleanPath(p)) return `must be a clean path (${cleanPath(p)})`
  return ''
}

/** A path as Go's filepath.Clean writes an absolute one. */
function cleanPath(p: string): string {
  const out: string[] = []
  for (const part of p.split('/')) {
    if (part === '' || part === '.') continue
    if (part === '..') out.pop()
    else out.push(part)
  }
  return '/' + out.join('/')
}

/**
 * The configuration form's checks, mirroring the server's Validate: each field's words by its name as the server names it
 * (`budget.turnMinutes`, `scope.runs`), empty when the form may be saved. `adapter` is the chosen agent's adapter ("" for an agent
 * with none), or null while the catalog is not read: the server checks the agent then.
 */
export function configProblems(c: BrakemanConfig, adapter: string | null): Record<string, string> {
  const out: Record<string, string> = {}
  const caps = adapter ? BRAKEMAN_ADAPTERS[adapter] : undefined
  if (c.agentId) {
    if (adapter === null) {
      /* the server checks it */
    } else if (!caps) out.agentId = "This agent cannot be Brakeman's: its adapter registers no Conductor tools or has no read-only mode."
    else if (c.model && !caps.model) out.model = 'This agent takes no model from Conductor: leave it empty for its default.'
    else if (c.allowConductorTools && !caps.allowTools) out.allowConductorTools = "This agent cannot be told to allow Conductor's tools: it asks before each."
  } else if (c.enabled) out.agentId = 'Pick the agent Brakeman runs on.'
  else if (c.model) out.model = 'A model needs an agent.'
  if (c.model && !out.model && !MODEL.test(c.model)) out.model = 'A model is letters, digits and . _ : / @ -, at most 100, starting with a letter or a digit.'
  if (c.account) {
    const p = folderProblem(c.account)
    if (p) out.account = `The account folder ${p}.`
  }
  if (!c.cwd) {
    if (c.enabled) out.cwd = 'Brakeman on needs the folder its turns run in.'
  } else {
    const p = folderProblem(c.cwd)
    if (p) out.cwd = `The folder ${p}.`
  }
  for (const key of Object.keys(SCOPE_PATTERNS) as Array<keyof BrakemanScope>) {
    const list = c.scope[key] ?? []
    const pat = SCOPE_PATTERNS[key]
    if (list.length > BOUNDS.selector) out[`scope.${key}`] = `At most ${BOUNDS.selector}.`
    const bad = list.find((v) => (pat.re ? !pat.re.test(v) : folderProblem(v) !== ''))
    if (bad !== undefined) out[`scope.${key}`] = `${bad} is not ${pat.words}.`
    const twice = list.find((v, i) => list.indexOf(v) !== i)
    if (twice !== undefined) out[`scope.${key}`] = `${twice} is named twice.`
  }
  if (scopeJSONBytes(c.scope) > BOUNDS.scopeBytes) out.scope = `The scope is at most ${BOUNDS.scopeBytes >> 10} KiB as the server keeps it: name fewer, or shorter.`
  const b = c.budget
  const bound = (field: keyof BrakemanBudget, words: string) => {
    const [lo, hi] = BOUNDS[field]
    const v = b[field]
    if (!Number.isInteger(v) || v < lo || v > hi) out[`budget.${field}`] = `${words} are ${lo} to ${hi}.`
  }
  bound('turnMinutes', 'Minutes a turn')
  bound('readsPerTurn', 'Reads a turn')
  bound('turnsPerDay', 'Turns a day')
  bound('minutesPerDay', 'Minutes a day')
  const w = c.wake
  if (w.mode !== 'invoked' && w.mode !== 'background') out['wake.mode'] = 'Brakeman is woken only when asked, or in the background too.'
  const wakeBound = (field: 'windowSeconds' | 'cooldownMinutes' | 'questionMinutes', words: string) => {
    const [lo, hi] = BOUNDS[field]
    const v = w[field]
    if (!Number.isInteger(v) || v < lo || v > hi) out[`wake.${field}`] = `${words} ${lo} to ${hi}.`
  }
  wakeBound('windowSeconds', 'The window is')
  wakeBound('cooldownMinutes', 'The cooldown is')
  wakeBound('questionMinutes', 'A question wakes it after')
  const times = w.briefings ?? []
  const badTime = times.find((t) => !BRIEFING_TIME.test(t))
  const twiceTime = times.find((t, i) => times.indexOf(t) !== i)
  if (times.length > BOUNDS.briefings) out['wake.briefings'] = `At most ${BOUNDS.briefings} briefing times.`
  else if (badTime !== undefined) out['wake.briefings'] = `${badTime} is not a time of day: HH:MM, 00:00 to 23:59.`
  else if (twiceTime !== undefined) out['wake.briefings'] = `${twiceTime} is named twice.`
  return out
}

/** A briefing time, as the server takes one (internal/brakeman/config.go, briefingPattern). */
export const BRIEFING_TIME = /^([01][0-9]|2[0-3]):[0-5][0-9]$/

/** The briefing times as the form's text holds them, comma or space apart, and back. */
export function briefingLine(times: readonly string[] | undefined): string {
  return (times ?? []).join(', ')
}
export function briefingList(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((t) => t.trim())
    .filter(Boolean)
}

/**
 * The form's field a refusal belongs to: the server names an entry of a selector by its index (`scope.repos[3]`), the form shows the
 * selector's words. A field the form draws no control for (`permissions.observe`, `wake.mode`, a model while the agent shows no model
 * field) is the caller's to show elsewhere.
 */
export function formField(field: string): string {
  return field.replace(/\[\d+\]$/, '')
}

/** A wake source firing, as the role's log keeps it (internal/crew/role.go, RoleLogRow). */
export interface RoleLogRow {
  source: string
  subject?: string
  key?: string
  at: string
  batch?: string
  request?: string
  held?: string
}

/** A batch's wakes of one source and subject, coalesced. */
export interface BatchWake {
  source: string
  subject?: string
  count: number
  first: string
  last: string
}

/** A batch: the wakes one turn reads. */
export interface RoleBatch {
  id: string
  openedAt: string
  wakes: BatchWake[]
  overflow?: { count: number; first: string; last: string }
}

/** One of the role's notes: text a person kept from a brief. */
export interface RoleNote {
  text: string
  from?: string
  at: string
}

/** The background role (brakeman/role.json) as GET /api/brakeman and GET /api/brakeman/role answer it. */
export interface BrakemanRole {
  state: string
  why?: string
  retryAt?: string
  /** What made it unavailable (start, budget, off, stuck), which says what else tries again. */
  reason?: string
  /** The turns in a row whose agent did not start. */
  tries?: number
  last?: number
  open?: RoleBatch
  fired?: RoleBatch
  request?: string
  coolUntil?: string
  briefed?: string
  notes?: RoleNote[]
  updatedAt?: string
  minutesToday?: number
}

/** Whether two configurations hold the same settings, whatever their revisions: a form untouched since it was read is its base. */
export function sameConfig(a: BrakemanConfig, b: BrakemanConfig): boolean {
  const norm = (c: BrakemanConfig) => {
    const { rev: _rev, updatedAt: _at, ...rest } = c
    // No grant is as none (the server leaves an empty act out), and a grant list is the same whatever its order.
    const act = rest.permissions?.act?.length ? [...rest.permissions.act].sort() : undefined
    return stable({ ...rest, account: rest.account || undefined, model: rest.model || undefined, permissions: { ...rest.permissions, act }, scope: Object.fromEntries(Object.entries(rest.scope ?? {}).filter(([, v]) => v?.length)) })
  }
  return norm(a) === norm(b)
}

/** JSON with every object's keys in order, so two values are compared by what they hold. */
function stable(v: unknown): string {
  if (Array.isArray(v)) return `[${v.map(stable).join(',')}]`
  if (v && typeof v === 'object') {
    return `{${Object.entries(v as Record<string, unknown>)
      .filter(([, x]) => x !== undefined)
      .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
      .map(([k, x]) => `${JSON.stringify(k)}:${stable(x)}`)
      .join(',')}}`
  }
  return JSON.stringify(v)
}

/**
 * What Stop does to the request holding the slot: an open one is cancelled (the server stops its run after); a closed one the desk
 * still holds (the brief's grace for the agent's last output, a stop that left a process) has its run stopped, since cancelling a
 * closed request does nothing; one with no run has nothing to stop.
 */
export function stopAction(r: Pick<BrakemanRequest, 'state' | 'runId'>): 'cancel' | 'stop_run' | null {
  if (!closed(r.state)) return 'cancel'
  return r.runId ? 'stop_run' : null
}

/** A scope selector as the form's text holds it, one entry a line, and back. */
export function scopeLines(list: readonly string[] | undefined): string {
  return (list ?? []).join('\n')
}
export function scopeList(text: string): string[] {
  return text
    .split('\n')
    .map((s) => s.trim())
    .filter(Boolean)
}

/**
 * A brief's mark as opened in a browser: its request's id and when the server wrote it, which tells one server's br-3 from another's
 * (a new token may be another server's, whose br-3 is not the one opened).
 */
export function openedMark(r: Pick<BrakemanRequest, 'id' | 'createdAt'>): string {
  return `${r.id}@${r.createdAt}`
}

/** The briefed requests of a list this browser has not opened (their openedMark not among `opened`), newest first. */
export function unopened(requests: Iterable<Pick<BrakemanRequest, 'id' | 'hasBrief' | 'createdAt'>>, opened: ReadonlySet<string>): string[] {
  const out: string[] = []
  for (const r of requests) if (r.hasBrief && !opened.has(openedMark(r))) out.push(r.id)
  return out
}

/** A request's number, for ordering: br-12 is 12. */
export function requestNumber(id: string): number {
  return Number(id.slice(3)) || 0
}

/**
 * The operations the person may grant Brakeman (internal/brakeman, Grantable; docs/plans/brakeman.md, BK2), in Settings' order, each
 * with what it authorizes in words. None approves, answers, accepts, closes or changes the configuration: those stay the person's,
 * whatever is granted. A grant whose route this server does not serve is shown as not in this build (the admin's guide lists the ones it
 * serves, under the scope act), and the server refuses it at the save.
 */
export const GRANTS: ReadonlyArray<{ value: string; label: string; words: string }> = [
  {
    value: 'launch_approved_plan',
    label: 'Launch an approved plan',
    words: 'Starts a run from a plan you approved. It never approves one.',
  },
  {
    value: 'request_repair',
    label: 'Request a repair',
    words: 'Asks a running task for changes, as its own review. It never accepts a result.',
  },
  {
    value: 'prepare_followup',
    label: 'Prepare a follow-up',
    words: 'Proposes the next plan for a ticket. It runs only once you approve it.',
  },
  {
    value: 'route_decision',
    label: 'Route a decision',
    words: 'Sends an open decision to you or a crew member. It never answers one.',
  },
]

/** A grant's label, or its name for one this page does not know. */
export function grantLabel(op: string): string {
  return GRANTS.find((g) => g.value === op)?.label ?? op
}

/** The ticket a ref names, for a ticket:, comment: or plan: ref; "" for any other. */
export function refTicket(ref: string): string {
  const r = parseRef(ref)
  return r && (r.kind === 'ticket' || r.kind === 'comment' || r.kind === 'plan') ? r.ticket! : ''
}

/**
 * The act Brakeman ran for a proposal, when it ran one: the request's act whose opId the proposal's arguments name (`args.opId`, as the
 * guide asks of a proposal of an act it ran), of the proposal's operation on the proposal's ticket (an act's target is its ticket; the
 * proposal's a ticket: or a plan: of it). Nothing weaker ties them: two proposals of one operation on one ticket (two revisions, two
 * follow-ups) are told apart only by the act each names, so a proposal that names none, or names an opId of no such act, stays a
 * proposal, whatever else Brakeman ran. An act not run (refused before its operation, the server stopped first) is still the
 * proposal's: the card says so.
 */
export function actFor(p: Extract<BriefItem, { kind: 'proposal/1' }>, acts: readonly BrakemanAct[] | undefined): BrakemanAct | undefined {
  const opId = typeof p.args?.opId === 'string' ? p.args.opId : ''
  if (!opId || !acts?.length) return undefined
  if (p.op === 'route_decision') {
    // A route's act and its proposal name the decision: decision:<dr-n>.
    const d = refDecision(p.target)
    return d ? acts.find((a) => a.opId === opId && a.operation === p.op && refDecision(a.target) === d) : undefined
  }
  const ticket = refTicket(p.target)
  if (!ticket) return undefined
  return acts.find((a) => a.opId === opId && a.operation === p.op && refTicket(a.target) === ticket)
}

/** The decision a decision: ref names; "" for any other. */
export function refDecision(ref: string): string {
  const r = parseRef(ref)
  return r?.kind === 'decision' ? r.decision! : ''
}

/** The decision a done route_decision act routed, from its receipt (DR1's, dr-<n>#<seq>); "" for any other act. */
export function actDecision(a: BrakemanAct): string {
  const m = a.state === 'done' && a.operation === 'route_decision' ? /^(dr-[1-9][0-9]*)#[1-9][0-9]*$/.exec(a.receipt ?? '') : null
  return m ? m[1]! : ''
}

/**
 * The acts of a request that no proposal of its brief shows (actFor), in the order Brakeman ran them: the brief page shows them apart,
 * under "What Brakeman did", each with its receipt, so every act a request holds shows on the page whether or not the brief names it
 * (two real models briefed an act they ran without a proposal naming it, and it showed nowhere).
 */
export function actsApart(items: readonly BriefItem[], acts: readonly BrakemanAct[] | undefined): BrakemanAct[] {
  if (!acts?.length) return []
  const shown = new Set<BrakemanAct>()
  for (const it of items) {
    if (it.kind !== 'proposal/1') continue
    const a = actFor(it, acts)
    if (a) shown.add(a)
  }
  return acts.filter((a) => !shown.has(a))
}

/**
 * Brakeman's reading as the brief page draws it: the summary, then what it is unsure of, each Markdown rendered the same way (a list
 * written in either is drawn as a list); the uncertainty only when it has one.
 */
export function readingBlocks(b: Pick<BrakemanBrief, 'summary' | 'uncertainty'>): Array<{ key: 'summary' | 'uncertainty'; label?: string; text: string }> {
  const out: Array<{ key: 'summary' | 'uncertainty'; label?: string; text: string }> = [{ key: 'summary', text: b.summary }]
  if (b.uncertainty?.trim()) out.push({ key: 'uncertainty', label: 'Uncertain', text: b.uncertainty })
  return out
}

/**
 * The launch an act's receipt names (GET /api/dispatch/launches/{id}), as much of it as the act's card needs: Dispatch keeps the launch,
 * and its receipt done, before the run is started, so the launch's own state says what came of it: pending while its run is being
 * started, started with its run, failed with its words when the run never started.
 */
export interface ActLaunch {
  state: 'pending' | 'started' | 'failed' | string
  runId?: string
  why?: string
}

/**
 * What a proposal Brakeman ran shows of its act. A launch's act is done once its receipt is in, but the launch is done only once its
 * run has started: until its launch is read, and while the launch is pending, it is under way (pending); a launch that failed is
 * `failed`, never "Brakeman did this". Any other act is its own state.
 */
export function actShown(a: BrakemanAct, launch?: ActLaunch | null): string {
  if (!actLaunchRef(a)) return a.state
  if (!launch || launch.state === 'pending') return 'pending'
  return launch.state === 'started' ? 'done' : 'failed'
}

/** The launch an act's receipt names, by its id ("" for an act that is not a launch's done act): the key its launch is kept under. */
export function actLaunchId(a: BrakemanAct): string {
  const ref = actLaunchRef(a)
  return ref ? ref.slice('launch:'.length) : ''
}

/**
 * What a proposal, or an act's card, shows of the act, drawn afresh each time from the request's act and the launch its receipt names
 * as last read (kept by the launch's id, never by the act's object): a re-read of the request hands the same act as a new object, and
 * it shows what it showed, a launch that failed still failed. "" with no act.
 */
export function actShownFrom(a: BrakemanAct | undefined | null, launches: Readonly<Record<string, ActLaunch>>): string {
  if (!a) return ''
  const id = actLaunchId(a)
  return actShown(a, id ? (launches[id] ?? null) : null)
}

/** Whether a proposal offers its own control: not while Brakeman's act of it is done or under way (a launch that failed is offered again). */
export function proposalOffered(shown: string): boolean {
  return shown !== 'done' && shown !== 'pending'
}

/** The launches the page keeps read at most: the oldest read is dropped past it. */
export const MAX_KEPT_LAUNCHES = 200

/** The launches kept with `id`'s read `l` added (or replaced), at most MAX_KEPT_LAUNCHES of them, the oldest dropped first. */
export function keepLaunch<L extends ActLaunch>(kept: Readonly<Record<string, L>>, id: string, l: L): Record<string, L> {
  const out: Record<string, L> = { ...kept }
  delete out[id]
  out[id] = l
  const keys = Object.keys(out)
  for (const k of keys.slice(0, Math.max(0, keys.length - MAX_KEPT_LAUNCHES))) delete out[k]
  return out
}

/**
 * What a proposal Brakeman ran says of the act: done with its receipt, under way, refused with the words, or never run. A launch's act
 * says so by its launch (actShown): its run still being started, or the launch failed with its words and no run.
 */
export function actWords(a: BrakemanAct, launch?: ActLaunch | null): string {
  const what = grantLabel(a.operation)
  const shown = actShown(a, launch)
  const receipt = actReceiptRef(a) ? `: receipt ${a.op}` : ''
  const made = actMade(a)
  if (a.state === 'done' && actLaunchRef(a)) {
    if (shown === 'pending') return `Brakeman launched this, under the grant "${what}"${receipt}; ${made}. ${launch ? 'Its run is still being started.' : 'Whether its run has started is not read yet.'}`
    if (shown === 'failed') return `Brakeman launched this, under the grant "${what}"${receipt}, but ${made} failed${launch?.why ? `: ${launch.why}` : ''}. No run was started.`
  }
  switch (a.state) {
    case 'done':
      return `Brakeman did this, under the grant "${what}" you gave it${receipt}${made ? `; ${made}` : ''}.`
    case 'pending':
      return `Brakeman is doing this, under the grant "${what}": its receipt is not in yet.`
    case 'refused':
      return `Brakeman tried this, under the grant "${what}", and it was refused${a.why ? `: ${a.why}` : ''}. Nothing was done.`
    case 'not_run':
      return 'Brakeman began this, but the server stopped before it ran: it was not done, and it is not run again.'
  }
  return a.state
}

/**
 * The ref of a done act's receipt, for its receipt chip: op:brakeman/<op>, the act's derived key, under which Dispatch keeps the receipt,
 * the ref an outcome of its brief cites it by (the act tool's receiptRef); null for an act not done. The receipt's own id (a.receipt) is
 * what the operation made, a launch's or a revision's, never named the receipt (actMade).
 */
export function actReceiptRef(a: BrakemanAct): string | null {
  // A route's receipt is DR1's, in the decision's history: no op: ref names it.
  return a.state === 'done' && a.operation !== 'route_decision' && OP.test(a.op ?? '') ? `op:brakeman/${a.op}` : null
}

/** The ref of the launch a done launch_approved_plan act made, for its launch chip and the launch it is read by; else null. */
export function actLaunchRef(a: BrakemanAct): string | null {
  return a.state === 'done' && a.operation === 'launch_approved_plan' && a.receipt && HEX.test(a.receipt) ? `launch:${a.receipt}` : null
}

/** What a done act made, by its receipt's id, in words: the launch, the plan revision or the review of its ticket; "" with none. */
export function actMade(a: BrakemanAct): string {
  if (a.state !== 'done' || !a.receipt) return ''
  const ticket = refTicket(a.target)
  const of = ticket ? ` of ${ticket}` : ''
  switch (a.operation) {
    case 'launch_approved_plan':
      return `the launch ${a.receipt}${of}`
    case 'prepare_followup':
      return `the plan revision ${a.receipt}${of}`
    case 'request_repair':
      return `the review ${a.receipt}${of}`
    case 'route_decision': {
      const [d, seq] = a.receipt.split('#')
      return `${d} routed: row ${seq} of its history`
    }
  }
  return `${a.receipt}, what it made`
}

/** How followLaunch reads a launch, and when it stops. */
export interface FollowLaunch<L extends ActLaunch> {
  /** The launch read now: resolves with it; rejects when the read failed. */
  read: () => Promise<L>
  /** What the card shows, given each launch a read answered. */
  show: (l: L) => void
  /** Whether it is no longer wanted (the card shows another act, or is gone): nothing more is read or shown. */
  stop: () => boolean
  /** Waits that many milliseconds before the launch is read again. */
  wait: (ms: number) => Promise<void>
  /** The first wait, doubled after each read that left the launch unsettled, up to `maxMs`. */
  firstMs: number
  maxMs: number
}

/** A launch still pending, or a read of it that failed, is read again after this, doubled each time up to LAUNCH_FOLLOW_MAX_MS. */
export const LAUNCH_FOLLOW_FIRST_MS = 2000
export const LAUNCH_FOLLOW_MAX_MS = 8000

/**
 * The launch an act's receipt names, read until it is settled: a launch pending (its run still being started) and a read that failed
 * are read again after `firstMs`, doubled up to `maxMs`, until the launch has started or failed, or it is no longer wanted. Each answer
 * is shown as it comes. Answers the settled launch, or null once stopped.
 */
export async function followLaunch<L extends ActLaunch>(o: FollowLaunch<L>): Promise<L | null> {
  let delay = o.firstMs
  for (;;) {
    if (o.stop()) return null
    try {
      const l = await o.read()
      if (o.stop()) return null
      o.show(l)
      if (l.state !== 'pending') return l
    } catch {
      if (o.stop()) return null
    }
    await o.wait(delay)
    delay = Math.min(delay * 2, o.maxMs)
  }
}

/** A grant list with `op` granted (on) or not, in GRANTS' order, each once: what Settings' switches save as `permissions.act`. */
export function withGrant(act: readonly string[] | undefined, op: string, on: boolean): string[] {
  const set = new Set(act ?? [])
  if (on) set.add(op)
  else set.delete(op)
  return [...GRANTS.map((g) => g.value).filter((v) => set.has(v)), ...[...set].filter((v) => !GRANTS.some((g) => g.value === v))]
}

/**
 * Whether the permissions leave Brakeman to observe alone, as Settings says: nothing drafted, nothing proposed and no operation granted.
 * A grant is checked by itself (the server's actGrant), so with drafting and proposing off a turn granted a launch still launches.
 */
export function observesOnly(p: Pick<BrakemanPermissions, 'draft' | 'propose' | 'act'>): boolean {
  return !p.draft && !p.propose && !p.act?.length
}

/**
 * The grants a server serves, from its admin guide's operations (the scope act, an entry each under the grant's name), or null while the
 * guide is not read: Settings offers those alone, and says of the rest that this build does not serve them (the server refuses them at
 * the save, 400 invalid_config on permissions.act).
 */
export function servedGrants(ops: ReadonlyArray<{ name: string; scope?: string }> | null): Set<string> | null {
  return ops ? new Set(ops.filter((o) => o.scope === 'act' && o.name !== 'act').map((o) => o.name)) : null
}

/**
 * Whether Settings' switch for the grant `op` is held still: only turning a grant on is, while the guide is unread or its read failed
 * (`served` null) or for a grant this server serves no route of. A grant that is on can always be turned off, whatever the guide: taking
 * a grant away is its revoke, which needs nothing the guide says and never waits on a read (the Program's article 4).
 */
export function grantHeld(served: ReadonlySet<string> | null, on: boolean, op: string): boolean {
  return !on && (!served || !served.has(op))
}
