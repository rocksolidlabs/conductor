import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as vue from 'vue'
import { ApiError } from './useApi'
import type { RunCandidate, RunCandidatesPage } from './useDispatch'
import type { RunAttempt, RunTask } from './useSessions'
import { DispatchOrder, through } from '../utils/dispatchOrder'

// A run's candidates as DispatchCandidates reads them (Program item DS2, PR-11), against replies the test lets go: a run_changing reply
// asked again after its second, on the first read too, with nothing said as a failure; two review tasks' findings opened while the
// first one's reply is held back, both lists shown. useRunCandidates reads Nuxt's auto-imports (Vue's, useDispatch, useSessions,
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

async function candidates(runId = 'crew-0000aaaa') {
  const { useRunCandidates } = await import('./useRunCandidates')
  const revision = vue.ref<number | undefined>(1)
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
