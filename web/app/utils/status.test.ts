import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import type { RunSummary } from '~/composables/useDispatch'
import type { RunInfo, RunMember, RunTask, SessionInfo } from '~/composables/useSessions'
import type { Decision } from './decisions'
import type { InboxSummary } from './inbox'
import { needsYou } from './needsYou'
import type { Attention } from './protocol'
import {
  blockStatus,
  finishedWord,
  joinedStatus,
  markLook,
  markSection,
  memberStatusOf,
  runBarParts,
  runItems,
  runStatusLine,
  sessionStatus,
  STATUS_KEYS,
  STATUS_LOOK,
  statusTally,
  summaryStatusLine,
  TASK_KEYS,
  TASK_LOOK,
  taskStatus,
  type StatusInput,
  type StatusKey,
} from './status'

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

// ---- Runs and tasks (PR-10) ----

const RUN = 'crew-1a2b3c4d'
const sess = (id: string, attention?: Attention, over: Partial<SessionInfo> = {}) => ({ id, status: 'running', turns: true, attention, ...over }) as unknown as SessionInfo
const WORKING = sess('s-working', { state: 'working', since: ago(MIN) })
const FINISHED = sess('s-finished', { state: 'done', since: ago(5 * MIN) })
const ASKING = sess('s-asking', { state: 'needs_input', since: ago(MIN), id: 'q1' })
const SILENT = sess('s-silent', undefined, { turns: false })
const task = (id: string, over: Partial<RunTask> = {}): RunTask => ({ id, member: 'lead', kind: 'work', state: 'ready', attempts: 0, ...over })
const attemptIn = (session: SessionInfo, n = 1) => ({ n, sessionId: session.id, state: 'running' as const })
const hold = (id: string, state: 'open' | 'answered' = 'open') => ({ id, type: 'approval', state, question: `May ${id} go?` }) as unknown as NonNullable<RunTask['holds']>[number]
const managed = (tasks: RunTask[], over: Partial<RunInfo> = {}): RunInfo =>
  ({ id: RUN, crewId: 'crew', name: 'Crew', goal: '', cwd: '/', isolation: 'worktree', startedAt: ago(60 * MIN), members: [], log: [], state: 'running', needsInput: 0, yolo: false, managed: { revision: 3 }, tasks, ...over }) as RunInfo
const member = (name: string, over: Partial<RunMember> = {}): RunMember => ({ name, agentId: 'worker-cli', start: { when: 'immediately' }, status: 'running', ...over }) as RunMember
const decision = (id: string, run = RUN) => ({ id, run, state: 'open', createdAt: ago(10 * MIN), origin: 'plan', type: 'approval' }) as unknown as Decision
const taskItem = (run: string, id: string): InboxSummary => ({ ref: `task:${run}/${id}`, kind: 'task', since: ago(3 * MIN), title: `${id} failed`, brief: '', question: '', answerer: 'operator', actions: ['retry', 'accept'] })
/** The one list over a run's stores: its sessions, its open decision records and the inbox's task items. */
const listOf = (o: { sessions?: SessionInfo[]; decisions?: Decision[]; inbox?: InboxSummary[] }) =>
  needsYou({
    sessions: { list: o.sessions ?? [], loaded: true, error: '' },
    decisions: { records: o.decisions ?? [], loaded: true, error: '', cut: false },
    inbox: { items: o.inbox ?? [], loaded: true, error: '', next: '', unavailable: '' },
  })

// The engine's table of a task's commands (internal/crew/testdata/task-commands.json): the engine's TaskCommands, the inbox's task items
// and the run page's actions are held to it, and so is taskStatus's "needs you".
const COMMAND_ROWS = (
  JSON.parse(readFileSync(fileURLToPath(new URL('../../../internal/crew/testdata/task-commands.json', import.meta.url)), 'utf8')) as {
    rows: Array<{ name: string; state: RunTask['state']; run: string; commands: Array<'retry' | 'accept' | 'cancel'> }>
  }
).rows

