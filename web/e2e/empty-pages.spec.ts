import type { Page, Route } from '@playwright/test'
import { budget, dayPage, expect, test, useTheme, type Session } from './fixtures'
import { inside, noSideways, tapTarget, uncut } from './layout'
import { ownServer, type OwnServer } from './own-server'
import { START_WAIT_MS } from './server'

// One shape for a page with nothing in it (UX1 PR-22, docs/plans/ux-review.md, P23): the page's own sentence, why it is empty and
// what fills it, and the action that fills it; the reading shape before the first read, the failed shape when that read failed, the
// not-found shape for a thing that is not there. Never the empty picture before the sessions are read: a server with sessions never
// flashes the welcome or an empty shape.

/** An expect's bound and an action's (the config's expect timeout). */
const STEP_MS = 15_000

/** The pages' sentences, word for word as the pages' table gives them (web/app/utils/pages.ts). */
const ABOUT = {
  yard: 'Every live terminal on one screen.',
  roundhouse: 'One terminal at a time, on rotation.',
  history: 'What was said, decided and checked, kept after the work ends.',
}
const YARD_WHY = 'Nothing is running. A terminal shows here when you launch an agent, or bring one in from another machine.'
const ROUNDHOUSE_WHY = 'Nothing is running. The first agent you launch shows here.'
const HOME_WHY = 'Nothing is running. Launch an agent, or resume one from Exited in the list.'

/** The windows: the suite's desktop, and its phone with touch. */
const DESK = { width: 1440, height: 900 }
const PHONE = { width: 390, height: 844, touch: true }

// A server with nothing on it, shared by the cases that only read it (the shared e2e server is never empty, and counting there would
// count what other specs left). A case that makes something starts a server of its own.
let emptyOne: OwnServer | undefined
async function empty(): Promise<OwnServer> {
  if (!emptyOne) {
    budget(START_WAIT_MS)
    emptyOne = await ownServer('empty-pages')
  }
  return emptyOne
}
test.afterAll(async () => {
  await emptyOne?.stop()
  emptyOne = undefined
})

const shape = (page: Page, state = 'empty') => page.locator(`[data-page-empty][data-state="${state}"]`)

/**
 * The empty shape of `path`, read whole: it is the one shape on the page and no welcome is drawn; its first line is `about` (none when
 * not given), its second `why`, its first action `action` with its key `kbd`; each part whole and inside the page's panel; at the phone
 * the action is big enough to tap and nothing scrolls sideways.
 */
async function readEmpty(page: Page, at: string, o: { panel: string; about?: string; why: string; action: string; kbd?: string; phone?: boolean }) {
  const s = shape(page)
  await expect(s, `${at}: the empty shape`).toBeVisible()
  await expect(page.locator('[data-page-empty]'), `${at}: one shape`).toHaveCount(1)
  await expect(page.locator('[data-welcome]'), `${at}: no welcome`).toHaveCount(0)
  if (o.about) await expect(s.locator('[data-page-about]'), `${at}: the page's sentence`).toHaveText(o.about)
  else await expect(s.locator('[data-page-about]'), `${at}: no first line`).toHaveCount(0)
  await expect(s.locator('[data-page-why]'), `${at}: why it is empty`).toHaveText(o.why)
  const action = s.locator('[data-page-action]').first()
  await expect(action.locator('[data-slot="label"]'), `${at}: the action`).toHaveText(o.action)
  if (o.kbd) await expect(action.locator('[data-page-kbd]'), `${at}: the action's key`).toHaveText(o.kbd)
  const parts = s.locator('[data-page-about], [data-page-why], [data-page-action] [data-slot="label"]')
  await uncut(parts)
  await inside(s.locator('[data-page-about], [data-page-why], [data-page-action]'), page.locator(o.panel))
  if (o.phone) {
    await tapTarget(s.locator('[data-page-action]'))
    await noSideways(page)
  }
}

