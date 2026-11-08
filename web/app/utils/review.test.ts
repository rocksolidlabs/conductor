import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import {
  MAX_ANCHOR_LINE,
  MAX_ANCHOR_LINES,
  MAX_REPAIR_PAYLOAD,
  applicationWords,
  FAILURE_CLASSES,
  checkNote,
  classWords,
  failedReceiptActions,
  findingsInOrder,
  markBlocked,
  recheckWords,
  batchProblems,
  changeRequestOf,
  composerOnFileMove,
  findingOf,
  findingState,
  goJSONBytes,
  locatedWords,
  noteFromSelection,
  noteWhere,
  offeredBases,
  barTop,
  heldUntilReady,
  SELECTION_BAR_HEIGHT,
  repairBytes,
  reviewBody,
  latest,
  noteIsOn,
  notesPerFile,
  requestWords,
  withDraftBase,
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
      detail: 'In run r1: build reopened at aaaaaaaaaaaa, its attempt 2 takes up the request (command rv.x.1, revision 9).',
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

  it("keep a base that is the candidate's own commit: a task that submitted the commit it started at is reviewed against an empty diff", () => {
    expect(offeredBases(C, { attempt: 1, start: C, commit: C }, [{ attempt: 1, commit: C }])).toEqual([{ commit: C, label: "the run's base, where attempt 1 started" }])
    expect(offeredBases(B, { attempt: 2, start: C, commit: C }, [])).toEqual([
      { commit: B, label: "the run's base" },
      { commit: C, label: 'where attempt 2 started' },
    ])
  })
})

describe("the draft's base beyond the candidates listed", () => {
  const S = 'c'.repeat(40)
  const OLD = 'd'.repeat(40)
  const listed = [{ commit: B, label: "the run's base" }]

  it('is offered beside the bases listed until the server refuses it: an older result of the task paged out of the list is still one', () => {
    expect(withDraftBase(listed, OLD, false)).toEqual([...listed, { commit: OLD, label: "the draft's base" }])
    expect(withDraftBase(listed, OLD, true)).toEqual(listed)
    expect(withDraftBase(listed, B, false)).toEqual(listed)
    expect(withDraftBase(listed, '', false)).toEqual(listed)
    expect(withDraftBase(null, S, false)).toBeNull()
  })
})

describe("a renamed file's notes", () => {
  const renamed = { path: 'renamed.txt', from: 'old.txt' }
  const plain = { path: 'a.txt' }

  it("are matched by their side's path: a base-side note by the old one, a candidate-side note by the new one", () => {
    expect(noteIsOn({ path: 'old.txt', side: 'base' }, renamed)).toBe(true)
    expect(noteIsOn({ path: 'renamed.txt', side: 'candidate' }, renamed)).toBe(true)
    expect(noteIsOn({ path: 'renamed.txt', side: 'base' }, renamed)).toBe(false)
    expect(noteIsOn({ path: 'old.txt', side: 'candidate' }, renamed)).toBe(false)
    expect(noteIsOn({ path: 'a.txt', side: 'base' }, plain)).toBe(true)
    expect(noteIsOn({ side: 'candidate' }, plain)).toBe(false)
  })

  it("count both sides' notes for the file the list shows", () => {
    const notes = [
      { path: 'old.txt', side: 'base' as const },
      { path: 'renamed.txt', side: 'candidate' as const },
      { path: 'a.txt', side: 'candidate' as const },
      { path: 'gone.txt', side: 'candidate' as const },
      {},
    ]
    expect(notesPerFile(notes, [renamed, plain])).toEqual({ 'renamed.txt': 2, 'a.txt': 1 })
  })
})

