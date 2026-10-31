import type { BrowserContext, Page } from '@playwright/test'
import { expect, test, type Api } from './fixtures'
import { freeProjectKey, makeProject, makeTicket, opOf, secondBrowser, storedTicket } from './dispatch-helpers'

// The close policy on a ticket's page (docs/plans/dispatch.md, PR-11): Done refused on the page with the policy's words, by the status
// and by the card's Close; a board drop on Done snapping back with the same words; Accept, then Done allowed; the owner changed after the
// acceptance, and the criteria changed after another, Done refused with the card's reason each time, Accept again and allowed; a
// delivery link required by a second project, added and removed on the page; the card's states for each; and a policy edited on the
// project's settings, and in the New project dialog. Review round 1 added: an Accept that waited behind another operation while another
// person changed the owner, not sent and said; a policy ticked while a second browser saved another, theirs said before a second Save
// replaces it. Review round 2 added: a tick made while the policy's Save is on its way kept once the saved policy comes; a link's add
// still on its way after its form was cancelled leaving a form opened since as it is. Every test makes a project of its own (CLS, CLS1… when the server holds the earlier ones) and looks at its own tickets by
// key.
test.describe.configure({ mode: 'serial' })

let other: BrowserContext | null = null

test.afterEach(async () => {
  await other?.close()
  other = null
})

/** A ticket's field set through the API, as another person's edit is: on the revision the store holds. */
async function setField(api: Api, key: string, n: string, set: Record<string, unknown>): Promise<void> {
  const t = await storedTicket(api, key)
  await api.ok('POST', `/api/dispatch/tickets/${key}/edit`, { opId: opOf(key.replace('-', ''), `e${n}`), expectedRev: t.rev, set })
}

/** The card's line of a requirement. */
function item(page: Page, requirement: 'acceptance' | 'delivery') {
  return page.locator(`[data-close-policy] [data-close-item="${requirement}"]`)
}

