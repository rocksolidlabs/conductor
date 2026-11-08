import { relativeTime } from './sessions'

// History (Program item HS1, docs/plans/history.md, "web/app", PR-6): the
// types of the history routes' replies (docs/protocol.md, History) and the
// words a row and a session's record are shown in. Pure: the composable
// (useHistory.ts) reads, the pages show. Every row is one of three kinds of
// evidence that never mix, what was said, what was decided and what was
// checked, beside the plain records they are about; only a check that
// passed is ever drawn with a ✓.

/** An answer of availability or eligibility: `unknown` is never said as gone. */
export type Answer = 'yes' | 'no' | 'unknown'

export type HistoryClass = 'said' | 'decided' | 'checked' | 'record'

/** Whether the thing an entry names can still be opened or resumed, computed as the page is answered. */
export interface Availability {
  state: string
  words: string
  /** A session's record: whether Resume is offered, and why not or what it asks (`confirm`: a lost record's second yes). */
  resume?: { offered: Answer; why?: string; confirm?: boolean; url?: string }
  /** An attempt's result commit and whether its repository holds it. */
  commit?: { id: string; present: Answer }
  branch?: string
  /** A receipt's log: its size and SHA-256. */
  log?: { size: number; sha256: string }
}

/** A link seen from an entry: its relation, which way it goes, the other end and, when the index holds it, that end's class and outcome. */
export interface HistoryLink {
  rel: string
  dir: 'out' | 'in'
  ref: string
  label: string
  key?: string
  class?: HistoryClass
  outcome?: string
  url?: string
}

/** An entry as the search routes serve it (HistoryItem: utils/dispatch.ts has a HistoryEntry of its own, a ticket's field history): what it is, its six fields, the page that shows its evidence, its links. */
export interface HistoryItem {
  key: string
  kind: string
  class: HistoryClass
  outcome?: string
  title: string
  summary?: string
  snippet?: string
  at: string
  actor?: string
  source: { store: string; label: string; ref: string }
  host: { name?: string; user?: string; hosted?: boolean; where?: string }
  project: { key?: string; repo?: string; tree?: string }
  revision: { label?: string; commit?: string }
  agent?: { id: string; name?: string; version?: string; session?: string }
  crew?: string
  run?: string
  ticket?: string
  anchor: { ref: string; label: string; url?: string }
  url: string
  repeats: { count: number; anchors: number }
  links: HistoryLink[]
  availability: Availability
}

/** One entry of a session's record: what passed through Conductor, in order. */
export interface RecordEntry {
  seq: number
  at: string
  kind: 'typed' | 'question' | 'answer' | 'chat' | 'report' | 'status' | string
  id?: string
  ref?: string
  event?: string
  byName?: string
  message?: string
  op?: string
  path?: string
  tool?: string
  url?: string
  to?: string
  choices?: string[]
  outcome?: string
}

/** A resume's receipt, kept in the record it resumed from. */
export interface ResumeReceipt {
  commandId: string
  actor?: string
  session?: string
  state: 'pending' | 'started' | 'failed' | 'uncertain'
  code?: string
  error?: string
  at: string
}

/** A session's record as `GET /api/history/sessions/{id}` serves it. */
export interface SessionRecord {
  v: number
  id: string
  seq: number
  name: string
  kind: string
  agent: { id: string; name?: string; adapter?: string; version?: string; session?: { id: string; source?: string; resumable: boolean } }
  host: { name?: string; user?: string; owner?: string; hosted?: boolean }
  where?: string
  cwd: string
  repo?: string
  repoCommon?: string
  branch?: string
  startCommit?: string
  startFrom?: string
  endCommit?: string
  launch?: { args?: string[]; argCount: number; argsSha256?: string; argsKept: boolean; envNames?: string[]; envCount?: number; envSha256?: string; yolo?: boolean; renamed?: boolean }
  crew?: { runId: string; crewId?: string; member?: string; task?: string; attempt?: number; brakeman?: string }
  resumedFrom?: string
  startedAt: string
  endedAt?: string
  lostAt?: string
  ending?: string
  entries: RecordEntry[]
  dropped?: number
  files?: Array<{ path: string; op: string; count: number; last: string }>
  tools?: Record<string, number>
  resumes?: ResumeReceipt[]
}

