import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { parse as parseSfc } from 'vue/compiler-sfc'
import { describe, expect, it } from 'vitest'
import { CONTRAST_FLOORS } from './contrast'
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
  'components/CrewMiniDag.vue': { lines: 1, why: "the Crews page's small graph: a member that asks is tinted amber, a chip the line diagram (PR-33's) has not replaced there" },
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
