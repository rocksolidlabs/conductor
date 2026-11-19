import { describe, expect, it } from 'vitest'
import type { CrewInput, CrewMember, RunInfo, RunMember, SessionInfo } from '~/composables/useSessions'
import {
  askedCount,
  barsSummary,
  changedFields,
  DEFAULT_RUN_FILTER,
  filterCounts,
  filterRuns,
  readRunFilter,
  runSubtitle,
  runTicketChips,
  runTitle,
  sinceStart,
  crewShape,
  medianMinutes,
  memberWords,
  minutesLabel,
  runBars,
  runNote,
  runOutcome,
  runTook,
  runWhen,
  shortRunId,
  startSentence,
  startWords,
} from './crewWords'
import { ownError, STOPPED_ERROR } from './crews'

const m = (name: string, start: CrewMember['start'] = { when: 'immediately' }): CrewMember => ({ name, agentId: 'claude', prompt: '', start })
const after = (member: string): CrewMember['start'] => ({ when: 'after', member })

const T0 = Date.parse('2026-10-04T08:31:00Z')
const iso = (s: number) => new Date(T0 + s * 1000).toISOString()
const member = (p: Partial<RunMember>): RunMember => ({ name: 'lead', agentId: 'claude', start: { when: 'immediately' }, status: 'running', ...p })
const run = (p: Partial<RunInfo> = {}): RunInfo => ({
  id: 'users-api-cca6cdf3',
  crewId: 'users-api',
  name: 'users api',
  goal: 'ship',
  cwd: '/srv/api',
  isolation: 'worktree',
  startedAt: iso(0),
  members: [],
  log: [],
  state: 'running',
  needsInput: 0,
  yolo: false,
  ...p,
})
const session = (p: Partial<SessionInfo>): SessionInfo =>
  ({ id: 's1', name: 'lead', kind: 'server', agentId: 'claude', command: [], cwd: '/w', status: 'running', cols: 80, rows: 24, viewers: 0, createdAt: iso(0), ...p }) as SessionInfo

describe('startWords', () => {
  it('says each rule with its icon, and mutes the one that waits for a person', () => {
    expect(startWords({ when: 'immediately' })).toEqual({ icon: 'i-lucide-zap', text: 'At launch', muted: false })
    expect(startWords(after('lead'))).toEqual({ icon: 'i-lucide-corner-down-right', text: 'After lead is done', muted: false })
    expect(startWords({ when: 'manual' })).toEqual({ icon: 'i-lucide-hand', text: 'When you press Start', muted: true })
  })
})

describe('crewShape', () => {
  it('counts the members and says how they start', () => {
    expect(crewShape([])).toBe('no members')
    expect(crewShape([m('solo')])).toBe('1 · at launch')
    expect(crewShape([m('solo', { when: 'manual' })])).toBe('1 · by hand')
    expect(crewShape([m('a'), m('b'), m('c'), m('d')])).toBe('4 · all at launch')
    expect(crewShape([m('writer'), m('checker', after('writer'))])).toBe('2 · checker after writer')
    expect(crewShape([m('notes'), m('tag', after('notes')), m('publish', after('tag'))])).toBe('3 · a chain')
    expect(crewShape([m('lead'), m('core', after('lead')), m('tests', after('core')), m('docs', { when: 'manual' }), m('review')])).toBe('5 · a chain of 3, 1 by hand, 1 alone')
  })

  it('says a crew whose members start by their tasks is run by tasks, not at launch', () => {
    const task = { when: 'task' } as const
    expect(crewShape([m('lead', task), m('core', task), m('cli', task), m('reviewer', task)])).toBe('4 · by tasks')
    expect(crewShape([m('solo', task)])).toBe('1 · by tasks')
  })

  it('calls two members starting after the same one a tree', () => {
    expect(crewShape([m('lead'), m('core', after('lead')), m('cli', after('lead')), m('tester', after('cli'))])).toBe('4 · a tree')
    expect(crewShape([m('lead'), m('core', after('lead')), m('cli', after('lead')), m('docs', { when: 'manual' })])).toBe('4 · a tree of 3, 1 by hand')
  })

  it('treats a rule naming a member that is not here as starting on its own', () => {
    expect(crewShape([m('a', after('ghost')), m('b')])).toBe('2 · all at launch')
  })
})

