import { describe, expect, it } from 'vitest'
import type { RunStep, RunTask } from '~/composables/useSessions'
import {
  CommandRefused,
  commandBody,
  mintCommandId,
  prerequisitesRefused,
  sendCommand,
  stepCommand,
  stepDuration,
  stepHasLog,
  stepManifest,
  stepPorts,
  stepWhere,
  stepWords,
  taskActions,
  taskBadges,
  taskCounts,
  taskWords,
  type CommandBody,
  type Receipt,
} from './tasks'

const task = (p: Partial<RunTask>): RunTask => ({ id: 'build', member: 'lead', kind: 'work', state: 'ready', attempts: 0, ...p })

describe('taskCounts and taskBadges', () => {
  const tasks = [
    task({ id: 'a', state: 'succeeded' }),
    task({ id: 'b', state: 'failed' }),
    task({ id: 'c', state: 'running' }),
    task({ id: 'd', state: 'verifying' }),
    task({ id: 'e', state: 'blocked' }),
    task({ id: 'f', state: 'ready' }),
    task({ id: 'g', state: 'cancelled' }),
  ]
  it('counts every state', () => {
    expect(taskCounts(tasks)).toEqual({ total: 7, done: 1, failed: 1, cancelled: 1, running: 1, checking: 1, waiting: 2 })
    expect(taskCounts(undefined).total).toBe(0)
  })
  it('says the counts as badges, the wrong and the moving ones only when there are any', () => {
    expect(taskBadges({ tasks, paused: true })).toEqual([
      { label: '1/7 tasks', color: 'neutral' },
      { label: '1 failed', color: 'error' },
      { label: '1 cancelled', color: 'neutral' },
      { label: '1 running', color: 'success' },
      { label: '1 checking', color: 'info' },
      { label: 'paused', color: 'warning', title: 'Paused by a person' },
    ])
    expect(taskBadges({ tasks: [task({ state: 'succeeded' })], outcome: 'succeeded' })).toEqual([{ label: '1/1 tasks', color: 'success' }])
  })
  it("titles a pause the run made itself with its words, and says cleaning up while trees are being ended (EN1)", () => {
    const why = 'the prerequisite node failed: exited 127: node: not found'
    expect(taskBadges({ tasks: [task({})], paused: true, pausedWhy: why })).toContainEqual({ label: 'paused', color: 'warning', title: `Paused: ${why}` })
    expect(taskBadges({ tasks: [task({ state: 'succeeded' })], outcome: 'succeeded', cleaning: 2 })).toEqual([
      { label: '1/1 tasks', color: 'success' },
      { label: 'cleaning up', color: 'info', title: '2 trees are being ended: services stopped, cleanup steps run' },
    ])
    expect(taskBadges({ tasks: [], cleaning: 1 }).at(-1)?.title).toBe('1 tree is being ended: services stopped, cleanup steps run')
    expect(taskBadges({ tasks: [], cleaning: 0 }).map((b) => b.label)).toEqual(['0/0 tasks'])
  })
})