describe('taskStatus', () => {
  const run = managed([])
  const of = (t: RunTask, s?: SessionInfo) => pick(taskStatus(t, run, s, NOW) as { key: StatusKey; word: string })
  it('calls a task held by an open decision needs, and one held by an answered decision (Yardmaster applies it) not', () => {
    expect(of(task('a', { state: 'running', holds: [hold('dr-7')], attempt: attemptIn(WORKING) }), WORKING)).toEqual(['needs', 'needs you'])
    expect(of(task('a', { state: 'ready', holds: [hold('dr-7')] }))).toEqual(['needs', 'needs you'])
    expect(of(task('a', { state: 'ready', holds: [hold('dr-7', 'answered')] }))).toEqual(['waiting', 'ready'])
    expect(of(task('a', { state: 'running', holds: [hold('dr-7', 'answered')], attempt: attemptIn(WORKING) }), WORKING)).toEqual(['working', 'working'])
  })
  it("reads a running task as its attempt's session: working, finished, needs, running where the agent reports no turns, running with none", () => {
    const running = (s: SessionInfo) => task('a', { state: 'running', attempts: 1, attempt: attemptIn(s) })
    expect(of(running(WORKING), WORKING)).toEqual(['working', 'working'])
    expect(of(running(FINISHED), FINISHED)).toEqual(['finished', 'finished 5m ago'])
    expect(of(running(ASKING), ASKING)).toEqual(['needs', 'needs you'])
    expect(of(running(SILENT), SILENT)).toEqual(['running', 'running'])
    expect(of(running(WORKING), undefined)).toEqual(['running', 'running'])
    // An attempt's session that ended while the engine still says running: the engine's word stands.
    expect(of(running(WORKING), sess('s-working', undefined, { status: 'exited' }))).toEqual(['running', 'running'])
  })
  it("gives a task's own states their own marks: checking on the working mark, waiting, ready, done, cancelled", () => {
    expect(of(task('a', { state: 'verifying', attempts: 1 }))).toEqual(['working', 'checking'])
    expect(of(task('a', { state: 'blocked', needs: ['b'] }))).toEqual(['waiting', 'waiting'])
    expect(of(task('a', { state: 'ready' }))).toEqual(['waiting', 'ready'])
    expect(of(task('a', { state: 'succeeded' }))).toEqual(['done', 'done'])
    expect(of(task('a', { state: 'cancelled' }))).toEqual(['cancelled', 'cancelled'])
    expect(of(task('a', { state: 'failed' }))).toEqual(['failed', 'failed'])
  })
  it.each(COMMAND_ROWS.filter((r) => r.state === 'failed').map((r) => [r.name, r] as const))("a failed task is needs exactly when the engine names a command on it: %s", (_, row) => {
    const t = task('a', { state: 'failed', attempts: 3, ...(row.commands.length ? { commands: row.commands } : {}) })
    expect(taskStatus(t, run, undefined, NOW).key).toBe(row.commands.length ? 'needs' : 'failed')
  })
  it('marks a task that will never run "will not run" on the cancelled mark, never waiting: blocked for good, or never started in a run that ended', () => {
    expect(of(task('a', { state: 'blocked', needs: ['b'], reason: 'needs b, which will not succeed', blockedForGood: true }))).toEqual(['cancelled', 'will not run'])
    // Blocked for now (the server says nothing more): waiting, as before.
    expect(of(task('a', { state: 'blocked', needs: ['b'], reason: 'needs b' }))).toEqual(['waiting', 'waiting'])
    // A run that ended: every task that never started, blocked or ready, will not run.
    const ended = { ...run, outcome: 'cancelled' as const }
    for (const state of ['blocked', 'ready'] as const) expect(taskStatus(task('a', { state }), ended, undefined, NOW)).toMatchObject({ key: 'cancelled', word: 'will not run' })
    expect(taskStatus(task('a', { state: 'succeeded' }), ended, undefined, NOW).key).toBe('done')
  })
  it('reads a record as nothing to act on: its failed task is failed', () => {
    expect(taskStatus(task('a', { state: 'failed', commands: ['retry'] }), { record: true }, undefined, NOW).key).toBe('failed')
  })
})

