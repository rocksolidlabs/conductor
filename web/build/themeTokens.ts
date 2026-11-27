import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import tailwind from 'tailwindcss/colors'
import { mixSrgb, parseColor, type Rgba } from '../app/utils/contrast'

// The app's tokens, resolved in light and in dark the way the page resolves them, from the files that set them: main.css (the
// scales, the canvas, the text-safe amber and green), app.config.ts (the colour each role names), Nuxt UI's colours plugin (each
// role's steps, and the step each role takes in light and dark) and Nuxt UI's runtime stylesheet (which step is text, muted, dimmed,
// the page and the raised surfaces). The plugin and the stylesheet are read from node_modules, so an upgrade that remaps a token or
// a step is followed here, not hidden. The contrast test (app/utils/themeContrast.test.ts) and DESIGN.md's generator (designMd.ts)
// both read the tokens from here.

/** A file of web/ by its path from web/ (`app/assets/css/main.css`), read from the disk: vitest hands a stylesheet's `?raw` import over empty. */
export const readWeb = (rel: string) => readFileSync(fileURLToPath(new URL(`../${rel}`, import.meta.url)), 'utf8')

export const mainCss = readWeb('app/assets/css/main.css')
const nuxtCss = readWeb('node_modules/@nuxt/ui/dist/runtime/index.css')
const colorsPlugin = readWeb('node_modules/@nuxt/ui/dist/runtime/plugins/colors.js')

export type Vars = Record<string, string>
export type Theme = 'light' | 'dark'

/** The colour each role names in app.config.ts (`primary: 'forest'`), read from its text: the file calls Nuxt's defineAppConfig as it loads. */
export function appColors(text = readWeb('app/app.config.ts')): Record<string, string> {
  const block = text.match(/\bcolors:\s*\{([^}]*)\}/)
  if (!block) throw new Error('app.config.ts names no colors')
  return Object.fromEntries([...block[1]!.matchAll(/(\w+):\s*'([\w-]+)'/g)].map((m) => [m[1]!, m[2]!]))
}

/** The declarations of every rule whose selector list matches `pick`, in source order, from CSS text (comments removed). */
export function declarations(css: string, pick: (selectors: string[]) => boolean): Vars {
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
export const atDeclarations = (head: string) => declarations(`theme ${atBlock(mainCss, head)}`, () => true)

export const isRoot = (s: string[]) => s.includes(':root')
const isDark = (s: string[]) => s.length === 1 && s[0] === '.dark'

/** The steps the colours plugin gives every role in light and in dark, read from its source; it fails when the source changed shape. */
function pluginSteps(): { light: number; dark: number } {
  const writes = '--ui-color-${key}-${shade}: var(--${prefixStr}color-${value === "neutral" ? "old-neutral" : value}-${shade}, ${getColor(value, shade)})'
  if (!colorsPlugin.includes(writes)) throw new Error('the colours plugin no longer writes each step from the colour named in app.config.ts: read its source again')
  const light = colorsPlugin.match(/:root, :host, \.light \{[\s\S]*?generateColor\(key, (\d+)\)/)
  const dark = colorsPlugin.match(/\.dark \{[\s\S]*?generateColor\(key, (\d+)\)/)
  if (!light || !dark) throw new Error('the colours plugin no longer names the step of each role in light and dark: read its source again')
  return { light: Number(light[1]), dark: Number(dark[1]) }
}

export const SHADES = [50, 100, 200, 300, 400, 500, 600, 700, 800, 900, 950]

/** Tailwind's colour of a name and step, as the plugin's fallback (getColor), or '' for a name it does not know. */
export function tailwindColour(name: string, n: number): string {
  const scale = (tailwind as unknown as Record<string, unknown>)[name]
  return scale && typeof scale === 'object' ? String((scale as Record<number, string>)[n] ?? '') : ''
}

/** Every custom property the page holds in `theme`: Nuxt UI's layer first, then main.css's unlayered rules over it. */
function themeVars(theme: Theme, colors: Record<string, string>): Vars {
  const steps = pluginSteps()
  const scales = atDeclarations('@theme static')
  // The plugin's output: `--ui-color-<role>-<n>` from the named colour, with Tailwind's own colour of that name as the fallback (an
  // empty one for a name Tailwind does not know), and `--ui-<role>` at a step.
  const plugin: Vars = {}
  for (const [role, name] of Object.entries(colors)) {
    for (const n of SHADES) plugin[`--ui-color-${role}-${n}`] = `var(--color-${name}-${n}, ${tailwindColour(name, n)})`
    if (role !== 'neutral') plugin[`--ui-${role}`] = `var(--ui-color-${role}-${theme === 'dark' ? steps.dark : steps.light})`
  }
  const nuxtTheme = nuxtCss.slice(nuxtCss.indexOf('@layer theme'))
  const layered = { ...scales, ...plugin, ...declarations(nuxtTheme, isRoot), ...(theme === 'dark' ? declarations(nuxtTheme, isDark) : {}) }
  const unlayered = mainCss.replace(atBlock(mainCss, '@theme static'), '{}').replace(atBlock(mainCss, '@theme inline'), '{}')
  return { ...layered, ...declarations(unlayered, isRoot), ...(theme === 'dark' ? declarations(unlayered, isDark) : {}) }
}

/** A value with every var() substituted (its fallback, which may hold parentheses, where the name is not set). */
export function substitute(value: string, vars: Vars, depth = 0): string {
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
export function colourOf(value: string): Rgba | undefined {
  const mix = value.match(/^color-mix\(in srgb,\s*(.+?)\s+(\d+(?:\.\d+)?)%,\s*(.+)\)$/)
  if (mix) {
    const a = colourOf(mix[1]!)
    const b = colourOf(mix[3]!)
    return a && b ? mixSrgb(a, b, Number(mix[2])) : undefined
  }
  return parseColor(value)
}

/** The tokens of one theme, resolved. */
export class Resolved {
  readonly theme: Theme
  readonly vars: Vars
  constructor(theme: Theme, colors: Record<string, string> = appColors()) {
    this.theme = theme
    this.vars = themeVars(theme, colors)
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
