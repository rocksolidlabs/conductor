import { describe, expect, it } from 'vitest'
import {
  DecisionRefused,
  acceptedAnswer,
  answerRows,
  confirmAuthorizes,
  mirroringSessions,
  oneRowPerDecision,
  replaces,
  requiredRecord,
  rowDecision,
  runHeldOf,
  askForMore,
  cancelDecision,
  commentDecision,
  decisionWords,
  optionsOf,
  recommended,
  refusalOf,
  respondDecision,
  responseBody,
  runRuns,
  subjectWords,
  unmirrored,
  type Decision,
  type ResponseBody,
} from './decisions'

// Durable decisions in the app's words (Program item DR1, docs/plans/decisions.md, "web/app"): every state's words and the four states
// apart, each with its receipt; Ask for more and why not; the writes' ids minted once and a lost reply asked once more with the same
// id; a refusal's words; Needs you's and the palette's rows.

const RUN = 'e2e-crew-0a1b2c3d'
const T0 = '2026-11-07T10:00:00Z'
const T1 = '2026-11-07T10:05:00Z'
const T2 = '2026-11-07T10:05:01Z'

function decision(p: Partial<Decision> = {}): Decision {
  return {
    id: 'dr-7',
    run: RUN,
    openedIn: RUN,
    origin: 'agent',
    type: 'approval',
    asker: { task: 'build', attempt: 1, live: true },
    request: { question: 'May build commit its change?', options: [{ label: 'Approve', tradeoffs: 'it goes in' }, { label: 'Reject' }], recommendation: 0, authorizes: 'build commits' },
    policy: { revision: 'local/1', approver: 'operator', quorum: 1 },
    state: 'open',
    rev: 1,
    createdAt: T0,
    ...p,
  }
}

const recorded = { decision: 'dr-7', k: 1, status: 'accepted' as const, words: 'Recorded: dr-7 answered by Nate', actor: 'admin', byName: 'Nate', choice: 0, at: T1, rev: 2 }
const typed = { at: T2, by: 'yardmaster', run: RUN, task: 'build', attempt: 1, response: 1, validated: true, how: 'typed' as const, words: "typed into build's attempt 1" }

