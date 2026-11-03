import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as vue from 'vue'
import { ApiError } from './useApi'
import type { RunCandidate, RunCandidatesPage } from './useDispatch'
import type { RunAttempt, RunTask } from './useSessions'
import { type AbsentOptions, DispatchOrder, through, throughOrAbsent } from '../utils/dispatchOrder'
import { repairs } from '../utils/dispatchEvidence'
import { dispatchLive } from './useDispatchLive'

// A run's candidates as DispatchCandidates reads them (Program item DS2, PR-11), against replies the test lets go: a run_changing reply
// asked again after its second, on the first read too, with nothing said as a failure; two review tasks' findings opened while the
// first one's reply is held back, both lists shown; a snapshot reading the pages again; a failure asked before a reply applied, or before
// a snapshot, never said; a failure that may pass asked again with no event; the repairs' tasks read from the task route, a record's
// repair that never submitted included. useRunCandidates reads Nuxt's auto-imports (Vue's, useDispatch, useSessions,
// onBeforeUnmount) as globals: each is stubbed, on fake timers.

function held<T>() {
  let ok!: (v: T) => void
  let fail!: (e: unknown) => void
  const p = new Promise<T>((resolve, reject) => {
    ok = resolve
    fail = reject
  })
  return { p, ok, fail }
}

type TaskReply = { task: RunTask; attempts: RunAttempt[] }
let pagesSent: Array<{ run: string; after: string } & ReturnType<typeof held<RunCandidatesPage>>>
let tasksSent: Array<{ run: string; task: string } & ReturnType<typeof held<TaskReply>>>
let unmount: Array<() => void>

