// Brakeman on the app's pages (docs/plans/brakeman.md, "web/app: Settings, the Brakeman page and a brief"): the records as
// GET /api/brakeman and its requests answer them (docs/protocol.md, Brakeman), the words the pages say of them, where a ref leads
// and what it is called, the order a brief's items are shown in, and the configuration form's checks, which mirror the server's
// (internal/brakeman/config.go, Validate) so the form says what is wrong before a save is refused for it. Pure: no Nuxt, no fetch.

/** What Brakeman may see, a switch a class. */
export interface BrakemanVisibility {
  runs: boolean
  logs: boolean
  dispatch: boolean
  sessions: boolean
  touched: boolean
  terminals: boolean
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
  wake: { mode: string }
  budget: BrakemanBudget
  rev: number
  updatedAt?: string
}

/** The agent a turn ran: its id, adapter, probed version, model, and whether an account was set. */
export interface BrakemanStamp {
  agentId?: string
  adapter?: string
  version?: string
  model?: string
  account?: boolean
}

/** A draft made work by a person, or its claim (PR-7 shows them). */
export interface BrakemanMade {
  item: number
  opId: string
  state: string
  key?: string
  crew?: string
  why?: string
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
  strayRun?: string
  closedAt?: string
}

/** GET /api/brakeman's answer. */
export interface BrakemanState {
  config: BrakemanConfig
  current: BrakemanRequest | null
  queue: BrakemanRequest[]
  turnsToday: number
  /** The server's words for the agent saved, when its read-only mode does not hold (brakeman.Warnings). */
  warnings?: string[]
  readOnly?: boolean
  why?: string
  stuck?: boolean
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
]

/** What the page calls a request's kind. */
export function kindWords(kind: string): string {
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
}

/** A note's bound, in bytes of UTF-8 (brakeman.MaxNote). */
export const MAX_NOTE = 16 << 10

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
      return "not started: today's turns were spent"
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
}

const RUN = /^[a-z0-9][a-z0-9-]{0,63}-[0-9a-f]{8}$/
const TASK = /^[a-z0-9][a-z0-9_-]{0,39}$/
const OP = /^[A-Za-z0-9._~-]{1,32}$/
const HEX = /^[0-9a-f]{16}$/
const TICKET = /^[A-Z][A-Z0-9]{1,5}-[1-9][0-9]{0,17}$/
const COMMIT = /^[0-9a-f]{40}([0-9a-f]{24})?$/
const REQUEST = /^br-[1-9][0-9]{0,17}$/
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
  }
  return null
}

/** A ticket's page: /dispatch/<project>/<number>, at a comment when one is named. */
function ticketPath(key: string, comment?: string): string {
  const dash = key.lastIndexOf('-')
  const path = `/dispatch/${encodeURIComponent(key.slice(0, dash))}/${key.slice(dash + 1)}`
  return comment ? `${path}#comment-${encodeURIComponent(comment)}` : path
}

/**
 * Where a person follows a ref in the app (the server's refURL, internal/api/brakeman_refs.go), or null for one with no page: a
 * Dispatch receipt. A check receipt and a command receipt are read from the API with the token, which a link would not carry, so
 * their chip leads to the run page at the task, where the receipt is shown.
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
      return `${run}?task=${encodeURIComponent(r.task!)}`
    case 'attempt':
    case 'check':
      return `${run}?task=${encodeURIComponent(r.task!)}&attempt=${r.n}`
    case 'session':
    case 'question':
      return `/sessions/${encodeURIComponent(r.session!)}`
    case 'file':
      return `/sessions/${encodeURIComponent(r.session!)}?tab=files&open=${encodeURIComponent(r.path!)}`
    case 'ticket':
      return ticketPath(r.ticket!)
    case 'comment':
      return ticketPath(r.ticket!, r.comment)
    case 'brief':
      return `/brakeman?request=${encodeURIComponent(r.request!)}#item-${r.item}`
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
  }
  return ref
}

/** Whether a ref's record moved since the brief was kept: the live state or revision the page has differs from the resolution's `now`. */
export function changedSince(now: RefState | undefined, live: { state?: string; rev?: string } | undefined): boolean {
  if (!now || !live) return false
  if (live.state !== undefined && now.state !== undefined && live.state !== now.state) return true
  return live.rev !== undefined && now.rev !== undefined && live.rev !== now.rev
}

