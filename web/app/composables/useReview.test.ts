import { beforeEach, describe, expect, it, vi } from 'vitest'
import * as vue from 'vue'
import { ApiError } from './useApi'
import {
  ContinuationNeeded,
  MAX_REVIEWS,
  openFindingsOf,
  openFindingsOfTask,
  useCandidateDiff,
  useFileSides,
  useLaunchedCriteria,
  useNamedCandidate,
  usePreparedRepair,
  useRecheckAsk,
  useReview,
  useSentReviews,
  useShowWhenReady,
  useTaskFindings,
  type CandidateDiff,
  type ReviewFinding,
  type Review,
  type ReviewSummary,
} from './useReview'
import type { PreparedRepair } from '../utils/review'
import type { RunCandidate } from './useDispatch'
import { DispatchOrder } from '../utils/dispatchOrder'
import { CommandRefused } from '../utils/tasks'
import { reviewBody } from '../utils/review'

// A review's reads and writes (Program item RV1, PR-6) against replies the test holds and lets go in the order it picks: each read's
// reply through the page's ordering gate, so a slow one never replaces a newer; a write's lost reply asked once more with the same op
// id; the server's continuation_needed a ContinuationNeeded. useReview reads Nuxt's auto-imports (useApi, useIdentity) as globals: each
// is stubbed, the API's requests held by path.

function held<T>() {
  let ok!: (v: T) => void
  let fail!: (e: unknown) => void
  const p = new Promise<T>((resolve, reject) => {
    ok = resolve
    fail = reject
  })
  return { p, ok, fail }
}

interface Sent {
  path: string
  body?: Record<string, unknown>
  h: ReturnType<typeof held<unknown>>
}
let sent: Sent[]
function request(path: string, opts: { method?: string; body?: Record<string, unknown> } = {}) {
  const h = held<unknown>()
  sent.push({ path, body: opts.body ? structuredClone(opts.body) : undefined, h })
  return h.p
}
const settle = () => new Promise((resolve) => setTimeout(resolve, 0))

const C = 'a'.repeat(40)
const B = 'b'.repeat(40)
const S = 's'.repeat(40)
const ref = { run: 'run-1', task: 'build', attempt: 1, commit: C }
const cand = `run-1/build/1@${C}`
const diff = (base: string): CandidateDiff => ({ candidate: C, base, files: [{ path: 'a.txt', status: 'M', added: 1, removed: 0, binary: false }] })
const review = (rev: number, state: Review['application']['state']): Review =>
  ({ id: 'r1', ticketId: 't1', ticketKey: 'CND-12', candidate: cand, base: B, verdict: 'request_changes', summary: '', createdAt: '', createdBy: 'Nate', actor: 'admin', via: 'web', application: { state }, rev, counts: { open: 1, resolved: 0, withdrawn: 0 }, findings: [] }) as Review
const receipt = { opId: 'op', rev: 1, intent: 'review.submit', status: 'done' }

beforeEach(() => {
  sent = []
  vi.stubGlobal('useApi', () => ({ request, requestText: (path: string) => request(path) }))
  vi.stubGlobal('useIdentity', () => ({ name: vue.ref(' Nate ') }))
  vi.stubGlobal('ref', vue.ref)
})

describe('the reads, through the ordering gate', () => {
  it('drop a slow diff for the base asked first once the later one is shown', async () => {
    const gate = new DispatchOrder()
    const r = useReview(gate)
    const shown: string[] = []
    const first = r.readDiff(ref, cand, '', (d) => shown.push(d.base))
    const second = r.readDiff(ref, cand, '', (d) => shown.push(d.base))
    expect(sent.map((s) => s.path)).toEqual(['/api/runs/run-1/tasks/build/attempts/1/diff', '/api/runs/run-1/tasks/build/attempts/1/diff'])
    sent[1]!.h.ok(diff(S))
    expect(await second).toBe(true)
    sent[0]!.h.ok(diff(B))
    expect(await first).toBe(false)
    expect(shown).toEqual([S])
    void r.readDiff(ref, cand, S, () => {})
    expect(sent[2]!.path).toBe(`/api/runs/run-1/tasks/build/attempts/1/diff?base=${S}`)
  })

  it("keep a review at the newest revision whatever order its replies come in", async () => {
    const gate = new DispatchOrder()
    const r = useReview(gate)
    let shown: Review | null = null
    const a = r.readReview('r1', (v) => (shown = v))
    const b = r.readReview('r1', (v) => (shown = v))
    sent[0]!.h.ok({ review: review(3, 'applied') })
    await a
    sent[1]!.h.ok({ review: review(2, 'pending') })
    expect(await b).toBe(false)
    expect(shown!.application.state).toBe('applied')
  })
})

