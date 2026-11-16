import { expect, type Locator, type Page } from '@playwright/test'

// Layout asserted by measures, never by pixels (UX1's PR-1, docs/plans/ux-review.md, the chapter Tests, "Layout by measures"). A
// helper that takes a locator waits for its first match to be drawn (failing, naming it, when nothing matches: a page not yet drawn
// passes every "hidden"), then for document.fonts.ready, then measures in one read the matches drawn at that moment (a box, not
// `visibility: hidden`), skipping and counting the others, and fails when none is drawn by then. Its slack is one of the constants
// below, and it returns what it measured, so that a failure reads "`mac-only-…` is 143 px wide in 118 px". Each doc comment says
// what its helper waits on, its slack and the limit of what it sees. Each is proved on pages of its own, good and bad, in
// layout.spec.ts.

/** How far a box may pass another's edge: sub-pixel layout rounds. */
export const BOX_SLACK = 0.5
/** How far two edges said to line up may differ. */
export const ALIGN_SLACK = 1
/** The smallest a control may be each way under touch. */
export const TAP_MIN = 44
/** How long a helper waits for what it measures to be drawn: the suite's expect bound. */
export const MEASURE_WAIT_MS = 15_000

/** A helper's options: how long to wait for its first match (MEASURE_WAIT_MS when not given). */
export interface Wait {
  timeout?: number
}

export interface Box {
  what: string
  left: number
  right: number
  top: number
  bottom: number
}

/** A drawn match as a list helper reads it: its box, its content's size and its box's, and the ancestor that cuts it, if one does. */
export interface Measure extends Box {
  scrollWidth: number
  clientWidth: number
  scrollHeight: number
  clientHeight: number
  cutBy: string
}

/** What a helper measured, and how many matches it skipped: not drawn, or (noOverlap, noSideways) given to be left alone. */
export interface Measured<T> {
  got: T[]
  skipped: number
}

/** Waits for the locator's first match to be drawn, failing, naming `what`, when nothing matches. */
async function visible(l: Locator, what: string, o: Wait): Promise<void> {
  await expect(l.first(), `${what}: nothing matched ${l}`).toBeVisible({ timeout: o.timeout ?? MEASURE_WAIT_MS })
}

function fonts(page: Page): Promise<void> {
  return page.evaluate(() => document.fonts.ready.then(() => undefined))
}

/** For a helper that reads the first match only: how many matched, for its failure. */
async function firstOf(l: Locator): Promise<string> {
  const n = await l.count()
  return n > 1 ? ` (the first of ${n} matches)` : ''
}

/**
 * One read of every match: the drawn ones measured, each with the first ancestor that clips without scrolling and that its box runs
 * past (up to the first ancestor that scrolls: what a scroller holds past its edge is scrolled, not cut). Fails when none is drawn.
 */
async function read(l: Locator, what: string): Promise<Measured<Measure>> {
  const all = await l.evaluateAll(
    (els, slack) =>
      els.map((el) => {
        const t = (el.textContent || el.getAttribute('aria-label') || el.tagName).trim().replace(/\s+/g, ' ')
        const r = el.getBoundingClientRect()
        let cutBy = ''
        for (let a = el.parentElement; a && !cutBy; a = a.parentElement) {
          const s = getComputedStyle(a)
          if (/auto|scroll/.test(s.overflowX + s.overflowY)) break
          const b = a.getBoundingClientRect()
          const [x0, y0] = [b.left + a.clientLeft, b.top + a.clientTop]
          const x = s.overflowX !== 'visible' && (r.left < x0 - slack || r.right > x0 + a.clientWidth + slack)
          const y = s.overflowY !== 'visible' && (r.top < y0 - slack || r.bottom > y0 + a.clientHeight + slack)
          if (x || y) cutBy = a.tagName.toLowerCase() + (a.id ? `#${a.id}` : a.classList[0] ? `.${a.classList[0]}` : '')
        }
        const drawn = r.width > 0 && r.height > 0 && getComputedStyle(el).visibility !== 'hidden'
        const what = t.length > 30 ? t.slice(0, 29) + '…' : t
        return { drawn, what, left: r.left, right: r.right, top: r.top, bottom: r.bottom, scrollWidth: el.scrollWidth, clientWidth: el.clientWidth, scrollHeight: el.scrollHeight, clientHeight: el.clientHeight, cutBy }
      }),
    BOX_SLACK,
  )
  const got = all.filter((m) => m.drawn).map(({ drawn: _, ...m }) => m)
  expect(got.length, `${what}: nothing of ${l} drawn at the measure (${all.length} matched)`).toBeGreaterThan(0)
  return { got, skipped: all.length - got.length }
}

