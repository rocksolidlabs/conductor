import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Session } from './fixtures'

// The palette's reach (#303): ] shows the pill and 1 answers without
// navigating; ?tab= opens the inspector's view, ?view= the run's view,
// ?filter= the Yard's chips; S shares and X asks to stop the session a page
// is on; I types a line into a running session; Answer all answers the same
// question asked by several sessions; Stop every session that is done asks
// with their names; the launch verb opens the Launch dialog prefilled.
test.describe.configure({ mode: 'serial' })

const sessions: string[] = []
const runs: string[] = []
let crewId = ''

test.afterAll(async ({ api }) => {
  for (const id of runs) await api.stopRun(id)
  for (const id of sessions) await api.stopSession(id)
  if (crewId) await api.call('DELETE', `/api/crews/${encodeURIComponent(crewId)}`)
})

function transcript(home: string, s: Session): string {
  const file = join(home, '.stub-sessions', `${s.agentSession?.id ?? ''}.txt`)
  return existsSync(file) ? readFileSync(file, 'utf8') : ''
}

test('] shows the pill for the next session that needs you; 1 answers it from the page without leaving; [ goes back', async ({ page, api, state }, info) => {
  const a = await api.ok<Session>('POST', '/api/sessions', { agentId: 'asker', name: 'e2e pill a' })
  const b = await api.ok<Session>('POST', '/api/sessions', { agentId: 'asker', name: 'e2e pill b' })
  sessions.push(a.id, b.id)
  await expect.poll(async () => `${(await api.session(a.id)).attention?.state}/${(await api.session(b.id)).attention?.state}`, { timeout: 30_000 }).toBe('needs_input/needs_input')
  // A press shows the pill for four seconds (PILL_MS, proved in useNeedsCursor.test.ts): the product's bound, not this test's.
  // On a loaded machine the reads below and the next key took longer, the pill went, and the next ] started over on the first
  // (#281); here it lives as long as the test may, and where the test needs it gone it is dismissed.
  await page.addInitScript((ms) => ((window as Window & { conductorPillMs?: number }).conductorPillMs = ms), info.timeout)
  await page.goto('/agents')
  await expect(page.locator('[data-session-list]')).toBeVisible({ timeout: 30_000 })
  // The page's own list has both waiting before the first ]: a ] before the page knows them shows nothing.
  const row = (s: Session) => page.locator(`[data-session-list] [data-sidebar-row="s:${s.id}"]`).first()
  for (const s of [a, b]) await expect(row(s)).toHaveAttribute('data-row-state', 'needs', { timeout: 30_000 })
  const pill = page.locator('[data-needs-pill]')
  await page.keyboard.press(']')
  await expect(pill).toBeVisible()
  await expect(pill.locator('[data-pill-count]')).toHaveText('1 of 2')
  await expect(pill.locator('[data-pill-message]')).toHaveText('Which database?')
  await expect(pill.locator('[data-pill-choice="3"]')).toContainText('Keep both')
  const first = await pill.getAttribute('data-pill-session')
  await page.keyboard.press(']')
  await expect(pill.locator('[data-pill-count]')).toHaveText('2 of 2')
  expect(await pill.getAttribute('data-pill-session')).not.toBe(first)
  await page.keyboard.press('[')
  await expect(pill.locator('[data-pill-count]')).toHaveText('1 of 2')
  expect(await pill.getAttribute('data-pill-session')).toBe(first)
  const shown = first === a.id ? a : b
  const other = first === a.id ? b : a
  // While the pill shows, a key typed into another surface that owns its keys is that surface's (Codex's round on #303): a
  // digit typed into the palette's input is the query, no answer goes anywhere.
  await page.keyboard.press('Control+k')
  const palette = page.locator('[data-palette]')
  await expect(palette).toBeVisible()
  // Its input takes the focus a frame after it shows: a 2 typed before that is the pill's, an answer.
  await expect(palette.locator('input[data-palette-input]')).toBeFocused()
  await page.keyboard.type('2')
  await expect(palette.locator('input[data-palette-input]')).toHaveValue('2')
  expect(transcript(state.home, shown)).not.toContain('SQLite')
  expect(transcript(state.home, other)).not.toContain('SQLite')
  await page.keyboard.press('Escape')
  await expect(palette).toBeHidden()
  // The palette took that Escape; the next, from the page, dismisses the pill.
  await expect(pill).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(pill).toBeHidden()
  // The pill gone, the cursor starts over: [ from nothing is the last, [ again the one before it.
  await page.keyboard.press('[')
  await expect(pill).toBeVisible()
  await expect(pill.locator('[data-pill-count]')).toHaveText('2 of 2')
  await page.keyboard.press('[')
  await expect(pill.locator('[data-pill-count]')).toHaveText('1 of 2')
  expect(await pill.getAttribute('data-pill-session')).toBe(first)
  await page.keyboard.press('1')
  await expect.poll(() => transcript(state.home, shown), { timeout: 15_000, message: 'the choice was typed' }).toContain('Postgres\n')
  expect(transcript(state.home, other)).not.toContain('Postgres')
  await expect(pill).toBeHidden()
  await expect(page).toHaveURL(/\/agents$/)
  // The one left: Escape dismisses the pill, Enter opens the session.
  await expect.poll(async () => (await api.session(shown.id)).attention?.state, { timeout: 15_000 }).not.toBe('needs_input')
  await expect(row(shown)).not.toHaveAttribute('data-row-state', 'needs')
  await page.keyboard.press(']')
  await expect(pill.locator('[data-pill-count]')).toHaveText('1 of 1')
  await page.keyboard.press('Escape')
  await expect(pill).toBeHidden()
  await page.keyboard.press(']')
  await expect(pill).toBeVisible()
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(new RegExp(`/sessions/${other.id}$`))
  await expect(pill).toBeHidden()
})

