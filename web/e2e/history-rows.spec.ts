import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { Locator } from '@playwright/test'
import { Api, budget, expect, test, type Session } from './fixtures'
import { noSideways, tapTarget } from './layout'
import { walk, worst } from './contrast'
import { renderConfig, scratchRepo, START_WAIT_MS, startServer, stopServer, type Started } from './server'

// The History page's rows and filters as the review of its screens asked (#1024): a row's name is its first text, its state one word of
// the app's status language, its time at the right, its action written out (Open, Transcript) and done by Enter on it; the rows under
// day headings that a screen reader hears as headings and that stay at the top while their day scrolls; no id strip and no card. A
// pressed chip says so (aria-pressed, a check mark); the agents that have history come first, the rest behind More agents. On a phone
// the results follow the search at once, the groups wait behind Filters, and the active filters stay on one line. Rows are found by
// their key, never by a count.

const STEP_MS = 15_000 // the config's expect bound, and an action's
const INDEX_MS = 60_000 // a session's record indexed (history-page.spec.ts's bound)
const search = (api: Api, q: string) => api.ok<{ entries: Array<{ key: string; availability: { state: string } }> }>('GET', `/api/history?q=${encodeURIComponent(q)}&limit=100`).then((r) => r.entries)

/** The first text a person meets in `l`: its first text node with something in it. Bounded by an action's wait, not the test's. */
const firstText = (l: Locator) =>
  l.evaluate(
    (el) => {
      const w = document.createTreeWalker(el, NodeFilter.SHOW_TEXT, { acceptNode: (n) => (n.textContent?.trim() ? NodeFilter.FILTER_ACCEPT : NodeFilter.FILTER_SKIP) })
      return w.nextNode()?.textContent?.trim() ?? ''
    },
    undefined,
    { timeout: STEP_MS },
  )

