import type { RunInfo, SessionInfo } from '~/composables/useSessions'
import type { EventMark, EventType, RouteRow } from './events'
import { joinedPath, type JoinedEntry } from './joined'
import type { AttentionKind, AttentionOption } from './protocol'
import { isEnded } from './attention'
import { questionOf } from './reply'
import { agentInitials, relativeTime } from './sessions'

/** The desktop sidebar's two modes: everything, or an icon rail. */
export type SidebarMode = 'full' | 'rail'
/** The one place the mode is kept: localStorage, per browser. Nuxt UI's own collapse cookie is off (the layout's UDashboardGroup). */
export const SIDEBAR_KEY = 'conductor.sidebar.mode'
/** The key before the rail existed: '1' meant hidden, which is now the rail. */
export const LEGACY_SIDEBAR_KEY = 'conductor.sidebar.hidden'

/** What localStorage offers; a fake in tests. */
export interface KeyValueStore {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
  removeItem(key: string): void
}

/**
 * The mode saved for this browser. An old hidden flag is moved to the new key
 * on the way; when the new key cannot be written, the old choice still holds
 * and its key stays for a later try. Anything unreadable is full.
 */
export function readSidebarMode(storage: KeyValueStore): SidebarMode {
  let legacy: string | null
  try {
    const mode = storage.getItem(SIDEBAR_KEY)
    if (mode === 'rail' || mode === 'full') return mode
    legacy = storage.getItem(LEGACY_SIDEBAR_KEY)
  } catch {
    return 'full'
  }
  if (legacy === null) return 'full'
  const migrated: SidebarMode = legacy === '1' ? 'rail' : 'full'
  try {
    storage.setItem(SIDEBAR_KEY, migrated)
    storage.removeItem(LEGACY_SIDEBAR_KEY)
  } catch {
    /* not saved: the old key stays */
  }
  return migrated
}

export function writeSidebarMode(storage: KeyValueStore, mode: SidebarMode): void {
  try {
    storage.setItem(SIDEBAR_KEY, mode)
  } catch {
    /* ignore */
  }
}

/**
 * The full sidebar's width, in rem (the unit of the layout's UDashboardGroup): it opens at its minimum, 12.5 rem (200 px), on
 * every screen, and a drag widens it up to 26 % of the window, never below the minimum (#437). Nuxt UI clamps only during a
 * drag: `sidebarBounds` gives the bounds of a window for the drag, and `SIDEBAR_WIDTH` keeps the drawn width within them as
 * the window changes.
 */
export const SIDEBAR_SIZE = { min: 12.5, maxShare: 0.26 } as const
/** The full sidebar's bounds in a window, in rem. */
export interface SidebarBounds {
  min: number
  max: number
}

/** The bounds in a window `windowWidth` px wide, with `remPx` px to the rem: the maximum is 26 % of the window, never below the minimum. */
export function sidebarBounds(windowWidth: number, remPx = 16): SidebarBounds {
  const max = Number.isFinite(windowWidth) && Number.isFinite(remPx) && remPx > 0 ? Math.floor(((windowWidth * SIDEBAR_SIZE.maxShare) / remPx) * 100) / 100 : 0
  return { min: SIDEBAR_SIZE.min, max: Math.max(SIDEBAR_SIZE.min, max) }
}

/** The full sidebar's drawn width: the width Nuxt UI holds (`--width`), within the bounds of the window as it is now. */
export const SIDEBAR_WIDTH = `clamp(${SIDEBAR_SIZE.min}rem, var(--width), max(${SIDEBAR_SIZE.min}rem, ${SIDEBAR_SIZE.maxShare * 100}vw))`

/** The width a drag left, in rem, per browser, beside the mode: with Nuxt UI's persistence off, nothing else keeps it over a reload. */
export const SIDEBAR_SIZE_KEY = 'conductor.sidebar.width'
/** The width before #437, in percent of the window: dropped once, so every browser opens at the minimum. */
export const LEGACY_SIDEBAR_SIZE_KEY = 'conductor.sidebar.size'

export function clampSidebarSize(size: number, bounds: SidebarBounds): number {
  return Math.min(bounds.max, Math.max(bounds.min, size))
}

/**
 * The width saved for this browser, within the bounds of the window it opens in; the minimum when there is none, it is not a
 * number, or storage throws. A width saved before #437 is removed on the way.
 */
