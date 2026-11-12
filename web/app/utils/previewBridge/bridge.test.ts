import { describe, expect, it } from 'vitest'
import { ELEMENT_BOUNDS, PREVIEW_MARK, parsePreviewMessage, type PreviewToWorkbench } from '../protocol'
import * as bridge from './bridge'
import { startBridge } from './bridge'
import { HINT_NODES } from './hints'

/** The event names selection mode stops (none where the bridge has no such table). */
const SELECTION_EVENTS: readonly string[] = (bridge as { SELECTION_EVENTS?: readonly string[] }).SELECTION_EVENTS ?? []
import { FakeDoc, FakeWin, comment, event, h, keysCounted, pageStrings, plainText, reads, resetReads, type FakeEl } from './fakeDom'
import { fixture } from './fixture'

const WORKBENCH = 'http://127.0.0.1:8080'
const NONCE = 'n0nce-of-the-hello-1234'
const m = (t: string, extra: Record<string, unknown> = {}, nonce = NONCE) => ({ conductor: PREVIEW_MARK, t, nonce, ...extra })

/** A framed page over root with the bridge started in it, answering WORKBENCH alone. */
function framed(root: FakeEl) {
  const doc = new FakeDoc(root)
  const win = new FakeWin(doc)
  startBridge(win as unknown as Window & typeof globalThis, [WORKBENCH])
  /** What the bridge posted since the last look, each held to the workbench's parser and addressed to WORKBENCH alone. */
  const answers = () => {
    const out = win.posts.splice(0).map((p) => {
      expect(p.target).toBe(WORKBENCH)
      expect(parsePreviewMessage(p.message, 'workbench', NONCE), JSON.stringify(p.message).slice(0, 200)).not.toBeNull()
      return p.message as PreviewToWorkbench
    })
    return out
  }
  return { doc, win, answers, ask: (t: string, extra: Record<string, unknown> = {}) => win.deliver(WORKBENCH, m(t, extra)) }
}

/** A page with no framework, no title and no revision: the most common page there is. */
function plainPage() {
  const p = h('p', {}, 'Hello')
  const root = h('html', {}, h('head'), h('body', {}, p))
  return { root, p }
}

/** The outline's host on the page: the element the bridge puts there, known by its style. */
const hostOf = (root: FakeEl) => root.elements.find((c) => (c.attrs.get('style') ?? '').includes('2147483647')) ?? null
const OTHER_NONCE = 'a-later-hello-nonce-5678'

