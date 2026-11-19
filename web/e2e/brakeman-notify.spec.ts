import type { Browser, Page } from '@playwright/test'
import { atRest, expect, test, type Run } from './fixtures'
import { brakemanServer, type BrakemanServer } from './brakeman-server'

// A background brief of Brakeman's that holds a decision notifies the operator's open pages, in Conductor's own words, once; every
// other background brief stays silent (internal/api/brakeman_notify.go decides, the page alerts on its event alone). On a server of its
// own, Brakeman in the background with a 10 s window and no cooldown. A managed run whose check always fails (brakeman-role.spec.ts's
// fixture) wakes it; the stub holds each turn at its gate and then submits the brief the spec has set (or its own, facts only). The
// page's Notification is a stub of the spec's that records what it is asked to show; "none" is proved by order, a person's session
// asking after the brief being the only notification. The decision cards are about a ticket the stub reads as it turns, at the
// revision it read: a later brief about the same ticket at another revision holds a new decision.
test.describe.configure({ mode: 'serial' })

/** The cap of every wait a step makes without a bound of its own. */
const STEP_MS = 15_000
/** The cap of a page's first draw after a load, which only bounds a hang (brakeman-role.spec.ts says why). */
const DRAW_MS = 60_000
test.use({ actionTimeout: STEP_MS, navigationTimeout: STEP_MS })

const PROJECT = 'BKN'
const TICKET = `${PROJECT}-1`
/** Words each decision brief holds where a person reads it: none may reach a notification. */
const MARKS = ['MARKTITLE', 'MARKREADING', 'MARKQUESTION', 'MARKHAPPENED', 'MARKWHY', 'MARKAUTH', 'MARKOPTION', `${PROJECT}-`]

