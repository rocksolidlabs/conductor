import type { Ref } from 'vue'
import { INTENTS, SEVERITIES, VERDICTS, type DraftNote, type Verdict } from '~/utils/review'
import { mintCommandId } from '~/utils/tasks'

/**
 * The draft of one review (Program item RV1, docs/plans/review-repair.md, "web/app"): the notes on one candidate, the verdict, the
 * summary, and the base the candidate's diff was read against, kept in this browser's storage under the ticket and the candidate until
 * Send review commits them. A convenience of this browser, never a record: every read and write of the storage is in try/catch, so a
 * private window, or a storage that refuses, keeps the draft in the page alone. The draft's base is the review's: a reload opens the
 * diff at it; the base moves with the picker while the draft holds no note on the base side (a candidate-side line is the candidate's
 * whatever the base), and once it holds one the base is locked, so a base-side note's lines and the anchor the server gives it are of
 * one commit. A stored base the page no longer offers is shown with its notes, and not sent. The page keeps every draft it opened too,
 * by its key (`pageDrafts`), whichever view opened it: a candidate left and opened again finds its draft where the storage refused it,
 * and a send's answer settles the draft of its key as the page has it now, not a copy held by a view closed since.
 *
 * Sending is one rule, kept here alone: a draft has at most one Send review at a time. A send keeps the draft as it was sent and its op
 * id with the draft (`pending`) until its own answer settles it, matched by that id: recorded, what it sent leaves the draft; refused,
 * the pending send goes and the draft stays. While it is under way (`flying`, the page's, so a view closed and opened again sees it) a
 * second send is refused in words; when its answer was lost, Send review sends that same body again under that same id, never the draft
 * as edited since, so the store answers the first and records it once. An edit made meanwhile stays in the draft for the send after.
 */

/** A draft's text: what a Send review sends. */
export interface DraftText {
  /** The full commit the diff was read against: '' until the page knows the run's base. */
  base: string
  verdict: Verdict
  summary: string
  notes: DraftNote[]
}

/** A Send review of the draft whose answer has not settled it: its op id and the draft as it was sent. */
export interface Pending {
  opId: string
  sent: DraftText
}

/** What the browser keeps of a draft. */
export interface StoredDraft extends DraftText {
  v: 1
  /** The draft's one send not yet settled by its answer: sent again as it was, with its id, until its answer settles it. Edits leave it. */
  pending?: Pending
}

/** The part of a Storage the draft uses. */
export type DraftStorage = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>

/** The key of a draft: one per ticket and candidate. */
export function draftKey(ticket: string, candidate: string): string {
  return `conductor.reviewDraft:${ticket}:${candidate}`
}

export function emptyDraft(): StoredDraft {
  return { v: 1, base: '', verdict: 'request_changes', summary: '', notes: [] }
}

function isNote(n: unknown): n is DraftNote {
  const x = n as DraftNote
  return (
    !!x &&
    typeof x.key === 'string' &&
    ['line', 'file', 'general', 'check'].includes(x.kind) &&
    (x.kind !== 'check' || (!!x.check && x.check.source === 'ledger' && typeof x.check.receipt === 'string')) &&
    (x.measured === undefined || (!!x.measured && typeof x.measured === 'object' && typeof x.measured.receipt === 'string' && typeof x.measured.stable === 'boolean')) &&
    INTENTS.includes(x.intent) &&
    typeof x.message === 'string' &&
    (x.severity === undefined || SEVERITIES.includes(x.severity)) &&
    (x.lines === undefined || (Array.isArray(x.lines) && x.lines.every((l) => typeof l === 'string')))
  )
}

function textOf(d: unknown): DraftText | null {
  const x = d as DraftText
  if (!x || typeof x.base !== 'string' || !VERDICTS.includes(x.verdict) || typeof x.summary !== 'string' || !Array.isArray(x.notes) || !x.notes.every(isNote)) return null
  return { base: x.base, verdict: x.verdict, summary: x.summary, notes: x.notes }
}

/** The draft stored under the key, or null for none, one this page cannot read (another version's), or a storage that refuses. */
export function readDraft(storage: DraftStorage | null, key: string): StoredDraft | null {
  try {
    const raw = storage?.getItem(key)
    if (!raw) return null
    const d = JSON.parse(raw) as StoredDraft
    const text = d?.v === 1 ? textOf(d) : null
    if (!text) return null
    // A pending send this page cannot read is none to repeat: the draft is read without it, and its next send is an operation of its own.
    const sent = d.pending && typeof d.pending.opId === 'string' && d.pending.opId ? textOf(d.pending.sent) : null
    return { v: 1, ...text, ...(sent ? { pending: { opId: d.pending!.opId, sent } } : {}) }
  } catch {
    return null
  }
}

/** Keeps the draft, or forgets it when it holds nothing (no text and no send to settle); whether the storage took it. */
export function writeDraft(storage: DraftStorage | null, key: string, d: StoredDraft): boolean {
  try {
    if (!storage) return false
    if (!d.notes.length && !d.summary.trim() && !d.pending) storage.removeItem(key)
    else storage.setItem(key, JSON.stringify(d))
    return true
  } catch {
    return false
  }
}

