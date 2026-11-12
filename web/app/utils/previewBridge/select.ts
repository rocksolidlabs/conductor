import { ELEMENT_BOUNDS, measuredBytes, wellFormed } from '../protocol'

/**
 * How the bridge reads a page (docs/protocol.md, Preview bridge): every string through pageText, every list it walks by index up
 * to its bound, every walk of the tree its own and bounded (walkTree, up); and what it picks an element by: a selector it builds, the
 * elements stacked under the pointer and the DOM's walk from one, each leaving out the bridge's own host; and the page epoch,
 * which says only that the page moved. The DOM's types; a test hands in fakes.
 */
export type El = Element
export type Doc = Document

/**
 * The one way the bridge reads a page's string: cut to most code units and one more (to know it was longer: a code unit measures a
 * byte at least) by a slice of its start (or, with end, its end) before anything else reads it, then made well-formed. So a long
 * string costs no more than its bound; a value that does not answer as a string is "".
 */
export function pageText(v: unknown, most: number, end = false): string {
  if (typeof v !== 'string' && (typeof v !== 'object' || v === null)) return ''
  const slice = (v as { slice?: unknown }).slice
  if (typeof slice !== 'function') return ''
  const cut: unknown = end ? slice.call(v, -(most + 1)) : slice.call(v, 0, most + 1)
  return typeof cut === 'string' && cut.length <= most + 1 ? wellFormed(cut) : ''
}

/** e's local name, lower case, read to most bytes. */
export function nameOf(e: El, most: number = ELEMENT_BOUNDS.name): string {
  return pageText(e.localName, most).toLowerCase()
}

/** The most ancestors a walk up the tree visits, the element's own step included. */
export const UP = 64

/** The first of el and its ancestors, at most most of them, that test takes, else null. */
export function up(el: El | null, test: (e: El) => boolean, most = UP): El | null {
  for (let i = 0; el && i < most; i++, el = el.parentElement) if (test(el)) return el
  return null
}

/**
 * root's subtree in tree order by the DOM's own links (firstChild, nextSibling, parentNode), root first, every link it follows,
 * down, across or up, counted against most: when they are spent the walk ends where it is. visit says of each node whether the
 * walk goes into its children (true), past them (false) or ends (null), and leave is called after the children of every node it
 * went into.
 */
export function walkTree(root: Node, most: number, visit: (n: Node, depth: number) => boolean | null, leave: (n: Node) => void = () => {}) {
  let n: Node = root
  let depth = 0
  let left = most
  for (;;) {
    const go = visit(n, depth)
    if (go === null) return
    if (go) {
      if (--left < 0) return
      const child = n.firstChild
      if (child) {
        ;[n, depth] = [child, depth + 1]
        continue
      }
      leave(n)
    }
    for (let next: Node | null = null; !next; ) {
      if (n === root || --left < 0) return
      if ((next = n.nextSibling)) n = next
      else if (--left < 0) return
      else leave((n = n.parentNode!)), depth--
    }
  }
}

/** s escaped as a CSS identifier by CSS.escape, which every browser has; "" where there is none. */
const cssIdent = (s: string) => (globalThis as { CSS?: { escape?: (s: string) => string } }).CSS?.escape?.(s) ?? ''

/** The most nodes a selector's path visits: its steps up and the siblings passed to count each step's place, together. */
export const PATH = 4096

/**
 * The selector an element is named by, built and never searched for or cut: its id, `#` and the id escaped, in a document not in
 * quirks mode, when it is within the bound and the document, asked that very selector, gives the element; else its path from the
 * root, `:root > tag:nth-child(n) > …`, each step its place among its siblings and its name only where the element's local name,
 * as read, is a plain lower-case one (else any, `*`), so a step matches one child in any document and namespace. "" when the path
 * passes PATH nodes, leaves the document, or would pass ELEMENT_BOUNDS.selector bytes.
 */
export function selectorFor(el: El, doc: Doc): string {
  const most = ELEMENT_BOUNDS.selector
  const id = pageText(el.id, most)
  const ident = id && pageText(doc.compatMode, 16) === 'CSS1Compat' && cssIdent(id)
  if (ident && measuredBytes('#' + ident) <= most && doc.querySelector('#' + ident) === el) return '#' + ident
  const steps: string[] = []
  let left = PATH
  let bytes = ':root'.length
  for (let e = el; e !== doc.documentElement; ) {
    const parent = e.parentElement
    if (!parent || --left < 0) return ''
    let place = 1
    for (let s = e.previousSibling; s; s = s.previousSibling) {
      if (--left < 0) return ''
      if (s.nodeType === 1) place++
    }
    const tag = pageText(e.localName, 32)
    const step = `${/^[a-z][a-z0-9-]{0,31}$/.test(tag) ? tag : '*'}:nth-child(${place})`
    if ((bytes += step.length + 3) > most) return ''
    steps.push(step)
    e = parent
  }
  return [':root', ...steps.reverse()].join(' > ')
}

