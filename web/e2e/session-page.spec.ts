import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import type { Locator, Page } from '@playwright/test'
import { atRest, dayPage, expect, test, useTheme, type Api, type Session } from './fixtures'
import { BOX_SLACK, noOverlap, noSideways, probeColour, uncut } from './layout'

// A session's page where the terminal has the room (the UX review, P11, D7, D9): the inspector closed until a person opens it, the
// line under the name agent · folder · branch with the ids in More, Share an outline button like its neighbours, the connection an
// icon while it is healthy, a reply choice filled only when the agent recommends it, a page for a session the server no longer
// holds, and on a phone a name that keeps its room. Sessions made by id on the shared server, ended after each test.

const made: string[] = []

test.afterEach(async ({ api }) => {
  // Stopped, then removed (a second DELETE removes an ended session); one already gone answers 404, which is fine.
  for (const id of made.splice(0)) {
    await api.stopSession(id)
    await expect.poll(async () => (await api.call<{ session?: Session }>('GET', `/api/sessions/${id}`)).body?.session?.status ?? 'gone', { timeout: 15_000 }).toMatch(/exited|stopped|gone/)
    await api.stopSession(id)
  }
})

async function launch(api: Api, root: string, name: string, folder = 'session-page'): Promise<Session & { cwd: string }> {
  const cwd = join(root, folder)
  mkdirSync(cwd, { recursive: true })
  const s = await api.ok<Session & { cwd: string }>('POST', '/api/sessions', { agentId: 'claude', name, cwd })
  made.push(s.id)
  return s
}

/** The page drawn and its terminal connected: the badge says open. */
async function connected(page: Page) {
  await expect(page.locator('[data-transport-state="open"]')).toHaveCount(1, { timeout: 30_000 })
}

