import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Locator, Page } from '@playwright/test'
import { budget, dayPage, expect, openMore, test, useTheme, type Session } from './fixtures'
import { inside, noSideways } from './layout'
import { ownServer, type OwnServer } from './own-server'

// Launch with a first message (the UX review, P19): the Launch dialog's "What should it do?" goes with the launch, the Name field's
// placeholder is the name it gives, the session's page says above the terminal what the message waits for, and the message is
// typed once, after any startup question. On a server of its own: the stub's trust of a folder is written under that server's HOME,
// not the one trust.spec.ts reads (what a child keeps under HOME is shared by every process of that HOME).

let own: OwnServer
const made: string[] = []

test.describe.configure({ mode: 'serial' })

/** The stub's own prompt, "> " and what is typed after it as the last row: what the server recognises before it types a first message. */
const STUB_PROMPT = String.raw`(?m)^> ?[^\n]*\s*\z`

test.beforeAll(async () => {
  // Copies of the two stubs whose prompt the server knows, drawing what is typed in their own row as the agents do (STUB_TUI); the
  // catalog's own stubs have no prompt shape, so they take no message.
  own = await ownServer('first-message', {
    agents: [
      { id: 'prompted-untrusted', from: 'codex-untrusted', env: { STUB_TUI: '1' }, promptShape: STUB_PROMPT },
      { id: 'prompted', from: 'claude', env: { STUB_TUI: '1' }, promptShape: STUB_PROMPT },
    ],
  })
})

const UNTRUSTED = 'Codex CLI, untrusted (e2e stub), prompted-untrusted'
const PROMPTED = 'Claude Code (e2e stub), prompted'

/** Picks the agent named in the open dialog: its radio's name ends with the agent's (the avatar's letters come first). */
async function pick(dialog: Locator, name: string) {
  const radio = dialog.getByRole('radio', { name: new RegExp(`${name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`) })
  await radio.click()
  await expect(radio).toHaveAttribute('aria-checked', 'true')
}

test.afterAll(async () => {
  // The sessions ended before the server and its directories go.
  for (const id of made) {
    await own.api.stopSession(id)
    await expect.poll(async () => (await own.api.call<{ session?: Session }>('GET', `/api/sessions/${id}`)).body?.session?.status ?? 'gone', { timeout: 15_000 }).toMatch(/exited|stopped|gone/)
  }
  await own.stop()
})

/** The Launch dialog opened from the page's Launch agent button. */
async function openLaunch(page: Page): Promise<Locator> {
  await page.getByRole('button', { name: 'Launch agent' }).first().click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByRole('button', { name: 'Launch', exact: true })).toBeVisible()
  return dialog
}

/** Launches from the open dialog and returns the session the server made, held for the end before anything else can fail. */
async function launchFrom(page: Page, dialog: Locator): Promise<Session> {
  const created = page.waitForResponse((r) => r.request().method() === 'POST' && new URL(r.url()).pathname === '/api/sessions')
  await dialog.getByRole('button', { name: 'Launch', exact: true }).click()
  const answer = await created
  const s = (await answer.json()) as Session
  if (s.id) made.push(s.id)
  expect(answer.status(), 'the launch made a session').toBe(201)
  return s
}

/** The lines the stub was typed, read by the agent session's id once the session names it (a listing is a moment: by id). */
async function transcript(id: string): Promise<string[]> {
  const agent = (await own.api.session(id)).agentSession?.id
  const file = agent ? join(own.home, '.stub-sessions', `${agent}.txt`) : ''
  return file && existsSync(file) ? readFileSync(file, 'utf8').split('\n').filter(Boolean) : []
}

test('a first message waits for the trust question, is typed once after it, and names the session', async ({ browser }) => {
  budget(60_000)
  const page = await dayPage(browser, { server: own.started })
  const dialog = await openLaunch(page)
  await pick(dialog, UNTRUSTED)
  await dialog.locator('[data-launch-message] textarea').fill('say hello')
  await expect((await openMore(dialog)).getByRole('textbox', { name: 'Name', exact: true })).toHaveAttribute('placeholder', 'say hello')
  const s = await launchFrom(page, dialog)
  expect(s.name).toBe('say hello')
  await expect(page).toHaveURL(new RegExp(`/sessions/${s.id}$`))
  await expect(page.locator('[data-session-title]')).toHaveText('say hello')

  // The trust question shows first: the strip says the message waits for it, and the bar offers the stub's answer.
  const strip = page.locator('[data-first-message]')
  await expect(strip).toHaveText('Your first message is typed once its question is answered.', { timeout: 30_000 })
  const bar = page.locator('[data-quick-reply]')
  await bar.getByRole('button', { name: 'Yes, trust this folder' }).click()

  // The message typed once, after the answer: the agent says it got it, the strip goes, the transcript holds the line once. Had it
  // been typed into the dialog, its Enter would have answered the question and the transcript would lack it.
  await expect.poll(async () => (await own.api.session(s.id)).attention?.message ?? '', { timeout: 30_000 }).toContain('got: say hello')
  await expect(strip).toHaveCount(0, { timeout: 30_000 })
  await expect.poll(() => transcript(s.id), { timeout: 15_000 }).toEqual(['say hello'])

  // The theme switched and the page drawn again: the same name, and no strip.
  await useTheme(page, 'light')
  await expect(page.locator('[data-session-title]')).toHaveText('say hello')
  await expect(page.locator('[data-first-message]')).toHaveCount(0)
})

