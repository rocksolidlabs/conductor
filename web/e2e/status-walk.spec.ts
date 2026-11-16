import { spawn, type ChildProcess } from 'node:child_process'
import { openSync } from 'node:fs'
import { join } from 'node:path'
import type { Locator, Page } from '@playwright/test'
import { budget, dayPage, endHosted, expect, hostedName, hostedSession, test, useTheme, type Session } from './fixtures'
import { DAY_STEP_MS, type Day, type Names } from './day'
import { probeColour } from './layout'
import { serverBinary } from './server'

// The status walk (UX1's PR-3, docs/plans/ux-review.md, the chapter Tests, "The status walk"): every surface that draws a session's
// state draws one of five marks with its word, the same everywhere. The day's `states` part holds one loose session in each state.
// The table below is this spec's own, never the app's, so that a change of the app's words fails it. One test per surface and size,
// so that a failure names the surface. Each test opens its page once, waits for the sessions its surface shows by id (a mark drawn,
// never an absence), reads each key, word and colour, only then asserts that what its surface does not show is not drawn there, then
// causes a redraw (the theme switched as the app keeps it, a section folded and unfolded) and reads it all again.

interface Want {
  key: string
  word: RegExp
  /** The token the mark is drawn in. */
  tone: string
}

const WALK: Record<string, Want> = {
  'mac-only-flake': { key: 'needs', word: /^needs you$/, tone: '--ui-warning' },
  'yard-tile-resize': { key: 'working', word: /^working$/, tone: '--ui-success' },
  // The age ticks while the test reads.
  'flaky-vim-spec': { key: 'finished', word: /^finished (just now|\d+[mh] ago)$/, tone: '--ui-text-muted' },
  driver: { key: 'running', word: /^running$/, tone: '--ui-success' },
  'old-bisect': { key: 'exited', word: /^exited$/, tone: '--ui-text-muted' },
}
const ALL = Object.keys(WALK)
/** The Yard and the Roundhouse hold live sessions alone (isActive): never the exited one. */
const LIVE = ALL.filter((n) => n !== 'old-bisect')
const KEYS = /^(needs|working|finished|running|exited)$/
/** One page load's and its first read's bound (the config's expect timeout): each load a test makes in a loop raises its timeout by it. */
const LOAD_MS = 15_000

/** Reads a mark (`[data-status-mark]`) once it shows `want`'s key: its word (drawn, or its label where the mark is drawn alone) and its colours. */
async function check(page: Page, mark: Locator, name: string, want: Want, drawsWord: boolean): Promise<void> {
  await expect(mark, `${name}'s mark`).toHaveAttribute('data-status-mark', want.key)
  const got = await mark.evaluate((el) => {
    const glyph = el.querySelector('[data-status-glyph]')
    const word = el.querySelector('[data-status-word]')
    return {
      label: el.getAttribute('aria-label') ?? '',
      role: el.getAttribute('role') ?? '',
      colour: glyph ? getComputedStyle(glyph).color : '',
      word: word ? (word.textContent ?? '').trim() : null,
      wordColour: word ? getComputedStyle(word).color : '',
    }
  })
  expect(got.role, `${name}'s mark is an image`).toBe('img')
  expect(got.label, `${name}'s mark is named by its word`).toMatch(want.word)
  expect(await probeColour(page, got.colour), `${name}'s mark is drawn in ${want.tone}`).toBe(await probeColour(page, `var(${want.tone})`))
  if (drawsWord) {
    expect(got.word, `${name}'s word`).toMatch(want.word)
    expect(await probeColour(page, got.wordColour), `${name}'s word is in the muted text, never a status colour`).toBe(await probeColour(page, 'var(--ui-text-muted)'))
  } else expect(got.word, `${name}'s mark is drawn alone here`).toBeNull()
}

/** Each named session's mark on the page, by `at(id)`. */
async function walk(page: Page, n: Names, names: string[], at: (id: string) => Locator, drawsWord: boolean): Promise<void> {
  for (const name of names) await check(page, at(n[name]!).first(), name, WALK[name]!, drawsWord)
}

/** The day's states, its server's sessions listed as the walk reads them. */
async function states(day: Day): Promise<Names> {
  return day.part('states')
}

/** Unfolds a section of the list (`root`) if it is folded; the fold is kept in the browser's storage, so it outlasts a reload. */
async function unfold(root: Locator, section: string, title: RegExp, tap = false): Promise<Locator> {
  const s = root.locator(`[data-sidebar-section="${section}"]`).first()
  await expect(s).toBeVisible()
  if ((await s.getAttribute('data-folded')) === 'true') {
    const header = s.getByRole('button', { name: title })
    if (tap) await header.tap()
    else await header.click()
  }
  await expect(s).toHaveAttribute('data-folded', 'false')
  return s
}

