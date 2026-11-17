import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import type { Page } from '@playwright/test'
import { dayPage, expect, test, useTheme, type Api, type Session } from './fixtures'
import { noOverlap, noSideways, probeColour, uncut } from './layout'

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

/** A name as long as the server takes (proto.MaxSessionName, 120): far too long for a phone's header, and for a desktop's. */
const LONG_NAME = 'e2e a session named at length, so that its header cannot hold it: every word of the name a person gave it, to the end'

/**
 * The session's name in its header cut with an ellipsis, with room of its own, its full text its title, nothing of the header over
 * it. The measures are read where the answer is made, the page at rest (its fonts loaded).
 */
async function cutAndWhole(page: Page, at: string) {
  const header = page.locator('[data-session-bar]')
  const title = header.locator('[data-session-title]')
  await expect(title, at).toHaveText(LONG_NAME)
  await expect(title, `${at}: the full name is its title`).toHaveAttribute('title', LONG_NAME)
  await page.evaluate(() => document.fonts.ready.then(() => undefined))
  const m = await title.evaluate((e) => ({ overflow: getComputedStyle(e).textOverflow, cut: e.scrollWidth > e.clientWidth, width: e.getBoundingClientRect().width }))
  expect(m, `${at}: the name is cut with an ellipsis`).toMatchObject({ overflow: 'ellipsis', cut: true })
  expect(m.width, `${at}: the name has room`).toBeGreaterThanOrEqual(100)
  await noOverlap(header)
}

test('on a phone and at 1440 a name too long for the header is cut with an ellipsis and its full text is its title, again after the window changes, and on the page of a session the server no longer holds', async ({ browser, api, state }) => {
  expect(LONG_NAME.length, 'the longest name the server takes').toBeLessThanOrEqual(120)
  const s = await launch(api, state.root, LONG_NAME)
  await api.ok('POST', `/api/sessions/${s.id}/attention`, { state: 'needs_input', message: 'Ready to go on?', options: [{ label: 'Yes', input: '1' }, { label: 'No', input: '2' }] })
  const page = await dayPage(browser, { server: state, path: `/sessions/${s.id}`, width: 390, height: 844, touch: true })
  await expect(page.locator('[data-quick-reply]')).toBeVisible({ timeout: 30_000 })
  await cutAndWhole(page, 'at 390 × 844')
  // The window changes and the header is drawn again: at 1024 and back at 390 the name is still cut, with its title.
  await page.setViewportSize({ width: 1024, height: 768 })
  await expect(page.getByRole('button', { name: 'Share', exact: true })).toBeVisible()
  await cutAndWhole(page, 'resized to 1024 × 768')
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(page.locator('[data-session-bar]').getByRole('button', { name: 'Share', exact: true })).toBeHidden()
  await cutAndWhole(page, 'back at 390 × 844')

  // At the desktop app's width with the sidebar at its default width.
  const wide = await dayPage(browser, { server: state, path: `/sessions/${s.id}` })
  await expect(wide.locator('#dashboard-sidebar-main')).toBeVisible()
  await cutAndWhole(wide, 'at 1440 × 900')

  // Ended and removed: the page of a session the server no longer holds has its name in the head, cut, its full text its title.
  await api.stopSession(s.id)
  await expect.poll(async () => (await api.session(s.id)).status, { timeout: 15_000 }).toMatch(/exited|stopped/)
  await api.stopSession(s.id)
  await expect.poll(async () => (await api.call('GET', `/api/sessions/${s.id}`)).status, { timeout: 15_000 }).toBe(404)
  await page.reload()
  await expect(page.locator('[data-session-gone]')).toHaveAttribute('data-session-gone', 'record')
  await cutAndWhole(page, 'gone, at 390 × 844')
  // The theme switched, the page drawn again.
  await useTheme(page, 'light', '[data-session-gone]')
  await cutAndWhole(page, 'gone, at 390 × 844, light')
  await page.setViewportSize({ width: 1024, height: 768 })
  await cutAndWhole(page, 'gone, resized to 1024 × 768')
})