/** The record route's reply: the record whole, `live` while its session runs. */
export interface RecordReply {
  record: SessionRecord
  live: boolean
  newer: boolean
}

/** A resume from a record answered: the session it started (absent for a repeat once it is no longer listed), and the receipt. */
export interface ResumeReply {
  session?: { id: string; name?: string }
  resumed?: boolean
  notice?: string
  receipt: ResumeReceipt
}

/**
 * Where a resume that started leads: the new session's page while the server lists it (`session` in the reply), else the new session's
 * record. A command id sent again is answered from its receipt, which names the session it started, and that session is in the reply
 * only while the registry lists it (after a restart it is not): its page would answer 404, its record is what the server kept. Null
 * for a receipt that names no session.
 */
export function resumeDestination(res: Pick<ResumeReply, 'session' | 'receipt'>): { to: string; listed: boolean } | null {
  if (res.session?.id) return { to: `/sessions/${encodeURIComponent(res.session.id)}`, listed: true }
  if (res.receipt.session) return { to: `/history/sessions/${encodeURIComponent(res.receipt.session)}`, listed: false }
  return null
}

/** A command's receipt as `GET /api/history/command` reads it, by the actor its URL names. */
export interface HistoryCommand {
  id: string
  actor: string
  intent: string
  status: string
  receipt?: { status?: string; error?: string; refusal?: string; revision?: number; paused?: boolean; task?: { id: string; state: string } } | null
  createdAt: string
}

/** One field of a row's detail line, and the words its hover gives (the exact time of "2 h ago"). */
export interface DetailPart {
  field: 'source' | 'host' | 'project' | 'revision' | 'when' | 'availability'
  text: string
  title?: string
}

/**
 * A repository's name for the detail line: the last folder of its common directory without its `.git`, so sibling worktrees of one
 * repository (a crew attempt's `.conductor/worktrees/<run>/<task>.<n>`) are named as the repository; else of its worktree.
 */
export function repoName(p: HistoryItem['project']): string {
  const path = (p.repo || p.tree || '').replace(/\/+$/, '').replace(/\/\.git$/, '')
  return path.split('/').filter(Boolean).pop() ?? ''
}

/** The host in words: `lab-box`, or `nate@mac via conductor host` for a hosted one. */
export function hostWords(h: HistoryItem['host']): string {
  if (!h.name) return ''
  if (h.hosted) return `${h.user ? `${h.user}@` : ''}${h.name} via conductor host`
  return h.name
}

/** What an availability says in a row: the server's words, else its state's. */
export function availabilityWords(a?: Availability): string {
  if (!a) return ''
  if (a.words) return a.words
  const words: Record<string, string> = {
    live: 'live',
    listed: 'ended, listed',
    record: 'record only',
    lost: 'record only: the server stopped while it ran',
    host_away: 'host away',
    forgotten: 'forgotten',
    worktree: 'worktree kept',
    worktree_gone: 'worktree removed',
    log: 'log kept',
    log_dropped: 'log dropped: its hash and tail kept',
    pruned: 'pruned',
    deleted: 'deleted',
    kept: 'kept',
    unknown: 'not known now',
  }
  return words[a.state] ?? a.state
}

/**
 * A row's detail line, its six fields in a fixed order: source · host · project · revision · when · availability, each left out
 * when the entry has none. The project is its Dispatch key, else its repository's folder.
 */
export function historyDetailParts(e: HistoryItem, now = Date.now()): DetailPart[] {
  const parts: DetailPart[] = []
  const add = (field: DetailPart['field'], text: string, title?: string) => {
    if (text) parts.push(title ? { field, text, title } : { field, text })
  }
  add('source', e.source?.label ?? '')
  add('host', hostWords(e.host ?? {}))
  add('project', e.project?.key || repoName(e.project ?? {}))
  add('revision', e.revision?.label ?? '', e.revision?.commit || undefined)
  add('when', e.at && !Number.isNaN(Date.parse(e.at)) ? relativeTime(e.at, now, { suffix: true }) : '', e.at || undefined)
  add('availability', availabilityWords(e.availability))
  return parts
}

