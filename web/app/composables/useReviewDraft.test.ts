import { beforeEach, describe, expect, it, vi } from 'vitest'
import * as vue from 'vue'
import { draftKey, readDraft, useReviewDraft, writeDraft, type DraftStorage } from './useReviewDraft'
import type { DraftNote } from '~/utils/review'

// The draft of a review (Program item RV1, PR-6): kept per ticket and candidate in this browser's storage, every access in try/catch;
// its base is the review's, moved by the picker while no note is on the base side and locked by one; a stored base the page no longer
// offers is not sent. useReviewDraft reads Vue's ref, computed and watch as Nuxt's auto-imports: each is stubbed with Vue's own.

const RUN_BASE = 'b'.repeat(40)
const START = 's'.repeat(40)
const OTHER = 'o'.repeat(40)
const C1 = `run-1/build/1@${'a'.repeat(40)}`
const C2 = `run-1/build/2@${'c'.repeat(40)}`

function memory(): DraftStorage & { map: Map<string, string> } {
  const map = new Map<string, string>()
  return { map, getItem: (k) => map.get(k) ?? null, setItem: (k, v) => void map.set(k, v), removeItem: (k) => void map.delete(k) }
}
const throwing: DraftStorage = {
  getItem: () => {
    throw new Error('SecurityError')
  },
  setItem: () => {
    throw new Error('QuotaExceededError')
  },
  removeItem: () => {
    throw new Error('SecurityError')
  },
}

const candidateLine: Omit<DraftNote, 'key'> = { kind: 'line', intent: 'change', severity: 'major', path: 'a.txt', side: 'candidate', from: 1, to: 1, lines: ['x'], message: 'fix it' }
const baseLine: Omit<DraftNote, 'key'> = { ...candidateLine, side: 'base', path: 'b.txt', message: 'why was this removed?' }

beforeEach(() => {
  for (const k of ['ref', 'computed', 'watch'] as const) vi.stubGlobal(k, vue[k])
})

/** A draft of the candidate the test points `on` at, the bases offered as `bases` holds them. */
function open(storage: DraftStorage | null, on = vue.ref(C1), bases = vue.ref<string[] | null>([RUN_BASE, START])) {
  return { d: useReviewDraft({ ticket: () => 'CND-12', candidate: () => on.value, offered: () => bases.value, storage }), on, bases }
}

describe('the draft kept in this browser', () => {
  it('is kept per ticket and candidate, and read back by a page opened later', async () => {
    const s = memory()
    const { d, on } = open(s)
    d.knowBase(RUN_BASE)
    d.add(candidateLine)
    d.setSummary('Two things.')
    expect(JSON.parse(s.map.get(draftKey('CND-12', C1))!)).toMatchObject({ v: 1, base: RUN_BASE, summary: 'Two things.', notes: [{ ...candidateLine }] })

    on.value = C2
    await vue.nextTick()
    expect(d.draft.value.notes).toEqual([])
    d.add({ ...candidateLine, message: 'on the second' })
    on.value = C1
    await vue.nextTick()
    expect(d.draft.value.notes.map((n) => n.message)).toEqual(['fix it'])

    const again = open(s).d
    expect(again.draft.value).toMatchObject({ base: RUN_BASE, summary: 'Two things.', verdict: 'request_changes' })
    expect(again.draft.value.notes).toHaveLength(1)
  })

  it('works in the page alone when the storage throws, and says it is not kept', () => {
    const { d } = open(throwing)
    expect(d.draft.value.notes).toEqual([])
    d.add(candidateLine)
    expect(d.draft.value.notes).toHaveLength(1)
    expect(d.unsaved.value).toBe(true)
    expect(readDraft(throwing, 'k')).toBeNull()
    expect(writeDraft(throwing, 'k', d.draft.value)).toBe(false)
    expect(open(null).d.draft.value.notes).toEqual([])
  })

  it('forgets a draft that holds nothing, and one it cannot read', () => {
    const s = memory()
    const { d } = open(s)
    d.add(candidateLine)
    d.drop(d.draft.value.notes[0]!.key)
    expect(s.map.size).toBe(0)
    s.map.set(draftKey('CND-12', C1), '{"v":2}')
    expect(open(s).d.draft.value.notes).toEqual([])
    s.map.set(draftKey('CND-12', C1), 'not json')
    expect(open(s).d.draft.value.notes).toEqual([])
  })

  it('is emptied once sent, its base kept for the diff shown; an edit drops the op id of a send not answered', () => {
    const s = memory()
    const { d } = open(s)
    d.knowBase(START)
    d.add(candidateLine)
    d.sending('op-1')
    expect(open(s).d.draft.value.opId).toBe('op-1')
    d.setSummary('more')
    expect(d.draft.value.opId).toBeUndefined()
    const s2 = d.sending('op-2')
    d.sent(s2)
    expect(d.draft.value).toMatchObject({ base: START, notes: [], summary: '' })
    expect(s.map.size).toBe(0)
  })

  it("settles the draft a send was made from when its answer lands, not the one shown then", async () => {
    const s = memory()
    const { d, on } = open(s)
    d.knowBase(RUN_BASE)
    d.add(candidateLine)
    const sending = d.sending('op-1')
    on.value = C2
    await vue.nextTick()
    d.knowBase(RUN_BASE)
    d.add({ ...candidateLine, message: 'on the second, unsent' })
    d.sent(sending)
    expect(d.draft.value.notes.map((n) => n.message)).toEqual(['on the second, unsent'])
    expect(s.map.has(draftKey('CND-12', C1))).toBe(false)
    expect(JSON.parse(s.map.get(draftKey('CND-12', C2))!).notes).toHaveLength(1)

    // A refusal clears the op id of the draft it was sent from alone.
    const second = d.sending('op-2')
    on.value = C1
    await vue.nextTick()
    d.add(candidateLine)
    const third = d.sending('op-3')
    d.unsent(second)
    expect(d.draft.value.opId).toBe('op-3')
    expect(JSON.parse(s.map.get(draftKey('CND-12', C2))!).opId).toBeUndefined()
    d.unsent(third)
    expect(d.draft.value.opId).toBeUndefined()
  })

  it('keeps what was added or edited while a send was under way', () => {
    const s = memory()
    const { d } = open(s)
    d.knowBase(RUN_BASE)
    d.add(candidateLine)
    d.add({ ...candidateLine, message: 'second' })
    d.setSummary('Two things.')
    d.setVerdict('comment')
    const sending = d.sending('op-1')
    const [one, two] = d.draft.value.notes
    d.update(two!.key, { ...candidateLine, message: 'second, edited' })
    d.add({ kind: 'general', intent: 'note', message: 'later' })
    d.sent(sending)
    expect(d.draft.value.notes.map((n) => n.message)).toEqual(['second, edited', 'later'])
    expect(d.draft.value.notes.some((n) => n.key === one!.key)).toBe(false)
    expect(d.draft.value).toMatchObject({ summary: '', verdict: 'comment', base: RUN_BASE })
    expect(d.draft.value.opId).toBeUndefined()
    expect(JSON.parse(s.map.get(draftKey('CND-12', C1))!).notes).toHaveLength(2)
  })

  it('replaces a note whole when its form saves it: a resolution cleared is gone', () => {
    const { d } = open(memory())
    const n = d.add({ ...candidateLine, resolution: 'Say what it prepared.' })
    d.update(n.key, { ...candidateLine, message: 'fix it, no resolution' })
    expect(d.draft.value.notes[0]).not.toHaveProperty('resolution')
    expect(d.draft.value.notes[0]).toMatchObject({ key: n.key, message: 'fix it, no resolution' })
  })
})

