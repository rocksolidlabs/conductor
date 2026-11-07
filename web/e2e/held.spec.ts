import { expect, test } from './fixtures'
import { HELD, holdOne } from './held-helpers'

// A line a link's controller sent is typed into the session, and the link is
// revoked before its Enter: the Enter is never written, the session's view
// says so in plain words and takes no reply, and nothing but a person's typing
// in the terminal ends it (the pointer moved over it does not); typing clears
// the words, and a reply sent before then goes, a line of its own.
//
// Whatever renderer the browser has: with WebGL the screen is a canvas and
// holds no text, so what the agent holds is read from the stub's files (its
// TUI mode's line not yet entered, the mouse reports it read, whether it
// spins, its transcript of the lines entered), never from the screen.
test('a line typed for a link that is let go holds the session: the view says so and takes no reply, the pointer ends nothing, typing does', async ({ page, api, browser, state }) => {
  const { s, bar, terminal, guestCtx, stub } = await holdOne(page, api, browser, state.home)
  try {
    // The owner's view says the session waits for the terminal, in plain words, and takes no reply meanwhile.
    await expect(bar).toContainText('Type in the terminal to go on')
    await expect(bar).toContainText(HELD)
    await expect(bar.locator('input')).toBeDisabled()
    await expect(bar.locator('input')).toHaveAttribute('placeholder', 'Type in the terminal first')
    expect((await api.session(s.id)).attention).toMatchObject({ state: 'needs_input', source: 'submit', message: HELD })
    // The sidebar's row says it too, and takes no reply either.
    const row = page.locator(`[data-session-list="sidebar"] [data-sidebar-row="s:${s.id}"]`).first()
    await expect(row.locator('[data-row-prompt]')).toHaveText(HELD, { timeout: 10_000 })
    await expect(row.locator('[data-row-reply] input, input[data-row-reply]').first()).toBeDisabled()

    // The pointer moved over the terminal reaches the agent (its mouse reports) and types nothing: the hold and its words
    // stay, and so does the guest's line, not entered.
    const before = stub.mice()
    const box = (await page.locator('.terminal-host .xterm-screen').first().boundingBox())!
    for (let i = 0; i < 6; i++) await page.mouse.move(box.x + 40 + i * 30, box.y + 40 + i * 10, { steps: 4 })
    await expect.poll(stub.mice, { timeout: 10_000, message: 'the pointer reached the agent' }).toBeGreaterThan(before)
    await page.waitForTimeout(1_000)
    await expect(bar).toHaveAttribute('data-held', 'true')
    await expect(bar).toContainText(HELD)
    expect((await api.session(s.id)).attention).toMatchObject({ source: 'submit', message: HELD })
    expect(stub.pending()).toBe('guest line')

    // Typing in the terminal (Ctrl+U, the leftover line killed) clears the words.
    await terminal.focus()
    await page.keyboard.press('Control+U')
    await expect(page.getByText(HELD)).toHaveCount(0, { timeout: 10_000 })
    await expect.poll(async () => (await api.session(s.id)).attention?.source, { timeout: 10_000 }).not.toBe('submit')
    await expect.poll(stub.pending, { timeout: 10_000, message: 'Ctrl+U cleared the line' }).toBe('')
    // The agent's work done, a line typed in the terminal is the stub's next: the guest's was never entered.
    stub.stopSpin()
    await expect.poll(stub.spin, { timeout: 10_000 }).toBe('done')
    await page.keyboard.type('after')
    await page.keyboard.press('Enter')
    await expect.poll(() => stub.entered('after'), { timeout: 30_000, message: 'the typed line was entered' }).toBe(true)
    expect(stub.typedLines()).not.toContain('guest line')
    expect(stub.enters().map((e) => e.line)).not.toContain('guest line')
  } finally {
    await guestCtx.close()
    await api.stopSession(s.id)
  }
})

test('a reply waiting for the held turn goes once someone types in the terminal, a line of its own', async ({ page, api, browser, state }) => {
  const { s, bar, terminal, guestCtx, stub } = await holdOne(page, api, browser, state.home, { reply: 'owner line' })
  try {
    // The reply was sent (its box emptied) and waits: nothing of it is typed while the turn is held, the guest's line alone
    // is. The agent's work is done meanwhile, its screen quiet, so that the reply goes as soon as it may: within its ten
    // seconds.
    await expect(bar).toContainText(HELD)
    await expect(bar.locator('input')).toHaveValue('')
    stub.stopSpin()
    await expect.poll(stub.spin, { timeout: 10_000 }).toBe('done')
    expect(stub.pending()).toBe('guest line')
    expect(stub.typedLines()).not.toContain('owner line')
    // Typing in the terminal (Ctrl+U, the leftover line killed) clears the words, and the waiting reply goes, a line of its
    // own: the guest's never entered.
    await terminal.focus()
    await page.keyboard.press('Control+U')
    await expect(page.getByText(HELD)).toHaveCount(0, { timeout: 10_000 })
    await expect.poll(() => stub.entered('owner line'), { timeout: 30_000, message: 'the reply was entered' }).toBe(true)
    expect(stub.typedLines()).not.toContain('guest line')
    expect(stub.enters().map((e) => e.line)).not.toContain('guest line')
  } finally {
    await guestCtx.close()
    await api.stopSession(s.id)
  }
})

test('the question the held turn took the place of comes back once someone types in the terminal: a choice refused meanwhile can be clicked again', async ({ page, api, browser, state }) => {
  const { s, bar, terminal, guestCtx, stub } = await holdOne(page, api, browser, state.home, { chat: true })
  try {
    await expect(bar).toContainText(HELD)
    // The owner's chat: the question keeps its choices while the turn is held over it.
    await page.locator('[data-inspector] [data-chat-tab]').click()
    const left = page.locator('[data-chat] [data-chat-question]').filter({ hasText: 'Which way?' }).last().locator('[data-chat-option="1"]')
    // A choice clicked while the turn is held is refused: the hold, not the question, is what the session waits on.
    await left.click()
    const refused = page.locator('li').filter({ hasText: 'Already answered' })
    await expect(refused.last()).toBeVisible({ timeout: 15_000 })
    const toasts = await refused.count()
    expect(stub.pending()).toBe('guest line')
    stub.stopSpin()
    await expect.poll(stub.spin, { timeout: 10_000 }).toBe('done')
    // Typing in the terminal (Ctrl+U, the leftover line killed) answers nothing: the question shows again, with its id.
    await terminal.focus()
    await page.keyboard.press('Control+U')
    await expect(page.getByText(HELD)).toHaveCount(0, { timeout: 10_000 })
    await expect(bar).toContainText('Which way?', { timeout: 10_000 })
    await expect.poll(stub.pending, { timeout: 10_000, message: 'Ctrl+U cleared the line' }).toBe('')
    // The same choice, clicked again from the same page, goes: the refusal is not remembered as an answer.
    await left.click()
    await expect.poll(stub.pending, { timeout: 15_000, message: 'the choice was typed' }).toBe('l')
    await expect.poll(async () => (await api.session(s.id)).attention?.message ?? '', { timeout: 10_000 }).not.toBe('Which way?')
    expect(await refused.count()).toBeLessThanOrEqual(toasts)
  } finally {
    await guestCtx.close()
    await api.stopSession(s.id)
  }
})
