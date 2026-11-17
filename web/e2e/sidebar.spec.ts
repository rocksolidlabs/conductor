import { spawn, type ChildProcess } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { existsSync, openSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Locator, Page } from '@playwright/test'
import { dayPage, endHosted, expect, hostedName, hostedSession, rowAnswer, test, type Session } from './fixtures'
import { inside, noOverlap, tabTo, uncut } from './layout'
import { ownServer, type OwnServer } from './own-server'
import { serverBinary, stubBash, stubPath } from './server'

// The sidebar's list (design 3a, 3b, 3c, 3f): sessions and runs ordered by
// what needs you, a run kept whole where its most urgent member is, a machine
// as a tag on the row, Shared with you below your own, Exited folded to one
// line, every section folding from its header and remembered; the actions on
// a row; a prompt answered in the row; the keys on a focused row; your menu
// beside the name, holding Alerts and the collapse; the foot holding only the
// pages; the rail.
test.describe.configure({ mode: 'serial' })

const sessions: string[] = []
let crewId = ''
let runId = ''
let reviewId = ''
let alive: Session | null = null
let host: ChildProcess | null = null
/** The hosted session's name, this run's own: the teardown ends whatever is listed under it. */
let hostName = ''

test.afterAll(async ({ api }) => {
  try {
    // The hosted session ends through its host and is removed before the host dies: killed alone, it stays listed for a minute and
    // more, where a later repetition finds it and the rail counts its machine (#211).
    await endHosted(api, hostName, host)
  } finally {
    // Whatever became of that, the rest is stopped; a failure above is still reported.
    if (runId) await api.stopRun(runId)
    for (const id of sessions) await api.stopSession(id)
    if (crewId) await api.call('DELETE', `/api/crews/${encodeURIComponent(crewId)}`)
  }
})

function transcript(home: string, s: Session): string {
  const file = join(home, '.stub-sessions', `${s.agentSession?.id ?? ''}.txt`)
  return existsSync(file) ? readFileSync(file, 'utf8') : ''
}

async function ended(api: { session: (id: string) => Promise<Session> }, id: string) {
  await expect.poll(async () => (await api.session(id)).status, { timeout: 15_000 }).toMatch(/exited|stopped/)
}

test('a run of two in Needs you shows its asking member, the rest folded', async ({ page, api }) => {
  alive = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e alive' })
  sessions.push(alive.id)
  crewId = (
    await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
      name: 'e2e sidebar crew',
      goal: 'be listed',
      cwd: '',
      where: 'server',
      isolation: 'none',
      openAfterLaunch: false,
      members: [
        { name: 'lead', agentId: 'claude', prompt: 'say hello', start: { when: 'immediately' } },
        { name: 'review', agentId: 'codex-untrusted', prompt: 'review it', start: { when: 'immediately' } },
      ],
    })
  ).crew.id
  runId = (await api.launchCrew(crewId)).id
  // review holds the trust question: it needs you; lead runs.
  await expect
    .poll(
      async () => {
        const run = await api.run(runId)
        reviewId = run.members.find((m) => m.name === 'review')?.sessionId ?? ''
        if (!reviewId) return ''
        return (await api.session(reviewId)).attention?.state ?? ''
      },
      { timeout: 30_000 },
    )
    .toBe('needs_input')
  await expect.poll(async () => (await api.run(runId)).members.find((m) => m.name === 'lead')?.status, { timeout: 30_000 }).toBe('running')

  await page.goto('/')
  const needs = page.locator('[data-sidebar-section="needs"]').first()
  const block = needs.locator(`[data-sidebar-run-block="${runId}"]`)
  await expect(block).toBeVisible()
  // Whole, in one section: found once in the whole sidebar, inside Needs you.
  await expect(page.locator(`[data-session-list="sidebar"] [data-sidebar-run-block="${runId}"]`)).toHaveCount(1)
  await expect(page.locator(`[data-session-list="sidebar"] [data-sidebar-section="needs"] [data-sidebar-run-block="${runId}"]`)).toHaveCount(1)
  await expect(block).toHaveAttribute('data-row-state', 'needs')
  const header = block.locator('[data-sidebar-run-group]')
  await expect(header).toContainText('e2e sidebar crew')
  await expect(header).toContainText('2 agents')
  await expect(header).toHaveAttribute('href', `/runs/${encodeURIComponent(runId)}`)
  await expect(block.locator('[data-run-play="needs"]')).toHaveCount(1)
  // Under a line, the member asking with its question, and one line for the other; no crew, run or path words on the rows.
  const members = block.locator('ol > [data-row-kind="session"]')
  await expect(members).toHaveCount(1)
  await expect(members.nth(0)).toHaveAttribute('data-row-state', 'needs')
  await expect(members.nth(0)).toContainText('review')
  await expect(members.nth(0).locator('[data-row-prompt]')).toContainText('Trust this folder?')
  const more = block.locator('[data-run-more]')
  await expect(more).toHaveText(/^and 1 more · 1 (working|finished|running)$/)
  // At the sidebar's minimum the question is two lines in the row and its choices are in the panel beside it (rowAnswer), inside
  // the panel and the window.
  const answers = await rowAnswer(page, members.nth(0))
  const choices = answers.locator('[data-row-answer] button')
  // The trust question's two answers, drawn before they are measured: a loop over none would check nothing.
  await expect(choices).toHaveCount(2)
  await inside(choices, answers)
  for (let i = 0; i < (await choices.count()); i++) expect((await choices.nth(i).boundingBox())!.x + (await choices.nth(i).boundingBox())!.width, 'a choice inside the window').toBeLessThanOrEqual(page.viewportSize()!.width)
  // Unfolded, the run shows both, the one asking first.
  await more.click()
  await expect(more).toHaveCount(0)
  await expect(members).toHaveCount(2)
  await expect(members.nth(0)).toContainText('review')
  await expect(members.nth(1)).toContainText('lead')
  await expect(members.nth(1).locator('[data-row-meta]')).toHaveText(/^claude · \d+[smh]/)
  // Other specs leave sessions behind: the counts are checked by shape, not by number.
  await expect(needs.locator('[data-section-count]')).toHaveText(/^\d+$/)
  // The loose session runs on the server, sending no signal: it is in Working, says so, and nothing says the path.
  const working = page.locator('[data-sidebar-section="working"]').first()
  await expect(working.locator(`[data-sidebar-row="s:${alive.id}"] [data-row-meta]`)).toHaveText(/^claude · server · \d+[smh]/)
  await expect(working.locator(`[data-sidebar-row="s:${alive.id}"]`)).not.toContainText('/tmp/')
  await expect(working.locator('[data-section-count]')).toHaveText(/^· \d+$/)
})

test('Exited starts folded with its count; opened, its session has Resume; a fold outlives a reload with a preview', async ({ page, api }) => {
  const done = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e done' })
  sessions.push(done.id)
  await api.stopSession(done.id)
  await ended(api, done.id)

  await page.goto('/')
  const exited = page.locator('[data-sidebar-section="exited"]').first()
  await expect(exited).toHaveAttribute('data-folded', 'true')
  await expect(exited.locator('[data-section-count]')).toHaveText(/^· \d+$/)
  await expect(exited.locator('[data-sidebar-row]')).toHaveCount(0)
  expect(await exited.locator('[data-section-preview] [data-status-mark="exited"]').count()).toBeGreaterThan(0)
  await exited.getByRole('button', { name: /^Exited/ }).click()
  await expect(exited).toHaveAttribute('data-folded', 'false')
  const row = exited.locator(`[data-sidebar-row="s:${done.id}"]`)
  await expect(row).toHaveAttribute('data-row-state', 'exited')
  await expect(row).toContainText(/(exit \d+|stopped) · \d+[smh] ago/)
  await expect(row.locator('[data-resume][data-resume-kind="icon"]')).toBeVisible()
  // Hovering Resume shows Resume, not the row's More button fading in over it (#257): the pointer's target at its centre is the
  // Resume button, and a click resumes the session.
  const resume = row.locator('[data-resume]')
  await resume.hover()
  await expect(row.locator('[data-row-actions]')).toBeVisible()
  // The arrow and the … line up (#331): one vertical centre within a pixel, the … just left of the arrow, both inside the row.
  const boxes = await row.evaluate((el) => {
    const r = (q: string) => (el.querySelector(q) as HTMLElement).getBoundingClientRect()
    const row = el.getBoundingClientRect()
    const a = r('[data-resume]')
    const m = r('[data-row-more]')
    return { centres: Math.abs(a.top + a.height / 2 - (m.top + m.height / 2)), gap: a.left - m.right, arrowInset: row.right - a.right, moreInside: m.left > row.left }
  })
  expect(boxes.centres).toBeLessThanOrEqual(1)
  expect(boxes.gap).toBeGreaterThanOrEqual(0)
  expect(boxes.gap).toBeLessThanOrEqual(8)
  expect(boxes.arrowInset).toBeGreaterThanOrEqual(4)
  expect(boxes.moreInside).toBe(true)
  const target = await resume.evaluate((el) => {
    const r = el.getBoundingClientRect()
    const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2)
    return hit ? (hit.closest('[data-resume]') ? 'resume' : hit.closest('[data-row-more]') ? 'more' : hit.tagName) : 'none'
  })
  expect(target).toBe('resume')
  await resume.click()
  // Resume makes a new session from the exited one; it is stopped with the rest at the end.
  let fresh: Session | undefined
  await expect
    .poll(
      async () => {
        fresh = (await api.ok<{ sessions: Session[] }>('GET', '/api/sessions')).sessions.find((s) => s.resumedFrom === done.id)
        return fresh?.status ?? ''
      },
      { timeout: 30_000, message: 'the click resumed the session' },
    )
    .toMatch(/^(starting|running)$/)
  sessions.push(fresh!.id)

  // Working folds from its header, keeps its count and shows what is inside as squares; the fold survives a reload. The resumed
  // stub sends no signal (running) or its own turn (working): either is Working's.
  const working = page.locator('[data-sidebar-section="working"]').first()
  await working.getByRole('button', { name: /^Working/ }).click()
  await expect(working).toHaveAttribute('data-folded', 'true')
  await expect(working.locator('[data-section-count]')).toHaveText(/^· \d+$/)
  const marks = working.locator('[data-section-preview] [data-status-mark]')
  expect(await marks.count()).toBeGreaterThan(0)
  for (const key of await marks.evaluateAll((els) => els.map((e) => e.getAttribute('data-status-mark')))) expect(key).toMatch(/^(working|running)$/)
  await expect(working.locator('[data-sidebar-row]')).toHaveCount(0)
  await page.reload()
  await expect(page.locator('[data-sidebar-section="working"]').first()).toHaveAttribute('data-folded', 'true')
  await expect(page.locator('[data-sidebar-section="exited"]').first()).toHaveAttribute('data-folded', 'false')
  await page.locator('[data-sidebar-section="working"]').first().getByRole('button', { name: /^Working/ }).click()
  await expect(page.locator('[data-sidebar-section="working"]').first()).toHaveAttribute('data-folded', 'false')
})