describe("the draft's base", () => {
  it('opens at the base the draft was made against, not the default', () => {
    const s = memory()
    const { d } = open(s)
    d.knowBase(RUN_BASE)
    expect(d.setBase(START)).toBe('')
    d.add(baseLine)
    const reloaded = open(s).d
    reloaded.knowBase(RUN_BASE)
    expect(reloaded.draft.value.base).toBe(START)
  })

  it('moves with the picker while every note is on the candidate side, and is locked by a base-side note', () => {
    const { d } = open(memory())
    d.knowBase(RUN_BASE)
    d.add(candidateLine)
    expect(d.lock.value).toBeNull()
    expect(d.setBase(START)).toBe('')
    expect(d.draft.value.base).toBe(START)
    d.add(baseLine)
    expect(d.lock.value).toEqual({ base: START, count: 1 })
    expect(d.setBase(RUN_BASE)).toBe('1 note is on the base sssssss: send or drop it to compare against another.')
    expect(d.draft.value.base).toBe(START)
    d.add({ ...baseLine, kind: 'file', from: undefined, to: undefined, lines: undefined })
    expect(d.setBase(RUN_BASE)).toMatch(/^2 notes are on the base sssssss: send or drop them/)
    // A general note names no side: it never holds the base.
    d.drop(d.draft.value.notes[1]!.key)
    d.drop(d.draft.value.notes[1]!.key)
    d.add({ kind: 'general', intent: 'note', message: 'overall' })
    expect(d.setBase(RUN_BASE)).toBe('')
  })

  it('is shown and not sent when the page no longer offers it', async () => {
    const s = memory()
    const { d, bases } = open(s)
    d.knowBase(OTHER)
    d.add(baseLine)
    expect(d.stale.value).toBe(true)
    bases.value = null
    expect(d.stale.value).toBe(false)
    bases.value = [RUN_BASE, OTHER]
    expect(d.stale.value).toBe(false)
    bases.value = [RUN_BASE]
    d.discard()
    expect(d.draft.value).toMatchObject({ base: '', notes: [] })
    expect(s.map.size).toBe(0)
  })

  it("is made on the base of the diff shown once a stale draft is dropped, so new notes can be made at once", () => {
    const { d } = open(memory(), vue.ref(C1), vue.ref<string[] | null>([RUN_BASE]))
    d.knowBase(OTHER)
    d.add(baseLine)
    expect(d.stale.value).toBe(true)
    d.discard(RUN_BASE)
    expect(d.draft.value).toMatchObject({ base: RUN_BASE, notes: [] })
    expect(d.stale.value).toBe(false)
    expect(d.setBase(RUN_BASE)).toBe('')
  })
})
