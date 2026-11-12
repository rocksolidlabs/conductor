/**
 * A small DOM for the bridge's tests (vitest runs in node): elements and text nodes, attributes, a selector matcher for what the
 * bridge writes and the tests ask (tags, *, #id, .class, [attr], :root, :nth-child, :nth-of-type, :disabled, :hover, lists and the
 * child combinator), and a document whose hit tests the test sets. Never imported by the bridge itself.
 *
 * It counts what is read of it (reads): every node a link of the DOM reaches (parentNode, firstChild, nextSibling and the
 * element links, and the nodes an engine's search or serialiser goes through: querySelectorAll, closest, contains, outerHTML,
 * textContent, importNode), every item of a list (attributes, children, childNodes, classList, animations, a hit test's stack,
 * a query's result) and every code unit of a page's string a slice gives. With pageStrings on, the page's strings (names, ids,
 * attributes, text, title, URL, styles) are values that answer only their length and a slice: anything else asked of them throws.
 */
export const reads = { nodes: 0, items: 0, units: 0 }

// CSS.escape, as CSSOM serialises an identifier, which a browser has and node does not.
;(globalThis as { CSS?: unknown }).CSS ??= {
  escape: (s: string) =>
    [...s]
      .map((ch, i) => {
        const c = ch.codePointAt(0)!
        if (c === 0) return '�'
        if (c <= 0x1f || c === 0x7f || (/[0-9]/.test(ch) && (i === 0 || (i === 1 && s[0] === '-')))) return `\\${c.toString(16)} `
        if (s === '-') return '\\-'
        return c >= 0x80 || /[\w-]/.test(ch) ? ch : '\\' + ch
      })
      .join(''),
}

export function resetReads() {
  reads.nodes = reads.items = reads.units = 0
}

let guarded = false

/** Whether the page's strings are given as values that answer only their length and a slice. */
export function pageStrings(on: boolean) {
  guarded = on
}

class PageString {
  constructor(private readonly s: string) {}
  get length() {
    return this.s.length
  }
  slice(a?: number, b?: number): string {
    const out = this.s.slice(a, b)
    reads.units += out.length
    return out
  }
}

/** s as the page gives it: itself, or with pageStrings on a value that answers only its length and a slice. */
function pg(s: string): string {
  if (!guarded) return s
  const p = new PageString(s)
  return new Proxy(p, {
    get(t, k) {
      if (k === 'length') return t.length
      if (k === 'slice') return t.slice.bind(t)
      throw new Error(`a page's string was asked ${String(k)}`)
    },
  }) as unknown as string
}

/** items as a list the page gives: each item read counted. */
function counted<T>(items: T[]): T[] {
  return new Proxy(items, {
    get(t, k, r) {
      if (typeof k === 'string' && /^\d+$/.test(k)) reads.items++
      if (k === 'item') return (i: number) => (reads.items++, t[i] ?? null)
      if (k === Symbol.iterator) return function* () {
        for (const x of t) {
          reads.items++
          yield x
        }
      }
      return Reflect.get(t, k, r)
    },
  })
}

abstract class FakeNode {
  _parent: FakeEl | null = null
  /** Its place among its parent's nodes, and among its parent's elements. */
  _i = 0
  _ei = 0
  abstract readonly nodeType: number
  private sibling(d: number, element: boolean): FakeNode | null {
    const s = this._parent?._kids ?? []
    let i = this._i + d
    reads.nodes++
    // An element link steps over every node between: each is visited.
    while (element && s[i] && !(s[i] instanceof FakeEl)) (reads.nodes++, (i += d))
    return s[i] ?? null
  }
  get parentNode(): FakeEl | null {
    reads.nodes++
    return this._parent
  }
  get parentElement(): FakeEl | null {
    reads.nodes++
    return this._parent
  }
  get nextSibling() {
    return this.sibling(1, false)
  }
  get previousSibling() {
    return this.sibling(-1, false)
  }
  get nextElementSibling() {
    return this.sibling(1, true) as FakeEl | null
  }
  get previousElementSibling() {
    return this.sibling(-1, true) as FakeEl | null
  }
  remove() {
    if (!this._parent) return
    const self: FakeNode = this
    this._parent._kids = this._parent._kids.filter((c) => c !== self)
    let e = 0
    this._parent._kids.forEach((c, i) => ((c._i = i), (c._ei = c instanceof FakeEl ? e++ : e)))
    this._parent._elements = e
    this._parent = null
  }
}