/**
 * Every drawn match is whole: its content's width (scrollWidth) at most its box's (clientWidth), its height at most its box's, and
 * its box not past an ancestor that clips it (up to the first that scrolls). Give the element that clips (a `truncate` label), not
 * an inline span inside it: an element with no box of its own fails, since it measures nothing. Containment alone would pass a
 * label cut to "C…", whose box is inside its row. Waits as the module says; no slack on the sizes (whole pixels), BOX_SLACK on an
 * ancestor's box. Limit: text holding an emoji or another fallback glyph can measure a pixel taller than its line, and then fails a
 * box exactly one line tall.
 */
export async function uncut(l: Locator, o: Wait = {}): Promise<Measured<Measure>> {
  await visible(l, 'uncut', o)
  await fonts(l.page())
  const m = await read(l, 'uncut')
  const bad = m.got.flatMap((c) => {
    if (!c.clientWidth || !c.clientHeight) return [`\`${c.what}\` has no box of its own to measure (inline?): give the element that clips`]
    const out: string[] = []
    if (c.scrollWidth > c.clientWidth) out.push(`\`${c.what}\` is ${c.scrollWidth} px wide in ${c.clientWidth} px`)
    if (c.scrollHeight > c.clientHeight) out.push(`\`${c.what}\` is ${c.scrollHeight} px tall in ${c.clientHeight} px`)
    if (c.cutBy) out.push(`\`${c.what}\` runs past the box of \`${c.cutBy}\`, which clips it`)
    return out
  })
  expect(bad, 'uncut: every text whole').toEqual([])
  return m
}

/**
 * Every drawn match of `part` lies within the first match of `container`'s box on all four sides (a title that runs out of its row
 * across the open item). Waits for both as the module says; slack BOX_SLACK.
 */
export async function inside(part: Locator, container: Locator, o: Wait = {}): Promise<{ parts: Measure[]; container: Measure; skipped: number }> {
  await visible(container, 'inside: the container', o)
  await visible(part, 'inside: the part', o)
  await fonts(part.page())
  const c = (await read(container.first(), 'inside: the container')).got[0]!
  const { got: parts, skipped } = await read(part, 'inside: the part')
  const of = await firstOf(container)
  const bad = parts.flatMap((p) => {
    const out: string[] = []
    if (p.left < c.left - BOX_SLACK) out.push(`\`${p.what}\` starts ${(c.left - p.left).toFixed(1)} px left of \`${c.what}\`${of}`)
    if (p.right > c.right + BOX_SLACK) out.push(`\`${p.what}\` runs ${(p.right - c.right).toFixed(1)} px past \`${c.what}\`'s right edge${of}`)
    if (p.top < c.top - BOX_SLACK) out.push(`\`${p.what}\` starts ${(c.top - p.top).toFixed(1)} px above \`${c.what}\`${of}`)
    if (p.bottom > c.bottom + BOX_SLACK) out.push(`\`${p.what}\` runs ${(p.bottom - c.bottom).toFixed(1)} px below \`${c.what}\`${of}`)
    return out
  })
  expect(bad, 'inside: every part within its container').toEqual([])
  return { parts, container: c, skipped }
}

/** The pieces noOverlap reads besides text: the controls, headings, badges and inline groups a header or a row is made of. */
export const PIECES = 'button, a, input, select, textarea, h1, h2, h3, [role="button"], [data-slot="badge"], .inline-flex'

