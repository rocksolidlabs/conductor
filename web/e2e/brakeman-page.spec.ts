import type { Browser, Page } from '@playwright/test'
import { brakemanServer, type BrakemanServer } from './brakeman-server'
import { filledPrimaries, walk, worst } from './contrast'
import { atRest, budget, dayPage, expect, test, useTheme, type Run } from './fixtures'
import { inside, noOverlap, noSideways, tapTarget } from './layout'

// The Brakeman page as the Brakeman design review proposes it (issue 993), on a server of its own with the Brakeman stub (the seeded
// day holds no Brakeman records), its gate shut unless a test opens it: a turn under way shows the reads it has used, read through
// the API first (a starting value is not a result), Watch leads to its session and Stop cancels it; the latest brief is the newest kept
// as the page is arrived at, and a newer one is offered by a line that swaps nothing; Earlier lists every request done, says what each
// brief found, filters on the server and searches by a word, and offers Ask again where a turn ended with no brief; Now's need-you
// count is the sidebar's, and its line names a run that ended since the last briefing; the page lays out wide and on a phone, in both
// themes, with one filled button a card; and on an older server, which takes no filter and has no since route, the filters hide.
test.describe.configure({ mode: 'serial' })

/** The cap of every wait a step makes without a bound of its own. */
const STEP_MS = 15_000
/** A turn's start (its session launched, its agent reading its request) and its brief kept once the gate opens. */
const TURN_MS = 60_000
test.use({ actionTimeout: STEP_MS, navigationTimeout: STEP_MS })

const KEY = 'BKP'
const DESK = { width: 1440, height: 900 }
const PHONE = { width: 390, height: 844, touch: true }

let srv: BrakemanServer
/** Sessions and runs this spec started, ended at its end. */
const sessions: string[] = []
const runs: string[] = []

/** The brief the stub submits for the drafts request: a fact and a draft ticket. */
const draftBrief = {
  schema: 'brakeman.brief/1',
  title: 'Two tickets from the discussion',
  summary: 'The discussion asks for one ticket to fix the check.',
  items: [
    { kind: 'fact/1', text: 'The ticket asks for the check to be fixed.', evidence: [`ticket:${KEY}-1`] },
    { kind: 'draft_ticket/1', project: KEY, title: 'Fix the check', priority: 2 },
  ],
}

/** A brief taller than a phone's window: its reading is a dozen paragraphs. */
const longBrief = {
  schema: 'brakeman.brief/1',
  title: 'A long reading of the check',
  summary: Array.from({ length: 12 }, (_, i) => `Paragraph ${i + 1} of the reading: the check failed on the same test, and the test is flaky at the base commit too.`).join('\n\n'),
  items: [{ kind: 'fact/1', text: 'The check failed.', evidence: [`ticket:${KEY}-1`] }],
}

test.beforeAll(async () => {
  test.setTimeout(240_000)
  srv = await brakemanServer({ name: 'brakeman-page' })
  await srv.api.ok('POST', '/api/dispatch/projects', { opId: 'bkp-project', key: KEY, name: 'Brakeman page' })
  await srv.api.ok('POST', '/api/dispatch/tickets', { opId: 'bkp-ticket', project: KEY, title: 'The check never passes' })
})

test.afterAll(async () => {
  if (!srv) return
  for (const id of sessions) await srv.api.stopSession(id).catch(() => undefined)
  for (const id of runs) await srv.api.stopRun(id).catch(() => undefined)
  await srv.stop()
})

/** A page in a context of its own on this spec's server (dayPage's), at `path`. */
const pageAt = (browser: Browser, path: string, o: { theme?: 'dark' | 'light'; phone?: boolean } = {}) =>
  dayPage(browser, { ...(o.phone ? PHONE : DESK), theme: o.theme, path, server: srv.started })

/** The request's reads as the server counts them now. */
const reads = async (id: string) => (await srv.request(id)).reads ?? 0

