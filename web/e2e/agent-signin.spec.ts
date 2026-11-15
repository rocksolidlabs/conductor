import type { BrowserContext, Page } from '@playwright/test'
import { expect, test, type Api, type Session } from './fixtures'
import { stubFiles } from './held-helpers'

// A sign-in code given to an agent from a phone (#902). The stub signed out (signed-out in conductor.e2e.json) starts on Claude
// Code's sign-in screen, a startup question with no choices, and records every byte it reads there. Conductor types nothing into
// it: the person gives the code in the terminal. On a touch device that is the terminal's paste control, which sends the text as
// one paste with no Enter; the person presses Enter. The question bar says to answer in the terminal and opens that control. Every
// code here is invented: no real tool runs, nothing signs in.
test.describe.configure({ mode: 'serial' })

/** The code the stub takes (STUB_SIGNIN in conductor.e2e.json): an invented string. */
const CODE = 'e2e-Code-7Hq2Wx9Kp4'
const PASTE_START = '\u001b[200~'
const PASTE_END = '\u001b[201~'
/** The Right arrow's keys: typed after a paste, once the stub has read them it has read everything before them. */
const RIGHT = '\u001b[C'
/** The hex of text's bytes, as the stub records what it read. */
const hex = (text: string) => Buffer.from(text, 'utf8').toString('hex')

let ctx: BrowserContext | null = null
let phone: Page
const sessions: string[] = []

test.beforeAll(async ({ browser, state }) => {
  ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })
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

/** Opens session id on p and waits for its terminal's connection. */
async function open(p: Page, id: string) {
  await p.goto(`/sessions/${encodeURIComponent(id)}`)
  await expect(p.locator('[data-transport-state]').first()).toHaveAttribute('data-transport-state', 'open', { timeout: 30_000 })
}

/** The signed-out stub's session, once its sign-in screen is asked as a startup question with no choices: the session and its id. */
async function signedOut(api: Api, name: string) {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'signed-out', name })
  sessions.push(s.id)
  const asked = async () => {
    const a = (await api.session(s.id)).attention
    return a?.state === 'needs_input' && a.source === 'trust' && !a.options?.length && (a.message ?? '').includes("Browser didn't open") ? (a.id ?? '') : ''
  }
  await expect.poll(asked, { timeout: 30_000, message: 'the sign-in screen is asked, with no choices' }).not.toBe('')
  return { s, first: await asked() }
}