test('with no question, the message is typed once the agent is ready, and its first words name the session everywhere', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started })
  const dialog = await openLaunch(page)
  await pick(dialog, PROMPTED)
  const message = 'fix the flaky vim spec on the mac runner please'
  await dialog.locator('[data-launch-message] textarea').fill(message)
  await expect((await openMore(dialog)).getByRole('textbox', { name: 'Name', exact: true })).toHaveAttribute('placeholder', 'fix the flaky vim spec on the mac…')
  const s = await launchFrom(page, dialog)
  await expect(page.locator('[data-session-title]')).toHaveText('fix the flaky vim spec on the mac…')
  await expect(page.locator(`[data-session-list="sidebar"] [data-sidebar-row="s:${s.id}"]`).first()).toContainText('fix the flaky vim spec on the mac…')
  await expect.poll(() => transcript(s.id), { timeout: 30_000 }).toEqual([message])
  await expect(page.locator('[data-first-message]')).toHaveCount(0, { timeout: 15_000 })
})

test('a message past 4,096 bytes says so under the field, and Launch sends nothing', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started })
  const dialog = await openLaunch(page)
  await pick(dialog, PROMPTED)
  let posts = 0
  page.on('request', (r) => {
    if (r.method() === 'POST' && new URL(r.url()).pathname === '/api/sessions') posts++
  })
  await dialog.locator('[data-launch-message] textarea').fill('a'.repeat(4097))
  await expect(dialog.locator('[data-launch-message]')).toContainText('At most 4,096 bytes: this is 4,097.')
  const launch = dialog.getByRole('button', { name: 'Launch', exact: true })
  await expect(launch).toBeDisabled()
  await launch.click({ force: true })
  await dialog.locator('[data-launch-message] textarea').fill('a'.repeat(4096))
  await expect(dialog.locator('[data-launch-message]')).not.toContainText('At most')
  await expect(launch).toBeEnabled()
  expect(posts, 'no launch was sent').toBe(0)
})

test('an agent whose prompt the server does not know is offered no message, and the dialog says why', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started })
  const dialog = await openLaunch(page)
  await pick(dialog, PROMPTED)
  await expect(dialog.locator('[data-launch-message] textarea')).toBeVisible()
  await expect(dialog.locator('[data-launch-no-message]')).toHaveCount(0)
  await dialog.locator('[data-launch-message] textarea').fill('say hello')
  // The catalog's own stub has no prompt shape: no field, and one line in its place saying where its task goes.
  await pick(dialog, 'Plain (e2e stub)')
  await expect(dialog.locator('[data-launch-message]')).toHaveCount(0)
  const line = dialog.locator('[data-launch-no-message]')
  await expect(line).toHaveText('Plain (e2e stub) starts at its prompt. Type your task in its terminal.')
  await expect(dialog).not.toContainText('Conductor types a first message')
  await expect((await openMore(dialog)).getByRole('textbox', { name: 'Name', exact: true })).toHaveAttribute('placeholder', 'Plain (e2e stub) and a number')
  // Launched so, it carries no message: the server would refuse one, and the session is named as any launch is.
  const s = await launchFrom(page, dialog)
  expect(s.name).toMatch(/^Plain \(e2e stub\) #\d+$/)
  expect((s as { firstMessage?: string }).firstMessage).toBeUndefined()
})

test('on a phone the field sits inside the dialog, which does not scroll sideways', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, path: '/sessions', width: 390, height: 844, touch: true })
  const dialog = await openLaunch(page)
  await pick(dialog, PROMPTED)
  const field = dialog.locator('[data-launch-message]')
  await expect(field).toBeVisible()
  // The dialog's body scrolls up and down on a phone: the field brought into view, then measured against the dialog's box.
  await field.scrollIntoViewIfNeeded()
  await inside(field, dialog)
  await noSideways(page, { root: dialog })
})
