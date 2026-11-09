import { describe, expect, it } from 'vitest'
import { ReplyError } from './reply'
import { CommandRefused } from './tasks'
import {
  decisionLinkTargets,
  addRecent,
  answerFailure,
  receiptFailure,
  unknownWords,
  whereToAnswer,
  homeNext,
  inboxEmpty,
  mergeRecent,
  recentBadge,
  recentFor,
  UNCONFIRMED,
  withoutRecent,
  barCount,
  firstLandingInTab,
  inboxOrder,
  keptRecent,
  movedOnWords,
  recheckStands,
  notificationTarget,
  phoneLanding,
  phoneOffer,
  planIdOf,
  readRecentActions,
  reconcileDelivery,
  RECENT_KEY,
  RECENT_MAX,
  RECENT_MS,
  refParts,
  showingWords,
  stateWords,
  STEP_TITLES,
  stepsFinal,
  ticketOf,
  itemOf,
  decisionIdOf,
  inboxKindWords,
  inboxSections,
  isDecisionRef,
  type InboxItem,
  withOutcome,
  writeRecentActions,
  mintRequestId,
  type DeliveryStep,
  type RecentEntry,
  type StepState,
} from './inbox'

// The decision page's pure parts (docs/plans/decision-inbox.md, PR-2).

const steps = (...states: StepState[]): DeliveryStep[] =>
  (['available', 'recorded', 'validated', 'resumed'] as const).map((step, i) => ({ step, state: states[i] ?? 'unknown', words: `${step} ${states[i]}` }))
const stateOf = (s: DeliveryStep[]) => s.map((x) => x.state)

/** A storage in memory, or one that throws on every access. */
function memory(): Storage {
  const m = new Map<string, string>()
  return { getItem: (k) => m.get(k) ?? null, setItem: (k, v) => void m.set(k, v), removeItem: (k) => void m.delete(k), clear: () => m.clear(), key: () => null, length: 0 }
}
const throwing = { getItem: () => { throw new Error('denied') }, setItem: () => { throw new Error('denied') } }

describe('the order', () => {
  it('lists the oldest first, by since then ref, the server order', () => {
    const items = [
      { ref: 'task:r/b', since: '2026-11-06T10:00:02Z' },
      { ref: 'question:s/q2', since: '2026-11-06T10:00:01Z' },
      { ref: 'question:s/q1', since: '2026-11-06T10:00:01Z' },
      { ref: 'plan:CND-1/1', since: '2026-11-06T09:59:00Z' },
    ]
    expect(inboxOrder(items).map((i) => i.ref)).toEqual(['plan:CND-1/1', 'question:s/q1', 'question:s/q2', 'task:r/b'])
  })
  it('says how much of the list it shows when it holds fewer than there are', () => {
    expect(showingWords(150, 212)).toBe('Showing 150 of 212')
    expect(showingWords(3, 3)).toBe('')
  })
})

describe('the steps in words', () => {
  it('names the four steps apart and says each state', () => {
    expect(Object.values(STEP_TITLES)).toEqual(['Request available', 'Response recorded', 'Received and validated by the execution owner', 'Work resumed'])
    expect(['done', 'waiting', 'refused', 'not_applicable', 'unknown'].map(stateWords)).toEqual(['Done', 'Waiting', 'Refused', 'Does not apply', 'Unknown'])
  })
  it("says a stale refusal's words with what the item is now and that nothing was done", () => {
    expect(movedOnWords('task', null)).toBe('The run moved on. Nothing was done.')
    expect(movedOnWords('plan', { now: 'revision 2 is approved', title: '' })).toBe('The revision or its ticket moved on: revision 2 is approved. Nothing was done.')
  })
  it("says what stands now for a Check again the run moved on from: the candidate its item read again names, or none (#713)", () => {
    const recheck = { name: 'recheck', label: 'Check again', expect: '9', scope: '', route: 'POST /api/runs/r1/tasks/build/recheck', attempt: 4, commit: '0123456789abcdef0123456789abcdef01234567' }
    expect(recheckStands({ actions: [recheck] })).toBe('; the candidate that stands is attempt 4 at 0123456789ab, which Check again offers now')
    expect(recheckStands({ actions: [{ ...recheck, name: 'retry' }] })).toBe('; no candidate stands that Check again takes')
    expect(recheckStands(null)).toBe('')
  })
})

