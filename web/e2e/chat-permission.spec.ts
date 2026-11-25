import { rmSync } from 'node:fs'
import { dayPage, expect, test } from './fixtures'
import { chatServer, firstTurn, launch, openSession, readStub, say, stubFile, toChat } from './chat-fixtures'
import type { OwnServer } from './own-server'

// A chat's permission question on its page (docs/plans/chat-or-terminal.md, row 7, designs 7 and 11), against the chat's stub on a
// server of the spec's own (`asker-proto`): the stub announces a Bash call in its stream and asks through the session's own route
// with its agent token, as the prompt tool does, held until the operator answers. The question says the call in plain words and
// shows the exact command; Allow once lets the stub's call run; a long input's Allow once waits until it is opened; Deny's message
// reaches the stub; characters a person cannot see are marks; a view link's join page shows the question and no button.
test.describe.configure({ mode: 'serial' })

let own: OwnServer
const sessions: string[] = []

test.beforeAll(async () => {
  test.setTimeout(120_000)
  own = await chatServer('chat-permission')
})

test.afterAll(async () => {
  for (const id of sessions) await own.api.stopSession(id)
  await own.stop()
})

test('the operator allows a call shown whole, opens a long one before allowing, and a deny tells the agent why', async ({ browser }) => {
  test.setTimeout(180_000)
  const api = own.api
  const { s, conv } = await launch(api, sessions, 'e2e chat permission', 'asker-proto')
  const page = await dayPage(browser, { server: own.started })
  try {
    const view = await openSession(page, s.id)
    await firstTurn(page, api, s.id)
    await toChat(api, s.id, view)
    const chat = page.locator('[data-chat-view]')

    // A one-line call: its plain words, the exact command in one line, Allow once offered at once.
    await say(page, 'ask short')
    const q = chat.locator('[data-permission-question][data-permission-state="open"]')
    await expect(q).toBeVisible({ timeout: 30_000 })
    await expect(q.locator('[data-permission-words]')).toHaveText('Run a command?')
    await expect(q.locator('[data-permission-line]')).toHaveText('echo allowed')
    await expect(q.locator('[data-permission-hidden]')).toHaveCount(0)
    // The reply bar takes no answer for it: it points to the chat.
    await expect(page.locator('[data-quick-reply-in-chat]')).toHaveText('Answer it in the chat.', { timeout: 15_000 })
    const allow = q.locator('[data-permission-allow]')
    await expect(allow).toBeEnabled()
    await allow.click()
    await expect.poll(() => readStub(own, conv, 'allowed'), { timeout: 30_000 }).not.toBe('')
    const done = chat.locator('[data-permission-question]').first()
    await expect(done).toHaveAttribute('data-permission-state', 'allowed', { timeout: 30_000 })
    await expect(done.locator('[data-permission-end]')).toHaveText(/^Allowed once by /)
    await expect(chat.locator('[data-chat-turn]').last()).toHaveAttribute('data-turn-outcome', 'done', { timeout: 30_000 })

    // A long call: Allow once waits until the whole input is drawn; opened, it is offered. The operator denies it with a message.
    await say(page, 'ask long')
    const long = chat.locator('[data-permission-question][data-permission-state="open"]')
    await expect(long).toBeVisible({ timeout: 30_000 })
    await expect(long.locator('[data-permission-allow]')).toBeDisabled()
    await long.locator('[data-permission-line]').click()
    await expect(long.locator('[data-permission-whole]')).toContainText('echo "step 12 of a long command that runs over many lines"')
    await expect(long.locator('[data-permission-allow]')).toBeEnabled()
    await long.locator('[data-permission-message]').fill('Use make instead.')
    await long.locator('[data-permission-deny]').click()
    await expect.poll(() => readStub(own, conv, 'denied'), { timeout: 30_000 }).toBe('Use make instead.\n')
    await expect(chat.locator('[data-permission-question]').nth(1)).toHaveAttribute('data-permission-state', 'denied', { timeout: 30_000 })

    // Characters a person cannot see: each a mark, and one line says so; nothing of them is in what the question draws.
    rmSync(stubFile(own, conv, 'denied'), { force: true })
    await say(page, 'ask hidden')
    const hid = chat.locator('[data-permission-question][data-permission-state="open"]')
    await expect(hid).toBeVisible({ timeout: 30_000 })
    await expect(hid.locator('[data-permission-hidden]')).toHaveText('This call holds characters that are not shown as typed.')
    await hid.locator('[data-permission-line]').click()
    const whole = hid.locator('[data-permission-whole]')
    await expect(whole.locator('[data-hidden-mark]').first()).toBeVisible()
    const drawn = await whole.innerText()
    const unseen = [0x202e, 0x2066, 0x2067, 0x2068, 0x2069, 0x200b, 0xfeff, 0x0d, 0x2028]
    expect([...drawn].filter((c) => unseen.includes(c.codePointAt(0)!))).toEqual([])
    expect(drawn).toContain(`${'\\'}u202E`)
    await hid.locator('[data-permission-deny]').click()
    await expect.poll(() => readStub(own, conv, 'denied'), { timeout: 30_000 }).toBe('The operator denied this call.\n')
  } finally {
    await page.context().close()
  }
})

test("a view link's join page shows the question and who answers it, with no button", async ({ browser }) => {
  test.setTimeout(180_000)
  const api = own.api
  const { s, conv } = await launch(api, sessions, 'e2e chat permission link', 'asker-proto')
  const page = await dayPage(browser, { server: own.started })
  const guest = await browser.newContext({ baseURL: own.started.baseURL, viewport: { width: 1280, height: 800 } })
  try {
    const view = await openSession(page, s.id)
    await firstTurn(page, api, s.id)
    await toChat(api, s.id, view)
    await say(page, 'ask short')
    const q = page.locator('[data-chat-view] [data-permission-question][data-permission-state="open"]')
    await expect(q).toBeVisible({ timeout: 30_000 })

    const { token } = await api.ok<{ token: string }>('POST', `/api/sessions/${s.id}/links`, { role: 'view' })
    await guest.addInitScript(() => localStorage.setItem('conductor.displayName', 'Priya'))
    const g = await guest.newPage()
    await g.goto(`/join/${token}`)
    await g.getByRole('button', { name: 'Join session' }).click()
    const gq = g.locator('[data-chat-view] [data-permission-question]')
    await expect(gq).toHaveAttribute('data-permission-state', 'open', { timeout: 30_000 })
    await expect(gq.locator('[data-permission-words]')).toHaveText('Run a command?')
    await expect(gq.locator('[data-permission-line]')).toHaveText('echo allowed')
    await expect(gq.locator('[data-permission-operator-only]')).toHaveText('Only the person who runs this server can answer.')
    await expect(gq.locator('button[data-permission-allow], button[data-permission-deny]')).toHaveCount(0)
    await expect(g.locator('[data-way-label]')).toHaveText('Chat')
    // A view link has no box to write in.
    await expect(g.locator('[data-chat-box]')).toHaveCount(0)

    // The operator answers; the guest sees it answered.
    await q.locator('[data-permission-deny]').click()
    await expect(gq).toHaveAttribute('data-permission-state', 'denied', { timeout: 30_000 })
    await expect.poll(() => readStub(own, conv, 'denied'), { timeout: 30_000 }).not.toBe('')
  } finally {
    await guest.close()
    await page.context().close()
  }
})
