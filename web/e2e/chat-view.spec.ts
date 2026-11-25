import { rmSync } from 'node:fs'
import { dayPage, expect, test } from './fixtures'
import { chatServer, firstTurn, launch, openSession, readStub, release, say, stubFile, turnWith } from './chat-fixtures'
import type { OwnServer } from './own-server'

// A session run as a chat on its page (docs/plans/chat-or-terminal.md, row 7, design 11), against the chat's stub on a server of
// the spec's own: before the first reply the switch says why Chat waits; after a first turn in the terminal, the switch's Chat side
// moves it; in the chat a turn's words, its steps folded into one line that opens to a step and its output, a file change's diff and
// its editor tab, the turn's end; Stop on a turn held at the stub's gate ends it as interrupted; a message during a turn waits and is
// sent at its end; the box's help line is the session's verbatim's.
test.describe.configure({ mode: 'serial' })

let own: OwnServer
const sessions: string[] = []

test.beforeAll(async () => {
  test.setTimeout(120_000)
  own = await chatServer('chat-view')
})

test.afterAll(async () => {
  for (const id of sessions) await own.api.stopSession(id)
  await own.stop()
})

test("a terminal session's switch moves it to its chat, whose turn shows its words, folded steps, a file's diff and its end", async ({ browser }) => {
  test.setTimeout(180_000)
  const api = own.api
  const { s, conv } = await launch(api, sessions, 'e2e chat view')
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  try {
    const view = await openSession(page, s.id)
    // Before the first reply: Chat waits, in the plan's words, and cannot be pressed.
    const sw = page.locator('[data-way-switch]')
    await expect(sw).toHaveAttribute('data-way', 'terminal')
    await expect(page.locator('[data-way-line]')).toHaveText("Chat opens after the agent's first reply.")
    await expect(sw.locator('[data-way-side="protocol"]')).toBeDisabled()
    await firstTurn(page, api, s.id, 'remember PINEAPPLE')
    await expect(page.locator('[data-way-line]')).toHaveCount(0)
    // The switch moves it; the first press in this browser says the agent's terms decide how the mode is used.
    await sw.locator('[data-way-side="protocol"]').click()
    const terms = page.getByRole('region', { name: /^Notifications/ }).locator('li').filter({ hasText: "Chat runs the agent's own non-interactive mode with its existing sign-in" })
    await expect(terms).toBeVisible()
    await expect(view).toHaveAttribute('data-session-way', 'protocol', { timeout: 30_000 })
    await expect(sw).toHaveAttribute('data-way', 'protocol')
    const chat = page.locator('[data-chat-view]')
    await expect(chat).toBeVisible()
    await expect(chat.locator('[data-chat-notice]').first()).toContainText('Moved to Chat.')

    // The box's help line by the session's verbatim.
    const info = await api.session(s.id)
    await expect(chat.locator('[data-chat-help]')).toHaveAttribute('data-chat-help', info.verbatim ? 'verbatim' : 'commands')

    // A turn: the message, the agent's words, its steps folded into one line, the file it changed, the turn's end.
    await say(page, 'steps edit please')
    const turn = turnWith(page, 'stub chat: steps edit please')
    await expect(turn).toHaveAttribute('data-turn-outcome', 'done', { timeout: 30_000 })
    await expect(turn.locator('[data-chat-prompt-text]')).toHaveText('steps edit please')
    await expect(turn.locator('[data-chat-words]')).toContainText('stub chat: steps edit please')
    const line = turn.locator('[data-chat-steps-line]')
    await expect(line).toHaveText('3 steps')
    // Nothing of a step is on the surface until the line is opened: no tool's name, no count of tokens, no timing, no id.
    await expect(turn.locator('[data-tool-step]')).toHaveCount(0)
    expect(await turn.innerText()).not.toMatch(/toolu_|msg_|tokens?|\d+\s?ms\b|\d+(\.\d+)?\s?s\b/)
    await line.click()
    const rows = turn.locator('[data-tool-step]')
    await expect(rows).toHaveCount(3)
    await expect(rows.nth(0).locator('[data-tool-name]')).toHaveText('Bash')
    await expect(rows.nth(0).locator('[data-tool-summary]')).toHaveText('ls')
    await rows.nth(0).locator('[data-tool-step-row]').click()
    await expect(rows.nth(0).locator('[data-tool-output]')).toHaveText('notes.txt')
    // The file change: one line that opens to its diff, and from there the editor's diff tab.
    const change = turn.locator('[data-file-change]')
    await expect(change.locator('[data-file-change-line]')).toContainText('notes.txt')
    await expect(change.locator('[data-file-change-line]')).toContainText('changed.')
    await change.locator('[data-file-change-line]').click()
    await expect(change.locator('[data-inline-diff]')).toContainText('+gamma')
    await change.locator('[data-file-change-open-diff]').click()
    await expect(page.locator('[data-editor-tab]').filter({ hasText: 'notes.txt' })).toBeVisible({ timeout: 15_000 })
  } finally {
    await page.context().close()
  }
  expect(readStub(own, conv, 'txt')).toContain('remember PINEAPPLE')
})

