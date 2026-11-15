import { expect, type Locator, type Page } from '@playwright/test'

// Layout asserted by measures, never by pixels (UX1's PR-1, docs/plans/ux-review.md, the chapter Tests, "Layout by measures"). Each
// helper waits for what it measures to be drawn and fails when it matched nothing (a page not yet drawn passes every "hidden"),
// measures after document.fonts.ready, keeps its slack in the constants below, and returns what it measured, so that a failure
// reads "`mac-only-…` is 143 px wide in 118 px". Each is proved on pages of its own, good and bad, in layout.spec.ts.

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

interface Box {
  what: string
  left: number
  right: number
  top: number
  bottom: number
}

/** Waits for the locator's first match to be drawn (failing, naming `what`, when nothing matches) and for the page's fonts. */
async function drawn(l: Locator, what: string, o: Wait = {}): Promise<void> {
  await expect(l.first(), `${what}: nothing matched ${l}`).toBeVisible({ timeout: o.timeout ?? MEASURE_WAIT_MS })
  await l.page().evaluate(() => document.fonts.ready.then(() => undefined))
}

/** A short name for each match in a message: its text, else its label, else its tag. */
function names(l: Locator): Promise<string[]> {
  return l.evaluateAll((els) =>
    els.map((el) => {
      const t = (el.textContent || el.getAttribute('aria-label') || el.tagName).trim().replace(/\s+/g, ' ')
      return t.length > 30 ? t.slice(0, 29) + '…' : t
    }),
  )
}

export interface Cut {
  what: string
  scrollWidth: number
  clientWidth: number
  scrollHeight: number
  clientHeight: number
}

/**
 * Every match is whole: its content's width (scrollWidth) at most its box's (clientWidth), and its height at most its box's. Give
 * the element that clips (a `truncate` label), not an inline span inside it: an element with no box of its own fails, since it
 * measures nothing. Containment alone would pass a label cut to "C…", whose box is inside its row.
 */
export async function uncut(l: Locator, o: Wait = {}): Promise<Cut[]> {
  await drawn(l, 'uncut', o)
  const what = await names(l)
  const got = (await l.evaluateAll((els) => els.map((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth, scrollHeight: el.scrollHeight, clientHeight: el.clientHeight })))).map((c, i) => ({ what: what[i]!, ...c }))
  const bad = got.flatMap((c) => {
    if (!c.clientWidth || !c.clientHeight) return [`\`${c.what}\` has no box of its own to measure (inline?): give the element that clips`]
    const out: string[] = []
    if (c.scrollWidth > c.clientWidth) out.push(`\`${c.what}\` is ${c.scrollWidth} px wide in ${c.clientWidth} px`)
    if (c.scrollHeight > c.clientHeight) out.push(`\`${c.what}\` is ${c.scrollHeight} px tall in ${c.clientHeight} px`)
    return out
  })
  expect(bad, 'uncut: every text whole').toEqual([])
  return got
}

/** The boxes of every match, named. */
async function boxes(l: Locator): Promise<Box[]> {
  const what = await names(l)
  const got = await l.evaluateAll((els) =>
    els.map((el) => {
      const r = el.getBoundingClientRect()
      return { left: r.left, right: r.right, top: r.top, bottom: r.bottom }
    }),
  )
  return got.map((b, i) => ({ what: what[i]!, ...b }))
}

/** Every match of `part` lies within the first match of `container`'s box (a title that runs out of its row across the open item). */
export async function inside(part: Locator, container: Locator, o: Wait = {}): Promise<{ parts: Box[]; container: Box }> {
  await drawn(container, 'inside: the container', o)
  await drawn(part, 'inside: the part', o)
  const c = (await boxes(container.first()))[0]!
  const parts = await boxes(part)
  const bad = parts.flatMap((p) => {
    const out: string[] = []
    if (p.left < c.left - BOX_SLACK) out.push(`\`${p.what}\` starts ${(c.left - p.left).toFixed(1)} px left of \`${c.what}\``)
    if (p.right > c.right + BOX_SLACK) out.push(`\`${p.what}\` runs ${(p.right - c.right).toFixed(1)} px past \`${c.what}\`'s right edge`)
    if (p.top < c.top - BOX_SLACK) out.push(`\`${p.what}\` starts ${(c.top - p.top).toFixed(1)} px above \`${c.what}\``)
    if (p.bottom > c.bottom + BOX_SLACK) out.push(`\`${p.what}\` runs ${(p.bottom - c.bottom).toFixed(1)} px below \`${c.what}\``)
    return out
  })
  expect(bad, 'inside: every part within its container').toEqual([])
  return { parts, container: c }
}

/** The pieces noOverlap reads when none are given: the controls, headings, badges and inline groups a header or a row is made of. */
export const PIECES = 'button, a, input, select, textarea, h1, h2, h3, [role="button"], [data-slot="badge"], .inline-flex'

/**
 * The root's visible outermost pieces (`pieces` within it, none inside another) are pairwise clear and on screen across: a count badge
 * under a run's buttons, a phone header's buttons over the name. Returns the pieces measured; fewer than two measures nothing, and fails.
 */
