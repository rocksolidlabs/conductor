import { afterEach, describe, expect, it, vi } from 'vitest'
import type { RunInfo, RunMember, SessionInfo } from '~/composables/useSessions'
import { RUN_EVENT_LIST_AT, RUN_EVENT_WINDOW_MS, RunStore, memberOffer, runOffer, sessionResumeOffered, runLive, runState, type OfferRun, type RunReader } from './runs'

const member = (p: Partial<RunMember>): RunMember => ({ name: 'lead', agentId: 'claude', start: { when: 'immediately' }, status: 'running', ...p })

const run = (p: Partial<RunInfo> = {}): RunInfo => ({
  id: 'api-sweep-1a2b3c4d',
  crewId: 'api-sweep',
  name: 'API sweep',
  goal: 'ship',
  cwd: '/srv/api',
  isolation: 'worktree',
  startedAt: '2026-10-01T10:00:00Z',
  members: [],
  log: [],
  state: 'running',
  needsInput: 0,
  yolo: false,
  ...p,
})

const session = (p: Partial<SessionInfo>): SessionInfo =>
  ({ id: 's1', name: 'lead', kind: 'server', agentId: 'claude', command: [], cwd: '/w', status: 'running', cols: 80, rows: 24, viewers: 0, createdAt: '2026-10-01T10:00:00Z', ...p }) as SessionInfo

describe('runState', () => {
  // The rows of TestRunState in internal/crew/prompt_test.go, from the run as read (no live session).
  const m = (status: RunMember['status'], needsInput = false) => member({ name: `${status}${Math.random()}`, status, needsInput })
  it.each([
    ['all pending', run({ members: [m('pending')] }), 'running', 0],
    ['one running', run({ members: [m('running'), m('pending')] }), 'running', 0],
    ['two waiting', run({ members: [m('running', true), m('starting', true), m('ended')] }), 'needs_input', 2],
    ['ended and pending', run({ members: [m('ended'), m('pending')] }), 'running', 0],
    ['all ended', run({ members: [m('ended'), m('ended')] }), 'finished', 0],
    ['stopped', run({ stoppedAt: '2026-10-01T11:00:00Z', members: [m('ended')] }), 'stopped', 0],
  ] as const)('%s', (_, r, state, needs) => {
    expect(runState(r, [])).toEqual({ state, needs })
  })

  it('follows the live sessions: a prompt raised, a session ended', () => {
    const r = run({ members: [member({ sessionId: 's1' })] })
    const crew = { runId: r.id, crewId: r.crewId, member: 'lead' }
    expect(runState(r, [session({ crew, attention: { state: 'needs_input' } })])).toEqual({ state: 'needs_input', needs: 1 })
    expect(runState(r, [session({ crew, attention: { state: 'done' } })])).toEqual({ state: 'running', needs: 0 })
    expect(runState(r, [session({ crew, status: 'exited' })])).toEqual({ state: 'finished', needs: 0 })
  })
  it('a managed run is finished by its outcome, not by its sessions ending', () => {
    const member = (name: string, status: RunMember['status']): RunMember => ({ name, agentId: 'claude', start: { when: 'task' }, status, sessionId: name === 'lead' ? 's1' : undefined })
    const base: Omit<RunInfo, 'members'> = { id: 'r', crewId: 'c', name: 'c', goal: '', cwd: '/w', isolation: 'worktree', startedAt: '', log: [], state: 'running', needsInput: 0, yolo: false }
    const running: RunInfo = { ...base, managed: { revision: 3 }, members: [member('lead', 'running')], tasks: [] }
    expect(runState(running, []).state).toBe('running')
    const done: RunInfo = { ...running, outcome: 'succeeded' }
    expect(runState(done, []).state).toBe('finished')
    const ended: RunInfo = { ...running, members: [member('lead', 'ended')] }
    expect(runState(ended, []).state).toBe('running')
    const legacy: RunInfo = { ...base, members: [member('lead', 'ended')] }
    expect(runState(legacy, []).state).toBe('finished')
  })
})

// A run's words (its sentence, "Ended" or its outcome for a run that ended) and a member's mark are status.test.ts's: runStatusLine and
// memberStatusOf.
describe('runLive', () => {
  it('says which runs still go', () => {
    expect([runLive('running'), runLive('needs_input'), runLive('stopped'), runLive('finished')]).toEqual([true, true, false, false])
  })
})

