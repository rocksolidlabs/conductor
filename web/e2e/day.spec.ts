import { existsSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { Api, budget, DAY_WORKER_MS, dayPage, expect, task, test, type Session } from './fixtures'
import { BUSY_LIVE, DAY_STEP_MS, letGo, LONG_LABEL, LONG_NAME, LONG_TITLE, SNAPSHOT_KINDS, startDay, STATES, TICKETS, type Day, type SnapshotKind } from './day'
import { ownServer, type OwnServer } from './own-server'
import { stopServer } from './server'

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

/** The five sessions of `states` as the server lists them: name, status, attention and whether the agent reports its turns. */
async function fiveStates(day: Day): Promise<Array<[string, string, string, boolean]>> {
  const n = await day.part('states')
  const out: Array<[string, string, string, boolean]> = []
  for (const name of Object.keys(STATES)) {
    const s = await day.api.session(n[name]!)
    out.push([s.name, s.status, s.attention?.state ?? '', s.turns === true])
  }
  return out
}

// The listing's turns is what the status walk reads: working and finished are told by an agent that reports its turns.
const FIVE: Array<[string, string, string, boolean]> = [
  ['mac-only-flake', 'running', 'needs_input', false],
  ['yard-tile-resize', 'running', 'working', true],
  ['flaky-vim-spec', 'running', 'done', true],
  ['driver', 'running', '', false],
  ['old-bisect', 'stopped', '', false],
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

  // busy: eighteen live sessions; and the server lists what the builds left, each item of a kind the guard knows.
  expect((await day.listSessions()).filter((s) => s.status === 'running')).toHaveLength(BUSY_LIVE)
  expect([...day.base.keys()].filter((k) => !SNAPSHOT_KINDS.some((kind) => k.startsWith(`${kind} `)))).toEqual([])
  expect(await day.drift()).toEqual([])
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
  expect(await day.drift()).toEqual([])
})

