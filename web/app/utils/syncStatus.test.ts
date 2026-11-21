import { describe, expect, it } from 'vitest'
import { clock, syncKeptLines, syncLine } from './syncStatus'

const at = new Date(2026, 10, 20, 14, 2).toISOString()

describe('the Sync row', () => {
  it('says each state', () => {
    expect(clock(at)).toBe('14:02')
    expect(syncLine({ state: 'current' })).toBe('current')
    expect(syncLine({ state: 'behind', waiting: 37 })).toBe('behind: 37 changes waiting')
    expect(syncLine({ state: 'unreachable', since: at, waiting: 37 })).toBe('unreachable since 14:02; 37 changes waiting')
    expect(syncLine({ state: 'lapsed', since: at })).toBe('lapsed since 14:02: this machine works alone and shares nothing new')
    expect(syncLine({ state: 'signed_out' })).toBe('signed out: nothing is shared')
    expect(syncLine({})).toBe('')
  })

  it('lists what was kept back, then what this server keeps itself', () => {
    expect(
      syncKeptLines({
        state: 'current',
        withheld: [{ ref: "CND-12's comment", words: 'quotes captured output' }],
        here: {
          check: 'the output check is reindexing: text written meanwhile waits',
          cut: 2,
          hosted: 1,
        },
      }),
    ).toEqual([
      "CND-12's comment: quotes captured output",
      'the output check is reindexing: text written meanwhile waits',
      '2 decisions have parts cut as output: a person elsewhere sees "[output kept on this machine]" in their place',
      "A text a hosted session's agent wrote stays on this machine: this server does not see that agent's output",
    ])
    expect(syncKeptLines({})).toEqual([])
  })
})