test('a new prompt opens a folded Needs you by itself', async ({ page, api }) => {
  // A page with no open run: the home redirects to the session asking, whose run the list would unfold.
  await page.goto('/crews')
  const needs = page.locator('[data-sidebar-section="needs"]').first()
  const before = Number(await needs.locator('[data-section-count]').textContent())
  await needs.getByRole('button', { name: /^Needs you/ }).click()
  await expect(needs).toHaveAttribute('data-folded', 'true')
  await page.reload()
  await expect(page.locator('[data-sidebar-section="needs"]').first()).toHaveAttribute('data-folded', 'true')
  // The live session starts asking: the section unfolds and its row shows the question.
  await api.ok('POST', `/api/sessions/${alive!.id}/attention`, { state: 'needs_input', message: 'Which branch?' })
  const after = page.locator('[data-sidebar-section="needs"]').first()
  await expect(after).toHaveAttribute('data-folded', 'false', { timeout: 15_000 })
  await expect(after.locator(`[data-sidebar-row="s:${alive!.id}"] [data-row-prompt]`)).toHaveText('Which branch?')
  await expect(after.locator('[data-section-count]')).toHaveText(String(before + 1))
  await api.ok('POST', `/api/sessions/${alive!.id}/attention`, { state: 'working' })
  const back = page.locator(`[data-sidebar-section="working"] [data-sidebar-row="s:${alive!.id}"]`).first()
  await expect(back).toBeVisible({ timeout: 15_000 })
  await expect(back.locator('[data-status-mark]').first()).toHaveAttribute('data-status-mark', 'working')
})

test('a session hosted on another machine carries its machine on the row, under no heading', async ({ page, api, state }) => {
  // This host's own log: the session is found by the id on the line the host prints once registered, never by its name (#211).
  const logFile = join(state.root, `sidebar-host-${randomUUID()}.log`)
  const log = openSync(logFile, 'a')
  hostName = hostedName('hosted-e2e')
  const bash = stubBash()
  host = spawn(serverBinary(), ['host', '--server', state.baseURL, '--token', state.hostToken, '--host-name', 'e2e-laptop', '--name', hostName, '--agent', 'claude', '--cwd', state.home, '--relay-only', '--no-local', '--', bash.command, stubPath, '--session-id', randomUUID()], {
    env: { PATH: bash.path, LANG: 'C.UTF-8', HOME: state.home, STUB_IDENTITY: 'claude', STUB_REPORT: 'plain' },
    stdio: ['ignore', log, log],
  })
  const hostedId = (await hostedSession(api, logFile, hostName)).id
  await page.goto('/')
  const row = page.locator(`[data-session-list] [data-sidebar-row="s:${hostedId}"]`).first()
  await expect(row).toBeVisible()
  await expect(row.locator('[data-row-machine="e2e-laptop"]')).toBeVisible()
  await expect(row.locator('[data-row-meta]')).toHaveText(/^e2e-laptop · claude · /)
  await expect(page.locator('[data-sidebar-host-group]')).toHaveCount(0)
  await expect(page.locator('[data-rail-host]')).toHaveCount(0)
})

test('on a run page the whole list shows, the open run marked and its section open; no crew box filters it', async ({ page }) => {
  await page.goto(`/runs/${encodeURIComponent(runId)}`)
  const open = page.locator('[data-session-list] [data-sidebar-run-open]').first()
  await expect(open).toBeVisible()
  await expect(open).toHaveAttribute('aria-current', 'page')
  await expect(page.locator('[data-sidebar-run]')).toHaveCount(0)
  await expect(page.locator(`[data-session-list] [data-sidebar-row="s:${alive!.id}"]`).first()).toBeVisible()
  await expect(page.locator('[data-sidebar-section="needs"]').first()).toHaveAttribute('data-folded', 'false')
})

test('a row offers Share, Stop and More on hover, with the same menu on a right-click', async ({ page }) => {
  await page.goto('/crews')
  const row = page.locator(`[data-session-list] [data-sidebar-row="s:${alive!.id}"]`).first()
  await row.hover()
  await expect(row.locator('[data-row-actions]')).toBeVisible()
  await fitsSidebar(page, row.locator('[data-row-actions]'))
  // Share opens the dialog on this session, with one link made and copied.
  await row.locator('[data-row-share]').click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toContainText('Share e2e alive')
  await expect(dialog.locator('[data-created-url]')).toBeVisible({ timeout: 15_000 })
  await expect(dialog.locator('[data-share-link]')).toHaveCount(1)
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)
  // More lists the actions; Show in the Yard focuses the session there.
  await row.hover()
  await row.locator('[data-row-more]').click()
  await expect(page.getByRole('menuitem', { name: /Share/ })).toBeVisible()
  await page.getByRole('menuitem', { name: /Show in the Yard/ }).click()
  await expect(page).toHaveURL(new RegExp(`/yard\\?focus=${alive!.id}`))
  // A right-click opens the same menu.
  await page.goto('/crews')
  await page.locator(`[data-session-list] [data-sidebar-row="s:${alive!.id}"]`).first().click({ button: 'right' })
  await expect(page.getByRole('menuitem', { name: /^Open/ }).first()).toBeVisible()
  await expect(page.getByRole('menuitem', { name: /Stop/ })).toBeVisible()
  await page.keyboard.press('Escape')
})

test('a member stops from its own row after the row asks; the run stops from its header; the block lands in Exited whole', async ({ page, api }) => {
  await page.goto('/crews')
  const leadId = (await api.run(runId)).members.find((m) => m.name === 'lead')!.sessionId!
  const block = page.locator(`[data-session-list] [data-sidebar-run-block="${runId}"]`).first()
  const lead = block.locator(`[data-sidebar-row="s:${leadId}"]`)
  // In Needs you the run shows review alone: its line shows lead too.
  await block.locator('[data-run-more]').click()
  await lead.hover()
  await lead.locator('[data-row-stop]').click()
  await expect(lead.locator('[data-row-stop-confirm]')).toHaveText('Stop lead?')
  await expect(lead).toContainText('Its terminal closes')
  await fitsSidebar(page, lead.locator('[data-row-stop-buttons]'))
  await lead.locator('[data-confirm-stop]').click()
  await expect.poll(async () => (await api.session(leadId)).status, { timeout: 15_000 }).toMatch(/exited|stopped/)
  // Exited inside its run, with Resume; the run stays where review still asks.
  await expect(lead).toHaveAttribute('data-row-state', 'exited', { timeout: 15_000 })
  await expect(lead.locator('[data-resume]')).toBeVisible()
  await expect(page.locator(`[data-sidebar-section="needs"] [data-sidebar-run-block="${runId}"]`)).toHaveCount(1)
  // The run from its header: it asks in the row, then every member ends and the block moves to Exited, whole.
  // Hovered at its play icon: at the sidebar's minimum (#437) the actions it shows cover the middle of the header.
  await block.locator('[data-sidebar-run-group]').hover({ position: { x: 12, y: 12 } })
  await block.locator('[data-run-stop]').click()
  await expect(block.locator('[data-run-stop-confirm]')).toHaveText('Stop e2e sidebar crew?')
  // The run header's confirm at the sidebar's minimum (#137, #437): its buttons whole inside it.
  await fitsSidebar(page, block.locator('[data-run-stop-buttons]'))
  await block.locator('[data-confirm-stop]').click()
  await expect.poll(async () => !!(await api.run(runId)).stoppedAt, { timeout: 20_000 }).toBe(true)
  const exited = page.locator('[data-sidebar-section="exited"]').first()
  await expect(exited).toBeVisible({ timeout: 20_000 })
  if ((await exited.getAttribute('data-folded')) === 'true') await exited.getByRole('button', { name: /^Exited/ }).click()
  await expect(exited.locator(`[data-sidebar-run-block="${runId}"]`)).toBeVisible({ timeout: 20_000 })
  await expect(exited.locator(`[data-sidebar-run-block="${runId}"] [data-sidebar-row]`)).toHaveCount(2)
  await expect(page.locator(`[data-sidebar-section="needs"] [data-sidebar-run-block="${runId}"]`)).toHaveCount(0)
})

test('a loose session stops from its row and moves to Exited', async ({ page, api }) => {
  await page.goto('/crews')
  const row = page.locator(`[data-session-list] [data-sidebar-row="s:${alive!.id}"]`).first()
  await row.hover()
  await row.locator('[data-row-stop]').click()
  await row.locator('[data-confirm-stop]').click()
  await expect.poll(async () => (await api.session(alive!.id)).status, { timeout: 15_000 }).toMatch(/exited|stopped/)
  const exited = page.locator('[data-sidebar-section="exited"]').first()
  if ((await exited.getAttribute('data-folded')) === 'true') await exited.getByRole('button', { name: /^Exited/ }).click()
  await expect(exited.locator(`[data-sidebar-row="s:${alive!.id}"]`)).toBeVisible({ timeout: 15_000 })
  await expect(exited.locator(`[data-sidebar-row="s:${alive!.id}"] [data-row-stop]`)).toHaveCount(0)
})

test('a free-text prompt gets a reply field, in the panel beside the row at 200 px; Enter sends the line and the row moves on', async ({ page, api, state }) => {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e branch' })
  sessions.push(s.id)
  await api.ok('POST', `/api/sessions/${s.id}/attention`, { state: 'needs_input', message: 'Which branch should I base it on?' })
  await page.goto('/crews')
  const row = page.locator(`[data-session-list="sidebar"] [data-sidebar-row="s:${s.id}"]`).first()
  await expect(row).toHaveAttribute('data-row-state', 'needs', { timeout: 30_000 })
  await expect(row.locator('[data-row-prompt]')).toHaveText('Which branch should I base it on?')
  await expect(row.locator('[data-row-choice]')).toHaveCount(0)
  const field = (await rowAnswer(page, row)).locator('[data-row-reply] input, input[data-row-reply]').first()
  await field.fill('main')
  await field.press('Enter')
  await expect.poll(() => transcript(state.home, s), { timeout: 15_000, message: 'the line was typed' }).toContain('main\n')
  await expect(row).toHaveAttribute('data-row-state', /^(working|finished|running)$/, { timeout: 15_000 })
  await expect(row.locator('[data-row-reply]')).toHaveCount(0)
  await expect(page.locator('[data-row-panel]')).toHaveCount(0)
  await expect(page).toHaveURL(/\/crews$/)
})

