import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Locator, Page } from '@playwright/test'
import { dayPage, expect, test, type Api, type Session } from './fixtures'
import { ownServer, type OwnServer } from './own-server'

// The move between ways (docs/plans/chat-or-terminal.md, row 5), on a server of the spec's own: a copy of the Claude Code stub with
// its chat declared (claude-proto: web/e2e/stub-agent.sh in its terminal, web/e2e/stub-protocol.mjs as its chat's child, run by this
// suite's node, one transcript under the server's $HOME/.stub-sessions), which no other spec's server offers. A session has a first
// turn in its terminal, is moved to its chat through the route, and back, on the page's one connection; a move pressed during a turn
// held at the stub's gate is noted and made at the turn's end. The switch is not on the page yet (row 7): the page's connection says
// the way on its terminal's root (data-session-way, data-way-noted, data-welcomed) and counts the TRANSCRIPT frames it received
// (data-transcript-frames).
test.describe.configure({ mode: 'serial' })

let own: OwnServer
const sessions: string[] = []

test.beforeAll(async () => {
  test.setTimeout(120_000)
  own = await ownServer('chat-move', {
    agents: [{ id: 'claude-proto', from: 'claude', env: { STUB_CLAUDE_VERSION: '2.1.331', STUB_NODE: process.execPath }, protocol: { name: 'claude-stream-json' } }],
  })
})

test.afterAll(async () => {
  for (const id of sessions) await own.api.stopSession(id)
  await own.stop()
})

function stubFile(conv: string, ext: string): string {
  return join(own.home, '.stub-sessions', `${conv}.${ext}`)
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

/** The session page, once its terminal's root has read the connection's welcome (data-welcomed: set by the welcome alone). */
async function open(page: Page, id: string, way: 'terminal' | 'protocol'): Promise<Locator> {
  await page.goto(`/sessions/${encodeURIComponent(id)}`)
  const view = page.locator('[data-session-way]').first()
  await expect(view).toHaveAttribute('data-welcomed', 'true', { timeout: 30_000 })
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

/** A turn of the terminal's, ended: its hook's done shown, its conversation resumable, its first turn listed. */
async function firstTurn(page: Page, api: Api, id: string, line: string) {
  await typeLine(page, line)
  await expect
    .poll(async () => {
      const s = await api.session(id)
      return s.attention?.state === 'done' && !!s.agentSession?.resumable && !!s.firstTurn
    }, { timeout: 30_000 })
    .toBe(true)
}

async function move(api: Api, id: string, to: 'terminal' | 'protocol') {
  return api.call<{ way?: string; noted?: string; why?: string; moving?: boolean; error?: { code: string; message: string } }>('POST', `/api/sessions/${id}/way`, { to })
}

test('a terminal session moves to its chat and back on one connection, the terminal resuming its conversation at the page size', async ({ browser }) => {
  const api = own.api
  const { s, conv } = await launch(api, 'e2e move')
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  try {
    const view = await open(page, s.id, 'terminal')
    // Before the first reply: refused, in the plan's words.
    const early = await move(api, s.id, 'protocol')
    expect(early.status).toBe(409)
    expect(early.body.error).toEqual({ code: 'move_refused', message: "Chat opens after the agent's first reply." })
    await firstTurn(page, api, s.id, 'remember PINEAPPLE')
    const pid = read(stubFile(conv, 'pid')).trim()
    expect(pid).not.toBe('')
    const made = await move(api, s.id, 'protocol')
    expect(made.status).toBe(200)
    // The page's own connection: the way message, and the chat's frames (the boundary's notice among them).
    await expect(view).toHaveAttribute('data-session-way', 'protocol', { timeout: 30_000 })
    await expect.poll(async () => Number(await view.getAttribute('data-transcript-frames')), { timeout: 30_000 }).toBeGreaterThan(0)
    await expect.poll(async () => (await api.session(s.id)).way, { timeout: 30_000 }).toBe('protocol')
    // A new connection (another window) reads the way from its welcome, and the transcript's replay after its ready.
    const other = await dayPage(browser, { server: own.started })
    try {
      const again = await open(other, s.id, 'protocol')
      await expect.poll(async () => Number(await again.getAttribute('data-transcript-frames')), { timeout: 30_000 }).toBeGreaterThan(0)
    } finally {
      await other.context().close()
    }
    // The window that sizes the session changes while it is a chat (which takes no resize): the terminal it moves back to is sized
    // again, to what the page draws then.
    const host = page.locator('.terminal-host').first()
    const before = Number(await host.getAttribute('data-cols'))
    await page.setViewportSize({ width: 1100, height: 800 })
    await expect.poll(async () => Number(await host.getAttribute('data-cols')), { timeout: 30_000 }).not.toBe(before)
    // Back to the terminal: the stub resumes the same conversation's transcript and prints it.
    const back = await move(api, s.id, 'terminal')
    expect(back.status).toBe(200)
    await expect(view).toHaveAttribute('data-session-way', 'terminal', { timeout: 30_000 })
    await expect.poll(() => read(stubFile(conv, 'resumed')).trim(), { timeout: 30_000 }).not.toBe('')
    expect(Number(read(stubFile(conv, 'resumed')).trim().split('\n').pop())).toBeGreaterThan(0)
    expect(read(stubFile(conv, 'txt'))).toContain('remember PINEAPPLE')
    await expect.poll(() => read(stubFile(conv, 'pid')).trim(), { timeout: 30_000 }).not.toBe(pid)
    // The session runs at the size the page draws now, not the one from before the chat.
    await expect
      .poll(async () => {
        const drawn = Number(await host.getAttribute('data-cols'))
        return (await api.session(s.id)).cols === drawn && drawn !== before
      }, { timeout: 30_000 })
      .toBe(true)
    const info = await api.session(s.id)
    expect(info.id).toBe(s.id)
    expect(info.way).toBeUndefined()
    expect(info.status).toBe('running')
  } finally {
    await page.context().close()
  }
})

test('a move pressed during a turn held at the gate is noted on the page and made at the turn end', async ({ browser }) => {
  const api = own.api
  const { s, conv } = await launch(api, 'e2e noted move')
  const page = await dayPage(browser, { server: own.started })
  try {
    const view = await open(page, s.id, 'terminal')
    await firstTurn(page, api, s.id, 'a first turn')
    await typeLine(page, 'stub hold')
    // The held turn: working, its gate written by the stub.
    await expect.poll(() => read(stubFile(conv, 'hold')).trim(), { timeout: 30_000 }).toBe('hold')
    await expect.poll(async () => (await api.session(s.id)).attention?.state, { timeout: 30_000 }).toBe('working')
    const noted = await move(api, s.id, 'protocol')
    expect(noted.status).toBe(200)
    expect(noted.body).toMatchObject({ way: 'terminal', noted: 'protocol', why: 'Moves to Chat when this turn ends.' })
    await expect(view).toHaveAttribute('data-way-noted', 'protocol', { timeout: 30_000 })
    await expect(view).toHaveAttribute('data-session-way', 'terminal')
    // The turn ends: the move is made, and noted clears.
    writeFileSync(stubFile(conv, 'hold'), 'stop\n')
    await expect(view).toHaveAttribute('data-session-way', 'protocol', { timeout: 30_000 })
    await expect(view).not.toHaveAttribute('data-way-noted', /./)
    await expect.poll(async () => (await api.session(s.id)).way, { timeout: 30_000 }).toBe('protocol')
  } finally {
    await page.context().close()
  }
})