test('the guard: what a test makes on the day is named until it is removed', async ({ day }) => {
  await day.part('states')
  const mine = await day.api.ok<Session>('POST', '/api/sessions', { agentId: 'plain', name: 'e2e day guard' })
  const named = async () => (await day.guard()).left.filter((l) => l.id === mine.id).map((l) => l.kind)
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
// and ended after it: the shared day is never changed by them. Each changes what the server lists and reads what the `day` fixture
// would judge at its end (guard), which must name each change: what changed, from what to what. A session's agent and folder are
// compared too, but no API changes them, so no test does.
const own = test.extend<{ own: Day }>({
  // eslint-disable-next-line no-empty-pattern
  own: async ({}, use) => {
    budget(DAY_WORKER_MS)
    // Named for the test and its repetition: its server's log is kept in test-results under that name.
    const d = await startDay(`day-guard-${test.info().testId}-${test.info().repeatEachIndex}`)
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
  expect(words).toContain("project CND (Conductor) is not the day's")
})

own('the guard names a ticket made in another project than CND', async ({ own }) => {
  await own.part('dispatch')
  await own.api.ok('POST', '/api/dispatch/tickets', { opId: 'e2e-guard-ops', project: 'OPS', title: 'A ticket of the test', owner: 'Ana' })
  expect(await judged(own)).toContain("ticket OPS-1 is not the day's")
})

own("the guard names the day's Long label run stopped, its member never started", async ({ own }) => {
  const run = (await own.part('long'))['Long label']!
  await own.api.stopRun(run)
  const state = (await own.runUntil(run, 'Long label stops', (r) => r.state !== 'running')).state
  expect(await judged(own)).toContain(`run ${run} (Long label): state is "${state}" where the day left "running"`)
})

own("the guard names Ledger migration's member session renamed, and re-marked working by the operator", async ({ own }) => {
  const run = (await own.part('runs'))['Ledger migration']!
  const id = (await own.api.run(run)).members.find((m) => m.name === 'migrator')!.sessionId!
  const name = (await own.api.session(id)).name
  await own.api.ok('PATCH', `/api/sessions/${id}`, { name: 'e2e renamed' })
  expect(await judged(own)).toContain(`session ${id} (${name}): name is "e2e renamed" where the day left ${JSON.stringify(name)}`)
  await own.api.ok('PATCH', `/api/sessions/${id}`, { name })
  expect(await judged(own)).not.toContain(`session ${id}`)
  // Working still, as the day left it, but by another hand: the attention's source and message are not the day's.
  await own.api.ok('POST', `/api/sessions/${id}/attention`, { state: 'working', message: 'by hand' })
  const words = await judged(own)
  expect(words).toContain(`session ${id} (${name}): attention.source is "admin" where the day left "api"`)
  expect(words).not.toContain(`session ${id} (${name}): attention.state`)
})

own("the guard names a day's session renamed", async ({ own }) => {
  const id = (await own.part('states')).driver!
  await own.api.ok('PATCH', `/api/sessions/${id}`, { name: 'e2e driver' })
  expect(await judged(own)).toContain(`session ${id} (driver): name is "e2e driver" where the day left "driver"`)
})

own("the guard names a day's asking session stopped, and its question gone from the inbox", async ({ own }) => {
  const id = (await own.part('states'))['mac-only-flake']!
  const ref = [...own.refs.keys()].find((r) => r.startsWith(`question:${id}/`))!
  await own.api.stopSession(id)
  await expect.poll(() => own.statusOf(id), { message: 'mac-only-flake ends' }).toBe('stopped')
  const words = await judged(own)
  expect(words).toContain(`session ${id} (mac-only-flake): status is "stopped" where the day left "running"`)
  expect(words).toContain(`inbox ${ref} is gone`)
})

own("the guard names a session started in one of the day's runs, whichever parts are built", async ({ own }) => {
  const run = (await own.part('long'))['Long label']!
  await own.api.ok('POST', `/api/runs/${run}/members/idle/start`)
  await own.runUntil(run, "Long label's idle runs", (r) => r.members.find((m) => m.name === 'idle')?.status === 'running')
  const { left, problems } = await own.guard()
  expect(left.map((l) => l.kind)).toEqual(['session'])
  expect(problems.join('\n')).toMatch(new RegExp(`session ${left[0]!.id} \\([^\\n]*\\) is not the day's: [^\\n]*"run":"${run}","member":"idle"`))
  expect(problems).toContain(`run ${run} (Long label): members.idle.status is "running" where the day left "pending"`)
})

own("the guard names a task's decision answered", async ({ own }) => {
  const n = await own.part('waiting')
  const [run, gate] = [n['Pairing rollout']!, n.gate!]
  const rev = (await own.api.ok<{ decision: { rev: number } }>('GET', `/api/decisions/${gate}`)).decision.rev
  await own.api.ok('POST', `/api/decisions/${gate}/respond`, { responseId: 'e2e-guard-gate', choice: 0, expectedRev: rev })
  await own.runUntil(run, 'the gate is answered', (r) => !r.tasks?.find((t) => t.id === 'ship')?.holds?.some((h) => h.id === gate && h.state === 'open'))
  const words = await judged(own)
  expect(words).toMatch(new RegExp(`run ${run} \\(Pairing rollout\\): tasks\\.ship\\.holds\\.${gate} is \\S+ where the day left "open"`))
  expect(words).toContain(`inbox decision:${gate} is gone`)
})

own("the guard names a day's finished session marked working and set back by the operator: its attention whole is not the day's", async ({ own }) => {
  const id = (await own.part('states'))['flaky-vim-spec']!
  await own.api.ok('POST', `/api/sessions/${id}/attention`, { state: 'working' })
  expect(await judged(own)).toContain(`session ${id} (flaky-vim-spec): attention.state is "working" where the day left "done"`)
  await own.api.ok('POST', `/api/sessions/${id}/attention`, { state: 'done', message: 'set back' })
  // The state is the promised one again; the attention is the operator's, not the agent's the part saw.
  const words = await judged(own)
  expect(words).not.toContain(`session ${id} (flaky-vim-spec): attention.state`)
  expect(words).toContain(`session ${id} (flaky-vim-spec): attention.source is "admin" where the day left "api"`)
  expect(words).toContain(`session ${id} (flaky-vim-spec): attention.message is "set back" where the day left "a turn of its own, finished"`)
})

/** One change of each kind the snapshot holds, made through the API on a day with `states` and `long` built; what the guard says of it. */
const CHANGES: Record<SnapshotKind, (d: Day) => Promise<string>> = {
  person: async (d) => (await d.api.ok('POST', '/api/dispatch/people', { opId: 'e2e-t-person', name: 'Bo' }), "person Bo is not the day's"),
  project: async (d) => (await d.api.ok('POST', '/api/dispatch/projects', { opId: 'e2e-t-project', key: 'EXP', name: 'Experiments', closeRequires: [] }), "project EXP (Experiments) is not the day's"),
  label: async (d) => (await d.api.ok('POST', '/api/dispatch/projects/CND/labels', { opId: 'e2e-t-label', name: 'g14', color: 'red' }), "label CND/g14 is not the day's"),
  milestone: async (d) => (await d.api.ok('POST', '/api/dispatch/projects/CND/milestones', { opId: 'e2e-t-milestone', name: 'v3', target: '2026-12-09' }), "milestone CND/v3 is not the day's"),
  view: async (d) => `view e2e view (${(await d.api.ok<{ view: { id: string } }>('POST', '/api/dispatch/views', { opId: 'e2e-t-view', name: 'e2e view', kind: 'list', query: { status: ['ready'] } })).view.id}) is not the day's`,
  ticket: async (d) => {
    const rev = (await d.api.ok<{ ticket: { rev: number } }>('GET', '/api/dispatch/tickets/CND-2')).ticket.rev
    await d.api.ok('POST', '/api/dispatch/tickets/CND-2/edit', { opId: 'e2e-t-ticket', expectedRev: rev, set: { status: 'ready' } })
    return 'ticket CND-2: status is "ready" where the day left "backlog"'
  },
  plan: async (d) => {
    await d.api.ok('POST', `/api/dispatch/plans/${(await d.part('dispatch'))['plan:CND-1']}/withdraw`, { opId: 'e2e-t-plan' })
    return 'plan CND-1/1: withdrawn is true where the day left false'
  },
  crew: async (d) => {
    const body = { name: 'e2e guard crew', goal: 'g', cwd: '', where: 'server', isolation: 'none', openAfterLaunch: false, yolo: null, members: [{ name: 'a', agentId: 'plain', prompt: '', start: { when: 'manual' } }] }
    return `crew e2e guard crew (${(await d.api.ok<{ crew: { id: string } }>('POST', '/api/crews', body)).crew.id}) is not the day's`
  },
  'share link': async (d) => {
    const driver = (await d.part('states')).driver!
    return `share link ${(await d.api.ok<{ link: { id: string } }>('POST', `/api/sessions/${driver}/links`, { role: 'view' })).link.id} of session ${driver} is not the day's`
  },
  session: async (d) => {
    const id = (await d.part('states'))['flaky-vim-spec']!
    await d.api.ok('PATCH', `/api/sessions/${id}`, { name: 'e2e renamed' })
    return `session ${id} (flaky-vim-spec): name is "e2e renamed" where the day left "flaky-vim-spec"`
  },
  run: async (d) => {
    const run = (await d.part('long'))['Long label']!
    await d.api.stopRun(run)
    return `run ${run} (Long label): state is "${(await d.runUntil(run, 'Long label stops', (r) => r.state !== 'running')).state}" where the day left "running"`
  },
  inbox: async (d) => {
    const id = (await d.part('states'))['mac-only-flake']!
    await d.api.stopSession(id)
    await expect.poll(() => d.statusOf(id), { message: 'mac-only-flake ends' }).toBe('stopped')
    return `inbox ${[...d.refs.keys()].find((r) => r.startsWith(`question:${id}/`))} is gone`
  },
}

own('the guard is one rule: a change of each kind the server lists is named, and every kind has its change here', async ({ own }) => {
  expect(Object.keys(CHANGES).sort()).toEqual([...SNAPSHOT_KINDS].sort())
  await own.part('long')
  await own.part('states')
  const want: Array<[string, string]> = []
  for (const [kind, change] of Object.entries(CHANGES)) want.push([kind, await change(own)])
  const words = await judged(own)
  for (const [kind, line] of want) expect(words, kind).toContain(line)
})

// What startDay and ownServer start is ended when they return no day and no server (AGENTS.md, end what a test started before its
// directories go): no process of theirs left, their root removed, the error saying what failed.
function alive(pid: number): boolean {
  try {
    process.kill(pid, 0)
    return true
  } catch {
    return false
  }
}

test("a day whose first snapshot fails is ended: its server's process gone, its root removed", async () => {
  budget(DAY_WORKER_MS)
  let server: OwnServer | undefined
  // The seam: once the server answers, its API refuses the first snapshot's first read, as a route of the test's own.
  const refuse = (s: OwnServer) => {
    server = s
    s.api = new (class extends Api {
      override async call<T>(method: string, path: string, body?: unknown): Promise<{ status: number; body: T }> {
        if (path === '/api/dispatch/people') return { status: 503, body: { error: 'refused by the test' } as unknown as T }
        return super.call<T>(method, path, body)
      }
    })(s.started)
  }
  try {
    const started = startDay(`day-start-fails-${test.info().repeatEachIndex}`, refuse)
    await expect(started).rejects.toThrow(/did not start: its first snapshot failed: GET \/api\/dispatch\/people: 503 .*refused by the test.* \(it was ended\)$/)
    expect(alive(server!.started.pid), "the day's server is still running").toBe(false)
    expect(existsSync(server!.root), "the day's root is still there").toBe(false)
  } finally {
    // What the product left is ended here, so that this test's failure leaves nothing either.
    if (server && existsSync(server.root)) {
      await stopServer(server.started.pid)
      rmSync(server.root, { recursive: true, force: true })
    }
  }
})

test('a server of its own whose start fails leaves no root', async () => {
  const name = `own-start-fails-${process.pid}-${test.info().repeatEachIndex}`
  await expect(ownServer(name, { agents: [{ id: 'nowhere', from: 'no-such-agent', env: {} }] })).rejects.toThrow('the e2e catalog has no agent no-such-agent')
  expect(readdirSync(tmpdir()).filter((f) => f.startsWith(`conductor-e2e-${name}-`))).toEqual([])
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