test('the sign-in screen takes a code pasted on a touch device, as one paste with no Enter; the person’s Enter signs in', async ({ api, state }) => {
  const { s } = await signedOut(api, 'e2e sign-in paste')
  const stub = stubFiles(state.home, s)
  await open(phone, s.id)

  // The question bar says to answer in the terminal, takes no line itself, and its button opens the terminal's paste control.
  const bar = phone.locator('[data-quick-reply-startup]')
  await expect(bar).toContainText('Answer it in the terminal', { timeout: 30_000 })
  await expect(phone.locator('[data-quick-reply] input')).toHaveCount(0)
  expect(await phone.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390)
  await bar.locator('[data-quick-reply-paste]').tap()
  const text = phone.locator('[data-terminal-paste-text]')
  const send = phone.locator('[data-terminal-paste-send]')
  const terminal = phone.locator('.terminal-host .xterm-helper-textarea').first()
  const control = phone.locator('[data-terminal-paste]')

  // A text with line breaks, a tab, ESC, a C1 control and the sequence that ends a paste followed by a digit: the modal says the
  // breaks are taken out, and none of those arrives. The stub (bracketed paste on) reads one paste between its two marks, the
  // digit inside it, and no Enter: the arrow typed after it is read next.
  await text.fill('e2e\nJu\r\nnk\t\u001b[201~1\u0085')
  await expect(phone.locator('[data-terminal-paste-breaks]')).toBeVisible()
  await send.tap()
  await expect(text).toBeHidden()
  await terminal.focus()
  await phone.keyboard.press('ArrowRight')
  await expect
    .poll(() => stub.signin().at(-1), { timeout: 30_000, message: 'the stub read the paste and the arrow after it, and nothing between' })
    .toBe(hex(`${PASTE_START}e2eJunk[201~1${PASTE_END}${RIGHT}`))
  expect(stub.signin(), 'no Enter came of the paste').not.toContain('enter')
  // The person's Enter: the stub refuses that text and draws its screen again.
  await phone.keyboard.press('Enter')
  await expect.poll(() => stub.typedLines(), { timeout: 30_000, message: 'the stub refused the text' }).toContain('signin: refused')

  // The code, through the terminal's own paste control: read as one paste and nothing else, no Enter.
  await expect(control).toBeVisible({ timeout: 30_000 })
  await control.tap()
  await text.fill(CODE)
  await expect(phone.locator('[data-terminal-paste-breaks]')).toBeHidden()
  await send.tap()
  await expect(text).toBeHidden()
  await terminal.focus()
  await phone.keyboard.press('ArrowRight')
  await expect.poll(() => stub.signin().at(-1), { timeout: 30_000, message: 'the stub read the code as one paste' }).toBe(hex(`${PASTE_START}${CODE}${PASTE_END}${RIGHT}`))
  expect(stub.signin().filter((r) => r === 'enter'), 'one Enter so far, the person’s for the refused text').toHaveLength(1)

  // The browser that sent it keeps it nowhere: the control opened again is empty, and neither the page, the app's state nor the
  // browser's storages hold any of it.
  await control.tap()
  await expect(text).toBeVisible()
  await expect(text).toHaveValue('')
  await phone.getByRole('button', { name: 'Cancel' }).tap()
  await expect(text).toBeHidden()
  const kept = {
    page: await phone.evaluate(() => document.body.innerText),
    state: await phone.evaluate(() => {
      const host = Array.from(document.querySelectorAll('*')).find((e) => '__vue_app__' in e) as (Element & { __vue_app__?: { config: { globalProperties: { $nuxt?: { payload?: { state?: unknown } } } } } }) | undefined
      return JSON.stringify(host?.__vue_app__?.config.globalProperties.$nuxt?.payload?.state ?? null)
    }),
    storage: await phone.evaluate(() => JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage } })),
  }
  expect(kept.state, 'the app’s state was read').toContain('typedAnswers')
  for (const [where, held] of Object.entries(kept)) expect(partOf(held, CODE), `the phone's ${where} holds part of the code`).toBe('')

  // Enter pressed on the touch keyboard: the stub takes the code and asks whether to trust the folder, a startup question with
  // choices, where the terminal's paste control is not offered and the question bar has no paste button.
  await terminal.focus()
  await phone.keyboard.press('Enter')
  await expect.poll(() => stub.typedLines(), { timeout: 30_000, message: 'the stub took the code' }).toContain('signin: accepted')
  await expect.poll(async () => (await api.session(s.id)).attention?.message ?? '', { timeout: 30_000, message: 'the trust question follows' }).toContain('Trust this folder')
  await expect(phone.locator('[data-quick-reply-message]')).toContainText('Trust this folder', { timeout: 30_000 })
  await expect(phone.locator('[data-quick-reply]').getByRole('button', { name: /Yes, trust this folder/ })).toBeVisible()
  await expect(control).toBeHidden()
  await expect(phone.locator('[data-quick-reply-paste]')).toHaveCount(0)
})

