import { rmSync } from 'node:fs'
import type { Page } from '@playwright/test'
import { dayPage, expect, test } from './fixtures'
import { budget, chatServer, firstTurn, joined, launch, lineButton, LONG_MS, openSession, readStub, say, STEP_MS, stubFile, toChat } from './chat-fixtures'
import type { OwnServer } from './own-server'

// A chat's permission question on its page (docs/plans/chat-or-terminal.md, row 7, designs 7 and 11), against the chat's stub on a
// server of the spec's own (`asker-proto`): the stub announces a Bash call in its stream and asks through the session's own route
// with its agent token, as the prompt tool does, held until the operator answers. The question says the call in plain words with
// its tool and shows the exact command; Allow once lets the stub's call run; a long input's Allow once waits until it is opened and
// its end has been in view, the input lying in the chat's own scroll with its buttons under it; Deny's message reaches the stub,
// bounded in bytes; characters a person cannot see are marks; no bar or sidebar field repeats the question;
// a view link's join page shows the question and no button. Each step raises the test's timeout by its own waits' caps (budget).
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

/** How near its end a chat's scroll is at its end (CHAT_END_SLACK, web/app/utils/transcript.ts: its module reads the app's aliases). */
const CHAT_END_SLACK = 48

/** The chat's open question, once its input is read (its line a button when it is more than one line, else its Allow judged). */
function openQuestion(page: Page) {
  return page.locator('[data-chat-view] [data-permission-question][data-permission-state="open"]')
}

