import { describe, expect, it } from 'vitest'
import type { BrakemanDigest, BrakemanRequest, BrakemanRow } from './brakeman'
import { KINDS } from './brakeman'
import { EARLIER_FILTERS, MAX_COUNTS, MAX_SEARCH, askAgain, localMatch, searchProblem, askedWords, briefHeadWords, clockOfDay, countsWords, kindLine, madeCount, noBriefWords, rowAge, rowMeta, rowTitle, shortKind, subjectWords } from './brakemanRows'

// A Brakeman request as a row says it (issue 993, F7): the kinds' own words, the subjects by name, at most three counts in their
// order, "no decision" for a brief with none, the age, who asked, and when Ask again is offered.

function req(id: string, state: string, over: Partial<BrakemanRow> = {}): BrakemanRow {
  return { id, opId: `op-${id}`, kind: 'explain', createdAt: '2026-11-17T09:00:00Z', state, agent: {}, config: { visibility: {} as never, scope: {}, permissions: { observe: true, draft: true, propose: true }, budget: { turnMinutes: 15, readsPerTurn: 300, turnsPerDay: 40 }, rev: 1 }, reads: 0, ...over } as BrakemanRow
}
const digest = (counts: BrakemanDigest['counts']): BrakemanDigest => ({ schema: 1, title: 'Build is out of attempts', reading: '', counts, about: {} })
const names = { run: (id: string) => (id === 'fix-login-1a2b3c4d' ? 'fix-login' : undefined), session: (id: string) => (id === '0123456789abcdef' ? 'perf probe' : undefined) }

describe('a kind in short', () => {
  it("is the kind's own label without the chip's ellipsis, one set of words", () => {
    expect(KINDS.map((k) => shortKind(k.value))).toEqual(['Brief me since', 'Explain this', 'Prepare a decision', 'Draft tickets', 'Propose a crew', 'Prepare a handoff', 'Find', 'Overlapping changes', 'A question', 'Compare approaches'])
    for (const k of KINDS) expect(k.label.startsWith(shortKind(k.value))).toBe(true)
    expect(shortKind('wake')).toBe('Woken by events')
    expect(shortKind('something_new')).toBe('something_new')
  })

  it('leads a briefing with its start, in the browser\'s clock', () => {
    expect(kindLine({ kind: 'briefing', since: '2026-11-16T18:00:00Z' })).toBe(`Brief me since ${clockOfDay('2026-11-16T18:00:00Z')}`)
    expect(clockOfDay('2026-11-16T18:00:00Z')).toMatch(/^\d\d:\d\d$/)
    expect(kindLine({ kind: 'briefing' })).toBe('Brief me since the last briefing')
    expect(kindLine({ kind: 'explain', since: '2026-11-16T18:00:00Z' })).toBe('Explain this')
    expect(clockOfDay('not a time')).toBe('')
  })
})

describe('the subjects', () => {
  it("are named, a task's, an attempt's and a check's with its run's name", () => {
    expect(subjectWords(['task:fix-login-1a2b3c4d/build'], names)).toBe('task build of fix-login')
    expect(subjectWords(['attempt:fix-login-1a2b3c4d/build/3'], names)).toBe('build, attempt 3 of fix-login')
    expect(subjectWords(['run:fix-login-1a2b3c4d', 'ticket:CND-41', 'session:0123456789abcdef'], names)).toBe('run fix-login, CND-41, session perf probe')
    // A run the page has no name for is named by its id.
    expect(subjectWords(['task:other-0a0b0c0d/deploy'], names)).toBe('task deploy of other-0a0b0c0d')
    expect(subjectWords(undefined)).toBe('')
    expect(subjectWords([])).toBe('')
  })
})

