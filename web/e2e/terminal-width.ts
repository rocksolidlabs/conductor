import { expect, type Page } from '@playwright/test'

/**
 * The session page's terminal grid as the page draws it (TerminalView's data-cols) with its host's width, the same on two reads an
 * interval apart: a refit the window's change brings has ended.
 */
async function settledGrid(page: Page): Promise<{ cols: number; width: number }> {
  const host = page.locator('.terminal-host').first()
  let seen = ''
  let grid = { cols: 0, width: 0 }
  await expect
    .poll(
      async () => {
        grid = await host.evaluate((h) => ({ cols: Number(h.dataset.cols ?? 0), width: h.getBoundingClientRect().width }))
        const now = `${grid.cols} ${grid.width}`
        const held = grid.cols > 0 && now === seen
        seen = now
        return held
      },
      { timeout: 15_000, intervals: [250], message: "the page's terminal grid settles" },
    )
    .toBe(true)
  return grid
}

/**
 * The session page's terminal as drawn now, read at once: its columns (data-cols), the fit passes it has made after its pane changed
 * (data-fit-passes) and a cell's width in px (xterm's screen is its columns' width).
 */
export async function drawnGrid(page: Page): Promise<{ cols: number; passes: number; cell: number }> {
  return page
    .locator('.terminal-host')
    .first()
    .evaluate((h) => {
      const cols = Number(h.dataset.cols ?? 0)
      const screen = h.querySelector('.xterm-screen')
      return { cols, passes: Number(h.dataset.fitPasses ?? 0), cell: cols && screen ? screen.getBoundingClientRect().width / cols : 0 }
    })
}

/**
 * Takes a step that makes the session page's terminal pane narrower (the inspector opened, say) and waits for the grid the page
 * draws for the pane after it, and for the session to have that size: a fit pass made after the step, with fewer columns than
 * before it. A grid read at once after the step is the one drawn before it (the fit waits for the pane to settle), and the
 * session already holds it. The page has first sized the session to the grid it draws before the step. Returns the grid.
 */
export async function narrowedBy(page: Page, sessionCols: () => Promise<number>, step: () => Promise<void>): Promise<{ cols: number; passes: number; cell: number }> {
  let before = await drawnGrid(page)
  await expect
    .poll(
      async () => {
        before = await drawnGrid(page)
        return before.cols > 0 && (await sessionCols()) === before.cols
      },
      { timeout: 15_000, message: 'the page sized the session to its grid' },
    )
    .toBe(true)
  await step()
  let now = before
  await expect
    .poll(
      async () => {
        now = await drawnGrid(page)
        return now.passes > before.passes && now.cols > 0 && now.cols < before.cols && (await sessionCols()) === now.cols
      },
      { timeout: 15_000, message: `the page drew its grid again for the narrower pane (it drew ${before.cols} columns before) and sized the session to it` },
    )
    .toBe(true)
  return now
}

/**
 * Sets the window's width (its height kept) so that the session page's terminal draws at most `cols` columns and no fewer than
 * `cols - 4`, choosing the width from what the page reports: the grid's columns and the host's width give a column's width, and the
 * window is moved by the columns too many. A step that crosses a layout's breakpoint (the sidebar, the inspector) is measured again,
 * so no layout's widths are assumed. Returns the columns the page draws.
 */
export async function terminalColumns(page: Page, cols: number): Promise<number> {
  const size = page.viewportSize()
  if (!size) throw new Error('terminalColumns: the page has no viewport')
  let width = size.width
  for (let step = 0; step < 8; step++) {
    const g = await settledGrid(page)
    if (g.cols <= cols && g.cols >= cols - 4) return g.cols
    const column = g.width / g.cols
    width = Math.round(width - (g.cols - (cols - 2)) * column)
    await page.setViewportSize({ width, height: size.height })
  }
  const g = await settledGrid(page)
  throw new Error(`terminalColumns: the terminal draws ${g.cols} columns at ${width} px, not ${cols - 4} to ${cols}`)
}