test("a row leads with its name, says its state in one word, writes its action, and sits under its day's heading", async ({ page, api }) => {
  const tag = `hr${Date.now().toString(36)}`
  const live = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: `e2e history rows live ${tag}` })
  const ended = await api.ok<Session>('POST', '/api/sessions', { agentId: 'codex', name: `e2e history rows ended ${tag}` })
  try {
    await api.stopSession(ended.id)
    budget(2 * INDEX_MS)
    await expect.poll(async () => (await search(api, `${tag} kind:session`)).find((e) => e.key === `session:${live.id}`)?.availability.state ?? '', { timeout: INDEX_MS, message: 'the live session is indexed' }).toBe('live')
    await expect.poll(async () => (await search(api, `${tag} kind:session`)).find((e) => e.key === `session:${ended.id}`)?.availability.state ?? '', { timeout: INDEX_MS, message: 'the ended session is indexed as ended' }).not.toMatch(/^(live|)$/)

    budget(30_000 + 30 * STEP_MS)
    await page.goto(`/history?q=${encodeURIComponent(`${tag} kind:session`)}`)
    const pageEl = page.locator('[data-history-page]')
    const liveRow = pageEl.locator(`[data-history-row="session:${live.id}"]`)
    const endedRow = pageEl.locator(`[data-history-row="session:${ended.id}"]`)
    await expect(liveRow).toBeVisible({ timeout: 30_000 })
    await expect(endedRow).toBeVisible()

    // The name first: the row's first text, and the first text of its day after the heading; no id strip, no card around the rows.
    expect(await firstText(liveRow)).toBe(`e2e history rows live ${tag}`)
    expect(await firstText(endedRow)).toBe(`e2e history rows ended ${tag}`)
    const today = pageEl.locator('[data-history-day]').filter({ has: page.locator(`[data-history-row="session:${live.id}"]`) })
    expect(await firstText(today.locator('ul'))).toMatch(new RegExp(`^e2e history rows (live|ended) ${tag}$`))
    await expect(pageEl.locator('[data-history-group]')).toHaveCount(0)
    expect(await endedRow.evaluate((el) => el.closest('ul')!.parentElement!.className)).not.toMatch(/\bborder\b/)

    // One state word each, the sidebar's: the ended one exited; the live one one of the live states. Its time at the right of its name.
    await expect(endedRow.locator('[data-history-row-state]')).toHaveAttribute('aria-label', 'exited')
    await expect(liveRow.locator('[data-history-row-state]')).toHaveAttribute('aria-label', /^(running|working|finished.*|needs you|starting)$/)
    await expect(endedRow.locator('[data-history-row-time]')).toHaveText(/^\d\d:\d\d$/)
    const [name, time] = [await endedRow.locator('[data-history-row-title]').boundingBox(), await endedRow.locator('[data-history-row-time]').boundingBox()]
    expect(time!.x).toBeGreaterThan(name!.x + name!.width)
    // The details say nothing twice: no path, no storage words, no state.
    const detail = endedRow.locator('[data-history-row-detail]')
    await expect(detail).not.toContainText(/listed|record only|live|stopped|exited|\//)
    await expect(endedRow.locator('[data-history-row-id]')).toHaveText(ended.id)
    await expect(endedRow.locator('[data-history-row-id]')).toHaveAccessibleName(`Copy the id ${ended.id}`)

    // The action written out: Open for the live one, Transcript for the ended one, with the sidebar's Resume beside it.
    await expect(liveRow.locator('[data-history-row-action]')).toHaveText('Open')
    await expect(endedRow.locator('[data-history-row-action]')).toHaveText('Transcript')
    await expect(endedRow.locator('[data-resume]')).toBeVisible()
    await expect(endedRow.locator('[data-history-row-title]')).toHaveAccessibleName(`e2e history rows ended ${tag}: Transcript`)

    // The day's heading: Today, a heading, held at the top while its day scrolls.
    const heading = today.locator('[data-history-day-heading]')
    await expect(heading).toHaveText('Today')
    await expect(today.getByRole('heading', { name: 'Today' })).toBeVisible()
    expect(await heading.evaluate((el) => getComputedStyle(el).position)).toBe('sticky')

    // A pressed chip says so: aria-pressed and a check mark; one not pressed has neither.
    const chip = (v: string) => pageEl.locator(`[data-history-chip="${v}"]`)
    await chip('since:7d').click()
    await expect(chip('since:7d')).toHaveAttribute('aria-pressed', 'true')
    await expect(chip('since:7d').locator('[data-history-chip-check]')).toBeVisible()
    await expect(chip('since:24h')).toHaveAttribute('aria-pressed', 'false')
    await expect(chip('since:24h').locator('[data-history-chip-check]')).toHaveCount(0)
    // Since leads the column; the agents that have history first (codex has it now), the rest (Goose: none starts here) behind More agents.
    expect(await pageEl.locator('[data-history-chips-of]').first().getAttribute('data-history-chips-of')).toBe('since')
    await expect(chip('agent:codex')).toBeVisible()
    await expect(chip('agent:goose')).toHaveCount(0)
    await pageEl.locator('[data-history-chips-more="agent"]').click()
    await expect(chip('agent:goose')).toBeVisible()
    // The pressed look keeps its text over the floor in both themes (the column walked as it is).
    for (const theme of ['light', 'dark'] as const) {
      await page.evaluate((t) => localStorage.setItem('conductor-color-mode', t), theme)
      await page.reload()
      await expect(chip('since:7d')).toHaveAttribute('aria-pressed', 'true', { timeout: 30_000 })
      await expect(liveRow).toBeVisible({ timeout: 30_000 })
      const { judged, failures } = await walk(page, pageEl)
      expect(judged, `${theme}: the walk judged some text`).toBeGreaterThan(0)
      expect(failures, `${theme}: the History page's text under its floor:\n${worst(failures)}`).toEqual([])
    }

    // Enter on a focused row does its action.
    const href = await endedRow.locator('[data-history-row-title]').getAttribute('href')
    expect(href).toBeTruthy()
    await endedRow.locator('[data-history-row-title]').focus()
    await page.keyboard.press('Enter')
    await page.waitForURL((u) => `${u.pathname}${u.search}` === href, { timeout: STEP_MS })
  } finally {
    for (const s of [live, ended]) await api.stopSession(s.id).catch(() => undefined)
  }
})