describe('the writes', () => {
  const body = reviewBody(cand, B, 'request_changes', '', [{ key: 'k', kind: 'general', intent: 'change', severity: 'minor', message: 'do it' }])

  it('ask once more with the same op id when the reply is lost, the display name with it', async () => {
    const gate = new DispatchOrder()
    const r = useReview(gate)
    let shown: Review | null = null
    const p = r.submit('CND-12', body, { opId: 'op-kept' }, (v) => (shown = v))
    expect(sent[0]!.path).toBe('/api/dispatch/tickets/CND-12/reviews')
    expect(sent[0]!.body).toMatchObject({ opId: 'op-kept', byName: 'Nate', candidate: cand, base: B, verdict: 'request_changes', via: 'web' })
    expect(sent[0]!.body).not.toHaveProperty('continueRun')
    sent[0]!.h.fail(new ApiError(0, 'network', 'Cannot reach the server'))
    await settle()
    expect(sent).toHaveLength(2)
    expect(sent[1]!.body).toEqual(sent[0]!.body)
    sent[1]!.h.ok({ receipt: { ...receipt, repeat: true }, review: review(2, 'applied') })
    expect((await p).receipt).toMatchObject({ repeat: true })
    expect(shown!.application.state).toBe('applied')
  })

  it('mint an op id when none is kept, and a second loss is the error', async () => {
    const r = useReview(new DispatchOrder())
    const p = r.submit('CND-12', body, {})
    const id = sent[0]!.body!.opId as string
    expect(id).toMatch(/^[0-9a-f]{16}$/)
    sent[0]!.h.fail(new ApiError(0, 'network', 'lost'))
    await settle()
    expect(sent[1]!.body!.opId).toBe(id)
    sent[1]!.h.fail(new ApiError(0, 'network', 'lost again'))
    await expect(p).rejects.toThrow('lost again')
  })

  it("turn the server's continuation_needed into a question, and send continueRun when the person says so", async () => {
    const r = useReview(new DispatchOrder())
    const p = r.submit('CND-12', body, { opId: 'op-1' })
    sent[0]!.h.fail(new ApiError(409, 'continuation_needed', 'Yardmaster continues run R as a new run', {}, { error: {}, run: 'run-1', words: 'Yardmaster continues run run-1 as a new run' }))
    const e = await p.catch((x) => x)
    expect(e).toBeInstanceOf(ContinuationNeeded)
    expect(e).toMatchObject({ run: 'run-1', words: 'Yardmaster continues run run-1 as a new run' })
    void r.submit('CND-12', body, { opId: 'op-1', continueRun: true })
    expect(sent[1]!.body).toMatchObject({ opId: 'op-1', continueRun: true })
  })

  it('throw a refusal with its receipt as a CommandRefused, and a 413 as it came', async () => {
    const r = useReview(new DispatchOrder())
    const p = r.retry('r1', { opId: 'op-r', expectedRev: 4 })
    expect(sent[0]!.path).toBe('/api/dispatch/reviews/r1/retry')
    expect(sent[0]!.body).toMatchObject({ opId: 'op-r', expectedRev: 4, byName: 'Nate' })
    sent[0]!.h.fail(new ApiError(409, 'bound', 'past its fifth try', {}, { receipt: { ...receipt, status: 'refused', refusal: 'bound', error: 'past its fifth try' } }))
    await expect(p).rejects.toBeInstanceOf(CommandRefused)
    const q = r.submit('CND-12', body, {})
    sent[1]!.h.fail(new ApiError(413, 'too_large', 'with build\'s own instructions the task file would be 74 KiB, more than 64 KiB: shorten the notes or their paths'))
    await expect(q).rejects.toMatchObject({ status: 413, code: 'too_large' })
  })
})