test('a turn under way shows the reads it has used, Watch leads to its session, and Stop cancels it', async ({ browser }) => {
  budget(TURN_MS + 8 * STEP_MS)
  srv.shutGate()
  const r = await srv.ask({ kind: 'overlaps', by: 'Nate' })
  const running = await srv.until(r.id, ['running'], TURN_MS)
  // The stub's counted read (what changed) comes before its gate: the server's count is the page's to show.
  await expect.poll(() => reads(r.id), { timeout: STEP_MS }).toBeGreaterThanOrEqual(1)
  const used = await reads(r.id)
  const page = await pageAt(browser, '/brakeman')
  const current = page.locator('[data-brakeman-current]')
  await expect(current.locator(`[data-request="${r.id}"]`)).toHaveAttribute('data-request-state', 'running')
  await expect(current.locator('[data-progress]')).toHaveAttribute('data-reads-used', String(used))
  await expect(current.locator('[data-progress]')).toHaveAttribute('data-reads-of', '300')
  await expect(current.locator('[data-progress-reading]')).toContainText('changed')
  await expect(current.locator('[data-request-title]')).toHaveText('Overlapping changes')
  await expect(current.locator('[data-request-words]')).toContainText('Asked by Nate')
  // Watch leads to its session's terminal.
  await expect(current.locator('[data-request-session]')).toHaveAttribute('href', `/sessions/${running.sessionId}`)
  // Stop cancels it: the state that holds, read through the API.
  await current.locator('[data-request-stop="cancel"]').click()
  await srv.until(r.id, ['cancelled'], STEP_MS)
  await expect(page.locator(`[data-brakeman-briefs] [data-request="${r.id}"] [data-request-words]`)).toHaveText('cancelled: a person cancelled it')
  await expect(page.locator(`[data-brakeman-briefs] [data-request="${r.id}"] [data-request-again]`)).toHaveCount(0)
})

/** Ids the spec's requests got, by what they are. */
const ids = { briefing: '', drafts: '', ended: '', again: '' }