test("Done refused in the policy's words by the status and by Close, the board's drop snapping back, Accept then Done, and the card's reasons for an owner and criteria changed", async ({ page, api }) => {
  const key = await freeProjectKey(api, 'CLS')
  // The default close policy: the owner's acceptance.
  await makeProject(api, key)
  const t = await makeTicket(api, key, '1', { title: 'Close me', owner: 'Alice', acceptance: 'It works.', status: 'in_review' })
  await page.addInitScript(() => localStorage.setItem('conductor.displayName', 'Nate'))

  await page.goto(`/dispatch/${key}/1`)
  const card = page.locator('[data-close-policy]')
  await expect(card.locator('[data-close-summary]')).toHaveText("To close: the owner's acceptance")
  await expect(card).toHaveAttribute('data-accepted', 'none')
  await expect(card).not.toHaveAttribute('data-close-ready')
  await expect(item(page, 'acceptance')).not.toHaveAttribute('data-met')
  await expect(item(page, 'acceptance').locator('[data-close-state]')).toHaveText('Not accepted yet: Alice is the owner')
  await expect(item(page, 'delivery')).toHaveCount(0)

  // Done by the status: refused, in the policy's words, the status kept.
  await page.locator('[data-ticket-status]').click()
  await page.getByRole('option', { name: 'Done' }).click()
  await expect(page.getByText("It needs the owner's acceptance to be done.").first()).toBeVisible()
  await expect(page.locator('[data-ticket-status]')).toContainText('In review')
  expect((await storedTicket(api, t)).status).toBe('in_review')

  // Done by the card's Close: refused, said in the card.
  await card.locator('[data-close]').click()
  await expect(card.locator('[data-close-refused]')).toContainText("It needs the owner's acceptance to be done.")
  expect((await storedTicket(api, t)).status).toBe('in_review')

  // The board: a drop on Done snaps back with the same words.
  await page.goto(`/dispatch/${key}?view=board`)
  const board = page.locator('[data-board]')
  await board.evaluate((el) => el.scrollTo({ left: el.scrollWidth }))
  await expect(board.locator('[data-column="done"]')).toBeInViewport()
  await board.locator(`[data-card="${t}"]`).dragTo(board.locator('[data-column="done"]'))
  await expect(page.getByText("It needs the owner's acceptance to be done.").first()).toBeVisible()
  await expect(board.locator(`[data-column="in_review"] [data-card="${t}"]`)).toBeVisible()
  expect((await storedTicket(api, t)).status).toBe('in_review')

  // Accept, with a note: recorded for the owner, by the display name; the card says so and is ready.
  await page.goto(`/dispatch/${key}/1`)
  await card.locator('[data-accept-note-open]').click()
  await card.locator('[data-accept-note]').fill('Seen it work on staging.')
  await expect(card.locator('[data-accept]')).toHaveText('Accept for Alice')
  await card.locator('[data-accept]').click()
  await expect(card).toHaveAttribute('data-accepted', 'met')
  await expect(card).toHaveAttribute('data-close-ready', '')
  await expect(item(page, 'acceptance')).toHaveAttribute('data-met', '')
  await expect(item(page, 'acceptance').locator('[data-close-state]')).toHaveText('Accepted by Nate for Alice')
  await expect(card.locator('[data-acceptance-note]')).toHaveText('Seen it work on staging.')
  // Acceptance is its own fact: no status moved with it.
  await expect(page.locator('[data-ticket-status]')).toContainText('In review')
  const accepted = await api.ok<{ ticket: { acceptedBy: string; acceptedFor: string; acceptedNote: string; status: string } }>('GET', `/api/dispatch/tickets/${t}`)
  expect([accepted.ticket.acceptedBy, accepted.ticket.acceptedFor, accepted.ticket.acceptedNote, accepted.ticket.status]).toEqual(['Nate', 'Alice', 'Seen it work on staging.', 'in_review'])

  // The owner changed after the acceptance, by another person: the card says for whom it was accepted, and Close is refused.
  await setField(api, t, '1', { owner: 'Bob' })
  await expect(card).toHaveAttribute('data-accepted', 'unmet')
  await expect(item(page, 'acceptance').locator('[data-close-state]')).toHaveText('accepted for Alice; the owner is now Bob')
  await expect(card).not.toHaveAttribute('data-close-ready')
  await card.locator('[data-close]').click()
  await expect(card.locator('[data-close-refused]')).toContainText("It needs the owner's acceptance to be done.")
  expect((await storedTicket(api, t)).status).toBe('in_review')

  // Accept again, for Bob: met.
  await expect(card.locator('[data-accept]')).toHaveText('Accept again for Bob')
  await card.locator('[data-accept]').click()
  await expect(card).toHaveAttribute('data-accepted', 'met')
  await expect(item(page, 'acceptance').locator('[data-close-state]')).toHaveText('Accepted by Nate for Bob')

  // The criteria changed after it: unmet again, the card says why.
  await setField(api, t, '2', { acceptance: 'It works, and it is fast.' })
  await expect(card).toHaveAttribute('data-accepted', 'unmet')
  await expect(item(page, 'acceptance').locator('[data-close-state]')).toHaveText('accepted before the criteria changed')
  await card.locator('[data-accept]').click()
  await expect(card).toHaveAttribute('data-accepted', 'met')

  // Close: done, and the card offers no Close on a closed ticket.
  await card.locator('[data-close]').click()
  await expect(card).toHaveAttribute('data-close-closed', '')
  await expect(card.locator('[data-close]')).toHaveCount(0)
  await expect(page.locator('[data-ticket-status]')).toContainText('Done')
  await expect.poll(async () => (await storedTicket(api, t)).status).toBe('done')

  // The acceptance taken back: a fact of its own, the status kept.
  await card.locator('[data-unaccept]').click()
  await expect(card).toHaveAttribute('data-accepted', 'none')
  await expect(page.locator('[data-ticket-status]')).toContainText('Done')
  expect((await storedTicket(api, t)).status).toBe('done')
  const history = page.locator('[data-ticket-history]')
  await expect(history).toContainText('accepted it for Alice')
  await expect(history).toContainText('took the acceptance back')
})