/**
 * Within the first match of `root`, every two drawn pieces where neither holds the other are clear of each other, and every piece is
 * on screen across: a count badge under a run's buttons, a phone header's buttons over the name. A piece is a match of `pieces`, or
 * an element with text of its own (measured by that text, cut to the element when it clips); one a pixel or less across (a screen
 * reader's text), or transparent with an ancestor (a row's actions shown on hover), is not drawn. Two pieces are compared as seen, cut by every ancestor that clips (a row scrolled out of its list is
 * not compared); off screen is judged on the whole box. `ignore` names what overlaps by design (a count on a corner): it and what it
 * holds are skipped, and counted. Waits as the module says; slack BOX_SLACK each way. Fewer than two pieces measures nothing: fails.
 */
export async function noOverlap(root: Locator, pieces = PIECES, o: Wait & { ignore?: Locator } = {}): Promise<Measured<Box>> {
  await visible(root, 'noOverlap', o)
  await fonts(root.page())
  const skip = o.ignore ? await o.ignore.elementHandles() : []
  const r = await root.first().evaluate(
    (el, { pieces, skip, slack }) => {
      const matched = new Set(Array.from(el.querySelectorAll(pieces)))
      const own = (p: Element) => Array.from(p.childNodes).filter((n) => n.nodeType === Node.TEXT_NODE && n.textContent!.trim())
      const cands = Array.from(el.querySelectorAll('*')).filter((p) => matched.has(p) || own(p).length)
      const kept = cands.filter((p) => !skip.some((s) => s.contains(p)))
      const out: Array<{ el: Element; box: { what: string; left: number; right: number; top: number; bottom: number }; seen: { left: number; right: number; top: number; bottom: number } }> = []
      for (const p of kept) {
        const s = getComputedStyle(p)
        let r: { left: number; right: number; top: number; bottom: number } = p.getBoundingClientRect()
        if (!matched.has(p)) {
          const rs = own(p).map((n) => {
            const g = document.createRange()
            g.selectNodeContents(n)
            return g.getBoundingClientRect()
          })
          const t = { left: Math.min(...rs.map((b) => b.left)), right: Math.max(...rs.map((b) => b.right)), top: Math.min(...rs.map((b) => b.top)), bottom: Math.max(...rs.map((b) => b.bottom)) }
          r = s.overflowX === 'visible' ? t : { left: Math.max(t.left, r.left), right: Math.min(t.right, r.right), top: Math.max(t.top, r.top), bottom: Math.min(t.bottom, r.bottom) }
        }
        let faded = false
        for (let a: Element | null = p; a && !faded; a = a.parentElement) faded = getComputedStyle(a).opacity === '0'
        if (r.right - r.left <= 1 || r.bottom - r.top <= 1 || s.visibility === 'hidden' || faded) continue
        const seen = { left: r.left, right: r.right, top: r.top, bottom: r.bottom }
        for (let a = p.parentElement; a; a = getComputedStyle(a).position === 'fixed' ? null : a.parentElement) {
          const c = getComputedStyle(a)
          const b = a.getBoundingClientRect()
          const [x0, y0] = [b.left + a.clientLeft, b.top + a.clientTop]
          if (c.overflowX !== 'visible') [seen.left, seen.right] = [Math.max(seen.left, x0), Math.min(seen.right, x0 + a.clientWidth)]
          if (c.overflowY !== 'visible') [seen.top, seen.bottom] = [Math.max(seen.top, y0), Math.min(seen.bottom, y0 + a.clientHeight)]
        }
        const t = (p.textContent || p.getAttribute('aria-label') || p.tagName).trim().replace(/\s+/g, ' ')
        out.push({ el: p, box: { what: t.length > 30 ? t.slice(0, 29) + '…' : t, left: r.left, right: r.right, top: r.top, bottom: r.bottom }, seen })
      }
      const bad: string[] = []
      for (const [i, a] of out.entries()) {
        for (const b of out.slice(i + 1)) {
          if (a.el.contains(b.el) || b.el.contains(a.el)) continue
          const x = Math.min(a.seen.right, b.seen.right) - Math.max(a.seen.left, b.seen.left)
          const y = Math.min(a.seen.bottom, b.seen.bottom) - Math.max(a.seen.top, b.seen.top)
          if (x > slack && y > slack) bad.push(`\`${a.box.what}\` and \`${b.box.what}\` overlap by ${x.toFixed(1)} x ${y.toFixed(1)} px`)
        }
      }
      return { got: out.map((p) => p.box), bad, skipped: cands.length - kept.length, width: window.innerWidth }
    },
    { pieces, skip, slack: BOX_SLACK },
  )
  const of = await firstOf(root)
  expect(r.got.length, `noOverlap: the pieces of ${root}${of} (${r.got.map((b) => b.what).join(', ')})`).toBeGreaterThan(1)
  const off = r.got.flatMap((a) => (a.left < -BOX_SLACK || a.right > r.width + BOX_SLACK ? [`\`${a.what}\` is off screen (${a.left.toFixed(1)} to ${a.right.toFixed(1)} in ${r.width} px)`] : []))
  expect([...off, ...r.bad], `noOverlap: every piece of ${root}${of} clear of the others and on screen`).toEqual([])
  return { got: r.got, skipped: r.skipped }
}

