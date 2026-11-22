import { describe, expect, it } from 'vitest'
import { ref } from 'vue'
import { CONTROL_TOO_LARGE, MAX_QUESTION_ID, TOO_LONG } from './protocol'
import { clearAnswered, answerMessage, bindDraft, draftQuestion, holdLine, LINE_KEPT, listedAttention, questionOf, repliedQuestions, ReplyError, replyFailure, replyLine } from './reply'
import { sessionRow } from './sidebar'
import { rowPrompt } from './sidebarActions'
import type { SessionInfo } from '~/composables/useSessions'

describe('questionOf', () => {
  it('is the id of a question that waits on input, and nothing for any other state or an older owner', () => {
    expect(questionOf({ state: 'needs_input', id: '0123456789abcdef' })).toBe('0123456789abcdef')
    expect(questionOf({ state: 'needs_input' })).toBeUndefined()
    expect(questionOf({ state: 'done', id: '0123456789abcdef' })).toBeUndefined()
    expect(questionOf({ state: '' })).toBeUndefined()
    expect(questionOf(undefined)).toBeUndefined()
  })
})

describe('draftQuestion', () => {
  const q1 = { state: 'needs_input' as const, id: 'q1q1q1q1q1q1q1q1' }
  const q2 = { state: 'needs_input' as const, id: 'q2q2q2q2q2q2q2q2' }
  it('binds a line to the question shown as its first character is typed', () => {
    expect(draftQuestion('', 'm', undefined, q1)).toBe(q1.id)
    expect(draftQuestion('  ', 'main', undefined, q1)).toBe(q1.id)
  })
  it('keeps naming that question as the line grows, whatever shows by then (someone else answered, the next came)', () => {
    let bound = draftQuestion('', 'm', undefined, q1)
    bound = draftQuestion('m', 'ma', bound, q2)
    bound = draftQuestion('ma', 'main', bound, q2)
    expect(bound).toBe(q1.id)
  })
  it('binds anew once the line is emptied and begun again', () => {
    let bound = draftQuestion('', 'main', undefined, q1)
    bound = draftQuestion('main', '', bound, q2)
    bound = draftQuestion('', 'x', bound, q2)
    expect(bound).toBe(q2.id)
  })
  it('names nothing for a line begun while no question waits, or for an older owner', () => {
    expect(draftQuestion('', 'x', undefined, { state: 'working' })).toBeUndefined()
    expect(draftQuestion('', 'x', undefined, { state: 'needs_input' })).toBeUndefined()
  })
  it("binds a sidebar row's line by the id its prompt shows (bindDraft)", () => {
    let bound = bindDraft('', 'f', undefined, 'q1q1q1q1q1q1q1q1')
    bound = bindDraft('f', 'feature', bound, 'q2q2q2q2q2q2q2q2')
    expect(bound).toBe('q1q1q1q1q1q1q1q1')
  })
})

describe('answerMessage', () => {
  it('names the question a choice or a line was shown for', () => {
    expect(answerMessage({ question: 'q1', input: 'y' }, 'a1')).toEqual({ t: 'answer', reqId: 'a1', question: 'q1', input: 'y' })
    expect(answerMessage({ question: 'q1', text: 'Postgres' }, 'a2')).toEqual({ t: 'answer', reqId: 'a2', question: 'q1', text: 'Postgres' })
  })

  it('is nothing for a reply that names no question the owner could take: it goes as before', () => {
    expect(answerMessage({ input: 'y' }, 'a1')).toBeNull()
    expect(answerMessage({ question: '', text: 'x' }, 'a1')).toBeNull()
    expect(answerMessage({ question: 'q'.repeat(MAX_QUESTION_ID + 1), input: 'y' }, 'a1')).toBeNull()
    expect(answerMessage({ question: 'a b', input: 'y' }, 'a1')).toBeNull()
    expect(answerMessage({ question: 'q1', input: '' }, 'a1')).toBeNull()
    expect(answerMessage({ question: 'q'.repeat(MAX_QUESTION_ID), input: 'y' }, 'a1')).not.toBeNull()
  })
})

