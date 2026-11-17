import { describe, expect, it } from 'vitest'
import { MAX_SEND_QUESTION, MAX_TICKET_TITLE, SEND_KINDS, cancellable, captureAttachments, sendBody, sendState, titleFrom } from './captureSend'

// A capture's sends in the app (PV1 PR-6, utils/captureSend.ts): each destination's body, a send's state in words and marks, and
// the attachments a new plan hands over (what changed since a send is utils/capture.ts's diffSinceSent).

describe('sendBody', () => {
  it('makes each destination’s `to`, its text trimmed', () => {
    expect(sendBody('ticket-new', { project: 'CND', title: '  The save button is hidden ' })).toEqual({ to: { kind: 'ticket', project: 'CND', title: 'The save button is hidden' } })
    expect(sendBody('ticket', { key: 'CND-14' })).toEqual({ to: { kind: 'ticket', key: 'CND-14' } })
    expect(sendBody('brakeman', { question: ' Why? ' })).toEqual({ to: { kind: 'brakeman', question: 'Why?' } })
  })

  it('says what is missing, and what is past its bound', () => {
    expect(sendBody('ticket-new', { project: '', title: 'x' })).toEqual({ problem: 'Choose a project' })
    expect(sendBody('ticket-new', { project: 'CND', title: '  ' })).toEqual({ problem: 'Give the ticket a title' })
    expect(sendBody('ticket-new', { project: 'CND', title: 'é'.repeat(MAX_TICKET_TITLE + 1) })).toEqual({ problem: `A title is at most ${MAX_TICKET_TITLE} characters` })
    expect(sendBody('ticket-new', { project: 'CND', title: 'é'.repeat(MAX_TICKET_TITLE) })).toHaveProperty('to')
    expect(sendBody('ticket', { key: 'cnd-14' })).toEqual({ problem: 'Choose a ticket' })
    expect(sendBody('brakeman', { question: '' })).toEqual({ problem: 'Ask a question' })
    expect(sendBody('brakeman', { question: 'é'.repeat(MAX_SEND_QUESTION + 1) })).toEqual({ problem: `A question is at most ${MAX_SEND_QUESTION} characters` })
    expect(sendBody('decision', {})).toEqual({ problem: 'This destination is not offered here' })
  })

  it('offers the three destinations of today, in the editor’s order', () => {
    expect(SEND_KINDS.map((k) => k.kind)).toEqual(['ticket-new', 'ticket', 'brakeman'])
  })
})

describe('titleFrom', () => {
  it('takes the first note’s first line, its Markdown marks off', () => {
    expect(titleFrom('\n## The save button is hidden\nunder the banner')).toBe('The save button is hidden')
    expect(titleFrom('> - quoted')).toBe('quoted')
    expect(titleFrom('')).toBe('')
  })

  it('cuts a long one on a word, within the title’s bound', () => {
    const t = titleFrom(`${'word '.repeat(80)}end`)
    expect([...t].length).toBeLessThanOrEqual(MAX_TICKET_TITLE)
    expect(t.endsWith('word…')).toBe(true)
  })
})

describe('sendState', () => {
  const base = { rev: 2, kind: 'ticket' as const, target: 'CND-14' }
  it('says each state in words, with a task’s own mark', () => {
    expect(sendState({ ...base, state: 'sent' })).toMatchObject({ word: 'sent as r2 to CND-14', tone: 'neutral', mark: { key: 'done' } })
    expect(sendState({ rev: 1, kind: 'brakeman', state: 'sent', target: 'br-7' }).word).toBe('sent as r1 to Brakeman (br-7)')
    expect(sendState({ rev: 1, kind: 'brakeman', state: 'pending', waiting: '8 requests wait already' })).toMatchObject({
      word: 'waiting for Brakeman: 8 requests wait already',
      mark: { key: 'waiting' },
    })
    expect(sendState({ rev: 1, kind: 'brakeman', state: 'pending' }).word).toBe('waiting for Brakeman')
    expect(sendState({ rev: 1, kind: 'brakeman', state: 'refused', why: 'Brakeman is off: turn it on in Settings' })).toMatchObject({
      word: 'refused: Brakeman is off: turn it on in Settings',
      tone: 'error',
      mark: { key: 'failed' },
    })
    expect(sendState({ rev: 1, kind: 'brakeman', state: 'cancelled' })).toMatchObject({ word: 'cancelled', mark: { key: 'cancelled' } })
  })

  it('names its mark by its own words, the legend the task mark’s', () => {
    const v = sendState({ ...base, state: 'sent' })
    expect(v.mark.word).toBe(v.word)
    expect(v.mark.legend).toMatch(/^done:/)
  })

  it('offers Cancel on a send to Brakeman that may still run alone', () => {
    expect([cancellable({ kind: 'brakeman', state: 'pending' }), cancellable({ kind: 'brakeman', state: 'sent' })]).toEqual([true, true])
    expect([cancellable({ kind: 'brakeman', state: 'refused' }), cancellable({ kind: 'brakeman', state: 'cancelled' }), cancellable({ kind: 'ticket', state: 'sent' })]).toEqual([
      false,
      false,
      false,
    ])
  })
})

describe('captureAttachments', () => {
  it('ticks the live attachments a capture’s send made, newest first, within the bound', () => {
    const a = (id: string, name: string, createdAt: string, deletedAt?: string) => ({ id, name, createdAt, deletedAt })
    const attachments = [
      a('1', 'capture-0123456789abcdef-r1.png', '2026-11-16T10:00:00Z'),
      a('2', 'capture-0123456789abcdef-r1.json', '2026-11-16T10:00:01Z'),
      a('3', 'notes.txt', '2026-11-16T11:00:00Z'),
      a('4', 'capture-0123456789abcdef-r2.json', '2026-11-16T12:00:00Z', '2026-11-16T12:30:00Z'),
      a('5', 'capture-0123456789ABCDEF-r1.json', '2026-11-16T13:00:00Z'),
      a('6', 'capture-0123456789abcdef-r0.json', '2026-11-16T13:00:00Z'),
    ]
    expect(captureAttachments(attachments, 10)).toEqual(['2', '1'])
    expect(captureAttachments(attachments, 1)).toEqual(['2'])
  })
})
