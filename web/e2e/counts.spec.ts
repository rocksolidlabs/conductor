import type { BrowserContext, Page, Route } from '@playwright/test'
import { budget, dayPage, expect, itemURL, pagePath, pressChord, test, type Session } from './fixtures'
import { inboxItem, summary } from './inbox-stub'
import { tapTarget } from './layout'
import type { Day } from './day'
import { DAY_STEP_MS } from './day'
import { ownServer, type OwnServer } from './own-server'

// One number of what needs you, the same on every surface that shows it (the UX review's P2, X-counts; utils/needsYou.ts). The
// oracle is the server: the refs GET /api/inbox lists, held first to be exactly the refs the day's parts made, and GET
// /api/sessions read at the same moment. Every expected number is computed from them, never written down. The readers are read at
// one moment, as one tuple, and polled until that tuple equals what the oracle says at the same poll: read one by one while the
// inbox moves, they would give a picture neither order would.

type Item = { ref: string; kind: string; tracking?: boolean }

/** What the server says, read at one moment: the one number and its parts, and the asking sessions on the Yard's grid. */
async function oracle(day: Day) {
  const [items, sessions] = await Promise.all([day.inbox() as Promise<Item[]>, day.listSessions()])
  const open = items.filter((it) => !it.tracking)
  const plansTasks = open.filter((it) => it.kind === 'plan' || it.kind === 'task').length
  const grid = sessions.filter((s) => (s.status === 'running' || s.status === 'starting') && s.attention?.state === 'needs_input' && !(s.attention as { answered?: string }).answered).length
  const n = open.length
  return {
    n,
    want: {
      nav: n ? String(n) : '',
      title: n ? String(n) : '',
      favicon: n > 0,
      palette: n ? `Needs you · ${n} waiting` : 'Needs you',
      railCount: n ? String(n) : '',
      railLabel: n ? `Needs you, ${n} waiting` : 'Needs you',
      phone: n ? String(n) : '',
      phoneYard: grid ? String(grid) : '',
      yardChip: String(grid),
      // The sidebar's section lists every kind (UX1 PR-13): it counts the one number, and says nothing is left out.
      section: n ? String(n) : '',
      sectionMore: 0,
      yardNavBadge: 0,
      railYardChip: 0,
    },
    pill: `1 of ${n - plansTasks}${plansTasks ? ` · ${plansTasks} more in Needs you` : ''}`,
  }
}

const text = async (page: Page, sel: string) => ((await page.locator(sel).count()) ? ((await page.locator(sel).first().textContent()) ?? '').trim() : '')

/** Every reader, at one moment. */
async function readAll(desk: Page, rail: Page, phone: Page) {
  const [nav, title, favicon, palette, section, sectionMore, yardNavBadge] = await Promise.all([
    text(desk, '#dashboard-sidebar-main nav [data-count-of="needs"]'),
    desk.title().then((t) => /^\((\d+\+?)\) /.exec(t)?.[1] ?? ''),
    desk.evaluate(() => document.querySelector<HTMLLinkElement>('link[rel="icon"]')?.href.startsWith('data:') ?? false),
    text(desk, '[data-palette-row="go:decisions"] [data-palette-label]'),
    text(desk, '[data-sidebar-section="needs"] [data-section-count]'),
    desk.locator('[data-sidebar-section="needs"] [data-section-more]').count(),
    desk.locator('#dashboard-sidebar-main [data-sidebar-tile="yard"] [data-tile-badge]').count(),
  ])
  const [railCount, railLabel, railYardChip, yardChip] = await Promise.all([
    text(rail, '[data-rail-count="needs"][data-count-of="needs"]'),
    rail.locator('#dashboard-sidebar-main nav a[data-count-of="needs"]').getAttribute('aria-label').catch(() => ''),
    rail.locator('#dashboard-sidebar-main nav a[href="/yard"] [data-slot="linkLeadingChip"]').count(),
    text(rail, '[data-yard-filter="needs"] [data-yard-count]'),
  ])
  const [phoneN, phoneYard] = await Promise.all([text(phone, '[data-bottom-tab="decide"] [data-count-of="needs"]'), text(phone, '[data-bottom-tab="yard"] [data-bottom-count]')])
  return { nav, title, favicon, palette, railCount, railLabel: railLabel ?? '', phone: phoneN, phoneYard, yardChip, section, sectionMore, yardNavBadge, railYardChip }
}

