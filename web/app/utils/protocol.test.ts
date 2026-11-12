import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import {
  DECISION_PAGE,
  DecideCode,
  FrameType,
  HOST_MAX_DESK_ID,
  HOST_MAX_LOCAL_ID,
  HOST_MAX_RUN_ID,
  HOST_MAX_TOKEN,
  HOST_MAX_TOKEN_MESSAGE,
  MAX_DECIDE,
  MAX_DECIDE_IN_FLIGHT,
  MAX_DECISION,
  MAX_DECISION_CURSOR,
  MAX_CAPACITY_EVENT,
  MAX_DECISION_EVENT,
  MAX_DECISION_READS_IN_FLIGHT,
  MAX_DECISION_RECEIPT,
  MAX_LANE_REF,
  MAX_RATIONALE,
  MAX_SUBMIT,
  decideValid,
  decisionRequestValid,
  decodeDecision,
  decodeFrame,
  encodeDecide,
  encodeJSONFrame,
  goJSONLength,
  hostDeskLocalId,
  parseJSON,
  type DecideMsg,
  type DecideOkMsg,
  type DecideRefusedMsg,
  type CapacityEvent,
  type DecisionEvent,
  type DecisionsChangedMsg,
  type HostRegisterDeskMessage,
  type HostRegisteredRenews,
  type HostRunGroup,
  type HostTokenMsg,
  type Welcome,
} from './protocol'

// host_token (internal/proto HostTokenMsg, ParseHostToken; docs/protocol.md, Host control connection): a token at its bound,
// of token68's characters, which JSON never escapes, fits the message's bound; registered's renews is optional.
describe('host_token', () => {
  it('fits a token at its bound in the message', () => {
    expect(HOST_MAX_TOKEN).toBe(2048)
    expect(HOST_MAX_TOKEN_MESSAGE).toBe(2560)
    const m: HostTokenMsg = { t: 'host_token', token: 'Az09-._~+/='.padEnd(HOST_MAX_TOKEN, 'x') }
    expect(m.token).toHaveLength(HOST_MAX_TOKEN)
    expect(new TextEncoder().encode(JSON.stringify(m)).length).toBeLessThanOrEqual(HOST_MAX_TOKEN_MESSAGE)
    const plain: HostRegisteredRenews = {}
    const renews: HostRegisteredRenews = { renews: true }
    expect(plain.renews).toBeUndefined()
    expect(JSON.parse(JSON.stringify(renews)).renews).toBe(true)
  })
})

// The decision lane's mirror of internal/proto/decisions.go (docs/protocol.md, Decision links): each message within the bounds the
// owner checks, each refused past them, and the committed fixture read equal to Go's values.

/** The fixture's values, built as decisions_test.go builds them: every field at its bound, its texts escaping. */
function maxDecide(): DecideMsg {
  return {
    reqId: 'r0123456789abcdef0123456789abcde',
    ref: 'question:' + '<'.repeat(503),
    action: 'a'.repeat(32),
    revision: '&'.repeat(64),
    ticketRev: 9007199254740991,
    input: '\x01'.repeat(47) + '\r',
    choice: 5,
    text: '"'.repeat(MAX_SUBMIT),
    rationale: '<'.repeat(341),
    opId: 'op-' + 'x'.repeat(28) + '~',
  }
}

