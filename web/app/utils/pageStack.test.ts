import { describe, expect, it } from 'vitest'
import { dropEntry, pushEntry, topEntry } from './pageStack'

describe('pageStack: the page group registered last is the page’s (UX3 R72)', () => {
  const outer = { name: 'run' }
  const inner = { name: 'member' }
  it('two groups, the inner removed: the outer is the page’s again', () => {
    const both = pushEntry(pushEntry([], outer), inner)
    expect(topEntry(both)).toBe(inner)
    expect(topEntry(dropEntry(both, inner))).toBe(outer)
  })
  it('the outer removed first (the next page set up before the last unmounted): the inner stays', () => {
    const both = pushEntry(pushEntry([], outer), inner)
    expect(topEntry(dropEntry(both, outer))).toBe(inner)
  })
  it('the same group removed twice changes nothing more; an empty stack has no page', () => {
    const one = dropEntry(pushEntry(pushEntry([], outer), inner), inner)
    expect(dropEntry(one, inner)).toEqual([outer])
    expect(topEntry([])).toBeNull()
  })
})
