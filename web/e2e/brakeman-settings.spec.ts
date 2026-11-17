import type { Locator, Page } from '@playwright/test'
import { brakemanServer, type BrakemanServer } from './brakeman-server'
import { budget, expect, test, useTheme } from './fixtures'
import { noSideways } from './layout'

// Brakeman's settings (#993), on a server of their own with the Brakeman stub (the seeded day holds no Brakeman configuration of
// its own): the card saves Brakeman with the stub as its agent and terminal screens off; it opens on a choice of presets, each of
// which fills only what Brakeman may do and may run (never the terminals, the agent or its folder), Act within grants asking for a
// grant until one is ticked, and a configuration that matches none said as Custom; a preset only fills the form, so a save made
// over a configuration changed elsewhere is refused stale and saves nothing; the background role's window, cooldown and the day's
// minutes sit under Advanced, a fold that keeps its fields and says how many hold a value refused, and its briefing times are a
// list of times; the scope is picked from what the app lists, typed, or kept as saved when it is not listed; and the card lays out
// on a wide window and a phone in both themes without scrolling sideways.
test.describe.configure({ mode: 'serial' })

/** The cap of every wait a step makes without a bound of its own. */
const STEP_MS = 15_000
test.use({ actionTimeout: STEP_MS, navigationTimeout: STEP_MS })

type Config = {
  rev: number
  enabled: boolean
  agentId: string
  cwd: string
  visibility: Record<string, boolean>
  scope: Record<string, string[]>
  permissions: { observe: boolean; draft: boolean; propose: boolean; act?: string[] }
  wake: { mode: string; windowSeconds: number; cooldownMinutes: number; questionMinutes: number; briefings?: string[] }
  budget: { minutesPerDay: number } & Record<string, number>
} & Record<string, unknown>

let srv: BrakemanServer

test.beforeAll(async () => {
  // Brakeman left off: the first test turns it on from Settings.
  srv = await brakemanServer({ name: 'brakeman-settings', enabled: false })
})

test.afterAll(async () => {
  await srv?.stop()
})

const conf = () => srv.api.ok<{ config: Config }>('GET', '/api/brakeman').then((r) => r.config)
/** Saves a configuration through the API, as a save made elsewhere would: the one read now, with `over` over it. */
async function putConf(over: (c: Config) => Partial<Config>) {
  const c = await conf()
  await srv.api.ok('PUT', '/api/brakeman/config', { rev: c.rev, config: { ...c, ...over(c) } })
}
const card = (page: Page) => page.locator('[data-brakeman-settings]')
const field = (page: Page, hook: string) => card(page).locator(`[data-${hook}] input, input[data-${hook}]`).first()
/** Opens Advanced, once its toggle is drawn, and waits for its fields to show. */
async function openAdvanced(page: Page) {
  const toggle = card(page).locator('[data-brakeman-advanced-toggle]')
  await expect(toggle).toHaveAttribute('aria-expanded', 'false')
  await toggle.click()
  await expect(field(page, 'brakeman-reads')).toBeVisible()
}
/** Picks a preset by its words. */
const preset = (page: Page, name: string) => card(page).locator('[data-brakeman-presets]').getByRole('radio', { name, exact: true })
/** A select menu closed: its listbox gone, so that nothing typed next lands in its search (AGENTS.md). */
async function closeMenu(page: Page) {
  await page.keyboard.press('Escape')
  await expect(page.getByRole('listbox')).toHaveCount(0)
}

test('Settings saves Brakeman with the stub as its agent and terminal screens off', async ({ page }) => {
  // The defaults, written on the first read, at the revision the save names.
  const before = await conf()
  expect(before.enabled).toBe(false)
  await srv.open(page, '/settings')
  const c = card(page)
  await expect(c).toBeVisible()
  // Terminal screens are off until a person turns them on, and say what they would let Brakeman read.
  const terminals = c.getByRole('switch', { name: 'Terminal screens' })
  await expect(terminals).toHaveAttribute('aria-checked', 'false')
  await expect(c).toContainText("Lets Brakeman read what a session's screen shows")
  await c.getByRole('switch', { name: 'Brakeman on' }).click()
  // No agent yet: the select says None, which is an item of its own.
  const agent = c.locator('[data-brakeman-agent]')
  await expect(agent).toContainText('None')
  await agent.click()
  await page.getByRole('option', { name: 'Brakeman (e2e stub)' }).click()
  // The stub's adapter is Claude Code's: it takes a model, and can be told to allow Conductor's tools.
  await expect(c.locator('[data-brakeman-model]')).toBeVisible()
  // None is chosen like any agent, and takes the agent's fields away; the stub chosen again for the save.
  await agent.click()
  await page.getByRole('option', { name: 'None', exact: true }).click()
  await expect(agent).toContainText('None')
  await expect(c.locator('[data-brakeman-model]')).toHaveCount(0)
  await agent.click()
  await page.getByRole('option', { name: 'Brakeman (e2e stub)' }).click()
  await expect(c.locator('[data-brakeman-model]')).toBeVisible()
  await page.locator('[data-save-settings]').click()
  await expect
    .poll(async () => {
      const s = await conf()
      return [s.enabled, s.agentId, s.visibility.terminals, s.visibility.runs, s.rev]
    })
    .toEqual([true, 'brakeman-stub', false, true, before.rev + 1])
  await expect(page.locator('[data-save-settings]')).toBeDisabled()
  // Where its turns run (#705): the configured folder, which is not the home folder, reading nothing else.
  const { cwd } = await conf()
  await expect(c.locator('[data-brakeman-folders]')).toHaveText(`Its turns run in ${cwd}.`)
})