const failCrew = {
  name: 'e2e brakeman notify',
  goal: 'fail a check',
  cwd: '',
  where: 'server',
  isolation: 'worktree',
  openAfterLaunch: false,
  yolo: null,
  members: [{ name: 'builder', agentId: 'worker-cli', prompt: '', start: { when: 'task' } }],
  tasks: [{ id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.', checks: [{ id: 'never', argv: ['sh', '-c', 'exit 1'] }] }],
}

/**
 * The caps of one wake turn: the run's failure (its check's three attempts, 120 s), the wake fired from the window (40 s), its turn
 * running at the gate (60 s), its brief kept once the gate opens (60 s), and the notification that follows, or the person's (30 s).
 */
const TURN_CAPS = [120_000, 40_000, 60_000, 60_000, 30_000]
const TURN_MS = TURN_CAPS.reduce((a, b) => a + b, 0)

let srv: BrakemanServer
let crewId = ''
const runs: string[] = []
let wake2 = ''

interface Note {
  title: string
  body?: string
  tag?: string
}

test.beforeAll(async () => {
  srv = await brakemanServer({
    name: 'brakeman-notify',
    env: { STUB_BRAKEMAN_TICKET: TICKET },
    config: { wake: { mode: 'background', windowSeconds: 10, cooldownMinutes: 0, questionMinutes: 15, briefings: [] } },
  })
  await srv.api.ok('POST', '/api/dispatch/projects', { opId: 'bkn-p', key: PROJECT, name: 'Brakeman notify' })
  await srv.api.ok('POST', '/api/dispatch/tickets', { opId: 'bkn-t1', project: PROJECT, title: 'The build that never passes' })
  crewId = (await srv.api.ok<{ crew: { id: string } }>('POST', '/api/crews', failCrew)).crew.id
})

test.afterAll(async () => {
  if (!srv) return
  for (const id of runs) await srv.api.stopRun(id).catch(() => undefined)
  await srv.stop()
})

/**
 * The page on this spec's server at `path`: browser notifications on and granted (unless this browser already keeps its settings: a
 * reload keeps what the page saved), and a Notification of the spec's that records each it is asked to show (window.__notes) and
 * keeps it, so a click can be made on it (window.__shown).
 */
async function open(page: Page, path: string) {
  await page.addInitScript(() => {
    if (!localStorage.getItem('conductor.attention.settings')) localStorage.setItem('conductor.attention.settings', JSON.stringify({ notifications: true, chime: false }))
    const notes: Array<{ title: string; body?: string; tag?: string }> = []
    const shown: unknown[] = []
    Object.assign(window, { __notes: notes, __shown: shown })
    class RecordingNotification {
      static permission = 'granted'
      static requestPermission() {
        return Promise.resolve('granted')
      }
      onclick: (() => void) | null = null
      constructor(title: string, options?: { body?: string; tag?: string }) {
        notes.push({ title, body: options?.body, tag: options?.tag })
        shown.push(this)
      }
      close() {}
    }
    ;(window as unknown as { Notification: unknown }).Notification = RecordingNotification
  })
  await srv.open(page, path)
}

/** A page of a browser of its own (its own storage: the briefs' switch as a fresh browser has it). */
async function freshPage(browser: Browser, path: string): Promise<Page> {
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const page = await context.newPage()
  await open(page, path)
  return page
}

const notes = (page: Page) => page.evaluate(() => (window as unknown as { __notes: Note[] }).__notes)
/** Clicks the notification the page showed at `i`, as a person does. */
const clickNote = (page: Page, i: number) => page.evaluate((i) => (window as unknown as { __shown: Array<{ onclick: () => void }> }).__shown[i]!.onclick(), i)
const runOf = (id: string) => srv.api.ok<{ run: Run }>('GET', `/api/runs/${encodeURIComponent(id)}`).then((r) => r.run)
const roleLog = () => srv.api.ok<{ log: Array<{ source: string; subject?: string; request?: string }> }>('GET', '/api/brakeman/role?limit=1000').then((r) => r.log)
const ticketRev = () => srv.api.ok<{ ticket: { rev: number } }>('GET', `/api/dispatch/tickets/${TICKET}`).then((r) => r.ticket.rev)

/** A brief of one decision card about the ticket at `rev`, every word a person reads one of MARKS. */
function decisionBrief(rev: number) {
  return {
    schema: 'brakeman.brief/1',
    title: 'MARKTITLE',
    summary: 'MARKREADING',
    items: [
      { kind: 'fact/1', text: 'MARKHAPPENED', evidence: [`ticket:${TICKET}`] },
      {
        kind: 'decision/1',
        about: `ticket:${TICKET}`,
        happened: 'MARKHAPPENED',
        whyHuman: 'MARKWHY',
        question: 'MARKQUESTION?',
        alternatives: [
          { label: 'MARKOPTION one', tradeoffs: 'MARKOPTION a' },
          { label: 'MARKOPTION two', tradeoffs: 'MARKOPTION b' },
        ],
        recommendation: 0,
        authorizes: 'MARKAUTH',
        evidence: [`ticket:${TICKET}`],
        subjectRevision: String(rev),
      },
    ],
  }
}

/**
 * One wake turn: the gate shut, a run of the failing crew launched and failed, the wake request its failure fired read from the
 * role's log, running at the gate; then `brief` set (null for the stub's own), the gate opened, and the request briefed. Each step's
 * wait is its cap in TURN_CAPS.
 */
async function wakeTurn(brief: unknown): Promise<string> {
  srv.shutGate()
  const runId = (await srv.api.launchCrew(crewId)).id
  runs.push(runId)
  await expect.poll(async () => (await runOf(runId)).outcome ?? '', { timeout: TURN_CAPS[0], intervals: [500] }).toBe('failed')
  await expect
    .poll(async () => (await roleLog()).find((r) => r.source === 'task_failed' && r.subject === `task:${runId}/build`)?.request ?? '', { timeout: TURN_CAPS[1], intervals: [500] })
    .toMatch(/^br-\d+$/)
  const id = (await roleLog()).find((r) => r.source === 'task_failed' && r.subject === `task:${runId}/build`)!.request!
  await srv.until(id, ['running'], TURN_CAPS[2])
  srv.setBrief(brief)
  srv.openGate()
  await srv.until(id, ['briefed'], TURN_CAPS[3])
  return id
}

/** A person's session asking its question ("Which database?"), whose notification follows whatever the brief raised; then it stops. */
async function personAsks(...pages: Page[]) {
  const asker = await srv.api.ok<{ id: string }>('POST', '/api/sessions', { agentId: 'asker' })
  try {
    for (const page of pages) await expect.poll(async () => (await notes(page)).some((n) => n.body === 'Which database?'), { timeout: TURN_CAPS[4] }).toBe(true)
  } finally {
    await srv.api.stopSession(asker.id)
  }
}

test("a background brief without a decision raises nothing; one with a decision raises one in Conductor's words, whose click opens it, wide and on a phone", async ({ page }) => {
  test.setTimeout(DRAW_MS + 2 * TURN_MS + 12 * STEP_MS)
  await page.setViewportSize({ width: 1440, height: 900 })
  await open(page, '/brakeman')
  await expect(page.locator('[data-brakeman-role] [data-role-state]')).toHaveAttribute('data-role-state', 'idle', { timeout: DRAW_MS })
  // A background brief of facts alone: the person's question after it is the only notification.
  const quiet = await wakeTurn(null)
  await expect(page.locator(`[data-brakeman-briefs] [data-request="${quiet}"]`)).toBeVisible()
  await personAsks(page)
  expect(await notes(page)).toEqual([expect.objectContaining({ body: 'Which database?' })])
  // A background brief with a decision: one notification, the fixed words, no word of the brief.
  wake2 = await wakeTurn(decisionBrief(await ticketRev()))
  await expect.poll(async () => (await notes(page)).length, { timeout: TURN_CAPS[4] }).toBe(2)
  const shown = (await notes(page))[1]!
  expect(shown).toEqual({ title: `Brakeman's background brief ${wake2} holds 1 decision`, body: 'It opens on the Brakeman page.', tag: 'conductor-brakeman-needs-you' })
  for (const m of MARKS) expect(JSON.stringify(shown)).not.toContain(m)
  // Its click opens the brief on the Brakeman page, its decision drawn; nothing else moved by itself.
  expect(new URL(page.url()).search).toBe('')
  await clickNote(page, 1)
  await expect(page).toHaveURL(new RegExp(`/brakeman\\?request=${wake2}$`))
  const decision = page.locator(`[data-brief-decision][data-decision-about="ticket:${TICKET}"]`)
  await expect(decision).toBeVisible()
  await expect(decision.locator('[data-decision-question]')).toHaveText('MARKQUESTION?')
  // The same address on a phone: back to the list, at 390 x 844, and the click again.
  await page.goBack()
  await expect(page).toHaveURL(/\/brakeman$/)
  await page.setViewportSize({ width: 390, height: 844 })
  await clickNote(page, 1)
  await expect(page).toHaveURL(new RegExp(`/brakeman\\?request=${wake2}$`))
  await expect(decision).toBeVisible()
  expect(await notes(page)).toHaveLength(2)
})

test("the briefs' switch is on by default and kept off after a reload; switched off, a new decision raises nothing in that browser and one elsewhere", async ({ page, browser }) => {
  test.setTimeout(2 * DRAW_MS + TURN_MS + 16 * STEP_MS)
  await page.setViewportSize({ width: 1440, height: 900 })
  await open(page, '/brakeman?alerts')
  const panel = page.locator('[data-alerts-panel-body]')
  const briefs = panel.locator('[data-alert-briefs]')
  await expect(briefs).toBeVisible({ timeout: DRAW_MS })
  await atRest(briefs)
  await expect(briefs).toContainText("Brakeman's background briefs with a decision")
  await expect(briefs).toContainText('once, on the pages open then')
  // Under the two switches, which stay as they were.
  await expect(panel.getByText('When a session needs input, and for the events routed to Browser:')).toBeVisible()
  const chime = (await panel.getByRole('switch', { name: 'Chime' }).boundingBox())!
  expect((await briefs.boundingBox())!.y).toBeGreaterThan(chime.y)
  const toggle = briefs.getByRole('switch')
  await expect(toggle).toHaveAttribute('aria-checked', 'true')
  await toggle.click()
  await expect(toggle).toHaveAttribute('aria-checked', 'false')
  expect(await page.evaluate(() => JSON.parse(localStorage.getItem('conductor.attention.settings') ?? '{}').briefs)).toBe(false)
  // Loaded again, it is still off.
  await page.goto(`${srv.started.baseURL}/brakeman?alerts`)
  await expect(briefs).toBeVisible({ timeout: DRAW_MS })
  await expect(briefs.getByRole('switch')).toHaveAttribute('aria-checked', 'false')
  await page.keyboard.press('Escape')
  await expect(panel).toHaveCount(0)
  // The phone's More draws it too, a touch target, still off.
  await page.setViewportSize({ width: 390, height: 844 })
  await page.locator('[data-bottom-tab="more"]').click()
  const more = page.locator('[data-bottom-more] [data-alert-briefs]')
  await expect(more).toBeVisible()
  await atRest(more)
  expect((await more.boundingBox())!.height).toBeGreaterThanOrEqual(44 - 0.5)
  await expect(more.getByRole('switch')).toHaveAttribute('aria-checked', 'false')
  await page.keyboard.press('Escape')
  await page.setViewportSize({ width: 1440, height: 900 })
  // The ticket moves on: a brief about it at its new revision holds a decision no earlier one held. This browser, switched off,
  // shows only the person's question; a fresh browser, on, shows the brief's first.
  const other = await freshPage(browser, '/brakeman')
  await expect(other.locator('[data-brakeman-role] [data-role-state]')).toBeVisible({ timeout: DRAW_MS })
  await srv.api.ok('POST', `/api/dispatch/tickets/${TICKET}/edit`, { opId: 'bkn-rename', set: { title: 'The build that never passes, renamed' } })
  const wake3 = await wakeTurn(decisionBrief(await ticketRev()))
  await personAsks(page, other)
  expect(await notes(other)).toEqual([expect.objectContaining({ title: `Brakeman's background brief ${wake3} holds 1 decision` }), expect.objectContaining({ body: 'Which database?' })])
  expect(await notes(page)).toEqual([expect.objectContaining({ body: 'Which database?' })])
  await other.context().close()
})

test('a later background brief that repeats the decision raises nothing', async ({ page }) => {
  test.setTimeout(DRAW_MS + TURN_MS + 6 * STEP_MS)
  await page.setViewportSize({ width: 1440, height: 900 })
  await open(page, '/brakeman')
  await expect(page.locator('[data-brakeman-role] [data-role-state]')).toBeVisible({ timeout: DRAW_MS })
  // The ticket as it is now: the decision the last brief held, held again.
  const again = await wakeTurn(decisionBrief(await ticketRev()))
  await expect(page.locator(`[data-brakeman-briefs] [data-request="${again}"]`)).toBeVisible()
  await personAsks(page)
  expect(await notes(page)).toEqual([expect.objectContaining({ body: 'Which database?' })])
  expect(wake2).not.toBe('')
})