test('a reply being typed in a row keeps the focus and its keys while the rows change (#596)', async ({ page, api, state }) => {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e reply kept' })
  sessions.push(s.id)
  await api.ok('POST', `/api/sessions/${s.id}/attention`, { state: 'needs_input', message: 'Which fix should I make?' })
  await page.goto('/crews')
  const list = page.locator('[data-session-list="sidebar"]')
  const row = list.locator(`[data-sidebar-row="s:${s.id}"]`).first()
  await expect(row).toHaveAttribute('data-row-state', 'needs', { timeout: 30_000 })
  // In the panel beside the row at the sidebar's minimum: the panel counts as the list's, so the row stays the one the keys act on.
  const field = (await rowAnswer(page, row)).locator('[data-row-reply] input, input[data-row-reply]').first()
  // The field takes the focus, so the list names the row (its focusin) as the row the keys act on.
  await field.click()
  await page.keyboard.type('fi')
  await expect(field).toHaveValue('fi')

  // Another session launched: the rows change, and the list's watch has run once its row is drawn (it follows that render).
  const other = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e rows change' })
  sessions.push(other.id)
  await expect(list.locator(`[data-sidebar-row="s:${other.id}"]`).first()).toBeVisible({ timeout: 15_000 })
  await expect(field).toBeFocused()

  // The next keys are the reply's: an x is typed, not the list's Stop question, and Enter sends the line.
  await page.keyboard.type('x')
  await expect(field).toHaveValue('fix')
  await expect(row.locator('[data-confirm-stop]')).toHaveCount(0)
  await page.keyboard.press('Enter')
  await expect.poll(() => transcript(state.home, s), { timeout: 15_000, message: 'the line was typed' }).toContain('fix\n')
  expect((await api.session(s.id)).status).not.toMatch(/exited|stopped/)
})

test('the list takes the keys: ↓ from the filter, J K move, Escape leaves; a digit answers the focused row alone; X asks, Enter opens, R the run', async ({ page, api, state }) => {
  // Two sessions asking: the page open on one, the other answered from its row by a digit.
  const a = await api.ok<Session>('POST', '/api/sessions', { agentId: 'asker', name: 'e2e keys a' })
  const b = await api.ok<Session>('POST', '/api/sessions', { agentId: 'asker', name: 'e2e keys b' })
  sessions.push(a.id, b.id)
  await expect.poll(async () => `${(await api.session(a.id)).attention?.state}/${(await api.session(b.id)).attention?.state}`, { timeout: 30_000 }).toBe('needs_input/needs_input')
  await page.goto(`/sessions/${a.id}`)
  await expect(page.locator('[data-quick-reply]')).toBeVisible({ timeout: 30_000 })
  const list = page.locator('[data-session-list="sidebar"]')
  const focused = list.locator('[data-row-focused]')

  // The filter box, then ↓: the first row has the focus; J and K move it; Escape leaves the list. (Alt+S and / open the command
  // palette since #302, checked in palette-keys.spec.ts; the box is a plain filter of the list, reached by a click.)
  // The terminal takes the focus once it connects, which can come after the reply bar shows: wait for it, so the click is not
  // undone by the connect taking the focus back.
  await expect(page.locator('.terminal-host .xterm-helper-textarea').first()).toBeFocused({ timeout: 30_000 })
  await page.getByPlaceholder('Filter sessions, runs, people').click()
  await expect(page.getByPlaceholder('Filter sessions, runs, people')).toBeFocused()
  await page.keyboard.press('ArrowDown')
  await expect(focused).toHaveCount(1)
  const first = await focused.getAttribute('data-sidebar-row')
  await page.keyboard.press('j')
  await expect(focused).toHaveCount(1)
  expect(await focused.getAttribute('data-sidebar-row')).not.toBe(first)
  await page.keyboard.press('k')
  expect(await focused.getAttribute('data-sidebar-row')).toBe(first)
  await page.keyboard.press('Escape')
  await expect(focused).toHaveCount(0)

  // A digit answers the focused row's prompt and never the open page's.
  const rowB = list.locator(`[data-sidebar-row="s:${b.id}"]`).first()
  await rowB.locator('a[href]').first().focus()
  await expect(rowB).toHaveAttribute('data-row-focused', '')
  await page.keyboard.press('1')
  await expect.poll(() => transcript(state.home, b), { timeout: 15_000, message: 'b got the choice' }).toContain('Postgres\n')
  expect(transcript(state.home, a)).not.toContain('Postgres')
  expect((await api.session(a.id)).attention?.state).toBe('needs_input')
  await expect(rowB).toHaveAttribute('data-row-state', /^(working|finished|running)$/, { timeout: 15_000 })
  // The row kept the focus as it moved sections.
  await expect(rowB).toHaveAttribute('data-row-focused', '')

  // X asks in the row and Escape takes it back; S opens the share dialog; Enter opens the row.
  await page.keyboard.press('x')
  await expect(rowB.locator('[data-row-stop-confirm]')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(rowB.locator('[data-row-stop-confirm]')).toHaveCount(0)
  await expect(rowB).toHaveAttribute('data-row-focused', '')
  await page.keyboard.press('s')
  const dialog = page.getByRole('dialog')
  await expect(dialog).toContainText('Share e2e keys b')
  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
  await rowB.locator('a[href]').first().focus()
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(new RegExp(`/sessions/${b.id}$`))

  // R from a member opens its run: the crew's run, in Exited by now.
  const exited = page.locator('[data-sidebar-section="exited"]')
  if ((await exited.getAttribute('data-folded')) === 'true') await exited.getByRole('button', { name: /^Exited/ }).click()
  const member = exited.locator(`[data-sidebar-row="s:${reviewId}"]`)
  await member.locator('a[href]').first().focus()
  await page.keyboard.press('r')
  await expect(page).toHaveURL(new RegExp(`/runs/${runId}$`))
})

test('your menu holds Alerts and the collapse; the foot holds only the pages', async ({ page }) => {
  await page.goto('/crews')
  await expect(page.locator('[data-session-list="sidebar"]')).toBeVisible()
  const header = page.locator('#dashboard-sidebar-main [data-slot="header"]')
  const account = header.locator('[data-header-account]')
  await expect(page.locator('[data-sidebar-tools]')).toHaveCount(0)
  await expect(header.locator('[data-header-alerts]')).toHaveCount(0)
  await expect(header.locator('[data-sidebar-collapse]')).toHaveCount(0)
  await account.click()
  await expect(page.getByRole('menuitem')).toHaveText([/Your name/, /^Alerts…$/, /Collapse to the rail/, /Keyboard shortcuts/, /Toggle theme/, /Open on your phone/, /Workbench token/, /Forget token/])
  // Alerts opens its panel with the switches and, under them, the rules; Escape closes it and gives the focus back to your menu's
  // button.
  await page.getByRole('menuitem', { name: /^Alerts/ }).click()
  await expect(page.getByRole('menu')).toHaveCount(0)
  const panel = page.getByRole('dialog', { name: 'Alerts' })
  await expect(panel.getByText('Browser notification')).toBeVisible()
  await expect(panel.locator('[data-alert-switches] ~ [data-alerts-rules] [data-routing-matrix]')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(panel).toHaveCount(0)
  await expect(account).toBeFocused()
  // Collapse to the rail: the rail shows, its Expand button takes the focus; Alt+B brings the sidebar back, and Expand gives the
  // focus to your menu's button.
  await account.click()
  await page.getByRole('menuitem', { name: /Collapse to the rail/ }).click()
  await expect(page.getByRole('menu')).toHaveCount(0)
  const rail = page.locator('[data-rail]')
  await expect(rail).toBeVisible()
  await expect(page.locator('[data-rail-expand]')).toBeFocused()
  // On the rail the menu keeps Alerts, not the collapse.
  await page.locator('[data-rail-foot] [data-header-account]').click()
  await expect(page.getByRole('menuitem', { name: /^Alerts/ })).toBeVisible()
  await expect(page.getByRole('menuitem', { name: /Collapse to the rail/ })).toHaveCount(0)
  await page.keyboard.press('Escape')
  await expect(page.getByRole('menu')).toHaveCount(0)
  await page.keyboard.press('Alt+b')
  await expect(page.locator('[data-session-list="sidebar"]')).toBeVisible()
  await page.keyboard.press('Alt+b')
  await expect(rail).toBeVisible()
  await page.locator('[data-rail-expand]').click()
  await expect(page.locator('[data-session-list="sidebar"]')).toBeVisible()
  await expect(account).toBeFocused()
  await account.click()
  await page.getByRole('menuitem', { name: /Your name/ }).click()
  const dialog = page.locator('[data-name-dialog]')
  await dialog.getByRole('textbox', { name: 'Your name' }).fill('Nate R')
  await dialog.getByRole('button', { name: 'Save' }).click()
  await expect(dialog).toBeHidden()
  await expect(account).toHaveAttribute('aria-label', 'Your menu, Nate R')
  // The foot holds only the pages: its tiles and More's list, in order, are destinations.spec.ts's (every list of the pages).
  await expect(page.getByRole('navigation', { name: 'Pages' }).locator('a, button')).toHaveCount(6)
})

test('the rail: the counts on top, a capsule holding its members, the corner tiles, +N for the exited', async ({ page, api }) => {
  // A crew asking for the capsule (review holds the trust question); a session with an event badge for the news corner.
  const crew2 = (
    await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
      name: 'e2e rail crew',
      goal: 'show',
      cwd: '',
      where: 'server',
      isolation: 'none',
      openAfterLaunch: false,
      members: [
        { name: 'lead', agentId: 'claude', prompt: 'say hello', start: { when: 'immediately' } },
        { name: 'review', agentId: 'codex-untrusted', prompt: 'review it', start: { when: 'immediately' } },
      ],
    })
  ).crew.id
  const run2 = (await api.launchCrew(crew2)).id
  await expect
    .poll(
      async () => {
        const run = await api.run(run2)
        const id = run.members.find((m) => m.name === 'review')?.sessionId ?? ''
        return id ? ((await api.session(id)).attention?.state ?? '') : ''
      },
      { timeout: 30_000 },
    )
    .toBe('needs_input')
  const marked = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e marked' })
  sessions.push(marked.id)

  await page.goto('/crews')
  // Folded by its key, pressed once the layout that listens for it has drawn the list.
  await expect(page.locator('[data-session-list="sidebar"]')).toBeVisible()
  await page.keyboard.press('Alt+b')
  const rail = page.locator('[data-rail]')
  await expect(rail).toBeVisible()
  await expect(rail.locator(`[data-rail-session="${marked.id}"]`)).toBeVisible()
  // A badge the Alerts rules route to the sidebar, arriving live (badges come from the stream the page watches).
  await api.ok('POST', `/api/sessions/${marked.id}/attention`, { state: 'done', message: 'finished the sweep' })
  await expect(rail.locator('[data-rail-count="needs"]')).toHaveText(/^[1-9]\d*$/)
  await expect(rail.locator('[data-rail-count="news"]')).toHaveText(/^[1-9]\d*$/)
  // The run as a capsule, its members inside, the play icon amber while review asks; the tooltip in words.
  const capsule = rail.locator(`[data-rail-run="${run2}"]`)
  await expect(capsule.locator('[data-rail-member]')).toHaveCount(2)
  await expect(capsule.locator('[data-rail-play="needs"]')).toBeVisible()
  // Each member by its status's word (utils/status.ts): lead's stub answers its prompt and ends its turn, so it has finished.
  await expect(capsule.locator('a').first()).toHaveAttribute('aria-label', /^e2e rail crew · run started \d\d:\d\d · review needs you · lead finished (just now|\d+[mh] ago)$/)
  await expect(capsule.locator('[data-status-mark]').first()).toHaveAttribute('data-status-mark', 'needs')
  // The corners: new events bottom right, the machine bottom left; the exited run dashed; loose exited folded into +N.
  await expect(rail.locator(`[data-rail-session="${marked.id}"] [data-rail-news]`)).toBeVisible()
  await expect(rail.locator('[data-rail-session][data-rail-tile="machine"]')).toHaveCount(1)
  await expect(rail.locator(`[data-rail-run="${runId}"][data-rail-dashed] [data-rail-member][data-rail-dashed]`)).toHaveCount(2)
  const plus = rail.locator('[data-rail-exited]')
  await expect(plus).toHaveText(/^\+[1-9]\d*$/)
  await plus.click()
  await expect(page.locator('[data-session-list="sidebar"]')).toBeVisible()
  await expect(page.locator('[data-sidebar-section="exited"]')).toHaveAttribute('data-folded', 'false')
  await api.stopRun(run2)
  await api.call('DELETE', `/api/crews/${encodeURIComponent(crew2)}`)
})

