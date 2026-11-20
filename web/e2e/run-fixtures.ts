import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Page, Route } from '@playwright/test'
import type { Run, RunAttempt, RunMember, RunTask } from './fixtures'
import { repoRoot } from './server'

// A managed run answered in the browser (UX1's PR-1, docs/plans/ux-review.md, "Stubs in the browser"): the shapes the day cannot make
// cheaply (a run of 40 or 128 tasks, every task state at once, a task checking, which the day cannot hold: a check's hold is capped
// at 3,600 s). A stub proves how a shape is drawn, never that the server sends it. It answers as the server does: each task carries
// the commands the engine gives its state, a member with an attempt runs with its session, an attempt says where it ran, and the
// run carries what the server's does. stubs.spec.ts holds every key of it to the day's real runs, so that a field the server gains,
// or a read the run page gains, fails there, not in a layout spec. Modelled on GET /api/runs/{run} and its reads (internal/api,
// internal/crew).

/** Every state a task of a managed run can be in (internal/crew/ledger/rows.go); `verifying` is a task checking. Each blocked task of a
 * default run needs the ready one before it, so the order keeps a blocked task from being first. */
export const TASK_STATES = ['succeeded', 'running', 'verifying', 'failed', 'cancelled', 'ready', 'blocked'] as const
export type TaskState = (typeof TASK_STATES)[number]

/** An attempt as the run's read gives it, with when and where it ran. */
export type StubAttempt = RunAttempt & { startedAt: string; endedAt?: string; where: string }

/** A task as the run's read gives it, with the commands the engine allows it now. */
export type StubTask = RunTask & { commands?: string[]; attempt?: StubAttempt }

/** A member as the run's read gives it: one with an attempt runs it in its session. */
export type StubMember = RunMember & {
  start: { when: string }
  task?: string
  attempt?: number
  worktree?: string
  base?: string
  agentSession?: { id: string; resumable: boolean; source: string }
  diff?: { added: number; removed: number }
}

/** The run, as GET /api/runs/{run} gives it. */
export type StubRun = Omit<Run, 'tasks' | 'members'> & {
  label: string
  goal: string
  cwd: string
  isolation: string
  startedAt: string
  yolo: boolean
  needsInput: number
  managed: { revision: number }
  members: StubMember[]
  tasks: StubTask[]
}

/**
 * The engine's table of the commands a task's state allows (internal/crew/testdata/task-commands.json, which the engine's
 * TaskCommands and the app's taskActions are held to): for each state, its first row in a live run, with the attempts that row
 * names (each of which submitted a commit, or not).
 */
const COMMAND_ROWS = (
  JSON.parse(readFileSync(join(repoRoot, 'internal/crew/testdata/task-commands.json'), 'utf8')) as {
    rows: Array<{ state: string; kind: string; run: string; attempts: Array<{ commit: string }>; commands: string[] }>
  }
).rows

function commandRow(state: TaskState) {
  const row = COMMAND_ROWS.find((r) => r.state === state && r.run === 'live' && r.kind === 'work')
  if (!row) throw new Error(`run-fixtures: the engine's table has no live row for a ${state} task`)
  return row
}

let made = 0
const MEMBERS = ['builder', 'reviewer', 'writer', 'tester']
const COMMIT = (n: number) => n.toString(16).padStart(40, 'a')
const hex = (n: number, width: number) => n.toString(16).padStart(width, '0')

/** One task of `id` in `state` of the run `run`, with what a task in that state carries: its attempts as the engine's table names
 * them (each ended attempt with its times, the last with its session, branch and worktree), its result, its reason, its commands. */
