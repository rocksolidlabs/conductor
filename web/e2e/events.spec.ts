import { rmSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test } from './fixtures'

// The Events page: it opens on the feed, which names a crew member as
// "<crew> / <member>" and offers Answer on what needs you; beside it the last
// hour and where events go; Routing toggles a destination and keeps it in
// this browser. The Integrations tab's test moved with the table to the Agents page (agents-reporting.spec.ts).
test.describe.configure({ mode: 'serial' })

let crewId = ''
const runs: string[] = []

test.beforeAll(async ({ api, state }) => {
  // The untrusted Codex stub asks to trust the folder unless an earlier spec's answer is saved.
  rmSync(join(state.home, '.codex', 'config.toml'), { force: true })
  crewId = (
    await api.ok<{ crew: { id: string } }>('POST', '/api/crews', {
      name: 'e2e events',
      goal: 'events',
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

test('the feed names crew members, offers Answer on what needs you, and the last hour fills beside it', async ({ page, api }) => {
  await page.goto('/events')
  await expect(page.locator('[data-events-feed-tab]')).toBeVisible()
  const run = await api.launchCrew(crewId)
  runs.push(run.id)
  const feed = page.locator('[data-event-feed]')
  const ask = feed.locator('[data-event="needs_input"]').filter({ hasText: 'e2e events / review' }).first()
  await expect(ask).toBeVisible({ timeout: 45_000 })
  await expect(ask.locator('[data-feed-answer]')).toHaveAttribute('href', /^\/sessions\//)
  await expect(feed.locator('[data-feed-group]').first()).toContainText('now')
  const chart = page.locator('[data-activity-chart]')
  await expect.poll(async () => Number(await chart.getAttribute('data-total')), { timeout: 30_000 }).toBeGreaterThanOrEqual(2)
  await expect(chart.locator('[data-activity-bar]')).toHaveCount(60)
  await expect(page.locator('[data-routing-readout]')).toContainText('Browser notification')
  await expect(page.locator('[data-reporting-agent="claude"]')).toBeVisible()
})

test('Routing toggles a destination as a pill and keeps it in this browser', async ({ page }) => {
  await page.goto('/events')
  await page.locator('[data-routing-readout]').getByRole('button', { name: 'Edit routing' }).click()
  await expect(page).toHaveURL(/tab=routing/)
  const pill = page.locator('[data-cell="done.browser"]')
  await expect(pill).toHaveAttribute('aria-checked', 'false')
  await pill.click()
  await expect(pill).toHaveAttribute('aria-checked', 'true')
  expect(JSON.parse((await page.evaluate(() => localStorage.getItem('conductor.events.routes'))) ?? '{}').done.browser).toBe(true)
  await page.reload()
  await expect(page.locator('[data-cell="done.browser"]')).toHaveAttribute('aria-checked', 'true')
  await expect(page.locator('[data-cell="working.badge"]')).toHaveAttribute('data-fixed-cell', '')
  await page.locator('[data-cell="done.browser"]').click()
})