export function readSidebarSize(storage: KeyValueStore, bounds: SidebarBounds): number {
  try {
    if (storage.getItem(LEGACY_SIDEBAR_SIZE_KEY) !== null) storage.removeItem(LEGACY_SIDEBAR_SIZE_KEY)
  } catch {
    /* not removed: tried again on the next read */
  }
  try {
    const size = Number(storage.getItem(SIDEBAR_SIZE_KEY) || Number.NaN)
    if (Number.isFinite(size)) return clampSidebarSize(size, bounds)
  } catch {
    /* no storage: the minimum */
  }
  return bounds.min
}

/** Saves a width within the bounds, to two decimals; a value that is not a number is not saved. */
export function writeSidebarSize(storage: KeyValueStore, size: number, bounds: SidebarBounds): void {
  if (!Number.isFinite(size)) return
  try {
    storage.setItem(SIDEBAR_SIZE_KEY, String(Math.round(clampSidebarSize(size, bounds) * 100) / 100))
  } catch {
    /* ignore */
  }
}

/** Whether `path` is the page of session `id`: its sidebar entry is marked as the current page. */
export function sessionOpen(path: string, id: string): boolean {
  return path === `/sessions/${id}`
}

/** Whether `path` is the run page of run `runId`. */
export function runOpen(path: string, runId: string): boolean {
  return path === `/runs/${runId}`
}

/** The amber dot on a session needing input follows the Events page's Badge route for needs_input; the group and its text stay. */
export function needsDotShown(routes: Readonly<Record<EventType, RouteRow>>): boolean {
  return routes.needs_input.badge
}

/** The machine tag of a hosted session whose machine gave no name. */
export const UNNAMED_HOST = 'unnamed host'

// ---------------------------------------------------------------------------
// The list's model (design 3a, 3b): sessions and runs, ordered by what needs
// you. A run is one block, kept whole, placed where its most urgent member
// is. A crew is not listed (it names the run). A machine is a tag on a row.

/** A row's state and colour: amber, green, grey, grey dashed. */
export type RowState = 'needs' | 'running' | 'idle' | 'exited'
/** The list's sections, in order; `shared` holds the links joined from here. */
export type SectionKey = 'needs' | 'running' | 'shared' | 'exited'
export const SECTION_KEYS: readonly SectionKey[] = ['needs', 'running', 'shared', 'exited']
/** The sections holding sessions (every one but shared). */
export type ListSection = 'needs' | 'running' | 'exited'

export interface SessionRow {
  kind: 'session'
  /** The session's id. */
  id: string
  session: SessionInfo
  state: RowState
  /** The machine of a hosted session (UNNAMED_HOST when it gave none); absent for this server's own. */
  machine?: string
  /** What the session asks, while it needs you; `question` its id, which an answer from the row names (absent from an older owner). */
  prompt?: { message: string; options: AttentionOption[]; kind?: AttentionKind; since?: string; question?: string }
  /** `exit 0`, `stopped`: an exited row's first word; the age is added with `now` where it shows. */
  exitWord?: string
}

export interface RunBlock {
  kind: 'run'
  /** `run:<runId>`. */
  id: string
  runId: string
  /** The run's own name, else its crew's; the crew's id when the server no longer keeps the run. */
  title: string
  startedAt?: string
  stoppedAt?: string
  /** The run's member count (pending ones included), else the sessions seen. */
  agents: number
  /** The most urgent member's. */
  state: RowState
  /** How many members need you. */
  needs: number
  /** The members with a session, the most urgent first, then the crew's order. */
  members: SessionRow[]
}

export type SidebarItem = SessionRow | RunBlock

export interface SidebarModel {
  needs: SidebarItem[]
  running: SidebarItem[]
  /** Loose exited sessions and wholly exited runs. */
  exited: SidebarItem[]
  /** For the headers: sessions needing you (anywhere); the Running section's sessions that are not exited; the Exited section's. */
  counts: Record<ListSection, number>
}

const RANK: Record<RowState, number> = { needs: 0, running: 1, idle: 2, exited: 3 }

/** A session's row state: exited once ended; needs while it waits on input; running while its process runs; idle otherwise (starting, a host away). */
export function rowState(s: SessionInfo): RowState {
  if (isEnded(s.status)) return 'exited'
  if (s.attention?.state === 'needs_input') return 'needs'
  return s.status === 'running' ? 'running' : 'idle'
}

