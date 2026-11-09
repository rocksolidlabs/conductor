import { detectSystem, rowsFor, systemNotes, type Chord, type System } from '../utils/chords'

export interface ShortcutRow {
  /** The action whose chord differs by system (utils/chords.ts, #312): the modal shows the chord of the system the page runs on. */
  chord?: Chord
  /** Keys as UKbd values ('meta', 'shift', 'escape', 'arrowleft', single characters…) outside a terminal: when no terminal or text field has the focus. */
  keys: string[]
  label: string
  /** The same action while typing into an agent: the Alt twin the terminal hands back to the page; none when the action needs the list or the page focused. */
  terminal?: string[]
}

export interface ShortcutGroup {
  title: string
  rows: ShortcutRow[]
}

/**
 * Key codes that a focused terminal hands back to the page when Alt is held,
 * so the Alt variants below work while typing into an agent. Plain keys keep
 * going to the agent; xterm ignores only these Alt chords.
 */
export const ALT_PASSTHROUGH_CODES = new Set(['KeyN', 'KeyS', 'KeyY', 'KeyR', 'KeyA', 'KeyE', 'KeyC', 'KeyD', 'KeyB', 'KeyH', 'KeyF', 'KeyP', 'KeyJ', 'KeyK', 'KeyT', 'KeyX', 'Slash', 'BracketRight', 'BracketLeft', 'Comma', 'ArrowLeft', 'ArrowRight', 'Escape'])

/** Shortcuts that work on every page. Registered in the default layout. */
export const GLOBAL_SHORTCUTS: ShortcutGroup = {
  title: 'Everywhere',
  rows: [
    { keys: ['meta', 'K'], label: 'The command palette: find a session, run, crew, file or action and act on it (/ too)', terminal: ['alt', '/'] },
    { keys: ['meta', 'B'], label: 'Collapse the sidebar to the rail, or expand it', terminal: ['alt', 'B'] },
    { keys: ['?'], label: 'Keyboard shortcuts', terminal: ['alt', 'H'] },
    { keys: ['N'], label: 'Launch an agent', terminal: ['alt', 'N'] },
    { keys: ['G', 'N'], label: 'Go to Needs your decision' },
    { keys: ['G', 'Y'], label: 'Go to the Yard', terminal: ['alt', 'Y'] },
    { keys: ['G', 'R'], label: 'Go to the Roundhouse', terminal: ['alt', 'R'] },
    { keys: ['G', 'C'], label: 'Go to Crews', terminal: ['alt', 'C'] },
    { keys: ['G', 'D'], label: 'Go to Dispatch', terminal: ['alt', 'D'] },
    { keys: ['G', 'A'], label: 'Go to Agents', terminal: ['alt', 'A'] },
    { keys: ['G', 'E'], label: 'Go to Events', terminal: ['alt', 'E'] },
    // No terminal twin: Alt+H is the shortcuts' own (chords.ts), so History has the plain chord alone.
    { keys: ['G', 'H'], label: 'Go to History' },
    { keys: ['F'], label: 'Toggle fullscreen', terminal: ['alt', 'F'] },
    { keys: [']'], label: 'The next session that needs you, in a pill with its choices: 1–9 answer, Enter opens, Escape dismisses', terminal: ['alt', ']'] },
    { keys: ['['], label: 'The one before', terminal: ['alt', '['] },
    { keys: ['G', 'S'], label: 'Go to Settings (the desktop app)', terminal: ['alt', ','] },
  ],
}

/** The command palette's keys (#302): in its input, then on the focused row, where the sidebar's letters work the same. */
export const PALETTE_SHORTCUTS: ShortcutGroup = {
  title: 'The command palette',
  rows: [
    { keys: ['arrowdown'], label: 'From the input, into the list (↑ from the first row goes back)' },
    { keys: ['enter'], label: 'In the input: the first row. On a row: open it, run it, or ask first (Stop, Restart)' },
    { keys: ['1'], label: "1–9 answer the focused row's prompt; only while a row has the focus" },
    { keys: ['J'], label: 'Next row (or ↓); K the one before (or ↑)' },
    { keys: ['arrowright'], label: "The row's actions; ← folds them" },
    { keys: ['S'], label: 'Share the focused session or run; Shift+S copies a view link at once' },
    { keys: ['X'], label: 'Stop it: the row asks; Enter stops, Escape cancels' },
    { keys: ['R'], label: "Open a member's run, or focus a free-text prompt's reply field" },
    { keys: ['meta', 'enter'], label: 'Open in a new tab; Shift+Enter shows it in the Yard' },
    { keys: ['escape'], label: 'From a row, back to the input; from the input, close' },
    { keys: ['>'], label: '> actions · # runs and crews · : files · @ people · ! events · ? shortcuts · ~ history, typed first' },
  ],
}

