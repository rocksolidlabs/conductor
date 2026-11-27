import type { Browser, Page, Route } from '@playwright/test'
import { atRest, budget, dayPage, expect, readAgain, test, useTheme, type Session } from './fixtures'
import { inside, noOverlap, noSideways, tapTarget, uncut } from './layout'
import { ownServer, type OwnServer } from './own-server'
import { START_WAIT_MS } from './server'

// One shape for a page with nothing in it (UX1 PR-22 and PR-23, docs/plans/ux-review.md, P23): the page's own sentence, why it is
// empty and what fills it, and the action that fills it; the reading shape before the first read, the failed shape when that read
// failed, the not-found shape for a thing that is not there. Never the empty picture before the sessions are read: a server with
// sessions never flashes the welcome or an empty shape. A page's standing paragraph folds to About this page once it has content.

/** An expect's bound and an action's (the config's expect timeout). */
const STEP_MS = 15_000

/** The pages' sentences, word for word as the pages' table gives them (web/app/utils/pages.ts). */
const ABOUT = {
  yard: 'Every live terminal on one screen.',
  roundhouse: "The Yard's second view: one terminal at a time, on rotation.",
  history: 'What was said, decided and checked, kept after the work ends.',
  crews: 'Saved teams of agents, and each run of one.',
  dispatch: 'Tickets, the plans you approve, the runs they start.',
  feed: 'What your agents report, as it happens.',
  brakeman: 'A sidekick that reads all of it and briefs you.',
  agents: 'The agents this machine can launch, and how each reports to Conductor.',
  decisions: 'Everything that waits for your answer, in one list.',
}
const DECISIONS_WHY = 'Nothing needs you now.'
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
async function readEmpty(page: Page, at: string, o: { panel: string; about?: string; why: string; action?: string; kbd?: string; phone?: boolean }) {
  const s = shape(page)
  await expect(s, `${at}: the empty shape`).toBeVisible()
  await expect(page.locator('[data-page-empty]'), `${at}: one shape`).toHaveCount(1)
  await expect(page.locator('[data-welcome]'), `${at}: no welcome`).toHaveCount(0)
  if (o.about) await expect(s.locator('[data-page-about]'), `${at}: the page's sentence`).toHaveText(o.about)
  else await expect(s.locator('[data-page-about]'), `${at}: no first line`).toHaveCount(0)
  await expect(s.locator('[data-page-why]'), `${at}: why it is empty`).toHaveText(o.why)
  const action = s.locator('[data-page-action]').first()
  if (o.action) {
    await expect(action.locator('[data-slot="label"]'), `${at}: the action`).toHaveText(o.action)
    if (o.kbd) await expect(action.locator('[data-page-kbd]'), `${at}: the action's key`).toHaveText(o.kbd)
    else await expect(action.locator('[data-page-kbd]'), `${at}: the action has no key`).toHaveCount(0)
  } else await expect(s.locator('[data-page-action]'), `${at}: no action`).toHaveCount(0)
  const parts = s.locator('[data-page-about], [data-page-why], [data-page-action] [data-slot="label"]')
  // A shape below the page's other parts (Brakeman's, under its Ask card) is scrolled to, as a person would.
  await s.scrollIntoViewIfNeeded()
  await uncut(parts)
  await inside(s.locator('[data-page-about], [data-page-why], [data-page-action]'), page.locator(o.panel))
  if (o.phone) {
    if (o.action) await tapTarget(s.locator('[data-page-action]'))
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
  // The phone's Sessions page: the welcome in place of the list's empty line (the UX review's P17), Launch agent standing above it.
  const page = await dayPage(browser, { ...PHONE, path: '/sessions', server: srv.started })
  await expect(page.locator('[data-welcome]')).toBeVisible()
  await expect(page.locator('[data-sidebar-empty]')).toHaveCount(0)
  await expect(page.locator('[data-page-empty]')).toHaveCount(0)
  await page.context().close()
  // At the desktop the line stays the sidebar's: the welcome is the home's, and the Yard's sidebar says the list is empty.
  const desk = await dayPage(browser, { ...DESK, path: '/yard', server: srv.started })
  const line = desk.locator('#dashboard-sidebar-main [data-sidebar-empty]')
  await expect(line).toHaveText('Nothing is running.')
  await expect(desk.locator('#dashboard-sidebar-main')).not.toContainText('No sessions yet.')
  await uncut(line)
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
    // The home opens the session worth opening: its page, or a crew member's place inside its crew (UX3 R72).
    ['/', () => expect(page).toHaveURL(/\/sessions\/[^/?]+$|\/runs\/[^/?]+\?session=[^&]+$/)],
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
    await expect(page.locator('[data-history-day]').first()).toBeVisible({ timeout: 30_000 })
    await expect(page.locator('[data-history-caption]'), `${theme}: no caption in the body`).toHaveCount(0)
    // The popover scales in as it opens, and its paragraph measured while it does runs past a box still scaled: the page's
    // animations run at a tenth of their pace, so that a measure not taken at rest fails every run, not only on a slow one.
    const cdp = await page.context().newCDPSession(page)
    await cdp.send('Animation.enable')
    await cdp.send('Animation.setPlaybackRate', { playbackRate: 0.1 })
    const button = page.locator('[data-page-about-button]')
    await button.click()
    const open = page.locator('[data-page-about-open]')
    await expect(open, theme).toContainText('a sentence repeated is still Conversation')
    await atRest(open)
    await uncut(open.locator('p'))
    await cdp.send('Animation.setPlaybackRate', { playbackRate: 1 })
    await page.keyboard.press('Escape')
    // A popover that has closed is not gone at once: wait for it to be gone before the next step.
    await expect(open).toHaveCount(0)
    await page.context().close()
  }
})


