import type { BrowserContext, Page } from '@playwright/test'
import { expect, test, type Api } from './fixtures'

// Dispatch's pages (docs/plans/dispatch.md, PR-8): a project made from the projects page, a ticket made from the project page's New
// ticket button, the ticket's page editing its title, its description (the preview shown), its priority and its owner, its history
// listing the fields set, the counts; and a second browser that sees the title edited and the projects page's open count move with no
// reload, by the dispatch events. Then what review round 1 found: two assignees ticked while the first one's edit is held both kept; an
// editor's save that never replaces another person's text unseen; a filter whose first read fails read again; a ticket's page opened
// before its ticket is made. And review round 2's: an edit waiting behind one whose reply a later read overtook, made on the revision
// that one committed; each page's first read, failed with the stream connected already, read again; what is typed while an editor's
// save goes, kept. And review round 3's: an edit waiting behind one whose reply carries no ticket, made on the revision its receipt
// names; an owner picked back while the edit of another is on its way, kept; an acceptance made again for the same owner, in the
// history. And review round 4's: a title typed back to the one shown while its save goes, kept and saved after it; a new ticket's and
// a new project's dialog, which stay open while what they make is on its way and are closed by its answer alone. And review round 5's:
// an owner picked back behind an edit whose reply carries no ticket, and behind one refused for a stale revision, kept while the reads
// after it fail and made once one answers; a new ticket and a new project answered with their receipts alone, said and opened by the
// key the receipt names. The spec names what it made: a project of its own per test (E2E, or E2E1, E2E2… when the server holds the
// earlier ones), and counts only the rows of that project.
test.describe.configure({ mode: 'serial' })

let other: BrowserContext | null = null

test.afterEach(async () => {
  await other?.close()
  other = null
})

/** The first of E2E, E2E1, E2E2… that no project, archived or not, holds on this server. */
async function freeKey(api: Api): Promise<string> {
  const taken = new Set<string>()
  let cursor = ''
  do {
    const page = await api.ok<{ projects: Array<{ key: string }>; next?: string }>('GET', `/api/dispatch/projects?archived=1${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`)
    for (const p of page.projects) taken.add(p.key)
    cursor = page.next ?? ''
  } while (cursor)
  for (let i = 0; i < 1000; i++) {
    const key = i === 0 ? 'E2E' : `E2E${i}`
    if (!taken.has(key)) return key
  }
  throw new Error('every E2E key is taken on this server')
}

/** An op id of the spec's own, unique on the server since the project's key is: E2E3-t1. */
function opOf(key: string, what: string): string {
  return `${key.toLowerCase()}-${what}`
}

/** A project made through the API, and its first ticket when `title` is given (its key KEY-1). */
async function made(api: Api, key: string, title?: string): Promise<void> {
  await api.ok('POST', '/api/dispatch/projects', { opId: opOf(key, 'p'), key, name: `E2E project ${key}` })
  if (title) await api.ok('POST', '/api/dispatch/tickets', { opId: opOf(key, 't1'), project: key, title })
}

/** The ticket as the store holds it. */
async function stored(api: Api, key: string) {
  return (await api.ok<{ ticket: { title: string; description: string; acceptance: string; priority: number; owner: string; assignees: string[] } }>('GET', `/api/dispatch/tickets/${key}`)).ticket
}

/** An edit of a ticket as the page sent it. */
interface SentEdit {
  expectedRev?: number
  set?: Record<string, unknown>
  add?: { assignees?: string[] }
  remove?: { assignees?: string[] }
}

/** What the page's edits of a ticket sent, in order, by the request's body, the edit route held where `hold` says (from 1). */
async function recordEdits(page: Page, hold?: { n: number; until: Promise<void> }): Promise<SentEdit[]> {
  const sent: SentEdit[] = []
  await page.route('**/api/dispatch/tickets/*/edit', async (route) => {
    sent.push(route.request().postDataJSON())
    if (hold && sent.length === hold.n) await hold.until
    await route.continue()
  })
  return sent
}

/** A second person's workbench on the same server. */
async function secondBrowser(context: BrowserContext, token: string): Promise<Page> {
  await context.addInitScript((t) => {
    localStorage.setItem('conductor.workbenchToken', t)
    localStorage.setItem('conductor.displayName', 'Priya')
  }, token)
  return context.newPage()
}

/** Marks the page's window, to tell later that it was not loaded again. */
async function mark(page: Page): Promise<void> {
  await page.evaluate(() => ((window as unknown as { dispatchMark?: string }).dispatchMark = 'kept'))
}
async function marked(page: Page): Promise<string | undefined> {
  return page.evaluate(() => (window as unknown as { dispatchMark?: string }).dispatchMark)
}

test('a project, a ticket made from its page and edited on its own page, its history and counts, and a second browser that follows', async ({ page, api, state, browser }) => {
  const key = await freeKey(api)
  const name = `E2E project ${key}`
  const owner = `Owner ${key}`
  await page.addInitScript(() => localStorage.setItem('conductor.displayName', 'Nate'))

  // New project, from the projects page: the key is upper case as it is typed.
  await page.goto('/dispatch')
  await page.locator('[data-new-project-open]').click()
  const dialog = page.locator('[data-new-project]')
  await expect(dialog).toBeVisible()
  await dialog.locator('[data-new-project-key]').fill(key.toLowerCase())
  await expect(dialog.locator('[data-new-project-key]')).toHaveValue(key)
  await dialog.locator('[data-new-project-name]').fill(name)
  await page.locator('[data-new-project-submit]').click()
  // Its page opens, with no tickets yet.
  await expect(page).toHaveURL(new RegExp(`/dispatch/${key}$`))
  await expect(page.locator('[data-project-name]')).toHaveText(name)
  await expect(page.locator('[data-list-empty]')).toBeVisible()

  // A second browser on the projects page: the project's row, nothing open.
  other = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const second = await secondBrowser(other, state.token)
  await second.goto('/dispatch')
  const secondRow = second.locator(`[data-project-row="${key}"]`)
  await expect(secondRow.locator('[data-project-name]')).toHaveText(name)
  await expect(secondRow.locator('[data-project-open]')).toHaveAttribute('data-count', '0')
  await mark(second)

  // New ticket, from the project page's button: the project's first, its key KEY-1.
  await page.locator('[data-new-ticket-open]').click()
  await page.locator('[data-new-ticket-title]').fill('Fix the login')
  await page.locator('[data-new-ticket-submit]').click()
  const row = page.locator(`[data-ticket-row="${key}-1"]`)
  await expect(row.locator('[data-row-title]')).toHaveText('Fix the login')
  await expect(row.locator('[data-row-key]')).toHaveText(`${key}-1`)
  await expect(page.locator('[data-list-total]')).toHaveAttribute('data-count', '1')

  // The second browser's projects page counts it, with no reload.
  await expect(secondRow.locator('[data-project-open]')).toHaveAttribute('data-count', '1')
  expect(await marked(second)).toBe('kept')

  // The ticket's page, from its row.
  await row.click()
  await expect(page).toHaveURL(new RegExp(`/dispatch/${key}/1$`))
  await expect(page.locator('[data-ticket-key]')).toHaveText(`${key}-1`)
  await expect(page.locator('[data-ticket-title]')).toHaveText('Fix the login')

  // The second browser on the same ticket.
  await second.goto(`/dispatch/${key}/1`)
  await expect(second.locator('[data-ticket-title]')).toHaveText('Fix the login')
  await mark(second)

  // The title, edited in place: the second browser sees it with no reload.
  await page.locator('[data-title-edit]').click()
  await page.locator('[data-title-input]').fill('Fix the login page')
  await page.locator('[data-title-input]').press('Enter')
  await expect(page.locator('[data-ticket-title]')).toHaveText('Fix the login page')
  await expect(second.locator('[data-ticket-title]')).toHaveText('Fix the login page')
  expect(await marked(second)).toBe('kept')

  // The description, written as Markdown: its preview while it is written, and rendered once saved.
  const description = page.locator('[data-ticket-markdown="description"]')
  await description.locator('[data-markdown-empty]').click()
  await description.locator('[data-markdown-input]').fill('## Steps\n\n1. Open the **login** page\n2. See [the notes](notes.md)')
  await description.getByRole('tab', { name: 'Preview' }).click()
  await expect(description.locator('[data-markdown-draft-preview] h2')).toHaveText('Steps')
  await description.locator('[data-markdown-save]').click()
  const view = description.locator('[data-markdown-view]')
  await expect(view.locator('h2')).toHaveText('Steps')
  await expect(view.locator('strong')).toHaveText('login')
  // A relative link names no file of a ticket's: its words stay, and nothing opens.
  await expect(view.locator('.md-link-held')).toHaveText('the notes')
  await expect(view.locator('a')).toHaveCount(0)

  // The priority.
  await page.locator('[data-ticket-priority]').click()
  await page.getByRole('option', { name: 'Urgent' }).click()
  await expect(page.locator('[data-ticket-priority]')).toContainText('Urgent')

  // The owner: a name typed, which the directory gains as it is set.
  await page.locator('[data-ticket-owner]').click()
  await page.getByPlaceholder('A name…', { exact: true }).fill(owner)
  await page.getByRole('option', { name: `Make ${owner} the owner` }).click()
  await expect(page.locator('[data-ticket-owner]')).toContainText(owner)
  const people = await api.ok<{ people: Array<{ name: string }> }>('GET', '/api/dispatch/people')
  expect(people.people.map((p) => p.name)).toContain(owner)

  // The assignees, the first tick's edit held on its way: Codex ticked meanwhile, and Goose ticked and unticked. The picker shows the
  // picks while they wait; both agents are kept, each one operation made on the ticket as the edit before it left it, and Goose, ticked
  // and unticked before its edit went, is no operation. Then a person typed, the next operation, with nothing sent between.
  let release!: () => void
  const sent = await recordEdits(page, { n: 1, until: new Promise<void>((resolve) => (release = resolve)) })
  const assignees = page.locator('[data-ticket-assignees]')
  const chips = assignees.locator('[data-assignee]')
  const revAt = Number(await page.locator('[data-ticket-rev]').getAttribute('data-ticket-rev'))
  await assignees.click()
  await page.getByRole('option', { name: 'Claude Code (e2e stub)', exact: true }).click()
  await expect.poll(() => sent.length).toBe(1)
  await page.getByRole('option', { name: 'Codex CLI (e2e stub)', exact: true }).click()
  await page.getByRole('option', { name: 'Goose (e2e stub)', exact: true }).click()
  await expect(assignees.locator('[data-assignee="agent:goose"]')).toHaveCount(1)
  await page.getByRole('option', { name: 'Goose (e2e stub)', exact: true }).click()
  await expect(assignees.locator('[data-assignee="agent:goose"]')).toHaveCount(0)
  await expect(chips).toHaveCount(2)
  release()
  await expect(page.locator('[data-ticket-rev]')).toHaveAttribute('data-ticket-rev', String(revAt + 2))
  expect([...(await stored(api, `${key}-1`)).assignees].sort()).toEqual(['agent:claude', 'agent:codex'])
  const helper = `Helper ${key}`
  await page.getByPlaceholder('A name or an agent…').fill(helper)
  await page.getByRole('option', { name: `Assign ${helper}`, exact: true }).click()
  await page.keyboard.press('Escape')
  await expect(page.locator('[data-ticket-rev]')).toHaveAttribute('data-ticket-rev', String(revAt + 3))
  await page.unroute('**/api/dispatch/tickets/*/edit')
  expect(sent.map((b) => [b.add?.assignees ?? [], b.remove?.assignees ?? []])).toEqual([
    [['agent:claude'], []],
    [['agent:codex'], []],
    [[`person:${helper}`], []],
  ])
  await expect(chips).toHaveCount(3)
  await expect(assignees.locator(`[data-assignee="person:${helper}"]`)).toContainText(helper)
  await expect(assignees.locator('[data-assignee="agent:claude"]')).toContainText('Claude Code (e2e stub)')
  await expect(assignees.locator('[data-assignee="agent:codex"]')).toContainText('Codex CLI (e2e stub)')

  // The history lists every field set, under who set it.
  const history = page.locator('[data-ticket-history]')
  await expect(history).toContainText('made the ticket')
  await expect(history).toContainText('set the title to "Fix the login page"')
  await expect(history).toContainText('wrote the description')
  await expect(history).toContainText('set the priority to Urgent')
  await expect(history).toContainText(`made ${owner} the owner`)
  await expect(history).toContainText('assigned Claude Code (e2e stub)')
  await expect(history).toContainText('assigned Codex CLI (e2e stub)')
  await expect(history).toContainText(`assigned ${helper}`)
  await expect(history).not.toContainText('Goose')
  await expect(history.locator('[data-history-who]').first()).toHaveText('Nate')

  // The second browser followed every edit, still with no reload.
  await expect(second.locator('[data-ticket-priority]')).toContainText('Urgent')
  await expect(second.locator('[data-ticket-owner]')).toContainText(owner)
  await expect(second.locator('[data-ticket-markdown="description"] [data-markdown-view] h2')).toHaveText('Steps')
  expect(await marked(second)).toBe('kept')

  // The counts: the project's list holds its one ticket, and the projects page counts it open, none done.
  await page.goto(`/dispatch/${key}`)
  await expect(page.locator(`[data-ticket-row="${key}-1"] [data-row-title]`)).toHaveText('Fix the login page')
  await expect(page.locator('[data-list-total]')).toHaveAttribute('data-count', '1')
  await page.goto('/dispatch')
  const mine = page.locator(`[data-project-row="${key}"]`)
  await expect(mine.locator('[data-project-open]')).toHaveAttribute('data-count', '1')
  await expect(mine.locator('[data-project-done]')).toHaveAttribute('data-count', '0')
})

