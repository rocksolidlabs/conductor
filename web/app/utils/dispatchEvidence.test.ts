import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { CandidateReceipt, DispatchChange, RunCandidate, RunSummary, TaskCandidates, TicketRunEntry } from '../composables/useDispatch'
import type { RunAttempt, RunInfo, SessionInfo } from '../composables/useSessions'
import {
  type TaskDetail,
  candidateWords,
  changedSinceWords,
  changesSince,
  changesSinceNote,
  durationWords,
  entryKey,
  entryRunId,
  findingsWords,
  gather,
  launchEntryWords,
  logMatches,
  mergeEntries,
  pickReceipt,
  pickedReceipt,
  receiptChips,
  receiptDuration,
  receiptState,
  receiptVersion,
  repairReads,
  repairs,
  runBlockers,
  runQuestions,
  runsChanged,
  runsSignature,
  stepOf,
  summaryBadges,
  summaryState,
} from './dispatchEvidence'

// A ticket's runs and their evidence in words (Program item DS2, docs/plans/dispatch-evidence.md, "web/app", PR-11).

function receipt(p: Partial<CandidateReceipt> = {}): CandidateReceipt {
  return { phase: 'check', id: 'fixed', k: 1, where: 'native', state: 'finished', exit: 0, timedOut: false, killed: false, stable: true, startedAt: '2026-11-01T10:00:00Z', finishedAt: '2026-11-01T10:00:02Z', logSha256: 'ab'.repeat(32), logBytes: 12, logDropped: false, ...p }
}
function candidate(p: Partial<RunCandidate> = {}): RunCandidate {
  return { task: 'build', kind: 'work', attempt: 1, state: 'succeeded', standing: false, withdrawn: false, receipts: [], ...p }
}
function summary(p: Partial<RunSummary> = {}): RunSummary {
  return { id: 'crew-0000aaaa', label: '', state: 'running', paused: false, startedAt: '2026-11-01T10:00:00Z', record: false, members: [], tasks: {}, questions: 0, blockers: [], ...p }
}
function session(id: string, p: Partial<SessionInfo> = {}): SessionInfo {
  return { id, status: 'running', ...p } as SessionInfo
}