describe('taskStatus and the one list agree on what needs a person', () => {
  // One run of the engine table's live rows, each a task in its state with the commands the engine names, and one task held by an open
  // decision. The inbox lists a failed task with a command (internal/api/inbox.go, taskItem): its items are made by that rule here.
  const rows = COMMAND_ROWS.filter((r) => r.run === 'live')
  const tasks = rows.map((r, i) => task(`t${i}`, { state: r.state, attempts: 1, ...(r.commands.length ? { commands: r.commands } : {}) }))
  tasks.push(task('held', { state: 'ready', holds: [hold('dr-7')] }))
  const run = managed(tasks)
  const inbox = tasks.filter((t) => t.state === 'failed' && (t.commands?.length ?? 0) > 0).map((t) => taskItem(RUN, t.id))
  const list = runItems(listOf({ decisions: [decision('dr-7')], inbox }).items, RUN)
  const needy = tasks.filter((t) => taskStatus(t, run, undefined, NOW).key === 'needs')
  it('has tasks of both kinds to judge', () => {
    expect(needy.map((t) => t.id)).toContain('held')
    expect(needy.length).toBeGreaterThan(2)
  })
  it('lists every task taskStatus calls needs, as a task item or under the decision that holds it', () => {
    for (const t of needy) {
      const listed = list.some((it) => it.ref === `task:${RUN}/${t.id}` || (it.kind === 'decision' && (t.holds ?? []).some((h) => h.id === it.decision?.id)))
      expect(listed, `${t.id} (${t.state}) is in the list`).toBe(true)
    }
  })
  it('calls every task item of the list needs', () => {
    for (const it of list.filter((x) => x.kind === 'task')) {
      const t = tasks.find((x) => `task:${RUN}/${x.id}` === it.ref)!
      expect(taskStatus(t, run, undefined, NOW).key, it.ref).toBe('needs')
    }
  })
})

describe('memberStatusOf', () => {
  const run = (members: RunMember[], over: Partial<RunInfo> = {}) => managed([], { managed: undefined, tasks: undefined, members, ...over })
  const of = (m: RunMember, sessions: SessionInfo[] = []) => {
    const r = memberStatusOf(run([m]), m, sessions, NOW)
    return [r.status, r.mark.key, r.mark.word]
  }
  it('says waiting to start, starting, each session key, exited, and failed for an error of its own; data-status stays memberStatus', () => {
    expect(of(member('a', { status: 'pending' }))).toEqual(['pending', 'waiting', 'waiting to start'])
    expect(of(member('a', { status: 'starting', sessionId: 's-start' }), [sess('s-start', undefined, { status: 'starting' })])).toEqual(['starting', 'running', 'starting'])
    expect(of(member('a', { sessionId: WORKING.id }), [WORKING])).toEqual(['running', 'working', 'working'])
    expect(of(member('a', { sessionId: FINISHED.id }), [FINISHED])).toEqual(['running', 'finished', 'finished 5m ago'])
    expect(of(member('a', { sessionId: ASKING.id }), [ASKING])).toEqual(['needs_input', 'needs', 'needs you'])
    expect(of(member('a', { sessionId: SILENT.id }), [SILENT])).toEqual(['running', 'running', 'running'])
    expect(of(member('a', { status: 'ended' }))).toEqual(['ended', 'exited', 'exited'])
    expect(of(member('a', { status: 'ended', error: 'the run is stopped' }))).toEqual(['ended', 'exited', 'exited'])
    expect(of(member('a', { status: 'ended', error: 'exited (exit 1) before its prompt was typed' }))).toEqual(['ended', 'failed', 'failed'])
  })
})

