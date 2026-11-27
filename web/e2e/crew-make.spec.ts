import { randomBytes } from 'node:crypto'
import type { Locator, Page, Route } from '@playwright/test'
import { budget, expect, test, type Run } from './fixtures'
import { noSideways, tapTarget, uncut } from './layout'

// Making a crew and starting it, by the Setup's labelled fields (issue 1024's review, C7 to C10 and C16): a new crew's Name field is the
// first of its Setup and has the focus; a crew that cannot be saved says why beside the field it is about and beside its held Save (no
// name, a name another crew has, a member's name twice, a member's name with characters it may not have); a new crew's button reads
// "Save and launch": it saves, then opens the launch sheet, a short table of who it starts; and the run starts from there. Every crew
// and run is this spec's own, found by its id.

const made = { crews: [] as string[], runs: [] as string[] }
test.afterEach(async ({ api }) => {
  for (const id of made.runs.splice(0)) await api.stopRun(id)
  for (const id of made.crews.splice(0)) await api.call('DELETE', `/api/crews/${encodeURIComponent(id)}`)
})

/** The config's bound of an expect, of an action, and of a page's load: each group of steps raises the test's timeout by its own (budget). */
const EXPECT = 15_000

const unique = (what: string) => `e2e ${what} ${randomBytes(3).toString('hex')}`

/** Picks a select's option and waits for its menu to be gone, so the next keys reach the page (a menu that has closed is not gone). */
async function pick(page: Page, trigger: Locator, option: string) {
  await trigger.click()
  await page.getByRole('option', { name: option, exact: true }).click()
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await expect(trigger).toContainText(option)
}