test('a preset fills only what Brakeman may do and run: Act within grants asks for a grant, and a configuration no preset makes is Custom', async ({ page }) => {
  // A load and some thirty steps, each within STEP_MS.
  budget(32 * STEP_MS)
  const start = await conf()
  expect([start.permissions.draft, start.permissions.propose, start.permissions.act ?? []]).toEqual([true, true, []])
  await srv.open(page, '/settings')
  const c = card(page)
  const presets = c.locator('[data-brakeman-presets]')
  const draft = c.getByRole('switch', { name: 'Draft', exact: true })
  const propose = c.getByRole('switch', { name: 'Propose', exact: true })
  const terminals = c.getByRole('switch', { name: 'Terminal screens' })
  const hint = c.locator('[data-brakeman-preset-hint]')
  const launch = c.locator('[data-brakeman-grants]').getByRole('switch', { name: 'Launch an approved plan' })
  const save = page.locator('[data-save-settings]')
  // Drafting and proposing with no grant is Draft and propose.
  await expect(presets.locator('[data-brakeman-preset]')).toHaveAttribute('data-brakeman-preset', 'draft')
  await expect(preset(page, 'Draft and propose')).toHaveAttribute('aria-checked', 'true')
  await expect(hint).toHaveCount(0)

  // Terminal screens turned on by the person: no preset turns them off, or on.
  await terminals.click()
  await expect(terminals).toHaveAttribute('aria-checked', 'true')
  // Observe: drafting and proposing off, and the card says it observes only.
  await preset(page, 'Observe').click()
  await expect(preset(page, 'Observe')).toHaveAttribute('aria-checked', 'true')
  await expect(draft).toHaveAttribute('aria-checked', 'false')
  await expect(propose).toHaveAttribute('aria-checked', 'false')
  await expect(c.locator('[data-brakeman-observe-only]')).toBeVisible()
  await expect(terminals).toHaveAttribute('aria-checked', 'true')
  await expect(c.locator('[data-brakeman-agent]')).toContainText('Brakeman (e2e stub)')

  // Act within grants: drafting and proposing on, no grant ticked by the preset, and the card asks for one.
  await expect(launch).toHaveAttribute('data-grant-served', 'true')
  await preset(page, 'Act within grants').click()
  await expect(preset(page, 'Act within grants')).toHaveAttribute('aria-checked', 'true')
  await expect(draft).toHaveAttribute('aria-checked', 'true')
  await expect(propose).toHaveAttribute('aria-checked', 'true')
  for (const sw of await c.locator('[data-brakeman-grants]').getByRole('switch').all()) await expect(sw).toHaveAttribute('aria-checked', 'false')
  await expect(hint).toHaveText('Tick at least one grant. With none, this saves as Draft and propose.')
  await expect(terminals).toHaveAttribute('aria-checked', 'true')
  // A grant ticked by the person: Act within grants holds, and asks for nothing more.
  await launch.click()
  await expect(launch).toHaveAttribute('aria-checked', 'true')
  await expect(hint).toHaveCount(0)
  await expect(preset(page, 'Act within grants')).toHaveAttribute('aria-checked', 'true')
  // Terminal screens back off by the person, and the page's one Save sends the form at the revision read.
  await terminals.click()
  await expect(terminals).toHaveAttribute('aria-checked', 'false')
  await save.click()
  await expect
    .poll(async () => {
      const s = await conf()
      return [s.rev, s.permissions.draft, s.permissions.propose, s.permissions.act, s.visibility.terminals, s.agentId, s.cwd]
    })
    .toEqual([start.rev + 1, true, true, ['launch_approved_plan'], false, start.agentId, start.cwd])
  await expect(save).toBeDisabled()

  // Saved elsewhere with drafting alone: no preset makes that, so the untouched form follows it and says Custom, its switches as saved.
  await putConf((s) => ({ permissions: { ...s.permissions, draft: true, propose: false, act: [] } }))
  await expect(presets.locator('[data-brakeman-preset]')).toHaveAttribute('data-brakeman-preset', 'custom')
  await expect(preset(page, 'Custom')).toHaveAttribute('aria-checked', 'true')
  await expect(preset(page, 'Custom')).toBeDisabled()
  await expect(hint).toContainText('Custom')
  await expect(draft).toHaveAttribute('aria-checked', 'true')
  await expect(propose).toHaveAttribute('aria-checked', 'false')
  // Check again after a redraw the test causes: the theme switched, the page read again.
  await useTheme(page, 'dark')
  await expect(presets.locator('[data-brakeman-preset]')).toHaveAttribute('data-brakeman-preset', 'custom')
  await useTheme(page, 'light')

  // A preset only fills the form: picked, then saved over a configuration changed elsewhere since, the save is refused stale and
  // nothing of the preset is saved; the form keeps it until Read again drops it.
  await preset(page, 'Observe').click()
  await expect(draft).toHaveAttribute('aria-checked', 'false')
  const elsewhere = await conf()
  await putConf((s) => ({ budget: { ...s.budget, turnsPerDay: s.budget.turnsPerDay === 7 ? 8 : 7 } }))
  await save.click()
  const stale = c.locator('[data-brakeman-stale]')
  await expect(stale).toBeVisible()
  const after = await conf()
  expect(after.rev).toBe(elsewhere.rev + 1)
  expect([after.permissions.draft, after.permissions.propose]).toEqual([true, false])
  await expect(preset(page, 'Observe')).toHaveAttribute('aria-checked', 'true')
  await stale.getByRole('button', { name: 'Read again' }).click()
  await page.locator('[data-brakeman-read-again]').click()
  await expect(stale).toHaveCount(0)
  await expect(presets.locator('[data-brakeman-preset]')).toHaveAttribute('data-brakeman-preset', 'custom')
  await expect(save).toBeDisabled()
  // Put back: Draft and propose, no grant.
  await putConf((s) => ({ permissions: { ...s.permissions, draft: true, propose: true, act: [] } }))
})

