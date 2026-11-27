import { dayPage, expect, test, type Session } from './fixtures'
import { budget, firstTurn, LONG_MS, openSession, readStub, say, STEP_MS, turnWith } from './chat-fixtures'
import { ownServer, type OwnServer } from './own-server'

// A Codex session as a chat (docs/plans/chat-or-terminal.md, row 10), on a server of the spec's own: a copy of the Codex stub with its
// chat declared (codex-proto: web/e2e/stub-agent.sh in its terminal, the protocol stub's Codex mode, web/e2e/stub-protocol-codex.mjs,
// as its chat's child, run by this suite's node, one transcript under the server's $HOME/.stub-sessions by thread), which no other
// spec's server offers. Codex moves between ways only once a thread handed from its terminal to its app server and back has been
// observed by hand: until Codex's declaration sets codexMoves (internal/agentproto/codex.go) and CODEX_MOVES with it, a Codex session
// stays in its terminal, and the chat's walk waits (fixme). Each step raises the test's timeout by its own waits' caps (budget).
const CODEX_MOVES = false

test.describe.configure({ mode: 'serial' })

let own: OwnServer
const sessions: string[] = []

test.beforeAll(async () => {
  test.setTimeout(120_000)
  own = await ownServer('chat-codex', {
    agents: [{ id: 'codex-proto', from: 'codex', env: { STUB_CODEX_VERSION: '0.167.2', STUB_NODE: process.execPath }, protocol: { name: 'codex-app-server' } }],
  })
})

test.afterAll(async () => {
  for (const id of sessions) await own.api.stopSession(id)
  await own.stop()
})

/** A Codex session of the stub's: Codex names its thread by its hooks, after its first turn. */
async function launch(name: string): Promise<Session> {
  const s = await own.api.ok<Session>('POST', '/api/sessions', { agentId: 'codex-proto', name })
  sessions.push(s.id)
  return s
}

/** The thread the session's terminal reported (its first turn's). */
async function threadOf(id: string): Promise<string> {
  const thread = (await own.api.session(id)).agentSession?.id ?? ''
  expect(thread).not.toBe('')
  return thread
}

test('a Codex session stays in its terminal until its move between ways is observed', async ({ browser }) => {
  test.skip(CODEX_MOVES, 'Codex moves: its chat is walked below')
  const s = await launch('e2e codex stays')
  const page = await dayPage(browser, { server: own.started })
  try {
    const view = await openSession(page, s.id)
    await firstTurn(page, own.api, s.id)
    budget(2 * STEP_MS)
    const r = await own.api.call<{ error?: { code: string; message: string } }>('POST', `/api/sessions/${s.id}/way`, { to: 'protocol' })
    expect(r.status).toBe(409)
    expect(r.body.error).toEqual({ code: 'move_refused', message: 'This agent cannot move its conversation yet.' })
    await expect(view).toHaveAttribute('data-session-way', 'terminal')
    expect((await own.api.session(s.id)).way ?? 'terminal').toBe('terminal')
  } finally {
    await page.context().close()
  }
})

test('a Codex session runs as a chat: its box says what a / may do, a message is answered once, an approval is the operator\'s, Stop ends a held turn', async ({ browser }) => {
  test.fixme(!CODEX_MOVES, "Codex's move between ways waits for its by-hand observation (row 10)")
  const s = await launch('e2e codex chat')
  const page = await dayPage(browser, { server: own.started })
  try {
    const view = await openSession(page, s.id)
    await firstTurn(page, own.api, s.id)
    const thread = await threadOf(s.id)
    budget(STEP_MS + LONG_MS)
    await page.locator('[data-way-side="protocol"]').click()
    await expect(view).toHaveAttribute('data-session-way', 'protocol', { timeout: LONG_MS })
    const chat = page.locator('[data-chat-view]')

    // Codex claims no as-written mark: the box says a message starting with / may run as the agent's commands.
    await expect(chat.locator('[data-chat-help="commands"]')).toBeVisible({ timeout: LONG_MS })
    await expect(chat.locator('[data-chat-help="verbatim"]')).toHaveCount(0)

    // A message: answered in the app server's words, delivered once.
    await say(page, 'hello codex')
    budget(2 * LONG_MS)
    await expect(turnWith(page, 'stub chat: hello codex')).toHaveAttribute('data-turn-outcome', 'done', { timeout: LONG_MS })
    expect(readStub(own, thread, 'txt').split('\n').filter((l) => l === 'chat: hello codex')).toHaveLength(1)

    // An approval: the session's question, in plain words with Codex's tool; Allow once reaches the app server as accept.
    await say(page, 'ask command')
    budget(6 * STEP_MS + 3 * LONG_MS)
    const q = chat.locator('[data-permission-question][data-permission-state="open"]')
    await expect(q).toBeVisible({ timeout: LONG_MS })
    await expect(q.locator('[data-permission-words]')).toHaveText('Run a command?')
    await expect(q.locator('[data-permission-tool]')).toHaveText('commandExecution')
    await q.locator('[data-permission-allow]').click()
    await expect.poll(() => readStub(own, thread, 'allowed'), { timeout: LONG_MS }).not.toBe('')
    await expect(chat.locator('[data-permission-question]').first()).toHaveAttribute('data-permission-state', 'allowed', { timeout: LONG_MS })
    await expect(turnWith(page, 'stub chat: ask command')).toHaveAttribute('data-turn-outcome', 'done', { timeout: LONG_MS })

    // A turn held at the stub's gate: Stop ends it, interrupted (turn/interrupt).
    await say(page, 'hold here')
    budget(3 * STEP_MS + 2 * LONG_MS)
    await expect.poll(() => readStub(own, thread, 'held'), { timeout: LONG_MS }).toContain('held')
    await expect(chat).toHaveAttribute('data-turn-running', 'true', { timeout: LONG_MS })
    await chat.locator('[data-chat-stop]').click()
    await expect(turnWith(page, 'hold here')).toHaveAttribute('data-turn-outcome', 'interrupted', { timeout: LONG_MS })
  } finally {
    await page.context().close()
  }
})
