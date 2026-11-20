import { expect, test } from './fixtures'

// The free build (docs/plans/sync-seams.md, OS-4): with no sync store registered, no Share control on a project's page, no Sync row
// in Settings → The app, and no other machine's rows on the decision page, a ticket's page or the sidebar.

test('a server with no sync store draws no Share control, no Sync row and no remote rows', async ({ page, api }) => {
  await api.ok('POST', '/api/dispatch/projects', { opId: `sf-${Date.now()}`, key: 'SFREE', name: 'Sync free' }).catch(() => undefined)
  const t = await api.ok<{ ticket: { key: string } }>('POST', '/api/dispatch/tickets', {
    opId: `sft-${Date.now()}`,
    project: 'SFREE',
    title: 'Nothing elsewhere',
  })
  await page.setViewportSize({ width: 1440, height: 900 })

  await page.goto('/dispatch/SFREE')
  await expect(page.locator('[data-project-settings-open]')).toBeVisible()
  await expect(page.locator('[data-new-ticket-open]')).toBeEnabled()
  await expect(page.locator('[data-project-share]')).toHaveCount(0)

  const [project, n] = t.ticket.key.split('-')
  await page.goto(`/dispatch/${project}/${n}`)
  await expect(page.locator(`[data-ticket="${t.ticket.key}"]`)).toBeVisible()
  await expect(page.locator('[data-remote-decisions], [data-remote-runs], [data-ticket-also-linked]')).toHaveCount(0)

  await page.goto('/decisions')
  await expect(page.locator('[data-decisions]')).toBeVisible()
  await expect(page.locator('[data-remote-decisions]')).toHaveCount(0)
  await expect(page.locator('[data-remote-pin]')).toHaveCount(0)

  await page.goto('/settings')
  await expect(page.locator('[data-about-card]')).toBeVisible()
  await expect(page.locator('[data-about-row="sync"]')).toHaveCount(0)
})
