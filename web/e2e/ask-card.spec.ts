import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import type { Locator, Page } from '@playwright/test'
import { walk, worst } from './contrast'
import { atRest, dayPage, expect, test, useTheme, type Api, type Session } from './fixtures'
import { BOX_SLACK, tapTarget, uncut } from './layout'
import { budget, chatServer, firstTurn, launch, LONG_MS, openSession, say, STEP_MS, toChat } from './chat-fixtures'
import type { OwnServer } from './own-server'

// The card for an agent's question (UX3 R3): one look wherever it is drawn: amber, `<agent> asks:` then the question whole, the
// choices beside it when they fit on its row and under it when they do not, each a 44 px target under a finger. In the Terminal it
// sits under the terminal; in Chat just above the chat's box, inside the chat's column. Measured at rest, at 2000 x 810, 1440 x 900 and 390 x 844, in both themes; the session found by its id.

const made: string[] = []

test.afterEach(async ({ api }) => {
  for (const id of made.splice(0)) await api.stopSession(id)
})

const QUESTION = 'Round the odd cent onto the first payment, or the last?'
const CHOICES = ['The first payment', 'The last payment', 'Wait for the tests']

async function asking(api: Api, root: string, name: string): Promise<Session> {
  const cwd = join(root, 'ask-card')
  mkdirSync(cwd, { recursive: true })
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name, cwd })
  made.push(s.id)
  await api.ok('POST', `/api/sessions/${s.id}/attention`, { state: 'needs_input', message: QUESTION, options: CHOICES.map((label, i) => ({ label, input: String(i + 1) })) })
  return s
}

/** The card's look: its words whole after `<agent> asks:`, every choice whole, the words over the amber tint at their floor. */
async function looksRight(page: Page, card: Locator, at: string): Promise<void> {
  await expect(card).toBeVisible({ timeout: 30_000 })
  await atRest(card)
  const words = card.locator('[data-quick-reply-message]')
  await expect(words).toHaveText(QUESTION)
  expect(await words.evaluate((e) => getComputedStyle(e, '::before').content), `${at}: the heading leads the question`).toMatch(/^".+ asks: "$/)
  await uncut(words)
  const choices = card.getByRole('button')
  await expect(choices).toHaveCount(3)
  await uncut(choices)
  const { failures } = await walk(page, card)
  expect(failures, `${at}: the card's words over its tint\n${worst(failures)}`).toEqual([])
}

/** Where the choices sit: beside the question (their row meets its first line) or under it. */
async function choicesAre(card: Locator): Promise<'beside' | 'under'> {
  const words = (await card.locator('[data-quick-reply-message]').boundingBox())!
  const first = (await card.getByRole('button').first().boundingBox())!
  return first.y >= words.y + words.height - BOX_SLACK ? 'under' : 'beside'
}

for (const size of [
  { width: 2000, height: 810 },
  { width: 1440, height: 900 },
]) {
  test(`in the Terminal the card sits under the terminal with its choices beside the question, at ${size.width} x ${size.height}, in both themes`, async ({ browser, api, state }) => {
    const s = await asking(api, state.root, `e2e ask card ${size.width}`)
    const page = await dayPage(browser, { server: state, path: `/sessions/${s.id}`, ...size })
    const card = page.locator('[data-quick-reply]')
    for (const theme of ['dark', 'light'] as const) {
      if (theme === 'light') await useTheme(page, 'light')
      await looksRight(page, card, `${size.width}, ${theme}`)
      const slot = (await page.locator('[data-terminal-slot]').boundingBox())!
      expect((await card.boundingBox())!.y, 'under the terminal').toBeGreaterThanOrEqual(slot.y + slot.height - BOX_SLACK)
      expect(await choicesAre(card)).toBe('beside')
    }
  })
}

