import { MAX_PREVIEW_REVISION, MAX_PREVIEW_TITLE, MAX_PREVIEW_URL, MAX_PREVIEW_USER_AGENT, PREVIEW_VERSION, type PreviewPage } from '../protocol'
import { createGate, type Answer } from './gate'
import { hintKindsOf } from './hints'
import { cutStart, readElement } from './report'
import { nameOf, pageEpoch, pageText, stackAt, stepStack, walk, walkTree, type El } from './select'

/**
 * The preview bridge (docs/protocol.md, Preview bridge) in the page's window: it does nothing until a workbench origin the server
 * wrote into it says hello from the page's parent window, and it takes a message only through its gate (gate.ts). It reads, never
 * writes, the page: the one thing it adds is its outline's host, a div holding a closed shadow root no style of the page reaches,
 * put on the page when selection mode starts and taken off when it ends, before anything is read. It reads no storage, cookie,
 * console or network, acts only on events the browser delivered, and answers nothing it was not asked.
 */
const LOADED = Symbol.for('conductor.previewBridge')

/**
 * Every pointer, mouse, touch and key event the browser delivers, which selection mode stops (the page's own listeners never see
 * them) and acts on: its listeners are added and removed from this table alone. A tap's touches and a wheel keep their default
 * action (the click a tap makes, a scroll); the rest have theirs prevented.
 */
export const SELECTION_EVENTS = [
  'pointerover', 'pointerenter', 'pointerdown', 'pointermove', 'pointerrawupdate', 'pointerup', 'pointercancel', 'pointerout', 'pointerleave',
  'gotpointercapture', 'lostpointercapture', 'mouseover', 'mouseenter', 'mousedown', 'mousemove', 'mouseup', 'mouseout', 'mouseleave',
  'click', 'dblclick', 'auxclick', 'contextmenu', 'wheel', 'touchstart', 'touchmove', 'touchend', 'touchcancel', 'keydown', 'keypress', 'keyup',
] as const
const KEEP_DEFAULT = new Set(['wheel', 'touchstart', 'touchmove', 'touchend', 'touchcancel'])

/** v when it is a finite number of at least min, else fallback: what a page's window says may be anything. */
function finite(v: unknown, min: number, fallback: number): number {
  return typeof v === 'number' && Number.isFinite(v) && v >= min ? v : fallback
}

/** The most nodes the revision is looked for among: the root's children and, of the head, its children. */
const HEAD_NODES = 128

/** The page's revision (<meta name="conductor-revision"> in the head) when it is in the schema's form, else "". */
function revisionOf(doc: Document): string {
  let rev = ''
  const meta = (e: El) => nameOf(e, 8) === 'meta' && pageText(e.getAttribute('name'), 32) === 'conductor-revision'
  const visit = (e: El, depth: number) =>
    depth === 2 && meta(e) ? ((rev = pageText(e.getAttribute('content'), MAX_PREVIEW_REVISION).trim().toLowerCase()), null) : depth === 0 || (depth === 1 && nameOf(e, 8) === 'head')
  if (doc.documentElement) walkTree(doc.documentElement, HEAD_NODES, (n, depth) => n.nodeType === 1 && visit(n as El, depth))
  return rev.length <= MAX_PREVIEW_REVISION && /^[0-9a-f]{4,64}(?:\+[0-9a-f]{4,64})?$/.test(rev) ? rev : ''
}

/** Selection mode, while it lasts: the outline's host, its box and label, the outlined element and the pointer. */
interface Selection {
  host: HTMLElement
  box: HTMLElement
  label: HTMLElement
  current: El | null
  px: number
  py: number
}