describe("a decision's words", () => {
  it('says every state, each in its words', () => {
    expect(decisionWords(decision()).state).toBe("Open: waits for the operator's answer")
    expect(decisionWords(decision({ state: 'answered', answeredAt: T1, responses: [recorded] })).state).toBe('Recorded: Yardmaster applies it next')
    expect(decisionWords(decision({ state: 'answered', answeredAt: T1 }), { runHeld: false }).state).toBe('Recorded: waiting for the run to be continued')
    expect(decisionWords(decision({ state: 'applied', answeredAt: T1, applied: typed })).state).toBe("Applied: typed into build's attempt 1")
    expect(decisionWords(decision({ state: 'superseded', why: 'HEAD is now 9f2c1ab, was 3c1f2d0', supersededBy: 'dr-9' })).state).toBe('Superseded: HEAD is now 9f2c1ab, was 3c1f2d0 (dr-9 takes its place)')
    expect(decisionWords(decision({ state: 'cancelled', why: 'the run ended, failed, before dr-7 was answered' })).state).toBe('Cancelled: the run ended, failed, before dr-7 was answered')
    expect(decisionWords(decision({ state: 'expired', why: 'escalated' })).state).toBe('Expired: escalated')
  })

  it('shows the four states apart, each reached with its receipt and time', () => {
    const open = decisionWords(decision()).steps
    expect(open.map((s) => [s.key, s.status])).toEqual([['open', 'done'], ['recorded', 'waiting'], ['validated', 'pending'], ['applied', 'pending']])
    expect(open[0]).toMatchObject({ at: T0, words: "asked by build's attempt 1" })

    const answered = decisionWords(decision({ state: 'answered', answeredAt: T1, responses: [recorded] })).steps
    expect(answered.map((s) => s.status)).toEqual(['done', 'done', 'waiting', 'pending'])
    expect(answered[1]).toMatchObject({ words: 'Recorded: dr-7 answered by Nate', at: T1, receipt: 'dr-7/1' })
    expect(answered[2]!.words).toBe('Yardmaster validates it next')
    expect(decisionWords(decision({ state: 'answered', answeredAt: T1, responses: [recorded] }), { runHeld: false }).steps[2]!.words).toBe('waiting for the run to be continued')

    const applied = decisionWords(decision({ state: 'applied', answeredAt: T1, responses: [recorded], applied: typed })).steps
    expect(applied.map((s) => s.status)).toEqual(['done', 'done', 'done', 'done'])
    expect(applied[2]).toMatchObject({ label: 'Validated by Yardmaster', at: T2, receipt: 'dr-7/1' })
    expect(applied[3]).toMatchObject({ label: 'Applied', words: "typed into build's attempt 1", at: T2, receipt: 'dr-7/1 applied' })

    // Why not: a decision that ended unanswered, and an approval applied whose subject moved after.
    const cancelled = decisionWords(decision({ state: 'cancelled', why: 'build failed, so deploy will not start', closedAt: T2 })).steps
    expect(cancelled.map((s) => s.status)).toEqual(['done', 'ended', 'ended', 'ended'])
    expect(cancelled[3]).toMatchObject({ label: 'Not applied: cancelled', words: 'build failed, so deploy will not start', at: T2 })
    const moved = decisionWords(decision({ state: 'superseded', why: 'the candidate is now 9f2c1ab, attempt 3', answeredAt: T1, responses: [recorded], applied: typed })).steps
    expect(moved[3]).toMatchObject({ label: 'Applied, then superseded', words: "typed into build's attempt 1; superseded: the candidate is now 9f2c1ab, attempt 3" })
  })

  it("names a gate's asker, its options, the recommended one and the subject", () => {
    const gate = decision({ origin: 'plan', gate: 'ship', asker: undefined, request: { question: 'Ship it?', options: [{ label: 'Approve' }, { label: 'Reject' }], subject: { kind: 'plan', plan: 'pl_1', digest: 'f'.repeat(64) } } })
    expect(decisionWords(gate).steps[0]!.words).toBe("asked by the plan's gate ship")
    expect(recommended(gate)).toBe(-1)
    expect(recommended(decision())).toBe(0)
    expect(optionsOf(decision({ type: 'assignment', request: { question: 'Run the migration' } })).map((o) => o.label)).toEqual(['Done', 'Not done'])
    expect(subjectWords({ kind: 'commit', commit: '3c1f2d0aa' })).toEqual({ text: 'commit 3c1f2d0' })
    expect(subjectWords({ kind: 'candidate', run: RUN, task: 'build', n: 2, commit: '9f2c1abff' })).toEqual({ text: 'the result of build, attempt 2 (9f2c1ab)', to: `/runs/${RUN}?view=tasks&task=build` })
    expect(subjectWords({ kind: 'artifact', path: 'README.md', commit: '3c1f2d0aa', sha256: 'a'.repeat(64) }).text).toBe(`README.md at 3c1f2d0, sha256 ${'a'.repeat(12)}`)
  })
})

describe('Ask for more', () => {
  it('is offered only for an open request of a live asking attempt', () => {
    expect(askForMore(decision())).toEqual({ enabled: true, why: '' })
  })
  it("is disabled with the record's words for a gate, an asker not live, and a decision no longer open", () => {
    expect(askForMore(decision({ origin: 'plan', gate: 'ship', asker: undefined }))).toEqual({ enabled: false, why: "dr-7 is the plan's: no agent asked it, comment instead" })
    expect(askForMore(decision({ asker: { task: 'build', attempt: 1, live: false } }))).toEqual({ enabled: false, why: "dr-7's agent has ended its attempt: comment, answer or cancel it" })
    expect(askForMore(decision({ state: 'answered' }))).toEqual({ enabled: false, why: 'dr-7 is answered' })
  })
})

/** A route that loses its first `lost` replies (status 0) and answers after, recording every body sent. */
function route<B>(lost: number, reply: () => unknown = () => ({ receipt: recorded, decision: decision({ state: 'answered' }) })) {
  const sent: Array<{ path: string; body: B }> = []
  const post = async (path: string, body: B) => {
    sent.push({ path, body })
    if (sent.length <= lost) throw Object.assign(new Error('Cannot reach the server'), { status: 0, code: 'network' })
    return reply() as never
  }
  return { sent, post }
}