test('Stop ends a turn held at the gate as interrupted, and a message sent during a turn waits and is sent at its end', async ({ browser }) => {
  test.setTimeout(180_000)
  const api = own.api
  const { s, conv } = await launch(api, sessions, 'e2e chat stop')
  const page = await dayPage(browser, { server: own.started })
  try {
    const view = await openSession(page, s.id)
    await firstTurn(page, api, s.id)
    await page.locator('[data-way-side="protocol"]').click()
    await expect(view).toHaveAttribute('data-session-way', 'protocol', { timeout: 30_000 })
    const chat = page.locator('[data-chat-view]')

    // A turn held at the stub's gate: working, its steps' line says so; Stop ends it, interrupted.
    await say(page, 'hold the first')
    await expect.poll(() => readStub(own, conv, 'held'), { timeout: 30_000 }).toContain('held')
    await expect(chat).toHaveAttribute('data-turn-running', 'true', { timeout: 30_000 })
    await expect(turnWith(page, 'hold the first').locator('[data-chat-steps-line]')).toHaveText('Working.')
    await chat.locator('[data-chat-stop]').click()
    const stopped = turnWith(page, 'hold the first')
    await expect(stopped).toHaveAttribute('data-turn-outcome', 'interrupted', { timeout: 30_000 })
    await expect(stopped.locator('[data-chat-turn-stopped]')).toHaveText('The turn was stopped.')
    await expect(chat.locator('[data-chat-stop]')).toHaveCount(0)

    // Another held turn, and a message written during it: it waits, so said, and is sent once the turn ends.
    rmSync(stubFile(own, conv, 'held'), { force: true })
    await say(page, 'hold the second')
    await expect.poll(() => readStub(own, conv, 'held'), { timeout: 30_000 }).toContain('held')
    await expect(chat).toHaveAttribute('data-turn-running', 'true', { timeout: 30_000 })
    await say(page, 'after the hold')
    const waiting = chat.locator('[data-chat-prompt]').filter({ hasText: 'after the hold' })
    await expect(waiting.locator('[data-chat-prompt-line]')).toHaveText('Sends when the turn ends.', { timeout: 30_000 })
    release(own, conv)
    await expect(turnWith(page, 'stub chat: hold the second')).toHaveAttribute('data-turn-outcome', 'done', { timeout: 30_000 })
    const after = turnWith(page, 'stub chat: after the hold')
    await expect(after).toHaveAttribute('data-turn-outcome', 'done', { timeout: 30_000 })
    await expect(waiting.locator('[data-chat-prompt-line]')).toHaveCount(0)
  } finally {
    await page.context().close()
  }
})
