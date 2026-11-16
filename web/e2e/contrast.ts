import type { Locator, Page } from '@playwright/test'
import { CONTRAST_FLOORS, floorFor, parseColor, ratio, toHex, type Rgba } from '../app/utils/contrast'

/**
 * The contrast the browser actually draws (the UX review's PR-25). In the page, `collect` gathers each visible element with a
 * non-blank text node of its own: its computed colour, font size and weight, and every ancestor up to the root that paints a
 * background or has an opacity below 1. In Node, `judge` composes them with `app/utils/contrast.ts`: each background is laid under
 * what is above it, and where an ancestor has an opacity below 1, both the text and the ground composed so far are laid over what
 * lies beneath that ancestor at that opacity. The floors are CONTRAST_FLOORS': 4.5:1, 3:1 for large text, and for dimmed text 3:1
 * where its first opaque ground is the page and 2.7:1 over the canvas and the raised surfaces.
 *
 * What is painted under the text without holding it (a tab's moving indicator) is found at the text's centre
 * (`elementsFromPoint`) and laid just above the ancestor that holds both; an ancestor's ::before or ::after fill that covers the
 * text's box is laid just above that ancestor's own background.
 *
 * Skipped: what has no CSS ground to compose (inside a terminal, the editor, a canvas, an image, an SVG), text that is disabled
 * (`[disabled]`, `[aria-disabled="true"]` and what they hold) or invisible (no box, clipped to nothing by an ancestor, `sr-only`,
 * transparent text, an opacity of 0 on the way up). Limits: a background image or gradient is not seen (the colour under it is);
 * blend modes, filters and text shadows are not counted; text scrolled out of its scroller is clipped and skipped.
 */

/** One drawn text as the page gave it. */
export interface Drawn {
  /** The element's `data-*` path: it and up to five ancestors that carry `data-*` attributes, nearest last. */
  path: string
  text: string
  color: string
  px: number
  weight: number
  /** What paints or fades under the text, nearest first: each ancestor's computed background colour and opacity, and what is painted
   *  under the text without holding it, its background laid at `alpha` (its own opacity). */
  layers: Array<{ bg: string; opacity: number; alpha?: number }>
  /** The element's colour is the dimmed text token's. */
  dimmed: boolean
  /** The first opaque background up the chain is the page's (`--ui-bg`), a raised surface's or the canvas's, or another. */
  ground: 'page' | 'raised' | 'other'
}

/** A text under its floor. */
export interface Failure {
  path: string
  text: string
  ratio: number
  floor: number
  fg: string
  bg: string
}