test("an editor's save never replaces another person's edit unseen: their text is said, the draft kept, and a second save replaces it", async ({ page, api }) => {
  const key = await freeKey(api)
  await made(api, key, 'The login')
  const sent = await recordEdits(page)
  await page.goto(`/dispatch/${key}/1`)
  await expect(page.locator('[data-ticket-title]')).toHaveText('The login')
  const rev = page.locator('[data-ticket-rev]')
  await expect(rev).toHaveAttribute('data-ticket-rev', '1')

  // The description, opened and written; another person saves theirs meanwhile, which the page reads live behind the editor.
  const description = page.locator('[data-ticket-markdown="description"]')
  await description.locator('[data-markdown-empty]').click()
  await description.locator('[data-markdown-input]').fill('Mine')
  await api.ok('POST', `/api/dispatch/tickets/${key}-1/edit`, { opId: opOf(key, 'theirs-d'), byName: 'Priya', set: { description: 'Theirs' } })
  await expect(rev).toHaveAttribute('data-ticket-rev', '2')
  // Save sends nothing: their text is shown and the draft kept.
  await description.locator('[data-markdown-save]').click()
  await expect(description.locator('[data-markdown-theirs-text]')).toHaveText('Theirs')
  await expect(description.locator('[data-markdown-input]')).toHaveValue('Mine')

  // The title, the same way.
  await page.locator('[data-title-edit]').click()
  await page.locator('[data-title-input]').fill('My login')
  await api.ok('POST', `/api/dispatch/tickets/${key}-1/edit`, { opId: opOf(key, 'theirs-t'), byName: 'Priya', set: { title: 'Their login' } })
  await expect(rev).toHaveAttribute('data-ticket-rev', '3')
  await page.locator('[data-title-input]').press('Enter')
  await expect(page.locator('[data-title-theirs]')).toContainText('Their login')
  await expect(page.locator('[data-title-input]')).toHaveValue('My login')
  expect(await stored(api, `${key}-1`)).toMatchObject({ title: 'Their login', description: 'Theirs' })

  // A second save replaces each, knowingly; the page sent those two and nothing before them.
  await page.locator('[data-title-input]').press('Enter')
  await expect(page.locator('[data-ticket-title]')).toHaveText('My login')
  await description.locator('[data-markdown-save]').click()
  await expect(description.locator('[data-markdown-view]')).toHaveText('Mine')
  await expect(rev).toHaveAttribute('data-ticket-rev', '5')
  expect(await stored(api, `${key}-1`)).toMatchObject({ title: 'My login', description: 'Mine' })
  expect(sent.map((b) => b.set)).toEqual([{ title: 'My login' }, { description: 'Mine' }])

  // The acceptance criteria, saved before the page has read another person's: a read of the ticket that carries their edit (rev 6) is
  // held until the save has gone, so the save goes on rev 5 and the store refuses it stale_revision; the page reads the ticket again,
  // and says theirs the same way.
  let saving = false
  const held: Array<() => void> = []
  await page.route(`**/api/dispatch/tickets/${key}-1`, async (route) => {
    const response = await route.fetch()
    const read = (await response.json()) as { ticket: { rev: number } }
    if (!saving && read.ticket.rev >= 6) await new Promise<void>((resolve) => held.push(resolve))
    await route.fulfill({ response })
  })
  const acceptance = page.locator('[data-ticket-markdown="acceptance"]')
  await acceptance.locator('[data-markdown-empty]').click()
  await acceptance.locator('[data-markdown-input]').fill('Mine to accept')
  await api.ok('POST', `/api/dispatch/tickets/${key}-1/edit`, { opId: opOf(key, 'theirs-a'), byName: 'Priya', set: { acceptance: 'Theirs to accept' } })
  // Their event's read is on its way, held.
  await expect.poll(() => held.length).toBeGreaterThanOrEqual(1)
  await expect(rev).toHaveAttribute('data-ticket-rev', '5')
  saving = true
  await acceptance.locator('[data-markdown-save]').click()
  await expect(acceptance.locator('[data-markdown-theirs-text]')).toHaveText('Theirs to accept')
  await expect(acceptance.locator('[data-markdown-input]')).toHaveValue('Mine to accept')
  await expect(rev).toHaveAttribute('data-ticket-rev', '6')
  expect((await stored(api, `${key}-1`)).acceptance).toBe('Theirs to accept')
  for (const release of held) release()
  await acceptance.locator('[data-markdown-save]').click()
  await expect(acceptance.locator('[data-markdown-view]')).toHaveText('Mine to accept')
  expect((await stored(api, `${key}-1`)).acceptance).toBe('Mine to accept')
  // The refused save and the second one.
  expect(sent.map((b) => b.set).slice(2)).toEqual([{ acceptance: 'Mine to accept' }, { acceptance: 'Mine to accept' }])
})

test('a filter whose first read fails is read again, with no further event and nothing done', async ({ page, api }) => {
  const key = await freeKey(api)
  await made(api, key, 'Waiting')
  await page.goto(`/dispatch/${key}`)
  const row = page.locator(`[data-ticket-row="${key}-1"]`)
  await expect(row).toBeVisible()
  // The filtered list's read fails, and so does the one retry of it that $fetch makes of a GET by itself, as a server busy for a moment
  // answers; the next goes through.
  let asked = 0
  await page.route(/\/api\/dispatch\/tickets\?.*status=backlog/, async (route) => {
    if (++asked <= 2) return route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: { code: 'unavailable', message: 'The store is busy.' } }) })
    return route.continue()
  })
  await page.locator('[data-filter-status]').click()
  await page.getByRole('option', { name: 'Backlog', exact: true }).click()
  await page.keyboard.press('Escape')
  await expect(page).toHaveURL(/status=backlog/)
  // Asked again after the window, by the page alone.
  await expect.poll(() => asked).toBeGreaterThanOrEqual(3)
  await expect(row).toBeVisible()
  await expect(page.locator('[data-list-total]')).toHaveAttribute('data-count', '1')
})

test("a ticket's page opened before its ticket is made says so, and shows it once it is made, with no reload", async ({ page, api }) => {
  const key = await freeKey(api)
  await made(api, key)
  // A project key no project has says so too.
  const none = await freeKey(api)
  await page.goto(`/dispatch/${none}`)
  await expect(page.locator('[data-project-missing]')).toBeVisible()

  await page.goto(`/dispatch/${key}/1`)
  await expect(page.locator('[data-ticket-missing]')).toBeVisible()
  await mark(page)
  await api.ok('POST', '/api/dispatch/tickets', { opId: opOf(key, 't1'), project: key, title: 'Made since' })
  await expect(page.locator('[data-ticket-title]')).toHaveText('Made since')
  await expect(page.locator('[data-ticket-missing]')).toHaveCount(0)
  expect(await marked(page)).toBe('kept')
})

test('an edit waiting behind one whose reply a later read overtook is made on the revision that one committed', async ({ page, api }) => {
  const key = await freeKey(api)
  await made(api, key, 'In turn')
  let release!: () => void
  const sent = await recordEdits(page, { n: 1, until: new Promise<void>((resolve) => (release = resolve)) })
  await page.goto(`/dispatch/${key}/1`)
  const rev = page.locator('[data-ticket-rev]')
  await expect(rev).toHaveAttribute('data-ticket-rev', '1')

  // The priority set: its edit held on its way, before the store has it.
  await page.locator('[data-ticket-priority]').click()
  await page.getByRole('option', { name: 'Urgent' }).click()
  await expect.poll(() => sent.length).toBe(1)
  // A read of the ticket sent after that edit and answered before its commit: another ticket of the project made, whose event reads the
  // page again after the window. Its reply, rev 1, is applied, so the held edit's reply, numbered before it, is dropped when it comes.
  const reread = page.waitForResponse((r) => r.request().method() === 'GET' && new URL(r.url()).pathname === `/api/dispatch/tickets/${key}-1`)
  await api.ok('POST', '/api/dispatch/tickets', { opId: opOf(key, 't2'), project: key, title: 'Another' })
  expect(((await (await reread).json()) as { ticket: { rev: number } }).ticket.rev).toBe(1)
  // The owner set meanwhile: its edit waits its turn behind the priority's.
  const owner = `Owner ${key}`
  await page.locator('[data-ticket-owner]').click()
  await page.getByPlaceholder('A name…', { exact: true }).fill(owner)
  await page.getByRole('option', { name: `Make ${owner} the owner` }).click()
  release()

  // The priority's edit commits rev 2, and the page reads the ticket at it before the owner's edit is made: that one expects rev 2 and
  // is done. Made on the rev 1 shown, it would be refused stale_revision.
  await expect(rev).toHaveAttribute('data-ticket-rev', '3')
  await expect(page.locator('[data-ticket-priority]')).toContainText('Urgent')
  await expect(page.locator('[data-ticket-owner]')).toContainText(owner)
  expect(sent.map((b) => [b.set, b.expectedRev])).toEqual([
    [{ priority: 4 }, 1],
    [{ owner }, 2],
  ])
  expect(await stored(api, `${key}-1`)).toMatchObject({ priority: 4, owner })
})

test("each page's first read, failed with the stream connected already, is read again with no event, no snapshot and no reload", async ({ page, api }) => {
  const key = await freeKey(api)
  await made(api, key)
  // The projects page, its stream connected: the project's open count follows a ticket made through the API, by the ticket's event.
  await page.goto('/dispatch')
  const project = page.locator(`[data-project-row="${key}"]`)
  await expect(project.locator('[data-project-open]')).toHaveAttribute('data-count', '0')
  await api.ok('POST', '/api/dispatch/tickets', { opId: opOf(key, 't1'), project: key, title: 'Read again' })
  await expect(project.locator('[data-project-open]')).toHaveAttribute('data-count', '1')
  await mark(page)

  /**
   * Fails the first two reads `url` matches as a server busy for a moment answers (a read and the one retry of it that $fetch makes of a
   * GET by itself), lets the next go through, and counts them.
   */
  async function failTwice(url: RegExp): Promise<{ asked: number }> {
    const r = { asked: 0 }
    await page.route(url, async (route) => {
      if (route.request().method() !== 'GET') return route.fallback()
      if (++r.asked <= 2) return route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: { code: 'unavailable', message: 'The store is busy.' } }) })
      return route.continue()
    })
    return r
  }

  // The project's page, opened from its row: its list's first read fails, and is asked again after the window by the page alone.
  const list = await failTwice(new RegExp(`/api/dispatch/tickets\\?project=${key}&`))
  await project.click()
  await expect(page).toHaveURL(new RegExp(`/dispatch/${key}$`))
  await expect.poll(() => list.asked).toBeGreaterThanOrEqual(3)
  const row = page.locator(`[data-ticket-row="${key}-1"]`)
  await expect(row.locator('[data-row-title]')).toHaveText('Read again')

  // The ticket's page, opened from its row: the same.
  const ticket = await failTwice(new RegExp(`/api/dispatch/tickets/${key}-1$`))
  await row.click()
  await expect(page).toHaveURL(new RegExp(`/dispatch/${key}/1$`))
  await expect.poll(() => ticket.asked).toBeGreaterThanOrEqual(3)
  await expect(page.locator('[data-ticket-title]')).toHaveText('Read again')

  // The projects page again, from the ticket page's Dispatch link: the same.
  const projects = await failTwice(/\/api\/dispatch\/projects(\?[^/]*)?$/)
  await page.locator('[data-ticket-header]').getByRole('link', { name: 'Dispatch', exact: true }).click()
  await expect(page).toHaveURL(/\/dispatch$/)
  await expect.poll(() => projects.asked).toBeGreaterThanOrEqual(3)
  await expect(project.locator('[data-project-open]')).toHaveAttribute('data-count', '1')
  // All in the one load of the page the test began with.
  expect(await marked(page)).toBe('kept')
})

