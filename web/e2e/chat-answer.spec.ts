import { dayPage, expect, test } from './fixtures'
import { budget, chatServer, firstTurn, launch, LONG_MS, openSession, say, STEP_MS, toChat, turnWith } from './chat-fixtures'
import type { OwnServer } from './own-server'

// An answer to the agent's own question in a chat (issue 1139), on a server of the spec's own with the chat's stub. The stub's turn
// reports and asks through Conductor's own tools (its "question" words), the question held open by the session; the person answers
// from the reply bar, and the agent's next turn has it. The transcript keeps no answer's text (#364): the sender's own page shows
// what it sent, with the line that says only they see it; another page shows the words that say the question was answered, never
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

test("an answer to the agent's question from the bar reaches it, its sender sees it, another page sees that it was answered", async ({ browser }) => {
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
    await bar.locator('input').fill('Yes, go on.')
    await bar.getByRole('button', { name: 'Send' }).click()
    // It reached the agent: its next turn has the words.
    budget(LONG_MS + 4 * STEP_MS)
    await expect(turnWith(page, 'stub chat: Yes, go on.')).toHaveAttribute('data-turn-outcome', /./, { timeout: 30_000 })
    // The sender's own page: what it sent, and that only they see it.
    const answer = page.locator('[data-chat-prompt-answer]')
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
