import { ELEMENT_BOUNDS, MAX_ELEMENT_REPORT, measuredBytes, previewMeasure, wellFormed, type ElementCut, type ElementReport } from '../protocol'
import { hintsOf } from './hints'
import { nameOf, pageText, selectorFor, up, walkTree, type Doc, type El } from './select'

/**
 * The bridge's report of one element (docs/protocol.md, Preview bridge): read from the page by bounded walks, lists read by index
 * and strings by pageText, never written to it; its markup and text written by the bridge itself from what is allowed. Each field
 * cut to its own bound, then the whole cut to MAX_ELEMENT_REPORT in a fixed order, measured after each step by the measure the
 * server uses (previewMeasure). Every length is in measured bytes, never in characters, and a cut never splits a character.
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

/**
 * s well-formed and cut to its longest prefix of at most most measured bytes, whole characters only. Every code unit measures a
 * byte at least, so nothing past its first most + 1 is ever kept (a pair cut there is past the cut too) and nothing past them is read.
 */
export function cutStart(s: string, most: number): string {
  const head = wellFormed(s.length > most + 1 ? s.slice(0, most + 1) : s)
  if (measuredBytes(head) <= most) return head
  let n = 0
  let out = ''
  for (const ch of head) {
    const w = measuredBytes(ch)
    if (n + w > most) break
    n += w
    out += ch
  }
  return out
}