test("what is typed while an editor's save goes is kept: the save is done, the newer text stays in the editor, and its own save follows", async ({ page, api }) => {
  const key = await freeKey(api)
  await made(api, key, 'Typed on')
  let release!: () => void
  const sent = await recordEdits(page, { n: 1, until: new Promise<void>((resolve) => (release = resolve)) })
  await page.goto(`/dispatch/${key}/1`)
  const rev = page.locator('[data-ticket-rev]')
  await expect(rev).toHaveAttribute('data-ticket-rev', '1')

  // The description saved, its edit held on its way; more is written meanwhile.
  const description = page.locator('[data-ticket-markdown="description"]')
  await description.locator('[data-markdown-empty]').click()
  const input = description.locator('[data-markdown-input]')
  const save = description.locator('[data-markdown-save]')
  await input.fill('First version')
  await save.click()
  await expect.poll(() => sent.length).toBe(1)
  await input.fill('First version, and more')
  release()
  // The save is done (Save is offered again): the store has what was saved, and the editor what was written after it.
  await expect(save).toBeEnabled()
  await expect(input).toHaveValue('First version, and more')
  await expect(rev).toHaveAttribute('data-ticket-rev', '2')
  expect((await stored(api, `${key}-1`)).description).toBe('First version')
  // Its own save is made against the text saved, with no notice of another person's, and closes the editor.
  await save.click()
  await expect(description.locator('[data-markdown-view]')).toHaveText('First version, and more')
  expect((await stored(api, `${key}-1`)).description).toBe('First version, and more')
  expect(sent.map((b) => b.set)).toEqual([{ description: 'First version' }, { description: 'First version, and more' }])

  // The title, the same way: Enter saves it, held, and more is typed.
  await page.unroute('**/api/dispatch/tickets/*/edit')
  let releaseTitle!: () => void
  const titled = await recordEdits(page, { n: 1, until: new Promise<void>((resolve) => (releaseTitle = resolve)) })
  await page.locator('[data-title-edit]').click()
  const title = page.locator('[data-title-input]')
  await title.fill('Typed on, first')
  await title.press('Enter')
  await expect.poll(() => titled.length).toBe(1)
  await title.fill('Typed on, first and more')
  releaseTitle()
  await expect(page.locator('[data-title-save]')).toBeEnabled()
  await expect(title).toHaveValue('Typed on, first and more')
  await expect(rev).toHaveAttribute('data-ticket-rev', '4')
  expect((await stored(api, `${key}-1`)).title).toBe('Typed on, first')
  await expect(page.locator('[data-title-theirs]')).toHaveCount(0)
  await title.press('Enter')
  await expect(page.locator('[data-ticket-title]')).toHaveText('Typed on, first and more')
  expect((await stored(api, `${key}-1`)).title).toBe('Typed on, first and more')
  expect(titled.map((b) => b.set)).toEqual([{ title: 'Typed on, first' }, { title: 'Typed on, first and more' }])
})

test("an edit waiting behind one whose reply carries no ticket is made on the revision that reply's receipt names", async ({ page, api }) => {
  const key = await freeKey(api)
  await made(api, key, 'No ticket')
  const ticketRead = new RegExp(`/api/dispatch/tickets/${key}-1$`)
  // The page's reads of its ticket from the first edit's commit on, held until two are asked or the second edit is sent. The commit's
  // event reads the page again at once: answered then, it could show the revision committed before the second edit is made, which would
  // expect it whether the page read it after the reply or not. With the page's own read after the reply, two are asked.
  let holding = false
  let reads = 0
  let letReadsGo!: () => void
  const readsGo = new Promise<void>((resolve) => (letReadsGo = resolve))
  await page.route(ticketRead, async (route) => {
    if (route.request().method() !== 'GET' || !holding) return route.fallback()
    if (++reads >= 2) letReadsGo()
    await readsGo
    await route.fallback()
  })
  // The page's edits, in order: the first held on its way, then answered as the server answers when its read after the commit fails,
  // its receipt done and no ticket.
  const sent: SentEdit[] = []
  let release!: () => void
  const held = new Promise<void>((resolve) => (release = resolve))
  let receiptRev: number | undefined
  await page.route('**/api/dispatch/tickets/*/edit', async (route) => {
    sent.push(route.request().postDataJSON())
    if (sent.length > 1) {
      letReadsGo()
      return route.continue()
    }
    await held
    holding = true
    const response = await route.fetch()
    const reply = (await response.json()) as { receipt: { rev?: number }; ticket: unknown }
    receiptRev = reply.receipt.rev
    await route.fulfill({ response, json: { ...reply, ticket: null } })
  })
  await page.goto(`/dispatch/${key}/1`)
  const rev = page.locator('[data-ticket-rev]')
  await expect(rev).toHaveAttribute('data-ticket-rev', '1')
  // The page's stream is connected and its snapshot taken: the ticket's description set through the API, whose event reads the page
  // again at once.
  await api.ok('POST', `/api/dispatch/tickets/${key}-1/edit`, { opId: opOf(key, 'd'), set: { description: 'Set elsewhere' } })
  await expect(rev).toHaveAttribute('data-ticket-rev', '2')

  // The priority set: its edit held on its way. The owner set meanwhile: its edit waits its turn behind the priority's.
  await page.locator('[data-ticket-priority]').click()
  await page.getByRole('option', { name: 'Urgent' }).click()
  await expect.poll(() => sent.length).toBe(1)
  const owner = `Owner ${key}`
  await page.locator('[data-ticket-owner]').click()
  await page.getByPlaceholder('A name…', { exact: true }).fill(owner)
  await page.getByRole('option', { name: `Make ${owner} the owner` }).click()
  release()

  // The priority's edit commits rev 3, which its reply says in its receipt alone: the page reads the ticket at it before the owner's edit
  // is made, which expects rev 3 and is done. Made on the rev 2 shown, it would be refused stale_revision.
  await expect(rev).toHaveAttribute('data-ticket-rev', '4')
  expect(receiptRev).toBe(3)
  await expect(page.locator('[data-ticket-priority]')).toContainText('Urgent')
  await expect(page.locator('[data-ticket-owner]')).toContainText(owner)
  expect(sent.map((b) => [b.set, b.expectedRev])).toEqual([
    [{ priority: 4 }, 2],
    [{ owner }, 3],
  ])
  expect(await stored(api, `${key}-1`)).toMatchObject({ priority: 4, owner })
})

test('an owner picked back while the edit setting another is on its way is the owner the ticket keeps', async ({ page, api }) => {
  const key = await freeKey(api)
  await made(api, key, 'Owned')
  const first = `Alice ${key}`
  const second = `Bob ${key}`
  await api.ok('POST', `/api/dispatch/tickets/${key}-1/edit`, { opId: opOf(key, 'o1'), set: { owner: first } })
  await api.ok('POST', '/api/dispatch/people', { opId: opOf(key, 'b'), name: second })
  let release!: () => void
  const sent = await recordEdits(page, { n: 1, until: new Promise<void>((resolve) => (release = resolve)) })
  await page.goto(`/dispatch/${key}/1`)
  const rev = page.locator('[data-ticket-rev]')
  await expect(rev).toHaveAttribute('data-ticket-rev', '2')
  const owner = page.locator('[data-ticket-owner]')
  await expect(owner).toContainText(first)

  // Bob picked: the edit held on its way, and the picker still shows Alice, the owner the store last answered. Alice picked back.
  await owner.click()
  await page.getByRole('option', { name: second, exact: true }).click()
  await expect.poll(() => sent.length).toBe(1)
  await expect(owner).toContainText(first)
  await owner.click()
  await page.getByRole('option', { name: first, exact: true }).click()
  release()

  // Bob's edit is done, then Alice's, made on the revision Bob's left: the ticket keeps the owner picked last.
  await expect(rev).toHaveAttribute('data-ticket-rev', '4')
  await expect(owner).toContainText(first)
  expect(sent.map((b) => [b.set, b.expectedRev])).toEqual([
    [{ owner: second }, 2],
    [{ owner: first }, 3],
  ])
  expect((await stored(api, `${key}-1`)).owner).toBe(first)
})

test('an acceptance made again for the same owner is in the history, each acceptance said once', async ({ page, api }) => {
  const key = await freeKey(api)
  await made(api, key, 'Accepted twice')
  const owner = `Owner ${key}`
  const ticket = `${key}-1`
  await api.ok('POST', `/api/dispatch/tickets/${ticket}/edit`, { opId: opOf(key, 'e1'), set: { owner, acceptance: 'It works' } })
  await api.ok('POST', `/api/dispatch/tickets/${ticket}/accept`, { opId: opOf(key, 'a1') })
  await api.ok('POST', `/api/dispatch/tickets/${ticket}/edit`, { opId: opOf(key, 'e2'), set: { acceptance: 'It works, and fast' } })
  // Accepted again for the same owner, by the same person: the store records its time and the criteria it was made against, no owner.
  await api.ok('POST', `/api/dispatch/tickets/${ticket}/accept`, { opId: opOf(key, 'a2') })
  await page.goto(`/dispatch/${key}/1`)
  const lines = page.locator('[data-ticket-history] [data-history-line]')
  await expect.poll(async () => (await lines.allTextContents()).filter((l) => l.includes('accept'))).toEqual([
    'wrote the acceptance criteria',
    `accepted it for ${owner}`,
    'edited the acceptance criteria',
    'accepted it again',
  ])
})

test('a title typed back to the one shown while its save goes stays in the editor, and its own Enter saves it after that save', async ({ page, api }) => {
  const key = await freeKey(api)
  await made(api, key, 'Original')
  let release!: () => void
  const sent = await recordEdits(page, { n: 1, until: new Promise<void>((resolve) => (release = resolve)) })
  await page.goto(`/dispatch/${key}/1`)
  const rev = page.locator('[data-ticket-rev]')
  await expect(rev).toHaveAttribute('data-ticket-rev', '1')

  // A title saved, its edit held on its way; the title shown typed back, and Enter pressed again while the save goes.
  await page.locator('[data-title-edit]').click()
  const title = page.locator('[data-title-input]')
  const save = page.locator('[data-title-save]')
  await title.fill('First save')
  await title.press('Enter')
  await expect.poll(() => sent.length).toBe(1)
  await expect(save).toBeDisabled()
  await title.fill('Original')
  await title.press('Enter')
  // Nothing is made of it while the save goes: the editor stays, with the title typed back.
  await expect(title).toHaveValue('Original')
  release()

  // The save is done: the store has the title saved, and the editor the one typed back, with no notice of another person's.
  await expect(rev).toHaveAttribute('data-ticket-rev', '2')
  await expect(save).toBeEnabled()
  await expect(title).toHaveValue('Original')
  await expect(page.locator('[data-title-theirs]')).toHaveCount(0)
  expect((await stored(api, `${key}-1`)).title).toBe('First save')
  // Its own Enter saves it, made on the revision that save left, and closes the editor.
  await title.press('Enter')
  await expect(page.locator('[data-ticket-title]')).toHaveText('Original')
  await expect(rev).toHaveAttribute('data-ticket-rev', '3')
  expect((await stored(api, `${key}-1`)).title).toBe('Original')
  expect(sent.map((b) => [b.set, b.expectedRev])).toEqual([
    [{ title: 'First save' }, 1],
    [{ title: 'Original' }, 2],
  ])
})

/** Holds the first creation `url` is sent (a POST) until `until` is done, and counts the creations sent. */
async function holdCreation(page: Page, url: string, until: Promise<void>): Promise<{ sent: number }> {
  const r = { sent: 0 }
  await page.route(url, async (route) => {
    if (route.request().method() !== 'POST') return route.fallback()
    if (++r.sent === 1) await until
    await route.continue()
  })
  return r
}

