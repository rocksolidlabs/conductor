import type { ElementReport, PreviewPage } from '../protocol'

/** A bridge's element report for the tests, every field the schema asks for, `over` replacing any. */
export function testElement(over: Partial<ElementReport> = {}): ElementReport {
  return {
    tag: 'button',
    id: 'save',
    classes: 'save primary',
    role: 'button',
    name: 'Save',
    text: 'Save',
    selector: '#save',
    box: { x: 10, y: 20, width: 80, height: 30 },
    attributes: [['type', 'button']],
    state: { disabled: false, hidden: false, ariaDisabled: '', ariaExpanded: '', pointerEvents: 'auto', inert: false, hovered: false, motion: 0 },
    hit: { self: true, selector: '#save', tag: 'button' },
    html: '<button id="save">Save</button>',
    neighbourhood: '<div><button id="save">Save</button></div>',
    ancestors: [{ tag: 'div', id: '', classes: '', role: '' }],
    styles: [['display', 'block']],
    hints: [],
    cut: [],
    ...over,
  }
}

/** What every bridge answer carries of the page, `over` replacing any. */
export function testPage(over: Partial<PreviewPage> = {}): PreviewPage {
  return {
    url: 'http://127.0.0.1:41000/',
    title: 'The page',
    viewport: { width: 400, height: 300, dpr: 1, scrollX: 0, scrollY: 0, colorScheme: 'light', userAgent: 'test' },
    epoch: 1,
    revision: '',
    ...over,
  }
}
