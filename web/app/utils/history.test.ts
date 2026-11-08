import { describe, expect, it } from 'vitest'
import {
  availabilityWords,
  byAnchor,
  classBadge,
  filterOf,
  historyIcon,
  historySession,
  historyTarget,
  indexingWords,
  isReceipt,
  machineOf,
  withFilter,
  historyDetail,
  historyDetailParts,
  historyQuestions,
  linkLine,
  lostQuestion,
  recordEnding,
  recordEntryWords,
  repeatWords,
  resumeDestination,
  resumeQuestion,
  resumeReceiptWords,
  type HistoryItem,
  type SessionRecord,
} from './history'

// A row's words (Program item HS1, PR-6): the detail line's six fields in their order, each left out when the entry has none; the
// badges, a ✓ only on a check that passed; the link line; repeats; the four questions' queries; availability; a record's words.

const now = Date.parse('2026-11-05T16:03:11Z')

function entry(p: Partial<HistoryItem> = {}): HistoryItem {
  return {
    key: 'check:todo-3f2a91c0/build/1/unit/1',
    kind: 'check',
    class: 'checked',
    outcome: 'failed',
    title: 'unit on build #1',
    at: '2026-11-05T14:03:11Z',
    source: { store: 'ledger', label: 'check receipt', ref: 'check:todo-3f2a91c0/build/1/unit/1' },
    host: { name: 'lab-box', where: 'native' },
    project: { key: 'CND', repo: '/home/nate/code/todo/.git' },
    revision: { label: 'abc1234', commit: 'abc1234def' },
    anchor: { ref: 'task:todo-3f2a91c0/build', label: 'build · todo-3f2a91c0' },
    url: '/runs/todo-3f2a91c0?view=tasks&task=build&attempt=1&step=check/unit/1',
    repeats: { count: 1, anchors: 1 },
    links: [],
    availability: { state: 'log', words: 'log kept' },
    ...p,
  }
}

describe('historyDetail', () => {
  it('says the six fields in their order: source, host, project, revision, when, availability', () => {
    expect(historyDetail(entry(), now)).toBe('check receipt · lab-box · CND · abc1234 · 2h ago · log kept')
    expect(historyDetailParts(entry(), now).map((p) => p.field)).toEqual(['source', 'host', 'project', 'revision', 'when', 'availability'])
  })

  it('keeps the exact time and the full commit for the hover', () => {
    const parts = historyDetailParts(entry(), now)
    expect(parts.find((p) => p.field === 'when')?.title).toBe('2026-11-05T14:03:11Z')
    expect(parts.find((p) => p.field === 'revision')?.title).toBe('abc1234def')
  })

  it('leaves out each field the entry has none of, the order of the rest kept', () => {
    const e = entry({ host: {}, revision: {}, availability: { state: '', words: '' } })
    expect(historyDetail(e, now)).toBe('check receipt · CND · 2h ago')
    expect(historyDetail(entry({ project: {}, at: '' }), now)).toBe('check receipt · lab-box · abc1234 · log kept')
  })

  it("names a project by its repository's folder when no Dispatch project holds it", () => {
    expect(historyDetail(entry({ project: { repo: '/home/nate/code/todo/.git' } }), now)).toContain(' · todo · ')
    // A linked worktree's entry (a crew attempt's) is named as its repository, never as the attempt's folder.
    const linked = historyDetail(entry({ project: { repo: '/home/nate/code/todo/.git', tree: '/home/nate/code/todo/.conductor/worktrees/r/build.1' } }), now)
    expect([linked.includes(' · todo · '), linked.includes('build.1')]).toEqual([true, false])
    expect(historyDetail(entry({ project: { tree: '/home/nate/code/todo' } }), now)).toContain(' · todo · ')
  })

  it('says a hosted host with its user, through conductor host', () => {
    expect(historyDetail(entry({ host: { name: 'mac', user: 'nate', hosted: true } }), now)).toContain('nate@mac via conductor host')
  })
})