describe('reconcileDelivery', () => {
  it('never moves a step back, whatever order the replies come back in', () => {
    const early = steps('done', 'done', 'waiting', 'waiting')
    const late = steps('done', 'done', 'done', 'done')
    // In order.
    expect(stateOf(reconcileDelivery(reconcileDelivery(undefined, early), late))).toEqual(['done', 'done', 'done', 'done'])
    // The later read's reply first, the earlier's after it: the done steps stay.
    expect(stateOf(reconcileDelivery(reconcileDelivery(undefined, late), early))).toEqual(['done', 'done', 'done', 'done'])
  })
  it('keeps a done over a later unknown (a session that forgot) and a refused over a later waiting', () => {
    const seen = reconcileDelivery(undefined, steps('done', 'done', 'done', 'done'))
    expect(stateOf(reconcileDelivery(seen, steps('done', 'unknown', 'unknown', 'unknown')))).toEqual(['done', 'done', 'done', 'done'])
    const refused = reconcileDelivery(undefined, steps('done', 'done', 'refused', 'not_applicable'))
    // The refusal stays, and the action it ended resumes nothing, whatever a later read says of the task.
    expect(stateOf(reconcileDelivery(refused, steps('done', 'done', 'waiting', 'waiting')))).toEqual(['done', 'done', 'refused', 'not_applicable'])
  })
  it("fills a hosted question's unknown recorded and validated steps with the reply's own word", () => {
    const hosted = steps('done', 'unknown', 'unknown', 'waiting')
    const took = reconcileDelivery(undefined, hosted, 'accepted')
    expect(stateOf(took)).toEqual(['done', 'done', 'done', 'waiting'])
    expect(took[1]!.words).toContain('reply')
    const refused = reconcileDelivery(undefined, hosted, 'refused: Already answered')
    // The owner refused it: its steps end there, resumed not applicable, never waiting on the question's own resumption.
    expect(stateOf(refused)).toEqual(['done', 'refused', 'refused', 'not_applicable'])
    expect(refused[2]!.words).toBe('Already answered')
    expect(stepsFinal(refused)).toBe(true)
    // An older owner's silence, or a connection lost before the word, fills nothing: unknown stays unknown.
    expect(stateOf(reconcileDelivery(undefined, hosted, UNCONFIRMED))).toEqual(['done', 'unknown', 'unknown', 'waiting'])
    // No word yet (sent): the server's track alone.
    expect(stateOf(reconcileDelivery(undefined, hosted))).toEqual(['done', 'unknown', 'unknown', 'waiting'])
  })
  it('takes an explicit refusal over a kept question\'s waiting steps: set aside or held over, it refused this answer', () => {
    // The kept question's track (questionSteps): recorded and validated waiting, "set aside: the session keeps it to show again".
    const kept = steps('done', 'waiting', 'waiting', 'waiting')
    const refused = reconcileDelivery(undefined, kept, 'refused: this question was already answered: nothing was sent')
    expect(stateOf(refused)).toEqual(['done', 'refused', 'refused', 'not_applicable'])
    expect(refused[1]!.words).toBe('this question was already answered: nothing was sent')
    expect(stepsFinal(refused)).toBe(true)
    // A later read that still says waiting (the question kept on) moves nothing back.
    expect(stateOf(reconcileDelivery(refused, kept, 'refused: this question was already answered: nothing was sent'))).toEqual(['done', 'refused', 'refused', 'not_applicable'])
    // A confirmation fills only what the server cannot know (unknown), never a waiting step: the server's track says when it is taken.
    expect(stateOf(reconcileDelivery(undefined, kept, 'accepted'))).toEqual(['done', 'waiting', 'waiting', 'waiting'])
    // The refusal leaves what the server settled as it said it.
    expect(stateOf(reconcileDelivery(undefined, steps('done', 'done', 'done', 'waiting'), 'refused: no'))).toEqual(['done', 'done', 'done', 'waiting'])
  })
  it("ends a refused hosted answer with a resumption that does not apply, even where another answer's resumed the question", () => {
    // Another answer won and the work went on: the question's resumption is done, but not this refused action's.
    const after = reconcileDelivery(undefined, steps('done', 'unknown', 'unknown', 'done'), 'refused: Already answered')
    expect(stateOf(after)).toEqual(['done', 'refused', 'refused', 'not_applicable'])
    expect(after[3]!.words).toContain('refused')
  })
})

