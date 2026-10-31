import { describe, expect, it, vi } from 'vitest'
import type { AgentInfo, CrewCheck, CrewInfo, CrewMember, CrewManifest, CrewStart, CrewTask, RunInfo, RunMember, SessionInfo } from '~/composables/useSessions'
import type { FeedEntry } from './events'
import { argsFrom, broadcastByName, broadcastSelection, broadcastSummary, checkProblems, crewFeed, crewKey, defaultCrew, holdViewLink, joinTileStatus, keepParked, liftSetup, manifestIds, memberNameError, memberNameFrom, memberStatus, pageAfterDelete, manifestMarks, manifestProblems, runCounts, secondsText, sidebarRunFor, startFrom, startValue, summaryOf, takeViewLink, typedSeconds, toCrewInput, toCrewMember, toDraft, taskProblems, taskRowProblems, taskSaveProblems, draftTask, tasksFollowMembers, TASK_LIMITS } from './crews'
import type { ManifestList, ManifestShape, TaskShape } from './crews'
import { startSentence, startWords } from './crewWords'

const lead: CrewMember = { name: 'lead', agentId: 'claude', prompt: 'Own the plan for $GOAL.', args: ['--model', 'x'], start: { when: 'immediately' } }
const tests: CrewMember = { name: 'tests', agentId: 'shell', prompt: '', start: { when: 'after', member: 'lead' } }

const info: CrewInfo = {
  id: 'api-sweep',
  name: 'API sweep',
  goal: 'ship /v1/users',
  cwd: '/srv/api',
  where: 'server',
  isolation: 'worktree',
  openAfterLaunch: true,
  viewLinkTtlSeconds: 28800,
  members: [lead, tests],
  createdAt: '2026-09-29T10:00:00Z',
  updatedAt: '2026-09-29T11:00:00Z',
}

/** What goes over the wire: undefined fields are dropped as JSON drops them. */
const wire = (v: unknown) => JSON.parse(JSON.stringify(v))

describe('toCrewInput', () => {
  it('leaves out what the server sets, so a listed crew can be saved as it is', () => {
    expect(wire(toCrewInput(info))).toEqual({
      name: 'API sweep',
      goal: 'ship /v1/users',
      cwd: '/srv/api',
      where: 'server',
      isolation: 'worktree',
      openAfterLaunch: true,
      viewLinkTtlSeconds: 28800,
      members: [
        { name: 'lead', agentId: 'claude', prompt: 'Own the plan for $GOAL.', args: ['--model', 'x'], start: { when: 'immediately' } },
        { name: 'tests', agentId: 'shell', prompt: '', start: { when: 'after', member: 'lead' } },
      ],
    })
  })

  it("carries the crew's yolo choice, and leaves it out to follow the server's default", () => {
    expect(wire(toCrewInput({ ...info, yolo: true })).yolo).toBe(true)
    expect(wire(toCrewInput({ ...info, yolo: false })).yolo).toBe(false)
    expect(wire(toCrewInput(info))).not.toHaveProperty('yolo')
  })

  it('keeps only the known fields of members and their start', () => {
    const edited = { ...lead, key: 7, start: { when: 'manual' as const, expanded: true } }
    expect(wire(toCrewInput({ ...info, members: [edited] })).members).toEqual([
      { name: 'lead', agentId: 'claude', prompt: 'Own the plan for $GOAL.', args: ['--model', 'x'], start: { when: 'manual' } },
    ])
  })
})

describe('toCrewMember', () => {
  it('keeps a member to the fields the server accepts, for adding it to a run', () => {
    const withState = { ...tests, key: 7, start: { when: 'after' as const, member: 'lead', open: true } }
    expect(wire(toCrewMember(withState))).toEqual({ name: 'tests', agentId: 'shell', prompt: '', start: { when: 'after', member: 'lead' } })
  })
})

const session = (p: Partial<SessionInfo>): SessionInfo => ({
  id: 's1',
  name: 'lead',
  kind: 'server',
  agentId: 'claude',
  command: ['claude'],
  cwd: '/srv/api',
  status: 'running',
  cols: 80,
  rows: 24,
  viewers: 0,
  createdAt: '2026-09-29T10:00:00Z',
  ...p,
})

const member = (p: Partial<RunMember>): RunMember => ({ name: 'lead', agentId: 'claude', start: { when: 'immediately' }, status: 'running', ...p })

const run = (members: RunMember[]): RunInfo => ({
  id: 'api-sweep-1a2b3c4d',
  crewId: 'api-sweep',
  name: 'API sweep',
  goal: 'ship /v1/users',
  cwd: '/srv/api',
  isolation: 'worktree',
  startedAt: '2026-09-29T10:00:00Z',
  members,
  log: [],
  state: 'running',
  needsInput: 0,
  yolo: false,
})

describe('memberStatus', () => {
  const r = run([])
  const crew = { runId: r.id, crewId: r.crewId, member: 'lead' }

  it('is the run state while nothing live says otherwise', () => {
    expect(memberStatus(r, member({ status: 'pending' }), [])).toBe('pending')
    expect(memberStatus(r, member({ status: 'starting', sessionId: 's1' }), [session({ crew })])).toBe('starting')
    expect(memberStatus(r, member({ status: 'running', sessionId: 's1' }), [session({ crew })])).toBe('running')
    expect(memberStatus(r, member({ status: 'ended' }), [])).toBe('ended')
  })

  it('is needs_input when a running member waits on a prompt', () => {
    const waiting = session({ crew, attention: { state: 'needs_input', message: 'Allow edit?' } })
    expect(memberStatus(r, member({ status: 'running', sessionId: 's1' }), [waiting])).toBe('needs_input')
  })

  it('calls a starting member needs_input while its session waits: its trust question holds its prompt', () => {
    const waiting = session({ crew, attention: { state: 'needs_input', source: 'trust' } })
    expect(memberStatus(r, member({ status: 'starting', sessionId: 's1' }), [waiting])).toBe('needs_input')
    expect(memberStatus(r, member({ status: 'starting', sessionId: 's1' }), [session({ crew })])).toBe('starting')
  })

  it("takes the run's own needsInput when no live session says otherwise", () => {
    expect(memberStatus(r, member({ status: 'starting', needsInput: true }), [])).toBe('needs_input')
    expect(memberStatus(r, member({ status: 'running', sessionId: 's1', needsInput: true }), [session({ crew })])).toBe('running')
  })

  it('finds the session by its crew tag before the run is read again', () => {
    const waiting = session({ id: 's9', crew, attention: { state: 'needs_input' } })
    expect(memberStatus(r, member({ status: 'running' }), [waiting])).toBe('needs_input')
    expect(memberStatus(r, member({ status: 'pending' }), [session({ id: 's9', crew })])).toBe('starting')
  })

  it('ignores a session of another run or member', () => {
    const other = session({ crew: { ...crew, runId: 'other-00000000' }, attention: { state: 'needs_input' } })
    const sibling = session({ id: 's2', crew: { ...crew, member: 'core' }, attention: { state: 'needs_input' } })
    expect(memberStatus(r, member({ status: 'running' }), [other, sibling])).toBe('running')
  })

  it('is ended once its session ended, even when the run was read before', () => {
    expect(memberStatus(r, member({ status: 'running', sessionId: 's1' }), [session({ crew, status: 'exited', exitCode: 0 })])).toBe('ended')
    expect(memberStatus(r, member({ status: 'starting', sessionId: 's1' }), [session({ crew, status: 'stopped' })])).toBe('ended')
  })
})

describe('runCounts', () => {
  it('counts members waiting on a prompt and the others running', () => {
    const r = run([
      member({ name: 'lead', sessionId: 'a' }),
      member({ name: 'core', sessionId: 'b' }),
      member({ name: 'web', sessionId: 'c' }),
      member({ name: 'tests', status: 'pending', start: { when: 'after', member: 'core' } }),
      member({ name: 'logs', status: 'starting', sessionId: 'e' }),
      member({ name: 'old', status: 'ended' }),
    ])
    const tag = (m: string) => ({ runId: r.id, crewId: r.crewId, member: m })
    const live = [
      session({ id: 'a', crew: tag('lead'), attention: { state: 'needs_input' } }),
      session({ id: 'b', crew: tag('core'), attention: { state: 'working' } }),
      session({ id: 'c', crew: tag('web') }),
      session({ id: 'e', crew: tag('logs'), attention: { state: 'needs_input' } }),
    ]
    expect(runCounts(r, live)).toEqual({ needs: 2, running: 2 })
  })

  it('is zero for a run with nothing running', () => {
    expect(runCounts(run([member({ status: 'ended' })]), [])).toEqual({ needs: 0, running: 0 })
  })
})