describe('the reads of every page', () => {
  const summary = (id: string, candidate: string, open: number) => ({ ...review(1, 'applied'), id, candidate, counts: { open, resolved: 0, withdrawn: 0 } }) as ReviewSummary

  it("follow the reviews' next cursor and apply every page at once", async () => {
    const gate = new DispatchOrder()
    const r = useReview(gate)
    let shown: ReviewSummary[] | null = null
    const p = r.readReviews('CND-12', (v) => (shown = v))
    expect(sent[0]!.path).toBe('/api/dispatch/tickets/CND-12/reviews')
    sent[0]!.h.ok({ reviews: [summary('r3', cand, 1)], next: 'cur-1' })
    await settle()
    expect(shown).toBeNull()
    expect(sent[1]!.path).toBe('/api/dispatch/tickets/CND-12/reviews?before=cur-1')
    sent[1]!.h.ok({ reviews: [summary('r2', cand, 0), summary('r1', `run-1/build/2@${C}`, 2)] })
    expect(await p).toBe(true)
    expect(shown!.map((x) => x.id)).toEqual(['r3', 'r2', 'r1'])
  })

  it("stop at the store's bound of a ticket's reviews", async () => {
    const r = useReview(new DispatchOrder())
    let shown: ReviewSummary[] = []
    const p = r.readReviews('CND-12', (v) => (shown = v))
    for (let i = 0; i < MAX_REVIEWS / 20; i++) {
      await settle()
      sent[i]!.h.ok({ reviews: Array.from({ length: 20 }, (_, j) => summary(`r${i}-${j}`, cand, 0)), next: `cur-${i}` })
    }
    expect(await p).toBe(true)
    expect(sent).toHaveLength(MAX_REVIEWS / 20)
    expect(shown).toHaveLength(MAX_REVIEWS)
  })

  it("count a task's open findings from its reviews' counts, whatever page they came on", () => {
    const rs = [summary('r3', cand, 1), summary('r2', `run-1/build/2@${C}`, 2), summary('r1', `run-1/test/1@${C}`, 4), summary('r0', `run-2/build/1@${C}`, 8)]
    expect(openFindingsOf(rs, 'run-1', 'build')).toBe(3)
    expect(openFindingsOf(rs, 'run-1', 'test')).toBe(4)
    expect(openFindingsOf(rs, 'run-3', 'build')).toBe(0)
  })
})

describe('the diff shown, bound to the candidate and the base wanted', () => {
  const Y = 'y'.repeat(40)
  const Z = 'z'.repeat(40)
  const at = (base: string) => `/api/runs/run-1/tasks/build/attempts/1/diff${base ? `?base=${base}` : ''}`

  it('drops a reply for a base left (Y, then Z, then Y again) and shows only the base wanted', async () => {
    const known: string[] = []
    const d = useCandidateDiff(useReview(new DispatchOrder()), { known: (b) => known.push(b) })
    const first = d.read(cand, '')
    sent[0]!.h.ok(diff(Y))
    await first
    expect(d.shown.value).toMatchObject({ key: cand, at: '', diff: { base: Y } })
    expect(known).toEqual([Y])
    // The draft's base is Y now: read at it, the diff shown is it already.
    await d.read(cand, Y)
    expect(sent).toHaveLength(1)
    expect(d.at(cand, Y)).toBe(true)

    const toZ = d.read(cand, Z)
    expect(d.shown.value).toBeNull()
    expect(d.at(cand, Y)).toBe(false)
    const back = d.read(cand, Y)
    expect(sent.map((s) => s.path)).toEqual([at(''), at(Z), at(Y)])
    sent[1]!.h.ok(diff(Z))
    await toZ
    expect(d.shown.value).toBeNull()
    expect(d.at(cand, Z)).toBe(false)
    expect(d.loading.value).toBe(true)
    sent[2]!.h.ok(diff(Y))
    await back
    expect(d.shown.value!.diff.base).toBe(Y)
    expect(d.at(cand, Y)).toBe(true)
    expect(d.loading.value).toBe(false)
    expect(known).toEqual([Y, Y])
  })

  it('shows nothing of the base left while a read fails, and nothing of another candidate', async () => {
    const d = useCandidateDiff(useReview(new DispatchOrder()))
    const first = d.read(cand, Y)
    sent[0]!.h.ok(diff(Y))
    await first
    const toZ = d.read(cand, Z)
    sent[1]!.h.fail(new ApiError(410, 'commit_gone', 'a commit of the diff is no longer in the repository'))
    await toZ
    expect(d.shown.value).toBeNull()
    expect(d.error.value).toMatch(/no longer in the repository/)
    expect(d.at(cand, Y)).toBe(false)
    expect(d.at(cand, Z)).toBe(false)

    const other = `run-1/build/2@${'c'.repeat(40)}`
    const toOther = d.read(other, Y)
    const again = d.read(cand, Y)
    sent[3]!.h.ok(diff(Y))
    await again
    sent[2]!.h.ok({ ...diff(Y), candidate: 'c'.repeat(40) })
    await toOther
    expect(d.shown.value).toMatchObject({ key: cand, diff: { candidate: C } })
    expect(d.at(other, Y)).toBe(false)
    expect(d.error.value).toBe('')
  })
})