/** The detail line as one string. */
export function historyDetail(e: HistoryItem, now = Date.now()): string {
  return historyDetailParts(e, now)
    .map((p) => p.text)
    .join(' · ')
}

/** A row's badge, in the brand palette: Conversation, Decision, Checked ✓ or Checked ✗; a record has none. */
export interface ClassBadge {
  label: string
  color: 'neutral' | 'primary' | 'success' | 'error' | 'warning'
}

/**
 * The badge of an entry's class. Only a `checked` entry that passed gets a ✓; a failed one a ✗; a receipt that neither passed nor
 * failed (interrupted) says so without either. What was said or decided is never drawn as checked, whatever its outcome says.
 */
export function classBadge(e: Pick<HistoryItem, 'class' | 'outcome'>): ClassBadge | null {
  switch (e.class) {
    case 'said':
      return { label: 'Conversation', color: 'neutral' }
    case 'decided':
      return { label: 'Decision', color: 'primary' }
    case 'checked':
      if (e.outcome === 'passed') return { label: 'Checked ✓', color: 'success' }
      if (e.outcome === 'failed') return { label: 'Checked ✗', color: 'error' }
      return { label: e.outcome ? `Checked: ${e.outcome}` : 'Checked', color: 'warning' }
  }
  return null
}

/** "build #2" from a receipt's title "unit on build #2"; the label as it is otherwise. */
function attemptOf(label: string): string {
  const at = label.lastIndexOf(' on ')
  return at >= 0 ? label.slice(at + 4) : label
}

/**
 * The links under a row: who investigated a failure (the attempt it was handed), where it passed after, the attempt a review reopened,
 * the session one was resumed from. "Investigated by build #2 · passed on build #2". A link names no commit, so a pass is said by its
 * attempt.
 */
export function linkLine(e: Pick<HistoryItem, 'links'>): string {
  const out: string[] = []
  const of = (rel: string, dir: 'out' | 'in') => (e.links ?? []).filter((l) => l.rel === rel && l.dir === dir)
  const handed = of('handed', 'out')
  if (handed.length) out.push(`Investigated by ${handed.map((l) => l.label).join(', ')}`)
  const passed = of('passed_after', 'out')
  if (passed.length) out.push(`passed on ${passed.map((l) => attemptOf(l.label)).join(', ')}`)
  const given = of('handed', 'in')
  if (given.length) out.push(`Handed ${given.map((l) => l.label).join(', ')}`)
  const reopened = of('reopened', 'out')
  if (reopened.length) out.push(`Reopened as ${reopened.map((l) => l.label).join(', ')}`)
  const resumed = of('resumed_from', 'out')
  if (resumed.length) out.push(`Resumed from ${resumed.map((l) => l.label).join(', ')}`)
  return out.join(' · ')
}

/**
 * A row that stands for the same words in several places: "said in 3 places". Still what it is: a repeat is never promoted. The places
 * are the distinct anchors of the entries with the same digest and class, and a Conversation entry's anchor may be a session, a ticket's
 * comment, a run's message or a briefing's item: `repeats` counts them without saying which, so the words never call them sessions.
 */
export function repeatWords(e: Pick<HistoryItem, 'class' | 'repeats'>): string {
  const r = e.repeats
  if (!r) return ''
  const said = e.class === 'said'
  if (r.anchors > 1) return said ? `said in ${r.anchors} places` : `in ${r.anchors} places`
  if (r.count > 1) return said ? `said ${r.count} times` : `${r.count} times`
  return ''
}

/** One of the four questions history answers, as a ready query. */
export interface HistoryQuestion {
  id: 'failures' | 'rejected' | 'verified' | 'sessions'
  label: string
  query: string
}

/** A filter's value as one token of the query: a value with a space in it cannot be one, and is left out. */
function filter(name: string, value?: string): string {
  return value && !/\s/.test(value) ? ` ${name}:${value}` : ''
}

/** The four questions, each a query: what failed and who investigated it, rejected designs, what was last verified here, this machine's sessions. */
export function historyQuestions(o: { project?: string; host?: string } = {}): HistoryQuestion[] {
  return [
    { id: 'failures', label: 'Failures and who investigated them', query: 'class:checked outcome:failed' },
    { id: 'rejected', label: 'Rejected designs', query: 'kind:plan.withdrawn,plan.superseded,review.verdict outcome:withdrawn,superseded,needs_changes' },
    { id: 'verified', label: 'What was last verified here', query: `class:checked outcome:passed${filter('project', o.project)}` },
    { id: 'sessions', label: 'Sessions on this machine', query: `${filter('host', o.host).trimStart()}${filter('host', o.host) ? ' ' : ''}kind:session` },
  ]
}

