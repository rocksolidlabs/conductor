import { writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { budget, DAY_WORKER_MS, dayPage, expect, task, test, type Session } from './fixtures'
import { BUSY_LIVE, DAY_STEP_MS, letGo, LONG_LABEL, LONG_NAME, LONG_TITLE, startDay, STATES, TICKETS, type Day } from './day'

// The seeded day proves itself through the API (UX1's PR-1, docs/plans/ux-review.md, the chapter Tests): each part holds what it
// promises, read by name; the states hold once a page has attached every session; the `day` fixture's guard names what a test left
// and what it changed; dayPage opens the theme and the touch it is asked for. When a stub or the engine changes what a part makes, this
// spec fails first and names the part, so that a later layout failure is never a seeding drift in disguise.

/** The refs of the inbox's items by kind, each kind's sorted. */
function byKind(refs: Iterable<{ ref: string; kind: string }>): Record<string, string[]> {
  const out: Record<string, string[]> = {}
  for (const { ref, kind } of refs) (out[kind] ??= []).push(ref)
  for (const k of Object.keys(out)) out[k]!.sort()
  return out
}

/** The five sessions of `states` as the server lists them: name, status and attention. */
async function fiveStates(day: Day): Promise<Array<[string, string, string]>> {
  const n = await day.part('states')
  const out: Array<[string, string, string]> = []
  for (const name of Object.keys(STATES)) {
    const s = await day.api.session(n[name]!)
    out.push([s.name, s.status, s.attention?.state ?? ''])
  }
  return out
}

const FIVE: Array<[string, string, string]> = [
  ['mac-only-flake', 'running', 'needs_input'],
  ['yard-tile-resize', 'running', 'working'],
  ['flaky-vim-spec', 'running', 'done'],
  ['driver', 'running', ''],
  ['old-bisect', 'stopped', ''],
]

test('each part of the day holds what it promises, read through the API by name', async ({ day }) => {
  await day.part('busy')
  const n = { ...(await day.part('waiting')), ...(await day.part('runs')), ...(await day.part('long')) }

  // states: one session in each state of the status table.
  expect(await fiveStates(day)).toEqual(FIVE)

  // The inbox: exactly the refs the parts made, by kind.
  const inbox = await day.inbox()
  expect(byKind(inbox)).toEqual(byKind([...day.refs.keys()].map((ref) => ({ ref, kind: ref.slice(0, ref.indexOf(':')) }))))
  expect(Object.fromEntries(Object.entries(byKind(inbox)).map(([k, v]) => [k, v.length]))).toEqual({ question: 3, decision: 3, plan: 2, task: 1 })

  // waiting: Paging spike's index failed with three attempts; runs: Ledger migration's six task states, Release notes succeeded.
  const spike = await day.api.run(n['Paging spike']!)
  expect([task(spike, 'index').state, task(spike, 'index').attempts]).toEqual(['failed', 3])
  const ledger = await day.api.run(n['Ledger migration']!)
  expect(Object.fromEntries((ledger.tasks ?? []).map((t) => [t.id, t.state]))).toEqual({ schema: 'succeeded', backfill: 'succeeded', migrate: 'running', verify: 'running', cutover: 'blocked', cleanup: 'blocked' })
  expect((await day.api.run(n['Release notes']!)).outcome).toBe('succeeded')

  // dispatch: the fourteen tickets by key with their owners.
  const tickets = (await day.api.ok<{ tickets: Array<{ key: string; owner: string }> }>('GET', '/api/dispatch/tickets?project=CND')).tickets
  expect(tickets.map((t) => [t.key, t.owner]).sort((a, b) => Number(a[0]!.slice(4)) - Number(b[0]!.slice(4)))).toEqual(TICKETS.map((t, i) => [`CND-${i + 1}`, t.owner]))

  // long: the session's name 120 bytes, the run's label exactly 60 characters (the bound a launch accepts), the ticket's title 200.
  expect((await day.api.session(n.long!)).name).toBe(LONG_NAME)
  expect([LONG_NAME.length, LONG_LABEL.length, LONG_TITLE.length]).toEqual([120, 60, 200])
  expect((await day.api.run(n['Long label']!) as { label?: string }).label).toBe(LONG_LABEL)
  expect((await day.api.ok<{ ticket: { title: string } }>('GET', '/api/dispatch/tickets/OPS-1')).ticket.title).toBe(LONG_TITLE)

  // busy: eighteen live sessions; and every built part reads as it promised.
  expect((await day.listSessions()).filter((s) => s.status === 'running')).toHaveLength(BUSY_LIVE)
  expect(await day.check()).toEqual([])
})

test("the day's states hold once a page of the Yard has attached every live session", async ({ day, browser }) => {
  await day.part('busy')
  const live = (await day.listSessions()).filter((s) => s.status === 'running')
  expect(live).toHaveLength(BUSY_LIVE)
  await dayPage(browser, { path: '/yard' })
  // Every live session has a viewer: the page's own attach, counted by the session (a state that holds, not one passed through).
  budget(DAY_STEP_MS)
  await expect
    .poll(async () => {
      const now = await day.listSessions()
      return live.filter((s) => !(now.find((x) => x.id === s.id)?.viewers ?? 0)).map((s) => s.name)
    }, { timeout: DAY_STEP_MS, message: 'every live session of the day is viewed' })
    .toEqual([])
  expect(await fiveStates(day)).toEqual(FIVE)
  expect(await day.check()).toEqual([])
})

test('the guard: what a test makes on the day is named until it is removed', async ({ day }) => {
  await day.part('states')
  const mine = await day.api.ok<Session>('POST', '/api/sessions', { agentId: 'plain', name: 'e2e day guard' })
  const named = async () => (await day.leftovers()).filter((l) => l.id === mine.id).map((l) => l.kind)
  expect(await named()).toEqual(['session'])
  // Stopped and ended, it is still named: an ended session stays listed on the day.
  await day.api.stopSession(mine.id)
  await expect.poll(() => day.statusOf(mine.id), { message: 'the guard session ends' }).toBe('stopped')
  expect(await named()).toEqual(['session'])
  // Removed with a second DELETE, it is not.
  await day.api.stopSession(mine.id)
  await expect.poll(() => day.statusOf(mine.id), { message: 'the guard session is removed' }).toBe('gone')
  expect(await named()).toEqual([])
})

test('dayPage opens the theme and the touch it is asked for', async ({ day, browser }) => {
  void day
  const light = await dayPage(browser, { theme: 'light' })
  await expect(light.locator('html')).not.toHaveClass(/\bdark\b/)
  const dark = await dayPage(browser, { theme: 'dark' })
  await expect(dark.locator('html')).toHaveClass(/\bdark\b/)
  const touch = () => [matchMedia('(pointer: coarse)').matches, navigator.maxTouchPoints > 0]
  const phone = await dayPage(browser, { width: 390, height: 844, touch: true })
  expect(await phone.evaluate(touch)).toEqual([true, true])
  const desk = await dayPage(browser, { width: 1440, height: 900 })
  expect(await desk.evaluate(touch)).toEqual([false, false])
})

// After a test that used the day, in the same worker: a page on the day is only for a test that asks for it.
test('dayPage in a test that did not ask for the day has no server, whatever ran before it', async ({ browser }) => {
  await expect(dayPage(browser, { path: '/' })).rejects.toThrow("dayPage: no server: ask for the `day` fixture (the day's server), or give `server`")
})

test("letGo ends the held turn of a session the test made, and the session is the test's to remove", async ({ day }) => {
  const mine = await day.api.ok<Session>('POST', '/api/sessions', { agentId: 'day-working', name: 'e2e let go' })
  budget(DAY_STEP_MS)
  await expect.poll(async () => (await day.api.session(mine.id)).attention?.state, { timeout: DAY_STEP_MS, message: 'the held turn shows' }).toBe('working')
  const s = await letGo(day.server, mine.id)
  expect([s.status, s.attention?.state, s.attention?.message]).toEqual(['running', 'done', 'a held turn, let go'])
  await day.api.stopSession(mine.id)
  await expect.poll(() => day.statusOf(mine.id), { message: 'the session ends' }).toBe('stopped')
  await day.api.stopSession(mine.id)
  await expect.poll(() => day.statusOf(mine.id), { message: 'the session is removed' }).toBe('gone')
})

// The guard's own proofs leave on a day what the shared day must never hold, so each runs on a day of its own, started for the test
// and ended after it: the shared day is never changed by them. Each leaves one thing and reads what the `day` fixture would judge
// at its end (guard), which must name it.
const own = test.extend<{ own: Day }>({
  // eslint-disable-next-line no-empty-pattern
  own: async ({}, use) => {
    budget(DAY_WORKER_MS)
    const d = await startDay('day-guard')
    try {
      await use(d)
    } finally {
      await d.end()
    }
  },
})

/** What the guard names on the day, one line each. */
async function judged(d: Day): Promise<string> {
  return (await d.guard()).problems.join('\n')
}

own('the guard names a Dispatch person and project a test made before Dispatch was built, a part built after them', async ({ own }) => {
  await own.api.ok('POST', '/api/dispatch/people', { opId: 'e2e-guard-person', name: 'Ana' })
  await own.api.ok('POST', '/api/dispatch/projects', { opId: 'e2e-guard-project', key: 'CND', name: 'Conductor', closeRequires: [] })
  await own.part('states')
  const words = await judged(own)
  expect(words).toContain("person Ana is not the day's")
  expect(words).toContain("project CND is not the day's")
})

own('the guard names a ticket made in another project than CND', async ({ own }) => {
  await own.part('dispatch')
  await own.api.ok('POST', '/api/dispatch/tickets', { opId: 'e2e-guard-ops', project: 'OPS', title: 'A ticket of the test', owner: 'Ana' })
  expect(await judged(own)).toContain("ticket OPS-1 is not the day's")
})

own("the guard names a day's ticket moved to another status", async ({ own }) => {
  await own.part('dispatch')
  const rev = (await own.api.ok<{ ticket: { rev: number } }>('GET', '/api/dispatch/tickets/CND-2')).ticket.rev
  await own.api.ok('POST', '/api/dispatch/tickets/CND-2/edit', { opId: 'e2e-guard-status', expectedRev: rev, set: { status: 'ready' } })
  expect(await judged(own)).toMatch(/ticket CND-2 is \{[^\n]*"status":"ready"[^\n]* where the day left \{[^\n]*"status":"backlog"/)
})

own('the guard names a label added to CND', async ({ own }) => {
  await own.part('dispatch')
  await own.api.ok('POST', '/api/dispatch/projects/CND/labels', { opId: 'e2e-guard-label', name: 'g14', color: 'red' })
  expect(await judged(own)).toContain("label CND/g14 is not the day's")
})

own("the guard names CND-1's approved plan withdrawn", async ({ own }) => {
  const plan = (await own.part('dispatch'))['plan:CND-1']!
  await own.api.ok('POST', `/api/dispatch/plans/${plan}/withdraw`, { opId: 'e2e-guard-withdraw' })
  const words = await judged(own)
  expect(words).toMatch(/plan CND-1\/1 is \{[^\n]*"withdrawn":true[^\n]* where the day left \{[^\n]*"withdrawn":false/)
  expect(words).toContain('dispatch: CND-1 revision 1 is withdrawn where approved, fresh was promised')
})

own('the guard names a crew made and not launched', async ({ own }) => {
  const body = { name: 'e2e guard crew', goal: 'g', cwd: '', where: 'server', isolation: 'none', openAfterLaunch: false, yolo: null, members: [{ name: 'a', agentId: 'plain', prompt: '', start: { when: 'manual' } }] }
  const id = (await own.api.ok<{ crew: { id: string } }>('POST', '/api/crews', body)).crew.id
  expect(await judged(own)).toContain(`crew e2e guard crew (${id}) is not the day's`)
})

own("the guard names a share link minted on a day's session", async ({ own }) => {
  const driver = (await own.part('states')).driver!
  await own.api.ok('POST', `/api/sessions/${driver}/links`, { role: 'view' })
  expect(await judged(own)).toMatch(new RegExp(`share link \\S+ of session driver \\(${driver}\\) is not the day's`))
})

own("the guard names a session started in one of the day's runs, whichever parts are built", async ({ own }) => {
  const run = (await own.part('long'))['Long label']!
  await own.api.ok('POST', `/api/runs/${run}/members/idle/start`)
  const { left } = await own.guard()
  expect(left.map((l) => l.words).join('\n')).toMatch(new RegExp(`session \\S+ \\(\\S+, running, of run ${run}\\)`))
})

own("the guard names a day's finished session marked working and set back by the operator: its attention whole is not the day's", async ({ own }) => {
  const finished = (await own.part('states'))['flaky-vim-spec']!
  await own.api.ok('POST', `/api/sessions/${finished}/attention`, { state: 'working' })
  expect(await judged(own)).toContain(`flaky-vim-spec (${finished}) is running with attention working where running with attention done was promised`)
  await own.api.ok('POST', `/api/sessions/${finished}/attention`, { state: 'done', message: 'set back' })
  // The state is the promised one again; the attention is the operator's, not the agent's the part saw.
  expect((await own.api.session(finished)).attention?.state).toBe('done')
  expect(await judged(own)).toMatch(new RegExp(`flaky-vim-spec \\(${finished}\\)'s attention is \\{"state":"done","source":"admin"[^\\n]*"message":"set back"[^\\n]* where the day saw \\{"state":"done","source":"api"`))
})

// To look at the day by hand: CONDUCTOR_E2E_DAY_OPEN=1 npx playwright test day.spec.ts -g open. It builds the whole day, prints its
// address and the path of a file only its owner reads that holds its token (never the token), and holds it until Ctrl+C.
test('open the day by hand', async ({ day }) => {
  test.skip(!process.env.CONDUCTOR_E2E_DAY_OPEN, 'only with CONDUCTOR_E2E_DAY_OPEN=1')
  await day.part('busy')
  const file = join(tmpdir(), `conductor-e2e-day-token-${process.pid}`)
  writeFileSync(file, day.server.started.token + '\n', { mode: 0o600 })
  console.log(`the day: ${day.server.started.baseURL} (its workbench token is in ${file}); Ctrl+C ends it`)
  test.setTimeout(0)
  await new Promise(() => {})
})