function stubTask(run: string, n: number, id: string, i: number, state: TaskState, prev?: string): StubTask {
  const member = MEMBERS[i % MEMBERS.length]!
  const row = commandRow(state)
  const base: StubTask = { id, member, kind: 'work', state, attempts: row.attempts.length }
  const commands = row.commands.length ? { commands: [...row.commands] } : {}
  const started = new Date(Date.now() - (10 - (i % 5)) * 60_000).toISOString()
  const ended = new Date(Date.now() - (5 - (i % 5)) * 60_000).toISOString()
  const last = row.attempts.length
  const attempt = (s: string, done: boolean): StubAttempt => ({
    n: last,
    sessionId: hex(n * 0x10000 + i * 16 + last, 16),
    worktree: `/stub/.conductor/worktrees/${run}/${id}.${last}`,
    branch: `crew/${run}/${id}.${last}`,
    start: COMMIT(1),
    ...(row.attempts[last - 1]?.commit ? { result: COMMIT(i + 2) } : {}),
    state: s,
    startedAt: started,
    ...(done ? { endedAt: ended } : {}),
    where: 'native',
  })
  switch (state) {
    case 'blocked':
      return { ...base, needs: prev ? [prev] : [], reason: prev ? `needs ${prev}` : 'needs nothing yet', ...commands }
    case 'ready':
      return { ...base, reason: `${id} needs ${member}`, ...commands }
    case 'running':
      return { ...base, attempt: attempt('running', false), ...commands }
    case 'verifying':
      return { ...base, attempt: { ...attempt('verifying', false), stage: 'check never' }, ...commands }
    case 'succeeded':
      return { ...base, attempt: attempt('succeeded', true), result: { attempt: last, commit: COMMIT(i + 2), summary: `${id} done`, issues: ['none'], at: ended }, ...commands }
    case 'failed':
      return { ...base, reason: `attempt ${last} failed at check never: exited 1`, attempt: { ...attempt('failed', true), stage: 'check never', error: 'exited 1' }, ...commands }
    case 'cancelled':
      return { ...base, reason: 'cancelled', attempt: attempt('cancelled', true), ...commands }
  }
}

/** The run's members: one that has worked an attempt runs in that attempt's session (the latest of a running or checking task, else
 * its latest), with its branch and the agent's own session; one that has not is pending, started by its task. */
function stubMembers(run: string, tasks: StubTask[]): StubMember[] {
  return MEMBERS.map((name, k) => {
    const mine = tasks.filter((t) => t.member === name && t.attempt)
    const at = mine.filter((t) => t.state === 'running' || t.state === 'verifying').at(-1) ?? mine.at(-1)
    const m: StubMember = { name, agentId: 'worker-cli', start: { when: 'task' }, status: 'pending' }
    if (!at?.attempt) return m
    const a = at.attempt
    return {
      ...m,
      sessionId: a.sessionId,
      branch: a.branch,
      worktree: a.worktree,
      status: 'running',
      startedAt: a.startedAt,
      task: at.id,
      attempt: a.n,
      agentSession: { id: `${hex(k + 1, 8)}-0000-4000-8000-${hex(made, 12)}`, resumable: false, source: 'set' },
      base: COMMIT(1),
      diff: { added: 0, removed: 0 },
    }
  })
}

/**
 * A managed run of `tasks` tasks (6, 40 or 128: a crew's bound), each in its state from `states` in turn (every state at once, in
 * TASK_STATES' order, when not given), ids t01, t02…, its members four stubs of the catalog's worker-cli. `sessions` names, by task id,
 * the session its attempt runs in (a session the day holds, so that the task's state is that session's); a task not named runs in a
 * session of its own that no server holds. Its id is a run id of its own. Nothing of it is on the server: serveRun answers its reads.
 */