test("a new ticket's dialog stays open while its ticket is made, and only that ticket's answer closes it", async ({ page, api }) => {
  const key = await freeKey(api)
  await made(api, key)
  let release!: () => void
  const creations = await holdCreation(page, '**/api/dispatch/tickets', new Promise<void>((resolve) => (release = resolve)))
  await page.goto(`/dispatch/${key}`)
  await expect(page.locator('[data-list-empty]')).toBeVisible()

  // New ticket, made: its creation held on its way.
  await page.locator('[data-new-ticket-open]').click()
  const dialog = page.getByRole('dialog')
  const title = page.locator('[data-new-ticket-title]')
  await title.fill('Made while held')
  await page.locator('[data-new-ticket-submit]').click()
  await expect.poll(() => creations.sent).toBe(1)
  // Nothing closes it while the ticket is made: Escape and a click outside do nothing, its close button and Cancel are off, and it
  // keeps what was sent.
  await page.keyboard.press('Escape')
  await page.mouse.click(5, 5)
  await expect(dialog.getByRole('button', { name: 'Close', exact: true })).toBeDisabled()
  await expect(dialog.getByRole('button', { name: 'Cancel', exact: true })).toBeDisabled()
  await expect(title).toHaveValue('Made while held')
  release()
  // Answered: the dialog it was made from closes, and the list shows the ticket.
  await expect(dialog).toHaveCount(0)
  await expect(page.locator(`[data-ticket-row="${key}-1"] [data-row-title]`)).toHaveText('Made while held')

  // Opened again, with nothing on its way: an empty draft, which Cancel closes; and the next opening's own ticket, made.
  await page.locator('[data-new-ticket-open]').click()
  await expect(title).toHaveValue('')
  await title.fill('Not made')
  await dialog.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(dialog).toHaveCount(0)
  await page.locator('[data-new-ticket-open]').click()
  await expect(title).toHaveValue('')
  await title.fill('The next one')
  await page.locator('[data-new-ticket-submit]').click()
  await expect(dialog).toHaveCount(0)
  await expect(page.locator(`[data-ticket-row="${key}-2"] [data-row-title]`)).toHaveText('The next one')
  await expect(page.locator('[data-list-total]')).toHaveAttribute('data-count', '2')
  expect(creations.sent).toBe(2)
})

test("a new project's dialog stays open while its project is made, and that project's answer opens its page", async ({ page, api }) => {
  const key = await freeKey(api)
  const name = `E2E project ${key}`
  let release!: () => void
  const creations = await holdCreation(page, '**/api/dispatch/projects', new Promise<void>((resolve) => (release = resolve)))
  await page.goto('/dispatch')

  // New project, made: its creation held on its way.
  await page.locator('[data-new-project-open]').click()
  const dialog = page.getByRole('dialog')
  await page.locator('[data-new-project-key]').fill(key)
  await page.locator('[data-new-project-name]').fill(name)
  await page.locator('[data-new-project-submit]').click()
  await expect.poll(() => creations.sent).toBe(1)
  // Nothing closes it while the project is made, and the page stays: Escape and a click outside do nothing, its close button and
  // Cancel are off, and it keeps what was sent.
  await page.keyboard.press('Escape')
  await page.mouse.click(5, 5)
  await expect(dialog.getByRole('button', { name: 'Close', exact: true })).toBeDisabled()
  await expect(dialog.getByRole('button', { name: 'Cancel', exact: true })).toBeDisabled()
  await expect(page.locator('[data-new-project-name]')).toHaveValue(name)
  await expect(page).toHaveURL(/\/dispatch$/)
  release()
  // Answered: the project's page opens, from the dialog it was made from.
  await expect(page).toHaveURL(new RegExp(`/dispatch/${key}$`))
  await expect(page.locator('[data-project-name]')).toHaveText(name)
  await expect(dialog).toHaveCount(0)
  expect(creations.sent).toBe(1)
})

/**
 * Fails every read of the ticket `ticket` the page makes while `failing` is set, as a server busy for a moment answers (a read, and the
 * one retry of it that $fetch makes of a GET by itself), and counts them.
 */
async function failReads(page: Page, ticket: string): Promise<{ failing: boolean; failed: number }> {
  const r = { failing: false, failed: 0 }
  await page.route(new RegExp(`/api/dispatch/tickets/${ticket}$`), async (route) => {
    if (route.request().method() !== 'GET' || !r.failing) return route.fallback()
    r.failed++
    return route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: { code: 'unavailable', message: 'The store is busy.' } }) })
  })
  return r
}

test('an owner picked back behind an edit whose reply carries no ticket is kept while the reads after that edit fail, and made once one answers', async ({ page, api }) => {
  const key = await freeKey(api)
  await made(api, key, 'Owned')
  const first = `Alice ${key}`
  const second = `Bob ${key}`
  await api.ok('POST', `/api/dispatch/tickets/${key}-1/edit`, { opId: opOf(key, 'o1'), set: { owner: first } })
  await api.ok('POST', '/api/dispatch/people', { opId: opOf(key, 'b'), name: second })
  const reads = await failReads(page, `${key}-1`)
  // The page's edits, in order: the first held on its way, then answered as the server answers when its read after the commit fails,
  // its receipt done and no ticket; the page's reads of the ticket fail from its commit on.
  const sent: SentEdit[] = []
  let release!: () => void
  const held = new Promise<void>((resolve) => (release = resolve))
  let receiptRev: number | undefined
  /** How many reads had failed when the first edit's reply was handed to the page; -1 before. */
  let failedAtReply = -1
  await page.route('**/api/dispatch/tickets/*/edit', async (route) => {
    sent.push(route.request().postDataJSON())
    if (sent.length > 1) return route.continue()
    await held
    reads.failing = true
    const response = await route.fetch()
    const reply = (await response.json()) as { receipt: { rev?: number }; ticket: unknown }
    receiptRev = reply.receipt.rev
    failedAtReply = reads.failed
    await route.fulfill({ response, json: { ...reply, ticket: null } })
  })
  await page.goto(`/dispatch/${key}/1`)
  const rev = page.locator('[data-ticket-rev]')
  await expect(rev).toHaveAttribute('data-ticket-rev', '2')
  const owner = page.locator('[data-ticket-owner]')
  await expect(owner).toContainText(first)

  // Bob picked: his edit held on its way. Alice picked back meanwhile: her edit waits its turn behind his.
  await owner.click()
  await page.getByRole('option', { name: second, exact: true }).click()
  await expect.poll(() => sent.length).toBe(1)
  await owner.click()
  await page.getByRole('option', { name: first, exact: true }).click()
  release()

  // Bob's edit commits rev 3, which its reply says in its receipt alone, and the page's reads of the ticket fail: its read after that
  // reply, the commit's event's re-read, and each asked again, three of them at least after the reply. Alice's edit waits: made on the
  // rev 2 shown, which holds her already, it would change nothing and not be sent, and Bob would stay.
  await expect.poll(() => (failedAtReply < 0 ? 0 : reads.failed - failedAtReply)).toBeGreaterThanOrEqual(6)
  expect(receiptRev).toBe(3)
  expect(sent).toHaveLength(1)
  await expect(rev).toHaveAttribute('data-ticket-rev', '2')
  // The reads go through: the page shows rev 3, Bob's, and Alice's edit is made on it. The page's next read is at most
  // DISPATCH_RETRY_MAX_MS (8 s) away, the wait after a failure doubled up to it, so the test waits longer than that for it.
  reads.failing = false
  await expect(rev).toHaveAttribute('data-ticket-rev', '4', { timeout: 30_000 })
  await expect(owner).toContainText(first)
  expect(sent.map((b) => [b.set, b.expectedRev])).toEqual([
    [{ owner: second }, 2],
    [{ owner: first }, 3],
  ])
  expect((await stored(api, `${key}-1`)).owner).toBe(first)
})

test('an owner picked back behind an edit refused for a stale revision is kept while the reads after the refusal fail, and made on what the store holds', async ({ page, api }) => {
  const key = await freeKey(api)
  await made(api, key, 'Owned')
  const first = `Alice ${key}`
  const second = `Bob ${key}`
  const third = `Carol ${key}`
  await api.ok('POST', `/api/dispatch/tickets/${key}-1/edit`, { opId: opOf(key, 'o1'), set: { owner: first } })
  await api.ok('POST', '/api/dispatch/people', { opId: opOf(key, 'b'), name: second })
  const reads = await failReads(page, `${key}-1`)
  let release!: () => void
  const sent = await recordEdits(page, { n: 1, until: new Promise<void>((resolve) => (release = resolve)) })
  await page.goto(`/dispatch/${key}/1`)
  const rev = page.locator('[data-ticket-rev]')
  await expect(rev).toHaveAttribute('data-ticket-rev', '2')
  const owner = page.locator('[data-ticket-owner]')
  await expect(owner).toContainText(first)

  // The page's reads of the ticket fail from now on, and another person makes Carol the owner: the page's re-read for it fails, and it
  // still shows Alice at rev 2.
  reads.failing = true
  await api.ok('POST', `/api/dispatch/tickets/${key}-1/edit`, { opId: opOf(key, 'o2'), set: { owner: third } })
  await expect.poll(() => reads.failed).toBeGreaterThanOrEqual(2)
  await expect(rev).toHaveAttribute('data-ticket-rev', '2')
  // Bob picked: his edit held on its way, made on the rev 2 shown. Alice picked back meanwhile: her edit waits its turn behind his.
  await owner.click()
  await page.getByRole('option', { name: second, exact: true }).click()
  await expect.poll(() => sent.length).toBe(1)
  await owner.click()
  await page.getByRole('option', { name: first, exact: true }).click()
  const failedAtRelease = reads.failed
  release()

  // Bob's edit is refused stale_revision, the store at rev 3, and the page's read of the ticket after the refusal fails, and each asked
  // again, three of them at least. Alice's edit waits: made on the rev 2 shown, which holds her already, it would change nothing and not
  // be sent, and Carol would stay.
  await expect.poll(() => reads.failed - failedAtRelease).toBeGreaterThanOrEqual(6)
  expect(sent).toHaveLength(1)
  await expect(rev).toHaveAttribute('data-ticket-rev', '2')
  // The reads go through: the page shows rev 3, Carol's, and Alice's edit is made on it, at most DISPATCH_RETRY_MAX_MS (8 s) later.
  reads.failing = false
  await expect(rev).toHaveAttribute('data-ticket-rev', '4', { timeout: 30_000 })
  await expect(owner).toContainText(first)
  expect(sent.map((b) => [b.set, b.expectedRev])).toEqual([
    [{ owner: second }, 2],
    [{ owner: first }, 3],
  ])
  expect((await stored(api, `${key}-1`)).owner).toBe(first)
})

/**
 * Answers the first creation `url` names (a POST) as the server answers when its read after the commit fails: done, its receipt and no
 * entity (`entity` null). `key` is the key its receipt names, once answered.
 */
async function receiptAlone(page: Page, url: string, entity: 'ticket' | 'project'): Promise<{ key?: string }> {
  const r: { key?: string } = {}
  await page.route(url, async (route) => {
    if (route.request().method() !== 'POST' || r.key !== undefined) return route.fallback()
    const response = await route.fetch()
    const reply = (await response.json()) as { receipt: { key?: string } }
    r.key = reply.receipt.key ?? ''
    await route.fulfill({ response, json: { ...reply, [entity]: null } })
  })
  return r
}

test('a new ticket answered with its receipt alone is said by the key the receipt names, and opened from there, though the filter leaves it out', async ({ page, api }) => {
  const key = await freeKey(api)
  await made(api, key)
  const reply = await receiptAlone(page, '**/api/dispatch/tickets', 'ticket')
  // The list filtered to the tickets done: a new ticket, in the backlog, is none of its rows.
  await page.goto(`/dispatch/${key}?status=done`)
  await expect(page.locator('[data-list-empty]')).toBeVisible()

  await page.locator('[data-new-ticket-open]').click()
  const dialog = page.getByRole('dialog')
  await page.locator('[data-new-ticket-title]').fill('Out of the filter')
  await page.locator('[data-new-ticket-submit]').click()
  await expect(dialog).toHaveCount(0)
  expect(reply.key).toBe(`${key}-1`)
  // Said by its key, with Open, while the list still shows nothing; Open is its page.
  const toast = page.locator('li').filter({ hasText: `${key}-1 made` }).last()
  await expect(toast).toBeVisible()
  await expect(toast).toContainText('Out of the filter')
  await expect(page.locator('[data-list-empty]')).toBeVisible()
  await toast.getByRole('button', { name: 'Open' }).click()
  await expect(page).toHaveURL(new RegExp(`/dispatch/${key}/1$`))
  await expect(page.locator('[data-ticket-title]')).toHaveText('Out of the filter')
})