export function sessionRow(s: SessionInfo): SessionRow {
  const state = rowState(s)
  const row: SessionRow = { kind: 'session', id: s.id, session: s, state }
  if (s.kind === 'hosted') row.machine = s.hostName || UNNAMED_HOST
  if (state === 'needs') row.prompt = { message: s.attention?.message || 'Waiting for input', options: s.attention?.options ?? [], kind: s.attention?.kind, since: s.attention?.since, question: questionOf(s.attention) }
  if (state === 'exited') row.exitWord = s.status === 'exited' && s.exitCode !== undefined ? `exit ${s.exitCode}` : s.status
  return row
}

/** The state of a run block: its most urgent member's (needs, then running, idle, exited). */
export function blockState(members: readonly SessionRow[]): RowState {
  let best: RowState = 'exited'
  for (const m of members) if (RANK[m.state] < RANK[best]) best = m.state
  return best
}

/** When a row last signalled, for the order within a section: the prompt's time, the end, else the start. */
function signalTime(row: SessionRow): string {
  const s = row.session
  if (row.state === 'needs') return s.attention?.since ?? s.createdAt
  if (row.state === 'exited') return s.endedAt ?? s.createdAt
  return s.createdAt
}

function sectionOf(state: RowState): ListSection {
  return state === 'needs' ? 'needs' : state === 'exited' ? 'exited' : 'running'
}

/**
 * The list: every run as one block where its most urgent member belongs, never split; loose sessions by their own state. In a
 * section the loose rows come first, then the blocks, each newest signal first. `runOf` is the live store's run (its name, start,
 * members and their order); a run it does not know is named by its crew's id.
 */
export function sidebarModel(sessions: readonly SessionInfo[], runOf: (runId: string) => RunInfo | undefined = () => undefined): SidebarModel {
  const loose: SessionRow[] = []
  const byRun = new Map<string, SessionRow[]>()
  for (const s of sessions) {
    const row = sessionRow(s)
    if (s.crew) {
      const list = byRun.get(s.crew.runId) ?? []
      list.push(row)
      byRun.set(s.crew.runId, list)
    } else loose.push(row)
  }
  const blocks: RunBlock[] = []
  for (const [runId, rows] of byRun) {
    const run = runOf(runId)
    const order = new Map((run?.members ?? []).map((m, i) => [m.name, i]))
    const place = (r: SessionRow) => order.get(r.session.crew?.member ?? '') ?? Number.MAX_SAFE_INTEGER
    const members = [...rows].sort((a, b) => RANK[a.state] - RANK[b.state] || place(a) - place(b) || b.session.createdAt.localeCompare(a.session.createdAt))
    blocks.push({
      kind: 'run',
      id: `run:${runId}`,
      runId,
      title: run ? run.label || run.name : (rows[0]?.session.crew?.crewId ?? runId),
      startedAt: run?.startedAt,
      stoppedAt: run?.stoppedAt,
      agents: run ? run.members.length : rows.length,
      state: blockState(members),
      needs: members.filter((m) => m.state === 'needs').length,
      members,
    })
  }
  const newestFirst = (a: string, b: string) => b.localeCompare(a)
  const out: SidebarModel = { needs: [], running: [], exited: [], counts: { needs: 0, running: 0, exited: 0 } }
  for (const key of ['needs', 'running', 'exited'] as const) {
    const rows = loose.filter((r) => sectionOf(r.state) === key).sort((a, b) => newestFirst(signalTime(a), signalTime(b)))
    const bs = blocks.filter((b) => sectionOf(b.state) === key).sort((a, b) => newestFirst(signalTime(a.members[0]!), signalTime(b.members[0]!)))
    out[key] = [...rows, ...bs]
  }
  out.counts.needs = loose.filter((r) => r.state === 'needs').length + blocks.reduce((n, b) => n + b.needs, 0)
  out.counts.running = out.running.reduce((n, it) => n + (it.kind === 'run' ? it.members.filter((m) => m.state !== 'exited').length : 1), 0)
  out.counts.exited = out.exited.reduce((n, it) => n + (it.kind === 'run' ? it.members.length : 1), 0)
  return out
}

/**
 * A row's meta line, in parts: the machine (a hosted session), the agent, "server" (a loose session of this server's own; a run's
 * members say nothing, they run here), and who is here or how long it has run. The path is not among them (design 3f): it stays
 * on the session page and in the row's tooltip.
 */
