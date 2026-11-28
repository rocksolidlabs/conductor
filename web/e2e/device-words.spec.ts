import { dayPage, expect, showView, test, type Api, type Session } from './fixtures'
import { MEASURE_WAIT_MS } from './layout'

// Conductor is used in its apps, never "in a browser" (UX3 R86): the Share panel of a session with no link says a link lets someone
// watch it from anywhere, and Alerts says this device where it said Browser. Wide and short, and at 1440 (the phone's session page
// has no inspector; its More's switch is phone.spec.ts's). A session of the spec's own, found by its id, ended after each test.

const made: string[] = []

test.afterEach(async ({ api }) => {
  for (const id of made.splice(0)) await api.stopSession(id)
})

async function launch(api: Api, name: string): Promise<Session> {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name })
  made.push(s.id)
  return s
}

const SIZES = [
  { width: 2000, height: 810 },
  { width: 1440, height: 900 },
]

for (const size of SIZES) {
  test(`the Share panel with no link says from anywhere, and Alerts says this device, ${size.width} x ${size.height}`, async ({ browser, api, state }) => {
    const s = await launch(api, `e2e device words ${size.width}`)
    const page = await dayPage(browser, { server: state, path: `/sessions/${s.id}`, width: size.width, height: size.height })
    try {
      await expect(page.locator('[data-inspector]')).toBeVisible({ timeout: MEASURE_WAIT_MS })
      await showView(page, 'people')
      // With the people's chat on, People's head folds the roster, share links among it: opened by its head, until it shows.
      const head = page.locator('[data-inspector] [data-inspector-head="people"]')
      await expect(head).toBeVisible({ timeout: MEASURE_WAIT_MS })
      await expect(async () => {
        if ((await head.getAttribute('aria-expanded')) === 'false') await head.click()
        await expect(page.locator('[data-people-roster]')).toBeVisible({ timeout: 1000 })
      }).toPass({ timeout: MEASURE_WAIT_MS })
      const empty = page.locator('[data-share-links-empty]')
      await expect(empty).toHaveText('No share links yet. A link lets someone watch this session from anywhere, or take control of it, until it expires or you revoke it.')
      await expect(empty).not.toContainText('browser')

      await page.goto(`/sessions/${s.id}?alerts`)
      const panel = page.getByRole('dialog', { name: 'Alerts' })
      await expect(panel.locator('[data-routing-matrix]')).toBeVisible({ timeout: MEASURE_WAIT_MS })
      await expect(panel.getByText('Notification on this device', { exact: true })).toBeVisible()
      await expect(panel.getByText('When a session needs you, and for the events routed to This device:')).toBeVisible()
      await expect(panel.getByText('Where each kind of event goes on this device.', { exact: false })).toBeVisible()
      expect(await panel.innerText(), 'Alerts names no browser').not.toMatch(/browser/i)
    } finally {
      await page.context().close()
    }
  })
}
