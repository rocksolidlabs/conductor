import { describe, expect, it } from 'vitest'
import { ALT_PASSTHROUGH_CODES, CAROUSEL_SHORTCUTS, DISPATCH_SHORTCUTS, EDITOR_SHORTCUTS, GLOBAL_SHORTCUTS, PALETTE_SHORTCUTS, SESSION_SHORTCUTS, SIDEBAR_SHORTCUTS, WALL_SHORTCUTS, type ShortcutGroup } from './useShortcuts'

const fRows = (g: ShortcutGroup) => g.rows.filter((r) => r.keys.length === 1 && r.keys[0] === 'F')

describe('the fullscreen shortcut', () => {
  it('is listed once, under Everywhere, and no longer by the wall or the carousel', () => {
    expect(fRows(GLOBAL_SHORTCUTS)).toHaveLength(1)
    expect(fRows(GLOBAL_SHORTCUTS)[0]!.label).toBe('Toggle fullscreen')
    expect(fRows(GLOBAL_SHORTCUTS)[0]!.terminal).toEqual(['alt', 'F'])
    expect(fRows(WALL_SHORTCUTS)).toHaveLength(0)
    expect(fRows(CAROUSEL_SHORTCUTS)).toHaveLength(0)
  })
})

describe('the sidebar shortcut', () => {
  it('says it toggles the rail', () => {
    expect(GLOBAL_SHORTCUTS.rows.find((r) => r.keys.join('+') === 'meta+B')?.label).toBe('Collapse the sidebar to the rail, or expand it')
  })
})

describe('the sidebar shortcuts', () => {
  it('list the filter and every row key, after Everywhere, and take / with them', () => {
    expect(GLOBAL_SHORTCUTS.rows.some((r) => r.keys.join('') === '/')).toBe(false)
    expect(SIDEBAR_SHORTCUTS.title).toBe('The sidebar')
    expect(SIDEBAR_SHORTCUTS.rows.map((r) => r.keys.join('+'))).toEqual(['/', 'arrowdown', 'J', 'K', 'enter', '1', 'R', 'S', 'X', 'escape'])
    expect(SIDEBAR_SHORTCUTS.rows.find((r) => r.keys[0] === 'X')?.label).toBe('Stop it: the row asks; Enter stops, Escape cancels')
  })
})

describe('the page chords', () => {
  const chord = (second: string) => GLOBAL_SHORTCUTS.rows.find((r) => r.keys[0] === 'G' && r.keys[1] === second)
  it("go by each page's initial: Y the Yard, R the Roundhouse, C Crews, A Agents, E Events, each with its Alt twin in a terminal", () => {
    expect(chord('Y')).toEqual({ keys: ['G', 'Y'], label: 'Go to the Yard', terminal: ['alt', 'Y'] })
    expect(chord('R')).toEqual({ keys: ['G', 'R'], label: 'Go to the Roundhouse', terminal: ['alt', 'R'] })
    expect(chord('C')).toEqual({ keys: ['G', 'C'], label: 'Go to Crews', terminal: ['alt', 'C'] })
    expect(chord('A')?.terminal).toEqual(['alt', 'A'])
    expect(chord('E')?.terminal).toEqual(['alt', 'E'])
    expect(chord('W')).toBeUndefined()
  })
  it('go to Brakeman with G then B, and no Alt twin: Alt+B is the sidebar\'s', () => {
    expect(chord('B')).toEqual({ keys: ['G', 'B'], label: 'Go to Brakeman' })
    expect(GLOBAL_SHORTCUTS.rows.find((r) => r.terminal?.join('+') === 'alt+B')?.keys).toEqual(['meta', 'B'])
  })
  it('name no Alt twin for what needs the list or the page focused, and no label says "in a terminal" any more', () => {
    for (const g of [GLOBAL_SHORTCUTS, SIDEBAR_SHORTCUTS, WALL_SHORTCUTS, CAROUSEL_SHORTCUTS]) for (const r of g.rows) expect(r.label).not.toMatch(/in a terminal/)
    expect(SIDEBAR_SHORTCUTS.rows.find((r) => r.keys[0] === 'J')?.terminal).toBeUndefined()
    // The Yard's rows are Escape alone: its queue is gone, and ] and [ step through what needs you on every page.
    expect(WALL_SHORTCUTS.rows.map((r) => r.keys)).toEqual([['escape']])
  })
  it('hand their Alt twins back from a terminal, and Alt+W no longer', () => {
    for (const code of ['KeyY', 'KeyR', 'KeyC', 'KeyA', 'KeyE']) expect(ALT_PASSTHROUGH_CODES.has(code)).toBe(true)
    expect(ALT_PASSTHROUGH_CODES.has('KeyW')).toBe(false)
    // Alt+J and Alt+K reach the terminal on every page.
    expect(ALT_PASSTHROUGH_CODES.has('KeyJ')).toBe(false)
    expect(ALT_PASSTHROUGH_CODES.has('KeyK')).toBe(false)
  })
})

describe("the editor's keys", () => {
  it('fold with T and its Alt twin, switch with Ctrl+Tab, close with Ctrl+W, find and go to a line', () => {
    const fold = EDITOR_SHORTCUTS.rows.find((r) => r.keys.join('') === 'T')
    expect(fold?.terminal).toEqual(['alt', 'T'])
    expect(ALT_PASSTHROUGH_CODES.has('KeyT')).toBe(true)
    expect(EDITOR_SHORTCUTS.rows.map((r) => r.keys.join('+'))).toEqual(['T', 'alt+P', 'ctrl+tab', 'ctrl+W', 'ctrl+F', 'ctrl+G'])
    for (const r of EDITOR_SHORTCUTS.rows) expect(r.label).not.toMatch(/in a terminal/)
  })
})

