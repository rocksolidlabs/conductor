import { describe, expect, it } from 'vitest'
import { FakeDoc, h, type FakeEl } from './fakeDom'
import { readElement } from './report'
import { pageEpoch, selectorFor, stackAt, stepStack, walk, type Doc, type El } from './select'

const el = (e: FakeEl) => e as unknown as El
const doc = (d: FakeDoc) => d as unknown as Doc

/** A page: a bar of two buttons, a list, a pointer-events: none button, an inert section, and an overlay over all of it. */
function page() {
  const save = h('button', { class: 'btn save' }, 'Save')
  const cancel = h('button', { class: 'btn' }, 'Cancel')
  const bar = h('div', { class: 'bar' }, save, cancel)
  const ghost = h('button', { id: 'ghost', class: 'ghost' }, 'Ghost')
  ghost.style['pointer-events'] = 'none'
  const sleeper = h('a', { href: '/x' }, 'Asleep')
  const inert = h('section', { inert: '' }, h('p', {}, sleeper))
  const overlay = h('div', { class: 'overlay' })
  const twins = [h('span', { id: 'twin' }, 'one'), h('span', { id: 'twin' }, 'two')]
  const main = h('main', { id: 'app' }, bar, ghost, inert, ...twins)
  const body = h('body', {}, main, overlay)
  const root = h('html', {}, body)
  const d = new FakeDoc(root)
  // What a hit test at any point finds: the overlay above the bar's Save, the ghost and the inert link left out (CSSOM's
  // elementsFromPoint lists hit-test targets only).
  d.hitAt = () => [overlay, save, bar, main, body, root]
  save.rect = { x: 10, y: 10, width: 80, height: 30 }
  return { d, root, body, main, bar, save, cancel, ghost, inert, sleeper, overlay, twins }
}

describe('the selector', () => {
  it('is found exactly once, an id only when no other element has it', () => {
    const p = page()
    for (const e of [p.save, p.cancel, p.ghost, p.sleeper, p.overlay, p.twins[0]!, p.twins[1]!]) {
      const s = selectorFor(el(e), doc(p.d))
      expect(p.d.querySelectorAll(s)).toEqual([e])
    }
    expect(selectorFor(el(p.ghost), doc(p.d))).toBe('#ghost')
    // Two spans share an id: the id is no selector, their place among their parent's spans is.
    expect(selectorFor(el(p.twins[1]!), doc(p.d))).toBe('span:nth-of-type(2)')
    expect(selectorFor(el(p.save), doc(p.d))).toBe('button.btn.save:nth-of-type(1)')
    // A step found more than once climbs to its parent's, up to an ancestor's unique id.
    p.body.append(h('div', { class: 'bar' }, h('button', { class: 'btn' }, 'A'), h('button', { class: 'btn' }, 'B')))
    expect(selectorFor(el(p.cancel), doc(p.d))).toBe('#app > div.bar > button.btn:nth-of-type(2)')
  })

  it('leaves classes a build generated out, and gives none for the bridge host', () => {
    const p = page()
    const gen = h('i', { class: 'css-1a2b3c4d icon' })
    p.bar.append(gen)
    expect(selectorFor(el(gen), doc(p.d))).toBe('i.icon')
    expect(selectorFor(el(p.overlay), doc(p.d), el(p.overlay))).toBe('')
  })
})