describe('startSentence', () => {
  it('reads the start order as one sentence', () => {
    expect(startSentence([])).toBe('no members yet')
    expect(startSentence([m('solo')])).toBe('solo starts at launch')
    expect(startSentence([m('a'), m('b')])).toBe('all start at launch')
    expect(startSentence([m('lead'), m('core', after('lead')), m('tests', after('core')), m('docs', { when: 'manual' }), m('review')])).toBe(
      'lead → core → tests start in a chain; docs waits for you; review starts at once',
    )
    expect(startSentence([m('lead'), m('core', after('lead')), m('cli', after('lead')), m('tester', after('cli'))])).toBe('lead → core, cli → tester start in order')
    expect(startSentence([m('a', { when: 'manual' }), m('b', { when: 'manual' }), m('c'), m('d')])).toBe('a and b wait for you; c and d start at once')
  })
})

describe('runOutcome', () => {
  it('keeps failed for an error of a member\'s own, not for the stop that cut it off', () => {
    const stopped = run({ stoppedAt: iso(30), members: [member({ status: 'ended' }), member({ name: 'late', status: 'ended', error: STOPPED_ERROR })] })
    expect(runOutcome(stopped, 'stopped')).toBe('stopped')
    const failed = run({ members: [member({ status: 'ended', error: 'exited (exit 1) before its prompt was typed' })] })
    expect(runOutcome(failed, 'finished')).toBe('failed')
    expect(runOutcome(run({ ...failed, stoppedAt: iso(30) }), 'stopped')).toBe('failed')
    expect(runOutcome(run(), 'running')).toBe('running')
    expect(runOutcome(run(), 'needs_input')).toBe('needs you')
    // A run that has ended says so: "finished" is a session's word, never a run's.
    expect(runOutcome(run({ members: [member({ status: 'ended' })] }), 'finished')).toBe('ended')
    expect(ownError({ error: STOPPED_ERROR })).toBe('')
    expect(ownError({ error: 'boom' })).toBe('boom')
  })
})

describe('memberWords', () => {
  it('says what a stop cut off in grey and keeps red for an error of its own', () => {
    const going = run()
    const stopped = run({ stoppedAt: iso(30) })
    expect(memberWords(going, member({}), 'needs_input')).toEqual({ text: 'needs input', tone: 'warning' })
    expect(memberWords(going, member({}), 'running')).toEqual({ text: 'running', tone: 'default' })
    expect(memberWords(going, member({}), 'starting')).toEqual({ text: 'starting…', tone: 'muted' })
    expect(memberWords(going, member({ start: after('lead') }), 'pending').text).toBe('starts after lead is done')
    expect(memberWords(going, member({ start: { when: 'manual' } }), 'pending').text).toBe('waits for Start now')
    expect(memberWords(going, member({}), 'pending').text).toBe('waiting to start')
    expect(memberWords(stopped, member({}), 'pending')).toEqual({ text: 'not started', tone: 'muted' })
    expect(memberWords(stopped, member({ error: STOPPED_ERROR }), 'ended')).toEqual({ text: 'not started', tone: 'muted' })
    expect(memberWords(stopped, member({}), 'ended')).toEqual({ text: 'stopped', tone: 'muted' })
    expect(memberWords(going, member({}), 'ended')).toEqual({ text: 'ended', tone: 'muted' })
    expect(memberWords(stopped, member({ error: 'exited (exit 1)' }), 'ended')).toEqual({ text: 'ended: exited (exit 1)', tone: 'error' })
  })
})

