import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Locator } from '@playwright/test'
import { expect, member, test, type Session } from './fixtures'

// A long question (#187): an agent's permission prompt quotes the command,
// so what it asks runs to a couple of hundred characters. It reads whole,
// wrapped over its lines above the choices and never cut to one line with
// an ellipsis: in the sidebar's row, on the Yard's queue card, in the
// session page's reply bar, on the run card of the Crews page, and in the
// phone's list, where a choice still answers. A question at the server's
// bound (500 bytes, the longest any surface shows) reads whole too, and the
// row's geometry never moves under the pointer.
test.describe.configure({ mode: 'serial' })

/** The asker-long stub's question (conductor.e2e.json): a Claude Code permission prompt's length, 200 characters. */
const QUESTION =
  'Claude needs your permission to use Bash: git log --oneline --graph origin/main..HEAD -- internal/session web/app/components (the commits this branch adds, so that the release notes can name each one)'
/** A question at the server's bound (session.MaxAttentionMessage, 500 bytes, then trimmed): the prompt three times, cut at the bound on a letter. */
const LONGEST = [QUESTION, QUESTION, QUESTION].join(' ').slice(0, 500)
/** The asker-long stub's choices, as the attention route takes them (a label typed as a line). */
const CHOICES = ['Yes', "Yes, and don't ask again", 'No'].map((label) => ({ label, input: `${label}\r` }))

const sessions: string[] = []
let crewId = ''
const runs: string[] = []
let asker: Session

test.afterAll(async ({ api }) => {
  for (const id of runs) await api.stopRun(id)
  if (crewId) await api.call('DELETE', `/api/crews/${encodeURIComponent(crewId)}`)
  for (const id of sessions) await api.stopSession(id)
})

function transcript(home: string, s: Session): string {
  const file = join(home, '.stub-sessions', `${s.agentSession?.id ?? ''}.txt`)
  return existsSync(file) ? readFileSync(file, 'utf8') : ''
}

/** An element shows all of its text in a box of its own: nothing cut off to the right (an ellipsis) or below (a clamp). */
async function expectWhole(el: Locator): Promise<void> {
  const m = await el.evaluate((e) => ({ clientWidth: e.clientWidth, scrollWidth: e.scrollWidth, clientHeight: e.clientHeight, scrollHeight: e.scrollHeight }))
  expect(m.clientWidth, `a box of its own: ${JSON.stringify(m)}`).toBeGreaterThan(0)
  expect(m.scrollWidth, `nothing cut off to the right: ${JSON.stringify(m)}`).toBeLessThanOrEqual(m.clientWidth + 1)
  expect(m.scrollHeight, `nothing cut off below: ${JSON.stringify(m)}`).toBeLessThanOrEqual(m.clientHeight + 1)
}

test('the sidebar row shows the whole question, wrapped above its choices', async ({ page, api }) => {
  expect(QUESTION).toHaveLength(200)
  asker = await api.ok<Session>('POST', '/api/sessions', { agentId: 'asker-long', name: 'e2e long question' })
  sessions.push(asker.id)
  await expect.poll(async () => (await api.session(asker.id)).attention?.message, { timeout: 30_000 }).toBe(QUESTION)
  expect((await api.session(asker.id)).attention?.options?.map((o) => o.label)).toEqual(CHOICES.map((c) => c.label))

  await page.goto('/crews')
  const row = page.locator(`[data-session-list="sidebar"] [data-sidebar-row="s:${asker.id}"]`).first()
  await expect(row).toHaveAttribute('data-row-state', 'needs', { timeout: 30_000 })
  const prompt = row.locator('[data-row-prompt]')
  await expect(prompt).toHaveText(QUESTION)
  await expectWhole(prompt)
  // Wrapped over its lines (one line of its text is 16 px), above the numbered buttons.
  const box = (await prompt.boundingBox())!
  expect(box.height).toBeGreaterThan(30)
  await expect(row.locator('[data-row-choice]')).toHaveCount(3)
  const first = (await row.locator('[data-row-choice="1"]').boundingBox())!
  expect(box.y + box.height).toBeLessThanOrEqual(first.y + 1)
})