test('the operator allows a call shown whole, opens a long one before allowing, and a deny tells the agent why', async ({ browser }) => {
  const api = own.api
  const { s, conv } = await launch(api, sessions, 'e2e chat permission', 'asker-proto')
  const page = await dayPage(browser, { server: own.started })
  try {
    const view = await openSession(page, s.id)
    await firstTurn(page, api, s.id)
    await toChat(api, s.id, view)
    const chat = page.locator('[data-chat-view]')

    // A short call: its plain words, its tool, the whole input drawn at once with no line to open, Allow once offered at once.
    await say(page, 'ask short')
    budget(10 * STEP_MS + 4 * LONG_MS)
    const q = openQuestion(page)
    await expect(q).toBeVisible({ timeout: LONG_MS })
    await expect(q.locator('[data-permission-words]')).toHaveText('Run a command?')
    await expect(q.locator('[data-permission-tool]')).toHaveText('Bash')
    await expect(q.locator('[data-permission-whole][data-permission-short]')).toContainText('echo allowed')
    await expect(q.locator('[data-permission-line]')).toHaveCount(0)
    await expect(q.locator('[data-permission-hidden]')).toHaveCount(0)
    // The reason goes with a deny alone, and its field says so.
    await expect(q.locator('[data-permission-message]')).toHaveAttribute('placeholder', 'Why you deny, for the agent (optional)')
    // No bar repeats the question under the chat that shows it, and the sidebar's row says where it is answered, with no field.
    await expect(page.locator('[data-quick-reply]')).toHaveCount(0)
    const row = page.locator(`#dashboard-sidebar-main [data-sidebar-row="s:${s.id}"]`).first()
    await expect(row.locator('[data-row-in-chat]')).toHaveText('Answer it in the chat.', { timeout: LONG_MS })
    await expect(row.locator('[data-row-reply]')).toHaveCount(0)
    const allow = q.locator('[data-permission-allow]')
    await expect(allow).toBeEnabled()
    await allow.click()
    await expect.poll(() => readStub(own, conv, 'allowed'), { timeout: LONG_MS }).not.toBe('')
    const done = chat.locator('[data-permission-question]').first()
    await expect(done).toHaveAttribute('data-permission-state', 'allowed', { timeout: LONG_MS })
    await expect(done.locator('[data-permission-end]')).toHaveText(/^Allowed once by /)
    await expect(chat.locator('[data-chat-turn]').last()).toHaveAttribute('data-turn-outcome', 'done', { timeout: LONG_MS })

    // A long call: Allow once waits until the whole input is drawn; opened, it is offered. A deny's message is bounded in bytes:
    // past them the field says so and Deny waits; the operator denies with a message that fits.
    await say(page, 'ask long')
    budget(12 * STEP_MS + 3 * LONG_MS)
    const long = openQuestion(page)
    await expect(lineButton(long)).toBeVisible({ timeout: LONG_MS })
    // Waiting, Allow once looks like nothing to press, and one line says what it waits for.
    await expect(long.locator('[data-permission-allow]')).toBeDisabled()
    await expect(long.locator('[data-permission-allow]')).toHaveAttribute('data-permission-waiting', 'true')
    await expect(long.locator('[data-permission-waiting-line]')).toHaveText('Open the call above and read it whole to allow it.')
    await lineButton(long).click()
    await expect(long.locator('[data-permission-whole]')).toContainText('echo "step 12 of a long command that runs over many lines"')
    await long.locator('[data-permission-allow]').scrollIntoViewIfNeeded()
    await expect(long.locator('[data-permission-allow]')).toBeEnabled()
    await expect(long.locator('[data-permission-allow]')).not.toHaveAttribute('data-permission-waiting', /./)
    await expect(long.locator('[data-permission-waiting-line]')).toHaveCount(0)
    await long.locator('[data-permission-message]').fill(String.fromCodePoint(0x65e5).repeat(200))
    await expect(long.locator('[data-permission-message-long]')).toHaveText('The message to the agent is too long: it takes at most 500 bytes.')
    await expect(long.locator('[data-permission-deny]')).toBeDisabled()
    await long.locator('[data-permission-message]').fill(String.fromCodePoint(0x65e5).repeat(3) + ' Use make instead.')
    await expect(long.locator('[data-permission-message-long]')).toHaveCount(0)
    await long.locator('[data-permission-deny]').click()
    await expect.poll(() => readStub(own, conv, 'denied'), { timeout: LONG_MS }).toBe(`${String.fromCodePoint(0x65e5).repeat(3)} Use make instead.\n`)
    await expect(chat.locator('[data-permission-question]').nth(1)).toHaveAttribute('data-permission-state', 'denied', { timeout: LONG_MS })

    // Characters a person cannot see: each a mark, and one line says so; nothing of them is in what the question draws.
    rmSync(stubFile(own, conv, 'denied'), { force: true })
    await say(page, 'ask hidden')
    budget(8 * STEP_MS + 2 * LONG_MS)
    const hid = openQuestion(page)
    await expect(hid.locator('[data-permission-hidden]')).toHaveText('This call holds characters that are not shown as typed.', { timeout: LONG_MS })
    // Short: drawn whole at once.
    const whole = hid.locator('[data-permission-whole]')
    await expect(whole.locator('[data-hidden-mark]').first()).toBeVisible()
    const drawn = await whole.innerText()
    const unseen = [0x202e, 0x2066, 0x2067, 0x2068, 0x2069, 0x200b, 0xfeff, 0x0d, 0x2028]
    expect([...drawn].filter((c) => unseen.includes(c.codePointAt(0)!))).toEqual([])
    expect(drawn).toContain(`${'\\'}u202E`)
    await hid.locator('[data-permission-deny]').click()
    await expect.poll(() => readStub(own, conv, 'denied'), { timeout: LONG_MS }).toBe('The operator denied this call.\n')
  } finally {
    await page.context().close()
  }
})

