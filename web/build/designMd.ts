import { parseColor, toHex } from '../app/utils/contrast'
import { Resolved, SHADES, atDeclarations, declarations, isRoot, readWeb, type Theme } from './themeTokens'

// DESIGN.md at the root of the repository: the design system in the form outside design tools read (a YAML front matter of tokens,
// then sections of prose, the public DESIGN.md format). Every value is copied from the files that set it; nothing is decided here.
// The rules are docs/design/system.md and the brand is docs/design/brand.md: the prose points at them and says nothing of its own.
// `make design-md` writes the file; designMd.test.ts fails when the committed one is not what this makes.

/** The semantic tokens written for each theme, by their Nuxt UI name without `--ui-`. */
const TOKENS = [
  'bg',
  'bg-muted',
  'bg-elevated',
  'bg-accented',
  'bg-canvas',
  'bg-canvas-hover',
  'border',
  'border-accented',
  'text-highlighted',
  'text',
  'text-toned',
  'text-muted',
  'text-dimmed',
  'text-inverted',
  'primary',
  'secondary',
  'warning',
  'warning-text',
  'success',
  'success-text',
  'error',
  'info',
] as const

/** The brand's anchors, by their name in web/public/brand/tokens.css without `--conductor-`. */
const BRAND = ['forest', 'terracotta', 'sage'] as const

/** The Tailwind sizes written for each face. */
const SIZES = ['xs', 'sm', 'base'] as const

/** The app opens dark; light is a choice. Each theme's tokens are written in this order. */
const THEMES: Theme[] = ['dark', 'light']

/** A colour as written in a stylesheet, as lower-case hex; it fails, naming what it read, when the value is no colour. */
function colour(what: string, value: string | undefined): string {
  const c = parseColor(value)
  if (!c) throw new Error(`${what} is no colour (${JSON.stringify(value)})`)
  return toHex(c)
}

/** A YAML scalar: a JSON string is a YAML double-quoted one. */
const q = (s: string) => JSON.stringify(s)

/** A length in rem (`0.25rem`, `.25rem`) as px, at the 16 px root. */
function px(rem: string): string {
  const m = rem.trim().match(/^(\d*\.?\d+)rem$/)
  if (!m) throw new Error(`not a length in rem: ${rem}`)
  return `${Number((Number(m[1]) * 16).toFixed(2))}px`
}

/** A Tailwind size and its line height, from tailwindcss's theme.css (`--text-sm: 0.875rem`, `--text-sm--line-height: calc(1.25 / 0.875)`). */
function textSize(themeCss: string, size: string): { fontSize: string; lineHeight: string } {
  const s = themeCss.match(new RegExp(`--text-${size}:\\s*([\\d.]+rem);`))
  const l = themeCss.match(new RegExp(`--text-${size}--line-height:\\s*calc\\(([\\d.]+)\\s*/\\s*([\\d.]+)\\);`))
  if (!s || !l) throw new Error(`tailwindcss's theme.css no longer sets text-${size} and its line height as calc(a / b)`)
  const fontSize = px(s[1]!)
  const lineHeight = `${Number(((Number(l[1]) / Number(l[2])) * parseFloat(fontSize)).toFixed(2))}px`
  return { fontSize, lineHeight }
}

/** A rule heading's anchor as GitHub makes it: lower case, punctuation dropped, spaces as hyphens. */
export function anchor(heading: string): string {
  return heading
    .toLowerCase()
    .replace(/[^\p{L}\p{N}\s_-]/gu, '')
    .replace(/\s/g, '-')
}