// The full sidebar's width (#437): it opens at its minimum, 12.5 rem (200 px; 16.75 rem while its header held five parts, #706),
// on every screen; a drag widens it up to 26 % of the window, never less than the minimum plus 5 rem (280 px), and no narrower than
// the minimum, and on toward the window's edge gives the rail. Each case has its own page, so its own browser context and storage.
// The widths are written here, never read from the app's table, so that a change of the number fails them.
test.describe('the full sidebar\'s width', () => {
  const MIN = 200
  const handle = (page: Page) => page.locator('[role="separator"][aria-controls="dashboard-sidebar-main"]')
  const width = (page: Page) => page.locator('#dashboard-sidebar-main').evaluate((el) => el.getBoundingClientRect().width)
  const saved = (page: Page, key = 'conductor.sidebar.width') => page.evaluate((k) => localStorage.getItem(k), key)
  /** Where the handle is: it is 0 px wide with a 12 px hit area, so the mouse goes to its box, not a locator's click. */
  async function grip(page: Page) {
    const box = (await handle(page).boundingBox())!
    return { x: box.x + box.width / 2, y: box.y + box.height / 2 }
  }
  /** Presses the handle and moves to each x in turn, holding; `release` lets go at the last. */
  async function drag(page: Page, xs: number[], release = true) {
    const at = await grip(page)
    await page.mouse.move(at.x, at.y)
    await page.mouse.down()
    for (const x of xs) await page.mouse.move(x, at.y, { steps: 4 })
    if (release) await page.mouse.up()
  }
  async function open(page: Page, path = '/crews') {
    await page.goto(path)
    await expect(handle(page)).toBeAttached()
    await expect(page.locator('[data-session-list="sidebar"]')).toBeVisible()
  }

  for (const [w, h] of [
    [1440, 900],
    [2560, 1440],
  ] as const) {
    test(`with no saved width it opens at its minimum, 200 px, at ${w}×${h}`, async ({ page }) => {
      await page.setViewportSize({ width: w, height: h })
      await open(page)
      await expect.poll(() => width(page)).toBeCloseTo(MIN, 0)
      expect(await saved(page)).toBeNull()
    })
  }

  test('a drag widens it, and one past 26 % of the window stops there', async ({ page }) => {
    await open(page)
    await drag(page, [300])
    await expect.poll(() => width(page)).toBeCloseTo(300, 0)
    await expect.poll(() => saved(page)).toBe('18.75')
    await drag(page, [900])
    // 26 % of 1440 px.
    await expect.poll(() => width(page)).toBeCloseTo(374.4, 0)
    await expect.poll(() => saved(page)).toBe('23.4')
  })

  test('a drag left stops at the minimum, and going on to the window\'s edge gives the rail', async ({ page }) => {
    await open(page)
    await drag(page, [300])
    await drag(page, [120], false)
    await expect.poll(() => width(page)).toBeCloseTo(MIN, 0)
    await page.mouse.move(1, (await grip(page)).y, { steps: 4 })
    await page.mouse.up()
    await expect(page.locator('[data-rail]')).toBeVisible()
    await expect.poll(() => width(page)).toBe(64)
    await expect(handle(page)).toHaveCount(0)
  })

  test('a reload keeps the width, and a double-click on the handle returns it to the minimum', async ({ page }) => {
    await open(page)
    await drag(page, [320])
    await expect.poll(() => saved(page)).toBe('20')
    await page.reload()
    await expect(handle(page)).toBeAttached()
    await expect.poll(() => width(page)).toBeCloseTo(320, 0)
    const at = await grip(page)
    await page.mouse.dblclick(at.x, at.y)
    await expect.poll(() => width(page)).toBeCloseTo(MIN, 0)
    await expect.poll(() => saved(page)).toBe('12.5')
  })

  test('the rail and back keeps the width', async ({ page }) => {
    await open(page)
    await drag(page, [310])
    await page.keyboard.press('Alt+b')
    await expect(page.locator('[data-rail]')).toBeVisible()
    await expect.poll(() => width(page)).toBe(64)
    await page.locator('[data-rail-expand]').click()
    await expect(page.locator('[data-session-list="sidebar"]')).toBeVisible()
    await expect.poll(() => width(page)).toBeCloseTo(310, 0)
  })

  test('a width saved before this change is dropped once: the sidebar opens at the minimum, and a drag after that is kept', async ({ page }) => {
    await open(page)
    await page.evaluate(() => localStorage.setItem('conductor.sidebar.size', '24'))
    await page.reload()
    await expect(handle(page)).toBeAttached()
    await expect.poll(() => width(page)).toBeCloseTo(MIN, 0)
    expect(await saved(page, 'conductor.sidebar.size')).toBeNull()
    await drag(page, [288])
    await expect.poll(() => saved(page)).toBe('18')
    await page.reload()
    await expect(handle(page)).toBeAttached()
    await expect.poll(() => width(page)).toBeCloseTo(288, 0)
  })

  test('resizing the window keeps the width within the new bounds', async ({ page }) => {
    await open(page)
    await drag(page, [900])
    await expect.poll(() => width(page)).toBeCloseTo(374.4, 0)
    // At 1024 px 26 % is 266 px, so the most is the minimum plus 5 rem, 280 px; a drag there stops at it too.
    await page.setViewportSize({ width: 1024, height: 900 })
    await expect.poll(() => width(page)).toBeCloseTo(280, 0)
    await drag(page, [600])
    await expect.poll(() => width(page)).toBeCloseTo(280, 0)
    // Wider again: the width stays what it was, the drag's bound grows back.
    await page.setViewportSize({ width: 2560, height: 1440 })
    await expect.poll(() => width(page)).toBeCloseTo(280, 0)
    await drag(page, [1500])
    await expect.poll(() => width(page)).toBeCloseTo(665.6, 0)
    await page.setViewportSize({ width: 1440, height: 900 })
    await expect.poll(() => width(page)).toBeCloseTo(374.4, 0)
    // Narrowed while at the minimum: still the minimum.
    const at = await grip(page)
    await page.mouse.dblclick(at.x, at.y)
    await page.setViewportSize({ width: 1024, height: 900 })
    await expect.poll(() => width(page)).toBeCloseTo(MIN, 0)
  })

  test('a press on the handle that moves nothing, in a narrower window, keeps the width dragged in the wider one', async ({ page }) => {
    await page.setViewportSize({ width: 2560, height: 1440 })
    await open(page)
    await drag(page, [1500])
    await expect.poll(() => width(page)).toBeCloseTo(665.6, 0)
    await expect.poll(() => saved(page)).toBe('41.6')
    // Narrower: drawn at that window's maximum, 26 % of 1440 px, while the dragged width is still held.
    await page.setViewportSize({ width: 1440, height: 900 })
    await expect.poll(() => width(page)).toBeCloseTo(374.4, 0)
    const at = await grip(page)
    await page.mouse.move(at.x, at.y)
    await page.mouse.down()
    await page.mouse.up()
    // Wider again: drawn at the held width in this visit. The press's save ran in its mouseup's tick, before the resize.
    await page.setViewportSize({ width: 2560, height: 1440 })
    await expect.poll(() => width(page)).toBeCloseTo(665.6, 0)
    expect(await saved(page)).toBe('41.6')
    // And after a reload in the same window, the same width: the visit and the saved width agree.
    await page.reload()
    await expect(handle(page)).toBeAttached()
    await expect.poll(() => width(page)).toBeCloseTo(665.6, 0)
  })

  /** Whether `el`'s text shows whole: nothing of it cut off by an ellipsis or its box. */
  const whole = (el: Locator) => el.evaluate((e) => e.scrollWidth <= e.clientWidth)

  for (const [w, h, widest] of [
    [1440, 900, 374],
    [1280, 720, 333],
  ] as const) {
    test(`the header is the mark, the name and your menu, the name whole, as it opens and dragged to its widest and back, at ${w}×${h}`, async ({ page }) => {
      await page.setViewportSize({ width: w, height: h })
      await open(page)
      await expect.poll(() => width(page)).toBeCloseTo(MIN, 0)
      const root = page.locator('#dashboard-sidebar-main')
      const header = root.locator('[data-slot="header"]')
      const name = header.locator('[data-sidebar-name]')
      const launch = root.getByRole('button', { name: /Launch agent/ }).first()
      async function shownWhole(at: string) {
        for (const part of ['[data-sidebar-mark]', '[data-sidebar-name]', '[data-header-account]']) await inside(header.locator(part), root)
        // Alerts and the collapse are in your menu, not in the header.
        await expect(header.locator('[data-header-alerts]'), at).toHaveCount(0)
        await expect(header.locator('[data-sidebar-collapse]'), at).toHaveCount(0)
        // "Conductor", not "C…"; "Launch agent", not "Launch ag…" (#906, row 4).
        await expect(name).toHaveText('Conductor')
        await uncut(name)
        await uncut(launch.locator('[data-slot="label"]'))
        expect(await header.evaluate((el) => el.scrollWidth <= el.clientWidth), `nothing spills out of the header ${at}`).toBe(true)
        // Not on a margin of a pixel or two: with the bundled font the name has at least 8 px to spare before your menu.
        const spare = await name.evaluate((el) => el.parentElement!.querySelector('[data-sidebar-menus]')!.getBoundingClientRect().left - el.getBoundingClientRect().right - Number.parseFloat(getComputedStyle(el.parentElement!).columnGap))
        expect(spare, `the row's room to spare ${at}`).toBeGreaterThanOrEqual(8)
      }
      await shownWhole('as it opens')
      // Dragged to its widest and back past the minimum: it stops at each, drawn again, and still shows it all.
      await drag(page, [w])
      await expect.poll(() => width(page)).toBeCloseTo(widest, 0)
      await shownWhole('at its widest')
      await drag(page, [100])
      await expect.poll(() => width(page)).toBeCloseTo(MIN, 0)
      await shownWhole('back at the minimum')
    })
  }

  test('with a wider font the name gives way, shortened with its whole text in its title, and every control stays in the bar, uncovered (#706)', async ({ page }) => {
    await open(page)
    await expect.poll(() => width(page)).toBeCloseTo(MIN, 0)
    const header = page.locator('#dashboard-sidebar-main [data-slot="header"]')
    const name = header.locator('[data-sidebar-name]')
    await expect(name).toHaveText('Conductor')
    expect(await whole(name), 'the name, with the bundled font').toBe(true)
    // A machine whose font sets "Conductor" much wider than the bundled Inter does.
    await page.addStyleTag({ content: '[data-sidebar-name] { letter-spacing: 0.6em; }' })
    await expect.poll(() => whole(name), { message: 'the name is shortened' }).toBe(false)
    await expect(name).toHaveAttribute('title', 'Conductor')
    for (const part of ['[data-sidebar-mark]', '[data-header-account]']) {
      const control = header.locator(part)
      await expect(control, part).toBeVisible()
      await fitsSidebar(page, control)
      // Nothing over it: what a click at its middle reaches is the control itself.
      const reached = await control.evaluate((el) => {
        const r = el.getBoundingClientRect()
        const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2)
        return !!hit && el.contains(hit)
      })
      expect(reached, `${part} uncovered`).toBe(true)
    }
    expect(await header.evaluate((el) => el.scrollWidth <= el.clientWidth), 'nothing spills out of the bar').toBe(true)
  })

  test('in a window 1024 wide the handle widens the sidebar from 200 to 280 px: its most is the minimum plus 5 rem (#706)', async ({ page }) => {
    await page.setViewportSize({ width: 1024, height: 768 })
    await open(page)
    await expect.poll(() => width(page)).toBeCloseTo(MIN, 0)
    await drag(page, [(await grip(page)).x + 200])
    // 26 % of 1024 px is 266 px, short of the minimum plus 80 px: the most is 280 px, the width at which a row's question is whole.
    await expect.poll(() => width(page)).toBeCloseTo(280, 0)
  })

  test('the filter lines up with Launch agent, at the width it opens with, dragged wider, and on the phone\'s list (#702)', async ({ page }) => {
    await open(page)
    const root = page.locator('#dashboard-sidebar-main')
    /** The left and right edges of the filter and of Launch agent, which agree within a pixel. */
    async function linedUp(scope: Locator, at: string) {
      const field = (await scope.getByPlaceholder('Filter sessions, runs, people').boundingBox())!
      const button = (await scope.getByRole('button', { name: /Launch agent/ }).first().boundingBox())!
      expect(Math.abs(field.x - button.x), `left edges ${at}`).toBeLessThanOrEqual(1)
      expect(Math.abs(field.x + field.width - (button.x + button.width)), `right edges ${at}`).toBeLessThanOrEqual(1)
    }
    await linedUp(root, 'as it opens')
    await drag(page, [420])
    await expect.poll(() => width(page)).toBeCloseTo(374.4, 0)
    await linedUp(root, 'dragged wider')
    // The phone's home screen is the same list, at the window's width.
    await page.setViewportSize({ width: 400, height: 800 })
    await page.goto('/sessions')
    const list = page.locator('[data-session-list="page"]')
    await expect(list).toBeVisible()
    await linedUp(list.locator('xpath=..'), 'on the phone')
  })

  test('Shared with you: its heading whole, its count on one line, and a joined session\'s name whole, as the sidebar opens and drawn again (#906)', async ({ page, api }) => {
    await page.addInitScript(() => localStorage.setItem('conductor.displayName', 'e2e sharer'))
    const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e shared row' })
    sessions.push(s.id)
    const { token } = await api.ok<{ token: string }>('POST', `/api/sessions/${s.id}/links`, { role: 'view' })
    try {
      await page.goto(`/join/${token}`)
      await page.getByRole('button', { name: 'Join session' }).click()
      const section = page.locator('[data-sidebar-section="shared"]')
      const row = section.locator('[data-shared-entry]').filter({ hasText: 'e2e shared row' })
      await expect(row).toHaveCount(1)
      await expect.poll(() => width(page)).toBeCloseTo(MIN, 0)
      const title = section.locator('[data-section-title]')
      const count = section.locator('[data-section-count]')
      const name = row.locator('[data-shared-name]')
      const forget = row.locator('[data-shared-forget]')
      async function shownWhole(at: string) {
        // The section sits below Needs you, Finished and Working: on a busy server it is under the list's fold, and a person scrolls
        // to it. Its row brought into view first, so that what is judged (and pointed at) is what a person sees.
        await row.scrollIntoViewIfNeeded()
        await expect(title).toHaveText('Shared with you')
        expect(await whole(title), `the heading ${at}`).toBe(true)
        // One line: the count is no taller than the heading's one line beside it.
        await expect(count).toHaveText('· 1')
        expect((await count.boundingBox())!.height, `the count ${at}`).toBeLessThanOrEqual((await title.boundingBox())!.height + 1)
        await expect(name).toHaveText('e2e shared row')
        expect(await whole(name), `the row's name ${at}`).toBe(true)
        // Hidden, Forget takes no click: the pointer away, what its place reaches is the row's link.
        await page.mouse.move(1, 1)
        await expect(forget.locator('xpath=ancestor::span[1]')).toHaveCSS('opacity', '0')
        const reached = await forget.evaluate((el) => {
          const r = el.getBoundingClientRect()
          const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2)
          // The row's own link, not any link that happens to be drawn there (a page's tile below the list's fold is a link too).
          const own = el.closest('[data-shared-entry]')?.querySelector('a')
          return own && hit?.closest('a') === own ? 'link' : hit && el.contains(hit) ? 'forget' : 'other'
        })
        expect(reached, `the hidden Forget's place ${at}`).toBe('link')
        // Forget shows over the row's corner on hover, inside the sidebar, as a session row's actions do.
        await row.hover()
        await expect(forget.locator('xpath=ancestor::span[1]')).toHaveCSS('opacity', '1')
        await fitsSidebar(page, forget)
      }
      await shownWhole('as the sidebar opens')
      // A longer name than fits is shortened, and its whole text is the row's title.
      await expect(row.getByRole('link')).toHaveAttribute('title', 'e2e shared row · view only')
      // Dragged wider and back to the minimum: the list is drawn again at each width, and still shows it all.
      await drag(page, [420])
      await expect.poll(() => width(page)).toBeCloseTo(374.4, 0)
      await drag(page, [150])
      await expect.poll(() => width(page)).toBeCloseTo(MIN, 0)
      await shownWhole('dragged wider and back')
    } finally {
      await page.locator('[data-shared-entry]').first().hover().catch(() => {})
      await page.locator('[data-shared-forget]').first().click().catch(() => {})
    }
  })

  test('Shared with you on a touch screen: Forget shows, a tap beside it opens the link and forgets nothing, a tap on it forgets (#906)', async ({ browser, api, state }) => {
    const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e shared touch' })
    sessions.push(s.id)
    const { token } = await api.ok<{ token: string }>('POST', `/api/sessions/${s.id}/links`, { role: 'view' })
    const ctx = await browser.newContext({ viewport: { width: 400, height: 800 }, hasTouch: true, isMobile: true })
    try {
      await ctx.addInitScript((t) => {
        localStorage.setItem('conductor.workbenchToken', t)
        localStorage.setItem('conductor.displayName', 'e2e sharer')
      }, state.token)
      const phone = await ctx.newPage()
      await phone.goto(`/join/${token}`)
      await phone.getByRole('button', { name: 'Join session' }).tap()
      await expect(phone.locator('.terminal-host')).toHaveCount(1, { timeout: 30_000 })
      await phone.goto('/sessions')
      const row = phone.locator('[data-sidebar-section="shared"] [data-shared-entry]').filter({ hasText: 'e2e shared touch' })
      const forget = row.locator('[data-shared-forget]')
      const bar = row.locator('[data-shared-forget-bar]')
      /** Forget in sight with nothing hovered (a touch screen has no hover), over none of the row's words. */
      async function forgetShows(at: string) {
        await expect(row, at).toHaveCount(1)
        await expect(bar, at).toHaveCSS('opacity', '1')
        await expect(forget, at).toBeVisible()
        const name = (await row.locator('[data-shared-name]').boundingBox())!
        expect(name.x + name.width, `the name clear of Forget ${at}`).toBeLessThanOrEqual((await bar.boundingBox())!.x)
      }
      await forgetShows('on the list')
      // A tap on the row's upper right corner, just beside Forget: the link opens, and nothing is forgotten. A tap at a point goes
      // where the point is on the screen: the row is brought into view first (other specs' sessions can push it below the fold).
      await row.scrollIntoViewIfNeeded()
      const b = (await bar.boundingBox())!
      await phone.touchscreen.tap(b.x - 6, b.y + b.height / 2)
      await expect(phone).toHaveURL(new RegExp(`/join/${token}$`))
      await phone.goBack()
      await expect(phone).toHaveURL(/\/sessions$/)
      // Drawn again on the way back: still listed, Forget still in sight.
      await forgetShows('after going back')
      await forget.tap()
      await expect(row).toHaveCount(0)
    } finally {
      await ctx.close()
    }
  })

  /**
   * The desktop app's bridge, with `ready` the update it holds ('' for none); `found` changes it as the app's check does, and
   * tells the page, which asks again.
   */
  async function desktopApp(page: Page, state: { token: string; baseURL: string }, ready: string) {
    await page.addInitScript(
      ({ token, url, ready }) => {
        const w = window as unknown as { conductorDesktop: unknown; e2eReady: string; e2eFound?: () => void }
        w.e2eReady = ready
        w.conductorDesktop = {
          version: 'e2e',
          platform: 'linux',
          token: async () => token,
          openExternal: async () => {},
          settings: { get: async () => ({}), set: async (p: object) => p },
          restartServer: async () => {},
          openInBrowser: async () => {},
          showLog: async () => {},
          serverState: async () => ({ state: 'running', url, failures: 0 }),
          notice: async () => '',
          ice: async () => ({ forwarding: false, port: 0, publicIp: '', wslAddress: '', firewall: 'unknown' }),
          allowIceFirewall: async () => 'unknown',
          versions: async () => ({ app: 'e2e', electron: '', node: '', chrome: '', server: '' }),
          onServerState: () => () => {},
          update: async () => ({ ready: w.e2eReady, announce: false, failed: '' }),
          onUpdate: (cb: () => void) => {
            w.e2eFound = cb
            return () => {}
          },
          onInvite: () => () => {},
          installUpdate: async () => 'gone',
        }
      },
      { token: state.token, url: state.baseURL, ready },
    )
  }
  async function found(page: Page, ready: string) {
    await page.evaluate((ready) => {
      const w = window as unknown as { e2eReady: string; e2eFound?: () => void }
      w.e2eReady = ready
      w.e2eFound?.()
    }, ready)
  }

  test('at the minimum the header, Launch agent, the filter and a pending update sit inside the sidebar', async ({ page, state }) => {
    // The desktop app's bridge with an update ready: the header's widest, its update button beside your menu.
    await desktopApp(page, state, '0.7.0-rc.99')
    await open(page)
    const root = page.locator('#dashboard-sidebar-main')
    await expect(root.locator('[data-header-update]')).toBeVisible()
    await expect.poll(() => width(page)).toBeCloseTo(MIN, 0)
    const header = root.locator('[data-slot="header"]')
    const parts = [
      header.locator('[data-header-update]'),
      header.locator('[data-header-account]'),
      root.getByRole('button', { name: /Launch agent/ }).first(),
      root.getByRole('textbox').first(),
    ]
    for (const part of parts) await fitsSidebar(page, part)
    expect(await header.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true)
  })

  test('at the minimum, with an update pending, the name keeps the width it has without one; wider, the header is as it was (#604)', async ({ page, state }) => {
    await desktopApp(page, state, '')
    await open(page)
    await expect.poll(() => width(page)).toBeCloseTo(MIN, 0)
    const root = page.locator('#dashboard-sidebar-main')
    const header = root.locator('[data-slot="header"]')
    const name = header.locator('[data-sidebar-name]')
    const mark = header.locator('[data-sidebar-mark]')
    const shown = async () => (await name.boundingBox())?.width ?? 0
    // Without an update: the mark, and as much of the name as the row leaves.
    await expect(header.locator('[data-sidebar-menus]')).toBeVisible()
    await expect(header.locator('[data-header-update]')).toHaveCount(0)
    await expect(mark).toBeVisible()
    const without = await shown()
    expect(without).toBeGreaterThan(0)
    const menus = header.locator('[data-sidebar-menus]')
    const menusWithout = (await menus.boundingBox())!.width

    // The app's check finds one: its button joins the header, and the name keeps at least that width, every part inside.
    await found(page, '0.7.0-rc.99')
    await expect(header.locator('[data-header-update]')).toBeVisible()
    expect(await shown()).toBeGreaterThanOrEqual(without)
    for (const part of ['[data-header-update]', '[data-header-account]']) await fitsSidebar(page, header.locator(part))
    expect(await header.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true)

    // As wide as the minimum plus what the update's button takes, the name has its width back with the mark beside it: the
    // mark steps aside only below that.
    const extra = (await menus.boundingBox())!.width - menusWithout
    expect(extra).toBeGreaterThan(0)
    await drag(page, [MIN + Math.ceil(extra)])
    await expect.poll(() => width(page)).toBeCloseTo(MIN + Math.ceil(extra), 0)
    await expect(mark).toBeVisible()
    expect(await shown()).toBeGreaterThanOrEqual(without)

    // Wider, the mark stays beside the name with the update pending, as it always did.
    await drag(page, [300])
    await expect.poll(() => width(page)).toBeCloseTo(300, 0)
    await expect(mark).toBeVisible()
    await expect(header.locator('[data-header-update]')).toBeVisible()
    for (const part of ['[data-header-update]', '[data-header-account]']) await fitsSidebar(page, header.locator(part))
  })
})

