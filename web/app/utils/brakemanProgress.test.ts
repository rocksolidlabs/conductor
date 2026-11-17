import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { READ_WORDS, STARTING_WORDS, clockOf, readCounts, readingWords, readsLabel, readsPercent, readCountWords, timeWords } from './brakemanProgress'

// A Brakeman turn's progress in words, against the reads the guide names (internal/api/guide.go, observeWhats).

const observeWhats = () => {
  const src = readFileSync(new URL('../../../internal/api/guide.go', import.meta.url), 'utf8')
  return /var observeWhats = \[\]string\{([^}]*)\}/.exec(src)![1]!.split(',').map((k) => k.trim().replace(/"/g, ''))
}

describe('what the turn read last', () => {
  it("words a listing's read by what it read, each one the guide offers", () => {
    const whats = observeWhats()
    for (const what of Object.keys(READ_WORDS)) expect(whats, what).toContain(what)
    expect(readingWords({ what: 'changes' }, 1)).toBe('Read what changed')
    expect(readingWords({ what: 'status' }, 2)).toBe('Read the status')
    expect(readingWords({ what: 'overlaps' }, 2)).toBe('Read overlapping changes')
    expect(readingWords({ what: 'briefs' }, 2)).toBe('Read earlier briefs')
    expect(readingWords({ what: 'search' }, 2)).toBe('Searched')
    expect(readingWords({ what: 'runs' }, 2)).toBe('Read the runs')
    expect(readingWords({ what: 'projects' }, 2)).toBe("Read Dispatch's projects")
    expect(readingWords({ what: 'tickets' }, 2)).toBe('Read the tickets')
    expect(readingWords({ what: 'sessions' }, 2)).toBe('Read the sessions')
    expect(readingWords({ what: 'touched' }, 2)).toBe('Read the touched files')
    expect(readingWords({ what: 'capacity' }, 2)).toBe('Read capacity')
    // A read the table does not word (one the guide adds later) is still a read.
    expect(readingWords({ what: 'screen' }, 2)).toBe('Read')
  })

  it('names the record a read named, with the names the page holds, for each kind of ref', () => {
    const names = { run: (id: string) => (id === 'fix-login-1a2b3c4d' ? 'fix-login' : undefined), session: (id: string) => (id === '0123456789abcdef' ? 'docs sweep' : undefined) }
    expect(readingWords({ what: 'task', ref: 'task:fix-login-1a2b3c4d/build' }, 3, names)).toBe('Last read: task build')
    expect(readingWords({ what: 'run', ref: 'run:fix-login-1a2b3c4d' }, 3, names)).toBe('Last read: run fix-login')
    expect(readingWords({ what: 'log', ref: 'check:fix-login-1a2b3c4d/build/3/tests/1' }, 3, names)).toBe('Last read: check tests of build, attempt 3')
    expect(readingWords({ what: 'screen', ref: 'session:0123456789abcdef' }, 3, names)).toBe('Last read: session docs sweep')
    expect(readingWords({ what: 'ticket', ref: 'ticket:CND-41' }, 3, names)).toBe('Last read: CND-41')
    // A ref the grammar does not take is said as it came.
    expect(readingWords({ what: 'task', ref: 'nothing' }, 3)).toBe('Last read: nothing')
  })

  it('says the agent is starting before the first read, and nothing of a read the page does not hold', () => {
    expect(readingWords(undefined, 0)).toBe(STARTING_WORDS)
    expect(STARTING_WORDS).toBe('Starting its agent')
    expect(readingWords(undefined, 4)).toBe('')
  })
})

describe('the reads used of the budget', () => {
  const request = { id: 'br-9001', state: 'running', reads: 12, deadline: '2026-11-17T05:23:00Z', config: { budget: { readsPerTurn: 300 } } }

  it("takes the server's progress for the same request, else the request's own count against its budget", () => {
    expect(readCounts(request, { id: 'br-9001', reads: 41, readsPerTurn: 300 })).toEqual({ used: 41, of: 300 })
    expect(readCounts(request)).toEqual({ used: 12, of: 300 })
    // Another request's progress is not this one's.
    expect(readCounts(request, { id: 'br-9002', reads: 41, readsPerTurn: 200 })).toEqual({ used: 12, of: 300 })
    // A request read after the event is never drawn behind it.
    expect(readCounts({ ...request, reads: 50 }, { id: 'br-9001', reads: 41, readsPerTurn: 300 })).toEqual({ used: 50, of: 300 })
    expect(readCounts({ id: 'br-9001', state: 'running', reads: 3 })).toEqual({ used: 3, of: 0 })
  })

  it('words and fills the meter', () => {
    expect(readCountWords({ used: 41, of: 300 })).toBe('41 of 300 reads')
    expect(readsLabel({ used: 41, of: 300 })).toBe('41 of 300 reads used')
    expect(readCountWords({ used: 1, of: 0 })).toBe('1 read')
    expect(readCountWords({ used: 3, of: 0 })).toBe('3 reads')
    expect(readsPercent({ used: 150, of: 300 })).toBe(50)
    expect(readsPercent({ used: 400, of: 300 })).toBe(100)
    expect(readsPercent({ used: 4, of: 0 })).toBe(0)
  })
})

describe('the time the turn has run', () => {
  const start = '2026-11-17T05:08:00Z'
  const deadline = '2026-11-17T05:23:00Z'

  it('says the time run of the whole turn, in the app\'s words, while the start is known', () => {
    expect(timeWords(start, deadline, Date.parse('2026-11-17T05:11:12Z'))).toBe('3m of 15m')
    expect(timeWords(start, deadline, Date.parse('2026-11-17T05:08:20Z'))).toBe('20s of 15m')
    // Past the deadline it stops at the whole turn.
    expect(timeWords(start, deadline, Date.parse('2026-11-17T06:00:00Z'))).toBe('15m of 15m')
  })

  it('says when it must end with only the deadline, and nothing with neither', () => {
    expect(timeWords(undefined, deadline, 0, 'UTC')).toBe('until 05:23')
    expect(timeWords('not a time', deadline, 0, 'UTC')).toBe('until 05:23')
    expect(timeWords(undefined, undefined, 0)).toBe('')
    expect(clockOf(Date.parse(deadline), 'America/Chicago')).toBe('23:23')
  })
})