const sidebarRow = (page: Page, list: 'sidebar' | 'page') => (id: string) => page.locator(`[data-session-list="${list}"] [data-sidebar-row="s:${id}"] [data-status-mark]`)

test("the sidebar's row shows each of the five states with its mark, the Exited section unfolded", async ({ day, browser }) => {
  const n = await states(day)
  const page = await dayPage(browser, { path: `/sessions/${n.driver}` })
  const list = page.locator('[data-session-list="sidebar"]')
  await unfold(list, 'exited', /^Exited/)
  // The row draws the mark alone (its word is the row's meta line's from PR-7): the word is read from the mark's label.
  await walk(page, n, ALL, sidebarRow(page, 'sidebar'), false)
  await useTheme(page, 'light')
  await walk(page, n, ALL, sidebarRow(page, 'sidebar'), false)
})

test("the folded sections' squares are status marks: Needs you's begin with needs, Exited's are exited", async ({ day, browser }) => {
  const n = await states(day)
  const page = await dayPage(browser, { path: `/sessions/${n.driver}` })
  const list = page.locator('[data-session-list="sidebar"]')
  // Every row is drawn first (a mark by id): the folds below then fold what was drawn.
  await unfold(list, 'exited', /^Exited/)
  await walk(page, n, ALL, sidebarRow(page, 'sidebar'), false)
  const squares = (section: string) => list.locator(`[data-sidebar-section="${section}"] [data-section-preview] [data-status-mark]`)
  const read = async () => {
    for (const [section, title] of [['needs', /^Needs you/], ['exited', /^Exited/]] as const) {
      const s = list.locator(`[data-sidebar-section="${section}"]`).first()
      if ((await s.getAttribute('data-folded')) !== 'true') await s.getByRole('button', { name: title }).click()
      await expect(s).toHaveAttribute('data-folded', 'true')
      await expect(squares(section).first()).toBeVisible()
    }
    const needs = await squares('needs').evaluateAll((els) => els.map((e) => e.getAttribute('data-status-mark')))
    const exited = await squares('exited').evaluateAll((els) => els.map((e) => e.getAttribute('data-status-mark')))
    // A loose session that asks comes first; a run block there adds its members' keys, each one of the five.
    expect(needs[0]).toBe('needs')
    for (const k of needs) expect(k).toMatch(KEYS)
    expect(exited.length).toBeGreaterThan(0)
    for (const k of exited) expect(k).toBe('exited')
    await check(page, squares('needs').first(), 'the first square of Needs you', { ...WALK['mac-only-flake']!, word: /^needs you/ }, false)
    await check(page, squares('exited').first(), 'the first square of Exited', WALK['old-bisect']!, false)
  }
  await read()
  // Unfolded and folded again: the squares are drawn afresh.
  await unfold(list, 'needs', /^Needs you/)
  await unfold(list, 'exited', /^Exited/)
  await read()
  await useTheme(page, 'light')
  await read()
})

test('the rail (the sidebar folded) shows the four live states as squares and folds the exited one into its +N', async ({ day, browser }) => {
  const n = await states(day)
  const page = await dayPage(browser, { path: `/sessions/${n.driver}` })
  await page.evaluate(() => localStorage.setItem('conductor.sidebar.mode', 'rail'))
  await page.reload()
  await expect(page.locator('[data-rail]')).toBeVisible()
  const square = (id: string) => page.locator(`[data-rail-session="${id}"] [data-status-mark]`)
  const read = async () => {
    await walk(page, n, LIVE, square, false)
    // Read after the squares are drawn: the exited session has no square, its count is in the +N.
    await expect(page.locator(`[data-rail-session="${n['old-bisect']}"]`)).toHaveCount(0)
    await expect(page.locator('[data-rail-exited]')).toBeVisible()
  }
  await read()
  await useTheme(page, 'light')
  await expect(page.locator('[data-rail]')).toBeVisible()
  await read()
})

test("the session page's header shows each of the five states with its mark and word, at 1440 x 900", async ({ day, browser }) => {
  const n = await states(day)
  const page = await dayPage(browser, { path: `/sessions/${n.driver}` })
  const header = (id: string) => page.locator(`[data-session-status="${id}"]`)
  const read = async () => {
    for (const name of ALL) {
      budget(LOAD_MS)
      await page.goto(`/sessions/${n[name]}`)
      await check(page, header(n[name]!), name, WALK[name]!, true)
    }
  }
  await read()
  await useTheme(page, 'light')
  await read()
})