describe("a stream's reset under the Review view", () => {
  it('reads again the diff whose reply the reset dropped, which nothing else would ask for', async () => {
    const gate = new DispatchOrder()
    const d = useCandidateDiff(useReview(gate))
    const first = d.read(cand, B)
    gate.reset()
    sent[0]!.h.ok(diff(B))
    await first
    expect(d.shown.value).toBeNull()
    expect(d.loading.value).toBe(false)
    const again = d.again()
    expect(sent).toHaveLength(2)
    expect(d.loading.value).toBe(true)
    sent[1]!.h.ok(diff(B))
    await again
    expect(d.shown.value).toMatchObject({ key: cand, at: B, diff: { base: B } })
    // Shown: nothing is read again.
    await d.again()
    expect(sent).toHaveLength(2)
  })

  it("reads again a file's sides whose replies the reset dropped, and an older read's late answer applies nothing", async () => {
    const gate = new DispatchOrder()
    const fs = useFileSides(useReview(gate))
    const t = { key: `${cand}:${B}:a.txt`, candidate: cand, base: B, file: { path: 'a.txt', status: 'M' as const, added: 1, removed: 0, binary: false } }
    const first = fs.read(t)
    expect(sent).toHaveLength(2)
    gate.reset()
    sent[0]!.h.ok({ text: 'old', headers: new Headers({ 'X-Conductor-Commit': B }) })
    await settle()
    expect(fs.sides.value).toMatchObject({ base: null, candidate: null })
    const again = fs.again()
    expect(sent).toHaveLength(4)
    sent[2]!.h.ok({ text: 'base text', headers: new Headers({ 'X-Conductor-Commit': B }) })
    sent[3]!.h.ok({ text: 'candidate text', headers: new Headers({ 'X-Conductor-Commit': C }) })
    await again
    sent[1]!.h.fail(new ApiError(500, 'internal', 'the first read failed late'))
    await first
    expect(fs.sides.value).toMatchObject({ key: t.key, base: { text: 'base text' }, candidate: { text: 'candidate text' }, error: '' })
    expect(fs.again()).toBeUndefined()
  })

  it("says nothing of an older read of the reviews failing after a newer one answered, and keeps the reviews read beside a failure", async () => {
    const gate = new DispatchOrder()
    const rv = useSentReviews(useReview(gate), () => 'CND-12')
    const older = rv.read()
    const newer = rv.read()
    sent[1]!.h.ok({ reviews: [review(1, 'applied')] })
    await newer
    sent[0]!.h.fail(new ApiError(500, 'internal', 'the older read failed'))
    await older
    expect(rv.error.value).toBe('')
    expect(rv.list.value.map((r) => r.id)).toEqual(['r1'])
    // The latest read failing is said, the reviews read before still listed.
    const third = rv.read()
    sent[2]!.h.fail(new ApiError(500, 'internal', 'the store is away'))
    await third
    expect(rv.error.value).toBe('the store is away')
    expect(rv.list.value.map((r) => r.id)).toEqual(['r1'])
  })
})

describe('a Review view closed', () => {
  it("asks no read of the reviews, so the view shown keeps its own: a send answered after its view closed", async () => {
    const gate = new DispatchOrder()
    const closedScope = vue.effectScope()
    const closed = closedScope.run(() => useSentReviews(useReview(gate), () => 'CND-12'))!
    const shown = useSentReviews(useReview(gate), () => 'CND-12')
    const visible = shown.read()
    closedScope.stop()
    // What the closed view's send would ask once its answer came: nothing, the gate's turn left to the view shown.
    await closed.read()
    expect(sent).toHaveLength(1)
    sent[0]!.h.ok({ reviews: [review(1, 'applied')] })
    await visible
    expect(shown.list.value.map((r) => r.id)).toEqual(['r1'])
    expect(shown.error.value).toBe('')
  })
})

