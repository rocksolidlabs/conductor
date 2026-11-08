import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { encodeControl, encodeJSONFrame, FrameType, MAX_SUBMIT, parseJSON, type FileHeader, type FileResponse, type Welcome } from '../protocol'
import { ReplyError } from '../reply'
import { ANSWER_TIMEOUT_MS, BaseTransport, DECIDE_TIMEOUT_MS, LANE_READ_TIMEOUT_MS } from './base'
import { LaneError } from './decide'

/** A transport whose wire is a list: what it sends is kept, and a test hands it frames from the other end. */
class TestTransport extends BaseTransport {
  sent: Uint8Array[] = []
  constructor() {
    super('ws')
    this.state.value = 'open'
  }
  connect(): Promise<Welcome> {
    return Promise.reject(new Error('not used'))
  }
  protected send(frame: Uint8Array<ArrayBuffer>): void {
    this.sent.push(frame)
  }
  close(): void {
    this.emitClose(1000, 'closed')
  }
  receive(frame: Uint8Array): void {
    this.handleFrame(frame)
  }
  /** The reqId of the n-th frame sent, a FILE_WRITE part or a file_get. */
  reqIdOf(n: number): string {
    const f = this.sent[n]!
    if (f[0] === FrameType.FileWrite) {
      const len = new DataView(f.buffer, f.byteOffset, f.byteLength).getUint32(9)
      return parseJSON<{ reqId: string }>(f.subarray(13, 13 + len))!.reqId
    }
    return parseJSON<{ reqId: string }>(f.subarray(1))!.reqId
  }
}

/** A FILE frame answering reqId: [type][8-byte reqId][uint32 headerLen][header]. */
function fileFrame(header: Partial<FileHeader> & { reqId: string }): Uint8Array {
  const hb = new TextEncoder().encode(JSON.stringify(header))
  const out = new Uint8Array(1 + 8 + 4 + hb.length)
  out[0] = FrameType.File
  out.set(new TextEncoder().encode(header.reqId).subarray(0, 8), 1)
  new DataView(out.buffer).setUint32(9, hb.length)
  out.set(hb, 13)
  return out
}

/** Where a promise stands, without waiting for it. */
function track<T>(p: Promise<T>): { value?: T; error?: Error } {
  const out: { value?: T; error?: Error } = {}
  p.then(
    (v) => (out.value = v),
    (e: Error) => (out.error = e),
  )
  return out
}

