import { describe, expect, it } from 'vitest'
import { MAX_TRANSCRIPT_ENTRY, MAX_TRANSCRIPT_PAGE, type TranscriptEntry, type TranscriptReply, type TranscriptRequest } from './protocol'
import { chatBlocks, entryValid, lastWords, limitReads, MAX_ENTRY_TEXT, MAX_KEPT_ENTRIES, promptLine, promptText, readPart, stepsLine, Transcript } from './transcript'

// A chat's transcript as the page keeps it (docs/plans/chat-or-terminal.md, designs 5, 6 and 11): each entry at its latest revision
// by seq, the highest mark held as the next hello's resume point, what is past the wire's bounds refused, a message that answers a
// question shown with no text, and the chat's blocks drawn from them.

const at = '2026-11-25T12:00:00.000Z'
const e = (seq: number, mark: number, o: Partial<TranscriptEntry> = {}): TranscriptEntry => ({ seq, mark, rev: 0, at, kind: 'text', turn: 1, text: `words ${seq}`, ...o })

describe('Transcript', () => {
  it('mergesRevisionsBySeq: an entry is kept at its latest revision, whatever order the revisions come in', () => {
    const t = new Transcript()
    t.take({ t: 'entry', entry: e(1, 1, { kind: 'tool', status: 'running', tool: 'Bash' }) })
    t.take({ t: 'entry', entry: e(2, 2) })
    t.take({ t: 'entry', entry: e(1, 4, { kind: 'tool', status: 'ok', tool: 'Bash', rev: 2 }) })
    // An older revision come late (a replay's page after the live entry) does not take its place.
    t.take({ t: 'page', entries: [e(1, 3, { kind: 'tool', status: 'running', tool: 'Bash', rev: 1 }), e(2, 2)], first: 1, last: 2 })
    expect(t.list().map((x) => [x.seq, x.rev, x.status ?? ''])).toEqual([
      [1, 2, 'ok'],
      [2, 0, ''],
    ])
    expect(t.entries.size).toBe(2)
  })

  it('keepsResumePoint: the highest mark held is the next hello\'s transcriptAfter, and a replay that left changes out moves it only once read back', async () => {
    const t = new Transcript()
    t.take({ t: 'entry', entry: e(1, 5) })
    t.take({ t: 'entry', entry: e(2, 9) })
    expect(t.mark).toBe(9)
    // A replay past its budget (more): the mark stays until what it left out is read back.
    t.take({ t: 'page', entries: [e(30, 40), e(31, 41)], first: 1, last: 31, more: true })
    expect(t.mark).toBe(9)
    expect(t.gap).toBe(30)
    const asked: TranscriptRequest[] = []
    const get = async (req: TranscriptRequest): Promise<TranscriptReply> => {
      asked.push(req)
      return asked.length === 1
        ? { t: 'page', entries: Array.from({ length: 27 }, (_, i) => e(i + 3, 10 + i)), first: 1, last: 31, more: true }
        : { t: 'page', entries: [], first: 1, last: 31, more: false }
    }
    await t.readBack(get)
    expect(asked[0]).toEqual({ before: 30, limit: MAX_TRANSCRIPT_PAGE })
    expect(asked[1]).toEqual({ before: 3, limit: MAX_TRANSCRIPT_PAGE })
    expect(t.gap).toBeNull()
    expect(t.mark).toBe(41)
    // A welcome whose transcript is behind the mark held is a new life of the transcript: started afresh.
    t.welcome({ way: 'protocol', transcript: { first: 1, last: 1, mark: 2 } })
    expect(t.mark).toBe(0)
    expect(t.entries.size).toBe(0)
  })

  it('refusesPastBound: an entry or a page past the wire\'s bounds is refused whole, and what the page keeps is bounded', () => {
    const t = new Transcript()
    expect(t.take({ t: 'entry', entry: e(1, 1, { text: 'x'.repeat(MAX_ENTRY_TEXT) }) })).toBe(true)
    expect(t.take({ t: 'entry', entry: e(2, 2, { text: 'x'.repeat(MAX_ENTRY_TEXT + 1) }) })).toBe(false)
    expect(t.take({ t: 'entry', entry: e(3, 3, { kind: 'later' as never }) })).toBe(false)
    expect(t.take({ t: 'entry', entry: e(0, 3) })).toBe(false)
    expect(t.take({ t: 'entry', entry: e(4, 4, { ref: 'r'.repeat(129) }) })).toBe(false)
    expect(t.take({ t: 'entry', entry: e(5, 5, { kind: 'permission', inputSha256: 'not hex' }) })).toBe(false)
    expect(t.take({ t: 'entry', entry: e(6, 6, { byName: 'n'.repeat(41) }) })).toBe(false)
    // An entry within every field's bound but past the frame's bound for one entry, encoded.
    expect(entryValid(e(7, 7, { kind: 'tool', summary: 'y'.repeat(1024), text: 'z'.repeat(4096), words: 'w'.repeat(500), why: 'v'.repeat(500), path: 'p'.repeat(1024), cwd: 'c'.repeat(1024) }))).toBe(true)
    expect(JSON.stringify(e(7, 7, { kind: 'tool', summary: String.fromCharCode(1).repeat(1024), text: String.fromCharCode(1).repeat(4096) })).length).toBeGreaterThan(MAX_TRANSCRIPT_ENTRY)
    expect(entryValid(e(7, 7, { kind: 'tool', summary: String.fromCharCode(1).repeat(1024), text: String.fromCharCode(1).repeat(4096) }))).toBe(false)
    expect(t.take({ t: 'page', entries: Array.from({ length: MAX_TRANSCRIPT_PAGE + 1 }, (_, i) => e(i + 10, i + 10)), first: 1, last: 200 })).toBe(false)
    expect(t.take({ t: 'page', entries: [e(10, 10), e(11, 11, { text: 'x'.repeat(MAX_ENTRY_TEXT + 1) })], first: 1, last: 11 })).toBe(false)
    expect(t.list().map((x) => x.seq)).toEqual([1])
    // The page keeps at most the ring's entries, the oldest going first, an open request's never.
    const k = new Transcript()
    k.take({ t: 'entry', entry: e(1, 1, { kind: 'permission', state: 'open', question: 'q1' }) })
    for (let i = 2; i <= MAX_KEPT_ENTRIES + 5; i++) k.take({ t: 'entry', entry: e(i, i) })
    expect(k.entries.size).toBe(MAX_KEPT_ENTRIES)
    expect(k.entries.has(1)).toBe(true)
    expect(k.entries.has(2)).toBe(false)
    expect(k.entries.has(MAX_KEPT_ENTRIES + 5)).toBe(true)
  })

  it('promptAnsweringShowsNoText: a message that answers a question shows no text to anyone, whatever it carries', () => {
    expect(promptText(e(1, 1, { kind: 'prompt', by: 'person', text: 'make it so' }))).toBe('make it so')
    expect(promptText(e(2, 2, { kind: 'prompt', by: 'person', answers: 'q-1', text: 'my secret' }))).toBe('')
    expect(promptText(e(3, 3, { kind: 'prompt', by: 'person', answers: 'q-1' }))).toBe('')
    expect(promptLine(e(4, 4, { kind: 'prompt', state: 'queued' }))).toBe('Sends when the turn ends.')
    expect(promptLine(e(5, 5, { kind: 'prompt', state: 'unconfirmed' }))).toBe('Sent, not yet confirmed.')
    expect(promptLine(e(6, 6, { kind: 'prompt', state: 'accepted' }))).toBe('')
  })
})

