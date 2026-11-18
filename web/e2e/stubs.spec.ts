import type { Page } from '@playwright/test'
import { dayPage, expect, test, type Run } from './fixtures'
import { inboxItem, stubInbox, type InboxKind, type ItemOver, type StubItem } from './inbox-stub'
import type { Day } from './day'
import { runInfo, serveRun } from './run-fixtures'

// The two browser stubs (inbox-stub.ts, run-fixtures.ts) held to the real server (UX1's PR-1c, docs/plans/ux-review.md, "Stubs in the
// browser"): what a stub answers carries the keys, and the kinds of value, of what the day's server answers for the same thing, read
// at the test's run, never copied. A field the server gains or drops, or one the stub makes up, fails here, naming it, not in a layout
// spec that would blame the layout. Every comparison also proves it can fail: one field taken out of the stub's answer is named.

/** Every key of a value, as `path: kind` (an array's elements under `[]`, their keys joined). */
function paths(v: unknown, at = '', out = new Set<string>()): Set<string> {
  const kind = v === null ? 'null' : Array.isArray(v) ? 'array' : typeof v
  if (at) out.add(`${at}: ${kind}`)
  if (Array.isArray(v)) for (const x of v) paths(x, `${at}[]`, out)
  else if (v && typeof v === 'object') for (const [k, x] of Object.entries(v)) paths(x, at ? `${at}.${k}` : k, out)
  return out
}

/** Whether a path is one of `drop`: a key the server sends only in a situation the stub is not in (`x`, and what is under it), or what
 * is under a key compared on its own (`x.*`). */
function dropped(path: string, drop: string[]): boolean {
  const name = path.slice(0, path.indexOf(': '))
  return drop.some((d) => {
    const under = d.endsWith('.*')
    const base = under ? d.slice(0, -2) : d
    return (!under && name === base) || name.startsWith(`${base}.`) || name.startsWith(`${base}[]`)
  })
}

function diff(stub: unknown, real: unknown, drop: string[]) {
  const s = paths(stub)
  const r = paths(real)
  return { missing: [...r].filter((p) => !s.has(p) && !dropped(p, drop)).sort(), extra: [...s].filter((p) => !r.has(p) && !dropped(p, drop)).sort() }
}

/**
 * The stub's answer has the keys of the server's, each with its kind of value, but for `drop`. Then the comparison proves it can
 * fail: the first key of the stub's answer it compares, taken out, is named as missing.
 */
function same(what: string, stub: Record<string, unknown>, real: Record<string, unknown>, drop: string[] = []): void {
  expect(diff(stub, real, drop), `${what}: the keys the server sends that the stub lacks (missing), and the stub's that it never sends (extra)`).toEqual({ missing: [], extra: [] })
  const key = Object.keys(stub).find((k) => k in real && !dropped(`${k}: x`, drop))
  expect(key, `${what}: a key to take out`).toBeTruthy()
  const cut = { ...stub }
  delete cut[key!]
  expect(diff(cut, real, drop).missing, `${what}: the stub's answer with ${key} taken out`).toContain([...paths({ [key!]: stub[key!] })][0])
}

/** Reads `path` as the page reads it (through its routes, then the server, with the workbench token): status and body. */
async function read(page: Page, path: string): Promise<{ status: number; body: Record<string, unknown> }> {
  return page.evaluate(async (p) => {
    const r = await fetch(p, { headers: { Authorization: `Bearer ${localStorage.getItem('conductor.workbenchToken') ?? ''}` } })
    return { status: r.status, body: (await r.json()) as Record<string, unknown> }
  }, path)
}