// A row at the sidebar's minimum (the UX review's P5): the name on one line across the row, the gutter 8 px, the initials a chip on the
// meta line, the status's word whole at its end, and the question's choices from the row's text edge, each whole.
test('a row fits 200 px', async ({ day, browser }) => {
  const api = day.api
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'plain', name: 'e2e twenty chars xy' })
  try {
    const choices = ['Yes', "Yes, and don't ask again", 'No, tell Claude why']
    const ask = (message: string) => api.ok('POST', `/api/sessions/${s.id}/attention`, { state: 'needs_input', message, options: choices.map((label, i) => ({ label, input: String(i + 1) })) })
    await ask('Edit the file?')
    // A page that is not the session's: its own page would mark the row as the one open.
    const page = await dayPage(browser, { width: 1440, height: 900, path: '/crews' })
    const root = page.locator('#dashboard-sidebar-main')
    await expect.poll(() => root.evaluate((el) => el.getBoundingClientRect().width)).toBeCloseTo(200, 0)
    const row = page.locator(`[data-session-list="sidebar"] [data-sidebar-row="s:${s.id}"]`)
    async function fits(message: string) {
      await expect(row.locator('[data-row-prompt]')).toHaveText(message)
      const name = row.locator('[data-row-name]')
      await expect(name).toHaveText('e2e twenty chars xy')
      await uncut(name)
      const [r, side] = [(await row.boundingBox())!, (await root.boundingBox())!]
      expect(r.x - side.x, "the row's gutter").toBeLessThanOrEqual(9)
      // The initials are a chip on the meta line, below the name's line, not a column before it.
      await inside(row.locator('[data-row-initials]'), row.locator('[data-row-meta-line]'))
      expect((await row.locator('[data-row-initials]').boundingBox())!.y, 'the initials below the name').toBeGreaterThanOrEqual((await name.boundingBox())!.y + (await name.boundingBox())!.height - 1)
      await expect(row.locator('[data-status-word]')).toHaveText('needs you')
      await uncut(row.locator('[data-status-word]'))
      // Below 280 px the question is two lines and its choices are in the panel beside the row (sidebar-question.spec.ts).
      await expect(row.locator('[data-row-prompt]')).toHaveAttribute('data-row-clamped', '')
      await expect(row.locator('[data-row-choice]')).toHaveCount(0)
    }
    await fits('Edit the file?')
    // Drawn again (Needs you folded and unfolded), it still fits. Not by asking again: a second question is posted to the session's
    // chat too, and the row's unread pill then takes the room a name of twenty characters needs (cut by its rule, whole in its title).
    const needs = page.locator('[data-session-list="sidebar"] [data-sidebar-section="needs"]')
    await needs.getByRole('button', { name: /^Needs you/ }).click()
    await expect(needs).toHaveAttribute('data-folded', 'true')
    await needs.getByRole('button', { name: /^Needs you/ }).click()
    await expect(needs).toHaveAttribute('data-folded', 'false')
    await fits('Edit the file?')
    // At 300 px the choices are in the row, from its text edge, each whole.
    const grip = (await page.locator('[role="separator"][aria-controls="dashboard-sidebar-main"]').boundingBox())!
    await page.mouse.move(grip.x + grip.width / 2, grip.y + grip.height / 2)
    await page.mouse.down()
    await page.mouse.move(300, grip.y + grip.height / 2, { steps: 4 })
    await page.mouse.up()
    await expect.poll(() => root.evaluate((el) => el.getBoundingClientRect().width)).toBeCloseTo(300, 0)
    await expect(row.locator('[data-row-prompt]')).not.toHaveAttribute('data-row-clamped', '')
    const name = row.locator('[data-row-name]')
    const buttons = row.locator('[data-row-choice]')
    await expect(buttons).toHaveCount(3)
    expect(Math.abs((await buttons.first().boundingBox())!.x - (await name.boundingBox())!.x), "the first choice at the row's text edge").toBeLessThanOrEqual(2)
    for (let i = 0; i < choices.length; i++) {
      await expect(buttons.nth(i)).toContainText(choices[i]!)
      await uncut(buttons.nth(i).locator('[data-slot="label"]'))
    }
  } finally {
    await api.stopSession(s.id)
    await expect.poll(async () => (await api.session(s.id)).status, { message: 'the row\'s session ends' }).toMatch(/^(exited|stopped)$/)
    await api.stopSession(s.id)
  }
})