// ---- a session's record, as its page shows it

/** A record whose end the server did not observe: it stopped observing it while it ran, so whether its process ended is not known. */
export function recordLost(r: Pick<SessionRecord, 'endedAt' | 'lostAt'>): boolean {
  return !r.endedAt && !!r.lostAt
}

/** How the record's session ended, in words: "exited (exit 0)", "stopped", "not observed since …", or "running". */
export function recordEnding(r: Pick<SessionRecord, 'endedAt' | 'lostAt' | 'ending'>, live = false): string {
  if (recordLost(r)) return `not observed since ${r.lostAt}; whether its process ended is not known`
  if (r.endedAt) return r.ending || 'ended'
  return live ? 'running' : r.ending || 'no end recorded'
}

/** The agent in words: "Claude Code 2.1.318". */
export function recordAgent(r: Pick<SessionRecord, 'agent'>): string {
  return [r.agent.name || r.agent.id, r.agent.version].filter(Boolean).join(' ')
}

/** One entry of a record in words: what it is (its label) and what it says (`text`, empty when the record keeps no words for it). */
export function recordEntryWords(e: RecordEntry): { label: string; text: string; icon: string } {
  switch (e.kind) {
    case 'typed':
      return { label: 'Typed by Conductor', text: e.message ?? '', icon: 'i-lucide-keyboard' }
    case 'question':
      return { label: 'Asked', text: e.message ?? '', icon: 'i-lucide-message-circle-question' }
    case 'answer':
      switch (e.outcome) {
        case 'answered':
          return { label: `Answered${e.byName ? ` by ${e.byName}` : ''}`, text: e.message ?? 'a line typed (its words are never kept)', icon: 'i-lucide-check' }
        case 'answered_in_terminal':
          return { label: 'Answered in the terminal', text: 'keys typed in the terminal (never kept)', icon: 'i-lucide-terminal' }
        default:
          return { label: 'Withdrawn', text: 'the question ended unanswered', icon: 'i-lucide-undo-2' }
      }
    case 'chat':
      if (e.event === 'sent_to_agent') return { label: 'Sent to the agent', text: e.message ?? '', icon: 'i-lucide-send' }
      return { label: e.byName ? `${e.byName} in the chat` : 'In the chat', text: e.message ?? '', icon: 'i-lucide-message-circle' }
    case 'report': {
      const label: Record<string, string> = { progress: 'Progress', artifact: 'Artifact', handoff: 'Handoff', error: 'Error', tool_denied: 'Tool refused' }
      const text = [e.message, e.tool, e.to ? `to ${e.to}` : '', e.url].filter(Boolean).join(' · ')
      return { label: `${label[e.event ?? ''] ?? 'Report'} (reported by its agent)`, text, icon: e.event === 'error' ? 'i-lucide-circle-alert' : 'i-lucide-flag' }
    }
    case 'status':
      return { label: 'Status', text: e.message ?? '', icon: 'i-lucide-activity' }
  }
  return { label: e.kind, text: e.message ?? '', icon: 'i-lucide-dot' }
}

/** A resume's receipt in words. */
export function resumeReceiptWords(r: ResumeReceipt): string {
  switch (r.state) {
    case 'started':
      return r.session ? `started session ${r.session}` : 'started'
    case 'failed':
      return `refused${r.code ? ` (${r.code})` : ''}${r.error ? `: ${r.error}` : ''}`
    case 'uncertain':
      return r.error || 'the server stopped while starting the session; its process may have started before its record was written'
    case 'pending':
      return 'being started'
  }
  return r.state
}

/**
 * What Resume asks before it sends: always a first yes; for a launch with variables, their names: a session still listed (`listed`, its
 * entry's `availability.state`) resumes with its own launch, variables among it, and a record alone without them, since no record keeps a
 * value (sent as `withoutEnv`); while the page does not know which, both are said. For a lost record, that its process may still be
 * running, asked a second time before `confirmLost`.
 */
