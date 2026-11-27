import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Page, Request } from '@playwright/test'
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
  let six: Run | undefined
  let sixCrew = ''
  /** A crew of `n` shells launched and its members started. */
  async function crewOf(n: number, label: string): Promise<{ crew: string; run: Run }> {
    const members = Array.from({ length: n }, (_, i) => ({ name: `member-${String(i + 1).padStart(2, '0')}`, agentId: 'shell', prompt: '', start: { when: 'immediately' } }))
    const crew = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', { name: `${CREW} ${label}`, goal: '', cwd, where: 'server', isolation: 'none', openAfterLaunch: false, members })).crew.id
    let r = await api.launchCrew(crew)
    budget(2 * LONG_MS)
    const last = `member-${String(n).padStart(2, '0')}`
    await expect.poll(async () => member(await api.run(r.id), last).sessionId ?? '', { timeout: 2 * LONG_MS }).not.toBe('')
    r = await api.run(r.id)
    return { crew, run: r }
  }
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

    // Six members: the strip takes the row's room, every tab whole and none past an edge.
    ;({ crew: sixCrew, run: six } = await crewOf(6, 'six'))
    await page.goto(`/sessions/${encodeURIComponent(member(six, 'member-03').sessionId!)}`)
    const sixTabs = page.locator(`[data-member-strip][data-run="${six.id}"] [data-member-tabs]`)
    await expect(sixTabs.locator('[data-member-tab]')).toHaveCount(6, { timeout: LONG_MS })
    await headerWhole(page, true)
    for (const t of await sixTabs.locator('[data-member-tab]').all()) await uncut(t)
    await expect(sixTabs).not.toHaveAttribute('data-tabs-more')
    expect(await sixTabs.evaluate((el) => el.scrollWidth <= el.clientWidth), 'six tabs fit').toBe(true)

    // Twelve members: the tabs scroll inside the strip, the open one whole in view, the edge that cuts says there are more.
    ;({ crew: bigCrew, run: big } = await crewOf(12, 'twelve'))
    await page.goto(`/sessions/${encodeURIComponent(member(big, 'member-12').sessionId!)}`)
    const tabs = page.locator(`[data-member-strip][data-run="${big.id}"] [data-member-tabs]`)
    await expect(tabs.locator('[data-member-tab]')).toHaveCount(12, { timeout: LONG_MS })
    await headerWhole(page, true)
    expect(await tabs.evaluate((el) => el.scrollWidth > el.clientWidth), 'the tabs scroll').toBe(true)
    await expect(tabs.locator('[data-member-tab="member-12"]')).toBeInViewport()
    const [list, open] = [await tabs.boundingBox(), await tabs.locator('[data-member-tab="member-12"]').boundingBox()]
    expect(list && open && open.x >= list.x - 0.5 && open.x + open.width <= list.x + list.width + 0.5, 'the open tab inside the strip').toBe(true)
    await uncut(tabs.locator('[data-member-tab="member-12"]'))
    await expect(tabs).toHaveAttribute('data-tabs-more', /left|both/)
  } finally {
    await page.context().close()
    await api.stopSession(alone.id)
    if (big) await api.stopRun(big.id)
    if (bigCrew) await api.call('DELETE', `/api/crews/${encodeURIComponent(bigCrew)}`)
    if (six) await api.stopRun(six.id)
    if (sixCrew) await api.call('DELETE', `/api/crews/${encodeURIComponent(sixCrew)}`)
  }
})

/** A move inside the app, as a link or a press makes it (the app's own router): no page load, the live list as the app holds it. */
async function inApp(page: Page, to: string): Promise<void> {
  await page.evaluate((t) => (document.querySelector('#__nuxt') as unknown as { __vue_app__: { config: { globalProperties: { $router: { push: (to: string) => Promise<unknown> } } } } }).__vue_app__.config.globalProperties.$router.push(t), to)
}
/** The app's live list holds the session: its sidebar row is drawn. */
async function listed(page: Page, id: string): Promise<void> {
  await expect(page.locator(`[data-sidebar-row="s:${id}"]`)).toHaveCount(1, { timeout: LONG_MS })
}
const sessionRead = (id: string) => (r: Request) => r.method() === 'GET' && new URL(r.url()).pathname === `/api/sessions/${id}`