test('Earlier says what each brief found, filters on the server and searches, the latest card keeps its brief, and Ask again asks once more', async ({ browser }) => {
  budget(5 * TURN_MS + 40 * STEP_MS)
  srv.openGate()
  ids.briefing = (await srv.ask({ kind: 'briefing' })).id
  await srv.until(ids.briefing, ['briefed'], TURN_MS)
  srv.setBrief(draftBrief)
  ids.drafts = (await srv.ask({ kind: 'draft_tickets', subjects: [`ticket:${KEY}-1`] })).id
  await srv.until(ids.drafts, ['briefed'], TURN_MS)
  srv.setBrief(null)
  // A turn whose session ends before it briefs: it is closed with no brief.
  srv.shutGate()
  const ended = await srv.ask({ kind: 'explain', subjects: [`ticket:${KEY}-1`] })
  ids.ended = ended.id
  const turn = await srv.until(ended.id, ['running'], TURN_MS)
  await srv.api.stopSession(turn.sessionId!)
  const closed = await srv.until(ended.id, ['ended', 'failed', 'interrupted'], TURN_MS)
  expect(closed.hasBrief).toBeFalsy()

  const page = await pageAt(browser, '/brakeman')
  const latest = page.locator('[data-brakeman-latest]')
  const earlier = page.locator('[data-brakeman-briefs]')
  // The latest card holds the newest brief, and Earlier every request done, the card's among them.
  await expect(latest).toHaveAttribute('data-request', ids.drafts)
  await expect(latest.locator(`[data-brief="${ids.drafts}"] [data-brief-title]`)).toHaveText(draftBrief.title)
  await expect(earlier.locator(`[data-request="${ids.drafts}"]`)).toHaveAttribute('data-request-latest', '')
  const rows = earlier.locator('[data-request]')
  const shown = () => rows.evaluateAll((rs) => rs.map((r) => r.getAttribute('data-request')))
  await expect.poll(shown).toEqual([ids.ended, ids.drafts, ids.briefing, 'br-1'])
  // A row says what its brief found without being opened.
  await expect(earlier.locator(`[data-request="${ids.drafts}"] [data-request-title]`)).toHaveText(draftBrief.title)
  await expect(earlier.locator(`[data-request="${ids.drafts}"] [data-request-meta]`)).toHaveText(`Draft tickets · ${KEY}-1 · 1 draft · 1 fact · no decision`)
  await expect(earlier.locator(`[data-request="${ids.briefing}"] [data-request-meta]`)).toContainText('Brief me since the last briefing')
  await expect(earlier.locator(`[data-request="${ids.ended}"] [data-request-title]`)).toHaveText(`Explain this · ${KEY}-1`)
  await expect(earlier.locator(`[data-request="${ids.ended}"] [data-request-meta]`)).toContainText('no brief: ')

  // The filters, one pressed at a time, each the server's.
  const filter = (k: string) => earlier.locator(`[data-briefs-filter="${k}"]`)
  await expect(filter('all')).toHaveAttribute('aria-pressed', 'true')
  await filter('drafts').click()
  await expect(filter('drafts')).toHaveAttribute('aria-pressed', 'true')
  await expect(filter('all')).toHaveAttribute('aria-pressed', 'false')
  await expect.poll(shown).toEqual([ids.drafts])
  await filter('briefings').click()
  await expect.poll(shown).toEqual([ids.briefing])
  await filter('decision').click()
  await expect(earlier.locator('[data-briefs-none]')).toContainText('No brief matches.')
  await expect(rows).toHaveCount(0)
  await earlier.locator('[data-briefs-none] button', { hasText: 'Clear' }).click()
  await expect(filter('all')).toHaveAttribute('aria-pressed', 'true')
  await expect.poll(shown).toEqual([ids.ended, ids.drafts, ids.briefing, 'br-1'])
  expect(await earlier.getAttribute('data-briefs-filtered')).toBe('server')
  // A word searches the titles.
  await earlier.getByRole('textbox', { name: 'Search briefs' }).fill('tickets from the discussion')
  await expect.poll(shown).toEqual([ids.drafts])
  await earlier.getByRole('textbox', { name: 'Search briefs' }).fill('')
  await expect.poll(shown).toEqual([ids.ended, ids.drafts, ids.briefing, 'br-1'])
  // A redraw the test causes (the theme switched, the page read again): the server's filters checked again.
  await useTheme(page, 'light')
  await expect(filter('all')).toHaveAttribute('aria-pressed', 'true')
  await expect.poll(shown).toEqual([ids.ended, ids.drafts, ids.briefing, 'br-1'])
  await filter('drafts').click()
  await expect(filter('drafts')).toHaveAttribute('aria-pressed', 'true')
  await expect.poll(shown).toEqual([ids.drafts])
  await filter('all').click()
  await expect.poll(shown).toEqual([ids.ended, ids.drafts, ids.briefing, 'br-1'])

  // Ask again asks the same once more, under a new id: it runs (the gate is shut), and the row no longer offers it.
  await earlier.locator(`[data-request="${ids.ended}"] [data-request-again]`).click()
  const again = page.locator('[data-brakeman-current] [data-request]')
  await expect(again).toHaveAttribute('data-request-state', /^(starting|running)$/, { timeout: TURN_MS })
  ids.again = (await again.getAttribute('data-request'))!
  expect(ids.again).not.toBe(ids.ended)
  expect(await srv.request(ids.again)).toMatchObject({ kind: 'explain', subjects: [`ticket:${KEY}-1`] })
  await expect(earlier.locator(`[data-request="${ids.ended}"] [data-request-again]`)).toHaveCount(0)
  // A turn under way is In progress's: a search the server matches by its id lists it nowhere in Earlier.
  await earlier.getByRole('textbox', { name: 'Search briefs' }).fill(ids.again)
  await expect(earlier.locator('[data-briefs-none]')).toBeVisible()
  await expect(earlier.locator(`[data-request="${ids.again}"]`)).toHaveCount(0)
  await earlier.getByRole('textbox', { name: 'Search briefs' }).fill('')
  await expect.poll(shown).toEqual([ids.ended, ids.drafts, ids.briefing, 'br-1'])

  // Its brief (a long one), kept while the page shows another, is offered by a line and swaps nothing.
  srv.setBrief(longBrief)
  srv.openGate()
  await srv.until(ids.again, ['briefed'], TURN_MS)
  srv.setBrief(null)
  await expect(latest.locator('[data-brief-newer]')).toContainText(`A newer brief: ${longBrief.title}`)
  await expect(latest).toHaveAttribute('data-request', ids.drafts)
  await latest.locator('[data-brief-newer-show]').click()
  await expect(latest).toHaveAttribute('data-request', ids.again)
  await expect(latest.locator(`[data-brief="${ids.again}"] [data-brief-title]`)).toHaveText(longBrief.title)
  await expect(latest.locator('[data-brief-newer]')).toHaveCount(0)

  // A row's link shows its brief in the card, with Back to the latest; the row of the brief shown folds the card.
  await earlier.locator(`[data-request="${ids.briefing}"] [data-request-open]`).click()
  await expect(latest).toHaveAttribute('data-request', ids.briefing)
  await expect(latest.locator(`[data-brief="${ids.briefing}"] [data-brief-title]`)).toHaveText(`Stub brief for ${ids.briefing}`)
  await expect(page).toHaveURL(new RegExp(`request=${ids.briefing}`))
  await latest.locator('[data-brief-back]').click()
  await expect(latest).toHaveAttribute('data-request', ids.again)
  await expect(page.locator('[data-brief]')).toHaveCount(1)
})