describe('what Request changes asks', () => {
  const S = 'c'.repeat(40)

  it('says the next attempt starts at the reviewed commit', () => {
    expect(requestWords('build', { attempt: 2, commit: C, start: S, state: 'succeeded' }, 3)).toBe('Yardmaster reopens build at aaaaaaaaaaaa: its next attempt starts there with 3 notes, and what rests on it runs again.')
    expect(requestWords('build', { attempt: 2, commit: C, start: S, state: 'failed', stage: 'check go-test' }, 1)).toMatch(/its next attempt starts there with 1 note,/)
  })

  it('says it starts where the attempt started for a candidate refused before its checks, as the engine starts it', () => {
    for (const stage of ['checkout', 'setup', 'setup npm ci', 'manifest', 'service db', 'ready', 'ready http']) {
      expect(requestWords('build', { attempt: 2, commit: C, start: S, state: 'failed', stage }, 2)).toBe(
        'Yardmaster reopens build at aaaaaaaaaaaa with 2 notes: attempt 2 was refused before its checks, so its next attempt starts where it started, at cccccccccccc, and what rests on it runs again.',
      )
    }
    // Not refused (a succeeded candidate's stage says nothing), or no start known: the reviewed commit.
    expect(requestWords('build', { attempt: 2, commit: C, start: S, state: 'succeeded', stage: 'setup' }, 2)).toMatch(/starts there/)
    expect(requestWords('build', { attempt: 2, commit: C, state: 'failed', stage: 'setup' }, 2)).toMatch(/starts there/)
  })
})

describe('the late-answer rule', () => {
  it('holds a request current until a later one is asked', () => {
    const l = latest()
    const first = l.ask()
    expect(first()).toBe(true)
    const second = l.ask()
    expect(first()).toBe(false)
    expect(second()).toBe(true)
    expect(latest().ask()()).toBe(true)
    expect(second()).toBe(true)
  })
})

describe('the bar under a selection', () => {
  it('goes just below the selection, and inside the editor box when the selection is on its last line or scrolled past it', () => {
    expect(barTop(100, 480)).toBe(104)
    expect(barTop(470, 480)).toBe(480 - SELECTION_BAR_HEIGHT - 4)
    expect(barTop(2000, 480)).toBe(480 - SELECTION_BAR_HEIGHT - 4)
    expect(barTop(-60, 480)).toBe(4)
    expect(barTop(10, 0)).toBe(4)
  })
})

describe('a call held until its target is ready', () => {
  it('acts at once on a target there, holds the latest call until one is, and forgets it when the target goes', () => {
    const done: string[] = []
    const h = heldUntilReady((t: string, from: number, to: number) => done.push(`${t}:${from}-${to}`))
    h.call(3, 4)
    h.call(7, 9)
    expect(done).toEqual([])
    h.ready('editor')
    expect(done).toEqual(['editor:7-9'])
    h.call(1, 1)
    expect(done).toEqual(['editor:7-9', 'editor:1-1'])
    h.gone()
    h.call(5, 5)
    h.ready('again')
    expect(done).toEqual(['editor:7-9', 'editor:1-1', 'again:5-5'])
    h.gone()
    h.call(2, 2)
    h.gone()
    h.ready('third')
    expect(done).toHaveLength(3)
  })
})

describe('the note form when the file shown moves', () => {
  it("drops a form on the diff left, and keeps a general note's and an edit's with their text (the first diff coming in among such moves)", () => {
    const general = { note: { kind: 'general' as const, intent: 'change' as const, message: 'typed before the diff came' } }
    const editing = { note: { kind: 'line' as const, intent: 'change' as const, message: 'edited' }, editing: 'k1' }
    expect(composerOnFileMove(general)).toBe(general)
    expect(composerOnFileMove(editing)).toBe(editing)
    expect(composerOnFileMove({ ...general, inline: true, at: 'c|b' })).toBeNull()
    expect(composerOnFileMove({ ...general, at: 'c|b' })).toBeNull()
    expect(composerOnFileMove(null)).toBeNull()
  })
})