test("a member's old address lands inside its crew before any socket to it opens: exactly one socket, its own closed at a switch", async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900, path: '/crews' })
  try {
    const [lead, core] = [idOf('lead'), idOf('core')]
    // Every socket to a member, with the address the page had as it opened and whether it has closed. The run's chat keeps a quiet
    // connection of its own to a live member (its hello says chatOnly): the view's are the others.
    const sockets: Array<{ id: string; at: string; closed: boolean; chatOnly: boolean }> = []
    page.on('websocket', (ws) => {
      const id = new URL(ws.url()).pathname.split('/ws/sessions/')[1] ?? ''
      if (id !== lead && id !== core) return
      const s = { id, at: new URL(page.url()).pathname, closed: false, chatOnly: false }
      sockets.push(s)
      ws.on('framesent', (f) => {
        const text = typeof f.payload === 'string' ? f.payload : f.payload.toString('utf8')
        if (text.includes('"chatOnly":true')) s.chatOnly = true
      })
      ws.on('close', () => (s.closed = true))
    })
    // A view's sockets open now (a loaded machine may see one closed and opened again by its transport, one at a time).
    const views = (id: string) => sockets.filter((s) => s.id === id && !s.chatOnly && !s.closed)
    await memberPage(page, 'lead')
    await expect(page.locator('[data-transport-state="open"]')).toHaveCount(1, { timeout: LONG_MS })
    await expect.poll(() => views(lead).length, { message: "one view's socket to the member" }).toBe(1)
    expect(sockets.filter((s) => s.id === lead).map((s) => s.at), 'every socket to it opened on its place').toEqual(sockets.filter((s) => s.id === lead).map(() => runPath()))
    // Its row in the sidebar is the page; the run's header row is not.
    await expect(page.locator(`[data-sidebar-row="s:${lead}"] [aria-current="page"]`)).toHaveCount(1)
    // Another member's tab: the first member's socket closes, exactly one opens to the other.
    await page.locator(`[data-member-strip] [data-member-tab="core"]`).click()
    await expect.poll(() => where(page)).toBe(placeOf('core'))
    await expect(page.locator('[data-transport-state="open"]')).toHaveCount(1, { timeout: LONG_MS })
    await expect.poll(() => views(lead).length, { message: "the first member's view's socket closed" }).toBe(0)
    await expect.poll(() => views(core).length, { message: "one view's socket to the other member" }).toBe(1)
    // The old address was replaced, and the tab replaced the member: one Back leaves for where the person was.
    await page.goBack()
    await expect.poll(() => pagePath(page)).toBe('/crews')
  } finally {
    await page.context().close()
  }
})

