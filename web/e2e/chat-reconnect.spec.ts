import { writeFileSync } from 'node:fs'
import type { WebSocket as WireSocket } from '@playwright/test'
import { dayPage, expect, test } from './fixtures'
import { budget, chatServer, firstTurn, launch, LONG_MS, openSession, say, STEP_MS, stubFile, toChat, turnWith } from './chat-fixtures'
import type { OwnServer } from './own-server'

// A chat's connection dropped and made again (docs/plans/chat-or-terminal.md, row 7, design 6), on a server of the spec's own: the
// hello of the new connection carries the highest mark the page held, and the owner sends it only the entries changed since,
// counted on the wire as the server sent them (never by the page's list); a request withdrawn while the page was away is drawn
// withdrawn. The page's socket is closed through a seam the spec adds to the page (each WebSocket it opens, kept to close).
test.describe.configure({ mode: 'serial' })

let own: OwnServer
const sessions: string[] = []

test.beforeAll(async () => {
  test.setTimeout(120_000)
  own = await chatServer('chat-reconnect')
})

test.afterAll(async () => {
  for (const id of sessions) await own.api.stopSession(id)
  await own.stop()
})

/** The TRANSCRIPT frames' entries one socket received, as the owner sent them (a 0x0B frame: an entry, or a page's entries). */
function entriesOn(ws: WireSocket): Array<{ seq: number; mark: number; kind: string; state?: string }> {
  const got: Array<{ seq: number; mark: number; kind: string; state?: string }> = []
  ws.on('framereceived', (f) => {
    if (typeof f.payload === 'string' || f.payload[0] !== 0x0b) return
    const m = JSON.parse(f.payload.subarray(1).toString('utf8')) as { t: string; entry?: { seq: number; mark: number; kind: string }; entries?: Array<{ seq: number; mark: number; kind: string }> }
    if (m.t === 'entry' && m.entry) got.push(m.entry)
    if (m.t === 'page' && m.entries) got.push(...m.entries)
  })
  return got
}

test('a connection made again is sent only what changed after its mark, and a request withdrawn meanwhile is drawn withdrawn', async ({ browser }) => {
  const api = own.api
  const { s, conv } = await launch(api, sessions, 'e2e chat reconnect')
  const page = await dayPage(browser, { server: own.started })
  // The seam: every WebSocket the page opens is kept, so that the spec can drop the newest.
  await page.addInitScript(() => {
    const Native = window.WebSocket
    const kept: Array<InstanceType<typeof Native>> = []
    ;(window as unknown as { __sockets: typeof kept }).__sockets = kept
    window.WebSocket = class extends Native {
      constructor(url: string | URL, protocols?: string | string[]) {
        super(url, protocols)
        kept.push(this)
      }
    } as typeof Native
  })
  const sockets: WireSocket[] = []
  page.on('websocket', (ws) => {
    if (ws.url().includes(`/ws/sessions/${s.id}`)) sockets.push(ws)
  })
  try {
    const view = await openSession(page, s.id)
    await firstTurn(page, api, s.id)
    await toChat(api, s.id, view)
    await say(page, 'steps first')
    budget(6 * STEP_MS + 9 * LONG_MS)
    await expect(turnWith(page, 'stub chat: steps first')).toHaveAttribute('data-turn-outcome', 'done', { timeout: LONG_MS })
    // A request open as the connection drops.
    await say(page, 'ask withdraw')
    const q = page.locator('[data-chat-view] [data-permission-question]')
    await expect(q).toHaveAttribute('data-permission-state', 'open', { timeout: LONG_MS })
    const held = Number(await page.locator('[data-chat-view]').getAttribute('data-chat-entries'))
    expect(held).toBeGreaterThan(5)

    // Dropped: the page shows it, and while it is away the agent gives the request up and the turn ends.
    const before = sockets.length
    await page.evaluate(() => (window as unknown as { __sockets: Array<{ close: () => void }> }).__sockets.at(-1)?.close())
    const again = page.getByRole('button', { name: 'Reconnect', exact: true })
    await expect(again).toBeVisible({ timeout: LONG_MS })
    writeFileSync(stubFile(own, conv, 'withdraw'), 'go\n')
    await expect.poll(async () => (await api.session(s.id)).attention?.state, { timeout: LONG_MS }).toBe('done')

    // Made again: its hello names the mark held, and it is sent the changes after it alone.
    const hello = new Promise<{ transcriptAfter?: number }>((resolve) =>
      page.on('websocket', (ws) =>
        ws.on('framesent', (f) => {
          if (typeof f.payload === 'string' || f.payload[0] !== 0x03) return
          const m = JSON.parse(f.payload.subarray(1).toString('utf8')) as { t: string; transcriptAfter?: number }
          if (m.t === 'hello') resolve(m)
        }),
      ),
    )
    const got: Array<{ seq: number; mark: number; kind: string; state?: string }>[] = []
    page.on('websocket', (ws) => got.push(entriesOn(ws)))
    await again.click()
    await expect.poll(() => sockets.length, { timeout: LONG_MS }).toBeGreaterThan(before)
    const said = await hello
    expect(said.transcriptAfter).toBeGreaterThan(0)
    await expect(q).toHaveAttribute('data-permission-state', 'withdrawn', { timeout: LONG_MS })
    await expect(q.locator('[data-permission-end]')).toHaveText('The request was lost.')
    await expect(turnWith(page, 'stub chat: ask withdraw')).toHaveAttribute('data-turn-outcome', 'done', { timeout: LONG_MS })
    const sent = got.flat()
    // Only changes after the mark: every entry sent is marked past it, the request's end among them; nothing of the first turns.
    expect(sent.length).toBeGreaterThan(0)
    expect(sent.length).toBeLessThan(held)
    for (const e of sent) expect(e.mark, `entry ${e.seq} (${e.kind})`).toBeGreaterThan(said.transcriptAfter!)
    expect(sent.some((e) => e.kind === 'permission' && e.state === 'withdrawn')).toBe(true)
  } finally {
    await page.context().close()
  }
})
