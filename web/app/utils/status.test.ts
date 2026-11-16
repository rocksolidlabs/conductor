import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import type { Attention } from './protocol'
import { blockStatus, finishedWord, joinedStatus, sessionStatus, STATUS_KEYS, STATUS_LOOK, statusTally, type StatusInput, type StatusKey } from './status'

const NOW = Date.parse('2026-11-16T12:00:00Z')
const ago = (ms: number) => new Date(NOW - ms).toISOString()
const MIN = 60_000

const of = (over: Partial<StatusInput>) => sessionStatus({ status: 'running', ...over }, NOW)
const pick = (v: { key: StatusKey; word: string }) => [v.key, v.word]

describe('sessionStatus', () => {
  it('reads a running session with no state as running, and starting as the running mark with "starting"', () => {
    expect(pick(of({}))).toEqual(['running', 'running'])
    expect(pick(of({ attention: { state: '' } }))).toEqual(['running', 'running'])
    expect(pick(of({ status: 'starting' }))).toEqual(['running', 'starting'])
  })

  it('reads working as working, whoever said it', () => {
    expect(pick(of({ attention: { state: 'working', source: 'api' }, turns: true }))).toEqual(['working', 'working'])
    expect(pick(of({ attention: { state: 'working', source: 'admin' } }))).toEqual(['working', 'working'])
  })

  it('reads done as finished, with its age from since', () => {
    expect(pick(of({ attention: { state: 'done', source: 'api', since: ago(12 * MIN) }, turns: true }))).toEqual(['finished', 'finished 12m ago'])
    expect(pick(of({ attention: { state: 'done', source: 'api', since: ago(30_000) }, turns: true }))).toEqual(['finished', 'finished just now'])
    expect(pick(of({ attention: { state: 'done', source: 'api', since: ago(3 * 60 * MIN + 5 * MIN) } }))).toEqual(['finished', 'finished 3h ago'])
    expect(of({ attention: { state: 'done', since: ago(MIN) } }).since).toBe(ago(MIN))
  })

  it('says "finished" with no age where no time is known (a guest page: the attention frame carries none)', () => {
    expect(pick(of({ attention: { state: 'done', source: 'api' } }))).toEqual(['finished', 'finished'])
    expect(finishedWord('not a time', NOW)).toBe('finished')
    expect(finishedWord(ago(-5000), NOW)).toBe('finished just now')
    expect(finishedWord(ago(59_999), NOW)).toBe('finished just now')
    expect(finishedWord(ago(MIN), NOW)).toBe('finished 1m ago')
    expect(finishedWord(ago(60 * MIN), NOW)).toBe('finished 1h ago')
    expect(finishedWord(ago(49 * 60 * MIN), NOW)).toBe('finished 2d ago')
  })

  it('reads a question as needs, and one keys in the terminal answered as working where the agent tells its turns, else running', () => {
    const asks: Attention = { state: 'needs_input', source: 'api', message: 'Allow?', since: ago(MIN) }
    expect(pick(of({ attention: asks }))).toEqual(['needs', 'needs you'])
    expect(of({ attention: asks }).since).toBe(asks.since)
    const answered: Attention = { ...asks, answered: 'terminal' }
    expect(pick(of({ attention: answered, turns: true }))).toEqual(['working', 'working'])
    expect(pick(of({ attention: answered }))).toEqual(['running', 'running'])
  })

  it('reads the empty state after an Enter (source input) as working only where the agent reports its turns', () => {
    expect(pick(of({ attention: { state: '', source: 'input' }, turns: true }))).toEqual(['working', 'working'])
    expect(pick(of({ attention: { state: '', source: 'input' }, turns: false }))).toEqual(['running', 'running'])
    expect(pick(of({ attention: { state: '', source: 'input' } }))).toEqual(['running', 'running'])
  })

  it('reads an ended session as exited, whatever it asked: "exit N" for status exited with a code other than 0, a stopped one "exited"', () => {
    expect(pick(of({ status: 'exited', attention: { state: 'needs_input' } }))).toEqual(['exited', 'exited'])
    expect(pick(of({ status: 'exited', exitCode: 0 }))).toEqual(['exited', 'exited'])
    expect(pick(of({ status: 'exited' }))).toEqual(['exited', 'exited'])
    expect(pick(of({ status: 'exited', exitCode: 1 }))).toEqual(['exited', 'exit 1'])
    expect(pick(of({ status: 'stopped' }))).toEqual(['exited', 'exited'])
    expect(pick(of({ status: 'stopped', exitCode: -1 }))).toEqual(['exited', 'exited'])
    expect(pick(of({ status: 'stopped', attention: { state: 'working' }, turns: true }))).toEqual(['exited', 'exited'])
  })

  it('reads a host away as the exited mark with "host away", and as needs with "needs you · host away" when it asks', () => {
    expect(pick(of({ status: 'host_disconnected' }))).toEqual(['exited', 'host away'])
    expect(pick(of({ status: 'host_disconnected', attention: { state: 'working' }, turns: true }))).toEqual(['exited', 'host away'])
    expect(pick(of({ status: 'host_disconnected', attention: { state: 'needs_input' } }))).toEqual(['needs', 'needs you · host away'])
    expect(of({ status: 'host_disconnected' }).legend).toMatch(/^host away: /)
  })

  it("gives each state its table's legend line", () => {
    for (const k of STATUS_KEYS) expect(STATUS_LOOK[k].legend.startsWith(`${STATUS_LOOK[k].word}: `)).toBe(true)
    expect(of({}).legend).toBe('running: the agent reports no turns; Conductor cannot tell working from waiting')
    expect(of({ status: 'starting' }).legend).toMatch(/^starting: /)
  })
})