test('?tab= opens the inspector on that view, ?filter= the Yard on that chip, ?view= the run on that view', async ({ page, api }) => {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e reach tabs' })
  sessions.push(s.id)
  await page.goto(`/sessions/${s.id}?tab=chat`)
  await expect(page.locator('[data-inspector] [data-rail-item="chat"]')).toHaveAttribute('aria-pressed', 'true', { timeout: 30_000 })
  await page.goto(`/sessions/${s.id}?tab=files`)
  await expect(page.locator('[data-inspector] [data-rail-item="explorer"]')).toHaveAttribute('aria-pressed', 'true', { timeout: 30_000 })
  await page.goto('/yard?filter=running')
  await expect(page.locator('[data-yard-filter="running"]')).toHaveAttribute('data-active', '', { timeout: 30_000 })
  await expect(page.locator('[data-yard-filter="all"]')).not.toHaveAttribute('data-active', '')
  crewId = (
    await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
      name: 'e2e reach crew',
      goal: 'the run view by its link',
      cwd: '',
      where: 'server',
      isolation: 'none',
      openAfterLaunch: false,
      yolo: false,
      members: [{ name: 'lead', agentId: 'claude', prompt: 'say hello', start: { when: 'immediately' } }],
    })
  ).crew.id
  const run = await api.ok<{ run: { id: string } }>('POST', `/api/crews/${encodeURIComponent(crewId)}/launch`, {})
  runs.push(run.run.id)
  await page.goto(`/runs/${encodeURIComponent(run.run.id)}?view=graph`)
  await expect(page.locator('[data-run-graph]')).toBeVisible({ timeout: 30_000 })
  await page.goto(`/runs/${encodeURIComponent(run.run.id)}?view=timeline`)
  await expect(page.locator('[data-run-timeline-view]')).toBeVisible({ timeout: 30_000 })
})

