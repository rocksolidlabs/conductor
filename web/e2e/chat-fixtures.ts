import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Locator, Page } from '@playwright/test'
import { expect, type Api, type Session } from './fixtures'
import { ownServer, type OwnServer } from './own-server'

// The chat's screens' specs (docs/plans/chat-or-terminal.md, row 7), each on a server of its own: copies of the Claude Code stub
// with its chat declared (web/e2e/stub-agent.sh in its terminal, web/e2e/stub-protocol.mjs as its chat's child, run by this suite's
// node), which no other spec's server offers. The protocol stub scripts a turn by the words of its message (its header says which)
// and keeps its gates and what it was told under the server's $HOME/.stub-sessions, by conversation.

/** A server with `claude-proto` and `asker-proto`, the chat's stub under two names (the second for the permission specs). */
export function chatServer(name: string): Promise<OwnServer> {
  const env = { STUB_CLAUDE_VERSION: '2.1.331', STUB_NODE: process.execPath }
  return ownServer(name, {
    agents: [
      { id: 'claude-proto', from: 'claude', env, protocol: { name: 'claude-stream-json' } },
      { id: 'asker-proto', from: 'claude', env, protocol: { name: 'claude-stream-json' } },
    ],
  })
}

/** A file the stub keeps for the conversation, by its extension; '' while it is not there. */
export function stubFile(own: OwnServer, conv: string, ext: string): string {
  return join(own.home, '.stub-sessions', `${conv}.${ext}`)
}
export function readStub(own: OwnServer, conv: string, ext: string): string {
  const p = stubFile(own, conv, ext)
  return existsSync(p) ? readFileSync(p, 'utf8') : ''
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
  await page.goto(`/sessions/${encodeURIComponent(id)}`)
  const view = page.locator('[data-session-way]').first()
  await expect(view).toHaveAttribute('data-welcomed', 'true', { timeout: 30_000 })
  await expect(view).toHaveAttribute('data-session-way', way, { timeout: 30_000 })
  return view
}

/** A line typed into the session's terminal. */
export async function typeLine(page: Page, line: string): Promise<void> {
  const screen = page.locator('.terminal-host .xterm-screen').first()
  await expect(screen).toBeVisible()
  await screen.click()
  await page.keyboard.type(line)
  await page.keyboard.press('Enter')
}

/** A turn of the terminal's, ended: its hook's done shown, its conversation resumable (what a move to Chat waits for). */
export async function firstTurn(page: Page, api: Api, id: string, line = 'a first turn'): Promise<void> {
  await typeLine(page, line)
  await expect
    .poll(async () => {
      const s = await api.session(id)
      return s.attention?.state === 'done' && !!s.agentSession?.resumable
    }, { timeout: 30_000 })
    .toBe(true)
}

/** Moves the session to Chat through the route (the operator's), and waits for the page's connection to say so. */
export async function toChat(api: Api, id: string, view: Locator): Promise<void> {
  const r = await api.call('POST', `/api/sessions/${id}/way`, { to: 'protocol' })
  expect(r.status).toBe(200)
  await expect(view).toHaveAttribute('data-session-way', 'protocol', { timeout: 30_000 })
}

/** A message written in the chat's box and sent with Enter. */
export async function say(page: Page, text: string): Promise<void> {
  const box = page.locator('[data-chat-box] textarea').first()
  await expect(box).toBeVisible()
  await box.fill(text)
  await box.press('Enter')
}

/** The chat's turn whose words or message hold `text`, once it has ended (its outcome said). */
export function turnWith(page: Page, text: string): Locator {
  return page.locator('[data-chat-turn]').filter({ hasText: text }).last()
}
