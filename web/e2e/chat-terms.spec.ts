import { rmSync } from 'node:fs'
import { join } from 'node:path'
import type { Page } from '@playwright/test'
import { dayPage, expect, test } from './fixtures'
import { budget, chatServer, firstTurn, launch, LONG_MS, openSession, STEP_MS } from './chat-fixtures'
import type { OwnServer } from './own-server'

// The note on the vendor's terms in Chat (design 11), once: marked shown when it was drawn, never at the press, and kept by the
// server (its record of the last launch), so that a page of a new origin (the desktop app's page after a start: a fresh port) does
// not draw it again. On a server of its own with the chat's stub: `claude-bad`, whose chat does not start, so its move goes back.
test.describe.configure({ mode: 'serial' })

let own: OwnServer
const sessions: string[] = []

test.beforeAll(async () => {
  test.setTimeout(120_000)
  own = await chatServer('chat-terms')
})

test.afterAll(async () => {
  for (const id of sessions) await own.api.stopSession(id)
  await own.stop()
})

/** What the server keeps of the note. */
const noted = async () => (await own.api.ok<{ chatNoted?: boolean }>('GET', '/api/launch/last')).chatNoted === true
/** What this page keeps of it. */
const notedHere = (page: Page) => page.evaluate(() => localStorage.getItem('conductor.chatTermsNoted'))

test('the note is marked shown once drawn, not when a move that goes back was pressed, and a new page does not draw it again', async ({ browser }) => {
  budget(3 * (STEP_MS + 2 * LONG_MS) + 12 * STEP_MS + 4 * LONG_MS)
  // Each run starts with the note never drawn on this server (a repeat of the test meets the last run's mark).
  rmSync(join(own.root, 'data', 'launch', 'last.json'), { force: true })
  expect(await noted()).toBe(false)

  // A press of Chat whose move goes back to the terminal: the note was never drawn, so nothing is marked.
  const bad = await launch(own.api, sessions, 'e2e terms goes back', 'claude-bad')
  let page = await dayPage(browser, { server: own.started })
  try {
    const view = await openSession(page, bad.s.id)
    await firstTurn(page, own.api, bad.s.id)
    await page.locator('[data-way-side="protocol"]').click()
    const line = page.locator('[data-way-line]')
    await expect(line).toBeVisible({ timeout: LONG_MS })
    await expect(line).not.toHaveText('The session is moving; try again in a moment.', { timeout: LONG_MS })
    await expect(view).toHaveAttribute('data-session-way', 'terminal')
    await expect(page.locator('[data-chat-terms]')).toHaveCount(0)
    expect(await notedHere(page), 'not marked here at the press').toBeNull()
    expect(await noted(), 'not marked on the server at the press').toBe(false)

    // A move that is made: the note is drawn in the chat, and then marked, here and on the server.
    const good = await launch(own.api, sessions, 'e2e terms moves')
    const moved = await openSession(page, good.s.id)
    await firstTurn(page, own.api, good.s.id)
    await page.locator('[data-way-side="protocol"]').click()
    await expect(moved).toHaveAttribute('data-session-way', 'protocol', { timeout: LONG_MS })
    await expect(page.locator('[data-chat-view] [data-chat-terms]')).toBeVisible()
    await expect.poll(() => notedHere(page)).toBe('1')
    await expect.poll(noted, { timeout: STEP_MS }).toBe(true)
  } finally {
    await page.context().close()
  }

  // A new browser context, as the desktop app's page is after a start: its storage is empty, and the server's mark holds.
  const again = await launch(own.api, sessions, 'e2e terms new page')
  page = await dayPage(browser, { server: own.started })
  try {
    expect(await notedHere(page)).toBeNull()
    const view = await openSession(page, again.s.id)
    await firstTurn(page, own.api, again.s.id)
    await page.locator('[data-way-side="protocol"]').click()
    await expect(view).toHaveAttribute('data-session-way', 'protocol', { timeout: LONG_MS })
    await expect(page.locator('[data-chat-view] [data-chat-notice]').first()).toContainText('Moved to Chat.')
    // The chat drawn with its notice; the note is not, and the page took the server's mark for its own.
    await expect.poll(() => notedHere(page)).toBe('1')
    await expect(page.locator('[data-chat-terms]')).toHaveCount(0)
  } finally {
    await page.context().close()
  }
})