describe('runOffer and memberOffer', () => {
  const plain: OfferRun = {}
  const managed: OfferRun = { managed: { revision: 3 } }
  const brakeman: OfferRun = { class: 'brakeman' }
  const stopped: OfferRun = { stoppedAt: '2026-11-14T12:00:00Z' }

  const record: OfferRun = { record: true }

  it('offers Add agent on a plain run not stopped; never on a managed run, a Brakeman turn, a record or a run not known', () => {
    expect(runOffer(plain)).toEqual({ byHand: true, add: true })
    expect(runOffer(stopped)).toEqual({ byHand: true, add: false })
    expect(runOffer(record)).toEqual({ byHand: false, add: false })
    expect(runOffer({ ...stopped, ...record })).toEqual({ byHand: false, add: false })
    expect(runOffer(managed)).toEqual({ byHand: false, add: false })
    expect(runOffer(brakeman)).toEqual({ byHand: false, add: false })
    expect(runOffer(undefined)).toEqual({ byHand: false, add: false })
    expect(runOffer(null)).toEqual({ byHand: false, add: false })
  })

  it('offers Start now for a waiting member of a run not stopped, by hand, as the server takes it', () => {
    expect(memberOffer('pending', plain)).toBe('start')
    expect(memberOffer('pending', managed)).toBeNull()
    expect(memberOffer('pending', brakeman)).toBeNull()
    expect(memberOffer('pending', stopped)).toBeNull()
    expect(memberOffer('pending', undefined)).toBeNull()
    expect((['starting', 'running', 'needs_input', undefined] as const).map((s) => memberOffer(s, plain))).toEqual([null, null, null, null])
  })

  it('offers Resume or Relaunch for an ended member, in a stopped run too, never in a managed run, a Brakeman turn or a run not known', () => {
    expect(memberOffer('ended', plain)).toBe('resume')
    expect(memberOffer('ended', stopped)).toBe('resume')
    expect(memberOffer('ended', { ...stopped, ...managed })).toBeNull()
    expect(memberOffer('ended', managed)).toBeNull()
    expect(memberOffer('ended', brakeman)).toBeNull()
    expect(memberOffer('ended', { ...stopped, ...brakeman })).toBeNull()
    expect(memberOffer('ended', undefined)).toBeNull()
  })

  it('offers nothing on a record: no Start now for its waiting members, no Resume or Relaunch for its ended ones, stopped or not', () => {
    expect(memberOffer('pending', record)).toBeNull()
    expect(memberOffer('ended', record)).toBeNull()
    expect(memberOffer('pending', { ...stopped, ...record })).toBeNull()
    expect(memberOffer('ended', { ...stopped, ...record })).toBeNull()
  })

  it("offers an ended session's Resume or Relaunch by its run's memberOffer: never for a managed run's attempt, a Brakeman turn or a run not held", () => {
    const crew = { runId: 'r', crewId: 'c', member: 'lead' }
    const of = (run: OfferRun | undefined) => (id: string) => (id === 'r' ? run : undefined)
    expect(sessionResumeOffered({}, of(undefined))).toBe(true)
    expect(sessionResumeOffered({ crew }, of(plain))).toBe(true)
    expect(sessionResumeOffered({ crew }, of(stopped))).toBe(true)
    expect(sessionResumeOffered({ crew }, of({ ...stopped, ...record }))).toBe(false)
    expect(sessionResumeOffered({ crew }, of(undefined))).toBe(false)
    expect(sessionResumeOffered({ crew: { ...crew, task: 'build', attempt: 2 } }, of(managed))).toBe(false)
    expect(sessionResumeOffered({ crew: { ...crew, brakeman: { request: 'q1', quiet: false } } }, of(brakeman))).toBe(false)
  })
})

function reader(over: Partial<RunReader> = {}): RunReader & { gets: string[]; lists: number } {
  const r = {
    gets: [] as string[],
    lists: 0,
    get: async (id: string) => {
      r.gets.push(id)
      return run({ id })
    },
    list: async () => {
      r.lists++
      return [] as RunInfo[]
    },
    ...over,
  }
  return r
}

describe('runState', () => {
  it('takes a record as recorded, its interrupted state included, since nothing of it is live', () => {
    const r = run({ id: 'r1', state: 'interrupted', needsInput: 0, record: true, members: [{ name: 'a', agentId: 'cat', start: { when: 'immediately' }, status: 'ended' }] })
    expect(runState(r, [])).toEqual({ state: 'interrupted', needs: 0 })
  })
})

