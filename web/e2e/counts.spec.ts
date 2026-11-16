import type { Page, Route } from '@playwright/test'
import { budget, dayPage, expect, test, type Session } from './fixtures'
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
  // A decision of a run that no longer runs is on the page, not in the sidebar's section.
  let stopped = 0
  for (const it of open.filter((it) => it.kind === 'decision')) {
    const d = (await day.api.ok<{ decision: { run: string } }>('GET', `/api/decisions/${encodeURIComponent(it.ref.slice('decision:'.length))}`)).decision
    const r = await day.api.call<{ run?: { stoppedAt?: string; record?: boolean; resumedBy?: string } }>('GET', `/api/runs/${encodeURIComponent(d.run)}`)
    const run = r.status === 200 ? r.body.run : undefined
    if (!run || run.stoppedAt || run.record || run.resumedBy) stopped++
  }
  const grid = sessions.filter((s) => (s.status === 'running' || s.status === 'starting') && s.attention?.state === 'needs_input' && !(s.attention as { answered?: string }).answered).length
  const n = open.length
  const more = plansTasks + stopped
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
      section: String(n - more),
      sectionMore: more ? `+${more} on the page` : '',
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
    text(desk, '[data-sidebar-section="needs"] [data-section-more]'),
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
    // After a reload of each page, the same.
    for (const p of [desk, rail, phone]) await p.reload()
    await expect(desk.locator('#dashboard-sidebar-main')).toBeVisible()
    await desk.keyboard.press('Alt+/')
    await desk.locator('input[data-palette-input]').fill('Needs you')
    const again = await agree(day, desk, rail, phone, 'every reader after a reload')
    expect(again.n).toBe(before.n)
    // The section's "+N on the page" opens the page.
    await desk.keyboard.press('Escape')
    await expect(desk.locator('[data-palette]')).toBeHidden()
    if (again.want.sectionMore) {
      await desk.locator('[data-sidebar-section="needs"] [data-section-more]').click()
      await expect(desk).toHaveURL(/\/decisions$/)
    }
  } finally {
    await day.api.stopSession(mine.id)
    await expect.poll(() => day.statusOf(mine.id), { timeout: DAY_STEP_MS }).toMatch(/^(exited|stopped|gone)$/)
    await day.api.stopSession(mine.id)
    await expect.poll(() => day.statusOf(mine.id), { timeout: DAY_STEP_MS }).toBe('gone')
  }
})

// The row's rule is the rail's: in the full sidebar Needs you is a tile of the six (PR-15), which stays at zero and loses its number.
test.describe('the row leaves at zero', () => {
  let own: OwnServer
  test.beforeAll(async () => {
    own = await ownServer('counts')
  })
  test.afterAll(async () => {
    await own?.stop()
  })

  /**
   * A page on the spec's own server, its inbox read answered by `inbox` when given, set before the page loads; on a desktop's width
   * the sidebar is folded to the rail (the row's rule is the rail's) unless `full`.
   */
  async function at(browser: Parameters<typeof dayPage>[0], o: { path?: string; width?: number; inbox?: (r: Route) => void; full?: boolean } = {}) {
    const ctx = await browser.newContext({
      baseURL: own.started.baseURL,
      viewport: { width: o.width ?? 1440, height: o.width ? 844 : 900 },
      ...(o.width ? { hasTouch: true, isMobile: true } : {}),
      storageState: {
        cookies: [],
        origins: [{ origin: new URL(own.started.baseURL).origin, localStorage: [{ name: 'conductor-color-mode', value: 'dark' }, { name: 'conductor.workbenchToken', value: own.started.token }, ...(o.full ? [] : [{ name: 'conductor.sidebar.mode', value: 'rail' }])] }],
      },
    })
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
    await page.keyboard.press('g')
    await page.keyboard.press('n')
    await expect(page).toHaveURL(/\/decisions$/)
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
    await expect(page).toHaveURL(/\/decisions$/)
    const ref = `question:${s.id}/${q}`
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
