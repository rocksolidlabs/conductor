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

/** One-line description under a session name: "~/src/api · hosted · 3 here". */
export function sessionMeta(s: SessionInfo, now = Date.now()): string {
  const where = s.kind === 'hosted' ? 'hosted' : 'server'
  const tail = s.viewers > 0 ? `${s.viewers} here` : relativeTime(s.createdAt, now)
  return `${shortCwd(s.cwd)} · ${where} · ${tail}`
}