/** This browser's storage, or null where reading it throws. */
function browserStorage(): DraftStorage | null {
  try {
    return globalThis.localStorage ?? null
  } catch {
    return null
  }
}

let noteSeq = 0
/** A note's key in the draft, unique in this page. */
export function noteKey(): string {
  return `${Date.now().toString(36)}-${(++noteSeq).toString(36)}`
}

/**
 * A Send review under way: the key of the draft it sent, its op id, and the draft as sent. Its answer settles that draft, by this, whichever
 * draft the page shows when the answer lands (another candidate's, or this one edited meanwhile), and only while that draft's pending
 * send is this one (the same op id).
 */
export interface Sending {
  key: string
  opId: string
  sent: DraftText
}

/** The words for a send asked while the draft's one send is under way. */
export const SEND_UNDER_WAY = "This draft's review is being sent: it can be sent again once that send's answer has come."

/**
 * The draft `d` once the review `s` sent from it was recorded: the notes sent, and the summary sent, gone; a note added or edited since,
 * or a summary changed since, stays to be sent on its own. The base is kept for the diff shown; the pending send goes, answered.
 */
export function afterSent(d: StoredDraft, s: DraftText): StoredDraft {
  const was = new Set(s.notes.map((n) => JSON.stringify(n)))
  const notes = d.notes.filter((n) => !was.has(JSON.stringify(n)))
  const summary = d.summary === s.summary ? '' : d.summary
  return { ...emptyDraft(), base: d.base, notes, summary, ...(notes.length || summary ? { verdict: d.verdict } : {}) }
}

/** The base-side notes' lock: the base they are on, and how many hold it; null while the draft has none. */
export interface BaseLock {
  base: string
  count: number
}

export function baseLockOf(d: StoredDraft): BaseLock | null {
  const count = d.notes.filter((n) => n.side === 'base' && n.kind !== 'general').length
  return count ? { base: d.base, count } : null
}

/**
 * The drafts a page holds, by key, the keys whose last write the storage refused, and the op id of each key's send under way: one per
 * storage, so every view shares them, a view closed with its send under way among them.
 */
interface PageDrafts {
  drafts: Map<string, StoredDraft>
  refused: Set<string>
  flying: Map<string, string>
}
const pages = new WeakMap<object, PageDrafts>()
/** The page's drafts where there is no storage at all. */
const NO_STORAGE = {}
function pageDrafts(storage: DraftStorage | null): PageDrafts {
  const at = storage ?? NO_STORAGE
  let p = pages.get(at)
  if (!p) pages.set(at, (p = { drafts: reactive(new Map()), refused: reactive(new Set()), flying: reactive(new Map()) }))
  return p
}

/**
 * The draft of the candidate `candidate()` names on the ticket `ticket()`, read again when either changes. `offered()` is the bases
 * the page offers for the candidate, null until it knows them: a stored base outside them is `stale`, and the draft is not sent. The
 * draft is the page's (`pageDrafts`): the storage's when it keeps it, else the one the page holds, so a candidate left and opened again
 * loses nothing while the storage refuses.
 */