test('on a phone, a row of Earlier pressed below the card brings the card into view, and the focus stays on the row', async ({ browser }) => {
  budget(10 * STEP_MS)
  const page = await pageAt(browser, '/brakeman', { phone: true })
  const card = page.locator('[data-brakeman-latest]')
  await expect(card.locator(`[data-brief="${ids.again}"] [data-brief-title]`)).toHaveText(longBrief.title)
  // Scrolled down to Earlier, past the long brief: the card is out of view.
  const row = page.locator(`[data-brakeman-briefs] [data-request="${ids.briefing}"] [data-request-open]`)
  await page.locator('[data-brakeman-notes]').scrollIntoViewIfNeeded()
  await expect(card.locator('[data-brief-title]')).not.toBeInViewport()
  await row.click()
  await expect(card).toHaveAttribute('data-request', ids.briefing)
  await expect(card.locator(`[data-brief="${ids.briefing}"] [data-brief-title]`)).toBeInViewport()
  expect(await row.evaluate((el) => document.activeElement === el)).toBe(true)
})

test("Now's need-you count is the sidebar's, and its line names a run that ended since the last briefing", async ({ browser }) => {
  budget(TURN_MS + 20 * STEP_MS)
  const asker = await srv.api.ok<{ id: string }>('POST', '/api/sessions', { agentId: 'asker', name: 'bkp asks' })
  sessions.push(asker.id)
  // A run that ends after the last briefing (the spec's first briefing request).
  const crew = await srv.api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
    name: 'bkp ended',
    goal: 'end',
    cwd: '',
    where: 'server',
    isolation: 'none',
    openAfterLaunch: false,
    yolo: null,
    members: [{ name: 'lead', agentId: 'worker-cli', prompt: '', start: { when: 'immediately' } }],
  })
  const run = await srv.api.launchCrew(crew.crew.id)
  runs.push(run.id)
  await srv.api.stopRun(run.id)
  await expect.poll(async () => (await srv.api.ok<{ run: Run }>('GET', `/api/runs/${run.id}`)).run.state, { timeout: TURN_MS }).toBe('stopped')

  for (const theme of ['dark', 'light'] as const) {
    const page = await pageAt(browser, '/brakeman', { theme })
    await nowAgrees(page)
    // The theme switched as the app keeps it, and the check made again after the redraw.
    await useTheme(page, theme === 'dark' ? 'light' : 'dark')
    await nowAgrees(page)
    await page.context().close()
  }
})