test("a call with its description, as every real one has, is allowed with one press; one whose end shows with its top out of view waits", async ({ browser }) => {
  const api = own.api
  const { s, conv } = await launch(api, sessions, 'e2e chat permission described', 'asker-proto')
  // A window short enough that the seven lines' call cannot lie whole in the chat's pane above the box (one row since UX3 R4).
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 360 })
  try {
    const view = await openSession(page, s.id)
    await firstTurn(page, api, s.id)
    await toChat(api, s.id, view)
    const chat = page.locator('[data-chat-view]')
    // A short call with a description: drawn whole as it arrives, both its fields, and Allow once offered with no opening.
    await say(page, 'ask described')
    budget(8 * STEP_MS + 4 * LONG_MS)
    const q = openQuestion(page)
    const whole = q.locator('[data-permission-whole][data-permission-short]')
    await expect(whole).toContainText('echo allowed', { timeout: LONG_MS })
    await expect(whole).toContainText('Say allowed')
    await expect(q.locator('[data-permission-allow]')).toBeEnabled()
    await q.locator('[data-permission-allow]').click()
    await expect.poll(() => readStub(own, conv, 'allowed'), { timeout: LONG_MS }).not.toBe('')
    await expect(chat.locator('[data-chat-turn]').last()).toHaveAttribute('data-turn-outcome', 'done', { timeout: LONG_MS })
    // Another short call arrives below the person, who reads the chat's top: they jump to its end alone, its first row above the
    // window: Allow once waits, saying so; once its first row has been in view too, it is offered.
    await say(page, 'steps before')
    budget(10 * STEP_MS + 3 * LONG_MS)
    await expect(chat.locator('[data-chat-turn]').last()).toHaveAttribute('data-turn-outcome', 'done', { timeout: LONG_MS })
    await page.locator('[data-chat-scroll]').evaluate((el) => (el.scrollTop = 0))
    await say(page, 'ask seven')
    const seven = openQuestion(page)
    await expect(seven.locator('[data-permission-whole][data-permission-short]')).toBeAttached({ timeout: LONG_MS })
    await expect(seven.locator('[data-permission-input-start]')).not.toBeInViewport()
    await seven.locator('[data-permission-input-end]').evaluate((el) => el.scrollIntoView({ block: 'start' }))
    await expect(seven.locator('[data-permission-input-end]')).toBeInViewport()
    await expect(seven.locator('[data-permission-input-start]')).not.toBeInViewport()
    await expect(seven.locator('[data-permission-allow]')).toBeDisabled()
    await expect(seven.locator('[data-permission-waiting-line]')).toHaveText('Read the call from its start to its end to allow it.')
    await seven.locator('[data-permission-input-start]').evaluate((el) => el.scrollIntoView({ block: 'start' }))
    await expect(seven.locator('[data-permission-allow]')).toBeEnabled()
    await expect(seven.locator('[data-permission-waiting-line]')).toHaveCount(0)
    await seven.locator('[data-permission-deny]').click()
  } finally {
    await page.context().close()
  }
})

test("an Edit's question: a small change drawn whole with its path, a large one's path whole in its line", async ({ browser }) => {
  const api = own.api
  const { s } = await launch(api, sessions, 'e2e chat permission edit', 'asker-proto')
  const page = await dayPage(browser, { server: own.started, width: 390, height: 844, touch: true })
  try {
    const view = await openSession(page, s.id)
    await firstTurn(page, api, s.id)
    await toChat(api, s.id, view)
    const chat = page.locator('[data-chat-view]')
    await say(page, 'ask edit')
    budget(8 * STEP_MS + 3 * LONG_MS)
    const q = openQuestion(page)
    await expect(q.locator('[data-permission-words]')).toHaveText('Change a file?', { timeout: LONG_MS })
    await expect(q.locator('[data-permission-tool]')).toHaveText('Edit')
    const whole = q.locator('[data-permission-whole][data-permission-short]')
    await expect(whole).toContainText('confirmation_email_template.go')
    await expect(whole).toContainText('alpha and beta')
    await q.locator('[data-permission-deny]').click()
    await expect(chat.locator('[data-chat-turn]').last()).toHaveAttribute('data-turn-outcome', 'done', { timeout: LONG_MS })
    // A large change: one line, the path, never cut: wrapped whole in the line, at a phone's width.
    await say(page, 'ask bigedit')
    const big = openQuestion(page)
    await expect(lineButton(big)).toBeVisible({ timeout: LONG_MS })
    await expect(lineButton(big)).toContainText('/src/services/accounts/internal/handlers/registration/confirmation_email_template.go')
    const fits = await lineButton(big).locator('[data-permission-line-text]').evaluate((el) => el.scrollWidth <= el.clientWidth + 1)
    expect(fits).toBe(true)
    await big.locator('[data-permission-deny]').click()
  } finally {
    await page.context().close()
  }
})