test('a crew of two made from nothing by its labelled fields says why it cannot be saved, then saves and launches from its sheet', async ({ page, api }) => {
  // A crew another person saved: its name is taken. The page knows the crews it lists (a hundred, by name), so its name sorts first.
  const taken = `0 ${unique('taken')}`
  const { crew: other } = await api.ok<{ crew: { id: string } }>('POST', '/api/crews', { name: taken, goal: '', cwd: '', where: 'server', isolation: 'none', openAfterLaunch: false, members: [{ name: 'lead', agentId: 'claude', prompt: '', start: { when: 'manual' } }] })
  made.crews.push(other.id)
  const { agents } = await api.ok<{ agents: Array<{ id: string; name: string }> }>('GET', '/api/catalog')

  budget(6 * EXPECT) // the home, New crew, the URL, the label, the focus, the first field
  await page.goto('/crews')
  await page.locator('[data-new-crew]').click()
  await expect(page).toHaveURL(/\/crews\/new$/)
  // The Name field: labelled, the first of the Setup, and it has the focus.
  const field = page.locator('[data-crew-name-field]')
  await expect(field.locator('label')).toHaveText('Name')
  const name = field.getByRole('textbox', { name: 'Crew name' })
  await expect(name).toBeFocused()
  await expect(page.locator('[data-crew-editor]').getByRole('textbox').first()).toHaveAccessibleName('Crew name')

  // No name yet: said beside the field and beside the held Save, and the launch is held with it.
  budget(4 * EXPECT)
  const bar = page.locator('[data-unsaved-bar]')
  const save = bar.getByRole('button', { name: 'Save', exact: true })
  const reason = bar.locator('[data-save-reason]')
  const launch = page.locator('[data-launch]')
  await expect(field).toContainText('A crew needs a name')
  await expect(reason).toHaveText('A crew needs a name')
  await expect(save).toBeDisabled()
  await expect(launch).toBeDisabled()

  // A name another crew has, typed into the focused field; then a name of its own, the goal and the workspace (a pick is three waits).
  budget(15 * EXPECT)
  await page.keyboard.type(taken.toUpperCase())
  await expect(name).toHaveValue(taken.toUpperCase())
  const takenWords = `Another crew is called ${taken}: give this one a name of its own`
  await expect(field).toContainText(takenWords)
  await expect(reason).toHaveText(takenWords)
  await expect(save).toBeDisabled()

  const crewName = unique('made')
  await name.fill(crewName)
  await expect(name).toHaveValue(crewName)
  await expect(reason).toHaveCount(0)
  await expect(save).toBeEnabled()
  await expect(page.locator('[data-crew-title]')).toHaveText(crewName)
  await page.getByLabel('Goal').fill('show how a crew is made')
  await expect(page.getByLabel('Goal')).toHaveValue('show how a crew is made')
  await pick(page, page.locator('[data-crew-workspace]'), 'One folder for everyone')

  // A second member: named as the first, then with characters a name may not have, each said beside its field and beside Save.
  budget(16 * EXPECT)
  await page.locator('[data-add-member]').click()
  const rows = page.locator('[data-crew-member]')
  await expect(rows).toHaveCount(2)
  const second = rows.nth(1).getByRole('textbox', { name: /^Name of / })
  await second.fill('lead')
  await expect(second).toHaveValue('lead')
  await expect(rows.nth(1).locator('[data-name-error]')).toHaveText('Another member has this name')
  await expect(reason).toHaveText('Two members are called lead: give each a name of its own')
  await expect(save).toBeDisabled()
  await second.fill('Checker!')
  await expect(second).toHaveValue('Checker!')
  await expect(rows.nth(1).locator('[data-name-error]')).toHaveText('Lower-case letters, digits, ".", "_" and "-", starting with a letter or digit, at most 40')
  await expect(reason).toHaveText('The member name Checker! may hold only lower-case letters, digits, ".", "_" and "-", starting with a letter or digit, at most 40')
  await expect(save).toBeDisabled()
  await second.fill('checker')
  await expect(second).toHaveValue('checker')
  await expect(reason).toHaveCount(0)
  await pick(page, rows.nth(1).locator('[data-member-start]'), 'When you press Start')
  // Its model, labelled, empty: the agent's choice.
  await expect(rows.nth(1).getByRole('textbox', { name: 'Model for checker' })).toHaveAttribute('placeholder', "The agent's choice")

  // A draft's button saves, then opens the sheet on the crew as saved; its table, its Launch, the run page.
  budget(17 * EXPECT)
  await expect(launch).toHaveAccessibleName('Save and launch')
  const created = page.waitForResponse((r) => r.request().method() === 'POST' && new URL(r.url()).pathname === '/api/crews')
  await launch.click()
  const { crew } = (await (await created).json()) as { crew: { id: string } }
  made.crews.push(crew.id)
  await expect(page).toHaveURL(new RegExp(`/crews/${crew.id}$`))
  // The sheet: a row a member, member · agent · model · account in plain words; its permission prompts in one line.
  const first = agents[0]!.name
  const sheet = page.locator('[data-crew-launch-sheet]')
  await expect(sheet.getByRole('columnheader')).toHaveText(['Member', 'Agent', 'Model', 'Account'])
  for (const member of ['lead', 'checker']) {
    const row = sheet.locator(`[data-crew-launch-line="${member}"]`)
    await expect(row.getByRole('cell').first()).toHaveText(member)
    await expect(row.locator('[data-crew-launch-agent]')).toHaveText(first)
    await expect(row.locator('[data-crew-launch-model]')).toHaveText("The agent's choice")
    await expect(row.locator('[data-crew-launch-account]')).not.toBeEmpty()
  }
  await expect(page.locator('[data-crew-launch-yolo]')).toHaveText(/^Yolo: (on|off)\. /)
  // The header's button now reads Launch run, the crew saved (the page behind the sheet is hidden from the accessibility tree).
  await expect(launch).toHaveAttribute('aria-label', 'Launch run')
  const started = page.waitForResponse((r) => r.request().method() === 'POST' && new URL(r.url()).pathname === `/api/crews/${crew.id}/launch`)
  await page.locator('[data-crew-launch-confirm]').click()
  const answer = await started
  expect(answer.status()).toBe(201)
  const { run } = (await answer.json()) as { run: Run }
  made.runs.push(run.id)
  // A new crew opens its run after the launch.
  await expect(page).toHaveURL(new RegExp(`/runs/${run.id}$`))
  expect(run.members.map((m) => m.name)).toEqual(['lead', 'checker'])
  const { crew: saved } = await api.ok<{ crew: { name: string; isolation: string; members: Array<{ name: string; start: { when: string } }> } }>('GET', `/api/crews/${crew.id}`)
  expect([saved.name, saved.isolation, saved.members.map((m) => [m.name, m.start.when])]).toEqual([crewName, 'none', [['lead', 'immediately'], ['checker', 'manual']]])
})

