import { existsSync, mkdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Page } from '@playwright/test'
import { atRest, dayPage, expect, member, pagePath, test, type Run } from './fixtures'
import { noOverlap, uncut } from './layout'
import { ownServer, type OwnServer } from './own-server'

// A crew member's page carries its crew (UX3 R12, #1170): one strip above the session's header with `Back to the crew`, the run's
// title and a tab for each member, at every width; a tab opens that member in the page's place, so one Back leaves. A run of two
// shells and a copy of the Claude Code stub with its chat declared, launched by the spec, on a server of its own: a member's page
// shows the strip with the run's title; `Back to the crew` lands on the run's page; Alt+Q typed while the terminal has the keys goes back too, and Escape typed
// there reaches the shell while the page stays; on a phone the strip is in view and `Back to the list` is not drawn. The session's name
// is whole and nothing in the header overlaps, measured at rest, at 2000 x 810, 1440 x 900 and 390 x 844. Members are found by the
// run's id and their names, never by a count. A member's Terminal | Chat (UX3 R13, #1171) draws both sides as on any agent, the
// Chat side dimmed with why, and a press of it sends nothing. A member opens inside its crew (UX3 R72, #1170): its old address lands
// on its run's page with `?session=` by replace, before any socket to it opens; Back to the crew shows the run's own page; a session
// that is not the run's, asked for there, goes to its own page; a tile opens its member in place; the sidebar marks the member's row.
test.describe.configure({ mode: 'serial' })

const STEP_MS = 15_000
const LONG_MS = 30_000
function budget(ms: number) {
  const info = test.info()
  info.setTimeout(info.timeout + ms)
}

const CREW = `Refund split ${Date.now().toString(36)}`
let own: OwnServer
let run: Run
let crewId = ''
let cwd = ''

test.beforeAll(async () => {
  test.setTimeout(120_000)
  own = await ownServer('member-strip', {
    agents: [{ id: 'claude-proto', from: 'claude', env: { STUB_CLAUDE_VERSION: '2.1.331', STUB_NODE: process.execPath }, protocol: { name: 'claude-stream-json' } }],
  })
  cwd = join(own.repo, 'strip')
  mkdirSync(cwd, { recursive: true })
  const api = own.api
  crewId = (
    await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
      name: CREW,
      goal: '',
      cwd,
      where: 'server',
      isolation: 'none',
      openAfterLaunch: false,
      members: [
        { name: 'lead', agentId: 'shell', prompt: '', start: { when: 'immediately' } },
        { name: 'core', agentId: 'shell', prompt: '', start: { when: 'immediately' } },
        { name: 'review', agentId: 'claude-proto', prompt: 'say hello', start: { when: 'immediately' } },
      ],
    })
  ).crew.id
  run = await api.launchCrew(crewId)
  for (const name of ['lead', 'core', 'review']) {
    await expect.poll(async () => member(await api.run(run.id), name).sessionId ?? '', { timeout: LONG_MS, message: `${name} starts` }).not.toBe('')
  }
  run = await api.run(run.id)
})

test.afterAll(async () => {
  if (run) await own.api.stopRun(run.id)
  if (crewId) await own.api.call('DELETE', `/api/crews/${encodeURIComponent(crewId)}`)
  await own.stop()
})

const idOf = (name: string) => member(run, name).sessionId!
const runPath = () => `/runs/${encodeURIComponent(run.id)}`
/** The page's address and its `session`, as one string to compare. */
const where = (page: Page) => {
  const u = new URL(page.url())
  return `${u.pathname}${u.searchParams.has('session') ? `?session=${u.searchParams.get('session')}` : ''}`
}
const placeOf = (name: string) => `${runPath()}?session=${idOf(name)}`

/** A member opened at its old address: it lands inside its crew, its terminal drawn and its strip holding the run's title. */
async function memberPage(page: Page, name: string): Promise<void> {
  budget(STEP_MS + LONG_MS)
  await page.goto(`/sessions/${encodeURIComponent(idOf(name))}`)
  await expect.poll(() => where(page)).toBe(placeOf(name))
  await expect(page.locator('.terminal-host .xterm-screen').first()).toBeVisible({ timeout: LONG_MS })
  await expect(page.locator(`[data-member-strip][data-run="${run.id}"] [data-member-strip-title]`)).toHaveText(CREW)
}

/**
 * The header at rest: the strip and the session's bar each clear inside, the session's name whole; the strip above the bar, or, on a
 * very wide header, inside the bar's own row, left of the name (UX3 R12b).
 */