test("a press on the member already open does nothing: its sidebar row and a palette row keep its file, its socket and history", async ({ browser }) => {
  writeFileSync(join(cwd, 'notes.txt'), 'one\ntwo\nthree\n')
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900, path: '/crews' })
  try {
    const lead = idOf('lead')
    budget(3 * LONG_MS)
    // The member with a file open in its editor, reached by a press inside the app: tab, open and line carried, and no read of the
    // session made on its old address (the live list knew it).
    await listed(page, lead)
    const reads: string[] = []
    page.on('request', (r) => sessionRead(lead)(r) && reads.push(new URL(page.url()).pathname))
    await inApp(page, `/sessions/${lead}?tab=files&open=notes.txt&line=2`)
    await expect.poll(() => new URL(page.url()).search).toBe(`?session=${lead}&tab=files&open=notes.txt&line=2`)
    expect(pagePath(page)).toBe(runPath())
    const tab = page.locator('[data-editor-tab]').filter({ hasText: 'notes.txt' })
    await expect(tab).toBeVisible({ timeout: LONG_MS })
    expect(reads.filter((p) => p !== runPath()), 'no read of the session on its old address').toEqual([])
    // Another member and back by the strip: the member's place alone, its file still open (kept for the app's life).
    await page.locator(`[data-member-strip] [data-member-tab="core"]`).click()
    await expect.poll(() => where(page)).toBe(placeOf('core'))
    await page.locator(`[data-member-strip] [data-member-tab="lead"]`).click()
    await expect.poll(() => page.url()).toBe(new URL(placeOf('lead'), page.url()).href)
    await expect(tab).toBeVisible({ timeout: LONG_MS })
    await expect(page.locator('[data-transport-state="open"]')).toHaveCount(1, { timeout: LONG_MS })
    // Any new socket to the member from here on would be a view built again.
    const sockets: string[] = []
    page.on('websocket', (ws) => ws.url().includes(`/ws/sessions/${lead}`) && sockets.push(ws.url()))
    const depth = () => page.evaluate(() => history.length)
    const before = await depth()
    const url = page.url()
    // Its own sidebar row: the same place, so no move at all.
    await page.locator(`[data-sidebar-row="s:${lead}"] a`).first().click()
    await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => r(null))))
    expect(page.url()).toBe(url)
    expect(await depth()).toBe(before)
    await expect(tab).toBeVisible()
    // A palette row for the member: the same.
    const name = (await own.api.session(lead)).name
    await page.locator('body').press('Alt+/')
    const palette = page.locator('[data-palette]')
    await expect(palette).toBeVisible()
    await page.keyboard.type(name)
    await palette.locator(`[data-palette-row="session:${lead}"]`).click()
    await expect(palette).toBeHidden()
    expect(await depth()).toBe(before)
    await expect(tab).toBeVisible()
    // Its History row, whose address the server builds as the session's own page: the same place too.
    await page.locator('body').press('Alt+/')
    await expect(palette).toBeVisible()
    await page.keyboard.type(name)
    const historyRow = palette.locator('[data-palette-row^="history:"]').filter({ hasText: name }).first()
    await expect(historyRow).toBeVisible({ timeout: LONG_MS })
    await historyRow.click()
    await expect(palette).toBeHidden()
    expect(page.url()).toBe(url)
    expect(await depth()).toBe(before)
    await expect(tab).toBeVisible()
    expect(sockets, 'no new socket to the member').toEqual([])
  } finally {
    await page.context().close()
  }
})

test("what a person typed in a member's files is there again after another member and back: both tabs unsaved, the one behind asks", async ({ browser }) => {
  writeFileSync(join(cwd, 'draft.txt'), 'first line\n')
  writeFileSync(join(cwd, 'second.txt'), 'second file\n')
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900, path: '/crews' })
  try {
    const lead = idOf('lead')
    budget(3 * LONG_MS)
    await listed(page, lead)
    await inApp(page, `/sessions/${lead}?open=draft.txt`)
    const tab = page.locator('[data-editor-tab]').filter({ hasText: 'draft.txt' })
    await expect(tab).toBeVisible({ timeout: LONG_MS })
    const editor = page.locator('[data-editor-area] .monaco-editor').first()
    await expect(editor).toBeVisible({ timeout: LONG_MS })
    await editor.click()
    await page.keyboard.press('Control+End')
    await page.keyboard.type('typed and not saved')
    await expect(tab.locator('[data-editor-dirty]')).toBeVisible()
    // A second file, opened in the same place, typed into too: it is the one in front, draft.txt behind it.
    await inApp(page, `${placeOf('lead')}&open=second.txt`)
    const second = page.locator('[data-editor-tab]').filter({ hasText: 'second.txt' })
    await expect(second).toHaveAttribute('data-editor-tab-active', '', { timeout: LONG_MS })
    await expect(page.locator('[data-editor-area] .monaco-editor .view-lines').first()).toContainText('second file', { timeout: LONG_MS })
    await page.locator('[data-editor-area] .monaco-editor').first().click()
    await page.keyboard.press('Control+End')
    await page.keyboard.type('also typed')
    await expect(second.locator('[data-editor-dirty]')).toBeVisible()
    await page.locator(`[data-member-strip] [data-member-tab="core"]`).click()
    await expect.poll(() => where(page)).toBe(placeOf('core'))
    await expect(page.locator('[data-editor-tab]').filter({ hasText: 'draft.txt' })).toHaveCount(0)
    await page.locator(`[data-member-strip] [data-member-tab="lead"]`).click()
    await expect.poll(() => where(page)).toBe(placeOf('lead'))
    // Both back, both unsaved: the one in front, and the one behind whose editor is not drawn.
    await expect(second).toBeVisible({ timeout: LONG_MS })
    await expect(second.locator('[data-editor-dirty]')).toBeVisible({ timeout: LONG_MS })
    await expect(tab.locator('[data-editor-dirty]')).toBeVisible()
    await expect(page.locator('[data-editor-area] .monaco-editor .view-lines').first()).toContainText('also typed', { timeout: LONG_MS })
    // Closing the one behind asks first, in the editor's own words.
    const id = await tab.getAttribute('data-editor-tab')
    await page.locator(`[data-editor-close="${id}"]`).click({ force: true })
    await expect(page.getByText('Save draft.txt?')).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(page.getByText('Save draft.txt?')).toHaveCount(0)
    await expect(tab).toBeVisible()
    // Its text is there when it is in front again.
    await tab.locator('button').first().click()
    await expect(page.locator('[data-editor-area] .monaco-editor .view-lines').first()).toContainText('typed and not saved', { timeout: LONG_MS })
  } finally {
    await page.context().close()
  }
})

