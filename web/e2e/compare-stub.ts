import type { Page, Route } from '@playwright/test'
import { runInfo, type StubRun, type StubTask } from './run-fixtures'

// A comparison answered in the browser (Program item CP1, docs/plans/compare-crew.md, PR-4): GET /api/runs/{run}/comparison with every
// part at a state the test picks, for the page's drawing (two or four candidates, a failed lane, each verdict, a cell not run, git
// unavailable for a lane, Brakeman's request), and a comparison's run as GET /api/runs/{run} gives it (its `compare` part, its lanes'
// tasks, the ticket it was launched from). A stub proves how a shape is drawn, never that the server sends it: it answers as the server
// does (internal/crew/comparison.go's JSON tags, the verdict rule's words), and compare.spec.ts holds its keys to the real reply of a
// real comparison. Its ids are its own: run ids no server of the suite issues.

const COMMIT = (c: string) => c.repeat(40).slice(0, 40)
const SHA = (c: string) => c.repeat(64).slice(0, 64)
export const BASE = COMMIT('1')
/** The server's words for what independence does not observe (internal/api/compare.go). */
export const NOT_OBSERVED =
  "A read of another candidate's folder or of an agent's own stored state is not observed, and a change rewritten by hand is not detected; what the agents report of their own file activity is not used as a measure."


/** The lanes' task ids, in the block's order. */
export const LANES = ['approach-a', 'approach-b', 'approach-c', 'approach-d'] as const

export interface ComparisonShape {
  /** The chain's launched run. */
  run: string
  /** Two (the default) or four candidates. */
  candidates?: 2 | 4
  /** approach-b failed: its last submitted commit shown, not selectable. */
  failed?: boolean
  /** approach-a changed a test file and a build file: every row that would be comparable is "comparable only if…" (the rule's own). */
  testChange?: boolean
  /** Git could not be read for the last lane: its words, the rest of the reply standing. */
  unavailable?: boolean
  /** approach-a still running: no result yet, no cells. */
  running?: boolean
  /** The supporting part (PR-5) with Brakeman's request, absent when not given (and no `supporting` asked). */
  brakemanRequest?: string
  /**
   * The supporting part as the server gives it (PR-5, PR-6b): each candidate's claim, which favours its own candidate, and each review's
   * verdict, an approval favouring the candidate it approved; Brakeman's request when `brakemanRequest` names one, else why there is
   * none ("Brakeman is not set up").
   */
  supporting?: boolean
  /** The selection part (PR-6b): the newest record, open, or applied with approach-a selected; absent when not given. */
  selection?: 'open' | 'applied'
}

type Cell = Record<string, unknown>

function cell(run: string, task: string, id: string, o: Partial<{ state: string; exit: number | null; notRun: string; durationMs: number }> = {}): Cell {
  const state = o.state ?? 'finished'
  const ran = state !== 'not_run'
  return {
    task,
    attempt: 1,
    k: ran ? 1 : 0,
    state,
    exit: ran ? (o.exit === undefined ? 0 : o.exit) : null,
    signal: '',
    timedOut: false,
    durationMs: ran ? (o.durationMs ?? 4200) : 0,
    where: ran ? 'native' : '',
    manifest: '',
    stable: ran,
    receipt: ran ? `check:${run}/${task}/1/${id}/1` : '',
    ...(ran ? { logSha256: SHA('e') } : {}),
    ...(o.notRun ? { notRun: o.notRun } : {}),
  }
}

/**
 * A comparison's reply. Its rows, by the server's rule: check `unit` passes on every candidate (comparable, or "comparable only if
 * their test changes are fair" when a candidate changed a test or build file); check `bench`, whose script approach-b changed, is not
 * comparable; the manifest's verify step `lint` did not run on approach-b, whose bench failed first (a tree stops at its first
 * failure), and is not comparable either.
 */