describe('runNote', () => {
  it('names who asks, then a failure, then what a stop cut off, then what changed', () => {
    const asking = run({ members: [member({ sessionId: 's1' })] })
    expect(runNote(asking, [session({ id: 's1', attention: { state: 'needs_input' } })])).toEqual({ text: 'lead asks a question', tone: 'warning' })
    const failed = run({ members: [member({ name: 'tests', status: 'ended', error: 'exited (exit 1) before its prompt was typed' })] })
    expect(runNote(failed, [])).toEqual({ text: 'tests: exited (exit 1) before its prompt was typed', tone: 'error' })
    const cut = run({ stoppedAt: iso(33), members: [member({ status: 'ended' }), member({ name: 'b', status: 'pending' }), member({ name: 'c', status: 'ended', error: STOPPED_ERROR }), member({ name: 'd', status: 'pending' })] })
    expect(runNote(cut, [])).toEqual({ text: '3 never started', tone: 'muted' })
    const waits = run({ members: [member({}), member({ name: 'docs', status: 'pending', start: { when: 'manual' } })] })
    expect(runNote(waits, [])).toEqual({ text: 'docs waits for Start now', tone: 'muted' })
    const changed = run({
      state: 'finished',
      members: [member({ status: 'ended', branch: 'crew/x/lead', diff: { added: 400, removed: 50 } }), member({ name: 'b', status: 'ended', branch: 'crew/x/b', diff: { added: 12, removed: 8 } })],
    })
    expect(runNote(changed, [])).toEqual({ text: '+412 −58 on 2 branches', tone: 'muted' })
    const answered = run({ state: 'finished', members: [member({ status: 'ended' })], log: [{ at: iso(1), type: 'status', message: 'review asks "Trust this folder?": answer it' }, { at: iso(2), type: 'status', message: 'review asks "Continue?": answer it' }] })
    expect(runNote(answered, [])).toEqual({ text: 'answered twice', tone: 'muted' })
    expect(runNote(run({ state: 'finished', members: [member({ status: 'ended' })] }), []).text).toBe('')
  })
})

describe('names and times', () => {
  it('names a run by its crew and start, with its short id', () => {
    expect(shortRunId('users-api-cca6cdf3')).toBe('cca6cdf3')
    expect(shortRunId('cca6cdf3')).toBe('cca6cdf3')
    expect(runWhen(iso(0), T0 + 3_600_000)).toMatch(/^today \d{2}:\d{2}$/)
    expect(runWhen(iso(0), T0 + 3_600_000)).not.toMatch(/[AP]M/)
    expect(runWhen(iso(0), T0 + 2 * 86_400_000)).toMatch(/^Oct 4 \d/)
    expect(runWhen(iso(0), T0 + 2 * 86_400_000, false)).toBe('Oct 4')
    expect(runWhen('nope')).toBe('')
    expect(askedCount([{ message: 'lead asks "Trust?": …' }, { message: 'typed lead\'s prompt' }, { message: 'asks "x"' }])).toBe(1)
  })

  it('says how long a run went', () => {
    expect(runTook(run(), true, T0 + 4 * 60_000)).toBe('up 4m')
    expect(runTook(run({ stoppedAt: iso(33) }), false, T0 + 3_600_000)).toBe('33s')
    expect(runTook(run({ state: 'finished', members: [member({ status: 'ended', endedAt: iso(22 * 60) })] }), false, T0 + 3_600_000)).toBe('22m')
  })

  it('draws the last runs as bars, oldest first, and sums them up', () => {
    const runs = [
      run({ id: 'r3', startedAt: iso(300), state: 'needs_input', members: [member({ needsInput: true })] }),
      run({ id: 'r2', startedAt: iso(200), stoppedAt: iso(230), state: 'stopped', members: [member({ status: 'ended' })] }),
      run({ id: 'r1', startedAt: iso(100), state: 'finished', members: [member({ status: 'ended', error: 'exited (exit 2)', endedAt: iso(160) })], log: [{ at: iso(1), type: 'status', message: 'lead asks "x": y' }] }),
      run({ id: 'r0', startedAt: iso(0), state: 'finished', members: [member({ status: 'ended', endedAt: iso(120) })] }),
    ]
    const bars = runBars(runs, [], T0 + 600_000, 3)
    expect(bars.map((b) => b.id)).toEqual(['r1', 'r2', 'r3'])
    expect(bars.map((b) => b.outcome)).toEqual(['failed', 'stopped', 'needs you'])
    expect(bars.map((b) => b.minutes)).toEqual([1, 0.5, 5])
    expect(bars.map((b) => b.asked)).toEqual([1, 0, 0])
    expect(medianMinutes(bars)).toBe(1)
    expect(medianMinutes(bars.slice(0, 2))).toBe(0.8)
    expect(medianMinutes([])).toBe(0)
    expect(barsSummary(bars)).toBe('duration in minutes · median 1m · 1 needed an answer · 1 failed')
    expect(minutesLabel(0.5)).toBe('30s')
    expect(minutesLabel(64)).toBe('1h 4m')
    expect(minutesLabel(120)).toBe('2h')
  })
})

