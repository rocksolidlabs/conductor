import { describe, expect, it } from 'vitest'
import { FakeDoc, h } from './fakeDom'
import { hintKindsOf, hintsOf, parseSource } from './hints'
import type { Doc, El } from './select'

describe('the source hints', () => {
  it('reads data-conductor-source on the element and its ancestors, innermost first', () => {
    const btn = h('button', { 'data-conductor-source': 'app/components/SaveBar.vue:12:5' })
    h('div', { 'data-conductor-source': 'C:/repo/app/pages/index.vue:3' }, btn)
    expect(hintsOf(btn as unknown as El)).toEqual([
      { how: 'attribute', file: 'app/components/SaveBar.vue', line: 12, column: 5, component: '' },
      { how: 'attribute', file: 'C:/repo/app/pages/index.vue', line: 3, column: 0, component: '' },
    ])
    expect(parseSource('just/a/file.ts')).toEqual({ file: 'just/a/file.ts', line: 0, column: 0 })
  })

  it("reads a Vue dev build's component chain", () => {
    const btn = h('button')
    const app = { type: { __file: '/repo/web/app/app.vue', __name: 'App' }, parent: null }
    const lib = { type: { __file: '/repo/web/node_modules/@nuxt/ui/dist/runtime/components/Button.vue', name: 'UButton' }, parent: { type: { __file: '/repo/web/app/components/SaveBar.vue', __name: 'SaveBar' }, parent: app } }
    btn.__vueParentComponent = lib
    expect(hintsOf(btn as unknown as El).map((x) => [x.how, x.file, x.component])).toEqual([
      ['vue', '/repo/web/node_modules/@nuxt/ui/dist/runtime/components/Button.vue', 'UButton'],
      ['vue', '/repo/web/app/components/SaveBar.vue', 'SaveBar'],
      ['vue', '/repo/web/app/app.vue', 'App'],
    ])
  })

  it("reads Svelte's __svelte_meta and React 18's _debugSource through the fiber", () => {
    const s = h('button')
    s.__svelte_meta = { loc: { file: 'src/routes/+page.svelte', line: 7, column: 2 } }
    expect(hintsOf(s as unknown as El)).toEqual([{ how: 'svelte', file: 'src/routes/+page.svelte', line: 7, column: 2, component: '' }])
    function SaveBar() {}
    const r = h('button')
    r['__reactFiber$abc123'] = { _debugSource: { fileName: 'src/Button.jsx', lineNumber: 4, columnNumber: 9 }, type: 'button', return: { _debugSource: { fileName: 'src/SaveBar.jsx', lineNumber: 20 }, type: SaveBar, return: null } }
    expect(hintsOf(r as unknown as El)).toEqual([
      { how: 'react', file: 'src/Button.jsx', line: 4, column: 9, component: '' },
      { how: 'react', file: 'src/SaveBar.jsx', line: 20, column: 0, component: 'SaveBar' },
    ])
  })

  it('gives none for a page without them, keeps only strings and whole numbers, and bounds the count', () => {
    const plain = h('button')
    expect(hintsOf(plain as unknown as El)).toEqual([])
    const odd = h('button')
    odd.__svelte_meta = { loc: { file: { toString: () => 'x' }, line: -3 } }
    odd.__vueParentComponent = { type: { __file: 7 } }
    expect(hintsOf(odd as unknown as El)).toEqual([])
    let e = h('i', { 'data-conductor-source': 'a.vue:1' })
    for (let i = 0; i < 12; i++) e = h('div', { 'data-conductor-source': `d${i}.vue:1` }, e)
    let inner = e
    while (inner.firstElementChild) inner = inner.firstElementChild
    expect(hintsOf(inner as unknown as El)).toHaveLength(8)
  })

  it("names the kinds a page gives in a ready's hints", () => {
    const v = h('span')
    v.__vueParentComponent = { type: { __file: 'a.vue' } }
    const root = h('html', {}, h('body', {}, v, h('b', { 'data-conductor-source': 'b.vue:1' })))
    expect(hintKindsOf(new FakeDoc(root) as unknown as Doc)).toEqual(['attribute', 'vue'])
    expect(hintKindsOf(new FakeDoc(h('html')) as unknown as Doc)).toEqual([])
  })

  it("looks for React's kind on the first 64 elements of the body, whatever the head holds", () => {
    const reactApp = () => {
      const app = h('div', { class: 'App' })
      app['__reactFiber$abc'] = { _debugSource: { fileName: '/src/App.tsx' } }
      return app
    }
    const head = (n: number) => h('head', {}, ...Array.from({ length: n }, () => h('style', { 'data-emotion': 'css' })))
    // A full head; then the React element as the body's 64th element and as its 65th.
    const pages = [
      h('html', {}, head(100), h('body', {}, h('div', { id: 'root' }, reactApp()))),
      h('html', {}, head(0), h('body', {}, ...Array.from({ length: 63 }, () => h('i')), reactApp())),
      h('html', {}, head(0), h('body', {}, ...Array.from({ length: 64 }, () => h('i')), reactApp())),
    ]
    expect(pages.map((root) => hintKindsOf(new FakeDoc(root) as unknown as Doc))).toEqual([['react'], ['react'], []])
  })
})