// A crew of three in Needs you: only the member that asks is a row; the rest is one line that says how many and in which states, a
// button that shows them all for the visit (a reload folds them again), reached by J and opened by Enter.
test('a run in Needs you shows who asks', async ({ page, api }) => {
  const crew = (
    await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
      name: 'e2e who asks',
      goal: 'be listed',
      cwd: '',
      where: 'server',
      isolation: 'none',
      openAfterLaunch: false,
      members: [
        { name: 'lead', agentId: 'claude', prompt: 'say hello', start: { when: 'immediately' } },
        { name: 'review', agentId: 'codex-untrusted', prompt: 'review it', start: { when: 'immediately' } },
        { name: 'docs', agentId: 'claude', prompt: 'write it up', start: { when: 'immediately' } },
      ],
    })
  ).crew.id
  let run = ''
  try {
    run = (await api.launchCrew(crew)).id
    // review holds the trust question; lead and docs run.
    await expect
      .poll(
        async () => {
          const r = await api.run(run)
          const review = r.members.find((m) => m.name === 'review')?.sessionId
          const others = r.members.filter((m) => m.name !== 'review').map((m) => m.status)
          return `${review ? (await api.session(review)).attention?.state : ''}/${others.join(',')}`
        },
        { timeout: 30_000 },
      )
      .toBe('needs_input/running,running')
    await page.goto('/crews')
    const block = page.locator(`[data-session-list="sidebar"] [data-sidebar-section="needs"] [data-sidebar-run-block="${run}"]`)
    const members = block.locator('ol > [data-row-kind="session"]')
    const more = block.locator('[data-run-more]')
    await expect(more).toHaveText(/^and 2 more · /)
    await expect(members).toHaveCount(1)
    await expect(members.first()).toContainText('review')
    await more.click()
    await expect(members).toHaveCount(3)
    await expect(more).toHaveCount(0)
    // Held for the visit only: a reload folds it again.
    await page.reload()
    await expect(more).toHaveText(/^and 2 more · /)
    await expect(members).toHaveCount(1)
    // From the run's header, J lands on the member, J again on the line; Enter unfolds it, the focus on the first member it held.
    await block.locator('[data-sidebar-run-group]').focus()
    await expect(block.locator('[data-row-focused] [data-sidebar-run-group]')).toHaveCount(1)
    await page.keyboard.press('j')
    await expect(members.first()).toHaveAttribute('data-row-focused', '')
    await page.keyboard.press('j')
    await expect(block.locator('[data-sidebar-row^="m:"]')).toHaveAttribute('data-row-focused', '')
    await page.keyboard.press('Enter')
    await expect(members).toHaveCount(3)
    await expect(block.locator('[data-row-focused]')).toHaveAttribute('data-row-kind', 'session')
    await expect(page).toHaveURL(/\/crews$/)
  } finally {
    if (run) await api.stopRun(run)
    await api.call('DELETE', `/api/crews/${encodeURIComponent(crew)}`)
  }
})