describe('listedAttention', () => {
  const shown = { state: 'needs_input' as const, message: 'Allow?', options: [{ label: 'Yes', input: 'y' }], id: '0123456789abcdef' }

  it("leaves the owner's own word standing while its connection is open and the listing names no question", () => {
    expect(listedAttention({ state: 'needs_input', message: 'Which port?' }, shown, true)).toBe(shown)
    expect(listedAttention({ state: '' }, shown, true)).toBe(shown)
  })

  it('takes the listing when it names a question, when nothing named one, or with the connection closed', () => {
    const named = { state: 'needs_input' as const, message: 'Allow?', options: [{ label: 'Yes', input: 'y' }], id: 'fedcba9876543210' }
    expect(listedAttention(named, shown, true)).toBe(named)
    const plain = { state: 'needs_input' as const, message: 'Allow?' }
    expect(listedAttention(plain, { state: '' }, true)).toBe(plain)
    expect(listedAttention(plain, shown, false)).toBe(plain)
  })

  it('never gives a listing the id of the question shown by its words: the same words asked again are another question', () => {
    const again = { state: 'needs_input' as const, message: 'Allow?', options: [{ label: 'Yes', input: 'y' }] }
    expect(listedAttention(again, shown, false).id).toBeUndefined()
  })
})

describe('repliedQuestions', () => {
  it('lets one reply per question go from a page: a double click or a digit pressed twice is one answer', () => {
    const replied = repliedQuestions()
    expect(replied.take('q1')).toBe(true)
    expect(replied.take('q1')).toBe(false)
    expect(replied.take('q2')).toBe(true)
    // A reply that names no question (an older owner's) always goes.
    expect(replied.take(undefined)).toBe(true)
    expect(replied.take(undefined)).toBe(true)
  })

  it('forgets a question whose reply did not go for another reason, so it can be tried again; remembers the newest 64', () => {
    const replied = repliedQuestions()
    replied.take('q1')
    replied.release('q1')
    expect(replied.take('q1')).toBe(true)
    for (let i = 0; i < 64; i++) replied.take(`n${i}`)
    expect(replied.take('q1')).toBe(true)
    expect(replied.take('n63')).toBe(false)
  })

  it('sends once: a second reply while one is on its way, or after it went, is not sent; one refused, answered too, may go again', async () => {
    const replied = repliedQuestions()
    const sent: string[] = []
    let refuse: ((e: Error) => void) | undefined
    const first = replied.once('q1', () => new Promise<void>((_, reject) => (refuse = reject)))
    expect(await replied.once('q1', async () => void sent.push('double'))).toBe(false)
    // Refused as answered (the session's turn was held over it): the question may be asked again with its id, its hold over.
    refuse!(new ReplyError('question_answered', 'this question was already answered: nothing was sent'))
    await expect(first).rejects.toMatchObject({ code: 'question_answered' })
    expect(await replied.once('q1', async () => void sent.push('again'))).toBe(true)
    expect(await replied.once('q1', async () => void sent.push('third'))).toBe(false)
    expect(sent).toEqual(['again'])
    // A reply naming no question always goes.
    expect(await replied.once(undefined, async () => void sent.push('none'))).toBe(true)
    expect(await replied.once(undefined, async () => void sent.push('none'))).toBe(true)
  })
})

describe('holdLine', () => {
  it('keeps a line refused exactly as typed, told once', () => {
    const text = ref('"'.repeat(4096))
    let kept = 0
    const taken = holdLine(text, () => kept++)
    taken('refused')
    expect(text.value).toBe('"'.repeat(4096))
    expect(kept).toBe(1)
    taken('sent')
    expect(text.value).toBe('"'.repeat(4096))
  })

  it('empties the field once the line is sent, unless something was typed since; a line held changes nothing', () => {
    const text = ref('looks good')
    holdLine(text)('sent')
    expect(text.value).toBe('')
    const more = ref('looks good')
    const taken = holdLine(more)
    more.value = 'looks good, and'
    taken('sent')
    expect(more.value).toBe('looks good, and')
    const held = ref('looks good')
    let kept = 0
    holdLine(held, () => kept++)('held')
    expect(held.value).toBe('looks good')
    expect(kept).toBe(0)
  })
})

