import { dayPage, expect, test } from './fixtures'
import { budget, chatServer, firstTurn, joined, launch, LONG_MS, openSession, readStub, say, STEP_MS, toChat, turnWith } from './chat-fixtures'
import type { BrowserContext, Page } from '@playwright/test'
import type { OwnServer } from './own-server'

// The operator's own lines go for the agent to expand (UX3 R10, I19), on a server of the spec's own with the chat's stub, which
// writes each message's mark to <conversation>.marks ("marked" as written, "unmarked" for the agent to expand). Only what the person
// typed or pasted into the box, or picked from its list, themselves goes unmarked, and only where the server said this connection
// may: a line put in the box by code goes as written, a line from a control link's page goes as written. The conversation draws an
// expanded message's command and files as chips, and nothing else as one; again after a reload.
test.describe.configure({ mode: 'serial' })

let own: OwnServer
const sessions: string[] = []
const guests: BrowserContext[] = []

test.beforeAll(async () => {
  test.setTimeout(120_000)
  own = await chatServer('chat-expand')
})

test.afterAll(async () => {
  for (const c of guests) await c.close()
  for (const id of sessions) await own.api.stopSession(id)
  await own.stop()
})

async function aChat(page: Page, name: string): Promise<{ id: string; conv: string }> {
  const { s, conv } = await launch(own.api, sessions, name)
  const view = await openSession(page, s.id)
  await firstTurn(page, own.api, s.id)
  await toChat(own.api, s.id, view)
  await say(page, 'edit please')
  budget(LONG_MS)
  await expect(turnWith(page, 'stub chat: edit please')).toHaveAttribute('data-turn-outcome', 'done', { timeout: LONG_MS })
  return { id: s.id, conv }
}

const box = (page: Page) => page.locator('[data-chat-view] [data-chat-box] textarea')
/** The mark the stub was told with the conversation's last message. */
const lastMark = (conv: string) => readStub(own, conv, 'marks').trim().split('\n').pop()
/** The stub's marks once there are n of them: the message's own, never one before it. */
async function markOf(conv: string, n: number): Promise<string | undefined> {
  await expect.poll(() => readStub(own, conv, 'marks').trim().split('\n').filter(Boolean).length, { timeout: LONG_MS }).toBe(n)
  return lastMark(conv)
}
const marks = (conv: string) => readStub(own, conv, 'marks').trim().split('\n').filter(Boolean).length

test("the operator's own typed lines go for the agent to expand: a command, a picked file, a line kept while another agent was open", async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  try {
    const { id, conv } = await aChat(page, 'e2e expand')
    const b = await launch(own.api, sessions, 'e2e expand other')
    budget(16 * STEP_MS + 6 * LONG_MS)
    await expect(box(page)).toHaveAttribute('placeholder', /· @ for a file, \/ for a command$/)
    // A command typed whole.
    let n = marks(conv)
    await box(page).click()
    await page.keyboard.type('/compact')
    await expect(page.locator('[data-chat-help]')).toHaveCount(0)
    await page.keyboard.press('Escape')
    await box(page).press('Enter')
    expect(await markOf(conv, ++n)).toBe('unmarked')
    const cmd = page.locator('[data-chat-prompt-text]').filter({ hasText: '/compact' }).last()
    await expect(cmd.locator('[data-chat-chip="command"]')).toHaveText('/compact', { timeout: LONG_MS })
    // A file picked from the list, then words typed: the agent attaches it.
    await box(page).click()
    await page.keyboard.type('Look at @no')
    await expect(page.locator('[data-chat-menu-row="notes.txt"]')).toBeVisible({ timeout: LONG_MS })
    await page.keyboard.press('Enter')
    await page.keyboard.type('first word?')
    await page.keyboard.press('Enter')
    expect(await markOf(conv, ++n)).toBe('unmarked')
    await expect(page.locator('[data-chat-words]').filter({ hasText: 'stub attached: notes.txt' }).last()).toBeVisible({ timeout: LONG_MS })
    const mention = page.locator('[data-chat-prompt-text]').filter({ hasText: 'first word?' }).last()
    await expect(mention.locator('[data-chat-chip="mention"]')).toHaveText('@notes.txt')
    // A line typed, kept while another agent is open, and sent on coming back: still the person's own.
    await box(page).click()
    await page.keyboard.type('@notes.txt kept for later')
    await page.keyboard.press('Escape')
    for (const to of [b.s.id, id]) {
      const link = page.locator(`[data-session-list] [data-sidebar-row="s:${to}"] a[href="/sessions/${to}"]`).first()
      await link.scrollIntoViewIfNeeded()
      await link.click()
      await expect(page).toHaveURL(new RegExp(`/sessions/${to}$`))
    }
    await expect(page.locator('[data-session-way]').first()).toHaveAttribute('data-session-way', 'protocol', { timeout: LONG_MS })
    await expect(box(page)).toHaveValue('@notes.txt kept for later')
    await box(page).press('Enter')
    expect(await markOf(conv, ++n)).toBe('unmarked')
    // Drawn again after a reload: the chips are the entries' own.
    await page.reload()
    await expect(page.locator('[data-chat-prompt-text][data-chat-prompt-expanded] [data-chat-chip]').first()).toBeVisible({ timeout: LONG_MS })
  } finally {
    await page.context().close()
  }
})