test('a new project answered with its receipt alone opens the page of the key the receipt names', async ({ page, api }) => {
  const key = await freeKey(api)
  const name = `E2E project ${key}`
  const reply = await receiptAlone(page, '**/api/dispatch/projects', 'project')
  await page.goto('/dispatch')

  await page.locator('[data-new-project-open]').click()
  const dialog = page.getByRole('dialog')
  await page.locator('[data-new-project-key]').fill(key)
  await page.locator('[data-new-project-name]').fill(name)
  await page.locator('[data-new-project-submit]').click()
  // Its page opens, from the key its receipt names.
  await expect(page).toHaveURL(new RegExp(`/dispatch/${key}$`))
  expect(reply.key).toBe(key)
  await expect(page.locator('[data-project-name]')).toHaveText(name)
  await expect(dialog).toHaveCount(0)
})

// ---------------------------------------------------------------------------
// PR-10: comments, attachments, the parent and the relations, labels and milestones, the project's description, delete and undelete.
// Then its review round 2's: a comment kept below the read's window by its answer alone, shown as another person's edit and delete
// leave it; a comment's save answered with another person's later edit, said at the next Save rather than taken as what was saved; a
// milestone's and a label's edit typed on while its save goes, kept, and an editor opened meanwhile left open; a picker offering the
// key typed whole and a title past the search's first page. Then its round 3's: a comment being edited kept, its draft with it, as
// the read's window moves past it; a comment's answer held until the window moved past it, read again as another person left it; a
// second Save of a milestone's or a label's edit while the first goes, which sends nothing.

/** A PNG of one pixel: 67 bytes an `img` can show. */
const PIXEL_PNG = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==', 'base64')

/** Picks a ticket of the project in a ticket picker, by its key typed. */
async function pickTicket(page: Page, picker: ReturnType<Page['locator']>, key: string): Promise<void> {
  await picker.click()
  await page.getByPlaceholder('A key or words of its title…').fill(key)
  await page.locator(`[data-ticket-pick="${key}"]`).click()
}

test('comments posted, edited and deleted with their numbers and their author, one seen live by a second browser', async ({ page, api, state, browser }) => {
  const key = await freeKey(api)
  await made(api, key, 'The discussion')
  await page.addInitScript(() => localStorage.setItem('conductor.displayName', 'Nate'))
  await page.goto(`/dispatch/${key}/1`)
  const comments = page.locator('[data-ticket-comments]')
  await expect(comments.locator('[data-comments-none]')).toBeVisible()

  // A second browser on the same ticket.
  other = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const second = await secondBrowser(other, state.token)
  await second.goto(`/dispatch/${key}/1`)
  await expect(second.locator('[data-comments-none]')).toBeVisible()
  await mark(second)

  // The first comment, as Markdown, previewed and posted: #1 by Nate.
  await comments.locator('[data-comment-input]').fill('First, **bold**')
  await comments.getByRole('tab', { name: 'Preview' }).click()
  await expect(comments.locator('[data-comment-draft-preview] strong')).toHaveText('bold')
  await comments.locator('[data-comment-submit]').click()
  const first = comments.locator('[data-comment="1"]')
  await expect(first.locator('[data-comment-body] strong')).toHaveText('bold')
  await expect(first.locator('[data-comment-author]')).toHaveText('Nate')
  await expect(first.locator('[data-comment-n]')).toHaveText('#1')
  // The composer is empty again, on Write.
  await expect(comments.locator('[data-comment-input]')).toHaveValue('')

  // The second browser sees it with no reload, and answers: #2 by Priya, which the first sees the same way.
  const seen = second.locator('[data-ticket-comments] [data-comment="1"]')
  await expect(seen.locator('[data-comment-body] strong')).toHaveText('bold')
  await expect(seen.locator('[data-comment-author]')).toHaveText('Nate')
  expect(await marked(second)).toBe('kept')
  await second.locator('[data-ticket-comments] [data-comment-input]').fill('A reply')
  await second.locator('[data-ticket-comments] [data-comment-input]').press('Control+Enter')
  await expect(comments.locator('[data-comment="2"] [data-comment-body]')).toHaveText('A reply')
  await expect(comments.locator('[data-comment="2"] [data-comment-author]')).toHaveText('Priya')

  // The first edited: said edited, and seen so by the second.
  await first.locator('[data-comment-edit]').click()
  await first.locator('[data-comment-input]').fill('First, edited')
  await first.locator('[data-comment-submit]').click()
  await expect(first.locator('[data-comment-body]')).toHaveText('First, edited')
  await expect(first.locator('[data-comment-edited]')).toBeVisible()
  await expect(seen.locator('[data-comment-body]')).toHaveText('First, edited')

  // The first deleted: a tombstone that keeps its number, #2 still #2.
  await first.locator('[data-comment-delete]').click()
  await first.locator('[data-comment-delete-confirm]').click()
  await expect(first).toHaveAttribute('data-comment-deleted', '')
  await expect(first).toContainText('This comment was deleted.')
  await expect(comments.locator('[data-comment="2"] [data-comment-body]')).toHaveText('A reply')
  await expect(second.locator('[data-ticket-comments] [data-comment="1"]')).toHaveAttribute('data-comment-deleted', '')
  expect(await marked(second)).toBe('kept')

  // The history says it, and the store numbers them so.
  const history = page.locator('[data-ticket-history]')
  await expect(history).toContainText('commented')
  await expect(history).toContainText('edited a comment')
  await expect(history).toContainText('deleted a comment')
  const read = await api.ok<{ ticket: { comments: Array<{ n: number; body: string; by: string; deletedAt?: string }> } }>('GET', `/api/dispatch/tickets/${key}-1`)
  expect(read.ticket.comments.map((c) => [c.n, c.by, !!c.deletedAt])).toEqual([
    [1, 'Nate', true],
    [2, 'Priya', false],
  ])
})

test("a comment's edit never replaces another person's unseen, and what is typed while its save goes stays", async ({ page, api }) => {
  const key = await freeKey(api)
  await made(api, key, 'The edited discussion')
  const ticket = `${key}-1`
  const one = await api.ok<{ comment: { id: string } }>('POST', `/api/dispatch/tickets/${ticket}/comments`, { opId: opOf(key, 'c1'), byName: 'Nate', body: 'The first' })
  await api.ok('POST', `/api/dispatch/tickets/${ticket}/comments`, { opId: opOf(key, 'c2'), byName: 'Nate', body: 'The second' })
  const sent: Array<{ expectedRev?: number; body: string }> = []
  let hold: Promise<void> | null = null
  await page.route('**/api/dispatch/tickets/*/comments/*/edit', async (route) => {
    sent.push(route.request().postDataJSON())
    if (hold) await hold
    await route.continue()
  })
  await page.goto(`/dispatch/${key}/1`)
  const first = page.locator('[data-ticket-comments] [data-comment="1"]')
  await expect(first).toHaveAttribute('data-comment-rev', '1')

  // #1 opened and written; Priya edits it meanwhile, which the page reads live behind the editor.
  await first.locator('[data-comment-edit]').click()
  await first.locator('[data-comment-input]').fill('Mine')
  await api.ok('POST', `/api/dispatch/tickets/${ticket}/comments/${one.comment.id}/edit`, { opId: opOf(key, 'c1-theirs'), byName: 'Priya', body: 'Theirs' })
  await expect(first).toHaveAttribute('data-comment-rev', '2')
  // Save sends nothing: their text is said and the draft kept.
  await first.locator('[data-comment-submit]').click()
  await expect(first.locator('[data-comment-theirs-text]')).toHaveText('Theirs')
  await expect(first.locator('[data-comment-input]')).toHaveValue('Mine')
  expect(sent).toEqual([])
  // A second Save replaces it, knowingly, on the revision said.
  await first.locator('[data-comment-submit]').click()
  await expect(first.locator('[data-comment-body]')).toHaveText('Mine')
  expect(sent.map((b) => [b.expectedRev, b.body])).toEqual([[2, 'Mine']])

  // #2 saved while more is typed: the save is done, the editor stays open with the newer text, and its own Save follows on the
  // revision the first committed.
  const second = page.locator('[data-ticket-comments] [data-comment="2"]')
  let release!: () => void
  hold = new Promise<void>((resolve) => (release = resolve))
  await second.locator('[data-comment-edit]').click()
  await second.locator('[data-comment-input]').fill('The second, edited')
  await second.locator('[data-comment-submit]').click()
  await expect.poll(() => sent.length).toBe(2)
  await second.locator('[data-comment-input]').fill('The second, edited twice')
  hold = null
  release()
  await expect(second).toHaveAttribute('data-comment-rev', '2')
  await expect(second.locator('[data-comment-input]')).toHaveValue('The second, edited twice')
  await second.locator('[data-comment-submit]').click()
  await expect(second.locator('[data-comment-body]')).toHaveText('The second, edited twice')
  expect(sent.map((b) => [b.expectedRev, b.body]).slice(1)).toEqual([
    [1, 'The second, edited'],
    [2, 'The second, edited twice'],
  ])
  const read = await api.ok<{ ticket: { comments: Array<{ n: number; body: string }> } }>('GET', `/api/dispatch/tickets/${ticket}`)
  expect(read.ticket.comments.map((c) => [c.n, c.body])).toEqual([
    [1, 'Mine'],
    [2, 'The second, edited twice'],
  ])
})

test("a comment posted here and kept below the read's window by its answer alone shows another person's edit and delete of it", async ({ page, api }) => {
  const key = await freeKey(api)
  await made(api, key, 'The long discussion')
  const ticket = `${key}-1`
  await page.goto(`/dispatch/${key}/1`)
  const comments = page.locator('[data-ticket-comments]')
  await expect(comments.locator('[data-comments-none]')).toBeVisible()

  // #1 posted here; Priya's 50 follow, so the ticket's read carries #2–#51 and #1 is below its window.
  await comments.locator('[data-comment-input]').fill('Mine, the first')
  await comments.locator('[data-comment-submit]').click()
  const first = comments.locator('[data-comment="1"]')
  await expect(first.locator('[data-comment-body]')).toHaveText('Mine, the first')
  for (let i = 2; i <= 51; i++) await api.ok('POST', `/api/dispatch/tickets/${ticket}/comments`, { opId: opOf(key, `c${i}`), byName: 'Priya', body: `Reply ${i}` })
  await expect(comments.locator('[data-comment="51"] [data-comment-body]')).toHaveText('Reply 51')
  const read = await api.ok<{ ticket: { comments: Array<{ n: number }> } }>('GET', `/api/dispatch/tickets/${ticket}`)
  expect(read.ticket.comments[0]!.n).toBe(2)
  await expect(first.locator('[data-comment-body]')).toHaveText('Mine, the first')

  // Priya edits #1, then deletes it: the page shows each, with nothing said missing and no earlier page offered.
  const id = (await first.getAttribute('data-comment-id'))!
  await api.ok('POST', `/api/dispatch/tickets/${ticket}/comments/${id}/edit`, { opId: opOf(key, 'c1-theirs'), byName: 'Priya', body: 'Theirs now' })
  await expect(first.locator('[data-comment-body]')).toHaveText('Theirs now')
  await api.ok('POST', `/api/dispatch/tickets/${ticket}/comments/${id}/delete`, { opId: opOf(key, 'c1-gone'), byName: 'Priya' })
  await expect(first).toHaveAttribute('data-comment-deleted', '')
  await expect(comments.locator('[data-comments-gap]')).toHaveCount(0)
  await expect(comments.locator('[data-comments-earlier]')).toHaveCount(0)
})

