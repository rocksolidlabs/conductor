import type { BrowserContext } from '@playwright/test'
import { expect, test } from './fixtures'
import { freeProjectKey, makeMilestone, makeProject, makeTicket, mark, marked, rowKeys, secondBrowser, sideways, storedTicket } from './dispatch-helpers'

// A project's views (docs/plans/dispatch.md, PR-9): the filter in the link (?status=, ?q=), the list filtered by it; a saved view made,
// its chip applying its filter, renamed and deleted; a view of every project saved by a second browser, its chip shown on the first's
// open list with no reload and gone when the second deletes it; the board's columns with each ticket in its status, a drag to In review,
// a drop on Done the close policy refuses snapping back with its words, and ← → on a focused card; the roadmap's columns, Next and Now
// by → and ← on a focused card, the milestones beside with done/total; and a phone's width (390 px) for the list, the board and the
// ticket's page. Each test makes a project of its own (EV, EV1, EV2…) and counts only its rows.
test.describe.configure({ mode: 'serial' })

let other: BrowserContext | null = null

test.afterEach(async () => {
  await other?.close()
  other = null
})

test('the filter in the link: ?status= and ?q= list what they name, together too, and Clear lists every ticket again', async ({ page, api }) => {
  const key = await freeProjectKey(api, 'EV')
  await makeProject(api, key)
  await makeTicket(api, key, '1', { title: 'Fix the login', status: 'in_progress' })
  await makeTicket(api, key, '2', { title: 'Write the docs' })
  await makeTicket(api, key, '3', { title: 'Login rate limits', status: 'in_progress', priority: 4 })
  const total = page.locator('[data-list-total]')

  await page.goto(`/dispatch/${key}?status=in_progress`)
  await expect(total).toHaveAttribute('data-count', '2')
  expect((await rowKeys(page, key)).sort()).toEqual([`${key}-1`, `${key}-3`])

  await page.goto(`/dispatch/${key}?q=login`)
  await expect(total).toHaveAttribute('data-count', '2')
  expect((await rowKeys(page, key)).sort()).toEqual([`${key}-1`, `${key}-3`])

  await page.goto(`/dispatch/${key}?status=in_progress&q=rate`)
  await expect(total).toHaveAttribute('data-count', '1')
  expect(await rowKeys(page, key)).toEqual([`${key}-3`])

  // Clear takes the filter out of the link, and the list is every ticket again.
  await page.locator('[data-filter-clear]').click()
  await expect(total).toHaveAttribute('data-count', '3')
  await expect(page).not.toHaveURL(/status=|q=/)
})

