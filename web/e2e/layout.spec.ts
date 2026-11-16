import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { aligned, ageBy, atWidths, inside, lines, noOverlap, noSideways, probeColour, rowsInView, tabTo, tapTarget, topAtMost, uncut } from './layout'

// Each layout helper (layout.ts) on pages made for it, no server: it passes a good page and fails a bad one with its measure in the
// message, and fails on a locator that matches nothing, so that a helper that measures nothing cannot pass. Each half of a helper
// (a side, a wait, a filter, a guard) has a bad page of its own, so that taking the half out fails a case.

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

/** A page whose document.fonts.ready, when a helper asks for it, removes every `.gone`: the matches go between the wait and the measure. */
const GOES = `<script>Object.defineProperty(document.fonts, 'ready', { get() { document.querySelectorAll('.gone').forEach((e) => e.remove()); return Promise.resolve(document.fonts) } })</script>`

test('uncut: a whole label passes; a label cut across, cut down or cut by an ancestor fails with its measure', async ({ page }) => {
  const box = (w: number) => `<div id="l" style="width:${w}px;overflow:hidden;white-space:nowrap;text-overflow:ellipsis">Conductor</div>`
  await show(page, box(200))
  const [whole] = (await uncut(page.locator('#l'))).got
  expect(whole!.scrollWidth).toBeLessThanOrEqual(whole!.clientWidth)
  await show(page, box(40))
  await expect(uncut(page.locator('#l'))).rejects.toThrow(/`Conductor` is \d+ px wide in 40 px/)
  await show(page, '<div id="l" style="width:60px;height:20px;overflow:hidden">two words wrap</div>')
  await expect(uncut(page.locator('#l'))).rejects.toThrow(/`two words wrap` is \d+ px tall in 20 px/)
  await show(page, '<div class="w" style="width:40px;overflow:hidden"><span id="l" style="display:inline-block;white-space:nowrap">Conductor</span></div>')
  await expect(uncut(page.locator('#l'))).rejects.toThrow(/`Conductor` runs past the box of `div\.w`, which clips it/)
  await show(page, `<div class="w" style="width:200px;height:40px;overflow:hidden"><p id="l" style="margin:0">${'a line of words '.repeat(8)}</p></div>`)
  await expect(uncut(page.locator('#l'))).rejects.toThrow(/runs past the box of `div\.w`/)
  // A label scrolled out of its list is scrolled, not cut; a match not drawn is skipped and counted.
  await show(page, '<div style="height:40px;overflow:auto"><div style="height:100px"></div><span id="l" style="display:inline-block">below</span></div><span class="n" style="display:inline-block">one</span><span class="n" style="display:none">two</span>')
  await uncut(page.locator('#l'))
  expect((await uncut(page.locator('.n'))).skipped).toBe(1)
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
  await expect(inside(page.locator('#title'), page.locator('#row'))).rejects.toThrow(/runs [\d.]+ px past `Deploy the release candidate.*`'s right edge/)
  const at = (l: number, t: number) => `<i class="p" style="position:absolute;left:${l}px;top:${t}px;width:20px;height:20px">p</i>`
  await show(page, `<div id="c" style="position:relative;margin:50px;width:200px;height:200px">c${at(-10, 50)}${at(50, -10)}${at(100, 190)}</div>`)
  const said = await failure(inside(page.locator('.p'), page.locator('#c')))
  for (const side of [/starts 10\.0 px left of/, /starts 10\.0 px above/, /runs 10\.0 px below/]) expect(said).toMatch(side)
})

test('noOverlap: pieces apart pass; pieces into each other, inside one group or over a name, or off screen fail', async ({ page }) => {
  const two = (gap: number) => `<div id="h" style="position:relative;height:40px">
    <button style="position:absolute;left:0;top:0;width:100px;height:32px">Stop</button>
    <button style="position:absolute;left:${100 + gap}px;top:0;width:100px;height:32px">Open</button></div>`
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
  // Off screen across fails; a piece not drawn is not measured.
  await show(page, '<div id="h" style="position:relative;height:40px"><button style="position:absolute;left:0;width:100px">Stop</button><button style="position:absolute;right:-60px;width:100px">Open</button></div>')
  await expect(noOverlap(page.locator('#h'))).rejects.toThrow(/`Open` is off screen/)
  await show(page, two(8).replace('</div>', '<button style="position:absolute;left:20px;width:100px;visibility:hidden">Ghost</button></div>'))
  expect((await noOverlap(page.locator('#h'))).got).toHaveLength(2)
})

test('aligned: a column that starts at one edge passes; a cell 3 px off fails naming its row; a row not drawn is skipped', async ({ page }) => {
  const table = (offs: number[], hide = -1) =>
    offs.map((o, i) => `<div class="r" style="display:${i === hide ? 'none' : 'flex'}"><span style="width:${80 + o}px">t${i}</span><span class="c">state</span></div>`).join('')
  await show(page, table([0, 0, 0]))
  expect(new Set((await aligned(page.locator('.r'), '.c')).got).size).toBe(1)
  await show(page, table([0, 0, 3]))
  await expect(aligned(page.locator('.r'), '.c')).rejects.toThrow(/row 3's \.c starts at 83\.0 px, row 1's at 80\.0 px/)
  await show(page, table([0, 3, 0], 1))
  expect((await aligned(page.locator('.r'), '.c')).skipped).toBe(1)
  // A row without the cell fails: every row has it, empty or not.
  await show(page, '<div class="r"><span class="c">a</span></div><div class="r"><span>b</span></div>')
  await expect(aligned(page.locator('.r'), '.c')).rejects.toThrow(/row 2 has no \.c/)
})

test('rowsInView: the rows wholly in the list, its borders, a clipping box and the window taken off', async ({ page }) => {
  const list = (h: number, style = '') => `<ul id="s" style="margin:0;padding:0;height:${h}px;overflow:auto;${style}">${Array.from({ length: 30 }, (_, i) => `<li class="row" style="height:40px">${i}</li>`).join('')}</ul>`
  await show(page, list(200))
  expect(await rowsInView(page.locator('#s'), '.row')).toBe(5)
  await show(page, list(199))
  expect(await rowsInView(page.locator('#s'), '.row')).toBe(4)
  await show(page, list(199, 'border:1px solid'))
  expect(await rowsInView(page.locator('#s'), '.row')).toBe(4)
  await show(page, `<div style="height:120px;overflow:hidden">${list(400)}</div>`)
  expect(await rowsInView(page.locator('#s'), '.row')).toBe(3)
  await page.setViewportSize({ width: 800, height: 600 })
  await show(page, list(1000))
  expect(await rowsInView(page.locator('#s'), '.row')).toBe(15)
})

test('topAtMost: an item 100 px down passes at 100; 10 px too low for 90 it fails with where it starts and how many matched', async ({ page }) => {
  await show(page, '<div style="height:100px"></div><div class="t">first ticket</div><div class="t">second</div>')
  expect(await topAtMost(page.locator('.t'), 100)).toBe(100)
  await expect(topAtMost(page.locator('.t'), 90)).rejects.toThrow(/\(the first of 2 matches\) starts 100\.0 px down the window, past 90 px/)
})

test('lines: two lines pass where two are allowed; three fail with the count; a normal line height is read as drawn', async ({ page }) => {
  const text = (n: number, style = '') => `<p id="q" style="width:200px;margin:0;${style}">${Array.from({ length: n }, () => '<span style="display:inline-block;width:190px">a line</span>').join(' ')}</p>`
  await show(page, text(2))
  expect(await lines(page.locator('#q'), 2)).toBe(2)
  await show(page, text(3))
  await expect(lines(page.locator('#q'), 2)).rejects.toThrow(/is 3 lines \(60\.0 px of 20\.0 px lines\), past 2/)
  await show(page, text(2, 'line-height:normal'))
  expect(await lines(page.locator('#q'), 2)).toBe(2)
})

test('noSideways: a page that fits passes; a document, a clipping shell or a panel wider than the window fails; an allowed panel is skipped', async ({ page }) => {
  await page.setViewportSize({ width: 800, height: 600 })
  await show(page, '<div style="width:100%">fits</div>')
  expect((await noSideways(page)).document).toBe(0)
  await show(page, '<div style="width:2000px">too wide</div>')
  await expect(noSideways(page)).rejects.toThrow(/the document scrolls 1200 px sideways/)
  // The app's shell: fixed and clipping, so the document never scrolls; what it holds past the window does.
  const shell = (inner: string) => `<div style="position:fixed;inset:0;overflow:hidden">${inner}</div>`
  await show(page, shell('<div id="w" style="width:2000px">wide</div>'))
  await expect(noSideways(page)).rejects.toThrow(/`div#w` runs from 0\.0 to 2000\.0 px in a 800 px window/)
  await show(page, shell('<div id="p" style="height:100%;overflow:auto"><div style="width:2000px">wide</div></div>'))
  await expect(noSideways(page)).rejects.toThrow(/`div#p` scrolls 1200 px sideways/)
  expect((await noSideways(page, { allow: page.locator('#p') })).skipped).toBe(1)
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

test('tabTo: a ring by outline, shadow, a parent or ::after passes; what focus does not add fails; a start that takes no focus fails', async ({ page }) => {
  const controls = (css: string, go = '<button id="go">Go</button>') => `<style>${css}</style><input id="from"><a href="#x">a link</a>${go}`
  const reach = () => tabTo(page, page.locator('#go'), 3, page.locator('#from'))
  for (const good of [
    '',
    '#go{outline:none} #go:focus-visible{box-shadow:0 0 0 2px blue}',
    '#go{outline:none} .w:focus-within{outline:2px solid blue}',
    "#go{outline:none;position:relative} #go:focus-visible::after{content:'';position:absolute;inset:-4px;border:2px solid blue}",
  ]) {
    await show(page, controls(good, '<span class="w"><button id="go">Go</button></span>'))
    expect(await reach(), good).toBe(2)
  }
  // Each a button with no focus style of its own but what it shows at rest, and one whose focus adds a shadow nobody sees.
  for (const bad of [
    'outline:none;box-shadow:none',
    'outline:none;box-shadow:0 1px 2px rgba(0,0,0,.2)',
    'outline:2px solid transparent;outline-offset:2px',
    'outline:none;box-shadow:0 0 #0000, 0 0 #0000',
    'outline:none;box-shadow:inset 0 0 0 1px #888',
    'outline:none;box-shadow:0 10px 20px 4px #000',
    'outline:none} #go:focus-visible{box-shadow:0 0 0 0 blue, 0 0 0 3px transparent',
  ]) {
    await show(page, controls(`#go{${bad}}`))
    expect(await failure(reach()), bad).toMatch(/draws no focus ring/)
  }
  await show(page, controls(''))
  await expect(tabTo(page, page.locator('#go'), 1, page.locator('#from'))).rejects.toThrow(/was not reached in 1 presses of Tab/)
  await show(page, '<button id="a">a</button><div id="from">not focusable</div><button id="b">b</button><button id="go">go</button>')
  await page.locator('#b').focus()
  await expect(tabTo(page, page.locator('#go'), 5, page.locator('#from'))).rejects.toThrow(/#from.* cannot take the focus/)
})

test('a match gone between the wait and the measure fails; only drawn matches are measured, the rest counted', async ({ page }) => {
  await show(page, `${GOES}<div class="gone" style="width:200px">label</div>`)
  await expect(uncut(page.locator('.gone'))).rejects.toThrow(/uncut: nothing of .* drawn at the measure \(0 matched\)/)
  await show(page, `${GOES}<div id="c">c<span class="gone">part</span></div>`)
  await expect(inside(page.locator('.gone'), page.locator('#c'))).rejects.toThrow(/inside: the part: nothing of .* drawn at the measure/)
  await show(page, `${GOES}<div class="gone"><span class="c">a</span></div>`)
  await expect(aligned(page.locator('.gone'), '.c')).rejects.toThrow(/aligned: nothing of .* drawn at the measure/)
  await show(page, `${GOES}<button class="gone" style="width:44px;height:44px">A</button>`)
  await expect(tapTarget(page.locator('.gone'))).rejects.toThrow(/tapTarget: nothing of .* drawn at the measure/)
  await show(page, '<button class="b" style="width:44px;height:44px">A</button><button class="b" style="width:20px;height:20px;visibility:hidden">B</button>')
  expect((await tapTarget(page.locator('.b'))).skipped).toBe(1)
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
  await expect(measured).rejects.toThrow(/`iiiiiiiiiiii` is \d+ px wide in 90 px/)
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

test('atWidths runs at each size it names, after a reload, once what it waits for is drawn', async ({ page }) => {
  await served(page, '<main id="root">the page</main>')
  await page.evaluate(() => ((window as unknown as { loaded?: string }).loaded = 'before'))
  const seen: Array<[number, number, string | undefined]> = []
  await atWidths(
    page,
    [
      { width: 390, height: 844 },
      { width: 1280, height: 720 },
    ],
    async () => {
      seen.push(await page.evaluate(() => [window.innerWidth, window.innerHeight, (window as unknown as { loaded?: string }).loaded] as [number, number, string | undefined]))
    },
    '#root',
  )
  expect(seen).toEqual([
    [390, 844, undefined],
    [1280, 720, undefined],
  ])
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
