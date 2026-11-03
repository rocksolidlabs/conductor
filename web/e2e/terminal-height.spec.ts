import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Browser, BrowserContext, Locator, Page } from '@playwright/test'
import { expect, member, stopStrays, test, type Api, type Run, type Session } from './fixtures'

// A short pane (#189, #201): the terminal fits the rows that show in its pane, never more: its last row and the cursor's row lie
// inside the pane. In the editor column (the session page, the Yard's focus, a guest's join page), whose slot can be 8rem tall, on a
// short window and with a file open in the editor above it; and in every other pane a terminal is shown in: a tile on the wall and on
// a run's page, the Roundhouse, a crew's tiles on a control link and the paste page. Measured by geometry, whatever the renderer: the
// screen's box over the session's rows, and the cursor where xterm keeps its input box; never the text.

/** A window a few lines tall: the terminal's slot is all the column has, under 192 px. */
const SHORT = { width: 1440, height: 230 }
/** With a file open the editor takes the column's top 60 % and the terminal's slot is under 192 px. */
const EDITOR = { width: 1440, height: 480 }
/** A low window for the panes under a page's own header: the Roundhouse's and a run page's tile are under 192 px (#201). */
const LOW = { width: 1440, height: 360 }

let made = 0
const sessions: string[] = []
const runs: Array<{ run: string; crew: string }> = []

// Each test's sessions stop as it ends, so that the wall and the Roundhouse show few of them.
test.afterEach(async ({ api }) => {
  for (const id of sessions.splice(0)) await api.stopSession(id)
  for (const { run, crew } of runs.splice(0)) {
    await api.stopRun(run)
    await api.call('DELETE', `/api/crews/${encodeURIComponent(crew)}`)
  }
})

/** A folder of its own, with a file to open. */
function folder(root: string, name: string): string {
  const cwd = join(root, name)
  mkdirSync(cwd, { recursive: true })
  writeFileSync(join(cwd, 'notes.md'), '# notes\n')
  return cwd
}

/** A shell in a folder with a file to open. */
async function shell(api: Api, root: string, prefix: string): Promise<{ s: Session; cwd: string }> {
  const name = `${prefix}-${++made}`
  const cwd = folder(root, name)
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'shell', name, cwd })
  sessions.push(s.id)
  return { s, cwd }
}

/** A run of a crew of one shell, started at once: its run and the shell's session. */
async function crewShell(api: Api, root: string): Promise<{ run: Run; id: string; cwd: string }> {
  const name = `crew-${++made}`
  const cwd = folder(root, name)
  const { crew } = await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
    name,
    goal: '',
    cwd,
    where: 'server',
    isolation: 'none',
    openAfterLaunch: false,
    members: [{ name: 'sh', agentId: 'shell', prompt: '', start: { when: 'immediately' } }],
  })
  const run = await api.launchCrew(crew.id)
  runs.push({ run: run.id, crew: crew.id })
  await expect.poll(async () => member(await api.run(run.id), 'sh').sessionId ?? '', { timeout: 30_000, message: 'the shell starts' }).not.toBe('')
  return { run, id: member(await api.run(run.id), 'sh').sessionId!, cwd }
}

async function rows(api: Api, id: string): Promise<number> {
  return ((await api.session(id)) as Session & { rows: number }).rows
}

let marks = 0

/**
 * Fills the terminal (the one in `pane`, else the page's first) with output once the session's size has settled, so that the cursor
 * is on its last row, then checks the last row and the cursor's row lie inside the pane: what of the terminal shows, every box that
 * clips it and the window.
 */