test('a saved view made, its chip applying its filter, renamed and deleted; a view of every project saved by a second browser shows on the first with no reload, and goes when it is deleted', async ({ page, api, state, browser }) => {
  const key = await freeProjectKey(api, 'EV')
  await makeProject(api, key)
  await makeTicket(api, key, '1', { title: 'Doing it', status: 'in_progress' })
  await makeTicket(api, key, '2', { title: 'Not yet' })
  const total = page.locator('[data-list-total]')
  const bar = page.locator('[data-views-bar]')

  // Saved from the filter shown: its chip says it is applied.
  await page.goto(`/dispatch/${key}?status=in_progress`)
  await expect(total).toHaveAttribute('data-count', '1')
  await expect(bar).toHaveAttribute('data-loaded', '')
  await page.locator('[data-view-save]').click()
  const dialog = page.locator('[data-view-dialog]')
  await expect(dialog).toBeVisible()
  await dialog.locator('[data-view-name-input]').fill('Doing')
  await page.locator('[data-view-submit]').click()
  await expect(dialog).toBeHidden()
  const chip = bar.locator('[data-view-chip][data-view-name="Doing"]')
  await expect(chip).toHaveAttribute('data-active', '')
  const id = (await chip.getAttribute('data-view-chip')) ?? ''
  expect(id).toMatch(/^[0-9a-f]{16}$/)
  const saved = await api.ok<{ views: Array<{ id: string; name: string; kind: string; projectId: string; query: { status?: string[] } }> }>('GET', `/api/dispatch/views?project=${key}`)
  expect(saved.views.find((v) => v.id === id)).toMatchObject({ name: 'Doing', kind: 'list', query: { status: ['in_progress'] } })

  // The list unfiltered: the chip is not applied; a click applies it, the filter written to the link.
  await page.goto(`/dispatch/${key}`)
  await expect(total).toHaveAttribute('data-count', '2')
  const byId = bar.locator(`[data-view-chip="${id}"]`)
  await expect(byId).toBeVisible()
  await expect(byId).not.toHaveAttribute('data-active', '')
  await byId.locator('[data-view-apply]').click()
  await expect(page).toHaveURL(/status=in_progress/)
  await expect(total).toHaveAttribute('data-count', '1')
  await expect(byId).toHaveAttribute('data-active', '')

  // Renamed from its menu.
  await page.locator(`[data-view-menu="${id}"]`).click()
  await page.getByRole('menuitem', { name: 'Rename…' }).click()
  await expect(dialog.locator('[data-view-name-input]')).toHaveValue('Doing')
  await dialog.locator('[data-view-name-input]').fill('In flight')
  await page.locator('[data-view-submit]').click()
  await expect(byId).toHaveAttribute('data-view-name', 'In flight')
  await expect.poll(async () => (await api.ok<{ views: Array<{ id: string; name: string }> }>('GET', `/api/dispatch/views?project=${key}`)).views.find((v) => v.id === id)?.name).toBe('In flight')

  // Deleted from its menu, asked first.
  await page.locator(`[data-view-menu="${id}"]`).click()
  await page.getByRole('menuitem', { name: 'Delete…' }).click()
  await page.locator('[data-view-delete-confirm]').click()
  await expect(byId).toHaveCount(0)
  expect((await api.ok<{ views: Array<{ id: string }> }>('GET', `/api/dispatch/views?project=${key}`)).views.some((v) => v.id === id)).toBe(false)

  // A view of every project, saved by a second browser on another project's page: the first, open on its list, shows its chip with no
  // reload, and loses it when the second deletes it.
  const key2 = await freeProjectKey(api, 'EV')
  await makeProject(api, key2)
  await mark(page)
  other = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const second = await secondBrowser(other, state.token)
  await second.goto(`/dispatch/${key2}?priority=urgent`)
  await expect(second.locator('[data-views-bar]')).toHaveAttribute('data-loaded', '')
  await second.locator('[data-view-save]').click()
  const name = `Urgent everywhere ${key}`
  await second.locator('[data-view-dialog] [data-view-name-input]').fill(name)
  await second.getByRole('radio', { name: /Every project/ }).click()
  await second.locator('[data-view-submit]').click()
  const every = bar.locator(`[data-view-chip][data-view-name="${name}"]`)
  await expect(every).toHaveAttribute('data-view-every', '')
  expect(await marked(page)).toBe('kept')
  const everyId = (await every.getAttribute('data-view-chip')) ?? ''
  await second.locator(`[data-view-menu="${everyId}"]`).click()
  await second.getByRole('menuitem', { name: 'Delete…' }).click()
  await second.locator('[data-view-delete-confirm]').click()
  await expect(every).toHaveCount(0)
  expect(await marked(page)).toBe('kept')
})

test("the board: a column per status with each ticket in its own, a drag to In review, a drop on Done the close policy refuses snapping back with its words, and ← → on a focused card", async ({ page, api }) => {
  const key = await freeProjectKey(api, 'EV')
  // The default close policy: the owner's acceptance.
  await makeProject(api, key)
  await makeTicket(api, key, '1', { title: 'Board one', status: 'in_progress' })
  await makeTicket(api, key, '2', { title: 'Board two' })

  await page.goto(`/dispatch/${key}?view=board`)
  const board = page.locator('[data-board]')
  await expect(board.locator('[data-column]')).toHaveCount(6)
  expect(await board.locator('[data-column]').evaluateAll((els) => els.map((el) => el.getAttribute('data-column')))).toEqual(['backlog', 'ready', 'in_progress', 'in_review', 'done', 'cancelled'])
  const card = (k: string) => board.locator(`[data-card="${k}"]`)
  const inColumn = (column: string, k: string) => board.locator(`[data-column="${column}"] [data-card="${k}"]`)
  await expect(inColumn('in_progress', `${key}-1`)).toBeVisible()
  await expect(inColumn('backlog', `${key}-2`)).toBeVisible()

  // A drag to In review.
  await card(`${key}-2`).dragTo(board.locator('[data-column="in_review"]'))
  await expect(inColumn('in_review', `${key}-2`)).toBeVisible()
  await expect.poll(async () => (await storedTicket(api, `${key}-2`)).status).toBe('in_review')

  // A drop on Done, which the policy refuses: its words, and the card back in In review. The columns scroll sideways; Done is brought
  // into the window first, as a person scrolls to it.
  await board.evaluate((el) => el.scrollTo({ left: el.scrollWidth }))
  await expect(board.locator('[data-column="done"]')).toBeInViewport()
  await card(`${key}-2`).dragTo(board.locator('[data-column="done"]'))
  await expect(page.getByText("It needs the owner's acceptance to be done.").first()).toBeVisible()
  await expect(inColumn('in_review', `${key}-2`)).toBeVisible()
  await expect(board.locator('[data-column="done"] [data-card]')).toHaveCount(0)
  expect((await storedTicket(api, `${key}-2`)).status).toBe('in_review')

  // ← → on a focused card move it a column, the focus staying on it.
  await card(`${key}-1`).focus()
  await page.keyboard.press('ArrowRight')
  await expect(inColumn('in_review', `${key}-1`)).toBeVisible()
  await expect(card(`${key}-1`)).toBeFocused()
  await expect.poll(async () => (await storedTicket(api, `${key}-1`)).status).toBe('in_review')
  await page.keyboard.press('ArrowLeft')
  await page.keyboard.press('ArrowLeft')
  await expect(inColumn('ready', `${key}-1`)).toBeVisible()
  await expect(card(`${key}-1`)).toBeFocused()
  await expect.poll(async () => (await storedTicket(api, `${key}-1`)).status).toBe('ready')
})