export function runInfo(o: { tasks: 6 | 40 | 128; states?: TaskState[]; sessions?: Record<string, string> }): StubRun {
  const n = ++made
  const states = o.states ?? [...TASK_STATES]
  const width = String(o.tasks).length
  const id = `stub-run-${(0x1000_0000 + n).toString(16)}`
  const ids = Array.from({ length: o.tasks }, (_, i) => `t${String(i + 1).padStart(Math.max(2, width), '0')}`)
  const tasks = ids.map((t, i) => stubTask(id, n, t, i, states[i % states.length]!, ids[i - 1]))
  for (const t of tasks) if (o.sessions?.[t.id] && t.attempt) t.attempt.sessionId = o.sessions[t.id]
  const name = `Stub run of ${o.tasks}`
  return {
    id,
    crewId: 'stub-run',
    name,
    label: name,
    goal: 'draw every task',
    cwd: '/stub',
    isolation: 'worktree',
    startedAt: new Date(Date.now() - 15 * 60_000).toISOString(),
    yolo: false,
    state: 'running',
    needsInput: 0,
    members: stubMembers(id, tasks),
    log: [{ at: new Date().toISOString(), type: 'status', message: `launched ${name}: ${MEMBERS.length} members, 0 starting now` }],
    managed: { revision: 1 },
    tasks,
  }
}

/** A task of a stub run named `id` in `state`, made as runInfo makes its tasks, with what it needs and who owns its outcome. */
function namedTask(run: string, n: number, i: number, id: string, state: TaskState, o: { needs?: string[]; owner?: string; ownerFrom?: string; attempt?: number; session?: string } = {}): StubTask {
  const t = stubTask(run, n, id, i, state)
  if (o.needs?.length) t.needs = o.needs
  else delete t.needs
  if (state === 'blocked') t.reason = `needs ${o.needs?.join(', ') ?? 'nothing yet'}`
  if (o.owner) Object.assign(t, { owner: o.owner, ownerFrom: o.ownerFrom ?? 'acceptance' })
  if (o.attempt && t.attempt && o.attempt !== t.attempt.n) {
    t.attempt = { ...t.attempt, n: o.attempt, branch: `crew/${run}/${id}.${o.attempt}`, worktree: `/stub/.conductor/worktrees/${run}/${id}.${o.attempt}` }
    t.attempts = o.attempt
  }
  if (o.session && t.attempt) t.attempt.sessionId = o.session
  return t
}

/** The decision that holds the Ledger run's release-notes, by default. */
export const LEDGER_HOLD = 'dr-990'

/**
 * The Ledger run: six tasks in this order: schema (done); naming (done) and backfill (checking, on attempt 2), which need schema; soak
 * (running on attempt 1, its attempt's session `soak`, a session the spec starts), which needs nothing; migrate (failed out of its
 * attempts, Retry and Accept named) and release-notes (ready, held by the open decision `hold`), which need naming. Every task's
 * outcome is ana's as the acceptance owner but migrate's, which the task names nate. `name` is the crew's (60 characters at most),
 * `startedAt` when it started (now, when not given). Shared by the run header's, the task rows' and the line diagram's specs.
 */
export function ledgerRun(o: { name?: string; startedAt?: string; soak?: string; hold?: { id: string; question: string } } = {}): StubRun {
  const run = runInfo({ tasks: 6 })
  const n = made
  const own = { owner: 'ana', ownerFrom: 'acceptance' }
  // An id no server of the suite issues (they count from dr-1): a page on the seeded day lists the day's own decisions beside the stub's.
  const hold = o.hold ?? { id: LEDGER_HOLD, question: 'May release-notes ship?' }
  const notes = namedTask(run.id, n, 5, 'release-notes', 'ready', { needs: ['naming'], ...own })
  notes.reason = `waits for decision ${hold.id}`
  notes.holds = [{ id: hold.id, type: 'approval', state: 'open', question: hold.question }]
  run.tasks = [
    namedTask(run.id, n, 0, 'schema', 'succeeded', own),
    namedTask(run.id, n, 1, 'naming', 'succeeded', { needs: ['schema'], ...own }),
    namedTask(run.id, n, 2, 'backfill', 'verifying', { needs: ['schema'], attempt: 2, ...own }),
    namedTask(run.id, n, 3, 'soak', 'running', { ...own, ...(o.soak ? { session: o.soak } : {}) }),
    namedTask(run.id, n, 4, 'migrate', 'failed', { needs: ['naming'], owner: 'nate', ownerFrom: 'task' }),
    notes,
  ]
  run.people = [
    { name: 'ana', responsibilities: ['acceptance'] },
    { name: 'nate', responsibilities: ['architecture'] },
  ]
  run.name = o.name ?? 'Ledger migration'
  run.label = run.name
  run.startedAt = o.startedAt ?? new Date().toISOString()
  run.members = stubMembers(run.id, run.tasks)
  return run
}