describe('runStatusLine', () => {
  const held = task('notes', { state: 'ready', holds: [hold('dr-7')] })
  const out = task('migrate', { state: 'failed', attempts: 3, commands: ['retry', 'accept'] })
  const six = managed([
    task('schema', { state: 'succeeded' }),
    task('naming', { state: 'succeeded' }),
    task('soak', { state: 'running', attempts: 1, attempt: attemptIn(WORKING) }),
    task('backfill', { state: 'verifying', attempts: 2 }),
    out,
    held,
  ])
  const items = (o: Parameters<typeof listOf>[0]) => runItems(listOf(o).items, RUN)
  it('says a managed run as done of all, then working, checking and need you', () => {
    const line = runStatusLine(six, [WORKING], items({ decisions: [decision('dr-7')], inbox: [taskItem(RUN, 'migrate')] }), NOW)
    expect(line.text).toBe('2 of 6 done · 1 working · 1 checking · 2 need you')
    expect(line.needs).toBe(2)
    expect(line.parts.find((p) => p.key === 'needs')?.tone).toBe('warning')
    expect(line.parts.filter((p) => p.key !== 'needs').every((p) => p.tone === 'neutral')).toBe(true)
  })
  it('says finished apart from working, from the attempts\' sessions', () => {
    const run = managed([task('a', { state: 'running', attempt: attemptIn(WORKING) }), task('b', { state: 'running', attempt: attemptIn(FINISHED) })])
    expect(runStatusLine(run, [WORKING, FINISHED], [], NOW).text).toBe('0 of 2 done · 1 working · 1 finished')
  })
  it('counts items, never tasks: one gate that holds two tasks is one, two decisions that hold one task are two', () => {
    const two = managed([task('a', { holds: [hold('dr-1')] }), task('b', { holds: [hold('dr-1')] })])
    expect(runStatusLine(two, [], items({ decisions: [decision('dr-1')] }), NOW).text).toBe('0 of 2 done · 1 needs you')
    const one = managed([task('a', { holds: [hold('dr-1'), hold('dr-2')] })])
    expect(runStatusLine(one, [], items({ decisions: [decision('dr-1'), decision('dr-2')] }), NOW).text).toBe('0 of 1 done · 2 need you')
  })
  it("counts a plan gate's open decision that holds no task, once (as taskBadges' proof of the same in tasks.test.ts)", () => {
    const run = managed([task('a', { state: 'running' })])
    expect(runStatusLine(run, [], items({ decisions: [decision('dr-4')] }), NOW).text).toBe('0 of 1 done · 1 running · 1 needs you')
    // Another run's decision is not this run's.
    expect(runStatusLine(run, [], items({ decisions: [decision('dr-4'), decision('dr-5', 'crew-other')] }), NOW).needs).toBe(1)
  })
  it('says a run of members by its members: "1 working · 2 finished · 1 needs you"', () => {
    const f2 = sess('s-finished-2', { state: 'done', since: ago(MIN) })
    const asking = sess('s-asking', { state: 'needs_input', since: ago(MIN), id: 'q1' }, { crew: { runId: RUN, member: 'd' } } as Partial<SessionInfo>)
    const sessions = [WORKING, FINISHED, f2, asking]
    const run = managed([], { managed: undefined, tasks: undefined, members: [member('a', { sessionId: WORKING.id }), member('b', { sessionId: FINISHED.id }), member('c', { sessionId: f2.id }), member('d', { sessionId: asking.id })] })
    expect(runStatusLine(run, sessions, items({ sessions }), NOW).text).toBe('1 working · 2 finished · 1 needs you')
  })
  it('says an ended run by its outcome', () => {
    const at = ago(MIN)
    expect(runStatusLine(managed([task('a', { state: 'succeeded' })], { outcome: 'succeeded' }), [], [], NOW).text).toBe('Succeeded')
    expect(runStatusLine(managed([task('a', { state: 'failed' })], { outcome: 'failed' }), [], [], NOW).text).toBe('Failed')
    expect(runStatusLine(managed([task('a', { state: 'running' })], { stoppedAt: at }), [], [], NOW).text).toBe('Stopped')
    expect(runStatusLine(managed([task('a')], { record: true, state: 'interrupted' }), [], [], NOW).text).toBe('Interrupted')
    const members = (over: Partial<RunMember>) => managed([], { managed: undefined, tasks: undefined, members: [member('a', { status: 'ended' }), member('b', { status: 'ended', ...over })] })
    expect(runStatusLine(members({}), [], [], NOW)).toMatchObject({ text: 'Ended', outcome: 'Ended' })
    expect(runStatusLine(members({ error: 'exited (exit 2)' }), [], [], NOW).text).toBe('Failed')
    // A decision still open on a run that ended is still a person's to answer: said beside the outcome.
    expect(runStatusLine(managed([task('a', { state: 'running' })], { stoppedAt: at }), [], items({ decisions: [decision('dr-9')] }), NOW).text).toBe('Stopped · 1 needs you')
  })
})