describe('replyLine', () => {
  /** A field whose page shows the question `shown`. */
  function field() {
    const page = { shown: 'q1' as string | undefined }
    const line = replyLine(() => page.shown)
    return { page, line }
  }

  it('names the question a line was begun for, whatever shows when it is sent', () => {
    const { page, line } = field()
    line.text.value = 'm'
    line.text.value = 'main'
    page.shown = 'q2'
    expect(line.send()).toMatchObject({ line: 'main', question: 'q1' })
  })

  it('a press that never reached the owner changes nothing: the next press names the same question', () => {
    const { page, line } = field()
    line.text.value = 'main'
    const first = line.send()!
    page.shown = 'q2'
    first.taken('held')
    const again = line.send()!
    expect(again).toMatchObject({ line: 'main', question: 'q1' })
    expect(line.text.value).toBe('main')
  })

  it('a line refused stays bound to its question: Enter again without a change names it again', () => {
    const { page, line } = field()
    line.text.value = 'main'
    const first = line.send()!
    page.shown = 'q2'
    first.taken('refused')
    expect(line.text.value).toBe('main')
    expect(line.send()).toMatchObject({ line: 'main', question: 'q1' })
  })

  it('a line refused is bound anew when the person changes it, to the question shown at that change', () => {
    const { page, line } = field()
    line.text.value = 'main'
    line.send()!.taken('refused')
    page.shown = 'q2'
    line.text.value = 'main branch'
    page.shown = 'q3'
    expect(line.send()).toMatchObject({ line: 'main branch', question: 'q2' })
  })

  it('a line not refused is not bound anew by a change: only one begun on an empty field is', () => {
    const { page, line } = field()
    line.text.value = 'main'
    page.shown = 'q2'
    line.text.value = 'main branch'
    expect(line.send()).toMatchObject({ question: 'q1' })
    line.send()!.taken('sent')
    expect(line.text.value).toBe('')
    line.text.value = 'next'
    expect(line.send()).toMatchObject({ line: 'next', question: 'q2' })
  })
})

describe('replyFailure', () => {
  it('says a typed line refused as answered is kept, and how it goes; a choice\'s keys as before', () => {
    const refused = new ReplyError('question_answered', 'this question was already answered by Nate: nothing was sent')
    expect(replyFailure(refused, 'fix-auth', true).description).toBe(`fix-auth's question was answered by Nate. Nothing was sent. ${LINE_KEPT}`)
    expect(replyFailure(refused, 'fix-auth').description).toMatch(/answer it in its terminal\.$/)
  })

  it("says a line past the control bound was not sent, in the owner's words", () => {
    expect(replyFailure(new ReplyError(TOO_LONG, CONTROL_TOO_LARGE), 'fix-auth')).toEqual({ title: 'Not sent', description: CONTROL_TOO_LARGE, color: 'warning' })
  })

  it("says where a durable decision's question is answered when an answer names it (decision): nothing was sent", () => {
    const refused = Object.assign(new Error('dr-7 is answered as a decision, by the operator in Conductor: nothing was sent'), { code: 'decision' })
    expect(replyFailure(refused, 'fix-auth')).toEqual({
      title: 'Answered as a decision',
      description: "The operator answers dr-7 in Conductor: fix-auth's question takes no reply. Nothing was sent.",
      color: 'warning',
    })
  })

  it('says a question was answered already, nothing sent, naming the session where the page lists several', () => {
    const refused = new ReplyError('question_answered', 'this question was already answered: nothing was sent')
    expect(replyFailure(refused, 'fix-auth')).toEqual({
      title: 'Already answered',
      description: "fix-auth's question was answered before your reply arrived. Nothing was sent. If the agent still asks, answer it in its terminal.",
      color: 'warning',
    })
    expect(replyFailure(refused).description).toMatch(/^This question was answered before your reply arrived\. Nothing was sent\./)
  })

  it("says who answered, that another reply is being written, or that the terminal answered, from the owner's words", () => {
    expect(replyFailure(new ReplyError('question_answered', 'this question was already answered by Nate: nothing was sent'), 'fix-auth').description).toBe(
      "fix-auth's question was answered by Nate. Nothing was sent. If the agent still asks, answer it in its terminal.",
    )
    expect(replyFailure(new ReplyError('question_answered', 'this question is being answered: nothing was sent')).description).toMatch(/^This question is being answered by someone else\. Nothing was sent\./)
    expect(replyFailure(new ReplyError('question_answered', 'this question was answered in the terminal: nothing was sent')).description).toMatch(/^This question was answered in its terminal\. Nothing was sent\./)
  })

  it('says a reply the session did not confirm in time, or before the connection closed, was not confirmed, and to look first', () => {
    const late = replyFailure(new ReplyError('no_word', 'x'), 'fix-auth')
    expect(late).toMatchObject({ title: 'Not confirmed', color: 'warning' })
    expect(late.description).toBe('fix-auth did not say within 15 s whether it took your reply. Look at the session before you answer again.')
    expect(replyFailure(new ReplyError('closed', 'x')).description).toBe('The session did not say before the connection closed whether it took your reply. Look at the session before you answer again.')
  })

  it('says a reply that could not name its question was not sent', () => {
    expect(replyFailure(new ReplyError('no_question', 'x'))).toMatchObject({ title: 'Not sent', color: 'warning' })
    expect(replyFailure(new ReplyError('no_question', 'x')).description).toContain('nothing was sent')
  })

  it('says a terminal not connected, and any other failure with its own words', () => {
    expect(replyFailure(new ReplyError('not_connected', 'not connected'))).toMatchObject({ title: 'Not connected', color: 'warning' })
    expect(replyFailure(new Error('This link is view-only'), 'fix-auth')).toEqual({ title: 'Reply to fix-auth failed', description: 'This link is view-only', color: 'error' })
    expect(replyFailure(new ReplyError('read_only', 'this link is view-only'))).toEqual({ title: 'Reply failed', description: 'this link is view-only', color: 'error' })
  })
})

