import type { Ref } from 'vue'
import { INTENTS, SEVERITIES, VERDICTS, type DraftNote, type Verdict } from '~/utils/review'

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

/**
 * A Send review under way: the key of the draft it sent, its op id, and the draft as sent. Its answer settles that draft, by this, whichever
 * draft the page shows when the answer lands (another candidate's, or this one edited meanwhile).
 */
export interface Sending {
  key: string
  opId: string
  sent: StoredDraft
}

/**
 * The draft `d` once the review `s` sent from it was recorded: the notes sent, and the summary sent, gone; a note added or edited since,
 * or a summary changed since, stays to be sent on its own. The base is kept for the diff shown; the op id goes, the send answered.
 */
export function afterSent(d: StoredDraft, s: StoredDraft): StoredDraft {
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

/** The drafts a page holds, by key, and the keys whose last write the storage refused: one per storage, so every view shares them. */
interface PageDrafts {
  drafts: Map<string, StoredDraft>
  refused: Set<string>
}
const pages = new WeakMap<object, PageDrafts>()
/** The page's drafts where there is no storage at all. */
const NO_STORAGE = {}
function pageDrafts(storage: DraftStorage | null): PageDrafts {
  const at = storage ?? NO_STORAGE
  let p = pages.get(at)
  if (!p) pages.set(at, (p = { drafts: reactive(new Map()), refused: reactive(new Set()) }))
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
  function save(edited = true): void {
    if (edited) delete draft.value.opId
    keep(key.value)
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
   * The Send review about to go, with its op id: the id kept with the draft, so a resend after a lost answer is the same operation; what
   * it answers (`sent`, `unsent`) settles the draft it was sent from, by the key and the draft as sent that this returns.
   */
  function sending(opId: string): Sending {
    draft.value.opId = opId
    save(false)
    return { key: key.value, opId, sent: JSON.parse(JSON.stringify(draft.value)) as StoredDraft }
  }
  /**
   * Settles the draft under the key `k` as the page has it now, whichever view shows it (this one, another candidate's, or one opened
   * since this one was closed), so what was added since the send stays: never a copy this view held when it sent.
   */
  function settle(k: string, f: (d: StoredDraft) => StoredDraft): void {
    const d = page.drafts.get(k) ?? readDraft(storage, k)
    if (!d) return
    page.drafts.set(k, f(d))
    keep(k)
  }
  /** The review `s` sent was recorded: what it sent leaves its draft (afterSent), the base kept for the diff shown. */
  function sent(s: Sending): void {
    settle(s.key, (d) => afterSent(d, s.sent))
  }
  /** The server answered `s` without keeping it: its op id leaves its draft, the next send an operation of its own. */
  function unsent(s: Sending): void {
    settle(s.key, (d) => {
      if (d.opId !== s.opId) return d
      const { opId: _, ...rest } = d
      return rest
    })
  }
  /**
   * Drops every note and the summary, keeping nothing. A base the page no longer offers goes with them, for `fallback`: the base the diff
   * shown was read against with none asked (the run's), which the new draft is then made on, as knowBase would have made it.
   */
  function discard(fallback = ''): void {
    draft.value = { ...emptyDraft(), base: stale.value ? fallback : draft.value.base }
    save()
  }

  return { draft, unsaved, lock, stale, load, knowBase, setBase, add, update, drop, setVerdict, setSummary, sending, sent, unsent, discard }
}