async function headerWhole(page: Page, inline = false): Promise<void> {
  const strip = page.locator('[data-member-strip]')
  const bar = page.locator('[data-session-bar]')
  await atRest(strip)
  await atRest(bar)
  await noOverlap(strip)
  await noOverlap(bar)
  await uncut(page.locator('[data-session-title]'))
  const [s, b, name] = [await strip.boundingBox(), await bar.boundingBox(), await page.locator('[data-session-title]').boundingBox()]
  if (inline) {
    await expect(strip).toHaveAttribute('data-strip-inline', '')
    expect(s && b && s.y >= b.y - 0.5 && s.y + s.height <= b.y + b.height + 0.5, 'the strip sits in the bar').toBe(true)
    expect(s && name && s.x + s.width <= name.x + 0.5, 'left of the name').toBe(true)
  } else {
    await expect(strip).not.toHaveAttribute('data-strip-inline', '')
    expect(s && b && s.y + s.height <= b.y + 0.5, 'the strip sits above the bar').toBe(true)
  }
}

for (const size of [
  { width: 2000, height: 810 },
  { width: 1440, height: 900 },
]) {
  test(`a member's page carries its crew; Back to the crew lands on the run (${size.width} x ${size.height})`, async ({ browser }) => {
    const page = await dayPage(browser, { server: own.started, ...size })
    try {
      await memberPage(page, 'lead')
      const back = page.getByRole('button', { name: 'Back to the crew' })
      await expect(back).toBeVisible()
      await expect(back).toHaveText('Back to the crew')
      await expect(page.locator('[data-back-to-list]')).toHaveCount(0)
      await headerWhole(page, size.width === 2000)
      // The tooltip names the run it goes back to, with its key.
      await back.hover()
      // reka keeps the role on a copy of the words, hidden from the tree, for the button's description (tooltips.spec.ts).
      await expect(page.getByRole('tooltip', { includeHidden: true })).toContainText(`Back to ${CREW}`)
      await back.click()
      await expect.poll(() => where(page)).toBe(runPath())
      // The run's own page again, its views drawn, no member's view left.
      await expect(page.locator('[data-run-view]')).toBeVisible()
      await expect(page.locator('[data-member-strip]')).toHaveCount(0)
    } finally {
      await page.context().close()
    }
  })
}

test("a member's tab opens that member in this page's place: one Back leaves the crew's members", async ({ browser }) => {
  // From the Crews page, which stays where it is (the home leaves for a session worth opening).
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900, path: '/crews' })
  try {
    const home = pagePath(page)
    expect(home).toBe('/crews')
    await memberPage(page, 'lead')
    const strip = page.locator(`[data-member-strip][data-run="${run.id}"]`)
    const lead = strip.locator('[data-member-tab="lead"]')
    const core = strip.locator('[data-member-tab="core"]')
    await expect(lead).toHaveAttribute('aria-current', 'page')
    await expect(core).not.toHaveAttribute('aria-current', 'page')
    // Each tab carries its member's mark: both shells run.
    await expect(lead.locator('[data-status-mark]')).toBeVisible()
    await expect(core.locator('[data-status-mark]')).toBeVisible()
    await core.click()
    await expect.poll(() => where(page)).toBe(placeOf('core'))
    await expect(strip.locator('[data-member-tab="core"]')).toHaveAttribute('aria-current', 'page')
    await expect(strip.locator('[data-member-tab="lead"]')).not.toHaveAttribute('aria-current', 'page')
    await headerWhole(page)
    // The tab replaced the page: one Back leaves the crew's members for where the person was before.
    await page.goBack()
    await expect.poll(() => pagePath(page)).toBe(home)
  } finally {
    await page.context().close()
  }
})

const MEMBER_TERMINAL = 'Crew members run in their terminals.'

