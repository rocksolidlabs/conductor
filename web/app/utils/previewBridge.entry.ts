import { MAX_PREVIEW_REVISION, MAX_PREVIEW_TITLE, MAX_PREVIEW_URL, MAX_PREVIEW_USER_AGENT, PREVIEW_VERSION, wellFormed, type PreviewPage } from './protocol'
import { createGate, originsOf, type Answer } from './previewBridge/gate'
import { hintKindsOf } from './previewBridge/hints'
import { cutStart, readElement } from './previewBridge/report'
import { pageEpoch, stackAt, stepStack, walk, type El } from './previewBridge/select'

/**
 * The preview bridge (docs/protocol.md, Preview bridge): one classic script a previewed page loads (or the desktop shell puts in
 * it), which reads the page for the workbench that frames it. It does nothing until a workbench origin the server wrote in below
 * says hello from the page's parent window; it reads, never writes, the page, and reads no storage, cookie, console or network.
 * Selection mode draws its outline in a closed shadow root of its own and takes pointer events in the capture phase while it
 * selects. Built by Vite's library build (web/nuxt.config.ts) into the app's output as /preview-bridge.js; the server serves it
 * with its own workbench origins in place of the marker.
 */
// The marker, a string literal the server replaces with a JSON array of origins: unreplaced, the bridge answers no one.
const ORIGINS: unknown = "__CONDUCTOR_WORKBENCH_ORIGINS__"

const LOADED = Symbol.for('conductor.previewBridge')

function start() {
  const win = window
  const doc = document
  if (win.parent === win || (win as unknown as Record<symbol, boolean>)[LOADED]) return
  Object.defineProperty(win, LOADED, { value: true })
  const origins = originsOf(ORIGINS)
  if (origins.length === 0) return
  const gate = createGate(origins, win.parent)

  // The outline, in a closed shadow root no style of the page reaches.
  let host: HTMLElement | null = null
  let box: HTMLElement | null = null
  let label: HTMLElement | null = null
  const epoch = pageEpoch({ win, doc, host: () => host, observer: (cb) => new MutationObserver(cb), frame: (cb) => requestAnimationFrame(cb) })

  const page = (): PreviewPage => {
    const meta = doc.querySelector('meta[name="conductor-revision"]')?.getAttribute('content')?.trim().toLowerCase() ?? ''
    return {
      url: cutStart(wellFormed(location.href), MAX_PREVIEW_URL),
      title: [...wellFormed(doc.title)].slice(0, MAX_PREVIEW_TITLE).join(''),
      viewport: {
        width: win.innerWidth,
        height: win.innerHeight,
        dpr: win.devicePixelRatio || 1,
        scrollX: win.scrollX,
        scrollY: win.scrollY,
        colorScheme: win.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light',
        userAgent: cutStart(wellFormed(navigator.userAgent), MAX_PREVIEW_USER_AGENT),
      },
      epoch: epoch.value(),
      revision: meta.length <= MAX_PREVIEW_REVISION && /^[0-9a-f]{4,64}(?:\+[0-9a-f]{4,64})?$/.test(meta) ? meta : '',
    }
  }
  const answer = (m: Answer) => gate.answer(m)

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
    const cls = [...el.classList].slice(0, 2).map((c) => '.' + c).join('')
    const name = (el.getAttribute('aria-label') || el.textContent || '').replace(/\s+/g, ' ').trim().slice(0, 40)
    label.textContent = `${el.tagName.toLowerCase()}${cls}${name ? ` "${name}"` : ''}`
  }

  const leave = () => {
    if (!selecting) return
    selecting = false
    for (const [type, fn] of listeners) win.removeEventListener(type, fn, true)
    host?.remove()
    host = box = label = null
    current = null
  }

  const choose = (el: El) => {
    // The outline is off the page before it is read, so no frame holds it.
    leave()
    answer({ t: 'selected', element: readElement(el, { doc, win, host: null }), ...page() })
  }

  const swallow = (e: Event) => {
    e.preventDefault()
    e.stopImmediatePropagation()
  }
  const onMove = (e: Event) => {
    const p = e as PointerEvent
    px = p.clientX
    py = p.clientY
    show(stackAt(doc, px, py, host)[0] ?? null)
  }
  const onClick = (e: Event) => {
    swallow(e)
    if (e.type === 'click' && current) choose(current)
  }
  const onKey = (e: Event) => {
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
    } else if (ways[k.key]) {
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
    host = doc.createElement('conductor-preview-bridge')
    host.style.cssText = 'all: initial; position: fixed; inset: 0; z-index: 2147483647; pointer-events: none;'
    const root = host.attachShadow({ mode: 'closed' })
    box = doc.createElement('div')
    box.style.cssText = 'position: fixed; display: none; box-sizing: border-box; border: 2px solid #3b82f6; background: rgba(59,130,246,.12); pointer-events: none;'
    label = doc.createElement('div')
    label.style.cssText = 'position: absolute; left: 0; bottom: 100%; font: 12px/1.4 ui-monospace, monospace; color: #fff; background: #1e3a8a; padding: 1px 4px; white-space: nowrap;'
    box.append(label)
    root.append(box)
    doc.documentElement.append(host)
    for (const [type, fn] of listeners) win.addEventListener(type, fn, true)
  }

  win.addEventListener('message', (e) => {
    const m = gate.take(e)
    if (!m) return
    switch (m.t) {
      case 'hello':
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
        answer(el ? { t: 'described', element: readElement(el, { doc, win, host: null }), ...page() } : { t: 'described', missing: true, ...page() })
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
  })
}

start()
