import { expect, test, type Session } from './fixtures'

// Share is one click: opening the dialog makes a view link, copies it and
// says where it reaches; opening it again on the same session shows the
// same link rather than minting another. The e2e server publishes nowhere
// (CONDUCTOR_RENDEZVOUS=0) and has reach off, so the link is local.
const sessions: string[] = []

// Its session stops as the test ends: one left running stays on the Yard's wall for the rest of the suite, where it narrows the
// tiles later specs measure (#219).
test.afterEach(async ({ api }) => {
  for (const id of sessions.splice(0)) await api.stopSession(id)
})

test('Share makes a link in one click, says where it reaches, and keeps it for the next open', async ({ page, api }) => {
  const session = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'share me' })
  sessions.push(session.id)
  await page.goto(`/sessions/${session.id}`)
  await page.getByRole('button', { name: 'Share', exact: true }).first().click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.locator('[data-created-url]')).toBeVisible({ timeout: 15_000 })
  await expect(dialog.locator('[data-link-reach]')).toHaveAttribute('data-link-reach', 'local')
  await expect(dialog.locator('[data-share-link]')).toHaveCount(1)
  await expect(dialog.locator('[data-share-link]').first()).toContainText('View')
  const url = await dialog.locator('[data-created-url]').getAttribute('title')
  expect(url).toMatch(/\/join\//)
  // The link as a QR code for a phone (#144), labelled with the link's text, drawn module by module.
  const qr = dialog.locator('[data-share-qr]')
  await expect(qr).toHaveAttribute('aria-label', url!)
  expect(((await qr.locator('path').getAttribute('d')) ?? '').split('h1v1h-1z').length, 'modules drawn').toBeGreaterThan(200)
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)
  await page.getByRole('button', { name: 'Share', exact: true }).first().click()
  await expect(page.getByRole('dialog').locator('[data-created-url]')).toHaveAttribute('title', url!)
  await expect(page.getByRole('dialog').locator('[data-share-link]')).toHaveCount(1)
  // Control on the link itself: a control link replaces the view one nobody used, and the next Share makes control.
  await page.getByRole('dialog').locator('[data-share-role-option="control"]').click()
  await expect(page.getByRole('dialog').locator('[data-share-role-option="control"]')).toHaveAttribute('aria-checked', 'true')
  await expect(page.getByRole('dialog').locator('[data-share-link]')).toHaveCount(1)
  await expect(page.getByRole('dialog').locator('[data-share-link]').first()).toContainText('Control')
  expect(await page.evaluate(() => localStorage.getItem('conductor.share.role'))).toBe('control')
  await page.getByRole('dialog').locator('[data-share-role-option="view"]').click()
  await expect(page.getByRole('dialog').locator('[data-share-link]').first()).toContainText('View')
  // Another link, with control, from the secondary form.
  await page.getByRole('dialog').getByText('Another link').click()
  await page.getByRole('dialog').locator('[data-another-link]').getByRole('radio', { name: /Control/ }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Create link' }).click()
  await expect(page.getByRole('dialog').locator('[data-share-link]')).toHaveCount(2)
})

// A decision link at a switchyard that carries no decisions (docs/plans/decision-inbox.md, Version skew; MB0 PR-7): the server
// refuses it 409 switchyard_too_old, and the share switch goes off and stays off with the server's words, no link made. The e2e
// server publishes nowhere, so the refusal is answered here (page.route), for a request that asks for a decision link alone.
test('a decision link refused by a switchyard too old: the switch goes off and stays off with the words why, and no link is made', async ({ page, api }) => {
  const session = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'share too old' })
  sessions.push(session.id)
  const words = 'This switchyard must be updated to answer decisions through its links.'
  let refused = 0
  await page.route(`**/api/sessions/${session.id}/links`, async (route) => {
    const req = route.request()
    if (req.method() === 'POST' && (req.postDataJSON() as { decide?: boolean } | null)?.decide === true) {
      refused++
      await route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ error: { code: 'switchyard_too_old', message: words } }) })
      return
    }
    await route.continue()
  })
  await page.goto(`/sessions/${session.id}`)
  await page.getByRole('button', { name: 'Share', exact: true }).first().click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.locator('[data-created-url]')).toBeVisible({ timeout: 15_000 })
  await expect(dialog.locator('[data-share-link]')).toHaveCount(1)
  await dialog.getByText('Another link').click()
  await dialog.locator('[data-another-link]').getByRole('radio', { name: /Control/ }).click()
  const sw = dialog.getByRole('switch', { name: 'Answer decisions from this link' })
  await sw.click()
  await expect(sw).toHaveAttribute('aria-checked', 'true')
  await dialog.getByRole('button', { name: 'Create link' }).click()
  await expect(dialog.locator('[data-share-decide-refused]')).toHaveText(words)
  await expect(sw).toHaveAttribute('aria-checked', 'false')
  await expect(sw).toBeDisabled()
  expect(refused).toBe(1)
  await expect(dialog.locator('[data-created-decide]')).toHaveCount(0)
  await expect(dialog.locator('[data-share-link]')).toHaveCount(1)
  const { links } = await api.ok<{ links: Array<{ decide?: boolean }> }>('GET', `/api/sessions/${session.id}/links`)
  expect(links.map((l) => l.decide === true)).toEqual([false])
})
