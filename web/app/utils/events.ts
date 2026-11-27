import type { SessionInfo, WebhookInfo } from '~/composables/useSessions'
import type { ActivityEntry } from '~/utils/protocol'

/**
 * Something a session reported, as the Alerts rules route it: the six event
 * types agents report, the three attention states (an `attention` entry counts
 * as the state it recorded) and `exit_nonzero`, a process that exited on its
 * own with a non-zero code.
 */
export type EventType = 'needs_input' | 'done' | 'working' | 'tool_denied' | 'progress' | 'artifact' | 'handoff' | 'error' | 'exit_nonzero' | 'tool_use' | 'file'

/**
 * Where events of one type go in this browser: a badge on the session in the
 * sidebar and on its wall tile, a browser notification with the chime, a jump
 * of the carousel's follow mode, and a line in the live feed.
 */
export interface RouteRow { badge: boolean; browser: boolean; wall: boolean; feed: boolean }

/** Every event type, in the order the routing lists them by default. */
export const EVENT_TYPES: readonly EventType[] = ['needs_input', 'done', 'working', 'tool_denied', 'progress', 'artifact', 'handoff', 'error', 'exit_nonzero', 'tool_use', 'file']

function row(badge: boolean, browser: boolean, wall: boolean, feed: boolean): RouteRow {
  return Object.freeze({ badge, browser, wall, feed })
}

/**
 * Routes nobody has changed: the mockup's matrix, with `error` routed like
 * `tool_denied`. Frozen; parseRoutes hands out copies.
 */
export const DEFAULT_ROUTES: Record<EventType, RouteRow> = Object.freeze({
  needs_input: row(true, true, true, true),
  done: row(true, false, false, true),
  working: row(false, false, false, true),
  tool_denied: row(true, true, false, true),
  progress: row(false, false, false, true),
  artifact: row(false, true, false, true),
  handoff: row(false, false, true, true),
  error: row(true, true, false, true),
  exit_nonzero: row(true, true, false, true),
  tool_use: row(false, false, false, true),
  file: row(false, false, false, true),
})

export type EventColor = 'warning' | 'success' | 'error' | 'info' | 'neutral'

export interface EventInfo {
  /** Short name on a session badge. */
  label: string
  /** What happened, in a sentence's words: the routing row's heading. */
  title: string
  icon: string
  /** Its colour in the feed and on a session badge, by severity. */
  color: EventColor
  /** What reports it, under its title on the Routing tab. */
  source: string
}

export const EVENT_INFO: Record<EventType, EventInfo> = {
  needs_input: { title: 'An agent asks for input', label: 'needs input', icon: 'i-lucide-hand', color: 'warning', source: 'permission and question hooks · bell · OSC 9/777 · screen pattern' },
  done: { title: 'An agent finished its turn', label: 'finished', icon: 'i-lucide-check', color: 'neutral', source: 'the agent finished its turn' },
  working: { title: 'An agent is at work', label: 'working', icon: 'i-lucide-loader-circle', color: 'neutral', source: 'a prompt submitted · a tool about to run (the hooks) · Codex\'s title saying Working · clears the badge' },
  tool_denied: { title: 'A tool call was refused', label: 'denied', icon: 'i-lucide-shield-x', color: 'error', source: 'a tool call was refused' },
  progress: { title: 'A step of a long task', label: 'progress', icon: 'i-lucide-list-checks', color: 'success', source: 'skill: a step of a long task' },
  artifact: { title: 'A pull request, file or URL', label: 'artifact', icon: 'i-lucide-link', color: 'info', source: 'skill: a pull request, file or URL' },
  handoff: { title: 'Work handed to another member', label: 'handoff', icon: 'i-lucide-arrow-right-left', color: 'info', source: 'skill: work passed to another member' },
  error: { title: 'An agent hit an error', label: 'error', icon: 'i-lucide-triangle-alert', color: 'error', source: 'an error the agent hit' },
  exit_nonzero: { title: 'A process exited badly', label: 'exited', icon: 'i-lucide-circle-x', color: 'error', source: 'the process exited with a non-zero code' },
  tool_use: { title: 'Every tool call', label: 'tool', icon: 'i-lucide-wrench', color: 'neutral', source: 'every tool call (chatty)' },
  file: { title: 'A file the agent touched', label: 'file', icon: 'i-lucide-file-pen', color: 'neutral', source: 'the files the agent reads, edits, writes and deletes (quiet)' },
}