test('on a phone the results follow the search, the groups wait behind Filters, and the active filters stay on one line', async ({ page, api }) => {
  const tag = `hf${Date.now().toString(36)}`
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: `e2e history phone ${tag}` })
  try {
    await api.stopSession(s.id)
    budget(INDEX_MS)
    await expect.poll(async () => (await search(api, `${tag} kind:session`)).some((e) => e.key === `session:${s.id}`), { timeout: INDEX_MS, message: 'the session is indexed' }).toBe(true)

    budget(30_000 + 24 * STEP_MS)
    await page.setViewportSize({ width: 390, height: 844 })
    await page.goto(`/history?q=${encodeURIComponent(`${tag} class:record since:7d agent:claude`)}`)
    const pageEl = page.locator('[data-history-page]')
    const row = pageEl.locator(`[data-history-row="session:${s.id}"]`)
    await expect(row).toBeVisible({ timeout: 30_000 })

    // The first row within the first screen, right after the search and the active filters; the groups not drawn in the page.
    const [box, bar, first] = [await pageEl.locator('[data-history-search]').boundingBox(), await pageEl.locator('[data-history-active-bar]').boundingBox(), await row.boundingBox()]
    expect(first!.y).toBeGreaterThan(bar!.y)
    expect(first!.y + first!.height).toBeLessThan(844)
    expect(first!.y - (box!.y + box!.height)).toBeLessThan(200)
    await expect(pageEl.locator('[data-history-chips]')).toBeHidden()
    // A phone's row leaves the id out of its line (it would take a line of its own): the line's title holds it.
    await expect(row.locator('[data-history-row-id]')).toBeHidden()
    await expect(row.locator('[data-history-row-detail]')).toHaveAttribute('title', new RegExp(`id ${s.id}`))

    // The active filters on one line that scrolls sideways, Clear all beside it, each a 44 px target.
    const tops = await pageEl.locator('[data-history-active]').evaluateAll((els) => els.map((e) => Math.round(e.getBoundingClientRect().top)))
    expect(tops.length).toBe(4)
    expect(new Set(tops).size).toBe(1)
    await tapTarget(pageEl.locator('[data-history-active-remove]').first())
    await tapTarget(pageEl.locator('[data-history-clear-all]'))
    await noSideways(page, { root: pageEl, allow: pageEl.locator('[data-history-active-strip]') })

    // Filters: its count the filters on; it opens the groups in a sheet, where a press filters the results behind it.
    const filters = pageEl.locator('[data-history-filters-button]')
    await expect(filters.locator('[data-history-filters-count]')).toHaveText('3')
    await tapTarget(filters)
    await filters.click()
    const sheet = page.locator('[data-history-filters-sheet]')
    await expect(sheet).toBeVisible()
    await expect(sheet.locator('[data-history-chips-of="since"]')).toBeVisible()
    const day = sheet.locator('[data-history-chip="since:24h"]')
    await tapTarget(day)
    await day.click()
    await expect(day).toHaveAttribute('aria-pressed', 'true')
    await expect(day.locator('[data-history-chip-check]')).toBeVisible()
    await expect(pageEl.locator('[data-history-active="since:24h"]')).toBeAttached()
    await sheet.locator('[data-history-sheet-done]').click()
    await expect(sheet).toHaveCount(0)
    await expect(row).toBeVisible({ timeout: 30_000 })
  } finally {
    await api.stopSession(s.id).catch(() => undefined)
  }
})

// The ranked answer's test runs its own server, ended after it (a test run again starts its own, on data of its own).
let own: Started | null = null
let ownLog = ''
test.afterEach(async () => {
  if (own) await stopServer(own.pid)
  own = null
  if (ownLog) {
    mkdirSync('test-results', { recursive: true })
    copyFileSync(ownLog, 'test-results/history-rows-server.log')
  }
  ownLog = ''
})