test('the panel is closed until opened, the terminal takes its room, and the line under the name is agent and folder', async ({ browser, api, state }) => {
  const s = await launch(api, state.root, 'e2e session page')
  const page = await dayPage(browser, { server: state, path: `/sessions/${s.id}` })
  const inspector = page.locator('[data-inspector]')
  const rail = inspector.locator('[data-inspector-rail]')
  await connected(page)
  await expect(inspector).toHaveAttribute('data-inspector-open', 'false')
  await expect(rail).toBeVisible()
  await expect(rail.locator('[data-rail-item]')).toHaveCount(6)
  await expect(rail.locator('[data-rail-item][aria-pressed="true"]')).toHaveCount(0)
  await expect(inspector.locator('[data-inspector-panel]')).toHaveCount(0)
  // The terminal's column is as wide as the page's body less the rail.
  const widths = await page.evaluate(() => {
    const column = document.querySelector('[data-editor-column]')!
    const body = column.closest('[data-slot=body]')!
    const w = (e: Element) => e.getBoundingClientRect().width
    return { column: w(column), body: w(body), rail: w(document.querySelector('[data-inspector]')!) }
  })
  expect(Math.abs(widths.column - (widths.body - widths.rail)), `the column ${widths.column} px in a body of ${widths.body} px beside a rail of ${widths.rail} px`).toBeLessThanOrEqual(2)

  // The line: agent and folder (no branch: the folder is no repository), the whole path its tooltip, no id in it.
  const meta = page.locator('[data-session-meta]')
  await expect(meta).toHaveText('Claude Code · session-page')
  await expect(meta).toHaveAttribute('title', s.cwd)
  // The ids are in More.
  await page.getByRole('button', { name: 'More', exact: true }).click()
  await expect(page.getByRole('menuitem', { name: 'Copy session id' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('menu')).toHaveCount(0)

  // People opened from the rail: pressed, its head shown, and a reload keeps the panel open.
  await rail.locator('[data-rail-item="people"]').click()
  await expect(rail.locator('[data-rail-item="people"]')).toHaveAttribute('aria-pressed', 'true')
  await expect(inspector.locator('[data-inspector-head="people"]')).toContainText('People')
  await page.reload()
  await expect(page.locator('[data-inspector]')).toHaveAttribute('data-inspector-open', 'true')
  // With no view chosen, the panel opens on Explorer.
  await expect(page.locator('[data-inspector] [data-rail-item="explorer"]')).toHaveAttribute('aria-pressed', 'true')
})

for (const theme of ['dark', 'light'] as const) {
  test(`Share is an outline button like More, and a healthy connection is an icon (${theme})`, async ({ browser, api, state }) => {
    const s = await launch(api, state.root, `e2e session share ${theme}`)
    const page = await dayPage(browser, { server: state, path: `/sessions/${s.id}`, theme })
    await connected(page)
    const bg = (name: string) => page.getByRole('button', { name, exact: true }).evaluate((e) => getComputedStyle(e).backgroundColor)
    expect(await bg('Share')).toBe(await bg('More'))
    expect(await bg('Share')).not.toBe(await probeColour(page, 'var(--ui-primary)'))
    const badge = page.locator('[data-transport-state="open"]')
    await expect(badge).toBeVisible()
    expect((await badge.innerText()).trim(), 'the badge draws no words while the connection is open').toBe('')
    await expect(badge).toHaveAttribute('aria-label', /^WebSocket/)
  })
}

test('the reply bar fills a choice only when the agent recommends it, in both themes', async ({ browser, api, state }) => {
  const s = await launch(api, state.root, 'e2e session reply')
  const page = await dayPage(browser, { server: state, path: `/sessions/${s.id}` })
  await connected(page)
  const bar = page.locator('[data-quick-reply]')
  const choices = bar.getByRole('button')
  const ask = (message: string, labels: string[]) => api.ok('POST', `/api/sessions/${s.id}/attention`, { state: 'needs_input', message, options: labels.map((label, i) => ({ label, input: String(i + 1) })) })
  // The colours read in a state that holds: the pointer off the bar (no hover shade) and every transition of the choices ended (a
  // choice that turns filled fades in over its transition; read mid-way it is a shade between).
  const backgrounds = async () => {
    await page.mouse.move(0, 0)
    return choices.evaluateAll(async (els) => {
      await Promise.all(els.flatMap((e) => e.getAnimations({ subtree: true }).map((a) => a.finished.catch(() => undefined))))
      return els.map((e) => getComputedStyle(e).backgroundColor)
    })
  }

  const plain = async () => {
    await expect(bar.locator('[data-quick-reply-message]')).toHaveText('Which merge?')
    await expect(choices).toHaveCount(3)
    await expect(bar.locator('[data-recommended]')).toHaveCount(0)
    expect(new Set(await backgrounds()).size, 'every choice is drawn alike').toBe(1)
  }
  const recommended = async () => {
    await expect(bar.locator('[data-quick-reply-message]')).toHaveText('Which merge, then?')
    await expect(choices).toHaveCount(3)
    await expect(bar.locator('[data-recommended]')).toHaveCount(1)
    await expect(bar.locator('[data-recommended]')).toContainText('Squash (Recommended)')
    const [a, b, c] = await backgrounds()
    expect(b, 'the recommended choice is filled primary').toBe(await probeColour(page, 'var(--ui-primary)'))
    expect(a, 'the other choices are drawn alike').toBe(c)
    expect(a).not.toBe(b)
  }

  await ask('Which merge?', ['Merge commit', 'Squash', 'Rebase'])
  await plain()
  await ask('Which merge, then?', ['Merge commit', 'Squash (Recommended)', 'Rebase'])
  await recommended()
  // The theme switched and the page drawn again: the same choice is filled, and only it.
  await useTheme(page, 'light')
  await connected(page)
  await recommended()
  await ask('Which merge?', ['Merge commit', 'Squash', 'Rebase'])
  await plain()
})

test('a session the server no longer holds opens on its name and a way to its record', async ({ browser, api, state }) => {
  const s = await launch(api, state.root, 'e2e session gone')
  // Stopped, ended, removed: the server no longer lists it; its record says it ended.
  await api.stopSession(s.id)
  await expect.poll(async () => (await api.session(s.id)).status, { timeout: 15_000 }).toMatch(/exited|stopped/)
  await api.stopSession(s.id)
  await expect.poll(async () => (await api.call('GET', `/api/sessions/${s.id}`)).status, { timeout: 15_000 }).toBe(404)
  await expect.poll(async () => (await api.call<{ record?: { endedAt?: string } }>('GET', `/api/history/sessions/${s.id}`)).body?.record?.endedAt ?? '', { timeout: 15_000 }).not.toBe('')

  const page = await dayPage(browser, { server: state, path: `/sessions/${s.id}` })
  const goneBody = page.locator('[data-session-gone]')
  await expect(goneBody).toHaveAttribute('data-session-gone', 'record')
  await expect(goneBody).toContainText('This session ended, and this server no longer holds it.')
  await expect(page.locator('[data-session-title]')).toHaveText('e2e session gone')
  // The exited mark of the one status language, with its word.
  await expect(page.locator('[data-session-gone-head] [data-status-mark="exited"] [data-status-word]')).toHaveText('exited')
  await expect(page).toHaveTitle(/e2e session gone/)
  // No control that cannot work: no connection, no viewers, Files, Share, Chat or More.
  for (const name of ['Share', 'More', 'Files', 'Chat']) await expect(page.getByRole('button', { name, exact: true })).toHaveCount(0)
  await expect(page.locator('[data-transport-state]')).toHaveCount(0)
  await expect(page.locator('[data-inspector]')).toHaveCount(0)
  await goneBody.getByRole('link', { name: 'Open its record' }).click()
  await expect(page).toHaveURL(new RegExp(`/history/sessions/${s.id}$`))

  // The record forgotten, the page read again: nothing is kept, and History is the way on.
  await api.ok('DELETE', `/api/history/sessions/${s.id}`)
  await expect.poll(async () => (await api.call('GET', `/api/history/sessions/${s.id}`)).status, { timeout: 15_000 }).toBe(404)
  await page.goto(`/sessions/${s.id}`)
  await expect(goneBody).toHaveAttribute('data-session-gone', 'forgotten')
  await expect(goneBody).toContainText('Nothing of this session is kept.')
  await expect(goneBody.getByRole('link', { name: 'Open History' })).toHaveAttribute('href', '/history')
  await expect(page.getByRole('button', { name: 'Share', exact: true })).toHaveCount(0)
})

test("on a phone the name keeps its room: Files and Share are in More, and Share from More opens the dialog", async ({ browser, api, state }) => {
  const s = await launch(api, state.root, 'e2e phone title')
  await api.ok('POST', `/api/sessions/${s.id}/attention`, { state: 'needs_input', message: 'Ready to go on?', options: [{ label: 'Yes', input: '1' }, { label: 'No', input: '2' }] })
  const page = await dayPage(browser, { server: state, path: `/sessions/${s.id}`, width: 390, height: 844, touch: true })
  const header = page.locator('[data-session-bar]')
  const title = page.locator('[data-session-title]')
  await expect(title).toHaveText('e2e phone title')
  await expect(page.locator('[data-quick-reply]')).toBeVisible({ timeout: 30_000 })
  await uncut(title)
  expect((await title.boundingBox())!.width, 'the name has room').toBeGreaterThanOrEqual(100)
  // A name the header holds is plain text: no button, nothing to open.
  expect(await title.evaluate((e) => e.tagName), 'a whole name is plain text').toBe('SPAN')
  await expect(header.getByRole('button', { name: 'e2e phone title' })).toHaveCount(0)
  await expect(header.getByRole('button', { name: 'Share', exact: true })).toBeHidden()
  await expect(header.getByRole('button', { name: 'Files', exact: true })).toBeHidden()
  // The terminal's rows clip by design (its own grid, drawn to its width): what it holds is the terminal's, not the page's.
  await noSideways(page, { allow: page.locator('.terminal-host') })

  await header.getByRole('button', { name: 'More', exact: true }).tap()
  const menu = page.getByRole('menu')
  await expect(menu.getByRole('menuitem', { name: 'Files' })).toBeVisible()
  await expect(menu.getByRole('menuitem').first()).toHaveText('Files')
  await menu.getByRole('menuitem', { name: 'Share' }).tap()
  // A menu that has closed is not gone: the dialog is judged once the menu is.
  await expect(page.getByRole('menu')).toHaveCount(0)
  await expect(page.getByRole('dialog', { name: 'Share e2e phone title' })).toBeVisible()

  // From sm up nothing moves: Share is in the bar, and Files beside it below xl.
  await page.setViewportSize({ width: 1024, height: 768 })
  await page.keyboard.press('Escape')
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Share', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Files', exact: true })).toBeVisible()
})