type StateEvent = 'needs_input' | 'working' | 'done'

function isState(s?: string): s is StateEvent {
  return s === 'needs_input' || s === 'working' || s === 'done'
}

/**
 * Whether the session's attention is the change this attention entry records:
 * the session logs the report's message, or the state itself when the report
 * had none.
 */
function records(session: SessionInfo | undefined, e: ActivityEntry): boolean {
  const a = session?.attention
  if (!a || !isState(a.state)) return false
  return a.message ? a.message === e.message : e.message === a.state
}

/** The code of a process that exited on its own, from its status entry ("exited (exit 1)"); null otherwise. */
export function exitCode(message?: string): number | null {
  const m = /^exited \(exit (-?\d+)\)$/.exec(message ?? '')
  return m ? Number(m[1]) : null
}

/**
 * The event type of an activity entry, or null for entries that are not
 * events (join, leave, input, link, other status changes). An `attention`
 * entry counts as the attention state it records: an entry from the event
 * stream carries that state, and the session is consulted only for one that
 * does not (a viewer's replay, an older host), where the entry counts as the
 * state of the session's attention it records, and one recorded without a
 * message names the state itself. A status entry counts only for a process
 * that exited with a non-zero code: an admin's Stop ends one with a signal,
 * which is not the agent failing.
 */
export function eventTypeOf(entry: ActivityEntry, session?: SessionInfo): EventType | null {
  switch (entry.type) {
    case 'attention': {
      // The event stream says which state the entry records.
      if (isState(entry.state)) return entry.state
      if (records(session, entry)) return session!.attention!.state as StateEvent
      if (isState(entry.message)) return entry.message
      const state = session?.attention?.state
      return isState(state) ? state : null
    }
    case 'status': {
      const code = exitCode(entry.message)
      return code !== null && code !== 0 ? 'exit_nonzero' : null
    }
    case 'progress':
    case 'artifact':
    case 'handoff':
    case 'tool_use':
    case 'tool_denied':
    case 'error':
    case 'file':
      return entry.type
  }
  return null
}

/**
 * Whether eventTypeOf can type an entry against this session yet. An entry
 * from the event stream carries its state and waits for nothing. The session
 * is consulted only for one that does not (a viewer's replay, an older host):
 * the admin stream sends an attention entry just before the session change
 * that carries its state, so one with a message of its own waits for the
 * session to show that message; one recorded without a message names its
 * state.
 */
export function attentionSettled(entry: ActivityEntry, session?: SessionInfo): boolean {
  return entry.type !== 'attention' || isState(entry.state) || records(session, entry) || isState(entry.message)
}

/**
 * The hosts of the webhooks the server sends events of type t to, sorted and
 * each once: those that list t and, for an attention state or exit_nonzero,
 * those that list the entry type it comes from (attention, status), which
 * get every entry of that type.
 */
export function webhookHosts(webhooks: readonly WebhookInfo[] | undefined, t: EventType): string[] {
  const from = isState(t) ? 'attention' : t === 'exit_nonzero' ? 'status' : null
  const hosts = new Set<string>()
  for (const w of webhooks ?? []) {
    if (!w.events.includes(t) && !(from && w.events.includes(from))) continue
    let host = w.url
    try {
      host = new URL(w.url).host || w.url
    } catch {
      /* shown as it is */
    }
    hosts.add(host)
  }
  return [...hosts].sort()
}

/** Longest URL an event carries, in bytes (the server's limit). */
export const MAX_URL_BYTES = 2048

const encoder = new TextEncoder()

/**
 * The URL itself when it is safe to render as a link: http(s) only, at most
 * 2048 bytes, no whitespace or control characters. Anything else (a
 * `javascript:` URL, a local path) is shown as text, never linked.
 */