/**
 * A run of members and no tasks (a crew without tasks, launched by hand): four members of the catalog's worker-cli, each running in a
 * session of its own. Options: `name`, `paused` with `pausedWhy`, `record` (a run the server no longer holds, read-only), `startedAt`.
 * Add agent is offered on it (runOffer), as on any live run of members.
 */
export function legacyRun(o: { name?: string; record?: boolean; startedAt?: string } = {}): StubRun {
  const n = ++made
  const id = `stub-crew-${(0x2000_0000 + n).toString(16)}`
  const name = o.name ?? 'Stub crew of four'
  const startedAt = o.startedAt ?? new Date(Date.now() - 15 * 60_000).toISOString()
  const run: StubRun = {
    id,
    crewId: 'stub-crew',
    name,
    label: name,
    goal: 'four members at work',
    cwd: '/stub',
    isolation: 'worktree',
    startedAt,
    yolo: false,
    state: 'running',
    needsInput: 0,
    members: MEMBERS.map((m, k) => ({
      name: m,
      agentId: 'worker-cli',
      start: { when: 'immediately' },
      sessionId: hex(n * 0x10000 + k, 16),
      branch: `crew/${id}/${m}`,
      worktree: `/stub/.conductor/worktrees/${id}/${m}`,
      status: 'running',
      startedAt,
      agentSession: { id: `${hex(k + 1, 8)}-0000-4000-8000-${hex(n, 12)}`, resumable: false, source: 'set' },
      base: COMMIT(1),
      diff: { added: 0, removed: 0 },
    })),
    log: [{ at: startedAt, type: 'status', message: `launched ${name}: ${MEMBERS.length} members, ${MEMBERS.length} starting now` }],
    managed: { revision: 0 },
    tasks: [],
  }
  // A run of members has no ledger: no `managed`, no tasks.
  const { managed: _m, tasks: _t, ...members } = run
  return { ...(members as StubRun), ...(o.record ? { record: true, state: 'stopped', stoppedAt: new Date().toISOString() } : {}) }
}

/** The run as GET /api/runs lists it: the read's own parts (its chat, each member's diff) left out. */
export function listedRun(run: StubRun): Record<string, unknown> {
  const { chat: _chat, ...rest } = run
  return { ...rest, members: run.members.map(({ diff: _diff, ...m }) => m) }
}

/** The decisions that hold the run's tasks (task `holds`), as GET /api/runs/{run} and a task's read list them: none when none does. */
function heldBy(run: StubRun, task?: string): Array<Record<string, unknown>> {
  const out: Array<Record<string, unknown>> = []
  for (const t of run.tasks ?? []) {
    if (task && t.id !== task) continue
    for (const h of t.holds ?? []) {
      if (out.some((d) => d.id === h.id)) continue
      const agent = t.state === 'running'
      out.push({ id: h.id, ...(agent ? { requestId: `stub-${t.id}-1` } : {}), type: h.type, origin: agent ? 'agent' : 'plan', ...(agent ? { task: t.id } : { gate: 'gate' }), state: h.state, rev: 1, question: h.question, createdAt: run.startedAt })
    }
  }
  return out
}

/** A task's read: the task, every attempt it has made (the last as the run has it; the ones before it ended, each failed), the checks a
 * failed attempt ran, and what each attempt with a commit submitted. */