describe("a brief's counts", () => {
  it('say at most three, in their order, decisions first', () => {
    expect(countsWords(digest({ decisions: 1, proposals: 2, drafts: 3, facts: 4 }))).toEqual(['1 decision', '2 proposals', '3 drafts'])
    expect(countsWords(digest({ decisions: 2, facts: 1 }))).toEqual(['2 decisions', '1 fact'])
    expect(countsWords(digest({ decisions: 1, proposals: 1, drafts: 1, facts: 1 })).length).toBe(MAX_COUNTS)
  })

  it('say "no decision" where there is none, in the last place left, never "for you"', () => {
    expect(countsWords(digest({ facts: 2 }))).toEqual(['2 facts', 'no decision'])
    expect(countsWords(digest({ proposals: 1, drafts: 3, facts: 2 }))).toEqual(['1 proposal', '3 drafts', 'no decision'])
    expect(countsWords(digest({}))).toEqual(['no decision'])
    expect(countsWords(digest({ interpretations: 2, evidence: 4 }))).toEqual(['no decision'])
    for (const w of countsWords(digest({ facts: 1 }))) expect(w).not.toMatch(/for you/)
  })

  it('say how many drafts were made, each item once by its latest claim', () => {
    const made = [
      { item: 1, opId: 'a', state: 'made', at: '2026-11-17T09:01:00Z' },
      { item: 2, opId: 'b', state: 'made', at: '2026-11-17T09:02:00Z' },
      { item: 3, opId: 'c', state: 'refused', at: '2026-11-17T09:03:00Z' },
      { item: 1, opId: 'd', state: 'made', at: '2026-11-17T09:04:00Z' },
    ]
    expect(madeCount(made)).toBe(2)
    expect(countsWords(digest({ drafts: 3 }), made)).toEqual(['3 drafts, 2 made', 'no decision'])
    expect(countsWords(digest({ drafts: 3 }), [])).toEqual(['3 drafts', 'no decision'])
    expect(madeCount([{ item: 1, opId: 'a', state: 'made', at: '1' }, { item: 1, opId: 'b', state: 'deleted', at: '2' }])).toBe(0)
    // By the claim's time, not its place in the list.
    expect(madeCount([{ item: 1, opId: 'b', state: 'deleted', at: '2' }, { item: 1, opId: 'a', state: 'made', at: '1' }])).toBe(0)
  })

  it('are none without a digest', () => {
    expect(countsWords(undefined)).toEqual([])
  })
})

describe('a row', () => {
  const now = Date.parse('2026-11-17T11:00:00Z')

  it('is as old as its brief, else its close, else its ask', () => {
    expect(rowAge({ createdAt: '2026-11-17T08:00:00Z', closedAt: '2026-11-17T09:00:00Z', briefAt: '2026-11-17T10:00:00Z' }, now)).toBe('1h ago')
    expect(rowAge({ createdAt: '2026-11-17T08:00:00Z', closedAt: '2026-11-17T09:00:00Z' }, now)).toBe('2h ago')
    expect(rowAge({ createdAt: '2026-11-16T08:00:00Z' }, now)).toBe('1d ago')
  })

  it('says who asked by the name it was sent with, never "you"', () => {
    expect(askedWords({ kind: 'explain', by: 'Nate' })).toBe('Asked by Nate')
    expect(askedWords({ kind: 'explain' })).toBe('Asked')
    expect(askedWords({ kind: 'wake', by: 'Nate' })).toBe('Woken by events')
  })

  it('leads with its title, and a closed one with no brief says why', () => {
    expect(rowTitle(req('br-3', 'briefed', { hasBrief: true, digest: digest({}) }))).toBe('Build is out of attempts')
    expect(rowTitle(req('br-3', 'timed_out'))).toBe('')
    expect(noBriefWords(req('br-3', 'timed_out'))).toBe('no brief: timed out after 15 min')
    expect(noBriefWords(req('br-3', 'cancelled'))).toBe('no brief: cancelled')
    expect(noBriefWords(req('br-3', 'running'))).toBe('')
    expect(noBriefWords(req('br-3', 'briefed', { hasBrief: true }))).toBe('')
  })

  it("says its kind, its subjects and its counts on its second line, or why it has no brief", () => {
    expect(rowMeta(req('br-3', 'briefed', { hasBrief: true, subjects: ['task:fix-login-1a2b3c4d/build'], digest: digest({ proposals: 1 }) }), names)).toEqual(['Explain this', 'task build of fix-login', '1 proposal', 'no decision'])
    expect(rowMeta(req('br-4', 'briefed', { kind: 'wake', hasBrief: true, digest: digest({ facts: 2 }) }), names)).toEqual(['Woken by events', '2 facts', 'no decision'])
    expect(rowMeta(req('br-5', 'timed_out', { kind: 'briefing', since: '2026-11-16T18:00:00Z' }))).toEqual([`Brief me since ${clockOfDay('2026-11-16T18:00:00Z')}`, 'no brief: timed out after 15 min'])
    // An older server's row (no digest) says its kind and subjects alone.
    expect(rowMeta(req('br-6', 'briefed', { hasBrief: true, subjects: ['ticket:CND-41'], kind: 'draft_tickets' }))).toEqual(['Draft tickets', 'CND-41'])
  })
})

