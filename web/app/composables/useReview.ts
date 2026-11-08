import type { InjectionKey } from 'vue'
import { ApiError } from './useApi'
import type { DispatchReceipt, OpBody } from './useDispatch'
import { through, type DispatchOrder } from '~/utils/dispatchOrder'
import { mintCommandId, sendCommand } from '~/utils/tasks'
import type { CandidateRef } from '~/utils/dispatch'
import type { Located, NewFinding, ReviewApplication, ReviewBody, Side } from '~/utils/review'

/**
 * A person's reviews of a ticket's candidates (Program item RV1, docs/plans/review-repair.md, "web/app"): a candidate's diff and its
 * files' sides, the prepared failed-check request and Check again, a review sent and sent again, a ticket's reviews and findings, a
 * finding's marks. Every read goes through the page's ordering gate (utils/dispatchOrder.ts), so a slow reply never replaces a newer
 * one; every write mints its op id (or a command id) and asks once more with the same id when its reply is lost (utils/tasks.ts,
 * sendCommand), which the server answers from its receipt. A request the server would apply by continuing a run it does not run is
 * answered `continuation_needed`, thrown as ContinuationNeeded for the page to ask the person first.
 */

/** A file that differs between the base and the candidate. */
export interface DiffFile {
  path: string
  /** A rename's old path. */
  from?: string
  status: 'A' | 'M' | 'D' | 'R'
  added: number
  removed: number
  /** git calls it binary, or it is past 1 MiB: listed without its lines. */
  binary: boolean
}

/** GET /api/runs/{run}/tasks/{task}/attempts/{n}/diff: the candidate's commit, the base read against (the run's when none was asked). */
export interface CandidateDiff {
  candidate: string
  base: string
  files: DiffFile[]
  truncated?: boolean
}

/** One side of a file of the diff, as text: cut at 1 MiB (`truncated`), `commit` the one read. */
export interface DiffSide {
  text: string
  truncated: boolean
  commit: string
}

/** A finding of a review, as the store keeps it with its latest mark. */
export interface ReviewFinding extends NewFinding {
  id: string
  reviewId: string
  ticketId: string
  k: number
  anchorCommit?: string
  lines: string[]
  message: string
  createdAt: string
  candidate: string
  state: 'open' | 'resolved' | 'withdrawn'
  marks: number
  latest?: FindingMark
  /** On a ticket's findings: the candidate it was located on, and where its lines are there. */
  locatedOn?: string
  located?: Located
  addressed?: Array<{ run: string; attempt: number; note?: string }>
}

export interface FindingMark {
  id: string
  findingId: string
  candidate: string
  verdict: 'resolved' | 'open' | 'withdrawn'
  located?: Located
  note?: string
  createdAt: string
  createdBy: string
  actor: string
  via: string
}

/** A review without its findings, counted by state. */
export interface ReviewSummary {
  id: string
  ticketId: string
  ticketKey: string
  candidate: string
  base: string
  verdict: 'request_changes' | 'comment' | 'approve'
  summary: string
  createdAt: string
  createdBy: string
  actor: string
  via: string
  application: ReviewApplication
  rev: number
  counts: { open: number; resolved: number; withdrawn: number }
}

export interface Review extends ReviewSummary {
  findings: ReviewFinding[]
}

export interface ReviewReply {
  receipt: DispatchReceipt
  /** Null for a review kept whose read after its commit failed: answered by its receipt, its request left to the server's worker. */
  review: Review | null
}

/** How a review is applied, beside what is recorded: the run's revision expected, and the word to continue a run the server does not run. */
export interface ApplyOptions {
  expectedRevision?: number
  continueRun?: boolean
  /** The op id minted before the send, kept with the draft so a resend is the same operation. */
  opId?: string
}

/** A request the server would apply only by continuing a run it does not run: the run, and what continuing it starts, in words. */
export class ContinuationNeeded extends Error {
  constructor(
    public run: string,
    public words: string,
  ) {
    super(words)
  }
}