function taskRead(run: StubRun, t: StubTask): Record<string, unknown> {
  const a = t.attempt
  const attempts: Array<Record<string, unknown>> = []
  if (a) {
    for (let k = 1; k <= a.n; k++) {
      const one: Record<string, unknown> = k === a.n ? { ...a } : { ...a, n: k, sessionId: a.sessionId!.slice(0, -1) + hex(k, 1), branch: `crew/${run.id}/${t.id}.${k}`, worktree: a.worktree!.replace(/\.\d+$/, `.${k}`), state: 'failed', stage: 'check never', error: 'exited 1', endedAt: a.startedAt }
      if (one.result) one.submitted = { summary: `${t.id}, attempt ${k}`, issues: ['none'] }
      if (one.state === 'failed') {
        const at = (one.endedAt as string | undefined) ?? a.startedAt
        one.steps = [{ phase: 'check', id: 'never', k: 1, state: 'finished', argv: ['sh', '-c', 'exit 1'], dir: `${one.worktree as string}.check`, subject: one.result, exit: 1, duration: 120, error: 'exited 1', tries: 1, startedAt: at, finishedAt: at, logBytes: 0, logSha256: 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855', where: 'native' }]
      }
      attempts.push(one)
    }
  }
  const decisions = heldBy(run, t.id)
  return { task: t, attempts, ...(decisions.length ? { decisions } : {}) }
}

/** The candidates' page size: GET /api/runs/{run}/candidates reads at most 8 tasks a page, in the crew's order. */
const CANDIDATE_PAGE = 8

export interface ServedRun {
  /** Every read the module answered, by method, path and query. */
  readonly answered: string[]
  /** Every /api/ request the page made that the module did not answer (a read under a run answered 404 is among them). */
  readonly unanswered: string[]
  /** Every task command the page sent (POST /api/runs/<id>/tasks/<task>/<command>), each answered done. */
  readonly commands: Array<{ task: string; command: string; commandId: string }>
}

const fulfil = (route: Route, status: number, body: unknown) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })
const refusal = (code: string, message: string) => ({ error: { code, message } })

/**
 * Answers the run page's reads of `runs` (one run, or several) as the server does: GET /api/runs (the server's list, each stub run
 * but a record first in the list's form: the live store keeps only the runs it lists), GET /api/runs/<id> (the run, `record` beside
 * a record, and the decisions that hold its tasks), its candidates (paged by eight tasks; a task or an `after` that names none refused
 * 400 as the server refuses them; none for a run of members), its links (none), a task (with its attempts), GET
 * /api/dispatch/links?run=<id> (no ticket), and a task's command (answered done, and counted in `commands`). Any other request under
 * /api/runs/<id> is answered 404 and recorded; every other /api/ request it does not answer is recorded and goes on (to an earlier route, stubInbox's
 * for the decisions, or the server). Set before the page loads.
 */
