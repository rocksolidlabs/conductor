import { ELEMENT_BOUNDS, MAX_ELEMENT_REPORT, measuredBytes, previewMeasure, wellFormed, type ElementCut, type ElementReport } from '../protocol'
import { hintsOf } from './hints'
import { selectorFor, type Doc, type El } from './select'

/**
 * The bridge's report of one element (docs/protocol.md, Preview bridge): read from the page, never written to it, each field cut to
 * its own bound, then the whole cut to MAX_ELEMENT_REPORT in a fixed order, measured after each step by the measure the server
 * uses (previewMeasure). Every length is in measured bytes, never in characters, and a cut never splits a character.
 */

/**
 * The computed styles a report carries, in order: the first 8 and 16 are what the state and the hit test lean on (visibility, hit
 * and box), kept longest by the cut.
 */
export const REPORT_STYLES = [
  'display', 'visibility', 'opacity', 'pointer-events', 'position', 'z-index', 'inset', 'transform',
  'width', 'height', 'overflow', 'content-visibility', 'clip-path', 'filter', 'cursor', 'box-sizing',
  'margin', 'padding', 'border-width', 'border-style', 'border-color', 'border-radius', 'min-width', 'min-height',
  'max-width', 'max-height', 'overflow-x', 'overflow-y', 'flex-direction', 'flex-wrap', 'flex-grow', 'flex-shrink',
  'flex-basis', 'justify-content', 'align-items', 'align-self', 'gap', 'grid-template-columns', 'grid-template-rows', 'grid-column',
  'grid-row', 'order', 'color', 'background-color', 'background-image', 'font-family', 'font-size', 'font-weight',
  'font-style', 'line-height', 'letter-spacing', 'text-align', 'text-decoration-line', 'text-transform', 'white-space', 'text-overflow',
  'outline', 'box-shadow', 'isolation', 'mix-blend-mode', 'user-select', 'touch-action', 'transition-property', 'animation-name',
] as const

/** s cut to its longest prefix of at most most measured bytes, whole characters only. */
export function cutStart(s: string, most: number): string {
  if (measuredBytes(s) <= most) return s
  let n = 0
  let out = ''
  for (const ch of wellFormed(s)) {
    const w = measuredBytes(ch)
    if (n + w > most) break
    n += w
    out += ch
  }
  return out
}

/** s cut to its longest suffix of at most most measured bytes (a path keeps its file's name). */
export function cutEnd(s: string, most: number): string {
  if (measuredBytes(s) <= most) return s
  const chars = [...wellFormed(s)]
  let n = 0
  let i = chars.length
  while (i > 0) {
    const w = measuredBytes(chars[i - 1]!)
    if (n + w > most) break
    n += w
    i--
  }
  return chars.slice(i).join('')
}

/** HTML cut to at most most measured bytes, ending at an element's boundary (after a `>`), "" when no tag fits. */
export function cutHTML(html: string, most: number): string {
  const s = cutStart(html, most)
  if (s === html) return s
  const end = s.lastIndexOf('>')
  return end < 0 ? '' : s.slice(0, end + 1)
}

/** The HTML's opening tag: up to its first `>`. */
export function openingTag(html: string): string {
  const end = html.indexOf('>')
  return end < 0 ? html : html.slice(0, end + 1)
}

/** An ancestor cut to fit its bound: its classes first, then its id, its role and its tag. */
function cutAncestor(a: ElementReport['ancestors'][number], most: number): ElementReport['ancestors'][number] {
  const out = { ...a }
  for (const k of ['classes', 'id', 'role', 'tag'] as const) {
    const over = previewMeasure(out) - most
    if (over <= 0) break
    out[k] = cutStart(out[k], Math.max(0, measuredBytes(out[k]) - over))
  }
  return out
}

/** r with every field cut to its own bound (ELEMENT_BOUNDS): the cut that comes before the order. */
export function boundFields(r: ElementReport): ElementReport {
  const B = ELEMENT_BOUNDS
  return {
    ...r,
    tag: cutStart(r.tag, B.name),
    id: cutStart(r.id, B.name),
    classes: cutStart(r.classes, B.name),
    role: cutStart(r.role, B.name),
    name: cutStart(r.name, B.name),
    text: cutStart(r.text, B.text),
    selector: cutStart(r.selector, B.selector),
    attributes: r.attributes.slice(0, B.attributes).map(([n, v]) => [cutStart(n, B.attributeName), cutStart(v, B.attributeValue)]),
    state: {
      ...r.state,
      ariaDisabled: cutStart(r.state.ariaDisabled, B.name),
      ariaExpanded: cutStart(r.state.ariaExpanded, B.name),
      pointerEvents: cutStart(r.state.pointerEvents, B.name),
    },
    hit: { ...r.hit, selector: cutStart(r.hit.selector, B.selector), tag: cutStart(r.hit.tag, B.name) },
    html: cutHTML(r.html, B.html),
    neighbourhood: cutHTML(r.neighbourhood, B.neighbourhood),
    ancestors: r.ancestors.slice(0, B.ancestors).map((a) => cutAncestor(a, B.ancestor)),
    styles: r.styles.slice(0, B.styles).map(([n, v]) => [cutStart(n, B.styleName), cutStart(v, B.styleValue)]),
    hints: r.hints.slice(0, B.hints).map((h) => ({ ...h, file: cutEnd(h.file, B.hintFile), component: cutStart(h.component, B.hintComponent) })),
  }
}