test('a ticket with no owner has no one to accept it for: Accept is off and says why', async ({ page, api }) => {
  const key = await freeProjectKey(api, 'CLS')
  await makeProject(api, key)
  await makeTicket(api, key, '1', { title: 'No owner' })
  await page.goto(`/dispatch/${key}/1`)
  const card = page.locator('[data-close-policy]')
  await expect(item(page, 'acceptance').locator('[data-close-state]')).toHaveText('Not accepted yet: the ticket has no owner to accept it')
  await expect(card.locator('[data-accept]')).toBeDisabled()
  await expect(card.locator('[data-accept-blocked]')).toContainText('It needs an owner to accept it for.')
})

test('a delivery link required by a second project: Close refused until one is added, the link listed and removed, the card following', async ({ page, api }) => {
  const key = await freeProjectKey(api, 'CLS')
  await makeProject(api, key, ['delivery'])
  const t = await makeTicket(api, key, '1', { title: 'Ship it', status: 'in_review' })
  await page.goto(`/dispatch/${key}/1`)
  const card = page.locator('[data-close-policy]')
  await expect(card.locator('[data-close-summary]')).toHaveText('To close: a delivery link')
  await expect(item(page, 'delivery').locator('[data-close-state]')).toHaveText('No delivery link yet')
  await expect(item(page, 'acceptance')).toHaveCount(0)
  // The acceptance is said on its own though no policy asks for it.
  await expect(card.locator('[data-acceptance-state]')).toContainText('Not accepted yet')

  await card.locator('[data-close]').click()
  await expect(card.locator('[data-close-refused]')).toContainText('It needs a delivery link to be done.')

  // An external link is no delivery: the card stays unmet.
  const links = page.locator('[data-ticket-links]')
  await links.locator('[data-link-add-open]').click()
  await links.locator('[data-link-kind-pick]').getByRole('radio', { name: 'External' }).click()
  await links.locator('[data-link-url-input]').fill('https://tracker.example.com/T-9')
  await links.locator('[data-link-submit]').click()
  await expect(links.locator('[data-link-kind="external"] [data-link-url]')).toHaveText('tracker.example.com/T-9')
  await expect(item(page, 'delivery')).not.toHaveAttribute('data-met')

  // A delivery link, the merged pull request's URL with a note: listed in short, the card met.
  const pr = `https://github.com/acme/${key.toLowerCase()}/pull/12`
  await links.locator('[data-link-add-open]').click()
  await expect(links.locator('[data-link-kind-pick]').getByRole('radio', { name: 'Delivery' })).toBeChecked()
  await links.locator('[data-link-url-input]').fill(pr)
  await links.locator('[data-link-note-input]').fill('Merged on Friday')
  await links.locator('[data-link-submit]').click()
  const delivery = links.locator('[data-link-kind="delivery"]')
  await expect(delivery.locator('[data-link-url]')).toHaveText(`acme/${key.toLowerCase()}#12`)
  await expect(delivery.locator('[data-link-url]')).toHaveAttribute('href', pr)
  await expect(delivery.locator('[data-link-note]')).toHaveText('Merged on Friday')
  await expect(item(page, 'delivery')).toHaveAttribute('data-met', '')
  await expect(item(page, 'delivery').locator('[data-close-state]')).toHaveText('Delivered: one link')

  // The same URL again is refused in the store's words, in the form.
  await links.locator('[data-link-add-open]').click()
  await links.locator('[data-link-url-input]').fill(pr)
  await links.locator('[data-link-submit]').click()
  await expect(links.locator('[data-link-error]')).toBeVisible()
  await links.getByRole('button', { name: 'Cancel' }).click()

  // Removed: unmet again.
  await delivery.locator('[data-link-remove]').click()
  await expect(links.locator('[data-link-kind="delivery"]')).toHaveCount(0)
  await expect(item(page, 'delivery')).not.toHaveAttribute('data-met')
  const stored = await api.ok<{ ticket: { links: Array<{ kind: string; url: string }> } }>('GET', `/api/dispatch/tickets/${t}`)
  expect(stored.ticket.links.map((l) => [l.kind, l.url])).toEqual([['external', 'https://tracker.example.com/T-9']])
})

