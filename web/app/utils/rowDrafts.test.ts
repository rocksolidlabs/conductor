import { describe, expect, it } from 'vitest'
import { RowDrafts } from './rowDrafts'

describe('RowDrafts', () => {
  it('binds a line to the question shown as it is begun, and keeps it while a change is no fresh start', () => {
    const d = new RowDrafts()
    d.edit('s1', 'm', 'q1')
    d.edit('s1', 'main', 'q2')
    expect(d.get('s1')).toEqual({ text: 'main', question: 'q1' })
  })

  it('a line that leaves empties the field; refused, it comes back bound to its question, whatever field shows it next', () => {
    const d = new RowDrafts()
    d.edit('s1', 'main', 'q1')
    d.settle('s1', 'main', 'left')
    expect(d.text('s1')).toBe('')
    d.settle('s1', 'main', 'refused')
    // The field that drew it went (its prompt cleared): the sidebar holds the line and its question for the next one.
    expect(d.text('s1')).toBe('main')
    expect(d.get('s1')?.question).toBe('q1')
    d.edit('s1', 'main now', 'q2')
    expect(d.get('s1')).toEqual({ text: 'main now', question: 'q2' })
  })

  it('a line taken for a held turn, or whose word did not come, is let go: nothing is left to send again', () => {
    const d = new RowDrafts()
    d.edit('s1', 'owner line', 'q1')
    d.settle('s1', 'owner line', 'left')
    d.settle('s1', 'owner line', 'sent')
    expect(d.get('s1')).toBeUndefined()
    d.edit('s1', 'next', 'q2')
    expect(d.get('s1')).toEqual({ text: 'next', question: 'q2' })
  })

  it('a press that never left changes nothing; newer text typed while a line was away stays when it is refused', () => {
    const d = new RowDrafts()
    d.edit('s1', 'main', 'q1')
    d.settle('s1', 'main', 'held')
    expect(d.get('s1')).toEqual({ text: 'main', question: 'q1' })
    d.settle('s1', 'main', 'left')
    d.edit('s1', 'other', 'q1')
    d.settle('s1', 'main', 'refused')
    expect(d.text('s1')).toBe('other')
  })

  it('drops a line its person empties, and the lines of sessions no longer listed', () => {
    const d = new RowDrafts()
    d.edit('s1', 'one', 'q1')
    d.edit('s2', 'two', 'q2')
    d.edit('s1', '', 'q1')
    expect(d.get('s1')).toBeUndefined()
    d.edit('s3', 'three', 'q3')
    d.keep(['s3'])
    expect(d.get('s2')).toBeUndefined()
    expect(d.text('s3')).toBe('three')
  })
  it('rebinds a line begun before its question had an id, and frees a line whose question was already answered here (UX3 R5)', () => {
    const d = new RowDrafts()
    d.edit('s1', 'blue', 'ASKED')
    d.rebind('s1', 'ASKED', 'q7')
    expect(d.get('s1')).toEqual({ text: 'blue', question: 'q7' })
    // Only a line bound to that marker.
    d.rebind('s1', 'ASKED', 'q8')
    expect(d.get('s1')?.question).toBe('q7')
    d.free('s1')
    expect(d.get('s1')).toEqual({ text: 'blue' })
    d.free('none')
    expect(d.get('none')).toBeUndefined()
  })
  it("putsARefusedMessageBack (UX3 R10): into its own session's box, at once when empty, else once the person empties it; never dropped", () => {
    const d = new RowDrafts()
    d.putBack('s1', '/compact')
    expect(d.get('s1')).toEqual({ text: '/compact' })
    // Another line begun meanwhile: the refused one waits, and comes back when the box is emptied (the new line sent).
    d.edit('s2', 'a new line', undefined)
    d.putBack('s2', '/model opus')
    expect(d.text('s2')).toBe('a new line')
    d.edit('s2', '', undefined)
    expect(d.get('s2')).toEqual({ text: '/model opus' })
    // A session no longer listed takes its waiting line with it.
    d.edit('s3', 'busy', undefined)
    d.putBack('s3', 'refused')
    d.keep(['s1', 's2'])
    d.edit('s3', 'x', undefined)
    d.edit('s3', '', undefined)
    expect(d.get('s3')).toBeUndefined()
  })
})