test("a comment being edited stays, its draft with it, as the read's window moves past it, and shows another person's edit at Save", async ({ page, api }) => {
  const key = await freeKey(api)
  await made(api, key, 'The long edit')
  const ticket = `${key}-1`
  const one = await api.ok<{ comment: { id: string } }>('POST', `/api/dispatch/tickets/${ticket}/comments`, { opId: opOf(key, 'c1'), byName: 'Nate', body: 'The first' })
  const sent: Array<{ expectedRev?: number; body: string }> = []
  await page.route('**/api/dispatch/tickets/*/comments/*/edit', async (route) => {
    sent.push(route.request().postDataJSON())
    await route.continue()
  })
  await page.goto(`/dispatch/${key}/1`)
  const comments = page.locator('[data-ticket-comments]')
  const first = comments.locator('[data-comment="1"]')
  await first.locator('[data-comment-edit]').click()
  await first.locator('[data-comment-input]').fill('Mine, being written')

  // Priya's 50 follow: the ticket's read carries #2–#51, and #1, being edited, stays with its draft.
  for (let i = 2; i <= 51; i++) await api.ok('POST', `/api/dispatch/tickets/${ticket}/comments`, { opId: opOf(key, `c${i}`), byName: 'Priya', body: `Reply ${i}` })
  await expect(comments.locator('[data-comment="51"] [data-comment-body]')).toHaveText('Reply 51')
  const read = await api.ok<{ ticket: { comments: Array<{ n: number }> } }>('GET', `/api/dispatch/tickets/${ticket}`)
  expect(read.ticket.comments[0]!.n).toBe(2)
  await expect(first.locator('[data-comment-input]')).toHaveValue('Mine, being written')
  await expect(comments.locator('[data-comments-gap]')).toHaveCount(0)
  await expect(comments.locator('[data-comments-earlier]')).toHaveCount(0)

  // Priya edits #1, below the window now: it is read again, and Save says hers, the draft kept; a second Save replaces it.
  await api.ok('POST', `/api/dispatch/tickets/${ticket}/comments/${one.comment.id}/edit`, { opId: opOf(key, 'c1-theirs'), byName: 'Priya', body: 'Theirs, below' })
  await expect(first).toHaveAttribute('data-comment-rev', '2')
  await expect(first.locator('[data-comment-input]')).toHaveValue('Mine, being written')
  await first.locator('[data-comment-submit]').click()
  await expect(first.locator('[data-comment-theirs-text]')).toHaveText('Theirs, below')
  expect(sent.length).toBe(0)
  await first.locator('[data-comment-submit]').click()
  await expect(first.locator('[data-comment-body]')).toHaveText('Mine, being written')
  expect(sent.map((b) => [b.expectedRev, b.body])).toEqual([[2, 'Mine, being written']])
})

test("a comment's answer held until the read's window moved past it shows it as another person's edit left it, read again", async ({ page, api }) => {
  const key = await freeKey(api)
  await made(api, key, 'The slow comment')
  const ticket = `${key}-1`
  let release!: () => void
  const held = new Promise<void>((resolve) => (release = resolve))
  let id = ''
  await page.route('**/api/dispatch/tickets/*/comments', async (route) => {
    if (route.request().method() !== 'POST') return route.continue()
    // #1 commits; its answer is held while Priya edits it and posts 50 more.
    const res = await route.fetch()
    const reply = await res.json()
    id = reply.comment.id
    await held
    await route.fulfill({ response: res, json: reply })
  })
  await page.goto(`/dispatch/${key}/1`)
  const comments = page.locator('[data-ticket-comments]')
  await expect(comments.locator('[data-comments-none]')).toBeVisible()
  await comments.locator('[data-comment-input]').fill('Mine, the first')
  await comments.locator('[data-comment-submit]').click()
  await expect.poll(() => id).not.toBe('')

  // Priya's edit shows by the ticket's read; her 50 move its window past #1, which goes with it, the answer still held.
  const first = comments.locator('[data-comment="1"]')
  await api.ok('POST', `/api/dispatch/tickets/${ticket}/comments/${id}/edit`, { opId: opOf(key, 'c1-theirs'), byName: 'Priya', body: 'Theirs now' })
  await expect(first.locator('[data-comment-body]')).toHaveText('Theirs now')
  for (let i = 2; i <= 51; i++) await api.ok('POST', `/api/dispatch/tickets/${ticket}/comments`, { opId: opOf(key, `c${i}`), byName: 'Priya', body: `Reply ${i}` })
  await expect(comments.locator('[data-comment="51"] [data-comment-body]')).toHaveText('Reply 51')
  await expect(first).toHaveCount(0)

  // The answer, made before her edit, comes: #1 is read again, and shown as she left it.
  release()
  await expect(comments.locator('[data-comment-input]')).toHaveValue('')
  await expect(first).toHaveAttribute('data-comment-rev', '2')
  await expect(first.locator('[data-comment-body]')).toHaveText('Theirs now')
  await expect(comments.locator('[data-comments-gap]')).toHaveCount(0)
})

test("a comment's save answered with another person's later edit says theirs at the next Save, never taking it for what was saved", async ({ page, api }) => {
  const key = await freeKey(api)
  await made(api, key, 'The raced discussion')
  const ticket = `${key}-1`
  const one = await api.ok<{ comment: { id: string } }>('POST', `/api/dispatch/tickets/${ticket}/comments`, { opId: opOf(key, 'c1'), byName: 'Nate', body: 'The first' })
  const sent: Array<{ expectedRev?: number; body: string }> = []
  let release!: () => void
  const held = new Promise<void>((resolve) => (release = resolve))
  await page.route('**/api/dispatch/tickets/*/comments/*/edit', async (route) => {
    sent.push(route.request().postDataJSON())
    if (sent.length > 1) return route.continue()
    // The save commits; Priya's edit follows it before its answer is read, so the answer carries hers, as one read after the commit can.
    const res = await route.fetch()
    const reply = await res.json()
    await held
    await api.ok('POST', `/api/dispatch/tickets/${ticket}/comments/${one.comment.id}/edit`, { opId: opOf(key, 'c1-theirs'), byName: 'Priya', body: 'Theirs, after' })
    const now = await api.ok<{ ticket: { comments: Array<{ id: string }> } }>('GET', `/api/dispatch/tickets/${ticket}`)
    reply.comment = now.ticket.comments.find((c) => c.id === one.comment.id)
    await route.fulfill({ response: res, json: reply })
  })
  await page.goto(`/dispatch/${key}/1`)
  const first = page.locator('[data-ticket-comments] [data-comment="1"]')
  await expect(first).toHaveAttribute('data-comment-rev', '1')

  // Saved while more is typed: the answer carries Priya's revision, and the editor stays open with the newer text.
  await first.locator('[data-comment-edit]').click()
  await first.locator('[data-comment-input]').fill('Mine')
  await first.locator('[data-comment-submit]').click()
  await expect.poll(() => sent.length).toBe(1)
  await first.locator('[data-comment-input]').fill('Mine, and more')
  release()
  await expect(first).toHaveAttribute('data-comment-rev', '3')
  await expect(first.locator('[data-comment-input]')).toHaveValue('Mine, and more')
  // Save sends nothing: hers is said, the draft kept.
  await first.locator('[data-comment-submit]').click()
  await expect(first.locator('[data-comment-theirs-text]')).toHaveText('Theirs, after')
  await expect(first.locator('[data-comment-input]')).toHaveValue('Mine, and more')
  expect(sent.length).toBe(1)
  // A second Save replaces it, knowingly, on her revision.
  await first.locator('[data-comment-submit]').click()
  await expect(first.locator('[data-comment-body]')).toHaveText('Mine, and more')
  expect(sent.map((b) => [b.expectedRev, b.body])).toEqual([
    [1, 'Mine'],
    [3, 'Mine, and more'],
  ])
})

test('an attachment uploaded and downloaded byte for byte, an image shown through an object URL, an SVG offered as a download only', async ({ page, api }) => {
  const key = await freeKey(api)
  await made(api, key, 'The files')
  await page.goto(`/dispatch/${key}/1`)
  const files = page.locator('[data-ticket-attachments]')
  await expect(files).toHaveAttribute('data-count', '0')

  // Bytes of every value, so a download that changed one would show.
  const bytes = Buffer.from(Array.from({ length: 70_000 }, (_, i) => (i * 31 + 7) % 256))
  await files.locator('[data-attachment-input]').setInputFiles([
    { name: 'data.bin', mimeType: 'application/octet-stream', buffer: bytes },
    { name: 'dot.png', mimeType: 'image/png', buffer: PIXEL_PNG },
    { name: 'drawing.svg', mimeType: 'image/svg+xml', buffer: Buffer.from('<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>') },
  ])
  await expect(files).toHaveAttribute('data-count', '3')
  await expect(files.locator('[data-upload]')).toHaveCount(0)

  // The download is the bytes uploaded, every one.
  const bin = files.locator('[data-attachment="data.bin"]')
  const [download] = await Promise.all([page.waitForEvent('download'), bin.locator('[data-attachment-download]').click()])
  expect(download.suggestedFilename()).toBe('data.bin')
  const fs = await import('node:fs/promises')
  expect(Buffer.compare(await fs.readFile((await download.path())!), bytes)).toBe(0)

  // The image is shown from an object URL of what the page fetched with the token, never a source at the API.
  const img = files.locator('[data-attachment="dot.png"] [data-attachment-image]')
  await expect(img).toHaveAttribute('src', /^blob:/)
  await expect.poll(() => img.evaluate((el) => (el as HTMLImageElement).naturalWidth)).toBe(1)
  // The SVG is no image here: offered as a download, its script never run.
  const svg = files.locator('[data-attachment="drawing.svg"]')
  await expect(svg.locator('[data-attachment-image]')).toHaveCount(0)
  await expect(svg.locator('[data-attachment-insert]')).toHaveCount(0)
  expect(await page.locator('img[src^="/api/"], img[src*="/api/dispatch/"]').count()).toBe(0)

  // The image named in the description is drawn from its content too, and an SVG named so keeps its words.
  const pngId = await files.locator('[data-attachment="dot.png"]').getAttribute('data-attachment-id')
  const svgId = await svg.getAttribute('data-attachment-id')
  const description = page.locator('[data-ticket-markdown="description"]')
  await description.locator('[data-markdown-empty]').click()
  await description.locator('[data-markdown-input]').fill(`![the dot](attachment:${pngId})\n\n![the drawing](attachment:${svgId})`)
  await description.locator('[data-markdown-save]').click()
  await expect(description.locator('[data-markdown-view] img[data-md-attachment]')).toHaveAttribute('src', /^blob:/)
  await expect(description.locator('[data-markdown-view] .md-image-held')).toHaveText('the drawing')

  // An image put into a comment from its row, shown in the comment the same way.
  await files.locator('[data-attachment="dot.png"] [data-attachment-insert]').click()
  await expect(page.locator('[data-ticket-comments] [data-comment-input]')).toHaveValue(`![dot.png](attachment:${pngId})`)
  await page.locator('[data-ticket-comments] [data-comment-submit]').click()
  await expect(page.locator('[data-ticket-comments] [data-comment="1"] img[data-md-attachment]')).toHaveAttribute('src', /^blob:/)

  // Removed: gone from the list, the others kept.
  await bin.locator('[data-attachment-remove]').click()
  await bin.locator('[data-attachment-remove-confirm]').click()
  await expect(files).toHaveAttribute('data-count', '2')
  await expect(files.locator('[data-attachment="data.bin"]')).toHaveCount(0)
  const stored = await api.ok<{ ticket: { attachments: Array<{ name: string }> } }>('GET', `/api/dispatch/tickets/${key}-1`)
  expect(stored.ticket.attachments.map((a) => a.name).sort()).toEqual(['dot.png', 'drawing.svg'])
})