describe('the decision lane', () => {
  it('numbers its frames and bounds them as the owner does', () => {
    expect(FrameType.Decision).toBe(0x09)
    expect(FrameType.Decide).toBe(0x0a)
    expect(MAX_DECISION).toBe(128 * 1024)
    expect(MAX_DECIDE).toBe(32 * 1024)
    expect(DECISION_PAGE).toBe(25)
    expect(MAX_DECIDE_IN_FLIGHT).toBe(4)
    expect(MAX_DECISION_READS_IN_FLIGHT).toBe(4)
  })

  it('reads the committed fixture equal, and encodes the same values under the bound', () => {
    const raw = readFileSync(fileURLToPath(new URL('../../../internal/proto/testdata/decide-max.json', import.meta.url)), 'utf8')
    const got = JSON.parse(raw) as DecideMsg
    expect(got).toEqual(maxDecide())
    expect(decideValid(got)).toBe(true)
    expect(goJSONLength(got.input!)).toBe(284)
    expect(goJSONLength(got.rationale!)).toBe(2046)
    const frame = encodeDecide(maxDecide())
    expect(frame).not.toBeNull()
    expect(frame![0]).toBe(FrameType.Decide)
    expect(frame!.length - 1).toBeLessThanOrEqual(MAX_DECIDE)
    expect(parseJSON<DecideMsg>(decodeFrame(frame!)!.payload)).toEqual(maxDecide())
  })

  it('refuses a decide past any of its bounds', () => {
    const past: Array<(m: DecideMsg) => void> = [
      (m) => { m.reqId += 'x' },
      (m) => { m.reqId = '' },
      (m) => { m.opId += 'x' },
      (m) => { m.opId = 'a/b' },
      (m) => { m.ref += 'x' },
      (m) => { m.action += 'x' },
      (m) => { m.revision += 'x' },
      (m) => { m.input += 'x' },
      (m) => { m.text += 'x' },
      (m) => { m.rationale += '<' },
      (m) => { m.choice = 6 },
      (m) => { m.choice = -1 },
      (m) => { m.ticketRev = -1 },
    ]
    for (const mutate of past) {
      const m = maxDecide()
      mutate(m)
      expect(decideValid(m)).toBe(false)
      expect(encodeDecide(m)).toBeNull()
    }
    expect(goJSONLength('\b\f\n\r\t"\\')).toBe(14)
    expect(goJSONLength(' <é')).toBe(14)
  })

  it('bounds every read as the owner does', () => {
    expect(decisionRequestValid({ t: 'decisions_list', reqId: 'r1' })).toBe(true)
    expect(decisionRequestValid({ t: 'decisions_list', reqId: 'r1', after: 'a'.repeat(MAX_DECISION_CURSOR) })).toBe(true)
    expect(decisionRequestValid({ t: 'decisions_list', reqId: 'r1', after: 'a'.repeat(MAX_DECISION_CURSOR + 1) })).toBe(false)
    expect(decisionRequestValid({ t: 'decisions_list', reqId: 'r 1' })).toBe(false)
    expect(decisionRequestValid({ t: 'decision_read', reqId: 'r1', ref: 'q'.repeat(MAX_LANE_REF) })).toBe(true)
    expect(decisionRequestValid({ t: 'decision_read', reqId: 'r1', ref: 'q'.repeat(MAX_LANE_REF + 1) })).toBe(false)
    expect(decisionRequestValid({ t: 'decision_track', reqId: 'r1', ref: 'task:a/b', receipt: 'c'.repeat(MAX_DECISION_RECEIPT), attempt: 'op-1' })).toBe(true)
    expect(decisionRequestValid({ t: 'decision_track', reqId: 'r1', ref: 'task:a/b', receipt: 'c'.repeat(MAX_DECISION_RECEIPT + 1), attempt: 'op-1' })).toBe(false)
    expect(decisionRequestValid({ t: 'decision_track', reqId: 'r1', ref: 'task:a/b', receipt: 'c', attempt: 'a b' })).toBe(false)
    expect(decisionRequestValid({ t: 'decision_record', reqId: 'r1', ref: 'decision:d1', part: 'head' })).toBe(true)
    expect(decisionRequestValid({ t: 'decision_record', reqId: 'r1', ref: 'decision:d1', part: 'history', rev: '9'.repeat(20) })).toBe(true)
    expect(decisionRequestValid({ t: 'decision_record', reqId: 'r1', ref: 'decision:d1', part: 'responses' })).toBe(false)
    expect(decisionRequestValid({ t: 'decision_record', reqId: 'r1', ref: 'decision:d1', part: 'history', rev: '9'.repeat(21) })).toBe(false)
    expect(decisionRequestValid({ t: 'decision_evidence', reqId: 'r1', ref: 'task:a/b', evidence: 'check:a/b/1/test/1' })).toBe(true)
    expect(decisionRequestValid({ t: 'decision_evidence', reqId: 'r1', ref: 'task:a/b', evidence: '' })).toBe(false)
  })

  it('reads a decision frame naming its request, within its bound', () => {
    const enc = new TextEncoder()
    expect(decodeDecision(enc.encode(JSON.stringify({ reqId: 'r1', list: { items: [], total: 0 } })))).toEqual({ reqId: 'r1', list: { items: [], total: 0 } })
    expect(decodeDecision(enc.encode('{"list":{}}'))).toBeNull()
    expect(decodeDecision(enc.encode('not json'))).toBeNull()
    expect(decodeDecision(new Uint8Array(MAX_DECISION + 1))).toBeNull()
  })

  it('carries the owner\'s replies and the welcome\'s word', () => {
    const ok: DecideOkMsg = { t: 'decide_ok', reqId: 'r1', ref: 'task:a/b', receipt: 'op-1', repeat: true }
    const refused: DecideRefusedMsg = { t: 'decide_refused', reqId: 'r1', code: DecideCode.StaleRevision, message: 'the run moved on', ref: 'task:a/b', receipt: 'op-1' }
    const changed: DecisionsChangedMsg = { t: 'decisions_changed', seq: 3 }
    for (const m of [ok, refused, changed]) {
      expect(parseJSON(decodeFrame(encodeJSONFrame(FrameType.Control, m))!.payload)).toEqual(m)
    }
    expect(Object.values(DecideCode)).toEqual(['stale_revision', 'not_in_scope', 'not_decision_link', 'not_found', 'question_answered', 'too_many_requests',
      'unsupported', 'invalid', 'record_moved', 'revoked', 'expired', 'op_conflict'])
    const welcome: Partial<Welcome> = { t: 'welcome', decisions: true }
    expect(welcome.decisions).toBe(true)
    expect(MAX_RATIONALE).toBe(2048)
  })
})