/**
 * A name as long as the server takes (proto.MaxSessionName, 120): far too long for a phone's header and for a narrow window's
 * (some 880 px of text where the title has 150 to 300). At 1440 it is within a few pixels of its room (17 px over, measured
 * 2026-11-17), so whether it is cut there turns on what else the header holds: that case asserts what holds either way.
 */
const LONG_NAME = 'e2e a session named at length, so that its header cannot hold it: every word of the name a person gave it, to the end'

/**
 * The session's name in its header cut with an ellipsis, with room of its own, its full text its title, nothing of the header over
 * it. The measures are read where the answer is made, the page at rest (its fonts loaded). `room: 'may hold it'` is for a
 * width where the name is near its room: there it is whole or cut, and everything else holds the same.
 */
async function cutAndWhole(page: Page, at: string, room: 'too small' | 'may hold it' = 'too small') {
  const header = page.locator('[data-session-bar]')
  const title = header.locator('[data-session-title]')
  await expect(title, at).toHaveText(LONG_NAME)
  await expect(title, `${at}: the full name is its title`).toHaveAttribute('title', LONG_NAME)
  await page.evaluate(() => document.fonts.ready.then(() => undefined))
  const m = await title.evaluate((e) => ({ overflow: getComputedStyle(e).textOverflow, cut: e.scrollWidth > e.clientWidth, width: e.getBoundingClientRect().width }))
  expect(m.overflow, `${at}: a name past its room is cut with an ellipsis`).toBe('ellipsis')
  if (room === 'too small') expect(m.cut, `${at}: the name is cut`).toBe(true)
  expect(m.width, `${at}: the name has room`).toBeGreaterThanOrEqual(100)
  await noOverlap(header)
}

