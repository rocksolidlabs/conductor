import { effectScope, nextTick, ref } from 'vue'
import { describe, expect, it } from 'vitest'
import { boxDrafts } from './useBoxDrafts'

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
