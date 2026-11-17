import { describe, expect, it } from 'vitest'
import type { RunInfo, RunTask, SessionInfo } from '~/composables/useSessions'
import type { BrakemanSince } from './brakeman'
import { NOW_NAMES, needsName, nowTiles, sinceName, sinceWords, waitingTasks, waitingWords } from './brakemanNow'
import type { NeedsItem, NeedsYou } from './needsYou'
import { needsCount } from './needsYou'
import { sidebarModel } from './sidebar'
import { statusTally } from './status'

// Now on the Brakeman page (issue 993, F10): the tiles are the sidebar's own counts for the same input, the waiting tile counts the
// blocked tasks alone, a zero says so with no mark, and the since line says what changed in parts.

const NOW = Date.parse('2026-11-17T05:30:00Z')
const session = (over: Partial<SessionInfo>): SessionInfo =>
  ({ id: 'id', name: 'name', kind: 'server', agentId: 'claude', command: [], cwd: '/w', status: 'running', cols: 80, rows: 24, viewers: 0, createdAt: '2026-11-17T05:00:00Z', ...over }) as SessionInfo
const run = (over: Partial<RunInfo>): RunInfo =>
  ({ id: 'fix-login-1a2b3c4d', crewId: 'fix-login', name: 'fix-login', goal: '', cwd: '/w', isolation: 'worktree', startedAt: '2026-11-17T05:00:00Z', members: [], log: [], state: 'running', needsInput: 0, yolo: false, ...over }) as RunInfo
const task = (id: string, state: RunTask['state'], needs: string[] = []): RunTask => ({ id, member: 'builder', kind: 'work', state, needs, attempts: 0 })

const asking = session({ id: 'a'.repeat(16), name: 'ui', status: 'running', attention: { state: 'needs_input', since: '2026-11-17T05:10:00Z', message: 'Which name?', source: 'hook' } as never })
const working = session({ id: 'b'.repeat(16), name: 'perf probe', attention: { state: 'working', since: '2026-11-17T05:20:00Z', source: 'hook' } as never })
const brakeman = session({ id: 'c'.repeat(16), name: 'Brakeman · br-15', attention: { state: 'working', since: '2026-11-17T05:21:00Z', source: 'hook' } as never })
const finished = session({ id: 'd'.repeat(16), name: 'docs sweep', attention: { state: 'done', since: '2026-11-17T05:18:00Z', source: 'hook' } as never })
const exited = session({ id: 'e'.repeat(16), name: 'old', status: 'exited', exitCode: 0 })
const sessions = [asking, working, brakeman, finished, exited]
const question: NeedsItem = { kind: 'question', ref: `question:${asking.id}/q1`, since: '2026-11-17T05:10:00Z', session: asking }
const known = (items: NeedsItem[]): Pick<NeedsYou, 'number' | 'known' | 'more' | 'items'> => ({ items, number: items.length, known: true, more: false })

describe("Now's tiles", () => {
  it("are the sidebar's own counts for the same input, each naming what it counts", () => {
    const model = sidebarModel(sessions, () => undefined, [question])
    const needs = known([question])
    const tiles = nowTiles({ model, needs, runs: [], sessions, brakemanSession: brakeman.id, now: NOW })
    expect(tiles.map((t) => t.key)).toEqual(['needs', 'working', 'finished', 'waiting'])
    expect(tiles[0]).toMatchObject({ count: needsCount(needs), words: '1 need you', names: 'a question in ui', to: '/decisions' })
    expect(tiles[1]).toMatchObject({ count: String(model.counts.working), words: '2 working', names: "perf probe · Brakeman's turn", to: '/yard' })
    expect(tiles[2]).toMatchObject({ count: String(model.counts.finished), words: '1 finished', names: 'docs sweep 12m ago', to: '/yard' })
    // One set of words: the status language's tallies.
    expect(tiles[0]!.words).toBe(statusTally(['needs']))
    expect(tiles[1]!.words).toBe(statusTally(['working', 'working']))
    for (const t of tiles.slice(0, 3)) expect(t.mark?.key).toBe(t.key)
  })

  it('say a zero in words, with no mark', () => {
    const model = sidebarModel([], () => undefined, [])
    const tiles = nowTiles({ model, needs: known([]), runs: [], sessions: [], now: NOW })
    expect(tiles.map((t) => [t.count, t.words, t.mark])).toEqual([
      ['0', 'Nothing needs you', undefined],
      ['0', 'Nothing working', undefined],
      ['0', 'Nothing finished', undefined],
      ['0', 'Nothing waiting', undefined],
    ])
  })

  it("say Needs you's number as the sidebar does: none while unknown, N+ while cut short", () => {
    const model = sidebarModel([], () => undefined, [])
    const unknown = nowTiles({ model, needs: { items: [], number: 0, known: false, more: false }, runs: [], sessions: [], now: NOW })[0]!
    expect([unknown.count, unknown.words, unknown.mark]).toEqual(['', 'Needs you', undefined])
    expect(nowTiles({ model, needs: { items: [question], number: 1, known: false, more: true }, runs: [], sessions: [], now: NOW })[0]).toMatchObject({ count: '1+', words: '1+ need you' })
  })

  it(`name at most ${NOW_NAMES}, then how many more`, () => {
    const many = Array.from({ length: 5 }, (_, i) => session({ id: `${i}`.repeat(16), name: `w${i}`, createdAt: `2026-11-17T05:0${i}:00Z`, attention: { state: 'working', since: '', source: 'hook' } as never }))
    const model = sidebarModel(many, () => undefined, [])
    expect(nowTiles({ model, needs: known([]), runs: [], sessions: many, now: NOW })[1]!.names).toMatch(/^w\d · w\d · w\d \+2$/)
  })

  it('name a question by its session, and any other item by its title', () => {
    expect(needsName(question)).toBe('a question in ui')
    expect(needsName({ kind: 'task', ref: 'task:fix-login-1a2b3c4d/build', since: '', summary: { title: 'build is out of attempts' } as never })).toBe('build is out of attempts')
  })
})

