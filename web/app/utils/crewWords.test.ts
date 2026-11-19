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
  runWhen,
  shortRunId,
  startSentence,
  startWords,
  barClass,
  CHART_AT,
  clockTime,
  crewMembersWords,
  filterState,
  lengthWords,
  MEMBER_LOOK,
  MEMBER_PHASES,
  memberAsk,
  memberLook,
  RUN_FILTER_CHIPS,
  RUN_LOOK,
  RUN_PHASES,
  runListName,
  runLook,
  runPhase,
  runPhaseView,
  runsSentence,
  startedClock,
  tookWords,
  type RunBar,
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

describe('runPhase', () => {
  const managed = { revision: 3 }
  it('says every run state in one word, from what the run itself carries (C1, C2)', () => {
    // Live: running, or needs you while a member waits on a prompt or the run has items of the one list.
    expect(runPhase(run({ members: [member({})] }), [])).toBe('running')
    expect(runPhase(run({ members: [member({ needsInput: true })] }), [])).toBe('needs')
    expect(runPhase(run({ members: [member({ sessionId: 's1' })] }), [session({ id: 's1', attention: { state: 'needs_input' } })])).toBe('needs')
    expect(runPhase(run({ managed, members: [member({})] }), [], 1)).toBe('needs')
    // A run with tasks says its own outcome: the failed run whose members carry no error of their own reads Failed (C1).
    const tasks = (outcome: RunInfo['outcome']) => run({ managed, outcome, state: 'finished', members: [member({ status: 'running' }), member({ name: 'b', status: 'pending' })] })
    expect(runPhase(tasks('failed'), [])).toBe('failed')
    expect(runPhase(tasks('succeeded'), [])).toBe('succeeded')
    expect(runPhase(tasks('cancelled'), [])).toBe('stopped')
    // Its items do not make an ended run "needs you": it failed, and says so.
    expect(runPhase(tasks('failed'), [], 2)).toBe('failed')
    // A record says what it recorded.
    expect(runPhase({ ...tasks('failed'), record: true }, [])).toBe('failed')
    expect(runPhase(run({ record: true, state: 'interrupted', members: [member({ status: 'running' })] }), [])).toBe('stopped')
    // A run of members: failed for an error of a member's own, stopped or not; stopped for a stop; succeeded when all ended well.
    const failed = run({ members: [member({ status: 'ended', error: 'exited (exit 1) before its prompt was typed' })] })
    expect(runPhase(failed, [])).toBe('failed')
    expect(runPhase(run({ ...failed, stoppedAt: iso(30) }), [])).toBe('failed')
    expect(runPhase(run({ stoppedAt: iso(30), members: [member({ status: 'ended' }), member({ name: 'late', status: 'ended', error: STOPPED_ERROR })] }), [])).toBe('stopped')
    expect(runPhase(run({ members: [member({ status: 'ended' })] }), [])).toBe('succeeded')
    expect(ownError({ error: STOPPED_ERROR })).toBe('')
    expect(ownError({ error: 'boom' })).toBe('boom')
  })

  it('gives each state one word and one mark, none the same, none said by colour alone', () => {
    const words = RUN_PHASES.map((p) => RUN_LOOK[p].word)
    expect(words).toEqual(['Running', 'Needs you', 'Succeeded', 'Failed', 'Stopped'])
    expect(new Set(RUN_PHASES.map((p) => RUN_LOOK[p].mark)).size).toBe(RUN_PHASES.length)
    for (const p of RUN_PHASES) {
      const v = runPhaseView(p)
      expect(v.word).toBe(RUN_LOOK[p].word)
      expect(v.legend.startsWith(`${v.word}:`)).toBe(true)
    }
    expect(runPhaseView('stopped', true).legend).toBe('Stopped: the server stopped while it ran')
    expect(runPhaseView('failed').key).toBe('failed')
  })

  it('says a run with its counts in the member words (runLook)', () => {
    const r = run({ members: [member({ sessionId: 's1' }), member({ name: 'b', sessionId: 's2' }), member({ name: 'c', sessionId: 's3' })] })
    const ss = [
      session({ id: 's1', attention: { state: 'working' }, turns: true }),
      session({ id: 's2' }),
      session({ id: 's3', attention: { state: 'needs_input', message: 'x' } }),
    ]
    const l = runLook(r, ss, [])
    expect(l.phase).toBe('needs')
    expect(l.view.word).toBe('Needs you')
    expect(l.detail).toBe('1 working · 1 idle · 1 needs you')
    expect(l.needs).toBe(1)
    const failed = run({ managed: { revision: 1 }, outcome: 'failed', state: 'finished', members: [member({})], tasks: [{ id: 'a', member: 'lead', kind: 'work', state: 'succeeded', needs: [], attempts: 1 } as never, { id: 'b', member: 'lead', kind: 'work', state: 'failed', needs: [], attempts: 3 } as never] })
    expect(runLook(failed, [], [])).toMatchObject({ phase: 'failed', detail: '1 of 2 done' })
  })
})

