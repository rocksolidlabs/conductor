import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { parse as parseSfc } from 'vue/compiler-sfc'
import { describe, expect, it } from 'vitest'
import { CONTRAST_FLOORS } from './contrast'
import MONO_COUNTS_JSON from './designRules.mono.json'
import { STATUS_KEYS, STATUS_LOOK, TASK_KEYS, TASK_LOOK, finishedWord, type StatusLook } from './status'

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

/**
 * True for a class list that hides its element at some width or always: `hidden`, `sr-only`, and either behind any variant
 * (`hidden sm:inline`, `max-sm:sr-only`, `max-lg:hidden`, `pointer-coarse:hidden`).
 */
function hiddenSomewhere(cls: string): boolean {
  return cls.split(/\s+/).some((w) => /^([\w-]+:)*(hidden|sr-only)$/.test(w))
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

/** The exported string constants of utils/ (`export const INBOX_ROW_CLASS = '…'`), by name: what a `:class` may name. */
const CONSTANTS: Record<string, string> = Object.fromEntries(
  Object.entries(sources)
    .filter(([path]) => path.startsWith('utils/') && path.endsWith('.ts'))
    .flatMap(([, text]) => [...text.matchAll(/^export const ([A-Z][A-Z0-9_]*) = (?:'([^'\n]*)'|"([^"\n]*)"|`([^`$\n]*)`)\s*$/gm)].map((m) => [m[1]!, m[2] ?? m[3] ?? m[4] ?? ''])),
)

/**
 * The classes an element may carry: its `class` and, of a bound `:class`, every string written in it and every constant of
 * utils/ it names (an expression's branches all counted, as either may be drawn). The words without a variant (`sm:`, `hover:`)
 * alone, since the rule reads the layout every width starts from.
 */
function classesOf(el: TNode, constants: Record<string, string> = CONSTANTS): Set<string> {
  const words: string[] = []
  for (const p of el.props ?? []) {
    if (p.type === 6 && p.name === 'class') words.push(...(p.value?.content ?? '').split(/\s+/))
    if (p.type === 7 && p.name === 'bind' && p.arg?.content === 'class') {
      const exp = p.exp?.content ?? ''
      for (const m of exp.matchAll(/'([^']*)'|"([^"]*)"|`([^`]*)`/g)) words.push(...(m[1] ?? m[2] ?? m[3] ?? '').split(/\s+/))
      for (const m of exp.matchAll(/\b[A-Z][A-Z0-9_]*\b/g)) if (constants[m[0]] !== undefined) words.push(...constants[m[0]]!.split(/\s+/))
    }
  }
  return new Set(words.filter((w) => w && !w.includes(':')))
}

/** True for an element of the app's own HTML (a tag in lower case), whose classes say its layout; NuxtLink and ULink draw an `a`. */
function htmlOf(el: TNode): boolean {
  return /^[a-z][a-z0-9]*$/.test(el.tag ?? '') || el.tag === 'NuxtLink' || el.tag === 'ULink'
}

/** A width set on the element itself, which bounds a cut inside it: `w-48`, `w-[12rem]`, `max-w-56`, `basis-40`; never `w-auto`, `w-fit`. */
function ownWidth(cls: Set<string>): boolean {
  return [...cls].some((w) => /^(w|max-w|basis)-(\d|\[|px|xs|sm|md|lg|xl|\dxl|prose)/.test(w))
}

/**
 * The cut rule (docs/design/system.md, Cutting text): for each element with `truncate` or `line-clamp-*`, the path up its
 * template. In a flex column whose items are not stretched (`items-start`, `-center`, `-end`, `-baseline`, or a `<button>` with no
 * `items-stretch`, whose browser style starts its items at the start), the line that holds the cut takes `self-stretch` or `w-full`
 * (or a width of its own). In a flex row, a child that holds the cut deeper inside it takes `min-w-0` (or a width of its own); the
 * cut element itself needs none. The walk stops at a width of its own and at a component (its layout is in another template).
 * Returns the breaking places of each file, by count.
 */
function badCuts(from: Record<string, TNode> = templates, constants: Record<string, string> = CONSTANTS): Record<string, number> {
  const out: Record<string, number> = {}
  for (const [path, ast] of Object.entries(from)) {
    let n = 0
    const walk = (node: TNode, up: TNode[]) => {
      for (const c of node.children ?? []) {
        if (c.type !== 1) continue
        // A <template> (a v-if, a v-for, a slot's content) and a <slot> draw no box: their children sit in their parent's.
        const box = c.tag !== 'template' && c.tag !== 'slot'
        const chain = box ? [...up, c] : up
        if (box && [...classesOf(c, constants)].some((w) => w === 'truncate' || /^line-clamp-\d+$/.test(w))) n += cutBreaks(chain, constants)
        walk(c, chain)
      }
    }
    walk(ast, [])
    if (n > 0) out[path] = n
  }
  return out
}

/** How many of the rule's steps the cut at the end of `chain` (outermost first) breaks: 0 or 1, the first break counted. */
function cutBreaks(chain: TNode[], constants: Record<string, string>): number {
  const cut = chain[chain.length - 1]!
  for (let i = chain.length - 1; i > 0; i--) {
    const child = chain[i]!
    const parent = chain[i - 1]!
    const mine = classesOf(child, constants)
    if (ownWidth(mine)) return 0
    if (!htmlOf(parent)) return 0
    const theirs = classesOf(parent, constants)
    const flex = theirs.has('flex') || theirs.has('inline-flex')
    if (!flex) continue
    if (theirs.has('flex-col') || theirs.has('flex-col-reverse')) {
      const unstretched = !theirs.has('items-stretch') && (['items-start', 'items-center', 'items-end', 'items-baseline'].some((w) => theirs.has(w)) || parent.tag === 'button')
      if (unstretched && !mine.has('self-stretch') && !mine.has('w-full')) return 1
    } else if (child !== cut && !mine.has('min-w-0')) return 1
  }
  return 0
}

/** A source's template parsed on its own, for the rules' samples. */
function sample(text: string): Record<string, TNode> {
  return { 'components/Sample.vue': parseSfc(text, { filename: 'Sample.vue' }).descriptor.template!.ast as unknown as TNode }
}

/**
 * The neutral ghost buttons that sit on the canvas itself (the rail's, the sidebar header's, the rail's Expand in the layout), as
 * `path:line` when one would hover to Nuxt UI's raised tone: bg-elevated is the light canvas itself, so its hover would show nothing.
 * Each carries the canvas's hover tone (hover:bg-canvas-hover) instead. A row's buttons sit on the row, which hovers to that tone, and
 * keep the raised one.
 */
function canvasGhosts(from: Record<string, TNode> = templates): { found: number; breaking: string[] } {
  const onCanvas = (path: string) => /^components\/(SidebarRail|SidebarHeaderMenus)\.vue$/.test(path) || path === 'layouts/default.vue'
  let found = 0
  const breaking: string[] = []
  for (const [path, ast] of Object.entries(from)) {
    if (!onCanvas(path)) continue
    for (const { el } of elements(ast)) {
      if (el.tag !== 'UButton' && el.tag !== 'IconButton') continue
      if (propOf(el, 'variant')?.value !== 'ghost' || propOf(el, 'color')?.value !== 'neutral') continue
      // In the layout, the rail's own button alone: the rest of it is not on the canvas.
      if (path === 'layouts/default.vue' && !propOf(el, 'data-rail-expand')) continue
      found++
      if (!/(^|\s)hover:bg-canvas-hover(\s|$)/.test(propOf(el, 'class')?.value ?? '')) breaking.push(`${path}:${el.loc.start.line}`)
    }
  }
  return { found, breaking }
}

describe("the canvas's controls", () => {
  it("hover the rail's and the sidebar header's ghost buttons to the canvas's hover tone, never to the raised tone the light canvas is", () => {
    const { found, breaking } = canvasGhosts()
    expect(found).toBeGreaterThanOrEqual(3)
    expect(breaking).toEqual([])
  })

  it('see a ghost button on the canvas left with the raised hover', () => {
    const sample = parseSfc('<template><UButton icon="i-lucide-search" color="neutral" variant="ghost" /><UButton color="neutral" variant="ghost" class="hover:bg-canvas-hover" /></template>', { filename: 'x.vue' }).descriptor.template!.ast as unknown as TNode
    expect(canvasGhosts({ 'components/SidebarRail.vue': sample })).toEqual({ found: 2, breaking: ['components/SidebarRail.vue:1'] })
  })
})

describe("the design system's rules", () => {
  it('give every icon-only button a tooltip: an IconButton, whose label is its tooltip and its name', () => {
    expect(Object.keys(templates).length).toBeGreaterThan(100)
    expect(bareIconButtons()).toEqual([])
  })

  it('see a bare icon button, and a label hidden at some width as none', () => {
    const found = bareIconButtons(undefined, sample(`<template><div>
      <UButton icon="i-lucide-x" aria-label="Close" />
      <UButton icon="i-lucide-share"><span class="hidden sm:inline">Share</span></UButton>
      <UButton icon="i-lucide-plus"><span class="max-sm:sr-only">Add agent</span></UButton>
      <UButton icon="i-lucide-x"><span class="max-lg:hidden">Close</span></UButton>
      <UButton icon="i-lucide-copy" label="Copy" />
      <UButton icon="i-lucide-copy">Copy {{ what }}</UButton>
      <UTooltip text="Stop"><UButton icon="i-lucide-square" aria-label="Stop" /></UTooltip>
      <IconButton icon="i-lucide-x" label="Close" />
    </div></template>`))
    expect(found).toEqual(['components/Sample.vue:2', 'components/Sample.vue:3', 'components/Sample.vue:4', 'components/Sample.vue:5'])
  })

  it('cut a text only inside a box whose width comes from outside it, no new place breaking the rule', () => {
    expect(badCuts()).toEqual(CUTS_KNOWN)
  })

  it("see a cut whose column does not stretch it, as the Needs you page's rows did before #912", () => {
    const loose = { ...CONSTANTS, INBOX_ROW_CLASS: CONSTANTS.INBOX_ROW_CLASS!.replace(/\bitems-stretch\b/, '') }
    expect(CONSTANTS.INBOX_ROW_CLASS).toMatch(/\bitems-stretch\b/)
    const now = badCuts()
    const without = badCuts(templates, loose)
    for (const path of ['pages/decisions.vue', 'components/JoinDecide.vue']) expect(without[path] ?? 0, path).toBeGreaterThan(now[path] ?? 0)
  })

  it('see a wrapper in a flex row with no min-w-0, and pass a cut element with none of its own', () => {
    expect(
      badCuts(
        sample(`<template><div>
          <div class="flex items-center gap-2"><span class="truncate">a name</span><span>3</span></div>
          <div class="flex items-center gap-2"><div class="flex flex-col"><span class="truncate">a wrapped name</span></div></div>
          <div class="flex items-center gap-2"><div class="flex min-w-0 flex-col"><span class="truncate">a wrapped name</span></div></div>
          <button class="flex flex-col"><span class="truncate">a title</span></button>
          <button class="flex flex-col"><span class="w-full truncate">a title</span></button>
          <div class="flex flex-col items-start"><p class="line-clamp-2">a question</p></div>
          <div class="flex flex-col"><p class="line-clamp-2">a question</p></div>
        </div></template>`),
      ),
    ).toEqual({ 'components/Sample.vue': 3 })
  })
})

// ---- The design system's file and the code agree (docs/design/system.md) ----

/** docs/design/system.md, read from the repository as tasks.test.ts reads a file outside web/. */
const SYSTEM_MD = readFileSync(fileURLToPath(new URL('../../../docs/design/system.md', import.meta.url)), 'utf8')

/** The rules of a design-system file: each `###` heading under `## The rules`, with the text up to the next heading. */
function rulesOf(md: string): { heading: string; body: string }[] {
  const part = md.split(/^## The rules\s*$/m)[1]?.split(/^## (?!#)/m)[0] ?? ''
  return part
    .split(/^### /m)
    .slice(1)
    .map((s) => ({ heading: s.split('\n')[0]!.trim(), body: s.slice(s.indexOf('\n') + 1) }))
}

const DECIDED = /^Decided (\d{4}-\d{2}-\d{2}) by the maintainers, #(\d+)\.$/
const DESCRIBED = /^Described, not decided\.$/

/** Each rule's mark: "Decided …" or "Described, not decided."; a rule with none, or with more than one, is named with what it has. */
function ruleMarks(md: string): Record<string, string> {
  return Object.fromEntries(
    rulesOf(md).map(({ heading, body }) => {
      const marks = body.split('\n').filter((l) => DECIDED.test(l.trim()) || DESCRIBED.test(l.trim()))
      return [heading, marks.length === 1 ? marks[0]!.trim() : marks.length === 0 ? 'no mark' : `${marks.length} marks`]
    }),
  )
}

/** The rows of the first table in a text, as cells (the header and its rule left out). */
function tableOf(text: string): string[][] {
  const rows = text.split('\n').filter((l) => l.trim().startsWith('|'))
  return rows.slice(2).map((l) => l.trim().replace(/^\||\|$/g, '').split('|').map((c) => c.trim()))
}

/** A rule's text by its heading. */
function ruleText(md: string, heading: string): string {
  const r = rulesOf(md).find((x) => x.heading === heading)
  if (!r) throw new Error(`no rule "${heading}" in docs/design/system.md`)
  return r.body
}

const TONE_WORD: Record<StatusLook['tone'], string> = { warning: 'amber', success: 'green', error: 'red', neutral: 'grey' }

/** The words the table gives a mark, from its shape and tone: "amber dot", "green dot in a green ring", "dashed grey ring". */
function markWords(look: StatusLook): RegExp {
  const c = TONE_WORD[look.tone]
  const words: Record<StatusLook['shape'], string> = {
    dot: `(plain )?${c} dot`,
    'ringed-dot': `${c} dot in a ${c} ring`,
    ring: `${c} ring`,
    'dashed-ring': `dashed ${c} ring`,
    square: `hollow ${c} square`,
    check: `check in ${look.tone === 'neutral' ? 'neutral ink' : c}`,
    cross: `${c} cross`,
    dash: `${c} dash`,
  }
  return new RegExp(`^${words[look.shape]}$`)
}

/** A legend line ("working: the agent is in a turn; …") as the table's Means says it: "The agent is in a turn. …". */
function meansOf(legend: string): string {
  const said = legend.slice(legend.indexOf(': ') + 2)
  return said
    .split('; ')
    .map((s) => s.charAt(0).toUpperCase() + s.slice(1))
    .join('. ')
    .concat('.')
}

/** What the Status table says against what STATUS_LOOK holds, key by key: the differences, as lines. */
function statusTableDrift(md: string): string[] {
  const rows = new Map(tableOf(ruleText(md, 'Status')).map((r) => [r[1]!.replace(/`/g, ''), r]))
  const out: string[] = []
  for (const key of STATUS_KEYS) {
    const row = rows.get(key)
    const look = STATUS_LOOK[key]
    if (!row) {
      out.push(`${key}: no row`)
      continue
    }
    const [, , mark, word, means] = row
    if (!markWords(look).test(mark!)) out.push(`${key}: mark "${mark}", the code's ${look.tone} ${look.shape}`)
    const wordWanted = key === 'finished' ? finishedWord(new Date(Date.now() - 12 * 60_000).toISOString(), Date.now()) : look.word
    if (word !== wordWanted) out.push(`${key}: word "${word}", the code's "${wordWanted}"`)
    if (means !== meansOf(look.legend)) out.push(`${key}: means "${means}", the code's legend "${meansOf(look.legend)}"`)
  }
  for (const k of rows.keys()) if (!(STATUS_KEYS as readonly string[]).includes(k)) out.push(`${k}: a row the code has no key for`)
  const tasks = new Map(tableOf(ruleText(md, 'Runs and tasks')).map((r) => [r[1]!.replace(/`/g, ''), r]))
  for (const key of TASK_KEYS) {
    const row = tasks.get(key)
    if (!row) out.push(`${key}: no row in Runs and tasks`)
    else if (!markWords(TASK_LOOK[key]).test(row[2]!)) out.push(`${key}: mark "${row[2]}", the code's ${TASK_LOOK[key].tone} ${TASK_LOOK[key].shape}`)
    else if (!row[3]!.startsWith(TASK_LOOK[key].word)) out.push(`${key}: word "${row[3]}", the code's "${TASK_LOOK[key].word}"`)
  }
  return out
}

/** The floors the Colour and contrast rule's table gives, in CONTRAST_FLOORS' shape. */
function floorsOf(md: string): unknown {
  const rows = new Map(tableOf(ruleText(md, 'Colour and contrast')).map((r) => [r[0]!.split(' (')[0]!, r]))
  const ratio = (name: string) => Number(rows.get(name)?.[1]?.replace(/:1$/, ''))
  const large = rows.get('Large text')?.[0] ?? ''
  const nums = [...large.matchAll(/(\d+(?:\.\d+)?)/g)].map((m) => Number(m[1]))
  return {
    text: ratio('Text'),
    large: ratio('Large text'),
    dimmed: { page: ratio('Dimmed text on the page'), raised: ratio('Dimmed text on the canvas and the raised surfaces') },
    largeText: { px: nums[0], boldPx: nums[1], boldWeight: nums[2] },
  }
}

/** The state names a source may map to a status colour: the five's own words and the server's, a run's and a task's states. */
const STATE_NAMES = 'running|idle|exited|stopped|needs_input|working|done|finished|starting|host_disconnected|succeeded|failed|cancelled|interrupted|paused|waiting|ready|blocked|checking|pending'
/** The status colours: the fills and the words of amber and green, and the grey dot's fill. */
const STATUS_COLOURS = '(?:bg-success|bg-warning|bg-neutral-400|text-success|text-warning)\\b'
/** A line that maps a state's name to a status colour: `{ running: 'bg-success' }`, `x === 'running' ? 'text-success'`. */
const STATUS_MAP = new RegExp(`(?:\\b(?:${STATE_NAMES})\\b['"]?\\s*[:?]|['"](?:${STATE_NAMES})['"]\\s*(?:\\)|\\]|\\?|:|,|&&|\\|\\|))[^\\n]*?${STATUS_COLOURS}|===?\\s*['"](?:${STATE_NAMES})['"][^\\n]*${STATUS_COLOURS}`)

/** The sources still mapping a state to a colour themselves, by their count of such lines, each with why it stays. Only shrinks. */
const STATUS_MAP_KNOWN: Record<string, { lines: number; why: string }> = {
  'components/AttentionBar.vue': { lines: 2, why: "the Yard's bar: a fill for each of its segments, by StatusSlice, until the bar draws its tones from STATUS_LOOK" },
  'components/ChatThread.vue': { lines: 1, why: "not a status: a chat post that failed to send says so in amber, a read that failed (Amber when someone must act)" },
  'components/EventFeed.vue': { lines: 1, why: "the Events feed: a row that asks is tinted amber, until History's feed redraws it (PR-29)" },
  'utils/crews.ts': { lines: 2, why: "joinTileStatus: a run link's member tile, from what its terminal last reported, which sessionStatus does not read yet" },
}

/** Every source but utils/status.ts with lines that map a state's name to a status colour, by count. */
function statusMaps(from: Record<string, string> = sources): Record<string, number> {
  const out: Record<string, number> = {}
  for (const [path, text] of Object.entries(from)) {
    if (path === 'utils/status.ts') continue
    const n = text.split('\n').filter((l) => STATUS_MAP.test(l)).length
    if (n > 0) out[path] = n
  }
  return out
}

describe('docs/design/system.md and the code', () => {
  it('mark every rule: Decided, with the date and the issue, or Described', () => {
    const marks = ruleMarks(SYSTEM_MD)
    expect(Object.keys(marks).length).toBeGreaterThanOrEqual(10)
    expect(Object.entries(marks).filter(([, m]) => m === 'no mark' || m.endsWith('marks'))).toEqual([])
  })

  it('see a rule with no mark', () => {
    const md = `# x\n\n## The rules\n\n### One\n\nDecided 2026-11-15 by the maintainers, #374.\n\nText.\n\n### Two\n\nText alone.\n`
    expect(ruleMarks(md)).toEqual({ One: 'Decided 2026-11-15 by the maintainers, #374.', Two: 'no mark' })
  })

  it("give the Status table the code's marks, words and legends, key by key", () => {
    expect(statusTableDrift(SYSTEM_MD)).toEqual([])
  })

  it('see a word changed in the table alone', () => {
    const md = SYSTEM_MD.replace(/\| `finished` \| grey ring \| finished 12m ago \|/, '| `finished` | grey ring | done 12m ago |')
    expect(md).not.toBe(SYSTEM_MD)
    expect(statusTableDrift(md)).toEqual(['finished: word "done 12m ago", the code\'s "finished 12m ago"'])
  })

  it("give the floors' table the floors the contrast tests judge by", () => {
    expect(floorsOf(SYSTEM_MD)).toEqual(CONTRAST_FLOORS)
    expect(floorsOf(SYSTEM_MD.replace('| Text | 4.5:1 |', '| Text | 4:1 |'))).not.toEqual(CONTRAST_FLOORS)
  })

  it('map a state to a status colour in utils/status.ts alone, the places that still do so each with its reason', () => {
    expect(statusMaps()).toEqual(Object.fromEntries(Object.entries(STATUS_MAP_KNOWN).map(([p, k]) => [p, k.lines])))
  })

  it('see a new map of a state to a status colour', () => {
    expect(statusMaps({ 'components/Sample.vue': "const tone = { running: 'bg-success', exited: 'bg-neutral-400' }\nconst w = s === 'needs_input' ? 'text-warning' : ''" })).toEqual({ 'components/Sample.vue': 2 })
  })
})

/**
 * The cuts the rule sees breaking it, by file, as the rule found them when it was written (the UX review's PR-37). It sees one
 * template at a time, so a cut whose width a parent component sets may be counted here and be whole on the page; each file's count
 * only falls. A file fixed, or a count that fell, is taken out or lowered here in the same change.
 */
const CUTS_KNOWN: Record<string, number> = {}

// ---- The main action, the code font and amber (docs/design/system.md; the UX review's PR-26) ----

const attr = (n: TNode, name: string) => n.props?.find((p) => p.type === 6 && p.name === name)
const bound = (n: TNode, name: string) => n.props?.find((p) => p.type === 7 && p.name === 'bind' && p.arg?.content === name)
const directive = (n: TNode, name: string) => n.props?.find((p) => p.type === 7 && p.name === name)
const childElements = (n: TNode) => (n.children ?? []).filter((c) => c.type === 1)

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
  for (const [path, root] of Object.entries(templates)) {
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
  if (/^U(FieldGroup|ButtonGroup)$/.test(n.tag ?? '')) return Math.min(1, childElements(n).reduce((sum, c) => sum + filledAtOnce(c), 0))
  let sum = solidPrimary(n) ? 1 : 0
  let chain: number | undefined
  for (const c of childElements(n)) {
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
  for (const [path, root] of Object.entries(templates)) {
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

describe('the main action and the code font', () => {
  it('set sentences in the app\'s face and keep the code font for evidence, marked with what it is (a count per source that only shrinks)', () => {
    expect(Object.keys(templates).length).toBeGreaterThan(100)
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
  const root = (template: string) => parseSfc(`<template>${template}</template>`).descriptor.template!.ast as unknown as TNode
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

// ---- What stays in view, and the words (docs/design/system.md, #1158) ----

/** A fold: a part that hides what it holds until a person opens it. */
const FOLD = /^(UCollapsible|UAccordion)$/

/** Per source, the controls bound to a Yolo value (`v-model="yolo"`, `:model-value="crew.yolo"`) that sit inside a fold. */
function foldedYolo(from: Record<string, TNode> = templates): Record<string, number> {
  const out: Record<string, number> = {}
  for (const [path, ast] of Object.entries(from)) {
    let n = 0
    for (const { el, up } of elements(ast)) {
      if (up.some((tag) => FOLD.test(tag)) && (el.props ?? []).some((p) => p.type === 7 && /\byolo\b/i.test(p.exp?.content ?? ''))) n++
    }
    if (n > 0) out[path] = n
  }
  return out
}

/** The settings page and each component its template draws (a tag that is a file of components/), by the folds each holds. */
function settingsFolds(from: Record<string, TNode> = templates): Record<string, number> {
  const page = from['pages/settings.vue']
  if (!page) throw new Error('no pages/settings.vue')
  const parts = ['pages/settings.vue', ...new Set([...elements(page)].map(({ el }) => `components/${el.tag}.vue`))].filter((path) => from[path])
  const out: Record<string, number> = {}
  for (const path of parts) {
    const n = [...elements(from[path]!)].filter(({ el }) => FOLD.test(el.tag!)).length
    if (n > 0) out[path] = n
  }
  return out
}

/** The attributes written as text that a person reads. */
const SHOWN = new Set(['label', 'description', 'placeholder', 'title', 'text', 'hint', 'help', 'aria-label', 'alt'])

/** The text of a template that a person reads: its text and its SHOWN attributes written as text (comments and bound values left out). */
function* shownText(node: TNode): Generator<string> {
  for (const c of node.children ?? []) {
    if (c.type === 2 && typeof c.content === 'string') yield c.content
    if (c.type !== 1) continue
    for (const p of c.props ?? []) if (p.type === 6 && SHOWN.has(p.name) && p.value) yield p.value.content
    yield* shownText(c)
  }
}

/** Per source, how many times the text a person reads says "browser". */
function browserWords(from: Record<string, TNode> = templates): Record<string, number> {
  const out: Record<string, number> = {}
  for (const [path, ast] of Object.entries(from)) {
    const n = [...shownText(ast)].reduce((sum, text) => sum + (text.match(/\bbrowser/gi) ?? []).length, 0)
    if (n > 0) out[path] = n
  }
  return out
}

/** The places known to break a rule, or to be its exception, by their count, each with why. A fixed place lowers or drops its entry. */
type Known = Record<string, { count: number; why: string }>
const countsOf = (known: Known) => Object.fromEntries(Object.entries(known).map(([path, k]) => [path, k.count]))

const YOLO_FOLDED_KNOWN: Known = {
  'components/CrewEditor.vue': { count: 1, why: "More options holds the crew's Yolo, as Permission prompts: to be brought into view" },
}

const SETTINGS_FOLDS_KNOWN: Known = {
  'components/BrakemanSettings.vue': { count: 1, why: "Brakeman's card folds its advanced settings: to be unfolded" },
}

const BROWSER_WORDS_KNOWN: Known = {
  'components/AddAgentSlideover.vue': { count: 1, why: "a real browser: an agent's browser profile folder" },
  'components/AlertSwitches.vue': { count: 2, why: 'the notification and its routing named Browser: to be reworded' },
  'components/AlertsPanel.vue': { count: 1, why: 'where routing is kept, "in this browser": to be reworded' },
  'components/BrakemanAskBar.vue': { count: 1, why: `"in this browser's time": to be reworded` },
  'components/DecisionsRecent.vue': { count: 1, why: '"From this browser": to be reworded' },
  'components/DispatchViewsBar.vue': { count: 1, why: '"for every browser": to be reworded' },
  'components/ReviewDraft.vue': { count: 2, why: 'where a draft is kept, "this browser": to be reworded' },
  'components/SessionInspector.vue': { count: 1, why: '"watch this session from their browser": to be reworded' },
  'components/WelcomeCard.vue': { count: 1, why: '"from any browser": to be reworded as from anywhere' },
  'components/WorkbenchTokenGate.vue': { count: 1, why: 'where the token is kept, "in this browser": to be reworded' },
  'pages/settings.vue': { count: 3, why: 'a real browser: Open in browser, which the desktop app opens' },
}

describe('what stays in view, and the words', () => {
  it('keep Yolo out of a fold, the places that still fold it each with its reason', () => {
    expect(foldedYolo()).toEqual(countsOf(YOLO_FOLDED_KNOWN))
  })

  it('fold nothing on the settings page or in a card it draws, the places that still do each with its reason', () => {
    expect(settingsFolds()).toEqual(countsOf(SETTINGS_FOLDS_KNOWN))
  })

  it('never say the app is used in a browser, each place that still says it or means a real browser with its reason', () => {
    expect(browserWords()).toEqual(countsOf(BROWSER_WORDS_KNOWN))
  })

  it('see Yolo in a fold, a fold in a card of the settings page and the word browser', () => {
    expect(
      foldedYolo(sample(`<template><div><USwitch v-model="yolo" /><UCollapsible><template #content><USwitch v-model="crew.yolo" /><USwitch v-model="yoloDefault" /></template></UCollapsible></div></template>`)),
    ).toEqual({ 'components/Sample.vue': 1 })
    const page = sample('<template><div><SampleCard /></div></template>')['components/Sample.vue']!
    const card = sample('<template><UCard><UAccordion :items="items" /></UCard></template>')['components/Sample.vue']!
    expect(settingsFolds({ 'pages/settings.vue': page, 'components/SampleCard.vue': card })).toEqual({ 'components/SampleCard.vue': 1 })
    expect(browserWords(sample('<template><p title="Kept in this browser">Watch it from any browser.<!-- a browser --></p><UButton :label="browserLabel" /></template>'))).toEqual({ 'components/Sample.vue': 2 })
  })
})