describe('taskWords', () => {
  const run = {}
  it.each([
    [task({ state: 'blocked', needs: ['build'], reason: 'needs build' }), 'needs build', 'muted'],
    [task({ state: 'blocked', needs: ['build'], reason: 'needs build, which will not succeed' }), 'needs build, which will not succeed', 'error'],
    [task({ state: 'ready' }), 'ready to start', 'default'],
    [task({ state: 'ready', reason: 'attempt 1 failed at check go: exited 1' }), 'attempt 1 failed at check go: exited 1; ready to start', 'default'],
    [task({ state: 'running', attempts: 2, attempt: { n: 2, state: 'running' } }), 'attempt 2 running', 'default'],
    [task({ state: 'running', attempts: 1, attempt: { n: 1, state: 'running' }, blocker: { reason: 'no token', by: 'lead', at: '' } }), 'attempt 1 blocked by lead: no token', 'warning'],
    [task({ state: 'verifying', attempts: 1, attempt: { n: 1, state: 'verifying' } }), 'attempt 1: checking the result', 'default'],
    [task({ state: 'succeeded', result: { attempt: 2, commit: 'abc', summary: 'built it', at: '' } }), 'succeeded by attempt 2: built it', 'success'],
    [task({ state: 'succeeded', kind: 'review', result: { attempt: 1, verdict: 'approved', at: '' } }), 'approved', 'success'],
    [task({ state: 'succeeded', kind: 'review', result: { attempt: 1, verdict: 'needs_changes', findings: [{ severity: 'major', message: 'x' }], at: '' } }), 'asked for changes (1 findings)', 'success'],
    [task({ state: 'failed', reason: 'attempt 3 failed at check go: exited 1' }), 'attempt 3 failed at check go: exited 1', 'error'],
    [task({ state: 'cancelled' }), 'cancelled', 'muted'],
  ] as const)('%o → %s', (t, text, tone) => {
    expect(taskWords(t, run)).toEqual({ text, tone })
  })
  it('says why a ready task does not start, and what a continuation takes up', () => {
    expect(taskWords(task({ state: 'ready' }), { paused: true })).toEqual({ text: 'ready, scheduling is paused', tone: 'warning' })
    expect(taskWords(task({ state: 'ready' }), { stoppedAt: 'x' })).toEqual({ text: 'ready, the run is stopped', tone: 'muted' })
    expect(taskWords(task({ state: 'ready', reason: 'attempt 2 was interrupted by the server\'s end: the run continues it' }), { state: 'interrupted' })).toEqual({
      text: "attempt 2 was interrupted by the server's end: the run continues it; taken up when the run is continued",
      tone: 'warning',
    })
    expect(taskWords(task({ state: 'succeeded', importedFrom: 'r-1/build/1', result: { attempt: 1, commit: 'abc', summary: 'built', at: '' } }), {})).toEqual({ text: 'succeeded by attempt 1 (kept from the run continued): built', tone: 'success' })
  })
  it("says the dispatch is paused over the task's own reason when the run paused itself (EN1), and as before for a person's pause", () => {
    const why = 'attempt 1 could not be made usable at setup deps: exited 1: npm ERR! network'
    expect(taskWords(task({ state: 'ready', reason: why }), { paused: true, pausedWhy: why })).toEqual({ text: `ready, the dispatch is paused: ${why}`, tone: 'warning' })
    // A person paused: the reason stays first, the pause after it.
    expect(taskWords(task({ state: 'ready', reason: why }), { paused: true })).toEqual({ text: `${why}; ready, scheduling is paused`, tone: 'warning' })
    // The run paused itself over another task: this one has no reason of its own.
    expect(taskWords(task({ state: 'ready' }), { paused: true, pausedWhy: why })).toEqual({ text: 'ready, scheduling is paused', tone: 'warning' })
    // Unpaused, the reason reads as it did.
    expect(taskWords(task({ state: 'ready', reason: why }), { pausedWhy: '' })).toEqual({ text: `${why}; ready to start`, tone: 'default' })
  })
  it("never presents a reason that is not the pause's as its cause (the reviews of #502)", () => {
    // A continuation's task keeps the reason its interrupted attempt left; the continuation's prerequisites then fail.
    const kept = "attempt 2 was interrupted by the server's end: the run continues it"
    const prereq = 'the prerequisite tool failed: exited 1'
    expect(taskWords(task({ state: 'ready', reason: kept }), { paused: true, pausedWhy: prereq })).toEqual({ text: `${kept}; ready, scheduling is paused`, tone: 'warning' })
    // B's setup paused the run; A's attempt then could not be made usable either: A's words are its own, the pause B's.
    const b = 'attempt 1 could not be made usable at setup deps: exited 1: npm ERR! network'
    const a = 'attempt 1 could not be made usable at setup lint: exited 2'
    expect(taskWords(task({ id: 'a', state: 'ready', reason: a }), { paused: true, pausedWhy: b })).toEqual({ text: `${a}; ready, scheduling is paused`, tone: 'warning' })
    expect(taskWords(task({ id: 'b', state: 'ready', reason: b }), { paused: true, pausedWhy: b })).toEqual({ text: `ready, the dispatch is paused: ${b}`, tone: 'warning' })
  })
})

