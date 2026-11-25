import { dayPage, expect, test } from './fixtures'
import { budget, chatServer, firstTurn, joined, launch, LONG_MS, openSession, readStub, say, STEP_MS, toChat, turnWith } from './chat-fixtures'
import type { OwnServer } from './own-server'

// A chat through a share link (docs/plans/chat-or-terminal.md, row 7, design 12), on a server of the spec's own: a view link and a
// control link open the session on the join page and see the chat, never an empty terminal; the view link has no box to write in;
// the control link sends a message and presses Stop; a file change opens to its diff with no link to an editor the join page has not;
// below xl the join page's People opens the people's sheet, titled People. Each step raises the test's timeout by its own waits' caps.
test.describe.configure({ mode: 'serial' })

let own: OwnServer
const sessions: string[] = []

test.beforeAll(async () => {
  test.setTimeout(120_000)
  own = await chatServer('chat-link')
})

test.afterAll(async () => {
  for (const id of sessions) await own.api.stopSession(id)
  await own.stop()
})

test('a view link and a control link see the chat; the control link sends a message and stops a turn', async ({ browser }) => {
  const api = own.api
  const { s, conv } = await launch(api, sessions, 'e2e chat link')
  const page = await dayPage(browser, { server: own.started })
  const guests: Array<{ close: () => Promise<void> }> = []
  try {
    const view = await openSession(page, s.id)
    await firstTurn(page, api, s.id)
    await toChat(api, s.id, view)
    await say(page, 'the owner says hello')
    budget(LONG_MS)
    await expect(turnWith(page, 'stub chat: the owner says hello')).toHaveAttribute('data-turn-outcome', 'done', { timeout: LONG_MS })

    // A view link: the chat, its turn, no box; the terminal under it is not drawn.
    budget(12 * STEP_MS + 10 * LONG_MS)
    const v = await joined(browser, own, s.id, 'view')
    guests.push(v.ctx)
    await expect(turnWith(v.g, 'stub chat: the owner says hello')).toBeVisible({ timeout: LONG_MS })
    await expect(v.g.locator('[data-chat-box]')).toHaveCount(0)
    await expect(v.g.locator('.terminal-host').first()).toBeHidden()
    await expect(v.g.locator('[data-way-label]')).toHaveText('Chat')

    // A control link: the box; a message it sends is the chat's turn, seen by the owner too; Stop ends a held turn.
    const c = await joined(browser, own, s.id, 'control')
    guests.push(c.ctx)
    await expect(turnWith(c.g, 'stub chat: the owner says hello')).toBeVisible({ timeout: LONG_MS })
    await expect(c.g.locator('[data-chat-box]')).toBeVisible()
    await say(c.g, 'hello from the link')
    await expect(turnWith(c.g, 'stub chat: hello from the link')).toHaveAttribute('data-turn-outcome', 'done', { timeout: LONG_MS })
    await expect(turnWith(page, 'stub chat: hello from the link')).toHaveAttribute('data-turn-outcome', 'done', { timeout: LONG_MS })
    await say(c.g, 'hold for the link')
    await expect.poll(() => readStub(own, conv, 'held'), { timeout: LONG_MS }).toContain('held')
    await c.g.locator('[data-chat-view] [data-chat-stop]').click()
    await expect(turnWith(c.g, 'hold for the link')).toHaveAttribute('data-turn-outcome', 'interrupted', { timeout: LONG_MS })
    await expect(turnWith(v.g, 'hold for the link')).toHaveAttribute('data-turn-outcome', 'interrupted', { timeout: LONG_MS })

    // A file change on the join page: its diff opens; no link to an editor this page has not.
    await say(c.g, 'edit for the link')
    budget(4 * STEP_MS + LONG_MS)
    const change = turnWith(c.g, 'stub chat: edit for the link').locator('[data-file-change]')
    await expect(change.locator('[data-file-change-line]')).toContainText('notes.txt', { timeout: LONG_MS })
    await change.locator('[data-file-change-line]').click()
    await expect(change.locator('[data-inline-diff]')).toContainText('+gamma')
    await expect(change.locator('[data-file-change-open-diff]')).toHaveCount(0)
  } finally {
    for (const g of guests) await g.close()
    await page.context().close()
  }
})

test("below xl the join page's People opens the people's sheet, titled People", async ({ browser }) => {
  const api = own.api
  const { s } = await launch(api, sessions, 'e2e chat link people')
  const guest = await joined(browser, own, s.id, 'control', { width: 1024, height: 800 })
  try {
    budget(4 * STEP_MS + LONG_MS)
    const button = guest.g.locator('[data-chat-button]')
    await expect(button).toBeVisible({ timeout: LONG_MS })
    await expect(button).toHaveAccessibleName('People')
    // At this width the people's chat is beside the terminal: the button folds it away, and below md it is a sheet.
    await guest.g.setViewportSize({ width: 600, height: 800 })
    await button.click()
    const sheet = guest.g.locator('[data-chat-sheet]')
    await expect(sheet).toBeVisible()
    await expect(guest.g.getByRole('dialog').filter({ has: sheet })).toContainText('People')
    await expect(guest.g.getByRole('dialog').filter({ has: sheet })).not.toContainText(/^Chat/)
  } finally {
    await guest.ctx.close()
  }
})
