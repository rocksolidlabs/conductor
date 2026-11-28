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

/** The ways a run's member tiles are laid out on its Members view, in the order its switch draws them. */
export type RunLayout = 'grid' | 'columns' | 'rows' | 'large'
export const RUN_LAYOUTS: readonly RunLayout[] = ['grid', 'columns', 'rows', 'large']
/** Each layout's name (the switch's tooltip, the palette's row) and icon. */
export const RUN_LAYOUT_LOOK: Record<RunLayout, { label: string; icon: string }> = {
  grid: { label: 'Grid', icon: 'i-lucide-layout-grid' },
  columns: { label: 'Columns', icon: 'i-lucide-columns-3' },
  rows: { label: 'Rows', icon: 'i-lucide-rows-3' },
  large: { label: 'One large', icon: 'i-lucide-layout-panel-left' },
}
export function isRunLayout(v: unknown): v is RunLayout {
  return typeof v === 'string' && (RUN_LAYOUTS as readonly string[]).includes(v)
}
/** A tile on a run's wall is never narrower or lower than these: past them the tiles scroll. */
export const TILE_MIN_WIDTH = 260
export const TILE_MIN_HEIGHT = 140
/** The gap between two tiles, in px (gap-3). */
export const TILE_GAP = 12

/** A tile's cell on the wall (its grid-column and grid-row); empty where the grid places it. */
export type WallCell = { gridColumn?: string; gridRow?: string }

/** The wall's tracks and each tile's cell, in the run's order. */
export interface WallTracks {
  columns: string
  rows: string
  cells: WallCell[]
  /** The tile drawn large (One large), which stays in view while the column beside it scrolls; -1 for none. */
  large: number
  /** The way the wall scrolls past its floors: down, or sideways (Columns). */
  scroll: 'y' | 'x'
}

/**
 * The tracks of a run's wall of `n` tiles in a box `w` x `h`. Grid: the columns and rows that give the tiles the most room
 * (bestGrid), no more columns than TILE_MIN_WIDTH allows, each row at least TILE_MIN_HEIGHT, the last tile taking the cells its row
 * leaves. One large: the tile `large` (the first when it is out of range) in two thirds of the width and the whole height, the
 * others in a column at the right, each at least TILE_MIN_HEIGHT tall, the column scrolling. Columns: one row, each tile at least
 * TILE_MIN_WIDTH wide, the row scrolling sideways past that. Rows: one column, each tile at least TILE_MIN_HEIGHT tall, the wall
 * scrolling down. `sizes`, the shares a person dragged the gaps to, apply where they hold as many tracks as the layout draws. The
 * tiles keep their order on the page and only their cells change, so a change of layout never draws a tile again.
 */
export function wallTracks(layout: RunLayout, n: number, w: number, h: number, large = 0, sizes?: WallShares): WallTracks {
  const none = Array.from({ length: n }, (): WallCell => ({}))
  const count = Math.max(1, n)
  if (layout === 'columns') return { columns: tracksOf(count, `${TILE_MIN_WIDTH}px`, sizes?.cols), rows: 'minmax(0, 1fr)', cells: none, large: -1, scroll: 'x' }
  if (layout === 'rows') return { columns: 'minmax(0, 1fr)', rows: tracksOf(count, `${TILE_MIN_HEIGHT}px`, sizes?.rows), cells: none, large: -1, scroll: 'y' }
  if (layout === 'large' && n > 1) {
    const big = large >= 0 && large < n ? large : 0
    const others = n - 1
    let k = 0
    const cells = Array.from({ length: n }, (_, i): WallCell => (i === big ? { gridColumn: '1', gridRow: `1 / span ${others}` } : { gridColumn: '2', gridRow: String(++k) }))
    return { columns: tracksOf(2, '0', sharesFor(sizes?.cols, 2) ?? [2 / 3, 1 / 3]), rows: `repeat(${others}, minmax(${TILE_MIN_HEIGHT}px, 1fr))`, cells, large: big, scroll: 'y' }
  }
  const best = bestGrid(n, w, h, TILE_GAP, 1.4)
  const most = w > 0 ? Math.max(1, Math.floor((w + TILE_GAP) / (TILE_MIN_WIDTH + TILE_GAP))) : best.cols
  const cols = Math.min(best.cols, most)
  const g = { cols, rows: Math.max(1, Math.ceil(n / cols)) }
  const span = lastItemSpan(n, g)
  const cells = Array.from({ length: n }, (_, i): WallCell => (i === n - 1 && span > 1 ? { gridColumn: `span ${span} / span ${span}` } : {}))
  return { columns: tracksOf(g.cols, '0', sizes?.cols), rows: tracksOf(g.rows, `${TILE_MIN_HEIGHT}px`, sizes?.rows), cells, large: -1, scroll: 'y' }
}

