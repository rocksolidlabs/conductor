import { shortCommit } from './dispatchPlans'

/**
 * How each candidate of a ticket's task came to be (Program item RV1, docs/plans/review-repair.md, "web/app", PR-7): which attempt
 * repaired which person's review, which review task's needs_changes or which failed check, which attempt checked a failed commit again
 * (Check again), and through which continuation. Read from what the page already has: the runs' candidates (DS2: each attempt's start,
 * result and receipts), the ticket's reviews (each applied one names the run and the attempt that took it up), the review tasks'
 * verdicts and the runs a continuation continues. Provenance stops at the attempt: nothing here says who wrote a line. Pure.
 */

/** A work attempt that submitted a commit, in one run of the chain. */
export interface LineageAttempt {
  run: string
  task: string
  attempt: number
  start?: string
  commit?: string
  state: string
  /** The name of a receipt of it that failed ("check test"), when one did. */
  failed?: string
}

/** A person's review as the ticket lists it: the candidate it was of, who sent it, and the attempt that took up its request. */
export interface LineageReview {
  id: string
  candidate: string
  createdBy: string
  verdict: string
  application: { state: string; run?: string; attempt?: number }
}

/** A review task's attempt: the commit it reviewed (its start) and its verdict. */
export interface LineageReviewTask {
  run: string
  task: string
  attempt: number
  reviewed?: string
  verdict?: 'approved' | 'needs_changes'
}

/** Where a repair or a check again came from: the attempt before, in its run, at its commit. */
export interface LineageFrom {
  run: string
  attempt: number
  commit: string
}

export type LineageCause =
  | { kind: 'first' }
  | { kind: 'again' }
  | { kind: 'review'; review: string; by: string; from: LineageFrom }
  | { kind: 'review_task'; task: string; attempt: number; from: LineageFrom }
  | { kind: 'check'; check: string; from: LineageFrom }
  | { kind: 'recheck'; from: LineageFrom }

/** A candidate's lineage: the attempt, the run it is in (and the run that run continues), and what made it. */
export interface Lineage {
  run: string
  task: string
  attempt: number
  commit: string
  /** The run the attempt's run continues, when it is a continuation. */
  continues?: string
  cause: LineageCause
}

export interface LineageInput {
  attempts: readonly LineageAttempt[]
  reviews: readonly LineageReview[]
  reviewTasks: readonly LineageReviewTask[]
  /** By run: the run it continues. */
  continues: ReadonlyMap<string, string>
}

/** The runs of a run's chain, back to its first: the run itself, then the run it continues, and so on. */
function chainBack(run: string, continues: ReadonlyMap<string, string>): string[] {
  const out = [run]
  for (let r = continues.get(run); r && !out.includes(r) && out.length < 64; r = continues.get(r)) out.push(r)
  return out
}

function parseRef(s: string): { run: string; task: string; attempt: number; commit: string } | null {
  const m = /^([^/@]+)\/([^/@]+)\/([1-9][0-9]*)@([0-9a-f]{40}|[0-9a-f]{64})$/.exec(s)
  return m ? { run: m[1]!, task: m[2]!, attempt: Number(m[3]), commit: m[4]! } : null
}

/**
 * The lineage of one candidate, judged in this order: the repair of a person's review (an applied review names this run and attempt);
 * a Check again (it started at its own result, the commit an earlier attempt of the task failed a check on); the repair of a review
 * task's needs_changes (a review of this run's chain reviewed the commit this attempt started at, a result of the same task); the repair
 * of a failed check (an earlier attempt failed a check on the commit it started at); else its task's first attempt, or one more.
 */
export function lineageOf(c: { run: string; task: string; attempt: number; commit: string; start?: string }, input: LineageInput): Lineage {
  const chain = chainBack(c.run, input.continues)
  const continues = input.continues.get(c.run)
  const base = { run: c.run, task: c.task, attempt: c.attempt, commit: c.commit, ...(continues ? { continues } : {}) }
  const earlier = input.attempts.filter((a) => a.task === c.task && chain.includes(a.run) && a.attempt < c.attempt && a.commit)
  for (const r of input.reviews) {
    const a = r.application
    if (a.state !== 'applied' || a.run !== c.run || a.attempt !== c.attempt) continue
    const own = parseRef(r.candidate)
    if (!own || own.task !== c.task) continue
    return { ...base, cause: { kind: 'review', review: r.id, by: r.createdBy || 'admin', from: { run: own.run, attempt: own.attempt, commit: own.commit } } }
  }
  if (c.start && c.start === c.commit) {
    const p = earlier.filter((a) => a.commit === c.commit && a.failed).sort((x, y) => y.attempt - x.attempt)[0]
    if (p) return { ...base, cause: { kind: 'recheck', from: { run: p.run, attempt: p.attempt, commit: p.commit! } } }
  }
  if (c.start) {
    const p = earlier.filter((a) => a.commit === c.start).sort((x, y) => y.attempt - x.attempt)[0]
    if (p) {
      const rt = input.reviewTasks.filter((t) => chain.includes(t.run) && t.verdict === 'needs_changes' && t.reviewed === c.start).sort((x, y) => y.attempt - x.attempt)[0]
      if (rt) return { ...base, cause: { kind: 'review_task', task: rt.task, attempt: rt.attempt, from: { run: p.run, attempt: p.attempt, commit: p.commit! } } }
      if (p.failed) return { ...base, cause: { kind: 'check', check: p.failed, from: { run: p.run, attempt: p.attempt, commit: p.commit! } } }
    }
  }
  return { ...base, cause: { kind: earlier.length ? 'again' : 'first' } }
}

/**
 * A lineage in words: "attempt 3 of build, the repair of review 1a2b3c4 by Nate on attempt 2 (a1b2c3d4e5f6)", "attempt 2 of build, after
 * check test failed on attempt 1 (a1b2c3d4e5f6)", "attempt 4 of build, attempt 3's commit checked again (a1b2c3d)", with ", in run B, a
 * continuation of A" when the attempt's run is a continuation.
 */
export function lineageWords(l: Lineage): string {
  const head = `attempt ${l.attempt} of ${l.task}`
  const on = (f: LineageFrom) => `attempt ${f.attempt}${f.run !== l.run ? ` of run ${f.run}` : ''} (${shortCommit(f.commit)})`
  let what: string
  switch (l.cause.kind) {
    case 'review':
      what = `${head}, the repair of review ${l.cause.review.slice(0, 7)} by ${l.cause.by} on ${on(l.cause.from)}`
      break
    case 'review_task':
      what = `${head}, the repair the review task ${l.cause.task} (attempt ${l.cause.attempt}) asked for on ${on(l.cause.from)}`
      break
    case 'check':
      what = `${head}, after ${l.cause.check} failed on ${on(l.cause.from)}`
      break
    case 'recheck':
      what = `${head}, ${on(l.cause.from)} checked again, the same commit`
      break
    case 'again':
      what = `${head}, one more attempt`
      break
    default:
      what = `${head}, its first`
  }
  return l.continues ? `${what}, in run ${l.run}, a continuation of ${l.continues}` : what
}