test("Settings turns the background role on with its window, cooldown, briefing times and the day's minutes, the limits under Advanced", async ({ page }) => {
  budget(26 * STEP_MS)
  await srv.open(page, '/settings')
  const c = card(page)
  const save = page.locator('[data-save-settings]')
  await expect(c.locator('[data-brakeman-role-settings]')).toBeVisible()
  // The limits are folded at first, kept in the page: none shows until Advanced is opened.
  await expect(c.locator('[data-brakeman-window]')).toHaveCount(1)
  await expect(field(page, 'brakeman-window')).toBeHidden()
  await c.getByRole('switch', { name: 'Wake on events' }).click()
  // The briefing times are a list: a row a time, added and taken out.
  const times = c.locator('[data-brakeman-briefings]')
  await expect(times.locator('[data-briefing-time]')).toHaveCount(0)
  await times.locator('[data-brakeman-briefing-add]').click()
  await times.locator('[data-brakeman-briefing-add]').click()
  await expect(times.locator('input')).toHaveCount(2)
  await times.locator('input').nth(0).fill('07:00')
  await times.locator('input').nth(1).fill('18:30')
  await expect(times.locator('input').nth(1)).toHaveValue('18:30')
  await openAdvanced(page)
  await field(page, 'brakeman-window').fill('10')
  await field(page, 'brakeman-cooldown').fill('30')
  // A value the server would refuse is said before the save, and on Advanced's toggle while it is folded.
  await field(page, 'brakeman-minutes').fill('5')
  await expect(c).toContainText('Minutes a day are 10 to 1440.')
  await expect(save).toBeDisabled()
  await c.locator('[data-brakeman-advanced-toggle]').click()
  await expect(c.locator('[data-brakeman-advanced-toggle]')).toHaveText('Advanced: 1 to fix')
  await expect(field(page, 'brakeman-minutes')).toBeHidden()
  await openAdvanced(page)
  await field(page, 'brakeman-minutes').fill('240')
  await expect(c.locator('[data-brakeman-advanced-toggle]')).toHaveText('Advanced')
  await save.click()
  await expect
    .poll(async () => {
      const s = await conf()
      return [s.wake.mode, s.wake.windowSeconds, s.wake.cooldownMinutes, s.wake.briefings, s.budget.minutesPerDay]
    })
    .toEqual(['background', 10, 30, ['07:00', '18:30'], 240])
  await expect(save).toBeDisabled()
  // Each time taken out by its own button, and saved: no briefing time from here on, so none falls due while the spec runs. A row
  // added and left empty is no time, and nothing to save.
  await times.getByRole('button', { name: 'Take this time out' }).first().click()
  await expect(times.locator('input')).toHaveCount(1)
  await expect(times.locator('input').first()).toHaveValue('18:30')
  await times.getByRole('button', { name: 'Take this time out' }).first().click()
  await times.locator('[data-brakeman-briefing-add]').click()
  await expect(times.locator('input')).toHaveCount(1)
  await save.click()
  await expect.poll(async () => (await conf()).wake.briefings ?? []).toEqual([])
  await expect(save).toBeDisabled()
  // Put back: woken only when asked.
  await putConf((s) => ({ wake: { ...s.wake, mode: 'invoked' } }))
})

