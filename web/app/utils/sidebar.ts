import type { RunInfo, SessionInfo } from '~/composables/useSessions'
import type { EventMark, EventType, RouteRow } from './events'
import { joinedPath, type JoinedEntry } from './joined'
import type { AttentionKind, AttentionOption } from './protocol'
import { questionOf } from './reply'
import { askedWhere } from './decisions'
import type { NeedsItem } from './needsYou'
import { agentInitials, relativeTime } from './sessions'
import { clockTime } from './crewWords'
import { blockStatus, joinedStatus, sessionStatus, statusOfKey, statusTally, type StatusKey, type StatusView } from './status'

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
 * The full sidebar's width, in rem (the unit of the layout's UDashboardGroup). It opens at `open`, 17.5 rem (280 px, the width
 * `NARROW_SIDEBAR_PX` names), on every screen: there a row is whole, its name, its question and its choices in the row, and so
 * are the filter's words, a run's line with one agent and the line that counts a run's other members (#1022). A drag narrows it
 * to `min`, 16 rem (256 px), the narrowest at which every row still names itself whole (a member's "clarifier · naming #1", a
 * decision's question, a one-agent run's "started 19:21 · 1 agent · 1 need you", the filter's words), its question on two lines
 * with the choices in the panel beside it; at 200 px, the minimum before #1022, those were cut. A drag widens it up to 26 % of
 * the window, never less than the minimum plus `range` rem (336 px), so the handle widens it in a window 1024 px wide too
 * (#437). Nuxt UI clamps only during a drag: `sidebarBounds` gives the bounds of a window for the drag, and `SIDEBAR_WIDTH` keeps
 * the drawn width within them as the window changes.
 */
export const SIDEBAR_SIZE = { min: 16, open: 17.5, maxShare: 0.26, range: 5 } as const
/** The full sidebar's bounds in a window, in rem. */
export interface SidebarBounds {
  min: number
  max: number
}

/**
 * The bounds in a window `windowWidth` px wide, with `remPx` px to the rem: the maximum is 26 % of the window, never below the
 * minimum plus `SIDEBAR_SIZE.range`.
 */
export function sidebarBounds(windowWidth: number, remPx = 16): SidebarBounds {
  const max = Number.isFinite(windowWidth) && Number.isFinite(remPx) && remPx > 0 ? Math.floor(((windowWidth * SIDEBAR_SIZE.maxShare) / remPx) * 100) / 100 : 0
  return { min: SIDEBAR_SIZE.min, max: Math.max(SIDEBAR_SIZE.min + SIDEBAR_SIZE.range, max) }
}

/** The full sidebar's drawn width: the width Nuxt UI holds (`--width`), within the bounds of the window as it is now. */
export const SIDEBAR_WIDTH = `clamp(${SIDEBAR_SIZE.min}rem, var(--width), max(${SIDEBAR_SIZE.min + SIDEBAR_SIZE.range}rem, ${SIDEBAR_SIZE.maxShare * 100}vw))`

/**
 * Below this drawn width (px) a row's question is two lines and whole in the panel beside the row (owner decision 7): at 200 px a
 * question at the server's bound is two dozen lines. At 280 px and wider, and on the phone's list, it stays whole in the row.
 */
export const NARROW_SIDEBAR_PX = 280

/** Whether the list drawn `px` wide is narrow: one answer decides both the clamp and the panel. Never for the phone's list (`page`). */
export function narrowSidebar(px: number, page = false): boolean {
  return !page && Number.isFinite(px) && px < NARROW_SIDEBAR_PX
}

/**
 * The row the question's panel is for (`s:<id>`), or null: in a narrow sidebar, the row whose Answer was pressed, else the focused
 * row, while it asks (it has a prompt) and does not show its Stop question (`confirming`); none in a wide one.
 */
export function panelRow(focused: string | null, pressed: string | null, rows: ReadonlyMap<string, SessionRow | RunBlock>, narrow: boolean, confirming: string | null = null): string | null {
  if (!narrow) return null
  const id = pressed ?? focused
  if (!id || id === confirming) return null
  const row = rows.get(id)
  return row?.kind === 'session' && row.prompt ? id : null
}

/** The width a drag left, in rem, per browser, beside the mode: with Nuxt UI's persistence off, nothing else keeps it over a reload. */
export const SIDEBAR_SIZE_KEY = 'conductor.sidebar.width'
/** The width before #437, in percent of the window: read once as rem of the window it opens in, then removed. */
export const LEGACY_SIDEBAR_SIZE_KEY = 'conductor.sidebar.size'

export function clampSidebarSize(size: number, bounds: SidebarBounds): number {
  return Math.min(bounds.max, Math.max(bounds.min, size))
}

/** The width the sidebar opens at in a window with `bounds`: `SIDEBAR_SIZE.open`, within them. */
export function openSidebarSize(bounds: SidebarBounds): number {
  return clampSidebarSize(SIDEBAR_SIZE.open, bounds)
}

/**
 * The width saved for this browser, within the bounds of the window it opens in. The opening width when there is none, it is not
 * a number, storage throws, or it is narrower than the minimum (a width an earlier version kept at its 200 px minimum, by a drag
 * or a double-click on the handle: unusably narrow now, #1022); it stays saved until the next drag. A width saved before #437,
 * in percent of the window, is read as that share of this window (`windowRem`, the window's width in rem) where no width is
 * saved in rem, kept under the new key when it is wide enough, and removed.
 */
export function readSidebarSize(storage: KeyValueStore, bounds: SidebarBounds, windowRem = Number.NaN): number {
  const open = openSidebarSize(bounds)
  let saved = Number.NaN
  try {
    saved = Number(storage.getItem(SIDEBAR_SIZE_KEY) || Number.NaN)
  } catch {
    return open
  }
  try {
    const legacy = storage.getItem(LEGACY_SIDEBAR_SIZE_KEY)
    if (legacy !== null) {
      storage.removeItem(LEGACY_SIDEBAR_SIZE_KEY)
      const share = Number(legacy || Number.NaN)
      if (!Number.isFinite(saved) && Number.isFinite(share) && share > 0 && share <= 100 && Number.isFinite(windowRem) && windowRem > 0) {
        saved = (share / 100) * windowRem
        if (saved >= bounds.min) writeSidebarSize(storage, saved, bounds)
      }
    }
  } catch {
    /* not removed: tried again on the next read */
  }
  return Number.isFinite(saved) && saved >= bounds.min ? clampSidebarSize(saved, bounds) : open
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

/** The amber dot on a session needing input follows the Alerts rules' Badge route for needs_input; the group and its text stay. */
export function needsDotShown(routes: Readonly<Record<EventType, RouteRow>>): boolean {
  return routes.needs_input.badge
}

/** The machine tag of a hosted session whose machine gave no name. */
export const UNNAMED_HOST = 'unnamed host'

// ---------------------------------------------------------------------------
// The list's model (design 3a, 3b): sessions and runs, ordered by what needs
// you. A run is one block, kept whole, placed where its most urgent member
// is. A crew is not listed (it names the run). A machine is a tag on a row.

/** A row's state: its status key (utils/status.ts, sessionStatus): needs, working, finished, running (no signal, or starting) or exited. */
export type RowState = StatusKey
/**
 * The list's sections, in the order of how hard a thing waits on you (the UX review's P4): Needs you, Finished (an agent that ended
 * its turn waits for your next word), Working (an agent in a turn, and one that sends no signal), Shared with you (the links joined from
 * here), Exited.
 */
export type SectionKey = 'pinned' | 'needs' | 'finished' | 'working' | 'shared' | 'exited'
/** Pinned first: the sessions a person pinned to the top (utils/pins.ts), each also in its own section. */
export const SECTION_KEYS: readonly SectionKey[] = ['pinned', 'needs', 'finished', 'working', 'shared', 'exited']
/** Each section's title, as its header, the palette's groups and its fold actions say it. */
export const SECTION_TITLES: Readonly<Record<SectionKey, string>> = { pinned: 'Pinned', needs: 'Needs you', finished: 'Finished', working: 'Working', shared: 'Shared with you', exited: 'Exited' }
/** The sections holding sessions (every one but shared), in order. */
export type ListSection = 'needs' | 'finished' | 'working' | 'exited'
export const LIST_SECTIONS: readonly ListSection[] = ['needs', 'finished', 'working', 'exited']

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
  /** The most urgent member's; `needs` while one of its items waits on you. */
  state: RowState
  /** How many members need you, and how many of its items wait on you. */
  needs: number
  /** The members with a session, the most urgent first, then the crew's order. */
  members: SessionRow[]
  /** What of the run waits on you that is no member's question: its decisions and its tasks out of their attempts, oldest first. */
  items: ItemRow[]
}

/**
 * A row of Needs you for what waits on a person that is no session's question (the UX review's P3): a plan waiting for approval, a
 * task out of its attempts, a run's decision. A decision is `d:<id>` (its card opens from the row), a plan or a task `i:<ref>`.
 */
export interface ItemRow {
  kind: 'item'
  id: string
  item: NeedsItem
}

export type SidebarItem = SessionRow | RunBlock | ItemRow

export interface SidebarModel {
  /** Loose sessions that ask, the loose items (a plan, a decision or a task whose run the store does not hold), and the runs that wait. */
  needs: SidebarItem[]
  /** Sessions that ended their turn, and runs whose busiest member did. */
  finished: SidebarItem[]
  /** Sessions in a turn, sending no signal or starting, and runs whose busiest member is one. */
  working: SidebarItem[]
  /** Loose exited sessions (a host away that does not ask among them) and wholly exited runs. */
  exited: SidebarItem[]
  /** For the headers: what needs you that the list holds (sessions anywhere, items); each other section's sessions that are not exited (Exited's all). */
  counts: Record<ListSection, number>
}

/** An item's row id: `d:<id>` for a decision, `i:<ref>` for a plan or a task. */
export function itemRowId(item: Pick<NeedsItem, 'kind' | 'ref' | 'decision'>): string {
  return item.kind === 'decision' && item.decision ? `d:${item.decision.id}` : `i:${item.ref}`
}

/** The items a filter leaves: those whose words, ref or decision id hold the query (case folded); the questions, whose sessions are filtered as sessions. */
export function filterNeeds(items: readonly NeedsItem[], query: string): readonly NeedsItem[] {
  const q = query.trim().toLowerCase()
  if (!q) return items
  return items.filter((it) => {
    if (it.kind === 'question') return true
    const w = itemWords(it)
    return [w.title, w.detail, it.ref].some((v) => v.toLowerCase().includes(q))
  })
}

/**
 * An item's two lines: its title, and its question or brief. A decision is its question, then its id and where it was asked (or that
 * its run no longer runs: it is answered from its card all the same).
 */
export function itemWords(item: NeedsItem): { title: string; detail: string } {
  const d = item.decision
  if (item.kind === 'decision' && d) return { title: d.request.question, detail: `${d.id} · ${item.stopped ? 'its run has stopped' : `asked by ${askedWhere(d)}`}` }
  const s = item.summary
  return { title: s?.title || item.ref, detail: s?.question || s?.brief || '' }
}

/** The members' order in a block: the busiest first (blockStatus's order). */
const RANK: Record<RowState, number> = { needs: 0, working: 1, running: 2, finished: 3, exited: 4 }

/**
 * A session's row state: its status key (sessionStatus). A host away that asks is needs; one that does not ask takes the exited mark
 * ("host away") and sits in Exited; starting is running.
 */
export function rowState(s: SessionInfo): RowState {
  return sessionStatus(s).key
}

export function sessionRow(s: SessionInfo): SessionRow {
  const state = rowState(s)
  const row: SessionRow = { kind: 'session', id: s.id, session: s, state }
  if (s.kind === 'hosted') row.machine = s.hostName || UNNAMED_HOST
  if (state === 'needs') row.prompt = { message: s.attention?.message || 'Waiting for input', options: s.attention?.options ?? [], kind: s.attention?.kind, since: s.attention?.since, question: questionOf(s.attention) }
  if (state === 'exited') row.exitWord = s.status === 'host_disconnected' ? 'host away' : s.status === 'exited' && s.exitCode !== undefined ? `exit ${s.exitCode}` : s.status
  return row
}

/**
 * An exited row's line: its first word, then how long ago the session ended ("exit 1 · 3m ago"). A host away says "host away"
 * alone: the listing gives no moment its host went (the server sets no end for a host that may come back), and the session's start
 * is not that moment.
 */
export function exitLine(row: Pick<SessionRow, 'exitWord' | 'session'>, now: number): string {
  const s = row.session
  if (s.status === 'host_disconnected') return row.exitWord ?? 'host away'
  return `${row.exitWord} · ${relativeTime(s.endedAt ?? s.createdAt, now, { suffix: true })}`
}

/** The state of a run block: its busiest member's (blockStatus: needs, working, running, finished, exited). */
export function blockState(members: readonly SessionRow[]): RowState {
  return blockStatus(members.map((m) => m.state))
}

/** When a row last signalled, for the order within a section: the prompt's time, the end, else the start; an item's, since when it waits. */
function signalTime(row: SessionRow | ItemRow): string {
  if (row.kind === 'item') return row.item.since
  const s = row.session
  // A finished row: when its turn ended, so the latest finished comes first.
  if (row.state === 'needs' || row.state === 'finished') return s.attention?.since ?? s.createdAt
  if (row.state === 'exited') return s.endedAt ?? s.createdAt
  return s.createdAt
}

/** A block's signal: its first member's, or the newest of its items' when they wait on you. */
function blockTime(b: RunBlock): string {
  const times = [...(b.members[0] ? [signalTime(b.members[0])] : []), ...b.items.map((it) => it.item.since)]
  return times.reduce((a, t) => (t > a ? t : a), '')
}

/** A state's section: Working holds the working, the running (no signal) and the starting. */
export function sectionOf(state: RowState): ListSection {
  return state === 'needs' || state === 'finished' || state === 'exited' ? state : 'working'
}

/**
 * The list: every run as one block where its most urgent member belongs, never split; loose sessions by their own state. In a
 * section the loose rows come first, then the blocks, each newest signal first. `runOf` is the live store's run (its name, start,
 * members and their order); a run it does not know is named by its crew's id.
 *
 * `needs` is the one list of what needs you (useNeedsYou's items): its questions are the sessions' own rows; every other item gets a
 * row of its own (the UX review's P3). An item of a run the store holds or a session names (a decision, a task) sits under that run's
 * header, before its members, and puts the run in Needs you in the same call, whatever its members do: a run whose task failed while
 * its members work, a stopped run with an open decision. A run that waits with no session listed (a gate held before any attempt,
 * sessions aged out of the listing) gets a block of its own, its header and its items. A plan, and an item whose run the store does
 * not hold, is a loose row of Needs you.
 */
export function sidebarModel(sessions: readonly SessionInfo[], runOf: (runId: string) => RunInfo | undefined = () => undefined, needs: readonly NeedsItem[] = []): SidebarModel {
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
  const looseItems: ItemRow[] = []
  const itemsByRun = new Map<string, ItemRow[]>()
  for (const item of needs) {
    if (item.kind === 'question') continue
    const row: ItemRow = { kind: 'item', id: itemRowId(item), item }
    const run = item.kind === 'plan' ? undefined : item.run
    if (run && (byRun.has(run) || runOf(run))) {
      itemsByRun.set(run, [...(itemsByRun.get(run) ?? []), row])
      if (!byRun.has(run)) byRun.set(run, [])
    } else looseItems.push(row)
  }
  const blocks: RunBlock[] = []
  for (const [runId, rows] of byRun) {
    const run = runOf(runId)
    const order = new Map((run?.members ?? []).map((m, i) => [m.name, i]))
    const place = (r: SessionRow) => order.get(r.session.crew?.member ?? '') ?? Number.MAX_SAFE_INTEGER
    const members = [...rows].sort((a, b) => RANK[a.state] - RANK[b.state] || place(a) - place(b) || b.session.createdAt.localeCompare(a.session.createdAt))
    const items = itemsByRun.get(runId) ?? []
    blocks.push({
      kind: 'run',
      id: `run:${runId}`,
      runId,
      title: run ? run.label || run.name : (rows[0]?.session.crew?.crewId ?? runId),
      startedAt: run?.startedAt,
      stoppedAt: run?.stoppedAt,
      agents: run ? run.members.length : rows.length,
      state: items.length ? 'needs' : blockState(members),
      needs: members.filter((m) => m.state === 'needs').length + items.length,
      members,
      items,
    })
  }
  const newestFirst = (a: string, b: string) => b.localeCompare(a)
  const out: SidebarModel = { needs: [], finished: [], working: [], exited: [], counts: { needs: 0, finished: 0, working: 0, exited: 0 } }
  for (const key of LIST_SECTIONS) {
    const rows: Array<SessionRow | ItemRow> = [...loose.filter((r) => sectionOf(r.state) === key), ...(key === 'needs' ? looseItems : [])].sort((a, b) => newestFirst(signalTime(a), signalTime(b)))
    const bs = blocks.filter((b) => sectionOf(b.state) === key).sort((a, b) => newestFirst(blockTime(a), blockTime(b)))
    out[key] = [...rows, ...bs]
  }
  out.counts.needs = loose.filter((r) => r.state === 'needs').length + looseItems.length + blocks.reduce((n, b) => n + b.needs, 0)
  for (const key of ['finished', 'working'] as const) out.counts[key] = out[key].reduce((n, it) => n + (it.kind === 'run' ? it.members.filter((m) => m.state !== 'exited').length : 1), 0)
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

/** A clock for the narrow sidebar: the Crews pages' one clock (clockTime: 24-hour, "started 08:31", never "24:05"), so the line fits. */
function clock(iso: string): string {
  return clockTime(iso)
}

/**
 * A run block's second line: "started 08:31 · 4 agents · 2 working · 2 finished" (its members' states in the status table's words,
 * statusTally), "stopped 08:40 · 3 agents", or the count alone for a run the server forgot.
 */
export function blockSubtitle(block: Pick<RunBlock, 'startedAt' | 'stoppedAt' | 'agents'> & Partial<Pick<RunBlock, 'members'>>): string {
  const agents = `${block.agents} ${block.agents === 1 ? 'agent' : 'agents'}`
  const tally = statusTally((block.members ?? []).map((m) => m.state))
  const head = block.stoppedAt ? `stopped ${clock(block.stoppedAt)} · ${agents}` : block.startedAt ? `started ${clock(block.startedAt)} · ${agents}` : agents
  return tally ? `${head} · ${tally}` : head
}

/** What a run block draws under its header: its member rows, and the line that stands for the members it folds away. */
export interface BlockRows {
  members: SessionRow[]
  /** "and 2 more · 1 working · 1 finished": the members not shown, counted and told by their status's words (statusTally). */
  more?: { count: number; words: string }
}

/**
 * The rows of a run block in `section`: in Needs you, the members that ask and one line for the rest ("and 2 more · 1 working ·
 * 1 finished"), so that a crew of six with one question takes two rows; elsewhere, or once `unfolded` (the line pressed, held for
 * the visit), every member. A run whose members all ask, or none while no item of it waits, shows them all.
 */
export function blockRows(block: Pick<RunBlock, 'members'> & Partial<Pick<RunBlock, 'items'>>, section: ListSection, unfolded = false): BlockRows {
  if (section !== 'needs' || unfolded) return { members: block.members }
  const asking = block.members.filter((m) => m.state === 'needs')
  const rest = block.members.filter((m) => m.state !== 'needs')
  // A run that waits on an item folds its members that do not ask, so the item under its header is what the block shows.
  if (!rest.length || (!asking.length && !block.items?.length)) return { members: block.members }
  const tally = statusTally(rest.map((m) => sessionStatus(m.session).key))
  return { members: asking, more: { count: rest.length, words: [`and ${rest.length} more`, tally].filter(Boolean).join(' · ') } }
}

/** The status keys a folded section shows as marks (utils/status.ts, sessionStatus): a run contributes its items' and its members'; an item is `needs`. At most six. */
export function sectionPreview(items: readonly SidebarItem[]): StatusKey[] {
  const out: StatusKey[] = []
  for (const it of items) {
    if (it.kind === 'run') out.push(...it.items.map((): StatusKey => 'needs'), ...it.members.map((m) => sessionStatus(m.session).key))
    else if (it.kind === 'item') out.push('needs')
    else out.push(sessionStatus(it.session).key)
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
export const DEFAULT_FOLDS: Readonly<Folds> = { pinned: false, needs: false, finished: false, working: false, shared: false, exited: true }

/** The folds saved for this browser; anything unreadable or missing takes the default. A fold saved for Running (before Finished and Working) folds Working. */
export function readFolds(storage: KeyValueStore): Folds {
  const out: Folds = { ...DEFAULT_FOLDS }
  try {
    const raw = storage.getItem(SIDEBAR_FOLDS_KEY)
    if (!raw) return out
    const parsed = JSON.parse(raw) as unknown
    if (parsed && typeof parsed === 'object') {
      const saved = parsed as Record<string, unknown>
      if (typeof saved.running === 'boolean') out.working = saved.running
      for (const k of SECTION_KEYS) {
        const v = saved[k]
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
  | { id: string; kind: 'decision'; decision: string }
  | { id: string; kind: 'item'; ref: string }
  | { id: string; kind: 'more'; runId: string }

function sessionFocus(row: SessionRow, runId?: string): FocusRow {
  return { id: `s:${row.id}`, kind: 'session', sessionId: row.id, runId, options: row.prompt?.options.length ?? 0 }
}

function itemFocus(row: ItemRow): FocusRow {
  return row.item.kind === 'decision' && row.item.decision ? { id: row.id, kind: 'decision', decision: row.item.decision.id } : { id: row.id, kind: 'item', ref: row.item.ref }
}

/**
 * The rows a key can land on, in the list's order. Needs you's items are rows (`d:dr-7` a decision, which Enter opens the card of;
 * `i:<ref>` a plan or a task, which Enter opens on the page), a run's after its header and before its members. A run's members are
 * those `blockRows` shows, then its "and 2 more" line (`m:<runId>`), which Enter unfolds; `unfolded` holds the runs whose line was
 * pressed.
 */
export function focusRows(model: SidebarModel, shared: readonly Pick<JoinedEntry, 'id'>[], folds: Folds, unfolded: ReadonlySet<string> = new Set()): FocusRow[] {
  const out: FocusRow[] = []
  const add = (items: readonly SidebarItem[], section: ListSection) => {
    for (const it of items) {
      if (it.kind === 'run') {
        out.push({ id: `r:${it.runId}`, kind: 'run', runId: it.runId })
        for (const item of it.items) out.push(itemFocus(item))
        const shown = blockRows(it, section, unfolded.has(it.runId))
        for (const m of shown.members) out.push(sessionFocus(m, it.runId))
        if (shown.more) out.push({ id: `m:${it.runId}`, kind: 'more', runId: it.runId })
      } else if (it.kind === 'item') out.push(itemFocus(it))
      else out.push(sessionFocus(it))
    }
  }
  if (!folds.needs) add(model.needs, 'needs')
  if (!folds.finished) add(model.finished, 'finished')
  if (!folds.working) add(model.working, 'working')
  if (!folds.shared) for (const e of shared) out.push({ id: `j:${e.id}`, kind: 'shared', entryId: e.id })
  if (!folds.exited) add(model.exited, 'exited')
  return out
}

/** The id of the row `delta` steps from `current`, clamped to the ends; from nowhere, the first (down) or the last (up); null when there are none. */
export function moveFocus(rows: readonly FocusRow[], current: string | null, delta: 1 | -1): string | null {
  if (!rows.length) return null
  const i = current === null ? -1 : rows.findIndex((r) => r.id === current)
  if (i < 0) return (delta === 1 ? rows[0] : rows[rows.length - 1])!.id
  return rows[Math.min(rows.length - 1, Math.max(0, i + delta))]!.id
}

/**
 * Whether a row that re-rendered may take the focus back (#596): only when the focus was lost (on the body, or nowhere: the
 * element it was on left the page as its row moved sections), and never while the row sits behind an open modal (a dialog or a
 * menu hides the rest of the page from assistive technology, `aria-hidden`, and holds the focus even while it is on nothing, as
 * when the Rename dialog's focused button goes disabled as it saves). Anything that holds the focus keeps it: a dialog, a
 * field elsewhere, and in the list itself a row's reply field, its Stop question or a section's header, whose keys would
 * otherwise drive the list.
 */
export function mayRefocusRow(active: Node | null, body: Node | null, behindModal: boolean): boolean {
  if (behindModal) return false
  return active === null || active === body
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
  /** The corner mark: the session's status (sessionStatus), a capsule's its busiest member's (blockStatus); none for a share that does not say. */
  status?: StatusView
  /** Exited. */
  dashed: boolean
  /** A square's agent (its initials); a run link shared with you shows a crew instead. */
  agentId: string
  kind: 'session' | 'run' | 'shared'
  /** A session square's working directory (its folder's letters when squares would match, railGlyphs); none for a share. */
  cwd?: string
  /** Bottom left: where it comes from. */
  tile?: 'machine' | 'share'
  /** Bottom right: new events on it (the badges the Alerts rules route here). */
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
  /** In the sidebar's order: needs you, finished, working, shared, exited. */
  items: RailShape[]
  /** Loose exited sessions fold into +N; an exited run stays a capsule, dashed. */
  exitedFolded: number
}

/** "3 unread in chat". */
function unreadWords(n: number): string {
  return `${n} unread in chat`
}

function railSquare(row: SessionRow, marks: Readonly<Record<string, EventMark>>, unread: Readonly<Record<string, number>>, now: number): RailShape {
  const s = row.session
  const status = sessionStatus(s, now)
  const parts = [s.name, status.word]
  if (row.state === 'needs' && s.attention?.message) parts.push(s.attention.message)
  if (row.machine) parts.push(`on ${row.machine}`)
  const mark = marks[s.id]
  // A finished turn's event is counted below, but not named while the status says finished: the status's word says it.
  if (mark && !(mark.type === 'done' && status.key === 'finished')) parts.push(mark.label)
  const chat = unread[`session:${s.id}`] ?? 0
  if (chat) parts.push(unreadWords(chat))
  return { id: s.id, shape: 'square', to: `/sessions/${s.id}`, label: parts.join(' · '), state: row.state, status, dashed: row.state === 'exited', agentId: s.agentId, kind: 'session', tile: row.machine ? 'machine' : undefined, news: (mark ? 1 : 0) + chat, cwd: s.cwd }
}

/** "users api · run started 08:31 · review needs you · core working · lead exited", then "3 unread in chat" when the run's chat has them. */
export function railRunLabel(block: RunBlock, unread = 0, now = Date.now()): string {
  const sub = blockSubtitle(block).split(' · ')[0]!
  const when = /^(started|stopped) /.test(sub) ? `run ${sub}` : sub
  const parts = [block.title, when, ...block.items.map((it) => `${it.item.kind} needs you`), ...block.members.map((m) => `${m.session.name} ${sessionStatus(m.session, now).word}`)]
  if (unread) parts.push(unreadWords(unread))
  return parts.join(' · ')
}

function railCapsule(block: RunBlock, marks: Readonly<Record<string, EventMark>>, unread: Readonly<Record<string, number>>, now: number): RailShape {
  const members = block.members.map((m) => railSquare(m, marks, unread, now))
  const chat = unread[`run:${block.runId}`] ?? 0
  return {
    id: `run:${block.runId}`,
    shape: 'capsule',
    to: `/runs/${encodeURIComponent(block.runId)}`,
    label: railRunLabel(block, chat, now),
    state: block.state,
    // The block's own state, the one that placed it in the list: the list and the rail say one thing of a run (a run that waits on an
    // item is amber whatever its members do).
    status: statusOfKey(block.state),
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
export function railModel(model: SidebarModel, shared: readonly JoinedEntry[], marks: Readonly<Record<string, EventMark>>, unread: Readonly<Record<string, number>> = {}, now = Date.now()): RailModel {
  const items: RailShape[] = []
  let news = 0
  const count = (sq: RailShape) => {
    if (sq.news) news++
  }
  const add = (it: SidebarItem) => {
    // A loose item (a plan) is no session and no run: the rail draws sessions and runs, and counts it on top.
    if (it.kind === 'item') return
    if (it.kind === 'run') {
      const c = railCapsule(it, marks, unread, now)
      c.members!.forEach(count)
      if (unread[`run:${it.runId}`]) news++
      items.push(c)
    } else {
      const sq = railSquare(it, marks, unread, now)
      count(sq)
      items.push(sq)
    }
  }
  for (const it of [...model.needs, ...model.finished, ...model.working]) add(it)
  for (const e of shared) {
    const gone = e.lastStatus === 'revoked' || e.lastStatus === 'gone'
    items.push({
      id: `j:${e.id}`,
      shape: 'square',
      to: joinedPath(e),
      label: `${e.name} · ${e.role === 'control' ? 'control' : 'view only'} · through ${e.host}`,
      state: gone ? 'exited' : 'running',
      status: joinedStatus(e),
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
    else if (it.kind === 'session') {
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