/**
 * The sizes a person gave a wall's tracks by dragging the gaps between them (RunTileGutter), as shares of the axis: kept for each
 * crew and layout, and dropped when the number of tracks changes (sharesFor).
 */
export interface WallShares {
  cols?: number[]
  rows?: number[]
}
/** The most tracks an axis keeps shares for. */
export const SHARES_MAX = 12

/**
 * The axes of a layout whose gaps a person drags: a layout's own (Grid both, Columns and One large across, Rows down), each only with 2
 * to SHARES_MAX tracks. An axis of more tracks than a size is kept for (the layout document's bound) has no gap: its tracks stay even.
 */
export function gutterAxes(layout: RunLayout, cols: number, rows: number): Array<'cols' | 'rows'> {
  const own: Record<RunLayout, Array<'cols' | 'rows'>> = { grid: ['cols', 'rows'], columns: ['cols'], rows: ['rows'], large: ['cols'] }
  return own[layout].filter((axis) => {
    const n = axis === 'cols' ? cols : rows
    return n >= 2 && n <= SHARES_MAX
  })
}

/** `shares` when they are `n` positive shares (normalised to a sum of 1), else undefined: a record with the wrong count is ignored. */
export function sharesFor(shares: readonly number[] | undefined, n: number): number[] | undefined {
  if (!shares || n < 2 || shares.length !== n || n > SHARES_MAX || !shares.every((s) => Number.isFinite(s) && s > 0)) return undefined
  const sum = shares.reduce((a, b) => a + b, 0)
  return shares.map((s) => s / sum)
}

/** `n` tracks of at least `min`, even, or in the given shares (as flex factors that sum to n, so that they fill the axis). */
function tracksOf(n: number, min: string, shares?: readonly number[]): string {
  const s = sharesFor(shares, n)
  if (!s) return `repeat(${n}, minmax(${min}, 1fr))`
  return s.map((x) => `minmax(${min}, ${+(x * n).toFixed(4)}fr)`).join(' ')
}

/**
 * The shares of an axis after the gap between tracks `k` and `k + 1` moved by `delta` px, from the tracks' drawn sizes `px`: the two
 * tracks share what they had, neither below `floor` (as far as they can), and the others keep their sizes.
 */
export function movedShares(px: readonly number[], k: number, delta: number, floor: number): number[] {
  const out = [...px]
  const pair = out[k]! + out[k + 1]!
  const lo = Math.min(floor, pair / 2)
  const a = Math.min(pair - lo, Math.max(lo, out[k]! + delta))
  out[k] = a
  out[k + 1] = pair - a
  const sum = out.reduce((x, y) => x + y, 0)
  return out.map((x) => x / sum)
}

/** How far the gap after track `k` may move each way, in px, keeping both tracks at `floor` (as far as they can). */
export function gutterRange(px: readonly number[], k: number, floor: number): { min: number; max: number } {
  const pair = px[k]! + px[k + 1]!
  const lo = Math.min(floor, pair / 2)
  return { min: Math.min(0, lo - px[k]!), max: Math.max(0, pair - lo - px[k]!) }
}

/** The shares of an axis with tracks `k` and `k + 1` made even, the others kept. */
export function evenShares(px: readonly number[], k: number): number[] {
  const out = [...px]
  const half = (out[k]! + out[k + 1]!) / 2
  out[k] = half
  out[k + 1] = half
  const sum = out.reduce((x, y) => x + y, 0)
  return out.map((x) => x / sum)
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
