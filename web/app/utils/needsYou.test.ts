import { describe, expect, it } from 'vitest'
import type { SessionInfo, SessionStatus } from '~/composables/useSessions'
import type { Decision, DecisionState } from './decisions'
import type { InboxSummary } from './inbox'
import { needsCount, needsRowShown, needsYou, pillWords, sectionHeader, unanswerable, type NeedsInput } from './needsYou'

// The shape of the UX review's day and its edges: six sessions that ask (one a host away, one mirroring an open record), one that
// is starting, one ended that still shows a question; a gate no question mirrors, a stopped run's open record, a record answered and
// not yet applied; two plans and a failed task; and the inbox's own summaries of the questions and decisions beside them.

const T = (m: number) => new Date(Date.UTC(2026, 10, 16, 9, m)).toISOString()

function session(id: string, o: { status?: SessionStatus; asks?: boolean; since?: number; decision?: string; run?: string } = {}): SessionInfo {
  return {
    id,
    name: id,
    kind: 'server',
    agentId: 'stub',
    command: [],
    cwd: '/w',
    status: o.status ?? 'running',
    cols: 80,
    rows: 24,
    viewers: 0,
    ...(o.run ? { crew: { runId: o.run, crewId: 'c', member: id } } : {}),
    ...(o.asks ? { attention: { state: 'needs_input', id: `q-${id}`, since: T(o.since ?? 0), message: 'May I?', ...(o.decision ? { decision: o.decision } : {}) } } : {}),
  } as unknown as SessionInfo
}

function record(id: string, run: string, at: number, state: DecisionState = 'open'): Decision {
  return { id, run, openedIn: run, origin: 'plan', type: 'approval', gate: 'g', request: {} as Decision['request'], policy: {}, state, rev: 1, createdAt: T(at) } as Decision
}

function summary(ref: string, kind: string, at: number, o: Partial<InboxSummary> = {}): InboxSummary {
  return { ref, kind, since: T(at), title: ref, brief: '', question: '', answerer: '', actions: [], ...o }
}

const SESSIONS = [
  session('ask-1', { asks: true, since: 10 }),
  session('away', { asks: true, since: 11, status: 'host_disconnected' }),
  session('mirror', { asks: true, since: 12, decision: 'dr-1' }),
  session('ask-4', { asks: true, since: 13 }),
  session('ask-5', { asks: true, since: 14 }),
  session('ask-6', { asks: true, since: 15 }),
  session('starting', { status: 'starting' }),
  session('ended', { asks: true, since: 1, status: 'exited' }),
]
const RECORDS = [record('dr-1', 'r-mirror', 5), record('dr-2', 'r-gate', 0), record('dr-3', 'r-stopped', 2), record('dr-4', 'r-gate', 3, 'answered')]
const RUNS: Record<string, { stoppedAt?: string }> = { 'r-mirror': {}, 'r-gate': {}, 'r-task': {}, 'r-stopped': { stoppedAt: T(4) } }
const INBOX = [
  summary('plan:CND-1/1', 'plan', 20),
  summary('plan:CND-2/1', 'plan', 21),
  summary('task:r-task/t-1', 'task', 1),
  // The inbox's own questions and decisions: never counted (the stores the app holds say them first).
  summary('question:ask-1/q-ask-1', 'question', 10),
  summary('decision:dr-2', 'decision', 0),
  summary('decision:dr-4', 'decision', 3, { tracking: true }),
]

function day(over: { sessions?: Partial<NeedsInput['sessions']>; decisions?: Partial<NeedsInput['decisions']>; inbox?: Partial<NeedsInput['inbox']> } = {}): NeedsInput {
  return {
    sessions: { list: SESSIONS, loaded: true, error: '', ...over.sessions },
    decisions: { records: RECORDS, loaded: true, error: '', cut: false, unavailable: '', ...over.decisions },
    inbox: { items: INBOX, loaded: true, error: '', next: '', unavailable: '', ...over.inbox },
    runOf: (id) => RUNS[id],
  }
}

