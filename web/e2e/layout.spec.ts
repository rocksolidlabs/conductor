import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { aligned, ageBy, atWidths, inside, lines, noOverlap, noSideways, probeColour, rowsInView, tabTo, tapTarget, topAtMost, uncut } from './layout'

// Each layout helper (layout.ts) on pages made for it, no server: it passes a good page and fails a bad one with its measure in the
// message, and fails on a locator that matches nothing, so that a helper that measures nothing cannot pass. Each case is the proof
// of one helper.

const BASE = '<style>body{margin:0;font:16px/20px sans-serif}button{font:inherit}</style>'

/** The page holds `html` (and the base style), set as its content. */
async function show(page: Page, html: string): Promise<void> {
  await page.setContent(BASE + html)
}

/** A page served from a URL of its own (routed, no server), so that a reload or an init script reaches it as a load does. */
async function served(page: Page, html: string): Promise<void> {
  await page.route('http://layout.test/**', (route) => route.fulfill({ status: 200, contentType: 'text/html', body: BASE + html }))
  await page.goto('http://layout.test/')
}

const NONE = { timeout: 500 }

test('uncut: a whole label passes; "Conductor" cut in a 40 px truncate box fails with its widths', async ({ page }) => {
  const box = (w: number) => `<div id="l" style="width:${w}px;overflow:hidden;white-space:nowrap;text-overflow:ellipsis">Conductor</div>`
  await show(page, box(200))
  const [whole] = await uncut(page.locator('#l'))
  expect(whole!.scrollWidth).toBeLessThanOrEqual(whole!.clientWidth)
  await show(page, box(40))
  await expect(uncut(page.locator('#l'))).rejects.toThrow(/`Conductor` is \d+ px wide in 40 px/)
})

test('inside: a title in a stretched flex-col button passes; in one with no items-stretch it runs out of its row and fails', async ({ page }) => {
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
})

test('noOverlap: two buttons apart pass; two buttons 4 px into each other fail with the overlap', async ({ page }) => {
  const two = (gap: number) => `<div id="h" style="position:relative;height:40px">
    <button style="position:absolute;left:0;top:0;width:100px;height:32px">Stop</button>
    <button style="position:absolute;left:${100 + gap}px;top:0;width:100px;height:32px">Open</button></div>`
  await show(page, two(8))
  expect(await noOverlap(page.locator('#h'))).toHaveLength(2)
  await show(page, two(-4))
  await expect(noOverlap(page.locator('#h'))).rejects.toThrow(/`Stop` and `Open` overlap by 4\.0 x 32\.0 px/)
})

test('aligned: a column that starts at one edge passes; a cell 3 px off fails naming its row', async ({ page }) => {
  const table = (off: number) =>
    [0, 0, off].map((o, i) => `<div class="r" style="display:flex"><span style="width:${80 + o}px">t${i}</span><span class="c">state</span></div>`).join('')
  await show(page, table(0))
  expect(new Set(await aligned(page.locator('.r'), '.c')).size).toBe(1)
  await show(page, table(3))
  await expect(aligned(page.locator('.r'), '.c')).rejects.toThrow(/row 3's \.c starts at 83\.0 px, row 1's at 80\.0 px/)
  // A row without the cell fails: every row has it, empty or not.
  await show(page, '<div class="r"><span class="c">a</span></div><div class="r"><span>b</span></div>')
  await expect(aligned(page.locator('.r'), '.c')).rejects.toThrow(/row 2 has no \.c/)
})

test('rowsInView: five 40 px rows wholly in a 200 px list; a list one row short shows four', async ({ page }) => {
  const list = (h: number) => `<ul id="s" style="margin:0;padding:0;height:${h}px;overflow:auto">${Array.from({ length: 18 }, (_, i) => `<li class="row" style="height:40px">${i}</li>`).join('')}</ul>`
  await show(page, list(200))
  expect(await rowsInView(page.locator('#s'), '.row')).toBe(5)
  await show(page, list(199))
  expect(await rowsInView(page.locator('#s'), '.row')).toBe(4)
})