describe("a note's lines shown once its file is ready", () => {
  function view() {
    const key = vue.ref('')
    const ready = vue.ref(false)
    const shown: string[] = []
    const w = useShowWhenReady({ key: () => key.value, ready: () => ready.value })
    return { key, ready, shown, w }
  }

  it('are shown at once when the file is ready, or when its sides come in', async () => {
    const { key, ready, shown, w } = view()
    key.value = 'c:b:a.txt'
    ready.value = true
    w.show('c:b:a.txt', () => shown.push('a.txt'))
    expect(shown).toEqual(['a.txt'])
    key.value = 'c:b:b.txt'
    ready.value = false
    w.show('c:b:b.txt', () => shown.push('b.txt'))
    await vue.nextTick()
    expect(shown).toEqual(['a.txt'])
    ready.value = true
    await vue.nextTick()
    await vue.nextTick()
    expect(shown).toEqual(['a.txt', 'b.txt'])
  })

  it("are never shown in another file: leaving the note's file before it is ready cancels them", async () => {
    const { key, ready, shown, w } = view()
    key.value = 'c:b:b.txt'
    w.show('c:b:b.txt', () => shown.push('b.txt lines'))
    key.value = 'c:b:c.txt'
    await vue.nextTick()
    ready.value = true
    await vue.nextTick()
    await vue.nextTick()
    expect(shown).toEqual([])
    // Back on b.txt and ready: the cancelled one stays cancelled.
    key.value = 'c:b:b.txt'
    await vue.nextTick()
    await vue.nextTick()
    expect(shown).toEqual([])
  })

  it('are cancelled by a later note opened, and by the view closing', async () => {
    const scope = vue.effectScope()
    const { key, ready, shown, w } = scope.run(view)!
    key.value = 'c:b:b.txt'
    w.show('c:b:b.txt', () => shown.push('first'))
    w.show('c:b:b.txt', () => shown.push('second'))
    ready.value = true
    await vue.nextTick()
    await vue.nextTick()
    expect(shown).toEqual(['second'])
    ready.value = false
    w.show('c:b:b.txt', () => shown.push('third'))
    scope.stop()
    ready.value = true
    await vue.nextTick()
    await vue.nextTick()
    expect(shown).toEqual(['second'])
  })
})

describe('a draft base the server refuses', () => {
  it('is told, so the page stops offering it, and only for a base asked', async () => {
    const refused: string[] = []
    const d = useCandidateDiff(useReview(new DispatchOrder()), { refused: (k, b) => refused.push(`${k}|${b}`) })
    const OLD = 'd'.repeat(40)
    const p = d.read(cand, OLD)
    sent[0]!.h.fail(new ApiError(400, 'invalid_request', 'the base is none the run names'))
    await p
    expect(refused).toEqual([`${cand}|${OLD}`])
    const q = d.read(cand, '')
    sent[1]!.h.fail(new ApiError(400, 'invalid_request', 'nope'))
    await q
    expect(refused).toHaveLength(1)
  })
})

// The Review view's panel of a failed check (Program item RV1, PR-7, review round 1): each reply applied only to the request that asked
// for it, by its generation and its identity, checked when it lands.
function preparedOf(receipt: string, run = 'run-1', attempt = 3): PreparedRepair {
  return {
    finding: { kind: 'check', intent: 'change', message: 'it failed', check: { source: 'ledger', receipt, run, task: 'build', attempt, phase: 'check', id: 'test', k: 1, subject: C, argv: ['false'], exit: 1, timedOut: false, killed: false, stable: true, state: 'finished', where: 'native', logBytes: 1, tail: 'no', class: 'code' } },
    criteria: { source: 'checks' },
    recheck: { allowed: true, run: 'run-2', revision: 7, attempt, commit: C },
  }
}

