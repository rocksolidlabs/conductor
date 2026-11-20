import type { Browser, Page } from '@playwright/test'
import { expect, test, useTheme, type Api, type Session } from './fixtures'

// The app's screens' state kept by the server (docs/plans/sync-seams.md, OS-2): a layout, a palette's recents and a pin set in one
// browser context are there in a second one, through the server; a pin survives a reload; a document past its bound is refused and
// the page goes on. Every test starts from a fresh server's documents (the fixtures' uiStateFresh).

/** A page of its own browser context (its own local storage), with the workbench token, its layout drawn. */
async function contextPage(browser: Browser, token: string): Promise<Page> {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const page = await ctx.newPage()
  await page.addInitScript((t) => localStorage.setItem('conductor.workbenchToken', t), token)
  await page.goto('/')
  await expect(page.locator('#dashboard-sidebar-main')).toBeVisible()
  return page
}

/** The server's document, as GET /api/ui/{name} answers it. */
async function doc(api: Api, name: string): Promise<Record<string, unknown>> {
  return (await api.ok<Record<string, unknown>>('GET', `/api/ui/${name}`)) ?? {}
}

test.describe('the app screens\' state', () => {
  let session: Session | undefined

  test.afterEach(async ({ api }) => {
    if (session) await api.stopSession(session.id)
    session = undefined
  })

  test('a fold, a recent and a pin made in one browser are there in another, and the pin survives a reload', async ({ browser, api, state }) => {
    session = await api.ok<Session>('POST', '/api/sessions', { agentId: 'plain', name: 'e2e ui state' })
    const id = session.id
    const a = await contextPage(browser, state.token)
    const row = a.locator(`[data-sidebar-row="s:${id}"]`).first()
    await expect(row).toBeVisible({ timeout: 15_000 })

    // The pin, from the row's menu.
    await row.click({ button: 'right' })
    await a.getByRole('menuitem', { name: 'Pin to top' }).click()
    const pinnedA = a.locator('[data-sidebar-section="pinned"]')
    await expect(pinnedA.locator(`[data-pinned-session="${id}"]`)).toBeVisible()
    await expect.poll(async () => JSON.stringify((await doc(api, 'pins')).pins ?? []), { timeout: 15_000 }).toContain(id)

    // A fold: the section the session is in, folded from its header.
    const section = a.locator('[data-sidebar-section]').filter({ has: a.locator(`[data-sidebar-row="s:${id}"]`) }).last()
    const key = (await section.getAttribute('data-sidebar-section'))!
    await section.locator('button[aria-expanded]').first().click()
    await expect(a.locator(`[data-sidebar-section="${key}"]`)).toHaveAttribute('data-folded', 'true')
    await expect.poll(async () => JSON.stringify(((await doc(api, 'layout')).sidebar as { folds?: object } | undefined)?.folds ?? {}), { timeout: 15_000 }).toContain(`"${key}":true`)

    // A recent: the session opened from the palette.
    await a.keyboard.press('Control+k')
    const palette = a.locator('[data-palette]')
    await palette.locator('input[data-palette-input]').fill('e2e ui state')
    const prow = palette.locator(`[data-palette-row="session:${id}"]`)
    await expect(prow).toBeVisible({ timeout: 15_000 })
    await prow.click()
    await expect(a).toHaveURL(new RegExp(`/sessions/${id}`))
    await expect.poll(async () => Object.keys(((await doc(api, 'palette')).recents as object | undefined) ?? {}), { timeout: 15_000 }).toContain(`session:${id}`)

    // Another browser, its own storage empty: the server's documents.
    const b = await contextPage(browser, state.token)
    await expect(b.locator('[data-sidebar-section="pinned"]').locator(`[data-pinned-session="${id}"]`)).toBeVisible({ timeout: 15_000 })
    await expect(b.locator(`[data-sidebar-section="${key}"]`)).toHaveAttribute('data-folded', 'true')
    await expect.poll(() => b.evaluate(() => Object.keys(JSON.parse(localStorage.getItem('conductor.palette.recent') ?? '{}') as object))).toContain(`session:${id}`)

    // A reload keeps the pin.
    await b.reload()
    await expect(b.locator('[data-sidebar-section="pinned"]').locator(`[data-pinned-session="${id}"]`)).toBeVisible({ timeout: 15_000 })
    await a.context().close()
    await b.context().close()
  })

  test('a fold made just before a reload is kept: this browser\'s change, not yet sent, is newer than the server\'s copy', async ({ browser, api, state }) => {
    session = await api.ok<Session>('POST', '/api/sessions', { agentId: 'plain', name: 'e2e ui reload' })
    const page = await contextPage(browser, state.token)
    const row = page.locator(`[data-sidebar-row="s:${session.id}"]`).first()
    await expect(row).toBeVisible({ timeout: 15_000 })
    const section = page.locator('[data-sidebar-section]').filter({ has: row }).last()
    const key = (await section.getAttribute('data-sidebar-section'))!
    // The page has read the server's documents (useUiState's own mark).
    await expect(page.locator('[data-ui-state]').first()).toHaveAttribute('data-ui-state', 'read', { timeout: 15_000 })
    await section.locator('button[aria-expanded]').first().click()
    await expect(page.locator(`[data-sidebar-section="${key}"]`)).toHaveAttribute('data-folded', 'true')
    // Reloaded at once, inside the second a change waits before it is written: the server still holds the layout without the fold.
    expect(JSON.stringify(((await doc(api, 'layout')).sidebar as { folds?: object } | undefined)?.folds ?? {})).not.toContain(`"${key}":true`)
    await page.reload()
    await expect(page.locator(`[data-sidebar-section="${key}"]`)).toHaveAttribute('data-folded', 'true')
    await expect.poll(async () => JSON.stringify(((await doc(api, 'layout')).sidebar as { folds?: object } | undefined)?.folds ?? {}), { timeout: 15_000 }).toContain(`"${key}":true`)
    await page.reload()
    await expect(page.locator(`[data-sidebar-section="${key}"]`)).toHaveAttribute('data-folded', 'true')
    await page.context().close()
  })

  test('a pinned row fits a phone\'s Sessions page in both themes, its name cut and its Unpin in reach', async ({ browser, api, state }) => {
    session = await api.ok<Session>('POST', '/api/sessions', { agentId: 'plain', name: 'e2e ui pinned on a phone, its name long enough to be cut at this width' })
    const id = session.id
    await api.ok('PUT', '/api/ui/pins', { v: 1, pins: [{ session: id, name: session.name, agent: 'plain', at: Date.now() }] })
    const ctx = await browser.newContext({ viewport: { width: 390, height: 844 } })
    const page = await ctx.newPage()
    await page.addInitScript((t) => localStorage.setItem('conductor.workbenchToken', t), state.token)
    await page.goto('/sessions')
    for (const theme of ['dark', 'light'] as const) {
      await useTheme(page, theme, '[data-bottom-bar]')
      const pin = page.locator(`[data-sidebar-section="pinned"] [data-pinned-session="${id}"]`)
      await expect(pin).toBeVisible({ timeout: 15_000 })
      const row = (await pin.boundingBox())!
      const unpin = (await pin.locator('[data-pinned-unpin]').boundingBox())!
      const name = pin.locator('[data-pinned-name]')
      expect(row.x).toBeGreaterThanOrEqual(0)
      expect(row.x + row.width).toBeLessThanOrEqual(390)
      expect(unpin.x + unpin.width).toBeLessThanOrEqual(row.x + row.width)
      expect(await name.evaluate((el) => el.scrollWidth > el.clientWidth)).toBe(true)
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390)
    }
    await ctx.close()
  })

  test('a document past its bound is refused, and the page goes on: its next change is kept', async ({ browser, api, state }) => {
    session = await api.ok<Session>('POST', '/api/sessions', { agentId: 'plain', name: 'e2e ui bound' })
    const id = session.id
    const page = await contextPage(browser, state.token)
    const status = await page.evaluate(async (t) => {
      const res = await fetch('/api/ui/layout', { method: 'PUT', headers: { Authorization: `Bearer ${t}`, 'Content-Type': 'application/json' }, body: JSON.stringify({ v: 1, runView: 'x'.repeat(70_000) }) })
      return res.status
    }, state.token)
    expect(status).toBe(413)
    expect((await doc(api, 'layout')).runView).toBeUndefined()

    const row = page.locator(`[data-sidebar-row="s:${id}"]`).first()
    await expect(row).toBeVisible({ timeout: 15_000 })
    await row.click({ button: 'right' })
    await page.getByRole('menuitem', { name: 'Pin to top' }).click()
    await expect.poll(async () => JSON.stringify((await doc(api, 'pins')).pins ?? []), { timeout: 15_000 }).toContain(id)
    await page.locator(`[data-pinned-session="${id}"] a`).click()
    await expect(page).toHaveURL(new RegExp(`/sessions/${id}`))
    await page.context().close()
  })
})
