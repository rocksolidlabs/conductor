import { writeFileSync } from 'node:fs'
import { atRest, dayPage, expect, test } from './fixtures'
import { budget, chatServer, firstTurn, joined, launch, LONG_MS, openSession, readStub, say, STEP_MS, stubFile, toChat } from './chat-fixtures'
import { inside, uncut } from './layout'
import type { BrowserContext, Page } from '@playwright/test'
import type { OwnServer } from './own-server'

// `/` in the Chat box offers the agent's own commands (UX3 R11), on a server of the spec's own with the chat's stub, whose made-up
// list is compact, clear, model and fix-issue, and whose <conversation>.commands file adds commands (a `^` line first, a file of
// one `-` lists none): the list is the server's for this connection; from an open list Enter and Tab complete the highlighted row
// and never send, a held Enter sends nothing, and a line is sent only by an Enter with the list closed; no list while the box
// answers a question, none for an empty list; a new list that re-orders keeps the highlighted name; a pick the person made is
// theirs and one a script made is not; long names that differ at their end are told apart at 390; a control link's box opens no
// list.
test.describe.configure({ mode: 'serial' })

let own: OwnServer
const sessions: string[] = []
const guests: BrowserContext[] = []

test.beforeAll(async () => {
  test.setTimeout(120_000)
  own = await chatServer('chat-commands')
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
  return { id: s.id, conv }
}

const box = (page: Page) => page.locator('[data-chat-view] [data-chat-box] textarea')
const menu = (page: Page) => page.locator('[data-chat-view] [data-chat-menu][data-chat-menu-kind="command"]')
const row = (page: Page, name: string) => menu(page).locator(`[data-chat-menu-row="${name}"]`)
const activeRow = (page: Page) => menu(page).locator('[data-chat-menu-active]')
const marks = (conv: string) => readStub(own, conv, 'marks').trim().split('\n').filter(Boolean)
/** The stub's marks once there are n of them: the message's own. */
async function markOf(conv: string, n: number): Promise<string | undefined> {
  await expect.poll(() => marks(conv).length, { timeout: LONG_MS }).toBe(n)
  return marks(conv).pop()
}

test('from an open list Enter completes the highlighted row and sends nothing; a line goes only by an Enter with the list closed', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  try {
    const { conv } = await aChat(page, 'e2e commands')
    writeFileSync(stubFile(own, conv, 'commands'), 'clear-history|Forget the saved history|\n')
    budget(16 * STEP_MS + 4 * LONG_MS)
    const n = marks(conv).length
    await box(page).click()
    await page.keyboard.type('/')
    for (const name of ['compact', 'clear', 'model', 'fix-issue']) await expect(row(page, name)).toBeVisible({ timeout: LONG_MS })
    await expect(row(page, 'clear-history')).toBeVisible({ timeout: LONG_MS })
    // A whole name typed, the highlight moved to another row: Enter completes that row, nothing is sent.
    await page.keyboard.type('clear')
    await expect(activeRow(page)).toHaveAttribute('data-chat-menu-row', 'clear')
    await page.keyboard.press('ArrowDown')
    await expect(activeRow(page)).toHaveAttribute('data-chat-menu-row', 'clear-history')
    await page.keyboard.press('Enter')
    await expect(box(page)).toHaveValue('/clear-history ')
    await expect(menu(page)).toHaveCount(0)
    // The cursor in the middle of the word: Enter completes the highlighted row, whatever follows the cursor.
    await box(page).fill('')
    await page.keyboard.type('/clearx')
    await page.keyboard.press('ArrowLeft')
    await expect(row(page, 'clear')).toBeVisible()
    await expect(activeRow(page)).toHaveAttribute('data-chat-menu-row', 'clear')
    await page.keyboard.press('Enter')
    await expect(box(page)).toHaveValue('/clear ')
    // `/`, Enter held down: the first press completes, the repeats send nothing.
    await box(page).fill('')
    await page.keyboard.type('/')
    await expect(activeRow(page)).toHaveAttribute('data-chat-menu-row', 'compact')
    await page.keyboard.down('Enter')
    await page.keyboard.down('Enter')
    await page.keyboard.down('Enter')
    await page.keyboard.up('Enter')
    await expect(box(page)).toHaveValue('/compact ')
    // Nothing went in all of that.
    expect(marks(conv).length).toBe(n)
    // With the list closed, the person's own Enter sends: the completed line is theirs.
    await page.keyboard.press('Enter')
    expect(await markOf(conv, n + 1)).toBe('unmarked')
    await expect(box(page)).toHaveValue('')
    // A completion and a command's argument, sent: the agent runs it.
    await page.keyboard.type('/fi')
    await page.keyboard.press('Tab')
    await expect(box(page)).toHaveValue('/fix-issue ')
    await page.keyboard.type('12')
    await page.keyboard.press('Enter')
    expect(await markOf(conv, n + 2)).toBe('unmarked')
    await expect(page.locator('[data-chat-words]').filter({ hasText: 'stub ran: /fix-issue 12' }).last()).toBeVisible({ timeout: LONG_MS })
    // A pick a script made is not the person's: the line goes as written.
    await page.keyboard.type('/mod')
    await expect(row(page, 'model')).toBeVisible()
    await row(page, 'model').evaluate((el: HTMLElement) => el.click())
    await expect(box(page)).toHaveValue('/model ')
    await page.keyboard.press('Enter')
    expect(await markOf(conv, n + 3)).toBe('marked')
  } finally {
    await page.context().close()
  }
})