export function linkableUrl(url?: string): string | null {
  if (!url || /[\s\u0000-\u001f\u007f-\u009f]/.test(url)) return null
  if (encoder.encode(url).length > MAX_URL_BYTES) return null
  let parsed: URL
  try {
    parsed = new URL(url)
  } catch {
    return null
  }
  return parsed.protocol === 'http:' || parsed.protocol === 'https:' ? url : null
}

function flag(v: unknown, fallback: boolean): boolean {
  return typeof v === 'boolean' ? v : fallback
}

/**
 * Routes from their stored JSON: known types and keys with boolean values are
 * kept, unknown keys ignored, and whatever is missing or malformed falls back
 * to DEFAULT_ROUTES. Always a fresh object.
 */
export function parseRoutes(raw: string | null | undefined): Record<EventType, RouteRow> {
  let stored: unknown
  try {
    stored = raw ? JSON.parse(raw) : undefined
  } catch {
    stored = undefined
  }
  const obj = stored && typeof stored === 'object' && !Array.isArray(stored) ? (stored as Record<string, unknown>) : {}
  const out = {} as Record<EventType, RouteRow>
  for (const t of EVENT_TYPES) {
    const r = Object.hasOwn(obj, t) ? obj[t] : undefined
    const given = r && typeof r === 'object' && !Array.isArray(r) ? (r as Record<string, unknown>) : {}
    const d = DEFAULT_ROUTES[t]
    out[t] = { badge: flag(given.badge, d.badge), browser: flag(given.browser, d.browser), wall: flag(given.wall, d.wall), feed: flag(given.feed, d.feed) }
  }
  return out
}

/** The list with item appended, keeping the newest `max` entries. */
export function appendRing<T>(list: readonly T[], item: T, max: number): T[] {
  const out = list.length >= max ? list.slice(list.length - max + 1) : list.slice()
  out.push(item)
  return out
}

/** Lines the live feed keeps, across sessions, in memory only. */
export const FEED_SIZE = 500

/** One line of the live feed, with what it shows worked out once, when it arrived. */
export interface FeedEntry extends ActivityEntry {
  sessionId: string
  event: EventType
  /** The session's name when the entry arrived: the line outlives the session. */
  sessionName: string
  seq: number
  /** The time of day of `at`, as the feed shows it. */
  time: string
  /** eventDetail of the entry. */
  detail: string
  /** The URL when it may be rendered as a link (linkableUrl), else null. */
  link: string | null
}

/**
 * The badge a session carries for its newest event routed to Badge, until the
 * session reports `working` or `needs_input` (whose amber dot follows the live
 * state instead) or its page is opened.
 */
export interface EventMark {
  type: EventType
  at: string
  label: string
  color: EventColor
  detail: string
}

/** What routing keeps: the feed, oldest first, and one badge per session. */
export interface EventState {
  feed: readonly FeedEntry[]
  marks: Readonly<Record<string, EventMark>>
}

/** A typed event of a session, as listeners (browser alerts, the carousel's follow mode) receive it. */
export interface RoutedEvent {
  sessionId: string
  session?: SessionInfo
  type: EventType
  entry: ActivityEntry
}

const timeOfDay = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false })

/** The time of day of an ISO time, with seconds, as the feed shows it; empty for a bad time. */
export function feedTime(at: string): string {
  const d = new Date(at)
  return Number.isNaN(d.getTime()) ? '' : timeOfDay.format(d)
}

function withoutMark(marks: Readonly<Record<string, EventMark>>, sessionId: string): Readonly<Record<string, EventMark>> {
  if (!(sessionId in marks)) return marks
  const next = { ...marks }
  delete next[sessionId]
  return next
}

/**
 * Routes one activity entry of a session: its event type against the session
 * as the store shows it, a feed line when its Feed route is on, and the
 * session's badge, set when its Badge route is on and cleared by `working` and
 * `needs_input` whatever theirs say. Returns the next state (the same objects
 * where nothing changed) and the type, null for an entry that is not an event.
 */