// The pages' one table (utils/pages.ts): a page's sentence is its tooltip in the full sidebar (on hover over its label) and on the rail
// (on hover and on focus, in place of the label alone), and the second line of its row in the palette, which a search reads.
test("a page's tooltip says what it is", async ({ day, browser }) => {
  void day
  const page = await dayPage(browser, { width: 1440, height: 900 })
  const sidebar = page.locator('#dashboard-sidebar-main')
  const tip = (text: string) => page.locator('[data-slot="content"] [data-slot="text"]').filter({ hasText: new RegExp(`^${text}$`) })
  const yardTip = 'Yard: every live terminal on one screen'
  // Needs you's row shows until every store of the one list is read, then leaves when nothing waits: hover once it is in its place.
  await expect(sidebar.locator('nav[data-needs-read="true"]')).toHaveCount(1)
  await sidebar.locator('[data-nav-label]').filter({ hasText: /^Yard$/ }).hover()
  await expect(tip(yardTip)).toBeVisible()
  // Folded to the rail by its key, pressed once the layout that listens for it is drawn (dayPage waits for it).
  await page.mouse.move(1000, 450)
  await page.keyboard.press('Alt+b')
  await expect(sidebar.locator('[data-rail]')).toBeVisible()
  const nav = sidebar.getByRole('navigation').last()
  // From the rail's search button, down its squares and past Needs you's row when it shows (it leaves when nothing waits).
  await tabTo(page, nav.locator('a[href="/yard"]'), 80, sidebar.locator('[data-rail] button[aria-label="Filter sessions"]'))
  await expect(tip(yardTip)).toBeVisible()
  await expect(nav.locator('a[href="/yard"]')).toHaveAttribute('aria-label', /^Yard/)
  // The palette: "terminal" finds the Yard by its sentence.
  await page.keyboard.press('Alt+/')
  const palette = page.locator('[data-palette]')
  await expect(palette).toBeVisible()
  await palette.locator('input[data-palette-input]').fill('terminal')
  const row = palette.locator('[data-palette-row="go:yard"]')
  await expect(row).toBeVisible()
  await expect(row.locator('[data-palette-detail]')).toHaveText('Every live terminal on one screen.')
})

// The tiles' tooltips (#374): each is the page's sentence and its key, a plain tooltip of one line (no wrap of TipButton's). Measured as
// drawn: at the sidebar's default width (its narrowest) in the desktop app's window and in the narrowest window with a sidebar, and
// widened as far as a drag takes it, in both themes, each tile's tooltip shows its sentence whole and inside the window.
test("each sidebar tile's tooltip shows its sentence whole, inside the window, at the sidebar's widths and in both themes", async ({ day, browser }) => {
  void day
  const cases = [
    { width: 1440, height: 900, sidebar: undefined },
    { width: 1024, height: 768, sidebar: undefined },
    { width: 1440, height: 900, sidebar: '23.4' },
  ] as const
  test.setTimeout(test.info().timeout + 2 * cases.length * 12 * 3 * 15_000)
  for (const theme of ['dark', 'light'] as const) {
    for (const c of cases) {
      const at = `${theme}, ${c.width} × ${c.height}, the sidebar ${c.sidebar ? `at ${c.sidebar} rem` : 'at its default width'}`
      const page = await dayPage(browser, { width: c.width, height: c.height, theme })
      if (c.sidebar) {
        await page.evaluate((w) => localStorage.setItem('conductor.sidebar.width', w), c.sidebar)
        await page.reload()
      }
      const sidebar = page.locator('#dashboard-sidebar-main')
      await expect(sidebar.locator('[data-sidebar-tiles]')).toBeVisible()
      const tiles = sidebar.locator('[data-sidebar-tiles] a[data-sidebar-tile]')
      const n = await tiles.count()
      expect(n, `${at}: tiles drawn`).toBeGreaterThan(3)
      for (let i = 0; i < n; i++) {
        const tile = tiles.nth(i)
        const name = await tile.getAttribute('data-sidebar-tile')
        await tile.hover()
        const content = page.locator('[data-slot="content"]').filter({ has: page.getByRole('tooltip', { includeHidden: true }) })
        const text = content.locator('[data-slot="text"]')
        await expect(text, `${at}: ${name}'s tooltip`).toBeVisible()
        const m = await text.evaluate((e) => {
          const box = e.closest('[data-slot="content"]')!.getBoundingClientRect()
          return { sw: e.scrollWidth, cw: e.clientWidth, sh: e.scrollHeight, ch: e.clientHeight, left: box.left, right: box.right, top: box.top, bottom: box.bottom, vw: innerWidth, vh: innerHeight, words: e.textContent }
        })
        expect(m.sw, `${at}: ${name}'s tooltip "${m.words}" is as wide as its box at most`).toBeLessThanOrEqual(m.cw)
        expect(m.sh, `${at}: ${name}'s tooltip is as tall as its box at most`).toBeLessThanOrEqual(m.ch)
        expect([m.left >= 0, m.right <= m.vw, m.top >= 0, m.bottom <= m.vh], `${at}: ${name}'s tooltip inside the window`).toEqual([true, true, true, true])
        await uncut(text)
        await page.mouse.move(c.width - 2, c.height - 2)
        await expect(page.getByRole('tooltip', { includeHidden: true })).toHaveCount(0)
      }
      await page.context().close()
    }
  }
})

// The UX review's P4: Running splits in two. Finished (an agent that ended its turn waits for your next word) sits above Working (an
// agent in a turn, or one that sends no signal), each with its own mark; a run sits once, where its busiest member is.
test('finished and working apart: Finished above Working, each with its own mark, a run once where its busiest member is', async ({ page, api, browser, state }) => {
  test.setTimeout(test.info().timeout + 120_000)
  const w = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e split working' })
  const f = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e split finished' })
  sessions.push(w.id, f.id)
  for (const s of [w, f]) await expect.poll(async () => (await api.session(s.id)).status, { timeout: 30_000 }).toBe('running')
  await api.ok('POST', `/api/sessions/${w.id}/attention`, { state: 'working' })
  await api.ok('POST', `/api/sessions/${f.id}/attention`, { state: 'done' })
  for (const [s, want] of [[w, 'working'], [f, 'done']] as const) await expect.poll(async () => (await api.session(s.id)).attention?.state, { timeout: 15_000 }).toBe(want)

  /** The two rows in their sections of `list` (the sidebar's, or the phone's page), each with its own mark, Finished above Working. */
  const apart = async (p: Page, list: 'sidebar' | 'page') => {
    const section = (key: string) => p.locator(`[data-session-list="${list}"] [data-sidebar-section="${key}"]`)
    const wRow = section('working').locator(`[data-sidebar-row="s:${w.id}"]`)
    const fRow = section('finished').locator(`[data-sidebar-row="s:${f.id}"]`)
    await expect(wRow.locator('[data-status-mark]').first()).toHaveAttribute('data-status-mark', 'working', { timeout: 15_000 })
    await expect(fRow.locator('[data-status-mark]').first()).toHaveAttribute('data-status-mark', 'finished')
    await expect(fRow.locator('[data-status-word]').first()).toHaveText(/^finished( just now| \d+[mhd] ago)?$/)
    await expect(wRow).toHaveAttribute('data-row-state', 'working')
    await expect(fRow).toHaveAttribute('data-row-state', 'finished')
    const [fBox, wBox] = await Promise.all([section('finished').boundingBox(), section('working').boundingBox()])
    expect(fBox!.y, 'Finished is above Working').toBeLessThan(wBox!.y)
  }
  await page.goto('/crews')
  await apart(page, 'sidebar')
  const phone = await dayPage(browser, { server: state, width: 390, height: 844, touch: true, path: '/sessions' })
  await apart(phone, 'page')

  // The working one ends its turn: it moves to Finished, found there by its id (a state that holds).
  await api.ok('POST', `/api/sessions/${w.id}/attention`, { state: 'done' })
  const finished = page.locator('[data-session-list="sidebar"] [data-sidebar-section="finished"]')
  await expect(finished.locator(`[data-sidebar-row="s:${w.id}"]`)).toHaveAttribute('data-row-state', 'finished', { timeout: 15_000 })
  await expect(page.locator(`[data-session-list="sidebar"] [data-sidebar-section="working"] [data-sidebar-row="s:${w.id}"]`)).toHaveCount(0)
  // A fold of Finished survives a reload.
  await finished.getByRole('button', { name: /^Finished/ }).click()
  await expect(finished).toHaveAttribute('data-folded', 'true')
  await page.reload()
  await expect(page.locator('[data-session-list="sidebar"] [data-sidebar-section="finished"]')).toHaveAttribute('data-folded', 'true')
  await page.locator('[data-session-list="sidebar"] [data-sidebar-section="finished"]').getByRole('button', { name: /^Finished/ }).click()
  await expect(page.locator('[data-session-list="sidebar"] [data-sidebar-section="finished"]')).toHaveAttribute('data-folded', 'false')

  // A crew of two, one working and one finished: the run sits in Working once, saying "1 working · 1 finished", nothing of it in Finished.
  const pair = (
    await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
      name: 'e2e split crew',
      goal: 'split',
      cwd: '',
      where: 'server',
      isolation: 'none',
      openAfterLaunch: false,
      members: [
        { name: 'one', agentId: 'claude', prompt: 'work', start: { when: 'immediately' } },
        { name: 'two', agentId: 'claude', prompt: 'rest', start: { when: 'immediately' } },
      ],
    })
  ).crew.id
  let pairRun = ''
  try {
    pairRun = (await api.launchCrew(pair)).id
    const ids: Record<string, string> = {}
    await expect
      .poll(
        async () => {
          const run = await api.run(pairRun)
          for (const m of run.members) if (m.sessionId) ids[m.name] = m.sessionId
          const [a, b] = await Promise.all([ids.one, ids.two].map((id) => (id ? api.session(id).then((s) => s.status) : Promise.resolve(''))))
          return `${a},${b}`
        },
        { timeout: 30_000 },
      )
      .toBe('running,running')
    await api.ok('POST', `/api/sessions/${ids.one}/attention`, { state: 'working' })
    await api.ok('POST', `/api/sessions/${ids.two}/attention`, { state: 'done' })
    for (const [id, want] of [[ids.one!, 'working'], [ids.two!, 'done']] as const) await expect.poll(async () => (await api.session(id)).attention?.state, { timeout: 15_000 }).toBe(want)
    const list = page.locator('[data-session-list="sidebar"]')
    const block = list.locator(`[data-sidebar-section="working"] [data-sidebar-run-block="${pairRun}"]`)
    await expect(block.locator('[data-sidebar-run-group]')).toContainText('1 working · 1 finished', { timeout: 15_000 })
    await expect(list.locator(`[data-sidebar-run-block="${pairRun}"]`)).toHaveCount(1)
    await expect(list.locator(`[data-sidebar-section="finished"] [data-sidebar-run-block="${pairRun}"]`)).toHaveCount(0)
  } finally {
    if (pairRun) await api.stopRun(pairRun)
    await api.call('DELETE', `/api/crews/${encodeURIComponent(pair)}`)
  }
})