export function rowMeta(s: SessionInfo, member: boolean, now = Date.now()): string[] {
  const tail = s.viewers > 0 ? `${s.viewers} here` : relativeTime(s.createdAt, now)
  if (s.kind === 'hosted') return [s.hostName || UNNAMED_HOST, s.agentId, tail]
  return member ? [s.agentId, tail] : [s.agentId, 'server', tail]
}

/** A clock for the narrow sidebar: 24-hour, as the design writes it ("started 08:31"), so the line fits. */
function clock(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', hour12: false })
}

/** A run block's second line: "started 08:31 · 3 agents", "stopped 08:40 · 3 agents", or the count alone for a run the server forgot. */
export function blockSubtitle(block: Pick<RunBlock, 'startedAt' | 'stoppedAt' | 'agents'>): string {
  const agents = `${block.agents} ${block.agents === 1 ? 'agent' : 'agents'}`
  if (block.stoppedAt) return `stopped ${clock(block.stoppedAt)} · ${agents}`
  if (block.startedAt) return `started ${clock(block.startedAt)} · ${agents}`
  return agents
}

/** The states a folded section shows as squares: a run contributes its members'. At most six. */
export function sectionPreview(items: readonly SidebarItem[]): RowState[] {
  const out: RowState[] = []
  for (const it of items) {
    if (it.kind === 'run') out.push(...it.members.map((m) => m.state))
    else out.push(it.state)
    if (out.length >= 6) break
  }
  return out.slice(0, 6)
}

// ---------------------------------------------------------------------------
// Folds (design 3c): every section folds from its header, remembered per
// browser; Exited starts folded; Needs you opens again by itself when a new
// prompt arrives, so folding it never hides one.

export const SIDEBAR_FOLDS_KEY = 'conductor.sidebar.folds'
/** true = folded. */
export type Folds = Record<SectionKey, boolean>
export const DEFAULT_FOLDS: Readonly<Folds> = { needs: false, running: false, shared: false, exited: true }

/** The folds saved for this browser; anything unreadable or missing takes the default. */
export function readFolds(storage: KeyValueStore): Folds {
  const out: Folds = { ...DEFAULT_FOLDS }
  try {
    const raw = storage.getItem(SIDEBAR_FOLDS_KEY)
    if (!raw) return out
    const parsed = JSON.parse(raw) as unknown
    if (parsed && typeof parsed === 'object') {
      for (const k of SECTION_KEYS) {
        const v = (parsed as Record<string, unknown>)[k]
        if (typeof v === 'boolean') out[k] = v
      }
    }
  } catch {
    /* the defaults */
  }
  return out
}

export function writeFolds(storage: KeyValueStore, folds: Folds): void {
  try {
    storage.setItem(SIDEBAR_FOLDS_KEY, JSON.stringify(folds))
  } catch {
    /* ignore */
  }
}

/** The folds after new prompts arrived (`newlyNeedingInput`'s sessions): Needs you unfolds; the same object when nothing changes. */
export function reopenNeeds(folds: Folds, fresh: readonly SessionInfo[]): Folds {
  return fresh.length && folds.needs ? { ...folds, needs: false } : folds
}

// ---------------------------------------------------------------------------
// Keyboard focus (design 3c): one flat list of the rows a key can land on,
// in the list's order, a run's header among them; a folded section holds none.

export type FocusRow =
  | { id: string; kind: 'session'; sessionId: string; runId?: string; options: number }
  | { id: string; kind: 'run'; runId: string }
  | { id: string; kind: 'shared'; entryId: string }

function sessionFocus(row: SessionRow, runId?: string): FocusRow {
  return { id: `s:${row.id}`, kind: 'session', sessionId: row.id, runId, options: row.prompt?.options.length ?? 0 }
}

export function focusRows(model: SidebarModel, shared: readonly Pick<JoinedEntry, 'id'>[], folds: Folds): FocusRow[] {
  const out: FocusRow[] = []
  const add = (items: readonly SidebarItem[]) => {
    for (const it of items) {
      if (it.kind === 'run') {
        out.push({ id: `r:${it.runId}`, kind: 'run', runId: it.runId })
        for (const m of it.members) out.push(sessionFocus(m, it.runId))
      } else out.push(sessionFocus(it))
    }
  }
  if (!folds.needs) add(model.needs)
  if (!folds.running) add(model.running)
  if (!folds.shared) for (const e of shared) out.push({ id: `j:${e.id}`, kind: 'shared', entryId: e.id })
  if (!folds.exited) add(model.exited)
  return out
}