test('topAtMost: an item 100 px down passes at 100; 10 px too low for 90 it fails with where it starts', async ({ page }) => {
  await show(page, '<div style="height:100px"></div><div id="t">first ticket</div>')
  expect(await topAtMost(page.locator('#t'), 100)).toBe(100)
  await expect(topAtMost(page.locator('#t'), 90)).rejects.toThrow(/starts 100\.0 px down the window, past 90 px/)
})

test('lines: two lines pass where two are allowed; three fail with the count', async ({ page }) => {
  const text = (n: number) => `<p id="q" style="width:200px;margin:0">${Array.from({ length: n }, () => '<span style="display:inline-block;width:190px">a line</span>').join(' ')}</p>`
  await show(page, text(2))
  expect(await lines(page.locator('#q'), 2)).toBe(2)
  await show(page, text(3))
  await expect(lines(page.locator('#q'), 2)).rejects.toThrow(/is 3 lines \(60\.0 px of 20\.0 px lines\), past 2/)
})

test('noSideways: a page as wide as the window passes; one 2000 px wide fails with how far it scrolls', async ({ page }) => {
  await page.setViewportSize({ width: 800, height: 600 })
  await show(page, '<div style="width:100%">fits</div>')
  expect(await noSideways(page)).toBeLessThanOrEqual(0)
  await show(page, '<div style="width:2000px">too wide</div>')
  await expect(noSideways(page)).rejects.toThrow(/the document scrolls 1200 px sideways/)
})

test('tapTarget: a 44 px button passes; a 32 px one fails with its size', async ({ page }) => {
  await show(page, '<button id="b" style="width:44px;height:44px">A</button>')
  await tapTarget(page.locator('#b'))
  await show(page, '<button id="b" style="width:120px;height:32px">Answer</button>')
  await expect(tapTarget(page.locator('#b'))).rejects.toThrow(/`Answer` is 120\.0 x 32\.0 px, under 44 x 44/)
})

test('tabTo: Tab reaches a button with a ring; one with outline none and no shadow fails', async ({ page }) => {
  const controls = (style: string) => `<input id="from"><a href="#x">a link</a><button id="go" style="${style}">Go</button>`
  await show(page, controls(''))
  expect(await tabTo(page, page.locator('#go'), 3, page.locator('#from'))).toBe(2)
  await show(page, controls('outline:none;box-shadow:none'))
  await expect(tabTo(page, page.locator('#go'), 3, page.locator('#from'))).rejects.toThrow(/draws no focus ring/)
  // Not within the presses given: said so.
  await show(page, controls(''))
  await expect(tabTo(page, page.locator('#go'), 1, page.locator('#from'))).rejects.toThrow(/was not reached in 1 presses of Tab/)
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

test("ageBy moves the page's Date.now() and new Date() ahead by its offset", async ({ page }) => {
  const TWO_HOURS = 2 * 3600_000
  await ageBy(page, TWO_HOURS)
  await served(page, '<p>clock</p>')
  const before = Date.now()
  const [now, made] = await page.evaluate(() => [Date.now(), new Date().getTime()])
  const after = Date.now()
  for (const t of [now!, made!]) {
    expect(t - TWO_HOURS).toBeGreaterThanOrEqual(before)
    expect(t - TWO_HOURS).toBeLessThanOrEqual(after)
  }
  // A date made from a value is that value.
  expect(await page.evaluate(() => new Date(0).getTime())).toBe(0)
})

test('probeColour resolves a value, a token among them, to the sRGB a drawn colour reads as', async ({ page }) => {
  await show(page, '<style>:root{--mark:oklch(0.62 0.17 145)}</style><span id="m" style="color:var(--mark)">mark</span>')
  const token = await probeColour(page, 'var(--mark)')
  expect(token).toMatch(/^rgb\(\d+, \d+, \d+\)$/)
  const drawn = await page.locator('#m').evaluate((el) => getComputedStyle(el).color)
  expect(await probeColour(page, drawn)).toBe(token)
  expect(await probeColour(page, '#ff0000')).toBe('rgb(255, 0, 0)')
  expect(await probeColour(page, 'rgb(0 0 255 / 50%)')).toBe('rgba(0, 0, 255, 0.502)')
  expect(token).not.toBe(await probeColour(page, 'oklch(0.62 0.17 30)'))
})