describe('changedFields', () => {
  const saved: CrewInput = { name: 'users api', goal: 'ship', cwd: '/srv/api', where: 'server', isolation: 'worktree', openAfterLaunch: true, members: [m('lead')] }
  it('names what a draft changed', () => {
    expect(changedFields(saved, saved)).toEqual([])
    expect(changedFields({ ...saved, goal: 'ship it' }, saved)).toEqual(['Goal'])
    expect(changedFields({ ...saved, name: 'x', cwd: '/x', yolo: true, members: [m('lead'), m('b')] }, saved)).toEqual(['Name', 'Folder', 'More options', 'Members'])
    expect(changedFields(saved, undefined)).toEqual([])
  })
})

describe('run names and the Runs tab filter', () => {
  it('names a run by its own name when it has one, else by its crew and start', () => {
    expect(runSubtitle({ label: 'fix login', startedAt: iso(0) })).toBe('fix login')
    expect(runSubtitle({ startedAt: iso(0) })).toMatch(/^run started \d/)
    expect(runTitle({ name: 'users api', label: 'fix login' })).toBe('fix login')
    expect(runTitle({ name: 'users api' })).toBe('users api')
  })

  it('filters runs by how they ended and when they started', () => {
    const now = new Date(2026, 9, 4, 10, 0, 0).getTime()
    const at = (d: number, h: number) => new Date(2026, 9, d, h, 0, 0).toISOString()
    const rows = [
      { run: { startedAt: at(4, 9) }, outcome: 'needs you' as const },
      { run: { startedAt: at(4, 0) }, outcome: 'ended' as const },
      { run: { startedAt: at(3, 23) }, outcome: 'stopped' as const },
      { run: { startedAt: at(1, 9) }, outcome: 'failed' as const },
      { run: { startedAt: at(1, 10) }, outcome: 'running' as const },
      { run: { startedAt: new Date(2026, 8, 1).toISOString() }, outcome: 'ended' as const },
    ]
    expect(filterRuns(rows, { state: 'all', since: 'today' }, now)).toHaveLength(2)
    expect(filterRuns(rows, { state: 'live', since: 'all' }, now).map((r) => r.outcome)).toEqual(['needs you', 'running'])
    expect(filterRuns(rows, { state: 'ended', since: '7d' }, now)).toHaveLength(1)
    expect(filterRuns(rows, { state: 'ended', since: '30d' }, now)).toHaveLength(1)
    expect(filterRuns(rows, { state: 'ended', since: 'all' }, now)).toHaveLength(2)
    expect(filterCounts(rows, '7d', now)).toEqual({ all: 5, live: 2, ended: 1, stopped: 1, failed: 1 })
    expect(sinceStart('today', now)).toBe(new Date(2026, 9, 4).getTime())
  })

  it('reads the kept filter safely', () => {
    expect(readRunFilter(null)).toEqual(DEFAULT_RUN_FILTER)
    expect(readRunFilter('{not json')).toEqual(DEFAULT_RUN_FILTER)
    expect(readRunFilter('{"state":"failed","since":"7d"}')).toEqual({ state: 'failed', since: '7d' })
    expect(readRunFilter('{"state":"nope","since":"1y"}')).toEqual(DEFAULT_RUN_FILTER)
    // The chip named Finished before it read Ended: what this browser kept still picks it.
    expect(readRunFilter('{"state":"finished","since":"all"}')).toEqual({ state: 'ended', since: 'all' })
  })
})

