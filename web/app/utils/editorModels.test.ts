import { describe, expect, it } from 'vitest'
import { closeWords, conflictWords, forgetModel, hasModel, isDirty, markSaved, noteVersion, savedAt, unsavedNow } from './editorModels'

describe('a save completing', () => {
  it('marks the version it sent saved, and what was typed since stays unsaved', () => {
    noteVersion('/r/s.go', 4)
    markSaved('/r/s.go', 1)
    // Sent at version 4; typed on to 6 while the answer was on its way.
    noteVersion('/r/s.go', 6)
    expect(savedAt('/r/s.go', 4)).toBe(true)
    expect(isDirty('/r/s.go', 4)).toBe(false)
    expect(isDirty('/r/s.go', 6)).toBe(true)
  })
  it('leaves a model with nothing typed since clean', () => {
    noteVersion('/r/t.go', 7)
    expect(savedAt('/r/t.go', 7)).toBe(false)
    expect(isDirty('/r/t.go', 7)).toBe(false)
  })
  it('touches no other file', () => {
    noteVersion('/r/u.go', 2)
    markSaved('/r/u.go', 1)
    noteVersion('/r/v.go', 3)
    savedAt('/r/v.go', 3)
    expect(isDirty('/r/u.go', 2)).toBe(true)
  })
  it('forgets a model whose text was not what was sent (a save from the compare view)', () => {
    markSaved('/r/w.go', 1)
    expect(savedAt('/r/w.go', undefined)).toBe(false)
    expect(hasModel('/r/w.go')).toBe(false)
  })
})

describe('the editor\'s saved versions', () => {
  it('knows an unsaved model by its version', () => {
    expect(isDirty('/r/a.go', 3)).toBe(false)
    markSaved('/r/a.go', 1)
    expect(hasModel('/r/a.go')).toBe(true)
    expect(isDirty('/r/a.go', 1)).toBe(false)
    expect(isDirty('/r/a.go', 2)).toBe(true)
    markSaved('/r/a.go', 2)
    expect(isDirty('/r/a.go', 2)).toBe(false)
    forgetModel('/r/a.go')
    expect(hasModel('/r/a.go')).toBe(false)
    expect(isDirty('/r/a.go', 9)).toBe(false)
  })
})

describe('unsavedNow: a model unsaved with no editor of it mounted (UX3 R72)', () => {
  it('is its edits as its editor saw them last, and nothing for a path never read', () => {
    markSaved('/kept/a.txt', 3)
    noteVersion('/kept/a.txt', 3)
    expect(unsavedNow('/kept/a.txt')).toBe(false)
    noteVersion('/kept/a.txt', 5)
    expect(unsavedNow('/kept/a.txt')).toBe(true)
    forgetModel('/kept/a.txt')
    expect(unsavedNow('/kept/a.txt')).toBe(false)
    expect(unsavedNow('/kept/never.txt')).toBe(false)
  })
})

describe('the words', () => {
  it('say what changed the file on disk', () => {
    expect(conflictWords({ at: '2026-10-09T08:34:10Z', by: 'codex', tool: 'Edit', exists: true })).toMatch(/^Changed on disk since you opened it · \d{2}:\d{2}:\d{2} · codex \(Edit\)\. Saving would overwrite that\.$/)
    expect(conflictWords({ mtime: 'garbage', exists: true })).toBe('Changed on disk since you opened it. Saving would overwrite that.')
    expect(conflictWords({ exists: false })).toBe('Deleted on disk since you opened it. Saving would write it again.')
    expect(conflictWords(null)).toBe('')
  })
  it('ask before closing unsaved tabs', () => {
    expect(closeWords(['users.go'])).toEqual({ title: 'Save users.go?', words: "Your changes are lost if you don't save them." })
    expect(closeWords(['a.go', 'b.go']).title).toBe('Close 2 files with unsaved changes?')
  })
})
