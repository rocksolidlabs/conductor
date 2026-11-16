import { expect, type ElementHandle, type JSHandle, type Locator, type Page } from '@playwright/test'

// Layout asserted by measures, never by pixels (UX1's PR-1, docs/plans/ux-review.md, the chapter Tests, "Layout by measures"). What
// a person can see of an element is the browser's answer, read by one function, `look`, that every helper measures with: drawn by
// the element's checkVisibility, its visible box by an IntersectionObserver, its text by a Range, what covers it by hit-testing. A
// helper that takes a locator waits until one of its matches is drawn (failing, naming it, with how many matched), then for
// document.fonts.ready, then measures the matches in one look and fails when none is drawn at that moment ("3 matched, none drawn").
// Where the browser cannot say, a helper fails, saying what to give it, or its doc comment states the limit. Its slack is one of the
// constants below, and it returns what it measured, so that a failure reads "`mac-only-…`'s text is 143.2 px wide in 118.0 px". Each
// is proved on pages of its own, good and bad, and on the app's own shapes, in layout.spec.ts.

/** How far a box may pass another's edge: sub-pixel layout rounds. */
export const BOX_SLACK = 0.5
/** How far two edges said to line up may differ. */
export const ALIGN_SLACK = 1
/** How far a text may pass its own box across: the layout's unit, 1/64 px (a third of a pixel too narrow draws an ellipsis). */
export const TEXT_SLACK = 1 / 64
/** The smallest a control may be each way under touch. */
export const TAP_MIN = 44
/** How long a helper waits for what it measures to be drawn: the suite's expect bound. */
export const MEASURE_WAIT_MS = 15_000

/** A helper's options: how long to wait for a match to be drawn (MEASURE_WAIT_MS when not given). */
export type Wait = { timeout?: number }
export type Rect = { left: number; right: number; top: number; bottom: number }
export type Box = Rect & { what: string }
/** What a helper measured, and how many matches it skipped: not drawn, or (noOverlap, noSideways) given to be left alone. */
export type Measured<T> = { got: T[]; skipped: number }

/**
 * What a person can see of one element, as `look` reads it: its box; `tag`, a name for it as a selector reads; `drawn`, the browser
 * draws it (checkVisibility: display, visibility, opacity, content-visibility, on it and up its chain) over a pixel each way; `seen`,
 * the part of its box inside every clip between it and the window and inside the window (null: none of it); `content`, its box less
 * borders and padding, fractional; `clips`, whether it clips what it holds across and down (its overflow, contain: paint); `inline`,
 * no box of its own; `text`, when asked, one box per line of each drawn text node, fractional, its line height tall about its middle,
 * not cut; `across`, how far it scrolls sideways (overflow-x auto or scroll); `over`, with `edges` and wholly in view, what is drawn
 * over its top and bottom edges ('' for nothing), null when the browser cannot tell.
 */
export type Seen = Box & { tag: string; drawn: boolean; seen: Rect | null; content: Rect; clips: [boolean, boolean]; inline: boolean; text: Rect[]; across: number; over?: [string, string] | null }

/** What `look` read: the window's width, how far the document scrolls sideways, and each element as it was asked. */
type Looked = { width: number; scrolls: number; got: Array<Seen | null> }
/** What `look` is asked: the elements (null stays null), whose text to read (`own` text nodes, or `all` it holds), whether to hit-test its edges. */
type Ask = { els: Array<Element | null>; text?: 'own' | 'all'; edges?: boolean; slack: number }

/**
 * What a person can see of each element, read in one frame. Drawn: checkVisibility with its opacity and visibility options, and a
 * box over a pixel each way. The visible box: a one-shot IntersectionObserver with the window as root (observe, take the first
 * entries, disconnect), right for overflow of any kind on either axis, contain: paint, clip-path, transforms and the containing block
 * of an absolute or fixed element; it does not see what covers the box, which the edges' hit-test does (document.elementsFromPoint
 * a pixel inside each edge at a quarter, half and three quarters across: the first drawn element met before the element, its
 * descendants or ancestors), and it cannot for an element that takes no pointer. Text: a Range's client rects over each text node,
 * one per line. Run in the page.
 */