/**
 * Every drawn row has the cell (empty or not) and the cells' left edges agree within ALIGN_SLACK: a column that can be read down.
 * Waits as the module says. Returns the left edges, row by row, of the rows drawn.
 */
export async function aligned(rows: Locator, cell: string, o: Wait = {}): Promise<Measured<number>> {
  await visible(rows, 'aligned', o)
  await fonts(rows.page())
  const all = await rows.evaluateAll(
    (els, cell) =>
      els.map((r) => {
        const b = r.getBoundingClientRect()
        return { drawn: b.width > 0 && b.height > 0 && getComputedStyle(r).visibility !== 'hidden', left: r.querySelector(cell)?.getBoundingClientRect().left ?? null }
      }),
    cell,
  )
  const drawn = all.filter((r) => r.drawn)
  expect(drawn.length, `aligned: nothing of ${rows} drawn at the measure (${all.length} matched)`).toBeGreaterThan(0)
  const missing = drawn.flatMap((r, i) => (r.left === null ? [`row ${i + 1} has no ${cell}`] : []))
  expect(missing, 'aligned: every row has the cell').toEqual([])
  const xs = drawn.map((r) => r.left!)
  const off = xs.flatMap((x, i) => (Math.abs(x - xs[0]!) > ALIGN_SLACK ? [`row ${i + 1}'s ${cell} starts at ${x.toFixed(1)} px, row 1's at ${xs[0]!.toFixed(1)} px`] : []))
  expect(off, 'aligned: the cells start at one edge').toEqual([])
  return { got: xs, skipped: all.length - drawn.length }
}

/**
 * How many drawn rows lie wholly inside the first scroller's visible box: its client area (borders and scrollbar off), cut by every
 * ancestor that clips and by the window: "the list shows two of eighteen". Waits for the scroller and a row as the module says;
 * slack BOX_SLACK.
 */
export async function rowsInView(scroller: Locator, row: string, o: Wait = {}): Promise<number> {
  await visible(scroller, 'rowsInView: the scroller', o)
  await visible(scroller.locator(row), 'rowsInView: a row', o)
  await fonts(scroller.page())
  return scroller.first().evaluate(
    (el, { row, slack }) => {
      const box = (a: Element) => {
        const b = a.getBoundingClientRect()
        return { top: b.top + a.clientTop, bottom: b.top + a.clientTop + a.clientHeight }
      }
      let { top, bottom } = box(el)
      for (let a = el.parentElement; a; a = getComputedStyle(a).position === 'fixed' ? null : a.parentElement) {
        if (getComputedStyle(a).overflowY === 'visible') continue
        const b = box(a)
        ;[top, bottom] = [Math.max(top, b.top), Math.min(bottom, b.bottom)]
      }
      ;[top, bottom] = [Math.max(top, 0), Math.min(bottom, window.innerHeight)]
      return Array.from(el.querySelectorAll(row)).filter((r) => {
        const b = r.getBoundingClientRect()
        return b.height > 0 && getComputedStyle(r).visibility !== 'hidden' && b.top >= top - slack && b.bottom <= bottom + slack
      }).length
    },
    { row, slack: BOX_SLACK },
  )
}

