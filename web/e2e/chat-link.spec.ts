import type { Browser } from '@playwright/test'
import { dayPage, expect, test } from './fixtures'
import { chatServer, firstTurn, launch, openSession, readStub, say, toChat, turnWith } from './chat-fixtures'
import type { OwnServer } from './own-server'

// A chat through a share link (docs/plans/chat-or-terminal.md, row 7, design 12), on a server of the spec's own: a view link and a
// control link open the session on the join page and see the chat, never an empty terminal; the view link has no box to write in;
// the control link sends a message and presses Stop.
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

/** The join page of a link of `role`, joined as Priya. */
async function joined(browser: Browser, id: string, role: 'view' | 'control') {
  const { token } = await own.api.ok<{ token: string }>('POST', `/api/sessions/${id}/links`, { role })
  const ctx = await browser.newContext({ baseURL: own.started.baseURL, viewport: { width: 1280, height: 800 } })
  await ctx.addInitScript(() => localStorage.setItem('conductor.displayName', 'Priya'))
  const g = await ctx.newPage()
  await g.goto(`/join/${token}`)
  await g.getByRole('button', { name: 'Join session' }).click()
  return { ctx, g }
}

test('a view link and a control link see the chat; the control link sends a message and stops a turn', async ({ browser }) => {
  test.setTimeout(180_000)
  const api = own.api
  const { s, conv } = await launch(api, sessions, 'e2e chat link')
  const page = await dayPage(browser, { server: own.started })
  const guests: Array<{ close: () => Promise<void> }> = []
  try {
    const view = await openSession(page, s.id)
    await firstTurn(page, api, s.id)
    await toChat(api, s.id, view)
    await say(page, 'the owner says hello')
    await expect(turnWith(page, 'stub chat: the owner says hello')).toHaveAttribute('data-turn-outcome', 'done', { timeout: 30_000 })

    // A view link: the chat, its turn, no box; the terminal under it is not drawn.
    const v = await joined(browser, s.id, 'view')
    guests.push(v.ctx)
    await expect(turnWith(v.g, 'stub chat: the owner says hello')).toBeVisible({ timeout: 30_000 })
    await expect(v.g.locator('[data-chat-box]')).toHaveCount(0)
    await expect(v.g.locator('.terminal-host').first()).toBeHidden()
    await expect(v.g.locator('[data-way-label]')).toHaveText('Chat')

    // A control link: the box; a message it sends is the chat's turn, seen by the owner too; Stop ends a held turn.
    const c = await joined(browser, s.id, 'control')
    guests.push(c.ctx)
    await expect(turnWith(c.g, 'stub chat: the owner says hello')).toBeVisible({ timeout: 30_000 })
    await expect(c.g.locator('[data-chat-box]')).toBeVisible()
    await say(c.g, 'hello from the link')
    await expect(turnWith(c.g, 'stub chat: hello from the link')).toHaveAttribute('data-turn-outcome', 'done', { timeout: 30_000 })
    await expect(turnWith(page, 'stub chat: hello from the link')).toHaveAttribute('data-turn-outcome', 'done', { timeout: 30_000 })
    await say(c.g, 'hold for the link')
    await expect.poll(() => readStub(own, conv, 'held'), { timeout: 30_000 }).toContain('held')
    await c.g.locator('[data-chat-view] [data-chat-stop]').click()
    await expect(turnWith(c.g, 'hold for the link')).toHaveAttribute('data-turn-outcome', 'interrupted', { timeout: 30_000 })
    await expect(turnWith(v.g, 'hold for the link')).toHaveAttribute('data-turn-outcome', 'interrupted', { timeout: 30_000 })
  } finally {
    for (const g of guests) await g.close()
    await page.context().close()
  }
})