// The `decision` event's data (docs/protocol.md "Decisions"): its largest, every field at its longest as the server writes it,
// fits the bound the server holds it to (internal/api, maxDecisionEvent, TestDecisionEventIsBounded).
describe('the decision event', () => {
  it('fits its bound at its largest', () => {
    const largest: DecisionEvent = {
      id: 'dr-9223372036854775807',
      run: `${'c'.repeat(64)}-0123abcd`,
      task: 't'.repeat(40),
      state: 'superseded',
      rev: 2 ** 31 - 1,
    }
    expect(new TextEncoder().encode(JSON.stringify(largest)).length).toBeLessThanOrEqual(MAX_DECISION_EVENT)
    expect(MAX_DECISION_EVENT).toBe(256)
  })
})

// The `capacity` event's data (docs/protocol.md "Capacity"): its largest, an agent id of 32 bytes, an account id, the longest
// state and a time with nanoseconds, fits the bound the server holds it to (internal/api, maxCapacityEvent,
// TestCapacityEventBounded).
describe('the capacity event', () => {
  it('fits its bound at its largest', () => {
    const largest: CapacityEvent = {
      agent: 'a'.repeat(32),
      account: 'a-abcdefghijkl',
      state: 'unqualified',
      readAt: '2026-11-10T23:59:59.999999999+14:00',
    }
    expect(new TextEncoder().encode(JSON.stringify(largest)).length).toBeLessThanOrEqual(MAX_CAPACITY_EVENT)
    expect(MAX_CAPACITY_EVENT).toBe(512)
  })
})

// A run's desk (internal/proto RegisterDesk, RunGroup.Desk; docs/protocol.md, A run's desk): its local id is the run's, and
// a group may name it with no member yet.
describe('a run desk', () => {
  it('registers under the local id of its run and is named by its group', () => {
    const runId = 'r'.repeat(HOST_MAX_RUN_ID)
    const m: HostRegisterDeskMessage = { t: 'register_desk', proto: 1, host: { name: 'box', version: 'v', instance: 'i' }, localId: hostDeskLocalId(runId), run: { id: runId, name: 'Run' } }
    expect(m.localId).toBe(`desk:${runId}`)
    // The desk's local id is checked as the run's, not against a session's local id bound, which it may pass.
    expect(m.localId.length).toBeGreaterThan(HOST_MAX_LOCAL_ID)
    const g: HostRunGroup = { id: runId, name: 'Run', members: [], desk: 'd'.repeat(HOST_MAX_DESK_ID) }
    expect(JSON.parse(JSON.stringify(g)).desk).toHaveLength(64)
  })
})
