import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Session } from './fixtures'
import { freeProjectKey, makeProject, makeTicket, rowKeys, storedTicket } from './dispatch-helpers'

// Dispatch's keys (docs/plans/dispatch.md, Owner decisions, 10): J K and Enter on a project's list, Escape back to it with the row
// focused again; S opening the status picker and a choice applying, P the priority, C a new ticket, the letters typed into a text field
// staying there; X selecting two and the palette's page group setting their status, Escape clearing the selection; G D from the Yard,
// and Alt+D from a focused terminal, which never gets the key. Each test makes a project of its own (EK, EK1…) and counts only its rows.
test.describe.configure({ mode: 'serial' })

function transcript(home: string, s: Session): string {
  const file = join(home, '.stub-sessions', `${s.agentSession?.id ?? ''}.txt`)
  return existsSync(file) ? readFileSync(file, 'utf8') : ''
}

test('J and K move through the list, wrapping; Enter opens the ticket; Escape comes back to the list, the row focused again', async ({ page, api }) => {
  const key = await freeProjectKey(api, 'EK')
  await makeProject(api, key)
  for (const n of ['1', '2', '3']) await makeTicket(api, key, n, { title: `Keys ${n}` })
  await page.goto(`/dispatch/${key}`)
  await expect(page.locator('[data-list-total]')).toHaveAttribute('data-count', '3')
  const order = await rowKeys(page, key)
  expect(order).toHaveLength(3)
  const row = (k: string) => page.locator(`[data-ticket-row="${k}"]`)

  await page.keyboard.press('j')
  await expect(row(order[0]!)).toBeFocused()
  await page.keyboard.press('j')
  await expect(row(order[1]!)).toBeFocused()
  await page.keyboard.press('k')
  await expect(row(order[0]!)).toBeFocused()
  // From the first, K goes to the last.
  await page.keyboard.press('k')
  await expect(row(order[2]!)).toBeFocused()

  await page.keyboard.press('Enter')
  const n = order[2]!.split('-')[1]
  await expect(page).toHaveURL(new RegExp(`/dispatch/${key}/${n}$`))
  await expect(page.locator('[data-ticket-title]')).toHaveText(`Keys ${n}`)
  await page.keyboard.press('Escape')
  await expect(page).toHaveURL(new RegExp(`/dispatch/${key}$`))
  await expect(row(order[2]!)).toBeFocused()
  // And J goes on from there.
  await page.keyboard.press('j')
  await expect(row(order[0]!)).toBeFocused()
})

test("Escape stays on a ticket's page while an editor is open, the focus on its Preview tab or its buttons, and the draft stays; closed, Escape goes back", async ({ page, api }) => {
  const key = await freeProjectKey(api, 'EK')
  await makeProject(api, key)
  await makeTicket(api, key, '1', { title: 'Draft keeper' })
  await page.goto(`/dispatch/${key}/1`)
  await expect(page.locator('[data-ticket-title]')).toHaveText('Draft keeper')
  const description = page.locator('[data-ticket-markdown="description"]')

  // The description's editor, a draft typed, the focus moved to its Preview tab: Escape leaves the page and the draft as they are.
  await description.locator('[data-markdown-empty]').click()
  await description.locator('[data-markdown-input]').fill('A draft not saved yet')
  await description.getByRole('tab', { name: 'Preview' }).click()
  await expect(description.locator('[data-markdown-draft-preview]')).toContainText('A draft not saved yet')
  await expect(description.getByRole('tab', { name: 'Preview' })).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(page).toHaveURL(new RegExp(`/dispatch/${key}/1$`))
  await expect(description.locator('[data-markdown-draft-preview]')).toContainText('A draft not saved yet')
  // The Save button focused: the same.
  await description.locator('[data-markdown-save]').focus()
  await page.keyboard.press('Escape')
  await expect(page).toHaveURL(new RegExp(`/dispatch/${key}/1$`))
  await description.getByRole('tab', { name: 'Write' }).click()
  await expect(description.locator('[data-markdown-input]')).toHaveValue('A draft not saved yet')

  // The title's editor, its Cancel button focused: Escape stays too.
  await description.getByRole('button', { name: 'Cancel' }).click()
  await expect(description.locator('[data-markdown-editor]')).toHaveCount(0)
  await page.locator('[data-title-edit]').click()
  await page.locator('[data-title-input]').fill('A new title not saved')
  await page.getByRole('button', { name: 'Cancel' }).focus()
  await page.keyboard.press('Escape')
  await expect(page).toHaveURL(new RegExp(`/dispatch/${key}/1$`))
  await expect(page.locator('[data-title-input]')).toHaveValue('A new title not saved')

  // Every editor closed: Escape goes back to the list.
  await page.getByRole('button', { name: 'Cancel' }).click()
  await expect(page.locator('[data-title-input]')).toHaveCount(0)
  await page.locator('body').focus()
  await page.keyboard.press('Escape')
  await expect(page).toHaveURL(new RegExp(`/dispatch/${key}$`))
  expect((await storedTicket(api, `${key}-1`)).title).toBe('Draft keeper')
})

