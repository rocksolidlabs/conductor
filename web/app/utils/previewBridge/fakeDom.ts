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
  cloneNode(_deep: boolean): FakeEl {
    const out = new FakeEl(this.tagName, Object.fromEntries(this.attrs))
    for (const c of this.childNodes) out.append(typeof c === 'string' ? c : c.cloneNode(true))
    return out
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

const COMPOUND = /^([a-z][a-z0-9-]*|\*)?((?:#[\w-]+|\.[\w-]+|\[[\w-]+\]|:nth-of-type\(\d+\)|:disabled|:hover)*)$/

function compound(e: FakeEl, s: string): boolean {
  const m = COMPOUND.exec(s)
  if (!m) throw new SyntaxError(`not a selector the fake reads: ${s}`)
  if (m[1] && m[1] !== '*' && m[1] !== e.tagName.toLowerCase()) return false
  for (const part of m[2]!.match(/#[\w-]+|\.[\w-]+|\[[\w-]+\]|:nth-of-type\(\d+\)|:disabled|:hover/g) ?? []) {
    if (part[0] === '#' && e.id !== part.slice(1)) return false
    if (part[0] === '.' && !e.classList.includes(part.slice(1))) return false
    if (part[0] === '[' && !e.hasAttribute(part.slice(1, -1))) return false
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

/** A document over root (the html element); the test sets what its hit tests return. */
export class FakeDoc {
  hitAt: (x: number, y: number) => FakeEl[] = () => []
  constructor(readonly documentElement: FakeEl) {}
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

/** h('button', {id: 'save'}, 'Save'): an element. */
export function h(tag: string, attrs: Record<string, string> = {}, ...children: Node[]) {
  return new FakeEl(tag, attrs, children)
}