// ---- PR-23: the rest of the pages, the not-found shapes, and About this page.

const CREWS_WHY =
  'A crew is a saved plan: agents, each with a first prompt and a start rule, in one working directory or a git worktree each. A run is one launch of it, here or with conductor up <crew>. The examples show four shapes of crew by start rules and one by tasks, where Yardmaster gives each member its tasks as the results they need come in; they are ordinary crews once loaded.'
const DISPATCH_WHY = "Dispatch keeps a team's work as tickets: what is asked, who owns it, what became of it. A project holds them under its key."
const EVENTS_WHY = 'Nothing yet since this tab opened. An agent reports when it needs input, finishes a turn, hands work off or hits an error.'
const BRAKEMAN_WHY = 'No brief yet. Ask for the first one.'
const AGENTS_WHY = "The catalog has no agent. Add one here, or in the config file's catalog."

test("a server with nothing on it: Crews, Dispatch, Brakeman and History's feed draw the one empty shape, at the desktop and the phone, in both themes", async ({ browser }) => {
  const srv = await empty()
  const pages = [
    ['/crews', { panel: '#dashboard-panel-crews', about: ABOUT.crews, why: CREWS_WHY, action: 'New crew' }],
    ['/dispatch', { panel: '#dashboard-panel-dispatch', about: ABOUT.dispatch, why: DISPATCH_WHY, action: 'New project' }],
    ['/brakeman', { panel: '#dashboard-panel-brakeman', about: ABOUT.brakeman, why: BRAKEMAN_WHY }],
    ['/history?view=feed', { panel: '#dashboard-panel-history', about: ABOUT.feed, why: EVENTS_WHY, action: 'Which agents report' }],
  ] as const
  // The feed's way to the agents that report: Agents' Reporting section.
  {
    const page = await dayPage(browser, { ...DESK, path: '/history?view=feed', server: srv.started })
    await expect(shape(page).locator('[data-page-action]')).toHaveAttribute('href', '/agents#reporting')
    await page.context().close()
  }
  for (const win of [DESK, PHONE]) {
    budget(pages.length * 2 * 12 * STEP_MS)
    for (const [path, o] of pages) {
      const page = await dayPage(browser, { ...win, path, server: srv.started })
      const at = `${path} at ${win.width}`
      await readEmpty(page, `${at}, dark`, { ...o, phone: win === PHONE })
      await useTheme(page, 'light')
      await readEmpty(page, `${at}, light`, { ...o, phone: win === PHONE })
      await page.context().close()
    }
  }
  // Nothing to show: the standing paragraphs stand in place, and no page offers About this page.
  budget(6 * STEP_MS)
  const page = await dayPage(browser, { ...DESK, path: '/brakeman', server: srv.started })
  await expect(shape(page)).toBeVisible()
  await expect(page.locator('[data-brakeman-about]')).toBeVisible()
  await expect(page.locator('[data-page-about-button]')).toHaveCount(0)
  await page.goto('/crews')
  await expect(shape(page)).toBeVisible()
  await expect(page.locator('[data-crews-caption]').first()).toBeVisible()
  await expect(page.locator('[data-page-about-button]')).toHaveCount(0)
})

