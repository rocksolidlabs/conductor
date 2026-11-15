import type { Page, Route } from '@playwright/test'
import type { Run, RunTask } from './fixtures'

// A managed run answered in the browser (UX1's PR-1, docs/plans/ux-review.md, "Stubs in the browser"): the shapes the day cannot make
// cheaply (a run of 40 or 128 tasks, every task state at once, a task checking, which the day cannot hold: a check's hold is capped
// at 3,600 s). A stub proves how a shape is drawn, never that the server sends it; stubs.spec.ts holds the run page to it, so that a
// read the page gains fails there, not in a layout spec. Modelled on GET /api/runs/{run} and its reads (internal/api, internal/crew).

/** Every state a task of a managed run can be in (internal/crew/ledger/rows.go); `verifying` is a task checking. */
export const TASK_STATES = ['blocked', 'ready', 'running', 'verifying', 'succeeded', 'failed', 'cancelled'] as const
export type TaskState = (typeof TASK_STATES)[number]

/** The run, with the crew's members and tasks as the run page reads them. */
export type StubRun = Run & { label: string; goal: string; cwd: string; isolation: string; startedAt: string; managed: { revision: number }; tasks: RunTask[] }

let made = 0
const MEMBERS = ['builder', 'reviewer', 'writer', 'tester']
const COMMIT = (n: number) => n.toString(16).padStart(40, 'a')

/** One task of `id` in `state`, with what a task in that state carries (its attempt, its result, its reason). */
function stubTask(id: string, i: number, state: TaskState, prev?: string): RunTask {
  const member = MEMBERS[i % MEMBERS.length]!
  const base: RunTask = { id, member, kind: 'work', state, attempts: 0 }
  const attempt = (n: number, s: string) => ({ n, branch: `crew/stub/${id}.${n}`, start: COMMIT(1), state: s })
  switch (state) {
    case 'blocked':
      return { ...base, needs: prev ? [prev] : [], reason: prev ? `needs ${prev}` : 'needs nothing yet' }
    case 'ready':
      return { ...base, reason: 'waits for a free member' }
    case 'running':
      return { ...base, attempts: 1, attempt: attempt(1, 'running') }
    case 'verifying':
      return { ...base, attempts: 1, attempt: { ...attempt(1, 'verifying'), stage: 'check', result: COMMIT(i + 2) } }
    case 'succeeded':
      return { ...base, attempts: 1, attempt: { ...attempt(1, 'succeeded'), result: COMMIT(i + 2) }, result: { attempt: 1, commit: COMMIT(i + 2), summary: `${id} done`, issues: ['none'], at: new Date().toISOString() } }
    case 'failed':
      return { ...base, attempts: 3, attempt: { ...attempt(3, 'failed'), stage: 'check', error: 'check never: exited 1' }, reason: 'attempt 3 failed at check never: exited 1' }
    case 'cancelled':
      return { ...base, reason: 'cancelled' }
  }
}

/**
 * A managed run of `tasks` tasks (6, 40 or 128: a crew's bound), each in its state from `states` in turn (every state at once, in
 * TASK_STATES' order, when not given), ids t01, t02…, its members four stubs. Its id is a run id of its own.
 */
export function runInfo(o: { tasks: 6 | 40 | 128; states?: TaskState[] }): StubRun {
  const n = ++made
  const states = o.states ?? [...TASK_STATES]
  const width = String(o.tasks).length
  const ids = Array.from({ length: o.tasks }, (_, i) => `t${String(i + 1).padStart(Math.max(2, width), '0')}`)
  const tasks = ids.map((id, i) => stubTask(id, i, states[i % states.length]!, ids[i - 1]))
  const id = `stub-run-${(0x1000_0000 + n).toString(16)}`
  return {
    id,
    crewId: 'stub-run',
    name: `Stub run of ${o.tasks}`,
    label: `Stub run of ${o.tasks}`,
    goal: 'draw every task',
    cwd: '/stub',
    isolation: 'worktree',
    startedAt: new Date(Date.now() - 15 * 60_000).toISOString(),
    state: 'running',
    members: MEMBERS.map((name) => ({ name, agentId: 'worker-cli', status: 'pending' as const })),
    log: [{ at: new Date().toISOString(), type: 'status', message: `launched Stub run of ${o.tasks}` }],
    managed: { revision: 1 },
    tasks,
  }
}