/** The cut's steps, in order: each makes r smaller and is marked in its cut when taken. */
const STEPS: Array<[ElementCut, (r: ElementReport) => ElementReport[]]> = [
  ['neighbourhood', (r) => [{ ...r, neighbourhood: '' }]],
  ['html', (r) => [{ ...r, html: openingTag(r.html) }]],
  ['styles', (r) => [{ ...r, styles: r.styles.slice(0, 16) }]],
  ['attributes', (r) => [{ ...r, attributes: r.attributes.map(([n, v]) => [n, cutStart(v, 64)]) }]],
  // The opening tag to 512 bytes, measured; then, if that is not enough, the HTML dropped.
  ['tag', (r) => [{ ...r, html: cutStart(r.html, 512) }, { ...r, html: '' }]],
  ['hints', (r) => [{ ...r, hints: r.hints.slice(0, 2).map((h) => ({ ...h, file: cutEnd(h.file, 256), component: cutStart(h.component, 128) })) }]],
  ['ancestors', (r) => [{ ...r, ancestors: r.ancestors.slice(0, 4) }]],
  ['attribute-count', (r) => [{ ...r, attributes: r.attributes.slice(0, 8) }]],
  [
    'names',
    (r) => [
      {
        ...r,
        tag: cutStart(r.tag, 256),
        text: cutStart(r.text, 256),
        name: cutStart(r.name, 256),
        id: cutStart(r.id, 256),
        role: cutStart(r.role, 256),
        classes: cutStart(r.classes, 256),
        selector: cutStart(r.selector, 256),
        hit: { ...r.hit, tag: cutStart(r.hit.tag, 256), selector: cutStart(r.hit.selector, 256) },
      },
    ],
  ],
  ['style-count', (r) => [{ ...r, styles: r.styles.slice(0, 8) }]],
]

/**
 * r cut to fit most (MAX_ELEMENT_REPORT but in a test): its fields to their bounds, then the steps in their order, measuring after
 * each and stopping at the first that fits, each step taken named in cut. At the report's own bound the fields' bounds leave the
 * steps up to the names' under 12 KiB, so the cut always ends within it; a smaller bound may not, and the caller checks.
 */
export function cutReport(r: ElementReport, most = MAX_ELEMENT_REPORT): ElementReport {
  let out: ElementReport = { ...boundFields(r), cut: [] }
  for (const [name, step] of STEPS) {
    if (previewMeasure(out) <= most) break
    for (const next of step(out)) {
      out = { ...next, cut: [...out.cut, name].filter((c, i, a) => a.indexOf(c) === i) }
      if (previewMeasure(out) <= most) break
    }
  }
  return out
}

/** What the reading of an element needs of the page beyond the element: its document, its window and the bridge's own host. */
export interface Page {
  doc: Doc
  win: { getComputedStyle(el: El): { getPropertyValue(p: string): string } }
  host: El | null
}

/** n rounded to hundredths, never -0 (Go writes -0 where JavaScript writes 0). */
function round(n: number): number {
  return Number.isFinite(n) ? Math.round(n * 100) / 100 + 0 : 0
}

/** v when it is a string, else "": a page's property may be anything. */
function text(v: unknown): string {
  return typeof v === 'string' ? wellFormed(v) : ''
}

/** el's outer HTML from a clone whose inputs' values and textareas' text are emptied first: no field's value is ever read. */
export function sanitizedHTML(el: El, elide?: El): string {
  const clone = el.cloneNode(true) as El
  const fields = [clone, ...clone.querySelectorAll('input, textarea')]
  for (const f of fields) {
    const tag = f.tagName.toLowerCase()
    if (tag === 'input') f.removeAttribute('value')
    if (tag === 'textarea') f.textContent = ''
  }
  if (elide) {
    // The neighbourhood: each sibling's subtree past depth 2 left out.
    const index = [...el.children].indexOf(elide)
    ;[...clone.children].forEach((sib, i) => {
      if (i === index) return
      for (const child of sib.children) for (const grand of child.children) grand.textContent = '…'
    })
  }
  return text(clone.outerHTML)
}

