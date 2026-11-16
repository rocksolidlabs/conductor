import type { ITheme } from '@xterm/xterm'
import { parseColor, toHex } from './contrast'

/**
 * Terminal colours follow the workbench: the canvas is the UI background
 * (`--ui-bg`), text is the UI text colour and the cursor is the brand accent.
 * ANSI colours are fixed palettes tuned for a neutral dark or light canvas.
 */

const ANSI_DARK = {
  black: '#484f58',
  red: '#ff7b72',
  green: '#3fb950',
  yellow: '#d29922',
  blue: '#58a6ff',
  magenta: '#bc8cff',
  cyan: '#39c5cf',
  white: '#b1bac4',
  brightBlack: '#6e7681',
  brightRed: '#ffa198',
  brightGreen: '#56d364',
  brightYellow: '#e3b341',
  brightBlue: '#79c0ff',
  brightMagenta: '#d2a8ff',
  brightCyan: '#56d4dd',
  brightWhite: '#f0f6fc',
}

const ANSI_LIGHT = {
  black: '#24292f',
  red: '#cf222e',
  green: '#116329',
  yellow: '#7d4e00',
  blue: '#0969da',
  magenta: '#8250df',
  cyan: '#1b7c83',
  white: '#6e7781',
  brightBlack: '#57606a',
  brightRed: '#a40e26',
  brightGreen: '#1a7f37',
  brightYellow: '#633c01',
  brightBlue: '#218bff',
  brightMagenta: '#a475f9',
  brightCyan: '#3192aa',
  brightWhite: '#8c959f',
}

/** Used when a CSS variable is missing or in an unexpected format. */
const FALLBACK = {
  dark: { bg: '#18211c', fg: '#dcdfd7', cursor: '#df8259', selection: '#9bb3a3' },
  light: { bg: '#ffffff', fg: '#18211c', cursor: '#a44727', selection: '#263d35' },
}

export interface UiColors {
  bg?: string
  text?: string
  secondary?: string
  primary?: string
}

/**
 * Normalises a CSS colour to `#rrggbb` for xterm, which only understands hex
 * and rgb(). Handles hex, rgb()/rgba(), color(srgb …), oklab() and oklch(),
 * which is what Tailwind v4 palettes resolve to (contrast.ts's parser). Alpha
 * is dropped; `transparent` is no colour for a canvas.
 */
export function cssColorToHex(value: string | undefined | null): string | undefined {
  if (!value || value.trim().toLowerCase() === 'transparent') return undefined
  const c = parseColor(value)
  return c ? toHex(c) : undefined
}

function withAlpha(hex: string, alpha: string): string {
  return `${hex}${alpha}`
}

/** Builds the xterm theme for the current colour mode from resolved UI colours. */
export function buildTerminalTheme(dark: boolean, colors: UiColors = {}): ITheme {
  const fb = dark ? FALLBACK.dark : FALLBACK.light
  const background = cssColorToHex(colors.bg) ?? fb.bg
  const foreground = cssColorToHex(colors.text) ?? fb.fg
  const cursor = cssColorToHex(colors.secondary) ?? fb.cursor
  const selection = cssColorToHex(colors.primary) ?? fb.selection
  return {
    background,
    foreground,
    cursor,
    cursorAccent: background,
    selectionBackground: withAlpha(selection, dark ? '66' : '40'),
    selectionInactiveBackground: withAlpha(selection, dark ? '33' : '20'),
    ...(dark ? ANSI_DARK : ANSI_LIGHT),
  }
}
