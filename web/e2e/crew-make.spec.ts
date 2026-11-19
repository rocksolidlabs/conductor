import { randomBytes } from 'node:crypto'
import type { Locator, Page } from '@playwright/test'
import { expect, test, type Run } from './fixtures'
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
  const bar = page.locator('[data-unsaved-bar]')
  const save = bar.getByRole('button', { name: 'Save', exact: true })
  const reason = bar.locator('[data-save-reason]')
  const launch = page.locator('[data-launch]')
  await expect(field).toContainText('A crew needs a name')
  await expect(reason).toHaveText('A crew needs a name')
  await expect(save).toBeDisabled()
  await expect(launch).toBeDisabled()

  // A name another crew has, typed into the focused field.
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

  // A draft's button saves, then opens the sheet on the crew as saved.
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
  await expect(page.locator('[data-crew-launch-yolo]')).toHaveText(/^Permission prompts: /)
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

test('a saved crew with edits says so beside its launch, which saves them first; More options keeps every choice one press away', async ({ page, api }) => {
  const crewName = unique('edits')
  const { crew } = await api.ok<{ crew: { id: string } }>('POST', '/api/crews', { name: crewName, goal: '', cwd: '', where: 'server', isolation: 'none', openAfterLaunch: false, yolo: false, members: [{ name: 'lead', agentId: 'claude', prompt: '', start: { when: 'manual' } }] })
  made.crews.push(crew.id)
  await page.goto(`/crews/${crew.id}`)
  const launch = page.locator('[data-launch]')
  await expect(page.getByRole('textbox', { name: 'Crew name' })).toHaveValue(crewName)
  await expect(launch).toHaveAccessibleName('Launch run')
  await expect(page.locator('[data-launch-unsaved]')).toHaveCount(0)
  // Folded, the line beside More options says each choice; opened, each is a labelled control.
  await expect(page.locator('[data-crew-more-line]')).toHaveText('Stays on this page · Permission prompts: ask · No view link')
  await page.locator('[data-crew-more-options]').click()
  await expect(page.locator('[data-crew-more]')).toBeVisible()
  await pick(page, page.getByRole('combobox', { name: 'After launch' }), 'Open the run')
  await pick(page, page.getByRole('combobox', { name: 'Permission prompts' }), 'Skip: members act without asking')
  const viewLink = page.locator('[data-crew-more]').getByRole('switch')
  await expect(page.locator('[data-crew-more]')).toContainText('Make one for 8 h')
  await viewLink.click()
  await expect(viewLink).toHaveAttribute('aria-checked', 'true')
  await expect(page.locator('[data-unsaved-words]')).toHaveText('More options edited.')
  // The edits are said beside the launch, which saves them before the sheet opens.
  await expect(page.locator('[data-launch-unsaved]')).toHaveText('Unsaved changes')
  await expect(launch).toHaveAccessibleName('Save and launch')
  const put = page.waitForRequest((r) => r.method() === 'PUT' && new URL(r.url()).pathname === `/api/crews/${crew.id}`)
  await launch.click()
  expect((await put).postDataJSON()).toMatchObject({ openAfterLaunch: true, yolo: true, viewLinkTtlSeconds: 8 * 3600 })
  await expect(page.locator('[data-crew-launch-line="lead"]')).toBeVisible()
  await expect(page.locator('[data-crew-launch-yolo]')).toHaveText("Permission prompts: skipped: the members act without asking (the crew's setting)")
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
    // The table's columns are drawn (the wide layout): its heads, and no card labels.
    await expect(page.locator('[data-crew-members] > [aria-hidden="true"]').first()).toBeVisible()
    await uncut(row.locator('[data-slot="value"]'))
    // Its account and model, each under a visible label; the model's empty state is the agent's choice.
    await expect(row.locator('[data-member-login]').getByText('Account', { exact: true })).toBeVisible()
    await expect(row.locator('[data-member-login]').getByText('Model', { exact: true })).toBeVisible()
    await expect(row.locator('[data-member-model]')).toHaveAttribute('placeholder', "The agent's choice")
  })
}
