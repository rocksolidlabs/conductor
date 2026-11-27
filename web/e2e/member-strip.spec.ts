import { existsSync, mkdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Page } from '@playwright/test'
import { atRest, dayPage, expect, member, pagePath, test, type Run } from './fixtures'
import { noOverlap, uncut } from './layout'
import { ownServer, type OwnServer } from './own-server'

// A crew member's page carries its crew (UX3 R12, #1170): one strip above the session's header with `Back to the crew` and the run's
// title, at every width. A run of two shells launched by the spec, on a server of its own: a member's page shows the strip with the
// run's title; `Back to the crew` lands on the run's page; Alt+Q typed while the terminal has the keys goes back too, and Escape typed
// there reaches the shell while the page stays; on a phone the strip is in view and `Back to the list` is not drawn. The session's name
// is whole and nothing in the header overlaps, measured at rest, at 2000 x 810, 1440 x 900 and 390 x 844. Members are found by the
// run's id and their names, never by a count.
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
  own = await ownServer('member-strip')
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
      ],
    })
  ).crew.id
  run = await api.launchCrew(crewId)
  for (const name of ['lead', 'core']) {
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
      await expect(page.getByRole('tooltip')).toContainText(`Back to ${CREW}`)
      await back.click()
      await expect.poll(() => pagePath(page)).toBe(`/runs/${encodeURIComponent(run.id)}`)
    } finally {
      await page.context().close()
    }
  })
}

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
  } finally {
    await page.context().close()
  }
})
