import { beforeEach, describe, expect, it, vi } from 'vitest'
import * as vue from 'vue'
import { ApiError } from './useApi'
import { ContinuationNeeded, useReview, type CandidateDiff, type Review } from './useReview'
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