describe('classBadge', () => {
  it('draws Conversation, Decision, Checked ✓ and Checked ✗, and nothing for a record', () => {
    expect(classBadge({ class: 'said' })).toEqual({ label: 'Conversation', color: 'neutral' })
    expect(classBadge({ class: 'decided' })).toEqual({ label: 'Decision', color: 'primary' })
    expect(classBadge({ class: 'checked', outcome: 'passed' })).toEqual({ label: 'Checked ✓', color: 'success' })
    expect(classBadge({ class: 'checked', outcome: 'failed' })).toEqual({ label: 'Checked ✗', color: 'error' })
    expect(classBadge({ class: 'record' })).toBeNull()
  })

  it('never draws a ✓ on what was said or decided, whatever its outcome says, nor on a receipt that did not pass', () => {
    for (const outcome of ['passed', 'answered', 'approved', 'failed', 'withdrawn', undefined]) {
      expect(classBadge({ class: 'said', outcome })?.label).not.toContain('✓')
      expect(classBadge({ class: 'decided', outcome })?.label).not.toContain('✓')
    }
    expect(classBadge({ class: 'checked', outcome: 'interrupted' })).toEqual({ label: 'Checked: interrupted', color: 'warning' })
    expect(classBadge({ class: 'checked' })?.label).not.toContain('✓')
  })
})

describe('linkLine', () => {
  it('says who investigated a failure and where it passed after', () => {
    const e = entry({
      links: [
        { rel: 'handed', dir: 'out', ref: 'attempt:todo-3f2a91c0/build/2', label: 'build #2', class: 'record' },
        { rel: 'passed_after', dir: 'out', ref: 'check:todo-3f2a91c0/build/2/unit/1', label: 'unit on build #2', class: 'checked', outcome: 'passed' },
      ],
    })
    expect(linkLine(e)).toBe('Investigated by build #2 · passed on build #2')
  })

  it('says what an attempt was handed, a reopening and a resume, and nothing with no link it knows', () => {
    expect(linkLine({ links: [{ rel: 'handed', dir: 'in', ref: 'check:r/build/1/unit/1', label: 'unit on build #1' }] })).toBe('Handed unit on build #1')
    expect(linkLine({ links: [{ rel: 'reopened', dir: 'out', ref: 'attempt:r/build/3', label: 'build #3' }] })).toBe('Reopened as build #3')
    expect(linkLine({ links: [{ rel: 'resumed_from', dir: 'out', ref: 'session:aa', label: 'session aa' }] })).toBe('Resumed from session aa')
    expect(linkLine({ links: [{ rel: 'cites', dir: 'out', ref: 'run:r', label: 'r' }] })).toBe('')
    expect(linkLine({ links: [] })).toBe('')
  })
})

describe('repeatWords', () => {
  it('says a sentence said in several places, and stays silent for one', () => {
    expect(repeatWords({ class: 'said', repeats: { count: 3, anchors: 3 } })).toBe('said in 3 places')
    expect(repeatWords({ class: 'said', repeats: { count: 2, anchors: 1 } })).toBe('said 2 times')
    expect(repeatWords({ class: 'said', repeats: { count: 1, anchors: 1 } })).toBe('')
    expect(repeatWords({ class: 'checked', repeats: { count: 4, anchors: 2 } })).toBe('in 2 places')
  })

  // Codex's round 3 on #731: Conversation's anchors are sessions, tickets' comments, runs' messages and briefings' items, and `repeats`
  // counts them without saying which. The same comment on three tickets, or said once in a session and twice on tickets, is in places.
  it('never calls the places sessions: not for tickets\' comments, nor for a mix', () => {
    const rows = [
      entry({ class: 'said', kind: 'ticket.comment', anchor: { ref: 'ticket:CND-1', label: 'CND-1 comment #2' }, repeats: { count: 3, anchors: 3 } }),
      entry({ class: 'said', kind: 'session.typed', anchor: { ref: 'session:0123456789abcdef', label: 'fix-tests' }, repeats: { count: 3, anchors: 3 } }),
    ]
    for (const r of rows) expect(repeatWords(r)).toBe('said in 3 places')
    expect(rows.map(repeatWords).join(' ')).not.toMatch(/session/)
  })
})