export async function noOverlap(root: Locator, pieces = PIECES, o: Wait = {}): Promise<Box[]> {
  await drawn(root, 'noOverlap', o)
  const { got, width } = await root.first().evaluate(
    (el, pieces) => {
      const label = (p: Element) => {
        const t = (p.textContent || p.getAttribute('aria-label') || p.tagName).trim().replace(/\s+/g, ' ')
        return t.length > 30 ? t.slice(0, 29) + '…' : t
      }
      const pick = Array.from(el.querySelectorAll(pieces)).filter((p) => {
        const r = p.getBoundingClientRect()
        const s = getComputedStyle(p)
        return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none'
      })
      const outer = pick.filter((p) => !pick.some((o) => o !== p && o.contains(p)))
      return {
        got: outer.map((p) => {
          const r = p.getBoundingClientRect()
          return { what: label(p), left: r.left, right: r.right, top: r.top, bottom: r.bottom }
        }),
        width: window.innerWidth,
      }
    },
    pieces,
  )
  expect(got.length, `noOverlap: the pieces of ${root} (${got.map((b) => b.what).join(', ')})`).toBeGreaterThan(1)
  const bad: string[] = []
  for (const [i, a] of got.entries()) {
    if (a.left < -BOX_SLACK || a.right > width + BOX_SLACK) bad.push(`\`${a.what}\` is off screen (${a.left.toFixed(1)} to ${a.right.toFixed(1)} in ${width} px)`)
    for (const b of got.slice(i + 1)) {
      const x = Math.min(a.right, b.right) - Math.max(a.left, b.left)
      const y = Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top)
      if (x > BOX_SLACK && y > BOX_SLACK) bad.push(`\`${a.what}\` and \`${b.what}\` overlap by ${x.toFixed(1)} x ${y.toFixed(1)} px`)
    }
  }
  expect(bad, 'noOverlap: every piece clear of the others').toEqual([])
  return got
}

/**
 * Every row has the cell (empty or not) and the cells' left edges agree within ALIGN_SLACK: a column that can be read down. Returns
 * the left edges, row by row.
 */
export async function aligned(rows: Locator, cell: string, o: Wait = {}): Promise<number[]> {
  await drawn(rows, 'aligned', o)
  const lefts = await rows.evaluateAll((els, cell) => els.map((r) => r.querySelector(cell)?.getBoundingClientRect().left ?? null), cell)
  const missing = lefts.flatMap((l, i) => (l === null ? [`row ${i + 1} has no ${cell}`] : []))
  expect(missing, 'aligned: every row has the cell').toEqual([])
  const xs = lefts as number[]
  const off = xs.flatMap((x, i) => (Math.abs(x - xs[0]!) > ALIGN_SLACK ? [`row ${i + 1}'s ${cell} starts at ${x.toFixed(1)} px, row 1's at ${xs[0]!.toFixed(1)} px`] : []))
  expect(off, 'aligned: the cells start at one edge').toEqual([])
  return xs
}

/** How many rows lie wholly inside the scroller's visible box (and the window): "the list shows two of eighteen". */
export async function rowsInView(scroller: Locator, row: string, o: Wait = {}): Promise<number> {
  await drawn(scroller, 'rowsInView: the scroller', o)
  await drawn(scroller.locator(row), 'rowsInView: a row', o)
  return scroller.first().evaluate(
    (el, { row, slack }) => {
      const s = el.getBoundingClientRect()
      const top = Math.max(s.top, 0)
      const bottom = Math.min(s.bottom, window.innerHeight)
      return Array.from(el.querySelectorAll(row)).filter((r) => {
        const b = r.getBoundingClientRect()
        return b.height > 0 && b.top >= top - slack && b.bottom <= bottom + slack
      }).length
    },
    { row, slack: BOX_SLACK },
  )
}

/** The first match starts at most `y` px down the window ("the first ticket starts 400 px down"). Returns where it starts. */
export async function topAtMost(l: Locator, y: number, o: Wait = {}): Promise<number> {
  await drawn(l, 'topAtMost', o)
  const top = await l.first().evaluate((el) => el.getBoundingClientRect().top)
  expect(top, `topAtMost: ${l} starts ${top.toFixed(1)} px down the window, past ${y} px`).toBeLessThanOrEqual(y + BOX_SLACK)
  return top
}

/**
 * The first match's rendered lines: its height (less its padding) over its line height, the line height read as drawn (a `normal`
 * one from a line of its own text). With `atMost`, fails past it. Returns the lines.
 */
export async function lines(l: Locator, atMost?: number, o: Wait = {}): Promise<number> {
  await drawn(l, 'lines', o)
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
  if (atMost !== undefined) expect(n, `lines: ${l} is ${n} lines (${height.toFixed(1)} px of ${line.toFixed(1)} px lines), past ${atMost}`).toBeLessThanOrEqual(atMost)
  return n
}