/** The first `method` request `match` takes, held at this test's route until `release` (`seen` once it is held); the others pass. */
async function holdOnce(page: Page, match: (u: URL) => boolean, method: string) {
  let seen!: () => void
  let release!: () => void
  const at = new Promise<void>((r) => (seen = r))
  const gate = new Promise<void>((r) => (release = r))
  let first = true
  const handler = async (route: Route) => {
    if (route.request().method() !== method || !first) return route.fallback()
    first = false
    seen()
    await gate
    await route.fallback()
  }
  await page.route(match, handler)
  return { seen: at, release, off: () => page.unroute(match, handler) }
}

/** The app's router, through the page itself: a move inside the app, as a link to another crew makes it (the page stays mounted). */
const navigate = (page: Page, path: string) =>
  page.evaluate((u) => (document.querySelector('#__nuxt') as unknown as { __vue_app__: { config: { globalProperties: { $router: { push: (u: string) => Promise<unknown> } } } } }).__vue_app__.config.globalProperties.$router.push(u), path)

for (const button of ['Save and launch', 'Save'] as const) {
  test(`a new crew's ${button} that lands after the person went to another crew saves it and moves nobody: no sheet opens there`, async ({ page, api }) => {
    budget(12 * EXPECT) // the draft, the held save, the other crew, the answer, where the person is, the words, no sheet
    const other = unique('elsewhere')
    const { crew: b } = await api.ok<{ crew: { id: string } }>('POST', '/api/crews', { name: other, goal: '', cwd: '', where: 'server', isolation: 'none', openAfterLaunch: false, members: [{ name: 'lead', agentId: 'claude', prompt: '', start: { when: 'manual' } }] })
    made.crews.push(b.id)
    await page.goto('/crews/new')
    const name = page.getByRole('textbox', { name: 'Crew name' })
    await expect(name).toBeFocused()
    const crewName = unique('left')
    await name.fill(crewName)
    await expect(name).toHaveValue(crewName)
    // The new crew's save held at this test's route until the person is on the other crew's page.
    const held = await holdOnce(page, (u) => u.pathname === '/api/crews', 'POST')
    if (button === 'Save') await page.locator('[data-unsaved-bar]').getByRole('button', { name: 'Save', exact: true }).click()
    else await page.locator('[data-launch]').click()
    await held.seen
    await navigate(page, `/crews/${encodeURIComponent(b.id)}`)
    await expect(name).toHaveValue(other)
    const answered = page.waitForResponse((r) => r.request().method() === 'POST' && new URL(r.url()).pathname === '/api/crews')
    held.release()
    const { crew } = (await (await answered).json()) as { crew: { id: string; name: string } }
    made.crews.push(crew.id)
    await held.off()
    // Saved, and said so; the person stays on the crew they went to, and no sheet opens there.
    await expect(page.getByText(button === 'Save' ? 'Crew saved' : 'its launch sheet was not opened').first()).toBeVisible()
    await expect(page).toHaveURL(new RegExp(`/crews/${b.id}$`))
    await expect(name).toHaveValue(other)
    await expect(page.locator('[data-crew-launch-confirm]')).toHaveCount(0)
    expect(crew.name).toBe(crewName)
    expect((await api.ok<{ crew: { name: string } }>('GET', `/api/crews/${encodeURIComponent(crew.id)}`)).crew.name).toBe(crewName)
  })
}

