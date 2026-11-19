import { decisionWords } from './decisions'
import { DRAFT_MAX_AGE_MS } from './drafts'
import { readRefusal, type ShellDraft, type ShellDrafts, type ShellInbox, type ShellItem, type ShellReply } from './shell'

// An answer from the phone, from the tap to the record (docs/plans/phone-shell.md, "On the phone: an answer, from the tap to the
// record"): the state machine over a provider's ShellInbox and ShellDrafts, one item at a time. Every send begins from a read made in
// the same press; a draft keeps one responseId until a changed decision is drawn and the draft is rebased; a send whose outcome is
// not known stays locked until the person's answer is read back by that responseId. The rows past `kept` are the provider's, shown
// and never derived. It names no service.

export type AnswerStep = 'editing' | 'checking' | 'sending' | 'waiting' | 'unknown' | 'kept'

/** What the screen shows for the item's answer. */
export interface AnswerView {
  step: AnswerStep
  /** The words under the answer ('' for none). */
  words: string
  /** The content may not be edited nor sent (checking, sending, waiting, unknown). */
  lock: boolean
  /** The actions offered: Cancel (a waiting answer back to editing), Confirm (sent through checking). */
  actions: Array<'cancel' | 'confirm'>
  /** No Answer while this holds: the decision is answered by another, not open, or answered elsewhere. */
  noAnswer: boolean
  /** The item as last read: the page draws it (a changed decision, a kept answer's rows). */
  item?: ShellItem
  /** The draft's content as kept (what a restart restores). */
  draft: ShellDraft | null
  /** The draft could not be kept on this phone: its words. */
  notSaved: string
}

export type AnswerContent = Pick<ShellDraft, 'choice' | 'text' | 'rationale'>

/** The words of each step, as the table says them. */
export const ANSWER_WORDS = {
  checking: 'Checking the decision',
  sending: 'Sending',
  changed: 'This decision changed after you wrote your answer: read it again',
  offline: 'Not sent: you are offline. It will be checked and sent when you are back online, if the decision is still as you read it',
  back: 'Back online: confirm to send your answer',
  presence: 'Not sent: confirm with Face ID or the passcode to answer',
  notSavedSend: 'Not saved on this phone: nothing was sent',
  notSaved: 'Not saved on this phone',
  unknown: 'Checking whether your answer arrived',
  didNotArrive: 'Your answer did not arrive: confirm to send it again',
  earlier: 'An earlier answer from this phone was kept',
} as const

export interface AnswerDeps {
  inbox: ShellInbox
  drafts: ShellDrafts
  /** The provider's name, for "<name> cannot be reached". */
  name: string
  /** A new responseId. */
  mint: () => string
  now?: () => number
  /** Each change of an item's view. */
  changed?: (id: string, v: AnswerView) => void
}

const LOCKED: readonly AnswerStep[] = ['checking', 'sending', 'waiting', 'unknown']

/** A draft's content alone. */
const contentOf = (d: AnswerContent | null | undefined): AnswerContent => {
  const c: AnswerContent = {}
  if (d?.choice !== undefined) c.choice = d.choice
  if (d?.text) c.text = d.text
  if (d?.rationale) c.rationale = d.rationale
  return c
}

/** Whether the item's *Response recorded* row is done by an answer that is not this draft's. */
function answeredByAnother(item: ShellItem, responseId: string): boolean {
  const recorded = item.rows.find((r) => r.step === 'recorded')
  return recorded?.state === 'done' && item.own?.responseId !== responseId
}

/**
 * The machine. Each call answers once its step is done; a call made while the item is locked in a step that ignores it (a second
 * Answer while checking or sending, an edit while locked) does nothing. After `signedOut`, nothing begun before it changes a view
 * or a draft (A4).
 */
