import { writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { budget, dayPage, expect, task, test, type Session } from './fixtures'
import { BUSY_LIVE, DAY_STEP_MS, LONG_LABEL, LONG_NAME, LONG_TITLE, STATES, TICKETS, type Day } from './day'

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

test("the guard: what a test makes on the day is named until it is removed, and a day's item changed is named at the change", async ({ day }) => {
  const n = await day.part('states')
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

  // A day's session changed through the API is named by the reread; set back, it is not.
  const finished = n['flaky-vim-spec']!
  await day.api.ok('POST', `/api/sessions/${finished}/attention`, { state: 'working' })
  expect((await day.check()).join('\n')).toContain(`flaky-vim-spec (${finished}) is running with attention working where running with attention done was promised`)
  await day.api.ok('POST', `/api/sessions/${finished}/attention`, { state: 'done', message: 'set back' })
  await expect.poll(() => day.check(), { message: 'the day reads as promised again' }).toEqual([])
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