describe('the shared status table', () => {
  // The table the server's session is held to (internal/session/testdata/status-table.json, read by TestTheStatusTable): what a
  // session shows after each row's steps, and the key the app gives it. A Go change that moves a row fails there or here.
  const table = JSON.parse(readFileSync(fileURLToPath(new URL('../../../internal/session/testdata/status-table.json', import.meta.url)), 'utf8')) as {
    rows: Array<{ name: string; attention: Attention; turns: boolean; status: StatusKey }>
  }
  it('has rows', () => {
    expect(table.rows.length).toBeGreaterThan(10)
  })
  it.each(table.rows.map((r) => [r.name, r] as const))('%s', (_, row) => {
    expect(sessionStatus({ status: 'running', attention: row.attention, turns: row.turns }, NOW).key).toBe(row.status)
  })
})

describe('STATUS_LOOK: the distinctness guard', () => {
  const pairs = STATUS_KEYS.flatMap((a, i) => STATUS_KEYS.slice(i + 1).map((b) => [a, b] as const))
  it.each(pairs)('%s and %s differ in word and in mark', (a, b) => {
    const [x, y] = [STATUS_LOOK[a], STATUS_LOOK[b]]
    expect(x.word).not.toBe(y.word)
    expect(`${x.shape} ${x.tone}`).not.toBe(`${y.shape} ${y.tone}`)
    // Two of one tone are told apart by their shape alone.
    if (x.tone === y.tone) expect(x.shape).not.toBe(y.shape)
  })
  it('keeps green for a live agent alone (working ringed, running plain) and amber for needs alone', () => {
    const toned = (t: string) => STATUS_KEYS.filter((k) => STATUS_LOOK[k].tone === t)
    expect(toned('success')).toEqual(['working', 'running'])
    expect(toned('warning')).toEqual(['needs'])
    expect([STATUS_LOOK.working.shape, STATUS_LOOK.running.shape]).toEqual(['ringed-dot', 'dot'])
    expect(STATUS_LOOK.finished.tone).toBe('neutral')
    expect(STATUS_LOOK.exited.shape).toBe('dashed-ring')
  })
  it('names a mark by its word, never a colour: no status colour is given to a word', () => {
    // The look holds no class or colour for the word: StatusMark draws it in the muted text (the walk checks the drawn colour).
    for (const k of STATUS_KEYS) expect(Object.keys(STATUS_LOOK[k]).sort()).toEqual(['legend', 'shape', 'tone', 'word'])
  })
})

describe('blockStatus', () => {
  it('takes the busiest member: needs, working, running, finished, exited', () => {
    expect(blockStatus(['exited', 'finished', 'needs', 'working'])).toBe('needs')
    expect(blockStatus(['finished', 'working', 'running'])).toBe('working')
    expect(blockStatus(['finished', 'running', 'exited'])).toBe('running')
    expect(blockStatus(['exited', 'finished'])).toBe('finished')
    expect(blockStatus(['exited'])).toBe('exited')
    expect(blockStatus([])).toBe('exited')
  })
})

describe('statusTally', () => {
  it('says how many are in each state, in the table order, at 0 none', () => {
    expect(statusTally(['finished', 'working', 'finished'])).toBe('1 working · 2 finished')
    expect(statusTally(['needs', 'exited', 'running'], ['working', 'finished', 'running', 'exited'])).toBe('1 running · 1 exited')
    expect(statusTally([])).toBe('')
  })
})

describe('joinedStatus', () => {
  it('says what a joined entry knows: running, starting or exited; nothing for a link that does not answer, is revoked or gone', () => {
    expect(joinedStatus({ kind: 'session', sessionStatus: 'running' })?.key).toBe('running')
    expect(joinedStatus({ kind: 'session', sessionStatus: 'starting' })?.word).toBe('starting')
    expect(joinedStatus({ kind: 'session', sessionStatus: 'exited' })?.key).toBe('exited')
    expect(joinedStatus({ kind: 'session', sessionStatus: 'host_disconnected' })?.word).toBe('host away')
    expect(joinedStatus({ kind: 'session' })).toBeUndefined()
    expect(joinedStatus({ kind: 'session', sessionStatus: 'running', lastStatus: 'unreachable' })).toBeUndefined()
    expect(joinedStatus({ kind: 'session', lastStatus: 'gone' })).toBeUndefined()
    expect(joinedStatus({ kind: 'run', lastStatus: 'revoked' })).toBeUndefined()
    expect(joinedStatus({ kind: 'run' })?.key).toBe('running')
  })
})
