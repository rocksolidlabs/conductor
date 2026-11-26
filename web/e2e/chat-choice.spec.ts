import type { Page, Route } from '@playwright/test'
import { dayPage, expect, test } from './fixtures'
import { budget, chatServer, firstTurn, launch, LONG_MS, openSession, say, STEP_MS, toChat, turnWith } from './chat-fixtures'
import type { OwnServer } from './own-server'

// A choice of the agent's own question in a chat (issue 1148), on a server of the spec's own with the chat's stub: its "question
// choices" words ask "Pears or plums?" with the choices Pears and Plums through Conductor's own tool, the question kept open by the
// session. A chat has no terminal: a choice pressed is the person's answer in the choice's words, as a typed answer is, from the
// reply bar and from the session's row in the sidebar alike; it ends the question, the agent's next turn has it, and the sender's
// page shows it as its own answer; a session decision link's Needs you sends it so too. An answer the session refuses (the question answered from another page first) is said where it
// was pressed: another page's row, its live stream answered with the sessions as the question stood and later reads held, so that
// it still offers the choice.
test.describe.configure({ mode: 'serial' })

const OWN_LINE = 'Only you see your answer.'

let own: OwnServer
const sessions: string[] = []

test.beforeAll(async () => {
  test.setTimeout(120_000)
  own = await chatServer('chat-choice')
})

test.afterAll(async () => {
  for (const id of sessions) await own.api.stopSession(id)
  await own.stop()
})

/** A chat session whose agent asks "Pears or plums?" with its two choices, the question shown on the page's reply bar. */
async function asked(page: Page, name: string): Promise<{ id: string; question: string }> {
  const { s } = await launch(own.api, sessions, name)
  const view = await openSession(page, s.id)
  await firstTurn(page, own.api, s.id)
  await toChat(own.api, s.id, view)
  await say(page, 'a question choices please')
  budget(LONG_MS + 2 * STEP_MS)
  await expect(page.locator('[data-quick-reply] [data-quick-reply-message]')).toHaveText('Pears or plums?', { timeout: LONG_MS })
  const info = await own.api.session(s.id)
  expect(info.attention?.state).toBe('needs_input')
  return { id: s.id, question: info.attention!.id! }
}

/** The question answered: the session asks nothing, the agent's next turn has the choice's words, the sender's page its own answer. */
async function answered(page: Page, id: string, word: string) {
  budget(2 * LONG_MS + 3 * STEP_MS)
  await expect(turnWith(page, `stub chat: ${word}`)).toHaveAttribute('data-turn-outcome', 'done', { timeout: LONG_MS })
  await expect.poll(async () => (await own.api.session(id)).attention?.state ?? '', { timeout: LONG_MS }).not.toBe('needs_input')
  const answer = page.locator('[data-chat-prompt-answer]')
  await expect(answer).toHaveText(word)
  await expect(answer).toHaveAttribute('data-answer-own', 'true')
  await expect(page.locator('[data-chat-answer-line]')).toHaveText(OWN_LINE)
  await expect(page.locator('[data-quick-reply]')).toHaveCount(0)
}

test("a choice pressed in the reply bar is the person's answer in a chat: the question ends and the agent's next turn has it", async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  try {
    const { id } = await asked(page, 'e2e chat choice bar')
    const bar = page.locator('[data-quick-reply]')
    await expect(bar.getByRole('button', { name: /Plums/ })).toBeEnabled()
    await bar.getByRole('button', { name: /Plums/ }).click()
    await answered(page, id, 'Plums')
  } finally {
    await page.context().close()
  }
})

test("a choice pressed on the session's row in the sidebar is the person's answer in a chat, as the bar's is", async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  try {
    const { id } = await asked(page, 'e2e chat choice row')
    const choice = page.locator(`[data-sidebar-row="s:${id}"] [data-row-choice]`).filter({ hasText: 'Pears' })
    await choice.scrollIntoViewIfNeeded()
    await expect(choice).toBeEnabled()
    await choice.click()
    await answered(page, id, 'Pears')
  } finally {
    await page.context().close()
  }
})