test('a saved crew with edits says so beside its launch, which saves them first; More options keeps every choice one press away', async ({ page, api }) => {
  const crewName = unique('edits')
  const { crew } = await api.ok<{ crew: { id: string } }>('POST', '/api/crews', { name: crewName, goal: '', cwd: '', where: 'server', isolation: 'none', openAfterLaunch: false, yolo: false, members: [{ name: 'lead', agentId: 'claude', prompt: '', start: { when: 'manual' } }] })
  made.crews.push(crew.id)
  await page.goto(`/crews/${crew.id}`)
  const launch = page.locator('[data-launch]')
  await expect(page.getByRole('textbox', { name: 'Crew name' })).toHaveValue(crewName)
  await expect(launch).toHaveAccessibleName('Launch run')
  await expect(page.locator('[data-launch-unsaved]')).toHaveCount(0)
  // Yolo is in view, never in the fold (UX3 R87): picked with More options closed.
  await expect(page.locator('[data-crew-more] [data-crew-yolo]')).toHaveCount(0)
  await pick(page, page.getByRole('combobox', { name: 'Yolo' }), 'On: members act without asking')
  // Folded, the line beside More options says each choice; opened, each is a labelled control.
  await expect(page.locator('[data-crew-more-line]')).toHaveText('Stays on this page · No view link')
  await page.locator('[data-crew-more-options]').click()
  await expect(page.locator('[data-crew-more]')).toBeVisible()
  await pick(page, page.getByRole('combobox', { name: 'After launch' }), 'Open the run')
  const viewLink = page.locator('[data-crew-more]').getByRole('switch')
  await expect(page.locator('[data-crew-more]')).toContainText('Make one for 8 h')
  await viewLink.click()
  await expect(viewLink).toHaveAttribute('aria-checked', 'true')
  await expect(page.locator('[data-unsaved-words]')).toHaveText('Yolo, More options edited.')
  // The edits are said beside the launch, which saves them before the sheet opens.
  await expect(page.locator('[data-launch-unsaved]')).toHaveText('Unsaved changes')
  await expect(launch).toHaveAccessibleName('Save and launch')
  const put = page.waitForRequest((r) => r.method() === 'PUT' && new URL(r.url()).pathname === `/api/crews/${crew.id}`)
  await launch.click()
  expect((await put).postDataJSON()).toMatchObject({ openAfterLaunch: true, yolo: true, viewLinkTtlSeconds: 8 * 3600 })
  await expect(page.locator('[data-crew-launch-line="lead"]')).toBeVisible()
  await expect(page.locator('[data-crew-launch-yolo]')).toHaveText("Yolo: on. The members act without asking (the crew's setting)")
  await expect(page.locator('[data-launch-unsaved]')).toHaveCount(0)
  await page.locator('[data-crew-launch-cancel]').click()
  await expect(page.locator('[data-crew-launch-confirm]')).toHaveCount(0)
  expect(await api.ok<{ runs: unknown[] }>('GET', `/api/crews/${crew.id}/runs`).then((r) => r.runs)).toEqual([])
})