describe('selection mode', () => {
  it('ends with a later hello, which is sent nothing of the exchange before it', () => {
    const { root, p } = plainPage()
    const { doc, win, ask } = framed(root)
    doc.hitAt = () => [p, root]
    ask('hello')
    ask('select')
    win.dispatch('pointermove', event('pointermove', { clientX: 1, clientY: 1 }))
    win.deliver(WORKBENCH, m('hello', {}, OTHER_NONCE))
    expect([hostOf(root), win.count]).toEqual([null, 3])
    win.dispatch('pointermove', event('pointermove', { clientX: 1, clientY: 1 }))
    const click = event('click')
    win.dispatch('click', click)
    expect(click.swallowed).toBe(false)
    expect(win.posts.map((x) => [(x.message as { t: string }).t, (x.message as { nonce: string }).nonce])).toEqual([['ready', NONCE], ['ready', OTHER_NONCE]])
  })

  it('ends before a describe reads, so no answer holds anything of the bridge’s', () => {
    const { root } = plainPage()
    const { win, answers, ask } = framed(root)
    ask('hello')
    for (const selector of [':root', ':root > body:nth-child(2)']) {
      ask('select')
      ask('describe', { selector })
      expect([hostOf(root), win.count]).toEqual([null, 3])
    }
    const [, html, body] = answers() as Array<Extract<PreviewToWorkbench, { t: 'described' }>>
    expect([html!.element!.html, body!.element!.neighbourhood].join('')).not.toMatch(/2147483647|shadow/)
    expect(html!.element!.html).toBe('<html><head></head><body><p>Hello</p></body></html>')
  })

  it('draws its outline whatever the page has registered, and a throw in its handlers leaves selection mode clean', () => {
    const { root, p } = plainPage()
    const { doc, win, answers, ask } = framed(root)
    doc.registry.set('conductor-preview-bridge', (el) => el.attachShadow({ mode: 'open' }))
    doc.hitAt = () => [p, root]
    ask('hello')
    ask('select')
    win.dispatch('pointermove', event('pointermove', { clientX: 1, clientY: 1 }))
    const box = hostOf(root)?.shadow?.elements[0]
    expect(box?.style.display).toBe('block')
    p.getBoundingClientRect = () => {
      throw new Error('a getter of the page’s')
    }
    win.dispatch('pointermove', event('pointermove', { clientX: 2, clientY: 2 }))
    expect([hostOf(root), win.count]).toEqual([null, 3])
    answers()
    ask('hello')
    expect(answers().map((a) => a.t)).toEqual(['ready'])
  })

  it('stops the pointer, mouse, touch and key events of its table while it selects, and none outside', () => {
    const { root, p } = plainPage()
    const { doc, win, answers, ask } = framed(root)
    ask('hello')
    // Each event of the table, with a key that does nothing and nothing under the pointer: none picks or leaves.
    const each = (trusted = true) =>
      SELECTION_EVENTS.map((t) => {
        const e = event(t, { key: 'a' }, trusted)
        win.dispatch(t, e)
        return e.swallowed
      })
    expect(each()).toEqual(SELECTION_EVENTS.map(() => false))
    ask('select')
    expect(win.count).toBe(3 + SELECTION_EVENTS.length)
    expect(each(false)).toEqual(SELECTION_EVENTS.map(() => false))
    expect(each()).toEqual(SELECTION_EVENTS.map(() => true))
    expect(SELECTION_EVENTS).toEqual(expect.arrayContaining(['keyup', 'touchstart', 'touchend', 'pointerdown', 'mousedown', 'click', 'contextmenu', 'wheel']))
    // The key that picks is stopped to its release, and what follows the pick reaches the page.
    doc.hitAt = () => [p, root]
    win.dispatch('pointermove', event('pointermove', { clientX: 1, clientY: 1 }))
    const down = event('keydown', { key: 'Enter' })
    const up = event('keyup', { key: 'Enter' })
    win.dispatch('keydown', down)
    expect(answers().map((a) => a.t)).toEqual(['ready'])
    win.dispatch('keyup', up)
    const after = event('keyup', { key: 'Enter' })
    win.dispatch('keyup', after)
    expect([down.swallowed, up.swallowed, after.swallowed, answers().map((a) => a.t), win.count]).toEqual([true, true, false, ['selected'], 3])
  })

  /** A page of two buttons, the first under the point (1, 10) until the page scrolls, the second after. */
  function twoButtons() {
    const [one, two] = [h('button', { id: 'one' }, 'One'), h('button', { id: 'two' }, 'Two')]
    const root = h('html', {}, h('head'), h('body', {}, one, two))
    const page = framed(root)
    let scrolled = false
    page.doc.hitAt = (_x, y) => [y < 50 === !scrolled ? one : two, root.children[1]!, root]
    page.ask('hello')
    page.answers()
    const scroll = () => ((scrolled = true), page.win.dispatch('scroll', event('scroll')))
    const box = () => hostOf(root)?.shadow?.elements[0]?.style.display
    const picked = () => page.answers().map((a) => (a.t === 'selected' ? a.element.id : a.t))
    return { ...page, root, one, two, scroll, box, picked }
  }

  it('picks the element under the click’s own point, whether a move came before it or not', () => {
    const p = twoButtons()
    p.ask('select')
    p.win.dispatch('click', event('click', { clientX: 1, clientY: 10 }))
    expect([p.picked(), hostOf(p.root) !== null, p.win.count]).toEqual([['one'], false, 3])
    // A move over the first, a click at the second's point: the second.
    p.ask('select')
    p.win.dispatch('pointermove', event('pointermove', { clientX: 1, clientY: 10 }))
    p.win.dispatch('click', event('click', { clientX: 1, clientY: 60 }))
    expect(p.picked()).toEqual(['two'])
  })

  it('takes the outline off when the page scrolls, so a click picks what is under it then and Enter picks nothing', () => {
    const p = twoButtons()
    p.ask('select')
    p.win.dispatch('pointermove', event('pointermove', { clientX: 1, clientY: 10 }))
    const seen: unknown[] = [p.box()]
    p.scroll()
    seen.push(p.box())
    // Enter with nothing outlined: nothing sent, selection mode still on.
    p.win.dispatch('keydown', event('keydown', { key: 'Enter' }))
    p.win.dispatch('keyup', event('keyup', { key: 'Enter' }))
    seen.push(p.picked(), hostOf(p.root) !== null, p.win.count)
    // The click at the point the pointer stayed at picks what the scroll brought there.
    p.win.dispatch('click', event('click', { clientX: 1, clientY: 10 }))
    seen.push(p.picked(), hostOf(p.root) !== null)
    expect(seen).toEqual(['block', 'none', [], true, 3 + SELECTION_EVENTS.length, ['two'], false])
  })

  it('picks nothing with Enter when the outlined element has left the page, and stays in selection mode', () => {
    const p = twoButtons()
    p.ask('select')
    p.win.dispatch('pointermove', event('pointermove', { clientX: 1, clientY: 10 }))
    const seen: unknown[] = [p.box()]
    p.one.remove()
    p.win.observers[0]!([{ type: 'childList', target: p.root.children[1], addedNodes: [], removedNodes: [p.one] }])
    p.win.dispatch('keydown', event('keydown', { key: 'Enter' }))
    const up = event('keyup', { key: 'Enter' })
    p.win.dispatch('keyup', up)
    seen.push(up.swallowed, p.picked(), hostOf(p.root) !== null, p.win.count)
    expect(seen).toEqual(['block', true, [], true, 3 + SELECTION_EVENTS.length])
  })

  it('outlines a frame’s element from an event aimed at it, and its box then takes the pointer, so a click there picks it', () => {
    const p = plainPage()
    const frame = h('iframe', { id: 'frame', src: '/inner.html' })
    p.root.children[1]!.append(frame)
    const { doc, win, answers, ask } = framed(p.root)
    // A real hit test lists the host first where its box takes the pointer; the bridge leaves it out.
    doc.hitAt = (x) => (x >= 100 ? [hostOf(p.root), frame, p.root] : [p.p, p.root]).filter((e): e is FakeEl => e !== null)
    ask('hello')
    ask('select')
    const host = hostOf(p.root)!
    const box = host.shadow!.elements[0]!
    const seen: unknown[] = [(host.style.cssText ?? '').includes('pointer-events: none')]
    win.dispatch('pointermove', event('pointermove', { target: p.p, clientX: 1, clientY: 1 }))
    seen.push([box.style.display, box.style.pointerEvents])
    win.dispatch('pointerover', event('pointerover', { target: frame, clientX: 150, clientY: 10 }))
    seen.push([box.style.display, box.style.pointerEvents])
    // What is aimed at the box reaches the window as aimed at the host, and is hit-tested by its point.
    win.dispatch('pointermove', event('pointermove', { target: host, clientX: 160, clientY: 20 }))
    win.dispatch('click', event('click', { target: host, clientX: 160, clientY: 20 }))
    seen.push(answers().map((a) => (a.t === 'selected' ? a.element.tag : a.t)), hostOf(p.root) !== null)
    expect(seen).toEqual([true, ['block', 'none'], ['block', 'auto'], ['ready', 'iframe'], false])
  })
})