test('no list while the box answers a question, none for a line no command starts, and none for an empty list', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  try {
    const { conv } = await aChat(page, 'e2e commands answer')
    budget(10 * STEP_MS + 4 * LONG_MS)
    // A line no listed command starts: no list, no words of the page's own.
    await box(page).click()
    await page.keyboard.type('/etc/hosts')
    await expect(box(page)).toHaveValue('/etc/hosts')
    await expect(page.locator('[data-chat-view] [data-chat-menu]')).toHaveCount(0)
    await box(page).fill('')
    // The agent asks: the box's line is an answer, and `/` offers nothing.
    await say(page, 'a question please')
    await expect(page.locator('[data-quick-reply] [data-quick-reply-message]')).toHaveText('Shall I go on?', { timeout: LONG_MS })
    await box(page).click()
    await page.keyboard.type('/')
    await expect(box(page)).toHaveValue('/')
    await page.keyboard.type('c')
    await expect(box(page)).toHaveValue('/c')
    await expect(page.locator('[data-chat-view] [data-chat-menu]')).toHaveCount(0)
    await page.keyboard.press('Enter')
    await expect(page.locator('[data-quick-reply]')).toHaveCount(0, { timeout: LONG_MS })
    // An empty list sent by the agent: none is offered.
    await expect(box(page)).toHaveValue('')
    await page.keyboard.type('/')
    await expect(row(page, 'compact')).toBeVisible({ timeout: LONG_MS })
    await page.keyboard.press('Escape')
    await box(page).fill('')
    writeFileSync(stubFile(own, conv, 'commands'), '-\n')
    await expect
      .poll(
        async () => {
          await box(page).fill('')
          await box(page).click()
          await page.keyboard.type('/')
          return page.locator('[data-chat-view] [data-chat-menu]').count()
        },
        { timeout: LONG_MS },
      )
      .toBe(0)
  } finally {
    await page.context().close()
  }
})

test('a new list that re-orders keeps the highlighted name; a control link opens no list', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  try {
    const { id, conv } = await aChat(page, 'e2e commands added')
    budget(10 * STEP_MS + 4 * LONG_MS)
    await box(page).click()
    await page.keyboard.type('/')
    await expect(row(page, 'compact')).toBeVisible({ timeout: LONG_MS })
    await page.keyboard.press('ArrowDown')
    await expect(activeRow(page)).toHaveAttribute('data-chat-menu-row', 'clear')
    // The agent's list again, a command put first: the rows move under the highlight, which stays on its name.
    writeFileSync(stubFile(own, conv, 'commands'), '^triage|Sort the open issues|<label>\n')
    await expect(menu(page).locator('[data-chat-menu-row]').first()).toHaveAttribute('data-chat-menu-row', 'triage', { timeout: LONG_MS })
    await expect(activeRow(page)).toHaveAttribute('data-chat-menu-row', 'clear')
    await page.keyboard.press('Escape')
    await expect(menu(page)).toHaveCount(0)

    const c = await joined(browser, own, id, 'control')
    guests.push(c.ctx)
    await expect(box(c.g)).toBeVisible({ timeout: LONG_MS })
    await box(c.g).click()
    await c.g.keyboard.type('/co')
    await expect(box(c.g)).toHaveValue('/co')
    await expect(c.g.locator('[data-chat-menu]')).toHaveCount(0)
  } finally {
    await page.context().close()
  }
})

test('long names that differ at their end are told apart, the rows whole and inside the window at 2000 x 810, 1440 x 900 and 390 x 844', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 2000, height: 810 })
  try {
    const { conv } = await aChat(page, 'e2e commands sizes')
    const long = ['shop-api-folder-tools-clean-the-old-build-cache', 'shop-api-folder-tools-clean-the-old-build-logs']
    writeFileSync(stubFile(own, conv, 'commands'), long.map((n) => `${n}|Tidy the folder|`).join('\n') + '\n')
    for (const size of [{ width: 2000, height: 810 }, { width: 1440, height: 900 }, { width: 390, height: 844 }]) {
      budget(8 * STEP_MS + LONG_MS)
      await page.setViewportSize(size)
      await box(page).fill('')
      await box(page).click()
      await page.keyboard.type('/shop')
      for (const n of long) await expect(row(page, n)).toBeVisible({ timeout: LONG_MS })
      await atRest(menu(page))
      const names = menu(page).locator('[data-chat-menu-name]')
      await expect(names).toHaveText(long.map((n) => `/${n}`))
      await uncut(names)
      await inside(menu(page), page.locator('body'))
      await page.keyboard.press('Escape')
      await expect(menu(page)).toHaveCount(0)
    }
  } finally {
    await page.context().close()
  }
})