describe("a ticket's run entries", () => {
  const launch = { id: 'l1', runId: 'crew-0000aaaa', state: 'started' } as TicketRunEntry['launch']
  it('are keyed by their kind and what they are of, and name the run each shows', () => {
    expect(entryKey({ kind: 'launch', launch })).toBe('launch:l1')
    expect(entryKey({ kind: 'link', link: { id: 'k1', ref: 'crew-0000bbbb' } as TicketRunEntry['link'] })).toBe('link:k1')
    expect(entryKey({ kind: 'run', run: summary({ id: 'crew-0000cccc' }) })).toBe('run:crew-0000cccc')
    expect(entryRunId({ kind: 'launch', launch })).toBe('crew-0000aaaa')
    expect(entryRunId({ kind: 'launch', launch: { ...launch!, state: 'pending' } })).toBe('')
    expect(entryRunId({ kind: 'link', link: { id: 'k1', ref: 'crew-0000bbbb' } as TicketRunEntry['link'] })).toBe('crew-0000bbbb')
  })

  it("know a chain's runs by their own ids, though every continuation carries the launch or the link that holds the chain", () => {
    const link = { id: 'k1', ref: 'crew-0000bbbb' } as TicketRunEntry['link']
    const c1: TicketRunEntry = { kind: 'run', launch, run: summary({ id: 'crew-0000c001' }), continues: 'crew-0000aaaa' }
    const c2: TicketRunEntry = { kind: 'run', launch, run: summary({ id: 'crew-0000c002' }), continues: 'crew-0000c001' }
    const c3: TicketRunEntry = { kind: 'run', link, run: summary({ id: 'crew-0000c003' }), continues: 'crew-0000bbbb' }
    expect([c1, c2, c3].map(entryKey)).toEqual(['run:crew-0000c001', 'run:crew-0000c002', 'run:crew-0000c003'])
    expect([c1, c2, c3].map(entryRunId)).toEqual(['crew-0000c001', 'crew-0000c002', 'crew-0000c003'])
    // Every continuation shown, on one page and across Show older's pages; the launch and the link themselves once each.
    const all = mergeEntries([{ entries: [c2, c1, { kind: 'launch', launch }] }, { entries: [{ kind: 'launch', launch }, c3, { kind: 'link', link }] }])
    expect(all.map(entryKey)).toEqual(['run:crew-0000c002', 'run:crew-0000c001', 'launch:l1', 'run:crew-0000c003', 'link:k1'])
    // A chain run whose summary did not answer is named by nothing: it is kept, never taken for another, and shows no run.
    const unread: TicketRunEntry = { kind: 'run', launch, continues: 'crew-0000c002' }
    expect(entryKey(unread)).toBe('')
    expect(entryRunId(unread)).toBe('')
    expect(mergeEntries([{ entries: [unread, unread, c1] }])).toHaveLength(3)
  })

  it('merge page after page, each once, in their order', () => {
    const a: TicketRunEntry = { kind: 'launch', launch }
    const b: TicketRunEntry = { kind: 'run', run: summary({ id: 'crew-0000dddd' }) }
    expect(mergeEntries([{ entries: [a, b] }, { entries: [b, { kind: 'run', run: summary({ id: 'crew-0000eeee' }) }] }]).map(entryKey)).toEqual(['launch:l1', 'run:crew-0000dddd', 'run:crew-0000eeee'])
  })

  it("say a launch's state: failed with why, pending, a run no one can read", () => {
    expect(launchEntryWords({ launch: { ...launch!, state: 'failed', why: 'the agent worker is not installed' } })).toBe('The launch failed: the agent worker is not installed')
    expect(launchEntryWords({ launch: { ...launch!, state: 'pending' } })).toMatch(/^Recorded/)
    expect(launchEntryWords({ launch, run: summary() })).toBe('')
    expect(launchEntryWords({ launch })).toMatch(/known to no engine/)
  })
})