describe('the bridge in a page', () => {
  it('starts nothing in a window no parent frames, without origins, or a second time', () => {
    const doc = new FakeDoc(plainPage().root)
    const top = new FakeWin(doc)
    ;(top as unknown as { parent: unknown }).parent = top
    startBridge(top as unknown as Window & typeof globalThis, [WORKBENCH])
    const none = new FakeWin(doc)
    startBridge(none as unknown as Window & typeof globalThis, [])
    expect([top.count, none.count]).toEqual([0, 0])
    const twice = new FakeWin(doc)
    startBridge(twice as unknown as Window & typeof globalThis, [WORKBENCH])
    startBridge(twice as unknown as Window & typeof globalThis, [WORKBENCH])
    expect(twice.count).toBe(1)
    twice.deliver(WORKBENCH, m('hello'))
    expect(twice.posts).toHaveLength(1)
  })

  it('does nothing at all for what the gate drops, and observes nothing before a hello', () => {
    const { win, doc, answers, ask } = framed(plainPage().root)
    const dropped = () => {
      win.forge(WORKBENCH, m('hello'))
      win.deliver('http://127.0.0.1:5173', m('hello'))
      win.deliver(WORKBENCH, m('hello'), { postMessage() {} })
      win.deliver(WORKBENCH, m('ready', { version: 1, hints: [] }))
      win.deliver(WORKBENCH, m('select', {}, 'another-nonce-0000000'))
      win.deliver(WORKBENCH, { conductor: PREVIEW_MARK, t: 'look' })
    }
    dropped()
    ask('look')
    ask('select')
    expect(answers()).toEqual([])
    // Nothing observed, nothing listened for but the messages, nothing put on the page.
    expect([win.observers.length, doc.count, win.count, hostOf(doc.documentElement)]).toEqual([0, 0, 1, null])
    ask('hello')
    expect(answers().map((a) => a.t)).toEqual(['ready'])
    // The epoch's: one observer, the window's resize and scroll, the document's seven animation and transition events.
    expect([win.observers.length, win.count, doc.count]).toEqual([1, 3, 7])
    // After the hello, what is dropped changes nothing either: no answer, and the hello's nonce still the one answered.
    dropped()
    expect([answers(), win.count, hostOf(doc.documentElement)]).toEqual([[], 3, null])
    ask('look')
    expect(answers().map((a) => a.t)).toEqual(['looked'])
  })

  it("reports a plain page: no hint kind, no title, the root with no ancestor, and a selection; and writes them for Go's parser", () => {
    const { root, p } = plainPage()
    const { doc, win, answers, ask } = framed(root)
    ask('hello')
    ask('describe', { selector: 'html' })
    doc.hitAt = () => [p, root.children[1]!, root]
    ask('select')
    win.dispatch('pointermove', event('pointermove', { clientX: 10, clientY: 10 }))
    win.dispatch('click', event('click'))
    const got = answers()
    expect(got.map((a) => a.t)).toEqual(['ready', 'described', 'selected'])
    const [ready, described, selected] = got as [Extract<PreviewToWorkbench, { t: 'ready' }>, Extract<PreviewToWorkbench, { t: 'described' }>, Extract<PreviewToWorkbench, { t: 'selected' }>]
    expect([ready.hints, ready.title, ready.revision]).toEqual([[], '', ''])
    const pSelector = ':root > body:nth-child(2) > p:nth-child(1)'
    expect(described.element).toMatchObject({ tag: 'html', selector: ':root', ancestors: [], hints: [], neighbourhood: '', hit: { self: false, selector: '', tag: '' }, cut: [] })
    expect(described.element!.html).toBe('<html><head></head><body><p>Hello</p></body></html>')
    expect(selected.element).toMatchObject({ tag: 'p', text: 'Hello', selector: pSelector, hit: { self: true, selector: pSelector, tag: 'p' }, hints: [], cut: [] })
    expect(selected.element.ancestors.map((a) => a.tag)).toEqual(['body', 'html'])
    writeAnswers('plain-page-messages.json', 'the bridge’s answers on a plain page (no framework, no title, the root element described, a paragraph selected)', got)
  })

  it('puts its host on the page while it selects and takes it off, and acts only on what the browser delivers', () => {
    const { root, p } = plainPage()
    const { doc, win, answers, ask } = framed(root)
    doc.hitAt = () => [p, root]
    ask('hello')
    answers()
    ask('select')
    const host = hostOf(root)
    expect(host).not.toBeNull()
    expect(host!.tag).toBe('DIV')
    expect(win.count).toBe(3 + SELECTION_EVENTS.length) // the messages, the epoch's two, and selection mode's table
    // What the page's own script dispatches is neither swallowed nor acted on.
    for (const e of [event('pointermove', { clientX: 10, clientY: 10 }, false), event('click', {}, false), event('keydown', { key: 'Escape' }, false)]) {
      win.dispatch(e.type, e)
      expect(e.swallowed).toBe(false)
    }
    expect([answers(), hostOf(root)]).toEqual([[], host])
    // The host's own coming and going never moves the epoch; the page's mutations do, once a frame.
    const epoch = () => (ask('look'), (answers()[0] as Extract<PreviewToWorkbench, { t: 'looked' }>).epoch)
    const before = epoch()
    win.observers[0]!([{ type: 'childList', target: root, addedNodes: [host], removedNodes: [] }])
    expect(epoch()).toBe(before)
    win.observers[0]!([{ type: 'attributes', target: p, addedNodes: [], removedNodes: [] }])
    expect(epoch()).toBe(before + 1)
    // Escape leaves as it is released: cancelled, the host off the page, selection mode's listeners gone.
    const esc = event('keydown', { key: 'Escape' })
    win.dispatch('keydown', esc)
    expect([esc.swallowed, answers(), hostOf(root)]).toEqual([true, [], host])
    const released = event('keyup', { key: 'Escape' })
    win.dispatch('keyup', released)
    expect([released.swallowed, answers().map((a) => a.t), hostOf(root), win.count]).toEqual([true, ['cancelled'], null, 3])
    // Again, and a click selects: the host is off the page before the element is read.
    ask('select')
    win.dispatch('pointermove', event('pointermove', { clientX: 1, clientY: 1 }))
    const down = event('keydown', { key: 'ArrowDown' })
    win.dispatch('keydown', down)
    win.dispatch('keydown', event('keydown', { key: 'ArrowUp' }))
    win.dispatch('click', event('click'))
    const [sel] = answers() as [Extract<PreviewToWorkbench, { t: 'selected' }>]
    expect([down.swallowed, sel.t, sel.element.tag, hostOf(root), win.count]).toEqual([true, 'selected', 'p', null, 3])
    expect(sel.element.html).not.toContain('2147483647')
  })

  it('answers a describe that finds nothing, that the page refuses, or that names its own host as missing', () => {
    const { root } = plainPage()
    const { answers, ask } = framed(root)
    ask('hello')
    ask('select')
    for (const selector of ['#nothing', 'p:has(', 'conductor-preview-bridge']) ask('describe', { selector })
    ask('cancel')
    expect(answers().map((a) => [a.t, 'missing' in a && a.missing])).toEqual([['ready', false], ['described', true], ['described', true], ['described', true], ['cancelled', false]])
  })
})