describe('stepsFinal', () => {
  it('is final once resumed is done or does not apply, or validated is refused', () => {
    expect(stepsFinal(steps('done', 'done', 'done', 'done'))).toBe(true)
    expect(stepsFinal(steps('done', 'done', 'done', 'not_applicable'))).toBe(true)
    expect(stepsFinal(steps('done', 'done', 'refused', 'not_applicable'))).toBe(true)
    expect(stepsFinal(undefined)).toBe(false)
  })
  it('keeps an accepted command tracked, and an uncertain one once its resumed step is read from the task', () => {
    // accepted: validated waiting, resumed waiting: its settlement follows.
    expect(stepsFinal(steps('done', 'done', 'waiting', 'waiting'))).toBe(false)
    // uncertain (validated unknown): not final until the task says what it did; then final either way.
    expect(stepsFinal(steps('done', 'done', 'unknown', 'waiting'))).toBe(false)
    expect(stepsFinal(steps('done', 'done', 'unknown', 'done'))).toBe(true)
    expect(stepsFinal(steps('done', 'done', 'unknown', 'not_applicable'))).toBe(true)
  })
})

describe("this browser's recent actions", () => {
  const now = Date.parse('2026-11-06T12:00:00Z')
  const entry = (i: number, ago = 0): RecentEntry => ({ ref: `task:r/t${i}`, receipt: `c${i}`, at: now - ago - i })
  it('keeps the newest 50, none older than a day', () => {
    let list: RecentEntry[] = []
    for (let i = 0; i < 60; i++) list = addRecent(list, entry(i), now)
    expect(list).toHaveLength(RECENT_MAX)
    expect(list[0]!.receipt).toBe('c0')
    expect(keptRecent([entry(1, RECENT_MS + 1), entry(2)], now).map((e) => e.receipt)).toEqual(['c2'])
  })
  it('reads storage that throws, holds nothing or holds something else as empty, and writes through a storage that throws', () => {
    expect(readRecentActions(throwing, now)).toEqual([])
    expect(readRecentActions(null, now)).toEqual([])
    const m = memory()
    m.setItem(RECENT_KEY, '{"not":"a list"}')
    expect(readRecentActions(m, now)).toEqual([])
    m.setItem(RECENT_KEY, 'not json')
    expect(readRecentActions(m, now)).toEqual([])
    expect(() => writeRecentActions(throwing, [entry(1)])).not.toThrow()
    writeRecentActions(m, [entry(1), { bad: true } as unknown as RecentEntry])
    expect(readRecentActions(m, now).map((e) => e.receipt)).toEqual(['c1'])
  })
  it("keeps the page's own actions over a storage that lost them, once each, with the owner's word where either has it", () => {
    const mine = [{ ...entry(1), attempt: 'a1', outcome: 'accepted' }, entry(2)]
    const stored = [{ ...entry(1), attempt: 'a1' }, entry(3)]
    const merged = mergeRecent(mine, stored, now)
    expect(merged.map((e) => e.receipt)).toEqual(['c1', 'c2', 'c3'])
    expect(merged[0]!.outcome).toBe('accepted')
    // A storage that answers nothing (it threw) loses none of the page's.
    expect(mergeRecent(mine, [], now).map((e) => e.receipt)).toEqual(['c1', 'c2'])
  })
  it('opens the action a Recent row names, else the newest on the item, and drops one never sent', () => {
    const list = [
      { ref: 'question:s/q', receipt: 'q', attempt: 'd2', at: now, outcome: 'accepted' },
      { ref: 'question:s/q', receipt: 'q', attempt: 'd1', at: now - 1, outcome: 'refused: Already answered' },
    ]
    expect(recentFor(list, 'question:s/q')?.attempt).toBe('d2')
    expect(recentFor(list, 'question:s/q', { receipt: 'q', attempt: 'd1' })?.outcome).toBe('refused: Already answered')
    expect(recentFor(list, 'question:s/q', { receipt: 'q', attempt: 'gone' })).toBeUndefined()
    expect(withoutRecent(list, { ref: 'question:s/q', receipt: 'q', attempt: 'd2' }).map((e) => e.attempt)).toEqual(['d1'])
  })
  it("writes the reply's outcome into its entry, by its receipt and attempt", () => {
    const list = [{ ...entry(1), attempt: 'a1' }, { ...entry(1), attempt: 'a2' }]
    const out = withOutcome(list, 'c1', 'a2', 'accepted')
    expect(out.map((e) => e.outcome)).toEqual([undefined, 'accepted'])
  })
})

