import { ELEMENT_BOUNDS, type ElementReport, type PreviewHintKind } from '../protocol'
import { UP, pageText, walkTree, type Doc, type El } from './select'

/**
 * What a page's dev build says drew an element (docs/protocol.md, Preview bridge): a data-conductor-source attribute
 * ("<file>:<line>:<column>") on it or an ancestor, which any build may add; a Vue dev build's component chain
 * (__vueParentComponent, its type's __file and __name, which @vitejs/plugin-vue sets in development); Svelte's __svelte_meta.loc;
 * React's _debugSource (React 18 and before) through the element's fiber, found among the element's first KEYS own keys. Innermost
 * first, the attribute's before the frameworks', each walk stopping at ELEMENT_BOUNDS.hints hints: the attribute's and Svelte's at
 * UP levels, the element's own included, Vue's at UP levels to its instance and 64 instances, React's at 256 fibers. A page with
 * none gives none. Everything read is the page's own and may be anything: only strings, read by pageText, and whole numbers are
 * kept.
 */
export type Hint = ElementReport['hints'][number]

type Bag = Record<string, unknown>

function prop(o: unknown, k: string): unknown {
  try {
    return (typeof o === 'object' && o !== null) || typeof o === 'function' ? (o as Bag)[k] : undefined
  } catch {
    return undefined
  }
}

const MOST = ELEMENT_BOUNDS.hints
const file = (v: unknown) => pageText(v, ELEMENT_BOUNDS.hintFile, true)
const component = (v: unknown) => pageText(v, ELEMENT_BOUNDS.hintComponent)
const whole = (v: unknown) => (Number.isInteger(v) && (v as number) >= 0 && (v as number) <= Number.MAX_SAFE_INTEGER ? (v as number) : 0)

/** "<file>:<line>:<column>" (the file may hold colons), or the file alone. */
export function parseSource(s: string): { file: string; line: number; column: number } {
  const m = /^(.*?):(\d+)(?::(\d+))?$/.exec(s)
  if (!m) return { file: s, line: 0, column: 0 }
  return { file: m[1]!, line: whole(Number(m[2])), column: whole(Number(m[3] ?? 0)) }
}

function attributeHints(el: El, out: Hint[]) {
  for (let e: El | null = el, i = 0; e && i < UP && out.length < MOST; e = e.parentElement, i++) {
    // The end of the value: the line and column, and the file's own name.
    const v = pageText(e.getAttribute('data-conductor-source'), ELEMENT_BOUNDS.hintFile + 40, true)
    if (v) out.push({ how: 'attribute', ...parseSource(v), component: '' })
  }
}

function vueHints(el: El, out: Hint[]) {
  let inst: unknown
  for (let e: El | null = el, i = 0; e && !inst && i < UP; e = e.parentElement, i++) inst = prop(e, '__vueParentComponent')
  for (let i = 0; inst && i < 64 && out.length < MOST; i++, inst = prop(inst, 'parent')) {
    const type = prop(inst, 'type')
    const f = file(prop(type, '__file'))
    if (f) out.push({ how: 'vue', file: f, line: 0, column: 0, component: component(prop(type, '__name')) || component(prop(type, 'name')) })
  }
}

function svelteHints(el: El, out: Hint[]) {
  for (let e: El | null = el, i = 0; e && i < UP && out.length < MOST; e = e.parentElement, i++) {
    const loc = prop(prop(e, '__svelte_meta'), 'loc')
    const f = file(prop(loc, 'file'))
    if (f) out.push({ how: 'svelte', file: f, line: whole(prop(loc, 'line')), column: whole(prop(loc, 'column')), component: '' })
  }
}

/** The most of an element's own keys read for React's, and the most elements whose own keys a ready's kinds list. */
export const KEYS = 64

/** The fiber React keeps on a DOM node, under a key of its own (__reactFiber$… in React 17 and 18), among its first KEYS own keys. */
function fiberOf(el: El): unknown {
  const keys = Object.keys(el)
  for (let i = 0; i < keys.length && i < KEYS; i++) if (keys[i]!.startsWith('__reactFiber$') || keys[i]!.startsWith('__reactInternalInstance$')) return prop(el, keys[i]!)
  return undefined
}

function reactHints(el: El, out: Hint[]) {
  const seen = new Set<string>()
  let fiber = fiberOf(el)
  for (let i = 0; fiber && i < 256 && out.length < MOST; i++, fiber = prop(fiber, 'return')) {
    const src = prop(fiber, '_debugSource')
    const f = file(prop(src, 'fileName'))
    if (!f) continue
    const line = whole(prop(src, 'lineNumber'))
    const key = `${f}:${line}`
    if (seen.has(key)) continue
    seen.add(key)
    const type = prop(fiber, 'type')
    out.push({ how: 'react', file: f, line, column: whole(prop(src, 'columnNumber')), component: typeof type === 'function' ? component(prop(type, 'displayName')) || component(prop(type, 'name')) : '' })
  }
}

/** The hints for el, innermost first by kind, at most ELEMENT_BOUNDS.hints: each kind read only while there is room. */
export function hintsOf(el: El): Hint[] {
  const out: Hint[] = []
  for (const kind of [attributeHints, vueHints, svelteHints, reactHints]) if (out.length < MOST) kind(el, out)
  return out
}

/** The most links a ready's hint kinds are read along, one node at a time in the document's order (walkTree). */
export const HINT_NODES = 2000

/**
 * The kinds of hint the page gives (a ready's hints), read from the elements the walk reaches within HINT_NODES links; React's
 * from the own keys of the first KEYS of them.
 */
export function hintKindsOf(doc: Doc): PreviewHintKind[] {
  const kinds = new Set<PreviewHintKind>()
  let keyed = 0
  const kind = (e: El) => {
    if (e.hasAttribute('data-conductor-source')) kinds.add('attribute')
    if (prop(prop(prop(e, '__vueParentComponent'), 'type'), '__file')) kinds.add('vue')
    if (prop(prop(e, '__svelte_meta'), 'loc')) kinds.add('svelte')
    if (keyed++ < KEYS && prop(prop(fiberOf(e), '_debugSource'), 'fileName')) kinds.add('react')
    return true
  }
  if (doc.documentElement) walkTree(doc.documentElement, HINT_NODES, (n) => n.nodeType === 1 && kind(n as El))
  return (['attribute', 'vue', 'svelte', 'react'] as const).filter((k) => kinds.has(k))
}
