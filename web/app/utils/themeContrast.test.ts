import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import tailwind from 'tailwindcss/colors'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import { CONTRAST_FLOORS, mixSrgb, over, parseColor, ratio, toHex, type Rgba } from './contrast'
import { MONACO_COLORS } from './monacoColors'

// The contrast test of the tokens (the UX review's PR-24). It resolves every `--ui-*` token in both themes the way the page does, from
// the sources that set them: Nuxt UI's colours plugin (each role's steps from the colour named in app.config.ts, and the step each
// role takes in light and dark), Nuxt UI's runtime stylesheet (which step is text, muted, dimmed, the page and the raised surfaces),
// and main.css (the scales, the canvas, the text-safe amber and green). The plugin and the stylesheet are read from node_modules, so
// an upgrade that remaps a token or a step is followed here, not hidden. Each pair is then held to CONTRAST_FLOORS.

const read = (rel: string) => readFileSync(fileURLToPath(new URL(rel, import.meta.url)), 'utf8')
// main.css too is read from the disk: vitest hands a stylesheet's `?raw` import over empty.
const mainCss = read('../assets/css/main.css')
const nuxtCss = read('../../node_modules/@nuxt/ui/dist/runtime/index.css')
const colorsPlugin = read('../../node_modules/@nuxt/ui/dist/runtime/plugins/colors.js')
const tokensCss = read('../../public/brand/tokens.css')

type Vars = Record<string, string>
type Theme = 'light' | 'dark'
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