/** The groups a brief's items are shown in, in this order (docs/plans/brakeman.md, PR-6); comparisons go with the evidence panels. */
export const ITEM_GROUPS: ReadonlyArray<{ group: string; title: string; kinds: readonly string[] }> = [
  { group: 'facts', title: 'Facts', kinds: ['fact/1'] },
  { group: 'interpretations', title: 'Interpretations', kinds: ['interpretation/1'] },
  { group: 'decisions', title: 'Decisions', kinds: ['decision/1'] },
  { group: 'proposals', title: 'Proposals', kinds: ['proposal/1'] },
  { group: 'outcomes', title: 'Verified outcomes', kinds: ['outcome/1'] },
  { group: 'drafts', title: 'Drafts', kinds: ['draft_ticket/1', 'draft_crew/1'] },
  { group: 'handoffs', title: 'Handoffs', kinds: ['handoff/1'] },
  { group: 'overlaps', title: 'Overlapping changes', kinds: ['overlap/1'] },
  { group: 'evidence', title: 'Evidence', kinds: ['comparison/1', 'evidence/1'] },
]

/** A brief's item as a group shows it: the item, its index in the brief (its anchor, `item-<index>`), and a fact's number. */
export interface GroupedItem {
  item: BriefItem
  index: number
  /** A fact's number among the brief's facts, from 1, as an interpretation names it. */
  fact?: number
}

/** A brief's items in their groups, in ITEM_GROUPS' order and the brief's own within a group; an empty group is left out. */
export function groupItems(items: readonly BriefItem[]): Array<{ group: string; title: string; items: GroupedItem[] }> {
  const facts = factNumbers(items)
  return ITEM_GROUPS.map((g) => ({
    group: g.group,
    title: g.title,
    items: items.flatMap((item, index) => (g.kinds.includes(item.kind) ? [{ item, index, fact: facts.get(index) }] : [])),
  })).filter((g) => g.items.length > 0)
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

/** A brief's provenance line: the agent, its model, its version, the request and when the brief was kept. */
export function provenance(r: Pick<BrakemanRequest, 'id' | 'agent' | 'briefAt'>, agentName?: string, at?: (iso: string) => string): string {
  const a = r.agent ?? {}
  const parts = [agentName || a.agentId || 'an agent', a.model ? `model ${a.model}` : 'its default model']
  if (a.version) parts.push(`version ${a.version}`)
  parts.push(`request ${r.id}`)
  if (r.briefAt) parts.push(`as of ${at ? at(r.briefAt) : r.briefAt}`)
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

/** The configuration's bounds (internal/brakeman/config.go). */
export const BOUNDS = {
  turnMinutes: [1, 60],
  readsPerTurn: [10, 2000],
  turnsPerDay: [1, 500],
  selector: 50,
  path: 4096,
} as const

const MODEL = /^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,99}$/
const SCOPE_PATTERNS: Record<keyof BrakemanScope, { re?: RegExp; words: string }> = {
  projects: { re: /^[A-Z][A-Z0-9]{1,5}$/, words: 'a project key, such as CND' },
  repos: { words: 'an absolute folder' },
  crews: { re: /^[a-z0-9][a-z0-9-]{0,63}$/, words: "a crew's id" },
  runs: { re: RUN, words: "a run's id" },
  sessions: { re: HEX, words: "a session's id, 16 hex digits" },
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
  } else if (c.enabled) out.agentId = 'Brakeman on needs an agent.'
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
  const b = c.budget
  const bound = (field: keyof BrakemanBudget, words: string) => {
    const [lo, hi] = BOUNDS[field]
    const v = b[field]
    if (!Number.isInteger(v) || v < lo || v > hi) out[`budget.${field}`] = `${words} are ${lo} to ${hi}.`
  }
  bound('turnMinutes', 'Minutes a turn')
  bound('readsPerTurn', 'Reads a turn')
  bound('turnsPerDay', 'Turns a day')
  return out
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

/** The briefed requests of a list this browser has not opened, newest first. */
export function unopened(requests: Iterable<Pick<BrakemanRequest, 'id' | 'hasBrief'>>, opened: ReadonlySet<string>): string[] {
  const out: string[] = []
  for (const r of requests) if (r.hasBrief && !opened.has(r.id)) out.push(r.id)
  return out
}

/** A request's number, for ordering: br-12 is 12. */
export function requestNumber(id: string): number {
  return Number(id.slice(3)) || 0
}
