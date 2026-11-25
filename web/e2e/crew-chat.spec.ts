import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { dayPage, expect, logged, member, task, test, type Run } from './fixtures'
import { ownServer, type OwnServer } from './own-server'

// A crew's members as chats (docs/plans/chat-or-terminal.md, row 9), on a server of the spec's own: copies of the Claude Code stub
// with its chat declared (web/e2e/stub-agent.sh in its terminal, web/e2e/stub-protocol.mjs as its chat's child, which, as a
// managed run's worker, commits its file and submits the task's result), the stub's prompt as their prompt's shape. Every member
// starts in its terminal and moves to its chat at its prompt; each attempt's way is in the run's log, and a task is finished by its
// result alone. A member whose stub draws the trust question first is held there, answered on its session page, then moved, its
// prompt arriving in the chat. Nothing runs a real agent.
test.describe.configure({ mode: 'serial' })

/** The stub's prompt at rest, as the server recognises it (launch-first-message.spec.ts's). */
const STUB_PROMPT = String.raw`(?m)^> ?[^\n]*\s*\z`
const CHAT = { STUB_CLAUDE_VERSION: '2.1.331', STUB_NODE: process.execPath, STUB_PROTOCOL_WORKER: '1' }

let own: OwnServer
const runs: string[] = []

test.beforeAll(async () => {
  test.setTimeout(120_000)
  own = await ownServer('crew-chat', {
    agents: [
      { id: 'claude-chat', from: 'claude', env: CHAT, protocol: { name: 'claude-stream-json' }, promptShape: STUB_PROMPT },
      {
        id: 'claude-trust',
        from: 'claude',
        env: { ...CHAT, STUB_TRUST_DIALOG: '1' },
        protocol: { name: 'claude-stream-json' },
        promptShape: STUB_PROMPT,
        // The stub draws its folder's trust question as Codex does; this copy knows it by its words, as the catalog knows Claude Code's.
        trustPrompt: String.raw`Trust\s*this\s*folder\?`,
        trustAnswers: [
          { label: 'Yes, trust this folder', input: '\r' },
          { label: 'No, quit', input: '\u001b[B\r' },
        ],
      },
    ],
  })
})

test.afterAll(async () => {
  for (const id of runs) await own.api.stopRun(id)
  await own.stop()
})

/** A managed crew run as a chat: one work task per member. */
async function chatCrew(name: string, members: Array<{ name: string; agentId: string; task: string }>): Promise<string> {
  const body = {
    name,
    goal: 'ship it',
    cwd: own.repo,
    where: 'server',
    isolation: 'worktree',
    openAfterLaunch: false,
    way: 'chat',
    members: members.map((m) => ({ name: m.name, agentId: m.agentId, prompt: '', start: { when: 'task' } })),
    tasks: members.map((m) => ({ id: m.task, member: m.name, kind: 'work', instructions: `Do ${m.task}.` })),
  }
  const made = await own.api.ok<{ crew: { id: string; way?: string } }>('POST', '/api/crews', body)
  expect(made.crew.way).toBe('chat')
  return made.crew.id
}

/** The chat's arguments, as the stub's chat keeps them for its conversation. */
function chatArgv(conv: string): string[] {
  const path = join(own.home, '.stub-sessions', `${conv}.argv`)
  return existsSync(path) ? (JSON.parse(readFileSync(path, 'utf8')) as string[]) : []
}

/** What the stub's conversation holds: the chat's turns, "chat: <message>". */
function transcript(conv: string): string {
  const path = join(own.home, '.stub-sessions', `${conv}.txt`)
  return existsSync(path) ? readFileSync(path, 'utf8') : ''
}

test('a managed run of two chat members: each attempt runs as a chat, its task finished by its result alone', async () => {
  test.setTimeout(150_000)
  const api = own.api
  const crewId = await chatCrew('e2e chat crew', [
    { name: 'builder', agentId: 'claude-chat', task: 'build' },
    { name: 'writer', agentId: 'claude-chat', task: 'docs' },
  ])
  const run = await api.launchCrew(crewId)
  runs.push(run.id)
  let r: Run = run
  await expect
    .poll(async () => {
      r = await api.run(run.id)
      return ['build', 'docs'].map((id) => task(r, id).state)
    }, { timeout: 90_000, message: 'both tasks finished by their results' })
    .toEqual(['succeeded', 'succeeded'])
  for (const [m, t] of [['builder', 'build'], ['writer', 'docs']] as const) {
    expect(logged(r, `${m} (task ${t}, attempt 1): runs as a chat.`), `${m}'s way in the run's log`).toBe(true)
    expect(task(r, t).result?.summary).toBe(`stub ${m} did ${t} as a chat, attempt 1`)
  }
  expect(logged(r, 'in its terminal')).toBe(false)
  // Each chat started a new conversation by the recipe's start, with no permission mode of Conductor's: the member is not yolo.
  const conv = (await api.session(member(r, 'builder').sessionId ?? '')).agentSession?.id ?? ''
  const argv = chatArgv(conv)
  expect(argv).toContain('--session-id')
  expect(argv[argv.indexOf('--session-id') + 1]).toBe(conv)
  expect(argv).not.toContain('--resume')
  expect(argv).not.toContain('--permission-mode')
  expect(argv).toContain('--strict-mcp-config')
})

test('a member whose stub asks the trust question first is held, answered on its session page, then moved, its prompt in the chat', async ({ browser }) => {
  test.setTimeout(150_000)
  const api = own.api
  const crewId = await chatCrew('e2e chat trust', [{ name: 'solo', agentId: 'claude-trust', task: 'build' }])
  const run = await api.launchCrew(crewId)
  runs.push(run.id)
  let r: Run = run
  await expect
    .poll(async () => {
      r = await api.run(run.id)
      return member(r, 'solo').needsInput === true && logged(r, '; once answered, it moves to a chat at its prompt')
    }, { timeout: 30_000, message: 'held at the trust question, the run saying it moves once answered' })
    .toBe(true)
  const id = member(r, 'solo').sessionId ?? ''
  expect((await api.session(id)).way).toBeUndefined()
  const page = await dayPage(browser, { server: own.started })
  try {
    await page.goto(`/sessions/${encodeURIComponent(id)}`)
    const bar = page.locator('[data-quick-reply]')
    await bar.getByRole('button', { name: 'Yes, trust this folder' }).click({ timeout: 30_000 })
    // Moved once its prompt shows, in place: the same session, now a chat, its prompt the chat's first message.
    await expect.poll(async () => (await api.session(id)).way, { timeout: 30_000, message: 'the member moved to its chat' }).toBe('protocol')
    const conv = (await api.session(id)).agentSession?.id ?? ''
    await expect.poll(() => transcript(conv), { timeout: 30_000, message: 'its prompt in the chat' }).toContain('chat: Conductor task "build", attempt 1')
    await expect
      .poll(async () => {
        r = await api.run(run.id)
        return task(r, 'build').state
      }, { timeout: 60_000, message: 'its task finished by its result' })
      .toBe('succeeded')
    expect(logged(r, 'solo (task build, attempt 1): runs as a chat.')).toBe(true)
    expect(logged(r, "typed solo's prompt")).toBe(false)
  } finally {
    await page.context().close()
  }
})