function look({ els, text, edges, slack }: Ask): Promise<Looked> {
  const live = [...new Set(els.filter((e): e is Element => !!e?.isConnected))]
  const vis = { opacityProperty: true, visibilityProperty: true, contentVisibilityAuto: true }
  const px = (v: string) => parseFloat(v) || 0
  const rect = (r: DOMRectReadOnly) => ({ left: r.left, right: r.right, top: r.top, bottom: r.bottom })
  const tag = (e: Element) => e.tagName.toLowerCase() + (e.id ? `#${e.id}` : '') + Array.from(e.classList).slice(0, 3).map((c) => `.${c}`).join('')
  const one = (el: Element, en: IntersectionObserverEntry): Seen => {
    const s = getComputedStyle(el)
    const b = rect(en.boundingClientRect)
    const i = en.intersectionRect
    const seen = en.isIntersecting && i.width > 0 && i.height > 0 ? rect(i) : null
    const lines: Rect[] = []
    let own = ''
    const walk = document.createTreeWalker(el, NodeFilter.SHOW_TEXT)
    for (let n = text ? walk.nextNode() : null; n; n = walk.nextNode()) {
      const p = n.parentElement!
      if ((text === 'own' && p !== el) || !n.textContent!.trim() || !p.checkVisibility(vis)) continue
      own += n.textContent
      const g = document.createRange()
      g.selectNodeContents(n)
      const lh = px(getComputedStyle(p).lineHeight)
      for (const r of Array.from(g.getClientRects())) {
        const [m, h] = [(r.top + r.bottom) / 2, lh || r.height]
        if (r.width > 0) lines.push({ left: r.left, right: r.right, top: m - h / 2, bottom: m + h / 2 })
      }
    }
    let over: [string, string] | null | undefined
    const whole = seen && seen.left - b.left <= slack && b.right - seen.right <= slack && seen.top - b.top <= slack && b.bottom - seen.bottom <= slack
    if (edges && seen && whole) {
      const at = (y: number) => {
        for (const x of [0.25, 0.5, 0.75].map((f) => seen.left + f * (seen.right - seen.left))) {
          for (const h of document.elementsFromPoint(x, y)) {
            if (h === el || el.contains(h) || h.contains(el)) break
            if (h.checkVisibility(vis)) return tag(h)
          }
        }
        return ''
      }
      over = s.pointerEvents === 'none' ? null : [at(b.top + 2 * slack), at(b.bottom - 2 * slack)]
    }
    const content = { left: b.left + px(s.borderLeftWidth) + px(s.paddingLeft), right: b.right - px(s.borderRightWidth) - px(s.paddingRight), top: b.top + px(s.borderTopWidth) + px(s.paddingTop), bottom: b.bottom - px(s.borderBottomWidth) - px(s.paddingBottom) }
    const paints = /paint|strict|content/.test(s.contain)
    const words = ((text === 'own' && own) || el.textContent || el.getAttribute('aria-label') || el.tagName).trim().replace(/\s+/g, ' ')
    const [what, drawn] = [words.length > 30 ? words.slice(0, 29) + '…' : words, el.checkVisibility(vis) && b.right - b.left > 1 && b.bottom - b.top > 1]
    const across = /auto|scroll/.test(s.overflowX) ? el.scrollWidth - el.clientWidth : 0
    return { what, tag: tag(el), ...b, drawn, seen, content, clips: [paints || s.overflowX !== 'visible', paints || s.overflowY !== 'visible'], inline: s.display === 'inline', text: lines, across, over }
  }
  const read = (got: Map<Element, IntersectionObserverEntry>) => ({
    width: window.innerWidth,
    scrolls: document.documentElement.scrollWidth - document.documentElement.clientWidth,
    got: els.map((el) => (el && got.has(el) ? one(el, got.get(el)!) : null)),
  })
  return new Promise((done, fail) => {
    if (!live.length) return done(read(new Map()))
    const got = new Map<Element, IntersectionObserverEntry>()
    const o = new IntersectionObserver((es) => {
      for (const e of es) got.set(e.target, e)
      if (got.size < live.length) return
      o.disconnect()
      done(read(got))
    })
    for (const e of live) o.observe(e)
    setTimeout(() => fail(new Error('look: the browser drew no frame in 5 s')), 5000)
  })
}

/** Waits until a match of `l` is drawn, failing with how many matched, none drawn, or that nothing matched; `fonts` then waits for the fonts. */
async function ready(l: Locator, what: string, o: Wait, fonts = true): Promise<{ n: number; first: number }> {
  let got = { n: 0, first: -1 }
  await expect(async () => {
    const vis = { opacityProperty: true, visibilityProperty: true, contentVisibilityAuto: true }
    const d = await l.evaluateAll((els, vis) => els.map((e) => e.checkVisibility(vis) && e.getBoundingClientRect().width > 1 && e.getBoundingClientRect().height > 1), vis)
    got = { n: d.length, first: d.indexOf(true) }
    expect(got.first, `${what}: ${d.length ? `${d.length} matched, none drawn` : 'nothing matched'} ${l}`).toBeGreaterThanOrEqual(0)
  }).toPass({ timeout: o.timeout ?? MEASURE_WAIT_MS })
  if (fonts) await l.page().evaluate(() => document.fonts.ready.then(() => undefined))
  return got
}

/** One look (see `look`) at the elements given as handles, or as a handle to an array. */
function lookAt(page: Page, els: ElementHandle<Node>[] | JSHandle<unknown>, o: Omit<Ask, 'els' | 'slack'> = {}): Promise<Looked> {
  return page.evaluate(look as unknown as (a: unknown) => Promise<Looked>, { els, ...o, slack: BOX_SLACK })
}

/** The drawn ones of what a look read; fails, naming `what`, when none is drawn at that moment. */
function drawnOf(got: Array<Seen | null>, what: string): Seen[] {
  const d = got.filter((s): s is Seen => !!s?.drawn)
  expect(d.length, `${what}: ${got.length} matched, none drawn at the measure`).toBeGreaterThan(0)
  return d
}

/** For a helper that reads one match: which of how many, for its failure. */
const firstOf = (n: number) => (n > 1 ? ` (the first drawn of ${n} matches)` : '')

const cut = (a: Rect, b: Rect): Rect | null => {
  const r = { left: Math.max(a.left, b.left), right: Math.min(a.right, b.right), top: Math.max(a.top, b.top), bottom: Math.min(a.bottom, b.bottom) }
  return r.right - r.left > 0 && r.bottom - r.top > 0 ? r : null
}

/** The box around every rect given; null for none. */
const around = (rs: Rect[]): Rect | null =>
  rs.length ? { left: Math.min(...rs.map((r) => r.left)), right: Math.max(...rs.map((r) => r.right)), top: Math.min(...rs.map((r) => r.top)), bottom: Math.max(...rs.map((r) => r.bottom)) } : null