async function expectRowsInPane(page: Page, api: Api, id: string, cwd: string, where: string, pane: Page | Locator = page): Promise<void> {
  const host = pane.locator('.terminal-host:has(.xterm-screen)').first()
  await expect(host, `${where}: the terminal`).toBeVisible({ timeout: 30_000 })
  await expect(pane.locator('.terminal-host.terminal-scale'), `${where}: no scaled view`).toHaveCount(0)
  let last = 0
  await expect
    .poll(
      async () => {
        const now = await rows(api, id)
        const same = now === last
        last = now
        return same
      },
      { timeout: 15_000, intervals: [700], message: `${where}: the size settles` },
    )
    .toBe(true)
  const mark = `mark-${++marks}`
  await host.locator('.xterm-helper-textarea').focus()
  await page.keyboard.type(`seq 300; touch ${mark}\n`)
  await expect.poll(() => existsSync(join(cwd, mark)), { timeout: 15_000, message: `${where}: the shell ran the line` }).toBe(true)

  const measure = async () => {
    const n = await rows(api, id)
    const g = await host.evaluate((host) => {
      // The pane: the host and every box around it that clips, and the window.
      let top = 0
      let bottom = window.innerHeight
      for (let el: Element | null = host; el; el = el.parentElement) {
        if (getComputedStyle(el).overflowY === 'visible') continue
        const r = el.getBoundingClientRect()
        top = Math.max(top, r.top)
        bottom = Math.min(bottom, r.bottom)
      }
      const screen = host.querySelector('.xterm-screen')!.getBoundingClientRect()
      const cursor = host.querySelector('.xterm-helper-textarea')!.getBoundingClientRect()
      return { pane: { top, bottom }, screen: { top: screen.top, height: screen.height }, cursorTop: cursor.top }
    })
    const cell = g.screen.height / n
    return { rows: n, cell, cursorRow: (g.cursorTop - g.screen.top) / cell, ...g }
  }
  // The output reached the screen: the cursor is on the session's last row (its distance from it, in rows, is 0).
  await expect
    .poll(
      async () => {
        const m = await measure()
        // + 0 makes a -0 (the cursor a hair past the row, rounded) the 0 it is: toBe compares with Object.is, and -0 is not 0 there.
        return Math.round((m.rows - 1 - m.cursorRow) * 100) / 100 + 0
      },
      { timeout: 15_000, message: `${where}: the cursor on the last row` },
    )
    .toBe(0)
  const m = await measure()
  const detail = `${where}: ${JSON.stringify(m)}`
  expect(m.rows, detail).toBeGreaterThan(2)
  expect(m.screen.top, `${detail}: the first row shows`).toBeGreaterThanOrEqual(m.pane.top - 0.5)
  expect(m.screen.top + m.rows * m.cell, `${detail}: the last row shows`).toBeLessThanOrEqual(m.pane.bottom + 0.5)
  expect(m.screen.top + (m.cursorRow + 1) * m.cell, `${detail}: the cursor's row shows`).toBeLessThanOrEqual(m.pane.bottom + 0.5)
}

type Where = 'the session page' | "the Yard's focus" | "a guest's join page"

/**
 * Opens the session where the test says, at a size, as the window that sizes it: the workbench's page, or a guest's on a control
 * link, which follows the size until Fit to my window (round 14).
 */
async function open(where: Where, page: Page, browser: Browser, api: Api, s: Session, size: { width: number; height: number }): Promise<Page> {
  if (where === "a guest's join page") {
    const { token } = await api.ok<{ token: string }>('POST', `/api/sessions/${s.id}/links`, { role: 'control', ttlSeconds: 3600 })
    const ctx = await browser.newContext({ viewport: size })
    contexts.push(ctx)
    await ctx.addInitScript(() => localStorage.setItem('conductor.displayName', 'Jane'))
    const jane = await ctx.newPage()
    await jane.goto(`/join/${token}`)
    await jane.getByRole('button', { name: 'Join session' }).click()
    await expect(jane.locator('.terminal-host .xterm-screen')).toBeVisible({ timeout: 30_000 })
    await jane.locator('[data-terminal-sizer] [data-fit-mine]').click({ timeout: 30_000 })
    return jane
  }
  await page.setViewportSize(size)
  await page.goto(where === 'the session page' ? `/sessions/${s.id}` : `/yard?focus=${encodeURIComponent(s.id)}`)
  await expect(page.locator('.terminal-host .xterm-screen')).toBeVisible({ timeout: 30_000 })
  return page
}

/** Opens the folder's file in the editor above the terminal, from the Files tab of the session page's inspector or the Files pane beside the column. */
async function openFile(where: Where, p: Page, cwd: string): Promise<void> {
  if (where === 'the session page') {
    await p.locator('[data-inspector] [data-rail-item="explorer"]').click()
    await p.locator(`[data-files-tree] [data-file-node="${cwd}/notes.md"]`).click()
  } else {
    await p.locator(`[data-files-aside] [data-file-node="${cwd}/notes.md"]`).click()
  }
  await expect(p.locator('[data-editor-area]')).toHaveAttribute('data-editor-area', 'open')
}

const contexts: BrowserContext[] = []
test.afterEach(async () => {
  for (const ctx of contexts.splice(0)) await ctx.close()
})

