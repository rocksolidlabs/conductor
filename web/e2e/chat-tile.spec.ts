import { dayPage, expect, test } from './fixtures'
import { budget, chatServer, firstTurn, launch, LONG_MS, openSession, readStub, release, say, STEP_MS, toChat, turnWith } from './chat-fixtures'
import type { OwnServer } from './own-server'

// A chat's tile (UX3 R8a), on the Yard of a server of the spec's own with the chat's stub: the tile shows the end of the
// conversation, read-only (no box, no Stop), its turn's steps one line that says Working. while a turn is held at the stub's gate.
// The tile is found by its session's id, at 2000 x 810 and at 1440 x 900.
test.describe.configure({ mode: 'serial' })

let own: OwnServer
const sessions: string[] = []

test.beforeAll(async () => {
  test.setTimeout(120_000)
  own = await chatServer('chat-tile')
})

test.afterAll(async () => {
  for (const id of sessions) await own.api.stopSession(id)
  await own.stop()
})

for (const size of [
  { width: 2000, height: 810 },
  { width: 1440, height: 900 },
]) {
  test(`a chat's tile on the Yard shows the end of its conversation, and Working. while a turn runs, at ${size.width} x ${size.height}`, async ({ browser }) => {
    const { s, conv } = await launch(own.api, sessions, `e2e chat tile ${size.width}`)
    const page = await dayPage(browser, { server: own.started, ...size })
    const yard = await dayPage(browser, { server: own.started, ...size, path: '/yard' })
    try {
      const view = await openSession(page, s.id)
      await firstTurn(page, own.api, s.id)
      await toChat(own.api, s.id, view)
      await say(page, 'tile words please')
      budget(2 * LONG_MS + 6 * STEP_MS)
      await expect(turnWith(page, 'stub chat: tile words please')).toHaveAttribute('data-turn-outcome', 'done', { timeout: LONG_MS })
      const tile = yard.locator(`[data-session-tile][data-session-id="${s.id}"] [data-chat-tile]`)
      await expect(tile).toBeVisible({ timeout: LONG_MS })
      await expect(tile.locator('[data-chat-words]').last()).toContainText('stub chat: tile words please', { timeout: LONG_MS })
      await expect(tile.locator('[data-chat-prompt-text]').last()).toHaveText('tile words please')
      // Read-only: no box, no Stop, nothing to press.
      await expect(tile.locator('textarea, [data-chat-box], [data-chat-stop]')).toHaveCount(0)
      await expect(tile).toHaveAttribute('inert', /.*/)
      // The end in view: the last words lie inside the tile.
      const [t, last] = await Promise.all([tile.boundingBox(), tile.locator('[data-chat-words]').last().boundingBox()])
      expect(last!.y + last!.height, 'the end is in view').toBeLessThanOrEqual(t!.y + t!.height + 1)

      // A turn held at the stub's gate: the tile says it works, and the turn's end shows when it goes on.
      budget(3 * LONG_MS + 2 * STEP_MS)
      await say(page, 'hold the tile')
      await expect.poll(() => readStub(own, conv, 'held'), { timeout: LONG_MS }).toContain('held')
      await expect(tile).toHaveAttribute('data-turn-running', 'true', { timeout: LONG_MS })
      await expect(tile.locator('[data-chat-steps-line]').last()).toHaveText('Working.')
      release(own, conv)
      await expect(tile.locator('[data-chat-words]').last()).toContainText('stub chat: hold the tile', { timeout: LONG_MS })
      await expect(tile).not.toHaveAttribute('data-turn-running', 'true', { timeout: LONG_MS })
    } finally {
      await yard.context().close()
      await page.context().close()
    }
  })
}
