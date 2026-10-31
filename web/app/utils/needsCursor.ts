import type { SessionInfo } from '~/composables/useSessions'
import type { AttentionOption } from './protocol'

/**
 * `]` and `[` (#303): a cursor through the sessions that need you, in the
 * order `needingInput` lists them, starting from the page's own session
 * when it is one of them. Pure.
 */

/** The index the cursor moves to: from `from` (the page's session, or the last shown) one step `delta`, wrapping; -1 with none waiting. */
export function nextNeeding(ids: readonly string[], from: string | null, delta: 1 | -1): number {
  if (!ids.length) return -1
  const i = from ? ids.indexOf(from) : -1
  if (i < 0) return delta > 0 ? 0 : ids.length - 1
  return (i + delta + ids.length) % ids.length
}

/** How long the pill shows after a press, in milliseconds. */
export const PILL_MS = 4000
/** The longest delay setTimeout keeps: one longer fires at once. */
const TIMER_MAX_MS = 2_147_483_647

/**
 * How long a press shows the pill: PILL_MS, or what the page was given as `conductorPillMs` (a positive number on the window,
 * set by an e2e spec's init script). Four seconds is the product's bound, not a spec's: a spec that reads the pill and then
 * presses again holds it for its own time, so a loaded machine cannot let it go in between and start the cursor over (#281).
 */
export function pillMs(scope: { conductorPillMs?: unknown } = globalThis as { conductorPillMs?: unknown }): number {
  const ms = scope.conductorPillMs
  return typeof ms === 'number' && Number.isFinite(ms) && ms > 0 ? Math.min(ms, TIMER_MAX_MS) : PILL_MS
}

/**
 * Answer all (#303): the same question (message and choices) asked by
 * several live sessions, grouped; each group names its sessions in the
 * order given. A question with no choices (a free line) is not grouped.
 */
export interface SameQuestion {
  key: string
  message: string
  choices: AttentionOption[]
  sessions: SessionInfo[]
}

export function sameQuestions(sessions: readonly SessionInfo[]): SameQuestion[] {
  const groups = new Map<string, SameQuestion>()
  for (const s of sessions) {
    const a = s.attention
    if (!a || a.state !== 'needs_input' || !a.options?.length || a.answered) continue
    const choices = a.options
    const key = `${a.message ?? ''}\u0000${choices.map((o) => `${o.label}\u0001${o.input}`).join('\u0002')}`
    const g = groups.get(key)
    if (g) g.sessions.push(s)
    else groups.set(key, { key, message: a.message ?? '', choices, sessions: [s] })
  }
  return [...groups.values()].filter((g) => g.sessions.length > 1)
}
