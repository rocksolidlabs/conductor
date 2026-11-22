import { randomBytes } from 'node:crypto'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import type { Page, Response, Route } from '@playwright/test'
import { expect, standingRoute, test, type Api, type Run } from './fixtures'

// A crew's launch names the revision the launch sheet read, and the server refuses another (#957; docs/protocol.md, POST
// /api/crews/{id}/launch). The sheet keeps no guess of its own about whether its copy is current: a launch of a copy saved over is
// refused `crew_changed`, nothing started, and the sheet says so, reads the crew again and launches what it then shows. Each order is
// held at this test's own route (a save held until released), never by a clock. Every crew is this spec's own, with one member that
// starts when pressed, so a run starts no session; the runs are counted by the crew's own runs, never by a list's length.

const made = { runs: [] as string[], crews: [] as string[] }

/** The config's bound of an expect, and this spec's bound of a wait on a read of the crew. */
const EXPECT = 15_000
const READ = 30_000
/**
 * Raises the running test's timeout by the caps of the waits a step is about to make (palette-rows.spec.ts's budget): the test's timeout
 * is their sum, and the config's 120 s above it for the steps between them.
 */
function budget(...caps: number[]) {
  const info = test.info()
  info.setTimeout(info.timeout + caps.reduce((a, b) => a + b, 0))
}

// A standing route (standingRoute: one no request matches), kept from before the page loads: the tests' own routes go on top, so
// interception is never switched on or off while the page has requests out (capacity-crew.spec.ts lost a GET to an unroute so, #281).
test.beforeEach(async ({ page }) => {
  await standingRoute(page.context())
})

test.afterEach(async ({ api }) => {
  for (const id of made.runs.splice(0)) await api.stopRun(id)
  for (const id of made.crews.splice(0)) await api.call('DELETE', `/api/crews/${encodeURIComponent(id)}`)
})

const label = (what: string) => `e2e ${what} ${randomBytes(3).toString('hex')}`

/** A crew of this test's own, saved through the API: one member, `lead`, that starts when pressed; yolo off, or the server's (`inherit`). */
async function saveCrew(api: Api, name: string, inherit = false) {
  const { crew } = await api.ok<{ crew: { id: string } }>('POST', '/api/crews', { name, goal: '', cwd: '', where: 'server', isolation: 'none', openAfterLaunch: false, ...(inherit ? {} : { yolo: false }), members: [{ name: 'lead', agentId: 'claude', prompt: '', start: { when: 'manual' } }] })
  made.crews.push(crew.id)
  return { id: crew.id, name }
}

/** The crew saved again through the API (another person), its member renamed. */
async function renameThroughApi(api: Api, id: string, to: string) {
  const { crew } = await api.ok<{ crew: Record<string, unknown> & { members: Array<Record<string, unknown>> } }>('GET', `/api/crews/${encodeURIComponent(id)}`)
  const { id: _id, createdAt: _c, updatedAt: _u, manifestRevision: _m, ...body } = crew
  await api.ok('PUT', `/api/crews/${encodeURIComponent(id)}`, { ...body, members: body.members.map((m) => ({ ...m, name: to })) })
}

/** The ids of the runs the server lists for the crew. */
const crewRuns = (api: Api, id: string) => api.ok<{ runs: Array<{ id: string }> }>('GET', `/api/crews/${encodeURIComponent(id)}/runs`).then((r) => r.runs.map((x) => x.id))

/** The first `method` request `match` takes (each one, with `all`), held at this test's route until `release` (`seen` once one is held); the others pass. */
async function holdOnce(page: Page, match: (u: URL) => boolean, method: string, all = false) {
  let seen!: () => void
  let release!: () => void
  const at = new Promise<void>((r) => (seen = r))
  const gate = new Promise<void>((r) => (release = r))
  let first = true
  const handler = async (route: Route) => {
    if (route.request().method() !== method || !(first || all)) return route.fallback()
    first = false
    seen()
    await gate
    await route.fallback()
  }
  await page.route(match, handler)
  return { seen: at, release, off: () => page.unroute(match, handler) }
}

