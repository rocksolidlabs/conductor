import type { Locator, Page } from '@playwright/test'
import { filledPrimaries } from './contrast'
import { atRest, budget, dayPage, expect, showView, test } from './fixtures'
import { MEASURE_WAIT_MS, probeColour } from './layout'

// One colour for the main action, the code font for evidence, amber only where someone must act (the UX review's P16, PR-26), read
// off the seeded day as the browser draws it, at 1440 x 900 and at 390 x 844 with touch, in both themes. The pages are only looked
// at: nothing is pressed, so the read-only day stays as it was.
//
// - The main action: on the Needs you page the failed task's Retry and the waiting plan revision's Approve are the forest fill
//   (`--ui-primary`); a session page's bar has one filled button at most and Share is outlined; a live run's Open run is outlined.
// - The code font: the sentences (the sidebar run header's subtitle, the run card's meta line, the inspector's People line) are set
//   in the app's face; the ids (the run card's short run id, a task's id on the run page) stay in the code font.
// - Amber: Not installed and an identified agent's badge on Agents are neither amber nor green; the Yard's Needs you chip keeps its
//   amber mark (its text and number are the chip's neutral ink, PR-17's, and not read here). The Launch agent dialog's Yolo, turned
//   on, is the switch's own on colour, never amber (its switch is moved and nothing launched, so the day stays as it was).

const SIZES = [
  { width: 1440, height: 900, touch: false },
  { width: 390, height: 844, touch: true },
] as const
const THEMES = ['dark', 'light'] as const

/** The element's computed background, resolved to sRGB as probeColour gives it. */
async function background(page: Page, el: Locator): Promise<string> {
  return probeColour(page, await el.evaluate((n) => getComputedStyle(n).backgroundColor))
}

/** The element's computed text colour, resolved likewise. */
async function colour(page: Page, el: Locator): Promise<string> {
  return probeColour(page, await el.evaluate((n) => getComputedStyle(n).color))
}