export async function serveRun(page: Page, runs: StubRun | StubRun[]): Promise<ServedRun> {
  const all = Array.isArray(runs) ? runs : [runs]
  const answered: string[] = []
  const unanswered: string[] = []
  const commands: ServedRun['commands'] = []
  const candidates = (t: StubTask) => ({
    task: t.id,
    kind: t.kind,
    // A candidate counts the context its attempt was given (CT-3) when the stub's attempt carries any.
    candidates: t.result
      ? [{ task: t.id, kind: t.kind, attempt: t.result.attempt, commit: t.result.commit, start: COMMIT(1), state: 'succeeded', standing: true, withdrawn: false, receipts: [], ...(t.attempt?.n === t.result.attempt && t.attempt.context?.length ? { context: t.attempt.context.length } : {}) }]
      : [],
    more: 0,
    withdrawn: 0,
    acceptedOlder: 0,
  })
  await page.route(
    (u) => u.pathname.startsWith('/api/'),
    async (route) => {
      const req = route.request()
      const u = new URL(req.url())
      const path = u.pathname
      const answer = (status: number, body: unknown) => {
        answered.push(`${req.method()} ${path}${u.search}`)
        return fulfil(route, status, body)
      }
      const run = all.find((r) => path === `/api/runs/${r.id}` || path.startsWith(`/api/runs/${r.id}/`))
      const under = run ? `/api/runs/${run.id}` : ''
      if (req.method() === 'GET') {
        if (path === '/api/runs') {
          // The runs list, the live store's: the server's, with the stub's runs first (it would drop a run the list leaves out).
          // A record is no run the live store lists.
          const real = await route.fetch()
          const body = (await real.json().catch(() => ({ runs: [] }))) as { runs?: Array<{ id: string }> }
          const theirs = (body.runs ?? []).filter((r) => !all.some((s) => s.id === r.id))
          return answer(real.status(), { ...body, runs: [...all.filter((r) => !r.record).map(listedRun), ...theirs] })
        }
        if (path === '/api/dispatch/links') {
          const of = all.find((r) => r.id === u.searchParams.get('run'))
          if (of) return answer(200, { ticket: null })
        }
        if (path === '/api/brakeman/requests') {
          // What Brakeman said about a stub run (the run page's panel and its rows' answers ask it): nothing, as the server answers a
          // run it holds no request about. Any other list is the server's.
          const about = u.searchParams.get('about') ?? ''
          if (all.some((r) => about === `run:${r.id}` || about.startsWith(`task:${r.id}/`))) return answer(200, { requests: [], next: '' })
        }
        if (run) {
          if (path === under) {
            // A record is answered as the server answers one: the run, and `record` beside it.
            const decisions = heldBy(run)
            const { record, ...held } = run
            return answer(200, { run: held, ...(record ? { record: true } : {}), ...(decisions.length ? { decisions } : {}) })
          }
          if (path === `${under}/links`) return answer(200, { links: [] })
          if (path === `${under}/candidates` && !run.managed) return answer(200, { run: run.id, managed: false, base: '', candidates: [] })
          if (path === `${under}/candidates`) {
            const task = u.searchParams.get('task')
            if (task) {
              const t = run.tasks.find((x) => x.id === task)
              return t ? answer(200, { base: COMMIT(1), candidates: [candidates(t)], managed: true, run: run.id }) : answer(400, refusal('invalid_request', 'task names no task of the run'))
            }
            const after = u.searchParams.get('after')
            const from = after ? run.tasks.findIndex((t) => t.id === after) + 1 : 0
            if (after && from === 0) return answer(400, refusal('invalid_request', 'after names no task of the run'))
            const tasks = run.tasks.slice(from, from + CANDIDATE_PAGE)
            const next = from + CANDIDATE_PAGE < run.tasks.length ? tasks[tasks.length - 1]!.id : undefined
            return answer(200, { base: COMMIT(1), candidates: tasks.map(candidates), managed: true, run: run.id, ...(next ? { next } : {}) })
          }
          const task = path.startsWith(`${under}/tasks/`) ? decodeURIComponent(path.slice(`${under}/tasks/`.length)) : ''
          if (task && !task.includes('/')) {
            const t = run.tasks?.find((x) => x.id === task)
            if (t) return answer(200, taskRead(run, t))
          }
        }
      }
      // A task's command (cancel, retry, accept, recheck): answered done at the run's next revision, and counted.
      const command = run && req.method() === 'POST' ? new RegExp(`^${under}/tasks/([^/]+)/(cancel|retry|accept|recheck)$`).exec(path) : null
      const t = command ? run!.tasks?.find((x) => x.id === decodeURIComponent(command[1]!)) : undefined
      if (command && t) {
        const body = (req.postDataJSON() ?? {}) as { commandId?: string }
        commands.push({ task: t.id, command: command[2]!, commandId: body.commandId ?? '' })
        return answer(200, { receipt: { commandId: body.commandId ?? '', intent: command[2], revision: (run!.managed?.revision ?? 0) + 1, status: 'done', task: { id: t.id, state: t.state, attempts: t.attempts } } })
      }
      unanswered.push(`${req.method()} ${path}${u.search}`)
      if (run) return fulfil(route, 404, refusal('not_found', 'not answered by serveRun'))
      return route.fallback()
    },
  )
  return { answered, unanswered, commands }
}