test("the Yard's queue card and the session page's reply bar show it whole", async ({ page }) => {
  await page.goto('/yard')
  const card = page.locator(`[data-wall-queue] [data-queue-session="${asker.id}"]`)
  await expect(card).toBeVisible({ timeout: 30_000 })
  const words = card.locator('[data-queue-message]')
  await expect(words).toHaveText(QUESTION)
  await expectWhole(words)
  await expect(card.getByRole('button', { name: "Yes, and don't ask again" })).toBeVisible()

  await page.goto(`/sessions/${encodeURIComponent(asker.id)}`)
  const bar = page.locator('[data-quick-reply]')
  await expect(bar).toBeVisible({ timeout: 30_000 })
  const line = bar.locator('[data-quick-reply-message]')
  await expect(line).toHaveText(QUESTION)
  await expectWhole(line)
  await expect(bar.getByRole('button', { name: "Yes, and don't ask again" })).toBeVisible()
})

test("a member's question is whole on the run card of the Crews page", async ({ page, api }) => {
  crewId = (
    await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
      name: 'e2e long question',
      goal: 'ask',
      cwd: '',
      where: 'server',
      isolation: 'none',
      openAfterLaunch: false,
      members: [{ name: 'core', agentId: 'claude', prompt: 'say hello', start: { when: 'immediately' } }],
    })
  ).crew.id
  const run = await api.launchCrew(crewId)
  runs.push(run.id)
  let coreId = ''
  await expect
    .poll(
      async () => {
        const m = member(await api.run(run.id), 'core')
        coreId = m.sessionId ?? ''
        return coreId ? m.status : ''
      },
      { timeout: 30_000 },
    )
    .toBe('running')
  // The member's first turn (its prompt) ends with the stub's Stop hook, which reports done: a question posted before that would
  // be overwritten by it.
  await expect.poll(async () => (await api.session(coreId)).attention?.state, { timeout: 30_000, message: "the member's first turn is done" }).toBe('done')
  await api.ok('POST', `/api/sessions/${encodeURIComponent(coreId)}/attention`, { state: 'needs_input', message: QUESTION, options: CHOICES.slice(0, 2) })
  expect((await api.session(coreId)).attention?.message).toBe(QUESTION)

  await page.goto('/crews')
  const ask = page.locator(`[data-crew-runs] [data-run="${run.id}"] [data-run-ask]`)
  await expect(ask).toContainText(QUESTION, { timeout: 15_000 })
  await expectWhole(ask.locator('[data-run-ask-words]'))
})