test('a server with nothing on it: the Yard and the Roundhouse draw the one empty shape, at the desktop and the phone, in both themes', async ({ browser }) => {
  const srv = await empty()
  for (const win of [DESK, PHONE]) {
    const phone = win === PHONE
    budget(2 * 2 * 12 * STEP_MS) // two pages, two themes, each a load and the reads of readEmpty
    for (const [path, o] of [
      ['/yard', { panel: '#dashboard-panel-wall', about: ABOUT.yard, why: YARD_WHY, action: 'Launch agent', kbd: 'N' }],
      ['/roundhouse', { panel: '#dashboard-panel-carousel', about: ABOUT.roundhouse, why: ROUNDHOUSE_WHY, action: 'Launch agent', kbd: 'N' }],
    ] as const) {
      const page = await dayPage(browser, { ...win, path, server: srv.started })
      const at = `${path} at ${win.width}`
      await readEmpty(page, `${at}, dark`, { ...o, phone })
      if (path === '/yard') {
        const how = shape(page).locator('[data-page-action]').nth(1)
        await expect(how).toHaveText('How to bring one in')
        await expect(how).toHaveAttribute('href', /#quick-start$/)
      }
      // The theme switched as the app keeps it: the page drawn again, and every part read again after that redraw.
      await useTheme(page, 'light')
      await readEmpty(page, `${at}, light`, { ...o, phone })
      await page.context().close()
    }
  }
  // The phone's Sessions page: the sidebar's line alone, Launch agent standing above it.
  const page = await dayPage(browser, { ...PHONE, path: '/sessions', server: srv.started })
  const line = page.locator('[data-sidebar-empty]')
  await expect(line).toHaveText('No sessions yet.')
  await uncut(line)
  await expect(page.locator('[data-page-empty]')).toHaveCount(0)
})

test('a server whose one session has exited: the home says to resume it from Exited, the Yard is empty, the sidebar lists it', async ({ browser }) => {
  budget(START_WAIT_MS + 6 * STEP_MS)
  const srv = await ownServer('empty-pages-exited')
  try {
    const s = await srv.api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e empty exited' })
    await srv.api.stopSession(s.id)
    await expect.poll(async () => (await srv.api.session(s.id)).status, { message: 'the session ends' }).toMatch(/^(exited|stopped)$/)

    const page = await dayPage(browser, { ...DESK, path: '/', server: srv.started })
    await readEmpty(page, 'the home', { panel: '#dashboard-panel-home', why: HOME_WHY, action: 'Launch agent', kbd: 'N' })
    // Listed under Exited, so the sidebar has no empty line.
    await expect(page.locator('[data-sidebar-section="exited"]')).toBeVisible()
    await expect(page.locator('[data-sidebar-empty]')).toHaveCount(0)

    await page.goto('/yard')
    await readEmpty(page, 'the Yard', { panel: '#dashboard-panel-wall', about: ABOUT.yard, why: YARD_WHY, action: 'Launch agent', kbd: 'N' })
  } finally {
    await srv.stop()
  }
})

test('History with nothing kept says so under its sentence; a query that matches nothing says that, without the sentence', async ({ browser }) => {
  const srv = await empty()
  budget(12 * STEP_MS)
  const page = await dayPage(browser, { ...DESK, path: '/history', server: srv.started })
  const none = page.locator('[data-history-none]')
  const kept = async (at: string) => {
    await expect(none, at).toHaveAttribute('data-page-empty', /.*/)
    await expect(none.locator('[data-page-about]'), at).toHaveText(ABOUT.history)
    await expect(none.locator('[data-page-why]'), at).toHaveText('No session is kept yet.')
    await expect(none, at).not.toContainText('matches')
  }
  await kept('nothing typed')
  // Nothing kept: the search's caption stands in place, and there is no About this page.
  await expect(page.locator('[data-history-caption]')).toBeVisible()
  await expect(page.locator('[data-page-about-button]')).toHaveCount(0)

  const input = page.locator('[data-history-input] input, input[data-history-input]').first()
  await input.fill('zzz')
  await expect(input).toHaveValue('zzz')
  await expect(none.locator('[data-page-why]')).toHaveText('Nothing in the history matches.')
  await expect(none.locator('[data-page-about]')).toHaveCount(0)
  // Cleared: the first words come back, read again after that redraw.
  await input.fill('')
  await expect(input).toHaveValue('')
  await kept('cleared')
})

test('a focused session not on the server draws the not-found shape, with the way back and its record', async ({ browser }) => {
  const srv = await empty()
  budget(6 * STEP_MS)
  const page = await dayPage(browser, { ...DESK, path: '/yard?focus=nope', server: srv.started })
  const s = shape(page, 'missing')
  await expect(s).toBeVisible()
  await expect(s.locator('[data-page-about]')).toHaveCount(0)
  await expect(s.locator('[data-page-why]')).toHaveText('That session is no longer on this server.')
  const actions = s.locator('[data-page-action]')
  await expect(actions.nth(0)).toHaveText('Back to the grid')
  await expect(actions.nth(1)).toHaveText('Its record')
  await expect(actions.nth(1)).toHaveAttribute('href', '/history/sessions/nope')
  await uncut(s.locator('[data-page-why]'))
  await actions.nth(0).click()
  await expect(page).toHaveURL(/\/yard$/)
  await expect(shape(page)).toBeVisible()
})

test('the first read fails: the Yard says the server\'s words, and the store\'s own retry brings the empty shape', async ({ browser }) => {
  const srv = await empty()
  budget(4 * STEP_MS + 30_000)
  const page = await dayPage(browser, { ...DESK, path: '/settings', server: srv.started })
  let failing = true
  const fail = (r: Route) => (failing ? r.fulfill({ status: 500, contentType: 'application/json', body: '{"error":{"code":"internal","message":"the store is down"}}' }) : r.fallback())
  await page.route('**/api/events*', fail)
  await page.route('**/api/sessions', (r) => (r.request().method() === 'GET' ? fail(r) : r.fallback()))
  await page.goto('/yard')
  const failed = shape(page, 'failed')
  await expect(failed).toBeVisible()
  await expect(failed.locator('[data-page-about]')).toHaveText(ABOUT.yard)
  await expect(failed.locator('[data-page-why]')).not.toBeEmpty()
  await expect(failed.locator('[data-page-again]')).toBeVisible()
  // The read refused, no alert is drawn over it: the failed shape says the server's words once.
  const words = (await failed.locator('[data-page-why]').textContent())!.trim()
  await expect(page.locator('#dashboard-panel-wall').getByText(words, { exact: true })).toHaveCount(1)
  failing = false
  // The store reads again by itself (its poll while the stream is down: every 3 s; the stream's backoff after it): no press.
  await expect(shape(page)).toBeVisible({ timeout: 30_000 })
})

/**
 * Records, in every document the page loads, every element it ever draws that says the page is empty or a thing is not there:
 * the welcome, an empty shape, a not-found shape. Read with `seen()`.
 */
async function emptyRecorder(page: Page): Promise<() => string[]> {
  const seen: string[] = []
  await page.exposeFunction('__emptySeen', (s: string) => void seen.push(s))
  await page.addInitScript(() => {
    const watch = ['[data-welcome]', '[data-page-empty][data-state="empty"]', '[data-page-empty][data-state="missing"]']
    const check = () => {
      for (const sel of watch)
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        if (document.querySelector(sel)) void (window as any).__emptySeen(`${sel} at ${location.pathname}${location.search}`)
    }
    new MutationObserver(check).observe(document, { childList: true, subtree: true, attributes: true, attributeFilter: ['data-state'] })
  })
  return () => [...seen]
}

/** Holds the event stream and the sessions' list until `release`: the first read of the sessions is out while held. */
async function holdSessions(page: Page): Promise<{ release: () => Promise<void>; hold: () => void }> {
  let held: Route[] = []
  let holding = true
  const take = (r: Route) => (holding ? void held.push(r) : r.fallback())
  await page.route('**/api/events*', take)
  await page.route('**/api/sessions', (r) => (r.request().method() === 'GET' ? take(r) : r.fallback()))
  return {
    hold: () => {
      holding = true
    },
    release: async () => {
      holding = false
      const go = held
      held = []
      for (const r of go) await r.fallback().catch(() => undefined)
    },
  }
}

test('the sessions not yet read: every page shows the reading shape, and never an empty picture, on a server with sessions', async ({ day, browser }) => {
  const names = await day.part('states')
  const live = (await day.listSessions()).find((s) => s.status === 'running' && Object.values(names).includes(s.id))
  expect(live, 'a live session of the day').toBeTruthy()
  budget(4 * 5 * STEP_MS)
  const page = await dayPage(browser, { ...DESK, path: '/settings' })
  const seen = await emptyRecorder(page)
  const held = await holdSessions(page)
  for (const [path, then] of [
    ['/yard', () => expect(page.locator('[data-session-tile]').first()).toBeVisible()],
    ['/roundhouse', () => expect(page.locator('[data-page-empty]')).toHaveCount(0)],
    ['/', () => expect(page).toHaveURL(/\/sessions\/[^/]+$/)],
    [`/yard?focus=${live!.id}`, () => expect(page.locator('[data-yard-focus] [data-quick-reply], [data-yard-focus] .xterm').first()).toBeVisible()],
  ] as const) {
    held.hold()
    await page.goto(path)
    await expect(shape(page, 'reading'), `${path}: the reading shape while the sessions are held`).toBeVisible()
    await held.release()
    await then()
    await expect(shape(page, 'reading'), `${path}: read`).toHaveCount(0)
  }
  expect(seen(), 'nothing said the page was empty or the session gone').toEqual([])
})

test('About this page: History with entries folds its caption to a popover, which Escape closes', async ({ day, browser }) => {
  await day.part('states')
  budget(2 * 8 * STEP_MS)
  for (const theme of ['dark', 'light'] as const) {
    const page = await dayPage(browser, { ...DESK, theme, path: '/history' })
    await expect(page.locator('[data-history-group]').first()).toBeVisible({ timeout: 30_000 })
    await expect(page.locator('[data-history-caption]'), `${theme}: no caption in the body`).toHaveCount(0)
    const button = page.locator('[data-page-about-button]')
    await button.click()
    const open = page.locator('[data-page-about-open]')
    await expect(open, theme).toContainText('a sentence repeated is still Conversation')
    await uncut(open.locator('p'))
    await page.keyboard.press('Escape')
    // A popover that has closed is not gone at once: wait for it to be gone before the next step.
    await expect(open).toHaveCount(0)
    await page.context().close()
  }
})

