import type { Locator, Page } from '@playwright/test'
import { expect, test, type Api, type Session } from './fixtures'

// A row's More menu in the sidebar stays open while its row moves between the list's sections. An agent ends its turn (Working to
// Finished), starts another (back to Working), all while a person has the menu open on its row: the row is drawn again in its new
// section, and the menu was the old row's, so it closed under the person's pointer (CI, the clone spec: a Yardmaster attempt's row
// went Finished, Working, Finished as its menu opened). The list keeps the open menu now; only the person closes it.
const sessions: string[] = []
/** The app's one clock ticks this often (NOW_TICK_MS in `app/composables/useNow.ts`, which needs Vue's auto-imports to load). */
const NOW_TICK_MS = 15_000

test.afterAll(async ({ api }) => {
  for (const id of sessions) await api.stopSession(id)
})

/** The session's row in the section `key` of the sidebar's list. */
function rowIn(page: Page, key: 'working' | 'finished', s: Session): Locator {
  return page.locator(`[data-session-list="sidebar"] [data-sidebar-section="${key}"] [data-sidebar-row="s:${s.id}"]`)
}

/** Sets the session's turn through the attention route and waits for the server to hold it. */
async function turn(api: Api, s: Session, state: 'working' | 'done') {
  await api.ok('POST', `/api/sessions/${s.id}/attention`, { state })
  await expect.poll(async () => (await api.session(s.id)).attention?.state, { timeout: 15_000 }).toBe(state)
}

/** The row is drawn in `key` and nowhere else: the move has been drawn. */
async function movedTo(page: Page, key: 'working' | 'finished', s: Session) {
  const other = key === 'working' ? 'finished' : 'working'
  await expect(rowIn(page, key, s)).toHaveAttribute('data-row-state', key, { timeout: 15_000 })
  await expect(rowIn(page, other, s)).toHaveCount(0)
}

/** The menu open on the row as it is drawn in `key`: its More button says so and the menu it controls is shown, with the row's items. */
async function menuOnRow(page: Page, key: 'working' | 'finished', s: Session) {
  const more = rowIn(page, key, s).locator('[data-row-more]')
  await expect(more).toHaveAttribute('aria-expanded', 'true')
  const id = await more.getAttribute('aria-controls')
  expect(id, 'More names the menu it opens').toBeTruthy()
  const menu = page.locator(`[role="menu"][id="${id}"]`)
  await expect(menu).toBeVisible()
  await expect(menu.getByRole('menuitem', { name: /^Rename…/ })).toBeVisible()
  await expect(page.getByRole('menu')).toHaveCount(1)
}

test("a row's More menu stays open while its row moves between Working and Finished, and the clock ticks", async ({ page, api }) => {
  // One tick of the app's clock is waited for below.
  test.setTimeout(test.info().timeout + NOW_TICK_MS)
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e row menu' })
  sessions.push(s.id)
  await expect.poll(async () => (await api.session(s.id)).status, { timeout: 30_000 }).toBe('running')
  await turn(api, s, 'working')
  await page.goto('/crews')
  await movedTo(page, 'working', s)

  // More, on the working row.
  await rowIn(page, 'working', s).hover()
  await rowIn(page, 'working', s).locator('[data-row-more]').click()
  await menuOnRow(page, 'working', s)

  // Its turn ends: the row is drawn in Finished, its menu open on it there.
  await turn(api, s, 'done')
  await movedTo(page, 'finished', s)
  await menuOnRow(page, 'finished', s)

  // Another turn starts, as the attempt's did on CI: back in Working, the menu still open.
  await turn(api, s, 'working')
  await movedTo(page, 'working', s)
  await menuOnRow(page, 'working', s)

  // The clock ticks (the row's age is drawn again): the menu stays.
  const age = rowIn(page, 'working', s).locator('[data-row-meta]')
  const before = await age.textContent()
  await expect(age).not.toHaveText(before ?? '', { timeout: NOW_TICK_MS + 5_000 })
  await menuOnRow(page, 'working', s)

  // The person closes it, and a move after that opens nothing.
  await page.keyboard.press('Escape')
  await expect(page.getByRole('menu')).toHaveCount(0)
  await turn(api, s, 'done')
  await movedTo(page, 'finished', s)
  await expect(rowIn(page, 'finished', s).locator('[data-row-more]')).toHaveAttribute('aria-expanded', 'false')
  await expect(page.getByRole('menu')).toHaveCount(0)
})