test("a choice pressed on a session decision link's Needs you is the person's answer in a chat, as the bar's is", async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  let linkCtx: Awaited<ReturnType<typeof browser.newContext>> | undefined
  try {
    const { id, question } = await asked(page, 'e2e chat choice link')
    const made = await own.api.ok<{ token: string }>('POST', `/api/sessions/${id}/links`, { role: 'control', decide: true, label: 'e2e chat decide' })
    linkCtx = await browser.newContext({ baseURL: own.started.baseURL, viewport: { width: 1280, height: 800 } })
    const holder = await linkCtx.newPage()
    budget(4 * STEP_MS)
    await holder.goto(`/join/${made.token}`)
    await holder.getByRole('textbox', { name: 'Your name' }).fill('e2e holder')
    await holder.getByRole('button', { name: 'Join session' }).click()
    await expect(holder.locator('[data-join-tab="decide"]')).toHaveAttribute('aria-selected', 'true', { timeout: STEP_MS })
    const ref = `question:${id}/${question}`
    await holder.locator(`[data-join-session-decide] [data-inbox-row="${ref}"]`).click()
    const item = holder.locator(`[data-join-session-decide] [data-inbox-item="${ref}"]`)
    await expect(item.locator('[data-inbox-action="answer"]')).toHaveText(['Pears', 'Plums'])
    await item.locator('[data-inbox-action="answer"][data-action-label="Plums"]').click()
    budget(2 * LONG_MS)
    await expect(turnWith(page, 'stub chat: Plums')).toHaveAttribute('data-turn-outcome', 'done', { timeout: LONG_MS })
    await expect.poll(async () => (await own.api.session(id)).attention?.state ?? '', { timeout: LONG_MS }).not.toBe('needs_input')
    await expect(item.locator('[data-inbox-refusal]')).toHaveCount(0)
  } finally {
    await linkCtx?.close()
    await page.context().close()
  }
})

test('an answer the session refuses is said where it was pressed: the question answered first from another page', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  const other = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  const held: Route[] = []
  try {
    const { id } = await asked(page, 'e2e chat choice refused')
    // The other page's sidebar as the question stood: its live stream answered with the sessions as they are now (the question
    // open), and every later read of them held, so that its row still offers the choices once the question is answered.
    const listed = (await own.api.ok<{ sessions: unknown[] } | unknown[]>('GET', '/api/sessions'))
    const snapshot = Array.isArray(listed) ? listed : listed.sessions
    let frozen = false
    await other.route('**/api/events', (route) => {
      if (frozen) return void held.push(route)
      frozen = true
      void route.fulfill({ status: 200, headers: { 'content-type': 'text/event-stream' }, body: `event: snapshot\ndata: ${JSON.stringify(snapshot)}\n\n` })
    })
    await other.route('**/api/sessions', (route) => (frozen ? void held.push(route) : void route.fallback()))
    await other.reload()
    const choice = other.locator(`[data-sidebar-row="s:${id}"] [data-row-choice]`).filter({ hasText: 'Plums' })
    budget(2 * STEP_MS)
    await choice.scrollIntoViewIfNeeded()
    await expect(choice).toBeEnabled()
    // Answered from the session's page first.
    await page.locator('[data-quick-reply]').getByRole('button', { name: /Pears/ }).click()
    await answered(page, id, 'Pears')
    // Pressed on the other page's row: the session refuses it, and the page says so.
    await choice.click()
    budget(STEP_MS)
    // The toast in the page's own words for the session's refusal (replyFailure), its live region beside it.
    await expect(other.getByText('Already answered').first()).toBeVisible()
    // The words whole: a chat has no terminal, and a choice no line to keep, so they say neither.
    await expect(other.getByText("e2e chat choice refused's question was answered by Nate. Nothing was sent.", { exact: true }).first()).toBeVisible()
    await expect(turnWith(page, 'stub chat: Plums')).toHaveCount(0)
  } finally {
    for (const r of held) await r.abort().catch(() => undefined)
    await other.context().close()
    await page.context().close()
  }
})