export function comparisonOf(o: ComparisonShape): Record<string, unknown> {
  const lanes = LANES.slice(0, o.candidates ?? 2)
  const last = lanes[lanes.length - 1]!
  const candidates = lanes.map((task, i) => {
    const failed = o.failed && task === 'approach-b'
    const running = o.running && task === 'approach-a'
    const unavailable = o.unavailable && task === last
    const commit = running ? '' : COMMIT('abcd'[i]!)
    const measured = !running && !unavailable
    return {
      task,
      member: `cand-${'abcd'[i]}`,
      agent: 'worker-cli',
      args: i % 2 ? ['--model', 'small'] : [],
      approach: i === 0 ? 'Key the cache by locale' : i === 1 ? 'Drop the cache and memoise in the handler' : '',
      state: running ? 'running' : failed ? 'failed' : 'ready',
      run: o.run,
      attempt: running ? 0 : failed ? 3 : 1,
      commit,
      start: BASE,
      selectable: !running && !failed,
      attempts: running ? 1 : failed ? 3 : 1,
      acceptedAnyway: false,
      ...(measured
        ? {
            size:
              i === 0
                ? { files: 3, added: 40, removed: 2, filesAdded: 1, filesDeleted: 0, list: [{ path: 'internal/cache/cache.go', status: 'M', added: 30, removed: 2, binary: false }, { path: 'internal/cache/locale.go', status: 'A', added: 10, removed: 0, binary: false }, { path: 'go.mod', status: 'M', added: 0, removed: 0, binary: false }], total: 3 }
                : { files: 2, added: 12, removed: 30, filesAdded: 0, filesDeleted: 1, list: [{ path: 'internal/cache/cache.go', status: 'D', added: 0, removed: 28, binary: false }, { path: 'scripts/bench.sh', status: 'M', added: 12, removed: 2, binary: false }], total: 2 },
            // No dependency file changed: no `deps` at all, as the server leaves it out.
            ...(i === 0 ? { deps: { files: ['go.mod', 'go.sum'], modules: [{ file: 'go.mod', name: 'golang.org/x/text', from: '', to: 'v0.3.0' }], total: 2, modulesTotal: 1 } } : {}),
            ...(o.testChange && i === 0 ? { verifyFiles: { files: [{ path: 'internal/cache/cache_test.go', kind: 'test' }, { path: 'Makefile', kind: 'build' }], total: 2 } } : {}),
          }
        : {}),
      shared: i === 1 ? [{ with: 'approach-a', count: 2, commits: [COMMIT('5'), COMMIT('6')] }] : [],
      equivalent: i === 1 ? [{ with: 'approach-a', count: 1, commits: [COMMIT('7')], note: 'same patches: checked; a change rewritten by hand is not detected' }] : [],
      manifestChanged: false,
      ...(unavailable ? { unavailable: 'git diff: exit status 128: fatal: bad object' } : {}),
    }
  })
  // The server's words (internal/crew/comparison.go): each candidate's first test or build file, how many more, or none.
  const changed = lanes.map((t) => (o.unavailable && t === last ? `${t}'s change could not be read` : o.testChange && t === 'approach-a' ? `${t} changed internal/cache/cache_test.go, a test file and 1 more` : `${t} changed none`))
  const tested = o.testChange || o.unavailable ? [`comparable only if their test changes are fair: ${changed.join('; ')}`] : []
  const ran = (task: string, id: string, x: Partial<{ state: string; exit: number | null; notRun: string }> = {}) => (o.running && task === 'approach-a' ? cell(o.run, task, id, { state: 'not_run', notRun: 'no_such_step' }) : cell(o.run, task, id, x))
  const checks = [
    { phase: 'check', id: 'unit', argv: ['go', 'test', './...'], argvCut: false, argvDigest: SHA('a'), timeoutSeconds: 600, cells: lanes.map((t) => ran(t, 'unit')), verdict: tested.length ? 'unknown' : 'comparable', why: tested },
    {
      phase: 'check',
      id: 'bench',
      argv: ['sh', 'scripts/bench.sh'],
      argvCut: false,
      argvDigest: SHA('b'),
      timeoutSeconds: 300,
      cells: lanes.map((t) => (t === 'approach-b' ? ran(t, 'bench', { exit: 1 }) : ran(t, 'bench'))),
      verdict: 'not_comparable',
      why: ['approach-b changed scripts/bench.sh, which the check runs'],
    },
    {
      phase: 'verify',
      id: 'lint',
      argv: ['make', 'lint'],
      argvCut: false,
      argvDigest: SHA('c'),
      timeoutSeconds: 120,
      cells: lanes.map((t) => (t === 'approach-b' ? cell(o.run, t, 'lint', { state: 'not_run', notRun: 'earlier_failed' }) : ran(t, 'lint'))),
      verdict: 'not_comparable',
      why: ['approach-b: not run: an earlier check failed'],
    },
  ]
  return {
    run: o.run,
    base: BASE,
    objective: 'Make the locale cache read faster without a new dependency',
    criteria: [
      { id: 'no-dep', text: 'no new dependency' },
      { id: 'p99', text: 'p99 of the cache read under 2 ms in bench_test.go' },
    ],
    candidates,
    checks,
    settled: !o.running,
    // The server's word for where the comparison is (running, selecting, answered, selected, none, ended).
    state: o.running ? 'running' : o.selection === 'applied' ? 'selected' : 'selecting',
    independence: { notObserved: NOT_OBSERVED },
    ...(o.supporting ? { supporting: supportingOf(o, lanes) } : o.brakemanRequest ? { supporting: { claims: [], reviews: [], brakeman: { request: o.brakemanRequest, state: 'queued' } } } : {}),
    ...(o.selection ? { selection: selectionOf(o, candidates) } : {}),
  }
}

/** What was said (PR-5): a claim per candidate with a result, a review per candidate, the first approved; Brakeman's part. */
function supportingOf(o: ComparisonShape, lanes: readonly string[]): Record<string, unknown> {
  return {
    claims: lanes.map((task) => ({ task, attempt: 1, summary: `${task}: the stub's own summary of its change.`, favours: task })),
    reviews: lanes.map((task, i) => ({ task: `review-${task.slice(-1)}`, attempt: 1, verdict: i === 0 ? 'approved' : 'needs_changes', findings: i === 0 ? [] : ['the bench script was changed'], ...(i === 0 ? { favours: task } : {}) })),
    brakeman: o.brakemanRequest ? { request: o.brakemanRequest, state: 'queued' } : { why: 'Brakeman is not set up' },
  }
}