/** What a clip or the window takes off each side of `box`, in words; empty when it is whole. */
function sides(box: Rect, seen: Rect): string[] {
  const off = [['left', seen.left - box.left], ['right', box.right - seen.right], ['top', seen.top - box.top], ['bottom', box.bottom - seen.bottom]] as const
  return off.flatMap(([k, v]) => (v > BOX_SLACK ? [`${v.toFixed(1)} px on the ${k}`] : []))
}

/** Text that runs out across of a box that does not clip it: what cuts it further out is not seen, so the helper that finds it fails. */
function loose(s: Seen): string[] {
  const t = around(s.text)
  return t && !s.inline && !s.clips[0] && (t.left < s.left - BOX_SLACK || t.right > s.right + BOX_SLACK)
    ? [`\`${s.what}\`'s text runs from ${t.left.toFixed(1)} to ${t.right.toFixed(1)} px, out of its box (${s.left.toFixed(1)} to ${s.right.toFixed(1)}), which does not clip it: give the element that clips`]
    : []
}

/**
 * Every drawn match is whole: its box wholly in view (no clip of any kind, scroller or window cuts it: a name cut sideways by a list
 * that scrolls only up and down), and its text, measured by a Range as fractions, inside its content box (TEXT_SLACK across: a label
 * cut to an ellipsis by a third of a pixel; BOX_SLACK down). Give the element that clips (a `truncate` label): an inline element
 * fails, having no box of its own, and so does text that runs out of a box that does not clip it, since what cuts it further out is
 * not seen. A match out of view fails too (none of it is seen): scroll its list first. Waits as the module says.
 */
export async function uncut(l: Locator, o: Wait = {}): Promise<Measured<Seen>> {
  await ready(l, 'uncut', o)
  const { got } = await lookAt(l.page(), await l.elementHandles(), { text: 'all' })
  const drawn = drawnOf(got, `uncut: ${l}`)
  const bad = drawn.flatMap((c) => {
    if (c.inline) return [`\`${c.what}\` has no box of its own to measure (inline): give the element that clips`]
    const out = c.seen ? sides(c, c.seen).map((s) => `\`${c.what}\` is cut: ${s} of its box is out of view`) : [`\`${c.what}\` is not in view: none of its box is seen`]
    const t = around(c.text)
    if (!t) return out
    // Across by where the text lies (a Range is exact across); down by how tall it is (its lines sit up to 0.9 px from the layout's).
    const across = Math.max(c.content.left - t.left, t.right - c.content.right)
    const down = t.bottom - t.top - (c.content.bottom - c.content.top)
    if (across > TEXT_SLACK && c.clips[0]) out.push(`\`${c.what}\`'s text is ${(t.right - t.left).toFixed(1)} px wide in ${(c.content.right - c.content.left).toFixed(1)} px`)
    if (down > BOX_SLACK && c.clips[1]) out.push(`\`${c.what}\`'s text is ${(t.bottom - t.top).toFixed(1)} px tall in ${(c.content.bottom - c.content.top).toFixed(1)} px`)
    if ((across > TEXT_SLACK && !c.clips[0]) || (down > BOX_SLACK && !c.clips[1])) out.push(`\`${c.what}\`'s text runs out of its box, which does not clip it: give the element that clips`)
    return out
  })
  expect(bad, 'uncut: every text whole').toEqual([])
  return { got: drawn, skipped: got.length - drawn.length }
}

/**
 * Every drawn match of `part` lies within the first drawn match of `container`'s box on all four sides (a title that runs out of its
 * row across the open item). Boxes, not what of them is in view. Waits for both as the module says; slack BOX_SLACK.
 */
export async function inside(part: Locator, container: Locator, o: Wait = {}): Promise<{ parts: Seen[]; container: Seen; skipped: number }> {
  const { n } = await ready(container, 'inside: the container', o, false)
  await ready(part, 'inside: the part', o)
  const cs = await container.elementHandles()
  const { got } = await lookAt(part.page(), [...cs, ...(await part.elementHandles())])
  const c = drawnOf(got.slice(0, cs.length), `inside: the container ${container}`)[0]!
  const parts = drawnOf(got.slice(cs.length), `inside: the part ${part}`)
  const bad = parts.flatMap((p) => sides(p, c).map((s) => `\`${p.what}\` runs ${s.replace(' on the', ' past')} edge of \`${c.what}\`${firstOf(n)}`))
  expect(bad, 'inside: every part within its container').toEqual([])
  return { parts, container: c, skipped: got.length - cs.length - parts.length }
}

/** The pieces noOverlap reads besides text: the controls, headings, badges and inline groups a header or a row is made of. */
export const PIECES = 'button, a, input, select, textarea, h1, h2, h3, [role="button"], [data-slot="badge"], .inline-flex'

/**
 * Within the first drawn match of `root`, every two pieces are clear of each other, and every piece is on screen across: a count
 * badge under a run's buttons, a phone header's buttons over the name. A piece is a match of `pieces`, measured by its visible box,
 * or an element (`root` among them) with text of its own, measured by that text's line boxes, each cut to the element's visible box
 * (a wrapped title and a badge on its last line are apart; stacked labels with a tight line height touch). Two pieces are not
 * compared when one is a match of `pieces` that holds the other; an element's own text is compared with what it holds. A piece not
 * drawn or not in view (a row scrolled out of its list) is not measured; one whose box runs past the window's edge where its visible
 * box meets that edge is off screen. Text that runs out of a box that does not clip it fails, since what cuts it is not seen.
 * `ignore` names what overlaps by design (a count on a corner): it and what it holds are skipped, and counted. Waits as the module
 * says; slack BOX_SLACK each way. Fewer than two pieces measures nothing: fails. Limit: an icon with no text, and text drawn by
 * ::before or ::after, are not pieces.
 */
