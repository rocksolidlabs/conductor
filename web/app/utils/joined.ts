import type { JoinInfo, SessionKind, SessionStatus } from '~/composables/useSessions'
import type { Role } from './protocol'
import { joinServer, SHARE_TOKEN } from './invite'
import type { KeyValueStore } from './sidebar'

/**
 * The share links this workbench has joined, kept so that a session someone
 * shared stays one click away in the sidebar ("Shared with you") instead of
 * taking the window over. Kept in this browser alone, at most JOINED_MAX; an
 * entry holds the link's token, which is the credential the link is, pinned
 * to the server it was shared through.
 */

export const JOINED_KEY = 'conductor.joined'
export const JOINED_MAX = 20

/** What the last look at a link said: it works; it was revoked or expired; what it opened is gone; the server did not answer (or does not allow this origin). */
export type JoinedStatus = 'ok' | 'revoked' | 'gone' | 'unreachable'

export interface JoinedEntry {
  /** A random id: the page's handle on the entry, never the token. */
  id: string
  token: string
  /** '' for this workbench's own server, else the server the link was shared through (as joinServer accepts it). */
  server: string
  kind: 'session' | 'run'
  name: string
  role: Role
  /** The host the link was shared through, as the row says it. */
  host: string
  agentId?: string
  sessionKind?: SessionKind
  /** The machine a hosted session runs on. */
  hostedOn?: string
  /** A run's member count. */
  members?: number
  sessionStatus?: SessionStatus
  addedAt: string
  lastSeenAt?: string
  lastStatus?: JoinedStatus
}

const KINDS = new Set(['session', 'run'])
const ROLES = new Set(['view', 'control'])
const STATUSES = new Set(['ok', 'revoked', 'gone', 'unreachable'])
const str = (v: unknown): v is string => typeof v === 'string'

/** One stored entry, checked field by field; null for one that cannot be used. Unknown fields are dropped. */
function entryOf(v: unknown): JoinedEntry | null {
  if (!v || typeof v !== 'object') return null
  const o = v as Record<string, unknown>
  if (!str(o.id) || !str(o.token) || !SHARE_TOKEN.test(o.token) || !str(o.server) || !str(o.name) || !str(o.host) || !str(o.addedAt)) return null
  if (o.server !== '' && joinServer(o.server) !== o.server) return null
  if (!KINDS.has(o.kind as string) || !ROLES.has(o.role as string)) return null
  const e: JoinedEntry = { id: o.id, token: o.token, server: o.server, kind: o.kind as JoinedEntry['kind'], name: o.name, role: o.role as Role, host: o.host, addedAt: o.addedAt }
  if (str(o.agentId)) e.agentId = o.agentId
  if (o.sessionKind === 'server' || o.sessionKind === 'hosted') e.sessionKind = o.sessionKind
  if (str(o.hostedOn)) e.hostedOn = o.hostedOn
  if (typeof o.members === 'number') e.members = o.members
  if (str(o.sessionStatus)) e.sessionStatus = o.sessionStatus as SessionStatus
  if (str(o.lastSeenAt)) e.lastSeenAt = o.lastSeenAt
  if (STATUSES.has(o.lastStatus as string)) e.lastStatus = o.lastStatus as JoinedStatus
  return e
}

/** The kept entries; none when nothing usable is kept (or the storage refuses). */
export function readJoined(storage: KeyValueStore): JoinedEntry[] {
  try {
    const v = JSON.parse(storage.getItem(JOINED_KEY) ?? '[]') as unknown
    if (!Array.isArray(v)) return []
    return v.map(entryOf).filter((e): e is JoinedEntry => e !== null).slice(0, JOINED_MAX)
  } catch {
    return []
  }
}

/** Keeps the entries; a storage that refuses keeps nothing, and the list lasts the visit. */
export function writeJoined(storage: KeyValueStore, list: readonly JoinedEntry[]): void {
  try {
    storage.setItem(JOINED_KEY, JSON.stringify(list))
  } catch {
    /* storage refused */
  }
}

/**
 * Adds an entry first, or replaces the one for the same server and token in place (its id and when it was added kept). Over the
 * bound the revoked and gone go first, then the oldest.
 */
export function upsertJoined(list: readonly JoinedEntry[], entry: Omit<JoinedEntry, 'id' | 'addedAt'>, now = Date.now(), id: () => string = () => crypto.randomUUID()): JoinedEntry[] {
  const at = list.findIndex((e) => e.server === entry.server && e.token === entry.token)
  if (at >= 0) {
    const next = [...list]
    next[at] = { ...entry, id: list[at]!.id, addedAt: list[at]!.addedAt }
    return next
  }
  const next = [{ ...entry, id: id(), addedAt: new Date(now).toISOString() }, ...list]
  while (next.length > JOINED_MAX) {
    const dead = next.findLastIndex((e) => e.lastStatus === 'revoked' || e.lastStatus === 'gone')
    if (dead > 0) next.splice(dead, 1)
    else {
      let oldest = 1
      for (let i = 2; i < next.length; i++) if (next[i]!.addedAt < next[oldest]!.addedAt) oldest = i
      next.splice(oldest, 1)
    }
  }
  return next
}

export function patchJoined(list: readonly JoinedEntry[], id: string, patch: Partial<JoinedEntry>): JoinedEntry[] {
  return list.map((e) => (e.id === id ? { ...e, ...patch, id: e.id, token: e.token, server: e.server } : e))
}

export function removeJoined(list: readonly JoinedEntry[], id: string): JoinedEntry[] {
  return list.filter((e) => e.id !== id)
}

/** What an entry keeps of a join reply. */
export function joinedFromInfo(info: JoinInfo): Pick<JoinedEntry, 'kind' | 'name' | 'role' | 'agentId' | 'sessionKind' | 'hostedOn' | 'members' | 'sessionStatus'> {
  if (info.run) return { kind: 'run', name: info.run.name, role: info.role, members: info.run.members.length }
  const s = info.session
  return { kind: 'session', name: s?.name ?? 'session', role: info.role, agentId: s?.agentId, sessionKind: s?.kind, hostedOn: s?.hostName || undefined, sessionStatus: s?.status }
}

/**
 * The folder a joined session's Files pane roots at: the one the session's own welcome names (its machine's word), else the join
 * reply's when that is a whole path. A switchyard lists a published session with its folder's last element alone, which roots
 * nothing: until the welcome comes there is no root.
 */
export function filesRoot(listed: string | undefined, fromOwner: string | undefined): string | undefined {
  if (fromOwner) return fromOwner
  return listed?.startsWith('/') ? listed : undefined
}

/** What a failed look at a link says of it; undefined when it says nothing new (the last status stands). */
export function joinedStatusOf(err: { status: number; code: string }): JoinedStatus | undefined {
  if (err.status === 0) return 'unreachable'
  if (err.status === 404 && (err.code === 'revoked' || err.code === 'expired' || err.code === 'invalid_link' || err.code === 'not_found')) return 'revoked'
  if (err.status === 404 && (err.code === 'session_gone' || err.code === 'run_gone')) return 'gone'
  return undefined
}

/** The join page of an entry. */
export function joinedPath(e: Pick<JoinedEntry, 'token' | 'server'>): string {
  return `/join/${encodeURIComponent(e.token)}${e.server ? `?server=${encodeURIComponent(e.server)}` : ''}`
}

/** Whether the page at `path` is the join page of `token`. */
export function joinedOpen(path: string, token: string): boolean {
  return path === `/join/${encodeURIComponent(token)}`
}