describe('taskActions', () => {
  const live = {}
  it('offers retry and accept on a failed task with a submitted commit, cancel on a live one, open and copy with an attempt', () => {
    expect(taskActions(task({ state: 'failed', attempt: { n: 3, state: 'failed', result: 'abc', sessionId: 's', branch: 'crew/r/build.3' } }), live)).toEqual({ retry: true, accept: true, cancel: false, open: true, copyBranch: true, showResult: false, preview: '' })
    expect(taskActions(task({ state: 'failed', attempt: { n: 3, state: 'failed' } }), live).accept).toBe(false)
    expect(taskActions(task({ state: 'failed', kind: 'review', attempt: { n: 1, state: 'failed', result: 'abc' } }), live).accept).toBe(false)
    expect(taskActions(task({ state: 'cancelled' }), live)).toMatchObject({ retry: true, accept: false, cancel: false })
    expect(taskActions(task({ state: 'running', attempt: { n: 1, state: 'running', sessionId: 's' } }), live)).toMatchObject({ retry: false, cancel: true, open: true })
    expect(taskActions(task({ state: 'succeeded', result: { attempt: 1, at: '' } }), live)).toMatchObject({ retry: false, cancel: false, showResult: true })
  })
  it('offers nothing that changes a stopped run, a record or a run continued', () => {
    expect(taskActions(task({ state: 'failed', attempt: { n: 1, state: 'failed', result: 'abc' } }), { stoppedAt: 'x' })).toMatchObject({ retry: false, accept: false, cancel: false })
    expect(taskActions(task({ state: 'running' }), { record: true })).toMatchObject({ cancel: false })
    // A finished run, continued: its continuation owns its tasks.
    expect(taskActions(task({ state: 'failed', attempt: { n: 1, state: 'failed', result: 'abc', sessionId: 's' } }), { resumedBy: 'next' })).toMatchObject({ retry: false, accept: false, cancel: false, open: true })
  })
  it("offers Open preview by the attempt's preview alone (EN1): the server sets it at tree.ready and clears it at the close", () => {
    const url = 'http://127.0.0.1:41234/'
    expect(taskActions(task({ state: 'running', attempt: { n: 1, state: 'running', preview: url } }), live).preview).toBe(url)
    expect(taskActions(task({ state: 'verifying', attempt: { n: 1, state: 'verifying', preview: url } }), live).preview).toBe(url)
    // No preview on the attempt: none offered, whatever the task's state.
    expect(taskActions(task({ state: 'running', attempt: { n: 1, state: 'running' } }), live).preview).toBe('')
    expect(taskActions(task({ state: 'succeeded', attempt: { n: 1, state: 'succeeded' }, result: { attempt: 1, at: '' } }), live).preview).toBe('')
    expect(taskActions(task({ state: 'ready' }), live).preview).toBe('')
    // Nor is it second-guessed: the run's other fields do not take it away or add it.
    expect(taskActions(task({ state: 'running', attempt: { n: 1, state: 'running', preview: url } }), { stoppedAt: 'x', record: true, resumedBy: 'n' }).preview).toBe(url)
  })
})