for (const tasks of [6, 40] as const) {
  test(`the run page drawn from serveRun with ${tasks} tasks shows every task, a failed task's Retry and Accept, and reads nothing of the run the stub does not answer`, async ({ day, browser }) => {
    const run = runInfo({ tasks })
    const page = await dayPage(browser, { path: '/' })
    await stubInbox(page, [])
    const served = await serveRun(page, run)
    await page.goto(`/runs/${run.id}?view=tasks`)
    await expect(page.locator('[data-crew-run-header]')).toBeVisible()
    for (const t of run.tasks) {
      const row = page.locator(`[data-task-row="${t.id}"]`)
      await expect(row, t.id).toHaveCount(1)
      await expect(row, t.id).toHaveAttribute('data-task-state', t.state)
    }
    // Every state at once (as many as the tasks hold), drawn: the stub's default.
    expect(new Set(run.tasks.map((t) => t.state)).size).toBe(Math.min(tasks, 7))
    // The engine's commands for a failed task, drawn as the row's buttons.
    for (const t of run.tasks.filter((x) => x.state === 'failed')) {
      await expect(page.locator(`[data-task-retry="${t.id}"]`), `${t.id}'s Retry`).toBeVisible()
      await expect(page.locator(`[data-task-accept="${t.id}"]`), `${t.id}'s Accept`).toBeVisible()
    }
    expect(served.answered).toContain(`GET /api/runs/${run.id}`)
    expect(served.unanswered.filter((r) => r.includes(run.id))).toEqual([])
    void day
  })
}

test("runInfo's run, its tasks in each state the day holds, its members and serveRun's reads carry the keys of the day's real runs", async ({ day, browser }) => {
  await day.part('waiting')
  const names = await day.part('runs')
  const api = day.api
  const real = (await api.ok<{ runs: Run[] }>('GET', '/api/runs')).runs
  expect(real.length, "the day's runs").toBeGreaterThan(0)
  // Every state at once, and a run whose members have not started.
  const run = runInfo({ tasks: 40 })
  const pending = runInfo({ tasks: 6, states: ['ready'] })
  const page = await dayPage(browser, { path: '/' })
  await serveRun(page, [run, pending])
  const stub = (path: string) => read(page, path).then((r) => r.body)
  const stubGet = await stub(`/api/runs/${run.id}`)
  const listed = (await stub('/api/runs')).runs as Array<Record<string, unknown>>
  expect(listed.map((r) => r.id)).toContain(run.id)
  // What a run carries only in a situation the stub's is not in: launched from a ticket, with a label, ended, with a chat.
  const situational = ['ticket', 'label', 'outcome', 'chat']
  const seen = new Set<string>()
  for (const r of real) {
    const get = await api.ok<Record<string, unknown>>('GET', `/api/runs/${r.id}`)
    const whole = get.run as Run & Record<string, unknown>
    if (!whole.managed) continue
    seen.add('run')
    same(`GET /api/runs/${r.name}`, stubGet, get, ['run.*', 'decisions'])
    same(`the run ${r.name}`, stubGet.run as Record<string, unknown>, whole, [...situational, 'tasks.*', 'members.*'])
    same(`the run ${r.name} as listed`, listed.find((x) => x.id === run.id)!, r as unknown as Record<string, unknown>, [...situational, 'tasks.*', 'members.*'])
    for (const t of whole.tasks ?? []) {
      const mine = run.tasks.find((x) => x.state === t.state)
      expect(mine, `the stub has a ${t.state} task`).toBeTruthy()
      seen.add(`task ${t.state}`)
      // Held by a decision, needing another task: the crew's and the run's own.
      same(`a ${t.state} task (${r.name}'s ${t.id})`, mine as unknown as Record<string, unknown>, t as unknown as Record<string, unknown>, ['holds', 'needs'])
      const readOf = await api.ok<Record<string, unknown>>('GET', `/api/runs/${r.id}/tasks/${t.id}`)
      same(`GET a ${t.state} task (${r.name}'s ${t.id})`, await stub(`/api/runs/${run.id}/tasks/${mine!.id}`), readOf, ['task', 'decisions'])
    }
    for (const m of whole.members) {
      const mine = [...run.members, ...pending.members].find((x) => x.status === m.status)
      expect(mine, `the stub has a ${m.status} member`).toBeTruthy()
      seen.add(`member ${m.status}`)
      // A member waiting on a question: the run's own.
      same(`a ${m.status} member (${r.name}'s ${m.name})`, mine as unknown as Record<string, unknown>, m as unknown as Record<string, unknown>, ['needsInput'])
    }
    // The candidates: a page, one task's, and a task's candidate (one that succeeded, as the stub's are).
    const cands = '[].candidates.*'
    const page1 = (await api.ok<Record<string, unknown>>('GET', `/api/runs/${r.id}/candidates`))
    const stubPage = await stub(`/api/runs/${run.id}/candidates`)
    same(`GET ${r.name}'s candidates`, stubPage, page1, ['next', `candidates${cands}`])
    const first = whole.tasks![0]!
    const like = run.tasks.find((x) => x.state === first.state)!
    same(`GET ${r.name}'s candidates of ${first.id}`, await stub(`/api/runs/${run.id}/candidates?task=${like.id}`), await api.ok<Record<string, unknown>>('GET', `/api/runs/${r.id}/candidates?task=${first.id}`), [`candidates${cands}`])
    type Cands = { candidates: Array<{ candidates: Array<Record<string, unknown>> }> }
    const theirs = (page1 as Cands).candidates.flatMap((c) => c.candidates).find((c) => c.state === 'succeeded')
    if (theirs) {
      seen.add('candidate')
      same(`a candidate of ${r.name}`, (stubPage as Cands).candidates.flatMap((c) => c.candidates)[0]!, theirs)
    }
    same(`GET ${r.name}'s links`, await stub(`/api/runs/${run.id}/links`), await api.ok<Record<string, unknown>>('GET', `/api/runs/${r.id}/links`))
    // What Brakeman said about the run, as the run page asks it (useBrakemanAbout): the stub's run has nothing said about it.
    const about = (id: string) => `/api/brakeman/requests?limit=50&about=${encodeURIComponent(`run:${id}`)}`
    same(`GET Brakeman's requests about ${r.name}`, await stub(about(run.id)), await api.ok<Record<string, unknown>>('GET', about(r.id)), ['requests.*'])
    // The ticket linked to a run that was not launched from one: none.
    if (!whole.ticket) {
      seen.add('no ticket')
      same(`GET the ticket linked to ${r.name}`, await stub(`/api/dispatch/links?run=${run.id}`), await api.ok<Record<string, unknown>>('GET', `/api/dispatch/links?run=${r.id}`))
    }
  }
  // The day holds these, so each was compared.
  for (const s of ['run', 'task succeeded', 'task running', 'task failed', 'task ready', 'task blocked', 'member running', 'member pending', 'candidate', 'no ticket']) expect([...seen], s).toContain(s)
  // A paging read the server refuses, refused as it refuses it.
  const ledger = names['Ledger migration']!
  const refused = await api.call<{ error: { code: string } }>('GET', `/api/runs/${ledger}/candidates?after=nope`)
  const mine = await read(page, `/api/runs/${run.id}/candidates?after=nope`)
  expect([mine.status, (mine.body.error as { code: string }).code]).toEqual([refused.status, refused.body.error.code])
})

