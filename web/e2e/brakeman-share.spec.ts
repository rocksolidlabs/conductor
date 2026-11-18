import type { Locator, Page } from '@playwright/test'
import { brakemanServer, type BrakemanServer } from './brakeman-server'
import { walk, worst } from './contrast'
import { atRest, budget, expect, test, useTheme } from './fixtures'
import { noSideways, tapTarget } from './layout'

// Share a brief with a crew's people (issue #1012), on a server of its own with the Brakeman stub. Two crews with people are
// launched: one from a ticket (its run carries the ticket), one from the crew alone. A brief asked about both runs names each crew's
// people; Copy for them puts their names on the first line of the Markdown on the clipboard, every name as text (one is a link and a
// mention as typed); Comment on the run's ticket posts the same text under the person's name, read back through the API; a crew
// whose run has no ticket says so. A brief about no run says it goes with no names and still copies. At 390 x 844 the head's two
// actions stay hidden and Share is an icon a thumb can tap, nothing goes sideways; in both themes every text of the dialog passes
// its floor, checked again after the theme's redraw.
test.describe.configure({ mode: 'serial' })

const PROJECT = 'BKS'
const KEY = `${PROJECT}-1`
const SHARE_WHAT =
  "Conductor sends nothing and notifies no one: you copy the brief, or comment it on a ticket, with their names on its first line. No one gains a way to read the brief here. A ticket's comment is read by whoever reads the ticket, and by the agents of a run launched from it later."
/** A person's name that is a link and a mention as typed: it must reach the text as its own characters, never as markup. */
const HOSTILE = 'Bo [x](https://evil.example/a) @team'
/**
 * The first line Copy for them writes for the api crew: a name a tracker would link (a mention, an address) as a code span, in which
 * nothing is linked.
 */
const API_LINE = "For e2e share api's people: Ada (architecture owner), `Bo [x](https://evil.example/a) @team` (security reviewer)"
/** A name with a variation selector, which the first line keeps as the chip shows it. */
const HEART = 'Ana \u2764\ufe0f'
const TITLE = 'Two crews worked on it'

/** Each wait's cap: a page's first draw of a brief, and every other step. */
const DRAW_MS = 30_000
const STEP_MS = 10_000

let srv: BrakemanServer
let about = ''
let alone = ''
let apiCrew = ''
let webCrew = ''
const runs: string[] = []