export class FakeText extends FakeNode {
  readonly nodeType = 3
  constructor(public text: string) {
    super()
  }
  get data() {
    return pg(this.text)
  }
}

export type Kid = FakeEl | FakeText | string

export class FakeEl extends FakeNode {
  readonly nodeType = 1
  readonly tag: string
  _kids: Array<FakeEl | FakeText> = []
  _elements = 0
  attrs = new Map<string, string>()
  rect = { x: 0, y: 0, width: 0, height: 0 }
  /** What getComputedStyle answers for it. */
  computed: Record<string, string> = {}
  hovered = false
  running = 0
  shadow: FakeEl | null = null
  /** What a person typed: the page's own, which the bridge never reads. */
  value = ''
  /** A template's content (a fragment of its own, not its children). */
  content: FakeEl | null = null
  /** Its inline style: cssText is its style attribute, as the DOM has it. */
  readonly style: Record<string, string> = Object.defineProperty({} as Record<string, string>, 'cssText', {
    get: () => this.attrs.get('style') ?? '',
    set: (v: string) => this.attrs.set('style', v),
  })
  ;[k: string]: unknown

  constructor(tag: string, attrs: Record<string, string> = {}, children: Kid[] = []) {
    super()
    this.tag = tag.toUpperCase()
    for (const [k, v] of Object.entries(attrs)) this.attrs.set(k, v)
    this.append(...children)
  }

  append(...nodes: Kid[]) {
    for (const c of nodes) {
      const n = typeof c === 'string' ? new FakeText(c) : c
      n.remove()
      n._parent = this
      n._i = this._kids.length
      n._ei = this._elements
      if (n instanceof FakeEl) this._elements++
      this._kids.push(n)
    }
  }
  get tagName() {
    return pg(this.tag)
  }
  get localName() {
    return pg(this.tag.toLowerCase())
  }
  get elements(): FakeEl[] {
    return this._kids.filter((c): c is FakeEl => c instanceof FakeEl)
  }
  get children() {
    return counted(this.elements)
  }
  get childNodes() {
    return counted([...this._kids])
  }
  get firstChild() {
    reads.nodes++
    return this._kids[0] ?? null
  }
  get firstElementChild(): FakeEl | null {
    reads.nodes++
    for (const c of this._kids) {
      if (c instanceof FakeEl) return c
      reads.nodes++
    }
    return null
  }
  get id() {
    return pg(this.attrs.get('id') ?? '')
  }
  get classList() {
    return counted((this.attrs.get('class') ?? '').split(/\s+/).filter(Boolean).map(pg))
  }
  get attributes() {
    return counted([...this.attrs].map(([name, value]) => ({ name: pg(name), value: pg(value) })))
  }
  getAttribute(k: string) {
    const v = this.attrs.get(k)
    return v === undefined ? null : pg(v)
  }
  hasAttribute(k: string) {
    return this.attrs.has(k)
  }
  removeAttribute(k: string) {
    this.attrs.delete(k)
  }
  /** Its text, every node of its subtree visited. */
  get textContent(): string {
    return pg(serialise(this, false))
  }
  set textContent(s: string) {
    this._kids = []
    this._elements = 0
    if (s) this.append(s)
  }
  /** Its markup, every node of its subtree visited. */
  get outerHTML(): string {
    return pg(serialise(this, true))
  }
  /** The page's own copy, which the bridge never makes. */
  cloneNode(_deep: boolean): FakeEl {
    throw new Error('the bridge copied an element in the page’s own document')
  }
  get isConnected(): boolean {
    let e: FakeEl = this
    while (e._parent) e = e._parent
    return e.tag === 'HTML'
  }
  /** Editable when it or an ancestor says contenteditable (and not "false"), as the engine's style says. */
  get isContentEditable(): boolean {
    for (let e: FakeEl | null = this; e; e = e._parent) {
      const v = e.attrs.get('contenteditable')
      if (v !== undefined) return v !== 'false'
    }
    return false
  }
  attachShadow(_init: { mode: string }) {
    if (this.shadow) throw new Error('NotSupportedError: the element already hosts a shadow root')
    this.shadow = new FakeEl('#shadow-root')
    return this.shadow
  }
  contains(other: FakeEl | null): boolean {
    for (let e = other; e; e = e._parent) {
      reads.nodes++
      if (e === this) return true
    }
    return false
  }
  /** Its descendant elements in tree order (the test's own look, nothing counted). */
  descendants(): FakeEl[] {
    return this.elements.flatMap((c) => [c, ...c.descendants()])
  }
  querySelectorAll(sel: string): FakeEl[] {
    const all = this.descendants()
    reads.nodes += all.length
    return counted(all.filter((e) => matches(e, sel)))
  }
  querySelector(sel: string): FakeEl | null {
    return first([...this.descendants()], sel)
  }
  matches(sel: string): boolean {
    return matches(this, sel)
  }
  closest(sel: string): FakeEl | null {
    for (let e: FakeEl | null = this; e; e = e._parent) {
      reads.nodes++
      if (matches(e, sel)) return e
    }
    return null
  }
  getBoundingClientRect() {
    return this.rect
  }
  getAnimations(opts?: { subtree?: boolean }): Array<{ playState: string }> {
    const own = Array.from({ length: this.running }, () => ({ playState: 'running' }))
    if (opts?.subtree) reads.nodes += this.descendants().length
    return counted(opts?.subtree ? [...own, ...this.descendants().flatMap((d) => Array.from({ length: d.running }, () => ({ playState: 'running' })))] : own)
  }
}

