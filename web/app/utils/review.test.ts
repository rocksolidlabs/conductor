import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import {
  MAX_ANCHOR_LINE,
  MAX_ANCHOR_LINES,
  MAX_REPAIR_PAYLOAD,
  applicationWords,
  batchProblems,
  changeRequestOf,
  findingOf,
  findingState,
  goJSONBytes,
  locatedWords,
  noteFromSelection,
  noteWhere,
  offeredBases,
  repairBytes,
  reviewBody,
  type ChangeRequest,
  type DraftNote,
} from './review'

// The fixture internal/crew's TestFitChangeRequestAtThePayload writes and holds repairBytes to: a person's change request as the task's
// row keeps its repair, its size in Go's JSON, at the edge of what the run keeps and one byte past it.
const fixture = JSON.parse(readFileSync(fileURLToPath(new URL('../../../internal/crew/testdata/change-request-payload.json', import.meta.url)), 'utf8')) as {
  maxPayload: number
  cases: Array<{ name: string; request: ChangeRequest; bytes: number; fits: boolean }>
}

const C = 'a'.repeat(40)
const B = 'b'.repeat(40)
const cand = `run-1/build/1@${C}`
let k = 0
function note(n: Partial<DraftNote>): DraftNote {
  return { key: `n${++k}`, kind: 'general', intent: 'note', message: 'words', ...n }
}

describe('the batch measured as the run keeps it', () => {
  it('holds the fixture the engine is held to', () => {
    expect(fixture.maxPayload).toBe(MAX_REPAIR_PAYLOAD)
    expect(fixture.cases.length).toBeGreaterThanOrEqual(4)
    for (const c of fixture.cases) {
      expect(repairBytes(c.request), c.name).toBe(c.bytes)
      expect(repairBytes(c.request) <= MAX_REPAIR_PAYLOAD, c.name).toBe(c.fits)
    }
  })

  it('counts what Go escapes for HTML at six bytes, as the server does', () => {
    expect(goJSONBytes('<')).toBe(8)
    expect(goJSONBytes('a&b>')).toBe(16)
    expect(goJSONBytes(' ')).toBe(8)
    expect(goJSONBytes('é')).toBe(4)
    expect(goJSONBytes('\t\n')).toBe(6)
    expect(goJSONBytes('\u0001')).toBe(8)
  })

  it('measures a draft as the server builds its request: placeholder ids, the summary the repair\'s, the display name', () => {
    const body = reviewBody(cand, B, 'request_changes', ' Two things. ', [note({ kind: 'line', intent: 'change', severity: 'major', path: 'a.txt', side: 'candidate', from: 1, to: 2, lines: ['x', 'y'], message: ' fix ' })])
    const cr = changeRequestOf(body, 'Nate')
    expect(cr).toEqual({
      review: '0000000000000000',
      by: 'Nate',
      via: 'web',
      base: B,
      summary: 'Two things.',
      changes: [{ id: '0000000000000001', kind: 'line', intent: 'change', severity: 'major', path: 'a.txt', side: 'candidate', from: 1, to: 2, lines: ['x', 'y'], message: 'fix' }],
    })
    // The summary counts once, in the repair, not in its request.
    const without = repairBytes({ ...cr, summary: undefined })
    expect(repairBytes(cr) - without).toBe(goJSONBytes({ summary: 'Two things.' }) - 2 + 1)
  })

  it('refuses a request past 64 KiB in the server\'s words, and lets the same notes go as a comment', () => {
    const big = Array.from({ length: 50 }, () => note({ intent: 'change', severity: 'nit', message: '<'.repeat(1000), resolution: '<'.repeat(1000) }))
    const asked = reviewBody(cand, B, 'request_changes', '', big)
    expect(batchProblems(asked, 'admin').at(-1)).toMatch(/^The notes are \d+ KiB as the run keeps them, more than 64 KiB: shorten them\.$/)
    expect(batchProblems({ ...asked, verdict: 'comment' }, 'admin')).toEqual([])
  })
})

describe('a note from a selection', () => {
  const file = ['one', 'two', 'three\r', 'é'.repeat(150), ...Array.from({ length: 30 }, (_, i) => `line ${i + 5}`)]

  it('keeps the range in order on either side, and the lines as the person saw them', () => {
    expect(noteFromSelection('a.txt', 'candidate', file, 2, 1)).toEqual({ kind: 'line', path: 'a.txt', side: 'candidate', from: 1, to: 2, lines: ['one', 'two'] })
    expect(noteFromSelection('a.txt', 'base', file, 3, 3)).toEqual({ kind: 'line', path: 'a.txt', side: 'base', from: 3, to: 3, lines: ['three�'] })
  })

  it('cuts the lines to twelve of 200 bytes, the store\'s bounds, and keeps the range whole', () => {
    const n = noteFromSelection('a.txt', 'candidate', file, 4, 30)
    expect(n.from).toBe(4)
    expect(n.to).toBe(30)
    expect(n.lines).toHaveLength(MAX_ANCHOR_LINES)
    expect(new TextEncoder().encode(n.lines![0]!).length).toBeLessThanOrEqual(MAX_ANCHOR_LINE)
    expect(n.lines![0]).toBe('é'.repeat(100))
  })

  it('stays within the file', () => {
    expect(noteFromSelection('a.txt', 'candidate', ['x'], 5, 9)).toMatchObject({ from: 1, to: 1, lines: ['x'] })
  })

  it('is sent with only what its kind names', () => {
    expect(findingOf(note({ kind: 'general', intent: 'question', severity: 'major', path: 'x', side: 'base', resolution: 'r', message: ' why? ' }))).toEqual({ kind: 'general', intent: 'question', message: 'why?' })
    expect(findingOf(note({ kind: 'file', intent: 'change', severity: 'minor', path: 'x', side: 'candidate', from: 3, to: 4, lines: ['l'], resolution: ' do ' }))).toEqual({
      kind: 'file',
      intent: 'change',
      severity: 'minor',
      path: 'x',
      side: 'candidate',
      message: 'words',
      resolution: 'do',
    })
    expect(noteWhere({ kind: 'line', path: 'x', side: 'base', from: 3, to: 4 })).toBe('x:3–4 (base)')
    expect(noteWhere({ kind: 'file', path: 'x', side: 'candidate' })).toBe('x (the whole file)')
  })
})

