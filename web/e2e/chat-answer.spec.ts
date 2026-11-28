import { dayPage, expect, test } from './fixtures'
import { budget, chatServer, firstTurn, launch, LONG_MS, openSession, say, STEP_MS, toChat, turnWith } from './chat-fixtures'
import type { OwnServer } from './own-server'

// An answer to the agent's own question in a chat (issue 1139), on a server of the spec's own with the chat's stub. The stub's turn
// reports and asks through Conductor's own tools (its "question" words), the question held open by the session; the person answers
// from the chat's box (the card draws no field of its own in Chat), and the agent's next turn has it. The transcript keeps no answer's text (#364): the sender's own page shows
// what it sent, with the line that says only they see it, also after leaving the page and coming back in the same tab (the
// tab's store of answers); another page shows the words that say the question was answered, never
// an empty bubble. The turn's steps of Conductor's own tools are drawn as what they did, never as a tool name with JSON.
test.describe.configure({ mode: 'serial' })

const ANSWERED = "Answered the agent's question."

let own: OwnServer
const sessions: string[] = []

test.beforeAll(async () => {
  test.setTimeout(120_000)
  own = await chatServer('chat-answer')
})

test.afterAll(async () => {
  for (const id of sessions) await own.api.stopSession(id)
  await own.stop()
})

test("an answer to the agent's question from the chat's box reaches it, its sender sees it, another page sees that it was answered", async ({ browser }) => {
  const api = own.api
  const { s } = await launch(api, sessions, 'e2e chat answer')
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  try {
    const view = await openSession(page, s.id)
    await firstTurn(page, api, s.id)
    await toChat(api, s.id, view)
    await say(page, 'a question please')
    // The question, held open by the session once the turn has ended: the bar asks it.
    budget(LONG_MS + 3 * STEP_MS)
    const asked = turnWith(page, 'stub chat: a question please')
    await expect(asked).toHaveAttribute('data-turn-outcome', /./, { timeout: 30_000 })
    const bar = page.locator('[data-quick-reply]')
    await expect(bar.locator('[data-quick-reply-message]')).toHaveText('Shall I go on?')
    // The chat's box answers it (UX3 R5): the card has no field of its own; the box says so, and its Send reads Answer once typed.
    await expect(bar.locator('input')).toHaveCount(0)
    const box = page.locator('[data-chat-view] [data-chat-box] textarea')
    await expect(box).toHaveAttribute('placeholder', /^Answer .+…$/)
    await box.fill('Yes, go on.')
    await expect(page.locator('[data-chat-view] [data-chat-send]')).toHaveText('Answer')
    await box.press('Enter')
    await expect(box).toHaveValue('')
    // It reached the agent: its next turn has the words.
    budget(LONG_MS + 4 * STEP_MS)
    await expect(turnWith(page, 'stub chat: Yes, go on.')).toHaveAttribute('data-turn-outcome', /./, { timeout: 30_000 })
    // The sender's own page: what it sent, and that only they see it.
    const answer = page.locator('[data-chat-prompt-answer]')
    await expect(answer).toHaveText('Yes, go on.')
    await expect(answer).toHaveAttribute('data-answer-own', 'true')
    await expect(page.locator('[data-chat-answer-line]')).toHaveText('Only you see your answer.')
    // Received as the question's answer, never as a message: no message bubble holds the words.
    await expect(page.locator('[data-chat-prompt-text]').filter({ hasText: 'Yes, go on.' })).toHaveCount(0)
    // Away from the session page and back in the same tab: a new view and connection, the answer's words the tab's own.
    budget(LONG_MS + 4 * STEP_MS)
    await page.locator('[data-sidebar-tile]').filter({ has: page.locator('[data-nav-label]', { hasText: /^Yard$/ }) }).first().click({ timeout: STEP_MS })
    await expect(page).toHaveURL(/\/yard/)
    await page.goBack()
    await expect(page.locator('[data-session-way]').first()).toHaveAttribute('data-session-way', 'protocol', { timeout: 30_000 })
    await expect(answer).toHaveText('Yes, go on.')
    await expect(answer).toHaveAttribute('data-answer-own', 'true')
    await expect(page.locator('[data-chat-answer-line]')).toHaveText('Only you see your answer.')
    // The turn's steps of Conductor's own tools, opened: what each did.
    await asked.locator('[data-chat-steps-line]').click()
    await expect(asked.locator('[data-tool-step] [data-tool-name]')).toHaveText(['Report', 'Question'])
    await expect(asked.locator('[data-tool-step] [data-tool-summary]')).toHaveText(['progress: checked the files', 'Shall I go on?'])
    // Another page on the session: the words that say the question was answered, never its text, never an empty bubble.
    const other = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
    try {
      await openSession(other, s.id, 'protocol')
      budget(2 * STEP_MS)
      await expect(other.locator('[data-chat-prompt-answer]')).toHaveText(ANSWERED, { timeout: 30_000 })
      await expect(other.locator('[data-chat-answer-line]')).toHaveCount(0)
      await expect(other.locator('[data-chat-prompt]').filter({ hasText: 'Yes, go on.' })).toHaveCount(0)
    } finally {
      await other.context().close()
    }
  } finally {
    await page.context().close()
  }
})
