import { writeFileSync } from 'node:fs'
import { atRest, dayPage, expect, test } from './fixtures'
import { budget, chatServer, firstTurn, joined, launch, LONG_MS, openSession, readStub, STEP_MS, stubFile, toChat } from './chat-fixtures'
import { inside, uncut } from './layout'
import type { BrowserContext, Page } from '@playwright/test'
import type { OwnServer } from './own-server'

// `/` in the Chat box offers the agent's own commands (UX3 R11), on a server of the spec's own with the chat's stub, whose made-up
// list is compact, clear, model and fix-issue, and whose <conversation>.commands file adds one: the list as the server sent it to the
// operator's own connection; typing narrows it; Enter completes, and a whole name sends at once; a command added while the list is
// closed and while it is open appears; a control link's box opens no list; rows whole and inside the window at 2000 x 810,
// 1440 x 900 and 390 x 844, at rest.
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
const marks = (conv: string) => readStub(own, conv, 'marks').trim().split('\n').filter(Boolean)

test('/ lists the agent\'s commands; typing narrows them, Enter completes, a whole name sends at once, and the agent runs it', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  try {
    const { conv } = await aChat(page, 'e2e commands')
    budget(14 * STEP_MS + 4 * LONG_MS)
    await box(page).click()
    await page.keyboard.type('/')
    for (const name of ['compact', 'clear', 'model', 'fix-issue']) await expect(row(page, name)).toBeVisible({ timeout: LONG_MS })
    await page.keyboard.type('fi')
    await expect(menu(page).locator('[data-chat-menu-row]')).toHaveCount(1)
    await expect(row(page, 'fix-issue')).toBeVisible()
    await page.keyboard.press('Enter')
    await expect(menu(page)).toHaveCount(0)
    await expect(box(page)).toHaveValue('/fix-issue ')
    await page.keyboard.type('12')
    let n = marks(conv).length
    await page.keyboard.press('Enter')
    await expect.poll(() => marks(conv).length, { timeout: LONG_MS }).toBe(n + 1)
    expect(marks(conv).pop()).toBe('unmarked')
    await expect(page.locator('[data-chat-words]').filter({ hasText: 'stub ran: /fix-issue 12' }).last()).toBeVisible({ timeout: LONG_MS })
    // A whole name: one Enter sends it.
    n = marks(conv).length
    await page.keyboard.type('/compact')
    await expect(row(page, 'compact')).toBeVisible()
    await page.keyboard.press('Enter')
    await expect.poll(() => marks(conv).length, { timeout: LONG_MS }).toBe(n + 1)
    expect(marks(conv).pop()).toBe('unmarked')
    await expect(box(page)).toHaveValue('')
    // A name the agent does not list: said, and sent as typed with Enter.
    await page.keyboard.type('/nothing-by-that')
    await expect(menu(page).locator('[data-chat-menu-foot]')).toHaveText('No command by that name. It is sent as you typed it.')
    await page.keyboard.press('Escape')
    await expect(menu(page)).toHaveCount(0)
    await expect(box(page)).toHaveValue('/nothing-by-that')
  } finally {
    await page.context().close()
  }
})

test('a command the agent adds appears, with the list closed and with it open; a control link opens no list', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  try {
    const { id, conv } = await aChat(page, 'e2e commands added')
    budget(10 * STEP_MS + 4 * LONG_MS)
    writeFileSync(stubFile(own, conv, 'commands'), 'triage|Sort the open issues|<label>\n')
    await box(page).click()
    await page.keyboard.type('/tri')
    await expect(row(page, 'triage')).toBeVisible({ timeout: LONG_MS })
    await page.keyboard.press('Backspace')
    await page.keyboard.press('Backspace')
    await page.keyboard.press('Backspace')
    await expect(row(page, 'compact')).toBeVisible()
    // Added while it is open: its row comes, the highlighted one kept.
    await page.keyboard.press('ArrowDown')
    const active = await menu(page).locator('[data-chat-menu-active]').getAttribute('data-chat-menu-row')
    writeFileSync(stubFile(own, conv, 'commands'), 'triage|Sort the open issues|<label>\nrelease|Cut a release|\n')
    await expect(row(page, 'release')).toBeVisible({ timeout: LONG_MS })
    await expect(menu(page).locator('[data-chat-menu-active]')).toHaveAttribute('data-chat-menu-row', active!)
    await page.keyboard.press('Escape')

    const c = await joined(browser, own, id, 'control')
    guests.push(c.ctx)
    await expect(box(c.g)).toBeVisible({ timeout: LONG_MS })
    await box(c.g).click()
    await c.g.keyboard.type('/')
    await expect(box(c.g)).toHaveValue('/')
    await expect(c.g.locator('[data-chat-menu]')).toHaveCount(0)
  } finally {
    await page.context().close()
  }
})

test('the rows are whole and inside the window at 2000 x 810, 1440 x 900 and 390 x 844, measured at rest', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 2000, height: 810 })
  try {
    await aChat(page, 'e2e commands sizes')
    for (const size of [{ width: 2000, height: 810 }, { width: 1440, height: 900 }, { width: 390, height: 844 }]) {
      budget(6 * STEP_MS + LONG_MS)
      await page.setViewportSize(size)
      await box(page).fill('')
      await box(page).click()
      await page.keyboard.type('/')
      await expect(row(page, 'fix-issue')).toBeVisible({ timeout: LONG_MS })
      await atRest(menu(page))
      await uncut(menu(page).locator('[data-chat-menu-row] [data-mono="command"]'))
      await inside(menu(page), page.locator('body'))
      await page.keyboard.press('Escape')
      await expect(menu(page)).toHaveCount(0)
    }
  } finally {
    await page.context().close()
  }
})
