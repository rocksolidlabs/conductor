import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, rowAnswer, test, type Session } from './fixtures'

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
    // The line not sent stays in the field, whole, to shorten (#654).
    await expect(field).toHaveValue('"'.repeat(MAX_SUBMIT))
    expect(sent.filter((n) => n > 1 + MAX_CONTROL), 'no frame past the bound left the page').toEqual([])
    expect(sockets, 'the connection was kept').toBe(before)
    // The question still waits, and a line that fits answers it.
    await expect(bar).toContainText('Which branch?')
    await field.fill('main-after-the-refusal')
    await field.press('Enter')
    await expect.poll(transcript, { timeout: 15_000, message: 'the line that fits reached the agent' }).toContain('main-after-the-refusal')
    // A line sent does not stay: the field empties once the owner took it, or the bar goes with the question it answered.
    const kept = async () => ((await field.count()) ? await field.inputValue({ timeout: 1_000 }).catch(() => '') : '')
    await expect.poll(kept, { timeout: 15_000 }).toBe('')
  } finally {
    await api.stopSession(s.id)
  }
})

// A line refused because its question was answered stays in the reply bar, bound to that question: pressed again without a change,
// quickly, it is refused again and never answers the question that came next; changed, it is bound to the question shown then, and
// answers it.
test('a kept reply line stays bound to the question it was typed for until the person changes it', async ({ page, api, state }) => {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e kept line' })
  const transcript = async () => {
    const id = (await api.session(s.id)).agentSession?.id ?? ''
    const file = join(state.home, '.stub-sessions', `${id}.txt`)
    return id && existsSync(file) ? readFileSync(file, 'utf8') : ''
  }
  // The owner's refusals the page is told, as they come on its connection (watched before the page opens it).
  let refusals = 0
  page.on('websocket', (ws) =>
    ws.on('framereceived', (f) => {
      if ((typeof f.payload === 'string' ? f.payload : f.payload.toString('latin1')).includes('"question_answered"')) refusals++
    }),
  )
  try {
    await api.ok('POST', `/api/sessions/${s.id}/attention`, { state: 'needs_input', message: 'Which branch?' })
    await page.goto(`/sessions/${encodeURIComponent(s.id)}`)
    const bar = page.locator('[data-quick-reply]')
    await expect(bar).toContainText('Which branch?', { timeout: 30_000 })
    const field = bar.locator('input')
    await field.fill('kept-draft')
    // The question is answered elsewhere, and the next one comes while the line waits in the field.
    await api.ok('POST', `/api/sessions/${s.id}/attention`, { state: 'working' })
    await api.ok('POST', `/api/sessions/${s.id}/attention`, { state: 'needs_input', message: 'Which file?' })
    await expect(bar).toContainText('Which file?', { timeout: 15_000 })
    // Three quick presses, and one more once the refusal is said: none of them is a change.
    for (let i = 0; i < 3; i++) await field.press('Enter')
    const refused = page.locator('li').filter({ hasText: 'Already answered' }).last()
    await expect(refused).toContainText('Your line is kept: change it to send it for what is asked now.', { timeout: 15_000 })
    await expect(field).toHaveValue('kept-draft')
    const before = refusals
    await field.press('Enter')
    await expect.poll(() => refusals, { timeout: 15_000, message: 'the unchanged line is refused again' }).toBeGreaterThan(before)
    await expect(field).toHaveValue('kept-draft')
    // Changed, the line is bound to the question shown now and answers it.
    await field.press('End')
    await field.pressSequentially(' now')
    await field.press('Enter')
    await expect.poll(transcript, { timeout: 15_000, message: 'the changed line answered the question shown' }).toContain('kept-draft now')
    // The owner takes a connection's lines in order: the unchanged presses, before it, never answered the next question.
    expect((await transcript()).split('kept-draft').length - 1, 'the line went to the agent once, changed').toBe(1)
  } finally {
    await api.stopSession(s.id)
  }
})

// A sidebar row's line refused because its question was answered is kept by the sidebar, past the field that drew it (cleared with
// the question): when the next question comes, the row's field shows it again, bound to the question it was typed for, so that
// Enter is refused again and answers nothing; changed, it answers the question shown.
test("a sidebar row's kept line comes back with the next question, bound to the one it was typed for", async ({ page, api, state }) => {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e kept row line' })
  const transcript = async () => {
    const id = (await api.session(s.id)).agentSession?.id ?? ''
    const file = join(state.home, '.stub-sessions', `${id}.txt`)
    return id && existsSync(file) ? readFileSync(file, 'utf8') : ''
  }
  const row = page.locator(`[data-session-list="sidebar"] [data-sidebar-row="s:${s.id}"]`).first()
  const rowField = async () => (await rowAnswer(page, row)).locator('[data-row-answer] input')
  const answered = page.locator('li').filter({ hasText: 'Already answered' })
  // A row's reply goes on a short connection of its own, closed once the owner's word on it came back: count those closed.
  let replied = 0
  page.on('websocket', (ws) => ws.on('close', () => replied++))
  try {
    await api.ok('POST', `/api/sessions/${s.id}/attention`, { state: 'needs_input', message: 'Which branch?' })
    await page.goto(`/sessions/${encodeURIComponent(s.id)}`)
    await expect(row).toContainText('Which branch?', { timeout: 30_000 })
    let field = await rowField()
    await field.fill('row-draft')
    // Answered elsewhere: the next question takes the first one's place while the line waits in the row's field.
    await api.ok('POST', `/api/sessions/${s.id}/attention`, { state: 'needs_input', message: 'Which file?' })
    await expect(row).toContainText('Which file?', { timeout: 15_000 })
    await field.press('Enter')
    await expect(answered.last()).toContainText('Your line is kept: change it to send it for what is asked now.', { timeout: 15_000 })
    // The row asks nothing for a while (its field goes with its prompt), then the next question comes: the row's field shows the
    // line again, still bound to the first question.
    await api.ok('POST', `/api/sessions/${s.id}/attention`, { state: 'working' })
    await expect(row.locator('[data-row-prompt]')).toHaveCount(0, { timeout: 15_000 })
    await expect(page.locator('[data-row-answer]')).toHaveCount(0)
    await api.ok('POST', `/api/sessions/${s.id}/attention`, { state: 'needs_input', message: 'Which path?' })
    await expect(row).toContainText('Which path?', { timeout: 15_000 })
    field = await rowField()
    await expect(field).toHaveValue('row-draft')
    const before = replied
    await field.press('Enter')
    await expect.poll(() => replied, { timeout: 15_000, message: "the owner's word on the unchanged line came back" }).toBeGreaterThan(before)
    await expect(field).toHaveValue('row-draft')
    // Changed, it is bound to the question shown and answers it.
    await field.press('End')
    await field.pressSequentially(' now')
    await field.press('Enter')
    await expect.poll(transcript, { timeout: 15_000, message: 'the changed line answered the question shown' }).toContain('row-draft now')
    // The unchanged presses, refused before it, answered nothing.
    expect((await transcript()).split('row-draft').length - 1, 'the line went to the agent once, changed').toBe(1)
  } finally {
    await api.stopSession(s.id)
  }
})