describe("the phone's paths", () => {
  it('lands a phone on the decision page when an item waits, and nowhere at lg or with none', () => {
    expect(phoneLanding(390, 2)).toBe('/decisions')
    expect(phoneLanding(390, 0)).toBeNull()
    expect(phoneLanding(1024, 2)).toBeNull()
  })
  it("keeps the phone's landing once chosen: a later change of the home's target sends it nowhere else", () => {
    const o = { wide: false, answered: true, late: false, width: 390, open: 1, home: '/sessions' }
    const first = homeNext({ judging: true, landed: false }, o)
    expect(first.to).toBe('/decisions')
    // The sessions answer while that navigation is under way: the home's target changes, and the landing stays.
    expect(homeNext(first.landing, { ...o, home: '/sessions' }).to).toBeNull()
    // Before the inbox answered, nothing; with no item open, the home's way, and after that the home's way again.
    expect(homeNext({ judging: true, landed: false }, { ...o, answered: false }).to).toBeNull()
    const none = homeNext({ judging: true, landed: false }, { ...o, open: 0 })
    expect(none.to).toBe('/sessions')
    expect(homeNext(none.landing, { ...o, open: 3 }).to).toBe('/sessions')
    // Wide: no landing at all.
    expect(homeNext({ judging: true, landed: false }, { ...o, wide: true, home: null }).to).toBeNull()
  })
  it('judges the landing once per tab, and each load when the storage throws', () => {
    const m = memory()
    expect(firstLandingInTab(m)).toBe(true)
    expect(firstLandingInTab(m)).toBe(false)
    expect(firstLandingInTab(throwing)).toBe(true)
  })
  it("opens a tapped question's item below lg and its session at lg", () => {
    expect(notificationTarget(390, 'sess1', 'q1')).toBe(`/decisions?item=${encodeURIComponent('question:sess1/q1')}`)
    expect(notificationTarget(1440, 'sess1', 'q1')).toBe('/sessions/sess1')
    expect(notificationTarget(390, 'sess1')).toBe('/sessions/sess1')
  })
  it('counts every open item for the bar, none before the first read', () => {
    expect(barCount({ total: 212 })).toBe(212)
    expect(barCount(null)).toBe(0)
  })
  it("offers a LAN origin's page as a QR code of the address alone, and on a loopback origin the words why not", () => {
    const lan = phoneOffer('http://192.168.1.20:8080')
    expect(lan).toEqual({ kind: 'qr', url: 'http://192.168.1.20:8080/decisions' })
    // Whatever the location carries, the code holds the origin and the page's path: no token, query or fragment.
    const odd = phoneOffer('https://box.lan:8443/?token=secret#t=x')
    expect(odd).toEqual({ kind: 'qr', url: 'https://box.lan:8443/decisions' })
    expect(JSON.stringify(odd)).not.toContain('secret')
    for (const o of ['http://127.0.0.1:8080', 'http://localhost:3000', 'http://[::1]:8080']) {
      const offer = phoneOffer(o)
      expect(offer.kind).toBe('loopback')
      // It says a phone cannot reach this address, and asks the person for no step: no other address to open, no network to set up.
      const words = offer.kind === 'loopback' ? offer.words : ''
      expect(words).toContain('A phone cannot reach this page')
      expect(words).not.toMatch(/network|open the page from|address on|forward|configure|set up/i)
    }
  })
})

