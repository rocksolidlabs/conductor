import { describe, expect, it } from 'vitest'
import {
  DecisionRefused,
  answerRows,
  askForMore,
  cancelDecision,
  commentDecision,
  decisionWords,
  optionsOf,
  recommended,
  refusalOf,
  respondDecision,
  responseBody,
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

  it('counts each open decision once: none a shown question mirrors, none that is not open', () => {
    expect(unmirrored([gate, asked, done], sessions).map((d) => d.id)).toEqual(['dr-2'])
    // A mirror set aside while its agent works shows no question: the decision is counted on its own.
    expect(unmirrored([asked], [{ attention: { state: 'working', decision: 'dr-1' } }]).map((d) => d.id)).toEqual(['dr-1'])
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