/** The first of list, in its order, that sel matches: every element before it visited. */
function first(list: FakeEl[], sel: string): FakeEl | null {
  for (const e of list) {
    reads.nodes++
    if (matches(e, sel)) return e
  }
  return null
}

const VOID = new Set(['area', 'base', 'br', 'col', 'embed', 'hr', 'img', 'input', 'link', 'meta', 'source', 'track', 'wbr'])

/** n's markup (or its text alone), as a serialiser of the page's writes it: every node of the subtree visited. */
function serialise(n: FakeEl | FakeText, markup: boolean): string {
  reads.nodes++
  if (n instanceof FakeText) return markup ? n.text.replace(/&/g, '&amp;').replace(/</g, '&lt;') : n.text
  const inner = n._kids.map((c) => serialise(c, markup)).join('')
  if (!markup) return inner
  const tag = n.tag.toLowerCase()
  const attrs = [...n.attrs].map(([k, v]) => ` ${k}="${v.replace(/&/g, '&amp;').replace(/"/g, '&quot;')}"`).join('')
  return VOID.has(tag) ? `<${tag}${attrs}>` : `<${tag}${attrs}>${inner}</${tag}>`
}

/** The test's own look at the text of n's subtree: nothing counted, nothing guarded. */
export function plainText(n: FakeEl): string {
  return n._kids.map((c) => (c instanceof FakeText ? c.text : plainText(c))).join('')
}

const PART = /#[\w-]+|\.[\w-]+|\[[\w-]+(?:="[^"]*")?\]|:nth-of-type\(\d+\)|:nth-child\(\d+\)|:root|:disabled|:hover/g
const COMPOUND = new RegExp(`^([a-z][a-z0-9-]*|\\*)?((?:${PART.source})*)$`)

function compound(e: FakeEl, s: string): boolean {
  const m = COMPOUND.exec(s)
  if (!m) throw new SyntaxError(`not a selector the fake reads: ${s}`)
  if (m[1] && m[1] !== '*' && m[1] !== e.tag.toLowerCase()) return false
  for (const part of m[2]!.match(PART) ?? []) {
    if (part[0] === '#' && (e.attrs.get('id') ?? '') !== part.slice(1)) return false
    if (part[0] === '.' && !(e.attrs.get('class') ?? '').split(/\s+/).includes(part.slice(1))) return false
    if (part[0] === '[') {
      const [name, value] = part.slice(1, -1).split('=')
      if (!e.attrs.has(name!) || (value !== undefined && e.attrs.get(name!) !== value.slice(1, -1))) return false
    }
    const n = Number(/\d+/.exec(part)?.[0])
    if (part.startsWith(':nth-of-type') && (e._parent?.elements ?? [e]).filter((c) => c.tag === e.tag).indexOf(e) + 1 !== n) return false
    if (part.startsWith(':nth-child') && e._ei + 1 !== n) return false
    if (part === ':root' && (e._parent || e.tag !== 'HTML')) return false
    if (part === ':disabled' && !e.attrs.has('disabled')) return false
    if (part === ':hover' && !e.hovered) return false
  }
  return true
}

