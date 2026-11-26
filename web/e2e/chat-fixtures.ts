import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Browser, BrowserContext, Locator, Page } from '@playwright/test'
import { expect, test, type Api, type Session } from './fixtures'
import { ownServer, type OwnServer } from './own-server'

// The chat's screens' specs (docs/plans/chat-or-terminal.md, row 7), each on a server of its own: copies of the Claude Code stub
// with its chat declared (web/e2e/stub-agent.sh in its terminal, web/e2e/stub-protocol.mjs as its chat's child, run by this suite's
// node), which no other spec's server offers. The protocol stub scripts a turn by the words of its message (its header says which)
// and keeps its gates and what it was told under the server's $HOME/.stub-sessions, by conversation.

/**
 * The caps of a step's waits, for a test's timeout (AGENTS.md, "A test's timeout bounds a hang, not its steps"): the expect's and an
 * action's own bound, and the long ones a chat's step waits with.
 */
export const STEP_MS = 15_000
export const LONG_MS = 30_000
/** Raises the running test's timeout by `ms`, as a step is about to make waits whose caps add up to it. */
export function budget(ms: number): void {
  const info = test.info()
  info.setTimeout(info.timeout + ms)
}

/**
 * A server with `claude-proto` and `asker-proto`, the chat's stub under two names (the second for the permission specs), and
 * `claude-bad`, whose chat's start fails at once (its initialize answered without the list it must hold): a move to it goes back.
 */
export function chatServer(name: string): Promise<OwnServer> {
  const env = { STUB_CLAUDE_VERSION: '2.1.331', STUB_NODE: process.execPath }
  return ownServer(name, {
    agents: [
      { id: 'claude-proto', from: 'claude', env, protocol: { name: 'claude-stream-json' } },
      { id: 'asker-proto', from: 'claude', env, protocol: { name: 'claude-stream-json' } },
      { id: 'claude-bad', from: 'claude', env: { ...env, STUB_PROTOCOL_INIT: 'bad' }, protocol: { name: 'claude-stream-json' } },
    ],
  })
}

/**
 * The join page of a link of `role` to session `id`, joined as Priya, in a context of its own: closed again when a step of the join
 * throws, so that no window is left calling the server.
 */
export async function joined(browser: Browser, own: OwnServer, id: string, role: 'view' | 'control', size = { width: 1280, height: 800 }): Promise<{ ctx: BrowserContext; g: Page }> {
  budget(3 * STEP_MS)
  const { token } = await own.api.ok<{ token: string }>('POST', `/api/sessions/${id}/links`, { role })
  const ctx = await browser.newContext({ baseURL: own.started.baseURL, viewport: size })
  try {
    await ctx.addInitScript(() => localStorage.setItem('conductor.displayName', 'Priya'))
    const g = await ctx.newPage()
    await g.goto(`/join/${token}`)
    await g.getByRole('button', { name: 'Join session' }).click()
    return { ctx, g }
  } catch (e) {
    await ctx.close()
    throw e
  }
}

/** A file the stub keeps for the conversation, by its extension; '' while it is not there. */
export function stubFile(own: OwnServer, conv: string, ext: string): string {
  return join(own.home, '.stub-sessions', `${conv}.${ext}`)
}
export function readStub(own: OwnServer, conv: string, ext: string): string {
  const p = stubFile(own, conv, ext)
  return existsSync(p) ? readFileSync(p, 'utf8') : ''
}
/** The permission question's line once it is a button (its input read, more than one line): wait for it before judging Allow once. */
export function lineButton(q: Locator): Locator {
  return q.locator('button[data-permission-line]')
}

/** Opens the stub's gate for the conversation's held turn (a message's "hold"). */
export function release(own: OwnServer, conv: string): void {
  writeFileSync(stubFile(own, conv, 'go'), 'go\n')
}

export async function launch(api: Api, sessions: string[], name: string, agentId = 'claude-proto'): Promise<{ s: Session; conv: string }> {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId, name })
  sessions.push(s.id)
  const conv = s.agentSession?.id ?? ''
  expect(conv).not.toBe('')
  return { s, conv }
}

/** The session page once its connection's welcome is read (data-welcomed, set by the welcome alone), the way it says. */
export async function openSession(page: Page, id: string, way: 'terminal' | 'protocol' = 'terminal'): Promise<Locator> {
  budget(STEP_MS + 2 * LONG_MS)
  await page.goto(`/sessions/${encodeURIComponent(id)}`)
  const view = page.locator('[data-session-way]').first()
  await expect(view).toHaveAttribute('data-welcomed', 'true', { timeout: 30_000 })
  await expect(view).toHaveAttribute('data-session-way', way, { timeout: 30_000 })
  return view
}

/** A line typed into the session's terminal. */
export async function typeLine(page: Page, line: string): Promise<void> {
  budget(2 * STEP_MS)
  const screen = page.locator('.terminal-host .xterm-screen').first()
  await expect(screen).toBeVisible()
  await screen.click()
  await page.keyboard.type(line)
  await page.keyboard.press('Enter')
}

/** A turn of the terminal's, ended: its hook's done shown, its conversation resumable, its first turn listed (what a move to Chat waits for). */
export async function firstTurn(page: Page, api: Api, id: string, line = 'a first turn'): Promise<void> {
  budget(LONG_MS)
  await typeLine(page, line)
  await expect
    .poll(async () => {
      const s = await api.session(id)
      return s.attention?.state === 'done' && !!s.agentSession?.resumable && !!s.firstTurn
    }, { timeout: 30_000 })
    .toBe(true)
}

/** Moves the session to Chat through the route (the operator's), and waits for the page's connection to say so. */
export async function toChat(api: Api, id: string, view: Locator): Promise<void> {
  budget(STEP_MS + LONG_MS)
  const r = await api.call('POST', `/api/sessions/${id}/way`, { to: 'protocol' })
  expect(r.status).toBe(200)
  await expect(view).toHaveAttribute('data-session-way', 'protocol', { timeout: 30_000 })
}

/** A message written in the chat's box and sent with Enter. */
export async function say(page: Page, text: string): Promise<void> {
  budget(3 * STEP_MS)
  const box = page.locator('[data-chat-box] textarea').first()
  await expect(box).toBeVisible()
  await box.fill(text)
  await box.press('Enter')
}

/** The chat's turn whose words or message hold `text`, once it has ended (its outcome said). */
export function turnWith(page: Page, text: string): Locator {
  return page.locator('[data-chat-turn]').filter({ hasText: text }).last()
}