/** Asks for a crew's launch from the app-wide palette, on a page that has drawn its layout: the crew's row, then Enter. */
async function paletteLaunch(page: Page, crew: { id: string; name: string }) {
  budget(EXPECT, EXPECT) // the row, its focus
  await page.keyboard.press('Control+k')
  const palette = page.locator('[data-palette]')
  await palette.locator('input[data-palette-input]').fill(`# ${crew.name}`)
  const row = palette.locator(`[data-palette-row="crew:${crew.id}"]`)
  await expect(row).toBeVisible({ timeout: 15_000 })
  await page.keyboard.press('ArrowDown')
  await expect(row).toHaveAttribute('data-palette-focused', '')
  await page.keyboard.press('Enter')
}

/**
 * The app's router, through the page itself. The push is started and never awaited for its answer: a call that moves the page
 * can be rejected across the move ("Execution context was destroyed", #281), so each caller waits for what the new page draws.
 */
const navigate = (page: Page, path: string) =>
  page.evaluate((u) => {
    void (document.querySelector('#__nuxt') as unknown as { __vue_app__: { config: { globalProperties: { $router: { push: (u: string) => Promise<unknown> } } } } }).__vue_app__.config.globalProperties.$router.push(u)
  }, path)

/** The page's launches of the crew: each one asked (`asked`), and each one answered: its status, and the run of one that started. */
function launchesOf(page: Page, id: string) {
  const seen: Array<{ status: number; run?: Run }> = []
  const pending: Array<Promise<void>> = []
  let asked = 0
  page.on('request', (r) => void (r.method() === 'POST' && new URL(r.url()).pathname === `/api/crews/${id}/launch` && asked++))
  page.on('response', (r: Response) => {
    if (r.request().method() !== 'POST' || new URL(r.url()).pathname !== `/api/crews/${id}/launch`) return
    pending.push(
      r.json().then(
        (b: { run?: Run }) => {
          seen.push({ status: r.status(), run: b.run })
          if (b.run) made.runs.push(b.run.id)
        },
        () => void seen.push({ status: r.status() }),
      ),
    )
  })
  return { seen, asked: () => asked, settled: () => Promise.all(pending) }
}

const sheet = (page: Page) => ({
  confirm: page.locator('[data-crew-launch-confirm]'),
  line: (name: string) => page.locator(`[data-crew-launch-line="${name}"] [data-crew-launch-account]`),
  changed: page.locator('[data-crew-launch-changed]'),
  unrevised: page.locator('[data-crew-launch-unrevised]'),
  yolo: page.locator('[data-crew-launch-yolo]'),
})

const REFUSED = 'The crew was saved since this sheet read it, so nothing was launched.'

/**
 * The sheet's Launch of a copy saved over: the server refuses it `crew_changed`, the sheet says so (announced) and reads the crew again,
 * drawing `now` with the focus on Launch, and its Launch then starts exactly one run of that copy. With `holdRead`, the crew's reads are
 * held at this test's route after the refusal: the sheet says it reads again, Launch held, until they are let through. Returns once
 * that run is answered.
 */
async function refusedThenLaunched(page: Page, launches: ReturnType<typeof launchesOf>, now: string, holdRead?: (u: URL) => boolean) {
  budget(EXPECT, EXPECT, READ, EXPECT, EXPECT, EXPECT, EXPECT, ...(holdRead ? [EXPECT, EXPECT] : []))
  const s = sheet(page)
  const reads = holdRead && (await holdOnce(page, holdRead, 'GET', true))
  const refused = page.waitForResponse((r) => r.request().method() === 'POST' && /\/launch$/.test(new URL(r.url()).pathname))
  await s.confirm.click()
  expect((await refused).status(), 'the copy saved over is refused').toBe(409)
  await expect(s.changed).toBeVisible()
  await expect(page.getByRole('status').filter({ has: s.changed }), 'the notice is announced').toHaveCount(1)
  if (reads) {
    await expect(s.changed).toHaveText(`${REFUSED} It is being read again.`)
    await expect(s.confirm).toBeDisabled()
    reads.release()
    await reads.off()
  }
  await expect(s.line(now), 'the sheet reads the crew again and draws the copy saved').toBeVisible({ timeout: READ })
  await expect(s.line('lead')).toHaveCount(0)
  await expect(s.changed).toContainText('It is shown below as the server holds it now: Launch starts the crew as shown here')
  await expect(s.confirm, 'Launch has the focus once the crew is drawn again').toBeFocused()
  const started = page.waitForResponse((r) => r.request().method() === 'POST' && /\/launch$/.test(new URL(r.url()).pathname))
  await s.confirm.click()
  expect((await started).status(), 'the copy the sheet read again starts').toBe(201)
  await expect(s.confirm).toHaveCount(0)
  await launches.settled()
}

