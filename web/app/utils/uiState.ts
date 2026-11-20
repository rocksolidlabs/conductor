// The app's screens' state kept by the server (docs/plans/sync-seams.md,
// OS-2): three documents, GET and PUT /api/ui/{layout,palette,pins}, each
// with `v` and, as kept, `at` and `by`. The browser's local storage stays
// the cache the page reads first, so it draws at once; the layout here is
// the same settings the storage keys hold, one document. Pure.
import { LAYOUT_KEY, SIDE_SPLIT_KEY, type EditorLayout } from './editorLayout'
import { SPLIT_KEY } from './editorTabs'
import { RECENT_MAX, type Recent } from './palette'
import { SIDEBAR_FOLDS_KEY, SIDEBAR_KEY, SIDEBAR_SIZE_KEY, type SectionKey, type SidebarMode } from './sidebar'
import type { Pin } from './pins'

export type UiName = 'layout' | 'palette' | 'pins'
export const UI_NAMES: readonly UiName[] = ['layout', 'palette', 'pins']
/** A document's bound on the server, in bytes. */
export const UI_DOC_MAX = 64 * 1024
/** How long a change waits before it is written, so a drag writes once. */
export const UI_WRITE_DELAY = 1000

export type RunView = 'grid' | 'graph' | 'timeline' | 'tasks'
export type ProjectView = 'list' | 'board' | 'roadmap'
export type GraphDirection = 'LR' | 'TB'

export interface UiLayout {
  v: 1
  editor?: { layout?: EditorLayout; split?: number; sideSplit?: number }
  sidebar?: { mode?: SidebarMode; width?: number; folds?: Partial<Record<SectionKey, boolean>> }
  crewGraph?: { direction?: GraphDirection; handoffs?: boolean; members?: boolean }
  runView?: RunView
  projectView?: ProjectView
}

export interface UiPalette {
  v: 1
  recents: Recent
}

export interface UiPins {
  v: 1
  pins: Pin[]
}

/** A document as the server keeps it: its content, and when and by which data directory it last changed. */
export type Stamped<T> = T & { at?: string; by?: string }

/** The keys the layout's settings live under in local storage. */
export const CREW_GRAPH_KEY = 'conductor.crewGraph'
export const RUN_VIEW_KEY = 'conductor.runView'
export const PROJECT_VIEW_KEY = 'conductor.dispatchView'

type Read = Pick<Storage, 'getItem'> | null
type Write = Pick<Storage, 'setItem'> | null

function get(storage: Read, key: string): string | null {
  try {
    return storage?.getItem(key) ?? null
  } catch {
    return null
  }
}

function set(storage: Write, key: string, value: string): void {
  try {
    storage?.setItem(key, value)
  } catch {
    // The browser may refuse storage; the server keeps the document.
  }
}

function fractionOf(raw: string | null): number | undefined {
  const v = Number(raw)
  return raw !== null && Number.isFinite(v) && v >= 0 && v <= 1 ? v : undefined
}

function jsonOf(raw: string | null): Record<string, unknown> | undefined {
  try {
    const v = raw ? (JSON.parse(raw) as unknown) : undefined
    return v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : undefined
  } catch {
    return undefined
  }
}

const SECTIONS: readonly SectionKey[] = ['pinned', 'needs', 'finished', 'working', 'shared', 'exited']
const RUN_VIEWS: readonly RunView[] = ['grid', 'graph', 'timeline', 'tasks']
const PROJECT_VIEWS: readonly ProjectView[] = ['list', 'board', 'roadmap']

/** The layout the browser's storage holds: what each setting's key says, a setting not saved left out. */
export function layoutFromStorage(storage: Read): UiLayout {
  const out: UiLayout = { v: 1 }
  const editor: NonNullable<UiLayout['editor']> = {}
  const layout = get(storage, LAYOUT_KEY)
  if (layout === 'side' || layout === 'stacked') editor.layout = layout
  const split = fractionOf(get(storage, SPLIT_KEY))
  if (split !== undefined) editor.split = split
  const sideSplit = fractionOf(get(storage, SIDE_SPLIT_KEY))
  if (sideSplit !== undefined) editor.sideSplit = sideSplit
  if (Object.keys(editor).length) out.editor = editor
  const sidebar: NonNullable<UiLayout['sidebar']> = {}
  const mode = get(storage, SIDEBAR_KEY)
  if (mode === 'full' || mode === 'rail') sidebar.mode = mode
  const width = Number(get(storage, SIDEBAR_SIZE_KEY) || Number.NaN)
  if (Number.isFinite(width) && width > 0 && width <= 100) sidebar.width = width
  const folds = jsonOf(get(storage, SIDEBAR_FOLDS_KEY))
  if (folds) {
    const f: Partial<Record<SectionKey, boolean>> = {}
    for (const k of SECTIONS) if (typeof folds[k] === 'boolean') f[k] = folds[k] as boolean
    if (Object.keys(f).length) sidebar.folds = f
  }
  if (Object.keys(sidebar).length) out.sidebar = sidebar
  const graph = jsonOf(get(storage, CREW_GRAPH_KEY))
  if (graph) {
    const g: NonNullable<UiLayout['crewGraph']> = {}
    if (graph.direction === 'LR' || graph.direction === 'TB') g.direction = graph.direction
    if (typeof graph.handoffs === 'boolean') g.handoffs = graph.handoffs
    if (typeof graph.members === 'boolean') g.members = graph.members
    if (Object.keys(g).length) out.crewGraph = g
  }
  const run = get(storage, RUN_VIEW_KEY)
  if (RUN_VIEWS.includes(run as RunView)) out.runView = run as RunView
  const project = get(storage, PROJECT_VIEW_KEY)
  if (PROJECT_VIEWS.includes(project as ProjectView)) out.projectView = project as ProjectView
  return out
}