test('S opens the status picker and a choice applies, P the priority; C makes a ticket, and letters typed into a field stay in it', async ({ page, api }) => {
  const key = await freeProjectKey(api, 'EK')
  await makeProject(api, key)
  await makeTicket(api, key, '1', { title: 'Pick me' })
  await page.goto(`/dispatch/${key}`)
  const row = page.locator(`[data-ticket-row="${key}-1"]`)
  await expect(row).toBeVisible()
  await page.keyboard.press('j')
  await expect(row).toBeFocused()

  // S: the status picker on the focused ticket, its status ticked; a search and Enter pick.
  await page.keyboard.press('s')
  const picker = page.locator('[data-picker]')
  await expect(picker).toHaveAttribute('data-picker-field', 'status')
  await expect(picker.locator('[data-picker-title]')).toHaveText(`Status of ${key}-1`)
  await expect(picker.locator('[data-pick-value="backlog"]')).toHaveAttribute('data-pick-held', '')
  await expect(picker.locator('input[data-picker-input]')).toBeFocused()
  await page.keyboard.type('review')
  await page.keyboard.press('Enter')
  await expect(picker).toBeHidden()
  await expect(row).toHaveAttribute('data-ticket-row-status', 'in_review')
  expect((await storedTicket(api, `${key}-1`)).status).toBe('in_review')

  // P: the priority, picked by a click; the focus is back on the row.
  await expect(row).toBeFocused()
  await page.keyboard.press('p')
  await expect(picker).toHaveAttribute('data-picker-field', 'priority')
  await picker.locator('[data-pick-value="4"]').click()
  await expect(picker).toBeHidden()
  await expect.poll(async () => (await storedTicket(api, `${key}-1`)).priority).toBe(4)

  // C: New ticket. Its title is typed with letters the page takes (s, p, a, l, m, o, x, c), which stay in the field.
  await page.keyboard.press('c')
  const dialog = page.locator('[data-new-ticket]')
  await expect(dialog).toBeVisible()
  const title = 'Make a plan, sort the mess, close the loop'
  await page.locator('[data-new-ticket-title]').fill('')
  await page.locator('[data-new-ticket-title]').pressSequentially(title)
  await expect(page.locator('[data-new-ticket-title]')).toHaveValue(title)
  await expect(picker).toHaveCount(0)
  await page.locator('[data-new-ticket-submit]').click()
  await expect(page.locator(`[data-ticket-row="${key}-2"] [data-row-title]`)).toHaveText(title)
})

test('in a picker, Tab to a choice and Enter picks that choice, not the one the arrows were on', async ({ page, api }) => {
  const key = await freeProjectKey(api, 'EK')
  await makeProject(api, key)
  await makeTicket(api, key, '1', { title: 'Tab and Enter' })
  await page.goto(`/dispatch/${key}`)
  const row = page.locator(`[data-ticket-row="${key}-1"]`)
  await expect(row).toBeVisible()
  await page.keyboard.press('j')
  await expect(row).toBeFocused()
  await page.keyboard.press('s')
  const picker = page.locator('[data-picker]')
  await expect(picker.locator('input[data-picker-input]')).toBeFocused()
  // Tab from the search to the first choice (Backlog, the cursor's), and on to Ready.
  const ready = picker.locator('[data-pick-value="ready"]')
  for (let i = 0; i < 5 && !(await ready.evaluate((el) => el === document.activeElement)); i++) await page.keyboard.press('Tab')
  await expect(ready).toBeFocused()
  await expect(ready).toHaveAttribute('aria-selected', 'true')
  await page.keyboard.press('Enter')
  await expect(picker).toBeHidden()
  await expect.poll(async () => (await storedTicket(api, `${key}-1`)).status).toBe('ready')
})

test('Escape from a ticket opened after More comes back to the list as deep as it was, the row focused again', async ({ page, api }) => {
  test.slow()
  const key = await freeProjectKey(api, 'EK')
  await makeProject(api, key)
  // One more than a page of the list (200).
  for (let i = 1; i <= 201; i += 20) await Promise.all(Array.from({ length: Math.min(20, 202 - i) }, (_, j) => makeTicket(api, key, String(i + j), { title: `Deep ${i + j}` })))
  await page.goto(`/dispatch/${key}`)
  await expect(page.locator('[data-list-total]')).toHaveAttribute('data-count', '201')
  const rows = page.locator(`[data-ticket-row^="${key}-"]`)
  await expect(rows).toHaveCount(200)
  const first = await rowKeys(page, key)
  await page.locator('[data-list-more]').click()
  await expect(rows).toHaveCount(201)
  const deep = (await rowKeys(page, key)).find((k) => !first.includes(k))!
  const row = page.locator(`[data-ticket-row="${deep}"]`)
  await row.click()
  await expect(page).toHaveURL(new RegExp(`/dispatch/${key}/${deep.split('-')[1]}$`))
  await expect(page.locator('[data-ticket-title]')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page).toHaveURL(new RegExp(`/dispatch/${key}$`))
  await expect(row).toBeFocused()
  await expect(rows).toHaveCount(201)
})

