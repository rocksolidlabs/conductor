import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { aligned, ageBy, atWidths, inside, lines, noOverlap, noSideways, probeColour, rowsInView, tabTo, tapTarget, topAtMost, uncut } from './layout'

// Each layout helper (layout.ts) on pages made for it, no server: it passes a good page and fails a bad one with its measure in the
// message, and fails on a locator that matches nothing, so that a helper that measures nothing cannot pass. Each check of a helper
// has a bad page of its own, so that taking the check out fails a case. The last tests copy the app's own markup shapes.

const BASE = '<style>body{margin:0;font:16px/20px sans-serif}button{font:inherit}</style>'

/** The page holds `html` (and the base style), set as its content. */
async function show(page: Page, html: string): Promise<void> {
  await page.setContent(BASE + html)
}

/** A page served from a URL of its own (routed, no server), so that a reload or an init script reaches it as a load does. */
async function served(page: Page, html: string, waitUntil: 'load' | 'domcontentloaded' = 'load'): Promise<void> {
  await page.route('http://layout.test/', (route) => route.fulfill({ status: 200, contentType: 'text/html', body: BASE + html }))
  await page.goto('http://layout.test/', { waitUntil })
}

/** The message a helper fails with; a helper that passed fails the test. */
async function failure(p: Promise<unknown>): Promise<string> {
  try {
    await p
  } catch (e) {
    return (e as Error).message
  }
  throw new Error('the helper passed a bad page')
}

const NONE = { timeout: 500 }

/**
 * A page whose document.fonts.ready, when a helper asks for it, removes every `.gone` (or, `hide`, sets it display:none): the matches
 * go between the wait and the measure. Configurable, since set content keeps the document and its `fonts` for the next page.
 */
const goes = (hide = false) =>
  `<script>Object.defineProperty(document.fonts, 'ready', { configurable: true, get() { document.querySelectorAll('.gone').forEach((e) => ${hide ? "(e.style.display = 'none')" : 'e.remove()'}); return Promise.resolve(document.fonts) } })</script>`

