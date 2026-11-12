import { describe, expect, it } from 'vitest'
import { ELEMENT_BOUNDS as B, ELEMENT_CUTS, MAX_ELEMENT_REPORT, measuredBytes, previewMeasure, validElement, type ElementReport } from '../protocol'
import { FakeDoc, h, plainText } from './fakeDom'
import { fixture } from './fixture'
import { REPORT_STYLES, boundFields, cutEnd, cutReport, cutStart, markup, readElement, tagWithin } from './report'
import type { Doc, El } from './select'

/** s of exactly n measured bytes, made of c (a character of a measure that divides n, or 'a'). */
const of = (n: number, c = 'a') => c.repeat(Math.floor(n / measuredBytes(c)))

/** A report with every field at its own bound, its strings of c. */
function fullest(c: string): ElementReport {
  return {
    tag: of(B.name, c),
    id: of(B.name, c),
    classes: of(B.name, c),
    role: of(B.name, c),
    name: of(B.name, c),
    text: of(B.text, c),
    selector: of(B.selector, c),
    box: { x: 1.25, y: -3, width: 100, height: 20 },
    attributes: Array.from({ length: B.attributes }, () => [of(B.attributeName, c), of(B.attributeValue, c)] as [string, string]),
    state: { disabled: true, hidden: false, ariaDisabled: of(B.name, c), ariaExpanded: of(B.name, c), pointerEvents: of(B.name, c), inert: false, hovered: true, motion: 2 },
    hit: { self: false, selector: of(B.selector, c), tag: of(B.name, c) },
    html: '<' + of(B.html - 2, c) + '>',
    neighbourhood: '<i>' + of(B.neighbourhood - 7, c) + '</i>',
    ancestors: Array.from({ length: B.ancestors }, () => ({ tag: of(128, c), id: of(128, c), classes: of(128, c), role: of(80, c) })),
    styles: Array.from({ length: B.styles }, (_, i) => [REPORT_STYLES[i]!, of(B.styleValue, c)] as [string, string]),
    hints: Array.from({ length: B.hints }, () => ({ how: 'vue' as const, file: of(B.hintFile, c), line: 9007199254740991, column: 1, component: of(B.hintComponent, c) })),
    cut: [],
  }
}

/** The plan's largest-tag shape: a 12 KiB opening tag, eight 1,024-byte hint files, twelve 512-byte ancestors, 32 attributes at their bounds. */
function largestTag(c: string): ElementReport {
  return { ...fullest(c), neighbourhood: '', text: '', name: '' }
}

/** The fixture the server's tests read (internal/api/testdata): the report as cut and its measure. */
function writeFixture(name: string, r: ElementReport) {
  const comment = `Written by web/app/utils/previewBridge/report.test.ts: the bridge's report cut to ${MAX_ELEMENT_REPORT} bytes by its measure, which bytes is.`
  fixture(name, JSON.stringify({ comment, bytes: previewMeasure(r), report: r }, null, 1) + '\n')
}

describe('the cut of one field', () => {
  it('cuts by measured bytes and never splits a character', () => {
    expect(cutStart('a b', 6)).toBe('a') // U+2028 is six bytes as Go writes it
    expect(cutStart('a b', 7)).toBe('a ')
    expect(cutStart('😀😀', 5)).toBe('😀')
    expect(cutStart('é'.repeat(10), 5)).toBe('éé')
    expect(cutEnd('dir/very/long/path/File.vue', 8)).toBe('File.vue')
    expect(measuredBytes(cutStart('\x01'.repeat(100), 64))).toBe(60)
    // Go's measure: < and & as they are, U+2028 in six bytes, a lone surrogate as U+FFFD, which the cut writes in its place.
    expect([cutStart('<& ', 8), cutStart('<& ', 7)]).toEqual(['<& ', '<&'])
    expect([cutStart('a\uD800b', 9), cutEnd('a\uDC00b', 9), cutStart('a\uD800b', 3)]).toEqual(['a�b', 'a�b', 'a'])
    // A pair the cut's first look splits is past the cut, never half of it.
    expect(cutStart('a'.repeat(8) + '😀', 8)).toBe('a'.repeat(8))
    expect(cutEnd('😀' + 'a'.repeat(8), 8)).toBe('a'.repeat(8))
  })

  it('never cuts markup or a selector: one past its bound is none, an opening tag loses whole attributes', () => {
    const over = '<ul>' + '<li class="row">item</li>'.repeat(2000) + '</ul>'
    const r = boundFields({ ...fullest('a'), html: over, neighbourhood: over, selector: '#' + of(B.selector), hit: { self: true, selector: '#' + of(B.selector), tag: 'a' } })
    expect([r.html, r.neighbourhood, r.selector, r.hit.selector]).toEqual(['', '', '', ''])
    const open = '<div id="a b" class="x &quot;y&quot;" title="t">text</div>'
    // Measured as a message measures them: each quote in two bytes.
    expect([tagWithin(open, 100), tagWithin(open, 42), tagWithin(open, 41), tagWithin(open, 5), tagWithin(open, 4)]).toEqual([
      '<div id="a b" class="x &quot;y&quot;" title="t">',
      '<div id="a b" class="x &quot;y&quot;">',
      '<div id="a b">',
      '<div>',
      '',
    ])
  })
})

