import type { SessionInfo } from '~/composables/useSessions'
import { isEnded } from './attention'

export type SessionGroupKey = 'needs' | 'running' | 'exited'
export interface SessionGroups {
  needs: SessionInfo[]
  running: SessionInfo[]
  exited: SessionInfo[]
}

/** Sidebar order: who needs you (newest signal first), then running, then ended. */
export function groupSessions(list: SessionInfo[]): SessionGroups {
  const g: SessionGroups = { needs: [], running: [], exited: [] }
  for (const s of list) {
    if (isEnded(s.status)) g.exited.push(s)
    else if (s.attention?.state === 'needs_input') g.needs.push(s)
    else g.running.push(s)
  }
  g.needs.sort((a, b) => (b.attention?.since ?? '').localeCompare(a.attention?.since ?? ''))
  g.running.sort((a, b) => b.createdAt.localeCompare(a.createdAt))
  g.exited.sort((a, b) => (b.endedAt ?? b.createdAt).localeCompare(a.endedAt ?? a.createdAt))
  return g
}

/** The session worth opening first: someone waiting (the newest signal), else the newest running one; none when neither. */
export function sessionWorthOpening(list: SessionInfo[]): SessionInfo | null {
  const g = groupSessions(list)
  return g.needs[0] ?? g.running[0] ?? null
}

/**
 * Where the home (`/`) goes (#359): below lg to the list, which is a page of its own there (design 3e); at lg and up to the session
 * worth opening, else nowhere: it stays and shows its welcome, so that nothing moves under a dialog opened on it.
 */
export function homeLeavesFor(wide: boolean, target: Pick<SessionInfo, 'id'> | null): string | null {
  if (!wide) return '/sessions'
  return target ? `/sessions/${target.id}` : null
}

/**
 * Where the list page (`/sessions`) goes (#359): nowhere below lg; at lg and up always away, since the sidebar is the list and
 * staying would show it twice beside it: to the session worth opening, else to the home, which shows the welcome.
 */
export function listLeavesFor(wide: boolean, target: Pick<SessionInfo, 'id'> | null): string | null {
  if (!wide) return null
  return target ? `/sessions/${target.id}` : '/'
}

/** Case-insensitive substring match over name, path, host, user, branch and agent. */
export function filterSessions(list: SessionInfo[], query: string): SessionInfo[] {
  const q = query.trim().toLowerCase()
  if (!q) return list
  return list.filter((s) => [s.name, s.cwd, s.hostName ?? '', s.hostUser ?? '', s.branch ?? '', s.agentId].some((v) => v.toLowerCase().includes(q)))
}

const AGENT_INITIALS: Record<string, string> = {
  claude: 'CC',
  'claude-code': 'CC',
  codex: 'CX',
  agy: 'AG',
  antigravity: 'AG',
  sh: '$_',
  bash: '$_',
  zsh: '$_',
  fish: '$_',
}

/** Two-character chip label for an agent id. */
export function agentInitials(agentId: string): string {
  const id = agentId.trim().toLowerCase()
  if (!id) return '?'
  const known = AGENT_INITIALS[id]
  if (known) return known
  return id.slice(0, 2).toUpperCase()
}

/** Avatar initials for a person's display name. */
export function initials(name: string): string {
  const words = name.trim().split(/\s+/).filter(Boolean)
  if (!words.length) return '?'
  if (words.length === 1) return words[0]!.slice(0, 2).toUpperCase()
  return words
    .slice(0, 2)
    .map((w) => w[0]!.toUpperCase())
    .join('')
}

/** "20s", "4m", "1h 4m", "2d"; with suffix: "38m ago". */
export function relativeTime(iso: string, now = Date.now(), opts: { suffix?: boolean } = {}): string {
  const sec = Math.max(0, Math.floor((now - Date.parse(iso)) / 1000))
  let out: string
  if (sec < 60) out = `${sec}s`
  else if (sec < 3600) out = `${Math.floor(sec / 60)}m`
  else if (sec < 86400) {
    const h = Math.floor(sec / 3600)
    const m = Math.floor((sec % 3600) / 60)
    out = m ? `${h}h ${m}m` : `${h}h`
  } else out = `${Math.floor(sec / 86400)}d`
  return opts.suffix ? `${out} ago` : out
}