/** The failure of a write as the page shows it: a ContinuationNeeded for the server's continuation_needed, else as it came. */
function continuation(e: unknown): unknown {
  if (e instanceof ApiError && e.status === 409 && e.code === 'continuation_needed') {
    const b = (e.body ?? {}) as { run?: string; words?: string }
    return new ContinuationNeeded(b.run ?? '', b.words || e.message)
  }
  return e
}

const enc = encodeURIComponent
const attemptPath = (c: Pick<CandidateRef, 'run' | 'task' | 'attempt'>) => `/api/runs/${enc(c.run)}/tasks/${enc(c.task)}/attempts/${c.attempt}`

/** The routes, as calls. */
export function useReviewApi() {
  const { request, requestText } = useApi()
  return {
    candidateDiff: (c: CandidateRef, base?: string) => request<CandidateDiff>(`${attemptPath(c)}/diff` + (base ? `?base=${enc(base)}` : '')),
    diffFile: async (c: CandidateRef, path: string, side: Side, base?: string): Promise<DiffSide> => {
      const r = await requestText(`${attemptPath(c)}/diff/file?path=${enc(path)}&side=${side}` + (base ? `&base=${enc(base)}` : ''))
      return { text: r.text, truncated: r.headers.get('X-Conductor-Truncated') === '1', commit: r.headers.get('X-Conductor-Commit') ?? '' }
    },
    prepareRepair: (c: CandidateRef, step: string, k: number) => request<unknown>(`${attemptPath(c)}/steps/${enc(step)}/${k}/repair`),
    recheck: (run: string, task: string, body: { commandId: string; attempt: number; commit: string; expectedRevision?: number }) =>
      request<{ receipt: DispatchReceipt; run: string; attempt: number }>(`/api/runs/${enc(run)}/tasks/${enc(task)}/recheck`, { method: 'POST', body }),
    submitReview: (key: string, body: OpBody & ReviewBody & Omit<ApplyOptions, 'opId'>) => request<ReviewReply>(`/api/dispatch/tickets/${enc(key)}/reviews`, { method: 'POST', body }),
    retryReview: (id: string, body: OpBody & Omit<ApplyOptions, 'opId'>) => request<ReviewReply>(`/api/dispatch/reviews/${enc(id)}/retry`, { method: 'POST', body }),
    ticketReviews: (key: string, before = '') => request<{ reviews: ReviewSummary[]; next?: string }>(`/api/dispatch/tickets/${enc(key)}/reviews` + (before ? `?before=${enc(before)}` : '')),
    review: (id: string) => request<{ review: Review }>(`/api/dispatch/reviews/${enc(id)}`).then((r) => r.review),
    findings: (key: string, q: { state?: 'open' | 'resolved' | 'withdrawn' | 'all'; candidate?: string; after?: string } = {}) => {
      const qs = Object.entries(q)
        .filter(([, v]) => v)
        .map(([k, v]) => `${k}=${enc(v!)}`)
        .join('&')
      return request<{ findings: ReviewFinding[]; next?: string }>(`/api/dispatch/tickets/${enc(key)}/findings` + (qs ? `?${qs}` : ''))
    },
    findingMarks: (id: string, after = '') => request<{ marks: FindingMark[]; next?: string }>(`/api/dispatch/findings/${enc(id)}/marks` + (after ? `?after=${enc(after)}` : '')),
    markFinding: (id: string, body: OpBody & { candidate: string; verdict: FindingMark['verdict']; note?: string; via: 'web' }) =>
      request<{ receipt: DispatchReceipt; mark: FindingMark }>(`/api/dispatch/findings/${enc(id)}/marks`, { method: 'POST', body }),
  }
}

/** What the ticket page gives the candidates it lists (DispatchCandidates): the open findings of a run's task, and the Review view's link. */
export interface ReviewEntry {
  openFindings: (run: string, task: string) => number
  href: (candidate: string) => { query: Record<string, string> }
}
export const REVIEW_ENTRY: InjectionKey<ReviewEntry> = Symbol('review-entry')