describe('the markup the bridge writes', () => {
  it('writes tags and attributes itself, escaped, closes every element it opens and stops whole at its bound', () => {
    const p = h('p', { title: 'a "quoted" <b>' }, 'one < two & ', h('b', {}, 'three'), h('br'), 'four')
    expect(markup(p as unknown as El, 1000)).toBe('<p title="a &quot;quoted&quot; &lt;b&gt;">one &lt; two &amp; <b>three</b><br>four</p>')
    const list = h('ul', {}, ...Array.from({ length: 2000 }, (_, i) => h('li', { class: 'row' }, `item ${i}`)))
    for (const bound of [B.html, B.neighbourhood]) {
      const html = markup(list as unknown as El, bound)
      expect(measuredBytes(html)).toBeLessThanOrEqual(bound)
      expect(measuredBytes(html)).toBeGreaterThan(bound - 40)
      // A text node written up to what is left, then every open element closed: never half a tag.
      expect(html).toMatch(/^<ul><li class="row">item 0<\/li>.*<\/li><\/ul>$|^<ul>.*<li class="row">item \d*<\/li><\/ul>$/)
      expect(html.match(/<li/g)!.length).toBe(html.match(/<\/li>/g)!.length)
    }
  })

  it('writes the neighbourhood to the siblings’ grandchildren, the element itself whole', () => {
    const el = h('span', {}, h('i', {}, h('b', {}, h('u', {}, 'deep'))))
    const sib = h('div', {}, h('i', {}, h('b', {}, h('u', {}, 'deep'))))
    const parent = h('section', {}, el, sib)
    expect(markup(parent as unknown as El, B.neighbourhood, el as unknown as El)).toBe('<section><span><i><b><u>deep</u></b></i></span><div><i><b>…</b></i></div></section>')
  })
})

describe('the cut of a report', () => {
  it('takes every field to its bound, then the steps in order to the report bound, and writes the fullest fixture', () => {
    const raw = fullest('a')
    expect(previewMeasure(raw)).toBeGreaterThan(70 * 1024) // the bounds add up to about 78 KiB
    const r = cutReport(raw)
    expect(previewMeasure(r)).toBeLessThanOrEqual(MAX_ELEMENT_REPORT)
    expect(validElement(r)).toBe(true)
    // The steps taken are the first ones, in order, each named once.
    expect(r.cut).toEqual(ELEMENT_CUTS.slice(0, r.cut.length))
    expect(r.cut[0]).toBe('neighbourhood')
    expect(r.neighbourhood).toBe('')
    writeFixture('fullest-report.json', r)
  })

  it('cuts the largest-tag shape of six-byte characters past the first four steps and writes its fixture', () => {
    const raw = largestTag('\x01')
    // Steps 1 to 4 are not enough for it: the opening tag is cut, then the hints.
    const r = cutReport(raw)
    expect(r.cut).toEqual(ELEMENT_CUTS.slice(0, 6))
    expect(previewMeasure(r)).toBeLessThanOrEqual(MAX_ELEMENT_REPORT)
    expect(r.html).toBe('')
    expect(r.hints).toHaveLength(2)
    expect(validElement(r)).toBe(true)
    writeFixture('largest-tag-report.json', r)
  })

  it('goes through the tenth step below the bound, and what it leaves at worst is under 12 KiB', () => {
    for (const c of ['a', '\x01', ' ', 'é']) {
      const r = cutReport(fullest(c), 0)
      expect(r.cut).toEqual([...ELEMENT_CUTS])
      expect(previewMeasure(r)).toBeLessThan(12 * 1024)
      expect([r.styles.length, r.attributes.length, r.ancestors.length, r.hints.length, r.html, r.neighbourhood]).toEqual([8, 8, 4, 2, '', ''])
      for (const k of ['tag', 'text', 'name', 'id', 'role', 'classes', 'selector'] as const) expect(measuredBytes(r[k])).toBeLessThanOrEqual(256)
      expect(validElement(r)).toBe(true)
    }
  })

  it('stops at the first step that fits', () => {
    const lean = { ...fullest('a'), attributes: [], styles: [], hints: [], ancestors: [] }
    const small = cutReport({ ...lean, html: '<b>' + 'x'.repeat(12000) + '</b>', neighbourhood: '<i>aaaa</i>'.repeat(2234) })
    expect(small.cut).toEqual(['neighbourhood'])
    expect(cutReport({ ...fullest('a'), neighbourhood: '', html: '', attributes: [], styles: [], hints: [], ancestors: [] }).cut).toEqual([])
  })
})