export async function noOverlap(root: Locator, pieces = PIECES, o: Wait & { ignore?: Locator } = {}): Promise<Measured<Box>> {
  const { n, first } = await ready(root, 'noOverlap', o)
  const page = root.page()
  const pick = await root.nth(first).evaluateHandle(
    (el, { pieces, skip }) => {
      const matched = new Set(Array.from(el.querySelectorAll(pieces)))
      const own = (p: Element) => Array.from(p.childNodes).some((t) => t.nodeType === Node.TEXT_NODE && t.textContent!.trim())
      const cands = [el, ...Array.from(el.querySelectorAll('*'))].filter((p) => (p !== el && matched.has(p)) || own(p))
      const els = cands.filter((p) => !skip.some((s) => s.contains(p)))
      return { els, box: els.map((p) => p !== el && matched.has(p)), holds: els.map((a) => els.flatMap((b, j) => (a !== b && a.contains(b) ? [j] : []))), skipped: cands.length - els.length }
    },
    { pieces, skip: o.ignore ? await o.ignore.elementHandles() : [] },
  )
  const { box, holds, skipped } = await pick.evaluate(({ box, holds, skipped }) => ({ box, holds, skipped }))
  const { width, got } = await lookAt(page, await pick.evaluateHandle((p) => p.els), { text: 'own' })
  const runs: string[] = []
  const out = got.flatMap((s, i) => {
    if (!s?.drawn || !s.seen) return []
    if (!box[i]) runs.push(...loose(s))
    const rects = box[i] ? [s.seen] : s.text.flatMap((t) => cut(t, s.seen!) ?? [])
    return rects.length ? [{ i, s, rects, ext: { what: s.what, ...(box[i] ? s : around(s.text)!) } }] : []
  })
  const of = firstOf(n)
  expect(out.length, `noOverlap: the pieces of ${root}${of} (${out.map((p) => p.s.what).join(', ')})`).toBeGreaterThan(1)
  const bad: string[] = []
  for (const [x, a] of out.entries()) {
    for (const b of out.slice(x + 1)) {
      if ((box[a.i] && holds[a.i]!.includes(b.i)) || (box[b.i] && holds[b.i]!.includes(a.i))) continue
      const most = a.rects.flatMap((p) => b.rects.map((q) => [Math.min(p.right, q.right) - Math.max(p.left, q.left), Math.min(p.bottom, q.bottom) - Math.max(p.top, q.top)] as const))
      const over = most.filter(([w, h]) => w > BOX_SLACK && h > BOX_SLACK).sort((p, q) => q[0] * q[1] - p[0] * p[1])[0]
      if (over) bad.push(`\`${a.s.what}\` and \`${b.s.what}\` overlap by ${over[0].toFixed(1)} x ${over[1].toFixed(1)} px`)
    }
  }
  const off = out.flatMap(({ s, ext }) =>
    (ext.left < -BOX_SLACK && s.seen!.left <= BOX_SLACK) || (ext.right > width + BOX_SLACK && s.seen!.right >= width - BOX_SLACK) ? [`\`${s.what}\` is off screen (${ext.left.toFixed(1)} to ${ext.right.toFixed(1)} in ${width} px)`] : [],
  )
  expect([...runs, ...off, ...bad], `noOverlap: every piece of ${root}${of} clear of the others and on screen`).toEqual([])
  return { got: out.map((p) => p.ext), skipped }
}

/**
 * Every drawn row has the cell, drawn (empty or not), and the cells' left edges agree within ALIGN_SLACK: a column that can be read
 * down. It compares the cells' boxes; with `content`, the left of each cell's first line of text instead (a column whose text sits
 * 3 px in), a cell with no text by its box. Waits as the module says. Returns the left edges, row by row, of the rows drawn.
 */
export async function aligned(rows: Locator, cell: string, o: Wait & { content?: boolean } = {}): Promise<Measured<number>> {
  await ready(rows, 'aligned', o)
  const page = rows.page()
  const rs = await rows.elementHandles()
  const els = await page.evaluateHandle(({ rs, cell }) => [...rs, ...rs.map((r) => (r as Element).querySelector(cell))], { rs, cell })
  const { got } = await lookAt(page, els, { text: o.content ? 'all' : undefined })
  const cells = got.slice(rs.length)
  const drawn = got.slice(0, rs.length).flatMap((r, i) => (r?.drawn ? [i] : []))
  expect(drawn.length, `aligned: ${rows}: ${rs.length} matched, none drawn at the measure`).toBeGreaterThan(0)
  const missing = drawn.flatMap((i, k) => (cells[i]?.drawn ? [] : [`row ${k + 1} has no ${cell} drawn`]))
  expect(missing, 'aligned: every row has the cell').toEqual([])
  const xs = drawn.map((i) => (o.content && cells[i]!.text[0] ? cells[i]!.text[0].left : cells[i]!.left))
  const off = xs.flatMap((x, k) => (Math.abs(x - xs[0]!) > ALIGN_SLACK ? [`row ${k + 1}'s ${cell} starts at ${x.toFixed(1)} px, row 1's at ${xs[0]!.toFixed(1)} px`] : []))
  expect(off, 'aligned: the cells start at one edge').toEqual([])
  return { got: xs, skipped: rs.length - drawn.length }
}

