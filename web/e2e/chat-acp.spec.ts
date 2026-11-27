import { dayPage, expect, test, type Session } from './fixtures'
import { budget, firstTurn, LONG_MS, openSession, readStub, say, STEP_MS, turnWith } from './chat-fixtures'
import { ownServer, type OwnServer } from './own-server'

// A session of an Agent Client Protocol agent as a chat (docs/plans/chat-or-terminal.md, row 11), on a server of the spec's own:
// a copy of the terminal stub with the protocol declared (acp-proto: web/e2e/stub-agent.sh in its terminal, the protocol stub's
// third mode, web/e2e/stub-protocol-acp.mjs, as its chat's child, run by this suite's node, one transcript under the server's
// $HOME/.stub-sessions by session), which no other spec's server offers. An ACP agent's session moves between ways only once its
// session id through the protocol has been shown, by hand, to be its terminal's own: until an agent's declaration sets it
// (acpMoves, internal/agentproto/acp.go) and ACP_MOVES with it, a session stays in its terminal, and the chat's walk waits
// (fixme). Each step raises the test's timeout by its own waits' caps (budget).
const ACP_MOVES = false

test.describe.configure({ mode: 'serial' })

let own: OwnServer
const sessions: string[] = []

test.beforeAll(async () => {
  test.setTimeout(120_000)
  own = await ownServer('chat-acp', {
    agents: [{ id: 'acp-proto', from: 'claude', env: { STUB_CLAUDE_VERSION: '2.1.331', STUB_NODE: process.execPath, STUB_ACP_OPTIONS: 'opencode' }, protocol: { name: 'acp-v1', args: ['acp'] } }],
  })
})

test.afterAll(async () => {
  for (const id of sessions) await own.api.stopSession(id)
  await own.stop()
})

async function launch(name: string): Promise<{ s: Session; conv: string }> {
  const s = await own.api.ok<Session>('POST', '/api/sessions', { agentId: 'acp-proto', name })
  sessions.push(s.id)
  const conv = s.agentSession?.id ?? ''
  expect(conv).not.toBe('')
  return { s, conv }
}

test('an ACP agent\'s session stays in its terminal until its move between ways is observed', async ({ browser }) => {
  test.skip(ACP_MOVES, 'ACP sessions move: the chat is walked below')
  const { s } = await launch('e2e acp stays')
  const page = await dayPage(browser, { server: own.started })
  try {
    const view = await openSession(page, s.id)
    await firstTurn(page, own.api, s.id)
    budget(2 * STEP_MS)
    const r = await own.api.call<{ error?: { code: string; message: string } }>('POST', `/api/sessions/${s.id}/way`, { to: 'protocol' })
    expect(r.status).toBe(409)
    expect(r.body.error).toEqual({ code: 'move_refused', message: 'This agent cannot move its conversation yet.' })
    await expect(view).toHaveAttribute('data-session-way', 'terminal')
  } finally {
    await page.context().close()
  }
})

test('an ACP agent\'s session runs as a chat: its box says what a / may do, a message is answered once, Allow once selects the agent\'s own option, Stop cancels the turn', async ({ browser }) => {
  test.fixme(!ACP_MOVES, "an ACP agent's move between ways waits for its by-hand observation (row 11)")
  const { s, conv } = await launch('e2e acp chat')
  const page = await dayPage(browser, { server: own.started })
  try {
    const view = await openSession(page, s.id)
    await firstTurn(page, own.api, s.id)
    budget(STEP_MS + LONG_MS)
    await page.locator('[data-way-side="protocol"]').click()
    await expect(view).toHaveAttribute('data-session-way', 'protocol', { timeout: LONG_MS })
    const chat = page.locator('[data-chat-view]')

    await expect(chat.locator('[data-chat-help="commands"]')).toBeVisible({ timeout: LONG_MS })
    await expect(chat.locator('[data-chat-help="verbatim"]')).toHaveCount(0)

    await say(page, 'hello acp')
    budget(2 * LONG_MS)
    await expect(turnWith(page, 'stub chat: hello acp')).toHaveAttribute('data-turn-outcome', 'done', { timeout: LONG_MS })
    expect(readStub(own, conv, 'txt').split('\n').filter((l) => l === 'chat: hello acp')).toHaveLength(1)

    await say(page, 'ask')
    budget(6 * STEP_MS + 3 * LONG_MS)
    const q = chat.locator('[data-permission-question][data-permission-state="open"]')
    await expect(q).toBeVisible({ timeout: LONG_MS })
    await expect(q.locator('[data-permission-words]')).toHaveText('Use a tool?')
    await q.locator('[data-permission-allow]').click()
    await expect.poll(() => readStub(own, conv, 'outcome'), { timeout: LONG_MS }).toContain('"optionId":"once"')
    await expect(turnWith(page, 'stub chat: ask')).toHaveAttribute('data-turn-outcome', 'done', { timeout: LONG_MS })

    await say(page, 'hold here')
    budget(3 * STEP_MS + 2 * LONG_MS)
    await expect.poll(() => readStub(own, conv, 'held'), { timeout: LONG_MS }).toContain('held')
    await chat.locator('[data-chat-stop]').click()
    await expect(turnWith(page, 'hold here')).toHaveAttribute('data-turn-outcome', 'interrupted', { timeout: LONG_MS })
  } finally {
    await page.context().close()
  }
})