test("the close policy ticked in the New project dialog, and edited on the project's settings", async ({ page, api }) => {
  const key = await freeProjectKey(api, 'CLS')
  await page.goto('/dispatch')
  await page.locator('[data-new-project-open]').click()
  const dialog = page.locator('[data-new-project]')
  await dialog.locator('[data-new-project-key]').fill(key)
  await dialog.locator('[data-new-project-name]').fill(`E2E project ${key}`)
  // A project of Dispatch's own asks for the owner's acceptance by default; ticked otherwise, both.
  const policy = dialog.locator('[data-new-project-policy]')
  await expect(policy.locator('[data-close-requirement="acceptance"]')).toHaveAttribute('aria-checked', 'true')
  await expect(policy.locator('[data-close-requirement="delivery"]')).toHaveAttribute('aria-checked', 'false')
  await expect(policy.locator('[data-close-requirement]')).toHaveCount(2)
  await policy.locator('[data-close-requirement="delivery"]').click()
  await page.locator('[data-new-project-submit]').click()
  await expect(page).toHaveURL(new RegExp(`/dispatch/${key}$`))
  const made = await api.ok<{ project: { closeRequires: string[] } }>('GET', `/api/dispatch/projects/${key}`)
  expect(made.project.closeRequires).toEqual(['acceptance', 'delivery'])

  // The settings: the policy as the project holds it, edited to nothing required.
  await page.locator('[data-project-settings-open]').click()
  const section = page.locator('[data-project-close-policy]')
  await expect(section).toHaveAttribute('data-policy', 'acceptance delivery')
  await expect(section.locator('[data-close-policy-save]')).toBeDisabled()
  await section.locator('[data-close-requirement="acceptance"]').click()
  await section.locator('[data-close-requirement="delivery"]').click()
  await section.locator('[data-close-policy-save]').click()
  await expect(section).toHaveAttribute('data-policy', '')
  await expect.poll(async () => (await api.ok<{ project: { closeRequires: string[] } }>('GET', `/api/dispatch/projects/${key}`)).project.closeRequires).toEqual([])
})

test('an Accept that waited behind another operation is not sent once another person changed the owner: said, and accepted again for the new one', async ({ page, api }) => {
  const key = await freeProjectKey(api, 'CLS')
  await makeProject(api, key)
  const t = await makeTicket(api, key, '1', { title: 'Accept me', owner: 'Alice', acceptance: 'It works.', status: 'in_review' })
  // A link's reply held after the store has it: the Accept clicked behind it waits its turn in the page's one queue.
  let release!: () => void
  const held = new Promise<void>((resolve) => (release = resolve))
  await page.route('**/api/dispatch/tickets/*/links', async (route) => {
    const response = await route.fetch()
    await held
    await route.fulfill({ response })
  })
  await page.goto(`/dispatch/${key}/1`)
  const card = page.locator('[data-close-policy]')
  await expect(card.locator('[data-accept]')).toHaveText('Accept for Alice')
  const links = page.locator('[data-ticket-links]')
  await links.locator('[data-link-add-open]').click()
  await links.locator('[data-link-kind-pick]').getByRole('radio', { name: 'External' }).click()
  await links.locator('[data-link-url-input]').fill('https://tracker.example.com/T-1')
  await links.locator('[data-link-submit]').click()
  await expect.poll(async () => (await api.ok<{ ticket: { links: unknown[] } }>('GET', `/api/dispatch/tickets/${t}`)).ticket.links.length).toBe(1)
  await card.locator('[data-accept]').click()

  // Another person makes Bob the owner while the Accept waits; the page shows it before the Accept's turn comes.
  await setField(api, t, '1', { owner: 'Bob' })
  await expect(page.locator('[data-ticket-owner]')).toContainText('Bob')
  release()
  await expect(card.locator('[data-accept-refused]')).toContainText('The owner changed to Bob before it was recorded, so nothing was accepted')
  await expect(card).toHaveAttribute('data-accepted', 'none')
  const stored = await api.ok<{ ticket: { acceptedAt?: string; acceptedFor?: string } }>('GET', `/api/dispatch/tickets/${t}`)
  expect([stored.ticket.acceptedAt ?? '', stored.ticket.acceptedFor ?? '']).toEqual(['', ''])
  await page.unroute('**/api/dispatch/tickets/*/links')

  // Accepted again, knowingly, for the owner shown now.
  await expect(card.locator('[data-accept]')).toHaveText('Accept for Bob')
  await card.locator('[data-accept]').click()
  await expect(card).toHaveAttribute('data-accepted', 'met')
  await expect(card.locator('[data-accept-refused]')).toHaveCount(0)
  await expect(item(page, 'acceptance').locator('[data-close-state]')).toContainText('for Bob')
})