describe('the list', () => {
  it('says nothing needs a decision only when its last read answered with none', () => {
    const answered = { loaded: true, items: [], unavailable: '', error: '' }
    expect(inboxEmpty(answered)).toBe(true)
    // An empty read, then a failed one: what is open now is not known.
    expect(inboxEmpty({ ...answered, error: 'the server could not be reached' })).toBe(false)
    expect(inboxEmpty({ ...answered, loaded: false })).toBe(false)
    expect(inboxEmpty({ ...answered, unavailable: 'decisions are answered where the work runs' })).toBe(false)
  })
})

describe("an answer's failure", () => {
  it('tells nothing sent from no word and from the owner\'s refusal', () => {
    expect(answerFailure(new Error('closed (1006)'))).toBe('not_sent')
    expect(answerFailure(new ReplyError('not_connected', 'not connected'))).toBe('not_sent')
    expect(answerFailure(new ReplyError('no_question', 'no question'))).toBe('not_sent')
    expect(answerFailure(new ReplyError('no_word', 'no word'))).toBe('unconfirmed')
    expect(answerFailure(new ReplyError('closed', 'closed first'))).toBe('unconfirmed')
    expect(answerFailure(new ReplyError('question_answered', 'answered by Nate: nothing was sent'))).toBe('refused')
  })
})

describe("a command's or an operation's failure", () => {
  it('is a refusal only with its receipt: a transport failure, a lost reply or a reply with no receipt is unconfirmed', () => {
    expect(receiptFailure(new CommandRefused('stale', { status: 'failed', refusal: 'stale_revision' } as never))).toBe('refused')
    expect(receiptFailure(new TypeError('Failed to fetch'))).toBe('unconfirmed')
    expect(receiptFailure(Object.assign(new Error('no receipt'), { status: 500 }))).toBe('unconfirmed')
    expect(receiptFailure(Object.assign(new Error('lost'), { status: 0 }))).toBe('unconfirmed')
  })
})

describe("a Recent row's word", () => {
  const answer: RecentEntry = { ref: 'question:s/q', receipt: 'q', attempt: 'd1', at: 1, outcome: UNCONFIRMED }
  const command: RecentEntry = { ref: 'task:r/t', receipt: 'c1', at: 1 }
  it('is Not confirmed for an answer nobody confirmed, even once its session ended or resumed: never a word of success', () => {
    // A hosted answer whose acknowledgement was lost, the session then ended (resumed not_applicable) or resumed on another answer.
    expect(recentBadge(answer, steps('done', 'unknown', 'unknown', 'not_applicable'), false)).toEqual({ text: 'Not confirmed', color: 'warning' })
    expect(recentBadge(answer, steps('done', 'unknown', 'unknown', 'done'), false)).toEqual({ text: 'Not confirmed', color: 'warning' })
    // A page reloaded while it was out (no word kept): the same.
    expect(recentBadge({ ...answer, outcome: undefined }, steps('done', 'unknown', 'unknown', 'not_applicable'), false).text).toBe('Not confirmed')
  })
  it('says what the track says once something confirmed it, Refused first, and Sent while it is out', () => {
    expect(recentBadge(answer, steps('done', 'done', 'done', 'done'), false)).toEqual({ text: 'Work resumed', color: 'success' })
    expect(recentBadge(answer, steps('done', 'done', 'done', 'not_applicable'), false)).toEqual({ text: 'Taken', color: 'neutral' })
    expect(recentBadge({ ...answer, outcome: 'accepted' }, steps('done', 'unknown', 'unknown', 'not_applicable'), false).text).toBe('Taken')
    expect(recentBadge(answer, steps('done', 'done', 'done', 'waiting'), false).text).toBe('Resumed: waiting')
    expect(recentBadge(answer, steps('done', 'refused', 'unknown', 'not_applicable'), false)).toEqual({ text: 'Refused', color: 'error' })
    expect(recentBadge(command, steps('done', 'done', 'refused', 'unknown'), false).text).toBe('Refused')
    expect(recentBadge(command, undefined, true)).toEqual({ text: 'Sent', color: 'neutral' })
  })
})