describe('BaseTransport.writeFile', () => {
  beforeEach(() => {
    // The transport's timers are the page's (window.setTimeout); in node the global stands for it.
    vi.stubGlobal('window', globalThis)
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('does not give a save up after 20 s: it says it is slow, once, and takes the answer when it comes', async () => {
    const t = new TestTransport()
    const onSlow = vi.fn()
    const save = track(t.writeFile('a.txt', new TextEncoder().encode('hi'), { baseSha256: 'old', onSlow }))
    expect(t.sent).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(19_999)
    expect(onSlow).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(1)
    expect(onSlow).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(5 * 60_000)
    expect(save).toEqual({})
    t.receive(fileFrame({ reqId: t.reqIdOf(0), path: '/w/a.txt', kind: 'written', sha256: 'new' }))
    await vi.advanceTimersByTimeAsync(0)
    expect(save.value?.header).toMatchObject({ kind: 'written', sha256: 'new' })
    expect(onSlow).toHaveBeenCalledTimes(1)
  })

  it('a save answered in time is never called slow', async () => {
    const t = new TestTransport()
    const onSlow = vi.fn()
    const save = track(t.writeFile('a.txt', new TextEncoder().encode('hi'), { onSlow }))
    t.receive(fileFrame({ reqId: t.reqIdOf(0), path: '/w/a.txt', kind: 'error', error: { code: 'changed_on_disk', message: 'changed' } }))
    await vi.advanceTimersByTimeAsync(60_000)
    expect(save.value?.header.error?.code).toBe('changed_on_disk')
    expect(onSlow).not.toHaveBeenCalled()
  })

  it('a connection that closes ends a save still waiting', async () => {
    const t = new TestTransport()
    const save = track(t.writeFile('a.txt', new TextEncoder().encode('hi')))
    await vi.advanceTimersByTimeAsync(30_000)
    t.close()
    await vi.advanceTimersByTimeAsync(0)
    expect(save.error?.message).toBe('connection closed')
  })

  it('a save whose part the owner could not read is refused at once, sends nothing, and the next save goes', async () => {
    const t = new TestTransport()
    // A path of 4026 bytes: with the hash and the rest, the part's header passes 4 KiB.
    const long = '/w/' + 'd'.repeat(4023)
    const refused = track(t.writeFile(long, new TextEncoder().encode('hi'), { baseSha256: 'a'.repeat(64) }))
    await vi.advanceTimersByTimeAsync(0)
    expect(refused.error?.message).toBe('the path is too long to save from the editor')
    expect(t.sent).toHaveLength(0)
    const next = track(t.writeFile('a.txt', new TextEncoder().encode('hi'), { baseSha256: 'a'.repeat(64) }))
    expect(t.sent).toHaveLength(1)
    t.receive(fileFrame({ reqId: t.reqIdOf(0), path: '/w/a.txt', kind: 'written', sha256: 'new' }))
    await vi.advanceTimersByTimeAsync(0)
    expect(next.value?.header.kind).toBe('written')
  })

  it('a save whose header fits 4 KiB is sent whole', async () => {
    const t = new TestTransport()
    track(t.writeFile('/w/' + 'd'.repeat(3800), new TextEncoder().encode('hi'), { baseSha256: 'a'.repeat(64) }))
    expect(t.sent).toHaveLength(1)
  })

  it('a file request still gives up after 20 s', async () => {
    const t = new TestTransport()
    const read = track<FileResponse>(t.requestFile('a.txt'))
    await vi.advanceTimersByTimeAsync(20_000)
    expect(read.error?.message).toBe('file request timed out')
  })
})

describe('BaseTransport.answer', () => {
  beforeEach(() => {
    vi.stubGlobal('window', globalThis)
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  /** A transport welcomed by an owner that takes answers (`answer`), or an older one. */
  function welcomed(answer: boolean): TestTransport {
    const t = new TestTransport()
    t.receive(encodeControl({ t: 'welcome', proto: 1, sessionId: 's', role: 'control', cols: 80, rows: 24, status: 'running', scrollbackBytes: 0, transport: 'ws', fileView: true, ...(answer ? { answer: true } : {}) }))
    return t
  }
  function sentControl(t: TestTransport, n: number): Record<string, unknown> {
    const f = t.sent[n]!
    expect(f[0]).toBe(FrameType.Control)
    return parseJSON<Record<string, unknown>>(f.subarray(1))!
  }

  it('names the question to an owner that takes answers, and settles true on its answer_ok', async () => {
    const t = welcomed(true)
    const seen = vi.fn()
    t.onControl(seen)
    const sent = track(t.answer({ question: '0123456789abcdef', input: 'y' }))
    const msg = sentControl(t, 0)
    expect(msg).toMatchObject({ t: 'answer', question: '0123456789abcdef', input: 'y' })
    expect(msg.text).toBeUndefined()
    t.receive(encodeControl({ t: 'answer_ok', requestId: msg.reqId as string }))
    await vi.advanceTimersByTimeAsync(0)
    expect(sent.value).toBe(true)
    // The word was the answer's own: the page's handlers never see it.
    expect(seen).not.toHaveBeenCalled()
  })

  it('a question answered already is refused with question_answered, the page told nothing else', async () => {
    const t = welcomed(true)
    const seen = vi.fn()
    t.onControl(seen)
    const line = track(t.answer({ question: 'q1', text: 'Postgres' }))
    const msg = sentControl(t, 0)
    expect(msg).toMatchObject({ t: 'answer', question: 'q1', text: 'Postgres' })
    t.receive(encodeControl({ t: 'error', code: 'question_answered', message: 'this question was already answered: nothing was sent', requestId: msg.reqId as string }))
    await vi.advanceTimersByTimeAsync(0)
    expect(line.error).toBeInstanceOf(ReplyError)
    expect((line.error as ReplyError).code).toBe('question_answered')
    expect(seen).not.toHaveBeenCalled()
    // An error naming no answer of this transport goes to the page as before.
    t.receive(encodeControl({ t: 'error', code: 'not_sent', message: 'x', requestId: 'chat-nonce' }))
    expect(seen).toHaveBeenCalledTimes(1)
  })

  it('to an older owner goes as keys or a submit and settles false at once', async () => {
    const old = welcomed(false)
    const keys = track(old.answer({ question: 'q1', input: 'y' }))
    const line = track(old.answer({ text: 'Postgres' }))
    await vi.advanceTimersByTimeAsync(0)
    expect(keys.value).toBe(false)
    expect(line.value).toBe(false)
    expect(old.sent[0]![0]).toBe(FrameType.Input)
    expect(new TextDecoder().decode(old.sent[0]!.subarray(1))).toBe('y')
    expect(sentControl(old, 1)).toEqual({ t: 'submit', text: 'Postgres' })
  })

  it('to an owner that takes answers, a reply that names no question is not sent: unguarded keys could land on another', async () => {
    const t = welcomed(true)
    const plain = track(t.answer({ input: '1' }))
    const unnamed = track(t.answer({ question: 'not an id', text: 'Postgres' }))
    await vi.advanceTimersByTimeAsync(0)
    expect((plain.error as ReplyError).code).toBe('no_question')
    expect((unnamed.error as ReplyError).code).toBe('no_question')
    expect(t.sent).toHaveLength(0)
  })

  it('a connection that closes, or no word in time, ends the wait with a ReplyError; one not open refuses at once', async () => {
    const t = welcomed(true)
    const closed = track(t.answer({ question: 'q1', input: 'y' }))
    t.close()
    await vi.advanceTimersByTimeAsync(0)
    expect((closed.error as ReplyError).code).toBe('closed')
    const notOpen = track(t.answer({ question: 'q1', input: 'y' }))
    await vi.advanceTimersByTimeAsync(0)
    expect((notOpen.error as ReplyError).code).toBe('not_connected')
    const slow = welcomed(true)
    const late = track(slow.answer({ question: 'q1', input: 'y' }))
    await vi.advanceTimersByTimeAsync(ANSWER_TIMEOUT_MS)
    expect((late.error as ReplyError).code).toBe('no_word')
  })
})

describe('BaseTransport.submitAcked (#322)', () => {
  beforeEach(() => {
    vi.stubGlobal('window', globalThis)
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })
  function welcomedBy(submitAck: boolean): TestTransport {
    const t = new TestTransport()
    t.receive(encodeControl({ t: 'welcome', proto: 1, sessionId: 's', role: 'control', cols: 80, rows: 24, status: 'running', scrollbackBytes: 0, transport: 'ws', fileView: true, answer: true, ...(submitAck ? { submitAck: true } : {}) }))
    return t
  }
  function control(t: TestTransport, n: number): Record<string, unknown> {
    const f = t.sent[n]!
    expect(f[0]).toBe(FrameType.Control)
    return parseJSON<Record<string, unknown>>(f.subarray(1))!
  }

  it('names its request and the guard to an owner that acknowledges, and settles true on submit_ok, which the page never sees', async () => {
    const t = welcomedBy(true)
    const seen = vi.fn()
    t.onControl(seen)
    const sent = track(t.submitAcked('looks good', { unlessWaiting: true }))
    const msg = control(t, 0)
    expect(msg).toMatchObject({ t: 'submit', text: 'looks good', unlessWaiting: true })
    expect(typeof msg.reqId).toBe('string')
    t.receive(encodeControl({ t: 'submit_ok', requestId: msg.reqId as string }))
    await vi.advanceTimersByTimeAsync(0)
    expect(sent.value).toBe(true)
    expect(seen).not.toHaveBeenCalled()
  })

  it('a refusal naming the request is a ReplyError with the owner’s code; no word in time is no_word', async () => {
    const t = welcomedBy(true)
    const held = track(t.submitAcked('x', { unlessWaiting: true }))
    const msg = control(t, 0)
    t.receive(encodeControl({ t: 'error', code: 'not_sent', message: 'the agent asks a question on its screen', requestId: msg.reqId as string }))
    await vi.advanceTimersByTimeAsync(0)
    expect(held.error).toBeInstanceOf(ReplyError)
    expect((held.error as ReplyError).code).toBe('not_sent')
    const late = track(t.submitAcked('y'))
    expect(control(t, 1).unlessWaiting).toBeUndefined()
    await vi.advanceTimersByTimeAsync(ANSWER_TIMEOUT_MS + 1)
    expect((late.error as ReplyError).code).toBe('no_word')
  })

  it('to an older owner goes as a plain submit and settles false at once', async () => {
    const old = welcomedBy(false)
    const sent = track(old.submitAcked('plain', { unlessWaiting: true }))
    await vi.advanceTimersByTimeAsync(0)
    expect(sent.value).toBe(false)
    expect(control(old, 0)).toEqual({ t: 'submit', text: 'plain' })
  })
})

// A decision link's lane over the transport (docs/plans/decision-inbox.md, PR-7; docs/protocol.md, Decision links): reads as controls
// naming a reqId, settled by the DECISION frame naming it (fragmented by CHUNK on a data channel); a decide as a DECIDE frame,
// settled by decide_ok; a decide_refused naming either refuses it with the owner's code; and only to an owner whose welcome said
// decisions.
describe('BaseTransport, a decision link\'s lane', () => {
  beforeEach(() => {
    vi.stubGlobal('window', globalThis)
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })
  function welcomedBy(decisions: boolean): TestTransport {
    const t = new TestTransport()
    t.receive(encodeControl({ t: 'welcome', proto: 1, sessionId: '', role: 'control', cols: 0, rows: 0, status: '', scrollbackBytes: 0, transport: 'ws', fileView: false, ...(decisions ? { decisions: true } : {}) }))
    return t
  }
  function sentJSON(t: TestTransport, n: number): { type: number; body: Record<string, unknown> } {
    const f = t.sent[n]!
    return { type: f[0]!, body: parseJSON<Record<string, unknown>>(f.subarray(1))! }
  }

  it('a read goes as a control naming its reqId and is settled by the DECISION frame naming it, whole or in chunks', async () => {
    const t = welcomedBy(true)
    expect(t.decisions).toBe(true)
    const list = track(t.laneRequest({ t: 'decisions_list' }))
    const read = track(t.laneRequest({ t: 'decision_read', ref: 'task:run-1/build' }))
    const a = sentJSON(t, 0)
    const b = sentJSON(t, 1)
    expect(a).toMatchObject({ type: FrameType.Control, body: { t: 'decisions_list' } })
    expect(b.body).toMatchObject({ t: 'decision_read', ref: 'task:run-1/build' })
    expect(a.body.reqId).not.toBe(b.body.reqId)
    // The second's reply first: each settles its own.
    t.receive(encodeJSONFrame(FrameType.Decision, { reqId: b.body.reqId, item: { ref: 'task:run-1/build' } }))
    await vi.advanceTimersByTimeAsync(0)
    expect(read.value).toMatchObject({ item: { ref: 'task:run-1/build' } })
    expect(list).toEqual({})
    // The first's reply in two chunks, as a data channel carries a frame past its message size.
    const whole = encodeJSONFrame(FrameType.Decision, { reqId: a.body.reqId, list: { items: [], total: 0, at: 'now' } })
    const half = Math.floor(whole.length / 2)
    for (const [off, part] of [
      [0, whole.subarray(0, half)],
      [half, whole.subarray(half)],
    ] as const) {
      const c = new Uint8Array(1 + 10 + part.length)
      c[0] = FrameType.Chunk
      const v = new DataView(c.buffer)
      v.setUint16(1, 7)
      v.setUint32(3, whole.length)
      v.setUint32(7, off)
      c.set(part, 11)
      t.receive(c)
    }
    await vi.advanceTimersByTimeAsync(0)
    expect(list.value).toMatchObject({ list: { total: 0 } })
  })

  it('a decide goes as a DECIDE frame, a line of any permitted text whole, and is settled by its decide_ok, which goes no further', async () => {
    const t = welcomedBy(true)
    const seen = vi.fn()
    t.onControl(seen)
    const text = '"'.repeat(MAX_SUBMIT)
    const ok = track(t.decide({ ref: 'question:s/q', action: 'answer', revision: 'q', text, opId: 'op-1' }))
    const d = sentJSON(t, 0)
    expect(d.type).toBe(FrameType.Decide)
    expect(d.body).toMatchObject({ ref: 'question:s/q', action: 'answer', revision: 'q', opId: 'op-1' })
    expect(d.body.text).toBe(text)
    t.receive(encodeControl({ t: 'decide_ok', reqId: d.body.reqId as string, ref: 'question:s/q', receipt: 'q', repeat: true }))
    await vi.advanceTimersByTimeAsync(0)
    expect(ok.value).toMatchObject({ receipt: 'q', repeat: true })
    expect(seen).not.toHaveBeenCalled()
    // decisions_changed is the page's: it goes on to the listeners.
    t.receive(encodeControl({ t: 'decisions_changed', seq: 3 }))
    expect(seen).toHaveBeenCalledWith({ t: 'decisions_changed', seq: 3 })
  })

  it('a decide_refused naming a request refuses it with the owner\'s code, its words and its kept receipt', async () => {
    const t = welcomedBy(true)
    const done = track(t.decide({ ref: 'task:r/build', action: 'retry', revision: '12', opId: 'op-2' }))
    const read = track(t.laneRequest({ t: 'decision_evidence', ref: 'task:r/build', evidence: 'ticket:CND-1' }))
    t.receive(encodeControl({ t: 'decide_refused', reqId: sentJSON(t, 0).body.reqId as string, code: 'stale_revision', message: 'the run moved on', ref: 'task:r/build', receipt: 'op-2' }))
    t.receive(encodeControl({ t: 'decide_refused', reqId: sentJSON(t, 1).body.reqId as string, code: 'unsupported', message: 'open it on the desktop or the LAN' }))
    await vi.advanceTimersByTimeAsync(0)
    expect(done.error).toBeInstanceOf(LaneError)
    expect(done.error).toMatchObject({ code: 'stale_revision', message: 'the run moved on', extra: { ref: 'task:r/build', receipt: 'op-2' } })
    expect(read.error).toMatchObject({ code: 'unsupported' })
  })

  it('nothing is sent to an owner whose welcome did not say decisions, nor past the bounds; a close or no reply in time ends the wait', async () => {
    const old = welcomedBy(false)
    expect(old.decisions).toBe(false)
    const refused = track(old.laneRequest({ t: 'decisions_list' }))
    const notSent = track(old.decide({ ref: 'r', action: 'retry', revision: '1', opId: 'x' }))
    await vi.advanceTimersByTimeAsync(0)
    expect(refused.error).toMatchObject({ code: 'not_sent' })
    expect(notSent.error).toMatchObject({ code: 'not_sent' })
    expect(old.sent).toHaveLength(0)

    const t = welcomedBy(true)
    const big = track(t.laneRequest({ t: 'decision_read', ref: 'r'.repeat(513) }))
    const badOp = track(t.decide({ ref: 'r', action: 'retry', revision: '1', opId: 'not an op id' }))
    await vi.advanceTimersByTimeAsync(0)
    expect(big.error).toMatchObject({ code: 'invalid' })
    expect(badOp.error).toMatchObject({ code: 'invalid' })
    expect(t.sent).toHaveLength(0)

    const slow = track(t.laneRequest({ t: 'decisions_list' }))
    const act = track(t.decide({ ref: 'r', action: 'retry', revision: '1', opId: 'op-3' }))
    await vi.advanceTimersByTimeAsync(LANE_READ_TIMEOUT_MS + 1)
    expect(slow.error).toMatchObject({ code: 'no_word' })
    expect(act).toEqual({})
    t.close()
    await vi.advanceTimersByTimeAsync(0)
    expect(act.error).toMatchObject({ code: 'closed' })
    expect(DECIDE_TIMEOUT_MS).toBeGreaterThan(LANE_READ_TIMEOUT_MS)
  })
})
