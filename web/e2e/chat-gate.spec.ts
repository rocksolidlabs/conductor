import { writeFileSync } from 'node:fs'
import type { Locator, Page } from '@playwright/test'
import { dayPage, expect, test } from './fixtures'
import { budget, chatServer, launch, LONG_MS, openSession, readStub, STEP_MS, stubFile, typeLine } from './chat-fixtures'
import type { OwnServer } from './own-server'

// The switch's offer of Chat and its marks (issue 1136), on a server of the spec's own with the chat's stub: the page offers Chat by
// the session's firstTurn, the one fact the move's route judges by. A first turn held at the stub's gate (its conversation already
// resumable, as the agent's prompt hook makes it) keeps Chat unpressable and its sentence shown for the whole of it, through a redraw
// the test causes; the turn's end offers Chat and takes the sentence away with no reload. Moves to Chat, the terminal and Chat again
// on the page's one connection mark each move once, in order, the terminal's among them, as a reload's replay draws them.
test.describe.configure({ mode: 'serial' })

const FIRST_REPLY = "Chat opens after the agent's first reply."
const TO_CHAT = 'Moved to Chat. Turns made in the terminal are not shown here. What the agent left running in its terminal was ended.'
const TO_TERMINAL = 'Moved to the terminal.'

let own: OwnServer
const sessions: string[] = []

test.beforeAll(async () => {
  test.setTimeout(120_000)
  own = await chatServer('chat-gate')
})

test.afterAll(async () => {
  for (const id of sessions) await own.api.stopSession(id)
  await own.stop()
})

const side = (page: Page, way: 'terminal' | 'protocol'): Locator => page.locator(`[data-way-switch] [data-way-side="${way}"]`)

test('a first turn held at the gate keeps Chat waiting with its sentence; its end offers Chat, the sentence gone, with no reload', async ({ browser }) => {
  const api = own.api
  const { s, conv } = await launch(api, sessions, 'e2e chat gate')
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  try {
    await openSession(page, s.id)
    const line = page.locator('[data-way-line]')
    budget(2 * STEP_MS)
    await expect(line).toHaveText(FIRST_REPLY)
    await expect(side(page, 'protocol')).toBeDisabled()
    await expect(page.locator(`[data-session-status="${s.id}"]`)).not.toHaveAttribute('data-status-mark', 'working')
    // The first line, its turn held at the stub's gate: the agent at work, its conversation resumable (the prompt hook's word), its
    // first reply not ended.
    await typeLine(page, 'stub hold')
    budget(3 * LONG_MS)
    await expect.poll(() => readStub(own, conv, 'hold').trim(), { timeout: 30_000 }).toBe('hold')
    await expect
      .poll(async () => {
        const info = await api.session(s.id)
        return `${info.attention?.state} ${!!info.agentSession?.resumable} ${!!info.firstTurn}`
      }, { timeout: 30_000 })
      .toBe('working true false')
    // Judged only once the page has drawn the held turn's own update (Chat is disabled before the turn too, a starting value): the
    // header's status says working, drawn from the attention the turn's report set.
    budget(5 * STEP_MS)
    await expect(page.locator(`[data-session-status="${s.id}"]`)).toHaveAttribute('data-status-mark', 'working')
    await expect(side(page, 'protocol')).toBeDisabled()
    await expect(line).toHaveText(FIRST_REPLY)
    // Again after a redraw the test causes (a narrower window): still waiting.
    await page.setViewportSize({ width: 1280, height: 860 })
    await expect(side(page, 'protocol')).toBeDisabled()
    await expect(line).toHaveText(FIRST_REPLY)
    // The gate opened, the turn ended: Chat offered, the sentence gone, no reload.
    writeFileSync(stubFile(own, conv, 'hold'), 'stop\n')
    budget(LONG_MS + 2 * STEP_MS)
    await expect.poll(async () => !!(await api.session(s.id)).firstTurn, { timeout: 30_000 }).toBe(true)
    await expect(side(page, 'protocol')).toBeEnabled()
    await expect(line).toHaveCount(0)
  } finally {
    await page.context().close()
  }
})

test('moves to Chat, the terminal and Chat again mark each move once, in order, on the open page', async ({ browser }) => {
  const api = own.api
  const { s } = await launch(api, sessions, 'e2e chat marks')
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  try {
    const view = await openSession(page, s.id)
    await typeLine(page, 'a first turn')
    budget(LONG_MS + STEP_MS)
    await expect.poll(async () => !!(await api.session(s.id)).firstTurn, { timeout: 30_000 }).toBe(true)
    const notices = page.locator('[data-chat-notice]')
    const press = async (to: 'terminal' | 'protocol') => {
      budget(2 * STEP_MS + LONG_MS)
      await side(page, to).click()
      await expect(view).toHaveAttribute('data-session-way', to, { timeout: 30_000 })
      await expect.poll(async () => (await api.session(s.id)).way ?? 'terminal', { timeout: 30_000 }).toBe(to)
    }
    await press('protocol')
    budget(STEP_MS)
    await expect(notices).toHaveText([TO_CHAT])
    await press('terminal')
    await press('protocol')
    budget(STEP_MS)
    await expect(notices).toHaveText([TO_CHAT, TO_TERMINAL, TO_CHAT])
    // A reload's replay draws the same.
    await page.reload()
    budget(LONG_MS + STEP_MS)
    await expect(page.locator('[data-session-way]').first()).toHaveAttribute('data-session-way', 'protocol', { timeout: 30_000 })
    await expect(notices).toHaveText([TO_CHAT, TO_TERMINAL, TO_CHAT])
  } finally {
    await page.context().close()
  }
})