export function useReviewDraft(o: { ticket: () => string; candidate: () => string; offered: () => readonly string[] | null; storage?: DraftStorage | null }) {
  const storage = o.storage === undefined ? browserStorage() : o.storage
  const page = pageDrafts(storage)
  const key = computed(() => draftKey(o.ticket(), o.candidate()))

  /** The draft under the key as this page has it: the storage's when it keeps it, else the page's own, else an empty one. */
  function opened(k: string): StoredDraft {
    const kept = page.refused.has(k) ? null : readDraft(storage, k)
    if (kept || !page.drafts.has(k)) page.drafts.set(k, kept ?? emptyDraft())
    return page.drafts.get(k)!
  }
  function load(): void {
    opened(key.value)
  }
  // At once on a move, so the draft read next is the new candidate's.
  watch(key, load, { immediate: true, flush: 'sync' })

  const draft = computed<StoredDraft>({
    get: () => page.drafts.get(key.value) ?? emptyDraft(),
    set: (d) => void page.drafts.set(key.value, d),
  }) as Ref<StoredDraft>
  /** The storage refused the last write: the draft is this page's alone until a write is taken. */
  const unsaved = computed(() => page.refused.has(key.value))

  /** Writes the draft under `k` as the page has it, and notes whether the storage took it. */
  function keep(k: string): void {
    const d = page.drafts.get(k)
    if (!d) return
    if (writeDraft(storage, k, d)) page.refused.delete(k)
    else page.refused.add(k)
  }
  function save(): void {
    keep(key.value)
  }
  /** A send of this draft is under way (from this view or one closed since): Send review waits for its answer. */
  const flying = computed(() => page.flying.has(key.value))
  /** The send whose answer was lost, not under way: what Send review sends again, as it was and with its id; null for none. */
  const resend = computed<Pending | null>(() => (flying.value ? null : (draft.value.pending ?? null)))

  const lock = computed(() => baseLockOf(draft.value))
  /** The stored base is none the page offers now: the draft is shown, to drop or copy, and not sent. */
  const stale = computed(() => {
    const bases = o.offered()
    return !!draft.value.base && bases !== null && !bases.includes(draft.value.base)
  })

  /** The base the diff was read against with no base asked (the run's): the draft's, while it has none. Nothing is written for it. */
  function knowBase(commit: string): void {
    if (!draft.value.base && commit) draft.value.base = commit
  }

  /** Moves the draft's base: '' when it moved (or was there), else why not, a base-side note holding it. */
  function setBase(commit: string): string {
    if (commit === draft.value.base) return ''
    const l = lock.value
    if (l) return `${l.count === 1 ? '1 note is' : `${l.count} notes are`} on the base ${l.base.slice(0, 7)}: send or drop ${l.count === 1 ? 'it' : 'them'} to compare against another.`
    draft.value.base = commit
    save()
    return ''
  }

  function add(n: Omit<DraftNote, 'key'>): DraftNote {
    const note = { ...n, key: noteKey() }
    draft.value.notes.push(note)
    save()
    return note
  }
  /** Replaces the note whole with what its form saved: a field the form cleared (a resolution) is gone, not kept from before. */
  function update(key: string, n: Omit<DraftNote, 'key'>): void {
    const i = draft.value.notes.findIndex((x) => x.key === key)
    if (i < 0) return
    draft.value.notes[i] = { ...n, key }
    save()
  }
  function drop(key: string): void {
    draft.value.notes = draft.value.notes.filter((x) => x.key !== key)
    save()
  }
  function setVerdict(v: Verdict): void {
    if (v === draft.value.verdict) return
    draft.value.verdict = v
    save()
  }
  function setSummary(s: string): void {
    if (s === draft.value.summary) return
    draft.value.summary = s
    save()
  }
  /**
   * The Send review about to go, or SEND_UNDER_WAY while the draft's one send has not been answered. It is the draft's pending send when
   * one was not settled (its answer lost: the same body under the same id), else the draft as it is now under `newId` (minted when not
   * given), kept as the draft's pending send. Its answer (`sent`, `unsent`, `lost`) settles it by the key and the id this returns.
   */
  function sending(newId?: string): Sending | string {
    const k = key.value
    if (page.flying.has(k)) return SEND_UNDER_WAY
    const d = draft.value
    const p = d.pending ?? { opId: newId ?? mintCommandId(), sent: JSON.parse(JSON.stringify({ base: d.base, verdict: d.verdict, summary: d.summary, notes: d.notes })) as DraftText }
    d.pending = p
    keep(k)
    page.flying.set(k, p.opId)
    return { key: k, opId: p.opId, sent: JSON.parse(JSON.stringify(p.sent)) as DraftText }
  }
  /**
   * Settles the send `s`: it is no longer under way, and the draft under its key as the page has it now (whichever view shows it: this
   * one, another candidate's, or one opened since this one was closed) is changed by `f` only while its pending send is `s`, by the op id.
   * Nothing of another send is touched, and what was added since the send stays: never a copy this view held when it sent.
   */
  function settle(s: Sending, f?: (d: StoredDraft) => StoredDraft): void {
    if (page.flying.get(s.key) === s.opId) page.flying.delete(s.key)
    if (!f) return
    const d = page.drafts.get(s.key) ?? readDraft(storage, s.key)
    if (!d || d.pending?.opId !== s.opId) return
    page.drafts.set(s.key, f(d))
    keep(s.key)
  }
  /** The review `s` sent was recorded: what it sent leaves its draft (afterSent), the base kept for the diff shown. */
  function sent(s: Sending): void {
    settle(s, (d) => afterSent(d, d.pending!.sent))
  }
  /** The server answered `s` without keeping it: the pending send goes, the draft stays, and the next send is an operation of its own. */
  function unsent(s: Sending): void {
    settle(s, (d) => {
      const { pending: _, ...rest } = d
      return rest
    })
  }
  /** The answer to `s` was lost: it is no longer under way and stays pending, so Send review asks it again as it was, with its id. */
  function lost(s: Sending): void {
    settle(s)
  }
  /**
   * Drops every note and the summary, keeping nothing. A base the page no longer offers goes with them, for `fallback`: the base the diff
   * shown was read against with none asked (the run's), which the new draft is then made on, as knowBase would have made it.
   */
  function discard(fallback = ''): void {
    // A pending send is not the draft's text: it stays, to be settled by its own answer.
    const pending = draft.value.pending
    draft.value = { ...emptyDraft(), base: stale.value ? fallback : draft.value.base, ...(pending ? { pending } : {}) }
    save()
  }

  return { draft, unsaved, lock, stale, flying, resend, load, knowBase, setBase, add, update, drop, setVerdict, setSummary, sending, sent, unsent, lost, discard }
}
