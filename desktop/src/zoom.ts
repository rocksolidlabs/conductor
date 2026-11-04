/**
 * The workbench's zoom. The keys are taken before the page sees them (webContents' before-input-event): on Windows and Linux a
 * page sees a key before the menu does, and the terminal swallows Ctrl+- (a control character), while the menu roles' own
 * accelerator, Ctrl+Plus, needs Shift on most layouts, so plain Ctrl+= did nothing. The level is the app's (kept in its settings),
 * so it outlives the server's port changing between starts.
 */

/** The bounds and step of the zoom level (Chromium's: each level is a factor of 1.2). */
export const ZOOM_MIN = -3
export const ZOOM_MAX = 5
export const ZOOM_STEP = 0.5

export type ZoomMove = 'in' | 'out' | 'reset'

/** What before-input-event hands over of a key (Electron's Input), as much as the zoom reads. */
export interface ZoomInput {
  type: string
  key: string
  code: string
  control: boolean
  meta: boolean
  alt: boolean
}

/**
 * zoomKey is the zoom move a key asks for, or null: with Ctrl (Cmd on macOS, where Ctrl stays the terminal's) and no Alt,
 * = or + (Shift or not, the numpad's +) zooms in, - (or the numpad's) out, 0 (or the numpad's) back to 100%.
 */
export function zoomKey(i: ZoomInput, platform: NodeJS.Platform): ZoomMove | null {
  if (i.type !== 'keyDown' || i.alt) return null
  if (!(platform === 'darwin' ? i.meta : i.control)) return null
  if (i.key === '=' || i.key === '+' || i.code === 'NumpadAdd' || i.code === 'Equal') return 'in'
  if (i.key === '-' || i.key === '_' || i.code === 'NumpadSubtract' || i.code === 'Minus') return 'out'
  if (i.key === '0' || i.code === 'Numpad0' || i.code === 'Digit0') return 'reset'
  return null
}

/** clampZoom bounds a level, a non-number being 0 (100%). */
export function clampZoom(level: unknown): number {
  if (typeof level !== 'number' || !Number.isFinite(level)) return 0
  return Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, Math.round(level / ZOOM_STEP) * ZOOM_STEP))
}

/** nextZoom is the level after a move. */
export function nextZoom(level: number, move: ZoomMove): number {
  if (move === 'reset') return 0
  return clampZoom(level + (move === 'in' ? ZOOM_STEP : -ZOOM_STEP))
}

/** zoomPercent is a level as the percent it reads (Chromium's factor, 1.2 to the level): the one formula, which the menu and the page both show. */
export function zoomPercent(level: number): number {
  return Math.round(100 * 1.2 ** level)
}

/** What the workbench is told of the zoom: the level, its percent, and whether it is the largest or the smallest ('' between). */
export interface ZoomStatus {
  level: number
  percent: number
  bound: 'largest' | 'smallest' | ''
}

/** zoomStatus is a level as the workbench is told it. */
export function zoomStatus(level: number): ZoomStatus {
  return { level, percent: zoomPercent(level), bound: level >= ZOOM_MAX ? 'largest' : level <= ZOOM_MIN ? 'smallest' : '' }
}

/** What a zoom move needs of the app: its level and where it is kept, the windows it zooms, the workbenches it tells, the menu. */
export interface ZoomMoveDeps {
  level: () => number
  /** Keeps a level that changed (a move at a bound changes nothing, and nothing is saved). */
  save: (level: number) => void
  /** Applies the level now kept to every window of the server's origin. */
  apply: () => void
  /** The workbench windows' pages, each told the move (as conductor:update is told). */
  workbenches: () => Array<{ send: (channel: string, status: ZoomStatus) => void }>
  /** Builds the menu again for the level it shows. */
  menu: (level: number) => void
}

/**
 * moveZoom is a move by key or menu: the level after it kept (when it changed), applied, told to every workbench window and shown
 * in the View menu. A move at a bound that changes nothing is still told and shown, so the key that did nothing is explained.
 */
export function moveZoom(move: ZoomMove, d: ZoomMoveDeps): ZoomStatus {
  const level = nextZoom(d.level(), move)
  if (level !== d.level()) d.save(level)
  d.apply()
  const status = zoomStatus(level)
  for (const wc of d.workbenches()) wc.send('conductor:zoom', status)
  d.menu(level)
  return status
}

/**
 * ZoomStart says a level kept from before once per start of the app: the first workbench page to ask, when the level is not 100%,
 * is told to say it (announce); every later ask is not. A page load is not a start. A move does not take the saying away: one made
 * before any page has asked (the splash still showing) reached no page that listens, since a page listens before it asks, so the
 * first page still says the level; a page told a move before its ask's answer keeps the move it showed (useDesktopZoom).
 */
export class ZoomStart {
  private said = false

  /** The status of level, and whether this ask is the one that says it. */
  ask(level: number): ZoomStatus & { announce: boolean } {
    const announce = !this.said && level !== 0
    this.said = true
    return { ...zoomStatus(level), announce }
  }
}