/** The day's items, each with the stub's item of the same shape: the kind, and a plan's staleness or a decision's type and asker. */
async function pairs(day: Day) {
  const out: Array<{ real: { ref: string; kind: string }; whole: Record<string, unknown>; over: ItemOver; shape: string }> = []
  for (const it of await day.inbox()) {
    const whole = (await day.api.ok<{ item: Record<string, unknown> }>('GET', `/api/inbox/item?ref=${encodeURIComponent(it.ref)}`)).item
    let over: ItemOver = {}
    let shape = it.kind
    if (it.kind === 'plan' && 'now' in whole) {
      over = { stale: true }
      shape = 'stale plan'
    }
    if (it.kind === 'decision') {
      const d = (await day.api.ok<{ decision: { type: 'approval' | 'clarification'; origin: 'agent' | 'plan' } }>('GET', `/api/decisions/${it.ref.slice('decision:'.length)}`)).decision
      over = { type: d.type, origin: d.origin }
      shape = `${d.origin}'s ${d.type}`
    }
    out.push({ real: it, whole, over, shape })
  }
  return out
}

/** A receipt the server keeps none of, of the form its kind's track takes. */
const receiptFor = (it: { ref: string; kind: string }) => (it.kind === 'question' ? it.ref.split('/')[1]! : it.kind === 'decision' ? 'response:stubs-spec-1' : 'stubs-spec-1')

