import { ELEMENT_BOUNDS, wellFormed, type ElementReport, type PreviewHintKind } from '../protocol'
import type { Doc, El } from './select'

/**
 * What a page's dev build says drew an element (docs/protocol.md, Preview bridge): a data-conductor-source attribute
 * ("<file>:<line>:<column>") on it or an ancestor, which any build may add; a Vue dev build's component chain
 * (__vueParentComponent, its type's __file and __name, which @vitejs/plugin-vue sets in development); Svelte's __svelte_meta.loc;
 * React's _debugSource (React 18 and before) through the element's fiber. Innermost first, the attribute's before the frameworks'.
 * A page with none gives none. Everything read is the page's own and may be anything: only strings and whole numbers are kept.
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

const str = (v: unknown) => (typeof v === 'string' ? wellFormed(v) : '')
const whole = (v: unknown) => (Number.isInteger(v) && (v as number) >= 0 && (v as number) <= Number.MAX_SAFE_INTEGER ? (v as number) : 0)

/** "<file>:<line>:<column>" (the file may hold colons), or the file alone. */
export function parseSource(s: string): { file: string; line: number; column: number } {
  const m = /^(.*?):(\d+)(?::(\d+))?$/.exec(s)
  if (!m) return { file: s, line: 0, column: 0 }
  return { file: m[1]!, line: whole(Number(m[2])), column: whole(Number(m[3] ?? 0)) }
}

function attributeHints(el: El): Hint[] {
  const out: Hint[] = []
  for (let e: El | null = el; e; e = e.parentElement) {
    const v = e.getAttribute('data-conductor-source')
    if (v) out.push({ how: 'attribute', ...parseSource(wellFormed(v)), component: '' })
  }
  return out
}

function vueHints(el: El): Hint[] {
  const out: Hint[] = []
  let inst: unknown
  for (let e: El | null = el; e && !inst; e = e.parentElement) inst = prop(e, '__vueParentComponent')
  for (let i = 0; inst && i < 64; i++, inst = prop(inst, 'parent')) {
    const type = prop(inst, 'type')
    const file = str(prop(type, '__file'))
    if (file) out.push({ how: 'vue', file, line: 0, column: 0, component: str(prop(type, '__name')) || str(prop(type, 'name')) })
  }
  return out
}

function svelteHints(el: El): Hint[] {
  const out: Hint[] = []
  for (let e: El | null = el; e; e = e.parentElement) {
    const loc = prop(prop(e, '__svelte_meta'), 'loc')
    const file = str(prop(loc, 'file'))
    if (file) out.push({ how: 'svelte', file, line: whole(prop(loc, 'line')), column: whole(prop(loc, 'column')), component: '' })
  }
  return out
}

/** The fiber React keeps on a DOM node, under a key of its own (__reactFiber$… in React 17 and 18). */
function fiberOf(el: El): unknown {
  for (const k of Object.keys(el)) if (k.startsWith('__reactFiber$') || k.startsWith('__reactInternalInstance$')) return prop(el, k)
  return undefined
}

function reactHints(el: El): Hint[] {
  const out: Hint[] = []
  const seen = new Set<string>()
  let fiber = fiberOf(el)
  for (let i = 0; fiber && i < 256; i++, fiber = prop(fiber, 'return')) {
    const src = prop(fiber, '_debugSource')
    const file = str(prop(src, 'fileName'))
    if (!file) continue
    const line = whole(prop(src, 'lineNumber'))
    const key = `${file}:${line}`
    if (seen.has(key)) continue
    seen.add(key)
    const type = prop(fiber, 'type')
    const component = typeof type === 'function' ? str(prop(type, 'displayName')) || str(prop(type, 'name')) : ''
    out.push({ how: 'react', file, line, column: whole(prop(src, 'columnNumber')), component })
  }
  return out
}

/** The hints for el, innermost first by kind, at most ELEMENT_BOUNDS.hints. */
export function hintsOf(el: El): Hint[] {
  return [...attributeHints(el), ...vueHints(el), ...svelteHints(el), ...reactHints(el)].slice(0, ELEMENT_BOUNDS.hints)
}

/** The kinds of hint the page gives, read from its elements once (a ready's hints), at most the first 2,000 elements looked at. */
export function hintKindsOf(doc: Doc): PreviewHintKind[] {
  const kinds = new Set<PreviewHintKind>()
  const all = [...doc.querySelectorAll('*')].slice(0, 2000)
  for (const e of all) {
    if (e.hasAttribute('data-conductor-source')) kinds.add('attribute')
    if (prop(prop(prop(e, '__vueParentComponent'), 'type'), '__file')) kinds.add('vue')
    if (prop(prop(e, '__svelte_meta'), 'loc')) kinds.add('svelte')
    if (prop(prop(fiberOf(e), '_debugSource'), 'fileName')) kinds.add('react')
  }
  return (['attribute', 'vue', 'svelte', 'react'] as const).filter((k) => kinds.has(k))
}