/** The run started is of the copy the sheet drew: one member, `now`. */
function oneRunOf(launches: ReturnType<typeof launchesOf>, now: string) {
  const runs = launches.seen.filter((l) => l.status === 201)
  expect(runs, 'exactly one run').toHaveLength(1)
  expect(runs[0]!.run!.members.map((m) => m.name)).toEqual([now])
  expect(launches.seen.filter((l) => l.status === 409).map((l) => l.status), 'one refusal').toEqual([409])
}

test("Save and launch saves the edits first; a sheet the palette opens while the save is held draws the copy before it, then the copy saved once it lands: one run", async ({ page, api }) => {
  const a = await saveCrew(api, label('reopened'))
  const launches = launchesOf(page, a.id)
  const s = sheet(page)
  const aCrew = (u: URL) => u.pathname === `/api/crews/${a.id}`
  budget(READ, EXPECT, READ, EXPECT, READ, EXPECT) // the page, the button, the sheet, the save, the sheet again, its Launch
  await page.goto(`/crews/${encodeURIComponent(a.id)}`)
  await expect(page.getByLabel('Crew name')).toHaveValue(a.name, { timeout: READ })
  // The edit renames the member: the header says so beside the button, which now saves first.
  await page.getByLabel('Name of lead').fill('renamed')
  await expect(page.locator('[data-launch-unsaved]')).toHaveText('Unsaved changes')
  await expect(page.locator('[data-launch]')).toHaveAccessibleName('Save and launch')
  const save = await holdOnce(page, aCrew, 'PUT')
  await page.locator('[data-launch]').click()
  await save.seen
  // Opened from the palette while the save is held: it reads the crew as saved before the edit.
  await paletteLaunch(page, a)
  await expect(s.line('lead')).toBeVisible({ timeout: READ })
  const saved = page.waitForResponse((r) => r.request().method() === 'PUT' && aCrew(new URL(r.url())))
  save.release()
  expect((await saved).ok()).toBe(true)
  await save.off()
  // The save landed: the sheet opens again on the crew as saved, read by its id; nothing was launched meanwhile.
  await expect(s.line('renamed'), 'the sheet draws the copy saved').toBeVisible({ timeout: READ })
  await expect(s.line('lead')).toHaveCount(0)
  expect(launches.seen, 'nothing launched before Launch').toEqual([])
  const started = page.waitForResponse((r) => r.request().method() === 'POST' && new URL(r.url()).pathname === `/api/crews/${a.id}/launch`)
  await s.confirm.click()
  expect((await started).status()).toBe(201)
  await launches.settled()
  expect(launches.seen.map((l) => [l.status, l.run?.members.map((m) => m.name).join()])).toEqual([[201, 'renamed']])
  expect(await crewRuns(api, a.id)).toEqual([launches.seen[0]!.run!.id])
})

test("a sheet opened from the palette while the editor's own Save is under way is refused once it lands, read again, one run", async ({ page, api }) => {
  const a = await saveCrew(api, label('editor save'))
  const launches = launchesOf(page, a.id)
  const s = sheet(page)
  const aCrew = (u: URL) => u.pathname === `/api/crews/${a.id}`
  budget(READ, READ) // the page, the sheet
  await page.goto(`/crews/${encodeURIComponent(a.id)}`)
  await expect(page.getByLabel('Crew name')).toHaveValue(a.name, { timeout: READ })
  await page.getByLabel('Name of lead').fill('renamed')
  const save = await holdOnce(page, aCrew, 'PUT')
  await page.locator('[data-unsaved-bar]').getByRole('button', { name: 'Save' }).click()
  await save.seen
  await paletteLaunch(page, a)
  await expect(s.line('lead')).toBeVisible({ timeout: READ })
  const saved = page.waitForResponse((r) => r.request().method() === 'PUT' && aCrew(new URL(r.url())))
  save.release()
  expect((await saved).ok()).toBe(true)
  await save.off()
  await refusedThenLaunched(page, launches, 'renamed')
  oneRunOf(launches, 'renamed')
})