describe("a run summary", () => {
  it('counts its tasks as the run page does, and says its state', () => {
    expect(summaryBadges(summary({ tasks: { succeeded: 2, running: 1, failed: 1 }, paused: true })).map((b) => b.label)).toEqual(['2/4 tasks', '1 failed', '1 running', 'paused'])
    expect(summaryBadges(summary({ tasks: { succeeded: 2 }, outcome: 'succeeded' }))[0]).toEqual({ label: '2/2 tasks', color: 'success' })
    expect(summaryState(summary(), 0).label).toBe('Running')
    expect(summaryState(summary(), 2).label).toBe('Needs input 2')
    expect(summaryState(summary({ outcome: 'failed' }), 0).label).toBe('Failed')
    expect(summaryState(summary({ stoppedAt: '2026-11-01T11:00:00Z' }), 1).label).toBe('Stopped')
  })

  it("counts its questions from the live sessions, by the session ids it names, with no run event", () => {
    const s = summary({ members: [{ name: 'builder', sessionId: 's1', status: 'running' }, { name: 'asker', sessionId: 's2', status: 'running' }], questions: 0 })
    const quiet = [session('s1'), session('s2', { attention: { state: 'working' } })]
    expect(runQuestions(s, undefined, quiet)).toEqual({ count: 0, waiting: [] })
    // The asker's session comes to wait on a person: the session changed, the run did not, and the count follows.
    const asking = [session('s1'), session('s2', { attention: { state: 'needs_input', message: 'Which way?' } })]
    expect(runQuestions(s, undefined, asking)).toEqual({ count: 1, waiting: [{ member: 'asker', sessionId: 's2', message: 'Which way?' }] })
    // Answered: it leaves them.
    expect(runQuestions(s, undefined, quiet).count).toBe(0)
    // An ended member, or an ended session, waits on no one; a question answered at the terminal is no question.
    expect(runQuestions({ ...s, members: [{ name: 'asker', sessionId: 's2', status: 'ended' }] }, undefined, asking).count).toBe(0)
    expect(runQuestions(s, undefined, [session('s2', { status: 'exited', attention: { state: 'needs_input' } })]).count).toBe(0)
    expect(runQuestions(s, undefined, [session('s2', { attention: { state: 'needs_input', answered: 'terminal' } })]).count).toBe(0)
  })

  it("takes a live run's own members, its latest session found by the run and the member when none is named", () => {
    const s = summary({ members: [{ name: 'asker', status: 'pending' }] })
    const live = { id: 'crew-0000aaaa', members: [{ name: 'asker', status: 'running' }] } as unknown as RunInfo
    const sessions = [session('s9', { crew: { runId: 'crew-0000aaaa', member: 'asker' }, attention: { state: 'needs_input', message: 'Ok?' } } as Partial<SessionInfo>)]
    expect(runQuestions(s, live, sessions).waiting).toEqual([{ member: 'asker', sessionId: 's9', message: 'Ok?' }])
  })

  it("takes a record's own count only when no live session can be joined", () => {
    expect(runQuestions(summary({ record: true, questions: 2 }), undefined, [])).toEqual({ count: 2, waiting: [] })
  })

  it('is read again when a run it shows changes in the live store, or a member session of it changes status, never for a question', () => {
    const runs = ['r1']
    let live: RunInfo | undefined = { id: 'r1', managed: { revision: 3 }, state: 'running', members: [{ name: 'a', sessionId: 's1', status: 'running' }] } as unknown as RunInfo
    const runOf = () => live
    const base = runsSignature(runs, runOf, [session('s1')])
    expect(runsChanged(base, runsSignature(runs, runOf, [session('s1', { attention: { state: 'needs_input' } })]))).toBe(false)
    expect(runsChanged(base, runsSignature(runs, runOf, [session('s1', { status: 'exited' })]))).toBe(true)
    live = { ...live!, managed: { revision: 4 } }
    expect(runsChanged(base, runsSignature(runs, runOf, [session('s1')]))).toBe(true)
    live = undefined
    expect(runsChanged(base, runsSignature(runs, runOf, [session('s1')]))).toBe(true)
    // A run that only came with a read of the pages is no change: the read that brought it is its summary.
    expect(runsChanged(base, { ...base, r2: 'null' })).toBe(false)
    expect(runsChanged({ ...base, r2: 'null' }, base)).toBe(false)
  })

  it('is read again when a live run comes to continue a run it shows, a record the live store never holds among them', () => {
    // A record resumed from another page: the record reads the same (the live store never holds it), the new live run names it.
    const runs = ['crew-0000aaaa']
    const runOf = () => undefined
    const others = [{ id: 'crew-0000ffff' }]
    const base = runsSignature(runs, runOf, [], others)
    expect(runsChanged(base, runsSignature(runs, runOf, [], others))).toBe(false)
    const resumed = [...others, { id: 'crew-0000c001', resumedFrom: 'crew-0000aaaa' }]
    const now = runsSignature(runs, runOf, [], resumed)
    expect(runsChanged(base, now)).toBe(true)
    // Once read, the continuation is shown too, and the source's reading stands until another one comes.
    const after = runsSignature([...runs, 'crew-0000c001'], (id) => (id === 'crew-0000c001' ? ({ id, state: 'running', members: [] } as unknown as RunInfo) : undefined), [], resumed)
    expect(runsChanged(now, after)).toBe(false)
    // A live run resumed: the run that resumed it is noted on it.
    let live = { id: 'r1', state: 'running', members: [] } as unknown as RunInfo
    const first = runsSignature(['r1'], () => live, [])
    live = { ...live, resumedBy: 'r2' }
    expect(runsChanged(first, runsSignature(['r1'], () => live, []))).toBe(true)
  })
})

