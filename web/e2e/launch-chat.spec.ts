import { existsSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Locator, Page } from '@playwright/test'
import { budget, dayPage, expect, test, type Session } from './fixtures'
import { ownServer, type OwnServer } from './own-server'

// A launch as a chat (issue 1174), on a server of the spec's own: the Launch agent dialog's Run as: Terminal | Chat, for a copy of
// the Claude Code stub with its chat declared and its prompt known (claude-proto: web/e2e/stub-agent.sh in its terminal,
// web/e2e/stub-protocol.mjs as its chat's child, one transcript under the server's $HOME/.stub-sessions). A launch with Chat starts in
// its terminal, its switch saying it moves to Chat after the agent's first reply; the task, typed into the terminal as the launch's
// first message, is a turn held at the stub's gate (`stub hold`), so the page is judged while the move waits, not raced; let go, the
// turn's end moves the session to Chat, and the task was typed once. The choice comes back as the agent was last launched.
test.describe.configure({ mode: 'serial' })

let own: OwnServer
const made: string[] = []

/** The stub's own prompt, "> " and what is typed after it as the last row: what the server recognises before it types a first message. */
const STUB_PROMPT = String.raw`(?m)^> ?[^\n]*\s*\z`
const CHAT_AGENT = 'Claude Code (e2e stub), claude-proto'
/** The catalog's own Claude Code stub, with no chat declared. */
const PLAIN_AGENT = 'Claude Code (e2e stub)'
/** The caps of a step's waits (AGENTS.md, "A test's timeout bounds a hang, not its steps"). */
const STEP_MS = 15_000
const LONG_MS = 30_000

test.beforeAll(async () => {
  test.setTimeout(120_000)
  own = await ownServer('launch-chat', {
    agents: [
      {
        id: 'claude-proto',
        from: 'claude',
        env: { STUB_CLAUDE_VERSION: '2.1.331', STUB_NODE: process.execPath, STUB_TUI: '1' },
        promptShape: STUB_PROMPT,
        protocol: { name: 'claude-stream-json' },
      },
    ],
  })
})

test.afterAll(async () => {
  // The held turns let go and the sessions ended before the server and its directories go.
  for (const id of made) {
    const conv = (await own.api.call<{ session?: Session }>('GET', `/api/sessions/${id}`)).body?.session?.agentSession?.id
    if (conv && existsSync(stubFile(conv, 'hold'))) writeFileSync(stubFile(conv, 'hold'), 'stop\n')
    await own.api.stopSession(id)
  }
  await own.stop()
})

function stubFile(conv: string, ext: string): string {
  return join(own.home, '.stub-sessions', `${conv}.${ext}`)
}

/** The lines the stub was typed and its chat was told, from the conversation's transcript (by its id: a listing is a moment). */
function transcript(conv: string): string[] {
  const p = stubFile(conv, 'txt')
  return existsSync(p) ? readFileSync(p, 'utf8').split('\n').filter(Boolean) : []
}

/** The Launch dialog opened from the page's Launch agent button. */
async function openLaunch(page: Page): Promise<Locator> {
  budget(2 * STEP_MS)
  await page.getByRole('button', { name: 'Launch agent' }).first().click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByRole('button', { name: 'Launch', exact: true })).toBeVisible()
  return dialog
}

/** Picks the agent named in the open dialog: its radio's name ends with the agent's (the avatar's letters come first). */
async function pick(dialog: Locator, name: string) {
  budget(2 * STEP_MS)
  const radio = dialog.getByRole('radio', { name: new RegExp(`${name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`) })
  await radio.click()
  await expect(radio).toHaveAttribute('aria-checked', 'true')
}

/** The dialog's Run as, and its two sides. */
function runAs(dialog: Locator) {
  const choice = dialog.locator('[data-launch-run-as]')
  return { choice, terminal: choice.locator('[data-run-as-side="terminal"]'), chat: choice.locator('[data-run-as-side="protocol"]') }
}

/**
 * The sizes of the dialog and of its Run as, measured at rest (AGENTS.md, "Measure a thing at rest"): once the dialog's own
 * animations (its opening) have ended. Sizes, not places: the dialog is centred, so its place follows its height.
 */