describe('stepWords and the receipt helpers (EN1)', () => {
  const at = '2026-10-31T10:00:00Z'
  const now = Date.parse('2026-10-31T10:02:30Z')
  const step = (p: Partial<RunStep>): RunStep => ({ phase: 'setup', id: 'deps', k: 1, state: 'finished', tries: 1, startedAt: at, logBytes: 10, ...p })
  it.each([
    [step({ exit: 0, duration: 4200 }), 'exited 0 in 4.2 s', 'success'],
    [step({ exit: 1, duration: 3000, error: 'exited 1: npm ERR!' }), 'exited 1 in 3.0 s', 'error'],
    [step({ exit: -1, timedOut: true, duration: 60_040 }), 'timed out after 60 s', 'error'],
    [step({ phase: 'check', id: 'unit', exit: 0, duration: 125_000 }), 'exited 0 in 2 min 5 s', 'success'],
    [step({ phase: 'prerequisite', id: 'node', exit: 127, duration: 12 }), 'exited 127 in 0.0 s', 'error'],
    [step({ exit: -1, error: 'could not start: no such file' }), 'failed after 0.0 s', 'error'],
    // A command a signal ended after it ran a while and wrote output (Codex round 3 on #502): it ran, so never "did not run".
    [step({ exit: -1, duration: 8300, logBytes: 2048, logSha256: 'ab'.repeat(32), error: 'setup deps was ended by a signal (killed)' }), 'failed after 8.3 s', 'error'],
    [step({ phase: 'check', id: 'unit', exit: -1, duration: 61_000, error: 'ended by a signal (terminated)' }), 'failed after 61 s', 'error'],
    [step({ phase: 'ready', id: 'up', exit: 0, tries: 3, duration: 4000 }), 'ready after 3 tries', 'success'],
    [step({ phase: 'ready', id: 'up', exit: 0, tries: 1, duration: 10 }), 'ready after 1 try', 'success'],
    [step({ phase: 'ready', id: 'up', exit: 7, tries: 30, timedOut: true, duration: 60_000 }), 'not ready after 30 tries', 'error'],
    // A command probe that exited 0 and failed (Codex round 2 on #502): a child it left holding its output.
    [step({ phase: 'ready', id: 'up', exit: 0, tries: 1, duration: 10, error: 'try 1: the command left a process holding its output' }), 'not ready after 1 try', 'error'],
    [step({ phase: 'ready', id: 'up', exit: -1, tries: 0 }), 'never tried', 'error'],
    [step({ phase: 'service', id: 'web', exit: -1, duration: 12_000 }), 'ended after 12 s', 'muted'],
    [step({ phase: 'service', id: 'web', exit: 0, duration: 2000, error: 'exited 0 before the tree was ready: a service must stay in the foreground' }), 'exited 0 after 2.0 s', 'error'],
    // A receipt that exited 0 and failed (the reviews of #502): a check or verify step, or a setup step in a result's checkout,
    // that changed tracked files.
    [step({ phase: 'check', id: 'unit', exit: 0, duration: 3000, error: 'changed tracked files: go.sum' }), 'failed in 3.0 s (exited 0)', 'error'],
    [step({ phase: 'verify', id: 'fmt', exit: 0, duration: 1500, error: 'changed tracked files: main.go' }), 'failed in 1.5 s (exited 0)', 'error'],
    [step({ phase: 'setup', id: 'deps', exit: 0, duration: 4200, error: 'setup deps changed tracked files: package-lock.json' }), 'failed in 4.2 s (exited 0)', 'error'],
    [step({ state: 'running' }), 'running since 2m ago', 'default'],
    [step({ phase: 'service', id: 'web', state: 'running' }), 'running since 2m ago', 'default'],
    [step({ state: 'started' }), 'starting', 'muted'],
    [step({ phase: 'cleanup', id: 'db', state: 'interrupted' }), 'interrupted', 'warning'],
  ] as const)('%o → %s', (s, text, tone) => {
    expect(stepWords(s, now)).toEqual({ text, tone })
  })
  it("says why the prerequisites failed when no receipt can: the run's pause words for a refused env name, none else", () => {
    const env = 'the prerequisites failed: the host has no NPM_TOKEN: set it where the server starts, start it again and continue the run'
    expect(prerequisitesRefused({ paused: true, pausedWhy: env })).toBe(env)
    expect(prerequisitesRefused({ paused: true, pausedWhy: "the prerequisites failed: NPM_TOKEN is not in the server's envPassthrough" })).toMatch(/envPassthrough$/)
    // A prerequisite command that failed has its receipt; a task's pause is not the prerequisites'; unpaused, nothing.
    expect(prerequisitesRefused({ paused: true, pausedWhy: 'the prerequisite tool failed: exited 1' })).toBe('')
    expect(prerequisitesRefused({ paused: true, pausedWhy: 'attempt 1 could not be made usable at setup deps: exited 1' })).toBe('')
    expect(prerequisitesRefused({ paused: false, pausedWhy: env })).toBe('')
    expect(prerequisitesRefused({ paused: true })).toBe('')
  })
  it('offers Log for a log kept, an empty one among them, and for one dropped; none for a receipt without one (Codex round 2 on #502)', () => {
    const empty = 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855'
    expect(stepHasLog(step({ exit: 0, logBytes: 0, logSha256: empty })), 'a quiet command: its empty log kept').toBe(true)
    expect(stepHasLog(step({ exit: 0, logBytes: 12, logSha256: 'ab'.repeat(32) }))).toBe(true)
    expect(stepHasLog(step({ exit: 0, logBytes: 2_000_000, logDropped: true, logSha256: 'cd'.repeat(32) })), 'dropped: Log says so').toBe(true)
    expect(stepHasLog(step({ phase: 'cleanup', id: 'db', state: 'interrupted', logBytes: 4, logSha256: 'ef'.repeat(32) }))).toBe(true)
    expect(stepHasLog(step({ phase: 'check', id: 'unit', state: 'interrupted', exit: -1, logBytes: 0, logSha256: undefined })), 'no log: the route answers 404').toBe(false)
    expect(stepHasLog(step({ state: 'running', logBytes: 0, logSha256: undefined }))).toBe(false)
    expect(stepHasLog(step({ state: 'started', logBytes: 0, logSha256: undefined }))).toBe(false)
  })
  it('says a duration in seconds, then minutes', () => {
    expect([stepDuration(0), stepDuration(9_949), stepDuration(10_000), stepDuration(119_400), stepDuration(120_000), stepDuration(3_605_000)]).toEqual(['0.0 s', '9.9 s', '10 s', '119 s', '2 min', '60 min 5 s'])
  })
  it('says the command: a probe over HTTP as GET and its URL, an argv with the words that need it quoted', () => {
    expect(stepCommand({ url: 'http://127.0.0.1:41234/health' })).toBe('GET http://127.0.0.1:41234/health')
    expect(stepCommand({ argv: ['npm', 'ci'] })).toBe('npm ci')
    expect(stepCommand({ argv: ['sh', '-c', 'echo "hi" > x', ''] })).toBe('sh -c "echo \\"hi\\" > x" ""')
    expect(stepCommand({})).toBe('')
  })
  it('names the manifest, where the step ran and its ports', () => {
    expect(stepManifest('crew:3')).toBe('crew:3')
    expect(stepManifest('file:0123456789abcdef0123456789abcdef01234567')).toBe('file: 0123456789ab')
    expect(stepManifest(undefined)).toBe('')
    const a = { worktree: '/r/.conductor/worktrees/run/build.1' }
    expect(stepWhere({ dir: '/r/.conductor/worktrees/run/build.1' }, a)).toBe('worktree')
    expect(stepWhere({ dir: '/r/.conductor/worktrees/run/build.1/app' }, a)).toBe('worktree')
    expect(stepWhere({ dir: '/r/.conductor/worktrees/run/build.1.check' }, a)).toBe('checkout')
    expect(stepWhere({ dir: '/r/.conductor/worktrees/run/build.1.check/app' }, a)).toBe('checkout')
    expect(stepWhere({ dir: '/r/.conductor/worktrees/run/build.10' }, a)).toBe('/r/.conductor/worktrees/run/build.10')
    expect(stepWhere({ dir: '/home/ann/src/app' })).toBe('~/src/app')
    expect(stepPorts({ web: 41234, db: 40112 })).toBe('web 41234, db 40112')
    expect(stepPorts(undefined)).toBe('')
  })
})