/** Gathers the drawn text under `root` (the whole page when not given). Runs in the page. */
function gather(root: Element | null): Drawn[] {
  const probe = (prop: 'color' | 'backgroundColor', value: string) => {
    const el = document.body.appendChild(document.createElement('span'))
    el.style[prop] = value
    const got = getComputedStyle(el)[prop]
    el.remove()
    return got
  }
  const dimmed = probe('color', 'var(--ui-text-dimmed)')
  const page = probe('backgroundColor', 'var(--ui-bg)')
  const raised = new Set(['var(--ui-bg-muted)', 'var(--ui-bg-elevated)', 'var(--ui-bg-canvas)', 'var(--ui-bg-canvas-hover)'].map((v) => probe('backgroundColor', v)))
  const clear = (c: string) => c === 'transparent' || /^rgba\(\d+, \d+, \d+, 0\)$/.test(c) || /\/ 0\)$/.test(c)
  const opaque = (c: string) => !clear(c) && !/rgba\(|\/ (0?\.\d+|\d+%)\)$/.test(c)
  const skip = '.xterm, .monaco-editor, canvas, img, svg, video, iframe, [disabled], [aria-disabled="true"], .sr-only, [data-contrast-skip]'
  const seen = new Set<Element>()
  const out: Drawn[] = []
  const start = root ?? document.body
  const walker = document.createTreeWalker(start, NodeFilter.SHOW_TEXT)
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    const text = (node.textContent ?? '').replace(/\s+/g, ' ').trim()
    if (!text) continue
    const el = node.parentElement
    if (!el || seen.has(el)) continue
    seen.add(el)
    if (el.closest(skip)) continue
    if (!el.checkVisibility({ opacityProperty: true, visibilityProperty: true })) continue
    const range = document.createRange()
    range.selectNodeContents(node)
    const box = range.getBoundingClientRect()
    if (box.width < 1 || box.height < 1) continue
    // Clipped to nothing by the element or an ancestor that clips (a fold, a collapsed panel, a 1 px box).
    let shown = true
    for (let a: Element | null = el; a && shown; a = a.parentElement) {
      const s = getComputedStyle(a)
      if (s.overflowX === 'visible' && s.overflowY === 'visible' && s.clipPath === 'none' && s.clip !== 'rect(0px, 0px, 0px, 0px)') continue
      const r = a.getBoundingClientRect()
      if (Math.min(box.right, r.right) - Math.max(box.left, r.left) < 2 || Math.min(box.bottom, r.bottom) - Math.max(box.top, r.top) < 2) shown = false
    }
    if (!shown) continue
    const style = getComputedStyle(el)
    if (clear(style.color)) continue
    // What is painted under the text but is no ancestor of it (a tab's moving indicator, a positioned backdrop): the elements at the
    // text's centre below its own, each laid just above the ancestor that holds both, at its own opacity.
    const under = new Map<Element, Array<{ bg: string; alpha: number }>>()
    const cx = Math.min(Math.max(box.left + box.width / 2, 0), innerWidth - 1)
    const cy = Math.min(Math.max(box.top + box.height / 2, 0), innerHeight - 1)
    const stack = box.bottom > 0 && box.top < innerHeight ? document.elementsFromPoint(cx, cy) : []
    const top = stack.findIndex((x) => x === el || x.contains(el))
    if (top >= 0) {
      for (const u of stack.slice(top + 1)) {
        if (u.contains(el) || el.contains(u)) continue
        const us = getComputedStyle(u)
        if (clear(us.backgroundColor)) continue
        let holder: Element | null = u.parentElement
        while (holder && !holder.contains(el)) holder = holder.parentElement
        if (!holder) continue
        const list = under.get(holder) ?? []
        list.push({ bg: us.backgroundColor, alpha: Number(us.opacity) })
        under.set(holder, list)
      }
    }
    const layers: Drawn['layers'] = []
    let ground: Drawn['ground'] | undefined
    const groundOf = (bg: string) => {
      if (!ground && opaque(bg)) ground = bg === page ? 'page' : raised.has(bg) ? 'raised' : 'other'
    }
    for (let a: Element | null = el; a; a = a.parentElement) {
      for (const u of under.get(a) ?? []) {
        layers.push({ bg: u.bg, opacity: 1, alpha: u.alpha })
        if (u.alpha === 1) groundOf(u.bg)
      }
      // A pseudo-element's fill under the content (Nuxt UI draws a nav link's pill as ::before), when it covers the text's box.
      for (const pseudo of ['::after', '::before']) {
        const ps = getComputedStyle(a, pseudo)
        if (ps.content === 'none' || clear(ps.backgroundColor)) continue
        if (parseFloat(ps.width) + 1 < box.width || parseFloat(ps.height) + 1 < box.height) continue
        layers.push({ bg: ps.backgroundColor, opacity: 1, alpha: Number(ps.opacity) })
        if (Number(ps.opacity) === 1) groundOf(ps.backgroundColor)
      }
      const s = getComputedStyle(a)
      const opacity = Number(s.opacity)
      if (!clear(s.backgroundColor) || opacity < 1) layers.push({ bg: s.backgroundColor, opacity })
      groundOf(s.backgroundColor)
    }
    const path: string[] = []
    for (let a: Element | null = el; a && a !== document.body && path.length < 6; a = a.parentElement) {
      const data = Array.from(a.attributes).filter((x) => x.name.startsWith('data-') && !x.name.startsWith('data-v-') && x.name !== 'data-state')
      if (a === el || data.length) path.unshift(a.tagName.toLowerCase() + data.map((x) => (x.value ? `[${x.name}="${x.value.slice(0, 40)}"]` : `[${x.name}]`)).join(''))
    }
    out.push({ path: path.join(' > '), text: text.slice(0, 80), color: style.color, px: parseFloat(style.fontSize), weight: Number(style.fontWeight) || 400, layers, dimmed: style.color === dimmed, ground: ground ?? 'page' })
  }
  return out
}

/** The drawn text of the page, or of what `root` holds (its first match). */
export async function collect(page: Page, root?: Locator): Promise<Drawn[]> {
  if (root) return root.first().evaluate(gather)
  return page.evaluate(gather, null)
}

/** A colour still to be laid over what lies beneath: `p` (premultiplied) plus `t` of what is beneath. */
type Pending = { p: [number, number, number]; t: number }

