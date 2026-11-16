import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import type { Locator } from '@playwright/test'
import { expect, test, useTheme, type Session } from './fixtures'

// The collapsed sidebar (#314): the session open on the page carries the
// selection mark on its square (a primary bar down the rail's edge beside
// it, clear of the corner badges), and two squares of one agent never look
// the same: in different folders they show their folders' letters, in the
// same folder an ordinal top left as well. The mark reads in both colour
// modes. The suite shares one server, so the folders here start with
// letters no other spec uses, and nothing is assumed about the other
// squares on the rail.
async function box(l: Locator) {
  const b = await l.boundingBox()
  if (!b) throw new Error('not drawn')
  return b
}
const apart = (a: { x: number; y: number; width: number; height: number }, b: { x: number; y: number; width: number; height: number }) => a.x + a.width <= b.x || b.x + b.width <= a.x || a.y + a.height <= b.y || b.y + b.height <= a.y

test('the open session is marked on the rail, and two squares of one agent differ', async ({ page, api, state }) => {
  const q1 = join(state.root, 'q1-rail')
  const q2 = join(state.root, 'q2-rail')
  const q3 = join(state.root, 'q3-rail')
  mkdirSync(q1, { recursive: true })
  mkdirSync(q2, { recursive: true })
  mkdirSync(q3, { recursive: true })
  // Two Claude Code sessions in one folder, one in another, a Codex in the first and a Codex in a third. The second Codex is the
  // spec's own: a Codex alone on the rail keeps its initials (CX), so without it the first one's letters hung on whatever Codex
  // another spec had running at the time.
  const a = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e rail a', cwd: q1 })
  const b = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e rail b', cwd: q1 })
  const c = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e rail c', cwd: q2 })
  const x = await api.ok<Session>('POST', '/api/sessions', { agentId: 'codex', name: 'e2e rail codex', cwd: q1 })
  const y = await api.ok<Session>('POST', '/api/sessions', { agentId: 'codex', name: 'e2e rail codex 2', cwd: q3 })
  try {
    await page.goto(`/sessions/${a.id}`)
    // Folded by its key, pressed once the layout that listens for it has drawn the list.
    await expect(page.locator('[data-session-list="sidebar"]')).toBeVisible()
    await page.keyboard.press('Alt+b')
    const rail = page.locator('[data-rail]')
    await expect(rail).toBeVisible()
    const square = (id: string) => rail.locator(`[data-rail-session="${id}"]`)
    const letters = (id: string) => square(id).locator('[data-avatar-label]')
    // The selection mark on the open session alone; aria-current with it.
    await expect(square(a.id).locator('[data-rail-current]')).toBeVisible()
    await expect(square(a.id)).toHaveAttribute('aria-current', 'page')
    await expect(square(b.id).locator('[data-rail-current]')).toHaveCount(0)
    await expect(square(c.id).locator('[data-rail-current]')).toHaveCount(0)
    // The two in q1-rail share its letters and get ordinals; the one in q2-rail shows Q2 alone; Codex in q1-rail shows Q1 with an
    // ordinal of its own (three Q1 squares), never the same as either Claude Code's; the other Codex shows Q3 alone.
    await expect(letters(a.id)).toHaveText('Q1')
    await expect(letters(b.id)).toHaveText('Q1')
    await expect(letters(c.id)).toHaveText('Q2')
    await expect(letters(x.id)).toHaveText('Q1')
    await expect(letters(y.id)).toHaveText('Q3')
    await expect(square(c.id).locator('[data-rail-ordinal]')).toHaveCount(0)
    await expect(square(y.id).locator('[data-rail-ordinal]')).toHaveCount(0)
    const ordinals = await Promise.all([a, b, x].map((s) => square(s.id).locator('[data-rail-ordinal]').textContent()))
    expect(ordinals.map((o) => Number(o)).sort()).toEqual([1, 2, 3])
    // The bar stands in the rail's margin, inside the rail's box and clear of the ordinal badge in the square's top-left corner and of
    // the place the origin tile takes bottom left (a hosted session's laptop, a share's globe: 14 px, 4 px out of the square).
    const bar = await box(square(a.id).locator('[data-rail-current]'))
    const sessions = await box(rail.locator('[data-rail-sessions]'))
    const sq = await box(square(a.id))
    expect(bar.x).toBeGreaterThanOrEqual(sessions.x)
    expect(bar.height).toBeGreaterThanOrEqual(16)
    expect(apart(bar, await box(square(a.id).locator('[data-rail-ordinal]')))).toBe(true)
    expect(apart(bar, { x: sq.x - 4, y: sq.y + sq.height - 10, width: 14, height: 14 })).toBe(true)
    // Moving to the other session moves the mark.
    await square(b.id).click()
    await expect(page).toHaveURL(new RegExp(`/sessions/${b.id}$`))
    await expect(square(b.id).locator('[data-rail-current]')).toBeVisible()
    await expect(square(a.id).locator('[data-rail-current]')).toHaveCount(0)
    // The mark is drawn in both colour modes (a colour, never transparent), each mode set the way the app keeps it and seen on
    // `html` before the colour is read: a mode the app did not take would read the other's colour twice.
    for (const mode of ['dark', 'light'] as const) {
      await useTheme(page, mode, '[data-rail]')
      if (mode === 'dark') await expect(page.locator('html'), mode).toHaveClass(/\bdark\b/)
      else await expect(page.locator('html'), mode).not.toHaveClass(/\bdark\b/)
      const bg = await square(b.id).locator('[data-rail-current]').evaluate((el) => getComputedStyle(el).backgroundColor)
      expect(bg, mode).not.toMatch(/rgba\(\d+, \d+, \d+, 0\)|transparent/)
    }
  } finally {
    for (const s of [a, b, c, x, y]) await api.stopSession(s.id)
  }
})
