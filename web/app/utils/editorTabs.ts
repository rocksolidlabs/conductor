/**
 * The editor area's tabs (design 4b, 4c): one tab per file or URL open,
 * the active one shown, Ctrl+Tab and Ctrl+Shift+Tab to switch, × or
 * Ctrl+W to close, the strip's × for all; closing the last tab takes the
 * area away. Folded (T; Alt+T in the terminal) the strip stays as one line
 * above the terminal. The other way, the terminal collapses to a bar at the
 * column's foot under an open editor, only when the person asks (#432). The
 * split with the terminal is a fraction of the column, kept per browser.
 * Pure: the page keeps the state, this moves it.
 */
export type TabKind = 'file' | 'url' | 'diff'

export interface EditorTab {
  /** `file:<path>` or `url:<url>`: one tab per thing open. */
  id: string
  kind: TabKind
  /** The absolute path, or the URL. */
  path: string
  /** The file's name, or the URL's host. */
  title: string
  /** The line to show when the tab was opened at one, and the range's last (a quote's, F7); cleared once shown. */
  line?: number
  lineTo?: number
  /**
   * A diff tab: the file's status and lines, and the revision it is against (HEAD when empty) with its words. A commit's diff (design 4e)
   * also names the commit (`rev`, the right side, in place of the working directory; `short` for the title) and a rename's old path (`from`).
   */
  meta?: { status?: string; added?: number; removed?: number; base?: string; against?: string; rev?: string; short?: string; from?: string }
  /** A Markdown file shown rendered (the editor's Preview toggle, Alt+P, #166) rather than as source; kept per tab. */
  preview?: boolean
  /** The tab's preview takes the focus once it shows (Preview plan, #432); cleared as it is taken. */
  takeFocus?: boolean
}

export interface TabsState {
  tabs: EditorTab[]
  active: string | null
  folded: boolean
  /**
   * The terminal collapsed to a bar under the open editor (#432): set only by the person (the strip's button, the palette's row), and
   * cleared by them, by folding the editor and by closing the last tab. It lasts the page; nothing keeps it per browser.
   */
  collapsed?: boolean
}

export const SPLIT_KEY = 'conductor.editor.split'
/** The editor's share of the column, by default and at its ends. */
export const SPLIT_DEFAULT = 0.6
export const SPLIT_MIN = 0.2
export const SPLIT_MAX = 0.85
/** The terminal keeps at least this many lines under the editor (design 4b). */
export const MIN_TERMINAL_LINES = 6

export function emptyTabs(): TabsState {
  return { tabs: [], active: null, folded: false }
}

export function tabId(kind: TabKind, path: string, rev?: string): string {
  return rev ? `${kind}@${rev}:${path}` : `${kind}:${path}`
}

export function tabTitle(kind: TabKind, path: string): string {
  if (kind === 'url') {
    try {
      return new URL(path).host || path
    } catch {
      return path
    }
  }
  return path.split('/').pop() || path
}

/** Opens a file, a URL or a diff: a tab already open is brought to the front (at the line asked for, its meta refreshed); the area unfolds. */
export function openTab(state: TabsState, kind: TabKind, path: string, line?: number, meta?: EditorTab['meta'], lineTo?: number): TabsState {
  const id = tabId(kind, path, meta?.rev)
  const had = state.tabs.find((t) => t.id === id)
  const title = meta?.rev ? `${meta.short || meta.rev.slice(0, 7)} ${tabTitle(kind, path)}` : tabTitle(kind, path)
  const tabs = had ? state.tabs.map((t) => (t.id === id ? { ...t, line, lineTo, meta: meta ?? t.meta } : t)) : [...state.tabs, { id, kind, path, title, line, lineTo, meta }]
  return { ...state, tabs, active: id, folded: false }
}

/**
 * Opens a file shown rendered (Preview plan, #432): its tab, or the open one brought to the front, with its preview on whatever it
 * showed before and the focus to be taken by it; the area unfolds, the terminal stays as it was, the other tabs keep their choice.
 */
