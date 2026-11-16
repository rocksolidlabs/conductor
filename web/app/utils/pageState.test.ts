import { describe, expect, it } from 'vitest'
import { pageState } from './pageState'

describe('pageState', () => {
  it('is reading before the first answer, with no error', () => {
    expect(pageState({ read: false, error: '', count: 0 })).toBe('reading')
    expect(pageState({ read: false, count: 0 })).toBe('reading')
  })
  it('is failed when the first read failed and nothing was ever read', () => {
    expect(pageState({ read: false, error: 'events 500', count: 0 })).toBe('failed')
  })
  it('is empty only once a read said there is nothing', () => {
    expect(pageState({ read: true, error: '', count: 0 })).toBe('empty')
  })
  it('is content whenever something is shown, even with a later error beside it', () => {
    expect(pageState({ read: true, error: '', count: 3 })).toBe('content')
    expect(pageState({ read: true, error: 'events 502', count: 3 })).toBe('content')
  })
  it('stays empty when a read said nothing and a later read failed', () => {
    expect(pageState({ read: true, error: 'events 502', count: 0 })).toBe('empty')
  })
})