/** The id of the row `delta` steps from `current`, clamped to the ends; from nowhere, the first (down) or the last (up); null when there are none. */
export function moveFocus(rows: readonly FocusRow[], current: string | null, delta: 1 | -1): string | null {
  if (!rows.length) return null
  const i = current === null ? -1 : rows.findIndex((r) => r.id === current)
  if (i < 0) return (delta === 1 ? rows[0] : rows[rows.length - 1])!.id
  return rows[Math.min(rows.length - 1, Math.max(0, i + delta))]!.id
}

// ---------------------------------------------------------------------------
// The rail, drawn from the same model (design 3d): a square is a session, a
// capsule a run with its sessions inside, a corner tile a machine or a share;
// the counts sit on top. Every shape has a tooltip naming it in words.

export interface RailShape {
  id: string
  shape: 'square' | 'capsule'
  to: string
  /** The tooltip: the shape named in words. */
  label: string
  state: RowState
  /** Exited. */
  dashed: boolean
  /** A square's agent (its initials); a run link shared with you shows a crew instead. */
  agentId: string
  kind: 'session' | 'run' | 'shared'
  /** A session square's working directory (its folder's letters when squares would match, railGlyphs); none for a share. */
  cwd?: string
  /** Bottom left: where it comes from. */
  tile?: 'machine' | 'share'
  /** Bottom right: new events on it (the badges the Events page routes here). */
  news: number
  /** A capsule's sessions, the one needing you first. */
  members?: RailShape[]
  /** A capsule: the run's own unread chat, drawn beside the play icon (its members' is on their squares). */
  chat?: number
  /** A capsule: the play icon amber while one of its sessions needs you. */
  amber?: boolean
  /**
   * A square's two letters (#314): the agent's initials, unless another square shares them, then its folder's letters
   * (railGlyphs); absent for a capsule.
   */
  glyph?: string
  /** A square's ordinal, top left, when its letters would still match another square's (railGlyphs). */
  ordinal?: number
}

export interface RailModel {
  /** On top: how many need you, then how many have new events. */
  needs: number
  news: number
  /** In the sidebar's order: needs you, running, shared, exited. */
  items: RailShape[]
  /** Loose exited sessions fold into +N; an exited run stays a capsule, dashed. */
  exitedFolded: number
}

const STATE_WORDS: Record<RowState, string> = { needs: 'needs you', running: 'running', idle: 'idle', exited: 'exited' }

/** "3 unread in chat". */
function unreadWords(n: number): string {
  return `${n} unread in chat`
}

function railSquare(row: SessionRow, marks: Readonly<Record<string, EventMark>>, unread: Readonly<Record<string, number>>): RailShape {
  const s = row.session
  const parts = [s.name, STATE_WORDS[row.state]]
  if (row.state === 'needs' && s.attention?.message) parts.push(s.attention.message)
  if (row.machine) parts.push(`on ${row.machine}`)
  const mark = marks[s.id]
  if (mark) parts.push(mark.label)
  const chat = unread[`session:${s.id}`] ?? 0
  if (chat) parts.push(unreadWords(chat))
  return { id: s.id, shape: 'square', to: `/sessions/${s.id}`, label: parts.join(' · '), state: row.state, dashed: row.state === 'exited', agentId: s.agentId, kind: 'session', tile: row.machine ? 'machine' : undefined, news: (mark ? 1 : 0) + chat, cwd: s.cwd }
}

/** "users api · run started 08:31 · review needs you · core running · lead exited", then "3 unread in chat" when the run's chat has them. */
export function railRunLabel(block: RunBlock, unread = 0): string {
  const sub = blockSubtitle(block).split(' · ')[0]!
  const when = /^(started|stopped) /.test(sub) ? `run ${sub}` : sub
  const parts = [block.title, when, ...block.members.map((m) => `${m.session.name} ${STATE_WORDS[m.state]}`)]
  if (unread) parts.push(unreadWords(unread))
  return parts.join(' · ')
}