test('a read that answers while the person is already on the way elsewhere moves nothing', async ({ browser }) => {
  const lead = idOf('lead')
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900, path: '/crews' })
  try {
    budget(2 * LONG_MS)
    let release: () => void = () => {}
    const held = new Promise<void>((r) => (release = r))
    await page.route((u) => u.pathname === `/api/sessions/${lead}`, async (route) => {
      if (route.request().method() !== 'GET') return route.fallback()
      await held
      await route.fallback()
    })
    // The old address, cold: the app's list not read yet, so the view asks for the session, and that read is held.
    const asked = page.waitForRequest(sessionRead(lead))
    await page.goto(`/sessions/${lead}`)
    await asked
    // The person's move, held on its way: the page it goes to is still loading its code when the read answers.
    let releaseCode: () => void = () => {}
    const code = new Promise<void>((r) => (releaseCode = r))
    const heldCode: string[] = []
    await page.route((u) => u.pathname.startsWith('/_nuxt/') && u.pathname.endsWith('.js'), async (route) => {
      heldCode.push(route.request().url())
      await code
      await route.fallback()
    })
    const going = inApp(page, '/carousel').catch(() => undefined)
    await expect.poll(() => heldCode.length, { message: "the next page's code is on its way, held" }).toBeGreaterThan(0)
    const answered = page.waitForResponse((r) => sessionRead(lead)(r.request()))
    release()
    await answered
    releaseCode()
    await going
    // Where the person went, once that page has loaded: the read's answer sent the page nowhere.
    // (The carousel's address is the Roundhouse's old one: it lands there.)
    await expect.poll(() => pagePath(page), { timeout: LONG_MS }).toBe('/roundhouse')
    await expect(page.locator('[data-member-strip]')).toHaveCount(0)
  } finally {
    await page.context().close()
  }
})

test("a list and a read that disagree move the page once, and it stands", async ({ browser }) => {
  // A session of its own whose read says it is this run's member (the app's list says it is no one's, as a list can for a while).
  const alone = await own.api.ok<{ id: string }>('POST', '/api/sessions', { agentId: 'shell', name: 'e2e disagree', cwd })
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900, path: '/crews' })
  try {
    budget(3 * LONG_MS)
    let reads = 0
    await page.route((u) => u.pathname === `/api/sessions/${alone.id}`, async (route) => {
      if (route.request().method() !== 'GET') return route.fallback()
      reads++
      const res = await route.fetch()
      const body = await res.json()
      body.session.crew = { runId: run.id, crewId, member: 'ghost' }
      await route.fulfill({ response: res, json: body })
    })
    await listed(page, alone.id)
    await inApp(page, `/sessions/${alone.id}`)
    await expect.poll(() => where(page)).toBe(`${runPath()}?session=${alone.id}`)
    await expect(page.locator('.terminal-host .xterm-screen').first()).toBeVisible({ timeout: LONG_MS })
    // Drawn in the run's place and left there: the list, which still says otherwise, sends it nowhere.
    await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => r(null))))
    expect(where(page)).toBe(`${runPath()}?session=${alone.id}`)
    expect(reads, 'one read on the old address, one in its place').toBe(2)
  } finally {
    await page.context().close()
    await own.api.stopSession(alone.id)
  }
})