test("X selects two tickets and the palette's page group sets their status, one operation each; Escape clears the selection", async ({ page, api }) => {
  const key = await freeProjectKey(api, 'EK')
  await makeProject(api, key)
  for (const n of ['1', '2', '3']) await makeTicket(api, key, n, { title: `Select ${n}` })
  await page.goto(`/dispatch/${key}`)
  await expect(page.locator('[data-list-total]')).toHaveAttribute('data-count', '3')
  const order = await rowKeys(page, key)
  const edits: string[] = []
  page.on('request', (r) => {
    if (r.method() === 'POST' && /\/api\/dispatch\/tickets\/[^/]+\/edit$/.test(r.url())) edits.push(decodeURIComponent(r.url().split('/').at(-2)!))
  })

  await page.keyboard.press('j')
  await page.keyboard.press('x')
  await page.keyboard.press('j')
  await page.keyboard.press('x')
  const bar = page.locator('[data-selection-bar]')
  await expect(bar).toHaveAttribute('data-count', '2')
  await expect(page.locator(`[data-ticket-row="${order[0]}"]`)).toHaveAttribute('data-selected', '')
  await expect(page.locator(`[data-ticket-row="${order[1]}"]`)).toHaveAttribute('data-selected', '')
  await expect(page.locator(`[data-ticket-row="${order[2]}"]`)).not.toHaveAttribute('data-selected', '')

  // The palette: the page's group offers what the selection can be set to.
  await page.keyboard.press('Control+k')
  const palette = page.locator('[data-palette]')
  await expect(palette).toBeVisible()
  await expect(palette.locator('[data-palette-group="page"] h3')).toContainText(`${key} · Dispatch`)
  await expect(palette.locator('[data-palette-row="page:selection:status"]')).toContainText('Set status of 2 selected…')
  await palette.locator('input[data-palette-input]').fill('set status of 2')
  await expect(palette.locator('[data-palette-list] [data-palette-row]').first()).toHaveAttribute('data-palette-row', 'page:selection:status')
  await page.keyboard.press('Enter')
  await expect(palette).toBeHidden()
  const picker = page.locator('[data-picker]')
  await expect(picker).toHaveAttribute('data-picker-field', 'status')
  await expect(picker.locator('[data-picker-title]')).toHaveText('Status of 2 tickets')
  await picker.locator('[data-pick-value="ready"]').click()
  await expect.poll(async () => [(await storedTicket(api, order[0]!)).status, (await storedTicket(api, order[1]!)).status, (await storedTicket(api, order[2]!)).status]).toEqual(['ready', 'ready', 'backlog'])
  // One operation per ticket.
  expect([...edits].sort()).toEqual([order[0], order[1]].sort())

  // Escape clears the selection. The page leaves Escape to a dialog or a list of choices while one is in the page, the picker's
  // through its closing animation too: the key goes in once none is (CI pressed it inside that animation, and the key was the picker's).
  await expect(bar).toBeVisible()
  await expect(page.locator('[role="dialog"], [role="menu"], [role="listbox"]')).toHaveCount(0)
  await page.locator(`[data-ticket-row="${order[1]}"]`).focus()
  await page.keyboard.press('Escape')
  await expect(bar).toBeHidden()
  await expect(page.locator('[data-ticket-row][data-selected]')).toHaveCount(0)
})

test('G D goes to Dispatch from the Yard; Alt+D does from a focused terminal, which never gets the key', async ({ page, api, state }) => {
  await page.goto('/yard')
  await expect(page.locator('[data-session-list]')).toBeVisible({ timeout: 30_000 })
  await page.keyboard.press('g')
  await page.keyboard.press('d')
  await expect(page).toHaveURL(/\/dispatch$/)

  const s = await api.ok<Session>('POST', '/api/sessions', { agentId: 'claude', name: 'e2e dispatch alt d' })
  try {
    await page.goto(`/sessions/${s.id}`)
    const terminal = page.locator('.terminal-host .xterm-helper-textarea').first()
    await expect(terminal).toBeFocused({ timeout: 30_000 })
    await page.keyboard.press('Alt+d')
    await expect(page).toHaveURL(/\/dispatch$/)
    // Back in the terminal: a line typed now carries nothing of the chord.
    await page.goto(`/sessions/${s.id}`)
    await expect(terminal).toBeFocused({ timeout: 30_000 })
    await page.keyboard.type('alt key check')
    await page.keyboard.press('Enter')
    await expect.poll(() => transcript(state.home, s), { timeout: 15_000, message: 'the line was typed' }).toContain('alt key check\n')
    expect(transcript(state.home, s)).not.toMatch(/d|∂/)
  } finally {
    await api.stopSession(s.id)
  }
})