test("a question at the server's bound reads whole in the row, and the pointer over it and onto a choice moves nothing", async ({ page, api, browser, state }) => {
  expect(LONGEST).toHaveLength(500)
  await api.ok('POST', `/api/sessions/${encodeURIComponent(asker.id)}/attention`, { state: 'needs_input', message: LONGEST, options: CHOICES })
  expect((await api.session(asker.id)).attention?.message).toBe(LONGEST)
  await page.goto('/crews')
  const row = page.locator(`[data-session-list="sidebar"] [data-sidebar-row="s:${asker.id}"]`).first()
  const prompt = row.locator('[data-row-prompt]')
  await expect(prompt).toHaveText(LONGEST, { timeout: 30_000 })
  await expectWhole(prompt)

  // The pointer over the question and then onto a choice moves nothing: the choice is where it was, under the pointer.
  // At the sidebar's 200 px (#437) the row is taller than the list shows: the choice is brought into view, and the pointer goes
  // over the question where it shows above the choices, since a hover at the question's middle would scroll the list.
  const choice = row.locator('[data-row-choice="3"]')
  await choice.scrollIntoViewIfNeeded()
  const before = (await choice.boundingBox())!
  const pt = { x: before.x + before.width / 2, y: before.y + before.height / 2 }
  const words = (await prompt.boundingBox())!
  const over = { x: words.x + words.width / 2, y: words.y + words.height - 6 }
  await page.mouse.move(over.x, over.y)
  expect(await page.evaluate(({ x, y }) => !!document.elementFromPoint(x, y)?.closest('[data-row-prompt]'), over)).toBe(true)
  expect((await choice.boundingBox())!.y).toBe(before.y)
  await expectWhole(prompt)
  await page.mouse.move(pt.x, pt.y)
  await expectWhole(prompt)
  expect((await choice.boundingBox())!.y).toBe(before.y)
  expect(await page.evaluate(({ x, y }) => document.elementFromPoint(x, y)?.closest('[data-row-choice]')?.getAttribute('data-row-choice'), pt)).toBe('3')

  // The session page's reply bar holds it whole too.
  await page.goto(`/sessions/${encodeURIComponent(asker.id)}`)
  const line = page.locator('[data-quick-reply] [data-quick-reply-message]')
  await expect(line).toHaveText(LONGEST, { timeout: 30_000 })
  await expectWhole(line)

  // A landscape phone in the Yard's focus mode: the terminal's least height and the whole question outgrow the 390 px, and the
  // choices are reached by scrolling (the second review round found them clipped under the panel, out of reach).
  const landscape = await browser.newContext({ viewport: { width: 844, height: 390 }, hasTouch: true, isMobile: true })
  try {
    await landscape.addInitScript(({ token }) => localStorage.setItem('conductor.workbenchToken', token), { token: state.token })
    const yard = await landscape.newPage()
    await yard.goto(`/yard?focus=${encodeURIComponent(asker.id)}`)
    const bar = yard.locator('[data-quick-reply]')
    await expect(bar.locator('[data-quick-reply-message]')).toHaveText(LONGEST, { timeout: 30_000 })
    const no = bar.getByRole('button', { name: 'No' })
    await no.scrollIntoViewIfNeeded()
    const box = (await no.boundingBox())!
    expect(box.y, 'the choice is in the viewport').toBeGreaterThanOrEqual(0)
    expect(box.y + box.height, 'the choice is in the viewport').toBeLessThanOrEqual(390)
    expect(await yard.evaluate(({ x, y }) => document.elementFromPoint(x, y)?.closest('button')?.textContent?.trim(), { x: box.x + box.width / 2, y: box.y + box.height / 2 })).toMatch(/No$/)
  } finally {
    await landscape.close()
  }

  // The stub's own question again, for the phone.
  await api.ok('POST', `/api/sessions/${encodeURIComponent(asker.id)}/attention`, { state: 'needs_input', message: QUESTION, options: CHOICES })
})

test("the phone's list and its session page show it whole, and a choice from the row answers", async ({ browser, api, state }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })
  try {
    await ctx.addInitScript(
      ({ token }) => {
        localStorage.setItem('conductor.workbenchToken', token)
        localStorage.setItem('conductor.displayName', 'Nate')
      },
      { token: state.token },
    )
    const phone = await ctx.newPage()
    await phone.goto(`/sessions/${encodeURIComponent(asker.id)}`)
    const line = phone.locator('[data-quick-reply] [data-quick-reply-message]')
    await expect(line).toHaveText(QUESTION, { timeout: 30_000 })
    await expectWhole(line)

    await phone.goto('/sessions')
    const row = phone.locator(`[data-session-list="page"] [data-sidebar-row="s:${asker.id}"]`).first()
    const prompt = row.locator('[data-row-prompt]')
    await expect(prompt).toHaveText(QUESTION, { timeout: 15_000 })
    await expectWhole(prompt)
    const choice = row.locator('[data-row-choice="3"]')
    await expect(choice).toContainText('No')
    await choice.tap()
    await expect.poll(() => transcript(state.home, asker), { timeout: 15_000, message: 'the choice was typed' }).toContain('No\n')
    await expect.poll(async () => (await api.session(asker.id)).attention?.state, { timeout: 15_000 }).not.toBe('needs_input')
    await expect(row).toHaveAttribute('data-row-state', 'running', { timeout: 15_000 })
  } finally {
    await ctx.close()
  }
})
