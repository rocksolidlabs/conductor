import { atRest, dayPage, expect, test } from './fixtures'
import { budget, chatServer, firstTurn, launch, lineButton, LONG_MS, openSession, say, STEP_MS, toChat } from './chat-fixtures'
import { noSideways, uncut } from './layout'
import type { OwnServer } from './own-server'

// A chat's page laid out (docs/plans/chat-or-terminal.md, row 7, design 11), on a server of the spec's own: at 1440 x 900,
// 1280 x 720 and 390 x 844, in both themes, nothing scrolls or is cut sideways, the header's name is whole beside the switch (in More
// on a phone), and the box's controls are in view; measured once the page's own animations have ended. One session, a turn with its
// steps opened and a permission question open in it, is drawn at each size.
test.describe.configure({ mode: 'serial' })

let own: OwnServer
const sessions: string[] = []
let id = ''

test.beforeAll(async ({ browser }) => {
  test.setTimeout(180_000)
  own = await chatServer('chat-layout')
  const api = own.api
  const { s } = await launch(api, sessions, 'layout')
  id = s.id
  const page = await dayPage(browser, { server: own.started })
  try {
    const view = await openSession(page, s.id)
    await firstTurn(page, api, s.id)
    await toChat(api, s.id, view)
    await say(page, 'steps edit markdown')
    await expect(page.locator('[data-chat-turn][data-turn-outcome="done"]')).toHaveCount(1, { timeout: LONG_MS })
    await say(page, 'ask long')
    await expect(page.locator('[data-permission-question][data-permission-state="open"]')).toBeVisible({ timeout: LONG_MS })
  } finally {
    await page.context().close()
  }
})

test.afterAll(async () => {
  for (const sid of sessions) await own.api.stopSession(sid)
  await own.stop()
})

for (const theme of ['light', 'dark'] as const) {
  for (const [width, height] of [[1440, 900], [1280, 720], [390, 844]] as const) {
    test(`a chat's page at ${width} x ${height} (${theme}): nothing sideways, the name whole with the switch, the box in reach`, async ({ browser }) => {
      const phone = width < 640
      // The walk's waits: the page and its session, about twenty bounded checks and measures, and the long ones.
      budget(22 * STEP_MS + 2 * LONG_MS)
      const page = await dayPage(browser, { server: own.started, width, height, theme, touch: phone })
      try {
        await openSession(page, id, 'protocol')
        const chat = page.locator('[data-chat-view]')
        await expect(chat.locator('[data-permission-question][data-permission-state="open"]')).toBeVisible({ timeout: LONG_MS })
        // The turn's steps opened, the question's whole input opened: the longest things the chat draws.
        await chat.locator('[data-chat-steps-line]').first().click()
        await expect(chat.locator('[data-tool-step]').first()).toBeVisible()
        await expect(lineButton(chat.locator('[data-permission-question][data-permission-state="open"]'))).toBeVisible()
        await lineButton(chat.locator('[data-permission-question][data-permission-state="open"]')).click()
        await expect(chat.locator('[data-permission-whole]')).toBeVisible()
        await atRest(page.locator('[data-session-bar]'))
        await atRest(chat)
        await noSideways(page)
        // The name whole; beside it the switch, or on a phone the switch's two items in More.
        await uncut(page.locator('[data-session-title]'))
        if (phone) {
          await expect(page.locator('[data-way-switch]')).toHaveCount(0)
          await page.locator('[data-session-bar] [data-more-button]').click()
          await expect(page.getByRole('menuitemcheckbox', { name: 'Chat' })).toBeVisible()
          await expect(page.getByRole('menuitemcheckbox', { name: 'Terminal' })).toBeVisible()
          await page.keyboard.press('Escape')
          await expect(page.getByRole('menu')).toHaveCount(0)
        } else {
          await uncut(page.locator('[data-way-switch] [data-way-side]'))
        }
        // The box's controls in view and whole: the field, Send, the help line.
        const box = chat.locator('[data-chat-box]')
        await box.locator('textarea').scrollIntoViewIfNeeded()
        await uncut(box.locator('[data-chat-send]'))
        await expect(box.locator('textarea')).toBeInViewport()
        await uncut(box.locator('[data-chat-help]'))
        // The question's buttons in reach.
        const q = chat.locator('[data-permission-question][data-permission-state="open"]')
        await q.locator('[data-permission-allow]').scrollIntoViewIfNeeded()
        await uncut(q.locator('[data-permission-allow]'))
        await uncut(q.locator('[data-permission-deny]'))
      } finally {
        await page.context().close()
      }
    })
  }
}