describe('summaryStatusLine', () => {
  const summary = (over: Partial<RunSummary> = {}): RunSummary => ({ id: RUN, label: 'Crew', state: 'running', paused: false, startedAt: ago(60 * MIN), record: false, members: [], tasks: {}, questions: 0, blockers: [], ...over })
  it('says what a summary can carry: done of all and need you, and no working, finished or checking part', () => {
    const six = summary({ tasks: { succeeded: 2, running: 2, verifying: 1, failed: 1 } })
    const line = summaryStatusLine(six, runItems(listOf({ decisions: [decision('dr-7')], inbox: [taskItem(RUN, 'migrate')] }).items, RUN))
    expect(line.text).toBe('2 of 6 done · 2 need you')
    expect(line.parts.map((p) => p.key)).toEqual(['done', 'needs'])
  })
  it('says an ended summary by its outcome, and a live run of members alone as running', () => {
    expect(summaryStatusLine(summary({ outcome: 'succeeded', state: 'finished', tasks: { succeeded: 6 } }), []).text).toBe('Succeeded')
    expect(summaryStatusLine(summary({ outcome: 'failed', state: 'finished' }), []).text).toBe('Failed')
    expect(summaryStatusLine(summary({ stoppedAt: ago(MIN), state: 'stopped' }), []).text).toBe('Stopped')
    expect(summaryStatusLine(summary({ state: 'interrupted', record: true }), []).text).toBe('Interrupted')
    expect(summaryStatusLine(summary({ state: 'finished' }), []).text).toBe('Ended')
    expect(summaryStatusLine(summary(), []).text).toBe('running')
  })
})

describe('TASK_LOOK: the distinctness guard', () => {
  const all = [...STATUS_KEYS, ...TASK_KEYS] as const
  const pairs = all.flatMap((a, i) => all.slice(i + 1).map((b) => [a, b] as const))
  it.each(pairs)('%s and %s differ in word and in mark', (a, b) => {
    const [x, y] = [markLook(a), markLook(b)]
    expect(x.word).not.toBe(y.word)
    expect(`${x.shape} ${x.tone}`).not.toBe(`${y.shape} ${y.tone}`)
  })
  it("keeps a task's own marks off the five's shapes, green for none of them, and done in neutral ink, failed red", () => {
    const five = new Set(STATUS_KEYS.map((k) => STATUS_LOOK[k].shape))
    for (const k of TASK_KEYS) {
      expect(five.has(TASK_LOOK[k].shape), `${k}'s shape is none of the five's`).toBe(false)
      expect(TASK_LOOK[k].tone).not.toBe('success')
      expect(TASK_LOOK[k].tone).not.toBe('warning')
    }
    expect([TASK_LOOK.waiting.shape, TASK_LOOK.done.shape, TASK_LOOK.failed.shape, TASK_LOOK.cancelled.shape]).toEqual(['square', 'check', 'cross', 'dash'])
    expect(TASK_LOOK.done.tone).toBe('neutral')
    expect(TASK_LOOK.failed.tone).toBe('error')
    for (const k of TASK_KEYS) expect(Object.keys(TASK_LOOK[k]).sort()).toEqual(['legend', 'shape', 'tone', 'word'])
  })
  it('puts each mark in one sidebar section', () => {
    expect(all.map((k) => markSection(k))).toEqual(['needs', 'running', 'running', 'running', 'exited', 'idle', 'exited', 'exited', 'exited'])
  })
})