test("an inboxItem of each kind, listed, whole, its actions, its track and its decision's record carry the keys of the day's real ones", async ({ day, browser }) => {
  await day.part('waiting')
  await day.part('runs')
  const api = day.api
  const list = await api.ok<Record<string, unknown>>('GET', '/api/inbox')
  const real = await pairs(day)
  const stubs = new Map<string, StubItem>()
  for (const p of real) if (!stubs.has(p.shape)) stubs.set(p.shape, inboxItem(p.real.kind as InboxKind, p.over))
  for (const shape of ['question', 'plan', 'stale plan', 'task', "agent's approval", "agent's clarification", "plan's approval"]) expect([...stubs.keys()], `the day holds a ${shape}`).toContain(shape)
  const page = await dayPage(browser, { path: '/' })
  await stubInbox(page, [...stubs.values()])
  const stub = (path: string) => read(page, path).then((r) => r.body)
  const stubList = await stub('/api/inbox')
  same('GET /api/inbox', stubList, list, ['items.*', 'tracking', 'next', 'recent'])
  const listed = stubList.items as Array<Record<string, unknown>>
  for (const p of real) {
    const it = stubs.get(p.shape)!
    const what = `a ${p.shape} (${p.real.ref})`
    same(`${what} as listed`, listed.find((x) => x.ref === it.ref)!, p.real)
    const whole = (await stub(`/api/inbox/item?ref=${encodeURIComponent(it.ref)}`)).item as Record<string, unknown>
    same(`${what} whole`, whole, p.whole, ['actions.*'])
    // Each kind of action the server offers (by its name, and whether a line is typed), the stub offers, with its keys; and no other.
    const role = (a: Record<string, unknown>) => `${a.name as string}${a.text ? ' (a line)' : ''}`
    const mine = whole.actions as Array<Record<string, unknown>>
    const theirs = p.whole.actions as Array<Record<string, unknown>>
    expect([...new Set(mine.map(role))].sort(), `${what}'s actions`).toEqual([...new Set(theirs.map(role))].sort())
    for (const a of theirs) same(`${what}'s action ${role(a)}`, mine.find((x) => role(x) === role(a))!, a)
    const track = await api.call<Record<string, unknown>>('GET', `/api/inbox/track?ref=${encodeURIComponent(p.real.ref)}&receipt=${encodeURIComponent(receiptFor(p.real))}`)
    expect(track.status, `${what}'s track`).toBe(200)
    same(`${what}'s track`, await stub(`/api/inbox/track?ref=${encodeURIComponent(it.ref)}&receipt=${encodeURIComponent(receiptFor(it))}`), track.body)
  }
  // A decision's record, listed and whole, the server's but for what its asker chose to say and the ticket a stub run has none of.
  const asked = ['ticket', 'plan', 'planDigest', 'request.context', 'request.recommendation', 'request.uncertainty', 'request.authorizes', 'request.options[].tradeoffs', 'request.subject.*', 'history[].detail.*']
  const records = (await api.ok<{ decisions: Array<Record<string, unknown>> }>('GET', '/api/decisions')).decisions
  const stubRecords = (await stub('/api/decisions')).decisions as Array<Record<string, unknown>>
  for (const p of real.filter((x) => x.real.kind === 'decision')) {
    const id = p.real.ref.slice('decision:'.length)
    const it = stubs.get(p.shape)!
    const mineId = it.ref.slice('decision:'.length)
    same(`the record of ${p.shape} ${id} as listed`, stubRecords.find((d) => d.id === mineId)!, records.find((d) => d.id === id)!, asked)
    const whole = await api.ok<Record<string, unknown>>('GET', `/api/decisions/${id}`)
    const stubWhole = await stub(`/api/decisions/${mineId}`)
    same(`GET /api/decisions/${id}`, stubWhole, whole, ['decision.*'])
    same(`the record of ${p.shape} ${id}`, stubWhole.decision as Record<string, unknown>, whole.decision as Record<string, unknown>, asked)
  }
  // A decision record's run is a run runInfo makes, and its asker a task of it.
  for (const d of stubRecords) {
    const it = [...stubs.values()].find((x) => x.ref === `decision:${d.id as string}`)!
    expect(d.run, `${d.id as string}'s run`).toBe(it.run!.id)
    if (d.origin === 'agent') expect(it.run!.tasks.map((t) => t.id)).toContain((d.asker as { task: string }).task)
  }
})