/** el's text with every textarea's left out (a textarea's text is its value as the page started it), spaces folded. */
export function fieldFreeText(el: El): string {
  if (el.tagName.toLowerCase() === 'textarea') return ''
  const clone = el.cloneNode(true) as El
  for (const f of clone.querySelectorAll('textarea')) f.textContent = ''
  return (clone.textContent ?? '').replace(/\s+/g, ' ').trim()
}

/** The element's accessible name, as far as a page's script can read it: aria-label, aria-labelledby's text, a label's, its text. */
export function accessibleName(el: El, doc: Doc): string {
  const label = el.getAttribute('aria-label')
  if (label) return label.trim()
  const by = el.getAttribute('aria-labelledby')
  if (by) {
    const names = by.split(/\s+/).map((id) => { const e = doc.getElementById(id); return e ? fieldFreeText(e) : '' }).filter(Boolean)
    if (names.length) return names.join(' ')
  }
  if (el.id) {
    const forLabel = [...doc.querySelectorAll('label')].find((l) => l.getAttribute('for') === el.id)
    if (forLabel) return fieldFreeText(forLabel)
  }
  const wrap = el.closest('label')
  if (wrap) return fieldFreeText(wrap)
  return fieldFreeText(el)
}

function ancestorOf(a: El): ElementReport['ancestors'][number] {
  return { tag: a.tagName.toLowerCase(), id: text(a.id), classes: [...a.classList].join(' '), role: a.getAttribute('role') ?? '' }
}

/** The animations playing on el, its subtree and its ancestors (Element.getAnimations where the page has it). */
function motionOf(el: El): number {
  const playing = (list: Array<{ playState?: string }> | undefined) => (list ?? []).filter((a) => a.playState === 'running').length
  let n = playing(el.getAnimations?.({ subtree: true }))
  for (let a = el.parentElement; a; a = a.parentElement) n += playing(a.getAnimations?.())
  return n
}

/** el's report as read now, cut to MAX_ELEMENT_REPORT. The bridge's outline is off the page while it reads. */
export function readElement(el: El, page: Page): ElementReport {
  const { doc, win, host } = page
  const style = win.getComputedStyle(el)
  const rect = el.getBoundingClientRect()
  const box = { x: round(rect.x), y: round(rect.y), width: round(rect.width), height: round(rect.height) }
  const hitEl = doc.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2)
  const hit = hitEl && hitEl !== host ? hitEl : null
  const tag = el.tagName.toLowerCase()
  const ancestors: ElementReport['ancestors'] = []
  for (let a = el.parentElement; a && ancestors.length < ELEMENT_BOUNDS.ancestors; a = a.parentElement) ancestors.push(ancestorOf(a))
  const report: ElementReport = {
    tag,
    id: text(el.id),
    classes: [...el.classList].join(' '),
    role: el.getAttribute('role') ?? '',
    name: wellFormed(accessibleName(el, doc)),
    text: wellFormed(fieldFreeText(el)),
    selector: selectorFor(el, doc, host),
    box,
    // An input's value is never read, a password field's least of all.
    attributes: [...el.attributes].filter((a) => !(tag === 'input' && a.name === 'value')).map((a) => [text(a.name), text(a.value)]),
    state: {
      disabled: el.hasAttribute('disabled') || safeMatches(el, ':disabled'),
      hidden: el.hasAttribute('hidden') || style.getPropertyValue('display') === 'none' || style.getPropertyValue('visibility') === 'hidden',
      ariaDisabled: el.getAttribute('aria-disabled') ?? '',
      ariaExpanded: el.getAttribute('aria-expanded') ?? '',
      pointerEvents: style.getPropertyValue('pointer-events'),
      inert: !!el.closest('[inert]'),
      hovered: safeMatches(el, ':hover'),
      motion: motionOf(el),
    },
    hit: { self: !!hit && (hit === el || el.contains(hit)), selector: hit ? selectorFor(hit, doc, host) : '', tag: hit ? hit.tagName.toLowerCase() : '' },
    html: sanitizedHTML(el),
    neighbourhood: el.parentElement ? sanitizedHTML(el.parentElement, el) : '',
    ancestors,
    styles: REPORT_STYLES.map((p) => [p, text(style.getPropertyValue(p))]),
    hints: hintsOf(el),
    cut: [],
  }
  return cutReport(report)
}

function safeMatches(el: El, sel: string): boolean {
  try {
    return el.matches(sel)
  } catch {
    return false
  }
}