test("the session page's header shows each of the five states with its mark and word on a phone, 390 x 844 with touch", async ({ day, browser }) => {
  const n = await states(day)
  const page = await dayPage(browser, { path: `/sessions/${n.driver}`, width: 390, height: 844, touch: true })
  const header = (id: string) => page.locator(`[data-session-status="${id}"]`)
  const read = async () => {
    for (const name of ALL) {
      budget(LOAD_MS)
      await page.goto(`/sessions/${n[name]}`)
      await check(page, header(n[name]!), name, WALK[name]!, true)
    }
  }
  await read()
  await useTheme(page, 'light')
  await read()
})

test("the phone's list shows each of the five states with its mark, its Exited section unfolded, 390 x 844 with touch", async ({ day, browser }) => {
  const n = await states(day)
  const page = await dayPage(browser, { path: '/sessions', width: 390, height: 844, touch: true })
  const list = page.locator('[data-session-list="page"]')
  await unfold(list, 'exited', /^Exited/, true)
  await walk(page, n, ALL, sidebarRow(page, 'page'), false)
  await useTheme(page, 'light')
  await walk(page, n, ALL, sidebarRow(page, 'page'), false)
})

test("the palette's session rows show each of the five states with its mark, the exited one in its Exited group", async ({ day, browser }) => {
  const n = await states(day)
  // A page with no terminal, which would take the key for itself.
  const page = await dayPage(browser, { path: '/agents' })
  const read = async () => {
    for (const name of ALL) {
      // The palette's key is the layout's: its root is drawn (dayPage, useTheme), so the key is heard; pressed once.
      if (!(await page.locator('[data-palette]').isVisible())) await page.keyboard.press('Control+k')
      await expect(page.locator('[data-palette]')).toBeVisible()
      await page.locator('input[data-palette-input]').fill(name)
      const row = page.locator(`[data-palette-row="session:${n[name]}"]`)
      if (name === 'old-bisect') await expect(page.locator('[data-palette-group="exited"]').locator(`[data-palette-row="session:${n[name]}"]`)).toBeVisible()
      await check(page, row.locator('[data-status-mark]').first(), name, WALK[name]!, false)
    }
    await page.keyboard.press('Escape')
    await expect(page.locator('[data-palette]')).toBeHidden()
  }
  await read()
  await useTheme(page, 'light')
  await read()
})

test("the Yard's tiles, its bar and its focused header show the four live states; the exited session is not drawn there", async ({ day, browser }) => {
  const n = await states(day)
  const page = await dayPage(browser, { path: '/yard' })
  const tile = (id: string) => page.locator(`[data-session-tile][data-session-id="${id}"] [data-tile-foot] [data-status-mark]`)
  const slice = (key: string) => page.locator(`[data-attention-bar] [data-attention-slice="${key}"]`)
  const read = async () => {
    await walk(page, n, LIVE, tile, true)
    // The bar: a slice for each live state, each counted, the four summing to the live sessions the server lists now.
    for (const key of ['needs', 'working', 'finished', 'running']) await expect(slice(key)).toHaveAttribute('data-count', /^[1-9]\d*$/)
    await expect
      .poll(async () => {
        const counts = await Promise.all(['needs', 'working', 'finished', 'running'].map(async (k) => Number(await slice(k).getAttribute('data-count'))))
        const live = (await day.listSessions()).filter((s: Session) => s.status === 'running' || s.status === 'starting').length
        return counts.reduce((a, b) => a + b, 0) - live
      }, { message: "the bar's four slices count the live sessions" })
      .toBe(0)
    for (const [key, tone] of [['needs', '--ui-warning'], ['working', '--ui-success'], ['finished', '--ui-text-muted']] as const) {
      const bg = await slice(key).evaluate((el) => getComputedStyle(el).backgroundColor)
      expect(await probeColour(page, bg), `the bar's ${key} slice is drawn in ${tone}`).toBe(await probeColour(page, `var(${tone})`))
    }
    await expect(page.locator(`[data-session-tile][data-session-id="${n['old-bisect']}"]`)).toHaveCount(0)
  }
  await read()
  await useTheme(page, 'light')
  await read()
  // The focused header: each live session opened in place.
  for (const name of LIVE) {
    budget(LOAD_MS)
    await page.goto(`/yard?focus=${n[name]}`)
    await check(page, page.locator(`[data-yard-focused-status="${n[name]}"]`), name, WALK[name]!, true)
  }
})