describe('historyQuestions', () => {
  it('asks the four questions as ready queries', () => {
    expect(historyQuestions({ project: 'CND', host: 'lab-box' }).map((q) => [q.id, q.query])).toEqual([
      ['failures', 'class:checked outcome:failed'],
      ['rejected', 'kind:plan.withdrawn,plan.superseded,review.verdict outcome:withdrawn,superseded,needs_changes'],
      ['verified', 'class:checked outcome:passed project:CND'],
      ['sessions', 'host:lab-box kind:session'],
    ])
  })

  it('leaves a filter out with no value, or one that is no single token', () => {
    const q = historyQuestions({ host: 'my box' })
    expect(q.find((x) => x.id === 'verified')?.query).toBe('class:checked outcome:passed')
    expect(q.find((x) => x.id === 'sessions')?.query).toBe('kind:session')
    expect(historyQuestions().find((x) => x.id === 'sessions')?.query).toBe('kind:session')
  })
})

describe('availabilityWords', () => {
  it("says the server's words, else its state's", () => {
    expect(availabilityWords({ state: 'log', words: 'log kept (14,822 bytes)' })).toBe('log kept (14,822 bytes)')
    expect(availabilityWords({ state: 'listed', words: '' })).toBe('ended, listed')
    expect(availabilityWords({ state: 'record', words: '' })).toBe('record only')
    expect(availabilityWords({ state: 'host_away', words: '' })).toBe('host away')
    expect(availabilityWords({ state: 'worktree_gone', words: '' })).toBe('worktree removed')
    expect(availabilityWords({ state: 'something_new', words: '' })).toBe('something_new')
    expect(availabilityWords(undefined)).toBe('')
  })
})

function record(p: Partial<SessionRecord> = {}): SessionRecord {
  return {
    v: 1,
    id: '0123456789abcdef',
    seq: 4,
    name: 'resume me',
    kind: 'server',
    agent: { id: 'claude', name: 'Claude Code', version: '2.1.318', session: { id: 'u-1', resumable: true } },
    host: { name: 'lab-box' },
    where: 'native',
    cwd: '/home/nate/code/todo',
    startedAt: '2026-11-05T14:00:00Z',
    entries: [],
    ...p,
  }
}

