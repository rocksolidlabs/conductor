import { describe, expect, it } from 'vitest'
import { RowDrafts } from './rowDrafts'

describe('RowDrafts', () => {
  it('binds a line to the question shown as it is begun, and keeps it while a change is no fresh start', () => {
    const d = new RowDrafts()
    d.edit('s1', 'm', 'q1')
    d.edit('s1', 'main', 'q2')
    expect(d.get('s1')).toEqual({ text: 'main', question: 'q1' })
  })

  it('keeps a line refused, bound to its question, whatever field shows it next; a change binds it to the question shown then', () => {
    const d = new RowDrafts()
    d.edit('s1', 'main', 'q1')
    d.settle('s1', 'main', 'refused')
    // The field that drew it went (its prompt cleared): the sidebar still holds the line and its question.
    expect(d.text('s1')).toBe('main')
    expect(d.get('s1')?.question).toBe('q1')
    d.edit('s1', 'main now', 'q2')
    expect(d.get('s1')).toEqual({ text: 'main now', question: 'q2' })
  })

  it('a press that never reached the owner changes nothing; a line sent leaves, unless the field changed since', () => {
    const d = new RowDrafts()
    d.edit('s1', 'main', 'q1')
    d.settle('s1', 'main', 'held')
    expect(d.get('s1')).toEqual({ text: 'main', question: 'q1' })
    d.edit('s1', 'main and', 'q1')
    d.settle('s1', 'main', 'sent')
    expect(d.text('s1')).toBe('main and')
    d.settle('s1', 'main and', 'sent')
    expect(d.get('s1')).toBeUndefined()
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
})
