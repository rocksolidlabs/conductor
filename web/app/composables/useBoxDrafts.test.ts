import { effectScope, nextTick, ref } from 'vue'
import { describe, expect, it } from 'vitest'
import { boxDrafts, ownLines } from './useBoxDrafts'

// The chat boxes' lines, by session (UX3 R4): kept per session past the box that drew one, dropped when sent, emptied, or when the
// session is no longer listed.

function made(ids: string[] | undefined) {
  const listed = ref<string[] | undefined>(ids)
  const scope = effectScope()
  const drafts = scope.run(() => boxDrafts(() => listed.value))!
  return { listed, drafts, scope }
}

describe('the chat boxes’ lines', () => {
  it('keeps a line for each session, there again when the person comes back to it', () => {
    const { drafts } = made(['a', 'b'])
    drafts.edit('a', 'half a thought for a', undefined)
    drafts.edit('b', 'and one for b', undefined)
    expect(drafts.text('a')).toBe('half a thought for a')
    expect(drafts.text('b')).toBe('and one for b')
    expect(drafts.text('c')).toBe('')
  })

  it('drops a line when it is sent or the box is emptied', () => {
    const { drafts } = made(['a'])
    drafts.edit('a', 'go on', undefined)
    drafts.edit('a', '', undefined)
    expect(drafts.text('a')).toBe('')
    drafts.edit('a', 'an answer', 'q1')
    drafts.settle('a', 'an answer', 'left')
    expect(drafts.text('a')).toBe('')
    drafts.settle('a', 'an answer', 'sent')
    expect(drafts.text('a')).toBe('')
  })

  it('binds a line begun while a question shows to it, and a refused answer comes back bound as it was', () => {
    const { drafts } = made(['a'])
    drafts.edit('a', 'blue', 'q1')
    expect(drafts.get('a')?.question).toBe('q1')
    drafts.settle('a', 'blue', 'left')
    drafts.settle('a', 'blue', 'refused')
    expect(drafts.text('a')).toBe('blue')
    expect(drafts.get('a')?.question).toBe('q1')
    // A line begun before the question came stays a message.
    drafts.edit('a', '', undefined)
    drafts.edit('a', 'a message', undefined)
    drafts.edit('a', 'a message, longer', 'q2')
    expect(drafts.get('a')?.question).toBeUndefined()
  })

  it("drops the lines of sessions no longer listed, once the list is read; none while it is not", async () => {
    const { listed, drafts, scope } = made(undefined)
    drafts.edit('a', 'kept', undefined)
    drafts.edit('b', 'gone soon', undefined)
    listed.value = ['a', 'b']
    await nextTick()
    expect(drafts.text('b')).toBe('gone soon')
    listed.value = ['a']
    await nextTick()
    expect(drafts.text('a')).toBe('kept')
    expect(drafts.text('b')).toBe('')
    // Not read (the store starting again): nothing is dropped.
    listed.value = undefined
    await nextTick()
    expect(drafts.text('a')).toBe('kept')
    scope.stop()
  })
})

describe('whose line it is (UX3 R10): only a line the person typed or pasted, or picked themselves, may go for expand', () => {
  function own(ids: string[] | undefined) {
    const listed = ref<string[] | undefined>(ids)
    const scope = effectScope()
    const lines = scope.run(() => ownLines(() => listed.value))!
    return { listed, lines, scope }
  }

  it("a line the person typed is theirs while the box holds what they left; a send lets it go; one kept while they were away stays theirs", () => {
    const { lines } = own(['a', 'b'])
    expect(lines.is('a', '/compact')).toBe(false)
    lines.typed('a', '/compact')
    expect(lines.is('a', '/compact')).toBe(true)
    // They open b and come back: the kept line is still what they typed.
    expect(lines.is('a', '/compact')).toBe(true)
    // Any text the person's own input did not leave is not theirs.
    expect(lines.is('a', '/compact now')).toBe(false)
    lines.sent('a')
    expect(lines.is('a', '/compact')).toBe(false)
  })

  it('a line the page puts back, or one code writes after the person typed, is the page\'s until the person changes it', () => {
    const { lines } = own(['a'])
    lines.typed('a', 'x')
    lines.set('a')
    expect(lines.is('a', 'x')).toBe(false)
    lines.typed('a', 'x')
    // Code writes another text in the box: the record does not hold it.
    expect(lines.is('a', '/clear')).toBe(false)
    // An emptied box holds nobody's line, and an empty text is never one.
    lines.typed('a', '')
    expect(lines.is('a', '')).toBe(false)
    expect(lines.is('a', 'x')).toBe(false)
  })

  it('drops the sessions no longer listed, once the list is read', async () => {
    const { listed, lines, scope } = own(undefined)
    lines.typed('a', 'one')
    lines.typed('b', 'two')
    listed.value = ['a']
    await nextTick()
    expect(lines.is('a', 'one')).toBe(true)
    expect(lines.is('b', 'two')).toBe(false)
    scope.stop()
  })
})