describe("a failed check's prepared repair", () => {
  it("drops a prepared request that lands once another candidate is shown, and says so", async () => {
    const r = useReview(new DispatchOrder())
    const shown = vue.ref(cand)
    const p = usePreparedRepair(r, () => shown.value)
    const asked = p.prepare(ref, 'test', 1)
    expect(sent[0]!.path).toBe('/api/runs/run-1/tasks/build/attempts/1/steps/test/1/repair')
    shown.value = `run-1/build/2@${B}`
    sent[0]!.h.ok(preparedOf('run-1/build/1/check/test/1'))
    await asked
    expect(p.prepared.value).toBeNull()
    expect(p.error.value).toContain('Another candidate is shown now')
    expect(p.loading.value).toBe(false)
  })

  it('drops an earlier prepare\'s reply once a later one was asked, whichever lands first', async () => {
    const r = useReview(new DispatchOrder())
    const p = usePreparedRepair(r, () => cand)
    const first = p.prepare(ref, 'test', 1)
    const second = p.prepare(ref, 'test', 2)
    sent[1]!.h.ok(preparedOf('run-1/build/1/check/test/2'))
    await second
    sent[0]!.h.ok(preparedOf('run-1/build/1/check/test/1'))
    await first
    expect(p.prepared.value?.finding.check.receipt).toBe('run-1/build/1/check/test/2')
  })

  it('sends Check again of the candidate the prepared request names, at its revision, not the one shown', async () => {
    const r = useReview(new DispatchOrder())
    const p = usePreparedRepair(r, () => `run-9/build/8@${B}`)
    const words = p.recheck(preparedOf('run-1/build/3/check/test/1'))
    expect(sent[0]!.path).toBe('/api/runs/run-1/tasks/build/recheck')
    expect(sent[0]!.body).toMatchObject({ attempt: 3, commit: C, expectedRevision: 7 })
    sent[0]!.h.ok({ receipt, run: 'run-2', attempt: 4 })
    expect(await words).toBe('Checked again as attempt 4 of run run-2, a continuation of run-1.')
  })

  it('reads the request again when the run refuses Check again as moved on, and asks at the new revision, the edits kept', async () => {
    const r = useReview(new DispatchOrder())
    const p = usePreparedRepair(r, () => cand)
    const asked = p.prepare(ref, 'test', 1)
    sent[0]!.h.ok(preparedOf('run-1/build/3/check/test/1'))
    await asked
    const shown = p.prepared.value!
    const panel = useRecheckAsk(() => p.prepared.value)
    // The continuation moved on between the read and the press (it had just started): refused, nothing done.
    const first = panel.again(p.recheck)
    expect(sent[1]!.body).toMatchObject({ expectedRevision: 7 })
    sent[1]!.h.fail(new ApiError(409, 'stale_revision', 'moved on', {}, { receipt: { ...receipt, status: 'failed', refusal: 'stale_revision', error: 'the run has moved on since the revision expected: expected 7, the run was at 8 when the command was taken and is at 9' } }))
    await settle()
    expect(sent[2]!.path).toBe('/api/runs/run-1/tasks/build/attempts/1/steps/test/1/repair')
    sent[2]!.h.ok({ ...preparedOf('run-1/build/3/check/test/1'), recheck: { allowed: true, run: 'run-2', revision: 9, attempt: 3, commit: C } })
    await first
    // The same request shown (its class, quote and words kept by the panel), its Check again at the run as it is now.
    expect(p.prepared.value).toBe(shown)
    expect(p.prepared.value!.recheck.revision).toBe(9)
    expect(panel.failure.value).toBe('The run moved on since the repair was prepared: nothing was done. It is read again: Check again asks the run as it is now.')
    expect(panel.rechecked.value).toBe('')
    const second = panel.again(p.recheck)
    expect(sent[3]!.body).toMatchObject({ attempt: 3, commit: C, expectedRevision: 9 })
    sent[3]!.h.ok({ receipt, run: 'run-2', attempt: 4 })
    await second
    expect(panel.rechecked.value).toBe('Checked again as attempt 4 of run run-2, a continuation of run-1.')
    expect(panel.failure.value).toBe('')
  })

  it('leaves a request prepared since alone when the moved-on read lands late', async () => {
    const r = useReview(new DispatchOrder())
    const p = usePreparedRepair(r, () => cand)
    const asked = p.prepare(ref, 'test', 1)
    sent[0]!.h.ok(preparedOf('run-1/build/3/check/test/1'))
    await asked
    const words = p.recheck(p.prepared.value!)
    sent[1]!.h.fail(new ApiError(409, 'stale_revision', 'moved on', {}, { receipt: { ...receipt, status: 'failed', refusal: 'stale_revision', error: 'moved on' } }))
    await settle()
    // Another receipt prepared while the request was read again.
    const later = p.prepare(ref, 'test', 2)
    sent[3]!.h.ok(preparedOf('run-1/build/3/check/test/2'))
    await later
    sent[2]!.h.ok({ ...preparedOf('run-1/build/3/check/test/1'), recheck: { allowed: false, run: 'run-2', revision: 9, attempt: 3, commit: C } })
    await expect(words).rejects.toThrow('The run moved on')
    expect(p.prepared.value!.finding.check.receipt).toBe('run-1/build/3/check/test/2')
    expect(p.prepared.value!.recheck).toMatchObject({ allowed: true, revision: 7 })
  })

  it("never writes a slow Check again's reply into the panel of the receipt prepared after it", async () => {
    const shown = vue.ref<PreparedRepair | null>(preparedOf('run-1/build/3/check/test/1'))
    const a = useRecheckAsk(() => shown.value)
    const slow = held<string>()
    const asked = a.again(() => slow.p)
    expect(a.busy.value).toBe(true)
    // The panel closed and Prepare a repair asked of another receipt: the panel resets for it.
    shown.value = preparedOf('run-1/build/3/check/other/1')
    a.reset()
    slow.ok('Checked again as attempt 4, the same commit as attempt 3.')
    await asked
    expect(a.rechecked.value).toBe('')
    expect(a.busy.value).toBe(false)
    // Its failure too.
    const failing = held<string>()
    const b = a.again(() => failing.p)
    shown.value = preparedOf('run-1/build/3/check/third/1')
    failing.fail(new Error('refused'))
    await b
    expect(a.failure.value).toBe('')
    // And the panel's own Check again is applied.
    const own = a.again(async () => 'Checked again as attempt 5, the same commit as attempt 3.')
    await own
    expect(a.rechecked.value).toBe('Checked again as attempt 5, the same commit as attempt 3.')
    expect(a.busy.value).toBe(false)
  })
})

