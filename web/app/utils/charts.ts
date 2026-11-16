import type { RunInfo, SessionInfo } from '~/composables/useSessions'

/**
 * The numbers behind the charts: how long a crew's runs lasted (runLength,
 * which the Crews pages draw as bars) and sessions by attention state for the
 * Yard; the Events page counts its own (sparkBuckets in utils/events.ts).
 * Each is drawn only when it says something: a chart needs at least two
 * points (enough), and the empty state says what will appear.
 */

/** The least points a chart is drawn with. */
export const MIN_POINTS = 2

export function enough(points: number): boolean {
  return points >= MIN_POINTS
}

/** A run's length: to its stop, to its last member's end, or to now while it goes. */
export function runLength(run: Pick<RunInfo, 'startedAt' | 'stoppedAt' | 'members' | 'state'>, now: number): number {
  const start = Date.parse(run.startedAt)
  if (Number.isNaN(start)) return 0
  let end: number | undefined = run.stoppedAt ? Date.parse(run.stoppedAt) : undefined
  if (end === undefined && run.state === 'finished') {
    for (const m of run.members) {
      const t = m.endedAt ? Date.parse(m.endedAt) : NaN
      if (!Number.isNaN(t) && (end === undefined || t > end)) end = t
    }
  }
  return Math.max(0, (end ?? now) - start)
}

export type AttentionSlice = 'needs_input' | 'working' | 'done' | 'idle'

/** Sessions by attention state, for the Yard's attention bar: active sessions only, in a fixed order. */
export function attentionSlices(sessions: readonly SessionInfo[], active: (s: SessionInfo) => boolean): Record<AttentionSlice, number> {
  const out: Record<AttentionSlice, number> = { needs_input: 0, working: 0, done: 0, idle: 0 }
  for (const s of sessions) {
    if (!active(s)) continue
    const st = s.attention?.state
    if (st === 'needs_input' || st === 'working' || st === 'done') out[st]++
    else out.idle++
  }
  return out
}

/** The role colours the charts draw with, read from the theme's variables at runtime (brand.md anchors as the fallback). */
export const ROLE_FALLBACK = {
  primary: '#4a6a5a',
  success: '#3f8f5f',
  warning: '#c98a1b',
  info: '#245d85',
  error: '#a1332f',
  neutral: '#666e67',
} as const

export type RoleColors = Record<keyof typeof ROLE_FALLBACK, string>

/** Reads `--ui-<role>` from the document, falling back to the brand anchors; `--ui-text-muted` stands for neutral. */
export function roleColors(read: (name: string) => string = (n) => (typeof getComputedStyle === 'function' ? getComputedStyle(document.documentElement).getPropertyValue(n) : '')): RoleColors {
  const pick = (name: string, fallback: string) => {
    const v = read(name).trim()
    return v && !v.includes('var(') ? v : fallback
  }
  return {
    primary: pick('--ui-primary', ROLE_FALLBACK.primary),
    success: pick('--ui-success', ROLE_FALLBACK.success),
    warning: pick('--ui-warning', ROLE_FALLBACK.warning),
    info: pick('--ui-info', ROLE_FALLBACK.info),
    error: pick('--ui-error', ROLE_FALLBACK.error),
    neutral: pick('--ui-text-muted', ROLE_FALLBACK.neutral),
  }
}