test('the board left while a move is on its way and its answer carries the receipt alone: the move queued behind is not sent, and says so', async ({ page, api }) => {
  const key = await freeProjectKey(api, 'EV')
  await makeProject(api, key)
  await makeTicket(api, key, '1', { title: 'Moved and left' })
  await page.goto(`/dispatch/${key}?view=board`)
  const board = page.locator('[data-board]')
  const card = board.locator(`[data-card="${key}-1"]`)
  const inColumn = (column: string) => board.locator(`[data-column="${column}"] [data-card="${key}-1"]`)
  await expect(inColumn('backlog')).toBeVisible()
  const edits: string[] = []
  let release!: () => void
  const held = new Promise<void>((resolve) => (release = resolve))
  const edit = (url: URL) => url.pathname === `/api/dispatch/tickets/${key}-1/edit`
  await page.route(edit, async (route) => {
    edits.push((route.request().postDataJSON() as { set?: { status?: string } }).set?.status ?? '')
    await held
    // The server's read after the commit failed: the answer carries the receipt alone.
    const res = await route.fetch()
    const body = (await res.json()) as Record<string, unknown>
    await route.fulfill({ response: res, json: { ...body, ticket: null } })
  })
  try {
    await card.focus()
    await page.keyboard.press('ArrowRight')
    await expect(inColumn('ready')).toBeVisible()
    await expect.poll(() => edits.length).toBe(1)
    // Moved back while Ready is on its way, then the projects page, in the app.
    await page.keyboard.press('ArrowLeft')
    await expect(inColumn('backlog')).toBeVisible()
    await page.locator('[data-project-header] a[href="/dispatch"]:visible').first().click()
    await expect(page).toHaveURL(/\/dispatch$/)
    release()
    await expect(page.getByText('Not sent: the page was left').first()).toBeVisible({ timeout: 15_000 })
    expect(edits).toEqual(['ready'])
    expect((await storedTicket(api, `${key}-1`)).status).toBe('ready')
  } finally {
    release()
    await page.unroute(edit)
  }
})

test('a move whose two replies are both lost after the store committed it: the ticket is read before the move queued behind, which is sent', async ({ page, api }) => {
  const key = await freeProjectKey(api, 'EV')
  await makeProject(api, key)
  await makeTicket(api, key, '1', { title: 'Moved, replies lost' })
  await page.goto(`/dispatch/${key}?view=board`)
  const board = page.locator('[data-board]')
  const card = board.locator(`[data-card="${key}-1"]`)
  const inColumn = (column: string) => board.locator(`[data-column="${column}"] [data-card="${key}-1"]`)
  await expect(inColumn('backlog')).toBeVisible()
  const edits: string[] = []
  let release!: () => void
  const held = new Promise<void>((resolve) => (release = resolve))
  const edit = (url: URL) => url.pathname === `/api/dispatch/tickets/${key}-1/edit`
  await page.route(edit, async (route) => {
    edits.push((route.request().postDataJSON() as { set?: { status?: string } }).set?.status ?? '')
    if (edits.length > 2) return route.continue()
    // Ready, and its resend with the same op id: each committed (the resend answered from the receipt), each reply lost.
    if (edits.length === 1) await held
    await route.fetch()
    await route.abort('failed')
  })
  try {
    await card.focus()
    await page.keyboard.press('ArrowRight')
    await expect(inColumn('ready')).toBeVisible()
    await expect.poll(() => edits.length).toBe(1)
    await page.keyboard.press('ArrowLeft')
    release()
    await expect.poll(() => edits).toEqual(['ready', 'ready', 'backlog'])
    await expect.poll(async () => (await storedTicket(api, `${key}-1`)).status).toBe('backlog')
    await expect(inColumn('backlog')).toBeVisible()
  } finally {
    release()
    await page.unroute(edit)
  }
})