describe('a record in words', () => {
  it('says how it ended, a lost one as not known', () => {
    expect(recordEnding(record({ endedAt: '2026-11-05T15:00:00Z', ending: 'exited (exit 0)' }))).toBe('exited (exit 0)')
    expect(recordEnding(record({ lostAt: '2026-11-05T15:00:00Z', ending: 'lost' }))).toBe('not observed since 2026-11-05T15:00:00Z; whether its process ended is not known')
    expect(recordEnding(record(), true)).toBe('running')
  })

  it('says an answer as the record keeps it: a typed one and keys in the terminal never with their words', () => {
    expect(recordEntryWords({ seq: 1, at: '', kind: 'answer', outcome: 'answered', message: 'Yes', byName: 'ana' })).toMatchObject({ label: 'Answered by ana', text: 'Yes' })
    expect(recordEntryWords({ seq: 1, at: '', kind: 'answer', outcome: 'answered' }).text).toBe('a line typed (its words are never kept)')
    expect(recordEntryWords({ seq: 1, at: '', kind: 'answer', outcome: 'answered_in_terminal' }).label).toBe('Answered in the terminal')
    expect(recordEntryWords({ seq: 1, at: '', kind: 'answer', outcome: 'withdrawn' }).label).toBe('Withdrawn')
    expect(recordEntryWords({ seq: 1, at: '', kind: 'report', event: 'progress', message: 'tests pass' })).toMatchObject({ label: 'Progress (reported by its agent)', text: 'tests pass' })
  })

  it("says a resume's receipt", () => {
    expect(resumeReceiptWords({ commandId: 'c', state: 'started', session: 'fedcba9876543210', at: '' })).toBe('started session fedcba9876543210')
    expect(resumeReceiptWords({ commandId: 'c', state: 'failed', code: 'env_not_kept', error: 'its launch had FOO', at: '' })).toBe('refused (env_not_kept): its launch had FOO')
    expect(resumeReceiptWords({ commandId: 'c', state: 'uncertain', at: '' })).toContain('may have started')
  })

  it("names the launch's variables and resumes without them; a lost record asks a second time", () => {
    const q = resumeQuestion(record({ launch: { argCount: 0, argsKept: true, envNames: ['API_URL', 'TOKEN'], envCount: 2 } }))
    expect([q.env, q.withoutEnv, q.lost]).toEqual([['API_URL', 'TOKEN'], true, false])
    expect(q.detail).toContain('API_URL, TOKEN')
    expect(resumeQuestion(record({ launch: { argCount: 0, argsKept: true } })).withoutEnv).toBe(false)
    // The route resumes a still-listed session with its own launch, its variables among it: the words say so and no withoutEnv is sent.
    const vars = { launch: { argCount: 0, argsKept: true, envNames: ['API_URL', 'TOKEN'], envCount: 2 } }
    const listed = resumeQuestion(record(vars), true)
    expect(listed.withoutEnv).toBe(false)
    expect(listed.detail).toContain('its session is still listed, so it resumes with its own launch, its variables among it')
    expect(listed.detail).not.toContain('without them')
    // A record alone resumes without them, said plainly; while the page does not know which, both are said.
    const alone = resumeQuestion(record(vars), false)
    expect([alone.withoutEnv, alone.detail.includes('so it resumes without them.'), alone.detail.includes('still listed')]).toEqual([true, true, false])
    const unsure = resumeQuestion(record(vars))
    expect([unsure.withoutEnv, unsure.detail.includes('unless its session is still listed')]).toEqual([true, true])
    expect(resumeQuestion(record({ lostAt: '2026-11-05T15:00:00Z' })).lost).toBe(true)
    expect(lostQuestion(record({ lostAt: '2026-11-05T15:00:00Z' })).detail).toContain('whether its process ended is not known')
  })
})

describe('resumeDestination', () => {
  it("opens the new session while it is listed, and its record when only the receipt names it", () => {
    const receipt = { commandId: 'c1', state: 'started' as const, session: 'fedcba9876543210', at: '' }
    expect(resumeDestination({ session: { id: 'fedcba9876543210' }, receipt })).toEqual({ to: '/sessions/fedcba9876543210', listed: true })
    // A command id sent again is answered from its receipt; the session is in the reply only while the registry lists it.
    expect(resumeDestination({ receipt })).toEqual({ to: '/history/sessions/fedcba9876543210', listed: false })
    expect(resumeDestination({ receipt: { ...receipt, session: undefined } })).toBeNull()
  })
})

// ---- PR-7: the search's words, where a row opens, its grouping, and the filters the History page's chips write.

describe("a row's destination (PR-7)", () => {
  it('opens the page of its evidence, never an API route: the anchor when its own is one', () => {
    expect(historyTarget(entry())).toBe('/runs/todo-3f2a91c0?view=tasks&task=build&attempt=1&step=check/unit/1')
    const anchor = { ref: 'task:todo-3f2a91c0/build', label: 'build · todo-3f2a91c0', url: '/runs/todo-3f2a91c0?view=tasks&task=build' }
    expect(historyTarget(entry({ url: '/api/runs/todo-3f2a91c0/tasks/build/attempts/1/steps/unit/1/log', anchor }))).toBe(anchor.url)
    expect(historyTarget(entry({ url: '/api?x', anchor: { ...anchor, url: '' } }))).toBeNull()
    expect(historyTarget(entry({ url: 'https://elsewhere.example/x', anchor: { ...anchor, url: '//elsewhere.example' } }))).toBeNull()
  })

  it("names the session a row belongs to: its anchor, else a link's end, with its page or its record's", () => {
    expect(historySession(entry())).toBeNull()
    expect(historySession(entry({ anchor: { ref: 'session:0123456789abcdef', label: 'session 0123456789abcdef', url: '/sessions/0123456789abcdef' } }))).toEqual({ id: '0123456789abcdef', url: '/sessions/0123456789abcdef', listed: true })
    expect(historySession(entry({ links: [{ rel: 'session_of', dir: 'out', ref: 'session:fedcba9876543210', label: 'session fedcba9876543210' }] }))).toEqual({ id: 'fedcba9876543210', url: '/history/sessions/fedcba9876543210', listed: false })
    expect(isReceipt(entry())).toBe(true)
    expect(isReceipt(entry({ kind: 'session.chat' }))).toBe(false)
  })

  it('draws a session, a receipt passed or failed, a decision and words each with its own icon', () => {
    expect(historyIcon(entry())).toBe('i-lucide-circle-x')
    expect(historyIcon(entry({ outcome: 'passed' }))).toBe('i-lucide-circle-check')
    expect(historyIcon(entry({ kind: 'session', class: 'record' }))).toBe('i-lucide-terminal')
    expect(historyIcon(entry({ kind: 'plan.withdrawn', class: 'decided' }))).toBe('i-lucide-gavel')
    expect(historyIcon(entry({ kind: 'session.chat', class: 'said', outcome: 'passed' }))).toBe('i-lucide-message-circle')
  })
})