/**
 * The first match starts at most `y` px down the window ("the first ticket starts 400 px down"). Waits as the module says; slack
 * BOX_SLACK. Returns where it starts.
 */
export async function topAtMost(l: Locator, y: number, o: Wait = {}): Promise<number> {
  await visible(l, 'topAtMost', o)
  await fonts(l.page())
  const top = await l.first().evaluate((el) => el.getBoundingClientRect().top)
  expect(top, `topAtMost: ${l}${await firstOf(l)} starts ${top.toFixed(1)} px down the window, past ${y} px`).toBeLessThanOrEqual(y + BOX_SLACK)
  return top
}

/**
 * The first match's rendered lines: its height (less its padding and borders) over its line height, the line height read as drawn
 * (a `normal` one from a line of its own text), rounded. With `atMost`, fails past it. Waits as the module says; the rounding is its
 * slack. Limit: a line holding an emoji or another fallback glyph can be taller than the line height, and two such lines can count as
 * three. Returns the lines.
 */
export async function lines(l: Locator, atMost?: number, o: Wait = {}): Promise<number> {
  await visible(l, 'lines', o)
  await fonts(l.page())
  const { height, line } = await l.first().evaluate((el) => {
    const s = getComputedStyle(el)
    const height = el.getBoundingClientRect().height - parseFloat(s.paddingTop) - parseFloat(s.paddingBottom) - parseFloat(s.borderTopWidth) - parseFloat(s.borderBottomWidth)
    let line = parseFloat(s.lineHeight)
    if (Number.isNaN(line)) {
      const probe = document.createElement('span')
      probe.textContent = 'x'
      probe.style.cssText = 'position:absolute;visibility:hidden;white-space:nowrap;display:block'
      el.appendChild(probe)
      line = probe.getBoundingClientRect().height
      probe.remove()
    }
    return { height, line }
  })
  const n = Math.round(height / line)
  if (atMost !== undefined) expect(n, `lines: ${l}${await firstOf(l)} is ${n} lines (${height.toFixed(1)} px of ${line.toFixed(1)} px lines), past ${atMost}`).toBeLessThanOrEqual(atMost)
  return n
}

/**
 * Nothing a person sees scrolls sideways: not the document; not an element under `root` that scrolls across (`overflow-x` auto or
 * scroll) and holds more than its width; not a drawn element under `root` (over a pixel each way) whose box runs past the window's
 * left or right edge, though an ancestor clips it (the app's shell is fixed and clips, so the document alone never scrolls). `root`
 * is the body when not given; `allow` names what scrolls sideways by design (a board, the terminal): it and what it holds are
 * skipped, and counted. Waits for `root`, or for anything drawn in the body, as the module says; slack BOX_SLACK. Returns how far
 * the document scrolls, how many elements it checked and how many `allow` regions it skipped.
 */