describe("a run's blockers", () => {
  const at = '2026-11-01T10:00:00Z'
  it("are the live run's, whole and as of its last run event, when the live store holds it", () => {
    const long = 'x'.repeat(300)
    const live = { tasks: [{ id: 'build', blocker: { reason: long, by: 'builder', at } }, { id: 'review' }, { id: 'hold', blocker: { reason: 'waits on the keys', message: 'm', by: 'asker', at } }] } as unknown as RunInfo
    const s = summary({ blockers: [{ task: 'old', reason: 'gone by now', by: 'x', at }] })
    expect(runBlockers(s, live)).toEqual([
      { task: 'build', reason: long, by: 'builder', at },
      { task: 'hold', reason: 'waits on the keys', by: 'asker', at },
    ])
    // A live run with no blocker left shows none, whatever the summary read before said.
    expect(runBlockers(s, { tasks: [{ id: 'build' }] } as unknown as RunInfo)).toEqual([])
  })

  it("are the summary's for a run the live store does not hold, and none with neither", () => {
    const s = summary({ blockers: [{ task: 'build', reason: 'the base is gone', by: 'builder', at }] })
    expect(runBlockers(s, undefined)).toEqual([{ task: 'build', reason: 'the base is gone', by: 'builder', at }])
    expect(runBlockers(undefined, undefined)).toEqual([])
    expect(runBlockers(summary(), undefined)).toEqual([])
  })
})

describe('gathered reads', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(0)
  })
  afterEach(() => vi.useRealTimers())

  it('make one read for a burst after the window, one more for an ask made while it reads, none once stopped', async () => {
    let n = 0
    let release: () => void = () => {}
    const g = gather(() => {
      n++
      return new Promise<void>((r) => (release = r))
    }, 250)
    g.ask()
    g.ask()
    await vi.advanceTimersByTimeAsync(249)
    expect(n).toBe(0)
    await vi.advanceTimersByTimeAsync(1)
    expect(n).toBe(1)
    g.ask()
    await vi.advanceTimersByTimeAsync(1000)
    expect(n).toBe(1)
    release()
    await vi.advanceTimersByTimeAsync(250)
    expect(n).toBe(2)
    release()
    g.ask()
    g.stop()
    await vi.advanceTimersByTimeAsync(1000)
    expect(n).toBe(2)
  })

  it('wait for the next ask after a read that fails', async () => {
    let n = 0
    const g = gather(() => {
      n++
      return Promise.reject(new Error('Cannot reach the server'))
    }, 250)
    g.ask()
    await vi.advanceTimersByTimeAsync(10_000)
    expect(n).toBe(1)
    g.ask()
    await vi.advanceTimersByTimeAsync(250)
    expect(n).toBe(2)
  })
})

describe('a receipt', () => {
  it('says how it ended, with where it ran kept on it', () => {
    const chips = (p: Partial<CandidateReceipt>) => receiptChips(receipt(p)).map((c) => c.label)
    expect(chips({})).toEqual(['passed'])
    expect(chips({ exit: 1 })).toEqual(['failed (exit 1)'])
    expect(chips({ exit: undefined, signal: 'killed' })).toEqual(['failed (killed)'])
    expect(chips({ timedOut: true, exit: -1 })).toEqual(['timed out'])
    expect(chips({ state: 'interrupted', exit: undefined })).toEqual(['interrupted'])
    expect(chips({ stable: false })).toEqual(['changed the tree'])
    expect(chips({ state: 'running', exit: undefined, finishedAt: undefined })).toEqual(['running'])
    expect(chips({ exit: 2, logDropped: true })).toEqual(['failed (exit 2)', 'log dropped'])
    expect([receiptState(receipt()), receiptState(receipt({ exit: 1 })), receiptState(receipt({ state: 'interrupted' })), receiptState(receipt({ state: 'started' }))]).toEqual(['passed', 'failed', 'interrupted', 'running'])
    expect(receipt().where).toBe('native')
  })

  it('says how long it ran, finds its step for the argv, and compares the log the route sent with its hash', () => {
    expect(receiptDuration(receipt())).toBe(2000)
    expect(receiptDuration(receipt({ finishedAt: undefined }))).toBeUndefined()
    expect([durationWords(850), durationWords(4200), durationWords(185_000), durationWords(undefined)]).toEqual(['850 ms', '4.2 s', '3 min 5 s', ''])
    const steps = [{ phase: 'check', id: 'fixed', k: 2, argv: ['git', 'status'] }, { phase: 'verify', id: 'fixed', k: 1, argv: ['make'] }] as unknown as Parameters<typeof stepOf>[0]
    expect(stepOf(steps, receipt({ k: 2 }))?.argv).toEqual(['git', 'status'])
    expect(stepOf(steps, receipt({ phase: 'verify' }))?.argv).toEqual(['make'])
    expect(stepOf(steps, receipt())).toBeUndefined()
    expect(logMatches('AB'.repeat(32), 'ab'.repeat(32))).toBe('matches')
    expect(logMatches('cd'.repeat(32), 'ab'.repeat(32))).toBe('differs')
    expect(logMatches('', 'ab')).toBe('unknown')
  })
})