describe('the reading of an element', () => {
  function form() {
    const pw = h('input', { type: 'password', name: 'pw', value: 'hunter2' })
    const typed = h('input', { name: 'q', value: 'typed-words' })
    const area = h('textarea', { name: 'note' }, 'draft-text')
    const label = h('label', {}, 'Search ', typed)
    const save = h('button', { id: 'save', 'aria-label': 'Save the form' }, 'Save')
    const f = h('form', { id: 'f' }, pw, label, area, save)
    const root = h('html', {}, h('body', {}, f))
    const d = new FakeDoc(root)
    d.hitAt = () => [save]
    return { f, pw, typed, area, label, save, d }
  }
  const win = (styles: Record<string, string> = {}) => ({ getComputedStyle: () => ({ getPropertyValue: (p: string) => styles[p] ?? '' }) })

  it('never reads an input value, a password or a textarea text', () => {
    const p = form()
    for (const el of [p.f, p.pw, p.typed, p.area, p.label]) {
      const r = readElement(el as unknown as El, { doc: p.d as unknown as Doc, win: win() })
      const all = JSON.stringify(r)
      for (const secret of ['hunter2', 'typed-words', 'draft-text']) expect(all, `${el.tag} ${secret}`).not.toContain(secret)
    }
    // Of a control, what it is: its type, its name, its id, its classes, its role and its aria- attributes.
    const typed = readElement(p.typed as unknown as El, { doc: p.d as unknown as Doc, win: win() })
    expect([typed.attributes, typed.html, typed.text]).toEqual([[['name', 'q']], '<input name="q">', ''])
    // The page keeps them: nothing is written to it.
    expect(p.pw.getAttribute('value')).toBe('hunter2')
    expect(plainText(p.area)).toBe('draft-text')
  })

  it('reads the name, the state, the hit test and the fixed list of styles', () => {
    const p = form()
    p.save.attrs.set('disabled', '')
    const r = readElement(p.save as unknown as El, { doc: p.d as unknown as Doc, win: win({ display: 'block', 'pointer-events': 'none' }) })
    expect(r.name).toBe('Save the form')
    expect(r.selector).toBe('#save')
    expect(r.state).toMatchObject({ disabled: true, pointerEvents: 'none' })
    expect(r.hit).toEqual({ self: true, selector: '#save', tag: 'button' })
    expect(r.styles.map(([k]) => k)).toEqual([...REPORT_STYLES])
    expect(r.styles[0]).toEqual(['display', 'block'])
    expect(new Set(REPORT_STYLES).size).toBe(64)
    expect(REPORT_STYLES.slice(0, 8)).toEqual(['display', 'visibility', 'opacity', 'pointer-events', 'position', 'z-index', 'inset', 'transform'])
    expect(validElement(r)).toBe(true)
  })

  it('names nothing by an id, a for or an aria-labelledby token past its bound', () => {
    const long = 'i'.repeat(B.name + 1)
    const first = h('input', { id: `${long}1` })
    const second = h('input', { id: `${long}2` })
    const other = h('span', { id: long }, 'Other')
    const end = h('span', { id: 'end' }, 'End')
    const pad = ' '.repeat(B.name - 2)
    const byToken = h('button', { 'aria-labelledby': `${long}x` }, 'Own')
    const byList = h('button', { 'aria-labelledby': `${pad}endless` }, 'Kept')
    const f = h('form', {}, h('label', { for: `${long}1` }, 'First'), first, h('label', { for: `${long}2` }, 'Second'), second, other, end, byToken, byList)
    const d = new FakeDoc(h('html', {}, h('body', {}, f)))
    const name = (e: El) => readElement(e, { doc: d as unknown as Doc, win: win() }).name
    expect([second, byToken, byList].map((e) => name(e as unknown as El))).toEqual(['', 'Own', 'Kept'])
  })

  it('makes every string it reads from the page well-formed before it measures or cuts it, and bounds the lists as it reads them', () => {
    const lone = '\uD800'
    const attrs: Record<string, string> = { class: `a${lone} b`, role: `r${lone}`, 'aria-disabled': lone, 'aria-expanded': `\uDC00x`, title: `t${lone}` }
    for (let i = 0; i < 100; i++) attrs[`data-a${i}`] = 'v'
    const el = h('button', attrs, `Save${lone}`)
    const parent = h('div', { class: `bar${lone}`, role: lone }, el)
    const d = new FakeDoc(h('html', {}, h('body', {}, parent)))
    d.hitAt = () => [el]
    const r = readElement(el as unknown as El, { doc: d as unknown as Doc, win: win({ 'pointer-events': `auto${lone}` }) })
    expect(validElement(r)).toBe(true)
    expect(previewMeasure(r)).toBeLessThanOrEqual(MAX_ELEMENT_REPORT)
    expect([r.classes, r.role, r.text, r.state.ariaDisabled, r.state.ariaExpanded, r.state.pointerEvents]).toEqual(['a� b', 'r�', 'Save�', '�', '�x', 'auto�'])
    expect(r.ancestors[0]).toEqual({ tag: 'div', id: '', classes: 'bar�', role: '�' })
    expect(r.attributes).toHaveLength(B.attributes)
    expect(JSON.stringify(r)).not.toMatch(/[\uD800-\uDFFF]/)
  })
})