describe("the batch's checks", () => {
  it('asks for a note or a summary, a change in a request, words on every note', () => {
    expect(batchProblems(reviewBody(cand, B, 'comment', '', []), 'admin')).toEqual(['Add a note or a summary first.'])
    expect(batchProblems(reviewBody(cand, B, 'approve', 'Looks right.', []), 'admin')).toEqual([])
    expect(batchProblems(reviewBody(cand, B, 'request_changes', '', [note({ intent: 'question' })]), 'admin')).toEqual(['Request changes needs at least one note that asks for a change.'])
    expect(batchProblems(reviewBody(cand, B, 'comment', '', [note({ message: '  ' })]), 'admin')).toEqual(['Note 1 has no words.'])
  })

  it('holds each text to its bound, characters for words and bytes for a path', () => {
    const ok = note({ message: 'é'.repeat(1000) })
    expect(batchProblems(reviewBody(cand, B, 'comment', '', [ok]), 'admin')).toEqual([])
    const words = batchProblems(reviewBody(cand, B, 'comment', 'x'.repeat(4001), [note({ message: 'x'.repeat(1001) }), note({ kind: 'file', path: 'é'.repeat(513), side: 'candidate' })]), 'admin')
    expect(words).toEqual(['The summary is more than 4000 characters.', "Note 1's words are more than 1000 characters.", "Note 2's path is more than 1024 bytes."])
    expect(batchProblems(reviewBody(cand, B, 'comment', '', Array.from({ length: 51 }, () => note({}))), 'admin')).toEqual(['A review holds at most 50 notes: this one has 51.'])
  })
})

describe('words', () => {
  it("say a review's application as separate facts", () => {
    expect(applicationWords({ state: 'applied', run: 'r1', command: 'rv.x.1', revision: 9, attempt: 2 }, 'build', C)).toEqual({
      title: 'Applied by Yardmaster',
      detail: 'In run r1: build reopened, its attempt 2 starts at aaaaaaaaaaaa (command rv.x.1, revision 9).',
      tone: 'success',
    })
    expect(applicationWords({ state: 'refused', why: 'the result is now attempt 4' }, 'build', C)).toMatchObject({ title: 'Refused by Yardmaster', detail: 'the result is now attempt 4', tone: 'error' })
    expect(applicationWords({ state: 'pending' }, 'build', C).detail).toBe('Yardmaster took the request; the ledger has not recorded it yet.')
    expect(applicationWords({ state: 'none' }, 'build', C).title).toBe('Recorded')
  })

  it('say where a finding\'s lines are, never whether it is resolved', () => {
    expect(locatedWords({ state: 'same' })).toBe('same lines')
    expect(locatedWords({ state: 'moved', from: 41, to: 44 })).toBe('moved: now 41–44')
    expect(locatedWords({ state: 'moved', from: 5, to: 5 })).toBe('moved: now 5')
    expect(locatedWords({ state: 'changed' })).toBe('the lines changed')
    expect(locatedWords({ state: 'file_gone' })).toBe('the file is gone')
    expect(locatedWords({ state: 'renamed', path: 'b.go', located: { state: 'moved', from: 3, to: 4 } })).toBe('renamed to b.go, moved: now 3–4')
    expect(locatedWords({ state: 'unknown', why: 'the file is binary' })).toBe('not located: the file is binary')
    expect(locatedWords(null)).toBe('')
  })

  it("take a finding's state from its latest mark, open with none", () => {
    expect(findingState([])).toBe('open')
    expect(findingState([{ verdict: 'resolved' }, { verdict: 'open' }])).toBe('open')
    expect(findingState([{ verdict: 'open' }, { verdict: 'withdrawn' }])).toBe('withdrawn')
  })
})

describe('the bases offered', () => {
  it("are the run's base, where the attempt started and the task's other results, each once", () => {
    const S = 'c'.repeat(40)
    expect(offeredBases(B, { attempt: 2, start: S, commit: C }, [{ attempt: 2, commit: C }, { attempt: 1, commit: S }])).toEqual([
      { commit: B, label: "the run's base" },
      { commit: S, label: "where attempt 2 started, attempt 1's result" },
    ])
    expect(offeredBases(B, { attempt: 1, start: B, commit: C }, [])).toEqual([{ commit: B, label: "the run's base, where attempt 1 started" }])
    expect(offeredBases(undefined, { attempt: 1, commit: C }, [])).toEqual([])
  })
})