/** Writes the layout's settings to their keys, each one the layout holds; a setting it leaves out keeps what the browser has. */
export function layoutToStorage(storage: Write, l: UiLayout): void {
  const e = l.editor
  if (e?.layout) set(storage, LAYOUT_KEY, e.layout)
  if (e?.split !== undefined) set(storage, SPLIT_KEY, String(e.split))
  if (e?.sideSplit !== undefined) set(storage, SIDE_SPLIT_KEY, String(e.sideSplit))
  const sb = l.sidebar
  if (sb?.mode) set(storage, SIDEBAR_KEY, sb.mode)
  if (sb?.width !== undefined) set(storage, SIDEBAR_SIZE_KEY, String(sb.width))
  if (sb?.folds) set(storage, SIDEBAR_FOLDS_KEY, JSON.stringify(sb.folds))
  if (l.crewGraph) set(storage, CREW_GRAPH_KEY, JSON.stringify(l.crewGraph))
  if (l.runView) set(storage, RUN_VIEW_KEY, l.runView)
  if (l.projectView) set(storage, PROJECT_VIEW_KEY, l.projectView)
}

/** The layout with `patch` laid over it, each part merged one level deep. */
export function patchLayout(l: UiLayout, patch: Omit<Partial<UiLayout>, 'v'>): UiLayout {
  const out: UiLayout = { ...l, v: 1 }
  if (patch.editor) out.editor = { ...l.editor, ...patch.editor }
  if (patch.sidebar) out.sidebar = { ...l.sidebar, ...patch.sidebar }
  if (patch.crewGraph) out.crewGraph = { ...l.crewGraph, ...patch.crewGraph }
  if (patch.runView) out.runView = patch.runView
  if (patch.projectView) out.projectView = patch.projectView
  return out
}

/**
 * Two machines' recents as one: each key's greater count and later use, the least lately used dropped past RECENT_MAX. Commutative
 * and idempotent, so two machines writing them reach one value without a loop.
 */
export function mergeRecents(a: Recent, b: Recent): Recent {
  const out: Recent = { ...a }
  for (const [k, u] of Object.entries(b)) {
    const mine = out[k]
    out[k] = mine ? { count: Math.max(mine.count, u.count), last: Math.max(mine.last, u.last) } : { ...u }
  }
  const keys = Object.keys(out)
  if (keys.length > RECENT_MAX) {
    keys.sort((x, y) => out[x]!.last - out[y]!.last || (x < y ? -1 : 1))
    for (const k of keys.slice(0, keys.length - RECENT_MAX)) delete out[k]
  }
  return out
}

/** A document's content: without `at` and `by`, which the server sets. */
export function contentOf<T extends object>(doc: Stamped<T>): T {
  const { at: _at, by: _by, ...rest } = doc as Stamped<T> & Record<string, unknown>
  return rest as T
}

/** A value with its object keys in order, so two of one content encode alike. */
function sorted(v: unknown): unknown {
  if (Array.isArray(v)) return v.map(sorted)
  if (v && typeof v === 'object') return Object.fromEntries(Object.keys(v).sort().map((k) => [k, sorted((v as Record<string, unknown>)[k])]))
  return v
}

/** Whether two documents hold one content (their stamps aside). */
export function sameContent(a: object, b: object): boolean {
  return JSON.stringify(sorted(contentOf(a))) === JSON.stringify(sorted(contentOf(b)))
}

/** Whether a reply is a kept document (it has its stamp): `{}` is none. */
export function isKept(doc: object | null | undefined): boolean {
  return !!doc && typeof (doc as { at?: unknown }).at === 'string'
}