describe("the operator's writes", () => {
  it('mints the id once, and asks a lost reply once more with the same id and body', async () => {
    const r = route<ResponseBody>(1)
    const body = responseBody({ choice: 0, rationale: ' small enough ', expectedRev: 1, byName: 'Nate' })
    expect(body.responseId).toMatch(/^[0-9a-f]{16}$/)
    expect(body).toEqual({ responseId: body.responseId, choice: 0, rationale: 'small enough', expectedRev: 1, byName: 'Nate' })
    const got = await respondDecision(r.post, 'dr-7', body)
    expect(got.receipt.words).toBe('Recorded: dr-7 answered by Nate')
    expect(r.sent).toHaveLength(2)
    expect(r.sent[0]!.path).toBe('/api/decisions/dr-7/respond')
    expect(r.sent[1]!.body).toBe(r.sent[0]!.body)
  })

  it('asks once more after a 503 decision_moved, with the same id, and gives up after a second loss', async () => {
    let n = 0
    const moved = async (_: string, b: ResponseBody) => {
      n++
      if (n === 1) throw Object.assign(new Error('moved'), { status: 503, code: 'decision_moved' })
      return { receipt: { ...recorded, decision: b.responseId }, decision: decision() }
    }
    const body = responseBody({ choice: 1, expectedRev: 1 })
    expect((await respondDecision(moved, 'dr-7', body)).receipt.decision).toBe(body.responseId)
    const twice = route<ResponseBody>(2)
    await expect(respondDecision(twice.post, 'dr-7', responseBody({ choice: 0, expectedRev: 1 }))).rejects.toThrow('Cannot reach the server')
    expect(twice.sent).toHaveLength(2)
  })

  it("mints a cancel's and a comment's id once and sends them on their routes", async () => {
    const op = () => ({ receipt: { decision: 'dr-7', op: 'comment', seq: 4, at: T1, rev: 2, words: 'kept' }, decision: decision({ rev: 2 }) })
    const c = route<{ commandId: string; why: string; release?: boolean; expectedRev: number }>(1, op)
    await cancelDecision(c.post, 'dr-7', { why: ' not needed ', release: true, expectedRev: 1 })
    expect(c.sent.map((s) => s.path)).toEqual(['/api/decisions/dr-7/cancel', '/api/decisions/dr-7/cancel'])
    expect(c.sent[0]!.body).toMatchObject({ why: 'not needed', release: true, expectedRev: 1 })
    expect(c.sent[1]!.body.commandId).toBe(c.sent[0]!.body.commandId)
    const m = route<{ opId: string; kind: string; text: string; expectedRev: number }>(0, op)
    await commentDecision(m.post, 'dr-7', { kind: 'more_info', text: 'Which tests?', expectedRev: 1 })
    expect(m.sent[0]).toMatchObject({ path: '/api/decisions/dr-7/comment', body: { kind: 'more_info', text: 'Which tests?', expectedRev: 1 } })
    expect(m.sent[0]!.body.opId).toMatch(/^[0-9a-f]{16}$/)
  })

  it("reads a refusal's words and its kept receipt from the server's reply", async () => {
    const words = 'dr-7 was answered by Priya at 2026-11-07T10:05:00Z'
    const kept = { ...recorded, status: 'refused' as const, code: 'decision_answered', words, k: 2 }
    const refusing = async () => {
      throw Object.assign(new Error('Request failed'), { status: 409, code: 'decision_answered', body: { error: { code: 'decision_answered', message: words }, receipt: kept } })
    }
    const err = await respondDecision(refusing, 'dr-7', responseBody({ choice: 0, expectedRev: 1 })).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(DecisionRefused)
    expect((err as DecisionRefused).message).toBe(words)
    expect((err as DecisionRefused).code).toBe('decision_answered')
    expect((err as DecisionRefused).receipt).toEqual(kept)
    // A refusal with no receipt (a 403, a bound): its words alone; anything else, its own message.
    expect(refusalOf(Object.assign(new Error('x'), { status: 403, body: { error: { code: 'not_approver', message: 'dr-7 is the operator\'s to answer' } } })).message).toBe("dr-7 is the operator's to answer")
    expect(refusalOf(new Error('boom')).message).toBe('boom')
  })
})