const colour = (s: string): Rgba => {
  const c = parseColor(s)
  if (!c) throw new Error(`contrast: the page drew a colour it does not read: ${s}`)
  return c
}

/** The text's colour and its ground as drawn, every layer composed. */
export function composed(d: Drawn): { fg: Rgba; bg: Rgba } {
  const c = colour(d.color)
  let fg: Pending = { p: [c.r * c.a, c.g * c.a, c.b * c.a], t: 1 - c.a }
  let bg: Pending = { p: [0, 0, 0], t: 1 }
  const under = (x: Pending, l: Rgba): Pending => ({ p: [x.p[0] + x.t * l.r * l.a, x.p[1] + x.t * l.g * l.a, x.p[2] + x.t * l.b * l.a], t: x.t * (1 - l.a) })
  const fade = (x: Pending, o: number): Pending => ({ p: [x.p[0] * o, x.p[1] * o, x.p[2] * o], t: o * x.t + 1 - o })
  for (const l of d.layers) {
    const c0 = colour(l.bg)
    const b = { ...c0, a: c0.a * (l.alpha ?? 1) }
    fg = under(fg, b)
    bg = under(bg, b)
    if (l.opacity < 1) {
      fg = fade(fg, l.opacity)
      bg = fade(bg, l.opacity)
    }
  }
  // Beneath everything: the browser's white canvas (the app's body paints its own ground above it).
  const done = (x: Pending): Rgba => ({ r: x.p[0] + x.t, g: x.p[1] + x.t, b: x.p[2] + x.t, a: 1 })
  return { fg: done(fg), bg: done(bg) }
}

/** The floor of one drawn text. */
export function floorOf(d: Drawn): number {
  if (d.dimmed && d.ground !== 'other') return floorFor({ px: d.px, weight: d.weight, dimmed: d.ground })
  return floorFor({ px: d.px, weight: d.weight })
}

/** Every drawn text under its floor, worst first. */
export function judge(drawn: Drawn[]): Failure[] {
  const out: Failure[] = []
  for (const d of drawn) {
    const { fg, bg } = composed(d)
    const r = ratio(fg, bg)
    const floor = floorOf(d)
    if (r + 0.005 < floor) out.push({ path: d.path, text: d.text, ratio: Math.round(r * 100) / 100, floor, fg: toHex(fg), bg: toHex(bg) })
  }
  return out.sort((a, b) => a.ratio / a.floor - b.ratio / b.floor)
}

/** The ten worst, one a line, for a failure's message. */
export function worst(failures: Failure[], n = 10): string {
  return failures
    .slice(0, n)
    .map((f) => `${f.ratio.toFixed(2)} < ${f.floor}  ${f.fg} on ${f.bg}  "${f.text}"  ${f.path}`)
    .join('\n')
}

/** A style that stops every transition and animation, so that no colour is read on its way. */
export const STILL = '*, *::before, *::after { transition: none !important; animation: none !important; }'

/** Walks the page (or `root`) and returns its failures, worst first, with how many texts it judged. */
export async function walk(page: Page, root?: Locator): Promise<{ judged: number; failures: Failure[] }> {
  await page.addStyleTag({ content: STILL })
  const drawn = await collect(page, root)
  return { judged: drawn.length, failures: judge(drawn) }
}

/**
 * The visible buttons inside `area` (every match of it) whose computed background is the resolved `--ui-primary`: the filled forest
 * buttons, of which an area has one, its main action (the UX review's P16). Returned as their accessible names (the label, else the
 * text), in the page's order.
 */
export async function filledPrimaries(area: Locator): Promise<string[]> {
  return area.evaluateAll((roots) => {
    const probe = document.body.appendChild(document.createElement('span'))
    probe.style.backgroundColor = 'var(--ui-primary)'
    const primary = getComputedStyle(probe).backgroundColor
    probe.remove()
    const out: string[] = []
    for (const root of roots) {
      for (const b of Array.from(root.querySelectorAll('button, a'))) {
        if (!(b as HTMLElement).checkVisibility({ opacityProperty: true, visibilityProperty: true })) continue
        const box = b.getBoundingClientRect()
        if (box.width < 1 || box.height < 1) continue
        if (getComputedStyle(b).backgroundColor !== primary) continue
        out.push((b.getAttribute('aria-label') ?? b.textContent ?? '').replace(/\s+/g, ' ').trim())
      }
    }
    return out
  })
}

export { CONTRAST_FLOORS }