/** Whether an entry is a live event, one the listeners (browser notifications, the wall, the follow jump) hear: not one that reached the server late. */
export function liveEvent(entry: ActivityEntry): boolean {
  return !entry.late
}

export function routeEntry(
  state: EventState,
  sessionId: string,
  entry: ActivityEntry,
  session: SessionInfo | undefined,
  routes: Record<EventType, RouteRow>,
  seq: number,
  max = FEED_SIZE,
): { state: EventState; type: EventType | null } {
  const type = eventTypeOf(entry, session)
  if (!type) return { state, type: null }
  const route = routes[type]
  let { feed, marks } = state
  if (route.feed) {
    const line: FeedEntry = { ...entry, sessionId, event: type, sessionName: session?.name || sessionId, seq, time: feedTime(entry.at), detail: eventDetail(entry), link: linkableUrl(entry.url) }
    feed = appendRing(feed, line, max)
  }
  // An entry that reached the server late is shown in the feed alone: what its session has moved past changes no badge.
  if (!liveEvent(entry)) return { state: { feed, marks }, type }
  if (type === 'working' || type === 'needs_input') marks = withoutMark(marks, sessionId)
  else if (route.badge) marks = { ...marks, [sessionId]: { type, at: entry.at, ...markOf(type, entry) } }
  return { state: { feed, marks }, type }
}

/**
 * The badges left once the Badge route of their type is off, or their session
 * is one `keep` rejects (gone from the store); the same object when none goes.
 */
export function pruneMarks(
  marks: Readonly<Record<string, EventMark>>,
  routes: Record<EventType, RouteRow>,
  keep?: (sessionId: string) => boolean,
): Readonly<Record<string, EventMark>> {
  const gone = Object.keys(marks).filter((id) => !routes[marks[id]!.type].badge || (keep && !keep(id)))
  if (!gone.length) return marks
  const next = { ...marks }
  for (const id of gone) delete next[id]
  return next
}

/**
 * Where the carousel's follow mode moves for an event: the index of its
 * session among those shown, or null to stay. Only a type routed to Roundhouse jump
 * moves it, never needs_input (follow mode jumps for that from the session
 * state), and never while follow mode is off, someone types, or it holds on a
 * session that needs input.
 */
export function followJump(
  e: Pick<RoutedEvent, 'type' | 'sessionId'>,
  ctx: { follow: boolean; routes: Record<EventType, RouteRow>; typing: boolean; holding: boolean; activeIds: readonly string[]; selected: number },
): number | null {
  if (e.type === 'needs_input' || !ctx.follow || !ctx.routes[e.type].wall || ctx.typing || ctx.holding) return null
  const i = ctx.activeIds.indexOf(e.sessionId)
  return i >= 0 && i !== ctx.selected ? i : null
}

/**
 * The browser alert for an event routed to Browser: a title naming the
 * session and what happened, a body, and a tag per session and type so a
 * burst replaces one notification. null when the type is not routed there,
 * and for needs_input, which alerts from the session state.
 */
export function eventAlert(e: RoutedEvent, routes: Record<EventType, RouteRow>): { title: string; body: string; tag: string } | null {
  if (e.type === 'needs_input' || !routes[e.type].browser) return null
  const mark = markOf(e.type, e.entry)
  const what = e.type === 'exit_nonzero' ? mark.label : e.type.replace('_', ' ')
  return { title: `${e.session?.name || e.sessionId}: ${what}`, body: mark.detail || EVENT_INFO[e.type].source, tag: `conductor-${e.sessionId}-${e.type}` }
}

/** The past tense of a file event's op: "edited", "read", "wrote", "deleted". */
export function fileOpWords(op?: string): string {
  switch (op) {
    case 'edit':
      return 'edited'
    case 'write':
      return 'wrote'
    case 'delete':
      return 'deleted'
    case 'read':
      return 'read'
  }
  return 'touched'
}

