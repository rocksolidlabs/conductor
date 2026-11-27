import { rmSync } from 'node:fs'
import { dayPage, expect, test } from './fixtures'
import { budget, chatServer, firstTurn, launch, LONG_MS, openSession, readStub, release, say, STEP_MS, stubFile, turnWith } from './chat-fixtures'
import type { OwnServer } from './own-server'

// A session run as a chat on its page (docs/plans/chat-or-terminal.md, row 7, design 11), against the chat's stub on a server of
// the spec's own: before the first reply the switch says why Chat waits; after a first turn in the terminal, the switch's Chat side
// moves it; in the chat a turn's words, its steps folded into one line that opens to a step and its output, a file change's diff and
// its editor tab, the turn's end; Stop on a turn held at the stub's gate ends it as interrupted; a message during a turn waits and is
// sent at its end; the box's help line is the session's verbatim's; the first-Chat note sits in the chat, covering nothing; a move
// whose start fails goes back and the switch says why; below xl People opens as the sheet. Each step raises the test's timeout by its
// own waits' caps (budget), so a loaded machine spends its time and no fixed bound cuts a walk short.
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
  const api = own.api
  const { s, conv } = await launch(api, sessions, 'e2e chat view')
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  try {
    const view = await openSession(page, s.id)
    budget(3 * STEP_MS)
    // Before the first reply: Chat waits, in the plan's words on its dimmed side (no line above the terminal: UX3 R15), and cannot
    // be pressed.
    const sw = page.locator('[data-way-switch]')
    await expect(sw).toHaveAttribute('data-way', 'terminal')
    await expect(sw.locator('[data-way-side="protocol"]')).toHaveAttribute('aria-description', "Chat opens after the agent's first reply.")
    await expect(page.locator('[data-way-line]')).toHaveCount(0)
    await expect(sw.locator('[data-way-side="protocol"]')).toBeDisabled()
    await firstTurn(page, api, s.id, 'remember PINEAPPLE')
    budget(12 * STEP_MS + LONG_MS)
    await expect(sw.locator('[data-way-side="protocol"]')).not.toHaveAttribute('aria-description', "Chat opens after the agent's first reply.")
    await expect(page.locator('[data-way-line]')).toHaveCount(0)
    // The switch moves it. The first press in this browser draws one note in the chat, under the move's notice, where it covers
    // nothing: the agent's terms decide how the mode is used, and under it that the chat has Conductor's tools only.
    await sw.locator('[data-way-side="protocol"]').click()
    await expect(view).toHaveAttribute('data-session-way', 'protocol', { timeout: LONG_MS })
    await expect(sw).toHaveAttribute('data-way', 'protocol')
    const chat = page.locator('[data-chat-view]')
    await expect(chat).toBeVisible()
    await expect(chat.locator('[data-chat-notice]').first()).toContainText('Moved to Chat.')
    const note = chat.locator('[data-chat-terms]')
    await expect(note.locator('[data-chat-terms-first]')).toHaveText("Chat runs the agent's own non-interactive mode with its existing sign-in; the agent's terms decide how that may be used and counted.")
    await expect(note.locator('[data-chat-terms-second]')).toHaveText("In Chat the agent has Conductor's tools only; your own tool servers stay in its terminal.")
    expect(await page.getByRole('region', { name: /^Notifications/ }).locator('li').count()).toBe(0)
    // It covers nothing: the box's Send is clear of it, and the note lies under the move's notice.
    const [n, send, notice] = await Promise.all([note.boundingBox(), chat.locator('[data-chat-send]').boundingBox(), chat.locator('[data-chat-notice]').first().boundingBox()])
    expect(n!.y + n!.height <= send!.y || send!.y + send!.height <= n!.y || n!.x + n!.width <= send!.x || send!.x + send!.width <= n!.x).toBe(true)
    expect(n!.y).toBeGreaterThanOrEqual(notice!.y + notice!.height)
    await note.locator('[data-chat-terms-close]').click()
    await expect(note).toHaveCount(0)
    // Shown once: a reload of the chat draws no note.
    await page.reload()
    await expect(page.locator('[data-session-way]').first()).toHaveAttribute('data-welcomed', 'true', { timeout: LONG_MS })
    await expect(page.locator('[data-chat-view] [data-chat-notice]').first()).toBeVisible()
    await expect(page.locator('[data-chat-terms]')).toHaveCount(0)

    // The operator's box, which the server lets expand a line (UX3 R10): its placeholder names @ and /, and a leading / draws no
    // plain-text line, at rest or typed (UX3 R4's line is for a box that sends as written).
    const field = chat.locator('[data-chat-box] textarea')
    await expect(field).toHaveAttribute('placeholder', /· @ for a file, \/ for a command$/)
    await expect(chat.locator('[data-chat-help]')).toHaveCount(0)
    await field.fill('/compact')
    await expect(field).toHaveValue('/compact')
    await expect(chat.locator('[data-chat-help]')).toHaveCount(0)
    await field.fill('')

    // A turn: the message, the agent's words, its steps folded into one line, the file it changed, the turn's end.
    await say(page, 'steps edit please')
    budget(16 * STEP_MS + LONG_MS)
    const turn = turnWith(page, 'stub chat: steps edit please')
    await expect(turn).toHaveAttribute('data-turn-outcome', 'done', { timeout: LONG_MS })
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
    // The file change: one line with its counts and, beside it, the editor's diff tab in one press (UX3 R9); opened, its diff.
    const change = turn.locator('[data-file-change]')
    await expect(change.locator('[data-file-change-line]')).toContainText('notes.txt')
    await expect(change.locator('[data-file-change-line]')).toContainText('changed')
    await expect(change.locator('[data-file-change-line]')).not.toContainText('changed.')
    await expect(change.locator('[data-file-change-counts]')).toHaveText(/^·\s*\+1$/)
    await expect(change.locator('[data-inline-diff]')).toHaveCount(0)
    await change.locator('[data-file-change-open-diff]').click()
    await expect(page.locator('[data-editor-tab]').filter({ hasText: 'notes.txt' })).toBeVisible({ timeout: STEP_MS })
    await change.locator('[data-file-change-line]').click()
    await expect(change.locator('[data-inline-diff]')).toContainText('+gamma')
  } finally {
    await page.context().close()
  }
  expect(readStub(own, conv, 'txt')).toContain('remember PINEAPPLE')
})

