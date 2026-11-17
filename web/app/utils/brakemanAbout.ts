import type { BrakemanBrief, BrakemanDigestAbout, BrakemanRequest, BrakemanRow, BrakemanState } from './brakeman'
import { closed, parseRef, requestNumber } from './brakeman'
import { relativeTime } from './sessions'
import { askOf, briefHeadWords, type AskBody } from './brakemanRows'

// What Brakeman said about one subject, where it was asked (issue 993, F8): a run's page, a task's row, a ticket's page. The subject
// half of the server's "about" rule (GET /api/brakeman/requests?about=, internal/brakeman), held equal to it by the one table both
// read, internal/brakeman/testdata/about-table.json; the cited half needs a brief's digest, which only the server's answer carries
// (a row's `match`). Nothing here reads a request's own run or session: those are the turn's, never what it was asked about.

/** How a request is about a subject, as the list's `match` says it: one of its subjects, or a ref its brief cites; '' for neither. */
export type AboutMatch = 'subject' | 'cited' | ''

/** The ref kinds a page asks about (the list route's `about=` takes these alone). */
export const ABOUT_KINDS = ['run', 'task', 'session', 'ticket'] as const

/**
 * Whether one of `subjects` is about the ref `about`: the ref itself, or one under it. A run holds its tasks and their attempts, a
 * task its attempts, a session its questions, a ticket its comments. A ref that does not parse, or of a kind no page asks about,
 * holds nothing.
 */
export function subjectUnder(about: string, subjects: readonly string[]): boolean {
  const a = parseRef(about)
  if (!a || !(ABOUT_KINDS as readonly string[]).includes(a.kind)) return false
  return subjects.some((s) => {
    const r = parseRef(s)
    if (!r) return false
    switch (a.kind) {
      case 'run':
        return (r.kind === 'run' || r.kind === 'task' || r.kind === 'attempt') && r.run === a.run
      case 'task':
        return (r.kind === 'task' || r.kind === 'attempt') && r.run === a.run && r.task === a.task
      case 'session':
        return (r.kind === 'session' || r.kind === 'question') && r.session === a.session
      case 'ticket':
        return (r.kind === 'ticket' || r.kind === 'comment') && r.ticket === a.ticket
    }
    return false
  })
}

/** Whether a brief's digest cites the ref `about` (its runs, its tasks as `<run>/<task>`, its sessions, its tickets). */
export function citedIn(about: string, cited: BrakemanDigestAbout | undefined): boolean {
  const a = parseRef(about)
  if (!a || !cited) return false
  switch (a.kind) {
    case 'run':
      return !!cited.runs?.includes(a.run!)
    case 'task':
      return !!cited.tasks?.includes(`${a.run}/${a.task}`)
    case 'session':
      return !!cited.sessions?.includes(a.session!)
    case 'ticket':
      return !!cited.tickets?.includes(a.ticket!)
  }
  return false
}

/** The whole rule, as the server applies it: a subject wins over a citation. */
export function matchOf(about: string, subjects: readonly string[], cited?: BrakemanDigestAbout): AboutMatch {
  if (subjectUnder(about, subjects)) return 'subject'
  return citedIn(about, cited) ? 'cited' : ''
}

/** Newest first, by the request's number. */
function newestFirst<R extends Pick<BrakemanRequest, 'id'>>(rows: readonly R[]): R[] {
  return [...rows].sort((a, b) => requestNumber(b.id) - requestNumber(a.id))
}

/** The newest request about the subject that has a brief: the panel's "Latest brief". */
export function latestBrief(rows: readonly BrakemanRow[]): BrakemanRow | undefined {
  return newestFirst(rows).find((r) => r.hasBrief)
}

/**
 * The requests still open about `about`, from Brakeman's state (the one under way, then the queue in its order), matched by their
 * subjects: a request's brief, which a citation needs, does not exist yet. Each with its place: 0 for the one under way, else its
 * place in the queue from 1.
 */
export function openAbout(state: Pick<BrakemanState, 'current' | 'queue'> | null | undefined, about: string): Array<{ request: BrakemanRequest; place: number }> {
  if (!state) return []
  const out: Array<{ request: BrakemanRequest; place: number }> = []
  if (state.current && !closed(state.current.state) && subjectUnder(about, state.current.subjects ?? [])) out.push({ request: state.current, place: 0 })
  state.queue.forEach((q, i) => {
    if (!closed(q.state) && subjectUnder(about, q.subjects ?? [])) out.push({ request: q, place: i + 1 })
  })
  return out
}