test("the Roundhouse's slide and strip show the four live states; the exited session has no slide and no strip item", async ({ day, browser }) => {
  const n = await states(day)
  const page = await dayPage(browser, { path: '/roundhouse' })
  const slide = (id: string) => page.locator(`[data-roundhouse-status="${id}"]`)
  const chip = (id: string) => page.locator(`[data-strip-chip="${id}"] [data-status-mark]`)
  const read = async () => {
    await walk(page, n, LIVE, slide, true)
    // The strip's chip draws the mark alone: its word is the mark's label (and the chip's).
    await walk(page, n, LIVE, chip, false)
    await expect(page.locator(`[data-roundhouse-status="${n['old-bisect']}"]`)).toHaveCount(0)
    await expect(page.locator(`[data-strip-chip="${n['old-bisect']}"]`)).toHaveCount(0)
  }
  await read()
  await useTheme(page, 'light')
  await read()
})

test("a guest's page shows finished with no age: the attention frame carries no time", async ({ day, browser }) => {
  await states(day)
  const api = day.api
  // A session of the test's own on the day's finished agent, ended and removed before the test ends (the day's guard).
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'day-finished', name: 'e2e walk guest' })
  let linkId = ''
  try {
    budget(DAY_STEP_MS)
    await expect.poll(async () => (await api.session(s.id)).attention?.state, { timeout: DAY_STEP_MS, message: 'the guest session finishes its turn' }).toBe('done')
    const made = await api.ok<{ link: { id: string }; token: string }>('POST', `/api/sessions/${s.id}/links`, { role: 'view', label: 'walk guest' })
    linkId = made.link.id
    const page = await dayPage(browser, { path: `/join/${made.token}`, token: false, ready: 'button:has-text("Join session")' })
    await page.getByRole('button', { name: 'Join session' }).click()
    const mark = page.locator('[data-join-status]')
    await check(page, mark, 'the guest session', { key: 'finished', word: /^finished$/, tone: '--ui-text-muted' }, true)
  } finally {
    if (linkId) await api.call('DELETE', `/api/sessions/${s.id}/links/${encodeURIComponent(linkId)}`)
    await api.stopSession(s.id)
    await expect.poll(async () => (await api.session(s.id)).status, { message: 'the guest session ends' }).toMatch(/^(exited|stopped)$/)
    await api.stopSession(s.id)
  }
})

test("a hosted session whose host is gone shows the exited mark with \"host away\" in the sidebar's row", async ({ day, browser }) => {
  await states(day)
  const api = day.api
  const name = hostedName('e2e-walk-away')
  const logFile = join(day.server.root, `${name}.log`)
  const log = openSync(logFile, 'a')
  let host: ChildProcess | null = spawn(serverBinary(), ['host', '--server', day.server.started.baseURL, '--token', day.server.hostToken!, '--host-name', 'e2e-laptop', '--name', name, '--cwd', day.server.home, '--relay-only', '--no-local', '--', '/bin/sh', '-c', 'sleep 3600'], {
    env: { PATH: process.env.PATH ?? '/usr/bin:/bin', LANG: 'C.UTF-8', HOME: day.server.home },
    stdio: ['ignore', log, log],
  })
  try {
    const id = (await hostedSession(api, logFile, name)).id
    const page = await dayPage(browser, { path: '/agents' })
    await expect(page.locator(`[data-session-list="sidebar"] [data-sidebar-row="s:${id}"] [data-status-mark]`)).toHaveAttribute('data-status-mark', 'running')
    host.kill('SIGKILL')
    host = null
    // A state that holds: the server says the host is away before the row is read.
    budget(DAY_STEP_MS)
    await expect.poll(async () => (await api.session(id)).status, { timeout: DAY_STEP_MS, message: 'the server sees the host gone' }).toBe('host_disconnected')
    await check(page, page.locator(`[data-session-list="sidebar"] [data-sidebar-row="s:${id}"] [data-status-mark]`), name, { key: 'exited', word: /^host away$/, tone: '--ui-text-muted' }, false)
    // The server drops the copy itself (signal.DisconnectGrace, 60 s, then the maintenance sweep's 30 s): a DELETE now answers 500.
    const drop = 60_000 + 30_000 + 30_000
    budget(drop)
    await expect.poll(async () => (await day.listSessions()).some((x) => x.id === id), { timeout: drop, intervals: [2_000], message: 'the server drops the hosted copy' }).toBe(false)
  } finally {
    // Only when the test failed before the kill: the host is ended through the server and the session removed.
    if (host) await endHosted(api, name, host)
  }
})