/** How a file event was learned, for its line: the agent's tool as it names it, or "seen by git" for a file the session found changed in git with no hook naming it (round 13, G2a). */
export function fileToolWords(tool?: string): string {
  return tool === 'git' ? 'seen by git' : (tool ?? '')
}

/** What an entry says beyond its type, for one line of the feed or a tooltip. The URL of an artifact is rendered on its own. */
export function eventDetail(e: ActivityEntry): string {
  const msg = e.message ?? ''
  switch (e.type) {
    case 'attention':
      return isState(msg) ? '' : msg
    case 'handoff':
      return e.to ? `to ${e.to}${msg ? `: ${msg}` : ''}` : msg
    case 'tool_use':
    case 'tool_denied':
    case 'error':
      return [e.tool, msg].filter(Boolean).join(': ')
    case 'file':
      return `${fileOpWords(e.op)} ${e.path ?? ''}${e.tool ? ` · ${fileToolWords(e.tool)}` : ''}`.trim()
  }
  return msg
}

/** The badge a session gets for an event routed to Badge: a short label and a colour by severity. */
export function markOf(type: EventType, entry: ActivityEntry): { label: string; color: EventColor; detail: string } {
  const code = type === 'exit_nonzero' ? exitCode(entry.message) : null
  const label = code !== null ? `exit ${code}` : EVENT_INFO[type].label
  return { label, color: EVENT_INFO[type].color, detail: eventDetail(entry) || entry.url || '' }
}

/** Tailwind text colour of each event colour. */
export const COLOR_TEXT: Record<EventColor, string> = {
  warning: 'text-warning',
  success: 'text-success',
  error: 'text-error',
  info: 'text-info',
  neutral: 'text-muted',
}

/**
 * The icon and colour of an activity entry in a session's Activity tab. An
 * attention entry with a message of its own gets a plain bell: which state it
 * recorded is known only while it is the session's current one.
 */
export function entryIcon(e: ActivityEntry): { icon: string; color: EventColor } {
  const t = eventTypeOf(e)
  if (t) return { icon: EVENT_INFO[t].icon, color: EVENT_INFO[t].color }
  switch (e.type) {
    case 'attention':
      return { icon: 'i-lucide-bell', color: 'neutral' }
    case 'input':
      return { icon: 'i-lucide-keyboard', color: 'neutral' }
    case 'join':
      return { icon: 'i-lucide-log-in', color: 'neutral' }
    case 'leave':
      return { icon: 'i-lucide-log-out', color: 'neutral' }
    case 'link':
      return { icon: 'i-lucide-share-2', color: 'neutral' }
  }
  return { icon: 'i-lucide-power', color: 'neutral' }
}

/**
 * Holds activity entries per session until eventTypeOf can type them. An
 * attention entry that carries its state (every one the stream sends now,
 * except one from an older host) can be typed at once. An entry without a
 * state comes just before the session change that carries its state, so an
 * entry attentionSettled refuses waits for settle() after that change, or
 * `holdMs` at most, and every later entry of the same session waits behind
 * it so the session's entries come out in order. Each released entry goes to
 * `release`, which types it against the session as the store has it then.
 */
export class EntryHold {
  private held = new Map<string, { items: Array<{ entry: ActivityEntry; until: number }>; timer?: ReturnType<typeof setTimeout> }>()

  constructor(
    private readonly session: (sessionId: string) => SessionInfo | undefined,
    private readonly release: (sessionId: string, entry: ActivityEntry) => void,
    private readonly holdMs = 2000,
  ) {}

  /** Takes one entry of a session: released now when nothing of its session waits and it can be typed, held otherwise. */
  push(sessionId: string, entry: ActivityEntry): void {
    const q = this.held.get(sessionId)
    if (!q && attentionSettled(entry, this.session(sessionId))) {
      this.release(sessionId, entry)
      return
    }
    const item = { entry, until: Date.now() + this.holdMs }
    if (q) q.items.push(item)
    else this.held.set(sessionId, { items: [item] })
    this.drain(sessionId)
  }

