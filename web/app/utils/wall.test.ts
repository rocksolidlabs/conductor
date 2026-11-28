import { describe, expect, it } from 'vitest'
import type { SessionInfo, SessionStatus } from '~/composables/useSessions'
import type { Attention } from './protocol'
import { liveSession } from './attention'
import { needsYou } from './needsYou'
import { sessionRow } from './sidebar'
import { rowPrompt } from './sidebarActions'
import { bestGrid, evenShares, gutterRange, isRunLayout, movedShares, RUN_LAYOUT_LOOK, RUN_LAYOUTS, sharesFor, TILE_GAP, TILE_MIN_HEIGHT, TILE_MIN_WIDTH, tileAsk, wallTracks, yardFilterOf, lastItemSpan, needsChipWords, yardChips, yardCounts, yardFilter, YARD_FILTERS, type YardCounts, gutterAxes } from './wall'

describe('bestGrid', () => {
  it('handles empty and degenerate boxes', () => {
    expect(bestGrid(0, 1000, 800)).toEqual({ cols: 1, rows: 1 })
    expect(bestGrid(4, 0, 0)).toEqual({ cols: 2, rows: 2 })
  })

  it('uses one row while the tiles stay wide enough', () => {
    expect(bestGrid(1, 1600, 900)).toEqual({ cols: 1, rows: 1 })
    expect(bestGrid(2, 1600, 900)).toEqual({ cols: 2, rows: 1 })
  })

  it('prefers bigger tiles with an empty cell over a cramped row', () => {
    // Three tiles: 2×2 gives 796×446 cells, a single row only 528 px wide.
    expect(bestGrid(3, 1600, 900)).toEqual({ cols: 2, rows: 2 })
  })

  it('wraps into rows on a widescreen box', () => {
    expect(bestGrid(4, 1600, 900)).toEqual({ cols: 2, rows: 2 })
    expect(bestGrid(6, 1600, 900)).toEqual({ cols: 3, rows: 2 })
    expect(bestGrid(9, 1600, 900)).toEqual({ cols: 3, rows: 3 })
    expect(bestGrid(12, 1600, 900)).toEqual({ cols: 4, rows: 3 })
    expect(bestGrid(20, 1600, 900)).toEqual({ cols: 5, rows: 4 })
  })

  it('stacks vertically on a portrait box', () => {
    expect(bestGrid(3, 600, 1200)).toEqual({ cols: 1, rows: 3 })
    expect(bestGrid(4, 600, 1200)).toEqual({ cols: 1, rows: 4 })
    expect(bestGrid(8, 600, 1200)).toEqual({ cols: 2, rows: 4 })
  })

  it('grows the tile size monotonically as the box grows', () => {
    // Same count, wider box: never fewer columns.
    expect(bestGrid(6, 1000, 900).cols).toBeLessThanOrEqual(bestGrid(6, 2000, 900).cols)
  })

  it('never leaves a tile outside the grid', () => {
    for (let n = 1; n <= 40; n++) {
      const { cols, rows } = bestGrid(n, 1440, 800)
      expect(cols * rows).toBeGreaterThanOrEqual(n)
      expect(cols).toBeLessThanOrEqual(n)
    }
  })
})

describe('lastItemSpan', () => {
  it('fills the cells the last row leaves', () => {
    expect(lastItemSpan(5, { cols: 3, rows: 2 })).toBe(2)
    expect(lastItemSpan(6, { cols: 3, rows: 2 })).toBe(1)
    expect(lastItemSpan(3, { cols: 2, rows: 2 })).toBe(2)
    expect(lastItemSpan(1, { cols: 1, rows: 1 })).toBe(1)
  })
  it('stays within a row, whatever bestGrid picks', () => {
    for (let n = 1; n <= 13; n++) {
      const g = bestGrid(n, 1600, 900, 12, 1.4)
      const span = lastItemSpan(n, g)
      expect(span).toBeGreaterThanOrEqual(1)
      expect(span).toBeLessThanOrEqual(g.cols)
    }
  })
})