const crewBody = (name: string, people: unknown[]) => ({
  name,
  goal: 'build it',
  cwd: '',
  where: 'server',
  isolation: 'worktree',
  openAfterLaunch: false,
  yolo: null,
  members: [{ name: 'builder', agentId: 'worker', prompt: '', start: { when: 'task' } }],
  people,
  tasks: [{ id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.' }],
})

test.beforeAll(async () => {
  test.setTimeout(240_000)
  srv = await brakemanServer({ name: 'brakeman-share' })
  const api = srv.api
  await api.ok('POST', '/api/dispatch/projects', { opId: 'bks-p', key: PROJECT, name: 'Brakeman share' })
  await api.ok('POST', '/api/dispatch/tickets', { opId: 'bks-t1', project: PROJECT, title: 'Share what the crews did' })
  apiCrew = (
    await api.ok<{ crew: { id: string } }>('POST', '/api/crews', crewBody('e2e share api', [
      { name: 'Ada', responsibilities: ['architecture'] },
      { name: HOSTILE, responsibilities: ['security'] },
    ]))
  ).crew.id
  webCrew = (await api.ok<{ crew: { id: string } }>('POST', '/api/crews', crewBody('e2e share web', [{ name: 'Cy', responsibilities: ['product'] }, { name: HEART }]))).crew.id
  // The api crew's run is launched from the ticket (an approved plan's launch): its record carries the ticket. The web crew's is not.
  const ticketRev = async () => (await api.ok<{ ticket: { rev: number } }>('GET', `/api/dispatch/tickets/${KEY}`)).ticket.rev
  const proposed = await api.ok<{ plan: { id: string; rev: number } }>('POST', `/api/dispatch/tickets/${KEY}/plans`, { opId: 'bks-r1', crew: apiCrew })
  await api.ok('POST', `/api/dispatch/plans/${proposed.plan.id}/approve`, { opId: 'bks-a1', expectedRev: proposed.plan.rev, ticketRev: await ticketRev() })
  const fromTicket = (await api.ok<{ launch: { runId: string } }>('POST', `/api/dispatch/tickets/${KEY}/launch`, { opId: 'bks-l1', plan: proposed.plan.id })).launch.runId
  runs.push(fromTicket)
  const plain = (await api.launchCrew(webCrew)).id
  runs.push(plain)

  // The briefs: one asked about both runs, one about the ticket alone (no run). The gate stays open: each turn submits at once.
  srv.setBrief({ schema: 'brakeman.brief/1', title: TITLE, summary: 'Both runs built what the ticket asks.', items: [{ kind: 'fact/1', text: 'The api run built it.', evidence: [`run:${fromTicket}`] }] })
  srv.openGate()
  about = (await srv.ask({ kind: 'ask', subjects: [`run:${fromTicket}`, `run:${plain}`], note: 'What did the two crews build?' })).id
  await srv.until(about, ['briefed'], 120_000)
  alone = (await srv.ask({ kind: 'explain', subjects: [`ticket:${KEY}`] })).id
  await srv.until(alone, ['briefed'], 120_000)
})

test.afterAll(async () => {
  for (const id of runs) await srv?.api.stopRun(id)
  await srv?.stop()
})

/** The brief on the page, drawn: its title shown. */
async function opened(page: Page, id: string): Promise<Locator> {
  await srv.open(page, `/brakeman?request=${id}`, { name: 'Nate' })
  const b = page.locator(`[data-brief="${id}"]`)
  await expect(b.locator('[data-brief-title]')).toHaveText(TITLE, { timeout: DRAW_MS })
  return b
}

/** Opens Share from the head's control drawn at this width, and returns the dialog once its runs are read and it is at rest. */
async function share(page: Page, b: Locator, which: 'wide' | 'phone'): Promise<Locator> {
  await b.locator(`[data-brief-share="${which}"]`).click()
  const dialog = page.locator('[data-share-dialog]')
  await expect(dialog).toBeVisible()
  await expect(dialog).not.toHaveAttribute('data-share-reading', '')
  await atRest(dialog)
  return dialog
}

test("Share names each crew's people, and Copy for them puts their names on the first line as text", async ({ page, context }) => {
  budget(DRAW_MS + 12 * STEP_MS)
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const b = await opened(page, about)
  await expect(b.locator('[data-brief-share="wide"]')).toHaveAttribute('aria-label', "Share with a crew's people")
  const dialog = await share(page, b, 'wide')
  await expect(dialog.locator('[data-share-what]')).toHaveText(SHARE_WHAT)
  // Two crews: the first run named is picked; each person is a chip with their responsibility, linked to their page.
  await expect(dialog.locator('[data-share-pick]')).toHaveCount(2)
  await expect(dialog.locator(`[data-share-pick="${apiCrew}"]`)).toHaveAttribute('aria-pressed', 'true')
  await expect(dialog.locator(`[data-share-crew="${apiCrew}"] [data-share-person]`)).toHaveText(['Ada · architecture owner', `${HOSTILE} · security reviewer`])
  await expect(dialog.locator('[data-share-person="Ada"]')).toHaveAttribute('href', '/people/Ada')
  await expect(dialog.locator('[data-share-no-ticket]')).toHaveCount(0)
  await expect(page.locator('[data-share-comment]')).toHaveText(`Comment on ${KEY}…`)

  await page.locator('[data-share-copy]').click()
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toMatch(/^For /)
  const md = await page.evaluate(() => navigator.clipboard.readText())
  const lines = md.split('\n')
  expect(lines[0]).toBe(API_LINE)
  expect(lines[1]).toBe('')
  expect(lines[2]).toBe(`# ${TITLE}`)
  // No token and no query string anywhere in what leaves the page.
  expect(md).not.toContain(srv.started.token)
  // A name a tracker would link is a code span, in which nothing is a link: every link outside one is a page's own path.
  for (const l of [...md.replace(/`[^`\n]*`/g, '').matchAll(/\]\(([^)]*)\)/g)].map((m) => m[1]!)) expect(l).toMatch(/^\/[^?]*$/)

  // The other crew: its run was launched from no ticket, which the dialog says; its comment asks for a ticket.
  await dialog.locator(`[data-share-pick="${webCrew}"]`).click()
  await expect(dialog.locator(`[data-share-pick="${webCrew}"]`)).toHaveAttribute('aria-pressed', 'true')
  await expect(dialog.locator(`[data-share-crew="${webCrew}"] [data-share-person]`)).toHaveText(['Cy · product owner', HEART])
  await expect(dialog.locator('[data-share-no-ticket]')).toHaveText("e2e share web's run was launched from no ticket: pick the ticket to comment on.")
  await expect(page.locator('[data-share-comment]')).toHaveText('Comment on a ticket…')
  await page.locator('[data-share-copy]').click()
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toMatch(new RegExp(`^For e2e share web's people: Cy \\(product owner\\), ${HEART}\\n\\n# `))
})

test("Comment on the run's ticket posts the addressed brief under the person's name", async ({ page }) => {
  budget(DRAW_MS + 10 * STEP_MS)
  const b = await opened(page, about)
  await share(page, b, 'wide')
  await page.locator('[data-share-comment]').click()
  // Today's comment dialog, given the addressed text with the run's ticket picked; Share's own dialog has closed.
  const comment = page.locator('[data-comment-ticket]')
  await expect(comment).toHaveAttribute('data-comment-target', KEY)
  await expect(page.locator('[data-share-dialog]')).toHaveCount(0)
  const body = comment.locator('textarea').first()
  await expect(body).toHaveValue(new RegExp(`^${API_LINE.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\n\\n# ${TITLE}\\n`))
  await expect(comment.locator('[data-comment-confirm]')).toContainText('It posts under your name, Nate.')
  await page.locator('[data-comment-send]').click()
  await expect(comment).toHaveCount(0)
  const { comments } = await srv.api.ok<{ comments: Array<{ body: string; by: string }> }>('GET', `/api/dispatch/tickets/${KEY}/comments?after=0&limit=50`)
  expect(comments).toHaveLength(1)
  expect(comments[0]!.by).toBe('Nate')
  expect(comments[0]!.body.split('\n')[0]).toBe(API_LINE)
})

test('where the browser refuses the clipboard, the dialog draws the addressed text to select by hand', async ({ page }) => {
  budget(DRAW_MS + 6 * STEP_MS)
  // A browser that refuses the page's write to the clipboard, as one over plain HTTP or with the permission denied does.
  await page.addInitScript(() => {
    Object.defineProperty(navigator.clipboard, 'writeText', { value: () => Promise.reject(new DOMException('denied', 'NotAllowedError')) })
  })
  const b = await opened(page, about)
  const dialog = await share(page, b, 'wide')
  await expect(dialog.locator('[data-share-uncopied]')).toHaveCount(0)
  await page.locator('[data-share-copy]').click()
  await expect(dialog.locator('[data-share-uncopied] span')).toHaveText('The browser did not let the page copy: select the text below and copy it by hand.')
  const text = await dialog.locator('[data-share-text]').evaluate((el) => el.textContent ?? '')
  expect(text.split('\n').slice(0, 3)).toEqual([API_LINE, '', `# ${TITLE}`])
})

test('a brief about no run says it goes with no names, and still copies', async ({ page, context }) => {
  budget(DRAW_MS + 6 * STEP_MS)
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const b = await opened(page, alone)
  const dialog = await share(page, b, 'wide')
  await expect(dialog.locator('[data-share-none]')).toHaveText("This brief names no run with a crew's people: it goes with no names.")
  await expect(dialog.locator('[data-share-people]')).toHaveCount(0)
  await expect(page.locator('[data-share-comment]')).toHaveText('Comment on a ticket…')
  await page.locator('[data-share-copy]').click()
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toMatch(new RegExp(`^# ${TITLE}\\n`))
})

test('on a phone Share is an icon a thumb taps in the head, the dialog fits, and nothing goes sideways', async ({ page }) => {
  budget(DRAW_MS + 8 * STEP_MS)
  await page.setViewportSize({ width: 390, height: 844 })
  const b = await opened(page, about)
  // The head's two actions stay hidden on a phone, as they were.
  await expect(b.locator('[data-brief-copy]')).toBeHidden()
  await expect(b.locator('[data-brief-share="wide"]')).toBeHidden()
  const control = b.locator('[data-brief-share="phone"]')
  await expect(control).toHaveAttribute('aria-label', "Share with a crew's people")
  await tapTarget(control)
  const dialog = await share(page, b, 'phone')
  await expect(dialog.locator('[data-share-what]')).toHaveText(SHARE_WHAT)
  await tapTarget(page.locator('[data-share-copy], [data-share-comment], [data-share-pick]'))
  await noSideways(page)
})

for (const theme of ['dark', 'light'] as const) {
  test(`every text of the share dialog passes its floor in the ${theme} theme, and it names the same people after the theme's redraw`, async ({ page }) => {
    budget(2 * DRAW_MS + 6 * STEP_MS)
    const b = await opened(page, about)
    await useTheme(page, theme)
    await expect(b.locator('[data-brief-title]')).toHaveText(TITLE, { timeout: DRAW_MS })
    const dialog = await share(page, b, 'wide')
    await expect(dialog.locator(`[data-share-crew="${apiCrew}"] [data-share-person]`)).toHaveText(['Ada · architecture owner', `${HOSTILE} · security reviewer`])
    await expect(dialog.locator('[data-share-what]')).toHaveText(SHARE_WHAT)
    const { judged, failures } = await walk(page, page.locator('[role="dialog"]'))
    expect(judged, `${theme}: the walk judged some text`).toBeGreaterThan(0)
    expect(failures, `${theme}: text under its floor (the ten worst):\n${worst(failures)}`).toEqual([])
  })
}
