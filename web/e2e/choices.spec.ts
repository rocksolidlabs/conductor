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

test("the wall queue wraps a question's choices onto lines of their own: none is cut off, a shortened one whole in its title (#906)", async ({ page, api }) => {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e three choices' })
  sessions.push(s.id)
  // Three labels as a permission prompt words them: each fits the card alone, together they do not fit one line of it.
  const labels = ['Yes', "Yes, and don't ask again", 'No, tell Claude why']
  await api.ok('POST', `/api/sessions/${s.id}/attention`, { state: 'needs_input', message: 'Run git status?', options: labels.map((label, i) => ({ label, input: String(i + 1) })) })
  await page.goto('/yard')
  const card = page.locator(`[data-wall-queue] [data-queue-session="${s.id}"]`)
  await expect(card).toBeVisible({ timeout: 30_000 })
  const choice = (label: string) => card.getByRole('button', { name: label, exact: true })
  /** Each choice inside the card; whole unless `cut` names it, which is shortened and inside the card all the same. */
  async function inside(all: string[], at: string, cut?: string) {
    const box = (await card.boundingBox())!
    for (const label of all) {
      await expect(choice(label), `${label} ${at}`).toBeVisible()
      const b = (await choice(label).boundingBox())!
      expect(b.x, `${label} ${at}`).toBeGreaterThanOrEqual(box.x)
      expect(b.x + b.width, `${label} ${at}`).toBeLessThanOrEqual(box.x + box.width)
      expect(await choice(label).locator('[data-slot="label"]').evaluate((e) => e.scrollWidth <= e.clientWidth), `${label} whole ${at}`).toBe(label !== cut)
    }
  }
  await inside(labels, 'as the card shows')
  // A label wider than the card would be shortened: its whole text is the button's title.
  for (const label of labels) await expect(choice(label)).toHaveAttribute('title', label)
  // More than one line: the third went below the first.
  expect((await choice(labels[2]!).boundingBox())!.y).toBeGreaterThan((await choice(labels[0]!).boundingBox())!.y)

  // The question asked again with a choice that changes the permission mode, longer than the card (the server's 60 runes at most): the
  // card is drawn again; every choice is still inside it, and the long one, shortened, has its words and what it does in its title,
  // as the sidebar's prompt says them.
  const mode = 'Yes, and switch to BYPASS PERMISSIONS (no further prompts)'
  await api.ok('POST', `/api/sessions/${s.id}/attention`, { state: 'needs_input', message: 'Run git status again?', options: [...labels, mode].map((label, i) => ({ label, input: String(i + 1), mode: label === mode })) })
  await expect(card.locator('[data-queue-message]')).toHaveText('Run git status again?')
  await expect(card.locator('button[data-choice-mode]')).toHaveCount(1)
  await inside([...labels, mode], 'asked again', mode)
  await expect(choice(mode)).toHaveAttribute('title', `${mode} (changes the session's permission mode)`)
  for (const label of labels) await expect(choice(label)).toHaveAttribute('title', label)
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