/** Polls until every reader agrees with the oracle read at the same poll; returns that oracle. */
async function agree(day: Day, desk: Page, rail: Page, phone: Page, message: string) {
  let last: Awaited<ReturnType<typeof oracle>> | undefined
  budget(DAY_STEP_MS)
  await expect
    .poll(
      async () => {
        const [o, seen] = await Promise.all([oracle(day), readAll(desk, rail, phone)])
        last = o
        return JSON.stringify(seen) === JSON.stringify(o.want) ? 'agree' : JSON.stringify({ want: o.want, seen })
      },
      { timeout: DAY_STEP_MS, message },
    )
    .toBe('agree')
  return last!
}

test('one number of what needs you on every surface that shows it, on the busy day', async ({ day, browser }) => {
  test.setTimeout(test.info().timeout + 4 * DAY_STEP_MS)
  await day.part('busy')
  // The oracle holds: the inbox lists exactly what the parts made.
  const refs = (await day.inbox()).map((it) => it.ref).sort()
  expect(refs).toEqual([...day.refs.keys()].sort())

  const desk = await dayPage(browser, { path: '/crews' })
  // The palette open on the desktop page, its Go to row found by the page's name, kept open: its rows follow the stores.
  await desk.keyboard.press('Alt+/')
  await expect(desk.locator('[data-palette]')).toBeVisible()
  await desk.locator('input[data-palette-input]').fill('Needs you')
  await expect(desk.locator('[data-palette-row="go:decisions"]')).toBeVisible()
  // The rail: the sidebar folded by its key on the Yard, where the chip counts its grid.
  const rail = await dayPage(browser, { path: '/yard' })
  await rail.keyboard.press('Alt+b')
  await expect(rail.locator('[data-rail]')).toBeVisible()
  const phone = await dayPage(browser, { width: 390, height: 844, touch: true, path: '/sessions' })

  const before = await agree(day, desk, rail, phone, 'every reader shows the one number')
  expect(before.n).toBeGreaterThan(0)

  // The pill counts what it can answer and says how many more wait.
  await rail.addInitScript((ms) => ((window as Window & { conductorPillMs?: number }).conductorPillMs = ms), test.info().timeout)
  await rail.reload()
  await expect(rail.locator('[data-rail]')).toBeVisible()
  // The pill counts what the stores hold when it is pressed: wait for the reloaded page to know the one number (the rail's count is
  // drawn only once every store is read), never for the clock.
  await expect(rail.locator('[data-rail-count="needs"]')).toHaveText(before.want.railCount)
  await rail.keyboard.press(']')
  await expect(rail.locator('[data-pill-count]')).toHaveText(before.pill)
  await rail.keyboard.press('Escape')

  // The test's own asker: every reader reads one more at the same poll.
  const mine = await day.api.ok<Session>('POST', '/api/sessions', { agentId: 'asker', name: 'e2e counts asker' })
  try {
    budget(DAY_STEP_MS)
    await expect.poll(async () => (await day.api.session(mine.id)).attention?.state, { timeout: DAY_STEP_MS }).toBe('needs_input')
    const asked = await agree(day, desk, rail, phone, 'every reader shows the asker')
    expect(asked.n).toBe(before.n + 1)
    // Its question cleared (the session stopped): one fewer, everywhere.
    await day.api.stopSession(mine.id)
    const cleared = await agree(day, desk, rail, phone, 'every reader drops the asker')
    expect(cleared.n).toBe(before.n)
    // After a reload of each page, the same. The sidebar's mode is the person's, kept by the server for every browser (OS-2): the
    // rail folded on the Yard is the desk's too once it has read the server's layout, so the desk opens its sidebar again, as a person
    // reading the full sidebar there would.
    for (const p of [desk, rail, phone]) await p.reload()
    await expect(desk.locator('[data-ui-state]').first()).toHaveAttribute('data-ui-state', 'read')
    await expect(desk.locator('[data-rail]')).toBeVisible()
    await desk.keyboard.press('Alt+b')
    await expect(desk.locator('[data-rail]')).toBeHidden()
    await expect(desk.locator('#dashboard-sidebar-main')).toBeVisible()
    await desk.keyboard.press('Alt+/')
    await desk.locator('input[data-palette-input]').fill('Needs you')
    const again = await agree(day, desk, rail, phone, 'every reader after a reload')
    expect(again.n).toBe(before.n)
  } finally {
    await day.api.stopSession(mine.id)
    await expect.poll(() => day.statusOf(mine.id), { timeout: DAY_STEP_MS }).toMatch(/^(exited|stopped|gone)$/)
    await day.api.stopSession(mine.id)
    await expect.poll(() => day.statusOf(mine.id), { timeout: DAY_STEP_MS }).toBe('gone')
  }
})