test('on a phone the choices go under the question, each a 44 px target, on the session page and on a control link', async ({ browser, api, state }) => {
  const s = await asking(api, state.root, 'e2e ask card phone')
  const page = await dayPage(browser, { server: state, path: `/sessions/${s.id}`, width: 390, height: 844, touch: true })
  const card = page.locator('[data-quick-reply]')
  await looksRight(page, card, 'the session page at 390')
  expect(await choicesAre(card)).toBe('under')
  await tapTarget(card.getByRole('button'))

  // A share link's guest on a phone answers on the same card.
  const { token } = await api.ok<{ token: string }>('POST', `/api/sessions/${s.id}/links`, { role: 'control' })
  const ctx = await browser.newContext({ baseURL: state.baseURL, viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })
  try {
    await ctx.addInitScript(() => localStorage.setItem('conductor.displayName', 'Priya'))
    const g = await ctx.newPage()
    await g.goto(`/join/${token}`)
    await g.getByRole('button', { name: 'Join session' }).click()
    const guest = g.locator('[data-quick-reply]')
    await looksRight(g, guest, 'the join page at 390')
    expect(await choicesAre(guest)).toBe('under')
    await tapTarget(guest.getByRole('button'))
  } finally {
    await ctx.close()
  }
})

// In Chat (the chat's stub on a server of its own): the card sits just above the box, inside the chat's column, with the question
// and its choices whole; again after a switch of theme, which draws the page anew.
test.describe('in Chat', () => {
  test.describe.configure({ mode: 'serial' })
  let own: OwnServer
  const chats: string[] = []

  test.beforeAll(async () => {
    test.setTimeout(120_000)
    own = await chatServer('ask-card')
  })

  test.afterAll(async () => {
    for (const id of chats) await own.api.stopSession(id)
    await own.stop()
  })

  for (const size of [
    { width: 2000, height: 810 },
    { width: 1440, height: 900 },
    { width: 390, height: 844 },
  ]) {
    test(`the card sits just above the box, in the chat's column, at ${size.width} x ${size.height}, in both themes`, async ({ browser }) => {
      const { s } = await launch(own.api, chats, `e2e ask card chat ${size.width}`)
      const page = await dayPage(browser, { server: own.started, ...size })
      try {
        const view = await openSession(page, s.id)
        await firstTurn(page, own.api, s.id)
        await toChat(own.api, s.id, view)
        await say(page, 'a question choices please')
        for (const theme of ['dark', 'light'] as const) {
          budget(LONG_MS + 6 * STEP_MS)
          if (theme === 'light') {
            await useTheme(page, 'light', '[data-session-way]')
            await expect(page.locator('[data-session-way]').first()).toHaveAttribute('data-session-way', 'protocol', { timeout: LONG_MS })
          }
          const chat = page.locator('[data-chat-view]')
          const card = chat.locator('[data-chat-ask] [data-quick-reply]')
          await expect(card.locator('[data-quick-reply-message]')).toHaveText('Pears or plums?', { timeout: LONG_MS })
          await atRest(card)
          await uncut(card.locator('[data-quick-reply-message]'))
          await uncut(card.getByRole('button'))
          const { failures } = await walk(page, card)
          expect(failures, `${size.width}, ${theme}: the card's words over its tint\n${worst(failures)}`).toEqual([])
          // Only one card: none under the pane.
          await expect(page.locator('[data-quick-reply]')).toHaveCount(1)
          const [c, box, column] = await Promise.all([card.boundingBox(), chat.locator('[data-chat-box] textarea').boundingBox(), chat.locator('[data-chat-foot]').boundingBox()])
          expect(c!.y + c!.height, `${size.width}, ${theme}: the card's bottom above the box's top`).toBeLessThanOrEqual(box!.y + BOX_SLACK)
          expect(c!.x, 'inside the column').toBeGreaterThanOrEqual(column!.x - BOX_SLACK)
          expect(c!.x + c!.width, 'inside the column').toBeLessThanOrEqual(column!.x + column!.width + BOX_SLACK)
          expect(c!.y, 'inside the column').toBeGreaterThanOrEqual(column!.y - BOX_SLACK)
        }
      } finally {
        await page.context().close()
      }
    })
  }
})