/** Now's need-you tile counts what the sidebar counts, read at one moment, and its line names the run that ended. */
async function nowAgrees(page: Page) {
  const now = page.locator('[data-brakeman-now]')
  const tile = now.locator('[data-now-tile="needs"]')
  const side = page.locator('[data-sidebar-section="needs"] [data-section-count]')
  await expect
    .poll(() => page.evaluate(() => [document.querySelector('[data-brakeman-now] [data-now-tile="needs"]')?.getAttribute('data-now-count'), document.querySelector('[data-sidebar-section="needs"] [data-section-count]')?.textContent?.trim()]), { timeout: STEP_MS })
    .toEqual(['1', '1'])
  await expect(tile).toContainText('1 need you')
  await expect(tile).toContainText('a question in bkp asks')
  await expect(side).toBeVisible()
  await expect(now.locator('[data-now-since]')).toHaveAttribute('data-since-from', 'briefing', { timeout: STEP_MS })
  await expect(now.locator('[data-now-since]')).toContainText('Since the last briefing, at ')
  await expect(now.locator('[data-now-since]')).toContainText('bkp ended', { timeout: 30_000 })
}

test('the page lays out wide with its side column beside the main one, and on a phone in one column with the bar at its foot, in both themes', async ({ browser }) => {
  budget(2 * 30 * STEP_MS)
  for (const theme of ['dark', 'light'] as const) {
    // Wide: the side column beside the main one, nothing over anything else.
    const wide = await pageAt(browser, '/brakeman', { theme })
    const main = wide.locator('[data-brakeman-main]')
    const side = wide.locator('[data-brakeman-side]')
    await expect(wide.locator('[data-brakeman-latest] [data-brief]')).toBeVisible()
    await expect(wide.locator('[data-brakeman-ask][data-ask-size="full"]')).toBeVisible()
    await inside(main.locator('[data-brakeman-latest]'), main)
    await inside(side.locator('[data-brakeman-role]'), side)
    const [m, s] = await Promise.all([main.boundingBox(), side.boundingBox()])
    expect(s!.x, `${theme}: the side column is right of the main one`).toBeGreaterThanOrEqual(m!.x + m!.width - 1)
    expect(s!.y, `${theme}: the side column starts beside the main one`).toBeLessThan(m!.y + 40)
    await noOverlap(wide.locator('[data-brakeman-header]'))
    await noSideways(wide)
    for (const card of ['[data-brakeman-now]', '[data-brakeman-latest]', '[data-brakeman-briefs]', '[data-brakeman-role]', '[data-brakeman-notes]']) {
      expect((await filledPrimaries(wide.locator(card))).length, `${theme}: ${card} has at most one filled button`).toBeLessThanOrEqual(1)
    }
    const atRestWalk = await walk(wide, wide.locator('[data-brakeman-page]'))
    expect(atRestWalk.judged).toBeGreaterThan(20)
    expect(atRestWalk.failures, `${theme} at rest:\n${worst(atRestWalk.failures)}`).toEqual([])
    // Hovered: a row of Earlier and a tile of Now.
    for (const hovered of [wide.locator('[data-brakeman-briefs] [data-request-open]').first(), wide.locator('[data-now-tile="needs"]')]) {
      await hovered.hover()
      await atRest(hovered)
      const w = await walk(wide, wide.locator('[data-brakeman-page]'))
      expect(w.failures, `${theme} hovered:\n${worst(w.failures)}`).toEqual([])
    }
    // Activity opens the wakes and notes.
    await wide.locator('[data-brakeman-notes] [data-notes-open]').click()
    const sheet = wide.locator('[data-role-sheet]')
    await expect(sheet.locator('[data-role-notes]')).toBeVisible()
    await atRest(sheet)
    const sw = await walk(wide, sheet)
    expect(sw.failures, `${theme} the sheet:\n${worst(sw.failures)}`).toEqual([])
    await wide.keyboard.press('Escape')
    await expect(sheet).toHaveCount(0)
    await wide.context().close()

    // A phone: the counts, the latest brief, then the rest in one column, the bar at the foot above the bottom bar.
    const phone = await pageAt(browser, '/brakeman', { theme, phone: true })
    await expect(phone.locator('[data-brakeman-latest] [data-brief]')).toBeVisible()
    const order = await phone.evaluate(() =>
      ['[data-brakeman-now]', '[data-brakeman-latest]', '[data-brakeman-briefs]', '[data-brakeman-role]', '[data-brakeman-notes]'].map((s) => document.querySelector(s)?.getBoundingClientRect().top ?? -1),
    )
    expect(order.every((t, i) => t >= 0 && (i === 0 || t > order[i - 1]!)), `${theme}: the phone's order, top to bottom: ${order.join(', ')}`).toBe(true)
    const foot = phone.locator('[data-brakeman-foot]')
    await expect(foot.locator('[data-brakeman-ask][data-ask-size="foot"]')).toBeVisible()
    await expect(foot.locator('[data-ask-foot]')).toHaveCount(1)
    const [f, bar] = await Promise.all([foot.boundingBox(), phone.locator('[data-bottom-bar]').boundingBox()])
    expect(f!.y + f!.height, `${theme}: the foot bar above the bottom bar`).toBeLessThanOrEqual(bar!.y + 1)
    await expect(phone.locator('[data-brakeman-header] [data-page-about-button]')).toBeVisible()
    await noOverlap(phone.locator('[data-brakeman-header]'))
    await noSideways(phone)
    await tapTarget(phone.locator('[data-brakeman-header] [data-brakeman-settings-link], [data-now-tile], [data-brakeman-briefs] [data-briefs-filter], [data-brakeman-briefs] [data-request-open], [data-role-activity], [data-notes-open]'))
    const pw = await walk(phone, phone.locator('[data-brakeman-page]'))
    expect(pw.failures, `${theme} phone:\n${worst(pw.failures)}`).toEqual([])
    await phone.context().close()
  }
})