test("a member's Terminal | Chat: both sides drawn, Chat dimmed with why; a press shows why and sends nothing", async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  try {
    const moves: string[] = []
    page.on('request', (r) => {
      if (r.method() === 'POST' && new URL(r.url()).pathname.endsWith('/way')) moves.push(r.url())
    })
    await memberPage(page, 'review')
    const sw = page.locator('[data-way-switch]')
    await expect(sw).toHaveAttribute('data-way', 'terminal')
    const terminal = sw.locator('[data-way-side="terminal"]')
    const chat = sw.locator('[data-way-side="protocol"]')
    await expect(terminal).toHaveAttribute('aria-pressed', 'true')
    // Dimmed, not disabled: it keeps its tooltip, on hover and on focus, and its press.
    await expect(chat).toHaveAttribute('aria-disabled', 'true')
    await expect(chat).not.toHaveAttribute('disabled')
    // No line stands above a member's terminal, the first reply's among them.
    await expect(page.locator('[data-way-line]')).toHaveCount(0)
    await chat.hover()
    await expect(page.getByRole('tooltip', { includeHidden: true })).toContainText(MEMBER_TERMINAL)
    // A press says it as the line, for a moment, and moves nothing. Playwright's click waits for an aria-disabled button to be enabled:
    // the press is the keyboard's, as a person's Enter on the focused side.
    await chat.focus()
    await page.keyboard.press('Enter')
    await expect(page.locator('[data-way-line]')).toHaveText(MEMBER_TERMINAL)
    await expect(sw).toHaveAttribute('data-way', 'terminal')
    expect(moves).toEqual([])
  } finally {
    await page.context().close()
  }
})

test('Alt+Q in the terminal goes back to the crew; Escape there is the agent\'s', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  try {
    await memberPage(page, 'core')
    const path = where(page)
    const out = join(cwd, 'escape.txt')
    // The shell copies what it is typed, Escape shown as ^[ (cat -v), into a file: Escape reached it, and the page stayed.
    await page.locator('.terminal-host .xterm-screen').first().click()
    await page.keyboard.type('cat -v > escape.txt\n')
    budget(STEP_MS)
    await expect.poll(() => existsSync(out), { timeout: STEP_MS, message: 'cat has made its file' }).toBe(true)
    await page.keyboard.press('Escape')
    await page.keyboard.press('Enter')
    await page.keyboard.press('Control+D')
    await expect.poll(() => readFileSync(out, 'utf8'), { timeout: STEP_MS, message: 'the shell read Escape' }).toContain('^[')
    expect(where(page)).toBe(path)
    await expect(page.locator('[data-member-strip]')).toBeVisible()
    // Alt+Q, the terminal still holding the keys, goes back to the crew.
    await expect(page.locator('.terminal-host .xterm-helper-textarea').first()).toBeFocused()
    await page.keyboard.press('Alt+KeyQ')
    await expect.poll(() => where(page)).toBe(runPath())
  } finally {
    await page.context().close()
  }
})

test("on a phone the strip is a member's way back, in view; Back to the list is not drawn", async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 390, height: 844, touch: true })
  try {
    await memberPage(page, 'lead')
    const back = page.getByRole('button', { name: 'Back to the crew' })
    await expect(back).toBeInViewport()
    await expect(back).toHaveText('Back to the crew')
    await expect(page.locator('[data-back-to-list]')).toHaveCount(0)
    await headerWhole(page)
    await back.tap()
    await expect.poll(() => where(page)).toBe(runPath())
    // Below sm the way is More's two items: Chat says why and cannot be pressed (UX3 R13).
    await memberPage(page, 'review')
    await page.locator('[data-more-button]').tap()
    const item = page.getByRole('menuitemcheckbox', { name: /Chat/ })
    await expect(item).toContainText(MEMBER_TERMINAL)
    await expect(item).toHaveAttribute('aria-disabled', 'true')
  } finally {
    await page.context().close()
  }
})