test('on a session page S shares and X asks to stop it in the palette; I types a line into a running session; Stop every session that is done asks with their names', async ({ page, api, state }) => {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e reach keys' })
  sessions.push(s.id)
  await page.goto(`/sessions/${s.id}`)
  const terminal = page.locator('.terminal-host .xterm-helper-textarea').first()
  await expect(terminal).toBeFocused({ timeout: 30_000 })
  // Alt+X from the terminal: the palette opens on the page's Stop row, asking; Escape leaves the row, Escape closes.
  await page.keyboard.press('Alt+x')
  const palette = page.locator('[data-palette]')
  await expect(palette).toBeVisible()
  const stop = palette.locator('[data-palette-row="page:stop"]')
  await expect(stop.locator('[data-palette-ask]')).toContainText('Stop e2e reach keys?')
  await page.keyboard.press('Escape')
  await expect(stop.locator('[data-palette-ask]')).toHaveCount(0)
  await page.keyboard.press('Escape')
  await page.keyboard.press('Escape')
  await expect(palette).toBeHidden()
  expect((await api.session(s.id)).status).toBe('running')
  // S outside the terminal: the Share dialog, which makes a link as it opens.
  await page.locator('[data-session-list]').click({ position: { x: 5, y: 5 } })
  await page.keyboard.press('s')
  await expect(page.locator('[data-share-created]')).toBeVisible({ timeout: 15_000 })
  await page.keyboard.press('Escape')
  await expect(page.locator('[data-share-created]')).toBeHidden()
  // I on a running row: the field takes a line, Enter types it into the session (the stub records it).
  await page.goto('/agents')
  await expect(page.locator('[data-session-list]')).toBeVisible({ timeout: 30_000 })
  await page.keyboard.press('Control+k')
  await palette.locator('input[data-palette-input]').fill('e2e reach keys')
  const row = palette.locator(`[data-palette-row="session:${s.id}"]`)
  await expect(row).toBeVisible()
  await page.keyboard.press('ArrowDown')
  await expect(row).toHaveAttribute('data-palette-focused', '')
  await page.keyboard.press('i')
  const field = row.locator('[data-palette-field] input')
  await expect(field).toBeFocused()
  await field.fill('typed from the palette')
  await page.keyboard.press('Enter')
  await expect(palette).toBeHidden()
  await expect.poll(() => transcript(state.home, s), { timeout: 15_000, message: 'the line was typed' }).toContain('typed from the palette\n')
  // The owner's word (#322): the line was entered, said as typed.
  await expect(page.locator('li').filter({ hasText: 'Typed into e2e reach keys' }).last()).toBeVisible({ timeout: 15_000 })
  // The stub answers and reports done: Stop every session that is done names it and asks; Enter stops it.
  await expect.poll(async () => (await api.session(s.id)).attention?.state, { timeout: 30_000 }).toBe('done')
  await page.keyboard.press('Control+k')
  await palette.locator('input[data-palette-input]').fill('> stop every session')
  const every = palette.locator('[data-palette-row="action:stop-done"]')
  await expect(every).toBeVisible()
  await page.keyboard.press('ArrowDown')
  await expect(every).toHaveAttribute('data-palette-focused', '')
  await page.keyboard.press('Enter')
  await expect(every.locator('[data-palette-ask]')).toContainText('e2e reach keys')
  await page.keyboard.press('Enter')
  await expect(palette).toBeHidden()
  await expect.poll(async () => (await api.session(s.id)).status, { timeout: 15_000 }).toMatch(/stopped|exited/)
})

test('Answer all answers the same question asked by several sessions, one send each; the launch verb opens the Launch dialog prefilled', async ({ page, api, state }) => {
  const a = await api.ok<Session>('POST', '/api/sessions', { agentId: 'asker', name: 'e2e answer all a' })
  const b = await api.ok<Session>('POST', '/api/sessions', { agentId: 'asker', name: 'e2e answer all b' })
  sessions.push(a.id, b.id)
  await expect.poll(async () => `${(await api.session(a.id)).attention?.state}/${(await api.session(b.id)).attention?.state}`, { timeout: 30_000 }).toBe('needs_input/needs_input')
  await page.goto('/agents')
  await expect(page.locator('[data-session-list]')).toBeVisible({ timeout: 30_000 })
  await page.keyboard.press('Control+k')
  const palette = page.locator('[data-palette]')
  const all = palette.locator('[data-palette-group="needs"] [data-palette-row^="answer-all:"]').first()
  await expect(all).toBeVisible()
  await expect(all).toContainText('Answer all')
  await expect(all.locator('[data-palette-detail]')).toContainText('Which database?')
  await page.keyboard.press('ArrowDown')
  await expect(all).toHaveAttribute('data-palette-focused', '')
  await page.keyboard.press('2')
  await expect.poll(() => transcript(state.home, a), { timeout: 15_000, message: 'a got the choice' }).toContain('SQLite\n')
  await expect.poll(() => transcript(state.home, b), { timeout: 15_000, message: 'b got the choice' }).toContain('SQLite\n')
  await expect(palette.locator('[data-palette-group="needs"]')).toHaveCount(0, { timeout: 15_000 })
  // launch <agent> in <dir>: the Launch dialog, the agent and the directory filled in.
  await palette.locator('input[data-palette-input]').fill('launch claude in /tmp/reach')
  const verb = palette.locator('[data-palette-row^="verb:launch:"]')
  await expect(verb).toBeVisible()
  await page.keyboard.press('ArrowDown')
  await page.keyboard.press('Enter')
  await expect(palette).toBeHidden()
  await expect(page.getByLabel('Working directory')).toHaveValue('/tmp/reach', { timeout: 15_000 })
  await page.keyboard.press('Escape')
})
