import { MAX_PREVIEW_REVISION, MAX_PREVIEW_TITLE, MAX_PREVIEW_URL, MAX_PREVIEW_USER_AGENT, PREVIEW_VERSION, type PreviewPage } from '../protocol'
import { createGate, type Answer } from './gate'
import { hintKindsOf } from './hints'
import { cutStart, readElement } from './report'
import { nameOf, pageEpoch, pageText, stackAt, stepStack, walk, walkTree, type El } from './select'

/**
 * The preview bridge (docs/protocol.md, Preview bridge) in the page's window: it does nothing until a workbench origin the server
 * wrote into it says hello from the page's parent window, and it takes a message only through its gate (gate.ts). It reads, never
 * writes, the page: the one thing it adds is its outline's host, an element holding a closed shadow root no style of the page
 * reaches, put on the page when selection mode starts and taken off when it ends. It reads no storage, cookie, console or network,
 * acts only on events the browser delivered, and answers nothing it was not asked.
 */
const LOADED = Symbol.for('conductor.previewBridge')

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

/** Starts the bridge in win, answering the workbench origins given (the server's): none, or a window no parent frames, starts nothing. */
export function startBridge(win: Window & typeof globalThis, origins: readonly string[]): void {
  const flags = win as unknown as Record<symbol, unknown>
  if (origins.length === 0 || win.parent === win || flags[LOADED]) return
  // A second copy (the page's own script tag and the desktop shell's) stays idle.
  Object.defineProperty(win, LOADED, { value: true })
  const doc = win.document
  const gate = createGate(origins, win.parent)
  const answer = (m: Answer) => gate.answer(m)

  // The outline's host, made once and kept while the bridge runs, on the page only in selection mode. The epoch leaves its
  // mutations out, so putting it on and taking it off never says the page moved.
  let host: HTMLElement | null = null
  let box: HTMLElement | null = null
  let label: HTMLElement | null = null
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

  // Selection mode: the outlined element, the pointer, the stack under it.
  let selecting = false
  let current: El | null = null
  let px = 0
  let py = 0

  const show = (el: El | null) => {
    current = el
    if (!box || !label) return
    if (!el) {
      box.style.display = 'none'
      return
    }
    const r = el.getBoundingClientRect()
    Object.assign(box.style, { display: 'block', left: `${r.x}px`, top: `${r.y}px`, width: `${r.width}px`, height: `${r.height}px` })
    const id = pageText(el.id, 64)
    const classes = pageText(el.getAttribute('class'), 128).trim().split(/\s+/).filter(Boolean).slice(0, 2)
    label.textContent = `${nameOf(el, 64)}${id ? '#' + id : ''}${classes.map((c) => '.' + c).join('')}`
  }

  const leave = () => {
    if (!selecting) return
    selecting = false
    for (const [type, fn] of listeners) win.removeEventListener(type, fn, true)
    show(null)
    host?.remove()
  }

  const read = (el: El) => readElement(el, { doc, win })
  const choose = (el: El) => {
    // The outline is off the page before it is read, so no frame holds it.
    leave()
    answer({ t: 'selected', element: read(el), ...page() })
  }

  const swallow = (e: Event) => {
    e.preventDefault()
    e.stopImmediatePropagation()
  }
  const onMove = (e: Event) => {
    if (!e.isTrusted) return
    const p = e as PointerEvent
    px = p.clientX
    py = p.clientY
    show(stackAt(doc, px, py, host)[0] ?? null)
  }
  const onClick = (e: Event) => {
    if (!e.isTrusted) return
    swallow(e)
    if (e.type === 'click' && current) choose(current)
  }
  const onKey = (e: Event) => {
    if (!e.isTrusted) return
    const k = e as KeyboardEvent
    const ways: Record<string, () => El | null> = {
      ArrowDown: () => (k.shiftKey && current ? walk(current, 'next', host) : stepStack(stackAt(doc, px, py, host), current, true)),
      ArrowUp: () => (k.shiftKey && current ? walk(current, 'previous', host) : stepStack(stackAt(doc, px, py, host), current, false)),
      ArrowLeft: () => (current ? walk(current, 'parent', host) : null),
      ArrowRight: () => (current ? walk(current, 'child', host) : null),
    }
    if (k.key === 'Escape') {
      swallow(e)
      leave()
      answer({ t: 'cancelled' })
    } else if (k.key === 'Enter' && current) {
      swallow(e)
      choose(current)
    } else if (Object.hasOwn(ways, k.key)) {
      swallow(e)
      show(ways[k.key]!())
    }
  }
  const listeners: Array<[string, (e: Event) => void]> = [
    ['pointermove', onMove],
    ['pointerdown', onClick],
    ['pointerup', onClick],
    ['mousedown', onClick],
    ['mouseup', onClick],
    ['click', onClick],
    ['keydown', onKey],
  ]

  const enter = () => {
    leave()
    selecting = true
    if (!host) {
      host = doc.createElement('conductor-preview-bridge')
      host.style.cssText = 'all: initial; position: fixed; inset: 0; z-index: 2147483647; pointer-events: none;'
      const root = host.attachShadow({ mode: 'closed' })
      box = doc.createElement('div')
      box.style.cssText = 'position: fixed; display: none; box-sizing: border-box; border: 2px solid #3b82f6; background: rgba(59,130,246,.12); pointer-events: none;'
      label = doc.createElement('div')
      label.style.cssText = 'position: absolute; left: 0; bottom: 100%; font: 12px/1.4 ui-monospace, monospace; color: #fff; background: #1e3a8a; padding: 1px 4px; white-space: nowrap;'
      box.append(label)
      root.append(box)
    }
    doc.documentElement.append(host)
    for (const [type, fn] of listeners) win.addEventListener(type, fn, true)
  }

  win.addEventListener('message', (e) => {
    const m = gate.take(e)
    if (!m) return
    try {
      switch (m.t) {
        case 'hello':
          epoch ??= pageEpoch({ win, doc, mine: (n) => !!host && n === host, observer: (cb) => new win.MutationObserver(cb), frame: (cb) => win.requestAnimationFrame(cb) })
          answer({ t: 'ready', version: PREVIEW_VERSION, hints: hintKindsOf(doc), ...page() })
          break
        case 'select':
          enter()
          break
        case 'describe': {
          let el: El | null = null
          try {
            el = doc.querySelector(m.selector)
          } catch {
            el = null
          }
          answer(el && el !== host ? { t: 'described', element: read(el), ...page() } : { t: 'described', missing: true, ...page() })
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
      // A page whose own properties throw as they are read gets no answer, as a message the gate drops gets none.
    }
  })
}