/** The sidebar's keys (design 3c): in the filter, then on the focused row. Registered by the default layout, after Everywhere. */
export const SIDEBAR_SHORTCUTS: ShortcutGroup = {
  title: 'The sidebar',
  rows: [
    { keys: ['/'], label: 'Open the command palette (the filter box above the list is a plain filter)', terminal: ['alt', 'S'] },
    { keys: ['arrowdown'], label: 'From the filter, into the list' },
    { keys: ['J'], label: 'Next row (or ↓)' },
    { keys: ['K'], label: 'Previous row (or ↑)' },
    { keys: ['enter'], label: 'Open the session, or the run from its header' },
    { keys: ['1'], label: '1–9 answer the focused row\'s prompt, as on its page' },
    { keys: ['R'], label: 'Open the run of the focused member' },
    { keys: ['S'], label: 'Share the focused session or run' },
    { keys: ['X'], label: 'Stop it: the row asks; Enter stops, Escape cancels' },
    { keys: ['escape'], label: 'Leave the list' },
  ],
}

/**
 * Dispatch's keys (Owner decisions, 10: Linear's letters, plain keys that pause in text fields and terminals as every page key does),
 * registered by the Dispatch page that takes them: a project's list moves a roving focus over its tickets, X selects, C makes a ticket,
 * and S P A L M O pick a field of the tickets selected, or of the focused one, a ticket or a card; a card of the board or the roadmap
 * moves by ← →; Escape on a ticket's page goes back to the list.
 */
export const DISPATCH_SHORTCUTS: ShortcutGroup = {
  title: 'Dispatch',
  rows: [
    { keys: ['J'], label: 'Next ticket in the list (or ↓); from the last, the first' },
    { keys: ['K'], label: 'Previous ticket (or ↑); from the first, the last' },
    { keys: ['enter'], label: 'Open the focused ticket' },
    { keys: ['X'], label: 'Select the focused ticket, or leave it out; the palette sets what the selection is' },
    { keys: ['C'], label: 'New ticket in this project' },
    { keys: ['S'], label: 'Status of the tickets selected, or of the focused one' },
    { keys: ['P'], label: 'Priority' },
    { keys: ['A'], label: 'Assignees' },
    { keys: ['L'], label: 'Labels' },
    { keys: ['M'], label: 'Milestone' },
    { keys: ['O'], label: 'Owner' },
    { keys: ['arrowright'], label: 'On a card of the board or the roadmap: move it a column right; ← left' },
    { keys: ['escape'], label: "Clear the selection, else leave the list; on a ticket's page, back to the list" },
  ],
}

/** The editor area's keys (design 4b, 4c), on a session page with a file open. */
/** Copy and paste in a terminal, as Windows Terminal has them (round 15; macOS keeps ⌘C and ⌘V). Shown on every page. */
export const TERMINAL_SHORTCUTS: ShortcutGroup = {
  title: 'The terminal',
  rows: [
    { keys: [], label: 'Copy the selection (Ctrl+Insert too)', terminal: ['ctrl', 'shift', 'C'], chord: 'copy' },
    { keys: [], label: 'On a selection: copy it and clear it; with none, the interrupt', terminal: ['ctrl', 'C'] },
    { keys: [], label: 'Paste (Shift+Insert and Ctrl+V too)', terminal: ['ctrl', 'shift', 'V'], chord: 'paste' },
    { keys: [], label: 'Right-click: copy a selection, else paste (the program\'s, when it uses the mouse); Shift+right-click: the menu' },
  ],
}

export const EDITOR_SHORTCUTS: ShortcutGroup = {
  title: 'The editor',
  rows: [
    { keys: ['T'], label: 'Terminal only: fold the editor to its tab strip, or bring it back', terminal: ['alt', 'T'] },
    { keys: ['alt', 'P'], label: 'In the editor: a Markdown file rendered (Mermaid diagrams drawn), or its source again' },
    { keys: ['ctrl', 'tab'], label: 'Next file (Ctrl+Shift+Tab the one before)' },
    { keys: ['ctrl', 'W'], label: 'Close the file' },
    { keys: ['ctrl', 'F'], label: 'Find in the file (Ctrl+H replace)' },
    { keys: ['ctrl', 'G'], label: 'Go to a line' },
  ],
}