describe('defaultCrew', () => {
  const agents: AgentInfo[] = [
    { id: 'claude', name: 'Claude Code', command: ['claude'], allowArgs: true },
    { id: 'shell', name: 'Shell', command: ['bash', '-l'], allowArgs: false },
  ]

  it('is a new crew on the server with a worktree per agent and one member on the first agent', () => {
    expect(wire(defaultCrew(agents))).toEqual({
      name: 'New crew',
      goal: '',
      cwd: '',
      where: 'server',
      isolation: 'worktree',
      openAfterLaunch: true,
      members: [{ name: 'lead', agentId: 'claude', prompt: '', start: { when: 'immediately' } }],
    })
  })

  it('has no member without an agent in the catalog', () => {
    expect(defaultCrew([]).members).toEqual([])
  })

  it('passes its own member names', () => {
    expect(memberNameError(defaultCrew(agents).members[0]!.name)).toBe('')
  })
})

describe('memberNameError', () => {
  it('accepts what the server accepts', () => {
    for (const n of ['lead', 'a', 'core-2', 'web_ui', 'v1.2', '0day', 'a'.repeat(40)]) expect(memberNameError(n), n).toBe('')
  })

  it('refuses names outside the pattern', () => {
    for (const n of ['', 'Lead', '-lead', '.lead', '_lead', 'le ad', 'le/ad', 'a'.repeat(41), 'é']) expect(memberNameError(n), n).not.toBe('')
  })

  it('refuses what git refuses in a branch name', () => {
    for (const n of ['a..b', 'lead.', 'lead.lock']) expect(memberNameError(n), n).toMatch(/git/)
  })

  it('refuses a name another member has', () => {
    expect(memberNameError('core', ['lead', 'core'])).toMatch(/another member/i)
    expect(memberNameError('core', ['lead'])).toBe('')
  })
})

describe('memberNameFrom', () => {
  it('makes a valid name from free text', () => {
    expect(memberNameFrom('Auth Refactor')).toBe('auth-refactor')
    expect(memberNameFrom('--weird..name.lock')).toBe('weird.name')
    expect(memberNameFrom('...')).toBe('agent')
    expect(memberNameFrom('x'.repeat(60))).toBe('x'.repeat(40))
  })

  it('avoids the names taken', () => {
    expect(memberNameFrom('claude', ['claude'])).toBe('claude-2')
    expect(memberNameFrom('claude', ['claude', 'claude-2'])).toBe('claude-3')
    const long = 'y'.repeat(40)
    const next = memberNameFrom(long, [long])
    expect(next).toBe(`${'y'.repeat(38)}-2`)
    expect(memberNameError(next)).toBe('')
  })
})

describe('startValue and startFrom', () => {
  it('round-trips a start condition through one select value', () => {
    const starts: CrewStart[] = [{ when: 'immediately' }, { when: 'manual' }, { when: 'after', member: 'core' }]
    for (const s of starts) expect(startFrom(startValue(s))).toEqual(s)
    expect(startValue({ when: 'after', member: 'core' })).toBe('after:core')
  })
})

describe('crewFeed', () => {
  const fe = (p: Partial<FeedEntry>): FeedEntry => ({
    at: '2026-09-29T10:05:00Z',
    type: 'progress',
    sessionId: 'a',
    event: 'progress',
    sessionName: 'lead',
    seq: 1,
    time: '10:05:00',
    detail: '4/7 handlers',
    link: null,
    ...p,
  })

  it('merges the run members events and the run log, newest first', () => {
    const items = crewFeed(
      [
        fe({ seq: 1, at: '2026-09-29T10:05:00.5Z' }),
        fe({ seq: 2, sessionId: 'zzz', sessionName: 'elsewhere' }),
        fe({ seq: 3, sessionId: 'b', sessionName: 'core', at: '2026-09-29T10:06:00Z', type: 'artifact', event: 'artifact', detail: '', url: 'https://x.test/pr/1', link: 'https://x.test/pr/1' }),
      ],
      [
        { at: '2026-09-29T10:00:00Z', type: 'status', message: 'launched API sweep: 2 members, 2 starting now' },
        { at: '2026-09-29T10:05:00.25Z', type: 'error', message: 'handoff to unknown member "x" from lead' },
      ],
      new Map([
        ['a', 'lead'],
        ['b', 'core'],
      ]),
    )
    expect(items.map((i) => [i.who, i.what])).toEqual([
      ['core', 'artifact'],
      ['lead', 'progress: 4/7 handlers'],
      ['', 'handoff to unknown member "x" from lead'],
      ['', 'launched API sweep: 2 members, 2 starting now'],
    ])
    expect(items[0]!.link).toBe('https://x.test/pr/1')
    expect(items[0]!.url).toBe('https://x.test/pr/1')
    expect(items[2]!.color).toBe('error')
    expect(items[3]!.color).toBe('neutral')
    expect(new Set(items.map((i) => i.key)).size).toBe(items.length)
  })

  it('keeps the newest entries up to the limit', () => {
    const log = Array.from({ length: 5 }, (_, i) => ({ at: `2026-09-29T10:0${i}:00Z`, type: 'status' as const, message: `m${i}` }))
    expect(crewFeed([], log, new Map(), 2).map((i) => i.what)).toEqual(['m4', 'm3'])
  })
})

describe('broadcastSummary', () => {
  it('names who got the line and who was skipped, and why', () => {
    expect(broadcastSummary({ sent: ['core', 'web'], skipped: [{ member: 'lead', reason: 'needs_input' }, { member: 'tests', reason: 'not_running' }] })).toEqual({
      title: 'Sent to 2 of 4',
      description: 'Sent to core, web. Skipped lead (waiting on a prompt), tests (not running).',
      color: 'warning',
    })
    expect(broadcastSummary({ sent: ['core'], skipped: [] })).toEqual({ title: 'Sent to 1 of 1', description: 'Sent to core.', color: 'success' })
    expect(broadcastSummary({ sent: [], skipped: [{ member: 'x', reason: 'unknown' }] })).toEqual({
      title: 'Sent to 0 of 1',
      description: 'Skipped x (not a member).',
      color: 'error',
    })
  })
})

describe('broadcastByName', () => {
  it('is the display name, without asking the server', async () => {
    let asked = 0
    const whoami = async () => {
      asked++
      return { user: 'nater' }
    }
    expect(await broadcastByName('  Nate  ', whoami)).toBe('Nate')
    expect(asked).toBe(0)
  })
  it("falls back to the server's OS user, as the display name defaults to", async () => {
    expect(await broadcastByName('', async () => ({ user: 'nater' }))).toBe('nater')
    expect(await broadcastByName('   ', async () => ({ user: ' nater ' }))).toBe('nater')
  })
  it('is undefined, recorded as guest, only when both are empty', async () => {
    expect(await broadcastByName('', async () => ({ user: '' }))).toBeUndefined()
    expect(
      await broadcastByName('', async () => {
        throw new Error('401')
      }),
    ).toBeUndefined()
  })
})