export async function noSideways(page: Page, o: Wait & { root?: Locator; allow?: Locator } = {}): Promise<{ document: number; checked: number; skipped: number }> {
  await visible(o.root ?? page.locator('body *:visible'), 'noSideways', o)
  await fonts(page)
  const allow = o.allow ? await o.allow.elementHandles() : []
  const r = await (o.root ?? page.locator('body')).first().evaluate(
    (el, { allow, slack }) => {
      const name = (e: Element) => e.tagName.toLowerCase() + (e.id ? `#${e.id}` : '') + Array.from(e.classList).slice(0, 3).map((c) => `.${c}`).join('')
      const bad: string[] = []
      const past = new Set<Element>()
      let checked = 0
      for (const e of Array.from(el.querySelectorAll('*'))) {
        if (allow.some((a) => a.contains(e))) continue
        checked++
        const s = getComputedStyle(e)
        if (/auto|scroll/.test(s.overflowX) && e.scrollWidth - e.clientWidth > slack) bad.push(`\`${name(e)}\` scrolls ${e.scrollWidth - e.clientWidth} px sideways`)
        const b = e.getBoundingClientRect()
        if (b.width > 1 && b.height > 1 && s.visibility !== 'hidden' && (b.left < -slack || b.right > window.innerWidth + slack)) {
          if (!past.has(e.parentElement!)) bad.push(`\`${name(e)}\` runs from ${b.left.toFixed(1)} to ${b.right.toFixed(1)} px in a ${window.innerWidth} px window`)
          past.add(e)
        }
      }
      const d = document.documentElement
      return { document: d.scrollWidth - d.clientWidth, bad, checked, skipped: allow.filter((a) => el.contains(a)).length }
    },
    { allow, slack: BOX_SLACK },
  )
  const all = [...(r.document > 0 ? [`the document scrolls ${r.document} px sideways`] : []), ...r.bad]
  expect(all.length > 12 ? [...all.slice(0, 12), `and ${all.length - 12} more`] : all, 'noSideways: nothing scrolls sideways').toEqual([])
  return { document: r.document, checked: r.checked, skipped: r.skipped }
}

/** Every drawn match is at least TAP_MIN px each way (a touch target). Waits as the module says; slack BOX_SLACK. Returns the boxes. */
export async function tapTarget(l: Locator, o: Wait = {}): Promise<Measured<Measure>> {
  await visible(l, 'tapTarget', o)
  await fonts(l.page())
  const m = await read(l, 'tapTarget')
  const bad = m.got.flatMap((b) => {
    const w = b.right - b.left
    const h = b.bottom - b.top
    return w < TAP_MIN - BOX_SLACK || h < TAP_MIN - BOX_SLACK ? [`\`${b.what}\` is ${w.toFixed(1)} x ${h.toFixed(1)} px, under ${TAP_MIN} x ${TAP_MIN}`] : []
  })
  expect(bad, 'tapTarget: every control big enough to tap').toEqual([])
  return m
}

/**
 * What a person sees of a ring on each element given: every visible outline, border and shadow layer of it and of its ::before and
 * ::after (a pseudo-element only when drawn). Visible: a colour that is not transparent, and a width (a shadow: an offset, a blur
 * or a spread). Run in the page.
 */
function rings(els: Element[]): string[][] {
  const ctx = document.createElement('canvas').getContext('2d', { willReadFrequently: true })!
  const shows = (c: string) => {
    ctx.clearRect(0, 0, 1, 1)
    ctx.fillStyle = 'rgba(0, 0, 0, 0)'
    ctx.fillStyle = c
    ctx.fillRect(0, 0, 1, 1)
    return ctx.getImageData(0, 0, 1, 1).data[3]! > 0
  }
  return els.map((el) =>
    [null, '::before', '::after'].flatMap((pseudo) => {
      const s = getComputedStyle(el, pseudo)
      if (pseudo && (s.content === 'none' || s.content === 'normal')) return []
      const at = pseudo ?? ''
      const out: string[] = []
      if (s.outlineStyle !== 'none' && parseFloat(s.outlineWidth) > 0 && shows(s.outlineColor)) out.push(`${at}outline ${s.outlineWidth} ${s.outlineStyle} ${s.outlineColor}`)
      for (const side of ['Top', 'Right', 'Bottom', 'Left'] as const) {
        const [w, st, c] = [s[`border${side}Width`], s[`border${side}Style`], s[`border${side}Color`]]
        if (st !== 'none' && parseFloat(w) > 0 && shows(c)) out.push(`${at}border-${side} ${w} ${st} ${c}`)
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
        const sizes = layer.slice(colour.length).match(/-?[\d.]+px/g) ?? []
        if (shows(colour) && sizes.some((v) => parseFloat(v) !== 0)) out.push(`${at}shadow ${layer}`)
      }
      return out
    }),
  )
}