async function sizes(dialog: Locator): Promise<{ dialog: number; width: number; height: number }> {
  await dialog.evaluate((el) => Promise.all(el.getAnimations({ subtree: true }).map((a) => a.finished)))
  const [d, c] = await Promise.all([dialog.boundingBox(), runAs(dialog).choice.boundingBox()])
  return { dialog: d!.height, width: c!.width, height: c!.height }
}

/** Launches from the open dialog and returns the session the server made, held for the end before anything else can fail. */
async function launchFrom(page: Page, dialog: Locator): Promise<Session> {
  budget(2 * STEP_MS)
  const created = page.waitForResponse((r) => r.request().method() === 'POST' && new URL(r.url()).pathname === '/api/sessions')
  await dialog.getByRole('button', { name: 'Launch', exact: true }).click()
  const answer = await created
  const s = (await answer.json()) as Session
  if (s.id) made.push(s.id)
  expect(answer.status(), 'the launch made a session').toBe(201)
  return s
}

/** The session page's connection, once its welcome is read (data-welcomed: set by the welcome alone). */
async function welcomed(page: Page, id: string): Promise<Locator> {
  budget(STEP_MS + LONG_MS)
  await expect(page).toHaveURL(new RegExp(`/sessions/${id}$`), { timeout: STEP_MS })
  const view = page.locator('[data-session-way]').first()
  await expect(view).toHaveAttribute('data-welcomed', 'true', { timeout: LONG_MS })
  return view
}

/** The launch's task, typed into the terminal as its first message and held at the stub's gate: the conversation, once held. */
async function held(own: OwnServer, s: Session): Promise<string> {
  budget(LONG_MS)
  const conv = s.agentSession?.id ?? ''
  expect(conv).not.toBe('')
  await expect.poll(() => (existsSync(stubFile(conv, 'hold')) ? readFileSync(stubFile(conv, 'hold'), 'utf8').trim() : ''), { timeout: LONG_MS }).toBe('hold')
  return conv
}

test('a launch as Chat starts in its terminal, says it moves, and is a chat after the first reply, its task typed once', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  try {
    const dialog = await openLaunch(page)
    await pick(dialog, CHAT_AGENT)
    budget(10 * STEP_MS)
    // The first time: Terminal, in the header switch's order; the choice alone, its text the two sides' and nothing more.
    const r = runAs(dialog)
    await expect(r.choice).toBeVisible()
    await expect(r.choice.getByRole('radio')).toHaveText(['Terminal', 'Chat'])
    await expect(r.terminal).toHaveAttribute('aria-checked', 'true')
    const asTerminal = await sizes(dialog)
    // One press picks Chat: the picked side shows, and nothing is added: the dialog does not grow. The arrow keys move between the two.
    await r.chat.click()
    await expect(r.chat).toHaveAttribute('aria-checked', 'true')
    expect((await r.choice.innerText()).replace(/\s+/g, ' ').trim()).toBe('Run as Terminal Chat')
    expect(await sizes(dialog)).toEqual(asTerminal)
    await r.chat.press('ArrowLeft')
    await expect(r.terminal).toHaveAttribute('aria-checked', 'true')
    await expect(r.terminal).toBeFocused()
    await r.terminal.press('ArrowRight')
    await expect(r.chat).toHaveAttribute('aria-checked', 'true')
    await expect(r.chat).toBeFocused()
    await dialog.locator('[data-launch-message] textarea').fill('stub hold')
    const s = await launchFrom(page, dialog)
    expect(s.way).toBeUndefined()

    // In its terminal, the move noted: the switch says it moves to Chat after the first reply, and Terminal may still be pressed.
    const view = await welcomed(page, s.id)
    budget(8 * STEP_MS + 3 * LONG_MS)
    await expect(view).toHaveAttribute('data-session-way', 'terminal')
    await expect(view).toHaveAttribute('data-way-noted', 'protocol')
    const conv = await held(own, s)
    const sw = page.locator('[data-way-switch]')
    await expect(sw).toHaveAttribute('data-way', 'terminal')
    await expect(sw).toHaveAttribute('data-noted', 'protocol')
    await expect(page.locator('[data-way-line]')).toHaveText("Moves to Chat after the agent's first reply.")
    await expect(sw.locator('[data-way-side="terminal"]')).toBeEnabled()
    await expect(page.locator('[data-chat-view]')).toHaveCount(0)
    expect(transcript(conv)).toEqual(['stub hold'])

    // The first reply ends: the session moves to Chat by itself, with the move's notice and the note on the vendor's terms.
    writeFileSync(stubFile(conv, 'hold'), 'stop\n')
    await expect(view).toHaveAttribute('data-session-way', 'protocol', { timeout: LONG_MS })
    await expect(view).not.toHaveAttribute('data-way-noted', /./)
    const chat = page.locator('[data-chat-view]')
    await expect(chat).toBeVisible()
    await expect(chat.locator('[data-chat-notice]').first()).toContainText('Moved to Chat.')
    await expect(chat.locator('[data-chat-terms]')).toBeVisible()
    await expect(sw).toHaveAttribute('data-way', 'protocol')
    await expect.poll(async () => (await own.api.session(s.id)).way, { timeout: LONG_MS }).toBe('protocol')

    // The chat answers, and the task was typed once, in the terminal: the chat was never handed it again.
    budget(4 * STEP_MS + LONG_MS)
    const box = chat.locator('[data-chat-box] textarea').first()
    await box.fill('hello there')
    await box.press('Enter')
    const turn = chat.locator('[data-chat-turn]').filter({ hasText: 'stub chat: hello there' }).last()
    await expect(turn).toHaveAttribute('data-turn-outcome', 'done', { timeout: LONG_MS })
    expect(transcript(conv).filter((l) => l.includes('stub hold'))).toEqual(['stub hold'])
  } finally {
    await page.context().close()
  }
})

