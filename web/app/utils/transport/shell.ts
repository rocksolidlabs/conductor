import { ref, type Ref } from 'vue'
import { createAnswer, type AnswerMachine, type AnswerView } from '../answer'
import type { DecisionAnswering } from '../answering'
import type { ResponseReceipt } from '../decisions'
import type { RefAccess } from '../refAccess'
import { withinWait, type ShellDrafts, type ShellInbox, type ShellProvider } from '../shell'
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
 * kept, with its receipt, once the answer was kept, whichever press sent it (Answer, or Confirm after a wait); else the words of
 * the last step (a changed decision, a refusal) and a draft not saved, or null.
 */
export function answeringState(v: AnswerView | undefined, now: () => number = Date.now): ReturnType<NonNullable<DecisionAnswering['state']>> {
  if (!v) return null
  if (LOCKED.has(v.step)) return { lock: true, words: v.words, actions: v.actions }
  if (v.step === 'kept') return { lock: false, kept: true, receipt: receiptOf(v, now), actions: [] }
  if (v.noAnswer) return { lock: true, words: v.words, actions: [] }
  const words = [v.step === 'editing' ? v.words : '', v.notSaved].filter(Boolean).join('. ')
  return words ? { lock: false, words, actions: [] } : null
}

/** A kept answer's receipt, from what the provider handed back. */
function receiptOf(v: AnswerView, now: () => number): ResponseReceipt {
  const d = v.item?.decision
  return { decision: d?.id ?? '', status: 'accepted', words: v.words || 'Your answer was recorded.', actor: '', at: new Date(now()).toISOString(), rev: d?.rev ?? 0 }
}

/** What an answering made for one item answers for a decision that is not that item's: nothing sent, nothing kept. */
const NOT_THIS = 'Not sent: this answer is not for the decision on this page.'

/**
 * The answering of the provider p: its answer machine (answer.ts) over p's inbox and drafts, each read of the provider bounded
 * (`PROVIDER_WAIT_MS`: a read that does not answer is the provider not reached) and its send left to the provider's own answer
 * (the person confirms inside it, on the system's sheet; the provider answers `unknown` past its own bound), its
 * views reactive (`views`), and, for the item a page draws, the DecisionAnswering that page provides to DecisionRespond
 * (`answeringFor`). That answering reaches its own item alone: DecisionRespond names a decision by its record's id, which repeats
 * across servers, so each call is checked against the record the item holds and refused for any other. `respond` hands the
 * body's content to the machine, which sends with the draft's responseId and rev, never the ones the body was minted with.
 */
export function shellAnswering(
  p: Pick<ShellProvider, 'inbox' | 'drafts'>,
  name: string,
  o: { mint?: () => string; now?: () => number; wait?: number } = {},
): { machine: AnswerMachine; views: Ref<Record<string, AnswerView>>; answeringFor: (itemId: string) => DecisionAnswering; signedOut: () => void } {
  const now = o.now ?? Date.now
  const views = ref<Record<string, AnswerView>>({})
  const machine = createAnswer({
    inbox: boundedInbox(p.inbox, name, o.wait),
    drafts: boundedDrafts(p.drafts, o.wait),
    name,
    mint: o.mint ?? mintCommandId,
    now,
    changed: (id, v) => (views.value = { ...views.value, [id]: v }),
  })
  /** Whether the decision a call names is the one the item holds now. */
  const holds = (itemId: string, decisionId: string) => views.value[itemId]?.item?.decision.id === decisionId

  function answeringFor(itemId: string): DecisionAnswering {
    return {
      supports: { respond: true, comment: SHELL_COMMENT_WORDS, more_info: SHELL_COMMENT_WORDS },
      async respond(id, body) {
        if (!holds(itemId, id)) return { outcome: 'refused', words: NOT_THIS }
        const c: { choice?: number; text?: string; rationale?: string } = {}
        if (body.choice !== undefined) c.choice = body.choice
        if (body.text) c.text = body.text
        if (body.rationale) c.rationale = body.rationale
        const v = await machine.answer(itemId, c)
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
          if (!holds(itemId, id)) return null
          const d = views.value[itemId]?.draft
          if (!d) return null
          return { ...(d.choice !== undefined ? { choice: d.choice } : {}), ...(d.text ? { text: d.text } : {}), ...(d.rationale ? { rationale: d.rationale } : {}) }
        },
        changed: (id, f) => {
          if (holds(itemId, id)) void machine.edit(itemId, f)
        },
      },
      state: (id) => (holds(itemId, id) ? answeringState(views.value[itemId], now) : { lock: true, words: NOT_THIS, actions: [] }),
      cancel: async (id) => {
        if (holds(itemId, id)) await machine.cancel(itemId)
      },
      confirm: async (id) => {
        if (holds(itemId, id)) await machine.confirm(itemId)
      },
    }
  }
  /**
   * A sign-out: the machine moves on (nothing begun before it writes again) and every view it kept is emptied, the reactive copy the
   * pages draw from among them: no card, no draft and no state of the signed-out person stays on the phone's screens.
   */
  function signedOut(): void {
    machine.signedOut()
    views.value = {}
  }
  return { machine, views, answeringFor, signedOut }
}

/** The provider's inbox with every call bounded: past the wait a call is refused `unreachable`, in words naming the provider. */
function boundedInbox(inbox: ShellInbox, name: string, wait?: number): ShellInbox {
  const words = `${name} did not answer`
  return {
    list: (after) => withinWait(inbox.list(after), words, wait),
    item: (id) => withinWait(inbox.item(id), words, wait),
    // Not bounded here: the system's sheet asking the person to confirm is inside the send, and it is the person's time. The
    // provider bounds what follows the confirmation and answers `unknown` when that bound passes (docs/plans/phone-shell.md, A1).
    respond: (id, a) => inbox.respond(id, a),
    answerOf: (id, responseId) => withinWait(inbox.answerOf(id, responseId), words, wait),
    resolve: (handle) => withinWait(inbox.resolve(handle), words, wait),
    watch: (cb) => inbox.watch(cb),
  }
}

/** The provider's drafts with every call bounded: a put past the wait is not saved. */
function boundedDrafts(drafts: ShellDrafts, wait?: number): ShellDrafts {
  const words = 'The draft was not kept in time'
  return {
    get: (id) => withinWait(drafts.get(id), words, wait),
    list: () => withinWait(drafts.list(), words, wait),
    put: (d) => withinWait(drafts.put(d), words, wait).catch(() => ({ ok: false as const, code: 'not_saved' as const, words })),
    remove: (id) => withinWait(drafts.remove(id), words, wait),
  }
}