describe('memberLook', () => {
  it('says every member state in one word, with whom it waits for', () => {
    const going = run()
    const stopped = run({ stoppedAt: iso(30) })
    const look = (r: RunInfo, p: Partial<RunMember>, ss: SessionInfo[] = []) => memberLook(r, member(p), ss, T0)
    expect(look(going, { sessionId: 's1' }, [session({ id: 's1', attention: { state: 'working' } })]).view.word).toBe('Working')
    expect(look(going, { sessionId: 's1' }, [session({ id: 's1' })]).view.word).toBe('Idle')
    expect(look(going, { sessionId: 's1' }, [session({ id: 's1', attention: { state: 'needs_input' } })]).view.word).toBe('Needs you')
    expect(look(going, { sessionId: 's1' }, [session({ id: 's1', attention: { state: 'done', since: iso(0) } })]).view.word).toBe('Finished')
    const waits = look(going, { status: 'pending', start: after('driver') })
    expect(waits).toMatchObject({ phase: 'waiting', after: 'after driver', status: 'pending' })
    expect(waits.view.word).toBe('Waiting to start')
    expect(look(going, { status: 'pending', start: { when: 'manual' } }).after).toBe('by hand')
    expect(look(going, { status: 'pending', sessionId: 's1' }, [session({ id: 's1', status: 'starting' })]).phase).toBe('waiting')
    expect(look(going, { status: 'ended' }).view.word).toBe('Finished')
    expect(look(going, { status: 'ended', error: 'exited (exit 1)' })).toMatchObject({ phase: 'failed', view: { word: 'Failed', legend: 'Failed: exited (exit 1)' } })
    expect(look(stopped, { status: 'ended' }).view.word).toBe('Stopped')
    expect(look(stopped, { status: 'ended', error: STOPPED_ERROR }).view.word).toBe('Stopped')
    expect(look(stopped, { status: 'pending' }).view.word).toBe('Stopped')
    expect(look(run({ managed: { revision: 1 }, outcome: 'failed', state: 'finished' }), { status: 'pending' }).view.legend).toBe('Stopped: the run ended before it started')
    expect(look(going, { sessionId: 's1' }, [session({ id: 's1', status: 'host_disconnected' })]).view.word).toBe('Host away')
    expect(MEMBER_PHASES.map((p) => MEMBER_LOOK[p].word)).toEqual(['Working', 'Idle', 'Needs you', 'Waiting to start', 'Finished', 'Failed', 'Stopped'])
    expect(new Set(MEMBER_PHASES.map((p) => MEMBER_LOOK[p].mark)).size).toBe(MEMBER_PHASES.length)
  })

  it("offers a question's choices as the chat's line, never a decision's nor one without an id", () => {
    const options = [
      { label: 'Yes', input: '1' },
      { label: 'No', input: '2' },
    ]
    const asks = memberAsk('navigator', { agentId: 'claude', attention: { state: 'needs_input', message: 'Add it?', id: 'q1', options } })
    expect(asks.question).toBe('Add it?')
    expect(asks.line).toMatchObject({ kind: 'question', id: 'q1', on: 'navigator', options })
    expect(memberAsk('n', { agentId: 'claude', attention: { state: 'needs_input', message: 'Add it?', options } }).line).toBeNull()
    expect(memberAsk('n', { agentId: 'claude', attention: { state: 'needs_input', message: 'Add it?', id: 'q1' } }).line).toBeNull()
    expect(memberAsk('n', { agentId: 'claude', attention: { state: 'needs_input', id: 'q1', options, decision: 'dr-1' } }).line).toBeNull()
    expect(memberAsk('n', { agentId: 'claude', attention: { state: 'needs_input', id: 'q1', options, answered: 'terminal' } }).line).toBeNull()
    expect(memberAsk('n', { agentId: 'claude', attention: { state: 'working' } })).toEqual({ question: '', line: null })
    expect(memberAsk('n', undefined)).toEqual({ question: '', line: null })
  })
})

