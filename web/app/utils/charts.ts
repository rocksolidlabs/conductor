import type { RunInfo, SessionInfo } from '~/composables/useSessions'
import { sessionStatus } from './status'

/**
 * The numbers behind the charts: how long a crew's runs lasted (runLength,
 * which the Crews pages draw as bars) and live sessions by status for the
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

/** The Yard's bar's slices: the live states of utils/status.ts, in the table's order. */
export type StatusSlice = 'needs' | 'working' | 'finished' | 'running'
export const STATUS_SLICES: readonly StatusSlice[] = ['needs', 'working', 'finished', 'running']

/** Live sessions by status key (sessionStatus), for the Yard's bar: active sessions only, one that reads exited (a host away) left out. */
export function statusSlices(sessions: readonly SessionInfo[], active: (s: SessionInfo) => boolean): Record<StatusSlice, number> {
  const out: Record<StatusSlice, number> = { needs: 0, working: 0, finished: 0, running: 0 }
  for (const s of sessions) {
    if (!active(s)) continue
    const k = sessionStatus(s).key
    if (k !== 'exited') out[k]++
  }
  return out
}

/** The bar's sentence: "18 sessions: 5 need you, 4 working, 8 finished, 1 running". */
export function statusSlicesLabel(n: Readonly<Record<StatusSlice, number>>): string {
  const total = STATUS_SLICES.reduce((t, k) => t + n[k], 0)
  return `${total} ${total === 1 ? 'session' : 'sessions'}: ${n.needs} ${n.needs === 1 ? 'needs' : 'need'} you, ${n.working} working, ${n.finished} finished, ${n.running} running`
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