test('a line the page put in the box goes as written: text set by code, and a pick no person pressed', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  try {
    const { conv } = await aChat(page, 'e2e expand by code')
    budget(10 * STEP_MS + 3 * LONG_MS)
    let n = marks(conv)
    // The person types in the box and empties it: what code writes after that is not theirs.
    await box(page).click()
    await page.keyboard.type('x')
    await page.keyboard.press('Backspace')
    await expect(box(page)).toHaveValue('')
    // Text set by code (an input event no person made), a command and a file in it, then sent with a person's Enter: as written.
    // (The cursor ends past the command's word, so no list of commands is open to take the Enter.)
    await box(page).evaluate((el: HTMLTextAreaElement) => {
      el.value = '/compact @notes.txt now'
      el.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await expect(box(page)).toHaveValue('/compact @notes.txt now')
    await box(page).press('End')
    await box(page).press('Enter')
    expect(await markOf(conv, ++n)).toBe('marked')
    await expect(turnWith(page, 'stub chat: /compact @notes.txt now')).toHaveAttribute('data-turn-outcome', 'done', { timeout: LONG_MS })
    // A line with a command and a file, sent as written, draws no chip: chips are the expanded entries' alone.
    const plain = page.locator('[data-chat-prompt-text]').filter({ hasText: '/compact @notes.txt now' }).last()
    await expect(plain).toBeVisible({ timeout: LONG_MS })
    await expect(plain.locator('[data-chat-chip]')).toHaveCount(0)
    // The person types an @ word, and a pick no person pressed fills it: the page's line, as written.
    await box(page).click()
    await page.keyboard.type('@no')
    await expect(page.locator('[data-chat-menu-row="notes.txt"]')).toBeVisible({ timeout: LONG_MS })
    await page.locator('[data-chat-menu-row="notes.txt"]').evaluate((el: HTMLElement) => el.click())
    await expect(box(page)).toHaveValue('@notes.txt ')
    await box(page).press('Enter')
    expect(await markOf(conv, ++n)).toBe('marked')
    // Changed by the person after that, it is theirs again.
    await box(page).click()
    await page.keyboard.type('@notes.txt now mine')
    await page.keyboard.press('Escape')
    await box(page).press('Enter')
    expect(await markOf(conv, ++n)).toBe('unmarked')
  } finally {
    await page.context().close()
  }
})

test("text dragged from the conversation into the box is not the person's typing: it goes as written, and a / line says so", async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  try {
    const { conv } = await aChat(page, 'e2e expand drop')
    // A line sent as written (code puts it in the box), so that the agent's reply repeats a command's and a file's words.
    await box(page).evaluate((el: HTMLTextAreaElement) => {
      el.value = '/model and @notes.txt in words'
      el.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await box(page).press('Enter')
    budget(8 * STEP_MS + 3 * LONG_MS)
    // The agent's reply holds a command's and a file's words; the person drags them from it onto the box with the mouse.
    const words = page.locator('[data-chat-words]').filter({ hasText: 'stub chat: /model and @notes.txt in words' }).last()
    await expect(words).toBeVisible({ timeout: LONG_MS })
    await words.click({ clickCount: 3 })
    const picked = await page.evaluate(() => window.getSelection()?.toString().trim() ?? '')
    expect(picked).toContain('/model')
    const [from, to] = await Promise.all([words.boundingBox(), box(page).boundingBox()])
    await page.mouse.move(from!.x + from!.width / 4, from!.y + from!.height / 2)
    await page.mouse.down()
    await page.mouse.move(to!.x + to!.width / 2, to!.y + to!.height / 2, { steps: 12 })
    await page.mouse.up()
    await expect(box(page)).toHaveValue(/stub chat: \/model and @notes\.txt in words/)
    const n = marks(conv)
    await box(page).press('Enter')
    expect(await markOf(conv, n + 1)).toBe('marked')
    // A line that begins with / and is not the person's own says it goes as plain text.
    await box(page).evaluate((el: HTMLTextAreaElement) => {
      el.value = '/compact'
      el.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await expect(page.locator('[data-chat-view] [data-chat-help="plain"]')).toHaveText('Sent as plain text. Type it yourself to run it as a command.')
    await box(page).fill('')
  } finally {
    await page.context().close()
  }
})

test("a control link's lines go as written, with no chips, in its window and in the operator's", async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  try {
    const { id, conv } = await aChat(page, 'e2e expand link')
    const c = await joined(browser, own, id, 'control')
    guests.push(c.ctx)
    budget(8 * STEP_MS + 3 * LONG_MS)
    await expect(box(c.g)).toBeVisible({ timeout: LONG_MS })
    await expect(box(c.g)).not.toHaveAttribute('placeholder', /@ for a file/)
    let n = marks(conv)
    await box(c.g).click()
    await c.g.keyboard.type('/compact')
    await box(c.g).press('Enter')
    expect(await markOf(conv, ++n)).toBe('marked')
    await expect(turnWith(c.g, 'stub chat: /compact')).toHaveAttribute('data-turn-outcome', 'done', { timeout: LONG_MS })
    for (const p of [c.g, page]) {
      const line = p.locator('[data-chat-prompt-text]').filter({ hasText: '/compact' }).last()
      await expect(line).toBeVisible({ timeout: LONG_MS })
      await expect(line.locator('[data-chat-chip]')).toHaveCount(0)
    }
  } finally {
    await page.context().close()
  }
})

test("the server's word is the box's: once the session becomes a crew's member, the operator's own line goes as written", async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  try {
    const { id, conv } = await aChat(page, 'e2e expand crew')
    budget(8 * STEP_MS + 3 * LONG_MS)
    await expect(box(page)).toHaveAttribute('placeholder', /· @ for a file, \/ for a command$/)
    // A crew formed round the session: its way message says no more expand, and the page takes the server's word.
    const r = await own.api.call('POST', `/api/sessions/${id}/crew`, { name: 'e2e expand crew', goal: 'a goal', members: [{ name: 'lead', start: { when: 'immediately' } }], self: 'lead' })
    expect(r.status, JSON.stringify(r.body)).toBe(201)
    await expect(box(page)).not.toHaveAttribute('placeholder', /@ for a file/, { timeout: LONG_MS })
    const n = marks(conv)
    await box(page).click()
    await page.keyboard.type('/compact')
    await box(page).press('Enter')
    expect(await markOf(conv, n + 1)).toBe('marked')
  } finally {
    await page.context().close()
  }
})