test('on an older server, which takes no filter and has no since or opened route, the filters hide and the search reads the pages read', async ({ browser }) => {
  budget(10 * STEP_MS)
  const page = await pageAt(browser, '/yard')
  // Routes set before the page loads the Brakeman page: the list's filters refused as an older server refuses them, no since route.
  await page.route('**/api/brakeman/requests?*', async (route) => {
    const q = new URL(route.request().url()).searchParams
    if ([...q.keys()].some((k) => k !== 'after' && k !== 'limit')) {
      return route.fulfill({ status: 400, json: { error: { code: 'invalid_request', message: `this route takes no parameter "${[...q.keys()].find((k) => k !== 'after' && k !== 'limit')}"` } } })
    }
    return route.fallback()
  })
  await page.route('**/api/brakeman/since*', (route) => route.fulfill({ status: 404, json: { error: { code: 'not_found', message: 'no such route' } } }))
  await page.route('**/api/brakeman/opened', (route) => route.fulfill({ status: 404, json: { error: { code: 'not_found', message: 'no such route' } } }))
  await page.goto('/brakeman')
  const earlier = page.locator('[data-brakeman-briefs]')
  await expect(earlier).toHaveAttribute('data-briefs-filtered', 'local')
  await expect(earlier.locator('[data-briefs-filter]')).toHaveCount(0)
  // The latest brief is the newest the page holds.
  await expect(page.locator('[data-brakeman-latest]')).toHaveAttribute('data-request', ids.again)
  await earlier.getByRole('textbox', { name: 'Search briefs' }).fill('tickets from the discussion')
  await expect(earlier.locator('[data-briefs-local]')).toHaveText('Only the pages read are searched.')
  await expect.poll(() => earlier.locator('[data-request]').evaluateAll((rs) => rs.map((r) => r.getAttribute('data-request')))).toEqual([ids.drafts])
  await expect(page.locator('[data-now-since]')).toHaveCount(0)
  await expect(page.locator('[data-now-tile="needs"]')).toBeVisible()
})