/**
 * The answer that stands under each task of `run`, by task id: the newest request one of whose subjects is the task or one of its
 * attempts. A request that only cites the task is under no row: a wake brief cites many, and would stand under each.
 */
export function answersByTask<R extends Pick<BrakemanRequest, 'id' | 'subjects'>>(rows: readonly R[], run: string): Record<string, R> {
  const out: Record<string, R> = {}
  for (const r of newestFirst(rows)) {
    for (const s of r.subjects ?? []) {
      const p = parseRef(s)
      if (!p || (p.kind !== 'task' && p.kind !== 'attempt') || p.run !== run) continue
      out[p.task!] ??= r
    }
  }
  return out
}

/** "1st", "2nd", "3rd", "4th", "11th", "22nd". */
export function ordinal(n: number): string {
  const tens = n % 100
  if (tens >= 11 && tens <= 13) return `${n}th`
  return `${n}${['th', 'st', 'nd', 'rd'][n % 10] ?? 'th'}`
}

/** A queued request's place, in words: "Queued: 2nd in line". */
export function queuedWords(place: number): string {
  return `Queued: ${ordinal(place)} in line`
}

/** What the page calls the subject's kind in its words: "run", "task", "ticket", "session". */
export function aboutNoun(about: string): string {
  return parseRef(about)?.kind ?? 'subject'
}

/** How the request is about the subject, in words that claim no more than the record keeps: never where it was asked from. */
export function matchWords(match: AboutMatch | undefined, about: string): string {
  const noun = aboutNoun(about)
  return match === 'cited' ? `mentions this ${noun}` : `about this ${noun}`
}

/** When a request was last heard of: its brief's time, else its close, else its asking. */
export function aboutAt(r: Pick<BrakemanRequest, 'briefAt' | 'closedAt' | 'createdAt'>): string {
  return r.briefAt || r.closedAt || r.createdAt
}

/**
 * The panel's line over its latest brief, the page's own head words (briefHeadWords: "Latest brief · 12m ago · woken by events", or
 * "· asked by Nate"), then "mentions this run" for an asked brief that cites the subject without being asked about it.
 */
export function latestWords(r: BrakemanRow, about: string, now: number): string {
  const head = briefHeadWords(r, true, now)
  return r.kind !== 'wake' && r.match === 'cited' ? `${head} · ${matchWords('cited', about)}` : head
}

/** The panel's words when Brakeman said nothing about the subject. */
export function noneWords(about: string): string {
  return `Brakeman has said nothing about this ${aboutNoun(about)}.`
}

/**
 * The line over a task row's answer: "Brakeman · about this task · 2h ago", with "before attempt N" when the request was asked before
 * the task's latest attempt started (what it says may be of an attempt before).
 */
export function answerWords(r: Pick<BrakemanRequest, 'createdAt' | 'briefAt' | 'closedAt'> & { match?: AboutMatch }, about: string, now: number, latestAttempt?: { n: number; startedAt?: string }): string {
  const parts = ['Brakeman', matchWords(r.match || 'subject', about), relativeTime(aboutAt(r), now, { suffix: true })]
  const started = latestAttempt?.startedAt ? Date.parse(latestAttempt.startedAt) : NaN
  if (latestAttempt && !Number.isNaN(started) && Date.parse(r.createdAt) < started) parts.push(`before attempt ${latestAttempt.n}`)
  return parts.join(' · ')
}

/**
 * The evidence a row's answer shows under its reading: the first decision's evidence, else its facts' in their order, each ref once,
 * at most `max`.
 */
export function answerEvidence(brief: Pick<BrakemanBrief, 'items'> | null | undefined, max = 3): string[] {
  const items = brief?.items ?? []
  const decision = items.find((i) => i.kind === 'decision/1')
  const refs = decision && decision.kind === 'decision/1' && decision.evidence.length ? decision.evidence : items.flatMap((i) => (i.kind === 'fact/1' ? i.evidence : []))
  return [...new Set(refs)].slice(0, max)
}

/**
 * The body a row's answer asks again (askOf: its kind, subjects, note and since), briefed or not, never a wake's (a person cannot ask
 * one). The answer is the task's newest request, so no same ask waits after it.
 */
export function askAgainBody(r: Pick<BrakemanRequest, 'kind' | 'subjects' | 'note' | 'since'>): AskBody | null {
  return r.kind === 'wake' ? null : askOf(r)
}
