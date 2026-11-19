import { rmSync } from 'node:fs'
import { join } from 'node:path'
import type { Page } from '@playwright/test'
import { expect, pageTitle, test } from './fixtures'

// History's Feed (the UX review's P12): the live feed of what agents report, the retired Events page's, as History's second view
// (`?view=feed`). It names a crew member as "<crew> / <member>" and offers Answer on what needs you; beside it the last hour, where
// events go and which agents report. The feed is this tab's memory, not the view's: switched to Search and back, its rows are there.
// Every old link to the Events page lands where its tab lives now, and G E opens the Feed.
test.describe.configure({ mode: 'serial' })

let crewId = ''
const runs: string[] = []

test.beforeAll(async ({ api, state }) => {
  // The untrusted Codex stub asks to trust the folder unless an earlier spec's answer is saved.
  rmSync(join(state.home, '.codex', 'config.toml'), { force: true })
  crewId = (
    await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
      name: 'e2e history feed',
      goal: 'feed',
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
})

test.afterAll(async ({ api }) => {
  for (const id of runs) await api.stopRun(id)
  if (crewId) await api.call('DELETE', `/api/crews/${encodeURIComponent(crewId)}`)
})

const view = (page: Page, name: 'Search' | 'Live') => page.locator('[data-history-views]').getByRole('tab', { name })

/**
 * Presses lowercase letters in turn as a person typing quickly does, every key handed to the browser at once (shortcuts.spec.ts's
 * pressInTurn): a press that waits for each key's answer can let the chord's wait for its second key run out on a loaded machine.
 */
async function pressInTurn(page: Page, ...keys: string[]) {
  const cdp = await page.context().newCDPSession(page)
  try {
    const sends: Array<Promise<unknown>> = []
    for (const key of keys) {
      const code = `Key${key.toUpperCase()}`
      const windowsVirtualKeyCode = key.toUpperCase().charCodeAt(0)
      sends.push(cdp.send('Input.dispatchKeyEvent', { type: 'keyDown', key, code, text: key, unmodifiedText: key, windowsVirtualKeyCode }))
      sends.push(cdp.send('Input.dispatchKeyEvent', { type: 'keyUp', key, code, windowsVirtualKeyCode }))
    }
    await Promise.all(sends)
  } finally {
    await cdp.detach()
  }
}

/** Opens the palette from the layout's key, pressed once the layout that listens for it is drawn. */
async function openPalette(page: Page) {
  await expect(page.locator('#dashboard-sidebar-main')).toBeVisible()
  await page.keyboard.press('Alt+/')
  const palette = page.locator('[data-palette]')
  await expect(palette).toBeVisible()
  return palette
}

test("History's Feed names crew members, offers Answer, fills the last hour beside it, and keeps its rows across a switch of views", async ({ page, api }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto('/history?view=feed')
  const feed = page.locator('[data-event-feed]')
  await expect(feed).toBeVisible()
  await expect(view(page, 'Live')).toHaveAttribute('aria-selected', 'true')
  await expect(page.locator('[data-history-feed-caption]')).toHaveText('What agents report, from the moment this page opened.')
  // The title's page part: a count of what needs you may lead it ("(2) History · Live · Conductor").
  await expect.poll(() => pageTitle(page)).toMatch(/^History · Live\b/)
  const run = await api.launchCrew(crewId)
  runs.push(run.id)
  const ask = () => feed.locator('[data-event="needs_input"]').filter({ hasText: 'e2e history feed / review' }).first()
  await expect(ask()).toBeVisible({ timeout: 45_000 })
  await expect(ask().locator('[data-feed-answer]')).toHaveAttribute('href', /^\/sessions\//)
  await expect(feed.locator('[data-feed-group]').first()).toContainText('now')
  const chart = page.locator('[data-activity-chart]')
  await expect.poll(async () => Number(await chart.getAttribute('data-total')), { timeout: 30_000 }).toBeGreaterThanOrEqual(2)
  await expect(chart.locator('[data-activity-bar]')).toHaveCount(60)
  await expect(page.locator('[data-routing-readout]')).toContainText('Browser notification')
  // Which agents report is set-up, not history: folded until asked for.
  await expect(page.locator('[data-reporting-agent="claude"]')).toBeHidden()
  await page.locator('[data-reporting-toggle]').click()
  await expect(page.locator('[data-reporting-agent="claude"]')).toBeVisible()

  // Search, the view History opens on: the search box, no feed, the address without the view.
  await view(page, 'Search').click()
  await expect(page).toHaveURL(/\/history$/)
  await expect(page.locator('[data-history-input]')).toBeVisible()
  await expect(feed).toHaveCount(0)
  // Back to the Feed: the rows are read again after the redraw, from the tab's memory.
  await view(page, 'Live').click()
  await expect(page).toHaveURL(/\/history\?view=feed$/)
  await expect(ask()).toBeVisible()
  await expect(ask().locator('[data-feed-answer]')).toHaveAttribute('href', /^\/sessions\//)
  await expect(chart.locator('[data-activity-bar]')).toHaveCount(60)

  // The palette's History group: its two views as rows, the shown one on, and Refresh while the Feed is shown.
  let palette = await openPalette(page)
  await expect(palette.locator('[data-palette-row="page:view:feed"] [data-palette-switch]')).toHaveAttribute('data-palette-switch', 'on')
  await expect(palette.locator('[data-palette-row="page:view:search"] [data-palette-switch]')).toHaveAttribute('data-palette-switch', 'off')
  await expect(palette.locator('[data-palette-row="page:refresh"]')).toBeVisible()
  // A view's row is a switch: the palette stays open on it, and Escape closes it.
  await palette.locator('[data-palette-row="page:view:search"]').click()
  await expect(page).toHaveURL(/\/history$/)
  await palette.locator('input[data-palette-input]').focus()
  await page.keyboard.press('Escape')
  await expect(palette).toBeHidden()
  // The palette gone, the focus back on the Feed's tab that was clicked last: the view stays the one the row chose.
  await expect(view(page, 'Search')).toHaveAttribute('aria-selected', 'true')
  await expect(page).toHaveURL(/\/history$/)
  palette = await openPalette(page)
  await expect(palette.locator('[data-palette-row="page:view:search"] [data-palette-switch]')).toHaveAttribute('data-palette-switch', 'on')
  await expect(palette.locator('[data-palette-row="page:refresh"]')).toHaveCount(0)
  await palette.locator('[data-palette-row="page:view:feed"]').click()
  await expect(page).toHaveURL(/\/history\?view=feed$/)
  await palette.locator('input[data-palette-input]').focus()
  await page.keyboard.press('Escape')
  await expect(palette).toBeHidden()
  await expect(ask()).toBeVisible()
})

test("every old link to the Events page lands where its tab lives now, and G E opens History's feed", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  for (const link of ['/events', '/events?tab=feed', '/events?tab=nonsense']) {
    await page.goto(link)
    await expect(page, link).toHaveURL(/\/history\?view=feed$/)
    await expect(page.locator('[data-event-feed]'), link).toBeVisible()
  }
  // The Routing tab's rules are the Alerts panel's: it opens over the feed, and the address loses `alerts`.
  await page.goto('/events?tab=routing')
  await expect(page.getByRole('dialog', { name: 'Alerts' }).locator('[data-routing-matrix]')).toBeVisible()
  await expect(page.locator('[data-event-feed]')).toBeAttached()
  await expect(page).toHaveURL(/\/history\?view=feed$/)
  await page.keyboard.press('Escape')
  await expect(page.getByRole('dialog', { name: 'Alerts' })).toHaveCount(0)
  // The Integrations tab's table is Agents' section Reporting to Conductor.
  await page.goto('/events?tab=integrations')
  await expect(page).toHaveURL(/\/agents#reporting$/)
  await expect(page.locator('#reporting [data-integration][data-id="claude"]')).toBeVisible()
  // G then E, pressed once the layout that listens for it has drawn its sidebar.
  await page.goto('/crews')
  await expect(page.locator('#dashboard-sidebar-main')).toBeVisible()
  await pressInTurn(page, 'g', 'e')
  await expect(page).toHaveURL(/\/history\?view=feed$/)
  await expect(page.locator('[data-event-feed]')).toBeVisible()
})