describe('chatBlocks', () => {
  it('draws a turn with its messages and words in order, its steps apart, a notice of its own, and a turn count begun again as a new turn', () => {
    const entries = [
      e(1, 1, { kind: 'notice', code: 'moved', words: 'Moved to Chat.', turn: 0 }),
      e(2, 2, { kind: 'turn', phase: 'start' }),
      e(3, 3, { kind: 'prompt', by: 'person', text: 'hi' }),
      e(4, 4, { kind: 'tool', tool: 'Bash', status: 'ok' }),
      e(5, 5, { kind: 'text', ref: 'm1', text: 'one ' }),
      e(6, 6, { kind: 'text', ref: 'm1', text: 'two', final: true }),
      e(7, 7, { kind: 'file', path: 'main.go', op: 'edit' }),
      e(8, 8, { kind: 'turn', phase: 'end', outcome: 'done' }),
      e(9, 9, { kind: 'turn', phase: 'start', turn: 1 }),
      e(10, 10, { kind: 'text', ref: 'm2', text: 'again', turn: 1 }),
    ]
    const b = chatBlocks(entries)
    expect(b.map((x) => x.kind)).toEqual(['notice', 'turn', 'turn'])
    const t = b[1]!
    if (t.kind !== 'turn') throw new Error('a turn')
    expect(t.prompts.map((p) => p.text)).toEqual(['hi'])
    expect(t.items.map((i) => i.kind)).toEqual(['text', 'file'])
    expect(t.items[0]).toMatchObject({ text: 'one two', final: true })
    expect(t.steps.map((s) => s.seq)).toEqual([4])
    expect(t.outcome).toBe('done')
    expect(b[2]).toMatchObject({ running: true, outcome: '' })
    expect(stepsLine(b[2] as never)).toBe('Working.')
    expect(lastWords(entries)).toBe('again')
  })
})

describe('readPart', () => {
  it('reads a part whole, a piece at a time, and refuses a piece that names another part', async () => {
    const piece = (offset: number, data: string, kept: number) => ({ t: 'part' as const, reqId: 'r', seq: 3, name: 'output' as const, offset, total: kept, kept, bytes: new TextEncoder().encode(data) })
    const asked: TranscriptRequest[] = []
    const got = await readPart(async (req) => (asked.push(req), req.part!.offset === 0 ? piece(0, 'abc', 6) : piece(3, 'def', 6)), 3, 'output')
    expect(new TextDecoder().decode(got.bytes)).toBe('abcdef')
    expect(asked.map((r) => r.part?.offset)).toEqual([0, 3])
    await expect(readPart(async () => ({ ...piece(0, 'x', 1), seq: 4 }), 3, 'output')).rejects.toThrow()
  })
})

describe('limitReads', () => {
  it("readsPastTheBound: reads past the connection's four wait their turn and all are answered, none refused", async () => {
    // A connection that refuses a fifth read while four wait, as the transport does, and answers each on a later tick.
    let waiting = 0
    let most = 0
    const get = (n: number) => {
      if (waiting >= 4) return Promise.reject(new Error('too_many_requests'))
      waiting++
      most = Math.max(most, waiting)
      return new Promise<number>((resolve) => setTimeout(() => (waiting--, resolve(n * 10)), 1))
    }
    const limited = limitReads(get, 4)
    const got = await Promise.all([1, 2, 3, 4, 5, 6, 7].map((n) => limited(n)))
    expect(got).toEqual([10, 20, 30, 40, 50, 60, 70])
    expect(most).toBe(4)
    // A refused read frees its place too.
    const failing = limitReads((n: number) => (n === 1 ? Promise.reject(new Error('gone')) : Promise.resolve(n)), 1)
    const [a, b] = await Promise.allSettled([failing(1), failing(2)])
    expect(a.status).toBe('rejected')
    expect(b).toEqual({ status: 'fulfilled', value: 2 })
  })
})