/**
 * How many drawn rows under the first drawn `scroller` are wholly in view: their visible box is their box (no clip between the
 * scroller and the row, the scroller, a clip around it or the window cuts them, on either axis), and nothing else is drawn over their
 * top or bottom edge (the phone's fixed bottom bar, a sticky header), hit-tested at three points of each: "the list shows two of
 * eighteen". A row that takes no pointer (pointer-events: none) cannot be hit-tested: it fails. Limit: what the browser draws over a
 * row but paints nothing on (a transparent overlay that takes the pointer) counts as covering it. Waits for the scroller and a row as
 * the module says; slack BOX_SLACK.
 */
export async function rowsInView(scroller: Locator, row: string, o: Wait = {}): Promise<number> {
  const { first } = await ready(scroller, 'rowsInView: the scroller', o, false)
  await ready(scroller.locator(row), 'rowsInView: a row', o)
  const rows = await scroller.nth(first).evaluateHandle((el, row) => Array.from(el.querySelectorAll(row)), row)
  const drawn = drawnOf((await lookAt(scroller.page(), rows, { edges: true })).got, `rowsInView: a row of ${scroller}`)
  const blind = drawn.flatMap((r) => (r.over === null ? [`\`${r.what}\``] : []))
  expect(blind, 'rowsInView: a row that takes no pointer (pointer-events: none) cannot be told covered or not').toEqual([])
  return drawn.filter((r) => r.over && !r.over[0] && !r.over[1]).length
}

/**
 * The first drawn match's box starts at most `y` px down the window ("the first ticket starts 400 px down"). Waits as the module
 * says; slack BOX_SLACK. Returns where it starts.
 */
export async function topAtMost(l: Locator, y: number, o: Wait = {}): Promise<number> {
  await ready(l, 'topAtMost', o)
  const { got } = await lookAt(l.page(), await l.elementHandles())
  const top = drawnOf(got, `topAtMost: ${l}`)[0]!.top
  expect(top, `topAtMost: ${l}${firstOf(got.length)} starts ${top.toFixed(1)} px down the window, past ${y} px`).toBeLessThanOrEqual(y + BOX_SLACK)
  return top
}

/**
 * The first drawn match's rendered lines: the distinct line boxes of its text (a Range's, one per line), less what its own clip
 * hides (a clamp to two lines reads two). With `atMost`, fails past it. Waits as the module says; lines that share more than
 * BOX_SLACK of their height are one. Limit: a line that an ancestor's clip hides outside the element's own box still counts. Returns
 * the lines.
 */
export async function lines(l: Locator, atMost?: number, o: Wait = {}): Promise<number> {
  await ready(l, 'lines', o)
  const { got } = await lookAt(l.page(), await l.elementHandles(), { text: 'all' })
  const s = drawnOf(got, `lines: ${l}`)[0]!
  const v = s.seen ?? { left: 0, right: 0, top: 0, bottom: 0 }
  const shown = s.text.flatMap((t) => {
    const r = { left: s.clips[0] ? Math.max(t.left, v.left) : t.left, right: s.clips[0] ? Math.min(t.right, v.right) : t.right, top: s.clips[1] ? Math.max(t.top, v.top) : t.top, bottom: s.clips[1] ? Math.min(t.bottom, v.bottom) : t.bottom }
    return r.right > r.left && r.bottom - r.top > BOX_SLACK ? [r] : []
  })
  let [n, bottom] = [0, -Infinity]
  for (const r of shown.sort((a, b) => a.top - b.top)) [n, bottom] = r.top >= bottom - BOX_SLACK ? [n + 1, r.bottom] : [n, Math.max(bottom, r.bottom)]
  if (atMost !== undefined) expect(n, `lines: ${l}${firstOf(got.length)} is ${n} lines (its text's line boxes), past ${atMost}`).toBeLessThanOrEqual(atMost)
  return n
}

/**
 * Nothing a person sees scrolls or is cut sideways, in `root` (the body when not given) and under it: not the document; not a drawn
 * element that scrolls across (overflow-x auto or scroll) and holds more than its width (a hidden one is not counted); not a drawn
 * element partly in view and cut on its left or right, by a clip or the window (the app's shell is fixed and clips, so the document
 * alone never scrolls). An element wholly out of view (a closed drawer moved off screen) is not counted. Text that runs out of a box
 * that does not clip it across fails, since what cuts it is not seen. `allow` names what scrolls sideways by design (a board, the
 * terminal): it and what it holds are skipped, and counted; an `allow` that matches nothing fails, and so does nothing left to check.
 * Waits for `root`, or for anything drawn in the body, as the module says; slack BOX_SLACK. Returns how far the document scrolls, how
 * many elements it checked and how many `allow` regions it skipped.
 */
