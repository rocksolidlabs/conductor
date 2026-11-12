/**
 * A small DOM for the bridge's tests (vitest runs in node): elements with attributes, children and text, a selector matcher for
 * what the bridge writes and reads (tags, #id, .class, [attr], :nth-of-type, :disabled, :hover, lists and the child combinator),
 * and a document whose hit tests the test sets. Never imported by the bridge itself.
 */
type Node = FakeEl | string

export class FakeEl {
  readonly tagName: string
  parentElement: FakeEl | null = null
  childNodes: Node[] = []
  attrs = new Map<string, string>()
  rect = { x: 0, y: 0, width: 0, height: 0 }
  style: Record<string, string> = {}
  hovered = false
  running = 0
  ;[k: string]: unknown

  constructor(tag: string, attrs: Record<string, string> = {}, children: Node[] = []) {
    this.tagName = tag.toUpperCase()
    for (const [k, v] of Object.entries(attrs)) this.attrs.set(k, v)
    for (const c of children) this.append(c)
  }

  append(...nodes: Node[]) {
    for (const c of nodes) {
      if (typeof c !== 'string') c.parentElement = this
      this.childNodes.push(c)
    }
  }
  remove() {
    if (!this.parentElement) return
    this.parentElement.childNodes = this.parentElement.childNodes.filter((c) => c !== this)
    this.parentElement = null
  }
  get children(): FakeEl[] {
    return this.childNodes.filter((c): c is FakeEl => typeof c !== 'string')
  }
  get firstElementChild(): FakeEl | null {
    return this.children[0] ?? null
  }
  private sibling(d: number): FakeEl | null {
    const s = this.parentElement?.children ?? []
    return s[s.indexOf(this) + d] ?? null
  }
  get nextElementSibling(): FakeEl | null {
    return this.sibling(1)
  }
  get previousElementSibling(): FakeEl | null {
    return this.sibling(-1)
  }
  get id() {
    return this.attrs.get('id') ?? ''
  }
  get classList() {
    return (this.attrs.get('class') ?? '').split(/\s+/).filter(Boolean)
  }
  get attributes() {
    return [...this.attrs].map(([name, value]) => ({ name, value }))
  }
  getAttribute(k: string) {
    return this.attrs.get(k) ?? null
  }
  hasAttribute(k: string) {
    return this.attrs.has(k)
  }
  removeAttribute(k: string) {
    this.attrs.delete(k)
  }
  get textContent(): string {
    return this.childNodes.map((c) => (typeof c === 'string' ? c : c.textContent)).join('')
  }
  set textContent(s: string) {
    this.childNodes = s ? [s] : []
  }
  get outerHTML(): string {
    const tag = this.tagName.toLowerCase()
    const attrs = [...this.attrs].map(([k, v]) => ` ${k}="${v.replace(/&/g, '&amp;').replace(/"/g, '&quot;')}"`).join('')
    const inner = this.childNodes.map((c) => (typeof c === 'string' ? c.replace(/&/g, '&amp;').replace(/</g, '&lt;') : c.outerHTML)).join('')
    return tag === 'input' ? `<${tag}${attrs}>` : `<${tag}${attrs}>${inner}</${tag}>`
  }
  /** The page's own copy, which the bridge never makes: it copies into a document of its own (importNode). */
  cloneNode(_deep: boolean): FakeEl {
    throw new Error('the bridge copied an element in the page’s own document')
  }
  get isConnected(): boolean {
    let e: FakeEl = this
    while (e.parentElement) e = e.parentElement
    return e.tagName === 'HTML'
  }
  attachShadow(_init: { mode: string }) {
    const root = new FakeEl('#shadow-root')
    this.shadow = root
    return root
  }
  contains(other: FakeEl | null): boolean {
    for (let e = other; e; e = e.parentElement) if (e === this) return true
    return false
  }
  descendants(): FakeEl[] {
    return this.children.flatMap((c) => [c, ...c.descendants()])
  }
  querySelectorAll(sel: string): FakeEl[] {
    return this.descendants().filter((e) => matches(e, sel))
  }
  querySelector(sel: string): FakeEl | null {
    return this.querySelectorAll(sel)[0] ?? null
  }
  matches(sel: string): boolean {
    return matches(this, sel)
  }
  closest(sel: string): FakeEl | null {
    for (let e: FakeEl | null = this; e; e = e.parentElement) if (matches(e, sel)) return e
    return null
  }
  getBoundingClientRect() {
    return this.rect
  }
  getAnimations(opts?: { subtree?: boolean }): Array<{ playState: string }> {
    const own = Array.from({ length: this.running }, () => ({ playState: 'running' }))
    return opts?.subtree ? [...own, ...this.descendants().flatMap((d): Array<{ playState: string }> => d.getAnimations())] : own
  }
}

const PART = /#[\w-]+|\.[\w-]+|\[[\w-]+(?:="[^"]*")?\]|:nth-of-type\(\d+\)|:disabled|:hover/g
const COMPOUND = new RegExp(`^([a-z][a-z0-9-]*|\\*)?((?:${PART.source})*)$`)

