import { describe, expect, it } from 'vitest'
import { MAX_TRANSCRIPT_ENTRY, MAX_TRANSCRIPT_PAGE, type TranscriptEntry, type TranscriptReply, type TranscriptRequest } from './protocol'
import { ANSWERED_WORDS, answerShown, chatBlocks, conductorStep, entryValid, lastWords, limitReads, MAX_ENTRY_TEXT, MAX_KEPT_ENTRIES, promptLine, promptText, readPart, stepsLine, Transcript } from './transcript'

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

  it('readsBackWhatItsTerminalAdded: what the session added while in its terminal (the move\'s notice) is read back as it returns, in order, the mark held until then', async () => {
    const t = new Transcript()
    const notice = (seq: number, mark: number, words: string) => e(seq, mark, { kind: 'notice', code: 'moved', words })
    t.take({ t: 'entry', entry: notice(1, 1, 'Moved to Chat.') })
    t.take({ t: 'entry', entry: e(2, 2) })
    // The way message says terminal: the owner sends nothing meanwhile, though the session adds its notice (seq 3).
    t.leave()
    // Back to Chat: the move's notice comes first, then the way message.
    t.take({ t: 'entry', entry: notice(4, 4, 'Moved to Chat.') })
    expect(t.mark).toBe(2)
    expect(t.rejoin()).toBe(true)
    const asked: TranscriptRequest[] = []
    await t.readBack(async (req) => {
      asked.push(req)
      return { t: 'page', entries: [notice(3, 3, 'Moved to the terminal.'), notice(4, 4, 'Moved to Chat.')], first: 1, last: 4, more: false }
    })
    expect(asked).toEqual([{ after: 2, limit: MAX_TRANSCRIPT_PAGE }])
    expect(chatBlocks(t.list()).filter((b) => b.kind === 'notice').map((b) => b.kind === 'notice' && b.entry.words)).toEqual(['Moved to Chat.', 'Moved to the terminal.', 'Moved to Chat.'])
    expect(t.mark).toBe(4)
    expect(t.gap).toBeNull()
    // A welcome of a session in its terminal (no way) is a leave too; a chat's welcome (its replay covers the gap) and a rejoin with no leave read nothing.
    const w = new Transcript()
    w.welcome({})
    w.welcome({ way: 'protocol', transcript: { first: 1, last: 1, mark: 1 } })
    expect(w.rejoin()).toBe(false)
    expect(new Transcript().rejoin()).toBe(false)
  })

  it('readsItsOwnWindowAfterARefusedReadBack: a replay\'s read-back refused (its oldest entries dropped) leaves its gap, and a return from the terminal still reads what the terminal added, its mark held', async () => {
    const t = new Transcript()
    const notice = (seq: number, mark: number, words: string) => e(seq, mark, { kind: 'notice', code: 'moved', words })
    // A fresh page on a long chat whose oldest entries were dropped: the replay says more; the read-back walks down to the first
    // entry kept (5) and the next read is refused.
    t.take({ t: 'page', entries: [e(10, 20), e(11, 21)], first: 5, last: 11, more: true })
    const gone = Object.assign(new Error('That part of the chat is no longer kept.'), { code: 'transcript_gone' })
    const asked: TranscriptRequest[] = []
    const get = async (req: TranscriptRequest): Promise<TranscriptReply> => {
      asked.push(req)
      // As the server's read answers: nothing lies before 5 and after 11; the entry before 5 is no longer kept.
      if (req.before !== undefined && req.after !== undefined && req.before <= req.after + 1) return { t: 'page', entries: [], first: 5, last: 13, more: false }
      if (req.before === 10) return { t: 'page', entries: [5, 6, 7, 8, 9].map((n) => e(n, n + 10)), first: 5, last: 11, more: true }
      if (req.before === 5) throw gone
      // The terminal's window: entries after the last held as the page left.
      return { t: 'page', entries: [notice(12, 22, 'Moved to the terminal.'), notice(13, 23, 'Moved to Chat.')], first: 5, last: 13, more: false }
    }
    await expect(t.readBack(get)).rejects.toBe(gone)
    expect(t.gap).toBe(5)
    expect(t.mark).toBe(0)
    // Terminal, then Chat: the move's notice to Chat comes first, then the way message.
    t.leave()
    t.take({ t: 'entry', entry: notice(13, 23, 'Moved to Chat.') })
    expect(t.rejoin()).toBe(true)
    asked.length = 0
    await t.readBack(get).catch(() => undefined)
    // It holds the terminal's notice: it asked for the window the terminal added to, whatever the gap below holds.
    expect(t.entries.get(12)?.words).toBe('Moved to the terminal.')
    expect(asked[0]).toEqual({ after: 11, limit: MAX_TRANSCRIPT_PAGE })
    expect(chatBlocks(t.list()).filter((b) => b.kind === 'notice').map((b) => b.kind === 'notice' && b.entry.words)).toEqual(['Moved to the terminal.', 'Moved to Chat.'])
    // The gap below stays for the next connection (its hello's mark is below it), and holds the terminal's marks for when it is read.
    expect(t.gap).toBe(5)
    expect(t.mark).toBe(0)
    await t.readBack(async () => ({ t: 'page', entries: [], first: 5, last: 13, more: false }))
    expect(t.mark).toBe(23)
  })

  it('leavesTheWindowWhenItsReadDoesNotAnswer: a read of the terminal\'s window that fails holds no mark past it, and a chat\'s welcome hands it to its replay', async () => {
    const t = new Transcript()
    t.take({ t: 'entry', entry: e(1, 1) })
    t.leave()
    t.take({ t: 'entry', entry: e(3, 3, { kind: 'notice', code: 'moved', words: 'Moved to Chat.' }) })
    expect(t.rejoin()).toBe(true)
    await expect(t.readBack(async () => Promise.reject(new Error('the owner did not answer the read')))).rejects.toThrow()
    expect(t.mark).toBe(1)
    // A take while the window is unread does not move the mark past it either.
    t.take({ t: 'entry', entry: e(4, 4) })
    expect(t.mark).toBe(1)
    t.welcome({ way: 'protocol', transcript: { first: 1, last: 4, mark: 4 } })
    t.take({ t: 'page', entries: [e(2, 2, { kind: 'notice', code: 'moved', words: 'Moved to the terminal.' }), e(3, 3), e(4, 4)], first: 1, last: 4 })
    expect(t.mark).toBe(4)
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

describe('answerShown', () => {
  it('answerBubbleSaysWhatHappened: an answer to a question is never an empty bubble; its sender sees what they sent, anyone else the words', () => {
    const answer = e(1, 1, { kind: 'prompt', by: 'person', byName: 'Priya', answers: 'q-1' })
    expect(answerShown(answer, 'Yes, go on.')).toEqual({ text: 'Yes, go on.', own: true })
    expect(answerShown(answer, undefined)).toEqual({ text: ANSWERED_WORDS, own: false })
    expect(ANSWERED_WORDS).toBe("Answered the agent's question.")
    expect(answerShown(e(2, 2, { kind: 'prompt', text: 'hello' }), 'x')).toBeNull()
  })
})

describe('conductorStep', () => {
  const step = (tool: string, summary: string) => conductorStep({ tool, summary })
  it('drawsWhatConductorsOwnToolDid: a report as its kind and message, a state as its word, a question as its words', () => {
    expect(step('mcp__conductor__report', '{"event":"progress","message":"checked the files"}')).toEqual({ name: 'Report', words: 'progress: checked the files' })
    expect(step('mcp__conductor__report', '{"event":"handoff","to":"tests","message":"the handlers are in"}')).toEqual({ name: 'Report', words: 'handoff to tests: the handlers are in' })
    expect(step('mcp__conductor__report', '{"event":"artifact","message":"the pull request","url":"https://example.com/pr/1"}')).toEqual({ name: 'Report', words: 'artifact: the pull request https://example.com/pr/1' })
    expect(step('mcp__conductor__set_state', '{"state":"done","message":"all green"}')).toEqual({ name: 'State', words: 'done: all green' })
    expect(step('mcp__conductor__set_state', '{"state":"clear"}')).toEqual({ name: 'State', words: 'cleared' })
    expect(step('mcp__conductor__ask', '{"message":"Shall I go on?","choices":["Yes","No"]}')).toEqual({ name: 'Question', words: 'Shall I go on? (Yes · No)' })
  })
  it('leavesEveryOtherStepAsItIs: another tool, an input cut or not of its shape, is drawn by its name and summary', () => {
    expect(step('Bash', '{"event":"progress"}')).toBeNull()
    expect(step('mcp__other__report', '{"event":"progress","message":"x"}')).toBeNull()
    expect(step('mcp__conductor__report', '{"event":"progress","message":"cut at its bo…')).toBeNull()
    expect(step('mcp__conductor__report', '{"event":"later","message":"x"}')).toBeNull()
    expect(step('mcp__conductor__report', '{"event":"__proto__","message":"x"}')).toBeNull()
    expect(step('mcp__conductor__set_state', '{"state":"sleepy"}')).toBeNull()
    expect(step('mcp__conductor__link', '{"label":"x"}')).toBeNull()
  })
})