describe('commands with receipts', () => {
  const receipt = (p: Partial<Receipt>): Receipt => ({ commandId: 'x', intent: 'cancel', revision: 3, status: 'done', ...p })
  it('mints 16 hex characters, different each time', () => {
    const a = mintCommandId()
    const b = mintCommandId()
    expect(a).toMatch(/^[0-9a-f]{16}$/)
    expect(b).not.toBe(a)
  })
  it("mints a command's body: its id, and the revision it expects only when one is given", () => {
    const body = commandBody(7)
    expect(body.commandId).toMatch(/^[0-9a-f]{16}$/)
    expect(body.expectedRevision).toBe(7)
    expect(commandBody()).not.toHaveProperty('expectedRevision')
    expect(commandBody(0)).not.toHaveProperty('expectedRevision')
    expect(commandBody().commandId).not.toBe(body.commandId)
  })
  it('sends one id, carries the expected revision, and retries a lost reply once with the same id', async () => {
    const ids: string[] = []
    let calls = 0
    const r = await sendCommand(async (body: CommandBody) => {
      ids.push(body.commandId)
      expect(body.expectedRevision).toBe(7)
      if (++calls === 1) throw Object.assign(new Error('Cannot reach the server'), { status: 0 })
      return { receipt: receipt({ commandId: body.commandId, repeat: true }) }
    }, commandBody(7))
    expect(calls).toBe(2)
    expect(ids[0]).toBe(ids[1])
    expect(r.receipt.repeat).toBe(true)
    expect(r.receipt.commandId).toBe(ids[0])
  })
  it('gives up after a second lost reply, and leaves other errors as they are', async () => {
    await expect(sendCommand(async () => Promise.reject(Object.assign(new Error('Cannot reach the server'), { status: 0 })), commandBody())).rejects.toThrow('Cannot reach the server')
    await expect(sendCommand(async () => Promise.reject(Object.assign(new Error('stale'), { status: 409, code: 'stale_revision' })), commandBody())).rejects.toThrow('stale')
  })
  it('turns a refusal with a receipt into its words', async () => {
    const failed = receipt({ status: 'failed', error: 'the task is not in a state for that: it is running', refusal: 'task_state' })
    const err = await sendCommand(async () => Promise.reject(Object.assign(new Error('refused'), { status: 409, body: { error: { code: 'task_state', message: failed.error }, receipt: failed } })), commandBody()).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(CommandRefused)
    expect((err as CommandRefused).message).toBe('the task is not in a state for that: it is running')
    expect((err as CommandRefused).receipt.refusal).toBe('task_state')
  })
})