/** The headings of docs/design/system.md's rules: each `###` under `## The rules`, as designRules.test.ts reads them. */
export function ruleHeadings(md: string): string[] {
  const part = md.split(/^## The rules\s*$/m)[1]?.split(/^## (?!#)/m)[0] ?? ''
  return part
    .split(/^### /m)
    .slice(1)
    .map((s) => s.split('\n')[0]!.trim())
}

/** The files DESIGN.md is made from, read from the disk; a test hands its own. */
export interface DesignSources {
  /** web/public/brand/tokens.css */
  tokensCss: string
  /** web/node_modules/tailwindcss/theme.css */
  tailwindCss: string
  /** docs/design/system.md */
  systemMd: string
  /** The tokens of each theme. */
  resolved: Record<Theme, Resolved>
  /** main.css's `@theme static` block: the scales and the two faces. */
  scales: Record<string, string>
}

export function designSources(): DesignSources {
  return {
    tokensCss: readWeb('public/brand/tokens.css'),
    tailwindCss: readWeb('node_modules/tailwindcss/theme.css'),
    systemMd: readWeb('../docs/design/system.md'),
    resolved: { dark: new Resolved('dark'), light: new Resolved('light') },
    scales: atDeclarations('@theme static'),
  }
}

/** DESIGN.md's text. */
export function designMd(src: DesignSources = designSources()): string {
  const brand = declarations(src.tokensCss.slice(0, src.tokensCss.indexOf('[data-conductor-theme')), isRoot)
  const hex = (theme: Theme, name: string) => toHex(src.resolved[theme].token(`--ui-${name}`))
  const fonts = { sans: src.scales['--font-sans'], mono: src.scales['--font-mono'] }
  if (!fonts.sans || !fonts.mono) throw new Error('main.css sets no --font-sans or --font-mono in its @theme static block')
  const radius = src.resolved.dark.vars['--ui-radius']
  if (!radius) throw new Error("Nuxt UI's stylesheet sets no --ui-radius")
  const panel = brand['--conductor-radius']
  if (!panel) throw new Error('tokens.css sets no --conductor-radius')

  const lines: string[] = [
    '---',
    '# Generated by `make design-md` (web/build/designMd.ts) from web/app/assets/css/main.css, web/app/app.config.ts,',
    "# Nuxt UI's theme, tailwindcss's theme.css, web/public/brand/tokens.css and docs/design/system.md.",
    '# Change those files, never this one. The app opens dark; light is a choice.',
    'name: Conductor',
    `description: ${q('Engineering intent, orchestrated.')}`,
    'colors:',
  ]
  for (const name of BRAND) {
    lines.push(`  ${name}: ${q(colour(`tokens.css's --conductor-${name}`, brand[`--conductor-${name}`]))}`)
  }
  // The ink scale's steps: every grey of every screen.
  for (const n of SHADES) {
    lines.push(`  ink-${n}: ${q(colour(`main.css's --color-ink-${n}`, src.scales[`--color-ink-${n}`]))}`)
  }
  for (const theme of THEMES) for (const name of TOKENS) lines.push(`  ${name}-${theme}: ${q(hex(theme, name))}`)

  lines.push('typography:')
  for (const [face, family] of Object.entries(fonts)) {
    for (const size of SIZES) {
      const t = textSize(src.tailwindCss, size)
      lines.push(`  ${face}-${size}:`, `    fontFamily: ${q(family!)}`, `    fontSize: ${q(t.fontSize)}`, `    lineHeight: ${q(t.lineHeight)}`)
    }
  }

  lines.push('rounded:', `  ui: ${q(px(radius))}`, `  panel: ${q(panel)}`, 'spacing:')
  for (const [name, v] of Object.entries(brand)) {
    const m = name.match(/^--conductor-space-(\d+)$/)
    if (m) lines.push(`  ${q(m[1]!)}: ${q(v)}`)
  }

  lines.push('components:')
  for (const theme of THEMES) {
    lines.push(
      `  page-${theme}:`,
      `    backgroundColor: ${q(`{colors.bg-${theme}}`)}`,
      `    textColor: ${q(`{colors.text-${theme}}`)}`,
      `    typography: ${q('{typography.sans-sm}')}`,
      `  sidebar-${theme}:`,
      `    backgroundColor: ${q(`{colors.bg-canvas-${theme}}`)}`,
      `    textColor: ${q(`{colors.text-${theme}}`)}`,
      `  button-primary-${theme}:`,
      `    backgroundColor: ${q(`{colors.primary-${theme}}`)}`,
      `    textColor: ${q(`{colors.text-inverted-${theme}}`)}`,
      `    typography: ${q('{typography.sans-sm}')}`,
      `    rounded: ${q('{rounded.ui}')}`,
    )
  }
  lines.push('---', '')

  const rule = (heading: string) => `[${heading}](docs/design/system.md#${anchor(heading)})`
  const headings = ruleHeadings(src.systemMd)
  const has = (h: string) => {
    if (!headings.includes(h)) throw new Error(`no rule "${h}" in docs/design/system.md: DESIGN.md points at it`)
    return rule(h)
  }

  lines.push(
    '# Design System: Conductor',
    '',
    'This file is generated: `make design-md` writes it from the files named at its top, and',
    '`npm --prefix web test` fails when it is out of date. Change those files, then run `make design-md`.',
    '',
    '## Overview',
    '',
    'Conductor runs coding agents in terminals and shows them in its apps, the desktop app and the phone.',
    'Its screens are a calm, dense workbench for people who use it all day, dark first.',
    'Who they are for and what comes first is [PRODUCT.md](PRODUCT.md). The rules every screen follows,',
    'each with the checks that hold it, are [docs/design/system.md](docs/design/system.md); the brand (its',
    'palette, mark and type) is [docs/design/brand.md](docs/design/brand.md). Where this file and those',
    'disagree, they are right and this file is out of date.',
    '',
    '## Colors',
    '',
    `Every grey is a step of the ink scale; forest is the one main action; terracotta is the mark's junction`,
    `and a restrained accent; amber is for what waits on a person; green is for a live agent. See`,
    `${has('Colour and contrast')}, ${has('Status')}, ${has('One main action')} and`,
    `${has('Amber when someone must act')}, and the brand's [palette](docs/design/brand.md#palette-and-semantic-colors).`,
    '',
    '## Typography',
    '',
    'Inter for sentences, names and labels; JetBrains Mono for ids, paths, commits, commands, keys, logs and',
    `measures: ${has('The code font for evidence')}. Sizes are Tailwind's; the app draws most text at`,
    '`text-xs` and `text-sm`.',
    '',
    '## Layout',
    '',
    "Nuxt UI's dashboard: a sidebar on the canvas, and panels whose header is a `UDashboardNavbar`. See",
    `${has('Who the screens are for')}, ${has('What stays in view')}, ${has('The page header')},`,
    `${has('The toolbar')}, ${has('The list row')} and ${has('Cutting text')}.`,
    '',
    '## Elevation & Depth',
    '',
    'Flat panels and quiet borders; no heavy shadow, glow or gradient ([the brand](docs/design/brand.md#typography-and-layout)).',
    '',
    '## Shapes',
    '',
    "Corners from Nuxt UI's radius; a state's mark is a shape as well as a colour, so colour is never",
    `alone: ${has('Status')}.`,
    '',
    '## Components',
    '',
    "Nuxt UI's components, and the app's own in `web/app/components/`: an icon-only button is an",
    `\`IconButton\` (${has('Tooltips')}); a state is a \`StatusMark\` (${has('Status')}).`,
    '',
    "## Do's and Don'ts",
    '',
    'Each rule of the design system, in its order there:',
    '',
    ...headings.map((h) => `- ${rule(h)}`),
    '- The junction of the mark is artwork, never a status light ([the brand](docs/design/brand.md#palette-and-semantic-colors)).',
    '',
  )
  return lines.join('\n')
}