/** The selection (PR-6b): its record's options (each selectable candidate at its commit, then None of these), and once applied the answer. */
function selectionOf(o: ComparisonShape, candidates: ReadonlyArray<{ task: string; run: string; attempt: number; commit: string; selectable: boolean }>): Record<string, unknown> {
  const picks = candidates.filter((c) => c.selectable)
  const options = [
    ...picks.map((c) => ({ label: `${c.task} at ${c.commit.slice(0, 7)}`, tradeoffs: `attempt ${c.attempt}; its checks passed`, subject: { kind: 'candidate', run: c.run, task: c.task, n: c.attempt, commit: c.commit } })),
    { label: 'None of these', tradeoffs: 'Ends the comparison with no candidate selected.' },
  ]
  const out: Record<string, unknown> = { decision: 'dr-9001', state: o.selection, options }
  if (o.selection === 'applied' && picks[0]) {
    const c = picks[0]
    Object.assign(out, {
      chosen: 0,
      rationale: 'the stub selects the first',
      by: 'Nate',
      at: new Date().toISOString(),
      applied: { how: 'selected', selected: { task: c.task, run: c.run, attempt: c.attempt, commit: c.commit }, words: `${c.task} selected at ${c.commit.slice(0, 7)}` },
    })
  }
  return out
}

export interface ServedComparison {
  /** How many reads of the comparison the module answered. */
  readonly reads: number
}

const fulfil = (route: Route, status: number, body: unknown) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })

/**
 * Answers GET /api/runs/<runId>/comparison with `reply` (or what `reply()` gives at each read; null for a run of no comparison, which
 * the server answers 404 not_found "this run is not a comparison"), and counts the reads. Set before the page loads, after serveRun
 * (a route set later is asked first). Any other request goes on.
 */
export async function serveComparison(page: Page, runId: string, reply: Record<string, unknown> | null | (() => Record<string, unknown> | null)): Promise<ServedComparison> {
  const served = { reads: 0 }
  await page.route(
    (u) => u.pathname === `/api/runs/${runId}/comparison`,
    (route) => {
      if (route.request().method() !== 'GET') return route.fallback()
      served.reads++
      const r = typeof reply === 'function' ? reply() : reply
      if (!r) return fulfil(route, 404, { error: { code: 'not_found', message: 'this run is not a comparison' } })
      return fulfil(route, 200, r)
    },
  )
  return served
}

/**
 * A comparison's run as GET /api/runs/{run} gives it: its lanes' tasks (approach-a and approach-b, each done, each reviewed: a review's
 * one need is its candidate), the review of approach-b running, its `compare` part (each candidate's task with its review's), and
 * the ticket it was launched from (`ticket`, the key the links go to). Built as runInfo builds a run, its tasks renamed.
 */
export function compareRun(o: { ticket: string }): StubRun & { compare: { candidates: Array<{ task: string; review?: string }> } } {
  const run = runInfo({ tasks: 6, states: ['succeeded', 'succeeded', 'succeeded', 'running', 'blocked', 'blocked'] })
  const ids: Record<string, { id: string; kind: 'work' | 'review'; needs?: string[] }> = {
    t01: { id: 'approach-a', kind: 'work' },
    t02: { id: 'approach-b', kind: 'work' },
    t03: { id: 'review-a', kind: 'review', needs: ['approach-a'] },
    t04: { id: 'review-b', kind: 'review', needs: ['approach-b'] },
  }
  const rename = (t: StubTask): StubTask => {
    const to = ids[t.id]!
    const out: StubTask = { ...t, id: to.id, kind: to.kind }
    if (to.needs) out.needs = to.needs
    else delete out.needs
    if (out.attempt) out.attempt = { ...out.attempt, branch: `crew/${run.id}/${to.id}.${out.attempt.n}`, worktree: `/stub/.conductor/worktrees/${run.id}/${to.id}.${out.attempt.n}` }
    return out
  }
  // The review tasks come first in the crew's order here, so that the lanes' order is the page's and not the crew's.
  const tasks = run.tasks.filter((t) => ids[t.id]).map(rename)
  run.tasks = [tasks[2]!, tasks[0]!, tasks[3]!, tasks[1]!]
  run.members = run.members.map((m) => ({ ...m, ...(m.task && ids[m.task] ? { task: ids[m.task]!.id } : {}) }))
  run.name = 'Compare the cache'
  run.label = run.name
  return Object.assign(run, {
    compare: { candidates: [{ task: 'approach-a', review: 'review-a' }, { task: 'approach-b', review: 'review-b' }] },
    ticket: { id: 'tk-stub', key: o.ticket, rev: 1, plan: 'pl-stub', planN: 1, launch: '', digest: SHA('d') },
  })
}
