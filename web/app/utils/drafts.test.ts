import { describe, expect, it } from 'vitest'
import { DRAFT_REFUSALS, localDrafts, MAX_DRAFT_BYTES, MAX_DRAFTS } from './drafts'
import type { ShellDraft } from './shell'

// Drafts through ShellDrafts (docs/plans/phone-shell.md, OSS-2b, Tests per surface, drafts.test.ts): drafts.ts's implementation over
// the browser's local storage, held to the interface's cases: read back by a new instance over the same store (a restart) with its
// responseId, rev and state; each refusal with its words and never as kept; another sign-in's drafts invisible; a put after a
// sign-out refused; a sign-out removing every draft; a draft older than seven days removed at read.

const T0 = Date.parse('2026-11-19T10:00:00Z')
const draft = (id: string, extra: Partial<ShellDraft> = {}): ShellDraft => ({ id, responseId: `r-${id}`, rev: 3, choice: 1, state: 'editing', savedAt: new Date(T0).toISOString(), ...extra })

function store() {
  const m = new Map<string, string>()
  return { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v), removeItem: (k: string) => void m.delete(k), m }
}

describe('drafts over the browser’s storage', () => {
  it('reads a draft back after a restart, with its responseId, rev and state', async () => {
    const s = store()
    expect(await localDrafts(s, () => 'ana', () => T0).put(draft('i1', { state: 'unknown', h: 'abc' }))).toEqual({ ok: true })
    const again = localDrafts(s, () => 'ana', () => T0)
    expect(await again.get('i1')).toEqual(draft('i1', { state: 'unknown', h: 'abc' }))
    expect(await again.list()).toHaveLength(1)
  })

  it('refuses a draft past 12 KiB and a 33rd, each with its words, never kept', async () => {
    const s = store()
    const d = localDrafts(s, () => 'ana', () => T0)
    const big = await d.put(draft('big', { text: 'x'.repeat(MAX_DRAFT_BYTES) }))
    expect(big).toEqual({ ok: false, code: 'too_large', words: DRAFT_REFUSALS.too_large })
    expect(await d.get('big')).toBeNull()
    for (let i = 0; i < MAX_DRAFTS; i++) expect(await d.put(draft(`i${i}`))).toEqual({ ok: true })
    expect(await d.put(draft('one-more'))).toEqual({ ok: false, code: 'full', words: DRAFT_REFUSALS.full })
    expect(await d.get('one-more')).toBeNull()
    // The same item put again is no new draft.
    expect(await d.put(draft('i0', { choice: 0 }))).toEqual({ ok: true })
  })

  it('answers not_saved when the storage refuses a write', async () => {
    const s = store()
    const d = localDrafts({ ...s, setItem: () => { throw new Error('quota') } }, () => 'ana', () => T0)
    expect(await d.put(draft('i1'))).toEqual({ ok: false, code: 'not_saved', words: DRAFT_REFUSALS.not_saved })
    expect(await d.get('i1')).toBeNull()
  })

  it("keeps another sign-in's drafts invisible, refuses a put after a sign-out, and a sign-out removes every draft", async () => {
    const s = store()
    let who: string | null = 'ana'
    const d = localDrafts(s, () => who, () => T0)
    await d.put(draft('i1'))
    who = 'bo'
    expect(await d.get('i1')).toBeNull()
    expect(await d.list()).toEqual([])
    who = null
    expect(await d.put(draft('i2'))).toEqual({ ok: false, code: 'signed_out', words: DRAFT_REFUSALS.signed_out })
    who = 'ana'
    expect(await d.get('i1')).not.toBeNull()
    d.signOut()
    expect(await d.list()).toEqual([])
    expect(s.m.size).toBe(0)
  })

  it('removes a draft older than seven days when it is read', async () => {
    const s = store()
    await localDrafts(s, () => 'ana', () => T0).put(draft('old', { savedAt: new Date(T0 - 8 * 86_400_000).toISOString() }))
    await localDrafts(s, () => 'ana', () => T0).put(draft('new', { savedAt: new Date(T0 - 6 * 86_400_000).toISOString() }))
    const d = localDrafts(s, () => 'ana', () => T0)
    expect((await d.list()).map((x) => x.id)).toEqual(['new'])
    expect(s.m.get('conductor.shell.drafts')).not.toContain('"old"')
  })
})