/**
 * The run as a ticket's runs summarize it (GET /api/dispatch/tickets/{key}/runs, internal/api/dispatch_evidence.go runSummary): its
 * tasks counted by state, its members with their sessions, its questions as the run counts them (needsInput), no blockers.
 */
export function runSummary(run: StubRun): Record<string, unknown> {
  const tasks: Record<string, number> = {}
  for (const t of run.tasks ?? []) tasks[t.state] = (tasks[t.state] ?? 0) + 1
  return {
    id: run.id,
    label: run.label,
    state: run.state,
    paused: !!run.paused,
    startedAt: run.startedAt,
    record: !!run.record,
    members: run.members.map((m) => ({ name: m.name, ...(m.sessionId ? { sessionId: m.sessionId } : {}), status: m.status })),
    tasks,
    questions: run.needsInput,
    blockers: [],
  }
}

/**
 * Answers GET /api/dispatch/tickets/{key}/runs with `run`'s summary (runSummary) as its one entry, a run of the ticket's chain, and no
 * next page: the ticket's Runs section draws the run's card from it. Its reads are counted. Leave the run out of serveRun (the server
 * then answers GET /api/runs/<id> 404) for the card that has the summary alone: neither the live store nor the records hold it then.
 * Set before the page loads.
 */
export async function serveTicketRuns(page: Page, key: string, run: StubRun): Promise<{ readonly reads: number }> {
  const served = { reads: 0 }
  await page.route(
    (u) => u.pathname === `/api/dispatch/tickets/${key}/runs`,
    (route) => {
      if (route.request().method() !== 'GET') return route.fallback()
      served.reads++
      return fulfil(route, 200, { entries: [{ kind: 'run', run: runSummary(run) }] })
    },
  )
  return served
}

/** The forty-task shape the line diagram is laid out for (web/app/utils/testdata/line-forty.json, lineDiagram.test.ts's bounds). */
const FORTY = JSON.parse(readFileSync(join(repoRoot, 'web/app/utils/testdata/line-forty.json'), 'utf8')) as Array<{ id: string; needs?: string[] }>

/** A managed run of `shape`'s tasks, each as runInfo makes one in the state `states` gives it in turn, with the needs the shape gives. */
function shapedRun(name: string, shape: ReadonlyArray<{ id: string; needs?: string[] }>, states: readonly TaskState[]): StubRun {
  const run = runInfo({ tasks: 6 })
  const n = made
  run.tasks = shape.map((s, i) => {
    const t = stubTask(run.id, n, s.id, i, states[i % states.length]!)
    if (s.needs?.length) t.needs = [...s.needs]
    else delete t.needs
    if (t.state === 'blocked') t.reason = s.needs?.length ? `needs ${s.needs.join(', ')}` : 'needs nothing yet'
    return t
  })
  run.name = name
  run.label = name
  run.members = stubMembers(run.id, run.tasks)
  return run
}

/**
 * The forty-task run (UX1 PR-33): the shape of line-forty.json, its states spread over every state a task can be in (TASK_STATES in
 * turn), so that its stations carry every mark a run's task draws without a live session: done, failed, cancelled, waiting, ready,
 * checking and running. A failed task carries the engine's commands, so it asks for a person (needs you).
 */
export function fortyRun(): StubRun {
  return shapedRun('Stub run of forty', FORTY, TASK_STATES)
}

/** A chain of `count` tasks (128 at most: a crew's bound), t001 to t128, each needing the one before it: done up to the middle, one running, the rest waiting. */
export function chainRun(count: number): StubRun {
  const width = Math.max(2, String(count).length)
  const ids = Array.from({ length: count }, (_, i) => `t${String(i + 1).padStart(width, '0')}`)
  const half = Math.floor(count / 2)
  return shapedRun(
    `Stub chain of ${count}`,
    ids.map((id, i) => (i ? { id, needs: [ids[i - 1]!] } : { id })),
    ids.map((_, i): TaskState => (i < half ? 'succeeded' : i === half ? 'running' : 'blocked')),
  )
}
