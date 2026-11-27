import type { Locator, Page } from '@playwright/test'
import { dayPage, expect, test, type Api, type Session } from './fixtures'
import { MEASURE_WAIT_MS, probeColour } from './layout'

// The yolo badge says a setting is on, so it is neutral (docs/design/system.md, Amber when someone must act; UX3 R2): the session
// header's badge and the sidebar row's shield are drawn in no warning colour, in both themes, wide and short, at 1440 and on a phone
// (which has the header and no sidebar). A yolo session of the spec's own on the stub agent, found by its id, ended after each test.

const made: string[] = []

test.afterEach(async ({ api }) => {
  for (const id of made.splice(0)) await api.stopSession(id)
})

async function yolo(api: Api, name: string): Promise<Session> {
  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name, yolo: true })
  made.push(s.id)
  expect(s.yolo, 'the stub agent has a yolo recipe, so the session is launched yolo').toBe(true)
  return s
}

const color = (l: Locator) => l.evaluate((e) => getComputedStyle(e).color)
const border = (l: Locator) => l.evaluate((e) => getComputedStyle(e).boxShadow + ' ' + getComputedStyle(e).borderColor)

/** Neither of the warning colours (its fill and its text) is what the element draws its text, its icon or its ring in. */
async function notAmber(page: Page, l: Locator, what: string) {
  const warning = [await probeColour(page, 'var(--ui-warning)'), await probeColour(page, 'var(--ui-warning-text)')]
  const drawn = await color(l)
  expect(warning, `${what} is drawn in ${drawn}, a warning colour`).not.toContain(drawn)
  const ring = await border(l)
  for (const w of warning) expect(ring, `${what}'s ring or border is a warning colour`).not.toContain(w)
}

const SIZES = [
  { width: 2000, height: 810, touch: false },
  { width: 1440, height: 900, touch: false },
  { width: 390, height: 844, touch: true },
]

for (const theme of ['dark', 'light'] as const) {
  for (const size of SIZES) {
    test(`the yolo badge is neutral in the header${size.touch ? '' : ' and the sidebar row'}, ${theme}, ${size.width} x ${size.height}`, async ({ browser, api, state }) => {
      const s = await yolo(api, `e2e yolo badge ${theme} ${size.width}`)
      const page = await dayPage(browser, { server: state, path: `/sessions/${s.id}`, theme, width: size.width, height: size.height, touch: size.touch })
      try {
        const header = page.locator('[data-session-bar] [data-yolo-badge]')
        await expect(header).toBeVisible({ timeout: MEASURE_WAIT_MS })
        await expect(header).toHaveText('yolo')
        await notAmber(page, header, "the header's yolo badge")
        // Its shield is the badge's text colour, so it is not amber either.
        await notAmber(page, header.locator('[data-slot="leadingIcon"], .iconify').first(), "the header badge's shield")

        if (!size.touch) {
          const shield = page.locator(`[data-session-list] [data-sidebar-row="s:${s.id}"] [data-yolo-badge]`).first()
          await shield.scrollIntoViewIfNeeded()
          await expect(shield).toBeVisible({ timeout: MEASURE_WAIT_MS })
          await expect(shield).toHaveAttribute('aria-label', 'yolo')
          await notAmber(page, shield, "the sidebar row's yolo shield")
          expect(await color(shield), "the sidebar row's shield is muted").toBe(await probeColour(page, 'var(--ui-text-muted)'))
        }
      } finally {
        await page.context().close()
      }
    })
  }
}