describe("a run's wall of member tiles", () => {
  // The Members view's tile area at 2000 x 810 (no Activity), with Activity's 320 px column beside it, and at 1440 x 900.
  const boxes = [
    [1680, 600],
    [1348, 600],
    [1120, 690],
  ] as const
  it('fits the members alone, the last tile taking the cells its row leaves, for 1, 2, 3, 5, 6 and 12 members', () => {
    for (const [w, h] of boxes) {
      for (const n of [1, 2, 3, 5, 6, 12]) {
        const g = bestGrid(n, w, h, 12, 1.4)
        const span = lastItemSpan(n, g)
        // Every cell is filled once: n - 1 tiles of one cell and the last of `span`.
        expect(n - 1 + span).toBe(g.cols * g.rows)
        expect(span).toBeLessThanOrEqual(g.cols)
      }
    }
  })
  it('puts three members in one row on the wide window, and wraps six into two rows', () => {
    expect(bestGrid(3, 1680, 600, 12, 1.4)).toEqual({ cols: 3, rows: 1 })
    expect(bestGrid(6, 1680, 600, 12, 1.4)).toEqual({ cols: 3, rows: 2 })
    expect(lastItemSpan(5, bestGrid(5, 1680, 600, 12, 1.4))).toBe(2)
  })
})