describe('Ask again', () => {
  const timedOut = req('br-5', 'timed_out', { kind: 'explain', subjects: ['task:fix-login-1a2b3c4d/build'], note: 'why?' })

  it('sends the same ask under a new id, for a closed request with no brief', () => {
    expect(askAgain(timedOut, [])).toEqual({ kind: 'explain', subjects: ['task:fix-login-1a2b3c4d/build'], note: 'why?' })
    expect(askAgain(req('br-6', 'failed', { kind: 'briefing', since: '2026-11-16T18:00:00Z' }), [])).toEqual({ kind: 'briefing', since: '2026-11-16T18:00:00Z' })
  })

  it('is not offered for a cancelled request, a wake, a brief, one still open, or while the same ask waits', () => {
    expect(askAgain(req('br-5', 'cancelled'), [])).toBeNull()
    expect(askAgain(req('br-5', 'timed_out', { kind: 'wake' }), [])).toBeNull()
    expect(askAgain(req('br-5', 'briefed', { hasBrief: true }), [])).toBeNull()
    expect(askAgain(req('br-5', 'running'), [])).toBeNull()
    expect(askAgain(timedOut, [req('br-7', 'queued', { kind: 'explain', subjects: ['task:fix-login-1a2b3c4d/build'], note: 'why?' })])).toBeNull()
    // Another ask waiting, or the same one closed already, leaves it offered.
    expect(askAgain(timedOut, [req('br-7', 'queued', { kind: 'explain', subjects: ['task:fix-login-1a2b3c4d/deploy'], note: 'why?' })])).not.toBeNull()
    expect(askAgain(timedOut, [{ ...timedOut, id: 'br-8' } as BrakemanRequest])).not.toBeNull()
  })
})

describe("a brief's head line", () => {
  it('says latest, its age and who it is of', () => {
    const now = Date.parse('2026-11-17T11:12:00Z')
    expect(briefHeadWords(req('br-14', 'briefed', { kind: 'wake', briefAt: '2026-11-17T11:00:00Z' }), true, now)).toBe('Latest brief · 12m ago · woken by events')
    expect(briefHeadWords(req('br-9', 'briefed', { by: 'Nate', briefAt: '2026-11-17T09:12:00Z' }), false, now)).toBe('br-9 · 2h ago · asked by Nate')
  })
})

describe("Earlier's filters and search", () => {
  it('are the list route\'s queries', () => {
    expect(EARLIER_FILTERS.map((f) => [f.key, f.label, f.query])).toEqual([
      ['all', 'All', undefined],
      ['decision', 'With a decision', { has: 'decision' }],
      ['briefings', 'Briefings', { kind: ['briefing', 'wake'] }],
      ['drafts', 'Drafts', { has: 'draft' }],
    ])
  })

  it('send one line of at most 200 bytes', () => {
    expect(searchProblem('')).toBe('')
    expect(searchProblem('  cache test  ')).toBe('')
    expect(searchProblem('a'.repeat(MAX_SEARCH))).toBe('')
    expect(searchProblem('a'.repeat(MAX_SEARCH + 1))).toBe('A search is at most 200 bytes.')
    // Bytes, not characters: 67 three-byte characters are 201 bytes.
    expect(searchProblem('é'.repeat(100))).toBe('')
    expect(searchProblem('€'.repeat(67))).toBe('A search is at most 200 bytes.')
    expect(searchProblem('a\nb')).toBe('A search is one line.')
  })

  it("search the pages read on an older server, by title, note, subjects and their names, kind and id", () => {
    const r = req('br-12', 'briefed', { hasBrief: true, note: 'Why is the CACHE slow?', subjects: ['task:fix-login-1a2b3c4d/build'], digest: { ...digest({}), title: 'Build is out of attempts' } })
    for (const q of ['out of attempts', 'cache', 'fix-login-1a2b3c4d', 'build of fix-login', 'explain this', 'br-12', '']) expect(localMatch(r, q, names)).toBe(true)
    expect(localMatch(r, 'deploy', names)).toBe(false)
    expect(localMatch(req('br-1', 'briefed'), 'cache')).toBe(false)
  })
})