const parsed = new Map<string, string[][]>()

function matches(e: FakeEl, sel: string): boolean {
  if (!parsed.has(sel)) parsed.set(sel, sel.split(',').map((one) => one.trim().split(/\s*>\s*/)))
  return parsed.get(sel)!.some((parts) => {
    let at: FakeEl | null = e
    for (let i = parts.length - 1; i >= 0; i--) {
      if (!at || !compound(at, parts[i]!)) return false
      at = at._parent
    }
    return true
  })
}

/** el copied, as importNode copies it into a document with no window: every node visited. */
function copy(el: FakeEl): FakeEl {
  reads.nodes++
  const out = new FakeEl(el.tag, Object.fromEntries(el.attrs))
  for (const c of el._kids) out.append(c instanceof FakeText ? (reads.nodes++, c.text) : copy(c))
  return out
}

type Listener = (e: unknown) => void

/** Listeners by type, as a window or a document keeps them (the capture flag is ignored: the fakes dispatch to every one). */
class Listeners {
  readonly on = new Map<string, Set<Listener>>()
  /** What a listener threw: the browser reports it and goes on, as the fake does. */
  readonly thrown: unknown[] = []
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
    for (const fn of [...(this.on.get(type) ?? [])]) {
      try {
        fn(e)
      } catch (err) {
        this.thrown.push(err)
      }
    }
  }
}

/** A document over root (the html element); the test sets what its hit tests return. */
export class FakeDoc extends Listeners {
  hitAt: (x: number, y: number) => FakeEl[] = () => []
  private _title = ''
  /** The page's custom elements: a name the page defined runs its constructor as the element is made. */
  readonly registry = new Map<string, (el: FakeEl) => void>()
  readonly implementation = { createHTMLDocument: (_title: string) => ({ importNode: (el: FakeEl, _deep: boolean) => copy(el) }) }
  constructor(readonly documentElement: FakeEl) {
    super()
  }
  get title() {
    return pg(this._title)
  }
  set title(s: string) {
    this._title = s
  }
  createElement(tag: string) {
    const el = new FakeEl(tag)
    this.registry.get(tag)?.(el)
    return el
  }
  querySelectorAll(sel: string): FakeEl[] {
    const all = [this.documentElement, ...this.documentElement.descendants()]
    reads.nodes += all.length
    return counted(all.filter((e) => matches(e, sel)))
  }
  /** The first element sel matches; a lone #id as an engine answers it, from its map of ids. */
  querySelector(sel: string): FakeEl | null {
    if (/^#[\w-]+$/.test(sel)) return this.getElementById(sel.slice(1))
    return first([this.documentElement, ...this.documentElement.descendants()], sel)
  }
  /** The first element with the id, as an engine answers it from its map of ids: one node. */
  getElementById(id: string) {
    reads.nodes++
    const find = (e: FakeEl): FakeEl | null => {
      if (e.attrs.get('id') === id) return e
      for (const c of e.elements) {
        const f = find(c)
        if (f) return f
      }
      return null
    }
    return find(this.documentElement)
  }
  elementsFromPoint(x: number, y: number) {
    return counted(this.hitAt(x, y))
  }
  elementFromPoint(x: number, y: number) {
    reads.nodes++
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
  href = 'http://127.0.0.1:5173/'
  userAgent = 'Mozilla/5.0 (a test)'
  declare readonly location: { readonly href: string }
  declare readonly navigator: { readonly userAgent: string }
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
    Object.defineProperty(this, 'location', { value: {} })
    Object.defineProperty(this.location, 'href', { get: () => pg(this.href) })
    Object.defineProperty(this, 'navigator', { value: {} })
    Object.defineProperty(this.navigator, 'userAgent', { get: () => pg(this.userAgent) })
  }
  matchMedia() {
    return { matches: true }
  }
  getComputedStyle(el: FakeEl) {
    return { getPropertyValue: (p: string) => pg(el.computed[p] ?? '') }
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
export function h(tag: string, attrs: Record<string, string> = {}, ...children: Kid[]) {
  return new FakeEl(tag, attrs, children)
}