test("an input whose end lies far down: no scroll of its own, its empty lines marked, Allow once under it and only once its end was seen", async ({ browser }) => {
  const api = own.api
  const { s, conv } = await launch(api, sessions, 'e2e chat permission padded', 'asker-proto')
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 700 })
  try {
    const view = await openSession(page, s.id)
    await firstTurn(page, api, s.id)
    await toChat(api, s.id, view)
    await say(page, 'ask padded')
    budget(14 * STEP_MS + 2 * LONG_MS)
    const q = openQuestion(page)
    await expect(lineButton(q)).toBeVisible({ timeout: LONG_MS })
    await lineButton(q).click()
    const whole = q.locator('[data-permission-whole]')
    await expect(whole).toBeVisible()
    // The line's top in view, the input's end far below it: Allow once waits.
    await lineButton(q).evaluate((el) => el.scrollIntoView({ block: 'start' }))
    await expect(q.locator('[data-permission-input-end]')).not.toBeInViewport()
    await expect(q.locator('[data-permission-allow]')).toBeDisabled()
    // Every empty line is marked, and so are the spaces left at the command's end.
    expect(await whole.locator('[data-line-end]').count()).toBeGreaterThanOrEqual(40)
    await expect(whole.locator('[data-trailing-spaces]')).toHaveText('···')
    // The input lies in the chat's scroll, whole: no box of its own scrolls it.
    expect(await whole.evaluate((el) => el.scrollHeight <= el.clientHeight + 1)).toBe(true)
    // Its end brought into view: the command's tail is seen, the buttons are under the input, and Allow once is offered.
    await q.locator('[data-permission-allow]').scrollIntoViewIfNeeded()
    // The tail's own words, measured where the browser drew them: inside the window and inside the chat's scroll.
    const tail = await whole.evaluate((el) => {
      const walk = document.createTreeWalker(el, NodeFilter.SHOW_TEXT)
      for (let n = walk.nextNode(); n; n = walk.nextNode()) {
        if (!n.textContent?.includes('curl -s')) continue
        const range = document.createRange()
        range.selectNodeContents(n)
        const r = range.getBoundingClientRect()
        const box = el.closest('[data-chat-scroll]')!.getBoundingClientRect()
        return { top: r.top, bottom: r.bottom, within: r.top >= box.top && r.bottom <= box.bottom && r.bottom <= window.innerHeight }
      }
      return null
    })
    expect(tail?.within).toBe(true)
    await expect(q.locator('[data-permission-allow]')).toBeEnabled()
    const [w, a] = await Promise.all([whole.boundingBox(), q.locator('[data-permission-allow]').boundingBox()])
    expect(w!.y + w!.height).toBeLessThanOrEqual(a!.y)
    await q.locator('[data-permission-deny]').click()
    await expect.poll(() => readStub(own, conv, 'denied'), { timeout: LONG_MS }).not.toBe('')
  } finally {
    await page.context().close()
  }
})

