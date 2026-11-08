import { describe, expect, it } from 'vitest'
import { lineageOf, lineageWords, type LineageInput } from './reviewLineage'

const c = (n: number) => String(n).repeat(40).slice(0, 40)
const A = c(1)
const B = c(2)
const C = c(3)
const S = 'f'.repeat(40)

function input(o: Partial<LineageInput>): LineageInput {
  return { attempts: [], reviews: [], reviewTasks: [], continues: new Map(), ...o }
}

describe("a candidate's lineage", () => {
  it("names a person's review the attempt took up, by the review's application, with who sent it", () => {
    const i = input({
      attempts: [
        { run: 'r1', task: 'build', attempt: 1, start: S, commit: A, state: 'succeeded' },
        { run: 'r1', task: 'build', attempt: 2, start: A, commit: B, state: 'succeeded' },
      ],
      reviews: [{ id: '0123456789abcdef', candidate: `r1/build/1@${A}`, createdBy: 'Nate', verdict: 'request_changes', application: { state: 'applied', run: 'r1', attempt: 2 } }],
    })
    const l = lineageOf({ run: 'r1', task: 'build', attempt: 2, commit: B, start: A }, i)
    expect(l.cause).toEqual({ kind: 'review', review: '0123456789abcdef', by: 'Nate', from: { run: 'r1', attempt: 1, commit: A } })
    expect(lineageWords(l)).toBe('attempt 2 of build, the repair of review 0123456 by Nate on attempt 1 (111111111111)')
    expect(lineageWords(lineageOf({ run: 'r1', task: 'build', attempt: 1, commit: A, start: S }, i))).toBe('attempt 1 of build, its first')
  })

  it("names a review task's needs_changes when the attempt started at the commit it reviewed", () => {
    const i = input({
      attempts: [
        { run: 'r1', task: 'build', attempt: 1, start: S, commit: A, state: 'succeeded' },
        { run: 'r1', task: 'build', attempt: 2, start: A, commit: B, state: 'succeeded' },
      ],
      reviewTasks: [{ run: 'r1', task: 'check', attempt: 1, reviewed: A, verdict: 'needs_changes' }],
    })
    const l = lineageOf({ run: 'r1', task: 'build', attempt: 2, commit: B, start: A }, i)
    expect(l.cause).toEqual({ kind: 'review_task', task: 'check', attempt: 1, from: { run: 'r1', attempt: 1, commit: A } })
    expect(lineageWords(l)).toBe('attempt 2 of build, the repair the review task check (attempt 1) asked for on attempt 1 (111111111111)')
  })

  it("pairs each reopening with the review that asked for it when the same commit is submitted again unchanged, never a later review", () => {
    // Review attempt 1 asks for changes on A; work attempt 2 starts at A and submits A unchanged; review attempt 2 asks again on A; work
    // attempt 3 starts at A. Attempt 2 is review 1's repair: review 2 ran after it.
    const i = input({
      attempts: [
        { run: 'r1', task: 'build', attempt: 1, start: S, commit: A, state: 'succeeded' },
        { run: 'r1', task: 'build', attempt: 2, start: A, commit: A, state: 'succeeded' },
        { run: 'r1', task: 'build', attempt: 3, start: A, commit: B, state: 'succeeded' },
      ],
      reviewTasks: [
        { run: 'r1', task: 'check', attempt: 2, reviewed: A, verdict: 'needs_changes' },
        { run: 'r1', task: 'check', attempt: 1, reviewed: A, verdict: 'needs_changes' },
      ],
    })
    expect(lineageOf({ run: 'r1', task: 'build', attempt: 2, commit: A, start: A }, i).cause).toEqual({ kind: 'review_task', task: 'check', attempt: 1, from: { run: 'r1', attempt: 1, commit: A } })
    expect(lineageOf({ run: 'r1', task: 'build', attempt: 3, commit: B, start: A }, i).cause).toEqual({ kind: 'review_task', task: 'check', attempt: 2, from: { run: 'r1', attempt: 2, commit: A } })
    // With only the first review sent, attempt 3 (were it there) is no review's repair: the second request has not been made.
    const one = { ...i, reviewTasks: [i.reviewTasks[1]!] }
    expect(lineageOf({ run: 'r1', task: 'build', attempt: 2, commit: A, start: A }, one).cause.kind).toBe('review_task')
    expect(lineageOf({ run: 'r1', task: 'build', attempt: 3, commit: B, start: A }, one).cause.kind).toBe('again')
  })

  it("keeps a review task's reopening of build when a person's review of prep's attempt of the same number was applied", () => {
    // Attempts are numbered per task: prep's attempt 2, a person's repair, is not build's attempt 2, the review task's reopening.
    const i = input({
      attempts: [
        { run: 'r1', task: 'build', attempt: 1, start: S, commit: A, state: 'succeeded' },
        { run: 'r1', task: 'build', attempt: 2, start: A, commit: B, state: 'succeeded' },
      ],
      reviewTasks: [{ run: 'r1', task: 'check', attempt: 1, reviewed: A, verdict: 'needs_changes' }],
      reviews: [{ id: '0000000000000002', candidate: `r1/prep/1@${C}`, createdBy: 'Nate', verdict: 'request_changes', application: { state: 'applied', run: 'r1', attempt: 2 } }],
    })
    expect(lineageOf({ run: 'r1', task: 'build', attempt: 2, commit: B, start: A }, i).cause).toEqual({ kind: 'review_task', task: 'check', attempt: 1, from: { run: 'r1', attempt: 1, commit: A } })
    // The same review of build's own attempt 1 makes build's attempt 2 its repair, the review task's request then left for the next.
    const own = { ...i, reviews: [{ ...i.reviews[0]!, candidate: `r1/build/1@${A}` }] }
    expect(lineageOf({ run: 'r1', task: 'build', attempt: 2, commit: B, start: A }, own).cause.kind).toBe('review')
  })

  it('names a failed check when the attempt started at the result that failed it, and a Check again when it is that same result', () => {
    const i = input({
      attempts: [
        { run: 'r1', task: 'build', attempt: 1, start: S, commit: A, state: 'failed', failed: 'check test' },
        { run: 'r1', task: 'build', attempt: 2, start: A, commit: B, state: 'succeeded' },
        { run: 'r1', task: 'build', attempt: 3, start: S, commit: A, state: 'succeeded' },
      ],
    })
    expect(lineageWords(lineageOf({ run: 'r1', task: 'build', attempt: 2, commit: B, start: A }, i))).toBe('attempt 2 of build, after check test failed on attempt 1 (111111111111)')
    const again = lineageOf({ run: 'r1', task: 'build', attempt: 3, commit: A, start: S }, i)
    expect(again.cause).toEqual({ kind: 'recheck', from: { run: 'r1', attempt: 1, commit: A } })
    expect(lineageWords(again)).toBe('attempt 3 of build, attempt 1 (111111111111) checked again, the same commit')
  })

  it('follows a continuation: an attempt of run B repairing or checking again a candidate of run A, which B continues', () => {
    const i = input({
      attempts: [
        { run: 'rA', task: 'build', attempt: 1, start: S, commit: A, state: 'failed', failed: 'check test' },
        // Check again in B: the engine records the failed attempt's start and result as the new attempt's.
        { run: 'rB', task: 'build', attempt: 2, start: S, commit: A, state: 'succeeded' },
        { run: 'rB', task: 'build', attempt: 3, start: A, commit: C, state: 'succeeded' },
      ],
      reviews: [{ id: 'fedcba9876543210', candidate: `rA/build/1@${A}`, createdBy: 'admin', verdict: 'request_changes', application: { state: 'applied', run: 'rB', attempt: 3 } }],
      continues: new Map([['rB', 'rA']]),
    })
    expect(lineageWords(lineageOf({ run: 'rB', task: 'build', attempt: 2, commit: A, start: S }, i))).toBe('attempt 2 of build, attempt 1 of run rA (111111111111) checked again, the same commit, in run rB, a continuation of rA')
    expect(lineageWords(lineageOf({ run: 'rB', task: 'build', attempt: 3, commit: C, start: A }, i))).toBe(
      'attempt 3 of build, the repair of review fedcba9 by admin on attempt 1 of run rA (111111111111), in run rB, a continuation of rA',
    )
  })

  it('never takes an ordinary attempt after a failed check that submitted the same commit for a Check again', () => {
    // The retry starts at the commit that failed (the engine's startCommit) and submits it unchanged: its start is its result.
    const i = input({
      attempts: [
        { run: 'r1', task: 'build', attempt: 1, start: S, commit: A, state: 'failed', failed: 'check test' },
        { run: 'r1', task: 'build', attempt: 2, start: A, commit: A, state: 'failed', failed: 'check test' },
      ],
    })
    const l = lineageOf({ run: 'r1', task: 'build', attempt: 2, commit: A, start: A }, i)
    expect(l.cause).toEqual({ kind: 'check', check: 'check test', from: { run: 'r1', attempt: 1, commit: A } })
    expect(lineageWords(l)).toBe('attempt 2 of build, after check test failed on attempt 1 (111111111111)')
    // Nor one whose start is not the failed attempt's: a Check again repeats both.
    expect(lineageOf({ run: 'r1', task: 'build', attempt: 3, commit: A, start: B }, i).cause.kind).not.toBe('recheck')
  })

  it("never takes another task's attempts or a refused review for a cause", () => {
    const i = input({
      attempts: [
        { run: 'r1', task: 'prep', attempt: 1, start: S, commit: A, state: 'failed', failed: 'check x' },
        { run: 'r1', task: 'build', attempt: 1, start: A, commit: B, state: 'succeeded' },
        { run: 'r1', task: 'build', attempt: 2, start: B, commit: C, state: 'succeeded' },
      ],
      reviews: [{ id: '0000000000000001', candidate: `r1/build/1@${B}`, createdBy: 'Nate', verdict: 'request_changes', application: { state: 'refused', run: 'r1' } }],
    })
    expect(lineageOf({ run: 'r1', task: 'build', attempt: 1, commit: B, start: A }, i).cause).toEqual({ kind: 'first' })
    expect(lineageOf({ run: 'r1', task: 'build', attempt: 2, commit: C, start: B }, i).cause).toEqual({ kind: 'again' })
  })
})