describe('clockTime', () => {
  const at = (h: number, min: number, s = 0) => new Date(2026, 10, 18, h, min, s).getTime()
  it('writes 24-hour time whatever the hour cycle of the locale (C3)', () => {
    for (const locale of ['en-US', 'en-GB', 'de-DE', undefined]) {
      expect(clockTime(at(19, 28), { locale })).toBe('19:28')
      expect(clockTime(at(7, 5), { locale })).toBe('07:05')
      // Midnight is 00, never 24 (the "hour12: false" of a 12-hour locale gives 24:05 in some browsers).
      expect(clockTime(at(0, 5), { locale })).toBe('00:05')
      expect(clockTime(at(12, 0), { locale })).toBe('12:00')
      expect(clockTime(at(19, 28, 50), { locale, seconds: true })).toBe('19:28:50')
    }
    expect(clockTime(new Date(at(19, 28)).toISOString())).toBe('19:28')
    expect(clockTime('nope')).toBe('')
    expect(startedClock(new Date(at(19, 28)).toISOString())).toBe('19:28')
    expect(runSubtitle({ startedAt: new Date(at(0, 5)).toISOString() })).toBe('run started 00:05')
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

  it('says how long a run went, in words (C12)', () => {
    expect(tookWords(run(), true, T0 + 20 * 60_000)).toBe('20 min so far')
    expect(tookWords(run({ stoppedAt: iso(33) }), false, T0 + 3_600_000)).toBe('under a minute')
    expect(tookWords(run({ state: 'finished', members: [member({ status: 'ended', endedAt: iso(22 * 60) })] }), false, T0 + 3_600_000)).toBe('22 min')
    // A run with tasks ends with its outcome, its log's last line, whatever its idle members do after.
    const tasks = run({ state: 'finished', outcome: 'failed', managed: { revision: 1 }, members: [member({ status: 'running' })], log: [{ at: iso(10), type: 'status' }, { at: iso(3 * 60 + 20), type: 'status' }] })
    expect(tookWords(tasks, false, T0 + 3_600_000)).toBe('3 min')
    expect(lengthWords(64 * 60_000)).toBe('1 h 4 min')
    expect(lengthWords(120 * 60_000)).toBe('2 h')
    expect(lengthWords(3 * 86_400_000)).toBe('3 days')
  })

  it("names a crew's members by how many and on which agents (C4), and a run in its crew's list (C12)", () => {
    expect(crewMembersWords([{ agentId: 'claude' }, { agentId: 'codex' }, { agentId: 'claude' }, { agentId: 'plain' }], new Map([['codex', 'Codex CLI'], ['plain', 'Shell agent']]))).toBe('4 members · Claude Code, Codex CLI, Shell agent')
    expect(crewMembersWords([{ agentId: 'claude' }])).toBe('1 member · Claude Code')
    expect(crewMembersWords([])).toBe('No members yet')
    expect(runListName({ label: 'Dry run', startedAt: iso(0) }, T0)).toBe('Dry run')
    expect(runListName({ startedAt: iso(0) }, T0 + 60_000)).toMatch(/^Run of today \d{2}:\d{2}$/)
  })

  it('says how the last run went and how long runs take, under five runs (C13)', () => {
    const bar = (phase: RunBar['phase'], minutes: number) => ({ id: 'x', startedAt: 0, minutes, phase, asked: 0 })
    expect(runsSentence([])).toBe('No runs yet.')
    expect(runsSentence([bar('failed', 3)])).toBe('The only run so far failed after 3 min.')
    expect(runsSentence([bar('stopped', 0.2)])).toBe('The only run so far was stopped in under a minute.')
    expect(runsSentence([bar('running', 2)])).toBe('The only run so far is running, 2 min so far.')
    expect(runsSentence([bar('succeeded', 4), bar('stopped', 10), bar('succeeded', 6)])).toBe('The last run succeeded after 6 min; runs of this crew usually take 6 min (the middle of 3).')
    expect(runsSentence([bar('succeeded', 4), bar('needs', 1)])).toBe('The last run needs you, 1 min so far; runs of this crew usually take 4 min.')
    expect(CHART_AT).toBe(5)
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
    expect(bars.map((b) => b.phase)).toEqual(['failed', 'stopped', 'needs'])
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

  it('filters runs by their state and when they started', () => {
    const now = new Date(2026, 9, 4, 10, 0, 0).getTime()
    const at = (d: number, h: number) => new Date(2026, 9, d, h, 0, 0).toISOString()
    const rows = [
      { run: { startedAt: at(4, 9) }, phase: 'needs' as const },
      { run: { startedAt: at(4, 0) }, phase: 'succeeded' as const },
      { run: { startedAt: at(3, 23) }, phase: 'stopped' as const },
      { run: { startedAt: at(1, 9) }, phase: 'failed' as const },
      { run: { startedAt: at(1, 10) }, phase: 'running' as const },
      { run: { startedAt: new Date(2026, 8, 1).toISOString() }, phase: 'succeeded' as const },
    ]
    expect(filterRuns(rows, { state: 'all', since: 'today' }, now)).toHaveLength(2)
    expect(filterRuns(rows, { state: 'live', since: 'all' }, now).map((r) => r.phase)).toEqual(['needs', 'running'])
    expect(filterRuns(rows, { state: 'succeeded', since: '7d' }, now)).toHaveLength(1)
    expect(filterRuns(rows, { state: 'succeeded', since: '30d' }, now)).toHaveLength(1)
    expect(filterRuns(rows, { state: 'succeeded', since: 'all' }, now)).toHaveLength(2)
    expect(filterRuns(rows, { state: 'failed', since: 'all' }, now)).toHaveLength(1)
    expect(filterCounts(rows, '7d', now)).toEqual({ all: 5, live: 2, succeeded: 1, failed: 1, stopped: 1 })
    expect(RUN_FILTER_CHIPS.map((c) => c.label)).toEqual(['All', 'Running', 'Succeeded', 'Failed', 'Stopped'])
    expect(sinceStart('today', now)).toBe(new Date(2026, 9, 4).getTime())
  })

  it('reads the kept filter safely', () => {
    expect(readRunFilter(null)).toEqual(DEFAULT_RUN_FILTER)
    expect(readRunFilter('{not json')).toEqual(DEFAULT_RUN_FILTER)
    expect(readRunFilter('{"state":"failed","since":"7d"}')).toEqual({ state: 'failed', since: '7d' })
    expect(readRunFilter('{"state":"nope","since":"1y"}')).toEqual(DEFAULT_RUN_FILTER)
    // The chip named Finished, then Ended, before it read Succeeded: what this browser kept still picks it.
    expect(readRunFilter('{"state":"finished","since":"all"}')).toEqual({ state: 'succeeded', since: 'all' })
    expect(readRunFilter('{"state":"ended","since":"all"}')).toEqual({ state: 'succeeded', since: 'all' })
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

describe('the Runs tab chart', () => {
  it("files a run the server's end cut short with the stopped ones, and draws each bar in its state's tone", () => {
    expect(filterState('needs')).toBe('live')
    expect(filterState('running')).toBe('live')
    expect(filterState('failed')).toBe('failed')
    expect(barClass('running')).toBe('bg-(--ui-success)')
    expect(barClass('needs')).toBe('bg-(--ui-warning)')
    expect(barClass('succeeded')).toBe('bg-(--ui-text-muted)')
    expect(barClass('stopped')).toBe('bg-accented')
    expect(barClass('failed')).toBe('bg-(--ui-error)')
    for (const p of ['succeeded', 'stopped', 'failed'] as const) expect(barClass(p)).not.toContain('success')
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