test('the roadmap: Now, Next, Later and a folded Not planned; Next and Now by → and ← on a focused card; the milestones beside with done/total', async ({ page, api }) => {
  const key = await freeProjectKey(api, 'EV')
  // No close policy: a ticket can be made done for the milestone's count.
  await makeProject(api, key, [])
  const v1 = await makeMilestone(api, key, 'v1', '2026-12-01')
  await makeTicket(api, key, '1', { title: 'Roadmap now', horizon: 'now', status: 'in_progress', milestone: v1 })
  await makeTicket(api, key, '2', { title: 'Roadmap later', horizon: 'later', status: 'done', milestone: v1 })
  await makeTicket(api, key, '3', { title: 'Roadmap unplanned', status: 'cancelled', milestone: v1 })

  await page.goto(`/dispatch/${key}?view=roadmap`)
  const roadmap = page.locator('[data-roadmap]')
  await expect(roadmap.locator('[data-column]')).toHaveCount(4)
  expect(await roadmap.locator('[data-column]').evaluateAll((els) => els.map((el) => el.getAttribute('data-column')))).toEqual(['now', 'next', 'later', 'none'])
  const inColumn = (column: string, k: string) => roadmap.locator(`[data-column="${column}"] [data-card="${k}"]`)
  await expect(inColumn('now', `${key}-1`)).toBeVisible()
  await expect(inColumn('later', `${key}-2`)).toBeVisible()
  // Not planned is folded: its count shows, its cards do not until it is opened.
  const none = roadmap.locator('[data-column="none"]')
  await expect(none).toHaveAttribute('data-folded', '')
  await expect(none).toHaveAttribute('data-count', '1')
  await none.locator('[data-column-unfold]').click()
  await expect(inColumn('none', `${key}-3`)).toBeVisible()

  // → on a focused card in Now: Next; ← back: Now.
  const card = roadmap.locator(`[data-card="${key}-1"]`)
  await card.focus()
  await page.keyboard.press('ArrowRight')
  await expect(inColumn('next', `${key}-1`)).toBeVisible()
  await expect(card).toBeFocused()
  await expect.poll(async () => (await storedTicket(api, `${key}-1`)).horizon).toBe('next')
  await page.keyboard.press('ArrowLeft')
  await expect(inColumn('now', `${key}-1`)).toBeVisible()
  await expect.poll(async () => (await storedTicket(api, `${key}-1`)).horizon).toBe('now')

  // The milestone beside: one of the two planned is done; the cancelled one counts in neither.
  const milestone = roadmap.locator(`[data-roadmap-milestones] [data-milestone="${v1}"]`)
  await expect(milestone).toHaveAttribute('data-done', '1')
  await expect(milestone).toHaveAttribute('data-total', '2')
  await expect(milestone.locator('[data-milestone-count]')).toHaveText('1/2')
})

test('the roadmap: a Not planned ticket opened from its unfolded column and left by Escape has its card focused again, the column unfolded for it', async ({ page, api }) => {
  const key = await freeProjectKey(api, 'EV')
  await makeProject(api, key)
  await makeTicket(api, key, '1', { title: 'Unplanned and opened' })
  await page.goto(`/dispatch/${key}?view=roadmap`)
  const roadmap = page.locator('[data-roadmap]')
  const none = roadmap.locator('[data-column="none"]')
  await expect(none).toHaveAttribute('data-folded', '')
  await none.locator('[data-column-unfold]').click()
  const card = roadmap.locator(`[data-column="none"] [data-card="${key}-1"]`)
  await card.click()
  await expect(page).toHaveURL(new RegExp(`/dispatch/${key}/1$`))
  await expect(page.locator('[data-ticket-title]')).toHaveText('Unplanned and opened')
  await page.keyboard.press('Escape')
  await expect(page).toHaveURL(new RegExp(`/dispatch/${key}\\?view=roadmap$`))
  // The roadmap drawn again folds Not planned; the card's way back unfolds it.
  await expect(card).toBeFocused()
  await expect(none).not.toHaveAttribute('data-folded', '')
})