/** The page's gutter: what a popover keeps from the window's sides. */
const GUTTER = 16

/**
 * The whole of a cut name, shown in its popover: the text whole (its box holds it across and down), the popover inside the window
 * with the gutter on both sides, nothing of the page sideways, the header's height as it was.
 */
async function wholeShown(page: Page, at: string, header: Locator, height: number) {
  const whole = page.locator('[data-session-title-whole]')
  await expect(whole, `${at}: the popover shows the whole name`).toHaveText(LONG_NAME)
  // The popover scales in as it opens: it is measured at rest (in CI its text, measured 0.6 px out of a box still scaled, "ran out").
  await atRest(whole)
  const m = await whole.evaluate((e) => {
    const box = e.closest('[role="dialog"]')!.getBoundingClientRect()
    return { sw: e.scrollWidth, cw: e.clientWidth, sh: e.scrollHeight, ch: e.clientHeight, left: box.left, right: box.right, top: box.top, bottom: box.bottom, vw: innerWidth, vh: innerHeight }
  })
  expect(m.sw, `${at}: the whole name's text is as wide as its box at most`).toBeLessThanOrEqual(m.cw)
  expect(m.sh, `${at}: the whole name's text is as tall as its box at most`).toBeLessThanOrEqual(m.ch)
  expect(m.left, `${at}: the popover keeps the gutter on the left`).toBeGreaterThanOrEqual(GUTTER - BOX_SLACK)
  expect(m.right, `${at}: the popover keeps the gutter on the right`).toBeLessThanOrEqual(m.vw - GUTTER + BOX_SLACK)
  expect(m.top, `${at}: the popover is in the window`).toBeGreaterThanOrEqual(0)
  expect(m.bottom, `${at}: the popover is in the window`).toBeLessThanOrEqual(m.vh)
  await uncut(whole)
  // The terminal's rows clip by design; the page of a session the server no longer holds has none.
  const terminal = page.locator('.terminal-host')
  await noSideways(page, (await terminal.count()) ? { allow: terminal } : {})
  expect((await header.boundingBox())!.height, `${at}: the header's height is as it was`).toBe(height)
}