test("a person reading an opened input is not carried past it by the chat's next change, and one at the chat's end is followed", async ({ browser }) => {
  const api = own.api
  const { s, conv } = await launch(api, sessions, 'e2e chat permission reading', 'asker-proto')
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 700 })
  try {
    const view = await openSession(page, s.id)
    await firstTurn(page, api, s.id)
    await toChat(api, s.id, view)
    await say(page, 'ask padded')
    budget(10 * STEP_MS + 4 * LONG_MS)
    const q = openQuestion(page)
    await expect(lineButton(q)).toBeVisible({ timeout: LONG_MS })
    // At the chat's end, the person opens the long input: it grows below them, with no scroll of the page.
    await lineButton(q).click()
    await expect(q.locator('[data-permission-whole]')).toBeVisible()
    await expect(q.locator('[data-permission-input-end]')).not.toBeInViewport()
    await expect(q.locator('[data-permission-allow]')).toBeDisabled()
    // A change of the chat while they read (a message queued behind the turn): the page does not move them to its end.
    const before = Number(await page.locator('[data-chat-view]').getAttribute('data-chat-entries'))
    await say(page, 'a message while reading')
    await expect.poll(async () => Number(await page.locator('[data-chat-view]').getAttribute('data-chat-entries')), { timeout: LONG_MS }).toBeGreaterThan(before)
    await expect(page.locator('[data-chat-view] [data-chat-prompt]').filter({ hasText: 'a message while reading' })).toBeAttached()
    await expect(q.locator('[data-permission-input-end]')).not.toBeInViewport()
    await expect(q.locator('[data-permission-allow]')).toBeDisabled()
    // A person at the chat's end is followed: scrolled there, the answer's turn and the queued message's come into view. The scroll
    // brings the input's end into view, which enables Allow once and takes its line away: the question changes under the scroll.
    // Deny is pressed only once that has happened and the chat is at its end again, at rest, Deny whole in view: a click on a moving
    // button is retried by Playwright with the button scrolled to the scroller's bottom edge, which takes the chat off its end
    // (issue 1144), and what is judged then is a person who scrolled away.
    const scroller = page.locator('[data-chat-scroll]')
    await scroller.evaluate((el) => (el.scrollTop = el.scrollHeight))
    await expect(q.locator('[data-permission-allow]')).toBeEnabled()
    await expect(q.locator('[data-permission-waiting-line]')).toHaveCount(0)
    await scroller.evaluate((el) => (el.scrollTop = el.scrollHeight))
    const fromEnd = () => scroller.evaluate((el) => el.scrollHeight - el.scrollTop - el.clientHeight)
    await expect.poll(fromEnd).toBeLessThan(CHAT_END_SLACK)
    await expect(q.locator('[data-permission-deny]')).toBeInViewport({ ratio: 1 })
    await q.locator('[data-permission-deny]').click()
    // The press left the chat at its end: nothing scrolled it away.
    expect(await fromEnd()).toBeLessThan(CHAT_END_SLACK)
    await expect.poll(() => readStub(own, conv, 'denied'), { timeout: LONG_MS }).not.toBe('')
    const last = page.locator('[data-chat-view] [data-chat-turn]').filter({ hasText: 'stub chat: a message while reading' })
    await expect(last).toHaveAttribute('data-turn-outcome', 'done', { timeout: LONG_MS })
    await expect(last.locator('[data-chat-words]')).toBeInViewport()
  } finally {
    await page.context().close()
  }
})

test('a value of no-break spaces wraps inside the question: its last word is not cut at the chat\'s edge', async ({ browser }) => {
  const api = own.api
  const { s, conv } = await launch(api, sessions, 'e2e chat permission nbsp', 'asker-proto')
  const page = await dayPage(browser, { server: own.started })
  try {
    const view = await openSession(page, s.id)
    await firstTurn(page, api, s.id)
    await toChat(api, s.id, view)
    await say(page, 'ask nbsp')
    budget(6 * STEP_MS + 2 * LONG_MS)
    const q = openQuestion(page)
    await expect(lineButton(q)).toBeVisible({ timeout: LONG_MS })
    await lineButton(q).click()
    const whole = q.locator('[data-permission-whole]')
    await expect(whole).toBeVisible()
    await q.locator('[data-permission-allow]').scrollIntoViewIfNeeded()
    // The word's own box, from the browser's Range, lies inside the question's.
    const inside = await q.evaluate((el) => {
      const walk = document.createTreeWalker(el.querySelector('[data-permission-whole]')!, NodeFilter.SHOW_TEXT)
      for (let n = walk.nextNode(); n; n = walk.nextNode()) {
        const at = n.textContent?.indexOf('tailword') ?? -1
        if (at < 0) continue
        const range = document.createRange()
        range.setStart(n, at)
        range.setEnd(n, at + 'tailword'.length)
        const r = range.getBoundingClientRect()
        const box = el.getBoundingClientRect()
        return { word: [r.left, r.right], box: [box.left, box.right], inside: r.left >= box.left && r.right <= box.right && r.width > 0 }
      }
      return null
    })
    expect(inside?.inside, JSON.stringify(inside)).toBe(true)
    await q.locator('[data-permission-deny]').click()
    await expect.poll(() => readStub(own, conv, 'denied'), { timeout: LONG_MS }).not.toBe('')
  } finally {
    await page.context().close()
  }
})