describe('needsYou: the one list of what needs you', () => {
  it("lists the day's items once each, oldest first", () => {
    const n = needsYou(day())
    expect(n.items.map((it) => `${it.kind} ${it.ref}`)).toEqual([
      'decision decision:dr-2',
      'task task:r-task/t-1',
      'decision decision:dr-3',
      'question question:ask-1/q-ask-1',
      'question question:away/q-away',
      'question question:mirror/q-mirror',
      'question question:ask-4/q-ask-4',
      'question question:ask-5/q-ask-5',
      'question question:ask-6/q-ask-6',
      'plan plan:CND-1/1',
      'plan plan:CND-2/1',
    ])
    // Six questions (the host away among them, the mirrored record its question's item), the gate, the stopped run's record, two
    // plans and the task: eleven, every store read and whole.
    expect([n.number, n.known, n.more]).toEqual([11, true, false])
    expect(n.answerable.map((it) => it.ref)).toEqual(n.items.filter((it) => it.kind === 'question' || it.kind === 'decision').map((it) => it.ref))
    expect(n.answerable).toHaveLength(8)
    // The runs that wait: the gate's, the failed task's, the stopped run's, and the run of the record a question mirrors.
    expect(n.runs).toEqual(['r-gate', 'r-task', 'r-stopped', 'r-mirror'])
    // What a lister needs of each: the question's session and the record it mirrors, whether a record's run still runs, a task's summary.
    const by = new Map(n.items.map((it) => [it.ref, it]))
    expect(by.get('question:mirror/q-mirror')!.decision?.id).toBe('dr-1')
    expect(by.get('question:mirror/q-mirror')!.session?.id).toBe('mirror')
    expect([by.get('decision:dr-2')!.stopped, by.get('decision:dr-3')!.stopped]).toEqual([false, true])
    expect(by.get('task:r-task/t-1')!.summary?.title).toBe('task:r-task/t-1')
    expect(by.get('decision:dr-3')!.since).toBe(T(2))
  })

  it('counts a new question the moment its session asks, and not again when the inbox reads it', () => {
    const asks = [...SESSIONS, session('new', { asks: true, since: 30 })]
    const before = needsYou(day({ sessions: { list: asks } }))
    expect([before.number, before.known]).toEqual([12, true])
    const read = needsYou(day({ sessions: { list: asks }, inbox: { items: [...INBOX, summary('question:new/q-new', 'question', 30)] } }))
    expect(read.number).toBe(12)
    expect(read.items.map((it) => it.ref)).toEqual(before.items.map((it) => it.ref))
  })

  it('counts a record no question mirrors once its question goes, and an answered record not at all', () => {
    const gone = SESSIONS.map((s) => (s.id === 'mirror' ? session('mirror') : s))
    const n = needsYou(day({ sessions: { list: gone } }))
    expect(n.items.filter((it) => it.kind === 'decision').map((it) => it.ref)).toEqual(['decision:dr-2', 'decision:dr-3', 'decision:dr-1'])
    expect(n.number).toBe(11)
    expect(n.items.some((it) => it.ref === 'decision:dr-4')).toBe(false)
  })

  const held: Array<[string, Parameters<typeof day>[0], boolean]> = [
    ['the sessions unread', { sessions: { loaded: false } }, false],
    ['the sessions failed', { sessions: { error: 'events 502' } }, false],
    ['the decisions unread', { decisions: { loaded: false } }, false],
    ['the decisions failed', { decisions: { error: 'decisions 500' } }, false],
    ['the decisions cut', { decisions: { cut: true } }, true],
    ['the inbox unread', { inbox: { loaded: false } }, false],
    ['the inbox failed', { inbox: { error: 'inbox 500' } }, false],
    ['the inbox past its first page', { inbox: { next: 'cursor' } }, true],
  ]
  for (const [what, over, cut] of held) {
    it(`does not know the number with ${what}, the other stores read`, () => {
      const n = needsYou(day(over))
      expect(n.known).toBe(false)
      expect(n.more).toBe(cut)
      // A store cut short gives a floor of what was read; one unread or failed gives no number a reader may draw.
      if (cut) expect(n.number).toBe(11)
    })
  }

  it('is unknown, never a known zero, while the decisions are first read and the inbox lists an open gate', () => {
    const n = needsYou({
      sessions: { list: [], loaded: true, error: '' },
      decisions: { records: [], loaded: false, error: '', cut: false },
      inbox: { items: [summary('decision:dr-9', 'decision', 0)], loaded: true, error: '', next: '', unavailable: '' },
    })
    expect([n.number, n.known, n.more]).toEqual([0, false, false])
  })

  it('counts the asking sessions on a switchyard, where neither the inbox nor the decisions are served', () => {
    const n = needsYou(
      day({
        decisions: { records: [], loaded: true, unavailable: 'this server coordinates hosted sessions and launches none' },
        // What the inbox held before it was refused is not counted.
        inbox: { items: INBOX, next: 'cursor', unavailable: 'this server coordinates hosted sessions and launches none' },
      }),
    )
    expect([n.number, n.known, n.more]).toEqual([6, true, false])
    expect(n.items.every((it) => it.kind === 'question')).toBe(true)
  })

  it('knows a zero: every store read, nothing waits', () => {
    const n = needsYou({ sessions: { list: [session('idle')], loaded: true, error: '' }, decisions: { records: [], loaded: true, error: '', cut: false }, inbox: { items: [], loaded: true, error: '', next: '', unavailable: '' } })
    expect([n.number, n.known, n.more, n.runs]).toEqual([0, true, false, []])
  })
})