beforeEach(() => {
  vi.useFakeTimers()
  pagesSent = []
  tasksSent = []
  unmount = []
  for (const k of ['ref', 'computed', 'reactive', 'watch'] as const) vi.stubGlobal(k, vue[k])
  vi.stubGlobal('onBeforeUnmount', (f: () => void) => unmount.push(f))
  vi.stubGlobal('useDispatch', () => {
    const gate = new DispatchOrder()
    return {
      gate,
      api: {
        runCandidates: (run: string, after = '') => {
          const h = held<RunCandidatesPage>()
          pagesSent.push({ run, after, ...h })
          return h.p
        },
      },
      read: <T>(thing: string, send: () => Promise<T>, apply: (r: T) => void) => through(gate, thing, send, apply),
      readOrAbsent: <T>(thing: string, send: () => Promise<T>, o: AbsentOptions<T>) => throughOrAbsent(gate, thing, send, o),
    }
  })
  vi.stubGlobal('useSessions', () => ({
    runTask: (run: string, task: string) => {
      const h = held<TaskReply>()
      tasksSent.push({ run, task, ...h })
      return h.p
    },
  }))
})
afterEach(() => {
  for (const f of unmount) f()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

async function candidates(runId = 'crew-0000aaaa', rev: number | undefined = 1) {
  const { useRunCandidates } = await import('./useRunCandidates')
  const revision = vue.ref<number | undefined>(rev)
  return { revision, c: useRunCandidates({ runId: () => runId, revision: () => revision.value }) }
}
const tick = (ms = 0) => vi.advanceTimersByTimeAsync(ms)

function review(task: string, attempt = 1): RunCandidate {
  return { task, kind: 'review', attempt, state: 'succeeded', verdict: 'needs_changes', findings: { major: 1 }, standing: false, withdrawn: false, receipts: [] }
}
function reply(task: string, message: string, n = 1): TaskReply {
  return { task: { id: task } as RunTask, attempts: Array.from({ length: n }, (_, i) => ({ n: i + 1, submitted: { findings: [{ severity: 'major', message: `${message} ${i + 1}` }] } }) as unknown as RunAttempt) }
}
const page: RunCandidatesPage = { managed: true, candidates: [{ task: 'review-a', kind: 'review', candidates: [review('review-a')] }] } as RunCandidatesPage

describe("a run's candidates", () => {
  it('ask a run_changing reply again after its second, on the first read too, with nothing said as a failure', async () => {
    const { c } = await candidates()
    expect(pagesSent).toHaveLength(1)
    pagesSent[0]!.fail(new ApiError(503, 'run_changing', 'the run moved through every read'))
    await tick()
    expect([c.error.value, c.pages.value]).toEqual(['', null])
    await tick(999)
    expect(pagesSent).toHaveLength(1)
    await tick(1)
    expect(pagesSent).toHaveLength(2)
    pagesSent[1]!.ok(page)
    await tick()
    expect(c.tasks.value.map((t) => t.task)).toEqual(['review-a'])
    expect(c.error.value).toBe('')
  })

  it('say any other failure of the first read', async () => {
    const { c } = await candidates()
    pagesSent[0]!.fail(new ApiError(404, 'not_found', 'no such run'))
    await tick()
    expect(c.error.value).toBe('The server knows no such run.')
    await tick(2000)
    expect(pagesSent).toHaveLength(1)
  })

  it("show two review tasks' findings opened while the first one's reply is held back", async () => {
    const { c } = await candidates()
    const a = c.toggleFindings(review('review-a'))
    const b = c.toggleFindings(review('review-b'))
    expect(tasksSent.map((t) => t.task)).toEqual(['review-a', 'review-b'])
    tasksSent[1]!.ok(reply('review-b', 'b'))
    await b
    tasksSent[0]!.ok(reply('review-a', 'a'))
    await a
    expect(c.findings.get('review-a/1')?.map((f) => f.message)).toEqual(['a 1'])
    expect(c.findings.get('review-b/1')?.map((f) => f.message)).toEqual(['b 1'])
    expect([...c.findingsError.keys()]).toEqual([])
  })

  it("apply only the last read of one review task's findings, and say its failure on every attempt of it still reading", async () => {
    const { c } = await candidates()
    const first = c.toggleFindings(review('review-a', 1))
    const second = c.toggleFindings(review('review-a', 2))
    // The later read fails, the earlier answers after it: the earlier is stale, both attempts opened say the failure.
    tasksSent[1]!.fail(new Error('the task could not be read'))
    await second
    tasksSent[0]!.ok(reply('review-a', 'old', 2))
    await first
    expect(c.findings.has('review-a/1')).toBe(false)
    expect(c.findingsError.get('review-a/1')).toBe('the task could not be read')
    expect(c.findingsError.get('review-a/2')).toBe('the task could not be read')
    // Closed and opened again: read again, and its answer clears the failure of every attempt of the task.
    await c.toggleFindings(review('review-a', 1))
    const again = c.toggleFindings(review('review-a', 1))
    tasksSent[2]!.ok(reply('review-a', 'new', 2))
    await again
    expect(c.findings.get('review-a/2')?.map((f) => f.message)).toEqual(['new 2'])
    expect([...c.findingsError.keys()]).toEqual([])
  })

  it('read the pages again on a snapshot: a failure said before it is read again, a record with no revision too', async () => {
    const { c, revision } = await candidates()
    revision.value = undefined
    pagesSent[0]!.fail(new Error('the server could not be reached'))
    await tick()
    expect(c.error.value).toBe('the server could not be reached')
    dispatchLive().snapshot()
    expect(pagesSent).toHaveLength(2)
    pagesSent[1]!.ok(page)
    await tick()
    expect([c.error.value, c.tasks.value.map((t) => t.task)]).toEqual(['', ['review-a']])
    // Gone from the page: a snapshot reads nothing for it.
    for (const f of unmount.splice(0)) f()
    dispatchLive().snapshot()
    expect(pagesSent).toHaveLength(2)
  })

  it("ask a snapshot's failure that may pass again with no further event, after the window doubled each time, until one answers", async () => {
    // A record: no revision, none to move.
    const { c } = await candidates('crew-0000aaaa', undefined)
    pagesSent[0]!.ok(page)
    await tick()
    // A record's candidates, read again on a reconnect's snapshot, refused by a ledger that could not be read.
    dispatchLive().snapshot()
    pagesSent[1]!.fail(new ApiError(503, 'ledger_unavailable', 'the ledger could not be read'))
    await tick()
    expect(c.error.value).toBe('the ledger could not be read')
    await tick(249)
    expect(pagesSent).toHaveLength(2)
    await tick(1)
    expect(pagesSent).toHaveLength(3)
    pagesSent[2]!.fail(new Error('the server could not be reached'))
    await tick(499)
    expect(pagesSent).toHaveLength(3)
    await tick(1)
    expect(pagesSent).toHaveLength(4)
    pagesSent[3]!.ok(page)
    await tick()
    expect([c.error.value, c.tasks.value.map((t) => t.task)]).toEqual(['', ['review-a']])
    await tick(10_000)
    expect(pagesSent).toHaveLength(4)
  })

  it("ask the first read's failure that may pass again after the window, and a 404 never", async () => {
    const { c } = await candidates()
    pagesSent[0]!.fail(new ApiError(503, 'ledger_unavailable', 'the ledger could not be read'))
    await tick(250)
    expect([c.error.value, pagesSent.length]).toEqual(['the ledger could not be read', 2])
    pagesSent[1]!.fail(new ApiError(404, 'not_found', 'no such run'))
    await tick(10_000)
    expect([c.error.value, pagesSent.length]).toEqual(['The server knows no such run.', 2])
  })

  it('drop a failure asked before a snapshot whose read answered, a 404 too', async () => {
    const { c, revision } = await candidates()
    revision.value = undefined
    dispatchLive().snapshot()
    expect(pagesSent).toHaveLength(2)
    pagesSent[1]!.ok(page)
    await tick()
    // The read asked before the snapshot fails after it: the page the snapshot's read found stays, with nothing said.
    pagesSent[0]!.fail(new ApiError(404, 'not_found', 'no such run'))
    await tick()
    expect([c.error.value, c.tasks.value.map((t) => t.task)]).toEqual(['', ['review-a']])
  })

  it('drop a failure of a read asked before a reply applied, and keep a failure said under a reply asked before it', async () => {
    {
      const { c, revision } = await candidates()
      revision.value = 2
      await tick(250)
      expect(pagesSent).toHaveLength(2)
      pagesSent[1]!.ok(page)
      await tick()
      pagesSent[0]!.fail(new Error('the server could not be reached'))
      await tick()
      expect([c.error.value, c.tasks.value.map((t) => t.task)]).toEqual(['', ['review-a']])
    }
    // The later read fails, then the earlier answers: its page is shown, and the failure, the newest answer, stays said.
    const { c, revision } = await candidates('crew-0000bbbb')
    revision.value = 2
    await tick(250)
    expect(pagesSent.slice(2).map((p) => p.run)).toEqual(['crew-0000bbbb', 'crew-0000bbbb'])
    pagesSent[3]!.fail(new Error('the server could not be reached'))
    await tick()
    pagesSent[2]!.ok(page)
    await tick()
    expect([c.error.value, c.tasks.value.map((t) => t.task)]).toEqual(['the server could not be reached', ['review-a']])
  })

  it("read the repairs' tasks after the pages: a record's repair that stopped before it submitted, and the task a review reviews", async () => {
    const { c, revision } = await candidates()
    revision.value = undefined
    const failed: RunCandidate = { task: 'build', kind: 'work', attempt: 1, start: 'b0', commit: 'c1', state: 'failed', standing: false, withdrawn: false, receipts: [{ phase: 'check', id: 'unit', k: 1, where: 'native', state: 'finished', exit: 1, timedOut: false, killed: false, stable: true, startedAt: '2026-11-01T10:00:00Z', logBytes: 0, logDropped: false }] }
    const asked: RunCandidate = { ...review('review'), start: 'c9' }
    pagesSent[0]!.ok({ managed: true, candidates: [{ task: 'build', kind: 'work', more: 0, candidates: [failed] }, { task: 'review', kind: 'review', more: 0, candidates: [asked] }] } as RunCandidatesPage)
    await tick()
    expect(tasksSent.map((t) => t.task)).toEqual(['build', 'review'])
    expect(repairs(c.tasks.value, c.details).map((r) => r.unread)).toEqual([['build'], ['review']])
    // build/2 started at the failed result and was stopped (the server restarted): no candidate names it, its task's read does.
    tasksSent[0]!.ok({ task: { id: 'build', kind: 'work' } as RunTask, attempts: [{ n: 1, start: 'b0', result: 'c1', state: 'failed' }, { n: 2, start: 'c1', state: 'stopped', startedAt: '2026-11-01T10:05:00Z' }] as RunAttempt[] })
    await tick()
    expect(repairs(c.tasks.value, c.details)[0]!.by).toEqual([{ task: 'build', attempt: 2, state: 'stopped', commit: undefined }])
    // The review's read names the task it reviews, which no candidate pointed at: it is read then (build, read this round, is not again).
    tasksSent[1]!.ok({ task: { id: 'review', kind: 'review', needs: ['deploy'] } as RunTask, attempts: [{ n: 1, start: 'c9', state: 'succeeded', submitted: { verdict: 'needs_changes', findings: [] } }] as unknown as RunAttempt[] })
    await tick()
    expect(tasksSent.map((t) => t.task)).toEqual(['build', 'review', 'deploy'])
    tasksSent[2]!.fail(new Error('the task could not be read'))
    await tick()
    expect([repairs(c.tasks.value, c.details)[1]!.unread, c.detailsError.get('deploy')]).toEqual([['deploy'], 'the task could not be read'])
  })

  it('read the pages again, gathered for the window, when the run revision moves', async () => {
    const { c, revision } = await candidates()
    pagesSent[0]!.ok(page)
    await tick()
    revision.value = 2
    await tick(10)
    revision.value = 3
    await tick(240)
    expect(pagesSent).toHaveLength(2)
    pagesSent[1]!.ok(page)
    await tick(1000)
    expect(pagesSent).toHaveLength(2)
    expect(c.pages.value).toHaveLength(1)
  })
})
