import { runCompare, type Comparison, type ComparisonSelection, type ComparisonSupporting } from './compare'
import type { Decision } from './decisions'
import { candidateRef } from './dispatch'
import { forWords } from './decisions'
import { stateWords, type BrakemanRequest } from './brakeman'

/*
 * A comparison's selection and what it says of each lane (Program item CP1, docs/plans/compare-crew.md, PR-7): the labels "selected"
 * and "not selected" on the comparison's columns, the run page's lanes and a run's candidates, read from the selection the comparison
 * carries once it is applied, never from what an agent, a review or Brakeman said (I6); and the selection panel's words before and
 * after there is a record to answer. The record is DR1's: its state is said by decisionWords, its answer asked by DecisionRespond.
 */

/** What a lane or a candidate is, once a selection is applied. */
export type SelectionMark = 'selected' | 'not-selected'

/** A mark in words: "selected", "not selected". */
export const SELECTION_WORDS: Record<SelectionMark, string> = { selected: 'selected', 'not-selected': 'not selected' }

/** The selection's candidate once it is applied; 'none' for None of these; null while none is applied. */
export function appliedSelection(sel: ComparisonSelection | null | undefined): { task: string; run: string; attempt: number; commit: string } | 'none' | null {
  if (!sel || sel.state !== 'applied' || !sel.applied) return null
  if (sel.applied.none) return 'none'
  return sel.applied.selected ?? null
}

/**
 * A lane's mark, by its candidate's task: selected for the selected candidate's lane, not selected for every other lane (and for each
 * after None of these); none while the selection is not applied, and none for a task that is no lane of the comparison (a review).
 */
export function laneSelection(c: Pick<Comparison, 'candidates' | 'selection'> | null | undefined, task: string): SelectionMark | undefined {
  if (!c || !c.candidates.some((x) => x.task === task)) return undefined
  const a = appliedSelection(c.selection)
  if (!a) return undefined
  if (a === 'none') return 'not-selected'
  return a.task === task ? 'selected' : 'not-selected'
}

/**
 * A candidate's mark (a task's attempt and its commit): not selected for every candidate of a lane not selected; selected for the
 * applied candidate itself; none for another attempt of the selected lane (an earlier one, or a repair made after the selection,
 * which the comparison does not show), and none while nothing is applied.
 */
export function candidateSelection(c: Pick<Comparison, 'candidates' | 'selection'> | null | undefined, cand: { task: string; attempt: number; commit?: string }): SelectionMark | undefined {
  const lane = laneSelection(c, cand.task)
  if (lane !== 'selected') return lane
  const a = appliedSelection(c!.selection) as { attempt: number; commit: string }
  return a.attempt === cand.attempt && (!cand.commit || cand.commit === a.commit) ? 'selected' : undefined
}

/** The selection panel's words when there is no record to answer, or the record shown is closed: '' when the record speaks for itself. */
export function selectionPanelWords(c: Pick<Comparison, 'settled' | 'state' | 'selection'> | null | undefined, record: Pick<Decision, 'id' | 'state' | 'why' | 'supersededBy'> | null): string {
  if (!c) return ''
  const sel = c.selection
  if (!sel) {
    if (c.state === 'ended') return 'This comparison ended with no candidate to select: no lane has a standing result, or the run had no room for a selection. Launch a new comparison to try again.'
    return c.settled ? 'The selection opens next: every lane has ended.' : 'The selection opens when every lane has ended. Nothing is selected by default.'
  }
  if (sel.state === 'superseded' || record?.state === 'superseded') {
    const id = record?.id ?? sel.decision
    const why = record?.why ? `: ${record.why}` : ''
    return `${id} is closed${why}. A new selection opens when every lane has ended; answer that one.`
  }
  return ''
}

/** Whom the selection is for, in words (HM1's `for`, PR-8a): "for ana, the architecture owner"; '' for none. */
export function selectionFor(sel: Pick<ComparisonSelection, 'for'> | null | undefined): string {
  const w = forWords(sel?.for)
  return w ? `for ${w}` : ''
}

/** Whether the selection's record may be answered on the panel: only while both the comparison and the record say open. */
export function selectionAnswerable(sel: Pick<ComparisonSelection, 'state'> | null | undefined, record: Pick<Decision, 'state'> | null): boolean {
  return !!sel && !!record && sel.state !== 'superseded' && record.state === 'open'
}

/** Who said a supporting statement: a candidate's agent, its review, or Brakeman. */
export function claimSource(task: string, attempt: number): string {
  return `said by ${task}'s agent, attempt ${attempt}`
}
export function reviewSource(task: string, attempt: number): string {
  return `${task}'s review, attempt ${attempt}`
}