describe('needsCount: the one number as a reader draws it', () => {
  it('draws the count, a floor with "+", and nothing while unknown', () => {
    expect([needsCount({ number: 9, known: true, more: false }), needsCount({ number: 9, known: false, more: true }), needsCount({ number: 9, known: false, more: false }), needsCount({ number: 0, known: true, more: false })]).toEqual(['9', '9+', '', '0'])
  })
})

describe("needsRowShown: Needs you's row in the nav", () => {
  const NOW = Date.UTC(2026, 10, 16, 12)
  const H = 60 * 60 * 1000
  const quiet = { unavailable: '', tracking: 0, closed: [] as Array<{ closed?: string; since: string }>, recent: [] as Array<{ at: number }> }
  const zero = needsYou(day({ sessions: { list: [] }, decisions: { records: [] }, inbox: { items: [] } }))
  const cases: Array<[string, Parameters<typeof needsRowShown>[0], Parameters<typeof day>[0] | 'zero', string, boolean]> = [
    ['nothing waits, nothing decided', quiet, 'zero', '/yard', false],
    ['an item waits', quiet, {}, '/yard', true],
    ['the sessions unread', quiet, { sessions: { list: [], loaded: false }, decisions: { records: [] }, inbox: { items: [] } }, '/yard', true],
    ['the decisions failed', quiet, { sessions: { list: [] }, decisions: { records: [], error: 'decisions 500' }, inbox: { items: [] } }, '/yard', true],
    ['the decisions cut short', quiet, { sessions: { list: [] }, decisions: { records: [], cut: true }, inbox: { items: [] } }, '/yard', true],
    ['the inbox unread', quiet, { sessions: { list: [] }, decisions: { records: [] }, inbox: { items: [], loaded: false } }, '/yard', true],
    ['the inbox failed', quiet, { sessions: { list: [] }, decisions: { records: [] }, inbox: { items: [], error: 'inbox 500' } }, '/yard', true],
    ['the inbox cut short', quiet, { sessions: { list: [] }, decisions: { records: [] }, inbox: { items: [], next: 'c' } }, '/yard', true],
    ['zero items, one record answered and not yet applied', { ...quiet, tracking: 1 }, 'zero', '/yard', true],
    ['closed 23 hours ago', { ...quiet, closed: [{ since: T(0), closed: new Date(NOW - 23 * H).toISOString() }] }, 'zero', '/yard', true],
    ['closed 25 hours ago', { ...quiet, closed: [{ since: T(0), closed: new Date(NOW - 25 * H).toISOString() }] }, 'zero', '/yard', false],
    ["this browser's answer 23 hours ago", { ...quiet, recent: [{ at: NOW - 23 * H }] }, 'zero', '/yard', true],
    ["this browser's answer 25 hours ago", { ...quiet, recent: [{ at: NOW - 25 * H }] }, 'zero', '/yard', false],
    ['its page open at zero', quiet, 'zero', '/decisions', true],
    ['a switchyard, its stores unread', { ...quiet, unavailable: 'switchyard' }, { sessions: { list: [], loaded: false } }, '/yard', false],
    ['a switchyard with a question waiting', { ...quiet, unavailable: 'switchyard' }, {}, '/yard', false],
  ]
  for (const [what, state, over, path, want] of cases) {
    it(`${want ? 'shows' : 'leaves'}: ${what}`, () => {
      expect(needsRowShown(state, over === 'zero' ? zero : needsYou(day(over)), path, NOW)).toBe(want)
    })
  }
})

describe("sectionHeader: the sidebar section's header says what it leaves out", () => {
  it('counts the plans and tasks of the one list, never a difference of two stores', () => {
    const items = needsYou(day({ decisions: { records: RECORDS.filter((d) => d.id !== 'dr-3') } })).items
    // The section lists six questions and the running gate; the list holds two plans and a task besides.
    expect(sectionHeader(7, items)).toEqual({ listed: 7, more: 3, words: 'Needs you 7 · +3 on the page' })
    // With an open record of a stopped run in the list as well: one more.
    expect(sectionHeader(7, needsYou(day()).items).words).toBe('Needs you 7 · +4 on the page')
    // Questions and decisions of running runs alone: nothing left out.
    const listed = needsYou(day({ decisions: { records: RECORDS.filter((d) => d.id !== 'dr-3') }, inbox: { items: [] } })).items
    expect(sectionHeader(7, listed)).toEqual({ listed: 7, more: 0, words: 'Needs you 7' })
  })

  it("gives the pill's count and what it cannot answer", () => {
    const items = needsYou(day()).items
    expect(unanswerable(items)).toBe(3)
    expect([pillWords(2, 8, 3), pillWords(1, 2, 0)]).toEqual(['2 of 8 · 3 more in Needs you', '1 of 2'])
  })
})