describe('RunStore', () => {
  it('hands a record back without taking it among the live runs, and drops the run it replaces', async () => {
    const r = reader({ get: async (id) => (id === 'r1' ? run({ id: 'r1', state: 'stopped', record: true }) : null) })
    const store = new RunStore(r, () => {})
    store.apply(run({ id: 'r1', state: 'running' }))
    const got = await store.read('r1')
    expect(got?.record).toBe(true)
    expect(store.runs.get('r1')).toBeUndefined()
    expect(store.list()).toEqual([])
  })

  afterEach(() => vi.useRealTimers())

  it('never lets an older reply replace a newer one', async () => {
    let release!: (r: RunInfo) => void
    const slow = new Promise<RunInfo>((res) => (release = res))
    let calls = 0
    const store = new RunStore(reader({ get: async () => (++calls === 1 ? slow : run({ id: 'r1', state: 'stopped' })) }), () => {})
    const first = store.read('r1')
    await store.read('r1') // asked later, answered first
    release(run({ id: 'r1', state: 'running' }))
    await first
    expect(store.runs.get('r1')?.state).toBe('stopped')
  })

  it('drops a run the server forgot, and an older read does not bring it back', async () => {
    let release!: (r: RunInfo) => void
    const store = new RunStore(reader({ get: () => new Promise<RunInfo>((res) => (release = res)) }), () => {})
    store.apply(run({ id: 'r1' }))
    const late = store.read('r1')
    store.remove('r1')
    release(run({ id: 'r1' }))
    await late
    expect(store.runs.has('r1')).toBe(false)
  })

  it('gathers events for a window and reads each run named once', async () => {
    vi.useFakeTimers()
    const r = reader()
    const store = new RunStore(r, () => {})
    store.schedule('a')
    store.schedule('a')
    store.schedule('b')
    expect(r.gets).toEqual([])
    await vi.advanceTimersByTimeAsync(RUN_EVENT_WINDOW_MS)
    expect(r.gets.sort()).toEqual(['a', 'b'])
    expect(store.runs.size).toBe(2)
  })

  it('reads every run with one list when more than RUN_EVENT_LIST_AT are named in a window', async () => {
    vi.useFakeTimers()
    const r = reader({ list: async () => [run({ id: 'x' })] })
    const store = new RunStore(r, () => {})
    for (let i = 0; i <= RUN_EVENT_LIST_AT; i++) store.schedule(`r${i}`)
    await vi.advanceTimersByTimeAsync(RUN_EVENT_WINDOW_MS)
    expect(r.gets).toEqual([])
    expect([...store.runs.keys()]).toEqual(['x'])
  })

  it('keeps a run a list does not have when a later read of it was applied, and forgets one it does not have otherwise', () => {
    const store = new RunStore(reader(), () => {})
    store.apply(run({ id: 'old' }), 1)
    store.apply(run({ id: 'new' }), 5)
    store.applyList([], 3)
    expect([...store.runs.keys()]).toEqual(['new'])
  })

  it('lists the newest first, and forgets all on clear, replies in flight included', async () => {
    let release!: (r: RunInfo) => void
    const store = new RunStore(reader({ get: () => new Promise<RunInfo>((res) => (release = res)) }), () => {})
    store.apply(run({ id: 'a', startedAt: '2026-10-01T09:00:00Z' }))
    store.apply(run({ id: 'b', startedAt: '2026-10-01T10:00:00Z' }))
    expect(store.list().map((r) => r.id)).toEqual(['b', 'a'])
    const late = store.read('c')
    store.clear()
    release(run({ id: 'c' }))
    await late
    expect(store.runs.size).toBe(0)
  })
})

describe('runState of a managed run', () => {
  it('is finished by its outcome, not by its sessions ending', () => {
    const member = (name: string, status: RunMember['status']): RunMember => ({ name, agentId: 'claude', start: { when: 'task' }, status, sessionId: name === 'lead' ? 's1' : undefined })
    const base: Omit<RunInfo, 'members'> = { id: 'r', crewId: 'c', name: 'c', goal: '', cwd: '/w', isolation: 'worktree', startedAt: '', log: [], state: 'running', needsInput: 0, yolo: false }
    const running: RunInfo = { ...base, managed: { revision: 3 }, members: [member('lead', 'running')], tasks: [] }
    expect(runState(running, []).state).toBe('running')
    const done: RunInfo = { ...running, outcome: 'succeeded' }
    expect(runState(done, []).state).toBe('finished')
    const ended: RunInfo = { ...running, members: [member('lead', 'ended')] }
    expect(runState(ended, []).state).toBe('running')
    const legacy: RunInfo = { ...base, members: [member('lead', 'ended')] }
    expect(runState(legacy, []).state).toBe('finished')
  })
})