test('a server with nothing on it: Needs you draws the one empty shape with what shows up there, at the desktop and the phone, in both themes', async ({ browser }) => {
  const srv = await empty()
  for (const win of [DESK, PHONE]) {
    budget(2 * 12 * STEP_MS)
    const page = await dayPage(browser, { ...win, path: '/decisions', server: srv.started })
    const at = `/decisions at ${win.width}`
    const o = { panel: '#dashboard-panel-decisions', about: ABOUT.decisions, why: DECISIONS_WHY, phone: win === PHONE }
    await readEmpty(page, `${at}, dark`, o)
    // What a page of nothing shows besides: the four kinds, and the way to every decision.
    await expect(shape(page).locator('[data-decisions-kind]')).toHaveCount(4)
    await expect(shape(page).locator('[data-decisions-history]')).toBeVisible()
    await useTheme(page, 'light')
    await readEmpty(page, `${at}, light`, o)
    await page.context().close()
  }
})

test("Needs you before its first answer reads, and a first read that fails says the server's words with Read again, then reads into its empty shape", async ({ browser }) => {
  const srv = await empty()
  budget(8 * STEP_MS)
  const page = await dayPage(browser, { ...DESK, path: '/settings', server: srv.started })
  // The list's read held: the reading shape; then answered 500: the failed shape; then let through.
  const held: Route[] = []
  let failing = true
  await page.route(
    (u) => u.pathname === '/api/inbox',
    (r) => {
      if (r.request().method() !== 'GET') return r.fallback()
      if (!held.length) return void held.push(r)
      return failing ? r.fulfill({ status: 500, contentType: 'application/json', body: '{"error":{"code":"internal","message":"the inbox is not readable"}}' }) : r.fallback()
    },
  )
  await page.goto('/decisions')
  const reading = shape(page, 'reading')
  await expect(reading).toBeVisible()
  await expect(reading.locator('[data-page-about]')).toHaveText(ABOUT.decisions)
  await expect.poll(() => held.length).toBeGreaterThan(0)
  await held[0]!.fulfill({ status: 500, contentType: 'application/json', body: '{"error":{"code":"internal","message":"the inbox is not readable"}}' })
  const failed = shape(page, 'failed')
  await expect(failed).toBeVisible()
  await expect(failed.locator('[data-page-why]')).toContainText('the inbox is not readable')
  await expect(page.locator('[data-decisions-empty]')).toHaveCount(0)
  failing = false
  // The page asks again by itself too: Read again is pressed until the empty shape shows.
  await readAgain(failed.locator('[data-page-again]'), shape(page))
  await expect(page.locator('[data-decisions-empty]')).toBeVisible()
})

test('an empty catalog: Agents reads, then says the catalog has no agent, with Add agent', async ({ browser }) => {
  const srv = await empty()
  budget(6 * STEP_MS)
  const page = await dayPage(browser, { ...DESK, path: '/settings', server: srv.started })
  // A server of its own still has the e2e catalog: the page's read of it is answered with none, once the test lets it go.
  const held: Route[] = []
  await page.route('**/api/catalog?*', (r) => (r.request().method() === 'GET' ? void held.push(r) : r.fallback()))
  await page.goto('/agents')
  await expect(shape(page, 'reading')).toBeVisible()
  await expect(shape(page, 'reading').locator('[data-page-about]')).toHaveText(ABOUT.agents)
  await expect.poll(() => held.length).toBeGreaterThan(0)
  for (const r of held.splice(0)) await r.fulfill({ status: 200, contentType: 'application/json', body: '{"agents":[],"hidden":[]}' })
  await readEmpty(page, 'Agents', { panel: '#dashboard-panel-agents', about: ABOUT.agents, why: AGENTS_WHY, action: 'Add agent' })
  await expect(page.locator('[data-agents-about]')).toBeVisible()
  await expect(page.locator('[data-page-about-button]')).toHaveCount(0)
})

test("the first read fails: Dispatch says the server's words with Read again, and reads again into its empty shape", async ({ browser }) => {
  const srv = await empty()
  budget(6 * STEP_MS)
  const page = await dayPage(browser, { ...DESK, path: '/settings', server: srv.started })
  let failing = true
  await page.route('**/api/dispatch/projects*', (r) =>
    failing && r.request().method() === 'GET' ? r.fulfill({ status: 500, contentType: 'application/json', body: '{"error":{"code":"internal","message":"the projects are not readable"}}' }) : r.fallback(),
  )
  await page.goto('/dispatch')
  const failed = shape(page, 'failed')
  await expect(failed).toBeVisible()
  await expect(failed.locator('[data-page-about]')).toHaveText(ABOUT.dispatch)
  await expect(failed.locator('[data-page-why]')).toContainText('the projects are not readable')
  failing = false
  // The page asks again by itself too: Read again is pressed until the empty shape shows.
  await readAgain(failed.locator('[data-page-again]'), shape(page))
  await expect(page.locator('[data-projects-empty]')).toBeVisible()
})