test("a word search keeps the server's ranked order under its day headings, and Show more's rows land at the end", async ({ state, page }) => {
  // Records of three days ago that match the words best, and today's that match them less well, more than a page (20) of them: the
  // server ranks the best first, so the days alternate from today's order. Records are dated by their start (the record's file, edited
  // while the server is stopped, the index rebuilt from them at the next start).
  const STOP_MS = 5_000 // stopServer's SIGTERM wait
  const here = dirname(fileURLToPath(import.meta.url))
  const root = mkdtempSync(join(state.root, 'history-ranked-'))
  const [home, data, repo] = ['home', 'data', 'repo'].map((d) => join(root, d)) as [string, string, string]
  for (const d of [home, data, repo]) mkdirSync(d, { recursive: true })
  scratchRepo(repo)
  const config = join(root, 'conductor.json')
  renderConfig(join(here, 'conductor.e2e.json'), config)
  ownLog = join(root, 'server.log')
  const start = () => startServer({ config, home, data, allowedRoot: root, defaultCwd: repo, log: ownLog })
  budget(START_WAIT_MS)
  own = await start()
  let api = new Api(own)

  const tag = `rk${Date.now().toString(36)}`
  const strong = 3
  const weak = 19
  budget((strong + weak) * 2 * STEP_MS)
  const made: Array<{ id: string; strong: boolean }> = []
  for (let i = 0; i < strong + weak; i++) {
    const isStrong = i < strong
    // The best matches: the words alone in a short name; the weaker: the word among many others.
    const name = isStrong ? `${tag} ${tag}` : `${tag} weaker match number ${i} among several other words of a longer name`
    const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name })
    made.push({ id: s.id, strong: isStrong })
  }
  for (const m of made) await api.stopSession(m.id)
  for (const m of made) await expect.poll(async () => (await api.ok<{ record: { endedAt?: string } }>('GET', `/api/history/sessions/${m.id}`)).record.endedAt ?? '', { timeout: STEP_MS }).not.toBe('')

  // Stopped: the best matches moved three days back, the index left to be built again from the records.
  budget(STOP_MS + START_WAIT_MS + INDEX_MS)
  await stopServer(own.pid)
  own = null
  const back = (iso?: string) => (iso ? new Date(Date.parse(iso) - 3 * 86_400_000).toISOString() : iso)
  const records = join(data, 'history', 'sessions')
  for (const m of made.filter((x) => x.strong)) {
    const file = join(records, `${m.id}.json`)
    const r = JSON.parse(readFileSync(file, 'utf8')) as { startedAt: string; endedAt?: string; entries?: Array<{ at: string }> }
    r.startedAt = back(r.startedAt)!
    r.endedAt = back(r.endedAt)
    for (const e of r.entries ?? []) e.at = back(e.at)!
    writeFileSync(file, JSON.stringify(r))
  }
  for (const f of readdirSync(join(data, 'history'))) if (f.startsWith('index.sqlite')) rmSync(join(data, 'history', f))
  own = await start()
  api = new Api(own)
  const q = `${tag} kind:session`
  const first = () => api.ok<{ entries: Array<{ key: string; at: string }>; next?: string }>('GET', `/api/history?q=${encodeURIComponent(q)}&limit=20`)
  await expect.poll(async () => { const r = await first(); return r.entries.length === 20 && !!r.next && r.entries.slice(0, strong).every((e) => made.some((m) => m.strong && `session:${m.id}` === e.key)) }, { timeout: INDEX_MS, message: 'the ranked answer: the three best, of three days ago, first, and a second page' }).toBe(true)
  const page1 = await first()
  const page2 = (await api.ok<{ entries: Array<{ key: string }> }>('GET', `/api/history?q=${encodeURIComponent(q)}&limit=20&after=${encodeURIComponent(page1.next!)}`)).entries.map((e) => e.key)
  expect(page2.length).toBeGreaterThan(0)

  budget(30_000 + 8 * STEP_MS)
  await page.addInitScript((t) => localStorage.setItem('conductor.workbenchToken', t), own.token)
  await page.goto(`${own.baseURL}/history?q=${encodeURIComponent(q)}`)
  const pageEl = page.locator('[data-history-page]')
  const rows = pageEl.locator('[data-history-row]')
  await expect(rows).toHaveCount(20, { timeout: 30_000 })
  // The server's order is the page's: its best match first, under its own day's heading, then Today.
  expect(await rows.evaluateAll((els) => els.map((e) => e.getAttribute('data-history-row')))).toEqual(page1.entries.map((e) => e.key))
  const headings = pageEl.locator('[data-history-day-heading]')
  await expect(headings).toHaveCount(2)
  await expect(headings.nth(0)).not.toHaveText('Today')
  await expect(headings.nth(1)).toHaveText('Today')
  // Show more: the next page's rows land after every row already read, in its order.
  await pageEl.locator('[data-history-more]').click()
  await expect(rows).toHaveCount(20 + page2.length, { timeout: 30_000 })
  expect(await rows.evaluateAll((els) => els.map((e) => e.getAttribute('data-history-row')))).toEqual([...page1.entries.map((e) => e.key), ...page2])
  await expect(headings).toHaveCount(2)
})