test("on a phone a member is a card: each control labelled and 44 px to press, and the launch sheet's table fits the window", async ({ page, api }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const crewName = unique('phone')
  const { crew } = await api.ok<{ crew: { id: string } }>('POST', '/api/crews', { name: crewName, goal: '', cwd: '', where: 'server', isolation: 'none', openAfterLaunch: false, yolo: false, members: [{ name: 'lead', agentId: 'claude', prompt: '', start: { when: 'manual' } }] })
  made.crews.push(crew.id)
  await page.goto(`/crews/${crew.id}`)
  const card = page.locator('[data-crew-member]').first()
  await expect(card).toBeVisible()
  for (const label of ['Name', 'Agent', 'First prompt', 'Starts', 'Account', 'Model']) await expect(card.getByText(label, { exact: true })).toBeVisible()
  await tapTarget(card.locator('input, textarea, button'))
  // Its extra arguments, asked for from its menu: labelled, and as tall to press as its neighbours.
  await card.locator('[data-member-menu]').click()
  await page.getByRole('menuitem', { name: 'Extra arguments' }).click()
  await expect(page.getByRole('menu')).toHaveCount(0)
  await expect(card.locator('[data-member-args-field]').getByText('Extra arguments', { exact: true })).toBeVisible()
  await tapTarget(card.locator('input, textarea, button'))
  await noSideways(page)
  await page.locator('[data-launch]').click()
  const row = page.locator('[data-crew-launch-line="lead"]')
  await expect(row.locator('[data-crew-launch-agent]')).toBeVisible()
  await expect(row).toContainText('Agent: ')
  await expect(row).toContainText('Model: ')
  await expect(row).toContainText('Account: ')
  await noSideways(page)
  await page.locator('[data-crew-launch-cancel]').click()
})

test("a live run's banner on its crew's page says the run in the Crews pages' words: a member at its prompt is idle, never running", async ({ page, api }) => {
  budget(4 * EXPECT) // the page, the state, the counts, no "running"
  const crewName = unique('banner')
  // One member on the plain shell, which reports no turn: at its prompt, it is idle.
  const { crew } = await api.ok<{ crew: { id: string } }>('POST', '/api/crews', { name: crewName, goal: '', cwd: '', where: 'server', isolation: 'none', openAfterLaunch: false, members: [{ name: 'sh', agentId: 'plain', prompt: '', start: { when: 'immediately' } }] })
  made.crews.push(crew.id)
  const run = await api.launchCrew(crew.id)
  made.runs.push(run.id)
  await page.goto(`/crews/${crew.id}`)
  const banner = page.locator(`[data-live-run-banner][data-run="${run.id}"]`)
  await expect(banner.locator('[data-live-run-state]')).toContainText('Running')
  await expect(banner.locator('[data-live-run-line]')).toHaveText('1 idle')
  await expect(banner).not.toContainText(/\brunning\b/)
})

for (const [width, height] of [
  [1440, 900],
  [1280, 800],
]) {
  test(`at ${width} a member's row holds its agent's name and "When you press Start" whole, its Account and Model labelled`, async ({ page, api }) => {
    await page.setViewportSize({ width, height })
    const crewName = unique('wide')
    // An agent with one of the longer names, and a member started by hand.
    const { crew } = await api.ok<{ crew: { id: string } }>('POST', '/api/crews', { name: crewName, goal: '', cwd: '', where: 'server', isolation: 'none', openAfterLaunch: false, members: [{ name: 'reconciliation-worker-01', agentId: 'worker-cli', prompt: '', start: { when: 'manual' } }] })
    made.crews.push(crew.id)
    await page.goto(`/crews/${crew.id}`)
    const row = page.locator('[data-crew-member]').first()
    await expect(row.getByRole('combobox', { name: 'Agent for reconciliation-worker-01' })).toContainText('Worker, CLI (e2e stub)')
    await expect(row.locator('[data-member-start]')).toContainText('When you press Start')
    // The sidebar at its opening width (280 px), as a person first meets it: the page's content is what is left.
    await expect.poll(async () => Math.round((await page.locator('#dashboard-sidebar-main').boundingBox())?.width ?? 0)).toBeGreaterThanOrEqual(280)
    // The table's columns are drawn (the wide layout): its heads, and no card labels.
    await expect(page.locator('[data-crew-members] > [aria-hidden="true"]').first()).toBeVisible()
    await uncut(row.locator('[data-slot="value"]'))
    // Its account and model, each under a visible label; the model's empty state is the agent's choice.
    await expect(row.locator('[data-member-login]').getByText('Account', { exact: true })).toBeVisible()
    await expect(row.locator('[data-member-login]').getByText('Model', { exact: true })).toBeVisible()
    await expect(row.locator('[data-member-model]')).toHaveAttribute('placeholder', "The agent's choice")
  })
}