test("a skill the agent runs on a link's line is refused without a question; on the operator's own line it is asked by its name", async ({ browser }) => {
  const api = own.api
  const { s, conv } = await launch(api, sessions, 'e2e chat permission skill', 'asker-proto')
  const page = await dayPage(browser, { server: own.started })
  let guest: Awaited<ReturnType<typeof joined>> | undefined
  try {
    const view = await openSession(page, s.id)
    await firstTurn(page, api, s.id)
    await toChat(api, s.id, view)
    const chat = page.locator('[data-chat-view]')

    // A control link's line: the agent's skill call is refused, its reason told to the agent, and nothing is drawn for the operator.
    guest = await joined(browser, own, s.id, 'control')
    await say(guest.g, 'ask skill')
    budget(8 * STEP_MS + 3 * LONG_MS)
    await expect.poll(() => readStub(own, conv, 'denied'), { timeout: LONG_MS }).toBe("Folder commands and skills run only from the operator's own line.\n")
    await expect(chat.locator('[data-chat-turn]').last()).toHaveAttribute('data-turn-outcome', 'done', { timeout: LONG_MS })
    await expect(chat.locator('[data-permission-question]')).toHaveCount(0)
    await expect(page.locator(`#dashboard-sidebar-main [data-sidebar-row="s:${s.id}"]`).first().locator('[data-row-in-chat]')).toHaveCount(0)
    expect(readStub(own, conv, 'allowed')).toBe('')

    // The operator's own line: asked, by the skill's name, with what an Allow grants, and Allow once and Deny alone.
    rmSync(stubFile(own, conv, 'denied'), { force: true })
    await say(page, 'ask skill')
    budget(8 * STEP_MS + 2 * LONG_MS)
    const q = openQuestion(page)
    await expect(q).toBeVisible({ timeout: LONG_MS })
    await expect(q.locator('[data-permission-words]')).toHaveText('Run a skill?')
    await expect(q.locator('[data-permission-tool]')).toHaveText('Skill')
    await expect(q.locator('[data-permission-whole]')).toContainText('hello')
    await expect(q.locator('[data-permission-skill-grant]')).toHaveText('A skill can run the tools its own file allows without asking again.')
    await expect(q.locator('[data-permission-buttons] button')).toHaveText(['Allow once', 'Deny'])
    await q.locator('[data-permission-deny]').click()
    await expect.poll(() => readStub(own, conv, 'denied'), { timeout: LONG_MS }).toBe('The operator denied this call.\n')
  } finally {
    await guest?.ctx.close()
    await page.context().close()
  }
})

