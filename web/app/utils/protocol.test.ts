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
  MAX_DECIDE,
  MAX_DECIDE_IN_FLIGHT,
  MAX_DECISION,
  MAX_DECISION_CURSOR,
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
  type DecisionEvent,
  type DecisionsChangedMsg,
  type HostRegisterDeskMessage,
  type HostRunGroup,
  type Welcome,
} from './protocol'

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

// The preview bridge's messages (internal/proto/preview.go): both parsers judge one fixture alike. Imported here, at the end, so
// this block stays apart from the lanes' list above.
import { vi } from 'vitest'
import {
  ELEMENT_BOUNDS,
  ELEMENT_CUTS,
  MAX_ELEMENT_REPORT,
  MAX_PREVIEW_REVISION,
  MAX_PREVIEW_SELECTOR,
  MAX_PREVIEW_TITLE,
  MAX_PREVIEW_TO_BRIDGE,
  MAX_PREVIEW_TO_WORKBENCH,
  MAX_PREVIEW_URL,
  MAX_PREVIEW_USER_AGENT,
  PREVIEW_HINT_KINDS,
  measuredBytes,
  parsePreviewMessage,
  previewMeasure,
  type PreviewDirection,
} from './protocol'

describe('the preview bridge', () => {
  const fixture = JSON.parse(readFileSync(fileURLToPath(new URL('../../../internal/proto/testdata/preview-messages.json', import.meta.url)), 'utf8')) as {
    bounds: Record<string, number>
    cuts: string[]
    hintKinds: string[]
    measures: Array<{ name: string; units: number[]; bytes: number }>
    messages: Array<{ name: string; to: PreviewDirection; nonce: string; message: unknown; ok: boolean }>
  }
  const nonce = 'n0nce-of-the-hello-1234'
  // A message of the fixture by its name, as a structured clone of it: what postMessage hands a listener.
  const named = (name: string) => {
    const c = fixture.messages.find((m) => m.name === name)
    if (!c) throw new Error(`no case ${name}`)
    return structuredClone(c.message) as Record<string, unknown> & { element: Record<string, unknown> }
  }

  it('holds the bounds Go holds, each by its name in preview.go', () => {
    const B = ELEMENT_BOUNDS
    const ours: Record<string, number> = {
      MaxPreviewToBridge: MAX_PREVIEW_TO_BRIDGE,
      MaxPreviewToWorkbench: MAX_PREVIEW_TO_WORKBENCH,
      MaxElementReport: MAX_ELEMENT_REPORT,
      MaxPreviewSelector: MAX_PREVIEW_SELECTOR,
      MaxPreviewURL: MAX_PREVIEW_URL,
      MaxPreviewTitle: MAX_PREVIEW_TITLE,
      MaxPreviewUserAgent: MAX_PREVIEW_USER_AGENT,
      MaxPreviewRevision: MAX_PREVIEW_REVISION,
      MaxElementName: B.name,
      MaxElementText: B.text,
      MaxElementSelector: B.selector,
      MaxElementAttributes: B.attributes,
      MaxElementAttributeName: B.attributeName,
      MaxElementAttributeValue: B.attributeValue,
      MaxElementHTML: B.html,
      MaxNeighbourhood: B.neighbourhood,
      MaxAncestors: B.ancestors,
      MaxAncestor: B.ancestor,
      MaxStyles: B.styles,
      MaxStyleName: B.styleName,
      MaxStyleValue: B.styleValue,
      MaxHints: B.hints,
      MaxHintFile: B.hintFile,
      MaxHintComponent: B.hintComponent,
    }
    expect(ours).toEqual(fixture.bounds)
    // Every one of the element's bounds is one of them.
    expect(Object.values(B).length).toBe(16)
    expect([...ELEMENT_CUTS]).toEqual(fixture.cuts)
    expect([...PREVIEW_HINT_KINDS]).toEqual(fixture.hintKinds)
  })

  it('gives every message of the fixture the verdict Go gives it, the message as text read and as a structured clone', () => {
    expect(fixture.messages.length).toBeGreaterThan(200)
    for (const c of fixture.messages) {
      expect({ name: c.name, ok: parsePreviewMessage(c.message, c.to, c.nonce) !== null }).toEqual({ name: c.name, ok: c.ok })
      expect({ name: c.name, ok: parsePreviewMessage(structuredClone(c.message), c.to, c.nonce) !== null }).toEqual({ name: c.name, ok: c.ok })
    }
  })

  it('measures as Go writes: <, & and > as they are, U+2028 in six bytes, a lone surrogate as U+FFFD, a negative zero as 0', () => {
    for (const m of fixture.measures) expect({ name: m.name, bytes: measuredBytes(String.fromCharCode(...m.units)) }).toEqual({ name: m.name, bytes: m.bytes })
    expect(previewMeasure({ a: '<&>' })).toBe(11)
    expect([previewMeasure(-0), previewMeasure({ x: -0 }), previewMeasure([-0, 0])]).toEqual([1, 7, 5])
    expect(previewMeasure({ a: [1.5, true, null, 'é'] })).toBe(JSON.stringify({ a: [1.5, true, null, 'é'] }).length + 1)
  })

  // A page posts a value, not text: whatever a structured clone carries, the parser answers null for what it does not certify,
  // and never throws.
  it('answers null, never an exception, for a value JSON does not hold: a big integer, a cycle, nesting past a message, another kind of object', () => {
    const cases: Record<string, unknown> = {}
    const select = named('select')
    cases['a big integer beside the members'] = { ...select, x: 1n }
    cases['a big integer as a selector'] = { ...named('the describe\'s selector at its bound'), selector: 1n }
    cases['a big integer as an epoch'] = { ...named('ready'), epoch: 1n }
    const loop: Record<string, unknown> = { ...select }
    loop.x = loop
    cases['a cycle beside the members'] = loop
    const selfish: Record<string, unknown> = {}
    selfish.self = selfish
    cases['a cycle as a selector'] = { ...named('the describe\'s selector at its bound'), selector: selfish }
    const sel = named('selected')
    cases['a cycle in an element'] = { ...sel, element: { ...sel.element, box: selfish } }
    let deep: unknown = 'x'
    for (let i = 0; i < 2000; i++) deep = [deep] // as deep as a structured clone goes here; any past four is refused
    cases['nesting beside the members'] = { ...select, x: deep }
    cases['nesting as a selector'] = { ...named('the describe\'s selector at its bound'), selector: deep }
    cases['nesting in an element'] = { ...sel, element: { ...sel.element, hints: [deep] } }
    cases['a date as a URL'] = { ...named('ready'), url: new Date(0) }
    cases['a map as a viewport'] = { ...named('ready'), viewport: new Map([['width', 1]]) }
    cases['a string object as a selector'] = { ...named('the describe\'s selector at its bound'), selector: new String('#x') }
    for (const [name, v] of Object.entries(cases)) {
      const cloned = structuredClone(v)
      const to: PreviewDirection = ['select', 'describe'].includes((cloned as { t: string }).t) ? 'bridge' : 'workbench'
      expect({ name, got: parsePreviewMessage(cloned, to, nonce) }).toEqual({ name, got: null })
    }
  })

  it('answers null for a list a structured clone keeps with holes or with a named member, which JSON would not carry', () => {
    const sel = named('selected')
    const holes = (n: number, ...items: unknown[]) => {
      const a = new Array(n)
      items.forEach((x, i) => (a[i] = x))
      return a
    }
    const withMember = (items: unknown[], k: string, v: unknown) => Object.assign([...items], { [k]: v })
    const pair = sel.element.attributes as unknown[]
    const cases: Record<string, unknown> = {
      'attributes of holes': { ...sel, element: { ...sel.element, attributes: holes(32) } },
      'an attribute with a hole': { ...sel, element: { ...sel.element, attributes: [holes(2, 'id')] } },
      'ancestors of holes': { ...sel, element: { ...sel.element, ancestors: holes(12) } },
      'styles of one pair and holes': { ...sel, element: { ...sel.element, styles: holes(64, ['display', 'block']) } },
      'hints of holes': { ...sel, element: { ...sel.element, hints: holes(8) } },
      'a cut of holes': { ...sel, element: { ...sel.element, cut: holes(10) } },
      'attributes with a named member': { ...sel, element: { ...sel.element, attributes: withMember(pair, 'script', '<img onerror>') } },
      'a cut with a named member': { ...sel, element: { ...sel.element, cut: withMember([], 'x', 'neighbourhood') } },
      'ready hints of a hole': { ...named('ready'), hints: holes(1) },
      'ready hints of vue and a hole': { ...named('ready'), hints: holes(2, 'vue') },
      'ready hints with a named member': { ...named('ready'), hints: withMember(['vue'], 'x', 'react') },
    }
    for (const [name, v] of Object.entries(cases)) {
      const cloned = structuredClone(v)
      expect({ name, got: parsePreviewMessage(cloned, 'workbench', nonce) }).toEqual({ name, got: null })
    }
    // The clone kept them: a hole is no index, a named member is a key.
    const kept = structuredClone(cases['attributes of holes']) as { element: { attributes: unknown[] } }
    expect(kept.element.attributes.length === 32 && !(0 in kept.element.attributes)).toBe(true)
    expect(Object.keys(structuredClone(cases['attributes with a named member']) as object).length).toBeGreaterThan(0)
  })

  // A member 200 MiB long is refused without being read: the parser counts a string's length first, and a member its type does
  // not carry is never measured. The work is counted in the code units read, not timed.
  it('refuses a member of 200 MiB without reading it', () => {
    const huge = 'a'.repeat(200 * 1024 * 1024)
    const read = vi.spyOn(String.prototype, 'charCodeAt')
    let got: unknown[]
    let reads = Infinity
    try {
      got = [
        parsePreviewMessage({ ...named('the describe\'s selector at its bound'), selector: huge }, 'bridge', nonce),
        parsePreviewMessage({ ...named('select'), x: huge }, 'bridge', nonce),
        parsePreviewMessage({ ...named('ready'), url: huge }, 'workbench', nonce),
        parsePreviewMessage({ ...named('selected'), element: { ...named('selected').element, html: huge } }, 'workbench', nonce),
      ]
    } finally {
      reads = read.mock.calls.length
      read.mockRestore()
    }
    expect(got).toEqual([null, null, null, null])
    expect(reads).toBeLessThan(100_000)
  })
})