export function resumeQuestion(r: Pick<SessionRecord, 'name' | 'agent' | 'launch' | 'endedAt' | 'lostAt' | 'where'>, listed?: boolean): { title: string; detail: string; env: string[]; withoutEnv: boolean; lost: boolean } {
  const env = r.launch?.envNames ?? []
  const n = r.launch?.envCount ?? 0
  const withoutEnv = listed !== true && (env.length > 0 || n > 0)
  const lost = recordLost(r)
  const who = recordAgent(r)
  const parts = [`A new session takes up ${who}'s own conversation${r.agent.session?.id ? ` (${r.agent.session.id})` : ''} in the record's folder.`]
  const vars = env.length ? `variables (${env.join(', ')})` : `${n} variables`
  if (env.length || n > 0) {
    if (listed) parts.push(`Its launch had ${vars}; its session is still listed, so it resumes with its own launch, its variables among it.`)
    else parts.push(`Its launch had ${vars}; no record keeps their values, so it resumes without them${listed === undefined ? ' (unless its session is still listed: then it resumes with its own launch, its variables among it)' : ''}.`)
  }
  return { title: `Resume ${r.name || who}?`, detail: parts.join(' '), env, withoutEnv, lost }
}

/** The second question for a lost record: its process may still be running, and a resume beside it would be a second agent on its conversation. */
export function lostQuestion(r: Pick<SessionRecord, 'lostAt' | 'where'>): { title: string; detail: string } {
  return {
    title: 'Its process may still be running',
    detail: `The server stopped while this session ran${r.where ? ` on ${r.where}` : ''}${r.lostAt ? ` (not observed since ${r.lostAt})` : ''}; whether its process ended is not known. Resume anyway only if you know it ended.`,
  }
}

// ---- the search, as the palette's `~` and the History page show it (PR-7)

/** The search's indexing state (`GET /api/history`'s `indexing`). */
export interface Indexing {
  state: 'opening' | 'catching_up' | 'checking' | 'ready' | 'read_only' | 'unavailable' | 'other_version' | string
  behind?: number
  ledger?: string
  oldest?: string
  dropped?: number
}

/** `GET /api/history`'s reply: a page of entries, the cursor after it, the total and the index's state. */
export interface HistoryReply {
  entries: HistoryItem[]
  next?: string
  total: number
  totalCapped?: boolean
  cut?: 'ranked' | 'scanned' | string
  truncated?: boolean
  indexing: Indexing
  readOnly?: boolean
}

/** `GET /api/history/similar`'s reply: the receipts of the same command elsewhere. */
export interface SimilarReply {
  entries: HistoryItem[]
  truncated?: boolean
}

/** The index's state in words, or '' when it is ready and nothing is behind: "Indexing: 3 sources behind". */
export function indexingWords(x?: Indexing | null): string {
  if (!x) return ''
  const behind = x.behind ?? 0
  const n = behind > 0 ? `: ${behind} ${behind === 1 ? 'source' : 'sources'} behind` : ''
  switch (x.state) {
    case 'ready':
      return behind > 0 ? `Indexing${n}` : ''
    case 'catching_up':
      return `Indexing${n || ': catching up'}`
    case 'opening':
      return 'Indexing: opening the history'
    case 'checking':
      return 'Indexing: checking the history after an unclean stop'
    case 'read_only':
      return `Read only: another server keeps this history${n}`
    case 'other_version':
      return 'Indexing: rebuilding the history for this version'
    case 'unavailable':
      return 'The history index is unavailable'
  }
  return `Indexing: ${x.state}${n}`
}

/** A path in the app the router can open: a page, never an API route (a raw log is never a row's destination). */
function appPage(u?: string): string | null {
  if (!u || !u.startsWith('/') || u.startsWith('//')) return null
  if (u === '/api' || u.startsWith('/api/') || u.startsWith('/api?')) return null
  return u
}

/** Where Enter on a row goes: the page that shows its evidence, else its anchor's page; null for neither. */
export function historyTarget(e: Pick<HistoryItem, 'url' | 'anchor'>): string | null {
  return appPage(e.url) ?? appPage(e.anchor?.url)
}