/**
 * A page on which every walk of the bridge's reaches its bound, made k times larger in every direction a page controls: its
 * elements, attributes, classes, children, depth and text, an id of megabytes, an attribute naming ten thousand ids, the own keys
 * of every element, text and comment nodes between elements, and a deeper tree a walk climbs back up.
 */
function bigPage(k: number) {
  const n = (x: number) => x * k
  const many = (count: number, f: () => FakeEl) => Array.from({ length: count }, f)
  const values = new Map<number, string>()
  const value = (size: number) => values.get(size) ?? (values.set(size, 'v'.repeat(size)), values.get(size)!)
  const attrs = (count: number, size: number) => Object.fromEntries(Array.from({ length: count }, (_, i) => [`data-a${i}`, value(size)]))
  const words = (count: number) => 'word '.repeat(count)
  const gap = (count: number) => Array.from({ length: count }, (_, i) => (i % 2 ? comment('c') : ' '))
  // The head: 600 elements (past where the root's markup ends), a tree n(100) deep and n(1,500) text and comment nodes (which the
  // hint kinds' walk goes down, climbs back up and goes across), then n(2,100) elements and the revision past every walk.
  let deepTree = h('div')
  for (let d = 0; d < n(100); d++) deepTree = h('div', {}, deepTree)
  const head = h('head')
  const meta = () => h('meta', { name: 'x', content: 'y' })
  for (let i = 0; i < 600; i++) head.append(meta())
  head.append(deepTree)
  for (const node of gap(n(1500))) head.append(node)
  for (let i = 0; i < n(2100); i++) head.append(meta())
  head.append(h('meta', { name: 'conductor-revision', content: 'abcdef' }))
  const big = h('div', { id: 'big' }, ...many(n(1000), () => h('span', {}, words(10))))
  const ctl = h('input', { id: 'ctl', type: 'text', name: 'q', value: 'default-value', ...attrs(n(100), 8) })
  ctl.value = 'typed-text'
  const form = h('form', { id: 'f' }, ...many(n(4000), () => h('i', {}, 'x')), h('label', { for: 'ctl' }, 'Query'), ctl)
  const classes = Array.from({ length: n(2000) }, (_, i) => `c${i}`).join(' ')
  const leaf = h('span', { id: 'leaf', class: classes, 'aria-labelledby': 'big '.repeat(n(10000)), 'data-conductor-source': 'src/Leaf.vue:1:1', ...attrs(n(100), 200) }, words(n(6000)))
  const siblings = many(n(200), () => h('div', attrs(n(40), 600), h('p', {}, h('b', {}, words(n(50))))))
  let chain = h('div', { id: 'x'.repeat(n(1_000_000)), 'data-conductor-source': 'src/Inner.vue:1:1' }, leaf, ...siblings)
  for (let d = 0; d < n(100); d++) chain = h('div', { 'data-conductor-source': `src/D${d}.vue:${d + 1}:1` }, chain)
  const root = h('html', {}, head, h('body', {}, big, form, chain))
  for (const e of [root, ...root.descendants()]) e.expandos = n(100)
  const stack: FakeEl[] = []
  for (let e: FakeEl | null = leaf; e; e = e._parent) stack.push(e)
  return { root, stack }
}