/** The declarations of every rule whose selector list matches `pick`, in source order, from CSS text (comments removed). */
function declarations(css: string, pick: (selectors: string[]) => boolean): Vars {
  const out: Vars = {}
  const text = css.replace(/\/\*[\s\S]*?\*\//g, '')
  for (const m of text.matchAll(/([^{};]+)\{([^{}]*)\}/g)) {
    const selectors = m[1]!.split(',').map((s) => s.trim())
    if (!pick(selectors)) continue
    for (const d of m[2]!.matchAll(/(--[\w-]+)\s*:\s*([^;]+)/g)) out[d[1]!] = d[2]!.trim()
  }
  return out
}

/** A block's body by its at-rule head (`@theme static`), or '' when there is none. */
function atBlock(css: string, head: string): string {
  const at = css.indexOf(`${head} {`)
  if (at < 0) return ''
  const open = css.indexOf('{', at)
  return css.slice(open, css.indexOf('}', open) + 1)
}

/** The declarations of an at-rule block of main.css (`@theme static`, `@theme inline`). */
const atDeclarations = (head: string) => declarations(`theme ${atBlock(mainCss, head)}`, () => true)

const isRoot = (s: string[]) => s.includes(':root')
const isDark = (s: string[]) => s.length === 1 && s[0] === '.dark'

/** The steps the colours plugin gives every role in light and in dark, read from its source. */
function pluginSteps(): { light: number; dark: number } {
  const light = colorsPlugin.match(/:root, :host, \.light \{[\s\S]*?generateColor\(key, (\d+)\)/)
  const dark = colorsPlugin.match(/\.dark \{[\s\S]*?generateColor\(key, (\d+)\)/)
  expect(colorsPlugin, 'the colours plugin writes each step from the colour named in app.config').toContain('--ui-color-${key}-${shade}: var(--${prefixStr}color-${value === "neutral" ? "old-neutral" : value}-${shade}, ${getColor(value, shade)})')
  expect(light && dark, 'the colours plugin names the step of each role in light and dark').toBeTruthy()
  return { light: Number(light![1]), dark: Number(dark![1]) }
}

const SHADES = [50, 100, 200, 300, 400, 500, 600, 700, 800, 900, 950]

/** Tailwind's colour of a name and step, as the plugin's fallback (getColor), or '' for a name it does not know. */
function tailwindColour(name: string, n: number): string {
  const scale = (tailwind as unknown as Record<string, unknown>)[name]
  return scale && typeof scale === 'object' ? String((scale as Record<number, string>)[n] ?? '') : ''
}

/** Every custom property the page holds in `theme`: Nuxt UI's layer first, then main.css's unlayered rules over it. */
function themeVars(theme: Theme): Vars {
  const steps = pluginSteps()
  const scales = atDeclarations('@theme static')
  // The plugin's output: `--ui-color-<role>-<n>` from the named colour, with Tailwind's own colour of that name as the fallback (an
  // empty one for a name Tailwind does not know), and `--ui-<role>` at a step.
  const plugin: Vars = {}
  for (const [role, name] of Object.entries(ui.colors)) {
    for (const n of SHADES) plugin[`--ui-color-${role}-${n}`] = `var(--color-${name}-${n}, ${tailwindColour(name, n)})`
    if (role !== 'neutral') plugin[`--ui-${role}`] = `var(--ui-color-${role}-${theme === 'dark' ? steps.dark : steps.light})`
  }
  const nuxtTheme = nuxtCss.slice(nuxtCss.indexOf('@layer theme'))
  const layered = { ...scales, ...plugin, ...declarations(nuxtTheme, isRoot), ...(theme === 'dark' ? declarations(nuxtTheme, isDark) : {}) }
  const unlayered = mainCss.replace(atBlock(mainCss, '@theme static'), '{}').replace(atBlock(mainCss, '@theme inline'), '{}')
  return { ...layered, ...declarations(unlayered, isRoot), ...(theme === 'dark' ? declarations(unlayered, isDark) : {}) }
}

/** A value with every var() substituted (its fallback, which may hold parentheses, where the name is not set). */
function substitute(value: string, vars: Vars, depth = 0): string {
  if (depth > 20) throw new Error(`a var() loop at ${value}`)
  const at = value.indexOf('var(')
  if (at < 0) return value
  let level = 0
  let comma = -1
  let end = -1
  for (let i = at + 4; i < value.length && end < 0; i++) {
    const c = value[i]
    if (c === '(') level++
    else if (c === ')') level === 0 ? (end = i) : level--
    else if (c === ',' && level === 0 && comma < 0) comma = i
  }
  if (end < 0) throw new Error(`an unclosed var() in ${value}`)
  const name = value.slice(at + 4, comma < 0 ? end : comma).trim()
  const fallback = comma < 0 ? '' : value.slice(comma + 1, end).trim()
  const got = substitute(vars[name] ?? fallback, vars, depth + 1)
  return substitute(value.slice(0, at) + got + value.slice(end + 1), vars, depth + 1)
}

/** A value as a colour: color-mix(in srgb …) of two colours is mixed, anything else parsed; undefined when it is no colour. */
function colourOf(value: string): Rgba | undefined {
  const mix = value.match(/^color-mix\(in srgb,\s*(.+?)\s+(\d+(?:\.\d+)?)%,\s*(.+)\)$/)
  if (mix) {
    const a = colourOf(mix[1]!)
    const b = colourOf(mix[3]!)
    return a && b ? mixSrgb(a, b, Number(mix[2])) : undefined
  }
  return parseColor(value)
}

class Resolved {
  readonly vars: Vars
  constructor(readonly theme: Theme) {
    this.vars = themeVars(theme)
  }
  /** A token's colour; it fails, naming the token, when the token is unset or no colour. */
  token(name: string): Rgba {
    const v = substitute(`var(${name})`, this.vars)
    const c = colourOf(v)
    if (!c) throw new Error(`${this.theme}: ${name} resolves to no colour (${JSON.stringify(v)})`)
    return c
  }
  /** What a `text-<role>` class draws: main.css's `--text-color-<role>` (Tailwind reads it first) or Nuxt UI's `--ui-<role>`. */
  text(role: string): Rgba {
    const inline = atDeclarations('@theme inline')
    const mapped = inline[`--text-color-${role}`]
    return mapped ? this.token(mapped.match(/var\((--[\w-]+)\)/)![1]!) : this.token(`--ui-${role}`)
  }
}

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
