import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Session } from './fixtures'

// A needs-input report with choices (conductor notify --choices): the
// session page, the wall queue and the sidebar's row show them as buttons,
// and a click types the choice into the session as a line, which the stub's
// transcript holds.
test.describe.configure({ mode: 'serial' })

const sessions: string[] = []

test.afterAll(async ({ api }) => {
  for (const id of sessions) await api.stopSession(id)
})

async function ask(api: { ok: <T>(m: string, p: string, b?: unknown) => Promise<T> }): Promise<Session> {
  // POST /api/sessions answers the session itself (its agent session is set at launch, from the agent's recipe).
  const session = await api.ok<Session>('POST', '/api/sessions', { agentId: 'asker' })
  sessions.push(session.id)
  return session
}

function transcript(home: string, s: Session): string {
  const file = join(home, '.stub-sessions', `${s.agentSession?.id ?? ''}.txt`)
  return existsSync(file) ? readFileSync(file, 'utf8') : ''
}

test('the session page offers the choices and a click types one as a line', async ({ page, api, state }) => {
  const s = await ask(api)
  await expect
    .poll(async () => (await api.session(s.id)).attention?.options?.map((o) => o.label), { timeout: 30_000 })
    .toEqual(['Postgres', 'SQLite', 'Keep both'])
  const info = await api.session(s.id)
  expect(info.attention?.state).toBe('needs_input')
  expect(info.attention?.kind).toBe('prompt')
  expect(info.attention?.message).toBe('Which database?')
  expect(info.attention?.options?.map((o) => o.input)).toEqual(['Postgres\r', 'SQLite\r', 'Keep both\r'])

  await page.goto(`/sessions/${encodeURIComponent(s.id)}`)
  const bar = page.locator('[data-quick-reply]')
  await expect(bar).toBeVisible({ timeout: 30_000 })
  await expect(bar.getByRole('button')).toHaveCount(3)
  await bar.getByRole('button', { name: /Keep both/ }).click()
  await expect.poll(() => transcript(state.home, s), { timeout: 15_000, message: 'the choice was typed' }).toContain('Keep both\n')
  await expect.poll(async () => (await api.session(s.id)).attention?.state, { timeout: 15_000 }).not.toBe('needs_input')
  await expect(bar).toBeHidden()
})

test('the wall queue offers the same buttons', async ({ page, api, state }) => {
  const s = await ask(api)
  await expect.poll(async () => (await api.session(s.id)).attention?.state, { timeout: 30_000 }).toBe('needs_input')
  await page.goto('/yard')
  const card = page.locator(`[data-wall-queue] [data-queue-session="${s.id}"]`)
  await expect(card).toBeVisible({ timeout: 30_000 })
  await card.getByRole('button', { name: /SQLite/ }).click()
  await expect.poll(() => transcript(state.home, s), { timeout: 15_000, message: 'the choice was typed' }).toContain('SQLite\n')
  await expect.poll(async () => (await api.session(s.id)).attention?.state, { timeout: 15_000 }).not.toBe('needs_input')
})

test("the wall queue wraps a question's three choices onto lines of their own: none is cut off (#906)", async ({ page, api }) => {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e three choices' })
  sessions.push(s.id)
  // Three labels as a permission prompt words them: each fits the card alone, together they do not fit one line of it.
  const labels = ['Yes', "Yes, and don't ask again", 'No, tell Claude why']
  await api.ok('POST', `/api/sessions/${s.id}/attention`, { state: 'needs_input', message: 'Run git status?', options: labels.map((label, i) => ({ label, input: String(i + 1) })) })
  await page.goto('/yard')
  const card = page.locator(`[data-wall-queue] [data-queue-session="${s.id}"]`)
  await expect(card).toBeVisible({ timeout: 30_000 })
  const choices = labels.map((label) => card.getByRole('button', { name: label, exact: true }))
  const inside = (await card.boundingBox())!
  for (let i = 0; i < labels.length; i++) {
    const choice = choices[i]!
    await expect(choice).toBeVisible()
    const box = (await choice.boundingBox())!
    expect(box.x, labels[i]).toBeGreaterThanOrEqual(inside.x)
    expect(box.x + box.width, labels[i]).toBeLessThanOrEqual(inside.x + inside.width)
    expect(await choice.locator('[data-slot="label"]').evaluate((e) => e.scrollWidth <= e.clientWidth), labels[i]).toBe(true)
  }
  // A label wider than the card would be shortened: its whole text is the button's title.
  for (let i = 0; i < labels.length; i++) await expect(choices[i]!).toHaveAttribute('title', labels[i]!)
  // More than one line: the third went below the first.
  expect((await choices[2]!.boundingBox())!.y).toBeGreaterThan((await choices[0]!.boundingBox())!.y)
})

test('the sidebar row offers the choices, numbered, and a click types one without opening the session', async ({ page, api, state }) => {
  const s = await ask(api)
  await expect.poll(async () => (await api.session(s.id)).attention?.state, { timeout: 30_000 }).toBe('needs_input')
  await page.goto('/crews')
  const row = page.locator(`[data-session-list="sidebar"] [data-sidebar-row="s:${s.id}"]`).first()
  await expect(row).toHaveAttribute('data-row-state', 'needs', { timeout: 30_000 })
  await expect(row.locator('[data-row-prompt]')).toHaveText('Which database?')
  await expect(row.locator('[data-row-choice]')).toHaveCount(3)
  await expect(row.locator('[data-row-choice="3"]')).toContainText('Keep both')
  await row.locator('[data-row-choice="1"]').click()
  await expect.poll(() => transcript(state.home, s), { timeout: 15_000, message: 'the choice was typed' }).toContain('Postgres\n')
  // The prompt clears and the row moves to Running, its buttons gone; the page never left /crews.
  await expect(row).toHaveAttribute('data-row-state', 'running', { timeout: 15_000 })
  await expect(row.locator('[data-row-choice]')).toHaveCount(0)
  await expect(page).toHaveURL(/\/crews$/)
})