/** Starts the bridge in win, answering the workbench origins given (the server's): none, or a window no parent frames, starts nothing. */
export function startBridge(win: Window & typeof globalThis, origins: readonly string[]): void {
  const flags = win as unknown as Record<symbol, unknown>
  if (origins.length === 0 || win.parent === win || flags[LOADED]) return
  // A second copy (the page's own script tag and the desktop shell's) stays idle.
  Object.defineProperty(win, LOADED, { value: true })
  const doc = win.document
  // An accepted hello ends what the exchange before it left on the page and the window before the gate takes its nonce.
  const gate = createGate(origins, win.parent, () => leave())
  const answer = (m: Answer) => gate.answer(m)

  // Selection mode's state, null outside it, dropped whole when it ends. Every host it made is known to the epoch, which leaves
  // their coming and going out, so putting one on and taking it off never says the page moved.
  let sel: Selection | null = null
  const hosts = new WeakSet<object>()
  // The epoch starts with the first hello: before it the bridge observes nothing.
  let epoch: { value(): number } | null = null

  const page = (): PreviewPage => ({
    url: cutStart(pageText(win.location?.href, MAX_PREVIEW_URL), MAX_PREVIEW_URL),
    // Characters, not bytes: twice the bound in code units holds the bound in characters, a pair cut there past it.
    title: [...pageText(doc.title, 2 * MAX_PREVIEW_TITLE)].slice(0, MAX_PREVIEW_TITLE).join(''),
    viewport: {
      width: finite(win.innerWidth, 0, 0),
      height: finite(win.innerHeight, 0, 0),
      dpr: finite(win.devicePixelRatio, Number.MIN_VALUE, 1),
      scrollX: finite(win.scrollX, -Infinity, 0),
      scrollY: finite(win.scrollY, -Infinity, 0),
      colorScheme: win.matchMedia?.('(prefers-color-scheme: dark)')?.matches === true ? 'dark' : 'light',
      userAgent: cutStart(pageText(win.navigator.userAgent, MAX_PREVIEW_USER_AGENT), MAX_PREVIEW_USER_AGENT),
    },
    epoch: epoch?.value() ?? 0,
    revision: revisionOf(doc),
  })

  const show = (s: Selection, el: El | null) => {
    s.current = el
    if (!el) {
      s.box.style.display = 'none'
      return
    }
    const r = el.getBoundingClientRect()
    Object.assign(s.box.style, { display: 'block', left: `${r.x}px`, top: `${r.y}px`, width: `${r.width}px`, height: `${r.height}px` })
    const id = pageText(el.id, 64)
    const classes = pageText(el.getAttribute('class'), 128).trim().split(/\s+/).filter(Boolean).slice(0, 2)
    s.label.textContent = `${nameOf(el, 64)}${id ? '#' + id : ''}${classes.map((c) => '.' + c).join('')}`
  }

  // Selection mode's one way out, taken on every end (a pick, Escape, a cancel, a describe, a hello, a select, a throw): the
  // listeners off the window, the host off the page.
  const leave = () => {
    const s = sel
    if (!s) return
    sel = null
    for (const t of SELECTION_EVENTS) win.removeEventListener(t, onEvent, true)
    s.host.remove()
  }

  const read = (el: El) => readElement(el, { doc, win })
  const choose = (el: El) => {
    leave()
    answer({ t: 'selected', element: read(el), ...page() })
  }

  const ways: Record<string, (s: Selection, shift: boolean) => El | null> = {
    ArrowDown: (s, shift) => (shift && s.current ? walk(s.current, 'next', s.host) : stepStack(stackAt(doc, s.px, s.py, s.host), s.current, true)),
    ArrowUp: (s, shift) => (shift && s.current ? walk(s.current, 'previous', s.host) : stepStack(stackAt(doc, s.px, s.py, s.host), s.current, false)),
    ArrowLeft: (s) => (s.current ? walk(s.current, 'parent', s.host) : null),
    ArrowRight: (s) => (s.current ? walk(s.current, 'child', s.host) : null),
  }
  // A pointer move outlines what is under it, the arrows step and walk, a click or the release of Enter picks, the release of
  // Escape leaves. Every event of the table is stopped; a throw while handling one leaves selection mode.
  const onEvent = (e: Event) => {
    const s = sel
    if (!e.isTrusted || !s) return
    if (!KEEP_DEFAULT.has(e.type)) e.preventDefault()
    e.stopImmediatePropagation()
    const k = e as KeyboardEvent
    try {
      if (e.type === 'pointermove') {
        s.px = (e as PointerEvent).clientX
        s.py = (e as PointerEvent).clientY
        show(s, stackAt(doc, s.px, s.py, s.host)[0] ?? null)
      } else if (e.type === 'click' && s.current) choose(s.current)
      else if (e.type === 'keydown' && Object.hasOwn(ways, k.key)) show(s, ways[k.key]!(s, k.shiftKey))
      else if (e.type === 'keyup' && k.key === 'Enter' && s.current) choose(s.current)
      else if (e.type === 'keyup' && k.key === 'Escape') {
        leave()
        answer({ t: 'cancelled' })
      }
    } catch {
      leave()
    }
  }

  // Selection mode's one way in: out of any before it, its host (a div, which no custom element of the page's names) made and
  // wired whole before the state says it exists, then put on the page and the table's listeners added.
  const enter = () => {
    leave()
    const host = doc.createElement('div')
    host.style.cssText = 'all: initial; position: fixed; inset: 0; z-index: 2147483647; pointer-events: none;'
    const root = host.attachShadow({ mode: 'closed' })
    const box = doc.createElement('div')
    box.style.cssText = 'position: fixed; display: none; box-sizing: border-box; border: 2px solid #3b82f6; background: rgba(59,130,246,.12); pointer-events: none;'
    const label = doc.createElement('div')
    label.style.cssText = 'position: absolute; left: 0; bottom: 100%; font: 12px/1.4 ui-monospace, monospace; color: #fff; background: #1e3a8a; padding: 1px 4px; white-space: nowrap;'
    box.append(label)
    root.append(box)
    hosts.add(host)
    sel = { host, box, label, current: null, px: 0, py: 0 }
    doc.documentElement.append(host)
    for (const t of SELECTION_EVENTS) win.addEventListener(t, onEvent, true)
  }

  win.addEventListener('message', (e) => {
    const m = gate.take(e)
    if (!m) return
    try {
      switch (m.t) {
        case 'hello':
          epoch ??= pageEpoch({ win, doc, mine: (n) => hosts.has(n as object), observer: (cb) => new win.MutationObserver(cb), frame: (cb) => win.requestAnimationFrame(cb) })
          answer({ t: 'ready', version: PREVIEW_VERSION, hints: hintKindsOf(doc), ...page() })
          break
        case 'select':
          enter()
          break
        case 'describe': {
          // Nothing is read with the outline on the page: a describe ends selection mode first, as a pick does.
          leave()
          let el: El | null = null
          try {
            el = doc.querySelector(m.selector)
          } catch {
            el = null
          }
          answer(el ? { t: 'described', element: read(el), ...page() } : { t: 'described', missing: true, ...page() })
          break
        }
        case 'look':
          answer({ t: 'looked', ...page() })
          break
        case 'cancel':
          leave()
          answer({ t: 'cancelled' })
          break
      }
    } catch {
      // A page whose own properties throw as they are read gets no answer, as a message the gate drops gets none, and selection
      // mode is left.
      leave()
    }
  })
}