/** How far the document scrolls sideways, which must be 0: nothing of the page wider than the window. */
export async function noSideways(page: Page): Promise<number> {
  await page.evaluate(() => document.fonts.ready.then(() => undefined))
  const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  expect(over, `noSideways: the document scrolls ${over} px sideways`).toBeLessThanOrEqual(0)
  return over
}

/** Every match is at least TAP_MIN px each way (a touch target). Returns the boxes. */
export async function tapTarget(l: Locator, o: Wait = {}): Promise<Box[]> {
  await drawn(l, 'tapTarget', o)
  const got = await boxes(l)
  const bad = got.flatMap((b) => {
    const w = b.right - b.left
    const h = b.bottom - b.top
    return w < TAP_MIN - BOX_SLACK || h < TAP_MIN - BOX_SLACK ? [`\`${b.what}\` is ${w.toFixed(1)} x ${h.toFixed(1)} px, under ${TAP_MIN} x ${TAP_MIN}`] : []
  })
  expect(bad, 'tapTarget: every control big enough to tap').toEqual([])
  return got
}

/**
 * Tab reaches `target` within `max` presses, starting from `from` (focused first: never a terminal, which keeps Tab), and the
 * target draws a focus ring once there (a computed outline or box shadow). Returns the presses it took.
 */
export async function tabTo(page: Page, target: Locator, max: number, from: Locator, o: Wait = {}): Promise<number> {
  await expect(target.first(), `tabTo: nothing matched ${target}`).toBeAttached({ timeout: o.timeout ?? MEASURE_WAIT_MS })
  await drawn(from, 'tabTo: where it starts', o)
  await from.first().focus()
  const at = () => target.first().evaluate((el) => el === document.activeElement || el.contains(document.activeElement))
  let presses = 0
  while (!(await at())) {
    if (presses >= max) throw new Error(`tabTo: ${target} was not reached in ${max} presses of Tab from ${from}`)
    await page.keyboard.press('Tab')
    presses++
  }
  const ring = await page.evaluate(() => {
    const el = document.activeElement!
    const s = getComputedStyle(el)
    return { outline: s.outlineStyle !== 'none' && parseFloat(s.outlineWidth) > 0 ? `${s.outlineWidth} ${s.outlineStyle}` : '', shadow: s.boxShadow !== 'none' ? s.boxShadow : '' }
  })
  expect(ring.outline || ring.shadow, `tabTo: ${target} draws no focus ring (no outline, no box shadow)`).not.toBe('')
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

/** For each size: the window set, the page reloaded, its layout's root (or `ready`) drawn and the fonts loaded, then `fn` run. */
export async function atWidths(page: Page, sizes: Size[], fn: (size: Size) => Promise<void>, ready?: string): Promise<void> {
  for (const size of sizes) {
    await page.setViewportSize(size)
    await page.reload()
    await expect(page.locator(ready ?? layoutRoot(size.width)).first(), `atWidths: the page at ${size.width} x ${size.height}`).toBeVisible()
    await page.evaluate(() => document.fonts.ready.then(() => undefined))
    await fn(size)
  }
}

/**
 * Moves the page's clock `ms` ahead from its next load (an init script: Playwright 1.44.1 has no page.clock): Date.now() and a
 * `new Date()` read that much later. For the longest age a width must hold; an age is otherwise matched by a pattern.
 */
export async function ageBy(page: Page, ms: number): Promise<void> {
  await page.addInitScript((skew) => {
    const Real = Date
    const now = Real.now.bind(Real)
    class Skewed extends Real {
      constructor(...a: ConstructorParameters<DateConstructor> | []) {
        if (a.length === 0) super(now() + skew)
        else super(...(a as ConstructorParameters<DateConstructor>))
      }
      static override now(): number {
        return now() + skew
      }
    }
    ;(window as unknown as { Date: DateConstructor }).Date = Skewed as unknown as DateConstructor
  }, ms)
}

/**
 * The colour a CSS value resolves to on the page, as `rgb(r, g, b)` or `rgba(r, g, b, a)`: a probe element resolves the value (a
 * token's var() among them) and a canvas reads it back in sRGB, so that a drawn colour (pass its computed value) is compared with a
 * token's without parsing oklch.
 */
export async function probeColour(page: Page, value: string): Promise<string> {
  const got = await page.evaluate((value) => {
    const probe = document.createElement('span')
    probe.style.color = value
    document.body.appendChild(probe)
    const resolved = getComputedStyle(probe).color
    probe.remove()
    const c = document.createElement('canvas')
    c.width = c.height = 1
    const ctx = c.getContext('2d', { willReadFrequently: true })!
    ctx.clearRect(0, 0, 1, 1)
    ctx.fillStyle = resolved
    ctx.fillRect(0, 0, 1, 1)
    const [r, g, b, a] = Array.from(ctx.getImageData(0, 0, 1, 1).data)
    return { resolved, rgba: a === 255 ? `rgb(${r}, ${g}, ${b})` : `rgba(${r}, ${g}, ${b}, ${Math.round((a! / 255) * 1000) / 1000})` }
  }, value)
  expect(got.resolved, `probeColour: ${value} resolves to no colour`).not.toBe('')
  return got.rgba
}