/** The candidates' page size: GET /api/runs/{run}/candidates reads at most 8 tasks a page, in the crew's order. */
const CANDIDATE_PAGE = 8

export interface ServedRun {
  /** Every /api/ request the page made that the module did not answer (a read under the run answered 404 is among them). */
  readonly unanswered: string[]
}

const json = (route: Route, status: number, body: unknown) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })

/**
 * Answers the run page's reads of `run`: the runs list (the server's, `run` added: the live store keeps only the runs it lists),
 * GET /api/runs/<id> (the run), its candidates (paged as the server pages them), its links, a
 * task (with its attempts), and GET /api/dispatch/links?run=<id> (no ticket). Any other read under /api/runs/<id> is answered 404.
 * Every /api/ request it did not answer is recorded (the rest go on: to an earlier route, stubInbox's for the decisions, or the
 * server). Set before the page loads.
 */
export async function serveRun(page: Page, run: StubRun): Promise<ServedRun> {
  const unanswered: string[] = []
  const under = `/api/runs/${run.id}`
  const candidates = (t: RunTask) => ({
    task: t.id,
    kind: t.kind,
    candidates: t.result ? [{ task: t.id, kind: t.kind, attempt: t.result.attempt, commit: t.result.commit, start: COMMIT(1), state: 'succeeded', standing: true, withdrawn: false, receipts: [] }] : [],
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
      if (req.method() === 'GET') {
        if (path === under) return json(route, 200, { run })
        if (path === '/api/runs') {
          // The runs list, the live store's: the server's, with the stub's run first (it would drop a run the list leaves out).
          const real = await route.fetch()
          const body = (await real.json().catch(() => ({ runs: [] }))) as { runs?: unknown[] }
          return json(route, real.status(), { ...body, runs: [run, ...(body.runs ?? [])] })
        }
        if (path === `${under}/links`) return json(route, 200, { links: [] })
        if (path === `${under}/candidates`) {
          const task = u.searchParams.get('task')
          if (task) {
            const t = run.tasks.find((x) => x.id === task)
            return t ? json(route, 200, { base: COMMIT(1), candidates: [{ ...candidates(t), candidates: [] }], managed: true, run: run.id }) : json(route, 404, { error: { code: 'not_found', message: 'no such task' } })
          }
          const after = u.searchParams.get('after')
          const from = after ? run.tasks.findIndex((t) => t.id === after) + 1 : 0
          const page = run.tasks.slice(from, from + CANDIDATE_PAGE)
          const next = from + CANDIDATE_PAGE < run.tasks.length ? page[page.length - 1]!.id : undefined
          return json(route, 200, { base: COMMIT(1), candidates: page.map(candidates), managed: true, run: run.id, ...(next ? { next } : {}) })
        }
        const task = path.startsWith(`${under}/tasks/`) ? decodeURIComponent(path.slice(`${under}/tasks/`.length)) : ''
        if (task && !task.includes('/')) {
          const t = run.tasks.find((x) => x.id === task)
          if (t) return json(route, 200, { task: t, attempts: t.attempt ? [{ ...t.attempt }] : [], decisions: [] })
        }
        if (path === '/api/dispatch/links' && u.searchParams.get('run') === run.id) return json(route, 200, { ticket: null })
      }
      unanswered.push(`${req.method()} ${path}${u.search}`)
      if (path === under || path.startsWith(`${under}/`)) return json(route, 404, { error: { code: 'not_found', message: 'not answered by serveRun' } })
      return route.fallback()
    },
  )
  return { unanswered }
}