for (const where of ['the session page', "the Yard's focus", "a guest's join page"] as const) {
  test(`${where}, a short window: the terminal's last row and the cursor's row show`, async ({ page, browser, api, state }) => {
    const { s, cwd } = await shell(api, state.root, 'short')
    const p = await open(where, page, browser, api, s, SHORT)
    await expectRowsInPane(p, api, s.id, cwd, `${where}, a short window`)
  })

  test(`${where}, a file open above the terminal: its last row and the cursor's row show`, async ({ page, browser, api, state }) => {
    const { s, cwd } = await shell(api, state.root, 'editor')
    const p = await open(where, page, browser, api, s, EDITOR)
    await openFile(where, p, cwd)
    await expectRowsInPane(p, api, s.id, cwd, `${where}, a file open`)
  })
}

// Every other pane a terminal is shown in (#201): no slot holds a least height for it, and the terminal fills the pane and no more.

test("a tile on the wall, a short window: the terminal's last row and the cursor's row show", async ({ page, api, state }) => {
  const { s, cwd } = await shell(api, state.root, 'wall')
  // The wall holds this tile alone, as wide as the wall: what is measured here is the tile's height. A tile narrowed beside another
  // is the next test's (#219, #221). A session a spec before this one left running would stand beside it: ended first (#281).
  await stopStrays(api, sessions)
  await page.setViewportSize(SHORT)
  await page.goto('/yard')
  await expect(page.locator('[data-session-tile]'), 'the wall holds this tile alone').toHaveCount(1)
  await expectRowsInPane(page, api, s.id, cwd, 'a tile on the wall', page.locator(`[data-session-tile][data-session-id="${s.id}"]`))
})

/** The session's size once it holds still: the same on two reads 700 ms apart (a tile fits FIT_DEBOUNCE_MS after its pane changes). */
async function settledSize(api: Api, id: string): Promise<{ cols: number; rows: number }> {
  let last = ''
  let size = { cols: 0, rows: 0 }
  await expect
    .poll(
      async () => {
        const s = (await api.session(id)) as Session & { cols: number; rows: number }
        size = { cols: s.cols, rows: s.rows }
        const now = `${s.cols}x${s.rows}`
        const same = now === last
        last = now
        return same
      },
      { timeout: 15_000, intervals: [700], message: 'the size settles' },
    )
    .toBe(true)
  return size
}

/**
 * Sizes the window until the session's tile on the wall gives it the `target` size: each step moves the window by what the columns and
 * rows still to go take at the tile's cell, the width times the tiles `across` the wall, until the session has that size.
 */
async function sizeTileTo(page: Page, api: Api, tile: Locator, id: string, target: { cols: number; rows: number }, across: number): Promise<void> {
  const seen: Array<{ cols: number; rows: number }> = []
  for (let i = 0; i < 15; i++) {
    const now = await settledSize(api, id)
    seen.push(now)
    if (now.cols === target.cols && now.rows === target.rows) return
    const box = await tile.locator('.xterm-screen').evaluate((el) => {
      const r = el.getBoundingClientRect()
      return { width: r.width, height: r.height }
    })
    const vp = page.viewportSize()!
    await page.setViewportSize({
      width: Math.round(vp.width + (target.cols - now.cols) * (box.width / now.cols) * across),
      height: Math.round(vp.height + (target.rows - now.rows) * (box.height / now.rows)),
    })
  }
  throw new Error(`the tile did not size its session to ${target.cols}x${target.rows}: ${JSON.stringify(seen.slice(-5))}`)
}

// #221: a wall tile narrowed beside a second tile while its cursor is above its last row and longer lines are above it, through the
// sizes CI's tile had (42 × 6, then 36 × 5). With no scrollback, xterm.js 6.0's reflow moved the screen one row past the end of its
// buffer: the tile drew its rows one off, the cursor's line on top, and kept its input box where it was, off the cursor's row.
test("a tile on the wall beside a second, narrowed with its cursor above the last row: the cursor's row shows where the cursor is", async ({ page, api, state }) => {
  const { s, cwd } = await shell(api, state.root, 'narrowed')
  await shell(api, state.root, 'beside')
  await stopStrays(api, sessions)
  await page.setViewportSize({ width: 900, height: 230 })
  await page.goto('/yard')
  await expect(page.locator('[data-session-tile]'), 'the wall holds the two tiles').toHaveCount(2, { timeout: 30_000 })
  const tile = page.locator(`[data-session-tile][data-session-id="${s.id}"]`)
  await expect(tile.locator('.terminal-host .xterm-screen')).toBeVisible({ timeout: 30_000 })
  await sizeTileTo(page, api, tile, s.id, { cols: 42, rows: 6 }, 2)

  // A screen that has not scrolled: three lines of 38 columns (a 30-column prompt and a command) and the prompt the cursor is on,
  // two rows blank below it. At 36 columns each of the three takes two rows.
  await tile.locator('.xterm-helper-textarea').focus()
  await page.keyboard.type(`PS1='tile-221-prompt-012345678901$ '; printf '\\033[H\\033[2J'\n`)
  for (const mark of ['a1', 'a2', 'a3']) {
    await page.keyboard.type(`touch ${mark}\n`)
    await expect.poll(() => existsSync(join(cwd, mark)), { timeout: 15_000, message: `the shell ran touch ${mark}` }).toBe(true)
  }
  // Where xterm keeps its input box, the cursor's row as it draws it: measured on the screen, never read from its text.
  const cursorRow = async () =>
    tile.locator('.terminal-host').evaluate((host) => {
      const screen = host.querySelector('.xterm-screen')!.getBoundingClientRect()
      const box = host.querySelector('.xterm-helper-textarea')!.getBoundingClientRect()
      return Math.round(((box.top - screen.top) / (screen.height / 6)) * 100) / 100
    })
  await expect.poll(cursorRow, { timeout: 15_000, message: 'the cursor on the fourth row of six' }).toBe(3)

  await sizeTileTo(page, api, tile, s.id, { cols: 36, rows: 5 }, 2)
  await expectRowsInPane(page, api, s.id, cwd, 'a tile on the wall, narrowed', tile)
})