test('the inbox stub keeps the server’s bounds, its order and its refusals, and leaves to the server what it does not hold', async ({ day, browser }) => {
  // Each bound, as the server keeps it (internal/api/inbox.go; held here, not read from the stub, so that a stub's bound that drifts
  // fails): at it, the item is made; one past it, refused naming the field.
  const at = (n: number) => 'x'.repeat(n)
  const cases: Array<[string, (n: number) => ItemOver, number]> = [
    ['title', (n) => ({ title: at(n) }), 200],
    ['brief', (n) => ({ brief: at(n) }), 2048],
    ['question', (n) => ({ question: at(n) }), 500],
    ['answerer', (n) => ({ answerer: at(n) }), 120],
    ['ref', (n) => ({ ref: `question:${at(n - 'question:/0'.length)}/0` }), 512],
    ['action label', (n) => ({ choices: [at(n)] }), 80],
    ['actions', (n) => ({ choices: Array.from({ length: n - 1 }, (_, i) => `c${i}`) }), 10],
  ]
  for (const [field, over, bound] of cases) {
    expect(() => inboxItem('question', over(bound)), `${field} at its bound`).not.toThrow()
    expect(() => inboxItem('question', over(bound + 1)), `${field} past its bound`).toThrow(field === 'actions' ? /actions are 11/ : new RegExp(`${field}.* is ${bound + 1} bytes`))
  }
  // The server's own encoding: '<' takes six bytes, so 40 of them are past a title's 200.
  expect(() => inboxItem('question', { title: '<'.repeat(40) })).toThrow(/title is 240 bytes/)
  const page = await dayPage(browser, { path: '/' })
  // A list past the server's page, refused.
  const many = Array.from({ length: 151 }, () => inboxItem('question'))
  await expect(stubInbox(page, many)).rejects.toThrow(/151 items, past the server's page of 150/)
  // Oldest first, by (since, ref), however given; the list's brief cut as the server cuts it, the item's whole.
  const t = (s: number) => new Date(Date.now() - s * 60_000).toISOString()
  const a = inboxItem('question', { since: t(3), ref: 'question:00000000000000aa/0000000000000001' })
  const b = inboxItem('question', { since: t(3), ref: 'question:00000000000000ab/0000000000000001' })
  const c = inboxItem('decision', { since: t(9), brief: 'é'.repeat(150) })
  const inbox = await stubInbox(page, [b, a, c])
  const listed = (await read(page, '/api/inbox')).body.items as Array<{ ref: string; brief: string }>
  expect(listed.map((x) => x.ref)).toEqual([c.ref, a.ref, b.ref])
  expect(listed[0]!.brief, 'cut at a character, within 280 bytes').toBe(`${'é'.repeat(138)}…`)
  expect(((await read(page, `/api/inbox/item?ref=${encodeURIComponent(c.ref)}`)).body.item as { brief: string }).brief).toBe(c.brief)
  expect(() => inbox.set(many)).toThrow(/past the server's page/)
  // A receipt the server would refuse, refused as it refuses it (the day's decision answers for the server).
  await day.part('waiting')
  const gate = (await day.inbox()).find((x) => x.kind === 'decision')!
  const server = await day.api.call<{ error: { code: string } }>('GET', `/api/inbox/track?ref=${encodeURIComponent(gate.ref)}&receipt=nope`)
  const mine = await read(page, `/api/inbox/track?ref=${encodeURIComponent(c.ref)}&receipt=nope`)
  expect([mine.status, (mine.body.error as { code: string }).code]).toEqual([server.status, server.body.error.code])
  // A decision and a track the stub does not hold: recorded, and answered by the server, as it answers them.
  const gone = await read(page, '/api/decisions/dr-99999')
  const truth = await day.api.call<{ error: { code: string } }>('GET', '/api/decisions/dr-99999')
  expect([gone.status, (gone.body.error as { code: string }).code]).toEqual([truth.status, truth.body.error.code])
  await read(page, `/api/inbox/track?ref=${encodeURIComponent(gate.ref)}&receipt=response%3Astubs-spec-2`)
  expect(inbox.unanswered).toEqual(expect.arrayContaining(['/api/decisions/dr-99999', `/api/inbox/track?ref=${encodeURIComponent(gate.ref)}&receipt=response%3Astubs-spec-2`]))
})