describe('candidates', () => {
  it("say a work attempt's checks and a review's verdict with its findings counted", () => {
    expect(candidateWords(candidate({ receipts: [receipt({ exit: 1 })], state: 'failed', stage: 'check fixed' })).text).toBe('failed check fixed')
    expect(candidateWords(candidate({ receipts: [receipt()] })).text).toBe('passed its checks')
    expect(candidateWords(candidate()).text).toBe('succeeded, no check')
    expect(candidateWords(candidate({ receipts: [receipt({ exit: 1 })], acceptedAnyway: { actor: 'admin', at: '', command: 'c1' } })).text).toBe('accepted anyway')
    expect(candidateWords(candidate({ kind: 'review', verdict: 'needs_changes', findings: { major: 1, nit: 2 } })).text).toBe('asked for changes: 1 major, 2 nit')
    expect(candidateWords(candidate({ kind: 'review', verdict: 'approved' })).text).toBe('approved')
    expect(findingsWords({ nit: 1, blocker: 2 })).toBe('2 blocker, 1 nit')
  })

  it('keep a receipt opened in full by its identity: a running check opened shows the finished receipt once a read has it', () => {
    const running = receipt({ state: 'running', exit: undefined, finishedAt: undefined, logSha256: undefined, logBytes: 0 })
    const c = candidate({ attempt: 2, commit: 'c2', receipts: [receipt({ id: 'lint' }), running] })
    const pick = pickReceipt(c, running)
    const page = (r: CandidateReceipt): TaskCandidates[] => [{ task: 'build', kind: 'work', more: 0, candidates: [{ ...c, receipts: [receipt({ id: 'lint' }), r] }] }]
    expect(pickedReceipt(page(running), pick)).toEqual({ task: 'build', attempt: 2, commit: 'c2', receipt: running })
    const finished = receipt()
    expect(pickedReceipt(page(finished), pick)?.receipt).toEqual(finished)
    // Its full view reads the log again when it finished, never for the same receipt read again unchanged.
    expect(receiptVersion(finished)).not.toBe(receiptVersion(running))
    expect(receiptVersion({ ...running })).toBe(receiptVersion(running))
    // A review's receipt runs against the commit it reviewed; a read that no longer holds the receipt leaves it as it was opened.
    const r = candidate({ task: 'review', kind: 'review', attempt: 1, start: 'c2', receipts: [running] })
    expect(pickedReceipt([], pickReceipt(r, running))).toEqual({ task: 'review', attempt: 1, commit: 'c2', receipt: running })
    expect(pickedReceipt(page(finished), null)).toBeNull()
  })

  // A task as its route reads it: what it needs and every attempt, `at` the minute each started (and, by `end`, closed).
  type At = { n: number; start?: string; result?: string; state?: RunAttempt['state']; at?: number; end?: number; verdict?: 'approved' | 'needs_changes' }
  const minute = (m: number | undefined) => (m === undefined ? undefined : new Date(Date.UTC(2026, 10, 1, 10, m)).toISOString())
  function detail(id: string, kind: 'work' | 'review', attempts: At[], needs?: string[]): [string, TaskDetail] {
    return [
      id,
      {
        task: { id, kind, needs },
        attempts: attempts.map((a) => ({ n: a.n, start: a.start, result: a.result, state: a.state ?? 'succeeded', startedAt: minute(a.at), endedAt: minute(a.end), submitted: a.verdict ? { verdict: a.verdict } : undefined }) as RunAttempt),
      },
    ]
  }

  it('make their repairs: a failed check then the attempt at its result, a review then the reviewed task at the commit it reviewed', () => {
    const tasks: TaskCandidates[] = [
      {
        task: 'build',
        kind: 'work',
        more: 0,
        candidates: [
          candidate({ attempt: 3, start: 'c2', commit: 'c3', receipts: [receipt()], standing: true }),
          candidate({ attempt: 2, start: 'c1', commit: 'c2', receipts: [receipt()] }),
          candidate({ attempt: 1, start: 'b0', commit: 'c1', state: 'failed', receipts: [receipt({ exit: 1 })] }),
        ],
      },
      {
        task: 'review',
        kind: 'review',
        more: 0,
        candidates: [candidate({ task: 'review', kind: 'review', attempt: 2, start: 'c3', verdict: 'approved' }), candidate({ task: 'review', kind: 'review', attempt: 1, start: 'c2', verdict: 'needs_changes', findings: { major: 1 } })],
      },
    ]
    const details = new Map([
      detail('build', 'work', [
        { n: 1, start: 'b0', result: 'c1', state: 'failed', at: 0, end: 1 },
        { n: 2, start: 'c1', result: 'c2', at: 2, end: 3 },
        { n: 3, start: 'c2', result: 'c3', at: 5, end: 6 },
      ]),
      detail('review', 'review', [{ n: 1, start: 'c2', at: 3, end: 4, verdict: 'needs_changes' }, { n: 2, start: 'c3', at: 6, end: 7, verdict: 'approved' }], ['build']),
    ])
    expect(repairs(tasks, details)).toEqual([
      { key: 'check:build:1', kind: 'check', task: 'build', attempt: 1, commit: 'c1', what: 'check fixed', by: [{ task: 'build', attempt: 2, state: 'succeeded', commit: 'c2' }], unread: [] },
      { key: 'review:review:1', kind: 'review', task: 'review', attempt: 1, commit: 'c2', what: '1 major', by: [{ task: 'build', attempt: 3, state: 'succeeded', commit: 'c3' }], unread: [] },
    ])
    // Until a task's own read answers, who took a repair up is not known, and the repair names the task it waits on.
    expect(repairs(tasks).map((r) => [r.key, r.by, r.unread])).toEqual([
      ['check:build:1', [], ['build']],
      ['review:review:1', [], ['review']],
    ])
    // The review read and the task it reviews not yet: both wait on that task.
    expect(repairs(tasks, new Map([['review', details.get('review')!]])).map((r) => r.unread)).toEqual([['build'], ['build']])
    expect(repairReads(tasks, new Map())).toEqual(['build', 'review'])
    expect(repairReads(tasks.slice(1), details)).toEqual(['review', 'build'])
  })

  it('keep a repair that submitted the reviewed commit unchanged, and give each review the attempts that started after its verdict', () => {
    const review = (attempt: number) => candidate({ task: 'review', kind: 'review', attempt, start: 'c1', verdict: 'needs_changes', findings: { major: 1 } })
    // build/1 makes c1, review/1 asks for changes, build/2 starts at c1 and submits it unchanged.
    const once: TaskCandidates[] = [
      { task: 'build', kind: 'work', more: 0, candidates: [candidate({ attempt: 2, start: 'c1', commit: 'c1' }), candidate({ attempt: 1, start: 'b0', commit: 'c1' })] },
      { task: 'review', kind: 'review', more: 0, candidates: [review(1)] },
    ]
    const build1 = { n: 1, start: 'b0', result: 'c1', at: 0, end: 1 }
    const build2 = { n: 2, start: 'c1', result: 'c1', at: 3, end: 4 }
    const onceRead = new Map([detail('build', 'work', [build1, build2]), detail('review', 'review', [{ n: 1, start: 'c1', at: 1, end: 2, verdict: 'needs_changes' }], ['build'])])
    expect(repairs(once, onceRead).map((r) => [r.key, r.by.map((b) => b.attempt)])).toEqual([['review:review:1', [2]]])
    // review/2 reads build/2's c1 and asks again; build/3 starts at c1: the first review's repair is build/2 alone, the second's build/3.
    const twice: TaskCandidates[] = [
      { task: 'build', kind: 'work', more: 0, candidates: [candidate({ attempt: 3, start: 'c1', commit: 'c4' }), ...once[0]!.candidates] },
      { task: 'review', kind: 'review', more: 0, candidates: [review(2), review(1)] },
    ]
    const twiceRead = new Map([
      detail('build', 'work', [build1, build2, { n: 3, start: 'c1', result: 'c4', at: 6, end: 7 }]),
      detail('review', 'review', [{ n: 1, start: 'c1', at: 1, end: 2, verdict: 'needs_changes' }, { n: 2, start: 'c1', at: 4, end: 5, verdict: 'needs_changes' }], ['build']),
    ])
    expect(repairs(twice, twiceRead).map((r) => [r.key, r.by.map((b) => b.attempt)])).toEqual([
      ['review:review:1', [2]],
      ['review:review:2', [3]],
    ])
    // A result that failed its check stood for no review: the check's repair that resubmitted it unchanged is what the review read.
    const checked: TaskCandidates[] = [
      {
        task: 'build',
        kind: 'work',
        more: 0,
        candidates: [candidate({ attempt: 3, start: 'c1', commit: 'c5' }), candidate({ attempt: 2, start: 'c1', commit: 'c1' }), candidate({ attempt: 1, start: 'b0', commit: 'c1', state: 'failed', receipts: [receipt({ exit: 1 })] })],
      },
      { task: 'review', kind: 'review', more: 0, candidates: [review(1)] },
    ]
    const checkedRead = new Map([
      detail('build', 'work', [{ ...build1, state: 'failed' }, { n: 2, start: 'c1', result: 'c1', at: 2, end: 3 }, { n: 3, start: 'c1', result: 'c5', at: 5, end: 6 }]),
      detail('review', 'review', [{ n: 1, start: 'c1', at: 3, end: 4, verdict: 'needs_changes' }], ['build']),
    ])
    expect(repairs(checked, checkedRead).map((r) => [r.key, r.by.map((b) => b.attempt)])).toEqual([
      ['check:build:1', [2, 3]],
      ['review:review:1', [3]],
    ])
  })

  it("give a review cancelled before its verdict nothing, and the next review's repair the attempt after its own verdict", () => {
    // review-a/1 asks for changes on c1 and the engine cancels review-b/1 with it; build/2 starts at c1 and submits it unchanged;
    // review-b/2 asks for changes on c1; build/3 repairs it. Counting review-b's verdicts would give review-b/2 build/2.
    const tasks: TaskCandidates[] = [
      { task: 'build', kind: 'work', more: 0, candidates: [candidate({ attempt: 3, start: 'c1', commit: 'c3' }), candidate({ attempt: 2, start: 'c1', commit: 'c1' }), candidate({ attempt: 1, start: 'b0', commit: 'c1' })] },
      { task: 'review-a', kind: 'review', more: 0, candidates: [candidate({ task: 'review-a', kind: 'review', attempt: 1, start: 'c1', verdict: 'needs_changes', findings: { major: 1 } })] },
      { task: 'review-b', kind: 'review', more: 0, candidates: [candidate({ task: 'review-b', kind: 'review', attempt: 2, start: 'c1', verdict: 'needs_changes', findings: { minor: 1 } })] },
    ]
    const details = new Map([
      detail('build', 'work', [
        { n: 1, start: 'b0', result: 'c1', at: 0, end: 1 },
        { n: 2, start: 'c1', result: 'c1', at: 3, end: 4 },
        { n: 3, start: 'c1', result: 'c3', at: 6, end: 7 },
      ]),
      detail('review-a', 'review', [{ n: 1, start: 'c1', at: 1, end: 2, verdict: 'needs_changes' }], ['build']),
      detail('review-b', 'review', [{ n: 1, start: 'c1', state: 'cancelled', at: 1, end: 2 }, { n: 2, start: 'c1', at: 4, end: 5, verdict: 'needs_changes' }], ['build']),
    ])
    expect(repairs(tasks, details).map((r) => [r.key, r.by.map((b) => `${b.task}/${b.attempt}`)])).toEqual([
      ['review:review-a:1', ['build/2']],
      ['review:review-b:2', ['build/3']],
    ])
  })

  it('name a repair attempt that never submitted from the task route, a record whose live run is gone too, and none before it starts', () => {
    const tasks: TaskCandidates[] = [{ task: 'build', kind: 'work', more: 0, candidates: [candidate({ attempt: 1, commit: 'c1', state: 'failed', receipts: [receipt({ exit: 1 })] })] }]
    expect(repairs(tasks, new Map([detail('build', 'work', [{ n: 1, start: 'b0', result: 'c1', state: 'failed', at: 0, end: 1 }])]))[0]!.by).toEqual([])
    // build/2 started at the failed result and stopped before it submitted: absent from the candidates, there in the task's own read.
    const read = new Map([detail('build', 'work', [{ n: 1, start: 'b0', result: 'c1', state: 'failed', at: 0, end: 1 }, { n: 2, start: 'c1', state: 'stopped', at: 2, end: 3 }])])
    expect(repairs(tasks, read)[0]!.by).toEqual([{ task: 'build', attempt: 2, state: 'stopped', commit: undefined }])
    // A repair of a review reserved but not launched yet (no startedAt) is the newest attempt: it is the review's.
    const reviewed: TaskCandidates[] = [{ task: 'review', kind: 'review', more: 0, candidates: [candidate({ task: 'review', kind: 'review', attempt: 1, start: 'c1', verdict: 'needs_changes' })] }]
    const reserved = new Map([
      detail('build', 'work', [{ n: 1, start: 'b0', result: 'c1', at: 0, end: 1 }, { n: 2, start: 'c1', state: 'reserved' }]),
      detail('review', 'review', [{ n: 1, start: 'c1', at: 1, end: 2, verdict: 'needs_changes' }], ['build']),
    ])
    expect(repairs(reviewed, reserved)[0]!.by).toEqual([{ task: 'build', attempt: 2, state: 'reserved', commit: undefined }])
  })
})