/** A page of fields and code: a typed input, a textarea, a select, a template holding controls, scripts, styles, an editable region. */
function fieldsPage() {
  const typed = h('input', { id: 'q', name: 'q', type: 'text', value: 'secret-default-value' })
  typed.value = 'secret-typed-text'
  const area = h('textarea', { id: 'note', name: 'note' }, 'secret-textarea-text')
  area.value = 'secret-textarea-typed'
  const option = h('option', { id: 'opt', value: 'secret-option-value', selected: '' }, 'secret-option-text')
  const select = h('select', { id: 'pick', name: 'pick' }, option)
  const tpl = h('template', { id: 'tpl' }, h('input', { value: 'secret-template-child' }))
  tpl.content = h('#document-fragment', {}, h('input', { value: 'secret-template-content' }), h('textarea', {}, 'secret-template-textarea'))
  const script = h('script', { id: 'js' }, 'var s = "secret-script-text"')
  const style = h('style', { id: 'css' }, '.x::after { content: "secret-style-text" }')
  const editable = h('div', { id: 'ed', contenteditable: 'true', role: 'textbox' }, h('p', { id: 'edp' }, 'secret-edited-text'))
  const form = h('form', { id: 'f' }, h('label', { for: 'q' }, 'Search'), typed, area, select, tpl, script, style, editable, h('button', { id: 'go' }, 'Go'))
  const root = h('html', {}, h('head', {}, h('style', {}, 'body { --x: "secret-head-style" }'), h('script', {}, 'secret-head-script')), h('body', {}, form))
  return { root, ids: ['f', 'q', 'note', 'pick', 'opt', 'tpl', 'js', 'css', 'ed', 'edp', 'go'], typed, area }
}

