import type { InjectionKey } from 'vue'
import type { Decision, OpReceipt, ResponseBody, ResponseReceipt } from './decisions'

// How DR1's answer (DecisionRespond.vue) is sent (docs/plans/phone-shell.md, OSS-2b, "The answer: one seam in DecisionRespond.vue"):
// injected (`provide(DECISION_ANSWERING, …)`) by a page that answers through something other than the server's routes (the phone
// shell's decision page, through its provider). With nothing injected the answering is today's: the server's routes through useApi,
// every control carried, no draft and no state, so the operator's decision page, the run's card and the join page are unchanged.

/** The answer's content as typed: the choice, the text and the rationale. */
export interface AnswerFields {
  choice?: number
  text?: string
  rationale?: string
}

/** The answer's lock, its words and the actions it offers; null while the answer may be edited and sent. */
export interface AnsweringState {
  lock: boolean
  words?: string
  actions?: Array<'cancel' | 'confirm'>
  /** The answer was kept, whichever press sent it: the component runs its success path once, with this receipt. */
  kept?: true
  receipt?: ResponseReceipt
}

export interface DecisionAnswering {
  /** What it carries: absent, every control. A control it does not carry is drawn disabled with these words. */
  supports?: { respond?: true | string; comment?: true | string; more_info?: true | string }
  /** An answer sent, waiting, refused or not known: the component's success path runs on 'kept' with a receipt alone. */
  respond(id: string, body: ResponseBody): Promise<{ outcome: 'kept' | 'refused' | 'waiting' | 'unknown'; receipt?: ResponseReceipt; decision?: Decision; words?: string }>
  comment(id: string, c: { kind: 'comment' | 'more_info'; text: string; expectedRev: number }): Promise<{ receipt: OpReceipt; decision: Decision }>
  /** The answer as typed, kept and restored by the page that provides it. */
  draft?: { initial(id: string): AnswerFields | null; changed(id: string, f: AnswerFields): void }
  /** Reactive: the answer's lock, its words and the actions it offers; null while the answer may be edited and sent. */
  state?(id: string): AnsweringState | null
  /** The 'cancel' action: a waiting answer back to editing. */
  cancel?(id: string): void | Promise<void>
  /** The 'confirm' action: a waiting answer, or one that did not arrive, sent through checking. */
  confirm?(id: string): void | Promise<void>
}

/** The injection key: the shell's decision page provides its answering to the DecisionRespond it draws. */
export const DECISION_ANSWERING: InjectionKey<DecisionAnswering> = Symbol('decisionAnswering')

/** Whether a control is carried, and the words it is drawn disabled with when it is not ('' when it is carried). */
export function carried(a: DecisionAnswering | null | undefined, control: 'respond' | 'comment' | 'more_info'): { on: boolean; words: string } {
  const s = a?.supports
  if (!s) return { on: true, words: '' }
  const v = s[control]
  if (v === true) return { on: true, words: '' }
  return { on: false, words: typeof v === 'string' && v ? v : 'Not offered here.' }
}

/** Whether a reply runs the component's success path (fields cleared, the receipt shown, the record taken): 'kept' with a receipt, alone. */
export function replyKept<R extends { outcome: string; receipt?: ResponseReceipt }>(r: R): r is R & { outcome: 'kept'; receipt: ResponseReceipt } {
  return r.outcome === 'kept' && !!r.receipt
}

/** The words a reply that is not kept shows: its own, or what its outcome means. */
export function replyWords(r: { outcome: string; words?: string }): string {
  if (r.words) return r.words
  return r.outcome === 'waiting' ? 'Not sent yet: waiting.' : r.outcome === 'unknown' ? 'Not known whether the answer arrived.' : 'Not sent.'
}