test("the Roundhouse, a low window: the terminal's last row and the cursor's row show", async ({ page, api, state }) => {
  const { s, cwd } = await shell(api, state.root, 'roundhouse')
  // Held on the session: no rotation and no following another session's prompt.
  await page.addInitScript(() => localStorage.setItem('conductor.carousel.settings', JSON.stringify({ intervalMs: 60000, autoplay: false, follow: false })))
  await page.setViewportSize(LOW)
  await page.goto('/roundhouse')
  await page.locator('[data-carousel-strip] [data-strip-chip]').filter({ has: page.getByText(s.name, { exact: true }) }).click()
  const slide = page.locator('[aria-roledescription="slide"]').filter({ has: page.getByText(s.name, { exact: true }) })
  await expectRowsInPane(page, api, s.id, cwd, 'the Roundhouse', slide)
})

test("a tile on a run's page, a low window: the terminal's last row and the cursor's row show", async ({ page, api, state }) => {
  const { run, id, cwd } = await crewShell(api, state.root)
  await page.setViewportSize(LOW)
  await page.goto(`/runs/${encodeURIComponent(run.id)}`)
  await expectRowsInPane(page, api, id, cwd, "a tile on a run's page", page.locator(`[data-run-grid] [data-session-tile][data-session-id="${id}"]`))
})

// A guest's tile follows the session's size and shows it scaled to the tile. Its row is 18rem tall, so its pane (about 228 px) was
// never under the old least height: here so that the terminal stays inside it.
test("a crew's tile on a control link: the terminal's last row and the cursor's row show", async ({ browser, api, state }) => {
  const { run, id, cwd } = await crewShell(api, state.root)
  const { token } = await api.ok<{ token: string }>('POST', `/api/runs/${encodeURIComponent(run.id)}/links`, { role: 'control', ttlSeconds: 3600 })
  const ctx = await browser.newContext({ viewport: EDITOR })
  contexts.push(ctx)
  await ctx.addInitScript(() => localStorage.setItem('conductor.displayName', 'Jane'))
  const jane = await ctx.newPage()
  await jane.goto(`/join/${token}`)
  await jane.getByRole('button', { name: 'Join crew' }).click()
  await expectRowsInPane(jane, api, id, cwd, "a crew's tile on a control link", jane.locator('[data-join-tiles] [data-member="sh"]'))
})

test("the paste page, a short window: the terminal's last row and the cursor's row show", async ({ page, api, state }) => {
  const { s, cwd } = await shell(api, state.root, 'paste')
  await page.goto('/paste')
  await page.getByLabel('Your name').fill('paste guest')
  await page.locator('[data-paste-make]').click()
  const offer = page.locator('[data-paste-offer]')
  await expect(offer).toBeVisible({ timeout: 15_000 })
  const { answer } = await api.ok<{ answer: string }>('POST', `/api/sessions/${encodeURIComponent(s.id)}/paste`, { offer: await offer.inputValue(), role: 'control', label: 'paste guest' })
  await page.locator('[data-paste-answer]').fill(answer)
  await page.locator('[data-paste-connect]').click()
  await expect(page.locator('[data-transport-state]').first()).toHaveAttribute('data-transport-state', 'open', { timeout: 30_000 })
  await page.setViewportSize(SHORT)
  await expectRowsInPane(page, api, s.id, cwd, 'the paste page')
})