export function openPreview(state: TabsState, path: string): TabsState {
  const s = openTab(state, 'file', path)
  return { ...s, tabs: s.tabs.map((t) => (t.id === s.active ? { ...t, preview: true, takeFocus: true } : t)) }
}

/** The focus a tab was opened to give its preview, taken once. */
export function takeFocus(state: TabsState, id: string): TabsState {
  return { ...state, tabs: state.tabs.map((t) => (t.id === id && t.takeFocus ? { ...t, takeFocus: undefined } : t)) }
}

export function activateTab(state: TabsState, id: string): TabsState {
  if (!state.tabs.some((t) => t.id === id)) return state
  return { ...state, active: id, folded: false }
}

/** Closes a tab; the one after it (else before it) takes its place. */
export function closeTab(state: TabsState, id: string): TabsState {
  const i = state.tabs.findIndex((t) => t.id === id)
  if (i < 0) return state
  const tabs = state.tabs.filter((t) => t.id !== id)
  // The last tab's close empties the area: unfolded, and the terminal's collapse gone with it (#432).
  if (!tabs.length) return emptyTabs()
  let active = state.active
  if (active === id) active = tabs[Math.min(i, tabs.length - 1)]?.id ?? null
  return { ...state, tabs, active }
}

export function closeAllTabs(): TabsState {
  return emptyTabs()
}

/** The tab after the active one (Ctrl+Tab), round the end; `-1` the one before. */
export function cycleTab(state: TabsState, step: 1 | -1 = 1): TabsState {
  if (state.tabs.length < 2) return state
  const i = state.tabs.findIndex((t) => t.id === state.active)
  const next = state.tabs[(i + step + state.tabs.length) % state.tabs.length]!
  return { ...state, active: next.id, folded: false }
}

/** Folds the editor to its strip, or back (T); the terminal's collapse goes with it, the terminal taking the column. */
export function toggleFold(state: TabsState): TabsState {
  if (!state.tabs.length) return state
  return { ...state, folded: !state.folded, collapsed: false }
}

/** Collapses the terminal under the editor, or brings it back (#432): the mirror of the fold. A folded editor comes back to take the column. */
export function toggleCollapse(state: TabsState): TabsState {
  if (!state.tabs.length) return state
  return { ...state, folded: false, collapsed: state.folded || !state.collapsed }
}

/** Shows the tab rendered rather than as source, or back: a Markdown file's Preview toggle (#166). Other tabs keep their choice. */
export function togglePreview(state: TabsState, id: string): TabsState {
  return { ...state, tabs: state.tabs.map((t) => (t.id === id ? { ...t, preview: !t.preview } : t)) }
}

/** The line a tab was opened at, taken once. */
export function takeLine(state: TabsState, id: string): TabsState {
  return { ...state, tabs: state.tabs.map((t) => (t.id === id && t.line !== undefined ? { ...t, line: undefined, lineTo: undefined } : t)) }
}

export function readSplit(storage: Pick<Storage, 'getItem'> | null): number {
  try {
    const v = Number(storage?.getItem(SPLIT_KEY))
    return Number.isFinite(v) && v > 0 ? clampSplit(v) : SPLIT_DEFAULT
  } catch {
    return SPLIT_DEFAULT
  }
}

export function writeSplit(storage: Pick<Storage, 'setItem'> | null, v: number): void {
  try {
    storage?.setItem(SPLIT_KEY, String(clampSplit(v)))
  } catch {
    // The browser may refuse storage; the split then lasts the page.
  }
}

export function clampSplit(v: number): number {
  return Math.min(SPLIT_MAX, Math.max(SPLIT_MIN, v))
}

/**
 * The editor's height in pixels for a column of `total` pixels at `split`,
 * the terminal keeping at least MIN_TERMINAL_LINES of `lineHeight` plus the
 * `reserved` pixels around it (the divider, the reply bar).
 */