describe('the answer grouped under its anchors (PR-7)', () => {
  it('keeps each group where its first row ranked, the rows in their order, a key seen twice once', () => {
    const t = { ref: 'task:r/build', label: 'build · r' }
    const s = { ref: 'session:0123456789abcdef', label: 'session 0123456789abcdef' }
    const a = entry({ key: 'a', anchor: t })
    const b = entry({ key: 'b', anchor: s })
    const c = entry({ key: 'c', anchor: t })
    const groups = byAnchor([a, b, c, a])
    expect(groups.map((g) => [g.anchor.ref, g.entries.map((e) => e.key)])).toEqual([
      ['task:r/build', ['a', 'c']],
      ['session:0123456789abcdef', ['b']],
    ])
    // An entry with no anchor is its own group, under its title.
    expect(byAnchor([entry({ key: 'x', title: 'x on y #1', anchor: { ref: '', label: '' } })]).map((g) => g.anchor)).toEqual([{ ref: 'x', label: 'x on y #1' }])
  })
})

describe("the index's state in words (PR-7)", () => {
  it('says nothing when it is ready, and how many sources are behind otherwise', () => {
    expect(indexingWords({ state: 'ready' })).toBe('')
    expect(indexingWords(null)).toBe('')
    expect(indexingWords({ state: 'ready', behind: 3 })).toBe('Indexing: 3 sources behind')
    expect(indexingWords({ state: 'catching_up', behind: 1 })).toBe('Indexing: 1 source behind')
    expect(indexingWords({ state: 'catching_up' })).toBe('Indexing: catching up')
    expect(indexingWords({ state: 'read_only' })).toBe('Read only: another server keeps this history')
  })
})

describe("the filters the chips write (PR-7)", () => {
  it('writes one value per filter at the end, keeps every other word in its order, and takes it out when cleared', () => {
    expect(withFilter('stub-fixed', 'class', 'checked')).toBe('stub-fixed class:checked')
    expect(withFilter('class:said stub-fixed outcome:failed', 'class', 'checked')).toBe('stub-fixed outcome:failed class:checked')
    expect(withFilter('stub-fixed class:checked', 'class', '')).toBe('stub-fixed')
    expect(withFilter('"a phrase:with colon" x', 'outcome', 'passed')).toBe('"a phrase:with colon" x outcome:passed')
    expect(withFilter('x', 'host', 'two words')).toBe('x')
    expect(filterOf('stub-fixed CLASS:said class:checked', 'class')).toBe('checked')
    expect(filterOf('stub-fixed', 'since')).toBe('')
  })

  it("finds this machine's name in the records of its own sessions, never a hosted one's", () => {
    expect(machineOf([entry({ host: { name: 'mac', user: 'nate', hosted: true } }), entry({ host: { name: 'lab-box' } })])).toBe('lab-box')
    expect(machineOf([entry({ host: {} })])).toBe('')
  })
})