describe("Needs you's and the palette's decisions", () => {
  const gate = decision({ id: 'dr-2', origin: 'plan', gate: 'ship', asker: undefined, createdAt: T1, request: { question: 'Ship it?' } })
  const asked = decision({ id: 'dr-1', createdAt: T0 })
  const done = decision({ id: 'dr-3', state: 'applied' })
  const sessions = [{ attention: { state: 'needs_input', decision: 'dr-1' } }, { attention: { state: 'working', decision: 'dr-9' } }]

  it('counts each open decision once: none a shown question mirrors, none that is not open, none of a run that does not run', () => {
    const running = () => true
    expect(unmirrored([gate, asked, done], sessions, running).map((d) => d.id)).toEqual(['dr-2'])
    // A mirror set aside while its agent works shows no question: the decision is counted on its own.
    expect(unmirrored([asked], [{ attention: { state: 'working', decision: 'dr-1' } }], running).map((d) => d.id)).toEqual(['dr-1'])
    // A stopped run's, or one the live store does not hold: answered from its run page, not counted as needing you now.
    expect(unmirrored([gate, asked], [], (id) => id !== RUN)).toEqual([])
    expect(runRuns({})).toBe(true)
    expect(runRuns({ stoppedAt: T0 })).toBe(false)
    expect(runRuns({ record: true })).toBe(false)
    expect(runRuns({ resumedBy: 'next-00000000' })).toBe(false)
    expect(runRuns(undefined)).toBe(false)
  })

  it("gives the palette a row per open decision, oldest first, Enter opening its card", () => {
    const opened: string[] = []
    const rows = answerRows([gate, done, asked], (id) => opened.push(id))
    expect(rows.map((r) => [r.key, r.label, r.detail])).toEqual([
      ['decision:dr-1', 'Answer dr-1: May build commit its change?', "asked by build's attempt 1"],
      ['decision:dr-2', 'Answer dr-2: Ship it?', "asked by the plan's gate ship"],
    ])
    rows[1]!.run()
    expect(opened).toEqual(['dr-2'])
  })
})

// The reviews of #719, each finding's rule.
describe("the card's answer, its confirm, its run and its record", () => {
  it('shows the accepted answer: the option picked, the words, the rationale and who (an approval, a clarification)', () => {
    // An agent's approval answered with its second option, and a rationale.
    expect(acceptedAnswer(decision({ state: 'applied', responses: [{ ...recorded, choice: 1, rationale: 'Too big' }] }))).toEqual({ choice: 'Reject', rationale: 'Too big', by: 'Nate' })
    // A clarification answered in words.
    const clar = decision({ type: 'clarification', request: { question: 'Which port?' }, state: 'answered', responses: [{ ...recorded, choice: undefined, text: 'Use 8443', rationale: 'TLS' }] })
    expect(acceptedAnswer(clar)).toEqual({ text: 'Use 8443', rationale: 'TLS', by: 'Nate' })
    // A refused response is no answer; a list's record carries only the words.
    expect(acceptedAnswer(decision({ responses: [{ ...recorded, status: 'refused', code: 'stale_decision' }] }))).toBeNull()
    expect(acceptedAnswer(decision({ state: 'answered', answer: 'Approve (Nate)' }))).toEqual({ words: 'Approve (Nate)' })
    expect(acceptedAnswer(decision())).toBeNull()
  })

  it("says what an agent's approval authorizes whichever option is picked, and a gate's only for its Approve", () => {
    // An agent's approval whose option 0 is not the yes.
    const agent = decision({ request: { question: 'Ship?', options: [{ label: 'Reject' }, { label: 'Approve' }], authorizes: 'build ships' } })
    expect(confirmAuthorizes(agent, 0)).toBe('This approval authorizes: build ships')
    expect(confirmAuthorizes(agent, 1)).toBe('This approval authorizes: build ships')
    const gate = decision({ origin: 'plan', gate: 'ship', request: { question: 'Ship?', options: [{ label: 'Approve' }, { label: 'Reject' }], authorizes: 'deploy runs' } })
    expect(confirmAuthorizes(gate, 0)).toBe('A yes authorizes: deploy runs')
    expect(confirmAuthorizes(gate, 1)).toBe('')
    expect(confirmAuthorizes(decision({ request: { question: 'Ship?' } }), 0)).toBe('')
  })

  it('takes a reply only of the decision shown and no older than it', () => {
    expect(replaces(null, { id: 'dr-7', rev: 1 }, false)).toBe(true)
    expect(replaces({ id: 'dr-7', rev: 3 }, { id: 'dr-7', rev: 2 }, true)).toBe(false)
    expect(replaces({ id: 'dr-7', rev: 3 }, { id: 'dr-7', rev: 3 }, false)).toBe(false)
    expect(replaces({ id: 'dr-7', rev: 3 }, { id: 'dr-7', rev: 3 }, true)).toBe(true)
    expect(replaces({ id: 'dr-7', rev: 3 }, { id: 'dr-7', rev: 4 }, false)).toBe(true)
    expect(replaces({ id: 'dr-7', rev: 3 }, { id: 'dr-8', rev: 1 }, false)).toBe(true)
  })

  it('knows a run interrupted at a restart (a record the live store leaves out) does not run, and one not read yet is not known', () => {
    expect(runHeldOf(undefined)).toBeUndefined()
    expect(runHeldOf(null)).toBe(false)
    expect(runHeldOf({ record: true, state: 'interrupted' })).toBe(false)
    expect(runHeldOf({ stoppedAt: T0 })).toBe(false)
    expect(runHeldOf({ state: 'running' })).toBe(true)
    // Answered on such a run: the words wait for it to be continued.
    expect(decisionWords(decision({ state: 'answered', responses: [recorded] }), { runHeld: runHeldOf({ record: true }) }).state).toBe('Recorded: waiting for the run to be continued')
  })

  it("names a required decision's record only from a run of the approved revision", () => {
    const r1 = decision({ id: 'dr-1', origin: 'plan', gate: 'ship', plan: 'pl-r1', state: 'applied' })
    const r2 = decision({ id: 'dr-2', origin: 'plan', gate: 'ship', plan: 'pl-r2' })
    // r2 approved, no run of it yet: r1's applied gate of the same id is not r2's.
    expect(requiredRecord([r1], 'ship', 'pl-r2')).toBeUndefined()
    expect(requiredRecord([r2, r1], 'ship', 'pl-r2')?.id).toBe('dr-2')
    expect(requiredRecord([r2, r1], 'ship', 'pl-r1')?.id).toBe('dr-1')
    expect(requiredRecord([r1], 'ship', undefined)).toBeUndefined()
  })
})