describe('a failed receipt', () => {
  const allowed = { allowed: true }
  const refused = { allowed: false, why: 'continue run A first' }
  it('offers Check again exactly when the prepared request says the engine takes it, first for a machine failure, whatever the class', () => {
    expect(failedReceiptActions(allowed, 'infrastructure', false)).toEqual({ actions: ['recheck', 'request'], why: '' })
    expect(failedReceiptActions(allowed, 'flaky', true)).toEqual({ actions: ['recheck', 'request', 'retry'], why: '' })
    expect(failedReceiptActions(allowed, 'code', false)).toEqual({ actions: ['request', 'recheck'], why: '' })
    expect(failedReceiptActions(allowed, 'unavailable', true)).toEqual({ actions: ['request', 'recheck', 'retry'], why: '' })
  })
  it("says why in its place when the engine would not take it, whatever the class: Request changes stays", () => {
    expect(failedReceiptActions(refused, 'infrastructure', false)).toEqual({ actions: ['request'], why: 'continue run A first' })
    expect(failedReceiptActions({ allowed: false }, 'code', true).actions).toEqual(['request', 'retry'])
    expect(failedReceiptActions({ allowed: false }, 'code', true).why).not.toBe('')
  })
  it('names the run a Check again was made in: the candidate\'s, or a continuation of it', () => {
    expect(recheckWords({ run: 'rA', attempt: 1 }, { run: 'rA', attempt: 2 })).toBe('Checked again as attempt 2, the same commit as attempt 1.')
    expect(recheckWords({ run: 'rA', attempt: 1 }, { run: 'rB', attempt: 4 })).toBe('Checked again as attempt 4 of run rB, a continuation of rA.')
  })
  it("says each class in words", () => {
    for (const c of FAILURE_CLASSES) expect(classWords(c).label).not.toBe('')
    expect(classWords('code').label).toBe('code defect')
    expect(classWords('flaky').label).toBe('possibly flaky')
  })
  it('makes a check note the route takes: the receipt, the class chosen and the quote, no path, side or lines', () => {
    const p = {
      finding: { kind: 'check' as const, intent: 'change' as const, message: 'The check test failed on 1111111 (attempt 1): exit 1.', check: { source: 'ledger' as const, receipt: 'r/build/1/check/test/1', run: 'r', task: 'build', attempt: 1, phase: 'check', id: 'test', k: 1, subject: '1'.repeat(40), argv: ['false'], exit: 1, timedOut: false, killed: false, stable: true, state: 'finished', where: 'native', logBytes: 3, tail: 'no\n', class: 'code' as const } },
      criteria: { source: 'ticket' as const, acceptance: 'It is built.' },
      recheck: { allowed: true, run: 'r', revision: 4, attempt: 1, commit: '1'.repeat(40) },
    }
    const n = checkNote(p, 'infrastructure', '  no  ', '')
    expect(findingOf({ ...n, key: 'k' })).toEqual({ kind: 'check', intent: 'change', severity: 'major', message: p.finding.message, check: { source: 'ledger', receipt: 'r/build/1/check/test/1', chosen: 'infrastructure', quote: '  no  ' } })
    expect(noteWhere(n)).toBe('check test failed (r/build/1/check/test/1)')
  })
})

describe('findings across candidates', () => {
  it('list the open first, then the resolved, then the withdrawn, the newest review first, each in its order', () => {
    const f = (id: string, state: 'open' | 'resolved' | 'withdrawn', createdAt: string, k: number) => ({ id, state, createdAt, k })
    const got = findingsInOrder([f('a', 'resolved', '2026-01-02', 1), f('b', 'open', '2026-01-01', 2), f('c', 'withdrawn', '2026-01-03', 1), f('d', 'open', '2026-01-02', 1), f('e', 'open', '2026-01-01', 1)])
    expect(got.map((x) => x.id)).toEqual(['d', 'e', 'b', 'a', 'c'])
  })
  it("are marked against a later candidate of their task, and never resolved on the commit they were made on", () => {
    const x = '1'.repeat(40)
    const y = '2'.repeat(40)
    expect(markBlocked(`r/build/1@${x}`, `r/build/2@${y}`, 'resolved')).toBe('')
    expect(markBlocked(`r/build/1@${x}`, `r/build/1@${x}`, 'withdrawn')).toContain('later candidate')
    expect(markBlocked(`r/build/1@${x}`, `r/build/2@${x}`, 'resolved')).toContain('the commit the finding was made on')
    expect(markBlocked(`r/build/1@${x}`, `r/build/2@${x}`, 'open')).toBe('')
    expect(markBlocked(`r/build/1@${x}`, `r2/build/3@${y}`, 'withdrawn')).toBe('')
    expect(markBlocked(`r/build/1@${x}`, `r/prep/2@${y}`, 'resolved')).toContain('a finding of build')
  })
})