  /** The store changed a session, or every session (a snapshot): release what that settles. */
  settle(sessionId?: string): void {
    if (sessionId !== undefined) this.drain(sessionId)
    else for (const id of [...this.held.keys()]) this.drain(id)
  }

  /** The session is gone: release what it holds now, in order. */
  forget(sessionId: string): void {
    const q = this.held.get(sessionId)
    if (!q) return
    clearTimeout(q.timer)
    this.held.delete(sessionId)
    for (const item of q.items) this.release(sessionId, item.entry)
  }

  /** A snapshot lists the sessions left: forget every other one. */
  retain(keep: (sessionId: string) => boolean): void {
    for (const id of [...this.held.keys()]) if (!keep(id)) this.forget(id)
  }

  /** Drops everything held, releasing nothing (another workbench token, another view of the server). */
  clear(): void {
    for (const q of this.held.values()) clearTimeout(q.timer)
    this.held.clear()
  }

  /**
   * Releases a session's entries in order while the first is settled or out
   * of time; `expired` releases the first regardless (its timer fired). A
   * timer then waits for whichever entry comes first.
   */
  private drain(sessionId: string, expired = false): void {
    const q = this.held.get(sessionId)
    if (!q) return
    clearTimeout(q.timer)
    q.timer = undefined
    while (q.items.length) {
      const first = q.items[0]!
      if (!expired && Date.now() < first.until && !attentionSettled(first.entry, this.session(sessionId))) break
      expired = false
      q.items.shift()
      this.release(sessionId, first.entry)
    }
    if (!q.items.length) {
      this.held.delete(sessionId)
      return
    }
    q.timer = setTimeout(() => this.drain(sessionId, true), Math.max(0, q.items[0]!.until - Date.now()))
  }
}

/** The routing rows, grouped by how loud each event is. Every type is in exactly one group. */
export const ROUTE_GROUPS: ReadonlyArray<{ key: string; title: string; note: string; types: readonly EventType[] }> = [
  { key: 'loud', title: 'Needs you', note: 'interrupts', types: ['needs_input', 'tool_denied', 'error', 'exit_nonzero'] },
  { key: 'notice', title: 'Worth knowing', note: 'shows up, never interrupts', types: ['done', 'handoff', 'artifact'] },
  { key: 'quiet', title: 'Background', note: 'feed only', types: ['working', 'progress', 'tool_use', 'file'] },
]

/** The destinations a route row has, as the routing pills and the readout name them. */
export const ROUTE_KEYS: ReadonlyArray<{ key: keyof RouteRow; label: string; icon: string; summary: string; hint: string }> = [
  { key: 'badge', label: 'Badge', icon: 'i-lucide-circle-dot', summary: 'Sidebar badge', hint: 'A badge on the session in the sidebar and on its Yard tile' },
  { key: 'browser', label: 'This device', icon: 'i-lucide-bell', summary: 'Notification on this device', hint: 'A notification on this device and the chime, as switched on under Alerts' },
  { key: 'wall', label: 'Roundhouse jump', icon: 'i-lucide-gallery-horizontal', summary: 'Roundhouse jumps to it', hint: 'The Roundhouse jumps to the session while it follows routed events' },
  { key: 'feed', label: 'Feed', icon: 'i-lucide-list', summary: 'Feed', hint: 'A line in the live feed' },
]

/** The short names the readout uses for each type. */
const SUMMARY_NAMES: Record<EventType, string> = {
  needs_input: 'needs you',
  done: 'done',
  working: 'working',
  tool_denied: 'tool denied',
  progress: 'progress',
  artifact: 'artifacts',
  handoff: 'handoffs',
  error: 'errors',
  exit_nonzero: 'exit ≠ 0',
  tool_use: 'tool calls',
  file: 'files touched',
}