describe('changed since a launch', () => {
  it('is said in words, the fields then the comments, nothing for nothing', () => {
    expect(changedSinceWords({ fields: ['description', 'acceptance'], comments: 2, rev: 9 })).toBe('The ticket changed after this run launched: the description and the acceptance criteria; 2 comments since')
    expect(changedSinceWords({ fields: ['title', 'description', 'acceptance'], comments: 0, rev: 9 })).toBe('The ticket changed after this run launched: the title, the description and the acceptance criteria')
    expect(changedSinceWords({ fields: [], comments: 1, rev: 9 })).toBe('The ticket changed after this run launched: 1 comment since')
    expect(changedSinceWords({ fields: [], comments: 0, rev: 9 })).toBe('')
    expect(changedSinceWords(undefined)).toBe('')
  })

  it("takes the ticket's own rows after the launch's create row, and none when the history read does not reach it", () => {
    const row = (seq: number, entity: DispatchChange['entity'], entityId: string, op: DispatchChange['op'] = 'set'): DispatchChange => ({ seq, entity, entityId, op, value: '', at: '', actor: 'admin', opId: `o${seq}`, n: 0 })
    const changes = [row(1, 'ticket', 't1', 'create'), row(2, 'launch', 'l1', 'create'), row(3, 'launch', 'l1'), row(4, 'ticket', 't1'), row(5, 'comment', 'c1', 'create'), row(6, 'plan', 'p2', 'create')]
    expect(changesSince(changes, 'l1')!.map((c) => c.seq)).toEqual([4, 5])
    expect(changesSince(changes, 'l9')).toBeNull()
  })

  it('says when the history read stops short of now: a launch found on an early page is not all that follows it', () => {
    // The history is read oldest first: the launch's row on the first page, later edits on pages not read yet.
    expect(changesSinceNote([], true)).toBe('partial')
    expect(changesSinceNote([{}], true)).toBe('partial')
    expect(changesSinceNote([], false)).toBe('none')
    expect(changesSinceNote([{}], false)).toBe('')
    expect(changesSinceNote(null, true)).toBe('unread')
    expect(changesSinceNote(null, false)).toBe('unread')
  })
})