export function createAnswer(deps: AnswerDeps) {
  const now = deps.now ?? Date.now
  const views = new Map<string, AnswerView>()
  /** What each item's view was begun under: a sign-out moves it, and a reply under an older one is dropped. */
  let generation = 0
  /** The step a send began from: a presence refused after a wait waits again. */
  const from = new Map<string, AnswerStep>()

  const view = (id: string): AnswerView =>
    views.get(id) ?? { step: 'editing', words: '', lock: false, actions: [], noAnswer: false, draft: null, notSaved: '' }

  function set(id: string, gen: number, patch: Partial<AnswerView>): AnswerView {
    if (gen !== generation) return view(id)
    const next = { ...view(id), ...patch }
    next.lock = LOCKED.includes(next.step)
    views.set(id, next)
    deps.changed?.(id, next)
    return next
  }

  /** Keeps d through the provider; its refusal's words, or ''. A refused put leaves the kept draft as it was. */
  async function keep(id: string, gen: number, d: ShellDraft): Promise<string> {
    // A sign-out since this step began: nothing is kept for it (A4).
    if (gen !== generation) return ''
    let words = ''
    try {
      const r = await deps.drafts.put(d)
      if (!r.ok) words = `${ANSWER_WORDS.notSaved}: ${readRefusal(r).words}`
    } catch (e) {
      words = `${ANSWER_WORDS.notSaved}: ${readRefusal(e).words}`
    }
    if (words) set(id, gen, { notSaved: words })
    else set(id, gen, { draft: d, notSaved: '' })
    return words
  }

  /** The draft of id as kept, removed when older than seven days. */
  async function stored(id: string): Promise<ShellDraft | null> {
    const d = await deps.drafts.get(id).catch(() => null)
    if (d && now() - Date.parse(d.savedAt) > DRAFT_MAX_AGE_MS) {
      await deps.drafts.remove(id).catch(() => {})
      return null
    }
    return d
  }

  /** A changed decision drawn: the draft stored again at the drawn rev with a new responseId, its content kept, editing. */
  async function rebase(id: string, gen: number, item: ShellItem, words: string): Promise<AnswerView> {
    if (gen !== generation) return view(id)
    const old = view(id).draft
    set(id, gen, { step: 'editing', item, words, actions: [], noAnswer: false })
    const d: ShellDraft = { ...contentOf(old), id, responseId: deps.mint(), rev: item.decision.rev, state: 'editing', savedAt: new Date(now()).toISOString() }
    await keep(id, gen, d)
    return view(id)
  }

  /** Judges an item read (at checking, or returned by a refused send) against the draft: what follows. */
  async function judge(id: string, gen: number, item: ShellItem, send: boolean): Promise<AnswerView> {
    if (gen !== generation) return view(id)
    const d = view(id).draft
    const dec = item.decision
    if (item.elsewhere) return set(id, gen, { step: 'editing', item, words: item.elsewhere, noAnswer: true, actions: [] })
    if (d && (dec.state === 'superseded' || dec.rev !== d.rev)) {
      const words = dec.state === 'superseded' && dec.supersededBy ? `${ANSWER_WORDS.changed}. It was replaced by ${dec.supersededBy}` : ANSWER_WORDS.changed
      return rebase(id, gen, item, words)
    }
    if (dec.state !== 'open' || answeredByAnother(item, d?.responseId ?? '')) {
      const recorded = item.rows.find((r) => r.step === 'recorded')
      const words = recorded?.state === 'done' && recorded.words ? recorded.words : decisionWords(dec).state
      return set(id, gen, { step: 'editing', item, words, noAnswer: true, actions: [] })
    }
    if (!send) return set(id, gen, { step: 'editing', item, noAnswer: false, actions: [] })
    return sendNow(id, gen, item)
  }

  async function sendNow(id: string, gen: number, item: ShellItem): Promise<AnswerView> {
    const d = view(id).draft!
    set(id, gen, { step: 'sending', item, words: ANSWER_WORDS.sending, actions: [] })
    let reply: ShellReply
    try {
      reply = await deps.inbox.respond(id, { responseId: d.responseId, expectedRev: d.rev, ...contentOf(d) })
    } catch {
      reply = { outcome: 'unknown' }
    }
    if (gen !== generation) return view(id)
    const words = readRefusal({ words: reply.words }, '').words
    switch (reply.outcome) {
      case 'kept':
      case 'same':
        await deps.drafts.remove(id).catch(() => {})
        return set(id, gen, { step: 'kept', item: reply.item ?? item, words: reply.outcome === 'kept' ? words : '', draft: null, noAnswer: false, actions: [] })
      case 'unknown':
        // The provider stored the draft `unknown` with its hash before its send: locked until answerOf says.
        set(id, gen, { step: 'unknown', words: ANSWER_WORDS.unknown, actions: [], draft: (await deps.drafts.get(id).catch(() => null)) ?? { ...d, state: 'unknown' } })
        return settle(id)
      default: {
        if (reply.code === 'presence_refused') {
          if (from.get(id) === 'waiting') return set(id, gen, { step: 'waiting', words: ANSWER_WORDS.presence, actions: ['confirm', 'cancel'] })
          await keep(id, gen, { ...d, state: 'editing', savedAt: new Date(now()).toISOString() })
          return set(id, gen, { step: 'editing', words: ANSWER_WORDS.presence, actions: [] })
        }
        if (reply.code === 'not_saved') return set(id, gen, { step: 'editing', words: ANSWER_WORDS.notSavedSend, actions: [] })
        await keep(id, gen, { ...d, state: 'editing', savedAt: new Date(now()).toISOString() })
        if (gen !== generation) return view(id)
        set(id, gen, { step: 'editing', words: words || 'Not sent.', actions: [] })
        if (reply.item) {
          const after = await judge(id, gen, reply.item, false)
          // The reply's own words stand unless the judgement said something of its own.
          if (!after.words) set(id, gen, { words: words || 'Not sent.' })
        }
        return view(id)
      }
    }
  }

  /** Checks the decision and, when it is as read, sends: Answer, and Confirm after a wait or an answer that did not arrive. */
  async function check(id: string, gen: number): Promise<AnswerView> {
    set(id, gen, { step: 'checking', words: ANSWER_WORDS.checking, actions: [], noAnswer: false })
    let item: ShellItem
    try {
      item = await deps.inbox.item(id)
    } catch (e) {
      if (gen !== generation) return view(id)
      const r = readRefusal(e)
      if (r.code === 'offline' || r.code === 'unreachable') {
        const d = view(id).draft!
        await keep(id, gen, { ...d, state: 'waiting', savedAt: new Date(now()).toISOString() })
        if (gen !== generation) return view(id)
        return set(id, gen, { step: 'waiting', words: r.code === 'offline' ? ANSWER_WORDS.offline : `${deps.name} cannot be reached`, actions: ['cancel'] })
      }
      return set(id, gen, { step: 'editing', words: r.words, actions: [] })
    }
    if (gen !== generation) return view(id)
    return judge(id, gen, item, true)
  }

  /** Reads back the person's answer by the draft's responseId: kept, did not arrive (Confirm offered), or not known yet. */
  async function settle(id: string): Promise<AnswerView> {
    const gen = generation
    const d = view(id).draft
    if (!d || view(id).step !== 'unknown') return view(id)
    const got = await deps.inbox.answerOf(id, d.responseId).catch(() => ({ code: 'unreachable' as const, words: '' }))
    if (gen !== generation) return view(id)
    if ('code' in got) return set(id, gen, { step: 'unknown', words: ANSWER_WORDS.unknown, actions: [] })
    if (!('h' in got)) return set(id, gen, { step: 'unknown', words: ANSWER_WORDS.didNotArrive, actions: ['confirm'] })
    await deps.drafts.remove(id).catch(() => {})
    const item = await deps.inbox.item(id).catch(() => view(id).item)
    if (gen !== generation) return view(id)
    return set(id, gen, { step: 'kept', item, words: got.h && got.h === d.h ? readRefusal({ words: got.words }, '').words : ANSWER_WORDS.earlier, draft: null, actions: [] })
  }

  return {
    view,

    /** Opens the item drawn: its draft restored, and a send that began resumed where the draft's state says. */
    async open(id: string, item: ShellItem): Promise<AnswerView> {
      const gen = generation
      const d = await stored(id)
      if (gen !== generation) return view(id)
      // A send pressed while the draft was read is left as it is: an open never unlocks it (A1).
      if (view(id).lock) return view(id)
      set(id, gen, { draft: d, item, words: '', notSaved: '' })
      if (d?.state === 'unknown') {
        set(id, gen, { step: 'unknown', words: ANSWER_WORDS.unknown, actions: [] })
        return settle(id)
      }
      if (d?.state === 'waiting') return set(id, gen, { step: 'waiting', words: ANSWER_WORDS.offline, actions: ['cancel'] })
      if (d && d.rev !== item.decision.rev) return rebase(id, gen, item, ANSWER_WORDS.changed)
      return judge(id, gen, item, false).then(() => view(id))
    },

    /** The person picked, typed or said: the draft kept, begun with its responseId at the drawn rev. Ignored while locked. */
    async edit(id: string, c: AnswerContent): Promise<AnswerView> {
      const gen = generation
      const v = view(id)
      if (v.lock) return v
      const rev = v.draft?.rev ?? v.item?.decision.rev
      if (rev === undefined) return v
      const d: ShellDraft = { ...contentOf(c), id, responseId: v.draft?.responseId ?? deps.mint(), rev, state: 'editing', savedAt: new Date(now()).toISOString() }
      if (v.step === 'kept') set(id, gen, { step: 'editing', words: '' })
      await keep(id, gen, d)
      return view(id)
    },

    /** Answer: checked, then sent once. A second press while checking or sending is ignored. */
    async answer(id: string, c: AnswerContent): Promise<AnswerView> {
      const gen = generation
      const v = view(id)
      // A kept answer is settled: a press after it sends nothing until the person begins a new answer (an edit).
      if (v.lock || v.noAnswer || v.step === 'kept') return v
      // The step is taken at once, before any wait: a second press finds it locked.
      set(id, gen, { step: 'checking', words: ANSWER_WORDS.checking, actions: [] })
      const rev = v.draft?.rev ?? v.item?.decision.rev ?? 0
      const d: ShellDraft = { ...contentOf(c), id, responseId: v.draft?.responseId ?? deps.mint(), rev, state: 'editing', savedAt: new Date(now()).toISOString() }
      if (await keep(id, gen, d)) return set(id, gen, { step: 'editing', words: ANSWER_WORDS.notSavedSend, actions: [] })
      from.set(id, 'editing')
      return check(id, gen)
    },

    /** Back in the foreground and online: a waiting answer offers Confirm; one not known is read back. */
    async resume(id: string): Promise<AnswerView> {
      const v = view(id)
      if (v.step === 'waiting') return set(id, generation, { words: ANSWER_WORDS.back, actions: ['confirm', 'cancel'] })
      if (v.step === 'unknown' && !v.actions.includes('confirm')) return settle(id)
      return v
    },

    /** Confirm: a waiting answer, or one that did not arrive, sent through checking with the same responseId and content. */
    async confirm(id: string): Promise<AnswerView> {
      const gen = generation
      const v = view(id)
      if (!v.actions.includes('confirm') || !v.draft) return v
      from.set(id, v.step)
      return check(id, gen)
    },

    /** Cancel: a waiting answer back to editing, its draft no longer waiting. */
    async cancel(id: string): Promise<AnswerView> {
      const gen = generation
      const v = view(id)
      if (v.step !== 'waiting' || !v.draft) return v
      set(id, gen, { step: 'editing', words: '', actions: [] })
      await keep(id, gen, { ...v.draft, state: 'editing', savedAt: new Date(now()).toISOString() })
      return view(id)
    },

    /** The provider signed out: every view dropped, and nothing begun before it writes one again. */
    signedOut(): void {
      generation++
      views.clear()
      from.clear()
    },
  }
}

export type AnswerMachine = ReturnType<typeof createAnswer>