test('not found: a crew, a project, a ticket and a run that are not there draw one shape, with their words and the way back', async ({ browser }) => {
  budget(START_WAIT_MS + 12 * STEP_MS)
  const srv = await ownServer('empty-pages-missing')
  try {
    const key = 'NF'
    await srv.api.ok('POST', '/api/dispatch/projects', { opId: 'empty-pages-nf', key, name: 'Not found' })
    const page = await dayPage(browser, { ...DESK, path: '/settings', server: srv.started })
    for (const [path, words, back, to] of [
      ['/crews/nope', 'No crew has the id nope.', 'All crews', '/crews'],
      ['/dispatch/NOPE', 'No project has the key NOPE.', 'Dispatch', '/dispatch'],
      [`/dispatch/${key}/999`, `No ticket has the key ${key}-999.`, `Back to ${key}`, `/dispatch/${key}`],
      ['/runs/nope', 'No run has the id nope. The server forgets runs when it restarts.', 'Crews', '/crews'],
    ] as const) {
      await page.goto(path)
      const s = shape(page, 'missing')
      await expect(s, path).toBeVisible()
      await expect(page.locator('[data-page-empty]'), `${path}: one shape`).toHaveCount(1)
      await expect(s.locator('[data-page-about]'), `${path}: no first line`).toHaveCount(0)
      await expect(s.locator('[data-page-why]'), path).toHaveText(words)
      const action = s.locator('[data-page-action]')
      await expect(action, path).toHaveText(back)
      await expect(action, path).toHaveAttribute('href', to)
    }
  } finally {
    await srv.stop()
  }
})

test('a project with no ticket: the list, the Board and the Roadmap each say so, with New ticket and its key', async ({ browser }) => {
  budget(START_WAIT_MS + 14 * STEP_MS)
  const srv = await ownServer('empty-pages-project')
  try {
    const key = 'NT'
    await srv.api.ok('POST', '/api/dispatch/projects', { opId: 'empty-pages-nt', key, name: 'No tickets' })
    const page = await dayPage(browser, { ...DESK, path: `/dispatch/${key}`, server: srv.started })
    for (const view of ['list', 'board', 'roadmap'] as const) {
      if (view !== 'list') await page.goto(`/dispatch/${key}?view=${view}`)
      await readEmpty(page, `${key}, ${view}`, { panel: '#dashboard-panel-dispatch-project', why: `${key} has no tickets yet.`, action: 'New ticket', kbd: 'C' })
      await expect(page.locator('[data-list-empty]'), view).toBeVisible()
    }
    // C opens the new ticket's dialog: the layout's root is drawn (the page's keys are the layout's), then the key pressed once.
    await expect(page.locator('#dashboard-sidebar-main')).toBeVisible()
    await page.keyboard.press('c')
    await expect(page.locator('[data-new-ticket-title]')).toBeVisible()
  } finally {
    await srv.stop()
  }
})

/** The page's standing paragraph is not in its body; About this page opens it, Escape closes it, and it is gone before the next step. */
/** Resolves once every finite animation on `l` or an element around it (a popover's scale as it opens) has finished. */
async function opened(l: ReturnType<Page['locator']>) {
  await l.evaluate((el) =>
    Promise.all(
      document
        .getAnimations()
        .filter((a) => {
          const t = (a.effect as KeyframeEffect | null)?.target
          return t instanceof Element && t.contains(el) && a.effect!.getTiming().iterations !== Infinity
        })
        .map((a) => a.finished.catch(() => undefined)),
    ).then(() => undefined),
  )
}

async function aboutFolds(page: Page, at: string, body: string, words: string) {
  await expect(page.locator(body), `${at}: no paragraph in the body`).toHaveCount(0)
  await page.locator('[data-page-about-button]').click()
  const open = page.locator('[data-page-about-open]')
  await expect(open, at).toContainText(words)
  // The popover opens with a short scale: measured inside it, its text's lines (each the line's height) run past a box drawn smaller.
  // The measure waits for the popover's own animations to end.
  await opened(open)
  await uncut(open.locator('p').first())
  await page.keyboard.press('Escape')
  // A popover that has closed is not gone at once.
  await expect(open, `${at}: closed`).toHaveCount(0)
}