test('Stop ends a turn held at the gate as interrupted, and a message sent during a turn waits and is sent at its end', async ({ browser }) => {
  const api = own.api
  const { s, conv } = await launch(api, sessions, 'e2e chat stop')
  const page = await dayPage(browser, { server: own.started })
  try {
    const view = await openSession(page, s.id)
    await firstTurn(page, api, s.id)
    budget(STEP_MS + LONG_MS)
    await page.locator('[data-way-side="protocol"]').click()
    await expect(view).toHaveAttribute('data-session-way', 'protocol', { timeout: LONG_MS })
    const chat = page.locator('[data-chat-view]')

    // A turn held at the stub's gate: working, its steps' line says so, and nothing is said above the pane of a move nobody pressed;
    // Stop ends it, interrupted.
    await say(page, 'hold the first')
    budget(5 * STEP_MS + 3 * LONG_MS)
    await expect.poll(() => readStub(own, conv, 'held'), { timeout: LONG_MS }).toContain('held')
    await expect(chat).toHaveAttribute('data-turn-running', 'true', { timeout: LONG_MS })
    await expect(turnWith(page, 'hold the first').locator('[data-chat-steps-line]')).toHaveText('Working.')
    await expect(page.locator('[data-way-line]')).toHaveCount(0)
    await chat.locator('[data-chat-stop]').click()
    const stopped = turnWith(page, 'hold the first')
    await expect(stopped).toHaveAttribute('data-turn-outcome', 'interrupted', { timeout: LONG_MS })
    await expect(stopped.locator('[data-chat-turn-stopped]')).toHaveText('The turn was stopped.')
    await expect(chat.locator('[data-chat-stop]')).toHaveCount(0)

    // Another held turn, and a message written during it: it waits, so said, and is sent once the turn ends.
    rmSync(stubFile(own, conv, 'held'), { force: true })
    await say(page, 'hold the second')
    budget(2 * LONG_MS)
    await expect.poll(() => readStub(own, conv, 'held'), { timeout: LONG_MS }).toContain('held')
    await expect(chat).toHaveAttribute('data-turn-running', 'true', { timeout: LONG_MS })
    await say(page, 'after the hold')
    budget(STEP_MS + 3 * LONG_MS)
    const waiting = chat.locator('[data-chat-prompt]').filter({ hasText: 'after the hold' })
    await expect(waiting.locator('[data-chat-prompt-line]')).toHaveText('Sends when the turn ends.', { timeout: LONG_MS })
    release(own, conv)
    await expect(turnWith(page, 'stub chat: hold the second')).toHaveAttribute('data-turn-outcome', 'done', { timeout: LONG_MS })
    const after = turnWith(page, 'stub chat: after the hold')
    await expect(after).toHaveAttribute('data-turn-outcome', 'done', { timeout: LONG_MS })
    await expect(waiting.locator('[data-chat-prompt-line]')).toHaveCount(0)
  } finally {
    await page.context().close()
  }
})

test('a move whose chat does not start goes back, and the switch says why rather than "moving"', async ({ browser }) => {
  const api = own.api
  const { s } = await launch(api, sessions, 'e2e chat goes back', 'claude-bad')
  const page = await dayPage(browser, { server: own.started })
  try {
    const view = await openSession(page, s.id)
    await firstTurn(page, api, s.id)
    budget(4 * STEP_MS + 2 * LONG_MS)
    await page.locator('[data-way-side="protocol"]').click()
    // The start fails, the session goes back to its terminal on the same way, and the line says why; it never stays on "moving".
    const line = page.locator('[data-way-line]')
    await expect(line).toBeVisible({ timeout: LONG_MS })
    await expect(line).not.toHaveText('The session is moving; try again in a moment.', { timeout: LONG_MS })
    await expect(line).toHaveText(/\S/)
    await expect(view).toHaveAttribute('data-session-way', 'terminal')
    await expect(page.locator('[data-way-side="protocol"]')).toBeEnabled()
  } finally {
    await page.context().close()
  }
})

test("below xl People is the sheet: ?tab=people, the palette's Open People and the header's button open the people's chat", async ({ browser }) => {
  const api = own.api
  const { s } = await launch(api, sessions, 'e2e chat people narrow')
  const page = await dayPage(browser, { server: own.started, width: 1024, height: 800 })
  try {
    await page.goto(`/sessions/${encodeURIComponent(s.id)}?tab=people`)
    budget(8 * STEP_MS)
    const sheet = page.locator('[data-chat-sheet]')
    await expect(sheet).toBeVisible({ timeout: STEP_MS })
    await page.keyboard.press('Escape')
    await expect(sheet).toHaveCount(0)
    await expect(page.locator('#dashboard-sidebar-main')).toBeAttached()
    await page.keyboard.press('Alt+/')
    const palette = page.locator('[data-palette]')
    await expect(palette).toBeVisible()
    await palette.locator('[data-palette-row="page:view:people"]').click()
    await expect(sheet).toBeVisible()
    await expect(page.getByRole('dialog').filter({ has: sheet })).toContainText('People')
  } finally {
    await page.context().close()
  }
})