describe('an action nobody confirmed', () => {
  const answer: RecentEntry = { ref: 'question:s/q', receipt: 'q', attempt: 'd1', at: 1 }
  const command: RecentEntry = { ref: 'task:r/t', receipt: 'c1', at: 1 }
  it('is said, with a read again, for every kind of send, until a receipt or the server\'s track says what became of it', () => {
    expect(unknownWords(answer, steps('done', 'unknown', 'unknown', 'waiting'), false)).toContain('Your answer went')
    expect(unknownWords({ ...command, outcome: UNCONFIRMED }, steps('done', 'unknown', 'unknown', 'unknown'), false)).toContain('no receipt came back')
    // Out from this page: not yet; a receipt's word, either way: known.
    expect(unknownWords(command, undefined, true)).toBe('')
    expect(unknownWords({ ...command, outcome: 'accepted' }, undefined, false)).toBe('')
    expect(unknownWords({ ...command, outcome: 'refused: stale' }, undefined, false)).toBe('')
    // The server's track recorded it (or refused it): known.
    expect(unknownWords({ ...command, outcome: UNCONFIRMED }, steps('done', 'done', 'done', 'waiting'), false)).toBe('')
    expect(unknownWords(undefined, undefined, false)).toBe('')
  })
  it('says where a refused question can still be answered, from its track: waiting (kept or shown), and nowhere once it is gone', () => {
    const kept = steps('done', 'waiting', 'waiting', 'waiting')
    kept[1]!.words = 'set aside: the session keeps it to show again when the agent\'s turn is done or the held turn ends'
    expect(whereToAnswer(kept)).toContain('set aside')
    expect(whereToAnswer(kept)).toContain("in the session's terminal")
    expect(whereToAnswer(steps('done', 'refused', 'not_applicable', 'not_applicable'))).toBe('')
    expect(whereToAnswer(undefined)).toBe('')
  })
})

describe('acting', () => {
  it('mints a fresh request id an answer may carry', () => {
    const a = mintRequestId()
    expect(a).toMatch(/^d[0-9a-f]{16}$/)
    expect(mintRequestId()).not.toBe(a)
  })
  it("reads a plan action's revision id from its route, a ref's parts, and the ticket a run was launched from", () => {
    expect(planIdOf('POST /api/dispatch/plans/pl-12/approve')).toBe('pl-12')
    expect(planIdOf('POST /api/runs/r/tasks/t/retry')).toBe('')
    expect(refParts('question:s1/q1')).toEqual({ kind: 'question', a: 's1', b: 'q1' })
    expect(ticketOf({ kind: 'task', evidence: [{ ref: 'run:r', label: '' }, { ref: 'ticket:CND-12', label: '' }] })).toBe('CND-12')
    expect(ticketOf({ kind: 'plan', evidence: [{ ref: 'ticket:CND-12', label: '' }] })).toBe('')
  })
})

