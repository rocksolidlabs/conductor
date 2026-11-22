import { describe, expect, it } from 'vitest'
import { otherMachinesShown, otherMachinesWhy } from './otherMachinesShown'
import { clock } from './syncStatus'

// When other machines are shown (docs/plans/hosts.md, PR-5): the web half of the one rule, read from a reply's `other` field.

const at = '2026-11-05T14:05:00Z'
const since = '2026-11-05T14:02:00Z'

describe('when other machines are shown', () => {
  it('says nothing with no other field: a plain build, a store never current, starting or signed out', () => {
    expect(otherMachinesShown({ at })).toBe('none')
    expect(otherMachinesShown({ at, other: null })).toBe('none')
    expect(otherMachinesShown(null)).toBe('none')
    expect(otherMachinesWhy({ at })).toBe('')
  })

  it('shows them while current, and while behind under 10 minutes by the reply\'s own clock', () => {
    expect(otherMachinesShown({ at, other: { state: 'current', since } })).toBe('shown')
    expect(otherMachinesShown({ at, other: { state: 'behind', since } })).toBe('shown')
    expect(otherMachinesShown({ at, other: { state: 'behind', since: '2026-11-05T13:55:01Z' } })).toBe('shown')
    expect(otherMachinesWhy({ at, other: { state: 'current', since } })).toBe('')
  })

  it('says one line while unreachable, lapsed, or behind 10 minutes or more, with its reason', () => {
    expect(otherMachinesShown({ at, other: { state: 'unreachable', since } })).toBe('line')
    expect(otherMachinesWhy({ at, other: { state: 'unreachable', since } })).toBe(`the sync service has not answered since ${clock(since)}`)
    expect(otherMachinesWhy({ at, other: { state: 'unreachable' } })).toBe('the sync service is not answering')
    expect(otherMachinesShown({ at, other: { state: 'lapsed', since } })).toBe('line')
    expect(otherMachinesWhy({ at, other: { state: 'lapsed', since } })).toBe('the subscription has lapsed')
    expect(otherMachinesShown({ at, other: { state: 'behind', since: '2026-11-05T13:55:00Z' } })).toBe('line')
    expect(otherMachinesShown({ at, other: { state: 'behind' } })).toBe('line')
    expect(otherMachinesWhy({ at, other: { state: 'behind', since: '2026-11-05T13:55:00Z' } })).toBe(`the sync service has been behind since ${clock('2026-11-05T13:55:00Z')}`)
  })
})