test("from the run's page a tile opens its member in place; Back to the crew, then Back, leave; the run's palette rows come back", async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900, path: '/crews' })
  try {
    budget(2 * LONG_MS)
    await inApp(page, `${runPath()}?view=grid&task=none`)
    await page.locator('[data-member="core"] [data-tile-open]').click()
    // The member's address holds the member alone (the run's own query comes back through history).
    await expect.poll(() => new URL(page.url()).search).toBe(`?session=${idOf('core')}`)
    await expect(page.locator('[data-run-grid]')).toHaveCount(0)
    await expect(page.locator(`[data-member-strip] [data-member-tab="core"]`)).toHaveAttribute('aria-current', 'page')
    // Back to the crew is the browser's Back here: the run's page, and one more Back leaves it for the page before.
    await page.getByRole('button', { name: 'Back to the crew' }).click()
    await expect.poll(() => where(page)).toBe(runPath())
    await expect(page.locator('[data-member="core"] [data-tile-open]')).toBeVisible({ timeout: LONG_MS })
    // The run's own rows are the palette's page group again.
    await page.locator('body').press('Alt+/')
    const palette = page.locator('[data-palette]')
    await expect(palette.locator('[data-palette-row="page:broadcast"]')).toHaveCount(1)
    await page.keyboard.press('Escape')
    await expect(palette).toBeHidden()
    await page.goBack()
    await expect.poll(() => pagePath(page)).toBe('/crews')
  } finally {
    await page.context().close()
  }
})

test("only the run's own member is drawn on its address: a session of its own, one removed, another run's member", async ({ browser }) => {
  const api = own.api
  const alone = await api.ok<{ id: string }>('POST', '/api/sessions', { agentId: 'shell', name: 'e2e not a member', cwd })
  const removed = await api.ok<{ id: string }>('POST', '/api/sessions', { agentId: 'shell', name: 'e2e removed', cwd })
  const otherCrew = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', { name: `${CREW} other`, goal: '', cwd, where: 'server', isolation: 'none', openAfterLaunch: false, members: [{ name: 'solo', agentId: 'shell', prompt: '', start: { when: 'immediately' } }] })).crew.id
  let other = await api.launchCrew(otherCrew)
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900, path: '/crews' })
  try {
    budget(6 * LONG_MS)
    await expect.poll(async () => member(await api.run(other.id), 'solo').sessionId ?? '', { timeout: LONG_MS }).not.toBe('')
    other = await api.run(other.id)
    const solo = member(other, 'solo').sessionId!
    await api.stopSession(removed.id)
    await expect.poll(async () => (await api.session(removed.id)).status, { timeout: STEP_MS }).toMatch(/exited|stopped/)
    await api.stopSession(removed.id)
    await expect.poll(async () => (await api.call('GET', `/api/sessions/${removed.id}`)).status, { timeout: STEP_MS }).toBe(404)

    // A session of its own, reached inside the app on the run's address: its bar is never drawn there.
    await listed(page, alone.id)
    await inApp(page, runPath())
    await expect(page.locator('[data-run-view]')).toBeVisible({ timeout: LONG_MS })
    await page.evaluate(() => {
      const w = window as unknown as { barOnRun: boolean }
      w.barOnRun = false
      new MutationObserver(() => {
        if (location.pathname.startsWith('/runs/') && document.querySelector('[data-session-bar]')) w.barOnRun = true
      }).observe(document.body, { childList: true, subtree: true })
    })
    await inApp(page, `${runPath()}?session=${alone.id}`)
    await expect.poll(() => pagePath(page)).toBe(`/sessions/${alone.id}`)
    await expect(page.locator('[data-session-bar]')).toBeVisible({ timeout: LONG_MS })
    expect(await page.evaluate(() => (window as unknown as { barOnRun: boolean }).barOnRun), "the session's bar never drawn on the run's address").toBe(false)
    await expect(page.locator('[data-member-strip]')).toHaveCount(0)

    // One the server no longer holds: drawn on its own page, its gone words there.
    await page.goto(`${runPath()}?session=${removed.id}`)
    await expect.poll(() => pagePath(page)).toBe(`/sessions/${removed.id}`)
    await expect(page.locator('[data-session-gone]')).toBeVisible({ timeout: LONG_MS })

    // Another run's member, asked for on this run's address: it lands in its own run.
    await page.goto(`${runPath()}?session=${solo}`)
    await expect.poll(() => where(page)).toBe(`/runs/${encodeURIComponent(other.id)}?session=${solo}`)
    await expect(page.locator(`[data-member-strip][data-run="${other.id}"]`)).toBeVisible({ timeout: LONG_MS })
  } finally {
    await page.context().close()
    await api.stopSession(alone.id)
    await api.stopRun(other.id)
    await api.call('DELETE', `/api/crews/${encodeURIComponent(otherCrew)}`)
  }
})