/** Where the sidebar lists each open item of the inbox: a question (or a decision its session's question mirrors) by its session's row, any other by its own ref and kind. */
async function listedAs(day: Day): Promise<Array<{ ref: string; sel: string }>> {
  const [items, sessions] = await Promise.all([day.inbox() as Promise<Item[]>, day.listSessions()])
  return items
    .filter((it) => !it.tracking)
    .map((it) => {
      if (it.kind === 'question') return { ref: it.ref, sel: `[data-sidebar-row="s:${it.ref.slice('question:'.length).split('/')[0]}"]` }
      const mirror = it.kind === 'decision' ? sessions.find((x) => x.attention?.state === 'needs_input' && x.attention.decision === it.ref.slice('decision:'.length)) : undefined
      if (mirror) return { ref: it.ref, sel: `[data-sidebar-row="s:${mirror.id}"]` }
      return { ref: it.ref, sel: `[data-sidebar-item="${it.ref}"][data-sidebar-item-kind="${it.kind}"]` }
    })
}

// The UX review's P3, the list: Needs you lists every kind that waits on a person, each once, and a run that waits sits there.
test('Needs you in the sidebar lists every kind of what needs you, each once, and a waiting run in it, on the busy day', async ({ day, browser }) => {
  test.setTimeout(test.info().timeout + 4 * DAY_STEP_MS)
  await day.part('busy')
  const w = await day.part('waiting')
  const desk = await dayPage(browser, { path: '/crews' })
  const needs = desk.locator('[data-session-list="sidebar"] [data-sidebar-section="needs"]')
  // Every open item listed once, and the run whose task failed in Needs you and not in Running: one read of the page, of one moment,
  // beside the oracle read at the same poll.
  budget(DAY_STEP_MS)
  await expect
    .poll(
      async () => {
        const want = await listedAs(day)
        const seen = await desk.evaluate(
          ({ want, run }) => {
            const list = document.querySelector('[data-session-list="sidebar"]')
            const section = list?.querySelector('[data-sidebar-section="needs"]')
            return {
              counts: Object.fromEntries(want.map((x) => [x.ref, section ? section.querySelectorAll(x.sel).length : -1])),
              runInNeeds: section?.querySelectorAll(`[data-sidebar-run-block="${run}"]`).length ?? 0,
              runAnywhere: list?.querySelectorAll(`[data-sidebar-run-block="${run}"]`).length ?? 0,
            }
          },
          { want, run: w['Paging spike']! },
        )
        const twice = Object.entries(seen.counts).filter(([, n]) => n !== 1)
        return want.length && !twice.length && seen.runInNeeds === 1 && seen.runAnywhere === 1 ? 'each once' : JSON.stringify(seen)
      },
      { timeout: DAY_STEP_MS, message: 'every open item is listed once in Needs you, the failed task under its run there' },
    )
    .toBe('each once')
  // The failed task under its run's header, before its members.
  const spike = needs.locator(`[data-sidebar-run-block="${w['Paging spike']}"]`)
  await expect(spike.locator(`[data-sidebar-item="task:${w['Paging spike']}/index"]`)).toHaveAttribute('data-sidebar-item-kind', 'task')
  // Enter on the plan's row, focused, opens it on the page.
  const plan = needs.locator('[data-sidebar-item="plan:CND-2/1"]')
  await expect(plan).toHaveAttribute('data-sidebar-item-kind', 'plan')
  await plan.focus()
  await desk.keyboard.press('Enter')
  await expect(desk).toHaveURL(new RegExp(`/decisions\\?item=${encodeURIComponent('plan:CND-2/1')}$`))
  // A click on the gate's row, under its run's header (the run has no session yet), opens its card.
  const gate = needs.locator(`[data-sidebar-run-block="${w['Pairing rollout']}"] [data-sidebar-item="decision:${w.gate}"]`)
  await expect(gate).toHaveAttribute('data-sidebar-decision', w.gate!)
  await gate.click()
  await expect(desk.locator(`[data-decision-modal][data-decision-modal-id="${w.gate}"]`)).toBeVisible()
  await desk.getByRole('dialog', { name: 'A decision for you' }).getByRole('button', { name: 'Close' }).click()
  await expect(desk.locator('[data-decision-modal]')).toHaveCount(0)
  // The rail is drawn from the same model: the run that waits on its gate (no session yet) and the run whose task failed are each
  // a capsule there, amber, with the needs mark.
  const rail = await dayPage(browser, { path: '/crews' })
  await rail.keyboard.press('Alt+b')
  await expect(rail.locator('[data-rail]')).toBeVisible()
  for (const run of [w['Pairing rollout']!, w['Paging spike']!]) {
    const capsule = rail.locator(`[data-rail] [data-rail-run="${run}"]`)
    await expect(capsule.locator('[data-rail-play]')).toHaveAttribute('data-rail-play', 'needs')
    await expect(capsule.locator('[data-status-mark]').first()).toHaveAttribute('data-status-mark', 'needs')
  }
  await rail.close()
  // The palette lists the loose question once: its session's row in Needs you, no second row of the same question.
  const asker = w['proc-race-tests']!
  await desk.keyboard.press('Alt+/')
  const palette = desk.locator('[data-palette]')
  await expect(palette).toBeVisible()
  await palette.locator('input[data-palette-input]').fill('proc-race-tests')
  await expect(palette.locator(`[data-palette-group="needs"] [data-palette-row="session:${asker}"]`)).toHaveCount(1)
  // Its one row: the session's, in Needs you alone; no row of the question's own (the old "Decide: …").
  await expect(palette.locator(`[data-palette-row="session:${asker}"]`)).toHaveCount(1)
  await expect(palette.locator(`[data-palette-row*="question:${asker}/"]`)).toHaveCount(0)
  await desk.keyboard.press('Escape')

  // The phone's list holds the plan and the task, each a 44 px target.
  const phone = await dayPage(browser, { width: 390, height: 844, touch: true, path: '/sessions' })
  const list = phone.locator('[data-session-list="page"] [data-sidebar-section="needs"]')
  await tapTarget(list.locator('[data-sidebar-item="plan:CND-2/1"]'))
  await tapTarget(list.locator(`[data-sidebar-item="task:${w['Paging spike']}/index"]`))
})

