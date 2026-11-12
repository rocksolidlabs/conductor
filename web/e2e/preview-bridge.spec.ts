import type { Frame, Page } from '@playwright/test'
import { expect, test } from './fixtures'

// The preview bridge (docs/protocol.md, Preview bridge) in a real browser: a page at the server's own origin, one of the bridge's
// workbench origins, frames a page of another origin that loads GET /preview-bridge.js from the test server. The spec serves the
// two pages itself; nothing is fetched from anywhere but the test server. In selection mode the pointer rests over a button while
// a scroll (of a container, then of the window) brings another under it: the outline is drawn over the button now under the
// pointer, and Enter answers a `selected` naming it. A scroll in a component's own tree, which the window does not hear, moves
// the outline at the next Enter, which answers nothing; the Enter after it picks. Waits are on the bridge's answers, the scrolls'
// ends and what the page draws.

/** The messages' mark (docs/protocol.md, Preview bridge: `conductor: "preview/1"`) and the hello's nonce. */
const PREVIEW_MARK = 'preview/1'
const NONCE = 'n0nce-of-the-bridge-spec-1'
const PARENT = '/preview-bridge-spec/parent.html'
const CHILD = '/preview-bridge-spec/page.html'

/** An answer of the bridge's, as far as the spec reads it. */
interface Answer {
  conductor: string
  t: string
  nonce: string
  element?: { tag: string; id: string }
}

/** The workbench's side: it frames the page, keeps what the frame's window posts it, and sends with the hello's nonce. */
function parentPage(childOrigin: string): string {
  const o = JSON.stringify(childOrigin)
  return `<!doctype html><html><body style="margin:0"><script>
window.got = []
const f = document.createElement('iframe')
f.style.cssText = 'width:800px;height:600px;border:0;display:block'
window.loaded = new Promise((r) => f.addEventListener('load', r))
addEventListener('message', (e) => { if (e.source === f.contentWindow && e.origin === ${o}) got.push(e.data) })
window.send = (m) => f.contentWindow.postMessage({ conductor: ${JSON.stringify(PREVIEW_MARK)}, nonce: ${JSON.stringify(NONCE)}, ...m }, ${o})
f.src = ${JSON.stringify(childOrigin + CHILD)}
document.body.append(f)
</script></body></html>`
}

/** Ten buttons, each 100 px tall, their ids p0 to p9. */
const buttons = (p: string) => Array.from({ length: 10 }, (_, i) => `<button id="${p}${i}" style="display:block;width:200px;height:100px;margin:0">${p}${i}</button>`).join('')

/** The page's body by default: a scrolling container of ten buttons, then ten buttons in the window. */
const LISTS = `<div id="list" style="height:300px;overflow:auto">${buttons('c')}</div>${buttons('w')}<div style="height:2000px"></div>`

/**
 * A component whose own tree holds the scrolling container, the page's ten buttons slotted into it; the page counts the
 * container's scrolls that ended (a component's scroll is not heard by the window).
 */
const SLOTTED = `<div id="comp">${buttons('s')}</div><script>
const own = document.getElementById('comp').attachShadow({ mode: 'open' })
own.innerHTML = '<div style="height:300px;overflow:auto"><slot></slot></div>'
window.ownEnds = 0
own.firstElementChild.addEventListener('scrollend', () => window.ownEnds++)
</script>`

/**
 * The previewed page, its body LISTS unless given. The test's own script, before the bridge's, keeps the shadow roots made (so
 * outlined() reads the outline's label, the bridge's root being the last made: the outlined element's tag and id, or "none") and
 * counts the scrolls that ended.
 */
function childPage(bridge: string, body = LISTS): string {
  return `<!doctype html><html><head><script>
const roots = []
const attach = Element.prototype.attachShadow
Element.prototype.attachShadow = function (o) { const r = attach.call(this, o); roots.push(r); return r }
window.outlined = () => {
  const box = roots.at(-1)?.firstElementChild
  return box?.isConnected && box.style.display === 'block' ? box.firstElementChild.textContent : 'none'
}
window.scrollEnds = 0
addEventListener('scrollend', () => window.scrollEnds++, true)
</script><script src="${bridge}"></script></head><body style="margin:0">
${body}
</body></html>`
}

type Parent = Window & { got: Answer[]; send(m: Record<string, unknown>): void; loaded: Promise<unknown> }
type Child = Window & { outlined(): string; scrollEnds: number; ownEnds: number }

/** The bridge's answers of type t so far, each with the mark and the hello's nonce (the vitests hold them to the parsers). */
async function answers(page: Page, t: string): Promise<Answer[]> {
  const got = await page.evaluate(() => (window as unknown as Parent).got)
  for (const a of got) expect([a.conductor, a.nonce]).toEqual([PREVIEW_MARK, NONCE])
  return got.filter((a) => a.t === t)
}

/** Waits for the bridge's answers of type t to number n. */
async function answered(page: Page, t: string, n: number) {
  await page.waitForFunction(([type, count]) => (window as unknown as Parent).got.filter((a) => a.t === type).length >= count, [t, n] as const)
}

