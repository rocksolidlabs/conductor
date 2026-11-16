import { parse as parseSfc } from 'vue/compiler-sfc'
import { describe, expect, it } from 'vitest'

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
})

// ---- The design system's rules (docs/design/system.md, PR-37): tooltips, ... ----

/** A template's node as the SFC parser gives it: an element (1), a text (2), a comment (3) or an interpolation (5). */
type TNode = { type: number; tag?: string; tagType?: number; props?: TProp[]; children?: TNode[]; content?: string | { content: string }; loc: { start: { line: number } } }
/** An element's attribute (6: `name="value"`) or directive (7: `v-name:arg="exp"`, `:arg` being `v-bind:arg`). */
type TProp = { type: number; name: string; value?: { content: string }; arg?: { content: string }; exp?: { content: string } }

/** The template of each .vue source, parsed. */
const templates: Record<string, TNode> = Object.fromEntries(
  Object.entries(sources)
    .filter(([path]) => path.endsWith('.vue'))
    .map(([path, text]) => [path, parseSfc(text, { filename: path }).descriptor.template?.ast as unknown as TNode])
    .filter(([, ast]) => ast),
)

/** The props an element has, by name: an attribute's value, a bound prop's expression (`:name`), a slot's name (`#name`). */
function propOf(el: TNode, name: string): { value: string; bound: boolean } | undefined {
  for (const p of el.props ?? []) {
    if (p.type === 6 && p.name === name) return { value: p.value?.content ?? '', bound: false }
    if (p.type === 7 && p.name === 'bind' && p.arg?.content === name) return { value: p.exp?.content ?? '', bound: true }
  }
  return undefined
}

/** The slot a `<template #name>` fills, or undefined for any other element. */
function slotOf(el: TNode): string | undefined {
  if (el.tag !== 'template') return undefined
  return el.props?.find((p) => p.type === 7 && p.name === 'slot')?.arg?.content ?? (el.props?.some((p) => p.type === 7 && p.name === 'slot') ? 'default' : undefined)
}

/** True for a class list that hides its element at some width (`hidden sm:inline`) or always (`sr-only`, `hidden`). */
function hiddenSomewhere(cls: string): boolean {
  const words = cls.split(/\s+/)
  return words.includes('sr-only') || words.some((w) => /^([a-z0-9]+:)*hidden$/.test(w))
}

/** True when the children draw a label a person can read at every width: text, an interpolation, or an element holding one. */
function visibleText(children: TNode[] = []): boolean {
  return children.some((c) => {
    if (c.type === 2) return typeof c.content === 'string' && c.content.trim() !== ''
    if (c.type === 5) return true
    if (c.type !== 1) return false
    if (c.tag === 'UIcon' || c.tag === 'UKbd') return false
    const slot = slotOf(c)
    if (slot && slot !== 'default') return false
    if (hiddenSomewhere(propOf(c, 'class')?.value ?? '')) return false
    return visibleText(c.children)
  })
}

/** Every element of a template with the tags of its ancestors, outermost first. */
function* elements(node: TNode, up: string[] = []): Generator<{ el: TNode; up: string[] }> {
  for (const c of node.children ?? []) {
    if (c.type !== 1) continue
    yield { el: c, up }
    yield* elements(c, [...up, c.tag!])
  }
}

/**
 * The tooltip rule: a UButton with an icon and no label a person can read at every width (no `label`, no text in its default
 * slot, or text only in a span hidden at some width) is inside a UTooltip; IconButton is the one component that draws one.
 * Returns each breaking button as `path:line`.
 */
function bareIconButtons(only: (path: string) => boolean = () => true, from: Record<string, TNode> = templates): string[] {
  const out: string[] = []
  for (const [path, ast] of Object.entries(from)) {
    if (!only(path)) continue
    for (const { el, up } of elements(ast)) {
      if (el.tag !== 'UButton') continue
      if (!propOf(el, 'icon') && !propOf(el, 'leading-icon') && !propOf(el, 'trailing-icon')) continue
      if (propOf(el, 'label') || visibleText(el.children)) continue
      if (up.includes('UTooltip')) continue
      out.push(`${path}:${el.loc.start.line}`)
    }
  }
  return out
}

/** A source's template parsed on its own, for the rules' samples. */
function sample(text: string): Record<string, TNode> {
  return { 'components/Sample.vue': parseSfc(text, { filename: 'Sample.vue' }).descriptor.template!.ast as unknown as TNode }
}

describe("the design system's rules", () => {
  it('give every icon-only button a tooltip: an IconButton, whose label is its tooltip and its name', () => {
    expect(Object.keys(templates).length).toBeGreaterThan(100)
    expect(bareIconButtons()).toEqual([])
  })

  it('see a bare icon button, and a label hidden at some width as none', () => {
    const found = bareIconButtons(undefined, sample(`<template><div>
      <UButton icon="i-lucide-x" aria-label="Close" />
      <UButton icon="i-lucide-share"><span class="hidden sm:inline">Share</span></UButton>
      <UButton icon="i-lucide-copy" label="Copy" />
      <UButton icon="i-lucide-copy">Copy {{ what }}</UButton>
      <UTooltip text="Stop"><UButton icon="i-lucide-square" aria-label="Stop" /></UTooltip>
      <IconButton icon="i-lucide-x" label="Close" />
    </div></template>`))
    expect(found).toEqual(['components/Sample.vue:2', 'components/Sample.vue:3'])
  })
})
