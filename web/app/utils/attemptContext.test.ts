import { describe, expect, it } from 'vitest'
import { type AttemptContextRow, contextBodyPath, contextCountWords, contextReadError, contextSize, contextSummary, phaseWords, shortDigest } from './attemptContext'

const row = (seq: number, phase: AttemptContextRow['phase'], extra: Partial<AttemptContextRow> = {}): AttemptContextRow => ({
  seq,
  phase,
  source: 'graph',
  title: `Block ${seq}`,
  sha256: 'ab'.repeat(32),
  bytes: 17,
  at: '2026-11-19T10:00:00Z',
  ...extra,
})

describe('attemptContext', () => {
  it('says each phase', () => {
    expect(phaseWords('task')).toBe('at start')
    expect(phaseWords('lookup')).toBe('looked up')
  })

  it('counts the blocks by phase, and says nothing of an empty list', () => {
    expect(contextSummary([row(1, 'task'), row(2, 'task'), row(3, 'task'), row(4, 'lookup'), row(5, 'lookup')])).toBe('Context: 3 at start, 2 looked up')
    expect(contextSummary([row(1, 'lookup')])).toBe('Context: 1 looked up')
    expect(contextSummary([row(1, 'task')])).toBe('Context: 1 at start')
    expect(contextSummary([])).toBe('')
    expect(contextSummary(undefined)).toBe('')
  })

  it("counts a candidate's blocks", () => {
    expect(contextCountWords(1)).toBe('context: 1 block')
    expect(contextCountWords(5)).toBe('context: 5 blocks')
  })

  it('shortens a digest to 12 characters', () => {
    expect(shortDigest('0123456789abcdef'.repeat(4))).toBe('0123456789ab')
  })

  it('says a size', () => {
    expect(contextSize(17)).toBe('17 B')
    expect(contextSize(8192)).toBe('8 KiB')
    expect(contextSize(1536)).toBe('1.5 KiB')
  })

  it('reads a row with no mark and no ref as it is', () => {
    const r = row(1, 'task')
    expect(r.pack).toBeUndefined()
    expect(contextSummary([r])).toBe('Context: 1 at start')
  })

  it("names a block's text by its attempt and seq, every id encoded", () => {
    expect(contextBodyPath('team-0000aaaa', 'build', 2, 3)).toBe('/api/runs/team-0000aaaa/tasks/build/attempts/2/context/3')
    expect(contextBodyPath('a/b', 'c d', 1, 1)).toBe('/api/runs/a%2Fb/tasks/c%20d/attempts/1/context/1')
  })

  it('says why a text could not be read', () => {
    expect(contextReadError({ status: 404 })).toMatch(/not kept/)
    expect(contextReadError({ status: 500, message: 'boom' })).toBe('boom')
    expect(contextReadError(undefined)).toBe('The text could not be read.')
  })
})