/**
 * Tab reaches `target` within `max` presses, starting from `from` (focused first, and failing at once when it cannot take the focus:
 * never a terminal, which keeps Tab), and the focus adds a ring a person sees: an outline, a border or a shadow layer (see `rings`)
 * on the target, inside it, on its ::before or ::after, or on an ancestor (a parent's `:focus-within`), that the same element did
 * not show with the focus on `from`. Waits for `target` to be attached and `from` drawn, and for the CSS transitions running once
 * Tab reaches the target to end; no slack. Returns the presses it took.
 */
export async function tabTo(page: Page, target: Locator, max: number, from: Locator, o: Wait = {}): Promise<number> {
  await expect(target.first(), `tabTo: nothing matched ${target}`).toBeAttached({ timeout: o.timeout ?? MEASURE_WAIT_MS })
  await visible(from, 'tabTo: where it starts', o)
  await from.first().focus()
  if (!(await from.first().evaluate((el) => el === document.activeElement)))
    throw new Error(`tabTo: ${from}${await firstOf(from)} cannot take the focus: give a focusable element, or one with tabindex="-1"`)
  const chain = await target.first().evaluateHandle((el) => {
    const up: Element[] = []
    for (let p = el.parentElement; p; p = p.parentElement) up.push(p)
    return [el, ...Array.from(el.querySelectorAll('*')), ...up]
  })
  const rest = await chain.evaluate(rings)
  const at = () => target.first().evaluate((el) => el === document.activeElement || el.contains(document.activeElement))
  let presses = 0
  while (!(await at())) {
    if (presses >= max) throw new Error(`tabTo: ${target}${await firstOf(target)} was not reached in ${max} presses of Tab from ${from}`)
    await page.keyboard.press('Tab')
    presses++
  }
  await page.evaluate(() => Promise.all(document.getAnimations().filter((a) => a instanceof CSSTransition).map((a) => a.finished.catch(() => undefined))))
  const focused = await chain.evaluate(rings)
  const added = focused.flatMap((now, i) => now.filter((r) => !rest[i]!.includes(r)))
  expect(added, `tabTo: ${target}${await firstOf(target)} draws no focus ring: nothing visible is added with the focus`).not.toEqual([])
  return presses
}

/** A window size. */
export interface Size {
  width: number
  height: number
}

/** The layout's root at a width: the sidebar at a desktop's, the bottom bar below 1024 px. */
export function layoutRoot(width: number): string {
  return width < 1024 ? '[data-bottom-bar]' : '#dashboard-sidebar-main'
}

/**
 * For each size: the window set, the page reloaded, its layout's root (or `ready`) drawn (failing, naming the size, when it is not)
 * and the fonts loaded, then `fn` run. Limits: the last size stays for the rest of the test, and it sets no touch (a context's
 * `hasTouch`: a phone with touch is the page helper's).
 */
export async function atWidths(page: Page, sizes: Size[], fn: (size: Size) => Promise<void>, ready?: string, o: Wait = {}): Promise<void> {
  for (const size of sizes) {
    await page.setViewportSize(size)
    await page.reload()
    await expect(page.locator(ready ?? layoutRoot(size.width)).first(), `atWidths: the page at ${size.width} x ${size.height}`).toBeVisible({ timeout: o.timeout ?? MEASURE_WAIT_MS })
    await fonts(page)
    await fn(size)
  }
}

/**
 * Moves the page's clock `ms` ahead from its next load (an init script: Playwright 1.44.1 has no page.clock): Date.now(), `new
 * Date()` and `Date()` read that much later. For the longest age a width must hold; an age is otherwise matched by a pattern.
 * Limits: the skew holds for every later load of the page and cannot be undone, a second call adds to the first, and
 * `performance.now()` and timers do not move.
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
 * nothing; no slack.
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