/** s well-formed and cut to its longest suffix of at most most measured bytes (a path keeps its file's name). */
export function cutEnd(s: string, most: number): string {
  const tail = wellFormed(s.length > most + 1 ? s.slice(-(most + 1)) : s)
  if (measuredBytes(tail) <= most) return tail
  const chars = [...tail]
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

/** s when it is within most measured bytes, else "": what is never cut (a selector, the bridge's own markup). */
const whole = (s: string, most: number) => (measuredBytes(s) <= most ? s : '')

/** The HTML's opening tag: up to its first `>` (the bridge's markup escapes every `>` of a value). */
export function openingTag(html: string): string {
  const end = html.indexOf('>')
  return end < 0 ? html : html.slice(0, end + 1)
}

/** The opening tag within most bytes: its attributes from the last that does not fit left out, "" when its name alone does not fit. */
export function tagWithin(html: string, most: number): string {
  let s = openingTag(html)
  while (s && measuredBytes(s) > most) {
    // An attribute ends at its closing quote: the bridge's markup writes every one as name="value", its quotes escaped.
    const i = s.lastIndexOf('" ')
    const j = i >= 0 ? i + 1 : s.indexOf(' ')
    s = j > 0 ? s.slice(0, j) + '>' : ''
  }
  return s
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

/** r with every field cut to its own bound (ELEMENT_BOUNDS), a selector or markup past its bound none: the cut before the order. */
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
    selector: whole(r.selector, B.selector),
    attributes: r.attributes.slice(0, B.attributes).map(([n, v]) => [cutStart(n, B.attributeName), cutStart(v, B.attributeValue)]),
    state: {
      ...r.state,
      ariaDisabled: cutStart(r.state.ariaDisabled, B.name),
      ariaExpanded: cutStart(r.state.ariaExpanded, B.name),
      pointerEvents: cutStart(r.state.pointerEvents, B.name),
    },
    hit: { ...r.hit, selector: whole(r.hit.selector, B.selector), tag: cutStart(r.hit.tag, B.name) },
    html: whole(r.html, B.html),
    neighbourhood: whole(r.neighbourhood, B.neighbourhood),
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
  // The opening tag to 512 bytes, whole attributes only; then, if that is not enough, the HTML dropped.
  ['tag', (r) => [{ ...r, html: tagWithin(r.html, 512) }, { ...r, html: '' }]],
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
        // A selector is never cut: one past 256 bytes is none.
        selector: whole(r.selector, 256),
        hit: { ...r.hit, tag: cutStart(r.hit.tag, 256), selector: whole(r.hit.selector, 256) },
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

/** What the reading of an element needs of the page beyond the element: its document and its window. */
export interface Page {
  doc: Doc
  win: { getComputedStyle(el: El): { getPropertyValue(p: string): string } }
}

/** n rounded to hundredths, never -0 (Go writes -0 where JavaScript writes 0). */
function round(n: number): number {
  return Number.isFinite(n) ? Math.round(n * 100) / 100 + 0 : 0
}

const B = ELEMENT_BOUNDS
/** Of an element that takes a person's input, or one a person can edit in place, the report says only what it is: these attributes and every aria-*. */
const CONTROLS = new Set(['input', 'textarea', 'select', 'option'])
export const CONTROL_ATTRIBUTES = ['type', 'name', 'id', 'class', 'role', 'contenteditable']
/** Of a script, a style and a template, the tag and attributes alone; a void element has no end tag. */
const CODE = new Set(['script', 'style', 'template'])
const VOID = new Set(['area', 'base', 'br', 'col', 'embed', 'hr', 'img', 'input', 'link', 'meta', 'source', 'track', 'wbr'])
/** The walks' bounds in nodes (a text or a name, the label for an id, a subtree's animations); the most aria-labelledby ids and animations of one element. */
const [TEXT_NODES, LABEL_NODES, MOTION, LABELLED_BY] = [256, 256, 64, 8] as const

const isControl = (e: El) => CONTROLS.has(nameOf(e, 16)) || (e as HTMLElement).isContentEditable === true
/** Whether nothing of e's content is written: a control's, a script's, a style's, a template's. */
const shut = (e: El) => isControl(e) || CODE.has(nameOf(e, 16))
const classesOf = (e: El, most: number) => pageText(e.getAttribute('class'), most).trim().replace(/\s+/g, ' ')
const ESCAPES: Record<string, string> = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }
const escape = (s: string) => s.replace(/[&<>"]/g, (c) => ESCAPES[c]!)

/** e's attributes as a report and its markup carry them: the first ELEMENT_BOUNDS.attributes by index; of a control, those that say what it is. */
function attributesOf(e: El): Array<[string, string]> {
  const list = e.attributes
  const control = isControl(e)
  const out: Array<[string, string]> = []
  for (let i = 0, n = Math.min(list.length, B.attributes); i < n; i++) {
    const name = pageText(list[i]!.name, B.attributeName)
    const lower = name.toLowerCase()
    if (!control || CONTROL_ATTRIBUTES.includes(lower) || lower.startsWith('aria-')) out.push([name, pageText(list[i]!.value, B.attributeValue)])
  }
  return out
}

/**
 * The markup of root's subtree, written by the bridge from the live tree (walkTree) with its own writer, never a copy and never the
 * page's serialiser: of an element its tag (to 64 bytes) and attributes (attributesOf), escaped; of a text node its data; nothing of
 * a comment; of a control, a script, a style and a template the tag alone. It visits at most most / 4 nodes and writes at most most
 * bytes, a node whole or not at all (a text node up to what is left) and every element it opens closed, so it is never cut after.
 * With keep (the neighbourhood), each child of root but keep is written to its grandchildren, their content as `…`.
 */
export function markup(root: El, most: number, keep?: El): string {
  const out: string[] = []
  const closes: string[] = []
  let left = most
  let elide = false
  // s written when it fits with reserve (an end tag written later) beside it, else nothing.
  const put = (s: string, reserve = '') => measuredBytes(s) + measuredBytes(reserve) <= left && ((left -= measuredBytes(s) + measuredBytes(reserve)), out.push(s) > 0)
  const visit = (n: Node, depth: number) => {
    if (n.nodeType === 3) {
      for (const ch of pageText((n as Text).data, left)) if (!put(escape(ch))) return null
      return false
    }
    if (n.nodeType !== 1) return false
    const e = n as El
    if (depth === 1) elide = !!keep && e !== keep
    const tag = cutStart(pageText(e.localName, 64), 64)
    const open = `<${tag}${attributesOf(e).map(([k, v]) => ` ${k}="${escape(v)}"`).join('')}>`
    const close = `</${tag}>`
    if (VOID.has(tag)) return put(open) ? false : null
    if (shut(e) || (elide && depth >= 3)) return put(open + (shut(e) ? '' : '…') + close) ? false : null
    return put(open, close) ? (closes.push(close), true) : null
  }
  walkTree(root, most / 4, visit, () => out.push(closes.pop()!))
  return out.concat(closes.reverse()).join('')
}

/** The text of root's subtree, from its first TEXT_NODES nodes' text to most code units and one more, spaces folded: none of what shut keeps. */
export function textOf(root: El, most: number): string {
  let out = ''
  walkTree(root, TEXT_NODES, (n) => {
    if (out.length > most) return null
    if (n.nodeType === 3) out += pageText((n as Text).data, most - out.length).replace(/\s+/g, ' ')
    return n.nodeType === 1 && !shut(n as El)
  })
  return out.replace(/\s+/g, ' ').trim()
}

/** The label whose for is id among the first LABEL_NODES nodes around el (its third ancestor's subtree), else null. */
function labelFor(el: El, id: string): El | null {
  let top = el
  for (let i = 0; i < 3 && top.parentElement; i++) top = top.parentElement
  let found = null as El | null
  const visit = (e: El) => (nameOf(e, 8) === 'label' && pageText(e.getAttribute('for'), B.name) === id ? ((found = e), null) : true)
  walkTree(top, LABEL_NODES, (n) => n.nodeType === 1 && visit(n as El))
  return found
}

/** The element's accessible name, as far as a page's script can read it: aria-label, aria-labelledby's text, a label's, its text. */
export function accessibleName(el: El, doc: Doc): string {
  const label = pageText(el.getAttribute('aria-label'), B.name).trim()
  if (label) return label
  const ids = [...new Set(pageText(el.getAttribute('aria-labelledby'), B.name).split(/\s+/).filter(Boolean))].slice(0, LABELLED_BY)
  const names = ids.map((id) => doc.getElementById(id)).flatMap((e) => (e ? [textOf(e, B.name)] : [])).filter(Boolean)
  if (names.length) return names.join(' ')
  const id = pageText(el.id, B.name)
  return textOf((id && labelFor(el, id)) || up(el, (e) => nameOf(e, 8) === 'label') || el, B.name)
}

function ancestorOf(a: El): ElementReport['ancestors'][number] {
  const m = B.ancestor
  return { tag: nameOf(a, m), id: pageText(a.id, m), classes: classesOf(a, m), role: pageText(a.getAttribute('role'), m) }
}

/** The animations playing on el, on the elements of its subtree's first MOTION nodes and on its ancestors (getAnimations where the page has it). */
function motionOf(el: El): number {
  let n = 0
  const playing = (e: El) => {
    const list = e.getAnimations?.()
    for (let i = 0; list && i < list.length && i < MOTION; i++) if (list[i]!.playState === 'running') n++
    return false
  }
  walkTree(el, MOTION, (e) => e.nodeType === 1 && !playing(e as El))
  up(el.parentElement, playing, B.ancestors)
  return n
}

/** el's report as read now, cut to MAX_ELEMENT_REPORT. Nothing of the bridge's own is on the page while it reads. */
export function readElement(el: El, page: Page): ElementReport {
  const { doc, win } = page
  const style = win.getComputedStyle(el)
  const css = (p: string, most: number = B.styleValue) => pageText(style.getPropertyValue(p), most)
  const rect = el.getBoundingClientRect()
  // Sizes are at least 0, as the schema has them, whatever the page's own getBoundingClientRect answers.
  const box = { x: round(rect.x), y: round(rect.y), width: Math.max(0, round(rect.width)), height: Math.max(0, round(rect.height)) }
  const hit = doc.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2)
  const ancestors: ElementReport['ancestors'] = []
  for (let a = el.parentElement; a && ancestors.length < B.ancestors; a = a.parentElement) ancestors.push(ancestorOf(a))
  const report: ElementReport = {
    tag: nameOf(el),
    id: pageText(el.id, B.name),
    classes: classesOf(el, B.name),
    role: pageText(el.getAttribute('role'), B.name),
    name: accessibleName(el, doc),
    text: textOf(el, B.text),
    selector: selectorFor(el, doc),
    box,
    // Of a control, what it is: never its value, a password field's least of all.
    attributes: attributesOf(el),
    state: {
      disabled: el.hasAttribute('disabled') || safeMatches(el, ':disabled'),
      hidden: el.hasAttribute('hidden') || css('display') === 'none' || css('visibility') === 'hidden',
      ariaDisabled: pageText(el.getAttribute('aria-disabled'), B.name),
      ariaExpanded: pageText(el.getAttribute('aria-expanded'), B.name),
      pointerEvents: css('pointer-events', B.name),
      inert: !!up(el, (e) => e.hasAttribute('inert')),
      hovered: safeMatches(el, ':hover'),
      motion: motionOf(el),
    },
    hit: { self: !!hit && !!up(hit, (e) => e === el), selector: hit ? selectorFor(hit, doc) : '', tag: hit ? nameOf(hit) : '' },
    html: markup(el, B.html),
    neighbourhood: el.parentElement ? markup(el.parentElement, B.neighbourhood, el) : '',
    ancestors,
    styles: REPORT_STYLES.map((p) => [p, css(p)]),
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
