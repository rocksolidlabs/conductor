/**
 * Who sizes a session's terminal, as one view sees it (round 14). An owner whose welcome says `sizer` sizes the session by one viewer:
 * the view that holds the size fills its pane and sizes the session; every other full view shows the session's grid scaled to its pane,
 * and a control viewer among them gets "Sized by Jane · 212 × 54" with Fit to my window to take the size. An older owner (no `sizer`)
 * takes every controller's size, as before: every view holds it.
 */
export interface SizerView {
  fit: 'fill' | 'scale' | 'tile'
  /** The view may size the session: not scaled, not read-only, not a view-only connection. */
  mayFit: boolean
  welcomed: boolean
  /** The owner's welcome said `sizer`. */
  sizer: boolean
  /** The viewer that sizes the session now, "" nobody. */
  sizedBy: string
  /** This view's own subscriber id. */
  me: string
  compact: boolean
  ended: boolean
  roster: ReadonlyArray<{ id: string; name?: string }>
  cols: number
  rows: number
}

/** This view holds the session's size. */
export function holdsSize(v: Pick<SizerView, 'sizer' | 'sizedBy' | 'me'>): boolean {
  return !v.sizer || (!!v.me && v.sizedBy === v.me)
}

/** This view sizes the session now: it may, and it holds the size. */
export function sizesSession(v: Pick<SizerView, 'mayFit' | 'sizer' | 'sizedBy' | 'me'>): boolean {
  return v.mayFit && holdsSize(v)
}

/** The pane shows the session's grid scaled: a scale view, or, once welcomed, a full view that does not size the session. */
export function showsScaled(v: Pick<SizerView, 'fit' | 'welcomed' | 'mayFit' | 'sizer' | 'sizedBy' | 'me'>): boolean {
  return v.fit === 'scale' || (v.fit === 'fill' && v.welcomed && !sizesSession(v))
}

/** "Sized by <who> · cols × rows" and Fit to my window: on a full view of a controller that does not hold the size; else null. */
export function sizerChip(v: SizerView): { who: string; cols: number; rows: number } | null {
  if (!v.sizer || !v.welcomed || holdsSize(v) || v.fit !== 'fill' || v.compact || v.ended || !v.mayFit) return null
  const who = v.sizedBy ? v.roster.find((r) => r.id === v.sizedBy)?.name || 'another window' : 'a window that left'
  return { who, cols: v.cols, rows: v.rows }
}

/**
 * Another window sets the session's size and this view cannot take it (a view-only link, a read-only view): one line under the grid
 * says so, where a controller has the chip instead. A full view, welcomed, not compact, not ended.
 */
export function sizedElsewhere(v: SizerView): boolean {
  return v.fit === 'fill' && v.welcomed && !v.mayFit && !v.compact && !v.ended
}

/** The font sizes a scaled pane may draw the session's grid at. */
export const SCALE_FONT = { min: 5, max: 28 } as const

/**
 * The font size a scaled pane draws the session's grid at: `font` (the size drawn now) times `k` (how much larger the pane is than
 * the grid drawn at it), rounded, within SCALE_FONT. A full view that follows another window's size never draws it larger than its
 * own `own` font size (#906): a grid smaller than the pane (a Yard tile's) is shown at the size text has everywhere else, not blown
 * up to fill the pane; a larger one is still drawn smaller to show it whole. A scale view (a view-only tile) fills its pane.
 */
export function scaledFont(font: number, k: number, fit: SizerView['fit'], own: number): number {
  const max = fit === 'fill' ? Math.min(own, SCALE_FONT.max) : SCALE_FONT.max
  return Math.max(SCALE_FONT.min, Math.min(max, Math.round(font * k)))
}