test.describe('on a server of its own', () => {
  let own: OwnServer
  test.beforeAll(async () => {
    own = await ownServer('sidebar-split')
  })
  test.afterAll(async () => {
    await own?.stop()
  })

  test('Working and Finished are each drawn only while they have rows', async ({ browser }) => {
    const s = await own.api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e split alone' })
    try {
      await expect.poll(async () => (await own.api.session(s.id)).status, { timeout: 30_000 }).toBe('running')
      await own.api.ok('POST', `/api/sessions/${s.id}/attention`, { state: 'working' })
      await expect.poll(async () => (await own.api.session(s.id)).attention?.state, { timeout: 15_000 }).toBe('working')
      const page = await dayPage(browser, { server: own.started, path: '/crews' })
      const list = page.locator('[data-session-list="sidebar"]')
      await expect(list.locator(`[data-sidebar-section="working"] [data-sidebar-row="s:${s.id}"]`)).toBeVisible()
      await expect(list.locator('[data-sidebar-section="finished"]')).toHaveCount(0)
      await own.api.ok('POST', `/api/sessions/${s.id}/attention`, { state: 'done' })
      await expect.poll(async () => (await own.api.session(s.id)).attention?.state, { timeout: 15_000 }).toBe('done')
      await expect(list.locator(`[data-sidebar-section="finished"] [data-sidebar-row="s:${s.id}"]`)).toBeVisible()
      await expect(list.locator('[data-sidebar-section="working"]')).toHaveCount(0)
    } finally {
      await own.api.stopSession(s.id)
    }
  })
})

// The pages as six tiles (the UX review's P6): the full sidebar's foot is Needs you, Yard, Crews, Dispatch, Brakeman and More in two
// rows of three, and gives the list the room the column of pages took.
test('the pages as six tiles', async ({ day, browser }) => {
  const runs = await day.part('runs')
  await day.part('busy')
  for (const size of [{ width: 1440, height: 900 }, { width: 1024, height: 768 }]) {
    const page = await dayPage(browser, { path: '/crews', ...size })
    const root = page.locator('#dashboard-sidebar-main')
    await expect.poll(() => root.evaluate((el) => el.getBoundingClientRect().width)).toBeCloseTo(200, 0)
    const tiles = page.getByRole('navigation', { name: 'Pages' })
    const tile = (id: string) => tiles.locator(`[data-sidebar-tile="${id}"]`)
    // Six tiles in order, once the server has said it has Brakeman (its slot is held until then).
    await expect(tiles.locator('[data-sidebar-tile]')).toHaveText([/^Needs you/, /^Yard/, /^Crews/, /^Dispatch/, /^Brakeman/, /^More$/])
    // Two rows: the foot is at most 120 px tall; each label whole, each tile inside the sidebar, none over another.
    const foot = await root.locator('[data-slot="footer"]').boundingBox()
    expect(foot!.height, `the foot at ${size.width}x${size.height}`).toBeLessThanOrEqual(120)
    await uncut(tiles.locator('[data-nav-label]'))
    await inside(tiles.locator('[data-sidebar-tile]'), root)
    await noOverlap(tiles)
    const tops = await tiles.locator('[data-sidebar-tile]').evaluateAll((els) => [...new Set(els.map((el) => Math.round(el.getBoundingClientRect().top)))])
    expect(tops).toHaveLength(2)
    // The busy day's list has the room the pages gave up.
    if (size.height === 900) {
      const list = await page.locator('[data-session-list="sidebar"]').boundingBox()
      expect(list!.height).toBeGreaterThanOrEqual(600)
    }
    // More, by a click: Agents and History, each opening its page; the menu is gone after each pick.
    const menu = page.getByRole('menu')
    for (const [name, path] of [['Agents', '/agents'], ['History', '/history']] as const) {
      await tile('more').click()
      await expect(menu.getByRole('menuitem')).toHaveText([/^Agents/, /^History/])
      await menu.getByRole('menuitem', { name: new RegExp(`^${name}`) }).click()
      await expect(menu).toHaveCount(0)
      await expect(page).toHaveURL(new RegExp(`${path}$`))
    }
    // On History, a page of More's, More is lit and no tile is the current page.
    await expect(tile('more')).toHaveAttribute('data-active', '')
    await expect(tiles.locator('[aria-current="page"]')).toHaveCount(0)
    // More by Enter after Tab from Dispatch; Escape closes it.
    await tabTo(page, tile('more'), 2, tile('dispatch'))
    await page.keyboard.press('Enter')
    await expect(menu.getByRole('menuitem')).toHaveText([/^Agents/, /^History/])
    await page.keyboard.press('Escape')
    await expect(menu).toHaveCount(0)
    // A tile's tooltip shows on keyboard focus: its page's sentence and key.
    const tip = page.locator('[data-slot="content"]').filter({ has: page.locator('[data-slot="text"]', { hasText: /^Yard: every live terminal on one screen$/ }) })
    await tabTo(page, tile('yard'), 1, tile('decisions'))
    await expect(tip).toBeVisible()
    await expect(tip.locator('kbd')).toHaveText(['G', 'Y'])
    // A run's page keeps Crews lit; G then H, pressed once the layout listens, still goes to History.
    await page.goto(`/runs/${runs['Ledger migration']}`)
    await expect(root).toBeVisible()
    await expect(tile('crews')).toHaveAttribute('aria-current', 'page')
    await expect(tile('more')).not.toHaveAttribute('data-active', '')
    await page.keyboard.press('g')
    await page.keyboard.press('h')
    await expect(page).toHaveURL(/\/history$/)
    await page.context().close()
  }
})

// Brakeman's slot is held while the server has not said whether it has Brakeman, so that More does not move when the tile fills; on a
// server without it More moves into the slot.
test("Brakeman's slot is held", async ({ day, browser }) => {
  void day
  const page = await dayPage(browser, { path: '/crews' })
  const tiles = page.getByRole('navigation', { name: 'Pages' })
  const more = tiles.locator('[data-sidebar-tile="more"]')
  let release = () => {}
  const held = new Promise<void>((r) => (release = r))
  let asked = 0
  // A standing route before the page loads again: the read of whether the server has Brakeman, held unanswered.
  await page.route(
    (u) => u.pathname === '/api/brakeman',
    async (route) => {
      asked++
      await held
      await route.continue().catch(() => {})
    },
  )
  await page.reload()
  await expect(page.locator('#dashboard-sidebar-main')).toBeVisible()
  await expect.poll(() => asked).toBeGreaterThan(0)
  const cell = tiles.locator('[data-sidebar-tile="held"]')
  await expect(cell).toBeAttached()
  await expect(cell).toBeEmpty()
  await expect(tiles.locator('[data-sidebar-brakeman]')).toHaveCount(0)
  const box = async (l: Locator) => {
    const b = (await l.boundingBox())!
    return { x: Math.round(b.x), y: Math.round(b.y), width: Math.round(b.width), height: Math.round(b.height) }
  }
  const before = await box(more)
  // The cell is a tile's size: the Dispatch tile's.
  const dispatch = await box(tiles.locator('[data-sidebar-tile="dispatch"]'))
  const slot = await box(cell)
  expect({ width: slot.width, height: slot.height }).toEqual({ width: dispatch.width, height: dispatch.height })
  release()
  // Filled: Brakeman's tile in the slot, the cell gone, and More where it was (compared after the fill, not before it).
  await expect(tiles.locator('[data-sidebar-brakeman]')).toBeVisible()
  await expect(cell).toHaveCount(0)
  expect(await box(more)).toEqual(before)
  // A server without Brakeman (no data directory: it answers 503 store_unavailable): More moves into the slot, the cell never left.
  await page.unroute((u) => u.pathname === '/api/brakeman')
  await page.route(
    (u) => u.pathname === '/api/brakeman',
    (route) => route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: { code: 'store_unavailable', message: 'Brakeman needs a data directory' } }) }),
  )
  await page.reload()
  await expect(tiles.locator('[data-sidebar-tile]')).toHaveText([/^Needs you/, /^Yard/, /^Crews/, /^Dispatch/, /^More$/])
  await expect(cell).toHaveCount(0)
  expect(await box(more)).toEqual(slot)
})

/** Nothing of the sidebar spills sideways, and `part` sits whole inside it: at the minimum width every spec runs at (#437). */
async function fitsSidebar(page: Page, part: Locator) {
  const root = page.locator('#dashboard-sidebar-main')
  const sides = await root.evaluate((el) => {
    const r = el.getBoundingClientRect()
    const spill = [el, ...Array.from(el.querySelectorAll('[data-slot="header"], [data-slot="body"], [data-slot="footer"]'))].filter((e) => e.scrollWidth > e.clientWidth)
    return { left: r.left, right: r.right, spill: spill.length }
  })
  expect(sides.spill).toBe(0)
  const box = (await part.boundingBox())!
  expect(box.x).toBeGreaterThanOrEqual(sides.left)
  expect(box.x + box.width).toBeLessThanOrEqual(sides.right)
}