describe('what the bridge reads of a page', () => {
  /** Every answer the bridge gives on bigPage(k), and what it read of the page for each: [nodes, list items, string units, elements whose keys were listed]. */
  function everyAnswer(k: number) {
    const p = bigPage(k)
    const { doc, win, answers, ask } = framed(p.root)
    doc.hitAt = () => p.stack
    const counts: Record<string, number[]> = {}
    const took = (name: string, act: () => void) => {
      resetReads()
      keysCounted(act)
      counts[name] = [reads.nodes, reads.items, reads.units, reads.keys]
    }
    took('ready', () => ask('hello'))
    took('selected', () => {
      ask('select')
      win.dispatch('pointermove', event('pointermove', { clientX: 1, clientY: 1 }))
      win.dispatch('click', event('click'))
    })
    for (const [name, selector] of [['the root', ':root'], ['a leaf', '#leaf'], ['a control', '#ctl']] as const) took(`described ${name}`, () => ask('describe', { selector }))
    took('looked', () => ask('look'))
    return { counts, got: answers() }
  }

  it('reads as much of a page ten times larger as of the page: every walk, list and string stops at its bound', () => {
    pageStrings(true)
    try {
      const small = everyAnswer(1)
      const large = everyAnswer(10)
      console.log('reads [nodes, items, units, elements whose keys were listed] of the page:', JSON.stringify(small.counts), 'and of the page ten times larger:', JSON.stringify(large.counts))
      for (const { got } of [small, large]) expect(got.map((a) => a.t)).toEqual(['ready', 'selected', 'described', 'described', 'described', 'looked'])
      expect(large.counts).toEqual(small.counts)
      writeAnswers('large-page-messages.json', 'the bridge’s answers on a page ten times larger than its bounds in every direction', large.got)
    } finally {
      pageStrings(false)
    }
  }, 60_000)

  it('follows no more links than each walk’s budget in a hello, a pick and a describe, whatever the page’s links answer', () => {
    const { root } = plainPage()
    const body = root.children[1]!
    const b = h('b', {}, 'x')
    body.append(b)
    // b's links as the page's own getters answer them: no child, no sibling, and each parent a new one with no sibling. The test
    // bounds its own run: past a million parents the getter throws.
    let climbs = 0
    const parent = (): object => {
      if (++climbs > 1_000_000) throw new Error('a million parents climbed')
      return Object.defineProperties({}, { nextSibling: { value: null }, parentNode: { get: parent } })
    }
    Object.defineProperties(b, { firstChild: { get: () => null }, nextSibling: { get: () => null }, parentNode: { get: parent } })
    const { doc, win, answers, ask } = framed(root)
    doc.hitAt = () => [b, body, root]
    const counts: number[] = []
    const took = (act: () => void) => {
      climbs = 0
      act()
      counts.push(climbs)
    }
    took(() => ask('hello'))
    took(() => {
      ask('select')
      win.dispatch('click', event('click', { clientX: 1, clientY: 1 }))
    })
    took(() => ask('describe', { selector: ':root' }))
    console.log('parents climbed by a hello, a pick and a describe:', JSON.stringify(counts))
    // Each within the walks that reach b: the hint kinds' (a hello), the neighbourhood's (a pick of b), the root's markup, text and motion.
    const B = ELEMENT_BOUNDS
    expect([answers().map((a) => a.t), counts[0]! <= HINT_NODES, counts[1]! <= B.neighbourhood / 4, counts[2]! <= B.html / 4 + 256 + 64]).toEqual([
      ['ready', 'selected', 'described'],
      true,
      true,
      true,
    ])
  })

  it('puts no field’s value, no typed text and nothing of a script, a style or a template in any answer', () => {
    const p = fieldsPage()
    const { doc, win, answers, ask } = framed(p.root)
    ask('hello')
    for (const id of p.ids) ask('describe', { selector: `#${id}` })
    ask('describe', { selector: ':root' })
    for (const id of p.ids) {
      doc.hitAt = () => [doc.getElementById(id)!]
      ask('select')
      win.dispatch('pointermove', event('pointermove', { clientX: 1, clientY: 1 }))
      win.dispatch('click', event('click'))
    }
    const got = answers()
    expect(got.map((a) => a.t)).toEqual(['ready', ...p.ids.map(() => 'described'), 'described', ...p.ids.map(() => 'selected')])
    expect(JSON.stringify(got)).not.toContain('secret')
    // The page keeps them all: nothing was written to it.
    expect([p.typed.attrs.get('value'), p.typed.value, plainText(p.area)]).toEqual(['secret-default-value', 'secret-typed-text', 'secret-textarea-text'])
    writeAnswers('fields-page-messages.json', 'the bridge’s answers on a page of fields, a template, scripts and styles, every element described and selected', got)
  })
})

/** The answers written for internal/proto's parser (internal/api/testdata), one message a line, as the browser's JSON writer writes it. */
function writeAnswers(name: string, what: string, messages: PreviewToWorkbench[]) {
  const comment = `Written by web/app/utils/previewBridge/bridge.test.ts: ${what}, each with the hello's nonce, for internal/proto's parser.`
  fixture(name, `{"comment": ${JSON.stringify(comment)}, "nonce": ${JSON.stringify(NONCE)}, "messages": [\n${messages.map((m) => JSON.stringify(m)).join(',\n')}\n]}\n`)
}