export async function noSideways(page: Page, o: Wait & { root?: Locator; allow?: Locator } = {}): Promise<{ document: number; checked: number; skipped: number }> {
  const { first } = await ready(o.root ?? page.locator('body *'), 'noSideways', o)
  const allow = o.allow ? await o.allow.elementHandles() : []
  if (o.allow) expect(allow.length, `noSideways: allow matched nothing: ${o.allow}`).toBeGreaterThan(0)
  const pick = await (o.root ? o.root.nth(first) : page.locator('body')).evaluateHandle(
    (el, allow) => ({ els: [el, ...Array.from(el.querySelectorAll('*'))].filter((e) => !allow.some((a) => a.contains(e))), skipped: allow.filter((a) => el.contains(a)).length }),
    allow,
  )
  const skipped = await pick.evaluate((p) => p.skipped)
  const r = await lookAt(page, await pick.evaluateHandle((p) => p.els), { text: 'own' })
  const bad = r.scrolls > 0 ? [`the document scrolls ${r.scrolls} px sideways`] : []
  let checked = 0
  for (const s of r.got) {
    if (!s?.drawn || !s.seen) continue
    checked++
    if (s.across > BOX_SLACK) bad.push(`\`${s.tag}\` scrolls ${s.across} px sideways`)
    if (s.seen.left > s.left + BOX_SLACK || s.seen.right < s.right - BOX_SLACK)
      bad.push(`\`${s.tag}\` is cut sideways: ${s.seen.left.toFixed(1)} to ${s.seen.right.toFixed(1)} px seen of ${s.left.toFixed(1)} to ${s.right.toFixed(1)} px in a ${r.width} px window`)
    bad.push(...loose({ ...s, what: s.tag }))
  }
  expect(checked, `noSideways: nothing in ${o.root ?? 'the body'} drawn in view to check`).toBeGreaterThan(0)
  expect(bad.length > 12 ? [...bad.slice(0, 12), `and ${bad.length - 12} more`] : bad, 'noSideways: nothing scrolls sideways').toEqual([])
  return { document: r.scrolls, checked, skipped }
}

/** Every drawn match is at least TAP_MIN px each way (a touch target), by its box. Waits as the module says; slack BOX_SLACK. Returns the boxes. */
export async function tapTarget(l: Locator, o: Wait = {}): Promise<Measured<Seen>> {
  await ready(l, 'tapTarget', o)
  const { got } = await lookAt(l.page(), await l.elementHandles())
  const drawn = drawnOf(got, `tapTarget: ${l}`)
  const bad = drawn.flatMap((b) => {
    const [w, h] = [b.right - b.left, b.bottom - b.top]
    return w < TAP_MIN - BOX_SLACK || h < TAP_MIN - BOX_SLACK ? [`\`${b.what}\` is ${w.toFixed(1)} x ${h.toFixed(1)} px, under ${TAP_MIN} x ${TAP_MIN}`] : []
  })
  expect(bad, 'tapTarget: every control big enough to tap').toEqual([])
  return { got: drawn, skipped: got.length - drawn.length }
}

/** What Tab can reach: the controls an ancestor's ring must not hold besides the target. */
const TABBABLE = 'a[href], button, input, select, textarea, summary, [tabindex], [contenteditable]'

/** One ring layer: the element of the chain it is on, what it is without its colour, its colour, a border or not, how far it reaches out of the element's box (0 inside, -1 not known). */
type Layer = [number, string, string, boolean, number]

/**
 * The ring layers a person could see on the chain (`els`: the target, what it holds, then its ancestors from `k`): every outline,
 * border and shadow layer with a colour that is not transparent and a size, on an element the browser draws (checkVisibility with
 * opacity and visibility) and of its ::before and ::after when drawn and not at opacity 0, an ancestor's only when it holds no other
 * control Tab reaches. CSS transitions on the chain are finished first and its CSS animations read at the end of their first
 * iteration, then put back, so that two reads see one animation time. Run in the page.
 */