describe('runBarParts: the run header\'s bar counts tasks, never items', () => {
  const held = (id: string, ...dr: string[]) => task(id, { state: 'ready', holds: dr.map((d) => hold(d)) })
  const items = (o: Parameters<typeof listOf>[0]) => runItems(listOf(o).items, RUN)
  const parts = (run: RunInfo, sessions: SessionInfo[], its: ReturnType<typeof items>) => runBarParts(runStatusLine(run, sessions, its, NOW)).map((p) => [p.part, p.count])
  it("over the Ledger's counts gives done 2, working 1, checking 1, needs 2, in that order, and no failed part", () => {
    const ledger = managed([
      task('schema', { state: 'succeeded' }),
      task('naming', { state: 'succeeded' }),
      task('backfill', { state: 'verifying', attempts: 2 }),
      task('soak', { state: 'running', attempts: 1, attempt: attemptIn(WORKING) }),
      task('migrate', { state: 'failed', attempts: 3, commands: ['retry', 'accept'] }),
      held('notes', 'dr-1'),
    ])
    expect(parts(ledger, [WORKING], items({ decisions: [decision('dr-1')], inbox: [taskItem(RUN, 'migrate')] }))).toEqual([
      ['done', 2],
      ['working', 1],
      ['checking', 1],
      ['needs', 2],
    ])
  })
  it('one gate holding two tasks: "1 needs you", its segment 2 tasks held; two decisions holding one task: "2 need you", 1 task held', () => {
    const gate = managed([held('a', 'dr-1'), held('b', 'dr-1')])
    const one = runStatusLine(gate, [], items({ decisions: [decision('dr-1')] }), NOW)
    expect(one.text).toBe('0 of 2 done · 1 needs you')
    expect(runBarParts(one).find((p) => p.part === 'needs')).toMatchObject({ count: 2, words: '2 tasks held', mark: 'needs' })
    const two = runStatusLine(managed([held('a', 'dr-1', 'dr-2')]), [], items({ decisions: [decision('dr-1'), decision('dr-2')] }), NOW)
    expect(two.text).toBe('0 of 1 done · 2 need you')
    expect(runBarParts(two).find((p) => p.part === 'needs')).toMatchObject({ count: 1, words: '1 task held' })
  })
  it('keeps the bar and the done words once a run has ended, and gives a run of members no bar', () => {
    const ended = runStatusLine(managed([task('a', { state: 'succeeded' }), task('b', { state: 'cancelled' })], { outcome: 'succeeded' }), [], [], NOW)
    expect([ended.text, ended.done]).toEqual(['Succeeded', '1 of 2 done'])
    expect(runBarParts(ended).map((p) => [p.part, p.mark])).toEqual([
      ['done', 'done'],
      ['cancelled', 'cancelled'],
    ])
    expect(runBarParts(runStatusLine(managed([], { managed: undefined, tasks: undefined, members: [member('a', { sessionId: WORKING.id })] }), [WORKING], [], NOW))).toEqual([])
  })
})