test('Run as comes back as the agent was last launched, is not kept by a cancel, is not offered without a chat, and Terminal keeps it', async ({ browser }) => {
  // The server keeps each agent's Run as (its record of the last launch): the test starts from none, never from the last test's launch.
  rmSync(join(own.root, 'data', 'launch', 'last.json'), { force: true })
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  try {
    // A side pressed and the dialog cancelled is not kept.
    let dialog = await openLaunch(page)
    await pick(dialog, CHAT_AGENT)
    budget(12 * STEP_MS)
    let r = runAs(dialog)
    await expect(r.terminal).toHaveAttribute('aria-checked', 'true')
    await r.chat.click()
    await expect(r.chat).toHaveAttribute('aria-checked', 'true')
    await dialog.getByRole('button', { name: 'Cancel' }).click()
    await expect(dialog).toHaveCount(0)
    dialog = await openLaunch(page)
    await pick(dialog, CHAT_AGENT)
    r = runAs(dialog)
    await expect(r.terminal).toHaveAttribute('aria-checked', 'true')
    // An agent with no chat declared is offered no choice.
    await pick(dialog, PLAIN_AGENT)
    await expect(runAs(dialog).choice).toHaveCount(0)
    // Launched as Chat: the next opening comes back at Chat for that agent.
    await pick(dialog, CHAT_AGENT)
    await runAs(dialog).chat.click()
    await dialog.locator('[data-launch-message] textarea').fill('stub hold')
    const s = await launchFrom(page, dialog)

    // Terminal pressed before the move takes the noted move back.
    const view = await welcomed(page, s.id)
    budget(6 * STEP_MS + LONG_MS)
    await expect(view).toHaveAttribute('data-way-noted', 'protocol')
    await held(own, s)
    const moved = page.waitForResponse((res) => res.request().method() === 'POST' && new URL(res.url()).pathname === `/api/sessions/${s.id}/way`)
    await page.locator('[data-way-switch] [data-way-side="terminal"]').click()
    expect((await moved).status()).toBe(200)
    await expect(view).not.toHaveAttribute('data-way-noted', /./, { timeout: LONG_MS })
    await expect(page.locator('[data-way-switch]')).not.toHaveAttribute('data-noted', /./)
    await expect(view).toHaveAttribute('data-session-way', 'terminal')

    dialog = await openLaunch(page)
    await pick(dialog, CHAT_AGENT)
    await expect(runAs(dialog).chat).toHaveAttribute('aria-checked', 'true')
    await dialog.getByRole('button', { name: 'Cancel' }).click()
  } finally {
    await page.context().close()
  }
})