test("a policy ticked while a second browser saves another is never saved over theirs unseen: theirs said, and a second Save replaces it", async ({ page, api, state, browser }) => {
  const key = await freeProjectKey(api, 'CLS')
  // The default policy: the owner's acceptance.
  await makeProject(api, key)
  const projectPolicy = async () => (await api.ok<{ project: { closeRequires: string[] } }>('GET', `/api/dispatch/projects/${key}`)).project.closeRequires

  // The first browser ticks a delivery link too, and does not save yet.
  await page.goto(`/dispatch/${key}`)
  await page.locator('[data-project-settings-open]').click()
  const mine = page.locator('[data-project-close-policy]')
  await expect(mine).toHaveAttribute('data-policy', 'acceptance')
  await mine.locator('[data-close-requirement="delivery"]').click()

  // The second browser saves a policy of no requirements.
  other = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const second = await secondBrowser(other, state.token)
  await second.goto(`/dispatch/${key}`)
  await second.locator('[data-project-settings-open]').click()
  const theirs = second.locator('[data-project-close-policy]')
  await expect(theirs).toHaveAttribute('data-policy', 'acceptance')
  await theirs.locator('[data-close-requirement="acceptance"]').click()
  await theirs.locator('[data-close-policy-save]').click()
  await expect(theirs).toHaveAttribute('data-policy', '')

  // The first page holds theirs now, its own ticks kept; its Save says theirs and sends nothing.
  await expect(mine).toHaveAttribute('data-policy', '')
  await expect(mine.locator('[data-close-requirement="acceptance"]')).toHaveAttribute('aria-checked', 'true')
  await expect(mine.locator('[data-close-requirement="delivery"]')).toHaveAttribute('aria-checked', 'true')
  await mine.locator('[data-close-policy-save]').click()
  await expect(mine.locator('[data-policy-theirs]')).toContainText('It was changed to nothing while you ticked')
  expect(await projectPolicy()).toEqual([])

  // A second Save replaces it, knowingly; the second browser follows.
  await mine.locator('[data-close-policy-save]').click()
  await expect(mine).toHaveAttribute('data-policy', 'acceptance delivery')
  await expect(mine.locator('[data-policy-theirs]')).toHaveCount(0)
  await expect.poll(projectPolicy).toEqual(['acceptance', 'delivery'])
  await expect(theirs).toHaveAttribute('data-policy', 'acceptance delivery')
})