/** The most elements under the pointer selection mode steps through. */
export const STACK = 64

/** The elements stacked under the point (elementsFromPoint), topmost first, the bridge's host left out: of its list the first STACK, the host among them. */
export function stackAt(doc: Doc, x: number, y: number, host: El | null): El[] {
  const all = doc.elementsFromPoint(x, y)
  const out: El[] = []
  for (let i = 0, n = Math.min(all.length, STACK); i < n; i++) if (all[i] !== host) out.push(all[i]!)
  return out
}

/** The next element in the stack from current: deeper (Down) or higher (Up); current where it is the last that way. */
export function stepStack(stack: El[], current: El | null, deeper: boolean): El | null {
  if (stack.length === 0) return current
  const i = current ? stack.indexOf(current) : -1
  if (i < 0) return stack[0]!
  return stack[Math.max(0, Math.min(stack.length - 1, i + (deeper ? 1 : -1)))]!
}

export type Walk = 'parent' | 'child' | 'previous' | 'next'

/** The element a walk of the DOM reaches from el (Left, Right, Shift+Up, Shift+Down), the bridge's host left out; el at an end. */
export function walk(el: El, way: Walk, host: El | null): El {
  let next: El | null
  switch (way) {
    case 'parent':
      next = el.parentElement
      break
    case 'child':
      next = el.firstElementChild
      if (next && next === host) next = next.nextElementSibling
      break
    case 'previous':
      next = el.previousElementSibling
      if (next && next === host) next = next.previousElementSibling
      break
    case 'next':
      next = el.nextElementSibling
      if (next && next === host) next = next.nextElementSibling
      break
  }
  return next ?? el
}

/** What the epoch listens on: the frame's window, its document, a MutationObserver and the next animation frame. */
export interface EpochPage {
  win: Pick<Window, 'addEventListener' | 'removeEventListener'>
  doc: Pick<Document, 'addEventListener' | 'removeEventListener' | 'documentElement'>
  /** Whether a node is the bridge's own host. */
  mine: (n: unknown) => boolean
  observer: (cb: (records: MutationRecord[]) => void) => { observe(target: Node, options: MutationObserverInit): void; disconnect(): void }
  frame: (cb: () => void) => void
  /** Called on a resize or a scroll of the window, beside the bump. */
  moved?: () => void
}

const MOTION_EVENTS = ['animationstart', 'animationiteration', 'animationend', 'animationcancel', 'transitionrun', 'transitionend', 'transitioncancel']

/**
 * The page epoch: a counter bumped, at most once per animation frame, by a resize or a scroll of the window, a mutation of the
 * document (the bridge's host put on the page or taken off, alone in its record, left out) and every animation and transition
 * event. It says only that the page moved: the records are looked at up to the first of the page's. A resize or a scroll also
 * calls moved, which selection mode takes its outline off by.
 */
export function pageEpoch(p: EpochPage): { value(): number; stop(): void } {
  let epoch = 0
  let bumped = false
  const bump = () => {
    if (bumped) return
    bumped = true
    epoch++
    p.frame(() => {
      bumped = false
    })
  }
  const ours = (r: MutationRecord) =>
    p.mine(r.target) || (r.type === 'childList' && r.addedNodes.length + r.removedNodes.length === 1 && p.mine(r.addedNodes[0] ?? r.removedNodes[0]))
  const mo = p.observer((records) => {
    for (let i = 0; !bumped && i < records.length; i++) if (!ours(records[i]!)) bump()
  })
  mo.observe(p.doc.documentElement, { subtree: true, childList: true, attributes: true, characterData: true })
  const moved = () => (bump(), p.moved?.())
  p.win.addEventListener('resize', moved)
  p.win.addEventListener('scroll', moved)
  for (const e of MOTION_EVENTS) p.doc.addEventListener(e, bump, true)
  return {
    value: () => epoch,
    stop() {
      mo.disconnect()
      p.win.removeEventListener('resize', moved)
      p.win.removeEventListener('scroll', moved)
      for (const e of MOTION_EVENTS) p.doc.removeEventListener(e, bump, true)
    },
  }
}