/** The session page's keys (#303): its own, then the editor's; registered by the session page. */
export const SESSION_SHORTCUTS: ShortcutGroup = {
  title: 'The session page',
  rows: [
    { keys: ['S'], label: 'Share this session' },
    { keys: ['X'], label: 'Stop this session: the palette asks; Enter stops, Escape cancels', terminal: ['alt', 'X'] },
    ...EDITOR_SHORTCUTS.rows,
  ],
}

export const WALL_SHORTCUTS: ShortcutGroup = {
  title: 'Yard',
  rows: [
    { keys: ['escape'], label: 'Back to the grid (Alt+Esc too, where the system leaves it)', terminal: ['alt', 'Q'], chord: 'leaveTerminal' },
    { keys: ['J'], label: 'Next in the queue', terminal: ['alt', 'J'] },
    { keys: ['K'], label: 'Previous in the queue', terminal: ['alt', 'K'] },
    { keys: ['enter'], label: 'Reply to the selected queue item' },
  ],
}

export const CAROUSEL_SHORTCUTS: ShortcutGroup = {
  title: 'Roundhouse',
  rows: [
    { keys: ['arrowleft'], label: 'Previous session', terminal: ['alt', 'arrowleft'] },
    { keys: ['arrowright'], label: 'Next session', terminal: ['alt', 'arrowright'] },
    { keys: ['enter'], label: 'Type into the current session' },
    { keys: ['space'], label: 'Pause or resume rotation', terminal: ['alt', 'P'] },
    { keys: ['escape'], label: 'Leave the terminal (Alt+Esc too, where the system leaves it)', terminal: ['alt', 'Q'], chord: 'leaveTerminal' },
    { keys: ['space'], label: 'On a chip of the strip: pick it up or drop it; the arrows move it, Escape puts it back' },
  ],
}

/**
 * The Alt chords a page takes beside the global ones (#312, Codex's round): the Yard and the Roundhouse take Alt+Q to leave the
 * terminal, and a terminal hands it back there alone, so a session page's terminal keeps it (zsh's push-line, for one).
 */
export function usePageAltCodes() {
  // Counted, not listed: Nuxt sets the next page up before the last one is unmounted, so a code both take must survive the
  // last page's cleanup (whole-batch round).
  const counts = useState<Record<string, number>>('pageAltCodes', () => ({}))
  function register(list: string[]) {
    const next = { ...counts.value }
    for (const c of list) next[c] = (next[c] ?? 0) + 1
    counts.value = next
    onBeforeUnmount(() => {
      const after = { ...counts.value }
      for (const c of list) {
        const n = (after[c] ?? 0) - 1
        if (n > 0) after[c] = n
        else delete after[c]
      }
      counts.value = after
    })
  }
  const has = (code: string) => (counts.value[code] ?? 0) > 0
  return { counts, register, has }
}

/**
 * State for the shortcuts modal. Pages register the group that applies to
 * them so the modal always lists what works on the current screen.
 * Plain shortcuts never fire while a terminal or a form field has focus
 * (keys go to the agent); the Alt variants do, because the terminal hands
 * those chords back to the page.
 */
export function useShortcutsModal() {
  const open = useState<boolean>('shortcutsOpen', () => false)
  const pageGroup = useState<ShortcutGroup | null>('shortcutsPageGroup', () => null)
  // The system the page runs on (#312): the rows show its chords, and the modal says what it takes.
  const system = useState<System>('shortcutsSystem', () => (import.meta.client ? detectSystem(navigator.userAgent, navigator.platform) : 'linux'))
  const desktop = useDesktop()
  const notes = computed(() => systemNotes(system.value, desktop.isDesktop.value ? 'app' : 'browser'))

  function registerPage(group: ShortcutGroup | null) {
    pageGroup.value = group
    onBeforeUnmount(() => {
      if (pageGroup.value === group) pageGroup.value = null
    })
  }

  const groups = computed<ShortcutGroup[]>(() => (pageGroup.value ? [pageGroup.value, GLOBAL_SHORTCUTS, TERMINAL_SHORTCUTS, PALETTE_SHORTCUTS, SIDEBAR_SHORTCUTS] : [GLOBAL_SHORTCUTS, TERMINAL_SHORTCUTS, PALETTE_SHORTCUTS, SIDEBAR_SHORTCUTS]).map((g) => rowsFor(g, system.value)))

  return { open, groups, system, notes, registerPage, show: () => (open.value = true) }
}