describe("the criteria as a launch's package froze them", () => {
  it('say a failed read in words and are asked again at the next tick until one answers', async () => {
    const reads: Array<ReturnType<typeof held<{ package?: unknown }>>> = []
    const launch = vue.ref<string | undefined>('l1')
    const tick = vue.ref(0)
    const c = useLaunchedCriteria(
      () => {
        const h = held<{ package?: unknown }>()
        reads.push(h)
        return h.p
      },
      () => launch.value,
      () => tick.value,
    )
    expect(reads).toHaveLength(1)
    expect(c.text.value).toBeNull()
    reads[0]!.fail(new ApiError(503, 'ledger_unavailable', 'the ledger is unavailable'))
    await settle()
    expect(c.error.value).toBe('the ledger is unavailable')
    tick.value++
    await vue.nextTick()
    expect(reads).toHaveLength(2)
    reads[1]!.ok({ package: { ticket: { acceptance: 'It is built.' } } })
    await settle()
    expect(c.text.value).toBe('It is built.')
    expect(c.error.value).toBe('')
    // Taken: not read again for the same launch.
    tick.value++
    await vue.nextTick()
    expect(reads).toHaveLength(2)
  })
})

describe('the candidate shown, read by its id', () => {
  type Page = { base?: string; candidates?: Array<{ task: string; kind: 'work' | 'review'; candidates: RunCandidate[] }> }
  const cand = (attempt: number, commit: string, o: Partial<RunCandidate> = {}): RunCandidate => ({ task: 'build', kind: 'work', attempt, commit, state: 'failed', standing: false, withdrawn: false, receipts: [], ...o })
  function named(want: () => typeof ref | null, tick: () => unknown) {
    const reads: Array<{ args: [string, string, number]; h: ReturnType<typeof held<Page>> }> = []
    const o = useNamedCandidate({
      read: (run: string, task: string, before: number) => {
        const h = held<Page>()
        reads.push({ args: [run, task, before], h })
        return h.p
      },
      want,
      ticks: tick,
    })
    return { o, reads }
  }
  it("is read from its own run whatever the ticket's pages hold: a run past the first page of Runs, an attempt past its task's six newest", async () => {
    // Nothing of the ticket's runs is consulted: the read is asked at once, of the run, the task and the attempt the route names.
    const old = { run: 'run-11', task: 'build', attempt: 9, commit: C }
    const tick = vue.ref(0)
    const { o, reads } = named(() => old, () => tick.value)
    expect(reads.map((r) => r.args)).toEqual([['run-11', 'build', 10]])
    reads[0]!.h.fail(new Error('offline'))
    await settle()
    expect(o.error.value).toBe('offline')
    expect(o.candidate.value).toBeNull()
    tick.value++
    await vue.nextTick()
    const c = cand(9, C, { receipts: [{ phase: 'check', id: 'test', k: 1, where: 'native', state: 'finished', exit: 1, timedOut: false, killed: false, stable: true, startedAt: '', logBytes: 0, logDropped: false }] })
    reads[1]!.h.ok({ base: S, candidates: [{ task: 'build', kind: 'work', candidates: [c, cand(8, B), cand(7, S)] }] })
    await settle()
    expect(o.candidate.value).toEqual(c)
    expect(o.base.value).toBe(S)
    expect(o.earlier.value.map((x) => x.attempt)).toEqual([8, 7])
    expect(o.error.value).toBe('')
    // Read again at each tick (a run's change), what was read staying shown meanwhile.
    tick.value++
    await vue.nextTick()
    expect(reads).toHaveLength(3)
    expect(o.candidate.value).toEqual(c)
    reads[2]!.h.ok({ base: S, candidates: [{ task: 'build', kind: 'work', candidates: [{ ...c, state: 'succeeded' }] }] })
    await settle()
    expect(o.candidate.value?.state).toBe('succeeded')
  })
  it("says a page that does not hold it and a run the server knows nowhere, and drops a reply for a candidate no longer named", async () => {
    const want = vue.ref<typeof ref | null>(ref)
    const { o, reads } = named(() => want.value, () => 0)
    reads[0]!.h.ok({ candidates: [{ task: 'build', kind: 'work', candidates: [cand(1, B)] }] })
    await settle()
    expect(o.error.value).toBe(`attempt 1 of build in run run-1 has no candidate at ${C.slice(0, 12)}`)
    want.value = { run: 'run-2', task: 'build', attempt: 1, commit: C }
    await vue.nextTick()
    reads[1]!.h.fail(new ApiError(404, 'not_found', 'no such run'))
    await settle()
    expect(o.error.value).toBe('the server knows no run run-2')
    want.value = { run: 'run-3', task: 'build', attempt: 1, commit: C }
    await vue.nextTick()
    want.value = { run: 'run-4', task: 'build', attempt: 1, commit: C }
    await vue.nextTick()
    reads[2]!.h.ok({ candidates: [{ task: 'build', kind: 'work', candidates: [cand(1, C)] }] })
    await settle()
    expect(o.candidate.value).toBeNull()
  })
})

