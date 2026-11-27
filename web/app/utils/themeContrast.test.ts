import { beforeAll, describe, expect, it, vi } from 'vitest'
import { Resolved, SHADES, appColors, atDeclarations, colourOf, declarations, isRoot, readWeb, substitute, tailwindColour, type Theme } from '../../build/themeTokens'
import { CONTRAST_FLOORS, over, parseColor, ratio, toHex, type Rgba } from './contrast'
import { MONACO_COLORS } from './monacoColors'

// The contrast test of the tokens (the UX review's PR-24). Every `--ui-*` token is resolved in both themes the way the page does it,
// from the files that set them (`web/build/themeTokens.ts`, which DESIGN.md's generator reads too), and each pair is then held to
// CONTRAST_FLOORS.

const tokensCss = readWeb('public/brand/tokens.css')

interface UiConfig {
  colors: Record<string, string>
  dashboardSidebar?: { slots?: Record<string, string> }
  button?: { compoundVariants?: Array<{ color?: string; variant?: string; class?: string }> }
  badge?: { compoundVariants?: Array<{ color?: string; variant?: string; class?: string }> }
}

let ui: UiConfig

beforeAll(async () => {
  vi.stubGlobal('defineAppConfig', (c: unknown) => c)
  ui = ((await import('../app.config')).default as unknown as { ui: UiConfig }).ui
})

const SURFACES = ['--ui-bg', '--ui-bg-muted', '--ui-bg-elevated', '--ui-bg-canvas'] as const
const TEXTS = ['--ui-text-highlighted', '--ui-text', '--ui-text-toned', '--ui-text-muted', '--ui-text-dimmed'] as const
const ROLES = ['primary', 'success', 'warning', 'error', 'info'] as const
const THEMES: Theme[] = ['light', 'dark']

/** "front on back: got < floor" for a pair under its floor, else nothing. */
function judge(what: string, front: Rgba, back: Rgba, floor: number): string[] {
  const r = ratio(front, back)
  return r + 1e-9 < floor ? [`${what}: ${r.toFixed(2)} < ${floor}`] : []
}

const short = (name: string) => name.replace('--ui-', '')

describe('the tokens', () => {
  it("read each role's colour from app.config.ts as the app does", () => {
    expect(appColors()).toEqual(ui.colors)
  })
  it('take the ink scale as the neutral, every one of its eleven steps written', () => {
    expect(ui.colors.neutral).toBe('ink')
    const scales = atDeclarations('@theme static')
    expect(SHADES.filter((n) => !parseColor(scales[`--color-ink-${n}`]))).toEqual([])
    // Every role's every step is a colour, main.css's or Tailwind's: the plugin's fallback for a name Tailwind does not know is empty.
    const missing = Object.entries(ui.colors).flatMap(([role, name]) => SHADES.filter((n) => !parseColor(scales[`--color-${name}-${n}`]) && !parseColor(tailwindColour(name, n))).map((n) => `${role} ${name}-${n}`))
    expect(missing).toEqual([])
    expect([parseColor(scales['--color-ink-100'])!, parseColor(scales['--color-ink-900'])!].map(toHex)).toEqual(['#eef1e9', '#18211c']) // sage, ink
    expect(toHex(parseColor(scales['--color-ink-300'])!)).toBe('#bfc9be') // the brand's border
  })

  for (const theme of THEMES) {
    it(`resolve to colours in ${theme}, and the canvas is a step off the page`, () => {
      const t = new Resolved(theme)
      const names = Object.keys(t.vars).filter((k) => /^--ui-(text|bg|border)/.test(k) || ROLES.some((r) => k === `--ui-${r}` || k === `--ui-${r}-text`))
      expect(names.length).toBeGreaterThan(20)
      expect(names.filter((n) => !colourOf(substitute(`var(${n})`, t.vars)))).toEqual([])
      expect(toHex(t.token('--ui-bg-canvas'))).not.toBe(toHex(t.token('--ui-bg')))
      expect(toHex(t.token('--ui-bg-canvas-hover'))).not.toBe(toHex(t.token('--ui-bg-canvas')))
    })
  }

  it('give the page the brand ground: white in light, ink in dark, under a canvas of sage and of the deeper ink', () => {
    const [light, dark] = THEMES.map((t) => new Resolved(t))
    expect([light!.token('--ui-bg'), light!.token('--ui-bg-canvas'), dark!.token('--ui-bg'), dark!.token('--ui-bg-canvas')].map(toHex)).toEqual(['#ffffff', '#eef1e9', '#18211c', '#0c1410'])
  })
})