describe('runResumable', () => {
  it('offers Resume to a stopped run the server holds, not to one live, resumed already, or a record', async () => {
    const { runResumable } = await import('./crewWords')
    const base = { id: 'c-1', crewId: 'c', name: 'C', goal: '', cwd: '/', isolation: 'none', startedAt: '2026-10-21T00:00:00Z', members: [], log: [], state: 'stopped', needsInput: 0, yolo: false } as unknown as import('~/composables/useSessions').RunInfo
    expect(runResumable({ ...base, stoppedAt: '2026-10-21T00:01:00Z' }, false)).toBe(true)
    expect(runResumable({ ...base, stoppedAt: '2026-10-21T00:01:00Z' }, true)).toBe(false)
    expect(runResumable({ ...base, stoppedAt: '2026-10-21T00:01:00Z', resumedBy: 'c-2' }, false)).toBe(false)
    expect(runResumable(base, false)).toBe(false)
    // A record is resumed from the record (#153): stopped, finished or interrupted, once.
    expect(runResumable({ ...base, stoppedAt: '2026-10-21T00:01:00Z', record: true }, false)).toBe(true)
    expect(runResumable({ ...base, state: 'interrupted', record: true }, false)).toBe(true)
    expect(runResumable({ ...base, state: 'finished', record: true }, false)).toBe(true)
    expect(runResumable({ ...base, state: 'interrupted', record: true, resumedBy: 'c-2' }, false)).toBe(false)
    expect(runResumable({ ...base, state: 'running', record: true }, false)).toBe(false)
  })
})

describe('runOutcome', () => {
  it('says interrupted for a run the server\'s end cut short, filed with the stopped ones', async () => {
    const { runOutcome, filterState, outcomeBadge } = await import('./crewWords')
    expect(runOutcome({ members: [] }, 'interrupted')).toBe('interrupted')
    expect(filterState('interrupted')).toBe('stopped')
    expect(outcomeBadge('interrupted')).toEqual({ label: 'interrupted', color: 'warning', variant: 'outline' })
  })

  it('says "ended" for a run that ended, and draws only a live run in green', async () => {
    const { outcomeBadge, barClass } = await import('./crewWords')
    expect(outcomeBadge('ended')).toEqual({ label: 'ended', color: 'neutral', variant: 'outline' })
    expect(outcomeBadge('running').color).toBe('success')
    // The bars: live green (the working green), ended neutral ink, stopped lighter, failed red; no ended run is green.
    expect(barClass('running')).toBe('bg-success')
    expect(barClass('needs you')).toBe('bg-success')
    expect(barClass('ended')).toBe('bg-(--ui-text-muted)')
    expect(barClass('stopped')).toBe('bg-accented')
    expect(barClass('interrupted')).toBe('bg-accented')
    expect(barClass('failed')).toBe('bg-error')
    for (const o of ['ended', 'stopped', 'interrupted', 'failed'] as const) expect(barClass(o)).not.toContain('success')
  })
})

describe('runTicketChips', () => {
  const launched = { id: 't12', key: 'CND-12', rev: 4, plan: 'p3', planN: 3, launch: 'l1', digest: 'd' }
  const linked = (key: string) => ({ id: `id-${key}`, key, title: `The ticket ${key}`, status: 'in_progress' as const })
  it('draws one chip for one ticket: launched from CND-12 and linked to it gives the launched chip alone', () => {
    expect(runTicketChips(launched, linked('CND-12'))).toEqual({ launched, linked: null })
  })
  it('draws both only when they name two tickets', () => {
    expect(runTicketChips(launched, linked('CND-9'))).toEqual({ launched, linked: linked('CND-9') })
  })
  it('draws the linked chip alone for a run not launched from a ticket, and none for neither', () => {
    expect(runTicketChips(undefined, linked('CND-9'))).toEqual({ launched: null, linked: linked('CND-9') })
    expect(runTicketChips(undefined, null)).toEqual({ launched: null, linked: null })
  })
})