describe('an item as the page draws it', () => {
  it('reads the lists the server leaves null when empty as empty lists', () => {
    const raw = { ref: 'question:s/q', blocking: { words: 'waits', refs: null }, actions: null } as unknown as InboxItem
    const it = itemOf(raw)
    expect(it.blocking.refs).toEqual([])
    expect(it.actions).toEqual([])
    expect(it.evidence).toEqual([])
  })
})

describe('decisionLinkTargets', () => {
  it("offers the runs the engine holds and this server's own sessions still running, never a hosted, ended or member session", () => {
    const sessions = [
      { id: 's1', name: 'api-fix', kind: 'server', status: 'running' },
      { id: 's2', name: 'hosted', kind: 'hosted', status: 'running' },
      { id: 's3', name: 'done', kind: 'server', status: 'exited' },
      { id: 's4', name: 'member', kind: 'server', status: 'running', crew: { runId: 'r1' } },
    ]
    const runs = [
      { id: 'r1', name: 'release', label: 'release 7', state: 'finished' },
      { id: 'r2', name: 'old', state: 'stopped' },
    ]
    expect(decisionLinkTargets(sessions, runs)).toEqual([
      { value: 'run:r1', label: 'Run: release 7' },
      { value: 'session:s1', label: 'Session: api-fix' },
    ])
  })
})

// DR1's records on the page (docs/plans/decision-inbox.md, PR-4): the decision kind's ref and words, the list's two parts ("Answered,
// not yet applied" from the server's list), the bar's number without them, and this browser's Recent keeping only actions on items
// DR1 does not hold (its records' Recent is the server's).
describe('the decision kind', () => {
  const s = (ref: string, tracking?: boolean) => ({ ref, kind: ref.startsWith('decision:') ? 'decision' : 'task', since: '', title: ref, brief: '', question: '', answerer: '', actions: [], ...(tracking ? { tracking } : {}) })
  it("names a DR1 record by its ref, and only a record's ref", () => {
    expect(isDecisionRef('decision:dr-12')).toBe(true)
    expect(decisionIdOf('decision:dr-12')).toBe('dr-12')
    for (const r of ['decision:dr-0', 'decision:12', 'question:0123456789abcdef/0123456789abcdef', 'decision:dr-1/x']) {
      expect(isDecisionRef(r)).toBe(false)
      expect(decisionIdOf(r)).toBe('')
    }
    expect(inboxKindWords('decision')).toBe('A durable decision')
    expect(inboxKindWords('task')).toBe('A failed task')
  })
  it('puts the records answered and not yet applied apart, each part in the list\'s order', () => {
    const items = [s('decision:dr-1', true), s('task:crew-0000aaaa-11112222/build'), s('decision:dr-2'), s('decision:dr-3', true)]
    const { open, tracking } = inboxSections(items)
    expect(open.map((x) => x.ref)).toEqual(['task:crew-0000aaaa-11112222/build', 'decision:dr-2'])
    expect(tracking.map((x) => x.ref)).toEqual(['decision:dr-1', 'decision:dr-3'])
  })
  it("leaves the records answered and not yet applied out of the bar's number", () => {
    expect(barCount({ total: 5, tracking: 2 })).toBe(3)
    expect(barCount({ total: 2, tracking: 2 })).toBe(0)
    expect(barCount({ total: 1, tracking: 4 })).toBe(0)
    expect(barCount({ total: 7 })).toBe(7)
  })
  it("keeps no action on a DR1 record in this browser's Recent: the server's Recent holds its records", () => {
    const now = 1_800_000_000_000
    const kept = keptRecent(
      [
        { ref: 'decision:dr-1', receipt: 'resp-1', at: now - 1000 },
        { ref: 'task:crew-0000aaaa-11112222/build', receipt: 'c-1', at: now - 2000 },
      ],
      now,
    )
    expect(kept.map((e) => e.ref)).toEqual(['task:crew-0000aaaa-11112222/build'])
    const stored = { getItem: () => JSON.stringify([{ ref: 'decision:dr-2', receipt: 'r', at: now - 10 }]) }
    expect(readRecentActions(stored, now)).toEqual([])
  })
})
