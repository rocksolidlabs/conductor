import { describe, expect, it } from 'vitest'
import { shareWords } from './dispatchShare'

describe("a project's share in words", () => {
  it('says each state, its words and what another key would let in', () => {
    expect(shareWords({ key: 'CND' })).toBe('CND is on this machine alone.')
    expect(shareWords({ key: 'CND', share: { state: 'pending' } })).toBe('CND is being shared.')
    expect(shareWords({ key: 'CND', share: { state: 'shared' } })).toBe('CND is shared with your other machines.')
    expect(shareWords({ key: 'CND', share: { state: 'refused', words: 'another project holds CND in the workspace' } })).toBe(
      'CND could not be shared: another project holds CND in the workspace.',
    )
    expect(shareWords({ key: 'CND', heldBy: { key: 'CND', rows: 3 } })).toBe(
      "CND is on this machine alone. 3 changes of another machine's CND wait for this key: give this project another key to let them in.",
    )
  })
})