// The same sender takes a Dispatch operation's body (docs/plans/dispatch.md, useDispatchOps.ts): the op id it carries, its expected
// revision and display name sent as they are, the whole reply (the receipt and the entity) handed back, a lost reply asked for once
// more with the same op id, and a refusal (a 409, or a 404 not_found, carrying the receipt) read from the receipt.
describe('a Dispatch operation sent as a command', () => {
  /** A Dispatch operation's body and receipt (docs/protocol.md, Dispatch operations). */
  interface OpBody {
    opId: string
    expectedRev?: number
    byName?: string
  }
  interface DispatchReceipt {
    opId: string
    intent: string
    status: 'done' | 'failed'
    rev?: number
    error?: string
    refusal?: string
    repeat?: boolean
    missing?: string[]
  }
  const op = (p: Partial<DispatchReceipt>): DispatchReceipt => ({ opId: 'op1', intent: 'ticket.edit', status: 'done', rev: 4, ...p })
  it('sends the op body as it is, retries a lost reply with the same op id, and hands back the whole reply', async () => {
    const sent: OpBody[] = []
    const body: OpBody = { opId: mintCommandId(), expectedRev: 3, byName: 'Nate' }
    const reply = await sendCommand(async (b: OpBody) => {
      sent.push({ ...b })
      if (sent.length === 1) throw Object.assign(new Error('Cannot reach the server'), { status: 0 })
      return { receipt: op({ opId: b.opId, repeat: true }), ticket: { key: 'CND-12', rev: 4 } }
    }, body)
    expect(sent).toHaveLength(2)
    expect(sent[0]).toEqual(body)
    expect(sent[1]).toEqual(body)
    expect(reply.receipt.opId).toBe(body.opId)
    expect(reply.receipt.repeat).toBe(true)
    expect(reply.ticket).toEqual({ key: 'CND-12', rev: 4 })
  })
  it("reads a refusal from the operation's receipt, a 404 not_found's among them", async () => {
    for (const [status, refusal, words] of [
      [409, 'stale_revision', 'CND-12 is at revision 5, not 3'],
      [409, 'close_policy', "CND-12 lacks the owner's acceptance"],
      [404, 'not_found', 'CND-12 is deleted'],
    ] as const) {
      const failed = op({ status: 'failed', refusal, error: words, rev: undefined, missing: refusal === 'close_policy' ? ['acceptance'] : undefined })
      const err = await sendCommand(async () => Promise.reject(Object.assign(new Error('refused'), { status, body: { error: { code: refusal, message: words }, receipt: failed } })), { opId: 'op1' } as OpBody).catch((e: unknown) => e)
      expect(err).toBeInstanceOf(CommandRefused)
      expect((err as CommandRefused<DispatchReceipt>).message).toBe(words)
      expect((err as CommandRefused<DispatchReceipt>).receipt.refusal).toBe(refusal)
    }
  })
})