/** "Where they go": per destination that interrupts or marks (browser, wall, badge), the types routed there; destinations with none are left out. */
export function routeSummary(routes: Record<EventType, RouteRow>): Array<{ key: keyof RouteRow; icon: string; title: string; types: string }> {
  const out: Array<{ key: keyof RouteRow; icon: string; title: string; types: string }> = []
  for (const key of ['browser', 'wall', 'badge'] as const) {
    const types = EVENT_TYPES.filter((t) => routes[t][key] && !(t === 'working' && key === 'badge'))
    if (!types.length) continue
    const k = ROUTE_KEYS.find((r) => r.key === key)!
    out.push({ key, icon: k.icon, title: k.summary, types: types.map((t) => SUMMARY_NAMES[t]).join(', ') })
  }
  return out
}

/** The feed in groups of one minute, newest first; the current minute's header says "now". */
export function feedGroups<T extends Pick<FeedEntry, 'at' | 'seq'>>(entries: readonly T[], now: number): Array<{ key: string; label: string; entries: T[] }> {
  const groups: Array<{ key: string; label: string; entries: T[] }> = []
  const nowMinute = Math.floor(now / 60_000)
  for (let i = entries.length - 1; i >= 0; i--) {
    const e = entries[i]!
    const t = Date.parse(e.at)
    const minute = Number.isNaN(t) ? -1 : Math.floor(t / 60_000)
    const key = String(minute)
    let g = groups.at(-1)
    if (!g || g.key !== key) {
      const d = new Date(minute * 60_000)
      const hm = minute < 0 ? '' : `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
      g = { key, label: minute === nowMinute ? `${hm} · now` : hm, entries: [] }
      groups.push(g)
    }
    g.entries.push(e)
  }
  return groups
}

/** The kinds the last-hour bars stack, loudest at the bottom: what needs you, handoffs, errors, and everything else. */
export type SparkKind = 'needs' | 'handoff' | 'error' | 'other'

/** What a feed event counts as in the last-hour bars. */
export function sparkKind(t: EventType): SparkKind {
  if (t === 'needs_input') return 'needs'
  if (t === 'handoff') return 'handoff'
  if (t === 'error' || t === 'exit_nonzero' || t === 'tool_denied') return 'error'
  return 'other'
}

/** The feed's events per minute over the last `minutes`, oldest first, one bucket a minute, and the totals per kind. */
export function sparkBuckets(entries: ReadonlyArray<Pick<FeedEntry, 'at' | 'event'>>, now: number, minutes = 60): { buckets: Array<Record<SparkKind, number> & { minute: number }>; totals: Record<SparkKind, number> } {
  const end = Math.floor(now / 60_000) * 60_000
  const start = end - (minutes - 1) * 60_000
  const buckets: Array<Record<SparkKind, number> & { minute: number }> = []
  for (let t = start; t <= end; t += 60_000) buckets.push({ minute: t, needs: 0, handoff: 0, error: 0, other: 0 })
  const totals: Record<SparkKind, number> = { needs: 0, handoff: 0, error: 0, other: 0 }
  for (const e of entries) {
    const at = Date.parse(e.at)
    if (Number.isNaN(at) || at < start || at >= end + 60_000) continue
    const k = sparkKind(e.event)
    buckets[Math.floor((at - start) / 60_000)]![k]++
    totals[k]++
  }
  return { buckets, totals }
}

/** Where an old link to the retired Events page lands (`/events`, `?tab=`): the feed in History, the rules in Alerts, Agents' Reporting. */
export interface EventsLanding {
  path: string
  query: Record<string, string | null>
  hash?: string
}

/**
 * The Events page is retired (the UX review's P12): its feed is History's Feed view, its Routing the rules in the Alerts panel (opened
 * over History's Feed by `alerts` in the address), and its Integrations tab Agents' section Reporting to Conductor. Any other tab, or
 * none, lands on the feed.
 */
export function eventsRedirect(query: Record<string, unknown>): EventsLanding {
  const tab = typeof query.tab === 'string' ? query.tab : ''
  // A redirect keeps the old address's query unless it gives one: each gives its own, so `tab=` never follows.
  if (tab === 'integrations') return { path: '/agents', query: {}, hash: '#reporting' }
  if (tab === 'routing') return { path: '/history', query: { view: 'feed', alerts: null } }
  return { path: '/history', query: { view: 'feed' } }
}