test("a policy ticked while its Save is on its way keeps that tick once the saved policy comes, and a second Save sends it", async ({ page, api }) => {
  const key = await freeProjectKey(api, 'CLS')
  // The default policy: the owner's acceptance.
  await makeProject(api, key)
  const projectPolicy = async () => (await api.ok<{ project: { closeRequires: string[] } }>('GET', `/api/dispatch/projects/${key}`)).project.closeRequires
  // The first Save's reply held after the store has it.
  let release!: () => void
  const held = new Promise<void>((resolve) => (release = resolve))
  let edits = 0
  await page.route('**/api/dispatch/projects/*/edit', async (route) => {
    const response = await route.fetch()
    if (++edits === 1) await held
    await route.fulfill({ response })
  })
  await page.goto(`/dispatch/${key}`)
  await page.locator('[data-project-settings-open]').click()
  const section = page.locator('[data-project-close-policy]')
  await expect(section).toHaveAttribute('data-policy', 'acceptance')
  await section.locator('[data-close-requirement="delivery"]').click()
  await section.locator('[data-close-policy-save]').click()
  await expect.poll(projectPolicy).toEqual(['acceptance', 'delivery'])

  // Delivery unticked while the Save waits; the project's event brings the saved policy, and the tick made since stays.
  await section.locator('[data-close-requirement="delivery"]').click()
  await expect(section).toHaveAttribute('data-policy', 'acceptance delivery')
  await expect(section.locator('[data-close-requirement="delivery"]')).toHaveAttribute('aria-checked', 'false')
  release()
  await expect(section.locator('[data-close-policy-save]')).toBeEnabled()
  await expect(section.locator('[data-close-requirement="acceptance"]')).toHaveAttribute('aria-checked', 'true')
  await expect(section.locator('[data-close-requirement="delivery"]')).toHaveAttribute('aria-checked', 'false')
  await expect(section.locator('[data-policy-theirs]')).toHaveCount(0)

  // A second Save sends it.
  await section.locator('[data-close-policy-save]').click()
  await expect(section).toHaveAttribute('data-policy', 'acceptance')
  await expect.poll(projectPolicy).toEqual(['acceptance'])
  await expect(section.locator('[data-close-policy-save]')).toBeDisabled()
  await page.unroute('**/api/dispatch/projects/*/edit')
})

test('a link whose add is on its way after its form was cancelled leaves a form opened since as it is', async ({ page, api }) => {
  const key = await freeProjectKey(api, 'CLS')
  await makeProject(api, key)
  const t = await makeTicket(api, key, '1', { title: 'Link me' })
  // The first add's reply held after the store has it.
  let release!: () => void
  const held = new Promise<void>((resolve) => (release = resolve))
  let adds = 0
  await page.route('**/api/dispatch/tickets/*/links', async (route) => {
    const first = route.request().method() === 'POST' && ++adds === 1
    const response = await route.fetch()
    if (first) await held
    await route.fulfill({ response })
  })
  await page.goto(`/dispatch/${key}/1`)
  const links = page.locator('[data-ticket-links]')
  await links.locator('[data-link-add-open]').click()
  await links.locator('[data-link-kind-pick]').getByRole('radio', { name: 'External' }).click()
  await links.locator('[data-link-url-input]').fill('https://tracker.example.com/T-1')
  await links.locator('[data-link-submit]').click()
  await expect.poll(async () => (await api.ok<{ ticket: { links: unknown[] } }>('GET', `/api/dispatch/tickets/${t}`)).ticket.links.length).toBe(1)

  // Cancelled while it waits, opened again and the same URL typed in it with a note: sent, it waits its turn behind the first in the
  // page's one queue, so its answer, the store's refusal, is said only after the first's answer came.
  await links.locator('[data-link-cancel]').click()
  await links.locator('[data-link-add-open]').click()
  await links.locator('[data-link-kind-pick]').getByRole('radio', { name: 'External' }).click()
  await links.locator('[data-link-url-input]').fill('https://tracker.example.com/T-1')
  await links.locator('[data-link-note-input]').fill('The second')
  await links.locator('[data-link-submit]').click()
  // The page's queue holds it: nothing more is sent before the first's answer.
  expect(adds).toBe(1)
  release()
  // The first add's answer left the form opened since as it is: the second's refusal is said in it, with what was typed kept.
  await expect(links.locator('[data-link-error]')).toBeVisible()
  expect(adds).toBe(2)
  await expect(links.locator('[data-link-form]')).toBeVisible()
  await expect(links.locator('[data-link-url-input]')).toHaveValue('https://tracker.example.com/T-1')
  await expect(links.locator('[data-link-note-input]')).toHaveValue('The second')
  await expect(links.locator('[data-link-url]')).toHaveText(['tracker.example.com/T-1'])

  // Another URL: its own add closes the form.
  await links.locator('[data-link-url-input]').fill('https://tracker.example.com/T-2')
  await links.locator('[data-link-submit]').click()
  await expect(links.locator('[data-link-form]')).toHaveCount(0)
  await expect(links.locator('[data-link-url]')).toHaveText(['tracker.example.com/T-1', 'tracker.example.com/T-2'])
  await page.unroute('**/api/dispatch/tickets/*/links')
})
