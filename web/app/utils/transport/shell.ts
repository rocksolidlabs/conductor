import { ref, type Ref } from 'vue'
import { createAnswer, type AnswerMachine, type AnswerView } from '../answer'
import type { DecisionAnswering } from '../answering'
import type { ResponseReceipt } from '../decisions'
import type { RefAccess } from '../refAccess'
import type { ShellProvider } from '../shell'
import { mintCommandId } from '../tasks'

// The phone shell's answering and evidence access (docs/plans/phone-shell.md, OSS-2b), beside utils/transport/decide.ts: DR1's
// card and DR1's answer drawn on the shell's decision page reach the provider through these two, never the server's routes.

/** The words a chip's ref is shown with in the shell: nothing of a server's record is read on the phone. */
export const SHELL_ELSEWHERE = 'open it on the desktop'
/** Comment and Ask for more, drawn disabled with these words. */
export const SHELL_COMMENT_WORDS = 'Comment on the desktop: this phone sends answers, not comments'

/** The shell's REF_ACCESS: every chip shows its ref with SHELL_ELSEWHERE, reads nothing, and the run's state is not known. */
export function shellRefAccess(): RefAccess {
  return { route: () => ({ words: SHELL_ELSEWHERE }), read: () => null, runHeld: () => undefined }
}

/** The machine's step an answering reports a lock for. */
const LOCKED = new Set(['checking', 'sending', 'waiting', 'unknown'])

/**
 * The answer as the shell's answering states it: locked while a send has begun and not settled, or while no Answer may be sent;
 * else the words of the last step (a changed decision, a refusal) and a draft not saved, or null.
 */
export function answeringState(v: AnswerView | undefined): ReturnType<NonNullable<DecisionAnswering['state']>> {
  if (!v) return null
  if (LOCKED.has(v.step)) return { lock: true, words: v.words, actions: v.actions }
  if (v.noAnswer) return { lock: true, words: v.words, actions: [] }
  const words = [v.step === 'editing' ? v.words : '', v.notSaved].filter(Boolean).join('. ')
  return words ? { lock: false, words, actions: [] } : null
}

/** A kept answer's receipt, from what the provider handed back. */
function receiptOf(v: AnswerView, now: () => number): ResponseReceipt {
  const d = v.item?.decision
  return { decision: d?.id ?? '', status: 'accepted', words: v.words || 'Your answer was recorded.', actor: '', at: new Date(now()).toISOString(), rev: d?.rev ?? 0 }
}

/**
 * The answering of the provider p, its calls naming a decision by its record's id (as DecisionRespond does), each reaching the item
 * that holds it: its answer machine (answer.ts) over p's inbox and drafts, its views reactive (`views`), and the
 * DecisionAnswering a page provides to DecisionRespond. `respond` hands the body's content to the machine, which sends with the
 * draft's responseId and rev, never the ones the body was minted with.
 */
export function shellAnswering(p: Pick<ShellProvider, 'inbox' | 'drafts'>, name: string, o: { mint?: () => string; now?: () => number } = {}): { machine: AnswerMachine; views: Ref<Record<string, AnswerView>>; answering: DecisionAnswering } {
  const now = o.now ?? Date.now
  const views = ref<Record<string, AnswerView>>({})
  const machine = createAnswer({
    inbox: p.inbox,
    drafts: p.drafts,
    name,
    mint: o.mint ?? mintCommandId,
    now,
    changed: (id, v) => (views.value = { ...views.value, [id]: v }),
  })
  // DecisionRespond names a decision by its record's id; the provider's items have ids of their own. Each item opened records
  // which record it holds, and the answering reaches the item through it.
  const itemOf = new Map<string, string>()
  const open = machine.open
  machine.open = (id, it) => {
    itemOf.set(it.decision.id, id)
    return open(id, it)
  }
  const item = (decisionId: string) => itemOf.get(decisionId) ?? decisionId
  const answering: DecisionAnswering = {
    supports: { respond: true, comment: SHELL_COMMENT_WORDS, more_info: SHELL_COMMENT_WORDS },
    async respond(id, body) {
      const c: { choice?: number; text?: string; rationale?: string } = {}
      if (body.choice !== undefined) c.choice = body.choice
      if (body.text) c.text = body.text
      if (body.rationale) c.rationale = body.rationale
      const v = await machine.answer(item(id), c)
      switch (v.step) {
        case 'kept':
          return { outcome: 'kept', receipt: receiptOf(v, now), ...(v.item ? { decision: v.item.decision } : {}), words: v.words }
        case 'waiting':
          return { outcome: 'waiting', words: v.words }
        case 'unknown':
          return { outcome: 'unknown', words: v.words }
        default:
          // What it means is the state's words (answeringState): the reply says that nothing was sent.
          return { outcome: 'refused', words: 'Not sent.', ...(v.item ? { decision: v.item.decision } : {}) }
      }
    },
    comment: () => Promise.reject(Object.assign(new Error(SHELL_COMMENT_WORDS), { code: 'not_offered' })),
    draft: {
      initial: (id) => {
        const d = views.value[item(id)]?.draft
        if (!d) return null
        return { ...(d.choice !== undefined ? { choice: d.choice } : {}), ...(d.text ? { text: d.text } : {}), ...(d.rationale ? { rationale: d.rationale } : {}) }
      },
      changed: (id, f) => void machine.edit(item(id), f),
    },
    state: (id) => answeringState(views.value[item(id)]),
    cancel: (id) => void machine.cancel(item(id)),
    confirm: (id) => void machine.confirm(item(id)),
  }
  return { machine, views, answering }
}