/** What a claim says of its cut: the server gives a summary's first 4 KiB as encoded. */
export function claimCutWords(c: { summaryCut?: boolean }): string {
  return c.summaryCut ? 'Cut: the rest of this summary is not shown.' : ''
}

/** How many of a review's findings are not shown: the server gives the first 20 of `findingsTotal`. */
export function moreFindingsWords(r: { findings: string[]; findingsTotal?: number }): string {
  const more = (r.findingsTotal ?? r.findings.length) - r.findings.length
  return more > 0 ? `${more} more ${more === 1 ? 'finding' : 'findings'} not shown` : ''
}

/**
 * Why Ask Brakeman to compare is closed, '' while it may be asked: once the selection is answered (answered, selected, or None of these)
 * a draft would read the person's answer, which is never Brakeman's input, and the server refuses it (409 selection_made).
 */
export function askClosedWords(state: string | undefined): string {
  return state === 'answered' || state === 'selected' || state === 'none' ? 'The selection is answered: Brakeman drafts a comparison before it, never after.' : ''
}

/** A review's verdict in words: a crew review's approved or needs_changes (a person's request_changes the same). */
export function verdictWord(v: string): string {
  return v === 'approved' ? 'approved' : v === 'needs_changes' || v === 'request_changes' ? 'asked for changes' : v
}

/**
 * What Brakeman's part of the supporting panel says: queued, drafting, briefed, or why there is none ("Brakeman is not set up", "over
 * today's budget", in the server's words), from the comparison's `brakeman` and the request as the live store of requests holds it
 * (`req`, which moves before the comparison is read again). `open` while the request may still brief; `briefed` once a brief is kept.
 */
export function brakemanWords(b: ComparisonSupporting['brakeman'], req?: Pick<BrakemanRequest, 'state' | 'why' | 'sessionId' | 'config' | 'hasBrief'> | null): { words: string; open: boolean; briefed: boolean } {
  if (!b || (!b.request && !b.why)) return { words: 'Brakeman was not asked to compare these candidates.', open: false, briefed: false }
  if (!b.request) return { words: b.why!, open: false, briefed: false }
  const state = req?.state ?? b.state ?? ''
  if (b.brief || req?.hasBrief || state === 'briefed') return { words: `Brakeman's draft (${b.request})`, open: false, briefed: true }
  if (state === 'queued') return { words: `Brakeman's draft is queued (${b.request})`, open: true, briefed: false }
  if (state === 'starting' || state === 'running') return { words: `Brakeman is drafting (${b.request})`, open: true, briefed: false }
  const why = req ? stateWords(req) : b.why || state
  return { words: `No draft from ${b.request}${why ? `: ${why}` : ''}`, open: false, briefed: false }
}

/** A candidate's short commit, in the panel's words: "approach-b at 4b825dc". */
export function candidateAt(task: string, commit: string): string {
  return commit ? `${task} at ${commit.slice(0, 7)}` : task
}

/**
 * Review the selected candidate (PR-8b): the ticket page's Review view at a selection's applied candidate, from its record; null for any
 * other record, a selection not applied, and one ended with None of these.
 */
export function selectedReview(r: Pick<Decision, 'origin' | 'state' | 'applied'>): { query: { view: 'review'; candidate: string } } | null {
  const s = r.origin === 'compare' && r.state === 'applied' && r.applied?.how === 'selected' ? r.applied.selected : undefined
  return s ? { query: { view: 'review', candidate: candidateRef({ run: s.run, task: s.task, attempt: s.attempt, commit: s.commit }) } } : null
}

/** Review the selected candidate from the comparison's own selection: the same target, null while none is applied or for None of these. */
export function selectionReview(sel: ComparisonSelection | null | undefined): { query: { view: 'review'; candidate: string } } | null {
  const a = appliedSelection(sel)
  return a && a !== 'none' ? { query: { view: 'review', candidate: candidateRef(a) } } : null
}

/**
 * The run of a comparison among a ticket's runs (`ids`), for the Review view: the named candidate's run when it is one, else the first
 * such run; '' for none. `info` gives a run as the live store holds it or as its own read answered (a record the store leaves out),
 * undefined while neither has: `known` is false then, and the view waits rather than open on a candidate of a lane not selected.
 */
export function compareRunOf(ids: readonly string[], named: string | undefined, info: (id: string) => unknown): { run: string; known: boolean } {
  const all = named && !ids.includes(named) ? [named, ...ids] : ids
  const known = all.every((id) => info(id) !== undefined)
  const isCompare = (id: string) => !!runCompare(info(id))
  if (named && isCompare(named)) return { run: named, known: true }
  return { run: all.find(isCompare) ?? '', known }
}
