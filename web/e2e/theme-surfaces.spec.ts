import type { Locator, Page } from '@playwright/test'
import type { Day } from './day'
import { budget, dayPage, expect, test } from './fixtures'
import { MEASURE_WAIT_MS, probeColour } from './layout'

// The brand's own neutrals as drawn (the UX review's PR-24): on the seeded day, in each theme, the page is the brand's ground (ink in
// dark, white in light); the sidebar sits on the canvas, a tone of its own a step off the page; a hovered row reaches the canvas's
// hover tone, apart from the canvas and from the current row; the current page's link in the sidebar's nav stands apart from the
// canvas; the terminal and the Files editor sit on the page's tone. At 390 x 844 with touch (no sidebar below lg: the bottom bar and
// the session list are the phone's) the page and the terminal are the same ground. Every colour is read through probeColour, so a
// token's oklch and a drawn colour compare as the browser draws them.

const THEMES = ['dark', 'light'] as const
const GROUND = { dark: 'rgb(24, 33, 28)', light: 'rgb(255, 255, 255)' }

/** The background colour an element is drawn with, as probeColour gives it. */
async function background(page: Page, l: Locator): Promise<string> {
  return probeColour(page, await l.evaluate((el) => getComputedStyle(el).backgroundColor))
}

/** The `::before` background, where Nuxt UI's nav draws a link's pill. */
async function pill(page: Page, l: Locator): Promise<string> {
  return probeColour(page, await l.evaluate((el) => getComputedStyle(el, '::before').backgroundColor))
}

/** The driver session's id and folder: the day's silent session, which opens a terminal and a Files pane of the day's repository. */
async function driver(day: Day) {
  const n = await day.part('states')
  return { id: n.driver!, other: n['flaky-vim-spec']!, cwd: (await day.api.session(n.driver!)).cwd ?? '' }
}

for (const theme of THEMES) {
  test(`the ${theme} theme: the page on the brand's ground, the sidebar on its canvas, the hover between, the terminal and the editor on the page`, async ({ day, browser }) => {
    const d = await driver(day)
    const page = await dayPage(browser, { path: `/sessions/${d.id}`, theme })
    const ground = GROUND[theme]
    expect(await background(page, page.locator('body'))).toBe(ground)
    expect(await probeColour(page, 'var(--ui-bg)')).toBe(ground)

    // The sidebar's root on the canvas, apart from the page.
    const canvas = await probeColour(page, 'var(--ui-bg-canvas)')
    expect(canvas).not.toBe(ground)
    const sidebar = page.locator('#dashboard-sidebar-main')
    expect(await background(page, sidebar)).toBe(canvas)

    // The current row is the page's tone; another row hovered reaches the hover tone, which is neither. The row's transition passes
    // through other colours on the way: poll for the tone that holds.
    const current = sidebar.locator(`[data-sidebar-row="s:${d.id}"]`)
    await expect(current).toBeVisible()
    expect(await background(page, current)).toBe(ground)
    const hoverTone = await probeColour(page, 'var(--ui-bg-canvas-hover)')
    expect(hoverTone).not.toBe(canvas)
    expect(hoverTone).not.toBe(ground)
    const link = sidebar.locator(`[data-sidebar-row="s:${d.other}"] a[href="/sessions/${d.other}"]`)
    await link.hover()
    await expect.poll(() => background(page, link), { timeout: MEASURE_WAIT_MS }).toBe(hoverTone)

    // The current page's link in the nav: its pill stands apart from the canvas it sits on.
    const nav = sidebar.locator('[data-slot="link"][aria-current="page"], [data-slot="link"][data-active]').first()
    await page.goto('/yard')
    await expect(page.locator('#dashboard-sidebar-main')).toBeVisible()
    await expect(nav).toBeVisible()
    expect(await pill(page, nav)).not.toBe(canvas)
    await page.goto(`/sessions/${d.id}`)

    // The terminal's host on the page's tone.
    const host = page.locator('.terminal-host').first()
    await expect(host).toBeVisible()
    expect(await background(page, host)).toBe(ground)

    // Monaco in Files on the page's tone: the day's repository's README, opened from the tree.
    budget(2 * 30_000)
    await page.locator('[data-inspector] [data-rail-item="explorer"]').click()
    const tree = page.locator('[data-files-tree]')
    await tree.locator(`[data-file-node="${d.cwd}/README.md"]`).click({ timeout: 30_000 })
    const editor = page.locator('[data-editor-area] .monaco-editor').first()
    await expect(editor.locator('.view-lines')).toContainText('e2e scratch repository', { timeout: 30_000 })
    await expect.poll(() => background(page, editor.locator('.monaco-editor-background').first()), { timeout: MEASURE_WAIT_MS }).toBe(ground)
  })

  test(`the ${theme} theme on a phone: the page and the terminal on the brand's ground`, async ({ day, browser }) => {
    const d = await driver(day)
    const page = await dayPage(browser, { path: '/sessions', theme, width: 390, height: 844, touch: true })
    const ground = GROUND[theme]
    expect(await background(page, page.locator('body'))).toBe(ground)
    await expect(page.locator('[data-session-list="page"]')).toBeVisible()
    expect(await background(page, page.locator('[data-bottom-bar]'))).toBe(ground)
    await page.goto(`/sessions/${d.id}`)
    const host = page.locator('.terminal-host').first()
    await expect(host).toBeVisible()
    expect(await background(page, host)).toBe(ground)
  })
}
