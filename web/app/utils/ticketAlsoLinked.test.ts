import { describe, expect, it } from 'vitest'
import { alsoLinkedWords } from './ticketAlsoLinked'

describe('a ticket linked to the same item as another', () => {
  it('names the others, and nothing for none', () => {
    expect(alsoLinkedWords(['CND-14'])).toBe('CND-14 is linked to the same GitHub issue, imported on another machine: keep one, and unlink or close the other')
    expect(alsoLinkedWords(['CND-14', 'OPS-3'])).toBe(
      'CND-14 and OPS-3 are linked to the same GitHub issue, imported on another machine: keep one, and unlink or close the others',
    )
    expect(alsoLinkedWords([])).toBe('')
    expect(alsoLinkedWords(undefined)).toBe('')
  })
})