function railCapsule(block: RunBlock, marks: Readonly<Record<string, EventMark>>, unread: Readonly<Record<string, number>>): RailShape {
  const members = block.members.map((m) => railSquare(m, marks, unread))
  const chat = unread[`run:${block.runId}`] ?? 0
  return {
    id: `run:${block.runId}`,
    shape: 'capsule',
    to: `/runs/${encodeURIComponent(block.runId)}`,
    label: railRunLabel(block, chat),
    state: block.state,
    dashed: block.state === 'exited',
    agentId: '',
    kind: 'run',
    news: members.reduce((n, m) => n + m.news, 0) + chat,
    members,
    chat,
    amber: block.state === 'needs',
  }
}

/**
 * The rail from the list's model, the links shared with you, the Events
 * page's badges (`useEvents().marks`) and the unread chat (`useChatUnread().counts`,
 * by thread): a square's bottom-right number is its badge plus its unread
 * chat, a capsule's its members' plus the run's own.
 */
export function railModel(model: SidebarModel, shared: readonly JoinedEntry[], marks: Readonly<Record<string, EventMark>>, unread: Readonly<Record<string, number>> = {}): RailModel {
  const items: RailShape[] = []
  let news = 0
  const count = (sq: RailShape) => {
    if (sq.news) news++
  }
  const add = (it: SidebarItem) => {
    if (it.kind === 'run') {
      const c = railCapsule(it, marks, unread)
      c.members!.forEach(count)
      if (unread[`run:${it.runId}`]) news++
      items.push(c)
    } else {
      const sq = railSquare(it, marks, unread)
      count(sq)
      items.push(sq)
    }
  }
  for (const it of model.needs) add(it)
  for (const it of model.running) add(it)
  for (const e of shared) {
    const gone = e.lastStatus === 'revoked' || e.lastStatus === 'gone'
    items.push({
      id: `j:${e.id}`,
      shape: 'square',
      to: joinedPath(e),
      label: `${e.name} · ${e.role === 'control' ? 'control' : 'view only'} · through ${e.host}`,
      state: gone ? 'exited' : 'idle',
      dashed: gone,
      agentId: e.agentId ?? '',
      kind: e.kind === 'run' ? 'run' : 'shared',
      tile: 'share',
      news: 0,
    })
  }
  let exitedFolded = 0
  for (const it of model.exited) {
    if (it.kind === 'run') add(it)
    else {
      if (marks[it.id] || unread[`session:${it.id}`]) news++
      exitedFolded++
    }
  }
  railGlyphs(items)
  return { needs: model.counts.needs, news, items, exitedFolded }
}

/** The first two letters of the last element of a path (`conductor` → `CO`), `?` for none. */
export function folderLetters(cwd: string): string {
  const name = cwd.replace(/[\\/]+$/, '').split(/[\\/]/).pop() ?? ''
  return name ? name.slice(0, 2).toUpperCase() : '?'
}

/**
 * Two squares never look the same (#314). Every square of the rail (a
 * capsule's members included, exited ones too), in the rail's order: its
 * letters are its agent's initials, unless another square shares them, when
 * each of those shows its folder's letters instead (`folderLetters` of the
 * session's working directory; a link shared with you, which names no
 * folder, keeps the agent's); then, across every square whatever its agent
 * (Claude Code and Codex both in `conductor` read `CO`), squares whose
 * letters still match get an ordinal, 1 up, in that order. The tooltip is
 * not the answer: this is what reads at a glance.
 */
export function railGlyphs(items: RailShape[]): void {
  const squares: RailShape[] = []
  for (const it of items) {
    if (it.shape === 'square' && it.kind !== 'run') squares.push(it)
    for (const m of it.members ?? []) squares.push(m)
  }
  const byInitials = new Map<string, RailShape[]>()
  for (const sq of squares) {
    const key = agentInitials(sq.agentId)
    byInitials.set(key, [...(byInitials.get(key) ?? []), sq])
  }
  for (const [key, group] of byInitials) {
    for (const sq of group) sq.glyph = group.length > 1 && sq.cwd ? folderLetters(sq.cwd) : key
  }
  const byGlyph = new Map<string, RailShape[]>()
  for (const sq of squares) byGlyph.set(sq.glyph!, [...(byGlyph.get(sq.glyph!) ?? []), sq])
  for (const same of byGlyph.values()) {
    if (same.length > 1) same.forEach((sq, i) => (sq.ordinal = i + 1))
  }
}
