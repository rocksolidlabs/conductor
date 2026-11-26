import type { RunMember, SessionInfo } from '~/composables/useSessions'
import { isActive } from './attention'
import { sessionRow } from './sidebar'
import { rowPrompt } from './sidebarActions'
import { sessionStatus } from './status'

/**
 * One tile of a run's wall (RunWall): a member in the run's order with its latest session, or a session of the run the last read did
 * not know yet (no member).
 */
export interface RunWallTile {
  name: string
  member?: RunMember
  session?: SessionInfo
}

export interface GridLayout {
  cols: number
  rows: number
}

/**
 * Picks the column count that gives `n` equally sized tiles the largest
 * possible size inside a `width` × `height` box, given a gap between tiles
 * and the tiles' preferred aspect ratio (width / height). Every tile stays
 * visible: the grid never scrolls, tiles shrink as sessions are added.
 */
export function bestGrid(n: number, width: number, height: number, gap = 8, aspect = 1.6): GridLayout {
  if (n <= 0) return { cols: 1, rows: 1 }
  if (width <= 0 || height <= 0) return { cols: Math.ceil(Math.sqrt(n)), rows: Math.ceil(n / Math.ceil(Math.sqrt(n))) }
  let best: GridLayout & { size: number } = { cols: 1, rows: n, size: -1 }
  for (let cols = 1; cols <= n; cols++) {
    const rows = Math.ceil(n / cols)
    const cellW = (width - gap * (cols - 1)) / cols
    const cellH = (height - gap * (rows - 1)) / rows
    const tileW = Math.min(cellW, cellH * aspect)
    if (tileW > best.size + 0.5) best = { cols, rows, size: tileW }
  }
  return { cols: best.cols, rows: best.rows }
}

/**
 * How many cells the item placed last in a grid of `n` items fills: the cells
 * the last row leaves empty, and its own. The run page puts its feed there,
 * so its grid has no empty cell. At least 1, at most a row.
 */
export function lastItemSpan(n: number, layout: GridLayout): number {
  if (n <= 0) return 1
  return Math.min(layout.cols, Math.max(1, layout.cols * layout.rows - n + 1))
}

/**
 * The Yard's chips (the UX review's P10): All, and one chip for each live state of the status table, so a chip filters the grid by one
 * key of sessionStatus (Working is `working` alone, Finished `finished` alone, Running the agents that report no turns and the ones
 * still starting). The grid holds the live sessions alone (isActive): an ended session and a host that is away are on no chip.
 */
export type YardFilter = 'all' | 'needs' | 'working' | 'finished' | 'running'

/** The chips in the order the header draws them: the status table's, with Running last since it comes and goes. */
export const YARD_FILTERS: readonly YardFilter[] = ['all', 'needs', 'working', 'finished', 'running']

const CHIP_LABEL: Record<YardFilter, string> = { all: 'All', needs: 'Needs you', working: 'Working', finished: 'Finished', running: 'Running' }

export type YardCounts = Record<YardFilter, number>

/** The sessions on the chip `filter` names: every live one for All, else the live ones whose sessionStatus key is the chip's. */
export function yardFilter(sessions: readonly SessionInfo[], filter: YardFilter, now: number): SessionInfo[] {
  const live = sessions.filter(isActive)
  return filter === 'all' ? live : live.filter((s) => sessionStatus(s, now).key === filter)
}

/** How many live sessions each chip holds. */
export function yardCounts(sessions: readonly SessionInfo[], now: number): YardCounts {
  const n: YardCounts = { all: 0, needs: 0, working: 0, finished: 0, running: 0 }
  for (const s of sessions) {
    if (!isActive(s)) continue
    n.all++
    const k = sessionStatus(s, now).key
    if (k !== 'exited') n[k]++
  }
  return n
}

export interface YardChip {
  key: YardFilter
  label: string
  count: number
  active: boolean
}

/** The chips the header shows: All, Needs you, Working and Finished always, Running while it holds a session or is the chosen chip. */
export function yardChips(counts: YardCounts, filter: YardFilter): YardChip[] {
  return YARD_FILTERS.filter((k) => k !== 'running' || counts.running > 0 || filter === 'running').map((key) => ({ key, label: CHIP_LABEL[key], count: counts[key], active: key === filter }))
}

/** The chip a link's `?filter=` names, else All: an unknown word, a repeated parameter (an array) and none at all open All. */
export function yardFilterOf(query: unknown): YardFilter {
  return typeof query === 'string' && (YARD_FILTERS as readonly string[]).includes(query) ? (query as YardFilter) : 'all'
}

/** What the Needs you chip counts, said in its tooltip and its accessible name: the sessions on this grid, not the one number. */
export function needsChipWords(n: number): string {
  return `${n} ${n === 1 ? 'session on this grid asks' : 'sessions on this grid ask'} you. Plans, tasks and decisions wait in Needs you.`
}

/**
 * What a tile that asks says at its foot, in place of the host (the UX review's P22): its question as the sidebar's row and the pill
 * read it (rowPrompt), "dr-7 · " before one that mirrors a durable decision; null for a tile that does not ask (a working session, an
 * ended one that still carries its question). The foot cuts it to one line; the whole is its title.
 */
export function tileAsk(s: SessionInfo): string | null {
  const p = rowPrompt(sessionRow(s))
  if (!p) return null
  return p.decision ? `${p.decision} · ${p.message}` : p.message
}