test('on a very wide window the strip is in the header row and the terminal as tall as any session\'s; twelve members scroll in it', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 2000, height: 810, path: '/crews' })
  const api = own.api
  const alone = await api.ok<{ id: string }>('POST', '/api/sessions', { agentId: 'shell', name: 'e2e wide alone', cwd })
  let big: Run | undefined
  let bigCrew = ''
  try {
    budget(4 * LONG_MS)
    const tall = async () => {
      await expect(page.locator('.terminal-host .xterm-screen').first()).toBeVisible({ timeout: LONG_MS })
      await atRest(page.locator('.terminal-host').first())
      return (await page.locator('.terminal-host').first().boundingBox())!.height
    }
    await page.goto(`/sessions/${encodeURIComponent(alone.id)}`)
    const aloneTall = await tall()
    await memberPage(page, 'lead')
    await headerWhole(page, true)
    expect(Math.abs((await tall()) - aloneTall), `a member's terminal against ${aloneTall} px`).toBeLessThanOrEqual(2)

    // Twelve members: the tabs scroll inside the strip, the open one in view, the name whole.
    const members = Array.from({ length: 12 }, (_, i) => ({ name: `member-${String(i + 1).padStart(2, '0')}`, agentId: 'shell', prompt: '', start: { when: 'immediately' } }))
    bigCrew = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', { name: `${CREW} twelve`, goal: '', cwd, where: 'server', isolation: 'none', openAfterLaunch: false, members })).crew.id
    big = await api.launchCrew(bigCrew)
    budget(2 * LONG_MS)
    await expect.poll(async () => member(await api.run(big!.id), 'member-12').sessionId ?? '', { timeout: 2 * LONG_MS }).not.toBe('')
    big = await api.run(big.id)
    await page.goto(`/sessions/${encodeURIComponent(member(big, 'member-12').sessionId!)}`)
    const tabs = page.locator(`[data-member-strip][data-run="${big.id}"] [data-member-tabs]`)
    await expect(tabs.locator('[data-member-tab]')).toHaveCount(12, { timeout: LONG_MS })
    await headerWhole(page, true)
    expect(await tabs.evaluate((el) => el.scrollWidth > el.clientWidth), 'the tabs scroll').toBe(true)
    await expect(tabs.locator('[data-member-tab="member-12"]')).toBeInViewport()
    const [list, open] = [await tabs.boundingBox(), await tabs.locator('[data-member-tab="member-12"]').boundingBox()]
    expect(list && open && open.x >= list.x - 0.5 && open.x + open.width <= list.x + list.width + 0.5, 'the open tab inside the strip').toBe(true)
  } finally {
    await page.context().close()
    await api.stopSession(alone.id)
    if (big) await api.stopRun(big.id)
    if (bigCrew) await api.call('DELETE', `/api/crews/${encodeURIComponent(bigCrew)}`)
  }
})

test("a member's old address lands inside its crew before any socket to it opens; one Back leaves; the sidebar marks its row", async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900, path: '/crews' })
  try {
    const lead = idOf('lead')
    // Every socket to the member, with the address the page had as it opened.
    const sockets: string[] = []
    page.on('websocket', (ws) => {
      if (new URL(ws.url()).pathname.endsWith(`/ws/sessions/${lead}`)) sockets.push(new URL(page.url()).pathname)
    })
    await memberPage(page, 'lead')
    await expect(page.locator('[data-transport-state="open"]')).toHaveCount(1, { timeout: LONG_MS })
    expect(sockets.length, 'the view connected').toBeGreaterThan(0)
    expect(sockets.filter((p) => p !== runPath()), 'no socket opened on the old address').toEqual([])
    // Its row in the sidebar is the page; the run's header row is not.
    await expect(page.locator(`[data-sidebar-row="s:${lead}"] [aria-current="page"]`)).toHaveCount(1)
    // The old address was replaced: one Back leaves for where the person was, never to it.
    await page.goBack()
    await expect.poll(() => pagePath(page)).toBe('/crews')
  } finally {
    await page.context().close()
  }
})

test("from the run's page a tile opens its member in place; Back to the crew returns to the run; another session is not drawn there", async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900, path: runPath() })
  const own_ = await own.api.ok<{ id: string }>('POST', '/api/sessions', { agentId: 'shell', name: 'e2e not a member', cwd })
  try {
    await page.locator('[data-member="core"] [data-tile-open]').click()
    await expect.poll(() => where(page)).toBe(placeOf('core'))
    await expect(page.locator('[data-run-grid]')).toHaveCount(0)
    await expect(page.locator(`[data-member-strip] [data-member-tab="core"]`)).toHaveAttribute('aria-current', 'page')
    // Back to the crew is the browser's Back here: the run's page, and one more Back leaves it.
    await page.getByRole('button', { name: 'Back to the crew' }).click()
    await expect.poll(() => where(page)).toBe(runPath())
    await expect(page.locator('[data-member="core"] [data-tile-open]')).toBeVisible({ timeout: LONG_MS })
    // A session that is not the run's member, asked for in the run's place, goes to its own page.
    budget(STEP_MS + LONG_MS)
    await page.goto(`${runPath()}?session=${encodeURIComponent(own_.id)}`)
    await expect.poll(() => pagePath(page)).toBe(`/sessions/${encodeURIComponent(own_.id)}`)
    await expect(page.locator('[data-session-bar]')).toBeVisible({ timeout: LONG_MS })
    await expect(page.locator('[data-member-strip]')).toHaveCount(0)
  } finally {
    await page.context().close()
    await own.api.stopSession(own_.id)
  }
})