function compound(e: FakeEl, s: string): boolean {
  const m = COMPOUND.exec(s)
  if (!m) throw new SyntaxError(`not a selector the fake reads: ${s}`)
  if (m[1] && m[1] !== '*' && m[1] !== e.tagName.toLowerCase()) return false
  for (const part of m[2]!.match(PART) ?? []) {
    if (part[0] === '#' && e.id !== part.slice(1)) return false
    if (part[0] === '.' && !e.classList.includes(part.slice(1))) return false
    if (part[0] === '[') {
      const [name, value] = part.slice(1, -1).split('=')
      if (!e.hasAttribute(name!) || (value !== undefined && e.getAttribute(name!) !== value.slice(1, -1))) return false
    }
    if (part.startsWith(':nth-of-type')) {
      const same = e.parentElement?.children.filter((c) => c.tagName === e.tagName) ?? [e]
      if (same.indexOf(e) + 1 !== Number(/\d+/.exec(part)![0])) return false
    }
    if (part === ':disabled' && !e.hasAttribute('disabled')) return false
    if (part === ':hover' && !e.hovered) return false
  }
  return true
}

function matches(e: FakeEl, sel: string): boolean {
  return sel.split(',').some((one) => {
    const parts = one.trim().split(/\s*>\s*/)
    let at: FakeEl | null = e
    for (let i = parts.length - 1; i >= 0; i--) {
      if (!at || !compound(at, parts[i]!)) return false
      at = at.parentElement
    }
    return true
  })
}

/** el copied, as importNode copies it into a document with no window. */
function copy(el: FakeEl): FakeEl {
  const out = new FakeEl(el.tagName, Object.fromEntries(el.attrs))
  for (const c of el.childNodes) out.append(typeof c === 'string' ? c : copy(c))
  return out
}

type Listener = (e: unknown) => void

/** Listeners by type, as a window or a document keeps them (the capture flag is ignored: the fakes dispatch to every one). */
class Listeners {
  readonly on = new Map<string, Set<Listener>>()
  addEventListener(type: string, fn: Listener) {
    if (!this.on.has(type)) this.on.set(type, new Set())
    this.on.get(type)!.add(fn)
  }
  removeEventListener(type: string, fn: Listener) {
    this.on.get(type)?.delete(fn)
  }
  /** The number of listeners of every type: what the page is given to run. */
  get count(): number {
    return [...this.on.values()].reduce((n, s) => n + s.size, 0)
  }
  dispatch(type: string, e: unknown) {
    for (const fn of [...(this.on.get(type) ?? [])]) fn(e)
  }
}

/** A document over root (the html element); the test sets what its hit tests return. */
export class FakeDoc extends Listeners {
  hitAt: (x: number, y: number) => FakeEl[] = () => []
  title = ''
  readonly implementation = { createHTMLDocument: (_title: string) => ({ importNode: (el: FakeEl, _deep: boolean) => copy(el) }) }
  constructor(readonly documentElement: FakeEl) {
    super()
  }
  createElement(tag: string) {
    return new FakeEl(tag)
  }
  querySelectorAll(sel: string): FakeEl[] {
    return [this.documentElement, ...this.documentElement.descendants()].filter((e) => matches(e, sel))
  }
  querySelector(sel: string) {
    return this.querySelectorAll(sel)[0] ?? null
  }
  getElementById(id: string) {
    return this.querySelectorAll('*').find((e) => e.id === id) ?? null
  }
  elementsFromPoint(x: number, y: number) {
    return this.hitAt(x, y)
  }
  elementFromPoint(x: number, y: number) {
    return this.hitAt(x, y)[0] ?? null
  }
}

/** What the parent window was posted: each message as a structured clone of it, and its target origin. */
export interface Post {
  message: unknown
  target: string
}

/**
 * A framed page's window over doc: its parent records what it is posted, message events are delivered as the browser delivers
 * them (deliver) or made by the page's own script (forge), and the mutation observers and animation frames it is asked for are kept
 * for the test to run.
 */
export class FakeWin extends Listeners {
  readonly posts: Post[] = []
  readonly parent = { postMessage: (message: unknown, target: string) => this.posts.push({ message: structuredClone(message), target }) }
  readonly observers: Array<(records: unknown[]) => void> = []
  readonly frames: Array<() => void> = []
  location = { href: 'http://127.0.0.1:5173/' }
  navigator = { userAgent: 'Mozilla/5.0 (a test)' }
  innerWidth = 1280
  innerHeight = 800
  devicePixelRatio = 2
  scrollX = 0
  scrollY = 0
  readonly MutationObserver: unknown
  constructor(readonly document: FakeDoc) {
    super()
    const observers = this.observers
    this.MutationObserver = class {
      constructor(cb: (records: unknown[]) => void) {
        observers.push(cb)
      }
      observe() {}
      disconnect() {}
    }
  }
  matchMedia() {
    return { matches: true }
  }
  getComputedStyle(el: FakeEl) {
    return { getPropertyValue: (p: string) => el.style[p] ?? '' }
  }
  requestAnimationFrame(cb: () => void) {
    this.frames.push(cb)
    return this.frames.length
  }
  /** A message event as the browser delivers one: trusted, its source and origin the sender's, its data a structured clone. */
  deliver(origin: string, data: unknown, source: unknown = this.parent) {
    this.dispatch('message', { isTrusted: true, origin, source, data: structuredClone(data) })
  }
  /** A message event the page's own script made and dispatched: whatever it says of its source and origin, untrusted. */
  forge(origin: string, data: unknown) {
    this.dispatch('message', { isTrusted: false, origin, source: this.parent, data })
  }
}

/** A pointer or key event of the browser's (trusted) or the page's own (untrusted), recording whether it was swallowed. */
export function event(type: string, fields: Record<string, unknown> = {}, isTrusted = true) {
  const e = { type, isTrusted, swallowed: false, ...fields, preventDefault() {}, stopImmediatePropagation() { e.swallowed = true } }
  return e
}

/** h('button', {id: 'save'}, 'Save'): an element. */
export function h(tag: string, attrs: Record<string, string> = {}, ...children: Node[]) {
  return new FakeEl(tag, attrs, children)
}
