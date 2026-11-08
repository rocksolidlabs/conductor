import type { Ref } from 'vue'
import { INTENTS, SEVERITIES, VERDICTS, type DraftNote, type Verdict } from '~/utils/review'

/**
 * The draft of one review (Program item RV1, docs/plans/review-repair.md, "web/app"): the notes on one candidate, the verdict, the
 * summary, and the base the candidate's diff was read against, kept in this browser's storage under the ticket and the candidate until
 * Send review commits them. A convenience of this browser, never a record: every read and write of the storage is in try/catch, so a
 * private window, or a storage that refuses, keeps the draft in the page alone. The draft's base is the review's: a reload opens the
 * diff at it; the base moves with the picker while the draft holds no note on the base side (a candidate-side line is the candidate's
 * whatever the base), and once it holds one the base is locked, so a base-side note's lines and the anchor the server gives it are of
 * one commit. A stored base the page no longer offers is shown with its notes, and not sent.
 */

/** What the browser keeps of a draft. */
export interface StoredDraft {
  v: 1
  /** The full commit the diff was read against: '' until the page knows the run's base. */
  base: string
  verdict: Verdict
  summary: string
  notes: DraftNote[]
  /** The op id of a Send review whose answer was not seen: sent again with it, the store answers the first. Any edit drops it. */
  opId?: string
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
    ['line', 'file', 'general'].includes(x.kind) &&
    INTENTS.includes(x.intent) &&
    typeof x.message === 'string' &&
    (x.severity === undefined || SEVERITIES.includes(x.severity)) &&
    (x.lines === undefined || (Array.isArray(x.lines) && x.lines.every((l) => typeof l === 'string')))
  )
}

/** The draft stored under the key, or null for none, one this page cannot read (another version's), or a storage that refuses. */
export function readDraft(storage: DraftStorage | null, key: string): StoredDraft | null {
  try {
    const raw = storage?.getItem(key)
    if (!raw) return null
    const d = JSON.parse(raw) as StoredDraft
    if (d?.v !== 1 || typeof d.base !== 'string' || !VERDICTS.includes(d.verdict) || typeof d.summary !== 'string' || !Array.isArray(d.notes) || !d.notes.every(isNote)) return null
    return { v: 1, base: d.base, verdict: d.verdict, summary: d.summary, notes: d.notes, ...(typeof d.opId === 'string' && d.opId ? { opId: d.opId } : {}) }
  } catch {
    return null
  }
}

/** Keeps the draft, or forgets it when it holds nothing; whether the storage took it. */
export function writeDraft(storage: DraftStorage | null, key: string, d: StoredDraft): boolean {
  try {
    if (!storage) return false
    if (!d.notes.length && !d.summary.trim()) storage.removeItem(key)
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
 * The draft of the candidate `candidate()` names on the ticket `ticket()`, read again when either changes. `offered()` is the bases
 * the page offers for the candidate, null until it knows them: a stored base outside them is `stale`, and the draft is not sent.
 */
export function useReviewDraft(o: { ticket: () => string; candidate: () => string; offered: () => readonly string[] | null; storage?: DraftStorage | null }) {
  const storage = o.storage === undefined ? browserStorage() : o.storage
  const draft = ref(emptyDraft()) as Ref<StoredDraft>
  /** The storage refused the last write: the draft is this page's alone until a write is taken. */
  const unsaved = ref(false)
  const key = computed(() => draftKey(o.ticket(), o.candidate()))

  function load(): void {
    draft.value = readDraft(storage, key.value) ?? emptyDraft()
    unsaved.value = false
  }
  watch(key, load, { immediate: true })

  function save(edited = true): void {
    if (edited) delete draft.value.opId
    unsaved.value = !writeDraft(storage, key.value, draft.value)
  }

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
  function update(key: string, n: Partial<Omit<DraftNote, 'key'>>): void {
    const i = draft.value.notes.findIndex((x) => x.key === key)
    if (i < 0) return
    draft.value.notes[i] = { ...draft.value.notes[i]!, ...n, key }
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
  /** The op id of the Send review about to go: kept with the draft, so a resend after a lost answer is the same operation. */
  function sending(opId: string): void {
    draft.value.opId = opId
    save(false)
  }
  /** The review was sent and recorded: the draft is emptied, its base kept for the diff shown. */
  function sent(): void {
    draft.value = { ...emptyDraft(), base: draft.value.base }
    save(false)
  }
  /** Drops every note and the summary, keeping nothing; a base the page no longer offers goes with them. */
  function discard(): void {
    draft.value = { ...emptyDraft(), base: stale.value ? '' : draft.value.base }
    save()
  }

  return { draft, unsaved, lock, stale, load, knowBase, setBase, add, update, drop, setVerdict, setSummary, sending, sent, discard }
}