test("a view link's join page shows the question and who answers it, with no button", async ({ browser }) => {
  const api = own.api
  const { s, conv } = await launch(api, sessions, 'e2e chat permission link', 'asker-proto')
  const page = await dayPage(browser, { server: own.started })
  let guest: Awaited<ReturnType<typeof joined>> | undefined
  try {
    const view = await openSession(page, s.id)
    await firstTurn(page, api, s.id)
    await toChat(api, s.id, view)
    await say(page, 'ask short')
    budget(9 * STEP_MS + 4 * LONG_MS)
    const q = openQuestion(page)
    await expect(q).toBeVisible({ timeout: LONG_MS })

    guest = await joined(browser, own, s.id, 'view')
    const g = guest.g
    const gq = g.locator('[data-chat-view] [data-permission-question]')
    await expect(gq).toHaveAttribute('data-permission-state', 'open', { timeout: LONG_MS })
    await expect(gq.locator('[data-permission-words]')).toHaveText('Run a command?')
    await expect(gq.locator('[data-permission-tool]')).toHaveText('Bash')
    await expect(gq.locator('[data-permission-whole]')).toContainText('echo allowed')
    await expect(gq.locator('[data-permission-operator-only]')).toHaveText('Only the person who runs this server can answer.')
    await expect(gq.locator('button[data-permission-allow], button[data-permission-deny]')).toHaveCount(0)
    await expect(g.locator('[data-way-label]')).toHaveText('Chat')
    // A view link has no box to write in, and no bar repeats the question.
    await expect(g.locator('[data-chat-box]')).toHaveCount(0)
    await expect(g.locator('[data-quick-reply]')).toHaveCount(0)

    // The operator answers; the guest sees it answered.
    await q.locator('[data-permission-deny]').click()
    await expect(gq).toHaveAttribute('data-permission-state', 'denied', { timeout: LONG_MS })
    await expect.poll(() => readStub(own, conv, 'denied'), { timeout: LONG_MS }).not.toBe('')
  } finally {
    await guest?.ctx.close()
    await page.context().close()
  }
})

test('a request scrolled out of view leaves one line above the box; Show it brings the card back with the focus on its first button', async ({ browser }) => {
  const api = own.api
  const { s } = await launch(api, sessions, 'e2e chat permission away', 'asker-proto')
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 560 })
  try {
    const view = await openSession(page, s.id)
    await firstTurn(page, api, s.id)
    await toChat(api, s.id, view)
    const chat = page.locator('[data-chat-view]')
    // A conversation taller than its pane: two turns of steps, opened, then the request.
    for (const words of ['steps one', 'steps two']) {
      await say(page, words)
      budget(LONG_MS + STEP_MS)
      await expect(chat.locator(`[data-chat-turn][data-turn-outcome="done"]`).filter({ hasText: `stub chat: ${words}` })).toHaveCount(1, { timeout: LONG_MS })
      await chat.locator('[data-chat-turn]').filter({ hasText: `stub chat: ${words}` }).locator('[data-chat-steps-line]').click()
    }
    await say(page, 'ask short')
    budget(8 * STEP_MS + LONG_MS)
    const q = openQuestion(page)
    await expect(q.locator('[data-permission-allow]')).toBeVisible({ timeout: LONG_MS })
    const away = chat.locator('[data-chat-permission-away]')
    await expect(away).toHaveCount(0)
    // Scrolled to the top, the card out of view: the line says what it asks.
    const scroll = chat.locator('[data-chat-scroll]')
    expect(await scroll.evaluate((el) => el.scrollHeight > el.clientHeight + 200), 'the conversation is taller than its pane').toBe(true)
    await scroll.evaluate((el) => (el.scrollTop = 0))
    await expect(away).toBeVisible()
    await expect(away).toContainText('asks: Run a command?')
    // Show it: the card in view, the focus on its first button, and the line gone.
    await away.locator('[data-chat-permission-show]').click()
    await expect(q).toBeInViewport()
    await expect(q.locator('button:not([disabled])').first()).toBeFocused()
    await expect(away).toHaveCount(0)
    // Answered, nothing is left of it.
    await q.locator('[data-permission-deny]').click()
    await expect(chat.locator('[data-permission-question]').last()).toHaveAttribute('data-permission-state', 'denied', { timeout: LONG_MS })
    await scroll.evaluate((el) => (el.scrollTop = 0))
    await expect(away).toHaveCount(0)
  } finally {
    await page.context().close()
  }
})