test('the scope is picked from what the app lists, typed, or kept as saved when it is not listed, and repositories are added by folder', async ({ page }) => {
  budget(30 * STEP_MS)
  // A project of Dispatch's to pick, and a run saved in the scope that no page lists (it ended long ago).
  await srv.api.ok('POST', '/api/dispatch/projects', { opId: `bks-p-${Date.now()}`, key: 'BKS', name: 'Brakeman settings' })
  const gone = 'gone-run-0a1b2c3d'
  await putConf((s) => ({ scope: { ...s.scope, runs: [gone] } }))
  await srv.open(page, '/settings')
  const c = card(page)
  const save = page.locator('[data-save-settings]')
  const runs = c.locator('[data-brakeman-scope="runs"]')
  await expect(runs.locator(`[data-scope-chip="${gone}"]`)).toHaveAttribute('data-scope-unlisted', '')
  // A project picked from Dispatch's list, by its key and name.
  await c.locator('[data-brakeman-scope="projects"]').click()
  await page.getByRole('option', { name: 'BKS · Brakeman settings' }).click()
  await closeMenu(page)
  await expect(c.locator('[data-brakeman-scope="projects"] [data-scope-chip="BKS"]')).toHaveText('BKS · Brakeman settings')
  // A crew typed by its id, though none is saved here.
  await c.locator('[data-brakeman-scope="crews"]').click()
  await page.keyboard.type('nightly-crew')
  await page.getByRole('option', { name: 'Add nightly-crew' }).click()
  await closeMenu(page)
  await expect(c.locator('[data-brakeman-scope="crews"] [data-scope-chip="nightly-crew"]')).toBeVisible()
  // A repository by its folder.
  const repos = c.locator('[data-brakeman-scope="repos"]')
  const repoInput = repos.locator('[data-brakeman-repo-input] input, input[data-brakeman-repo-input]').first()
  await repoInput.fill(srv.repo)
  await expect(repoInput).toHaveValue(srv.repo)
  await repoInput.press('Escape')
  await repos.locator('[data-brakeman-repo-add]').click()
  await expect(repos.locator(`[data-scope-chip="${srv.repo}"]`)).toBeVisible()
  await expect(repoInput).toHaveValue('')
  await save.click()
  await expect
    .poll(async () => (await conf()).scope)
    .toEqual({ projects: ['BKS'], repos: [srv.repo], crews: ['nightly-crew'], runs: [gone] })
  await expect(save).toBeDisabled()
  // Taken out: the run from its picker's list, the repository by its button.
  await runs.click()
  await page.getByRole('option', { name: gone }).click()
  await closeMenu(page)
  await expect(runs.locator('[data-scope-chip]')).toHaveCount(0)
  await repos.getByRole('button', { name: 'Take this repository out' }).click()
  await expect(repos.locator('[data-scope-chip]')).toHaveCount(0)
  await save.click()
  await expect.poll(async () => (await conf()).scope).toEqual({ projects: ['BKS'], crews: ['nightly-crew'] })
  await putConf(() => ({ scope: {} }))
})

for (const size of [
  { width: 1440, height: 900 },
  { width: 390, height: 844 },
]) {
  test(`the card at ${size.width} x ${size.height}, in both themes: nothing scrolls sideways, with Advanced folded and open`, async ({ page }) => {
    budget(20 * STEP_MS)
    await page.setViewportSize(size)
    await srv.open(page, '/settings')
    const c = card(page)
    await expect(c.locator('[data-brakeman-presets]')).toBeVisible()
    for (const theme of ['light', 'dark'] as const) {
      await useTheme(page, theme)
      await expect(preset(page, 'Draft and propose')).toHaveAttribute('aria-checked', 'true')
      // What is in view is measured: the presets, then the scope, then the limits once Advanced is open.
      for (const part of ['[data-brakeman-presets]', '[data-brakeman-scope="repos"]']) {
        await c.locator(part).scrollIntoViewIfNeeded()
        await noSideways(page, { root: c as Locator })
      }
      await openAdvanced(page)
      await c.locator('[data-brakeman-advanced]').scrollIntoViewIfNeeded()
      await noSideways(page, { root: c as Locator })
    }
  })
}
