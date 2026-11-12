import { ELEMENT_BOUNDS, measuredBytes, wellFormed } from '../protocol'

/**
 * What the bridge picks an element by (docs/protocol.md, Preview bridge): a selector found exactly once, the elements stacked under
 * the pointer and the DOM's walk from one, each leaving out the bridge's own host; and the page epoch, which says only that the
 * page moved. The DOM's types; a test hands in fakes.
 */
export type El = Element
export type Doc = Document

/** s escaped as a CSS identifier (CSS.escape where the page has it). */
export function cssIdent(s: string): string {
  const css = (globalThis as { CSS?: { escape?: (s: string) => string } }).CSS
  if (css?.escape) return css.escape(s)
  let out = ''
  for (let i = 0; i < s.length; i++) {
    const ch = s[i]!
    if (/[A-Za-z_-]/.test(ch) || (/[0-9]/.test(ch) && i > 0 && !(i === 1 && s[0] === '-'))) out += ch
    else out += `\\${ch.codePointAt(0)!.toString(16)} `
  }
  return out
}

/** A class a build generated (a hash, a run of digits) says nothing stable about the element. */
function stableClass(c: string): boolean {
  return /^[A-Za-z][\w-]*$/.test(c) && !/\d{3,}|[0-9a-f]{6,}/.test(c)
}

function count(doc: Doc, sel: string): number {
  try {
    return doc.querySelectorAll(sel).length
  } catch {
    return 0
  }
}

/** el's own step: its tag, its stable classes and, among its parent's children of its tag, its place when it shares the tag. */
function step(el: El): string {
  const tag = el.tagName.toLowerCase()
  let s = cssIdent(tag) + [...el.classList].filter(stableClass).map((c) => '.' + cssIdent(c)).join('')
  const parent = el.parentElement
  if (parent) {
    const same = [...parent.children].filter((c) => c.tagName === el.tagName)
    if (same.length > 1) s += `:nth-of-type(${same.indexOf(el) + 1})`
  }
  return s
}

/** Whether s holds no half of a surrogate pair alone: a selector that does could not be sent as it is, and is not one. */
const sound = (s: string) => wellFormed(s) === s

/**
 * A selector that finds el exactly once: its id when no other element has it, else the shortest chain of steps from el up (an
 * ancestor's unique id anchoring it) that querySelectorAll finds once; "" when none does within ELEMENT_BOUNDS.selector bytes, or
 * when the page's names would make it a text that is not well-formed.
 */
export function selectorFor(el: El, doc: Doc, host: El | null = null): string {
  if (el === host) return ''
  if (el.id && count(doc, '#' + cssIdent(el.id)) === 1) return sound(el.id) ? '#' + cssIdent(el.id) : ''
  let chain = ''
  for (let e: El | null = el; e; e = e.parentElement) {
    const anchor = e !== el && e.id && count(doc, '#' + cssIdent(e.id)) === 1
    const part = anchor ? '#' + cssIdent(e.id) : step(e)
    chain = chain ? `${part} > ${chain}` : part
    if (measuredBytes(chain) > ELEMENT_BOUNDS.selector || !sound(chain)) return ''
    if (anchor || count(doc, chain) === 1) return count(doc, chain) === 1 ? chain : ''
  }
  return ''
}

/** Whether e is the bridge's host or inside it. */
function own(e: El, host: El | null): boolean {
  return !!host && (e === host || host.contains(e))
}

/** The elements stacked under the point (elementsFromPoint), topmost first, the bridge's host left out. */
export function stackAt(doc: Doc, x: number, y: number, host: El | null): El[] {
  return doc.elementsFromPoint(x, y).filter((e) => !own(e, host))
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
      while (next && own(next, host)) next = next.nextElementSibling
      break
    case 'previous':
      next = el.previousElementSibling
      while (next && own(next, host)) next = next.previousElementSibling
      break
    case 'next':
      next = el.nextElementSibling
      while (next && own(next, host)) next = next.nextElementSibling
      break
  }
  return next && !own(next, host) ? next : el
}

/** What the epoch listens on: the frame's window, its document, a MutationObserver and the next animation frame. */
export interface EpochPage {
  win: Pick<Window, 'addEventListener' | 'removeEventListener'>
  doc: Pick<Document, 'addEventListener' | 'removeEventListener' | 'documentElement'>
  host: () => El | null
  observer: (cb: (records: MutationRecord[]) => void) => { observe(target: Node, options: MutationObserverInit): void; disconnect(): void }
  frame: (cb: () => void) => void
}

const MOTION_EVENTS = ['animationstart', 'animationiteration', 'animationend', 'animationcancel', 'transitionrun', 'transitionend', 'transitioncancel']

/**
 * The page epoch: a counter bumped, at most once per animation frame, by a resize or a scroll of the window, a mutation of the
 * document (the bridge's own host left out) and every animation and transition event. It says only that the page moved.
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
  const mo = p.observer((records) => {
    const host = p.host()
    const ownNode = (n: Node) => !!host && (n === host || host.contains(n))
    const moved = records.some((r) => !ownNode(r.target) && !(r.type === 'childList' && [...r.addedNodes, ...r.removedNodes].every(ownNode)))
    if (moved) bump()
  })
  mo.observe(p.doc.documentElement, { subtree: true, childList: true, attributes: true, characterData: true })
  p.win.addEventListener('resize', bump)
  p.win.addEventListener('scroll', bump)
  for (const e of MOTION_EVENTS) p.doc.addEventListener(e, bump, true)
  return {
    value: () => epoch,
    stop() {
      mo.disconnect()
      p.win.removeEventListener('resize', bump)
      p.win.removeEventListener('scroll', bump)
      for (const e of MOTION_EVENTS) p.doc.removeEventListener(e, bump, true)
    },
  }
}