describe('the stack and the walk', () => {
  it('steps Down from an overlay to the button under it, while the hit test still names the overlay', () => {
    const p = page()
    const stack = stackAt(doc(p.d), 20, 20, null)
    expect(stack[0]).toBe(p.overlay)
    const down = stepStack(stack, stack[0]!, true)
    expect(down).toBe(p.save)
    expect(stepStack(stack, down, false)).toBe(p.overlay)
    const r = readElement(down as El, { doc: doc(p.d), win: { getComputedStyle: () => ({ getPropertyValue: () => '' }) }, host: null })
    expect(r.hit).toEqual({ self: false, selector: 'div.overlay', tag: 'div' })
  })

  it('leaves the bridge host out of the stack', () => {
    const p = page()
    const host = h('conductor-preview-bridge')
    p.root.append(host)
    p.d.hitAt = () => [host, p.overlay, p.save]
    expect(stackAt(doc(p.d), 0, 0, el(host))).toEqual([p.overlay, p.save])
  })

  it('reaches a pointer-events: none button and an inert subtree by the DOM, neither in the stack', () => {
    const p = page()
    const stack = stackAt(doc(p.d), 20, 20, null)
    expect(stack).not.toContain(p.ghost)
    expect(stack).not.toContain(p.sleeper)
    // Left to the bar's parent, Shift+Down to its next sibling: the ghost button.
    const main = walk(el(p.bar), 'parent', null)
    expect(main).toBe(p.main)
    const ghost = walk(el(p.bar), 'next', null)
    expect(ghost).toBe(p.ghost)
    // Shift+Down again, Right twice: into the inert section's link.
    const inert = walk(ghost, 'next', null)
    expect(inert).toBe(p.inert)
    expect(walk(walk(inert, 'child', null), 'child', null)).toBe(p.sleeper)
    // Shift+Up back, and an end stays where it is.
    expect(walk(inert, 'previous', null)).toBe(p.ghost)
    expect(walk(el(p.save), 'previous', null)).toBe(p.save)
    // The report of the inert link says so; a click there reaches the overlay.
    const r = readElement(el(p.sleeper), { doc: doc(p.d), win: { getComputedStyle: () => ({ getPropertyValue: () => '' }) }, host: null })
    expect(r.state.inert).toBe(true)
    expect(r.hit.tag).toBe('div')
  })

  it('walks past the bridge host', () => {
    const p = page()
    const host = h('conductor-preview-bridge')
    p.body.append(host)
    expect(walk(el(p.overlay), 'next', el(host))).toBe(p.overlay)
  })
})

describe('the page epoch', () => {
  function fakePage() {
    const listeners = new Map<string, () => void>()
    const frames: Array<() => void> = []
    let mutate: (records: MutationRecord[]) => void = () => {}
    const host = h('conductor-preview-bridge')
    const target = {
      addEventListener: (t: string, fn: () => void) => listeners.set(t, fn),
      removeEventListener: (t: string) => listeners.delete(t),
    }
    const epoch = pageEpoch({
      win: target as unknown as Window,
      doc: { ...target, documentElement: h('html') } as unknown as Document,
      host: () => el(host),
      observer: (cb) => {
        mutate = cb
        return { observe: () => {}, disconnect: () => {} }
      },
      frame: (cb) => frames.push(cb),
    })
    const nextFrame = () => frames.splice(0).forEach((f) => f())
    const record = (target: unknown, type = 'attributes', added: unknown[] = []) =>
      ({ target, type, addedNodes: added, removedNodes: [] }) as unknown as MutationRecord
    return { epoch, fire: (t: string) => listeners.get(t)!(), mutate: (r: MutationRecord[]) => mutate(r), nextFrame, record, host, listeners }
  }

  it('is bumped once a frame by a mutation, a resize, a scroll, an animation and a transition', () => {
    const p = fakePage()
    expect(p.epoch.value()).toBe(0)
    for (const what of ['mutation', 'resize', 'scroll', 'animationstart', 'transitionend']) {
      const before = p.epoch.value()
      if (what === 'mutation') p.mutate([p.record(h('div'))])
      else p.fire(what)
      expect(p.epoch.value(), what).toBe(before + 1)
      // A second change in the same frame bumps nothing more.
      p.fire('scroll')
      expect(p.epoch.value(), what).toBe(before + 1)
      p.nextFrame()
    }
    expect(p.epoch.value()).toBe(5)
  })

  it('is never bumped by its own shadow host', () => {
    const p = fakePage()
    p.mutate([p.record(p.host), p.record(h('body'), 'childList', [p.host])])
    expect(p.epoch.value()).toBe(0)
    p.epoch.stop()
    expect(p.listeners.size).toBe(0)
  })
})