test('a sheet of a crew page left and opened again while its Save is under way is refused once it lands, read again, one run', async ({ page, api }) => {
  const a = await saveCrew(api, label('page left'))
  const launches = launchesOf(page, a.id)
  const s = sheet(page)
  const aCrew = (u: URL) => u.pathname === `/api/crews/${a.id}`
  budget(READ, READ) // the page, the page left
  await page.goto(`/crews/${encodeURIComponent(a.id)}`)
  await expect(page.getByLabel('Crew name')).toHaveValue(a.name, { timeout: READ })
  await page.getByLabel('Name of lead').fill('renamed')
  const save = await holdOnce(page, aCrew, 'PUT')
  await page.locator('[data-unsaved-bar]').getByRole('button', { name: 'Save' }).click()
  await save.seen
  // The crew page left while its save is held, then opened again by the palette's launch: a new page, its sheet reading the crew.
  await navigate(page, '/agents')
  await expect(page.locator('[data-session-list]')).toBeVisible({ timeout: READ })
  await paletteLaunch(page, a)
  budget(EXPECT, READ) // the crew page, the sheet
  await expect(page).toHaveURL(new RegExp(`/crews/${a.id}$`), { timeout: EXPECT })
  await expect(s.line('lead')).toBeVisible({ timeout: READ })
  const saved = page.waitForResponse((r) => r.request().method() === 'PUT' && aCrew(new URL(r.url())))
  save.release()
  expect((await saved).ok()).toBe(true)
  await save.off()
  await refusedThenLaunched(page, launches, 'renamed')
  oneRunOf(launches, 'renamed')
})

test('a crew another person saves after the sheet read it is refused, read again (the sheet says so while it reads), one run', async ({ page, api }) => {
  const a = await saveCrew(api, label('other person'))
  const launches = launchesOf(page, a.id)
  const s = sheet(page)
  budget(READ, READ, EXPECT) // the page, the sheet, its Launch
  await page.goto(`/crews/${encodeURIComponent(a.id)}`)
  await expect(page.getByLabel('Crew name')).toHaveValue(a.name, { timeout: READ })
  await page.locator('[data-launch]').click()
  await expect(s.line('lead')).toBeVisible({ timeout: READ })
  await expect(s.confirm).toBeEnabled()
  await renameThroughApi(api, a.id, 'theirs')
  await refusedThenLaunched(page, launches, 'theirs', (u) => u.pathname === `/api/crews/${a.id}`)
  oneRunOf(launches, 'theirs')
  expect(await crewRuns(api, a.id)).toHaveLength(1)
})

test('a refusal that lands after the crew page was left is told on the page the person is on, by a toast naming the crew: no run', async ({ page, api }) => {
  const a = await saveCrew(api, label('left'))
  const launches = launchesOf(page, a.id)
  const s = sheet(page)
  const launchOf = (u: URL) => u.pathname === `/api/crews/${a.id}/launch`
  budget(READ, READ, READ, EXPECT, EXPECT, EXPECT, EXPECT) // the page, the sheet, the page left, the sheet gone, the toast, the URL, no notice
  await page.goto(`/crews/${encodeURIComponent(a.id)}`)
  await expect(page.getByLabel('Crew name')).toHaveValue(a.name, { timeout: READ })
  await page.locator('[data-launch]').click()
  await expect(s.line('lead')).toBeVisible({ timeout: READ })
  await expect(s.confirm).toBeEnabled()
  await renameThroughApi(api, a.id, 'theirs')
  // The launch, refused once it reaches the server, is held at this test's route until the crew page has been left.
  const held = await holdOnce(page, launchOf, 'POST')
  await s.confirm.click()
  await held.seen
  await navigate(page, '/agents')
  await expect(page.locator('[data-session-list]')).toBeVisible({ timeout: READ })
  await expect(s.confirm, 'the sheet went with its page').toHaveCount(0)
  const refused = page.waitForResponse((r) => r.request().method() === 'POST' && launchOf(new URL(r.url())))
  held.release()
  expect((await refused).status(), 'the copy saved over is refused').toBe(409)
  await held.off()
  await expect(page.getByText(`${a.name} was saved since the launch sheet read it: no run was started.`).first()).toBeVisible()
  await expect(page, 'the person is where they went').toHaveURL(/\/agents$/)
  await expect(s.changed).toHaveCount(0)
  await launches.settled()
  expect(launches.seen.map((l) => l.status)).toEqual([409])
  expect(await crewRuns(api, a.id)).toEqual([])
})