describe('the waiting tile', () => {
  it('counts the blocked tasks alone, never a ready one, and groups them by what they wait on', () => {
    const r = run({ managed: { revision: 3 }, tasks: [task('build', 'failed'), task('review', 'blocked', ['build']), task('deploy', 'blocked', ['build']), task('lint', 'ready'), task('docs', 'blocked', ['lint'])] })
    const waiting = waitingTasks([r], [], NOW)
    expect(waiting.map((w) => w.task)).toEqual(['review', 'deploy', 'docs'])
    expect(waitingWords(waiting)).toEqual(['review and deploy, behind build', 'docs, behind lint'])
    const tile = nowTiles({ model: sidebarModel([], () => undefined, []), needs: known([]), runs: [r], sessions: [], now: NOW })[3]!
    expect(tile).toMatchObject({ count: '3', words: '3 waiting', names: 'review and deploy, behind build · docs, behind lint', to: '/runs/fix-login-1a2b3c4d' })
    expect(tile.mark?.key).toBe('waiting')
  })

  it("counts none of a stopped run, a finished one, a record or Brakeman's own", () => {
    const blocked = [task('build', 'running'), task('review', 'blocked', ['build'])]
    for (const over of [{ stoppedAt: '2026-11-17T05:00:00Z' }, { outcome: 'failed' as const }, { record: true }, { class: 'brakeman' }]) {
      expect(waitingTasks([run({ tasks: blocked, ...over })], [], NOW)).toEqual([])
    }
    expect(waitingTasks([run({ tasks: blocked })], [], NOW).map((w) => w.task)).toEqual(['review'])
  })

  it('names the tasks a task waits on that have no result yet', () => {
    const r = run({ tasks: [task('a', 'succeeded'), task('b', 'running'), task('c', 'blocked', ['a', 'b'])] })
    expect(waitingWords(waitingTasks([r], [], NOW))).toEqual(['c, behind b'])
  })
})

describe('the since line', () => {
  const clock = (iso: string) => iso.slice(11, 16)
  const none = { count: 0, refs: [] }
  const since = (over: Partial<BrakemanSince>): BrakemanSince => ({ since: { from: 'briefing', briefing: 'br-12', at: '2026-11-17T02:55:00Z' }, runsEnded: none, tasksFailed: none, ticketsReady: none, questionsOpen: none, truncated: false, ...over })
  const names = { run: (id: string) => ({ 'checkout-migration-0c1d2e3f': 'checkout-migration', 'fix-login-1a2b3c4d': 'fix-login' })[id], session: (id: string) => (id === 'a'.repeat(16) ? 'ui' : undefined) }

  it('says each kind that changed, by name, since the last briefing', () => {
    const s = since({
      runsEnded: { count: 1, refs: ['run:checkout-migration-0c1d2e3f'] },
      tasksFailed: { count: 1, refs: ['task:fix-login-1a2b3c4d/build'] },
      ticketsReady: { count: 1, refs: ['ticket:CND-52'] },
      questionsOpen: { count: 1, refs: [`question:${'a'.repeat(16)}/${'f'.repeat(16)}`], oldest: '2026-11-17T04:48:00Z' },
    })
    expect(sinceWords(s, clock, names, NOW)).toBe(
      'Since the last briefing, at 02:55: 1 run ended (checkout-migration) · 1 task failed (build of fix-login) · 1 ticket made ready (CND-52) · 1 question waiting 42m (ui)',
    )
  })

  it('leaves out a kind with nothing in it, counts past the refs it names, and says when it stopped counting', () => {
    const s = since({ runsEnded: { count: 7, refs: ['run:checkout-migration-0c1d2e3f', 'run:other-0a0b0c0d'] }, questionsOpen: { count: 2, refs: [], oldest: '2026-11-17T05:00:00Z' }, truncated: true })
    expect(sinceWords(s, clock, names, NOW)).toBe('Since the last briefing, at 02:55: 7 runs ended (checkout-migration, other-0a0b0c0d, +5) · 2 questions waiting, the oldest 30m · more than these, not all counted')
  })

  it('says the last day with no briefing kept, a time given, and nothing changed', () => {
    expect(sinceWords(since({ since: { from: 'day', at: '2026-11-16T05:30:00Z' }, tasksFailed: { count: 2, refs: [] } }), clock, names, NOW)).toBe('In the last day: 2 tasks failed')
    expect(sinceWords(since({ since: { from: 'given', at: '2026-11-17T01:00:00Z' }, ticketsReady: { count: 1, refs: ['ticket:CND-9'] } }), clock, names, NOW)).toBe('Since 01:00: 1 ticket made ready (CND-9)')
    expect(sinceWords(since({}), clock, names, NOW)).toBe('Nothing has changed since the last briefing, at 02:55.')
    expect(sinceWords(since({ since: { from: 'day', at: '2026-11-16T05:30:00Z' } }), clock, names, NOW)).toBe('Nothing has changed in the last day.')
  })

  it('names each kind of ref', () => {
    expect(sinceName('run:checkout-migration-0c1d2e3f', names)).toBe('checkout-migration')
    expect(sinceName('run:x-0a0b0c0d')).toBe('x-0a0b0c0d')
    expect(sinceName('ticket:CND-52')).toBe('CND-52')
    expect(sinceName(`question:${'a'.repeat(16)}/${'f'.repeat(16)}`, names)).toBe('ui')
    expect(sinceName('not a ref')).toBe('not a ref')
  })
})