/** The session an entry belongs to: its anchor when that is a session, else a link to one; with the page it opens (its session's or its record's). */
export function historySession(e: Pick<HistoryItem, 'anchor' | 'links'>): { id: string; url: string | null; listed: boolean } | null {
  const ends = [{ ref: e.anchor?.ref ?? '', url: e.anchor?.url }, ...(e.links ?? []).map((l) => ({ ref: l.ref, url: l.url }))]
  const s = ends.find((x) => x.ref.startsWith('session:'))
  if (!s) return null
  const id = s.ref.slice('session:'.length)
  const url = appPage(s.url) ?? `/history/sessions/${encodeURIComponent(id)}`
  return { id, url, listed: url.startsWith('/sessions/') }
}

/** Whether an entry is a receipt of a command run (a check's or a verify step's), the entries "Find this elsewhere" asks about. */
export function isReceipt(e: Pick<HistoryItem, 'kind'>): boolean {
  return e.kind === 'check' || e.kind === 'verify'
}

/** One anchor's rows: the session or the task the entries belong to, in the order its first entry came. */
export interface AnchorGroup {
  anchor: HistoryItem['anchor']
  entries: HistoryItem[]
}

/** Entries grouped under their anchors, each group where its first entry ranked, the entries in their order within it; a key seen twice (two pages) kept once. */
export function byAnchor(entries: readonly HistoryItem[]): AnchorGroup[] {
  const out: AnchorGroup[] = []
  const at = new Map<string, AnchorGroup>()
  const seen = new Set<string>()
  for (const e of entries) {
    if (seen.has(e.key)) continue
    seen.add(e.key)
    const ref = e.anchor?.ref || e.key
    let g = at.get(ref)
    if (!g) {
      g = { anchor: e.anchor?.ref ? e.anchor : { ref, label: e.title }, entries: [] }
      at.set(ref, g)
      out.push(g)
    }
    g.entries.push(e)
  }
  return out
}

/** The filters a query takes (`GET /api/history`'s `q`), in the grammar's names. */
export const HISTORY_FILTERS = ['class', 'kind', 'outcome', 'project', 'repo', 'host', 'agent', 'crew', 'run', 'ticket', 'session', 'since', 'until'] as const
export type HistoryFilter = (typeof HISTORY_FILTERS)[number]

/** The query's words split as the grammar splits them: a quoted phrase is one word, quotes kept. */
function words(q: string): string[] {
  return q.match(/"[^"]*"?|\S+/g) ?? []
}

/** A filter's value in a query, '' when it has none (the last one written wins, as the chips write one). */
export function filterOf(q: string, name: HistoryFilter): string {
  let v = ''
  for (const w of words(q)) if (w.toLowerCase().startsWith(`${name}:`)) v = w.slice(name.length + 1)
  return v
}

/**
 * The query with one filter written: every `name:` word of it dropped, then `name:value` added at the end; none added for an empty
 * value (the chip cleared). The query's other words keep their order. A value with a space in it is no one token, and is not written.
 */
export function withFilter(q: string, name: HistoryFilter, value: string): string {
  const kept = words(q).filter((w) => !w.toLowerCase().startsWith(`${name}:`))
  if (value && !/\s/.test(value)) kept.push(`${name}:${value}`)
  return kept.join(' ')
}

/** A row's icon, by its class and kind: a session, a receipt passed or failed, a decision, words. */
export function historyIcon(e: Pick<HistoryItem, 'class' | 'kind' | 'outcome'>): string {
  if (e.kind === 'session') return 'i-lucide-terminal'
  switch (e.class) {
    case 'checked':
      return e.outcome === 'passed' ? 'i-lucide-circle-check' : e.outcome === 'failed' ? 'i-lucide-circle-x' : 'i-lucide-circle-dot'
    case 'decided':
      return 'i-lucide-gavel'
    case 'said':
      return 'i-lucide-message-circle'
  }
  return 'i-lucide-file-text'
}

/** The machine's own name, from the records of its sessions (a server session's host, never a hosted one's): what "Sessions on this machine" asks for. */
export function machineOf(entries: readonly Pick<HistoryItem, 'host'>[]): string {
  return entries.find((e) => e.host?.name && !e.host.hosted)?.host.name ?? ''
}