test("a crew read without a revision holds Launch with words, and so does one Save and launch saved without one: nothing is launched", async ({ page, api }) => {
  const a = await saveCrew(api, label('unrevised'), true)
  const launches = launchesOf(page, a.id)
  const s = sheet(page)
  const { yoloDefault } = await api.ok<{ yoloDefault?: boolean }>('GET', '/api/catalog')
  const unrevised = 'This server does not say which copy of the crew it holds, so this sheet cannot tell which copy Launch would start: nothing is launched. Load the page again.'
  // The crew's reads and saves answered without a revision, at this test's route: a server that gives none.
  const aCrew = (u: URL) => u.pathname === `/api/crews/${a.id}`
  await page.route(aCrew, async (route) => {
    if (!['GET', 'PUT'].includes(route.request().method())) return route.fallback()
    const answer = await route.fetch()
    const { revision: _r, ...body } = (await answer.json()) as Record<string, unknown>
    await route.fulfill({ response: answer, json: body })
  })
  budget(READ, READ, EXPECT, EXPECT, EXPECT, EXPECT) // the page, the sheet, its words, its Launch, its yolo line, its close
  await page.goto(`/crews/${encodeURIComponent(a.id)}`)
  await expect(page.getByLabel('Crew name')).toHaveValue(a.name, { timeout: READ })
  await page.locator('[data-launch]').click()
  await expect(s.line('lead')).toBeVisible({ timeout: READ })
  await expect(s.unrevised).toHaveText(unrevised)
  await expect(s.confirm).toBeDisabled()
  // The crew inherits its yolo setting: the sheet says it is the server's at the launch, and what it is now.
  await expect(s.yolo).toHaveText(`Permission prompts: ${yoloDefault ? 'skipped: the members act without asking' : 'the members ask before they act'} (the server's default now)`)
  await page.locator('[data-crew-launch-cancel]').click()
  await expect(s.confirm).toHaveCount(0)
  // An edit's Save and launch saves it first, and the save answers no revision; the sheet then reads the crew, which comes without one
  // too: Launch held with the words, and no launch is sent.
  budget(EXPECT, READ, EXPECT, EXPECT) // the save, the sheet, its words, its Launch
  await page.getByLabel('Name of lead').fill('renamed')
  const saved = page.waitForResponse((r) => r.request().method() === 'PUT' && aCrew(new URL(r.url())))
  await page.locator('[data-launch]').click()
  expect((await saved).ok(), 'the edit is saved').toBe(true)
  await expect(s.line('renamed')).toBeVisible({ timeout: READ })
  await expect(s.unrevised).toHaveText(unrevised)
  await expect(s.confirm).toBeDisabled()
  expect(launches.asked(), 'no launch was asked').toBe(0)
  expect(await crewRuns(api, a.id)).toEqual([])
})