describe('toDraft and crewKey', () => {
  it('a draft of a saved crew is not a change until something is edited', () => {
    const d = toDraft(info)
    expect(d.id).toBe('api-sweep')
    expect(d.members.map((m) => m.argsText)).toEqual(['--model x', ''])
    expect(new Set(d.members.map((m) => m.key)).size).toBe(2)
    expect(crewKey(d)).toBe(crewKey(info))
    d.members[1]!.args = argsFrom('--verbose "two words"')
    expect(d.members[1]!.args).toEqual(['--verbose', 'two words'])
    expect(crewKey(d)).not.toBe(crewKey(info))
    d.members[1]!.args = argsFrom('  ')
    expect(crewKey(d)).toBe(crewKey(info))
  })

  it('counts argv text still being typed as unsaved, without sending it', () => {
    const saved: CrewInfo = { ...info, isolation: 'worktree', tasks: [{ id: 'build', member: 'lead', kind: 'work', instructions: 'Build.', checks: [{ id: 'unit', argv: ['go', 'test'] }] }], setup: [{ id: 'deps', argv: ['npm', 'ci'] }] }
    const d = toDraft(saved)
    expect(crewKey(d)).toBe(crewKey(saved))
    const typing = { ...d, tasks: d.tasks!.map((t) => ({ ...t, checks: t.checks!.map((c) => ({ ...c, pending: '"unclosed' })) })) }
    expect(crewKey(typing)).not.toBe(crewKey(saved))
    expect(toCrewInput(typing).tasks![0]!.checks).toEqual([{ id: 'unit', argv: ['go', 'test'] }])
    // The setup is in the project manifest the draft lifted it into, and the text typed there counts as unsaved too.
    expect(d.setup).toBeUndefined()
    const setupTyping = { ...d, manifest: { ...d.manifest, setup: d.manifest!.setup!.map((c) => ({ ...c, pending: 'x' })) } }
    expect(crewKey(setupTyping)).not.toBe(crewKey(saved))
    expect(toCrewInput(setupTyping).manifest?.setup).toEqual([{ id: 'deps', argv: ['npm', 'ci'] }])
    const probeTyping = { ...d, manifest: { ...d.manifest, ready: [{ id: 'web', by: 'command' as const, pending: '"unclosed' }] } }
    expect(crewKey(probeTyping)).not.toBe(crewKey({ ...probeTyping, manifest: { ...probeTyping.manifest, ready: [{ id: 'web', by: 'command' as const }] } }))
    expect(toCrewInput(probeTyping).manifest?.ready).toEqual([{ id: 'web' }])
  })
})

describe('summaryOf', () => {
  it('keeps what the list shows and drops the prompts', () => {
    const s = summaryOf(info)
    expect(s).toEqual({ id: info.id, name: info.name, goal: info.goal, cwd: info.cwd, where: info.where, isolation: info.isolation, members: info.members.map((m) => ({ name: m.name, agentId: m.agentId, start: m.start })), updatedAt: info.updatedAt })
    expect(JSON.stringify(s)).not.toContain('prompt')
  })
})

describe('pageAfterDelete', () => {
  it('steps back from a page the delete emptied, and only then', () => {
    expect(pageAfterDelete(3, 0)).toBe(2)
    expect(pageAfterDelete(2, 0)).toBe(1)
    expect(pageAfterDelete(1, 0)).toBe(1)
    expect(pageAfterDelete(2, 4)).toBe(2)
    expect(pageAfterDelete(1, 99)).toBe(1)
  })
})

describe('sidebarRunFor', () => {
  const tag = { runId: 'api-sweep-1a2b3c4d', crewId: 'api-sweep', member: 'core' }
  const live = [session({ id: 's1', crew: tag }), session({ id: 's2' })]

  it('is the run of a crew view', () => {
    expect(sidebarRunFor('/runs/api-sweep-1a2b3c4d', live)).toBe('api-sweep-1a2b3c4d')
    expect(sidebarRunFor('/runs/a%20b', [])).toBe('a b')
  })

  it('is the run of a member session however it was reached', () => {
    expect(sidebarRunFor('/sessions/s1', live)).toBe('api-sweep-1a2b3c4d')
  })

  it('is nothing for a session outside a crew, one the store lacks, and every other page', () => {
    expect(sidebarRunFor('/sessions/s2', live)).toBeUndefined()
    expect(sidebarRunFor('/sessions/nope', live)).toBeUndefined()
    for (const p of ['/', '/wall', '/crews', '/crews/api-sweep', '/runs/', '/runs/x/y', '/sessions/s1/files']) expect(sidebarRunFor(p, live), p).toBeUndefined()
  })
})