/** The page's reads and writes of reviews, through its ordering gate. */
export function useReview(gate: DispatchOrder) {
  const api = useReviewApi()
  const identity = useIdentity()

  /** The op's members every write carries: its id (minted unless given) and the display name. */
  function op(opId?: string): OpBody {
    const body: OpBody = { opId: opId ?? mintCommandId() }
    const name = identity.name.value.trim()
    if (name) body.byName = name
    return body
  }

  /** The thing of the gate a candidate's diff at a base is. */
  const diffThing = (candidate: string, base: string) => `review-diff:${candidate}:${base}`

  return {
    api,
    /** The display name the server records the request by, or the actor's when none is set: what a batch is measured with. */
    by: () => identity.name.value.trim() || 'admin',
    /** The candidate's diff against the base ('' for the run's); applied when the gate takes it. */
    readDiff: (c: CandidateRef, candidate: string, base: string, apply: (d: CandidateDiff) => void) => through(gate, diffThing(candidate, base), () => api.candidateDiff(c, base || undefined), apply),
    /** One side of a file of the diff; applied when the gate takes it. */
    readSide: (c: CandidateRef, candidate: string, base: string, path: string, side: Side, apply: (s: DiffSide) => void) =>
      through(gate, `review-file:${candidate}:${base}:${side}:${path}`, () => api.diffFile(c, path, side, base || undefined), apply),
    /** The ticket's newest reviews, a page of 20. */
    readReviews: (key: string, apply: (r: ReviewSummary[]) => void) => through(gate, `reviews:${key}`, () => api.ticketReviews(key).then((p) => p.reviews ?? []), apply),
    /** A review whole, ordered by its revision. */
    readReview: (id: string, apply: (r: Review) => void) => through(gate, `review:${id}`, () => api.review(id), apply, (r) => r.rev),
    /** The ticket's open findings, a page. */
    readOpenFindings: (key: string, apply: (f: ReviewFinding[]) => void) => through(gate, `findings-open:${key}`, () => api.findings(key, { state: 'open' }).then((p) => p.findings ?? []), apply),

    /**
     * Sends a review; its reply's review through the gate as `review:<id>`, applied by `apply`. A lost reply is asked once more with
     * the same op id; a refusal with its receipt is a CommandRefused; a continuation_needed is a ContinuationNeeded; anything else
     * (413 too_large among them: nothing kept) as it came.
     */
    async submit(key: string, body: ReviewBody, o: ApplyOptions, apply?: (r: Review) => void): Promise<ReviewReply> {
      const n = gate.ask()
      const { opId, ...how } = o
      try {
        const reply = await sendCommand<OpBody & ReviewBody & Omit<ApplyOptions, 'opId'>, ReviewReply>((b) => api.submitReview(key, b), { ...op(opId), ...body, ...how })
        if (reply.review && gate.take(`review:${reply.review.id}`, n, reply.review.rev)) apply?.(reply.review)
        return reply
      } catch (e) {
        throw continuation(e)
      }
    },

    /** Request again: a refused request applied as its next try, the same rules as `submit`. */
    async retry(id: string, o: ApplyOptions & { expectedRev?: number }, apply?: (r: Review) => void): Promise<ReviewReply> {
      const n = gate.ask()
      const { opId, expectedRev, ...how } = o
      try {
        const reply = await sendCommand<OpBody & Omit<ApplyOptions, 'opId'>, ReviewReply>((b) => api.retryReview(id, b), { ...op(opId), ...(expectedRev ? { expectedRev } : {}), ...how })
        if (reply.review && gate.take(`review:${reply.review.id}`, n, reply.review.rev)) apply?.(reply.review)
        return reply
      } catch (e) {
        throw continuation(e)
      }
    },
  }
}