test('the paste control is tied to the startup question it opened on, and types nothing at one with choices, answered in the terminal or not', async ({ api, page, state }) => {
  const sent: string[] = []
  phone.on('websocket', (ws) => ws.on('framesent', (f) => sent.push(typeof f.payload === 'string' ? f.payload : f.payload.toString('latin1'))))
  const PASTED = 'e2e-Paste-5Rt8'
  const { s, first } = await signedOut(api, 'e2e paste tie')
  const stub = stubFiles(state.home, s)
  const attention = async () => (await api.session(s.id)).attention

  // A pointer's page: the question bar's button opens the paste control too, and its terminal shows no button of its own.
  await open(page, s.id)
  await page.locator('[data-quick-reply-paste]').click({ timeout: 30_000 })
  await expect(page.locator('[data-terminal-paste-text]')).toBeVisible()
  await expect(page.locator('[data-terminal-paste]')).toHaveCount(0)
  await page.getByRole('button', { name: 'Cancel' }).click()
  await expect(page.locator('[data-terminal-paste-text]')).toBeHidden()

  // The phone opens its paste control on the sign-in screen, and a text waits in it.
  await open(phone, s.id)
  await phone.locator('[data-terminal-paste]').tap({ timeout: 30_000 })
  const pasteForm = phone.locator('[data-terminal-paste-shown]')
  await expect(pasteForm).toHaveAttribute('data-terminal-paste-shown', first)
  const text = phone.locator('[data-terminal-paste-text]')
  await text.fill(PASTED)
  const send = phone.locator('[data-terminal-paste-send]')
  const note = phone.locator('[data-terminal-paste-note]')

  // The code typed in the desk's terminal, with its Enter: the stub signs in and asks whether to trust the folder.
  const desk = page.locator('.terminal-host .xterm-helper-textarea').first()
  await desk.focus()
  await page.keyboard.type(CODE)
  await page.keyboard.press('Enter')
  await expect.poll(stub.typedLines, { timeout: 30_000, message: 'the stub took the code' }).toContain('signin: accepted')
  await expect.poll(async () => (await attention())?.message ?? '', { timeout: 30_000, message: 'the trust question' }).toContain('Trust this folder')
  const trust = (await attention())?.id ?? ''
  // The phone was told: its control says to answer with the choices. Type it types nothing and says the screen changed; again,
  // nothing, with the choices' words.
  await expect(pasteForm).toHaveAttribute('data-terminal-paste-shown', trust, { timeout: 30_000 })
  await expect(note).toContainText('answer it with the question’s choices')
  await send.tap()
  await expect(note).toContainText('The screen changed since this opened: nothing was typed')
  await send.tap()
  await expect(note).toContainText('answer it with the question’s choices')

  // The trust question answered in the desk's terminal while its screen stays drawn (the stub stopped, so that it reads nothing
  // yet): the phone is told it was answered, and its control still types nothing.
  await expect.poll(stub.pid, { timeout: 30_000, message: 'the stub wrote its pid' }).toBeGreaterThan(0)
  const pid = stub.pid()
  process.kill(pid, 'SIGSTOP')
  try {
    await desk.focus()
    await page.keyboard.press('Enter')
    await expect.poll(async () => (await attention())?.answered ?? '', { timeout: 30_000, message: 'answered in the terminal' }).toBe('terminal')
    expect((await attention())?.id, 'the trust question still shows').toBe(trust)
    await expect(pasteForm).toHaveAttribute('data-terminal-paste-answered', 'terminal', { timeout: 30_000 })
    await send.tap()
    await expect(note).toContainText('answer it with the question’s choices')
    await expect(text).toHaveValue(PASTED)
    await phone.getByRole('button', { name: 'Cancel' }).tap()
    await expect(text).toBeHidden()
    await expect(phone.locator('[data-terminal-paste-shown]')).toHaveCount(0)
    await expect(phone.locator('[data-terminal-paste]')).toBeHidden()
  } finally {
    process.kill(pid, 'SIGCONT')
  }

  // The stub reads the desk's Enter and trusts the folder: the control is offered again. A line typed after it is entered as it was
  // typed, nothing of the text before it: the phone never sent the text.
  await expect.poll(async () => (await attention())?.state ?? '', { timeout: 30_000, message: 'the trust question went' }).not.toBe('needs_input')
  await expect(phone.locator('[data-terminal-paste]')).toBeVisible({ timeout: 30_000 })
  await desk.focus()
  await page.keyboard.type('e2e after the trust')
  await page.keyboard.press('Enter')
  await expect.poll(() => stub.entered('e2e after the trust'), { timeout: 30_000, message: 'the line after the trust question' }).toBe(true)
  expect(stub.typedLines()).not.toContain(PASTED)
  expect(sent.some((f) => f.includes(PASTED)), 'the phone sent the text').toBe(false)
})
