import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import type { BrowserContext, Page } from '@playwright/test'
import { expect, showView, test, type Session } from './fixtures'
import { stubFiles } from './held-helpers'

// A sign-in code given to an agent from a phone (#902). The stub signed out (claude-signed-out in conductor.e2e.json) starts on
// Claude Code's sign-in screen, the startup question its entry marks as taking a line: at a phone's width the question bar offers
// a field for it, a refused code has the screen drawn again and asked again, the code it takes signs it in, and the code is kept
// nowhere Conductor keeps anything (the session, the chat, the activity log, the server's log). The terminal's paste control on a
// touch device types its text as keys, with no Enter. Every code here is invented: no real tool runs, nothing signs in.
test.describe.configure({ mode: 'serial' })

/** The code the stub takes (STUB_SIGNIN in conductor.e2e.json) and one it refuses: invented strings. */
const CODE = 'e2e-Code-7Hq2Wx9Kp4'
const WRONG = 'e2e-Wrong-3Fz8Lm1Qd6'

let ctx: BrowserContext | null = null
let phone: Page
const sessions: string[] = []

test.beforeAll(async ({ browser, state }) => {
  ctx = await browser.newContext({ viewport: { width: 360, height: 780 }, hasTouch: true, isMobile: true })
  await ctx.addInitScript(
    ({ token }) => {
      localStorage.setItem('conductor.workbenchToken', token)
      localStorage.setItem('conductor.displayName', 'Nate')
    },
    { token: state.token },
  )
  phone = await ctx.newPage()
})

test.afterAll(async ({ api }) => {
  await ctx?.close()
  for (const id of sessions) await api.stopSession(id)
})

/** The first 8 characters in a row of code that text holds, or '' when it holds none. */
function partOf(text: string, code: string): string {
  for (let i = 0; i + 8 <= code.length; i++) if (text.includes(code.slice(i, i + 8))) return code.slice(i, i + 8)
  return ''
}

test('the sign-in screen takes a code at a phone’s width, a refused one is asked again, and no code is kept', async ({ api, page, state }) => {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude-signed-out', name: 'e2e sign-in' })
  sessions.push(s.id)
  const stub = stubFiles(state.home, s)
  const signIn = async () => {
    const a = (await api.session(s.id)).attention
    return a?.state === 'needs_input' && a.source === 'trust' && a.line === true ? (a.id ?? '') : ''
  }
  await expect.poll(signIn, { timeout: 30_000, message: 'the sign-in screen is asked as a question that takes a line' }).not.toBe('')
  const first = await signIn()

  await phone.goto(`/sessions/${encodeURIComponent(s.id)}`)
  const form = phone.locator('[data-quick-reply-line]')
  await expect(form).toBeVisible({ timeout: 30_000 })
  await expect(phone.locator('[data-quick-reply-message]')).toContainText("Browser didn't open")
  const field = form.getByRole('textbox')
  const send = form.getByRole('button', { name: 'Send' })
  // The bar fits the phone: nothing of it past the window's edge, and the page does not scroll sideways.
  expect(((await form.boundingBox())?.x ?? 0) + ((await form.boundingBox())?.width ?? 0)).toBeLessThanOrEqual(360)
  expect(await phone.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(360)

  // A code the stub refuses: it is typed with its Enter, the field empties, and the screen drawn again is asked again.
  await field.fill(WRONG)
  await expect(field).toHaveValue(WRONG)
  await send.tap()
  await expect(field).toHaveValue('')
  await expect.poll(() => stub.typedLines(), { timeout: 30_000, message: 'the stub refused the code' }).toContain('signin: refused')
  await expect.poll(async () => { const id = await signIn(); return id !== '' && id !== first }, { timeout: 30_000, message: 'asked again' }).toBe(true)

  // The code it takes, typed by the keyboard and sent with its Send: the stub signs in and leaves the screen.
  await expect(form).toBeVisible()
  await field.fill(CODE)
  await expect(field).toHaveValue(CODE)
  await send.tap()
  await expect.poll(() => stub.typedLines(), { timeout: 30_000, message: 'the stub took the code' }).toContain('signin: accepted')
  await expect.poll(async () => (await api.session(s.id)).attention?.source ?? '', { timeout: 30_000, message: 'the sign-in question went' }).not.toBe('trust')
  await expect(form).toBeHidden()
  expect((await api.session(s.id)).lastAnswer).toMatchObject({ byName: 'Nate' })

  // Kept nowhere: the session, the chat and the activity log (the desktop's inspector, both answers listed), the server's log.
  await page.goto(`/sessions/${encodeURIComponent(s.id)}`)
  await showView(page, 'activity')
  const inspector = page.locator('[data-inspector]')
  await expect(inspector.locator('[data-activity="input"]')).toHaveCount(2, { timeout: 30_000 })
  const activity = (await inspector.textContent()) ?? ''
  await showView(page, 'chat')
  await expect(page.locator('[data-inspector] [data-rail-item="chat"]')).toHaveAttribute('aria-pressed', 'true')
  const chat = (await inspector.textContent()) ?? ''
  const described = JSON.stringify(await api.session(s.id))
  const log = existsSync(state.log) ? readFileSync(state.log, 'utf8') : ''
  for (const [where, text] of Object.entries({ activity, chat, described, log })) {
    for (const code of [CODE, WRONG]) expect(partOf(text, code), `${where} holds part of a code`).toBe('')
  }
  // The stub's own transcript says what it did with them, never a code.
  expect(partOf(stub.typedLines(), CODE) + partOf(stub.typedLines(), WRONG)).toBe('')
})

test('the terminal’s paste control on a touch device types its text as keys, with no Enter', async ({ api, state }) => {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e touch paste' })
  sessions.push(s.id)
  const stub = stubFiles(state.home, s)
  await phone.goto(`/sessions/${encodeURIComponent(s.id)}`)
  await expect(phone.locator('[data-transport-state]').first()).toHaveAttribute('data-transport-state', 'open', { timeout: 30_000 })
  // The stub reads key by key from here and keeps the line not yet entered in its .line file.
  const terminal = phone.locator('.terminal-host .xterm-helper-textarea').first()
  await terminal.focus()
  await phone.keyboard.type('stub tui on')
  await phone.keyboard.press('Enter')
  await expect.poll(stub.tui, { timeout: 30_000, message: 'the stub reads as a TUI' }).toBe(true)

  const control = phone.locator('[data-terminal-paste]')
  await expect(control).toBeVisible()
  await control.tap()
  const text = phone.locator('[data-terminal-paste-text]')
  await expect(text).toBeVisible()
  await text.fill('pasted on a phone\n')
  await phone.locator('[data-terminal-paste-send]').tap()
  await expect(text).toBeHidden()
  await expect.poll(stub.pending, { timeout: 30_000, message: 'the text reached the terminal as keys' }).toBe('pasted on a phone')
  expect(stub.enters().some((e) => e.line.includes('pasted on a phone')), 'no Enter was added').toBe(false)
})