test('nothing that should go is refused: a saved crew, a draft in one go, two launches in a row', async ({ page, api }) => {
  const a = await saveCrew(api, label('goes'))
  const launches = launchesOf(page, a.id)
  const s = sheet(page)
  const launchOnce = async (line: string) => {
    budget(READ, EXPECT) // the sheet, its close
    await expect(s.line(line)).toBeVisible({ timeout: READ })
    const started = page.waitForResponse((r) => r.request().method() === 'POST' && new URL(r.url()).pathname === `/api/crews/${a.id}/launch`)
    await s.confirm.click()
    expect((await started).status()).toBe(201)
    await expect(s.confirm).toHaveCount(0)
  }
  budget(READ, EXPECT) // the page; at the end, no notice
  await page.goto(`/crews/${encodeURIComponent(a.id)}`)
  await expect(page.getByLabel('Crew name')).toHaveValue(a.name, { timeout: READ })
  // The saved crew, twice in a row.
  for (let i = 0; i < 2; i++) {
    await page.locator('[data-launch]').click()
    await launchOnce('lead')
  }
  // An edit in one go: Save and launch saves it, and the sheet launches the revision the save answered.
  await page.getByLabel('Name of lead').fill('renamed')
  await page.locator('[data-launch]').click()
  await launchOnce('renamed')
  await launches.settled()
  expect(launches.seen.map((l) => l.status)).toEqual([201, 201, 201])
  expect(launches.seen.map((l) => l.run!.members.map((m) => m.name).join()).sort()).toEqual(['lead', 'lead', 'renamed'])
  await expect(s.changed).toHaveCount(0)
  expect(await crewRuns(api, a.id)).toHaveLength(3)
})

test("a launch the server refuses stays on the sheet in the server's words, with the run's name kept; Launch again is refused again: no run", async ({ page, api, state }) => {
  // A crew of worktrees in a folder that is no git working tree: the server refuses its launch 409 not_a_repo, starting nothing.
  const plain = join(state.root, `launch-plain-${randomBytes(3).toString('hex')}`)
  mkdirSync(plain, { recursive: true })
  const name = label('refused')
  const { crew } = await api.ok<{ crew: { id: string } }>('POST', '/api/crews', { name, goal: '', cwd: plain, where: 'server', isolation: 'worktree', openAfterLaunch: false, yolo: false, members: [{ name: 'lead', agentId: 'claude', prompt: '', start: { when: 'manual' } }] })
  made.crews.push(crew.id)
  const launches = launchesOf(page, crew.id)
  const s = sheet(page)
  const run = `e2e kept ${randomBytes(3).toString('hex')}`
  budget(READ, EXPECT, EXPECT, READ, EXPECT, EXPECT, EXPECT, EXPECT, EXPECT) // the page, the menu, the name, the sheet, the refusal and what stays, again, the close
  await page.goto(`/crews/${encodeURIComponent(crew.id)}`)
  await expect(page.getByLabel('Crew name')).toHaveValue(name, { timeout: READ })
  // The run named in the launch's own menu: the sheet opens as that run.
  await page.locator('[data-launch-menu]').click()
  await page.locator('[data-launch-name]').fill(run)
  await expect(page.locator('[data-launch-name]')).toHaveValue(run)
  await page.locator('[data-launch-named]').click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toContainText(`As the run ${run}`)
  await expect(s.line('lead')).toBeVisible({ timeout: READ })
  for (let i = 0; i < 2; i++) {
    const refused = page.waitForResponse((r) => r.request().method() === 'POST' && new URL(r.url()).pathname === `/api/crews/${crew.id}/launch`)
    await s.confirm.click()
    const answer = await refused
    expect(answer.status(), 'the launch is refused').toBe(409)
    const { error } = (await answer.json()) as { error: { code: string; message: string } }
    expect(error.code).toBe('not_a_repo')
    // On the sheet, announced, in the server's own words; the sheet as it was opened: the run's name, the member, Launch offered.
    const notice = page.locator('[data-crew-launch-refused="not_a_repo"]')
    await expect(notice).toHaveText(`The server refused the launch, so nothing was started: ${error.message}`)
    await expect(page.getByRole('status').filter({ has: notice }), 'the refusal is announced').toHaveCount(1)
    await expect(dialog).toContainText(`As the run ${run}`)
    await expect(s.line('lead')).toBeVisible()
    await expect(s.confirm).toBeEnabled()
  }
  await page.locator('[data-crew-launch-cancel]').click()
  await expect(s.confirm).toHaveCount(0)
  await launches.settled()
  expect(launches.seen.map((l) => l.status)).toEqual([409, 409])
  expect(await crewRuns(api, crew.id)).toEqual([])
})