describe('holdViewLink and takeViewLink', () => {
  it('hands a launch view link to its crew view once, and keeps no timer holding it once taken', () => {
    vi.useFakeTimers()
    try {
      holdViewLink('r1', 'https://x.test/join/t', 28800, 1000)
      expect(vi.getTimerCount()).toBe(1)
      expect(takeViewLink('r1', 2000)).toEqual({ url: 'https://x.test/join/t', ttlSeconds: 28800 })
      expect(vi.getTimerCount()).toBe(0)
      expect(takeViewLink('r1', 2000)).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  it('drops it when another run asks, or when nobody took it within a minute', () => {
    holdViewLink('r1', 'https://x.test/join/t', 28800, 1000)
    expect(takeViewLink('r2', 1500)).toBeNull()
    expect(takeViewLink('r1', 1500)).toBeNull()
    holdViewLink('r1', 'https://x.test/join/t', 28800, 1000)
    expect(takeViewLink('r1', 62_000)).toBeNull()
  })
})

describe('holdViewLink', () => {
  it('drops a link nobody took within a minute, without waiting for a take', () => {
    vi.useFakeTimers()
    try {
      holdViewLink('r1', 'https://x/join/t', 3600, 0)
      vi.advanceTimersByTime(60_001)
      // Taken "at" 0, within the hold: only the timer can have dropped it.
      expect(takeViewLink('r1', 0)).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })
})

describe('joinTileStatus', () => {
  it("prefers what the tile's terminal reported, and flags a prompt", () => {
    expect(joinTileStatus({ status: 'running' }, { attention: 'needs_input' })).toMatchObject({ label: 'Needs input' })
    expect(joinTileStatus({ status: 'starting' }, { status: 'running' })).toMatchObject({ label: 'Running' })
    expect(joinTileStatus({ status: 'ended' }, { attention: 'needs_input' })).toMatchObject({ label: 'ended' })
    expect(joinTileStatus({ status: 'pending' })).toMatchObject({ label: 'pending', cls: 'text-muted' })
  })
})

describe('broadcastSelection', () => {
  const m = (name: string, live = true, waiting = false) => ({ name, live, waiting })

  it('selects every member whose session runs, and none that has none', () => {
    expect(broadcastSelection([m('lead'), m('core'), m('tests', false)], {})).toEqual({ selected: ['lead', 'core'], sending: ['lead', 'core'], waiting: [] })
  })

  it('keeps an untick through every read, and selects a member that starts later', () => {
    const choices = { core: false }
    expect(broadcastSelection([m('lead'), m('core'), m('tests', false)], choices).selected).toEqual(['lead'])
    expect(broadcastSelection([m('lead'), m('core'), m('tests')], choices).selected).toEqual(['lead', 'tests'])
  })

  it('keeps a tick on a member with no session yet, and sends only to those the server will type into', () => {
    const r = broadcastSelection([m('lead', true, true), m('core'), m('tests', false)], { tests: true })
    expect(r).toEqual({ selected: ['lead', 'core', 'tests'], sending: ['core'], waiting: ['lead'] })
  })
})

describe('taskProblems', () => {
  // The fixtures hold plain tasks, so an edit can reach their needs and checks.
  type Fixture = Omit<TaskShape, 'tasks' | 'members'> & { tasks: CrewTask[]; members: Array<{ name: string; start: CrewStart }> }
  const managed = (): Fixture => ({
    isolation: 'worktree',
    members: [
      { name: 'builder', start: { when: 'task' } },
      { name: 'reviewer', start: { when: 'task' } },
    ],
    tasks: [
      { id: 'build', member: 'builder', kind: 'work', instructions: 'Build it.', checks: [{ id: 'unit', argv: ['go', 'test', './...'], timeoutSeconds: 600 }] },
      { id: 'review', member: 'reviewer', kind: 'review', instructions: 'Review it.', needs: ['build'] },
    ],
    setup: [{ id: 'deps', argv: ['npm', 'ci'] }],
  })
  it('accepts a builder and its reviewer', () => {
    expect(taskProblems(managed())).toEqual([])
  })
  it('has no problem with a crew without tasks, unless a member starts by tasks or a setup is left', () => {
    expect(taskProblems({ isolation: 'none', members: [{ name: 'lead', start: { when: 'immediately' } }] })).toEqual([])
    expect(taskProblems({ isolation: 'none', members: [{ name: 'lead', start: { when: 'task' } }] })).toEqual(['lead starts by its tasks, but the crew has none'])
    expect(taskProblems({ isolation: 'none', members: [], setup: [{ id: 'x', argv: ['true'] }] })).toEqual(['Setup commands need tasks'])
  })
  it('names each rule the server has, in its words', () => {
    const cases: Array<[string, (c: Fixture) => void, string]> = [
      ['a member not by tasks', (c) => (c.members[0]!.start = { when: 'immediately' }), 'builder must start by its tasks'],
      ['no worktrees', (c) => (c.isolation = 'none'), 'gives each member a git worktree'],
      ['a bad id', (c) => (c.tasks![0]!.id = 'Build.1'), 'Task Build.1: Lower-case'],
      ['an id twice', (c) => (c.tasks![1]!.id = 'build'), 'Task build: Another one has this id'],
      ['an unknown member', (c) => (c.tasks![0]!.member = 'ghost'), 'ghost is not a member'],
      ['long instructions', (c) => (c.tasks![0]!.instructions = 'x'.repeat(8001)), 'at most 8000 characters'],
      ['a review with checks', (c) => (c.tasks![1]!.checks = [{ id: 'x', argv: ['true'] }]), 'a review has no checks'],
      ['a check with no program', (c) => (c.tasks![0]!.checks![0]!.argv = []), 'check 1: name a program'],
      ['a check timeout past the bound', (c) => (c.tasks![0]!.checks![0]!.timeoutSeconds = 3601), 'the timeout is 1 to 3600'],
      ['a need of itself', (c) => (c.tasks![0]!.needs = ['build']), 'cannot need itself'],
      ['an unknown need', (c) => (c.tasks![0]!.needs = ['ghost']), 'needs ghost, which is not a task'],
      ['a review needing two', (c) => (c.tasks![1]!.needs = ['build', 'build']), 'exactly one work task'],
      [
        'a cycle',
        (c) => {
          c.tasks!.push({ id: 'docs', member: 'builder', kind: 'work', instructions: '', needs: ['build'] })
          c.tasks![0]!.needs = ['docs']
        },
        'go round in a cycle: build needs docs needs build',
      ],
      ['a member with no task', (c) => c.members.push({ name: 'idle', start: { when: 'task' } }), 'idle has no task'],
      ['a setup command with a bad id', (c) => (c.setup![0]!.id = 'Deps'), 'Setup command 1: Lower-case'],
    ]
    for (const [name, edit, words] of cases) {
      const c = managed()
      edit(c)
      expect(taskProblems(c).join('\n'), name).toContain(words)
    }
  })
  it('holds a crew to the save-only rules apart, so an unchanged crew from before them still launches', () => {
    const twice = managed()
    twice.tasks!.push({ id: 'docs', member: 'builder', kind: 'work', instructions: '', needs: ['build', 'build', 'build'] })
    expect(taskSaveProblems(twice)).toEqual(['Task docs: needs build twice'])
    expect(taskProblems(twice)).toEqual([])
    const wide = String.fromCodePoint(0x1d11e) // four bytes in UTF-8, as JSON keeps it
    const big = managed()
    Object.assign(big, { goal: wide.repeat(2000) })
    Object.assign(big.members[0]!, { prompt: wide.repeat(4000) })
    big.tasks![0]!.instructions = wide.repeat(8000)
    expect(taskSaveProblems(big).join('\n')).toContain('Task build: its goal, role and instructions are')
    expect(taskProblems(big)).toEqual([])
    expect(taskRowProblems(big.tasks![0]!, big).join('\n')).toContain('its goal, role and instructions')
    expect(taskSaveProblems(managed())).toEqual([])
  })
  it("counts a task's text as the server writes it as JSON, case for case with internal/crew tasks_test.go", () => {
    // A goal, the builder's role and the build's instructions of 1500, 3000 and 5500 four-byte characters, `last` after the instructions:
    // 40,006 bytes as the task file keeps them before it. The server writes U+2028 and U+2029 escaped (six bytes, where JSON.stringify keeps
    // three) and a lone surrogate as U+FFFD (three, where JSON.stringify sends a six-byte escape), so JSON.stringify's own text would pass
    // the U+2028 and U+2029 texts past the bound and refuse the lone surrogate's at it (#419).
    const wide = String.fromCodePoint(0x1d11e)
    const sized = (last: string): Fixture => {
      const c = managed()
      c.goal = wide.repeat(1500)
      Object.assign(c.members[0]!, { prompt: wide.repeat(3000) })
      c.tasks[0]!.instructions = wide.repeat(5500) + last
      return c
    }
    const problems = (c: Fixture) => [...taskProblems(c), ...taskSaveProblems(c)]
    const past = ['Task build: its goal, role and instructions are 40961 bytes as its task file keeps them, more than 40960']
    const [ls, ps, lone] = [String.fromCharCode(0x2028), String.fromCharCode(0x2029), String.fromCharCode(0xd800)]
    // The escape the Go test puts in the body for a lone surrogate is what the web sends for one.
    expect(JSON.stringify(lone)).toBe('"\\' + 'ud800"')
    expect(problems(sized('y'.repeat(954)))).toEqual([])
    expect(problems(sized('y'.repeat(955)))).toEqual(past)
    expect(problems(sized('y'.repeat(948) + ls))).toEqual([])
    expect(problems(sized('y'.repeat(949) + ls))).toEqual(past)
    expect(problems(sized('y'.repeat(949) + ps))).toEqual(past)
    expect(problems(sized('y'.repeat(951) + lone))).toEqual([])
    expect(problems(sized('y'.repeat(952) + lone))).toEqual(past)
    // A backslash before u, d, 8, 0, 0 is text, not a lone surrogate's escape: seven bytes as JSON.
    const text = '\\' + 'ud800'
    expect(problems(sized('y'.repeat(947) + text))).toEqual([])
    expect(problems(sized('y'.repeat(948) + text))).toEqual(past)
  })
  it("keeps a row's problems apart", () => {
    const c = managed()
    c.tasks![0]!.member = 'ghost'
    expect(taskRowProblems(c.tasks![0]!, c)).toEqual(['Task build: ghost is not a member'])
    expect(taskRowProblems(c.tasks![1]!, c)).toEqual([])
  })
})

describe('tasks in the editor', () => {
  it('carries tasks and setup through toCrewInput, empty needs and checks left out', () => {
    const c = { ...defaultCrew([]), tasks: [{ id: 'a', member: 'lead', kind: 'work' as const, instructions: '', needs: [], checks: [] }], setup: [{ id: 's', argv: ['true'], timeoutSeconds: 0 }] }
    expect(toCrewInput(c).tasks).toEqual([{ id: 'a', member: 'lead', kind: 'work', instructions: '' }])
    expect(toCrewInput(c).setup).toEqual([{ id: 's', argv: ['true'] }])
    expect(toCrewInput(defaultCrew([]))).not.toHaveProperty('tasks')
  })
  it('makes a task id from the member name, not one taken', () => {
    expect(draftTask('builder')).toMatchObject({ id: 'builder', member: 'builder', kind: 'work', instructions: '', needKeys: [] })
    expect(draftTask('builder', ['builder']).id).toBe('builder-2')
    expect(draftTask('a.b', []).id).toBe('a-b')
  })
  it('a task follows its member by key: through a duplicate name, a blank, and keeps the last name of one gone', () => {
    const dev = { name: 'dev', key: 1 }
    const dev2 = { name: 'dev2', key: 2 }
    const a = { ...draftTask(dev), id: 'a' }
    const b = { ...draftTask(dev2), id: 'b' }
    expect([a.memberKey, b.memberKey]).toEqual([1, 2])
    // dev2 → dev (a duplicate for a moment) → dev3: a stays dev's.
    let tasks = tasksFollowMembers([a, b], [dev, { ...dev2, name: 'dev' }])
    expect(tasks.map((t) => t.member)).toEqual(['dev', 'dev'])
    tasks = tasksFollowMembers(tasks, [dev, { ...dev2, name: 'dev3' }])
    expect(tasks.map((t) => t.member)).toEqual(['dev', 'dev3'])
    // Cleared and retyped as another name.
    tasks = tasksFollowMembers(tasks, [{ ...dev, name: '' }, { ...dev2, name: 'dev3' }])
    expect(tasks[0]!.member).toBe('')
    tasks = tasksFollowMembers(tasks, [{ ...dev, name: 'builder' }, { ...dev2, name: 'dev3' }])
    expect(tasks[0]!.member).toBe('builder')
    // The member gone: the task keeps the last name, which the rules flag.
    tasks = tasksFollowMembers(tasks, [{ ...dev2, name: 'dev3' }])
    expect(tasks[0]!.member).toBe('builder')
    expect(taskProblems({ isolation: 'worktree', members: [{ name: 'dev3', start: { when: 'task' } }], tasks })).toContain('Task a: builder is not a member')
    // A saved crew's tasks get their members' keys.
    const d = toDraft({ ...info, isolation: 'worktree', tasks: [{ id: 'x', member: info.members[0]!.name, kind: 'work', instructions: 'X.' }] })
    expect(d.tasks![0]!.memberKey).toBe(d.members[0]!.key)
  })
  it('a task whose member is gone is flagged, even when another member carries the name now', () => {
    const dev = { name: 'dev', key: 1, start: { when: 'task' as const } }
    const t = { ...draftTask({ name: 'dev', key: 2 }), id: 'a', instructions: 'A.' }
    expect(taskProblems({ isolation: 'worktree', members: [dev], tasks: [t] })).toEqual(['Task a: its member is gone (dev): choose one'])
    expect(taskProblems({ isolation: 'worktree', members: [dev], tasks: [{ ...t, memberKey: 1 }] })).toEqual([])
    // Keyless members (a saved crew's rules) judge by name alone.
    expect(taskProblems({ isolation: 'worktree', members: [{ name: 'dev', start: { when: 'task' } }], tasks: [t] })).toEqual([])
  })
  it('reads a task start', () => {
    expect(startFrom('task')).toEqual({ when: 'task' })
    expect(startWords({ when: 'task' }).text).toBe('By its tasks')
    expect(startSentence([{ name: 'a', start: { when: 'task' } }, { name: 'b', start: { when: 'task' } }])).toBe('each does its tasks')
  })
})

describe('project manifests', () => {
  const task: CrewTask = { id: 'build', member: 'lead', kind: 'work', instructions: 'Build it.', checks: [{ id: 'unit', argv: ['go', 'test', './...'] }] }
  const managedInfo: CrewInfo = { ...info, members: [{ ...lead, start: { when: 'task' } }], tasks: [task] }
  /** A project manifest with every phase, valid beside the task's check "unit" (internal/crew manifest_test.go fullManifest). */
  const fullManifest = (): CrewManifest => ({
    prerequisites: [{ id: 'node', argv: ['node', '--version'] }],
    env: ['NPM_TOKEN'],
    ports: ['web', 'db'],
    setup: [{ id: 'deps', argv: ['npm', 'ci'], timeoutSeconds: 900 }],
    services: [
      { id: 'dbserver', argv: ['postgres', '-p', '${CONDUCTOR_PORT_DB}', '-D', '${CONDUCTOR_STATE_DIR}/db'] },
      { id: 'webserver', argv: ['npm', 'run', 'dev', '--', '--port', '${CONDUCTOR_PORT_WEB}'] },
    ],
    ready: [
      { id: 'dbready', argv: ['pg_isready', '-p', '${CONDUCTOR_PORT_DB}'] },
      { id: 'webready', url: 'http://127.0.0.1:${CONDUCTOR_PORT_WEB}/health', timeoutSeconds: 120, intervalSeconds: 1 },
    ],
    preview: 'http://localhost:${CONDUCTOR_PORT_WEB}/',
    verify: [{ id: 'e2e', argv: ['npm', 'run', 'e2e'] }],
    cleanup: [{ id: 'dropdb', argv: ['rm', '-rf', '${CONDUCTOR_STATE_DIR}/db'] }],
  })
  type Fixture = ManifestShape & { manifest: CrewManifest; tasks: CrewTask[] }
  const crew = (): Fixture => ({ tasks: [structuredClone(task)], manifest: fullManifest() })
  const steps = (prefix: string, n: number): CrewCheck[] => Array.from({ length: n }, (_, i) => ({ id: `${prefix}${i}`, argv: ['true'] }))

  it('sends a manifest as the server takes it, never the revision, and setup only while the crew has one', () => {
    const saved: CrewInfo = { ...managedInfo, manifest: { ...fullManifest(), env: [], setup: [{ id: 'deps', argv: ['npm', 'ci'], timeoutSeconds: 0, pending: 'x' } as CrewCheck] }, manifestRevision: 3 }
    const sent = wire(toCrewInput(saved))
    expect(sent).not.toHaveProperty('manifestRevision')
    expect(sent).not.toHaveProperty('setup')
    expect(sent.manifest.setup).toEqual([{ id: 'deps', argv: ['npm', 'ci'] }])
    expect(sent.manifest).not.toHaveProperty('env')
    expect(sent.manifest.ready).toEqual(fullManifest().ready)
    expect(Object.keys(sent.manifest)).toEqual(['prerequisites', 'ports', 'setup', 'services', 'ready', 'preview', 'verify', 'cleanup'])
    // An empty manifest is none; an empty setup is none.
    expect(wire(toCrewInput({ ...managedInfo, manifest: { setup: [], ready: [] }, setup: [] }))).not.toHaveProperty('manifest')
    expect(wire(toCrewInput({ ...managedInfo, setup: [] }))).not.toHaveProperty('setup')
    // A draft of a crew with a manifest is no change until something is edited; the text still being typed in a step is one.
    const d = toDraft(saved)
    expect(d.manifest?.setup).toEqual([{ id: 'deps', argv: ['npm', 'ci'] }])
    const stored: CrewInfo = { ...saved, manifest: { ...saved.manifest, setup: [{ id: 'deps', argv: ['npm', 'ci'] }] } }
    expect(crewKey(d)).toBe(crewKey(stored))
    expect(crewKey(saved)).not.toBe(crewKey(stored))
    expect(d.liftedSetup).toBeUndefined()
  })

  it('lifts a loaded crew\'s setup into the manifest for the editor, which then sends it as the manifest', () => {
    const saved: CrewInfo = { ...managedInfo, setup: [{ id: 'deps', argv: ['npm', 'ci'], timeoutSeconds: 120 }], manifestRevision: 0 }
    const lifted = liftSetup(saved)
    expect(lifted.setup).toBeUndefined()
    expect(lifted.manifest).toEqual({ setup: [{ id: 'deps', argv: ['npm', 'ci'], timeoutSeconds: 120 }] })
    lifted.manifest!.setup![0]!.argv.push('--quiet')
    expect(saved.setup![0]!.argv).toEqual(['npm', 'ci'])
    const sent = wire(toCrewInput(liftSetup(saved)))
    expect(sent).not.toHaveProperty('setup')
    expect(sent.manifest).toEqual({ setup: [{ id: 'deps', argv: ['npm', 'ci'], timeoutSeconds: 120 }] })
    // A crew with a manifest, or without a setup, stays as it is.
    const withManifest: CrewInfo = { ...managedInfo, manifest: fullManifest() }
    expect(liftSetup(withManifest)).toBe(withManifest)
    expect(liftSetup(managedInfo)).toBe(managedInfo)
  })

  it('has no problem with a manifest with every phase, every list at its bound, the file form alone, or none', () => {
    expect(manifestProblems(crew())).toEqual([])
    const c = crew()
    const r = c.manifest
    r.prerequisites = steps('p', TASK_LIMITS.manifestSteps)
    r.setup = steps('s', TASK_LIMITS.manifestSteps)
    r.verify = steps('v', TASK_LIMITS.manifestSteps)
    r.cleanup = steps('c', TASK_LIMITS.manifestSteps)
    r.services = steps('svc', TASK_LIMITS.manifestServices)
    r.ready = Array.from({ length: TASK_LIMITS.manifestSteps }, (_, i) => ({ id: `r${i}`, url: 'https://[::1]:8443/', timeoutSeconds: TASK_LIMITS.probeTimeout, intervalSeconds: TASK_LIMITS.probeInterval }))
    r.env = Array.from({ length: TASK_LIMITS.manifestEnv }, (_, i) => `VAR_${i}`)
    r.ports = ['a', 'b', 'c', 'd', 'e', 'f', 'web', 'p'.repeat(16)]
    const head = `http://127.0.0.1:\${CONDUCTOR_PORT_${'P'.repeat(16)}}/`
    r.preview = head + 'x'.repeat(TASK_LIMITS.preview - head.length)
    expect(manifestProblems(c)).toEqual([])
    expect(manifestProblems({ tasks: [task], manifest: { file: true } })).toEqual([])
    expect(manifestProblems({ tasks: [task], manifest: { setup: [{ id: 'deps', argv: ['npm', 'ci'] }] } })).toEqual([])
    expect(manifestProblems({ tasks: [], manifest: {} })).toEqual([])
    expect(manifestProblems({ tasks: [task] })).toEqual([])
  })

  const big = 'x'.repeat(TASK_LIMITS.arg)
  /** Each rule the server holds a manifest to, broken alone in a manifest that is valid otherwise: its name, the edit, and its words. */
  const ruleCases: Array<[string, (c: Fixture) => void, string]> = [
    ['a manifest without tasks', (c) => (c.tasks = []), 'A project manifest needs tasks'],
    ['setup beside the manifest', (c) => (c.setup = [{ id: 'old', argv: ['true'] }]), "setup is the manifest's setup phase, put it there"],
    ['schema in a crew\'s manifest', (c) => (c.manifest.schema = 1), 'it is only set in conductor.manifest.json'],
    ['file beside a phase', (c) => (c.manifest.file = true), 'set nothing else'],
    ['too many prerequisites', (c) => (c.manifest.prerequisites = steps('p', 9)), 'At most 8 prerequisites'],
    ['too many setup steps', (c) => (c.manifest.setup = steps('s', 9)), 'At most 8 setup steps'],
    ['too many services', (c) => (c.manifest.services = steps('svc', 5)), 'At most 4 services'],
    ['too many ready probes', (c) => c.manifest.ready!.push(...Array.from({ length: 7 }, (_, i) => ({ id: `r${i}`, argv: ['true'] }))), 'At most 8 ready probes'],
    ['too many verify steps', (c) => (c.manifest.verify = steps('v', 9)), 'At most 8 verify steps'],
    ['too many cleanup steps', (c) => (c.manifest.cleanup = steps('c', 9)), 'At most 8 cleanup steps'],
    ['too many env names', (c) => c.manifest.env!.push(...Array.from({ length: 32 }, (_, i) => `VAR_${i}`)), 'At most 32 env names'],
    ['too many ports', (c) => c.manifest.ports!.push('a', 'b', 'c', 'd', 'e', 'f', 'g'), 'At most 8 ports'],
    ['a step with a bad id', (c) => (c.manifest.setup![0]!.id = 'Deps'), 'Setup step 1: Lower-case'],
    ['a step with no program', (c) => (c.manifest.cleanup![0]!.argv = []), 'Cleanup step 1: name a program'],
    ["a step's argv past its bound", (c) => (c.manifest.verify![0]!.argv = ['x', big, big]), 'Verify step 1: the arguments are more than 8 KiB in all'],
    ["a step's timeout past the bound", (c) => (c.manifest.prerequisites![0]!.timeoutSeconds = 3601), 'Prerequisite 1: the timeout is 1 to 3600'],
    ['a service with a timeout', (c) => (c.manifest.services![0]!.timeoutSeconds = 60), 'Service 1: a service has no timeout'],
    ['an id in two lists', (c) => (c.manifest.cleanup![0]!.id = 'deps'), 'Cleanup step 1: Another one has this id'],
    ["a probe's id taken by a step", (c) => (c.manifest.ready![0]!.id = 'webserver'), 'Ready probe 1: Another one has this id'],
    ["an id of a task's check", (c) => (c.manifest.setup![0]!.id = 'unit'), 'Setup step 1: unit is also a check of task build'],
    ['a bad env name', (c) => (c.manifest.env = ['NPM-TOKEN']), 'Env NPM-TOKEN: letters, digits and _'],
    ['an env name starting with a digit', (c) => (c.manifest.env = ['1PASSWORD']), 'Env 1PASSWORD: letters, digits and _'],
    ['a CONDUCTOR_ env name', (c) => (c.manifest.env = ['CONDUCTOR_NOTIFY_TOKEN']), "Env CONDUCTOR_NOTIFY_TOKEN is Conductor's own"],
    ['a loader', (c) => (c.manifest.env = ['PATH', 'LD_PRELOAD']), "Env LD_PRELOAD is a loader's variable"],
    ['an env name twice', (c) => (c.manifest.env = ['NPM_TOKEN', 'NPM_TOKEN']), 'Env NPM_TOKEN is named twice'],
    ['a port name in capitals', (c) => (c.manifest.ports = ['Web']), 'Port Web: lower-case letters'],
    ['a port name past 16', (c) => (c.manifest.ports = ['p'.repeat(17)]), `Port ${'p'.repeat(17)}: lower-case letters`],
    ['a port name twice', (c) => (c.manifest.ports = ['web', 'db', 'web']), 'Port web is named twice'],
    ['a placeholder naming no port', (c) => (c.manifest.setup![0]!.argv = ['npm', 'ci', '--port=${CONDUCTOR_PORT_API}']), 'Setup step 1: ${CONDUCTOR_PORT_API} names no port of the manifest'],
    ['a port placeholder in lower case', (c) => (c.manifest.services![1]!.argv[5] = '${CONDUCTOR_PORT_web}'), 'Service 2: ${CONDUCTOR_PORT_web} names no port of the manifest'],
    ["a placeholder naming no port in a probe's URL", (c) => (c.manifest.ready![1]!.url = 'http://127.0.0.1:${CONDUCTOR_PORT_API}/'), 'Ready probe 2: the URL: ${CONDUCTOR_PORT_API} names no port'],
    ['a placeholder naming no port in the preview', (c) => (c.manifest.preview = 'http://localhost:${CONDUCTOR_PORT_UI}/'), 'The preview: ${CONDUCTOR_PORT_UI} names no port'],
    ['a port placeholder in a prerequisite', (c) => (c.manifest.prerequisites![0]!.argv = ['nc', '-z', '127.0.0.1', '${CONDUCTOR_PORT_DB}']), 'Prerequisite 1: a prerequisite runs before any tree: no placeholders'],
    ['the state directory in a prerequisite', (c) => (c.manifest.prerequisites![0]!.argv = ['mkdir', '${CONDUCTOR_STATE_DIR}/cache']), 'Prerequisite 1: a prerequisite runs before any tree: no placeholders'],
    ['a probe with argv and url', (c) => (c.manifest.ready![0]!.url = 'http://127.0.0.1/'), 'Ready probe 1: a probe has a command or a URL, not both'],
    ['a probe with neither', (c) => (c.manifest.ready![0]!.argv = []), 'Ready probe 1: give it a command or a URL'],
    ['a probe with a bad id', (c) => (c.manifest.ready![0]!.id = 'db.ready'), 'Ready probe 1: Lower-case'],
    ["a probe's argv with no program", (c) => (c.manifest.ready![0]!.argv = ['']), 'Ready probe 1: name a program'],
    ["a probe's URL off the loopback", (c) => (c.manifest.ready![1]!.url = 'http://example.com/health'), 'Ready probe 2: the URL must be http or https on 127.0.0.1, localhost or [::1]'],
    ["a probe's URL on a host that starts as the loopback", (c) => (c.manifest.ready![1]!.url = 'http://127.0.0.1.example.com/'), 'must be http or https on 127.0.0.1, localhost or [::1]'],
    ["a probe's URL with a user", (c) => (c.manifest.ready![1]!.url = 'http://localhost@example.com/'), 'must be http or https on 127.0.0.1, localhost or [::1]'],
    ["a probe's URL of another scheme", (c) => (c.manifest.ready![1]!.url = 'ftp://127.0.0.1/'), 'must be http or https on 127.0.0.1, localhost or [::1]'],
    ["a probe's URL with a space", (c) => (c.manifest.ready![1]!.url = 'http://127.0.0.1/a b'), 'must be http or https on 127.0.0.1, localhost or [::1]'],
    ["a probe's URL past 2048", (c) => (c.manifest.ready![1]!.url = 'http://127.0.0.1/' + 'x'.repeat(2048)), 'Ready probe 2: the URL is longer than 2048 bytes'],
    ["a probe's port of 0", (c) => (c.manifest.ready![1]!.url = 'http://127.0.0.1:0/'), 'the port must be 1 to 65535'],
    ["a probe's port past 65535", (c) => (c.manifest.ready![1]!.url = 'http://[::1]:65536/'), 'the port must be 1 to 65535'],
    ["a probe's timeout past the bound", (c) => (c.manifest.ready![1]!.timeoutSeconds = 3601), 'Ready probe 2: the timeout is 1 to 3600 seconds'],
    ["a probe's negative timeout", (c) => (c.manifest.ready![1]!.timeoutSeconds = -1), 'Ready probe 2: the timeout is 1 to 3600 seconds'],
    ["a probe's interval past the bound", (c) => (c.manifest.ready![1]!.intervalSeconds = 61), 'Ready probe 2: the interval is 1 to 60 seconds'],
    ['a preview off the loopback', (c) => (c.manifest.preview = 'https://preview.example.com/'), 'The preview must be http or https on 127.0.0.1, localhost or [::1]'],
    ['a preview past 512', (c) => (c.manifest.preview = 'http://localhost/' + 'x'.repeat(512)), 'The preview is longer than 512 bytes'],
    ['a manifest past 64 KiB as JSON', (c) => (c.manifest.setup = Array.from({ length: 8 }, (_, i) => ({ id: `s${i}`, argv: [big, big] }))), 'bytes as JSON, more than 65536'],
  ]

  it('names each rule the server holds a manifest to, item for item with internal/crew manifest_test.go', () => {
    for (const [name, edit, words] of ruleCases) {
      const c = crew()
      edit(c)
      expect(manifestProblems(c).join('\n'), name).toContain(words)
    }
    expect(ruleCases).toHaveLength(52)
  })

  it('counts a manifest as the server writes it as JSON, case for case with internal/crew manifest_test.go', () => {
    // Eight setup steps of two arguments, the last step's second one `last`: 61,457 bytes as JSON before it. The server writes U+2028 and
    // U+2029 escaped (six bytes, where JSON.stringify keeps three) and a lone surrogate as U+FFFD (three, where JSON.stringify sends a
    // six-byte escape), so JSON.stringify's own text would pass the U+2028 and U+2029 manifests past the bound and refuse the lone surrogate's
    // at it.
    const sized = (last: string): Fixture => ({
      tasks: [structuredClone(task)],
      manifest: { setup: Array.from({ length: 8 }, (_, i) => ({ id: `s${i}`, argv: ['x'.repeat(TASK_LIMITS.arg), i === 7 ? last : 'y'.repeat(4066)] })) },
    })
    const past = ['The project manifest is 65537 bytes as JSON, more than 65536']
    const [ls, ps, lone] = [String.fromCharCode(0x2028), String.fromCharCode(0x2029), String.fromCharCode(0xd800)]
    expect(manifestProblems(sized('y'.repeat(4079)))).toEqual([])
    expect(manifestProblems(sized('y'.repeat(4080)))).toEqual(past)
    expect(manifestProblems(sized('y'.repeat(4073) + ls))).toEqual([])
    expect(manifestProblems(sized('y'.repeat(4074) + ls))).toEqual(past)
    expect(manifestProblems(sized('y'.repeat(4074) + ps))).toEqual(past)
    expect(manifestProblems(sized('y'.repeat(4076) + lone))).toEqual([])
    expect(manifestProblems(sized('y'.repeat(4077) + lone))).toEqual(past)
  })

  it('marks each rule where the editor shows it: under the row or the field that breaks it, else at the head of the manifest', () => {
    const labels: Record<ManifestList, string> = { prerequisites: 'Prerequisite', setup: 'Setup step', services: 'Service', ready: 'Ready probe', verify: 'Verify step', cleanup: 'Cleanup step' }
    const where: Record<string, string> = {
      'a manifest without tasks': 'manifest',
      'setup beside the manifest': 'manifest',
      "schema in a crew's manifest": 'manifest',
      'file beside a phase': 'manifest',
      'too many prerequisites': 'manifest',
      'too many setup steps': 'manifest',
      'too many services': 'manifest',
      'too many ready probes': 'manifest',
      'too many verify steps': 'manifest',
      'too many cleanup steps': 'manifest',
      'too many env names': 'env',
      'too many ports': 'ports',
      'a step with a bad id': 'setup.0',
      'a step with no program': 'cleanup.0',
      "a step's argv past its bound": 'verify.0',
      "a step's timeout past the bound": 'prerequisites.0',
      'a service with a timeout': 'services.0',
      'an id in two lists': 'cleanup.0',
      "a probe's id taken by a step": 'ready.0',
      "an id of a task's check": 'setup.0',
      'a bad env name': 'env',
      'an env name starting with a digit': 'env',
      'a CONDUCTOR_ env name': 'env',
      'a loader': 'env',
      'an env name twice': 'env',
      'a port name in capitals': 'ports',
      'a port name past 16': 'ports',
      'a port name twice': 'ports',
      'a placeholder naming no port': 'setup.0',
      'a port placeholder in lower case': 'services.1',
      "a placeholder naming no port in a probe's URL": 'ready.1',
      'a placeholder naming no port in the preview': 'preview',
      'a port placeholder in a prerequisite': 'prerequisites.0',
      'the state directory in a prerequisite': 'prerequisites.0',
      'a probe with argv and url': 'ready.0',
      'a probe with neither': 'ready.0',
      'a probe with a bad id': 'ready.0',
      "a probe's argv with no program": 'ready.0',
      "a probe's URL off the loopback": 'ready.1',
      "a probe's URL on a host that starts as the loopback": 'ready.1',
      "a probe's URL with a user": 'ready.1',
      "a probe's URL of another scheme": 'ready.1',
      "a probe's URL with a space": 'ready.1',
      "a probe's URL past 2048": 'ready.1',
      "a probe's port of 0": 'ready.1',
      "a probe's port past 65535": 'ready.1',
      "a probe's timeout past the bound": 'ready.1',
      "a probe's negative timeout": 'ready.1',
      "a probe's interval past the bound": 'ready.1',
      'a preview off the loopback': 'preview',
      'a preview past 512': 'preview',
      'a manifest past 64 KiB as JSON': 'manifest',
    }
    expect(Object.keys(where).sort()).toEqual(ruleCases.map(([name]) => name).sort())
    for (const [name, edit, words] of ruleCases) {
      const c = crew()
      edit(c)
      const marks = manifestMarks(c)
      const [place, row] = where[name]!.split('.') as [string, string | undefined]
      let shown: string
      if (row !== undefined) shown = `${labels[place as ManifestList]} ${Number(row) + 1}: ${marks.rows[place as ManifestList][Number(row)]}`
      else if (place === 'manifest') shown = marks.manifest.join('\n')
      else shown = marks[place as 'env' | 'ports' | 'preview']
      expect(shown, name).toContain(words)
    }
    // A manifest with no problem has no mark: a row for each step and probe, each empty.
    expect(manifestMarks(crew())).toEqual({ rows: { prerequisites: [''], setup: [''], services: ['', ''], ready: ['', ''], verify: [''], cleanup: [''] }, env: '', ports: '', preview: '', manifest: [] })
    // A row's first problem alone, without its label; the rest wait for it.
    const twice = crew()
    twice.manifest.setup![0]!.id = 'unit'
    twice.manifest.setup![0]!.argv = []
    expect(manifestMarks(twice).rows.setup).toEqual(['name a program'])
    expect(manifestProblems(twice)).toEqual(['Setup step 1: name a program', "Setup step 1: unit is also a check of task build: a step's id is its own"])
  })

  it('holds a probe whose command is still being typed, as a step is held', () => {
    const c = crew()
    c.manifest.ready![0] = { id: 'dbready', by: 'command', argv: ['pg_isready'], pending: '"-p' }
    expect(manifestProblems(c)).toEqual(['Ready probe 1: an argument is still being typed: close its quote, or press Enter'])
    expect(manifestMarks(c).rows.ready).toEqual(['an argument is still being typed: close its quote, or press Enter', ''])
    expect(toCrewInput({ ...managedInfo, manifest: c.manifest }).manifest?.ready?.[0]).toEqual({ id: 'dbready', argv: ['pg_isready'] })
  })

  it("holds a crew to its manifest's rules with its tasks', the manifest's last as the server checks it, never under a task's row", () => {
    const d = toDraft({ ...managedInfo, manifest: { setup: [{ id: 'Deps', argv: ['npm', 'ci'] }] } })
    const badId = 'Setup step 1: Lower-case letters, digits, "_" and "-", starting with a letter or digit, at most 40, no "."'
    expect(taskProblems(d)).toEqual([badId])
    expect(taskSaveProblems(d)).toEqual([])
    expect(taskRowProblems(d.tasks![0]!, d)).toEqual([])
    const long = { ...d, tasks: d.tasks!.map((t) => ({ ...t, instructions: 'x'.repeat(TASK_LIMITS.instructions + 1) })) }
    expect(taskProblems(long)).toEqual([`Task build: the instructions are at most ${TASK_LIMITS.instructions} characters`, badId])
    // Without tasks, a manifest is a problem of its own.
    expect(taskProblems({ isolation: 'none', members: [], manifest: { setup: [{ id: 'deps', argv: ['true'] }] } })).toEqual(['A project manifest needs tasks'])
    expect(taskProblems({ isolation: 'none', members: [], manifest: {} })).toEqual([])
  })

  it("lifts a stored setup into the draft's manifest: no change by itself, saved as the manifest, and the crew as stored still launches", () => {
    // A legacy setup may share an id with a task's check (main's rule for it); a manifest's step may not.
    const saved: CrewInfo = { ...managedInfo, setup: [{ id: 'unit', argv: ['npm', 'ci'], timeoutSeconds: 120 }] }
    const d = toDraft(saved)
    expect(d.setup).toBeUndefined()
    expect(d.manifest).toEqual({ setup: [{ id: 'unit', argv: ['npm', 'ci'], timeoutSeconds: 120 }] })
    expect(d.liftedSetup).toEqual(saved.setup)
    expect(crewKey(d)).toBe(crewKey(saved))
    const sent = wire(toCrewInput(d))
    expect(sent).not.toHaveProperty('setup')
    expect(sent).not.toHaveProperty('liftedSetup')
    expect(sent.manifest).toEqual({ setup: [{ id: 'unit', argv: ['npm', 'ci'], timeoutSeconds: 120 }] })

    // The clash is the row's problem and holds the save; the launch, of the crew as stored, it does not hold.
    const clash = "Setup step 1: unit is also a check of task build: a step's id is its own"
    expect(manifestMarks(d).rows.setup).toEqual(["unit is also a check of task build: a step's id is its own"])
    expect(taskProblems(d)).toEqual([])
    expect(taskSaveProblems(d)).toEqual([clash])
    // Another field edited: the save, which a launch makes first, is still held.
    const goal = { ...d, goal: 'changed' }
    expect(crewKey(goal)).not.toBe(crewKey(saved))
    expect(taskSaveProblems(goal)).toEqual([clash])
    // The id changed: nothing holds anything, and the crew is saved as its manifest.
    const renamed = { ...d, manifest: { setup: [{ ...d.manifest!.setup![0]!, id: 'deps' }] } }
    expect(crewKey(renamed)).not.toBe(crewKey(saved))
    expect([...taskProblems(renamed), ...taskSaveProblems(renamed)]).toEqual([])
    // The manifest edited otherwise with the clash left: it is the manifest's own now, and holds the launch too.
    const edited = { ...d, manifest: { ...d.manifest, prerequisites: [{ id: 'node', argv: ['node', '--version'] }] } }
    expect(taskProblems(edited)).toEqual([clash])
    expect(taskSaveProblems(edited)).toEqual([])
    // Edited back to the setup as stored: the crew as stored again.
    const back = { ...edited, manifest: { setup: d.manifest!.setup } }
    expect(crewKey(back)).toBe(crewKey(saved))
    expect(taskProblems(back)).toEqual([])

    // Any rule a manifest has and a legacy setup has not is the same: a port placeholder, which a manifest without ports refuses.
    const placeholder = toDraft({ ...managedInfo, setup: [{ id: 'deps', argv: ['npm', 'ci', '--port=${CONDUCTOR_PORT_WEB}'] }] })
    expect(taskProblems(placeholder)).toEqual([])
    expect(taskSaveProblems(placeholder)).toEqual(['Setup step 1: ${CONDUCTOR_PORT_WEB} names no port of the manifest'])
    // A crew stored with a manifest has nothing lifted, and is held to its rules for both.
    expect(toDraft({ ...managedInfo, manifest: { setup: [{ id: 'deps', argv: ['true'] }] } }).liftedSetup).toBeUndefined()
  })

  it('keeps the lists the file switch set aside in the draft, never sent, through a save or a read of the crew while the file is on', () => {
    const lists = { setup: [{ id: 'deps', argv: ['npm', 'ci'] }], ports: ['web'] }
    const fileCrew: CrewInfo = { ...managedInfo, manifest: { file: true }, manifestRevision: 2 }
    const d = { ...toDraft(fileCrew), manifest: { file: true, parked: lists } }
    // Parked, the lists are no part of what is sent, of what is unsaved, or of the rules: the file form alone.
    expect(wire(toCrewInput(d)).manifest).toEqual({ file: true })
    expect(crewKey(d)).toBe(crewKey(fileCrew))
    expect(manifestProblems(d)).toEqual([])
    expect(taskProblems(d)).toEqual([])
    // A save or a read hands back the crew as the server has it: the lists stay while its manifest is still the file.
    expect(keepParked(toDraft(fileCrew), d).manifest).toEqual({ file: true, parked: lists })
    const inline: CrewInfo = { ...managedInfo, manifest: { setup: [{ id: 'other', argv: ['true'] }] } }
    expect(keepParked(toDraft(inline), d).manifest).toEqual({ setup: [{ id: 'other', argv: ['true'] }] })
    const none = toDraft(fileCrew)
    expect(keepParked(none, toDraft(fileCrew))).toBe(none)
    expect(keepParked(none, undefined)).toBe(none)
  })

  it('keeps a duration as typed, so a negative or a fraction is marked rather than turned into one nobody typed', () => {
    expect(typedSeconds('')).toBeUndefined()
    expect(typedSeconds(' ')).toBeUndefined()
    expect(typedSeconds('30')).toBe(30)
    expect(typedSeconds('-1')).toBe(-1)
    expect(typedSeconds('1.5')).toBe(1.5)
    expect(typedSeconds('3600.5')).toBe(3600.5)
    expect([undefined, 0, -1, 1.5].map(secondsText)).toEqual(['', '0', '-1', '1.5'])
    const c = crew()
    const probe = c.manifest.ready![1]!
    for (const [text, rule] of [
      ['-1', 'timeout'],
      ['1.5', 'timeout'],
      [String(TASK_LIMITS.probeTimeout + 0.5), 'timeout'],
    ] as const) {
      c.manifest.ready![1] = { ...probe, timeoutSeconds: typedSeconds(text) }
      expect(manifestProblems(c), text).toEqual([`Ready probe 2: the ${rule} is 1 to ${TASK_LIMITS.probeTimeout} seconds`])
    }
    c.manifest.ready![1] = { ...probe, intervalSeconds: typedSeconds('0.5') }
    expect(manifestProblems(c)).toEqual([`Ready probe 2: the interval is 1 to ${TASK_LIMITS.probeInterval} seconds`])
    c.manifest.ready![1] = { ...probe, timeoutSeconds: typedSeconds(''), intervalSeconds: typedSeconds('') }
    expect(manifestProblems(c)).toEqual([])
    // A step's timeout, and a task's check's, the same.
    expect(checkProblems({ id: 'deps', argv: ['true'], timeoutSeconds: typedSeconds('-1') }, 'Setup step 1')).toEqual([`Setup step 1: the timeout is 1 to ${TASK_LIMITS.timeout} seconds`])
    expect(checkProblems({ id: 'deps', argv: ['true'], timeoutSeconds: typedSeconds('2.5') }, 'Setup step 1')).toEqual([`Setup step 1: the timeout is 1 to ${TASK_LIMITS.timeout} seconds`])
  })

  it("names every step's and probe's id of a manifest, for a task's new check to avoid", () => {
    expect(manifestIds(fullManifest())).toEqual(['node', 'deps', 'dbserver', 'webserver', 'dbready', 'webready', 'e2e', 'dropdb'])
    expect(manifestIds(undefined)).toEqual([])
    expect(manifestIds({ file: true, parked: { setup: [{ id: 'deps', argv: ['true'] }] } })).toEqual([])
  })
})
