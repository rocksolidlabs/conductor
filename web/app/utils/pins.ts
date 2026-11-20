// Pinned sessions (docs/plans/sync-seams.md, OS-2): a session row's menu
// pins it to the top of the sidebar, where pinned sessions are the first
// group. A pin keeps the session's id, and its name and agent and when it
// was pinned, so a session no longer listed still shows (greyed) until a
// week past its pinning, when it is let go. At most PINS_MAX, the newest
// first. Pure.
import type { SessionInfo } from '~/composables/useSessions'

export interface Pin {
  session: string
  name: string
  agent: string
  /** Milliseconds since the epoch: when it was pinned. */
  at: number
}

export const PINS_MAX = 20
/** A pin whose session is no longer listed is let go this long after it was pinned. */
export const PIN_GONE_MS = 7 * 86_400_000
/** The browser's copy of the pins, the cache the page reads first (useUiState). */
export const PINS_KEY = 'conductor.pins'

/** `pins` with the session pinned first; already pinned, as it is. Past PINS_MAX the oldest pin goes. */
export function pinSession(pins: readonly Pin[], s: Pick<SessionInfo, 'id' | 'name' | 'agentId'>, now = Date.now()): Pin[] {
  if (pins.some((p) => p.session === s.id)) return [...pins]
  return [{ session: s.id, name: s.name, agent: s.agentId ?? '', at: now }, ...pins].slice(0, PINS_MAX)
}

/** `pins` without the session's pin. */
export function unpinSession(pins: readonly Pin[], id: string): Pin[] {
  return pins.filter((p) => p.session !== id)
}

export function isPinned(pins: readonly Pin[], id: string): boolean {
  return pins.some((p) => p.session === id)
}

/** A pin as the sidebar draws it: its session when listed, else greyed. */
export interface PinRow {
  pin: Pin
  session?: SessionInfo
  gone: boolean
}

/**
 * The pins as rows, in the pins' order, and the pins kept: one whose session is no longer listed is drawn greyed until PIN_GONE_MS
 * past its pinning, then let go (`kept` leaves it out, for the caller to save).
 */
export function pinRows(pins: readonly Pin[], sessions: readonly SessionInfo[], now = Date.now()): { rows: PinRow[]; kept: Pin[] } {
  const byId = new Map(sessions.map((s) => [s.id, s]))
  const rows: PinRow[] = []
  const kept: Pin[] = []
  for (const pin of pins) {
    const session = byId.get(pin.session)
    if (!session && now - pin.at > PIN_GONE_MS) continue
    kept.push(pin)
    rows.push({ pin, session, gone: !session })
  }
  return { rows, kept }
}

/** Whether `v` is a pin of the document's shape. */
export function isPin(v: unknown): v is Pin {
  if (!v || typeof v !== 'object') return false
  const p = v as Record<string, unknown>
  return typeof p.session === 'string' && p.session !== '' && typeof p.name === 'string' && typeof p.agent === 'string' && typeof p.at === 'number'
}

/** The pins the browser keeps; anything unreadable is none. */
export function readPins(storage: Pick<Storage, 'getItem'> | null): Pin[] {
  try {
    const raw = storage?.getItem(PINS_KEY)
    if (!raw) return []
    const v = JSON.parse(raw) as unknown
    return Array.isArray(v) ? v.filter(isPin).slice(0, PINS_MAX) : []
  } catch {
    return []
  }
}

export function writePins(storage: Pick<Storage, 'setItem'> | null, pins: readonly Pin[]): void {
  try {
    storage?.setItem(PINS_KEY, JSON.stringify(pins))
  } catch {
    // The browser may refuse storage; the server keeps them.
  }
}