/**
 * On a touch screen, where a title is never shown: the cut name is a button (its name the session's name, its description what it
 * does, the heading's text still the name) that opens the whole name at a tap, and closes it at a tap outside (`outside`, a part of
 * the page that does nothing at a tap), at a second tap and at Escape; Enter and Space open it from the keyboard.
 */
async function tapShowsWhole(page: Page, at: string, outside: Locator) {
  const header = page.locator('[data-session-bar]')
  const name = header.getByRole('button', { name: LONG_NAME, exact: true })
  const whole = page.locator('[data-session-title-whole]')
  await expect(name, `${at}: the cut name is a button`).toHaveAttribute('data-session-title', '')
  await expect(name, `${at}: the button says what it does`).toHaveAccessibleDescription('Show the whole name')
  await expect(header.getByRole('heading', { level: 1 }), `${at}: the heading's text is the name`).toHaveAccessibleName(new RegExp(`^${LONG_NAME}`))
  const height = (await header.boundingBox())!.height
  await expect(whole).toHaveCount(0)

  await name.tap()
  await wholeShown(page, `${at}, tapped`, header, height)
  await outside.tap()
  await expect(whole, `${at}: a tap outside closes it`).toHaveCount(0)

  await name.tap()
  await expect(whole).toBeVisible()
  await name.tap()
  await expect(whole, `${at}: a second tap closes it`).toHaveCount(0)

  await name.focus()
  await page.keyboard.press('Enter')
  await wholeShown(page, `${at}, Enter`, header, height)
  await page.keyboard.press('Escape')
  await expect(whole, `${at}: Escape closes it`).toHaveCount(0)
  await name.focus()
  await page.keyboard.press(' ')
  await expect(whole, `${at}: Space opens it`).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(whole).toHaveCount(0)
}