describe('the sidebar row names its question', () => {
  const session = (attention: SessionInfo['attention']): SessionInfo =>
    ({ id: 's1', name: 'fix-auth', kind: 'server', agentId: 'codex', status: 'running', attention, createdAt: '2026-10-12T08:00:00Z' }) as unknown as SessionInfo

  it('carries the id of the question it shows into the answer it offers (a click, a digit, a typed line)', () => {
    const row = sessionRow(session({ state: 'needs_input', message: 'Allow?', options: [{ label: 'Yes', input: 'y' }], id: '0123456789abcdef' }))
    expect(row.prompt?.question).toBe('0123456789abcdef')
    expect(rowPrompt(row)?.question).toBe('0123456789abcdef')
  })

  it("a hosted session's row names the host's id, as the server lists it", () => {
    const hosted = { ...session({ state: 'needs_input', message: 'Allow?', options: [{ label: 'Yes', input: 'y' }], id: 'fedcba9876543210' }), kind: 'hosted' } as SessionInfo
    expect(rowPrompt(sessionRow(hosted))?.question).toBe('fedcba9876543210')
  })

  it('an older owner\'s question has none: the row answers as before', () => {
    const row = sessionRow(session({ state: 'needs_input', message: 'Allow?' }))
    expect(row.prompt?.question).toBeUndefined()
    expect(rowPrompt(row)?.question).toBeUndefined()
  })
})

describe('a refused reply clears the question it named (#258)', () => {
  const session = (id: string, attention: SessionInfo['attention']): SessionInfo => ({ id, name: id, agentId: 'codex', status: 'running', createdAt: '', kind: 'server', attention }) as unknown as SessionInfo
  it('removes the question from the live store when it is the one shown, and leaves another or none alone', () => {
    const sessions = new Map<string, SessionInfo>()
    sessions.set('a', session('a', { state: 'needs_input', message: 'Which?', source: 'question', id: 'q1', options: [{ label: 'Yes', input: 'y' }] }))
    sessions.set('b', session('b', { state: 'needs_input', message: 'Other?', source: 'api', id: 'q2' }))
    sessions.set('c', session('c', { state: 'working', source: 'title' }))
    expect(clearAnswered(sessions, 'a', 'q1')).toBe(true)
    expect(sessions.get('a')!.attention).toEqual({ state: '', source: 'input' })
    expect(clearAnswered(sessions, 'b', 'q1')).toBe(false)
    expect(sessions.get('b')!.attention?.id).toBe('q2')
    expect(clearAnswered(sessions, 'c', 'q9')).toBe(false)
    expect(clearAnswered(sessions, 'nope', 'q1')).toBe(false)
    expect(clearAnswered(sessions, 'b', undefined)).toBe(false)
  })
})