/** Selection mode on: a select, then a look, whose answer comes after the select is taken. */
async function select(page: Page) {
  const looked = (await answers(page, 'looked')).length
  await page.evaluate(() => (window as unknown as Parent).send({ t: 'select' }))
  await page.evaluate(() => (window as unknown as Parent).send({ t: 'look' }))
  await answered(page, 'looked', looked + 1)
}

/** The id of the element the page's own hit test finds at (x, y). */
const hitAt = (frame: Frame, x: number, y: number) => frame.evaluate(([px, py]) => document.elementFromPoint(px!, py!)!.id, [x, y])

/** The pointer rests at (x, y) over a button; a wheel scrolls what is under it; the outline then names the button now there. */
async function scrollUnderThePointer(page: Page, frame: Frame, x: number, y: number): Promise<string> {
  await page.mouse.move(x, y)
  const before = await hitAt(frame, x, y)
  await frame.waitForFunction((id) => (window as unknown as Child).outlined() === `button#${id}`, before)
  const ends = await frame.evaluate(() => (window as unknown as Child).scrollEnds)
  await page.mouse.wheel(0, 200)
  await frame.waitForFunction((n) => (window as unknown as Child).scrollEnds > n, ends)
  // The box takes no pointer over a button, so the page's own hit test names the button under the pointer.
  const now = await hitAt(frame, x, y)
  expect(now).not.toBe(before)
  await frame.waitForFunction((id) => (window as unknown as Child).outlined() === `button#${id}`, now)
  return now
}

/** The workbench's page framing the previewed page (its body given, or LISTS), the hello answered and the focus in the page. */
async function open(page: Page, baseURL: string, body?: string): Promise<Frame> {
  const childOrigin = `http://localhost:${new URL(baseURL).port}`
  await page.route(baseURL + PARENT, (route) => route.fulfill({ contentType: 'text/html', body: parentPage(childOrigin) }))
  await page.route(childOrigin + CHILD, (route) => route.fulfill({ contentType: 'text/html', body: childPage(`${baseURL}/preview-bridge.js`, body) }))
  await page.goto(PARENT)
  await page.evaluate(() => (window as unknown as Parent).loaded)
  const frame = page.frames().find((f) => f.url() === childOrigin + CHILD)!
  await page.evaluate(() => (window as unknown as Parent).send({ t: 'hello' }))
  await answered(page, 'ready', 1)
  // A click outside selection mode puts the focus in the page, so its window has the keys.
  await page.mouse.click(700, 550)
  return frame
}

test('the outline follows what is under the resting pointer after a container or the window scrolls, and Enter picks it', async ({ page, baseURL }) => {
  const frame = await open(page, baseURL!)

  await select(page)
  const inContainer = await scrollUnderThePointer(page, frame, 50, 50)
  expect(await frame.evaluate(() => document.getElementById('list')!.scrollTop)).toBeGreaterThan(0)
  await page.keyboard.press('Enter')
  await answered(page, 'selected', 1)

  await select(page)
  const inWindow = await scrollUnderThePointer(page, frame, 50, 450)
  expect(await frame.evaluate(() => window.scrollY)).toBeGreaterThan(0)
  await page.keyboard.press('Enter')
  await answered(page, 'selected', 2)
  const picked = await answers(page, 'selected')
  expect(picked.map((a) => [a.element?.tag, a.element?.id])).toEqual([
    ['button', inContainer],
    ['button', inWindow],
  ])
})

test('after a scroll in a component’s own tree, Enter outlines what is now under the pointer and picks nothing, and the next Enter picks it', async ({ page, baseURL }) => {
  const frame = await open(page, baseURL!, SLOTTED)

  await select(page)
  await page.mouse.move(50, 50)
  await frame.waitForFunction(() => (window as unknown as Child).outlined() === 'button#s0')
  const ends = await frame.evaluate(() => (window as unknown as Child).ownEnds)
  await page.mouse.wheel(0, 200)
  await frame.waitForFunction((n) => (window as unknown as Child).ownEnds > n, ends)
  const now = await hitAt(frame, 50, 50)
  expect(now).not.toBe('s0')
  // The window heard no scroll: the outline is still where the pointer drew it.
  expect(await frame.evaluate(() => (window as unknown as Child).outlined())).toBe('button#s0')

  // The press changes the outline: to the button now under the pointer (a pick would take it off).
  await page.keyboard.press('Enter')
  await frame.waitForFunction(() => (window as unknown as Child).outlined() !== 'button#s0')
  expect(await frame.evaluate(() => (window as unknown as Child).outlined())).toBe(`button#${now}`)
  // A look's answer comes after anything the press posted: the press answered nothing.
  const looked = (await answers(page, 'looked')).length
  await page.evaluate(() => (window as unknown as Parent).send({ t: 'look' }))
  await answered(page, 'looked', looked + 1)
  expect(await answers(page, 'selected')).toEqual([])

  await page.keyboard.press('Enter')
  await answered(page, 'selected', 1)
  expect((await answers(page, 'selected')).map((a) => [a.element?.tag, a.element?.id])).toEqual([['button', now]])
})
