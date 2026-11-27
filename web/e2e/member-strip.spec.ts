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
// Chat side dimmed with why, and a press of it sends nothing.
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

/** A member's page, its terminal drawn and its strip holding the run's title. */
async function memberPage(page: Page, name: string): Promise<void> {
  budget(STEP_MS + LONG_MS)
  await page.goto(`/sessions/${encodeURIComponent(idOf(name))}`)
  await expect(page.locator('.terminal-host .xterm-screen').first()).toBeVisible({ timeout: LONG_MS })
  await expect(page.locator(`[data-member-strip][data-run="${run.id}"] [data-member-strip-title]`)).toHaveText(CREW)
}

/** The header at rest: the strip and the session's bar each clear inside, the strip above the bar, the session's name whole. */
async function headerWhole(page: Page): Promise<void> {
  const strip = page.locator('[data-member-strip]')
  const bar = page.locator('[data-session-bar]')
  await atRest(strip)
  await atRest(bar)
  await noOverlap(strip)
  await noOverlap(bar)
  await uncut(page.locator('[data-session-title]'))
  const [s, b] = [await strip.boundingBox(), await bar.boundingBox()]
  expect(s && b && s.y + s.height <= b.y + 0.5, 'the strip sits above the bar').toBe(true)
}

for (const size of [
  { width: 2000, height: 810 },
  { width: 1440, height: 900 },
]) {
  test(`a member's page carries its crew; Back to the crew lands on the run (${size.width} x ${size.height})`, async ({ browser }) => {
    const page = await dayPage(browser, { server: own.started, ...size })
    try {
      await memberPage(page, 'lead')
      const back = page.getByRole('link', { name: 'Back to the crew' })
      await expect(back).toBeVisible()
      await expect(back).toHaveText('Back to the crew')
      await expect(page.locator('[data-back-to-list]')).toHaveCount(0)
      await headerWhole(page)
      // The tooltip names the run it goes back to, with its key.
      await back.hover()
      // reka keeps the role on a copy of the words, hidden from the tree, for the button's description (tooltips.spec.ts).
      await expect(page.getByRole('tooltip', { includeHidden: true })).toContainText(`Back to ${CREW}`)
      await back.click()
      await expect.poll(() => pagePath(page)).toBe(`/runs/${encodeURIComponent(run.id)}`)
    } finally {
      await page.context().close()
    }
  })
}

test("a member's tab opens that member in this page's place: one Back leaves the crew's members", async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 1440, height: 900 })
  try {
    const home = pagePath(page)
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
    await expect.poll(() => pagePath(page)).toBe(`/sessions/${encodeURIComponent(idOf('core'))}`)
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
    // Dimmed, not disabled: it keeps its tooltip, on hover and on focus.
    await expect(chat).toHaveAttribute('aria-disabled', 'true')
    await expect(chat).toBeEnabled()
    // No line stands above a member's terminal, the first reply's among them.
    await expect(page.locator('[data-way-line]')).toHaveCount(0)
    await chat.hover()
    await expect(page.getByRole('tooltip', { includeHidden: true })).toContainText(MEMBER_TERMINAL)
    // A press says it as the line, for a moment, and moves nothing.
    await chat.click()
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
    const path = pagePath(page)
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
    expect(pagePath(page)).toBe(path)
    await expect(page.locator('[data-member-strip]')).toBeVisible()
    // Alt+Q, the terminal still holding the keys, goes back to the crew.
    await expect(page.locator('.terminal-host .xterm-helper-textarea').first()).toBeFocused()
    await page.keyboard.press('Alt+KeyQ')
    await expect.poll(() => pagePath(page)).toBe(`/runs/${encodeURIComponent(run.id)}`)
  } finally {
    await page.context().close()
  }
})

test("on a phone the strip is a member's way back, in view; Back to the list is not drawn", async ({ browser }) => {
  const page = await dayPage(browser, { server: own.started, width: 390, height: 844, touch: true })
  try {
    await memberPage(page, 'lead')
    const back = page.getByRole('link', { name: 'Back to the crew' })
    await expect(back).toBeInViewport()
    await expect(back).toHaveText('Back to the crew')
    await expect(page.locator('[data-back-to-list]')).toHaveCount(0)
    await headerWhole(page)
    await back.tap()
    await expect.poll(() => pagePath(page)).toBe(`/runs/${encodeURIComponent(run.id)}`)
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