describe('the floors, in both themes', () => {
  for (const theme of THEMES) {
    it(`hold every text token on every surface in ${theme}`, () => {
      const t = new Resolved(theme)
      const low: string[] = []
      for (const s of SURFACES) {
        const back = t.token(s)
        for (const x of TEXTS) {
          const floor = x === '--ui-text-dimmed' ? (s === '--ui-bg' ? CONTRAST_FLOORS.dimmed.page : CONTRAST_FLOORS.dimmed.raised) : CONTRAST_FLOORS.text
          low.push(...judge(`${short(x)} on ${short(s)}`, t.token(x), back, floor))
        }
        for (const role of ROLES) low.push(...judge(`text-${role} on ${short(s)}`, t.text(role), back, CONTRAST_FLOORS.text))
      }
      expect(low).toEqual([])
    })

    it(`hold the subtle tints, the hover tone and the accented track in ${theme}`, () => {
      const t = new Resolved(theme)
      const low: string[] = []
      for (const s of ['--ui-bg', '--ui-bg-canvas']) {
        for (const role of ROLES) {
          const tint = over(t.token(`--ui-${role}`), t.token(s), 0.1)
          low.push(...judge(`text-${role} on its tint over ${short(s)}`, t.text(role), tint, CONTRAST_FLOORS.text))
        }
      }
      low.push(...judge('text-muted on bg-canvas-hover', t.token('--ui-text-muted'), t.token('--ui-bg-canvas-hover'), CONTRAST_FLOORS.text))
      for (const x of ['--ui-text-highlighted', '--ui-text']) low.push(...judge(`${short(x)} on bg-accented`, t.token(x), t.token('--ui-bg-accented'), CONTRAST_FLOORS.text))
      expect(low).toEqual([])
    })

    it(`hold the solid fills' text in ${theme}, ink on amber`, () => {
      const t = new Resolved(theme)
      const inkOnAmber = (part: 'button' | 'badge') => {
        const entry = ui[part]?.compoundVariants?.find((v) => v.color === 'warning' && v.variant === 'solid')
        const m = entry?.class?.match(/\btext-\((--[\w-]+)\)/)
        expect(m?.[1], `${part}: a solid warning fill names its text colour`).toBe('--ui-color-neutral-900')
        return t.token(m![1]!)
      }
      const low: string[] = []
      const fills = theme === 'light' ? (['primary', 'error', 'info'] as const) : (['primary', 'error', 'info', 'success'] as const)
      for (const role of fills) low.push(...judge(`text-inverted on bg-${role}`, t.token('--ui-text-inverted'), t.token(`--ui-${role}`), CONTRAST_FLOORS.text))
      for (const part of ['button', 'badge'] as const) low.push(...judge(`${part}: its text on bg-warning`, inkOnAmber(part), t.token('--ui-warning'), CONTRAST_FLOORS.text))
      expect(low).toEqual([])
    })
  }
})

describe('the sidebar', () => {
  it('is drawn on the canvas: the rail, the full sidebar and the slideover', () => {
    expect(ui.dashboardSidebar?.slots?.root).toContain('bg-canvas')
    expect(ui.dashboardSidebar?.slots?.content).toContain('bg-canvas')
    const inline = atDeclarations('@theme inline')
    expect(inline['--background-color-canvas']).toBe('var(--ui-bg-canvas)')
    expect(inline['--background-color-canvas-hover']).toBe('var(--ui-bg-canvas-hover)')
  })
})

describe("the brand's tokens.css", () => {
  // The workbench draws with main.css; tokens.css names the same values for the brand's pages and specimen, and agrees with them.
  const NAMES: Record<string, [Theme, string]> = {
    '--conductor-surface': ['light', '--ui-bg-canvas'],
    '--conductor-panel': ['light', '--ui-bg'],
    '--conductor-text': ['light', '--ui-text-highlighted'],
    '--conductor-border': ['light', '--ui-border-accented'],
    '--conductor-action': ['light', '--ui-primary'],
    '--conductor-accent-text': ['light', '--ui-secondary'],
    '--conductor-success': ['light', '--ui-success-text'],
    '--conductor-warning': ['light', '--ui-warning-text'],
    '--conductor-error': ['light', '--ui-error'],
    '--conductor-info': ['light', '--ui-info'],
    '--conductor-dark-surface': ['dark', '--ui-bg-canvas'],
    '--conductor-dark-panel': ['dark', '--ui-bg'],
    '--conductor-dark-text': ['dark', '--ui-text'],
    '--conductor-dark-muted': ['dark', '--ui-text-muted'],
  }
  it('names the values the workbench draws, in light and in dark', () => {
    const tokens = declarations(tokensCss.slice(0, tokensCss.indexOf('[data-conductor-theme')), isRoot)
    const t = { light: new Resolved('light'), dark: new Resolved('dark') }
    const got = Object.fromEntries(Object.entries(NAMES).map(([name, [theme, token]]) => [name, toHex(t[theme].token(token))]))
    const want = Object.fromEntries(Object.keys(NAMES).map((name) => [name, toHex(parseColor(tokens[name])!)]))
    expect(want).toEqual(got)
  })
})

describe("the Files editor's themes", () => {
  for (const theme of THEMES) {
    it(`sit on the page's ground in ${theme}, with readable text and line numbers`, () => {
      const t = new Resolved(theme)
      const c = MONACO_COLORS[theme] as Record<string, string>
      const bg = parseColor(c['editor.background'])!
      expect(toHex(bg)).toBe(toHex(t.token('--ui-bg')))
      const low = [
        ...judge('editor.foreground', parseColor(c['editor.foreground'])!, bg, CONTRAST_FLOORS.text),
        ...judge('editorLineNumber.foreground', parseColor(c['editorLineNumber.foreground'])!, bg, CONTRAST_FLOORS.large),
        ...judge('editorLineNumber.activeForeground', parseColor(c['editorLineNumber.activeForeground'])!, bg, CONTRAST_FLOORS.large),
      ]
      expect(low).toEqual([])
    })
  }
})