test('on a phone and in a narrow window a name too long for the header is cut with an ellipsis and its full text is its title, again after the window changes, at 1440 where the header may hold it, and on the page of a session the server no longer holds', async ({ browser, api, state }) => {
  expect(LONG_NAME.length, 'the longest name the server takes').toBeLessThanOrEqual(120)
  const s = await launch(api, state.root, LONG_NAME)
  await api.ok('POST', `/api/sessions/${s.id}/attention`, { state: 'needs_input', message: 'Ready to go on?', options: [{ label: 'Yes', input: '1' }, { label: 'No', input: '2' }] })
  const page = await dayPage(browser, { server: state, path: `/sessions/${s.id}`, width: 390, height: 844, touch: true })
  await expect(page.locator('[data-quick-reply]')).toBeVisible({ timeout: 30_000 })
  await cutAndWhole(page, 'at 390 × 844')
  // Somewhere a tap does nothing, away from the popover: the agent's question in the reply bar.
  const away = page.locator('[data-quick-reply-message]')
  await tapShowsWhole(page, 'at 390 × 844', away)

  // Renamed to a name the header holds: plain text, no button, the header's height the same; renamed back, a button again.
  const bar = page.locator('[data-session-bar]')
  const title = bar.locator('[data-session-title]')
  const heights = async () => ({ bar: (await bar.boundingBox())!.height, name: (await title.boundingBox())!.height })
  const cutHeights = await heights()
  await api.ok('PATCH', `/api/sessions/${s.id}`, { name: 'e2e short' })
  await expect(title).toHaveText('e2e short')
  await expect(bar.getByRole('button', { name: 'e2e short' }), 'a whole name is plain text').toHaveCount(0)
  await uncut(title)
  expect(await title.evaluate((e) => e.tagName)).toBe('SPAN')
  expect(await heights(), 'plain or a button, the name and the header are as tall').toEqual(cutHeights)
  await api.ok('PATCH', `/api/sessions/${s.id}`, { name: LONG_NAME })
  await expect(bar.getByRole('button', { name: LONG_NAME, exact: true }), 'renamed long again: a button').toHaveCount(1)
  await cutAndWhole(page, 'renamed back, at 390 × 844')
  // The window changes and the header is drawn again: at 1024 and back at 390 the name is still cut, with its title.
  await page.setViewportSize({ width: 1024, height: 768 })
  await expect(page.getByRole('button', { name: 'Share', exact: true })).toBeVisible()
  await cutAndWhole(page, 'resized to 1024 × 768')
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(page.locator('[data-session-bar]').getByRole('button', { name: 'Share', exact: true })).toBeHidden()
  await cutAndWhole(page, 'back at 390 × 844')
  // The other theme, the page drawn again.
  await useTheme(page, 'light')
  await cutAndWhole(page, 'at 390 × 844, light')
  await tapShowsWhole(page, 'at 390 × 844, light', away)

  // At the desktop app's width with the sidebar at its default width: the name is whole or cut, its title its full text, and its
  // whole text reachable either way: plain and uncut, or a button that shows it whole.
  const wide = await dayPage(browser, { server: state, path: `/sessions/${s.id}` })
  await expect(wide.locator('#dashboard-sidebar-main')).toBeVisible()
  await cutAndWhole(wide, 'at 1440 × 900', 'may hold it')
  const wideName = wide.locator('[data-session-bar] [data-session-title]')
  // The page measures the name and then draws it as it decided: wait for the drawing to agree with the measure, then read it once.
  await expect.poll(() => wideName.evaluate((e) => (e.tagName === 'BUTTON') === e.scrollWidth > e.clientWidth), { message: 'at 1440 × 900: a button exactly when cut' }).toBe(true)
  if (await wideName.evaluate((e) => e.tagName === 'BUTTON')) {
    // The popover scales in as it opens, and a measure taken while it does reads its text out of a box still scaled (#281): the
    // page's animations run at a tenth of their pace, so that a measure not taken at rest fails every run, not only on a slow one.
    const cdp = await wide.context().newCDPSession(wide)
    await cdp.send('Animation.enable')
    await cdp.send('Animation.setPlaybackRate', { playbackRate: 0.1 })
    await wideName.click()
    await expect(wide.locator('[data-session-title-whole]')).toHaveText(LONG_NAME)
    await atRest(wide.locator('[data-session-title-whole]'))
    await uncut(wide.locator('[data-session-title-whole]'))
    await cdp.send('Animation.setPlaybackRate', { playbackRate: 1 })
    await wide.keyboard.press('Escape')
    await expect(wide.locator('[data-session-title-whole]')).toHaveCount(0)
  } else await uncut(wideName)

  // Ended and removed: the page of a session the server no longer holds has its name in the head, cut, its full text its title.
  await api.stopSession(s.id)
  await expect.poll(async () => (await api.session(s.id)).status, { timeout: 15_000 }).toMatch(/exited|stopped/)
  await api.stopSession(s.id)
  await expect.poll(async () => (await api.call('GET', `/api/sessions/${s.id}`)).status, { timeout: 15_000 }).toBe(404)
  await page.reload()
  await expect(page.locator('[data-session-gone]')).toHaveAttribute('data-session-gone', 'record')
  const awayGone = page.locator('[data-session-gone]').getByText('This session ended, and this server no longer holds it.')
  await cutAndWhole(page, 'gone, at 390 × 844, light')
  await tapShowsWhole(page, 'gone, at 390 × 844, light', awayGone)
  // The theme switched, the page drawn again.
  await useTheme(page, 'dark', '[data-session-gone]')
  await cutAndWhole(page, 'gone, at 390 × 844, dark')
  await tapShowsWhole(page, 'gone, at 390 × 844, dark', awayGone)
  await page.setViewportSize({ width: 1024, height: 768 })
  await cutAndWhole(page, 'gone, resized to 1024 × 768')
})