describe('wallTracks', () => {
  const counts = [1, 2, 3, 5, 6, 12]
  it('Grid: every tile has a cell, the last one fills its row, no tile narrower than its floor', () => {
    for (const [w, h] of [
      [1680, 600],
      [1120, 690],
      [700, 500],
    ] as const) {
      for (const n of counts) {
        const t = wallTracks('grid', n, w, h)
        const cols = Number(/^repeat\((\d+),/.exec(t.columns)![1])
        const rows = Number(/^repeat\((\d+),/.exec(t.rows)![1])
        expect(cols * rows).toBeGreaterThanOrEqual(n)
        expect((w - TILE_GAP * (cols - 1)) / cols).toBeGreaterThanOrEqual(TILE_MIN_WIDTH)
        expect(t.rows).toContain(`minmax(${TILE_MIN_HEIGHT}px, 1fr)`)
        const span = Number(/span (\d+)/.exec(t.cells[n - 1]!.gridColumn ?? 'span 1')![1])
        expect(n - 1 + span).toBe(cols * rows)
        expect(t.large).toBe(-1)
      }
    }
    expect(wallTracks('grid', 3, 1680, 600).columns).toBe('repeat(3, minmax(0, 1fr))')
    expect(wallTracks('grid', 6, 1680, 600).rows).toBe(`repeat(2, minmax(${TILE_MIN_HEIGHT}px, 1fr))`)
  })

  it('One large: the chosen tile in two thirds and the whole height, the others in a column of rows at least 140 px', () => {
    for (const n of counts.filter((x) => x > 1)) {
      const t = wallTracks('large', n, 1680, 600, 1)
      expect(t.columns).toBe('minmax(0, 1.3333fr) minmax(0, 0.6667fr)')
      expect(t.rows).toBe(`repeat(${n - 1}, minmax(${TILE_MIN_HEIGHT}px, 1fr))`)
      expect(t.large).toBe(1)
      expect(t.cells[1]).toEqual({ gridColumn: '1', gridRow: `1 / span ${n - 1}` })
      // The others, in the run's order, one row each in the column.
      const others = t.cells.filter((_, i) => i !== 1)
      expect(others.map((c) => c.gridColumn)).toEqual(Array(n - 1).fill('2'))
      expect(others.map((c) => c.gridRow)).toEqual(Array.from({ length: n - 1 }, (_, i) => String(i + 1)))
    }
    // A large tile that is not there is the first; one member alone is the grid's one tile.
    expect(wallTracks('large', 3, 1680, 600, 7).large).toBe(0)
    expect(wallTracks('large', 1, 1680, 600).large).toBe(-1)
    expect(wallTracks('large', 1, 1680, 600).columns).toBe('repeat(1, minmax(0, 1fr))')
  })

  it('Columns: one row, each tile at least 260 px wide, scrolling sideways; Rows: one column, each at least 140 px tall', () => {
    for (const n of counts) {
      const c = wallTracks('columns', n, 1680, 600)
      expect(c.columns).toBe(`repeat(${n}, minmax(${TILE_MIN_WIDTH}px, 1fr))`)
      expect(c.rows).toBe('minmax(0, 1fr)')
      expect(c.scroll).toBe('x')
      expect(c.cells).toEqual(Array(n).fill({}))
      expect(c.large).toBe(-1)
      const r = wallTracks('rows', n, 1680, 600)
      expect(r.columns).toBe('minmax(0, 1fr)')
      expect(r.rows).toBe(`repeat(${n}, minmax(${TILE_MIN_HEIGHT}px, 1fr))`)
      expect(r.scroll).toBe('y')
      expect(r.cells).toEqual(Array(n).fill({}))
    }
    // Six members at 2000 x 810 in Columns: six of about 272 px fit the 1680 px wall; twelve scroll.
    expect(6 * TILE_MIN_WIDTH + 5 * TILE_GAP).toBeLessThanOrEqual(1680)
    expect(12 * TILE_MIN_WIDTH + 11 * TILE_GAP).toBeGreaterThan(1680)
    expect(wallTracks('grid', 3, 1680, 600).scroll).toBe('y')
    expect(wallTracks('large', 3, 1680, 600).scroll).toBe('y')
  })

  it('draws the shares a person dragged the gaps to, and ignores a record with the wrong count', () => {
    expect(wallTracks('grid', 3, 1680, 600, 0, { cols: [0.5, 0.25, 0.25] }).columns).toBe('minmax(0, 1.5fr) minmax(0, 0.75fr) minmax(0, 0.75fr)')
    expect(wallTracks('grid', 3, 1680, 600, 0, { cols: [0.5, 0.5] }).columns).toBe('repeat(3, minmax(0, 1fr))')
    expect(wallTracks('rows', 2, 1680, 600, 0, { rows: [3, 1] }).rows).toBe(`minmax(${TILE_MIN_HEIGHT}px, 1.5fr) minmax(${TILE_MIN_HEIGHT}px, 0.5fr)`)
    expect(wallTracks('columns', 2, 1680, 600, 0, { cols: [1, -1] }).columns).toBe(`repeat(2, minmax(${TILE_MIN_WIDTH}px, 1fr))`)
    expect(wallTracks('large', 3, 1680, 600, 0, { cols: [0.5, 0.5] }).columns).toBe('minmax(0, 1fr) minmax(0, 1fr)')
    expect(sharesFor([1, 1, 2], 3)).toEqual([0.25, 0.25, 0.5])
    expect(sharesFor([1, 1], 3)).toBeUndefined()
    expect(sharesFor([1, Number.NaN], 2)).toBeUndefined()
    expect(sharesFor(Array(13).fill(1), 13)).toBeUndefined()
  })

  it('moves a gap between two tracks, clamped to the floors, the others kept', () => {
    const px = [400, 400, 400]
    const moved = movedShares(px, 0, 100, TILE_MIN_WIDTH)
    expect(moved.map((s) => s * 1200)).toEqual([500, 300, 400])
    // Past the floor: the second keeps 260 px.
    expect(movedShares(px, 0, 500, TILE_MIN_WIDTH).map((s) => Math.round(s * 1200))).toEqual([540, 260, 400])
    expect(movedShares(px, 1, -500, TILE_MIN_WIDTH).map((s) => Math.round(s * 1200))).toEqual([400, 260, 540])
    expect(gutterRange(px, 0, TILE_MIN_WIDTH)).toEqual({ min: -140, max: 140 })
    // Two tracks below the floors together share what they have, half each at most.
    expect(gutterRange([200, 200], 0, TILE_MIN_WIDTH)).toEqual({ min: 0, max: 0 })
    expect(evenShares([500, 300, 400], 0).map((s) => s * 1200)).toEqual([400, 400, 400])
    expect(TILE_GAP).toBe(12)
  })

  it('names its layouts in the order the switch draws them', () => {
    expect(RUN_LAYOUTS).toEqual(['grid', 'columns', 'rows', 'large'])
    expect(RUN_LAYOUTS.map((l) => RUN_LAYOUT_LOOK[l].label)).toEqual(['Grid', 'Columns', 'Rows', 'One large'])
    expect(isRunLayout('rows')).toBe(true)
    expect(isRunLayout('large')).toBe(true)
    expect(isRunLayout('focus')).toBe(false)
  })
})

// The Yard's chips over a table of sessions: one in each live state, a starting one, an ended session that still carries its question,
// a host that is away and asks, a question answered in the terminal, and a question that mirrors a durable decision.
const NOW = Date.UTC(2026, 10, 16, 10, 0)
const AGO = (m: number) => new Date(NOW - m * 60_000).toISOString()

function tile(id: string, status: SessionStatus, attention?: Attention, turns = true): SessionInfo {
  return { id, name: id, kind: 'server', agentId: 'claude', command: [], cwd: '/w', status, cols: 80, rows: 24, viewers: 0, turns, ...(attention ? { attention } : {}) } as unknown as SessionInfo
}

const ask = (id: string, o: Partial<Attention> = {}): Attention => ({ state: 'needs_input', source: 'hook', id: `q-${id}`, since: AGO(3), message: 'Run the tests again?', ...o }) as Attention

const TABLE: SessionInfo[] = [
  tile('asks', 'running', ask('asks')),
  tile('mirror', 'running', ask('mirror', { decision: 'dr-7' })),
  tile('works', 'running', { state: 'working', source: 'hook', since: AGO(1) } as Attention),
  tile('done', 'running', { state: 'done', source: 'hook', since: AGO(28) } as Attention),
  tile('silent', 'running', undefined, false),
  tile('starting', 'starting', undefined, false),
  tile('ended', 'exited', ask('ended')),
  tile('away', 'host_disconnected', ask('away')),
  tile('answered', 'running', ask('answered', { answered: 'terminal' })),
]
const ids = (ss: readonly SessionInfo[]) => ss.map((s) => s.id).sort()

describe("the Yard's chips", () => {
  it('filters the grid by one state each: Working is working alone, Finished finished alone, Running the silent and the starting', () => {
    const live = TABLE.map(liveSession)
    expect(ids(yardFilter(live, 'all', NOW))).toEqual(['answered', 'asks', 'done', 'mirror', 'silent', 'starting', 'works'])
    expect(ids(yardFilter(live, 'needs', NOW))).toEqual(['asks', 'mirror'])
    expect(ids(yardFilter(live, 'working', NOW))).toEqual(['answered', 'works'])
    expect(ids(yardFilter(live, 'finished', NOW))).toEqual(['done'])
    expect(ids(yardFilter(live, 'running', NOW))).toEqual(['silent', 'starting'])
    // The ended and the away session are on no chip.
    for (const f of YARD_FILTERS) expect(ids(yardFilter(live, f, NOW)), f).not.toContain('ended')
    for (const f of YARD_FILTERS) expect(ids(yardFilter(live, f, NOW)), f).not.toContain('away')
    expect(yardCounts(live, NOW)).toEqual({ all: 7, needs: 2, working: 2, finished: 1, running: 2 })
  })

  it("counts on Needs you only sessions that the one list holds, as a question or as the record its question mirrors", () => {
    const live = TABLE.map(liveSession)
    const mirrored = { id: 'dr-7', run: 'r-1', openedIn: 'r-1', origin: 'plan', type: 'approval', gate: 'g', request: {}, policy: {}, state: 'open', rev: 1, createdAt: AGO(5) }
    const list = needsYou({
      sessions: { loaded: true, error: '', list: live },
      decisions: { loaded: true, error: '', records: [mirrored] as never, cut: false },
      inbox: { loaded: true, error: '', items: [], next: '', unavailable: '' },
    })
    const listed = new Set(list.items.map((it) => it.session?.id).filter(Boolean))
    const counted = yardFilter(live, 'needs', NOW)
    expect(counted.length).toBe(yardCounts(live, NOW).needs)
    expect(counted.filter((s) => !listed.has(s.id)).map((s) => s.id)).toEqual([])
    // The mirrored question is the record's one item, and the session that asks is still counted on the chip.
    expect(list.items.find((it) => it.session?.id === 'mirror')?.decision?.id).toBe('dr-7')
  })

  it('shows the four chips always and Running only while it holds a session or is chosen', () => {
    const none: YardCounts = { all: 3, needs: 1, working: 1, finished: 1, running: 0 }
    expect(yardChips(none, 'all').map((c) => c.key)).toEqual(['all', 'needs', 'working', 'finished'])
    expect(yardChips(none, 'running').map((c) => c.key)).toEqual(['all', 'needs', 'working', 'finished', 'running'])
    expect(yardChips({ ...none, running: 2 }, 'all').map((c) => [c.key, c.label, c.count, c.active])).toEqual([
      ['all', 'All', 3, true],
      ['needs', 'Needs you', 1, false],
      ['working', 'Working', 1, false],
      ['finished', 'Finished', 1, false],
      ['running', 'Running', 2, false],
    ])
  })

  it("opens the chip a link's ?filter= names, else All", () => {
    for (const f of YARD_FILTERS) expect(yardFilterOf(f)).toBe(f)
    expect(yardFilterOf('x')).toBe('all')
    expect(yardFilterOf(['working', 'finished'])).toBe('all')
    expect(yardFilterOf(undefined)).toBe('all')
    expect(yardFilterOf(null)).toBe('all')
  })

  it('says that the Needs you chip counts the sessions on this grid alone', () => {
    expect(needsChipWords(5)).toBe('5 sessions on this grid ask you. Plans, tasks and decisions wait in Needs you.')
    expect(needsChipWords(1)).toBe('1 session on this grid asks you. Plans, tasks and decisions wait in Needs you.')
  })
})

describe("an asking tile's words", () => {
  it('says the question as the sidebar row does, "dr-7 · " before a mirrored one, and nothing for a tile that does not ask', () => {
    const live = new Map(TABLE.map(liveSession).map((s) => [s.id, s]))
    expect(tileAsk(live.get('asks')!)).toBe('Run the tests again?')
    expect(tileAsk(live.get('mirror')!)).toBe('dr-7 · Run the tests again?')
    expect(tileAsk(live.get('works')!)).toBeNull()
    expect(tileAsk(live.get('ended')!)).toBeNull()
    expect(tileAsk(live.get('answered')!)).toBeNull()
    // The words the row's prompt shows, never a copy made apart: a question with no words says what the row says.
    const blank = liveSession(tile('blank', 'running', ask('blank', { message: '' })))
    expect(tileAsk(blank)).toBe(rowPrompt(sessionRow(blank))!.message)
  })
})

describe('gutterAxes', () => {
  it("draws a layout's own gaps, and none on an axis of more tracks than a size is kept for", () => {
    expect(gutterAxes('grid', 3, 2)).toEqual(['cols', 'rows'])
    expect(gutterAxes('columns', 12, 1)).toEqual(['cols'])
    expect(gutterAxes('columns', 13, 1)).toEqual([])
    expect(gutterAxes('rows', 1, 13)).toEqual([])
    expect(gutterAxes('grid', 1, 13)).toEqual([])
    expect(gutterAxes('large', 2, 6)).toEqual(['cols'])
  })
})