test('Needs you says when plans and tasks could not be read, and when the inbox holds more than its first page', async ({ day, browser }) => {
  await day.part('busy')
  const w = await day.part('waiting')
  const desk = await dayPage(browser, { path: '/crews' })
  const needs = desk.locator('[data-session-list="sidebar"] [data-sidebar-section="needs"]')
  // The inbox's read answering 500, then let go, then a first page of one plan with a next one and a total of 212: a route of the
  // spec's own that stands before the page loads again, and counts the reads it answered.
  let mode: 'fail' | 'server' | 'paged' = 'fail'
  let reads = 0
  const paged = inboxItem('plan', { title: 'A plan on the first page' })
  await desk.route(/\/api\/inbox(\?.*)?$/, (route) => {
    if (route.request().method() !== 'GET') return route.fallback()
    reads++
    if (mode === 'fail') return route.fulfill({ status: 500, json: { error: { code: 'internal', message: 'the inbox failed' } } })
    if (mode === 'paged') return route.fulfill({ status: 200, json: { items: [summary(paged)], total: 212, next: 'page-2', at: new Date().toISOString() } })
    return route.fallback()
  })
  await desk.reload()
  await expect(desk.locator('#dashboard-sidebar-main')).toBeVisible()
  const failed = needs.locator('[data-sidebar-inbox-failed]')
  await expect(failed).toContainText('Plans and tasks could not be read')
  // The questions are still listed; the plans are not.
  await expect(needs.locator(`[data-sidebar-row="s:${w['proc-race-tests']}"]`)).toHaveCount(1)
  await expect(needs.locator('[data-sidebar-item="plan:CND-2/1"]')).toHaveCount(0)
  // Let go, Read again: the line is gone once the route has answered a read after it.
  mode = 'server'
  const before = reads
  await failed.locator('[data-sidebar-inbox-read]').click()
  await expect.poll(() => reads).toBeGreaterThan(before)
  await expect(failed).toHaveCount(0)
  await expect(needs.locator('[data-sidebar-item="plan:CND-2/1"]')).toHaveCount(1)
  await expect(needs.locator('[data-sidebar-inbox-showing]')).toHaveCount(0)
  // A first page with more behind it: the line says how many of how many, and leads to the page.
  mode = 'paged'
  await desk.reload()
  const showing = needs.locator('[data-sidebar-inbox-showing]')
  await expect(showing).toHaveText('Showing 1 of 212 · all on Needs you')
  await expect(needs.locator(`[data-sidebar-item="${paged.ref}"]`)).toHaveCount(1)
  await showing.locator('[data-sidebar-inbox-all]').click()
  // The page of what needs you open (on this wide window it opens its oldest item by itself, in the address).
  await expect.poll(() => pagePath(desk)).toBe('/decisions')
})

