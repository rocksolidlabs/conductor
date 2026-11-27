import { atRest, dayPage, expect, test } from './fixtures'
import { budget, chatServer, firstTurn, joined, launch, LONG_MS, openSession, readStub, release, say, STEP_MS, toChat, turnWith } from './chat-fixtures'
import { inside, uncut } from './layout'
import type { BrowserContext, Page } from '@playwright/test'
import type { OwnServer } from './own-server'

// The chat box's list for `@` (UX3 R6), on a server of the spec's own with the chat's stub: an `@` word lists the folder's files
// whose name holds it, read from the find's answer (never a clock); Enter picks and writes `@<path> `, the list goes, and the
// operator's own line goes for the agent to attach the file (UX3 R10); Escape closes it and keeps the text; it stays open through
// a redraw the spec causes; a control link's box, whose lines go as written, offers none; every row uncut and inside the window at 2000 x 810, 1440 x 900 and 390 x 844, at rest.
test.describe.configure({ mode: 'serial' })

let own: OwnServer
const sessions: string[] = []
const guests: BrowserContext[] = []

test.beforeAll(async () => {
  test.setTimeout(120_000)
  own = await chatServer('chat-files-menu')
})

test.afterAll(async () => {
  for (const c of guests) await c.close()
  for (const id of sessions) await own.api.stopSession(id)
  await own.stop()
})

/** A chat whose folder holds notes.txt (the stub's edit writes it), its page open. */
async function aChat(page: Page, name: string): Promise<{ id: string; conv: string }> {
  const { s, conv } = await launch(own.api, sessions, name)
  const view = await openSession(page, s.id)
  await firstTurn(page, own.api, s.id)
  await toChat(own.api, s.id, view)
  await say(page, 'edit please')
  budget(LONG_MS)
  await expect(turnWith(page, 'stub chat: edit please')).toHaveAttribute('data-turn-outcome', 'done', { timeout: LONG_MS })
  return { id: s.id, conv }
}

const box = (page: Page) => page.locator('[data-chat-view] [data-chat-box] textarea')
const menu = (page: Page) => page.locator('[data-chat-view] [data-chat-menu]')
const row = (page: Page, path: string) => menu(page).locator(`[data-chat-menu-row="${path}"]`)

test('an @ word lists the files that match; Enter writes the path, the list goes, and the operator\'s own line goes for the agent to attach it', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  try {
    const { conv } = await aChat(page, 'e2e files menu')
    budget(10 * STEP_MS + 2 * LONG_MS)
    await box(page).click()
    await page.keyboard.type('Look at @no')
    await expect(row(page, 'notes.txt')).toBeVisible({ timeout: LONG_MS })
    await expect(box(page)).toHaveAttribute('aria-activedescendant', /.+/)
    // Escape closes the list and keeps the text; the cursor stays in the box.
    await page.keyboard.press('Escape')
    await expect(menu(page)).toHaveCount(0)
    await expect(box(page)).toHaveValue('Look at @no')
    await expect(box(page)).toBeFocused()
    // Typing on opens it again; Enter picks and sends nothing.
    await page.keyboard.type('t')
    await expect(row(page, 'notes.txt')).toBeVisible({ timeout: LONG_MS })
    await page.keyboard.press('Enter')
    await expect(menu(page)).toHaveCount(0)
    await expect(box(page)).toHaveValue('Look at @notes.txt ')
    await page.keyboard.type('please')
    await expect(box(page)).toHaveValue('Look at @notes.txt please')
    await page.keyboard.press('Enter')
    // The operator's own line, where the server lets this box expand it (UX3 R10): the agent attaches the file.
    await expect(turnWith(page, 'stub attached: notes.txt')).toHaveAttribute('data-turn-outcome', 'done', { timeout: LONG_MS })
    expect(readStub(own, conv, 'marks').trim().split('\n').pop()).toBe('unmarked')
  } finally {
    await page.context().close()
  }
})

test('the list stays open through a turn that ends while it shows; a control link\'s box, whose lines go as written, offers none', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  try {
    const { id, conv } = await aChat(page, 'e2e files menu redraw')
    await say(page, 'hold the list')
    budget(6 * STEP_MS + 3 * LONG_MS)
    await expect.poll(() => readStub(own, conv, 'held'), { timeout: LONG_MS }).toContain('held')
    await box(page).click()
    await page.keyboard.type('@no')
    await expect(row(page, 'notes.txt')).toBeVisible({ timeout: LONG_MS })
    release(own, conv)
    await expect(turnWith(page, 'stub chat: hold the list')).toHaveAttribute('data-turn-outcome', 'done', { timeout: LONG_MS })
    await expect(row(page, 'notes.txt')).toBeVisible()
    await expect(box(page)).toHaveValue('@no')
    await page.keyboard.press('Escape')

    const c = await joined(browser, own, id, 'control')
    guests.push(c.ctx)
    await expect(box(c.g)).toBeVisible({ timeout: LONG_MS })
    await box(c.g).click()
    await c.g.keyboard.type('@no')
    await expect(box(c.g)).toHaveValue('@no')
    // A picked file there would never be attached: no list (UX3 R10). Read again after a moment the find would have answered in.
    await c.g.keyboard.type('t')
    await expect(box(c.g)).toHaveValue('@not')
    await expect(c.g.locator('[data-chat-menu]')).toHaveCount(0)
  } finally {
    await page.context().close()
  }
})

test('the rows are whole and inside the window at 2000 x 810, 1440 x 900 and 390 x 844, measured at rest', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 2000, height: 810 })
  try {
    await aChat(page, 'e2e files menu sizes')
    for (const size of [{ width: 2000, height: 810 }, { width: 1440, height: 900 }, { width: 390, height: 844 }]) {
      budget(6 * STEP_MS + LONG_MS)
      await page.setViewportSize(size)
      await box(page).fill('')
      await box(page).click()
      await page.keyboard.type('@no')
      await expect(row(page, 'notes.txt')).toBeVisible({ timeout: LONG_MS })
      await atRest(menu(page))
      await uncut(menu(page).locator('[data-chat-menu-row]'))
      await inside(menu(page), page.locator('body'))
      await page.keyboard.press('Escape')
      await expect(menu(page)).toHaveCount(0)
    }
  } finally {
    await page.context().close()
  }
})
