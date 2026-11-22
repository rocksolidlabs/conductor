import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, test, type Session } from './fixtures'

/** The bounds and the words as the owner has them (proto.MaxSubmit, MaxControl, ControlTooLargeWords): the page says the same. */
const MAX_SUBMIT = 4096
const MAX_CONTROL = 8 * 1024
const CONTROL_TOO_LARGE = /\bControlTooLargeWords = "([^"]+)"/.exec(readFileSync(fileURLToPath(new URL('../../internal/proto/control.go', import.meta.url)), 'utf8'))![1]!

// A line within the 4096 bytes a reply takes can pass the CONTROL frame's 8 KiB once JSON encodes it (docs/protocol.md, Error
// codes). Typed in the session's reply bar, it is refused by the page with the owner's words, no frame past the bound sent and the
// connection kept; a line that fits is sent after it, as before, and reaches the agent.
test('a reply line past the control bound is refused with words, and a line that fits goes after it', async ({ page, api, state }) => {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e control bound' })
  const transcript = async () => {
    const id = (await api.session(s.id)).agentSession?.id ?? ''
    const file = join(state.home, '.stub-sessions', `${id}.txt`)
    return id && existsSync(file) ? readFileSync(file, 'utf8') : ''
  }
  // The size of every frame the page sends, and how many connections it opened.
  const sent: number[] = []
  let sockets = 0
  page.on('websocket', (ws) => {
    sockets++
    ws.on('framesent', (f) => sent.push(typeof f.payload === 'string' ? Buffer.byteLength(f.payload) : f.payload.length))
  })
  try {
    await api.ok('POST', `/api/sessions/${s.id}/attention`, { state: 'needs_input', message: 'Which branch?' })
    await page.goto(`/sessions/${encodeURIComponent(s.id)}`)
    const bar = page.locator('[data-quick-reply]')
    await expect(bar).toContainText('Which branch?', { timeout: 30_000 })
    const field = bar.locator('input')
    await field.fill('"'.repeat(MAX_SUBMIT))
    const before = sockets
    await field.press('Enter')
    const toast = page.locator('li').filter({ hasText: 'Not sent' }).last()
    await expect(toast).toContainText(CONTROL_TOO_LARGE, { timeout: 15_000 })
    expect(sent.filter((n) => n > 1 + MAX_CONTROL), 'no frame past the bound left the page').toEqual([])
    expect(sockets, 'the connection was kept').toBe(before)
    // The question still waits, and a line that fits answers it.
    await expect(bar).toContainText('Which branch?')
    await field.fill('main-after-the-refusal')
    await field.press('Enter')
    await expect.poll(transcript, { timeout: 15_000, message: 'the line that fits reached the agent' }).toContain('main-after-the-refusal')
  } finally {
    await api.stopSession(s.id)
  }
})