describe("the task's findings under the Review view", () => {
  it("are read again when a run changes while the same candidate stays shown: a repair's addressed notes come in", async () => {
    const asks: Array<{ key: string; at: string; apply: (f: ReviewFinding[]) => void; h: ReturnType<typeof held<void>> }> = []
    const r = {
      readFindings: (key: string, at: string, apply: (f: ReviewFinding[]) => void) => {
        const h = held<void>()
        asks.push({ key, at, apply, h })
        return h.p
      },
    } as unknown as Parameters<typeof useTaskFindings>[0]
    const runs = vue.ref<unknown[]>([])
    const shown = vue.ref(cand)
    const f = useTaskFindings(r, { ticket: () => 'CND-12', candidate: () => shown.value, ticks: () => [0, 0, runs.value] })
    expect(asks.map((a) => [a.key, a.at])).toEqual([['CND-12', cand]])
    const before = { id: 'f1', addressed: [] } as unknown as ReviewFinding
    asks[0]!.apply([before])
    asks[0]!.h.ok()
    await settle()
    expect(f.list.value).toEqual([before])
    // The repair attempt's result names f1 addressed: its run moves, the ticket's candidates are read again, and the findings with them.
    runs.value = [{ run: 'run-1' }]
    await vue.nextTick()
    expect(asks).toHaveLength(2)
    expect(asks[1]!.at).toBe(cand)
    const after = { id: 'f1', addressed: [{ run: 'run-1', attempt: 2, note: 'done' }] } as unknown as ReviewFinding
    asks[1]!.apply([after])
    asks[1]!.h.ok()
    await settle()
    expect(f.list.value).toEqual([after])
    // A reply for a candidate no longer shown is dropped, and the list is cleared at the move.
    shown.value = `run-1/build/2@${B}`
    await vue.nextTick()
    expect(f.list.value).toEqual([])
    asks[1]!.apply([before])
    expect(f.list.value).toEqual([])
  })
})

describe('the open findings of a task', () => {
  it("count every run's reviews of the task, as the Review view lists them", () => {
    const rv = (candidate: string, open: number) => ({ candidate, counts: { open, resolved: 0, withdrawn: 0 } }) as ReviewSummary
    const reviews = [rv(`run-1/build/1@${C}`, 2), rv(`run-2/build/4@${B}`, 1), rv(`run-1/prep/1@${C}`, 3)]
    expect(openFindingsOfTask(reviews, 'build')).toBe(3)
    expect(openFindingsOfTask(reviews, 'prep')).toBe(3)
    expect(openFindingsOf(reviews, 'run-1', 'build')).toBe(2)
  })
})
