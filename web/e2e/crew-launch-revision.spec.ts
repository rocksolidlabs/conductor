import { randomBytes } from 'node:crypto'
import type { Page, Response, Route } from '@playwright/test'
import { expect, test, type Api, type Run } from './fixtures'

// A crew's launch names the revision the launch sheet read, and the server refuses another (#957; docs/protocol.md, POST
// /api/crews/{id}/launch). The sheet keeps no guess of its own about whether its copy is current: a launch of a copy saved over is
// refused `crew_changed`, nothing started, and the sheet says so, reads the crew again and launches what it then shows. Each order is
// held at this test's own route (a save held until released), never by a clock. Every crew is this spec's own, with one member that
// starts when pressed, so a run starts no session; the runs are counted by the crew's own runs, never by a list's length.

const made = { runs: [] as string[], crews: [] as string[] }

test.afterEach(async ({ api }) => {
  for (const id of made.runs.splice(0)) await api.stopRun(id)
  for (const id of made.crews.splice(0)) await api.call('DELETE', `/api/crews/${encodeURIComponent(id)}`)
})

const label = (what: string) => `e2e ${what} ${randomBytes(3).toString('hex')}`

/** A crew of this test's own, saved through the API: one member, `lead`, that starts when pressed. */
async function saveCrew(api: Api, name: string) {
  const { crew } = await api.ok<{ crew: { id: string } }>('POST', '/api/crews', { name, goal: '', cwd: '', where: 'server', isolation: 'none', openAfterLaunch: false, yolo: false, members: [{ name: 'lead', agentId: 'claude', prompt: '', start: { when: 'manual' } }] })
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

/** Asks for a crew's launch from the app-wide palette, on a page that has drawn its layout: the crew's row, then Enter. */
async function paletteLaunch(page: Page, crew: { id: string; name: string }) {
  await page.keyboard.press('Control+k')
  const palette = page.locator('[data-palette]')
  await palette.locator('input[data-palette-input]').fill(`# ${crew.name}`)
  const row = palette.locator(`[data-palette-row="crew:${crew.id}"]`)
  await expect(row).toBeVisible({ timeout: 15_000 })
  await page.keyboard.press('ArrowDown')
  await expect(row).toHaveAttribute('data-palette-focused', '')
  await page.keyboard.press('Enter')
}

/** The app's router, through the page itself. */
const navigate = (page: Page, path: string) =>
  page.evaluate((u) => (document.querySelector('#__nuxt') as unknown as { __vue_app__: { config: { globalProperties: { $router: { push: (u: string) => Promise<unknown> } } } } }).__vue_app__.config.globalProperties.$router.push(u), path)

/** The page's launches of the crew, each answered: its status, and the run of one that started. */
function launchesOf(page: Page, id: string) {
  const seen: Array<{ status: number; run?: Run }> = []
  const pending: Array<Promise<void>> = []
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
  return { seen, settled: () => Promise.all(pending) }
}

const sheet = (page: Page) => ({
  confirm: page.locator('[data-crew-launch-confirm]'),
  line: (name: string) => page.locator(`[data-crew-launch-line="${name}"] [data-crew-launch-account]`),
  changed: page.locator('[data-crew-launch-changed]'),
})

/**
 * The sheet's Launch of a copy saved over: the server refuses it `crew_changed`, the sheet says so and reads the crew again, drawing
 * `now`, and its Launch then starts exactly one run of that copy. Returns once that run is answered.
 */
async function refusedThenLaunched(page: Page, launches: ReturnType<typeof launchesOf>, now: string) {
  const s = sheet(page)
  const refused = page.waitForResponse((r) => r.request().method() === 'POST' && /\/launch$/.test(new URL(r.url()).pathname))
  await s.confirm.click()
  expect((await refused).status(), 'the copy saved over is refused').toBe(409)
  await expect(s.changed).toBeVisible()
  await expect(s.line(now), 'the sheet reads the crew again and draws the copy saved').toBeVisible({ timeout: 30_000 })
  await expect(s.line('lead')).toHaveCount(0)
  await expect(s.confirm).toBeEnabled()
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

test("a sheet opened again from the palette while a draft's launch saves it shows the copy before the save: its launch is refused, read again, one run", async ({ page, api }) => {
  test.setTimeout(180_000)
  const a = await saveCrew(api, label('reopened'))
  const launches = launchesOf(page, a.id)
  const s = sheet(page)
  const aCrew = (u: URL) => u.pathname === `/api/crews/${a.id}`
  await page.goto(`/crews/${encodeURIComponent(a.id)}`)
  await expect(page.getByLabel('Crew name')).toHaveValue(a.name, { timeout: 30_000 })
  // The draft renames the member and launches; its save is held at this test's route.
  await page.getByLabel('Name of lead').fill('renamed')
  await page.locator('[data-launch]').click()
  await expect(s.line('renamed')).toBeVisible({ timeout: 30_000 })
  const save = await holdOnce(page, aCrew, 'PUT')
  await s.confirm.click()
  await save.seen
  await page.keyboard.press('Escape')
  await expect(s.confirm).toHaveCount(0)
  // Opened again from the palette: it reads the crew while the save is held, the copy before the save.
  await paletteLaunch(page, a)
  await expect(s.line('lead')).toBeVisible({ timeout: 30_000 })
  const saved = page.waitForResponse((r) => r.request().method() === 'PUT' && aCrew(new URL(r.url())))
  save.release()
  expect((await saved).ok()).toBe(true)
  await expect(page.getByText('was saved, but the launch sheet was opened again').first()).toBeVisible({ timeout: 15_000 })
  await save.off()
  expect(launches.seen, 'the first confirmation started nothing').toEqual([])
  await refusedThenLaunched(page, launches, 'renamed')
  oneRunOf(launches, 'renamed')
  expect(await crewRuns(api, a.id)).toEqual([launches.seen.find((l) => l.run)!.run!.id])
})

test("a sheet opened from the palette while the editor's own Save is under way is refused once it lands, read again, one run", async ({ page, api }) => {
  test.setTimeout(180_000)
  const a = await saveCrew(api, label('editor save'))
  const launches = launchesOf(page, a.id)
  const s = sheet(page)
  const aCrew = (u: URL) => u.pathname === `/api/crews/${a.id}`
  await page.goto(`/crews/${encodeURIComponent(a.id)}`)
  await expect(page.getByLabel('Crew name')).toHaveValue(a.name, { timeout: 30_000 })
  await page.getByLabel('Name of lead').fill('renamed')
  const save = await holdOnce(page, aCrew, 'PUT')
  await page.locator('[data-unsaved-bar]').getByRole('button', { name: 'Save' }).click()
  await save.seen
  await paletteLaunch(page, a)
  await expect(s.line('lead')).toBeVisible({ timeout: 30_000 })
  const saved = page.waitForResponse((r) => r.request().method() === 'PUT' && aCrew(new URL(r.url())))
  save.release()
  expect((await saved).ok()).toBe(true)
  await save.off()
  await refusedThenLaunched(page, launches, 'renamed')
  oneRunOf(launches, 'renamed')
})

test('a sheet of a crew page left and opened again while its Save is under way is refused once it lands, read again, one run', async ({ page, api }) => {
  test.setTimeout(180_000)
  const a = await saveCrew(api, label('page left'))
  const launches = launchesOf(page, a.id)
  const s = sheet(page)
  const aCrew = (u: URL) => u.pathname === `/api/crews/${a.id}`
  await page.goto(`/crews/${encodeURIComponent(a.id)}`)
  await expect(page.getByLabel('Crew name')).toHaveValue(a.name, { timeout: 30_000 })
  await page.getByLabel('Name of lead').fill('renamed')
  const save = await holdOnce(page, aCrew, 'PUT')
  await page.locator('[data-unsaved-bar]').getByRole('button', { name: 'Save' }).click()
  await save.seen
  // The crew page left while its save is held, then opened again by the palette's launch: a new page, its sheet reading the crew.
  await navigate(page, '/agents')
  await expect(page.locator('[data-session-list]')).toBeVisible({ timeout: 30_000 })
  await paletteLaunch(page, a)
  await expect(page).toHaveURL(new RegExp(`/crews/${a.id}$`), { timeout: 15_000 })
  await expect(s.line('lead')).toBeVisible({ timeout: 30_000 })
  const saved = page.waitForResponse((r) => r.request().method() === 'PUT' && aCrew(new URL(r.url())))
  save.release()
  expect((await saved).ok()).toBe(true)
  await save.off()
  await refusedThenLaunched(page, launches, 'renamed')
  oneRunOf(launches, 'renamed')
})

test('a crew another person saves after the sheet read it is refused, read again, one run', async ({ page, api }) => {
  test.setTimeout(180_000)
  const a = await saveCrew(api, label('other person'))
  const launches = launchesOf(page, a.id)
  const s = sheet(page)
  await page.goto(`/crews/${encodeURIComponent(a.id)}`)
  await expect(page.getByLabel('Crew name')).toHaveValue(a.name, { timeout: 30_000 })
  await page.locator('[data-launch]').click()
  await expect(s.line('lead')).toBeVisible({ timeout: 30_000 })
  await expect(s.confirm).toBeEnabled()
  await renameThroughApi(api, a.id, 'theirs')
  await refusedThenLaunched(page, launches, 'theirs')
  oneRunOf(launches, 'theirs')
  expect(await crewRuns(api, a.id)).toHaveLength(1)
})

test('nothing that should go is refused: a saved crew, a draft in one go, two launches in a row', async ({ page, api }) => {
  test.setTimeout(180_000)
  const a = await saveCrew(api, label('goes'))
  const launches = launchesOf(page, a.id)
  const s = sheet(page)
  const launchOnce = async (line: string) => {
    await expect(s.line(line)).toBeVisible({ timeout: 30_000 })
    const started = page.waitForResponse((r) => r.request().method() === 'POST' && new URL(r.url()).pathname === `/api/crews/${a.id}/launch`)
    await s.confirm.click()
    expect((await started).status()).toBe(201)
    await expect(s.confirm).toHaveCount(0)
  }
  await page.goto(`/crews/${encodeURIComponent(a.id)}`)
  await expect(page.getByLabel('Crew name')).toHaveValue(a.name, { timeout: 30_000 })
  // The saved crew, twice in a row.
  for (let i = 0; i < 2; i++) {
    await page.locator('[data-launch]').click()
    await launchOnce('lead')
  }
  // A draft in one go: saved, then launched at the revision its save answered.
  await page.getByLabel('Name of lead').fill('renamed')
  await page.locator('[data-launch]').click()
  await launchOnce('renamed')
  await launches.settled()
  expect(launches.seen.map((l) => l.status)).toEqual([201, 201, 201])
  expect(launches.seen.map((l) => l.run!.members.map((m) => m.name).join()).sort()).toEqual(['lead', 'lead', 'renamed'])
  await expect(s.changed).toHaveCount(0)
  expect(await crewRuns(api, a.id)).toHaveLength(3)
})