/** Whether the element is set in the code font: its computed family starts with the code font's first family. */
async function mono(el: Locator): Promise<boolean> {
  return el.evaluate((n) => /^["']?JetBrains Mono/.test(getComputedStyle(n).fontFamily))
}

/** An outlined button: no fill of its own, only the page's ground (Nuxt UI's neutral outline paints `bg-default`), never the forest. */
async function outlined(page: Page, el: Locator): Promise<void> {
  const css = await el.evaluate((n) => getComputedStyle(n).backgroundColor)
  if (/^(transparent|rgba\(0, 0, 0, 0\)|.*\/ 0\))$/.test(css)) return
  expect(await probeColour(page, css), 'an outlined button paints the page\'s ground at most').toBe(await probeColour(page, 'var(--ui-bg)'))
}

for (const size of SIZES) {
  for (const theme of THEMES) {
    const where = `${theme} at ${size.width}`
    const open = (browser: Parameters<typeof dayPage>[0], path: string) => dayPage(browser, { path, theme, width: size.width, height: size.height, touch: size.touch })

    test(`the Needs you page's Retry and Approve are the forest fill, ${where}`, async ({ day, browser }) => {
      budget(2 * MEASURE_WAIT_MS)
      const w = await day.part('waiting')
      // The failed task's Retry (a run's decision item has no Approve: the waiting plan revision's is read instead).
      const task = `task:${w['Paging spike']}/index`
      const page = await open(browser, `/decisions?item=${encodeURIComponent(task)}`)
      const primary = await probeColour(page, 'var(--ui-primary)')
      const retry = page.locator(`[data-inbox-item="${task}"] [data-inbox-action="retry"]`)
      await expect(retry).toBeVisible({ timeout: MEASURE_WAIT_MS })
      expect(await background(page, retry), "Retry is the failed task's main action").toBe(primary)
      await page.goto(`/decisions?item=${encodeURIComponent('plan:CND-2/1')}`)
      const approve = page.locator('[data-inbox-item="plan:CND-2/1"] [data-inbox-action="approve"]')
      await expect(approve).toBeVisible({ timeout: MEASURE_WAIT_MS })
      expect(await background(page, approve), "Approve is the waiting revision's main action").toBe(primary)
      expect(await filledPrimaries(page.locator('[data-inbox-item="plan:CND-2/1"] [data-inbox-actions]')), 'one filled choice').toHaveLength(1)
    })

    test(`a session's bar has one filled button and a run's card none, ${where}`, async ({ day, browser }) => {
      budget(4 * MEASURE_WAIT_MS)
      const r = await day.part('runs')
      const s = await day.part('states')

      // A session page: one filled button in its bar at most, and Share outlined where the bar shows it (below sm it is in More).
      const page = await open(browser, `/sessions/${s.driver}`)
      const bar = page.locator('[data-session-bar]')
      await expect(bar).toBeVisible({ timeout: MEASURE_WAIT_MS })
      expect((await filledPrimaries(bar)).length, `the session bar's filled buttons: ${(await filledPrimaries(bar)).join(', ')}`).toBeLessThanOrEqual(1)
      if (size.width >= 640) {
        const share = bar.locator('[data-share-button]')
        await expect(share).toBeVisible()
        await outlined(page, share)
      }

      // A live run's card on Crews: Open run outlined, no fill on the card.
      await page.goto('/crews')
      const card = page.locator(`[data-run="${r['Ledger migration']}"]`)
      await expect(card.locator('[data-run-open]')).toBeVisible({ timeout: MEASURE_WAIT_MS })
      await outlined(page, card.locator('[data-run-open]'))
      expect(await filledPrimaries(card), 'a run card has no filled button').toEqual([])
    })

    test(`sentences are in the app's face and ids in the code font, ${where}`, async ({ day, browser }) => {
      budget(4 * MEASURE_WAIT_MS)
      const r = await day.part('runs')
      const s = await day.part('states')
      const run = r['Ledger migration']!

      // The run card on Crews: its meta line ("up 9m · server · worktrees") in the app's face, its short run id in the code font.
      const page = await open(browser, '/crews')
      const card = page.locator(`[data-run="${run}"]`)
      await expect(card.locator('[data-run-meta]')).toBeVisible({ timeout: MEASURE_WAIT_MS })
      expect(await mono(card.locator('[data-run-meta]')), "the run card's meta line").toBe(false)
      expect(await mono(card.locator('[data-run-short-id]')), "the run card's short run id").toBe(true)
      await expect(card.locator('[data-run-short-id]')).toHaveAttribute('data-mono', 'id')

      // The sidebar's run header: its subtitle ("started 07:29 · 3 agents") in the app's face. The phone has no sidebar.
      if (!size.touch) {
        const subtitle = page.locator(`#dashboard-sidebar-main [data-sidebar-run-subtitle="${run}"]`)
        await expect(subtitle).toBeVisible({ timeout: MEASURE_WAIT_MS })
        expect(await mono(subtitle), "the sidebar run header's subtitle").toBe(false)
      }

      // A task's id on the run page, in the code font.
      await page.goto(`/runs/${encodeURIComponent(run)}?view=tasks`)
      const id = page.locator('span[data-task-id]').first()
      await expect(id).toBeVisible({ timeout: MEASURE_WAIT_MS })
      expect(await mono(id), "a task's id").toBe(true)

      // The inspector's People line ("1 here now · 0 share links"), in the app's face. The phone's session page has no inspector.
      if (!size.touch) {
        await page.goto(`/sessions/${s.driver}`)
        await expect(page.locator('[data-inspector]')).toBeVisible({ timeout: MEASURE_WAIT_MS })
        await showView(page, 'people')
        const people = page.locator('[data-inspector-head="people"] [data-inspector-sub]')
        await expect(people).toBeVisible({ timeout: MEASURE_WAIT_MS })
        await expect(people).toHaveText(/here now · \d+ share links?$/)
        expect(await mono(people), "the inspector's People line").toBe(false)
      }
    })

    test(`amber only where someone must act, ${where}`, async ({ day, browser }) => {
      budget(4 * MEASURE_WAIT_MS)
      await day.part('states')

      // Agents: Not installed and an identified agent's badge ask nothing, so neither is amber nor green.
      const page = await open(browser, '/agents')
      const notInstalled = page.locator('[data-agent="goose"] [data-not-installed]')
      await expect(notInstalled).toBeVisible({ timeout: 30_000 })
      expect(await colour(page, notInstalled), 'Not installed is not drawn in the warning text colour').not.toBe(await probeColour(page, 'var(--ui-warning-text)'))
      expect(await colour(page, notInstalled)).not.toBe(await probeColour(page, 'var(--ui-warning)'))
      const identified = page.locator('[data-agent="claude"] [data-identity="ok"]')
      await expect(identified).toBeVisible({ timeout: 30_000 })
      expect(await colour(page, identified), "an identified agent's badge is not green").not.toBe(await probeColour(page, 'var(--ui-success-text)'))
      expect(await colour(page, identified)).not.toBe(await probeColour(page, 'var(--ui-success)'))
      // The card of an agent not installed is drawn whole, its name muted, never faded.
      const card = page.locator('[data-agent="goose"]')
      expect(await card.evaluate((n) => getComputedStyle(n).opacity)).toBe('1')
      expect(await colour(page, card.locator('[data-agent-name]'))).toBe(await probeColour(page, 'var(--ui-text-muted)'))

      // The Yard's Needs you chip, with the day's questions waiting: its mark amber.
      await page.goto('/yard')
      const glyph = page.locator('[data-yard-filter="needs"] [data-status-mark="needs"] [data-status-glyph]').first()
      await expect(glyph).toBeVisible({ timeout: MEASURE_WAIT_MS })
      expect(await colour(page, glyph), "the Needs you chip's mark is amber").toBe(await probeColour(page, 'var(--ui-warning)'))

      // The Launch agent dialog's Yolo, on: one press beside Launch, never amber, its words the same on and off. Cancelled after.
      await page.goto('/sessions')
      await page.getByRole('button', { name: 'Launch agent' }).first().click()
      const dialog = page.getByRole('dialog')
      // The dialog opens on the agent launched last, the day's Plain, which has no recipe: its switch is rightly off and disabled.
      // Claude Code's stub has one; picked, its switch is the one a person turns on.
      const claude = dialog.getByRole('radiogroup', { name: 'Agent' }).getByRole('radio', { name: /Claude Code \(e2e stub\)$/ })
      await claude.click()
      await expect(claude).toHaveAttribute('aria-checked', 'true')
      const yolo = dialog.locator('[data-slot="footer"] [data-yolo-switch]')
      await expect(yolo).toBeEnabled({ timeout: MEASURE_WAIT_MS })
      if ((await yolo.getAttribute('aria-checked')) !== 'true') await yolo.click()
      await expect(yolo).toHaveAttribute('aria-checked', 'true')
      await expect(dialog.locator('[data-yolo-words]')).toHaveText('skips permission prompts')
      await atRest(yolo)
      expect(await background(page, yolo), 'Yolo on is not amber').not.toBe(await probeColour(page, 'var(--ui-warning)'))
      expect(await colour(page, dialog.locator('[data-yolo-words]')), "Yolo's words are not in the warning text colour").not.toBe(await probeColour(page, 'var(--ui-warning-text)'))
      await dialog.getByRole('button', { name: 'Cancel' }).click()
      await expect(dialog).toHaveCount(0)
    })
  }
}