describe('the terminal group', () => {
  it('lists copy and paste with their keys in a terminal, none outside one', async () => {
    const { TERMINAL_SHORTCUTS } = await import('./useShortcuts')
    expect(TERMINAL_SHORTCUTS.rows.map((r) => r.terminal?.join('+') ?? '')).toEqual(['ctrl+shift+C', 'ctrl+C', 'ctrl+shift+V', ''])
    expect(TERMINAL_SHORTCUTS.rows.every((r) => r.keys.length === 0)).toBe(true)
  })
})

describe("the command palette's keys (#302)", () => {
  it('open it with meta+K everywhere and Alt+/ from a terminal, which hands the slash back', () => {
    const open = GLOBAL_SHORTCUTS.rows.find((r) => r.keys.join('+') === 'meta+K')
    expect(open?.terminal).toEqual(['alt', '/'])
    expect(open?.label).toMatch(/command palette/)
    expect(ALT_PASSTHROUGH_CODES.has('Slash')).toBe(true)
  })
  it("list the palette's own keys as a group, and say what / does now", () => {
    expect(PALETTE_SHORTCUTS.title).toBe('The command palette')
    expect(PALETTE_SHORTCUTS.rows.map((r) => r.keys.join('+'))).toEqual(['arrowdown', 'enter', '1', 'J', 'arrowright', 'S', 'X', 'R', 'meta+enter', 'escape', '>'])
    for (const r of PALETTE_SHORTCUTS.rows) expect(r.terminal).toBeUndefined()
    expect(SIDEBAR_SHORTCUTS.rows.find((r) => r.keys[0] === '/')?.label).toMatch(/command palette/)
    expect(SIDEBAR_SHORTCUTS.rows.find((r) => r.keys[0] === '/')?.terminal).toEqual(['alt', 'S'])
  })
})

describe("the palette's reach (#303)", () => {
  it('lists ] and [ with their Alt twins, G S for the desktop app, and hands the codes back from a terminal', () => {
    const next = GLOBAL_SHORTCUTS.rows.find((r) => r.keys.join('') === ']')
    const prev = GLOBAL_SHORTCUTS.rows.find((r) => r.keys.join('') === '[')
    expect(next?.terminal).toEqual(['alt', ']'])
    expect(prev?.terminal).toEqual(['alt', '['])
    expect(next?.label).toMatch(/needs you/)
    expect(GLOBAL_SHORTCUTS.rows.find((r) => r.keys.join('+') === 'G+S')?.terminal).toEqual(['alt', ','])
    for (const code of ['BracketRight', 'BracketLeft', 'Comma', 'KeyX']) expect(ALT_PASSTHROUGH_CODES.has(code)).toBe(true)
    // Alt+Q is the Yard's and the Roundhouse's alone (usePageAltCodes): a session page's terminal keeps it.
    expect(ALT_PASSTHROUGH_CODES.has('KeyQ')).toBe(false)
  })
  it("lists S and X on the session page's group, with Alt+X in its terminal, ahead of the editor's keys", () => {
    expect(SESSION_SHORTCUTS.rows[0]).toEqual({ keys: ['S'], label: 'Share this session' })
    expect(SESSION_SHORTCUTS.rows[1]).toEqual({ keys: ['X'], label: 'Stop this session: the palette asks; Enter stops, Escape cancels', terminal: ['alt', 'X'] })
    expect(SESSION_SHORTCUTS.rows.slice(2)).toEqual(EDITOR_SHORTCUTS.rows)
  })
})

describe("Dispatch's keys (docs/plans/dispatch.md, Owner decisions, 9 and 10)", () => {
  it('go to Dispatch with G then D, and Alt+D from a terminal, which hands the D back', () => {
    const row = GLOBAL_SHORTCUTS.rows.find((r) => r.keys[0] === 'G' && r.keys[1] === 'D')
    expect(row).toEqual({ keys: ['G', 'D'], label: 'Go to Dispatch', terminal: ['alt', 'D'] })
    // After Crews, as the sidebar's foot has it.
    const i = GLOBAL_SHORTCUTS.rows.indexOf(row!)
    expect(GLOBAL_SHORTCUTS.rows[i - 1]?.label).toBe('Go to Crews')
    expect(ALT_PASSTHROUGH_CODES.has('KeyD')).toBe(true)
  })
  it("list the list's keys, none of them reaching past a terminal", () => {
    expect(DISPATCH_SHORTCUTS.title).toBe('Dispatch')
    // Linear's letters (PR-9): the selection, a new ticket, the six pickers, a card's move, then Escape.
    expect(DISPATCH_SHORTCUTS.rows.map((r) => r.keys.join('+'))).toEqual(['J', 'K', 'enter', 'X', 'C', 'S', 'P', 'A', 'L', 'M', 'O', 'arrowright', 'escape'])
    for (const r of DISPATCH_SHORTCUTS.rows) {
      expect(r.terminal).toBeUndefined()
      expect(r.label).not.toMatch(/in a terminal/)
    }
    expect(DISPATCH_SHORTCUTS.rows[0]!.label).toMatch(/from the last, the first/)
  })
})
