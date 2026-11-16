import type { Locator, Page } from '@playwright/test'
import { expect, stopStrays, test, type Api } from './fixtures'

// The crew editor's Project manifest section (EN1 PR-6, #398): under the
// tasks of a crew with tasks, where the Setup commands box was. Every phase
// is edited there and saved as the crew's `manifest`; a crew stored with
// Setup commands shows them as the manifest's setup phase and is saved as
// the manifest; every rule the server holds a manifest to is marked where it
// is broken and holds Save; the file switch saves `file: true`. Rows are found
// by their ids, never by a list's length.

interface Step {
  id: string
  argv: string[]
  timeoutSeconds?: number
}

interface ManifestCrew {
  id: string
  goal: string
  setup?: Step[]
  manifest?: Record<string, unknown>
  manifestRevision?: number
}

const crewIds: string[] = []
let runId = ''

test.beforeEach(async ({ api }) => {
  await stopStrays(api, [])
})

test.afterEach(async ({ api }) => {
  if (runId) await api.stopRun(runId)
  runId = ''
  for (const id of crewIds.splice(0)) await api.call('DELETE', `/api/crews/${encodeURIComponent(id)}`)
})

/** Saves a crew with tasks through the API: builder does build, checked by unit; `extra` adds a setup or a manifest. */
async function managedCrew(api: Api, name: string, extra: Record<string, unknown> = {}): Promise<string> {
  const body = {
    name,
    goal: 'prove the project manifest editor',
    cwd: '',
    where: 'server',
    isolation: 'worktree',
    openAfterLaunch: false,
    members: [{ name: 'builder', agentId: 'worker', prompt: '', start: { when: 'task' } }],
    tasks: [{ id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.', checks: [{ id: 'unit', argv: ['true'] }] }],
    ...extra,
  }
  const id = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', body)).crew.id
  crewIds.push(id)
  return id
}

function crewOf(api: Api, id: string): Promise<ManifestCrew> {
  return api.ok<{ crew: ManifestCrew }>('GET', `/api/crews/${encodeURIComponent(id)}`).then((r) => r.crew)
}

/** Types an argv into the chip input labelled `label`, commits it with Enter, and waits for each word to be a chip. */
async function typeArgv(page: Page, label: string, text: string) {
  const field = page.getByLabel(label)
  const input = field.locator('input')
  await input.fill(text)
  await input.press('Enter')
  await expect(input).toHaveValue('')
  for (const word of text.split(' ')) await expect(field.getByRole('button', { name: `Remove ${word}`, exact: true })).toBeVisible()
}

/**
 * Opens the select `trigger` and picks `option`, then waits for its menu to be gone. Closing, the menu stays mounted through its
 * animation with the rest of the page hidden from getByRole, and as it goes it hands the focus back to its trigger: a key pressed
 * in that window (the Enter that commits the next field's argv) lands on the trigger and opens the menu again, and the page stays
 * hidden from then on (#281). The menu gone, the focus is already back, so what is typed next stays where it was typed.
 */
async function pickOption(page: Page, trigger: Locator, option: string | RegExp) {
  await trigger.click()
  await page.getByRole('option', { name: option }).click()
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await expect(trigger).toContainText(option)
}

/** Adds a step to a phase's list (its first row) with an id and a command, and finds the row by its id. */
async function addStep(page: Page, phase: string, what: string, id: string, command: string) {
  const section = page.locator(`[data-manifest-phase="${phase}"]`)
  await section.getByRole('button', { name: `Add ${what}` }).click()
  await page.getByLabel(`Id of ${what} 1`, { exact: true }).fill(id)
  await typeArgv(page, `Command of ${what} 1`, command)
  const row = section.locator(`[data-check-id="${id}"]`)
  await expect(row).toBeVisible()
  return row
}

/** Adds a name to a chip input (the env names, the ports). */
async function addName(page: Page, label: string, name: string) {
  const input = page.getByLabel(label, { exact: true })
  await input.fill(name)
  await input.press('Enter')
}

async function openCrew(page: Page, id: string) {
  await page.goto(`/crews/${encodeURIComponent(id)}`)
  await expect(page.locator('[data-crew-kind]')).toHaveText('Saved crew')
  await expect(page.locator('[data-crew-manifest]')).toBeVisible()
}

const saveButton = (page: Page) => page.locator('[data-unsaved-bar]').getByRole('button', { name: 'Save' })

/** Saves the draft and waits for the unsaved bar to go, which it does once the server has the crew. */
async function save(page: Page) {
  const button = saveButton(page)
  await expect(button).not.toHaveAttribute('data-save-problem', /./)
  await expect(button).toBeEnabled()
  await button.click()
  await expect(page.locator('[data-unsaved-bar]')).toHaveCount(0)
}

test('a crew with tasks takes a project manifest with every phase under its tasks, saved and read back as sent, and fits a phone', async ({ page, api }) => {
  const id = await managedCrew(api, 'e2e manifest phases')
  await openCrew(page, id)
  // The section is under the tasks, and the tasks table has no Setup box at its foot any more.
  const tasks = await page.locator('[data-crew-tasks-section]').boundingBox()
  const section = await page.locator('[data-crew-manifest]').boundingBox()
  expect(section!.y).toBeGreaterThan(tasks!.y + tasks!.height - 1)
  await expect(page.locator('[data-crew-tasks-section] [data-show-setup], [data-crew-setup]')).toHaveCount(0)
  await expect(page.locator('[data-crew-manifest]')).toContainText('not an agent recipe')

  await addStep(page, 'prerequisites', 'prerequisite', 'python', 'python3 --version')
  await addName(page, 'Env names', 'NPM_TOKEN')
  await addName(page, 'Port names', 'web')
  const deps = await addStep(page, 'setup', 'setup step', 'deps', 'mkdir -p ${CONDUCTOR_STATE_DIR}/cache')
  await expect(deps.getByText('${CONDUCTOR_STATE_DIR}/cache', { exact: true })).toBeVisible()
  // A service has no timeout: it runs as long as its tree.
  const site = await addStep(page, 'services', 'service', 'site', 'python3 -m http.server ${CONDUCTOR_PORT_WEB}')
  await expect(site.getByLabel(/^Timeout in seconds/)).toHaveCount(0)

  // Two probes: a URL (a new probe's kind) and a command.
  const ready = page.locator('[data-manifest-phase="ready"]')
  await ready.getByRole('button', { name: 'Add ready probe' }).click()
  await page.getByLabel('Id of ready probe 1', { exact: true }).fill('up')
  await page.getByLabel('URL of ready probe 1', { exact: true }).fill('http://127.0.0.1:${CONDUCTOR_PORT_WEB}/')
  await page.getByLabel('Interval in seconds of ready probe 1', { exact: true }).fill('1')
  // A duration is kept as typed: a negative or a fraction is marked and holds Save, never turned into the default or a whole number.
  const up = ready.locator('[data-probe-id="up"]')
  const timeout = page.getByLabel('Timeout in seconds of ready probe 1', { exact: true })
  for (const typed of ['-1', '1.5', '3600.5']) {
    await timeout.fill(typed)
    await expect(up.locator('[data-probe-error]'), typed).toHaveText('the timeout is 1 to 3600 seconds')
    await expect(saveButton(page)).toHaveAttribute('data-save-problem', 'Ready probe 1: the timeout is 1 to 3600 seconds')
  }
  await timeout.fill('30')
  await expect(up.locator('[data-probe-error]')).toHaveCount(0)
  await ready.getByRole('button', { name: 'Add ready probe' }).click()
  await page.getByLabel('Id of ready probe 2', { exact: true }).fill('statedir')
  // A URL typed first: switched to a command, the probe keeps the command alone (saved below with no url).
  await page.getByLabel('URL of ready probe 2', { exact: true }).fill('http://127.0.0.1:${CONDUCTOR_PORT_WEB}/state')
  await pickOption(page, page.getByLabel('Kind of ready probe 2', { exact: true }), 'command')
  await typeArgv(page, 'Command of ready probe 2', 'test -d ${CONDUCTOR_STATE_DIR}')
  await expect(ready.locator('[data-probe-id="up"]')).toBeVisible()
  await expect(ready.locator('[data-probe-id="statedir"]')).toBeVisible()

  const preview = page.getByLabel('Preview URL', { exact: true })
  await preview.fill('http://127.0.0.1:${CONDUCTOR_PORT_WEB}/')
  await expect(preview).toHaveValue('http://127.0.0.1:${CONDUCTOR_PORT_WEB}/')
  const lint = await addStep(page, 'verify', 'verify step', 'lint', 'true')
  // A step's timeout as typed too.
  await lint.getByLabel('Timeout in seconds of verify step 1', { exact: true }).fill('-1')
  await expect(lint.locator('[data-check-error]')).toHaveText('the timeout is 1 to 3600 seconds')
  await lint.getByLabel('Timeout in seconds of verify step 1', { exact: true }).fill('120')
  await expect(lint.locator('[data-check-error]')).toHaveCount(0)
  await addStep(page, 'cleanup', 'cleanup step', 'tidy', 'rm -rf ${CONDUCTOR_STATE_DIR}/cache')
  await expect(page.locator('[data-crew-manifest] [data-check-error], [data-crew-manifest] [data-probe-error], [data-manifest-error]')).toHaveCount(0)

  await save(page)
  const crew = await crewOf(api, id)
  expect(crew.setup).toBeUndefined()
  expect(crew.manifestRevision).toBe(1)
  expect(crew.manifest).toEqual({
    prerequisites: [{ id: 'python', argv: ['python3', '--version'] }],
    env: ['NPM_TOKEN'],
    ports: ['web'],
    setup: [{ id: 'deps', argv: ['mkdir', '-p', '${CONDUCTOR_STATE_DIR}/cache'] }],
    services: [{ id: 'site', argv: ['python3', '-m', 'http.server', '${CONDUCTOR_PORT_WEB}'] }],
    ready: [
      { id: 'up', url: 'http://127.0.0.1:${CONDUCTOR_PORT_WEB}/', timeoutSeconds: 30, intervalSeconds: 1 },
      { id: 'statedir', argv: ['test', '-d', '${CONDUCTOR_STATE_DIR}'] },
    ],
    preview: 'http://127.0.0.1:${CONDUCTOR_PORT_WEB}/',
    verify: [{ id: 'lint', argv: ['true'], timeoutSeconds: 120 }],
    cleanup: [{ id: 'tidy', argv: ['rm', '-rf', '${CONDUCTOR_STATE_DIR}/cache'] }],
  })

  // Read again, the editor shows the manifest as saved: no change.
  await openCrew(page, id)
  await expect(page.locator('[data-manifest-phase="services"] [data-check-id="site"]')).toBeVisible()
  await expect(page.locator('[data-unsaved-bar]')).toHaveCount(0)

  // At a phone's width the section keeps to the window: nothing in it is wider than its box.
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(page.locator('[data-manifest-phase="cleanup"] [data-check-id="tidy"]')).toBeVisible()
  const widths = await page.evaluate(() => {
    const el = document.querySelector('[data-crew-manifest]')!
    return { past: el.scrollWidth - el.clientWidth, right: el.getBoundingClientRect().right - document.documentElement.clientWidth }
  })
  expect(widths.past, "the section's content past its box").toBeLessThanOrEqual(0)
  expect(widths.right, "the section's right edge past the window").toBeLessThanOrEqual(0)
})

test("an old crew's Setup commands are the manifest's setup phase, saved as the manifest with its revision kept", async ({ page, api }) => {
  const id = await managedCrew(api, 'e2e manifest lifted', { setup: [{ id: 'deps', argv: ['true'], timeoutSeconds: 120 }] })
  const stored = await crewOf(api, id)
  expect(stored.setup).toEqual([{ id: 'deps', argv: ['true'], timeoutSeconds: 120 }])
  expect(stored.manifest).toBeUndefined()

  await openCrew(page, id)
  const row = page.locator('[data-manifest-phase="setup"] [data-check-id="deps"]')
  await expect(row).toBeVisible()
  await expect(row.getByText('true', { exact: true })).toBeVisible()
  await expect(row.getByLabel('Timeout in seconds of setup step 1', { exact: true })).toHaveValue('120')
  // Shown there, the setup is no change by itself.
  await expect(page.locator('[data-unsaved-bar]')).toHaveCount(0)

  // Saved with another change, it is the manifest's setup phase, the same manifest to the server.
  await page.getByLabel('Goal').fill('prove the project manifest editor, saved again')
  await save(page)
  const crew = await crewOf(api, id)
  expect(crew.goal).toBe('prove the project manifest editor, saved again')
  expect(crew.setup).toBeUndefined()
  expect(crew.manifest).toEqual({ setup: [{ id: 'deps', argv: ['true'], timeoutSeconds: 120 }] })
  expect(crew.manifestRevision).toBe(stored.manifestRevision)
})

test("an old setup sharing a check's id: its row says so, Launch runs the crew as stored, and Save waits for the id to change", async ({ page, api }) => {
  // A legacy setup may share an id with a task's check (main's rule for it); a manifest's step may not.
  const id = await managedCrew(api, 'e2e manifest clash', { setup: [{ id: 'unit', argv: ['true'] }] })
  const revision = (await crewOf(api, id)).manifestRevision ?? 0
  await openCrew(page, id)
  const row = page.locator('[data-manifest-phase="setup"] [data-check-id="unit"]')
  await expect(row.locator('[data-check-error]')).toHaveText("unit is also a check of task build: a step's id is its own")
  await expect(page.locator('[data-unsaved-bar]')).toHaveCount(0)

  // Unchanged, the crew launches as it is stored. Its run is the one this launch made: a run of an earlier crew of the same id (a
  // repetition's, stopped) is still listed.
  const runs = async () => (await api.ok<{ runs: Array<{ id: string; crewId: string }> }>('GET', '/api/runs')).runs
  const before = new Set((await runs()).map((r) => r.id))
  const launch = page.locator('[data-launch]')
  await expect(launch).toBeEnabled()
  await launch.click()
  // A launch asks first (docs/plans/capacity.md, PR-7); its Launch launches.
  await page.locator('[data-crew-launch-confirm]').click()
  await expect
    .poll(async () => {
      runId = (await runs()).find((r) => r.crewId === id && !before.has(r.id))?.id ?? ''
      return runId
    }, { message: 'the launch made a run of the crew' })
    .not.toBe('')
  const stillStored = await crewOf(api, id)
  expect(stillStored.setup).toEqual([{ id: 'unit', argv: ['true'] }])
  expect(stillStored.manifest).toBeUndefined()

  // Changed, a save is the manifest's, which the clash refuses: Save and Launch wait for the id to change.
  await page.getByLabel('Goal').fill('prove the project manifest editor, changed')
  const saveIt = saveButton(page)
  await expect(saveIt).toBeDisabled()
  await expect(saveIt).toHaveAttribute('data-save-problem', "Setup step 1: unit is also a check of task build: a step's id is its own")
  await expect(launch).toBeDisabled()
  await page.getByLabel('Id of setup step 1', { exact: true }).fill('deps')
  await expect(page.locator('[data-manifest-phase="setup"] [data-check-id="deps"] [data-check-error]')).toHaveCount(0)
  await save(page)
  const crew = await crewOf(api, id)
  expect(crew.setup).toBeUndefined()
  expect(crew.manifest).toEqual({ setup: [{ id: 'deps', argv: ['true'] }] })
  expect(crew.manifestRevision).toBe(revision + 1)
})

test('an id used twice, and a name the server refuses, hold Save with their words', async ({ page, api }) => {
  const id = await managedCrew(api, 'e2e manifest ids')
  await openCrew(page, id)
  await addStep(page, 'setup', 'setup step', 'deps', 'true')
  await addStep(page, 'cleanup', 'cleanup step', 'tidy', 'true')
  await page.getByLabel('Id of cleanup step 1', { exact: true }).fill('deps')
  const cleanup = page.locator('[data-manifest-phase="cleanup"] [data-check-id="deps"]')
  await expect(cleanup.locator('[data-check-error]')).toHaveText('Another one has this id')
  const saveIt = saveButton(page)
  await expect(saveIt).toBeDisabled()
  await expect(saveIt).toHaveAttribute('data-save-problem', 'Cleanup step 1: Another one has this id')
  await expect(page.locator('[data-launch]')).toBeDisabled()

  // A task's check id is taken too.
  await page.getByLabel('Id of cleanup step 1', { exact: true }).fill('unit')
  await expect(page.locator('[data-manifest-phase="cleanup"] [data-check-id="unit"] [data-check-error]')).toHaveText("unit is also a check of task build: a step's id is its own")
  await page.getByLabel('Id of cleanup step 1', { exact: true }).fill('tidy')
  await expect(page.locator('[data-crew-manifest] [data-check-error]')).toHaveCount(0)
  await expect(saveIt).toBeEnabled()

  // A name the server refuses is marked under its field, and holds Save until it goes.
  await addName(page, 'Env names', 'CONDUCTOR_TOKEN')
  const env = page.locator('[data-manifest-field="env"]')
  await expect(env).toContainText("Env CONDUCTOR_TOKEN is Conductor's own: a CONDUCTOR_ name never comes from the host")
  await expect(saveIt).toHaveAttribute('data-save-problem', /^Env CONDUCTOR_TOKEN is Conductor's own/)
  await env.getByRole('button').first().click()
  await expect(env).not.toContainText('CONDUCTOR_TOKEN')
  await save(page)
  expect((await crewOf(api, id)).manifest).toEqual({ setup: [{ id: 'deps', argv: ['true'] }], cleanup: [{ id: 'tidy', argv: ['true'] }] })
})

test('on a crew page taller than the window the unsaved-changes bar stays at the bottom at every scroll, Save and Discard in reach (#592)', async ({ page, api }) => {
  const id = await managedCrew(api, 'e2e manifest long page')
  await openCrew(page, id)
  await addStep(page, 'setup', 'setup step', 'deps', 'true')
  await addName(page, 'Env names', 'NPM_TOKEN')
  const bar = page.locator('[data-unsaved-bar]')
  await expect(bar).toBeVisible()
  // The page scrolls in the panel's body, the crew page's parent; its content is taller than the window.
  const crewPage = page.locator('[data-crew-page]')
  const tall = await crewPage.evaluate((el) => ({ content: el.scrollHeight, window: window.innerHeight }))
  expect(tall.content).toBeGreaterThan(tall.window)

  /** Scrolls the body to `to` (a share of what it can scroll) and reads where the bar, the body and the page's end are. */
  const at = (to: number) =>
    crewPage.evaluate((el, to) => {
      const body = el.parentElement!
      body.scrollTo({ top: to * (body.scrollHeight - body.clientHeight), behavior: 'instant' })
      const sticky = document.querySelector('[data-unsaved-bar]')!.parentElement!.getBoundingClientRect()
      const hits = Array.from(document.querySelectorAll<HTMLElement>('[data-unsaved-bar] button')).map((b) => {
        const r = b.getBoundingClientRect()
        const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2)
        return { label: b.textContent?.trim() ?? '', reached: !!hit && b.contains(hit) }
      })
      return {
        scrolled: body.scrollTop,
        most: body.scrollHeight - body.clientHeight,
        barTop: sticky.top,
        barBottom: sticky.bottom,
        bodyBottom: body.getBoundingClientRect().bottom,
        pageBottom: el.getBoundingClientRect().bottom,
        window: window.innerHeight,
        hits,
      }
    }, to)

  for (const [where, to] of [['top', 0], ['middle', 0.5], ['end', 1]] as const) {
    const m = await at(to)
    expect(m.bodyBottom, where).toBeCloseTo(m.window, 0)
    expect(m.barBottom, where).toBeCloseTo(m.window, 0)
    expect(m.hits, where).toEqual([
      { label: 'Discard', reached: true },
      { label: 'Save', reached: true },
    ])
    if (where === 'middle') expect(m.scrolled, where).toBeGreaterThan(m.barBottom - m.barTop)
    if (where === 'end') {
      expect(m.scrolled, where).toBeCloseTo(m.most, 0)
      // At the end the bar sits under the page's last field rather than over it.
      expect(m.pageBottom, where).toBeLessThanOrEqual(m.barTop + 1)
    }
  }
  await save(page)
  expect((await crewOf(api, id)).manifest).toEqual({ setup: [{ id: 'deps', argv: ['true'] }], env: ['NPM_TOKEN'] })
})

test('the file switch saves file: true and hides the lists, and gives them back when it is turned off', async ({ page, api }) => {
  const id = await managedCrew(api, 'e2e manifest file', { manifest: { setup: [{ id: 'deps', argv: ['true'] }] } })
  await openCrew(page, id)
  await expect(page.locator('[data-manifest-phase="setup"] [data-check-id="deps"]')).toBeVisible()
  const file = page.getByRole('switch', { name: /conductor\.manifest\.json/ })
  await file.click()
  await expect(file).toBeChecked()
  await expect(page.locator('[data-manifest-phase]')).toHaveCount(0)
  await expect(page.locator('[data-manifest-file-note]')).toBeVisible()
  await save(page)
  const crew = await crewOf(api, id)
  expect(crew.manifest).toEqual({ file: true })
  expect(crew.manifestRevision).toBe(2)

  // Turned off, the lists come back as they were.
  await file.click()
  await expect(file).not.toBeChecked()
  await expect(page.locator('[data-manifest-phase="setup"] [data-check-id="deps"]')).toBeVisible()

  // Edited, set aside by the switch, and the Runs tab visited (the editor goes away): turned off on return, the edited lists come back.
  await addStep(page, 'cleanup', 'cleanup step', 'tidy', 'true')
  await file.click()
  await expect(file).toBeChecked()
  await page.getByRole('tab', { name: /Runs/ }).click()
  await expect(page.locator('[data-crew-manifest]')).toHaveCount(0)
  await page.getByRole('tab', { name: /Setup/ }).click()
  const back = page.getByRole('switch', { name: /conductor\.manifest\.json/ })
  await expect(back).toBeChecked()
  await back.click()
  await expect(back).not.toBeChecked()
  await expect(page.locator('[data-manifest-phase="setup"] [data-check-id="deps"]')).toBeVisible()
  await expect(page.locator('[data-manifest-phase="cleanup"] [data-check-id="tidy"]')).toBeVisible()
  await save(page)
  expect((await crewOf(api, id)).manifest).toEqual({ setup: [{ id: 'deps', argv: ['true'] }], cleanup: [{ id: 'tidy', argv: ['true'] }] })
})

test("a task's new check is given an id no step of the manifest has", async ({ page, api }) => {
  const id = await managedCrew(api, 'e2e manifest check ids')
  await openCrew(page, id)
  await addStep(page, 'setup', 'setup step', 'check', 'true')
  await page.locator('[data-crew-task][data-task-id="build"] [data-task-checks]').click()
  const checks = page.locator('[data-crew-checks="check"]')
  await checks.locator('[data-add-check]').click()
  await expect(checks.locator('[data-check-id="check-2"]')).toBeVisible()
  await expect(checks.locator('[data-check-id="check"]')).toHaveCount(0)
  await expect(page.locator('[data-manifest-phase="setup"] [data-check-id="check"] [data-check-error]')).toHaveCount(0)
})

test('a crew switched back to start rules drops its project manifest, and saves with none', async ({ page, api }) => {
  const id = await managedCrew(api, 'e2e manifest dropped', { manifest: { setup: [{ id: 'deps', argv: ['true'] }] } })
  await openCrew(page, id)
  await pickOption(page, page.locator('[data-crew-coordination]'), /by start rules/)
  await expect(page.locator('[data-crew-manifest]')).toHaveCount(0)
  await expect(page.locator('[data-crew-tasks-section]')).toHaveCount(0)
  // Nothing holds the save: no manifest is left behind that the page no longer shows.
  await save(page)
  const crew = await crewOf(api, id)
  expect(crew.manifest).toBeUndefined()
  expect(crew.setup).toBeUndefined()
  expect((crew as ManifestCrew & { tasks?: unknown }).tasks).toBeUndefined()
})