function rings({ els, k }: { els: Element[]; k: number }, tabbable: string): Layer[] {
  const ctx = document.createElement('canvas').getContext('2d', { willReadFrequently: true })!
  const shows = (c: string) => {
    ctx.clearRect(0, 0, 1, 1)
    ctx.fillStyle = 'rgba(0, 0, 0, 0)'
    ctx.fillStyle = c
    ctx.fillRect(0, 0, 1, 1)
    return ctx.getImageData(0, 0, 1, 1).data[3]! > 0
  }
  const chain = new Set(els)
  const held: Array<[Animation, CSSNumberish | null]> = []
  for (const a of document.getAnimations()) {
    if (!chain.has((a.effect as KeyframeEffect | null)?.target as Element)) continue
    if (a instanceof CSSTransition) a.finish()
    else if (a instanceof CSSAnimation) {
      const t = a.effect!.getComputedTiming()
      held.push([a, a.currentTime])
      a.currentTime = Number(t.delay) + Number(t.duration)
    }
  }
  const target = els[0]!
  const out: Layer[] = []
  els.forEach((el, i) => {
    if (!el.checkVisibility({ opacityProperty: true, visibilityProperty: true })) return
    const others = Array.from(el.querySelectorAll(tabbable)).filter((c) => c !== target && !target.contains(c) && (c as HTMLElement).tabIndex >= 0 && !(c as HTMLButtonElement).disabled && c.checkVisibility())
    if (i >= k && others.length) return
    for (const pseudo of [null, '::before', '::after']) {
      const s = getComputedStyle(el, pseudo)
      if (pseudo && (s.content === 'none' || s.content === 'normal' || s.display === 'none' || s.opacity === '0' || s.visibility === 'hidden')) continue
      const [at, far] = [pseudo ?? '', (v: number) => (pseudo ? -1 : Math.max(0, v))]
      const offset = parseFloat(s.outlineOffset)
      if (s.outlineStyle !== 'none' && parseFloat(s.outlineWidth) > 0 && shows(s.outlineColor)) out.push([i, `${at}outline ${s.outlineWidth} ${s.outlineStyle}`, s.outlineColor, false, far(offset >= 0 ? offset + parseFloat(s.outlineWidth) : 0)])
      for (const side of ['Top', 'Right', 'Bottom', 'Left'] as const) {
        const [w, st, c] = [s[`border${side}Width`], s[`border${side}Style`], s[`border${side}Color`]]
        if (st !== 'none' && parseFloat(w) > 0 && shows(c)) out.push([i, `${at}border-${side} ${w} ${st}`, c, true, far(0)])
      }
      // A computed box-shadow is a list of layers, each its colour first: split at the commas outside parentheses.
      let depth = 0
      const layers = ['']
      for (const ch of s.boxShadow === 'none' ? '' : s.boxShadow) {
        depth += ch === '(' ? 1 : ch === ')' ? -1 : 0
        if (ch === ',' && depth === 0) layers.push('')
        else layers[layers.length - 1] += ch
      }
      for (const layer of layers.map((l) => l.trim()).filter(Boolean)) {
        const colour = layer.match(/^([a-z-]+\([^)]*\)|#\w+|[a-z]+)/)?.[1] ?? ''
        const [x = 0, y = 0, blur = 0, spread = 0] = (layer.slice(colour.length).match(/-?[\d.]+px/g) ?? []).map(parseFloat)
        if (shows(colour) && [x, y, blur, spread].some((v) => v !== 0)) out.push([i, `${at}shadow ${layer.slice(colour.length).trim()}`, colour, false, far(/inset/.test(layer) ? 0 : Math.max(Math.abs(x), Math.abs(y)) + blur + spread)])
      }
    }
  })
  for (const [a, t] of held) a.currentTime = t
  return out
}

/**
 * Tab reaches `target` within `max` presses, starting from `from` (focused first, and failing at once when it cannot take the focus:
 * never a terminal, which keeps Tab), and the focus adds a ring a person can see: an outline, a border or a shadow layer (see
 * `rings`) on the target, inside it, on its ::before or ::after, or on an ancestor that holds no other control Tab reaches (a
 * parent's `:focus-within`, never a tile whose border lights for any control in it), that the same element did not show with the
 * focus on `from`. On something drawn: a ring under an ancestor at opacity 0 (a toolbar shown only on hover) fails. A border drawn at
 * rest that only changes colour is no ring. When every layer added lies outside its element's box (an outline, an outer shadow), a
 * probe of the ring's extent asks what of it is in view, and a ring a clip cuts away entirely fails. Limits: no contrast is measured
 * (a ring the colour of what is behind it passes); focus shown only by a background, on a sibling or inside a shadow root is not seen
 * and fails; a ring on a ::before or ::after, or on an element that clips itself, is not probed. Leaves the focus on the target.
 * Waits for `target` to be attached and `from` drawn; no slack. Returns the presses it took.
 */
export async function tabTo(page: Page, target: Locator, max: number, from: Locator, o: Wait = {}): Promise<number> {
  await expect(target.first(), `tabTo: nothing matched ${target}`).toBeAttached({ timeout: o.timeout ?? MEASURE_WAIT_MS })
  const { n } = await ready(from, 'tabTo: where it starts', o)
  await from.first().focus()
  if (!(await from.first().evaluate((el) => el === document.activeElement))) throw new Error(`tabTo: ${from}${firstOf(n)} cannot take the focus: give a focusable element, or one with tabindex="-1"`)
  const chain = await target.first().evaluateHandle((el) => {
    const up: Element[] = []
    for (let p = el.parentElement; p; p = p.parentElement) up.push(p)
    const down = Array.from(el.querySelectorAll('*'))
    return { els: [el, ...down, ...up], k: 1 + down.length }
  })
  const of = firstOf(await target.count())
  const rest = await chain.evaluate(rings, TABBABLE)
  const at = () => target.first().evaluate((el) => el === document.activeElement || el.contains(document.activeElement))
  let presses = 0
  while (!(await at())) {
    if (presses >= max) throw new Error(`tabTo: ${target}${of} was not reached in ${max} presses of Tab from ${from}`)
    await page.keyboard.press('Tab')
    presses++
  }
  const added = (await chain.evaluate(rings, TABBABLE)).filter(([i, key, colour, border]) => !rest.some((r) => r[0] === i && r[1] === key && (border || r[2] === colour)))
  expect(added.map((l) => `${l[1]} ${l[2]}`), `tabTo: ${target}${of} draws no focus ring: nothing drawn is added with the focus`).not.toEqual([])
  if (added.every((l) => l[4] > 0)) {
    // A probe on the ring's element as wide as the ring reaches: a clip cuts it as it cuts the ring. An element that clips itself would cut its own probe.
    const probes = await chain.evaluateHandle(
      ({ els }, out) =>
        out.flatMap(([i, reach]) => {
          const el = els[i] as HTMLElement
          const s = getComputedStyle(el)
          if (s.overflowX !== 'visible' || s.overflowY !== 'visible' || /paint|strict|content/.test(s.contain)) return [el, null]
          const p = el.appendChild(document.createElement('div'))
          p.dataset.layoutProbe = s.position === 'static' ? el.style.position || 'none' : ''
          if (s.position === 'static') el.style.position = 'relative'
          p.style.cssText = `position:absolute;visibility:hidden;pointer-events:none;left:${-parseFloat(s.borderLeftWidth) - reach}px;top:${-parseFloat(s.borderTopWidth) - reach}px;width:${el.offsetWidth + 2 * reach}px;height:${el.offsetHeight + 2 * reach}px`
          return [el, p]
        }),
      added.map((l) => [l[0], l[4]] as const),
    )
    const { got } = await lookAt(page, probes)
    await probes.evaluate((ps) =>
      ps.forEach((p) => {
        const was = p instanceof HTMLElement ? p.dataset.layoutProbe : undefined
        if (was) p!.parentElement!.style.position = was === 'none' ? '' : was
        if (was !== undefined) p!.remove()
      }),
    )
    // The ring shows when its probe is seen past its element's box on some side.
    const shown = added.map((_, j) => !got[2 * j + 1] || sides(got[2 * j + 1]!.seen ?? got[2 * j]!, got[2 * j]!).length > 0)
    expect(shown, `tabTo: ${target}${of}'s ring (${added.map((l) => l[1]).join(', ')}) lies outside its box and a clip cuts all of it away`).toContain(true)
  }
  return presses
}

/** A window size. */
export type Size = { width: number; height: number }

/** The layout's root at a width: the sidebar at a desktop's, the bottom bar below 1024 px. */
export function layoutRoot(width: number): string {
  return width < 1024 ? '[data-bottom-bar]' : '#dashboard-sidebar-main'
}

/**
 * For each size: the window set, the page reloaded, its layout's root (or `ready`) drawn (failing, naming the size, when it is not)
 * and the fonts loaded, then `fn` run; a failure in `fn` names its size. Limits: it stops at the first size that fails; the last size
 * stays for the rest of the test; it sets no touch (a context's `hasTouch`: a phone with touch is the page helper's).
 */
export async function atWidths(page: Page, sizes: Size[], fn: (size: Size) => Promise<void>, ready?: string, o: Wait = {}): Promise<void> {
  for (const size of sizes) {
    const at = `${size.width} x ${size.height}`
    await page.setViewportSize(size)
    await page.reload()
    await expect(page.locator(ready ?? layoutRoot(size.width)).first(), `atWidths: the page at ${at}`).toBeVisible({ timeout: o.timeout ?? MEASURE_WAIT_MS })
    await page.evaluate(() => document.fonts.ready.then(() => undefined))
    await fn(size).catch((e: Error) => Promise.reject(Object.assign(e, { message: `atWidths: at ${at}: ${e.message}` })))
  }
}

/**
 * Moves the page's clock `ms` ahead from its next load (an init script: Playwright 1.44.1 has no page.clock): Date.now(), `new
 * Date()` and `Date()` read that much later. For the longest age a width must hold; an age is otherwise matched by a pattern.
 * Limits: the skew holds for every later load of the page and cannot be undone, and a second call adds to the first; `Intl`
 * formatting with no date, a worker's clock, `performance.now()` and timers do not move.
 */
export async function ageBy(page: Page, ms: number): Promise<void> {
  await page.addInitScript((skew) => {
    const Real = Date
    const now = () => Real.now() + skew
    function Skewed(...a: unknown[]): unknown {
      if (!new.target) return new Real(now()).toString()
      return a.length ? new (Real as unknown as new (...b: unknown[]) => Date)(...a) : new Real(now())
    }
    Object.setPrototypeOf(Skewed, Real)
    Skewed.prototype = Real.prototype
    Skewed.now = now
    ;(window as unknown as { Date: unknown }).Date = Skewed
  }, ms)
}

/**
 * The colour a CSS value resolves to on the page, as `rgb(r, g, b)` or `rgba(r, g, b, a)`: a probe element resolves the value (a
 * token's var() among them) and a canvas reads it back in sRGB, so that a drawn colour (pass its computed value) is compared with a
 * token's without parsing oklch. A value that resolves to no colour of its own (an undefined token, a word that is no colour, the
 * empty string, `currentColor`) takes its parent's, which the probe sees under two parents of two colours, and fails. Waits for
 * nothing; no slack. Limits: a value that an `!important` colour rule of the page overrides on the probe cannot be resolved, and a
 * translucent colour comes back as the engine rounds it (compare it with another probeColour, not a literal).
 */
export async function probeColour(page: Page, value: string): Promise<string> {
  const got = await page.evaluate((value) => {
    const under = (parent: string) => {
      const p = document.createElement('div')
      p.style.color = parent
      const probe = p.appendChild(document.createElement('span'))
      probe.style.color = value
      document.body.appendChild(p)
      const c = getComputedStyle(probe).color
      p.remove()
      return c
    }
    const resolved = under('rgb(1, 2, 3)')
    if (resolved !== under('rgb(4, 5, 6)')) return { resolved: '', rgba: '' }
    const ctx = document.createElement('canvas').getContext('2d', { willReadFrequently: true })!
    ctx.clearRect(0, 0, 1, 1)
    ctx.fillStyle = resolved
    ctx.fillRect(0, 0, 1, 1)
    const [r, g, b, a] = Array.from(ctx.getImageData(0, 0, 1, 1).data)
    return { resolved, rgba: a === 255 ? `rgb(${r}, ${g}, ${b})` : `rgba(${r}, ${g}, ${b}, ${Math.round((a! / 255) * 1000) / 1000})` }
  }, value)
  expect(got.resolved, `probeColour: \`${value}\` resolves to no colour of its own`).not.toBe('')
  return got.rgba
}
