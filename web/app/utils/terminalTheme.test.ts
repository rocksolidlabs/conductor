import { describe, expect, it } from 'vitest'
import { CONTRAST_FLOORS, ratio } from './contrast'
import { buildTerminalTheme, cssColorToHex } from './terminalTheme'

function channels(hex: string): number[] {
  return [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16))
}

function expectClose(actual: string | undefined, expected: string, tolerance = 3) {
  expect(actual).toBeDefined()
  const a = channels(actual!)
  const e = channels(expected)
  for (let i = 0; i < 3; i++) expect(Math.abs(a[i]! - e[i]!), `${actual} vs ${expected}`).toBeLessThanOrEqual(tolerance)
}

describe('cssColorToHex', () => {
  it('normalises hex forms and drops alpha', () => {
    expect(cssColorToHex('#abc')).toBe('#aabbcc')
    expect(cssColorToHex('#ABCD')).toBe('#aabbcc')
    expect(cssColorToHex('#18181b')).toBe('#18181b')
    expect(cssColorToHex('#18181bcc')).toBe('#18181b')
    expect(cssColorToHex(' #FFFFFF ')).toBe('#ffffff')
    expect(cssColorToHex('#12345')).toBeUndefined()
  })

  it('parses rgb() in comma and space syntax', () => {
    expect(cssColorToHex('rgb(24, 24, 27)')).toBe('#18181b')
    expect(cssColorToHex('rgb(24 24 27 / 0.5)')).toBe('#18181b')
    expect(cssColorToHex('rgba(255,255,255,1)')).toBe('#ffffff')
    expect(cssColorToHex('rgb(100%, 0%, 0%)')).toBe('#ff0000')
  })

  it('parses color(srgb …)', () => {
    expect(cssColorToHex('color(srgb 1 0 0)')).toBe('#ff0000')
    expect(cssColorToHex('color(display-p3 1 0 0)')).toBeUndefined()
  })

  it('converts the Tailwind v4 zinc scale from oklch', () => {
    expectClose(cssColorToHex('oklch(21% 0.006 285.885)'), '#18181b') // zinc-900
    expectClose(cssColorToHex('oklch(27.4% 0.006 286.033)'), '#27272a') // zinc-800
    expectClose(cssColorToHex('oklch(55.2% 0.016 285.938)'), '#71717b') // zinc-500
    expectClose(cssColorToHex('oklch(70.5% 0.015 286.067)'), '#9f9fa9') // zinc-400
    expectClose(cssColorToHex('oklch(92% 0.004 286.32)'), '#e4e4e7') // zinc-200
    expectClose(cssColorToHex('oklch(96.7% 0.001 286.375)'), '#f4f4f5') // zinc-100
    expectClose(cssColorToHex('oklch(98.5% 0 none)'), '#fafafa') // zinc-50
    expectClose(cssColorToHex('oklch(1 0 0)'), '#ffffff')
    expectClose(cssColorToHex('oklch(0% 0 0)'), '#000000')
  })

  it('converts oklab and rejects unknown formats', () => {
    expectClose(cssColorToHex('oklab(0.21 0.0016 -0.0058)'), '#18181b', 4)
    expect(cssColorToHex('hsl(0 100% 50%)')).toBeUndefined()
    expect(cssColorToHex('var(--ui-bg)')).toBeUndefined()
    expect(cssColorToHex('')).toBeUndefined()
    expect(cssColorToHex(undefined)).toBeUndefined()
  })
})

describe('buildTerminalTheme', () => {
  it('uses the resolved UI colours for canvas, text and cursor', () => {
    const t = buildTerminalTheme(true, { bg: 'oklch(21% 0.006 285.885)', text: '#e4e4e7', secondary: '#df8259', primary: '#9bb3a3' })
    expectClose(t.background, '#18181b')
    expect(t.foreground).toBe('#e4e4e7')
    expect(t.cursor).toBe('#df8259')
    expect(t.cursorAccent).toBe(t.background)
    expect(t.selectionBackground).toBe('#9bb3a366')
    expect(t.red).toBe('#ff7b72')
  })

  it('falls back to the ink scale per mode when variables are missing', () => {
    const dark = buildTerminalTheme(true, {})
    expect(dark.background).toBe('#18211c')
    expect(dark.foreground).toBe('#dcdfd7')
    const light = buildTerminalTheme(false, { bg: 'var(--nope)' })
    expect(light.background).toBe('#ffffff')
    expect(light.foreground).toBe('#18211c')
    expect(light.red).toBe('#cf222e')
  })

  // The sixteen ANSI colours are held by numbers on the page's own ground (ink in dark, white in light), not looked at: every base
  // colour but black at the text floor, every bright one but bright black at the large-text floor. They pass as they are; they fail
  // a later retune that goes too far.
  const BASE = ['red', 'green', 'yellow', 'blue', 'magenta', 'cyan', 'white'] as const
  const BRIGHT = ['brightRed', 'brightGreen', 'brightYellow', 'brightBlue', 'brightMagenta', 'brightCyan', 'brightWhite'] as const
  for (const [mode, dark, bg] of [['dark', true, '#18211c'], ['light', false, '#ffffff']] as const) {
    it(`keeps the ANSI colours readable on the ${mode} ground`, () => {
      const t = buildTerminalTheme(dark, { bg })
      expect(t.background).toBe(bg)
      const low = [
        ...BASE.filter((k) => ratio(t[k]!, bg) < CONTRAST_FLOORS.text).map((k) => `${k} ${ratio(t[k]!, bg).toFixed(2)}`),
        ...BRIGHT.filter((k) => ratio(t[k]!, bg) < CONTRAST_FLOORS.large).map((k) => `${k} ${ratio(t[k]!, bg).toFixed(2)}`),
      ]
      expect(low).toEqual([])
    })
  }
})
