import { describe, expect, it } from 'vitest'
import { parse } from 'vue/compiler-sfc'
import MONO_COUNTS_JSON from './designRules.mono.json'

// The design rules read from the app's sources (the UX review's design system): each rule names what it holds and fails on the
// source that breaks it, with the count. Sources are every .ts and .vue of the app, read as text, by their path under app/ (a
// source beside this file comes as ./name); test files are left out.

const sources = Object.fromEntries(
  Object.entries(import.meta.glob(['../**/*.ts', '../**/*.vue', '!../**/*.test.ts'], { query: '?raw', import: 'default', eager: true }) as Record<string, string>).map(([path, text]) => [path.replace(/^\.\.\//, '').replace(/^\.\//, 'utils/'), text]),
)

/** Every source that matches `pattern` (global), with how many times, apart from the `allowed` lines (by their text). */
function breaking(pattern: RegExp, only: (path: string) => boolean = () => true, allowed: Record<string, RegExp> = {}): Record<string, number> {
  const out: Record<string, number> = {}
  for (const [path, text] of Object.entries(sources)) {
    if (!only(path)) continue
    const n = text
      .split('\n')
      .filter((line) => !allowed[path]?.test(line))
      .reduce((sum, line) => sum + (line.match(new RegExp(pattern.source, pattern.flags.includes('g') ? pattern.flags : `${pattern.flags}g`)) ?? []).length, 0)
    if (n > 0) out[path] = n
  }
  return out
}

// A template as `vue/compiler-sfc` parses it, untransformed: elements with their attributes (6) and directives (7) as written.
interface TNode {
  type: number
  tag?: string
  props?: TProp[]
  children?: TNode[]
  loc: { start: { line: number } }
}
interface TProp {
  type: number
  name: string
  value?: { content: string }
  arg?: { content?: string }
  exp?: { content?: string }
}

/** Every .vue source's template, parsed. */
const templates: Array<[string, TNode]> = Object.entries(sources)
  .filter(([path]) => path.endsWith('.vue'))
  .flatMap(([path, text]): Array<[string, TNode]> => {
    const ast = parse(text, { filename: path }).descriptor.template?.ast as unknown as TNode | undefined
    return ast ? [[path, ast]] : []
  })

const attr = (n: TNode, name: string) => n.props?.find((p) => p.type === 6 && p.name === name)
const bound = (n: TNode, name: string) => n.props?.find((p) => p.type === 7 && p.name === 'bind' && p.arg?.content === name)
const directive = (n: TNode, name: string) => n.props?.find((p) => p.type === 7 && p.name === name)
const elements = (n: TNode) => (n.children ?? []).filter((c) => c.type === 1)

/** What `data-mono` may name: the evidence the code font is for (the UX review's P16). */
const MONO_KINDS = ['id', 'path', 'commit', 'command', 'key', 'log', 'measure'] as const
/** Where the code font needs no mark: code, preformatted text and keys, and a field a person types into. */
const MONO_TAGS = /^(code|pre|kbd|UKbd|input|textarea|UInput|UTextarea|UInputNumber|UInputMenu|UInputTags|USelect|USelectMenu)$/

/**
 * The code font in each template that is not evidence, per source: `font-mono` in an element's class (as written or bound), unless
 * the element is code, pre, a key or a field, or carries `data-mono` (bound, or one of MONO_KINDS). Also each `data-mono` that names
 * no kind, with its line.
 */
function monoUses(): { counts: Record<string, number>; badKinds: string[] } {
  const counts: Record<string, number> = {}
  const badKinds: string[] = []
  const count = (s: string | undefined) => (s?.match(/\bfont-mono\b/g) ?? []).length
  for (const [path, root] of templates) {
    const visit = (n: TNode) => {
      if (n.type === 1) {
        const kind = attr(n, 'data-mono')
        if (kind && !(MONO_KINDS as readonly string[]).includes(kind.value?.content ?? '')) badKinds.push(`${path}:${n.loc.start.line} data-mono="${kind.value?.content ?? ''}"`)
        const marked = !!kind || !!bound(n, 'data-mono') || MONO_TAGS.test(n.tag ?? '')
        const uses = count(attr(n, 'class')?.value?.content) + count(bound(n, 'class')?.exp?.content)
        if (uses && !marked) counts[path] = (counts[path] ?? 0) + uses
      }
      for (const c of n.children ?? []) visit(c)
    }
    visit(root)
  }
  return { counts, badKinds }
}

/** A `UButton` drawn solid in the primary colour: no variant or `solid`, no colour or `primary`. A bound variant or colour, or a
 *  `v-bind` of an object, is not judged: the template does not say. */
function solidPrimary(n: TNode): boolean {
  if (n.tag !== 'UButton') return false
  if (bound(n, 'variant') || bound(n, 'color') || n.props?.some((p) => p.type === 7 && p.name === 'bind' && !p.arg)) return false
  const variant = attr(n, 'variant')?.value?.content
  const color = attr(n, 'color')?.value?.content
  return (variant === undefined || variant === 'solid') && (color === undefined || color === 'primary')
}

/** A menu's or popover's content slot: drawn over the page as an area of its own, so not counted with the area that opens it. */
const overlay = (parent: TNode, c: TNode) =>
  /^U(Popover|DropdownMenu|ContextMenu)$/.test(parent.tag ?? '') && c.tag === 'template' && c.props?.some((p) => p.type === 7 && p.name === 'slot' && p.arg?.content === 'content')

/**
 * How many solid primary buttons `n` can draw at once: a `v-if` chain's branches are counted apart, its largest kept; a field group
 * (a split button: the action and its chevron) is one button; a menu's or popover's content is not counted.
 */
function filledAtOnce(n: TNode): number {
  if (/^U(FieldGroup|ButtonGroup)$/.test(n.tag ?? '')) return Math.min(1, elements(n).reduce((sum, c) => sum + filledAtOnce(c), 0))
  let sum = solidPrimary(n) ? 1 : 0
  let chain: number | undefined
  for (const c of elements(n)) {
    if (overlay(n, c)) continue
    const here = filledAtOnce(c)
    if (directive(c, 'else-if') || directive(c, 'else')) {
      chain = Math.max(chain ?? 0, here)
      continue
    }
    if (chain !== undefined) sum += chain
    chain = directive(c, 'if') ? here : undefined
    if (chain === undefined) sum += here
  }
  return sum + (chain ?? 0)
}

/** Each navbar and dialog of every template that can draw more than one solid primary button at once, with how many. */
function areasWithTwoFilled(): Record<string, number> {
  const out: Record<string, number> = {}
  for (const [path, root] of templates) {
    const visit = (n: TNode) => {
      if (n.type === 1 && (n.tag === 'UDashboardNavbar' || n.tag === 'UModal')) {
        const filled = filledAtOnce(n)
        if (filled > 1) out[`${path}:${n.loc.start.line} ${n.tag}`] = filled
      }
      for (const c of n.children ?? []) visit(c)
    }
    visit(root)
  }
  return out
}

/** The unmarked uses of the code font each source may still have (designRules.mono.json): a list that only shrinks. */
const MONO_COUNTS: Record<string, number> = MONO_COUNTS_JSON

/** Tailwind's zinc, the eleven steps, as hex. */
const ZINC = ['#09090b', '#18181b', '#27272a', '#3f3f46', '#52525b', '#71717a', '#a1a1aa', '#d4d4d8', '#e4e4e7', '#f4f4f5', '#fafafa']

describe('the design rules', () => {
  it('read the sources', () => {
    expect(Object.keys(sources).length).toBeGreaterThan(100)
  })

  it("draw greys from the brand's ink scale, never from zinc", () => {
    // One place keeps a zinc class: a ticket label's colour named "gray" (Dispatch's label palette).
    const allowed = { 'utils/dispatch.ts': /^\s*gray: 'bg-zinc-400',$/ }
    expect(breaking(new RegExp(`\\bzinc\\b|${ZINC.join('|')}`, 'i'), () => true, allowed)).toEqual({})
  })

  it("hover the sidebar's rows to the canvas's hover tone, which a raised tone would vanish into on the light canvas", () => {
    const sidebar = (path: string) => /^components\/(Sidebar[^/]*|SessionSidebar)\.vue$/.test(path)
    expect(Object.keys(sources).filter(sidebar).length).toBeGreaterThan(5)
    expect(breaking(/hover:bg-elevated\b/, sidebar)).toEqual({})
  })

  it('set sentences in the app\'s face and keep the code font for evidence, marked with what it is (a count per source that only shrinks)', () => {
    expect(templates.length).toBeGreaterThan(100)
    const { counts, badKinds } = monoUses()
    expect(badKinds, `data-mono names one of ${MONO_KINDS.join(', ')}`).toEqual([])
    const grown = Object.entries(counts).filter(([path, n]) => n > (MONO_COUNTS[path] ?? 0))
    expect(
      Object.fromEntries(grown),
      'the code font on words that are not evidence: set them in the app\'s face, or mark an id, path, commit, command, key, log or measure with data-mono="<kind>"',
    ).toEqual(Object.fromEntries(grown.map(([path]) => [path, MONO_COUNTS[path] ?? 0])))
    const fallen = Object.entries(MONO_COUNTS).filter(([path, n]) => (counts[path] ?? 0) < n)
    expect(Object.fromEntries(fallen), 'fewer unmarked uses than designRules.mono.json allows: lower its entry (the list only shrinks)').toEqual(
      Object.fromEntries(fallen.map(([path]) => [path, counts[path] ?? 0])),
    )
  })

  it('hold one filled button in each navbar and each dialog, the main action; the branches of a v-if chain are counted apart', () => {
    expect(areasWithTwoFilled()).toEqual({})
  })
})

describe('the design rules judge what they are shown', () => {
  const root = (template: string) => parse(`<template>${template}</template>`).descriptor.template!.ast as unknown as TNode
  it('count solid primary buttons, a v-if chain once, a bound variant never', () => {
    expect(filledAtOnce(root('<UModal><UButton label="Copy" /><UButton label="Done" /></UModal>'))).toBe(2)
    expect(filledAtOnce(root('<UModal><UButton label="Copy" color="neutral" variant="outline" /><UButton label="Done" color="primary" /></UModal>'))).toBe(1)
    expect(filledAtOnce(root('<UModal><UButton v-if="a" label="One" /><UButton v-else-if="b" label="Two" /><div v-else><UButton label="Three" /></div></UModal>'))).toBe(1)
    expect(filledAtOnce(root('<UModal><UButton v-if="a" label="One" /><UButton v-if="b" label="Two" /></UModal>'))).toBe(2)
    expect(filledAtOnce(root('<UModal><UButton :variant="v" label="One" /><UButton v-bind="look" label="Two" /><UButton variant="solid" label="Three" /></UModal>'))).toBe(1)
    expect(filledAtOnce(root('<UModal><UButton color="warning" label="Save mine" /><UButton variant="soft" label="Soft" /></UModal>'))).toBe(0)
    const split = '<UFieldGroup><UButton label="Launch" /><UPopover><UButton icon="i-lucide-chevron-down" /><template #content><UButton label="Launch named" /></template></UPopover></UFieldGroup>'
    expect(filledAtOnce(root(`<UDashboardNavbar>${split}</UDashboardNavbar>`))).toBe(1)
    expect(filledAtOnce(root(`<UDashboardNavbar>${split}<UButton label="Save" /></UDashboardNavbar>`))).toBe(2)
  })
})