test("a member with its switch, Yolo, a long name and three here keeps its name readable and its buttons in the row, at 1500, 1700 and 2000", async ({ browser }) => {
  const api = own.api
  // A member's name at the most a crew allows (forty characters).
  const LONG = 'reviewer-of-the-refund-split-and-its-run'
  const members = [
    { name: LONG, agentId: 'claude-proto', prompt: 'say hello', start: { when: 'immediately' } },
    ...Array.from({ length: 11 }, (_, i) => ({ name: `member-${String(i + 1).padStart(2, '0')}`, agentId: 'shell', prompt: '', start: { when: 'immediately' } })),
  ]
  const crew = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', { name: `${CREW} wide`, goal: '', cwd, where: 'server', isolation: 'none', openAfterLaunch: false, yolo: true, members })).crew.id
  let wide = await api.launchCrew(crew)
  const pages: Page[] = []
  try {
    budget(4 * LONG_MS)
    await expect.poll(async () => member(await api.run(wide.id), LONG).sessionId ?? '', { timeout: 2 * LONG_MS }).not.toBe('')
    await expect.poll(async () => member(await api.run(wide.id), 'member-11').sessionId ?? '', { timeout: 2 * LONG_MS }).not.toBe('')
    wide = await api.run(wide.id)
    const id = member(wide, LONG).sessionId!
    // Two others here: three viewers with the page that measures.
    for (let i = 0; i < 2; i++) {
      const other = await dayPage(browser, { server: own.started, width: 1440, height: 900, path: `/sessions/${id}` })
      pages.push(other)
      await expect(other.locator('.terminal-host .xterm-screen').first()).toBeVisible({ timeout: LONG_MS })
    }
    for (const width of [1500, 1700, 2000]) {
      budget(3 * STEP_MS + LONG_MS)
      const page = await dayPage(browser, { server: own.started, width, height: 810, path: '/crews' })
      pages.push(page)
      await page.goto(`/sessions/${id}`)
      await expect.poll(() => where(page)).toBe(`/runs/${encodeURIComponent(wide.id)}?session=${id}`)
      await expect(page.locator('[data-way-switch]')).toBeVisible({ timeout: LONG_MS })
      await expect(page.locator('[data-session-bar] [data-yolo-badge]').first()).toBeVisible()
      // Three here, as the avatars at the right say (their title names everyone).
      await expect.poll(async () => ((await page.locator('[data-session-bar] [data-slot="right"] [title*=","]').first().getAttribute('title').catch(() => '')) ?? '').split(',').length, { timeout: LONG_MS }).toBeGreaterThanOrEqual(3)
      const strip = page.locator(`[data-member-strip][data-run="${wide.id}"]`)
      await expect(strip.locator('[data-member-tab]')).toHaveCount(12, { timeout: LONG_MS })
      const bar = page.locator('[data-session-bar]')
      await atRest(bar)
      // The name keeps a readable width, clear of the buttons at the right, which stay inside the row. (The switch's two sides share a
      // border by design, so the bar is judged by its parts rather than by noOverlap.)
      const [name, line, right, row] = [await page.locator('[data-session-title]').boundingBox(), await page.locator('[data-session-name-line]').boundingBox(), await bar.locator('[data-slot="right"]').boundingBox(), await bar.boundingBox()]
      expect(name!.width, `the name at ${width}`).toBeGreaterThanOrEqual(150)
      expect(line!.x + line!.width, `the name line clear of the buttons at ${width}`).toBeLessThanOrEqual(right!.x + 0.5)
      expect(right!.x + right!.width, `the buttons inside the row at ${width}`).toBeLessThanOrEqual(row!.x + row!.width + 0.5)
      // The open tab whole; a tab reached by the keyboard is scrolled clear of the fade.
      const tabs = strip.locator('[data-member-tabs]')
      await uncut(tabs.locator(`[data-member-tab="${LONG}"]`))
      const last = tabs.locator('[data-member-tab="member-11"]')
      await last.focus()
      // Clear of an edge's fade where that edge fades (an edge the tabs end at does not).
      const clear = async (t: typeof last) => {
        const [l, b, more] = [await tabs.boundingBox(), await t.boundingBox(), (await tabs.getAttribute('data-tabs-more')) ?? '']
        if (!l || !b) return false
        const left = /left|both/.test(more) ? 20 : 0
        const right = /right|both/.test(more) ? 20 : 0
        return b.x >= l.x + left - 0.5 && b.x + b.width <= l.x + l.width - right + 0.5
      }
      await expect.poll(() => clear(last), { message: `the focused tab clear of the fade at ${width}` }).toBe(true)
      await uncut(last)
      const middle = tabs.locator('[data-member-tab="member-05"]')
      await middle.focus()
      await expect.poll(() => clear(middle), { message: `a middle tab, focused, clear of the fade at ${width}` }).toBe(true)
      await uncut(middle)
    }
  } finally {
    for (const p of pages) await p.context().close()
    await api.stopRun(wide.id)
    await api.call('DELETE', `/api/crews/${encodeURIComponent(crew)}`)
  }
})

