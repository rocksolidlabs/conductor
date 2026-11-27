import { atRest, dayPage, expect, test } from './fixtures'
import { budget, chatServer, firstTurn, launch, LONG_MS, openSession, readStub, release, say, STEP_MS, toChat, turnWith } from './chat-fixtures'
import { BOX_SLACK, uncut } from './layout'
import type { Page } from '@playwright/test'
import type { OwnServer } from './own-server'

// The chat's box (UX3 R4), on a server of the spec's own with the chat's stub: the cursor is in it as Chat opens; a line typed for
// one agent is there again after opening another and coming back, and gone after a reload; `Stop turn`, with its word, sits left of
// Send while a turn is held at the stub's gate, and goes at the turn's end. Each session is found by its id.
test.describe.configure({ mode: 'serial' })

let own: OwnServer
const sessions: string[] = []

test.beforeAll(async () => {
  test.setTimeout(120_000)
  own = await chatServer('chat-box')
})

test.afterAll(async () => {
  for (const id of sessions) await own.api.stopSession(id)
  await own.stop()
})

/** A session of the stub moved to its chat, its page open on it. */
async function aChat(page: Page, name: string): Promise<{ id: string; conv: string }> {
  const { s, conv } = await launch(own.api, sessions, name)
  const view = await openSession(page, s.id)
  await firstTurn(page, own.api, s.id)
  await toChat(own.api, s.id, view)
  return { id: s.id, conv }
}

/** Opens a session from its row in the sidebar: a move inside the app, not a load. */
async function openFromRow(page: Page, id: string): Promise<void> {
  budget(2 * STEP_MS + LONG_MS)
  const link = page.locator(`[data-session-list] [data-sidebar-row="s:${id}"] a[href="/sessions/${id}"]`).first()
  await link.scrollIntoViewIfNeeded()
  await link.click()
  await expect(page).toHaveURL(new RegExp(`/sessions/${id}$`))
  await expect(page.locator('[data-session-way]').first()).toHaveAttribute('data-session-way', 'protocol', { timeout: LONG_MS })
}

test('the cursor is in the box as Chat opens, and a line typed for one agent waits for it while another is opened', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  try {
    const a = await aChat(page, 'e2e box a')
    const b = await aChat(page, 'e2e box b')
    // A chat opened from its address: the box has the cursor.
    budget(4 * STEP_MS + LONG_MS)
    await openSession(page, a.id, 'protocol')
    const field = page.locator('[data-chat-view] [data-chat-box] textarea')
    await expect(field).toBeFocused({ timeout: STEP_MS })
    await expect(field).toHaveAttribute('placeholder', /^Message /)
    await page.keyboard.type('half a thought for a')
    // Another agent opened and this one again: the line is there, and the other's box is its own.
    await openFromRow(page, b.id)
    await expect(field).toHaveValue('')
    await field.fill('and one for b')
    await openFromRow(page, a.id)
    await expect(field).toHaveValue('half a thought for a')
    await openFromRow(page, b.id)
    await expect(field).toHaveValue('and one for b')
    // Sent, it is gone; a reload drops what is not sent.
    budget(LONG_MS + 2 * STEP_MS)
    await field.press('Enter')
    await expect(turnWith(page, 'stub chat: and one for b')).toHaveAttribute('data-turn-outcome', 'done', { timeout: LONG_MS })
    await expect(field).toHaveValue('')
    await openFromRow(page, a.id)
    await expect(field).toHaveValue('half a thought for a')
    await page.reload()
    await expect(page.locator('[data-session-way]').first()).toHaveAttribute('data-session-way', 'protocol', { timeout: LONG_MS })
    await expect(field).toHaveValue('')
    // Escape leaves the box.
    await expect(field).toBeFocused({ timeout: STEP_MS })
    await page.keyboard.press('Escape')
    await expect(field).not.toBeFocused()
  } finally {
    await page.context().close()
  }
})

test('Stop turn, with its word, sits left of Send while a turn is held, at each size, and goes at the turn\'s end', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 2000, height: 810 })
  try {
    const { conv } = await aChat(page, 'e2e box stop')
    const chat = page.locator('[data-chat-view]')
    await expect(chat.locator('[data-chat-stop]')).toHaveCount(0)
    await say(page, 'hold the box')
    budget(2 * LONG_MS)
    await expect.poll(() => readStub(own, conv, 'held'), { timeout: LONG_MS }).toContain('held')
    await expect(chat).toHaveAttribute('data-turn-running', 'true', { timeout: LONG_MS })
    const stop = chat.locator('[data-chat-stop]')
    const send = chat.locator('[data-chat-send]')
    for (const size of [{ width: 2000, height: 810 }, { width: 1440, height: 900 }, { width: 390, height: 844 }]) {
      budget(5 * STEP_MS)
      await page.setViewportSize(size)
      await expect(stop).toHaveText('Stop turn')
      await atRest(chat.locator('[data-chat-box]'))
      await uncut(stop)
      await uncut(send)
      const [st, se, field] = await Promise.all([stop.boundingBox(), send.boundingBox(), chat.locator('[data-chat-box] textarea').boundingBox()])
      expect(st!.x + st!.width, `${size.width}: Stop turn left of Send`).toBeLessThanOrEqual(se!.x + BOX_SLACK)
      expect(field!.x + field!.width, `${size.width}: the field first`).toBeLessThanOrEqual(st!.x + BOX_SLACK)
      // One row: the three share a line.
      expect(Math.abs(st!.y + st!.height - (se!.y + se!.height)), `${size.width}: one row`).toBeLessThanOrEqual(1)
    }
    budget(LONG_MS + STEP_MS)
    release(own, conv)
    await expect(turnWith(page, 'stub chat: hold the box')).toHaveAttribute('data-turn-outcome', 'done', { timeout: LONG_MS })
    await expect(stop).toHaveCount(0)
  } finally {
    await page.context().close()
  }
})