describe('one palette row per decision', () => {
  const sessions = [
    { id: 's1', name: 'build', attention: { id: 'q1', state: 'needs_input', decision: 'dr-1' } },
    { id: 's2', name: 'docs', attention: { id: 'q2', state: 'needs_input' } },
    { id: 's3', name: 'old', attention: { id: 'q3', state: 'working', decision: 'dr-3' } },
  ]

  it('finds the sessions whose shown question mirrors an open decision', () => {
    expect([...mirroringSessions(sessions, [{ id: 'dr-1' }, { id: 'dr-3' }])]).toEqual([['s1', 'dr-1']])
    expect([...mirroringSessions(sessions, [])]).toEqual([])
  })

  it("names a row's decision: its own row, the decision page's row of a question that mirrors one, and none else", () => {
    expect(rowDecision('decision:dr-1', sessions)).toBe('dr-1')
    expect(rowDecision('decide:decision:dr-4', sessions)).toBe('dr-4')
    expect(rowDecision('decide:question:s1/q1', sessions)).toBe('dr-1')
    expect(rowDecision('decide:question:s1/q0', sessions)).toBeUndefined()
    expect(rowDecision('decide:question:s2/q2', sessions)).toBeUndefined()
    expect(rowDecision('session:s1', sessions)).toBeUndefined()
    expect(rowDecision('decision:nope', sessions)).toBeUndefined()
  })

  // The overlap with the decision page (#684): its "Needs your decision" group lists a row per inbox item, the question that mirrors a
  // decision among them; Needs you's "Answer dr-1" stands for it, and the page's row of it goes.
  it("keeps the first row of each decision across the groups and drops every later one, the decision page's among them", () => {
    const groups = [
      { id: 'needs', rows: [{ key: 'session:s2' }, { key: 'decision:dr-1' }, { key: 'decision:dr-2' }] },
      { id: 'running', rows: [{ key: 'session:s1' }] },
      { id: 'decide', rows: [{ key: 'decide:question:s1/q1' }, { key: 'decide:question:s2/q2' }, { key: 'decide:plan:DRG-1/1' }] },
    ]
    const out = oneRowPerDecision(groups, (k) => rowDecision(k, sessions))
    expect(out.map((g) => [g.id, g.rows.map((r) => r.key)])).toEqual([
      ['needs', ['session:s2', 'decision:dr-1', 'decision:dr-2']],
      ['running', ['session:s1']],
      ['decide', ['decide:question:s2/q2', 'decide:plan:DRG-1/1']],
    ])
  })

  it("names the session a mirrored decision's row stands for", () => {
    const rows = answerRows([decision({ id: 'dr-1' })], () => {}, (id) => (id === 'dr-1' ? 'build' : undefined))
    expect(rows[0]!.detail).toBe("asked by build's attempt 1 in build")
  })
})
