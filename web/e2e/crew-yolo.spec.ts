import { randomUUID } from 'node:crypto'
import { dayPage, expect, test } from './fixtures'
import { MEASURE_WAIT_MS } from './layout'

// A crew's Yolo is used all the time, so it is in view on its Setup, under its name, with More options closed (UX3 R87): wide and
// short, at 1440 and on a phone. Its launch sheet says it in the same word. A crew of the spec's own, found by its id, removed after.

const crews: string[] = []

test.afterEach(async ({ api }) => {
  for (const id of crews.splice(0)) await api.call('DELETE', `/api/crews/${encodeURIComponent(id)}`)
})

const SIZES = [
  { width: 2000, height: 810, touch: false },
  { width: 1440, height: 900, touch: false },
  { width: 390, height: 844, touch: true },
]

for (const size of SIZES) {
  test(`a crew's Yolo is in view on its Setup without a press, and its sheet says Yolo, ${size.width} x ${size.height}`, async ({ browser, api, state }) => {
    const name = `e2e crew yolo ${randomUUID().slice(0, 6)}`
    const { crew } = await api.ok<{ crew: { id: string } }>('POST', '/api/crews', { name, goal: '', cwd: '', where: 'server', isolation: 'none', openAfterLaunch: false, yolo: true, members: [{ name: 'lead', agentId: 'claude', prompt: '', start: { when: 'manual' } }] })
    crews.push(crew.id)
    const page = await dayPage(browser, { server: state, path: `/crews/${crew.id}`, width: size.width, height: size.height, touch: size.touch, ready: '[data-crew-fields]' })
    try {
      await expect(page.getByRole('textbox', { name: 'Crew name' })).toHaveValue(name)
      // More options is closed, and the Yolo select is drawn outside it, labelled Yolo, showing the crew's own choice.
      await expect(page.locator('[data-crew-more-options]')).toHaveAttribute('aria-expanded', 'false')
      const select = page.getByRole('combobox', { name: 'Yolo' })
      await select.scrollIntoViewIfNeeded()
      await expect(select).toBeVisible({ timeout: MEASURE_WAIT_MS })
      await expect(select).toHaveAttribute('data-crew-yolo', '')
      await expect(select).toContainText('On: members act without asking')
      await expect(page.locator('[data-crew-more] [data-crew-yolo]')).toHaveCount(0)
      await expect(page.getByRole('combobox', { name: 'Permission prompts' })).toHaveCount(0)
      await expect(page.locator('[data-crew-more-line]')).toHaveText('Stays on this page · No view link')

      // The launch sheet says it in the same word.
      await page.locator('[data-launch]').click()
      await expect(page.locator('[data-crew-launch-yolo]')).toHaveText("Yolo: on. The members act without asking (the crew's setting)", { timeout: MEASURE_WAIT_MS })
      await page.locator('[data-crew-launch-cancel]').click()
      await expect(page.locator('[data-crew-launch-confirm]')).toHaveCount(0)
    } finally {
      await page.context().close()
    }
  })
}
