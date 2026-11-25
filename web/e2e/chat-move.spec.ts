import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Locator, Page } from '@playwright/test'
import { expect, test, type Api, type Session } from './fixtures'

// The move between ways (docs/plans/chat-or-terminal.md, row 5): a session of the Claude Code stub with its chat declared
// (claude-proto: web/e2e/stub-agent.sh in its terminal, web/e2e/stub-protocol.mjs as its chat's child, one transcript under
// $HOME/.stub-sessions) has a first turn in its terminal, is moved to its chat through the route, and back, on the page's one
// connection; a move pressed during a turn held at the stub's gate is noted and made at the turn's end. The switch is not on the
// page yet (row 7): the page's connection says the way on its terminal's root (data-session-way, data-way-noted) and counts the
// TRANSCRIPT frames it received (data-transcript-frames).
test.describe.configure({ mode: 'serial' })

const sessions: string[] = []

test.afterAll(async ({ api }) => {
  for (const id of sessions) await api.stopSession(id)
})

function stubFile(home: string, conv: string, ext: string): string {
  return join(home, '.stub-sessions', `${conv}.${ext}`)
}

function read(path: string): string {
  return existsSync(path) ? readFileSync(path, 'utf8') : ''
}

async function launch(api: Api, name: string): Promise<{ s: Session; conv: string }> {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude-proto', name })
  sessions.push(s.id)
  const conv = s.agentSession?.id ?? ''
  expect(conv).not.toBe('')
  return { s, conv }
}

/** The session page, once its terminal's root (the connection's marks) is drawn and its welcome read. */
async function open(page: Page, id: string, way: 'terminal' | 'protocol'): Promise<Locator> {
  await page.goto(`/sessions/${encodeURIComponent(id)}`)
  const view = page.locator('[data-session-way]').first()
  await expect(view).toHaveAttribute('data-session-way', way, { timeout: 30_000 })
  return view
}

async function typeLine(page: Page, line: string) {
  const screen = page.locator('.terminal-host .xterm-screen').first()
  await expect(screen).toBeVisible()
  await screen.click()
  await page.keyboard.type(line)
  await page.keyboard.press('Enter')
}

/** A turn of the terminal's, ended: its hook's done shown, its conversation resumable. */
async function firstTurn(page: Page, api: Api, id: string, line: string) {
  await typeLine(page, line)
  await expect
    .poll(async () => {
      const s = await api.session(id)
      return s.attention?.state === 'done' && !!s.agentSession?.resumable
    }, { timeout: 30_000 })
    .toBe(true)
}

async function move(api: Api, id: string, to: 'terminal' | 'protocol') {
  return api.call<{ way?: string; noted?: string; why?: string; moving?: boolean; error?: { code: string; message: string } }>('POST', `/api/sessions/${id}/way`, { to })
}

test('a terminal session moves to its chat and back on one connection, the terminal resuming its conversation', async ({ page, api, state }) => {
  const { s, conv } = await launch(api, 'e2e move')
  const view = await open(page, s.id, 'terminal')
  // Before the first reply: refused, in the plan's words.
  const early = await move(api, s.id, 'protocol')
  expect(early.status).toBe(409)
  expect(early.body.error).toEqual({ code: 'move_refused', message: "Chat opens after the agent's first reply." })
  await firstTurn(page, api, s.id, 'remember PINEAPPLE')
  const pid = read(stubFile(state.home, conv, 'pid')).trim()
  expect(pid).not.toBe('')
  const made = await move(api, s.id, 'protocol')
  expect(made.status).toBe(200)
  // The page's own connection: the way message, and the chat's frames (the boundary's notice among them).
  await expect(view).toHaveAttribute('data-session-way', 'protocol', { timeout: 30_000 })
  await expect.poll(async () => Number(await view.getAttribute('data-transcript-frames')), { timeout: 30_000 }).toBeGreaterThan(0)
  await expect.poll(async () => (await api.session(s.id)).way, { timeout: 30_000 }).toBe('protocol')
  // A new connection reads the way from its welcome, and the transcript's replay after its ready.
  const again = await open(page, s.id, 'protocol')
  await expect.poll(async () => Number(await again.getAttribute('data-transcript-frames')), { timeout: 30_000 }).toBeGreaterThan(0)
  // Back to the terminal: the stub resumes the same conversation's transcript and prints it.
  const back = await move(api, s.id, 'terminal')
  expect(back.status).toBe(200)
  await expect(again).toHaveAttribute('data-session-way', 'terminal', { timeout: 30_000 })
  await expect.poll(() => read(stubFile(state.home, conv, 'resumed')).trim(), { timeout: 30_000 }).not.toBe('')
  expect(Number(read(stubFile(state.home, conv, 'resumed')).trim().split('\n').pop())).toBeGreaterThan(0)
  expect(read(stubFile(state.home, conv, 'txt'))).toContain('remember PINEAPPLE')
  await expect.poll(() => read(stubFile(state.home, conv, 'pid')).trim(), { timeout: 30_000 }).not.toBe(pid)
  const info = await api.session(s.id)
  expect(info.id).toBe(s.id)
  expect(info.way).toBeUndefined()
  expect(info.status).toBe('running')
})

test('a move pressed during a turn held at the gate is noted on the page and made at the turn end', async ({ page, api, state }) => {
  const { s, conv } = await launch(api, 'e2e noted move')
  const view = await open(page, s.id, 'terminal')
  await firstTurn(page, api, s.id, 'a first turn')
  await typeLine(page, 'stub hold')
  // The held turn: working, its gate written by the stub.
  await expect.poll(() => read(stubFile(state.home, conv, 'hold')).trim(), { timeout: 30_000 }).toBe('hold')
  await expect.poll(async () => (await api.session(s.id)).attention?.state, { timeout: 30_000 }).toBe('working')
  const noted = await move(api, s.id, 'protocol')
  expect(noted.status).toBe(200)
  expect(noted.body).toMatchObject({ way: 'terminal', noted: 'protocol', why: 'Moves to Chat when this turn ends.' })
  await expect(view).toHaveAttribute('data-way-noted', 'protocol', { timeout: 30_000 })
  await expect(view).toHaveAttribute('data-session-way', 'terminal')
  // The turn ends: the move is made, and noted clears.
  writeFileSync(stubFile(state.home, conv, 'hold'), 'stop\n')
  await expect(view).toHaveAttribute('data-session-way', 'protocol', { timeout: 30_000 })
  await expect(view).not.toHaveAttribute('data-way-noted', /./)
  await expect.poll(async () => (await api.session(s.id)).way, { timeout: 30_000 }).toBe('protocol')
})