test('a parent set and shown on both pages, a blocks relation both ways, and a second browser that sees Blocked go and a child leave', async ({ page, api, state, browser }) => {
  const key = await freeKey(api)
  await made(api, key, 'The epic')
  await api.ok('POST', '/api/dispatch/tickets', { opId: opOf(key, 't2'), project: key, title: 'The child' })
  await api.ok('POST', '/api/dispatch/tickets', { opId: opOf(key, 't3'), project: key, title: 'The blocker' })
  await api.ok('POST', '/api/dispatch/tickets', { opId: opOf(key, 't4'), project: key, title: 'Another epic' })

  // The child's parent, picked by key.
  await page.goto(`/dispatch/${key}/2`)
  await pickTicket(page, page.locator('[data-parent-picker]'), `${key}-1`)
  await expect(page.locator('[data-ticket-parent]')).toHaveAttribute('data-parent', `${key}-1`)
  await expect(page.locator('[data-parent-link]')).toContainText('The epic')
  await expect(page.locator('[data-parent-status]')).toHaveAttribute('data-parent-status', 'backlog')

  // "Is blocked by" the blocker, made from the blocker's side: shown here under Blocked by, and Blocked said.
  await page.locator('[data-relation-kind]').click()
  await page.getByRole('option', { name: 'Is blocked by' }).click()
  await pickTicket(page, page.locator('[data-relation-to]'), `${key}-3`)
  await expect(page.locator(`[data-relations="blocked-by"] [data-relation="${key}-3"]`)).toBeVisible()
  await expect(page.locator('[data-ticket-blocked]')).toContainText(`${key}-3`)
  await expect(page.locator('[data-ticket-words]')).toContainText('blocked')

  // The blocker's page says it the other way; the epic's lists its child.
  await page.goto(`/dispatch/${key}/3`)
  await expect(page.locator(`[data-relations="blocks"] [data-relation="${key}-2"]`)).toBeVisible()
  await expect(page.locator('[data-ticket-blocked]')).toHaveCount(0)
  await page.goto(`/dispatch/${key}/1`)
  await expect(page.locator(`[data-ticket-children] [data-child="${key}-2"]`)).toContainText('The child')
  await expect(page.locator('[data-children-count]')).toHaveText('(1)')

  // A second browser on the blocked child: the first cancels the blocker, and Blocked goes with no reload.
  other = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const second = await secondBrowser(other, state.token)
  await second.goto(`/dispatch/${key}/2`)
  await expect(second.locator('[data-ticket-blocked]')).toBeVisible()
  await mark(second)
  await page.goto(`/dispatch/${key}/3`)
  await page.locator('[data-ticket-status]').click()
  await page.getByRole('option', { name: 'Cancelled' }).click()
  await expect(page.locator('[data-ticket-status]')).toContainText('Cancelled')
  await expect(second.locator('[data-ticket-blocked]')).toHaveCount(0)
  await expect(second.locator(`[data-relations="blocked-by"] [data-relation="${key}-3"] [data-relation-state]`)).toHaveText('Cancelled')
  await expect(second.locator('[data-ticket-words]')).not.toContainText('blocked')
  expect(await marked(second)).toBe('kept')

  // The second on the epic: the first reparents the child under the other epic, and it leaves the epic's children with no reload.
  await second.goto(`/dispatch/${key}/1`)
  await expect(second.locator(`[data-ticket-children] [data-child="${key}-2"]`)).toBeVisible()
  await mark(second)
  await page.goto(`/dispatch/${key}/2`)
  await pickTicket(page, page.locator('[data-parent-picker]'), `${key}-4`)
  await expect(page.locator('[data-ticket-parent]')).toHaveAttribute('data-parent', `${key}-4`)
  await expect(second.locator(`[data-ticket-children] [data-child="${key}-2"]`)).toHaveCount(0)
  await expect(second.locator('[data-ticket-children]')).toHaveAttribute('data-count', '0')
  expect(await marked(second)).toBe('kept')

  // A blocks that would close a cycle is refused in the store's words: the blocker blocks the child, the child blocks the other epic,
  // and the other epic blocking the blocker would close it.
  await page.locator('[data-relation-kind]').click()
  await page.getByRole('option', { name: 'Blocks', exact: true }).click()
  await pickTicket(page, page.locator('[data-relation-to]'), `${key}-4`)
  await expect(page.locator(`[data-relations="blocks"] [data-relation="${key}-4"]`)).toBeVisible()
  // A relation of another kind with the same ticket is offered and made; one of the same kind is not offered.
  await page.locator('[data-relation-to]').click()
  await page.getByPlaceholder('A key or words of its title…').fill(`${key}-`)
  await expect(page.locator(`[data-ticket-pick="${key}-1"]`)).toBeVisible()
  await expect(page.locator(`[data-ticket-pick="${key}-3"]`)).toBeVisible()
  await expect(page.locator(`[data-ticket-pick="${key}-4"]`)).toHaveCount(0)
  await page.keyboard.press('Escape')
  await page.locator('[data-relation-kind]').click()
  await page.getByRole('option', { name: 'Relates to' }).click()
  await pickTicket(page, page.locator('[data-relation-to]'), `${key}-4`)
  await expect(page.locator(`[data-relations="relates"] [data-relation="${key}-4"]`)).toBeVisible()
  await expect(page.locator(`[data-relations="blocks"] [data-relation="${key}-4"]`)).toBeVisible()
  await page.goto(`/dispatch/${key}/4`)
  await expect(page.locator(`[data-relations="blocked-by"] [data-relation="${key}-2"]`)).toBeVisible()
  await page.locator('[data-relation-kind]').click()
  await page.getByRole('option', { name: 'Blocks', exact: true }).click()
  await pickTicket(page, page.locator('[data-relation-to]'), `${key}-3`)
  await expect(page.getByText(`${key}-4: not changed`, { exact: true })).toBeVisible()
  await expect(page.locator('[data-relations="blocks"]')).toHaveAttribute('data-count', '0')
})

test('a deleted parent is said on its child, inactive, its key opening its page, where Undelete brings it back', async ({ page, api }) => {
  const key = await freeKey(api)
  await made(api, key, 'The parent')
  await api.ok('POST', '/api/dispatch/tickets', { opId: opOf(key, 't2'), project: key, title: 'The child', parent: `${key}-1` })

  await page.goto(`/dispatch/${key}/1`)
  await page.locator('[data-ticket-delete]').click()
  await page.locator('[data-ticket-delete-confirm]').click()
  await expect(page.locator('[data-ticket-deleted]')).toBeVisible()
  await expect(page.locator('[data-ticket-words]')).toHaveText('Deleted')

  // The child keeps it as its parent, said deleted.
  await page.goto(`/dispatch/${key}/2`)
  const parent = page.locator('[data-ticket-parent]')
  await expect(parent).toHaveAttribute('data-parent', `${key}-1`)
  await expect(parent).toHaveAttribute('data-parent-deleted', '')
  await expect(page.locator('[data-parent-link]')).toContainText('(deleted)')

  // Its key opens the deleted ticket's page, where Undelete is; the child's parent is live again.
  await page.locator('[data-parent-link]').click()
  await expect(page).toHaveURL(new RegExp(`/dispatch/${key}/1$`))
  await page.locator('[data-ticket-deleted]').getByRole('button', { name: 'Undelete' }).click()
  await expect(page.locator('[data-ticket-deleted]')).toHaveCount(0)
  await expect(page.locator(`[data-ticket-children] [data-child="${key}-2"]`)).toBeVisible()
  await page.goto(`/dispatch/${key}/2`)
  await expect(page.locator('[data-ticket-parent]')).not.toHaveAttribute('data-parent-deleted', '')
  await expect(page.locator('[data-parent-link]')).not.toContainText('(deleted)')
})

test("a project's description, labels and milestones made on its page, picked on a ticket's, and a label's delete seen there live", async ({ page, api }) => {
  const key = await freeKey(api)
  await made(api, key, 'The labelled')
  await page.goto(`/dispatch/${key}`)

  // The description, written in place and shown rendered above the tickets.
  const about = page.locator('[data-project-about]')
  await about.locator('[data-markdown-empty]').click()
  await about.locator('[data-markdown-input]').fill('## Goals\n\nShip the **tracker**.')
  await about.locator('[data-markdown-save]').click()
  await expect(about.locator('[data-markdown-view] h2')).toHaveText('Goals')
  expect((await api.ok<{ project: { description: string } }>('GET', `/api/dispatch/projects/${key}`)).project.description).toBe('## Goals\n\nShip the **tracker**.')
  // Folded, it stays folded on this browser.
  await page.locator('[data-project-about-toggle]').click()
  await expect(about).toHaveCount(0)
  await page.reload()
  await expect(page.locator('[data-project-name]')).toBeVisible()
  await expect(page.locator('[data-project-about]')).toHaveCount(0)
  await page.locator('[data-project-about-toggle]').click()
  await expect(page.locator('[data-project-about] [data-markdown-view] strong')).toHaveText('tracker')

  // Two labels and a milestone, made from the page.
  await page.locator('[data-project-settings-open]').click()
  const settings = page.locator('[data-project-settings]')
  await settings.locator('[data-label-new-name]').fill('api')
  await settings.locator('[data-label-new-submit]').click()
  await expect(settings.locator('[data-project-label="api"]')).toHaveAttribute('data-label-color', 'blue')
  await settings.locator('[data-label-new-color]').click()
  await page.getByRole('option', { name: 'Red' }).click()
  await settings.locator('[data-label-new-name]').fill('bug')
  await settings.locator('[data-label-new-submit]').click()
  await expect(settings.locator('[data-project-label="bug"]')).toHaveAttribute('data-label-color', 'red')
  // Renamed and recoloured.
  await settings.locator('[data-project-label="api"] [data-label-rename]').click()
  await settings.locator('[data-label-rename-input]').fill('backend')
  await settings.locator('[data-label-rename-save]').click()
  await expect(settings.locator('[data-project-label="backend"]')).toBeVisible()
  await settings.locator('[data-project-label="backend"] [data-label-color-pick]').click()
  await page.getByRole('option', { name: 'Green' }).click()
  await expect(settings.locator('[data-project-label="backend"]')).toHaveAttribute('data-label-color', 'green')
  await settings.locator('[data-milestone-new-name]').fill('v1')
  await settings.locator('[data-milestone-new-target]').fill('2026-12-01')
  await settings.locator('[data-milestone-new-submit]').click()
  const milestone = settings.locator('[data-project-milestone="v1"]')
  await expect(milestone.locator('[data-milestone-target]')).toHaveValue('2026-12-01')
  await milestone.locator('[data-milestone-state-toggle]').click()
  await expect(milestone).toHaveAttribute('data-milestone-state', 'closed')
  await milestone.locator('[data-milestone-state-toggle]').click()
  await expect(milestone).toHaveAttribute('data-milestone-state', 'open')
  // Its name edited here while another person writes its description: theirs is kept, as the name is not theirs to change.
  const v1 = (await api.ok<{ milestones: Array<{ id: string; name: string }> }>('GET', `/api/dispatch/projects/${key}`)).milestones.find((m) => m.name === 'v1')!
  const rev = Number(await milestone.getAttribute('data-milestone-rev'))
  await milestone.locator('[data-milestone-edit]').click()
  await milestone.locator('[data-milestone-name-input]').fill('v1.0')
  await api.ok('POST', `/api/dispatch/projects/${key}/milestones/${v1.id}/edit`, { opId: opOf(key, 'v1-theirs'), byName: 'Priya', description: 'Theirs' })
  await expect(milestone).toHaveAttribute('data-milestone-rev', String(rev + 1))
  await milestone.locator('[data-milestone-save]').click()
  const renamed = settings.locator('[data-project-milestone="v1.0"]')
  await expect(renamed).toBeVisible()
  await expect(renamed).toContainText('Theirs')
  // The other way: their name, given while this one is renamed, is said, and a second Save replaces it.
  await renamed.locator('[data-milestone-edit]').click()
  await renamed.locator('[data-milestone-name-input]').fill('v1.1')
  await api.ok('POST', `/api/dispatch/projects/${key}/milestones/${v1.id}/edit`, { opId: opOf(key, 'v1-theirs-name'), byName: 'Priya', name: 'v1 final' })
  const theirs = settings.locator('[data-project-milestone="v1 final"]')
  await expect(theirs).toBeVisible()
  await theirs.locator('[data-milestone-save]').click()
  await expect(theirs.locator('[data-milestone-theirs-name]')).toContainText('v1 final')
  await expect(theirs.locator('[data-milestone-name-input]')).toHaveValue('v1.1')
  await theirs.locator('[data-milestone-save]').click()
  await expect(settings.locator('[data-project-milestone="v1.1"]')).toContainText('Theirs')
  const stored = (await api.ok<{ milestones: Array<{ id: string; name: string; description: string }> }>('GET', `/api/dispatch/projects/${key}`)).milestones.find((m) => m.id === v1.id)!
  expect([stored.name, stored.description]).toEqual(['v1.1', 'Theirs'])
  await page.keyboard.press('Escape')

  // Picked on the ticket's page.
  await page.goto(`/dispatch/${key}/1`)
  await page.locator('[data-ticket-labels]').click()
  await page.getByRole('option', { name: 'backend' }).click()
  await page.getByRole('option', { name: 'bug' }).click()
  await expect(page.locator('[data-ticket-labels] [data-label]')).toHaveCount(2)
  // Escape closes the menu, an item picked by a click holding the focus: the page's own Escape leaves it to the menu, and stays.
  await page.keyboard.press('Escape')
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await expect(page).toHaveURL(new RegExp(`/dispatch/${key}/1$`))
  await page.locator('[data-ticket-milestone]').click()
  await page.getByRole('option', { name: 'v1.1' }).click()
  await expect(page.locator('[data-ticket-milestone]')).toContainText('v1.1')
  await expect.poll(async () => (await api.ok<{ ticket: { labels: string[]; milestoneId: string } }>('GET', `/api/dispatch/tickets/${key}-1`)).ticket.labels.length).toBe(2)
  await mark(page)

  // A label deleted elsewhere leaves the ticket, and the open page sees it with no reload; the milestone the same.
  const read = await api.ok<{ labels: Array<{ id: string; name: string }>; milestones: Array<{ id: string }> }>('GET', `/api/dispatch/projects/${key}`)
  const bug = read.labels.find((l) => l.name === 'bug')!
  await api.ok('POST', `/api/dispatch/projects/${key}/labels/${bug.id}/delete`, { opId: opOf(key, 'del-bug'), byName: 'Priya' })
  await expect(page.locator('[data-ticket-labels] [data-label]')).toHaveCount(1)
  await expect(page.locator('[data-ticket-labels] [data-label="backend"]')).toBeVisible()
  await api.ok('POST', `/api/dispatch/projects/${key}/milestones/${read.milestones[0]!.id}/delete`, { opId: opOf(key, 'del-v1'), byName: 'Priya' })
  await expect(page.locator('[data-ticket-milestone]')).not.toContainText('v1.1')
  expect(await marked(page)).toBe('kept')
  await expect(page.locator('[data-ticket-history]')).toContainText('removed the label')
})