/** Collapses /home/<user> and /Users/<user> to ~. */
export function shortCwd(cwd: string): string {
  return cwd.replace(/^\/(?:home|Users)\/[^/]+/, '~')
}

const AGENT_NAMES: Record<string, string> = { claude: 'Claude Code', codex: 'Codex', agy: 'Antigravity' }

/** An agent's name as a session's page says it: the known agents by name, any other by its id. */
export function agentName(agentId: string): string {
  return AGENT_NAMES[agentId] ?? agentId
}

/** The last part of a path, `/` or `\` separated, a trailing separator ignored; the path itself when it has none. */
function folderOf(cwd: string): string {
  const parts = cwd.split(/[/\\]+/).filter(Boolean)
  return parts[parts.length - 1] ?? cwd
}

/**
 * The line under a session's name on its page (P11): the agent, the folder (the last part of its working directory) and the branch,
 * joined by " · ", a hosted session's machine after the agent ("Codex on build-box"), since that is what tells it apart. No id: those
 * are in the page's More menu. `title` is what the line leaves out, its tooltip: the whole path, and a hosted session's user and host.
 */
export function sessionHeadline(s: Pick<SessionInfo, 'agentId' | 'cwd' | 'branch' | 'kind' | 'hostName' | 'hostUser'>): { line: string; title: string } {
  const hosted = s.kind === 'hosted'
  const agent = agentName(s.agentId)
  const parts = [hosted ? `${agent} on ${s.hostName || 'its host'}` : agent, folderOf(s.cwd)]
  if (s.branch) parts.push(s.branch)
  const title = hosted ? `${s.cwd} · ${s.hostUser || 'someone'} on ${s.hostName || 'its host'}` : s.cwd
  return { line: parts.join(' · '), title }
}

/** A failed read's status and code, when it is the API's error (ApiError) or shaped as one. */
function apiStatus(e: unknown): { status: number; code: string } | null {
  if (!(e instanceof Error)) return null
  const { status, code } = e as Error & { status?: unknown; code?: unknown }
  return typeof status === 'number' && typeof code === 'string' ? { status, code } : null
}

/** Whether a session's read failed because the server holds no such session: its 404 `not_found`, the one answer a gone page follows. */
export function sessionGone(err: unknown): boolean {
  const a = apiStatus(err)
  return a?.status === 404 && a.code === 'not_found'
}

/** The page of a session the server no longer holds (D7): its record, a record forgotten, or a record that could not be read. */
export type GonePage =
  | { kind: 'record'; name: string }
  | { kind: 'forgotten'; history: true }
  | { kind: 'unread'; words: string; history: boolean }

/**
 * What a session's page shows when the session's read failed with `err` and its record's read gave `read` (D7): `record` when the
 * server no longer holds the session but kept its record; `forgotten` when the record answers 404 `not_found` too; `unread` when the
 * record's read failed otherwise, in its own words, `history: false` where the server keeps no history (404 `history_unavailable`,
 * a switchyard) so the page offers no way there. Null for any error but the session's 404 `not_found`: a 401, a token asked for, a
 * failed network is never "gone", and the page keeps its alert.
 */
export function gonePage(err: unknown, read: { ok: { name: string } } | { failed: unknown }): GonePage | null {
  if (!sessionGone(err)) return null
  if ('ok' in read) return { kind: 'record', name: read.ok.name }
  const a = apiStatus(read.failed)
  if (a?.status === 404 && a.code === 'not_found') return { kind: 'forgotten', history: true }
  const words = read.failed instanceof Error ? read.failed.message : String(read.failed)
  return { kind: 'unread', words, history: !(a?.status === 404 && a.code === 'history_unavailable') }
}

/** One-line description under a session name: "~/src/api · hosted · 3 here". */
export function sessionMeta(s: SessionInfo, now = Date.now()): string {
  const where = s.kind === 'hosted' ? 'hosted' : 'server'
  const tail = s.viewers > 0 ? `${s.viewers} here` : relativeTime(s.createdAt, now)
  return `${shortCwd(s.cwd)} · ${where} · ${tail}`
}