test('uncut: a whole label passes; a label cut across, cut down, cut by any clip, out of view or out of its box fails with its measure', async ({ page }) => {
  const box = (w: number | string) => `<div id="l" style="width:${w}px;overflow:hidden;white-space:nowrap;text-overflow:ellipsis">Conductor</div>`
  await show(page, box(200))
  expect((await uncut(page.locator('#l'))).got).toHaveLength(1)
  // A Range's line sits up to 0.9 px from the layout's (13 px on 22.75 px lines here): down, a text is judged by how tall it is.
  await show(page, '<div id="l" style="font-size:13px;line-height:22.75px;width:200px;overflow:hidden;white-space:nowrap">e2e short</div>')
  await uncut(page.locator('#l'))
  // Text not drawn (a hidden tail) is not read.
  await show(page, `<div id="l" style="width:100px;overflow:hidden;white-space:nowrap">short<span style="visibility:hidden">${' and a hidden tail'.repeat(4)}</span></div>`)
  await uncut(page.locator('#l'))
  // An emoji's taller glyphs: its line is the line height, so a box one line tall holds it whole.
  await show(page, '<div id="l" style="width:200px;height:20px;overflow:hidden">Deploy 🚀 done ✅</div>')
  await uncut(page.locator('#l'))
  expect(await lines(page.locator('#l'))).toBe(1)
  await show(page, box(40))
  await expect(uncut(page.locator('#l'))).rejects.toThrow(/`Conductor`'s text is [\d.]+ px wide in 40\.0 px/)
  // Cut by a third of a pixel: the rounded scroll and client widths agree, the Range does not.
  await show(page, '<span id="l" style="white-space:nowrap">Conductor</span>')
  const w = await page.locator('#l').evaluate((el) => el.getBoundingClientRect().width)
  await show(page, box((w - 0.33).toFixed(2)))
  expect(await page.locator('#l').evaluate((el) => el.scrollWidth - el.clientWidth)).toBe(0)
  await expect(uncut(page.locator('#l'))).rejects.toThrow(/`Conductor`'s text is [\d.]+ px wide in [\d.]+ px/)
  // The content box is the box less its padding; a box that clips by contain: paint clips its text as overflow does.
  await show(page, `<div id="l" style="width:${(w - 5).toFixed(2)}px;padding-right:10px;overflow:hidden;white-space:nowrap">Conductor</div>`)
  await expect(uncut(page.locator('#l'))).rejects.toThrow(/`Conductor`'s text is [\d.]+ px wide in [\d.]+ px/)
  await show(page, '<div id="l" style="width:40px;contain:paint;white-space:nowrap">Conductor</div>')
  await expect(uncut(page.locator('#l'))).rejects.toThrow(/`Conductor`'s text is [\d.]+ px wide in 40\.0 px/)
  await show(page, '<div id="l" style="width:60px;height:20px;overflow:hidden">two words wrap</div>')
  await expect(uncut(page.locator('#l'))).rejects.toThrow(/`two words wrap`'s text is 60\.0 px tall in 20\.0 px/)
  // Cut by a parent that clips, by a list that scrolls only up and down (cut sideways), by contain: paint, by clip-path.
  for (const clip of ['overflow:hidden', 'overflow-x:hidden;overflow-y:auto', 'contain:paint', 'clip-path:inset(0)']) {
    await show(page, `<div style="width:40px;${clip}"><span id="l" style="display:inline-block;white-space:nowrap">Conductor</span></div>`)
    await expect(uncut(page.locator('#l')), clip).rejects.toThrow(/`Conductor` is cut: [\d.]+ px on the right of its box is out of view/)
  }
  await show(page, `<div style="width:200px;height:40px;overflow:hidden"><p id="l" style="margin:0">${'a line of words '.repeat(8)}</p></div>`)
  await expect(uncut(page.locator('#l'))).rejects.toThrow(/is cut: [\d.]+ px on the bottom/)
  // Scrolled out of its list: none of it is seen, so it cannot be told whole; a match not drawn is skipped and counted.
  await show(page, '<div style="height:40px;overflow:auto"><div style="height:100px"></div><span id="l" style="display:inline-block">below</span></div><span class="n" style="display:inline-block">one</span><span class="n" style="display:none">two</span>')
  await expect(uncut(page.locator('#l'))).rejects.toThrow(/`below` is not in view/)
  expect((await uncut(page.locator('.n'))).skipped).toBe(1)
  // Text out of a box that does not clip it, and an inline element: what cuts them is not seen, so they fail, saying what to give.
  await show(page, '<div id="l" style="width:40px;white-space:nowrap">Conductor</div>')
  await expect(uncut(page.locator('#l'))).rejects.toThrow(/`Conductor`'s text runs out of its box, which does not clip it/)
  await show(page, '<div style="width:200px;overflow:hidden"><span id="l">Conductor</span></div>')
  await expect(uncut(page.locator('#l'))).rejects.toThrow(/`Conductor` has no box of its own to measure \(inline\)/)
})

test('inside: a title in a stretched flex-col button passes; one that runs out of its row, or a part out of any side, fails', async ({ page }) => {
  // The markup of the decision list's row (pages/decisions.vue): a button in a column, a line in it with a truncated title and a time.
  const row = (align: string) => `
    <button id="row" style="display:flex;flex-direction:column;align-items:${align};width:300px;padding:8px;border:1px solid">
      <span style="display:flex;align-items:baseline;justify-content:space-between;gap:8px">
        <span id="title" style="overflow:hidden;white-space:nowrap;text-overflow:ellipsis">${'Deploy the release candidate to every region '.repeat(3)}</span>
        <span style="flex-shrink:0">5m</span>
      </span>
    </button>`
  await show(page, row('stretch'))
  await inside(page.locator('#title'), page.locator('#row'))
  await show(page, row('flex-start'))
  await expect(inside(page.locator('#title'), page.locator('#row'))).rejects.toThrow(/`Deploy the release candidate.*` runs [\d.]+ px past right edge of `Deploy/)
  const at = (l: number, t: number) => `<i class="p" style="position:absolute;left:${l}px;top:${t}px;width:20px;height:20px">p</i>`
  await show(page, `<div id="c" style="position:relative;margin:50px;width:200px;height:200px">c${at(-10, 50)}${at(50, -10)}${at(100, 190)}${at(190, 100)}</div>`)
  const said = await failure(inside(page.locator('.p'), page.locator('#c')))
  for (const side of ['left', 'right', 'top', 'bottom']) expect(said).toMatch(new RegExp(`\`p\` runs 10\\.0 px past ${side} edge of \`c`))
})

test('noOverlap: pieces apart pass; pieces into each other, a group, a name under a button or off screen fail', async ({ page }) => {
  const two = (gap: number, extra = '') => `<div id="h" style="position:relative;height:40px">
    <button style="position:absolute;left:0;top:0;width:100px;height:32px">Stop</button>
    <button style="position:absolute;left:${100 + gap}px;top:0;width:100px;height:32px">Open</button>${extra}</div>`
  await show(page, two(8))
  expect((await noOverlap(page.locator('#h'))).got).toHaveLength(2)
  await show(page, two(-4))
  await expect(noOverlap(page.locator('#h'))).rejects.toThrow(/`Stop` and `Open` overlap by 4\.0 x 32\.0 px/)
  // A count drawn over its neighbour inside one group: the group is a piece, and so is each thing in it. Named in `ignore`, it passes.
  const group = '<div id="h" style="display:flex;gap:8px;padding:8px"><span class="inline-flex" style="position:relative"><button style="width:80px">Retry</button><span data-slot="badge" style="position:absolute;right:-6px;top:-6px">3</span></span><button>Stop</button></div>'
  await show(page, group)
  await expect(noOverlap(page.locator('#h'))).rejects.toThrow(/`Retry` and `3` overlap/)
  expect((await noOverlap(page.locator('#h'), undefined, { ignore: page.locator('[data-slot="badge"]') })).skipped).toBe(1)
  // A button over a name in plain text fails; a name cut by its own box beside the button passes.
  await show(page, '<div id="h" style="position:relative;height:40px"><span style="position:absolute;left:8px;white-space:nowrap">a-very-long-session-name-cut</span><button style="position:absolute;left:120px;width:80px">Stop</button></div>')
  await expect(noOverlap(page.locator('#h'))).rejects.toThrow(/`a-very-long-session-name-cut` and `Stop` overlap/)
  await show(page, '<div id="h" style="display:flex;width:300px"><span style="flex:1;min-width:0;overflow:hidden;white-space:nowrap;text-overflow:ellipsis">a-very-long-session-name-cut-by-its-box</span><button>Stop</button></div>')
  await noOverlap(page.locator('#h'))
  // A name that is the own text of the element holding the button over it, and the root's own text, are compared with the button.
  await show(page, '<div id="h"><div style="position:relative;white-space:nowrap;width:300px">a-long-session-name<button style="position:absolute;left:60px;top:0">Stop</button></div><button>Open</button></div>')
  await expect(noOverlap(page.locator('#h'))).rejects.toThrow(/`a-long-session-name` and `Stop` overlap/)
  await show(page, '<div id="h" style="position:relative;white-space:nowrap;width:300px">a-long-session-name<button style="position:absolute;left:60px;top:0">Stop</button></div>')
  await expect(noOverlap(page.locator('#h'))).rejects.toThrow(/`a-long-session-name` and `Stop` overlap/)
  // Text by its line boxes: a wrapped title and the badge on its last line are apart; stacked labels with a tight line height touch.
  await show(page, '<div id="h" style="width:260px"><span>A ticket title long enough that it wraps onto a second line</span> <span data-slot="badge">ready</span></div>')
  await noOverlap(page.locator('#h'))
  await show(page, '<div id="h"><div style="line-height:14px">a session name</div><div style="line-height:14px">its subtitle below</div></div>')
  await noOverlap(page.locator('#h'))
  // Text out of a box that does not clip it fails: what cuts it is not seen.
  await show(page, '<div id="h" style="display:flex;gap:8px"><div style="width:60px;white-space:nowrap">a-very-long-session-name</div><button>Stop</button></div>')
  await expect(noOverlap(page.locator('#h'))).rejects.toThrow(/`a-very-long-session-name`'s text runs from .* out of its box/)
  // Off screen across fails, judged where it is seen: a span past the window inside a label that cuts it is not off screen.
  await show(page, '<div id="h" style="position:relative;height:40px"><button style="position:absolute;left:0;width:100px">Stop</button><button style="position:absolute;right:-60px;width:100px">Open</button></div>')
  await expect(noOverlap(page.locator('#h'))).rejects.toThrow(/`Open` is off screen/)
  await show(page, `<div id="h" style="display:flex"><p style="margin:0;flex:1;min-width:0;overflow:hidden;white-space:nowrap;text-overflow:ellipsis">Name <span>· ${'a branch name '.repeat(40)}</span></p><button>Stop</button></div>`)
  await noOverlap(page.locator('#h'))
  // Not measured: a piece hidden, at opacity 0, a pixel across (a screen reader's text), or scrolled out of its list.
  await show(page, two(8, '<button style="position:absolute;left:20px;width:100px;visibility:hidden">Ghost</button><button style="position:absolute;left:20px;width:100px;opacity:0">Faded</button>'))
  expect((await noOverlap(page.locator('#h'))).got).toHaveLength(2)
  await show(page, two(8, '<span style="position:absolute;left:10px;top:5px;width:1px;height:1px;overflow:hidden;white-space:nowrap">for a screen reader</span>'))
  expect((await noOverlap(page.locator('#h'))).got).toHaveLength(2)
  await show(page, '<div id="h" style="position:relative"><div style="height:40px;overflow:auto"><button style="display:block;height:40px;width:100px">Top</button><button style="display:block;height:40px;width:100px">Hidden</button></div><button style="position:absolute;top:40px;left:0;width:100px;height:40px">Below</button></div>')
  expect((await noOverlap(page.locator('#h'))).got).toHaveLength(2)
})

test('aligned: a column at one edge passes; a cell or its text 3 px off fails naming its row; a row or a cell not drawn is told', async ({ page }) => {
  const table = (offs: number[], hide = -1, pad = -1) =>
    offs.map((o, i) => `<div class="r" style="display:${i === hide ? 'none' : 'flex'}"><span style="width:${80 + o}px">t${i}</span><span class="c" style="padding-left:${i === pad ? 3 : 0}px">state</span></div>`).join('')
  await show(page, table([0, 0, 0]))
  expect(new Set((await aligned(page.locator('.r'), '.c')).got).size).toBe(1)
  await show(page, table([0, 0, 3]))
  await expect(aligned(page.locator('.r'), '.c')).rejects.toThrow(/row 3's \.c starts at 83\.0 px, row 1's at 80\.0 px/)
  await show(page, table([0, 3, 0], 1))
  expect((await aligned(page.locator('.r'), '.c')).skipped).toBe(1)
  // The cells' boxes agree, the text in one sits 3 px in: the boxes pass, the content fails.
  await show(page, table([0, 0, 0], -1, 2))
  await aligned(page.locator('.r'), '.c')
  await expect(aligned(page.locator('.r'), '.c', { content: true })).rejects.toThrow(/row 3's \.c starts at 83\.0 px, row 1's at 80\.0 px/)
  // A row without the cell, or with it not drawn, fails: every row has it, empty or not.
  await show(page, '<div class="r"><span class="c">a</span></div><div class="r"><span>b</span></div><div class="r" style="height:20px"><span class="c" style="display:none">c</span></div>')
  const said = await failure(aligned(page.locator('.r'), '.c'))
  for (const row of [/row 2 has no \.c drawn/, /row 3 has no \.c drawn/]) expect(said).toMatch(row)
})

test('rowsInView: the rows wholly in view and uncovered, whatever clips or covers them', async ({ page }) => {
  await page.setViewportSize({ width: 800, height: 600 })
  const rows = (n = 30, style = (_: number) => '') => Array.from({ length: n }, (_, i) => `<li class="row" style="height:40px;${style(i)}">${i}</li>`).join('')
  const list = (h: number, style = '', inner = rows()) => `<ul id="s" style="margin:0;padding:0;height:${h}px;overflow:auto;${style}">${inner}</ul>`
  await show(page, list(200))
  expect(await rowsInView(page.locator('#s'), '.row')).toBe(5)
  await show(page, list(199))
  expect(await rowsInView(page.locator('#s'), '.row')).toBe(4)
  await show(page, list(199, 'border:1px solid'))
  expect(await rowsInView(page.locator('#s'), '.row')).toBe(4)
  await show(page, `<div style="height:120px;overflow:hidden">${list(400)}</div>`)
  expect(await rowsInView(page.locator('#s'), '.row')).toBe(3)
  await show(page, list(1000))
  expect(await rowsInView(page.locator('#s'), '.row')).toBe(15)
  // A clip between the scroller and the rows; rows cut sideways; a row not drawn.
  await show(page, list(600, '', `<div style="max-height:230px;overflow:hidden">${rows(6)}</div>`))
  expect(await rowsInView(page.locator('#s'), '.row')).toBe(5)
  await show(page, `<div style="width:40px;overflow:hidden">${list(200, 'width:200px')}</div>`)
  expect(await rowsInView(page.locator('#s'), '.row')).toBe(0)
  await show(page, list(200, '', rows(30, (i) => (i === 1 ? 'visibility:hidden' : ''))))
  expect(await rowsInView(page.locator('#s'), '.row')).toBe(4)
  // Covered: a fixed bar over the foot of the list, a sticky header in it.
  await show(page, `${list(400)}<nav style="position:fixed;left:0;right:0;bottom:0;height:280px;background:#eee">bar</nav>`)
  expect(await rowsInView(page.locator('#s'), '.row')).toBe(8)
  await show(page, list(200, '', `<div style="position:sticky;top:0;height:40px;background:#eee">head</div>${rows()}`))
  await page.locator('#s').evaluate((el) => (el.scrollTop = 100))
  expect(await rowsInView(page.locator('#s'), '.row')).toBe(3)
  // An overlay at opacity 0 covers nothing; the first scroller drawn is the one read.
  await show(page, `${list(400)}<nav style="position:fixed;left:0;right:0;bottom:0;height:280px;opacity:0">bar</nav>`)
  expect(await rowsInView(page.locator('#s'), '.row')).toBe(10)
  await show(page, `<ul class="s" style="display:none">${rows(2)}</ul>${list(200).replace('id="s"', 'class="s"')}`)
  expect(await rowsInView(page.locator('.s'), '.row')).toBe(5)
  // A row that takes no pointer cannot be hit-tested: it fails.
  await show(page, list(200, '', rows(30, () => 'pointer-events:none')))
  await expect(rowsInView(page.locator('#s'), '.row')).rejects.toThrow(/a row that takes no pointer/)
})

test('topAtMost: an item 100 px down passes at 100; 10 px too low for 90 it fails with where it starts and how many matched', async ({ page }) => {
  await show(page, '<div style="height:100px"></div><div class="t">first ticket</div><div class="t">second</div>')
  expect(await topAtMost(page.locator('.t'), 100)).toBe(100)
  await expect(topAtMost(page.locator('.t'), 90)).rejects.toThrow(/\(the first drawn of 2 matches\) starts 100\.0 px down the window, past 90 px/)
})

test('lines: the drawn lines of the text, less what its own clip hides; three past two fail with the count', async ({ page }) => {
  const text = (n: number, style = '') => `<p id="q" style="width:200px;margin:0;${style}">${Array.from({ length: n }, () => '<span style="display:inline-block;width:190px">a line</span>').join(' ')}</p>`
  await show(page, text(2))
  expect(await lines(page.locator('#q'), 2)).toBe(2)
  await show(page, text(3))
  await expect(lines(page.locator('#q'), 2)).rejects.toThrow(/is 3 lines \(its text's line boxes\), past 2/)
  await show(page, text(2, 'line-height:normal'))
  expect(await lines(page.locator('#q'), 2)).toBe(2)
  // Three text nodes and a bigger word on one line are one line.
  await show(page, '<p id="q" style="margin:0">one <b style="font-size:24px;line-height:30px">two</b> three</p>')
  expect(await lines(page.locator('#q'), 1)).toBe(1)
  // Three lines drawn in a box held to two lines' height are three; clamped by its own clip, two.
  await show(page, text(3, 'height:40px'))
  await expect(lines(page.locator('#q'), 2)).rejects.toThrow(/is 3 lines/)
  await show(page, text(3, 'height:40px;overflow:hidden'))
  expect(await lines(page.locator('#q'), 2)).toBe(2)
})

test('noSideways: a page that fits passes; a document, a clipping shell, a panel, the root or text wider than the window fails', async ({ page }) => {
  await page.setViewportSize({ width: 800, height: 600 })
  await show(page, '<div style="width:100%">fits</div>')
  expect((await noSideways(page)).document).toBe(0)
  await show(page, '<div style="width:2000px">too wide</div>')
  await expect(noSideways(page)).rejects.toThrow(/the document scrolls 1200 px sideways/)
  // The app's shell: fixed and clipping, so the document never scrolls; what it holds past the window does, on either side.
  const shell = (inner: string) => `<div style="position:fixed;inset:0;overflow:hidden">${inner}</div>`
  await show(page, shell('<div id="w" style="width:2000px">wide</div>'))
  await expect(noSideways(page)).rejects.toThrow(/`div#w` is cut sideways: 0\.0 to 800\.0 px seen of 0\.0 to 2000\.0 px in a 800 px window/)
  await show(page, shell('<div id="w" style="position:absolute;left:-100px;width:300px">wide</div>'))
  await expect(noSideways(page)).rejects.toThrow(/`div#w` is cut sideways: 0\.0 to 200\.0 px seen of -100\.0 to 200\.0 px/)
  await show(page, shell('<div id="p" style="height:100%;overflow:auto"><div style="width:2000px">wide</div></div>'))
  await expect(noSideways(page)).rejects.toThrow(/`div#p` scrolls 1200 px sideways/)
  expect((await noSideways(page, { allow: page.locator('#p') })).skipped).toBe(1)
  await expect(noSideways(page, { allow: page.locator('#nothing') })).rejects.toThrow(/allow matched nothing/)
  await expect(noSideways(page, { root: page.locator('#p'), allow: page.locator('#p') })).rejects.toThrow(/nothing in .* drawn in view to check/)
  // The root itself is checked; text that runs out of a box that does not clip it fails, since what cuts it is not seen.
  await show(page, '<div id="strip" style="width:300px;overflow-x:auto"><div style="width:600px">wide</div></div>')
  await expect(noSideways(page, { root: page.locator('#strip') })).rejects.toThrow(/`div#strip` scrolls 300 px sideways/)
  await show(page, shell(`<p id="t" style="margin:0;white-space:nowrap">${'a long branch name '.repeat(20)}</p>`))
  await expect(noSideways(page)).rejects.toThrow(/`p#t`'s text runs from 0\.0 to [\d.]+ px, out of its box \(0\.0 to 800\.0\), which does not clip it/)
  // Not counted: a hidden panel that scrolls, a drawer wholly off screen, a tip at opacity 0, a rule a pixel tall.
  await show(page, '<div style="visibility:hidden;overflow:auto;width:100px"><div style="width:200px">x</div></div><div>fits</div>')
  await noSideways(page)
  await show(page, '<div style="overflow:auto;width:100px"><div style="width:200px">x</div></div><div>fits</div>')
  await expect(noSideways(page)).rejects.toThrow(/`div` scrolls 100 px sideways/)
  await show(page, shell('<div>fits</div><aside style="position:absolute;left:-300px;width:280px">drawer</aside><div style="position:absolute;left:600px;width:400px;opacity:0">tip</div><div style="height:1px;width:3000px;background:#000"></div>'))
  expect((await noSideways(page)).checked).toBeGreaterThan(0)
  await show(page, '')
  await expect(noSideways(page, NONE)).rejects.toThrow(/noSideways: nothing matched/)
})

test('tapTarget: a 44 px button passes; one 32 px tall or 32 px wide fails with its size', async ({ page }) => {
  await show(page, '<button id="b" style="width:44px;height:44px">A</button>')
  await tapTarget(page.locator('#b'))
  await show(page, '<button id="b" style="width:120px;height:32px">Answer</button>')
  await expect(tapTarget(page.locator('#b'))).rejects.toThrow(/`Answer` is 120\.0 x 32\.0 px, under 44 x 44/)
  await show(page, '<button id="b" style="width:32px;height:44px">X</button>')
  await expect(tapTarget(page.locator('#b'))).rejects.toThrow(/`X` is 32\.0 x 44\.0 px, under 44 x 44/)
})

test('tabTo: a ring focus adds on something drawn passes; what focus does not add, or adds where nobody sees it, fails', async ({ page }) => {
  const controls = (css: string, go = '<button id="go">Go</button>') => `<style>${css}</style><input id="from"><a href="#x">a link</a>${go}`
  const reach = () => tabTo(page, page.locator('#go'), 3, page.locator('#from'))
  const wrapped = '<span class="w"><button id="go"><span>Go</span></button></span>'
  for (const good of [
    '',
    '#go{outline:none} #go:focus-visible{box-shadow:0 0 0 2px blue}',
    '#go{outline:none} .w:focus-within{outline:2px solid blue}',
    "#go{outline:none;position:relative} #go:focus-visible::after{content:'';position:absolute;inset:-4px;border:2px solid blue}",
    '#go{outline:none} #go:focus-visible span{outline:2px solid blue}',
    '#go{outline:none;box-shadow:0 0 0 3px transparent;transition:box-shadow 300ms 1s} #go:focus-visible{box-shadow:0 0 0 3px blue}',
  ]) {
    await show(page, controls(good, wrapped))
    expect(await reach(), good).toBe(2)
  }
  // Each a button with no focus style of its own but what it shows at rest, or whose focus adds what nobody sees.
  for (const bad of [
    '#go{outline:none;box-shadow:none}',
    '#go{outline:none;box-shadow:0 1px 2px rgba(0,0,0,.2)}',
    '#go{outline:2px solid transparent;outline-offset:2px}',
    '#go{outline:none} #go:focus-visible{outline:2px solid transparent;outline-offset:2px}',
    '#go{outline:none;box-shadow:0 0 #0000, 0 0 #0000}',
    '#go{outline:none;box-shadow:inset 0 0 0 1px #888}',
    '#go{outline:none;box-shadow:0 10px 20px 4px #000}',
    '#go{outline:none} #go:focus-visible{box-shadow:0 0 0 0 blue, 0 0 0 3px transparent}',
    '#go{outline:none;border:2px solid #ccc} #go:focus-visible{border-color:blue}',
    '@keyframes pulse{from{box-shadow:0 0 0 0 red}to{box-shadow:0 0 0 6px red}} #go{outline:none;animation:pulse 1s infinite}',
    "#go{outline:none;position:relative} #go:focus-visible::after{content:'';position:absolute;inset:-4px;border:2px solid blue;opacity:0}",
    '#go{outline:none} #go:focus-visible::after{border:2px solid blue}',
    '#go{outline:none} #go span{display:none} #go:focus-visible span{outline:2px solid blue}',
    '.w{opacity:0} #go:focus-visible{outline:2px solid blue}',
  ]) {
    await show(page, controls(bad, wrapped))
    expect(await failure(reach()), bad).toMatch(/draws no focus ring/)
  }
  // A ring on an ancestor that holds another control is no ring of the target's.
  await show(page, controls('#go{outline:none} .w:focus-within{outline:2px solid blue}', '<span class="w"><button id="go">Go</button><button>Other</button></span>'))
  expect(await failure(reach())).toMatch(/draws no focus ring/)
  // A ring outside the box that a wrapper as big as the button cuts away entirely; the same ring unwrapped passes.
  const ring = '#go{outline:none} #go:focus-visible{outline:3px solid rgb(0,120,255);outline-offset:0}'
  await show(page, controls(ring, '<span style="display:inline-flex"><button id="go">Go</button></span>'))
  expect(await reach()).toBe(2)
  await show(page, controls(ring, '<span style="display:inline-flex;overflow:hidden"><button id="go">Go</button></span>'))
  expect(await failure(reach())).toMatch(/ring \(outline 3px solid\) lies outside its box and a clip cuts all of it away/)
  expect(await page.locator('#go').evaluate((el) => [el.style.position, el.children.length])).toEqual(['', 0])
  await show(page, controls(''))
  await expect(tabTo(page, page.locator('#go'), 1, page.locator('#from'))).rejects.toThrow(/was not reached in 1 presses of Tab/)
  await show(page, '<button id="a">a</button><div id="from">not focusable</div><button id="b">b</button><button id="go">go</button>')
  await page.locator('#b').focus()
  await expect(tabTo(page, page.locator('#go'), 5, page.locator('#from'))).rejects.toThrow(/#from.* cannot take the focus/)
})

test('a match gone or hidden between the wait and the measure fails; only drawn matches are measured, the rest counted', async ({ page }) => {
  for (const hide of [false, true]) {
    await show(page, `${goes(hide)}<div class="gone" style="width:200px">label</div>`)
    await expect(uncut(page.locator('.gone'))).rejects.toThrow(/uncut: .* \d matched, none drawn at the measure/)
    await show(page, `${goes(hide)}<div id="c">c<span class="gone">part</span></div>`)
    await expect(inside(page.locator('.gone'), page.locator('#c'))).rejects.toThrow(/inside: the part .* matched, none drawn at the measure/)
    await show(page, `${goes(hide)}<div class="gone"><span class="c">a</span></div>`)
    await expect(aligned(page.locator('.gone'), '.c')).rejects.toThrow(/aligned: .* matched, none drawn at the measure/)
    await show(page, `${goes(hide)}<button class="gone" style="width:44px;height:44px">A</button>`)
    await expect(tapTarget(page.locator('.gone'))).rejects.toThrow(/tapTarget: .* matched, none drawn at the measure/)
    await show(page, `${goes(hide)}<div style="height:300px"></div><div class="gone">ticket</div>`)
    await expect(topAtMost(page.locator('.gone'), 90)).rejects.toThrow(/topAtMost: .* matched, none drawn at the measure/)
    await show(page, `${goes(hide)}<p class="gone" style="width:100px;margin:0">${'words '.repeat(40)}</p>`)
    await expect(lines(page.locator('.gone'), 2)).rejects.toThrow(/lines: .* matched, none drawn at the measure/)
    await show(page, `${goes(hide)}<ul id="s" style="height:200px;overflow:auto;margin:0"><li class="row gone" style="height:40px">a</li></ul>`)
    await expect(rowsInView(page.locator('#s'), '.row')).rejects.toThrow(/rowsInView: a row of .* matched, none drawn at the measure/)
  }
  // The first match hidden at the measure and a second drawn: the second is measured.
  await show(page, `${goes(true)}<div style="height:300px"></div><div class="t gone">first</div><div class="t">second</div>`)
  await expect(topAtMost(page.locator('.t'), 90)).rejects.toThrow(/starts 300\.0 px down the window/)
  // A hidden first match does not decide the wait: the drawn ones are measured, the hidden counted.
  await show(page, '<button class="b" style="display:none">session</button><button class="b" style="width:44px;height:44px">A</button><button class="b" style="width:20px;height:20px;visibility:hidden">B</button>')
  expect((await tapTarget(page.locator('.b'), NONE)).skipped).toBe(2)
  await show(page, '<button class="b" style="display:none">session</button>')
  await expect(tapTarget(page.locator('.b'), NONE)).rejects.toThrow(/tapTarget: 1 matched, none drawn/)
})

test('a helper measures once the fonts have loaded: a label whole in the fallback font and cut in the web font fails', async ({ page }) => {
  const here = dirname(fileURLToPath(import.meta.url))
  const font = readFileSync(join(here, '../node_modules/@fontsource-variable/jetbrains-mono/files/jetbrains-mono-latin-wght-normal.woff2'))
  let release = () => {}
  const held = new Promise<void>((r) => (release = r))
  await page.route('http://layout.test/late.woff2', async (route) => {
    await held
    await route.fulfill({ status: 200, contentType: 'font/woff2', body: font })
  })
  // Twelve i's are about 50 px in the fallback and 115 px in the monospaced web font; the page notes when a helper asks for the fonts.
  await served(
    page,
    `<style>@font-face{font-family:Late;src:url(/late.woff2) format("woff2");font-display:swap}</style>
    <script>const ready = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(document.fonts), 'ready').get; Object.defineProperty(document.fonts, 'ready', { get() { window.asked = true; return ready.call(document.fonts) } })</script>
    <div id="l" style="font-family:Late,sans-serif;width:90px;overflow:hidden;white-space:nowrap">iiiiiiiiiiii</div>`,
    'domcontentloaded',
  )
  const measured = uncut(page.locator('#l'))
  await expect.poll(() => page.evaluate(() => (window as unknown as { asked?: boolean }).asked === true)).toBe(true)
  expect(await page.evaluate(() => document.fonts.status)).toBe('loading')
  release()
  await expect(measured).rejects.toThrow(/`iiiiiiiiiiii`'s text is [\d.]+ px wide in 90\.0 px/)
})

test('every helper fails on a locator that matches nothing', async ({ page }) => {
  await show(page, '<div id="here"><button>one</button><button>two</button></div>')
  const none = page.locator('#nothing')
  const here = page.locator('#here')
  await expect(uncut(none, NONE)).rejects.toThrow(/uncut: nothing matched/)
  await expect(inside(none, here, NONE)).rejects.toThrow(/inside: the part: nothing matched/)
  await expect(inside(here, none, NONE)).rejects.toThrow(/inside: the container: nothing matched/)
  await expect(noOverlap(none, undefined, NONE)).rejects.toThrow(/noOverlap: nothing matched/)
  await expect(aligned(none, 'span', NONE)).rejects.toThrow(/aligned: nothing matched/)
  await expect(rowsInView(here, '.row', NONE)).rejects.toThrow(/rowsInView: a row: nothing matched/)
  await expect(topAtMost(none, 100, NONE)).rejects.toThrow(/topAtMost: nothing matched/)
  await expect(lines(none, 2, NONE)).rejects.toThrow(/lines: nothing matched/)
  await expect(tapTarget(none, NONE)).rejects.toThrow(/tapTarget: nothing matched/)
  await expect(tabTo(page, none, 3, here, NONE)).rejects.toThrow(/tabTo: nothing matched/)
  await expect(noSideways(page, { root: none, ...NONE })).rejects.toThrow(/noSideways: nothing matched/)
  await expect(atWidths(page, [{ width: 390, height: 844 }], async () => {}, '#nothing', NONE)).rejects.toThrow(/atWidths: the page at 390 x 844/)
  // A root with one piece measures nothing to overlap: it fails too.
  await show(page, '<div id="one"><button>alone</button></div>')
  await expect(noOverlap(page.locator('#one'))).rejects.toThrow(/noOverlap: the pieces of/)
})

test('atWidths runs at each size it names, after a reload, once what it waits for is drawn, and a failure names its size', async ({ page }) => {
  await served(page, '<main id="root">the page</main>')
  await page.evaluate(() => ((window as unknown as { loaded?: string }).loaded = 'before'))
  const sizes = [
    { width: 390, height: 844 },
    { width: 1280, height: 720 },
  ]
  const seen: Array<[number, number, string | undefined]> = []
  await atWidths(
    page,
    sizes,
    async () => {
      seen.push(await page.evaluate(() => [window.innerWidth, window.innerHeight, (window as unknown as { loaded?: string }).loaded] as [number, number, string | undefined]))
    },
    '#root',
  )
  expect(seen).toEqual([
    [390, 844, undefined],
    [1280, 720, undefined],
  ])
  await expect(atWidths(page, sizes, async (s) => expect(s.width).toBeLessThan(1000), '#root')).rejects.toThrow(/atWidths: at 1280 x 720: /)
})

test("ageBy moves the page's Date.now(), new Date() and Date() ahead by its offset", async ({ page }) => {
  const TWO_HOURS = 2 * 3600_000
  await ageBy(page, TWO_HOURS)
  await served(page, '<p>clock</p>')
  const before = Date.now()
  const [now, made, said, isDate] = await page.evaluate(() => [Date.now(), new Date().getTime(), Date.parse(Date()), new Date() instanceof Date] as const)
  const after = Date.now()
  for (const t of [now, made]) {
    expect(t - TWO_HOURS).toBeGreaterThanOrEqual(before)
    expect(t - TWO_HOURS).toBeLessThanOrEqual(after)
  }
  // Date() as a function answers a string, as the real one does, to the second.
  expect(said - TWO_HOURS).toBeGreaterThanOrEqual(before - 1000)
  expect(said - TWO_HOURS).toBeLessThanOrEqual(after)
  expect(isDate).toBe(true)
  // A date made from a value is that value.
  expect(await page.evaluate(() => new Date(0).getTime())).toBe(0)
})

test('probeColour resolves a value, a token among them, to the sRGB a drawn colour reads as; a value that is no colour fails', async ({ page }) => {
  await show(page, '<style>:root{--mark:oklch(0.62 0.17 145);--glass:oklch(0.62 0.17 145 / 50%)}</style><span id="m" style="color:var(--mark)">mark</span>')
  const token = await probeColour(page, 'var(--mark)')
  expect(token).toMatch(/^rgb\(\d+, \d+, \d+\)$/)
  const drawn = await page.locator('#m').evaluate((el) => getComputedStyle(el).color)
  expect(await probeColour(page, drawn)).toBe(token)
  expect(await probeColour(page, '#ff0000')).toBe('rgb(255, 0, 0)')
  expect(await probeColour(page, 'rgb(0 0 255 / 50%)')).toBe('rgba(0, 0, 255, 0.502)')
  expect(await probeColour(page, 'var(--glass)')).toMatch(/^rgba\(\d+, \d+, \d+, 0\.502\)$/)
  expect(token).not.toBe(await probeColour(page, 'oklch(0.62 0.17 30)'))
  for (const v of ['var(--nope)', 'not-a-colour', '']) await expect(probeColour(page, v), v).rejects.toThrow(/resolves to no colour of its own/)
})

// The app's own shapes: the class lists that matter, copied from the components, with the few utility rules they need. Each good
// page passes the helpers a later row uses on it; each seeded defect, one the helpers met on the built app, fails the right one.
const TW = `<style>
  .flex{display:flex}.flex-col{flex-direction:column}.flex-1{flex:1 1 0%}.flex-none{flex:none}.min-h-0{min-height:0}.min-w-0{min-width:0}
  .items-center{align-items:center}.gap-1{gap:4px}.gap-2{gap:8px}.gap-3{gap:12px}.px-1{padding:0 4px}.px-2\\.5{padding:0 10px}.p-0\\.5{padding:2px}
  .overflow-y-auto{overflow-y:auto}.overflow-x-hidden{overflow-x:hidden}.overflow-hidden{overflow:hidden}.break-words{overflow-wrap:break-word}
  .truncate{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.text-xs{font-size:12px;line-height:16px}.text-sm{font-size:14px;line-height:20px}
  .relative{position:relative}.absolute{position:absolute}.fixed{position:fixed}.inset-0{inset:0}.inset-x-0{left:0;right:0}.bottom-0{bottom:0}
  .right-1\\.5{right:6px}.top-1\\.5{top:6px}.h-14{height:56px}.min-h-11{min-height:44px}.border{border:1px solid #ddd}.border-t{border-top:1px solid #ddd}
  .bg-default{background:#fff}.rounded-md{border-radius:6px}.opacity-0{opacity:0}.outline-none{outline:none}
  .group:hover .group-hover\\:opacity-100,.focus-within\\:opacity-100:focus-within{opacity:1}.focus-within\\:border-accented:focus-within{border-color:#555}
  button{outline:none;min-width:28px;height:28px}button:focus-visible{outline:2px solid #3b82f6}
</style>`

/** The sidebar's list (SessionSidebar.vue, SidebarSessionRow.vue): it scrolls up and down, each row a link with a truncated name and a toolbar shown on hover or focus. */
const sidebar = (o: { list?: string; row?: string; bar?: string; prompt?: string } = {}) =>
  `${TW}<aside class="fixed flex flex-col" style="top:0;bottom:0;left:0;width:260px"><div class="flex-1 min-h-0 overflow-y-auto px-1 flex flex-col gap-3 ${o.list ?? ''}" data-session-list="sidebar">
  ${['alpha', 'bravo', 'charlie'].map((n) => `<li class="group relative flex flex-col rounded-md" data-sidebar-row="s:${n}">
    <div class="flex items-center gap-1"><a href="#${n}" class="flex min-w-0 flex-1 gap-2 rounded-md px-2.5" style="${n === 'bravo' ? (o.row ?? '') : ''}">
      <span class="flex min-w-0 flex-1 flex-col"><span class="truncate text-sm" data-name="${n}">${n}-session</span>
      ${n === 'bravo' ? `<span class="break-words text-xs" data-row-prompt style="${o.prompt ?? ''}">${o.prompt ? 'May I run the migration on the staging database now, and then the one on production after it?' : 'May I run the migration on the staging database now?'}</span>` : ''}</span></a></div>
    <span class="absolute flex items-center gap-1 rounded-md bg-default p-0.5 opacity-0 group-hover:opacity-100 ${o.bar ?? 'focus-within:opacity-100'} right-1.5 top-1.5" data-row-actions>
      <button aria-label="Share ${n}" data-row-share>S</button><button aria-label="Stop ${n}">X</button></span></li>`).join('')}
  </div></aside><main style="margin-left:260px"><button id="from">start</button></main>`

test('the app: the sidebar list passes; a name cut sideways, a toolbar ring under opacity 0, three lines in two fail', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const list = page.locator('[data-session-list]')
  await show(page, sidebar())
  await uncut(list.locator('[data-name]'))
  await noSideways(page)
  await noOverlap(list)
  expect(await rowsInView(list, '[data-sidebar-row]')).toBe(3)
  expect(await tabTo(page, page.locator('[data-row-share]').first(), 2, page.locator('[href="#alpha"]'))).toBe(1)
  expect(await lines(page.locator('[data-row-prompt]'), 2)).toBe(2)
  // The list hides what runs out sideways and a row is wider than it: the name is cut, and so is the row.
  await show(page, sidebar({ list: 'overflow-x-hidden', row: 'padding-left:200px;min-width:400px' }))
  await expect(uncut(list.locator('[data-name="bravo"]'))).rejects.toThrow(/`bravo-session` is cut: [\d.]+ px on the right/)
  await expect(noSideways(page)).rejects.toThrow(/is cut sideways/)
  // The toolbar shown on hover only (its focus-within rule gone): the Share button's ring is drawn under opacity 0.
  await show(page, sidebar({ bar: '' }))
  await expect(tabTo(page, page.locator('[data-row-share]').first(), 2, page.locator('[href="#alpha"]'))).rejects.toThrow(/draws no focus ring/)
  // A longer question held to two lines' height spills a third below it.
  await show(page, sidebar({ prompt: 'display:block;height:32px' }))
  await expect(lines(page.locator('[data-row-prompt]'), 2)).rejects.toThrow(/is 3 lines/)
})

/** A phone's page (layouts/default.vue, BottomBar.vue): the panel's list over the fixed bottom bar, the group padded by the bar's height. */
const phone = (o: { bar?: string; list?: string } = {}) =>
  `${TW}<div class="fixed inset-0 flex overflow-hidden" style="padding-bottom:56px"><div class="flex flex-col min-w-0 flex-1"><div class="h-14 flex items-center px-2.5">Tickets</div>
  <div class="flex-1 min-h-0 overflow-y-auto" id="scroller"><div style="${o.list ?? ''}" data-ticket-list>
  ${Array.from({ length: 30 }, (_, i) => `<a href="#t${i}" class="flex items-center gap-2 px-2.5" style="height:48px" data-ticket-row><span class="flex-none truncate text-xs" style="width:72px" data-row-key>LR-${i}</span><span class="min-w-0 truncate text-sm">Ticket ${i}</span></a>`).join('')}
  </div></div></div></div><nav class="fixed inset-x-0 bottom-0 flex border-t bg-default" style="z-index:40;${o.bar ?? ''}" data-bottom-bar>${['Home', 'Yard', 'Tickets'].map((t) => `<a href="#${t}" class="flex min-h-11 flex-1 items-center" style="justify-content:center">${t}</a>`).join('')}</nav>`

test('the app: the phone page passes; a bottom bar grown over the list and a clip in the list fail the count, a cell 3 px in fails the column', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const scroller = page.locator('#scroller')
  await show(page, phone())
  // 844 less the 56 px header and the 56 px the group keeps for the bar: 732 px, fifteen rows of 48 px.
  expect(await rowsInView(scroller, '[data-ticket-row]')).toBe(15)
  await noSideways(page)
  await noOverlap(page.locator('[data-bottom-bar]'))
  await aligned(page.locator('[data-ticket-row]'), '[data-row-key]', { content: true })
  // The bar grown to 160 px (two-line labels, a larger safe area) covers the foot of the list.
  await show(page, phone({ bar: 'height:160px' }))
  expect(await rowsInView(scroller, '[data-ticket-row]')).toBe(13)
  // The list in the scroller clips at half a row: the last row is half hidden.
  await show(page, phone({ list: 'max-height:600px;overflow:hidden' }))
  expect(await rowsInView(scroller, '[data-ticket-row]')).toBe(12)
  // One key's text sits 3 px in: the column's content is out though the cells' boxes agree.
  await show(page, phone())
  await page.locator('[data-row-key]').nth(3).evaluate((el) => (el.style.paddingLeft = '3px'))
  await aligned(page.locator('[data-ticket-row]'), '[data-row-key]')
  await expect(aligned(page.locator('[data-ticket-row]'), '[data-row-key]', { content: true })).rejects.toThrow(/row 4's \[data-row-key\] starts at 13\.0 px, row 1's at 10\.0 px/)
})

/** A Yard tile (SessionTile.vue): it clips, its border lights for any focus inside it, its header holds the name and Open beside the terminal. */
const tile = (open = '', wrap = false) =>
  `${TW}<button id="from">start</button><div class="relative" style="width:420px;height:260px;margin:16px"><div class="group flex flex-col overflow-hidden rounded-md border outline-none focus-within:border-accented" style="height:100%" role="group" tabindex="0" data-session-tile>
  <div class="flex items-center gap-2 px-2.5" style="border-bottom:1px solid #ddd;padding-top:4px;padding-bottom:4px" data-tile-header><span class="truncate flex-1 text-sm">charlie-session</span>
  ${wrap ? '<span class="overflow-hidden rounded-md" style="display:inline-flex">' : ''}<button aria-label="Open charlie-session" style="${open}" data-tile-open>O</button>${wrap ? '</span>' : ''}</div>
  <div class="flex-1 min-h-0"><textarea aria-label="terminal" style="width:100%;height:100%;border:0"></textarea></div></div></div>`

test("the app: a Yard tile's Open button passes; its ring on the tile only, or cut away by a wrapper, fails", async ({ page }) => {
  const reach = () => tabTo(page, page.locator('[data-tile-open]'), 3, page.locator('#from'))
  await show(page, tile())
  expect(await reach()).toBe(2)
  await noOverlap(page.locator('[data-tile-header]'))
  await show(page, tile('outline:none!important'))
  expect(await failure(reach())).toMatch(/draws no focus ring/)
  await show(page, tile('outline-offset:0', true))
  expect(await failure(reach())).toMatch(/lies outside its box and a clip cuts all of it away/)
})

/** A session's header (pages/sessions/[id].vue): the name truncated beside its badge, the buttons at the right. */
const header = (name: string) =>
  `${TW}<div class="h-14 flex items-center gap-2 px-2.5" style="width:390px;box-sizing:border-box" id="hdr"><div class="flex min-w-0 flex-1 flex-col"><div class="flex items-center gap-2 min-w-0">
  <span class="${name}" data-session-title>${'a-session-with-a-name-that-is-long-'.repeat(2)}</span><span class="inline-flex flex-none text-xs" data-slot="badge">needs you</span></div></div>
  <button aria-label="Files">F</button><button aria-label="Share">S</button><button aria-label="More">M</button></div>`

test('the app: the header with its name cut beside the buttons passes; the name without its truncate runs under them and fails', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await show(page, header('truncate text-sm'))
  await noOverlap(page.locator('#hdr'))
  await noSideways(page)
  await show(page, header('text-sm" style="white-space:nowrap'))
  await expect(noOverlap(page.locator('#hdr'))).rejects.toThrow(/`a-session-with-a-name-that-is…` and `F` overlap by [\d.]+ x 20\.0 px/)
})

test('the app: a wrapped title and the badge after it pass; the badge pulled over the title fails', async ({ page }) => {
  const row = (badge: string) =>
    `${TW}<p id="row" class="text-sm" style="width:240px;margin:0"><span data-row-title>Run the staging migration and report what changed</span> <span class="inline-flex text-xs" data-slot="badge" style="${badge}">blocked</span></p>`
  await show(page, row(''))
  await noOverlap(page.locator('#row'))
  await show(page, row('margin-left:-60px'))
  await expect(noOverlap(page.locator('#row'))).rejects.toThrow(/`Run the staging migration and…` and `blocked` overlap/)
})

test('the app: a panel in the fixed, clipping shell passes at 390; a panel wider than the window fails', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const shell = (panel: string) =>
    `${TW}<div class="fixed inset-0 flex overflow-hidden"><div class="flex flex-col min-w-0 flex-1" id="panel" style="${panel}"><div class="h-14 flex items-center px-2.5">Home</div><div class="flex-1 overflow-y-auto" style="padding:16px"><p class="break-words" style="margin:0">${'words '.repeat(60)}</p></div></div></div>`
  await show(page, shell(''))
  await noSideways(page)
  await show(page, shell('min-width:3000px'))
  await expect(noSideways(page)).rejects.toThrow(/`div#panel\.flex\.flex-col\.min-w-0` is cut sideways: 0\.0 to 390\.0 px seen of 0\.0 to 3000\.0 px/)
})