export function editorHeight(total: number, split: number, lineHeight: number, reserved: number): number {
  const minTerminal = MIN_TERMINAL_LINES * lineHeight
  const most = Math.max(0, total - reserved - minTerminal)
  return Math.max(0, Math.min(most, Math.round(total * clampSplit(split))))
}

/** The editor's keymap (design round 12, F8): Monaco's own keys, or the real Neovim on the session's machine. Kept per browser. */
export type Keymap = 'default' | 'nvim'
export const KEYMAP_KEY = 'conductor.editor.keymap'

export function readKeymap(storage: Pick<Storage, 'getItem'> | null): Keymap {
  try {
    return storage?.getItem(KEYMAP_KEY) === 'nvim' ? 'nvim' : 'default'
  } catch {
    return 'default'
  }
}

export function writeKeymap(storage: Pick<Storage, 'setItem'> | null, keymap: Keymap): void {
  try {
    storage?.setItem(KEYMAP_KEY, keymap)
  } catch {
    // The browser may refuse storage; the choice then lasts the page.
  }
}

/** What the keymap switch says when Neovim cannot be offered: why, in a few words, or '' when it can. */
export function nvimUnavailableWords(opts: { welcome: boolean; nvim: boolean; fileEdit: boolean; ownerOnly?: boolean; machine?: string }): string {
  if (!opts.welcome) return 'Not connected'
  if (opts.ownerOnly) return "Neovim is kept for the session's own window; edit here with these keys"
  if (!opts.nvim) return `Neovim is not installed on ${opts.machine || 'the machine that runs this session'}`
  if (!opts.fileEdit) return 'View only here: Neovim needs control, on a session that allows editing'
  return ''
}

/**
 * Why a file read whole is not edited here, in a few words, or '' when it may be: the owner takes no save of a file its read marked
 * `readOnly` (one in a repository's .git), so neither Monaco nor Neovim edits it.
 */
export function fileReadOnlyWords(header?: { readOnly?: boolean } | null): string {
  return header?.readOnly ? "Read only here: a repository's .git is not edited from Conductor" : ''
}

/**
 * What the editor says of a file the session refused to this viewer (`file_denied`), from the refusal's message: file viewing off for
 * everyone; a repository's .git or git's settings and credentials, which a view-only link is not given (they often hold the tokens git
 * signs in with); or files kept for controllers on this session.
 */
export function refusedWords(message: string): { title: string; words: string; icon: string } {
  if (/disabled|off/i.test(message)) return { icon: 'i-lucide-eye-off', title: 'File viewing is off for this session', words: 'Nothing is read from its working directory, for anyone. The owner can turn it on in Settings.' }
  if (/credentials/i.test(message)) return { icon: 'i-lucide-key-round', title: "Git's files are for controllers", words: "A repository's .git and git's own settings can hold the tokens git signs in with, so a view-only link does not open them. Ask for control, or for a controller to open it." }
  return { icon: 'i-lucide-lock', title: 'Files are for controllers on this session', words: 'This link is view only. Ask for control, or for a controller to open it.' }
}

/** What a tab's menu closes (#330), as VS Code's: the others, those to the right, those to the left, or all. */
export type CloseScope = 'others' | 'right' | 'left' | 'all'

/** The ids `scope` closes from the tab `id`, in strip order; a tab the strip does not hold closes nothing but `all`. */
export function tabsToClose(state: TabsState, id: string, scope: CloseScope): string[] {
  if (scope === 'all') return state.tabs.map((t) => t.id)
  const i = state.tabs.findIndex((t) => t.id === id)
  if (i < 0) return []
  switch (scope) {
    case 'others':
      return state.tabs.filter((t) => t.id !== id).map((t) => t.id)
    case 'right':
      return state.tabs.slice(i + 1).map((t) => t.id)
    case 'left':
      return state.tabs.slice(0, i).map((t) => t.id)
  }
}
