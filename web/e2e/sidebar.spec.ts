import { spawn, type ChildProcess } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { existsSync, openSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Locator, Page } from '@playwright/test'
import { endHosted, expect, hostedName, hostedSession, test, type Session } from './fixtures'
import { serverBinary, stubBash, stubPath } from './server'

// The sidebar's list (design 3a, 3b, 3c, 3f): sessions and runs ordered by
// what needs you, a run kept whole where its most urgent member is, a machine
// as a tag on the row, Shared with you below your own, Exited folded to one
// line, every section folding from its header and remembered; the actions on
// a row; a prompt answered in the row; the keys on a focused row; alerts and
// your menu beside the name, the foot holding only the pages; the rail.
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

test('a run sits whole in Needs you: its header, then its members, the one asking first', async ({ page, api }) => {
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
  // Whole, in one section: nothing of it in Running.
  await expect(page.locator(`[data-sidebar-section="running"] [data-sidebar-run-block="${runId}"]`)).toHaveCount(0)
  await expect(block).toHaveAttribute('data-row-state', 'needs')
  const header = block.locator('[data-sidebar-run-group]')
  await expect(header).toContainText('e2e sidebar crew')
  await expect(header).toContainText('2 agents')
  await expect(header).toHaveAttribute('href', `/runs/${encodeURIComponent(runId)}`)
  await expect(block.locator('[data-run-play="needs"]')).toHaveCount(1)
  // The members under a line, the one asking first with its question; no crew, run or path words on the rows.
  const members = block.locator('ol > [data-sidebar-row]')
  await expect(members).toHaveCount(2)
  await expect(members.nth(0)).toHaveAttribute('data-row-state', 'needs')
  await expect(members.nth(0)).toContainText('review')
  await expect(members.nth(0).locator('[data-row-prompt]')).toContainText('Trust this folder?')
  // At the sidebar's minimum (#437) the choices wrap inside it.
  const choices = members.nth(0).locator('[data-row-answer] button')
  // The trust question's two answers, drawn before they are measured: a loop over none would check nothing.
  await expect(choices).toHaveCount(2)
  for (let i = 0; i < (await choices.count()); i++) await fitsSidebar(page, choices.nth(i))
  await expect(members.nth(1)).toContainText('lead')
  await expect(members.nth(1).locator('[data-row-meta]')).toHaveText(/^claude · \d+[smh]/)
  // Other specs leave sessions behind: the counts are checked by shape, not by number.
  await expect(needs.locator('[data-section-count]')).toHaveText(/^\d+$/)
  // The loose session runs on the server: it says so, and nothing says the path.
  const running = page.locator('[data-sidebar-section="running"]').first()
  await expect(running.locator(`[data-sidebar-row="s:${alive.id}"] [data-row-meta]`)).toHaveText(/^claude · server · \d+[smh]/)
  await expect(running.locator(`[data-sidebar-row="s:${alive.id}"]`)).not.toContainText('/tmp/')
  await expect(running.locator('[data-section-count]')).toHaveText(/^· \d+$/)
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
  expect(await exited.locator('[data-section-preview] [data-state="exited"]').count()).toBeGreaterThan(0)
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

  // Running folds from its header, keeps its count and shows what is inside as squares; the fold survives a reload.
  const running = page.locator('[data-sidebar-section="running"]').first()
  await running.getByRole('button', { name: /^Running/ }).click()
  await expect(running).toHaveAttribute('data-folded', 'true')
  await expect(running.locator('[data-section-count]')).toHaveText(/^· \d+$/)
  expect(await running.locator('[data-section-preview] [data-state="running"]').count()).toBeGreaterThan(0)
  await expect(running.locator('[data-sidebar-row]')).toHaveCount(0)
  await page.reload()
  await expect(page.locator('[data-sidebar-section="running"]').first()).toHaveAttribute('data-folded', 'true')
  await expect(page.locator('[data-sidebar-section="exited"]').first()).toHaveAttribute('data-folded', 'false')
  await page.locator('[data-sidebar-section="running"]').first().getByRole('button', { name: /^Running/ }).click()
  await expect(page.locator('[data-sidebar-section="running"]').first()).toHaveAttribute('data-folded', 'false')
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
  await expect(page.locator(`[data-sidebar-section="running"] [data-sidebar-row="s:${alive!.id}"]`).first()).toBeVisible({ timeout: 15_000 })
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

test('a free-text prompt gets a reply field in the row; Enter sends the line and the row moves on', async ({ page, api, state }) => {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e branch' })
  sessions.push(s.id)
  await api.ok('POST', `/api/sessions/${s.id}/attention`, { state: 'needs_input', message: 'Which branch should I base it on?' })
  await page.goto('/crews')
  const row = page.locator(`[data-session-list="sidebar"] [data-sidebar-row="s:${s.id}"]`).first()
  await expect(row).toHaveAttribute('data-row-state', 'needs', { timeout: 30_000 })
  await expect(row.locator('[data-row-prompt]')).toHaveText('Which branch should I base it on?')
  await expect(row.locator('[data-row-choice]')).toHaveCount(0)
  const field = row.locator('[data-row-reply] input, input[data-row-reply]').first()
  await field.fill('main')
  await field.press('Enter')
  await expect.poll(() => transcript(state.home, s), { timeout: 15_000, message: 'the line was typed' }).toContain('main\n')
  await expect(row).toHaveAttribute('data-row-state', 'running', { timeout: 15_000 })
  await expect(row.locator('[data-row-reply]')).toHaveCount(0)
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
  const field = row.locator('[data-row-reply] input, input[data-row-reply]').first()
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
  await expect(rowB).toHaveAttribute('data-row-state', 'running', { timeout: 15_000 })
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

test('alerts and your menu sit beside the name; the foot holds only the pages', async ({ page }) => {
  await page.goto('/crews')
  await expect(page.locator('[data-sidebar-tools]')).toHaveCount(0)
  await page.locator('[data-header-alerts]').click()
  await expect(page.getByText('Browser notification')).toBeVisible()
  await page.keyboard.press('Escape')
  await page.locator('[data-header-account]').click()
  await expect(page.getByRole('menuitem')).toHaveText([/Your name/, /Keyboard shortcuts/, /Toggle theme/, /Workbench token/, /Forget token/])
  await page.getByRole('menuitem', { name: /Your name/ }).click()
  const dialog = page.locator('[data-name-dialog]')
  await dialog.getByRole('textbox', { name: 'Your name' }).fill('Nate R')
  await dialog.getByRole('button', { name: 'Save' }).click()
  await expect(dialog).toBeHidden()
  await expect(page.locator('[data-header-account]')).toHaveAttribute('aria-label', 'Your menu, Nate R')
  // The foot: the pages, nothing else; Needs your decision's Decide row first (docs/plans/decision-inbox.md, PR-2).
  const foot = page.locator('#dashboard-sidebar-main').getByRole('navigation').last()
  await expect(foot.getByRole('link')).toHaveText([/Decide/, /Yard/, /Roundhouse/, /Agents/, /Crews/, /Dispatch/, /Events/, /History/, /Brakeman/])
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
  await page.locator('[data-sidebar-collapse]').click()
  const rail = page.locator('[data-rail]')
  await expect(rail).toBeVisible()
  await expect(rail.locator(`[data-rail-session="${marked.id}"]`)).toBeVisible()
  // A badge the Events page routes to the sidebar, arriving live (badges come from the stream the page watches).
  await api.ok('POST', `/api/sessions/${marked.id}/attention`, { state: 'done', message: 'finished the sweep' })
  await expect(rail.locator('[data-rail-count="needs"]')).toHaveText(/^[1-9]\d*$/)
  await expect(rail.locator('[data-rail-count="news"]')).toHaveText(/^[1-9]\d*$/)
  // The run as a capsule, its members inside, the play icon amber while review asks; the tooltip in words.
  const capsule = rail.locator(`[data-rail-run="${run2}"]`)
  await expect(capsule.locator('[data-rail-member]')).toHaveCount(2)
  await expect(capsule.locator('[data-rail-play="needs"]')).toBeVisible()
  await expect(capsule.locator('a').first()).toHaveAttribute('aria-label', /^e2e rail crew · run started \d\d:\d\d · review needs you · lead running$/)
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

// The full sidebar's width (#437): it opens at its minimum, 12.5 rem (200 px), on every screen; a drag widens it up to 26 % of
// the window and no narrower than the minimum, and on toward the window's edge gives the rail. Each case has its own page, so
// its own browser context and storage.
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
    await page.locator('[data-sidebar-collapse]').click()
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
    await drag(page, [260])
    await expect.poll(() => saved(page)).toBe('16.25')
    await page.reload()
    await expect(handle(page)).toBeAttached()
    await expect.poll(() => width(page)).toBeCloseTo(260, 0)
  })

  test('resizing the window keeps the width within the new bounds', async ({ page }) => {
    await open(page)
    await drag(page, [900])
    await expect.poll(() => width(page)).toBeCloseTo(374.4, 0)
    // At 1024 px the most is 26 %, 266 px; a drag there stops at it too.
    await page.setViewportSize({ width: 1024, height: 900 })
    await expect.poll(() => width(page)).toBeCloseTo(266.24, 0)
    await drag(page, [600])
    await expect.poll(() => width(page)).toBeCloseTo(266.24, 0)
    // Wider again: the width stays what it was, the drag's bound grows back.
    await page.setViewportSize({ width: 2560, height: 1440 })
    await expect.poll(() => width(page)).toBeCloseTo(266.24, 0)
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
    // The desktop app's bridge with an update ready: the header's widest, its update button beside Alerts and your menu.
    await desktopApp(page, state, '0.7.0-rc.99')
    await open(page)
    const root = page.locator('#dashboard-sidebar-main')
    await expect(root.locator('[data-header-update]')).toBeVisible()
    await expect.poll(() => width(page)).toBeCloseTo(MIN, 0)
    const header = root.locator('[data-slot="header"]')
    const parts = [
      header.locator('[data-header-update]'),
      header.locator('[data-header-alerts]'),
      header.locator('[data-header-account]'),
      header.locator('[data-sidebar-collapse]'),
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
    for (const part of ['[data-header-update]', '[data-header-alerts]', '[data-header-account]', '[data-sidebar-collapse]']) await fitsSidebar(page, header.locator(part))
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
    for (const part of ['[data-header-update]', '[data-header-alerts]', '[data-header-account]', '[data-sidebar-collapse]']) await fitsSidebar(page, header.locator(part))
  })
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