// The row's rule is the rail's: in the full sidebar Needs you is a tile of the six (PR-15), which stays at zero and loses its number.
test.describe('the row leaves at zero', () => {
  let own: OwnServer
  /** The windows `at` opened: closed before the server stops, so none of them goes on reading a later server on the same port. */
  const opened: BrowserContext[] = []
  test.beforeAll(async () => {
    own = await ownServer('counts')
  })
  test.afterAll(async () => {
    await Promise.all(opened.splice(0).map((c) => c.close()))
    await own?.stop()
  })

  /**
   * A page on the spec's own server, its inbox read answered by `inbox` when given, set before the page loads; on a desktop's width
   * the sidebar is folded to the rail (the row's rule is the rail's) unless `full`.
   */
  async function at(browser: Parameters<typeof dayPage>[0], o: { path?: string; width?: number; inbox?: (r: Route) => void; full?: boolean } = {}) {
    // The server keeps the sidebar's mode for every browser (the app's screens' state, OS-2): each page here sets its own mode in its
    // own storage, so it starts from a server that keeps none, and the one an earlier page uploaded is not this page's start.
    await own.api.freshUiState()
    const ctx = await browser.newContext({
      baseURL: own.started.baseURL,
      viewport: { width: o.width ?? 1440, height: o.width ? 844 : 900 },
      ...(o.width ? { hasTouch: true, isMobile: true } : {}),
      storageState: {
        cookies: [],
        origins: [{ origin: new URL(own.started.baseURL).origin, localStorage: [{ name: 'conductor-color-mode', value: 'dark' }, { name: 'conductor.workbenchToken', value: own.started.token }, ...(o.full ? [] : [{ name: 'conductor.sidebar.mode', value: 'rail' }])] }],
      },
    })
    opened.push(ctx)
    const page = await ctx.newPage()
    if (o.inbox) await page.route(/\/api\/inbox(\?.*)?$/, o.inbox)
    const read = o.inbox ? null : page.waitForResponse((r) => /\/api\/inbox(\?|$)/.test(r.url()) && r.request().method() === 'GET')
    await page.goto(o.path ?? '/yard')
    await expect(page.locator(o.width ? '[data-bottom-bar]' : '#dashboard-sidebar-main').first()).toBeVisible()
    await read
    return page
  }
  const row = (p: Page) => p.locator('#dashboard-sidebar-main nav [data-sidebar-decide]')
  const yard = (p: Page) => p.locator('#dashboard-sidebar-main nav a[href="/yard"]')

  test('it leaves when nothing waits and nothing was decided, and comes back for an item, an answer of the day and its page', async ({ browser }) => {
    test.setTimeout(test.info().timeout + 4 * DAY_STEP_MS)
    const page = await at(browser)
    await expect(yard(page)).toBeVisible()
    // Every store read (a positive state, never only an absence), and then the row is gone.
    await expect(page.locator('#dashboard-sidebar-main nav[data-needs-read="true"]')).toHaveCount(1)
    await expect(row(page)).toHaveCount(0)
    // G then N: the page open, its row shown while it is.
    await pressChord(page, 'g', 'n')
    await expect.poll(() => pagePath(page)).toBe('/decisions')
    await expect(row(page)).toBeVisible()
    await page.goto('/yard')
    await expect(yard(page)).toBeVisible()
    await expect(row(page)).toHaveCount(0)

    // The test's own session asks: the row with 1.
    const s = await own.api.ok<Session>('POST', '/api/sessions', { agentId: 'asker', name: 'e2e zero asker' })
    budget(DAY_STEP_MS)
    await expect.poll(async () => (await own.api.session(s.id)).attention, { timeout: DAY_STEP_MS }).toMatchObject({ state: 'needs_input' })
    const q = (await own.api.session(s.id)).attention!.id!
    await expect(row(page).locator('[data-count-of="needs"]')).toHaveText('1')

    // Answered from the page's own buttons: the row stays with no number (this browser answered it today).
    await row(page).click()
    // The page open on its oldest item, the one that waits: the question, opened by itself on this wide window.
    const ref = `question:${s.id}/${q}`
    await expect(page).toHaveURL(itemURL(ref))
    await page.locator(`[data-inbox-row="${ref}"]`).click()
    await page.locator(`[data-inbox-item="${ref}"] [data-inbox-action="answer"]`).first().click()
    budget(DAY_STEP_MS)
    await expect.poll(async () => (await own.api.session(s.id)).attention?.state ?? '', { timeout: DAY_STEP_MS }).not.toBe('needs_input')
    await page.locator('#dashboard-sidebar-main nav a[href="/yard"]').click()
    await expect(page).toHaveURL(/\/yard$/)
    await expect(row(page)).toBeVisible()
    await expect(row(page).locator('[data-count-of="needs"]')).toHaveCount(0)
    // Again after a redraw it causes: the sidebar unfolded, where Needs you is a tile with no number, and folded again.
    await page.keyboard.press('Alt+b')
    await expect(page.locator('[data-rail]')).toHaveCount(0)
    await expect(row(page)).toBeVisible()
    await expect(row(page).locator('[data-count-of="needs"]')).toHaveCount(0)
    await page.keyboard.press('Alt+b')
    await expect(page.locator('[data-rail]')).toBeVisible()
    await expect(row(page)).toBeVisible()

    // Another browser, which answered nothing: no row.
    const other = await at(browser)
    await expect(yard(other)).toBeVisible()
    await expect(other.locator('#dashboard-sidebar-main nav[data-needs-read="true"]')).toHaveCount(1)
    await expect(row(other)).toHaveCount(0)
    // On a phone the tab stays, whatever waits.
    const phone = await at(browser, { width: 390, path: '/sessions' })
    await expect(phone.locator('[data-bottom-tab="decide"]')).toHaveText(/Needs you/)
    await own.api.stopSession(s.id)
    await own.api.stopSession(s.id)
  })

  test('it stays while the inbox is unread or its read failed, and is gone on a switchyard', async ({ browser }) => {
    // Held unanswered: unknown is not zero.
    const held = await at(browser, { inbox: () => {} })
    await expect(yard(held)).toBeVisible()
    await expect(row(held)).toBeVisible()
    await expect(held.locator('#dashboard-sidebar-main nav[data-needs-read="false"]')).toHaveCount(1)
    await expect(row(held).locator('[data-count-of="needs"]')).toHaveCount(0)
    // Answering 500: a read that failed says nothing of what waits.
    const failed = await at(browser, { inbox: (r) => void r.fulfill({ status: 500, json: { error: { code: 'internal', message: 'the inbox failed' } } }) })
    await expect(yard(failed)).toBeVisible()
    await expect(row(failed)).toBeVisible()
    // Answering 403 switchyard: the inbox is not served here, and the row is gone.
    const sy = await at(browser, { inbox: (r) => void r.fulfill({ status: 403, json: { error: { code: 'switchyard', message: 'this server coordinates hosted sessions and launches none' } } }) })
    await expect(yard(sy)).toBeVisible()
    await expect(sy.locator('#dashboard-sidebar-main nav[data-needs-read="true"]')).toHaveCount(1)
    await expect(row(sy)).toHaveCount(0)
    // The full sidebar on a switchyard: no Needs you tile, the Yard's first.
    const syFull = await at(browser, { full: true, inbox: (r) => void r.fulfill({ status: 403, json: { error: { code: 'switchyard', message: 'this server coordinates hosted sessions and launches none' } } }) })
    await expect(syFull.locator('#dashboard-sidebar-main nav[data-needs-read="true"]')).toHaveCount(1)
    await expect(syFull.locator('[data-sidebar-tile]').first()).toHaveAttribute('data-sidebar-tile', 'yard')
    await expect(row(syFull)).toHaveCount(0)
  })

  test('the Needs you tile stays at zero with no number, and shows the number when something asks', async ({ browser }) => {
    test.setTimeout(test.info().timeout + DAY_STEP_MS)
    const page = await at(browser, { full: true })
    const tile = page.locator('#dashboard-sidebar-main nav[aria-label="Pages"] [data-sidebar-decide]')
    // Every store read (a positive state, never only an absence): the tile is there, holding no number.
    await expect(page.locator('#dashboard-sidebar-main nav[data-needs-read="true"]')).toHaveCount(1)
    await expect(tile).toBeVisible()
    await expect(tile.locator('[data-count-of="needs"]')).toHaveCount(0)
    await expect(tile).toHaveText('Needs you')
    // The test's own session asks: the tile shows 1.
    const s = await own.api.ok<Session>('POST', '/api/sessions', { agentId: 'asker', name: 'e2e zero tile' })
    try {
      await expect.poll(async () => (await own.api.session(s.id)).attention, { timeout: DAY_STEP_MS }).toMatchObject({ state: 'needs_input' })
      await expect(tile.locator('[data-count-of="needs"]')).toHaveText('1')
    } finally {
      await own.api.stopSession(s.id)
      await own.api.stopSession(s.id)
    }
  })
})