test("a milestone's and a label's edit typed on while its save goes keeps the newer text, and an editor opened meanwhile stays open", async ({ page, api }) => {
  const key = await freeKey(api)
  await made(api, key)
  await api.ok('POST', `/api/dispatch/projects/${key}/milestones`, { opId: opOf(key, 'm1'), name: 'v1' })
  await api.ok('POST', `/api/dispatch/projects/${key}/milestones`, { opId: opOf(key, 'm2'), name: 'v2' })
  await api.ok('POST', `/api/dispatch/projects/${key}/labels`, { opId: opOf(key, 'l1'), name: 'api', color: 'blue' })
  const milestoneEdits: Array<{ expectedRev?: number; name?: string; description?: string }> = []
  const labelEdits: Array<{ expectedRev?: number; name?: string }> = []
  let hold: Promise<void> | null = null
  await page.route('**/api/dispatch/projects/*/milestones/*/edit', async (route) => {
    milestoneEdits.push(route.request().postDataJSON())
    if (hold) await hold
    await route.continue()
  })
  await page.route('**/api/dispatch/projects/*/labels/*/edit', async (route) => {
    labelEdits.push(route.request().postDataJSON())
    if (hold) await hold
    await route.continue()
  })
  await page.goto(`/dispatch/${key}`)
  await page.locator('[data-project-settings-open]').click()
  const settings = page.locator('[data-project-settings]')

  // v1's description saved while more is typed: the editor stays open with the newer text, and its own Save follows on the revision
  // the first committed.
  const one = settings.locator('[data-project-milestone="v1"]')
  const rev = Number(await one.getAttribute('data-milestone-rev'))
  let release!: () => void
  hold = new Promise<void>((resolve) => (release = resolve))
  await one.locator('[data-milestone-edit]').click()
  await one.locator('[data-milestone-description-input]').fill('The first cut')
  await one.locator('[data-milestone-save]').click()
  await expect.poll(() => milestoneEdits.length).toBe(1)
  await one.locator('[data-milestone-description-input]').fill('The first cut, and more')
  hold = null
  release()
  await expect(one).toHaveAttribute('data-milestone-rev', String(rev + 1))
  await expect(one.locator('[data-milestone-description-input]')).toHaveValue('The first cut, and more')
  await expect(one.locator('[data-milestone-theirs]')).toHaveCount(0)
  await one.locator('[data-milestone-save]').click()
  await expect(one.locator('[data-milestone-description-input]')).toHaveCount(0)
  await expect(one).toContainText('The first cut, and more')
  expect(milestoneEdits.map((b) => [b.expectedRev, b.description])).toEqual([
    [rev, 'The first cut'],
    [rev + 1, 'The first cut, and more'],
  ])

  // v1 renamed, and v2's editor opened while that save goes: its answer leaves v2's editor open, its draft kept.
  hold = new Promise<void>((resolve) => (release = resolve))
  await one.locator('[data-milestone-edit]').click()
  await one.locator('[data-milestone-name-input]').fill('v1.0')
  await one.locator('[data-milestone-save]').click()
  await expect.poll(() => milestoneEdits.length).toBe(3)
  const two = settings.locator('[data-project-milestone="v2"]')
  await two.locator('[data-milestone-edit]').click()
  await two.locator('[data-milestone-name-input]').fill('v2.0')
  hold = null
  release()
  await expect(settings.locator('[data-project-milestone="v1.0"]')).toBeVisible()
  await expect(two.locator('[data-milestone-name-input]')).toHaveValue('v2.0')
  await two.locator('[data-milestone-save]').click()
  await expect(settings.locator('[data-project-milestone="v2.0"]')).toBeVisible()

  // The label renamed while more is typed: the rename stays open with the newer name, and its own Save follows.
  const label = settings.locator('[data-project-label="api"]')
  const labelRev = Number(await label.getAttribute('data-label-rev'))
  hold = new Promise<void>((resolve) => (release = resolve))
  await label.locator('[data-label-rename]').click()
  await label.locator('[data-label-rename-input]').fill('server')
  await label.locator('[data-label-rename-save]').click()
  await expect.poll(() => labelEdits.length).toBe(1)
  await label.locator('[data-label-rename-input]').fill('server side')
  hold = null
  release()
  const renamed = settings.locator('[data-project-label="server"]')
  await expect(renamed).toHaveAttribute('data-label-rev', String(labelRev + 1))
  await expect(renamed.locator('[data-label-rename-input]')).toHaveValue('server side')
  await expect(renamed.locator('[data-label-theirs]')).toHaveCount(0)
  await renamed.locator('[data-label-rename-save]').click()
  await expect(settings.locator('[data-project-label="server side"] [data-label-name]')).toBeVisible()
  expect(labelEdits.map((b) => [b.expectedRev, b.name])).toEqual([
    [labelRev, 'server'],
    [labelRev + 1, 'server side'],
  ])
})

test("a milestone's or a label's second Save while the first goes sends nothing, and what was typed back is saved after it", async ({ page, api }) => {
  const key = await freeKey(api)
  await made(api, key)
  await api.ok('POST', `/api/dispatch/projects/${key}/milestones`, { opId: opOf(key, 'm1'), name: 'v1', description: 'A' })
  await api.ok('POST', `/api/dispatch/projects/${key}/labels`, { opId: opOf(key, 'l1'), name: 'api', color: 'blue' })
  const milestoneEdits: Array<{ expectedRev?: number; name?: string; description?: string }> = []
  const labelEdits: Array<{ expectedRev?: number; name?: string }> = []
  let hold: Promise<void> | null = null
  await page.route('**/api/dispatch/projects/*/milestones/*/edit', async (route) => {
    milestoneEdits.push(route.request().postDataJSON())
    if (hold) await hold
    await route.continue()
  })
  await page.route('**/api/dispatch/projects/*/labels/*/edit', async (route) => {
    labelEdits.push(route.request().postDataJSON())
    if (hold) await hold
    await route.continue()
  })
  await page.goto(`/dispatch/${key}`)
  await page.locator('[data-project-settings-open]').click()
  const settings = page.locator('[data-project-settings]')

  // v1's description A saved as B and held; typed back to A, its Save is off and Enter sends nothing.
  const one = settings.locator('[data-project-milestone="v1"]')
  const rev = Number(await one.getAttribute('data-milestone-rev'))
  let release!: () => void
  hold = new Promise<void>((resolve) => (release = resolve))
  await one.locator('[data-milestone-edit]').click()
  await one.locator('[data-milestone-description-input]').fill('B')
  await one.locator('[data-milestone-save]').click()
  await expect.poll(() => milestoneEdits.length).toBe(1)
  await one.locator('[data-milestone-description-input]').fill('A')
  await expect(one.locator('[data-milestone-save]')).toBeDisabled()
  await one.locator('[data-milestone-name-input]').press('Enter')
  await expect(one.locator('[data-milestone-description-input]')).toHaveValue('A')
  hold = null
  release()
  // B committed; the editor stays with A, made against B, and its Save sends A.
  await expect(one).toHaveAttribute('data-milestone-rev', String(rev + 1))
  await expect(one.locator('[data-milestone-description-input]')).toHaveValue('A')
  await one.locator('[data-milestone-save]').click()
  await expect(one.locator('[data-milestone-description-input]')).toHaveCount(0)
  await expect(one).toHaveAttribute('data-milestone-rev', String(rev + 2))
  expect(milestoneEdits.map((b) => [b.expectedRev, b.description])).toEqual([
    [rev, 'B'],
    [rev + 1, 'A'],
  ])

  // The label renamed server and held; typed back to api, its Save is off and Enter sends nothing; then api is saved after it.
  const label = settings.locator('[data-project-label="api"]')
  const labelRev = Number(await label.getAttribute('data-label-rev'))
  hold = new Promise<void>((resolve) => (release = resolve))
  await label.locator('[data-label-rename]').click()
  await label.locator('[data-label-rename-input]').fill('server')
  await label.locator('[data-label-rename-save]').click()
  await expect.poll(() => labelEdits.length).toBe(1)
  await label.locator('[data-label-rename-input]').fill('api')
  await expect(label.locator('[data-label-rename-save]')).toBeDisabled()
  await label.locator('[data-label-rename-input]').press('Enter')
  hold = null
  release()
  const renamed = settings.locator('[data-project-label="server"]')
  await expect(renamed).toHaveAttribute('data-label-rev', String(labelRev + 1))
  await expect(renamed.locator('[data-label-rename-input]')).toHaveValue('api')
  await renamed.locator('[data-label-rename-save]').click()
  await expect(settings.locator('[data-project-label="api"] [data-label-name]')).toBeVisible()
  expect(labelEdits.map((b) => [b.expectedRev, b.name])).toEqual([
    [labelRev, 'server'],
    [labelRev + 1, 'api'],
  ])
})

test("a picker offers the key typed whole and a title past the search's first page, though 50 older tickets only mention them", async ({ page, api }) => {
  const key = await freeKey(api)
  await made(api, key, 'The page')
  // #2–#51 mention #53 and the needle in their descriptions alone: the oldest updated, they are the search's first page.
  for (let i = 2; i <= 51; i++) await api.ok('POST', '/api/dispatch/tickets', { opId: opOf(key, `t${i}`), project: key, title: `Other ${i}`, description: `See ${key}-53, the needle` })
  await api.ok('POST', '/api/dispatch/tickets', { opId: opOf(key, 't52'), project: key, title: 'The needle' })
  await api.ok('POST', '/api/dispatch/tickets', { opId: opOf(key, 't53'), project: key, title: 'The target' })
  await page.goto(`/dispatch/${key}/1`)

  // The key typed whole is offered, and picked.
  await pickTicket(page, page.locator('[data-relation-to]'), `${key}-53`)
  await expect(page.locator(`[data-relations="blocks"] [data-relation="${key}-53"]`)).toBeVisible()
  // A word of a title on the search's second page is offered, and picked.
  await page.locator('[data-relation-to]').click()
  await page.getByPlaceholder('A key or words of its title…').fill('needle')
  await page.locator(`[data-ticket-pick="${key}-52"]`).click()
  await expect(page.locator(`[data-relations="blocks"] [data-relation="${key}-52"]`)).toBeVisible()
})

test('the ticket page is one column at a phone width, its comments, files and relations within it', async ({ page, api }) => {
  const key = await freeKey(api)
  await made(api, key, 'On a phone')
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto(`/dispatch/${key}/1`)
  await expect(page.locator('[data-ticket-comments]')).toBeVisible()
  for (const sel of ['[data-ticket-properties]', '[data-ticket-relations]', '[data-ticket-attachments]', '[data-ticket-comments]']) {
    const box = await page.locator(sel).boundingBox()
    expect(box, sel).not.toBeNull()
    expect(box!.x + box!.width, sel).toBeLessThanOrEqual(390)
  }
  expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBe(0)
})