test('Alt+Q goes to Crews while the run is missing; the rail marks the open member, not its run', async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900, path: '/crews' })
  try {
    budget(3 * LONG_MS)
    const lead = idOf('lead')
    // The rail: the open member's square is the page, its run's capsule is not; then the sidebar back as it was.
    await memberPage(page, 'lead')
    await page.locator('[data-header-account]').first().click()
    await page.getByRole('menuitem', { name: /Collapse to the rail/ }).click()
    await expect(page.locator('[data-rail]')).toBeVisible()
    await expect(page.locator(`[data-rail-session="${lead}"]`)).toHaveAttribute('aria-current', 'page', { timeout: LONG_MS })
    // The capsule's own link (its play mark, its first), not the member's square inside it.
    await expect(page.locator(`[data-rail-run="${run.id}"] > a`).first()).not.toHaveAttribute('aria-current', 'page')
    await page.locator('[data-rail-expand]').click()
    await expect(page.locator('[data-rail]')).toHaveCount(0)
    await page.context().close()

    // A run the server no longer holds (its reads answer so): the strip's way back is Crews, and Alt+Q in the terminal goes there.
    const cold = await dayPage(browser, { server: own.started, width: 1440, height: 900, path: '/crews' })
    await cold.route((u) => u.pathname === '/api/runs', async (route) => {
      const res = await route.fetch()
      const body = (await res.json()) as { runs: Array<{ id: string }> }
      await route.fulfill({ response: res, json: { ...body, runs: body.runs.filter((r) => r.id !== run.id) } })
    })
    await cold.route((u) => u.pathname === `/api/runs/${run.id}`, (route) => route.fulfill({ status: 404, json: { error: { code: 'not_found', message: 'no such run' } } }))
    await cold.goto(`${runPath()}?session=${lead}`)
    await expect(cold.locator('[data-member-strip] [data-back-to-crews]')).toBeVisible({ timeout: LONG_MS })
    await expect(cold.locator('.terminal-host .xterm-screen').first()).toBeVisible({ timeout: LONG_MS })
    await cold.locator('.terminal-host .xterm-screen').first().click()
    await expect(cold.locator('.terminal-host .xterm-helper-textarea').first()).toBeFocused()
    await cold.keyboard.press('Alt+KeyQ')
    await expect.poll(() => pagePath(cold)).toBe('/crews')
    await cold.context().close()
  } finally {
    await page.context().close().catch(() => undefined)
  }
})