test('About this page: Agents and Crews fold their paragraphs once they have content, in both themes', async ({ day, browser }) => {
  await day.part('runs')
  budget(2 * 2 * 10 * STEP_MS)
  for (const theme of ['dark', 'light'] as const) {
    const page = await dayPage(browser, { ...DESK, theme, path: '/agents' })
    await expect(page.locator('[data-agent]').first()).toBeVisible()
    await aboutFolds(page, `Agents, ${theme}`, '[data-agents-about]', 'Commands are argv arrays; nothing goes through a shell.')
    // The theme switched as the app keeps it, and the check made again after the redraw.
    await useTheme(page, theme === 'dark' ? 'light' : 'dark')
    await expect(page.locator('[data-agent]').first()).toBeVisible()
    await aboutFolds(page, `Agents, switched from ${theme}`, '[data-agents-about]', 'Commands are argv arrays; nothing goes through a shell.')
    await page.goto('/crews')
    await expect(page.locator('[data-crews-table], [data-running-now]').first()).toBeVisible()
    await aboutFolds(page, `Crews, ${theme}`, '[data-crews-caption]', 'Nothing runs until you launch one.')
    await page.context().close()
  }
})

/**
 * Brakeman's header on a phone with a brief done (About this page drawn beside Settings): the title whole, Settings still named and
 * a link to Settings, nothing in the header over anything else or past the window, and the day's budget line read in the body, in
 * the role's card.
 */
async function brakemanPhoneHeader(browser: Browser, srv: OwnServer, theme: 'dark' | 'light') {
  const page = await dayPage(browser, { ...PHONE, theme, path: '/brakeman', server: srv.started })
  const header = page.locator('[data-brakeman-header]')
  const at = `Brakeman at 390, ${theme}`
  await expect(header.locator('[data-page-about-button]'), `${at}: About this page in the header`).toBeVisible()
  const settings = header.getByRole('link', { name: 'Settings' })
  await expect(settings, `${at}: Settings in the header`).toBeVisible()
  await expect(settings).toHaveAttribute('href', '/settings')
  await uncut(header.locator('[data-slot="title"]'))
  await expect(header.locator('[data-slot="title"]'), `${at}: the title`).toHaveText('Brakeman')
  await noOverlap(header)
  await noSideways(page)
  const budgetLine = page.locator('[data-brakeman-page] [data-brakeman-role] [data-turns-today]')
  await expect(budgetLine, `${at}: the budget line in the role's card`).toContainText('turns,')
  // Below the latest brief on a phone: brought into view to be measured.
  await budgetLine.scrollIntoViewIfNeeded()
  await uncut(budgetLine)
  await page.context().close()
}

test("About this page: Brakeman folds its paragraph once a request is done, and keeps Settings in its header, whole on a phone", async ({ browser }) => {
  budget(START_WAIT_MS + 60_000 + 20 * STEP_MS)
  const srv = await ownServer('empty-pages-brakeman')
  try {
    const { config: c } = await srv.api.ok<{ config: Record<string, unknown> & { rev: number } }>('GET', '/api/brakeman')
    await srv.api.ok('PUT', '/api/brakeman/config', { rev: c.rev, config: { ...c, enabled: true, agentId: 'brakeman-stub' } })
    const asked = await srv.api.ok<{ request: { id: string } }>('POST', '/api/brakeman/requests', { opId: 'empty-pages-bk', kind: 'overlaps' })
    // The turn's end: the stub writes its brief and the request is briefed (a state that holds).
    await expect
      .poll(async () => (await srv.api.ok<{ request: { state: string } }>('GET', `/api/brakeman/requests/${asked.request.id}`)).request.state, { timeout: 60_000 })
      .toBe('briefed')
    for (const theme of ['dark', 'light'] as const) {
      const page = await dayPage(browser, { ...DESK, theme, path: '/brakeman', server: srv.started })
      await expect(page.locator(`[data-brakeman-briefs] [data-request="${asked.request.id}"]`)).toBeVisible()
      await expect(page.locator('[data-brakeman-settings-link]')).toHaveAttribute('href', '/settings')
      await aboutFolds(page, `Brakeman, ${theme}`, '[data-brakeman-about]', 'nothing it writes becomes work until you act')
      await page.context().close()
      await brakemanPhoneHeader(browser, srv, theme)
    }
  } finally {
    await srv.stop()
  }
})
