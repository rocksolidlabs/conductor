import { mkdirSync, writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { PREVIEW_MARK, parsePreviewMessage, type PreviewToWorkbench } from '../protocol'
import { startBridge } from './bridge'
import { FakeDoc, FakeWin, event, h, type FakeEl } from './fakeDom'

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

const hostOf = (root: FakeEl) => root.children.find((c) => c.tagName === 'CONDUCTOR-PREVIEW-BRIDGE') ?? null

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
    expect(described.element).toMatchObject({ tag: 'html', selector: 'html', ancestors: [], hints: [], neighbourhood: '', hit: { self: false, selector: '', tag: '' }, cut: [] })
    expect(selected.element).toMatchObject({ tag: 'p', text: 'Hello', selector: 'p', hit: { self: true, selector: 'p', tag: 'p' }, hints: [], cut: [] })
    expect(selected.element.ancestors.map((a) => a.tag)).toEqual(['body', 'html'])
    const dir = fileURLToPath(new URL('../../../../internal/api/testdata/', import.meta.url))
    mkdirSync(dir, { recursive: true })
    const comment = "Written by web/app/utils/previewBridge/bridge.test.ts: the bridge's answers on a plain page (no framework, no title, the root element described, a paragraph selected), each with the hello's nonce, for internal/proto's parser."
    writeFileSync(dir + 'plain-page-messages.json', JSON.stringify({ comment, nonce: NONCE, messages: got }, null, 1) + '\n')
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
    expect(win.count).toBe(10) // the messages, the epoch's two, and selection mode's seven
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
    // Escape leaves: cancelled, the host off the page, selection mode's listeners gone.
    const esc = event('keydown', { key: 'Escape' })
    win.dispatch('keydown', esc)
    expect([esc.swallowed, answers().map((a) => a.t), hostOf(root), win.count]).toEqual([true, ['cancelled'], null, 3])
    // Again, and a click selects: the host is off the page before the element is read.
    ask('select')
    win.dispatch('pointermove', event('pointermove', { clientX: 1, clientY: 1 }))
    const down = event('keydown', { key: 'ArrowDown' })
    win.dispatch('keydown', down)
    win.dispatch('keydown', event('keydown', { key: 'ArrowUp' }))
    win.dispatch('click', event('click'))
    const [sel] = answers() as [Extract<PreviewToWorkbench, { t: 'selected' }>]
    expect([down.swallowed, sel.t, sel.element.tag, hostOf(root), win.count]).toEqual([true, 'selected', 'p', null, 3])
    expect(sel.element.html).not.toContain('conductor-preview-bridge')
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