test('the save dialog holds while its save is on its way: Escape and Cancel leave it as it is, and the answer closes it', async ({ page, api }) => {
  const key = await freeProjectKey(api, 'EV')
  await makeProject(api, key)
  await makeTicket(api, key, '1', { title: 'Held save', status: 'ready' })
  await page.goto(`/dispatch/${key}?status=ready`)
  await expect(page.locator('[data-views-bar]')).toHaveAttribute('data-loaded', '')
  let release!: () => void
  const held = new Promise<void>((resolve) => (release = resolve))
  const views = (url: URL) => url.pathname === '/api/dispatch/views'
  await page.route(views, async (route) => {
    if (route.request().method() === 'POST') await held
    await route.continue()
  })
  try {
    await page.locator('[data-view-save]').click()
    const dialog = page.locator('[data-view-dialog]')
    await expect(dialog).toBeVisible()
    await dialog.locator('[data-view-name-input]').fill('Held')
    const sent = page.waitForRequest((r) => r.method() === 'POST' && new URL(r.url()).pathname === '/api/dispatch/views')
    await page.locator('[data-view-submit]').click()
    await sent
    await page.keyboard.press('Escape')
    await expect(dialog).toBeVisible()
    await expect(page.getByRole('dialog').filter({ has: dialog }).getByRole('button', { name: 'Cancel' })).toBeDisabled()
    await expect(dialog.locator('[data-view-name-input]')).toHaveValue('Held')
    release()
    await expect(dialog).toBeHidden()
    await expect(page.locator('[data-views-bar] [data-view-chip][data-view-name="Held"]')).toHaveAttribute('data-active', '')
  } finally {
    release()
    await page.unroute(views)
  }
})

test("at a phone's width (390 px) the list's rows wrap, the board scrolls sideways and the ticket's page is one column, and the page itself never scrolls sideways", async ({ browser, api, state }) => {
  const key = await freeProjectKey(api, 'EV')
  await makeProject(api, key)
  await makeTicket(api, key, '1', { title: 'A ticket whose title is long enough to wrap on a phone, as titles of real tickets are', status: 'in_progress', priority: 3, owner: 'Ann' })
  other = await browser.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })
  const phone = await secondBrowser(other, state.token, 'Nate')

  // The list: the row's key and title on a line of their own, the rest under them; nothing wider than the window.
  await phone.goto(`/dispatch/${key}`)
  const row = phone.locator(`[data-ticket-row="${key}-1"]`)
  await expect(row).toBeVisible()
  const title = await row.locator('[data-row-title]').boundingBox()
  const updated = await row.locator('[data-row-updated]').boundingBox()
  expect(title && updated && updated.y > title.y).toBe(true)
  expect(await sideways(phone)).toBeLessThanOrEqual(0)

  // The board: its columns scroll sideways inside it, the page does not.
  await phone.goto(`/dispatch/${key}?view=board`)
  const board = phone.locator('[data-board]')
  await expect(board.locator(`[data-column="in_progress"] [data-card="${key}-1"]`)).toBeVisible()
  expect(await board.evaluate((el) => el.scrollWidth > el.clientWidth)).toBe(true)
  expect(await sideways(phone)).toBeLessThanOrEqual(0)

  // The roadmap, the same.
  await phone.goto(`/dispatch/${key}?view=roadmap`)
  await expect(phone.locator('[data-roadmap] [data-column="none"]')).toBeVisible()
  expect(await sideways(phone)).toBeLessThanOrEqual(0)

  // The ticket's page: one column, its properties under its title.
  await phone.goto(`/dispatch/${key}/1`)
  await expect(phone.locator('[data-ticket-title]')).toBeVisible()
  const head = await phone.locator('[data-ticket-title]').boundingBox()
  const properties = await phone.locator('[data-ticket-properties]').boundingBox()
  expect(head && properties && properties.y > head.y && properties.x < 60).toBe(true)
  expect(await sideways(phone)).toBeLessThanOrEqual(0)
})
